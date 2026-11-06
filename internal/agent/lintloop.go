package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/vulnetix/belai/internal/lintloop"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tools"
)

// The per-edit lint loop (docs/lint.md). After a Write, Edit or apply_patch
// the harness runs the repository's detected linter on the edited file and the
// findings ride back on the same tool result, so the model fixes them in the
// turn that caused them. It is the user's opt-in (lint_loop.enabled), and
// language-server diagnostics stay the fast path beside it.
//
// The linter's argv is fixed by internal/lintdetect and run through
// internal/testrun (permission rules, the call's sandbox policy, the scrubbed
// environment). Its rows are arbitrary text a hostile file can shape, so they
// are classified as tools.KindLint before they are sealed; a row the
// classifier withholds, or a classifier error, leaves a harness-composed count
// and no text. When nothing can be said with confidence the block is empty:
// the loop never reports a file clean.

// lintEdit returns the text to append to an edit result: sealed finding rows,
// a harness count, a harness note, or "".
func (s *Session) lintEdit(ctx context.Context, call rolemanager.ToolCall, res tools.Result, emit func(Event)) string {
	if !s.settings.LintLoopEnabled() || s.planMode || s.exploreSubagent || s.workdir == "" || tools.IsNested(ctx) {
		return ""
	}
	var out string
	for _, abs := range editedPaths(res) {
		if text := s.lintFile(ctx, call, abs, emit); text != "" {
			if out != "" {
				out += "\n\n"
			}
			out += text
		}
	}
	if out == "" {
		return ""
	}
	return "\n\n" + out
}

func (s *Session) lintFile(ctx context.Context, call rolemanager.ToolCall, abs string, emit func(Event)) string {
	defer func() { _ = recover() }()
	rep := lintloop.Run(sandbox.WithPolicy(ctx, s.sandboxPolicy()), lintloop.Options{
		Root:    s.workdir,
		Timeout: time.Duration(s.settings.LintLoopTimeoutMS()) * time.Millisecond,
		Perms:   s.perms,
	}, abs)
	_ = lintloop.Record(s.workdir, rep)
	s.traceRecord("lint_loop", string(rep.Status), call.Name,
		fmt.Sprintf("tool=%s reason=%s rows=%d other=%d exit=%d ms=%d", sanitize.Ident(rep.Tool, 24), rep.Reason, len(rep.Rows), rep.Other, rep.ExitCode, rep.Duration.Milliseconds()), 0)

	switch rep.Status {
	case lintloop.Skipped:
		if rep.Reason == lintloop.ReasonNeedsApproval && s.lint.NoteOnce("approval:"+rep.Tool) {
			return lintloop.ApprovalLine(rep.Tool)
		}
		return ""
	case lintloop.Findings:
		if len(rep.Rows) > 0 {
			break
		}
		fallthrough
	default:
		s.lint.Clear(abs)
		return ""
	}

	rel := s.relPath(abs)
	passes := s.lint.Observe(abs, lintloop.Signature(rep.Rows))
	limit := s.settings.LintLoopMaxAttempts()
	switch {
	case passes > limit+1:
		return "" // said once; the model was told to move on
	case passes == limit+1:
		return lintloop.StuckLine(rep, rel, limit)
	}

	body := lintloop.Body(rep, s.settings.LintLoopMaxRows())
	admitted, ok := s.admitLint(ctx, body, emit)
	if !ok {
		return lintloop.CountsLine(rep, rel)
	}
	sealed := rolemanager.SealDiagnostics(admitted, s.pool)
	if sealed == "" {
		return lintloop.CountsLine(rep, rel)
	}
	return sealed
}

// admitLint runs the rendered rows through the same gate a tool result takes:
// sanitised always, then classified as tools.KindLint unless the posture
// ignores unsafe tool results. It returns the admitted text, or false when the
// classifier withheld it or failed, in which case the caller shows counts only.
func (s *Session) admitLint(ctx context.Context, body string, emit func(Event)) (string, bool) {
	body = sanitize.Sanitize(body)
	if body == "" {
		return "", false
	}
	if s.live.Level(posture.ToolResultUnsafe) == posture.Ignore {
		return body, true
	}
	emit(Event{Kind: EventRoleManagerKind, Phase: RoleManagerPhaseToolResult})
	dec, err := s.pipeline(emit).Process(ctx, tools.Result{Kind: tools.KindLint, Content: body})
	if err != nil {
		emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("classifier error for lint findings: %v; findings withheld", err)})
		return "", false
	}
	if dec.Action != rolemanager.ActionProceed {
		s.verdictWithheld.Add(1)
		return "", false
	}
	return dec.Content, true
}
