package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/sanitize"
)

// Hazard floor settings (docs/hazard-rails.md). They live in the permissions
// block beside the rules but are not rules:
//
//   - permissions.hazard_ask (bool, default true) keeps the always-ask floor.
//     Only a user layer can set it to false.
//   - permissions.hazard_yolo_waive (bool, default false) lets ask off (yolo)
//     skip the floor. Only a user layer can set it, and neither /yolo nor -yolo
//     does.
//   - permissions.protected_paths (list, default empty) adds paths to the fixed
//     table. Any layer may add; none removes.
//
// A malformed value fails the settings load like any other malformed key, so
// the floor is never off by accident: an absent key is on.

// MaxProtectedPaths bounds permissions.protected_paths.
const MaxProtectedPaths = 64

// MaxProtectedPathBytes bounds one permissions.protected_paths entry.
const MaxProtectedPathBytes = 200

// HazardAskOn reports whether the hazard floor is on. It is on unless a user
// layer turned it off.
func (s Settings) HazardAskOn() bool {
	return s.Permissions.HazardAsk == nil || *s.Permissions.HazardAsk
}

// HazardYoloWaived reports whether ask off skips the floor. It is false unless a
// user layer set it.
func (s Settings) HazardYoloWaived() bool {
	return s.Permissions.HazardYoloWaive != nil && *s.Permissions.HazardYoloWaive
}

// ProtectedPathList returns the user's additions to the protected paths.
func (s Settings) ProtectedPathList() []string {
	return append([]string(nil), s.Permissions.ProtectedPaths...)
}

// unmarshalHazard takes the three hazard keys out of raw, so what is left is the
// rule buckets.
func (p *PermissionRules) unmarshalHazard(raw map[string]json.RawMessage) error {
	for k, v := range raw {
		switch k {
		case "hazard_ask", "hazard_yolo_waive":
			var b bool
			if err := json.Unmarshal(v, &b); err != nil {
				return fmt.Errorf("permissions.%s must be true or false: %w", k, err)
			}
			if k == "hazard_ask" {
				p.HazardAsk = &b
			} else {
				p.HazardYoloWaive = &b
			}
		case "protected_paths":
			var l []string
			if err := json.Unmarshal(v, &l); err != nil {
				return fmt.Errorf("permissions.protected_paths must be a list of strings: %w", err)
			}
			p.ProtectedPaths = dedupeStrings(l)
		default:
			continue
		}
		delete(raw, k)
	}
	return nil
}

// mergeLayer folds other over p. Allow, ask and deny rules union, as before. The
// hazard settings follow the layer: a user layer sets the two switches and adds
// paths; a project layer adds paths only, so a cloned repository can neither
// turn the floor off nor waive it.
func (p PermissionRules) mergeLayer(other PermissionRules, project bool) PermissionRules {
	out := PermissionRules{
		Allow:           unionStrings(p.Allow, other.Allow),
		Ask:             unionStrings(p.Ask, other.Ask),
		Deny:            unionStrings(p.Deny, other.Deny),
		HazardAsk:       p.HazardAsk,
		HazardYoloWaive: p.HazardYoloWaive,
		ProtectedPaths:  unionStrings(p.ProtectedPaths, other.ProtectedPaths),
	}
	if !project {
		if other.HazardAsk != nil {
			b := *other.HazardAsk
			out.HazardAsk = &b
		}
		if other.HazardYoloWaive != nil {
			b := *other.HazardYoloWaive
			out.HazardYoloWaive = &b
		}
	}
	return out
}

// ValidProtectedPath reports whether s is acceptable as a
// permissions.protected_paths entry: one line of plain text, at most
// MaxProtectedPathBytes, no control, bidirectional or invisible rune, no
// surrounding space and no ".." segment.
func ValidProtectedPath(s string) bool {
	if s == "" || len(s) > MaxProtectedPathBytes || strings.TrimSpace(s) != s {
		return false
	}
	if sanitize.Line(s, MaxProtectedPathBytes*2) != s {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// ValidateHazard rejects too many protected paths or one that is not plain text,
// naming the key.
func ValidateHazard(s Settings) error {
	l := s.Permissions.ProtectedPaths
	if len(l) > MaxProtectedPaths {
		return fmt.Errorf("permissions.protected_paths has %d entries; at most %d", len(l), MaxProtectedPaths)
	}
	for i, e := range l {
		if !ValidProtectedPath(e) {
			return fmt.Errorf("permissions.protected_paths[%d] must be one line of plain text of at most %d bytes with no \"..\" segment", i, MaxProtectedPathBytes)
		}
	}
	return nil
}
