package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
)

type PaymentService struct {
	repo PaymentRepository
}

type CreatePaymentInput struct {
	UserID         string
	Amount         int64
	Currency       string
	IdempotencyKey string
}

func NewPaymentService(repo PaymentRepository) *PaymentService {
	return &PaymentService{repo: repo}
}

func (s *PaymentService) CreatePayment(ctx context.Context, input CreatePaymentInput) (domain.Payment, bool, error) {
	if strings.TrimSpace(input.UserID) == "" {
		return domain.Payment{}, false, domain.ErrUserIDRequired
	}
	if input.Amount <= 0 {
		return domain.Payment{}, false, domain.ErrInvalidAmount
	}

	currency := normalizeCurrency(input.Currency)
	if !isValidCurrency(currency) {
		return domain.Payment{}, false, domain.ErrInvalidCurrency
	}

	now := time.Now().UTC()
	payment := domain.Payment{
		ID:             generatePaymentID(),
		Amount:         input.Amount,
		Currency:       currency,
		Status:         domain.PaymentPending,
		IdempotencyKey: strings.TrimSpace(input.IdempotencyKey),
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	requestHash := hashCreatePaymentRequest(input.Amount, currency)
	return s.repo.CreatePayment(ctx, input.UserID, payment, requestHash)
}

func (s *PaymentService) GetPayment(ctx context.Context, userID, id string) (domain.Payment, error) {
	if strings.TrimSpace(userID) == "" {
		return domain.Payment{}, domain.ErrUserIDRequired
	}
	return s.repo.GetPayment(ctx, userID, id)
}

func (s *PaymentService) ListPayments(ctx context.Context, userID, status string, page, size int) ([]domain.Payment, int, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, 0, domain.ErrUserIDRequired
	}
	if page < 1 || size < 1 {
		return nil, 0, domain.ErrInvalidPagination
	}

	status = normalizePaymentStatus(status)
	if status != "" && !IsValidPaymentStatus(status) {
		return nil, 0, domain.ErrInvalidStatusFilter
	}

	return s.repo.ListPayments(ctx, userID, PaymentListFilter{
		Status: status,
		Page:   page,
		Size:   size,
	})
}

func (s *PaymentService) CancelPayment(ctx context.Context, userID, id string) error {
	if strings.TrimSpace(userID) == "" {
		return domain.ErrUserIDRequired
	}
	return s.repo.CancelPayment(ctx, userID, id, time.Now().UTC())
}

func IsValidPaymentStatus(status string) bool {
	switch domain.PaymentStatus(status) {
	case domain.PaymentCancelled,
		domain.PaymentFailed,
		domain.PaymentPending,
		domain.PaymentProcessing,
		domain.PaymentSucceeded:
		return true
	default:
		return false
	}
}

func normalizeCurrency(currency string) string {
	return strings.ToUpper(strings.TrimSpace(currency))
}

func normalizePaymentStatus(status string) string {
	return strings.ToUpper(strings.TrimSpace(status))
}

func isValidCurrency(currency string) bool {
	if len(currency) != 3 {
		return false
	}
	for _, r := range currency {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

func hashCreatePaymentRequest(amount int64, currency string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("amount=%d;currency=%s", amount, currency)))
	return hex.EncodeToString(sum[:])
}