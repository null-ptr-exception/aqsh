package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestAuthenticationEnvironment(t *testing.T) {
	// Keep the tests independent of local auth settings, including unknown names.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "AQSH_AUTH_") {
			t.Setenv(key, "")
			os.Unsetenv(key)
		}
	}
	t.Setenv("AQSH_MODE", "api")
	t.Setenv("AQSH_AUTH_MODE", "kubernetes")
	t.Setenv("AQSH_AUTH_URL", "https://kubernetes.default.svc")
	t.Setenv("AQSH_AUTH_TOKEN_FILE", "/mounted/token")
	t.Setenv("AQSH_AUTH_AUDIENCES", "aqsh")
	t.Run("defaults", func(t *testing.T) {
		c := Load()
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		if c.Auth.CacheTTL != 10*time.Second || c.Auth.RequestTimeout != 5*time.Second || c.Auth.IncludeClusterInIdentity || c.Auth.TrustClusterExtra {
			t.Fatalf("unexpected defaults: %+v", c.Auth)
		}
	})
	for _, tc := range []struct{ key, value string }{
		{"AQSH_AUTH_INCLUDE_CLUSTER_IN_IDENTITY", "tru"},
		{"AQSH_AUTH_TRUST_CLUSTER_EXTRA", "yes"},
		{"AQSH_AUTH_ALLOW_HTTP", ""},
		{"AQSH_AUTH_REQUEST_TIMEOUT", "typo"},
		{"AQSH_AUTH_REQUEST_TIMEOUT", "0s"},
		{"AQSH_AUTH_REQUEST_TIMEOUT", "-1s"},
		{"AQSH_AUTH_CACHE_TTL", "10"},
		{"AQSH_AUTH_CACHE_TTL", "-1s"},
		{"AQSH_AUTH_CACHE_TTL", "61s"},
		{"AQSH_AUTH_AUDIENCES", "aqsh,"},
		{"AQSH_AUTH_URL", "http://api"},
		{"AQSH_AUTH_TOKEN_FILE", ""},
		{"AQSH_AUTH_CONFIG", "/old/auth.yaml"},
		{"AQSH_AUTH_INCLUDE_CLUSTER_IN_IDENTIY", "true"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if err := Load().Validate(); err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("expected error naming %s, got %v", tc.key, err)
			}
		})
	}
	t.Run("explicit options", func(t *testing.T) {
		t.Setenv("AQSH_AUTH_INCLUDE_CLUSTER_IN_IDENTITY", "true")
		t.Setenv("AQSH_AUTH_TRUST_CLUSTER_EXTRA", "true")
		t.Setenv("AQSH_AUTH_ALLOW_HTTP", "true")
		t.Setenv("AQSH_AUTH_URL", "http://federated-auth:8080")
		t.Setenv("AQSH_AUTH_CACHE_TTL", "0s")
		t.Setenv("AQSH_AUTH_REQUEST_TIMEOUT", "2s")
		c := Load()
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		if !c.Auth.IncludeClusterInIdentity || !c.Auth.TrustClusterExtra || !c.Auth.TokenReview.AllowHTTP || c.Auth.CacheTTL != 0 || c.Auth.RequestTimeout != 2*time.Second {
			t.Fatalf("unexpected config: %+v", c.Auth)
		}
	})
	t.Run("auth settings cannot silently select legacy mode", func(t *testing.T) {
		t.Setenv("AQSH_AUTH_MODE", "trusted-header")
		if err := Load().Validate(); err == nil {
			t.Fatal("native settings ignored")
		}
	})
	t.Run("worker ignores API settings", func(t *testing.T) {
		t.Setenv("AQSH_MODE", "worker")
		t.Setenv("AQSH_AUTH_TRUST_CLUSTER_EXTRA", "typo")
		if err := Load().Validate(); err != nil {
			t.Fatal(err)
		}
	})
}
