package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type authenticateFunc func(context.Context, string) (Principal, error)

func (f authenticateFunc) Authenticate(ctx context.Context, token string) (Principal, error) {
	return f(ctx, token)
}

func TestMiddleware(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, authorization string
		err                               error
		want                              int
		called                            bool
	}{
		{"missing token", "GET", "/tasks", "", nil, 401, false},
		{"basic", "GET", "/tasks", "Basic value", nil, 401, false},
		{"extra fields", "GET", "/tasks", "Bearer one two", nil, 401, false},
		{"invalid", "POST", "/tasks/run", "Bearer caller-secret", ErrUnauthenticated, 401, true},
		{"backend down", "GET", "/executions/id", "Bearer caller-secret", ErrUnavailable, 503, true},
		{"unexpected error", "GET", "/tasks", "Bearer caller-secret", errors.New("sensitive backend detail"), 503, true},
		{"valid", "GET", "/tasks", "bEaReR caller-secret", nil, 204, true},
		{"health", "GET", "/health", "", nil, 204, false},
		{"metrics", "GET", "/metrics", "", nil, 204, false},
		{"health prefix protected", "GET", "/health/extra", "", nil, 401, false},
		{"POST health protected", "POST", "/health", "", nil, 401, false},
		{"debug protected", "POST", "/debug/coverage/flush", "", nil, 401, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			a := authenticateFunc(func(ctx context.Context, token string) (Principal, error) {
				called = true
				if token != "caller-secret" {
					t.Fatal("wrong bearer token")
				}
				return Principal{Cluster: "a", Username: "verified", UID: "uid"}, tc.err
			})
			h := Middleware(a, "Custom-User", "Custom-Groups", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.called {
					p, ok := FromContext(r.Context())
					if !ok || p.Username != "verified" || len(p.Groups) != 0 {
						t.Fatal("untrusted identity reached handler")
					}
					for _, header := range []string{"Authorization", "Custom-User", "Custom-Groups", "X-Forwarded-User", "X-Forwarded-Groups", "X-Forwarded-Cluster"} {
						if r.Header.Get(header) != "" {
							t.Fatalf("header %s survived", header)
						}
					}
				}
				w.WriteHeader(204)
			}))
			r := httptest.NewRequest(tc.method, tc.path, nil)
			for _, header := range []string{"Custom-User", "Custom-Groups", "X-Forwarded-User", "X-Forwarded-Groups", "X-Forwarded-Cluster"} {
				r.Header.Set(header, "forged")
			}
			if tc.authorization != "" {
				r.Header.Set("Authorization", tc.authorization)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want || called != tc.called {
				t.Fatalf("status=%d called=%v", w.Code, called)
			}
			if w.Code == 401 && w.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatal("missing challenge")
			}
		})
	}
}

func TestDuplicateAuthorization(t *testing.T) {
	a := authenticateFunc(func(context.Context, string) (Principal, error) {
		t.Fatal("ambiguous authorization accepted")
		return Principal{}, nil
	})
	r := httptest.NewRequest("GET", "/tasks", nil)
	r.Header.Add("Authorization", "Bearer one")
	r.Header.Add("Authorization", "Bearer two")
	w := httptest.NewRecorder()
	Middleware(a, "", "", http.NotFoundHandler()).ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}
