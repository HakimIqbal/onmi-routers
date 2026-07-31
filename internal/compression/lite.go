package compression

import "strings"

// ── Lite mode (ported from OmniRoute lite.ts) ──────────────────────────────
// Five safe, near-lossless techniques:
//   1. whitespace      — collapse newline runs (>2→2) + trim trailing WS
//   2. system-dedup    — drop duplicate system messages (by first 200 chars)
//   3. tool-compress   — truncate tool results > 2000 chars at a word boundary
//   4. redundant-remove— drop a message identical to the one right before it
//   5. image-placeholder— replace base64 data:image URLs with [image: fmt]
//                         (only when the model is known to lack vision)

const maxToolLength = 2000
const toolTruncationLookback = 80

func applyLite(body map[string]any, opts Options) Result {
	if messagesOf(body) == nil {
		return Result{Body: body, Compressed: false}
	}
	out := cloneBody(body)
	msgs := messagesOf(out)
	var techniques []string

	if msgs, ok := collapseWhitespace(msgs, opts); ok {
		out["messages"] = msgs
		techniques = append(techniques, "whitespace")
	}
	if msgs, ok := dedupSystemPrompt(messagesOf(out), opts); ok {
		out["messages"] = msgs
		techniques = append(techniques, "system-dedup")
	}
	if msgs, ok := compressToolResults(messagesOf(out)); ok {
		out["messages"] = msgs
		techniques = append(techniques, "tool-compress")
	}
	if msgs, ok := removeRedundantContent(messagesOf(out), opts); ok {
		out["messages"] = msgs
		techniques = append(techniques, "redundant-remove")
	}
	if msgs, ok := replaceImageURLs(messagesOf(out), opts); ok {
		out["messages"] = msgs
		techniques = append(techniques, "image-placeholder")
	}

	if len(techniques) == 0 {
		return Result{Body: body, Compressed: false}
	}
	return Result{
		Body:       out,
		Compressed: true,
		Stats:      &Stats{TechniquesUsed: techniques},
	}
}

// collapseWhitespace normalizes each string-content message: collapse runs of
// >2 newlines down to 2 and trim trailing horizontal whitespace per line.
func collapseWhitespace(msgs []any, opts Options) ([]any, bool) {
	applied := false
	out := make([]any, len(msgs))
	for i, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			out[i] = m
			continue
		}
		if opts.PreserveSystemPrompt && roleOf(mm) == "system" {
			out[i] = m
			continue
		}
		s, ok := mm["content"].(string)
		if !ok {
			out[i] = m
			continue
		}
		norm := normalizeWhitespace(s)
		if norm != s {
			applied = true
			nm := copyMsg(mm)
			nm["content"] = norm
			out[i] = nm
			continue
		}
		out[i] = m
	}
	return out, applied
}

func normalizeWhitespace(s string) string {
	s = collapseNewlineRuns(s)
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t")
	}
	return strings.Join(lines, "\n")
}

func collapseNewlineRuns(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	run := 0
	for _, r := range s {
		if r == '\n' {
			run++
			if run <= 2 {
				b.WriteRune(r)
			}
			continue
		}
		run = 0
		b.WriteRune(r)
	}
	return b.String()
}

// dedupSystemPrompt drops system messages whose first 200 trimmed chars match
// an earlier system message.
func dedupSystemPrompt(msgs []any, opts Options) ([]any, bool) {
	if opts.PreserveSystemPrompt {
		return msgs, false
	}
	seen := map[string]bool{}
	applied := false
	out := make([]any, 0, len(msgs))
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok || roleOf(mm) != "system" {
			out = append(out, m)
			continue
		}
		s, ok := mm["content"].(string)
		if !ok {
			out = append(out, m)
			continue
		}
		key := truncKey(strings.TrimSpace(s), 200)
		if seen[key] {
			applied = true
			continue
		}
		seen[key] = true
		out = append(out, m)
	}
	return out, applied
}

