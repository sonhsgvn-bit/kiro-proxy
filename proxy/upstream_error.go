package proxy

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxUpstreamErrorBody = 4096

type kiroHTTPError struct {
	StatusCode int
	Endpoint   string
	Body       string
	RetryAfter time.Duration
}

func (e *kiroHTTPError) Error() string {
	if e == nil {
		return "Kiro upstream error"
	}
	message := fmt.Sprintf("HTTP %d from %s", e.StatusCode, e.Endpoint)
	if e.Body != "" {
		message += ": " + e.Body
	}
	return message
}

func newKiroHTTPError(status int, endpoint string, body []byte, retryAfterHeader string) error {
	return &kiroHTTPError{
		StatusCode: status,
		Endpoint:   endpoint,
		Body:       normalizeUpstreamErrorBody(body),
		RetryAfter: parseRetryAfter(retryAfterHeader, time.Now()),
	}
}

func normalizeUpstreamErrorBody(body []byte) string {
	if len(body) > maxUpstreamErrorBody {
		body = body[:maxUpstreamErrorBody]
	}
	return strings.Join(strings.Fields(strings.TrimSpace(string(body))), " ")
}

func parseRetryAfter(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(raw); err == nil && retryAt.After(now) {
		return retryAt.Sub(now)
	}
	return 0
}

func proxyErrorStatus(err error) int {
	var upstreamErr *kiroHTTPError
	if errors.As(err, &upstreamErr) && upstreamErr.StatusCode >= 400 && upstreamErr.StatusCode <= 599 {
		return upstreamErr.StatusCode
	}
	if err != nil && isRateLimitErrorMessage(err.Error()) {
		return http.StatusTooManyRequests
	}
	return http.StatusInternalServerError
}

func proxyErrorType(err error, fallback string) string {
	if proxyErrorStatus(err) == http.StatusTooManyRequests {
		return "rate_limit_error"
	}
	return fallback
}

func retryAfterFromError(err error) time.Duration {
	var upstreamErr *kiroHTTPError
	if errors.As(err, &upstreamErr) {
		return upstreamErr.RetryAfter
	}
	return 0
}

func setRetryAfterHeader(w http.ResponseWriter, err error) {
	if w == nil || proxyErrorStatus(err) != http.StatusTooManyRequests {
		return
	}
	retryAfter := retryAfterFromError(err)
	if retryAfter <= 0 {
		retryAfter = 30 * time.Second
	}
	seconds := int((retryAfter + time.Second - 1) / time.Second)
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
}

func (h *Handler) cooldownErrorForModel(model string) error {
	if h == nil || h.pool == nil {
		return nil
	}
	retryAfter := h.pool.NextCooldownDelayForModel(model)
	if retryAfter <= 0 {
		return nil
	}
	seconds := int((retryAfter + time.Second - 1) / time.Second)
	return &kiroHTTPError{
		StatusCode: http.StatusTooManyRequests,
		Endpoint:   "account pool",
		Body:       fmt.Sprintf("all matching accounts are temporarily rate limited; retry in %d seconds", seconds),
		RetryAfter: retryAfter,
	}
}

func (h *Handler) cooldownErrorForAccount(accountID string) error {
	if h == nil || h.pool == nil {
		return nil
	}
	retryAfter := h.pool.CooldownDelay(accountID)
	if retryAfter <= 0 {
		return nil
	}
	seconds := int((retryAfter + time.Second - 1) / time.Second)
	return &kiroHTTPError{
		StatusCode: http.StatusTooManyRequests,
		Endpoint:   "account pool",
		Body:       fmt.Sprintf("account is temporarily rate limited; retry in %d seconds", seconds),
		RetryAfter: retryAfter,
	}
}
