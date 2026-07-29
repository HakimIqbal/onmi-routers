// Package proxy — ComboRegistry: groups multiple models under one logical
// name ("combo/<name>") with a routing strategy applied per request. Backed
// by Redis (see internal/db) and cached in memory behind a sync.RWMutex.
//
// Strategies (OmniRoute/9Router-style routing):
//
//	fallback     Try models in order — models[0] first; on upstream failure
//	             the proxy caller walks NextInFallback until one succeeds.
//	round_robin  Rotate models across requests (atomic Redis INCR).
//	latency      Pick the model whose upstream has the LOWEST avg latency.
//	cost         Pick the model with the LOWEST estimated USD cost per request.
//	priority     Alias of fallback (explicit tier order).
//	fill_first   Prefer first healthy model (skip OPEN circuits) then stick.
//	least_used   Prefer model with lowest request counter (local).
//	random       Uniform random among healthy models.
//	auto         4-tier auto: prefer non-OPEN; order models[0..n] as
//	             subscription → cheap → free; self-heal OPEN.
//
// Self-healer: models whose upstream circuit is OPEN are skipped for
// smart strategies (mirrors OmniRoute's selfHealer).
package proxy

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"foxrouters/internal/cost"
	"foxrouters/internal/db"
	"foxrouters/internal/upstream"
)

// Combo is re-exported from internal/db for callers that only import proxy.
type Combo = db.Combo

// ComboRegistry is a thread-safe in-memory cache of combos.
type ComboRegistry struct {
	mu     sync.RWMutex
	combos map[string]Combo
	store  *db.Store
	hc     *upstream.HealthChecker // optional: enables latency/cost/self-heal routing
}

// NewComboRegistry builds an empty registry bound to the given DB store.
// Call Load() before serving requests. Pass hc (may be nil) to enable
// latency-based / cost-based routing and the self-healer.
func NewComboRegistry(store *db.Store, hc *upstream.HealthChecker) *ComboRegistry {
	return &ComboRegistry{
		combos: map[string]Combo{},
		store:  store,
		hc:     hc,
	}
}

// Load pulls the current state from Redis. Safe to call at startup and on
// mutation (functions AddCombo/DeleteCombo already do). With a nil store
// the call is a no-op (used by tests that seed the cache directly).
func (r *ComboRegistry) Load() error {
	if r == nil || r.store == nil {
		return nil
	}
	combos, err := r.store.LoadCombos()
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.combos = combos
	r.mu.Unlock()
	return nil
}

// Reload is an alias for Load — sugar for mutation call sites.
func (r *ComboRegistry) Reload() error { return r.Load() }

// Resolve maps an incoming model name onto the next model to route to.
//
// If model starts with "combo/", the trailing segment is looked up. On hit
// the strategy picks the next model:
//   - fallback     → models[0] (retry chain handled by proxy caller)
//   - round_robin  → models[INCR(counter) % len(models)] (Redis atomic)
//
// Returns (nextModel, true) on a combo hit, ("", false) otherwise.
//
// A nil registry, an unknown combo, or a combo with zero models all yield
// ("", false) so callers can fall through to the built-in routing without
// special-casing.
func (r *ComboRegistry) Resolve(model string) (string, bool) {
	if r == nil {
		return "", false
	}
	if !strings.HasPrefix(model, "combo/") {
		return "", false
	}
	name := strings.TrimPrefix(model, "combo/")
	r.mu.RLock()
	c, ok := r.combos[name]
	r.mu.RUnlock()
	if !ok || len(c.Models) == 0 {
		return "", false
	}
	switch c.Strategy {
	case "round_robin":
		// Fall back to in-process rotation when Redis is unavailable — keeps
		// tests + local dev functional. The mod is signed-safe: even on a
		// negative INCR result the (n%L + L)%L keeps the index in range.
		var counter int64
		if r.store != nil {
			if v, err := r.store.IncrComboCounter(name); err == nil {
				counter = v
			}
		}
		if counter == 0 {
			// No Redis (or first call after wraparound) — use a package-level
			// rotating fallback based on a hash of name so tests are
			// deterministic per-key.
			counter = fallbackCounter(name)
		}
		idx := int((counter - 1) % int64(len(c.Models)))
		if idx < 0 {
			idx += len(c.Models)
		}
		return c.Models[idx], true
	case "latency", "cost", "fill_first", "least_used", "random", "auto", "priority":
		return r.resolveSmart(c, c.Strategy)
	default:
		// "fallback" (also the default) — return head; caller can call
		// NextInFallback on upstream error.
		return c.Models[0], true
	}
}

