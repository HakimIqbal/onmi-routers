// Package proxy wires the HTTP entrypoint (/v1/chat/completions, /v1/models)
// to the correct upstream — Grok or CodeBuddy — and emits Prometheus metrics
// + async ClickHouse audit rows for every proxied call.
//
// Dependencies:
//   - internal/upstream  (isGrokModel, expandGrokAlias, proxyGrok, proxyCodeBuddy, MAX_REQUEST_BODY)
//   - internal/db        (RequestLog DTO, Store.LogRequest)
//   - internal/auth      (Manager.Get / IsModelAllowed / IncrementTokens / IncrementRequests)
//   - internal/metrics   (RequestsTotal, RequestDuration)
package proxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"foxrouters/internal/auth"
	"foxrouters/internal/cache"
	"foxrouters/internal/compression"
	"foxrouters/internal/cost"
	"foxrouters/internal/db"
	"foxrouters/internal/guardrails"
	"foxrouters/internal/metrics"
	"foxrouters/internal/quota"
	"foxrouters/internal/rtk"
	"foxrouters/internal/tokensaver"
	"foxrouters/internal/upstream"

	"github.com/gin-gonic/gin"
)

// TokenSaver is the shared Token Saver config (RTK input + Caveman/CodeStyle
// output directives). Set via main.go (from Redis/env). Default: RTK on.
var TokenSaver = tokensaver.DefaultConfig()

// RTKEnabled gates only the input-compression half. Kept for back-compat
// with env var; prefer TokenSaver.Get().RTK.
var RTKEnabled = os.Getenv("RTK_ENABLED") != "false"

// SemanticCache stores recent non-stream chat responses (exact key).
var SemanticCache = cache.New(5*time.Minute, 512)

// recordCompression logs a compression result into the analytics tracker so
// the dashboard can show real measured token savings.
func recordCompression(stats *compression.Stats) {
	compression.Record(stats)
}

// QuotaTracker tracks live pool/spend per upstream family.
var QuotaTracker = quota.New()

// ============================================================================
// UTILITY FUNCTIONS
// ============================================================================

