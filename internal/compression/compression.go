// Package compression — OmniRoute-style prompt compression pipeline.
//
// A faithful Go port of OmniRoute's open-sse/services/compression engine.
// Five user-facing intensity modes (off/lite/standard/aggressive/ultra) plus
// the existing RTK tool-output filters. Every mode is fail-open: any error or
// non-applicable body leaves the request untouched and never grows it.
//
// Modes (mirrors OmniRoute strategySelector.ts):
//   - off        → no compression
//   - lite       → whitespace collapse, system dedup, tool truncate, redundant
//                  removal, image placeholder (safe, lossless-ish)
//   - standard   → caveman rule engine (regex filler/terse rewrite)
//   - aggressive → tool-result compress + progressive aging + summarizer,
//                  with caveman→lite fallback chain
//   - ultra      → heuristic token pruner (information-density scoring)
//
// Each Apply returns a Result carrying Stats (tokens before/after, savings %,
// techniques used) so the dashboard can show REAL measured savings.
package compression

import (
	"encoding/json"
	"time"
)

// Mode is a compression intensity level.
type Mode string

const (
	ModeOff        Mode = "off"
	ModeLite       Mode = "lite"
	ModeStandard   Mode = "standard" // caveman
	ModeAggressive Mode = "aggressive"
	ModeUltra      Mode = "ultra"
)

// Valid reports whether m is a recognized mode.
func (m Mode) Valid() bool {
	switch m {
	case ModeOff, ModeLite, ModeStandard, ModeAggressive, ModeUltra:
		return true
	}
	return false
}

// ParseMode normalizes a string to a Mode (defaults to off).
func ParseMode(s string) Mode {
	m := Mode(s)
	if m.Valid() {
		return m
	}
	return ModeOff
}

// Stats reports measured compression savings for one request.
// Mirrors OmniRoute's CompressionStats shape.
type Stats struct {
	Mode             Mode     `json:"mode"`
	OriginalTokens   int      `json:"original_tokens"`
	CompressedTokens int      `json:"compressed_tokens"`
	SavingsPercent   float64  `json:"savings_percent"`
	TechniquesUsed   []string `json:"techniques_used"`
	Timestamp        int64    `json:"timestamp"`
}

// Result is the outcome of an Apply call.
type Result struct {
	Body       map[string]any
	Compressed bool
	Stats      *Stats
}

// Options tunes a single Apply call.
type Options struct {
	Model                string
	Provider             string // explicit provider name (for cache-aware detection)
	PreserveSystemPrompt bool   // default true — never touch system messages
}

const charsPerToken = 4

// estimateTokens approximates token count (chars / 4), matching OmniRoute.
func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + charsPerToken - 1) / charsPerToken
}

// Apply runs the compression pipeline for the given mode on an OpenAI-style
// chat body (map with "messages"). Fail-open: returns the body unchanged with
// Compressed=false when the mode is off, the body is malformed, or no
// technique yields a net reduction. Never grows the body.
func Apply(body map[string]any, mode Mode, opts Options) Result {
	if body == nil || mode == ModeOff || !mode.Valid() {
		return Result{Body: body, Compressed: false}
	}
	if opts.PreserveSystemPrompt == false {
		// caller explicitly opted out; keep zero value handling below
	} else {
		opts.PreserveSystemPrompt = true
	}

	// ── Cache-aware adjustment (ported from OmniRoute cachingAware.ts) ──
	// For caching providers, downgrade aggressive/ultra → standard and force
	// system-prompt preservation so the cacheable prefix stays stable.
	provider := opts.Provider
	if provider == "" {
		provider = inferProvider(opts.Model)
	}
	mode, forcePreserve := cacheAwareAdjust(mode, provider, body)
	if forcePreserve {
		opts.PreserveSystemPrompt = true
	}

	original := snapshotTokens(body)

	var res Result
	switch mode {
	case ModeLite:
		res = applyLite(body, opts)
	case ModeStandard:
		res = applyCaveman(body, opts)
	case ModeAggressive:
		res = applyAggressive(body, opts)
	case ModeUltra:
		res = applyUltra(body, opts)
	default:
		return Result{Body: body, Compressed: false}
	}

	// Safety net: never ship a body that grew or lost all content.
	if !res.Compressed || res.Body == nil {
		return Result{Body: body, Compressed: false}
	}
	after := snapshotTokens(res.Body)
	if after >= original {
		return Result{Body: body, Compressed: false}
	}

	if res.Stats == nil {
		res.Stats = &Stats{Mode: mode, Timestamp: time.Now().Unix()}
	}
	res.Stats.OriginalTokens = original
	res.Stats.CompressedTokens = after
	if original > 0 {
		res.Stats.SavingsPercent = float64(original-after) * 100.0 / float64(original)
	}

	// ── Fidelity gate (ported from OmniRoute fidelityGate.ts) ──
	// For lossy modes (standard and above), reject the compression when the
	// measured savings fall below the minimum threshold — the rewriting risk
	// isn't worth a negligible gain. Lite is near-lossless so it is exempt.
	if !fidelityAccept(mode, res.Stats.SavingsPercent) {
		return Result{Body: body, Compressed: false}
	}
	res.Stats.Mode = mode
	if res.Stats.Timestamp == 0 {
		res.Stats.Timestamp = time.Now().Unix()
	}
	return res
}

// snapshotTokens estimates total tokens across all message content in a body.
func snapshotTokens(body map[string]any) int {
	total := 0
	msgs, ok := body["messages"].([]any)
	if !ok {
		return 0
	}
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		total += estimateTokens(extractText(mm["content"]))
	}
	return total
}

// extractText flattens a message content value (string or block array) to text.
func extractText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		out := ""
		for _, p := range c {
			part, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := part["text"].(string); ok {
				out += t
			}
		}
		return out
	}
	return ""
}

// messagesOf returns the messages slice from a body (nil if absent/wrong type).
func messagesOf(body map[string]any) []any {
	msgs, _ := body["messages"].([]any)
	return msgs
}

// cloneBody deep-copies a body via JSON round-trip (safe, cheap enough here).
func cloneBody(body map[string]any) map[string]any {
	b, err := json.Marshal(body)
	if err != nil {
		return body
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return body
	}
	return out
}
