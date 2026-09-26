package e2e

import (
	"reflect"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/acp"
	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/commands"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/hooks"
	"github.com/vulnetix/belai/internal/notify"
	"github.com/vulnetix/belai/internal/otel"
	"github.com/vulnetix/belai/internal/plugins"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/skills"
	"github.com/vulnetix/belai/internal/tui"
)

// The feature docs must name everything the code accepts: an event, key or
// value the code knows but the doc omits is behaviour a user cannot find.
// Each check below pairs one doc with the list the code is built from.

func docBody(t *testing.T, name string) string {
	t.Helper()
	body, ok := docFiles(t)[name]
	if !ok {
		t.Fatalf("%s not found", name)
	}
	return body
}

// jsonKeys returns the json tag names of a struct type's fields.
func jsonKeys(v any) []string {
	var out []string
	rt := reflect.TypeOf(v)
	for i := 0; i < rt.NumField(); i++ {
		tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			out = append(out, tag)
		}
	}
	return out
}

func mustName(t *testing.T, doc, body string, items ...string) {
	t.Helper()
	for _, it := range items {
		if !strings.Contains(body, "`"+it+"`") {
			t.Errorf("%s does not name `%s`", doc, it)
		}
	}
}

func TestHooksDocParity(t *testing.T) {
	body := docBody(t, "docs/hooks.md")
	mustName(t, "docs/hooks.md", body, hooks.Events()...)
	mustName(t, "docs/hooks.md", body, jsonKeys(hooks.Hook{})...)
	mustName(t, "docs/hooks.md", body, jsonKeys(hooks.Input{})...)
	mustName(t, "docs/hooks.md", body, jsonKeys(hooks.Output{})...)
	mustName(t, "docs/hooks.md", body, hooks.DecisionAllow, hooks.DecisionDeny, hooks.DecisionAsk)
	mustName(t, "docs/hooks.md", body, jsonKeys(config.HooksSettings{})...)
}

func TestNotificationsDocParity(t *testing.T) {
	body := docBody(t, "docs/notifications.md")
	mustName(t, "docs/notifications.md", body, notify.Events...)
	mustName(t, "docs/notifications.md", body, notify.Backends...)
	mustName(t, "docs/notifications.md", body, jsonKeys(config.NotificationSettings{})...)
}

func TestSkillsDocParity(t *testing.T) {
	body := docBody(t, "docs/skills.md")
	mustName(t, "docs/skills.md", body, skills.Fields()...)
	mustName(t, "docs/skills.md", body, "Skill", "SkillDraft")
	mustName(t, "docs/skills.md", body, jsonKeys(config.SkillsSettings{})...)
}

func TestPluginsDocParity(t *testing.T) {
	body := docBody(t, "docs/plugins.md")
	mustName(t, "docs/plugins.md", body, jsonKeys(plugins.Manifest{})...)
	mustName(t, "docs/plugins.md", body, plugins.ManifestFile)
	for _, cmd := range []string{"list", "install", "update", "enable", "disable", "remove"} {
		if !strings.Contains(body, "belai plugin "+cmd) {
			t.Errorf("docs/plugins.md does not document `belai plugin %s`", cmd)
		}
	}
}

func TestSandboxDocParity(t *testing.T) {
	body := docBody(t, "docs/sandbox.md")
	mustName(t, "docs/sandbox.md", body, jsonKeys(config.SandboxSettings{})...)
	mustName(t, "docs/sandbox.md", body, sandbox.ModeOff, sandbox.ModeAuto, sandbox.ModeRequired, sandbox.NetworkAllow, sandbox.NetworkDeny)
	// The documented default must be the code's default.
	var none *config.SandboxSettings
	if none.ModeOr() != sandbox.ModeAuto || none.NetworkOr() != sandbox.NetworkAllow || !none.CachesOr() {
		t.Fatal("sandbox defaults changed; update docs/sandbox.md")
	}
	if !strings.Contains(body, "| `mode` | `off`, `auto`, `required` | `auto` |") || !strings.Contains(body, "| `network` | `allow`, `deny` | `allow` |") {
		t.Error("docs/sandbox.md settings table does not state the code's defaults")
	}
}

