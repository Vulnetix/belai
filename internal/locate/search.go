package locate

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Search ranks the inventory's files for a query.
//
// Stages: every file is scored lexically by its path; the directories that
// hold the best files are put to the Rater (or, without one, kept in lexical
// order); the files in the directories that survive are read for the names
// they declare and put to the Rater; a file is a hit at HitAt, a lead at
// LeadAt. A file the Rater gave no answer for keeps its lexical score: unknown
// is not low. When the Rater answered for nothing the result is the lexical
// order and Lexical is true.
func Search(ctx context.Context, inv *Inventory, query string, opt Options) Result {
	qw := words(query)
	if inv == nil || len(inv.Files) == 0 || len(qw) == 0 {
		return Result{Lexical: true}
	}
	maxHits := opt.MaxHits
	if maxHits <= 0 {
		maxHits = DefaultMaxHits
	}
	dirCap, fileCap := DirsRemote, FilesRemote
	if opt.Local {
		dirCap, fileCap = DirsLocal, FilesLocal
	}

	if opt.Exclude != nil {
		kept := make([]File, 0, len(inv.Files))
		for _, f := range inv.Files {
			if !opt.Exclude(f.Path) {
				kept = append(kept, f)
			}
		}
		cp := *inv
		cp.Files = kept
		inv = &cp
	}
	docs := make([]*doc, len(inv.Files))
	for i, f := range inv.Files {
		docs[i] = newDoc(f.Path, nil)
	}
	lex := normalise(bm25(docs, qw))

	// Directories, scored by their best file.
	type dirInfo struct {
		path  string
		best  float64
		files []int
		exts  map[string]int
	}
	dirs := map[string]*dirInfo{}
	for i, f := range inv.Files {
		d := path.Dir(f.Path)
		di := dirs[d]
		if di == nil {
			di = &dirInfo{path: d, exts: map[string]int{}}
			dirs[d] = di
		}
		di.files = append(di.files, i)
		if lex[i] > di.best {
			di.best = lex[i]
		}
		di.exts[strings.ToLower(path.Ext(f.Path))]++
	}
	// Directories whose paths match the query come first. A question is often
	// about a name the path does not mention, so the rest fill the remaining
	// places, source-heavy directories first, and the Rater (or, without one,
	// the declaration names read below) decides.
	var matched, rest []*dirInfo
	for _, di := range dirs {
		if di.best > 0 {
			matched = append(matched, di)
		} else {
			rest = append(rest, di)
		}
	}
	sort.Slice(matched, func(a, b int) bool {
		if matched[a].best != matched[b].best {
			return matched[a].best > matched[b].best
		}
		return matched[a].path < matched[b].path
	})
	sort.Slice(rest, func(a, b int) bool {
		sa, sb := sourceFiles(inv, rest[a].files), sourceFiles(inv, rest[b].files)
		if sa != sb {
			return sa > sb
		}
		return rest[a].path < rest[b].path
	})
	ordered := append(matched, rest...)
	if len(ordered) > dirCap {
		ordered = ordered[:dirCap]
	}

	res := Result{Lexical: true}
	kept := ordered
	if opt.Rater != nil && ctx.Err() == nil {
		items := make([]Item, len(ordered))
		for i, di := range ordered {
			items[i] = Item{ID: fmt.Sprintf("d%d", i), Label: dirLabel(di.path, len(di.files), di.exts)}
		}
		scores, err := opt.Rater.Rate(ctx, StageDirs, query, items)
		if err == nil {
			res.DirsRated = len(items)
			var keep []*dirInfo
			for i, di := range ordered {
				s, ok := scores[items[i].ID]
				if !ok {
					res.Unknown++
					keep = append(keep, di)
					continue
				}
				res.Lexical = false
				if s >= LeadAt {
					keep = append(keep, di)
				}
			}
			if len(keep) > 0 {
				kept = keep
			}
		}
	}

	// Files in the surviving directories, best lexical first.
	var cand []int
	for _, di := range kept {
		cand = append(cand, di.files...)
	}
	sort.SliceStable(cand, func(a, b int) bool {
		if lex[cand[a]] != lex[cand[b]] {
			return lex[cand[a]] > lex[cand[b]]
		}
		return inv.Files[cand[a]].Path < inv.Files[cand[b]].Path
	})
	if len(cand) > fileCap {
		cand = cand[:fileCap]
	}
	var cdocs []*doc
	var cfiles []File
	for _, i := range cand {
		f := inv.Files[i]
		decls, text := Declarations(filepath.Join(inv.Root, filepath.FromSlash(f.Path)))
		if !text {
			continue
		}
		cdocs = append(cdocs, newDoc(f.Path, decls))
		cfiles = append(cfiles, f)
	}
	if len(cdocs) == 0 {
		return res
	}
	clex := normalise(bm25(cdocs, qw))

	final := make([]float64, len(cdocs))
	rated := make([]bool, len(cdocs))
	copy(final, clex)
	if opt.Rater != nil && ctx.Err() == nil {
		items := make([]Item, len(cdocs))
		for i, d := range cdocs {
			items[i] = Item{ID: fmt.Sprintf("f%d", i), Label: fileLabel(cfiles[i], d.decls, opt.Previews)}
		}
		scores, err := opt.Rater.Rate(ctx, StageFiles, query, items)
		if err == nil {
			res.FilesRated = len(items)
			for i := range cdocs {
				if s, ok := scores[items[i].ID]; ok {
					final[i], rated[i] = s, true
					res.Lexical = false
				} else {
					res.Unknown++
				}
			}
		}
	}

	idx := rank(cdocs, final)
	for _, i := range idx {
		if len(res.Hits) >= maxHits {
			break
		}
		h := Hit{Path: cdocs[i].path, Score: final[i], Line: bestLine(cdocs[i].decls, qw), Rated: rated[i]}
		if res.Lexical {
			if final[i] <= 0 || len(res.Hits) >= LexicalOnlyHits {
				continue
			}
			res.Hits = append(res.Hits, h)
			continue
		}
		if final[i] < LeadAt {
			continue
		}
		h.Lead = final[i] < HitAt
		res.Hits = append(res.Hits, h)
	}
	if len(res.Hits) == 0 && !res.Lexical {
		// The Rater found nothing above a lead. Its answer is kept as
		// "nothing is a strong match", but the best lexical files are offered
		// as leads so the caller still has somewhere to start.
		for _, i := range rank(cdocs, clex) {
			if len(res.Hits) >= 3 || clex[i] <= 0 {
				break
			}
			res.Hits = append(res.Hits, Hit{Path: cdocs[i].path, Score: clex[i], Line: bestLine(cdocs[i].decls, qw), Lead: true})
		}
	}
	return res
}

