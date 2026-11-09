package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func newRBACServer(t *testing.T, mode string) (*Server, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := &tasks.TasksConfig{
		Tasks: map[string]tasks.TaskDef{
			"deploy": {Script: "deploy.sh", Queue: "reads", Input: []tasks.Input{{Name: "identity", Env: "INPUT_IDENTITY", Type: "string"}}},
			"hidden": {Script: "hidden.sh"},
		},
		Roles: map[string][]tasks.Rule{
			"runner":    {{Tasks: []string{"deploy"}, Verbs: []string{"run"}}},
			"reader":    {{Tasks: []string{"deploy", "retired"}, Verbs: []string{"read"}}},
			"qualified": {{Tasks: []string{"deploy"}, Verbs: []string{"run"}}},
			"operator":  {{Tasks: []string{"*"}, Verbs: []string{"read", "run"}}},
		},
		Bindings: map[string]tasks.Binding{
			"runner":    {Users: []string{"alice"}, Groups: []string{"ci"}},
			"reader":    {Users: []string{"observer"}, Groups: []string{"ops"}},
			"qualified": {Users: []string{"a/alice"}, Groups: []string{"a/ci"}},
			"operator":  {Users: []string{"operator"}},
		},
	}
	s := New(&config.Config{AuthMode: mode, IdentityHeader: "X-Caller", GroupsHeader: "X-Groups", ResultRetention: time.Hour, LogRetention: time.Hour, WorkerQueues: []string{"default", "historical"}},
		cfg, rdb, asynq.RedisClientOpt{Addr: mr.Addr()}, "test")
	t.Cleanup(func() { s.client.Close(); s.inspector.Close(); rdb.Close() })
	return s, mr
}

func rbacRequest(s *Server, identity string, groups []string, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Last-Event-ID", "0")
	r.Header.Set("X-Forwarded-User", "alice")
	r.Header.Set("X-Forwarded-Groups", "ops")
	r.Header.Set("X-Forwarded-Cluster", "a")
	w := httptest.NewRecorder()
	if s.cfg.AuthMode == "kubernetes" {
		if identity != "" {
			r.Header.Set("Authorization", "Bearer private-token")
		}
		r.Header.Set("X-Caller", "alice")
		r.Header.Set("X-Groups", "ops")
		s.handler(verifiedCaller{identity: identity, groups: groups}).ServeHTTP(w, r)
	} else {
		r.Header.Set("X-Caller", identity)
		r.Header.Set("X-Groups", strings.Join(groups, ","))
		s.handler(nil).ServeHTTP(w, r)
	}
	return w
}

