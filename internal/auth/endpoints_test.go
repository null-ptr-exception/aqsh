package auth

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// The response shape and cluster extra key match the external
// kube-federated-auth TokenReview endpoint. No federated-auth code is embedded.
func TestTokenReviewEndpointContract(t *testing.T) {
	for _, backend := range []struct {
		name      string
		https     bool
		federated bool
	}{
		{"kubernetes", true, false},
		{"federated HTTP", false, true},
		{"federated HTTPS", true, true},
	} {
		for _, qualify := range []bool{false, true} {
			name := backend.name + "/shared identity"
			if qualify {
				name = backend.name + "/qualified identity"
			}
			t.Run(name, func(t *testing.T) {
				var calls atomic.Int32
				h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Method != "POST" || r.URL.Path != "/apis/authentication.k8s.io/v1/tokenreviews" || r.Header.Get("Authorization") != "Bearer reviewer-secret" {
						t.Error("incorrect endpoint or reviewer authentication")
					}
					if r.Header.Get("Content-Type") != "application/json" {
						t.Error("TokenReview must use JSON")
					}
					var request struct {
						APIVersion string
						Kind       string
						Spec       struct {
							Token     string
							Audiences []string
						}
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					if request.APIVersion != "authentication.k8s.io/v1" || request.Kind != "TokenReview" || !reflect.DeepEqual(request.Spec.Audiences, []string{"aqsh", "other-audience"}) {
						t.Error("incorrect TokenReview contract")
					}
					cluster := strings.TrimPrefix(request.Spec.Token, "caller-secret-")
					extra := map[string][]string{"unrelated-extra": {"ignored"}}
					if backend.federated {
						extra["authentication.kubernetes.io/cluster-name"] = []string{cluster}
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]any{
						"apiVersion": "authentication.k8s.io/v1", "kind": "TokenReview",
						"status": map[string]any{
							"authenticated": true, "audiences": []string{"aqsh"},
							"user": map[string]any{"username": "system:serviceaccount:team:caller", "uid": "uid-" + cluster, "groups": []string{"team"}, "extra": extra},
						},
					})
				})
				srv := httptest.NewUnstartedServer(h)
				if backend.https {
					srv.StartTLS()
				} else {
					srv.Start()
				}
				defer srv.Close()
				dir := t.TempDir()
				tokenFile := filepath.Join(dir, "reviewer")
				if err := os.WriteFile(tokenFile, []byte("reviewer-secret\n"), 0600); err != nil {
					t.Fatal(err)
				}
				cc := Endpoint{URL: srv.URL, TokenFile: tokenFile, AllowHTTP: !backend.https}
				if backend.https {
					cc.CAFile = filepath.Join(dir, "ca.pem")
					if err := os.WriteFile(cc.CAFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0600); err != nil {
						t.Fatal(err)
					}
				}
				staticCluster := "fallback"
				if backend.federated {
					staticCluster = ""
				}
				k, err := New(Config{Audiences: []string{"aqsh", "other-audience"}, IncludeClusterInIdentity: qualify, TrustClusterExtra: backend.federated, ClusterName: staticCluster, TokenReview: cc})
				if err != nil {
					t.Fatal(err)
				}
				defer k.Close()
				for i := 0; i < 2; i++ {
					for _, source := range []string{"cluster-a", "cluster-b"} {
						p, err := k.Authenticate(context.Background(), "caller-secret-"+source)
						wantCluster := "fallback"
						if backend.federated {
							wantCluster = source
						}
						wantIdentity, wantGroup := "system:serviceaccount:team:caller", "team"
						if qualify {
							wantIdentity = wantCluster + "/" + wantIdentity
							wantGroup = wantCluster + "/" + wantGroup
						}
						if err != nil || p.Identity != wantIdentity || !reflect.DeepEqual(p.Groups, []string{wantGroup}) {
							t.Fatalf("principal=%+v error=%v", p, err)
						}
					}
				}
				if calls.Load() != 4 {
					t.Fatalf("aqsh cached a review: %d calls, want 4", calls.Load())
				}
			})
		}
	}
}

