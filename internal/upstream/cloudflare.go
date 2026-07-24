// Package upstream — Cloudflare (cf/*) adapter.
//
// onmi-routers adds Cloudflare as a THIRD upstream alongside Grok and
// CodeBuddy. The CF adapter is purpose-built for the 12k-account farm
// scenario: each account is a separate Cloudflare account with its own
// Account ID + API token (Bearer). We front the Workers AI `ai/run/{model}`
// endpoint, which takes a plain chat/completions-style body and returns
// either a JSON object (non-stream) or an SSE stream we translate to the
// OpenAI SSE shape.
//
// Design notes (see project README for the full rationale):
//   - Tokens are STATIC — no OAuth refresh loop (unlike Grok/CB OAuth).
//   - 429 handling is split into two cases:
//       1. Rate-limit burst  (has Retry-After) → short cooldown + retry
//       2. Daily quota exhausted (no Retry-After) → skip until next UTC midnight
//   - 403 / invalid token → permanent disable (coret, never retry).
//   - Round-robin is weighted by remaining daily quota so accounts that are
//     nearly full get picked first, minimising 429s at the edge.
//   - Sticky HTTP/SOCKS5 proxy support is inherited from the upstream pool
//     (scope "cloudflare") — recommended for the 12k-account anti-correlation
//     use case so CF cannot link all traffic to a single egress IP.
package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"foxrouters/internal/db"
)

// ===========================================================================
// CLOUDFLARE KEY MANAGER
// ===========================================================================

// CFKey is one Cloudflare account: Account ID + static API token (Bearer).
// No refresh lifecycle (tokens are long-lived / static).
type CFKey struct {
	AccountID string // CF account id (header cf-account-id)
	Token     string // Bearer token (NEVER logged in full)
	mu        sync.RWMutex
	disabled  bool
	disabledAt time.Time // zero = permanent disable (banned/dead)

	// Daily-quota bookkeeping (best-effort, persisted to Redis).
	// quotaLimit = configured daily cap (0 = unknown → conservative).
	// quotaUsed  = running estimate of today's usage. Reset at UTC midnight.
	quotaLimit float64
	quotaUsed  float64
	quotaDate  string // YYYY-MM-DD of the last quotaUsed reset
	totalReqs  int64
	db         *db.Store
}

// NewCFKeyForTest returns a bare CFKey for whitebox tests.
func NewCFKeyForTest(accountID, token string, opts ...CFKeyOption) *CFKey {
	k := &CFKey{AccountID: accountID, Token: token}
	for _, o := range opts {
		o(k)
	}
	return k
}

// CFKeyOption mutates a test-only CFKey.
type CFKeyOption func(*CFKey)

// WithCFDisabledCooldown marks the key disabled with a cooldown timestamp.
// Zero time = permanent disable.
func WithCFDisabledCooldown(at time.Time) CFKeyOption {
	return func(k *CFKey) { k.disabled = true; k.disabledAt = at }
}

// WithCFQuota seeds the daily-quota counters for tests.
func WithCFQuota(used, limit float64) CFKeyOption {
	return func(k *CFKey) { k.quotaUsed = used; k.quotaLimit = limit }
}

// Stats returns usage estimate, total requests, and disabled flag.
func (k *CFKey) Stats() (float64, int64, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.quotaUsed, k.totalReqs, k.disabled
}

// IsDisabled returns the disabled flag (mutex-safe).
func (k *CFKey) IsDisabled() bool {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.disabled
}

// QuotaRemaining returns the estimated remaining daily quota (0 if unknown).
func (k *CFKey) QuotaRemaining() float64 {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.quotaLimit <= 0 {
		return 0 // unknown — callers treat 0 as "no hint"
	}
	rem := k.quotaLimit - k.quotaUsed
	if rem < 0 {
		return 0
	}
	return rem
}

