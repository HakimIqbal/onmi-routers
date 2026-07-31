package compression

import (
	"strings"
)

// ── Rule-based summarizer (ported from OmniRoute summarizer.ts) ────────────
// Extracts the high-signal skeleton of a message: user intents, file paths
// touched, error snippets, and the last assistant decision (code-trimmed).
// Deterministic, no LLM call.

var summarizerFileExts = map[string]bool{
	"ts": true, "tsx": true, "js": true, "jsx": true, "py": true, "md": true,
	"json": true, "sql": true, "css": true, "html": true, "yaml": true, "yml": true,
	"sh": true, "rb": true, "go": true, "rs": true, "java": true, "c": true,
	"cpp": true, "h": true, "hpp": true,
}

var summarizerIntentTriggers = []string{
	"request:", "fix:", "implement:", "add:", "remove:", "update:", "refactor:",
	"create:", "delete:", "change:", "build:",
}

// summarizeMessage builds a compressed skeleton summary of one message's text.
func summarizeMessage(text string, maxLen int, preserveCode bool) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	var parts []string
	parts = append(parts, "[COMPRESSED:summary]")

	if intent := firstIntentLine(text); intent != "" {
		parts = append(parts, "Intents: "+intent+".")
	}
	if files := extractFilePaths(text); len(files) > 0 {
		parts = append(parts, "Files touched: "+strings.Join(files, ", ")+".")
	}
	if errs := extractErrors(text); len(errs) > 0 {
		parts = append(parts, "Errors: "+strings.Join(errs, "; ")+".")
	}
	decision := text
	if preserveCode {
		decision = trimCodeFences(decision)
	}
	if decision != "" {
		if len(decision) > 200 {
			decision = decision[:200]
		}
		parts = append(parts, "Last decision: "+decision+".")
	}

	result := strings.Join(parts, " ")
	if maxLen > 0 && len(result) > maxLen {
		result = result[:maxLen-3] + "..."
	}
	return result
}

func firstIntentLine(text string) string {
	line := strings.TrimSpace(strings.Split(text, "\n")[0])
	if line == "" {
		return ""
	}
	lower := strings.ToLower(line)
	for _, trig := range summarizerIntentTriggers {
		if strings.HasPrefix(lower, trig) {
			return truncStr(line, 120)
		}
	}
	return truncStr(line, 120)
}

func extractFilePaths(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, tok := range strings.Fields(text) {
		if fp := knownFilePath(tok); fp != "" && !seen[fp] {
			seen[fp] = true
			out = append(out, fp)
			if len(out) >= 20 {
				break
			}
		}
	}
	return out
}

func knownFilePath(token string) string {
	clean := stripTokenPunct(token)
	dot := strings.LastIndex(clean, ".")
	if dot <= 0 || dot == len(clean)-1 {
		return ""
	}
	ext := strings.ToLower(clean[dot+1:])
	if !summarizerFileExts[ext] {
		return ""
	}
	if !strings.Contains(clean, "/") && !strings.Contains(clean, ".") {
		return ""
	}
	return clean
}

func stripTokenPunct(token string) string {
	leading := "'\"`([{"
	trailing := "'\"`)],;:"
	start, end := 0, len(token)
	for start < end && strings.ContainsRune(leading, rune(token[start])) {
		start++
	}
	for end > start && strings.ContainsRune(trailing, rune(token[end-1])) {
		end--
	}
	return token[start:end]
}

func extractErrors(text string) []string {
	var out []string
	for _, seg := range strings.FieldsFunc(text, func(r rune) bool { return r == '.' || r == '\n' }) {
		trimmed := strings.TrimSpace(seg)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.Contains(trimmed, "TypeError:") || strings.Contains(trimmed, "ReferenceError:") ||
			strings.Contains(trimmed, "SyntaxError:") || strings.Contains(trimmed, "RangeError:") ||
			strings.Contains(trimmed, "Error:") || strings.Contains(trimmed, "Exception:") ||
			strings.Contains(lower, "error ts") {
			out = append(out, truncStr(trimmed, 150))
			if len(out) >= 10 {
				break
			}
		}
	}
	return out
}

// trimCodeFences collapses long fenced code blocks to head(3)+tail(1).
func trimCodeFences(text string) string {
	var out strings.Builder
	cursor := 0
	for cursor < len(text) {
		start := strings.Index(text[cursor:], "```")
		if start == -1 {
			out.WriteString(text[cursor:])
			break
		}
		start += cursor
		openLineEnd := strings.Index(text[start+3:], "\n")
		if openLineEnd == -1 {
			out.WriteString(text[cursor:])
			break
		}
		openLineEnd += start + 3
		closeStart := strings.Index(text[openLineEnd+1:], "\n```")
		if closeStart == -1 {
			out.WriteString(text[cursor:])
			break
		}
		closeStart += openLineEnd + 1
		closeEnd := closeStart + 4
		opening := text[start:openLineEnd]
		code := text[openLineEnd+1 : closeStart]
		lines := strings.Split(code, "\n")

		out.WriteString(text[cursor:start])
		if len(lines) <= 4 {
			out.WriteString(text[start:closeEnd])
		} else {
			head := strings.Join(lines[:3], "\n")
			tail := lines[len(lines)-1]
			out.WriteString(opening + "\n" + head + "\n…\n" + tail + "\n```")
		}
		cursor = closeEnd
	}
	return out.String()
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
