package agent

import (
	"context"
	"os"

	"github.com/vulnetix/belai/internal/tools"
)

// maxDiagnosedFiles bounds how many files of one multi-file edit get a
// diagnostics block, so a large patch adds a few blocks and not dozens.
const maxDiagnosedFiles = 4

// diagnoseEdit returns the sealed diagnostics blocks to append to a Write, Edit
// or apply_patch result, or "" when there is nothing to say.
func (s *Session) diagnoseEdit(ctx context.Context, res tools.Result) string {
	var out string
	for _, abs := range editedPaths(res) {
		if block := s.diagnoseFile(ctx, abs); block != "" {
			if out != "" {
				out += "\n\n"
			}
			out += block
		}
	}
	return out
}

func (s *Session) diagnoseFile(ctx context.Context, abs string) string {
	body, err := os.ReadFile(abs)
	if err != nil {
		return ""
	}
	block, rep := s.diag.RenderReport(ctx, abs, body, s.pool)
	if note := s.triageDiagnostics(ctx, abs, rep); note != "" {
		if block != "" {
			block += "\n\n"
		}
		block += note
	}
	return block
}

// editedPaths returns the files a Write, Edit or apply_patch result says it
// changed, at most maxDiagnosedFiles, and nil for any other result.
func editedPaths(res tools.Result) []string {
	if res.Kind != tools.KindEdit && res.Kind != tools.KindWrite {
		return nil
	}
	var paths []string
	if abs, _ := res.Meta["abs_path"].(string); abs != "" {
		paths = append(paths, abs)
	}
	if many, ok := res.Meta["abs_paths"].([]string); ok {
		paths = append(paths, many...)
	}
	if len(paths) > maxDiagnosedFiles {
		paths = paths[:maxDiagnosedFiles]
	}
	return paths
}
