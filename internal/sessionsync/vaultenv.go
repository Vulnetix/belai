package sessionsync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/vulnetix/belai/internal/vaultenv"
)

// The Pix sandbox's environment variables come from the organisation's secrets
// vault. A session asks for them over its own connection, holds them in memory for a
// lease (internal/vaultenv), and asks again every minute, so a variable that expired
// or was revoked stops being injected within a minute of the vault saying so.
//
//	GET  /hosts/{id}/vault/env     the variables this host's vault grants, and the lease
//	POST /hosts/{id}/vault/scrub   how many times each was scrubbed from output (names and counts)
//
// The server answers only for a Pix sandbox's own host, over TLS, uncached. No value
// is logged, written to a file or put in an error here.

// VaultLeaseEvery is how often a session renews its lease.
const VaultLeaseEvery = time.Minute

// maxVaultEnvBytes bounds what one lease may carry.
const maxVaultEnvBytes = 1 << 20

// VaultEnv reads the variables the vault grants this host, and how long they may be
// held if the next renewal fails.
func (c *Client) VaultEnv(ctx context.Context, hostID string) ([]vaultenv.Var, time.Duration, error) {
	var out struct {
		Env []struct {
			Name      string `json:"name"`
			Value     string `json:"value"`
			ExpiresAt int64  `json:"expiresAt"`
		} `json:"env"`
		LeaseSeconds int `json:"leaseSeconds"`
	}
	path := fmt.Sprintf("/hosts/%s/vault/env", url.PathEscape(hostID))
	if err := c.do(ctx, http.MethodGet, path, nil, &out, requestTimeout); err != nil {
		return nil, 0, err
	}
	if len(out.Env) > vaultenv.MaxVars {
		return nil, 0, fmt.Errorf("sessionsync: the vault sent more than %d environment variables", vaultenv.MaxVars)
	}
	vars := make([]vaultenv.Var, 0, len(out.Env))
	total := 0
	for _, e := range out.Env {
		total += len(e.Name) + len(e.Value)
		if total > maxVaultEnvBytes {
			return nil, 0, errors.New("sessionsync: the vault's environment is too large")
		}
		v := vaultenv.Var{Name: e.Name, Value: e.Value}
		if e.ExpiresAt > 0 {
			v.ExpiresAt = time.UnixMilli(e.ExpiresAt)
		}
		vars = append(vars, v)
	}
	lease := time.Duration(out.LeaseSeconds) * time.Second
	if lease <= 0 || lease > time.Hour {
		lease = 15 * time.Minute
	}
	return vars, lease, nil
}

// ReportVaultScrub tells the vault how many times each variable was scrubbed from
// output. Only names and counts leave the machine.
func (c *Client) ReportVaultScrub(ctx context.Context, hostID string, hits map[string]int) error {
	path := fmt.Sprintf("/hosts/%s/vault/scrub", url.PathEscape(hostID))
	return c.do(ctx, http.MethodPost, path, map[string]any{"hits": hits}, nil, requestTimeout)
}

// vaultLease keeps this process's vault environment current until ctx ends. A server
// that has no such route (an older one) ends it quietly, since there is nothing to
// lease.
func (s *Syncer) vaultLease(ctx context.Context) {
	c, hostID := s.opts.Client, s.opts.HostID
	if c == nil || hostID == "" {
		return
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if !s.renewVault(ctx, c, hostID) {
			return
		}
		timer.Reset(VaultLeaseEvery)
	}
}

// renewVault does one renewal and reports whether to keep going. A failure keeps the
// lease the process already holds, which ends on its own at the time the vault gave.
func (s *Syncer) renewVault(ctx context.Context, c *Client, hostID string) bool {
	vars, lease, err := c.VaultEnv(ctx, hostID)
	switch {
	case err == nil:
		vaultenv.Default.Replace(vars, time.Now().Add(lease))
		if len(vars) > 0 {
			vaultenv.Protect()
		}
	case errors.Is(err, ErrNotFound):
		return false
	}
	if hits := vaultenv.Default.Hits(); len(hits) > 0 {
		if err := c.ReportVaultScrub(ctx, hostID, hits); err != nil {
			vaultenv.Default.Restore(hits)
		}
	}
	return true
}
