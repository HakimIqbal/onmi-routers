// Package eval — offline compression quality evaluation framework.
//
// A faithful Go port of OmniRoute's open-sse/services/compression/eval.
// Measures both MECHANICAL savings (tokens before/after) and FIDELITY
// (does the compressed context still produce a materially-same answer?).
//
// Flow per eval case:
//  1. model(full context) → baseline answer
//  2. compress via the REAL pipeline at the target mode
//  3. model(compressed context) → compressed answer
//  4. judge fidelity: does compressed answer MATERIALLY differ from baseline?
//  5. grade gold (when present): is each answer correct vs the gold?
//  6. compute mechanical savings (tokens before/after, ratio, cost delta)
//
// The judge is self-tested FIRST with a control pair; a broken judge aborts
// the entire run before any score is emitted (OmniRoute D-D3 discipline).
package eval

import (
	"regexp"
	"strings"

	"foxrouters/internal/compression"
)

// ── Types (ported from OmniRoute eval/types.ts) ────────────────────────────

// ContentKind classifies an eval case's content type.
type ContentKind string

const (
	KindToolOutputJSON ContentKind = "tool-output-json"
	KindLogs           ContentKind = "logs"
	KindCode           ContentKind = "code"
	KindProse          ContentKind = "prose"
	KindMultiTurn      ContentKind = "multi-turn"
)

// EvalCase is one evaluation scenario.
type EvalCase struct {
	ID       string      `json:"id"`
	Kind     ContentKind `json:"kind"`
	Context  string      `json:"context"`  // raw context to compress
	Question string      `json:"question"` // question asked against the context
	Gold     string      `json:"gold"`     // optional gold answer
	Captured bool        `json:"captured"` // true = curated seed; false = anonymized capture
}

// SavingsResult is the mechanical token savings for one case.
type SavingsResult struct {
	TokensBefore int      `json:"tokens_before"`
	TokensAfter  int      `json:"tokens_after"`
	Ratio        float64  `json:"ratio"` // after/before (1 = no savings)
	CostDelta    *float64 `json:"cost_delta,omitempty"` // USD saved on input, when priced
}

// EvalRecord is the outcome of one eval case.
type EvalRecord struct {
	ID               string             `json:"id"`
	Kind             ContentKind        `json:"kind"`
	Fidelity         compression.Verdict `json:"fidelity"`
	GoldFull         *bool              `json:"gold_full"`          // nil when no gold
	GoldCompressed   *bool              `json:"gold_compressed"`    // nil when no gold
	Savings          SavingsResult      `json:"savings"`
	Errored          bool               `json:"errored"`
	ErrorDetail      string             `json:"error_detail,omitempty"`
}

// KindSummary aggregates results for one content kind.
type KindSummary struct {
	Kind                 ContentKind `json:"kind"`
	CasesScored          int         `json:"cases_scored"`
	FidelityPreservedPct float64     `json:"fidelity_preserved_pct"`
	GoldAccuracyDeltaPct *float64    `json:"gold_accuracy_delta_pct"` // nil if no gold cases
	MeanRatio            float64     `json:"mean_ratio"`
}

// RunStamps identifies an eval run.
type RunStamps struct {
	AnswerModel string `json:"answer_model"`
	JudgeModel  string `json:"judge_model"`
	Mode        string `json:"mode"`
	SampleSize  int    `json:"sample_size"`
}

// EvalReport is the full eval output.
type EvalReport struct {
	Stamps              RunStamps     `json:"stamps"`
	Partial             bool          `json:"partial"`
	TotalCostUsd        float64       `json:"total_cost_usd"`
	CasesScored         int           `json:"cases_scored"`
	CasesErrored        int           `json:"cases_errored"`
	FidelityPreservedPct float64      `json:"fidelity_preserved_pct"`
	MeanRatio           float64       `json:"mean_ratio"`
	ByKind              []KindSummary `json:"by_kind"`
	Records             []EvalRecord  `json:"records"`
}

// RunEvalResult wraps the report with abort info.
type RunEvalResult struct {
	Aborted     bool        `json:"aborted"`
	AbortReason string      `json:"abort_reason,omitempty"`
	Report      *EvalReport `json:"report"`
}

// ── Grader (ported from OmniRoute eval/grader.ts) ──────────────────────────

// BuildGradePrompt constructs a gold-grading conversation. The grader judges
// meaning, not wording — a differently-phrased-but-correct answer is CORRECT.
func BuildGradePrompt(answer, gold string) []compression.ChatTurn {
	return []compression.ChatTurn{
		{
			Role: "system",
			Content: "You are a strict grader. Decide whether the candidate answer is CORRECT with respect " +
				"to the gold answer — judge meaning, not wording (a correctly-phrased-differently answer " +
				"is CORRECT). Reply with exactly one final line: `VERDICT: CORRECT` or `VERDICT: INCORRECT`.",
		},
		{Role: "user", Content: "Gold answer:\n" + gold + "\n\nCandidate answer:\n" + answer},
	}
}

var (
	gradeIncorrectRe = regexp.MustCompile(`(?i)\bincorrect\b`)
	gradeCorrectRe   = regexp.MustCompile(`(?i)\bcorrect\b`)
)

// GradeVerdict is the parsed gold-grading result.
type GradeVerdict struct {
	Correct bool   `json:"correct"`
	Raw     string `json:"raw"`
}

// ParseGradeVerdict parses a grader's raw output. Conservative: anything not
// clearly CORRECT grades INCORRECT (no benefit of the doubt).
func ParseGradeVerdict(raw string) GradeVerdict {
	if gradeIncorrectRe.MatchString(raw) {
		return GradeVerdict{Correct: false, Raw: raw}
	}
	if gradeCorrectRe.MatchString(raw) {
		return GradeVerdict{Correct: true, Raw: raw}
	}
	return GradeVerdict{Correct: false, Raw: raw}
}

// ── Savings (ported from OmniRoute eval/savings.ts) ────────────────────────

// ComputeSavings measures mechanical token savings between a full and
// compressed body. Reuses the production token estimator so the eval reports
// the same numbers the pipeline reports. costPerKTokenIn is optional (USD per
// 1000 input tokens); when > 0 the positive cost saved is reported.
func ComputeSavings(fullBody, compressedBody map[string]any, costPerKTokenIn float64) SavingsResult {
	before := estimateTokens(fullBody)
	after := estimateTokens(compressedBody)
	ratio := 1.0
	if before > 0 {
		ratio = float64(after) / float64(before)
		ratio = float64(int(ratio*10000+0.5)) / 10000
	}
	res := SavingsResult{TokensBefore: before, TokensAfter: after, Ratio: ratio}
	if costPerKTokenIn > 0 {
		saved := float64(before-after) / 1000.0 * costPerKTokenIn
		saved = float64(int(saved*1e6+0.5)) / 1e6
		res.CostDelta = &saved
	}
	return res
}

// estimateTokens approximates total tokens across all message content.
func estimateTokens(body map[string]any) int {
	total := 0
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		total += len(extractText(mm["content"])) / 4
	}
	return total
}

func extractText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, p := range c {
			part, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := part["text"].(string); ok {
				b.WriteString(t)
			}
		}
		return b.String()
	}
	return ""
}
