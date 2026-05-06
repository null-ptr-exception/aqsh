package tasks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInputValidate(t *testing.T) {
	tests := []struct {
		name    string
		input   Input
		value   any
		wantErr bool
	}{
		// Required field tests
		{
			name:    "required field missing",
			input:   Input{Name: "foo", Required: true},
			value:   nil,
			wantErr: true,
		},
		{
			name:    "required field with default",
			input:   Input{Name: "foo", Required: true, Default: "bar"},
			value:   nil,
			wantErr: false,
		},
		{
			name:    "optional field missing",
			input:   Input{Name: "foo", Required: false},
			value:   nil,
			wantErr: false,
		},

		// String type tests
		{
			name:    "string valid",
			input:   Input{Name: "foo", Type: "string"},
			value:   "hello",
			wantErr: false,
		},
		{
			name:    "string invalid type",
			input:   Input{Name: "foo", Type: "string"},
			value:   123,
			wantErr: true,
		},

		// Pattern tests
		{
			name:    "pattern match",
			input:   Input{Name: "version", Type: "string", Pattern: `^v?\d+\.\d+\.\d+$`},
			value:   "1.2.3",
			wantErr: false,
		},
		{
			name:    "pattern match with v prefix",
			input:   Input{Name: "version", Type: "string", Pattern: `^v?\d+\.\d+\.\d+$`},
			value:   "v1.2.3",
			wantErr: false,
		},
		{
			name:    "pattern no match",
			input:   Input{Name: "version", Type: "string", Pattern: `^v?\d+\.\d+\.\d+$`},
			value:   "invalid",
			wantErr: true,
		},

		// Enum tests
		{
			name:    "enum valid",
			input:   Input{Name: "env", Type: "string", Enum: []string{"dev", "staging", "prod"}},
			value:   "prod",
			wantErr: false,
		},
		{
			name:    "enum invalid",
			input:   Input{Name: "env", Type: "string", Enum: []string{"dev", "staging", "prod"}},
			value:   "invalid",
			wantErr: true,
		},

		// Int type tests
		{
			name:    "int valid",
			input:   Input{Name: "count", Type: "int"},
			value:   float64(42), // JSON numbers are float64
			wantErr: false,
		},
		{
			name:    "int invalid type",
			input:   Input{Name: "count", Type: "int"},
			value:   "42",
			wantErr: true,
		},
		{
			name:    "int min valid",
			input:   Input{Name: "count", Type: "int", Min: ptr(1.0)},
			value:   float64(5),
			wantErr: false,
		},
		{
			name:    "int min invalid",
			input:   Input{Name: "count", Type: "int", Min: ptr(1.0)},
			value:   float64(0),
			wantErr: true,
		},
		{
			name:    "int max valid",
			input:   Input{Name: "count", Type: "int", Max: ptr(100.0)},
			value:   float64(50),
			wantErr: false,
		},
		{
			name:    "int max invalid",
			input:   Input{Name: "count", Type: "int", Max: ptr(100.0)},
			value:   float64(101),
			wantErr: true,
		},

		// Float type tests
		{
			name:    "float valid",
			input:   Input{Name: "rate", Type: "float"},
			value:   float64(3.14),
			wantErr: false,
		},

		// Bool type tests
		{
			name:    "bool valid true",
			input:   Input{Name: "flag", Type: "bool"},
			value:   true,
			wantErr: false,
		},
		{
			name:    "bool valid false",
			input:   Input{Name: "flag", Type: "bool"},
			value:   false,
			wantErr: false,
		},
		{
			name:    "bool invalid type",
			input:   Input{Name: "flag", Type: "bool"},
			value:   "true",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.input.Validate(tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestInputGetEnvValue(t *testing.T) {
	tests := []struct {
		name  string
		input Input
		value any
		want  string
	}{
		{
			name:  "string value",
			input: Input{Name: "foo"},
			value: "hello",
			want:  "hello",
		},
		{
			name:  "nil with default",
			input: Input{Name: "foo", Default: "default"},
			value: nil,
			want:  "default",
		},
		{
			name:  "bool true",
			input: Input{Name: "foo", Type: "bool"},
			value: true,
			want:  "true",
		},
		{
			name:  "bool false",
			input: Input{Name: "foo", Type: "bool"},
			value: false,
			want:  "false",
		},
		{
			name:  "int value",
			input: Input{Name: "foo", Type: "int"},
			value: float64(42),
			want:  "42",
		},
		{
			name:  "float value",
			input: Input{Name: "foo", Type: "float"},
			value: float64(3.14),
			want:  "3.14",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.input.GetEnvValue(tt.value)
			if got != tt.want {
				t.Errorf("GetEnvValue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTasksConfigLoad(t *testing.T) {
	// Create a temporary tasks.yaml
	content := `
defaults:
  timeout: 5m
  max_retry: 3
  queue: default

tasks:
  test:
    script: test.sh
    description: "Test task"
    input:
      - name: foo
        env: FOO
        required: true
        type: string
`
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "tasks.yaml")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(cfg.Tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(cfg.Tasks))
	}

	task, ok := cfg.Tasks["test"]
	if !ok {
		t.Fatal("task 'test' not found")
	}

	if task.Script != "test.sh" {
		t.Errorf("expected script 'test.sh', got %q", task.Script)
	}

	if len(task.Input) != 1 {
		t.Errorf("expected 1 input, got %d", len(task.Input))
	}
}

func TestTasksConfigResolve(t *testing.T) {
	cfg := &TasksConfig{
		Defaults: TaskDefaults{
			Timeout:  "5m",
			MaxRetry: 3,
			Queue:    "default",
		},
		Tasks: map[string]TaskDef{
			"test": {
				Script:      "test.sh",
				Description: "Test",
				Timeout:     "10m", // Override default
			},
			"default-timeout": {
				Script:      "default.sh",
				Description: "Uses default timeout",
			},
		},
	}

	t.Run("resolve with override", func(t *testing.T) {
		resolved, err := cfg.Resolve("test")
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}

		if resolved.Timeout.String() != "10m0s" {
			t.Errorf("expected timeout 10m0s, got %s", resolved.Timeout)
		}

		if resolved.MaxRetry != 3 {
			t.Errorf("expected max_retry 3, got %d", resolved.MaxRetry)
		}
	})

	t.Run("resolve with defaults", func(t *testing.T) {
		resolved, err := cfg.Resolve("default-timeout")
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}

		if resolved.Timeout.String() != "5m0s" {
			t.Errorf("expected timeout 5m0s, got %s", resolved.Timeout)
		}
	})

	t.Run("resolve unknown task", func(t *testing.T) {
		_, err := cfg.Resolve("unknown")
		if err == nil {
			t.Error("expected error for unknown task")
		}
	})

	t.Run("resolve allowed_groups", func(t *testing.T) {
		cfgWithGroups := &TasksConfig{
			Tasks: map[string]TaskDef{
				"restricted": {
					Script:        "restricted.sh",
					AllowedGroups: []string{"admin", "ops"},
				},
				"open": {
					Script: "open.sh",
				},
			},
		}

		resolved, err := cfgWithGroups.Resolve("restricted")
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if len(resolved.AllowedGroups) != 2 || resolved.AllowedGroups[0] != "admin" || resolved.AllowedGroups[1] != "ops" {
			t.Errorf("expected AllowedGroups [admin, ops], got %v", resolved.AllowedGroups)
		}

		resolved, err = cfgWithGroups.Resolve("open")
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if len(resolved.AllowedGroups) != 0 {
			t.Errorf("expected empty AllowedGroups, got %v", resolved.AllowedGroups)
		}
	})

	t.Run("resolve allowed_users", func(t *testing.T) {
		cfg := &TasksConfig{
			Tasks: map[string]TaskDef{
				"sa-only": {
					Script:       "sa.sh",
					AllowedUsers: []string{"system:serviceaccount:rdsma:sertdxkkk"},
				},
				"both": {
					Script:        "both.sh",
					AllowedUsers:  []string{"alice"},
					AllowedGroups: []string{"ops"},
				},
			},
		}

		resolved, err := cfg.Resolve("sa-only")
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if len(resolved.AllowedUsers) != 1 || resolved.AllowedUsers[0] != "system:serviceaccount:rdsma:sertdxkkk" {
			t.Errorf("expected AllowedUsers [system:serviceaccount:rdsma:sertdxkkk], got %v", resolved.AllowedUsers)
		}

		resolved, err = cfg.Resolve("both")
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if len(resolved.AllowedUsers) != 1 || resolved.AllowedUsers[0] != "alice" {
			t.Errorf("expected AllowedUsers [alice], got %v", resolved.AllowedUsers)
		}
		if len(resolved.AllowedGroups) != 1 || resolved.AllowedGroups[0] != "ops" {
			t.Errorf("expected AllowedGroups [ops], got %v", resolved.AllowedGroups)
		}
	})
}

func TestValidatePayload(t *testing.T) {
	cfg := &TasksConfig{
		Defaults: TaskDefaults{
			Timeout: "5m",
			Queue:   "default",
		},
		Tasks: map[string]TaskDef{
			"deploy": {
				Script: "deploy.sh",
				Input: []Input{
					{Name: "version", Env: "VERSION", Required: true, Type: "string", Pattern: `^v?\d+\.\d+\.\d+$`},
					{Name: "env", Env: "ENVIRONMENT", Required: true, Type: "string", Enum: []string{"dev", "prod"}},
					{Name: "dry_run", Env: "DRY_RUN", Required: false, Type: "bool", Default: "false"},
				},
			},
		},
	}

	t.Run("valid payload", func(t *testing.T) {
		payload := map[string]any{
			"version": "1.2.3",
			"env":     "prod",
		}

		env, err := cfg.ValidatePayload("deploy", payload)
		if err != nil {
			t.Fatalf("ValidatePayload() error = %v", err)
		}

		if env["VERSION"] != "1.2.3" {
			t.Errorf("expected VERSION=1.2.3, got %s", env["VERSION"])
		}
		if env["ENVIRONMENT"] != "prod" {
			t.Errorf("expected ENVIRONMENT=prod, got %s", env["ENVIRONMENT"])
		}
		if env["DRY_RUN"] != "false" {
			t.Errorf("expected DRY_RUN=false, got %s", env["DRY_RUN"])
		}
	})

	t.Run("missing required field", func(t *testing.T) {
		payload := map[string]any{
			"version": "1.2.3",
			// missing "env"
		}

		_, err := cfg.ValidatePayload("deploy", payload)
		if err == nil {
			t.Error("expected error for missing required field")
		}
	})

	t.Run("invalid pattern", func(t *testing.T) {
		payload := map[string]any{
			"version": "invalid",
			"env":     "prod",
		}

		_, err := cfg.ValidatePayload("deploy", payload)
		if err == nil {
			t.Error("expected error for invalid pattern")
		}
	})

	t.Run("invalid enum", func(t *testing.T) {
		payload := map[string]any{
			"version": "1.2.3",
			"env":     "staging", // not in enum
		}

		_, err := cfg.ValidatePayload("deploy", payload)
		if err == nil {
			t.Error("expected error for invalid enum")
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		payload := map[string]any{
			"version": "1.2.3",
			"env":     "prod",
			"unknown": "value",
		}

		_, err := cfg.ValidatePayload("deploy", payload)
		if err == nil {
			t.Error("expected error for unknown field")
		}
	})
}

func TestLoadMutualExclusion(t *testing.T) {
	content := `
tasks:
  bad:
    script: test.sh
    input:
      - name: env
        env: ENV
        type: string
        enum: [dev, prod]
        values_url: "http://example.com/values"
`
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "tasks.yaml")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(tmpFile)
	if err == nil {
		t.Error("expected error for values_url + enum")
	}
}

func TestLoadWithValuesURL(t *testing.T) {
	content := `
tasks:
  test:
    script: test.sh
    input:
      - name: instance
        env: INSTANCE
        type: string
        values_url: "http://authz.internal/instances?user=${identity}"
`
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "tasks.yaml")
	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	task := cfg.Tasks["test"]
	if task.Input[0].ValuesURL != "http://authz.internal/instances?user=${identity}" {
		t.Errorf("expected values_url to be set, got %q", task.Input[0].ValuesURL)
	}
}

func TestLoadValuesCacheValidation(t *testing.T) {
	t.Run("valid values_cache", func(t *testing.T) {
		content := `
tasks:
  test:
    script: test.sh
    input:
      - name: instance
        type: string
        values_url: "http://example.com/values"
        values_cache: 30s
`
		tmpDir := t.TempDir()
		tmpFile := filepath.Join(tmpDir, "tasks.yaml")
		if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile error = %v", err)
		}

		cfg, err := Load(tmpFile)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.Tasks["test"].Input[0].ValuesCache != "30s" {
			t.Errorf("expected values_cache=30s, got %q", cfg.Tasks["test"].Input[0].ValuesCache)
		}
	})

	t.Run("values_cache without values_url", func(t *testing.T) {
		content := `
tasks:
  test:
    script: test.sh
    input:
      - name: env
        type: string
        values_cache: 30s
`
		tmpDir := t.TempDir()
		tmpFile := filepath.Join(tmpDir, "tasks.yaml")
		if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile error = %v", err)
		}

		_, err := Load(tmpFile)
		if err == nil {
			t.Error("expected error for values_cache without values_url")
		}
	})

	t.Run("invalid values_cache duration", func(t *testing.T) {
		content := `
tasks:
  test:
    script: test.sh
    input:
      - name: instance
        type: string
        values_url: "http://example.com/values"
        values_cache: notaduration
`
		tmpDir := t.TempDir()
		tmpFile := filepath.Join(tmpDir, "tasks.yaml")
		if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile error = %v", err)
		}

		_, err := Load(tmpFile)
		if err == nil {
			t.Error("expected error for invalid values_cache duration")
		}
	})
}

func ptr(f float64) *float64 {
	return &f
}
