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

	floatPtr := func(f float64) *float64 { return &f }
	intPtr := func(i int) *int { return &i }

	tasksConfig := &tasks.TasksConfig{
		Tasks: map[string]tasks.TaskDef{
			"deploy": {
				Script:      "deploy.sh",
				Description: "Deploy the application",
				Timeout:     "5m",
				MaxRetry:    intPtr(3),
				Queue:       "critical",
				Input: []tasks.Input{
					{
						Name:     "env",
						Env:      "DEPLOY_ENV",
						Type:     "string",
						Required: true,
						Enum:     []string{"staging", "production"},
					},
					{
						Name:        "replicas",
						Env:         "REPLICAS",
						Type:        "int",
						Required:    false,
						Min:         floatPtr(1),
						Max:         floatPtr(10),
						Default:     "3",
						Description: "Number of replicas",
					},
				},
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

	deployTask, ok := tasksResp["deploy"].(map[string]any)
	if !ok {
		t.Fatal("expected deploy task in response")
	}

	if deployTask["description"] != "Deploy the application" {
		t.Errorf("expected description='Deploy the application', got %v", deployTask["description"])
	}
	if deployTask["queue"] != "critical" {
		t.Errorf("expected queue='critical', got %v", deployTask["queue"])
	}
	if int(deployTask["max_retry"].(float64)) != 3 {
		t.Errorf("expected max_retry=3, got %v", deployTask["max_retry"])
	}

	inputs, ok := deployTask["input"].([]any)
	if !ok || len(inputs) != 2 {
		t.Fatalf("expected 2 inputs, got %v", deployTask["input"])
	}

	// Check first input (env)
	envInput := inputs[0].(map[string]any)
	if envInput["name"] != "env" {
		t.Errorf("expected input name='env', got %v", envInput["name"])
	}
	if envInput["required"] != true {
		t.Errorf("expected required=true, got %v", envInput["required"])
	}
	enum := envInput["enum"].([]any)
	if len(enum) != 2 {
		t.Errorf("expected 2 enum values, got %v", enum)
	}

	// Check second input (replicas)
	replicasInput := inputs[1].(map[string]any)
	if replicasInput["default"] != "3" {
		t.Errorf("expected default='3', got %v", replicasInput["default"])
	}
	if replicasInput["description"] != "Number of replicas" {
		t.Errorf("expected description set, got %v", replicasInput["description"])
	}
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

func TestHandleGetTaskNotFound(t *testing.T) {
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

	s.handleGetTask(rec, req)

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
