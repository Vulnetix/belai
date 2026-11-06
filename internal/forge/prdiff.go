package forge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DiffTimeout bounds one call that reads a pull request's diff. A diff is
// larger than a status line, so it gets more room than ReadTimeout.
const DiffTimeout = 30 * time.Second

// MaxPRDiffBytes bounds the diff text read from a forge. A longer one is cut
// and Truncated is set; the review applies its own, smaller cap on top.
const MaxPRDiffBytes = 16 << 20

// PRDiff is the read-only view of a pull request the code review needs: the
// unified diff and the commit it covers. Text is third-party text and is
// parsed by the review, never shown or sent as it is.
type PRDiff struct {
	// Diff is the unified diff text, at most MaxPRDiffBytes.
	Diff string
	// Truncated reports that Diff was cut at MaxPRDiffBytes.
	Truncated bool
	// HeadSHA is the full lower-case hex id of the PR's head commit, or "" when
	// the forge did not give one in that shape.
	HeadSHA string
}

var shaRE = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// cleanSHA returns s lower-cased when it is a full commit id, otherwise "".
func cleanSHA(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if shaRE.MatchString(s) {
		return s
	}
	return ""
}

func capDiff(out string) (string, bool) {
	if len(out) > MaxPRDiffBytes {
		return out[:MaxPRDiffBytes], true
	}
	return out, false
}

func (g github) DiffPR(ctx context.Context, dir string, number int) (PRDiff, error) {
	if number <= 0 {
		return PRDiff{}, errors.New("no pull request number")
	}
	n := strconv.Itoa(number)
	diff, err := run(ctx, g.r, DiffTimeout, dir, "gh", "pr", "diff", n, "--color=never")
	if err != nil {
		return PRDiff{}, err
	}
	out := PRDiff{}
	out.Diff, out.Truncated = capDiff(diff)
	view, err := run(ctx, g.r, ReadTimeout, dir, "gh", "pr", "view", n, "--json", "headRefOid")
	if err != nil {
		return out, nil // no head id: the review falls back to the checkout's HEAD
	}
	var v struct {
		Oid string `json:"headRefOid"`
	}
	if json.Unmarshal([]byte(view), &v) == nil {
		out.HeadSHA = cleanSHA(v.Oid)
	}
	return out, nil
}

func (g gitlab) DiffPR(ctx context.Context, dir string, number int) (PRDiff, error) {
	if number <= 0 {
		return PRDiff{}, errors.New("no merge request number")
	}
	n := strconv.Itoa(number)
	diff, err := run(ctx, g.r, DiffTimeout, dir, "glab", "mr", "diff", n, "--raw", "--color=never")
	if err != nil {
		return PRDiff{}, err
	}
	out := PRDiff{}
	out.Diff, out.Truncated = capDiff(diff)
	view, err := run(ctx, g.r, ReadTimeout, dir, "glab", "mr", "view", n, "-F", "json")
	if err != nil {
		return out, nil
	}
	var v struct {
		SHA string `json:"sha"`
	}
	if json.Unmarshal([]byte(view), &v) == nil {
		out.HeadSHA = cleanSHA(v.SHA)
	}
	return out, nil
}

func (g gitea) DiffPR(ctx context.Context, _ string, number int) (PRDiff, error) {
	if number <= 0 {
		return PRDiff{}, errors.New("no pull request number")
	}
	raw, err := g.fetch(ctx, DiffTimeout, http.MethodGet, g.repoPath()+"/pulls/"+strconv.Itoa(number)+".diff", "text/plain", nil)
	if err != nil {
		return PRDiff{}, err
	}
	out := PRDiff{}
	out.Diff, out.Truncated = capDiff(string(raw))
	var p giteaPull
	if err := g.do(ctx, ReadTimeout, http.MethodGet, g.repoPath()+"/pulls/"+strconv.Itoa(number), nil, &p); err == nil {
		out.HeadSHA = cleanSHA(p.Head.Sha)
	}
	return out, nil
}
