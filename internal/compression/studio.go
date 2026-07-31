package compression

import (
	"encoding/json"
	"strings"
)

// StepBreakdown reports token reduction for a single compression step.
type StepBreakdown struct {
	Name         string  `json:"name"`
	TokensBefore int     `json:"tokens_before"`
	TokensAfter  int     `json:"tokens_after"`
	SavingsPct   float64 `json:"savings_pct"`
}

// DiffSegment is one word-level diff fragment.
type DiffSegment struct {
	Type string `json:"type"` // "keep", "remove", "add"
	Text string `json:"text"`
}

// EngineResult is the per-engine lane output for the Studio multi-preview.
type EngineResult struct {
	Engine           string          `json:"engine"`
	Compressed       bool            `json:"compressed"`
	OriginalTokens   int             `json:"original_tokens"`
	CompressedTokens int             `json:"compressed_tokens"`
	SavingsPercent   float64         `json:"savings_percent"`
	Techniques       []string        `json:"techniques"`
	Result           string          `json:"result"`
	Steps            []StepBreakdown `json:"steps"`
}

// ApplyEngine runs a single named engine on the body and returns the result.
// Supported engines: "lite", "caveman", "ultra", "rtk", "summarizer".
func ApplyEngine(body map[string]any, engine string, opts Options) Result {
	switch strings.ToLower(engine) {
	case "lite":
		return applyLite(body, opts)
	case "caveman", "standard":
		return applyCaveman(body, opts)
	case "ultra":
		return applyUltra(body, opts)
	case "rtk":
		return applyRTK(body)
	case "summarizer":
		return applySummarizer(body, opts)
	default:
		return Result{Body: body, Compressed: false}
	}
}

// applyRTK wraps rtkCompressInPlace into the standard Result shape.
func applyRTK(body map[string]any) Result {
	origTokens := snapshotTokens(body)
	stats := rtkCompressInPlace(body)
	if stats == nil || stats.BytesAfter >= stats.BytesBefore {
		return Result{Body: body, Compressed: false}
	}
	newTokens := snapshotTokens(body)
	if newTokens >= origTokens {
		return Result{Body: body, Compressed: false}
	}
	saved := origTokens - newTokens
	pct := float64(saved) / float64(origTokens) * 100
	var techniques []string
	for _, h := range stats.Hits {
		techniques = append(techniques, h.Filter)
	}
	return Result{
		Body:       body,
		Compressed: true,
		Stats: &Stats{
			Mode:             "rtk",
			OriginalTokens:   origTokens,
			CompressedTokens: newTokens,
			SavingsPercent:   pct,
			TechniquesUsed:   techniques,
		},
	}
}

// applySummarizer runs summarizeMessage on each non-system message.
func applySummarizer(body map[string]any, opts Options) Result {
	origTokens := snapshotTokens(body)
	msgs, ok := body["messages"].([]any)
	if !ok {
		return Result{Body: body, Compressed: false}
	}
	changed := false
	for i, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role := roleOf(mm)
		if role == "system" && opts.PreserveSystemPrompt {
			continue
		}
		content, ok := mm["content"].(string)
		if !ok || len(content) < 200 {
			continue
		}
		summarized := summarizeMessage(content, len(content)/3, true)
		if len(summarized) < len(content) {
			nm := copyMsg(mm)
			nm["content"] = summarized
			msgs[i] = nm
			changed = true
		}
	}
	if !changed {
		return Result{Body: body, Compressed: false}
	}
	body["messages"] = msgs
	newTokens := snapshotTokens(body)
	if newTokens >= origTokens {
		return Result{Body: body, Compressed: false}
	}
	saved := origTokens - newTokens
	pct := float64(saved) / float64(origTokens) * 100
	return Result{
		Body:       body,
		Compressed: true,
		Stats: &Stats{
			Mode:             "summarizer",
			OriginalTokens:   origTokens,
			CompressedTokens: newTokens,
			SavingsPercent:   pct,
			TechniquesUsed:   []string{"summarizer"},
		},
	}
}

