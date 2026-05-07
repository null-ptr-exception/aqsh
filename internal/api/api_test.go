package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/rophy/aqsh/internal/config"
	"github.com/rophy/aqsh/internal/logs"
	"github.com/rophy/aqsh/internal/tasks"
)

func TestStatusResponseWriter(t *testing.T) {
	t.Run("captures status code", func(t *testing.T) {
		rec := httptest.NewRecorder()
		sw := &statusResponseWriter{ResponseWriter: rec, status: http.StatusOK}
		sw.WriteHeader(http.StatusNotFound)
		if sw.status != http.StatusNotFound {
			t.Errorf("expected status %d, got %d", http.StatusNotFound, sw.status)
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("expected underlying status %d, got %d", http.StatusNotFound, rec.Code)
		}
	})

	t.Run("Unwrap preserves Flusher via ResponseController", func(t *testing.T) {
		rec := httptest.NewRecorder()
		sw := &statusResponseWriter{ResponseWriter: rec, status: http.StatusOK}
		rc := http.NewResponseController(sw)
		if err := rc.Flush(); err != nil {
			t.Errorf("expected Flush via ResponseController to succeed, got %v", err)
		}
	})
}

func TestAccessLogMiddleware(t *testing.T) {
	s := &Server{
		cfg: &config.Config{
			IdentityHeader: "X-Forwarded-User",
			GroupsHeader:   "X-Forwarded-Groups",
		},
	}

	handler := s.accessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	t.Run("passes through status code", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Errorf("expected status %d, got %d", http.StatusCreated, rec.Code)
		}
	})

	t.Run("default status is 200 when handler does not call WriteHeader", func(t *testing.T) {
		noWriteHandler := s.accessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("ok"))
		}))
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		rec := httptest.NewRecorder()
		noWriteHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
		}
	})
}

func TestStateToStatus(t *testing.T) {
	tests := []struct {
		state    asynq.TaskState
		expected string
	}{
		{asynq.TaskStatePending, "pending"},
		{asynq.TaskStateActive, "running"},
		{asynq.TaskStateCompleted, "completed"},
		{asynq.TaskStateRetry, "retrying"},
		{asynq.TaskStateArchived, "failed"},
		{asynq.TaskStateScheduled, "scheduled"},
	}

	for _, tc := range tests {
		t.Run(tc.expected, func(t *testing.T) {
			got := stateToStatus(tc.state)
			if got != tc.expected {
				t.Errorf("stateToStatus(%v) = %q, want %q", tc.state, got, tc.expected)
			}
		})
	}
}