// extractInputText: extract last user message from request body for logging.
// Truncates to 500 chars to avoid bloating the DB.
func extractInputText(bodyMap map[string]any) string {
	msgs, ok := bodyMap["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return ""
	}
	// Find last user message
	for i := len(msgs) - 1; i >= 0; i-- {
		msg, ok := msgs[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "user" {
			continue
		}
		content := msg["content"]
		switch v := content.(type) {
		case string:
			return upstream.TruncateLog(v, 500)
		case []any:
			// Array of content parts (vision etc.) — extract text parts
			var parts []string
			for _, p := range v {
				pm, ok := p.(map[string]any)
				if !ok {
					continue
				}
				if pt, _ := pm["type"].(string); pt == "text" {
					if txt, ok := pm["text"].(string); ok {
						parts = append(parts, txt)
					}
				}
			}
			if len(parts) > 0 {
				return upstream.TruncateLog(strings.Join(parts, " "), 500)
			}
		}
	}
	return ""
}

// toInt safely converts interface{} from c.Get() to int.
func toInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// toFloat extracts a numeric value (JSON numbers arrive as float64) into a
// float64. Returns (0, false) when the value isn't numeric.
func toFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// toFloatNeurons extracts the Cloudflare "neurons" billing value into float64.
func toFloatNeurons(v interface{}) float64 {
	if f, ok := toFloat(v); ok {
		return f
	}
	return 0
}

// toString safely converts interface{} from c.Get() to string.
func toString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// ============================================================================
// MAIN HANDLER
// ============================================================================

// ProxyRequest routes /v1/chat/completions to Grok or CodeBuddy based on the
// requested model, expands Grok aliases, enforces per-key model whitelists,
// records Prometheus metrics, updates per-key token quotas, and emits an
// async RequestLog to ClickHouse for chat completions only.
//
// The optional `registry` argument (may be nil) resolves runtime-configured
// custom models + aliases (see internal/proxy/custom.go). Aliases are
// rewritten in-body before routing; custom models override the default
// grok-* / cb/* prefix routing.
//
// The optional `combos` argument (may be nil) resolves "combo/<name>"
// virtual models into concrete backend models. See internal/proxy/combo.go.
// Fallback combos retry on 5xx by buffering the upstream response through a
// httptest-style recorder and only flushing to the real writer on success
// or list exhaustion.
func ProxyRequest(grokAM *upstream.GrokAccountManager, cbKM *upstream.CBKeyManager, hc *upstream.HealthChecker, authMgr *auth.Manager, registry *CustomRegistry, combos *ComboRegistry, cfKM *upstream.CFKeyManager, store *db.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path

		// /v1/models — local
		if path == "/v1/models" || path == "/models" {
			models := []gin.H{
				// Grok models
				{"id": "grok-4.5", "object": "model", "owned_by": "xai"},
				{"id": "grok-4.5-high", "object": "model", "owned_by": "xai"},
				{"id": "grok-4.5-medium", "object": "model", "owned_by": "xai"},
				{"id": "grok-4.5-low", "object": "model", "owned_by": "xai"},
				{"id": "grok-4.5-xhigh", "object": "model", "owned_by": "xai"},
				{"id": "grok-4.5-auto", "object": "model", "owned_by": "xai"},
				{"id": "grok-4.5-none", "object": "model", "owned_by": "xai"},
				{"id": "grok-4", "object": "model", "owned_by": "xai"},
				{"id": "grok-4-fast-reasoning", "object": "model", "owned_by": "xai"},
				{"id": "grok-code-fast-1", "object": "model", "owned_by": "xai"},
				// CodeBuddy — GPT
				{"id": "cb/gpt-5.6-sol", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5.6-terra", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5.6-luna", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5.5", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5.4", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5.2", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5.1", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-4.1", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5.3-codex", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5.1-codex", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5.1-codex-mini", "object": "model", "owned_by": "codebuddy"},
				// CodeBuddy — Claude
				{"id": "cb/claude-opus-4.7-1m", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/claude-opus-4.6", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/claude-sonnet-4.6", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/claude-haiku-4.5", "object": "model", "owned_by": "codebuddy"},
				// CodeBuddy — Gemini
				{"id": "cb/gemini-3.1-pro", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gemini-3.5-flash", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gemini-3.0-flash", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gemini-2.5-pro", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gemini-2.5-flash", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gemini-3.1-flash-lite", "object": "model", "owned_by": "codebuddy"},
				// CodeBuddy — OpenAI Reasoning
				{"id": "cb/o3", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/o4-mini", "object": "model", "owned_by": "codebuddy"},
				// CodeBuddy — GLM
				{"id": "cb/glm-5.2", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/glm-5.1", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/glm-5.0", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/glm-4.6", "object": "model", "owned_by": "codebuddy"},
				// CodeBuddy — DeepSeek
				{"id": "cb/deepseek-v3", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/deepseek-v3.2", "object": "model", "owned_by": "codebuddy"},
				// CodeBuddy — Kimi
				{"id": "cb/kimi-k2.5", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/kimi-k3", "object": "model", "owned_by": "codebuddy"},
				// CodeBuddy — Default
				{"id": "cb/default-model", "object": "model", "owned_by": "codebuddy"},
				// Cloudflare Workers AI — full LLM catalog from
				// https://developers.cloudflare.com/workers-ai/platform/pricing/#llm-model-pricing
				// (official list prices; ids are the Workers AI model slugs).
				{"id": "@cf/meta/llama-3.2-1b-instruct", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/meta/llama-3.2-3b-instruct", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/meta/llama-3.1-8b-instruct-fp8-fast", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/meta/llama-3.2-11b-vision-instruct", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/meta/llama-3.1-70b-instruct-fp8-fast", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/meta/llama-3.3-70b-instruct-fp8-fast", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/deepseek-ai/deepseek-r1-distill-qwen-32b", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/mistral/mistral-7b-instruct-v0.1", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/mistralai/mistral-small-3.1-24b-instruct", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/meta/llama-3.1-8b-instruct", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/meta/llama-3.1-8b-instruct-fp8", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/meta/llama-3.1-8b-instruct-awq", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/meta/llama-3-8b-instruct", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/meta/llama-3-8b-instruct-awq", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/meta/llama-2-7b-chat-fp16", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/meta/llama-guard-3-8b", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/meta/llama-4-scout-17b-16e-instruct", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/google/gemma-3-12b-it", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/google/gemma-4-26b-a4b-it", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/qwen/qwq-32b", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/qwen/qwen2.5-coder-32b-instruct", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/qwen/qwen3-30b-a3b-fp8", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/openai/gpt-oss-120b", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/openai/gpt-oss-20b", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/aisingapore/gemma-sea-lion-v4-27b-it", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/ibm-granite/granite-4.0-h-micro", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/zai-org/glm-4.7-flash", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/zai-org/glm-5.2", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/nvidia/nemotron-3-120b-a12b", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/moonshotai/kimi-k2.5", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/moonshotai/kimi-k2.6", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/moonshotai/kimi-k2.7-code", "object": "model", "owned_by": "cloudflare"},
				// Friendly short aliases → expanded by ExpandCFAlias
				{"id": "cf/llama-70b", "object": "model", "owned_by": "cloudflare"},
				{"id": "cf/llama-8b", "object": "model", "owned_by": "cloudflare"},
				{"id": "cf/deepseek-r1", "object": "model", "owned_by": "cloudflare"},
				{"id": "cf/qwen-32b", "object": "model", "owned_by": "cloudflare"},
				{"id": "cf/mistral-7b", "object": "model", "owned_by": "cloudflare"},
				{"id": "cf/kimi-k2.5", "object": "model", "owned_by": "cloudflare"},
				{"id": "cf/kimi-k2.6", "object": "model", "owned_by": "cloudflare"},
				{"id": "cf/kimi-k2.7-code", "object": "model", "owned_by": "cloudflare"},
				{"id": "cf/glm-4.7-flash", "object": "model", "owned_by": "cloudflare"},
				{"id": "cf/glm-5.2", "object": "model", "owned_by": "cloudflare"},
				{"id": "cf/gpt-oss-120b", "object": "model", "owned_by": "cloudflare"},
				{"id": "cf/gpt-oss-20b", "object": "model", "owned_by": "cloudflare"},
				{"id": "cf/llama-4-scout", "object": "model", "owned_by": "cloudflare"},
				{"id": "cf/gemma-4", "object": "model", "owned_by": "cloudflare"},
				{"id": "cf/nemotron-3", "object": "model", "owned_by": "cloudflare"},
				// Multi-modal CF models (embeddings / image)
				{"id": "@cf/baai/bge-base-en-v1.5", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/baai/bge-large-en-v1.5", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/baai/bge-small-en-v1.5", "object": "model", "owned_by": "cloudflare"},
				{"id": "@cf/black-forest-labs/flux-1-schnell", "object": "model", "owned_by": "cloudflare"},
			}
			// Append runtime-registered custom models.
			if registry != nil {
				for _, entry := range registry.ListModels() {
					models = append(models, gin.H{"id": entry.ID, "object": "model", "owned_by": entry.OwnedBy})
				}
			}
			// Append runtime-registered combos (v1.4.0).
			if combos != nil {
				for _, c := range combos.ListCombos() {
					models = append(models, gin.H{"id": "combo/" + c.Name, "object": "model", "owned_by": "combo", "type": c.Strategy, "strategy": c.Strategy, "members": c.Models})
				}
			}
			// Anthropic clients (Claude Code, anthropic-sdk-*, etc.) probe
			// GET /v1/models and expect Anthropic-shaped entries. Detect them
			// by well-known request headers and enrich each entry in-place
			// with {type, display_name, created_at}. OpenAI fields stay too
			// so the same response works for both client families.
			if isAnthropicClient(c) {
				for i := range models {
					id, _ := models[i]["id"].(string)
					models[i]["type"] = "model"
					models[i]["display_name"] = displayNameForModel(id)
					models[i]["created_at"] = "2025-01-01T00:00:00Z"
				}
			}
			// Filter out models the operator has hidden (v1.7.0).
			if store != nil {
				if hidden, err := store.LoadHiddenModels(); err == nil && len(hidden) > 0 {
					filtered := models[:0]
					for _, m := range models {
						id, _ := m["id"].(string)
						if h, ok := hidden[id]; ok && h.Hidden {
							continue
						}
						filtered = append(filtered, m)
					}
					models = filtered
				}
			}
			c.JSON(200, gin.H{"object": "list", "data": models})
			return
		}

		// Only handle chat completions
		if path != "/v1/chat/completions" && path != "/chat/completions" {
			c.JSON(404, gin.H{"error": "not found: " + path})
			return
		}

		// Cap request body to prevent OOM / DoS via multi-GB uploads.
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, upstream.MAX_REQUEST_BODY)
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			// MaxBytesReader returns *http.MaxBytesError when limit exceeded
			if _, ok := err.(*http.MaxBytesError); ok {
				c.JSON(413, gin.H{"error": "request body too large", "limit_bytes": upstream.MAX_REQUEST_BODY})
				return
			}
			c.JSON(400, gin.H{"error": "read body failed"})
			return
		}

		var bodyMap map[string]any
		if err := json.Unmarshal(body, &bodyMap); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}

		model, _ := bodyMap["model"].(string)
		if model == "" {
			model = "grok-4.5"
			bodyMap["model"] = model
			body, _ = json.Marshal(bodyMap)
		}

		// ── Guardrails (soft scan + optional PII redaction) ──
		if _, reason := guardrails.ScanMessages(bodyMap); reason != "" {
			slog.Debug("guardrails soft-flag", "reason", reason)
		}
		if n := guardrails.RedactPII(bodyMap); n > 0 {
			body, _ = json.Marshal(bodyMap)
		}

		// ── Token Saver ──
		// Two paths:
		//   1. OmniRoute-style compression pipeline (mode != "off"): runs the
		//      full lite/standard/aggressive/ultra engine with measured stats.
		//      Aggressive mode already includes RTK tool-result compression
		//      internally, so the legacy RTK step is skipped to avoid double work.
		//   2. Legacy toggle path (mode == "off"): RTK + Headroom + directive,
		//      preserved for backward compatibility.
		cfg := TokenSaver.Get()
		compMode := compression.ParseMode(cfg.Mode)
		// Per-combo compression override: a combo may pin its own mode
		// (combo.CompressionMode), which wins over the global Token Saver mode.
		// Empty/"off" on the combo falls back to the global setting.
		if combos != nil && strings.HasPrefix(model, "combo/") {
			if cb, ok := combos.GetCombo(strings.TrimPrefix(model, "combo/")); ok && cb.CompressionMode != "" {
				compMode = compression.ParseMode(cb.CompressionMode)
			}
		}
		if compMode != compression.ModeOff {
			res := compression.Apply(bodyMap, compMode, compression.Options{
				Model:                model,
				PreserveSystemPrompt: true,
			})
			if res.Compressed && res.Stats != nil {
				bodyMap = res.Body
				body, _ = json.Marshal(bodyMap)
				recordCompression(res.Stats)
				slog.Info("[compression] saved tokens",
					"mode", string(compMode),
					"before", res.Stats.OriginalTokens,
					"after", res.Stats.CompressedTokens,
					"savings_pct", res.Stats.SavingsPercent,
					"techniques", res.Stats.TechniquesUsed)
			}
		} else if cfg.RTK && RTKEnabled {
			// Legacy RTK-only input compression.
			if stats := rtk.CompressMessages(bodyMap); stats != nil {
				if log := rtk.FormatLog(stats); log != "" {
					slog.Info(log)
				}
				body, _ = json.Marshal(bodyMap)
			}
		}
		if cfg.Headroom {
			if dropped := tokensaver.CompressHeadroom(bodyMap, 10); dropped > 0 {
				slog.Info("headroom compressed turns", "dropped", dropped)
				body, _ = json.Marshal(bodyMap)
			}
		}
		if dir := TokenSaver.Directive(); dir != "" {
			if tokensaver.InjectDirective(bodyMap, dir) {
				body, _ = json.Marshal(bodyMap)
			}
		}

		// ── Semantic cache (non-stream only) ──
		streamReq := false
		if s, ok := bodyMap["stream"].(bool); ok && s {
			streamReq = true
		}
		cacheKey := ""
		if !streamReq && SemanticCache != nil && SemanticCache.Enabled() {
			cacheKey = cache.KeyFromChat(model, bodyMap)
			if cached, ok := SemanticCache.Get(cacheKey); ok {
				c.Header("X-Cache", "HIT")
				c.Data(200, "application/json", cached)
				return
			}
			c.Header("X-Cache", "MISS")
		}

		// Custom alias + custom model resolution (runtime-configured).
		// 1) Alias rewrite (single hop): "my-claude" → "cb/claude-sonnet-4.6"
		// 2) Custom-model lookup: if resolved id is registered, override the
		//    routing upstream + the model_name that goes over the wire.
		var customUpstream, customModelName string
		if registry != nil {
			resolved, up, mn := registry.Resolve(model)
			if resolved != model {
				model = resolved
				bodyMap["model"] = model
				body, _ = json.Marshal(bodyMap)
			}
			customUpstream = up
			customModelName = mn
		}

		// Combo resolution (v1.4.0). Runs after custom alias but before
		// default grok/cb prefix routing. On a hit we rewrite the model in
		// the request body to the concrete backend model and, for fallback
		// strategy, remember the combo name so the dispatch loop below can
		// walk to the next model on 5xx.
		var comboName string
		var comboStrategy string
		if combos != nil {
			if next, ok := combos.Resolve(model); ok {
				// Remember for fallback retry chain.
				if strings.HasPrefix(model, "combo/") {
					comboName = strings.TrimPrefix(model, "combo/")
					if cb, ok2 := combos.GetCombo(comboName); ok2 {
						comboStrategy = cb.Strategy
					}
				}
				model = next
				bodyMap["model"] = model
				body, _ = json.Marshal(bodyMap)
				// Re-run custom alias resolution against the concrete model
				// so a combo pointing at "my-claude" still resolves through
				// the alias table (single hop).
				if registry != nil {
					resolved, up, mn := registry.Resolve(model)
					if resolved != model {
						model = resolved
						bodyMap["model"] = model
						body, _ = json.Marshal(bodyMap)
					}
					customUpstream = up
					customModelName = mn
				}
			}
		}

		// Model alias expansion: grok-4.5-{high,medium,low,auto,none} → grok-4.5 + reasoning_effort
		// Mirrors 9router's grok-cli provider (upstreamModelId + thinking level).
		// Skip when a custom model has already routed us — the custom model_name
		// is authoritative in that case.
		if customUpstream == "" {
			if effort, ok := upstream.ExpandGrokAlias(model); ok {
				model = "grok-4.5"
				bodyMap["model"] = model
				// Only set reasoning_effort if client didn't specify one already
				if _, has := bodyMap["reasoning_effort"]; !has {
					if _, has2 := bodyMap["reasoning"]; !has2 {
						bodyMap["reasoning_effort"] = effort
					}
				}
				body, _ = json.Marshal(bodyMap)
			}
		}

		// Per-key model whitelist check.
		// If the key has allowed_models set, reject models not on the list.
		// Supports glob: "grok-*", "cb/*", or exact "cb/gpt-5.5".
		fullKey := c.GetString("client_key")
		if fullKey != "" && authMgr != nil {
			if info, ok := authMgr.Get(fullKey); ok {
				if !info.IsModelAllowed(model) {
					c.JSON(403, gin.H{
						"error": "model not allowed for this API key",
						"model": model,
						"hint":  "this key is restricted to specific models — contact the gateway operator",
					})
					c.Set("error_msg", "model not allowed: "+model)
					errJSON, _ := json.Marshal(gin.H{"error": "model not allowed", "model": model})
					c.Set("response_body", json.RawMessage(errJSON))
					return
				}
			}
		}

		clientStream := false
		if s, ok := bodyMap["stream"].(bool); ok && s {
			clientStream = true
		}
		if c.GetHeader("Accept") == "text/event-stream" {
			clientStream = true
		}

		startTime := time.Now()
		upstreamName := "codebuddy"

		// dispatch runs the routing switch for a single (model, body). It
		// mutates upstreamName so the metrics/logging block below sees the
		// last-tried upstream. Extracted into a closure so fallback combos
		// can invoke it in a loop with different models.
		dispatch := func(m string, b []byte, bm map[string]any) {
			switch customUpstream {
			case "grok":
				upstreamName = "grok"
				effectiveModel := m
				if customModelName != "" {
					effectiveModel = customModelName
					bm["model"] = effectiveModel
					b, _ = json.Marshal(bm)
				}
				upstream.ProxyGrok(c, b, grokAM, clientStream, hc, effectiveModel)
			case "codebuddy":
				upstreamName = "codebuddy"
				if customModelName != "" {
					// cbTransform will TrimPrefix("cb/") on this — prepend so the
					// upstream sees exactly customModelName.
					bm["model"] = "cb/" + customModelName
					b, _ = json.Marshal(bm)
				}
				upstream.ProxyCodeBuddy(c, b, bm, cbKM, clientStream, hc)
			default:
				if upstream.IsCFModel(m) {
					upstreamName = "cloudflare"
					// Expand friendly cf/* alias to full Workers AI model id.
					if expanded, ok := upstream.ExpandCFAlias(m); ok {
						m = expanded
						bm["model"] = m
						b, _ = json.Marshal(bm)
					}
					upstream.ProxyCloudflare(c, b, bm, cfKM, clientStream, hc)
				} else if upstream.IsGrokModel(m) {
					upstreamName = "grok"
					upstream.ProxyGrok(c, b, grokAM, clientStream, hc, m)
				} else {
					upstreamName = "codebuddy"
					upstream.ProxyCodeBuddy(c, b, bm, cbKM, clientStream, hc)
				}
			}
		}

		// Fallback combo retry chain:
		//   For non-streaming requests we wrap c.Writer in a buffering
		//   recorder, invoke dispatch, then inspect status. On 5xx (or
		//   circuit-open 503) we peel model resolution + try the next entry.
		//   4xx bails out immediately — client errors don't get retried.
		//
		//   Streaming (SSE) requests skip retry: once the first byte hits
		//   the wire we cannot un-send it. They use the head model only.
		if comboName != "" && (comboStrategy == "fallback" || comboStrategy == "fill_first" || comboStrategy == "priority") && !clientStream {
			// Snapshot the concrete-model chain from the combo.
			cb, _ := combos.GetCombo(comboName)
			// Walk models[0..N-1]; model already holds models[0] (resolved
			// earlier). On retry we resolve alias + custom-model again for
			// each candidate so combos of aliases keep working.
			var lastRecorder *bufferedWriter
			for _, candidate := range cb.Models {
				// Re-run custom alias + custom-model on this candidate.
				candModel := candidate
				candCustomUp := ""
				candCustomMN := ""
				if registry != nil {
					r, up, mn := registry.Resolve(candModel)
					candModel = r
					candCustomUp = up
					candCustomMN = mn
				}
				// Re-expand grok alias when no custom routing hit.
				if candCustomUp == "" {
					if effort, ok := upstream.ExpandGrokAlias(candModel); ok {
						candModel = "grok-4.5"
						bodyMap["model"] = candModel
						if _, has := bodyMap["reasoning_effort"]; !has {
							if _, has2 := bodyMap["reasoning"]; !has2 {
								bodyMap["reasoning_effort"] = effort
							}
						}
					}
				}
				bodyMap["model"] = candModel
				candBody, _ := json.Marshal(bodyMap)

				// Buffer the upstream response so we can decide to retry.
				bw := newBufferedWriter(c.Writer)
				origWriter := c.Writer
				c.Writer = bw

				// Temporarily swap custom-upstream vars for this attempt so
				// the closure routes correctly.
				savedUp, savedMN := customUpstream, customModelName
				customUpstream = candCustomUp
				customModelName = candCustomMN
				dispatch(candModel, candBody, bodyMap)
				customUpstream, customModelName = savedUp, savedMN

				c.Writer = origWriter
				model = candModel // track last-tried for logging

				// 2xx / 3xx — final success.
				// 4xx — retry on 404 (model removed from upstream), bail on others (client error).
				// 5xx — retry (upstream failure).
				if bw.status < 500 && bw.status != 404 {
					lastRecorder = bw
					break
				}
				lastRecorder = bw
				// else continue to next candidate
			}
			// Flush whichever recorder held the terminal response.
			if lastRecorder != nil {
				lastRecorder.flush()
			}
		} else {
			// Routing decision:
			//   1. Custom model → routes to its declared upstream. If a ModelName
			//      override is set, we rewrite bodyMap[model] to that name so the
			//      upstream sees the "real" model. cbTransform strips the cb/
			//      prefix, so for CodeBuddy we prepend one to keep its stripCBPrefix
			//      happy; grok upstream sees the model_name as-is.
			//   2. Fall through to prefix routing (grok-* vs cb/*).
			dispatch(model, body, bodyMap)
		}

		// Record Prometheus metrics for this proxied request. Bucket status by
		// 3-digit HTTP code (200, 429, 500). Duration in seconds for the
		// standard histogram buckets. Cheap: label lookups + atomic increments.
		elapsed := time.Since(startTime).Seconds()
		status := strconv.Itoa(c.Writer.Status())
		metrics.RequestsTotal.WithLabelValues(upstreamName, status).Inc()
		metrics.RequestDuration.WithLabelValues(upstreamName).Observe(elapsed)

		// Per-key token quota tracking
		fullKey = c.GetString("client_key")
		if fullKey != "" && authMgr != nil {
			tokensIn, _ := c.Get("tokens_in")
			tokensOut, _ := c.Get("tokens_out")
			totalTokens := int64(toInt(tokensIn) + toInt(tokensOut))
			if totalTokens > 0 {
				authMgr.IncrementTokens(fullKey, totalTokens)
			} else {
				authMgr.IncrementRequests(fullKey)
			}
		}

		// Live quota tracker + semantic cache store (non-stream 2xx)
		{
			tokensIn, _ := c.Get("tokens_in")
			tokensOut, _ := c.Get("tokens_out")
			totalTokens := int64(toInt(tokensIn) + toInt(tokensOut))
			var fam quota.Family
			switch upstreamName {
			case "grok":
				fam = quota.Grok
			case "cloudflare":
				fam = quota.CF
			default:
				fam = quota.CB
			}
			costUSD := 0.0
			if strings.HasPrefix(model, "@cf/") || strings.HasPrefix(model, "cf/") {
				if n, ok := c.Get("neurons"); ok {
					costUSD = cost.USDNeurons(toFloatNeurons(n))
				}
			} else {
				costUSD = cost.USD(model, int64(toInt(tokensIn)), int64(toInt(tokensOut)))
			}
			if QuotaTracker != nil {
				QuotaTracker.AddUsage(fam, totalTokens, costUSD)
			}
			if cacheKey != "" && c.Writer.Status() == 200 && !streamReq {
				if rb, ok := c.Get("response_body"); ok {
					if raw, ok := rb.(json.RawMessage); ok && len(raw) > 0 {
						SemanticCache.Put(cacheKey, []byte(raw))
					} else if b, ok := rb.([]byte); ok && len(b) > 0 {
						SemanticCache.Put(cacheKey, b)
					}
				}
			}
		}

		// Async log to ClickHouse — only for chat completion endpoint,
		// not for probes to /v1/models, /health, /props, etc.
		if grokAM.DB() != nil && path == "/v1/chat/completions" {
			inputText := extractInputText(bodyMap)
			outputText, _ := c.Get("output_text")
			tokensIn, _ := c.Get("tokens_in")
			tokensOut, _ := c.Get("tokens_out")
			neurons, _ := c.Get("neurons")
			responseBody, _ := c.Get("response_body")

			// Full request/response JSON stored in ClickHouse (ZSTD) — unlimited.
			rl := db.RequestLog{
				Timestamp:  startTime,
				RequestID:  c.GetString("request_id"),
				ClientKey:  c.GetString("client_key_masked"),
				Model:      model,
				Upstream:   upstreamName,
				AccountID:  c.GetString("upstream_account"),
				StatusCode: c.Writer.Status(),
				LatencyMs:  int(time.Since(startTime).Milliseconds()),
				TokensIn:   toInt(tokensIn),
				TokensOut:  toInt(tokensOut),
				Neurons:    toFloatNeurons(neurons),
				InputText:  inputText,
				OutputText: toString(outputText),
			}
			// Estimated USD cost (OmniRoute-style pricing table).
			// Cloudflare bills per "neuron" (not tokens), so use the neuron rate.
			if strings.HasPrefix(model, "@cf/") {
				rl.CostUsd = cost.USDNeurons(rl.Neurons)
			} else {
				rl.CostUsd = cost.USD(model, int64(rl.TokensIn), int64(rl.TokensOut))
			}
			// Capture error message for non-2xx responses (audit trail)
			if errMsg, exists := c.Get("error_msg"); exists {
				rl.ErrorMsg = toString(errMsg)
			}
			if len(body) > 0 {
				rl.RequestBody = json.RawMessage(body)
			}
			if rb, ok := responseBody.(json.RawMessage); ok && len(rb) > 0 {
				rl.ResponseBody = rb
			} else if errMsg, exists := c.Get("response_body"); exists {
				// Fallback: error branches set response_body directly
				if rb, ok := errMsg.(json.RawMessage); ok && len(rb) > 0 {
					rl.ResponseBody = rb
				}
			}
			grokAM.DB().LogRequest(rl)
		}
	}
}
