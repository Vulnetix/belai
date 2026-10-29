package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/vaultenv"
)

// CF is the read-only Cloudflare API tool. There is no Cloudflare CLI with a
// read-only surface to put behind a prefix list, so the tool makes the request
// itself: GET only, to https://api.cloudflare.com/client/v4/ only, with the
// bearer token from the vault variable CLOUDFLARE_API_TOKEN (or the process
// environment on a host without a vault). proc.ScrubbedEnv drops CLOUDFLARE_*
// from every subprocess, so no command the model runs holds the token, and the
// tool never puts it in a result: the response is redacted and scrubbed before
// the model reads it.
//
// The output is zone settings, DNS records, firewall events and Radar data,
// much of it written by third parties, so the kind is KindRemote: read-only,
// and classified before promotion.
type Cloudflare struct {
	// MaxBytes caps the response body; zero means 64 KiB.
	MaxBytes int
	// Timeout bounds one request; zero means 30 seconds.
	Timeout time.Duration
	// Token returns the bearer token; nil means cloudflareToken.
	Token func() (string, bool)
	// Transport carries the request; nil means http.DefaultTransport. Tests
	// set it to answer without the network. The URL is fixed either way.
	Transport http.RoundTripper
}

const (
	// CFName is the tool's name.
	CFName = "CF"
	// CloudflareTokenEnv names the vault variable that holds the bearer.
	CloudflareTokenEnv = "CLOUDFLARE_API_TOKEN"
	// cloudflareHost is the one host the tool talks to.
	cloudflareHost = "api.cloudflare.com"
	// cloudflareBase is the prefix of every request URL.
	cloudflareBase = "https://" + cloudflareHost + "/client/v4/"
	// CloudflareProbePath is the availability probe. /user/tokens/verify is
	// never used: it answers 401 to a zone-scoped token that works everywhere
	// else, so it would hide the tool from the tokens Guardian is given.
	CloudflareProbePath = "zones?per_page=1"
	// cloudflareDefaultMax is the response cap, the native tools' cap.
	cloudflareDefaultMax = 64 * 1024
)

// cloudflareToken reads the bearer from the vault's live variables first and
// the process environment second.
func cloudflareToken() (string, bool) {
	if v, ok := vaultenv.Default.Lookup(CloudflareTokenEnv, time.Now()); ok && v != "" {
		return v, true
	}
	v := strings.TrimSpace(os.Getenv(CloudflareTokenEnv))
	return v, v != ""
}

// Definition returns the tool metadata.
func (c *Cloudflare) Definition() Definition {
	return Definition{
		Name: CFName,
		Description: "Query the Cloudflare API (read-only, GET only). Pass the path after https://api.cloudflare.com/client/v4/, " +
			"for example `zones?per_page=50`, `zones/ZONE_ID/dns_records?type=A`, `zones/ZONE_ID/settings`, `accounts/ACCOUNT_ID/rulesets` or `radar/entities/asns/13335`. " +
			"The harness adds the bearer token; you never see it. Any method but GET, any other host and any path with `..` are refused. " +
			"The response is the API's JSON, capped at 64 KiB: page with per_page and page rather than asking for everything.",
		Properties: map[string]Property{
			"path":   stringProp("The API path and query after /client/v4/, e.g. \"zones/ZONE_ID/dns_records?per_page=100\""),
			"method": stringProp("Optional. Only GET is accepted."),
		},
		Required: []string{"path"},
	}
}

// Kind is KindRemote: read-only, and classified before promotion.
func (c *Cloudflare) Kind() Kind { return KindRemote }

// Subject is the path, for permission rules such as CF(zones/*).
func (c *Cloudflare) Subject(args map[string]any) string {
	s, _ := argString(args, "path")
	return strings.TrimSpace(s)
}

// cloudflareURL turns the model's path into the one URL the tool may request.
// It accepts a path relative to /client/v4/ (with or without a leading slash
// or the client/v4/ prefix) or a full https://api.cloudflare.com/client/v4/
// URL, and refuses everything else rather than repairing it.
func cloudflareURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("missing path argument")
	}
	for _, r := range raw {
		if r < 0x21 || r == 0x7f || r == '\\' || r == '#' {
			return "", fmt.Errorf("path contains a space, control character, backslash or fragment")
		}
	}
	rel := raw
	if strings.Contains(raw, "://") || strings.HasPrefix(raw, "//") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("path is not a URL: %v", err)
		}
		if u.Scheme != "https" || u.User != nil || u.Host != cloudflareHost {
			return "", fmt.Errorf("only https://%s is allowed, got %q", cloudflareHost, u.Scheme+"://"+u.Host)
		}
		p, ok := strings.CutPrefix(u.EscapedPath(), "/client/v4/")
		if !ok {
			return "", fmt.Errorf("only paths under /client/v4/ are allowed")
		}
		rel = p
		if u.RawQuery != "" {
			rel += "?" + u.RawQuery
		}
	}
	rel = strings.TrimPrefix(rel, "/")
	rel = strings.TrimPrefix(rel, "client/v4/")
	if rel == "" || rel[0] == '?' || rel[0] == '/' {
		return "", fmt.Errorf("path must name an API resource, e.g. zones")
	}
	// A dot-dot segment in any spelling (raw, percent-encoded, encoded twice)
	// is refused, so the request cannot leave /client/v4/.
	dec := rel
	for i := 0; i < 4; i++ {
		if strings.Contains(dec, "..") {
			return "", fmt.Errorf("path must not contain ..")
		}
		next, err := url.PathUnescape(dec)
		if err != nil {
			return "", fmt.Errorf("path has a malformed escape")
		}
		if next == dec {
			break
		}
		dec = next
	}
	if strings.Contains(dec, "..") {
		return "", fmt.Errorf("path must not contain ..")
	}
	full := cloudflareBase + rel
	u, err := url.Parse(full)
	if err != nil || u.Scheme != "https" || u.Host != cloudflareHost || u.User != nil || !strings.HasPrefix(u.EscapedPath(), "/client/v4/") {
		return "", fmt.Errorf("path does not stay on https://%s/client/v4/", cloudflareHost)
	}
	return full, nil
}

