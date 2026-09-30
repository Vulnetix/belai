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
	return append(out, map[string]any{
		"id": "mode", "name": "Mode", "category": "mode", "type": "select",
		"currentValue": mode, "options": modeOpts,
	})
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
	default:
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "unknown config option %q", p.ConfigID)
	}
	return map[string]any{"configOptions": s.configOptions(ss)}, nil
}

func (s *Server) switchModel(ctx context.Context, ss *acpSession, value string) error {
	if s.opts.Switch == nil {
		return jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "model choice is not available")
	}
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
	busy := ss.cancel != nil
	ss.mu.Unlock()
	if busy {
		return jsonrpc.Errorf(jsonrpc.CodeInvalidRequest, "a prompt is running in this session")
	}
	ag, err := s.opts.Switch(ctx, ss.cwd, ss.id, pick.Provider, pick.Model)
	if err != nil {
		return &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
	}
	ss.mu.Lock()
	ss.agent, ss.provider, ss.model = ag, pick.Provider, pick.Model
	ss.mu.Unlock()
	s.note(ss, "Model: %s", value)
	return nil
}
