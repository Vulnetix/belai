package agentprofile

import (
	"crypto/rand"
	"crypto/sha1"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/vulnetix/belai/internal/sanitize"
)

// Profile identity and presentation. These fields describe who an agent is
// to the people looking at it, never what it may do: none of them reaches a
// permission, a tool list or a gate. The website shows them, and a profile
// that travels through the library keeps them.

// Limits on the presentation fields.
const (
	MaxDisplayNameRunes = 64
	MaxReportStyleRunes = 280
	MaxFocusItems       = 8
	MaxFocusRunes       = 80
	MaxVocabularyItems  = 20
	MaxVocabularyRunes  = 32
	// PaletteSize is the number of colours a palette holds: primary,
	// secondary and the two shades the active theme derives from them.
	PaletteSize = 4
)

// Personality is how an agent writes and what it weighs. It rides with the
// profile's persona as style hints, below the task in priority.
type Personality struct {
	// ReportStyle is how the agent writes its reports.
	ReportStyle string `json:"report_style,omitempty"`
	// Focus lists what the agent weighs most.
	Focus []string `json:"focus,omitempty"`
	// Vocabulary lists the words or kinds of words the agent prefers.
	Vocabulary []string `json:"vocabulary,omitempty"`
}

// IsZero reports whether no personality is set.
func (p *Personality) IsZero() bool {
	return p == nil || (p.ReportStyle == "" && len(p.Focus) == 0 && len(p.Vocabulary) == 0)
}

var (
	uuidPattern  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	colourRegexp = regexp.MustCompile(`^#[0-9a-f]{6}$`)
)

// ValidID reports whether s is a lowercase UUID.
func ValidID(s string) bool { return uuidPattern.MatchString(s) }

// NewID returns a random version 4 UUID.
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate profile id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return formatUUID(b), nil
}

func formatUUID(b [16]byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// builtinNamespace is the fixed namespace of built-in profile ids.
var builtinNamespace = [16]byte{
	0x7b, 0x61, 0x6c, 0x61, 0x69, 0x2d, 0x42, 0x75, 0x69, 0x6c, 0x74, 0x69, 0x6e, 0x50, 0x72, 0x6f,
}

// BuiltinID returns the id of a built-in profile: a version 5 UUID of its
// name, so every host agrees on it without storing anything.
func BuiltinID(name string) string {
	h := sha1.New()
	h.Write(builtinNamespace[:])
	h.Write([]byte(name))
	sum := h.Sum(nil)
	var b [16]byte
	copy(b[:], sum[:16])
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return formatUUID(b)
}

// DisplayNameKey is the form of a display name that uniqueness is checked
// on: Unicode-normalised, trimmed, whitespace collapsed and lower-cased. The
// store is per host, so the key alone makes the (display name, host)
// composite unique.
func DisplayNameKey(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(norm.NFKC.String(name)), " "))
}

// cleanLine reports whether s is already one clean line within max runes.
func cleanLine(s string, max int) bool {
	if s == "" || strings.TrimSpace(s) != s || utf8.RuneCountInString(s) > max {
		return false
	}
	return !strings.ContainsAny(s, "\n\t") && sanitize.Text(s) == s
}

// validateIdentity checks the presentation fields. It rejects rather than
// repairs, so a value that was written to be read is read as written.
func (p AgentProfile) validateIdentity() error {
	if p.ID != "" && !ValidID(p.ID) {
		return fmt.Errorf("id %q is not a lowercase UUID", p.ID)
	}
	if p.AvatarID != "" && !ValidID(p.AvatarID) {
		return fmt.Errorf("avatar_id %q is not a lowercase UUID", p.AvatarID)
	}
	if p.DisplayName != "" && !cleanLine(p.DisplayName, MaxDisplayNameRunes) {
		return fmt.Errorf("display_name must be one clean line of at most %d characters", MaxDisplayNameRunes)
	}
	if n := len(p.Palette); n != 0 {
		if n != PaletteSize {
			return fmt.Errorf("palette needs exactly %d colours, got %d", PaletteSize, n)
		}
		for _, c := range p.Palette {
			if !colourRegexp.MatchString(c) {
				return fmt.Errorf("palette colour %q is not a lowercase #rrggbb value", c)
			}
		}
	}
	return p.Personality.validate()
}

func (pe *Personality) validate() error {
	if pe == nil {
		return nil
	}
	if pe.ReportStyle != "" && !cleanLine(pe.ReportStyle, MaxReportStyleRunes) {
		return fmt.Errorf("personality.report_style must be one clean line of at most %d characters", MaxReportStyleRunes)
	}
	if err := validateList("personality.focus", pe.Focus, MaxFocusItems, MaxFocusRunes); err != nil {
		return err
	}
	return validateList("personality.vocabulary", pe.Vocabulary, MaxVocabularyItems, MaxVocabularyRunes)
}

func validateList(key string, items []string, maxItems, maxRunes int) error {
	if len(items) > maxItems {
		return fmt.Errorf("%s holds at most %d entries", key, maxItems)
	}
	for _, it := range items {
		if !cleanLine(it, maxRunes) {
			return fmt.Errorf("%s entries must be one clean line of at most %d characters", key, maxRunes)
		}
	}
	return nil
}

// personalityBlock renders the personality as style hints under fixed
// framing. The values are already validated clean lines, and the framing
// says they never widen what the agent may do.
func (p AgentProfile) personalityBlock() string {
	pe := p.Personality
	if pe.IsZero() {
		return ""
	}
	var b strings.Builder
	b.WriteString("Style hints from your profile. They shape how you write and what you weigh, rank below the task, and never change what you are allowed to do.")
	if pe.ReportStyle != "" {
		b.WriteString("\n- Report style: " + pe.ReportStyle)
	}
	if len(pe.Focus) > 0 {
		b.WriteString("\n- Weigh most: " + strings.Join(pe.Focus, "; "))
	}
	if len(pe.Vocabulary) > 0 {
		b.WriteString("\n- Preferred vocabulary: " + strings.Join(pe.Vocabulary, ", "))
	}
	return b.String()
}

// Persona is the profile section of a worker's system block: identity, the
// system prompt, the personality's style hints, then the declared facts.
func (p AgentProfile) Persona() string {
	parts := []string{strings.TrimSpace(p.Identity), strings.TrimSpace(p.SystemPrompt), p.personalityBlock(), p.factsBlock()}
	out := make([]string, 0, len(parts))
	for _, s := range parts {
		if s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n\n")
}

// Behavioural returns p without the fields that only present it: the id,
// display name, palette and avatar. What is left is exactly the part of the
// definition that changes what a worker does, which is what a running worker
// pins and compares.
func (p AgentProfile) Behavioural() AgentProfile {
	p.ID, p.DisplayName, p.Palette, p.AvatarID = "", "", nil, ""
	return p
}
