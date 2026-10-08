package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/redis/go-redis/v9"
	"github.com/rophy/aqsh/internal/config"
	"github.com/rophy/aqsh/internal/tasks"
	"github.com/rophy/aqsh/internal/worker"
)

func mcpFixture(t *testing.T) (*Server, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := &config.Config{
		MCPEnabled: true, RequireIdentity: true, ExecutionAccess: "owner",
		IdentityHeader: "X-Forwarded-User", GroupsHeader: "X-Forwarded-Groups",
		WorkerQueues: []string{"default"}, ResultRetention: time.Hour, LogRetention: time.Hour,
	}
	tc := &tasks.TasksConfig{Tasks: map[string]tasks.TaskDef{
		"inspect/health": {
			Script: "inspect.sh", Queue: "inspections", AllowedGroups: []string{"ops"},
			MCP: &tasks.MCPTool{Enabled: true, Name: "inspect_health", ReadOnly: true, ResultMode: "json"},
			Input: []tasks.Input{
				{Name: "target", Env: "TARGET", Type: "string", Required: true, Pattern: "^[a-z]+$"},
				{Name: "count", Env: "COUNT", Type: "int", Default: "2"},
				{Name: "flag", Env: "FLAG", Type: "bool", Default: "false"},
				{Name: "string_flag", Env: "STRING_FLAG", Type: "string", Default: "true"},
			},
		},
		"private": {Script: "private.sh", AllowedUsers: []string{"alice"},
			MCP: &tasks.MCPTool{Enabled: true, Name: "private_check", ReadOnly: true}},
		"hidden":   {Script: "hidden.sh"},
		"disabled": {Script: "disabled.sh", MCP: &tasks.MCPTool{Name: "disabled", Enabled: false}},
	}}
	s := New(cfg, tc, rdb, asynq.RedisClientOpt{Addr: mr.Addr()}, "test")
	t.Cleanup(func() {
		s.client.Close()
		s.inspector.Close()
		rdb.Close()
	})
	return s, mr
}

type principalTransport struct {
	identity string
	groups   string
}

func (p principalTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Forwarded-User", p.identity)
	r.Header.Set("X-Forwarded-Groups", p.groups)
	return http.DefaultTransport.RoundTrip(r)
}

