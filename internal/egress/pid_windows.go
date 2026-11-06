//go:build windows

package egress

// pidAlive is conservative on Windows: a socket is never swept, because the
// sandbox does not run there and nothing starts a proxy.
func pidAlive(int) bool { return true }
