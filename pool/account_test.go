package pool

import (
	"errors"
	"kiro-proxy/config"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestOverageAccountsAreSkippedByDefault(t *testing.T) {
	p := &AccountPool{}
	normal := config.Account{ID: "normal"}
	overLimit := config.Account{ID: "over", UsageCurrent: 10, UsageLimit: 10}

	p.accounts = []config.Account{normal, overLimit}

	for i := 0; i < 5; i++ {
		acc := p.GetNext()
		if acc == nil {
			t.Fatalf("expected an account")
		}
		if acc.ID == "over" {
			t.Fatalf("expected over-limit account to be skipped by default")
		}
	}
}

func TestOverageAccountsCanBeSelectedWhenAllowed(t *testing.T) {
	p := &AccountPool{}
	overLimit := config.Account{
		ID:            "over",
		UsageCurrent:  10,
		UsageLimit:    10,
		OverageStatus: "ENABLED",
	}

	p.accounts = []config.Account{overLimit}

	acc := p.GetNext()
	if acc == nil {
		t.Fatalf("expected allowed overage account")
	}
	if acc.ID != "over" {
		t.Fatalf("expected overage account, got %q", acc.ID)
	}
}

func TestGlobalAllowOverUsageKeepsOverLimitAccountsInReloadedPool(t *testing.T) {
	if err := config.Init(filepath.Join(t.TempDir(), "kiro.db")); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.UpdateAllowOverUsage(true); err != nil {
		t.Fatalf("enable allow over usage: %v", err)
	}
	if err := config.AddAccount(config.Account{ID: "over", Enabled: true, UsageCurrent: 10, UsageLimit: 10}); err != nil {
		t.Fatalf("add account: %v", err)
	}

	p := newTestPool()
	p.Reload()
	acc := p.GetNext()
	if acc == nil {
		t.Fatalf("expected over-limit account when global allow over-usage is enabled")
	}
	if acc.ID != "over" {
		t.Fatalf("expected over account, got %q", acc.ID)
	}
}

func TestUpstreamOverageEnabledOnlyAcceptsEnabledStatus(t *testing.T) {
	if !isUpstreamOverageEnabled(config.Account{OverageStatus: "ENABLED"}) {
		t.Fatalf("expected ENABLED overage status to be accepted")
	}
	if isUpstreamOverageEnabled(config.Account{OverageStatus: "DISABLED"}) {
		t.Fatalf("expected DISABLED overage status to be rejected")
	}
	if isUpstreamOverageEnabled(config.Account{}) {
		t.Fatalf("expected empty overage status to be rejected")
	}
}

func TestGetNextKeepsFiveMinuteTokenAvailable(t *testing.T) {
	p := &AccountPool{}
	account := config.Account{
		ID:          "acct-1",
		AccessToken: "access-token",
		ExpiresAt:   time.Now().Unix() + 300,
	}

	p.accounts = []config.Account{account}

	got := p.GetNext()
	if got == nil {
		t.Fatalf("expected five-minute token to be available")
	}
	if got.ID != account.ID {
		t.Fatalf("expected account %q, got %q", account.ID, got.ID)
	}
}

func TestGetNextKeepsRefreshableNearExpiryTokenAvailable(t *testing.T) {
	p := &AccountPool{}
	account := config.Account{
		ID:           "acct-refreshable",
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Unix() + 60,
	}

	p.accounts = []config.Account{account}

	got := p.GetNext()
	if got == nil {
		t.Fatalf("expected near-expiry account with refresh token to be available")
	}
	if got.ID != account.ID {
		t.Fatalf("expected account %q, got %q", account.ID, got.ID)
	}
}

func TestGetNextSkipsNearExpiryTokenWithoutRefreshToken(t *testing.T) {
	p := &AccountPool{}
	p.accounts = []config.Account{
		{
			ID:          "acct-no-refresh",
			AccessToken: "access-token",
			ExpiresAt:   time.Now().Unix() + 60,
		},
	}

	if got := p.GetNext(); got != nil {
		t.Fatalf("expected near-expiry account without refresh token to be skipped, got %q", got.ID)
	}
}

func TestGetNextRotatesAcrossUsableAccounts(t *testing.T) {
	p := newTestPool(
		config.Account{ID: "a"},
		config.Account{ID: "b"},
		config.Account{ID: "c"},
	)

	var got []string
	for i := 0; i < 6; i++ {
		acc := p.GetNext()
		if acc == nil {
			t.Fatalf("selection %d returned nil", i)
		}
		got = append(got, acc.ID)
	}

	want := []string{"a", "b", "c", "a", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected rotation: got %v want %v", got, want)
		}
	}
}

