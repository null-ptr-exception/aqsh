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
	HooksConfig       string
	ScriptsDir        string
	WorkerConcurrency int
	WorkerQueues      []string
	LogRetention      time.Duration
	ResultRetention   time.Duration
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
		HooksConfig:       getEnv("AQSH_HOOKS_CONFIG", "/etc/aqsh/hooks.yaml"),
		ScriptsDir:        getEnv("AQSH_SCRIPTS_DIR", "/scripts"),
		WorkerConcurrency: getEnvInt("AQSH_WORKER_CONCURRENCY", 10),
		WorkerQueues:      getEnvList("AQSH_WORKER_QUEUES", []string{"default"}),
		LogRetention:      getEnvDuration("AQSH_LOG_RETENTION", 24*time.Hour),
		ResultRetention:   getEnvDuration("AQSH_RESULT_RETENTION", 72*time.Hour),
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

func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultVal
}
