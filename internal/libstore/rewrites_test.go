package libstore

import (
	"errors"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/shellsafe"
)

const rewriteDoc = `{"name":"bash_rewrite","enabled":false,"rules":[{"match":"npm install","replace":"pnpm add"},{"match":"npm","replace":"pnpm"}]}`

func TestRewriteInstallWritesTheTableAndExportsTheSameBytes(t *testing.T) {
	home(t)
	res, err := Install(libitem.Rewrite, []byte(rewriteDoc), InstallOptions{Name: "bash_rewrite"})
	if err != nil || res.Replaced {
		t.Fatalf("install: %+v %v", res, err)
	}
	s, _ := config.LoadGlobal()
	if s.BashRewrite == nil || len(s.BashRewrite.Rules) != 2 || s.BashRewrite.Enabled == nil || *s.BashRewrite.Enabled {
		t.Fatalf("settings = %+v", s.BashRewrite)
	}
	want, _ := libitem.Validate(libitem.Rewrite, []byte(rewriteDoc))
	got, err := Get(libitem.Rewrite, "bash_rewrite")
	if err != nil || string(got.Doc) != string(want.Doc) || got.SHA256 != want.SHA256 {
		t.Fatalf("exported %q (%v), want %q", got.Doc, err, want.Doc)
	}
	// The table the host now reads is the one that was installed.
	if rules := s.BashRewriteRules(); len(rules) != 0 {
		t.Errorf("a switched-off table has active rules: %+v", rules)
	}
	on := strings.Replace(rewriteDoc, `"enabled":false,`, "", 1)
	if _, err := Install(libitem.Rewrite, []byte(on), InstallOptions{Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	s, _ = config.LoadGlobal()
	rules := s.BashRewriteRules()
	if len(rules) != 2 || strings.Join(rules[0].Match, " ") != "npm install" || strings.Join(rules[0].Replace, " ") != "pnpm add" {
		t.Fatalf("active rules = %+v", rules)
	}
	for _, r := range rules {
		if err := shellsafe.ValidRewriteRule(r); err != nil {
			t.Errorf("an installed rule fails the rewrite engine's own check: %v", err)
		}
	}
}

func TestRewriteInstallNeedsOverwriteAndKeepsTheName(t *testing.T) {
	home(t)
	if _, err := Install(libitem.Rewrite, []byte(rewriteDoc), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(libitem.Rewrite, []byte(`{"name":"bash_rewrite","rules":[{"match":"a","replace":"b"}]}`), InstallOptions{}); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v", err)
	}
	if s, _ := config.LoadGlobal(); len(s.BashRewrite.Rules) != 2 {
		t.Fatal("a refused install changed the table")
	}
	res, err := Install(libitem.Rewrite, []byte(`{"name":"bash_rewrite","rules":[{"match":"a","replace":"b"}]}`), InstallOptions{Overwrite: true})
	if err != nil || !res.Replaced {
		t.Fatalf("overwrite: %+v %v", res, err)
	}
	s, _ := config.LoadGlobal()
	if len(s.BashRewrite.Rules) != 1 || s.BashRewrite.Enabled != nil {
		t.Fatalf("after overwrite: %+v", s.BashRewrite)
	}
	if _, err := Install(libitem.Rewrite, []byte(`{"name":"other","rules":[]}`), InstallOptions{Overwrite: true}); err == nil || !IsRefusal(err) {
		t.Errorf("another name: %v", err)
	}
	// ExportAs for a rewrite insists on the one name.
	if _, err := ExportAs(libitem.Rewrite, "other"); err == nil || !IsRefusal(err) {
		t.Errorf("ExportAs(other): %v", err)
	}
	if got, err := ExportAs(libitem.Rewrite, "bash_rewrite"); err != nil || got.Name != "bash_rewrite" {
		t.Errorf("ExportAs(bash_rewrite): %+v %v", got, err)
	}
}

func TestRewriteInstallRefusals(t *testing.T) {
	home(t)
	cases := map[string]string{
		`{"name":"bash_rewrite","rules":[{"match":"npm","replace":"pnpm; rm -rf /"}]}`: "must be plain",
		`{"name":"bash_rewrite","rules":[{"match":"-x","replace":"y"}]}`:               "must start with a command name",
		`{"name":"bash_rewrite","rules":[{"match":"a","replace":"a"}]}`:                "match and replace are the same",
		`{"name":"bash_rewrite"}`: "rules is required",
	}
	for doc, want := range cases {
		if _, err := Install(libitem.Rewrite, []byte(doc), InstallOptions{}); err == nil || !IsRefusal(err) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want a refusal naming %q", doc, err, want)
		}
	}
	if s, _ := config.LoadGlobal(); s.BashRewrite != nil {
		t.Fatal("a refused install wrote a table")
	}
}

func TestRewriteEmptyTableIsNotAnItem(t *testing.T) {
	home(t)
	if items, _, err := List(libitem.Rewrite); err != nil || len(items) != 0 {
		t.Fatalf("nothing configured: %+v %v", items, err)
	}
	if _, err := Install(libitem.Rewrite, []byte(`{"name":"bash_rewrite","rules":[]}`), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	// An empty table with no switch says nothing, so there is nothing to sync.
	if items, _, _ := List(libitem.Rewrite); len(items) != 0 {
		t.Fatalf("an empty table is an item: %+v", items)
	}
}
