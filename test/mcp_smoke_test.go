package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This runs the production binary, a real Redis, and a real shell task. It is
// skipped on machines without redis-server; no database or cluster is required.
func TestMCPRealWorkerSmoke(t *testing.T) {
	redisBinary, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server is required for the production-worker smoke test")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "aqsh")
	build := exec.Command("go", "build", "-o", binary, "./cmd/aqsh")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	writeFixture(t, filepath.Join(dir, "inspect.sh"), `#!/bin/sh
set -eu
printf '{"target":"%s","flag":"%s","owner":"%s"}\n' "$TARGET" "$FLAG" "$AQSH_IDENTITY" > "$AQSH_RESULT_FILE"
echo "inspection finished"
`, 0700)
	writeFixture(t, filepath.Join(dir, "tasks.yaml"), `defaults:
  max_retry: 0
tasks:
  inspect:
    script: inspect.sh
    allowed_users: [alice]
    mcp:
      enabled: true
      name: inspect
      read_only: true
      result_mode: json
    input:
      - name: target
        env: TARGET
        type: string
        required: true
        pattern: '^[a-z]+$'
      - name: flag
        env: FLAG
        type: bool
        default: "false"
`, 0600)
	redisPort, apiPort := freePort(t), freePort(t)
	for apiPort == redisPort {
		apiPort = freePort(t)
	}
	redisLog := startProcess(t, dir, "redis", redisBinary,
		"--bind", "127.0.0.1", "--port", redisPort, "--save", "", "--appendonly", "no", "--dir", dir)
	if !waitUntil(10*time.Second, func() bool {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+redisPort, 100*time.Millisecond)
		if err == nil {
			conn.Close()
		}
		return err == nil
	}) {
		t.Fatalf("Redis did not start: %s", readLog(redisLog))
	}
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(),
		"AQSH_MODE=both", "AQSH_BIND=127.0.0.1:"+apiPort,
		"AQSH_REDIS_ADDR=127.0.0.1:"+redisPort, "AQSH_REDIS_PASSWORD=", "AQSH_REDIS_DB=0",
		"AQSH_REDIS_SENTINEL_ADDRS=", "AQSH_WORKER_QUEUES=default", "AQSH_WORKER_CONCURRENCY=1",
		"AQSH_TASKS_CONFIG="+filepath.Join(dir, "tasks.yaml"), "AQSH_TASKS_DIR="+dir,
		"AQSH_RESULTS_DIR="+filepath.Join(dir, "results"),
		"AQSH_MCP_ENABLED=true", "AQSH_EXECUTION_ACCESS=owner", "AQSH_REQUIRE_IDENTITY=true",
		"AQSH_IDENTITY_HEADER=X-Forwarded-User", "AQSH_GROUPS_HEADER=X-Forwarded-Groups",
		"AQSH_MCP_ALLOWED_ORIGINS=",
	)
	serverLog := runProcess(t, dir, "aqsh", cmd)
	baseURL := "http://127.0.0.1:" + apiPort
	httpClient := &http.Client{Timeout: time.Second}
	if !waitUntil(15*time.Second, func() bool {
		resp, err := httpClient.Get(baseURL + "/health")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}) {
		t.Fatalf("aqsh did not start: %s", readLog(serverLog))
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "smoke"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: baseURL + "/mcp", DisableStandaloneSSE: true, MaxRetries: -1,
		HTTPClient: &http.Client{Transport: callerTransport{"alice"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	submitted, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "inspect", Arguments: map[string]any{"target": "demo"}})
	if err != nil || submitted.IsError {
		t.Fatalf("submit failed: %+v, %v", submitted, err)
	}
	id := decodeResult(t, submitted)["execution_id"].(string)
	var completed map[string]any
	if !waitUntil(15*time.Second, func() bool {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "aqsh_get_execution", Arguments: map[string]any{"execution_id": id}})
		if err != nil || result.IsError {
			t.Fatalf("query failed: %+v, %v", result, err)
		}
		completed = decodeResult(t, result)
		return completed["status"] == "completed" || completed["status"] == "failed"
	}) {
		t.Fatalf("worker did not finish: %+v\n%s", completed, readLog(serverLog))
	}
	data, ok := completed["data"].(map[string]any)
	if completed["status"] != "completed" || !ok || data["target"] != "demo" ||
		data["flag"] != "false" || data["owner"] != "alice" {
		t.Fatalf("real shell execution/defaults/identity failed: %+v\n%s", completed, readLog(serverLog))
	}
	// The same real execution is visible through REST to its owner only.
	for _, caller := range []string{"alice", "bob"} {
		for _, suffix := range []string{"", "/logs?follow=false"} {
			req, _ := http.NewRequest(http.MethodGet, baseURL+"/executions/"+id+suffix, nil)
			req.Header.Set("X-Forwarded-User", caller)
			resp, err := httpClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			want := http.StatusOK
			if caller == "bob" {
				want = http.StatusNotFound
			}
			if resp.StatusCode != want {
				t.Fatalf("REST %s access for %s: got %d, want %d", suffix, caller, resp.StatusCode, want)
			}
		}
	}
}

type callerTransport struct{ identity string }

func (c callerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("X-Forwarded-User", c.identity)
	return http.DefaultTransport.RoundTrip(req)
}

func decodeResult(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func writeFixture(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return fmt.Sprint(listener.Addr().(*net.TCPAddr).Port)
}

func startProcess(t *testing.T, dir, name, binary string, args ...string) string {
	t.Helper()
	return runProcess(t, dir, name, exec.Command(binary, args...))
}

func runProcess(t *testing.T, dir, name string, cmd *exec.Cmd) string {
	t.Helper()
	path := filepath.Join(dir, name+".log")
	log, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = log, log
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
		case <-time.After(10 * time.Second):
			cmd.Process.Kill()
			<-done
		}
		log.Close()
	})
	return path
}

func waitUntil(timeout time.Duration, ready func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func readLog(path string) string {
	data, _ := os.ReadFile(path)
	return strings.TrimSpace(string(data))
}
