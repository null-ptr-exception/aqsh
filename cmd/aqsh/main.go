package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/rophy/aqsh/internal/api"
	"github.com/rophy/aqsh/internal/config"
	"github.com/rophy/aqsh/internal/hooks"
	"github.com/rophy/aqsh/internal/worker"
	"golang.org/x/sync/errgroup"
)

func main() {
	mode := flag.String("mode", "", "Run mode: api, worker, or both")
	hooksConfig := flag.String("hooks", "", "Path to hooks.yaml")
	bind := flag.String("bind", "", "API bind address")
	flag.Parse()

	cfg := config.Load()

	// CLI flags override env vars
	if *mode != "" {
		cfg.Mode = *mode
	}
	if *hooksConfig != "" {
		cfg.HooksConfig = *hooksConfig
	}
	if *bind != "" {
		cfg.Bind = *bind
	}

	// Validate mode
	switch cfg.Mode {
	case "api", "worker", "both":
	default:
		log.Fatalf("Invalid mode %q: must be api, worker, or both", cfg.Mode)
	}

	// Load hooks config
	hooksCfg, err := hooks.Load(cfg.HooksConfig)
	if err != nil {
		log.Fatalf("Failed to load hooks config: %v", err)
	}
	log.Printf("Loaded %d hooks from %s", len(hooksCfg.Hooks), cfg.HooksConfig)

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
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	log.Printf("Connected to Redis")

	// Setup signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		cancel()
	}()

	// Run components
	g, ctx := errgroup.WithContext(ctx)

	if cfg.Mode == "api" || cfg.Mode == "both" {
		apiServer := api.New(cfg, hooksCfg, rdb, asynqOpt)
		g.Go(func() error {
			return apiServer.Run(ctx)
		})
	}

	if cfg.Mode == "worker" || cfg.Mode == "both" {
		workerServer := worker.New(cfg, hooksCfg, rdb, asynqOpt)
		g.Go(func() error {
			return workerServer.Run(ctx)
		})
	}

	if err := g.Wait(); err != nil && err != context.Canceled {
		log.Fatalf("Error: %v", err)
	}

	log.Printf("Shutdown complete")
}
