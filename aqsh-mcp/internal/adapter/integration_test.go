package adapter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

func unusedAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}

func startProcess(t *testing.T, path string, args, env []string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "process.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		cmd.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			cmd.Process.Kill()
			<-done
		}
		log.Close()
		if t.Failed() {
			data, _ := os.ReadFile(logPath)
			t.Logf("%s output:\n%s", filepath.Base(path), data)
		}
	})
}

func waitHTTP(t *testing.T, location string) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(location)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("service did not become ready: %s", location)
}

func TestRealQueueAndWorker(t *testing.T) {
	aqsh, mcpBinary := os.Getenv("AQSH_BINARY"), os.Getenv("AQSH_MCP_BINARY")
	if aqsh == "" || mcpBinary == "" {
		t.Skip("set AQSH_BINARY and AQSH_MCP_BINARY for real Redis/shell integration")
	}
	redis, err := exec.LookPath("redis-server")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	redisAddr, aqshAddr, mcpAddr := unusedAddress(t), unusedAddress(t), unusedAddress(t)
	_, redisPort, _ := net.SplitHostPort(redisAddr)
	startProcess(t, redis, []string{"--bind", "127.0.0.1", "--port", redisPort, "--save", "", "--appendonly", "no", "--dir", dir}, nil)
	i := newIssuer(t)
	policy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("user") != identity(i.server.URL, "alice") {
			http.Error(w, "not allowed", 403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"name":"Allowed target","value":"allowed"}]`))
	}))
	t.Cleanup(policy.Close)
	script := `#!/bin/sh
set -eu
echo invoked >> "${MARKER_FILE}"
echo private-log-secret
printf '{"healthy":false,"target":"%s","count":%s}\n' "$TARGET" "$COUNT" > "$AQSH_RESULT_FILE"
`
	if err := os.WriteFile(filepath.Join(dir, "inspect.sh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	tasks := fmt.Sprintf(`defaults:
  timeout: 5s
  max_retry: 0
  queue: reads
tasks:
  inspect:
    script: inspect.sh
    allowed_groups: [db-readers]
    input:
      - {name: target, env: TARGET, type: string, default: allowed, values_url: "%s?user=${identity}"}
      - {name: count, env: COUNT, type: int, default: "2"}
  blocked-default:
    script: inspect.sh
    allowed_groups: [db-readers]
    input:
      - {name: target, env: TARGET, type: string, default: denied, values_url: "%s?user=${identity}"}
      - {name: count, env: COUNT, type: int, default: "2"}
  mutate:
    script: inspect.sh
`, policy.URL, policy.URL)
	tasksPath := filepath.Join(dir, "tasks.yaml")
	if err := os.WriteFile(tasksPath, []byte(tasks), 0600); err != nil {
		t.Fatal(err)
	}
	// Wait for Redis without depending on another Redis client library in the adapter.
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", redisAddr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	startProcess(t, aqsh, []string{"-tasks", tasksPath, "-bind", aqshAddr}, []string{
		"AQSH_MODE=both", "AQSH_REDIS_ADDR=" + redisAddr, "AQSH_REDIS_PASSWORD=", "AQSH_REDIS_DB=0", "AQSH_REDIS_SENTINEL_ADDRS=",
		"AQSH_TASKS_DIR=" + dir, "AQSH_RESULTS_DIR=" + filepath.Join(dir, "results"), "AQSH_WORKER_QUEUES=default,reads",
		"AQSH_EXECUTION_OWNER_ONLY=true", "AQSH_REQUIRE_IDENTITY=true", "AQSH_IDENTITY_HEADER=X-Forwarded-User", "AQSH_GROUPS_HEADER=X-Forwarded-Groups",
		"MARKER_FILE=" + filepath.Join(dir, "invocations"),
	})
	waitHTTP(t, "http://"+aqshAddr+"/health")
	cfg := Config{Bind: mcpAddr, PublicURL: "http://" + mcpAddr + "/mcp", BackendURL: "http://" + aqshAddr,
		OAuth: OAuthConfig{Issuer: i.server.URL, RequiredScopes: []string{"aqsh:read"}, GroupMapping: map[string]string{"ops": "db-readers"}},
		Tools: []ToolConfig{{Name: "inspect", Task: "inspect", ReadOnly: true, ResultMode: "json"}, {Name: "blocked_default", Task: "blocked-default", ReadOnly: true}},
	}
	data, _ := yaml.Marshal(cfg)
	configPath := filepath.Join(dir, "adapter.yaml")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	startProcess(t, mcpBinary, []string{"-config", configPath}, nil)
	waitHTTP(t, "http://"+mcpAddr+"/health")
	alice := connectOAuth(t, cfg.PublicURL)
	submitted := call(t, alice, "inspect", map[string]any{})
	if submitted.IsError {
		t.Fatalf("submission failed: %+v", submitted)
	}
	id := content(t, submitted)["execution_id"].(string)
	var completed *mcp.CallToolResult
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		completed = call(t, alice, "aqsh_get_execution", map[string]any{"execution_id": id})
		if completed.IsError {
			t.Fatalf("poll failed: %+v", completed)
		}
		if content(t, completed)["status"] == "completed" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	out := content(t, completed)
	if out["status"] != "completed" || out["exit_code"] != float64(0) {
		t.Fatalf("execution did not complete: %+v", out)
	}
	result := out["data"].(map[string]any)
	if result["healthy"] != false || result["target"] != "allowed" || result["count"] != float64(2) {
		t.Fatalf("shell result: %+v", result)
	}
	encoded, _ := json.Marshal(completed)
	if strings.Contains(string(encoded), "private-log-secret") {
		t.Fatal("logs exposed through MCP")
	}
	for _, args := range []map[string]any{{}, {"target": nil}} {
		if !call(t, alice, "blocked_default", args).IsError {
			t.Fatal("forbidden default enqueued")
		}
	}
	if result, err := alice.CallTool(t.Context(), &mcp.CallToolParams{Name: "mutate", Arguments: map[string]any{}}); err == nil && !result.IsError {
		t.Fatal("hidden mutation accepted")
	}
	bob := connect(t, cfg.PublicURL, i.token(t, cfg.PublicURL, "bob", nil))
	if !call(t, bob, "aqsh_get_execution", map[string]any{"execution_id": id}).IsError {
		t.Fatal("other owner read execution")
	}
	for _, tc := range []struct {
		path, caller string
		status       int
	}{
		{"/executions/" + id, identity(i.server.URL, "bob"), 404},
		{"/executions/" + id + "/logs?follow=false", identity(i.server.URL, "bob"), 404},
		{"/executions/" + id, "", 401},
		{"/executions/" + id, identity(i.server.URL, "alice"), 200},
		{"/executions/" + id + "?include_result_json=true", identity(i.server.URL, "alice"), 200},
	} {
		req, _ := http.NewRequest("GET", "http://"+aqshAddr+tc.path, nil)
		req.Header.Set("X-Forwarded-User", tc.caller)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Fatalf("REST %s: %d %s", tc.path, resp.StatusCode, data)
		}
		if tc.status == 200 {
			var value map[string]json.RawMessage
			if json.Unmarshal(data, &value) != nil {
				t.Fatal("REST invalid JSON")
			}
			var name string
			json.Unmarshal(value["task"], &name)
			if name != "inspect" {
				t.Fatal("task name missing")
			}
			var result map[string]json.RawMessage
			json.Unmarshal(value["result"], &result)
			var raw string
			if json.Unmarshal(result["data"], &raw) != nil || !json.Valid([]byte(raw)) {
				t.Fatal("legacy result.data changed")
			}
			if strings.Contains(tc.path, "include_result_json") != (len(result["json"]) > 0) {
				t.Fatal("JSON opt-in contract broken")
			}
		}
	}
	marker, err := os.ReadFile(filepath.Join(dir, "invocations"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(marker, []byte("invoked")) != 1 {
		t.Fatalf("unexpected shell executions: %s", marker)
	}
}
