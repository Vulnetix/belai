package run

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/vulnetix/belai/internal/kiroauth"
	"github.com/vulnetix/belai/internal/kiromodels"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/transcript"
	"github.com/vulnetix/belai/internal/wire"
)

// kiroRefresher trades the stored Kiro login for an access token inside the
// request factory. It is the process-wide kiroauth.Shared, so the model list
// and the profile lookup reuse its tokens; tests substitute an
// httptest-backed refresher.
var kiroRefresher = kiroauth.Shared

// SetKiroRotationHook installs fn to persist a rotated Kiro login. The
// callers that own a credential store (the CLI and the TUI) install it; fn
// receives the login as stored and as it should now be stored.
func SetKiroRotationHook(fn func(oldLogin, newLogin string)) {
	kiroRefresher.SetOnRotate(fn)
}

// Kiro placeholders for a message the service requires to be non-empty. They
// are harness text, not model or user content.
const (
	kiroEmptyUser      = "(tool results)"
	kiroEmptyAssistant = "(tool calls)"
	kiroContinue       = "Continue."
)

// kiroMaxToolDescription caps an advertised tool description; the service
// rejects very long ones.
const kiroMaxToolDescription = 10000

// buildKiroRequest renders the conversation as a generateAssistantResponse
// body. Kiro has no system or tool role: the system prompt leads the first
// user message, tool results ride on the user message that follows the
// assistant's tool uses, and consecutive same-role turns merge so history
// strictly alternates. Tools are advertised on the current message only.
//
// images says whether the model accepts image input. Only the current
// message carries images: earlier turns' images are dropped, because the
// service rejects a follow-up request that replays them. On a model without
// image input they are dropped with a harness note in their place.
func buildKiroRequest(model, system string, turns []Turn, tools []wire.OpenAITool, profileARN string, images bool) wire.KiroRequest {
	var entries []wire.KiroHistoryEntry
	var user *wire.KiroUserInputMessage
	var text []string

	flushUser := func() {
		if user == nil {
			return
		}
		user.Content = strings.Join(text, "\n\n")
		if user.Content == "" {
			// A message carrying only tool results still needs text.
			user.Content = kiroEmptyUser
		}
		entries = append(entries, wire.KiroHistoryEntry{UserInputMessage: user})
		user, text = nil, nil
	}
	openUser := func() {
		if user == nil {
			user = &wire.KiroUserInputMessage{ModelID: model, Origin: wire.KiroOrigin}
		}
	}

	for _, t := range turns {
		switch t.Role {
		case "assistant":
			if t.Content == "" && len(t.ToolCalls) == 0 {
				continue
			}
			if user == nil && len(entries) == 0 {
				// History must open with a user message.
				openUser()
				text = append(text, kiroContinue)
			}
			flushUser()
			var msg *wire.KiroAssistantResponseMessage
			if n := len(entries); n > 0 && entries[n-1].AssistantResponseMessage != nil {
				msg = entries[n-1].AssistantResponseMessage
				if t.Content != "" {
					if msg.Content == kiroEmptyAssistant {
						msg.Content = t.Content
					} else {
						msg.Content += "\n\n" + t.Content
					}
				}
			} else {
				msg = &wire.KiroAssistantResponseMessage{Content: t.Content}
				entries = append(entries, wire.KiroHistoryEntry{AssistantResponseMessage: msg})
			}
			for _, tc := range t.ToolCalls {
				msg.ToolUses = append(msg.ToolUses, wire.KiroToolUse{ToolUseID: tc.ID, Name: tc.Name, Input: kiroToolInput(tc)})
			}
			if msg.Content == "" {
				msg.Content = kiroEmptyAssistant
			}
		case "tool":
			openUser()
			if user.UserInputMessageContext == nil {
				user.UserInputMessageContext = &wire.KiroUserInputMessageCtx{}
			}
			// An image a tool returned (a screenshot) rides on the same user
			// message; history images are cleared below and a text-only model
			// gets the omitted note, exactly like an attached image. One the
			// service would refuse is named in the result, not dropped silently.
			want := imageAttachments(t.Attachments)
			got := kiroImages(want)
			user.Images = append(user.Images, got...)
			result := t.Content
			if len(got) < len(want) {
				result += "\n" + kiroImageRefused
			}
			user.UserInputMessageContext.ToolResults = append(user.UserInputMessageContext.ToolResults, wire.KiroToolResult{
				ToolUseID: t.ToolCallID,
				Content:   []wire.KiroTextContent{{Text: result}},
				Status:    "success",
			})
		default:
			openUser()
			if t.Content != "" {
				text = append(text, t.Content)
			}
			want := imageAttachments(t.Attachments)
			got := kiroImages(want)
			user.Images = append(user.Images, got...)
			if len(got) < len(want) {
				text = append(text, kiroImageRefused)
			}
		}
	}
	if user == nil {
		// The newest message must be the user's.
		openUser()
		text = append(text, kiroContinue)
	}
	flushUser()

	// The sealed system prompt leads the first user message.
	if system != "" {
		first := entries[0].UserInputMessage
		first.Content = system + "\n\n" + first.Content
	}

	current := entries[len(entries)-1]
	history := entries[:len(entries)-1]
	for _, e := range history {
		if e.UserInputMessage != nil {
			e.UserInputMessage.Images = nil
		}
	}
	if cur := current.UserInputMessage; len(cur.Images) > 0 && !images {
		cur.Images = nil
		cur.Content += "\n\n" + kiroImageOmitted
	}
	if specs := kiroTools(tools); len(specs) > 0 {
		if current.UserInputMessage.UserInputMessageContext == nil {
			current.UserInputMessage.UserInputMessageContext = &wire.KiroUserInputMessageCtx{}
		}
		current.UserInputMessage.UserInputMessageContext.Tools = specs
	}
	return wire.KiroRequest{
		ConversationState: wire.KiroConversationState{
			ChatTriggerType: "MANUAL",
			ConversationID:  kiroConversationID(entries[0].UserInputMessage.Content),
			CurrentMessage:  current,
			History:         history,
		},
		ProfileArn: profileARN,
	}
}

