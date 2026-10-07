// Package libscan searches a host for library items kept by other agent
// harnesses (Claude Code, Cursor, Codex and the rest in internal/harness) and by
// Belai itself, in the user's directories and in the repositories the user has
// trusted, and reports each candidate with a verdict.
//
// A scan reads files through agentimport.ImportItem, the same converter
// `belai agent import` and a library import use, so a verdict is exactly what an
// import would do: an item is valid when the converter takes it whole, a warning
// when it had to drop or flag part of it, invalid when it refuses it. Nothing is
// written. Only names, paths, hashes, verdicts and short notes leave in the
// report; a document leaves only when the user imports that item.
//
// A hook is read like the rest, with one difference: its item carries the scripts
// its commands name (report field files), and its hash covers them. A settings file
// is read for its hooks key alone.
//
// The reader is bounded: it never follows a symbolic link, reads no credential
// file, caps the entries it looks at and stops at the context's deadline, in
// which case the report says it is partial.
package libscan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/agentimport"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/harness"
	"github.com/vulnetix/belai/internal/hooks"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/sanitize"
)

// Verdicts.
const (
	Valid   = "valid"
	Warning = "warning"
	Invalid = "invalid"
)

// DefaultBudget is how long a scan searches before it reports what it has found.
const DefaultBudget = 25 * time.Second

// Limits of a scan and its report.
const (
	// MaxItems is the most items a report carries.
	MaxItems = 2000
	// MaxNotes and MaxNoteBytes bound the notes of one item.
	// MaxDescriptionBytes bounds an item's description.
	MaxDescriptionBytes = 200
	MaxNotes            = 8
	MaxNoteBytes        = 160
	// MaxReportBytes is the largest report.
	MaxReportBytes = 1 << 20
	// maxDirEntries bounds how many entries of one directory are looked at.
	maxDirEntries = 500
	// maxExamined bounds the files a scan opens.
	maxExamined = 6000
	// maxLooked bounds the folders named for one repository.
	maxLooked = 128
)

// Note is one line of an item's conversion report.
type Note struct {
	Kind  string `json:"kind"`
	Field string `json:"field"`
	Text  string `json:"text"`
}

// Item is one candidate found.
type Item struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Harness string `json:"harness"`
	Format  string `json:"format"`
	Scope   string `json:"scope"`
	Repo    string `json:"repo,omitempty"`
	SHA256  string `json:"sha256"`
	Bytes   int    `json:"bytes"`
	// Description is the item's own description, cut to 200 bytes.
	Description string `json:"description,omitempty"`
	Verdict     string `json:"verdict"`
	Reason      string `json:"reason,omitempty"`
	Converted   bool   `json:"converted"`
	Notes       []Note `json:"notes"`
	// Files are the scripts a hook would carry, by bundle-relative path, at most
	// 32. Only a hook has them. Sha256 above covers the document and these.
	Files []ItemFile `json:"files,omitempty"`
}

// ItemFile is one script of a hook bundle: its path in the bundle, its size and
// the SHA-256 of its bytes. The bytes leave only in an import.
type ItemFile struct {
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// HarnessReport is an installed harness and what was found in it.
type HarnessReport struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Home   string         `json:"home"`
	Counts map[string]int `json:"counts"`
	// Notes name what was not searched, such as a kind kept in a format Belai
	// does not read.
	Notes []string `json:"notes,omitempty"`
}

// RepoReport is a trusted repository, the folders looked in and what was found.
type RepoReport struct {
	Path   string         `json:"path"`
	Name   string         `json:"name"`
	Remote string         `json:"remote,omitempty"`
	Looked []string       `json:"looked"`
	Found  map[string]int `json:"found"`
}

// Report is the whole answer of a scan.
type Report struct {
	ScannedAt    int64           `json:"scannedAt"`
	DurationMs   int64           `json:"durationMs"`
	Partial      bool            `json:"partial"`
	Items        []Item          `json:"items"`
	Harnesses    []HarnessReport `json:"harnesses"`
	Checked      int             `json:"checked"`
	NotInstalled int             `json:"notInstalled"`
	Repos        []RepoReport    `json:"repos"`
	Skipped      int             `json:"skipped"`

	// Locations is how many places were looked in (not part of the report).
	Locations int `json:"-"`
}

