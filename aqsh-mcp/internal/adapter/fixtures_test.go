package adapter

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// This authorization service exists only in tests. Login is intentionally
// simulated; codes are single-use and bound to PKCE, client, redirect and resource.
type testIssuer struct {
	server                    *httptest.Server
	key                       *rsa.PrivateKey
	mu                        sync.Mutex
	codes                     map[string]url.Values
	authorizations, exchanges int
}

func newIssuer(t *testing.T) *testIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	i := &testIssuer{key: key, codes: map[string]url.Values{}}
	i.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server", "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": i.server.URL, "jwks_uri": i.server.URL + "/jwks",
				"authorization_endpoint": i.server.URL + "/authorize", "token_endpoint": i.server.URL + "/token",
				"response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code"},
				"code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none"},
				"authorization_response_iss_parameter_supported": true, "scopes_supported": []string{"aqsh:read"}})
		case "/jwks":
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &i.key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
		case "/authorize":
			q := r.URL.Query()
			if q.Get("client_id") != "test-client" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("resource") == "" || q.Get("redirect_uri") != "http://127.0.0.1/callback" {
				http.Error(w, "bad authorization request", 400)
				return
			}
			i.mu.Lock()
			i.authorizations++
			code := fmt.Sprintf("code-%d", i.authorizations)
			i.codes[code] = q
			i.mu.Unlock()
			u, _ := url.Parse(q.Get("redirect_uri"))
			params := u.Query()
			params.Set("code", code)
			params.Set("state", q.Get("state"))
			params.Set("iss", i.server.URL)
			u.RawQuery = params.Encode()
			http.Redirect(w, r, u.String(), 302)
		case "/token":
			// Reject unsupported client authentication before consuming a code.
			if _, _, basic := r.BasicAuth(); basic {
				http.Error(w, "invalid_client", 401)
				return
			}
			r.ParseForm()
			i.mu.Lock()
			q, ok := i.codes[r.Form.Get("code")]
			delete(i.codes, r.Form.Get("code"))
			i.mu.Unlock()
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if !ok || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("client_id") != q.Get("client_id") || r.Form.Get("redirect_uri") != q.Get("redirect_uri") || r.Form.Get("resource") != q.Get("resource") || base64.RawURLEncoding.EncodeToString(hash[:]) != q.Get("code_challenge") {
				http.Error(w, "invalid_grant", 400)
				return
			}
			i.mu.Lock()
			i.exchanges++
			i.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"access_token": i.token(t, q.Get("resource"), "alice", nil), "token_type": "Bearer", "expires_in": 300, "scope": "aqsh:read"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(i.server.Close)
	return i
}

func (i *testIssuer) token(t *testing.T, audience, subject string, overrides jwt.MapClaims) string {
	t.Helper()
	claims := jwt.MapClaims{"iss": i.server.URL, "aud": audience, "sub": subject, "exp": time.Now().Add(5 * time.Minute).Unix(), "scope": "aqsh:read", "groups": []string{"ops"}}
	for k, v := range overrides {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test"
	raw, err := token.SignedString(i.key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header = r.Header.Clone()
	r.Header.Set("Authorization", "Bearer "+b.token)
	r.Header.Set("X-Forwarded-User", "spoofed-admin")
	r.Header.Set("X-Forwarded-Groups", "system:masters")
	return http.DefaultTransport.RoundTrip(r)
}

func connect(t *testing.T, endpoint, token string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	s, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: &http.Client{Transport: bearerTransport{token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func connectOAuth(t *testing.T, endpoint string) *mcp.ClientSession {
	t.Helper()
	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		PreregisteredClient: &oauthex.ClientCredentials{ClientID: "test-client"}, RedirectURL: "http://127.0.0.1/callback",
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			req, _ := http.NewRequestWithContext(ctx, "GET", args.URL, nil)
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			resp, err := client.Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			if resp.StatusCode != 302 {
				return nil, fmt.Errorf("authorize status %d", resp.StatusCode)
			}
			u, err := url.Parse(resp.Header.Get("Location"))
			if err != nil {
				return nil, err
			}
			q := u.Query()
			return &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "oauth-test", Version: "1"}, nil)
	s, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: endpoint, OAuthHandler: handler}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func call(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := s.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func content(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	var out map[string]any
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(data, &out) != nil {
		t.Fatalf("invalid structured content: %s", data)
	}
	return out
}

type testBackend struct {
	server                                      *httptest.Server
	mu                                          sync.Mutex
	definitions                                 map[string]taskDef
	records                                     map[string]map[string]any
	accepted                                    int
	lastArgs                                    map[string]any
	lastIdentity, lastGroups, lastAuthorization string
	ownerOnly                                   bool
}

func newBackend(t *testing.T) *testBackend {
	t.Helper()
	b := &testBackend{ownerOnly: true, definitions: map[string]taskDef{
		"inspect": {Description: "Inspect", AllowedGroups: []string{"db-readers"}, Inputs: []input{
			{Name: "target", Type: "string", Default: "allowed", ValuesURL: true},
			{Name: "count", Type: "int", Default: "2"},
			{Name: "verbose", Type: "bool", Default: "false"},
		}},
	}, records: map[string]map[string]any{}}
	b.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/health" {
			json.NewEncoder(w).Encode(map[string]any{"execution_owner_only": b.ownerOnly, "result_json_supported": true})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/tasks/") {
			name := strings.TrimPrefix(r.URL.Path, "/tasks/")
			d, ok := b.definitions[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			if r.Method == "GET" {
				json.NewEncoder(w).Encode(d)
				return
			}
			b.lastIdentity, b.lastGroups, b.lastAuthorization = r.Header.Get("X-Forwarded-User"), r.Header.Get("X-Forwarded-Groups"), r.Header.Get("Authorization")
			var args map[string]any
			if json.NewDecoder(r.Body).Decode(&args) != nil {
				http.Error(w, "bad input", 400)
				return
			}
			b.lastArgs = args
			if !d.allowed(principal{b.lastIdentity, strings.Split(b.lastGroups, ",")}) || args["target"] != "allowed" {
				http.Error(w, "private policy URL secret", 403)
				return
			}
			b.accepted++
			id := fmt.Sprintf("exec-%d", b.accepted)
			b.records[id] = map[string]any{"id": id, "task": name, "identity": b.lastIdentity, "status": "completed", "last_error": "secret stderr", "groups": "secret groups",
				"result": map[string]any{"exit_code": 0, "json": map[string]any{"healthy": false}, "data": "secret raw data", "error": "secret script error"}}
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(map[string]any{"id": id, "status": "pending"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/executions/") {
			e, ok := b.records[strings.TrimPrefix(r.URL.Path, "/executions/")]
			if !ok || b.ownerOnly && e["identity"] != r.Header.Get("X-Forwarded-User") {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(e)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(b.server.Close)
	return b
}

func testConfig(b *testBackend, i *testIssuer, public string) Config {
	return Config{PublicURL: public, BackendURL: b.server.URL,
		OAuth: OAuthConfig{Issuer: i.server.URL, RequiredScopes: []string{"aqsh:read"}, GroupMapping: map[string]string{"ops": "db-readers"}},
		Tools: []ToolConfig{{Name: "inspect", Task: "inspect", ReadOnly: true, ResultMode: "json"}},
	}
}

func serveAdapter(t *testing.T, b *testBackend, i *testIssuer) (*httptest.Server, Config) {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	cfg := testConfig(b, i, "http://"+server.Listener.Addr().String()+"/mcp")
	handler, err := New(t.Context(), cfg)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(server.Close)
	return server, cfg
}
