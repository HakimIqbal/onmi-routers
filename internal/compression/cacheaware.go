package compression

import "strings"

// ── Cache-aware strategy adjustment (ported from OmniRoute cachingAware.ts) ──
// When a provider supports prompt caching (explicit cache_control markers or
// automatic prefix caching per #3955), rewriting the cacheable prefix forces a
// cache miss. The guard downgrades aggressive/ultra → standard and always
// preserves the system prompt, so the cacheable prefix stays stable.

var cachingProviders = map[string]bool{
	"claude": true, "anthropic": true, "zai": true, "deepseek": true,
	"kimi-coding": true, "kimi-coding-apikey": true, "xiaomi-mimo": true,
	"openai": true, "codex": true, "azure": true,
	"dashscope": true, "alicode": true, "alicode-intl": true,
}

// CachingProvider reports whether a provider supports prompt caching.
func CachingProvider(provider string) bool {
	return cachingProviders[strings.ToLower(strings.TrimSpace(provider))]
}

// inferProvider guesses the provider from a model name prefix.
func inferProvider(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "@cf/"):
		return "cloudflare"
	case strings.HasPrefix(m, "grok"):
		return "grok"
	case strings.HasPrefix(m, "cb/"):
		return "codebuddy"
	case strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "o1-") || strings.HasPrefix(m, "o3-"):
		return "openai"
	case strings.HasPrefix(m, "claude"):
		return "anthropic"
	case strings.HasPrefix(m, "gemini"):
		return "google"
	case strings.HasPrefix(m, "deepseek"):
		return "deepseek"
	}
	if idx := strings.Index(m, "/"); idx > 0 {
		return m[:idx]
	}
	return ""
}

// hasCacheControl scans a body for explicit cache_control markers (recursive).
func hasCacheControl(body map[string]any) bool {
	return scanCacheControl(body)
}

func scanCacheControl(v any) bool {
	switch val := v.(type) {
	case map[string]any:
		if _, ok := val["cache_control"]; ok {
			return true
		}
		for _, child := range val {
			if scanCacheControl(child) {
				return true
			}
		}
	case []any:
		for _, item := range val {
			if scanCacheControl(item) {
				return true
			}
		}
	}
	return false
}

// cacheAwareAdjust adjusts the compression mode for a caching provider.
// Returns the adjusted mode and whether the system prompt must be preserved.
func cacheAwareAdjust(mode Mode, provider string, body map[string]any) (Mode, bool) {
	isCaching := CachingProvider(provider) || hasCacheControl(body)
	if !isCaching {
		return mode, false
	}
	adjusted := mode
	if mode == ModeAggressive || mode == ModeUltra {
		adjusted = ModeStandard
	}
	return adjusted, true
}
