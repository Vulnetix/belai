package tui

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/knowledge"
	"github.com/vulnetix/belai/internal/knowledge/tags"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/projectregistry"
	"github.com/vulnetix/belai/internal/tools"
)

// The knowledge screen (/knowledge) browses what the retrieval indexes hold
// (docs/knowledge.md) in three scopes: the current project, every project on
// this host (filter to one), and the agents' document collections (filter to
// one). It lists documents with their type, labels and topics, and its search
// box runs the same code a model's Grep, Glob and Read run over the store, so
// what it prints is what a model would be shown.
//
// It is a view for the user alone. Indexes are opened read-only through the
// catalogue, nothing it shows reaches a model, a transcript, telemetry or sync,
// and every string drawn from an index is cleaned before it is drawn.

// Scopes, in tab order.
const (
	knowTabProject = iota
	knowTabGlobal
	knowTabAgents
	knowTabCount
)

var knowledgeTabNames = [knowTabCount]string{"project", "global", "agents"}

// Search modes, in tab order: the three tools a model searches knowledge with.
const (
	knowModeGrep = iota
	knowModeGlob
	knowModeRead
	knowModeCount
)

var knowledgeModeNames = [knowModeCount]string{"grep", "glob", "read"}

// knowledgeEntry is one index in the global or agents list.
type knowledgeEntry struct {
	info    knowledge.IndexInfo
	title   string
	path    string // a project's root, muted
	note    string // "built in", "not indexed yet", "no registry entry"
	current bool
	// wants is true for an agent whose profile lists documents.
	wants bool
}

type knowledgeRowKind int

const (
	knowRowIndex knowledgeRowKind = iota
	knowRowDoc
)

// knowledgeRow is one line of the list: an index (global, agents) or a
// document.
type knowledgeRow struct {
	kind  knowledgeRowKind
	entry int // knowRowIndex: position in the scope's entries
	doc   knowledge.Doc
	ix    *knowledge.Index
	index string // the index a document belongs to
}

type knowledgeViewState struct {
	tab      int
	loading  bool
	gen      int
	loadedAt time.Time
	err      string

	projects []knowledgeEntry
	agents   []knowledgeEntry
	// pick is the key of the index a list was narrowed to, per tab; empty is
	// every index of the scope.
	pickGlobal, pickAgent string

	selected, scroll int
	filtering        bool
	filter           string

	// Search.
	searching bool
	mode      int
	query     string
	ran       bool
	result    []string
	resultSel int
	resultTop int
	resultCmd string

	// Document preview.
	showDoc  bool
	docLines []string
	docTop   int
	docTitle string
}

// knowledgeLoadedMsg carries the global and agents entries once the indexes are
// read.
type knowledgeLoadedMsg struct {
	gen      int
	projects []knowledgeEntry
	agents   []knowledgeEntry
	err      error
}

var knowledgeAddrRe = regexp.MustCompile(`^(kb\+[^:\s]+)`)

func (a *App) openKnowledge(tab int) tea.Cmd {
	cmd := a.push(viewKnowledge)
	if tab > 0 && tab < knowTabCount {
		a.knowledgeState.tab = tab
	}
	return cmd
}

func (a *App) enterKnowledge() tea.Cmd {
	prev := a.knowledgeState
	a.knowledgeState = knowledgeViewState{tab: knowTabProject, gen: prev.gen + 1}
	a.knowledgeStore()
	a.knowledgeState.loading = true
	return a.loadKnowledgeCmd(a.knowledgeState.gen)
}