// kiroImageOmitted replaces images a text-only model cannot take. It is
// harness text.
const kiroImageOmitted = "[an attached image was not sent: this model does not accept images]"

// kiroImageRefused names an image the service would not take (a format it does
// not list, or over its size ceiling). It is harness text.
const kiroImageRefused = "[harness: an image was not sent: it is over the service's 3.75 MB limit or in a format it does not take]"

// kiroMaxImageBytes is the service's per-image ceiling before base64.
const kiroMaxImageBytes = 3_750_000

// kiroImages converts image attachments to Kiro images, dropping any in an
// unsupported format or over the size ceiling.
func kiroImages(atts []Attachment) []wire.KiroImage {
	var out []wire.KiroImage
	for _, a := range atts {
		var format string
		switch strings.ToLower(strings.TrimSpace(a.MediaType)) {
		case "image/png":
			format = "png"
		case "image/jpeg", "image/jpg":
			format = "jpeg"
		case "image/gif":
			format = "gif"
		case "image/webp":
			format = "webp"
		default:
			continue
		}
		if len(a.Data) == 0 || len(a.Data) > kiroMaxImageBytes {
			continue
		}
		out = append(out, wire.KiroImage{Format: format, Source: wire.KiroImageSource{Bytes: a.Data}})
	}
	return out
}

// kiroModelInfo returns the live catalogue entry for model at base, fetching
// the catalogue with token when the process holds none younger than
// kiromodels.TTL. A failed fetch is remembered as empty, so it is not
// retried on every request and the request falls back to the plain shape.
func kiroModelInfo(ctx context.Context, base, model, token, profileARN string) kiromodels.Info {
	info, found, fresh := kiromodels.Lookup(base, model)
	if fresh {
		return info
	}
	infos, err := kiromodels.Fetch(ctx, kiroModelsClient, base, token, profileARN)
	if err != nil {
		infos = nil
	}
	kiromodels.Remember(base, infos)
	info, found, _ = kiromodels.Lookup(base, model)
	if !found {
		return kiromodels.Info{}
	}
	return info
}

// kiroModelsClient fetches the model catalogue; nil means kiromodels' own
// short-timeout client. Tests substitute an httptest client.
var kiroModelsClient *http.Client

