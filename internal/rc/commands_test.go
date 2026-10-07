package rc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/libstore"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/turnlog"
)

// slashPromptRunner records the prompt each turn was given.
type slashPromptRunner struct {
	mu      sync.Mutex
	prompts []string
}

func (r *slashPromptRunner) RunInputObserved(_ context.Context, _ []run.Turn, in agent.TurnInput, _ func(agent.Event)) (run.Result, error) {
	r.mu.Lock()
	r.prompts = append(r.prompts, in.Prompt)
	r.mu.Unlock()
	return run.Result{Reply: "ok"}, nil
}

func (r *slashPromptRunner) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.prompts...)
}

func installCommand(t *testing.T, name, body string) {
	t.Helper()
	doc := "---\nname: " + name + "\ndescription: Runs " + name + "\nargument-hint: <id>\n---\n\n" + body + "\n"
	if _, err := libstore.Install(libitem.Command, []byte(doc), libstore.InstallOptions{}); err != nil {
		t.Fatal(err)
	}
}

// slashSession runs a session, sends cmds on the commands channel (waiting for
// each ack), then waits until want turns have run.
func slashSession(t *testing.T, workdir string, slashOn bool, want int, cmds ...sessionsync.RemoteCommand) (*slashPromptRunner, *shellAcks) {
	t.Helper()
	r := &slashPromptRunner{}
	m := &fakeMirror{prompts: make(chan sessionsync.RemotePrompt, 4)}
	commands := make(chan sessionsync.RemoteCommand, 8)
	acks := &shellAcks{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = RunSession(ctx, SessionOptions{
			Agent: r, Log: turnlog.New(nil), Mirror: m, Prompt: "first", Idle: time.Hour,
			Mode: modes.ModeAgent, Commands: commands, AckCommand: acks.ack,
			Workdir: workdir, SlashCommands: slashOn, Shell: nopShell,
		})
	}()
	for len(r.all()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	for _, c := range cmds {
		commands <- c
		acks.wait(t, c.ID)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(r.all()) < want {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	return r, acks
}

// nopShell satisfies ShellRunner so the commands channel is read.
func nopShell(context.Context, ShellRequest) ShellOutcome { return ShellOutcome{} }

func TestWebCommandExpandsTheHostsOwnFile(t *testing.T) {
	home(t)
	installCommand(t, "triage", "Triage $1 for the $2 team. All: $ARGUMENTS")
	wd := t.TempDir()
	r, acks := slashSession(t, wd, true, 2, sessionsync.RemoteCommand{ID: "c1", Command: "triage", Args: `CVE-1 "the api"`})
	if got := acks.wait(t, "c1"); got != "accepted:" {
		t.Fatalf("ack = %q", got)
	}
	prompts := r.all()
	if len(prompts) != 2 || prompts[1] != `Triage CVE-1 for the the api team. All: CVE-1 "the api"` {
		t.Fatalf("prompts = %q", prompts)
	}
}

func TestWebCommandProjectLayerWinsAndIsReadAtInvocation(t *testing.T) {
	home(t)
	installCommand(t, "triage", "global")
	wd := t.TempDir()
	dir := filepath.Join(wd, ".vulnetix", "belai", "commands")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	doc := "---\nname: triage\ndescription: d\n---\n\nproject\n"
	if err := os.WriteFile(filepath.Join(dir, "triage.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	r, _ := slashSession(t, wd, true, 2, sessionsync.RemoteCommand{ID: "c1", Command: "triage"})
	if p := r.all(); len(p) != 2 || p[1] != "project" {
		t.Fatalf("prompts = %q", p)
	}
}

func TestWebCommandRefusals(t *testing.T) {
	home(t)
	installCommand(t, "triage", "x")
	wd := t.TempDir()
	cases := []struct {
		name string
		on   bool
		cmd  sessionsync.RemoteCommand
		want string
	}{
		{"unknown name", true, sessionsync.RemoteCommand{ID: "a", Command: "nope"}, "this host has no command named nope"},
		{"a name that is not a name", true, sessionsync.RemoteCommand{ID: "b", Command: "../x"}, "not a command name"},
		{"a path is not a name", true, sessionsync.RemoteCommand{ID: "c", Command: "a/b"}, "not a command name"},
		{"switch off", false, sessionsync.RemoteCommand{ID: "d", Command: "triage"}, "sync.commands is false"},
		{"mixed with a control line", true, sessionsync.RemoteCommand{ID: "e", Command: "triage", Line: "/caveman on"}, "one name and its arguments"},
		{"oversized arguments", true, sessionsync.RemoteCommand{ID: "f", Command: "triage", Args: strings.Repeat("x", 5<<10)}, "the most is"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, acks := slashSession(t, wd, c.on, 1, c.cmd)
			got := acks.wait(t, c.cmd.ID)
			if !strings.HasPrefix(got, "refused:") || !strings.Contains(got, c.want) {
				t.Errorf("ack = %q, want a refusal naming %q", got, c.want)
			}
			if len(r.all()) != 1 {
				t.Errorf("a refused command ran a turn: %q", r.all())
			}
		})
	}
}

// A web prompt that starts with a slash stays plain text; only the structured
// form runs a command.
func TestWebPromptStartingWithASlashIsStillPlainText(t *testing.T) {
	home(t)
	installCommand(t, "triage", "EXPANDED")
	r := &slashPromptRunner{}
	m := &fakeMirror{prompts: make(chan sessionsync.RemotePrompt, 4)}
	m.prompts <- sessionsync.RemotePrompt{ID: "p1", Content: "/triage now"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = RunSession(ctx, SessionOptions{Agent: r, Log: turnlog.New(nil), Mirror: m, Prompt: "first", Idle: time.Hour, Mode: modes.ModeAgent, Workdir: t.TempDir(), SlashCommands: true})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(r.all()) < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if p := r.all(); len(p) != 2 || p[1] != "/triage now" {
		t.Fatalf("prompts = %q", p)
	}
}

func TestInventoryAdvertisesCommandsWithoutTemplates(t *testing.T) {
	home(t)
	installCommand(t, "triage", "SECRET TEMPLATE")
	before := LocalInventory()
	if len(before.Commands) != 1 || before.Commands[0] != (sessionsync.RCCommand{Name: "triage", Description: "Runs triage", ArgumentHint: "<id>"}) {
		t.Fatalf("commands = %+v", before.Commands)
	}
	b, _ := json.Marshal(before.Commands)
	if strings.Contains(string(b), "SECRET") {
		t.Errorf("a template left the host: %s", b)
	}
	var wire []map[string]string
	if err := json.Unmarshal(b, &wire); err != nil || len(wire) != 1 || wire[0]["name"] != "triage" || wire[0]["description"] != "Runs triage" || wire[0]["argumentHint"] != "<id>" || len(wire[0]) != 3 {
		t.Errorf("wire form = %s", b)
	}
	// The library item is reported with the rest of the items, by the same hash.
	var found bool
	for _, it := range before.Items {
		if it.Kind == "command" && it.Name == "triage" {
			doc, err := libstore.Get(libitem.Command, "triage")
			found = err == nil && doc.SHA256 == it.SHA256
		}
	}
	if !found {
		t.Errorf("items = %+v", before.Items)
	}
	// A new command changes the catalogue the daemon compares.
	installCommand(t, "release", "x")
	after := LocalInventory()
	if len(after.Commands) != 2 || before.catalogueHash() == after.catalogueHash() {
		t.Error("the catalogue hash ignores commands")
	}
	// sync.commands off: nothing is advertised.
	off := false
	if err := config.Mutate(config.ScopeGlobal, "", func(s *config.Settings) error {
		s.Sync = &config.SyncSettings{Commands: &off}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if inv := LocalInventory(); len(inv.Commands) != 0 {
		t.Errorf("with sync.commands off: %+v", inv.Commands)
	}
}

// A website item_install of a command writes <home>/commands/<name>.md, and the
// host then holds it as the library's copy (no edit to sync back).
func TestItemInstallWritesACommand(t *testing.T) {
	h := newItemHarness(t)
	doc := "---\nname: triage\ndescription: Triage a finding\nargument-hint: <id>\n---\n\nTriage $1.\n"
	h.serve("triage", doc)
	if status, why := h.install(libitem.Command, false); status != sessionsync.DispatchStarted {
		t.Fatalf("install: %s %s", status, why)
	}
	got, err := os.ReadFile(filepath.Join(h.home, "commands", "triage.md"))
	if err != nil || string(got) != doc {
		t.Fatalf("file = %q, %v", got, err)
	}
	it, err := libstore.Get(libitem.Command, "triage")
	if err != nil {
		t.Fatal(err)
	}
	if h.d.libsync.due(localItem{kind: "command", id: "triage", name: "triage", data: it.Doc}, time.Now()) {
		t.Error("an installed command is due for a sync")
	}
	// With sync.commands off the install is refused.
	h.on[libitem.Command] = false
	if status, _ := h.install(libitem.Command, true); status == sessionsync.DispatchStarted {
		t.Error("a command installed with its switch off")
	}
}
