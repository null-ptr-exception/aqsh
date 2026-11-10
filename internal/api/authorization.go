package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/hibiken/asynq"
	"github.com/rophy/aqsh/internal/auth"
	"github.com/rophy/aqsh/internal/worker"
)

// allows is the authorization entry point for both legacy ACLs and RBAC.
// Legacy reads are unrestricted, including logs whose execution has expired.
// For read, an empty task asks whether access is unrestricted by task name.
func (s *Server) allows(r *http.Request, task, verb string) bool {
	identity, groups := s.caller(r)
	verifiedGroups := splitGroups(groups)
	if p, ok := auth.FromContext(r.Context()); ok {
		verifiedGroups = p.Groups
	}
	if !s.tasks.RBACEnabled() {
		switch verb {
		case "read":
			return true
		case "run":
			// Handlers check task existence separately, preserving legacy 404s.
			def := s.tasks.Tasks[task]
			return len(def.AllowedUsers) == 0 && len(def.AllowedGroups) == 0 ||
				isAllowedUser(identity, def.AllowedUsers) || hasAnyGroup(verifiedGroups, def.AllowedGroups)
		default:
			return false
		}
	}
	if strings.TrimSpace(task) == "" {
		return false
	}
	return s.tasks.Allows(task, verb, identity, verifiedGroups)
}

// requireRBACIdentity rejects anonymous requests except for public probes.
func (s *Server) requireRBACIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/health" && r.URL.Path != "/metrics" {
			identity, _ := s.caller(r)
			if strings.TrimSpace(identity) == "" {
				s.jsonError(w, http.StatusUnauthorized, "caller identity required")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// executionInfo authorizes metadata, results and logs from the original queued
// task name/identity. It never relies on the task definition still existing.
func (s *Server) executionInfo(w http.ResponseWriter, r *http.Request) *asynq.TaskInfo {
	queues := append([]string{"default"}, s.cfg.WorkerQueues...)
	if s.tasks != nil {
		queues = append(queues, s.tasks.Queue(""))
		for name := range s.tasks.Tasks {
			queues = append(queues, s.tasks.Queue(name))
		}
	}
	seen := make(map[string]bool)
	for _, queue := range queues {
		if seen[queue] {
			continue
		}
		seen[queue] = true
		info, err := s.inspector.GetTaskInfo(queue, r.PathValue("id"))
		if errors.Is(err, asynq.ErrTaskNotFound) || errors.Is(err, asynq.ErrQueueNotFound) {
			continue
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "execution lookup failed", "task_id", r.PathValue("id"), "queue", queue, "error", err)
			s.jsonError(w, http.StatusServiceUnavailable, "execution lookup unavailable")
			return nil
		}
		var payload worker.TaskPayload
		if info.Type != worker.TaskType || json.Unmarshal(info.Payload, &payload) != nil || strings.TrimSpace(payload.Name) == "" {
			payload = worker.TaskPayload{}
		}
		identity, _ := s.caller(r)
		owner := strings.TrimSpace(identity) != "" && payload.Identity == identity
		if !s.allows(r, payload.Name, "read") && !(owner && s.allows(r, payload.Name, "run")) {
			break
		}
		return info
	}
	// Do not reveal whether an execution exists to an unauthorized caller.
	s.jsonError(w, http.StatusNotFound, "task not found")
	return nil
}
