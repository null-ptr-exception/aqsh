package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/hibiken/asynq"
	"github.com/rophy/aqsh/internal/worker"
)

// executionInfo checks the recorded owner before exposing metadata, results or logs.
// The caller comes from the same verified context or trusted proxy as submission.
func (s *Server) executionInfo(w http.ResponseWriter, r *http.Request) *asynq.TaskInfo {
	identity, _ := s.caller(r)
	if s.cfg.ExecutionOwnerOnly && strings.TrimSpace(identity) == "" {
		s.jsonError(w, http.StatusUnauthorized, "identity header required")
		return nil
	}
	queues := append([]string{"default"}, s.cfg.WorkerQueues...)
	// API-only processes may submit to queues they do not consume themselves.
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
		if s.cfg.ExecutionOwnerOnly {
			var payload worker.TaskPayload
			if info.Type != worker.TaskType || json.Unmarshal(info.Payload, &payload) != nil ||
				strings.TrimSpace(payload.Identity) == "" || payload.Identity != identity {
				break
			}
		}
		return info
	}
	// Other owners, missing records and ownerless legacy records look identical.
	s.jsonError(w, http.StatusNotFound, "task not found")
	return nil
}
