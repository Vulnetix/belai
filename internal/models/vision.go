package models

import (
	"regexp"
	"strings"
)

// visionID matches the model ids known to accept image input. The rule is by
// id, provider prefixes ("anthropic/claude-…", "openai/gpt-…") and date
// suffixes tolerated, so a relay that serves the same model gets the same
// answer. It is deliberately a list of what is known to work: an id it does
// not name is text-only, and the harness then keeps the image on disk and
// tells the model so, rather than sending bytes a model may reject.
var visionID = regexp.MustCompile(`(^|/)(` +
	`claude-(opus|sonnet|haiku|fable)-\d|claude-3|` +
	`gpt-4o|gpt-4\.1|gpt-4-turbo|gpt-5|o3|o4-mini|o1(-pro)?$|` +
	`gemini-|` +
	`grok-(2|3|4)-vision|grok-4|` +
	`pixtral|llama-3\.2-\d+b-vision|llama-4|` +
	`qwen[\d.]*-vl|kimi-vl|mistral-(medium|large)-3` +
	`)`)

// Vision reports whether the model is known to accept image input. Unknown
// ids answer false.
func Vision(providerName, modelID string) bool {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return false
	}
	return visionID.MatchString(id)
}
