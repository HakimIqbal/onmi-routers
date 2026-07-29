package tokensaver

// CompressHeadroom trims older chat turns when message count is large.
// Keeps system + last keepLast user/assistant turns. Fail-open.
func CompressHeadroom(body map[string]any, keepLast int) int {
	if body == nil {
		return 0
	}
	if keepLast <= 0 {
		keepLast = 8
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
	dropped := len(rest) - keepLast
	rest = rest[len(rest)-keepLast:]
	// Insert a short marker so the model knows history was compressed.
	marker := map[string]any{
		"role":    "system",
		"content": "[HEADROOM] Earlier turns compressed for context budget; continue from remaining recent messages.",
	}
	out := make([]any, 0, len(system)+1+len(rest))
	out = append(out, system...)
	out = append(out, marker)
	out = append(out, rest...)
	body["messages"] = out
	return dropped
}
