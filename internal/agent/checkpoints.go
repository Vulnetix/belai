package agent

import (
	"context"
	"fmt"

	"github.com/vulnetix/belai/internal/checkpoint"
	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tools"
)

// File checkpoints (docs/checkpoints.md). Before a call changes a file the
// session keeps a copy of what the file held, so the user can put it back with
// /undo or /rewind. The store is the user's and no model reads it.
//
// Write, Edit and apply_patch name their files, so each is copied before the
// call runs and a copy that cannot be kept withholds the call. A shell command
// names nothing, so the harness learns what it changed from the file-diff
// snapshot taken around it and records the earlier bytes afterwards; before it
// runs the store is only checked for room.

// checkpointPolicy is what the tracker needs from this session: the
// confinement roots, the Read rules that withhold a file's bytes from a model,
// and the Write and Edit rules that forbid a restore.
func (s *Session) checkpointPolicy() checkpoint.Policy {
	return checkpoint.PolicyFor(s.workdir, func(abs string) bool {
		cwd := s.registry.Cwd()
		return cwd != nil && cwd.Contains(abs)
	}, s.perms)
}

// checkpointBefore copies what a mutating call is about to change. It returns
// a withheld-result message when the store cannot keep a copy, and "" when the
// call may run (or the feature does not apply to it).
func (s *Session) checkpointBefore(tool tools.Tool, call rolemanager.ToolCall) string {
	t := s.ckpt
	if t == nil || !tools.Mutates(tool) || tool.Kind() == tools.KindCode {
		return ""
	}
	var err error
	switch v := tool.(type) {
	case *tools.Bash:
		if v.ReadOnly {
			return ""
		}
		err = t.Ready()
	case tools.HazardTargeter:
		for _, abs := range v.HazardTargets(call.Args) {
			if err = t.Save(abs); err != nil {
				break
			}
		}
	default:
		return ""
	}
	if err != nil {
		return fmt.Sprintf("tool result withheld: checkpoint unavailable (%s); file checkpoints are on, so %q did not run. Set checkpoints.enabled to false or pass -no-checkpoints to run without them",
			sanitize.Line(err.Error(), 120), call.Name)
	}
	return ""
}

// checkpointAfter records the earlier bytes of the files a shell command
// changed. A failure leaves the store marked failed, so the next mutating call
// is withheld rather than run with nothing to restore it from.
func (s *Session) checkpointAfter(ctx context.Context, tool tools.Tool, snap *filediff.Snapshot) {
	t := s.ckpt
	if t == nil || snap == nil {
		return
	}
	if b, ok := tool.(*tools.Bash); !ok || b.ReadOnly {
		return
	}
	set := snap.Baselines(ctx)
	items := make([]checkpoint.Baseline, 0, len(set.Items))
	for _, b := range set.Items {
		items = append(items, checkpoint.Baseline{Path: b.Path, Existed: b.Existed, Known: b.Known, Body: b.Body, Mode: b.Mode, Why: b.Why})
	}
	var gaps []string
	if set.Unavailable != "" {
		gaps = append(gaps, "a command's effects could not be seen: "+set.Unavailable)
	}
	if set.Outside > 0 {
		gaps = append(gaps, fmt.Sprintf("%d changed paths outside the working directory are not tracked", set.Outside))
	}
	if set.Overflow > 0 {
		gaps = append(gaps, fmt.Sprintf("%d more changed paths than one command lists are not tracked", set.Overflow))
	}
	_ = t.SaveBaselines(items, gaps...)
}
