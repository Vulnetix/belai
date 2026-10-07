// Command gen-harnesses builds internal/harness/harnesses.json from the cli
// repository's three harness lists and the hand-written overlay next to this
// file. Run it from the belai root with the cli checkout beside it:
//
//	go run ./tools/gen-harnesses -cli ../cli -out internal/harness/harnesses.json
//
// The sources:
//
//   - internal/agent/hosts.go: ten verified hosts (name, user and project skill
//     directories, detect paths).
//   - cmd/skills.go, the unexported agentDirs map: about 65 ids and their user
//     skill directories. It is read as Go source, not imported.
//   - internal/aibom/catalog/tools.json: project-scope globs per tool for
//     skills, commands, agents and prompts, plus fixed instruction files.
//
// overlay.json adds what no source records: user-scope commands, agents,
// prompts and instruction directories, file shapes, the agentimport format and
// the id mapping between the sources. The output is sorted and indented, so two
// runs over the same inputs give identical bytes.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/vulnetix/belai/internal/harness"
)

const defaultFormat = "generic-md"

// Overlay is the hand-written part of the registry.
type Overlay struct {
	// IDMap maps an id used by one cli source to the canonical id.
	IDMap map[string]string `json:"idMap"`
	// Exclude names tools.json entries that are not harnesses, with the reason.
	Exclude map[string]string `json:"exclude"`
	// Harnesses is keyed by canonical id.
	Harnesses map[string]OverlayHarness `json:"harnesses"`
}

// OverlayHarness adds to or replaces what the cli sources give for one harness.
type OverlayHarness struct {
	Name   string                    `json:"name,omitempty"`
	Detect []string                  `json:"detect,omitempty"`
	Format string                    `json:"format,omitempty"`
	Dirs   map[string]OverlayKindDir `json:"dirs,omitempty"`
}

// OverlayKindDir is one kind of one harness. Directories are added to the
// generated ones unless Replace is set.
type OverlayKindDir struct {
	User    []string `json:"user,omitempty"`
	Project []string `json:"project,omitempty"`
	Shape   string   `json:"shape,omitempty"`
	Files   []string `json:"files,omitempty"`
	Replace bool     `json:"replace,omitempty"`
}

type toolInfo struct {
	ID    string              `json:"id"`
	Name  string              `json:"name"`
	Type  string              `json:"type"`
	Paths map[string][]string `json:"paths"`
}

type host struct {
	id, name                    string
	skillDirs, projectSkillDirs []string
	detect                      []string
}

// kindOfCategory maps a tools.json path category to a harness kind.
var kindOfCategory = map[string]string{
	"skills":   harness.Skill,
	"commands": harness.Command,
	"agents":   harness.Agent,
	"prompts":  harness.Prompt,
}

var defaultShape = map[string]string{
	harness.Skill:    harness.SkillDir,
	harness.Command:  harness.MDFile,
	harness.Agent:    harness.MDFile,
	harness.Prompt:   harness.MDFile,
	harness.Document: harness.DocFile,
}

// harnessTypes are the tools.json types that are a harness a user runs.
var harnessTypes = map[string]bool{"cli-agent": true, "ide": true, "ide-extension": true}

func main() {
	cli := flag.String("cli", "../cli", "path to the cli repository checkout")
	out := flag.String("out", "internal/harness/harnesses.json", "file to write")
	overlayPath := flag.String("overlay", "tools/gen-harnesses/overlay.json", "hand-written overlay")
	table := flag.String("table", "", "also write the markdown table for docs/harnesses.md here (- for stdout)")
	flag.Parse()
	if err := run(*cli, *overlayPath, *out, *table); err != nil {
		fmt.Fprintln(os.Stderr, "gen-harnesses:", err)
		os.Exit(1)
	}
}

func run(cli, overlayPath, out, table string) error {
	hs, ov, err := Build(cli, overlayPath)
	if err != nil {
		return err
	}
	data, err := Marshal(hs)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		return err
	}
	if table == "" {
		return nil
	}
	md := Table(hs, ov)
	if table == "-" {
		_, err = os.Stdout.WriteString(md)
		return err
	}
	return os.WriteFile(table, []byte(md), 0o644)
}

