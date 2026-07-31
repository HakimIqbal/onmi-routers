package compression

import (
	"strings"

	"github.com/dlclark/regexp2"
)

// ── Standard mode = Caveman rule engine (ported from OmniRoute caveman*.ts) ──
// Regex-based prose rewriting: strip filler/pleasantries/hedging, condense
// verbose phrasing, drop articles at higher intensity, and (ultra) abbreviate
// technical terms. Rules are gated by intensity tier (lite<full<ultra) and by
// message role context. Code blocks are protected from rewriting.

const (
	intLite  = 0
	intFull  = 1
	intUltra = 2
)

// cavemanRule is one rewrite rule.
type cavemanRule struct {
	name         string
	re           *regexp2.Regexp
	repl         string                 // static replacement ("" when replFn set)
	replFn       func(match string) string
	context      string // "all" | "user" | "system" | "assistant"
	minIntensity int
}

// mapRepl builds a replacement func that lowercases the match and looks it up
// in m, falling back to fallback (usually "" or the original match).
func mapRepl(m map[string]string, fallbackKeep bool) func(string) string {
	return func(match string) string {
		key := strings.ToLower(strings.TrimSpace(match))
		if v, ok := m[key]; ok {
			return v
		}
		if fallbackKeep {
			return match
		}
		return ""
	}
}

func mustRe(pattern string) *regexp2.Regexp {
	// JS rules use /gi — global + case-insensitive. regexp2: IgnoreCase flag;
	// global is handled by our replaceAll loop.
	re, err := regexp2.Compile(pattern, regexp2.IgnoreCase|regexp2.RE2)
	if err != nil {
		// fall back to non-RE2 (supports lookbehind/lookahead)
		re2, err2 := regexp2.Compile(pattern, regexp2.IgnoreCase)
		if err2 != nil {
			return nil
		}
		return re2
	}
	return re
}

