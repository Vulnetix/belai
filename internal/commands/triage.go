package commands

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/scanartifacts"
)

// BuildTriageBlocks renders the model-facing report blocks from the artifacts
// a /vulnetix review produced. Each block is bounded by the scanartifacts
// finding-line caps; the bodies are repository-derived bytes and are therefore
// classified by the TUI before they reach the model, exactly like every other
// tool result.
func BuildTriageBlocks(ctx context.Context, workdir string) []TriageBlock {
	return BuildTriageBlocksFor(ctx, workdir, nil)
}

// BuildTriageBlocksFor is BuildTriageBlocks limited to the artifacts named by
// rels (paths relative to the project's .vulnetix directory, as a Scanner
// lists them). A nil rels means every artifact.
func BuildTriageBlocksFor(ctx context.Context, workdir string, rels []string) []TriageBlock {
	dir := config.ProjectDir(workdir)
	arts, err := scanartifacts.Enumerate(dir)
	if err != nil {
		return nil
	}
	if rels != nil {
		arts = artifactsIn(arts, rels)
	}

	var blocks []TriageBlock
	for i := range arts {
		a := &arts[i]
		if a.Superseded || a.Kind == scanartifacts.KindBelai {
			continue
		}
		label := triageLabel(a)
		if label == "" {
			continue
		}

		var lines []string
		switch a.Kind {
		case scanartifacts.KindSARIF:
			lines, _ = scanartifacts.SARIFFindingLines(ctx, a.Path, scanartifacts.DefaultMaxBytes)
		case scanartifacts.KindCycloneDXSBOM, scanartifacts.KindCycloneDXCBOM, scanartifacts.KindCycloneDXAIBOM:
			lines, _ = scanartifacts.CycloneDXFindingLines(ctx, a.Path, scanartifacts.DefaultMaxBytes)
		case scanartifacts.KindOpenVEX, scanartifacts.KindOpenVEXRiskAccepted:
			lines, _ = scanartifacts.OpenVEXFindingLines(ctx, a.Path, scanartifacts.DefaultMaxBytes)
		}
		if len(lines) == 0 {
			continue
		}
		blocks = append(blocks, TriageBlock{
			Scanner: a.Tool,
			Label:   label,
			Body:    strings.Join(lines, "\n"),
		})
	}
	return blocks
}

// artifactsIn keeps the artifacts whose Rel is one of rels.
func artifactsIn(arts []scanartifacts.Artifact, rels []string) []scanartifacts.Artifact {
	var out []scanartifacts.Artifact
	for _, a := range arts {
		for _, rel := range rels {
			if filepath.ToSlash(a.Rel) == rel {
				out = append(out, a)
				break
			}
		}
	}
	return out
}

func triageLabel(a *scanartifacts.Artifact) string {
	base := filepath.Base(a.Rel)
	switch a.Kind {
	case scanartifacts.KindSARIF:
		if a.Tool != "" {
			return a.Tool + " report"
		}
		return strings.TrimSuffix(base, filepath.Ext(base)) + " report"
	case scanartifacts.KindCycloneDXSBOM, scanartifacts.KindCycloneDXCBOM, scanartifacts.KindCycloneDXAIBOM:
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		stem = strings.TrimSuffix(stem, ".cdx")
		return stem + " report"
	case scanartifacts.KindOpenVEX, scanartifacts.KindOpenVEXRiskAccepted:
		return strings.TrimSuffix(base, filepath.Ext(base)) + " report"
	default:
		return ""
	}
}
