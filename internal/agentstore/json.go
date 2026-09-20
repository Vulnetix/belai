package agentstore

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"time"
)

// vscodeAdapter parses VS Code Copilot Chat chatSessions JSON files. Each
// request is a turn: message is the user prompt, response is the assistant.
type vscodeAdapter struct{}

type vscodeRequest struct {
	RequestID string          `json:"requestId"`
	Message   vscodeMessage   `json:"message"`
	Response  json.RawMessage `json:"response"`
	Timestamp string          `json:"timestamp"`
}

type vscodeMessage struct {
	Text  string `json:"text"`
	Parts []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"parts"`
}

func (vscodeAdapter) Sources(path string) ([]Source, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	src := Source{Agent: "copilot-vscode", Format: FormatJSONVSCode, Path: path, ModTime: fi.ModTime()}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(data, &doc) == nil {
		src.SessionID = doc.SessionID
	}
	return []Source{src}, nil
}

func (vscodeAdapter) Turns(src Source, from, to int) ([]Turn, error) {
	data, err := os.ReadFile(src.Path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Requests []vscodeRequest `json:"requests"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	var turns []Turn
	idx := 0
	for _, r := range doc.Requests {
		if from > 0 && idx < from {
			idx++
			continue
		}
		if to > 0 && idx >= to {
			break
		}
		if txt := vscodeMessageText(r.Message); txt != "" {
			turns = append(turns, Turn{Index: idx, Role: "user", Text: txt, At: vscodeTime(r.Timestamp)})
			idx++
		}
		if txt := vscodeResponseText(r.Response); txt != "" {
			turns = append(turns, Turn{Index: idx, Role: "assistant", Text: txt, At: vscodeTime(r.Timestamp)})
			idx++
		}
	}
	return turns, nil
}

func (vscodeAdapter) Scan(ctx context.Context, src Source, re *regexp.Regexp, caps Caps) ([]Hit, error) {
	turns, err := vscodeAdapter{}.Turns(src, 0, 0)
	if err != nil {
		return nil, err
	}
	hits, _, err := scanTurns(ctx, src, re, caps, func(fn func(Turn) bool) error {
		for _, t := range turns {
			if !fn(t) {
				return errStopScan
			}
		}
		return nil
	})
	return hits, err
}

func vscodeMessageText(m vscodeMessage) string {
	if strings.TrimSpace(m.Text) != "" {
		return m.Text
	}
	var parts []string
	for _, p := range m.Parts {
		if p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func vscodeResponseText(raw json.RawMessage) string {
	var parts []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var out []string
	for _, p := range parts {
		if p.Text != "" {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n")
}

func vscodeTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return parseTime(s)
}

// genericAdapter tolerates stores whose schema is not guaranteed. It handles a
// JSON object with a requests[] array (the VS Code shape) and otherwise
// degrades to no sessions.
type genericAdapter struct{}

func (genericAdapter) Sources(path string) ([]Source, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	// Reuse the VS Code shape when present; otherwise report the file as one
	// opaque source so a search can attempt to scan it.
	return []Source{{Format: FormatJSONGeneric, Path: path, ModTime: fi.ModTime()}}, nil
}

func (genericAdapter) Turns(src Source, from, to int) ([]Turn, error) {
	// Try the VS Code shape.
	if turns, err := (vscodeAdapter{}).Turns(src, from, to); err == nil && len(turns) > 0 {
		return turns, nil
	}
	return nil, nil
}

func (genericAdapter) Scan(ctx context.Context, src Source, re *regexp.Regexp, caps Caps) ([]Hit, error) {
	if turns, err := (vscodeAdapter{}).Turns(src, 0, 0); err == nil && len(turns) > 0 {
		hits, _, err := scanTurns(ctx, src, re, caps, func(fn func(Turn) bool) error {
			for _, t := range turns {
				if !fn(t) {
					return errStopScan
				}
			}
			return nil
		})
		return hits, err
	}
	return nil, nil
}
