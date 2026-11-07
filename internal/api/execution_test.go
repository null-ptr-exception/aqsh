package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/rophy/aqsh/internal/auth"
	"github.com/rophy/aqsh/internal/config"
	"github.com/rophy/aqsh/internal/tasks"
	"github.com/rophy/aqsh/internal/worker"
)

// newExecutionServer uses a non-default task queue that the API does not consume.
func newExecutionServer(t *testing.T, ownerOnly bool) (*Server, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := New(&config.Config{
		IdentityHeader: "X-Caller", GroupsHeader: "X-Groups", ExecutionOwnerOnly: ownerOnly,
		WorkerQueues: []string{"default", "historical", "default"}, LogRetention: time.Hour,
	}, &tasks.TasksConfig{Tasks: map[string]tasks.TaskDef{
		"inspect": {Script: "inspect.sh", Queue: "reads", Input: []tasks.Input{{Name: "identity", Env: "INPUT_IDENTITY", Type: "string"}}},
	}}, rdb, asynq.RedisClientOpt{Addr: mr.Addr()}, "test")
	t.Cleanup(func() { s.client.Close(); s.inspector.Close(); rdb.Close() })
	return s, mr
}

// TestExecutionOwnerOnly covers both identity sources and every execution read route.
func TestExecutionOwnerOnly(t *testing.T) {
	s, mr := newExecutionServer(t, true)
	ids := map[string]string{"missing": "missing", "orphan": "orphan"}
	for name, owner := range map[string]string{
		"owned": "alice", "legacy": "", "blank": "  ", "padded": " alice ",
		"qualified": "cluster-a/alice", "historical": "alice",
	} {
		data, err := json.Marshal(worker.TaskPayload{Name: "inspect", Identity: owner, Groups: "shared"})
		if err != nil {
			t.Fatal(err)
		}
		queue := "reads"
		if name == "historical" {
			queue = "historical"
		}
		info, err := s.client.Enqueue(asynq.NewTask(worker.TaskType, data), asynq.Queue(queue))
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = info.ID
	}
	for name, task := range map[string]*asynq.Task{
		"malformed": asynq.NewTask(worker.TaskType, []byte(`{`)),
		"foreign":   asynq.NewTask("other:task", []byte(`{"identity":"alice"}`)),
	} {
		info, err := s.client.Enqueue(task, asynq.Queue("reads"))
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = info.ID
	}
	for _, id := range ids {
		if err := s.logStream.Write(t.Context(), id, "private log"); err != nil {
			t.Fatal(err)
		}
		if err := s.logStream.WriteEOF(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"trusted-header", "kubernetes"} {
		t.Run(mode, func(t *testing.T) {
			s.cfg.AuthMode = mode
			for _, tc := range []struct {
				name, record, caller string
				want                 int
			}{
				{"owner", "owned", "alice", 200},
				{"shared group is not ownership", "owned", "bob", 404},
				{"missing identity", "owned", "", 401},
				{"blank identity", "owned", "  ", 401},
				{"no identity normalization", "owned", " alice ", 404},
				{"exact padded owner", "padded", " alice ", 200},
				{"qualified owner", "qualified", "cluster-a/alice", 200},
				{"other cluster", "qualified", "cluster-b/alice", 404},
				{"unqualified caller", "qualified", "alice", 404},
				{"legacy record", "legacy", "alice", 404},
				{"blank record", "blank", "alice", 404},
				{"malformed record", "malformed", "alice", 404},
				{"foreign task type", "foreign", "alice", 404},
				{"unknown execution", "missing", "alice", 404},
				{"orphan logs", "orphan", "alice", 404},
				{"removed task queue", "historical", "alice", 200},
			} {
				for _, suffix := range []string{"", "/logs?follow=false", "/logs"} {
					t.Run(tc.name+suffix, func(t *testing.T) {
						var a auth.Authenticator
						if mode == "kubernetes" {
							a = verifiedCaller{identity: tc.caller, groups: []string{"shared"}}
						}
						r := httptest.NewRequest("GET", "/executions/"+ids[tc.record]+suffix, nil)
						r.Header.Set("X-Caller", tc.caller)
						r.Header.Set("X-Groups", "shared")
						r.Header.Set("Last-Event-ID", "0")
						if mode == "kubernetes" {
							// A caller cannot read Alice's records by forging either header.
							r.Header.Set("X-Caller", "alice")
							r.Header.Set("X-Forwarded-User", "alice")
							if tc.caller != "" {
								r.Header.Set("Authorization", "Bearer caller-token")
							}
						}
						w := httptest.NewRecorder()
						s.handler(a).ServeHTTP(w, r)
						if w.Code != tc.want {
							t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body.String())
						}
						if tc.want == 404 && w.Body.String() != "{\"error\":\"task not found\"}\n" {
							t.Fatalf("denial reveals record existence: %s", w.Body.String())
						}
						if tc.want != 200 && (strings.Contains(w.Body.String(), "private log") || strings.Contains(w.Header().Get("Content-Type"), "event-stream")) {
							t.Fatal("denied response exposed logs or started SSE")
						}
						if tc.want == 200 && suffix != "" && !strings.Contains(w.Body.String(), "private log") {
							t.Fatal("owner did not receive logs")
						}
					})
				}
			}
		})
	}
	t.Run("lookup failure is not a missing execution", func(t *testing.T) {
		s.cfg.AuthMode = "trusted-header"
		mr.SetError("backend failure must not be exposed")
		defer mr.SetError("")
		for _, suffix := range []string{"", "/logs"} {
			r := httptest.NewRequest("GET", "/executions/"+ids["owned"]+suffix, nil)
			r.Header.Set("X-Caller", "alice")
			w := httptest.NewRecorder()
			s.handler(nil).ServeHTTP(w, r)
			if w.Code != 503 || strings.Contains(w.Body.String(), "backend failure") || strings.Contains(w.Header().Get("Content-Type"), "event-stream") {
				t.Fatalf("unexpected lookup failure: %d %s", w.Code, w.Body.String())
			}
		}
	})
}

