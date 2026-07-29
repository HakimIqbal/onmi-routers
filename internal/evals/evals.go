// Package evals — lightweight gateway self-evaluation suite.
package evals

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Case struct {
	Name  string `json:"name"`
	Model string `json:"model"`
	Prompt string `json:"prompt"`
}

type Result struct {
	Name       string  `json:"name"`
	Model      string  `json:"model"`
	OK         bool    `json:"ok"`
	Status     int     `json:"status"`
	LatencyMs  int64   `json:"latency_ms"`
	TokensOut  int     `json:"tokens_out,omitempty"`
	Preview    string  `json:"preview,omitempty"`
	Error      string  `json:"error,omitempty"`
}

type Report struct {
	StartedAt string   `json:"started_at"`
	DurationMs int64   `json:"duration_ms"`
	Passed    int      `json:"passed"`
	Failed    int      `json:"failed"`
	Results   []Result `json:"results"`
}

// DefaultCases — cheap CF-first probes (fast models, short timeouts).
func DefaultCases() []Case {
	return []Case{
		{Name: "cf-tiny-ping", Model: "@cf/meta/llama-3.2-1b-instruct", Prompt: "Reply with exactly: pong"},
		{Name: "cf-3b-math", Model: "@cf/meta/llama-3.2-3b-instruct", Prompt: "What is 2+2? Answer with one number only."},
		{Name: "combo-cheap", Model: "combo/cheap", Prompt: "Say hi in one word."},
		{Name: "combo-cf-free", Model: "combo/cf-free", Prompt: "Reply OK"},
	}
}

// RunAgainst hits local gateway /v1/chat/completions with a bearer key.
func RunAgainst(baseURL, apiKey string, cases []Case) Report {
	if len(cases) == 0 {
		cases = DefaultCases()
	}
	start := time.Now()
	rep := Report{StartedAt: start.UTC().Format(time.RFC3339)}
	client := &http.Client{Timeout: 60 * time.Second}
	for _, tc := range cases {
		r := Result{Name: tc.Name, Model: tc.Model}
		body, _ := json.Marshal(map[string]any{
			"model":      tc.Model,
			"messages":   []map[string]string{{"role": "user", "content": tc.Prompt}},
			"max_tokens": 32,
			"stream":     false,
		})
		t0 := time.Now()
		req, _ := http.NewRequest("POST", strings.TrimRight(baseURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		r.LatencyMs = time.Since(t0).Milliseconds()
		if err != nil {
			r.Error = err.Error()
			rep.Failed++
			rep.Results = append(rep.Results, r)
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		r.Status = resp.StatusCode
		r.OK = resp.StatusCode == 200
		if r.OK {
			var parsed struct {
				Choices []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
				Usage struct {
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
			}
			_ = json.Unmarshal(raw, &parsed)
			if len(parsed.Choices) > 0 {
				r.Preview = trim(parsed.Choices[0].Message.Content, 120)
			}
			r.TokensOut = parsed.Usage.CompletionTokens
			rep.Passed++
		} else {
			r.Error = trim(string(raw), 200)
			rep.Failed++
		}
		rep.Results = append(rep.Results, r)
	}
	rep.DurationMs = time.Since(start).Milliseconds()
	return rep
}

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func FormatSummary(r Report) string {
	return fmt.Sprintf("evals passed=%d failed=%d duration_ms=%d", r.Passed, r.Failed, r.DurationMs)
}
