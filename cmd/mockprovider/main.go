package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/At0-m/PTrans/internal/config"
	"github.com/At0-m/PTrans/internal/mockprovider"
)

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	delay, err := time.ParseDuration(config.Env("MOCK_PROVIDER_DELAY", "8s"))
	if err != nil {
		return err
	}
	mock, err := mockprovider.New(os.Getenv("PROVIDER_API_KEY"), config.Env("MOCK_PROVIDER_DATA", "data/provider.json"), config.Env("MOCK_PROVIDER_MODE", "ok"), delay)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: config.Env("MOCK_PROVIDER_ADDR", ":8091"), Handler: mock.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func main() {
	if err := run(); err != nil {
		slog.Error("mock provider stopped", "error", err)
		os.Exit(1)
	}
}
