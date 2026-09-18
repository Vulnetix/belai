package tools

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vulnetix/signet/internal/repoindex"
)

// repoTools builds the three repository-native tools when the local index is
// non-empty. A tool that cannot work is never offered, so an empty index
// yields no tools.
func repoTools(ix repoindex.Index) []nativeCommand {
	if ix.Empty() {
		return nil
	}
	return []nativeCommand{
		repoListTool(ix),
		repoFilesTool(ix),
		repoReadTool(ix),
	}
}

// repoListTool renders the local repository index.
func repoListTool(ix repoindex.Index) nativeCommand {
	return nativeCommand{
		name: "Repos",
		desc: "List locally available git repositories. Use this first to discover which repositories exist on this machine before calling RepoFiles or RepoRead.",
		props: map[string]Property{
			"owner": stringProp("Optional remote owner to filter by (e.g. \"Vulnetix\")."),
		},
		build: func(_ string, args map[string]any) ([]string, string, error) {
			owner, _ := argString(args, "owner")
			var entries []repoindex.Entry
			if owner != "" {
				entries = ix.Owner(owner)
			} else {
				entries = ix.Entries()
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].String() < entries[j].String() })
			var b strings.Builder
			for _, e := range entries {
				b.WriteString(e.String() + "\n")
			}
			return nil, b.String(), nil
		},
		subject: func(args map[string]any) string { s, _ := argString(args, "owner"); return s },
	}
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
// that it carries arbitrary file bytes that must be classified.
func repoReadTool(ix repoindex.Index) nativeCommand {
	return nativeCommand{
		name:     "RepoRead",
		binary:   "cat",
		kind:     KindRead,
		desc:     "Read a file from a locally available git repository. Resolve the repository name with Repos first.",
		required: []string{"repo", "path"},
		props: map[string]Property{
			"repo": {Type: "string", Description: "Repository reference: \"owner/repo\" or a bare \"repo\" name when unique."},
			"path": {Type: "string", Description: "File path inside the repository (e.g. \".github/workflows/ci.yml\")."},
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
