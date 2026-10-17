// Package attention lists what is waiting on a person. A flow stage that holds
// its card at a checkpoint, a card assigned to a person and a card a worker
// blocked all sit on the kanban board until someone looks at them; this package
// turns the board into one ordered queue, most blocking first. It imports no
// terminal code, so the TUI, a command and a test can all use it.
//
// Every field is a harness fact: a card's id, title, list, labels and the
// checkpoint reason. Nothing is read from a model's text beyond the card's own
// title and its last history note, which the caller shows as the card's.
package attention

import (
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/kanban"
)

// Kind says why a card is waiting. The order is the queue's order: a checkpoint
// holds a whole flow behind it, so it comes first.
type Kind string

const (
	// KindCheckpoint is a card a flow stage holds for a decision.
	KindCheckpoint Kind = "checkpoint"
	// KindPerson is a card assigned to a person, which no worker claims.
	KindPerson Kind = "person"
	// KindBlocked is a card a worker could not finish.
	KindBlocked Kind = "blocked"
)

// Kinds lists the kinds in queue order.
var Kinds = []Kind{KindCheckpoint, KindPerson, KindBlocked}

func (k Kind) rank() int {
	for i, o := range Kinds {
		if o == k {
			return i
		}
	}
	return len(Kinds)
}

// Lists are the board lists the queue reads.
var Lists = []kanban.List{kanban.Backlog, kanban.Review, kanban.Blocked}

// Item is one card waiting on a person.
type Item struct {
	Kind Kind
	// ID is the card's full id and Short its short one.
	ID, Short string
	Title     string
	Project   string
	List      kanban.List
	// Reason is the checkpoint reason, the assignee ("person:HANDLE"), or empty
	// for a blocked card.
	Reason string
	// Since is when the card last changed, unix milliseconds: how long it has
	// waited.
	Since int64
	// Note is the card's latest history note, which says why it is here.
	Note string
}

// Decidable reports whether a person can approve or reject the item with a
// checkpoint decision.
func (i Item) Decidable() bool { return i.Kind == KindCheckpoint }

// FromCards picks the cards that wait on a person and orders them: by kind,
// then the longest waiting first, then by id so the order is stable. A card a
// worker holds, a deleted one, and one in a list a worker would claim from
// without a checkpoint are not in the queue.
func FromCards(cards []kanban.Item) []Item {
	var out []Item
	for _, it := range cards {
		if it.Deleted || it.ClaimedBy != "" {
			continue
		}
		if i, ok := classify(it); ok {
			out = append(out, i)
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if ra, rb := out[a].Kind.rank(), out[b].Kind.rank(); ra != rb {
			return ra < rb
		}
		if out[a].Since != out[b].Since {
			return out[a].Since < out[b].Since
		}
		return out[a].ID < out[b].ID
	})
	return out
}

func classify(it kanban.Item) (Item, bool) {
	base := Item{
		ID: it.ID, Short: it.Short(), Title: it.Title, Project: it.Project,
		List: it.List, Since: it.Updated, Note: it.LastNote(),
	}
	if reason, held := kanban.HeldFor(it); held {
		base.Kind, base.Reason = KindCheckpoint, reason
		return base, true
	}
	if kind, name := kanban.ParseAssignee(it.Assignee); kind == kanban.AssigneePerson && it.List != kanban.Done {
		base.Kind, base.Reason = KindPerson, "person:"+name
		return base, true
	}
	if it.List == kanban.Blocked {
		base.Kind = KindBlocked
		return base, true
	}
	return Item{}, false
}

// Count is how many cards wait on a person.
func Count(cards []kanban.Item) int { return len(FromCards(cards)) }

// Label is the short human name of an item's reason, for a list row. A
// checkpoint reason reads as words; an unknown one (from a newer Belai) is
// shown as it is.
func (i Item) Label() string {
	switch i.Kind {
	case KindCheckpoint:
		return strings.ReplaceAll(i.Reason, "_", " ")
	case KindPerson:
		return i.Reason
	}
	return "blocked"
}
