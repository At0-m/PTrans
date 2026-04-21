package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
	"github.com/At0-m/PTrans/internal/service"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool         *pgxpool.Pool
	ensuredUsers sync.Map
}

type userTables struct {
	Payments string
	Webhooks string
	Suffix   string
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var _ service.PaymentRepository = (*Store)(nil)
var _ service.WebhookRepository = (*Store)(nil)

func New(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	store := &Store{pool: pool}
	if err := store.Init(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() {
	if s == nil || s.pool == nil {
		return
	}
	s.pool.Close()
}

func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}

func (s *Store) Init(ctx context.Context) error {
	const schemaCheckSQL = `
SELECT
    EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'app_users'),
    EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'idempotency_keys'),
    EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'rate_limit_windows')
`

	var hasUsersTable bool
	var hasIdempotencyTable bool
	var hasRateLimitTable bool
	if err := s.pool.QueryRow(ctx, schemaCheckSQL).Scan(&hasUsersTable, &hasIdempotencyTable, &hasRateLimitTable); err != nil {
		return fmt.Errorf("check postgres schema: %w", err)
	}
	if !hasUsersTable || !hasIdempotencyTable || !hasRateLimitTable {
		return fmt.Errorf("database schema is missing; apply migrations/001_init.sql before starting the app")
	}
	return nil
}

func (s *Store) CreatePayment(ctx context.Context, userID string, payment domain.Payment, requestHash string) (domain.Payment, bool, error) {
	tables, err := s.ensureUserSchema(ctx, userID)
	if err != nil {
		return domain.Payment{}, false, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Payment{}, false, fmt.Errorf("begin create payment tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if payment.IdempotencyKey != "" {
		cmdTag, err := tx.Exec(ctx, `
INSERT INTO idempotency_keys (user_id, idempotency_key, request_hash, payment_id, created_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id, idempotency_key) DO NOTHING
`, userID, payment.IdempotencyKey, requestHash, payment.ID, payment.CreatedAt)
		if err != nil {
			return domain.Payment{}, false, fmt.Errorf("insert idempotency key: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			var existingHash string
			var existingPaymentID string
			if err := tx.QueryRow(ctx, `
SELECT request_hash, payment_id
FROM idempotency_keys
WHERE user_id = $1 AND idempotency_key = $2
`, userID, payment.IdempotencyKey).Scan(&existingHash, &existingPaymentID); err != nil {
				return domain.Payment{}, false, fmt.Errorf("load existing idempotency key: %w", err)
			}
			if existingHash != requestHash {
				return domain.Payment{}, false, domain.ErrIdempotencyConflict
			}
			existing, err := getPayment(ctx, tx, tables.Payments, existingPaymentID)
			if err != nil {
				return domain.Payment{}, false, err
			}
			return existing, false, nil
		}
	}

	insertPaymentSQL := fmt.Sprintf(`
INSERT INTO %s (id, amount, currency, status, idempotency_key, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
`, quoteIdentifier(tables.Payments))
	if _, err := tx.Exec(ctx, insertPaymentSQL,
		payment.ID,
		payment.Amount,
		payment.Currency,
		payment.Status,
		payment.IdempotencyKey,
		payment.CreatedAt,
		payment.UpdatedAt,
	); err != nil {
		return domain.Payment{}, false, fmt.Errorf("insert payment: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Payment{}, false, fmt.Errorf("commit create payment tx: %w", err)
	}
	return payment, true, nil
}

func (s *Store) GetPayment(ctx context.Context, userID, id string) (domain.Payment, error) {
	tables, err := s.ensureUserSchema(ctx, userID)
	if err != nil {
		return domain.Payment{}, err
	}
	return getPayment(ctx, s.pool, tables.Payments, id)
}

func (s *Store) ListPayments(ctx context.Context, userID string, filter service.PaymentListFilter) ([]domain.Payment, int, error) {
	tables, err := s.ensureUserSchema(ctx, userID)
	if err != nil {
		return nil, 0, err
	}

	countSQL := fmt.Sprintf(`SELECT COUNT(*) FROM %s`, quoteIdentifier(tables.Payments))
	countArgs := []any{}
	whereClause := ""
	if filter.Status != "" {
		whereClause = " WHERE status = $1"
		countArgs = append(countArgs, filter.Status)
	}

	var total int
	if err := s.pool.QueryRow(ctx, countSQL+whereClause, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count payments: %w", err)
	}

	querySQL := fmt.Sprintf(`
SELECT id, amount, currency, status, COALESCE(idempotency_key, ''), created_at, updated_at
FROM %s%s
ORDER BY created_at DESC
LIMIT $%d OFFSET $%d
`, quoteIdentifier(tables.Payments), whereClause, len(countArgs)+1, len(countArgs)+2)
	args := append(countArgs, filter.Size, (filter.Page-1)*filter.Size)

	rows, err := s.pool.Query(ctx, querySQL, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list payments: %w", err)
	}
	defer rows.Close()

	payments := make([]domain.Payment, 0, filter.Size)
	for rows.Next() {
		var payment domain.Payment
		if err := rows.Scan(&payment.ID, &payment.Amount, &payment.Currency, &payment.Status, &payment.IdempotencyKey, &payment.CreatedAt, &payment.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan payment: %w", err)
		}
		payments = append(payments, payment)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate payments: %w", err)
	}
	return payments, total, nil
}

func (s *Store) CancelPayment(ctx context.Context, userID, id string, cancelledAt time.Time) error {
	tables, err := s.ensureUserSchema(ctx, userID)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin cancel payment tx: %w", err)
	}
	defer tx.Rollback(ctx)

	payment, err := getPayment(ctx, tx, tables.Payments, id)
	if err != nil {
		return err
	}
	if payment.Status != domain.PaymentPending {
		return domain.ErrInvalidPaymentState
	}

	updateSQL := fmt.Sprintf(`
UPDATE %s
SET status = $1, updated_at = $2
WHERE id = $3
`, quoteIdentifier(tables.Payments))
	if _, err := tx.Exec(ctx, updateSQL, domain.PaymentCancelled, cancelledAt, id); err != nil {
		return fmt.Errorf("cancel payment: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit cancel payment tx: %w", err)
	}
	return nil
}

func (s *Store) CreateSubscription(ctx context.Context, userID string, subscription domain.WebhookSubscription) (domain.WebhookSubscription, error) {
	tables, err := s.ensureUserSchema(ctx, userID)
	if err != nil {
		return domain.WebhookSubscription{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.WebhookSubscription{}, fmt.Errorf("begin create subscription tx: %w", err)
	}
	defer tx.Rollback(ctx)

	insertSQL := fmt.Sprintf(`
INSERT INTO %s (id, url, active, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5)
`, quoteIdentifier(tables.Webhooks))
	if _, err := tx.Exec(ctx, insertSQL, subscription.ID, subscription.URL, subscription.Active, subscription.CreateAt, subscription.UpdatedAt); err != nil {
		return domain.WebhookSubscription{}, fmt.Errorf("insert webhook subscription: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.WebhookSubscription{}, fmt.Errorf("commit create subscription tx: %w", err)
	}
	return subscription, nil
}

func (s *Store) ListSubscriptions(ctx context.Context, userID string) ([]domain.WebhookSubscription, error) {
	tables, err := s.ensureUserSchema(ctx, userID)
	if err != nil {
		return nil, err
	}

	querySQL := fmt.Sprintf(`
SELECT id, url, active, created_at, updated_at
FROM %s
ORDER BY created_at DESC
`, quoteIdentifier(tables.Webhooks))
	rows, err := s.pool.Query(ctx, querySQL)
	if err != nil {
		return nil, fmt.Errorf("list webhook subscriptions: %w", err)
	}
	defer rows.Close()

	items := make([]domain.WebhookSubscription, 0)
	for rows.Next() {
		var sub domain.WebhookSubscription
		if err := rows.Scan(&sub.ID, &sub.URL, &sub.Active, &sub.CreateAt, &sub.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan webhook subscription: %w", err)
		}
		items = append(items, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate webhook subscriptions: %w", err)
	}
	return items, nil
}

func (s *Store) GetSubscription(ctx context.Context, userID, id string) (domain.WebhookSubscription, error) {
	tables, err := s.ensureUserSchema(ctx, userID)
	if err != nil {
		return domain.WebhookSubscription{}, err
	}
	return getSubscription(ctx, s.pool, tables.Webhooks, id)
}

func (s *Store) SetSubscriptionActive(ctx context.Context, userID, id string, active bool, updatedAt time.Time) error {
	tables, err := s.ensureUserSchema(ctx, userID)
	if err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin update subscription tx: %w", err)
	}
	defer tx.Rollback(ctx)

	subscription, err := getSubscription(ctx, tx, tables.Webhooks, id)
	if err != nil {
		return err
	}
	if subscription.Active == active {
		return nil
	}

	updateSQL := fmt.Sprintf(`
UPDATE %s
SET active = $1, updated_at = $2
WHERE id = $3
`, quoteIdentifier(tables.Webhooks))
	if _, err := tx.Exec(ctx, updateSQL, active, updatedAt, id); err != nil {
		return fmt.Errorf("update webhook subscription: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit update subscription tx: %w", err)
	}
	return nil
}

func (s *Store) ensureUserSchema(ctx context.Context, userID string) (userTables, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return userTables{}, domain.ErrUserIDRequired
	}

	tables := deriveUserTables(userID)
	if _, ok := s.ensuredUsers.Load(userID); ok {
		return tables, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return userTables{}, fmt.Errorf("begin ensure user schema tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := ensureUserTablesTx(ctx, tx, userID, tables); err != nil {
		return userTables{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return userTables{}, fmt.Errorf("commit ensure user schema tx: %w", err)
	}

	s.ensuredUsers.Store(userID, struct{}{})
	return tables, nil
}

func ensureUserTablesTx(ctx context.Context, tx pgx.Tx, userID string, tables userTables) error {
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `
INSERT INTO app_users (user_id, payments_table_name, webhook_subscriptions_table_name, created_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id) DO NOTHING
`, userID, tables.Payments, tables.Webhooks, now); err != nil {
		return fmt.Errorf("register user tables: %w", err)
	}

	var existingPaymentsTable string
	var existingWebhooksTable string
	if err := tx.QueryRow(ctx, `
SELECT payments_table_name, webhook_subscriptions_table_name
FROM app_users
WHERE user_id = $1
`, userID).Scan(&existingPaymentsTable, &existingWebhooksTable); err != nil {
		return fmt.Errorf("load registered user tables: %w", err)
	}
	if existingPaymentsTable != tables.Payments || existingWebhooksTable != tables.Webhooks {
		return fmt.Errorf("user %q is registered with unexpected table names", userID)
	}

	paymentsSQL := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
    id TEXT PRIMARY KEY,
    amount BIGINT NOT NULL,
    currency TEXT NOT NULL,
    status TEXT NOT NULL,
    idempotency_key TEXT,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
)`, quoteIdentifier(tables.Payments))
	if _, err := tx.Exec(ctx, paymentsSQL); err != nil {
		return fmt.Errorf("create payments table for %s: %w", userID, err)
	}
	paymentsCreatedIdx := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (created_at DESC)`, quoteIdentifier("idx_"+tables.Suffix+"_payments_created_at"), quoteIdentifier(tables.Payments))
	if _, err := tx.Exec(ctx, paymentsCreatedIdx); err != nil {
		return fmt.Errorf("create payments created_at index: %w", err)
	}
	paymentsStatusIdx := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (status, created_at DESC)`, quoteIdentifier("idx_"+tables.Suffix+"_payments_status_created_at"), quoteIdentifier(tables.Payments))
	if _, err := tx.Exec(ctx, paymentsStatusIdx); err != nil {
		return fmt.Errorf("create payments status index: %w", err)
	}

	webhooksSQL := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
    id TEXT PRIMARY KEY,
    url TEXT NOT NULL,
    active BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
)`, quoteIdentifier(tables.Webhooks))
	if _, err := tx.Exec(ctx, webhooksSQL); err != nil {
		return fmt.Errorf("create webhook subscriptions table for %s: %w", userID, err)
	}
	webhooksCreatedIdx := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (created_at DESC)`, quoteIdentifier("idx_"+tables.Suffix+"_webhooks_created_at"), quoteIdentifier(tables.Webhooks))
	if _, err := tx.Exec(ctx, webhooksCreatedIdx); err != nil {
		return fmt.Errorf("create webhook created_at index: %w", err)
	}
	return nil
}

func deriveUserTables(userID string) userTables {
	sum := sha256.Sum256([]byte(strings.TrimSpace(userID)))
	suffix := hex.EncodeToString(sum[:])[:16]
	return userTables{Payments: "payments_u_" + suffix, Webhooks: "webhook_subscriptions_u_" + suffix, Suffix: suffix}
}

func getPayment(ctx context.Context, db rowQuerier, tableName, id string) (domain.Payment, error) {
	querySQL := fmt.Sprintf(`
SELECT id, amount, currency, status, COALESCE(idempotency_key, ''), created_at, updated_at
FROM %s
WHERE id = $1
`, quoteIdentifier(tableName))

	var payment domain.Payment
	if err := db.QueryRow(ctx, querySQL, id).Scan(&payment.ID, &payment.Amount, &payment.Currency, &payment.Status, &payment.IdempotencyKey, &payment.CreatedAt, &payment.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Payment{}, domain.ErrPaymentNotFound
		}
		return domain.Payment{}, fmt.Errorf("get payment: %w", err)
	}
	return payment, nil
}

func getSubscription(ctx context.Context, db rowQuerier, tableName, id string) (domain.WebhookSubscription, error) {
	querySQL := fmt.Sprintf(`
SELECT id, url, active, created_at, updated_at
FROM %s
WHERE id = $1
`, quoteIdentifier(tableName))

	var sub domain.WebhookSubscription
	if err := db.QueryRow(ctx, querySQL, id).Scan(&sub.ID, &sub.URL, &sub.Active, &sub.CreateAt, &sub.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.WebhookSubscription{}, domain.ErrSubscriptionNotFound
		}
		return domain.WebhookSubscription{}, fmt.Errorf("get webhook subscription: %w", err)
	}
	return sub, nil
}

func quoteIdentifier(name string) string {
	if name == "" {
		panic("empty sql identifier")
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		panic(fmt.Sprintf("unsafe sql identifier %q", name))
	}
	return `"` + name + `"`
}