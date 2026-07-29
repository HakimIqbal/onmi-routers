// Package mcp — MCP-over-HTTP + A2A agent surface backed by live gateway state.
package mcp

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"foxrouters/internal/cost"
	"foxrouters/internal/db"
	"foxrouters/internal/evals"
	"foxrouters/internal/guardrails"
	"foxrouters/internal/memory"
	"foxrouters/internal/proxy"

	"github.com/gin-gonic/gin"
)

var Mem = memory.New(500)

// Optional live deps set from main.
var (
	DBStore   *db.Store
	BaseURL   = "http://127.0.0.1:20130"
	EvalKeyFn func() string // returns a gateway API key for evals
)

func Register(r gin.IRoutes) {
	r.POST("/mcp", handle)
	r.POST("/a2a", handleA2A)
	r.GET("/mcp/tools", listTools)
	r.POST("/api/evals/run", runEvalsHTTP)
	r.GET("/api/evals", listEvals)
}

func listTools(c *gin.Context) {
	c.JSON(200, gin.H{"tools": tools()})
}

func tools() []gin.H {
	return []gin.H{
		{"name": "list_models", "description": "List known catalog model families"},
		{"name": "health", "description": "Gateway health summary"},
		{"name": "quota", "description": "Live quota / spend per upstream family"},
		{"name": "cache_stats", "description": "Semantic cache stats"},
		{"name": "cache_toggle", "description": "Enable/disable semantic cache", "params": []string{"enabled"}},
		{"name": "cost_estimate", "description": "Estimate USD for model+tokens", "params": []string{"model", "tokens_in", "tokens_out"}},
		{"name": "list_combos", "description": "List combo presets"},
		{"name": "memory_search", "description": "Search session memory", "params": []string{"q", "limit"}},
		{"name": "memory_add", "description": "Add memory item", "params": []string{"key", "content"}},
		{"name": "guardrails_status", "description": "Guardrails config"},
		{"name": "run_evals", "description": "Run lightweight self-eval suite"},
		{"name": "history_summary", "description": "Request stats last N hours", "params": []string{"hours"}},
	}
}

func handle(c *gin.Context) {
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      any             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	switch req.Method {
	case "tools/list", "list_tools", "initialize":
		c.JSON(200, gin.H{"jsonrpc": "2.0", "id": req.ID, "result": gin.H{
			"tools": tools(),
			"serverInfo": gin.H{"name": "onmi-routers", "version": "dev"},
			"protocolVersion": "2024-11-05",
			"capabilities": gin.H{"tools": gin.H{}},
		}})
	case "tools/call", "call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.Name == "" {
			// some clients put name at top level
			var alt map[string]any
			_ = json.Unmarshal(req.Params, &alt)
			if n, ok := alt["name"].(string); ok {
				p.Name = n
			}
			if a, ok := alt["arguments"].(map[string]any); ok {
				p.Arguments = a
			}
		}
		c.JSON(200, gin.H{"jsonrpc": "2.0", "id": req.ID, "result": callTool(p.Name, p.Arguments)})
	default:
		c.JSON(200, gin.H{"jsonrpc": "2.0", "id": req.ID, "result": callTool(req.Method, map[string]any{})})
	}
}

func handleA2A(c *gin.Context) {
	var req map[string]any
	_ = c.ShouldBindJSON(&req)
	method, _ := req["method"].(string)
	if method == "" {
		method = "skills/list"
	}
	switch method {
	case "skills/list", "agent/card":
		c.JSON(200, gin.H{
			"name":        "OnmiRouters Agent",
			"description": "Go AI gateway: multi-account CF/Grok/CB, combos, cache, quota",
			"skills": []string{
				"smart_routing", "combo_auto", "quota", "cost", "health",
				"memory", "cache", "evals", "embeddings", "gemini_translate",
			},
			"url": BaseURL,
		})
	case "message/send", "tasks/send":
		// Extract text and route to memory + quota snapshot
		text, _ := req["message"].(string)
		if text == "" {
			if m, ok := req["params"].(map[string]any); ok {
				text, _ = m["message"].(string)
			}
		}
		if strings.TrimSpace(text) != "" {
			Mem.Add("a2a", text)
		}
		c.JSON(200, gin.H{
			"status": "accepted",
			"quota":  proxy.QuotaTracker.All(),
			"cache":  proxy.SemanticCache.Stats(),
		})
	default:
		c.JSON(200, gin.H{"ok": true, "method": method, "tools": tools()})
	}
}