// sourceFiles counts the files in idx with a known language other than plain
// text, prose or data: the directories most likely to hold code.
func sourceFiles(inv *Inventory, idx []int) int {
	n := 0
	for _, i := range idx {
		switch Language(inv.Files[i].Path) {
		case "text", "Markdown", "JSON", "YAML", "TOML", "HTML", "CSS":
		default:
			n++
		}
	}
	return n
}

// dirLabel is what a Rater is shown for a directory: its path, how many files
// it holds and their common extensions. No file text.
func dirLabel(dir string, n int, exts map[string]int) string {
	type kv struct {
		k string
		n int
	}
	var list []kv
	for k, v := range exts {
		if k == "" {
			k = "(none)"
		}
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(a, b int) bool {
		if list[a].n != list[b].n {
			return list[a].n > list[b].n
		}
		return list[a].k < list[b].k
	})
	if len(list) > 4 {
		list = list[:4]
	}
	parts := make([]string, len(list))
	for i, e := range list {
		parts[i] = fmt.Sprintf("%s x%d", e.k, e.n)
	}
	return fmt.Sprintf("directory %s (%d files: %s)", dir, n, strings.Join(parts, ", "))
}

// fileLabel is what a Rater is shown for a file: its path, language and size,
// and, when previews are allowed, the names it declares.
func fileLabel(f File, decls []Decl, previews bool) string {
	l := fmt.Sprintf("file %s (%s, %s)", f.Path, Language(f.Path), humanSize(f.Size))
	if !previews || len(decls) == 0 {
		return l
	}
	names := make([]string, 0, 12)
	for _, d := range decls {
		if len(names) == 12 {
			break
		}
		names = append(names, d.Name)
	}
	return l + " declares " + strings.Join(names, ", ")
}

func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d MB", n>>20)
}
