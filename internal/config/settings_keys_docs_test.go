package config

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// settingKeys flattens a settings type into the dotted JSON paths a user can
// write: a struct field recurses, a pointer to a struct recurses, and anything
// else (a scalar, a list, a map) is a leaf.
func settingKeys(t reflect.Type, prefix string, out *[]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "-" || tag == "" || !f.IsExported() {
			continue
		}
		path := tag
		if prefix != "" {
			path = prefix + "." + tag
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft.PkgPath() != "time" {
			settingKeys(ft, path, out)
			continue
		}
		*out = append(*out, path)
	}
}

// settingsPages names the page that owns each settings block. A key under one
// of these blocks must be named in its own page (as its dotted path or as the
// backticked key), because a generic key such as enabled, mode or network is
// otherwise satisfied by any page that happens to use the word. A block not
// listed here is checked against all the docs.
var settingsPages = map[string][]string{
	"classifier":    {"docs/role-manager.md", "docs/architecture.md"},
	"jev":           {"docs/jev-jobs.md", "docs/settings.md"},
	"lsp":           {"docs/lsp.md"},
	"mcp":           {"docs/mcp.md"},
	"notifications": {"docs/notifications.md"},
	"resilience":    {"docs/resilience.md"},
	"sandbox":       {"docs/sandbox.md"},
	"skills":        {"docs/skills.md"},
	"telemetry":     {"docs/telemetry.md"},
	"voice":         {"docs/voice.md"},
	"tts":           {"docs/tts.md"},
	"sync":          {"docs/session-sync.md"},
	"hooks":         {"docs/hooks.md"},
	"tests":         {"docs/testing.md"},
	"firewall":      {"docs/firewall.md"},
	"vulnetix":      {"docs/vulnetix.md"},
}

// TestEverySettingIsDocumented keeps the documentation in step with the
// settings file: each key a user can write appears in the docs, either as its
// full dotted path or as the backticked key. A key under a block listed in
// settingsPages must appear in that block's own page.
func TestEverySettingIsDocumented(t *testing.T) {
	all := docparity.ReadDir(t, "docs") + docparity.Read(t, "README.md")
	var keys []string
	settingKeys(reflect.TypeOf(Settings{}), "", &keys)
	sort.Strings(keys)
	if len(keys) < 50 {
		t.Fatalf("only %d settings found; the walk is wrong", len(keys))
	}
	for _, k := range keys {
		leaf := k[strings.LastIndex(k, ".")+1:]
		corpus, where := all, "under docs/ or in the README"
		if pages, ok := settingsPages[strings.SplitN(k, ".", 2)[0]]; ok {
			corpus, where = "", strings.Join(pages, " or ")
			for _, p := range pages {
				corpus += docparity.Read(t, p) + "\n"
			}
		}
		if strings.Contains(corpus, k) || strings.Contains(corpus, "`"+leaf+"`") || strings.Contains(corpus, "\""+leaf+"\"") {
			continue
		}
		t.Errorf("the setting %q is not documented in %s", k, where)
	}
}
