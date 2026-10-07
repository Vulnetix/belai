//go:build !belai_sandbox

package clefmcp

// SandboxBuild reports whether this binary is the Pix Sandbox build; see
// flag_sandbox.go. Outside it the server needs Cloudflare Workers AI credentials.
const SandboxBuild = false
