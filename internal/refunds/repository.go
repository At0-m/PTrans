package refunds

import (
	"context"
	"github.com/At0-m/PTrans/internal/domain"
	"time"
)

type ClaimKind string

const (
	ClaimSubmit    ClaimKind = "submit"
	ClaimReconcile ClaimKind = "reconcile"
)

type Outcome struct {
	Status         domain.RefundStatus
	Retryable      bool
	ManualReview   bool
	LastError      string
	CheckFailures  int
	NextActionAt   time.Time
	Reconciliation *domain.ReconciliationResult
}

type WorkerRepository interface {
	ClaimRefund(context.Context, ClaimKind, time.Time, time.Duration, int) (domain.Refund, bool, error)
	ApplyRefundOutcome(context.Context, domain.Refund, Outcome, time.Time) error
}
