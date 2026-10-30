// Package attribution builds the trailer block that every Pix [bot] and
// vulnetix[bot] action on GitHub carries. A commit message gets every line; a
// pull request body, an issue, a comment or a review gets the links only
// (Session, Finding, Vulnerability, Advisory), because Co-authored-by and
// Signed-off-by mean something to git and nothing in Markdown. Code appends the
// block after the text is written, so a model never has to reproduce an email
// address or a link.
//
// It mirrors website/src/shared/attribution-trailers.ts and vdb-site/api/internal/attribution. The cases in
// testdata/attribution-cases.json are the website's attribution-cases.json;
// change a rule and its case in the website, vdb-site, belai and vdb-manager
// together.
package attribution

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/vulnetix/belai/internal/vulnid"
)

// Bot is the identity that acts and signs off.
type Bot string

const (
	Pix      Bot = "pix"
	Vulnetix Bot = "vulnetix"
)

const (
	// Origin is the site the links point at.
	Origin = "https://vulnetix.com"
	// AdvisoryRepoURL is the public GCVE advisory repository.
	AdvisoryRepoURL = "https://github.com/Vulnetix/advisory-database"
	// MaxVulnLinks caps the Vulnerability lines of one message.
	MaxVulnLinks = 10
)

// Human is who started the work.
type Human struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Context is everything a trailer block can name. Empty fields are left out.
type Context struct {
	Bot Bot `json:"bot"`
	// Human is nil for a cron, a webhook or a batch job.
	Human *Human `json:"human,omitempty"`
	// CommitAs is the person whose GitHub user makes and signs the commit with their own signing
	// key. They sign off and Bot becomes the co-author. It replaces Human; an unusable value is
	// ignored and the bot signs off as usual.
	CommitAs    *Human   `json:"commitAs,omitempty"`
	SessionID   string   `json:"sessionId,omitempty"`
	FindingUUID string   `json:"findingUuid,omitempty"`
	VulnIDs     []string `json:"vulnIds,omitempty"`
	// AdvisoryPath is the record's path inside Vulnetix/advisory-database.
	AdvisoryPath string `json:"advisoryPath,omitempty"`
	Origin       string `json:"origin,omitempty"`
}

// Bots are the identities that sign off. The Vulnetix address is the GitHub
// App's noreply address, the one its own commits use.
var Bots = map[Bot]Human{
	Pix:      {Name: "Pix [bot]", Email: "security@vulnetix.com"},
	Vulnetix: {Name: "vulnetix[bot]", Email: "176591487+vulnetix[bot]@users.noreply.github.com"},
}

