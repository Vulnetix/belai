package agent

import (
	"context"

	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/tools"
)

// classifyRead runs a result through the pipeline, reading only as much of a
// Read's file as its content class needs (rolemanager.ScanForRead).
func (s *Session) classifyRead(ctx context.Context, pipe *rolemanager.Pipeline, res tools.Result) (rolemanager.Decision, error) {
	return pipe.ProcessRead(ctx, res, s.cfg.ClassifierOrDefault().CodeScanFull)
}
