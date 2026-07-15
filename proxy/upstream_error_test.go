package proxy

import (
	"encoding/json"
	"io"
	"kiro-proxy/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCallKiroAPIPreservesRateLimitDetails(t *testing.T) {
	if err := config.Init(t.TempDir() + "/kiro.db"); err != nil {
		t.Fatalf("init config: %v", err)
	}
	if err := config.UpdatePreferredEndpoint("kiro"); err != nil {
		t.Fatalf("set preferred endpoint: %v", err)
	}
	if err := config.UpdateEndpointFallback(false); err != nil {
		t.Fatalf("disable endpoint fallback: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"code":"ThrottlingException","message":"token rate exceeded"}`)
	}))
	defer server.Close()

	oldEndpoints := kiroEndpoints
	kiroEndpoints = []kiroEndpoint{{URL: server.URL, Origin: "AI_EDITOR", Name: "rate-test"}}
	defer func() { kiroEndpoints = oldEndpoints }()

	payload := &KiroPayload{ProfileArn: "arn:aws:codewhisperer:us-east-1:1:profile/test"}
	payload.ConversationState.CurrentMessage.UserInputMessage.Content = "hello"
	err := CallKiroAPI(&config.Account{AccessToken: "token"}, payload, nil)
	if err == nil {
		t.Fatal("expected rate-limit error")
	}
	if got := proxyErrorStatus(err); got != http.StatusTooManyRequests {
		t.Fatalf("expected HTTP 429, got %d: %v", got, err)
	}
	if got := retryAfterFromError(err); got != 7*time.Second {
		t.Fatalf("expected Retry-After 7s, got %s", got)
	}
	if !strings.Contains(err.Error(), "ThrottlingException") || !strings.Contains(err.Error(), "token rate exceeded") {
		t.Fatalf("expected upstream body in error, got %v", err)
	}
	if isQuotaErrorMessage(err.Error()) {
		t.Fatalf("transient throttling must not be classified as exhausted quota: %v", err)
	}
	if !isRateLimitErrorMessage(err.Error()) {
		t.Fatalf("expected rate-limit classification: %v", err)
	}
}

func TestSetRetryAfterHeaderDefaultsForRateLimit(t *testing.T) {
	err := newKiroHTTPError(http.StatusTooManyRequests, "gateway", nil, "")
	recorder := httptest.NewRecorder()
	setRetryAfterHeader(recorder, err)
	if got := recorder.Header().Get("Retry-After"); got != "30" {
		t.Fatalf("expected default Retry-After 30, got %q", got)
	}
}

func TestNormalizeUpstreamErrorBodyLimitsAndCompacts(t *testing.T) {
	body, _ := json.Marshal(map[string]string{"message": strings.Repeat("x", maxUpstreamErrorBody+100)})
	got := normalizeUpstreamErrorBody(body)
	if len(got) > maxUpstreamErrorBody {
		t.Fatalf("expected body capped at %d bytes, got %d", maxUpstreamErrorBody, len(got))
	}
}
