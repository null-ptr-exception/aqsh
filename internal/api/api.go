package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime/coverage"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	asynqmetrics "github.com/hibiken/asynq/x/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"github.com/rophy/aqsh/internal/config"
	"github.com/rophy/aqsh/internal/logs"
	"github.com/rophy/aqsh/internal/tasks"
	"github.com/rophy/aqsh/internal/worker"
)

type Server struct {
	cfg        *config.Config
	tasks      *tasks.TasksConfig
	client     *asynq.Client
	inspector  *asynq.Inspector
	logStream  *logs.LogStreamer
	rdb        redis.UniversalClient
	version    string
	valCache   *valuesCache
}

func New(cfg *config.Config, tasksConfig *tasks.TasksConfig, rdb redis.UniversalClient, asynqOpt asynq.RedisConnOpt, version string) *Server {
	return &Server{
		cfg:       cfg,
		tasks:     tasksConfig,
		client:    asynq.NewClient(asynqOpt),
		inspector: asynq.NewInspector(asynqOpt),
		logStream: logs.NewLogStreamer(rdb, cfg.LogRetention),
		rdb:       rdb,
		version:   version,
		valCache:  newValuesCache(),
	}
}

type statusResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		attrs := []slog.Attr{
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", sw.status),
			slog.String("duration", time.Since(start).String()),
		}
		if identity := r.Header.Get(s.cfg.IdentityHeader); identity != "" {
			attrs = append(attrs, slog.String("user", identity))
		}
		if groups := r.Header.Get(s.cfg.GroupsHeader); groups != "" {
			attrs = append(attrs, slog.String("groups", groups))
		}
		level := slog.LevelInfo
		if r.URL.Path == "/health" || r.URL.Path == "/metrics" {
			level = slog.LevelDebug
		}
		slog.LogAttrs(r.Context(), level, "http request", attrs...)
	})
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
	mux.HandleFunc("POST /tasks/{name}", s.handleSubmitTask)
	mux.HandleFunc("GET /tasks/{name}", s.handleGetTaskDef)
	mux.HandleFunc("GET /tasks", s.handleListTasks)
	mux.HandleFunc("GET /executions/{id}", s.handleGetExecution)
	mux.HandleFunc("GET /executions/{id}/logs", s.handleGetLogs)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.Handle("GET /metrics", promhttp.Handler())

	// Coverage endpoint - only available when GOCOVERDIR is set
	if coverDir := os.Getenv("GOCOVERDIR"); coverDir != "" {
		mux.HandleFunc("POST /debug/coverage/flush", s.handleCoverageFlush)
		slog.Info("coverage endpoint enabled", "cover_dir", coverDir)
	}

	srv := &http.Server{
		Addr:    s.cfg.Bind,
		Handler: s.accessLog(mux),
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	slog.Info("API server listening", "addr", s.cfg.Bind)
	return srv.ListenAndServe()
}

