// Package scanartifacts parses the artifact files Vulnetix leaves under a
// project's .vulnetix directory and turns them into severity-aggregated
// summaries. It treats every file as untrusted until its structure is proven.
package scanartifacts

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Kind identifies the type of file produced by Vulnetix or signet.
type Kind string

const (
	KindCycloneDXSBOM       Kind = "cyclonedx-sbom"
	KindCycloneDXCBOM       Kind = "cyclonedx-cbom"
	KindCycloneDXAIBOM      Kind = "cyclonedx-aibom"
	KindSARIF               Kind = "sarif"
	KindOpenVEX             Kind = "openvex"
	KindOpenVEXRiskAccepted Kind = "openvex-risk-accepted"
	KindMemory              Kind = "memory"
	KindCapabilities        Kind = "capabilities"
	KindPackagesScan        Kind = "packages-scan"
	KindAnalyzeReport       Kind = "analyze-report"
	KindToolLog             Kind = "tool-log"
	KindToolNative          Kind = "tool-native"
	KindSignet              Kind = "signet"
	KindUnknown             Kind = "unknown"
)

// Artifact describes one file on disk.
type Artifact struct {
	Path        string
	Rel         string
	Kind        Kind
	Tool        string
	Stamp       time.Time
	Label       string
	Size        int64
	ModTime     time.Time
	Superseded  bool
	Description string
	SkipReason  string
}

