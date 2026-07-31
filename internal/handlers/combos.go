// Package handlers — combos admin API (v1.4.0).
//
// All four handlers are admin-only (wire under AdminMiddleware). State lives
// in Redis via *proxy.ComboRegistry; the registry caches combos in memory
// and reloads on every mutation.
package handlers

import (
	"strings"

	"foxrouters/internal/db"
	"foxrouters/internal/proxy"

	"github.com/gin-gonic/gin"
)

// comboInput is the wire schema for POST /api/combos.
type comboInput struct {
	Name            string   `json:"name"`
	Strategy        string   `json:"strategy"`
	Models          []string `json:"models"`
	Description     string   `json:"description"`
	CompressionMode string   `json:"compression_mode"` // per-combo override: off/lite/standard/aggressive/ultra
}

// HandleListCombos: GET /api/combos → { combos: [Combo, ...] }.
func HandleListCombos(reg *proxy.ComboRegistry) gin.HandlerFunc {
	return func(c *gin.Context) {
		if reg == nil {
			c.JSON(500, gin.H{"error": "combo registry not initialised"})
			return
		}
		c.JSON(200, gin.H{"combos": reg.ListCombos()})
	}
}

// HandleGetCombo: GET /api/combos/*name → { combo: Combo } | 404.
func HandleGetCombo(reg *proxy.ComboRegistry) gin.HandlerFunc {
	return func(c *gin.Context) {
		if reg == nil {
			c.JSON(500, gin.H{"error": "combo registry not initialised"})
			return
		}
		name := strings.TrimPrefix(c.Param("name"), "/")
		if name == "" {
			c.JSON(400, gin.H{"error": "name required"})
			return
		}
		combo, ok := reg.GetCombo(name)
		if !ok {
			c.JSON(404, gin.H{"error": "combo not found", "name": name})
			return
		}
		c.JSON(200, gin.H{"combo": combo})
	}
}

// HandleAddCombo: POST /api/combos { name, strategy, models, description? }.
func HandleAddCombo(reg *proxy.ComboRegistry) gin.HandlerFunc {
	return func(c *gin.Context) {
		if reg == nil {
			c.JSON(500, gin.H{"error": "combo registry not initialised"})
			return
		}
		var in comboInput
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON: " + err.Error()})
			return
		}
		combo := db.Combo{
			Name:            in.Name,
			Strategy:        in.Strategy,
			Models:          in.Models,
			Description:     in.Description,
			CompressionMode: in.CompressionMode,
		}
		if err := reg.AddCombo(combo); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"ok": true, "name": combo.Name})
	}
}

// HandleDeleteCombo: DELETE /api/combos/*name.
func HandleDeleteCombo(reg *proxy.ComboRegistry) gin.HandlerFunc {
	return func(c *gin.Context) {
		if reg == nil {
			c.JSON(500, gin.H{"error": "combo registry not initialised"})
			return
		}
		name := strings.TrimPrefix(c.Param("name"), "/")
		if name == "" {
			c.JSON(400, gin.H{"error": "name required"})
			return
		}
		if err := reg.DeleteCombo(name); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"ok": true, "name": name})
	}
}
