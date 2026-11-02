package nativeauth_test

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/rophy/aqsh/internal/worker"
)

// This test is run by the native-auth Skaffold profiles inside a real cluster.
// Ordinary go test ./... skips it rather than substituting a mock TokenReview.
func TestNativeAuthenticationE2E(t *testing.T) {
	base := os.Getenv("AQSH_NATIVE_E2E_URL")
	if base == "" {
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
	const group = "system:serviceaccounts:aqsh"
	identity, accepted, rejected := username, "identity", "qualified-identity"
	qualified := os.Getenv("AQSH_NATIVE_E2E_QUALIFIED") == "true"
	if qualified {
		identity, accepted, rejected = "kind-e2e/"+username, rejected, accepted
	}
	request := func(t *testing.T, c *http.Client, method, url, token, body string, headers map[string]string, want int) []byte {
		t.Helper()
		req, err := http.NewRequest(method, url, strings.NewReader(body))
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
		return data
	}
	call := func(t *testing.T, method, path, token string, headers map[string]string, want int) []byte {
		t.Helper()
		return request(t, client, method, base+path, token, "{}", headers, want)
	}

	// Read the actual ServiceAccount UID for comparison with the queued Principal.
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(read("ca.crt")) {
		t.Fatal("invalid cluster CA")
	}
	kube := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
	}}
	defer kube.CloseIdleConnections()
	sa := request(t, kube, "GET", "https://kubernetes.default.svc/api/v1/namespaces/aqsh/serviceaccounts/aqsh-e2e-caller", apiToken, "", nil, 200)
	var account struct {
		Metadata struct {
			UID string `json:"uid"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(sa, &account); err != nil || account.Metadata.UID == "" {
		t.Fatal("missing live ServiceAccount UID")
	}
	t.Run("caller cannot act as reviewer", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"apiVersion": "authentication.k8s.io/v1", "kind": "TokenReview", "spec": map[string]any{"token": caller, "audiences": []string{"aqsh-e2e"}}})
		request(t, kube, "POST", "https://kubernetes.default.svc/apis/authentication.k8s.io/v1/tokenreviews", apiToken, string(body), nil, 403)
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
	t.Run("identity format controls ACL", func(t *testing.T) { call(t, "POST", "/tasks/"+rejected, caller, nil, 403) })
	t.Run("verified Kubernetes group authorizes", func(t *testing.T) { call(t, "POST", "/tasks/serviceaccount-group", caller, nil, 202) })

	t.Run("API Redis worker result and logs", func(t *testing.T) {
		submitted := call(t, "POST", "/tasks/"+accepted, caller, map[string]string{
			"X-Forwarded-User": "forged-user", "X-Forwarded-Groups": "ops-team",
			"X-Forwarded-Cluster": "forged-cluster", "X-Forwarded-Extra-Cluster-Name": "forged-cluster",
		}, 202)
		var task struct{ ID, Queue string }
		if err := json.Unmarshal(submitted, &task); err != nil || task.ID == "" || task.Queue == "" {
			t.Fatal("missing submitted task ID/queue")
		}
		inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: "redis:6379"})
		defer inspector.Close()
		deadline := time.Now().Add(45 * time.Second)
		var info *asynq.TaskInfo
		for {
			var err error
			info, err = inspector.GetTaskInfo(task.Queue, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if info.State == asynq.TaskStateCompleted {
				break
			}
			if info.State == asynq.TaskStateArchived || time.Now().After(deadline) {
				t.Fatal("worker did not complete task:", info.State)
			}
			time.Sleep(200 * time.Millisecond)
		}
		var payload worker.TaskPayload
		if err := json.Unmarshal(info.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		principal := payload.Principal
		if payload.Identity != identity || principal == nil {
			t.Fatal("queue lost verified identity/Principal")
		}
		if principal.Username != username || principal.UID != account.Metadata.UID || principal.Cluster != "kind-e2e" || principal.Provider != "kubernetes" || !slices.Contains(principal.Groups, group) || slices.Contains(principal.Groups, "ops-team") {
			t.Fatal("queue Principal differs from live TokenReview identity")
		}
		if (qualified && principal.Identity != identity) || (!qualified && principal.Identity != "") {
			t.Fatal("incorrect optional Principal identity")
		}
		execution := call(t, "GET", "/executions/"+task.ID, caller, nil, 200)
		var result struct {
			Status, Identity, Groups string
			Result                   worker.TaskResult
		}
		if err := json.Unmarshal(execution, &result); err != nil {
			t.Fatal(err)
		}
		if result.Status != "completed" || result.Identity != identity || result.Groups != strings.Join(principal.Groups, ",") || result.Result.ExitCode != 0 || result.Result.Data == nil {
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
		for _, data := range [][]byte{info.Payload, execution, logs} {
			for _, secret := range []string{caller, wrongAudience, apiToken, "forged-user", "forged-cluster", "ops-team"} {
				if bytes.Contains(data, []byte(secret)) {
					t.Fatal("credential or forged identity leaked into queue/result/logs")
				}
			}
		}
		call(t, "GET", "/executions/"+task.ID, "", nil, 401)
		call(t, "GET", "/executions/"+task.ID+"/logs", "not-a-valid-token", nil, 401)
	})
}
