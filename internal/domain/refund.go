package domain

import (
	"errors"
	"fmt"
	"time"
)

type RefundStatus string

const (
	RefundPending    RefundStatus = "PENDING"
	RefundProcessing RefundStatus = "PROCESSING"
	RefundSucceeded  RefundStatus = "SUCCEEDED"
	RefundFailed     RefundStatus = "FAILED"
)

var (
	ErrRefundNotFound         = errors.New("refund not found")
	ErrInvalidRefundState     = errors.New("invalid refund state")
	ErrRefundAmountExceeded   = errors.New("refund amount exceeds available payment amount")
	ErrIdempotencyKeyRequired = errors.New("Idempotency-Key is required and must be at most 128 characters")
	ErrLeaseLost              = errors.New("refund lease lost")
)

func ValidateRefundTransition(from, to RefundStatus) error {
	if from == RefundPending && to == RefundProcessing ||
		from == RefundProcessing && (to == RefundSucceeded || to == RefundFailed) ||
		from == RefundFailed && to == RefundProcessing {
		return nil
	}
	return fmt.Errorf("%w: %s -> %s", ErrInvalidRefundState, from, to)
}

func IsRefundStatus(status RefundStatus) bool {
	return status == RefundPending || status == RefundProcessing || status == RefundSucceeded || status == RefundFailed
}

type Refund struct {
	ID                  string       `json:"id"`
	UserID              string       `json:"user_id"`
	PaymentID           string       `json:"payment_id"`
	Amount              int64        `json:"amount"`
	Currency            string       `json:"currency"`
	Status              RefundStatus `json:"status"`
	IdempotencyKey      string       `json:"idempotency_key"`
	RequestHash         string       `json:"-"`
	Attempts            int          `json:"attempts"`
	CheckFailures       int          `json:"check_failures"`
	Retryable           bool         `json:"retryable"`
	ManualReview        bool         `json:"manual_review"`
	LastError           string       `json:"last_error,omitempty"`
	NextActionAt        time.Time    `json:"next_action_at"`
	ProcessingStartedAt *time.Time   `json:"processing_started_at,omitempty"`
	LeaseToken          string       `json:"-"`
	LeaseUntil          *time.Time   `json:"-"`
	Version             int64        `json:"version"`
	CreatedAt           time.Time    `json:"created_at"`
	UpdatedAt           time.Time    `json:"updated_at"`
}

type ReconciliationResult struct {
	ID             string       `json:"id"`
	OperationID    string       `json:"operation_id"`
	LocalStatus    RefundStatus `json:"local_status"`
	ExternalStatus string       `json:"external_status"`
	Result         string       `json:"result"`
	Detail         string       `json:"detail,omitempty"`
	CheckedAt      time.Time    `json:"checked_at"`
}
