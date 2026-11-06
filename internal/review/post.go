package review

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/hooks"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/sanitize"
)

// PostToolName is the name a permission rule or a hook matcher uses for the
// post: `ReviewPost(…)` in a rule, `ReviewPost` as a pre_tool matcher. Posting
// is a write to a forge, so a Deny rule, a Block rule and a denying hook stop
// it, and an allow rule or an allowing hook never skips the question.
const PostToolName = "ReviewPost"

// Marker returns the fixed first line of a posted review. It carries the head
// commit so a person or a CI step can tell a review of this head from an
// earlier one.
func Marker(headSHA string) string { return "<!-- belai-review head=" + headSHA + " -->" }

// Post errors. Each is final: nothing is posted after one.
var (
	ErrPostDenied      = errors.New("posting is denied (review.post, a permission rule or a hook)")
	ErrPostNeedsAnswer = errors.New("posting needs a yes from a person, and nobody can be asked here")
	ErrPostNotPR       = errors.New("only the review of a pull request can be posted")
	ErrPostNoHead      = errors.New("the pull request's head commit is unknown, so the post could not be matched to it")
	ErrPostIncomplete  = errors.New("the review has no readable verdict, so it is not posted")
	ErrAlreadyPosted   = errors.New("this review of this head was already posted from this host")
)

// Guard is the permission check in front of a post: the user's permission
// rules and the pre_tool hooks, both matched against [PostToolName].
type Guard struct {
	Perms     permissions.Settings
	Hooks     *hooks.Set
	SessionID string
	Cwd       string
}

// Check returns ErrPostDenied when a rule or a hook refuses the post and
// reports whether a rule or a hook asks for an explicit answer. A hook or rule
// that allows changes nothing.
func (g Guard) Check(ctx context.Context, number int) (ask bool, err error) {
	subject := fmt.Sprintf("pr %d", number)
	switch g.Perms.Evaluate(PostToolName, subject) {
	case permissions.DecisionBlock:
		return false, ErrPostDenied
	case permissions.DecisionAsk:
		ask = true
	}
	if g.Hooks.Has(hooks.EventPreTool) {
		o := g.Hooks.Dispatch(ctx, hooks.Input{
			Event: hooks.EventPreTool, SessionID: g.SessionID, Cwd: g.Cwd,
			ToolName: PostToolName, ToolInput: map[string]any{"pr": number},
		})
		switch o.Decision {
		case hooks.DecisionDeny:
			return false, ErrPostDenied
		case hooks.DecisionAsk:
			ask = true
		}
	}
	return ask, nil
}

// Comment builds the text posted for r. Every field comes from the validated
// result, passes through [sanitize.Inert] so nobody is notified and no link is
// made, and the whole is capped under MaxCommentBytes by dropping the last
// findings and saying how many.
func Comment(r Result) string {
	var head strings.Builder
	head.WriteString(Marker(r.HeadSHA) + "\n")
	fmt.Fprintf(&head, "**Code review** (advisory, written by a model and checked against the diff): verdict `%s`", r.Verdict)
	switch n := len(r.Findings); n {
	case 0:
		head.WriteString(", no findings")
	case 1:
		head.WriteString(", 1 finding")
	default:
		fmt.Fprintf(&head, ", %d findings", n)
	}
	head.WriteString(".\n")
	if r.Truncated {
		fmt.Fprintf(&head, "\nThe diff was cut at a size or file cap; %d changed file(s) were not reviewed.\n", r.Skipped)
	}
	foot := "\nThis comment was posted by a person's Belai session. It does not approve or block the change.\n"
	var b strings.Builder
	b.WriteString(head.String())
	shown := 0
	for i, f := range r.Findings {
		item := fmt.Sprintf("\n%d. `%s:%d` %s/%s: %s\n", i+1, f.File, f.Line, f.Severity, f.Rubric, sanitize.Inert(f.Body, MaxBodyBytes))
		if f.Fix != "" {
			item += "   Suggested fix: " + sanitize.Inert(f.Fix, MaxFixBytes) + "\n"
		}
		if b.Len()+len(item)+len(foot)+120 > MaxCommentBytes {
			break
		}
		b.WriteString(item)
		shown++
	}
	if shown < len(r.Findings) {
		fmt.Fprintf(&b, "\n%d more finding(s) are not shown here.\n", len(r.Findings)-shown)
	}
	b.WriteString(foot)
	return b.String()
}

// PostRequest asks for a review to be posted.
type PostRequest struct {
	Result   Result
	Provider forge.Provider
	Dir      string
	Store    *Store
	Guard    Guard
	// Mode is the effective review.post value.
	Mode string
	// Confirmed is true when a person answered yes to the question for this
	// post, in a surface that can ask.
	Confirmed bool
	// Unattended is true for the headless CLI run with -comment: no person is
	// asked, so only review.post=allow from the user's own layer lets it post.
	Unattended bool
}

// Post posts a review as a comment on its pull request, or returns the reason
// it did not. Any error means nothing was posted.
//
// The order is fixed: the setting, the shape of the result, the permission
// rules and hooks, who answered, the record of an earlier post, then the
// write. A person's yes does not override a Deny rule, and an allow value
// does not skip a rule that asks.
func Post(ctx context.Context, q PostRequest) error {
	r := q.Result
	switch {
	case q.Mode != config.ReviewPostAsk && q.Mode != config.ReviewPostAllow:
		return ErrPostDenied
	case r.Target.Kind != KindPR || r.PR <= 0:
		return ErrPostNotPR
	case !isSHA(r.HeadSHA):
		return ErrPostNoHead
	case r.Incomplete:
		return ErrPostIncomplete
	case q.Provider == nil:
		return errors.New("no forge provider to post with")
	}
	ask, err := q.Guard.Check(ctx, r.PR)
	if err != nil {
		return err
	}
	switch {
	case q.Unattended:
		// Nobody to ask: the user's own allow setting, the -comment flag and no
		// rule or hook that asks.
		if q.Mode != config.ReviewPostAllow || ask {
			return ErrPostNeedsAnswer
		}
	case !q.Confirmed:
		return ErrPostNeedsAnswer
	}
	if q.Store != nil && q.Store.WasPosted(r.Repo, r.ID(), r.PR) {
		return ErrAlreadyPosted
	}
	if err := q.Provider.CommentPR(ctx, q.Dir, r.PR, Comment(r)); err != nil {
		return fmt.Errorf("the forge refused the comment: %s", forge.CleanErr(err))
	}
	if q.Store != nil {
		// The comment is out; failing to record it only risks a repeat.
		_ = q.Store.MarkPosted(r.Repo, r.ID(), r.PR)
	}
	return nil
}
