package eval

import (
	"context"

	"foxrouters/internal/compression"
)

// ── Cost meter (ported from OmniRoute eval/costMeter.ts) ───────────────────

// costMeter tracks cumulative USD spend against an optional cap.
type costMeter struct {
	capUsd  float64 // <= 0 means unbounded
	spentUsd float64
}

func newCostMeter(capUsd float64) *costMeter {
	return &costMeter{capUsd: capUsd}
}

func (m *costMeter) add(usd float64) {
	m.spentUsd += usd
}

func (m *costMeter) exceeded() bool {
	return m.capUsd > 0 && m.spentUsd >= m.capUsd
}

// ── Runner (ported from OmniRoute eval/runner.ts) ──────────────────────────

// RunEvalOptions configures an eval run.
type RunEvalOptions struct {
	Corpus          []EvalCase
	Client          compression.ModelClient
	Mode            compression.Mode
	AnswerModel     string
	JudgeModel      string
	Provider        string
	CostCapUsd      float64 // <= 0 means unbounded
	Sample          int     // score at most N cases (0 = all)
	CostPerKTokenIn float64 // USD per 1000 input tokens (optional, for cost delta)
}

// buildBody builds a one-question chat body the pipeline + model both accept.
func buildBody(context, question string) map[string]any {
	return map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": context + "\n\nQuestion: " + question},
		},
	}
}

// answerText extracts the concatenated text content from a body's messages.
func answerText(body map[string]any) string {
	return extractText(messagesContent(body))
}

func messagesContent(body map[string]any) any {
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		return ""
	}
	// For single-message bodies, return the content directly.
	if len(msgs) == 1 {
		if mm, ok := msgs[0].(map[string]any); ok {
			return mm["content"]
		}
	}
	// Multi-message: join all text.
	var parts []any
	for _, m := range msgs {
		if mm, ok := m.(map[string]any); ok {
			parts = append(parts, mm["content"])
		}
	}
	return parts
}

// RunEval executes the offline corpus eval. It self-tests the judge FIRST
// (abort on failure), then for each case: model(full) baseline → compress via
// the REAL pipeline → model(compressed) → judge fidelity → grade gold (when
// present) → compute mechanical savings. Errored cases are recorded + excluded;
// the cost cap stops the loop and flags partial (no silent truncation).
func RunEval(ctx context.Context, opts RunEvalOptions) RunEvalResult {
	// D-D3 self-test gate — a broken judge aborts before any score is emitted.
	selfTest := compression.RunSelfTest(ctx, opts.Client, opts.JudgeModel)
	if !selfTest.Passed {
		return RunEvalResult{Aborted: true, AbortReason: "judge self-test failed: " + selfTest.Detail}
	}

	limit := len(opts.Corpus)
	if opts.Sample > 0 && opts.Sample < limit {
		limit = opts.Sample
	}
	cases := opts.Corpus[:limit]

	meter := newCostMeter(opts.CostCapUsd)
	records := make([]EvalRecord, 0, len(cases))
	partial := false

	for _, c := range cases {
		// Stop BEFORE a case if we cannot afford its (~3) model calls; flag partial.
		if meter.exceeded() {
			partial = true
			break
		}

		fullBody := buildBody(c.Context, c.Question)
		rec := EvalRecord{ID: c.ID, Kind: c.Kind}

		// 1. Baseline: model answers from FULL context.
		fullText, err := opts.Client.Complete(ctx, opts.AnswerModel, []compression.ChatTurn{
			{Role: "user", Content: answerText(fullBody)},
		})
		if err != nil {
			rec.Errored = true
			rec.ErrorDetail = "full-context call failed: " + err.Error()
			records = append(records, rec)
			continue
		}

		// 2. Compress via the REAL pipeline at the target mode.
		compResult := compression.Apply(fullBody, opts.Mode, compression.Options{
			Model:                opts.AnswerModel,
			Provider:             opts.Provider,
			PreserveSystemPrompt: true,
		})
		compressedBody := fullBody
		if compResult.Compressed {
			compressedBody = compResult.Body
		}

		// 3. Model answers from COMPRESSED context.
		compressedText, err := opts.Client.Complete(ctx, opts.AnswerModel, []compression.ChatTurn{
			{Role: "user", Content: answerText(compressedBody)},
		})
		if err != nil {
			rec.Errored = true
			rec.ErrorDetail = "compressed-context call failed: " + err.Error()
			records = append(records, rec)
			continue
		}

		// 4. Judge fidelity.
		verdict, err := compression.JudgeFidelity(ctx, opts.Client, opts.JudgeModel, fullText, compressedText)
		if err != nil {
			rec.Errored = true
			rec.ErrorDetail = "judge call failed: " + err.Error()
			records = append(records, rec)
			continue
		}
		rec.Fidelity = verdict

		// 5. Grade gold (when present).
		if c.Gold != "" {
			gfRaw, err := opts.Client.Complete(ctx, opts.JudgeModel, BuildGradePrompt(fullText, c.Gold))
			if err == nil {
				gf := ParseGradeVerdict(gfRaw)
				rec.GoldFull = &gf.Correct
			}
			gcRaw, err := opts.Client.Complete(ctx, opts.JudgeModel, BuildGradePrompt(compressedText, c.Gold))
			if err == nil {
				gc := ParseGradeVerdict(gcRaw)
				rec.GoldCompressed = &gc.Correct
			}
		}

		// 6. Mechanical savings.
		rec.Savings = ComputeSavings(fullBody, compressedBody, opts.CostPerKTokenIn)

		records = append(records, rec)
	}

	report := aggregate(opts, records, partial, meter.spentUsd)
	return RunEvalResult{Report: report}
}
