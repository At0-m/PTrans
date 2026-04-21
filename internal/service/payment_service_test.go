package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
)

type fakePaymentRepo struct {
	payments    map[string]map[string]domain.Payment
	idempotency map[string]map[string]struct {
		RequestHash string
		PaymentID   string
	}
}

func newFakePaymentRepo() *fakePaymentRepo {
	return &fakePaymentRepo{payments: make(map[string]map[string]domain.Payment), idempotency: make(map[string]map[string]struct {
		RequestHash string
		PaymentID   string
	})}
}

func (r *fakePaymentRepo) CreatePayment(_ context.Context, userID string, payment domain.Payment, requestHash string) (domain.Payment, bool, error) {
	if _, ok := r.payments[userID]; !ok {
		r.payments[userID] = map[string]domain.Payment{}
	}
	if _, ok := r.idempotency[userID]; !ok {
		r.idempotency[userID] = map[string]struct{ RequestHash, PaymentID string }{}
	}
	if payment.IdempotencyKey != "" {
		if entry, ok := r.idempotency[userID][payment.IdempotencyKey]; ok {
			if entry.RequestHash != requestHash {
				return domain.Payment{}, false, domain.ErrIdempotencyConflict
			}
			return r.payments[userID][entry.PaymentID], false, nil
		}
		r.idempotency[userID][payment.IdempotencyKey] = struct{ RequestHash, PaymentID string }{requestHash, payment.ID}
	}
	r.payments[userID][payment.ID] = payment
	return payment, true, nil
}

func (r *fakePaymentRepo) GetPayment(_ context.Context, userID, id string) (domain.Payment, error) {
	payment, ok := r.payments[userID][id]
	if !ok {
		return domain.Payment{}, domain.ErrPaymentNotFound
	}
	return payment, nil
}

func (r *fakePaymentRepo) ListPayments(_ context.Context, userID string, filter PaymentListFilter) ([]domain.Payment, int, error) {
	items := make([]domain.Payment, 0)
	for _, payment := range r.payments[userID] {
		if filter.Status == "" || string(payment.Status) == filter.Status {
			items = append(items, payment)
		}
	}
	return items, len(items), nil
}

func (r *fakePaymentRepo) CancelPayment(_ context.Context, userID, id string, cancelledAt time.Time) error {
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

func TestPaymentServiceCreateIdempotencyAndCancel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newFakePaymentRepo()
	service := NewPaymentService(repo)

	created, wasCreated, err := service.CreatePayment(ctx, CreatePaymentInput{UserID: "alice", Amount: 1500, Currency: "rub", IdempotencyKey: "order-1"})
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}
	if !wasCreated || created.Status != domain.PaymentPending || created.Currency != "RUB" {
		t.Fatalf("unexpected create result: created=%v payment=%+v", wasCreated, created)
	}

	duplicate, wasCreated, err := service.CreatePayment(ctx, CreatePaymentInput{UserID: "alice", Amount: 1500, Currency: "RUB", IdempotencyKey: "order-1"})
	if err != nil {
		t.Fatalf("repeat create payment: %v", err)
	}
	if wasCreated || duplicate.ID != created.ID {
		t.Fatalf("expected same payment for idempotent request, got new=%v ids=%s/%s", wasCreated, duplicate.ID, created.ID)
	}

	_, _, err = service.CreatePayment(ctx, CreatePaymentInput{UserID: "alice", Amount: 2000, Currency: "RUB", IdempotencyKey: "order-1"})
	if !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}

	if err := service.CancelPayment(ctx, "alice", created.ID); err != nil {
		t.Fatalf("cancel payment: %v", err)
	}
	cancelled, err := service.GetPayment(ctx, "alice", created.ID)
	if err != nil {
		t.Fatalf("get cancelled payment: %v", err)
	}
	if cancelled.Status != domain.PaymentCancelled {
		t.Fatalf("expected cancelled payment status, got %s", cancelled.Status)
	}
}

func TestPaymentServiceValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service := NewPaymentService(newFakePaymentRepo())

	_, _, err := service.CreatePayment(ctx, CreatePaymentInput{UserID: "alice", Amount: 0, Currency: "RUB"})
	if !errors.Is(err, domain.ErrInvalidAmount) {
		t.Fatalf("expected invalid amount error, got %v", err)
	}
	_, _, err = service.CreatePayment(ctx, CreatePaymentInput{UserID: "alice", Amount: 100, Currency: "R"})
	if !errors.Is(err, domain.ErrInvalidCurrency) {
		t.Fatalf("expected invalid currency error, got %v", err)
	}
	_, _, err = service.CreatePayment(ctx, CreatePaymentInput{Amount: 100, Currency: "RUB"})
	if !errors.Is(err, domain.ErrUserIDRequired) {
		t.Fatalf("expected missing user error, got %v", err)
	}
}

func TestPaymentServiceListNormalizesStatusFilter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newFakePaymentRepo()
	service := NewPaymentService(repo)

	created, _, err := service.CreatePayment(ctx, CreatePaymentInput{UserID: "alice", Amount: 500, Currency: "rub"})
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}
	if _, err := service.GetPayment(ctx, "alice", created.ID); err != nil {
		t.Fatalf("get payment: %v", err)
	}

	items, total, err := service.ListPayments(ctx, "alice", "pending", 1, 10)
	if err != nil {
		t.Fatalf("list payments with lowercase status: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected one payment after lowercase status filter, got total=%d len=%d", total, len(items))
	}
}