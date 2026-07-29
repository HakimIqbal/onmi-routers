// Package tokensaver — Token Saver stacking layer for the gateway.
//
// Provides prompt-injection directives (Caveman, Ponytail) plus config for
// toggling RTK input-compression. Output compression (Caveman/Ponytail) is
// expressed as a system-message directive injected at request time; the
// upstream model returns terser output, saving output tokens.
//
// Mirrors 9Router's Token Saver: RTK (input, −20-40%) + Caveman (output,
// −65%) + Ponytail (output, YAGNI code style). Fail-open: disabled or empty
// config yields no injection.
package tokensaver

import (
	"encoding/json"
	"strings"
	"sync"
)

// Config is the runtime Token Saver configuration, hot-reloadable via Redis.
type Config struct {
	mu sync.RWMutex

	RTK       bool `json:"rtk"`        // input tool_result compression (see internal/rtk)
	Headroom  bool `json:"headroom"`   // context/history compression directive
	Caveman   bool `json:"caveman"`    // terse output directive
	CavemanLevel int `json:"caveman_level"` // 1=light, 2=standard, 3=aggressive (default 2)
	CodeStyle bool `json:"code_style"` // YAGNI-first code style directive (renamed from Ponytail)
}

// DefaultConfig returns RTK on, others off (RTK is the lowest-risk win).
func DefaultConfig() *Config {
	return &Config{RTK: true, Headroom: false, Caveman: false, CavemanLevel: 2, CodeStyle: false}
}

// Get returns a copy-safe snapshot.
func (c *Config) Get() Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Config{RTK: c.RTK, Headroom: c.Headroom, Caveman: c.Caveman, CavemanLevel: c.CavemanLevel, CodeStyle: c.CodeStyle}
}

// Set updates the config (used by admin API + Redis load).
func (c *Config) Set(r bool, headroom bool, caveman bool, cavemanLevel int, codeStyle bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.RTK, c.Headroom, c.Caveman, c.CavemanLevel, c.CodeStyle = r, headroom, caveman, cavemanLevel, codeStyle
}

// AnyEnabled reports whether any saver is active.
func (c *Config) AnyEnabled() bool {
	s := c.Get()
	return s.RTK || s.Headroom || s.Caveman || s.CodeStyle
}

// Directive builds the system-message suffix to inject for output/context savers.
// Returns "" when none is enabled.
func (c *Config) Directive() string {
	s := c.Get()
	var b strings.Builder
	if s.Headroom {
		b.WriteString(headroomDirective)
		b.WriteString("\n\n")
	}
	if s.Caveman {
		b.WriteString(cavemanDirectiveByLevel(s.CavemanLevel))
		b.WriteString("\n\n")
	}
	if s.CodeStyle {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(codeStyleDirective)
	}
	return strings.TrimSpace(b.String())
}

// cavemanDirectiveByLevel returns the caveman directive at the requested intensity.
func cavemanDirectiveByLevel(level int) string {
	switch level {
	case 1:
		return cavemanDirectiveLight
	case 3:
		return cavemanDirectiveAggressive
	default:
		return cavemanDirective
	}
}

// cavemanDirective — terse, substance-preserving output (adapted from
// JuliusBrussee/caveman). Saves up to ~65% output tokens.
const cavemanDirective = `Reply in terse "caveman speak": use fewest words, drop articles and polite fluff, keep technical substance. Example: instead of "I will now create the helper function that validates the input", say "make validate() input check". Never omit code, paths, values, or the actual fix.`

// cavemanDirectiveLight — lighter caveman mode (level 1). Saves ~40% output tokens.
const cavemanDirectiveLight = `Reply tersely: skip greetings, summaries, and "let me know if you need anything". Get to the point in 1-2 sentences. Keep code, paths, and values intact.`

// cavemanDirectiveAggressive — aggressive caveman mode (level 3). Saves ~80% output tokens.
const cavemanDirectiveAggressive = `MAXIMUM TERSeness. One sentence or less. No articles, no explanations, no sign-offs. Only raw code, commands, paths, or values. If you can't say it in 5 words, it doesn't need saying.`

// headroomDirective — compress repeated context / prior turns to save input+output tokens.
const headroomDirective = `[CONTEXT-COMPRESS] Keep prior turns tight: reference established context instead of repeating it, drop redundant restatements, and avoid echoing the user's request back. Reuse shorthand for variables/paths already introduced. This saves context tokens without losing information.`

// codeStyleDirective — lazy senior-dev YAGNI code style (renamed from Ponytail).
// Fewer tokens, shorter diffs, no over-engineering.
const codeStyleDirective = `Code like a lazy senior engineer: minimal diff, no speculative abstraction, no unused helpers, reuse existing utilities. Apply YAGNI: implement exactly what's asked, skip frameworks/config not needed. Prefer small, working changes over clever generality.`

// InjectDirective appends the output-saver directive to the system message
// of an OpenAI-style chat body (map with "messages"). Returns true if it
// mutated the body. Fail-open: any structural oddity is skipped.
func InjectDirective(body map[string]any, directive string) bool {
	if directive == "" || body == nil {
		return false
	}
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return false
	}
	// Find or create system message.
	const sysRole = "system"
	var sysMap map[string]any
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := mm["role"].(string); role == sysRole {
			sysMap = mm
			break
		}
	}
	if sysMap == nil {
		sysMap = map[string]any{"role": sysRole, "content": ""}
		msgs = append(msgs, sysMap)
		body["messages"] = msgs
	}
	existing, _ := sysMap["content"].(string)
	if strings.Contains(existing, "[TOKEN-SAVER]") {
		return false // already injected (idempotent)
	}
	if existing != "" {
		existing += "\n\n"
	}
	sysMap["content"] = existing + "[TOKEN-SAVER] " + directive
	return true
}

// ToJSON / FromJSON for Redis persistence.
func (c *Config) ToJSON() string {
	b, _ := json.Marshal(c.Get())
	return string(b)
}

// FromJSON loads from a JSON string; falls back to defaults on error.
func (c *Config) FromJSON(s string) {
	if s == "" {
		return
	}
	var v Config
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return
	}
	if v.CavemanLevel == 0 {
		v.CavemanLevel = 2
	}
	c.Set(v.RTK, v.Headroom, v.Caveman, v.CavemanLevel, v.CodeStyle)
}