func TestRBACExecutionAccess(t *testing.T) {
	for _, mode := range []string{"trusted-header", "kubernetes"} {
		t.Run(mode, func(t *testing.T) {
			s, mr := newRBACServer(t, mode)
			ids := map[string]string{"missing": "missing", "orphan": "orphan"}
			queues := map[string]string{"missing": "reads", "orphan": "reads"}
			for _, f := range []struct{ key, task, owner, queue, kind, raw string }{
				{key: "owned", task: "deploy", owner: "alice"},
				{key: "qualified", task: "deploy", owner: "a/alice"},
				{key: "legacy", task: "deploy"},
				{key: "retired", task: "retired", owner: "alice", queue: "historical"},
				{key: "hidden", task: "hidden", owner: "alice"},
				{key: "malformed", raw: "not-json"},
				{key: "foreign", task: "deploy", owner: "alice", kind: "other:job"},
				{key: "no task", owner: "alice"},
			} {
				payload, err := json.Marshal(worker.TaskPayload{Name: f.task, Identity: f.owner, Groups: "ops"})
				if err != nil {
					t.Fatal(err)
				}
				if f.raw != "" {
					payload = []byte(f.raw)
				}
				if f.kind == "" {
					f.kind = worker.TaskType
				}
				if f.queue == "" {
					f.queue = "reads"
				}
				info, err := s.client.Enqueue(asynq.NewTask(f.kind, payload), asynq.Queue(f.queue), asynq.Retention(time.Hour))
				if err != nil {
					t.Fatal(err)
				}
				ids[f.key] = info.ID
				queues[f.key] = info.Queue
			}
			for _, id := range ids {
				if err := s.logStream.Write(t.Context(), id, "private-log"); err != nil {
					t.Fatal(err)
				}
				if err := s.logStream.WriteEOF(t.Context(), id); err != nil {
					t.Fatal(err)
				}
			}
			for _, tc := range []struct {
				name, identity, record string
				groups                 []string
				want                   int
			}{
				{"runner owns", "alice", "owned", nil, 200},
				{"same group is not ownership", "bob", "owned", []string{"ci"}, 404},
				{"same group cannot forge owner", "a/alice", "owned", []string{"a/ci"}, 404},
				{"reader sees another owner", "observer", "owned", nil, 200},
				{"read from another role", "alice", "qualified", []string{"ops"}, 200},
				{"group read grants", "bob", "owned", []string{"ops"}, 200},
				{"qualified owner", "a/alice", "qualified", nil, 200},
				{"other cluster", "b/alice", "qualified", []string{"b/ci"}, 404},
				{"unqualified caller", "alice", "qualified", []string{"ci"}, 404},
				{"blank caller", " ", "owned", []string{"ops"}, 401},
				{"missing caller", "", "owned", []string{"ops"}, 401},
				{"identity is exact", " alice ", "owned", nil, 404},
				{"owner without current run", "alice", "hidden", nil, 404},
				{"payload groups do not grant access", "bob", "owned", nil, 404},
				{"runner cannot own legacy record", "alice", "legacy", nil, 404},
				{"reader can read ownerless history", "observer", "legacy", nil, 200},
				{"historical task and queue", "observer", "retired", nil, 200},
				{"wildcard reader", "operator", "hidden", nil, 200},
				{"malformed payload", "operator", "malformed", nil, 404},
				{"foreign payload", "operator", "foreign", nil, 404},
				{"missing task name", "operator", "no task", nil, 404},
				{"missing record", "operator", "missing", nil, 404},
				{"orphan logs", "operator", "orphan", nil, 404},
			} {
				t.Run(tc.name, func(t *testing.T) {
					queue := queues[tc.record]
					for _, suffix := range []string{"", "/logs?follow=false", "/logs", "?queue=" + queue, "/logs?queue=" + queue + "&follow=false", "/logs?queue=" + queue} {
						w := rbacRequest(s, tc.identity, tc.groups, "GET", "/executions/"+ids[tc.record]+suffix, "")
						if w.Code != tc.want {
							t.Fatalf("%s: got %d %s, want %d", suffix, w.Code, w.Body.String(), tc.want)
						}
						if tc.want == 404 && w.Body.String() != "{\"error\":\"task not found\"}\n" {
							t.Fatalf("execution existence leaked: %s", w.Body.String())
						}
						if tc.want != 200 && (strings.Contains(w.Header().Get("Content-Type"), "event-stream") || strings.Contains(w.Body.String(), "private-log")) {
							t.Fatal("authorization happened after streaming")
						}
						if tc.want == 200 && strings.HasPrefix(suffix, "/logs") && !strings.Contains(w.Body.String(), "private-log") {
							t.Fatal("authorized caller did not receive stored logs")
						}
					}
				})
			}
			t.Run("current grants replace submission time grants", func(t *testing.T) {
				delete(s.tasks.Bindings, "runner")
				for _, suffix := range []string{"", "/logs"} {
					if w := rbacRequest(s, "alice", nil, "GET", "/executions/"+ids["owned"]+suffix, ""); w.Code != 404 {
						t.Fatalf("revoked owner still authorized: %d", w.Code)
					}
					if w := rbacRequest(s, "observer", nil, "GET", "/executions/"+ids["owned"]+suffix, ""); w.Code != 200 {
						t.Fatalf("current reader rejected: %d", w.Code)
					}
				}
			})
			t.Run("lookup failures remain distinct from denial", func(t *testing.T) {
				var output bytes.Buffer
				previous := slog.Default()
				slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
				defer slog.SetDefault(previous)
				mr.SetError("private-backend-error")
				defer mr.SetError("")
				for _, suffix := range []string{"", "/logs"} {
					w := rbacRequest(s, "observer", nil, "GET", "/executions/"+ids["owned"]+suffix, "")
					if w.Code != 503 || strings.Contains(w.Body.String(), "private-backend-error") || strings.Contains(w.Header().Get("Content-Type"), "event-stream") {
						t.Fatalf("lookup error mishandled: %d %s", w.Code, w.Body.String())
					}
				}
				if !strings.Contains(output.String(), `"queue":"default"`) || !strings.Contains(output.String(), "private-backend-error") {
					t.Fatalf("lookup diagnostics missing: %s", output.String())
				}
			})
		})
	}
}

