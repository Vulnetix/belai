// Package copilotauth exchanges a GitHub OAuth token for a short-lived
// Copilot session token. It is the only auth path in Signet that performs a
// network call before the model request, so it lives in its own package with
// its own tests. Nothing here may make a real network call in the test suite.
package copilotauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Token is a short-lived Copilot session token with its expiry.
type Token struct {
	Value     string
	ExpiresAt time.Time
}

// ErrRevokedToken is returned when GitHub rejects the OAuth token. Callers can
// use errors.Is to distinguish an expired/revoked credential from a transport
// or decode failure.
var ErrRevokedToken = errors.New("copilot oauth token expired or revoked")

// refreshMargin is how far before ExpiresAt a cached token is refreshed.
const refreshMargin = 2 * time.Minute

// copilotTokenEndpoint is the GitHub endpoint that trades an OAuth token for a
// session token.
const copilotTokenEndpoint = "https://api.github.com/copilot_internal/v2/token"

type exchangeResult struct {
	token Token
	err   error
}

type inFlight struct {
	done chan struct{}
	res  exchangeResult
}

// Exchanger trades a GitHub OAuth token for a Copilot session token, caching
// the result until shortly before it expires. Concurrent callers sharing one
// OAuth token share one in-flight exchange.
type Exchanger struct {
	client   *http.Client
	endpoint string

	mu      sync.Mutex
	cache   map[string]exchangeResult
	pending map[string]*inFlight
}

// NewExchanger builds an exchanger over client (nil means http.DefaultClient).
func NewExchanger(client *http.Client) *Exchanger {
	if client == nil {
		client = http.DefaultClient
	}
	return &Exchanger{
		client:   client,
		endpoint: copilotTokenEndpoint,
		cache:    map[string]exchangeResult{},
		pending:  map[string]*inFlight{},
	}
}

// Token returns a cached, non-expired session token for oauth, or exchanges
// for a fresh one. It returns ErrRevokedToken when GitHub rejects the OAuth
// token.
func (e *Exchanger) Token(ctx context.Context, oauth string) (Token, error) {
	e.mu.Lock()
	if res, ok := e.cache[oauth]; ok && time.Until(res.token.ExpiresAt) > refreshMargin {
		e.mu.Unlock()
		return res.token, res.err
	}
	if call, ok := e.pending[oauth]; ok {
		e.mu.Unlock()
		select {
		case <-call.done:
			return call.res.token, call.res.err
		case <-ctx.Done():
			return Token{}, ctx.Err()
		}
	}
	call := &inFlight{done: make(chan struct{})}
	e.pending[oauth] = call
	e.mu.Unlock()

	res := e.exchange(ctx, oauth)

	e.mu.Lock()
	call.res = res
	close(call.done)
	delete(e.pending, oauth)
	e.cache[oauth] = res
	e.mu.Unlock()
	return res.token, res.err
}

func (e *Exchanger) exchange(ctx context.Context, oauth string) exchangeResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, nil)
	if err != nil {
		return exchangeResult{err: err}
	}
	req.Header.Set("Authorization", "Bearer "+oauth)
	req.Header.Set("Accept", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return exchangeResult{err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return exchangeResult{err: fmt.Errorf("%w (github returned %d)", ErrRevokedToken, resp.StatusCode)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return exchangeResult{err: fmt.Errorf("copilot token exchange returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))}
	}

	var payload struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return exchangeResult{err: fmt.Errorf("decode copilot token: %w", err)}
	}
	if payload.Token == "" {
		return exchangeResult{err: errors.New("copilot token exchange returned an empty token")}
	}
	return exchangeResult{token: Token{Value: payload.Token, ExpiresAt: time.Unix(payload.ExpiresAt, 0)}}
}