// loadKnowledgeCmd reads every project and agent index off the UI goroutine,
// a few at a time. Each file is opened read-only; one that fails its integrity
// check is listed as corrupt and left as it is.
func (a *App) loadKnowledgeCmd(gen int) tea.Cmd {
	workdir := a.workdir
	return func() tea.Msg {
		msg := knowledgeLoadedMsg{gen: gen}
		reg, err := projectregistry.Load()
		if err != nil {
			msg.err = err
		}
		var projs []knowledgeEntry
		known := map[string]bool{}
		cur, _ := knowledge.ProjectKey(workdir)
		if err == nil {
			for _, e := range reg.All() {
				key, kerr := knowledge.ProjectKey(e.Path)
				if kerr != nil || known[key] {
					continue
				}
				known[key] = true
				projs = append(projs, knowledgeEntry{
					info:  knowledge.IndexInfo{Scope: knowledge.ScopeProject, Key: key, Name: filepath.Base(e.Path), Root: e.Path},
					title: filepath.Base(e.Path), path: e.Path, current: key == cur,
				})
			}
		}
		keys, _ := knowledge.ProjectKeys()
		for _, k := range keys {
			if !known[k] {
				known[k] = true
				projs = append(projs, knowledgeEntry{
					info:  knowledge.IndexInfo{Scope: knowledge.ScopeProject, Key: k},
					title: "unknown project " + k[:8], note: "no registry entry", current: k == cur,
				})
			}
		}
		var agents []knowledgeEntry
		if list, lerr := agentprofile.List(); lerr == nil {
			for _, p := range list {
				if p.ID == "" {
					continue
				}
				e := knowledgeEntry{
					info:  knowledge.IndexInfo{Scope: knowledge.ScopeProfile, Key: p.ID, Name: p.Name},
					title: firstNonEmpty(p.DisplayName, p.Name),
					wants: len(p.KnowledgePaths()) > 0,
				}
				if p.Builtin {
					e.note = "built in"
				}
				agents = append(agents, e)
			}
		}
		// Load the indexes four at a time.
		var wg sync.WaitGroup
		sem := make(chan struct{}, 4)
		load := func(e *knowledgeEntry) {
			defer wg.Done()
			defer func() { <-sem }()
			switch e.info.Scope {
			case knowledge.ScopeProject:
				e.info = knowledge.LoadProjectKey(e.info.Key, e.info.Name, e.info.Root)
			default:
				e.info = knowledge.LoadProfile(e.info.Key, e.info.Name)
			}
		}
		for i := range projs {
			wg.Add(1)
			sem <- struct{}{}
			go load(&projs[i])
		}
		for i := range agents {
			wg.Add(1)
			sem <- struct{}{}
			go load(&agents[i])
		}
		wg.Wait()
		// A project that has no index yet is not worth a row.
		keep := projs[:0]
		for _, e := range projs {
			if e.info.Exists() || e.info.Err != nil || e.current {
				keep = append(keep, e)
			}
		}
		projs = keep
		keepAgents := agents[:0]
		for _, e := range agents {
			if e.wants || e.info.Exists() || e.info.Err != nil {
				keepAgents = append(keepAgents, e)
			}
		}
		agents = keepAgents
		sort.SliceStable(projs, func(i, j int) bool {
			if projs[i].current != projs[j].current {
				return projs[i].current
			}
			return strings.ToLower(projs[i].title) < strings.ToLower(projs[j].title)
		})
		sort.SliceStable(agents, func(i, j int) bool {
			return strings.ToLower(agents[i].title) < strings.ToLower(agents[j].title)
		})
		for i := range agents {
			if !agents[i].info.Exists() && agents[i].info.Err == nil {
				agents[i].note = strings.TrimSpace(agents[i].note + " not indexed yet")
			}
		}
		msg.projects, msg.agents = projs, agents
		return msg
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// handleKnowledgeMsg takes the load result, dropping a stale one.
func (a *App) handleKnowledgeMsg(msg tea.Msg) (tea.Cmd, bool) {
	m, ok := msg.(knowledgeLoadedMsg)
	if !ok {
		return nil, false
	}
	st := &a.knowledgeState
	if m.gen != st.gen {
		return nil, true
	}
	st.loading, st.loadedAt = false, time.Now()
	st.projects, st.agents = m.projects, m.agents
	if m.err != nil {
		st.err = mcpClean(m.err.Error(), 200)
	}
	a.clampKnowledge()
	return nil, true
}

// knowledgeEntries is the index list of the current scope.
func (a *App) knowledgeEntries() []knowledgeEntry {
	switch a.knowledgeState.tab {
	case knowTabGlobal:
		return a.knowledgeState.projects
	case knowTabAgents:
		return a.knowledgeState.agents
	}
	return nil
}

// knowledgePick is the key the current tab was narrowed to.
func (a *App) knowledgePick() string {
	switch a.knowledgeState.tab {
	case knowTabGlobal:
		return a.knowledgeState.pickGlobal
	case knowTabAgents:
		return a.knowledgeState.pickAgent
	}
	return ""
}

func (a *App) setKnowledgePick(key string) {
	switch a.knowledgeState.tab {
	case knowTabGlobal:
		a.knowledgeState.pickGlobal = key
	case knowTabAgents:
		a.knowledgeState.pickAgent = key
	}
}

// knowledgeIndexes returns the indexes the current scope covers, with the name
// each is shown under.
func (a *App) knowledgeIndexes() []*knowledge.Index {
	switch a.knowledgeState.tab {
	case knowTabProject:
		return a.knowledgeStore().Set().Indexes()
	}
	pick := a.knowledgePick()
	var out []*knowledge.Index
	for _, e := range a.knowledgeEntries() {
		if e.info.Index == nil || (pick != "" && e.info.Key != pick) {
			continue
		}
		out = append(out, e.info.Index)
	}
	return out
}

// knowledgeRows builds the rows the list shows, before the filter.
func (a *App) knowledgeAllRows() []knowledgeRow {
	st := &a.knowledgeState
	if st.tab != knowTabProject && a.knowledgePick() == "" {
		entries := a.knowledgeEntries()
		rows := make([]knowledgeRow, len(entries))
		for i := range entries {
			rows[i] = knowledgeRow{kind: knowRowIndex, entry: i}
		}
		return rows
	}
	var rows []knowledgeRow
	for _, ix := range a.knowledgeIndexes() {
		for _, d := range ix.Docs() {
			rows = append(rows, knowledgeRow{kind: knowRowDoc, doc: d, ix: ix, index: ix.Name()})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].index != rows[j].index {
			return rows[i].index < rows[j].index
		}
		return rows[i].doc.Address < rows[j].doc.Address
	})
	return rows
}

// knowledgeHay is the text a filter is matched against: for a document its
// address, type, language and every label (so topic:jwt and type:test filter);
// for an index its name and path.
func (a *App) knowledgeHay(r knowledgeRow) string {
	if r.kind == knowRowIndex {
		e := a.knowledgeEntries()[r.entry]
		return strings.ToLower(e.title + " " + e.path + " " + e.note)
	}
	return strings.ToLower(r.doc.Address + " " + strings.Join(r.doc.Tags.All(), " ") + " " + r.doc.Tags.Kind + " " + r.doc.Tags.Lang)
}

func (a *App) knowledgeShown() []knowledgeRow {
	rows := a.knowledgeAllRows()
	q := strings.ToLower(strings.TrimSpace(a.knowledgeState.filter))
	if q == "" {
		return rows
	}
	terms := strings.Fields(q)
	var out []knowledgeRow
	for _, r := range rows {
		hay := a.knowledgeHay(r)
		ok := true
		for _, t := range terms {
			if !strings.Contains(hay, t) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, r)
		}
	}
	return out
}

