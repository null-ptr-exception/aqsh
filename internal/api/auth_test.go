package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/rophy/aqsh/internal/auth"
	"github.com/rophy/aqsh/internal/config"
	"github.com/rophy/aqsh/internal/tasks"
	"github.com/rophy/aqsh/internal/worker"
)

type verifiedCaller struct{ groups []string }

func (v verifiedCaller) Authenticate(context.Context, string) (auth.Principal, error) {
	return auth.Principal{Provider: "kubernetes", Cluster: "cluster-a", Username: "verified-user", UID: "verified-uid", Groups: v.groups}, nil
}

func TestNativeIdentityReachesTaskAuthorizationAndQueue(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	opt := asynq.RedisClientOpt{Addr: mr.Addr()}
	s := New(&config.Config{AuthMode: "kubernetes", IdentityHeader: "X-Forwarded-User", GroupsHeader: "X-Forwarded-Groups", ResultRetention: time.Hour},
		&tasks.TasksConfig{Tasks: map[string]tasks.TaskDef{"run": {Script: "run.sh", AllowedGroups: []string{"verified-group"}}}}, rdb, opt, "test")
	defer s.client.Close()
	defer s.inspector.Close()
	for _, tc := range []struct {
		name   string
		groups []string
		want   int
	}{
		{"forged groups rejected", nil, 403},
		{"verified group cannot add CSV membership", []string{"other,verified-group"}, 403},
		{"verified group matches exactly", []string{" verified-group "}, 403},
		{"verified groups accepted", []string{"verified-group"}, 202},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := auth.Middleware(verifiedCaller{groups: tc.groups}, s.cfg.IdentityHeader, s.cfg.GroupsHeader, http.HandlerFunc(s.handleSubmitTask))
			r := httptest.NewRequest("POST", "/tasks/run", strings.NewReader(`{}`))
			r.SetPathValue("name", "run")
			r.Header.Set("Authorization", "Bearer caller-secret")
			r.Header.Set("X-Forwarded-User", "forged-user")
			r.Header.Set("X-Forwarded-Groups", "verified-group")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if w.Code != 202 {
				return
			}
			var submitted struct{ ID, Queue string }
			json.Unmarshal(w.Body.Bytes(), &submitted)
			info, err := s.inspector.GetTaskInfo(submitted.Queue, submitted.ID)
			if err != nil {
				t.Fatal(err)
			}
			var payload worker.TaskPayload
			if err := json.Unmarshal(info.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Identity != "verified-user" || payload.Groups != "verified-group" || payload.Principal == nil || payload.Principal.Cluster != "cluster-a" || payload.Principal.UID != "verified-uid" {
				t.Fatalf("wrong task identity: %+v", payload)
			}
			if strings.Contains(string(info.Payload), "secret") || strings.Contains(string(info.Payload), "forged") {
				t.Fatal("credential or forged identity in queue")
			}
		})
	}
}

func TestNativeModeNeverFallsBackToHeaders(t *testing.T) {
	s := &Server{cfg: &config.Config{AuthMode: "kubernetes", IdentityHeader: "X-Forwarded-User", GroupsHeader: "X-Forwarded-Groups"}}
	r := httptest.NewRequest("GET", "/tasks", nil)
	r.Header.Set("X-Forwarded-User", "forged")
	r.Header.Set("X-Forwarded-Groups", "admin")
	if identity, groups := s.caller(r); identity != "" || groups != "" {
		t.Fatal("native mode fell back to headers")
	}
}