// TestExecutionOwnershipSubmission verifies queued ownership and default compatibility.
func TestExecutionOwnershipSubmission(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, mode := range []string{"trusted-header", "kubernetes"} {
			s, _ := newExecutionServer(t, enabled)
			s.cfg.AuthMode = mode
			for _, caller := range []string{"", "  ", "alice"} {
				var a auth.Authenticator
				if mode == "kubernetes" {
					a = verifiedCaller{identity: caller}
				}
				r := httptest.NewRequest("POST", "/tasks/inspect", strings.NewReader(`{"identity":"forged-owner"}`))
				r.Header.Set("X-Caller", caller)
				if mode == "kubernetes" {
					r.Header.Set("X-Caller", "forged-header")
					if caller != "" {
						r.Header.Set("Authorization", "Bearer caller-token")
					}
				}
				w := httptest.NewRecorder()
				s.handler(a).ServeHTTP(w, r)
				denied := (mode == "kubernetes" && caller == "") || (enabled && strings.TrimSpace(caller) == "")
				if denied {
					if w.Code != 401 {
						t.Fatalf("anonymous submission: %d", w.Code)
					}
					continue
				}
				var submitted struct{ ID, Queue string }
				if err := json.Unmarshal(w.Body.Bytes(), &submitted); err != nil || w.Code != 202 {
					t.Fatalf("submission: %d %s", w.Code, w.Body.String())
				}
				info, err := s.inspector.GetTaskInfo(submitted.Queue, submitted.ID)
				if err != nil {
					t.Fatal(err)
				}
				var payload worker.TaskPayload
				if err := json.Unmarshal(info.Payload, &payload); err != nil || payload.Identity != caller {
					t.Fatalf("queued wrong owner: %s", info.Payload)
				}
				// With the option off, another authenticated caller can still read it.
				if !enabled {
					if err := s.logStream.WriteEOF(t.Context(), submitted.ID); err != nil {
						t.Fatal(err)
					}
					for _, suffix := range []string{"", "/logs?follow=false"} {
						r = httptest.NewRequest("GET", "/executions/"+submitted.ID+suffix, nil)
						r.Header.Set("Authorization", "Bearer another-token")
						w = httptest.NewRecorder()
						if mode == "kubernetes" {
							a = verifiedCaller{identity: "bob"}
						}
						s.handler(a).ServeHTTP(w, r)
						if w.Code != 200 {
							t.Fatalf("default read changed: %d %s", w.Code, w.Body.String())
						}
					}
				}
			}
			w := httptest.NewRecorder()
			s.handleHealth(w, httptest.NewRequest("GET", "/health", nil))
			var health struct {
				OwnerOnly bool `json:"execution_owner_only"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &health); err != nil || health.OwnerOnly != enabled {
				t.Fatal("health reports wrong ownership policy")
			}
		}
	}
}
