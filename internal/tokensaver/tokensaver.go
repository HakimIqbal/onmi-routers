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

	RTK      bool `json:"rtk"`      // input tool_result compression (see internal/rtk)
	Headroom bool `json:"headroom"` // context/history compression directive
	Caveman  bool `json:"caveman"`  // terse output directive
	Ponytail bool `json:"ponytail"` // YAGNI-first code style directive
	// CavemanLevel / PonytailLevel reserved for future granularity.
}

// DefaultConfig returns RTK on, others off (RTK is the lowest-risk win).
func DefaultConfig() *Config {
	return &Config{RTK: true, Headroom: false, Caveman: false, Ponytail: false}
}

// Get returns a copy-safe snapshot.
func (c *Config) Get() Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Config{RTK: c.RTK, Headroom: c.Headroom, Caveman: c.Caveman, Ponytail: c.Ponytail}
}

// Set updates the config (used by admin API + Redis load).
func (c *Config) Set(r bool, headroom bool, caveman bool, pony bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.RTK, c.Headroom, c.Caveman, c.Ponytail = r, headroom, caveman, pony
}

// AnyEnabled reports whether any saver is active.
func (c *Config) AnyEnabled() bool {
	s := c.Get()
	return s.RTK || s.Headroom || s.Caveman || s.Ponytail
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
		b.WriteString(cavemanDirective)
		b.WriteString("\n\n")
	}
	if s.Ponytail {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(ponytailDirective)
	}
	return strings.TrimSpace(b.String())
}

// cavemanDirective — terse, substance-preserving output (adapted from
// JuliusBrussee/caveman). Saves up to ~65% output tokens.
const cavemanDirective = `Reply in terse "caveman speak": use fewest words, drop articles and polite fluff, keep technical substance. Example: instead of "I will now create the helper function that validates the input", say "make validate() input check". Never omit code, paths, values, or the actual fix.`

// headroomDirective — compress repeated context / prior turns to save input+output tokens.
const headroomDirective = `[CONTEXT-COMPRESS] Keep prior turns tight: reference established context instead of repeating it, drop redundant restatements, and avoid echoing the user's request back. Reuse shorthand for variables/paths already introduced. This saves context tokens without losing information.`

// ponytailDirective — lazy senior-dev YAGNI code style (adapted from
// DietrichGebert/ponytail). Fewer tokens, shorter diffs, no over-engineering.
const ponytailDirective = `Code like a lazy senior engineer: minimal diff, no speculative abstraction, no unused helpers, reuse existing utilities. Apply YAGNI: implement exactly what's asked, skip frameworks/config not needed. Prefer small, working changes over clever generality.`

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
	c.Set(v.RTK, v.Headroom, v.Caveman, v.Ponytail)
}