var (
	sessionID    = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	findingUUID  = regexp.MustCompile(`(?i)^[0-9a-f-]{8,64}$`)
	gcveID       = regexp.MustCompile(`^GCVE-\d{1,6}-[A-Za-z0-9-]{1,64}$`)
	advisoryPath = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,300}$`)
	emailShape   = regexp.MustCompile(`^[^\s<>@]+@[^\s<>@]+$`)
	trailerLine  = regexp.MustCompile(`^[A-Za-z][A-Za-z-]*: \S`)
	markerLine   = regexp.MustCompile(`^<!--.*-->$`)
	paragraphs   = regexp.MustCompile(`\n\s*\n`)
	nameStrip    = strings.NewReplacer("<", " ", ">", " ", "\r", " ", "\n", " ")
)

func cleanHuman(h *Human) *Human {
	if h == nil {
		return nil
	}
	name := strings.Join(strings.Fields(nameStrip.Replace(h.Name)), " ")
	email := strings.TrimSpace(h.Email)
	if name == "" || !emailShape.MatchString(email) {
		return nil
	}

	return &Human{Name: name, Email: email}
}

func person(h Human) string { return h.Name + " <" + h.Email + ">" }

// isVulnID is strict like the website's isVulnId: only the canonical form.
func isVulnID(id string) bool {
	c, ok := vulnid.Valid(id)

	return ok && c == id
}

// AdvisoryURL is the link for a published GCVE record, or the repository when
// its path is not known.
func AdvisoryURL(path string) string {
	p := strings.TrimLeft(path, "/")
	if p != "" && advisoryPath.MatchString(p) {
		clean := true
		for _, seg := range strings.Split(p, "/") {
			if seg == ".." {
				clean = false
			}
		}
		if clean {
			return AdvisoryRepoURL + "/blob/main/" + p
		}
	}

	return AdvisoryRepoURL
}

// Trailers returns the trailer lines in order. With no usable human the other
// bot is the co-author, so the line is always there.
func Trailers(c Context) []string {
	origin := strings.TrimRight(c.Origin, "/")
	if origin == "" {
		origin = Origin
	}
	other := Bots[Pix]
	if c.Bot == Pix {
		other = Bots[Vulnetix]
	}
	co := other
	if h := cleanHuman(c.Human); h != nil {
		co = *h
	}
	lines := []string{"Co-authored-by: " + person(co), "Signed-off-by: " + person(Bots[c.Bot])}
	if signer := cleanHuman(c.CommitAs); signer != nil {
		lines = []string{"Co-authored-by: " + person(Bots[c.Bot]), "Signed-off-by: " + person(*signer)}
	}

	if c.SessionID != "" && sessionID.MatchString(c.SessionID) {
		lines = append(lines, "Session: "+origin+"/resolve/factory-session?id="+url.QueryEscape(c.SessionID))
	}
	if c.FindingUUID != "" && findingUUID.MatchString(c.FindingUUID) {
		lines = append(lines, "Finding: "+origin+"/resolve/scanner-results/finding/"+c.FindingUUID)
	}

	seen := map[string]bool{}
	gcve := false
	n := 0
	for _, id := range c.VulnIDs {
		if seen[id] || (!isVulnID(id) && !gcveID.MatchString(id)) {
			continue
		}
		seen[id] = true
		if n == MaxVulnLinks {
			break
		}
		n++
		lines = append(lines, "Vulnerability: "+origin+"/vdb/"+url.QueryEscape(id))
		if gcveID.MatchString(id) {
			gcve = true
		}
	}
	if gcve {
		lines = append(lines, "Advisory: "+AdvisoryURL(c.AdvisoryPath))
	}

	return lines
}

// Links is Trailers without Co-authored-by and Signed-off-by: what a pull
// request body, an issue, a comment or a review carries. It may be empty.
func Links(c Context) []string {
	var out []string
	for _, l := range Trailers(c) {
		if !strings.HasPrefix(l, "Co-authored-by: ") && !strings.HasPrefix(l, "Signed-off-by: ") {
			out = append(out, l)
		}
	}

	return out
}

// Append adds the block to the end of a message. It is safe to run twice: a
// line already present is not added again. In a commit message the lines join
// the last paragraph when that is already all trailers ("Refs: #12"), so git
// reads one block. With markdown set (a body or a comment) the block goes before
// a trailing hidden marker and always after a blank line, and holds Links only.
func Append(text string, c Context, markdown bool) string {
	all := strings.Split(text, "\n")
	end := len(all)

	if markdown {
		for end > 0 {
			l := strings.TrimSpace(all[end-1])
			if l != "" && !markerLine.MatchString(l) {
				break
			}
			end--
		}
	}
	body := strings.TrimRightFunc(strings.Join(all[:end], "\n"), unicode.IsSpace)
	tail := ""
	if markdown {
		tail = strings.TrimLeftFunc(strings.Join(all[end:], "\n"), unicode.IsSpace)
	}

	present := map[string]bool{}
	for _, l := range strings.Split(body, "\n") {
		present[strings.TrimSpace(l)] = true
	}
	lines := Trailers(c)
	if markdown {
		lines = Links(c)
	}
	var missing []string
	for _, l := range lines {
		if !present[l] {
			missing = append(missing, l)
		}
	}
	if len(missing) == 0 {
		return text
	}

	paras := paragraphs.Split(body, -1)
	joins := !markdown && len(paras) > 1
	if joins {
		for _, l := range strings.Split(paras[len(paras)-1], "\n") {
			if !trailerLine.MatchString(l) {
				joins = false
			}
		}
	}
	sep := ""
	if body != "" {
		sep = "\n\n"
		if joins {
			sep = "\n"
		}
	}
	out := body + sep + strings.Join(missing, "\n")

	if tail != "" {
		return out + "\n\n" + tail
	}
	if strings.HasSuffix(text, "\n") {
		return out + "\n"
	}

	return out
}
