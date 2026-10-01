package main

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestKiroPageFlagTableMatchesLogin keeps the flag table in docs/kiro.md equal
// to the flags `belai login kiro` defines: none missing, none invented.
func TestKiroPageFlagTableMatchesLogin(t *testing.T) {
	def := regexp.MustCompile(`fs\.(?:String|Bool)\("([a-z-]+)"`)
	var code []string
	for _, m := range def.FindAllStringSubmatch(docparity.Read(t, "cmd/belai/login.go"), -1) {
		code = append(code, "-"+m[1])
	}
	row := regexp.MustCompile("(?m)^\\| `(-[a-z-]+)` \\|")
	var doc []string
	for _, m := range row.FindAllStringSubmatch(docparity.Read(t, "docs/kiro.md"), -1) {
		doc = append(doc, m[1])
	}
	sort.Strings(code)
	sort.Strings(doc)
	if len(code) < 6 || strings.Join(code, " ") != strings.Join(doc, " ") {
		t.Errorf("login kiro flags %v, docs/kiro.md table %v", code, doc)
	}
}
