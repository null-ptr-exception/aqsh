package config

import (
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
		Redis: RedisConfig{
			Addr:           getEnv("AQSH_REDIS_ADDR", "localhost:6379"),
			Password:       getEnv("AQSH_REDIS_PASSWORD", ""),
			DB:             getEnvInt("AQSH_REDIS_DB", 0),
			SentinelAddrs:  getEnvList("AQSH_REDIS_SENTINEL_ADDRS", nil),
			SentinelMaster: getEnv("AQSH_REDIS_SENTINEL_MASTER", "mymaster"),
		},
	}
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

func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultVal
}
