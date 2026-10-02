package libitem

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MaxPromptOrder is the highest prompt order; 0 means "no position of its own".
const MaxPromptOrder = 999

var (
	promptFields = map[string]bool{"name": true, "description": true, "order": true, "enabled": true}
	promptOrder  = regexp.MustCompile(`^[0-9]{1,3}$`)
)

func init() {
	register(Prompt, func(c []byte) (string, error) { d, err := ParsePrompt(c); return d.Name, err }, nil)
}

// PromptDoc is a validated prompt. Order is 0 when the document gives none, in
// which case the host places the prompt after the last one it has. Enabled is
// true unless the document says otherwise.
type PromptDoc struct {
	Name        string
	Description string
	Order       int
	Enabled     bool
	Body        string
}

// ParsePrompt validates a canonical prompt document: front matter with a required
// name and an optional description, order (0 to 999, one to three digits) and
// enabled, then the prompt text, which must not be empty.
func ParsePrompt(canonical []byte) (PromptDoc, error) {
	fields, body, err := splitFrontMatter(string(canonical), promptFields)
	if err != nil {
		return PromptDoc{}, err
	}
	d := PromptDoc{Enabled: true}
	haveName := false
	for _, f := range fields {
		switch f.key {
		case "name":
			d.Name, haveName = fmString(f.val), true
			if d.Name == "" {
				return d, refuse("name is required")
			}
			if !ValidName(Prompt, d.Name) {
				return d, refuse("name %q is not valid: %s", cleanForMessage(d.Name), nameRule)
			}
		case "description":
			d.Description = fmString(f.val)
			if len(d.Description) > MaxDescriptionBytes {
				return d, refuse("description is %d bytes; the most is %d", len(d.Description), MaxDescriptionBytes)
			}
		case "order":
			s := fmString(f.val)
			if !promptOrder.MatchString(s) {
				return d, refuse("order must be a whole number from 0 to %d", MaxPromptOrder)
			}
			d.Order, _ = strconv.Atoi(s)
		case "enabled":
			if d.Enabled, err = fmBool(f.key, f.val); err != nil {
				return d, err
			}
		}
	}
	if !haveName {
		return d, refuse("name is required")
	}
	if strings.TrimSpace(body) == "" {
		return d, refuse("the prompt has no text after the front matter")
	}
	d.Body = strings.TrimLeft(body, "\n")
	return d, nil
}

// ComposePrompt writes a prompt document the way the host does when it exports a
// prompt it holds as a file. The name comes first; the description follows when
// there is one, flattened to one line; the order is written when it is above 0;
// enabled is written only when it is false. The result is canonical.
func ComposePrompt(d PromptDoc) ([]byte, error) {
	var b strings.Builder
	b.WriteString("---\nname: " + d.Name + "\n")
	if desc := strings.Join(strings.Fields(d.Description), " "); desc != "" {
		b.WriteString("description: " + desc + "\n")
	}
	if d.Order > 0 {
		fmt.Fprintf(&b, "order: %d\n", d.Order)
	}
	if !d.Enabled {
		b.WriteString("enabled: false\n")
	}
	b.WriteString("---\n\n" + d.Body)
	out, err := CanonicalMarkdown([]byte(b.String()))
	if err != nil {
		return nil, err
	}
	if _, err := ParsePrompt(out); err != nil {
		return nil, err
	}
	return out, nil
}
