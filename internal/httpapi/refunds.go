package httpapi

import (
	"net/http"
	"strconv"

	"github.com/At0-m/PTrans/internal/domain"
	"github.com/At0-m/PTrans/internal/refunds"
)

type TokenAuthenticator interface{ Authenticate(string) (string, error) }

func WithAuthenticator(a TokenAuthenticator) Option {
	return func(api *API) { api.authenticator = a }
}

func WithRefundService(s *refunds.Service) Option {
	return func(api *API) { api.refundService = s }
}

func (api *API) registerRefunds(mux *http.ServeMux) {
	if api.refundService == nil {
		return
	}
	mux.Handle("POST /v1/refunds", api.requireUser(api.withRateLimit("refunds:create", http.HandlerFunc(api.handleCreateRefund))))
	mux.Handle("GET /v1/refunds", api.requireUser(http.HandlerFunc(api.handleListRefunds)))
	mux.Handle("GET /v1/refunds/{id}", api.requireUser(http.HandlerFunc(api.handleGetRefund)))
	mux.Handle("GET /v1/refunds/{id}/reconciliations", api.requireUser(http.HandlerFunc(api.handleListReconciliations)))
	mux.Handle("POST /v1/refunds/{id}/recheck", api.requireUser(api.withRateLimit("refunds:recheck", http.HandlerFunc(api.handleRefundRecheck))))
}

func (api *API) handleCreateRefund(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PaymentID string `json:"payment_id"`
		Amount    int64  `json:"amount"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	refund, created, err := api.refundService.Create(r.Context(), userIDFromContext(r.Context()), req.PaymentID, req.Amount, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	status := http.StatusAccepted
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"refund": refund, "created": created})
}

func (api *API) handleGetRefund(w http.ResponseWriter, r *http.Request) {
	refund, err := api.refundService.Get(r.Context(), userIDFromContext(r.Context()), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, refund)
}

func refundPagination(r *http.Request) (int, int, error) {
	page, err := parsePositiveIntQuery(r, "page", 1)
	if err != nil {
		return 0, 0, err
	}
	size, err := parsePositiveIntQuery(r, "size", 20)
	if err != nil || size > 100 || page > 1000000 {
		return 0, 0, domain.ErrInvalidPagination
	}
	return size, (page - 1) * size, nil
}

func (api *API) handleListRefunds(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := refundPagination(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	review := false
	if raw := r.URL.Query().Get("manual_review"); raw != "" {
		review, err = strconv.ParseBool(raw)
		if err != nil {
			writeError(w, 400, "invalid manual_review filter")
			return
		}
	}
	items, err := api.refundService.List(r.Context(), userIDFromContext(r.Context()), refunds.ListFilter{
		PaymentID: r.URL.Query().Get("payment_id"), Status: domain.RefundStatus(r.URL.Query().Get("status")),
		ManualReview: review, Limit: limit, Offset: offset,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (api *API) handleListReconciliations(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := refundPagination(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	items, err := api.refundService.Reconciliations(r.Context(), userIDFromContext(r.Context()), r.PathValue("id"), limit, offset)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (api *API) handleRefundRecheck(w http.ResponseWriter, r *http.Request) {
	refund, err := api.refundService.Recheck(r.Context(), userIDFromContext(r.Context()), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, refund)
}
