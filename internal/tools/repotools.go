package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/repoindex"
)

// repoTools builds the two repository tools that shell out (RepoFiles: git,
// RepoRead: cat) when the local index is non-empty. A tool that cannot work
// is never offered, so an empty index yields no tools. The index listing
// itself is the in-process RepoList tool, which needs no binary at all.
func repoTools(ix repoindex.Index) []nativeCommand {
	if ix.Empty() {
		return nil
	}
	return []nativeCommand{
		repoFilesTool(ix),
		repoReadTool(ix),
	}
}

// RepoList renders the local repository index in-process.
//
// It is the one native tool with no binary: its content is computed from the
// in-memory index, so there is no argv to build, no subprocess to start, and
// no capability to detect. It exists as a standalone Tool because the
// catalogue's Native type can only express "shell out to a fixed binary" —
// modelling the listing as a binary-less nativeCommand made the executor
// resolve an empty binary to the lower-cased name and run
// exec.Command("repos"), which fails on every machine and pipes the
// rendered listing to nothing.
type RepoList struct {
	ix repoindex.Index
}

// Definition returns the static tool metadata.
func (r *RepoList) Definition() Definition {
	return Definition{
		Name:        "Repos",
		Description: "List locally available git repositories. Use this first to discover which repositories exist on this machine before calling RepoFiles or RepoRead.",
		Properties: map[string]Property{
			"owner": stringProp("Optional remote owner to filter by (e.g. \"Vulnetix\")."),
		},
	}
}

// Kind returns the native read-only kind. The listing is harness-composed
// from the index — a list of "host/owner/name path" lines — so it is shaped,
// controlled output that is sanitised like Grep/Glob/LS results rather than
// classified like arbitrary file bytes.
func (r *RepoList) Kind() Kind { return KindNative }

// Subject returns the permission-rule subject: the owner filter, when given.
func (r *RepoList) Subject(args map[string]any) string {
	s, _ := argString(args, "owner")
	return s
}

// Execute renders the index: every entry, or the entries of one owner,
// sorted by their rendered form. A filter that matches nothing says so
// explicitly, because an empty answer would read as "no repositories at
// all" rather than "none for this owner".
func (r *RepoList) Execute(_ context.Context, args map[string]any) (Result, error) {
	owner, _ := argString(args, "owner")
	var entries []repoindex.Entry
	if owner != "" {
		entries = r.ix.Owner(owner)
	} else {
		entries = r.ix.Entries()
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].String() < entries[j].String() })
	if len(entries) == 0 {
		if owner != "" {
			return NativeResult(fmt.Sprintf("no repositories owned by %q in the local index", owner)), nil
		}
		return NativeResult("no repositories in the local index"), nil
	}
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.String() + "\n")
	}
	return NativeResult(b.String()), nil
}

// repoFilesTool lists files in a local checkout using git ls-files.
func repoFilesTool(ix repoindex.Index) nativeCommand {
	return nativeCommand{
		name:     "RepoFiles",
		binary:   "git",
		desc:     "List files in a locally available git repository. Resolve the repository name with Repos first.",
		required: []string{"repo"},
		props: map[string]Property{
			"repo": {Type: "string", Description: "Repository reference: \"owner/repo\" or a bare \"repo\" name when unique."},
			"glob": {Type: "string", Description: "Optional path glob passed to git ls-files (e.g. \"*.go\")."},
		},
		build: func(_ string, args map[string]any) ([]string, string, error) {
			entry, err := lookupRepo(ix, args)
			if err != nil {
				return nil, "", err
			}
			glob, _ := argString(args, "glob")
			if err := gateQuery(glob); err != nil {
				return nil, "", err
			}
			argv := []string{"-C", entry.Path, "ls-files"}
			if glob != "" {
				argv = append(argv, "--", glob)
			}
			return argv, "", nil
		},
		subject: func(args map[string]any) string { s, _ := argString(args, "repo"); return s },
	}
}

// repoReadTool reads a file from a local checkout. It shells out to cat with
// a sanitised absolute path so the result can be marked KindRead, signalling
// that it carries arbitrary file bytes that must be classified. The path
// argument is relative to the checkout, not to the session working directory,
// so it is exempt from the Cd rebase.
func repoReadTool(ix repoindex.Index) nativeCommand {
	return nativeCommand{
		name:         "RepoRead",
		binary:       "cat",
		kind:         KindRead,
		noPathRebase: true,
		desc:         "Read a file from a locally available git repository. Resolve the repository name with Repos first.",
		required:     []string{"repo", "path"},
		props: map[string]Property{
			"repo": {Type: "string", Description: "Repository reference: \"owner/repo\" or a bare \"repo\" name when unique."},
			"path": {Type: "string", Description: "File path inside the repository (e.g. \".github/workflows/ci.yml\"). Relative to the repository checkout, not the session working directory."},
		},
		build: func(_ string, args map[string]any) ([]string, string, error) {
			entry, err := lookupRepo(ix, args)
			if err != nil {
				return nil, "", err
			}
			p, _ := argString(args, "path")
			rel, err := SanitizePath(entry.Path, p)
			if err != nil {
				return nil, "", err
			}
			abs := filepath.Join(entry.Path, rel)
			return []string{abs}, "", nil
		},
		subject: func(args map[string]any) string { s, _ := argString(args, "path"); return s },
	}
}

// lookupRepo resolves the repository reference in args against the index. A
// miss returns an error naming the repositories that are available, so the
// model learns to use GH for anything not on disk.
func lookupRepo(ix repoindex.Index, args map[string]any) (repoindex.Entry, error) {
	ref, _ := argString(args, "repo")
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return repoindex.Entry{}, fmt.Errorf("missing repo argument")
	}
	if e, ok := ix.Lookup(ref); ok {
		return e, nil
	}
	return repoindex.Entry{}, fmt.Errorf("repository %q not found locally. Available: %s", ref, availableRepos(ix))
}

// availableRepos returns a comma-separated list of owner/repo refs in the
// index for error messages.
func availableRepos(ix repoindex.Index) string {
	entries := ix.Entries()
	var refs []string
	for _, e := range entries {
		if e.Owner != "" && e.Name != "" {
			refs = append(refs, e.Owner+"/"+e.Name)
		} else if e.Name != "" {
			refs = append(refs, e.Name)
		}
	}
	if len(refs) == 0 {
		return "(none)"
	}
	return strings.Join(refs, ", ")
}
