package libscan

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/agentimport"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/harness"
	"github.com/vulnetix/belai/internal/hooks"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/projectregistry"
	"github.com/vulnetix/belai/internal/sanitize"
)

// Harnesses is the registry a scan searches. Tests replace it with a fixture.
var Harnesses = harness.All

// TrustedRepos lists the repositories the user has trusted on this host. Tests
// replace it.
var TrustedRepos = trustedFromRegistry

// Repo is a trusted repository a scan looks into.
type Repo struct {
	Path   string
	Name   string
	Remote string
}

func trustedFromRegistry() []Repo {
	reg, err := projectregistry.Load()
	if err != nil {
		return nil
	}
	var out []Repo
	for _, e := range reg.All() {
		if e.Trusted && !e.Missing && filepath.IsAbs(e.Path) {
			out = append(out, Repo{Path: filepath.Clean(e.Path), Name: e.Name, Remote: cleanRemote(e.GitRemote)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// cleanRemote drops any credential from a remote URL and keeps one clean line.
func cleanRemote(raw string) string {
	raw = strings.TrimSpace(raw)
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.User != nil {
		u.User = nil
		raw = u.String()
	}
	return sanitize.Line(raw, 200)
}

// Scopes of an item.
const (
	ScopeUser    = "user"
	ScopeProject = "project"
)

// BelaiID names Belai's own files as a harness in a report.
const BelaiID = "belai"

// shapes of the files in a root.
const (
	shapeMD       = "md"       // <name>.md, directly or one folder down
	shapeSkill    = "skill"    // <name>/SKILL.md
	shapeDoc      = "doc"      // fixed file names
	shapeProcess  = "process"  // <NNN>-<name>.json or .sh
	shapeProfile  = "profile"  // <name>.json or <name>.md
	shapeCrew     = "crew"     // <name>.json
	shapeSettings = "settings" // settings.json: budgets, rewrites, providers, repositories

	shapeJSONKey    = "jsonkey"    // a JSON file whose hooks key is one item (Claude Code settings.json)
	shapeJSONFile   = "jsonfile"   // a JSON file that is one item (Codex hooks.json)
	shapeHookBundle = "hookbundle" // <name>/hooks.json and the scripts beside it (Belai's own)
)

// Root is one place a scan looks for one kind: a directory, or for the
// settings kinds a file.
type Root struct {
	Kind    agentimport.Kind
	Path    string
	Shape   string
	Files   []string
	Harness string
	Name    string
	Format  agentimport.Format
	Scope   string
	// Repo is the repository path of a project root.
	Repo string
	// Rel is the root relative to the repository, for the report.
	Rel string
	// Installed is true when the harness is installed on this host.
	Installed bool
	// Unsupported is set for a kind the harness keeps in a format Belai does not read.
	Unsupported bool

	// The rest are for the hook kind. Key is the JSON key of a shapeJSONKey file.
	// RepoName names a project root's repository. Home resolves ~ in a command.
	// ScriptRoots are the directories a script may be carried from: the harness's
	// own directory for a user file, the trusted repository for a project file.
	Key         string
	RepoName    string
	Home        string
	ScriptRoots []string
}

var shapeOf = map[string]string{
	harness.MDFile:   shapeMD,
	harness.SkillDir: shapeSkill,
	harness.DocFile:  shapeDoc,
	harness.JSONKey:  shapeJSONKey,
	harness.JSONFile: shapeJSONFile,
}

// Roots returns every place a scan looks, for the kinds given (nil is every
// kind): the user directories of each installed harness, Belai's own, and for
// each repository the project directories of every harness plus Belai's.
func Roots(home string, repos []Repo, kinds map[agentimport.Kind]bool) []Root {
	want := func(k agentimport.Kind) bool { return len(kinds) == 0 || kinds[k] }
	var out []Root
	add := func(r Root) {
		if want(r.Kind) {
			out = append(out, r)
		}
	}
	all := Harnesses()
	for _, h := range all {
		installed := h.Installed(home)
		for kindWord, dirs := range h.Dirs {
			kind := agentimport.Kind(kindWord)
			if _, ok := agentimport.ParseKind(kindWord); !ok {
				continue
			}
			format, ok := agentimport.ParseItemFormat(h.Format)
			if !ok {
				format = agentimport.GenericMD
			}
			shape, known := shapeOf[dirs.Shape]
			unsupported := dirs.Shape == harness.Unsupported || !known
			var userScripts []string
			if kind == agentimport.KindHook {
				for _, d := range h.Detect {
					if strings.HasPrefix(d, "~/") {
						userScripts = append(userScripts, filepath.Clean(harness.Expand(home, d)))
					}
				}
			}
			if installed {
				for _, d := range dirs.User {
					if !strings.HasPrefix(d, "~/") {
						continue
					}
					add(Root{Kind: kind, Path: filepath.Clean(harness.Expand(home, d)), Shape: shape, Files: dirs.Files, Harness: h.ID, Name: h.Name,
						Format: format, Scope: ScopeUser, Installed: true, Unsupported: unsupported,
						Key: dirs.Key, Home: home, ScriptRoots: userScripts})
				}
			}
			for _, repo := range repos {
				for _, d := range dirs.Project {
					d = filepath.Clean(d)
					if !filepath.IsLocal(d) {
						continue
					}
					r := Root{Kind: kind, Path: filepath.Join(repo.Path, d), Shape: shape, Files: dirs.Files, Harness: h.ID, Name: h.Name,
						Format: format, Scope: ScopeProject, Repo: repo.Path, Rel: d, Installed: installed, Unsupported: unsupported,
						Key: dirs.Key, RepoName: repo.Name, Home: home}
					if kind == agentimport.KindHook {
						r.ScriptRoots = []string{repo.Path}
					}
					add(r)
				}
			}
		}
	}
	// Belai's own files.
	own := func(kind agentimport.Kind, shape, scope, repo, abs, rel string) {
		add(Root{Kind: kind, Path: abs, Shape: shape, Harness: BelaiID, Name: "Belai", Format: agentimport.Belai, Scope: scope, Repo: repo, Rel: rel, Installed: true})
	}
	if gd, err := config.GlobalDir(); err == nil {
		own(agentimport.KindCommand, shapeMD, ScopeUser, "", filepath.Join(gd, "commands"), "")
		own(agentimport.KindSkill, shapeSkill, ScopeUser, "", filepath.Join(gd, "skills"), "")
		own(agentimport.KindPrompt, shapeMD, ScopeUser, "", filepath.Join(gd, "prompts"), "")
		own(agentimport.KindProcess, shapeProcess, ScopeUser, "", filepath.Join(gd, "processes"), "")
		own(agentimport.KindAgent, shapeProfile, ScopeUser, "", filepath.Join(gd, "profiles", "agents"), "")
		own(agentimport.KindCrew, shapeCrew, ScopeUser, "", filepath.Join(gd, "profiles", "crews"), "")
		for _, k := range settingsKinds {
			own(k, shapeSettings, ScopeUser, "", filepath.Join(gd, "settings.json"), "")
		}
	}
	if hd, err := config.GlobalHooksDir(); err == nil {
		own(agentimport.KindHook, shapeHookBundle, ScopeUser, "", hd, "")
	}
	for _, repo := range repos {
		pd := config.ProjectDir(repo.Path)
		rel := func(abs string) string { r, _ := filepath.Rel(repo.Path, abs); return r }
		bd := config.ProjectBelaiDir(repo.Path)
		for _, x := range []struct {
			kind  agentimport.Kind
			shape string
			dir   string
		}{
			{agentimport.KindCommand, shapeMD, filepath.Join(bd, "commands")},
			{agentimport.KindSkill, shapeSkill, filepath.Join(bd, "skills")},
			{agentimport.KindAgent, shapeProfile, filepath.Join(bd, "profiles", "agents")},
			{agentimport.KindCrew, shapeCrew, filepath.Join(bd, "profiles", "crews")},
			{agentimport.KindPrompt, shapeMD, filepath.Join(pd, "prompts")},
			{agentimport.KindProcess, shapeProcess, filepath.Join(pd, "processes")},
		} {
			own(x.kind, x.shape, ScopeProject, repo.Path, x.dir, rel(x.dir))
		}
		for _, k := range settingsKinds {
			p := config.ProjectSettingsPath(repo.Path)
			own(k, shapeSettings, ScopeProject, repo.Path, p, rel(p))
		}
	}
	return out
}

// settingsKinds are the kinds read from a settings file.
var settingsKinds = []agentimport.Kind{agentimport.KindBudget, agentimport.KindRewrite, agentimport.KindProvider, agentimport.KindRepo}

// Resolved is a path a scan would report, matched to the root it is under.
type Resolved struct {
	Root Root
	// File is the file on disk. For a settings item, Name is the item in it.
	File string
	Name string
	// Hint is the name to give the importer: the item in a settings file, or the
	// name a command one folder down is known by (folder-name).
	Hint string
}

// SplitPath separates the file from the item a settings path names
// ("/home/me/.vulnetix/belai/settings.json#my-repo").
func SplitPath(p string) (file, name string) {
	if i := strings.LastIndexByte(p, '#'); i >= 0 {
		return p[:i], p[i+1:]
	}
	return p, ""
}

// ErrOutside is the refusal for a path that is not under a place this host scans.
var ErrOutside = errors.New("that path is not in a place this host scans")

// Resolve checks that path is exactly a file Scan would list for the kind: under
// one of the roots (re-derived from the host's own harness list and trusted
// repositories), of the root's shape, and reached through no symbolic link below
// the root. It reads nothing.
func Resolve(roots []Root, kind agentimport.Kind, p string) (Resolved, error) {
	file, name := SplitPath(p)
	if file == "" || len(p) > MaxPathBytes || strings.ContainsRune(p, 0) || !filepath.IsAbs(file) || filepath.Clean(file) != file {
		return Resolved{}, ErrOutside
	}
	for _, r := range roots {
		if r.Kind != kind || r.Unsupported || r.Shape == "" {
			continue
		}
		switch r.Shape {
		case shapeSettings:
			if file == r.Path && name != "" {
				if err := noLink(r.Path, r.Path); err != nil {
					return Resolved{}, err
				}
				return Resolved{Root: r, File: file, Name: name, Hint: name}, nil
			}
			continue
		case shapeJSONKey, shapeJSONFile:
			// The file is the root. A settings file names its key ("#hooks"); a
			// hooks file names nothing.
			want := ""
			if r.Shape == shapeJSONKey {
				want = r.Key
			}
			if file == r.Path && name == want {
				if err := noLink(r.Path, r.Path); err != nil {
					return Resolved{}, err
				}
				return Resolved{Root: r, File: file, Name: name, Hint: HookName(r)}, nil
			}
			continue
		}
		if name != "" {
			continue
		}
		rel, err := filepath.Rel(r.Path, file)
		if err != nil || rel == "." || !filepath.IsLocal(rel) {
			continue
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if !shapeAccepts(r, parts) {
			continue
		}
		if err := noLink(r.Path, file); err != nil {
			return Resolved{}, err
		}
		hint := ""
		if r.Shape == shapeMD && len(parts) == 2 {
			hint = parts[0] + "-" + strings.TrimSuffix(parts[1], ".md")
		}
		if r.Shape == shapeHookBundle {
			hint = parts[0]
		}
		return Resolved{Root: r, File: file, Hint: hint}, nil
	}
	return Resolved{}, ErrOutside
}

// shapeAccepts reports whether the path below a root (as segments) is a file of
// the root's shape.
func shapeAccepts(r Root, parts []string) bool {
	last := parts[len(parts)-1]
	if strings.HasPrefix(last, ".") && r.Shape != shapeDoc {
		return false
	}
	switch r.Shape {
	case shapeMD:
		return (len(parts) == 1 || len(parts) == 2) && strings.HasSuffix(last, ".md") && !strings.HasPrefix(parts[0], ".")
	case shapeSkill:
		return len(parts) == 2 && last == "SKILL.md" && !strings.HasPrefix(parts[0], ".")
	case shapeDoc:
		if len(parts) != 1 {
			return false
		}
		for _, f := range r.Files {
			if f == last {
				return true
			}
		}
		return false
	case shapeProcess:
		return len(parts) == 1 && (strings.HasSuffix(last, ".json") || strings.HasSuffix(last, ".sh"))
	case shapeProfile:
		return len(parts) == 1 && (strings.HasSuffix(last, ".json") || strings.HasSuffix(last, ".md"))
	case shapeCrew:
		return len(parts) == 1 && strings.HasSuffix(last, ".json")
	case shapeHookBundle:
		return len(parts) == 2 && last == libitem.HookDefinitionFile && hooks.ValidBundleName(parts[0])
	}
	return false
}

// HookName is the library name a hooks file is offered under, made from the
// harness and the scope: claude-code-user, codex-user, claude-code-<repo> for a
// project file, with -local for a settings.local.json. It keeps to the library's
// name rule. A Belai bundle keeps the name of its directory (not made here).
func HookName(r Root) string {
	name := r.Harness
	if r.Scope == ScopeUser {
		name += "-user"
	} else {
		repo := r.RepoName
		if repo == "" {
			repo = filepath.Base(r.Repo)
		}
		suffix := ""
		if strings.Contains(filepath.Base(r.Path), ".local.") {
			suffix = "-local"
		}
		room := libitem.MaxNameBytes - len(name) - 1 - len(suffix)
		if room < 1 {
			room = 1
		}
		slug := nameSlug(repo)
		if len(slug) > room {
			slug = strings.TrimRight(slug[:room], "-._")
			if slug == "" {
				slug = "repo"
			}
		}
		name += "-" + slug + suffix
	}
	if len(name) > libitem.MaxNameBytes {
		name = strings.TrimRight(name[:libitem.MaxNameBytes], "-._")
	}
	return name
}

// nameSlug folds text to the characters a library name takes: lower-case
// letters and digits, with . _ - inside, starting with a letter or digit.
func nameSlug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.TrimLeft(b.String(), "-._")
	out = strings.TrimRight(out, "-")
	if out == "" {
		return "repo"
	}
	return out
}

// ImportOptions are the options the importer reads a resolved item with: the name
// to give it and, for a hook, where its scripts may be read from.
func ImportOptions(res Resolved) agentimport.Options { return importOptions(res.Root, res.Hint) }

func importOptions(r Root, hint string) agentimport.Options {
	o := agentimport.Options{Name: hint}
	if r.Kind == agentimport.KindHook {
		o.Hook = agentimport.HookContext{Home: r.Home, RepoRoot: r.Repo, Roots: r.ScriptRoots, Settings: r.Shape == shapeJSONKey}
	}
	return o
}

// noLink checks that root and every part of file below it is not a symbolic link.
func noLink(root, file string) error {
	rel, err := filepath.Rel(root, file)
	if err != nil {
		return ErrOutside
	}
	cur := root
	segs := []string{""}
	if rel != "." {
		segs = append(segs, strings.Split(filepath.ToSlash(rel), "/")...)
	}
	for _, s := range segs {
		if s != "" {
			cur = filepath.Join(cur, s)
		}
		fi, err := os.Lstat(cur)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("that path no longer exists")
			}
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return errors.New("that path goes through a symbolic link")
		}
	}
	return nil
}

// MaxPathBytes is the longest path a report carries.
const MaxPathBytes = 1024

// DefaultHome is the user's home directory.
func DefaultHome() (string, error) { return os.UserHomeDir() }

// KindSet reads the itemKind of a request or the -kind flag: empty is every kind.
func KindSet(word string) (map[agentimport.Kind]bool, error) {
	word = strings.TrimSpace(word)
	if word == "" {
		return nil, nil
	}
	k, ok := agentimport.ParseKind(word)
	if !ok {
		return nil, fmt.Errorf("%q is not an item kind (want one of %s)", sanitize.Line(word, 40), kindList())
	}
	return map[agentimport.Kind]bool{k: true}, nil
}

func kindList() string {
	var out []string
	for _, k := range agentimport.ItemKinds() {
		out = append(out, string(k))
	}
	return strings.Join(out, ", ")
}
