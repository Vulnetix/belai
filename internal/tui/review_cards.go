package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/commands"
	"github.com/vulnetix/belai/internal/scanartifacts"
)

// reviewCard is one scanner's result, in that scanner's own terms: an SBOM
// inventories packages, a CBOM algorithms, a malware scan returns a verdict.
// Only the scanners that look for problems count issues.
type reviewCard struct {
	headline  string
	lines     []string
	attention bool // something the user should act on
	issues    int  // actionable results, for the review's closing total
}

// reviewCards maps each review activity to the template that describes its
// result. A scanner missing here gets genericCard.
var reviewCards = map[string]func(commands.ScanOutcome) reviewCard{
	"sca":        scaCard,
	"sast":       sarifIssuesCard("code issue", "code issues", "no code issues"),
	"secrets":    sarifIssuesCard("exposed secret", "exposed secrets", "no secrets in the working tree"),
	"iac":        sarifIssuesCard("misconfiguration", "misconfigurations", "no misconfigurations"),
	"containers": containersCard,
	"malscan":    malscanCard,
	"sbom":       sbomCard,
	"aibom":      aibomCard,
	"cbom":       cbomCard,
}

// cardFor returns the scanner's card.
func cardFor(o commands.ScanOutcome) reviewCard {
	if f, ok := reviewCards[o.Name]; ok {
		return f(o)
	}
	return genericCard(o)
}

// genericCard is the fallback for an activity without its own template.
func genericCard(o commands.ScanOutcome) reviewCard {
	return issuesCard(o.Findings, o.Counts, "finding", "findings", "no findings")
}

// issuesCard is the headline of a scanner that looks for problems.
func issuesCard(n int, c scanartifacts.Counts, one, many, none string) reviewCard {
	if n == 0 {
		return reviewCard{headline: none}
	}
	h := "**" + nounCount(n, one, many) + "**"
	if s := severityBreakdown(c); s != "" {
		h += " · " + s
	}
	return reviewCard{headline: h, attention: true, issues: n}
}

// sarifIssuesCard is the card of a rule-based scanner: its results and how
// many of its rules fired.
func sarifIssuesCard(one, many, none string) func(commands.ScanOutcome) reviewCard {
	return func(o commands.ScanOutcome) reviewCard {
		if o.SARIF == nil {
			return issuesCard(o.Findings, o.Counts, one, many, none)
		}
		card := issuesCard(o.SARIF.Results, o.SARIF.Counts, one, many, none)
		if l := rulesLine(o.SARIF); l != "" {
			card.lines = append(card.lines, l)
		}
		return card
	}
}

func rulesLine(f *scanartifacts.RunFacts) string {
	switch {
	case f.RulesEvaluated > 0:
		return fmt.Sprintf("%s of %s triggered", thousands(f.RulesTriggered), nounCount(f.RulesEvaluated, "rule", "rules"))
	case f.RulesTriggered > 0:
		return nounCount(f.RulesTriggered, "rule", "rules") + " triggered"
	}
	return ""
}

// scaCard: known vulnerabilities in the dependencies, license issues, and
// the packages the scan covered.
func scaCard(o commands.ScanOutcome) reviewCard {
	if o.BOM == nil {
		return issuesCard(o.Findings, o.Counts, "vulnerability", "vulnerabilities", "no known vulnerabilities")
	}
	b := o.BOM
	card := issuesCard(b.Vulns.Total(), b.Vulns, "vulnerability", "vulnerabilities", "no known vulnerabilities")
	if b.Licenses > 0 {
		card.lines = append(card.lines, nounCount(b.Licenses, "license issue", "license issues"))
		card.attention = true
	}
	if l := packagesLine(b, b.Components); l != "" {
		card.lines = append(card.lines, l)
	}
	return card
}

// containersCard: issues in container build files, and the images the
// scanner inventoried.
func containersCard(o commands.ScanOutcome) reviewCard {
	card := sarifIssuesCard("container issue", "container issues", "no container issues")(o)
	if o.BOM != nil && o.BOM.Components > 0 {
		card.lines = append(card.lines, nounCount(o.BOM.Components, "image", "images")+" inventoried")
	}
	return card
}