// Enumerate walks dir and returns classified artifacts. It skips symlinks,
// non-regular files, and files owned by signet's own state.
func Enumerate(dir string) ([]Artifact, error) {
	var out []Artifact

	sgn := signetPaths(dir)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			// Do not descend into signet-owned state directories.
			rel, _ := filepath.Rel(dir, path)
			if isSignetDir(rel) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			// Skip symlinks: they might point outside the project.
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		if sgn[rel] {
			return nil
		}
		if isSignetPath(rel) {
			return nil
		}
		kind, tool, stamp, label := Classify(rel)
		desc := describe(kind, tool, label)
		out = append(out, Artifact{
			Path:        path,
			Rel:         rel,
			Kind:        kind,
			Tool:        tool,
			Stamp:       stamp,
			Label:       label,
			Size:        info.Size(),
			ModTime:     info.ModTime(),
			Description: desc,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	markSuperseded(out)
	return out, nil
}

// signetPaths precomputes known signet-owned paths inside dir.
func signetPaths(dir string) map[string]bool {
	m := map[string]bool{
		"settings.json": true,
		// prompts.json is a tombstone: the old two-file prompt library. It is
		// still skipped so a leftover file from before the directory format is
		// never reclassified as a scannable artifact.
		"prompts.json":              true,
		"credentials.json":          true,
		"code-review-summary.md":    true,
		"code-review-manifest.json": true,
	}
	for _, base := range []string{"signet", "plans", "goals", "prompts"} {
		_ = filepath.WalkDir(filepath.Join(dir, base), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(dir, path)
			m[rel] = true
			return nil
		})
	}
	return m
}

// signet-owned directories by name.
var signetDirs = map[string]bool{
	"signet":  true,
	"plans":   true,
	"goals":   true,
	"prompts": true,
}

func isSignetDir(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	return signetDirs[parts[0]]
}

// isSignetPath matches signet files stored directly in .vulnetix.
func isSignetPath(rel string) bool {
	base := filepath.Base(rel)
	switch base {
	case "settings.json", "credentials.json",
		"code-review-summary.md", "code-review-manifest.json":
		return true
	case "prompts.json":
		// Tombstone: a leftover prompts.json from the old library is still
		// signet-owned, never a scannable artifact.
		return true
	}
	return false
}

// sarifTimestamp matches a 14-digit timestamp in a SARIF basename.
var sarifTimestamp = regexp.MustCompile(`\.\d{14,}`)

// Classify infers artifact kind and tool from a relative path.
func Classify(rel string) (Kind, string, time.Time, string) {
	base := filepath.Base(rel)
	lower := strings.ToLower(base)
	dir := filepath.ToSlash(filepath.Dir(rel))

	// Signet files.
	if isSignetPath(rel) {
		return KindSignet, "", time.Time{}, ""
	}
	for _, seg := range strings.Split(dir, "/") {
		if signetDirs[seg] {
			return KindSignet, "", time.Time{}, ""
		}
	}

	ext := strings.ToLower(filepath.Ext(base))
	stem := strings.TrimSuffix(lower, ext)
	gz := false
	if ext == ".gz" {
		gz = true
		base2 := strings.TrimSuffix(base, ext)
		ext = filepath.Ext(base2)
		stem = strings.TrimSuffix(strings.ToLower(base2), ext)
	}

	// CycloneDX.
	if ext == ".json" && strings.HasSuffix(stem, ".cdx") {
		prefix := strings.TrimSuffix(stem, ".cdx")
		switch prefix {
		case "cbom":
			return KindCycloneDXCBOM, "", time.Time{}, ""
		case "aibom", "ai-bom":
			return KindCycloneDXAIBOM, "", time.Time{}, ""
		default:
			return KindCycloneDXSBOM, "", time.Time{}, ""
		}
	}

	// SARIF.
	if ext == ".sarif" {
		tool := ""
		if idx := strings.IndexByte(stem, '.'); idx > 0 {
			tool = stem[:idx]
		} else {
			tool = stem
		}
		stamp, label := parseSARIFStem(stem)
		return KindSARIF, tool, stamp, label
	}

	// OpenVEX.
	if ext == ".json" && (strings.HasPrefix(stem, "vex") || stem == "openvex" || strings.HasSuffix(stem, ".openvex")) {
		if stem == "vex-risk-accepted" {
			return KindOpenVEXRiskAccepted, "", time.Time{}, ""
		}
		return KindOpenVEX, "", time.Time{}, ""
	}

	// Memory and capabilities.
	if lower == "memory.yaml" || lower == "memory.yml" {
		return KindMemory, "", time.Time{}, ""
	}
	if lower == "capabilities.yaml" || lower == "capabilities.yml" {
		return KindCapabilities, "", time.Time{}, ""
	}

	// Analyze report.
	if lower == "analyze.report.json" {
		return KindAnalyzeReport, "", time.Time{}, ""
	}

	// Package scan manifest.
	if (dir == "scans" || strings.HasPrefix(dir, "scans/")) && ext == ".json" && strings.HasSuffix(stem, ".packages") {
		return KindPackagesScan, "", time.Time{}, ""
	}

	// Tool logs.
	if ext == ".stderr" || ext == ".log" {
		return KindToolLog, "", time.Time{}, ""
	}

	// Native third-party output files are recognised by known prefixes.
	if knownNative(stem) {
		return KindToolNative, "", time.Time{}, ""
	}

	// A compressed file with no recognisable inner extension is a captured
	// tool log (e.g. gitleaks.gz), not a scannable artifact.
	if gz {
		return KindToolLog, "", time.Time{}, ""
	}

	return KindUnknown, "", time.Time{}, ""
}

// parseSARIFStem extracts an embedded timestamp or a branch label from a SARIF
// basename such as sast.20260623212031.sarif or sast.20260805-admin-reviews.sarif.
func parseSARIFStem(stem string) (time.Time, string) {
	parts := strings.Split(stem, ".")
	if len(parts) < 2 {
		return time.Time{}, ""
	}
	candidate := parts[1]
	if t, err := time.Parse("20060102150405", candidate); err == nil {
		return t, ""
	}
	// Try a shorter date prefix followed by a label.
	if len(candidate) >= 8 {
		if _, err := time.Parse("20060102", candidate[:8]); err == nil {
			return time.Time{}, candidate[8:]
		}
	}
	// A non-date middle segment is treated as a branch label.
	return time.Time{}, candidate
}

var nativeNames = map[string]bool{
	"gitleaks-report": true,
	"semgrep":         true,
	"trivy":           true,
	"grype":           true,
}

func knownNative(stem string) bool {
	for name := range nativeNames {
		if strings.HasPrefix(stem, name) {
			return true
		}
	}
	return false
}

func describe(kind Kind, tool, label string) string {
	switch kind {
	case KindCycloneDXSBOM:
		return "CycloneDX software bill of materials"
	case KindCycloneDXCBOM:
		return "CycloneDX cryptographic bill of materials"
	case KindCycloneDXAIBOM:
		return "CycloneDX AI bill of materials"
	case KindSARIF:
		if label != "" {
			return fmt.Sprintf("SARIF (%s, %s)", tool, label)
		}
		return fmt.Sprintf("SARIF (%s)", tool)
	case KindOpenVEX:
		return "OpenVEX status overlay"
	case KindOpenVEXRiskAccepted:
		return "OpenVEX risk-accepted overlay"
	case KindMemory:
		return "scan memory"
	case KindCapabilities:
		return "capability inventory"
	case KindAnalyzeReport:
		return "analysis report (not parsed)"
	case KindToolLog:
		return "tool diagnostics"
	case KindToolNative:
		return "tool-native output"
	case KindUnknown:
		return "unknown artifact"
	default:
		return string(kind)
	}
}

// markSuperseded flags older artifacts in the same (Kind, Tool) group. The
// newest by explicit timestamp, then by modtime, is authoritative.
func markSuperseded(artifacts []Artifact) {
	groups := map[[2]string][]int{}
	for i, a := range artifacts {
		key := [2]string{string(a.Kind), a.Tool}
		// CycloneDX documents carry no tool label; grouping them by kind alone
		// would make a distinct inventory.cdx.json look like an older variant
		// of sbom.cdx.json. Group those by basename so only same-named files
		// supersede each other.
		if a.Kind == KindCycloneDXSBOM || a.Kind == KindCycloneDXCBOM || a.Kind == KindCycloneDXAIBOM {
			key = [2]string{string(a.Kind), filepath.Base(a.Rel)}
		}
		groups[key] = append(groups[key], i)
	}
	for _, idxs := range groups {
		if len(idxs) <= 1 {
			continue
		}
		sort.Slice(idxs, func(i, j int) bool {
			a, b := artifacts[idxs[i]], artifacts[idxs[j]]
			if !a.Stamp.IsZero() && !b.Stamp.IsZero() {
				return a.Stamp.After(b.Stamp)
			}
			if !a.Stamp.IsZero() {
				return true
			}
			if !b.Stamp.IsZero() {
				return false
			}
			return a.ModTime.After(b.ModTime)
		})
		for _, i := range idxs[1:] {
			artifacts[i].Superseded = true
		}
	}
}