func TestHandleHealth(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	cfg := &config.Config{
		Mode: "combined",
	}

	s := &Server{
		cfg: cfg,
		rdb: rdb,
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	s.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp["status"] != "healthy" {
		t.Errorf("expected status=healthy, got %v", resp["status"])
	}
	if resp["redis"] != "connected" {
		t.Errorf("expected redis=connected, got %v", resp["redis"])
	}
	if resp["mode"] != "combined" {
		t.Errorf("expected mode=combined, got %v", resp["mode"])
	}
}

func TestHandleHealthRedisDown(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	mr.Close() // Simulate Redis being down

	cfg := &config.Config{
		Mode: "api",
	}

	s := &Server{
		cfg: cfg,
		rdb: rdb,
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	s.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	redisStatus, ok := resp["redis"].(string)
	if !ok || !strings.HasPrefix(redisStatus, "error:") {
		t.Errorf("expected redis error status, got %v", resp["redis"])
	}
}

func TestHandleListTasks(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	tasksConfig := &tasks.TasksConfig{
		Tasks: map[string]tasks.TaskDef{
			"deploy": {
				Script:      "deploy.sh",
				Description: "Deploy the application",
			},
			"backup": {
				Script:      "backup.sh",
				Description: "Backup database",
			},
		},
	}

	s := &Server{
		cfg:   &config.Config{},
		tasks: tasksConfig,
		rdb:   rdb,
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	rec := httptest.NewRecorder()

	s.handleListTasks(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	tasksResp, ok := resp["tasks"].(map[string]any)
	if !ok {
		t.Fatal("expected tasks object in response")
	}

	if len(tasksResp) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(tasksResp))
	}

	deployTask := tasksResp["deploy"].(map[string]any)
	if deployTask["description"] != "Deploy the application" {
		t.Errorf("expected description='Deploy the application', got %v", deployTask["description"])
	}

	if _, hasInput := deployTask["input"]; hasInput {
		t.Error("list response should not include input details")
	}
	if _, hasTimeout := deployTask["timeout"]; hasTimeout {
		t.Error("list response should not include timeout")
	}
}

func TestHandleGetTaskDef(t *testing.T) {
	valueSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := r.URL.Query().Get("user")
		switch user {
		case "alice":
			json.NewEncoder(w).Encode([]struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			}{
				{Name: "DB One", Value: "db-001"},
				{Name: "DB Two", Value: "db-002"},
			})
		default:
			json.NewEncoder(w).Encode([]string{})
		}
	}))
	defer valueSrv.Close()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	tasksConfig := &tasks.TasksConfig{
		Tasks: map[string]tasks.TaskDef{
			"deploy": {
				Script:      "deploy.sh",
				Description: "Deploy the app",
				Timeout:     "5m",
				Input: []tasks.Input{
					{Name: "env", Env: "ENV", Type: "string", Required: true, Enum: []string{"dev", "prod"}},
					{Name: "instance", Env: "INSTANCE", Type: "string", ValuesURL: valueSrv.URL + "?user=${identity}"},
				},
			},
		},
	}

	s := &Server{
		cfg: &config.Config{
			IdentityHeader: "X-Forwarded-User",
			GroupsHeader:   "X-Forwarded-Groups",
		},
		tasks: tasksConfig,
		rdb:   rdb,
	}

	t.Run("without identity returns values_url flag only", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/tasks/deploy", nil)
		req.SetPathValue("name", "deploy")
		rec := httptest.NewRecorder()

		s.handleGetTaskDef(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
		}

		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)

		inputs := resp["input"].([]any)
		instanceInput := inputs[1].(map[string]any)
		if instanceInput["values_url"] != true {
			t.Errorf("expected values_url=true, got %v", instanceInput["values_url"])
		}
		if _, hasValues := instanceInput["values"]; hasValues {
			t.Error("expected no values when no identity provided")
		}
	})

	t.Run("with identity resolves values", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/tasks/deploy", nil)
		req.SetPathValue("name", "deploy")
		req.Header.Set("X-Forwarded-User", "alice")
		rec := httptest.NewRecorder()

		s.handleGetTaskDef(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
		}

		var resp map[string]any
		json.Unmarshal(rec.Body.Bytes(), &resp)

		inputs := resp["input"].([]any)
		instanceInput := inputs[1].(map[string]any)

		values, ok := instanceInput["values"].([]any)
		if !ok {
			t.Fatalf("expected values array, got %v", instanceInput["values"])
		}
		if len(values) != 2 {
			t.Fatalf("expected 2 values, got %d", len(values))
		}

		first := values[0].(map[string]any)
		if first["name"] != "DB One" || first["value"] != "db-001" {
			t.Errorf("expected first value {DB One, db-001}, got %v", first)
		}
	})

	t.Run("unknown task", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/tasks/nonexistent", nil)
		req.SetPathValue("name", "nonexistent")
		rec := httptest.NewRecorder()

		s.handleGetTaskDef(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("expected status %d, got %d", http.StatusNotFound, rec.Code)
		}
	})
}

