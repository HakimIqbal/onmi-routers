package eval

import (
	"context"
	"testing"

	"foxrouters/internal/compression"
)

// stubClient implements compression.ModelClient for testing.
type stubClient struct {
	responses []string
	callIdx   int
}

func (s *stubClient) Complete(ctx context.Context, model string, messages []compression.ChatTurn) (string, error) {
	if s.callIdx >= len(s.responses) {
		return "", nil
	}
	resp := s.responses[s.callIdx]
	s.callIdx++
	return resp, nil
}

func TestBuildGradePrompt(t *testing.T) {
	turns := BuildGradePrompt("candidate answer", "gold answer")
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(turns))
	}
	if turns[0].Role != "system" {
		t.Error("first turn should be system")
	}
	if turns[1].Role != "user" {
		t.Error("second turn should be user")
	}
}

func TestParseGradeVerdict(t *testing.T) {
	cases := []struct {
		raw     string
		correct bool
	}{
		{"VERDICT: CORRECT", true},
		{"The answer is correct", true},
		{"VERDICT: INCORRECT", false},
		{"This is incorrect", false},
		{"I cannot determine", false}, // conservative: not clearly correct
		{"", false},
	}
	for _, c := range cases {
		v := ParseGradeVerdict(c.raw)
		if v.Correct != c.correct {
			t.Errorf("ParseGradeVerdict(%q) = %v, want %v", c.raw, v.Correct, c.correct)
		}
	}
}

func TestComputeSavings(t *testing.T) {
	full := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "This is a long message with many tokens that should be compressed"},
		},
	}
	compressed := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "Short message"},
		},
	}
	s := ComputeSavings(full, compressed, 0.01)
	if s.TokensBefore <= s.TokensAfter {
		t.Errorf("expected savings: before=%d after=%d", s.TokensBefore, s.TokensAfter)
	}
	if s.Ratio >= 1.0 {
		t.Errorf("expected ratio < 1.0, got %f", s.Ratio)
	}
	if s.CostDelta == nil || *s.CostDelta <= 0 {
		t.Error("expected positive cost delta")
	}
}

func TestSeedCorpus(t *testing.T) {
	corpus := SeedCorpus()
	if len(corpus) == 0 {
		t.Fatal("seed corpus should not be empty")
	}
	kinds := make(map[ContentKind]bool)
	for _, c := range corpus {
		if c.ID == "" || c.Context == "" || c.Question == "" {
			t.Errorf("case %s missing required fields", c.ID)
		}
		kinds[c.Kind] = true
	}
	// Should cover all 5 kinds
	expected := []ContentKind{KindProse, KindCode, KindLogs, KindToolOutputJSON, KindMultiTurn}
	for _, k := range expected {
		if !kinds[k] {
			t.Errorf("corpus missing kind %s", k)
		}
	}
}

func TestRunEvalAbortsOnBadJudge(t *testing.T) {
	// Stub returns SAME for both control pairs → judge fails self-test
	client := &stubClient{responses: []string{"VERDICT: SAME", "VERDICT: SAME"}}
	opts := RunEvalOptions{
		Corpus:      SeedCorpus()[:1],
		Client:      client,
		Mode:        compression.ModeStandard,
		AnswerModel: "test-model",
		JudgeModel:  "test-judge",
	}
	result := RunEval(context.Background(), opts)
	if !result.Aborted {
		t.Error("expected abort on failed judge self-test")
	}
	if result.Report != nil {
		t.Error("report should be nil when aborted")
	}
}

func TestRunEvalSuccess(t *testing.T) {
	// Stub returns: good→SAME, degraded→DIFFERS (self-test passes),
	// then full answer, compressed answer, judge verdict
	client := &stubClient{responses: []string{
		"VERDICT: SAME",              // self-test: good
		"VERDICT: MATERIALLY_DIFFERS", // self-test: degraded
		"Full answer text",           // model(full)
		"Compressed answer text",     // model(compressed)
		"VERDICT: SAME",              // judge fidelity
	}}
	opts := RunEvalOptions{
		Corpus:      SeedCorpus()[:1],
		Client:      client,
		Mode:        compression.ModeStandard,
		AnswerModel: "test-model",
		JudgeModel:  "test-judge",
	}
	result := RunEval(context.Background(), opts)
	if result.Aborted {
		t.Fatalf("unexpected abort: %s", result.AbortReason)
	}
	if result.Report == nil {
		t.Fatal("expected report")
	}
	if result.Report.CasesScored != 1 {
		t.Errorf("expected 1 scored case, got %d", result.Report.CasesScored)
	}
	if result.Report.FidelityPreservedPct != 100.0 {
		t.Errorf("expected 100%% fidelity, got %f", result.Report.FidelityPreservedPct)
	}
}
