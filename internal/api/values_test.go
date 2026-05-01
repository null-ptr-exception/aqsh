package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSubstituteURL(t *testing.T) {
	tests := []struct {
		name     string
		template string
		identity string
		groups   string
		task     string
		want     string
	}{
		{
			name:     "all variables",
			template: "http://authz/values?user=${identity}&groups=${groups}&task=${task}",
			identity: "alice@example.com",
			groups:   "ops,dev",
			task:     "upgrade-db",
			want:     "http://authz/values?user=alice%40example.com&groups=ops%2Cdev&task=upgrade-db",
		},
		{
			name:     "no variables",
			template: "http://authz/values",
			identity: "alice",
			groups:   "ops",
			task:     "test",
			want:     "http://authz/values",
		},
		{
			name:     "empty identity",
			template: "http://authz/values?user=${identity}",
			identity: "",
			groups:   "",
			task:     "test",
			want:     "http://authz/values?user=",
		},
		{
			name:     "k8s service account",
			template: "http://authz/values?user=${identity}",
			identity: "system:serviceaccount:default:deploy-bot",
			groups:   "",
			task:     "test",
			want:     "http://authz/values?user=system%3Aserviceaccount%3Adefault%3Adeploy-bot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := substituteURL(tt.template, tt.identity, tt.groups, tt.task)
			if got != tt.want {
				t.Errorf("substituteURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseValuesResponse(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    []AllowedValue
		wantErr bool
	}{
		{
			name: "simple list",
			body: `["prod-db-001", "prod-db-002"]`,
			want: []AllowedValue{
				{Name: "prod-db-001", Value: "prod-db-001"},
				{Name: "prod-db-002", Value: "prod-db-002"},
			},
		},
		{
			name: "name-value pairs",
			body: `[{"name": "Production DB 001", "value": "prod-db-001"}, {"name": "Production DB 002", "value": "prod-db-002"}]`,
			want: []AllowedValue{
				{Name: "Production DB 001", Value: "prod-db-001"},
				{Name: "Production DB 002", Value: "prod-db-002"},
			},
		},
		{
			name: "key-value object",
			body: `{"Production DB 001": "prod-db-001"}`,
			want: []AllowedValue{
				{Name: "Production DB 001", Value: "prod-db-001"},
			},
		},
		{
			name: "empty array",
			body: `[]`,
			want: []AllowedValue{},
		},
		{
			name:    "invalid json",
			body:    `not json`,
			wantErr: true,
		},
		{
			name:    "number",
			body:    `42`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseValuesResponse([]byte(tt.body))
			if (err != nil) != tt.wantErr {
				t.Errorf("parseValuesResponse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if len(got) != len(tt.want) {
				t.Errorf("parseValuesResponse() returned %d values, want %d", len(got), len(tt.want))
				return
			}
			for i, g := range got {
				if g.Name != tt.want[i].Name || g.Value != tt.want[i].Value {
					t.Errorf("parseValuesResponse()[%d] = %+v, want %+v", i, g, tt.want[i])
				}
			}
		})
	}
}

func TestIsValueAllowed(t *testing.T) {
	allowed := []AllowedValue{
		{Name: "DB 1", Value: "prod-db-001"},
		{Name: "DB 2", Value: "prod-db-002"},
	}

	if !isValueAllowed("prod-db-001", allowed) {
		t.Error("expected prod-db-001 to be allowed")
	}
	if isValueAllowed("prod-db-999", allowed) {
		t.Error("expected prod-db-999 to be rejected")
	}
	if isValueAllowed("", allowed) {
		t.Error("expected empty string to be rejected")
	}
}

func TestFetchAllowedValues(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode([]string{"db-001", "db-002"})
		}))
		defer srv.Close()

		values, err := fetchAllowedValues(context.Background(), srv.URL)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(values) != 2 {
			t.Errorf("expected 2 values, got %d", len(values))
		}
	})

	t.Run("non-200 status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		_, err := fetchAllowedValues(context.Background(), srv.URL)
		if err == nil {
			t.Error("expected error for non-200 status")
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("not json"))
		}))
		defer srv.Close()

		_, err := fetchAllowedValues(context.Background(), srv.URL)
		if err == nil {
			t.Error("expected error for invalid json")
		}
	})

	t.Run("timeout returns error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(100 * time.Millisecond)
			json.NewEncoder(w).Encode([]string{"db-001"})
		}))
		defer srv.Close()

		_, err := fetchAllowedValuesWithTimeout(context.Background(), srv.URL, 1*time.Millisecond)
		if err == nil {
			t.Error("expected timeout error")
		}
		if !strings.Contains(err.Error(), "timeout") {
			t.Errorf("expected timeout in error message, got %q", err.Error())
		}
	})

	t.Run("passes user context in query", func(t *testing.T) {
		var gotUser, gotGroups string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUser = r.URL.Query().Get("user")
			gotGroups = r.URL.Query().Get("groups")
			json.NewEncoder(w).Encode([]string{"db-001"})
		}))
		defer srv.Close()

		url := substituteURL(srv.URL+"?user=${identity}&groups=${groups}", "alice", "ops,dev", "test")
		_, err := fetchAllowedValues(context.Background(), url)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotUser != "alice" {
			t.Errorf("expected user=alice, got %q", gotUser)
		}
		if gotGroups != "ops,dev" {
			t.Errorf("expected groups=ops,dev, got %q", gotGroups)
		}
	})
}

func TestValuesCache(t *testing.T) {
	t.Run("cache hit", func(t *testing.T) {
		c := newValuesCache()
		values := []AllowedValue{{Name: "a", Value: "1"}}
		c.set("http://example.com", values, 1*time.Minute)

		got, ok := c.get("http://example.com")
		if !ok {
			t.Fatal("expected cache hit")
		}
		if len(got) != 1 || got[0].Value != "1" {
			t.Errorf("unexpected cached value: %v", got)
		}
	})

	t.Run("cache miss", func(t *testing.T) {
		c := newValuesCache()
		_, ok := c.get("http://example.com")
		if ok {
			t.Error("expected cache miss")
		}
	})

	t.Run("cache expired", func(t *testing.T) {
		c := newValuesCache()
		values := []AllowedValue{{Name: "a", Value: "1"}}
		c.set("http://example.com", values, 1*time.Millisecond)

		time.Sleep(2 * time.Millisecond)

		_, ok := c.get("http://example.com")
		if ok {
			t.Error("expected cache miss after expiry")
		}
	})

	t.Run("different keys", func(t *testing.T) {
		c := newValuesCache()
		c.set("http://example.com?user=alice", []AllowedValue{{Value: "a"}}, 1*time.Minute)
		c.set("http://example.com?user=bob", []AllowedValue{{Value: "b"}}, 1*time.Minute)

		alice, _ := c.get("http://example.com?user=alice")
		bob, _ := c.get("http://example.com?user=bob")

		if alice[0].Value != "a" {
			t.Errorf("expected alice value 'a', got %q", alice[0].Value)
		}
		if bob[0].Value != "b" {
			t.Errorf("expected bob value 'b', got %q", bob[0].Value)
		}
	})
}