// Marshal renders the registry as stable indented JSON with a trailing newline.
func Marshal(hs []harness.Harness) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(hs); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Build merges the three cli sources and the overlay.
func Build(cli, overlayPath string) ([]harness.Harness, Overlay, error) {
	var ov Overlay
	raw, err := os.ReadFile(overlayPath)
	if err != nil {
		return nil, ov, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ov); err != nil {
		return nil, ov, fmt.Errorf("%s: %w", overlayPath, err)
	}
	hosts, err := parseHosts(filepath.Join(cli, "internal", "agent", "hosts.go"))
	if err != nil {
		return nil, ov, err
	}
	dirs, err := parseAgentDirs(filepath.Join(cli, "cmd", "skills.go"))
	if err != nil {
		return nil, ov, err
	}
	tools, err := parseTools(filepath.Join(cli, "internal", "aibom", "catalog", "tools.json"))
	if err != nil {
		return nil, ov, err
	}
	canon := func(id string) string {
		if c, ok := ov.IDMap[id]; ok {
			return c
		}
		return id
	}

	byID := map[string]*harness.Harness{}
	aliases := map[string]map[string]bool{}
	get := func(srcID string) *harness.Harness {
		id := canon(srcID)
		h, ok := byID[id]
		if !ok {
			h = &harness.Harness{ID: id, Detect: []string{}, Dirs: map[string]harness.KindDirs{}}
			byID[id] = h
			aliases[id] = map[string]bool{}
		}
		if srcID != id {
			aliases[id][srcID] = true
		}
		return h
	}
	names := map[string]string{}

	// Source 1: verified hosts.
	for _, ho := range hosts {
		h := get(ho.id)
		names[h.ID] = ho.name
		addDirs(h, harness.Skill, ho.skillDirs, ho.projectSkillDirs)
		h.Detect = appendUniq(h.Detect, ho.detect...)
	}
	// Source 2: agentDirs, user skill directories. An entry without a ~/ prefix
	// is repository-relative.
	for _, id := range sortedKeys(dirs) {
		h := get(id)
		addDirs(h, harness.Skill, dirs[id], nil)
	}
	// Source 3: tools.json project paths.
	for _, t := range tools {
		if !harnessTypes[t.Type] {
			continue
		}
		if _, skip := ov.Exclude[t.ID]; skip {
			continue
		}
		h := get(t.ID)
		if _, ok := names[h.ID]; !ok {
			names[h.ID] = t.Name
		}
		for _, cat := range []string{"skills", "commands", "agents", "prompts"} {
			var proj []string
			for _, g := range t.Paths[cat] {
				if d, ok := globDir(g); ok {
					proj = append(proj, d)
				}
			}
			addDirs(h, kindOfCategory[cat], nil, proj)
		}
		var docDirs, docFiles []string
		for _, f := range t.Paths["instructions"] {
			if strings.ContainsAny(f, "*?[") {
				continue
			}
			docDirs = appendUniq(docDirs, path.Dir(f))
			docFiles = appendUniq(docFiles, path.Base(f))
		}
		if len(docFiles) > 0 {
			kd := h.Dirs[harness.Document]
			kd.Project = appendUniq(kd.Project, docDirs...)
			kd.Files = appendUniq(kd.Files, docFiles...)
			h.Dirs[harness.Document] = kd
		}
	}
	// The overlay may name a harness no cli source lists.
	for _, id := range sortedKeys(ov.Harnesses) {
		get(id)
	}
	for id := range ov.IDMap {
		if _, ok := byID[ov.IDMap[id]]; !ok {
			return nil, ov, fmt.Errorf("idMap %q -> %q: no such harness", id, ov.IDMap[id])
		}
	}

	// Shared user skill directories give no detect path of their own.
	userUse := map[string]int{}
	for _, h := range byID {
		for _, d := range h.Dirs[harness.Skill].User {
			userUse[d]++
		}
	}

	var list []harness.Harness
	for _, id := range sortedKeys(byID) {
		h := byID[id]
		o, hand := ov.Harnesses[id]
		if len(h.Dirs) == 0 && !hand {
			// A tools.json entry that records no directory or file is not a harness
			// a scan can search.
			continue
		}
		if o.Name != "" {
			h.Name = o.Name
		} else if names[id] != "" {
			h.Name = names[id]
		} else {
			h.Name = humanise(id)
		}
		h.Format = defaultFormat
		if o.Format != "" {
			h.Format = o.Format
		}
		for kind, k := range o.Dirs {
			if _, ok := defaultShape[kind]; !ok {
				return nil, ov, fmt.Errorf("%s: unknown kind %q in overlay", id, kind)
			}
			kd := h.Dirs[kind]
			if k.Replace {
				kd = harness.KindDirs{}
			}
			kd.User = appendUniq(kd.User, k.User...)
			kd.Project = appendUniq(kd.Project, k.Project...)
			kd.Files = appendUniq(kd.Files, k.Files...)
			if k.Shape != "" {
				kd.Shape = k.Shape
			}
			h.Dirs[kind] = kd
		}
		for kind, kd := range h.Dirs {
			if kd.Shape == "" {
				kd.Shape = defaultShape[kind]
			}
			if kd.Shape != harness.DocFile {
				kd.Files = nil
			}
			h.Dirs[kind] = kd
		}
		switch {
		case len(o.Detect) > 0:
			h.Detect = append([]string{}, o.Detect...)
		case len(h.Detect) == 0:
			for _, d := range h.Dirs[harness.Skill].User {
				if userUse[d] == 1 && strings.HasSuffix(d, "/skills") {
					h.Detect = []string{strings.TrimSuffix(d, "/skills")}
					break
				}
			}
		}
		for a := range aliases[id] {
			h.Aliases = append(h.Aliases, a)
		}
		sort.Strings(h.Aliases)
		list = append(list, *h)
	}
	return list, ov, nil
}

