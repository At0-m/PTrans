package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
	"github.com/At0-m/PTrans/internal/refunds"
	"github.com/At0-m/PTrans/internal/service"
)

const (
	maxRequestBodyBytes = 1 << 20
	defaultPageSize     = 20
	defaultPageNumber   = 1
	maxPageSize         = 100
)

type RateLimitDecision struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration
}

type rateLimiter interface {
	Allow(ctx context.Context, scopeKey string, now time.Time) (RateLimitDecision, error)
	Limit() int
}

type HealthChecker interface {
	Ping(ctx context.Context) error
}

type Option func(*API)

func WithHealthChecker(checker HealthChecker) Option {
	return func(api *API) {
		api.healthChecker = checker
	}
}

func WithLogger(logger *slog.Logger) Option {
	return func(api *API) {
		api.logger = logger
	}
}

type userIDContextKey struct{}

type API struct {
	authenticator  TokenAuthenticator
	refundService  *refunds.Service
	paymentService *service.PaymentService
	webhookService *service.WebhookService
	limiter        rateLimiter
	healthChecker  HealthChecker
	logger         *slog.Logger
}

var requestIDSeq atomic.Uint64

type createPaymentRequest struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

type createPaymentResponse struct {
	Payment domain.Payment `json:"payment"`
	Created bool           `json:"created"`
}

type listPaymentsResponse struct {
	Items []domain.Payment `json:"items"`
	Page  int              `json:"page"`
	Size  int              `json:"size"`
	Total int              `json:"total"`
}

type createSubscriptionRequest struct {
	URL    string `json:"url"`
	Active *bool  `json:"active,omitempty"`
}

type setSubscriptionActiveRequest struct {
	Active *bool `json:"active"`
}

