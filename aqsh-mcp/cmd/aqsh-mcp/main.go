package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rophy/aqsh/aqsh-mcp/internal/adapter"
)

func main() {
	path := flag.String("config", "config.yaml", "Path to adapter configuration")
	flag.Parse()
	if err := run(*path); err != nil {
		slog.Error("aqsh-mcp stopped", "error", err)
		os.Exit(1)
	}
}

func run(path string) error {
	cfg, err := adapter.LoadConfig(path)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	handler, err := adapter.New(startup, cfg)
	cancel()
	if err != nil {
		return err
	}
	server := &http.Server{Addr: cfg.Bind, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	slog.Info("aqsh-mcp listening", "addr", cfg.Bind)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
