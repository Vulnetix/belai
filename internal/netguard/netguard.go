// Package netguard is the deterministic URL and address policy. It decides
// which URLs the harness may fetch on a model's behalf (the Fetch profile) and
// which base URLs a user may configure for a service that receives keys or
// tool output (the Endpoint profile), and which resolved addresses are never
// dialled. Nothing here asks a model anything.
//
// A URL is refused, never repaired: whitespace, control characters,
// backslashes, credentials, non-canonical numeric hosts (decimal, octal, hex
// or short dotted forms that a resolver may read as an address), escapes that
// decode to control characters at any depth, zone identifiers and internal
// host names all fail, because each is a known way to make a check and a
// connection disagree about the destination.
package netguard

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// Profile selects the rule set applied by [CheckURL].
type Profile int

const (
	// Fetch is for a URL a model supplied: http or https only, and the host must
	// be a public destination.
	Fetch Profile = iota
	// Endpoint is for a base URL the user configured for a service: https, or
	// plain http to a loopback host only. Private addresses are allowed because
	// self-hosted services live there.
	Endpoint
)

// MaxURLBytes bounds a URL.
const MaxURLBytes = 8192

// maxDecodeRounds bounds the percent-decoding fixed point.
const maxDecodeRounds = 4

// ErrURL wraps every rejection so callers can test for it.
var ErrURL = errors.New("url rejected")

func reject(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrURL, fmt.Sprintf(format, args...))
}

// CheckURL validates raw under the profile and returns the parsed URL with an
// ASCII (punycode) host. It performs no DNS lookup; the dialer applies
// [Forbidden] to the addresses a name resolves to.
func CheckURL(raw string, p Profile) (*url.URL, error) {
	if raw == "" || len(raw) > MaxURLBytes {
		return nil, reject("empty or over %d bytes", MaxURLBytes)
	}
	if !utf8.ValidString(raw) {
		return nil, reject("not valid UTF-8")
	}
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; c <= ' ' || c == 0x7f || c == '\\' {
			return nil, reject("contains a space, control character or backslash; percent-encode it")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, reject("unparseable")
	}
	if u.Opaque != "" || u.Host == "" {
		return nil, reject("needs an absolute URL with a host")
	}
	if u.User != nil {
		return nil, reject("must not carry credentials")
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.TrimSuffix(u.Hostname(), ".")
	if host == "" {
		return nil, reject("empty host")
	}
	if port := u.Port(); port != "" && (port == "0" || len(port) > 5) {
		return nil, reject("invalid port")
	}
	host, err = canonicalHost(host)
	if err != nil {
		return nil, err
	}
	switch p {
	case Fetch:
		if scheme != "http" && scheme != "https" {
			return nil, reject("only http and https are allowed")
		}
		if internalName(host) {
			return nil, reject("internal host name")
		}
		if a, ok := parseAddr(host); ok && Forbidden(a) {
			return nil, reject("non-public address")
		}
	case Endpoint:
		switch scheme {
		case "https":
		case "http":
			if !IsLoopbackHost(host) {
				return nil, reject("plain http is allowed only to a loopback host")
			}
		default:
			return nil, reject("must be https (or http on a loopback host)")
		}
	default:
		return nil, reject("unknown profile")
	}
	if err := stableEscapes(u.EscapedPath()); err != nil {
		return nil, err
	}
	if err := stableEscapes(u.RawQuery); err != nil {
		return nil, err
	}
	out := *u
	out.Scheme = scheme
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" {
		host = host + ":" + port
	}
	out.Host = host
	return &out, nil
}

// canonicalHost returns the host in a form a resolver cannot read differently
// from the checker: an IP literal in canonical form, or a lower-case ASCII
// name (IDNA-converted). Numeric-looking names that are not canonical
// addresses are refused.
func canonicalHost(host string) (string, error) {
	if strings.Contains(host, "%") {
		return "", reject("host has an escape or zone identifier")
	}
	if a, ok := parseAddr(host); ok {
		if a.Zone() != "" {
			return "", reject("zone identifiers are not allowed")
		}
		return a.Unmap().String(), nil
	}
	if numericLike(host) {
		return "", reject("non-canonical numeric host")
	}
	ascii := strings.ToLower(host)
	if !isASCII(host) {
		for _, r := range host {
			if unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Cc, r) {
				return "", reject("host contains an invisible or control character")
			}
		}
		var err error
		ascii, err = idna.Lookup.ToASCII(host)
		if err != nil || ascii == "" {
			return "", reject("invalid host name")
		}
		ascii = strings.ToLower(ascii)
	}
	if len(ascii) > 253 {
		return "", reject("host name too long")
	}
	for i := 0; i < len(ascii); i++ {
		c := ascii[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_') {
			return "", reject("invalid host name")
		}
	}
	return ascii, nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func parseAddr(host string) (netip.Addr, bool) {
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a, true
}

// numericLike reports a host made only of labels that are all digits or 0x hex
// numbers ("2130706433", "0x7f.1", "127.1", "0177.0.0.1"): forms a system
// resolver may turn into an address even though the canonical parser did not.
func numericLike(host string) bool {
	labels := strings.Split(host, ".")
	for _, l := range labels {
		if l == "" {
			return false
		}
		if isDigits(l) {
			continue
		}
		if len(l) > 2 && (l[:2] == "0x" || l[:2] == "0X") && isHex(l[2:]) {
			continue
		}
		return false
	}
	return true
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return s != ""
}

// internalNames are suffixes that name a host on the local network.
var internalNames = []string{".localhost", ".local", ".internal", ".localdomain", ".home.arpa", ".lan", ".intranet"}