func TestMCPDocParity(t *testing.T) {
	body := docBody(t, "docs/mcp.md")
	mustName(t, "docs/mcp.md", body, jsonKeys(config.MCPServer{})...)
	mustName(t, "docs/mcp.md", body, "stdio", "http", "mcp")
}

func TestACPDocParity(t *testing.T) {
	body := docBody(t, "docs/acp.md")
	mustName(t, "docs/acp.md", body, acp.Methods[0])
	for _, m := range acp.Methods {
		if m == "authenticate" {
			continue // accepted as a no-op; Belai offers no auth methods
		}
		mustName(t, "docs/acp.md", body, m)
	}
	mustName(t, "docs/acp.md", body, "end_turn", "cancelled", "refusal", "session/request_permission")
}

func TestTelemetryDocParity(t *testing.T) {
	body := docBody(t, "docs/telemetry.md")
	mustName(t, "docs/telemetry.md", body, otel.AllowedKeys()...)
	mustName(t, "docs/telemetry.md", body, jsonKeys(config.TelemetrySettings{})...)
}

// The roadmap table must list every feature doc with the status its own
// status line carries, so the index can never disagree with the page.
func TestRoadmapStatusesMatchDocs(t *testing.T) {
	index := docBody(t, "docs/README.md")
	for _, doc := range []string{"hooks", "notifications", "skills", "plugins", "sandbox", "mcp", "acp", "telemetry"} {
		body := docBody(t, "docs/"+doc+".md")
		line := ""
		for _, l := range strings.Split(body, "\n") {
			if strings.HasPrefix(l, "**Status:** ") {
				line = strings.TrimPrefix(l, "**Status:** ")
				break
			}
		}
		status := strings.TrimSuffix(strings.Fields(line + " x")[0], ".")
		if status == "" || status == "x" {
			t.Errorf("docs/%s.md has no status line", doc)
			continue
		}
		row := ""
		for _, l := range strings.Split(index, "\n") {
			if strings.Contains(l, "("+doc+".md)") {
				row = l
			}
		}
		if !strings.HasSuffix(strings.TrimSpace(row), "| "+status+" |") {
			t.Errorf("docs/README.md row for %s.md = %q, want status %s", doc, row, status)
		}
	}
}

// Every built-in agent profile is named in docs/agent-profiles.md, so a
// profile the harness starts on its own is never one a user cannot look up.
func TestAgentProfilesDocNamesEveryBuiltin(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	body := docBody(t, "docs/agent-profiles.md")
	all, err := agentprofile.List()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range all {
		if p.Builtin {
			n++
			mustName(t, "docs/agent-profiles.md", body, p.Name)
		}
	}
	if n == 0 {
		t.Fatal("no built-in profiles listed")
	}
}

// docs/vulnetix.md names every review scanner, the card statuses' meaning,
// and the review's background agent, so the page the review points at
// describes what it does.
func TestVulnetixDocParity(t *testing.T) {
	body := docBody(t, "docs/vulnetix.md")
	for name := range commands.AllowedSubcommands {
		mustName(t, "docs/vulnetix.md", body, name)
	}
	mustName(t, "docs/vulnetix.md", body,
		"belai:vulnetix-scanner", "belai:vulnetix-review", "--ignore-git",
		"OnScanDone", "BuildTriageBlocksFor", "components.ReportRole", "TurnInput.ReviewFindings",
		"nothing to scan", "no secrets in the working tree", "no known vulnerabilities")
}

// The README's command list names every visible slash command, aliases
// included, so a command never ships undiscoverable.
func TestReadmeNamesEverySlashCommand(t *testing.T) {
	body := docBody(t, "README.md")
	for _, name := range tui.NewRegistry(t.TempDir()).Names() {
		if !strings.Contains(body, "`/"+name+"`") && !strings.Contains(body, "`/"+name+" ") {
			t.Errorf("README.md does not name `/%s`", name)
		}
	}
}