func callTool(name string, args map[string]any) any {
	switch name {
	case "quota":
		if proxy.QuotaTracker != nil {
			return proxy.QuotaTracker.All()
		}
		return []any{}
	case "cache_stats":
		if proxy.SemanticCache != nil {
			return proxy.SemanticCache.Stats()
		}
		return gin.H{}
	case "cache_toggle":
		en := true
		if v, ok := args["enabled"].(bool); ok {
			en = v
		}
		if proxy.SemanticCache != nil {
			proxy.SemanticCache.SetEnabled(en)
			return proxy.SemanticCache.Stats()
		}
		return gin.H{"error": "no cache"}
	case "cost_estimate":
		model, _ := args["model"].(string)
		tin := int64(asNum(args["tokens_in"]))
		tout := int64(asNum(args["tokens_out"]))
		if tin == 0 {
			tin = 1000
		}
		if tout == 0 {
			tout = 500
		}
		usd := cost.USD(model, tin, tout)
		return gin.H{"model": model, "tokens_in": tin, "tokens_out": tout, "cost_usd": usd}
	case "list_combos":
		return gin.H{"presets": []string{"combo/auto", "combo/cheap", "combo/coding", "combo/cf-free", "combo/balanced"},
			"strategies": []string{"fallback", "round_robin", "latency", "cost", "priority", "fill_first", "least_used", "random", "auto"}}
	case "memory_search":
		q, _ := args["q"].(string)
		return Mem.Search(q, 10)
	case "memory_add":
		key, _ := args["key"].(string)
		content, _ := args["content"].(string)
		return Mem.Add(key, content)
	case "guardrails_status":
		return guardrails.Status()
	case "run_evals":
		key := ""
		if EvalKeyFn != nil {
			key = EvalKeyFn()
		}
		if key == "" {
			key = os.Getenv("EVAL_GATEWAY_KEY")
		}
		if key == "" {
			return gin.H{"error": "no gateway key for evals"}
		}
		rep := evals.RunAgainst(BaseURL, key, nil)
		return rep
	case "history_summary":
		hours := int(asNum(args["hours"]))
		if hours <= 0 {
			hours = 24
		}
		if DBStore == nil {
			return gin.H{"error": "db not wired"}
		}
		stats, err := DBStore.GetRequestStats(time.Now().Add(-time.Duration(hours) * time.Hour))
		if err != nil {
			return gin.H{"error": err.Error()}
		}
		return stats
	case "health":
		return gin.H{"service": "onmi-routers", "mcp": true, "time": time.Now().UTC().Format(time.RFC3339)}
	case "list_models":
		return gin.H{
			"families": []string{"grok-*", "cb/*", "@cf/*", "combo/*"},
			"hint":     "GET /v1/models",
		}
	default:
		return gin.H{"error": "unknown tool", "name": name, "available": tools()}
	}
}

func runEvalsHTTP(c *gin.Context) {
	key := ""
	if EvalKeyFn != nil {
		key = EvalKeyFn()
	}
	if key == "" {
		// try bearer of caller
		h := c.GetHeader("Authorization")
		if strings.HasPrefix(h, "Bearer ") {
			key = strings.TrimPrefix(h, "Bearer ")
		}
	}
	if key == "" {
		c.JSON(400, gin.H{"error": "need gateway API key"})
		return
	}
	var req struct {
		Cases []evals.Case `json:"cases"`
	}
	_ = c.ShouldBindJSON(&req)
	rep := evals.RunAgainst(BaseURL, key, req.Cases)
	c.JSON(200, rep)
}

func listEvals(c *gin.Context) {
	c.JSON(200, gin.H{"default_cases": evals.DefaultCases()})
}

func asNum(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	default:
		return 0
	}
}