func mcpClient(t *testing.T, endpoint, identity, groups string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "aqsh-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: endpoint, DisableStandaloneSSE: true, MaxRetries: -1,
		HTTPClient: &http.Client{Transport: principalTransport{identity, groups}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func callMCP(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || result.IsError {
		t.Fatalf("call %s failed: %+v, %v", name, result, err)
	}
	return result
}

func structured(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != string(data) {
		t.Fatalf("structured/text content differ: %+v", result)
	}
	return parsed
}

func TestMCPHTTPDiscoverySubmissionAndOwnership(t *testing.T) {
	s, _ := mcpFixture(t)
	handler, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	alice := mcpClient(t, httpServer.URL+"/mcp", "alice", "ops")
	bob := mcpClient(t, httpServer.URL+"/mcp", "bob", "ops")
	for _, tc := range []struct {
		client *mcp.ClientSession
		want   []string
	}{
		{alice, []string{"aqsh_get_execution", "inspect_health", "private_check"}},
		{bob, []string{"aqsh_get_execution", "inspect_health"}},
	} {
		list, err := tc.client.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0)
		for _, tool := range list.Tools {
			names = append(names, tool.Name)
			data, _ := json.Marshal(tool.InputSchema)
			if strings.Contains(string(data), "TARGET") {
				t.Fatalf("tool schema leaked environment names: %s", data)
			}
		}
		sort.Strings(names)
		if !reflect.DeepEqual(names, tc.want) {
			t.Fatalf("wrong caller catalog: %v, want %v", names, tc.want)
		}
	}
	submitted := structured(t, callMCP(t, alice, "inspect_health", map[string]any{"target": "demo", "count": nil}))
	id := submitted["execution_id"].(string)
	if submitted["status"] != "pending" {
		t.Fatalf("submission claimed completion: %+v", submitted)
	}
	info, err := s.inspector.GetTaskInfo("inspections", id)
	if err != nil {
		t.Fatal(err)
	}
	var payload worker.TaskPayload
	if err := json.Unmarshal(info.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Identity != "alice" || payload.Groups != "ops" || payload.Name != "inspect/health" ||
		payload.Env["TARGET"] != "demo" || payload.Env["COUNT"] != "2" ||
		payload.Env["FLAG"] != "false" || payload.Env["STRING_FLAG"] != "true" {
		t.Fatalf("MCP did not reuse the worker payload/defaults: %+v", payload)
	}
	got := structured(t, callMCP(t, alice, "aqsh_get_execution", map[string]any{"execution_id": id}))
	if got["status"] != "pending" || got["execution_id"] != id || got["identity"] != nil || got["groups"] != nil {
		t.Fatalf("wrong query response: %+v", got)
	}
	for _, name := range []string{"private_check", "hidden", "disabled"} {
		result, err := bob.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err == nil && !result.IsError {
			t.Fatalf("hidden/unauthorized tool %q was callable", name)
		}
	}
	for _, args := range []map[string]any{
		{"target": "demo", "identity": "bob"},
		{"target": "demo", "queue": "other"},
		{"target": "demo", "script": "arbitrary.sh"},
		{"target": "demo", "count": 1.5},
		{"target": "demo", "flag": "false"},
		{"target": "BAD"},
		{},
	} {
		result, err := alice.CallTool(context.Background(), &mcp.CallToolParams{Name: "inspect_health", Arguments: args})
		if err == nil && !result.IsError {
			t.Fatalf("invalid arguments were accepted: %+v", args)
		}
	}
	queue, err := s.inspector.GetQueueInfo("inspections")
	if err != nil || queue.Pending != 1 {
		t.Fatalf("denied calls must enqueue nothing: %+v, %v", queue, err)
	}
	denied, err := bob.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "aqsh_get_execution", Arguments: map[string]any{"execution_id": id},
	})
	if err != nil || !denied.IsError {
		t.Fatalf("another caller read an execution: %+v, %v", denied, err)
	}
	missing, err := bob.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "aqsh_get_execution", Arguments: map[string]any{"execution_id": "missing"},
	})
	if err != nil || !reflect.DeepEqual(structured(t, denied), structured(t, missing)) {
		t.Fatalf("ownership denial exposed existence: %+v, %v", missing, err)
	}
	hidden := restRequest(t, handler, http.MethodPost, "/tasks/hidden", "alice", "ops", "{}")
	var hiddenExecution submission
	if hidden.Code != http.StatusAccepted || json.Unmarshal(hidden.Body.Bytes(), &hiddenExecution) != nil {
		t.Fatal("REST-only task did not remain usable")
	}
	hiddenResult, err := alice.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "aqsh_get_execution", Arguments: map[string]any{"execution_id": hiddenExecution.ID},
	})
	if err != nil || !hiddenResult.IsError {
		t.Fatalf("MCP exposed a REST-only execution: %+v, %v", hiddenResult, err)
	}
	revoked := s.tasks.Tasks["inspect/health"]
	revoked.AllowedGroups = []string{"admin"}
	s.tasks.Tasks["inspect/health"] = revoked
	revokedResult, err := alice.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "aqsh_get_execution", Arguments: map[string]any{"execution_id": id},
	})
	if err != nil || !revokedResult.IsError {
		t.Fatalf("revoked task access remained usable: %+v, %v", revokedResult, err)
	}
	// Removing exposure takes effect on subsequent requests from the same client.
	task := s.tasks.Tasks["inspect/health"]
	task.AllowedGroups = []string{"ops"}
	task.MCP = nil
	s.tasks.Tasks["inspect/health"] = task
	result, err := alice.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "aqsh_get_execution", Arguments: map[string]any{"execution_id": id},
	})
	if err != nil || !result.IsError {
		t.Fatalf("execution of a no-longer-exposed task was readable: %+v, %v", result, err)
	}
}