// Options set what a scan covers.
type Options struct {
	// Home is the user's home directory; empty means os.UserHomeDir.
	Home string
	// Kinds limits the scan to some kinds; nil is every kind.
	Kinds map[agentimport.Kind]bool
	// Repos are the repositories to look in; nil means TrustedRepos().
	Repos []Repo
	// NoRepos looks in no repository (Repos nil would otherwise mean the trusted ones).
	NoRepos bool
	// Now is the clock (time.Now unless a test sets it).
	Now func() time.Time
}

// ItemID is the identifier of an item: twelve hex digits of sha256(kind|path).
func ItemID(kind, path string) string {
	h := sha256.Sum256([]byte(kind + "|" + path))
	return hex.EncodeToString(h[:])[:12]
}

type candidate struct {
	root Root
	kind agentimport.Kind
	// file is read; path is what the report names (it adds #name for a settings item).
	file string
	path string
	hint string
	// stem is the name an invalid item is listed under.
	stem string
}

// Scan searches the host. It stops at ctx's deadline and says so.
func Scan(ctx context.Context, o Options) Report {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	start := now()
	home := o.Home
	if home == "" {
		home, _ = DefaultHome()
	}
	repos := o.Repos
	if repos == nil && !o.NoRepos {
		repos = TrustedRepos()
	}
	rep := Report{ScannedAt: start.UnixMilli(), Items: []Item{}, Harnesses: []HarnessReport{}, Repos: []RepoReport{}}

	known := Harnesses()
	rep.Checked = len(known) + 1
	installedAny := map[string]*HarnessReport{}
	for _, h := range known {
		if h.Installed(home) {
			hr := &HarnessReport{ID: h.ID, Name: h.Name, Home: homeLabel(home, h.Detect), Counts: map[string]int{}}
			installedAny[h.ID] = hr
		}
	}
	rep.NotInstalled = len(known) - len(installedAny)
	if gd, err := belaiHome(); err == nil {
		installedAny[BelaiID] = &HarnessReport{ID: BelaiID, Name: "Belai", Home: tilde(home, gd), Counts: map[string]int{}}
	}

	roots := Roots(home, repos, o.Kinds)
	repoRep := map[string]*RepoReport{}
	looked := map[string]map[string]bool{}
	for _, r := range repos {
		repoRep[r.Path] = &RepoReport{Path: r.Path, Name: sanitize.Line(r.Name, 100), Remote: cleanRemote(r.Remote), Looked: []string{}, Found: map[string]int{}}
		looked[r.Path] = map[string]bool{}
	}

	// A kind a harness keeps in a format Belai does not read is named once on the
	// harness, whatever scope it lives in (a project-only kind has no user root).
	for _, h := range known {
		hr := installedAny[h.ID]
		if hr == nil {
			continue
		}
		for _, k := range sortedKinds(h.Dirs) {
			if h.Dirs[k].Shape != harness.Unsupported {
				continue
			}
			if kind := agentimport.Kind(k); len(o.Kinds) == 0 || o.Kinds[kind] {
				hr.Notes = appendOnce(hr.Notes, k+": unsupported format, not read")
			}
		}
	}

	best := map[string]*scored{}
	var order []string
	examined := 0
	for _, root := range roots {
		if root.Unsupported {
			continue
		}
		rep.Locations++
		if root.Scope == ScopeProject {
			for _, l := range lookedNames(root) {
				looked[root.Repo][l] = true
			}
		}
		if ctx.Err() != nil {
			rep.Partial = true
			continue
		}
		cands, skipped := candidatesOf(ctx, root, &examined)
		rep.Skipped += skipped
		for _, c := range cands {
			key := string(c.kind) + "|" + c.path
			s := &scored{c: c, score: rank(c.root)}
			if prev, ok := best[key]; ok {
				if s.score > prev.score {
					best[key] = s
				}
				continue
			}
			best[key] = s
			order = append(order, key)
		}
	}
	for _, key := range order {
		c := best[key].c
		if ctx.Err() != nil {
			rep.Partial = true
			break
		}
		it, skipped := evaluate(c)
		if skipped {
			rep.Skipped++
			continue
		}
		rep.Items = append(rep.Items, it)
	}
	sortItems(rep.Items)

	for _, it := range rep.Items {
		if hr := installedAny[it.Harness]; hr != nil {
			hr.Counts[it.Kind]++
		}
		if rr := repoRep[it.Repo]; rr != nil && it.Scope == ScopeProject {
			rr.Found[it.Kind]++
		}
	}
	ids := make([]string, 0, len(installedAny))
	for id := range installedAny {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if (ids[i] == BelaiID) != (ids[j] == BelaiID) {
			return ids[i] == BelaiID
		}
		return ids[i] < ids[j]
	})
	for _, id := range ids {
		rep.Harnesses = append(rep.Harnesses, *installedAny[id])
	}
	for _, r := range repos {
		rr := repoRep[r.Path]
		for l := range looked[r.Path] {
			rr.Looked = append(rr.Looked, l)
		}
		sort.Strings(rr.Looked)
		if len(rr.Looked) > maxLooked {
			rr.Looked = rr.Looked[:maxLooked]
		}
		rep.Repos = append(rep.Repos, *rr)
	}
	rep.DurationMs = now().Sub(start).Milliseconds()
	Fit(&rep)
	return rep
}

