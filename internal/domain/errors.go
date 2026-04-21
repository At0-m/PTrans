package domain

import "errors"

var (
	ErrPaymentNotFound      = errors.New("payment not found")
	ErrSubscriptionNotFound = errors.New("subscription not found")
	ErrSubsctiptionNotFound = ErrSubscriptionNotFound
	ErrInvalidPaymentState  = errors.New("invalid payment state")
	ErrInvalidStatusFilter  = errors.New("invalid status filter")
	ErrInvalidPagination    = errors.New("invalid pagination")
	ErrInvalidAmount        = errors.New("invalid amount")
	ErrInvalidCurrency      = errors.New("invalid currency")
	ErrInvalidWebhookURL    = errors.New("invalid webhook url")
	ErrIdempotencyConflict  = errors.New("idempotency key already used with different payload")
	ErrUserIDRequired       = errors.New("header X-User-ID is required")
)