func TestRBACTaskAccess(t *testing.T) {
	for _, mode := range []string{"trusted-header", "kubernetes"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := newRBACServer(t, mode)
			var valuesCalls atomic.Int32
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				valuesCalls.Add(1)
				w.Write([]byte(`["safe"]`))
			}))
			defer endpoint.Close()
			for _, name := range []string{"deploy", "hidden"} {
				def := s.tasks.Tasks[name]
				def.Input = append(def.Input, tasks.Input{Name: "target", Env: "TARGET", Type: "string", Required: true, ValuesURL: endpoint.URL})
				s.tasks.Tasks[name] = def
			}
			for _, tc := range []struct {
				name, identity string
				groups         []string
				wantSubmit     int
				visible        bool
			}{
				{"run via user", "alice", nil, 202, true},
				{"run via group", "bob", []string{"ci"}, 202, true},
				{"read only", "observer", nil, 403, true},
				{"run and read", "alice", []string{"ops"}, 202, true},
				{"qualified group", "a/ci-user", []string{"a/ci"}, 202, true},
				{"other cluster group", "b/ci-user", []string{"b/ci"}, 403, false},
				{"no grants", "stranger", nil, 403, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					w := rbacRequest(s, tc.identity, tc.groups, "GET", "/tasks", "")
					if w.Code != 200 || strings.Contains(w.Body.String(), `"deploy"`) != tc.visible || strings.Contains(w.Body.String(), `"hidden"`) {
						t.Fatalf("task catalog leaked or omitted definitions: %d %s", w.Code, w.Body.String())
					}
					before := valuesCalls.Load()
					w = rbacRequest(s, tc.identity, tc.groups, "GET", "/tasks/deploy", "")
					if tc.visible {
						if w.Code != 200 || valuesCalls.Load() != before+1 {
							t.Fatalf("authorized definition failed: %d %s", w.Code, w.Body.String())
						}
					} else if w.Code != 404 || valuesCalls.Load() != before {
						t.Fatal("unauthorized definition fetched remote values")
					}
					for _, method := range []string{"GET", "POST"} {
						before = valuesCalls.Load()
						w = rbacRequest(s, tc.identity, tc.groups, method, "/tasks/hidden", `{}`)
						want := 404
						if method == "POST" {
							want = 403
						}
						if w.Code != want || valuesCalls.Load() != before {
							t.Fatalf("hidden task accessed: %d %s", w.Code, w.Body.String())
						}
					}
					before = valuesCalls.Load()
					w = rbacRequest(s, tc.identity, tc.groups, "POST", "/tasks/deploy", `{"target":"safe","identity":"forged-owner"}`)
					if w.Code != tc.wantSubmit {
						t.Fatalf("submission: got %d %s, want %d", w.Code, w.Body.String(), tc.wantSubmit)
					}
					if tc.wantSubmit == 403 {
						if valuesCalls.Load() != before {
							t.Fatal("read grant fetched values on a denied submission")
						}
						return
					}
					var submitted struct{ ID, Queue string }
					if err := json.Unmarshal(w.Body.Bytes(), &submitted); err != nil {
						t.Fatal(err)
					}
					info, err := s.inspector.GetTaskInfo(submitted.Queue, submitted.ID)
					if err != nil {
						t.Fatal(err)
					}
					var payload worker.TaskPayload
					if err := json.Unmarshal(info.Payload, &payload); err != nil || payload.Identity != tc.identity || payload.Groups != strings.Join(tc.groups, ",") {
						t.Fatalf("task input/headers changed ownership: %s (%v)", info.Payload, err)
					}
				})
			}
		})
	}
}

