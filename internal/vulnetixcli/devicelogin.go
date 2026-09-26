package vulnetixcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// The RFC 8628 device authorization grant the Vulnetix CLI uses for
// `vulnetix auth login`, served by www.vulnetix.com (cli/cmd/auth.go). Belai
// runs the grant itself so the TUI can show the code, then hands the result
// to the CLI, which stays the only writer of its credential store.
const (
	defaultWebURL      = "https://www.vulnetix.com"
	devicePath         = "/api/site/v1/cli/device"
	devicePollInterval = 5 * time.Second
	devicePollTimeout  = 5 * time.Minute
	deviceSlowDown     = 5 * time.Second
	deviceRequestLimit = 10 * time.Second
	maxDeviceBody      = 64 << 10
)

// ErrDeviceExpired means the code timed out before the user approved it.
var ErrDeviceExpired = errors.New("the login code expired before it was approved")

// ErrDeviceDenied means the user refused the login in the browser.
var ErrDeviceDenied = errors.New("the login was denied in the browser")

// DeviceGrant is a started device login: the code the user confirms and the
// page they confirm it on. DeviceCode is secret and never shown.
type DeviceGrant struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// BrowseURL prefers the code-carrying URL so the user does not have to type.
func (g DeviceGrant) BrowseURL() string {
	if g.VerificationURIComplete != "" {
		return g.VerificationURIComplete
	}
	return g.VerificationURI
}

// DeviceLogin runs the device grant against BaseURL (the Vulnetix console).
type DeviceLogin struct {
	// BaseURL is the console origin. Empty means $VULNETIX_WEB_URL, else
	// https://www.vulnetix.com, as for the CLI.
	BaseURL string
	// Client makes the requests. Nil means a client with a short timeout.
	Client *http.Client
	// SlowDown is added to the poll interval on slow_down. Zero means 5s.
	SlowDown time.Duration
}

func (d DeviceLogin) base() string {
	if d.BaseURL != "" {
		return strings.TrimRight(d.BaseURL, "/")
	}
	if u := strings.TrimSpace(os.Getenv("VULNETIX_WEB_URL")); u != "" {
		return strings.TrimRight(u, "/")
	}
	return defaultWebURL
}

func (d DeviceLogin) post(ctx context.Context, path string, body, out any) (int, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.base()+devicePath+path, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	hc := d.Client
	if hc == nil {
		hc = &http.Client{Timeout: deviceRequestLimit}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxDeviceBody)).Decode(out); err != nil {
		return resp.StatusCode, fmt.Errorf("malformed response from the Vulnetix login service: %w", err)
	}
	return resp.StatusCode, nil
}

// Start begins a grant.
func (d DeviceLogin) Start(ctx context.Context) (DeviceGrant, error) {
	var g DeviceGrant
	status, err := d.post(ctx, "/authorize", struct{}{}, &g)
	if err != nil {
		return DeviceGrant{}, fmt.Errorf("could not start the Vulnetix login: %w", err)
	}
	if status != http.StatusOK {
		return DeviceGrant{}, fmt.Errorf("the Vulnetix login was refused (HTTP %d)", status)
	}
	if g.DeviceCode == "" || g.UserCode == "" || g.VerificationURI == "" {
		return DeviceGrant{}, errors.New("incomplete response from the Vulnetix login service")
	}
	if !httpsURL(g.BrowseURL()) {
		return DeviceGrant{}, errors.New("the Vulnetix login service returned a non-https verification page")
	}
	return g, nil
}

// Poll waits for the user to approve g and returns the org id and the API
// key (the hex half of the server's "orgId:hex"). It honours the server's
// interval, backs off on slow_down, and ends at the grant's expiry or ctx.
func (d DeviceLogin) Poll(ctx context.Context, g DeviceGrant) (orgID, apiKey string, err error) {
	return d.pollWith(ctx, g, devicePollInterval)
}

// pollWith is Poll with the fallback interval as a seam for tests.
func (d DeviceLogin) pollWith(ctx context.Context, g DeviceGrant, fallback time.Duration) (orgID, apiKey string, err error) {
	expiry := devicePollTimeout
	if g.ExpiresIn > 0 {
		expiry = time.Duration(g.ExpiresIn) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, expiry)
	defer cancel()
	interval := fallback
	if g.Interval > 0 {
		interval = time.Duration(g.Interval) * time.Second
	}
	slow := d.SlowDown
	if slow <= 0 {
		slow = deviceSlowDown
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", "", ErrDeviceExpired
			}
			return "", "", ctx.Err()
		case <-timer.C:
		}
		var res struct {
			OrgID string `json:"orgId"`
			Key   string `json:"apiKey"`
			Error string `json:"error"`
		}
		status, err := d.post(ctx, "/token", map[string]string{"device_code": g.DeviceCode}, &res)
		if err == nil && status == http.StatusOK && res.OrgID != "" && res.Key != "" {
			org, key, ok := strings.Cut(res.Key, ":")
			if !ok || org != res.OrgID || key == "" {
				return "", "", errors.New("unexpected API key format from the Vulnetix login service")
			}
			return res.OrgID, key, nil
		}
		switch {
		case err != nil:
			// A transport hiccup or malformed body: keep trying until expiry.
		case res.Error == "authorization_pending":
		case res.Error == "slow_down", status == http.StatusTooManyRequests:
			interval += slow
		case res.Error == "expired_token":
			return "", "", ErrDeviceExpired
		case res.Error == "access_denied":
			return "", "", ErrDeviceDenied
		default:
			return "", "", fmt.Errorf("the Vulnetix login failed (HTTP %d)", status)
		}
		timer.Reset(interval)
	}
}

// SaveLogin hands an org id and API key to `vulnetix auth login
// --noninteractive --store home`, so the CLI writes its own credential file.
// The key travels in the child's environment only, never its argv, where
// other local users could read it from the process list.
func (c CLI) SaveLogin(ctx context.Context, orgID, apiKey string) error {
	env := []string{"VULNETIX_ORG_ID=" + orgID, "VULNETIX_API_KEY=" + apiKey}
	for _, e := range probeEnv() {
		k, _, _ := strings.Cut(e, "=")
		if k != "VULNETIX_ORG_ID" && k != "VULNETIX_API_KEY" && k != "VULNETIX_API_TOKEN" {
			env = append(env, e)
		}
	}
	c.Env = env
	res, err := c.Exec(ctx, "--disable-memory", "auth", "login", "--noninteractive", "--store", "home")
	if err != nil {
		msg := strings.TrimSpace(ansi.Strip(res.Stderr + "\n" + res.Stdout))
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		return fmt.Errorf("vulnetix auth login: %s", strings.Join(strings.Fields(msg), " "))
	}
	return nil
}

// httpsURL reports whether raw is an absolute https URL.
func httpsURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// OpenBrowser opens an https URL in the user's browser with a fixed argv.
// Anything but an absolute https URL is refused.
func OpenBrowser(raw string) error {
	if !httpsURL(raw) {
		return errors.New("refusing to open a non-https URL")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", raw)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", raw)
	default:
		cmd = exec.Command("xdg-open", raw)
	}
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