// compressToolResults truncates tool-role string content longer than
// maxToolLength at a word boundary, appending a truncation marker.
func compressToolResults(msgs []any) ([]any, bool) {
	applied := false
	out := make([]any, len(msgs))
	for i, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok || roleOf(mm) != "tool" {
			out[i] = m
			continue
		}
		s, ok := mm["content"].(string)
		if !ok || len(s) <= maxToolLength {
			out[i] = m
			continue
		}
		applied = true
		cut := backOffToWordBoundary(s, maxToolLength)
		nm := copyMsg(mm)
		nm["content"] = s[:cut] + "\n...[truncated]"
		out[i] = nm
	}
	return out, applied
}

// removeRedundantContent drops a message whose content is byte-identical to the
// immediately preceding message of the same role.
func removeRedundantContent(msgs []any, opts Options) ([]any, bool) {
	applied := false
	out := make([]any, 0, len(msgs))
	for i, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			out = append(out, m)
			continue
		}
		if opts.PreserveSystemPrompt && roleOf(mm) == "system" {
			out = append(out, m)
			continue
		}
		if i > 0 {
			if prev, ok := msgs[i-1].(map[string]any); ok && roleOf(prev) == roleOf(mm) {
				if extractText(prev["content"]) == extractText(mm["content"]) && extractText(mm["content"]) != "" {
					applied = true
					continue
				}
			}
		}
		out = append(out, m)
	}
	return out, applied
}

// replaceImageURLs swaps base64 data:image URLs for [image: fmt] placeholders
// when the model is known not to support vision.
func replaceImageURLs(msgs []any, opts Options) ([]any, bool) {
	if modelSupportsVision(opts.Model) {
		return msgs, false
	}
	applied := false
	out := make([]any, len(msgs))
	for i, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			out[i] = m
			continue
		}
		arr, ok := mm["content"].([]any)
		if !ok {
			out[i] = m
			continue
		}
		newArr := make([]any, len(arr))
		changed := false
		for j, p := range arr {
			part, ok := p.(map[string]any)
			if !ok || part["type"] != "image_url" {
				newArr[j] = p
				continue
			}
			iu, _ := part["image_url"].(map[string]any)
			url, _ := iu["url"].(string)
			if strings.HasPrefix(url, "data:image/") {
				applied = true
				changed = true
				fmtName := "unknown"
				if s := strings.Index(url, "/"); s >= 0 {
					rest := url[s+1:]
					if e := strings.Index(rest, ";"); e >= 0 {
						fmtName = rest[:e]
					}
				}
				newArr[j] = map[string]any{"type": "text", "text": "[image: " + fmtName + "]"}
				continue
			}
			newArr[j] = p
		}
		if changed {
			nm := copyMsg(mm)
			nm["content"] = newArr
			out[i] = nm
		} else {
			out[i] = m
		}
	}
	return out, applied
}

// ── shared helpers ──

func roleOf(mm map[string]any) string {
	r, _ := mm["role"].(string)
	return r
}

func copyMsg(mm map[string]any) map[string]any {
	nm := make(map[string]any, len(mm))
	for k, v := range mm {
		nm[k] = v
	}
	return nm
}

func truncKey(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func isWordChar(b byte) bool {
	return b != ' ' && b != '\t' && b != '\n' && b != '\r'
}

// backOffToWordBoundary adjusts a hard cut index to the nearest whitespace so a
// truncation never garbles a word. Prefers backing off; looks forward as a
// fallback; returns the original index if neither finds a boundary.
func backOffToWordBoundary(s string, cut int) int {
	if cut <= 0 || cut >= len(s) {
		return cut
	}
	onBoundary := !isWordChar(s[cut-1]) || !isWordChar(s[cut])
	if onBoundary {
		return cut
	}
	// backward
	lo := cut - toolTruncationLookback
	if lo < 0 {
		lo = 0
	}
	for i := cut; i > lo; i-- {
		if !isWordChar(s[i-1]) {
			return i - 1
		}
	}
	// forward
	hi := cut + toolTruncationLookback
	if hi > len(s) {
		hi = len(s)
	}
	for i := cut; i < hi; i++ {
		if !isWordChar(s[i]) {
			return i
		}
	}
	return cut
}