// kiroConversationID derives a stable UUID-shaped id from the conversation's
// opening message, so every request of one conversation carries the same id
// without the harness storing one.
func kiroConversationID(opening string) string {
	sum := sha256.Sum256([]byte(opening))
	h := hex.EncodeToString(sum[:16])
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-a" + h[17:20] + "-" + h[20:32]
}

// kiroToolInput returns a tool call's arguments as an object.
func kiroToolInput(tc rolemanager.ToolCall) map[string]any {
	if tc.RawArgs != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(tc.RawArgs), &m); err == nil && m != nil {
			return m
		}
	}
	if tc.Args != nil {
		return tc.Args
	}
	return map[string]any{}
}

// kiroTools converts the OpenAI tool definitions to Kiro tool specs.
func kiroTools(tools []wire.OpenAITool) []wire.KiroTool {
	out := make([]wire.KiroTool, 0, len(tools))
	for _, t := range tools {
		desc := t.Function.Description
		if desc == "" {
			desc = t.Function.Name
		}
		if r := []rune(desc); len(r) > kiroMaxToolDescription {
			desc = string(r[:kiroMaxToolDescription])
		}
		schema := t.Function.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, wire.KiroTool{ToolSpecification: wire.KiroToolSpecification{
			Name:        t.Function.Name,
			Description: desc,
			InputSchema: wire.KiroInputSchema{JSON: schema},
		}})
	}
	return out
}

// kiroStreamState tracks tool uses across event frames. Kiro keys fragments
// by toolUseId; the accumulator keys by index.
type kiroStreamState struct {
	index map[string]int
	done  map[string]bool
	next  int
	// usage is the latest metadataEvent's accounting; ctxPct the latest
	// contextUsageEvent's percentage, the fallback when no metadata came.
	usage  *transcript.Usage
	ctxPct float64
}

// finalUsage is the turn's usage: the service's own token counts when it sent
// them, else prompt tokens estimated from the context-window percentage and
// the model's input limit, else nil.
func (st *kiroStreamState) finalUsage(maxInput int) *transcript.Usage {
	if st.usage != nil {
		u := *st.usage
		return &u
	}
	if st.ctxPct > 0 && maxInput > 0 {
		pct := min(st.ctxPct, 100)
		return &transcript.Usage{PromptTokens: int(pct / 100 * float64(maxInput))}
	}
	return nil
}

func newKiroStreamState() *kiroStreamState {
	return &kiroStreamState{index: map[string]int{}, done: map[string]bool{}}
}

// kiroMaxErrorText caps the service's error message in an error.
const kiroMaxErrorText = 300

// decodeKiroFrame decodes one event-stream frame into a delta.
func decodeKiroFrame(msg wire.EventMessage, st *kiroStreamState, acc *toolAccumulator) (streamDelta, error) {
	switch msg.MessageType() {
	case "exception", "error":
		return streamDelta{}, kiroFrameError(msg)
	}
	switch msg.EventType() {
	case "assistantResponseEvent":
		var ev wire.KiroAssistantResponseEvent
		if err := json.Unmarshal(msg.Payload, &ev); err != nil {
			return streamDelta{}, fmt.Errorf("decode kiro assistantResponseEvent: %w", err)
		}
		return streamDelta{text: ev.Content}, nil
	case "toolUseEvent":
		var ev wire.KiroToolUseEvent
		if err := json.Unmarshal(msg.Payload, &ev); err != nil {
			return streamDelta{}, fmt.Errorf("decode kiro toolUseEvent: %w", err)
		}
		if ev.ToolUseID == "" || st.done[ev.ToolUseID] {
			// A repeat of a finished tool use is not a second call.
			return streamDelta{}, nil
		}
		idx, seen := st.index[ev.ToolUseID]
		if !seen {
			idx = st.next
			st.next++
			st.index[ev.ToolUseID] = idx
			acc.open(idx, ev.ToolUseID, ev.Name, "tool_use")
		}
		frag := kiroInputFragment(ev.Input)
		if frag != "" {
			acc.appendArgs(idx, frag)
		}
		d := streamDelta{toolDelta: &ToolCallDelta{Index: idx, ID: ev.ToolUseID, Name: ev.Name, Args: frag}}
		if ev.Stop {
			st.done[ev.ToolUseID] = true
			call, err := acc.complete(idx)
			if err != nil {
				return streamDelta{}, err
			}
			d.completed = []rolemanager.ToolCall{call}
			d.stopReason = "tool_use"
		}
		return d, nil
	case "metadataEvent":
		var ev wire.KiroMetadataEvent
		if err := json.Unmarshal(msg.Payload, &ev); err == nil && ev.TokenUsage != nil {
			tu := ev.TokenUsage
			input := tu.UncachedInputTokens
			if input == 0 {
				input = tu.InputTokens
			}
			u := transcript.Usage{
				PromptTokens:     max(input, 0) + max(tu.CacheReadInputTokens, 0) + max(tu.CacheWriteInputTokens, 0),
				CompletionTokens: max(tu.OutputTokens, 0),
				TotalTokens:      max(tu.TotalTokens, 0),
				CacheReadTokens:  max(tu.CacheReadInputTokens, 0),
				CacheWriteTokens: max(tu.CacheWriteInputTokens, 0),
			}
			if u.PromptTokens+u.CompletionTokens+u.TotalTokens > 0 {
				// A later event replaces an earlier one: the counters are
				// the response's running totals, not increments.
				st.usage = &u
			}
		}
		return streamDelta{}, nil
	case "contextUsageEvent":
		var ev wire.KiroContextUsageEvent
		if err := json.Unmarshal(msg.Payload, &ev); err == nil && ev.ContextUsagePercentage > 0 {
			st.ctxPct = ev.ContextUsagePercentage
		}
		return streamDelta{}, nil
	}
	// meteringEvent, codeReferenceEvent and the rest carry nothing the turn
	// records.
	return streamDelta{}, nil
}

