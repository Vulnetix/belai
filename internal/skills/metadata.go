package skills

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Metadata bounds. A library skill is held to them (libitem), and the builtin
// skills are pinned to them by a test. The loader enforces only the shape of
// the reserved belai.* keys, so a skill written for the earlier loader still
// loads.
const (
	MaxMetadataEntries = 32
	MaxMetadataKey     = 64
	MaxMetadataValue   = 4096
	MaxMetadataTotal   = 16384
)

// Reserved metadata keys. Everything under the belai. prefix is Belai's: a key
// that is not listed here is refused, so a typo never passes as data. Other
// keys are the author's own.
const (
	MetaPrefix    = "belai."
	MetaRole      = "belai.role"      // the profile the skill serves
	MetaNiche     = "belai.niche"     // one line naming the specialism
	MetaContexts  = "belai.contexts"  // comma-separated environment names
	MetaResources = "belai.resources" // space-separated https documentation URLs
	MetaUpdated   = "belai.updated"   // ISO date of the last review
)

const (
	maxNicheBytes    = 200
	maxContextTokens = 96
	maxContextBytes  = 48
	maxResources     = 40
	maxResourceBytes = 300
	maxRoleBytes     = 64
)

var (
	reservedMeta = map[string]bool{MetaRole: true, MetaNiche: true, MetaContexts: true, MetaResources: true, MetaUpdated: true}
	contextRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+/# -]*$`)
	roleRE       = regexp.MustCompile(`^[a-z0-9][a-z0-9:._-]*$`)
	specNameRE   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

// ValidSpecName reports whether name is a skill name under the specification:
// lowercase letters, digits and single hyphens, not starting or ending with a
// hyphen, at most 64. The directory a skill lives in must carry the same name.
func ValidSpecName(name string) bool {
	return len(name) <= 64 && specNameRE.MatchString(name)
}

// validateBelaiMetadata checks the reserved keys of a skill's metadata.
func validateBelaiMetadata(md map[string]string) error {
	keys := make([]string, 0, len(md))
	for k := range md {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !strings.HasPrefix(k, MetaPrefix) {
			continue
		}
		if !reservedMeta[k] {
			return fmt.Errorf("unknown reserved key %q", k)
		}
		v := md[k]
		var err error
		switch k {
		case MetaRole:
			if len(v) > maxRoleBytes || !roleRE.MatchString(v) {
				err = fmt.Errorf("must be a profile name of at most %d bytes", maxRoleBytes)
			}
		case MetaNiche:
			if v == "" || len(v) > maxNicheBytes || strings.ContainsAny(v, "\n\r") {
				err = fmt.Errorf("must be one line of at most %d bytes", maxNicheBytes)
			}
		case MetaContexts:
			err = checkContexts(v)
		case MetaResources:
			err = checkResources(v)
		case MetaUpdated:
			if _, perr := time.Parse("2006-01-02", v); perr != nil {
				err = fmt.Errorf("must be a date like 2026-01-31")
			}
		}
		if err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}
	return nil
}

func checkContexts(v string) error {
	toks := Contexts(v)
	if len(toks) == 0 {
		return fmt.Errorf("must name at least one context")
	}
	if len(toks) > maxContextTokens {
		return fmt.Errorf("names %d contexts; the most is %d", len(toks), maxContextTokens)
	}
	seen := map[string]bool{}
	for _, t := range toks {
		if len(t) > maxContextBytes || !contextRE.MatchString(t) {
			return fmt.Errorf("context %q must be letters, digits and . _ + / # - (at most %d bytes)", clipMsg(t, 30), maxContextBytes)
		}
		l := strings.ToLower(t)
		if seen[l] {
			return fmt.Errorf("context %q appears twice", t)
		}
		seen[l] = true
	}
	return nil
}

func checkResources(v string) error {
	urls := strings.Fields(v)
	if len(urls) == 0 {
		return fmt.Errorf("must list at least one URL")
	}
	if len(urls) > maxResources {
		return fmt.Errorf("lists %d URLs; the most is %d", len(urls), maxResources)
	}
	seen := map[string]bool{}
	for _, raw := range urls {
		if err := CheckResourceURL(raw); err != nil {
			return fmt.Errorf("%q: %w", clipMsg(raw, 60), err)
		}
		if seen[raw] {
			return fmt.Errorf("%q appears twice", clipMsg(raw, 60))
		}
		seen[raw] = true
	}
	return nil
}

// CheckResourceURL reports why raw is not an acceptable documentation link:
// https only, a host, no credentials, no fragment, bounded length.
func CheckResourceURL(raw string) error {
	if len(raw) > maxResourceBytes {
		return fmt.Errorf("longer than %d bytes", maxResourceBytes)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("must be an https URL")
	}
	if u.User != nil {
		return fmt.Errorf("must not carry credentials")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("must not carry a fragment")
	}
	for _, r := range raw {
		if r <= ' ' || r == 0x7f {
			return fmt.Errorf("must not hold control characters or spaces")
		}
	}
	return nil
}

// Contexts splits a belai.contexts value into its names.
func Contexts(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Resources splits a belai.resources value into its URLs.
func Resources(v string) []string { return strings.Fields(v) }

// CheckMetadataBounds applies the library's size limits to a skill's metadata:
// the number of entries, each key and value, and the total.
func CheckMetadataBounds(md map[string]string) error {
	if len(md) > MaxMetadataEntries {
		return fmt.Errorf("metadata has %d entries; the most is %d", len(md), MaxMetadataEntries)
	}
	total := 0
	for k, v := range md {
		if k == "" || len(k) > MaxMetadataKey {
			return fmt.Errorf("a metadata key is empty or over %d bytes", MaxMetadataKey)
		}
		if len(v) > MaxMetadataValue {
			return fmt.Errorf("metadata value %q is over %d bytes", clipMsg(k, 30), MaxMetadataValue)
		}
		for _, r := range k + v {
			if r < ' ' && r != '\t' && r != '\n' || r == 0x7f {
				return fmt.Errorf("metadata holds a control character")
			}
		}
		total += len(k) + len(v)
	}
	if total > MaxMetadataTotal {
		return fmt.Errorf("metadata is %d bytes; the most is %d", total, MaxMetadataTotal)
	}
	return nil
}

func clipMsg(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
