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
	// ── Cloudflare Workers AI — official token prices (USD / 1M tokens)
	// Source: https://developers.cloudflare.com/workers-ai/platform/pricing/#llm-model-pricing
	// Matched longest-prefix on lowercased model id (incl. @cf/ and cf/ forms).
	"@cf/meta/llama-3.2-1b-instruct":              {In: 0.027, Out: 0.201},
	"@cf/meta/llama-3.2-3b-instruct":              {In: 0.051, Out: 0.335},
	"@cf/meta/llama-3.1-8b-instruct-fp8-fast":     {In: 0.045, Out: 0.384},
	"@cf/meta/llama-3.2-11b-vision-instruct":      {In: 0.049, Out: 0.676},
	"@cf/meta/llama-3.1-70b-instruct-fp8-fast":    {In: 0.293, Out: 2.253},
	"@cf/meta/llama-3.3-70b-instruct-fp8-fast":    {In: 0.293, Out: 2.253},
	"@cf/deepseek-ai/deepseek-r1-distill-qwen-32b": {In: 0.497, Out: 4.881},
	"@cf/mistral/mistral-7b-instruct-v0.1":        {In: 0.110, Out: 0.190},
	"@cf/mistralai/mistral-small-3.1-24b-instruct": {In: 0.351, Out: 0.555},
	"@cf/meta/llama-3.1-8b-instruct-fp8":          {In: 0.152, Out: 0.287},
	"@cf/meta/llama-3.1-8b-instruct-awq":          {In: 0.123, Out: 0.266},
	"@cf/meta/llama-3.1-8b-instruct":              {In: 0.282, Out: 0.827},
	"@cf/meta/llama-3-8b-instruct-awq":            {In: 0.123, Out: 0.266},
	"@cf/meta/llama-3-8b-instruct":                {In: 0.282, Out: 0.827},
	"@cf/meta/llama-2-7b-chat-fp16":               {In: 0.556, Out: 6.667},
	"@cf/meta/llama-guard-3-8b":                   {In: 0.484, Out: 0.030},
	"@cf/meta/llama-4-scout-17b-16e-instruct":     {In: 0.270, Out: 0.850},
	"@cf/google/gemma-3-12b-it":                   {In: 0.345, Out: 0.556},
	"@cf/google/gemma-4-26b-a4b-it":               {In: 0.100, Out: 0.300},
	"@cf/qwen/qwq-32b":                            {In: 0.660, Out: 1.000},
	"@cf/qwen/qwen2.5-coder-32b-instruct":         {In: 0.660, Out: 1.000},
	"@cf/qwen/qwen3-30b-a3b-fp8":                  {In: 0.051, Out: 0.335},
	"@cf/openai/gpt-oss-120b":                     {In: 0.350, Out: 0.750},
	"@cf/openai/gpt-oss-20b":                      {In: 0.200, Out: 0.300},
	"@cf/aisingapore/gemma-sea-lion-v4-27b-it":    {In: 0.351, Out: 0.555},
	"@cf/ibm-granite/granite-4.0-h-micro":         {In: 0.017, Out: 0.112},
	"@cf/zai-org/glm-4.7-flash":                   {In: 0.060, Out: 0.400},
	"@cf/zai-org/glm-5.2":                         {In: 1.400, Out: 4.400},
	"@cf/nvidia/nemotron-3-120b-a12b":             {In: 0.500, Out: 1.500},
	"@cf/moonshotai/kimi-k2.5":                    {In: 0.600, Out: 3.000},
	"@cf/moonshotai/kimi-k2.6":                    {In: 0.950, Out: 4.000},
	"@cf/moonshotai/kimi-k2.7-code":               {In: 0.950, Out: 4.000},
	// short-form cf/* (same prices; ExpandCFAlias maps to @cf/…)
	"cf/llama-70b":      {In: 0.293, Out: 2.253},
	"cf/llama-8b":       {In: 0.045, Out: 0.384},
	"cf/deepseek-r1":    {In: 0.497, Out: 4.881},
	"cf/kimi-k2.5":      {In: 0.600, Out: 3.000},
	"cf/kimi-k2.6":      {In: 0.950, Out: 4.000},
	"cf/kimi-k2.7-code": {In: 0.950, Out: 4.000},
	"cf/glm-4.7-flash":  {In: 0.060, Out: 0.400},
	"cf/glm-5.2":        {In: 1.400, Out: 4.400},
	"cf/gpt-oss-120b":   {In: 0.350, Out: 0.750},
	"cf/gpt-oss-20b":    {In: 0.200, Out: 0.300},
	// Fallback for any other @cf/ model (neuron billing; $0 until mapped)
	"@cf/": {In: 0.0, Out: 0.0},
	"cf/":  {In: 0.0, Out: 0.0},
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
