// Package plans stores plan files under <workdir>/.vulnetix/plans/.
package plans

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/config"
)

// Plan is a named plan document.
type Plan struct {
	Name    string
	Content string
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// nameFile maps a plan name to a safe .md filename.
func nameFile(name string) (string, error) {
	clean := strings.Trim(unsafeName.ReplaceAllString(name, "_"), "._-")
	if clean == "" {
		return "", fmt.Errorf("invalid plan name %q", name)
	}
	return clean + ".md", nil
}

// Save writes a plan and returns its path.
func Save(workdir string, p Plan) (string, error) {
	fn, err := nameFile(p.Name)
	if err != nil {
		return "", err
	}
	dir := config.ProjectPlansDir(workdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fn)
	if err := os.WriteFile(path, []byte(p.Content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// Load reads a plan by name.
func Load(workdir, name string) (Plan, error) {
	fn, err := nameFile(name)
	if err != nil {
		return Plan{}, err
	}
	data, err := os.ReadFile(filepath.Join(config.ProjectPlansDir(workdir), fn))
	if err != nil {
		return Plan{}, err
	}
	return Plan{Name: name, Content: string(data)}, nil
}

// List returns all stored plans, sorted by name.
func List(workdir string) ([]Plan, error) {
	dir := config.ProjectPlansDir(workdir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Plan
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		p, err := Load(workdir, name)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
