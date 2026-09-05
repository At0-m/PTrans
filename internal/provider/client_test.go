package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
)

func TestClientClassifiesResponses(t *testing.T) {
	for _, code := range []int{400, 422, 429, 500, 503, 401, 403, 409, 302} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Idempotency-Key") != "refund_1" || r.Header.Get("Authorization") != "Bearer key" {
					t.Error("missing provider credentials or key")
				}
				w.Header().Set("Retry-After", "17")
				w.Header().Set("Location", "/redirect")
				w.WriteHeader(code)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, "key", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Submit(context.Background(), domain.Refund{ID: "refund_1", PaymentID: "pay_1", Amount: 100, Currency: "RUB"})
			var pe *Error
			if !errors.As(err, &pe) || pe.HTTPStatus != code || pe.Permanent != (code == 400 || code == 422) || pe.RetryAfter != 17*time.Second {
				t.Fatalf("unexpected error: %+v", err)
			}
		})
	}
}

func TestClientLookupNotFoundAndMalformed(t *testing.T) {
	for _, body := range []string{`{}`, `{"operation_id":"wrong","status":"SUCCEEDED"}`, `{"operation_id":"refund_1","status":"ALIEN"}`, `bad-json`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			client, _ := NewClient(server.URL, "key", time.Second)
			if _, err := client.Lookup(context.Background(), "refund_1"); err == nil {
				t.Fatal("accepted malformed result")
			}
		})
	}
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client, _ := NewClient(server.URL, "key", time.Second)
	result, err := client.Lookup(context.Background(), "refund_1")
	if err != nil || result.Status != NotFound {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestClientTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	client, _ := NewClient(server.URL, "key", 20*time.Millisecond)
	_, err := client.Lookup(context.Background(), "refund_1")
	var pe *Error
	if !errors.As(err, &pe) || pe.Permanent {
		t.Fatalf("timeout must be ambiguous: %v", err)
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	tests := map[string]time.Duration{"12": 12 * time.Second, "-1": 0, "invalid": 0, "99999999999999": 24 * time.Hour, now.Add(20 * time.Second).Format(http.TimeFormat): 20 * time.Second}
	for value, want := range tests {
		if got := ParseRetryAfter(value, now); got != want {
			t.Fatalf("%q: got %v want %v", value, got, want)
		}
	}
}