func TestRBACEmptyPolicyAndLegacy(t *testing.T) {
	for _, mode := range []string{"trusted-header", "kubernetes"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := newRBACServer(t, mode)
			w := rbacRequest(s, "alice", nil, "POST", "/tasks/deploy", `{}`)
			var existing struct{ ID string }
			if err := json.Unmarshal(w.Body.Bytes(), &existing); err != nil || w.Code != 202 || existing.ID == "" {
				t.Fatalf("fixture submission: %d %s (%v)", w.Code, w.Body.String(), err)
			}
			if err := s.logStream.WriteEOF(t.Context(), existing.ID); err != nil {
				t.Fatal(err)
			}
			s.tasks.Roles = map[string][]tasks.Rule{}
			s.tasks.Bindings = nil
			for _, route := range []struct {
				method, path string
				want         int
			}{
				{"GET", "/tasks/deploy", 404}, {"POST", "/tasks/deploy", 403},
				{"GET", "/executions/missing", 404}, {"GET", "/executions/missing/logs", 404},
				{"GET", "/executions/" + existing.ID, 404}, {"GET", "/executions/" + existing.ID + "/logs", 404},
				{"GET", "/executions/" + existing.ID + "?queue=reads", 404}, {"GET", "/executions/" + existing.ID + "/logs?queue=reads", 404},
			} {
				if w := rbacRequest(s, "alice", nil, route.method, route.path, `{}`); w.Code != route.want {
					t.Fatalf("empty policy fell back: %d %s", w.Code, w.Body.String())
				}
			}
			if w := rbacRequest(s, "alice", nil, "GET", "/tasks", ""); w.Code != 200 || w.Body.String() != "{\"tasks\":{}}\n" {
				t.Fatalf("empty policy catalog = %d %s", w.Code, w.Body.String())
			}
			for _, route := range []struct{ method, path string }{
				{"GET", "/tasks"}, {"GET", "/tasks/deploy"}, {"POST", "/tasks/deploy"},
				{"GET", "/executions/missing"}, {"GET", "/executions/missing/logs"},
			} {
				if w := rbacRequest(s, "", []string{"ops"}, route.method, route.path, `{}`); w.Code != 401 {
					t.Fatalf("anonymous RBAC request accepted: %d %s", w.Code, w.Body.String())
				}
			}
			for _, path := range []string{"/health", "/metrics"} {
				if w := rbacRequest(s, "", nil, "GET", path, ""); w.Code != 200 {
					t.Fatalf("public probe rejected: %d", w.Code)
				}
			}
			// No policy must retain legacy visibility, ACL submission checks, and
			// execution reads, including logs without an execution record.
			s.tasks.Roles = nil
			def := s.tasks.Tasks["deploy"]
			def.AllowedUsers = []string{"alice"}
			s.tasks.Tasks["deploy"] = def
			for _, path := range []string{"/tasks", "/tasks/deploy"} {
				if w := rbacRequest(s, "bob", nil, "GET", path, ""); w.Code != 200 {
					t.Fatalf("legacy visibility changed: %d", w.Code)
				}
			}
			if w := rbacRequest(s, "bob", nil, "POST", "/tasks/deploy", `{}`); w.Code != 403 {
				t.Fatal("legacy ACL was bypassed")
			}
			if w := rbacRequest(s, "bob", nil, "POST", "/tasks/missing", `{}`); w.Code != 404 {
				t.Fatalf("legacy unknown task changed: %d %s", w.Code, w.Body.String())
			}
			w = rbacRequest(s, "alice", nil, "POST", "/tasks/deploy", `{}`)
			var submitted struct{ ID string }
			if err := json.Unmarshal(w.Body.Bytes(), &submitted); err != nil || w.Code != 202 {
				t.Fatalf("legacy submit failed: %d %s (%v)", w.Code, w.Body.String(), err)
			}
			if w := rbacRequest(s, "bob", nil, "GET", "/executions/"+submitted.ID, ""); w.Code != 200 {
				t.Fatal("legacy cross-user execution access changed")
			}
			if err := s.logStream.WriteEOF(t.Context(), "orphan"); err != nil {
				t.Fatal(err)
			}
			if w := rbacRequest(s, "bob", nil, "GET", "/executions/orphan/logs", ""); w.Code != 200 {
				t.Fatal("legacy log reads changed")
			}
		})
	}
}

