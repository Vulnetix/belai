package kanban

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/session"
)

// Claims let worker agents take items off the board without two of them ever
// holding the same one. Every claim operation is one Store.mutate: the
// cross-process lock is held, the board is reread from disk, the change is
// applied and written atomically. The harness makes every claim; no model
// argument ever sets a claim field.

// Routing limits.
const (
	MaxLabels     = 8
	MaxLabelRunes = 32
	MinPriority   = -2
	MaxPriority   = 3
	// MaxDepends caps an item's dependency list.
	MaxDepends = 16
	// MinLease and MaxLease bound a claim's lease.
	MinLease = time.Minute
	MaxLease = 2 * time.Hour
)

// Claim errors.
var (
	ErrNoWork       = errors.New("kanban: no claimable item")
	ErrLeaseLost    = errors.New("kanban: the claim is no longer held by this worker")
	ErrClaimed      = errors.New("kanban: the item is claimed by another worker")
	ErrNotClaimable = errors.New("kanban: the item is not claimable")
	ErrBadAssignee  = errors.New("kanban: an assignee is a profile name ([A-Za-z0-9._:-], at most 64)")
)

// ClaimableLists are the lists a worker may claim from. An in_progress item
// is someone's work; blocked needs a human; done is done.
var ClaimableLists = []List{Backlog, Review}

var labelUnsafe = regexp.MustCompile(`[^a-z0-9:_-]+`)

// NormLabels lower-cases labels, maps unsafe runs to "-", trims, drops
// empties and duplicates, caps each at MaxLabelRunes and the set at
// MaxLabels, and sorts them.
func NormLabels(in []string) []string {
	var out []string
	for _, l := range in {
		l = strings.Trim(labelUnsafe.ReplaceAllString(strings.ToLower(strings.TrimSpace(l)), "-"), "-")
		if r := []rune(l); len(r) > MaxLabelRunes {
			l = strings.Trim(string(r[:MaxLabelRunes]), "-")
		}
		if l == "" || slices.Contains(out, l) {
			continue
		}
		out = append(out, l)
	}
	sort.Strings(out)
	if len(out) > MaxLabels {
		out = out[:MaxLabels]
	}
	return out
}

// ClampPriority bounds p to MinPriority..MaxPriority.
func ClampPriority(p int) int { return min(max(p, MinPriority), MaxPriority) }

var assigneeShape = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// AssigneeKind says what an assignee names. A bare value is a profile, as it
// always was; the prefixes "worker:" and "crew:" name a profile or a crew
// explicitly, and "person:" names someone on the website, who is not a worker.
type AssigneeKind string

const (
	AssigneeNone    AssigneeKind = ""
	AssigneeProfile AssigneeKind = "profile"
	AssigneeCrew    AssigneeKind = "crew"
	AssigneePerson  AssigneeKind = "person"
)

// ParseAssignee splits an assignee into its kind and name. A profile whose own
// name starts with one of the prefixes must be written with "worker:".
func ParseAssignee(s string) (AssigneeKind, string) {
	switch {
	case s == "":
		return AssigneeNone, ""
	case strings.HasPrefix(s, "crew:"):
		return AssigneeCrew, strings.TrimPrefix(s, "crew:")
	case strings.HasPrefix(s, "person:"):
		return AssigneePerson, strings.TrimPrefix(s, "person:")
	case strings.HasPrefix(s, "worker:"):
		return AssigneeProfile, strings.TrimPrefix(s, "worker:")
	}
	return AssigneeProfile, s
}

// assigneeTakes reports whether a worker of the given profile and crew may
// take an item assigned to a. A person is not a worker, so it never matches.
func assigneeTakes(assignee, profile, crew string) bool {
	kind, name := ParseAssignee(assignee)
	switch kind {
	case AssigneeNone:
		return true
	case AssigneeProfile:
		return name == profile
	case AssigneeCrew:
		return crew != "" && name == crew
	}
	return false
}

