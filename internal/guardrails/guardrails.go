// Package guardrails — lightweight request/response safety filters for the gateway.
// Prompt-injection patterns + optional PII redaction. Fail-open by default.
package guardrails

import (
	"regexp"
	"strings"
	"sync"
)

var (
	mu      sync.RWMutex
	enabled = true
	redact  = false
)

func SetEnabled(v bool) { mu.Lock(); enabled = v; mu.Unlock() }
func SetRedactPII(v bool) { mu.Lock(); redact = v; mu.Unlock() }
func Enabled() bool {
	mu.RLock()
	defer mu.RUnlock()
	return enabled
}

var injectRe = regexp.MustCompile(`(?i)(ignore (all )?(previous|prior) instructions|system prompt:|you are now DAN|jailbreak|do anything now|reveal (your )?(system|hidden) prompt)`)
var emailRe = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
var phoneRe = regexp.MustCompile(`\b(?:\+?\d{1,3}[-.\s]?)?(?:\(?\d{2,4}\)?[-.\s]?)?\d{3,4}[-.\s]?\d{3,4}\b`)

// ScanMessages returns (blocked, reason). blocked=true only for high-confidence injection.
func ScanMessages(body map[string]any) (bool, string) {
	mu.RLock()
	on := enabled
	mu.RUnlock()
	if !on || body == nil {
		return false, ""
	}
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := mm["role"].(string)
		if role == "system" {
			continue
		}
		text := extractText(mm["content"])
		if injectRe.MatchString(text) {
			// soft-flag only — do not hard-block production coding traffic by default
			return false, "prompt_injection_pattern"
		}
	}
	return false, ""
}

// RedactPII mutates message content strings when redact mode is on.
func RedactPII(body map[string]any) int {
	mu.RLock()
	on := enabled && redact
	mu.RUnlock()
	if !on || body == nil {
		return 0
	}
	n := 0
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		switch c := mm["content"].(type) {
		case string:
			nc := emailRe.ReplaceAllString(c, "[REDACTED_EMAIL]")
			nc = phoneRe.ReplaceAllString(nc, "[REDACTED_PHONE]")
			if nc != c {
				mm["content"] = nc
				n++
			}
		}
	}
	return n
}

func extractText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var b strings.Builder
		for _, p := range t {
			if pm, ok := p.(map[string]any); ok {
				if s, ok := pm["text"].(string); ok {
					b.WriteString(s)
					b.WriteByte(' ')
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}

func Status() map[string]any {
	mu.RLock()
	defer mu.RUnlock()
	return map[string]any{"enabled": enabled, "redact_pii": redact}
}
