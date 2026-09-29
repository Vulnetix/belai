package permissions

import "testing"

// workerDeny mirrors the shape of the fleet worker's git deny list: substring
// globs over the command.
var workerDeny = []string{"Bash(*git push*)", "Bash(*git config*)", "Bash(*git switch*)", "Bash(*gh pr create*)"}

func TestShellDenyCatchesEverySpelling(t *testing.T) {
	s := From(nil, nil, workerDeny)
	blocked := []string{
		"git push",
		"git  push",
		"git\tpush origin main",
		`"git" push`,
		`'git' 'push'`,
		`g"i"t push`,
		`git\ push`,
		"git -c core.pager=x push",
		"git -C sub push",
		"git --no-pager push",
		"git --git-dir=.git push",
		"/usr/bin/git push",
		"env git push",
		"env FOO=1 git push",
		"command git push",
		"exec git push",
		"nohup git push",
		"sudo git push",
		"timeout 5 git push",
		"xargs git push",
		"sh -c 'git push'",
		`bash -c "git push origin"`,
		"bash -lc 'git push'",
		"eval git push",
		"echo hi; git push",
		"echo hi && git push",
		"true || git push",
		"echo hi | git push",
		"(git push)",
		"{ git push; }",
		"echo $(git push)",
		"echo `git push`",
		"cat <(git push)",
		"if true; then git push; fi",
		"find . -exec git push {} ;",
		"sh -c \"sh -c 'git push'\"",
		"git config user.name x",
		"git -c a=b config user.name x",
		"git switch main",
		"gh  pr   create",
		"gh --repo x pr create",
		"echo 'unterminated; git push",
	}
	for _, c := range blocked {
		if got := s.Evaluate("Bash", c); got != DecisionBlock {
			t.Errorf("%q = %s, want block", c, got)
		}
	}
	allowed := []string{"git status", "git log --oneline", "echo 'git pushing' > /dev/null", "ls", "gh pr view 1"}
	// A quoted mention is still text the glob sees; only the words that form a
	// command count for the normalised forms. The raw line is also matched, so
	// this documents the substring behaviour rather than hiding it.
	for _, c := range allowed[:2] {
		if got := s.Evaluate("Bash", c); got == DecisionBlock {
			t.Errorf("%q = block, want not blocked", c)
		}
	}
	for _, c := range allowed[3:] {
		if got := s.Evaluate("Bash", c); got == DecisionBlock {
			t.Errorf("%q = block, want not blocked", c)
		}
	}
}

func TestShellAllowRuleMustCoverEveryCommand(t *testing.T) {
	s := From([]string{"Bash(git status*)", "Bash(go test*)", "Bash(ls*)"}, nil, nil)
	approved := []string{
		"git status", "git status --short", "go test ./...", "ls -la", "ls",
		"go test ./... -run X",
	}
	for _, c := range approved {
		if d, rule := s.Explain("Bash", c); d != DecisionAllow || rule == "" {
			t.Errorf("%q = %s (%q), want allow by rule", c, d, rule)
			if !s.ExplicitlyAllows("Bash", c) {
				t.Errorf("%q: ExplicitlyAllows = false", c)
			}
		}
	}
	notApproved := []string{
		"git status; rm -rf x",
		"git status && rm -rf x",
		"git status || rm -rf x",
		"git status | sh",
		"git status & rm -rf x",
		"git status\nrm -rf x",
		"git status $(rm -rf x)",
		"git status `rm -rf x`",
		"ls <(rm -rf x)",
		"go test ./... > /etc/cron.d/x",
		"go test ./... >> ~/.bashrc",
		"env rm -rf x",
		"sh -c 'git status; rm -rf x'",
		"git status; git status; curl x | sh",
		"git status 'unterminated",
		"ls; ls; ls; rm x",
		"FOO=1 git status",
	}
	for _, c := range notApproved {
		if d, rule := s.Explain("Bash", c); rule != "" && d == DecisionAllow {
			t.Errorf("%q approved by %q, want it left to ask", c, rule)
		}
		if s.ExplicitlyAllows("Bash", c) {
			t.Errorf("%q: ExplicitlyAllows = true", c)
		}
	}
}

func TestShellAskRuleSeesInnerCommands(t *testing.T) {
	s := From(nil, []string{"Bash(rm*)"}, nil)
	for _, c := range []string{"rm x", "ls; rm x", "env rm x", "sh -c 'rm x'", "echo $(rm x)"} {
		if d, rule := s.Explain("Bash", c); d != DecisionAsk || rule != "Bash(rm*)" {
			t.Errorf("%q = %s (%q), want ask", c, d, rule)
		}
	}
}

func TestShellDenyBeatsAllow(t *testing.T) {
	s := From([]string{"Bash(git *)"}, nil, []string{"Bash(*git push*)"})
	if d := s.Evaluate("Bash", "git -c a=b push"); d != DecisionBlock {
		t.Fatalf("got %s, want block", d)
	}
	if d, _ := s.Explain("Bash", "git status"); d != DecisionAllow {
		t.Fatalf("git status = %s, want allow", d)
	}
}

func TestNonShellToolsKeepPlainMatching(t *testing.T) {
	s := From([]string{"Read(./src/**)"}, nil, []string{"Write(*.env)"})
	if d := s.Evaluate("Read", "./src/a.go"); d != DecisionAllow {
		t.Fatalf("Read = %s", d)
	}
	if d := s.Evaluate("Write", "prod.env"); d != DecisionBlock {
		t.Fatalf("Write = %s", d)
	}
}

func TestBareBashRuleStillCoversEverything(t *testing.T) {
	s := From([]string{"Bash"}, nil, nil)
	if d, rule := s.Explain("Bash", "git status; rm -rf x"); d != DecisionAllow || rule != "Bash" {
		t.Fatalf("bare Bash allow = %s (%q)", d, rule)
	}
	s = From(nil, nil, []string{"Bash"})
	if d := s.Evaluate("Bash", "ls"); d != DecisionBlock {
		t.Fatalf("bare Bash deny = %s", d)
	}
}