func internalName(host string) bool {
	if host == "localhost" {
		return true
	}
	for _, s := range internalNames {
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	return !strings.Contains(host, ".") && !strings.Contains(host, ":")
}

// stableEscapes decodes percent escapes to a fixed point and refuses a value
// that is malformed or that decodes, at any depth, to a control character
// (NUL, CR, LF and the rest), which is how double encoding smuggles header and
// line injection past a single-decode check.
func stableEscapes(s string) error {
	cur := s
	for range maxDecodeRounds {
		dec, err := url.PathUnescape(cur)
		if err != nil {
			return reject("malformed percent escape")
		}
		for i := 0; i < len(dec); i++ {
			if c := dec[i]; c < ' ' || c == 0x7f {
				return reject("escape decodes to a control character")
			}
		}
		if dec == cur {
			return nil
		}
		cur = dec
	}
	return reject("escapes nested too deeply")
}

// IsLoopbackHost reports whether host names this machine: localhost (with or
// without a trailing dot, or a subdomain of it) or a loopback IP literal,
// including an IPv4-mapped IPv6 loopback.
func IsLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if a, ok := parseAddr(strings.Trim(host, "[]")); ok {
		return a.Unmap().IsLoopback()
	}
	return false
}

// forbidden lists the ranges a model-directed fetch must never reach. IPv4
// addresses are checked after unmapping; the IPv6 transition ranges that embed
// an IPv4 address (NAT64, 6to4, Teredo) are refused whole rather than parsed.
var forbidden = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
	"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16",
	"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/32",
	"2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "fec0::/10", "ff00::/8",
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// EnvAllowPrivateCIDRs names the environment variable that carries the allowed
// private ranges to every process a host starts: `belai rc --allow-private-cidr`
// sets it, and the sessions and workers it runs inherit it.
const EnvAllowPrivateCIDRs = "BELAI_ALLOW_PRIVATE_CIDRS"

// MaxAllowedPrefixes bounds the allow list.
const MaxAllowedPrefixes = 8

// allowable lists the only ranges an allow-list entry may sit inside: the
// private-use blocks. Loopback, link-local (the cloud metadata address),
// multicast, unspecified and every transition range can never be allowed, so a
// mistyped or hostile value cannot open them.
var allowable = mustPrefixes("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fc00::/7")

// allowed is the process's allow list: private ranges the host named on
// purpose because its own network answers with addresses there (the Pix
// Sandbox's egress gateway, which stands in for every host the container
// reaches). It is empty unless set, and an address in it is not forbidden.
var allowed atomic.Pointer[[]netip.Prefix]

func init() {
	// An invalid value leaves the list empty: the guard fails closed.
	if ps, err := ParseAllowCIDRs(os.Getenv(EnvAllowPrivateCIDRs)); err == nil {
		SetAllowedPrefixes(ps)
	}
}

// ParseAllowCIDRs reads a comma-separated list of CIDRs. Each must be written
// in canonical form (host bits zero) and lie wholly inside a private-use block,
// and there are at most [MaxAllowedPrefixes]. An empty string is an empty list.
func ParseAllowCIDRs(s string) ([]netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var out []netip.Prefix
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		p, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("allow-private-cidr %q: not a CIDR", part)
		}
		if p != p.Masked() {
			return nil, fmt.Errorf("allow-private-cidr %q: host bits are set, write %s", part, p.Masked())
		}
		if p.Addr().Is4In6() {
			return nil, fmt.Errorf("allow-private-cidr %q: write an IPv4 range as IPv4", part)
		}
		inside := false
		for _, a := range allowable {
			if a.Addr().Is4() == p.Addr().Is4() && a.Bits() <= p.Bits() && a.Contains(p.Addr()) {
				inside = true
				break
			}
		}
		if !inside {
			return nil, fmt.Errorf("allow-private-cidr %q: only a private-use range may be allowed (10/8, 172.16/12, 192.168/16, 100.64/10, fc00::/7)", part)
		}
		out = append(out, p)
	}
	if len(out) > MaxAllowedPrefixes {
		return nil, fmt.Errorf("allow-private-cidr: at most %d ranges", MaxAllowedPrefixes)
	}
	return out, nil
}

// SetAllowedPrefixes replaces the process's allow list. Callers validate with
// [ParseAllowCIDRs] first; an entry outside the private-use blocks is dropped
// here as well, so the list can never reach loopback or link-local.
func SetAllowedPrefixes(ps []netip.Prefix) {
	kept := make([]netip.Prefix, 0, len(ps))
	for _, p := range ps {
		for _, a := range allowable {
			if a.Addr().Is4() == p.Addr().Is4() && a.Bits() <= p.Bits() && a.Contains(p.Addr()) {
				kept = append(kept, p.Masked())
				break
			}
		}
	}
	allowed.Store(&kept)
}

// AllowedPrefixes returns the process's allow list.
func AllowedPrefixes() []netip.Prefix {
	if p := allowed.Load(); p != nil {
		return append([]netip.Prefix(nil), *p...)
	}
	return nil
}

// Forbidden reports whether a is not a public unicast address. The zero Addr
// is forbidden. An address inside the process's allow list ([SetAllowedPrefixes])
// is not.
func Forbidden(a netip.Addr) bool {
	if !a.IsValid() {
		return true
	}
	a = a.Unmap().WithZone("")
	if p := allowed.Load(); p != nil {
		for _, r := range *p {
			if r.Contains(a) {
				return false
			}
		}
	}
	for _, p := range forbidden {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ForbiddenIP is [Forbidden] for a net.IP. A nil or malformed IP is forbidden.
func ForbiddenIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	return Forbidden(a)
}