// A future caller of allows must not bypass legacy ACLs in either auth mode.
func TestAllowsLegacyACL(t *testing.T) {
	for _, mode := range []string{"trusted-header", "kubernetes"} {
		for _, tc := range []struct {
			name, identity string
			groups         []string
			def            tasks.TaskDef
			wantRun        bool
		}{
			{name: "open", identity: "alice", wantRun: true},
			{name: "exact user", identity: "alice", def: tasks.TaskDef{AllowedUsers: []string{"alice"}}, wantRun: true},
			{name: "wrong user", identity: "bob", def: tasks.TaskDef{AllowedUsers: []string{"alice"}}},
			{name: "verified group", identity: "bob", groups: []string{"ops"}, def: tasks.TaskDef{AllowedGroups: []string{"ops"}}, wantRun: true},
			{name: "wrong group", identity: "bob", groups: []string{"other"}, def: tasks.TaskDef{AllowedGroups: []string{"ops"}}},
			{name: "OR user", identity: "alice", groups: []string{"other"}, def: tasks.TaskDef{AllowedUsers: []string{"alice"}, AllowedGroups: []string{"ops"}}, wantRun: true},
			{name: "OR group", identity: "bob", groups: []string{"ops"}, def: tasks.TaskDef{AllowedUsers: []string{"alice"}, AllowedGroups: []string{"ops"}}, wantRun: true},
			{name: "forged headers", identity: "bob", groups: []string{"other"}, def: tasks.TaskDef{AllowedUsers: []string{"alice"}, AllowedGroups: []string{"ops"}}},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				s := &Server{cfg: &config.Config{AuthMode: mode, IdentityHeader: "X-Caller", GroupsHeader: "X-Groups"}, tasks: &tasks.TasksConfig{Tasks: map[string]tasks.TaskDef{"deploy": tc.def}}}
				r := httptest.NewRequest("POST", "/tasks/deploy", nil)
				r.Header.Set("X-Caller", tc.identity)
				r.Header.Set("X-Groups", strings.Join(tc.groups, ","))
				var check http.Handler = http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
					if got := s.allows(r, "deploy", "run"); got != tc.wantRun {
						t.Fatalf("legacy run = %v, want %v", got, tc.wantRun)
					}
					if !s.allows(r, "deploy", "read") || !s.allows(r, "", "read") || s.allows(r, "deploy", "delete") {
						t.Fatal("legacy read or unknown-verb behavior changed")
					}
				})
				if mode == "kubernetes" {
					r.Header.Set("Authorization", "Bearer private-token")
					r.Header.Set("X-Caller", "alice")
					r.Header.Set("X-Groups", "ops")
					check = auth.Middleware(verifiedCaller{identity: tc.identity, groups: tc.groups}, "X-Caller", "X-Groups", check)
				}
				check.ServeHTTP(httptest.NewRecorder(), r)
			})
		}
	}
}

