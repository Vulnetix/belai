package localinfer

import (
	"fmt"
	"net"
)

// PreferredPorts is the probe order for already-running local inference
// servers. It is exposed as a function so this list is the single source of
// truth used by the report, the launch path, and existing-server detection.
func PreferredPorts() []int {
	return []int{11434, 18080, 8000}
}

// FreePort returns an available TCP port on 127.0.0.1. The port is advisory:
// a second process may bind it between the probe socket closing and the
// caller using it. Callers should treat an "address in use" error as a
// signal to retry with a freshly allocated port.
func FreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("free port: %w", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// BaseURL returns the OpenAI-compatible base URL for a local server.
func BaseURL(host string, port int) string {
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d/v1", host, port)
}
