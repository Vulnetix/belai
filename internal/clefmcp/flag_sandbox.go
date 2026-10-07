//go:build belai_sandbox

package clefmcp

// SandboxBuild reports that this binary is the Pix Sandbox build. It is the one
// place the build tag is read: the rest of the package is always compiled, and
// this flag only decides whether the server is offered without Cloudflare
// credentials of the user's own (the sandbox answers decisions through its Worker).
const SandboxBuild = true
