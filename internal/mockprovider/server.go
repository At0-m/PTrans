package mockprovider

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/At0-m/PTrans/internal/provider"
)

type Record struct {
	Request provider.Request `json:"request"`
	Result  provider.Result  `json:"result"`
}

type Server struct {
	mu      sync.Mutex
	key     string
	file    string
	mode    string
	delay   time.Duration
	records map[string]Record
	posts   int
	lookups int
}

func New(key, file, mode string, delay time.Duration) (*Server, error) {
	if key == "" || !validMode(mode) || delay <= 0 {
		return nil, errors.New("invalid mock provider configuration")
	}
	s := &Server{key: key, file: file, mode: mode, delay: delay, records: map[string]Record{}}
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil {
			if err := json.Unmarshal(data, &s.records); err != nil {
				return nil, err
			}
			if s.records == nil {
				s.records = map[string]Record{}
			}
		}
	}
	return s, nil
}

func validMode(mode string) bool {
	switch mode {
	case "ok", "processing", "unknown", "invalid", "failed", "retryable-failed", "unavailable", "rate-limit", "timeout-after-success":
		return true
	}
	return false
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /refunds", s.submit)
	mux.HandleFunc("GET /refunds/{id}", s.lookup)
	mux.HandleFunc("POST /admin/mode", s.setMode)
	mux.HandleFunc("POST /admin/refunds/{id}/resolve", s.resolve)
	mux.HandleFunc("GET /admin/stats", s.stats)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			reply(w, 200, map[string]string{"status": "ok"})
			return
		}
		expected := "Bearer " + s.key
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(expected)) != 1 {
			reply(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func decode(w http.ResponseWriter, r *http.Request, target any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	d.DisallowUnknownFields()
	return d.Decode(target)
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	var req provider.Request
	if decode(w, r, &req) != nil || req.OperationID == "" || len(req.OperationID) > 128 || req.PaymentID == "" || req.Amount <= 0 || len(req.Currency) != 3 || r.Header.Get("Idempotency-Key") != req.OperationID {
		reply(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	result, code, delayed := s.submitLocked(req)
	if code == 429 {
		w.Header().Set("Retry-After", "2")
	}
	if code >= 300 {
		reply(w, code, map[string]string{"error": http.StatusText(code)})
		return
	}
	if delayed && !wait(r.Context(), s.delay) {
		return
	}
	reply(w, code, result)
}

func (s *Server) submitLocked(req provider.Request) (provider.Result, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.posts++
	if old, ok := s.records[req.OperationID]; ok {
		if old.Request != req {
			return provider.Result{}, 409, false
		}
		if old.Result.Status != provider.Failed || !old.Result.Retryable {
			return old.Result, 200, false
		}
	}
	switch s.mode {
	case "unavailable":
		return provider.Result{}, 503, false
	case "rate-limit":
		return provider.Result{}, 429, false
	case "invalid":
		return provider.Result{}, 400, false
	}
	result := provider.Result{OperationID: req.OperationID, Status: provider.Succeeded}
	switch s.mode {
	case "processing":
		result.Status = provider.Processing
	case "unknown":
		result.Status = provider.Unknown
	case "failed":
		result.Status = provider.Failed
		result.ErrorCode = "declined"
	case "retryable-failed":
		result.Status = provider.Failed
		result.Retryable = true
		result.ErrorCode = "temporary_decline"
	}
	if err := s.saveLocked(req.OperationID, Record{Request: req, Result: result}); err != nil {
		return provider.Result{}, 500, false
	}
	return result, 200, s.mode == "timeout-after-success"
}

func (s *Server) lookup(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.lookups++
	record, ok := s.records[r.PathValue("id")]
	s.mu.Unlock()
	if !ok {
		reply(w, 404, map[string]string{"error": "not_found"})
		return
	}
	reply(w, 200, record.Result)
}

func (s *Server) setMode(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Mode string `json:"mode"`
	}
	if decode(w, r, &input) != nil || !validMode(input.Mode) {
		reply(w, 400, map[string]string{"error": "invalid mode"})
		return
	}
	s.mu.Lock()
	s.mode = input.Mode
	s.mu.Unlock()
	reply(w, 200, input)
}

func (s *Server) resolve(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Status    provider.Status `json:"status"`
		Retryable bool            `json:"retryable"`
	}
	if decode(w, r, &input) != nil || input.Status != provider.Succeeded && input.Status != provider.Failed || input.Status == provider.Succeeded && input.Retryable {
		reply(w, 400, map[string]string{"error": "invalid resolution"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	record, ok := s.records[id]
	if !ok {
		reply(w, 404, map[string]string{"error": "not_found"})
		return
	}
	if record.Result.Status == provider.Succeeded || record.Result.Status == provider.Failed && !record.Result.Retryable {
		reply(w, 409, map[string]string{"error": "operation is final"})
		return
	}
	record.Result.Status = input.Status
	record.Result.Retryable = input.Retryable
	record.Result.ErrorCode = ""
	if err := s.saveLocked(id, record); err != nil {
		reply(w, 500, map[string]string{"error": "persist failed"})
		return
	}
	reply(w, 200, record.Result)
}

func (s *Server) stats(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	succeeded := 0
	for _, record := range s.records {
		if record.Result.Status == provider.Succeeded {
			succeeded++
		}
	}
	reply(w, 200, map[string]any{"post_requests": s.posts, "lookup_requests": s.lookups, "operations": len(s.records), "succeeded_operations": succeeded, "mode": s.mode})
}

func (s *Server) saveLocked(id string, record Record) error {
	next := make(map[string]Record, len(s.records)+1)
	for key, value := range s.records {
		next[key] = value
	}
	next[id] = record
	if s.file != "" {
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		dir := filepath.Dir(s.file)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		f, err := os.CreateTemp(dir, ".provider-*")
		if err != nil {
			return err
		}
		name := f.Name()
		defer os.Remove(name)
		if _, err = f.Write(raw); err != nil {
			f.Close()
			return err
		}
		if err = f.Sync(); err != nil {
			f.Close()
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		if err = os.Rename(name, s.file); err != nil {
			return err
		}
		directory, err := os.Open(dir)
		if err != nil {
			return err
		}
		err = directory.Sync()
		_ = directory.Close()
		if err != nil {
			return err
		}
	}
	s.records = next
	return nil
}

func wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func ValidMode(mode string) bool { return validMode(strings.TrimSpace(mode)) }