func TestHandleSubmitTaskNotFound(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	tasksConfig := &tasks.TasksConfig{
		Tasks: map[string]tasks.TaskDef{},
	}

	s := &Server{
		cfg:       &config.Config{},
		tasks:     tasksConfig,
		rdb:       rdb,
		logStream: logs.NewLogStreamer(rdb, time.Hour),
	}

	req := httptest.NewRequest(http.MethodPost, "/tasks/nonexistent", strings.NewReader(`{}`))
	req.SetPathValue("name", "nonexistent")
	rec := httptest.NewRecorder()

	s.handleSubmitTask(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if _, ok := resp["error"]; !ok {
		t.Error("expected error in response")
	}
}

func TestHandleSubmitTaskInvalidJSON(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	tasksConfig := &tasks.TasksConfig{
		Tasks: map[string]tasks.TaskDef{
			"test": {
				Script: "test.sh",
			},
		},
	}

	s := &Server{
		cfg:       &config.Config{},
		tasks:     tasksConfig,
		rdb:       rdb,
		logStream: logs.NewLogStreamer(rdb, time.Hour),
	}

	req := httptest.NewRequest(http.MethodPost, "/tasks/test", strings.NewReader(`{invalid json}`))
	req.SetPathValue("name", "test")
	rec := httptest.NewRecorder()

	s.handleSubmitTask(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestHandleSubmitTaskValidationError(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	tasksConfig := &tasks.TasksConfig{
		Tasks: map[string]tasks.TaskDef{
			"deploy": {
				Script: "deploy.sh",
				Input: []tasks.Input{
					{
						Name:     "env",
						Env:      "DEPLOY_ENV",
						Type:     "string",
						Required: true,
					},
				},
			},
		},
	}

	s := &Server{
		cfg:       &config.Config{},
		tasks:     tasksConfig,
		rdb:       rdb,
		logStream: logs.NewLogStreamer(rdb, time.Hour),
	}

	// Missing required field "env"
	req := httptest.NewRequest(http.MethodPost, "/tasks/deploy", strings.NewReader(`{}`))
	req.SetPathValue("name", "deploy")
	rec := httptest.NewRecorder()

	s.handleSubmitTask(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	errMsg, ok := resp["error"].(string)
	if !ok || !strings.Contains(errMsg, "required") {
		t.Errorf("expected validation error about required field, got %v", resp["error"])
	}
}

func TestHandleGetExecutionNotFound(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	cfg := &config.Config{
		WorkerQueues: []string{"default"},
	}

	s := &Server{
		cfg:       cfg,
		rdb:       rdb,
		inspector: asynq.NewInspector(asynq.RedisClientOpt{Addr: mr.Addr()}),
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks/nonexistent-id", nil)
	req.SetPathValue("id", "nonexistent-id")
	rec := httptest.NewRecorder()

	s.handleGetExecution(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestJsonResponse(t *testing.T) {
	s := &Server{}

	rec := httptest.NewRecorder()
	s.jsonResponse(rec, http.StatusCreated, map[string]any{"key": "value"})

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status %d, got %d", http.StatusCreated, rec.Code)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type=application/json, got %s", ct)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp["key"] != "value" {
		t.Errorf("expected key=value, got %v", resp["key"])
	}
}

func TestJsonError(t *testing.T) {
	s := &Server{}

	rec := httptest.NewRecorder()
	s.jsonError(rec, http.StatusBadRequest, "something went wrong")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp["error"] != "something went wrong" {
		t.Errorf("expected error='something went wrong', got %v", resp["error"])
	}
}

func TestHandleSubmitTaskIdentityRequired(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	tasksConfig := &tasks.TasksConfig{
		Tasks: map[string]tasks.TaskDef{
			"test": {Script: "test.sh"},
		},
	}

	s := &Server{
		cfg: &config.Config{
			IdentityHeader:  "X-Forwarded-User",
			RequireIdentity: true,
		},
		tasks:     tasksConfig,
		rdb:       rdb,
		logStream: logs.NewLogStreamer(rdb, time.Hour),
		client:    asynq.NewClient(asynq.RedisClientOpt{Addr: mr.Addr()}),
	}

	t.Run("missing identity returns 401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tasks/test", strings.NewReader(`{}`))
		req.SetPathValue("name", "test")
		rec := httptest.NewRecorder()

		s.handleSubmitTask(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
		}

		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if _, ok := resp["error"]; !ok {
			t.Error("expected error in response")
		}
	})

	t.Run("with identity header proceeds", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tasks/test", strings.NewReader(`{}`))
		req.SetPathValue("name", "test")
		req.Header.Set("X-Forwarded-User", "alice@example.com")
		rec := httptest.NewRecorder()

		s.handleSubmitTask(rec, req)

		// Should not be 401 — it will fail later (no asynq client) but that's fine
		if rec.Code == http.StatusUnauthorized {
			t.Error("expected request to pass identity check")
		}
	})
}

func TestHandleSubmitTaskIdentityOptional(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	tasksConfig := &tasks.TasksConfig{
		Tasks: map[string]tasks.TaskDef{
			"test": {Script: "test.sh"},
		},
	}

	s := &Server{
		cfg: &config.Config{
			IdentityHeader:  "X-Forwarded-User",
			RequireIdentity: false,
		},
		tasks:     tasksConfig,
		rdb:       rdb,
		logStream: logs.NewLogStreamer(rdb, time.Hour),
		client:    asynq.NewClient(asynq.RedisClientOpt{Addr: mr.Addr()}),
	}

	req := httptest.NewRequest(http.MethodPost, "/tasks/test", strings.NewReader(`{}`))
	req.SetPathValue("name", "test")
	rec := httptest.NewRecorder()

	s.handleSubmitTask(rec, req)

	// Should not be 401 — anonymous allowed
	if rec.Code == http.StatusUnauthorized {
		t.Error("expected anonymous request to be allowed when RequireIdentity is false")
	}
}

func TestHandleGetExecutionIdentity(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	cfg := &config.Config{
		WorkerQueues: []string{"default"},
	}

	s := &Server{
		cfg:       cfg,
		rdb:       rdb,
		inspector: asynq.NewInspector(asynq.RedisClientOpt{Addr: mr.Addr()}),
	}

	// Enqueue a task with identity to test GET response
	client := asynq.NewClient(asynq.RedisClientOpt{Addr: mr.Addr()})
	defer client.Close()

	payload := map[string]any{
		"name":       "test",
		"created_at": time.Now().Format(time.RFC3339),
		"identity":   "alice@example.com",
		"env":        map[string]string{},
		"payload":    map[string]any{},
	}
	payloadBytes, _ := json.Marshal(payload)
	task := asynq.NewTask("aqsh:job", payloadBytes,
		asynq.Queue("default"),
		asynq.Retention(time.Hour),
	)
	info, err := client.Enqueue(task)
	if err != nil {
		t.Fatalf("failed to enqueue task: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+info.ID, nil)
	req.SetPathValue("id", info.ID)
	rec := httptest.NewRecorder()

	s.handleGetExecution(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp["identity"] != "alice@example.com" {
		t.Errorf("expected identity='alice@example.com', got %v", resp["identity"])
	}
}

func TestHandleGetExecutionNoIdentity(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	cfg := &config.Config{
		WorkerQueues: []string{"default"},
	}

	s := &Server{
		cfg:       cfg,
		rdb:       rdb,
		inspector: asynq.NewInspector(asynq.RedisClientOpt{Addr: mr.Addr()}),
	}

	client := asynq.NewClient(asynq.RedisClientOpt{Addr: mr.Addr()})
	defer client.Close()

	payload := map[string]any{
		"name":       "test",
		"created_at": time.Now().Format(time.RFC3339),
		"env":        map[string]string{},
		"payload":    map[string]any{},
	}
	payloadBytes, _ := json.Marshal(payload)
	task := asynq.NewTask("aqsh:job", payloadBytes,
		asynq.Queue("default"),
		asynq.Retention(time.Hour),
	)
	info, err := client.Enqueue(task)
	if err != nil {
		t.Fatalf("failed to enqueue task: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+info.ID, nil)
	req.SetPathValue("id", info.ID)
	rec := httptest.NewRecorder()

	s.handleGetExecution(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if _, ok := resp["identity"]; ok {
		t.Errorf("expected identity to be omitted when not set, got %v", resp["identity"])
	}
}

func TestHandleSubmitTaskGroupAuthorization(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	tasksConfig := &tasks.TasksConfig{
		Tasks: map[string]tasks.TaskDef{
			"restricted": {
				Script:        "restricted.sh",
				AllowedGroups: []string{"admin", "ops"},
			},
			"open": {
				Script: "open.sh",
			},
		},
	}

	s := &Server{
		cfg: &config.Config{
			IdentityHeader: "X-Forwarded-User",
			GroupsHeader:   "X-Forwarded-Groups",
		},
		tasks:     tasksConfig,
		rdb:       rdb,
		logStream: logs.NewLogStreamer(rdb, time.Hour),
		client:    asynq.NewClient(asynq.RedisClientOpt{Addr: mr.Addr()}),
	}
	defer s.client.Close()

	t.Run("allowed group passes", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tasks/restricted", strings.NewReader(`{}`))
		req.SetPathValue("name", "restricted")
		req.Header.Set("X-Forwarded-Groups", "dev,ops")
		rec := httptest.NewRecorder()

		s.handleSubmitTask(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Errorf("expected status %d, got %d: %s", http.StatusAccepted, rec.Code, rec.Body.String())
		}
	})

	t.Run("no matching group returns 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tasks/restricted", strings.NewReader(`{}`))
		req.SetPathValue("name", "restricted")
		req.Header.Set("X-Forwarded-Groups", "dev,staging")
		rec := httptest.NewRecorder()

		s.handleSubmitTask(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("expected status %d, got %d", http.StatusForbidden, rec.Code)
		}
	})

	t.Run("no groups header returns 403 for restricted task", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tasks/restricted", strings.NewReader(`{}`))
		req.SetPathValue("name", "restricted")
		rec := httptest.NewRecorder()

		s.handleSubmitTask(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("expected status %d, got %d", http.StatusForbidden, rec.Code)
		}
	})

	t.Run("open task allows anyone", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tasks/open", strings.NewReader(`{}`))
		req.SetPathValue("name", "open")
		rec := httptest.NewRecorder()

		s.handleSubmitTask(rec, req)

		if rec.Code == http.StatusForbidden {
			t.Error("expected open task to allow anyone")
		}
	})

	t.Run("groups with spaces trimmed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tasks/restricted", strings.NewReader(`{}`))
		req.SetPathValue("name", "restricted")
		req.Header.Set("X-Forwarded-Groups", " admin , dev ")
		rec := httptest.NewRecorder()

		s.handleSubmitTask(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Errorf("expected status %d, got %d: %s", http.StatusAccepted, rec.Code, rec.Body.String())
		}
	})
}

func TestHandleSubmitTaskUserAuthorization(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	tasksConfig := &tasks.TasksConfig{
		Tasks: map[string]tasks.TaskDef{
			"sa-only": {
				Script:       "sa.sh",
				AllowedUsers: []string{"system:serviceaccount:rdsma:sertdxkkk"},
			},
			"users-and-groups": {
				Script:        "both.sh",
				AllowedUsers:  []string{"alice"},
				AllowedGroups: []string{"ops"},
			},
		},
	}

	s := &Server{
		cfg: &config.Config{
			IdentityHeader: "X-Forwarded-User",
			GroupsHeader:   "X-Forwarded-Groups",
		},
		tasks:     tasksConfig,
		rdb:       rdb,
		logStream: logs.NewLogStreamer(rdb, time.Hour),
		client:    asynq.NewClient(asynq.RedisClientOpt{Addr: mr.Addr()}),
	}
	defer s.client.Close()

	t.Run("allowed user passes", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tasks/sa-only", strings.NewReader(`{}`))
		req.SetPathValue("name", "sa-only")
		req.Header.Set("X-Forwarded-User", "system:serviceaccount:rdsma:sertdxkkk")
		rec := httptest.NewRecorder()

		s.handleSubmitTask(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Errorf("expected status %d, got %d: %s", http.StatusAccepted, rec.Code, rec.Body.String())
		}
	})

	t.Run("wrong user returns 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tasks/sa-only", strings.NewReader(`{}`))
		req.SetPathValue("name", "sa-only")
		req.Header.Set("X-Forwarded-User", "system:serviceaccount:other:other")
		rec := httptest.NewRecorder()

		s.handleSubmitTask(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("expected status %d, got %d", http.StatusForbidden, rec.Code)
		}
	})

	t.Run("no identity returns 403 for user-restricted task", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tasks/sa-only", strings.NewReader(`{}`))
		req.SetPathValue("name", "sa-only")
		rec := httptest.NewRecorder()

		s.handleSubmitTask(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("expected status %d, got %d", http.StatusForbidden, rec.Code)
		}
	})

	t.Run("user match passes even without matching group", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tasks/users-and-groups", strings.NewReader(`{}`))
		req.SetPathValue("name", "users-and-groups")
		req.Header.Set("X-Forwarded-User", "alice")
		req.Header.Set("X-Forwarded-Groups", "dev")
		rec := httptest.NewRecorder()

		s.handleSubmitTask(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Errorf("expected status %d, got %d: %s", http.StatusAccepted, rec.Code, rec.Body.String())
		}
	})

	t.Run("group match passes even without matching user", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tasks/users-and-groups", strings.NewReader(`{}`))
		req.SetPathValue("name", "users-and-groups")
		req.Header.Set("X-Forwarded-User", "bob")
		req.Header.Set("X-Forwarded-Groups", "ops")
		rec := httptest.NewRecorder()

		s.handleSubmitTask(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Errorf("expected status %d, got %d: %s", http.StatusAccepted, rec.Code, rec.Body.String())
		}
	})

	t.Run("neither user nor group match returns 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tasks/users-and-groups", strings.NewReader(`{}`))
		req.SetPathValue("name", "users-and-groups")
		req.Header.Set("X-Forwarded-User", "bob")
		req.Header.Set("X-Forwarded-Groups", "dev")
		rec := httptest.NewRecorder()

		s.handleSubmitTask(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("expected status %d, got %d", http.StatusForbidden, rec.Code)
		}
	})
}

