// Package clijobs is the closed table of Vulnetix CLI jobs a worker may run
// for a card, and the result document each one produces.
//
// A job is an id, a fixed argv, the artifacts to read afterwards and a
// Mutates flag. The argv is built from this table alone: no card text, model
// argument or setting reaches an argument, and every path in it is a fixed
// location under the repository's .vulnetix directory. A mutating job is one
// that changes a file outside .vulnetix, the forge or a Vulnetix
// organisation; it runs only for a profile that sets kanban.cli.write and for
// a card whose acceptance gates are all met (internal/fleet/clijob.go).
//
// The result is ids, counts and enums, never scanner, advisory or matched
// text (Result). The package imports no other part of the harness that
// touches the board, so the kanban store can validate a pulled result with
// Clean.
package clijobs

import (
	"slices"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/vulnetixcli"
)

// Label is the prefix of the card label that names a job: "cli-job:sca".
const Label = "cli-job:"

// Exit codes the CLI gives meaning to. Any other code is an error of the run.
const (
	ExitOK           = 0
	ExitGateBreach   = 1
	ExitNotEvaluable = 3
)

// Reader says how a job's artifacts become findings.
type Reader string

// The readers. Each reads identifier fields only (internal/scanartifacts).
const (
	// ReadNone reads no artifact: the result is the exit code and the names of
	// the artifacts that exist.
	ReadNone Reader = "none"
	// ReadReview reads the review artefacts (memory.yaml and the SARIF files
	// that record HEAD), keeping the job's Kinds.
	ReadReview Reader = "review"
	// ReadSARIF reads one SARIF file as Kind findings.
	ReadSARIF Reader = "sarif"
	// ReadCBOM reads a CycloneDX CBOM and reports the algorithms that are not
	// post-quantum safe.
	ReadCBOM Reader = "cbom"
	// ReadAIBOM reads a CycloneDX AIBOM and reports its AI components.
	ReadAIBOM Reader = "aibom"
	// ReadBOMVulns reads the vulnerability counts of a CycloneDX document.
	ReadBOMVulns Reader = "bom-vulns"
	// ReadScout derives profile signals from the scout's three outputs.
	ReadScout Reader = "scout"
)

// Step is one CLI invocation: the subcommand and flags after the program name.
type Step struct {
	Args []string
}

// Job is one entry of the closed table.
type Job struct {
	ID string
	// Mutates is true for a job that changes something outside .vulnetix.
	Mutates bool
	// Steps run in order; the result's exit code is the first step that is not
	// OK, or OK.
	Steps []Step
	// Needs are files, relative to the repository root, that must exist before
	// the job runs; a job whose input is missing is not evaluable.
	Needs []string
	// Artifacts are the files, relative to .vulnetix, the job leaves.
	Artifacts []string
	Reader    Reader
	// Kinds are the review kinds ReadReview keeps; Kind names the finding kind
	// of ReadSARIF; File is the artifact ReadSARIF, ReadCBOM, ReadAIBOM and
	// ReadBOMVulns read.
	Kinds []string
	Kind  string
	File  string
}

// Argvs returns the full argv of each step for the CLI at bin: the program, the
// harness's hardening flags and the step's fixed arguments.
func (j Job) Argvs(bin string) [][]string {
	out := make([][]string, 0, len(j.Steps))
	for _, s := range j.Steps {
		out = append(out, append([]string{bin}, vulnetixcli.HardenedArgs(slices.Clone(s.Args)...)...))
	}
	return out
}

func one(args ...string) []Step { return []Step{{Args: args}} }