type scored struct {
	c     candidate
	score int
}

// rank prefers, for a file several harnesses list (AGENTS.md at a repository's
// root, a shared ~/.agents/skills), the harness that is installed and has its own
// format.
func rank(r Root) int {
	s := 0
	if r.Installed {
		s += 2
	}
	if r.Format != agentimport.GenericMD {
		s++
	}
	if r.Harness == BelaiID {
		s += 4
	}
	return s
}

func appendOnce(l []string, s string) []string {
	for _, x := range l {
		if x == s {
			return l
		}
	}
	return append(l, s)
}

func belaiHome() (string, error) { return config.GlobalDir() }

// homeLabel names where a harness lives, with the user's home as ~.
func homeLabel(home string, detect []string) string {
	for _, d := range detect {
		if _, err := os.Stat(expand(home, d)); err == nil {
			return d
		}
	}
	if len(detect) > 0 {
		return detect[0]
	}
	return ""
}

// tilde writes a path under home as ~/..., so a report names no user.
func tilde(home, p string) string {
	if home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && filepath.IsLocal(rel) {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return p
}

func expand(home, p string) string {
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// lookedNames are the folders (or files) of a root, relative to its repository.
func lookedNames(r Root) []string {
	if r.Shape == shapeDoc {
		var out []string
		for _, f := range r.Files {
			out = append(out, filepath.ToSlash(filepath.Join(r.Rel, f)))
		}
		return out
	}
	return []string{filepath.ToSlash(r.Rel)}
}

// candidatesOf lists the files a root holds that could be items, without reading
// them. A link or an entry it must not follow is counted as skipped.
func candidatesOf(ctx context.Context, r Root, examined *int) ([]candidate, int) {
	skipped := 0
	var out []candidate
	mk := func(kind agentimport.Kind, file, hint string) candidate {
		stem := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
		if r.Shape == shapeSkill {
			stem = filepath.Base(filepath.Dir(file))
		}
		if r.Shape == shapeDoc {
			stem = filepath.Base(file)
		}
		return candidate{root: r, kind: kind, file: file, path: file, hint: hint, stem: stem}
	}
	fi, err := os.Lstat(r.Path)
	if err != nil {
		return nil, 0
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, 1
	}
	switch r.Shape {
	case shapeJSONKey, shapeJSONFile:
		if !fi.Mode().IsRegular() {
			return nil, 0
		}
		has, err := agentimport.ListHooks(r.Path, r.Shape == shapeJSONKey)
		if err != nil {
			if errors.Is(err, agentimport.ErrSkipped) {
				return nil, 1
			}
			return nil, 0
		}
		if !has {
			return nil, 0
		}
		name := HookName(r)
		c := mk(r.Kind, r.Path, name)
		if r.Shape == shapeJSONKey {
			c.path = r.Path + "#" + r.Key
		}
		c.stem = name
		out = append(out, c)
	case shapeHookBundle:
		des, ok := readDir(r.Path, fi)
		if !ok {
			return nil, 0
		}
		for _, de := range des {
			if ctx.Err() != nil || *examined >= maxExamined {
				break
			}
			if strings.HasPrefix(de.Name(), ".") || !hooks.ValidBundleName(de.Name()) {
				continue
			}
			if de.Type()&fs.ModeSymlink != 0 {
				skipped++
				continue
			}
			if !de.IsDir() {
				continue
			}
			*examined++
			p := filepath.Join(r.Path, de.Name(), libitem.HookDefinitionFile)
			switch ok, link := regularFile(p); {
			case link:
				skipped++
			case ok:
				c := mk(r.Kind, p, de.Name())
				c.stem = de.Name()
				out = append(out, c)
			}
		}
	case shapeSettings:
		if !fi.Mode().IsRegular() {
			return nil, 0
		}
		items, err := agentimport.ListSettings(r.Path)
		if err != nil {
			if errors.Is(err, agentimport.ErrSkipped) {
				return nil, 1
			}
			return nil, 0
		}
		for _, it := range items {
			if it.Kind != r.Kind {
				continue
			}
			c := mk(r.Kind, r.Path, it.Name)
			c.path = r.Path + "#" + it.Name
			c.stem = it.Name
			out = append(out, c)
		}
	case shapeDoc:
		if !fi.IsDir() {
			return nil, 0
		}
		for _, f := range r.Files {
			p := filepath.Join(r.Path, f)
			switch ok, link := regularFile(p); {
			case link:
				skipped++
			case ok:
				out = append(out, mk(r.Kind, p, ""))
			}
		}
	case shapeSkill:
		des, ok := readDir(r.Path, fi)
		if !ok {
			return nil, 0
		}
		for _, de := range des {
			if ctx.Err() != nil || *examined >= maxExamined {
				break
			}
			if strings.HasPrefix(de.Name(), ".") {
				continue
			}
			if de.Type()&fs.ModeSymlink != 0 {
				skipped++
				continue
			}
			if !de.IsDir() {
				continue
			}
			*examined++
			p := filepath.Join(r.Path, de.Name(), "SKILL.md")
			switch ok, link := regularFile(p); {
			case link:
				skipped++
			case ok:
				out = append(out, mk(r.Kind, p, ""))
			}
		}
	default:
		des, ok := readDir(r.Path, fi)
		if !ok {
			return nil, 0
		}
		for _, de := range des {
			if ctx.Err() != nil || *examined >= maxExamined {
				break
			}
			if strings.HasPrefix(de.Name(), ".") {
				continue
			}
			p := filepath.Join(r.Path, de.Name())
			if de.Type()&fs.ModeSymlink != 0 {
				skipped++
				continue
			}
			if de.IsDir() {
				if r.Shape != shapeMD {
					continue
				}
				sub, ok := readDir(p, nil)
				if !ok {
					continue
				}
				for _, se := range sub {
					if *examined >= maxExamined {
						break
					}
					if strings.HasPrefix(se.Name(), ".") || !strings.HasSuffix(se.Name(), ".md") {
						continue
					}
					if se.Type()&fs.ModeSymlink != 0 {
						skipped++
						continue
					}
					if se.Type().IsRegular() {
						*examined++
						out = append(out, mk(r.Kind, filepath.Join(p, se.Name()), de.Name()+"-"+strings.TrimSuffix(se.Name(), ".md")))
					}
				}
				continue
			}
			if !de.Type().IsRegular() || !extOK(r.Shape, de.Name()) {
				continue
			}
			*examined++
			out = append(out, mk(r.Kind, p, ""))
		}
	}
	return out, skipped
}

func extOK(shape, name string) bool {
	switch shape {
	case shapeMD:
		return strings.HasSuffix(name, ".md")
	case shapeProcess:
		return strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".sh")
	case shapeProfile:
		return strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".md")
	case shapeCrew:
		return strings.HasSuffix(name, ".json")
	}
	return false
}

