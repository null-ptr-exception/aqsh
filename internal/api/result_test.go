package api

import (
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
	"github.com/rophy/aqsh/internal/worker"
)

func TestExecutionResultJSONOptIn(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client := asynq.NewClient(asynq.RedisClientOpt{Addr: mr.Addr()})
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close(); client.Close(); inspector.Close() })
	s := &Server{cfg: &config.Config{}, rdb: rdb, inspector: inspector}
	payload, _ := json.Marshal(worker.TaskPayload{Name: "inspect"})
	info, err := client.Enqueue(asynq.NewTask(worker.TaskType, payload, asynq.Retention(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	data := `{"count":9007199254740993}`
	result, _ := json.Marshal(worker.TaskResult{Data: &data})
	// Seed a completed result; the worker itself is exercised by integration tests.
	if err := rdb.HSet(t.Context(), "asynq:{default}:t:"+info.ID, "state", "completed", "result", result).Err(); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "?include_result_json=false", "?include_result_json=true"} {
		t.Run(query, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/executions/"+info.ID+query, nil)
			req.SetPathValue("id", info.ID)
			rec := httptest.NewRecorder()
			s.handleGetExecution(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			var response struct {
				Task   string                     `json:"task"`
				Result map[string]json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			var preserved string
			if response.Task != "inspect" || json.Unmarshal(response.Result["data"], &preserved) != nil || preserved != data {
				t.Fatalf("task or legacy data changed: %s", rec.Body.String())
			}
			if query == "?include_result_json=true" {
				if string(response.Result["json"]) != data {
					t.Fatalf("structured JSON changed: %s", response.Result["json"])
				}
			} else if _, exists := response.Result["json"]; exists {
				t.Fatal("JSON returned without opt-in")
			}
		})
	}
}

func TestStructuredResult(t *testing.T) {
	for _, tc := range []struct {
		name, data, omitted string
		absent              bool
	}{
		{name: "object", data: `{"healthy":false,"count":9007199254740993}`},
		{name: "array", data: `[1,"two"]`},
		{name: "null", data: `null`},
		{name: "empty", omitted: "empty"},
		{name: "absent", absent: true, omitted: "absent"},
		{name: "invalid", data: "plain text", omitted: "invalid_json"},
		{name: "oversize", data: strings.Repeat(" ", maxResultJSONBytes) + "{}", omitted: "too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := worker.TaskResult{ExitCode: 2, Error: "script failed"}
			if !tc.absent {
				result.Data = &tc.data
			}
			out := structuredResult(result)
			encoded, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if out.JSONOmitted != tc.omitted {
				t.Fatalf("omission = %q", out.JSONOmitted)
			}
			if tc.omitted == "" && string(decoded["json"]) != tc.data {
				t.Fatalf("JSON changed: %s", decoded["json"])
			}
			if tc.omitted != "" && decoded["json"] != nil {
				t.Fatal("partial JSON returned")
			}
			var preserved string
			if !tc.absent && (json.Unmarshal(decoded["data"], &preserved) != nil || preserved != tc.data) {
				t.Fatal("legacy data changed")
			}
		})
	}
}
