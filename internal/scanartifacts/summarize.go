package scanartifacts

import (
	"context"
	"fmt"
	"os"
)

// Summarize parses every artifact in arts and returns a project summary.
func Summarize(ctx context.Context, workdir string, arts []Artifact) Summary {
	s := Summary{
		Dir:       workdir,
		Artifacts: arts,
		PerFile:   map[string]FileSummary{},
	}

	// First pass: parse each artifact into a FileSummary and collect per-file keys.
	var allCDX, allSARIF, allVEX []FindingKey
	for i := range arts {
		a := &arts[i]
		if a.Superseded || a.Kind == KindSignet {
			continue
		}
		fs, keys := parseArtifact(ctx, a)
		s.PerFile[a.Rel] = fs
		if fs.SkipReason != "" {
			continue
		}
		switch a.Kind {
		case KindCycloneDXSBOM:
			allCDX = append(allCDX, keys...)
		case KindSARIF:
			allSARIF = append(allSARIF, keys...)
		case KindOpenVEX:
			allVEX = append(allVEX, keys...)
		case KindOpenVEXRiskAccepted:
			// Risk-accepted tallies are not headline counts.
		case KindMemory:
			s.Union = s.Union.Merge(fs.Counts)
		}
		if a.Kind == KindSARIF || a.Kind == KindOpenVEX || a.Kind == KindOpenVEXRiskAccepted || a.Kind == KindCycloneDXSBOM {
			s.Suppressed.Add(SeverityUnknown, fs.Suppressed)
		}
	}

	s.Union = crossFileUnion(allCDX, allSARIF, allVEX)
	s.Licenses = licenseCounts(ctx, arts)
	return s
}

func parseArtifact(ctx context.Context, a *Artifact) (FileSummary, []FindingKey) {
	fs := FileSummary{}
	if a.Size > HardMaxBytes {
		fs.SkipReason = fmt.Sprintf("artifact exceeds %d byte hard cap", HardMaxBytes)
		return fs, nil
	}

	f, err := os.Open(a.Path)
	if err != nil {
		fs.SkipReason = fmt.Sprintf("open: %v", err)
		return fs, nil
	}
	defer f.Close()

	switch a.Kind {
	case KindCycloneDXSBOM, KindCycloneDXCBOM, KindCycloneDXAIBOM:
		c, _, keys, err := ParseCycloneDX(ctx, f, DefaultMaxBytes)
		if err != nil {
			fs.SkipReason = fmt.Sprintf("parse: %v", err)
			return fs, nil
		}
		fs.Counts = c
		return fs, keys
	case KindSARIF:
		c, sup, inf, keys, err := ParseSARIF(ctx, f, DefaultMaxBytes)
		if err != nil {
			fs.SkipReason = fmt.Sprintf("parse: %v", err)
			return fs, nil
		}
		fs.Counts = c
		fs.Suppressed = sup.Total()
		fs.Inferred = inf
		return fs, keys
	case KindOpenVEX, KindOpenVEXRiskAccepted:
		c, inv, risk, keys, err := ParseOpenVEX(ctx, f, DefaultMaxBytes)
		if err != nil {
			fs.SkipReason = fmt.Sprintf("parse: %v", err)
			return fs, nil
		}
		fs.Counts = c
		fs.Suppressed = inv + risk
		return fs, keys
	case KindMemory:
		m, err := ParseMemorySummary(a.Path)
		if err != nil {
			fs.SkipReason = fmt.Sprintf("parse memory: %v", err)
			return fs, nil
		}
		fs.Counts.Add(SeverityCritical, m.Critical)
		fs.Counts.Add(SeverityHigh, m.High)
		fs.Counts.Add(SeverityMedium, m.Medium)
		fs.Counts.Add(SeverityLow, m.Low)
	case KindAnalyzeReport:
		fs.SkipReason = "analyze.report.json has no summary block"
	case KindToolLog:
		fs.SkipReason = "raw tool log: may contain secret material"
	case KindUnknown:
		fs.SkipReason = "unknown file kind"
	}
	return fs, nil
}

// crossFileUnion merges per-artifact keys into a unique set and sums by
// severity. A key appearing in both SARIF and a VEX document only counts
// once; the first source's severity wins.
func crossFileUnion(cdx, sarif, vex []FindingKey) Counts {
	var c Counts
	seen := map[string]bool{}
	for _, it := range append(append(cdx, sarif...), vex...) {
		if seen[it.Key] {
			continue
		}
		seen[it.Key] = true
		c.Add(it.Severity, 1)
	}
	return c
}

func licenseCounts(ctx context.Context, arts []Artifact) Counts {
	var lc Counts
	for i := range arts {
		a := &arts[i]
		if a.Superseded || (a.Kind != KindCycloneDXSBOM && a.Kind != KindCycloneDXCBOM && a.Kind != KindCycloneDXAIBOM) {
			continue
		}
		f, err := os.Open(a.Path)
		if err != nil {
			continue
		}
		_, lics, _, err := ParseCycloneDX(ctx, f, DefaultMaxBytes)
		if err == nil {
			lc = lc.Merge(lics)
		}
		f.Close()
	}
	return lc
}
