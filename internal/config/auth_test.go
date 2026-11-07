package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Service discovery must not select an auth mode or hide misspelled auth settings.
func TestAuthenticationServiceLinks(t *testing.T) {
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "AQSH_AUTH_") {
			t.Setenv(key, "")
			os.Unsetenv(key)
		}
	}
	t.Setenv("AQSH_MODE", "api")
	for _, mode := range []string{"trusted-header", "kubernetes"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AQSH_AUTH_MODE", mode)
			if mode == "kubernetes" {
				t.Setenv("AQSH_AUTH_URL", "https://kubernetes.default.svc")
				t.Setenv("AQSH_AUTH_TOKEN_FILE", "/mounted/token")
				t.Setenv("AQSH_AUTH_AUDIENCES", "aqsh")
			}
			want := Load()
			for _, prefix := range []string{"AQSH_AUTH", "AQSH_AUTH_PROXY"} {
				for suffix, value := range map[string]string{
					"SERVICE_HOST":              "10.0.0.10",
					"SERVICE_PORT":              "8443",
					"SERVICE_PORT_HTTPS":        "8443",
					"SERVICE_PORT_METRICS_HTTP": "9090",
					"PORT":                      "tcp://10.0.0.10:8443",
				} {
					t.Setenv(prefix+"_"+suffix, value)
				}
				for _, protocol := range []string{"TCP", "UDP", "SCTP"} {
					portPrefix := prefix + "_PORT_8443_" + protocol
					t.Setenv(portPrefix, strings.ToLower(protocol)+"://10.0.0.10:8443")
					t.Setenv(portPrefix+"_PROTO", strings.ToLower(protocol))
					t.Setenv(portPrefix+"_PORT", "8443")
					t.Setenv(portPrefix+"_ADDR", "10.0.0.10")
				}
			}
			got := Load()
			if err := got.Validate(); err != nil {
				t.Fatalf("service links prevent startup: %v", err)
			}
			if got.AuthMode != want.AuthMode || !reflect.DeepEqual(got.Auth, want.Auth) {
				t.Fatal("service links changed authentication settings")
			}
			for _, key := range []string{
				"AQSH_AUTH_INCLUDE_CLUSTER_IN_IDENTIY",
				"AQSH_AUTH_PORT_TIMEOUT",
				"AQSH_AUTH_PORT_8443_TCP_TIMEOUT",
				"AQSH_AUTH_PORT_8443_HTTP",
				"AQSH_AUTH_SERVICE_PORTAL",
				"AQSH_AUTH_SERVICE_PORT_",
				"AQSH_AUTH_SERVICE_HOST_EXTRA",
			} {
				t.Run(key, func(t *testing.T) {
					t.Setenv(key, "typo")
					if err := Load().Validate(); err == nil || !strings.Contains(err.Error(), key) {
						t.Fatalf("expected error naming %s, got %v", key, err)
					}
				})
			}
		})
	}
}

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