// kiroInputFragment returns a tool-input fragment as JSON text. Kiro streams
// the input as string fragments; a whole object is marshalled.
func kiroInputFragment(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// kiroFrameError turns an exception frame into an error. The service's text
// is flattened and capped; it names what failed and never carries a token.
func kiroFrameError(msg wire.EventMessage) error {
	kind := msg.Headers[":exception-type"]
	if kind == "" {
		kind = msg.Headers[":error-code"]
	}
	var p wire.KiroExceptionPayload
	_ = json.Unmarshal(msg.Payload, &p)
	text := p.Message
	if text == "" {
		text = p.MessageU
	}
	if text == "" {
		text = msg.Headers[":error-message"]
	}
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > kiroMaxErrorText {
		text = string(r[:kiroMaxErrorText]) + "…"
	}
	if kind == "" {
		kind = "error"
	}
	return fmt.Errorf("kiro %s: %s", safeIdent(kind), text)
}

func safeIdent(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
		}
		if b.Len() >= 64 {
			break
		}
	}
	return b.String()
}

// parseKiro decodes a whole event-stream body into an Assistant, for the
// blocking send path.
func parseKiro(body []byte, status int, maxInput int) (Assistant, error) {
	r := wire.NewEventStreamReader(bytes.NewReader(body))
	st := newKiroStreamState()
	acc := newToolAccumulator()
	var text strings.Builder
	var calls []rolemanager.ToolCall
	var stopReason string
	for {
		msg, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Assistant{}, fmt.Errorf("decode kiro response (%d): %w", status, err)
		}
		d, err := decodeKiroFrame(msg, st, acc)
		if err != nil {
			return Assistant{}, err
		}
		text.WriteString(d.text)
		calls = append(calls, d.completed...)
		if d.stopReason != "" {
			stopReason = d.stopReason
		}
	}
	if rest, err := acc.completeAll(); err == nil {
		calls = append(calls, rest...)
	}
	for i := range calls {
		if calls[i].RawArgs != "" && calls[i].Args == nil {
			if args, err := parseToolCallArgs(json.RawMessage(calls[i].RawArgs)); err == nil {
				calls[i].Args = args
			}
		}
	}
	return Assistant{Text: text.String(), ToolCalls: calls, Stop: len(calls) == 0, StopReason: stopReason, Usage: st.finalUsage(maxInput)}, nil
}

// kiroMaxInput is the input-token limit of the dialect's model from the live
// catalogue, or 0 when it is not known.
func (d dialect) kiroMaxInput() int {
	if info, found, _ := kiromodels.Lookup(d.kiroBase, d.kiroModel); found {
		return info.MaxInput
	}
	return 0
}
