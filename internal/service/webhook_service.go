package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
	"github.com/At0-m/PTrans/internal/eventlog"
	"github.com/At0-m/PTrans/internal/state"
)

type WebhookService struct {
	store      *state.Store
	eventStore eventlog.EventStore
}

func NewWebhookService(store *state.Store, eventStore eventlog.EventStore) *WebhookService {
	return &WebhookService{
		store:      store,
		eventStore: eventStore,
	}
}

func (s *WebhookService) GetSubscription(id string) (domain.WebhookSubscription, error) {
	sub, ok := s.store.GetSubscription(id)
	if !ok {
		return domain.WebhookSubscription{}, domain.ErrSubsctiptionNotFound
	}
	return sub, nil
}

func (s *WebhookService) SetSubscriptionActive(ctx context.Context, id string, active bool) error {
	sub, ok := s.store.GetSubscription(id)
	if !ok {
		return domain.ErrSubsctiptionNotFound
	}
	if sub.Active == active {
		return nil
	}

	now := time.Now().UTC()

	var (
		eventType domain.EventType
		payload   any
	)

	if active {
		eventType = domain.EventWebhookSubscriptionActivated
		payload = domain.WebhookSubscriptionActivatedPayload{
			ID:          id,
			ActivatedAt: now,
		}
	} else {
		eventType = domain.EventWebhookSubscriptionDeactivated
		payload = domain.WebhookSubscriptionDeactivatedPayload{
			ID:            id,
			DeactivatedAt: now,
		}
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal webhook subscription event payload: %w", err)
	}

	evt := domain.Event{
		EventID:       generateEventID(),
		AggregateType: "webhook_subscription",
		AggregateID:   id,
		EventType:     eventType,
		Timestamp:     now,
		Payload:       payloadBytes,
	}

	if err := s.eventStore.Append(ctx, evt); err != nil {
		return fmt.Errorf("append webhook subscription event: %w", err)
	}

	if err := s.store.Apply(evt); err != nil {
		return fmt.Errorf("append webhook subscription event: %w", err)
	}

	return nil
}