// table is the closed job table. Ids are pinned by TestJobIDsAreClosed and the
// read-only versus mutating split by TestMutatingSplit.
var table = []Job{
	{ID: "scan", Steps: one("scan"), Artifacts: []string{"memory.yaml", "sast.sarif", "secrets.sarif", "iac.sarif", "malscan.sarif", "sbom.cdx.json"}, Reader: ReadReview, Kinds: []string{"sca", "sast", "secrets", "iac", "malscan"}},
	{ID: "sca", Steps: one("sca"), Artifacts: []string{"memory.yaml", "sbom.cdx.json"}, Reader: ReadReview, Kinds: []string{"sca"}},
	{ID: "sast", Steps: one("sast", "--disable-memory"), Artifacts: []string{"sast.sarif"}, Reader: ReadReview, Kinds: []string{"sast"}},
	{ID: "secrets", Steps: one("secrets", "--disable-memory", "--ignore-git"), Artifacts: []string{"secrets.sarif"}, Reader: ReadReview, Kinds: []string{"secrets"}},
	{ID: "iac", Steps: one("iac", "--disable-memory"), Artifacts: []string{"iac.sarif"}, Reader: ReadReview, Kinds: []string{"iac"}},
	{ID: "containers", Steps: one("containers", "--disable-memory", "-o", ".vulnetix/containers.cdx.json"), Artifacts: []string{"containers.sarif", "containers.cdx.json"}, Reader: ReadSARIF, Kind: "container", File: "containers.sarif"},
	{ID: "image", Steps: one("image", "--disable-memory", "--no-upload"), Reader: ReadNone},
	{ID: "cbom", Steps: one("cbom", "--disable-memory", "--no-upload"), Artifacts: []string{"cbom.cdx.json"}, Reader: ReadCBOM, File: "cbom.cdx.json"},
	{ID: "aibom", Steps: one("aibom", "--disable-memory", "--no-upload"), Artifacts: []string{"ai-bom.cdx.json"}, Reader: ReadAIBOM, File: "ai-bom.cdx.json"},
	{ID: "license", Steps: one("license"), Reader: ReadNone},
	{ID: "malscan", Steps: one("malscan", "--disable-memory", "--no-upload"), Artifacts: []string{"malscan.sarif"}, Reader: ReadReview, Kinds: []string{"malscan"}},
	{ID: "sbom", Steps: one("sbom", "--output-file", ".vulnetix/inventory.cdx.json"), Artifacts: []string{"inventory.cdx.json"}, Reader: ReadBOMVulns, File: "inventory.cdx.json"},
	{ID: "bom-diff", Steps: one("bom", "diff", ".vulnetix/baseline.cdx.json", ".vulnetix/inventory.cdx.json"), Needs: []string{".vulnetix/baseline.cdx.json", ".vulnetix/inventory.cdx.json"}, Reader: ReadNone},
	{ID: "bom-validate", Steps: one("bom", "validate", ".vulnetix/inventory.cdx.json"), Needs: []string{".vulnetix/inventory.cdx.json"}, Reader: ReadNone},
	{ID: "bom-skew", Steps: one("bom", "skew", "--from", ".vulnetix"), Reader: ReadNone},
	{ID: "triage", Steps: one("triage", "status", "--format", "json"), Reader: ReadNone},
	{ID: "defend", Steps: one("defend", "--quiet"), Reader: ReadNone},
	{ID: "jail", Steps: one("jail"), Reader: ReadNone},
	{ID: "jail-explain", Steps: one("jail", "explain"), Reader: ReadNone},
	{ID: "gha-audit", Steps: one("gha", "audit", "--offline"), Reader: ReadNone},
	{ID: "config-plan", Steps: one("config", "plan", "--file", ".vulnetix/config.yaml"), Needs: []string{".vulnetix/config.yaml"}, Reader: ReadNone},
	{ID: "ai-firewall-status", Steps: one("ai-firewall", "status"), Reader: ReadNone},
	{ID: "attest-verify", Steps: one("attest", "verify", "--envelope", ".vulnetix/attestation.json"), Needs: []string{".vulnetix/attestation.json"}, Reader: ReadNone},
	{ID: "api-discover", Steps: one("api", "discover", "--no-upload"), Reader: ReadNone},
	{ID: "surface-liveness", Steps: one("surface", "liveness"), Reader: ReadNone},
	{ID: "analyze", Steps: one("analyze", "--no-upload", "--output-file", ".vulnetix/analyze.report.json"), Artifacts: []string{"analyze.report.json"}, Reader: ReadNone},
	{ID: "report", Steps: one("report"), Reader: ReadNone},
	{ID: ScoutID, Steps: []Step{
		{Args: []string{"agent", "capabilities"}},
		{Args: []string{"scan", "--dry-run", "--show-detected"}},
		{Args: []string{"analyze", "--no-upload", "--output-file", ".vulnetix/analyze.report.json"}},
	}, Artifacts: []string{"capabilities.yaml", "analyze.report.json"}, Reader: ReadScout},

	{ID: "fix", Mutates: true, Steps: one("fix", "--yes"), Reader: ReadNone},
	{ID: "vex-apply", Mutates: true, Steps: one("vex", "apply", "--bom", ".vulnetix/sbom.cdx.json", "--vex", ".vulnetix/vex", "--out", ".vulnetix/sbom.effective.cdx.json"), Needs: []string{".vulnetix/sbom.cdx.json"}, Artifacts: []string{"sbom.effective.cdx.json"}, Reader: ReadNone},
	{ID: "ignore-sync", Mutates: true, Steps: one("ignore", "sync"), Reader: ReadNone},
	{ID: "bom-merge", Mutates: true, Steps: one("bom", "merge", "--out", ".vulnetix/merged.cdx.json", ".vulnetix/sbom.cdx.json", ".vulnetix/inventory.cdx.json"), Needs: []string{".vulnetix/sbom.cdx.json", ".vulnetix/inventory.cdx.json"}, Artifacts: []string{"merged.cdx.json"}, Reader: ReadNone},
	{ID: "bom-enrich", Mutates: true, Steps: one("bom", "enrich", ".vulnetix/inventory.cdx.json"), Needs: []string{".vulnetix/inventory.cdx.json"}, Reader: ReadNone},
	{ID: "gha-init", Mutates: true, Steps: one("gha", "init"), Reader: ReadNone},
	{ID: "gha-setup", Mutates: true, Steps: one("gha", "setup", "--all"), Reader: ReadNone},
	{ID: "gha-code-scanning", Mutates: true, Steps: one("gha", "publish", "code-scanning"), Reader: ReadNone},
	{ID: "gha-dep-graph", Mutates: true, Steps: one("gha", "publish", "dependency-graph"), Reader: ReadNone},
	{ID: "gha-advisories", Mutates: true, Steps: one("gha", "publish", "advisories"), Reader: ReadNone},
	{ID: "config-apply", Mutates: true, Steps: one("config", "apply", "--file", ".vulnetix/config.yaml", "--auto-approve"), Needs: []string{".vulnetix/config.yaml"}, Reader: ReadNone},
	{ID: "ai-firewall-apply", Mutates: true, Steps: one("ai-firewall", "apply", "--file", ".vulnetix/ai-firewall.yaml"), Needs: []string{".vulnetix/ai-firewall.yaml"}, Reader: ReadNone},
	{ID: "api-upload", Mutates: true, Steps: one("api", "upload"), Reader: ReadNone},
	{ID: "tea-release", Mutates: true, Steps: one("tea", "release", "--auto-version"), Reader: ReadNone},
}

