package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"foxrouters/internal/console"
	"foxrouters/internal/proxy"
	"github.com/gin-gonic/gin"
)

// ============================================================================
// TOKEN SAVER (RTK + Caveman + Ponytail)
// ============================================================================

// handleGetTokenSaver returns the current Token Saver config.
func handleGetTokenSaver() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(200, TokenSaverCfg.Get())
	}
}

// handleSetTokenSaver updates + persists the Token Saver config.
func handleSetTokenSaver() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			RTK      *bool `json:"rtk"`
			Headroom *bool `json:"headroom"`
			Caveman  *bool `json:"caveman"`
			Ponytail *bool `json:"ponytail"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "invalid body"})
			return
		}
		cur := TokenSaverCfg.Get()
		if req.RTK != nil {
			cur.RTK = *req.RTK
		}
		if req.Headroom != nil {
			cur.Headroom = *req.Headroom
		}
		if req.Caveman != nil {
			cur.Caveman = *req.Caveman
		}
		if req.Ponytail != nil {
			cur.Ponytail = *req.Ponytail
		}
		TokenSaverCfg.Set(cur.RTK, cur.Headroom, cur.Caveman, cur.Ponytail)
		// Sync into proxy hot-path global.
		proxy.TokenSaver = TokenSaverCfg
		// Persist to Redis.
		if rc := dbRef.Redis(); rc != nil {
			_ = rc.Set(context.Background(), "tokensaver:cfg", TokenSaverCfg.ToJSON(), 0).Err()
		}
		c.JSON(200, gin.H{
			"rtk":      cur.RTK,
			"headroom": cur.Headroom,
			"caveman":  cur.Caveman,
			"ponytail": cur.Ponytail,
		})
	}
}

// ============================================================================
// LIVE CONSOLE (SSE)
// ============================================================================

// handleConsoleStream streams live gateway log lines via Server-Sent Events.
func handleConsoleStream() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")

		flusher, ok := c.Writer.(http.Flusher)
		if !ok {
			c.JSON(500, gin.H{"error": "streaming unsupported"})
			return
		}

		// Send buffered history first so the panel isn't empty on connect.
		for _, l := range LiveConsole.Recent(100) {
			writeSSE(c, flusher, l)
		}

		ch := make(chan console.Line, 32)
		LiveConsole.Subscribe(ch)
		defer LiveConsole.Unsubscribe(ch)

		ctx := c.Request.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case l, ok := <-ch:
				if !ok {
					return
				}
				writeSSE(c, flusher, l)
			}
		}
	}
}

func writeSSE(c *gin.Context, f http.Flusher, l console.Line) {
	payload, _ := json.Marshal(l)
	c.Writer.WriteString("data: " + string(payload) + "\n\n")
	f.Flush()
}

// ============================================================================
// CLI TOOLS CONFIG GENERATOR (9Router-style client setup)
// ============================================================================

// handleCLIToolsConfig returns ready-to-paste config snippets for popular
// AI coding CLI tools, pointed at this gateway's /v1 endpoint.
func handleCLIToolsConfig() gin.HandlerFunc {
	return func(c *gin.Context) {
		base := strings.TrimRight(c.Request.Host, "/")
		// Prefer forwarded headers (set by Cloudflare tunnel / reverse proxy)
		// so the CLI endpoint reflects the public origin, not 127.0.0.1.
		if fwd := c.GetHeader("X-Forwarded-Host"); fwd != "" {
			base = strings.TrimRight(fwd, "/")
		}
		scheme := "https"
		if p := c.GetHeader("X-Forwarded-Proto"); p != "" {
			scheme = p
		} else if c.Request.TLS == nil && !strings.Contains(base, "onmi.my.id") {
			scheme = "http"
		}
		endpoint := scheme + "://" + base + "/v1"
		// Gateway key: use the first available key from the auth manager.
		apiKey := gatewayKeySample()
		models := []string{
			"grok-4.5", "grok-4.5-fast", "grok-3",
			"cb/claude-sonnet-4.6", "cb/claude-opus-4.6", "cb/gpt-5",
			"@cf/moonshotai/kimi-k2.6", "@cf/moonshotai/kimi-k2.7-code", "@cf/zai-org/glm-4.7-flash",
			"@cf/openai/gpt-oss-120b", "@cf/meta/llama-3.3-70b-instruct-fp8-fast",
			"@cf/meta/llama-3.1-8b-instruct-fp8-fast", "@cf/google/gemma-4-26b-a4b-it",
			"combo/auto", "combo/coding", "combo/cheap",
		}

		out := map[string]any{
			"endpoint": endpoint,
			"apiKey":   apiKey,
			"models":   models,
			"clients": map[string]any{
				"claude-code": map[string]any{
					"env": map[string]string{
						"ANTHROPIC_BASE_URL": endpoint,
						"ANTHROPIC_API_KEY":  apiKey,
					},
					"note": "claude-code uses /v1/messages; gateway normalises x-api-key → Bearer.",
				},
				"codex": map[string]any{
					"config": map[string]string{
						"OPENAI_BASE_URL": endpoint,
						"OPENAI_API_KEY":  apiKey,
					},
				},
				"cursor": map[string]any{
					"config": map[string]string{
						"OPENAI_API_BASE": endpoint,
						"OPENAI_API_KEY":  apiKey,
					},
				},
				"cline": map[string]any{
					"config": map[string]string{
						"openai.baseUrl": endpoint,
						"openai.apiKey":  apiKey,
					},
				},
				"openclaw": map[string]any{
					"env": map[string]string{
						"OPENAI_BASE_URL": endpoint,
						"OPENAI_API_KEY":  apiKey,
					},
				},
				"continue": map[string]any{
					"config": map[string]string{
						"requestOptions.baseURL": endpoint,
						"requestOptions.apiKey":  apiKey,
					},
				},
				"roo": map[string]any{
					"config": map[string]string{
						"openai.baseUrl": endpoint,
						"openai.apiKey":  apiKey,
					},
				},
				"copilot": map[string]any{
					"config": map[string]string{
						"endpoint": endpoint,
						"token":    apiKey,
					},
				},
			},
		}
		c.JSON(200, out)
	}
}

// gatewayKeySample returns one gateway key for client config snippets.
// Reads from the auth manager; returns "YOUR_GATEWAY_KEY" if none configured.
func gatewayKeySample() string {
	if authMgrRef == nil {
		return "YOUR_GATEWAY_KEY"
	}
	keys := authMgrRef.GetAll()
	if len(keys) == 0 {
		return "YOUR_GATEWAY_KEY"
	}
	return keys[0].Key
}
