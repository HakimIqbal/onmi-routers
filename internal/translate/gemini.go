// Package translate — format converters between OpenAI, Anthropic (partial), and Gemini.
// Used by multi-format gateway entrypoints.
package translate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ── OpenAI → Gemini ──────────────────────────────────────────────────────────

// GeminiGenerateRequest is a subset of Google Generative Language API request.
type GeminiGenerateRequest struct {
	Contents         []GeminiContent         `json:"contents"`
	SystemInstruction *GeminiContent         `json:"systemInstruction,omitempty"`
	GenerationConfig  *GeminiGenerationConfig `json:"generationConfig,omitempty"`
	Tools             []GeminiTool            `json:"tools,omitempty"`
}

type GeminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []GeminiPart `json:"parts"`
}

type GeminiPart struct {
	Text             string            `json:"text,omitempty"`
	FunctionCall     *GeminiFuncCall   `json:"functionCall,omitempty"`
	FunctionResponse *GeminiFuncResp   `json:"functionResponse,omitempty"`
	InlineData       *GeminiInlineData `json:"inlineData,omitempty"`
}

type GeminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type GeminiFuncCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type GeminiFuncResp struct {
	Name     string `json:"name"`
	Response any    `json:"response"`
}

type GeminiGenerationConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

type GeminiTool struct {
	FunctionDeclarations []GeminiFuncDecl `json:"functionDeclarations,omitempty"`
}

type GeminiFuncDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// OpenAIToGemini converts an OpenAI chat.completions body to Gemini generateContent body.
func OpenAIToGemini(body map[string]any) (*GeminiGenerateRequest, string, error) {
	if body == nil {
		return nil, "", fmt.Errorf("empty body")
	}
	model, _ := body["model"].(string)
	model = strings.TrimPrefix(model, "models/")
	model = strings.TrimPrefix(model, "gemini/")
	out := &GeminiGenerateRequest{}

	// system
	if msgs, ok := body["messages"].([]any); ok {
		var contents []GeminiContent
		for _, m := range msgs {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			role, _ := mm["role"].(string)
			text := extractOAText(mm["content"])
			switch role {
			case "system":
				if strings.TrimSpace(text) != "" {
					out.SystemInstruction = &GeminiContent{Parts: []GeminiPart{{Text: text}}}
				}
			case "assistant":
				// tool_calls
				if tcs, ok := mm["tool_calls"].([]any); ok && len(tcs) > 0 {
					var parts []GeminiPart
					if text != "" {
						parts = append(parts, GeminiPart{Text: text})
					}
					for _, tc := range tcs {
						tcm, _ := tc.(map[string]any)
						fn, _ := tcm["function"].(map[string]any)
						name, _ := fn["name"].(string)
						argsRaw, _ := fn["arguments"].(string)
						var args map[string]any
						_ = json.Unmarshal([]byte(argsRaw), &args)
						parts = append(parts, GeminiPart{FunctionCall: &GeminiFuncCall{Name: name, Args: args}})
					}
					contents = append(contents, GeminiContent{Role: "model", Parts: parts})
					continue
				}
				contents = append(contents, GeminiContent{Role: "model", Parts: []GeminiPart{{Text: text}}})
			case "tool":
				name, _ := mm["name"].(string)
				if name == "" {
					name = "tool"
				}
				var resp any = text
				var parsed any
				if json.Unmarshal([]byte(text), &parsed) == nil {
					resp = parsed
				}
				contents = append(contents, GeminiContent{Role: "user", Parts: []GeminiPart{{
					FunctionResponse: &GeminiFuncResp{Name: name, Response: resp},
				}}})
			default: // user
				contents = append(contents, GeminiContent{Role: "user", Parts: []GeminiPart{{Text: text}}})
			}
		}
		out.Contents = contents
	}

	cfg := &GeminiGenerationConfig{}
	if t, ok := asFloat(body["temperature"]); ok {
		cfg.Temperature = &t
	}
	if t, ok := asFloat(body["top_p"]); ok {
		cfg.TopP = &t
	}
	if n, ok := asInt(body["max_tokens"]); ok {
		cfg.MaxOutputTokens = n
	}
	if n, ok := asInt(body["max_completion_tokens"]); ok && cfg.MaxOutputTokens == 0 {
		cfg.MaxOutputTokens = n
	}
	if stops, ok := body["stop"].([]any); ok {
		for _, s := range stops {
			if ss, ok := s.(string); ok {
				cfg.StopSequences = append(cfg.StopSequences, ss)
			}
		}
	}
	if cfg.Temperature != nil || cfg.TopP != nil || cfg.MaxOutputTokens > 0 || len(cfg.StopSequences) > 0 {
		out.GenerationConfig = cfg
	}

	// tools
	if tools, ok := body["tools"].([]any); ok {
		var decls []GeminiFuncDecl
		for _, t := range tools {
			tm, _ := t.(map[string]any)
			if typ, _ := tm["type"].(string); typ != "" && typ != "function" {
				continue
			}
			fn, _ := tm["function"].(map[string]any)
			if fn == nil {
				fn = tm
			}
			name, _ := fn["name"].(string)
			if name == "" {
				continue
			}
			desc, _ := fn["description"].(string)
			params, _ := fn["parameters"].(map[string]any)
			decls = append(decls, GeminiFuncDecl{Name: name, Description: desc, Parameters: params})
		}
		if len(decls) > 0 {
			out.Tools = []GeminiTool{{FunctionDeclarations: decls}}
		}
	}
	return out, model, nil
}

