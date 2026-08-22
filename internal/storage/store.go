package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
	"github.com/At0-m/PTrans/internal/service"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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

var storageIDSeq atomic.Uint64

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
    EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'rate_limit_windows'),
    EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'outbox_events'),
    EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'webhook_deliveries')
`

	var hasUsersTable bool
	var hasIdempotencyTable bool
	var hasRateLimitTable bool
	var hasOutboxTable bool
	var hasWebhookDeliveriesTable bool
	if err := s.pool.QueryRow(ctx, schemaCheckSQL).Scan(&hasUsersTable, &hasIdempotencyTable, &hasRateLimitTable, &hasOutboxTable, &hasWebhookDeliveriesTable); err != nil {
		return fmt.Errorf("check postgres schema: %w", err)
	}
	if !hasUsersTable || !hasIdempotencyTable || !hasRateLimitTable || !hasOutboxTable || !hasWebhookDeliveriesTable {
		return fmt.Errorf("database schema is missing; apply migrations/001_init.sql and migrations/002_outbox.sql before starting the app")
	}
	return nil
}

func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
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

	if err := insertPaymentCreatedOutboxEvent(ctx, tx, userID, payment, payment.CreatedAt); err != nil {
		return domain.Payment{}, false, err
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

	cancelled := payment
	cancelled.Status = domain.PaymentCancelled
	cancelled.UpdatedAt = cancelledAt
	if err := insertPaymentCancelledOutboxEvent(ctx, tx, userID, cancelled, cancelledAt); err != nil {
		return err
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
func insertPaymentCreatedOutboxEvent(ctx context.Context, tx pgx.Tx, userID string, payment domain.Payment, now time.Time) error {
	payload, err := json.Marshal(domain.PaymentCreatedPayload{
		ID:             payment.ID,
		Amount:         payment.Amount,
		Currency:       payment.Currency,
		IdempotencyKey: payment.IdempotencyKey,
		Status:         payment.Status,
		CreatedAt:      payment.CreatedAt,
	})
	if err != nil {
		return fmt.Errorf("marshal payment created outbox payload: %w", err)
	}

	return insertOutboxEvent(ctx, tx, outboxEventInsert{
		ID:            nextStorageID("evt"),
		UserID:        userID,
		EventType:     domain.EventPaymentCreated,
		AggregateType: domain.AggregateTypePayment,
		AggregateID:   payment.ID,
		Payload:       payload,
		Now:           now,
	})
}

func insertPaymentCancelledOutboxEvent(ctx context.Context, tx pgx.Tx, userID string, payment domain.Payment, cancelledAt time.Time) error {
	payload, err := json.Marshal(domain.PaymentCancelledPayload{
		ID:          payment.ID,
		CancelledAt: cancelledAt,
	})
	if err != nil {
		return fmt.Errorf("marshal payment cancelled outbox payload: %w", err)
	}

	return insertOutboxEvent(ctx, tx, outboxEventInsert{
		ID:            nextStorageID("evt"),
		UserID:        userID,
		EventType:     domain.EventPaymentCancelled,
		AggregateType: domain.AggregateTypePayment,
		AggregateID:   payment.ID,
		Payload:       payload,
		Now:           cancelledAt,
	})
}

type outboxEventInsert struct {
	ID            string
	UserID        string
	EventType     domain.EventType
	AggregateType string
	AggregateID   string
	Payload       []byte
	Now           time.Time
}

func insertOutboxEvent(ctx context.Context, tx pgx.Tx, event outboxEventInsert) error {
	if _, err := tx.Exec(ctx, `
INSERT INTO outbox_events (
    id, user_id, event_type, aggregate_type, aggregate_id, payload,
    status, attempts, next_attempt_at, locked_until, last_error, created_at, updated_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, 0, $8, NULL, NULL, $8, $8)
`, event.ID, event.UserID, event.EventType, event.AggregateType, event.AggregateID, event.Payload, domain.OutboxPending, event.Now); err != nil {
		return fmt.Errorf("insert outbox event: %w", err)
	}
	return nil
}

func (s *Store) FetchDueOutboxEvents(ctx context.Context, limit int, now time.Time, lockFor time.Duration) ([]domain.OutboxEvent, error) {
	if limit < 1 {
		limit = 1
	}
	if lockFor <= 0 {
		lockFor = 30 * time.Second
	}
	lockedUntil := now.Add(lockFor)

	rows, err := s.pool.Query(ctx, `
WITH picked AS (
    SELECT id
    FROM outbox_events
    WHERE status = $1
      AND next_attempt_at <= $2
      AND (locked_until IS NULL OR locked_until <= $2)
    ORDER BY next_attempt_at ASC, created_at ASC
    LIMIT $3
    FOR UPDATE SKIP LOCKED
)
UPDATE outbox_events AS e
SET locked_until = $4,
    attempts = attempts + 1,
    updated_at = $2
