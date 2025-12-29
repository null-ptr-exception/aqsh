package config

import (
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