func TestReloadDoesNotRestartRotationAtFirstAccount(t *testing.T) {
	if err := config.Init(filepath.Join(t.TempDir(), "kiro.db")); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.AddAccount(config.Account{ID: "a", Enabled: true}); err != nil {
		t.Fatalf("add account a: %v", err)
	}
	if err := config.AddAccount(config.Account{ID: "b", Enabled: true}); err != nil {
		t.Fatalf("add account b: %v", err)
	}

	p := newTestPool()
	p.Reload()
	first := p.GetNext()
	if first == nil || first.ID != "a" {
		t.Fatalf("expected first account a, got %#v", first)
	}

	p.Reload()
	second := p.GetNext()
	if second == nil || second.ID != "b" {
		t.Fatalf("expected reload to preserve rotation and select b, got %#v", second)
	}
}

func TestWeightedAccountDoesNotRepeatWhenAnotherUsableAccountExists(t *testing.T) {
	if err := config.Init(filepath.Join(t.TempDir(), "kiro.db")); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.AddAccount(config.Account{ID: "heavy", Enabled: true, Weight: 5}); err != nil {
		t.Fatalf("add heavy account: %v", err)
	}
	if err := config.AddAccount(config.Account{ID: "normal", Enabled: true}); err != nil {
		t.Fatalf("add normal account: %v", err)
	}

	p := newTestPool()
	p.Reload()
	var previous string
	for i := 0; i < 6; i++ {
		acc := p.GetNext()
		if acc == nil {
			t.Fatalf("selection %d returned nil", i)
		}
		if previous != "" && acc.ID == previous {
			t.Fatalf("expected weighted rotation to avoid repeating %q while another account is usable", acc.ID)
		}
		previous = acc.ID
	}
}

func TestGetNextForModelRotatesAcrossMatchingAccounts(t *testing.T) {
	p := newTestPool(
		config.Account{ID: "a"},
		config.Account{ID: "b"},
		config.Account{ID: "c"},
	)
	p.SetModelList("a", []string{"claude-sonnet"})
	p.SetModelList("b", []string{"other-model"})
	p.SetModelList("c", []string{"claude-sonnet"})

	var got []string
	for i := 0; i < 4; i++ {
		acc := p.GetNextForModel("claude-sonnet")
		if acc == nil {
			t.Fatalf("selection %d returned nil", i)
		}
		got = append(got, acc.ID)
	}

	want := []string{"a", "c", "a", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected model rotation: got %v want %v", got, want)
		}
	}
}

func TestSingleUsableAccountCanRepeat(t *testing.T) {
	p := newTestPool(config.Account{ID: "only"})

	first := p.GetNext()
	second := p.GetNext()
	if first == nil || second == nil || first.ID != "only" || second.ID != "only" {
		t.Fatalf("expected single account to remain selectable, got %#v then %#v", first, second)
	}
}

func TestIsAuthFailureRecognizes401And403(t *testing.T) {
	positives := []string{
		"HTTP 401 from server",
		"received 403 Forbidden",
		"bad credentials",
		"invalid_grant",
		"invalid_token",
		"token expired",
		"token has expired",
		"unauthorized",
	}
	for _, msg := range positives {
		if !IsAuthFailure(errors.New(msg)) {
			t.Errorf("IsAuthFailure(%q) = false, want true", msg)
		}
	}
}

func TestIsAuthFailureIgnoresFalsePositives(t *testing.T) {

	negatives := []string{
		"status code 4011 found",
		"error 14013 exceeded",
		"request id req-401abc failed upstream",
		"some random error",
		"status 200 OK",
	}
	for _, msg := range negatives {
		if IsAuthFailure(errors.New(msg)) {
			t.Errorf("IsAuthFailure(%q) = true, want false", msg)
		}
	}
}

func TestIsAuthFailureNilError(t *testing.T) {
	if IsAuthFailure(nil) {
		t.Fatal("IsAuthFailure(nil) = true, want false")
	}
}

func TestIsSuspensionErrorDetectsKnownMessages(t *testing.T) {
	positives := []string{
		"account temporarily_suspended",
		"account temporarily suspended",
		"no available kiro profile",
		"No Available Kiro Profile",
	}
	for _, msg := range positives {
		if !IsSuspensionError(errors.New(msg)) {
			t.Errorf("IsSuspensionError(%q) = false, want true", msg)
		}
	}
}

func TestIsSuspensionErrorIgnoresUnrelatedErrors(t *testing.T) {
	negatives := []string{
		"some other error",
		"unauthorized",
		"429 too many requests",
	}
	for _, msg := range negatives {
		if IsSuspensionError(errors.New(msg)) {
			t.Errorf("IsSuspensionError(%q) = true, want false", msg)
		}
	}
}

