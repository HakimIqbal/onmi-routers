package features

// Preset combos for 4-tier fallback (subscription → paid → cheap CF → free CF).
// Seeded at startup if missing.

import (
	"log/slog"

	"foxrouters/internal/db"
	"foxrouters/internal/proxy"
)

// SeedDefaultCombos installs OmniRoute/9Router-style presets when absent.
func SeedDefaultCombos(reg *proxy.ComboRegistry) {
	if reg == nil {
		return
	}
	presets := []db.Combo{
		{
			Name:        "auto",
			Strategy:    "auto",
			Description: "4-tier auto: Grok/CB subscription → CB paid → CF cheap → CF free",
			Models: []string{
				"grok-4.5",
				"cb/claude-sonnet-4.6",
				"cb/gpt-5",
				"@cf/meta/llama-3.3-70b-instruct-fp8-fast",
				"@cf/openai/gpt-oss-120b",
				"@cf/meta/llama-3.1-8b-instruct-fp8-fast",
				"@cf/google/gemma-4-26b-a4b-it",
				"@cf/meta/llama-3.2-3b-instruct",
			},
		},
		{
			Name:        "cheap",
			Strategy:    "cost",
			Description: "Cheapest CF-first routing",
			Models: []string{
				"@cf/meta/llama-3.2-1b-instruct",
				"@cf/meta/llama-3.2-3b-instruct",
				"@cf/ibm-granite/granite-4.0-h-micro",
				"@cf/meta/llama-3.1-8b-instruct-fp8-fast",
				"@cf/zai-org/glm-4.7-flash",
			},
		},
		{
			Name:        "coding",
			Strategy:    "priority",
			Description: "Coding-optimized: CB codex/kimi → CF coder models",
			Models: []string{
				"cb/gpt-5.3-codex",
				"cb/claude-sonnet-4.6",
				"@cf/moonshotai/kimi-k2.7-code",
				"@cf/qwen/qwen2.5-coder-32b-instruct",
				"@cf/zai-org/glm-5.2",
			},
		},
		{
			Name:        "cf-free",
			Strategy:    "fill_first",
			Description: "Cloudflare Workers AI free/cheap pool only",
			Models: []string{
				"@cf/meta/llama-3.2-3b-instruct",
				"@cf/meta/llama-3.1-8b-instruct-fp8-fast",
				"@cf/google/gemma-4-26b-a4b-it",
				"@cf/qwen/qwen3-30b-a3b-fp8",
				"@cf/openai/gpt-oss-20b",
			},
		},
		{
			Name:        "balanced",
			Strategy:    "latency",
			Description: "Latency-optimized across families",
			Models: []string{
				"@cf/meta/llama-3.1-8b-instruct-fp8-fast",
				"grok-4.5-low",
				"cb/gemini-2.5-flash",
				"@cf/zai-org/glm-4.7-flash",
			},
		},
	}
	for _, p := range presets {
		if _, ok := reg.GetCombo(p.Name); ok {
			continue
		}
		if err := reg.AddCombo(p); err != nil {
			slog.Warn("seed combo failed", "name", p.Name, "error", err)
		} else {
			slog.Info("seeded combo preset", "name", p.Name, "strategy", p.Strategy, "models", len(p.Models))
		}
	}
}