// CFKeySnapshot is a mutex-safe copy of CFKey state for handlers/metrics.
type CFKeySnapshot struct {
	AccountID  string
	Token      string // masked
	QuotaUsed  float64
	QuotaLimit float64
	QuotaRemain float64
	TotalReqs  int64
	Disabled   bool
	DisabledAt time.Time
}

// Snapshot returns a mutex-safe copy of the key's current state.
func (k *CFKey) Snapshot() CFKeySnapshot {
	k.mu.RLock()
	defer k.mu.RUnlock()
	rem := k.quotaLimit - k.quotaUsed
	if rem < 0 {
		rem = 0
	}
	return CFKeySnapshot{
		AccountID:   k.AccountID,
		Token:       maskCFToken(k.Token),
		QuotaUsed:   k.quotaUsed,
		QuotaLimit:  k.quotaLimit,
		QuotaRemain: rem,
		TotalReqs:   k.totalReqs,
		Disabled:    k.disabled,
		DisabledAt:  k.disabledAt,
	}
}

// DisplayID returns a log/dashboard-safe identifier (masked token).
func (k *CFKey) DisplayID() string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return maskCFToken(k.Token)
}

// maskCFToken masks a CF token for logs/dashboard.
func maskCFToken(tok string) string {
	if len(tok) > 12 {
		return tok[:8] + "..." + tok[len(tok)-4:]
	}
	return tok
}

// AuthHeader returns the Authorization header value for this account.
func (k *CFKey) AuthHeader() string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return "Bearer " + k.Token
}

// toDTO returns a db.CFKeyDTO snapshot under RLock.
func (k *CFKey) toDTO() db.CFKeyDTO {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return db.CFKeyDTO{
		AccountID:  k.AccountID,
		Token:      k.Token,
		QuotaUsed:  k.quotaUsed,
		QuotaLimit: k.quotaLimit,
		QuotaDate:  k.quotaDate,
		TotalReqs:  k.totalReqs,
		Disabled:   k.disabled,
		DisabledAt: k.disabledAt,
	}
}

// recordUsage bumps the daily-quota estimate + total request counter, then
// persists. Midnight reset is handled lazily in Next() (see quotaDate).
func (k *CFKey) recordUsage(amount float64) {
	k.mu.Lock()
	today := time.Now().UTC().Format("2006-01-02")
	if k.quotaDate != today {
		// New UTC day — reset the running estimate.
		k.quotaUsed = 0
		k.quotaDate = today
	}
	k.quotaUsed += amount
	k.totalReqs++
	k.mu.Unlock()
	if k.db != nil {
		saveCFKey(k.db, k.toDTO())
	}
}

// CFKeyManager owns the CF account pool.
type CFKeyManager struct {
	keys []*CFKey
	mu   sync.RWMutex
	next int
	db   *db.Store
}

func NewCFKeyManager(store *db.Store) *CFKeyManager {
	return &CFKeyManager{keys: make([]*CFKey, 0), db: store}
}

// SetKeysForTest replaces the internal slice. Whitebox tests only.
func (km *CFKeyManager) SetKeysForTest(keys []*CFKey) {
	km.mu.Lock()
	defer km.mu.Unlock()
	km.keys = keys
}

