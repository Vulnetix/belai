package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/vulnetix/belai/internal/agentfiles"
	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/knowledge"
	"github.com/vulnetix/belai/internal/sanitize"
)

// agentFiles implements `belai agent files`: the files an agent profile carries
// (docs/knowledge.md, "A profile's own files"). A file attached here is kept in
// the profile's own directory under the state directory, listed in the profile's
// knowledge.paths, and goes with the profile in a library backup, so the agent
// restores on another host with the documents it was written around.
//
//	belai agent files NAME                    list what the profile names and where each is found
//	belai agent files add NAME FILE [-as P]   attach FILE (listed as P, default its name)
//	belai agent files rm NAME PATH            detach a file and stop listing it
//	belai agent files adopt DIR [-dry-run]    attach each DIR/<agent>.md to the agent of that name
func agentFiles(ctx context.Context, fs *flag.FlagSet, rest []string, stdout io.Writer, asJSON *bool) (int, error) {
	if len(rest) > 0 {
		switch rest[0] {
		case "add":
			return agentFilesAdd(fs, rest[1:], stdout)
		case "rm":
			return agentFilesRemove(fs, rest[1:], stdout)
		case "adopt":
			return agentFilesAdopt(fs, rest[1:], stdout)
		}
	}
	if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 1 {
		return 2, errors.New("usage: belai agent files NAME | add NAME FILE [-as PATH] | rm NAME PATH | adopt DIR [-dry-run]")
	}
	p, err := agentprofile.Load(fs.Arg(0))
	if err != nil {
		return 1, err
	}
	owned, err := agentfiles.Owned(p.ID)
	if err != nil {
		return 1, err
	}
	have := map[string]bool{}
	for _, f := range owned {
		have[f.Path] = true
	}
	wd, _ := os.Getwd()
	captured, _, _ := agentfiles.Capture(ctx, p, []string{repoRoot(wd)})
	type row struct {
		Path   string `json:"path"`
		Where  string `json:"where"`
		Listed bool   `json:"listed"`
	}
	var rows []row
	for _, listed := range agentfiles.ListedPaths(p) {
		raw := strings.TrimRight(listed, "/")
		where := "missing"
		for _, f := range captured {
			if f.Path == raw || strings.HasPrefix(f.Path, raw+"/") {
				where = "readable here"
				if have[f.Path] {
					where = "in the profile's own files"
				}
				break
			}
		}
		rows = append(rows, row{Path: listed, Where: where, Listed: true})
	}
	for _, f := range owned {
		listed := false
		for _, r := range rows {
			if r.Path == f.Path || strings.HasPrefix(f.Path, strings.TrimRight(r.Path, "/")+"/") {
				listed = true
			}
		}
		if !listed {
			rows = append(rows, row{Path: f.Path, Where: "in the profile's own files, but no path lists it", Listed: false})
		}
	}
	if *asJSON {
		return 0, jsonOut(stdout, rows)
	}
	if len(rows) == 0 {
		fmt.Fprintf(stdout, "%s names no files: add one with `belai agent files add %s FILE`\n", p.Name, p.Name)
		return 0, nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "PATH\tWHERE")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\n", sanitize.Line(r.Path, 200), r.Where)
	}
	return 0, tw.Flush()
}

// attach copies content into the profile's own files under the listed path and
// lists the path in knowledge.paths, saving the profile.
func attach(p agentprofile.AgentProfile, listed string, content []byte) error {
	if !agentprofile.ValidID(p.ID) {
		return fmt.Errorf("profile %q has no id yet: save it again (belai agent import -force) to give it one", p.Name)
	}
	if err := agentfiles.Add(p.ID, listed, content); err != nil {
		return err
	}
	k := p.Knowledge
	if k == nil {
		k = &agentprofile.KnowledgeSpec{}
	}
	for _, have := range k.Paths {
		if have == listed {
			return nil
		}
	}
	k.Paths = append(append([]string(nil), k.Paths...), listed)
	p.Knowledge = k
	_, err := agentprofile.Save(p)
	return err
}

func agentFilesAdd(fs *flag.FlagSet, rest []string, stdout io.Writer) (int, error) {
	as := fs.String("as", "", "the path the profile lists for the file (default: the file's name)")
	if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 2 {
		return 2, errors.New("usage: belai agent files add NAME FILE [-as PATH]")
	}
	p, err := agentprofile.Load(fs.Arg(0))
	if err != nil {
		return 1, err
	}
	data, err := os.ReadFile(fs.Arg(1))
	if err != nil {
		return 1, err
	}
	listed := *as
	if listed == "" {
		listed = filepath.Base(fs.Arg(1))
	}
	if _, ok := knowledge.OwnedRelPath(listed); !ok {
		return 1, fmt.Errorf("%q is not a path a profile can keep", listed)
	}
	if agentfiles.HoldsSecret(data) {
		return 1, errors.New("that file holds a private key or a token, so it is not attached")
	}
	if err := attach(p, listed, data); err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "attached %s to %s (%d bytes)\n", sanitize.Line(listed, 200), p.Name, len(data))
	return 0, nil
}