// ScoutID is the composite job whose result carries profile signals.
const ScoutID = "scout"

// Lookup returns the job with the given id.
func Lookup(id string) (Job, bool) {
	for _, j := range table {
		if j.ID == id {
			return j, true
		}
	}
	return Job{}, false
}

// Valid reports whether id is in the table.
func Valid(id string) bool { _, ok := Lookup(id); return ok }

// IDs returns every job id, sorted.
func IDs() []string {
	out := make([]string, 0, len(table))
	for _, j := range table {
		out = append(out, j.ID)
	}
	sort.Strings(out)
	return out
}

// ReadOnlyIDs returns the ids of the jobs that change nothing outside
// .vulnetix, sorted.
func ReadOnlyIDs() []string { return idsWhere(false) }

// MutatingIDs returns the ids of the jobs that change something outside
// .vulnetix, sorted.
func MutatingIDs() []string { return idsWhere(true) }

func idsWhere(mutates bool) []string {
	var out []string
	for _, j := range table {
		if j.Mutates == mutates {
			out = append(out, j.ID)
		}
	}
	sort.Strings(out)
	return out
}

// JobOfLabels returns the job id a card's labels name ("cli-job:sca"), and
// false when no label does. The first match in label order wins.
func JobOfLabels(labels []string) (string, bool) {
	for _, l := range labels {
		if id, ok := strings.CutPrefix(l, Label); ok && id != "" {
			return id, true
		}
	}
	return "", false
}
