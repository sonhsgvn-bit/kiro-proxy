package proxy

import (
	"kiro-proxy/config"
	"kiro-proxy/logger"
	"kiro-proxy/pool"
	"strings"
	"time"
)

type requestRetryPlan struct {
	maxPerAccount     int
	maxPerRequest     int
	backoff           pool.RetryConfig
	rateLimited       bool
	pendingRetryAfter time.Duration
}

func newRequestRetryPlan() requestRetryPlan {
	maxPerAccount, maxPerRequest, baseDelayMs, maxDelayMs := config.GetRetryConfig()
	if maxPerAccount < 1 {
		maxPerAccount = 1
	}
	if maxPerRequest < 1 {
		maxPerRequest = 1
	}
	return requestRetryPlan{
		maxPerAccount: maxPerAccount,
		maxPerRequest: maxPerRequest,
		backoff: pool.RetryConfig{
			MaxPerAccount: maxPerAccount,
			MaxPerRequest: maxPerRequest,
			BaseDelay:     time.Duration(baseDelayMs) * time.Millisecond,
			MaxDelay:      time.Duration(maxDelayMs) * time.Millisecond,
		},
	}
}

func (rp *requestRetryPlan) canRetrySameAccount(err error, accountAttempt, totalAttempts int) bool {
	rp.captureRetryHint(err)
	if err == nil || accountAttempt+1 >= rp.maxPerAccount || totalAttempts >= rp.maxPerRequest {
		return false
	}
	return !isTerminalAccountErrorMessage(err.Error())
}

func (rp *requestRetryPlan) shouldBackoffBeforeNextAccount(err error, totalAttempts int) bool {
	rp.captureRetryHint(err)
	if err == nil || totalAttempts >= rp.maxPerRequest {
		return false
	}
	return !isTerminalAccountErrorMessage(err.Error())
}

func isTerminalAccountErrorMessage(msg string) bool {
	return isQuotaErrorMessage(msg) ||
		isOverageErrorMessage(msg) ||
		isSuspensionErrorMessage(msg) ||
		isProfileUnavailableErrorMessage(msg) ||
		isAuthErrorMessage(msg)
}

func (rp *requestRetryPlan) captureRetryHint(err error) {
	if err == nil || isQuotaErrorMessage(err.Error()) || !isRateLimitErrorMessage(err.Error()) {
		return
	}
	rp.rateLimited = true
	if retryAfter := retryAfterFromError(err); retryAfter > rp.pendingRetryAfter {
		rp.pendingRetryAfter = retryAfter
	}
}

func (rp *requestRetryPlan) waitBeforeRetry(totalAttempts int) {
	delay := rp.backoff.CalculateBackoff(totalAttempts - 1)
	if rp.rateLimited {
		attempt := totalAttempts - 1
		if attempt < 0 {
			attempt = 0
		}
		if attempt > 3 {
			attempt = 3
		}
		rateDelay := 5 * time.Second * time.Duration(1<<uint(attempt))
		if rateDelay > 30*time.Second {
			rateDelay = 30 * time.Second
		}
		if rp.pendingRetryAfter > rateDelay {
			rateDelay = rp.pendingRetryAfter
		}
		if rateDelay > 2*time.Minute {
			rateDelay = 2 * time.Minute
		}
		if rateDelay > delay {
			delay = rateDelay
		}
	}
	rp.rateLimited = false
	rp.pendingRetryAfter = 0
	if delay > 0 {
		time.Sleep(delay)
	}
}

func isQuotaErrorMessage(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "quota exhausted") ||
		strings.Contains(msg, "quota exceeded") ||
		strings.Contains(msg, "quotaexceeded") ||
		strings.Contains(msg, "monthly quota") ||
		strings.Contains(msg, "credit limit exceeded") ||
		strings.Contains(msg, "usage limit exceeded")
}

func isRateLimitErrorMessage(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "http 429") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "ratelimit") ||
		strings.Contains(msg, "throttl")
}

func isOverageErrorMessage(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "402") && strings.Contains(msg, "overage")
}

func isSuspensionErrorMessage(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "temporarily_suspended") ||
		strings.Contains(msg, "temporarily is suspended") ||
		strings.Contains(msg, "account suspended")
}

func isProfileUnavailableErrorMessage(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "no available kiro profile")
}

func isAuthErrorMessage(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "http 401") ||
		strings.Contains(msg, "http 403") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "forbidden") ||
		strings.Contains(msg, "authentication failed") ||
		strings.Contains(msg, "token invalid") ||
		strings.Contains(msg, "token expired") ||
		strings.Contains(msg, "invalid_grant") ||
		strings.Contains(msg, "access token expired") ||
		strings.Contains(msg, "refresh token expired")
}

func (h *Handler) disableAccount(account *config.Account, banStatus, banReason string) {
	if account == nil {
		return
	}

	updatedAccount := *account
	if !updatedAccount.Enabled && updatedAccount.BanStatus == banStatus && updatedAccount.BanReason == banReason {
		return
	}

	updatedAccount.Enabled = false
	updatedAccount.BanStatus = banStatus
	updatedAccount.BanReason = banReason
	updatedAccount.BanTime = time.Now().Unix()

	if err := config.UpdateAccount(account.ID, updatedAccount); err != nil {
		logger.Warnf("[AccountFailover] Failed to disable %s: %v", account.Email, err)
		return
	}

	logger.Warnf("[AccountFailover] Disabled %s: %s", account.Email, banReason)
	h.pool.Reload()
}

func (h *Handler) disableAccountOverage(account *config.Account) {
	if account == nil {
		return
	}

	snap, fetchErr := FetchOverageStatus(account)
	if fetchErr != nil {
		logger.Warnf("[AccountFailover] Failed to refresh overage status for %s: %v", account.Email, fetchErr)
		return
	}
	if persistErr := PersistOverageSnapshot(account.ID, snap); persistErr != nil {
		logger.Warnf("[AccountFailover] Failed to persist overage snapshot for %s: %v", account.Email, persistErr)
		return
	}

	logger.Warnf("[AccountFailover] Refreshed overage status for %s after upstream overage limit error: %s", account.Email, snap.Status)
	h.pool.Reload()
}

func (h *Handler) handleAccountFailure(account *config.Account, err error) {
	if account == nil || err == nil {
		return
	}

	errMsg := err.Error()
	switch {
	case isOverageErrorMessage(errMsg):
		h.disableAccountOverage(account)
		h.pool.RecordError(account.ID, false)
	case isQuotaErrorMessage(errMsg):
		h.pool.RecordError(account.ID, true)
	case isRateLimitErrorMessage(errMsg):
		h.pool.RecordRateLimit(account.ID, retryAfterFromError(err))
	case isSuspensionErrorMessage(errMsg):
		h.disableAccount(account, "BANNED", "AWS temporarily suspended - unusual user activity detected")
	case isProfileUnavailableErrorMessage(errMsg):
		h.pool.RecordError(account.ID, false)
	case isAuthErrorMessage(errMsg):
		h.disableAccount(account, "BANNED", "Authentication failed - token invalid or expired")
	default:
		h.pool.RecordError(account.ID, false)
	}
}
