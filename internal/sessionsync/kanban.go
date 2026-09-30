package sessionsync

import (
	"context"
	"fmt"
	"net/http"
)

// The kanban routes live beside the session routes under /v1/belai and share
// the client, so they inherit its origin allowlist, its credential handling
// and its response bounds unchanged.

// KanbanMove is one history entry of a kanban item on the wire.
type KanbanMove struct {
	ID        string `json:"id"`
	From      string `json:"from,omitempty"`
	To        string `json:"to"`
	At        int64  `json:"at"`
	SessionID string `json:"sessionId,omitempty"`
	Note      string `json:"note,omitempty"`
}

// KanbanItem is a kanban item on the wire. Version is the server's; the host
// sends the last one it saw.
type KanbanItem struct {
	ID         string       `json:"id"`
	Title      string       `json:"title"`
	Body       string       `json:"body,omitempty"`
	List       string       `json:"list"`
	Project    string       `json:"project,omitempty"`
	ProjectKey string       `json:"projectKey,omitempty"`
	Cwd        string       `json:"cwd,omitempty"`
	HostID     string       `json:"hostId,omitempty"`
	SessionID  string       `json:"sessionId,omitempty"`
	CreatedAt  int64        `json:"createdAt"`
	UpdatedAt  int64        `json:"updatedAt"`
	History    []KanbanMove `json:"history,omitempty"`
	Version    int64        `json:"version,omitempty"`
	Deleted    bool         `json:"deleted,omitempty"`
	// Agent carries the routing and claim fields. It is always sent. A
	// backend that predates them omits it on a pull, and the host then keeps
	// its own values rather than clearing them.
	Agent *KanbanAgent `json:"agent,omitempty"`
}

// KanbanAgent is a kanban item's routing and claim state on the wire. The
// website may edit the routing fields and clear a claim; claims themselves
// are only ever made on a host.
type KanbanAgent struct {
	Labels     []string `json:"labels,omitempty"`
	Priority   int      `json:"priority,omitempty"`
	Assignee   string   `json:"assignee,omitempty"`
	PinHost    string   `json:"pinHost,omitempty"`
	Parent     string   `json:"parent,omitempty"`
	DependsOn  []string `json:"dependsOn,omitempty"`
	Hops       int      `json:"hops,omitempty"`
	ClaimedBy  string   `json:"claimedBy,omitempty"`
	ClaimHost  string   `json:"claimHost,omitempty"`
	ClaimFrom  string   `json:"claimFrom,omitempty"`
	LeaseUntil int64    `json:"leaseUntil,omitempty"`
	Attempts   int      `json:"attempts,omitempty"`
	Branch     string   `json:"branch,omitempty"`
	PR         string   `json:"pr,omitempty"`
	// Finding, SeenRef, Verdict and VEX are a security card's facts. Only a
	// host sets them (the website ignores them on an edit), and a host that
	// pulls them takes a value only into a field it holds none for.
	Finding string `json:"finding,omitempty"`
	SeenRef string `json:"seenRef,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	VEX     string `json:"vex,omitempty"`
}

// KanbanAck is the server's answer for one pushed item. Applied is false when
// the server already held a newer change; the next pull brings it down.
type KanbanAck struct {
	ID        string `json:"id"`
	UpdatedAt int64  `json:"updatedAt"`
	Version   int64  `json:"version"`
	Applied   bool   `json:"applied"`
}

// MaxKanbanBatch is the most items one batch push carries.
const MaxKanbanBatch = 100

// KanbanList pulls every item changed after version since, tombstones
// included. more is true when the page was full and the caller should pull
// again from cursor.
func (c *Client) KanbanList(ctx context.Context, since int64, limit int) (items []KanbanItem, cursor int64, more bool, err error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	var out struct {
		Items  []KanbanItem `json:"items"`
		Cursor int64        `json:"cursor"`
		More   bool         `json:"more"`
	}
	path := fmt.Sprintf("/kanban/items?since=%d&limit=%d", since, limit)
	err = c.do(ctx, http.MethodGet, path, nil, &out, requestTimeout)
	return out.Items, out.Cursor, out.More, err
}

// KanbanBatch pushes up to MaxKanbanBatch items (a Deleted item is a delete).
func (c *Client) KanbanBatch(ctx context.Context, items []KanbanItem) ([]KanbanAck, error) {
	if len(items) > MaxKanbanBatch {
		return nil, fmt.Errorf("sessionsync: kanban batch of %d exceeds %d", len(items), MaxKanbanBatch)
	}
	var out struct {
		Acks []KanbanAck `json:"acks"`
	}
	err := c.do(ctx, http.MethodPost, "/kanban/items/batch", map[string]any{"items": items}, &out, requestTimeout)
	return out.Acks, err
}
