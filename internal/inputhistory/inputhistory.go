// Package inputhistory records the composer lines that run locally instead of
// becoming a model turn — `!cmd` shells, `!!cmd` processes and slash commands
// — so up/down in the composer can recall them alongside prompts. Prompts are
// already recoverable from the session store; these lines are not, because a
// slash command or a supervised process leaves no user entry behind.
//
// The history is one small JSON file per project, kept oldest first, deduped
// (a repeated line moves to the newest slot) and capped at Max entries.
package inputhistory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/vulnetix/signet/internal/config"
)

// Max is the most lines a history file keeps; the oldest fall off first.
const Max = 500

// Item is one recalled line and when it was entered, in Unix milliseconds.
type Item struct {
	Text      string `json:"text"`
	Timestamp int64  `json:"ts"`
}

// Path returns the history file for a working directory.
func Path(workdir string) (string, error) { return config.InputHistoryPath(workdir) }

// Load reads the history at path, oldest first. A missing file is an empty
// history, not an error.
func Load(path string) ([]Item, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read input history %s: %w", path, err)
	}
	var items []Item
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("parse input history %s: %w", path, err)
	}
	return items, nil
}

// Record appends text at ts to the history at path. An earlier identical line
// is dropped so it is recalled once, at its newest position. Blank text is
// ignored. An unreadable file is replaced rather than blocking the record.
func Record(path, text string, ts int64) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	items, _ := Load(path)
	kept := items[:0]
	for _, it := range items {
		if it.Text != text {
			kept = append(kept, it)
		}
	}
	kept = append(kept, Item{Text: text, Timestamp: ts})
	if len(kept) > Max {
		kept = kept[len(kept)-Max:]
	}
	data, err := json.Marshal(kept)
	if err != nil {
		return fmt.Errorf("marshal input history: %w", err)
	}
	return config.WriteGlobalFileAtomic(path, data)
}

// Newest merges timestamped lists into one newest-first list of unique text.
// Ties keep the order the lists were given, and within a list later items
// count as newer, so an all-zero timestamp list still reads most recent first.
func Newest(lists ...[]Item) []string {
	type ranked struct {
		Item
		list, pos int
	}
	var all []ranked
	for li, l := range lists {
		for pi, it := range l {
			all = append(all, ranked{Item: it, list: li, pos: pi})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Timestamp != all[j].Timestamp {
			return all[i].Timestamp > all[j].Timestamp
		}
		if all[i].list != all[j].list {
			return all[i].list < all[j].list
		}
		return all[i].pos > all[j].pos
	})
	seen := make(map[string]bool, len(all))
	out := make([]string, 0, len(all))
	for _, r := range all {
		if r.Text == "" || seen[r.Text] {
			continue
		}
		seen[r.Text] = true
		out = append(out, r.Text)
	}
	return out
}
