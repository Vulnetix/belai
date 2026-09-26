// Package nonce implements the CSPRNG-seeded nonce pool used to seal harness
// delimiters. Nonces are minted with crypto/rand, reserved when attached to a
// block, released when the block is retired, and rotated to invalidate the
// whole pool. When a provider implements the nonce GET spec the harness can
// seed the pool from provider-supplied nonces; otherwise it falls back to
// local generation.
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

// ErrUnsupported is returned when a provider does not implement the nonce GET
// endpoint (or has it disabled), signalling callers to mint locally.
var ErrUnsupported = errors.New("nonce endpoint unsupported or not enabled")

// unsupportedURLs negative-caches base URLs whose nonce endpoint is known to
// be unsupported (401/403/404). A provider that does not implement
// GET /v1/nonces must not be re-probed on every session invalidation; the
// answer is stable for the process lifetime.
var unsupportedURLs sync.Map

// Pool holds available and active nonces. Only reserved nonces are considered
// valid: an available (unreserved) nonce has not yet sealed any content.
type Pool struct {
	mu     sync.Mutex
	avail  []string
	active map[string]bool
}

// New returns an empty pool. Reserve mints on demand.
func New() *Pool {
	return &Pool{active: map[string]bool{}}
}

// Seed pre-populates the pool with n CSPRNG nonces.
func (p *Pool) Seed(n int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := 0; i < n; i++ {
		s, err := mint()
		if err != nil {
			return err
		}
		p.avail = append(p.avail, s)
	}
	return nil
}

// Reserve removes one nonce from the available pool and marks it active. When
// the pool is empty it mints a fresh nonce (local-generation fallback).
func (p *Pool) Reserve() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.avail) == 0 {
		s, err := mint()
		if err != nil {
			return "", err
		}
		p.active[s] = true
		return s, nil
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

// Rotate discards all available and active nonces and seeds n fresh ones.
func (p *Pool) Rotate(n int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.avail = nil
	p.active = map[string]bool{}
	for i := 0; i < n; i++ {
		s, err := mint()
		if err != nil {
			return err
		}
		p.avail = append(p.avail, s)
	}
	return nil
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

// SeedFromProvider seeds the pool from the provider's nonce endpoint, falling
// back to n locally-generated nonces when the endpoint is unsupported. The
// client may be nil; it is used for the fetch and should carry a timeout when
// set by the caller.
func (p *Pool) SeedFromProvider(client *http.Client, baseURL, apiKey string, fallbackN int) error {
	nonces, err := FetchNonces(client, baseURL, apiKey)
	if errors.Is(err, ErrUnsupported) {
		return p.Seed(fallbackN)
	}
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.avail = append(p.avail, nonces...)
	return nil
}

// mint generates one 128-bit CSPRNG nonce, hex-encoded.
func mint() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("mint nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// NonceURL returns the nonce endpoint for a provider base URL. The spec
// endpoint is GET {base_url}/v1/nonces; OpenAI-style base URLs already carry
// /v1, so a trailing /v1 is normalised away to avoid doubling it.
func NonceURL(baseURL string) string {
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		base = strings.TrimSuffix(base, "/v1")
	}
	return base + "/v1/nonces"
}

// NonceResponse is the JSON body returned by the nonce endpoint.
type NonceResponse struct {
	Nonces []string `json:"nonces"`
	Count  int      `json:"count"`
}

// FetchNonces GETs {base_url}/v1/nonces. apiKey, when non-empty, is sent as a
// Bearer token. A 401/403/404 is reported as ErrUnsupported so callers fall
// back to local generation, and is negative-cached per base URL for the
// process lifetime. The request is bounded by a 3s deadline so a hanging
// provider cannot freeze session construction.
func FetchNonces(client *http.Client, baseURL, apiKey string) ([]string, error) {
	if _, ok := unsupportedURLs.Load(baseURL); ok {
		return nil, ErrUnsupported
	}
	if client == nil {
		client = httpclient.Default()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, NonceURL(baseURL), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("user-agent", version.UserAgent())
	if apiKey != "" {
		req.Header.Set("authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch nonces: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		unsupportedURLs.Store(baseURL, true)
		return nil, ErrUnsupported
	case http.StatusOK:
		// ok
	default:
		return nil, fmt.Errorf("nonce endpoint returned %d", resp.StatusCode)
	}
	var nr NonceResponse
	if err := json.NewDecoder(resp.Body).Decode(&nr); err != nil {
		return nil, fmt.Errorf("decode nonce response: %w", err)
	}
	return nr.Nonces, nil
}
