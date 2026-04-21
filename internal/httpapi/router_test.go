package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
	"github.com/At0-m/PTrans/internal/service"
)

type stubRepo struct {
	payments      map[string]map[string]domain.Payment
	subscriptions map[string]map[string]domain.WebhookSubscription
	idempotency   map[string]map[string]string
}

func newStubRepo() *stubRepo {
	return &stubRepo{payments: make(map[string]map[string]domain.Payment), subscriptions: make(map[string]map[string]domain.WebhookSubscription), idempotency: make(map[string]map[string]string)}
}

func (r *stubRepo) CreatePayment(_ context.Context, userID string, payment domain.Payment, _ string) (domain.Payment, bool, error) {
	if _, ok := r.payments[userID]; !ok {
		r.payments[userID] = map[string]domain.Payment{}
	}
	if _, ok := r.idempotency[userID]; !ok {
		r.idempotency[userID] = map[string]string{}
	}
	if payment.IdempotencyKey != "" {
		if paymentID, ok := r.idempotency[userID][payment.IdempotencyKey]; ok {
			existing := r.payments[userID][paymentID]
			if existing.Amount == payment.Amount && existing.Currency == payment.Currency {
				return existing, false, nil
			}
			return domain.Payment{}, false, domain.ErrIdempotencyConflict
		}
		r.idempotency[userID][payment.IdempotencyKey] = payment.ID
	}
	r.payments[userID][payment.ID] = payment
	return payment, true, nil
}

func (r *stubRepo) GetPayment(_ context.Context, userID, id string) (domain.Payment, error) {
	payment, ok := r.payments[userID][id]
	if !ok {
		return domain.Payment{}, domain.ErrPaymentNotFound
	}
	return payment, nil
}

func (r *stubRepo) ListPayments(_ context.Context, userID string, filter service.PaymentListFilter) ([]domain.Payment, int, error) {
	items := make([]domain.Payment, 0)
	for _, payment := range r.payments[userID] {
		if filter.Status == "" || string(payment.Status) == filter.Status {
			items = append(items, payment)
		}
	}
	return items, len(items), nil
}

func (r *stubRepo) CancelPayment(_ context.Context, userID, id string, cancelledAt time.Time) error {
	payment, ok := r.payments[userID][id]
	if !ok {
		return domain.ErrPaymentNotFound
	}
	if payment.Status != domain.PaymentPending {
		return domain.ErrInvalidPaymentState
	}
	payment.Status = domain.PaymentCancelled
	payment.UpdatedAt = cancelledAt
	r.payments[userID][id] = payment
	return nil
}

func (r *stubRepo) CreateSubscription(_ context.Context, userID string, subscription domain.WebhookSubscription) (domain.WebhookSubscription, error) {
	if _, ok := r.subscriptions[userID]; !ok {
		r.subscriptions[userID] = map[string]domain.WebhookSubscription{}
	}
	r.subscriptions[userID][subscription.ID] = subscription
	return subscription, nil
}

func (r *stubRepo) ListSubscriptions(_ context.Context, userID string) ([]domain.WebhookSubscription, error) {
	items := make([]domain.WebhookSubscription, 0)
	for _, item := range r.subscriptions[userID] {
		items = append(items, item)
	}
	return items, nil
}

func (r *stubRepo) GetSubscription(_ context.Context, userID, id string) (domain.WebhookSubscription, error) {
	item, ok := r.subscriptions[userID][id]
	if !ok {
		return domain.WebhookSubscription{}, domain.ErrSubscriptionNotFound
	}
	return item, nil
}

func (r *stubRepo) SetSubscriptionActive(_ context.Context, userID, id string, active bool, updatedAt time.Time) error {
	item, ok := r.subscriptions[userID][id]
	if !ok {
		return domain.ErrSubscriptionNotFound
	}
	item.Active = active
	item.UpdatedAt = updatedAt
	r.subscriptions[userID][id] = item
	return nil
}

func TestRouterCreatePaymentAndList(t *testing.T) {
	t.Parallel()
	repo := newStubRepo()
	handler := NewRouter(service.NewPaymentService(repo), service.NewWebhookService(repo), nil)

	body, err := json.Marshal(map[string]any{"amount": 999, "currency": "rub"})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/payments", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "checkout-1")
	request.Header.Set("X-User-ID", "alice")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201 on create payment, got %d: %s", response.Code, response.Body.String())
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/v1/payments?page=1&size=10", nil)
	listRequest.Header.Set("X-User-ID", "alice")
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("expected 200 on list payments, got %d: %s", listResponse.Code, listResponse.Body.String())
	}

	var payload struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if payload.Total != 1 || len(payload.Items) != 1 {
		t.Fatalf("expected one payment in list, got total=%d items=%d", payload.Total, len(payload.Items))
	}
}

func TestRouterRequiresUserHeader(t *testing.T) {
	t.Parallel()
	repo := newStubRepo()
	handler := NewRouter(service.NewPaymentService(repo), service.NewWebhookService(repo), nil)
	request := httptest.NewRequest(http.MethodGet, "/v1/payments?page=1&size=10", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing user header, got %d", response.Code)
	}

	var payload map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if payload["error"] != domain.ErrUserIDRequired.Error() {
		t.Fatalf("expected missing user error, got %v", payload)
	}
}

func TestRouterRejectsInvalidPagination(t *testing.T) {
	t.Parallel()
	repo := newStubRepo()
	handler := NewRouter(service.NewPaymentService(repo), service.NewWebhookService(repo), nil)

	request := httptest.NewRequest(http.MethodGet, "/v1/payments?page=0&size=10", nil)
	request.Header.Set("X-User-ID", "alice")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid pagination, got %d: %s", response.Code, response.Body.String())
	}
}