// CleanAssignee trims an assignee and checks it has a profile name's shape.
// Empty is allowed: the item is routed to any matching worker.
func CleanAssignee(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if !assigneeShape.MatchString(s) {
		return "", ErrBadAssignee
	}
	return s, nil
}

func hasLabels(it Item, want []string) bool {
	for _, l := range want {
		if !slices.Contains(it.Labels, l) {
			return false
		}
	}
	return true
}

// resolveRefs maps item refs to full ids. An unknown ref is an error.
func resolveRefs(b *Board, refs []string) ([]string, error) {
	var out []string
	for _, r := range refs {
		if strings.TrimSpace(r) == "" {
			continue
		}
		i, err := find(b, r)
		if err != nil {
			return nil, fmt.Errorf("depends on %s: %w", r, err)
		}
		if !slices.Contains(out, b.Items[i].ID) {
			out = append(out, b.Items[i].ID)
		}
	}
	if len(out) > MaxDepends {
		return nil, fmt.Errorf("kanban: an item depends on at most %d items", MaxDepends)
	}
	return out, nil
}

func cloneItem(it Item) Item {
	it.History = slices.Clone(it.History)
	it.Labels = slices.Clone(it.Labels)
	it.DependsOn = slices.Clone(it.DependsOn)
	it.Gates = cloneGates(it.Gates)
	it.Clauses = slices.Clone(it.Clauses)
	it.Covers = slices.Clone(it.Covers)
	return it
}

// Claimed reports whether the item is held under a lease that has not
// lapsed at now (unix ms).
func (it Item) Claimed(now int64) bool {
	return it.ClaimedBy != "" && it.LeaseUntil > now
}

// ClaimRequest is what a worker asks the board for.
type ClaimRequest struct {
	// Lists to claim from; each must be in ClaimableLists. Empty means
	// backlog.
	Lists []List
	// Labels the item must all carry.
	Labels []string
	// Profile is the claiming agent's profile name. An item with an Assignee
	// is claimable only by that profile; AssignedOnly also skips unassigned
	// items.
	Profile      string
	AssignedOnly bool
	// Crew is the crew this worker was started in, empty for a lone worker.
	// An item assigned to "crew:NAME" is claimable by that crew's workers.
	Crew string
	// Project limits the claim to one project (name or key); empty is any.
	Project string
	// Remote is the worker's checkout's origin as owner/name (NormRemote).
	// With Project set, an item filed in another checkout of the same
	// repository (on this machine or another) also matches: its Remote, or a
	// Project the Software Factory wrote as owner/name, equals this one.
	Remote string
	// Worker is the worker instance id; Host its sync host id.
	Worker string
	Host   string
	// SessionID is stamped on the history entry.
	SessionID string
	Lease     time.Duration
	// Skip lists item ids this worker will not take again (it failed them).
	Skip []string
	// OpenPR reports whether a pull request url is still open on the forge. It
	// feeds the duplicate guard (guard.go): a backlog finding card is not
	// claimed while another card for the same finding or package is claimed or
	// has an open pull request. Nil means the forge could not be asked, and
	// then only the claims hold a card back.
	OpenPR func(pr string) bool
}

func (r ClaimRequest) validate() error {
	if strings.TrimSpace(r.Worker) == "" {
		return errors.New("kanban: a claim needs a worker id")
	}
	if r.Lease < MinLease || r.Lease > MaxLease {
		return fmt.Errorf("kanban: a lease runs %s to %s", MinLease, MaxLease)
	}
	for _, l := range r.Lists {
		if !slices.Contains(ClaimableLists, l) {
			return fmt.Errorf("kanban: items are claimed from backlog or review, not %s", l)
		}
	}
	return nil
}

func (r ClaimRequest) lists() []List {
	if len(r.Lists) == 0 {
		return []List{Backlog}
	}
	return r.Lists
}

