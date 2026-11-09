package nativeauth_test

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/rophy/aqsh/internal/worker"
)

var requireE2E = flag.Bool("require-e2e", false, "fail if the E2E environment is missing")

// This test is run by the native-auth Skaffold profiles inside a real cluster.
// Ordinary go test ./... skips it rather than substituting a mock TokenReview.
func TestNativeAuthenticationE2E(t *testing.T) {
	base := os.Getenv("AQSH_NATIVE_E2E_URL")
	if base == "" {
		if *requireE2E {
			t.Fatal("AQSH_NATIVE_E2E_URL is required")
		}
		t.Skip("run test/e2e.sh with the native-auth Skaffold profile")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile("/var/run/aqsh-e2e/" + name)
		if err != nil || len(bytes.TrimSpace(data)) == 0 {
			t.Fatalf("read projected %s: %v", name, err)
		}
		return bytes.TrimSpace(data)
	}
	caller := string(read("token"))
	wrongAudience := string(read("wrong-audience"))
	apiToken := string(read("api-token"))
	const username = "system:serviceaccount:aqsh:aqsh-e2e-caller"
	group := "system:serviceaccounts:aqsh"
	acceptedGroup, rejectedGroup := "serviceaccount-group", "qualified-serviceaccount-group"
	identity, accepted, rejected := username, "identity", "qualified-identity"
	qualified := os.Getenv("AQSH_NATIVE_E2E_QUALIFIED") == "true"
	if qualified {
		identity, accepted, rejected = "kind-e2e/"+username, rejected, accepted
		group = "kind-e2e/" + group
		acceptedGroup, rejectedGroup = rejectedGroup, acceptedGroup
	}
	request := func(t *testing.T, c *http.Client, method, url, token, body string, headers map[string]string, want int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, url, err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			t.Fatalf("%s %s: got status %d, want %d", method, url, resp.StatusCode, want)
		}
		if want >= 400 && strings.Contains(resp.Header.Get("Content-Type"), "event-stream") {
			t.Fatal("denial started an SSE stream")
		}
		return data
	}
	call := func(t *testing.T, method, path, token string, headers map[string]string, want int) []byte {
		t.Helper()
		return request(t, client, method, base+path, token, "{}", headers, want)
	}

	// The caller can use its API-audience token but cannot create TokenReviews.
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(read("ca.crt")) {
		t.Fatal("invalid cluster CA")
	}
	kube := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
	}}
	defer kube.CloseIdleConnections()
	tokenRequest := `{"apiVersion":"authentication.k8s.io/v1","kind":"TokenRequest","spec":{"audiences":["aqsh-e2e"],"expirationSeconds":600}}`
	mint := func(name string) string {
		t.Helper()
		data := request(t, kube, "POST", "https://kubernetes.default.svc/api/v1/namespaces/aqsh/serviceaccounts/"+name+"/token", apiToken, tokenRequest, nil, 201)
		var response struct{ Status struct{ Token string } }
		if err := json.Unmarshal(data, &response); err != nil || response.Status.Token == "" {
			t.Fatal("missing secondary caller token")
		}
		return response.Status.Token
	}
	other, reader := mint("aqsh-e2e-other"), mint("aqsh-e2e-reader")
	t.Run("caller cannot act as reviewer", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"apiVersion": "authentication.k8s.io/v1", "kind": "TokenReview", "spec": map[string]any{"token": caller, "audiences": []string{"aqsh-e2e"}}})
		request(t, kube, "POST", "https://kubernetes.default.svc/apis/authentication.k8s.io/v1/tokenreviews", apiToken, string(body), nil, 403)
	})
	t.Run("caller cannot mint a reviewer token", func(t *testing.T) {
		request(t, kube, "POST", "https://kubernetes.default.svc/api/v1/namespaces/aqsh/serviceaccounts/aqsh-api-reviewer/token", apiToken, tokenRequest, nil, 403)
	})
	for _, path := range []string{"/health", "/metrics"} {
		t.Run("public "+path, func(t *testing.T) { call(t, "GET", path, "", nil, 200) })
	}
	for _, route := range []struct{ method, path string }{
		{"GET", "/tasks"}, {"GET", "/tasks/identity"}, {"POST", "/tasks/identity"},
		{"GET", "/executions/missing"}, {"GET", "/executions/missing/logs"},
		{"POST", "/debug/coverage/flush"}, {"HEAD", "/health"},
	} {
		t.Run("protect "+route.method+" "+route.path, func(t *testing.T) { call(t, route.method, route.path, "", nil, 401) })
	}
	t.Run("invalid token", func(t *testing.T) { call(t, "GET", "/tasks", "not-a-valid-token", nil, 401) })
	t.Run("API token cannot be replayed", func(t *testing.T) { call(t, "GET", "/tasks", apiToken, nil, 401) })
	t.Run("wrong token audience", func(t *testing.T) { call(t, "GET", "/tasks", wrongAudience, nil, 401) })
	t.Run("forwarded headers cannot authenticate", func(t *testing.T) {
		call(t, "POST", "/tasks/"+accepted, "", map[string]string{"X-Forwarded-User": identity, "X-Forwarded-Groups": group}, 401)
	})
	t.Run("forged user cannot authorize", func(t *testing.T) {
		call(t, "POST", "/tasks/forbidden-user", caller, map[string]string{"X-Forwarded-User": "system:serviceaccount:aqsh:someone-else"}, 403)
	})
	t.Run("forged groups cannot authorize", func(t *testing.T) {
		call(t, "POST", "/tasks/forbidden-group", caller, map[string]string{"X-Forwarded-Groups": "ops-team"}, 403)
	})
	t.Run("identity format controls RBAC", func(t *testing.T) { call(t, "POST", "/tasks/"+rejected, caller, nil, 403) })
	t.Run("verified Kubernetes group authorizes", func(t *testing.T) { call(t, "POST", "/tasks/"+acceptedGroup, caller, nil, 202) })

	t.Run("group format controls RBAC", func(t *testing.T) { call(t, "POST", "/tasks/"+rejectedGroup, caller, nil, 403) })
	t.Run("other cluster group cannot authorize", func(t *testing.T) { call(t, "POST", "/tasks/other-cluster-group", caller, nil, 403) })
	t.Run("task catalog and definitions follow grants", func(t *testing.T) {
		var catalog struct{ Tasks map[string]any }
		if err := json.Unmarshal(call(t, "GET", "/tasks", caller, nil, 200), &catalog); err != nil {
			t.Fatal(err)
		}
		if _, ok := catalog.Tasks[accepted]; !ok {
			t.Fatal("run permission did not expose task definition")
		}
		for _, name := range []string{rejected, rejectedGroup, "forbidden-user", "forbidden-group", "other-cluster-group"} {
			if _, ok := catalog.Tasks[name]; ok {
				t.Fatal("catalog exposed an unauthorized task:", name)
			}
			call(t, "GET", "/tasks/"+name, caller, nil, 404)
		}
		call(t, "GET", "/tasks/"+accepted, caller, nil, 200)
		call(t, "GET", "/tasks/shared-run", reader, nil, 200)
		call(t, "POST", "/tasks/shared-run", reader, nil, 403)
	})
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: "redis:6379"})
	defer inspector.Close()
	waitCompleted := func(t *testing.T, queue, id string) *asynq.TaskInfo {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		for {
			info, err := inspector.GetTaskInfo(queue, id)
			if err != nil {
				t.Fatal(err)
			}
			if info.State == asynq.TaskStateCompleted {
				return info
			}
			if info.State == asynq.TaskStateArchived || time.Now().After(deadline) {
				t.Fatal("worker did not complete task:", info.State)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	t.Run("API Redis worker result and logs", func(t *testing.T) {
		submitted := call(t, "POST", "/tasks/"+accepted, caller, map[string]string{
			"X-Forwarded-User": "forged-user", "X-Forwarded-Groups": "ops-team",
			"X-Forwarded-Cluster": "forged-cluster", "X-Forwarded-Extra-Cluster-Name": "forged-cluster",
		}, 202)
		var task struct{ ID, Queue string }
		if err := json.Unmarshal(submitted, &task); err != nil || task.ID == "" || task.Queue == "" {
			t.Fatal("missing submitted task ID/queue")
		}
		info := waitCompleted(t, task.Queue, task.ID)
		var payload worker.TaskPayload
		if err := json.Unmarshal(info.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		groups := strings.Split(payload.Groups, ",")
		if payload.Identity != identity || !slices.Contains(groups, group) || slices.Contains(groups, "ops-team") {
			t.Fatal("queue lost verified identity/groups")
		}
		if qualified {
			for _, g := range groups {
				if !strings.HasPrefix(g, "kind-e2e/") {
					t.Fatal("queue contains an unqualified group")
				}
			}
		}
		execution := call(t, "GET", "/executions/"+task.ID, caller, nil, 200)
		var result struct {
			Status, Identity, Groups string
			Result                   worker.TaskResult
		}
		if err := json.Unmarshal(execution, &result); err != nil {
			t.Fatal(err)
		}
		if result.Status != "completed" || result.Identity != identity || result.Groups != payload.Groups || result.Result.ExitCode != 0 || result.Result.Data == nil {
			t.Fatal("API result lost verified identity or successful worker result")
		}
		var output struct {
			Identity, Groups string
			ReviewerMounted  *bool `json:"reviewer_mounted"`
		}
		if err := json.Unmarshal([]byte(*result.Result.Data), &output); err != nil {
			t.Fatal(err)
		}
		if output.Identity != identity || output.Groups != result.Groups || output.ReviewerMounted == nil || *output.ReviewerMounted {
			t.Fatal("worker identity or credential isolation failed")
		}
		logs := call(t, "GET", "/executions/"+task.ID+"/logs", caller, nil, 200)
		if !bytes.Contains(logs, []byte("identity="+identity)) || !bytes.Contains(logs, []byte("event: eof")) {
			t.Fatal("missing worker identity or EOF in logs")
		}
		queue := url.QueryEscape(task.Queue)
		if selected := call(t, "GET", "/executions/"+task.ID+"?queue="+queue, caller, nil, 200); !bytes.Equal(selected, execution) {
			t.Fatal("queue selection changed execution results")
		}
		if selected := call(t, "GET", "/executions/"+task.ID+"/logs?queue="+queue, caller, nil, 200); !bytes.Equal(selected, logs) {
			t.Fatal("queue selection changed stored logs")
		}
		for _, suffix := range []string{"", "/logs"} {
			call(t, "GET", "/executions/"+task.ID+suffix+"?queue=default", caller, nil, 404)
		}
		// A separate real principal can read via its read grant, but cannot run.
		call(t, "GET", "/executions/"+task.ID, reader, nil, 200)
		call(t, "GET", "/executions/"+task.ID+"/logs", reader, nil, 200)
		call(t, "POST", "/tasks/"+accepted, reader, nil, 403)
		missing := call(t, "GET", "/executions/missing", other, nil, 404)
		for _, suffix := range []string{"", "/logs?follow=false", "/logs", "?queue=" + queue, "/logs?queue=" + queue} {
			denied := call(t, "GET", "/executions/"+task.ID+suffix, other, map[string]string{"X-Forwarded-User": identity, "X-Forwarded-Groups": group, "Last-Event-ID": "0"}, 404)
			if !bytes.Equal(denied, missing) {
				t.Fatal("denial exposed execution existence")
			}
		}
		for _, data := range [][]byte{info.Payload, execution, logs} {
			for _, secret := range []string{caller, wrongAudience, apiToken, other, reader, "forged-user", "forged-cluster", "ops-team"} {
				if bytes.Contains(data, []byte(secret)) {
					t.Fatal("credential or forged identity leaked into queue/result/logs")
				}
			}
		}
		call(t, "GET", "/executions/"+task.ID, "", nil, 401)
		call(t, "GET", "/executions/"+task.ID+"/logs", "not-a-valid-token", nil, 401)
	})
	t.Run("run-only callers own private executions", func(t *testing.T) {
		var jobs []struct{ ID, Queue string }
		for _, token := range []string{caller, other} {
			var job struct{ ID, Queue string }
			if err := json.Unmarshal(call(t, "POST", "/tasks/shared-run", token, nil, 202), &job); err != nil || job.ID == "" {
				t.Fatal("missing shared task execution")
			}
			jobs = append(jobs, job)
			info := waitCompleted(t, job.Queue, job.ID)
			var payload worker.TaskPayload
			if err := json.Unmarshal(info.Payload, &payload); err != nil || !slices.Contains(strings.Split(payload.Groups, ","), group) {
				t.Fatal("runner lost shared verified group")
			}
			for _, suffix := range []string{"", "/logs", "?queue=" + url.QueryEscape(job.Queue), "/logs?queue=" + url.QueryEscape(job.Queue)} {
				call(t, "GET", "/executions/"+job.ID+suffix, token, nil, 200)
				call(t, "GET", "/executions/"+job.ID+suffix, reader, nil, 200)
			}
		}
		for i, token := range []string{other, caller} {
			for _, suffix := range []string{"", "/logs?follow=false", "/logs", "?queue=" + url.QueryEscape(jobs[i].Queue), "/logs?queue=" + url.QueryEscape(jobs[i].Queue)} {
				call(t, "GET", "/executions/"+jobs[i].ID+suffix, token, map[string]string{"X-Forwarded-User": identity, "X-Forwarded-Groups": group, "Last-Event-ID": "0"}, 404)
			}
		}
		if err := inspector.DeleteTask(jobs[0].Queue, jobs[0].ID); err != nil {
			t.Fatal(err)
		}
		for _, token := range []string{caller, reader} {
			for _, suffix := range []string{"", "/logs", "?queue=" + url.QueryEscape(jobs[0].Queue), "/logs?queue=" + url.QueryEscape(jobs[0].Queue)} {
				call(t, "GET", "/executions/"+jobs[0].ID+suffix, token, nil, 404)
			}
		}
	})
}
