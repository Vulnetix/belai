package config

import (
	"strings"
	"testing"
)

func rules(pairs ...string) []BashRewriteRule {
	var out []BashRewriteRule
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, BashRewriteRule{Match: pairs[i], Replace: pairs[i+1]})
	}
	return out
}

func TestBashRewriteDefaultsToNoRules(t *testing.T) {
	if got := (Settings{}).BashRewriteRules(); len(got) != 0 {
		t.Fatalf("rules = %v", got)
	}
}

func TestBashRewriteRulesParseAndSwitchOff(t *testing.T) {
	s := Settings{BashRewrite: &BashRewriteSettings{Rules: rules("npm install", "pnpm add", "npm", "pnpm")}}
	got := s.BashRewriteRules()
	if len(got) != 2 || got[0].String() != "npm install -> pnpm add" || got[1].String() != "npm -> pnpm" {
		t.Fatalf("rules = %v", got)
	}
	s.BashRewrite.Enabled = ptr(false)
	if len(s.BashRewriteRules()) != 0 {
		t.Fatal("enabled:false must switch the table off")
	}
}

func TestValidateBashRewrite(t *testing.T) {
	if err := ValidateBashRewrite(Settings{BashRewrite: &BashRewriteSettings{Rules: rules("npm", "pnpm")}}); err != nil {
		t.Fatal(err)
	}
	for _, r := range [][]BashRewriteRule{
		rules("npm", ""),
		rules("", "pnpm"),
		rules("npm", "pnpm; rm -rf x"),
		rules("npm", "$(evil)"),
		rules("npm", "pnpm && x"),
		rules("npm", "npm"),
		rules("-x", "pnpm"),
	} {
		err := ValidateBashRewrite(Settings{BashRewrite: &BashRewriteSettings{Rules: r}})
		if err == nil || !strings.Contains(err.Error(), "bash_rewrite.rules[0]") {
			t.Errorf("%v: err = %v", r, err)
		}
	}
	many := make([]BashRewriteRule, MaxBashRewriteRules+1)
	for i := range many {
		many[i] = BashRewriteRule{Match: "a", Replace: "b"}
	}
	if err := ValidateBashRewrite(Settings{BashRewrite: &BashRewriteSettings{Rules: many}}); err == nil {
		t.Fatal("too many rules accepted")
	}
	// A hand-built table that skipped validation yields no rules at all.
	if got := (Settings{BashRewrite: &BashRewriteSettings{Rules: rules("npm", "pnpm;x")}}).BashRewriteRules(); got != nil {
		t.Fatalf("invalid rules were used: %v", got)
	}
}

func TestProjectLayerCannotAddOrEnableBashRewrite(t *testing.T) {
	proj := Settings{BashRewrite: &BashRewriteSettings{Enabled: ptr(true), Rules: rules("git", "rm")}}
	if got := (Settings{}).Override(proj).BashRewriteRules(); len(got) != 0 {
		t.Fatalf("a project layer supplied rules: %v", got)
	}
	user := Settings{BashRewrite: &BashRewriteSettings{Rules: rules("npm", "pnpm")}}
	merged := user.Override(proj)
	got := merged.BashRewriteRules()
	if len(got) != 1 || got[0].String() != "npm -> pnpm" {
		t.Fatalf("a project layer replaced the user's rules: %v", got)
	}
}

func TestProjectLayerMayTurnBashRewriteOff(t *testing.T) {
	user := Settings{BashRewrite: &BashRewriteSettings{Rules: rules("npm", "pnpm")}}
	merged := user.Override(Settings{BashRewrite: &BashRewriteSettings{Enabled: ptr(false), Rules: rules("a", "b")}})
	if len(merged.BashRewriteRules()) != 0 {
		t.Fatal("the project layer could not turn the table off")
	}
	if merged.BashRewrite.Rules[0].Match != "npm" {
		t.Fatal("the project layer replaced the user's rules")
	}
}

func TestUserLayersReplaceRulesAsAWhole(t *testing.T) {
	a := &BashRewriteSettings{Rules: rules("npm", "pnpm")}
	b := &BashRewriteSettings{Rules: rules("pip", "uv pip")}
	out := mergeBashRewrite(a, b, false)
	if len(out.Rules) != 1 || out.Rules[0].Match != "pip" {
		t.Fatalf("rules = %v", out.Rules)
	}
	out = mergeBashRewrite(a, &BashRewriteSettings{Enabled: ptr(false)}, false)
	if len(out.Rules) != 1 || out.Enabled == nil || *out.Enabled {
		t.Fatalf("out = %+v", out)
	}
}
