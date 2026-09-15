package version

// These are set at build time via -ldflags.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// UserAgent is the User-Agent every outbound HTTP request carries: provider
// calls, Role Manager classifier turns, nonce fetches, and tool fetches alike.
// One value from one place, so a server sees the same identity and version for
// every call a session makes.
func UserAgent() string {
	return "signet/" + Version + " (+https://github.com/Vulnetix/signet)"
}