// LoadFromRedis loads all CF keys from Redis (single source of truth).
// If Redis is empty (fresh deploy), falls back to CF_API_TOKENS env/file
// as a bootstrap seed, then persists to Redis.
func (km *CFKeyManager) LoadFromRedis() error {
	if km.db == nil || !km.db.Ready() {
		return fmt.Errorf("redis not available")
	}
	redisState, err := km.db.LoadCFKeys()
	if err != nil {
		return err
	}
	if len(redisState) > 0 {
		for accountID, state := range redisState {
			if state["token"] == "" {
				continue
			}
			key := &CFKey{AccountID: accountID, Token: state["token"], db: km.db}
			if v := state["quota_used"]; v != "" {
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					key.quotaUsed = f
				}
			}
			if v := state["quota_limit"]; v != "" {
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					key.quotaLimit = f
				}
			}
			key.quotaDate = state["quota_date"]
			if v := state["total_requests"]; v != "" {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					key.totalReqs = n
				}
			}
			if state["disabled"] == "true" || state["disabled"] == "1" {
				key.disabled = true
				if v := state["disabled_at"]; v != "" {
					if n, err := strconv.ParseInt(v, 10, 64); err == nil {
						if n <= 0 {
							key.disabledAt = time.Time{} // permanent
						} else {
							key.disabledAt = time.Unix(n, 0)
						}
					}
				}
			}
			km.keys = append(km.keys, key)
		}
		slog.Info("loaded CF keys from Redis", "module", "cf", "count", len(km.keys))
		return nil
	}

	// Bootstrap from env/file (first run only). Format: account_id:token, one per line.
	raw := os.Getenv("CF_API_TOKENS")
	if raw == "" {
		if v := os.Getenv("CF_TOKEN_FILE"); v != "" {
			if data, err := os.ReadFile(v); err == nil {
				raw = strings.TrimSpace(string(data))
			}
		} else if data, err := os.ReadFile("./cf-tokens.txt"); err == nil {
			raw = strings.TrimSpace(string(data))
		}
	}
	if raw == "" {
		slog.Warn("no CF tokens found (Redis empty, no env/file bootstrap)", "module", "cf")
		return nil
	}
	seedCount := 0
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// support "account_id:token" or just "token" (account id derived)
		parts := strings.SplitN(line, ":", 2)
		var accountID, token string
		if len(parts) == 2 {
			accountID = strings.TrimSpace(parts[0])
			token = strings.TrimSpace(parts[1])
		} else {
			token = strings.TrimSpace(parts[0])
			accountID = "acct-" + token[:min(8, len(token))]
		}
		if token == "" {
			continue
		}
		key := &CFKey{AccountID: accountID, Token: token, db: km.db}
		km.keys = append(km.keys, key)
		if km.db != nil {
			saveCFKey(km.db, key.toDTO())
		}
		seedCount++
	}
	slog.Info("bootstrapped CF keys from env/file → Redis (first run)", "module", "cf", "count", seedCount)
	return nil
}

// Next returns the next healthy account. Weighted by remaining quota so
// nearly-full accounts are used first; skips permanently disabled and
// daily-exhausted accounts. O(k) hot path, no Redis I/O.
func (km *CFKeyManager) Next() (*CFKey, error) {
	km.mu.Lock()
	defer km.mu.Unlock()
	if len(km.keys) == 0 {
		return nil, fmt.Errorf("no cf keys")
	}
	now := time.Now()
	today := now.UTC().Format("2006-01-02")
	var best *CFKey
	var bestRemain float64 = -1

	for i := 0; i < len(km.keys); i++ {
		idx := (km.next + i) % len(km.keys)
		key := km.keys[idx]
		key.mu.RLock()
		if key.disabled {
			key.mu.RUnlock()
			continue
		}
		// Reset stale quota estimate at UTC midnight.
		if key.quotaDate != today {
			key.quotaDate = today
			key.quotaUsed = 0
		}
		rem := key.quotaLimit - key.quotaUsed
		if key.quotaLimit <= 0 {
			// Unknown limit — treat as neutral candidate.
			rem = 1e9
		}
		if rem <= 0 {
			// Daily exhausted, skip silently (will be re-enabled at midnight
			// by a background worker that clears disabledAt).
			key.mu.RUnlock()
			continue
		}
		key.mu.RUnlock()
		// Pick the candidate with the highest remaining quota (use-first).
		if rem > bestRemain {
			bestRemain = rem
			best = key
		}
	}
	if best != nil {
		km.next = (km.next + 1) % len(km.keys)
		return best, nil
	}
	return nil, fmt.Errorf("all cf keys disabled or daily-exhausted")
}

func (km *CFKeyManager) Len() int {
	km.mu.RLock()
	defer km.mu.RUnlock()
	return len(km.keys)
}