// addDirs appends user and project directories to a kind. A user directory
// without a ~/ prefix is a repository-relative one.
func addDirs(h *harness.Harness, kind string, user, project []string) {
	kd := h.Dirs[kind]
	for _, d := range user {
		if strings.HasPrefix(d, "~/") {
			kd.User = appendUniq(kd.User, d)
		} else {
			kd.Project = appendUniq(kd.Project, cleanRel(d))
		}
	}
	for _, d := range project {
		kd.Project = appendUniq(kd.Project, cleanRel(d))
	}
	if len(kd.User)+len(kd.Project) > 0 || kd.Shape != "" {
		h.Dirs[kind] = kd
	}
}

func cleanRel(d string) string { return strings.TrimSuffix(path.Clean(d), "/") }

// globDir turns ".claude/commands/**" into ".claude/commands". A glob that is
// a file (skills-lock.json) or starts with ** is not a directory.
func globDir(g string) (string, bool) {
	d, ok := strings.CutSuffix(g, "/**")
	if !ok || strings.ContainsAny(d, "*?[") || d == "" {
		return "", false
	}
	return d, true
}

func appendUniq(dst []string, add ...string) []string {
	for _, a := range add {
		dup := false
		for _, d := range dst {
			if d == a {
				dup = true
				break
			}
		}
		if !dup {
			dst = append(dst, a)
		}
	}
	return dst
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func humanise(id string) string {
	parts := strings.Split(id, "-")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

func parseTools(file string) ([]toolInfo, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Tools []toolInfo `json:"tools"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return doc.Tools, nil
}

func parseGo(file string) (*ast.File, error) {
	return parser.ParseFile(token.NewFileSet(), file, nil, 0)
}

// varValue finds the composite literal assigned to a package-level var.
func varValue(f *ast.File, name string) *ast.CompositeLit {
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, s := range gd.Specs {
			vs := s.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if n.Name == name && i < len(vs.Values) {
					if cl, ok := vs.Values[i].(*ast.CompositeLit); ok {
						return cl
					}
				}
			}
		}
	}
	return nil
}

func lit(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(bl.Value)
	return s, err == nil
}

func litList(e ast.Expr) []string {
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	var out []string
	for _, el := range cl.Elts {
		if s, ok := lit(el); ok {
			out = append(out, s)
		}
	}
	return out
}

func parseHosts(file string) ([]host, error) {
	f, err := parseGo(file)
	if err != nil {
		return nil, err
	}
	cl := varValue(f, "Hosts")
	if cl == nil {
		return nil, fmt.Errorf("%s: no Hosts table", file)
	}
	var out []host
	for _, el := range cl.Elts {
		hc, ok := el.(*ast.CompositeLit)
		if !ok {
			continue
		}
		var h host
		for _, kv := range hc.Elts {
			kve, ok := kv.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, _ := kve.Key.(*ast.Ident)
			if key == nil {
				continue
			}
			switch key.Name {
			case "ID":
				h.id, _ = lit(kve.Value)
			case "Name":
				h.name, _ = lit(kve.Value)
			case "SkillDirs":
				h.skillDirs = litList(kve.Value)
			case "ProjectSkillDirs":
				h.projectSkillDirs = litList(kve.Value)
			case "Detect":
				h.detect = litList(kve.Value)
			}
		}
		if h.id == "" {
			return nil, errors.New("hosts.go: a host has no ID")
		}
		out = append(out, h)
	}
	return out, nil
}

func parseAgentDirs(file string) (map[string][]string, error) {
	f, err := parseGo(file)
	if err != nil {
		return nil, err
	}
	cl := varValue(f, "agentDirs")
	if cl == nil {
		return nil, fmt.Errorf("%s: no agentDirs map", file)
	}
	out := map[string][]string{}
	for _, el := range cl.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		id, ok := lit(kv.Key)
		if !ok {
			continue
		}
		out[id] = litList(kv.Value)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: agentDirs is empty", file)
	}
	return out, nil
}

// Table renders the supported harnesses for docs/harnesses.md.
func Table(hs []harness.Harness, ov Overlay) string {
	var b strings.Builder
	b.WriteString("| ID | Name | Format | Source | Commands | Agents | Prompts | Skills | Documents |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, h := range hs {
		src := "generated"
		if o, ok := ov.Harnesses[h.ID]; ok && len(o.Dirs) > 0 {
			src = "hand-written"
		}
		fmt.Fprintf(&b, "| `%s` | %s | `%s` | %s | %s | %s | %s | %s | %s |\n", h.ID, h.Name, h.Format, src,
			cell(h.Dirs[harness.Command]), cell(h.Dirs[harness.Agent]), cell(h.Dirs[harness.Prompt]),
			cell(h.Dirs[harness.Skill]), cell(h.Dirs[harness.Document]))
	}
	return b.String()
}

func cell(k harness.KindDirs) string {
	if k.Shape == harness.DocFile {
		return strings.Join(k.Files, ", ")
	}
	var parts []string
	for _, d := range k.User {
		parts = append(parts, "`"+d+"`")
	}
	for _, d := range k.Project {
		parts = append(parts, "`"+d+"`")
	}
	if len(parts) == 0 {
		return ""
	}
	s := strings.Join(parts, ", ")
	if k.Shape == harness.Unsupported {
		s += " (unsupported format)"
	}
	return s
}
