package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/At0-m/PTrans/internal/auth"
	"github.com/At0-m/PTrans/internal/refunds"
)

func Env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func JWT() auth.Config {
	return auth.Config{Secret: os.Getenv("JWT_SECRET"), Issuer: Env("JWT_ISSUER", "ptrans"), Audience: Env("JWT_AUDIENCE", "ptrans-api")}
}

func RefundOptions() (refunds.Options, error) {
	opts := refunds.DefaultOptions()
	durations := map[string]*time.Duration{
		"REFUND_POLL_INTERVAL": &opts.PollInterval, "REFUND_LEASE_DURATION": &opts.LeaseDuration,
		"PROVIDER_HTTP_TIMEOUT": &opts.RequestTimeout, "REFUND_CHECK_INTERVAL": &opts.CheckInterval,
		"REFUND_INITIAL_BACKOFF": &opts.InitialBackoff, "REFUND_MAX_BACKOFF": &opts.MaxBackoff, "REFUND_STUCK_AFTER": &opts.StuckAfter,
	}
	for name, target := range durations {
		if value := os.Getenv(name); value != "" {
			parsed, err := time.ParseDuration(value)
			if err != nil {
				return opts, fmt.Errorf("%s: %w", name, err)
			}
			*target = parsed
		}
	}
	ints := map[string]*int{"REFUND_MAX_ATTEMPTS": &opts.MaxAttempts, "REFUND_MAX_CHECK_FAILURES": &opts.MaxCheckFailures, "REFUND_BATCH_SIZE": &opts.BatchSize}
	for name, target := range ints {
		if value := os.Getenv(name); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return opts, fmt.Errorf("%s: %w", name, err)
			}
			*target = parsed
		}
	}
	return opts, opts.Validate()
}
