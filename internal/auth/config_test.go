package auth

import (
	"testing"
	"time"
)

func TestValidateConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
		valid  bool
	}{
		{"shared needs no cluster", func(*Config) {}, true},
		{"qualified direct", func(c *Config) { c.IncludeClusterInIdentity = true; c.ClusterName = "a" }, true},
		{"qualified missing cluster", func(c *Config) { c.IncludeClusterInIdentity = true }, false},
		{"qualified federated", func(c *Config) { c.IncludeClusterInIdentity = true; c.TrustClusterExtra = true }, true},
		{"federated cannot fall back", func(c *Config) { c.TrustClusterExtra = true; c.ClusterName = "a" }, false},
		{"comma in cluster", func(c *Config) { c.ClusterName = "a,b" }, false},
		{"slash in cluster", func(c *Config) { c.ClusterName = "a/b" }, false},
		{"missing audience", func(c *Config) { c.Audiences = nil }, false},
		{"empty audience", func(c *Config) { c.Audiences = []string{"aqsh", ""} }, false},
		{"spaced audience", func(c *Config) { c.Audiences = []string{" aqsh"} }, false},
		{"missing reviewer", func(c *Config) { c.TokenReview.TokenFile = "" }, false},
		{"missing URL", func(c *Config) { c.TokenReview.URL = "" }, false},
		{"HTTP requires opt in", func(c *Config) { c.TokenReview.URL = "http://api" }, false},
		{"HTTP allowed", func(c *Config) { c.TokenReview.URL = "http://api"; c.TokenReview.AllowHTTP = true }, true},
		{"negative timeout", func(c *Config) { c.RequestTimeout = -time.Second }, false},
		{"large timeout", func(c *Config) { c.RequestTimeout = 2 * time.Minute }, false},
		{"negative cache TTL", func(c *Config) { c.CacheTTL = -time.Second }, false},
		{"large cache TTL", func(c *Config) { c.CacheTTL = 2 * time.Minute }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{Audiences: []string{"aqsh"}, TokenReview: Endpoint{URL: "https://api", TokenFile: "/mounted/token"}}
			tc.change(&c)
			if err := c.Validate(); (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}
