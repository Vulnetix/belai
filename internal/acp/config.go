package acp

import (
	"context"
	"encoding/json"

	"github.com/vulnetix/belai/internal/jsonrpc"
	"github.com/vulnetix/belai/internal/sanitize"
)

// maxModelChoices bounds the model picker an editor is shown.
const maxModelChoices = 200

// ModelChoice is one provider and model an editor may pick for a session.
// Every choice comes from the host (the providers the user has credentials
// for and their catalogues), never from the editor.
type ModelChoice struct {
	Provider string
	Model    string
	Label    string
}

// value is the option id the editor sends back. It is only ever compared with
// the values Belai itself listed.
func (c ModelChoice) value() string { return c.Provider + "/" + c.Model }

// modelChoices lists what the editor may pick, always including the model the
// session already runs on. Nil when the host offers no picker.
func (s *Server) modelChoices(ss *acpSession) []ModelChoice {
	if s.opts.Models == nil {
		return nil
	}
	ss.mu.Lock()
	p, m := ss.provider, ss.model
	ss.mu.Unlock()
	out := s.opts.Models(ss.cwd, p, m)
	if len(out) > maxModelChoices {
		out = out[:maxModelChoices]
	}
	return out
}

// configOptions is the session's config state: the model picker (when the
// host offers one) then the mode. It is the complete state, as the protocol
// asks of every reply that changes an option.
func (s *Server) configOptions(ss *acpSession) []any {
	ss.mu.Lock()
	p, m, mode := ss.provider, ss.model, ss.mode
	ss.mu.Unlock()
	var out []any
	if choices := s.modelChoices(ss); len(choices) > 0 {
		opts := make([]any, len(choices))
		for i, c := range choices {
			opts[i] = map[string]any{"value": c.value(), "name": sanitize.Line(c.Label, 120)}
		}
		out = append(out, map[string]any{
			"id": "model", "name": "Model", "category": "model", "type": "select",
			"description":  "Provider and model for this session. A pick lasts for the session and is not saved to your settings.",
			"currentValue": ModelChoice{Provider: p, Model: m}.value(),
			"options":      opts,
		})
	}
	modeOpts := make([]any, len(modeList))
	for i, x := range modeList {
		modeOpts[i] = map[string]any{"value": x["id"], "name": x["name"], "description": x["description"]}
	}
	out = append(out, map[string]any{
		"id": "mode", "name": "Mode", "category": "mode", "type": "select",
		"currentValue": mode, "options": modeOpts,
	})
	if s.opts.Toggles != nil {
		ss.mu.Lock()
		t := ss.toggles
		ss.mu.Unlock()
		for _, o := range toggleOptions {
			out = append(out, map[string]any{
				"id": o.id, "name": o.name, "description": o.desc, "type": "select",
				"currentValue": t.value(o.id),
				"options": []any{
					map[string]any{"value": "on", "name": "On"},
					map[string]any{"value": "off", "name": "Off"},
				},
			})
		}
	}
	return out
}

// setConfigOption answers session/set_config_option for the model and mode.
// A model pick must be one of the values just listed; the session is rebuilt
// through the same builder that made it (trust check, posture, permission
// rules, sandbox and classifier all apply again) and keeps its conversation.
func (s *Server) setConfigOption(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"sessionId"`
		ConfigID  string `json:"configId"`
		Value     string `json:"value"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "invalid params: %v", err)
	}
	ss := s.lookup(p.SessionID)
	if ss == nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "unknown session %q", p.SessionID)
	}
	switch p.ConfigID {
	case "mode":
		if !knownMode(p.Value) {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "unknown mode %q", p.Value)
		}
		ss.mu.Lock()
		ss.mode = p.Value
		ss.mu.Unlock()
		s.update(ss, map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": p.Value})
	case "model":
		if err := s.switchModel(ctx, ss, p.Value); err != nil {
			return nil, err
		}
	case "guardrails", "ask", "caveman":
		if s.opts.Toggles == nil {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "unknown config option %q", p.ConfigID)
		}
		if err := s.switchToggle(ctx, ss, p.ConfigID, p.Value); err != nil {
			return nil, err
		}
	default:
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "unknown config option %q", p.ConfigID)
	}
	return map[string]any{"configOptions": s.configOptions(ss)}, nil
}