func TestIsAllowedUser(t *testing.T) {
	tests := []struct {
		name     string
		identity string
		allowed  []string
		expected bool
	}{
		{"match", "alice", []string{"alice", "bob"}, true},
		{"no match", "charlie", []string{"alice", "bob"}, false},
		{"empty identity", "", []string{"alice"}, false},
		{"empty identity empty allowed", "", []string{""}, false},
		{"non-empty identity empty allowed entry", "alice", []string{""}, false},
		{"empty allowed", "alice", nil, false},
		{"exact SA match", "system:serviceaccount:rdsma:sertdxkkk", []string{"system:serviceaccount:rdsma:sertdxkkk"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isAllowedUser(tc.identity, tc.allowed)
			if got != tc.expected {
				t.Errorf("isAllowedUser(%q, %v) = %v, want %v", tc.identity, tc.allowed, got, tc.expected)
			}
		})
	}
}

func TestSplitGroups(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		expected []string
	}{
		{"empty", "", nil},
		{"single", "admin", []string{"admin"}},
		{"multiple", "admin,ops,dev", []string{"admin", "ops", "dev"}},
		{"with spaces", " admin , ops ", []string{"admin", "ops"}},
		{"trailing comma", "admin,", []string{"admin"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitGroups(tc.header)
			if len(got) != len(tc.expected) {
				t.Fatalf("expected %d groups, got %d: %v", len(tc.expected), len(got), got)
			}
			for i := range got {
				if got[i] != tc.expected[i] {
					t.Errorf("group[%d] = %q, want %q", i, got[i], tc.expected[i])
				}
			}
		})
	}
}