func TestIsSuspensionErrorNilError(t *testing.T) {
	if IsSuspensionError(nil) {
		t.Fatal("IsSuspensionError(nil) = true, want false")
	}
}

func newTestPool(accounts ...config.Account) *AccountPool {
	p := &AccountPool{
		cooldowns:   make(map[string]time.Time),
		errorCounts: make(map[string]int),
		modelLists:  make(map[string]map[string]bool),
	}
	p.accounts = accounts
	return p
}

func TestGetNextForModelExcludingSkipsExcludedAccounts(t *testing.T) {
	p := newTestPool(
		config.Account{ID: "a"},
		config.Account{ID: "b"},
	)
	excluded := map[string]bool{"a": true}
	for i := 0; i < 5; i++ {
		acc := p.GetNextForModelExcluding("model", excluded)
		if acc == nil {
			t.Fatal("expected account b, got nil")
		}
		if acc.ID == "a" {
			t.Fatalf("excluded account a was returned on iteration %d", i)
		}
	}
}

func TestGetNextForModelExcludingReturnsNilWhenAllExcluded(t *testing.T) {
	p := newTestPool(config.Account{ID: "only"})
	acc := p.GetNextForModelExcluding("model", map[string]bool{"only": true})
	if acc != nil {
		t.Fatalf("expected nil when only account is excluded, got %q", acc.ID)
	}
}

func TestGetNextForModelExcludingReturnsNilOnEmptyPool(t *testing.T) {
	p := newTestPool()
	acc := p.GetNextForModelExcluding("model", map[string]bool{})
	if acc != nil {
		t.Fatalf("expected nil for empty pool, got %q", acc.ID)
	}
}

func TestReturnedAccountsAreSnapshots(t *testing.T) {
	p := newTestPool(config.Account{
		ID:           "acct",
		AccessToken:  "pool-access",
		RefreshToken: "pool-refresh",
	})

	next := p.GetNext()
	if next == nil {
		t.Fatal("expected account from GetNext")
	}
	next.AccessToken = "request-local-access"
	next.RefreshToken = "request-local-refresh"

	byID := p.GetByID("acct")
	if byID == nil {
		t.Fatal("expected account from GetByID")
	}
	if byID.AccessToken != "pool-access" || byID.RefreshToken != "pool-refresh" {
		t.Fatalf("GetNext returned shared account pointer, pool now has access=%q refresh=%q", byID.AccessToken, byID.RefreshToken)
	}

	p.cooldowns["acct"] = time.Now().Add(time.Minute)
	cooldown := p.GetNext()
	if cooldown != nil {
		t.Fatalf("expected cooled account to remain unavailable, got %#v", cooldown)
	}
}

func TestRecordRateLimitUsesShortCooldown(t *testing.T) {
	t.Setenv("KIRO_RATE_LIMIT_COOLDOWN_ENABLED", "true")
	p := newTestPool(config.Account{ID: "acct"})
	before := time.Now().Add(29 * time.Second)
	p.RecordRateLimit("acct", 0)
	after := time.Now().Add(31 * time.Second)

	p.mu.RLock()
	cooldown := p.cooldowns["acct"]
	p.mu.RUnlock()
	if cooldown.Before(before) || cooldown.After(after) {
		t.Fatalf("expected approximately 30-second cooldown, got %s", cooldown)
	}
	if got := p.GetNext(); got != nil {
		t.Fatalf("expected rate-limited account to be unavailable, got %#v", got)
	}
}

func TestRecordRateLimitAllowsProtectiveCooldown(t *testing.T) {
	t.Setenv("KIRO_RATE_LIMIT_COOLDOWN_ENABLED", "true")
	p := newTestPool(config.Account{ID: "acct"})
	before := time.Now().Add(59 * time.Minute)
	p.RecordRateLimit("acct", time.Hour)
	after := time.Now().Add(61 * time.Minute)

	p.mu.RLock()
	cooldown := p.cooldowns["acct"]
	p.mu.RUnlock()
	if cooldown.Before(before) || cooldown.After(after) {
		t.Fatalf("expected approximately one-hour cooldown, got %s", cooldown)
	}
}

func TestRecordRateLimitDisabledDoesNotCooldown(t *testing.T) {
	t.Setenv("KIRO_RATE_LIMIT_COOLDOWN_ENABLED", "false")
	p := newTestPool(config.Account{ID: "acct"})
	p.cooldowns["acct"] = time.Now().Add(time.Hour)

	p.RecordRateLimit("acct", time.Hour)

	p.mu.RLock()
	_, hasCooldown := p.cooldowns["acct"]
	p.mu.RUnlock()
	if hasCooldown {
		t.Fatal("expected disabled upstream-429 cooldown to clear the account block")
	}
}

