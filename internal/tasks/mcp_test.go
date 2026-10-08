package tasks

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPOptInLoadAndIncludes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tasks.yaml"), []byte("include: [read.yaml]\ntasks:\n  legacy:\n    script: legacy.sh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "read.yaml")
	body := "tasks:\n  read/status:\n    script: status.sh\n    mcp:\n      enabled: true\n      name: read_status\n      read_only: true\n      result_mode: json\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(filepath.Join(dir, "tasks.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	task, err := cfg.Resolve("read/status")
	if err != nil || task.MCP == nil || task.MCP.Name != "read_status" || cfg.Tasks["legacy"].MCP != nil {
		t.Fatalf("explicit opt-in was not preserved: %+v, %v", task, err)
	}
	if err := os.WriteFile(path, []byte(body+"  collision:\n    mcp:\n      enabled: true\n      name: read_status\n      read_only: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(dir, "tasks.yaml")); err == nil {
		t.Fatal("duplicate names across included tasks must fail")
	}
}

func TestMCPMetadataValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    *MCPTool
		ok   bool
	}{
		{"absent", nil, true},
		{"disabled write", &MCPTool{}, true},
		{"read", &MCPTool{Enabled: true, Name: "check", ReadOnly: true}, true},
		{"write rejected", &MCPTool{Enabled: true, Name: "change"}, false},
		{"name required", &MCPTool{Enabled: true, ReadOnly: true}, false},
		{"slash rejected", &MCPTool{Enabled: true, Name: "read/status", ReadOnly: true}, false},
		{"reserved", &MCPTool{Enabled: true, Name: "aqsh_get_execution", ReadOnly: true}, false},
		{"bad mode", &MCPTool{Enabled: true, Name: "check", ReadOnly: true, ResultMode: "logs"}, false},
		{"bad limit", &MCPTool{Enabled: true, Name: "check", ReadOnly: true, MaxResultBytes: -1}, false},
		{"oversized limit", &MCPTool{Enabled: true, Name: "check", ReadOnly: true, MaxResultBytes: 1048577}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &TasksConfig{Tasks: map[string]TaskDef{"check": {MCP: tc.m}}}
			if err := cfg.ValidateMCP(); (err == nil) != tc.ok {
				t.Fatalf("validation = %v, want valid=%v", err, tc.ok)
			}
		})
	}
}

func TestMCPInputSchemaAndDefaults(t *testing.T) {
	minimum, maximum := 0.0, 10.0
	cfg := &TasksConfig{Tasks: map[string]TaskDef{"check": {Input: []Input{
		{Name: "target", Env: "SECRET_INTERNAL_ENV", Type: "string", Required: true, Pattern: "^[a-z]+$", ValuesURL: "http://internal/policy"},
		{Name: "count", Type: "int", Required: true, Default: "2", Min: &minimum, Max: &maximum},
		{Name: "ratio", Type: "float", Default: "0.5"},
		{Name: "flag", Type: "bool", Default: "false"},
		{Name: "string_flag", Type: "string", Default: "true", Enum: []string{"true", "false"}},
	}}}}
	schema, err := cfg.MCPInputSchema("check")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(schema)
	if err != nil || strings.Contains(string(data), "SECRET_INTERNAL_ENV") || strings.Contains(string(data), "http://internal") {
		t.Fatalf("schema leaked internal settings: %s, %v", data, err)
	}
	if required := schema["required"].([]string); len(required) != 1 || required[0] != "target" {
		t.Fatalf("default-backed fields should not be required: %v", required)
	}
	args := map[string]any{"target": "demo", "count": nil}
	normalized, err := cfg.NormalizePayload("check", args)
	if err != nil || normalized["count"] != float64(2) || normalized["ratio"] != 0.5 ||
		normalized["flag"] != false || normalized["string_flag"] != "true" || args["count"] != nil {
		t.Fatalf("defaults/type preservation failed: %+v, %v", normalized, err)
	}
	for _, args := range []map[string]any{
		{"target": "demo", "count": 1.5},
		{"target": "demo", "ratio": math.Inf(1)},
		{"target": "demo", "count": 0x1p63},
		{"target": "demo", "identity": "spoof"},
		{"target": "demo", "string_flag": true},
	} {
		if _, err := cfg.NormalizePayload("check", args); err == nil {
			t.Fatalf("invalid arguments accepted: %+v", args)
		}
	}
}
