package tools

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/vulnetix/belai/internal/shellsafe"
)

// Hazard classes for a file a write tool names. Like the shell hazards of
// internal/shellsafe, the set is closed and an identifier is harness text:
// docs/hazard-rails.md lists exactly these, and a test holds the page and
// [HazardPathClasses] together.
const (
	// HazardPathVCS is a repository's own control files: .git, .gitmodules and
	// the hook directories that run on a commit.
	HazardPathVCS = "path.vcs"
	// HazardPathState is Belai's own state and settings.
	HazardPathState = "path.state"
	// HazardPathCredential is a credential or key store.
	HazardPathCredential = "path.credential"
	// HazardPathShell is a shell start-up file.
	HazardPathShell = "path.shell"
	// HazardPathLockfile is a dependency lockfile.
	HazardPathLockfile = "path.lockfile"
	// HazardPathCI is a build or release pipeline definition.
	HazardPathCI = "path.ci"
	// HazardPathUser is a path the user listed in permissions.protected_paths.
	HazardPathUser = "path.user"
)

// HazardPathClasses lists every class [HazardCall] can return for a file, in
// the order it returns them.
var HazardPathClasses = []string{
	HazardPathVCS, HazardPathState, HazardPathCredential, HazardPathShell,
	HazardPathLockfile, HazardPathCI, HazardPathUser,
}

// ProtectedRule is one row of the fixed protected-path table: a class, how the
// row matches, and the names it matches. Kind is "segment" (any directory or
// file name on the path), "name" (the file's own name), "dir" (a sequence of
// directories on the path), "home" (a path under the home directory) or
// "credential" (a file name that holds credentials, see credentialName).
type ProtectedRule struct {
	Class string
	Kind  string
	Match string
}

// ProtectedRules is the fixed table. A repository or a model cannot change it;
// the user adds to it with permissions.protected_paths, never removes.
var ProtectedRules = []ProtectedRule{
	{HazardPathVCS, "segment", ".git"},
	{HazardPathVCS, "name", ".gitmodules"},
	{HazardPathVCS, "segment", ".husky"},
	{HazardPathState, "segment", ".belai"},
	{HazardPathState, "dir", ".vulnetix/belai"},
	{HazardPathCredential, "home", ".ssh"},
	{HazardPathCredential, "home", ".gnupg"},
	{HazardPathCredential, "home", ".aws"},
	{HazardPathCredential, "home", ".kube"},
	{HazardPathCredential, "home", ".docker/config.json"},
	{HazardPathCredential, "home", ".config/gcloud"},
	{HazardPathCredential, "credential", "key and credential file names"},
	{HazardPathShell, "home", ".bashrc"},
	{HazardPathShell, "home", ".bash_profile"},
	{HazardPathShell, "home", ".bash_login"},
	{HazardPathShell, "home", ".profile"},
	{HazardPathShell, "home", ".zshrc"},
	{HazardPathShell, "home", ".zshenv"},
	{HazardPathShell, "home", ".zprofile"},
	{HazardPathShell, "home", ".config/fish/config.fish"},
	{HazardPathLockfile, "name", "package-lock.json"},
	{HazardPathLockfile, "name", "npm-shrinkwrap.json"},
	{HazardPathLockfile, "name", "yarn.lock"},
	{HazardPathLockfile, "name", "pnpm-lock.yaml"},
	{HazardPathLockfile, "name", "bun.lock"},
	{HazardPathLockfile, "name", "bun.lockb"},
	{HazardPathLockfile, "name", "Cargo.lock"},
	{HazardPathLockfile, "name", "go.sum"},
	{HazardPathLockfile, "name", "poetry.lock"},
	{HazardPathLockfile, "name", "Pipfile.lock"},
	{HazardPathLockfile, "name", "uv.lock"},
	{HazardPathLockfile, "name", "Gemfile.lock"},
	{HazardPathLockfile, "name", "composer.lock"},
	{HazardPathLockfile, "name", "Package.resolved"},
	{HazardPathCI, "dir", ".github/workflows"},
	{HazardPathCI, "dir", ".github/actions"},
	{HazardPathCI, "dir", ".circleci"},
	{HazardPathCI, "dir", ".buildkite"},
	{HazardPathCI, "name", ".gitlab-ci.yml"},
	{HazardPathCI, "name", "Jenkinsfile"},
	{HazardPathCI, "name", "azure-pipelines.yml"},
	{HazardPathCI, "name", ".travis.yml"},
	{HazardPathCI, "name", "bitbucket-pipelines.yml"},
	{HazardPathCI, "name", ".drone.yml"},
}

// HazardTargeter is implemented by a file-writing tool that can name the
// absolute, symlink-resolved files a call would change, by the same resolver
// its Execute uses (the confinement check stays the last word). A path that
// does not resolve is left out: Execute refuses it.
type HazardTargeter interface {
	HazardTargets(args map[string]any) []string
}

// HazardCall reports the hazard identifiers of one call of t, or nil. A Bash
// call is read by the shell parser (shellsafe.Hazards); a call of a tool that
// implements [HazardTargeter] is read by the protected-path table plus the
// user's extra paths. Any other tool has none. It never touches the disk beyond
// what the tool's own resolver does.
func HazardCall(t Tool, args map[string]any, extra []string) []string {
	if b, ok := t.(*Bash); ok {
		if b.ReadOnly {
			return nil
		}
		cmd, _ := argString(args, "command")
		var ids []string
		for _, h := range shellsafe.Hazards(shellsafe.Analyze(cmd)) {
			ids = append(ids, h.ID)
		}
		return ids
	}
	ht, ok := t.(HazardTargeter)
	if !ok {
		return nil
	}
	targets := ht.HazardTargets(args)
	if len(targets) == 0 {
		return nil
	}
	home, _ := os.UserHomeDir()
	hit := map[string]bool{}
	for _, abs := range targets {
		for _, c := range ProtectedClasses(abs, extra, home) {
			hit[c] = true
		}
	}
	var out []string
	for _, c := range HazardPathClasses {
		if hit[c] {
			out = append(out, c)
		}
	}
	return out
}

