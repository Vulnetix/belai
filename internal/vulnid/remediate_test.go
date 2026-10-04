package vulnid

import "testing"

func TestIsRemediation(t *testing.T) {
	id := "GHSA-5cv4-jp36-h3mw"
	for _, tc := range []struct {
		name, prompt string
		want         bool
	}{
		{"prepared prompt", RemediationPrompt(id), true},
		{"copied row", URL(id) + "\n" + Command(id), true},
		{"copied link only", URL(id), true},
		{"bare identifier", id, true},
		{"fix verb", "fix " + id + " please", true},
		{"patch verb", "patch CVE-2021-44228 in this repo", true},
		{"bump", "bump the package for " + id, true},
		{"question", "what is " + id + "?", false},
		{"explain", "explain " + id + " and how to fix it", false},
		{"no identifier", "fix the vulnerabilities", false},
		{"mention", "the log mentions " + id + " in passing", false},
		{"empty", "", false},
	} {
		if got := IsRemediation(tc.prompt); got != tc.want {
			t.Errorf("%s: IsRemediation(%q) = %v, want %v", tc.name, tc.prompt, got, tc.want)
		}
	}
	long := "fix " + id + " " + string(make([]byte, remediationPromptMax))
	if IsRemediation(long) {
		t.Error("a long prompt counted as a remediation request")
	}
}