// regularFile reports whether p is a regular file, and whether it is a link.
func regularFile(p string) (ok, link bool) {
	fi, err := os.Lstat(p)
	if err != nil {
		return false, false
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return false, true
	}
	return fi.Mode().IsRegular(), false
}

// readDir lists a directory's entries, at most maxDirEntries, sorted by name.
func readDir(p string, fi fs.FileInfo) ([]fs.DirEntry, bool) {
	if fi == nil {
		var err error
		if fi, err = os.Lstat(p); err != nil {
			return nil, false
		}
	}
	if !fi.IsDir() {
		return nil, false
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	des, _ := f.ReadDir(maxDirEntries)
	sort.Slice(des, func(i, j int) bool { return des[i].Name() < des[j].Name() })
	return des, true
}

// evaluate converts a candidate and builds its item. skipped is true for a file
// left unread on purpose (link, credential name, size).
func evaluate(c candidate) (item Item, skipped bool) {
	if len(c.path) > MaxPathBytes {
		return Item{}, true
	}
	item = Item{
		ID: ItemID(string(c.kind), c.path), Kind: string(c.kind), Name: sanitize.Line(c.stem, 64), Path: c.path,
		Harness: c.root.Harness, Format: string(c.root.Format), Scope: c.root.Scope, Repo: c.root.Repo, Notes: []Note{},
	}
	if c.root.Scope == ScopeProject {
		item.Repo = c.root.Repo
	}
	res, err := agentimport.ImportItem(c.file, c.kind, c.root.Format, importOptions(c.root, c.hint))
	if err != nil {
		if errors.Is(err, agentimport.ErrSkipped) {
			return Item{}, true
		}
		item.Verdict, item.Reason = Invalid, cleanText(err.Error(), MaxNoteBytes)
		if fi, serr := os.Lstat(c.file); serr == nil {
			item.Bytes = int(fi.Size())
		}
		return item, false
	}
	item.Name = sanitize.Line(res.Name, 64)
	item.SHA256 = res.SHA256
	item.Bytes = len(res.Doc)
	item.Description = cleanText(res.Description, MaxDescriptionBytes)
	item.Converted = res.Converted
	item.Verdict = Valid
	for _, n := range res.Notes {
		if n.Kind == agentimport.Warning || n.Kind == agentimport.Dropped {
			item.Verdict = Warning
			if item.Reason == "" {
				item.Reason = cleanText(n.Field+": "+n.Text, MaxNoteBytes)
			}
		}
	}
	item.Notes = capNotes(res.Notes, MaxNotes)
	for _, f := range res.Files {
		item.Files = append(item.Files, ItemFile{Path: f.Path, Bytes: len(f.Data), SHA256: f.SHA256})
	}
	return item, false
}

func sortedKinds(m map[string]harness.KindDirs) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// capNotes keeps at most n notes, the ones a person must read first, each cut to
// MaxNoteBytes.
func capNotes(notes []agentimport.Note, n int) []Note {
	pri := map[agentimport.NoteKind]int{agentimport.Warning: 0, agentimport.Dropped: 1, agentimport.Kept: 2, agentimport.Mapped: 3}
	idx := make([]int, len(notes))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return pri[notes[idx[a]].Kind] < pri[notes[idx[b]].Kind] })
	out := []Note{}
	for _, i := range idx {
		if len(out) >= n {
			break
		}
		out = append(out, Note{Kind: string(notes[i].Kind), Field: cleanText(notes[i].Field, 64), Text: cleanText(notes[i].Text, MaxNoteBytes)})
	}
	return out
}

// cleanText is one clean line cut to at most n bytes on a rune boundary.
func cleanText(s string, n int) string {
	s = strings.Join(strings.Fields(sanitize.Text(s)), " ")
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}

var kindOrder = func() map[string]int {
	m := map[string]int{}
	for i, k := range agentimport.ItemKinds() {
		m[string(k)] = i
	}
	return m
}()

func sortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if kindOrder[a.Kind] != kindOrder[b.Kind] {
			return kindOrder[a.Kind] < kindOrder[b.Kind]
		}
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		if a.Harness != b.Harness {
			return a.Harness < b.Harness
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Path < b.Path
	})
}

// Summary is the one line the host acknowledges a scan with.
func (r Report) Summary() string {
	s := fmt.Sprintf("%d found in %d locations", len(r.Items), r.Locations)
	if r.Partial {
		s += " (stopped at the time limit; the report is partial)"
	}
	return s
}