// reapLocked returns every lapsed claim to the list it was claimed from and
// counts it as a failed attempt. It returns a copy of each item it reaped, taken
// before the release so the claim is still on it, for the audit log.
func reapLocked(b *Board, now int64) []Item {
	var lapsed []Item
	for i := range b.Items {
		it := &b.Items[i]
		if it.Deleted || it.ClaimedBy == "" || it.LeaseUntil > now {
			continue
		}
		lapsed = append(lapsed, cloneItem(*it))
		releaseLocked(it, it.claimSource(), "claim lapsed: the worker stopped renewing its lease", "", now, true)
	}
	return lapsed
}

// claimSource is where a released item goes back to.
func (it Item) claimSource() List {
	if slices.Contains(ClaimableLists, it.ClaimFrom) {
		return it.ClaimFrom
	}
	return Backlog
}

// StageRank orders the lists an item moves through: backlog, review, done.
// The lists work happens in (in progress) and waits in (blocked) have no rank,
// so they are never a step forward or back.
func StageRank(l List) int {
	switch l {
	case Backlog:
		return 0
	case Review:
		return 1
	case Done:
		return 2
	}
	return -1
}

// MovesBack reports whether a release from list from to list to sends the item
// to an earlier stage: a review that rejects a branch goes back to the backlog.
func MovesBack(from, to List) bool {
	f, t := StageRank(from), StageRank(to)
	return f >= 0 && t >= 0 && t < f
}

// releaseLocked puts a claimed item in list to. Attempts are counted per stage:
// a failure that retries the stage counts one, a failure that sends the item back
// to an earlier stage counts a bounce and starts that stage's attempts afresh,
// and a success that moves the item on starts the next stage's attempts afresh.
func releaseLocked(it *Item, to List, note, sid string, now int64, failed bool) {
	releaseLockedAs(it, to, note, sid, now, failed, false)
}

// releaseLockedAs is releaseLocked for a claim that may be a continuation: one
// counts a continuation and leaves the attempts as they are.
func releaseLockedAs(it *Item, to List, note, sid string, now int64, failed, continued bool) {
	from := it.claimSource()
	appendHistory(it, Move{ID: session.MustID(), From: it.List, To: to, At: now, SessionID: sid, Note: CleanBody(note, MaxNoteBytes)})
	it.List = to
	it.ClaimedBy, it.ClaimHost, it.ClaimFrom, it.LeaseUntil = "", "", "", 0
	switch {
	case continued && !failed:
		it.Continuations++
	case failed && MovesBack(from, to):
		it.Bounces++
		it.Attempts, it.Continuations = 0, 0
	case failed:
		it.Attempts++
	case StageRank(to) > StageRank(from) && StageRank(from) >= 0:
		it.Attempts, it.Continuations = 0, 0
	}
	it.Updated, it.Dirty = now, true
}

// depsDone reports whether every dependency is done. A dependency that no
// longer exists (deleted and pruned) no longer blocks.
func depsDone(b *Board, it Item) bool {
	for _, d := range it.DependsOn {
		for _, o := range b.Items {
			if o.ID == d && !o.Deleted && o.List != Done {
				return false
			}
		}
	}
	return true
}

func (r ClaimRequest) matches(b *Board, hold holdIndex, it Item, now int64) bool {
	switch {
	case it.Deleted, it.Claimed(now), it.ClaimedBy != "":
		return false
	case isHeld(it):
		return false
	case !slices.Contains(r.lists(), it.List):
		return false
	case !hasLabels(it, NormLabels(r.Labels)):
		return false
	case !assigneeTakes(it.Assignee, r.Profile, r.Crew):
		return false
	case it.Assignee == "" && r.AssignedOnly:
		return false
	case it.PinHost != "" && it.PinHost != r.Host:
		return false
	case r.Project != "" && !r.sameRepository(it):
		return false
	case slices.Contains(r.Skip, it.ID):
		return false
	}
	if it.List == Backlog {
		if _, held := hold.of(it); held {
			return false
		}
	}
	return depsDone(b, it)
}