// buildCavemanRules constructs the rule set (ported from cavemanRules.ts).
func buildCavemanRules() []cavemanRule {
	type spec struct {
		name, pattern, repl, ctx string
		fn                       func(string) string
		min                      int
	}
	specs := []spec{
		// Category 1: Filler Removal
		{"redundant_phrasing", `\b(?:make sure to|be sure to|due to the fact that|the reason is because|it is important to|you should|remember to)\b\s*`, "",
			"all", mapRepl(map[string]string{"make sure to": "ensure ", "be sure to": "ensure ", "due to the fact that": "because ", "the reason is because": "because ", "it is important to": "", "you should": "", "remember to": ""}, false), intFull},
		{"pleasantries", `\b(?:i'?d be happy to|i would be happy to|i'?d be glad to|i would be glad to|glad to help|happy to|thank you|thanks|no problem|you'?re welcome|absolutely|certainly|of course|sure)\b[,.!?\s]*`, "", "all", nil, intLite},
		{"polite_framing", `\b(?:please|kindly|could you please|would you please|can you please|I would like you to|I want you to|I need you to)\b\s*`, "", "all", nil, intLite},
		{"hedging", `\b(?:it seems like|it appears that|I think that|I believe that|probably|possibly|maybe it)\b\s*`, "", "all", nil, intLite},
		{"verbose_instructions", `\b(?:provide a detailed explanation of|give me a comprehensive explanation of|write an in-depth explanation of|create a thorough explanation of|provide a detailed|give me a comprehensive|write an in-depth|create a thorough|explain in detail)\b`, "",
			"all", mapRepl(map[string]string{"provide a detailed explanation of": "explain ", "give me a comprehensive explanation of": "explain ", "write an in-depth explanation of": "explain ", "create a thorough explanation of": "explain ", "provide a detailed": "provide ", "give me a comprehensive": "give ", "write an in-depth": "write ", "create a thorough": "create ", "explain in detail": "explain "}, true), intLite},
		{"filler_adverbs", `\b(?:basically|essentially|actually|literally|simply|currently)\b\s*`, "", "all", nil, intLite},
		{"articles", `\b(?:[Aa]n|[Aa]|[Tt]he)\s+(?=[a-z])`, "", "all", nil, intFull},
		{"filler_phrases", `^(?:I want to|I need to|I'd like to|I'm looking for)\b\s*`, "", "user", nil, intLite},
		{"redundant_openers", `^(?:Hi there|Hello|Good morning|Hey)\s*[,.!?\s]?\s*`, "", "user", nil, intLite},
		{"verbose_requests", `\b(?:I was wondering if you could|Would it be possible to)\b\s*`, "", "user", nil, intLite},
		{"leader_phrases", `^(?:i'?ll|i will|i can|i'?d|let me|you can|we will|we can|let'?s)\s+(?=[a-z])`, "", "all", nil, intFull},
		{"self_reference", `^(?:I am trying to|I am working on|I have been)\b\s*`, "", "user", nil, intLite},
		{"excessive_gratitude", `\b(?:Thank you so much|Thanks in advance|I really appreciate)\b[,.!?\s]*`, "", "all", nil, intLite},
		{"qualifier_removal", `\b(?:a bit|a little|somewhat|kind of|sort of)\b\s*`, "", "all", nil, intLite},

		// Category 2: Context Condensation
		{"compound_collapse", `\band any potential\b`, "", "all", nil, intFull},
		{"explanatory_prefix", `\b(?:The function appears to be handling|The code seems to|The class is|This module is)\b`, "",
			"all", mapRepl(map[string]string{"the function appears to be handling": "Function:", "the code seems to": "Code:", "the class is": "Class:", "this module is": "Module:"}, true), intLite},
		{"question_to_directive", `\b(?:Can you explain why|Could you show me how|Would you tell me|Can you tell me)\b\s*`, "",
			"user", mapRepl(map[string]string{"can you explain why": "Explain why ", "could you show me how": "Show how ", "would you tell me": "Tell me ", "can you tell me": "Tell me "}, true), intLite},
		{"context_setup", `\b(?:I have the following code|Here is my code|Below is the code)\b\s*[:.]?\s*`, "Code:", "user", nil, intLite},
		{"intent_clarification", `\b(?:What I'm trying to do is|My objective is to|What I need is|I'm aiming to)\b\s*`, "Goal:", "user", nil, intLite},
		{"background_removal", `\b(?:As you may know,?\s*|As we discussed earlier,?\s*)`, "", "all", nil, intLite},
		{"meta_commentary", `^(?:Note that|Keep in mind that|Remember that)\b\s*`, "", "all", nil, intLite},
		{"purpose_statement", `\b(?:for the purpose of|with the goal of|in an effort to|for every)\b`, "",
			"all", mapRepl(map[string]string{"for the purpose of": "for", "with the goal of": "to", "in an effort to": "to", "for every": "per"}, true), intLite},

		// Category 3: Structural Compression
		{"list_conjunction", `,\s*and also\s+|,\s*as well as\s+`, ", ", "all", nil, intFull},
		{"purpose_phrases", `\b(?:in order to|so as to)\b\s*`, "to ", "all", nil, intLite},
		{"redundant_quantifiers", `\b(?:each and every single|each and every|any and all)\b`, "",
			"all", mapRepl(map[string]string{"each and every single": "each", "each and every": "each", "any and all": "all"}, true), intFull},
		{"verbose_connectors", `\b(?:furthermore|additionally|moreover|in addition)\b\s*`, "also ", "all", nil, intLite},
		{"transition_removal", `^(?:On the other hand,?\s*|In contrast,?\s*|However,?\s*)`, "", "all", nil, intLite},
		{"emphasis_removal", `\b(?:very|really|extremely|highly|quite)\s+(?=[a-z])`, "", "all", nil, intLite},
		{"passive_voice", `\b(?:is being used|is being called|is being generated|was created|was generated|was implemented)\b`, "",
			"all", mapRepl(map[string]string{"is being used": "uses", "is being called": "calls", "is being generated": "generated", "was created": "created", "was generated": "generated", "was implemented": "implemented"}, true), intFull},

		// Category 4: Multi-Turn Dedup
		{"repeated_context", `\b(?:As we discussed earlier|As mentioned before|As previously stated|As I said before)\b[,.]?\s*`, "See above. ", "all", nil, intLite},
		{"repeated_question", `\b(?:Same question as before|I asked this earlier|This is the same question)\b[,.]?\s*`, "[same question] ", "user", nil, intLite},
		{"reestablished_context", `\b(?:Going back to the code above|Referring back to|Returning to)\b\s*`, "Re: ", "all", nil, intLite},
		{"summary_replacement", `\b(?:To summarize what we've discussed|In summary of our conversation|To recap)\b[,.]?\s*`, "Summary: ", "assistant", nil, intLite},

		// Category 5: Ultra Abbreviations
		{"ultra_abbreviations", `\b(?:database|configuration|function|request|response|implementation|authentication|authorization|application|dependency|dependencies)\b`, "",
			"all", mapRepl(map[string]string{"database": "DB", "configuration": "config", "function": "fn", "request": "req", "response": "res", "implementation": "impl", "authentication": "auth", "authorization": "authz", "application": "app", "dependency": "dep", "dependencies": "deps"}, true), intUltra},
	}

	rules := make([]cavemanRule, 0, len(specs))
	for _, s := range specs {
		re := mustRe(s.pattern)
		if re == nil {
			continue
		}
		rules = append(rules, cavemanRule{
			name: s.name, re: re, repl: s.repl, replFn: s.fn,
			context: s.ctx, minIntensity: s.min,
		})
	}
	return rules
}

