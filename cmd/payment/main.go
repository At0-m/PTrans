package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/At0-m/PTrans/internal/httpapi"
	"github.com/At0-m/PTrans/internal/ratelimit"
	"github.com/At0-m/PTrans/internal/service"
	postgresstore "github.com/At0-m/PTrans/internal/storage"
)

type limiterAdapter struct {
	inner *ratelimit.Limiter
}

func (a limiterAdapter) Allow(ctx context.Context, scopeKey string, now time.Time) (httpapi.RateLimitDecision, error) {
	decision, err := a.inner.Allow(ctx, scopeKey, now)
	if err != nil {
		return httpapi.RateLimitDecision{}, err
	}
	return httpapi.RateLimitDecision{
		Allowed:    decision.Allowed,
		Remaining:  decision.Remaining,
		RetryAfter: decision.RetryAfter,
	}, nil
}

func (a limiterAdapter) Limit() int {
	return a.inner.Limit()
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	listenAddr := envOrDefault("LISTEN_ADDR", ":8080")
	databaseURL := envOrDefault("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/ptrans?sslmode=disable")
	rateLimitPerMinute := envOrDefaultInt("RATE_LIMIT_MUTATIONS_PER_MINUTE", 20)

	store, err := postgresstore.New(ctx, databaseURL)
	if err != nil {
		logger.Error("create postgres store", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	paymentService := service.NewPaymentService(store)
	webhookService := service.NewWebhookService(store)

	var limiter interface {
		Allow(ctx context.Context, scopeKey string, now time.Time) (httpapi.RateLimitDecision, error)
		Limit() int
	}
	if rateLimitPerMinute > 0 {
		limiter = limiterAdapter{inner: ratelimit.New(store.Pool(), rateLimitPerMinute, time.Minute)}
	}

	server := &http.Server{
		Addr: listenAddr,
		Handler: httpapi.NewRouter(
			paymentService,
			webhookService,
			limiter,
			httpapi.WithHealthChecker(store),
			httpapi.WithLogger(logger),
		),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown server", "error", err)
		}
	}()

	logger.Info("payment api listening",
		"addr", listenAddr,
		"postgres", maskDatabaseURL(databaseURL),
		"rate_limit_per_minute", rateLimitPerMinute,
	)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("listen and serve", "error", err)
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

func maskDatabaseURL(raw string) string {
	if raw == "" {
		return ""
	}
	return "configured"
}