func TestMCPConcurrentPrincipals(t *testing.T) {
	s, _ := mcpFixture(t)
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	alice := mcpClient(t, server.URL+"/mcp", "alice", "ops")
	bob := mcpClient(t, server.URL+"/mcp", "bob", "ops")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, caller := range []struct {
		identity string
		client   *mcp.ClientSession
	}{
		{"alice", alice}, {"bob", bob},
	} {
		go func() {
			<-start
			for i := 0; i < 5; i++ {
				list, err := caller.client.ListTools(ctx, nil)
				if err != nil {
					results <- err
					return
				}
				private := false
				for _, tool := range list.Tools {
					private = private || tool.Name == "private_check"
				}
				if private != (caller.identity == "alice") {
					results <- fmt.Errorf("catalog for %s mixed caller permissions", caller.identity)
					return
				}
				result, err := caller.client.CallTool(ctx, &mcp.CallToolParams{
					Name: "inspect_health", Arguments: map[string]any{"target": "demo"},
				})
				if err != nil {
					results <- err
					return
				}
				body, ok := result.StructuredContent.(map[string]any)
				id, _ := body["execution_id"].(string)
				if !ok || result.IsError || id == "" {
					results <- fmt.Errorf("submission for %s failed: %+v", caller.identity, result)
					return
				}
				info, err := s.inspector.GetTaskInfo("inspections", id)
				if err != nil {
					results <- err
					return
				}
				var payload worker.TaskPayload
				if err := json.Unmarshal(info.Payload, &payload); err != nil || payload.Identity != caller.identity {
					results <- fmt.Errorf("submission for %s acquired another caller's identity: %+v, %v", caller.identity, payload, err)
					return
				}
			}
			results <- nil
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
}

func restRequest(t *testing.T, h http.Handler, method, path, identity, groups, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Forwarded-User", identity)
	req.Header.Set("X-Forwarded-Groups", groups)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRESTOwnerIsolationAndLegacyCompatibility(t *testing.T) {
	s, _ := mcpFixture(t)
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	submitted := restRequest(t, h, http.MethodPost, "/tasks/inspect%2Fhealth", "alice", "ops", `{"target":"demo"}`)
	if submitted.Code != http.StatusAccepted {
		t.Fatal(submitted.Body.String())
	}
	var execution submission
	if err := json.Unmarshal(submitted.Body.Bytes(), &execution); err != nil {
		t.Fatal(err)
	}
	s.logStream.Write(context.Background(), execution.ID, "sensitive log")
	s.logStream.WriteEOF(context.Background(), execution.ID)
	for _, suffix := range []string{"", "/logs?follow=false"} {
		path := "/executions/" + execution.ID + suffix
		if got := restRequest(t, h, http.MethodGet, path, "alice", "ops", ""); got.Code != http.StatusOK {
			t.Fatalf("owner could not query %s: %d %s", path, got.Code, got.Body.String())
		}
		denied := restRequest(t, h, http.MethodGet, path, "bob", "ops", "")
		missing := restRequest(t, h, http.MethodGet, "/executions/missing"+suffix, "bob", "ops", "")
		if denied.Code != http.StatusNotFound || denied.Body.String() != missing.Body.String() ||
			strings.Contains(denied.Body.String(), "sensitive log") ||
			strings.Contains(denied.Header().Get("Content-Type"), "event-stream") {
			t.Fatalf("ownership failure leaked data or SSE headers: %d %s", denied.Code, denied.Body.String())
		}
		if got := restRequest(t, h, http.MethodGet, path, "", "", ""); got.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous read was allowed: %d", got.Code)
		}
	}
	if got := restRequest(t, h, http.MethodGet, "/tasks/private", "bob", "ops", ""); got.Code != http.StatusNotFound {
		t.Fatalf("private task definition disclosed: %d %s", got.Code, got.Body.String())
	}
	list := restRequest(t, h, http.MethodGet, "/tasks", "bob", "ops", "")
	if strings.Contains(list.Body.String(), "private") {
		t.Fatalf("private task listed: %s", list.Body.String())
	}
	anonymous, err := s.client.Enqueue(asynq.NewTask(worker.TaskType, []byte(`{"name":"inspect/health"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if got := restRequest(t, h, http.MethodGet, "/executions/"+anonymous.ID, "alice", "ops", ""); got.Code != http.StatusNotFound {
		t.Fatalf("anonymous legacy execution should fail closed: %d", got.Code)
	}
	s.cfg.MCPEnabled, s.cfg.RequireIdentity, s.cfg.ExecutionAccess = false, false, "legacy"
	legacy, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	if got := restRequest(t, legacy, http.MethodPost, "/mcp", "alice", "ops", "{}"); got.Code != http.StatusNotFound {
		t.Fatalf("MCP is still exposed when disabled: %d", got.Code)
	}
	if got := restRequest(t, legacy, http.MethodGet, "/executions/"+execution.ID, "", "", ""); got.Code != http.StatusOK {
		t.Fatalf("legacy execution behavior changed: %d %s", got.Code, got.Body.String())
	}
	if got := restRequest(t, legacy, http.MethodPost, "/tasks/hidden", "", "", "{}"); got.Code != http.StatusAccepted {
		t.Fatalf("legacy REST-only task changed: %d %s", got.Code, got.Body.String())
	}
}

func TestMCPHTTPGuardsAndLegacyProtocol(t *testing.T) {
	s, _ := mcpFixture(t)
	s.cfg.MCPAllowedOrigins = []string{"https://client.example"}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, identity, origin, body string
		chunked                      bool
		status                       int
	}{
		{"anonymous", "", "", "{}", false, http.StatusUnauthorized},
		{"bad origin", "alice", "https://evil.example", "{}", false, http.StatusForbidden},
		{"null origin", "alice", "null", "{}", false, http.StatusForbidden},
		{"large body", "alice", "", strings.Repeat(" ", (1<<20)+1), false, http.StatusRequestEntityTooLarge},
		{"large chunked body", "alice", "", strings.Repeat(" ", (1<<20)+1), true, http.StatusRequestEntityTooLarge},
		{"legacy protocol", "alice", "https://client.example", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`, false, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(tc.body))
			req.Header.Set("X-Forwarded-User", tc.identity)
			req.Header.Set("X-Forwarded-Groups", "ops")
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
			if tc.chunked {
				req.ContentLength = -1
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("got %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status == http.StatusOK && !strings.Contains(rec.Body.String(), "inspect_health") {
				t.Fatalf("legacy client did not see the tool: %s", rec.Body.String())
			}
		})
	}
	for _, origins := range [][]string{{""}, {"https://client.example", "https://evil.example"}} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
		req.Header.Set("X-Forwarded-User", "alice")
		for _, origin := range origins {
			req.Header.Add("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("empty or multiple Origin headers were accepted: %v, %d", origins, rec.Code)
		}
	}
}

func TestMCPResultsAreExplicitAndBounded(t *testing.T) {
	s, _ := mcpFixture(t)
	task, _ := s.tasks.Resolve("inspect/health")
	info := &asynq.TaskInfo{ID: "test", State: asynq.TaskStateCompleted, LastErr: "secret error"}
	for _, tc := range []struct {
		name, mode, data, omitted string
		limit                     int
		absent                    bool
	}{
		{"not enabled", "", `{"ok":true}`, "not_enabled", 0, false},
		{"valid JSON", "json", `{"health":"critical"}`, "", 0, false},
		{"absent", "json", "", "absent", 0, true},
		{"empty", "json", "", "empty", 0, false},
		{"malformed", "json", "not JSON", "invalid_json", 0, false},
		{"large", "json", `{"detail":"too long"}`, "too_large", 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task.MCP.ResultMode, task.MCP.MaxResultBytes = tc.mode, tc.limit
			outcome := worker.TaskResult{ExitCode: 1, Error: "secret stderr"}
			if !tc.absent {
				outcome.Data = &tc.data
			}
			info.Result, _ = json.Marshal(outcome)
			result := s.mcpExecutionResult(context.Background(), info, task)
			data, _ := json.Marshal(result)
			if strings.Contains(string(data), "secret") || result["status"] != "completed" {
				t.Fatalf("unsafe result or incorrect health/execution interpretation: %s", data)
			}
			if tc.omitted != "" && result["result_omitted"] != tc.omitted {
				t.Fatalf("wrong omission reason: %+v", result)
			}
			if tc.omitted == "" && result["data"].(map[string]any)["health"] != "critical" {
				t.Fatalf("health result lost: %+v", result)
			}
		})
	}
	// A query of a failed execution remains a successful tool result.
	info.State, info.Result = asynq.TaskStateArchived, nil
	result := mcpResult(s.mcpExecutionResult(context.Background(), info, task))
	if result.IsError || structured(t, result)["status"] != "failed" {
		t.Fatalf("failed execution was treated as a failed query: %+v", result)
	}
}

func TestTargetPolicyDefaultsCacheAndRevocation(t *testing.T) {
	s, _ := mcpFixture(t)
	allowed := []string{"allowed"}
	calls := 0
	var mu sync.Mutex
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		json.NewEncoder(w).Encode(allowed)
	}))
	defer policy.Close()
	task := s.tasks.Tasks["inspect/health"]
	task.Input = []tasks.Input{{Name: "target", Env: "TARGET", Type: "string", Required: true, Default: "blocked", ValuesURL: policy.URL, ValuesCache: "1h"}}
	s.tasks.Tasks["inspect/health"] = task
	for _, body := range []map[string]any{{}, {"target": nil}} {
		if _, err := s.submit(context.Background(), "inspect/health", principal{"alice", "ops"}, body); err == nil {
			t.Fatal("an unauthorized default bypassed target policy")
		}
	}
	execution, err := s.submit(context.Background(), "inspect/health", principal{"alice", "ops"}, map[string]any{"target": "allowed"})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	allowed = []string{"other"}
	mu.Unlock()
	if _, err := s.submit(context.Background(), "inspect/health", principal{"bob", "ops"}, map[string]any{"target": "allowed"}); err == nil {
		t.Fatal("bob reused alice's cached allowed values")
	}
	mu.Lock()
	count := calls
	mu.Unlock()
	if count != 2 {
		t.Fatalf("unexpected policy cache behavior: %d calls", count)
	}
	task.Input[0].ValuesCache = ""
	task.Input[0].Default = "other"
	s.tasks.Tasks["inspect/health"] = task
	if _, _, err := s.readExecution(context.Background(), execution.ID, principal{"alice", "ops"}, true); err == nil {
		t.Fatal("revoked target access was allowed or the stored target was replaced by today's default")
	}
}

func TestPolicyAndRedisFailuresAreClosed(t *testing.T) {
	s, mr := mcpFixture(t)
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "sensitive policy detail", http.StatusInternalServerError)
	}))
	defer policy.Close()
	task := s.tasks.Tasks["inspect/health"]
	task.Input[0].ValuesURL = policy.URL
	s.tasks.Tasks["inspect/health"] = task
	_, err := s.submit(context.Background(), "inspect/health", principal{"alice", "ops"}, map[string]any{"target": "demo"})
	if err == nil || strings.Contains(err.Error(), policy.URL) || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("policy error was accepted or exposed details: %v", err)
	}
	mr.Close()
	_, _, err = s.readExecution(context.Background(), "id", principal{"alice", "ops"}, true)
	if e, ok := err.(*serviceError); !ok || e.Status != http.StatusServiceUnavailable {
		t.Fatalf("Redis failure was mistaken for not found: %v", err)
	}
}