// A queue parameter locates records without granting access or falling back.
func TestExecutionQueueSelection(t *testing.T) {
	for _, mode := range []string{"trusted-header", "kubernetes"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := newRBACServer(t, mode)
			w := rbacRequest(s, "alice", nil, "POST", "/tasks/deploy", `{}`)
			var submitted struct{ ID string }
			if err := json.Unmarshal(w.Body.Bytes(), &submitted); err != nil || w.Code != 202 || submitted.ID == "" {
				t.Fatalf("submission: %d %s (%v)", w.Code, w.Body.String(), err)
			}
			if err := s.logStream.WriteEOF(t.Context(), submitted.ID); err != nil {
				t.Fatal(err)
			}
			for _, suffix := range []string{"", "/logs"} {
				for _, queue := range []string{"default", "missing"} {
					w := rbacRequest(s, "alice", nil, "GET", "/executions/"+submitted.ID+suffix+"?queue="+queue, "")
					if w.Code != 404 || strings.Contains(w.Header().Get("Content-Type"), "event-stream") {
						t.Fatalf("wrong queue fell back: %d %s", w.Code, w.Body.String())
					}
				}
			}
			for _, duration := range []string{"timeout", "retry_delay", "defaults", "inherited queue"} {
				def := s.tasks.Tasks["deploy"]
				def.Timeout, def.RetryDelay = "", ""
				if duration == "timeout" {
					def.Timeout = "invalid"
				} else if duration == "retry_delay" {
					def.RetryDelay = "invalid"
				} else {
					s.tasks.Defaults.Timeout, s.tasks.Defaults.RetryDelay = "invalid", "invalid"
					if duration == "inherited queue" {
						def.Queue = ""
						s.tasks.Defaults.Queue = "reads"
					}
				}
				s.tasks.Tasks["deploy"] = def
				for _, suffix := range []string{"", "/logs", "?queue=reads", "/logs?queue=reads"} {
					if w := rbacRequest(s, "alice", nil, "GET", "/executions/"+submitted.ID+suffix, ""); w.Code != 200 {
						t.Fatalf("%s blocked retained execution: %d %s", duration, w.Code, w.Body.String())
					}
				}
			}
			delete(s.tasks.Tasks, "deploy")
			s.tasks.Defaults.Queue = ""
			if w := rbacRequest(s, "alice", nil, "GET", "/executions/"+submitted.ID, ""); w.Code != 404 {
				t.Fatalf("unlisted queue unexpectedly discovered: %d %s", w.Code, w.Body.String())
			}
			for _, suffix := range []string{"?queue=reads", "/logs?queue=reads"} {
				if w := rbacRequest(s, "alice", nil, "GET", "/executions/"+submitted.ID+suffix, ""); w.Code != 200 {
					t.Fatalf("retired queue inaccessible: %d %s", w.Code, w.Body.String())
				}
			}
		})
	}
}

// Explicit queue lookups must not issue Redis reads for unrelated task queues.
func TestExecutionQueueLookupCost(t *testing.T) {
	s, mr := newRBACServer(t, "trusted-header")
	if err := s.rdb.SAdd(t.Context(), "asynq:queues", "reads").Err(); err != nil {
		t.Fatal(err)
	}
	lookup := func() int {
		before := mr.CommandCount()
		if w := rbacRequest(s, "observer", nil, "GET", "/executions/missing?queue=reads", ""); w.Code != 404 {
			t.Fatalf("missing execution: %d %s", w.Code, w.Body.String())
		}
		return mr.CommandCount() - before
	}
	lookup() // Warm the connection and Asynq's lookup script.
	before := lookup()
	for i := range 1000 {
		s.tasks.Tasks[fmt.Sprintf("other-%d", i)] = tasks.TaskDef{Queue: fmt.Sprintf("other-queue-%d", i)}
	}
	if after := lookup(); before == 0 || after != before {
		t.Fatalf("queue lookup cost grew: %d commands before, %d after", before, after)
	}
}
