package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// Authentication files must not silently ignore configuration after a document.
func TestLoadRejectsTrailingDocuments(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"one document", "audiences: [aqsh]\n# trailing comment\n", true},
		{"explicit end", "audiences: [aqsh]\n...\n", true},
		{"second config", "audiences: [aqsh]\n---\naudiences: [other]\n", false},
		{"empty second document", "audiences: [aqsh]\n---\n", false},
		{"invalid trailing YAML", "audiences: [aqsh]\n---\n[invalid", false},
		{"unknown field", "unknown: true\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.yaml")
			if err := os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}
