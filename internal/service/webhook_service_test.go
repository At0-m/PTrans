package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
)

type fakeWebhookRepo struct {
	items map[string]map[string]domain.WebhookSubscription
}

func newFakeWebhookRepo() *fakeWebhookRepo {
	return &fakeWebhookRepo{items: make(map[string]map[string]domain.WebhookSubscription)}
}

func (r *fakeWebhookRepo) CreateSubscription(_ context.Context, userID string, subscription domain.WebhookSubscription) (domain.WebhookSubscription, error) {
	if _, ok := r.items[userID]; !ok {
		r.items[userID] = map[string]domain.WebhookSubscription{}
	}
	r.items[userID][subscription.ID] = subscription
	return subscription, nil
}

func (r *fakeWebhookRepo) ListSubscriptions(_ context.Context, userID string) ([]domain.WebhookSubscription, error) {
	items := make([]domain.WebhookSubscription, 0)
	for _, item := range r.items[userID] {
		items = append(items, item)
	}
	return items, nil
}

func (r *fakeWebhookRepo) GetSubscription(_ context.Context, userID, id string) (domain.WebhookSubscription, error) {
	item, ok := r.items[userID][id]
	if !ok {
		return domain.WebhookSubscription{}, domain.ErrSubscriptionNotFound
	}
	return item, nil
}

func (r *fakeWebhookRepo) SetSubscriptionActive(_ context.Context, userID, id string, active bool, updatedAt time.Time) error {
	item, ok := r.items[userID][id]
	if !ok {
		return domain.ErrSubscriptionNotFound
	}
	item.Active = active
	item.UpdatedAt = updatedAt
	r.items[userID][id] = item
	return nil
}

func TestWebhookServiceCreateToggle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newFakeWebhookRepo()
	service := NewWebhookService(repo)

	subscription, err := service.CreateSubscription(ctx, CreateWebhookSubscriptionInput{UserID: "alice", URL: "http://localhost:8090/hook", Active: true})
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	if !subscription.Active {
		t.Fatalf("expected subscription to be active")
	}
	if err := service.SetSubscriptionActive(ctx, "alice", subscription.ID, false); err != nil {
		t.Fatalf("deactivate subscription: %v", err)
	}
	updated, err := service.GetSubscription(ctx, "alice", subscription.ID)
	if err != nil {
		t.Fatalf("get subscription: %v", err)
	}
	if updated.Active {
		t.Fatalf("expected subscription to be inactive")
	}
	list, err := service.ListSubscriptions(ctx, "alice")
	if err != nil {
		t.Fatalf("list subscriptions: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly one subscription, got %d", len(list))
	}
}

func TestWebhookServiceValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service := NewWebhookService(newFakeWebhookRepo())

	_, err := service.CreateSubscription(ctx, CreateWebhookSubscriptionInput{UserID: "alice", URL: "localhost:8090/hook", Active: true})
	if !errors.Is(err, domain.ErrInvalidWebhookURL) {
		t.Fatalf("expected invalid webhook url error, got %v", err)
	}
	_, err = service.CreateSubscription(ctx, CreateWebhookSubscriptionInput{URL: "http://localhost:8090/hook", Active: true})
	if !errors.Is(err, domain.ErrUserIDRequired) {
		t.Fatalf("expected missing user error, got %v", err)
	}
}