package proxy

import (
	"encoding/json"
	"fmt"
	"kiro-proxy/config"
	"kiro-proxy/logger"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultAccountPacingTPM          = 90000
	defaultAccountPacingMinInterval  = 5 * time.Second
	defaultAccountPacingMaxQueueWait = 5 * time.Minute
	defaultSuspiciousCooldown        = time.Hour
)

type accountPaceSlot struct {
	mu        sync.Mutex
	nextStart time.Time
}

type accountPacer struct {
	enabled      bool
	inputTPM     int
	minInterval  time.Duration
	maxQueueWait time.Duration
	slotsMu      sync.Mutex
	slots        map[string]*accountPaceSlot
	now          func() time.Time
	sleep        func(time.Duration)
}

func newAccountPacerFromEnv() *accountPacer {
	return &accountPacer{
		enabled:      envBool("KIRO_ACCOUNT_PACING_ENABLED", true),
		inputTPM:     envPositiveInt("KIRO_ACCOUNT_TPM_LIMIT", defaultAccountPacingTPM),
		minInterval:  time.Duration(envNonNegativeInt("KIRO_ACCOUNT_MIN_INTERVAL_MS", int(defaultAccountPacingMinInterval/time.Millisecond))) * time.Millisecond,
		maxQueueWait: time.Duration(envPositiveInt("KIRO_ACCOUNT_MAX_QUEUE_WAIT_SECONDS", int(defaultAccountPacingMaxQueueWait/time.Second))) * time.Second,
		slots:        make(map[string]*accountPaceSlot),
		now:          time.Now,
		sleep:        time.Sleep,
	}
}

func envBool(name string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return value
}

func envPositiveInt(name string, fallback int) int {
	value := envNonNegativeInt(name, fallback)
	if value <= 0 {
		return fallback
	}
	return value
}

func envNonNegativeInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func suspiciousRateLimitCooldown() time.Duration {
	seconds := envPositiveInt("KIRO_SUSPICIOUS_COOLDOWN_SECONDS", int(defaultSuspiciousCooldown/time.Second))
	return time.Duration(seconds) * time.Second
}

func shouldPaceKiroAccount(account *config.Account) bool {
	if account == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(account.AuthMethod), "external_idp")
}

func estimatePacingInputTokens(payload *KiroPayload, estimatedInputTokens int) int {
	if estimatedInputTokens > 0 {
		return estimatedInputTokens
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil || len(payloadJSON) == 0 {
		return 1
	}
	return (len(payloadJSON) + 3) / 4
}

func (p *accountPacer) pacingInterval(inputTokens int) time.Duration {
	if inputTokens < 1 {
		inputTokens = 1
	}
	interval := time.Duration((int64(inputTokens)*int64(time.Minute) + int64(p.inputTPM) - 1) / int64(p.inputTPM))
	if interval < p.minInterval {
		interval = p.minInterval
	}
	return interval
}

func (p *accountPacer) slot(accountID string) *accountPaceSlot {
	p.slotsMu.Lock()
	defer p.slotsMu.Unlock()
	if slot := p.slots[accountID]; slot != nil {
		return slot
	}
	slot := &accountPaceSlot{}
	p.slots[accountID] = slot
	return slot
}

func (p *accountPacer) clearRateLimitCooldowns() {
	if p == nil {
		return
	}
	p.slotsMu.Lock()
	p.slots = make(map[string]*accountPaceSlot)
	p.slotsMu.Unlock()
}

func (p *accountPacer) do(account *config.Account, payload *KiroPayload, estimatedInputTokens int, call func() error) error {
	if p == nil || !p.enabled || !shouldPaceKiroAccount(account) {
		return call()
	}

	accountID := strings.TrimSpace(account.ID)
	if accountID == "" {
		accountID = strings.TrimSpace(account.MachineId)
	}
	if accountID == "" {
		accountID = strings.TrimSpace(account.Email)
	}
	if accountID == "" {
		return call()
	}

	slot := p.slot(accountID)
	slot.mu.Lock()
	defer slot.mu.Unlock()

	now := p.now()
	if wait := slot.nextStart.Sub(now); wait > 0 {
		if wait > p.maxQueueWait {
			return newLocalPacingError(wait)
		}
		logger.Infof("[AccountPacer] Waiting %s before next Kiro request for %s", wait.Round(time.Second), accountEmailForLog(account))
		p.sleep(wait)
	}

	inputTokens := estimatePacingInputTokens(payload, estimatedInputTokens)
	slot.nextStart = p.now().Add(p.pacingInterval(inputTokens))
	err := call()
	if err == nil || !isRateLimitErrorMessage(err.Error()) {
		return err
	}

	cooldown := rateLimitCooldownForError(err)
	if cooldown <= 0 {
		return err
	}
	blockedUntil := p.now().Add(cooldown)
	if blockedUntil.After(slot.nextStart) {
		slot.nextStart = blockedUntil
	}
	return err
}

func newLocalPacingError(retryAfter time.Duration) error {
	seconds := int((retryAfter + time.Second - 1) / time.Second)
	body := []byte(fmt.Sprintf("account request pacing active; retry in %d seconds", seconds))
	return newKiroHTTPError(429, "local account pacer", body, strconv.Itoa(seconds))
}

func (h *Handler) callKiroAPI(account *config.Account, payload *KiroPayload, callback *KiroStreamCallback, estimatedInputTokens int) error {
	call := func() error {
		return CallKiroAPI(account, payload, callback)
	}
	if h == nil || h.accountPacer == nil {
		return call()
	}
	return h.accountPacer.do(account, payload, estimatedInputTokens, call)
}