// sameRepository reports whether an item belongs to the claim's project: by
// name or key as before, or by the repository's remote, so the key of one
// machine's checkout never keeps another machine's worker from the card.
func (r ClaimRequest) sameRepository(it Item) bool {
	if strings.EqualFold(it.Project, r.Project) || it.ProjectKey == r.Project {
		return true
	}
	rem := NormRemote(r.Remote)
	if rem == "" {
		return false
	}
	if NormRemote(it.Remote) == rem || NormRemote(it.Project) == rem {
		return true
	}
	// An item filed before remotes off github.com kept their host carries
	// the bare owner/name; it is still this checkout's.
	legacy := LegacyRemote(rem)
	return legacy != "" && (NormRemote(it.Remote) == legacy || NormRemote(it.Project) == legacy)
}

func claimLocked(it *Item, r ClaimRequest, now int64) {
	from := it.List
	appendHistory(it, Move{ID: session.MustID(), From: from, To: InProgress, At: now, SessionID: r.SessionID,
		Note: CleanBody("claimed by agent "+r.Profile, MaxNoteBytes)})
	it.List = InProgress
	it.ClaimedBy, it.ClaimHost, it.ClaimFrom = r.Worker, r.Host, from
	it.LeaseUntil = now + r.Lease.Milliseconds()
	it.Updated, it.Dirty = now, true
}

// Claim takes the best matching item — highest priority, then oldest —
// moves it to in_progress and leases it to the worker. Lapsed claims are
// reaped first. ErrNoWork means nothing matched.
func (s *Store) Claim(r ClaimRequest) (Item, error) {
	if err := r.validate(); err != nil {
		return Item{}, err
	}
	var out Item
	var lapsed []Item
	found := false
	err := s.mutate(true, func(b *Board) error {
		now := s.nowMs()
		reaped := reapLocked(b, now)
		lapsed = reaped
		hold := newHoldIndex(b, now, r.OpenPR)
		best := -1
		for i, it := range b.Items {
			if !r.matches(b, hold, it, now) {
				continue
			}
			if best < 0 || it.Priority > b.Items[best].Priority ||
				(it.Priority == b.Items[best].Priority && it.Created < b.Items[best].Created) {
				best = i
			}
		}
		if best < 0 {
			if len(reaped) > 0 {
				return nil // write the reap; the caller still sees ErrNoWork
			}
			return ErrNoWork
		}
		claimLocked(&b.Items[best], r, now)
		out, found = cloneItem(b.Items[best]), true
		return nil
	})
	if err == nil {
		auditLapsed(lapsed)
	}
	if err == nil && !found {
		return Item{}, ErrNoWork
	}
	return out, err
}

// Demand counts the items r would claim now, without claiming or reaping:
// what a crew supervisor weighs when it shares its workers between
// repositories.
func (s *Store) Demand(r ClaimRequest) (int, error) {
	n := 0
	err := s.read(func(b *Board) {
		now := s.nowMs()
		hold := newHoldIndex(b, now, r.OpenPR)
		for _, it := range b.Items {
			if r.matches(b, hold, it, now) {
				n++
			}
		}
	})
	return n, err
}

// ClaimID claims one named item. It must be in a claimable list, unclaimed,
// have its dependencies done, and not be assigned to another profile.
func (s *Store) ClaimID(ref string, r ClaimRequest) (Item, error) {
	if err := r.validate(); err != nil {
		return Item{}, err
	}
	var out Item
	var lapsed []Item
	err := s.mutate(true, func(b *Board) error {
		now := s.nowMs()
		lapsed = reapLocked(b, now)
		i, err := find(b, ref)
		if err != nil {
			return err
		}
		it := b.Items[i]
		if it.ClaimedBy != "" {
			return ErrClaimed
		}
		req := r
		req.Lists = ClaimableLists
		req.Labels, req.Skip, req.AssignedOnly, req.Project = nil, nil, false, ""
		hold := newHoldIndex(b, now, r.OpenPR)
		if it.List == Backlog {
			if h, held := hold.of(it); held {
				return fmt.Errorf("%w: %s duplicates %s", ErrNotClaimable, it.Short(), h)
			}
		}
		if !req.matches(b, hold, it, now) {
			return fmt.Errorf("%w: %s is in %s", ErrNotClaimable, it.Short(), it.List)
		}
		claimLocked(&b.Items[i], r, now)
		out = cloneItem(b.Items[i])
		return nil
	})
	if err == nil {
		auditLapsed(lapsed)
	}
	return out, err
}

