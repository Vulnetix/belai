// Package nonce implements the CSPRNG-seeded nonce pool used to seal harness
// delimiters. Nonces are minted with crypto/rand, reserved when attached to a
// block, released when the block is retired, and rotated to invalidate the
// whole pool. When a firewall or provider implements the nonce GET spec
// (docs/nonce-endpoint-spec.md) the harness seeds the pool from it, trying
// the firewall first, then the provider, then local generation. A pool seeded
// remotely stays remote: it refills from the same endpoint and only falls
// back to local minting (for the rest of the session) when a refill fails.
package nonce

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/httpclient"
	"github.com/vulnetix/belai/internal/version"
)

// ErrUnsupported is returned when an endpoint does not implement the nonce
// GET (or has it disabled), signalling callers to try the next one.
var ErrUnsupported = errors.New("nonce endpoint unsupported or not enabled")

// ErrUnavailable is returned without a request while an endpoint's recent
// transient failure is still cached.
var ErrUnavailable = errors.New("nonce endpoint failed recently; not retried yet")

// refillBatch is how many nonces a remote pool fetches when it runs dry.
const refillBatch = 64

// Pool holds available and active nonces. Only reserved nonces are considered
// valid: an available (unreserved) nonce has not yet sealed any content.
type Pool struct {
	mu     sync.Mutex
	avail  []string
	active map[string]bool
	// remote is the endpoint the pool was seeded from; nil for a local pool.
	remote *Endpoint
	client *http.Client
}

// New returns an empty pool. Reserve mints on demand.
func New() *Pool {
	return &Pool{active: map[string]bool{}}
}

// Seed pre-populates the pool with n CSPRNG nonces.
func (p *Pool) Seed(n int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mintLocked(n)
}

func (p *Pool) mintLocked(n int) error {
	for i := 0; i < n; i++ {
		s, err := mint()
		if err != nil {
			return err
		}
		p.avail = append(p.avail, s)
	}
	return nil
}

// Remote reports whether every nonce the pool hands out comes from a remote
// endpoint.
func (p *Pool) Remote() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.remote != nil
}

// Source describes where the pool's nonces come from: "local", or
// "remote (<label>)".
func (p *Pool) Source() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.remote == nil {
		return "local"
	}
	return "remote (" + p.remote.Label + ")"
}

// Local makes the pool mint locally from now on.
func (p *Pool) Local() {
	p.mu.Lock()
	p.remote = nil
	p.mu.Unlock()
}

// refillLocked tops the pool up. A remote pool asks its endpoint; a failure
// turns the pool local for the rest of the session and mints instead, so a
// sealed block is never left without a nonce.
func (p *Pool) refillLocked(n int) error {
	if p.remote != nil {
		if got, err := FetchFrom(p.client, *p.remote, n); err == nil && len(got) > 0 {
			p.avail = append(p.avail, got...)
			return nil
		}
		p.remote = nil
	}
	return p.mintLocked(n)
}

// Reserve removes one nonce from the available pool and marks it active. When
// the pool is empty it refills: remotely for a remote pool, else by minting.
func (p *Pool) Reserve() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.avail) == 0 {
		n := 1
		if p.remote != nil {
			n = refillBatch
		}
		if err := p.refillLocked(n); err != nil {
			return "", err
		}
	}
	s := p.avail[len(p.avail)-1]
	p.avail = p.avail[:len(p.avail)-1]
	p.active[s] = true
	return s, nil
}

// Release returns a reserved nonce to the available pool. Unknown nonces are
// ignored.
func (p *Pool) Release(nonce string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active[nonce] {
		delete(p.active, nonce)
		p.avail = append(p.avail, nonce)
	}
}

// Valid reports whether nonce is currently reserved (active).
func (p *Pool) Valid(nonce string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active[nonce]
}

// Rotate discards all available and active nonces and seeds n fresh ones,
// from the pool's endpoint when it is remote.
func (p *Pool) Rotate(n int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.avail = nil
	p.active = map[string]bool{}
	return p.refillLocked(n)
}

// Available returns the number of available (unreserved) nonces.
func (p *Pool) Available() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.avail)
}

// Active returns the number of reserved (active) nonces.
func (p *Pool) Active() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.active)
}

// Endpoint is one nonce source.
type Endpoint struct {
	// Label names the source for status lines ("vulnetix", "openai").
	Label string
	// BaseURL is the firewall or provider base URL; the nonce URL is
	// derived with NonceURL.
	BaseURL string
	// Authorize sets the request's credential. It must send only what the
	// endpoint's own model requests would carry.
	Authorize func(*http.Request)
}

// BearerEndpoint is an endpoint that takes the key as a Bearer token.
func BearerEndpoint(label, baseURL, apiKey string) Endpoint {
	return Endpoint{Label: label, BaseURL: baseURL, Authorize: func(r *http.Request) {
		if apiKey != "" {
			r.Header.Set("authorization", "Bearer "+apiKey)
		}
	}}
}

