package scanartifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Finding kinds. SCA findings are keyed by their advisory id; every other
// kind is keyed "<kind>:<rule>:<hash of the file>".
const (
	KindSCA     = "sca"
	KindSAST    = "sast"
	KindSecrets = "secrets"
	KindIaC     = "iac"
	KindMalscan = "malscan"
)

// ReviewFinding is one finding a review reported, reduced to identifiers.
// It carries no advisory prose, no matched text and no secret value: the
// fields are ids, names, versions, paths and a severity word, so a card can
// be composed from it without reading anything a scanner wrote as text.
type ReviewFinding struct {
	ID        string
	Kind      string
	Rule      string // SARIF rule id; empty for SCA
	Package   string
	Ecosystem string
	Version   string
	File      string // repository-relative manifest or source path
	Line      int
	Severity  string // critical, high, medium, low or unknown
}

// ReviewResult is what one review's artefacts say about one commit.
type ReviewResult struct {
	Commit   string
	Findings []ReviewFinding
	// Covered names the kinds whose artefact records Commit. A finding of
	// a kind that is not covered was not looked for at that commit, so its
	// absence proves nothing.
	Covered map[string]bool
}

// sarifKinds maps a SARIF artefact to the kind it reports.
var sarifKinds = map[string]string{
	"sast.sarif":    KindSAST,
	"secrets.sarif": KindSecrets,
	"iac.sarif":     KindIaC,
	"malscan.sarif": KindMalscan,
}

// scaOpen is the set of memory.yaml statuses that still count as present.
// Anything the CLI has resolved (fixed, not_affected) does not.
var scaOpen = map[string]bool{"": true, "affected": true, "under_investigation": true}

var (
	idBad = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

// ReadReviewFindings reads the findings dir's artefacts for commit. SCA
// comes from memory.yaml's findings map (only when its last scan is commit);
// every other kind comes from its SARIF file (only when that file records
// commit). An unreadable or mismatched artefact is simply not covered.
func ReadReviewFindings(dir, commit string) ReviewResult {
	commit = strings.ToLower(strings.TrimSpace(commit))
	res := ReviewResult{Commit: commit, Covered: map[string]bool{}}
	if !commitRE.MatchString(commit) {
		return res
	}
	if fs, ok := scaFindings(filepath.Join(dir, "memory.yaml"), commit); ok {
		res.Covered[KindSCA] = true
		res.Findings = append(res.Findings, fs...)
	}
	for name, kind := range sarifKinds {
		path := filepath.Join(dir, name)
		if strings.ToLower(sarifCommit(path)) != commit {
			continue
		}
		fs, err := sarifFindings(path, kind)
		if err != nil {
			continue
		}
		res.Covered[kind] = true
		res.Findings = append(res.Findings, fs...)
	}
	sort.Slice(res.Findings, func(i, j int) bool { return res.Findings[i].ID < res.Findings[j].ID })
	return res
}

func scaFindings(path, commit string) ([]ReviewFinding, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	data, err := budgetReadAll(f, DefaultMaxBytes)
	if err != nil {
		return nil, false
	}
	var doc struct {
		LastScan struct {
			GitCommit string `yaml:"git_commit"`
		} `yaml:"last_scan"`
		Findings map[string]struct {
			Package   string `yaml:"package"`
			Ecosystem string `yaml:"ecosystem"`
			Severity  string `yaml:"severity"`
			Status    string `yaml:"status"`
			Discovery struct {
				File string `yaml:"file"`
			} `yaml:"discovery"`
			Versions struct {
				Current string `yaml:"current"`
			} `yaml:"versions"`
		} `yaml:"findings"`
	}
	if yaml.Unmarshal(data, &doc) != nil || strings.ToLower(doc.LastScan.GitCommit) != commit {
		return nil, false
	}
	var out []ReviewFinding
	for id, fd := range doc.Findings {
		id = strings.TrimSpace(id)
		if !findingID.MatchString(id) || !scaOpen[strings.ToLower(strings.TrimSpace(fd.Status))] {
			continue
		}
		out = append(out, ReviewFinding{
			ID: id, Kind: KindSCA, Package: plain(fd.Package), Ecosystem: plain(fd.Ecosystem),
			Version: plain(fd.Versions.Current), File: relPath(fd.Discovery.File), Severity: sevOf(fd.Severity),
		})
	}
	return out, true
}

func sarifFindings(path, kind string) ([]ReviewFinding, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := budgetReadAll(f, DefaultMaxBytes)
	if err != nil {
		return nil, err
	}
	var doc sarifDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse sarif: %w", err)
	}
	if len(doc.Runs) == 0 {
		return nil, nil
	}
	run := doc.Runs[0]
	rules := buildRuleMap(run.Tool)
	seen := map[string]bool{}
	var out []ReviewFinding
	for _, r := range run.Results {
		if r.BaselineState == "absent" || len(r.Suppressions) > 0 || r.RuleID == "" {
			continue
		}
		sev, _ := severityForResult(r, rules)
		uri, line := firstSARIFLocation(r)
		rel := relPath(uri)
		rule := idBad.ReplaceAllString(r.RuleID, "_")
		sum := sha256.Sum256([]byte(rel))
		id := kind + ":" + clip(rule, 40) + ":" + hex.EncodeToString(sum[:4])
		if seen[id] {
			continue // one card per rule and file
		}
		seen[id] = true
		out = append(out, ReviewFinding{ID: id, Kind: kind, Rule: clip(rule, 80), File: rel, Line: line, Severity: sevOf(sev.String())})
	}
	return out, nil
}

// findingID is the shape a kanban card accepts for a finding.
var findingID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

func sevOf(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "critical", "high", "medium", "low":
		return s
	case "moderate":
		return "medium"
	}
	return "unknown"
}

// plain keeps a name or version to identifier characters.
func plain(s string) string {
	return clip(strings.Trim(idBadPath.ReplaceAllString(strings.TrimSpace(s), "_"), "_"), 120)
}

var idBadPath = regexp.MustCompile(`[^A-Za-z0-9._@/+:~-]+`)

// relPath cleans a scanner-reported path to a short repository-relative one.
func relPath(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "file://")
	s = strings.TrimPrefix(s, "./")
	if filepath.IsAbs(s) || strings.Contains(s, "..") {
		return filepath.Base(s)
	}
	return plain(s)
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// KindOfID names the kind a finding id belongs to: the prefix before the
// first colon when it is one of the SARIF kinds, otherwise SCA.
func KindOfID(id string) string {
	if k, _, ok := strings.Cut(id, ":"); ok {
		switch k {
		case KindSAST, KindSecrets, KindIaC, KindMalscan:
			return k
		}
	}
	return KindSCA
}
