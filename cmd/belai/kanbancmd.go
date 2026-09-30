package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/tools"
)

const kanbanUsage = `usage: belai kanban <command> [flags] [args]

  add [flags] TITLE        file an item; prints its id
      -body TEXT | -body-file FILE   details
      -list L                        backlog (default) or review
      -label L                       a routing label (repeat for more)
      -priority N                    -2 to 3
      -assignee NAME                 route to one agent profile
      -depends K-xxxxxx              must be done first (repeat for more)
      -project NAME                  file under another project
      -json                          print the item as JSON
  list [flags]             list items (-list L, -label L, -assignee NAME,
                           -project current|all|NAME, -limit N, -json)
  show ID [-json]          one item with its history
  move ID LIST [-note T]   move an item
  note ID TEXT             add a note
  release ID               clear a worker's claim; the item returns to its list
  assign ID PROFILE | -crew CREW [flags]
                           route an item to a worker profile, or to a crew's
                           entry profile: sets the assignee (profile only),
                           adds the profile's claim labels and moves the item
                           to the list the profile claims from
      -host this|ID|none             pin to this host, a sync host id, or unpin
      -start                         also start the worker (or crew) here
  delete ID                delete an item
  import FILE.jsonl        file one item per line: {"title", "body", "list",
                           "labels", "priority", "assignee", "depends_on"}

The board is global (~/.vulnetix/belai/kanban). Writes are stamped with this
directory's project and a CLI session id. Nothing here runs a model.
`

// multiFlag collects a repeatable flag.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			*m = append(*m, s)
		}
	}
	return nil
}

// kanbanCLI is the board and provenance a CLI write uses.
type kanbanCLI struct {
	store *kanban.Store
	prov  kanban.Provenance
	cfg   config.Settings
	wd    string
}

func openKanbanCLI(wd string) (*kanbanCLI, error) {
	// The OS sandbox hides Belai's state directory, so a board opened from
	// inside it would read as empty. Say so rather than print "no items".
	if sandbox.Nested() {
		return nil, errors.New("the kanban board is not reachable from inside Belai's sandbox; use the KanbanSearch, KanbanUpdate and KanbanMove tools")
	}
	settings, err := config.LoadMerged(wd)
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	if !settings.KanbanEnabled() {
		return nil, errors.New("the kanban board is turned off (the kanban setting)")
	}
	store, err := kanban.OpenDefault()
	if err != nil {
		return nil, err
	}
	sid := "cli-" + session.MustID()[:8]
	return &kanbanCLI{store: store, prov: kanban.ProvenanceFor(wd, sid, headless.HostID()), cfg: settings, wd: wd}, nil
}