// GeminiToOpenAI converts a Gemini generateContent response into OpenAI chat.completion JSON.
func GeminiToOpenAI(model string, raw []byte) ([]byte, error) {
	var gr struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string          `json:"text"`
					FunctionCall *GeminiFuncCall `json:"functionCall"`
				} `json:"parts"`
				Role string `json:"role"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(raw, &gr); err != nil {
		return nil, err
	}
	var text strings.Builder
	var toolCalls []map[string]any
	finish := "stop"
	if len(gr.Candidates) > 0 {
		c := gr.Candidates[0]
		switch strings.ToUpper(c.FinishReason) {
		case "MAX_TOKENS":
			finish = "length"
		case "SAFETY", "RECITATION":
			finish = "content_filter"
		case "STOP", "":
			finish = "stop"
		}
		for i, p := range c.Content.Parts {
			if p.FunctionCall != nil {
				args, _ := json.Marshal(p.FunctionCall.Args)
				toolCalls = append(toolCalls, map[string]any{
					"id":   fmt.Sprintf("call_%d", i),
					"type": "function",
					"function": map[string]any{
						"name":      p.FunctionCall.Name,
						"arguments": string(args),
					},
				})
				finish = "tool_calls"
			}
			if p.Text != "" {
				text.WriteString(p.Text)
			}
		}
	}
	msg := map[string]any{"role": "assistant", "content": text.String()}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
		if text.Len() == 0 {
			msg["content"] = nil
		}
	}
	out := map[string]any{
		"id":      "chatcmpl-gemini",
		"object":  "chat.completion",
		"created": 0,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": finish,
		}},
		"usage": map[string]any{
			"prompt_tokens":     gr.UsageMetadata.PromptTokenCount,
			"completion_tokens": gr.UsageMetadata.CandidatesTokenCount,
			"total_tokens":      gr.UsageMetadata.TotalTokenCount,
		},
	}
	return json.Marshal(out)
}

// GeminiRequestToOpenAI converts Gemini generateContent request → OpenAI chat body.
func GeminiRequestToOpenAI(model string, raw []byte) (map[string]any, error) {
	var gr GeminiGenerateRequest
	if err := json.Unmarshal(raw, &gr); err != nil {
		return nil, err
	}
	var msgs []any
	if gr.SystemInstruction != nil {
		var sb strings.Builder
		for _, p := range gr.SystemInstruction.Parts {
			sb.WriteString(p.Text)
		}
		if sb.Len() > 0 {
			msgs = append(msgs, map[string]any{"role": "system", "content": sb.String()})
		}
	}
	for _, c := range gr.Contents {
		role := "user"
		if c.Role == "model" {
			role = "assistant"
		}
		var text strings.Builder
		var toolCalls []any
		for i, p := range c.Parts {
			if p.FunctionCall != nil {
				args, _ := json.Marshal(p.FunctionCall.Args)
				toolCalls = append(toolCalls, map[string]any{
					"id":   fmt.Sprintf("call_%d", i),
					"type": "function",
					"function": map[string]any{"name": p.FunctionCall.Name, "arguments": string(args)},
				})
			}
			if p.FunctionResponse != nil {
				b, _ := json.Marshal(p.FunctionResponse.Response)
				msgs = append(msgs, map[string]any{
					"role": "tool", "name": p.FunctionResponse.Name, "content": string(b),
				})
				continue
			}
			text.WriteString(p.Text)
		}
		if len(toolCalls) > 0 {
			m := map[string]any{"role": "assistant", "content": text.String(), "tool_calls": toolCalls}
			msgs = append(msgs, m)
			continue
		}
		if text.Len() > 0 || role == "user" {
			msgs = append(msgs, map[string]any{"role": role, "content": text.String()})
		}
	}
	out := map[string]any{"model": model, "messages": msgs}
	if gr.GenerationConfig != nil {
		if gr.GenerationConfig.Temperature != nil {
			out["temperature"] = *gr.GenerationConfig.Temperature
		}
		if gr.GenerationConfig.TopP != nil {
			out["top_p"] = *gr.GenerationConfig.TopP
		}
		if gr.GenerationConfig.MaxOutputTokens > 0 {
			out["max_tokens"] = gr.GenerationConfig.MaxOutputTokens
		}
		if len(gr.GenerationConfig.StopSequences) > 0 {
			out["stop"] = gr.GenerationConfig.StopSequences
		}
	}
	if len(gr.Tools) > 0 {
		var tools []any
		for _, t := range gr.Tools {
			for _, d := range t.FunctionDeclarations {
				tools = append(tools, map[string]any{
					"type": "function",
					"function": map[string]any{
						"name": d.Name, "description": d.Description, "parameters": d.Parameters,
					},
				})
			}
		}
		if len(tools) > 0 {
			out["tools"] = tools
		}
	}
	return out, nil
}

func extractOAText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var b strings.Builder
		for _, p := range t {
			if pm, ok := p.(map[string]any); ok {
				if s, ok := pm["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case json.Number:
		i, err := t.Int64()
		return int(i), err == nil
	default:
		return 0, false
	}
}
