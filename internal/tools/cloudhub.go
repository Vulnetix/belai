package tools

import (
	"sync"
	"time"
)

// CloudHub holds what the cloud tools know about the place the session works:
// the engaged profile's facts, and the temporary credentials of any IAM role
// the AWS tool assumed. The tools are built before the session exists, so they
// hold the hub and the session fills it. The zero value is empty and nil-safe,
// and a nil hub means no facts and no role.
//
// The credentials are held in memory only. They are never serialised, logged
// or put in a result: the one place they leave the hub is the environment of
// an AWS or Terraform subprocess (awsrole.go).
type CloudHub struct {
	mu    sync.Mutex
	facts map[string][]string
	roles map[string]*assumedRole
	ids   map[string]cachedIdentity
}

// assumedRole is one IAM role's temporary credentials.
type assumedRole struct {
	arn, account, name string
	expires            time.Time
	id, secret, token  string
}

// SetFacts installs the profile's facts (nil clears them). A new set of facts
// drops any role assumed under the old one, so credentials never outlive the
// declaration that allowed them.
func (h *CloudHub) SetFacts(facts map[string][]string) {
	if h == nil {
		return
	}
	cp := make(map[string][]string, len(facts))
	for k, v := range facts {
		cp[k] = append([]string(nil), v...)
	}
	h.mu.Lock()
	h.facts = cp
	h.roles = nil
	h.ids = nil
	h.mu.Unlock()
}

// Facts returns a copy of the installed facts, or nil.
func (h *CloudHub) Facts() map[string][]string {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.facts) == 0 {
		return nil
	}
	cp := make(map[string][]string, len(h.facts))
	for k, v := range h.facts {
		cp[k] = append([]string(nil), v...)
	}
	return cp
}

// CloudHub returns the hub the registry's cloud tools consult, or nil.
func (r *Registry) CloudHub() *CloudHub {
	if r == nil {
		return nil
	}
	return r.cloud
}
