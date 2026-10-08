package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Mode              string
	Bind              string
	TasksConfig       string
	TasksDir          string
	ResultsDir        string
	WorkerConcurrency int
	WorkerQueues      []string
	LogRetention      time.Duration
	ResultRetention   time.Duration
	IdentityHeader    string
	RequireIdentity   bool
	GroupsHeader      string
	MCPEnabled        bool
	MCPAllowedOrigins []string
	ExecutionAccess   string
	LogLevel          slog.Level
	Redis             RedisConfig
}

type RedisConfig struct {
	Addr           string
	Password       string
	DB             int
	SentinelAddrs  []string
	SentinelMaster string
}

func (r RedisConfig) IsSentinel() bool {
	return len(r.SentinelAddrs) > 0
}

func Load() *Config {
	return &Config{
		Mode:              getEnv("AQSH_MODE", "both"),
		Bind:              getEnv("AQSH_BIND", "0.0.0.0:8080"),
		TasksConfig:       getEnv("AQSH_TASKS_CONFIG", "/etc/aqsh/tasks.yaml"),
		TasksDir:          getEnv("AQSH_TASKS_DIR", "/tasks"),
		ResultsDir:        getEnv("AQSH_RESULTS_DIR", "/var/lib/aqsh/results"),
		WorkerConcurrency: getEnvInt("AQSH_WORKER_CONCURRENCY", 10),
		WorkerQueues:      getEnvList("AQSH_WORKER_QUEUES", []string{"default"}),
		LogRetention:      getEnvDuration("AQSH_LOG_RETENTION", 24*time.Hour),
		ResultRetention:   getEnvDuration("AQSH_RESULT_RETENTION", 72*time.Hour),
		IdentityHeader:    getEnv("AQSH_IDENTITY_HEADER", "X-Forwarded-User"),
		RequireIdentity:   getEnvBool("AQSH_REQUIRE_IDENTITY", false),
		GroupsHeader:      getEnv("AQSH_GROUPS_HEADER", "X-Forwarded-Groups"),
		MCPEnabled:        getEnvBool("AQSH_MCP_ENABLED", false),
		MCPAllowedOrigins: getEnvList("AQSH_MCP_ALLOWED_ORIGINS", nil),
		ExecutionAccess:   getEnv("AQSH_EXECUTION_ACCESS", "legacy"),
		LogLevel:          getEnvLogLevel("AQSH_LOG_LEVEL", slog.LevelInfo),
		Redis: RedisConfig{
			Addr:           getEnv("AQSH_REDIS_ADDR", "localhost:6379"),
			Password:       getEnv("AQSH_REDIS_PASSWORD", ""),
			DB:             getEnvInt("AQSH_REDIS_DB", 0),
			SentinelAddrs:  getEnvList("AQSH_REDIS_SENTINEL_ADDRS", nil),
			SentinelMaster: getEnv("AQSH_REDIS_SENTINEL_MASTER", "mymaster"),
		},
	}
}

// Validate rejects configurations that would expose MCP without execution isolation.
func (c *Config) Validate() error {
	switch c.ExecutionAccess {
	case "", "legacy", "owner":
	default:
		return fmt.Errorf("AQSH_EXECUTION_ACCESS must be legacy or owner")
	}
	if c.ExecutionAccess == "owner" && (!c.RequireIdentity || c.IdentityHeader == "") {
		return fmt.Errorf("owner execution access requires AQSH_REQUIRE_IDENTITY=true and an identity header")
	}
	if c.MCPEnabled && c.ExecutionAccess != "owner" {
		return fmt.Errorf("MCP requires AQSH_EXECUTION_ACCESS=owner")
	}
	for _, origin := range c.MCPAllowedOrigins {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return fmt.Errorf("invalid MCP allowed origin %q: use scheme://host[:port]", origin)
		}
	}
	return nil
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultVal
}

func getEnvList(key string, defaultVal []string) []string {
	if v := os.Getenv(key); v != "" {
		return strings.Split(v, ",")
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	if v := os.Getenv(key); v != "" {
		return v == "true" || v == "1"
	}
	return defaultVal
}

func getEnvLogLevel(key string, defaultVal slog.Level) slog.Level {
	if v := os.Getenv(key); v != "" {
		var level slog.Level
		if err := level.UnmarshalText([]byte(v)); err == nil {
			return level
		}
	}
	return defaultVal
}

func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultVal
}
