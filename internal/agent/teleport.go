package agent

import (
	"context"

	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tools"
)

// TeleportReplay is the material a teleport's code replay hands the model: the
// origin host's hand-over (its model's summary and per-file instructions) and
// the parts of the patch that did not apply. Every word of it came from another
// host, through the backend, so it is untrusted: it rides only as a gated
// attachment on the user turn (the same rule as TurnInput.KanbanItem), never as
// prompt text, a directive or part of the system block. The harness's own facts
// about the replay (which files applied, which did not) are in the directive.
type TeleportReplay struct {
	// Label names the attachment, e.g. "teleport replay".
	Label string
	// Body is the composed text. It is sanitised and, unless the posture ignores
	// tool results, classified before it is promoted.
	Body string
}

// TeleportWithheldError is returned when the security classifier withheld a
// replay's hand-over. A withheld hand-over is never replayed: the caller tells
// the user and leaves the checkout as the harness's patch application left it.
type TeleportWithheldError struct {
	Sentinel rolemanager.Sentinel
}

func (e *TeleportWithheldError) Error() string {
	return "teleport replay material withheld by the security classifier: " + e.Sentinel.Label()
}

// teleportAttachment gates the replay material exactly as a KanbanSearch result
// is gated: sanitised always, classified unless the posture ignores tool
// results.
func (s *Session) teleportAttachment(ctx context.Context, pipe *rolemanager.Pipeline, r *TeleportReplay) (run.Attachment, error) {
	label := sanitize.Line(r.Label, 80)
	if label == "" {
		label = "teleport replay"
	}
	if s.live.Level(posture.ToolResultUnsafe) == posture.Ignore {
		return run.Attachment{Kind: "teleport", Label: label, Body: sanitize.Sanitize(r.Body)}, nil
	}
	dec, err := pipe.Process(ctx, tools.Result{Kind: tools.KindTeleport, Content: r.Body})
	if err != nil {
		return run.Attachment{}, err
	}
	if dec.Action != rolemanager.ActionProceed {
		s.verdictWithheld.Add(1)
		return run.Attachment{}, &TeleportWithheldError{Sentinel: dec.Sentinel}
	}
	return run.Attachment{Kind: "teleport", Label: label, Body: dec.Content}, nil
}