// runKanbanCLI implements `belai kanban …` and returns the exit code.
func runKanbanCLI(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "help" || args[0] == "--help" {
		fmt.Fprint(stderr, kanbanUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	wd, _ := os.Getwd()
	k, err := openKanbanCLI(wd)
	if err != nil {
		fmt.Fprintln(stderr, "belai:", err)
		return 1
	}
	// Push what this command changed, best effort, as a headless run does.
	defer headless.FlushKanban(k.cfg, wd)
	cmd, rest := args[0], args[1:]
	code, err := k.run(cmd, rest, stdin, stdout, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "belai:", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

func (k *kanbanCLI) run(cmd string, rest []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("kanban "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	switch cmd {
	case "add":
		body := fs.String("body", "", "details")
		bodyFile := fs.String("body-file", "", "read the details from a file")
		list := fs.String("list", "backlog", "backlog or review")
		priority := fs.Int("priority", 0, "-2 to 3")
		assignee := fs.String("assignee", "", "agent profile to route to")
		project := fs.String("project", "", "project name (default: this directory's)")
		var labels, deps multiFlag
		fs.Var(&labels, "label", "routing label (repeatable)")
		fs.Var(&deps, "depends", "item that must be done first (repeatable)")
		if err := parseInterleaved(fs, rest); err != nil {
			return 2, nil
		}
		title := strings.Join(fs.Args(), " ")
		if *bodyFile != "" {
			data, err := os.ReadFile(*bodyFile)
			if err != nil {
				return 1, err
			}
			*body = string(data)
		}
		l, ok := kanban.ParseList(*list)
		if !ok || (l != kanban.Backlog && l != kanban.Review) {
			return 2, errors.New("-list must be backlog or review")
		}
		prov := k.prov
		if *project != "" {
			prov.Project, prov.ProjectKey = *project, ""
		}
		it, dup, err := k.store.Add(kanban.ItemInput{Title: title, Body: *body, List: l, Labels: labels, Priority: *priority, Assignee: *assignee, DependsOn: deps}, prov)
		if err != nil {
			return 1, err
		}
		if *asJSON {
			return 0, printJSON(stdout, it)
		}
		if dup {
			fmt.Fprintf(stdout, "%s (already on the board)\n", it.Short())
		} else {
			fmt.Fprintln(stdout, it.Short())
		}
		return 0, nil

	case "list":
		lists := fs.String("list", "", "comma-separated lists (default: all but done)")
		assignee := fs.String("assignee", "", "only items routed to this profile")
		project := fs.String("project", "current", "current, all, or a project name")
		limit := fs.Int("limit", 50, "at most this many")
		var labels multiFlag
		fs.Var(&labels, "label", "only items with this label (repeatable)")
		if err := fs.Parse(rest); err != nil {
			return 2, nil
		}
		q := kanban.Query{Labels: kanban.NormLabels(labels), Assignee: *assignee, Limit: *limit}
		if *lists == "" {
			q.Lists = []kanban.List{kanban.Backlog, kanban.Review, kanban.InProgress, kanban.Blocked}
		} else {
			for _, s := range strings.Split(*lists, ",") {
				l, ok := kanban.ParseList(s)
				if !ok {
					return 2, fmt.Errorf("unknown list %q", s)
				}
				q.Lists = append(q.Lists, l)
			}
		}
		switch strings.ToLower(*project) {
		case "current", ".":
			q.Project = k.prov.ProjectKey
		case "all", "*":
		default:
			q.Project = *project
		}
		items, err := k.store.Search(q)
		if err != nil {
			return 1, err
		}
		if *asJSON {
			return 0, printJSON(stdout, items)
		}
		if len(items) == 0 {
			fmt.Fprintln(stdout, "no items")
		}
		now := time.Now().UnixMilli()
		for _, it := range items {
			fmt.Fprintf(stdout, "%s  %-11s %s", it.Short(), it.List, it.Title)
			if tags := cliTags(it, now); tags != "" {
				fmt.Fprintf(stdout, "  %s", tags)
			}
			fmt.Fprintln(stdout)
		}
		return 0, nil

	case "show":
		if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 1 {
			return 2, errors.New("usage: belai kanban show ID")
		}
		it, err := k.store.Get(fs.Arg(0))
		if err != nil {
			return 1, err
		}
		if *asJSON {
			return 0, printJSON(stdout, it)
		}
		fmt.Fprintln(stdout, tools.RenderKanbanItems([]kanban.Item{it}, it.Project))
		return 0, nil

	case "move":
		note := fs.String("note", "", "why")
		if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 2 {
			return 2, errors.New("usage: belai kanban move ID LIST [-note TEXT]")
		}
		to, ok := kanban.ParseList(fs.Arg(1))
		if !ok {
			return 2, fmt.Errorf("unknown list %q", fs.Arg(1))
		}
		it, err := k.store.Move(fs.Arg(0), to, *note, k.prov.SessionID)
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "%s → %s\n", it.Short(), it.List)
		return 0, nil

	case "note":
		if err := parseInterleaved(fs, rest); err != nil || fs.NArg() < 2 {
			return 2, errors.New("usage: belai kanban note ID TEXT")
		}
		it, err := k.store.Update(fs.Arg(0), kanban.Patch{Note: strings.Join(fs.Args()[1:], " ")}, k.prov.SessionID)
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "%s noted\n", it.Short())
		return 0, nil

	case "release":
		if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 1 {
			return 2, errors.New("usage: belai kanban release ID")
		}
		it, err := k.store.Unclaim(fs.Arg(0), "claim released from the command line", k.prov.SessionID, false)
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "%s released to %s\n", it.Short(), it.List)
		return 0, nil

	case "assign":
		crew := fs.String("crew", "", "route to a crew's entry profile")
		host := fs.String("host", "", "this, a sync host id, or none")
		start := fs.Bool("start", false, "start the worker or crew in this directory")
		if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 1+boolInt(*crew == "") {
			return 2, errors.New("usage: belai kanban assign ID PROFILE | -crew CREW [-host this|ID|none] [-start]")
		}
		return k.assign(fs.Arg(0), fs.Arg(1), *crew, *host, *start, stdout)

	case "delete":
		if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 1 {
			return 2, errors.New("usage: belai kanban delete ID")
		}
		it, err := k.store.Delete(fs.Arg(0))
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(stdout, "%s deleted\n", it.Short())
		return 0, nil

	case "import":
		if err := parseInterleaved(fs, rest); err != nil || fs.NArg() != 1 {
			return 2, errors.New("usage: belai kanban import FILE.jsonl (- for stdin)")
		}
		return k.importItems(fs.Arg(0), stdin, stdout)
	}
	fmt.Fprint(stderr, kanbanUsage)
	return 2, nil
}

// importLine is one item of an import file.
type importLine struct {
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	List      string   `json:"list"`
	Labels    []string `json:"labels"`
	Priority  int      `json:"priority"`
	Assignee  string   `json:"assignee"`
	DependsOn []string `json:"depends_on"`
	// Gates are the card's acceptance gates; see kanban.Gate. The suite of a
	// runnable gate is checked against the detected suites when the card is worked.
	Gates []importGate `json:"gates"`
}

// importGate is one acceptance gate of an imported item.
type importGate struct {
	Title string `json:"title"`
	Kind  string `json:"kind"`
	Suite string `json:"suite"`
	Dir   string `json:"dir"`
	Test  string `json:"test"`
}

func (k *kanbanCLI) importItems(path string, stdin io.Reader, stdout io.Writer) (int, error) {
	var r io.Reader = stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return 1, err
		}
		defer f.Close()
		r = f
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	n, line := 0, 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		var in importLine
		dec := json.NewDecoder(strings.NewReader(text))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			return 1, fmt.Errorf("line %d: %w", line, err)
		}
		l := kanban.Backlog
		if in.List != "" {
			pl, ok := kanban.ParseList(in.List)
			if !ok || (pl != kanban.Backlog && pl != kanban.Review) {
				return 1, fmt.Errorf("line %d: list must be backlog or review", line)
			}
			l = pl
		}
		var gates []kanban.Gate
		for _, g := range in.Gates {
			gates = append(gates, kanban.Gate{Title: g.Title, Kind: kanban.GateKind(g.Kind), Suite: g.Suite, Dir: g.Dir, Test: g.Test})
		}
		it, dup, err := k.store.Add(kanban.ItemInput{Title: in.Title, Body: in.Body, List: l, Labels: in.Labels, Priority: in.Priority, Assignee: in.Assignee, DependsOn: in.DependsOn, Gates: gates}, k.prov)
		if err != nil {
			return 1, fmt.Errorf("line %d: %w", line, err)
		}
		if !dup {
			n++
		}
		fmt.Fprintln(stdout, it.Short())
	}
	if err := sc.Err(); err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "%d items filed\n", n)
	return 0, nil
}

