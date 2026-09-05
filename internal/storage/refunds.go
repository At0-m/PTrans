package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
	"github.com/At0-m/PTrans/internal/refunds"
	"github.com/jackc/pgx/v5"
)

var _ refunds.Repository = (*Store)(nil)
var _ refunds.WorkerRepository = (*Store)(nil)

const refundColumns = `id, user_id, payment_id, amount, currency, status, idempotency_key, request_hash,
attempts, check_failures, retryable, manual_review, last_error, next_action_at, processing_started_at,
lease_token, lease_until, version, created_at, updated_at`

func scanRefund(row pgx.Row) (domain.Refund, error) {
	var r domain.Refund
	err := row.Scan(&r.ID, &r.UserID, &r.PaymentID, &r.Amount, &r.Currency, &r.Status, &r.IdempotencyKey,
		&r.RequestHash, &r.Attempts, &r.CheckFailures, &r.Retryable, &r.ManualReview, &r.LastError,
		&r.NextActionAt, &r.ProcessingStartedAt, &r.LeaseToken, &r.LeaseUntil, &r.Version, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, domain.ErrRefundNotFound
	}
	return r, err
}

func (s *Store) CreateRefund(ctx context.Context, r domain.Refund) (domain.Refund, bool, error) {
	tables, err := s.ensureUserSchema(ctx, r.UserID)
	if err != nil {
		return domain.Refund{}, false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Refund{}, false, err
	}
	defer tx.Rollback(ctx)

	lockKey := fmt.Sprintf("refund:%d:%s:%s", len(r.UserID), r.UserID, r.IdempotencyKey)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return domain.Refund{}, false, err
	}
	existing, err := scanRefund(tx.QueryRow(ctx, `SELECT `+refundColumns+` FROM refunds WHERE user_id=$1 AND idempotency_key=$2`, r.UserID, r.IdempotencyKey))
	if err == nil {
		if existing.RequestHash != r.RequestHash {
			return domain.Refund{}, false, domain.ErrIdempotencyConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, domain.ErrRefundNotFound) {
		return domain.Refund{}, false, err
	}

	var amount int64
	var status domain.PaymentStatus
	err = tx.QueryRow(ctx, fmt.Sprintf(`SELECT amount, currency, status FROM %s WHERE id=$1 FOR UPDATE`, quoteIdentifier(tables.Payments)), r.PaymentID).Scan(&amount, &r.Currency, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Refund{}, false, domain.ErrPaymentNotFound
	}
	if err != nil {
		return domain.Refund{}, false, err
	}
	if status != domain.PaymentSucceeded {
		return domain.Refund{}, false, domain.ErrInvalidPaymentState
	}
	var reserved int64
	err = tx.QueryRow(ctx, `SELECT COALESCE(SUM(amount),0) FROM refunds WHERE user_id=$1 AND payment_id=$2 AND (status <> 'FAILED' OR retryable)`, r.UserID, r.PaymentID).Scan(&reserved)
	if err != nil {
		return domain.Refund{}, false, err
	}
	if r.Amount <= 0 || r.Amount > amount-reserved {
		return domain.Refund{}, false, domain.ErrRefundAmountExceeded
	}

	created, err := scanRefund(tx.QueryRow(ctx, `INSERT INTO refunds
        (id,user_id,payment_id,amount,currency,status,idempotency_key,request_hash,next_action_at,created_at,updated_at)
        VALUES ($1,$2,$3,$4,$5,'PENDING',$6,$7,$8,$8,$8) RETURNING `+refundColumns,
		r.ID, r.UserID, r.PaymentID, r.Amount, r.Currency, r.IdempotencyKey, r.RequestHash, r.CreatedAt))
	if err != nil {
		return domain.Refund{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Refund{}, false, err
	}
	return created, true, nil
}

func (s *Store) GetRefund(ctx context.Context, user, id string) (domain.Refund, error) {
	return scanRefund(s.pool.QueryRow(ctx, `SELECT `+refundColumns+` FROM refunds WHERE user_id=$1 AND id=$2`, user, id))
}

func (s *Store) ListRefunds(ctx context.Context, user string, filter refunds.ListFilter) ([]domain.Refund, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+refundColumns+` FROM refunds WHERE user_id=$1
        AND ($2='' OR payment_id=$2) AND ($3='' OR status=$3) AND (NOT $4 OR manual_review)
        ORDER BY created_at DESC, id LIMIT $5 OFFSET $6`, user, filter.PaymentID, filter.Status, filter.ManualReview, filter.Limit, filter.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]domain.Refund, 0)
	for rows.Next() {
		r, err := scanRefund(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *Store) ListReconciliations(ctx context.Context, user, id string, limit, offset int) ([]domain.ReconciliationResult, error) {
	if _, err := s.GetRefund(ctx, user, id); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id,operation_id,local_status,external_status,result,detail,checked_at
        FROM reconciliation_results WHERE operation_id=$1 ORDER BY checked_at DESC,id LIMIT $2 OFFSET $3`, id, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ReconciliationResult, 0)
	for rows.Next() {
		var item domain.ReconciliationResult
		if err := rows.Scan(&item.ID, &item.OperationID, &item.LocalStatus, &item.ExternalStatus, &item.Result, &item.Detail, &item.CheckedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) RequestRefundRecheck(ctx context.Context, user, id string, now time.Time) (domain.Refund, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Refund{}, err
	}
	defer tx.Rollback(ctx)
	r, err := scanRefund(tx.QueryRow(ctx, `SELECT `+refundColumns+` FROM refunds WHERE user_id=$1 AND id=$2 FOR UPDATE`, user, id))
	if err != nil {
		return domain.Refund{}, err
	}
	if r.Status != domain.RefundProcessing || !r.ManualReview || r.LeaseUntil != nil && r.LeaseUntil.After(now) {
		return domain.Refund{}, domain.ErrInvalidRefundState
	}
	r, err = scanRefund(tx.QueryRow(ctx, `UPDATE refunds SET manual_review=false,check_failures=0,
        next_action_at=$2,updated_at=$2,last_error='' WHERE id=$1 RETURNING `+refundColumns, id, now))
	if err != nil {
		return domain.Refund{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Refund{}, err
	}
	return r, nil
}

func (s *Store) ClaimRefund(ctx context.Context, kind refunds.ClaimKind, now time.Time, lease time.Duration, maxAttempts int) (domain.Refund, bool, error) {
	condition := `status='PROCESSING' AND NOT manual_review`
	if kind == refunds.ClaimSubmit {
		condition = `(status='PENDING' OR (status='FAILED' AND retryable)) AND attempts < $2`
	} else if kind != refunds.ClaimReconcile {
		return domain.Refund{}, false, errors.New("unknown claim kind")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Refund{}, false, err
	}
	defer tx.Rollback(ctx)
	args := []any{now}
	if kind == refunds.ClaimSubmit {
		args = append(args, maxAttempts)
	}
	r, err := scanRefund(tx.QueryRow(ctx, `SELECT `+refundColumns+` FROM refunds WHERE `+condition+`
        AND next_action_at <= $1 AND (lease_until IS NULL OR lease_until <= $1)
        ORDER BY next_action_at,created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, args...))
	if errors.Is(err, domain.ErrRefundNotFound) {
		return domain.Refund{}, false, nil
	}
	if err != nil {
		return domain.Refund{}, false, err
	}
	token, err := refunds.NewID("lease")
	if err != nil {
		return domain.Refund{}, false, err
	}
	if kind == refunds.ClaimSubmit {
		if err := domain.ValidateRefundTransition(r.Status, domain.RefundProcessing); err != nil {
			return domain.Refund{}, false, err
		}
		r, err = scanRefund(tx.QueryRow(ctx, `UPDATE refunds SET status='PROCESSING',retryable=false,
            attempts=attempts+1,check_failures=0,processing_started_at=$2,lease_token=$3,lease_until=$4,
            next_action_at=$2,updated_at=$2 WHERE id=$1 RETURNING `+refundColumns, r.ID, now, token, now.Add(lease)))
	} else {
		r, err = scanRefund(tx.QueryRow(ctx, `UPDATE refunds SET lease_token=$2,lease_until=$3,
            updated_at=$4 WHERE id=$1 RETURNING `+refundColumns, r.ID, token, now.Add(lease), now))
	}
	if err != nil {
		return domain.Refund{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Refund{}, false, err
	}
	return r, true, nil
}