var cavemanRules = buildCavemanRules()

// applyCaveman runs the caveman rule engine over eligible message content.
// Standard mode uses "full" intensity. System prompts are preserved by default.
func applyCaveman(body map[string]any, opts Options) Result {
	msgs := messagesOf(body)
	if len(msgs) == 0 {
		return Result{Body: body, Compressed: false}
	}
	out := cloneBody(body)
	omsgs := messagesOf(out)
	applied := false
	rulesUsed := map[string]bool{}

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
		if !ok || len(s) < 20 {
			continue // skip very short content (min message length)
		}
		newText, used := cavemanRewrite(s, role, intFull)
		if newText != s {
			applied = true
			nm := copyMsg(mm)
			nm["content"] = newText
			omsgs[i] = nm
			for u := range used {
				rulesUsed[u] = true
			}
		}
	}

	if !applied {
		return Result{Body: body, Compressed: false}
	}
	techniques := make([]string, 0, len(rulesUsed))
	for u := range rulesUsed {
		techniques = append(techniques, u)
	}
	return Result{Body: out, Compressed: true, Stats: &Stats{TechniquesUsed: append([]string{"caveman"}, techniques...)}}
}

// cavemanRewrite applies all rules eligible for the role+intensity to text,
// protecting fenced code blocks from rewriting.
func cavemanRewrite(text, role string, intensity int) (string, map[string]bool) {
	used := map[string]bool{}
	// Split into code vs prose segments; only rewrite prose.
	segments := splitCodeBlocks(text)
	for i := range segments {
		if segments[i].isCode {
			continue
		}
		s := segments[i].text
		for _, rule := range cavemanRules {
			if rule.minIntensity > intensity {
				continue
			}
			if rule.context != "all" && rule.context != role {
				continue
			}
			newS, n := replaceAllRule(rule, s)
			if n > 0 {
				s = newS
				used[rule.name] = true
			}
		}
		segments[i].text = s
	}
	return joinSegments(segments), used
}

// replaceAllRule applies a rule globally, returning the new string and the
// number of replacements made.
func replaceAllRule(rule cavemanRule, s string) (string, int) {
	count := 0
	result, err := rule.re.ReplaceFunc(s, func(m regexp2.Match) string {
		count++
		if rule.replFn != nil {
			return rule.replFn(m.String())
		}
		return rule.repl
	}, 0, -1)
	if err != nil {
		return s, 0
	}
	return result, count
}
