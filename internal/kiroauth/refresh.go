package kiroauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Token is a short-lived Kiro access token with its expiry.
type Token struct {
	Value     string
	ExpiresAt time.Time
	// ProfileARN is the login's profile, sent with each request when set.
	ProfileARN string
}

// refreshMargin is how far before ExpiresAt a cached token is refreshed.
const refreshMargin = 2 * time.Minute

type refreshResult struct {
	token Token
	err   error
}

type refreshCall struct {
	done chan struct{}
	res  refreshResult
}

// Refresher trades a stored Login for an access token, caching the result
// until shortly before it expires. Concurrent callers sharing one login share
// one in-flight refresh.
type Refresher struct {
	oidc oidcClient
	now  func() time.Time

	mu      sync.Mutex
	cache   map[string]refreshResult
	pending map[string]*refreshCall
	rotated map[string]string
	// onRotate is set with SetOnRotate.
	onRotate func(oldLogin, newLogin string)
}

// SetOnRotate installs fn to be called after the service issues a new
// refresh token, with the login as stored and as it should now be stored. It
// is the caller's chance to persist the rotation; the refresher already uses
// the new login for the rest of the process either way.
func (r *Refresher) SetOnRotate(fn func(oldLogin, newLogin string)) {
	r.mu.Lock()
	r.onRotate = fn
	r.mu.Unlock()
}

// NewRefresher builds a refresher over client (nil means a short-timeout
// client).
func NewRefresher(client *http.Client) *Refresher {
	return &Refresher{
		oidc:    oidcClient{client: client},
		now:     time.Now,
		cache:   map[string]refreshResult{},
		pending: map[string]*refreshCall{},
		rotated: map[string]string{},
	}
}

// WithBaseURL points the refresher at a test SSO-OIDC endpoint. It must pass
// AllowedOIDCURL (loopback in tests).
func (r *Refresher) WithBaseURL(base string) *Refresher {
	r.oidc.base = base
	return r
}

// Token returns a cached, non-expired access token for the stored login, or
// refreshes for a fresh one. It returns ErrReloginRequired when the login can
// no longer mint tokens.
func (r *Refresher) Token(ctx context.Context, stored string) (Token, error) {
	r.mu.Lock()
	key := stored
	for i := 0; i < 8; i++ { // follow rotations; bounded so a cycle cannot spin
		next, ok := r.rotated[key]
		if !ok {
			break
		}
		key = next
	}
	if res, ok := r.cache[key]; ok && res.err == nil && res.token.ExpiresAt.Sub(r.now()) > refreshMargin {
		r.mu.Unlock()
		return res.token, nil
	}
	if call, ok := r.pending[key]; ok {
		r.mu.Unlock()
		select {
		case <-call.done:
			return call.res.token, call.res.err
		case <-ctx.Done():
			return Token{}, ctx.Err()
		}
	}
	call := &refreshCall{done: make(chan struct{})}
	r.pending[key] = call
	r.mu.Unlock()

	res, newLogin := r.refresh(ctx, key)

	r.mu.Lock()
	call.res = res
	close(call.done)
	delete(r.pending, key)
	if newLogin != "" && newLogin != key {
		r.rotated[key] = newLogin
		r.cache[newLogin] = res
		delete(r.cache, key)
	} else if res.err == nil {
		r.cache[key] = res
	}
	onRotate := r.onRotate
	r.mu.Unlock()

	if newLogin != "" && newLogin != key && onRotate != nil {
		onRotate(stored, newLogin)
	}
	return res.token, res.err
}

func (r *Refresher) refresh(ctx context.Context, stored string) (refreshResult, string) {
	l, err := ParseLogin(stored)
	if err != nil {
		return refreshResult{err: err}, ""
	}
	if l.Expired(r.now()) {
		return refreshResult{err: ErrReloginRequired}, ""
	}
	var tok tokenResponse
	status, oe, err := r.oidc.post(ctx, l.Region, "/token", map[string]string{
		"clientId":     l.ClientID,
		"clientSecret": l.ClientSecret,
		"grantType":    "refresh_token",
		"refreshToken": l.RefreshToken,
	}, &tok)
	if err != nil {
		return refreshResult{err: fmt.Errorf("kiro token refresh: %w", err)}, ""
	}
	if status < 200 || status >= 300 {
		switch oe.Error {
		case "invalid_grant", "InvalidGrantException", "invalid_client", "InvalidClientException",
			"unauthorized_client", "UnauthorizedClientException", "expired_token", "ExpiredTokenException":
			return refreshResult{err: ErrReloginRequired}, ""
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return refreshResult{err: ErrReloginRequired}, ""
		}
		return refreshResult{err: fmt.Errorf("kiro token refresh returned HTTP %d %s", status, safeCode(oe.Error))}, ""
	}
	if tok.AccessToken == "" {
		return refreshResult{err: errors.New("kiro token refresh returned an empty access token")}, ""
	}
	expiresIn := tok.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	token := Token{Value: tok.AccessToken, ExpiresAt: r.now().Add(time.Duration(expiresIn) * time.Second), ProfileARN: l.ProfileARN}
	var newLogin string
	if tok.RefreshToken != "" && tok.RefreshToken != l.RefreshToken {
		l.RefreshToken = tok.RefreshToken
		newLogin = l.Encode()
	}
	return refreshResult{token: token}, newLogin
}