func TestHasAnyGroup(t *testing.T) {
	tests := []struct {
		name     string
		user     []string
		allowed  []string
		expected bool
	}{
		{"match", []string{"dev", "ops"}, []string{"admin", "ops"}, true},
		{"no match", []string{"dev", "staging"}, []string{"admin", "ops"}, false},
		{"empty user", nil, []string{"admin"}, false},
		{"empty allowed", []string{"admin"}, nil, false},
		{"both empty", nil, nil, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := hasAnyGroup(tc.user, tc.allowed)
			if got != tc.expected {
				t.Errorf("hasAnyGroup(%v, %v) = %v, want %v", tc.user, tc.allowed, got, tc.expected)
			}
		})
	}
}

func TestHandleSubmitTaskValuesURL(t *testing.T) {
	valueSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := r.URL.Query().Get("user")
		switch user {
		case "alice":
			json.NewEncoder(w).Encode([]string{"db-001", "db-002"})
		case "bob":
			json.NewEncoder(w).Encode([]string{"db-003"})
		default:
			json.NewEncoder(w).Encode([]string{})
		}
	}))
	defer valueSrv.Close()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	tasksConfig := &tasks.TasksConfig{
		Tasks: map[string]tasks.TaskDef{
			"upgrade-db": {
				Script: "upgrade.sh",
				Input: []tasks.Input{
					{
						Name:      "instance",
						Env:       "DB_INSTANCE",
						Required:  true,
						Type:      "string",
						ValuesURL: valueSrv.URL + "?user=${identity}",
					},
				},
			},
		},
	}

	s := &Server{
		cfg: &config.Config{
			IdentityHeader: "X-Forwarded-User",
			GroupsHeader:   "X-Forwarded-Groups",
		},
		tasks:     tasksConfig,
		rdb:       rdb,
		logStream: logs.NewLogStreamer(rdb, time.Hour),
		client:    asynq.NewClient(asynq.RedisClientOpt{Addr: mr.Addr()}),
	}
	defer s.client.Close()

	t.Run("allowed value passes", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tasks/upgrade-db",
			strings.NewReader(`{"instance": "db-001"}`))
		req.SetPathValue("name", "upgrade-db")
		req.Header.Set("X-Forwarded-User", "alice")
		rec := httptest.NewRecorder()

		s.handleSubmitTask(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Errorf("expected status %d, got %d: %s", http.StatusAccepted, rec.Code, rec.Body.String())
		}
	})

	t.Run("disallowed value returns 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tasks/upgrade-db",
			strings.NewReader(`{"instance": "db-003"}`))
		req.SetPathValue("name", "upgrade-db")
		req.Header.Set("X-Forwarded-User", "alice")
		rec := httptest.NewRecorder()

		s.handleSubmitTask(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("expected status %d, got %d: %s", http.StatusForbidden, rec.Code, rec.Body.String())
		}
	})

	t.Run("different user sees different values", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tasks/upgrade-db",
			strings.NewReader(`{"instance": "db-003"}`))
		req.SetPathValue("name", "upgrade-db")
		req.Header.Set("X-Forwarded-User", "bob")
		rec := httptest.NewRecorder()

		s.handleSubmitTask(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Errorf("expected status %d, got %d: %s", http.StatusAccepted, rec.Code, rec.Body.String())
		}
	})

	t.Run("unknown user gets empty list returns 403", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tasks/upgrade-db",
			strings.NewReader(`{"instance": "db-001"}`))
		req.SetPathValue("name", "upgrade-db")
		req.Header.Set("X-Forwarded-User", "unknown")
		rec := httptest.NewRecorder()

		s.handleSubmitTask(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("expected status %d, got %d: %s", http.StatusForbidden, rec.Code, rec.Body.String())
		}
	})
}

