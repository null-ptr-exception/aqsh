package config

import (
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	// Clear any existing env vars
	envVars := []string{
		"AQSH_MODE",
		"AQSH_BIND",
		"AQSH_TASKS_CONFIG",
		"AQSH_TASKS_DIR",
		"AQSH_WORKER_CONCURRENCY",
		"AQSH_WORKER_QUEUES",
		"AQSH_LOG_RETENTION",
		"AQSH_RESULT_RETENTION",
		"AQSH_REDIS_ADDR",
		"AQSH_REDIS_PASSWORD",
		"AQSH_REDIS_DB",
		"AQSH_REDIS_SENTINEL_ADDRS",
		"AQSH_REDIS_SENTINEL_MASTER",
		"AQSH_IDENTITY_HEADER",
		"AQSH_REQUIRE_IDENTITY",
		"AQSH_GROUPS_HEADER",
		"AQSH_LOG_LEVEL",
	}
	for _, v := range envVars {
		os.Unsetenv(v)
	}

	t.Run("defaults", func(t *testing.T) {
		cfg := Load()

		if cfg.Mode != "both" {
			t.Errorf("expected Mode 'both', got %q", cfg.Mode)
		}
		if cfg.Bind != "0.0.0.0:8080" {
			t.Errorf("expected Bind '0.0.0.0:8080', got %q", cfg.Bind)
		}
		if cfg.TasksConfig != "/etc/aqsh/tasks.yaml" {
			t.Errorf("expected TasksConfig '/etc/aqsh/tasks.yaml', got %q", cfg.TasksConfig)
		}
		if cfg.TasksDir != "/tasks" {
			t.Errorf("expected TasksDir '/tasks', got %q", cfg.TasksDir)
		}
		if cfg.WorkerConcurrency != 10 {
			t.Errorf("expected WorkerConcurrency 10, got %d", cfg.WorkerConcurrency)
		}
		if len(cfg.WorkerQueues) != 1 || cfg.WorkerQueues[0] != "default" {
			t.Errorf("expected WorkerQueues [default], got %v", cfg.WorkerQueues)
		}
		if cfg.LogRetention != 24*time.Hour {
			t.Errorf("expected LogRetention 24h, got %v", cfg.LogRetention)
		}
		if cfg.ResultRetention != 72*time.Hour {
			t.Errorf("expected ResultRetention 72h, got %v", cfg.ResultRetention)
		}
		if cfg.IdentityHeader != "X-Forwarded-User" {
			t.Errorf("expected IdentityHeader 'X-Forwarded-User', got %q", cfg.IdentityHeader)
		}
		if cfg.RequireIdentity {
			t.Error("expected RequireIdentity to be false by default")
		}
		if cfg.GroupsHeader != "X-Forwarded-Groups" {
			t.Errorf("expected GroupsHeader 'X-Forwarded-Groups', got %q", cfg.GroupsHeader)
		}
		if cfg.LogLevel != slog.LevelInfo {
			t.Errorf("expected LogLevel INFO, got %v", cfg.LogLevel)
		}
		if cfg.Redis.Addr != "localhost:6379" {
			t.Errorf("expected Redis.Addr 'localhost:6379', got %q", cfg.Redis.Addr)
		}
		if cfg.Redis.IsSentinel() {
			t.Error("expected IsSentinel() to be false")
		}
	})

	t.Run("env overrides", func(t *testing.T) {
		os.Setenv("AQSH_MODE", "api")
		os.Setenv("AQSH_BIND", "127.0.0.1:9000")
		os.Setenv("AQSH_WORKER_CONCURRENCY", "20")
		os.Setenv("AQSH_WORKER_QUEUES", "high,low")
		os.Setenv("AQSH_LOG_RETENTION", "48h")
		defer func() {
			for _, v := range envVars {
				os.Unsetenv(v)
			}
		}()

		cfg := Load()

		if cfg.Mode != "api" {
			t.Errorf("expected Mode 'api', got %q", cfg.Mode)
		}
		if cfg.Bind != "127.0.0.1:9000" {
			t.Errorf("expected Bind '127.0.0.1:9000', got %q", cfg.Bind)
		}
		if cfg.WorkerConcurrency != 20 {
			t.Errorf("expected WorkerConcurrency 20, got %d", cfg.WorkerConcurrency)
		}
		if len(cfg.WorkerQueues) != 2 || cfg.WorkerQueues[0] != "high" || cfg.WorkerQueues[1] != "low" {
			t.Errorf("expected WorkerQueues [high, low], got %v", cfg.WorkerQueues)
		}
		if cfg.LogRetention != 48*time.Hour {
			t.Errorf("expected LogRetention 48h, got %v", cfg.LogRetention)
		}
	})

	t.Run("sentinel config", func(t *testing.T) {
		os.Setenv("AQSH_REDIS_SENTINEL_ADDRS", "sentinel1:26379,sentinel2:26379")
		os.Setenv("AQSH_REDIS_SENTINEL_MASTER", "mymaster")
		defer func() {
			for _, v := range envVars {
				os.Unsetenv(v)
			}
		}()

		cfg := Load()

		if !cfg.Redis.IsSentinel() {
			t.Error("expected IsSentinel() to be true")
		}
		if len(cfg.Redis.SentinelAddrs) != 2 {
			t.Errorf("expected 2 sentinel addrs, got %d", len(cfg.Redis.SentinelAddrs))
		}
		if cfg.Redis.SentinelMaster != "mymaster" {
			t.Errorf("expected SentinelMaster 'mymaster', got %q", cfg.Redis.SentinelMaster)
		}
	})
}