// Renew extends the worker's lease. It is host-local bookkeeping: no
// history entry, no sync, no Updated bump — a renewal every few minutes must
// not churn the website. ErrLeaseLost means the item is no longer this
// worker's (released on the web or by a human, or reaped).
func (s *Store) Renew(ref, worker string, lease time.Duration) error {
	if lease < MinLease || lease > MaxLease {
		return fmt.Errorf("kanban: a lease runs %s to %s", MinLease, MaxLease)
	}
	return s.mutate(false, func(b *Board) error {
		i, err := find(b, ref)
		if err != nil {
			return ErrLeaseLost
		}
		it := &b.Items[i]
		if it.ClaimedBy != worker || it.List != InProgress {
			return ErrLeaseLost
		}
		it.LeaseUntil = s.nowMs() + lease.Milliseconds()
		return nil
	})
}

// SetBranch records the branch the claiming worker works the item on, as soon
// as its worktree exists: a machine that dies mid-item then still names the
// branch its work was pushed to, so the next claim resumes it.
func (s *Store) SetBranch(ref, worker, branch string) error {
	err := s.mutate(true, func(b *Board) error {
		i, err := find(b, ref)
		if err != nil {
			return err
		}
		it := &b.Items[i]
		if it.ClaimedBy != worker || worker == "" {
			return ErrLeaseLost
		}
		if it.Branch == CleanTitle(branch) {
			return errNoWrite
		}
		it.Branch = CleanTitle(branch)
		it.Updated, it.Dirty = s.nowMs(), true
		return nil
	})
	if errors.Is(err, errNoWrite) {
		return nil
	}
	return err
}

// Outcome is how a worker hands an item back.
type Outcome struct {
	// To is the list the item goes to.
	To   List
	Note string
	// SessionID is stamped on the history entry.
	SessionID string
	// Failed counts the claim as a failed attempt.
	Failed bool
	// Continued counts the claim as a continuation: it ran out of budget after
	// making progress and goes back to be resumed. It never counts an attempt.
	Continued bool
	// AddLabels and DropLabels edit the item's labels on the way out.
	AddLabels  []string
	DropLabels []string
	// Branch and PR, when set, record where the work is.
	Branch string
	PR     string
	// Verdict and VEX, when set, record a security verdict and the relative
	// path of the VEX written for it.
	Verdict Verdict
	VEX     string
	// Releaser is the releasing worker's profile. When the item moves to
	// another list and is assigned to that profile, the assignment is cleared:
	// it was for this stage, and the next stage claims only unassigned items or
	// its own.
	Releaser string
}

