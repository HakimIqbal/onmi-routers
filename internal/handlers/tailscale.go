// Package handlers — Tailscale status for Endpoint page (read-only).
package handlers

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// HandleTailscaleStatus: GET /api/tailscale/status
// Returns installed/running/IPs/MagicDNS if tailscale CLI is present.
// Never errors hard — always 200 with ok=false when unavailable.
func HandleTailscaleStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		out := gin.H{
			"ok":         false,
			"installed":  false,
			"running":    false,
			"backend":    "",
			"dns_name":   "",
			"ips":        []string{},
			"magic_dns":  "",
			"https_url":  "",
			"http_url":   "",
			"note":       "",
		}

		path, err := exec.LookPath("tailscale")
		if err != nil {
			out["note"] = "tailscale CLI not installed on this host"
			c.JSON(http.StatusOK, out)
			return
		}
		out["installed"] = true
		out["path"] = path

		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, "status", "--json")
		cmd.Env = os.Environ()
		b, err := cmd.Output()
		if err != nil {
			// fallback: try ip only
			ip := tailscaleIP(path)
			if ip != "" {
				out["ok"] = true
				out["running"] = true
				out["ips"] = []string{ip}
				out["http_url"] = "http://" + ip + ":20130/v1"
				out["note"] = "status --json failed; using tailscale ip"
			} else {
				out["note"] = "tailscale installed but status failed: " + err.Error()
			}
			c.JSON(http.StatusOK, out)
			return
		}

		var st map[string]any
		if err := json.Unmarshal(b, &st); err != nil {
			out["note"] = "invalid tailscale status json"
			c.JSON(http.StatusOK, out)
			return
		}

		backend, _ := st["BackendState"].(string)
		out["backend"] = backend
		out["running"] = strings.EqualFold(backend, "Running")
		if md, ok := st["MagicDNSSuffix"].(string); ok {
			out["magic_dns"] = strings.TrimSuffix(md, ".")
		}

		self, _ := st["Self"].(map[string]any)
		if self != nil {
			if dn, ok := self["DNSName"].(string); ok {
				dn = strings.TrimSuffix(dn, ".")
				out["dns_name"] = dn
				if dn != "" {
					// Prefer https MagicDNS hostname when present.
					out["https_url"] = "https://" + dn + "/v1"
					out["http_url"] = "http://" + dn + ":20130/v1"
				}
			}
			if ips, ok := self["TailscaleIPs"].([]any); ok {
				list := make([]string, 0, len(ips))
				for _, v := range ips {
					if s, ok := v.(string); ok && s != "" {
						list = append(list, s)
					}
				}
				out["ips"] = list
				if out["http_url"] == "" || out["http_url"] == nil {
					for _, s := range list {
						if ip := net.ParseIP(s); ip != nil && ip.To4() != nil {
							out["http_url"] = "http://" + s + ":20130/v1"
							break
						}
					}
				}
			}
		}

		if out["running"].(bool) {
			out["ok"] = true
			out["note"] = "tailscale up · use MagicDNS / 100.x URL on tailnet only"
		} else {
			out["note"] = "tailscale not Running (backend=" + backend + ")"
		}
		c.JSON(http.StatusOK, out)
	}
}

func tailscaleIP(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "ip", "-4")
	b, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
