package refunds

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
	"github.com/At0-m/PTrans/internal/provider"
)

type memoryWorkerRepo struct {
	refund    domain.Refund
	saved     []Outcome
	failSaves int
}

func (m *memoryWorkerRepo) ClaimRefund(_ context.Context, kind ClaimKind, now time.Time, lease time.Duration, maxAttempts int) (domain.Refund, bool, error) {
	r := m.refund
	if r.NextActionAt.After(now) || r.LeaseUntil != nil && r.LeaseUntil.After(now) || r.ManualReview {
		return domain.Refund{}, false, nil
	}
	if kind == ClaimSubmit {
		if r.Status != domain.RefundPending && !(r.Status == domain.RefundFailed && r.Retryable) || r.Attempts >= maxAttempts {
			return domain.Refund{}, false, nil
		}
		r.Status = domain.RefundProcessing
		r.Retryable = false
		r.Attempts++
		r.ProcessingStartedAt = &now
		r.CheckFailures = 0
	} else if r.Status != domain.RefundProcessing {
		return domain.Refund{}, false, nil
	}
	token, err := NewID("lease")
	if err != nil {
		return domain.Refund{}, false, err
	}
	until := now.Add(lease)
	r.LeaseUntil = &until
	r.LeaseToken = token
	r.Version++
	m.refund = r
	return r, true, nil
}

func (m *memoryWorkerRepo) ApplyRefundOutcome(_ context.Context, r domain.Refund, out Outcome, now time.Time) error {
	if m.failSaves > 0 {
		m.failSaves--
		return errors.New("simulated database failure")
	}
	current := m.refund
	if current.LeaseToken != r.LeaseToken || current.Version != r.Version || current.LeaseUntil == nil || !current.LeaseUntil.After(now) {
		return domain.ErrLeaseLost
	}
	if current.Status != out.Status {
		if err := domain.ValidateRefundTransition(current.Status, out.Status); err != nil {
			return err
		}
	}
	current.Status = out.Status
	current.Retryable = out.Retryable
	current.ManualReview = out.ManualReview
	current.LastError = out.LastError
	current.CheckFailures = out.CheckFailures
	current.NextActionAt = out.NextActionAt
	current.LeaseToken = ""
	current.LeaseUntil = nil
	current.Version++
	m.refund = current
	m.saved = append(m.saved, out)
	return nil
}

type answer struct {
	result provider.Result
	err    error
}
type scriptedProvider struct {
	submits []answer
	lookups []answer
	calls   []string
	ids     []string
}

