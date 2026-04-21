package service

import (
	"context"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
)

type PaymentListFilter struct {
	Status string
	Page   int
	Size   int
}

type PaymentRepository interface {
	CreatePayment(ctx context.Context, userID string, payment domain.Payment, requestHash string) (domain.Payment, bool, error)
	GetPayment(ctx context.Context, userID, id string) (domain.Payment, error)
	ListPayments(ctx context.Context, userID string, filter PaymentListFilter) ([]domain.Payment, int, error)
	CancelPayment(ctx context.Context, userID, id string, cancelledAt time.Time) error
}

type WebhookRepository interface {
	CreateSubscription(ctx context.Context, userID string, subscription domain.WebhookSubscription) (domain.WebhookSubscription, error)
	ListSubscriptions(ctx context.Context, userID string) ([]domain.WebhookSubscription, error)
	GetSubscription(ctx context.Context, userID, id string) (domain.WebhookSubscription, error)
	SetSubscriptionActive(ctx context.Context, userID, id string, active bool, updatedAt time.Time) error
}