func TestRecordSuccessClearsCooldown(t *testing.T) {
	p := newTestPool(config.Account{ID: "acct"})
	p.cooldowns["acct"] = time.Now().Add(time.Hour)
	p.errorCounts["acct"] = 4

	p.RecordSuccess("acct")

	p.mu.RLock()
	_, hasCooldown := p.cooldowns["acct"]
	errorCount := p.errorCounts["acct"]
	p.mu.RUnlock()
	if hasCooldown || errorCount != 0 {
		t.Fatalf("expected success to clear cooldown and errors, cooldown=%v errors=%d", hasCooldown, errorCount)
	}
	if got := p.GetNext(); got == nil || got.ID != "acct" {
		t.Fatalf("expected account to become available after success, got %#v", got)
	}
}

func TestNextCooldownDelayForModel(t *testing.T) {
	p := newTestPool(config.Account{ID: "acct"})
	p.SetModelList("acct", []string{"claude-opus-4.8"})
	p.cooldowns["acct"] = time.Now().Add(20 * time.Second)

	delay := p.NextCooldownDelayForModel("claude-opus-4.8")
	if delay < 19*time.Second || delay > 21*time.Second {
		t.Fatalf("expected cooldown near 20s, got %s", delay)
	}
	if got := p.NextCooldownDelayForModel("claude-sonnet-4.6"); got != 0 {
		t.Fatalf("expected no cooldown for unmatched model, got %s", got)
	}
}

func TestCooldownDelayReturnsAccountCooldown(t *testing.T) {
	p := newTestPool(config.Account{ID: "acct"})
	p.cooldowns["acct"] = time.Now().Add(20 * time.Second)

	delay := p.CooldownDelay("acct")
	if delay < 19*time.Second || delay > 21*time.Second {
		t.Fatalf("expected cooldown near 20s, got %s", delay)
	}
	if got := p.CooldownDelay("missing"); got != 0 {
		t.Fatalf("expected no cooldown for missing account, got %s", got)
	}
}

func TestGetNextAndUpdateTokenCanRunConcurrently(t *testing.T) {
	p := newTestPool(config.Account{
		ID:           "acct",
		AccessToken:  "initial-access",
		RefreshToken: "initial-refresh",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	})

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			p.UpdateToken("acct", "access-"+strconv.Itoa(i), "refresh-"+strconv.Itoa(i), time.Now().Add(time.Hour).Unix())
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			acc := p.GetNext()
			if acc == nil {
				t.Errorf("expected account on iteration %d", i)
				return
			}
			_ = len(acc.AccessToken)
			acc.AccessToken = "request-local-" + strconv.Itoa(i)
			if latest := p.GetByID(acc.ID); latest != nil {
				_ = len(latest.AccessToken)
			}
		}
	}()

	wg.Wait()
}

func TestDisableAccountSetsCooldown(t *testing.T) {

	cfgFile := filepath.Join(t.TempDir(), "kiro.db")
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}

	p := newTestPool()
	p.DisableAccount("test-id", "test reason")

	p.mu.RLock()
	cooldown, ok := p.cooldowns["test-id"]
	p.mu.RUnlock()

	if !ok {
		t.Fatal("expected cooldown to be set after DisableAccount")
	}

	minExpected := time.Now().Add(23 * time.Hour)
	if cooldown.Before(minExpected) {
		t.Fatalf("expected cooldown >= 23h in future, got %v", cooldown)
	}
}

func TestGetNextExcludingSkipsExcludedAccount(t *testing.T) {
	p := &AccountPool{
		accounts: []config.Account{
			{ID: "a", Enabled: true},
			{ID: "b", Enabled: true},
		},
		cooldowns:    make(map[string]time.Time),
		errorCounts:  make(map[string]int),
		modelLists:   make(map[string]map[string]bool),
		currentIndex: ^uint64(0),
	}

	acc := p.GetNextExcluding(map[string]bool{"a": true})
	if acc == nil || acc.ID != "b" {
		t.Fatalf("expected account b, got %#v", acc)
	}
}

func TestGetNextForModelExcludingSkipsExcludedAccount(t *testing.T) {
	p := &AccountPool{
		accounts: []config.Account{
			{ID: "a", Enabled: true},
			{ID: "b", Enabled: true},
		},
		cooldowns:    make(map[string]time.Time),
		errorCounts:  make(map[string]int),
		modelLists:   make(map[string]map[string]bool),
		currentIndex: ^uint64(0),
	}
	p.SetModelList("a", []string{"claude-sonnet-4.5"})
	p.SetModelList("b", []string{"claude-sonnet-4.5"})

	acc := p.GetNextForModelExcluding("claude-sonnet-4.5", map[string]bool{"a": true})
	if acc == nil || acc.ID != "b" {
		t.Fatalf("expected account b, got %#v", acc)
	}
}
