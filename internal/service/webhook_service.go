package service

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
)

type WebhookService struct {
	repo WebhookRepository
}

type CreateWebhookSubscriptionInput struct {
	UserID string
	URL    string
	Active bool
}

func NewWebhookService(repo WebhookRepository) *WebhookService {
	return &WebhookService{repo: repo}
}

func (s *WebhookService) CreateSubscription(ctx context.Context, input CreateWebhookSubscriptionInput) (domain.WebhookSubscription, error) {
	if strings.TrimSpace(input.UserID) == "" {
		return domain.WebhookSubscription{}, domain.ErrUserIDRequired
	}

	normalizedURL, err := normalizeWebhookURL(input.URL)
	if err != nil {
		return domain.WebhookSubscription{}, err
	}

	now := time.Now().UTC()
	subscription := domain.WebhookSubscription{
		ID:        generateSubscriptionID(),
		URL:       normalizedURL,
		Active:    input.Active,
		CreateAt: now,
		UpdatedAt: now,
	}

	return s.repo.CreateSubscription(ctx, input.UserID, subscription)
}

func (s *WebhookService) ListSubscriptions(ctx context.Context, userID string) ([]domain.WebhookSubscription, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, domain.ErrUserIDRequired
	}
	return s.repo.ListSubscriptions(ctx, userID)
}

func (s *WebhookService) GetSubscription(ctx context.Context, userID, id string) (domain.WebhookSubscription, error) {
	if strings.TrimSpace(userID) == "" {
		return domain.WebhookSubscription{}, domain.ErrUserIDRequired
	}
	return s.repo.GetSubscription(ctx, userID, id)
}

func (s *WebhookService) SetSubscriptionActive(ctx context.Context, userID, id string, active bool) error {
	if strings.TrimSpace(userID) == "" {
		return domain.ErrUserIDRequired
	}
	return s.repo.SetSubscriptionActive(ctx, userID, id, active, time.Now().UTC())
}

func normalizeWebhookURL(rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", domain.ErrInvalidWebhookURL
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", domain.ErrInvalidWebhookURL
	}
	if parsed.Host == "" {
		return "", domain.ErrInvalidWebhookURL
	}
	return parsed.String(), nil
}