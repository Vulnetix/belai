// Package goals stores goal files under <workdir>/.vulnetix/goals/. A goal can
// be "memorised" and later replayed through slash-command autocomplete.
package goals

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/config"
)

// Goal is a named goal document.
type Goal struct {
	Name    string
	Content string
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func nameFile(name string) (string, error) {
	clean := strings.Trim(unsafeName.ReplaceAllString(name, "_"), "._-")
	if clean == "" {
		return "", fmt.Errorf("invalid goal name %q", name)
	}
	return clean + ".md", nil
}

// Memorise saves a goal and returns its path. This is the "memorise" action
// surfaced to the user.
func Memorise(workdir string, g Goal) (string, error) {
	fn, err := nameFile(g.Name)
	if err != nil {
		return "", err
	}
	dir := config.ProjectGoalsDir(workdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fn)
	if err := os.WriteFile(path, []byte(g.Content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// Load reads a goal by name.
func Load(workdir, name string) (Goal, error) {
	fn, err := nameFile(name)
	if err != nil {
		return Goal{}, err
	}
	data, err := os.ReadFile(filepath.Join(config.ProjectGoalsDir(workdir), fn))
	if err != nil {
		return Goal{}, err
	}
	return Goal{Name: name, Content: string(data)}, nil
}

// Names returns the stored goal names, sorted, for slash-command autocomplete.
func Names(workdir string) ([]string, error) {
	dir := config.ProjectGoalsDir(workdir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(out)
	return out, nil
}