// checkMethod refuses any method but GET.
func checkMethod(args map[string]any) error {
	v, ok := args["method"]
	if !ok || v == nil {
		return nil
	}
	s, isString := v.(string)
	if !isString {
		return fmt.Errorf("method must be a string")
	}
	if m := strings.TrimSpace(s); m != "" && !strings.EqualFold(m, http.MethodGet) {
		return fmt.Errorf("only GET is allowed for the Cloudflare API, got %q", m)
	}
	return nil
}

// client is the HTTP client for one call. Redirects are not followed: the
// bearer must never be carried to whatever a Location header names.
func (c *Cloudflare) client(timeout time.Duration) *http.Client {
	tr := c.Transport
	if tr == nil {
		tr = http.DefaultTransport
	}
	return &http.Client{
		Transport:     tr,
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (c *Cloudflare) token() (string, bool) {
	if c.Token != nil {
		return c.Token()
	}
	return cloudflareToken()
}

// get makes one GET and returns the status and up to max bytes of the body,
// with whether it was cut.
func (c *Cloudflare) get(ctx context.Context, full, token string, max int, timeout time.Duration) (int, []byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return 0, nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "belai-cf-tool")
	resp, err := c.client(timeout).Do(req)
	if err != nil {
		return 0, nil, false, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(max)+1))
	if err != nil {
		return resp.StatusCode, nil, false, err
	}
	cut := len(body) > max
	if cut {
		body = body[:max]
	}
	return resp.StatusCode, body, cut, nil
}

// Execute validates the path and method, makes the request and returns the
// status and body.
func (c *Cloudflare) Execute(ctx context.Context, args map[string]any) (Result, error) {
	if err := checkMethod(args); err != nil {
		return Result{}, err
	}
	raw, _ := argString(args, "path")
	full, err := cloudflareURL(raw)
	if err != nil {
		return Result{}, err
	}
	token, ok := c.token()
	if !ok {
		return Result{}, fmt.Errorf("no Cloudflare token: %s is not set for this session", CloudflareTokenEnv)
	}
	max := c.MaxBytes
	if max <= 0 {
		max = cloudflareDefaultMax
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	status, body, cut, err := c.get(ctx, full, token, max, timeout)
	if err != nil {
		// The error names the URL and the transport's reason; the token is
		// in a header, never in either, but redact it anyway.
		return Result{}, fmt.Errorf("%s", redact(fmt.Sprintf("GET %s: %v", full, err), []string{token}))
	}
	content := fmt.Sprintf("GET %s\nHTTP %d\n%s", full, status, body)
	if cut {
		content += fmt.Sprintf("\n… response cut at %d bytes; narrow it with per_page, page or a filter", max)
	}
	content = vaultenv.Default.Scrub(redact(content, []string{token}))
	return Result{Kind: KindRemote, Content: content}, nil
}

// cloudflareProbe reports whether token can read the API: GET
// zones?per_page=1 answers 200 with success true.
func cloudflareProbe(ctx context.Context, c *Cloudflare, token string) bool {
	status, body, _, err := c.get(ctx, cloudflareBase+CloudflareProbePath, token, cloudflareDefaultMax, detectTimeout)
	if err != nil || status != http.StatusOK {
		return false
	}
	var out struct {
		Success bool `json:"success"`
	}
	return json.Unmarshal(body, &out) == nil && out.Success
}

// cfDetect memoises the probe verdict for one token, by its SHA-256, so a
// session that starts after another reuses it and a new token (a renewed vault
// lease that changed it) is probed again. Nothing is written to disk.
var cfDetect struct {
	sync.Mutex
	key string
	ok  bool
	at  time.Time
}

// detectCloudflare is the real CF availability check: a token is held and the
// probe passes. It is run on every DetectDefault rather than memoised with the
// CLI verdicts, because the vault lease can arrive after the first session in
// a process was built.
func detectCloudflare(ctx context.Context) bool {
	token, ok := cloudflareToken()
	if !ok {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(sum[:])
	cfDetect.Lock()
	defer cfDetect.Unlock()
	if cfDetect.key == key && time.Since(cfDetect.at) < probeCacheTTL {
		return cfDetect.ok
	}
	pctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	v := cloudflareProbe(pctx, &Cloudflare{}, token)
	if pctx.Err() == nil {
		// A probe cut short by the deadline is not remembered, so a slow
		// network is retried next time rather than read as no access.
		cfDetect.key, cfDetect.ok, cfDetect.at = key, v, time.Now()
	}
	return v
}
