package compression

import (
	"context"
	"regexp"
	"strings"
)

// ── Fidelity judge (ported from OmniRoute eval/judge.ts) ───────────────────
// An LLM-backed judge that decides whether an answer produced from a COMPRESSED
// context MATERIALLY differs from one produced from the FULL context. Used by
// the offline eval framework (not the hot path) to verify compression quality.
//
// The judge is validated with a CONTROL PAIR before it is trusted: it must flag
// a known-degraded answer as MATERIALLY_DIFFERS and a known-good answer as SAME.
// A judge that mis-ranks either is untrusted and the eval run aborts.

// Verdict is the judge's classification of a compressed answer.
type Verdict string

const (
	VerdictSame              Verdict = "same"
	VerdictMateriallyDiffers Verdict = "materially-differs"
	VerdictUnparseable       Verdict = "unparseable"
)

// ChatTurn is one message in a judge conversation.
type ChatTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ModelClient is the minimal LLM interface the judge needs. Callers inject a
// concrete implementation (e.g. one that calls the gateway's own
// /v1/chat/completions). Keeping it an interface makes the judge unit-testable
// with a stub and decoupled from any specific provider.
type ModelClient interface {
	// Complete sends the turns to the model and returns the assistant text.
	Complete(ctx context.Context, model string, messages []ChatTurn) (string, error)
}

// BuildJudgePrompt constructs the judge conversation asking whether the
// compressed-context answer (B) materially differs from the full-context
// answer (A). Mirrors OmniRoute's buildJudgePrompt.
func BuildJudgePrompt(fullAnswer, compressedAnswer string) []ChatTurn {
	return []ChatTurn{
		{
			Role: "system",
			Content: "You are a strict evaluation judge. You are given two answers to the same question: " +
				"answer A produced from the full context, and answer B produced from a compressed context. " +
				"Decide whether B MATERIALLY differs from A (a difference that changes the substance, " +
				"correctness, or key facts — NOT mere wording/format). Reply with exactly one final line: " +
				"`VERDICT: SAME` or `VERDICT: MATERIALLY_DIFFERS`.",
		},
		{
			Role:    "user",
			Content: "Answer A (full context):\n" + fullAnswer + "\n\nAnswer B (compressed context):\n" + compressedAnswer,
		},
	}
}

var (
	verdictDiffersRe = regexp.MustCompile(`(?i)materially[_\s-]*differs|differs[_\s]+materially|\bdiffers\b`)
	verdictSameRe    = regexp.MustCompile(`(?i)verdict:\s*same|\bsame\b`)
)

// ParseJudgeVerdict parses a judge's raw output into a Verdict. Tolerant of
// case/format; unrecognized output yields VerdictUnparseable (never guessed).
func ParseJudgeVerdict(raw string) Verdict {
	if verdictDiffersRe.MatchString(raw) {
		return VerdictMateriallyDiffers
	}
	if verdictSameRe.MatchString(raw) {
		return VerdictSame
	}
	return VerdictUnparseable
}

// ControlPair is the known-good/known-degraded pair used to self-test a judge.
// The judge must rank Degraded as MATERIALLY_DIFFERS and Good as SAME relative
// to the same Reference. Mirrors OmniRoute's CONTROL_PAIR.
var ControlPair = struct {
	Reference string
	Good      string
	Degraded  string
}{
	Reference: "The function returns 3 because the input is clamped to the upper bound.",
	Good:      "It returns 3 since the value is clamped to the maximum allowed.",
	Degraded:  "It returns 0 because the value is set to zero.",
}

// SelfTestResult reports whether a judge passed the control-pair validation.
type SelfTestResult struct {
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

// RunSelfTest validates a judge against the control pair. PASS requires the
// degraded control => materially-differs AND the good control => same. Any
// other outcome (including unparseable) FAILS, so callers abort before
// trusting the judge's scores.
func RunSelfTest(ctx context.Context, client ModelClient, judgeModel string) SelfTestResult {
	goodRaw, err := client.Complete(ctx, judgeModel, BuildJudgePrompt(ControlPair.Reference, ControlPair.Good))
	if err != nil {
		return SelfTestResult{Passed: false, Detail: "judge call (good) failed: " + err.Error()}
	}
	degradedRaw, err := client.Complete(ctx, judgeModel, BuildJudgePrompt(ControlPair.Reference, ControlPair.Degraded))
	if err != nil {
		return SelfTestResult{Passed: false, Detail: "judge call (degraded) failed: " + err.Error()}
	}

	goodVerdict := ParseJudgeVerdict(goodRaw)
	degradedVerdict := ParseJudgeVerdict(degradedRaw)

	if degradedVerdict != VerdictMateriallyDiffers {
		return SelfTestResult{Passed: false, Detail: `judge failed to flag the known-degraded control (got "` + string(degradedVerdict) + `")`}
	}
	if goodVerdict != VerdictSame {
		return SelfTestResult{Passed: false, Detail: `judge flagged the known-good control as "` + string(goodVerdict) + `" (expected same)`}
	}
	return SelfTestResult{Passed: true, Detail: "control pair ranked correctly"}
}

// JudgeFidelity runs the judge on a single full/compressed answer pair and
// returns the verdict. Convenience wrapper over BuildJudgePrompt + Complete +
// ParseJudgeVerdict.
func JudgeFidelity(ctx context.Context, client ModelClient, judgeModel, fullAnswer, compressedAnswer string) (Verdict, error) {
	raw, err := client.Complete(ctx, judgeModel, BuildJudgePrompt(fullAnswer, compressedAnswer))
	if err != nil {
		return VerdictUnparseable, err
	}
	return ParseJudgeVerdict(raw), nil
}

// normalizeLower is a small helper kept for parity / future use.
func normalizeLower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