// malscanCard: a verdict, and how much the scanner looked at.
func malscanCard(o commands.ScanOutcome) reviewCard {
	f := o.SARIF
	if f == nil {
		return issuesCard(o.Findings, o.Counts, "malware indicator", "malware indicators", "clean")
	}
	var card reviewCard
	switch {
	case f.Malicious || f.Results > 0:
		n := max(f.Results, 1)
		card = reviewCard{headline: "**malicious** · " + nounCount(f.Results, "indicator", "indicators") + " matched", attention: true, issues: n}
	case f.FilesScanned == 0:
		// malscan scans installed dependencies only, and only the install
		// directories directly under the repository root; home caches such
		// as ~/go/pkg/mod need --include-home. Nothing inspected is not a
		// clean verdict, so the card says what was missing instead.
		return reviewCard{
			headline: "**nothing to scan**",
			lines:    []string{"no dependency install directory at the repository root (node_modules, .venv, vendor, …); malscan inspects installed packages only"},
		}
	default:
		card = reviewCard{headline: "**clean**"}
	}
	card.lines = append(card.lines, nounCount(f.FilesScanned, "file", "files")+" scanned · "+nounCount(f.Indicators, "indicator", "indicators")+" checked")
	return card
}

// sbomCard: the package inventory, by ecosystem.
func sbomCard(o commands.ScanOutcome) reviewCard {
	if o.BOM == nil {
		return reviewCard{headline: "no inventory written"}
	}
	n, ok := o.BOM.MetaInt("vulnetix:sbom/packages-detected")
	if !ok {
		n = o.BOM.Components
	}
	card := reviewCard{headline: "**" + nounCount(n, "package", "packages") + " inventoried**"}
	if l := ecosystemsLine(o.BOM); l != "" {
		card.lines = append(card.lines, l)
	}
	return card
}

// aiLibraryCategory and aiModelCategory are the AI-BOM categories that are
// not tools; every other category (coding agents, conventions, …) is one.
const (
	aiLibraryCategory = "ai-sdk"
	aiModelCategory   = "model"
)

// aibomCard: the AI tools, SDKs and models the repository uses.
func aibomCard(o commands.ScanOutcome) reviewCard {
	b := o.BOM
	if b == nil {
		return reviewCard{headline: "no AI inventory written"}
	}
	var toolCats []string
	toolCount := 0
	for cat, n := range b.Categories {
		if cat != aiLibraryCategory && cat != aiModelCategory {
			toolCats = append(toolCats, cat)
			toolCount += n
		}
	}
	sort.Strings(toolCats)
	tools := metaOr(b, "vulnetix:aibom/tools-detected", toolCount)
	libs := metaOr(b, "vulnetix:aibom/libraries-detected", b.Categories[aiLibraryCategory])
	models := metaOr(b, "vulnetix:aibom/models-detected", b.Categories[aiModelCategory])
	if tools+libs+models == 0 {
		return reviewCard{headline: "no AI usage detected"}
	}
	card := reviewCard{headline: "**" + strings.Join([]string{
		nounCount(tools, "AI tool", "AI tools"),
		nounCount(libs, "AI library", "AI libraries"),
		nounCount(models, "model", "models"),
	}, " · ") + "**"}
	var toolNames []string
	for _, cat := range toolCats {
		toolNames = append(toolNames, b.Names[cat]...)
	}
	if l := namesLine("tools", toolNames, toolCount); l != "" {
		card.lines = append(card.lines, l)
	}
	if l := namesLine("libraries", b.Names[aiLibraryCategory], b.Categories[aiLibraryCategory]); l != "" {
		card.lines = append(card.lines, l)
	}
	if l := namesLine("models", b.Names[aiModelCategory], b.Categories[aiModelCategory]); l != "" {
		card.lines = append(card.lines, l)
	}
	return card
}

