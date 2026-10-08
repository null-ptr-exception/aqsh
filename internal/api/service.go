package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/rophy/aqsh/internal/tasks"
	"github.com/rophy/aqsh/internal/worker"
)

type principal struct {
	Identity string
	Groups   string
}

type serviceError struct {
	Status  int
	Message string
}

func (e *serviceError) Error() string { return e.Message }

func failure(status int, message string) error {
	return &serviceError{Status: status, Message: message}
}

func (s *Server) requestPrincipal(r *http.Request) principal {
	return principal{
		Identity: strings.TrimSpace(r.Header.Get(s.cfg.IdentityHeader)),
		Groups:   r.Header.Get(s.cfg.GroupsHeader),
	}
}

func canUseTask(p principal, task *tasks.ResolvedTask) bool {
	return len(task.AllowedUsers) == 0 && len(task.AllowedGroups) == 0 ||
		isAllowedUser(p.Identity, task.AllowedUsers) ||
		hasAnyGroup(splitGroups(p.Groups), task.AllowedGroups)
}

func (s *Server) authorizeTask(name string, p principal) (*tasks.ResolvedTask, error) {
	if (s.cfg.RequireIdentity || s.cfg.ExecutionAccess == "owner") && p.Identity == "" {
		return nil, failure(http.StatusUnauthorized, "identity header required")
	}
	task, err := s.tasks.Resolve(name)
	if err != nil {
		return nil, failure(http.StatusNotFound, err.Error())
	}
	if !canUseTask(p, task) {
		return nil, failure(http.StatusForbidden, "not authorized for this task")
	}
	return task, nil
}

func (s *Server) authorizeValues(ctx context.Context, task *tasks.ResolvedTask, p principal, payload map[string]any) error {
	inputValues := make(map[string]string)
	for _, input := range task.Input {
		if value, ok := payload[input.Name].(string); ok {
			inputValues[input.Name] = value
		}
		if input.ValuesURL == "" {
			continue
		}
		value := payload[input.Name]
		submitted := ""
		if value != nil {
			var ok bool
			submitted, ok = value.(string)
			if !ok {
				return failure(http.StatusBadRequest, fmt.Sprintf("field %q must be a string for values_url validation", input.Name))
			}
		}
		fetchURL := substituteURL(input.ValuesURL, p.Identity, p.Groups, task.Name, inputValues)
		if strings.Contains(fetchURL, "${input.") {
			return failure(http.StatusBadRequest, fmt.Sprintf("field %q depends on an unresolved input parameter", input.Name))
		}
		allowed, err := s.fetchOrCachedValues(ctx, valuesCacheKey(p, task.Name, input.Name, fetchURL), fetchURL, input.ValuesCache)
		if err != nil {
			status := http.StatusBadGateway
			if strings.Contains(err.Error(), "timeout") {
				status = http.StatusGatewayTimeout
			}
			return failure(status, fmt.Sprintf("failed to fetch allowed values for field %q", input.Name))
		}
		if !isValueAllowed(submitted, allowed) {
			return failure(http.StatusForbidden, fmt.Sprintf("value is not allowed for field %q", input.Name))
		}
	}
	return nil
}

func valuesCacheKey(p principal, task, input, fetchURL string) string {
	key, _ := json.Marshal([]string{p.Identity, p.Groups, task, input, fetchURL})
	return string(key)
}

type submission struct {
	ID     string `json:"id"`
	Queue  string `json:"queue"`
	Status string `json:"status"`
}

