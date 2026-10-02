// Package libitem is the host's half of the library-item contract: the seven
// kinds of item the Vulnetix website's library keeps for a tenant (skills,
// prompts, processes, repositories, token budgets, provider sets and the Bash
// rewrite table), the canonical bytes whose SHA-256 the sync compares, and a
// validator per kind.
//
// Everything here is pure: no file, network or settings access. The server
// (vdb-site) and the website apply the same rules, so a document this package
// accepts is one they accept and the other way round. The rules live in
// docs/library-items.md, and the tests there pin the numbers.
//
// A validator never repairs. A document is accepted whole or refused with the
// first message that explains why, and an unknown key is a refusal, so a
// document written for a newer Belai is never half applied by an older one.
package libitem

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
)

// Kind is one kind of library item, spelled the way the contract spells it.
type Kind string

// The kinds of library item.
const (
	Skill    Kind = "skill"
	Prompt   Kind = "prompt"
	Process  Kind = "process"
	Repo     Kind = "repo"
	Budget   Kind = "budget"
	Provider Kind = "provider"
	Rewrite  Kind = "rewrite"
)

// KindInfo describes one kind.
type KindInfo struct {
	Kind Kind
	// Segment is the plural word the website API puts in a URL.
	Segment string
	// Markdown is true for a front-matter document, false for a JSON object.
	Markdown bool
	// MaxBytes is the largest canonical document the library holds.
	MaxBytes int
}

// Ext is the extension of the document in the library's object store.
func (i KindInfo) Ext() string {
	if i.Markdown {
		return "md"
	}
	return "json"
}

// MaxItemsPerKind is how many live items a tenant may keep of one kind. The
// server enforces it; the host never sends more than this many of a kind in
// one sync.
const MaxItemsPerKind = 200

// MaxNameBytes is the longest item name.
const MaxNameBytes = 64

// RewriteName is the only name a rewrite item has: the table is a singleton.
const RewriteName = "bash_rewrite"

var kinds = []KindInfo{
	{Skill, "skills", true, 32 << 10},
	{Prompt, "prompts", true, 32 << 10},
	{Process, "processes", false, 16 << 10},
	{Repo, "repos", false, 16 << 10},
	{Budget, "budgets", false, 16 << 10},
	{Provider, "providers", false, 32 << 10},
	{Rewrite, "rewrites", false, 16 << 10},
}

// Kinds lists every kind, in the contract's order.
func Kinds() []Kind {
	out := make([]Kind, len(kinds))
	for i, k := range kinds {
		out[i] = k.Kind
	}
	return out
}

// Parse reads a kind value (skill, prompt, ...). It does not accept a URL
// segment.
func Parse(s string) (Kind, bool) {
	for _, k := range kinds {
		if string(k.Kind) == s {
			return k.Kind, true
		}
	}
	return "", false
}

// ParseSegment reads a URL segment (skills, prompts, ...).
func ParseSegment(s string) (Kind, bool) {
	for _, k := range kinds {
		if k.Segment == s {
			return k.Kind, true
		}
	}
	return "", false
}

// Info returns the kind's description; the zero value for an unknown kind.
func (k Kind) Info() KindInfo {
	for _, i := range kinds {
		if i.Kind == k {
			return i
		}
	}
	return KindInfo{}
}

// Valid reports whether k is a kind.
func (k Kind) Valid() bool { return k.Info().Kind != "" }

// Segment is the URL segment of the kind, empty for an unknown kind.
func (k Kind) Segment() string { return k.Info().Segment }

// MaxBytes is the largest canonical document of the kind.
func (k Kind) MaxBytes() int { return k.Info().MaxBytes }

// IsMarkdown is true for the front-matter kinds.
func (k Kind) IsMarkdown() bool { return k.Info().Markdown }

// Singleton reports whether the host holds at most one document of the kind
// under a name the user picks: the budget and provider sets replace the
// host's whole configuration, and the rewrite table is one table.
func (k Kind) Singleton() bool { return k == Budget || k == Provider || k == Rewrite }

// nameRE is the name every item carries.
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidName reports whether name is a legal item name for the kind: the common
// pattern, and for a rewrite exactly bash_rewrite.
func ValidName(kind Kind, name string) bool {
	if kind == Rewrite {
		return name == RewriteName
	}
	return nameRE.MatchString(name)
}

func nameError(kind Kind, name string) error {
	if kind == Rewrite {
		return refuse("name must be %q", RewriteName)
	}
	if name == "" {
		return refuse("name is required")
	}
	return refuse("name %q must be lowercase letters, digits, dots, underscores and hyphens, starting with a letter or digit, at most %d bytes", clip(name, 40), MaxNameBytes)
}

// Error is a refusal: the document is not one this package accepts. Its text is
// the first reason, in words fit for an inline message.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// IsRefusal reports whether err is a validation refusal.
func IsRefusal(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

func refuse(format string, args ...any) error {
	return &Error{Msg: fmt.Sprintf(format, args...)}
}

// clip shortens a value quoted back to the caller, so a refusal never echoes a
// whole document.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "..."
}

// sortedKeys returns a map's keys in order, so messages and walks are stable.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
