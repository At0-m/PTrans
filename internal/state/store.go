package state

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
)

type Store struct {
	mu               sync.RWMutex
	payments         map[string]*domain.Payment
	subscriptions    map[string]*domain.WebhookSubscription
	idempotencyIndex map[string]string
}

func NewStore() *Store {
	return &Store{
		payments:         make(map[string]*domain.Payment),
		subscriptions:    make(map[string]*domain.WebhookSubscription),
		idempotencyIndex: make(map[string]string),
	}
}

func (s *Store) GetPayment(id string) (domain.Payment, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.payments[id]
	if !ok {
		return domain.Payment{}, false
	}
	return *p, true
}

func (s *Store) ListPayments() []domain.Payment {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]domain.Payment, 0, len(s.payments))
	for _, p := range s.payments {
		result = append(result, *p)
	}
	return result
}

func (s *Store) GetSubscription(id string) (domain.WebhookSubscription, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sub, ok := s.subscriptions[id]
	if !ok {
		return domain.WebhookSubscription{}, false
	}
	return *sub, true
}

func (s *Store) Apply(evt domain.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch evt.EventType {
	case domain.EventPaymentCreated:
		return s.applyPaymentCreated(evt)
	case domain.EventPaymentCancelled:
		return s.applyPaymentCancelled(evt)
	case domain.EventWebhookSubscriptionCreated:
		return s.applyWebhookSubscriptionCreated(evt)
	case domain.EventWebhookSubscriptionActivated:
		return s.applyWebhookSubscriptionActivated(evt)
	case domain.EventWebhookSubscriptionDeactivated:
		return s.applyWebhookSubscriptionDeactivated(evt)

	default:
		return fmt.Errorf("unknown event type: %s", evt.EventType)
	}
}

type PaymentCreatedPayload struct {
	ID             string               `json:"id"`
	Amount         int64                `json:"amount"`
	Currency       string               `json:"currency"`
	IdempotencyKey string               `json:"idempotency_key"`
	Status         domain.PaymentStatus `json:"status"`
	CreatedAt      time.Time            `json:"created_at"`
}

func (s *Store) applyPaymentCreated(evt domain.Event) error {
	var payload PaymentCreatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal PaymentCreated payload: %w", err)
	}

	s.payments[payload.ID] = &domain.Payment{
		ID:             payload.ID,
		Amount:         payload.Amount,
		Currency:       payload.Currency,
		Status:         payload.Status,
		IdempotencyKey: payload.IdempotencyKey,
		CreatedAt:      payload.CreatedAt,
		UpdatedAt:      payload.CreatedAt,
	}

	if payload.IdempotencyKey != "" {
		s.idempotencyIndex[payload.IdempotencyKey] = payload.ID
	}

	return nil
}

type PaymentCancelledPayload struct {
	ID          string    `json:"id"`
	CancelledAt time.Time `json:"cancelled_at"`
}

func (s *Store) applyPaymentCancelled(evt domain.Event) error {
	var payload PaymentCancelledPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal PaymentCancelled payload: %w", err)
	}

	p, ok := s.payments[payload.ID]
	if !ok {
		return fmt.Errorf("payment %s not found during apply", payload.ID)
	}

	p.Status = domain.PaymentCancelled
	p.UpdatedAt = payload.CancelledAt
	return nil
}

type WebhookSubscriptionCreatedPayload struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) applyWebhookSubscriptionCreated(evt domain.Event) error {
	var payload WebhookSubscriptionCreatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal WebhookSubscriptionCreated payload: %w", err)
	}

	if _, exists := s.subscriptions[payload.ID]; exists {
		return fmt.Errorf("subscription %s already exists during apply", payload.ID)
	}

	s.subscriptions[payload.ID] = &domain.WebhookSubscription{
		ID:        payload.ID,
		URL:       payload.URL,
		Active:    payload.Active,
		CreatedAt: payload.CreatedAt,
		UpdateAt:  payload.CreatedAt,
	}

	return nil
}

type WebhookSubscriptionActivatedPayload struct {
	ID          string    `json:"id"`
	ActivatedAt time.Time `json:"activated_at"`
}

func (s *Store) applyWebhookSubscriptionActivated(evt domain.Event) error {
	var payload WebhookSubscriptionActivatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal WebhookSubscriptionActivated payload: %w", err)
	}

	sub, ok := s.subscriptions[payload.ID]
	if !ok {
		return fmt.Errorf("subscription %s not found during apply", payload.ID)
	}

	sub.Active = true
	sub.UpdateAt = payload.ActivatedAt

	return nil
}

type WebhookSubscriptionDeactivatedPayload struct {
	ID            string    `json:"id"`
	DeactivatedAt time.Time `json:"deactivated_at"`
}

func (s *Store) applyWebhookSubscriptionDeactivated(evt domain.Event) error {
	var payload WebhookSubscriptionDeactivatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal WebhookSubscriptionDeactivated payload: %w", err)
	}

	sub, ok := s.subscriptions[payload.ID]
	if !ok {
		return fmt.Errorf("subscription %s not found during apply", payload.ID)
	}

	sub.Active = false
	sub.UpdateAt = payload.DeactivatedAt

	return nil
}
