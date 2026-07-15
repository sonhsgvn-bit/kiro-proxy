package proxy

import (
	"kiro-proxy/config"
	"testing"
	"time"
)

func newTestAccountPacer(now *time.Time) *accountPacer {
	return &accountPacer{
		enabled:      true,
		inputTPM:     90000,
		minInterval:  5 * time.Second,
		maxQueueWait: 5 * time.Minute,
		slots:        make(map[string]*accountPaceSlot),
		now:          func() time.Time { return *now },
		sleep:        func(delay time.Duration) { *now = now.Add(delay) },
	}
}

func TestAccountPacerSpacesLargeExternalIDPRequests(t *testing.T) {
	now := time.Unix(1000, 0)
	pacer := newTestAccountPacer(&now)
	account := &config.Account{ID: "acct", AuthMethod: "external_idp"}
	calls := 0

	call := func() error {
		calls++
		return nil
	}
	if err := pacer.do(account, &KiroPayload{}, 45000, call); err != nil {
		t.Fatalf("first paced call failed: %v", err)
	}
	firstFinishedAt := now
	if err := pacer.do(account, &KiroPayload{}, 45000, call); err != nil {
		t.Fatalf("second paced call failed: %v", err)
	}

	if calls != 2 {
		t.Fatalf("expected two upstream calls, got %d", calls)
	}
	if got := now.Sub(firstFinishedAt); got != 30*time.Second {
		t.Fatalf("expected 45k tokens at 90k TPM to wait 30s, got %s", got)
	}
}

func TestAccountPacerStopsQueueAfterSuspiciousRateLimit(t *testing.T) {
	t.Setenv("KIRO_SUSPICIOUS_COOLDOWN_SECONDS", "3600")
	now := time.Unix(1000, 0)
	pacer := newTestAccountPacer(&now)
	account := &config.Account{ID: "acct", AuthMethod: "external_idp"}
	upstreamCalls := 0
	suspiciousErr := newKiroHTTPError(429, "Kiro Gateway", []byte(`{"reason":"USER_REQUEST_RATE_EXCEEDED","message":"suspicious activity"}`), "")

	if err := pacer.do(account, &KiroPayload{}, 100, func() error {
		upstreamCalls++
		return suspiciousErr
	}); err == nil {
		t.Fatal("expected upstream suspicious rate limit")
	}

	err := pacer.do(account, &KiroPayload{}, 100, func() error {
		upstreamCalls++
		return nil
	})
	if err == nil || proxyErrorStatus(err) != 429 {
		t.Fatalf("expected local 429 while protective cooldown is active, got %v", err)
	}
	if upstreamCalls != 1 {
		t.Fatalf("expected queued request to be rejected locally, upstream calls=%d", upstreamCalls)
	}
	if got := retryAfterFromError(err); got < 59*time.Minute {
		t.Fatalf("expected retry-after near one hour, got %s", got)
	}
}

func TestAccountPacerOnlyTargetsExternalIDP(t *testing.T) {
	if shouldPaceKiroAccount(&config.Account{AuthMethod: "api_key"}) {
		t.Fatal("API-key account should not be paced by the external IdP limiter")
	}
	if !shouldPaceKiroAccount(&config.Account{AuthMethod: "External_IdP"}) {
		t.Fatal("external IdP account should be paced case-insensitively")
	}
}
