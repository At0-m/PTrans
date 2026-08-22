package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
)

type Repository interface {
	FetchDueOutboxEvents(ctx context.Context, limit int, now time.Time, lockFor time.Duration) ([]domain.OutboxEvent, error)
	ListSubscriptions(ctx context.Context, userID string) ([]domain.WebhookSubscription, error)
	ListDeliveredSubscriptionIDs(ctx context.Context, eventID string) (map[string]struct{}, error)
	RecordWebhookDelivery(ctx context.Context, delivery domain.WebhookDelivery) error
	MarkOutboxEventDelivered(ctx context.Context, eventID string, now time.Time) error
	ScheduleOutboxEventRetry(ctx context.Context, eventID string, nextAttemptAt time.Time, lastError string, now time.Time) error
	MarkOutboxEventDeadLetter(ctx context.Context, eventID string, lastError string, now time.Time) error
}

type Options struct {
	BatchSize      int
	PollInterval   time.Duration
	LockFor        time.Duration
	HTTPTimeout    time.Duration
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

type Worker struct {
	repo   Repository
	client *http.Client
	logger *slog.Logger
	opts   Options
}

type DeliveryResult struct {
	HTTPStatus int
	Duration   time.Duration
	Err        error
}

var deliveryIDSeq atomic.Uint64

func New(repo Repository, logger *slog.Logger, opts Options) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	opts = normalizeOptions(opts)
	return &Worker{
		repo: repo,
		client: &http.Client{
			Timeout: opts.HTTPTimeout,
		},
		logger: logger,
		opts:   opts,
	}
}

func normalizeOptions(opts Options) Options {
	if opts.BatchSize <= 0 {
		opts.BatchSize = 10
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 2 * time.Second
	}
	if opts.LockFor <= 0 {
		opts.LockFor = 30 * time.Second
	}
	if opts.HTTPTimeout <= 0 {
		opts.HTTPTimeout = 5 * time.Second
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 5
	}
	if opts.InitialBackoff <= 0 {
		opts.InitialBackoff = 2 * time.Second
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = time.Minute
	}
	return opts
}

