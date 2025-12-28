package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	asynqmetrics "github.com/hibiken/asynq/x/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"github.com/rophy/aqsh/internal/config"
	"github.com/rophy/aqsh/internal/hooks"
	"github.com/rophy/aqsh/internal/logs"
	"github.com/rophy/aqsh/internal/worker"
)

type Server struct {
	cfg       *config.Config
	hooks     *hooks.HooksConfig
	client    *asynq.Client
	inspector *asynq.Inspector
	logStream *logs.LogStreamer
	rdb       redis.UniversalClient
}

func New(cfg *config.Config, hooksConfig *hooks.HooksConfig, rdb redis.UniversalClient, asynqOpt asynq.RedisConnOpt) *Server {
	return &Server{
		cfg:       cfg,
		hooks:     hooksConfig,
		client:    asynq.NewClient(asynqOpt),
		inspector: asynq.NewInspector(asynqOpt),
		logStream: logs.NewLogStreamer(rdb, cfg.LogRetention),
		rdb:       rdb,
	}
}

func (s *Server) Run(ctx context.Context) error {
	// Ensure all configured queues are registered in Redis
	// This is needed for Inspector.GetTaskInfo to work
	for _, q := range s.cfg.WorkerQueues {
		s.rdb.SAdd(ctx, "asynq:queues", q)
	}
	s.rdb.SAdd(ctx, "asynq:queues", "default")

	// Register asynq queue metrics collector
	queueMetrics := asynqmetrics.NewQueueMetricsCollector(s.inspector)
	prometheus.MustRegister(queueMetrics)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /jobs/{hook}", s.handleSubmitJob)
	mux.HandleFunc("GET /jobs/{id}", s.handleGetJob)
	mux.HandleFunc("GET /jobs/{id}/logs", s.handleGetLogs)
	mux.HandleFunc("GET /hooks", s.handleListHooks)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.Handle("GET /metrics", promhttp.Handler())

	srv := &http.Server{
		Addr:    s.cfg.Bind,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Printf("API server listening on %s", s.cfg.Bind)
	return srv.ListenAndServe()
}

func (s *Server) handleSubmitJob(w http.ResponseWriter, r *http.Request) {
	hookName := r.PathValue("hook")

	hook, err := s.hooks.Resolve(hookName)
	if err != nil {
		s.jsonError(w, http.StatusNotFound, err.Error())
		return
	}

	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		s.jsonError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	env, err := s.hooks.ValidatePayload(hookName, payload)
	if err != nil {
		s.jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	taskPayload := worker.TaskPayload{
		Hook:    hookName,
		Env:     env,
		Payload: payload,
	}
	taskBytes, _ := json.Marshal(taskPayload)

	task := asynq.NewTask(worker.TaskType, taskBytes,
		asynq.Queue(hook.Queue),
		asynq.Timeout(hook.Timeout),
		asynq.MaxRetry(hook.MaxRetry),
		asynq.Retention(s.cfg.ResultRetention),
	)

	info, err := s.client.Enqueue(task)
	if err != nil {
		s.jsonError(w, http.StatusServiceUnavailable, "failed to enqueue job: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusAccepted, map[string]any{
		"id":     info.ID,
		"hook":   hookName,
		"queue":  info.Queue,
		"status": "pending",
	})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")

	// Try all queues the worker is configured to process
	allQueues := append([]string{"default"}, s.cfg.WorkerQueues...)
	seen := make(map[string]bool)

	var info *asynq.TaskInfo
	var err error

	for _, q := range allQueues {
		if seen[q] {
			continue
		}
		seen[q] = true
		info, err = s.inspector.GetTaskInfo(q, taskID)
		if err == nil {
			break
		}
	}

	if err != nil {
		s.jsonError(w, http.StatusNotFound, "job not found")
		return
	}

	status := stateToStatus(info.State)

	resp := map[string]any{
		"id":         info.ID,
		"queue":      info.Queue,
		"status":     status,
		"retried":    info.Retried,
		"max_retry":  info.MaxRetry,
		"created_at": info.NextProcessAt.Format(time.RFC3339),
	}

	if info.State == asynq.TaskStateCompleted || info.State == asynq.TaskStateArchived {
		resp["completed_at"] = info.CompletedAt.Format(time.RFC3339)
		if len(info.Result) > 0 {
			var result worker.TaskResult
			if err := json.Unmarshal(info.Result, &result); err == nil {
				resp["result"] = result
			}
		}
	}

	if info.LastErr != "" {
		resp["last_error"] = info.LastErr
	}

	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleGetLogs(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	follow := r.URL.Query().Get("follow") != "false"

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.jsonError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	ctx := r.Context()
	lastID := r.Header.Get("Last-Event-ID")
	if lastID == "" {
		lastID = "0"
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		blockTime := time.Duration(0)
		if follow {
			blockTime = 5 * time.Second
		}

		entries, err := s.logStream.Read(ctx, taskID, lastID, blockTime)
		if err != nil {
			log.Printf("Error reading logs: %v", err)
			return
		}

		for _, entry := range entries {
			if entry.EOF {
				fmt.Fprintf(w, "event: eof\ndata: done\n\n")
				flusher.Flush()
				return
			}
			fmt.Fprintf(w, "id: %s\ndata: %s\n\n", entry.ID, entry.Line)
			lastID = entry.ID
		}
		flusher.Flush()

		if !follow && len(entries) == 0 {
			return
		}
	}
}

func (s *Server) handleListHooks(w http.ResponseWriter, r *http.Request) {
	result := make(map[string]any)
	for name := range s.hooks.Hooks {
		hook, _ := s.hooks.Resolve(name)
		inputs := make([]map[string]any, 0, len(hook.Input))
		for _, input := range hook.Input {
			m := map[string]any{
				"name":     input.Name,
				"env":      input.Env,
				"required": input.Required,
				"type":     input.Type,
			}
			if input.Pattern != "" {
				m["pattern"] = input.Pattern
			}
			if len(input.Enum) > 0 {
				m["enum"] = input.Enum
			}
			if input.Min != nil {
				m["min"] = *input.Min
			}
			if input.Max != nil {
				m["max"] = *input.Max
			}
			if input.Default != "" {
				m["default"] = input.Default
			}
			if input.Description != "" {
				m["description"] = input.Description
			}
			inputs = append(inputs, m)
		}

		result[name] = map[string]any{
			"description": hook.Description,
			"timeout":     hook.Timeout.String(),
			"max_retry":   hook.MaxRetry,
			"queue":       hook.Queue,
			"input":       inputs,
		}
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{"hooks": result})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	redisStatus := "connected"
	if err := s.rdb.Ping(ctx).Err(); err != nil {
		redisStatus = "error: " + err.Error()
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status": "healthy",
		"redis":  redisStatus,
		"mode":   s.cfg.Mode,
	})
}

func (s *Server) jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (s *Server) jsonError(w http.ResponseWriter, status int, message string) {
	s.jsonResponse(w, status, map[string]string{"error": message})
}

func stateToStatus(state asynq.TaskState) string {
	switch state {
	case asynq.TaskStatePending:
		return "pending"
	case asynq.TaskStateActive:
		return "running"
	case asynq.TaskStateCompleted:
		return "completed"
	case asynq.TaskStateRetry:
		return "retrying"
	case asynq.TaskStateArchived:
		return "failed"
	case asynq.TaskStateScheduled:
		return "scheduled"
	default:
		return strings.ToLower(state.String())
	}
}
