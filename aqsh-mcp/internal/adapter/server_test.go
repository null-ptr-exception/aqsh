package adapter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOAuthMCPBoundary(t *testing.T) {
	i, b := newIssuer(t), newBackend(t)
	_, cfg := serveAdapter(t, b, i)
	alice := connect(t, cfg.PublicURL, i.token(t, cfg.PublicURL, "alice", jwt.MapClaims{"groups": []string{"ops", "system:masters"}}))
	listed, err := alice.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 2 {
		t.Fatalf("tools: %+v", listed.Tools)
	}
	encoded, _ := json.Marshal(listed)
	if strings.Contains(string(encoded), "values_url") || strings.Contains(string(encoded), "allowed_groups") {
		t.Fatal("internal policy exposed in schema")
	}
	result := call(t, alice, "inspect", map[string]any{})
	if result.IsError {
		t.Fatalf("submission failed: %+v", result)
	}
	id := content(t, result)["execution_id"].(string)
	b.mu.Lock()
	if b.lastIdentity != identity(i.server.URL, "alice") || b.lastGroups != "db-readers" || b.lastAuthorization != "" || b.lastArgs["count"] != float64(2) || b.lastArgs["verbose"] != false {
		t.Fatalf("identity/token/defaults incorrect: %+v %s %s", b.lastArgs, b.lastIdentity, b.lastGroups)
	}
	b.mu.Unlock()
	polled := call(t, alice, "aqsh_get_execution", map[string]any{"execution_id": id})
	data, _ := json.Marshal(polled)
	if polled.IsError || strings.Contains(string(data), "secret") || content(t, polled)["data"].(map[string]any)["healthy"] != false {
		t.Fatalf("unsafe or incorrect result: %s", data)
	}
	bob := connect(t, cfg.PublicURL, i.token(t, cfg.PublicURL, "bob", jwt.MapClaims{"groups": []string{}}))
	bobTools, err := bob.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bobTools.Tools) != 1 || bobTools.Tools[0].Name != "aqsh_get_execution" {
		t.Fatal("ACL did not filter catalog")
	}
	if !call(t, bob, "aqsh_get_execution", map[string]any{"execution_id": id}).IsError {
		t.Fatal("other owner read execution")
	}
	// Even an owner's execution from a REST-only task cannot be retrieved via MCP.
	b.mu.Lock()
	b.records["hidden"] = map[string]any{"id": "hidden", "identity": identity(i.server.URL, "alice"), "task": "mutate", "status": "completed", "result": map[string]any{"json": "secret"}}
	b.mu.Unlock()
	if !call(t, alice, "aqsh_get_execution", map[string]any{"execution_id": "hidden"}).IsError {
		t.Fatal("hidden task result was exposed")
	}
	for _, args := range []map[string]any{{"count": 1.5}, {"unknown": "value"}, {"count": 9007199254740992}} {
		r, err := alice.CallTool(t.Context(), &mcp.CallToolParams{Name: "inspect", Arguments: args})
		if err == nil && !r.IsError {
			t.Fatal("invalid arguments accepted")
		}
	}
	if r, err := alice.CallTool(t.Context(), &mcp.CallToolParams{Name: "mutate", Arguments: map[string]any{}}); err == nil && !r.IsError {
		t.Fatal("hidden tool accepted")
	}
	b.mu.Lock()
	accepted := b.accepted
	d := b.definitions["inspect"]
	d.Inputs[0].Default = "denied"
	b.definitions["inspect"] = d
	b.mu.Unlock()
	for _, args := range []map[string]any{{}, {"target": nil}} {
		r := call(t, alice, "inspect", args)
		if !r.IsError {
			t.Fatal("forbidden default target accepted")
		}
		encoded, _ := json.Marshal(r)
		if strings.Contains(string(encoded), "secret") {
			t.Fatal("upstream error leaked")
		}
	}
	b.mu.Lock()
	if b.accepted != accepted || b.lastArgs["target"] != "denied" {
		t.Fatal("default authorization bypass")
	}
	b.mu.Unlock()
}

func TestOAuthRejectsInvalidTokens(t *testing.T) {
	i, b := newIssuer(t), newBackend(t)
	server, cfg := serveAdapter(t, b, i)
	for _, tc := range []struct {
		name   string
		claims jwt.MapClaims
		status int
	}{
		{"expired", jwt.MapClaims{"exp": time.Now().Add(-time.Minute).Unix()}, 401},
		{"missing expiry", jwt.MapClaims{"exp": nil}, 401},
		{"wrong audience", jwt.MapClaims{"aud": "other-api"}, 401},
		{"wrong issuer", jwt.MapClaims{"iss": "https://other.example"}, 401},
		{"not yet valid", jwt.MapClaims{"nbf": time.Now().Add(time.Hour).Unix()}, 401},
		{"missing subject", jwt.MapClaims{"sub": nil}, 401},
		{"missing scope", jwt.MapClaims{"scope": "other"}, 403},
		{"bad groups", jwt.MapClaims{"groups": "system:masters"}, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", cfg.PublicURL, strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+i.token(t, cfg.PublicURL, "alice", tc.claims))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.status || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata") {
				t.Fatalf("status %d headers %v", resp.StatusCode, resp.Header)
			}
		})
	}
	for _, raw := range []string{"not-a-jwt", i.token(t, cfg.PublicURL, "alice", nil) + "x"} {
		req, _ := http.NewRequest("POST", cfg.PublicURL, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+raw)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("invalid signature status %d", resp.StatusCode)
		}
	}
	req, _ := http.NewRequest("POST", cfg.PublicURL, strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://untrusted.example")
	req.Header.Set("Authorization", "Bearer "+i.token(t, cfg.PublicURL, "alice", nil))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("origin accepted")
	}
	resp, err = http.Get(server.URL + "/.well-known/oauth-protected-resource/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var metadata map[string]any
	if json.NewDecoder(resp.Body).Decode(&metadata) != nil || metadata["resource"] != cfg.PublicURL {
		t.Fatal("discovery metadata invalid")
	}
}

