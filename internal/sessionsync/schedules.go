package sessionsync

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// A host's scheduled agents live beside the session routes under
// /v1/belai/hosts/{id}/schedules and share the client, so they inherit its
// origin allowlist, its credential handling and its response bounds
// unchanged.

// Schedule is a scheduled agent on the wire: a worker profile, a cron
// expression and a directory the host advertises. The definition (Profile,
// Cron, Dir, Enabled) is last-writer-wins on UpdatedAt; the run record
// (LastRunAt, LastStatus, NextRunAt) is the host's alone, and the website
// never writes it. Version is the server's; a pull returns it.
type Schedule struct {
	ID         string `json:"id"`
	Profile    string `json:"profile"`
	Cron       string `json:"cron"`
	Dir        string `json:"dir"`
	Enabled    bool   `json:"enabled"`
	LastRunAt  *int64 `json:"lastRunAt,omitempty"`
	LastStatus string `json:"lastStatus,omitempty"`
	NextRunAt  *int64 `json:"nextRunAt,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
	UpdatedAt  int64  `json:"updatedAt"`
	Version    int64  `json:"version,omitempty"`
	Deleted    bool   `json:"deleted,omitempty"`
}

// ScheduleAck is the server's answer for one pushed schedule. Applied is
// false when the server already held a newer definition; the next pull brings
// it down.
type ScheduleAck struct {
	ID        string `json:"id"`
	UpdatedAt int64  `json:"updatedAt"`
	Version   int64  `json:"version"`
	Applied   bool   `json:"applied"`
}

// MaxScheduleBatch is the most schedules one push carries.
const MaxScheduleBatch = 100

// ScheduleList pulls every schedule of this host changed after version since,
// tombstones included. more is true when the page was full and the caller
// should pull again from cursor.
func (c *Client) ScheduleList(ctx context.Context, hostID string, since int64, limit int) (items []Schedule, cursor int64, more bool, err error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	var out struct {
		Items  []Schedule `json:"items"`
		Cursor int64      `json:"cursor"`
		More   bool       `json:"more"`
	}
	path := fmt.Sprintf("/hosts/%s/schedules?since=%d&limit=%d", url.PathEscape(hostID), since, limit)
	err = c.do(ctx, http.MethodGet, path, nil, &out, requestTimeout)
	return out.Items, out.Cursor, out.More, err
}

// ScheduleBatch pushes up to MaxScheduleBatch schedules (a Deleted one is a
// delete).
func (c *Client) ScheduleBatch(ctx context.Context, hostID string, items []Schedule) ([]ScheduleAck, error) {
	if len(items) > MaxScheduleBatch {
		return nil, fmt.Errorf("sessionsync: schedule batch of %d exceeds %d", len(items), MaxScheduleBatch)
	}
	var out struct {
		Acks []ScheduleAck `json:"acks"`
	}
	err := c.do(ctx, http.MethodPut, "/hosts/"+url.PathEscape(hostID)+"/schedules", map[string]any{"items": items}, &out, requestTimeout)
	return out.Acks, err
}