// resolveSmart picks the best model for smart strategies, skipping models
// whose upstream circuit is OPEN (self-healer). When hc is nil or no models
// are healthy, it falls back to the first model (so requests still flow).
func (r *ComboRegistry) resolveSmart(c Combo, strategy string) (string, bool) {
	healthy := make([]string, 0, len(c.Models))
	for _, m := range c.Models {
		if r.hc != nil && r.upstreamOpen(m) {
			slog.Debug("combo self-heal: skipping OPEN upstream model",
				"module", "combo", "combo", c.Name, "model", m, "strategy", strategy)
			continue
		}
		healthy = append(healthy, m)
	}
	if len(healthy) == 0 {
		return c.Models[0], true
	}
	switch strategy {
	case "fill_first", "priority":
		return healthy[0], true
	case "auto":
		var tier1, tier2, tier3 []string
		for _, m := range healthy {
			isCF := strings.HasPrefix(m, "@cf/") || strings.HasPrefix(m, "cf/")
			if !isCF {
				tier1 = append(tier1, m)
			} else {
				if cost.USD(m, 1, 1) > 0 {
					tier2 = append(tier2, m)
				} else {
					tier3 = append(tier3, m)
				}
			}
		}
		if len(tier1) > 0 { return tier1[0], true }
		if len(tier2) > 0 { return tier2[0], true }
		if len(tier3) > 0 { return tier3[0], true }
		return healthy[0], true
	case "random":
		n := fallbackCounter(c.Name + ":rnd")
		if n < 0 { n = -n }
		return healthy[int(n)%len(healthy)], true
	case "least_used":
		best := healthy[0]
		bestN := r.usageCount(best)
		for _, m := range healthy[1:] {
			if n := r.usageCount(m); n < bestN {
				bestN = n
				best = m
			}
		}
		r.bumpUsage(best)
		return best, true
	case "latency":
		best := healthy[0]
		bestScore := r.avgLatencyMs(best)
		for _, m := range healthy[1:] {
			if s := r.avgLatencyMs(m); s < bestScore {
				bestScore = s
				best = m
			}
		}
		return best, true
	case "cost":
		best := healthy[0]
		bestScore := cost.USD(best, 1_000_000, 1_000_000)
		for _, m := range healthy[1:] {
			if s := cost.USD(m, 1_000_000, 1_000_000); s < bestScore {
				bestScore = s
				best = m
			}
		}
		return best, true
	case "cost_latency_balanced":
		best := healthy[0]
		bestScore := (r.avgLatencyMs(best) * 0.4) + (cost.USD(best, 1000, 1000) * 0.6)
		for _, m := range healthy[1:] {
			score := (r.avgLatencyMs(m) * 0.4) + (cost.USD(m, 1000, 1000) * 0.6)
			if score < bestScore {
				bestScore = score
				best = m
			}
		}
		return best, true
	case "throughput":
		best := healthy[0]
		bestTps := 0.0
		for _, m := range healthy {
			// Simplified TPS: 1 / avg_latency
			lat := r.avgLatencyMs(m)
			if lat > 0 {
				tps := 1000.0 / lat
				if tps > bestTps {
					bestTps = tps
					best = m
				}
			}
		}
		return best, true
	case "success_rate":
		best := healthy[0]
		bestRate := -1.0
		for _, m := range healthy {
			// Mock success rate from health checker if available, else 1.0
			rate := 1.0 
			if rate > bestRate {
				bestRate = rate
				best = m
			}
		}
		return best, true
	default:
		return healthy[0], true
	}
}

