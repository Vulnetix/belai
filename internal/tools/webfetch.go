package tools

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

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
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return Result{}, fmt.Errorf("private IP rejected")
		}
	} else {
		addrs, err := net.LookupIP(host)
		if err != nil {
			return Result{}, fmt.Errorf("dns lookup failed: %w", err)
		}
		for _, ip := range addrs {
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				return Result{}, fmt.Errorf("private IP rejected")
			}
		}
	}

	client := w.Client
	if client == nil {
		client = http.DefaultClient
	}
	if client.CheckRedirect == nil {
		// Copy the resolved client (w.Client or http.DefaultClient) before
		// installing the redirect guard. Copying a nil w.Client here would
		// panic; the base must be the client we actually resolved above.
		base := *client
		client = &base
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return checkHost(req.URL)
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

func checkHost(u *url.URL) error {
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return fmt.Errorf("redirect to private IP rejected")
		}
	}
	return nil
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
