package compression

import (
	"encoding/json"
	"strings"
)

// ── Aggressive mode (ported from OmniRoute aggressive.ts + progressiveAging.ts) ──
// Pipeline: (1) tool-result compression via the RTK engine, (2) progressive
// aging by distance-from-end, (3) fallback summarizer for remaining long
// messages. If total savings fall below the min threshold, a caveman→lite
// fallback chain tries to recover more. Fail-open at every step.

// aging thresholds (distance-from-end), matching DEFAULT_AGGRESSIVE_CONFIG.
const (
	agingVerbatim = 2 // keep last N messages verbatim
	agingLight    = 2 // next band: lite compression
	agingModerate = 3 // next band: caveman
	// anything older: full summary
)

const (
	aggrMaxTokensPerMessage = 2048
	aggrMinSavings          = 0.05 // 5%
)

var compressedMarkerPrefix = "[COMPRESSED:"

func applyAggressive(body map[string]any, opts Options) Result {
	msgs := messagesOf(body)
	if len(msgs) == 0 {
		return Result{Body: body, Compressed: false}
	}
	out := cloneBody(body)
	techniques := []string{}

	// Step 1: tool-result compression (reuse the RTK engine — already solid).
	if rtkStats := rtkCompressInPlace(out); rtkStats != nil {
		techniques = append(techniques, "toolResult")
	}

	// Step 2: progressive aging.
	if aged := applyAging(messagesOf(out), opts); aged != nil {
		out["messages"] = aged
		techniques = append(techniques, "aging")
	}

	// Step 3: fallback summarizer for remaining long messages.
	if summarized := applyFallbackSummarizer(messagesOf(out), opts); summarized {
		techniques = append(techniques, "summarizer")
	}

	if len(techniques) == 0 {
		return Result{Body: body, Compressed: false}
	}

	// Measure savings; if below threshold, try caveman→lite fallback chain.
	original := snapshotTokens(body)
	current := snapshotTokens(out)
	savings := 0.0
	if original > 0 {
		savings = float64(original-current) / float64(original)
	}
	if savings < aggrMinSavings {
		if cav := applyCaveman(out, opts); cav.Compressed {
			if snapshotTokens(cav.Body) < snapshotTokens(out) {
				out = cav.Body
				techniques = append(techniques, "caveman-fallback")
			}
		}
		if lit := applyLite(out, opts); lit.Compressed {
			if snapshotTokens(lit.Body) < snapshotTokens(out) {
				out = lit.Body
				techniques = append(techniques, "lite-fallback")
			}
		}
	}

	return Result{Body: out, Compressed: true, Stats: &Stats{TechniquesUsed: techniques}}
}

// applyAging compresses messages by distance-from-end: verbatim (nearest),
// lite, caveman (moderate), full summary (oldest). System + already-compressed
// messages are preserved.
func applyAging(msgs []any, opts Options) []any {
	total := len(msgs)
	if total == 0 {
		return nil
	}
	out := make([]any, total)
	changed := false
	for i, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			out[i] = m
			continue
		}
		text := extractText(mm["content"])
		if (opts.PreserveSystemPrompt && roleOf(mm) == "system") || strings.HasPrefix(strings.TrimSpace(text), compressedMarkerPrefix) {
			out[i] = m
			continue
		}
		dist := total - 1 - i
		switch {
		case dist <= agingVerbatim:
			out[i] = m
		case dist <= agingVerbatim+agingLight:
			nm, ch := ageMessage(mm, "light", text, func(s string) string {
				r := applyLite(wrapSingle(s), Options{})
				if r.Compressed {
					return extractText(messagesOf(r.Body)[0].(map[string]any)["content"])
				}
				return s
			})
			out[i] = nm
			if ch {
				changed = true
			}
		case dist <= agingVerbatim+agingLight+agingModerate:
			nm, ch := ageMessage(mm, "moderate", text, func(s string) string {
				r := applyCaveman(wrapSingle(s), Options{})
				if r.Compressed {
					return extractText(messagesOf(r.Body)[0].(map[string]any)["content"])
				}
				return s
			})
			out[i] = nm
			if ch {
				changed = true
			}
		default:
			nm, ch := ageFullSummary(mm, text)
			out[i] = nm
			if ch {
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	return out
}

// ageMessage applies a compression fn and tags the result, respecting
// structured content (JSON stays verbatim; fenced code gets a line-prefix tag).
// Returns the (possibly new) message and whether it changed.
func ageMessage(mm map[string]any, tier, originalText string, compress func(string) string) (map[string]any, bool) {
	if _, ok := mm["content"].(string); !ok {
		return mm, false
	}
	compressed := compress(originalText)
	tagged := tagAged(tier, originalText, compressed)
	if tagged == originalText {
		return mm, false
	}
	nm := copyMsg(mm)
	nm["content"] = tagged
	return nm, true
}

// ageFullSummary summarizes old assistant messages / keeps first line of user.
func ageFullSummary(mm map[string]any, text string) (map[string]any, bool) {
	if _, ok := mm["content"].(string); !ok {
		return mm, false
	}
	role := roleOf(mm)
	var compressed string
	switch role {
	case "assistant":
		compressed = summarizeMessage(text, 0, true)
	case "user":
		compressed = truncStr(strings.Split(text, "\n")[0], 120)
	default:
		return mm, false
	}
	tagged := tagAged("fullSummary", text, compressed)
	if tagged == text {
		return mm, false
	}
	nm := copyMsg(mm)
	nm["content"] = tagged
	return nm, true
}

// tagAged builds aged content, keeping structured payloads intact.
func tagAged(tier, originalText, compressed string) string {
	switch structuredKind(originalText) {
	case "json":
		return originalText
	case "fenced":
		return "[COMPRESSED:aging:" + tier + "]\n" + originalText
	default:
		return "[COMPRESSED:aging:" + tier + "] " + compressed
	}
}

// structuredKind detects pure-JSON or fenced-code content.
func structuredKind(text string) string {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "```") && strings.HasSuffix(trimmed, "```") {
		return "fenced"
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var v any
		if json.Unmarshal([]byte(trimmed), &v) == nil {
			return "json"
		}
	}
	return ""
}

// applyFallbackSummarizer summarizes messages still longer than the per-message
// token budget. Returns true if any message was summarized.
func applyFallbackSummarizer(msgs []any, opts Options) bool {
	changed := false
	maxChars := aggrMaxTokensPerMessage * charsPerToken
	for i, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if opts.PreserveSystemPrompt && roleOf(mm) == "system" {
			continue
		}
		text, ok := mm["content"].(string)
		if !ok || strings.HasPrefix(strings.TrimSpace(text), compressedMarkerPrefix) {
			continue
		}
		if len(text) <= maxChars {
			continue
		}
		summary := summarizeMessage(text, aggrMaxTokensPerMessage, true)
		if summary != "" && len(summary) < len(text) {
			nm := copyMsg(mm)
			nm["content"] = summary
			msgs[i] = nm
			changed = true
		}
	}
	return changed
}

// wrapSingle builds a minimal chat body with one user message (for reuse of
// lite/caveman on a single string during aging).
func wrapSingle(text string) map[string]any {
	return map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": text},
		},
	}
}