func (km *CFKeyManager) GetAll() []*CFKey {
	km.mu.RLock()
	defer km.mu.RUnlock()
	r := make([]*CFKey, len(km.keys))
	copy(r, km.keys)
	return r
}

// AddKey hot-imports a Cloudflare account (accountID:token) into the pool + Redis.
func (km *CFKeyManager) AddKey(accountID, token string) (added bool, total int) {
	accountID = strings.TrimSpace(accountID)
	token = strings.TrimSpace(token)
	if token == "" {
		return false, km.Len()
	}
	if accountID == "" {
		accountID = "acct-" + token[:min(8, len(token))]
	}
	km.mu.Lock()
	for _, existing := range km.keys {
		if existing.AccountID == accountID {
			n := len(km.keys)
			km.mu.Unlock()
			return false, n
		}
	}
	key := &CFKey{AccountID: accountID, Token: token, db: km.db}
	km.keys = append(km.keys, key)
	total = len(km.keys)
	km.mu.Unlock()
	if km.db != nil {
		saveCFKey(km.db, key.toDTO())
	}
	return true, total
}

// AddKeyRaw accepts "account_id:token" or "token" (id auto-derived).
func (km *CFKeyManager) AddKeyRaw(raw string) (added bool, total int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, km.Len()
	}
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) == 2 {
		return km.AddKey(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
	}
	return km.AddKey("", raw)
}

// ResolveKey resolves a masked token / full token / account id to the full
// AccountID. Returns empty string if not found.
func (km *CFKeyManager) ResolveKey(maskedOrFull string) string {
	km.mu.RLock()
	defer km.mu.RUnlock()
	for _, k := range km.keys {
		if k.AccountID == maskedOrFull || k.Token == maskedOrFull {
			return k.AccountID
		}
		if len(k.Token) > 12 {
			masked := k.Token[:8] + "..." + k.Token[len(k.Token)-4:]
			if masked == maskedOrFull {
				return k.AccountID
			}
		}
	}
	return ""
}

// DeleteKey removes a CF account by AccountID. Returns true if removed.
func (km *CFKeyManager) DeleteKey(accountID string) bool {
	km.mu.Lock()
	for i, k := range km.keys {
		if k.AccountID == accountID {
			km.keys = append(km.keys[:i], km.keys[i+1:]...)
			km.mu.Unlock()
			if km.db != nil {
				km.db.DeleteCFKey(accountID)
			}
			slog.Info("deleted cf key", "module", "cf", "account", accountID)
			return true
		}
	}
	km.mu.Unlock()
	return false
}

// ReenableCooldowns lifts temp cooldowns past CF_COOLDOWN_DURATION, and
// clears the daily-exhausted flag at UTC midnight. Background worker only.
func (km *CFKeyManager) ReenableCooldowns() {
	keys := km.GetAll()
	now := time.Now()
	today := now.UTC().Format("2006-01-02")
	var reenabled []*CFKey
	for _, key := range keys {
		key.mu.Lock()
		changed := false
		// Temp cooldown (rate-limit burst) — lift after duration.
		if key.disabled && !key.disabledAt.IsZero() && now.Sub(key.disabledAt) > CF_COOLDOWN_DURATION {
			key.disabled = false
			changed = true
		}
		// Daily-exhausted: if quotaDate is stale (new UTC day) transparently
		// reset the running estimate so Next() picks it up again.
		if key.quotaDate != today {
			key.quotaDate = today
			key.quotaUsed = 0
			changed = true
		}
		key.mu.Unlock()
		if changed {
			reenabled = append(reenabled, key)
		}
	}
	for _, key := range reenabled {
		if key.db != nil {
			saveCFKey(key.db, key.toDTO())
		}
		slog.Info("re-enabled cf key", "module", "cf", "account", key.AccountID)
	}
}