// cbomCard: the cryptography in use and its post-quantum standing. A
// deprecated or quantum-vulnerable algorithm is named, since that is what
// the user would change.
func cbomCard(o commands.ScanOutcome) reviewCard {
	b := o.BOM
	if b == nil {
		return reviewCard{headline: "no cryptography inventory written"}
	}
	algs := metaOr(b, "vulnetix:cbom/algorithms-detected", b.Categories["algorithm"])
	certs := metaOr(b, "vulnetix:cbom/certificates-detected", b.Categories["certificate"])
	libs := metaOr(b, "vulnetix:cbom/libraries-detected", b.Categories["library"])
	if algs+certs+libs == 0 {
		return reviewCard{headline: "no cryptography detected"}
	}
	card := reviewCard{headline: "**" + strings.Join([]string{
		nounCount(algs, "algorithm", "algorithms"),
		nounCount(certs, "certificate", "certificates"),
		nounCount(libs, "library", "libraries"),
	}, " · ") + "**"}
	safe := metaOr(b, "vulnetix:cbom/quantum-safe", b.PQCCounts["quantum-safe"])
	vulnerable := metaOr(b, "vulnetix:cbom/quantum-vulnerable", b.PQCCounts["quantum-vulnerable"])
	hybrid := metaOr(b, "vulnetix:cbom/hybrid", b.PQCCounts["hybrid"])
	deprecated := metaOr(b, "vulnetix:cbom/deprecated", b.PQCCounts["deprecated"])
	card.lines = append(card.lines, fmt.Sprintf("quantum-safe %d · quantum-vulnerable %d · hybrid %d", safe, vulnerable, hybrid))
	if deprecated > 0 {
		card.lines = append(card.lines, "**deprecated:** "+nameList(b.PQC["deprecated"], deprecated))
		card.attention = true
	}
	if vulnerable > 0 {
		card.lines = append(card.lines, "**quantum-vulnerable:** "+nameList(b.PQC["quantum-vulnerable"], vulnerable))
		card.attention = true
	}
	return card
}

// metaOr is a metadata count, or fallback when the generator did not record
// it.
func metaOr(b *scanartifacts.BOMFacts, name string, fallback int) int {
	if n, ok := b.MetaInt(name); ok {
		return n
	}
	return fallback
}

// packagesLine is "N packages · npm 383 · golang 149 · …".
func packagesLine(b *scanartifacts.BOMFacts, n int) string {
	if n == 0 {
		return ""
	}
	line := nounCount(n, "package", "packages")
	if eco := ecosystemsLine(b); eco != "" {
		line += " · " + eco
	}
	return line
}

// ecosystemsLine lists the largest ecosystems first.
func ecosystemsLine(b *scanartifacts.BOMFacts) string {
	top := b.TopEcosystems()
	const shown = 5
	var parts []string
	for i, t := range top {
		if i == shown {
			parts = append(parts, fmt.Sprintf("+%d more", len(top)-shown))
			break
		}
		parts = append(parts, cardName(t.Name)+" "+thousands(t.N))
	}
	return strings.Join(parts, " · ")
}

// namesLine is "label: a, b, c +k more", or "" with no names.
func namesLine(label string, names []string, total int) string {
	if len(names) == 0 {
		return ""
	}
	return label + ": " + nameList(names, total)
}

// nameList joins up to four names and says how many more there are.
func nameList(names []string, total int) string {
	const shown = 4
	var parts []string
	for i, n := range names {
		if i == shown {
			break
		}
		parts = append(parts, cardName(n))
	}
	out := strings.Join(parts, ", ")
	if more := max(total, len(names)) - len(parts); more > 0 {
		out += fmt.Sprintf(" +%d more", more)
	}
	return out
}

// cardName makes an artifact-supplied name safe for one card line. The name
// comes from the repository by way of the scanner, so it is flattened and
// stripped of control and bidi runes, loses any markdown backticks and
// asterisks, and is cut to a readable length. Cards are display-only.
func cardName(s string) string {
	s = auditLine(ansi.Strip(s))
	s = strings.NewReplacer("`", "'", "*", "").Replace(s)
	const maxRunes = 48
	if utf8.RuneCountInString(s) > maxRunes {
		r := []rune(s)
		s = string(r[:maxRunes-1]) + "…"
	}
	return strings.TrimSpace(s)
}

// nounCount is "1 package" / "3 packages", with thousands separators.
func nounCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return thousands(n) + " " + many
}

// thousands formats n as 246,709.
func thousands(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