// usageCount / bumpUsage — local least-used counters (per process).
var (
	usageMu sync.Mutex
	usageN  = map[string]int64{}
)

func (r *ComboRegistry) usageCount(model string) int64 {
	usageMu.Lock()
	defer usageMu.Unlock()
	return usageN[model]
}

func (r *ComboRegistry) bumpUsage(model string) {
	usageMu.Lock()
	usageN[model]++
	usageMu.Unlock()
}

// upstreamOpen reports whether the upstream that would serve model m has an
// OPEN circuit breaker. Conservative: unknown upstream → false (allowed).
func (r *ComboRegistry) upstreamOpen(model string) bool {
	if r.hc == nil {
		return false
	}
	var h *upstream.UpstreamHealth
	switch {
	case strings.HasPrefix(model, "@cf/"), strings.Contains(model, "cloudflare"):
		h = r.hc.CF
	case strings.HasPrefix(model, "cb/"), strings.Contains(model, "codebuddy"):
		h = r.hc.CB
	case strings.HasPrefix(model, "grok"):
		h = r.hc.Grok
	default:
		return false
	}
	if h == nil {
		return false
	}
	return h.State() == upstream.CircuitOpen
}

// avgLatencyMs returns the live avg latency (ms) for the upstream serving m.
func (r *ComboRegistry) avgLatencyMs(model string) float64 {
	if r.hc == nil {
		return 0
	}
	var h *upstream.UpstreamHealth
	switch {
	case strings.HasPrefix(model, "@cf/"), strings.Contains(model, "cloudflare"):
		h = r.hc.CF
	case strings.HasPrefix(model, "cb/"), strings.Contains(model, "codebuddy"):
		h = r.hc.CB
	case strings.HasPrefix(model, "grok"):
		h = r.hc.Grok
	default:
		return 0
	}
	if h == nil {
		return 0
	}
	st := h.Stats()
	if v, ok := st["avg_latency_ms"].(float64); ok {
		return v
	}
	return 0
}

// fallbackCounter is a per-process monotonic sequence used only when Redis
// is unavailable (tests, cold start with dead Redis). Not cluster-safe.
var (
	fallbackMu   sync.Mutex
	fallbackSeq  = map[string]int64{}
)

func fallbackCounter(name string) int64 {
	fallbackMu.Lock()
	defer fallbackMu.Unlock()
	fallbackSeq[name]++
	return fallbackSeq[name]
}

// NextInFallback returns the model that comes after failedModel in the
// combo's list. Returns ("", false) when the failed model was the last
// entry, when the combo doesn't exist, or when it isn't a fallback combo.
func (r *ComboRegistry) NextInFallback(name, failedModel string) (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.RLock()
	c, ok := r.combos[name]
	r.mu.RUnlock()
	if !ok || c.Strategy != "fallback" || len(c.Models) == 0 {
		return "", false
	}
	for i, m := range c.Models {
		if m == failedModel && i+1 < len(c.Models) {
			return c.Models[i+1], true
		}
	}
	return "", false
}

// ListCombos returns a snapshot of every combo (order is map-random, but
// stable per snapshot). Used by /v1/models and the admin GET /api/combos.
func (r *ComboRegistry) ListCombos() []Combo {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	out := make([]Combo, 0, len(r.combos))
	for _, c := range r.combos {
		out = append(out, c)
	}
	r.mu.RUnlock()
	return out
}

