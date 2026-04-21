package domain

import "time"

type PaymentCreatedPayload struct {
	ID             string        `json:"id"`
	Amount         int64         `json:"amount"`
	Currency       string        `json:"currency"`
	IdempotencyKey string        `json:"idempotency_key"`
	Status         PaymentStatus `json:"status"`
	CreatedAt      time.Time     `json:"created_at"`
}

type PaymentCancelledPayload struct {
	ID          string    `json:"id"`
	CancelledAt time.Time `json:"cancelled_at"`
}