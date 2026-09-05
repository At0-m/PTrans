package refunds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
	"github.com/At0-m/PTrans/internal/provider"
)

type Provider interface {
	Submit(context.Context, domain.Refund) (provider.Result, error)
	Lookup(context.Context, string) (provider.Result, error)
}

type Options struct {
	PollInterval     time.Duration
	LeaseDuration    time.Duration
	RequestTimeout   time.Duration
	CheckInterval    time.Duration
	InitialBackoff   time.Duration
	MaxBackoff       time.Duration
	StuckAfter       time.Duration
	MaxAttempts      int
	MaxCheckFailures int
	BatchSize        int
}

func DefaultOptions() Options {
	return Options{
		PollInterval: time.Second, LeaseDuration: 30 * time.Second, RequestTimeout: 5 * time.Second,
		CheckInterval: 30 * time.Second, InitialBackoff: 2 * time.Second, MaxBackoff: time.Minute,
		StuckAfter: 10 * time.Minute, MaxAttempts: 5, MaxCheckFailures: 5, BatchSize: 10,
	}
}

func (o Options) Validate() error {
	if o.PollInterval <= 0 || o.RequestTimeout <= 0 || o.LeaseDuration < 2*o.RequestTimeout ||
		o.CheckInterval <= 0 || o.InitialBackoff <= 0 || o.MaxBackoff < o.InitialBackoff ||
		o.StuckAfter <= 0 || o.MaxAttempts < 1 || o.MaxCheckFailures < 1 || o.BatchSize < 1 {
		return errors.New("invalid refund worker options; lease must be at least twice the request timeout")
	}
	return nil
}

type Worker struct {
	repo     WorkerRepository
	provider Provider
	opts     Options
	logger   *slog.Logger
	now      func() time.Time
	jitter   func(time.Duration) time.Duration
}

func NewWorker(repo WorkerRepository, p Provider, logger *slog.Logger, opts Options) (*Worker, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{repo: repo, provider: p, opts: opts, logger: logger, now: func() time.Time { return time.Now().UTC() },
		jitter: func(d time.Duration) time.Duration {
			half := d / 2
			return half + time.Duration(rand.Int64N(int64(d-half)+1))
		},
	}, nil
}

func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.opts.PollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		for i := 0; i < w.opts.BatchSize; i++ {
			reconciled, re := w.ReconcileOnce(ctx)
			processed, pe := w.ProcessOnce(ctx)
			if re != nil {
				w.logger.Error("refund reconciliation failed", "error", re)
			}
			if pe != nil {
				w.logger.Error("refund processing failed", "error", pe)
			}
			if !reconciled && !processed || ctx.Err() != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *Worker) ProcessOnce(ctx context.Context) (bool, error) {
	r, ok, err := w.repo.ClaimRefund(ctx, ClaimSubmit, w.now(), w.opts.LeaseDuration, w.opts.MaxAttempts)
	if err != nil || !ok {
		return ok, err
	}
	callCtx, cancel := context.WithTimeout(ctx, w.opts.RequestTimeout)
	result, callErr := w.provider.Submit(callCtx, r)
	cancel()
	now := w.now()
	out := w.decide(r, result, callErr, now, false)
	return true, w.save(ctx, r, out, now)
}

func (w *Worker) ReconcileOnce(ctx context.Context) (bool, error) {
	r, ok, err := w.repo.ClaimRefund(ctx, ClaimReconcile, w.now(), w.opts.LeaseDuration, w.opts.MaxAttempts)
	if err != nil || !ok {
		return ok, err
	}
	callCtx, cancel := context.WithTimeout(ctx, w.opts.RequestTimeout)
	result, callErr := w.provider.Lookup(callCtx, r.ID)
	cancel()
	now := w.now()
	out := w.decide(r, result, callErr, now, true)
	external := string(result.Status)
	if callErr != nil {
		external = "ERROR"
	}
	action := "matched"
	switch {
	case out.ManualReview:
		action = "manual_review"
	case out.Status == domain.RefundFailed && out.Retryable:
		action = "retry_scheduled"
	case out.Status != r.Status:
		action = "corrected"
	case callErr != nil:
		action = "provider_error"
	case result.Status == provider.Unknown:
		action = "unknown"
	}
	out.Reconciliation = &domain.ReconciliationResult{ExternalStatus: external, Result: action, Detail: out.LastError}
	return true, w.save(ctx, r, out, now)
}