func TestHandleSubmitTaskValuesURLError(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	t.Run("remote server error returns 502", func(t *testing.T) {
		errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer errSrv.Close()

		tasksConfig := &tasks.TasksConfig{
			Tasks: map[string]tasks.TaskDef{
				"test": {
					Script: "test.sh",
					Input: []tasks.Input{
						{Name: "val", Env: "VAL", Type: "string", ValuesURL: errSrv.URL},
					},
				},
			},
		}

		s := &Server{
			cfg:       &config.Config{IdentityHeader: "X-Forwarded-User"},
			tasks:     tasksConfig,
			rdb:       rdb,
			logStream: logs.NewLogStreamer(rdb, time.Hour),
			client:    asynq.NewClient(asynq.RedisClientOpt{Addr: mr.Addr()}),
		}
		defer s.client.Close()

		req := httptest.NewRequest(http.MethodPost, "/tasks/test",
			strings.NewReader(`{"val": "x"}`))
		req.SetPathValue("name", "test")
		rec := httptest.NewRecorder()

		s.handleSubmitTask(rec, req)

		if rec.Code != http.StatusBadGateway {
			t.Errorf("expected status %d, got %d: %s", http.StatusBadGateway, rec.Code, rec.Body.String())
		}
	})
}

func TestHandleGetLogs(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	logStream := logs.NewLogStreamer(rdb, time.Hour)
	s := &Server{
		cfg:       &config.Config{},
		rdb:       rdb,
		logStream: logStream,
	}

	// Write some logs
	ctx := context.Background()
	logStream.Write(ctx, "test-task-id", "line 1")
	logStream.Write(ctx, "test-task-id", "line 2")
	logStream.WriteEOF(ctx, "test-task-id")

	req := httptest.NewRequest(http.MethodGet, "/tasks/test-task-id/logs?follow=false", nil)
	req.SetPathValue("id", "test-task-id")
	rec := httptest.NewRecorder()

	s.handleGetLogs(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "data: line 1") {
		t.Errorf("expected 'line 1' in response, got %s", body)
	}
	if !strings.Contains(body, "data: line 2") {
		t.Errorf("expected 'line 2' in response, got %s", body)
	}
	if !strings.Contains(body, "event: eof") {
		t.Errorf("expected 'event: eof' in response, got %s", body)
	}
}