func (a *App) clampKnowledge() {
	st := &a.knowledgeState
	n := len(a.knowledgeShown())
	if st.selected >= n {
		st.selected = n - 1
	}
	if st.selected < 0 {
		st.selected = 0
	}
}

// knowledgeViewKB is the tools.Knowledge the search runs against: the scope's
// indexes merged, capped like a model's search. In the project scope it also
// applies the Read deny rules, as the session's adapter does, so the project
// tab shows what a model there could be shown.
type knowledgeViewKB struct {
	set   *knowledge.Set
	limit int
	deny  func(source string) bool
}

func (k *knowledgeViewKB) Search(query string) []tools.KnowledgeHit {
	hits := k.set.Search(query, k.limit, func(h knowledge.Hit) bool { return k.deny == nil || !k.deny(h.Source) })
	out := make([]tools.KnowledgeHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, tools.KnowledgeHit{Address: h.Address, Source: h.Source, Start: h.Start, End: h.End, Text: h.Text, Score: h.Score})
	}
	return out
}

func (k *knowledgeViewKB) Match(match func(address string) bool) []string {
	return k.set.Match(func(address, source string) bool {
		return (k.deny == nil || !k.deny(source)) && match(address)
	})
}

func (a *App) knowledgeKB() *knowledgeViewKB {
	_, _, limit := a.settings.KnowledgeLimits()
	kb := &knowledgeViewKB{set: knowledge.NewSet(a.knowledgeIndexes()...), limit: limit}
	if a.knowledgeState.tab == knowTabProject {
		perms := permissions.From(a.settings.Permissions.Allow, a.settings.Permissions.Ask, a.settings.Permissions.Deny)
		workdir := a.workdir
		kb.deny = func(source string) bool {
			if source == "" {
				return false
			}
			subjects := []string{source}
			if rel, err := filepath.Rel(workdir, source); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				subjects = append(subjects, filepath.ToSlash(rel))
			}
			for _, s := range subjects {
				if dec, rule := perms.Explain("Read", s); rule != "" && dec == permissions.DecisionBlock {
					return true
				}
			}
			return false
		}
	}
	return kb
}