// CleanupDisabled removes all permanently disabled CF keys (disabledAt is
// zero time). Returns the count of removed keys. Does NOT affect cooldown
// keys (disabledAt set = temporary rate-limit burst).
func (km *CFKeyManager) CleanupDisabled() int {
	km.mu.Lock()
	var removed int
	var kept []*CFKey
	for _, k := range km.keys {
		k.mu.RLock()
		permDisabled := k.disabled && k.disabledAt.IsZero()
		k.mu.RUnlock()
		if permDisabled {
			removed++
			if km.db != nil {
				km.db.DeleteCFKey(k.AccountID)
			}
		} else {
			kept = append(kept, k)
		}
	}
	km.keys = kept
	km.mu.Unlock()
	if removed > 0 {
		slog.Info("cleanup disabled cf keys", "module", "cf", "removed", removed, "remaining", km.Len())
	}
	return removed
}

// ReenableCFWorker is the long-lived goroutine that lifts cooldowns.
func ReenableCFWorker(km *CFKeyManager) {
	km.ReenableCooldowns()
	ticker := time.NewTicker(CF_REENABLE_TICK)
	defer ticker.Stop()
	for range ticker.C {
		km.ReenableCooldowns()
	}
}

// permanentDisable marks a CF key permanently disabled (banned/dead) + persists.
func permanentDisableCF(key *CFKey, reason string) {
	key.mu.Lock()
	key.disabled = true
	key.disabledAt = time.Time{} // permanent
	key.mu.Unlock()
	if key.db != nil {
		saveCFKey(key.db, key.toDTO())
	}
	slog.Warn("cf key disabled (permanent)", "module", "cf", "account", key.AccountID, "reason", reason)
}

// cooldownDisable marks a CF key with a temp cooldown (rate-limit burst) + persists.
func cooldownDisableCF(key *CFKey, reason string) {
	key.mu.Lock()
	key.disabled = true
	key.disabledAt = time.Now()
	key.mu.Unlock()
	if key.db != nil {
		saveCFKey(key.db, key.toDTO())
	}
	slog.Warn("cf key disabled (cooldown)", "module", "cf", "account", key.AccountID, "reason", reason)
}

// ===========================================================================
// CLOUDFLARE PROXY
// ===========================================================================

// cfTransform translates an OpenAI chat/completions body into the Workers AI
// `ai/run/{model}` request shape. Workers AI expects a top-level
// {"messages":[...], "stream":bool} (optionally temperature, max_tokens).
// We keep the model name as-is (routes already stripped the "cf/" prefix).
func cfTransform(body []byte, model string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	// Workers AI uses "max_tokens" (not max_completion_tokens).
	if mt, ok := m["max_completion_tokens"]; ok {
		if _, exists := m["max_tokens"]; !exists {
			m["max_tokens"] = mt
		}
		delete(m, "max_completion_tokens")
	}
	// Ensure stream is set (we always request stream from upstream, then
	// translate to the client's desired mode in ProxyCodeBuddy-style loop).
	m["stream"] = true
	// Remove OpenAI-only fields CF doesn't understand.
	for _, drop := range []string{"user", "n", "logit_bias", "logprobs", "top_logprobs", "response_format", "tool_choice", "parallel_tool_calls"} {
		delete(m, drop)
	}
	return json.Marshal(m)
}

