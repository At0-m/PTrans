package domain

import "time"

type WebhookSubscription struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Active    bool      `json:"active"`
	CreateAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *WebhookSubscription) Activate() {
	s.Active = true
}

func (s *WebhookSubscription) Deactivate() {
	s.Active = false
}

func (s *WebhookSubscription) Deactivete() {
	s.Deactivate()
}

func (s *WebhookSubscription) SetActive(flag bool) {
	s.Active = flag
}