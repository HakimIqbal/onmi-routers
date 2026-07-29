// Package quota — live quota / spend tracking per upstream family + free-tier drain.
// Used by combo auto routing to prefer healthy pools with remaining capacity.
package quota

import (
	"sync"
	"time"
)

type Family string

const (
	Grok Family = "grok"
	CB   Family = "codebuddy"
	CF   Family = "cloudflare"
)

type Snapshot struct {
	Family        Family  `json:"family"`
	Accounts      int     `json:"accounts"`
	Healthy       int     `json:"healthy"`
	Disabled      int     `json:"disabled"`
	CreditsRemain float64 `json:"credits_remain,omitempty"`
	CreditsLimit  float64 `json:"credits_limit,omitempty"`
	TokensToday   int64   `json:"tokens_today"`
	RequestsToday int64   `json:"requests_today"`
	CostUSDToday  float64 `json:"cost_usd_today"`
	CircuitOpen   bool    `json:"circuit_open"`
	UpdatedAt     string  `json:"updated_at"`
}

type Tracker struct {
	mu   sync.RWMutex
	snap map[Family]Snapshot
	day  string // YYYY-MM-DD UTC
}

func New() *Tracker {
	return &Tracker{snap: map[Family]Snapshot{}, day: dayKey()}
}

func dayKey() string { return time.Now().UTC().Format("2006-01-02") }

func (t *Tracker) rollDay() {
	d := dayKey()
	if d == t.day {
		return
	}
	t.day = d
	for k, s := range t.snap {
		s.TokensToday = 0
		s.RequestsToday = 0
		s.CostUSDToday = 0
		t.snap[k] = s
	}
}

func (t *Tracker) SetPool(f Family, accounts, healthy, disabled int, circuitOpen bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rollDay()
	s := t.snap[f]
	s.Family = f
	s.Accounts = accounts
	s.Healthy = healthy
	s.Disabled = disabled
	s.CircuitOpen = circuitOpen
	s.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	t.snap[f] = s
}

// SetPoolS is a string-friendly wrapper for handlers that pass family as string.
func (t *Tracker) SetPoolS(family string, accounts, healthy, disabled int, circuitOpen bool) {
	t.SetPool(Family(family), accounts, healthy, disabled, circuitOpen)
}

func (t *Tracker) SetCredits(f Family, remain, limit float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rollDay()
	s := t.snap[f]
	s.Family = f
	s.CreditsRemain = remain
	s.CreditsLimit = limit
	s.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	t.snap[f] = s
}

func (t *Tracker) AddUsage(f Family, tokens int64, cost float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rollDay()
	s := t.snap[f]
	s.Family = f
	s.TokensToday += tokens
	s.RequestsToday++
	s.CostUSDToday += cost
	s.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	t.snap[f] = s
}

func (t *Tracker) All() []Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]Snapshot, 0, len(t.snap))
	for _, f := range []Family{Grok, CB, CF} {
		if s, ok := t.snap[f]; ok {
			out = append(out, s)
		} else {
			out = append(out, Snapshot{Family: f})
		}
	}
	return out
}

// Preferable returns true if family looks usable for auto routing.
func (t *Tracker) Preferable(f Family) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s, ok := t.snap[f]
	if !ok {
		return true
	}
	if s.CircuitOpen {
		return false
	}
	if s.Accounts > 0 && s.Healthy == 0 {
		return false
	}
	if s.CreditsLimit > 0 && s.CreditsRemain <= 0 {
		return false
	}
	return true
}