func TestNativeRouterProtectsAllApplicationEndpoints(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	s := &Server{cfg: &config.Config{AuthMode: "kubernetes"}, rdb: rdb, tasks: &tasks.TasksConfig{Tasks: map[string]tasks.TaskDef{}}}
	handler := s.handler(verifiedCaller{})
	for _, route := range []struct{ method, path string }{{"GET", "/tasks"}, {"GET", "/tasks/run"}, {"POST", "/tasks/run"}, {"GET", "/executions/id"}, {"GET", "/executions/id/logs"}, {"POST", "/debug/coverage/flush"}} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(route.method, route.path, nil))
		if w.Code != 401 {
			t.Errorf("%s %s returned %d", route.method, route.path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("GET", "/tasks", nil)
	r.Header.Set("Authorization", "Bearer valid")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}

func TestFederatedIdentityAuthorizationAndQueue(t *testing.T) {
	const username = "system:serviceaccount:team:caller"
	for _, qualify := range []bool{false, true} {
		name := "shared identity"
		if qualify {
			name = "qualified identity"
		}
		t.Run(name, func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/apis/authentication.k8s.io/v1/tokenreviews" || r.Header.Get("Authorization") != "Bearer reviewer-private-token" {
					t.Error("wrong TokenReview request or reviewer credential")
				}
				for _, header := range []string{"X-Forwarded-User", "X-Forwarded-Groups", "X-Forwarded-Cluster", "X-Forwarded-Extra-Cluster-Name"} {
					if r.Header.Get(header) != "" {
						t.Errorf("caller header forwarded to verifier: %s", header)
					}
				}
				var review struct {
					Spec struct {
						Token     string
						Audiences []string
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
					t.Error(err)
				}
				if len(review.Spec.Audiences) != 1 || review.Spec.Audiences[0] != "aqsh" {
					t.Error("caller audiences not forwarded")
				}
				cluster := strings.TrimPrefix(review.Spec.Token, "caller-private-token-")
				json.NewEncoder(w).Encode(map[string]any{"status": map[string]any{
					"authenticated": true, "audiences": []string{"aqsh"},
					"user": map[string]any{"username": username, "uid": "uid-" + cluster, "groups": []string{"team"}, "extra": map[string][]string{
						"authentication.kubernetes.io/cluster-name": {cluster},
					}},
				}})
			}))
			defer endpoint.Close()
			tokenFile := filepath.Join(t.TempDir(), "reviewer")
			if err := os.WriteFile(tokenFile, []byte("reviewer-private-token"), 0600); err != nil {
				t.Fatal(err)
			}
			a, err := auth.New(auth.Config{Audiences: []string{"aqsh"}, IncludeClusterInIdentity: qualify, Clusters: map[string]auth.ClusterConfig{
				"fallback": {APIServer: endpoint.URL, AllowHTTP: true, TokenFile: tokenFile},
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			mr := miniredis.RunT(t)
			rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			defer rdb.Close()
			allowed := username
			if qualify {
				allowed = "cluster-a/" + username
			}
			s := New(&config.Config{AuthMode: "kubernetes", IdentityHeader: "X-Forwarded-User", GroupsHeader: "X-Forwarded-Groups", ResultRetention: time.Hour},
				&tasks.TasksConfig{Tasks: map[string]tasks.TaskDef{"run": {Script: "run.sh", AllowedUsers: []string{allowed}}}},
				rdb, asynq.RedisClientOpt{Addr: mr.Addr()}, "test")
			defer s.client.Close()
			defer s.inspector.Close()
			handler := s.handler(a)
			for _, cluster := range []string{"cluster-a", "cluster-b"} {
				r := httptest.NewRequest("POST", "/tasks/run", strings.NewReader(`{}`))
				r.Header.Set("Authorization", "Bearer caller-private-token-"+cluster)
				r.Header.Set("X-Forwarded-User", allowed)
				r.Header.Set("X-Forwarded-Groups", "admin")
				r.Header.Set("X-Forwarded-Cluster", "cluster-a")
				r.Header.Set("X-Forwarded-Extra-Cluster-Name", "cluster-a")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if qualify && cluster == "cluster-b" {
					if w.Code != http.StatusForbidden {
						t.Fatalf("other cluster used allowed identity: %d %s", w.Code, w.Body.String())
					}
					continue
				}
				if w.Code != http.StatusAccepted {
					t.Fatalf("submission rejected: %d %s", w.Code, w.Body.String())
				}
				var submitted struct{ ID, Queue string }
				if err := json.Unmarshal(w.Body.Bytes(), &submitted); err != nil {
					t.Fatal(err)
				}
				info, err := s.inspector.GetTaskInfo(submitted.Queue, submitted.ID)
				if err != nil {
					t.Fatal(err)
				}
				var payload worker.TaskPayload
				if err := json.Unmarshal(info.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if payload.Identity != allowed || payload.Groups != "team" || payload.Principal == nil || payload.Principal.Username != username || payload.Principal.Cluster != cluster || payload.Principal.UID != "uid-"+cluster {
					t.Fatalf("wrong verified identity in queue: %+v", payload)
				}
				if qualify && payload.Principal.Identity != allowed {
					t.Fatal("qualified identity missing from metadata")
				}
				for _, secret := range []string{"caller-private-token", "reviewer-private-token", "admin"} {
					if strings.Contains(string(info.Payload), secret) {
						t.Fatalf("credential or forged identity in queue: %s", secret)
					}
				}
			}
		})
	}
}
