package tools

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/vulnetix/signet/internal/httpclient"
	"github.com/vulnetix/signet/internal/version"
)

// WebFetch is the web-fetch tool with SSRF protection.
type WebFetch struct {
	Client *http.Client
}

// Definition returns the static tool metadata.
func (w *WebFetch) Definition() Definition {
	return Definition{
		Name:        "WebFetch",
		Description: "Fetch a web page by URL and return its text content.",
		Properties: map[string]Property{
			"url": {Type: "string", Description: "HTTP or HTTPS URL to fetch"},
		},
		Required: []string{"url"},
	}
}

// Kind returns the tool kind.
func (w *WebFetch) Kind() Kind { return KindWebFetch }

// Subject returns the permission-rule subject (the URL).
func (w *WebFetch) Subject(args map[string]any) string {
	if s, ok := args["url"].(string); ok {
		return s
	}
	return ""
}

// Execute fetches the URL, enforces SSRF guards, and returns text content.
func (w *WebFetch) Execute(ctx context.Context, args map[string]any) (Result, error) {
	raw, ok := args["url"].(string)
	if !ok || raw == "" {
		return Result{}, fmt.Errorf("missing url argument")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return Result{}, fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Result{}, fmt.Errorf("only http/https allowed")
	}

	host := u.Hostname()

	// The default client validates and pins every connection in its
	// DialContext, so a hostname is resolved exactly once (no second lookup in
	// client.Do) and the dial cannot race a DNS rebinding swap after
	// validation. A caller-supplied client keeps the pre-dial validation below
	// because its transport cannot be pinned here.
	client := w.Client
	if client == nil {
		client = newSSRFClient()
	} else if err := validateHost(host); err != nil {
		return Result{}, err
	}
	if client.CheckRedirect == nil {
		// Copy the resolved client before installing the redirect guard. The
		// guard only limits the redirect chain: each redirect dials through
		// the same validating DialContext, so a redirect to a private address
		// is rejected at connect time, not by a second DNS lookup here.
		base := *client
		client = &base
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to non-http scheme rejected")
			}
			return nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("user-agent", version.UserAgent())

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("content-type")
	if !allowedContentType(ct) {
		return Result{}, fmt.Errorf("content-type %q not allowed", ct)
	}

	const maxSize = 1 << 20 // 1 MiB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSize))
	if err != nil {
		return Result{}, err
	}

	if strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml") {
		return WebFetchResult(htmlText(string(body))), nil
	}
	return WebFetchResult(string(body)), nil
}

// newSSRFClient builds the default WebFetch client: a copy of the shared tuned
// transport whose DialContext resolves, validates and pins every connection
// address. This is the SSRF guard — no host is ever dialled without its
// resolved addresses passing the forbidden-address check, and the dial uses
// those exact addresses rather than re-resolving (closing the TOCTOU gap).
func newSSRFClient() *http.Client {
	transport := httpclient.Transport()
	dialer := &net.Dialer{Timeout: httpclient.DialTimeout, KeepAlive: httpclient.KeepAlive}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		var ips []net.IP
		if ip := net.ParseIP(host); ip != nil {
			ips = []net.IP{ip}
		} else {
			ips, err = net.LookupIP(host)
			if err != nil {
				return nil, err
			}
		}
		for _, ip := range ips {
			if forbiddenIP(ip) {
				return nil, fmt.Errorf("private IP %s rejected", ip)
			}
		}
		var lastErr error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("no addresses for %s", host)
		}
		return nil, lastErr
	}
	return &http.Client{Transport: transport}
}

// validateHost rejects a host that resolves to a forbidden address. It is the
// pre-dial SSRF guard for a caller-supplied client whose transport cannot be
// pinned.
func validateHost(host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if forbiddenIP(ip) {
			return fmt.Errorf("private IP rejected")
		}
		return nil
	}
	addrs, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("dns lookup failed: %w", err)
	}
	for _, ip := range addrs {
		if forbiddenIP(ip) {
			return fmt.Errorf("private IP rejected")
		}
	}
	return nil
}

// forbiddenIP reports whether an address must never be fetched.
func forbiddenIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

func allowedContentType(ct string) bool {
	ct = strings.ToLower(ct)
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	if strings.Contains(ct, "application/json") || strings.Contains(ct, "application/xhtml") {
		return true
	}
	return false
}

// htmlText is a minimal best-effort HTML-to-text extractor.
func htmlText(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				b.WriteRune(r)
			}
		}
	}
	return strings.TrimSpace(b.String())
}
