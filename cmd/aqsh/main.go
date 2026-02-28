package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/rophy/aqsh/internal/api"
	"github.com/rophy/aqsh/internal/config"
	"github.com/rophy/aqsh/internal/tasks"
	"github.com/rophy/aqsh/internal/worker"
	"golang.org/x/sync/errgroup"
)

var Version = "dev"

func main() {
	mode := flag.String("mode", "", "Run mode: api, worker, or both")
	tasksConfig := flag.String("tasks", "", "Path to tasks.yaml")
	bind := flag.String("bind", "", "API bind address")
	version := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *version {
		fmt.Println(Version)
		os.Exit(0)
	}

	cfg := config.Load()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	})))

	slog.Info("aqsh starting", "version", Version)

	// CLI flags override env vars
	if *mode != "" {
		cfg.Mode = *mode
	}
	if *tasksConfig != "" {
		cfg.TasksConfig = *tasksConfig
	}
	if *bind != "" {
		cfg.Bind = *bind
	}

	// Validate mode
	switch cfg.Mode {
	case "api", "worker", "both":
	default:
		slog.Error("invalid mode", "mode", cfg.Mode)
		os.Exit(1)
	}

	// Load tasks config
	tasksCfg, err := tasks.Load(cfg.TasksConfig)
	if err != nil {
		slog.Error("failed to load tasks config", "error", err)
		os.Exit(1)
	}
	slog.Info("loaded tasks config", "count", len(tasksCfg.Tasks), "path", cfg.TasksConfig)

	// Setup Redis
	var rdb redis.UniversalClient
	var asynqOpt asynq.RedisConnOpt

	if cfg.Redis.IsSentinel() {
		rdb = redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:    cfg.Redis.SentinelMaster,
			SentinelAddrs: cfg.Redis.SentinelAddrs,
			Password:      cfg.Redis.Password,
			DB:            cfg.Redis.DB,
		})
		asynqOpt = asynq.RedisFailoverClientOpt{
			MasterName:    cfg.Redis.SentinelMaster,
			SentinelAddrs: cfg.Redis.SentinelAddrs,
			Password:      cfg.Redis.Password,
			DB:            cfg.Redis.DB,
		}
	} else {
		rdb = redis.NewClient(&redis.Options{
			Addr:     cfg.Redis.Addr,
			Password: cfg.Redis.Password,
			DB:       cfg.Redis.DB,
		})
		asynqOpt = asynq.RedisClientOpt{
			Addr:     cfg.Redis.Addr,
			Password: cfg.Redis.Password,
			DB:       cfg.Redis.DB,
		}
	}

	// Test Redis connection
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		slog.Error("failed to connect to Redis", "error", err)
		os.Exit(1)
	}
	slog.Info("connected to Redis")

	// Setup signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	// Run components
	g, ctx := errgroup.WithContext(ctx)

	if cfg.Mode == "api" || cfg.Mode == "both" {
		apiServer := api.New(cfg, tasksCfg, rdb, asynqOpt, Version)
		g.Go(func() error {
			return apiServer.Run(ctx)
		})
	}

	if cfg.Mode == "worker" || cfg.Mode == "both" {
		workerServer := worker.New(cfg, tasksCfg, rdb, asynqOpt)
		g.Go(func() error {
			return workerServer.Run(ctx)
		})
	}

	if err := g.Wait(); err != nil && err != context.Canceled {
		slog.Error("runtime error", "error", err)
		os.Exit(1)
	}

	slog.Info("shutdown complete")
}
