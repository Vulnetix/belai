package clijobs

import (
	"context"
	"os"
	"path/filepath"

	"github.com/vulnetix/belai/internal/scanartifacts"
)

// ExitCode folds the exit codes of a job's steps: the first one that is not OK
// stands, so a later step cannot hide an earlier step's gate breach or a
// run that was not evaluable.
func ExitCode(codes []int) int {
	for _, c := range codes {
		if c != ExitOK {
			return c
		}
	}
	return ExitOK
}

// Outputs is what a job's steps printed, one entry per step in order. It is raw
// process text: only the scout detector reads it, and only by matching fixed
// tokens (Detect), so none of it reaches a result.
type Outputs [][]byte

// Build reads the job's artifacts under repo and returns its result. head is the
// full commit id of HEAD, which the review artefacts must record to count. A
// reader that finds nothing leaves the findings empty: the exit code still
// stands.
func Build(ctx context.Context, j Job, repo, head string, exit int, outs Outputs) Result {
	r := Result{Job: j.ID, ExitCode: exit, Findings: []Finding{}, Signals: []string{}, Artifacts: []string{}}
	dir := filepath.Join(repo, ".vulnetix")
	for _, a := range j.Artifacts {
		if fi, err := os.Lstat(filepath.Join(dir, a)); err == nil && fi.Mode().IsRegular() {
			r.Artifacts = append(r.Artifacts, a)
		}
	}
	switch j.Reader {
	case ReadReview:
		res := scanartifacts.ReadReviewFindings(dir, head)
		for _, f := range res.Findings {
			if !contains(j.Kinds, f.Kind) {
				continue
			}
			r.Add(Finding{ID: f.ID, Kind: f.Kind, Path: f.File, Severity: f.Severity, Ref: reviewRef(f)})
		}
	case ReadSARIF:
		fs, _ := scanartifacts.ReadSARIFFindings(filepath.Join(dir, j.File), j.Kind)
		for _, f := range fs {
			r.Add(Finding{ID: f.ID, Kind: j.Kind, Path: f.File, Severity: f.Severity, Ref: f.Rule})
		}
	case ReadCBOM:
		rows, _ := scanartifacts.BOMRows(ctx, filepath.Join(dir, j.File), 0, 20000)
		for _, row := range rows {
			sev := ""
			switch row.PQC {
			case "quantum-vulnerable":
				sev = "high"
			case "deprecated":
				sev = "medium"
			default:
				continue
			}
			r.Add(Finding{ID: "crypto:" + row.Name, Kind: "crypto", Severity: sev, Ref: row.PQC})
		}
	case ReadAIBOM:
		rows, _ := scanartifacts.BOMRows(ctx, filepath.Join(dir, j.File), 0, 20000)
		for _, row := range rows {
			if row.Category == "" {
				continue
			}
			r.Add(Finding{ID: "ai:" + row.Name, Kind: "ai", Severity: "info", Ref: row.Category})
		}
	case ReadTriage:
		rows, _ := scanartifacts.ReadTriageRows(filepath.Join(dir, j.File), MaxFindings*2)
		for _, row := range rows {
			r.Add(Finding{ID: row.ID, Kind: "triage", Severity: row.Severity, Ref: row.Status})
		}
	case ReadBOMVulns:
		if f, err := scanartifacts.CycloneDXFacts(ctx, filepath.Join(dir, j.File), 0); err == nil {
			r.Counts = Counts{Critical: f.Vulns.Critical, High: f.Vulns.High, Medium: f.Vulns.Medium, Low: f.Vulns.Low}
		}
	case ReadScout:
		caps, _ := readLimited(filepath.Join(dir, "capabilities.yaml"))
		analyze, _ := readLimited(filepath.Join(dir, "analyze.report.json"))
		var detected []byte
		if len(outs) > 1 {
			detected = outs[1]
		}
		r.Signals = Detect(caps, detected, analyze)
	}
	r.Normalize()
	return r
}

func reviewRef(f scanartifacts.ReviewFinding) string {
	switch {
	case f.Rule != "":
		return f.Rule
	case f.Package != "":
		ref := f.Package
		if f.Version != "" {
			ref += "@" + f.Version
		}
		return ref
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// readLimited reads a regular file up to 4 MiB; a larger or unreadable file
// reads as nothing.
func readLimited(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 4<<20 {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(path)
}
