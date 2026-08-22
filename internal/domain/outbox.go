package domain

import (
	"encoding/json"
	"time"
)

const (
	AggregateTypePayment = "payment"
)

type OutboxStatus string

const (
	OutboxPending    OutboxStatus = "pending"
	OutboxDelivered  OutboxStatus = "delivered"
	OutboxDeadLetter OutboxStatus = "dead_letter"
)

type OutboxEvent struct {
	ID            string          `json:"id"`
	UserID        string          `json:"user_id"`
	EventType     EventType       `json:"event_type"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   string          `json:"aggregate_id"`
	Payload       json.RawMessage `json:"payload"`
	Status        OutboxStatus    `json:"status"`
	Attempts      int             `json:"attempts"`
	NextAttemptAt time.Time       `json:"next_attempt_at"`
	LockedUntil   *time.Time      `json:"locked_until,omitempty"`
	LastError     string          `json:"last_error,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type WebhookDeliveryStatus string

const (
	WebhookDeliverySucceeded WebhookDeliveryStatus = "succeeded"
	WebhookDeliveryFailed    WebhookDeliveryStatus = "failed"
)

type WebhookDelivery struct {
	ID             string                `json:"id"`
	EventID        string                `json:"event_id"`
	SubscriptionID string                `json:"subscription_id"`
	TargetURL      string                `json:"target_url"`
	Status         WebhookDeliveryStatus `json:"status"`
	HTTPStatus     int                   `json:"http_status,omitempty"`
	Error          string                `json:"error,omitempty"`
	Attempt        int                   `json:"attempt"`
	DurationMS     int64                 `json:"duration_ms"`
	CreatedAt      time.Time             `json:"created_at"`
}

type WebhookEnvelope struct {
	EventID       string          `json:"event_id"`
	EventType     EventType       `json:"event_type"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   string          `json:"aggregate_id"`
	UserID        string          `json:"user_id"`
	Attempt       int             `json:"attempt"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Payload       json.RawMessage `json:"payload"`
}