func (s *Server) switchModel(ctx context.Context, ss *acpSession, value string) error {
	var pick *ModelChoice
	for _, c := range s.modelChoices(ss) {
		if c.value() == value {
			c := c
			pick = &c
			break
		}
	}
	if pick == nil {
		return jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "unknown model %q", value)
	}
	ss.mu.Lock()
	t := ss.toggles
	ss.mu.Unlock()
	if err := s.rebuild(ctx, ss, pick.Provider, pick.Model, t); err != nil {
		return err
	}
	s.note(ss, "Model: %s", value)
	return nil
}

// switchToggle flips one of the on/off options. They are the user's own
// session-only choices, like the TUI's toggles, and change nothing in their
// settings; the session is rebuilt so the choice reaches every gate it feeds.
func (s *Server) switchToggle(ctx context.Context, ss *acpSession, id, value string) error {
	if value != "on" && value != "off" {
		return jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "%s must be on or off", id)
	}
	on := value == "on"
	ss.mu.Lock()
	t, p, m := ss.toggles, ss.provider, ss.model
	ss.mu.Unlock()
	switch id {
	case "guardrails":
		t.Guardrails = on
	case "ask":
		t.Ask = on
	case "caveman":
		t.Caveman = on
	}
	if err := s.rebuild(ctx, ss, p, m, t); err != nil {
		return err
	}
	s.note(ss, "%s: %s", id, value)
	return nil
}

// rebuild replaces the session's agent with one built for the given model and
// toggles, through the same builder that made the first (trust check, posture,
// permission rules, sandbox and classifier all apply again). The conversation
// is held here, so it carries over.
func (s *Server) rebuild(ctx context.Context, ss *acpSession, provider, model string, t Toggles) error {
	if s.opts.Switch == nil {
		return jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "this option is not available")
	}
	ss.mu.Lock()
	busy := ss.cancel != nil
	ss.mu.Unlock()
	if busy {
		return jsonrpc.Errorf(jsonrpc.CodeInvalidRequest, "a prompt is running in this session")
	}
	ag, err := s.opts.Switch(ctx, ss.cwd, ss.id, provider, model, t)
	if err != nil {
		return &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
	}
	ss.mu.Lock()
	ss.agent, ss.provider, ss.model, ss.toggles = ag, provider, model, t
	ss.mu.Unlock()
	return nil
}

// Toggles are the session's on/off switches. Each starts from the user's
// settings for the directory; an editor changes them for its session only.
type Toggles struct {
	// Guardrails is the posture gates (off is all-ignore, as in the TUI).
	Guardrails bool
	// Ask is the permission-ask gate; off resolves an ask to allow.
	Ask bool
	// Caveman is the terse caveman voice.
	Caveman bool
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

var toggleOptions = []struct{ id, name, desc string }{
	{"guardrails", "Guardrails", "The posture gates and the security classifier. Off is for this session only."},
	{"ask", "Ask before tools", "Permission asks come to you. Off allows a call the rules would ask about."},
	{"caveman", "Caveman voice", "Short words in the model's replies."},
}

func (t Toggles) value(id string) string {
	switch id {
	case "guardrails":
		return onOff(t.Guardrails)
	case "ask":
		return onOff(t.Ask)
	}
	return onOff(t.Caveman)
}

// String is a compact form of the switches, for logs and tests: g, a and c
// for guardrails, ask and caveman, each followed by + when on and - when off.
func (t Toggles) String() string {
	f := func(name string, on bool) string {
		if on {
			return name + "+"
		}
		return name + "-"
	}
	return f("g", t.Guardrails) + f("a", t.Ask) + f("c", t.Caveman)
}
