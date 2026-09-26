package plugins

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/proc"
)

// ErrDeclined is returned when the user does not confirm.
var ErrDeclined = errors.New("plugin install declined")

// Confirm is asked with the full listing before anything is installed.
// prev is the installed version's listing on an update, nil on install.
type Confirm func(s Summary, source, commit string, prev *Summary) bool

// IsGitSource reports whether source names a git remote rather than a local
// directory.
func IsGitSource(source string) bool {
	for _, p := range []string{"https://", "ssh://", "git@", "git://"} {
		if strings.HasPrefix(source, p) {
			return true
		}
	}
	return false
}

// gitCmd runs git with a fixed shape: no repository hooks, no file://
// transport, no credential prompt, and the scrubbed environment.
var gitCmd = func(ctx context.Context, dir string, args ...string) (string, error) {
	base := []string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.file.allow=never", "-c", "credential.interactive=never"}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Dir = dir
	cmd.Env = append(proc.ScrubbedEnv(), "GIT_TERMINAL_PROMPT=0")
	proc.SetProcessGroup(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// fetch places the plugin source at dest and returns the commit it is
// pinned to ("local" for a directory copy).
func fetch(ctx context.Context, source, dest string) (string, error) {
	if IsGitSource(source) {
		url, ref, _ := strings.Cut(source, "#")
		if strings.HasPrefix(url, "-") || strings.HasPrefix(ref, "-") {
			return "", fmt.Errorf("invalid plugin source %q", source)
		}
		if _, err := gitCmd(ctx, filepath.Dir(dest), "clone", "--quiet", "--no-recurse-submodules", "--", url, dest); err != nil {
			return "", err
		}
		if ref != "" {
			if _, err := gitCmd(ctx, dest, "checkout", "--quiet", "--detach", ref, "--"); err != nil {
				return "", err
			}
		}
		return gitCmd(ctx, dest, "rev-parse", "HEAD")
	}
	src, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}
	if err := copyTree(src, dest); err != nil {
		return "", err
	}
	return "local", nil
}

// copyTree copies regular files and directories only: symlinks, devices and
// .git are skipped, so a local plugin cannot point outside itself.
func copyTree(src, dest string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		target := filepath.Join(dest, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o700)
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			return copyFile(path, target, info.Mode().Perm()&0o700)
		}
		return nil
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Install fetches source into a staging directory, validates every
// component, asks confirm with the full listing, and only then moves it into
// place and records it. A plugin of the same name must be updated instead.
func Install(ctx context.Context, source string, confirm Confirm) (Record, error) {
	return install(ctx, source, confirm, false)
}

// Update re-fetches an installed plugin from its recorded source (or a new
// one), shows the new listing next to the old, and moves the pin only when
// the user confirms again.
func Update(ctx context.Context, name, source string, confirm Confirm) (Record, error) {
	recs, err := List()
	if err != nil {
		return Record{}, err
	}
	for _, r := range recs {
		if r.Name == name {
			if source == "" {
				source = r.Source
			}
			return install(ctx, source, confirm, true)
		}
	}
	return Record{}, fmt.Errorf("plugin %q is not installed", name)
}

func install(ctx context.Context, source string, confirm Confirm, update bool) (Record, error) {
	base, err := Dir()
	if err != nil {
		return Record{}, err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return Record{}, err
	}
	staging, err := os.MkdirTemp(base, ".staging-")
	if err != nil {
		return Record{}, err
	}
	defer os.RemoveAll(staging)
	dest := filepath.Join(staging, "src")
	commit, err := fetch(ctx, source, dest)
	if err != nil {
		return Record{}, err
	}
	sum, err := Validate(dest)
	if err != nil {
		return Record{}, err
	}
	name := sum.Manifest.Name
	recs, err := List()
	if err != nil {
		return Record{}, err
	}
	var prev *Summary
	idx := -1
	for i, r := range recs {
		if r.Name == name {
			idx = i
		}
	}
	switch {
	case idx >= 0 && !update:
		return Record{}, fmt.Errorf("plugin %q is already installed; use update", name)
	case idx < 0 && update:
		return Record{}, fmt.Errorf("the source now names plugin %q, which is not the one being updated", name)
	case idx >= 0:
		root, _ := Root(name)
		if old, err := Validate(root); err == nil {
			prev = &old
		}
	}
	if confirm == nil || !confirm(sum, source, commit, prev) {
		return Record{}, ErrDeclined
	}
	root, err := Root(name)
	if err != nil {
		return Record{}, err
	}
	if update {
		old := root + ".old"
		_ = os.RemoveAll(old)
		if err := os.Rename(root, old); err != nil && !os.IsNotExist(err) {
			return Record{}, err
		}
		defer os.RemoveAll(old)
	}
	if err := os.Rename(dest, root); err != nil {
		return Record{}, err
	}
	rec := Record{Name: name, Source: source, Commit: commit, Version: sum.Manifest.Version, Enabled: true, Installed: time.Now().UTC()}
	if idx >= 0 {
		rec.Enabled = recs[idx].Enabled
		recs[idx] = rec
	} else {
		recs = append(recs, rec)
	}
	return rec, save(recs)
}

// Describe renders a summary for a confirmation prompt. Every string comes
// from plugin files, so control characters are removed.
func Describe(s Summary, source, commit string, prev *Summary) string {
	var b strings.Builder
	m := s.Manifest
	fmt.Fprintf(&b, "Plugin %s %s\n", clean(m.Name), clean(m.Version))
	if m.Description != "" {
		fmt.Fprintf(&b, "  %s\n", clean(m.Description))
	}
	fmt.Fprintf(&b, "  source: %s\n  commit: %s\n", clean(source), clean(commit))
	list := func(label string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "  %s:\n", label)
		for _, it := range items {
			fmt.Fprintf(&b, "    - %s\n", clean(it))
		}
	}
	list("skills", s.Skills)
	if len(s.Hooks) > 0 {
		b.WriteString("  hooks (commands Belai will run):\n")
		for _, h := range s.Hooks {
			fmt.Fprintf(&b, "    - %s on %s runs %s", clean(h.Name), clean(h.Event), clean(h.Command))
			if h.Matcher != "" {
				fmt.Fprintf(&b, " for %s", clean(h.Matcher))
			}
			b.WriteString("\n")
		}
	}
	list("prompts", s.Prompts)
	list("agents", s.Agents)
	if prev != nil {
		fmt.Fprintf(&b, "  replaces the installed version with %d skill(s), %d hook(s), %d prompt(s), %d agent(s)\n",
			len(prev.Skills), len(prev.Hooks), len(prev.Prompts), len(prev.Agents))
	}
	return b.String()
}

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || (r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return -1
		}
		return r
	}, s)
}