func (w *Worker) Run(ctx context.Context) error {
	w.logger.Info("outbox worker started",
		"batch_size", w.opts.BatchSize,
		"poll_interval", w.opts.PollInterval.String(),
		"lock_for", w.opts.LockFor.String(),
		"http_timeout", w.opts.HTTPTimeout.String(),
		"max_attempts", w.opts.MaxAttempts,
	)

	ticker := time.NewTicker(w.opts.PollInterval)
	defer ticker.Stop()

	for {
		processed, err := w.ProcessOnce(ctx)
		if err != nil {
			w.logger.Error("worker iteration failed", "error", err)
		}
		if processed > 0 {
			continue
		}

		select {
		case <-ctx.Done():
			w.logger.Info("outbox worker stopped")
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *Worker) ProcessOnce(ctx context.Context) (int, error) {
	now := time.Now().UTC()
	events, err := w.repo.FetchDueOutboxEvents(ctx, w.opts.BatchSize, now, w.opts.LockFor)
	if err != nil {
		return 0, err
	}
	for _, event := range events {
		if err := w.processEvent(ctx, event); err != nil {
			w.logger.Error("process outbox event failed", "event_id", event.ID, "attempt", event.Attempts, "error", err)
		}
	}
	return len(events), nil
}

func (w *Worker) processEvent(ctx context.Context, event domain.OutboxEvent) error {
	subscriptions, err := w.repo.ListSubscriptions(ctx, event.UserID)
	if err != nil {
		return w.failEvent(ctx, event, fmt.Errorf("list active subscriptions: %w", err))
	}

	deliveredIDs, err := w.repo.ListDeliveredSubscriptionIDs(ctx, event.ID)
	if err != nil {
		return w.failEvent(ctx, event, fmt.Errorf("list delivered subscriptions: %w", err))
	}

	pendingTargets := make([]domain.WebhookSubscription, 0, len(subscriptions))
	for _, sub := range subscriptions {
		if !sub.Active {
			continue
		}
		if _, ok := deliveredIDs[sub.ID]; ok {
			continue
		}
		pendingTargets = append(pendingTargets, sub)
	}

	if len(pendingTargets) == 0 {
		if err := w.repo.MarkOutboxEventDelivered(ctx, event.ID, time.Now().UTC()); err != nil {
			return err
		}
		w.logger.Info("outbox event delivered", "event_id", event.ID, "event_type", event.EventType, "targets", 0)
		return nil
	}

	var failures []string
	for _, target := range pendingTargets {
		result := w.sendWebhook(ctx, event, target)
		delivery := domain.WebhookDelivery{
			ID:             nextDeliveryID(),
			EventID:        event.ID,
			SubscriptionID: target.ID,
			TargetURL:      target.URL,
			HTTPStatus:     result.HTTPStatus,
			Attempt:        event.Attempts,
			DurationMS:     result.Duration.Milliseconds(),
			CreatedAt:      time.Now().UTC(),
		}
		if result.Err != nil {
			delivery.Status = domain.WebhookDeliveryFailed
			delivery.Error = result.Err.Error()
			failures = append(failures, fmt.Sprintf("subscription=%s url=%s error=%s", target.ID, target.URL, result.Err.Error()))
		} else {
			delivery.Status = domain.WebhookDeliverySucceeded
		}

		if err := w.repo.RecordWebhookDelivery(ctx, delivery); err != nil {
			return fmt.Errorf("record webhook delivery: %w", err)
		}
	}

	if len(failures) == 0 {
		if err := w.repo.MarkOutboxEventDelivered(ctx, event.ID, time.Now().UTC()); err != nil {
			return err
		}
		w.logger.Info("outbox event delivered", "event_id", event.ID, "event_type", event.EventType, "targets", len(pendingTargets), "attempt", event.Attempts)
		return nil
	}

	return w.failEvent(ctx, event, errors.New(strings.Join(failures, "; ")))
}

func (w *Worker) sendWebhook(ctx context.Context, event domain.OutboxEvent, target domain.WebhookSubscription) DeliveryResult {
	envelope := domain.WebhookEnvelope{
		EventID:       event.ID,
		EventType:     event.EventType,
		AggregateType: event.AggregateType,
		AggregateID:   event.AggregateID,
		UserID:        event.UserID,
		Attempt:       event.Attempts,
		OccurredAt:    event.CreatedAt,
		Payload:       event.Payload,
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return DeliveryResult{Err: fmt.Errorf("marshal webhook envelope: %w", err)}
	}

	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.URL, bytes.NewReader(body))
	if err != nil {
		return DeliveryResult{Err: fmt.Errorf("create webhook request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "PTrans-Webhooks/1.0")
	req.Header.Set("X-PTrans-Event-ID", event.ID)
	req.Header.Set("X-PTrans-Event-Type", string(event.EventType))

	resp, err := w.client.Do(req)
	duration := time.Since(started)
	if err != nil {
		return DeliveryResult{Duration: duration, Err: err}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return DeliveryResult{HTTPStatus: resp.StatusCode, Duration: duration, Err: fmt.Errorf("unexpected webhook status %d", resp.StatusCode)}
	}
	return DeliveryResult{HTTPStatus: resp.StatusCode, Duration: duration}
}

func (w *Worker) failEvent(ctx context.Context, event domain.OutboxEvent, failure error) error {
	now := time.Now().UTC()
	message := failure.Error()
	if event.Attempts >= w.opts.MaxAttempts {
		if err := w.repo.MarkOutboxEventDeadLetter(ctx, event.ID, message, now); err != nil {
			return err
		}
		w.logger.Warn("outbox event moved to dead letter", "event_id", event.ID, "attempt", event.Attempts, "error", message)
		return nil
	}

	nextAttemptAt := now.Add(backoff(event.Attempts, w.opts.InitialBackoff, w.opts.MaxBackoff))
	if err := w.repo.ScheduleOutboxEventRetry(ctx, event.ID, nextAttemptAt, message, now); err != nil {
		return err
	}
	w.logger.Warn("outbox event scheduled for retry", "event_id", event.ID, "attempt", event.Attempts, "next_attempt_at", nextAttemptAt, "error", message)
	return nil
}

func backoff(attempt int, initial, max time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := initial
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= max {
			return max
		}
	}
	if delay > max {
		return max
	}
	return delay
}

func nextDeliveryID() string {
	return fmt.Sprintf("del_%d_%d", time.Now().UTC().UnixNano(), deliveryIDSeq.Add(1))
}
