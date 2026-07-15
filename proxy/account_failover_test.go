package proxy

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"kiro-proxy/config"
	"kiro-proxy/db"
	accountpool "kiro-proxy/pool"
)

func TestAccountFailureClassifiers(t *testing.T) {
	tests := []struct {
		name string
		fn   func(string) bool
		msg  string
	}{
		{name: "quota", fn: isQuotaErrorMessage, msg: "HTTP 429: quota exhausted"},
		{name: "rate limit", fn: isRateLimitErrorMessage, msg: "HTTP 429: ThrottlingException"},
		{name: "overage", fn: isOverageErrorMessage, msg: "HTTP 402 from Kiro IDE: OVERAGE limit exceeded"},
		{name: "suspension", fn: isSuspensionErrorMessage, msg: "Your User ID temporarily is suspended"},
		{name: "profile", fn: isProfileUnavailableErrorMessage, msg: "no available Kiro profile"},
		{name: "auth", fn: isAuthErrorMessage, msg: "Authentication failed - token invalid or expired"},
	}

	for _, tc := range tests {
		if !tc.fn(tc.msg) {
			t.Fatalf("%s classifier did not match %q", tc.name, tc.msg)
		}
	}
}

func TestGeneric429IsNotRetriedOnSameAccount(t *testing.T) {
	err := newKiroHTTPError(429, "Kiro Gateway", []byte(`{"code":"ThrottlingException"}`), "9")
	if isQuotaErrorMessage(err.Error()) {
		t.Fatalf("generic 429 must not be treated as exhausted monthly quota: %v", err)
	}

	plan := newRequestRetryPlan()
	if plan.canRetrySameAccount(err, 0, 1) {
		t.Fatalf("rate-limited account must not be retried in the same request")
	}
	if !plan.rateLimited || plan.pendingRetryAfter != 9*time.Second {
		t.Fatalf("expected retry hint to be captured, rateLimited=%v retryAfter=%s", plan.rateLimited, plan.pendingRetryAfter)
	}
}

func TestSuspiciousRateLimitUsesProtectiveCooldown(t *testing.T) {
	t.Setenv("KIRO_SUSPICIOUS_COOLDOWN_SECONDS", "3600")
	err := newKiroHTTPError(429, "Kiro Gateway", []byte(`{
		"message":"Due to suspicious activity, we are imposing temporary limits on how frequently your account can send a request",
		"reason":"USER_REQUEST_RATE_EXCEEDED"
	}`), "")

	if !isSuspiciousRateLimitErrorMessage(err.Error()) {
		t.Fatalf("expected suspicious activity rate limit to be classified: %v", err)
	}
	if got := rateLimitCooldownForError(err); got != time.Hour {
		t.Fatalf("expected one-hour protective cooldown, got %s", got)
	}
}

func TestProfileUnavailableFailureDoesNotDisableAccount(t *testing.T) {
	dir := t.TempDir()
	if err := db.ResetForTest(dir); err != nil {
		t.Fatalf("reset db: %v", err)
	}
	if err := config.Init(filepath.Join(dir, "kiro.db")); err != nil {
		t.Fatalf("config init: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	account := config.Account{
		ID:      "acct-profile",
		Email:   "profile@example.com",
		Enabled: true,
	}
	if err := config.AddAccount(account); err != nil {
		t.Fatalf("add account: %v", err)
	}

	p := accountpool.GetPool()
	p.Reload()
	h := &Handler{pool: p}
	h.handleAccountFailure(&account, errors.New("no available Kiro profile"))

	accounts := config.GetAccounts()
	if len(accounts) != 1 {
		t.Fatalf("expected one account, got %#v", accounts)
	}
	if !accounts[0].Enabled {
		t.Fatalf("profile lookup failure should not disable account: %#v", accounts[0])
	}
	if accounts[0].BanStatus != "" || accounts[0].BanReason != "" {
		t.Fatalf("profile lookup failure should not mark ban status: %#v", accounts[0])
	}
}

func TestTransientProfileFetchErrorClassifier(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "empty list", err: errors.New("empty profile list"), want: false},
		{name: "http 400", err: errors.New("HTTP 400: bad request"), want: false},
		{name: "http 429", err: errors.New("HTTP 429: rate limited"), want: true},
		{name: "http 500", err: errors.New("HTTP 500: upstream"), want: true},
		{name: "network", err: errors.New("dial tcp: i/o timeout"), want: true},
	}

	for _, tc := range tests {
		if got := isTransientProfileFetchError(tc.err); got != tc.want {
			t.Fatalf("%s: expected %v, got %v", tc.name, tc.want, got)
		}
	}
}