FROM picked
WHERE e.id = picked.id
RETURNING e.id, e.user_id, e.event_type, e.aggregate_type, e.aggregate_id, e.payload,
          e.status, e.attempts, e.next_attempt_at, e.locked_until,
          COALESCE(e.last_error, ''), e.created_at, e.updated_at
`, domain.OutboxPending, now, limit, lockedUntil)
	if err != nil {
		return nil, fmt.Errorf("fetch due outbox events: %w", err)
	}
	defer rows.Close()

	events := make([]domain.OutboxEvent, 0, limit)
	for rows.Next() {
		var event domain.OutboxEvent
		var payload []byte
		var lockedUntil pgtype.Timestamptz
		if err := rows.Scan(
			&event.ID,
			&event.UserID,
			&event.EventType,
			&event.AggregateType,
			&event.AggregateID,
			&payload,
			&event.Status,
			&event.Attempts,
			&event.NextAttemptAt,
			&lockedUntil,
			&event.LastError,
			&event.CreatedAt,
			&event.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan outbox event: %w", err)
		}
		if lockedUntil.Valid {
			value := lockedUntil.Time
			event.LockedUntil = &value
		}
		event.Payload = append(event.Payload[:0], payload...)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate outbox events: %w", err)
	}
	return events, nil
}

func (s *Store) ListDeliveredSubscriptionIDs(ctx context.Context, eventID string) (map[string]struct{}, error) {
	rows, err := s.pool.Query(ctx, `
SELECT DISTINCT subscription_id
FROM webhook_deliveries
WHERE event_id = $1 AND status = $2
`, eventID, domain.WebhookDeliverySucceeded)
	if err != nil {
		return nil, fmt.Errorf("list delivered subscription ids: %w", err)
	}
	defer rows.Close()

	ids := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan delivered subscription id: %w", err)
		}
		ids[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate delivered subscription ids: %w", err)
	}
	return ids, nil
}

func (s *Store) RecordWebhookDelivery(ctx context.Context, delivery domain.WebhookDelivery) error {
	var httpStatus any
	if delivery.HTTPStatus > 0 {
		httpStatus = delivery.HTTPStatus
	}
	var errText any
	if delivery.Error != "" {
		errText = delivery.Error
	}
	if _, err := s.pool.Exec(ctx, `
INSERT INTO webhook_deliveries (
    id, event_id, subscription_id, target_url, status, http_status,
    error, attempt, duration_ms, created_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
`, delivery.ID, delivery.EventID, delivery.SubscriptionID, delivery.TargetURL, delivery.Status, httpStatus, errText, delivery.Attempt, delivery.DurationMS, delivery.CreatedAt); err != nil {
		return fmt.Errorf("record webhook delivery: %w", err)
	}
	return nil
}

func (s *Store) MarkOutboxEventDelivered(ctx context.Context, eventID string, now time.Time) error {
	cmdTag, err := s.pool.Exec(ctx, `
UPDATE outbox_events
SET status = $2,
    locked_until = NULL,
    last_error = NULL,
    updated_at = $3
WHERE id = $1
`, eventID, domain.OutboxDelivered, now)
	if err != nil {
		return fmt.Errorf("mark outbox event delivered: %w", err)
	}
	if cmdTag.RowsAffected() == 0 {
		return fmt.Errorf("mark outbox event delivered: event %s not found", eventID)
	}
	return nil
}

func (s *Store) ScheduleOutboxEventRetry(ctx context.Context, eventID string, nextAttemptAt time.Time, lastError string, now time.Time) error {
	cmdTag, err := s.pool.Exec(ctx, `
UPDATE outbox_events
SET status = $2,
    next_attempt_at = $3,
    locked_until = NULL,
    last_error = $4,
    updated_at = $5
WHERE id = $1
`, eventID, domain.OutboxPending, nextAttemptAt, lastError, now)
	if err != nil {
		return fmt.Errorf("schedule outbox event retry: %w", err)
	}
	if cmdTag.RowsAffected() == 0 {
		return fmt.Errorf("schedule outbox event retry: event %s not found", eventID)
	}
	return nil
}

func (s *Store) MarkOutboxEventDeadLetter(ctx context.Context, eventID string, lastError string, now time.Time) error {
	cmdTag, err := s.pool.Exec(ctx, `
UPDATE outbox_events
SET status = $2,
    locked_until = NULL,
    last_error = $3,
    updated_at = $4
WHERE id = $1
`, eventID, domain.OutboxDeadLetter, lastError, now)
	if err != nil {
		return fmt.Errorf("mark outbox event dead letter: %w", err)
	}
	if cmdTag.RowsAffected() == 0 {
		return fmt.Errorf("mark outbox event dead letter: event %s not found", eventID)
	}
	return nil
}

func nextStorageID(prefix string) string {
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UTC().UnixNano(), storageIDSeq.Add(1))
}