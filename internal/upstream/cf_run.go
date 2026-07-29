package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CFRun executes a non-stream Workers AI `ai/run/{model}` call with the next
// healthy CF account. Used by multi-modal (embeddings/image) paths.
// Returns HTTP status, raw response body, account id, error.
func CFRun(km *CFKeyManager, model string, payload any) (int, []byte, string, error) {
	if km == nil {
		return 0, nil, "", fmt.Errorf("no cf key manager")
	}
	model = strings.TrimPrefix(model, "cf/")
	if !strings.HasPrefix(model, "@cf/") && !strings.HasPrefix(model, "@hf/") {
		// allow bare names that already include vendor
		if !strings.Contains(model, "/") {
			model = "@cf/" + model
		} else if !strings.HasPrefix(model, "@") {
			model = "@cf/" + strings.TrimPrefix(model, "cf/")
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		key, err := km.Next()
		if err != nil {
			return 0, nil, "", err
		}
		url := fmt.Sprintf("%s/accounts/%s/ai/run/%s", CF_UPSTREAM_URL, key.AccountID, model)
		req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		req.Header.Set("Authorization", key.AuthHeader())
		req.Header.Set("Content-Type", "application/json")
		client, _ := getClient(upstreamClient, "cloudflare")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			permanentDisableCF(key, fmt.Sprintf("%d %s", resp.StatusCode, truncateLog(string(raw), 120)))
			lastErr = fmt.Errorf("cf auth %d", resp.StatusCode)
			continue
		}
		if resp.StatusCode == 429 {
			cooldownDisableCF(key, "429 multimodal")
			lastErr = fmt.Errorf("cf 429")
			continue
		}
		return resp.StatusCode, raw, key.AccountID, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("cf run failed")
	}
	return 0, nil, "", lastErr
}

// CFEmbed runs a BGE-style embedding model and returns OpenAI-compatible data.
func CFEmbed(km *CFKeyManager, model string, texts []string) (map[string]any, error) {
	if model == "" {
		model = "@cf/baai/bge-base-en-v1.5"
	}
	// Workers AI BGE accepts {"text": "..." } or {"text": ["..."]}
	var payload any
	if len(texts) == 1 {
		payload = map[string]any{"text": texts[0]}
	} else {
		payload = map[string]any{"text": texts}
	}
	status, raw, _, err := CFRun(km, model, payload)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("cf embed status %d: %s", status, truncateLog(string(raw), 200))
	}
	// Response shapes vary: {result: {data: [[...]]}} or {result: {shape, data}}
	var wrap struct {
		Result json.RawMessage `json:"result"`
		Success bool           `json:"success"`
		Errors  []any          `json:"errors"`
	}
	_ = json.Unmarshal(raw, &wrap)
	src := raw
	if len(wrap.Result) > 0 {
		src = wrap.Result
	}
	// Try common shapes
	var asMap map[string]any
	_ = json.Unmarshal(src, &asMap)
	var vectors [][]float64
	if asMap != nil {
		if data, ok := asMap["data"].([]any); ok {
			// data: [[floats]] or [{embedding:[]}]
			for _, d := range data {
				switch v := d.(type) {
				case []any:
					vectors = append(vectors, toFloats(v))
				case map[string]any:
					if emb, ok := v["embedding"].([]any); ok {
						vectors = append(vectors, toFloats(emb))
					}
				}
			}
		}
	}
	// single vector at top-level "data": [floats]
	if len(vectors) == 0 && asMap != nil {
		if data, ok := asMap["data"].([]any); ok && len(data) > 0 {
			if _, isNum := data[0].(float64); isNum {
				vectors = append(vectors, toFloats(data))
			}
		}
	}
	if len(vectors) == 0 {
		// last resort: try result as raw array of floats
		var one []float64
		if json.Unmarshal(src, &one) == nil && len(one) > 0 {
			vectors = append(vectors, one)
		}
	}
	if len(vectors) == 0 {
		return nil, fmt.Errorf("cf embed: could not parse vectors from %s", truncateLog(string(raw), 180))
	}
	data := make([]map[string]any, 0, len(vectors))
	for i, v := range vectors {
		data = append(data, map[string]any{
			"object":    "embedding",
			"index":     i,
			"embedding": v,
		})
	}
	return map[string]any{
		"object": "list",
		"data":   data,
		"model":  model,
		"usage":  map[string]any{"prompt_tokens": 0, "total_tokens": 0},
	}, nil
}

func toFloats(in []any) []float64 {
	out := make([]float64, 0, len(in))
	for _, v := range in {
		switch t := v.(type) {
		case float64:
			out = append(out, t)
		case json.Number:
			f, _ := t.Float64()
			out = append(out, f)
		}
	}
	return out
}