// Release ends the worker's claim and moves the item. ErrLeaseLost means the
// worker no longer holds it and nothing was changed.
func (s *Store) Release(ref, worker string, o Outcome) (Item, error) {
	if !o.To.Valid() {
		return Item{}, ErrBadList
	}
	var out Item
	err := s.mutate(true, func(b *Board) error {
		i, err := find(b, ref)
		if err != nil {
			return err
		}
		it := &b.Items[i]
		if it.ClaimedBy != worker || worker == "" {
			return ErrLeaseLost
		}
		if o.Releaser != "" && it.Assignee == o.Releaser && o.To != it.claimSource() {
			it.Assignee = ""
		}
		releaseLockedAs(it, o.To, o.Note, o.SessionID, s.nowMs(), o.Failed, o.Continued)
		labels := slices.DeleteFunc(slices.Clone(it.Labels), func(l string) bool {
			return slices.Contains(NormLabels(keepCheckpoints(o.DropLabels)), l)
		})
		it.Labels = NormLabels(append(labels, o.AddLabels...))
		if o.Branch != "" {
			it.Branch = CleanTitle(o.Branch)
		}
		if o.PR != "" {
			it.PR = CleanTitle(o.PR)
		}
		if o.Verdict.Valid() {
			it.Verdict = o.Verdict
		}
		if o.VEX != "" {
			it.VEX = CleanTitle(o.VEX)
		}
		out = cloneItem(*it)
		return nil
	})
	return out, err
}

// Unclaim clears a claim whoever holds it — a human's override from the CLI
// or TUI, or the registry releasing a dead worker's items — and returns the
// item to the list it was claimed from. failed counts it as an attempt.
func (s *Store) Unclaim(ref, note, sessionID string, failed bool) (Item, error) {
	var out Item
	err := s.mutate(true, func(b *Board) error {
		i, err := find(b, ref)
		if err != nil {
			return err
		}
		it := &b.Items[i]
		if it.ClaimedBy == "" {
			out = cloneItem(*it)
			return errNoWrite
		}
		releaseLocked(it, it.claimSource(), note, sessionID, s.nowMs(), failed)
		out = cloneItem(*it)
		return nil
	})
	if errors.Is(err, errNoWrite) {
		return out, nil
	}
	return out, err
}

// UnclaimWorker releases every item a worker holds, for a worker whose
// process has died. It returns the short ids released.
func (s *Store) UnclaimWorker(worker, note string) ([]string, error) {
	if worker == "" {
		return nil, nil
	}
	var ids []string
	err := s.mutate(true, func(b *Board) error {
		now := s.nowMs()
		for i := range b.Items {
			it := &b.Items[i]
			if it.Deleted || it.ClaimedBy != worker {
				continue
			}
			releaseLocked(it, it.claimSource(), note, "", now, true)
			ids = append(ids, it.Short())
		}
		if len(ids) == 0 {
			return errNoWrite
		}
		return nil
	})
	if errors.Is(err, errNoWrite) {
		return nil, nil
	}
	return ids, err
}

// Reap returns every lapsed claim to its source list.
func (s *Store) Reap() (int, error) {
	var lapsed []Item
	err := s.mutate(true, func(b *Board) error {
		lapsed = reapLocked(b, s.nowMs())
		if len(lapsed) == 0 {
			return errNoWrite
		}
		return nil
	})
	if errors.Is(err, errNoWrite) {
		return 0, nil
	}
	if err == nil {
		auditLapsed(lapsed)
	}
	return len(lapsed), err
}

// MoveAs is Move for a model's tool call. holder is the calling worker's id,
// or "" for an ordinary session. An item under another worker's live claim
// is refused, and a claimed item may not be moved by its holder either: the
// harness releases it. from, when set, is a compare-and-set on the item's
// current list.
func (s *Store) MoveAs(ref string, to List, note, sessionID, holder string, from []List) (Item, error) {
	if !to.Valid() {
		return Item{}, ErrBadList
	}
	var out Item
	err := s.mutate(true, func(b *Board) error {
		i, err := find(b, ref)
		if err != nil {
			return err
		}
		it := &b.Items[i]
		now := s.nowMs()
		if it.Claimed(now) {
			return ErrClaimed
		}
		if len(from) > 0 && !slices.Contains(from, it.List) {
			return fmt.Errorf("kanban: %s is in %s, not %s", it.Short(), it.List, joinLists(from))
		}
		appendHistory(it, Move{ID: session.MustID(), From: it.List, To: to, At: now, SessionID: sessionID, Note: CleanBody(note, MaxNoteBytes)})
		it.List, it.Updated, it.Dirty = to, now, true
		// A lapsed claim does not survive a move.
		it.ClaimedBy, it.ClaimHost, it.ClaimFrom, it.LeaseUntil = "", "", "", 0
		out = cloneItem(*it)
		return nil
	})
	return out, err
}

