package libitem

import (
	"encoding/json"
	"strings"

	"github.com/vulnetix/belai/internal/config"
)

// Rewrite limits, Belai's own (shellsafe.MaxRewriteWords, config.MaxBashRewriteRules).
const (
	MaxRewriteRules = 64
	MaxRewriteWords = 8
	MaxRewriteWord  = 128
)

var rewriteFields = []string{"name", "enabled", "rules"}

func init() {
	register(Rewrite, validateRewrite, func(c []byte) error {
		d, err := ParseRewrite(c)
		if err != nil {
			return err
		}
		s := config.Settings{BashRewrite: &config.BashRewriteSettings{Enabled: d.Enabled, Rules: d.Rules}}
		if err := config.ValidateBashRewrite(s); err != nil {
			return refuse("%s", clip(err.Error(), 200))
		}
		return nil
	})
}

// RewriteDoc is a validated rewrite table: the rules of the `bash_rewrite`
// setting, in order, and whether the table is on.
type RewriteDoc struct {
	Name    string                   `json:"name"`
	Enabled *bool                    `json:"enabled,omitempty"`
	Rules   []config.BashRewriteRule `json:"rules"`
}

// ParseRewrite validates a canonical rewrite document and decodes it.
func ParseRewrite(canonical []byte) (RewriteDoc, error) {
	if _, err := validateRewrite(canonical); err != nil {
		return RewriteDoc{}, err
	}
	var d RewriteDoc
	if err := json.Unmarshal(canonical, &d); err != nil {
		return RewriteDoc{}, refuse("the document does not match the schema")
	}
	return d, nil
}

// RewriteDocument is the library document for the rewrite table in s. enabled is
// left out unless the setting names it.
func RewriteDocument(s config.Settings) map[string]any {
	rules := []map[string]string{}
	d := map[string]any{"name": RewriteName}
	if b := s.BashRewrite; b != nil {
		for _, r := range b.Rules {
			rules = append(rules, map[string]string{"match": r.Match, "replace": r.Replace})
		}
		if b.Enabled != nil {
			d["enabled"] = *b.Enabled
		}
	}
	d["rules"] = rules
	return d
}

// validateRewrite checks a canonical rewrite document:
//
//	{name: "bash_rewrite", enabled?, rules: [{match, replace}]}
//
// rules is required, ordered, and may be empty; the first matching rule wins on
// the host. Each side is one to eight words separated by whitespace (the document
// may use spaces only: a tab or newline is refused here), and each word is at most
// 128 bytes of letters, digits and . _ - / @ + : = , so a replacement can never
// carry shell syntax. The first word of a side is a command name: not an option and
// not NAME=value. A rule that replaces a command with itself is refused. enabled
// false keeps the table and turns it off.
func validateRewrite(canonical []byte) (string, error) {
	m, err := object(canonical)
	if err != nil {
		return "", err
	}
	if err := onlyKeys(m, "rewrite", rewriteFields...); err != nil {
		return "", err
	}
	name, err := docName(m, func(n string) bool { return ValidName(Rewrite, n) }, "it must be "+RewriteName)
	if err != nil {
		return "", err
	}
	if _, err := boolean(m, "enabled", "rewrite", true); err != nil {
		return "", err
	}
	rules, present, err := list(m, "rules", "rewrite", MaxRewriteRules)
	if err != nil {
		return "", err
	}
	if !present {
		return "", refuse("rewrite.rules is required")
	}
	for i, v := range rules {
		r, err := entry(v, "rewrite.rules", i)
		if err != nil {
			return "", err
		}
		where := "rewrite.rules[" + itoa(i) + "]"
		if err := onlyKeys(r, where, "match", "replace"); err != nil {
			return "", err
		}
		var sides [2][]string
		for j, side := range []string{"match", "replace"} {
			s, present, err := str(r, side, where)
			if err != nil {
				return "", err
			}
			if !present {
				return "", refuse("%s.%s is required", where, side)
			}
			if sides[j], err = rewriteWords(s, where+"."+side); err != nil {
				return "", err
			}
		}
		if strings.Join(sides[0], " ") == strings.Join(sides[1], " ") {
			return "", refuse("%s: match and replace are the same", where)
		}
	}
	return name, nil
}

// rewriteWords splits one side of a rule and checks every word.
func rewriteWords(s, where string) ([]string, error) {
	if !plainText(s) {
		return nil, refuse("%s holds a control character; separate words with spaces", where)
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil, refuse("%s is empty", where)
	}
	if len(words) > MaxRewriteWords {
		return nil, refuse("%s has more than %d words", where, MaxRewriteWords)
	}
	for _, w := range words {
		if !plainWord(w) {
			return nil, refuse("%s word %q must be plain: letters, digits and . _ - / @ + : = ,", where, cleanForMessage(w))
		}
	}
	if first := words[0]; strings.HasPrefix(first, "-") || strings.Contains(first, "=") {
		return nil, refuse("%s must start with a command name, not %q", where, cleanForMessage(first))
	}
	return words, nil
}

// plainWord is Belai's shellsafe plainWord: ASCII letters, digits and . _ - / @ + : = ,
// only, 1 to 128 bytes.
func plainWord(w string) bool {
	if w == "" || len(w) > MaxRewriteWord {
		return false
	}
	for i := 0; i < len(w); i++ {
		c := w[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("._-/@+:=,", c) >= 0:
		default:
			return false
		}
	}
	return true
}
