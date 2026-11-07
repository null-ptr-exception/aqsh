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
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testCluster(t *testing.T, handler http.HandlerFunc) (Endpoint, *httptest.Server) {
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
	return Endpoint{URL: srv.URL, CAFile: ca, TokenFile: token}, srv
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
	k, err := New(Config{Audiences: []string{"aqsh"}, TokenReview: cluster})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	for i := 0; i < 2; i++ {
		p, err := k.Authenticate(context.Background(), "caller-token")
		if err != nil || p.Identity != "system:serviceaccount:team:caller" {
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
		{"missing username", `{"status":{"authenticated":true,"audiences":["aqsh"],"user":{"uid":"id"}}}`, 201, ErrUnauthenticated},
		{"NUL username", `{"status":{"authenticated":true,"audiences":["aqsh"],"user":{"username":"user\u0000other"}}}`, 201, ErrUnauthenticated},
		{"NUL group", `{"status":{"authenticated":true,"audiences":["aqsh"],"user":{"username":"u","groups":["team\u0000admin"]}}}`, 201, ErrUnauthenticated},
		{"missing status", `{}`, 201, ErrUnavailable},
		{"null status", `{"status":null}`, 201, ErrUnavailable},
		{"status error", `{"status":{"authenticated":true,"error":"secret","audiences":["aqsh"],"user":{"username":"u","uid":"id"}}}`, 201, ErrUnauthenticated},
		{"http failure", `private API error`, 403, ErrUnavailable},
		{"malformed", `{`, 201, ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster, _ := testCluster(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) })
			k, err := New(Config{Audiences: []string{"aqsh"}, TokenReview: cluster})
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
	k, err := New(Config{Audiences: []string{"aqsh"}, RequestTimeout: 20 * time.Millisecond, TokenReview: cluster})
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

// Optional Kubernetes UID must not prevent valid username-based authentication.
func TestTokenReviewAcceptsOptionalUID(t *testing.T) {
	cluster, _ := testCluster(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"status":{"authenticated":true,"audiences":["aqsh"],"user":{"username":"oidc-user"}}}`))
	})
	k, err := New(Config{Audiences: []string{"aqsh"}, TokenReview: cluster})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	p, err := k.Authenticate(context.Background(), "token")
	if err != nil || p.Identity != "oidc-user" {
		t.Fatalf("principal=%+v err=%v", p, err)
	}
}

// Saturated reviews must respect cancellation and release capacity after completion.
func TestTokenReviewConcurrencyAndCapacityRecovery(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	started := make(chan struct{}, maxConcurrentReviews+1)
	cluster, _ := testCluster(t, func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-release:
			successReview(w)
		case <-r.Context().Done():
		}
	})
	k, err := New(Config{Audiences: []string{"aqsh"}, RequestTimeout: 5 * time.Second, TokenReview: cluster})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	results := make(chan error, maxConcurrentReviews)
	for i := 0; i < maxConcurrentReviews; i++ {
		go func() { _, err := k.Authenticate(context.Background(), "token"); results <- err }()
	}
	for i := 0; i < maxConcurrentReviews; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("reviews did not reach endpoint")
		}
	}
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		if cancelled {
			cancel()
		}
		_, err := k.Authenticate(ctx, "token")
		cancel()
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("saturated review returned %v", err)
		}
	}
	select {
	case <-started:
		t.Fatal("review exceeded concurrency limit")
	default:
	}
	once.Do(func() { close(release) })
	for i := 0; i < maxConcurrentReviews; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := k.Authenticate(context.Background(), "token"); err != nil {
		t.Fatalf("capacity not recovered: %v", err)
	}
}
