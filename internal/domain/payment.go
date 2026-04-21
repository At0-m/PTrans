package domain

import "time"

type PaymentStatus string

const (
	PaymentPending    PaymentStatus = "PENDING"
	PaymentProcessing PaymentStatus = "PROCESSING"
	PaymentProcesing  PaymentStatus = PaymentProcessing
	PaymentSucceeded  PaymentStatus = "SUCCEEDED"
	PaymentFailed     PaymentStatus = "FAILED"
	PaymentCancelled  PaymentStatus = "CANCELLED"
)

type Payment struct {
	ID             string        `json:"id"`
	Amount         int64         `json:"amount"`
	Currency       string        `json:"currency"`
	Status         PaymentStatus `json:"status"`
	IdempotencyKey string        `json:"idempotency_key,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}