func TestHTTPRequiresExplicitOptIn(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("reviewer"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := New(Config{Audiences: []string{"aqsh"}, TokenReview: Endpoint{URL: srv.URL, TokenFile: file}})
	if err == nil || calls.Load() != 0 {
		t.Fatal("plain HTTP accepted without explicit opt-in")
	}
	for _, raw := range []string{"https://user:pass@api", "http://api?", "http://api?query", "http://api#fragment", "http://api#", "ftp://api", "//api"} {
		if validEndpoint(raw, true) {
			t.Errorf("allow_http accepted invalid endpoint: %s", raw)
		}
	}

}

func TestClusterExtraTrust(t *testing.T) {
	for _, trust := range []bool{false, true} {
		for _, qualify := range []bool{false, true} {
			for _, tc := range []struct {
				name  string
				extra map[string][]string
				valid bool
			}{
				{"missing", nil, false},
				{"valid", map[string][]string{clusterExtraKey: {"source"}}, true},
				{"empty", map[string][]string{clusterExtraKey: {}}, false},
				{"multiple", map[string][]string{clusterExtraKey: {"a", "b"}}, false},
				{"comma", map[string][]string{clusterExtraKey: {"a,b"}}, false},
				{"slash", map[string][]string{clusterExtraKey: {"a/b"}}, false},
				{"backslash", map[string][]string{clusterExtraKey: {"a\\b"}}, false},
				{"whitespace", map[string][]string{clusterExtraKey: {" a "}}, false},
				{"control", map[string][]string{clusterExtraKey: {"a\x00b"}}, false},
			} {
				t.Run(fmt.Sprintf("trusted=%v/qualified=%v/%s", trust, qualify, tc.name), func(t *testing.T) {
					endpoint, _ := testCluster(t, func(w http.ResponseWriter, _ *http.Request) {
						json.NewEncoder(w).Encode(map[string]any{"status": map[string]any{
							"authenticated": true, "audiences": []string{"aqsh"},
							"user": map[string]any{"username": "user", "groups": []string{"team"}, "extra": tc.extra},
						}})
					})
					cluster := "local"
					if trust {
						cluster = ""
					}
					k, err := New(Config{Audiences: []string{"aqsh"}, IncludeClusterInIdentity: qualify, TrustClusterExtra: trust, ClusterName: cluster, TokenReview: endpoint})
					if err != nil {
						t.Fatal(err)
					}
					defer k.Close()
					handler := Middleware(k, "", "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						p, _ := FromContext(r.Context())
						wantIdentity, wantGroup := "user", "team"
						if qualify {
							prefix := "local/"
							if trust {
								prefix = "source/"
							}
							wantIdentity = prefix + wantIdentity
							wantGroup = prefix + wantGroup
						}
						if p.Identity != wantIdentity || !reflect.DeepEqual(p.Groups, []string{wantGroup}) {
							t.Errorf("wrong trusted identity: %+v", p)
						}
						w.WriteHeader(204)
					}))
					req := httptest.NewRequest("GET", "/tasks", nil)
					req.Header.Set("Authorization", "Bearer caller")
					out := httptest.NewRecorder()
					handler.ServeHTTP(out, req)
					want := 204
					if trust && !tc.valid {
						want = 401
					}
					if out.Code != want {
						t.Fatalf("got %d want %d", out.Code, want)
					}
				})
			}
		}
	}
}

func TestTokenReviewErrorsReachHTTPBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"missing status", 200, `{}`, 503},
		{"null status", 200, `{"status":null}`, 503},
		{"denied", 200, `{"status":{"authenticated":false}}`, 401},
		{"reviewer rejected", 401, "private-reviewer-error", 503},
		{"reviewer forbidden", 403, "private-reviewer-error", 503},
		{"backend down", 500, "private-backend-error", 503},
		{"backend unavailable", 503, "private-backend-error", 503},
		{"malformed", 200, "invalid JSON", 503},
		{"trailing JSON", 200, `{"status":{"authenticated":false}} {}`, 503},
		{"oversized response", 200, strings.Repeat(" ", (1<<20)+1), 503},
		{"federated denial with error", 200, `{"status":{"authenticated":false,"error":"token not valid for any configured cluster"}}`, 401},
		{"wrong audience", 200, `{"status":{"authenticated":true,"audiences":["other"],"user":{"username":"u","uid":"uid"}}}`, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cc, _ := testCluster(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) })
			k, err := New(Config{Audiences: []string{"aqsh"}, TokenReview: cc})
			if err != nil {
				t.Fatal(err)
			}
			defer k.Close()
			h := Middleware(k, "X-Forwarded-User", "X-Forwarded-Groups", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("failed review reached API") }))
			r := httptest.NewRequest("GET", "/tasks", nil)
			r.Header.Set("Authorization", "Bearer caller-secret")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "secret") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestTokenReviewDoesNotFollowRedirects(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1); successReview(w) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("reviewer"), 0600); err != nil {
		t.Fatal(err)
	}
	k, err := New(Config{Audiences: []string{"aqsh"}, TokenReview: Endpoint{URL: source.URL, TokenFile: file, AllowHTTP: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if _, err := k.Authenticate(context.Background(), "caller-secret"); !errors.Is(err, ErrUnavailable) || leaked.Load() != 0 {
		t.Fatalf("redirect followed: calls=%d error=%v", leaked.Load(), err)
	}
}
