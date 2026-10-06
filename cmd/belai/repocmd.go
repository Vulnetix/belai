package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/repos"
)

// repoOptions is how git runs for `belai repo`; production is the zero value. Tests
// replace it to point a url at a local repository.
var repoOptions = func() repos.Options { return repos.Options{} }

// repoUsageExtra is what `belai repo` adds to the generic library commands.
const repoUsageExtra = `
  sync [NAME]               clone or update each enabled repository (or NAME) under
                            the repos directory; a branch is fast-forwarded, a tag
                            or commit is checked out detached, and a dirty tree or
                            a diverged branch is refused, never reset
  status [-json] [NAME]     where each repository stands, from the last sync (no network)`

// repoCommand handles the verbs `belai repo` has beyond import and export, and its
// own list. handled is false for every other verb.
func repoCommand(ctx context.Context, cmd string, rest []string, stdout, stderr io.Writer) (code int, handled bool, err error) {
	switch cmd {
	case "list", "sync", "status":
	default:
		return 0, false, nil
	}
	fs := flag.NewFlagSet("repo "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	if cmd == "sync" && fs.Lookup("json") != nil {
		// sync prints lines; -json is for list and status.
		fs = flag.NewFlagSet("repo sync", flag.ContinueOnError)
		fs.SetOutput(stderr)
	}
	if err := parseInterleaved(fs, rest); err != nil || fs.NArg() > 1 {
		return 2, true, fmt.Errorf("usage: belai repo %s%s", cmd, map[bool]string{true: " [NAME]", false: ""}[cmd != "list"])
	}
	s, err := config.LoadGlobal()
	if err != nil {
		return 1, true, err
	}
	selected, err := selectRepos(s, fs.Arg(0))
	if err != nil {
		return 1, true, err
	}
	switch cmd {
	case "list":
		return repoList(selected, *asJSON, stdout)
	case "status":
		return repoStatus(ctx, selected, *asJSON, stdout)
	}
	return repoSync(ctx, selected, fs.Arg(0) != "", stdout, stderr)
}

func selectRepos(s config.Settings, name string) ([]config.GitRepo, error) {
	if name == "" {
		return s.GitRepos, nil
	}
	r, ok := s.GitRepoNamed(name)
	if !ok {
		return nil, fmt.Errorf("no repository named %q in the repos setting (see `belai repo list`)", name)
	}
	return []config.GitRepo{r}, nil
}

func repoList(rs []config.GitRepo, asJSON bool, stdout io.Writer) (int, bool, error) {
	if asJSON {
		docs := make([]map[string]any, 0, len(rs))
		for _, r := range rs {
			docs = append(docs, libitem.RepoDocument(r))
		}
		return 0, true, jsonOut(stdout, docs)
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tURL\tREF\tDIR\tENABLED")
	for _, r := range rs {
		ref := ""
		if len(r.Refs) > 0 {
			ref = r.Refs[0].Kind + ":" + r.Refs[0].Name
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%v\n", cleanLog(r.Name), cleanLog(r.URL), cleanLog(ref), cleanLog(r.Subdir()), r.IsEnabled())
	}
	return 0, true, tw.Flush()
}

func repoStatus(ctx context.Context, rs []config.GitRepo, asJSON bool, stdout io.Writer) (int, bool, error) {
	var sts []repos.Status
	for _, r := range rs {
		sts = append(sts, repos.StatusOf(ctx, r, repoOptions()))
	}
	if asJSON {
		type row struct {
			Name   string `json:"name"`
			Path   string `json:"path"`
			State  string `json:"state"`
			Detail string `json:"detail,omitempty"`
			Commit string `json:"commit,omitempty"`
			Dirty  bool   `json:"dirty"`
		}
		out := make([]row, 0, len(sts))
		for _, st := range sts {
			out = append(out, row{st.Name, st.Path, st.State, st.Detail, st.Commit, st.Dirty})
		}
		return 0, true, jsonOut(stdout, out)
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATE\tCOMMIT\tDETAIL")
	for _, st := range sts {
		detail := st.Detail
		if st.Dirty {
			detail = strings.TrimSpace(detail + " (local changes to tracked files)")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", cleanLog(st.Name), cleanLog(st.State), st.Commit, cleanLog(detail))
	}
	return 0, true, tw.Flush()
}

func repoSync(ctx context.Context, rs []config.GitRepo, named bool, stdout, stderr io.Writer) (int, bool, error) {
	failed := 0
	for _, r := range rs {
		if !r.IsEnabled() && !named {
			fmt.Fprintf(stdout, "%s\tskipped (disabled)\n", cleanLog(r.Name))
			continue
		}
		res, err := repos.Sync(ctx, r, repoOptions())
		if err != nil {
			if errors.Is(err, repos.ErrDisabled) && named {
				return 1, true, fmt.Errorf("%s is disabled; set enabled to true to sync it", r.Name)
			}
			failed++
			fmt.Fprintf(stderr, "%s\tfailed: %s\n", cleanLog(r.Name), cleanLog(err.Error()))
			continue
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", cleanLog(res.Name), res.Action, cleanLog(res.Ref), res.Commit, cleanLog(res.Path))
		for _, n := range res.Notes {
			fmt.Fprintf(stdout, "%s\tnote: %s\n", cleanLog(res.Name), cleanLog(n))
		}
	}
	if len(rs) == 0 {
		fmt.Fprintln(stdout, "no repositories in the repos setting; add one with `belai repo import FILE`")
	}
	if failed > 0 {
		return 1, true, fmt.Errorf("%d of %d repositories failed", failed, len(rs))
	}
	return 0, true, nil
}
