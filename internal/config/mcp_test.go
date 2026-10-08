package config

import "testing"

func TestMCPConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"legacy unchanged", Config{}, true},
		{"MCP requires isolation", Config{MCPEnabled: true}, false},
		{"owner requires identity", Config{ExecutionAccess: "owner"}, false},
		{"owner requires header", Config{ExecutionAccess: "owner", RequireIdentity: true}, false},
		{"unknown policy", Config{ExecutionAccess: "shared"}, false},
		{"safe MCP", Config{MCPEnabled: true, ExecutionAccess: "owner", RequireIdentity: true, IdentityHeader: "X-User"}, true},
		{"wildcard origin rejected", Config{MCPAllowedOrigins: []string{"*"}}, false},
		{"origin path rejected", Config{MCPAllowedOrigins: []string{"https://client.example/path"}}, false},
		{"origin credentials rejected", Config{MCPAllowedOrigins: []string{"https://user@client.example"}}, false},
		{"origin query rejected", Config{MCPAllowedOrigins: []string{"https://client.example?next=x"}}, false},
		{"exact origins", Config{MCPAllowedOrigins: []string{"https://client.example", "http://localhost:3000"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate() = %v, want valid=%v", err, tc.ok)
			}
		})
	}
	t.Setenv("AQSH_MCP_ENABLED", "true")
	t.Setenv("AQSH_EXECUTION_ACCESS", "owner")
	t.Setenv("AQSH_REQUIRE_IDENTITY", "true")
	t.Setenv("AQSH_MCP_ALLOWED_ORIGINS", "https://client.example,http://localhost:3000")
	cfg := Load()
	if !cfg.MCPEnabled || cfg.ExecutionAccess != "owner" || len(cfg.MCPAllowedOrigins) != 2 || cfg.Validate() != nil {
		t.Fatalf("MCP environment configuration was not loaded: %+v", cfg)
	}
}