func (s *scriptedProvider) Submit(_ context.Context, r domain.Refund) (provider.Result, error) {
	s.calls = append(s.calls, "POST")
	s.ids = append(s.ids, r.ID)
	if len(s.submits) == 0 {
		return provider.Result{}, errors.New("unexpected POST")
	}
	a := s.submits[0]
	s.submits = s.submits[1:]
	return a.result, a.err
}
func (s *scriptedProvider) Lookup(_ context.Context, id string) (provider.Result, error) {
	s.calls = append(s.calls, "GET")
	s.ids = append(s.ids, id)
	if len(s.lookups) == 0 {
		return provider.Result{}, errors.New("unexpected GET")
	}
	a := s.lookups[0]
	s.lookups = s.lookups[1:]
	return a.result, a.err
}
func result(status provider.Status, retryable bool) answer {
	return answer{result: provider.Result{OperationID: "refund_1", Status: status, Retryable: retryable}}
}
func setup(t *testing.T) (*Worker, *memoryWorkerRepo, *scriptedProvider, *time.Time) {
	t.Helper()
	now := time.Now().UTC()
	repo := &memoryWorkerRepo{refund: domain.Refund{ID: "refund_1", UserID: "alice", PaymentID: "pay_1", Amount: 100, Currency: "RUB", Status: domain.RefundPending, NextActionAt: now}}
	p := &scriptedProvider{}
	w, err := NewWorker(repo, p, slog.New(slog.NewTextHandler(io.Discard, nil)), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	w.now = func() time.Time { return now }
	w.jitter = func(d time.Duration) time.Duration { return d }
	return w, repo, p, &now
}
func step(t *testing.T, fn func(context.Context) (bool, error)) {
	t.Helper()
	ok, err := fn(context.Background())
	if err != nil || !ok {
		t.Fatalf("processed=%v error=%v", ok, err)
	}
}

func TestTimeoutReconcilesBeforeResubmitting(t *testing.T) {
	w, repo, p, clock := setup(t)
	p.submits = []answer{{err: &provider.Error{Message: "timeout"}}}
	p.lookups = []answer{result(provider.Succeeded, false)}
	step(t, w.ProcessOnce)
	if repo.refund.Status != domain.RefundProcessing {
		t.Fatal(repo.refund.Status)
	}
	if ok, err := w.ProcessOnce(context.Background()); ok || err != nil {
		t.Fatalf("unexpected resubmit: %v %v", ok, err)
	}
	*clock = clock.Add(3 * time.Second)
	step(t, w.ReconcileOnce)
	if repo.refund.Status != domain.RefundSucceeded || !reflect.DeepEqual(p.calls, []string{"POST", "GET"}) {
		t.Fatalf("state=%+v calls=%v", repo.refund, p.calls)
	}
	if repo.saved[1].Reconciliation.Result != "corrected" {
		t.Fatal("missing reconciliation audit")
	}
}

func TestHTTP500ChecksNotFoundThenRetriesSameOperation(t *testing.T) {
	w, repo, p, clock := setup(t)
	p.submits = []answer{{err: &provider.Error{HTTPStatus: 500, Message: "HTTP 500"}}, result(provider.Succeeded, false)}
	p.lookups = []answer{result(provider.NotFound, false)}
	step(t, w.ProcessOnce)
	*clock = clock.Add(3 * time.Second)
	step(t, w.ReconcileOnce)
	if repo.refund.Status != domain.RefundFailed || !repo.refund.Retryable {
		t.Fatalf("%+v", repo.refund)
	}
	*clock = clock.Add(3 * time.Second)
	step(t, w.ProcessOnce)
	if !reflect.DeepEqual(p.calls, []string{"POST", "GET", "POST"}) || repo.refund.Status != domain.RefundSucceeded {
		t.Fatalf("calls=%v status=%s", p.calls, repo.refund.Status)
	}
	for _, id := range p.ids {
		if id != "refund_1" {
			t.Fatal("operation identity changed")
		}
	}
}

func TestPermanent400IsNotRetried(t *testing.T) {
	w, repo, p, clock := setup(t)
	p.submits = []answer{{err: &provider.Error{HTTPStatus: 400, Permanent: true, Message: "invalid request"}}}
	step(t, w.ProcessOnce)
	*clock = clock.Add(time.Hour)
	if repo.refund.Status != domain.RefundFailed || repo.refund.Retryable {
		t.Fatalf("%+v", repo.refund)
	}
	ok, err := w.ProcessOnce(context.Background())
	if ok || err != nil {
		t.Fatal("permanent failure retried")
	}
	ok, err = w.ReconcileOnce(context.Background())
	if ok || err != nil {
		t.Fatal("permanent failure reconciled")
	}
}

func TestRetryAfterAndRetryLimit(t *testing.T) {
	w, repo, p, clock := setup(t)
	w.opts.MaxAttempts = 1
	p.submits = []answer{{err: &provider.Error{HTTPStatus: 429, RetryAfter: 17 * time.Second, Message: "rate limited"}}}
	p.lookups = []answer{result(provider.NotFound, false)}
	initial := *clock
	step(t, w.ProcessOnce)
	if repo.refund.NextActionAt.Before(initial.Add(17 * time.Second)) {
		t.Fatal("Retry-After ignored")
	}
	*clock = clock.Add(16 * time.Second)
	if ok, _ := w.ReconcileOnce(context.Background()); ok {
		t.Fatal("checked too early")
	}
	*clock = clock.Add(2 * time.Second)
	step(t, w.ReconcileOnce)
	if repo.refund.Status != domain.RefundProcessing || !repo.refund.ManualReview {
		t.Fatal("retry limit ignored")
	}
}

func TestUnknownGoesToManualReview(t *testing.T) {
	w, repo, p, clock := setup(t)
	w.opts.MaxCheckFailures = 2
	p.submits = []answer{{err: &provider.Error{Message: "timeout"}}}
	p.lookups = []answer{result(provider.Unknown, false), result(provider.Unknown, false)}
	step(t, w.ProcessOnce)
	for i := 0; i < 2; i++ {
		*clock = clock.Add(time.Minute)
		step(t, w.ReconcileOnce)
	}
	if !repo.refund.ManualReview || repo.refund.Status != domain.RefundProcessing {
		t.Fatalf("%+v", repo.refund)
	}
	*clock = clock.Add(time.Hour)
	if ok, _ := w.ProcessOnce(context.Background()); ok {
		t.Fatal("manual review resubmitted")
	}
	if ok, _ := w.ReconcileOnce(context.Background()); ok {
		t.Fatal("manual review not paused")
	}
}

func TestCrashAfterProviderSuccessIsRecoveredByLookup(t *testing.T) {
	w, repo, p, clock := setup(t)
	p.submits = []answer{result(provider.Succeeded, false)}
	p.lookups = []answer{result(provider.Succeeded, false)}
	repo.failSaves = 1
	ok, err := w.ProcessOnce(context.Background())
	if !ok || err == nil {
		t.Fatal("expected failed database save")
	}
	if repo.refund.Status != domain.RefundProcessing {
		t.Fatal("uncommitted state leaked")
	}
	*clock = clock.Add(time.Minute)
	step(t, w.ReconcileOnce)
	if repo.refund.Status != domain.RefundSucceeded || len(p.calls) != 2 || p.calls[1] != "GET" {
		t.Fatalf("state=%s calls=%v", repo.refund.Status, p.calls)
	}
}

func TestStuckOperationQueriesProviderBeforeDecision(t *testing.T) {
	for _, external := range []provider.Status{provider.Processing, provider.Succeeded, provider.Failed} {
		t.Run(string(external), func(t *testing.T) {
			w, repo, p, clock := setup(t)
			p.submits = []answer{result(provider.Processing, false)}
			p.lookups = []answer{result(external, external == provider.Failed)}
			step(t, w.ProcessOnce)
			*clock = clock.Add(11 * time.Minute)
			step(t, w.ReconcileOnce)
			if external == provider.Processing && !repo.refund.ManualReview {
				t.Fatal("stuck processing not escalated")
			}
			if external == provider.Succeeded && repo.refund.Status != domain.RefundSucceeded {
				t.Fatal("success not reconciled")
			}
			if external == provider.Failed && !repo.refund.Retryable {
				t.Fatal("retryable failure not scheduled")
			}
		})
	}
}

func TestProviderLookupErrorsNeverCreateNewRefund(t *testing.T) {
	w, repo, p, clock := setup(t)
	w.opts.MaxCheckFailures = 2
	p.submits = []answer{{err: &provider.Error{Message: "timeout"}}}
	p.lookups = []answer{{err: &provider.Error{HTTPStatus: 500, Message: "HTTP 500"}}, {err: &provider.Error{Message: "timeout"}}}
	step(t, w.ProcessOnce)
	for i := 0; i < 2; i++ {
		*clock = clock.Add(time.Minute)
		step(t, w.ReconcileOnce)
	}
	if !repo.refund.ManualReview || repo.refund.Status != domain.RefundProcessing || !reflect.DeepEqual(p.calls, []string{"POST", "GET", "GET"}) {
		t.Fatalf("%+v calls=%v", repo.refund, p.calls)
	}
}
