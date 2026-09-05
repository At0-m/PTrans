package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
	"github.com/At0-m/PTrans/internal/refunds"
	"github.com/jackc/pgx/v5/pgxpool"
)

func integrationStore(t *testing.T) *Store {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Database != "ptrans_test" {
		t.Fatal("integration database must be the disposable ptrans_test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatal("migrations not found", err)
	}
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	store := &Store{pool: pool}
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	return store
}

func seedPayment(t *testing.T, s *Store, amount int64, status domain.PaymentStatus) (string, domain.Payment) {
	t.Helper()
	user, err := refunds.NewID("test_user")
	if err != nil {
		t.Fatal(err)
	}
	id, err := refunds.NewID("pay")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	p, _, err := s.CreatePayment(context.Background(), user, domain.Payment{ID: id, Amount: amount, Currency: "RUB", Status: status, CreatedAt: now, UpdatedAt: now}, "")
	if err != nil {
		t.Fatal(err)
	}
	return user, p
}

func createRefund(t *testing.T, s *Store, user string, p domain.Payment, amount int64, key string) domain.Refund {
	t.Helper()
	r, created, err := refunds.NewService(s).Create(context.Background(), user, p.ID, amount, key)
	if err != nil || !created {
		t.Fatalf("created=%v error=%v", created, err)
	}
	return r
}

func outboxCount(t *testing.T, s *Store, id, event string) int {
	t.Helper()
	var n int
	err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type=$2`, id, event).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRefundsPostgres(t *testing.T) {
	s := integrationStore(t)
	ctx := context.Background()
	t.Run("creation_replay_and_ownership", func(t *testing.T) {
		user, p := seedPayment(t, s, 10000, domain.PaymentSucceeded)
		r := createRefund(t, s, user, p, 3000, "key-1")
		if r.Status != domain.RefundPending || r.Currency != "RUB" || outboxCount(t, s, r.ID, "refund.created") != 1 {
			t.Fatalf("%+v", r)
		}
		replay, created, err := refunds.NewService(s).Create(ctx, user, p.ID, 3000, "key-1")
		if err != nil || created || replay.ID != r.ID {
			t.Fatalf("replay=%+v created=%v error=%v", replay, created, err)
		}
		if _, _, err := refunds.NewService(s).Create(ctx, user, p.ID, 3001, "key-1"); !errors.Is(err, domain.ErrIdempotencyConflict) {
			t.Fatal(err)
		}
		if _, err := s.GetRefund(ctx, "other-user", r.ID); !errors.Is(err, domain.ErrRefundNotFound) {
			t.Fatal(err)
		}
		if _, err := s.ListReconciliations(ctx, "other-user", r.ID, 20, 0); !errors.Is(err, domain.ErrRefundNotFound) {
			t.Fatal(err)
		}
		if outboxCount(t, s, r.ID, "refund.created") != 1 {
			t.Fatal("replay created duplicate event")
		}
	})
	t.Run("reject_unpaid_and_excess", func(t *testing.T) {
		user, p := seedPayment(t, s, 10000, domain.PaymentPending)
		if _, _, err := refunds.NewService(s).Create(ctx, user, p.ID, 100, "key"); !errors.Is(err, domain.ErrInvalidPaymentState) {
			t.Fatal(err)
		}
		user, p = seedPayment(t, s, 10000, domain.PaymentSucceeded)
		createRefund(t, s, user, p, 8000, "key-1")
		if _, _, err := refunds.NewService(s).Create(ctx, user, p.ID, 3000, "key-2"); !errors.Is(err, domain.ErrRefundAmountExceeded) {
			t.Fatal(err)
		}
	})
	t.Run("concurrent_idempotency", func(t *testing.T) {
		user, p := seedPayment(t, s, 10000, domain.PaymentSucceeded)
		type result struct {
			id      string
			created bool
			err     error
		}
		ch := make(chan result, 12)
		var wg sync.WaitGroup
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r, created, err := refunds.NewService(s).Create(ctx, user, p.ID, 100, "same-key")
				ch <- result{r.ID, created, err}
			}()
		}
		wg.Wait()
		close(ch)
		createdCount := 0
		id := ""
		for result := range ch {
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.created {
				createdCount++
			}
			if id != "" && id != result.id {
				t.Fatal("multiple refund IDs")
			}
			id = result.id
		}
		if createdCount != 1 || outboxCount(t, s, id, "refund.created") != 1 {
			t.Fatal("duplicate creation")
		}
	})
	t.Run("concurrent_amount_reservation", func(t *testing.T) {
		user, p := seedPayment(t, s, 10000, domain.PaymentSucceeded)
		ch := make(chan error, 10)
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, _, err := refunds.NewService(s).Create(ctx, user, p.ID, 6000, fmt.Sprint("key-", i))
				ch <- err
			}(i)
		}
		wg.Wait()
		close(ch)
		success := 0
		for err := range ch {
			if err == nil {
				success++
			} else if !errors.Is(err, domain.ErrRefundAmountExceeded) {
				t.Fatal(err)
			}
		}
		if success != 1 {
			t.Fatalf("successful refunds=%d", success)
		}
	})
	t.Run("outbox_failure_rolls_back_creation", func(t *testing.T) {
		user, p := seedPayment(t, s, 10000, domain.PaymentSucceeded)
		name := "block_" + user[len(user)-8:]
		_, err := s.pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE outbox_events ADD CONSTRAINT %s CHECK (user_id <> '%s' OR event_type <> 'refund.created') NOT VALID`, quoteIdentifier(name), user))
		if err != nil {
			t.Fatal(err)
		}
		defer s.pool.Exec(ctx, `ALTER TABLE outbox_events DROP CONSTRAINT `+quoteIdentifier(name))
		if _, _, err := refunds.NewService(s).Create(ctx, user, p.ID, 100, "blocked"); err == nil {
			t.Fatal("expected outbox error")
		}
		items, err := s.ListRefunds(ctx, user, refunds.ListFilter{Limit: 100})
		if err != nil || len(items) != 0 {
			t.Fatalf("refund escaped transaction: %v %v", items, err)
		}
	})
	t.Run("state_guard_outbox_and_audit_are_atomic", func(t *testing.T) {
		user, p := seedPayment(t, s, 10000, domain.PaymentSucceeded)
		r := createRefund(t, s, user, p, 100, "state-key")
		claimed := claimSpecific(t, s, r.ID)
		now := time.Now().UTC()
		name := "block_" + r.ID[len(r.ID)-8:]
		_, err := s.pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE outbox_events ADD CONSTRAINT %s CHECK (aggregate_id <> '%s' OR event_type <> 'refund.succeeded') NOT VALID`, quoteIdentifier(name), r.ID))
		if err != nil {
			t.Fatal(err)
		}
		outcome := refunds.Outcome{Status: domain.RefundSucceeded, NextActionAt: now, Reconciliation: &domain.ReconciliationResult{ExternalStatus: "SUCCEEDED", Result: "corrected"}}
		if err := s.ApplyRefundOutcome(ctx, claimed, outcome, now); err == nil {
			t.Fatal("expected outbox constraint failure")
		}
		current, err := s.GetRefund(ctx, user, r.ID)
		if err != nil || current.Status != domain.RefundProcessing {
			t.Fatal("status escaped failed transaction", current, err)
		}
		audit, err := s.ListReconciliations(ctx, user, r.ID, 20, 0)
		if err != nil || len(audit) != 0 {
			t.Fatal("audit escaped failed transaction")
		}
		if _, err := s.pool.Exec(ctx, `ALTER TABLE outbox_events DROP CONSTRAINT `+quoteIdentifier(name)); err != nil {
			t.Fatal(err)
		}
		if err := s.ApplyRefundOutcome(ctx, claimed, outcome, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if outboxCount(t, s, r.ID, "refund.succeeded") != 1 {
			t.Fatal("missing success event")
		}
		audit, err = s.ListReconciliations(ctx, user, r.ID, 20, 0)
		if err != nil || len(audit) != 1 || audit[0].LocalStatus != domain.RefundProcessing || audit[0].ExternalStatus != "SUCCEEDED" {
			t.Fatal(audit, err)
		}
		for _, status := range []string{"PENDING", "FAILED", "PROCESSING"} {
			if _, err := s.pool.Exec(ctx, `UPDATE refunds SET status=$2 WHERE id=$1`, r.ID, status); err == nil {
				t.Fatalf("allowed SUCCEEDED -> %s", status)
			}
		}
		if _, err := s.pool.Exec(ctx, `UPDATE refunds SET amount=amount+1 WHERE id=$1`, r.ID); err == nil {
			t.Fatal("amount changed")
		}
		if err := s.ApplyRefundOutcome(ctx, claimed, outcome, time.Now().UTC()); !errors.Is(err, domain.ErrLeaseLost) {
			t.Fatal("stale writer accepted", err)
		}
	})
	t.Run("expired_lease_fencing", func(t *testing.T) {
		user, p := seedPayment(t, s, 10000, domain.PaymentSucceeded)
		r := createRefund(t, s, user, p, 100, "lease-key")
		old := claimSpecific(t, s, r.ID)
		if _, err := s.pool.Exec(ctx, `UPDATE refunds SET lease_until=$2 WHERE id=$1`, r.ID, time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		fresh, ok, err := s.ClaimRefund(ctx, refunds.ClaimReconcile, time.Now().UTC(), time.Minute, 5)
		if err != nil || !ok || fresh.ID != r.ID {
			t.Fatalf("claim=%+v found=%v err=%v", fresh, ok, err)
		}
		outcome := refunds.Outcome{Status: domain.RefundSucceeded, NextActionAt: time.Now().UTC()}
		if err := s.ApplyRefundOutcome(ctx, old, outcome, time.Now().UTC()); !errors.Is(err, domain.ErrLeaseLost) {
			t.Fatal(err)
		}
		if err := s.ApplyRefundOutcome(ctx, fresh, outcome, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("manual_review_keeps_reservation_and_rechecks", func(t *testing.T) {
		user, p := seedPayment(t, s, 1000, domain.PaymentSucceeded)
		r := createRefund(t, s, user, p, 1000, "review")
		claimed := claimSpecific(t, s, r.ID)
		now := time.Now().UTC()
		err := s.ApplyRefundOutcome(ctx, claimed, refunds.Outcome{Status: domain.RefundProcessing, ManualReview: true, LastError: "unknown", NextActionAt: now, Reconciliation: &domain.ReconciliationResult{ExternalStatus: "UNKNOWN", Result: "manual_review"}}, now)
		if err != nil {
			t.Fatal(err)
		}
		if outboxCount(t, s, r.ID, "refund.manual_review") != 1 {
			t.Fatal("missing review event")
		}
		if _, _, err := refunds.NewService(s).Create(ctx, user, p.ID, 1, "another"); !errors.Is(err, domain.ErrRefundAmountExceeded) {
			t.Fatal(err)
		}
		if _, err := s.RequestRefundRecheck(ctx, "other", r.ID, now); !errors.Is(err, domain.ErrRefundNotFound) {
			t.Fatal(err)
		}
		current, err := s.RequestRefundRecheck(ctx, user, r.ID, now)
		if err != nil || current.ManualReview || current.Status != domain.RefundProcessing {
			t.Fatal(current, err)
		}
		if outboxCount(t, s, r.ID, "refund.recheck_requested") != 1 {
			t.Fatal("missing recheck event")
		}
	})
}

func claimSpecific(t *testing.T, s *Store, id string) domain.Refund {
	t.Helper()
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `UPDATE refunds SET next_action_at=$2 WHERE id=$1`, id, time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	r, ok, err := s.ClaimRefund(ctx, refunds.ClaimSubmit, time.Now().UTC(), time.Minute, 5)
	if err != nil || !ok || r.ID != id {
		t.Fatalf("claim=%+v found=%v err=%v expected=%s", r, ok, err, id)
	}
	return r
}