// cfCollectStream reads a Workers AI SSE stream and returns a single OpenAI
// chat.completion JSON (used for non-stream clients).
func cfCollectStream(resp *http.Response, model string, key *CFKey) gin.H {
	defer resp.Body.Close()
	var content strings.Builder
	var finish string
	var usage map[string]any

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Response string `json:"response"`
			Usage    map[string]any `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Response != "" {
			content.WriteString(chunk.Response)
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}
	if finish == "" {
		finish = "stop"
	}
	resp2 := gin.H{
		"id":      "chatcmpl-" + time.Now().Format("20060102150405"),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []gin.H{{
			"index":         0,
			"message":       gin.H{"role": "assistant", "content": content.String()},
			"finish_reason": finish,
		}},
	}
	if usage != nil {
		resp2["usage"] = usage
	}
	return resp2
}

// ProxyCloudflare forwards a chat/completions request to Cloudflare Workers AI.
func ProxyCloudflare(c *gin.Context, body []byte, bodyMap map[string]any, km *CFKeyManager, clientStream bool, hc *HealthChecker) {
	if !hc.CF.CanRequest() {
		hc.CF.RecordRequest(0, fmt.Errorf("circuit open"))
		c.JSON(503, gin.H{"error": "cloudflare upstream circuit breaker open"})
		c.Set("error_msg", "cf circuit breaker open")
		errJSON, _ := json.Marshal(gin.H{"error": "cloudflare upstream circuit breaker open"})
		c.Set("response_body", json.RawMessage(errJSON))
		return
	}

	originalModel, _ := bodyMap["model"].(string)
	model := strings.TrimPrefix(originalModel, "cf/")

	transformed, err := cfTransform(body, model)
	if err != nil {
		c.JSON(400, gin.H{"error": fmt.Sprintf("transform: %v", err)})
		return
	}

	client, proxyID := getClient(upstreamClient, "cloudflare")
	// Bound each CF attempt so a single slow upstream can't hang the client
	// beyond 120s (Workers AI can be slow under load; 12k-account fan-out
	// means one stall must not block the whole request).
	ctx, cancel := context.WithTimeout(c.Request.Context(), 120*time.Second)
	defer cancel()
	total := km.Len()
	var lastResp *http.Response
	var lastKey *CFKey
	reqStart := time.Now()

	for attempt := 0; attempt < total; attempt++ {
		if err := ctx.Err(); err != nil {
			break
		}
		key, err := km.Next()
		if err != nil {
			break
		}

		url := fmt.Sprintf("%s/accounts/%s/ai/run/%s", CF_UPSTREAM_URL, key.AccountID, model)
		req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(transformed))
		req.Header.Set("Authorization", key.AuthHeader())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")

		resp, err := client.Do(req)
		if err != nil {
			markProxyResult(proxyID, err, 0)
			continue
		}
		markProxyResult(proxyID, nil, resp.StatusCode)

		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			// 401/403 = dead/invalid token or account banned → permanent.
			permanentDisableCF(key, fmt.Sprintf("%d %s", resp.StatusCode, truncateLog(string(bodyBytes), 200)))
			continue
		}

		if resp.StatusCode == 429 {
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			retryAfter := parseRetryAfter(string(bodyBytes), resp.Header.Get("Retry-After"))
			if retryAfter > 0 {
				// Rate-limit burst — short cooldown, retry other accounts.
				cooldownDisableCF(key, fmt.Sprintf("429 rate limited (retry-after %s)", retryAfter))
			} else {
				// Daily quota exhausted — skip until UTC midnight.
				cooldownDisableCF(key, "429 daily-quota exhausted, skip until midnight")
			}
			continue
		}

		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			// 4xx besides 401/403/429 = bad request (model not found etc).
			// Don't permanently disable — it's a request error, not a key error.
			cooldownDisableCF(key, fmt.Sprintf("4xx status=%d body=%s", resp.StatusCode, truncateLog(string(bodyBytes), 200)))
			continue
		}

		if resp.StatusCode >= 500 {
			resp.Body.Close()
			hc.CF.RecordRequest(time.Since(reqStart), fmt.Errorf("upstream %d", resp.StatusCode))
			continue
		}

		lastResp = resp
		lastKey = key
		break
	}

	if lastResp == nil {
		c.JSON(503, gin.H{"error": "all cf keys disabled or daily-exhausted"})
		c.Set("error_msg", "all cf keys disabled or daily-exhausted")
		errJSON, _ := json.Marshal(gin.H{"error": "all cf keys disabled or daily-exhausted"})
		c.Set("response_body", json.RawMessage(errJSON))
		return
	}

	hc.CF.RecordRequest(time.Since(reqStart), nil)
	c.Set("upstream_account", lastKey.DisplayID())
	lastKey.recordUsage(1) // 1 request unit; refine with token estimate if desired

	if clientStream {
		defer lastResp.Body.Close()
		for k, v := range lastResp.Header {
			if strings.EqualFold(k, "Content-Encoding") || strings.EqualFold(k, "Content-Length") {
				continue
			}
			for _, vv := range v {
				c.Writer.Header().Add(k, vv)
			}
		}
		c.Writer.WriteHeader(lastResp.StatusCode)
		flusher, _ := c.Writer.(http.Flusher)
		scanner := bufio.NewScanner(lastResp.Body)
		scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
		ctx := c.Request.Context()
		var streamContent strings.Builder
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				lastResp.Body.Close()
				break
			}
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				// Pass through any non-data lines (Workers AI usually only emits data:).
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
				if flusher != nil {
					flusher.Flush()
				}
				break
			}
			var cfChunk struct {
				Response string         `json:"response"`
				Usage    map[string]any `json:"usage"`
			}
			if json.Unmarshal([]byte(data), &cfChunk) == nil {
				if cfChunk.Response != "" {
					streamContent.WriteString(cfChunk.Response)
					openaiChunk := gin.H{
						"id":      "chatcmpl-" + time.Now().Format("20060102150405"),
						"object":  "chat.completion.chunk",
						"created": time.Now().Unix(),
						"model":   originalModel,
						"choices": []gin.H{{
							"index": 0,
							"delta": gin.H{"role": "assistant", "content": cfChunk.Response},
						}},
					}
					if b, err := json.Marshal(openaiChunk); err == nil {
						fmt.Fprintf(c.Writer, "data: %s\n\n", b)
					}
				}
				if cfChunk.Usage != nil {
					if b, err := json.Marshal(gin.H{
						"id":      "chatcmpl-" + time.Now().Format("20060102150405"),
						"object":  "chat.completion.chunk",
						"created": time.Now().Unix(),
						"model":   originalModel,
						"choices": []gin.H{{"index": 0, "delta": gin.H{}, "finish_reason": "stop"}},
						"usage":   cfChunk.Usage,
					}); err == nil {
						fmt.Fprintf(c.Writer, "data: %s\n\n", b)
					}
				}
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		c.Set("output_text", truncateLog(streamContent.String(), 1000))
		respJSON, _ := json.Marshal(gin.H{
			"choices": []gin.H{{"message": gin.H{"role": "assistant", "content": streamContent.String()}, "finish_reason": "stop"}},
			"model":   originalModel,
			"stream":  true,
		})
		c.Set("response_body", json.RawMessage(respJSON))
	} else {
		result := cfCollectStream(lastResp, originalModel, lastKey)
		c.JSON(200, result)
		if respBytes, err := json.Marshal(result); err == nil {
			c.Set("response_body", json.RawMessage(respBytes))
		}
		if choices, ok := result["choices"].([]gin.H); ok && len(choices) > 0 {
			if msg, ok := choices[0]["message"].(gin.H); ok {
				if content, ok := msg["content"].(string); ok {
					c.Set("output_text", truncateLog(content, 1000))
				}
			}
		}
	}
}

// parseRetryAfter extracts a retry-after duration from the response body or
// header. Cloudflare's 429 sometimes carries a "Retry-After" header (seconds)
// or a JSON body with retry info. Returns 0 when not a burst (daily-exhausted).
func parseRetryAfter(body, header string) time.Duration {
	if header != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	// Some CF 429 bodies include "retry_after" or "retryAfter" (seconds).
	var parsed struct {
		RetryAfter float64 `json:"retry_after"`
		RetryAfter2 float64 `json:"retryAfter"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err == nil {
		if parsed.RetryAfter > 0 {
			return time.Duration(parsed.RetryAfter) * time.Second
		}
		if parsed.RetryAfter2 > 0 {
			return time.Duration(parsed.RetryAfter2) * time.Second
		}
	}
	return 0
}

// min is a tiny helper (avoids importing internals just for min).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
