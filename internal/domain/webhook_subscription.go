package domain

import "time"

type WebhookSubscription struct {
	ID        string
	URL       string
	Active    bool
	CreatedAt time.Time
	UpdateAt  time.Time
}

func (s WebhookSubscription) Activate() {
	s.Active = true
}

func (s WebhookSubscription) Deactivete() {
	s.Active = false
}

func (s WebhookSubscription) SetActive(flag bool) {
	s.Active = flag
}
