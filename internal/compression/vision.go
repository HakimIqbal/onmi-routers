package compression

import "strings"

// modelSupportsVision reports whether a model id is a known vision model.
// Conservative list mirroring OmniRoute's visionModels carve-outs; anything
// unrecognized is treated as NON-vision so image placeholders may apply. When
// opts.Model is empty we assume vision-capable (safe default: never strip).
func modelSupportsVision(model string) bool {
	if model == "" {
		return true
	}
	m := strings.ToLower(model)
	for _, frag := range visionFragments {
		if strings.Contains(m, frag) {
			return true
		}
	}
	return false
}

var visionFragments = []string{
	"vision", "gpt-4o", "gpt-4-turbo", "gpt-4-vision", "claude-3", "claude-sonnet",
	"claude-opus", "claude-haiku", "gemini", "pixtral", "llava", "qwen-vl", "qwen2-vl",
	"glm-4v", "kimi-vl", "mistral-medium-3", "minimax-m3", "image", "vl", "4o",
}
