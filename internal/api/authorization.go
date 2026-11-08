package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/hibiken/asynq"
	"github.com/rophy/aqsh/internal/auth"
	"github.com/rophy/aqsh/internal/worker"
)

func (s *Server) allows(r *http.Request, task, verb string) bool {
	if !s.tasks.RBACEnabled() {
		return true
	}
	identity, groups := s.caller(r)
	verifiedGroups := splitGroups(groups)
	if p, ok := auth.FromContext(r.Context()); ok {
		verifiedGroups = p.Groups
	}
	return s.tasks.Allows(task, verb, identity, verifiedGroups)
}

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
		for name := range s.tasks.Tasks {
			if task, err := s.tasks.Resolve(name); err == nil {
				queues = append(queues, task.Queue)
			}
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
			s.jsonError(w, http.StatusServiceUnavailable, "execution lookup unavailable")
			return nil
		}
		if s.tasks.RBACEnabled() {
			var payload worker.TaskPayload
			if info.Type != worker.TaskType || json.Unmarshal(info.Payload, &payload) != nil || strings.TrimSpace(payload.Name) == "" {
				break
			}
			identity, _ := s.caller(r)
			owner := strings.TrimSpace(identity) != "" && payload.Identity == identity
			if !s.allows(r, payload.Name, "read") && !(owner && s.allows(r, payload.Name, "run")) {
				break
			}
		}
		return info
	}
	// Do not reveal whether an execution exists to an unauthorized caller.
	s.jsonError(w, http.StatusNotFound, "task not found")
	return nil
}
