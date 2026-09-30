// Package vex composes OpenVEX 0.2.0 documents for the security crew's
// verdicts and writes them under a repository's `.vulnetix/vex` directory.
//
// The harness composes every byte. A verdict and an enum justification pick
// the status; every string a model supplied (the impact, the action, the
// evidence) is cleaned to one bounded line first, and the document is written
// by this package alone, never through a model's file tool. The finding id
// names the file and must be a plain identifier, so a verdict cannot choose a
// path.
package vex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/sanitize"
)

// Dir is where documents go, relative to the repository root.
const Dir = ".vulnetix/vex"

// Context is the OpenVEX 0.2.0 JSON-LD context.
const Context = "https://openvex.dev/ns/v0.2.0"

// Justification is an OpenVEX `not_affected` justification.
type Justification string

// The five OpenVEX justifications.
const (
	ComponentNotPresent      Justification = "component_not_present"
	VulnerableCodeNotPresent Justification = "vulnerable_code_not_present"
	NotInExecutePath         Justification = "vulnerable_code_not_in_execute_path"
	CannotBeControlled       Justification = "vulnerable_code_cannot_be_controlled_by_adversary"
	InlineMitigations        Justification = "inline_mitigations_already_exist"
)

// Justifications lists every valid justification, for a tool's enum.
func Justifications() []string {
	return []string{
		string(ComponentNotPresent), string(VulnerableCodeNotPresent), string(NotInExecutePath),
		string(CannotBeControlled), string(InlineMitigations),
	}
}

// Valid reports whether j is one of the five.
func (j Justification) Valid() bool {
	for _, v := range Justifications() {
		if string(j) == v {
			return true
		}
	}
	return false
}

// Limits on the model-supplied strings.
const (
	maxStatement = 500
	maxAuthor    = 80
)

// Input is everything a document is composed from.
type Input struct {
	// Finding is the advisory (or rule) id the card carries.
	Finding string
	// Verdict is fixed, false_positive, no_fix or needs_human.
	Verdict kanban.Verdict
	// Package, Ecosystem and Version name the product. With no package the
	// product is the repository itself at Commit.
	Package, Ecosystem, Version string
	Repo                        string // repository name, for a product with no package
	Commit                      string // the commit the verdict was made at
	// Justification is required for false_positive.
	Justification Justification
	// Impact explains a not_affected verdict; Action says what is to be done
	// for an affected one (required for no_fix). Both are cleaned here.
	Impact, Action string
	// Author names the writer; the harness passes the profile name.
	Author string
	// Now stamps the document.
	Now time.Time
	// DocVersion is the document version; 0 means 1.
	DocVersion int
}

var findingRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// FileName is the document's file name for a finding, or "" when the id is
// not a plain identifier.
func FileName(finding string) string {
	if !findingRE.MatchString(finding) {
		return ""
	}
	name := strings.ReplaceAll(finding, ":", "_")
	if strings.HasPrefix(name, ".") {
		return ""
	}
	return name + ".openvex.json"
}

// purlTypes maps a scanner ecosystem name to a package-url type.
var purlTypes = map[string]string{
	"npm": "npm", "golang": "golang", "go": "golang", "pypi": "pypi", "python": "pypi",
	"maven": "maven", "cargo": "cargo", "crates": "cargo", "rubygems": "gem", "gem": "gem",
	"nuget": "nuget", "composer": "composer", "packagist": "composer", "hex": "hex",
	"pub": "pub", "swift": "swift", "cocoapods": "cocoapods",
}

// Purl composes a package URL. An unknown ecosystem is "generic".
func Purl(ecosystem, name, version string) string {
	t := purlTypes[strings.ToLower(strings.TrimSpace(ecosystem))]
	if t == "" {
		t = "generic"
	}
	segs := strings.Split(strings.Trim(name, "/"), "/")
	for i, s := range segs {
		segs[i] = strings.ReplaceAll(url.PathEscape(s), "@", "%40")
	}
	p := "pkg:" + t + "/" + strings.Join(segs, "/")
	if v := strings.TrimSpace(version); v != "" {
		p += "@" + url.PathEscape(v)
	}
	return p
}

func (in Input) product() string {
	if strings.TrimSpace(in.Package) != "" {
		return Purl(in.Ecosystem, in.Package, in.Version)
	}
	name := sanitize.Ident(in.Repo, 80)
	if name == "" {
		name = "repository"
	}
	return Purl("generic", name, in.Commit)
}

