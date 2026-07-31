package compression

import (
	"sort"
	"strings"
)

// ── Ultra mode = heuristic token pruner (ported from OmniRoute ultraHeuristic.ts) ──
// Scores each word token by information density and prunes low-value tokens
// (stopwords, short words) to hit a target keep rate, while force-preserving
// numbers, URLs, paths, error markers, and code.

var ultraStopwords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "are": true, "was": true,
	"were": true, "be": true, "been": true, "being": true, "have": true, "has": true,
	"had": true, "do": true, "does": true, "did": true, "will": true, "would": true,
	"could": true, "should": true, "may": true, "might": true, "shall": true, "can": true,
	"need": true, "dare": true, "ought": true, "used": true, "i": true, "we": true,
	"you": true, "he": true, "she": true, "it": true, "they": true, "me": true,
	"us": true, "him": true, "her": true, "them": true, "my": true, "our": true,
	"your": true, "his": true, "its": true, "their": true, "this": true, "that": true,
	"these": true, "those": true, "and": true, "but": true, "or": true, "nor": true,
	"for": true, "yet": true, "so": true, "as": true, "at": true, "by": true,
	"in": true, "of": true, "on": true, "to": true, "up": true, "via": true,
	"with": true, "from": true, "into": true, "onto": true, "upon": true, "about": true,
	"just": true, "very": true, "really": true, "quite": true, "rather": true, "also": true,
	"too": true, "even": true, "still": true, "already": true, "always": true, "never": true,
	"often": true, "usually": true, "sometimes": true, "here": true, "there": true,
}

// forcePreserve reports whether a token must never be pruned.
func forcePreserve(token string) bool {
	if strings.ContainsAny(token, "0123456789") {
		return true
	}
	if strings.Contains(token, "http://") || strings.Contains(token, "https://") {
		return true
	}
	if strings.ContainsAny(token, "._/\\") {
		return true
	}
	if strings.Contains(token, "Error:") || strings.Contains(token, "Exception:") {
		return true
	}
	if strings.Contains(token, "```") {
		return true
	}
	return false
}

// scoreToken scores a token's information value from 0.0 (prune) to 1.0 (keep).
func scoreToken(token string) float64 {
	if forcePreserve(token) {
		return 1.0
	}
	lower := strings.ToLower(token)
	if ultraStopwords[lower] {
		return 0.1
	}
	if len(token) <= 2 {
		return 0.2
	}
	if token[0] >= 'A' && token[0] <= 'Z' {
		return 0.8 // proper nouns / identifiers
	}
	if len(token) >= 6 {
		return 0.7
	}
	return 0.5
}

// pruneByScore prunes low-value tokens to keep roughly keepRate of words.
func pruneByScore(text string, keepRate, minScore float64) string {
	if text == "" || keepRate >= 1 {
		return text
	}
	// Tokenize preserving whitespace runs.
	fields := strings.FieldsFunc(text, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' })
	if len(fields) == 0 {
		return text
	}
	targetKeep := int(float64(len(fields))*keepRate + 0.999) // ceil
	if targetKeep >= len(fields) {
		return text
	}

	type scored struct {
		idx   int
		score float64
	}
	scoredTokens := make([]scored, len(fields))
	for i, f := range fields {
		scoredTokens[i] = scored{idx: i, score: scoreToken(f)}
	}
	// Sort ascending by score — lowest pruned first.
	sorted := make([]scored, len(scoredTokens))
	copy(sorted, scoredTokens)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].score < sorted[b].score })

	prune := map[int]bool{}
	pruned := 0
	maxPrune := len(fields) - targetKeep
	for _, s := range sorted {
		if pruned >= maxPrune {
			break
		}
		if s.score < minScore {
			prune[s.idx] = true
			pruned++
		}
	}

	kept := make([]string, 0, targetKeep)
	for i, f := range fields {
		if !prune[i] {
			kept = append(kept, f)
		}
	}
	return strings.Join(kept, " ")
}

// applyUltra runs the heuristic pruner over eligible message content.
// Uses keepRate 0.6 / minScore 0.3 (conservative ultra tier).
func applyUltra(body map[string]any, opts Options) Result {
	msgs := messagesOf(body)
	if len(msgs) == 0 {
		return Result{Body: body, Compressed: false}
	}
	out := cloneBody(body)
	omsgs := messagesOf(out)
	applied := false

	for i, m := range omsgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role := roleOf(mm)
		if opts.PreserveSystemPrompt && role == "system" {
			continue
		}
		s, ok := mm["content"].(string)
		if !ok || len(s) < 40 {
			continue
		}
		// Protect code blocks; only prune prose segments.
		segs := splitCodeBlocks(s)
		changed := false
		for j := range segs {
			if segs[j].isCode {
				continue
			}
			pruned := pruneByScore(segs[j].text, 0.6, 0.3)
			if pruned != segs[j].text {
				segs[j].text = pruned
				changed = true
			}
		}
		if changed {
			newText := joinSegments(segs)
			if len(newText) < len(s) {
				applied = true
				nm := copyMsg(mm)
				nm["content"] = newText
				omsgs[i] = nm
			}
		}
	}

	if !applied {
		return Result{Body: body, Compressed: false}
	}
	return Result{Body: out, Compressed: true, Stats: &Stats{TechniquesUsed: []string{"ultra-prune"}}}
}