func agentFilesRemove(fs *flag.FlagSet, rest []string, stdout io.Writer) (int, error) {
	if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 2 {
		return 2, errors.New("usage: belai agent files rm NAME PATH")
	}
	p, err := agentprofile.Load(fs.Arg(0))
	if err != nil {
		return 1, err
	}
	listed := fs.Arg(1)
	found, err := agentfiles.RemoveFile(p.ID, listed)
	if err != nil {
		return 1, err
	}
	changed := false
	if p.Knowledge != nil {
		var keep []string
		for _, have := range p.Knowledge.Paths {
			if have == listed {
				changed = true
				continue
			}
			keep = append(keep, have)
		}
		if changed {
			if len(keep) == 0 {
				p.Knowledge = nil
			} else {
				p.Knowledge.Paths = keep
			}
		}
	}
	if !found && !changed {
		return 1, fmt.Errorf("%s has no file %q", p.Name, listed)
	}
	if changed {
		if _, err := agentprofile.Save(p); err != nil {
			return 1, err
		}
	}
	fmt.Fprintf(stdout, "detached %s from %s\n", sanitize.Line(listed, 200), p.Name)
	return 0, nil
}

// agentFilesAdopt attaches each DIR/<agent>.md to the stored agent of that name,
// listed as <DIR's name>/<agent>.md. It is for the markdown sources an agent was
// written from, kept in a directory beside the profiles: nothing ties them to the
// agent but the file name, so a backup missed them.
func agentFilesAdopt(fs *flag.FlagSet, rest []string, stdout io.Writer) (int, error) {
	dry := fs.Bool("dry-run", false, "say what would be attached without doing it")
	if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 1 {
		return 2, errors.New("usage: belai agent files adopt DIR [-dry-run]")
	}
	dir, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return 1, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 1, err
	}
	n := 0
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".md")
		if !ok || !e.Type().IsRegular() {
			continue
		}
		p, err := agentprofile.Load(name)
		if err != nil || p.Builtin {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		listed := filepath.Base(dir) + "/" + e.Name()
		if _, ok := knowledge.OwnedRelPath(listed); !ok || agentfiles.HoldsSecret(data) || len(data) == 0 || len(data) > agentfiles.MaxFileBytes || bytes.IndexByte(data, 0) >= 0 {
			fmt.Fprintf(stdout, "skipped %s: not a file a profile can keep\n", sanitize.Line(e.Name(), 100))
			continue
		}
		if *dry {
			fmt.Fprintf(stdout, "would attach %s to %s\n", listed, p.Name)
			n++
			continue
		}
		if err := attach(p, listed, data); err != nil {
			fmt.Fprintf(stdout, "skipped %s: %s\n", sanitize.Line(e.Name(), 100), sanitize.Line(err.Error(), 160))
			continue
		}
		fmt.Fprintf(stdout, "attached %s to %s\n", listed, p.Name)
		n++
	}
	if n == 0 {
		fmt.Fprintf(stdout, "no file in %s is named after a stored agent\n", dir)
	}
	return 0, nil
}

// agentCrew implements `belai agent crew import FILE [-force] | export NAME |
// delete NAME`: a crew's definition as the JSON the library keeps, so a crew can
// be written by hand or from the website's crew editor and moved between hosts
// without a backup request.
func agentCrew(fs *flag.FlagSet, rest []string, stdout io.Writer) (int, error) {
	usage := errors.New("usage: belai agent crew import FILE [-force] | export NAME | delete NAME")
	if len(rest) == 0 {
		return 2, usage
	}
	cmd, rest := rest[0], rest[1:]
	switch cmd {
	case "import":
		force := fs.Bool("force", false, "replace an existing crew of the same name")
		if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 1 {
			return 2, usage
		}
		data, err := os.ReadFile(fs.Arg(0))
		if err != nil {
			return 1, err
		}
		var c agentprofile.Crew
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return 1, fmt.Errorf("%s: %w", fs.Arg(0), err)
		}
		if existing, err := agentprofile.LoadCrew(c.Name); err == nil && !*force {
			return 1, fmt.Errorf("a crew named %q exists; pass -force to replace it", existing.Name)
		}
		if old := storedCrewNamed(c.Name); old != nil && c.ID == "" {
			// A replace keeps the crew's identity, so the library still sees one crew.
			c.ID = old.ID
		}
		path, err := agentprofile.SaveCrew(c)
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "saved %s\n", path)
		return 0, nil
	case "export":
		if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 1 {
			return 2, usage
		}
		if _, err := agentprofile.EnsureCrewIDs(); err != nil {
			return 1, err
		}
		c, err := agentprofile.LoadCrew(fs.Arg(0))
		if err != nil {
			return 1, err
		}
		js, err := c.CanonicalJSON()
		if err != nil {
			return 1, err
		}
		_, err = stdout.Write(js)
		return 0, err
	case "delete":
		if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 1 {
			return 2, usage
		}
		if err := agentprofile.DeleteCrew(fs.Arg(0)); err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "deleted crew %s\n", sanitize.Line(fs.Arg(0), 64))
		return 0, nil
	}
	return 2, usage
}

func storedCrewNamed(name string) *agentprofile.Crew {
	for _, c := range agentprofile.StoredCrews() {
		if c.Name == name {
			c := c
			return &c
		}
	}
	return nil
}
