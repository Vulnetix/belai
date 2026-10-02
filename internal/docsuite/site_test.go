package docsuite

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// siteRail returns the section ids of the status rail in site/src/lib/sections.ts,
// in order. The rail, the jump menu and every chapter label read that one list.
func siteRail(t *testing.T) []string {
	t.Helper()
	list := docparity.Read(t, "site/src/lib/sections.ts")
	block := regexp.MustCompile(`(?s)export const sections = \[(.*?)\] as const`).FindStringSubmatch(list)
	if block == nil {
		t.Fatal("no sections array in sections.ts")
	}
	var ids []string
	for _, m := range regexp.MustCompile(`\['([a-z-]+)',`).FindAllStringSubmatch(block[1], -1) {
		ids = append(ids, m[1])
	}
	return ids
}

// TestSitePageListsTheSectionsInOrder keeps the Layout paragraph of
// docs/site.md equal to the page's status rail, in the same order.
func TestSitePageListsTheSectionsInOrder(t *testing.T) {
	doc := docparity.Read(t, "docs/site.md")
	block := regexp.MustCompile(`(?s)Section order:\n\n(.*?)\n\n`).FindStringSubmatch(doc)
	if block == nil {
		t.Fatal("docs/site.md has no Section order paragraph")
	}
	alias := map[string]string{
		"hero": "intro", "agents & crews": "agents", "session intelligence": "intel",
		"web sessions": "web", "pix sandbox": "pix",
	}
	var got []string
	for _, name := range strings.Split(strings.Join(strings.Fields(block[1]), " "), " · ") {
		if id, ok := alias[name]; ok {
			name = id
		}
		got = append(got, name)
	}
	want := siteRail(t)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("docs/site.md lists\n  %v\nbut the page's rail is\n  %v", got, want)
	}
}

// TestEveryRailSectionExistsOnThePage checks each rail id is an element id in
// a section component, so no rail link points nowhere.
func TestEveryRailSectionExistsOnThePage(t *testing.T) {
	root := docparity.Root(t)
	var all strings.Builder
	files, _ := filepath.Glob(filepath.Join(root, "site", "src", "components", "sections", "*.astro"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
	}
	for _, id := range siteRail(t) {
		if !strings.Contains(all.String(), `id="`+id+`"`) {
			t.Errorf("the rail links to #%s, which no section declares", id)
		}
	}
}

// TestSitePageNamesRealAssetsAndInfrastructure keeps the poses, editor marks,
// recipes, Terraform resources and deploy check docs/site.md names real.
func TestSitePageNamesRealAssetsAndInfrastructure(t *testing.T) {
	root := docparity.Root(t)
	doc := docparity.Read(t, "docs/site.md")

	poses, _ := filepath.Glob(filepath.Join(root, "site", "src", "assets", "pix", "*.svg"))
	if len(poses) != 8 || !strings.Contains(doc, "holds eight poses") {
		t.Errorf("site has %d poses, the page says eight", len(poses))
	}
	for _, p := range []string{"agreeable", "contemplative", "professor", "sentinel", "facepalm", "yolo", "conductor", "homestead"} {
		if !strings.Contains(doc, p) {
			t.Errorf("docs/site.md does not name the %s pose", p)
		}
		if _, err := os.Stat(filepath.Join(root, "site", "src", "assets", "pix", "pix-"+p+".svg")); err != nil {
			t.Errorf("pose %s: %v", p, err)
		}
	}
	for _, e := range []string{"zed", "jetbrains", "neovim", "emacs", "vscode"} {
		if _, err := os.Stat(filepath.Join(root, "site", "src", "assets", "editors", e+".svg")); err != nil {
			t.Errorf("editor mark %s: %v", e, err)
		}
	}

	just := docparity.Read(t, "justfile")
	for _, r := range []string{"site-dev", "site-build", "site-check", "shots"} {
		if !strings.Contains(just, "\n"+r+":") {
			t.Errorf("docs/site.md names `just %s`, but the justfile has no such recipe", r)
		}
	}

	tf := docparity.Read(t, "site/terraform/dns.tf") + docparity.Read(t, "site/terraform/pages.tf") + docparity.Read(t, "site/terraform/variables.tf")
	for _, want := range []string{`resource "cloudflare_dns_record" "belai"`, `resource "github_repository_pages" "belai"`, `variable "manage_pages"`} {
		if !strings.Contains(tf, want) {
			t.Errorf("site/terraform has no %s", want)
		}
	}
	for _, want := range []string{"`cloudflare_dns_record.belai`", "terraform import github_repository_pages.belai belai", "`var.manage_pages = false`"} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/site.md does not say %s", want)
		}
	}

	pages := docparity.Read(t, ".github/workflows/pages.yml")
	if !strings.Contains(pages, "dist/CNAME") || !strings.Contains(pages, "belai.vulnetix.com") {
		t.Error("pages.yml no longer checks dist/CNAME for belai.vulnetix.com")
	}
	if cname := strings.TrimSpace(docparity.Read(t, "site/public/CNAME")); cname != "belai.vulnetix.com" {
		t.Errorf("site/public/CNAME = %q, want belai.vulnetix.com", cname)
	}
}
