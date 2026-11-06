package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/sanitize"
)

const worktreeUsage = `usage: belai worktree <command> [flags] [args]

  new [flags] TITLE        file a card a board agent works in its own worktree
      -agent NAME                    also route it to that worker profile
      -label L                       a routing label (repeat for more)
  list [-json]             the worktrees the harness made for this repository
  update [-yes] ID         bring a worktree's branch up to date with its base,
                           by worktrees.update_mode (merge or rebase)
  drop [-yes] ID           run the worktrees.teardown argv, then remove the
                           worktree; the branch stays when it holds work

  -trust-dir               trust the current directory without prompting
  -yes                     do not ask first (update and drop ask by default,
                           showing every argv they would run; with no terminal
                           and no -yes they refuse)

Setup and teardown argv, the update mode and the other worktrees.* settings come
from your own settings files only (docs/worktrees.md). Nothing here takes a path,
a ref or a command line: a worktree is named by the id "list" prints.
`

// worktreeCLI is what the subcommands share.
type worktreeCLI struct {
	wd          string
	stdin       io.Reader
	interactive bool
	stdout      io.Writer
	stderr      io.Writer
}

// runWorktreeCLI implements `belai worktree …` and returns the exit code.
func runWorktreeCLI(ctx context.Context, args []string, stdin io.Reader, interactive bool, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "-help" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stderr, worktreeUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "belai worktree:", err)
		return 1
	}
	c := worktreeCLI{wd: wd, stdin: stdin, interactive: interactive, stdout: stdout, stderr: stderr}
	if sandbox.Nested() {
		fmt.Fprintln(stderr, "belai worktree: not reachable from inside Belai's sandbox, which hides its state directory")
		return 1
	}
	code, err := c.run(ctx, args[0], args[1:])
	if err != nil {
		fmt.Fprintln(stderr, "belai worktree:", sanitize.Line(err.Error(), 400))
		if code == 0 {
			code = 1
		}
	}
	return code
}

func (c worktreeCLI) run(ctx context.Context, cmd string, rest []string) (int, error) {
	switch cmd {
	case "new":
		return c.newCard(rest)
	case "list":
		return c.list(rest)
	case "update":
		return c.act(ctx, "update", rest)
	case "drop":
		return c.act(ctx, "drop", rest)
	}
	fmt.Fprintf(c.stderr, "belai worktree: unknown command %q\n", cmd)
	fmt.Fprint(c.stderr, worktreeUsage)
	return 2, nil
}

func (c worktreeCLI) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("worktree "+name, flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	fs.Usage = func() { fmt.Fprint(c.stderr, worktreeUsage) }
	return fs
}

func (c worktreeCLI) newCard(args []string) (int, error) {
	fs := c.flags("new")
	agent := fs.String("agent", "", "route the card to this worker profile")
	trust := fs.Bool("trust-dir", false, "trust the current directory without prompting")
	var labels multiFlag
	fs.Var(&labels, "label", "a routing label")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, nil
	}
	title := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if title == "" {
		fmt.Fprint(c.stderr, worktreeUsage)
		return 2, nil
	}
	if err := trustRepo(c.wd, *trust); err != nil {
		return 1, err
	}
	k, err := openKanbanCLI(c.wd)
	if err != nil {
		return 1, err
	}
	defer headless.FlushKanban(k.cfg, c.wd)
	it, dup, err := fleet.NewWorktreeCard(k.store, k.prov, title, labels...)
	if err != nil {
		return 1, err
	}
	if dup {
		return 1, fmt.Errorf("%s is already on the board with that title", it.Short())
	}
	fmt.Fprintf(c.stdout, "%s filed with the %s label\n", it.Short(), config.WorktreeLabel)
	if *agent != "" {
		return k.assign(it.ID, *agent, "", "", false, c.stdout)
	}
	fmt.Fprintf(c.stdout, "route it to a worker with: belai kanban assign %s PROFILE\n", it.Short())
	return 0, nil
}

