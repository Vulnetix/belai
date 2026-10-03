package decisions

import "strings"

// The Cloudflare providers that can serve Clef, Cloudflare's decision models,
// through Workers AI. They are chat providers too; a Clef model on either is a
// decision backend and never a chat model.
const (
	CloudflareWorkersAIProvider = "cloudflare-workers-ai"
	CloudflareGatewayProvider   = "cloudflare-ai-gateway"
)

// ClefModel is one Clef model on Workers AI.
type ClefModel struct {
	// ID is the Workers AI model id, the settings value of classifier.model.
	ID string
	// Wire is the "model" value the API requires in the request body.
	Wire  string
	Label string
	Blurb string
}

// ClefMaxOptions and ClefMaxQuestions are Clef's request limits: llama.cpp
// reads up to 255 options of a choice, and Workers AI takes 64 questions.
const (
	ClefMaxOptions   = 255
	ClefMaxQuestions = 64
)

// ClefModels is the Workers AI Clef catalogue, the fast model first.
var ClefModels = []ClefModel{
	{ID: "@cf/cloudflare/clef-flash", Wire: "clef-flash", Label: "Clef-flash 9B", Blurb: "Workers AI · tens of milliseconds a check · $0.24/M input tokens"},
	{ID: "@cf/cloudflare/clef", Wire: "clef", Label: "Clef 27B", Blurb: "Workers AI · the most accurate Clef · $0.24/M input tokens"},
}

// ClefByID returns the catalogue entry for a Workers AI model id.
func ClefByID(id string) (ClefModel, bool) {
	for _, m := range ClefModels {
		if m.ID == id {
			return m, true
		}
	}
	return ClefModel{}, false
}

// IsCloudflareProvider reports whether provider is one of the Cloudflare
// providers that serve Workers AI.
func IsCloudflareProvider(provider string) bool {
	return provider == CloudflareWorkersAIProvider || provider == CloudflareGatewayProvider
}

// IsHostedClef reports whether a provider/model pair is Clef on Workers AI,
// directly or through AI Gateway.
func IsHostedClef(provider, model string) bool {
	_, ok := ClefByID(strings.TrimSpace(model))
	return ok && IsCloudflareProvider(provider)
}
