package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
