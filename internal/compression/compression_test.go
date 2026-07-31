package compression

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func mkBody(contents ...string) map[string]any {
	msgs := make([]any, 0, len(contents))
	for i, c := range contents {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, map[string]any{"role": role, "content": c})
	}
	return map[string]any{"messages": msgs}
}

func TestOffModeNoop(t *testing.T) {
	body := mkBody("Hello there, please help me with my code.")
	res := Apply(body, ModeOff, Options{})
	if res.Compressed {
		t.Fatal("off mode must not compress")
	}
}

func TestLiteWhitespace(t *testing.T) {
	body := mkBody("line1\n\n\n\n\nline2 with trailing spaces   \n\n\n\nline3")
	res := Apply(body, ModeLite, Options{})
	if !res.Compressed {
		t.Fatal("lite should compress newline runs")
	}
	out := extractText(messagesOf(res.Body)[0].(map[string]any)["content"])
	if strings.Contains(out, "\n\n\n") {
		t.Fatalf("newline runs not collapsed: %q", out)
	}
	if res.Stats.SavingsPercent <= 0 {
		t.Fatal("expected positive savings")
	}
}

func TestLiteToolTruncate(t *testing.T) {
	long := strings.Repeat("word ", 1000) // ~5000 chars > 2000
	body := map[string]any{"messages": []any{
		map[string]any{"role": "tool", "content": long},
	}}
	res := Apply(body, ModeLite, Options{})
	if !res.Compressed {
		t.Fatal("lite should truncate long tool result")
	}
	out := extractText(messagesOf(res.Body)[0].(map[string]any)["content"])
	if !strings.Contains(out, "...[truncated]") {
		t.Fatal("expected truncation marker")
	}
}

func TestLitePreservesSystem(t *testing.T) {
	body := map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "System\n\n\n\n\nprompt   "},
	}}
	res := Apply(body, ModeLite, Options{PreserveSystemPrompt: true})
	if res.Compressed {
		t.Fatal("lite must not touch system prompt when preserved")
	}
}

func TestCavemanStripsFiller(t *testing.T) {
	body := mkBody("Hello, I would like you to please help me. Basically I just want to essentially fix the bug. Thank you so much!")
	res := Apply(body, ModeStandard, Options{})
	if !res.Compressed {
		t.Fatal("caveman should strip filler")
	}
	out := strings.ToLower(extractText(messagesOf(res.Body)[0].(map[string]any)["content"]))
	for _, filler := range []string{"please", "basically", "essentially", "thank you"} {
		if strings.Contains(out, filler) {
			t.Fatalf("filler %q not stripped: %q", filler, out)
		}
	}
	if res.Stats.SavingsPercent <= 0 {
		t.Fatal("expected positive savings")
	}
}

func TestCavemanProtectsCode(t *testing.T) {
	code := "Please fix this:\n```go\nfunc main() { fmt.Println(\"please basically\") }\n```"
	body := mkBody(code)
	res := Apply(body, ModeStandard, Options{})
	out := extractText(messagesOf(res.Body)[0].(map[string]any)["content"])
	// code block content must be untouched
	if !strings.Contains(out, `fmt.Println("please basically")`) {
		t.Fatalf("code block was rewritten: %q", out)
	}
}

func TestUltraPrunes(t *testing.T) {
	body := mkBody("the quick brown fox is a very simple animal that we can usually see in the forest and it is just there")
	res := Apply(body, ModeUltra, Options{})
	if !res.Compressed {
		t.Fatal("ultra should prune stopwords")
	}
	out := extractText(messagesOf(res.Body)[0].(map[string]any)["content"])
	// stopwords like "the", "is", "a" should be reduced
	if len(out) >= len("the quick brown fox is a very simple animal that we can usually see in the forest and it is just there") {
		t.Fatalf("ultra did not shrink: %q", out)
	}
}

func TestUltraPreservesNumbers(t *testing.T) {
	body := mkBody("the error code is 404 and the port is 8080 with timeout 30")
	res := Apply(body, ModeUltra, Options{})
	out := extractText(messagesOf(res.Body)[0].(map[string]any)["content"])
	for _, num := range []string{"404", "8080", "30"} {
		if !strings.Contains(out, num) {
			t.Fatalf("ultra pruned a number %q: %q", num, out)
		}
	}
}