func NewRouter(paymentService *service.PaymentService, webhookService *service.WebhookService, limiter rateLimiter, opts ...Option) http.Handler {
	api := &API{
		paymentService: paymentService,
		webhookService: webhookService,
		limiter:        limiter,
		logger:         slog.Default(),
	}
	for _, opt := range opts {
		opt(api)
	}
	if api.logger == nil {
		api.logger = slog.Default()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", api.handleLive)
	mux.HandleFunc("GET /readyz", api.handleReady)
	mux.HandleFunc("GET /healthz", api.handleReady)
	mux.Handle("GET /v1/payments", api.requireUser(http.HandlerFunc(api.handleListPayments)))
	mux.Handle("POST /v1/payments", api.requireUser(api.withRateLimit("payments:create", http.HandlerFunc(api.handleCreatePayment))))
	mux.Handle("GET /v1/payments/{id}", api.requireUser(http.HandlerFunc(api.handleGetPayment)))
	mux.Handle("POST /v1/payments/{id}/cancel", api.requireUser(api.withRateLimit("payments:cancel", http.HandlerFunc(api.handleCancelPayment))))
	mux.Handle("GET /v1/webhooks/subscriptions", api.requireUser(http.HandlerFunc(api.handleListSubscriptions)))
	mux.Handle("POST /v1/webhooks/subscriptions", api.requireUser(api.withRateLimit("webhooks:create", http.HandlerFunc(api.handleCreateSubscription))))
	mux.Handle("GET /v1/webhooks/subscriptions/{id}", api.requireUser(http.HandlerFunc(api.handleGetSubscription)))
	mux.Handle("PATCH /v1/webhooks/subscriptions/{id}", api.requireUser(api.withRateLimit("webhooks:update", http.HandlerFunc(api.handlePatchSubscription))))

	api.registerRefunds(mux)
	return api.withRequestLogging(withCORS(mux))
}

func (api *API) handleLive(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (api *API) handleReady(w http.ResponseWriter, r *http.Request) {
	if api.healthChecker == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := api.healthChecker.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "unavailable",
			"error":  "database ping failed",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (api *API) handleCreatePayment(w http.ResponseWriter, r *http.Request) {
	var req createPaymentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	payment, created, err := api.paymentService.CreatePayment(r.Context(), service.CreatePaymentInput{
		UserID:         userIDFromContext(r.Context()),
		Amount:         req.Amount,
		Currency:       req.Currency,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, createPaymentResponse{Payment: payment, Created: created})
}

func (api *API) handleListPayments(w http.ResponseWriter, r *http.Request) {
	page, err := parsePositiveIntQuery(r, "page", defaultPageNumber)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	size, err := parsePositiveIntQuery(r, "size", defaultPageSize)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if size > maxPageSize {
		writeServiceError(w, domain.ErrInvalidPagination)
		return
	}

	status := r.URL.Query().Get("status")
	payments, total, err := api.paymentService.ListPayments(r.Context(), userIDFromContext(r.Context()), status, page, size)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listPaymentsResponse{Items: payments, Page: page, Size: size, Total: total})
}

func (api *API) handleGetPayment(w http.ResponseWriter, r *http.Request) {
	payment, err := api.paymentService.GetPayment(r.Context(), userIDFromContext(r.Context()), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payment)
}

func (api *API) handleCancelPayment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	userID := userIDFromContext(r.Context())
	if err := api.paymentService.CancelPayment(r.Context(), userID, id); err != nil {
		writeServiceError(w, err)
		return
	}
	payment, err := api.paymentService.GetPayment(r.Context(), userID, id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, payment)
}

func (api *API) handleListSubscriptions(w http.ResponseWriter, r *http.Request) {
	items, err := api.webhookService.ListSubscriptions(r.Context(), userIDFromContext(r.Context()))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (api *API) handleCreateSubscription(w http.ResponseWriter, r *http.Request) {
	var req createSubscriptionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	subscription, err := api.webhookService.CreateSubscription(r.Context(), service.CreateWebhookSubscriptionInput{
		UserID: userIDFromContext(r.Context()),
		URL:    req.URL,
		Active: active,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, subscription)
}

func (api *API) handleGetSubscription(w http.ResponseWriter, r *http.Request) {
	subscription, err := api.webhookService.GetSubscription(r.Context(), userIDFromContext(r.Context()), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, subscription)
}

func (api *API) handlePatchSubscription(w http.ResponseWriter, r *http.Request) {
	var req setSubscriptionActiveRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Active == nil {
		writeError(w, http.StatusBadRequest, "field active is required")
		return
	}
	id := r.PathValue("id")
	userID := userIDFromContext(r.Context())
	if err := api.webhookService.SetSubscriptionActive(r.Context(), userID, id, *req.Active); err != nil {
		writeServiceError(w, err)
		return
	}
	subscription, err := api.webhookService.GetSubscription(r.Context(), userID, id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, subscription)
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()

	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("request body is required")
		}
		return fmt.Errorf("decode request body: %w", err)
	}

	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("request body must contain only one JSON object")
	}
	return nil
}

func parsePositiveIntQuery(r *http.Request, key string, defaultValue int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return defaultValue, nil
	}

	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 1 {
		return 0, domain.ErrInvalidPagination
	}
	return parsed, nil
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrPaymentNotFound), errors.Is(err, domain.ErrSubscriptionNotFound), errors.Is(err, domain.ErrRefundNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, domain.ErrInvalidAmount),
		errors.Is(err, domain.ErrIdempotencyKeyRequired),
		errors.Is(err, domain.ErrInvalidCurrency),
		errors.Is(err, domain.ErrInvalidPagination),
		errors.Is(err, domain.ErrInvalidStatusFilter),
		errors.Is(err, domain.ErrInvalidWebhookURL),
		errors.Is(err, domain.ErrUserIDRequired):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, domain.ErrInvalidPaymentState), errors.Is(err, domain.ErrIdempotencyConflict), errors.Is(err, domain.ErrInvalidRefundState), errors.Is(err, domain.ErrRefundAmountExceeded):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Idempotency-Key, Authorization, X-Request-ID")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (api *API) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if api.authenticator == nil || len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "bearer token required")
			return
		}
		userID, err := api.authenticator.Authenticate(parts[1])
		if err != nil || userID == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		ctx := context.WithValue(r.Context(), userIDContextKey{}, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func userIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(userIDContextKey{}).(string)
	return value
}

func (api *API) withRateLimit(scope string, next http.Handler) http.Handler {
	if api.limiter == nil {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := userIDFromContext(r.Context())
		decision, err := api.limiter.Allow(r.Context(), scope+":"+userID, time.Now().UTC())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}

		if limit := api.limiter.Limit(); limit > 0 {
			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(decision.Remaining))
		}
		if !decision.Allowed {
			retrySeconds := int(decision.RetryAfter.Seconds())
			if retrySeconds < 1 {
				retrySeconds = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retrySeconds))
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":               "rate limit exceeded",
				"retry_after_seconds": retrySeconds,
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
	bytes      int
}

func (w *loggingResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *loggingResponseWriter) Write(data []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	return n, err
}

func (api *API) withRequestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = fmt.Sprintf("req_%d_%d", started.UTC().UnixNano(), requestIDSeq.Add(1))
		}
		w.Header().Set("X-Request-ID", requestID)

		wrapped := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(wrapped, r)

		api.logger.Info("http request completed",
			"request_id", requestID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", wrapped.statusCode,
			"bytes", wrapped.bytes,
			"latency_ms", time.Since(started).Milliseconds(),
		)
	})
}
