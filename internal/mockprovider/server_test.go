package mockprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
	"github.com/At0-m/PTrans/internal/provider"
)

func TestLostResponsePersistsSuccessAcrossRestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "provider.json")
	mock, err := New("key", file, "timeout-after-success", 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mock.Handler())
	client, err := provider.NewClient(server.URL, "key", 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	r := domain.Refund{ID: "refund_1", PaymentID: "pay_1", Amount: 100, Currency: "RUB"}
	if _, err := client.Submit(context.Background(), r); err == nil {
		t.Fatal("expected timeout")
	}
	server.Close()
	recovered, err := New("key", file, "ok", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	server = httptest.NewServer(recovered.Handler())
	defer server.Close()
	client, _ = provider.NewClient(server.URL, "key", time.Second)
	result, err := client.Lookup(context.Background(), r.ID)
	if err != nil || result.Status != provider.Succeeded {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	result, err = client.Submit(context.Background(), r)
	if err != nil || result.Status != provider.Succeeded || len(recovered.records) != 1 {
		t.Fatal("duplicate operation")
	}
	r.Amount++
	if _, err := client.Submit(context.Background(), r); err == nil {
		t.Fatal("accepted idempotency conflict")
	}
}

func TestConcurrentIdempotentSubmissions(t *testing.T) {
	mock, err := New("key", "", "ok", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mock.Handler())
	defer server.Close()
	client, _ := provider.NewClient(server.URL, "key", time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := client.Submit(context.Background(), domain.Refund{ID: "refund_1", PaymentID: "pay_1", Amount: 100, Currency: "RUB"})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/admin/stats", nil)
	req.Header.Set("Authorization", "Bearer key")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var stats struct {
		Operations int `json:"operations"`
		Succeeded  int `json:"succeeded_operations"`
		Posts      int `json:"post_requests"`
	}
	if err := json.NewDecoder(res.Body).Decode(&stats); err != nil {
		t.Fatal(err)
	}
	if stats.Operations != 1 || stats.Succeeded != 1 || stats.Posts != 20 {
		t.Fatalf("%+v", stats)
	}
}

func TestMockRequiresCredentials(t *testing.T) {
	mock, _ := New("key", "", "ok", time.Second)
	r := httptest.NewRequest(http.MethodGet, "/admin/stats", nil)
	w := httptest.NewRecorder()
	mock.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}
