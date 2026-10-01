// Package factspec is the one table behind an agent profile's `facts`: the
// structured key/value pairs a profile author declares about the environment
// an agent works in (an AWS role, a Kubernetes context, a Terraform
// directory).
//
// Any key of the right shape is accepted. It is shown to the model and read by
// nothing else. A well-known key (see Table) is also read by the harness: its
// shape is checked, and the tool it names receives it as a fixed environment
// variable or flag (Bind). The binding table is the harness's. No model
// argument chooses an environment variable, and a fact never widens what a
// tool may do, only where it points.
//
// Facts are shown to the model, so they are not for secrets: a key that names
// one, and a value shaped like an AWS access key id, are refused rather than
// repaired.
package factspec

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/sanitize"
)

// Caps on a profile's facts.
const (
	MaxKeys       = 64
	MaxValues     = 32
	MaxValueRunes = 512
)

var (
	keyShape = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

	// secretSuffixes name a key that holds a secret. Facts ride into the
	// prompt, so such a key is refused.
	secretSuffixes = []string{"_secret", "_token", "_password", "_passphrase", "_api_key", "_credentials"}

	// accessKeyID is the shape of an AWS access key id.
	accessKeyID = regexp.MustCompile(`(AKIA|ASIA)[A-Z0-9]{16}`)
)

// Entry is one well-known fact.
type Entry struct {
	// Key is the fact's name. For a Family it is the prefix, and the rest of
	// the key names the thing (tf_var_environment sets TF_VAR_environment).
	Key    string
	Family bool
	// Many allows a list; otherwise exactly one value.
	Many bool
	// Value and Effect describe the fact for the documentation table.
	Value  string
	Effect string
	// Check is the shape of each value.
	Check func(string) bool
	// Hidden keeps the fact out of the model's prompt: the harness reads it and
	// the model never sees it.
	Hidden bool
	// Pin makes the fact a boundary. While it is set, the flags in Overrides
	// are refused, so the model cannot point the tool elsewhere. Without it the
	// fact is a default and an explicit flag wins.
	Pin bool
	// Env, Flags and Overrides are the bindings (bind.go).
	Env       []EnvBind
	Flags     []FlagBind
	Overrides []Override
}

// EnvBind sets environment variables on one tool.
type EnvBind struct {
	Tool  string
	Names []string
}

// FlagBind adds a flag to one tool's command.
type FlagBind struct {
	Tool string
	Flag string
	// Joined writes Flag=value as one token; otherwise Flag and value are two.
	Joined bool
	// Front places the flag before the subcommand (a global option such as
	// terraform -chdir); otherwise it is appended.
	Front bool
	// Unless lists flags that mean the model already chose a value, so the
	// fact adds nothing.
	Unless []string
	// Skip reports subcommands that take no such flag.
	Skip func(argv []string) bool
}

// Override lists flags on a tool that a pinned fact refuses.
type Override struct {
	Tool  string
	Flags []string
}

// Table returns the well-known facts in documentation order.
func Table() []Entry { return table }

// Lookup returns the well-known entry for key, and for a family key (such as
// tf_var_environment) the family's entry.
func Lookup(key string) (Entry, bool) {
	for _, e := range table {
		if e.Family {
			if strings.HasPrefix(key, e.Key) && len(key) > len(e.Key) {
				return e, true
			}
			continue
		}
		if e.Key == key {
			return e, true
		}
	}
	return Entry{}, false
}

// Hidden reports whether key is a well-known fact kept out of the prompt.
func Hidden(key string) bool {
	e, ok := Lookup(key)
	return ok && e.Hidden
}

// ValidKey reports whether s may be a fact's key.
func ValidKey(s string) bool { return keyShape.MatchString(s) }

// Validate checks a profile's facts. It rejects rather than repairs, so a
// value that was written to be read is read as written.
func Validate(facts map[string][]string) error {
	if len(facts) > MaxKeys {
		return fmt.Errorf("facts holds at most %d keys", MaxKeys)
	}
	keys := sortedKeys(facts)
	for _, k := range keys {
		vals := facts[k]
		if !ValidKey(k) {
			return fmt.Errorf("facts key %q must be lowercase letters, digits and underscores, starting with a letter, at most 64 characters", k)
		}
		for _, s := range secretSuffixes {
			if strings.HasSuffix(k, s) {
				return fmt.Errorf("facts key %q names a secret; facts are shown to the model, so keep credentials out of them", k)
			}
		}
		if len(vals) == 0 {
			return fmt.Errorf("facts key %q has no value", k)
		}
		if len(vals) > MaxValues {
			return fmt.Errorf("facts key %q holds at most %d values", k, MaxValues)
		}
		for _, v := range vals {
			if err := cleanValue(k, v); err != nil {
				return err
			}
		}
		if e, ok := Lookup(k); ok {
			if err := checkEntry(k, e, vals); err != nil {
				return err
			}
		}
	}
	return nil
}

