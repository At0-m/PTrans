package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	postgresstore "github.com/At0-m/PTrans/internal/storage"
	"github.com/At0-m/PTrans/internal/worker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	databaseURL := envOrDefault("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/ptrans?sslmode=disable")
	store, err := postgresstore.New(ctx, databaseURL)
	if err != nil {
		logger.Error("create postgres store", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	w := worker.New(store, logger, worker.Options{
		BatchSize:      envOrDefaultInt("WORKER_BATCH_SIZE", 10),
		PollInterval:   envOrDefaultDuration("WORKER_POLL_INTERVAL", 2*time.Second),
		LockFor:        envOrDefaultDuration("WORKER_LOCK_FOR", 30*time.Second),
		HTTPTimeout:    envOrDefaultDuration("WEBHOOK_HTTP_TIMEOUT", 5*time.Second),
		MaxAttempts:    envOrDefaultInt("WEBHOOK_MAX_ATTEMPTS", 5),
		InitialBackoff: envOrDefaultDuration("WEBHOOK_INITIAL_BACKOFF", 2*time.Second),
		MaxBackoff:     envOrDefaultDuration("WEBHOOK_MAX_BACKOFF", time.Minute),
	})

	if err := w.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("worker stopped with error", "error", err)
		os.Exit(1)
	}
}

func envOrDefault(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func envOrDefaultInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func envOrDefaultDuration(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return value
}