func TestGetEnvInt(t *testing.T) {
	os.Unsetenv("TEST_INT")

	t.Run("default", func(t *testing.T) {
		got := getEnvInt("TEST_INT", 42)
		if got != 42 {
			t.Errorf("expected 42, got %d", got)
		}
	})

	t.Run("valid int", func(t *testing.T) {
		os.Setenv("TEST_INT", "100")
		defer os.Unsetenv("TEST_INT")

		got := getEnvInt("TEST_INT", 42)
		if got != 100 {
			t.Errorf("expected 100, got %d", got)
		}
	})

	t.Run("invalid int", func(t *testing.T) {
		os.Setenv("TEST_INT", "invalid")
		defer os.Unsetenv("TEST_INT")

		got := getEnvInt("TEST_INT", 42)
		if got != 42 {
			t.Errorf("expected default 42, got %d", got)
		}
	})
}

func TestGetEnvBool(t *testing.T) {
	os.Unsetenv("TEST_BOOL")

	t.Run("default false", func(t *testing.T) {
		got := getEnvBool("TEST_BOOL", false)
		if got {
			t.Error("expected false, got true")
		}
	})

	t.Run("default true", func(t *testing.T) {
		got := getEnvBool("TEST_BOOL", true)
		if !got {
			t.Error("expected true, got false")
		}
	})

	t.Run("true", func(t *testing.T) {
		os.Setenv("TEST_BOOL", "true")
		defer os.Unsetenv("TEST_BOOL")

		got := getEnvBool("TEST_BOOL", false)
		if !got {
			t.Error("expected true, got false")
		}
	})

	t.Run("1", func(t *testing.T) {
		os.Setenv("TEST_BOOL", "1")
		defer os.Unsetenv("TEST_BOOL")

		got := getEnvBool("TEST_BOOL", false)
		if !got {
			t.Error("expected true, got false")
		}
	})

	t.Run("false", func(t *testing.T) {
		os.Setenv("TEST_BOOL", "false")
		defer os.Unsetenv("TEST_BOOL")

		got := getEnvBool("TEST_BOOL", true)
		if got {
			t.Error("expected false, got true")
		}
	})

	t.Run("other value", func(t *testing.T) {
		os.Setenv("TEST_BOOL", "yes")
		defer os.Unsetenv("TEST_BOOL")

		got := getEnvBool("TEST_BOOL", true)
		if got {
			t.Error("expected false for non-true/1 value, got true")
		}
	})
}

func TestGetEnvLogLevel(t *testing.T) {
	os.Unsetenv("TEST_LOG_LEVEL")

	t.Run("default", func(t *testing.T) {
		got := getEnvLogLevel("TEST_LOG_LEVEL", slog.LevelInfo)
		if got != slog.LevelInfo {
			t.Errorf("expected INFO, got %v", got)
		}
	})

	t.Run("debug", func(t *testing.T) {
		os.Setenv("TEST_LOG_LEVEL", "debug")
		defer os.Unsetenv("TEST_LOG_LEVEL")

		got := getEnvLogLevel("TEST_LOG_LEVEL", slog.LevelInfo)
		if got != slog.LevelDebug {
			t.Errorf("expected DEBUG, got %v", got)
		}
	})

	t.Run("DEBUG uppercase", func(t *testing.T) {
		os.Setenv("TEST_LOG_LEVEL", "DEBUG")
		defer os.Unsetenv("TEST_LOG_LEVEL")

		got := getEnvLogLevel("TEST_LOG_LEVEL", slog.LevelInfo)
		if got != slog.LevelDebug {
			t.Errorf("expected DEBUG, got %v", got)
		}
	})

	t.Run("invalid falls back to default", func(t *testing.T) {
		os.Setenv("TEST_LOG_LEVEL", "bogus")
		defer os.Unsetenv("TEST_LOG_LEVEL")

		got := getEnvLogLevel("TEST_LOG_LEVEL", slog.LevelInfo)
		if got != slog.LevelInfo {
			t.Errorf("expected INFO, got %v", got)
		}
	})
}

func TestGetEnvDuration(t *testing.T) {
	os.Unsetenv("TEST_DUR")

	t.Run("default", func(t *testing.T) {
		got := getEnvDuration("TEST_DUR", time.Hour)
		if got != time.Hour {
			t.Errorf("expected 1h, got %v", got)
		}
	})

	t.Run("valid duration", func(t *testing.T) {
		os.Setenv("TEST_DUR", "30m")
		defer os.Unsetenv("TEST_DUR")

		got := getEnvDuration("TEST_DUR", time.Hour)
		if got != 30*time.Minute {
			t.Errorf("expected 30m, got %v", got)
		}
	})

	t.Run("invalid duration", func(t *testing.T) {
		os.Setenv("TEST_DUR", "invalid")
		defer os.Unsetenv("TEST_DUR")

		got := getEnvDuration("TEST_DUR", time.Hour)
		if got != time.Hour {
			t.Errorf("expected default 1h, got %v", got)
		}
	})
}
