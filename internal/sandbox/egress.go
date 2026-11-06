package sandbox

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/vulnetix/belai/internal/egress"
)

// planEgress starts (or reuses) the egress proxy for p's allowlist and returns
// a copy of p that knows the socket, the loopback port the in-sandbox relay will
// listen on and the helper executable. It fails closed: any missing piece is an
// error, and Wrap decides between refusing (required) and network deny (auto).
//
// A backend that cannot confine the command to the one port cannot honour
// proxy: Landlock before ABI 4 has no way to restrict TCP, so it is unavailable
// there rather than a proxy a command can walk around.
func planEgress(p Policy, st backendState) (Policy, error) {
	switch st.name {
	case "bwrap", "sandbox-exec":
	case "landlock":
		if st.abi < landlockNetABI {
			return p, fmt.Errorf("%w (Landlock ABI %d cannot restrict TCP, it needs ABI %d, Linux 6.7+)", ErrEgressUnavailable, st.abi, landlockNetABI)
		}
	default:
		return p, ErrEgressUnavailable
	}
	exe := st.path
	if st.name != "landlock" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return p, fmt.Errorf("%w: %v", ErrEgressUnavailable, err)
		}
	}
	sock, err := egress.Ensure(p.EgressAllow)
	if err != nil {
		return p, fmt.Errorf("%w: %v", ErrEgressUnavailable, err)
	}
	port, err := freePort()
	if err != nil {
		return p, fmt.Errorf("%w: %v", ErrEgressUnavailable, err)
	}
	p.egressSock, p.egressPort, p.egressExe = sock, port, exe
	return p, nil
}

// freePort asks the kernel for an unused loopback port. Under bubblewrap the
// relay listens inside a fresh network namespace, where any port is free; under
// Landlock and Seatbelt the host's namespace is shared and the helper fails
// closed, running nothing, if the port was taken in the meantime.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// egressPrefix is the helper invocation that precedes the real command:
// `__egress-exec -sock S -port N --`.
func egressPrefix(p Policy) []string {
	return []string{egress.Command, "-sock", p.egressSock, "-port", strconv.Itoa(p.egressPort), "--"}
}

// proxyEnv points the usual clients at the relay. NO_PROXY is emptied so a
// host-set exclusion cannot send a client around it (it would only find the
// network closed).
func proxyEnv(port int) []string {
	u := "http://127.0.0.1:" + strconv.Itoa(port)
	var out []string
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		out = append(out, k+"="+u)
	}
	return append(out, "NO_PROXY=", "no_proxy=")
}