func (s *Store) ApplyRefundOutcome(ctx context.Context, claimed domain.Refund, outcome refunds.Outcome, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := scanRefund(tx.QueryRow(ctx, `SELECT `+refundColumns+` FROM refunds WHERE id=$1 FOR UPDATE`, claimed.ID))
	if err != nil {
		return err
	}
	if current.LeaseToken == "" || current.LeaseToken != claimed.LeaseToken || current.Version != claimed.Version ||
		current.LeaseUntil == nil || !current.LeaseUntil.After(now) {
		return domain.ErrLeaseLost
	}
	if current.Status != domain.RefundProcessing {
		return domain.ErrInvalidRefundState
	}
	if outcome.Status != current.Status {
		if err := domain.ValidateRefundTransition(current.Status, outcome.Status); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE refunds SET status=$2,retryable=$3,manual_review=$4,last_error=$5,
        check_failures=$6,next_action_at=$7,lease_token='',lease_until=NULL,updated_at=$8 WHERE id=$1`,
		claimed.ID, outcome.Status, outcome.Retryable, outcome.ManualReview, outcome.LastError,
		outcome.CheckFailures, outcome.NextActionAt, now)
	if err != nil {
		return err
	}
	if result := outcome.Reconciliation; result != nil {
		id, err := refunds.NewID("rec")
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO reconciliation_results (id,operation_id,local_status,external_status,result,detail,checked_at)
            VALUES ($1,$2,$3,$4,$5,$6,$7)`, id, claimed.ID, current.Status, result.ExternalStatus, result.Result, result.Detail, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
