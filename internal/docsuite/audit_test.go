package docsuite

import (
	"regexp"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/docparity"
)

// siteAuditKinds expands the website's event list. An entry such as
// `host.first_seen · version_changed` names several kinds that share a prefix.
func siteAuditKinds(t *testing.T) map[string]bool {
	t.Helper()
	src := docparity.Read(t, "site/src/components/sections/WebSessions.astro")
	block := regexp.MustCompile(`(?s)const audit = \[(.*?)\n\];`).FindStringSubmatch(src)
	if block == nil {
		t.Fatal("no audit array in WebSessions.astro")
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`\['([^']+)',`).FindAllStringSubmatch(block[1], -1) {
		prefix := ""
		for _, part := range strings.Split(m[1], " · ") {
			if i := strings.Index(part, "."); i >= 0 {
				prefix = part[:i+1]
				out[part] = true
			} else {
				out[prefix+part] = true
			}
		}
	}
	return out
}

// TestEveryAuditKindIsDocumentedOnThePageAndTheSite keeps docs/audit.md and the
// website section equal to the kinds internal/audit defines, in both directions.
func TestEveryAuditKindIsDocumentedOnThePageAndTheSite(t *testing.T) {
	doc := docparity.Read(t, "docs/audit.md")
	site := siteAuditKinds(t)
	want := map[string]bool{}
	for _, k := range audit.Kinds() {
		want[string(k)] = true
		if !strings.Contains(doc, "`"+string(k)+"`") {
			t.Errorf("docs/audit.md does not document the kind %s", k)
		}
		if !site[string(k)] {
			t.Errorf("the website's audit list does not show the kind %s", k)
		}
	}
	for k := range site {
		if !want[k] {
			t.Errorf("the website's audit list shows %s, which internal/audit does not define", k)
		}
	}
	for _, m := range regexp.MustCompile("`((?:host|worker|card|repo|gate|vex|finding)\\.[a-z_]+)`").FindAllStringSubmatch(doc, -1) {
		if !want[m[1]] {
			t.Errorf("docs/audit.md names %s, which internal/audit does not define", m[1])
		}
	}
}

// TestDispatchKindsMatchTheDaemon keeps the request kinds listed for
// host.dispatch, on the page and on the site, equal to the ones the daemon
// accepts.
func TestDispatchKindsMatchTheDaemon(t *testing.T) {
	daemon := docparity.Read(t, "internal/rc/daemon.go")
	line := regexp.MustCompile(`case ("start", [^:]*"avatar"):`).FindStringSubmatch(daemon)
	if line == nil {
		t.Fatal("the dispatch case list moved in internal/rc/daemon.go")
	}
	var kinds []string
	for _, k := range regexp.MustCompile(`"([a-z_]+)"`).FindAllStringSubmatch(line[1], -1) {
		kinds = append(kinds, k[1])
	}
	doc := docparity.Read(t, "docs/audit.md")
	site := docparity.Read(t, "site/src/components/sections/WebSessions.astro")
	list := "(" + strings.Join(kinds, ", ")
	if len(kinds) < 9 {
		t.Fatalf("only %d dispatch kinds found: %v", len(kinds), kinds)
	}
	if !strings.Contains(doc, "`"+strings.Join(kinds, "`, `")+"`") {
		t.Errorf("docs/audit.md does not list the dispatch kinds %v in order", kinds)
	}
	if !strings.Contains(site, list+")") {
		t.Errorf("the website audit list does not show the dispatch kinds %v", kinds)
	}
}