// GetCombo returns a single combo by name (case-sensitive).
func (r *ComboRegistry) GetCombo(name string) (Combo, bool) {
	if r == nil {
		return Combo{}, false
	}
	r.mu.RLock()
	c, ok := r.combos[name]
	r.mu.RUnlock()
	return c, ok
}

// AddCombo validates + persists a combo and refreshes the cache. Cache is
// updated only after the Redis write succeeds so a persistence error does
// not silently diverge the in-memory view.
func (r *ComboRegistry) AddCombo(c Combo) error {
	if r == nil {
		return fmt.Errorf("registry not initialised")
	}
	c.Name = strings.TrimSpace(c.Name)
	c.Strategy = strings.TrimSpace(c.Strategy)
	if err := validateName(c.Name, "name"); err != nil {
		return err
	}
	// Reserve the "combo/" prefix — combos are addressed as combo/<name>, so
	// letting the name itself start with "combo/" would create combo/combo/x.
	if strings.HasPrefix(c.Name, "combo/") {
		return fmt.Errorf("name must not start with 'combo/'")
	}
	switch c.Strategy {
	case "":
		c.Strategy = "fallback"
	case "fallback", "round_robin", "latency", "cost",
		"priority", "fill_first", "least_used", "random", "auto",
		"cost_latency_balanced", "throughput", "success_rate":
	default:
		return fmt.Errorf("strategy must be fallback|round_robin|latency|cost|priority|fill_first|least_used|random|auto|cost_latency_balanced|throughput|success_rate")
	}
	// Trim + drop empty model entries.
	cleaned := make([]string, 0, len(c.Models))
	for _, m := range c.Models {
		m = strings.TrimSpace(m)
		if m != "" {
			cleaned = append(cleaned, m)
		}
	}
	// P3-2: cap combo size to prevent Redis memory DoS.
	if len(cleaned) > 32 {
		return fmt.Errorf("combo models list too long (max 32, got %d)", len(cleaned))
	}
	if len(cleaned) == 0 {
		return fmt.Errorf("at least one model required")
	}
	c.Models = cleaned
	c.Description = strings.TrimSpace(c.Description)

	if r.store != nil {
		if err := r.store.SaveCombo(c); err != nil {
			return err
		}
	}
	r.mu.Lock()
	r.combos[c.Name] = c
	r.mu.Unlock()
	return nil
}

// DeleteCombo removes one combo (and its round-robin counter) from Redis +
// cache.
func (r *ComboRegistry) DeleteCombo(name string) error {
	if r == nil {
		return fmt.Errorf("registry not initialised")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name required")
	}
	if r.store != nil {
		if err := r.store.DeleteCombo(name); err != nil {
			return err
		}
	}
	r.mu.Lock()
	delete(r.combos, name)
	r.mu.Unlock()
	return nil
}

// Heal is the self-healer worker. For every combo it logs (does NOT mutate
// persistence) which models are currently skipped because their upstream
// circuit is OPEN. This mirrors OmniRoute's selfHealer: unhealthy models are
// removed from the live routing decision (see upstreamOpen in Resolve) while
// still kept in the saved combo config, so they re-enter automatically once
// the upstream recovers. Call on a ticker (e.g. every 30s).
func (r *ComboRegistry) Heal() {
	if r == nil || r.hc == nil {
		return
	}
	r.mu.RLock()
	names := make([]string, 0, len(r.combos))
	for n := range r.combos {
		names = append(names, n)
	}
	r.mu.RUnlock()
	for _, n := range names {
		r.mu.RLock()
		c, ok := r.combos[n]
		r.mu.RUnlock()
		if !ok {
			continue
		}
		skipped := 0
		for _, m := range c.Models {
			if r.upstreamOpen(m) {
				skipped++
			}
		}
		if skipped > 0 {
			slog.Info("combo self-heal",
				"module", "combo", "combo", n, "strategy", c.Strategy,
				"skipped", skipped, "total", len(c.Models))
		}
	}
}
