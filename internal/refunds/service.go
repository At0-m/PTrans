package refunds

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
)

type ListFilter struct {
	PaymentID    string
	Status       domain.RefundStatus
	ManualReview bool
	Limit        int
	Offset       int
}

type Repository interface {
	CreateRefund(context.Context, domain.Refund) (domain.Refund, bool, error)
	GetRefund(context.Context, string, string) (domain.Refund, error)
	ListRefunds(context.Context, string, ListFilter) ([]domain.Refund, error)
	ListReconciliations(context.Context, string, string, int, int) ([]domain.ReconciliationResult, error)
	RequestRefundRecheck(context.Context, string, string, time.Time) (domain.Refund, error)
}

type Service struct{ repo Repository }

func NewService(repo Repository) *Service { return &Service{repo: repo} }

func NewID(prefix string) (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(buf[:]), nil
}

func (s *Service) Create(ctx context.Context, user, payment string, amount int64, key string) (domain.Refund, bool, error) {
	if strings.TrimSpace(user) == "" {
		return domain.Refund{}, false, domain.ErrUserIDRequired
	}
	if amount <= 0 {
		return domain.Refund{}, false, domain.ErrInvalidAmount
	}
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 128 {
		return domain.Refund{}, false, domain.ErrIdempotencyKeyRequired
	}
	if payment == "" || len(payment) > 128 {
		return domain.Refund{}, false, domain.ErrPaymentNotFound
	}
	id, err := NewID("refund")
	if err != nil {
		return domain.Refund{}, false, err
	}
	raw, err := json.Marshal(struct {
		Payment string
		Amount  int64
	}{payment, amount})
	if err != nil {
		return domain.Refund{}, false, err
	}
	hash := sha256.Sum256(raw)
	now := time.Now().UTC()
	return s.repo.CreateRefund(ctx, domain.Refund{
		ID: id, UserID: user, PaymentID: payment, Amount: amount, Status: domain.RefundPending,
		IdempotencyKey: key, RequestHash: hex.EncodeToString(hash[:]), NextActionAt: now,
		CreatedAt: now, UpdatedAt: now,
	})
}

func (s *Service) Get(ctx context.Context, user, id string) (domain.Refund, error) {
	return s.repo.GetRefund(ctx, user, id)
}

func (s *Service) List(ctx context.Context, user string, filter ListFilter) ([]domain.Refund, error) {
	if filter.Limit < 1 || filter.Limit > 100 || filter.Offset < 0 {
		return nil, domain.ErrInvalidPagination
	}
	if filter.Status != "" && !domain.IsRefundStatus(filter.Status) {
		return nil, domain.ErrInvalidStatusFilter
	}
	return s.repo.ListRefunds(ctx, user, filter)
}

func (s *Service) Reconciliations(ctx context.Context, user, id string, limit, offset int) ([]domain.ReconciliationResult, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, domain.ErrInvalidPagination
	}
	return s.repo.ListReconciliations(ctx, user, id, limit, offset)
}

func (s *Service) Recheck(ctx context.Context, user, id string) (domain.Refund, error) {
	return s.repo.RequestRefundRecheck(ctx, user, id, time.Now().UTC())
}
