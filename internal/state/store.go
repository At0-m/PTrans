package state

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"

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

func (s *Store) GetPaymentByIdempotencyKey(key string) (domain.Payment, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	id, ok := s.idempotencyIndex[key]
	if !ok {
		return domain.Payment{}, false
	}

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

	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

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

func (s *Store) ListSubscriptions() []domain.WebhookSubscription {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]domain.WebhookSubscription, 0, len(s.subscriptions))
	for _, sub := range s.subscriptions {
		result = append(result, *sub)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].CreateAt.After(result[j].CreateAt)
	})

	return result
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

func (s *Store) applyPaymentCreated(evt domain.Event) error {
	var payload domain.PaymentCreatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal PaymentCreated payload: %w", err)
	}

	if _, exists := s.payments[payload.ID]; exists {
		return fmt.Errorf("payment %s already exists during apply", payload.ID)
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

func (s *Store) applyPaymentCancelled(evt domain.Event) error {
	var payload domain.PaymentCancelledPayload
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

func (s *Store) applyWebhookSubscriptionCreated(evt domain.Event) error {
	var payload domain.WebhookSubscriptionCreatedPayload
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
		CreateAt: payload.CreatedAt,
		UpdatedAt: payload.CreatedAt,
	}

	return nil
}

func (s *Store) applyWebhookSubscriptionActivated(evt domain.Event) error {
	var payload domain.WebhookSubscriptionActivatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal WebhookSubscriptionActivated payload: %w", err)
	}

	sub, ok := s.subscriptions[payload.ID]
	if !ok {
		return fmt.Errorf("subscription %s not found during apply", payload.ID)
	}

	sub.Active = true
	sub.UpdatedAt = payload.ActivatedAt

	return nil
}

func (s *Store) applyWebhookSubscriptionDeactivated(evt domain.Event) error {
	var payload domain.WebhookSubscriptionDeactivatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("unmarshal WebhookSubscriptionDeactivated payload: %w", err)
	}

	sub, ok := s.subscriptions[payload.ID]
	if !ok {
		return fmt.Errorf("subscription %s not found during apply", payload.ID)
	}

	sub.Active = false
	sub.UpdatedAt = payload.DeactivatedAt

	return nil
}