func (s *Server) handleSubmitTask(w http.ResponseWriter, r *http.Request) {
	taskName := r.PathValue("name")

	identity := r.Header.Get(s.cfg.IdentityHeader)
	if s.cfg.RequireIdentity && identity == "" {
		s.jsonError(w, http.StatusUnauthorized, "identity header required")
		return
	}

	taskDef, err := s.tasks.Resolve(taskName)
	if err != nil {
		s.jsonError(w, http.StatusNotFound, err.Error())
		return
	}

	// Authorization: allowed_users OR allowed_groups
	groups := r.Header.Get(s.cfg.GroupsHeader)
	if len(taskDef.AllowedUsers) > 0 || len(taskDef.AllowedGroups) > 0 {
		userOK := len(taskDef.AllowedUsers) > 0 && isAllowedUser(identity, taskDef.AllowedUsers)
		groupOK := len(taskDef.AllowedGroups) > 0 && hasAnyGroup(splitGroups(groups), taskDef.AllowedGroups)
		if !userOK && !groupOK {
			s.jsonError(w, http.StatusForbidden, "not authorized for this task")
			return
		}
	}

	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		s.jsonError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Validate values_url inputs against remote allowed values (in order, for cascading)
	inputValues := make(map[string]string)
	for _, input := range taskDef.Input {
		if v, ok := payload[input.Name]; ok && v != nil {
			if s, ok := v.(string); ok {
				inputValues[input.Name] = s
			}
		}
		if input.ValuesURL == "" {
			continue
		}
		submitted, ok := payload[input.Name]
		if !ok || submitted == nil {
			continue
		}
		submittedStr, ok := submitted.(string)
		if !ok {
			s.jsonError(w, http.StatusBadRequest, fmt.Sprintf("field %q must be a string for values_url validation", input.Name))
			return
		}
		fetchURL := substituteURL(input.ValuesURL, identity, groups, taskName, inputValues)
		allowed, err := s.fetchOrCachedValues(r.Context(), fetchURL, input.ValuesCache)
		if err != nil {
			if strings.Contains(err.Error(), "timeout") {
				s.jsonError(w, http.StatusGatewayTimeout, fmt.Sprintf("timeout fetching allowed values for %q", input.Name))
			} else {
				s.jsonError(w, http.StatusBadGateway, fmt.Sprintf("error fetching allowed values for %q: %s", input.Name, err.Error()))
			}
			return
		}
		if !isValueAllowed(submittedStr, allowed) {
			s.jsonError(w, http.StatusForbidden, fmt.Sprintf("value %q is not allowed for field %q", submittedStr, input.Name))
			return
		}
	}

	env, err := s.tasks.ValidatePayload(taskName, payload)
	if err != nil {
		s.jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	taskPayload := worker.TaskPayload{
		Name:      taskName,
		CreatedAt: time.Now(),
		Identity:  identity,
		Groups:    groups,
		Env:       env,
		Payload:   payload,
	}
	taskBytes, _ := json.Marshal(taskPayload)

	task := asynq.NewTask(worker.TaskType, taskBytes,
		asynq.Queue(taskDef.Queue),
		asynq.Timeout(taskDef.Timeout),
		asynq.MaxRetry(taskDef.MaxRetry),
		asynq.Retention(s.cfg.ResultRetention),
	)

	info, err := s.client.Enqueue(task)
	if err != nil {
		s.jsonError(w, http.StatusServiceUnavailable, "failed to enqueue task: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusAccepted, map[string]any{
		"id":     info.ID,
		"queue":  info.Queue,
		"status": "pending",
	})
}

func (s *Server) handleGetTaskDef(w http.ResponseWriter, r *http.Request) {
	taskName := r.PathValue("name")

	taskDef, err := s.tasks.Resolve(taskName)
	if err != nil {
		s.jsonError(w, http.StatusNotFound, err.Error())
		return
	}

	result := serializeTaskDef(taskDef)

	identity := r.Header.Get(s.cfg.IdentityHeader)
	groups := r.Header.Get(s.cfg.GroupsHeader)
	if identity != "" {
		inputs := result["input"].([]map[string]any)
		for _, m := range inputs {
			valuesURL, ok := m["values_url"]
			if !ok || valuesURL != true {
				continue
			}
			input := findInput(taskDef.Input, m["name"].(string))
			if input == nil {
				continue
			}
			fetchURL := substituteURL(input.ValuesURL, identity, groups, taskName, nil)
			if strings.Contains(fetchURL, "${input.") {
				continue
			}
			allowed, err := s.fetchOrCachedValues(r.Context(), fetchURL, input.ValuesCache)
			if err != nil {
				continue
			}
			values := make([]map[string]string, len(allowed))
			for i, av := range allowed {
				values[i] = map[string]string{"name": av.Name, "value": av.Value}
			}
			m["values"] = values
		}
	}

	s.jsonResponse(w, http.StatusOK, result)
}

func (s *Server) handleGetExecution(w http.ResponseWriter, r *http.Request) {
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
		s.jsonError(w, http.StatusNotFound, "task not found")
		return
	}

	status := stateToStatus(info.State)

	resp := map[string]any{
		"id":        info.ID,
		"queue":     info.Queue,
		"status":    status,
		"retried":   info.Retried,
		"max_retry": info.MaxRetry,
	}

	// Get created_at and identity from task payload
	var payload worker.TaskPayload
	if err := json.Unmarshal(info.Payload, &payload); err == nil {
		if !payload.CreatedAt.IsZero() {
			resp["created_at"] = payload.CreatedAt.Format(time.RFC3339)
		}
		if payload.Identity != "" {
			resp["identity"] = payload.Identity
		}
		if payload.Groups != "" {
			resp["groups"] = payload.Groups
		}
	}

	// Get started_at from Redis metadata
	ctx := r.Context()
	metaKey := worker.MetaKeyPrefix + taskID
	if startedAtMs, err := s.rdb.HGet(ctx, metaKey, "started_at").Int64(); err == nil {
		resp["started_at"] = time.UnixMilli(startedAtMs).Format(time.RFC3339)
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
			slog.Error("error reading logs", "task_id", taskID, "error", err)
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

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	result := make(map[string]any)
	for name := range s.tasks.Tasks {
		taskDef, _ := s.tasks.Resolve(name)
		result[name] = map[string]any{
			"description": taskDef.Description,
		}
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{"tasks": result})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	redisStatus := "connected"
	if err := s.rdb.Ping(ctx).Err(); err != nil {
		redisStatus = "error: " + err.Error()
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":  "healthy",
		"version": s.version,
		"redis":   redisStatus,
		"mode":    s.cfg.Mode,
	})
}