func TestAggressiveAging(t *testing.T) {
	// Build 10 messages; oldest should get summarized/aged.
	var msgs []any
	for i := 0; i < 10; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, map[string]any{"role": role, "content": "This is message number " + strings.Repeat("padding content here ", 20)})
	}
	body := map[string]any{"messages": msgs}
	res := Apply(body, ModeAggressive, Options{})
	if !res.Compressed {
		t.Fatal("aggressive should compress long conversations")
	}
	outMsgs := messagesOf(res.Body)
	// oldest message (index 0) should be tagged compressed
	oldest := extractText(outMsgs[0].(map[string]any)["content"])
	if !strings.HasPrefix(strings.TrimSpace(oldest), "[COMPRESSED:") {
		t.Fatalf("oldest message not aged: %q", oldest[:min(60, len(oldest))])
	}
	// newest message should be verbatim (untagged)
	newest := extractText(outMsgs[len(outMsgs)-1].(map[string]any)["content"])
	if strings.HasPrefix(strings.TrimSpace(newest), "[COMPRESSED:") {
		t.Fatal("newest message should be verbatim")
	}
}

func TestNeverGrows(t *testing.T) {
	// Short content — no mode should grow it.
	body := mkBody("hi")
	for _, mode := range []Mode{ModeLite, ModeStandard, ModeAggressive, ModeUltra} {
		res := Apply(body, mode, Options{})
		if res.Compressed {
			out := extractText(messagesOf(res.Body)[0].(map[string]any)["content"])
			if len(out) > len("hi") {
				t.Fatalf("mode %s grew short content: %q", mode, out)
			}
		}
	}
}

func TestModeParse(t *testing.T) {
	cases := map[string]Mode{
		"off": ModeOff, "lite": ModeLite, "standard": ModeStandard,
		"aggressive": ModeAggressive, "ultra": ModeUltra, "garbage": ModeOff, "": ModeOff,
	}
	for in, want := range cases {
		if got := ParseMode(in); got != want {
			t.Errorf("ParseMode(%q)=%q want %q", in, got, want)
		}
	}
}

func TestFidelityGateRejectsTinySavings(t *testing.T) {
	// Unit-test the gate directly: lossy modes must clear the 3% bar.
	if fidelityAccept(ModeStandard, 2.9) {
		t.Fatal("standard at 2.9% should be rejected")
	}
	if fidelityAccept(ModeUltra, 1.0) {
		t.Fatal("ultra at 1.0% should be rejected")
	}
	if !fidelityAccept(ModeStandard, 3.0) {
		t.Fatal("standard at 3.0% should be accepted")
	}
	if !fidelityAccept(ModeAggressive, 50.0) {
		t.Fatal("aggressive at 50% should be accepted")
	}
}

func TestFidelityGateAllowsLite(t *testing.T) {
	// Lite is exempt from the fidelity gate (near-lossless).
	body := mkBody("a\n\n\n\n\nb")
	res := Apply(body, ModeLite, Options{})
	// Even tiny savings pass for lite.
	if !res.Compressed {
		t.Fatal("lite should be exempt from fidelity gate")
	}
}

func TestThinkTagsPreserved(t *testing.T) {
	// Build the tag via hex escapes so the literal tag string never appears in
	// this source file (tooling strips it as markup). "\x3c"=='<', "\x3e"=='>'.
	open := "\x3cthink\x3e"
	closeTag := "\x3c/think\x3e"
	content := "Please help me. " + open + "thinking here basically actually" + closeTag
	body := mkBody(content)
	res := Apply(body, ModeStandard, Options{})
	out := extractText(messagesOf(res.Body)[0].(map[string]any)["content"])
	if !strings.Contains(out, open) || !strings.Contains(out, closeTag) {
		t.Fatalf(" tags were mangled: %q", out)
	}
	if !strings.Contains(out, "thinking here basically actually") {
		t.Fatalf(" content was rewritten: %q", out)
	}
	// prose filler before the tag should still be compressed
	if strings.Contains(out, "Please help me.") {
		t.Fatalf("prose filler not compressed: %q", out)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestCacheAwareDowngrade(t *testing.T) {
	// Aggressive on a caching provider (anthropic) must downgrade to standard.
	adjusted, preserve := cacheAwareAdjust(ModeAggressive, "anthropic", nil)
	if adjusted != ModeStandard {
		t.Fatalf("expected downgrade to standard, got %s", adjusted)
	}
	if !preserve {
		t.Fatal("expected system prompt preservation for caching provider")
	}
}

func TestCacheAwareNoDowngrade(t *testing.T) {
	// Non-caching provider: no downgrade, no forced preservation.
	adjusted, preserve := cacheAwareAdjust(ModeAggressive, "cloudflare", nil)
	if adjusted != ModeAggressive {
		t.Fatalf("expected no downgrade, got %s", adjusted)
	}
	if preserve {
		t.Fatal("non-caching provider should not force preservation")
	}
}

func TestCacheAwareDetectsMarkers(t *testing.T) {
	// Explicit cache_control markers trigger cache-aware even without provider.
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "test", "cache_control": map[string]any{"type": "ephemeral"}},
		},
	}
	adjusted, preserve := cacheAwareAdjust(ModeUltra, "", body)
	if adjusted != ModeStandard {
		t.Fatalf("cache_control markers should trigger downgrade, got %s", adjusted)
	}
	if !preserve {
		t.Fatal("cache_control markers should force preservation")
	}
}