type vulnerability struct {
	Name string `json:"name"`
}

type product struct {
	ID string `json:"@id"`
}

type statement struct {
	Vulnerability   vulnerability `json:"vulnerability"`
	Products        []product     `json:"products"`
	Status          string        `json:"status"`
	Justification   string        `json:"justification,omitempty"`
	ImpactStatement string        `json:"impact_statement,omitempty"`
	ActionStatement string        `json:"action_statement,omitempty"`
}

type document struct {
	Context    string      `json:"@context"`
	ID         string      `json:"@id"`
	Author     string      `json:"author"`
	Role       string      `json:"role"`
	Timestamp  string      `json:"timestamp"`
	Version    int         `json:"version"`
	Statements []statement `json:"statements"`
}

// ErrNoDocument is returned for a verdict that has no VEX (rejected).
var ErrNoDocument = errors.New("vex: this verdict has no document")

// Compose builds the document for in.
func Compose(in Input) ([]byte, error) {
	if !findingRE.MatchString(in.Finding) {
		return nil, fmt.Errorf("vex: %q is not a finding id", in.Finding)
	}
	st := statement{
		Vulnerability: vulnerability{Name: in.Finding},
		Products:      []product{{ID: in.product()}},
	}
	switch in.Verdict {
	case kanban.VerdictFixed:
		st.Status = "fixed"
	case kanban.VerdictFalsePositive:
		if !in.Justification.Valid() {
			return nil, errors.New("vex: a false positive needs one of the OpenVEX justifications")
		}
		st.Status, st.Justification = "not_affected", string(in.Justification)
		st.ImpactStatement = sanitize.Line(in.Impact, maxStatement)
	case kanban.VerdictNoFix:
		st.Status = "affected"
		st.ActionStatement = sanitize.Line(in.Action, maxStatement)
		if st.ActionStatement == "" {
			return nil, errors.New("vex: an affected statement needs an action statement")
		}
	case kanban.VerdictNeedsHuman:
		st.Status = "under_investigation"
	default:
		return nil, ErrNoDocument
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	version := in.DocVersion
	if version < 1 {
		version = 1
	}
	author := sanitize.Line(in.Author, maxAuthor)
	if author == "" {
		author = "Belai security crew"
	}
	body, err := json.Marshal(struct {
		Author, Role, Time string
		Version            int
		St                 statement
	}{author, "Automated security verifier", now.UTC().Format(time.RFC3339), version, st})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	doc := document{
		Context:    Context,
		ID:         "urn:belai:vex:" + strings.ReplaceAll(in.Finding, ":", "-") + ":" + hex.EncodeToString(sum[:6]),
		Author:     author,
		Role:       "Automated security verifier",
		Timestamp:  now.UTC().Format(time.RFC3339),
		Version:    version,
		Statements: []statement{st},
	}
	return json.MarshalIndent(doc, "", "  ")
}

// Write composes the document and writes it to <root>/.vulnetix/vex/. It
// returns the path relative to root. root is the trusted repository root,
// never a worktree. A symlink at .vulnetix or .vulnetix/vex is refused, an
// existing document's version is continued, and the file appears whole or not
// at all.
func Write(root string, in Input) (string, error) {
	name := FileName(in.Finding)
	if name == "" {
		return "", fmt.Errorf("vex: %q is not a finding id", in.Finding)
	}
	dir := filepath.Join(root, filepath.FromSlash(Dir))
	for _, d := range []string{filepath.Join(root, ".vulnetix"), dir} {
		if fi, err := os.Lstat(d); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("vex: %s is a symlink", d)
		}
		if err := os.MkdirAll(d, 0o700); err != nil {
			return "", err
		}
	}
	path := filepath.Join(dir, name)
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return "", fmt.Errorf("vex: %s is not a regular file", path)
	}
	if in.DocVersion < 1 {
		in.DocVersion = previousVersion(path) + 1
	}
	data, err := Compose(in)
	if err != nil {
		return "", err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return Dir + "/" + name, nil
}

func previousVersion(path string) int {
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 1<<20 {
		return 0
	}
	var d struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(data, &d) != nil || d.Version < 0 {
		return 0
	}
	return d.Version
}
