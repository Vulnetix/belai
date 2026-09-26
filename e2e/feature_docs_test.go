package e2e

import (
	"reflect"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/acp"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/hooks"
	"github.com/vulnetix/signet/internal/notify"
	"github.com/vulnetix/signet/internal/otel"
	"github.com/vulnetix/signet/internal/plugins"
	"github.com/vulnetix/signet/internal/sandbox"
	"github.com/vulnetix/signet/internal/skills"
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
		if !strings.Contains(body, "signet plugin "+cmd) {
			t.Errorf("docs/plugins.md does not document `signet plugin %s`", cmd)
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
			continue // accepted as a no-op; Signet offers no auth methods
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
