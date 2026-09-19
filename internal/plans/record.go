package plans

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/sanitize"
)

// RecordName builds a plan file stem from the prompt text, timestamp, and
// revision number.
//
//	Base:  plan-20260918-143012-fix-plan-mode
//	r2:    plan-20260918-143012-fix-plan-mode-r2
//	r3:    plan-20260918-143012-fix-plan-mode-r3
//
// The slug is taken from the first six words of the prompt, lowercased,
// with characters outside [a-z0-9] replaced by hyphens. It is truncated to 40
// characters so the safe-name regex in plans.go passes it through unchanged.
func RecordName(prompt string, at time.Time, revision int) string {
	words := strings.Fields(prompt)
	if len(words) > 6 {
		words = words[:6]
	}
	slug := strings.ToLower(strings.Join(words, " "))
	slug = recordSlugRE.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 40 {
		slug = slug[:40]
		slug = strings.Trim(slug, "-")
	}
	if slug == "" {
		slug = "plan"
	}
	base := fmt.Sprintf("plan-%s-%s", at.Format("20060102-150405"), slug)
	if revision > 1 {
		base = fmt.Sprintf("%s-r%d", base, revision)
	}
	return base
}

var recordSlugRE = regexp.MustCompile(`[^a-z0-9]+`)

// NextRevision returns the next available revision number for a plan record
// whose base name (without -rN) matches the prompt slug. It counts files
// in the project's plans directory that share the same timestamp-less stem.
func NextRevision(workdir, base string) int {
	dir := config.ProjectPlansDir(workdir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 1
	}
	revision := 1
	prefix := base + "-r"
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		name = strings.TrimSuffix(name, ".md")
		var n int
		if _, err := fmt.Sscanf(name, prefix+"%d", &n); err == nil && n > revision {
			revision = n
		}
	}
	return revision + 1
}

// Record writes a plan file and returns the recorded plan metadata, absolute
// path, and any error. The content is sanitised (mandatory: approved plan text
// can re-enter the system prompt via prompt.CarrierPlan). When the content
// parses as a structured plan document (it has numbered steps), it is
// canonicalised through Doc.Render so every recorded plan has one stable shape
// regardless of how the model formatted it; unstructured fallback replies are
// recorded verbatim after sanitisation.
func Record(workdir, prompt, reply string, at time.Time, revision int) (Plan, string, error) {
	name := RecordName(prompt, at, revision)
	content := sanitize.Sanitize(reply)
	if doc, err := ParseDoc(content); err == nil {
		content = doc.Render()
	}
	path, err := Save(workdir, Plan{Name: name, Content: content})
	if err != nil {
		return Plan{}, "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Plan{Name: name, Content: content}, path, err
	}
	return Plan{Name: name, Content: content}, abs, nil
}
