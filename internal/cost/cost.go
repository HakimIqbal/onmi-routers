// Package cost — USD cost estimation from token usage (OmniRoute-style pricing).
//
// Ported concept from diegosouzapw/OmniRoute costRules: token->USD via a
// per-model pricing table. Costs are estimates only (provider pricing varies
// by tier/region); they power the per-key budget + spend dashboard.
//
// Cloudflare Workers AI bills per "neuron" (not tokens) — see USDNeurons.
package cost

import (
	"strings"
	"sync"
)

// Price is USD per 1M tokens.
type Price struct {
	In  float64
	Out float64
}

// Static pricing table (USD / 1M tokens). Approximate public list prices;
// tune as needed. Matched by longest-prefix on the model id (lowercased).
var table = map[string]Price{
	// ── Grok / xAI ──
	"grok-4.5":          {In: 3.00, Out: 15.00},
	"grok-4.1":          {In: 0.50, Out: 1.50},
	"grok-4":            {In: 5.00, Out: 15.00},
	"grok-3":            {In: 0.30, Out: 0.75},
	"grok-3-mini":       {In: 0.10, Out: 0.40},
	"grok-code":         {In: 0.50, Out: 1.50},
	"grok-2":            {In: 2.00, Out: 10.00},
	// ── CodeBuddy (Tencent) — internal proxy, cheap passthrough estimate ──
	"cb/":               {In: 0.35, Out: 1.05},
	// ── OpenAI ──
	"gpt-5":             {In: 1.25, Out: 10.00},
	"gpt-4.1":           {In: 2.00, Out: 8.00},
	"gpt-4o":            {In: 2.50, Out: 10.00},
	"o3":                {In: 10.00, Out: 40.00},
	"o4":                {In: 5.00, Out: 20.00},
	"gpt-4o-mini":       {In: 0.15, Out: 0.60},
	// ── Anthropic ──
	"claude-opus-4":     {In: 15.00, Out: 75.00},
	"claude-sonnet-4":   {In: 3.00, Out: 15.00},
	"claude-haiku-4":    {In: 0.80, Out: 4.00},
	// ── Google ──
	"gemini-2.5-pro":    {In: 1.25, Out: 10.00},
	"gemini-2.5-flash":  {In: 0.30, Out: 2.50},
	"gemini-2.0-flash":  {In: 0.10, Out: 0.40},
	// ── DeepSeek ──
	"deepseek-reasoner": {In: 0.55, Out: 2.19},
	"deepseek-chat":     {In: 0.27, Out: 1.10},
	// ── Cloudflare Workers AI — bills per "neuron", not tokens ──
	// Flat estimate ~$0.011 / 1M neurons (Llama-3.2-1B class). Paid plans
	// vary per model; this is a sane default for the spend dashboard.
	"@cf/": {In: 0.0, Out: 0.0},
	// ── Defaults ──
	"combo/": {In: 1.00, Out: 5.00},
}

// CFNeuronRate is USD per 1M neurons (Cloudflare Workers AI billing unit).
const CFNeuronRate = 0.011

var (
	mu     sync.RWMutex
	lookup []struct{ prefix string; p Price }
	built  bool
)

func ensureBuilt() {
	mu.RLock()
	if built {
		mu.RUnlock()
		return
	}
	mu.RUnlock()
	mu.Lock()
	defer mu.Unlock()
	if built {
		return
	}
	for k, v := range table {
		lookup = append(lookup, struct{ prefix string; p Price }{k, v})
	}
	// sort by prefix length desc so longest match wins
	for i := 0; i < len(lookup); i++ {
		for j := i + 1; j < len(lookup); j++ {
			if len(lookup[j].prefix) > len(lookup[i].prefix) {
				lookup[i], lookup[j] = lookup[j], lookup[i]
			}
		}
	}
	built = true
}

// PriceFor returns the price for a model id (longest-prefix match).
func PriceFor(model string) Price {
	ensureBuilt()
	m := strings.ToLower(model)
	mu.RLock()
	defer mu.RUnlock()
	for _, e := range lookup {
		if strings.HasPrefix(m, e.prefix) {
			return e.p
		}
	}
	return Price{In: 1.00, Out: 5.00} // sane fallback
}

// USD estimates cost in USD for a given model + token counts.
func USD(model string, tokensIn, tokensOut int64) float64 {
	p := PriceFor(model)
	in := float64(tokensIn) / 1_000_000.0 * p.In
	out := float64(tokensOut) / 1_000_000.0 * p.Out
	return in + out
}

// USDNeurons estimates Cloudflare Workers AI cost from the "neurons" billing
// unit reported in the response usage. CF bills per 1M neurons.
func USDNeurons(neurons float64) float64 {
	return neurons / 1_000_000.0 * CFNeuronRate
}
