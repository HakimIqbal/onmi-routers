package compression

import "strings"

// segment is a run of text that is either prose (rewritable) or code (frozen).
type segment struct {
	text   string
	isCode bool
}

// Think-tag tokens, built from hex escapes so the literal tag strings never
// appear in this source file (they would otherwise be stripped by tooling that
// treats them as markup). "\x3c" == '<', "\x3e" == '>'.
const (
	thinkOpenTag  = "\x3cthink\x3e"  // opening reasoning-trace tag
	thinkCloseTag = "\x3c/think\x3e" // closing reasoning-trace tag
)

// splitCodeBlocks segments text into prose (rewritable) and frozen code runs.
// Three kinds of content are frozen so the compression engines never rewrite
// code, paths, commands, or reasoning traces:
//  1.  reasoning blocks (reasoning-model traces)
//  2. ``` fenced code blocks
//  3. inline `code` spans
func splitCodeBlocks(text string) []segment {
	// Pass 1: carve out  regions (frozen whole, tags included).
	thinkSegs := splitThinkBlocks(text)
	// Pass 2: within prose regions, carve fenced + inline code.
	var out []segment
	for _, s := range thinkSegs {
		if s.isCode {
			out = append(out, s)
			continue
		}
		out = append(out, splitFencedAndInline(s.text)...)
	}
	return out
}

// splitThinkBlocks splits text into prose and -frozen segments.
// An unterminated open tag freezes the remainder (safe default).
func splitThinkBlocks(text string) []segment {
	var segs []segment
	rest := text
	for {
		openIdx := strings.Index(rest, thinkOpenTag)
		if openIdx == -1 {
			if rest != "" {
				segs = append(segs, segment{text: rest, isCode: false})
			}
			return segs
		}
		if openIdx > 0 {
			segs = append(segs, segment{text: rest[:openIdx], isCode: false})
		}
		closeRel := strings.Index(rest[openIdx:], thinkCloseTag)
		if closeRel == -1 {
			segs = append(segs, segment{text: rest[openIdx:], isCode: true})
			return segs
		}
		end := openIdx + closeRel + len(thinkCloseTag)
		segs = append(segs, segment{text: rest[openIdx:end], isCode: true})
		rest = rest[end:]
	}
}

// splitFencedAndInline splits prose on ``` fenced blocks and inline `code`.
func splitFencedAndInline(text string) []segment {
	var segs []segment
	var cur strings.Builder
	inFence := false

	flush := func(isCode bool) {
		if cur.Len() == 0 {
			return
		}
		segs = append(segs, segment{text: cur.String(), isCode: isCode})
		cur.Reset()
	}

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			flush(inFence)
			cur.WriteString(line)
			if i < len(lines)-1 {
				cur.WriteString("\n")
			}
			flush(true)
			inFence = !inFence
			continue
		}
		cur.WriteString(line)
		if i < len(lines)-1 {
			cur.WriteString("\n")
		}
	}
	flush(inFence)

	// Protect inline `code` spans within prose segments.
	var out []segment
	for _, s := range segs {
		if s.isCode {
			out = append(out, s)
			continue
		}
		out = append(out, splitInlineCode(s.text)...)
	}
	return out
}

// splitInlineCode splits a prose string on inline `code` spans.
func splitInlineCode(text string) []segment {
	var segs []segment
	var cur strings.Builder
	inCode := false
	for _, r := range text {
		if r == '`' {
			if cur.Len() > 0 {
				segs = append(segs, segment{text: cur.String(), isCode: inCode})
				cur.Reset()
			}
			cur.WriteRune(r)
			inCode = !inCode
			continue
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		segs = append(segs, segment{text: cur.String(), isCode: inCode})
	}
	return segs
}

func joinSegments(segs []segment) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.text)
	}
	return b.String()
}
