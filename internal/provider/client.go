package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/At0-m/PTrans/internal/domain"
)

type Status string

const (
	Succeeded  Status = "SUCCEEDED"
	Failed     Status = "FAILED"
	Processing Status = "PROCESSING"
	Unknown    Status = "UNKNOWN"
	NotFound   Status = "NOT_FOUND"
)

type Result struct {
	OperationID string `json:"operation_id"`
	Status      Status `json:"status"`
	Retryable   bool   `json:"retryable"`
	ErrorCode   string `json:"error_code,omitempty"`
}

type Request struct {
	OperationID string `json:"operation_id"`
	PaymentID   string `json:"payment_id"`
	Amount      int64  `json:"amount"`
	Currency    string `json:"currency"`
}

type Error struct {
	HTTPStatus    int
	RetryAfter    time.Duration
	Permanent     bool
	Configuration bool
	Message       string
}

func (e *Error) Error() string { return e.Message }

type Client struct {
	base string
	key  string
	http *http.Client
}

func NewClient(base, key string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid provider URL")
	}
	if key == "" || timeout <= 0 {
		return nil, errors.New("provider key and positive timeout are required")
	}
	return &Client{base: strings.TrimRight(base, "/"), key: key, http: &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *Client) Submit(ctx context.Context, r domain.Refund) (Result, error) {
	body, err := json.Marshal(Request{OperationID: r.ID, PaymentID: r.PaymentID, Amount: r.Amount, Currency: r.Currency})
	if err != nil {
		return Result{}, err
	}
	return c.call(ctx, http.MethodPost, "/refunds", r.ID, body)
}

func (c *Client) Lookup(ctx context.Context, id string) (Result, error) {
	return c.call(ctx, http.MethodGet, "/refunds/"+url.PathEscape(id), id, nil)
}

func (c *Client) call(ctx context.Context, method, path, id string, body []byte) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Idempotency-Key", id)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return Result{}, &Error{Message: "provider transport error: " + err.Error()}
	}
	defer res.Body.Close()
	if method == http.MethodGet && res.StatusCode == http.StatusNotFound {
		return Result{OperationID: id, Status: NotFound}, nil
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		return Result{}, &Error{
			HTTPStatus:    res.StatusCode,
			RetryAfter:    ParseRetryAfter(res.Header.Get("Retry-After"), time.Now()),
			Permanent:     method == http.MethodPost && (res.StatusCode == 400 || res.StatusCode == 422),
			Configuration: res.StatusCode == 401 || res.StatusCode == 403 || res.StatusCode == 409 || res.StatusCode >= 300 && res.StatusCode < 400,
			Message:       fmt.Sprintf("provider HTTP %d", res.StatusCode),
		}
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return Result{}, &Error{Message: "invalid provider response body"}
	}
	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return Result{}, &Error{Message: "invalid provider JSON"}
	}
	if result.OperationID != id || !validStatus(result.Status) || result.Status != Failed && result.Retryable {
		return Result{}, &Error{Message: "invalid provider operation or status"}
	}
	return result, nil
}

func validStatus(s Status) bool {
	return s == Succeeded || s == Failed || s == Processing || s == Unknown
}

func ParseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > 86400 {
			seconds = 86400
		}
		return time.Duration(seconds) * time.Second
	}
	at, err := http.ParseTime(value)
	if err != nil || !at.After(now) {
		return 0
	}
	delay := at.Sub(now)
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	return delay
}