func cleanValue(key, v string) error {
	if v == "" || strings.TrimSpace(v) != v || utf8.RuneCountInString(v) > MaxValueRunes ||
		strings.ContainsAny(v, "\n\t") || sanitize.Text(v) != v {
		return fmt.Errorf("facts key %q: each value must be one clean line of at most %d characters", key, MaxValueRunes)
	}
	if accessKeyID.MatchString(v) {
		return fmt.Errorf("facts key %q: a value looks like an AWS access key id; facts are shown to the model, so keep credentials out of them", key)
	}
	return nil
}

func checkEntry(key string, e Entry, vals []string) error {
	if !e.Many && len(vals) != 1 {
		return fmt.Errorf("facts key %q takes one value", key)
	}
	if e.Family {
		if !familyName.MatchString(strings.TrimPrefix(key, e.Key)) {
			return fmt.Errorf("facts key %q: the part after %q must be a lowercase variable name", key, e.Key)
		}
		return nil
	}
	for _, v := range vals {
		if e.Check != nil && !e.Check(v) {
			return fmt.Errorf("facts key %q: %q is not %s", key, v, e.Value)
		}
	}
	return nil
}

var familyName = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,63}$`)

// reservedPrefixes are the tool families whose keys the harness reads. A key
// under one that is not well-known is accepted, since any fact is, but it is
// probably a typo, so Warnings names it.
var reservedPrefixes = []string{
	"aws_", "terraform_", "tf_var_", "kubectl_", "azure_", "gcloud_", "github_", "gitlab_",
	"pulumi_", "heroku_", "fly_", "vercel_", "netlify_", "doctl_", "onepassword_", "vulnetix_", "stripe_",
}

// Conventional are keys that carry context for the model and that nothing
// reads. They are listed so profiles use the same names, and they are never
// warned about.
var Conventional = []string{
	"aws_log_groups", "aws_account_alias", "terraform_backend", "terraform_version",
	"fly_org", "stripe_account", "environment", "service", "team", "owner", "runbook",
}

// Warnings reports keys that look like a well-known fact written wrong. A
// warning never fails a profile.
func Warnings(facts map[string][]string) []string {
	var out []string
	for _, k := range sortedKeys(facts) {
		if _, ok := Lookup(k); ok || isConventional(k) {
			continue
		}
		for _, p := range reservedPrefixes {
			if !strings.HasPrefix(k, p) {
				continue
			}
			msg := fmt.Sprintf("facts key %q is not a well-known fact, so only the model reads it", k)
			if near := nearest(k); near != "" {
				msg += fmt.Sprintf(" (did you mean %q?)", near)
			}
			out = append(out, msg)
			break
		}
	}
	return out
}

func isConventional(k string) bool {
	for _, c := range Conventional {
		if c == k {
			return true
		}
	}
	return false
}

// nearest returns the well-known key closest to k, or "" when none is close.
func nearest(k string) string {
	best, bestD := "", 4
	consider := func(c string) {
		if d := distance(k, c); d < bestD {
			best, bestD = c, d
		}
	}
	for _, e := range table {
		if !e.Family {
			consider(e.Key)
		}
	}
	for _, c := range Conventional {
		consider(c)
	}
	return best
}

func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// Visible returns the facts the model may see, in key order: every fact but a
// hidden well-known one.
func Visible(facts map[string][]string) []string {
	var lines []string
	for _, k := range sortedKeys(facts) {
		if Hidden(k) {
			continue
		}
		lines = append(lines, k+": "+strings.Join(facts[k], ", "))
	}
	return lines
}

// One returns the single value of key, or "".
func One(facts map[string][]string, key string) string {
	if v := facts[key]; len(v) == 1 {
		return v[0]
	}
	return ""
}

// Seconds returns key as a number of seconds, or 0.
func Seconds(facts map[string][]string, key string) int {
	n, err := strconv.Atoi(One(facts, key))
	if err != nil {
		return 0
	}
	return n
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Markdown renders Table as the documentation's table of well-known facts.
// docs/agent-profiles.md carries it between marker comments, and a test holds
// the two together.
func Markdown() string {
	var b strings.Builder
	b.WriteString("| Fact | Value | Effect | Pin |\n| ---- | ----- | ------ | --- |\n")
	for _, e := range table {
		key := e.Key
		if e.Family {
			key += "NAME"
		}
		pin := "no"
		if e.Pin {
			pin = "yes"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", key, e.Value, e.Effect, pin)
	}
	return b.String()
}