// parseInterleaved parses flags that may come before or after positional
// arguments: `belai kanban add "Title" -label build` reads like a sentence.
func parseInterleaved(fs *flag.FlagSet, args []string) error {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	return fs.Parse(append([]string{"--"}, pos...))
}

func cliTags(it kanban.Item, now int64) string {
	var parts []string
	if len(it.Labels) > 0 {
		parts = append(parts, "["+strings.Join(it.Labels, ",")+"]")
	}
	if it.Priority != 0 {
		parts = append(parts, fmt.Sprintf("p%d", it.Priority))
	}
	if it.Assignee != "" {
		parts = append(parts, "@"+it.Assignee)
	}
	if it.ClaimedBy != "" {
		lease := "lapsed"
		if it.LeaseUntil > now {
			lease = (time.Duration(it.LeaseUntil-now) * time.Millisecond).Round(time.Minute).String()
		}
		parts = append(parts, "claimed by "+it.ClaimedBy+" ("+lease+")")
	}
	if it.Branch != "" {
		parts = append(parts, it.Branch)
	}
	if it.PR != "" {
		parts = append(parts, it.PR)
	}
	return strings.Join(parts, "  ")
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	switch x := v.(type) {
	case kanban.Item:
		return enc.Encode(kanban.ToWire(x))
	case []kanban.Item:
		out := make([]any, len(x))
		for i, it := range x {
			out[i] = kanban.ToWire(it)
		}
		return enc.Encode(out)
	}
	return enc.Encode(v)
}