// ProtectedClasses names the classes the absolute path abs falls in. home is
// the user's home directory ("" skips the home rows). extra are the user's
// permissions.protected_paths entries: "~/x" and absolute paths match that path
// and anything under it; an entry with a slash matches that path from any root;
// an entry without one matches a file or directory of that name (a glob is
// allowed).
func ProtectedClasses(abs string, extra []string, home string) []string {
	abs = filepath.ToSlash(filepath.Clean(abs))
	segs := strings.Split(strings.TrimPrefix(abs, "/"), "/")
	name := segs[len(segs)-1]
	inHome := ""
	if home != "" {
		h := filepath.ToSlash(filepath.Clean(home))
		if abs == h || strings.HasPrefix(abs, h+"/") {
			inHome = strings.TrimPrefix(strings.TrimPrefix(abs, h), "/")
		}
	}
	hit := map[string]bool{}
	for _, r := range ProtectedRules {
		switch r.Kind {
		case "segment":
			for _, s := range segs {
				if s == r.Match {
					hit[r.Class] = true
				}
			}
		case "name":
			if name == r.Match {
				hit[r.Class] = true
			}
		case "dir":
			if containsDir(segs, r.Match) {
				hit[r.Class] = true
			}
		case "home":
			if inHome != "" && (inHome == r.Match || strings.HasPrefix(inHome, r.Match+"/")) {
				hit[r.Class] = true
			}
		case "credential":
			if credentialName(name) {
				hit[r.Class] = true
			}
		}
	}
	for _, e := range extra {
		if userProtects(e, abs, segs, home) {
			hit[HazardPathUser] = true
		}
	}
	var out []string
	for _, c := range HazardPathClasses {
		if hit[c] {
			out = append(out, c)
		}
	}
	return out
}

// credentialName reports a file name that holds credentials: the .env family
// (a .example, .sample or .template copy is documentation), password and token
// stores, SSH private keys and key or certificate stores. Matching is by name
// alone, so credentials.go and keys.go are ordinary source files.
func credentialName(name string) bool {
	low := strings.ToLower(name)
	for _, suffix := range []string{".example", ".sample", ".template", ".dist"} {
		if strings.HasSuffix(low, suffix) {
			return false
		}
	}
	switch low {
	case ".env", ".netrc", ".npmrc", ".pypirc", ".htpasswd", ".pgpass", "credentials", "credentials.json", "secrets.json", "secrets.yaml", "secrets.yml", "secrets.env":
		return true
	}
	if strings.HasPrefix(low, ".env.") {
		return true
	}
	for _, prefix := range []string{"id_rsa", "id_ed25519", "id_ecdsa", "id_dsa"} {
		if strings.HasPrefix(low, prefix) {
			return true
		}
	}
	for _, suffix := range []string{".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".kdbx", ".ppk"} {
		if strings.HasSuffix(low, suffix) {
			return true
		}
	}
	return false
}

// containsDir reports whether the directories of dir appear in order, adjacent,
// on the path (a file's own name does not count as a directory).
func containsDir(segs []string, dir string) bool {
	want := strings.Split(dir, "/")
	if len(segs) <= len(want) {
		return false
	}
	for i := 0; i+len(want) < len(segs); i++ {
		ok := true
		for j, w := range want {
			if segs[i+j] != w {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func userProtects(entry, abs string, segs []string, home string) bool {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return false
	}
	switch {
	case strings.HasPrefix(entry, "~/"):
		if home == "" {
			return false
		}
		entry = path.Join(filepath.ToSlash(home), entry[2:])
		return abs == entry || strings.HasPrefix(abs, entry+"/")
	case strings.HasPrefix(entry, "/"):
		entry = path.Clean(entry)
		return abs == entry || strings.HasPrefix(abs, entry+"/")
	case strings.Contains(entry, "/"):
		return containsDir(append(append([]string(nil), segs...), "x"), path.Clean(entry)) ||
			strings.HasSuffix(abs, "/"+path.Clean(entry))
	}
	for _, s := range segs {
		if ok, err := path.Match(entry, s); err == nil && ok {
			return true
		}
	}
	return false
}

// HazardTargets names the absolute file Write would create or replace.
func (w *Write) HazardTargets(args map[string]any) []string {
	p, _ := argString(args, "file_path")
	res, err := resolveNewPath(w.Root, w.Cwd, p)
	if err != nil {
		return nil
	}
	return []string{res.Abs()}
}

// HazardTargets names the absolute file Edit would change.
func (e *Edit) HazardTargets(args map[string]any) []string {
	p, _ := argString(args, "file_path")
	res, err := resolvePath(e.Root, e.Cwd, p)
	if err != nil {
		return nil
	}
	return []string{res.Abs()}
}

// HazardTargets names every absolute file the patch adds, updates or deletes.
func (p *ApplyPatch) HazardTargets(args map[string]any) []string {
	pt, err := p.parse(args)
	if err != nil {
		return nil
	}
	var out []string
	for _, f := range pt.Files {
		res, err := p.resolve(f)
		if err != nil {
			continue
		}
		out = append(out, res.Abs())
	}
	return out
}
