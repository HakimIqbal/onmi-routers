package translate

import (
	"encoding/json"
	"testing"
)

func TestOpenAIToGeminiRoundTrip(t *testing.T) {
	body := map[string]any{
		"model": "gemini-2.5-flash",
		"messages": []any{
			map[string]any{"role": "system", "content": "be brief"},
			map[string]any{"role": "user", "content": "hi"},
		},
		"temperature": 0.2,
		"max_tokens":  16,
	}
	g, model, err := OpenAIToGemini(body)
	if err != nil {
		t.Fatal(err)
	}
	if model != "gemini-2.5-flash" {
		t.Fatalf("model %s", model)
	}
	if g.SystemInstruction == nil || len(g.Contents) != 1 {
		t.Fatalf("unexpected gemini req: %+v", g)
	}
	raw, _ := json.Marshal(g)
	oa, err := GeminiRequestToOpenAI("cb/gemini-2.5-flash", raw)
	if err != nil {
		t.Fatal(err)
	}
	msgs, _ := oa["messages"].([]any)
	if len(msgs) < 2 {
		t.Fatalf("msgs %v", msgs)
	}
}

func TestGeminiToOpenAIToolCall(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"get_weather","args":{"city":"Jakarta"}}}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":5,"totalTokenCount":8}}`)
	out, err := GeminiToOpenAI("gemini-x", raw)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	ch := m["choices"].([]any)[0].(map[string]any)
	msg := ch["message"].(map[string]any)
	if msg["tool_calls"] == nil {
		t.Fatalf("expected tool_calls: %s", out)
	}
}
