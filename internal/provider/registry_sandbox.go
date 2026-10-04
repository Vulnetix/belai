//go:build belai_sandbox

package provider

import "github.com/vulnetix/belai/internal/wire"

// The Pix Sandbox build adds a "builtin" provider: the two agent models a
// sandbox ships with, labelled Pix Smart and Pix Fast. It speaks the Workers
// AI /ai/run/{model} surface to the sandbox gateway, which maps each label to
// the model it serves, so the ids here are the labels and nothing else. The
// credentials are the stock Workers AI ones (the gateway swaps in its own).
// The default build never compiles this file.
func init() {
	registry["builtin"] = Descriptor{
		Name:           "builtin",
		Auth:           AuthBearer,
		Fields:         registry["cloudflare-workers-ai"].Fields,
		BaseURLBuilder: buildCloudflareWorkersAI,
		NetrcHost:      "api.cloudflare.com",
		Surface:        wire.SurfaceWorkersAI,
		ToolMethod:     wire.ToolMethodObject,
		Usage:          true,
		DefaultModel:   "pix-smart",
		FastModel:      "pix-fast",
		Models: []ModelSpec{
			{ID: "pix-smart", Label: "Pix Smart"},
			{ID: "pix-fast", Label: "Pix Fast"},
		},
		ListPath: "/ai/models/search",
	}
}
