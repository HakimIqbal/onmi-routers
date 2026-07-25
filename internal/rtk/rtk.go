// Package rtk — RTK token saver (ported from decolua/9router open-sse/rtk).
//
// Compresses tool_result / function_call_output content in-place on LLM
// request bodies before they hit the upstream. Cuts 20-40% of input tokens
// on agentic workloads (CLAUDE/Cursor/Codex style tool dumps).
//
// Fail-open by design: any error leaves the body untouched. Never returns an
// error. Skips is_error / status:"error" results to preserve traces.
//
// Supported shapes:
//   - OpenAI  {role:"tool", content: string | [{type:"text",text}]}
//   - Claude  content:[{type:"tool_result", content: string | [{type:"text",text}]}]
//   - OpenAI Responses input:[{type:"function_call_output", output: string | [{type:"input_text",text}]}]
//   - Kiro    body.conversationState.history[].userInputMessage...toolResults[].content[].text
package rtk

import (
	"strconv"
)

// Stats reports compression savings for one request.
type Stats struct {
	BytesBefore int
	BytesAfter  int
	Hits        []Hit
}

// Hit is one compressed blob.
type Hit struct {
	Shape  string
	Filter string
	Saved  int
}

// CompressMessages compresses tool_result content in-place. Returns nil if
// disabled or nothing applicable. Mutates body. Fail-open.
func CompressMessages(body map[string]any) *Stats {
	if body == nil {
		return nil
	}
	if _, ok := body["conversationState"]; ok {
		return compressKiro(body)
	}
	var items []any
	switch v := body["messages"].(type) {
	case []any:
		items = v
	case nil:
		// OpenAI Responses uses "input"
		if inp, ok := body["input"].([]any); ok {
			items = inp
		}
	}
	if items == nil {
		return nil
	}
	stats := &Stats{}
	for _, it := range items {
		msg, ok := it.(map[string]any)
		if !ok {
			continue
		}
		// OpenAI Responses function_call_output
		if msg["type"] == "function_call_output" {
			switch out := msg["output"].(type) {
			case string:
				msg["output"] = compressText(out, stats, "openai-responses-string")
			case []any:
				for _, p := range out {
					part, ok := p.(map[string]any)
					if ok && part["type"] == "input_text" {
						if txt, ok := part["text"].(string); ok {
							part["text"] = compressText(txt, stats, "openai-responses-array")
						}
					}
				}
			}
			continue
		}
		// OpenAI tool message — string content
		if role, _ := msg["role"].(string); role == "tool" {
			switch c := msg["content"].(type) {
			case string:
				msg["content"] = compressText(c, stats, "openai-tool")
				continue
			case []any:
				for _, p := range c {
					part, ok := p.(map[string]any)
					if ok && part["type"] == "text" {
						if txt, ok := part["text"].(string); ok {
							part["text"] = compressText(txt, stats, "openai-tool-array")
						}
					}
				}
				continue
			}
		}
		// Claude blocks array
		blocks, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, b := range blocks {
			block, ok := b.(map[string]any)
			if !ok || block["type"] != "tool_result" {
				continue
			}
			if isErr, _ := block["is_error"].(bool); isErr {
				continue
			}
			switch c := block["content"].(type) {
			case string:
				block["content"] = compressText(c, stats, "claude-string")
			case []any:
				for _, p := range c {
					part, ok := p.(map[string]any)
					if ok && part["type"] == "text" {
						if txt, ok := part["text"].(string); ok {
							part["text"] = compressText(txt, stats, "claude-array")
						}
					}
				}
			}
		}
	}
	if len(stats.Hits) == 0 {
		return nil
	}
	return stats
}

func compressKiro(body map[string]any) *Stats {
	stats := &Stats{}
	defer func() { _ = recover() }()
	state, _ := body["conversationState"].(map[string]any)
	if state == nil {
		return nil
	}
	var all []any
	if h, ok := state["history"].([]any); ok {
		all = append(all, h...)
	}
	if cm, ok := state["currentMessage"].(map[string]any); ok {
		all = append(all, cm)
	}
	for _, m := range all {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		uim, _ := msg["userInputMessage"].(map[string]any)
		if uim == nil {
			continue
		}
		ctx, _ := uim["userInputMessageContext"].(map[string]any)
		if ctx == nil {
			continue
		}
		trs, _ := ctx["toolResults"].([]any)
		for _, tr := range trs {
			t, ok := tr.(map[string]any)
			if !ok {
				continue
			}
			if status, _ := t["status"].(string); status == "error" {
				continue
			}
			content, ok := t["content"].([]any)
			if !ok {
				continue
			}
			for _, p := range content {
				part, ok := p.(map[string]any)
				if !ok {
					continue
				}
				if txt, ok := part["text"].(string); ok {
					part["text"] = compressText(txt, stats, "kiro-tool-result")
				}
			}
		}
	}
	if len(stats.Hits) == 0 {
		return nil
	}
	return stats
}

// compressText runs auto-detect + filter. Fail-open: never shrinks to empty,
// never grows.
func compressText(text string, stats *Stats, shape string) string {
	in := len(text)
	stats.BytesBefore += in
	if in < minCompressSize || in > rawCap {
		stats.BytesAfter += in
		return text
	}
	fn := autoDetect(text)
	if fn == nil {
		stats.BytesAfter += in
		return text
	}
	out := safeApply(*fn, text)
	if out == "" || len(out) >= in {
		stats.BytesAfter += in
		return text
	}
	stats.BytesAfter += len(out)
	stats.Hits = append(stats.Hits, Hit{Shape: shape, Filter: fn.name, Saved: in - len(out)})
	return out
}

func safeApply(fn filter, text string) string {
	defer func() { _ = recover() }()
	out := fn.apply(text)
	if out == "" {
		return text
	}
	return out
}

// FormatLog renders a one-line summary for request logging.
func FormatLog(stats *Stats) string {
	if stats == nil || len(stats.Hits) == 0 {
		return ""
	}
	saved := stats.BytesBefore - stats.BytesAfter
	if stats.BytesBefore == 0 {
		return ""
	}
	pct := float64(saved) * 100.0 / float64(stats.BytesBefore)
	seen := map[string]bool{}
	filters := ""
	for _, h := range stats.Hits {
		if !seen[h.Filter] {
			seen[h.Filter] = true
			if filters != "" {
				filters += ","
			}
			filters += h.Filter
		}
	}
	return "[RTK] saved " + itoa(saved) + "B / " + itoa(stats.BytesBefore) +
		"B (" + ftoa(pct) + "%) via [" + filters + "] hits=" + itoa(len(stats.Hits))
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func ftoa(f float64) string {
	s := ""
	if f < 10 {
		s = strconv.FormatFloat(f, 'f', 1, 64)
	} else {
		s = strconv.FormatFloat(f, 'f', 0, 64)
	}
	return s
}