// runKnowledgeSearch runs the query as the chosen tool would and keeps the
// text it prints. Nothing is sent anywhere: the passages were classified when
// they were indexed, and a search is a lookup.
func (a *App) runKnowledgeSearch() {
	st := &a.knowledgeState
	st.ran, st.result, st.resultSel, st.resultTop = true, nil, 0, 0
	q := strings.TrimSpace(st.query)
	if q == "" {
		st.resultCmd = ""
		return
	}
	kb := a.knowledgeKB()
	var out string
	switch st.mode {
	case knowModeGlob:
		st.resultCmd = fmt.Sprintf("Glob pattern=%q", q)
		out, _ = tools.PreviewGlob(kb, q)
	case knowModeRead:
		st.resultCmd = fmt.Sprintf("Read %s", q)
		out = a.knowledgeReadPreview(kb, q)
	default:
		st.resultCmd = fmt.Sprintf("Grep pattern=%q output_mode=content", q)
		out, _ = tools.PreviewGrep(kb, q, false)
	}
	if strings.TrimSpace(out) == "" {
		st.result = []string{"(the model would get no knowledge rows for this)"}
		return
	}
	for _, l := range strings.Split(out, "\n") {
		st.result = append(st.result, mcpClean(l, 400))
	}
}

// knowledgeReadPreview finds the document the query names (an address or a
// part of one) and shows the related-passages block a Read of it would carry.
func (a *App) knowledgeReadPreview(kb *knowledgeViewKB, q string) string {
	q = strings.ToLower(strings.TrimPrefix(q, "kb+"))
	for _, ix := range a.knowledgeIndexes() {
		for _, d := range ix.Docs() {
			if !strings.Contains(strings.ToLower(strings.TrimPrefix(d.Address, "kb+")), q) {
				continue
			}
			if kb.deny != nil && kb.deny(d.Source) {
				continue
			}
			_, text, ok := ix.Document(d.Address)
			if !ok {
				continue
			}
			out, _ := tools.PreviewRead(kb, text, d.Source)
			head := fmt.Sprintf("%s: %d lines of indexed text", d.Address, strings.Count(text, "\n")+1)
			if out == "" {
				return head + "\n(no related passages above the similarity floor)"
			}
			return head + "\n\n" + out
		}
	}
	return ""
}

// openKnowledgeDoc shows the indexed text of address.
func (a *App) openKnowledgeDoc(address string) bool {
	st := &a.knowledgeState
	for _, ix := range a.knowledgeIndexes() {
		d, text, ok := ix.Document(address)
		if !ok {
			continue
		}
		st.docLines = nil
		for _, l := range strings.Split(text, "\n") {
			st.docLines = append(st.docLines, mcpClean(l, 600))
		}
		st.docTitle, st.docTop, st.showDoc = d.Address, 0, true
		return true
	}
	return false
}

func topTags(docs []knowledge.Doc, prefix string, n int) []string {
	counts := map[string]int{}
	for _, d := range docs {
		for _, l := range d.Tags.All() {
			if strings.HasPrefix(l, prefix) {
				counts[strings.TrimPrefix(l, prefix)]++
			}
		}
	}
	type kv struct {
		k string
		n int
	}
	var all []kv
	for k, c := range counts {
		all = append(all, kv{k, c})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].n != all[j].n {
			return all[i].n > all[j].n
		}
		return all[i].k < all[j].k
	})
	if len(all) > n {
		all = all[:n]
	}
	out := make([]string, len(all))
	for i, e := range all {
		out[i] = fmt.Sprintf("%s %d", e.k, e.n)
	}
	return out
}

func topicLabel(id string) string {
	if d, ok := tags.Lookup(id); ok {
		return d.Label
	}
	return id
}

