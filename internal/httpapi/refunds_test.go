package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
	"github.com/At0-m/PTrans/internal/refunds"
	"github.com/At0-m/PTrans/internal/service"
)

type refundRepoStub struct {
	items   map[string]domain.Refund
	failure error
}

func (s *refundRepoStub) CreateRefund(_ context.Context, r domain.Refund) (domain.Refund, bool, error) {
	if s.failure != nil {
		return domain.Refund{}, false, s.failure
	}
	for _, old := range s.items {
		if old.UserID == r.UserID && old.IdempotencyKey == r.IdempotencyKey {
			if old.RequestHash != r.RequestHash {
				return domain.Refund{}, false, domain.ErrIdempotencyConflict
			}
			return old, false, nil
		}
	}
	r.Currency = "RUB"
	s.items[r.ID] = r
	return r, true, nil
}
func (s *refundRepoStub) GetRefund(_ context.Context, user, id string) (domain.Refund, error) {
	r, ok := s.items[id]
	if !ok || r.UserID != user {
		return domain.Refund{}, domain.ErrRefundNotFound
	}
	return r, nil
}
func (s *refundRepoStub) ListRefunds(_ context.Context, user string, f refunds.ListFilter) ([]domain.Refund, error) {
	items := []domain.Refund{}
	for _, r := range s.items {
		if r.UserID == user {
			items = append(items, r)
		}
	}
	return items, nil
}
func (s *refundRepoStub) ListReconciliations(ctx context.Context, user, id string, limit, offset int) ([]domain.ReconciliationResult, error) {
	if _, err := s.GetRefund(ctx, user, id); err != nil {
		return nil, err
	}
	return []domain.ReconciliationResult{}, nil
}
func (s *refundRepoStub) RequestRefundRecheck(ctx context.Context, user, id string, _ time.Time) (domain.Refund, error) {
	r, err := s.GetRefund(ctx, user, id)
	if err != nil {
		return r, err
	}
	if !r.ManualReview {
		return domain.Refund{}, domain.ErrInvalidRefundState
	}
	r.ManualReview = false
	s.items[id] = r
	return r, nil
}

func refundHandler(t *testing.T) (http.Handler, *refundRepoStub) {
	t.Helper()
	payments := newStubRepo()
	repo := &refundRepoStub{items: map[string]domain.Refund{}}
	return NewRouter(service.NewPaymentService(payments), service.NewWebhookService(payments), nil, WithAuthenticator(testAuth(t)), WithRefundService(refunds.NewService(repo))), repo
}
func callAPI(t *testing.T, h http.Handler, method, path, user, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if user != "" {
		r.Header.Set("Authorization", "Bearer "+testToken(t, user))
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestRefundAPIIdempotencyAndOwnership(t *testing.T) {
	h, repo := refundHandler(t)
	first := callAPI(t, h, "POST", "/v1/refunds", "alice", "refund-order-1", `{"payment_id":"pay_1","amount":100}`)
	if first.Code != 202 {
		t.Fatal(first.Code, first.Body.String())
	}
	var body struct {
		Refund domain.Refund `json:"refund"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	again := callAPI(t, h, "POST", "/v1/refunds", "alice", "refund-order-1", `{"payment_id":"pay_1","amount":100}`)
	if again.Code != 200 || len(repo.items) != 1 {
		t.Fatal(again.Code, again.Body.String())
	}
	conflict := callAPI(t, h, "POST", "/v1/refunds", "alice", "refund-order-1", `{"payment_id":"pay_1","amount":101}`)
	if conflict.Code != 409 {
		t.Fatal(conflict.Code)
	}
	for _, path := range []string{"/v1/refunds/" + body.Refund.ID, "/v1/refunds/" + body.Refund.ID + "/reconciliations"} {
		response := callAPI(t, h, "GET", path, "bob", "", "")
		if response.Code != 404 {
			t.Fatal("cross-user access", response.Code)
		}
	}
	request := httptest.NewRequest("GET", "/v1/refunds/"+body.Refund.ID, nil)
	request.Header.Set("Authorization", "Bearer "+testToken(t, "bob"))
	request.Header.Set("X-User-ID", "alice")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 404 {
		t.Fatal("header impersonation")
	}
	unauth := httptest.NewRequest("GET", "/v1/refunds", nil)
	unauth.Header.Set("X-User-ID", "alice")
	response = httptest.NewRecorder()
	h.ServeHTTP(response, unauth)
	if response.Code != 401 {
		t.Fatal("accepted identity header")
	}
}

func TestRefundAPIValidation(t *testing.T) {
	h, repo := refundHandler(t)
	cases := []struct{ key, body string }{
		{"", `{"payment_id":"pay_1","amount":100}`},
		{"key", `{"payment_id":"pay_1","amount":0}`},
		{"key", `{"payment_id":"pay_1","amount":100,"status":"SUCCEEDED"}`},
		{"key", `{"payment_id":"pay_1","amount":1.5}`},
	}
	for _, item := range cases {
		w := callAPI(t, h, "POST", "/v1/refunds", "alice", item.key, item.body)
		if w.Code != 400 {
			t.Fatalf("%s: %d %s", item.body, w.Code, w.Body.String())
		}
	}
	repo.failure = domain.ErrInvalidPaymentState
	w := callAPI(t, h, "POST", "/v1/refunds", "alice", "key", `{"payment_id":"pay_1","amount":100}`)
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	repo.failure = domain.ErrRefundAmountExceeded
	w = callAPI(t, h, "POST", "/v1/refunds", "alice", "key", `{"payment_id":"pay_1","amount":100}`)
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	for _, path := range []string{"/v1/refunds?status=INVALID", "/v1/refunds?size=101", "/v1/refunds?manual_review=invalid", "/v1/refunds?page=999999999999999"} {
		w := callAPI(t, h, "GET", path, "alice", "", "")
		if w.Code != 400 {
			t.Fatal(path, w.Code)
		}
	}
}

func TestRouterWithoutAuthenticatorFailsClosed(t *testing.T) {
	repo := newStubRepo()
	h := NewRouter(service.NewPaymentService(repo), service.NewWebhookService(repo), nil)
	w := callAPI(t, h, "GET", "/v1/payments", "alice", "", "")
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	w = callAPI(t, h, "GET", "/livez", "", "", "")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}