func TestAuthorizationCodeWithPKCE(t *testing.T) {
	i, b := newIssuer(t), newBackend(t)
	_, cfg := serveAdapter(t, b, i)
	session := connectOAuth(t, cfg.PublicURL)
	if _, err := session.ListTools(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if call(t, session, "inspect", map[string]any{}).IsError {
		t.Fatal("OAuth tool call failed")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.authorizations != 1 || i.exchanges != 1 {
		t.Fatalf("OAuth flow: authorize=%d exchange=%d", i.authorizations, i.exchanges)
	}
}

func TestFailClosedConfiguration(t *testing.T) {
	i, b := newIssuer(t), newBackend(t)
	cfg := testConfig(b, i, "http://127.0.0.1:8090/mcp")
	cfg.Tools[0].ReadOnly = false
	if _, err := New(t.Context(), cfg); err == nil {
		t.Fatal("write tool enabled")
	}
	cfg.Tools[0].ReadOnly = true
	b.mu.Lock()
	b.ownerOnly = false
	b.mu.Unlock()
	if _, err := New(t.Context(), cfg); err == nil {
		t.Fatal("unprotected aqsh accepted")
	}
	cfg.BackendURL = "https://backend.example"
	if err := cfg.validate(); err == nil {
		t.Fatal("untrusted remote header transport accepted")
	}
}

func TestResultPolicy(t *testing.T) {
	i, b := newIssuer(t), newBackend(t)
	_, cfg := serveAdapter(t, b, i)
	s := connect(t, cfg.PublicURL, i.token(t, cfg.PublicURL, "alice", nil))
	for _, tc := range []struct {
		name   string
		result map[string]any
		want   string
	}{
		{"null", map[string]any{"exit_code": 0, "json": nil}, ""},
		{"invalid", map[string]any{"exit_code": 1, "json_omitted": "invalid_json", "error": "secret"}, "invalid_json"},
		{"oversize", map[string]any{"exit_code": 0, "json": strings.Repeat("x", 65537)}, "too_large"},
		{"large-number", map[string]any{"exit_code": 0, "json": json.RawMessage(`{"count":9007199254740993}`)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b.mu.Lock()
			b.records[tc.name] = map[string]any{"id": tc.name, "task": "inspect", "identity": identity(i.server.URL, "alice"), "status": "completed", "result": tc.result}
			b.mu.Unlock()
			r := call(t, s, "aqsh_get_execution", map[string]any{"execution_id": tc.name})
			out := content(t, r)
			if r.IsError {
				t.Fatal("query failed")
			}
			if tc.want != "" && out["result_omitted"] != tc.want {
				t.Fatalf("omission: %+v", out)
			}
			if tc.name == "null" {
				if value, exists := out["data"]; !exists || value != nil {
					t.Fatal("JSON null lost")
				}
			}
			data, _ := json.Marshal(r)
			if tc.name == "large-number" && !strings.Contains(r.Content[0].(*mcp.TextContent).Text, "9007199254740993") {
				t.Fatal("result number rounded on the wire")
			}
			if strings.Contains(string(data), "secret") {
				t.Fatal("secret leaked")
			}
		})
	}
}

func TestBrowserOriginAndPreflight(t *testing.T) {
	i, b := newIssuer(t), newBackend(t)
	server := httptest.NewUnstartedServer(nil)
	cfg := testConfig(b, i, "http://"+server.Listener.Addr().String()+"/mcp")
	cfg.AllowedOrigins = []string{"https://inspector.example"}
	handler, err := New(t.Context(), cfg)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(server.Close)
	for _, method := range []string{"OPTIONS", "POST"} {
		req, _ := http.NewRequest(method, cfg.PublicURL, strings.NewReader(`{}`))
		req.Header.Set("Origin", cfg.AllowedOrigins[0])
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		want := 401
		if method == "OPTIONS" {
			want = 204
		}
		if resp.StatusCode != want || resp.Header.Get("Access-Control-Allow-Origin") != cfg.AllowedOrigins[0] {
			t.Fatal("CORS denied allowed origin")
		}
		if method == "POST" && !strings.Contains(resp.Header.Get("Access-Control-Expose-Headers"), "WWW-Authenticate") {
			t.Fatal("browser cannot discover OAuth")
		}
	}
}

func TestResultOmittedByDefault(t *testing.T) {
	i, b := newIssuer(t), newBackend(t)
	server := httptest.NewUnstartedServer(nil)
	cfg := testConfig(b, i, "http://"+server.Listener.Addr().String()+"/mcp")
	cfg.Tools[0].ResultMode = ""
	handler, err := New(t.Context(), cfg)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(server.Close)
	session := connect(t, cfg.PublicURL, i.token(t, cfg.PublicURL, "alice", nil))
	submitted := call(t, session, "inspect", map[string]any{})
	id := content(t, submitted)["execution_id"]
	result := call(t, session, "aqsh_get_execution", map[string]any{"execution_id": id})
	out := content(t, result)
	if result.IsError || out["result_omitted"] != "not_enabled" {
		t.Fatalf("unexpected result: %+v", out)
	}
	if _, exists := out["data"]; exists {
		t.Fatal("default policy exposed task output")
	}
}