// RunStacked runs multiple engines in sequence on the same body.
// Returns the final result plus per-step breakdowns.
func RunStacked(body map[string]any, engines []string, opts Options) (Result, []StepBreakdown) {
	var steps []StepBreakdown
	prevTokens := snapshotTokens(body)
	for _, eng := range engines {
		res := ApplyEngine(body, eng, opts)
		curTokens := prevTokens
		if res.Compressed && res.Stats != nil {
			curTokens = res.Stats.CompressedTokens
			body = res.Body
		}
		steps = append(steps, StepBreakdown{
			Name:         eng,
			TokensBefore: prevTokens,
			TokensAfter:  curTokens,
			SavingsPct:   safePct(prevTokens, curTokens),
		})
		prevTokens = curTokens
	}

	origTokens := snapshotTokens(body)
	// Re-estimate from the very first snapshot
	if len(steps) > 0 {
		origTokens = steps[0].TokensBefore
	}
	finalTokens := prevTokens
	if finalTokens >= origTokens {
		return Result{Body: body, Compressed: false}, steps
	}
	return Result{
		Body:       body,
		Compressed: true,
		Stats: &Stats{
			Mode:             Mode("stacked"),
			OriginalTokens:   origTokens,
			CompressedTokens: finalTokens,
			SavingsPercent:   safePct(origTokens, finalTokens),
			TechniquesUsed:   engines,
		},
	}, steps
}

// WordDiff computes a simple word-level diff between original and compressed.
func WordDiff(original, compressed string) []DiffSegment {
	origWords := strings.Fields(original)
	compWords := strings.Fields(compressed)

	// Build LCS table
	m, n := len(origWords), len(compWords)
	if m == 0 && n == 0 {
		return nil
	}
	// Cap to avoid OOM on huge inputs
	if m > 2000 {
		m = 2000
		origWords = origWords[:m]
	}
	if n > 2000 {
		n = 2000
		compWords = compWords[:n]
	}

	lcs := make([][]int, m+1)
	for i := range lcs {
		lcs[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if origWords[i-1] == compWords[j-1] {
				lcs[i][j] = lcs[i-1][j-1] + 1
			} else if lcs[i-1][j] >= lcs[i][j-1] {
				lcs[i][j] = lcs[i-1][j]
			} else {
				lcs[i][j] = lcs[i][j-1]
			}
		}
	}

	// Backtrack to produce diff segments
	var segments []DiffSegment
	i, j := m, n
	var pendingKeep, pendingRemove, pendingAdd []string

	flush := func() {
		if len(pendingRemove) > 0 {
			segments = append(segments, DiffSegment{Type: "remove", Text: strings.Join(pendingRemove, " ")})
			pendingRemove = nil
		}
		if len(pendingAdd) > 0 {
			segments = append(segments, DiffSegment{Type: "add", Text: strings.Join(pendingAdd, " ")})
			pendingAdd = nil
		}
		if len(pendingKeep) > 0 {
			segments = append(segments, DiffSegment{Type: "keep", Text: strings.Join(pendingKeep, " ")})
			pendingKeep = nil
		}
	}

	for i > 0 || j > 0 {
		if i > 0 && j > 0 && origWords[i-1] == compWords[j-1] {
			flush()
			pendingKeep = append([]string{origWords[i-1]}, pendingKeep...)
			i--
			j--
		} else if j > 0 && (i == 0 || lcs[i][j-1] >= lcs[i-1][j]) {
			pendingAdd = append([]string{compWords[j-1]}, pendingAdd...)
			j--
		} else if i > 0 {
			pendingRemove = append([]string{origWords[i-1]}, pendingRemove...)
			i--
		}
	}
	flush()

	// Reverse segments (we built them backwards)
	for l, r := 0, len(segments)-1; l < r; l, r = l+1, r-1 {
		segments[l], segments[r] = segments[r], segments[l]
	}
	return segments
}

// ExtractText pulls the first user message content from a body.
func ExtractText(body map[string]any) string {
	msgs, ok := body["messages"].([]any)
	if !ok {
		return ""
	}
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if s, ok := mm["content"].(string); ok {
			return s
		}
	}
	return ""
}

// BuildBody wraps text into an OpenAI-style chat body for engine processing.
func BuildBody(text string) map[string]any {
	var body map[string]any
	_ = json.Unmarshal([]byte(`{"messages":[{"role":"user","content":""}]}`), &body)
	if msgs, ok := body["messages"].([]any); ok && len(msgs) > 0 {
		if mm, ok := msgs[0].(map[string]any); ok {
			mm["content"] = text
		}
	}
	return body
}

func safePct(before, after int) float64 {
	if before <= 0 {
		return 0
	}
	return float64(before-after) / float64(before) * 100
}

// EstimateTokensExported exposes token estimation for handlers.
func EstimateTokensExported(text string) int {
	return estimateTokens(text)
}
