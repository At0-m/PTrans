package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
)

type fakeOutboxRepo struct {
	events        []domain.OutboxEvent
	subscriptions []domain.WebhookSubscription
	deliveries    []domain.WebhookDelivery
	delivered     []string
	retried       []string
	deadLetters   []string
}

func (r *fakeOutboxRepo) FetchDueOutboxEvents(_ context.Context, _ int, _ time.Time, _ time.Duration) ([]domain.OutboxEvent, error) {
	events := r.events
	r.events = nil
	return events, nil
}

func (r *fakeOutboxRepo) ListSubscriptions(_ context.Context, _ string) ([]domain.WebhookSubscription, error) {
	return r.subscriptions, nil
}

func (r *fakeOutboxRepo) ListDeliveredSubscriptionIDs(_ context.Context, _ string) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}

func (r *fakeOutboxRepo) RecordWebhookDelivery(_ context.Context, delivery domain.WebhookDelivery) error {
	r.deliveries = append(r.deliveries, delivery)
	return nil
}

func (r *fakeOutboxRepo) MarkOutboxEventDelivered(_ context.Context, eventID string, _ time.Time) error {
	r.delivered = append(r.delivered, eventID)
	return nil
}

func (r *fakeOutboxRepo) ScheduleOutboxEventRetry(_ context.Context, eventID string, _ time.Time, _ string, _ time.Time) error {
	r.retried = append(r.retried, eventID)
	return nil
}

func (r *fakeOutboxRepo) MarkOutboxEventDeadLetter(_ context.Context, eventID string, _ string, _ time.Time) error {
	r.deadLetters = append(r.deadLetters, eventID)
	return nil
}

func TestWorkerDeliversWebhook(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if got := r.Header.Get("X-PTrans-Event-ID"); got != "evt_1" {
			t.Fatalf("unexpected event id header: %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	payload, err := json.Marshal(domain.PaymentCreatedPayload{ID: "pay_1", Amount: 100, Currency: "RUB", Status: domain.PaymentPending, CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	repo := &fakeOutboxRepo{
		events: []domain.OutboxEvent{{
			ID:            "evt_1",
			UserID:        "alice",
			EventType:     domain.EventPaymentCreated,
			AggregateType: domain.AggregateTypePayment,
			AggregateID:   "pay_1",
			Payload:       payload,
			Status:        domain.OutboxPending,
			Attempts:      1,
			CreatedAt:     time.Now().UTC(),
		}},
		subscriptions: []domain.WebhookSubscription{{ID: "sub_1", URL: server.URL, Active: true}},
	}

	worker := New(repo, nil, Options{HTTPTimeout: time.Second})
	processed, err := worker.ProcessOnce(context.Background())
	if err != nil {
		t.Fatalf("process once: %v", err)
	}
	if processed != 1 {
		t.Fatalf("expected one processed event, got %d", processed)
	}
	if len(repo.delivered) != 1 || repo.delivered[0] != "evt_1" {
		t.Fatalf("expected event to be delivered, got %v", repo.delivered)
	}
	if len(repo.deliveries) != 1 || repo.deliveries[0].Status != domain.WebhookDeliverySucceeded {
		t.Fatalf("expected one successful delivery, got %+v", repo.deliveries)
	}
}

func TestWorkerRetriesFailedWebhook(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	repo := &fakeOutboxRepo{
		events: []domain.OutboxEvent{{
			ID:            "evt_2",
			UserID:        "alice",
			EventType:     domain.EventPaymentCreated,
			AggregateType: domain.AggregateTypePayment,
			AggregateID:   "pay_2",
			Payload:       json.RawMessage(`{"id":"pay_2"}`),
			Status:        domain.OutboxPending,
			Attempts:      1,
			CreatedAt:     time.Now().UTC(),
		}},
		subscriptions: []domain.WebhookSubscription{{ID: "sub_1", URL: server.URL, Active: true}},
	}

	worker := New(repo, nil, Options{HTTPTimeout: time.Second, MaxAttempts: 3})
	processed, err := worker.ProcessOnce(context.Background())
	if err != nil {
		t.Fatalf("process once: %v", err)
	}
	if processed != 1 {
		t.Fatalf("expected one processed event, got %d", processed)
	}
	if len(repo.retried) != 1 || repo.retried[0] != "evt_2" {
		t.Fatalf("expected retry, got %v", repo.retried)
	}
	if len(repo.deliveries) != 1 || repo.deliveries[0].Status != domain.WebhookDeliveryFailed {
		t.Fatalf("expected one failed delivery, got %+v", repo.deliveries)
	}
}