func (w *Worker) decide(r domain.Refund, result provider.Result, err error, now time.Time, lookup bool) Outcome {
	out := Outcome{Status: domain.RefundProcessing, NextActionAt: now.Add(w.opts.CheckInterval)}
	if err != nil {
		out.LastError = err.Error()
		out.CheckFailures = r.CheckFailures
		var pe *provider.Error
		if errors.As(err, &pe) {
			if pe.Permanent && !lookup {
				out.Status = domain.RefundFailed
				return out
			}
			if pe.Configuration {
				out.ManualReview = true
				return out
			}
		}
		if lookup {
			out.CheckFailures++
		}
		delay := w.backoff(max(r.Attempts, out.CheckFailures))
		if pe != nil && pe.RetryAfter > delay {
			delay = pe.RetryAfter
		}
		out.NextActionAt = now.Add(delay)
		if out.CheckFailures >= w.opts.MaxCheckFailures {
			out.ManualReview = true
		}
		return out
	}
	if result.OperationID != r.ID {
		out.LastError = "provider operation ID mismatch"
		out.ManualReview = true
		return out
	}
	switch result.Status {
	case provider.Succeeded:
		out.Status = domain.RefundSucceeded
	case provider.Failed:
		out.Status = domain.RefundFailed
		out.Retryable = result.Retryable && r.Attempts < w.opts.MaxAttempts
		out.LastError = result.ErrorCode
		out.NextActionAt = now.Add(w.backoff(r.Attempts))
		if result.Retryable && !out.Retryable {
			out.LastError = "retry attempts exhausted: " + result.ErrorCode
		}
	case provider.NotFound:
		if !lookup {
			out.LastError = "invalid NOT_FOUND response to submit"
			out.ManualReview = true
			return out
		}
		if r.Attempts >= w.opts.MaxAttempts {
			out.ManualReview = true
			out.LastError = "operation not found after retry limit; verify no delayed request can still complete"
			return out
		}
		out.Status = domain.RefundFailed
		out.Retryable = true
		out.NextActionAt = now.Add(w.backoff(r.Attempts))
		out.LastError = "provider confirmed operation not found; retry with the same operation ID"
	case provider.Processing:
		if r.ProcessingStartedAt != nil && now.Sub(*r.ProcessingStartedAt) >= w.opts.StuckAfter {
			out.ManualReview = true
			out.LastError = "provider operation is stuck in PROCESSING"
		}
	case provider.Unknown:
		out.CheckFailures = r.CheckFailures + 1
		out.LastError = "provider cannot determine operation status"
		out.NextActionAt = now.Add(w.backoff(out.CheckFailures))
		if out.CheckFailures >= w.opts.MaxCheckFailures || r.ProcessingStartedAt != nil && now.Sub(*r.ProcessingStartedAt) >= w.opts.StuckAfter {
			out.ManualReview = true
		}
	default:
		out.ManualReview = true
		out.LastError = "unsupported provider status"
	}
	return out
}

func (w *Worker) save(ctx context.Context, r domain.Refund, out Outcome, now time.Time) error {
	if err := w.repo.ApplyRefundOutcome(ctx, r, out, now); err != nil {
		return fmt.Errorf("save refund %s: %w", r.ID, err)
	}
	if out.ManualReview {
		w.logger.Error("refund requires manual review", "refund_id", r.ID, "reason", out.LastError)
	} else {
		w.logger.Info("refund processed", "refund_id", r.ID, "status", out.Status, "attempt", r.Attempts, "reconciliation", out.Reconciliation != nil)
	}
	return nil
}

func (w *Worker) backoff(attempt int) time.Duration {
	delay := w.opts.InitialBackoff
	for i := 1; i < attempt && delay < w.opts.MaxBackoff; i++ {
		if delay > w.opts.MaxBackoff/2 {
			delay = w.opts.MaxBackoff
			break
		}
		delay *= 2
	}
	if delay > w.opts.MaxBackoff {
		delay = w.opts.MaxBackoff
	}
	return w.jitter(delay)
}