func (s *Server) handleCoverageFlush(w http.ResponseWriter, r *http.Request) {
	coverDir := os.Getenv("GOCOVERDIR")
	if coverDir == "" {
		s.jsonError(w, http.StatusBadRequest, "GOCOVERDIR not set")
		return
	}

	if err := coverage.WriteCountersDir(coverDir); err != nil {
		s.jsonError(w, http.StatusInternalServerError, "failed to write coverage: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"status":    "flushed",
		"cover_dir": coverDir,
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

func (s *Server) fetchOrCachedValues(ctx context.Context, url string, cacheDuration string) ([]AllowedValue, error) {
	ttl, _ := time.ParseDuration(cacheDuration)
	if ttl > 0 && s.valCache != nil {
		if cached, ok := s.valCache.get(url); ok {
			return cached, nil
		}
	}

	values, err := fetchAllowedValues(ctx, url)
	if err != nil {
		return nil, err
	}

	if ttl > 0 && s.valCache != nil {
		s.valCache.set(url, values, ttl)
	}

	return values, nil
}

func findInput(inputs []tasks.Input, name string) *tasks.Input {
	for i := range inputs {
		if inputs[i].Name == name {
			return &inputs[i]
		}
	}
	return nil
}

func splitGroups(header string) []string {
	if header == "" {
		return nil
	}
	parts := strings.Split(header, ",")
	groups := make([]string, 0, len(parts))
	for _, p := range parts {
		if g := strings.TrimSpace(p); g != "" {
			groups = append(groups, g)
		}
	}
	return groups
}

func isAllowedUser(identity string, allowedUsers []string) bool {
	if identity == "" {
		return false
	}
	for _, u := range allowedUsers {
		if u != "" && identity == u {
			return true
		}
	}
	return false
}

func hasAnyGroup(userGroups, allowedGroups []string) bool {
	for _, ug := range userGroups {
		for _, ag := range allowedGroups {
			if ug == ag {
				return true
			}
		}
	}
	return false
}

func serializeTaskDef(taskDef *tasks.ResolvedTask) map[string]any {
	inputs := make([]map[string]any, 0, len(taskDef.Input))
	for _, input := range taskDef.Input {
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
		if input.ValuesURL != "" {
			m["values_url"] = true
		}
		inputs = append(inputs, m)
	}

	info := map[string]any{
		"description": taskDef.Description,
		"timeout":     taskDef.Timeout.String(),
		"max_retry":   taskDef.MaxRetry,
		"queue":       taskDef.Queue,
		"input":       inputs,
	}
	if len(taskDef.AllowedGroups) > 0 {
		info["allowed_groups"] = taskDef.AllowedGroups
	}
	if len(taskDef.AllowedUsers) > 0 {
		info["allowed_users"] = taskDef.AllowedUsers
	}
	return info
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
