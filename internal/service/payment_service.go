package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
	"github.com/At0-m/PTrans/internal/eventlog"
	"github.com/At0-m/PTrans/internal/state"
)

type PaymentService struct {
	store      *state.Store
	eventStore eventlog.EventStore
}

func NewEventStore(store *state.Store, eventStore eventlog.EventStore) *PaymentService {
	return &PaymentService{
		store:      store,
		eventStore: eventStore,
	}
}

func generateEventID() string {
	return fmt.Sprintf("evt_%d", time.Now().UnixNano())
}

func (s *PaymentService) ListPayments(status string, page, size int) ([]domain.Payment, int, error) {
	if page < 1 || size < 1 {
		return nil, 0, domain.ErrInvalidPagination
	}

	payments := s.store.ListPayments()

	if status != "" && !IsValidPaymentStatus(status) {
		return nil, 0, domain.ErrInvalidStatusFilter
	}

	filtred := make([]domain.Payment, 0, len(payments))
	for _, p := range payments {
		if status == "" || string(p.Status) == status {
			filtred = append(filtred, p)
		}
	}

	sort.Slice(filtred, func(i, j int) bool {
		return filtred[i].CreatedAt.After(filtred[j].CreatedAt)
	})

	total := len(filtred)
	offset := (page - 1) * size
	if offset >= total {
		return []domain.Payment{}, total, nil
	}
	end := offset + size
	if end > total {
		end = total
	}
	return filtred[offset:end], total, nil
}

func (s *PaymentService) CancelPayment(ctx context.Context, id string) error {
	payment, ok := s.store.GetPayment(id)
	if !ok {
		return domain.ErrPaymentNotFound
	}
	if payment.Status != domain.PaymentPending {
		return domain.ErrInvalidPaymentState
	}

	cancelledAt := time.Now().UTC()
	payload := domain.PaymentCancelledPayload{
		ID:          id,
		CancelledAt: cancelledAt,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal PaymentCancelled payload: %w", err)
	}

	evt := domain.Event{
		EventID:       generateEventID(),
		AggregateType: "payment",
		AggregateID:   id,
		EventType:     domain.EventPaymentCancelled,
		Timestamp:     cancelledAt,
		Payload:       payloadBytes,
	}

	if err := s.eventStore.Append(ctx, evt); err != nil {
		return fmt.Errorf("append PaymentCancelled event: %w", err)
	}

	if err := s.store.Apply(evt); err != nil {
		return fmt.Errorf("apply PaymentCancelled event: %w", err)
	}

	return nil
}

func IsValidPaymentStatus(status string) bool {
	switch domain.PaymentStatus(status) {
	case domain.PaymentCancelled,
		domain.PaymentFailed,
		domain.PaymentPending,
		domain.PaymentProcesing,
		domain.PaymentSucceeded:
		return true
	default:
		return false
	}
}
