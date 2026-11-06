package netguard

import (
	"fmt"
	"strings"
)

// EgressPorts are the only ports the egress proxy will connect to for a
// sandboxed command: https and plain http. Anything else (ssh, databases, a
// mail relay, a service on an odd port) is refused whatever the host.
var EgressPorts = []int{80, 443}

// CheckHostPort validates the target of one proxied connection: a host as
// the client wrote it (a name, an IP literal, an IPv6 literal in brackets or
// not) and a port. It returns the canonical host, which is what the allowlist
// is matched against and what the dialer resolves, so the destination that was
// judged is the destination that is contacted. It performs no DNS lookup; the
// proxy applies [Forbidden] to every address the name resolves to.
//
// It refuses, and never repairs: an empty or over-long host, a port other than
// those in [EgressPorts], an escape or zone identifier, a non-canonical numeric
// host, an internal name, and an address in a forbidden range.
func CheckHostPort(host string, port int) (string, error) {
	if port != EgressPorts[0] && port != EgressPorts[1] {
		return "", reject("only ports 80 and 443 are allowed")
	}
	host = strings.Trim(strings.TrimSuffix(host, "."), "[]")
	if host == "" || len(host) > 253 {
		return "", reject("empty or over-long host")
	}
	for i := 0; i < len(host); i++ {
		if c := host[i]; c <= ' ' || c == 0x7f || c == '\\' || c == '/' || c == '@' {
			return "", reject("host contains a space, control character, slash or at sign")
		}
	}
	h, err := canonicalHost(host)
	if err != nil {
		return "", err
	}
	if a, ok := parseAddr(h); ok {
		if Forbidden(a) {
			return "", reject("non-public address")
		}
		return h, nil
	}
	if internalName(h) {
		return "", reject("internal host name")
	}
	if !wellFormedName(h) {
		return "", reject("malformed host name")
	}
	return h, nil
}

// wellFormedName reports a canonical name made of labels of 1 to 63 bytes that
// neither start nor end with a hyphen.
func wellFormedName(h string) bool {
	for _, l := range strings.Split(h, ".") {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
	}
	return true
}

// CheckAllowEntry validates one egress allowlist entry and returns it in the
// form [MatchHost] expects. An entry is a host name, or `*.` followed by a
// domain with at least two labels. A bare apex is not covered by its
// wildcard: write both to allow both. An IP literal, an internal name, a
// single-label name, a bare `*` and a wildcard over a top-level domain
// (`*.com`) are refused. No public suffix list is consulted, so `*.co.uk` is
// accepted; the user chose it.
func CheckAllowEntry(entry string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(entry))
	if e == "" || len(e) > 253 {
		return "", reject("empty or over-long egress entry")
	}
	wild := strings.HasPrefix(e, "*.")
	if wild {
		e = e[2:]
	}
	if strings.ContainsAny(e, "*:/@ \t") {
		return "", reject("an egress entry is a host name or *.domain, no port, scheme or path")
	}
	e = strings.TrimSuffix(e, ".")
	h, err := canonicalHost(e)
	if err != nil {
		return "", err
	}
	if _, ok := parseAddr(h); ok {
		return "", reject("an egress entry is a name, not an address")
	}
	if wild && !strings.Contains(h, ".") {
		return "", reject("a wildcard needs a domain of at least two labels")
	}
	if internalName(h) {
		return "", reject("internal host name")
	}
	if !wellFormedName(h) {
		return "", reject("malformed host name")
	}
	if wild {
		return "*." + h, nil
	}
	return h, nil
}

// MatchHost reports whether host, already canonical (the result of
// [CheckHostPort]), is covered by entry, already canonical (the result of
// [CheckAllowEntry]). An exact entry matches only that name. A `*.example.com`
// entry matches any name beneath it at any depth and never `example.com`
// itself.
func MatchHost(entry, host string) bool {
	if entry == "" || host == "" {
		return false
	}
	if rest, ok := strings.CutPrefix(entry, "*."); ok {
		return rest != "" && len(host) > len(rest)+1 && strings.HasSuffix(host, "."+rest)
	}
	return entry == host
}

// MaxEgressEntries bounds the egress allowlist.
const MaxEgressEntries = 256

// CheckAllowList validates a list of entries: malformed ones are dropped, the
// rest are returned canonical, de-duplicated, in order, and at most
// [MaxEgressEntries]. The dropped entries come back as errors for notes.
func CheckAllowList(entries []string) (kept []string, dropped []error) {
	seen := map[string]bool{}
	for _, raw := range entries {
		e, err := CheckAllowEntry(raw)
		if err != nil {
			dropped = append(dropped, fmt.Errorf("%q: %w", clip(raw), err))
			continue
		}
		if seen[e] {
			continue
		}
		if len(kept) >= MaxEgressEntries {
			dropped = append(dropped, fmt.Errorf("%q: more than %d egress entries", clip(raw), MaxEgressEntries))
			continue
		}
		seen[e] = true
		kept = append(kept, e)
	}
	return kept, dropped
}

func clip(s string) string {
	if len(s) > 60 {
		return s[:60]
	}
	return s
}