func (s *Server) submit(ctx context.Context, name string, p principal, payload map[string]any) (*submission, error) {
	task, err := s.authorizeTask(name, p)
	if err != nil {
		return nil, err
	}
	payload, err = s.tasks.NormalizePayload(name, payload)
	if err != nil {
		return nil, failure(http.StatusBadRequest, err.Error())
	}
	env, err := s.tasks.ValidatePayload(name, payload)
	if err != nil {
		return nil, failure(http.StatusBadRequest, err.Error())
	}
	if err := s.authorizeValues(ctx, task, p, payload); err != nil {
		return nil, err
	}
	data, err := json.Marshal(worker.TaskPayload{
		Name: name, CreatedAt: time.Now(), Identity: p.Identity, Groups: p.Groups,
		Env: env, Payload: payload,
	})
	if err != nil {
		return nil, failure(http.StatusBadRequest, "invalid task payload")
	}
	job := asynq.NewTask(worker.TaskType, data,
		asynq.Queue(task.Queue), asynq.Timeout(task.Timeout),
		asynq.MaxRetry(task.MaxRetry), asynq.Retention(s.cfg.ResultRetention),
	)
	info, err := s.client.EnqueueContext(ctx, job)
	if err != nil {
		return nil, failure(http.StatusServiceUnavailable, "failed to enqueue task")
	}
	slog.InfoContext(ctx, "task submitted", "identity", p.Identity, "task", name, "execution_id", info.ID)
	return &submission{ID: info.ID, Queue: info.Queue, Status: "pending"}, nil
}

// taskQueues includes task-specific queues even if this API pod has no worker
// consuming them. Queue selection never comes from tool arguments.
func (s *Server) taskQueues() []string {
	queues := map[string]bool{"default": true}
	for _, queue := range s.cfg.WorkerQueues {
		queues[queue] = true
	}
	if s.tasks != nil {
		for name := range s.tasks.Tasks {
			if task, err := s.tasks.Resolve(name); err == nil {
				queues[task.Queue] = true
			}
		}
	}
	names := make([]string, 0, len(queues))
	for name := range queues {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (s *Server) readExecution(ctx context.Context, id string, p principal, viaMCP bool) (*asynq.TaskInfo, *tasks.ResolvedTask, error) {
	secured := s.cfg.ExecutionAccess == "owner" || viaMCP
	if secured && p.Identity == "" {
		return nil, nil, failure(http.StatusUnauthorized, "identity header required")
	}
	var info *asynq.TaskInfo
	for _, queue := range s.taskQueues() {
		var err error
		info, err = s.inspector.GetTaskInfo(queue, id)
		if err == nil {
			break
		}
		if !errors.Is(err, asynq.ErrTaskNotFound) && !errors.Is(err, asynq.ErrQueueNotFound) {
			return nil, nil, failure(http.StatusServiceUnavailable, "execution lookup unavailable")
		}
		info = nil
	}
	notFound := failure(http.StatusNotFound, "execution not found")
	if info == nil {
		return nil, nil, notFound
	}
	if !secured {
		return info, nil, nil
	}
	var payload worker.TaskPayload
	if info.Type != worker.TaskType || json.Unmarshal(info.Payload, &payload) != nil ||
		payload.Identity == "" || payload.Identity != p.Identity {
		return nil, nil, notFound
	}
	task, err := s.authorizeTask(payload.Name, p)
	if err != nil || viaMCP && (task.MCP == nil || !task.MCP.Enabled || !task.MCP.ReadOnly) {
		return nil, nil, notFound
	}
	// Use the actual stored environment for string target/dependency values.
	// Reapplying today's defaults could authorize a different target.
	arguments := make(map[string]any, len(task.Input))
	for _, input := range task.Input {
		value := payload.Payload[input.Name]
		if input.Type == "" || input.Type == "string" {
			if env, ok := payload.Env[input.Env]; input.Env != "" && ok {
				value = env
			}
		}
		if value != nil {
			arguments[input.Name] = value
		}
	}
	if err := s.authorizeValues(ctx, task, p, arguments); err != nil {
		var e *serviceError
		if errors.As(err, &e) && e.Status < 500 {
			return nil, nil, notFound
		}
		return nil, nil, err
	}
	return info, task, nil
}

func (s *Server) writeServiceError(w http.ResponseWriter, err error) {
	var e *serviceError
	if errors.As(err, &e) {
		s.jsonError(w, e.Status, e.Message)
		return
	}
	s.jsonError(w, http.StatusInternalServerError, "internal error")
}
