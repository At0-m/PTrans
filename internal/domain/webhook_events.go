package domain

import "time"

type WebhookSubscriptionCreatedPayload struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

type WebhookSubscriptionActivatedPayload struct {
	ID          string    `json:"id"`
	ActivatedAt time.Time `json:"activated_at"`
}

type WebhookSubscriptionDeactivatedPayload struct {
	ID            string    `json:"id"`
	DeactivatedAt time.Time `json:"deactivated_at"`
}
