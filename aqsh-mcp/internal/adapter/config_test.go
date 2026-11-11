package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigRejectsAmbiguousPolicy(t *testing.T) {
	valid := "public_url: http://127.0.0.1:8090/mcp\nbackend_url: http://127.0.0.1:8080\noauth:\n  issuer: http://127.0.0.1:9000\n  required_scopes: [aqsh:read]\n"
	for _, tc := range []struct {
		name, data string
		wantError  bool
	}{
		{"valid", valid, false},
		{"unknown-field", valid + "unexpected: true\n", true},
		{"second-document", valid + "---\ntools: []\n", true},
		{"duplicate-field", valid + "public_url: https://other.example/mcp\n", true},
		// A valid prefix followed by oversized comments must not be silently accepted.
		{"oversized", valid + "#" + strings.Repeat("x", 1<<20), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfig(path)
			if (err != nil) != tc.wantError {
				t.Fatalf("LoadConfig error = %v, wantError = %v", err, tc.wantError)
			}
		})
	}
}
