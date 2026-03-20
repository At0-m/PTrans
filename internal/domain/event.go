package domain

import (
	"encoding/json"
	"time"
)

type EventType string

const (
	EventPaymentCreated                 EventType = "PaymentCreated"
	EventPaymentCancelled               EventType = "Cancelled"
	EventWebhookSubscriptionCreated     EventType = "WebhookSubscriptionCreated"
	EventWebhookSubscriptionActivated   EventType = "WebhookSubscriptionActivated"
	EventWebhookSubscriptionDeactivated EventType = "WebhookSubscriptionDeactivated"
)

type Event struct {
	EventID       string          `json:"event_id"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   string          `json:"aggregate_id"`
	EventType     EventType       `json:"event_type"`
	Timestamp     time.Time       `json:"timestamp"`
	Payload       json.RawMessage `json:"payload"`
}