func (c worktreeCLI) list(args []string) (int, error) {
	fs := c.flags("list")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, nil
	}
	recs, err := fleet.WorktreeRecords(c.wd)
	if err != nil {
		return 1, err
	}
	if *asJSON {
		type row struct {
			ID      string `json:"id"`
			Item    string `json:"item"`
			Branch  string `json:"branch"`
			Base    string `json:"base"`
			BaseRef string `json:"base_ref,omitempty"`
			Kept    bool   `json:"kept"`
			Exists  bool   `json:"exists"`
			Dir     string `json:"dir"`
		}
		rows := make([]row, 0, len(recs))
		for _, r := range recs {
			rows = append(rows, row{r.ID, r.Item, r.Branch, r.Base, r.BaseRef, r.Kept, r.Exists, r.Dir})
		}
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		return 0, enc.Encode(rows)
	}
	if len(recs) == 0 {
		fmt.Fprintln(c.stdout, "no worktrees made by the harness for this repository")
		return 0, nil
	}
	for _, r := range recs {
		state := "present"
		if !r.Exists {
			state = "directory gone"
		}
		fmt.Fprintf(c.stdout, "%s  %s  %s  %s\n", r.ID, r.Branch, state, r.Dir)
	}
	return 0, nil
}

// confirm asks y/N on the terminal. Without a terminal it refuses, because an
// update or a removal that runs the user's argv is never done unasked.
func (c worktreeCLI) confirm(question string) error {
	if !c.interactive {
		return errors.New("this asks first and there is no terminal; pass -yes to run it unasked")
	}
	fmt.Fprintf(c.stderr, "%s [y/N] ", question)
	line, _ := bufio.NewReader(c.stdin).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
		return errors.New("cancelled")
	}
	return nil
}

func (c worktreeCLI) act(ctx context.Context, verb string, args []string) (int, error) {
	fs := c.flags(verb)
	yes := fs.Bool("yes", false, "do not ask first")
	trust := fs.Bool("trust-dir", false, "trust the current directory without prompting")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, nil
		}
		return 2, nil
	}
	if fs.NArg() != 1 || !fleet.ValidWorktreeID(fs.Arg(0)) {
		fmt.Fprint(c.stderr, worktreeUsage)
		return 2, nil
	}
	id := fs.Arg(0)
	if err := trustRepo(c.wd, *trust); err != nil {
		return 1, err
	}
	settings, pol, err := repoPolicy(c.wd)
	if err != nil {
		return 1, err
	}
	var board *kanban.Store
	if settings.KanbanEnabled() {
		board, _ = kanban.OpenDefault()
	}
	if verb == "update" {
		mode := settings.WorktreeUpdate()
		if mode == config.WorktreeUpdateOff {
			return 1, fleet.ErrUpdateOff
		}
		if !*yes {
			if err := c.confirm(fmt.Sprintf("%s worktree %s onto its base? A conflict aborts and changes nothing.", mode, id)); err != nil {
				return 1, err
			}
		}
		res, err := fleet.UpdateWorktree(ctx, c.wd, id, settings, board)
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(c.stdout, "%s: %s\n", id, res)
		return 0, nil
	}
	question := fmt.Sprintf("remove worktree %s?", id)
	if td := settings.WorktreeTeardownArgv(); len(td) > 0 {
		var lines []string
		for _, argv := range td {
			lines = append(lines, sanitize.Line(strings.Join(argv, " "), 160))
		}
		question = fmt.Sprintf("run %s, then remove worktree %s?", strings.Join(lines, "; "), id)
	}
	if !*yes {
		if err := c.confirm(question); err != nil {
			return 1, err
		}
	}
	if err := fleet.DropWorktree(ctx, c.wd, id, settings, pol, board); err != nil {
		var he *fleet.HookError
		if errors.As(err, &he) {
			// Your own command's output, for you at your terminal; it goes to no model.
			if d := he.Detail(); d != "" {
				fmt.Fprintln(c.stderr, d)
			}
			return 1, fmt.Errorf("%s; the worktree was kept", he.Error())
		}
		return 1, err
	}
	fmt.Fprintf(c.stdout, "removed worktree %s\n", id)
	return 0, nil
}
