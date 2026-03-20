package domain

import "time"

type PaymentStatus string

const (
	PaymentPending   PaymentStatus = "PENDING"
	PaymentProcesing PaymentStatus = "PROCESING"
	PaymentSucceeded PaymentStatus = "SUCCEEDED"
	PaymentFailed    PaymentStatus = "FAILED"
	PaymentCancelled PaymentStatus = "CANCELLED"
)

type Payment struct {
	ID             string
	Amount         int64
	Currency       string
	Status         PaymentStatus
	IdempotencyKey string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