// SeedFromProvider seeds the pool from one endpoint, falling back to n
// locally-generated nonces when the endpoint is unsupported. Any other
// failure propagates. The pool becomes remote when the endpoint answers.
func (p *Pool) SeedFromProvider(client *http.Client, baseURL, apiKey string, fallbackN int) error {
	ep := BearerEndpoint("", baseURL, apiKey)
	nonces, err := FetchFrom(client, ep, 0)
	if errors.Is(err, ErrUnsupported) {
		return p.Seed(fallbackN)
	}
	if err != nil {
		return err
	}
	p.adopt(client, ep, nonces)
	return nil
}

// SeedFromEndpoints tries each endpoint in order and adopts the first that
// answers; when none does it seeds fallbackN local nonces. It never fails for
// an endpoint's sake: an unsupported or failing endpoint is negative-cached
// and skipped. It returns the label of the endpoint used, or "" for local.
func (p *Pool) SeedFromEndpoints(client *http.Client, eps []Endpoint, fallbackN int) (string, error) {
	for _, ep := range eps {
		if ep.BaseURL == "" {
			continue
		}
		nonces, err := FetchFrom(client, ep, 0)
		if err != nil || len(nonces) == 0 {
			continue
		}
		p.adopt(client, ep, nonces)
		return ep.Label, nil
	}
	return "", p.Seed(fallbackN)
}

func (p *Pool) adopt(client *http.Client, ep Endpoint, nonces []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.avail = append(p.avail, nonces...)
	p.remote, p.client = &ep, client
}

// mint generates one 128-bit CSPRNG nonce, hex-encoded.
func mint() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("mint nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// NonceURL returns the nonce endpoint for a base URL. The spec endpoint is
// GET {base_url}/v1/nonces; OpenAI-style base URLs already carry /v1, so a
// trailing /v1 is normalised away to avoid doubling it.
func NonceURL(baseURL string) string {
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		base = strings.TrimSuffix(base, "/v1")
	}
	return base + "/v1/nonces"
}

// NonceResponse is the JSON body returned by the nonce endpoint.
type NonceResponse struct {
	Nonces    []string `json:"nonces"`
	Count     int      `json:"count"`
	ExpiresAt string   `json:"expires_at,omitempty"`
}

// FetchNonces GETs {base_url}/v1/nonces with the key as a Bearer token.
func FetchNonces(client *http.Client, baseURL, apiKey string) ([]string, error) {
	return FetchFrom(client, BearerEndpoint("", baseURL, apiKey), 0)
}

// errNoRedirect stops a nonce fetch from following a redirect: the request
// carries a credential meant for this endpoint only.
var errNoRedirect = errors.New("nonce endpoint redirected; refusing to follow")

// FetchFrom GETs an endpoint's nonces; count > 0 asks for that many. A
// 400/401/403/404/405 is ErrUnsupported and is negative-cached for
// UnsupportedTTL; a 5xx, transport error, timeout or undecodable body is
// negative-cached for TransientTTL, during which ErrUnavailable is returned
// without a request. Redirects are never followed. The request is bounded by
// a 3s deadline so a hanging endpoint cannot freeze session construction.
func FetchFrom(client *http.Client, ep Endpoint, count int) ([]string, error) {
	if knownUnsupported(ep.BaseURL) {
		return nil, ErrUnsupported
	}
	if knownTransient(ep.BaseURL) {
		return nil, ErrUnavailable
	}
	if client == nil {
		client = httpclient.Default()
	}
	nr := *client
	nr.CheckRedirect = func(*http.Request, []*http.Request) error { return errNoRedirect }
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	u := NonceURL(ep.BaseURL)
	if count > 0 {
		u += fmt.Sprintf("?count=%d", min(count, 64))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("user-agent", version.UserAgent())
	if ep.Authorize != nil {
		ep.Authorize(req)
	}
	resp, err := nr.Do(req)
	if err != nil {
		rememberTransient(ep.BaseURL)
		return nil, fmt.Errorf("fetch nonces: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed:
		rememberUnsupported(ep.BaseURL)
		return nil, ErrUnsupported
	case http.StatusOK:
		// ok
	default:
		rememberTransient(ep.BaseURL)
		return nil, fmt.Errorf("nonce endpoint returned %d", resp.StatusCode)
	}
	var out NonceResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		rememberTransient(ep.BaseURL)
		return nil, fmt.Errorf("decode nonce response: %w", err)
	}
	return out.Nonces, nil
}

// RemoteFor reports whether the pool is remote and seeded from baseURL.
func (p *Pool) RemoteFor(baseURL string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.remote != nil && p.remote.BaseURL == baseURL
}