func (a *App) handleKnowledgeKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.knowledgeState
	key := m.String()

	if st.searching {
		switch m.Type {
		case tea.KeyEsc:
			st.searching = false
		case tea.KeyEnter:
			st.searching = false
			a.runKnowledgeSearch()
		case tea.KeyTab:
			st.mode = (st.mode + 1) % knowModeCount
		case tea.KeyShiftTab:
			st.mode = (st.mode + knowModeCount - 1) % knowModeCount
		case tea.KeyRunes:
			st.query += string(m.Runes)
		case tea.KeySpace:
			st.query += " "
		case tea.KeyBackspace:
			st.query = trimLastRune(st.query)
		case tea.KeyCtrlU:
			st.query = ""
		}
		return a, nil
	}
	if st.filtering {
		switch m.Type {
		case tea.KeyEsc:
			st.filtering, st.filter = false, ""
		case tea.KeyEnter:
			st.filtering = false
		case tea.KeyRunes:
			st.filter += string(m.Runes)
		case tea.KeySpace:
			st.filter += " "
		case tea.KeyBackspace:
			st.filter = trimLastRune(st.filter)
		case tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown:
			a.moveKnowledge(key)
			return a, nil
		default:
			return a, nil
		}
		st.selected, st.scroll = 0, 0
		return a, nil
	}

	// A document or a result list takes the movement keys while it is open.
	if st.showDoc {
		switch key {
		case "esc", "enter", "q":
			st.showDoc = false
		case "up", "k":
			st.docTop = max(0, st.docTop-1)
		case "down", "j":
			st.docTop = min(max(0, len(st.docLines)-1), st.docTop+1)
		case "pgup", "ctrl+u":
			st.docTop = max(0, st.docTop-a.knowledgePaneRows())
		case "pgdown", "ctrl+d", " ":
			st.docTop = min(max(0, len(st.docLines)-1), st.docTop+a.knowledgePaneRows())
		case "home", "g":
			st.docTop = 0
		case "end", "G":
			st.docTop = max(0, len(st.docLines)-a.knowledgePaneRows())
		}
		return a, nil
	}

	switch key {
	case "1", "2", "3":
		return a, a.switchKnowledgeTab(int(key[0] - '1'))
	case "tab":
		return a, a.switchKnowledgeTab((st.tab + 1) % knowTabCount)
	case "shift+tab":
		return a, a.switchKnowledgeTab((st.tab + knowTabCount - 1) % knowTabCount)
	case "s":
		st.searching = true
		return a, nil
	case "m":
		st.mode = (st.mode + 1) % knowModeCount
		if st.ran {
			a.runKnowledgeSearch()
		}
		return a, nil
	case "/":
		st.filtering = true
		return a, nil
	case "r":
		st.loading, st.gen = true, st.gen+1
		return a, a.loadKnowledgeCmd(st.gen)
	}

	if st.ran && len(st.result) > 0 {
		switch key {
		case "esc":
			st.ran, st.result = false, nil
		case "enter":
			if l := st.result[st.resultSel]; knowledgeAddrRe.MatchString(l) {
				a.openKnowledgeDoc(knowledgeAddrRe.FindString(l))
			}
		case "up", "k":
			st.resultSel = max(0, st.resultSel-1)
		case "down", "j":
			st.resultSel = min(len(st.result)-1, st.resultSel+1)
		case "pgup", "ctrl+u":
			st.resultSel = max(0, st.resultSel-a.knowledgePaneRows())
		case "pgdown", "ctrl+d", " ":
			st.resultSel = min(len(st.result)-1, st.resultSel+a.knowledgePaneRows())
		case "home", "g":
			st.resultSel = 0
		case "end", "G":
			st.resultSel = len(st.result) - 1
		}
		return a, nil
	}

	shown := a.knowledgeShown()
	switch key {
	case "esc":
		switch {
		case st.filter != "":
			st.filter, st.selected, st.scroll = "", 0, 0
		case a.knowledgePick() != "":
			a.setKnowledgePick("")
			st.selected, st.scroll = 0, 0
		default:
			a.pop()
		}
	case "enter":
		if st.selected >= len(shown) {
			return a, nil
		}
		r := shown[st.selected]
		if r.kind == knowRowIndex {
			e := a.knowledgeEntries()[r.entry]
			a.setKnowledgePick(e.info.Key)
			st.selected, st.scroll, st.filter = 0, 0, ""
		} else {
			a.openKnowledgeDoc(r.doc.Address)
		}
	default:
		a.moveKnowledge(key)
	}
	return a, nil
}

func (a *App) switchKnowledgeTab(tab int) tea.Cmd {
	st := &a.knowledgeState
	if tab == st.tab {
		return nil
	}
	st.tab = tab
	st.selected, st.scroll, st.filter, st.filtering = 0, 0, "", false
	st.ran, st.result, st.showDoc = false, nil, false
	return nil
}

func (a *App) moveKnowledge(key string) {
	st := &a.knowledgeState
	n := len(a.knowledgeShown())
	page := max(1, a.knowledgeListRows()-1)
	switch key {
	case "up", "k":
		st.selected--
	case "down", "j":
		st.selected++
	case "pgup", "ctrl+u":
		st.selected -= page
	case "pgdown", "ctrl+d", " ":
		st.selected += page
	case "home", "g":
		st.selected = 0
	case "end", "G":
		st.selected = n - 1
	default:
		return
	}
	a.clampKnowledge()
}
