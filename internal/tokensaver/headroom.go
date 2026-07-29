package tokensaver

import (
	"fmt"
	"strings"
)

// CompressHeadroom trims older chat turns when message count is large.
// Keeps system + last keepLast user/assistant turns. Preserves tool_call
// + tool pairs together. Fail-open.
func CompressHeadroom(body map[string]any, keepLast int) int {
	if body == nil {
		return 0
	}
	if keepLast <= 0 {
		keepLast = 10
	}
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) <= keepLast+2 {
		return 0
	}
	var system []any
	var rest []any
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			rest = append(rest, m)
			continue
		}
		if role, _ := mm["role"].(string); role == "system" {
			system = append(system, m)
			continue
		}
		rest = append(rest, m)
	}
	if len(rest) <= keepLast {
		return 0
	}

	// Smart trimming: preserve tool_call + tool pairs.
	// Walk backwards from the end, collecting messages until we have keepLast
	// "logical units" (where a tool_call + its tool result = 1 unit).
	dropped := len(rest) - keepLast
	trimmed := smartTrim(rest, keepLast)

	// Build a summary of what was dropped for the marker.
	summary := buildHeadroomSummary(dropped, trimmed.totalTokens)

	marker := map[string]any{
		"role":    "system",
		"content": summary,
	}
	out := make([]any, 0, len(system)+1+len(trimmed.kept))
	out = append(out, system...)
	out = append(out, marker)
	out = append(out, trimmed.kept...)
	body["messages"] = out
	return dropped
}

// trimResult holds the result of smart trimming.
type trimResult struct {
	kept       []any
	totalTokens int
}

// smartTrim keeps the last keepLast logical units, preserving tool_call+tool pairs.
func smartTrim(rest []any, keepLast int) trimResult {
	var kept []any
	count := 0
	totalTokens := 0

	// Walk backwards, grouping tool_call + tool_result pairs
	for i := len(rest) - 1; i >= 0; i-- {
		if count >= keepLast {
			break
		}
		mm, ok := rest[i].(map[string]any)
		if !ok {
			kept = append([]any{rest[i]}, kept...)
			count++
			continue
		}
		role, _ := mm["role"].(string)
		totalTokens += estimateTokens(mm)

		if role == "tool" {
			// Include the preceding assistant message with tool_calls
			kept = append([]any{rest[i]}, kept...)
			count++
			// Look back for the assistant tool_call
			if i > 0 {
				if prevMm, ok := rest[i-1].(map[string]any); ok {
					if prevRole, _ := prevMm["role"].(string); prevRole == "assistant" {
						kept = append([]any{rest[i-1]}, kept...)
						count++
						totalTokens += estimateTokens(prevMm)
					}
				}
			}
		} else {
			kept = append([]any{rest[i]}, kept...)
			count++
		}
	}

	return trimResult{kept: kept, totalTokens: totalTokens}
}

// estimateTokens gives a rough token count for a message (chars / 4).
func estimateTokens(mm map[string]any) int {
	content, _ := mm["content"].(string)
	if content == "" {
		// Try array content
		if arr, ok := mm["content"].([]any); ok {
			for _, p := range arr {
				if part, ok := p.(map[string]any); ok {
					if txt, ok := part["text"].(string); ok {
						content += txt
					}
				}
			}
		}
	}
	return len(content) / 4
}

// buildHeadroomSummary creates a concise marker explaining what was compressed.
func buildHeadroomSummary(dropped int, remainingTokens int) string {
	var b strings.Builder
	b.WriteString("[HEADROOM] Compressed ")
	b.WriteString(fmt.Sprintf("%d older turn(s)", dropped))
	b.WriteString(" to fit context budget. ")
	b.WriteString("Continuing from recent messages. ")
	b.WriteString(fmt.Sprintf("~%d tokens in retained history.", remainingTokens))
	return b.String()
}
