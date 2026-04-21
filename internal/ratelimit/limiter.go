package ratelimit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Decision struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration
}

type Limiter struct {
	pool     *pgxpool.Pool
	limit    int
	interval time.Duration
}

func New(pool *pgxpool.Pool, limit int, interval time.Duration) *Limiter {
	return &Limiter{pool: pool, limit: limit, interval: interval}
}

func (l *Limiter) Limit() int {
	if l == nil {
		return 0
	}
	return l.limit
}

func (l *Limiter) Allow(ctx context.Context, scopeKey string, now time.Time) (Decision, error) {
	if l == nil || l.pool == nil || l.limit <= 0 || l.interval <= 0 {
		return Decision{Allowed: true}, nil
	}

	scopeKey = strings.TrimSpace(scopeKey)
	if scopeKey == "" {
		return Decision{}, fmt.Errorf("rate limit scope key is required")
	}

	windowStart := now.UTC().Truncate(l.interval)
	var hits int
	if err := l.pool.QueryRow(ctx, `
INSERT INTO rate_limit_windows (scope_key, window_start, hits, created_at, updated_at)
VALUES ($1, $2, 1, $3, $3)
ON CONFLICT (scope_key, window_start)
DO UPDATE SET hits = rate_limit_windows.hits + 1, updated_at = EXCLUDED.updated_at
RETURNING hits
`, scopeKey, windowStart, now.UTC()).Scan(&hits); err != nil {
		return Decision{}, fmt.Errorf("upsert rate limit window: %w", err)
	}

	remaining := l.limit - hits
	if remaining < 0 {
		remaining = 0
	}

	decision := Decision{Allowed: hits <= l.limit, Remaining: remaining}
	if !decision.Allowed {
		decision.RetryAfter = windowStart.Add(l.interval).Sub(now.UTC())
	}
	return decision, nil
}