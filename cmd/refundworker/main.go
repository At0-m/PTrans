package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/At0-m/PTrans/internal/config"
	"github.com/At0-m/PTrans/internal/provider"
	"github.com/At0-m/PTrans/internal/refunds"
	postgres "github.com/At0-m/PTrans/internal/storage"
)

func run() error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	opts, err := config.RefundOptions()
	if err != nil {
		return err
	}
	p, err := provider.NewClient(config.Env("PROVIDER_URL", "http://localhost:8091"), os.Getenv("PROVIDER_API_KEY"), opts.RequestTimeout)
	if err != nil {
		return err
	}
	store, err := postgres.New(ctx, config.Env("DATABASE_URL", "postgres://postgres:postgres@localhost:5433/ptrans?sslmode=disable"))
	if err != nil {
		return err
	}
	defer store.Close()
	w, err := refunds.NewWorker(store, p, logger, opts)
	if err != nil {
		return err
	}
	return w.Run(ctx)
}

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("refund worker stopped", "error", err)
		os.Exit(1)
	}
}
