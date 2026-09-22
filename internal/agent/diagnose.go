package agent

import (
	"context"
	"os"

	"github.com/vulnetix/signet/internal/tools"
)

// diagnoseEdit returns the sealed diagnostics block to append to a Write/Edit
// result, or "" when there is nothing to say.
func (s *Session) diagnoseEdit(ctx context.Context, res tools.Result) string {
	if res.Kind != tools.KindEdit && res.Kind != tools.KindWrite {
		return ""
	}
	abs, _ := res.Meta["abs_path"].(string)
	if abs == "" {
		return ""
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return ""
	}
	return s.diag.Render(ctx, abs, body, s.pool)
}