func TestCacheAwareLiteUnchanged(t *testing.T) {
	// Lite is never downgraded (already safe).
	adjusted, _ := cacheAwareAdjust(ModeLite, "openai", nil)
	if adjusted != ModeLite {
		t.Fatalf("lite should stay lite, got %s", adjusted)
	}
}

func TestInferProvider(t *testing.T) {
	cases := map[string]string{
		"@cf/meta/llama-3.3": "cloudflare",
		"grok-4.5":           "grok",
		"cb/deepseek":        "codebuddy",
		"gpt-4o":             "openai",
		"claude-sonnet-4":    "anthropic",
		"gemini-2.5-pro":     "google",
		"deepseek-chat":      "deepseek",
	}
	for model, want := range cases {
		if got := inferProvider(model); got != want {
			t.Errorf("inferProvider(%q)=%q want %q", model, got, want)
		}
	}
}

// ── Judge tests (stub ModelClient) ──

type stubJudgeClient struct {
	responses []string
	calls     int
}

func (s *stubJudgeClient) Complete(ctx context.Context, model string, msgs []ChatTurn) (string, error) {
	if s.calls >= len(s.responses) {
		return "", fmt.Errorf("stub exhausted")
	}
	r := s.responses[s.calls]
	s.calls++
	return r, nil
}

func TestParseJudgeVerdict(t *testing.T) {
	cases := map[string]Verdict{
		"VERDICT: SAME":                VerdictSame,
		"verdict: same":                VerdictSame,
		"The answers are the same":     VerdictSame,
		"VERDICT: MATERIALLY_DIFFERS":  VerdictMateriallyDiffers,
		"these materially differs from the original": VerdictMateriallyDiffers,
		"B differs from A":             VerdictMateriallyDiffers,
		"I cannot determine":           VerdictUnparseable,
		"":                             VerdictUnparseable,
	}
	for raw, want := range cases {
		if got := ParseJudgeVerdict(raw); got != want {
			t.Errorf("ParseJudgeVerdict(%q)=%q want %q", raw, got, want)
		}
	}
}

func TestJudgeSelfTestPass(t *testing.T) {
	// Stub returns: good=>SAME, degraded=>MATERIALLY_DIFFERS (correct order)
	client := &stubJudgeClient{responses: []string{"VERDICT: SAME", "VERDICT: MATERIALLY_DIFFERS"}}
	res := RunSelfTest(context.Background(), client, "test-model")
	if !res.Passed {
		t.Fatalf("expected self-test pass, got: %s", res.Detail)
	}
}

func TestJudgeSelfTestFailBadJudge(t *testing.T) {
	// Stub returns both as SAME — judge fails to flag degraded
	client := &stubJudgeClient{responses: []string{"VERDICT: SAME", "VERDICT: SAME"}}
	res := RunSelfTest(context.Background(), client, "test-model")
	if res.Passed {
		t.Fatal("expected self-test fail for bad judge")
	}
}

func TestJudgeSelfTestFailDegradedNotFlagged(t *testing.T) {
	// good=>SAME (ok), degraded=>SAME (wrong — should be differs)
	client := &stubJudgeClient{responses: []string{"VERDICT: SAME", "VERDICT: SAME"}}
	res := RunSelfTest(context.Background(), client, "test-model")
	if res.Passed {
		t.Fatal("judge that misses degraded control must fail self-test")
	}
}

func TestJudgeFidelity(t *testing.T) {
	client := &stubJudgeClient{responses: []string{"VERDICT: MATERIALLY_DIFFERS"}}
	v, err := JudgeFidelity(context.Background(), client, "m", "full answer", "compressed answer")
	if err != nil {
		t.Fatal(err)
	}
	if v != VerdictMateriallyDiffers {
		t.Fatalf("expected materially-differs, got %s", v)
	}
}

func TestBuildJudgePrompt(t *testing.T) {
	turns := BuildJudgePrompt("answer A", "answer B")
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(turns))
	}
	if turns[0].Role != "system" || turns[1].Role != "user" {
		t.Fatal("wrong roles")
	}
	if !strings.Contains(turns[1].Content, "answer A") || !strings.Contains(turns[1].Content, "answer B") {
		t.Fatal("prompt missing answers")
	}
}
