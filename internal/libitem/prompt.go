package libitem

import (
	"fmt"
	"strings"
)

// Prompt limits, shared with the website.
const (
	// MaxPromptOrder is the highest prompt order; 0 means "no position of its own".
	MaxPromptOrder = 999
)

func init() {
	register(Prompt, func(c []byte) (string, error) { d, err := ParsePrompt(c); return d.Name, err })
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

var promptKeys = []string{"name", "description", "order", "enabled"}

// ParsePrompt validates a canonical prompt document.
func ParsePrompt(canonical []byte) (PromptDoc, error) {
	fields, body, err := splitFrontMatter(string(canonical))
	if err != nil {
		return PromptDoc{}, err
	}
	d := PromptDoc{Enabled: true}
	gotName := false
	for _, f := range fields {
		switch f.key {
		case "name":
			d.Name, gotName = fmString(f.val), true
		case "description":
			d.Description = fmString(f.val)
		case "order":
			if d.Order, err = fmInt("order", f.val, 0, MaxPromptOrder); err != nil {
				return PromptDoc{}, err
			}
		case "enabled":
			if d.Enabled, err = fmBool("enabled", f.val); err != nil {
				return PromptDoc{}, err
			}
		default:
			return PromptDoc{}, refuse("unknown front-matter field %q (a prompt has %s)", clip(f.key, 40), strings.Join(promptKeys, ", "))
		}
	}
	if !gotName {
		return PromptDoc{}, refuse("required front-matter field %q is missing", "name")
	}
	if !ValidName(Prompt, d.Name) {
		return PromptDoc{}, nameError(Prompt, d.Name)
	}
	if err := checkDescription(d.Description, false); err != nil {
		return PromptDoc{}, err
	}
	if strings.TrimSpace(body) == "" {
		return PromptDoc{}, refuse("the prompt body is empty")
	}
	d.Body = body
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
