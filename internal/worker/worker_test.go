package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/rophy/aqsh/internal/config"
	"github.com/rophy/aqsh/internal/tasks"
)

func TestNew(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	cfg := &config.Config{
		LogRetention:    time.Hour,
		ResultRetention: time.Hour,
		WorkerQueues:    []string{"default", "critical"},
	}

	tasksConfig := &tasks.TasksConfig{
		Tasks: map[string]tasks.TaskDef{
			"test": {Script: "test.sh"},
		},
	}

	asynqOpt := asynq.RedisClientOpt{Addr: mr.Addr()}

	w := New(cfg, tasksConfig, rdb, asynqOpt)

	if w == nil {
		t.Fatal("expected non-nil Worker")
	}
	if w.cfg != cfg {
		t.Error("expected cfg to be set")
	}
	if w.tasks != tasksConfig {
		t.Error("expected tasks to be set")
	}
	if w.rdb != rdb {
		t.Error("expected rdb to be set")
	}
	if w.logStream == nil {
		t.Error("expected logStream to be initialized")
	}
}

func TestTaskPayloadAndResult(t *testing.T) {
	t.Run("TaskPayload marshaling", func(t *testing.T) {
		payload := TaskPayload{
			Name:      "deploy",
			CreatedAt: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
			Env:       map[string]string{"DEPLOY_ENV": "production"},
			Payload:   map[string]any{"replicas": 3},
		}

		bytes, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var decoded TaskPayload
		if err := json.Unmarshal(bytes, &decoded); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if decoded.Name != "deploy" {
			t.Errorf("expected name='deploy', got %q", decoded.Name)
		}
		if decoded.Env["DEPLOY_ENV"] != "production" {
			t.Errorf("expected env DEPLOY_ENV='production', got %q", decoded.Env["DEPLOY_ENV"])
		}
	})
}

func TestReadResultFile(t *testing.T) {
	// Create a temporary worker with minimal config
	w := &Worker{}

	t.Run("nonexistent file returns nil (no result)", func(t *testing.T) {
		result, err := w.readResultFile("/nonexistent/path/file")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if result != nil {
			t.Errorf("expected nil for nonexistent file, got %q", *result)
		}
	})

	t.Run("empty file returns empty string pointer", func(t *testing.T) {
		tmpFile := createTempFile(t, "")
		defer os.Remove(tmpFile)

		result, err := w.readResultFile(tmpFile)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if result == nil {
			t.Error("expected non-nil pointer for empty file, got nil")
		} else if *result != "" {
			t.Errorf("expected empty string for empty file, got %q", *result)
		}
	})

	t.Run("JSON content returned as string pointer", func(t *testing.T) {
		content := `{"status": "success", "count": 42}`
		tmpFile := createTempFile(t, content)
		defer os.Remove(tmpFile)

		result, err := w.readResultFile(tmpFile)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}

		if result == nil {
			t.Fatal("expected non-nil pointer, got nil")
		}
		if *result != content {
			t.Errorf("expected %q, got %q", content, *result)
		}
	})

	t.Run("plain text returned as string pointer", func(t *testing.T) {
		content := "just plain text\nwith newlines"
		tmpFile := createTempFile(t, content)
		defer os.Remove(tmpFile)

		result, err := w.readResultFile(tmpFile)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}

		if result == nil {
			t.Fatal("expected non-nil pointer, got nil")
		}
		if *result != content {
			t.Errorf("expected %q, got %q", content, *result)
		}
	})

	t.Run("file too large", func(t *testing.T) {
		// Create a file larger than maxResultSize
		largeContent := make([]byte, maxResultSize+1)
		for i := range largeContent {
			largeContent[i] = 'x'
		}
		tmpFile := createTempFile(t, string(largeContent))
		defer os.Remove(tmpFile)

		_, err := w.readResultFile(tmpFile)
		if err == nil {
			t.Error("expected error for large file, got nil")
		}
	})
}

func strPtr(s string) *string {
	return &s
}

func TestTaskResultJSON(t *testing.T) {
	t.Run("result with data", func(t *testing.T) {
		data := `{"status": "deployed", "count": 42}`
		result := TaskResult{
			ExitCode: 0,
			Data:     &data,
		}

		bytes, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var decoded TaskResult
		if err := json.Unmarshal(bytes, &decoded); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if decoded.ExitCode != 0 {
			t.Errorf("expected exit_code=0, got %d", decoded.ExitCode)
		}
		if decoded.Data == nil || *decoded.Data != data {
			t.Errorf("expected data to match, got %v", decoded.Data)
		}
	})

	t.Run("result with empty data (empty result file)", func(t *testing.T) {
		empty := ""
		result := TaskResult{
			ExitCode: 0,
			Data:     &empty,
		}

		bytes, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		// Verify data field IS present (even if empty)
		var raw map[string]any
		if err := json.Unmarshal(bytes, &raw); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}
		if _, exists := raw["data"]; !exists {
			t.Error("expected data field to be present for empty result")
		}
	})

	t.Run("result with no data (no result file)", func(t *testing.T) {
		result := TaskResult{
			ExitCode: 0,
			Data:     nil,
		}

		bytes, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		// Verify data field is omitted
		var raw map[string]any
		if err := json.Unmarshal(bytes, &raw); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}
		if _, exists := raw["data"]; exists {
			t.Error("expected data field to be omitted when nil")
		}
	})

	t.Run("result with error", func(t *testing.T) {
		result := TaskResult{
			ExitCode: -1,
			Error:    "script not found",
		}

		bytes, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var decoded TaskResult
		if err := json.Unmarshal(bytes, &decoded); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if decoded.ExitCode != -1 {
			t.Errorf("expected exit_code=-1, got %d", decoded.ExitCode)
		}
		if decoded.Error != "script not found" {
			t.Errorf("expected error='script not found', got %v", decoded.Error)
		}
	})
}

func createTempFile(t *testing.T, content string) string {
	t.Helper()
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "result")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	return tmpFile
}
