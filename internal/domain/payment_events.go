package domain

import "time"

type PaymentCreatedPayload struct {
	ID             string
	Amount         int64
	Currency       string
	IdempotencyKey string
	Status         PaymentStatus
	CreatedAt      time.Time
}

type PaymentCancelledPayload struct {
	ID          string
	CancelledAt time.Time
}