// UpdateAs is Update for a model's tool call: an item under another
// worker's live claim is refused. The holder itself may only add notes —
// rewriting the title or body of claimed work would carry text forward to
// the next worker.
func (s *Store) UpdateAs(ref string, p Patch, sessionID, holder string) (Item, error) {
	return s.update(ref, p, sessionID, func(it Item, now int64) error {
		if !it.Claimed(now) {
			return nil
		}
		if it.ClaimedBy != holder || holder == "" {
			return ErrClaimed
		}
		if p.Title != nil || p.Body != nil {
			return ErrNotesOnly
		}
		return nil
	})
}

// ErrNotesOnly refuses a title or body rewrite of claimed work.
var ErrNotesOnly = errors.New("kanban: a worker may only add notes to the item it holds")

func joinLists(ls []List) string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = string(l)
	}
	return strings.Join(out, " or ")
}

// SetPR records the draft pull request the harness opened for an item.
func (s *Store) SetPR(ref, url, sessionID string) (Item, error) {
	var out Item
	err := s.mutate(true, func(b *Board) error {
		i, err := find(b, ref)
		if err != nil {
			return err
		}
		it := &b.Items[i]
		now := s.nowMs()
		it.PR = CleanTitle(url)
		appendHistory(it, Move{ID: session.MustID(), From: it.List, To: it.List, At: now, SessionID: sessionID, Note: CleanBody("draft pull request opened: "+it.PR, MaxNoteBytes)})
		it.Updated, it.Dirty = now, true
		out = cloneItem(*it)
		return nil
	})
	return out, err
}

// RoutePatch edits an item's routing. Nil fields are left alone.
type RoutePatch struct {
	Labels   *[]string
	Priority *int
	Assignee *string
	// PinHost pins the item to one sync host id; "" unpins it.
	PinHost *string
}

// Route edits an item's labels, priority or assignee — a human's routing from
// the CLI, the TUI or the web. It records a note saying what changed.
func (s *Store) Route(ref string, p RoutePatch, sessionID string) (Item, error) {
	var assignee string
	if p.Assignee != nil {
		a, err := CleanAssignee(*p.Assignee)
		if err != nil {
			return Item{}, err
		}
		assignee = a
	}
	var out Item
	err := s.mutate(true, func(b *Board) error {
		i, err := find(b, ref)
		if err != nil {
			return err
		}
		it := &b.Items[i]
		var what []string
		if p.Labels != nil {
			it.Labels = NormLabels(*p.Labels)
			what = append(what, "labels "+strings.Join(it.Labels, ","))
		}
		if p.Priority != nil {
			it.Priority = ClampPriority(*p.Priority)
			what = append(what, fmt.Sprintf("priority %d", it.Priority))
		}
		if p.Assignee != nil {
			it.Assignee = assignee
			if assignee == "" {
				what = append(what, "unassigned")
			} else {
				what = append(what, "assigned to "+assignee)
			}
		}
		if p.PinHost != nil {
			it.PinHost = CleanTitle(*p.PinHost)
			if it.PinHost == "" {
				what = append(what, "unpinned")
			} else {
				what = append(what, "pinned to host "+it.PinHost)
			}
		}
		if len(what) == 0 {
			out = cloneItem(*it)
			return errNoWrite
		}
		now := s.nowMs()
		appendHistory(it, Move{ID: session.MustID(), From: it.List, To: it.List, At: now, SessionID: sessionID, Note: CleanBody(strings.Join(what, "; "), MaxNoteBytes)})
		it.Updated, it.Dirty = now, true
		out = cloneItem(*it)
		return nil
	})
	if errors.Is(err, errNoWrite) {
		return out, nil
	}
	return out, err
}
