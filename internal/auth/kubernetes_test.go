package auth

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func testCluster(t *testing.T, handler http.HandlerFunc) (ClusterConfig, *httptest.Server) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	ca, token := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "token")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("reviewer-one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return ClusterConfig{APIServer: srv.URL, CAFile: ca, TokenFile: token}, srv
}

func successReview(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":{"authenticated":true,"audiences":["aqsh"],"user":{"username":"system:serviceaccount:team:caller","uid":"sa-uid","groups":["team"]}}}`))
}

func TestTokenReviewAndProjectedTokenRotation(t *testing.T) {
	var requests atomic.Int32
	cluster, _ := testCluster(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/authentication.k8s.io/v1/tokenreviews" || r.Method != "POST" {
			t.Error("wrong endpoint")
		}
		wantReviewer := "reviewer-one"
		if requests.Add(1) > 1 {
			wantReviewer = "reviewer-two"
		}
		if r.Header.Get("Authorization") != "Bearer "+wantReviewer {
			t.Error("stale reviewer credential")
		}
		var tr tokenReview
		if err := json.NewDecoder(r.Body).Decode(&tr); err != nil {
			t.Error(err)
		}
		if tr.Spec.Token != "caller-token" || !reflect.DeepEqual(tr.Spec.Audiences, []string{"aqsh"}) {
			t.Error("wrong TokenReview spec")
		}
		successReview(w)
	})
	k, err := New(Config{Audiences: []string{"aqsh"}, Clusters: map[string]ClusterConfig{"trusted-a": cluster}})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	for i := 0; i < 2; i++ {
		p, err := k.Authenticate(context.Background(), "caller-token")
		if err != nil || p.Cluster != "trusted-a" || p.UID != "sa-uid" || p.Username != "system:serviceaccount:team:caller" {
			t.Fatalf("principal=%+v err=%v", p, err)
		}
		if err := os.WriteFile(cluster.TokenFile, []byte("reviewer-two"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(cluster.TokenFile); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Authenticate(context.Background(), "caller-token"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing reviewer did not fail closed")
	}
}

func TestTokenReviewRejectsInvalidResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"denied", `{"status":{"authenticated":false}}`, 201, ErrUnauthenticated},
		{"wrong audience", `{"status":{"authenticated":true,"audiences":["other"],"user":{"username":"u","uid":"id"}}}`, 201, ErrUnauthenticated},
		{"missing audience", `{"status":{"authenticated":true,"user":{"username":"u","uid":"id"}}}`, 201, ErrUnauthenticated},
		{"ambiguous groups", `{"status":{"authenticated":true,"audiences":["aqsh"],"user":{"username":"u","uid":"id","groups":["untrusted,admin"]}}}`, 201, ErrUnauthenticated},
		{"missing uid", `{"status":{"authenticated":true,"audiences":["aqsh"],"user":{"username":"u"}}}`, 201, ErrUnauthenticated},
		{"status error", `{"status":{"authenticated":true,"error":"secret","audiences":["aqsh"],"user":{"username":"u","uid":"id"}}}`, 201, ErrUnauthenticated},
		{"http failure", `private API error`, 403, ErrUnavailable},
		{"malformed", `{`, 201, ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster, _ := testCluster(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) })
			k, err := New(Config{Audiences: []string{"aqsh"}, Clusters: map[string]ClusterConfig{"a": cluster}})
			if err != nil {
				t.Fatal(err)
			}
			defer k.Close()
			if _, err := k.Authenticate(context.Background(), "token"); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}

func TestTokenReviewCancellationAndTimeout(t *testing.T) {
	cluster, _ := testCluster(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	})
	k, err := New(Config{Audiences: []string{"aqsh"}, RequestTimeout: 20 * time.Millisecond, Clusters: map[string]ClusterConfig{"a": cluster}})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := k.Authenticate(ctx, "token"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := k.Authenticate(context.Background(), "token"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("timeout not bounded")
	}
}

func TestLoadAndValidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.yaml")
	if err := os.WriteFile(path, []byte("audiences: [aqsh]\nrequest_timeout: 2s\nclusters:\n  a:\n    api_server: https://kubernetes.default.svc\n    token_file: /mounted/token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.validate() != nil || cfg.RequestTimeout != 2*time.Second {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
	for _, raw := range []string{"http://api", "https://user:password@api", "https://api?query", "https://api/#fragment", "not a URL"} {
		if validEndpoint(raw) {
			t.Fatalf("accepted endpoint %s", raw)
		}
	}
	cfg.Audiences = nil
	if cfg.validate() == nil {
		t.Fatal("missing audiences accepted")
	}
	if err := os.WriteFile(path, []byte("unknown_field: true"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("unknown config field accepted")
	}
}

type tokenReview struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Spec       struct {
		Token     string   `json:"token"`
		Audiences []string `json:"audiences"`
	} `json:"spec"`
	Status struct {
		Authenticated bool     `json:"authenticated"`
		Audiences     []string `json:"audiences"`
		User          userInfo `json:"user"`
		Error         string   `json:"error"`
	} `json:"status,omitempty"`
}

type userInfo struct {
	Username string   `json:"username"`
	UID      string   `json:"uid"`
	Groups   []string `json:"groups"`
}
