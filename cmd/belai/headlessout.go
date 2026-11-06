package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/session"
)

// headlessOptions is what a noninteractive -prompt run adds to the original
// contract (docs/headless.md): how it reports, a schema its final answer must
// satisfy, and a stored session it continues.
type headlessOptions struct {
	Format   headless.Format
	Schema   *headless.Schema
	EventCap int
	// ResumeKey and ResumeID name the session -resume or -continue resolved;
	// ResumeID is empty for a fresh run.
	ResumeKey session.Key
	ResumeID  string
}

// agentOutcome is a headless agent run's result with the schema verdict.
type agentOutcome struct {
	Res        run.Result
	Structured json.RawMessage
	SchemaErr  *headless.SchemaError
}

// usageError is a mistake in how belai was invoked: exit 2.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// resolveOutputFormat folds -output-format over headless.output_format.
func resolveOutputFormat(flagValue string, s config.Settings) (headless.Format, error) {
	if flagValue != "" {
		return headless.ParseFormat(flagValue)
	}
	return headless.ParseFormat(s.HeadlessOutputFormat())
}

// resolvePrompt returns the prompt of a noninteractive run. "-" reads it from
// standard input, and so does an empty -prompt when standard input is a pipe
// or a file; any other value is the prompt. A stdin read is bounded by
// headless.max_stdin_bytes. read reports whether the prompt came from stdin.
func resolvePrompt(flagValue string, stdin *os.File, eligible bool, limit int) (prompt string, fromStdin bool, err error) {
	switch {
	case flagValue == "-":
	case flagValue == "" && eligible && headless.StdinIsPrompt(stdin):
	default:
		return flagValue, false, nil
	}
	text, err := headless.ReadPrompt(stdin, limit)
	if err != nil {
		return "", true, &usageError{err.Error()}
	}
	return text, true, nil
}

// checkHeadlessCombo refuses the flag combinations the structured contract
// cannot honour, before anything runs.
func checkHeadlessCombo(ho headlessOptions, detectMode, enableTools, planMode bool, forceMode modes.Mode) error {
	if ho.Schema == nil && ho.ResumeID == "" {
		return nil
	}
	switch {
	case detectMode:
		return &usageError{"-json-schema and -resume/-continue run the agent: they cannot be combined with -detect-mode"}
	case !enableTools:
		return &usageError{"-json-schema and -resume/-continue run the agent: they cannot be combined with -tools=false"}
	case ho.Schema != nil && (planMode || forceMode == modes.ModePlan):
		return &usageError{"-json-schema cannot be combined with plan mode: a plan run's deliverable is the plan file"}
	}
	return nil
}

// runWithSchema runs one agent turn and, when a schema is set, checks the
// final answer. A failing answer gets one repair turn whose text is composed
// by the harness from a code and a pointer, never from the reply. The model
// is not asked whether its output is valid.
func runWithSchema(ctx context.Context, sess *agent.Session, history []run.Turn, prompt string, forceMode modes.Mode, planMode bool, schema *headless.Schema, observe func(agent.Event)) (agentOutcome, error) {
	in := agent.TurnInput{Prompt: prompt, ForceMode: forceMode}
	if schema != nil {
		in.Directive = schema.Directive()
	}
	res, err := sess.RunInputObserved(ctx, history, in, observe)
	out := agentOutcome{Res: res}
	if err != nil || schema == nil {
		return out, err
	}
	structured, serr := schema.Validate(res.Reply)
	if serr == nil {
		out.Structured = structured
		return out, nil
	}
	repairMode := modes.ModeAgent
	if forceMode == modes.ModeCode {
		repairMode = modes.ModeCode
	}
	next := append(append([]run.Turn(nil), history...),
		run.Turn{Role: "user", Content: prompt},
		run.Turn{Role: "assistant", Content: res.Reply})
	res2, err := sess.RunInputObserved(ctx, next, agent.TurnInput{Prompt: schema.RepairMessage(serr), ForceMode: repairMode}, observe)
	out.Res = res2
	if err != nil {
		return out, err
	}
	structured, serr = schema.Validate(res2.Reply)
	if serr != nil {
		out.SchemaErr = serr
		return out, nil
	}
	out.Structured = structured
	return out, nil
}

// buildFinal composes the result of a run. The subtype follows a fixed order:
// an error, then a reached run cap, then a schema failure, then a tool call
// that needed an approval nobody could give. The last is reported only in a
// structured format, because a plain -prompt run has always exited 0 then.
func buildFinal(ho headlessOptions, sessionID string, planMode bool, forceMode modes.Mode, o agentOutcome, runErr error, sum *run.UsageSummary, took time.Duration) headless.Final {
	f := headless.Final{
		Type:         headless.TypeResult,
		SessionID:    sessionID,
		Reply:        o.Res.Reply,
		Structured:   o.Structured,
		SchemaError:  o.SchemaErr,
		Passes:       o.Res.Passes,
		StopReason:   string(o.Res.StopReason),
		AsksWithheld: o.Res.AsksWithheld,
		DurationMS:   took.Milliseconds(),
		Subtype:      headless.SubtypeSuccess,
	}
	mode := forceMode
	if mode == "" {
		mode = o.Res.ModeDecision.Mode
	}
	if planMode && mode == "" {
		mode = modes.ModePlan
	}
	f.Mode = string(mode)
	if sum != nil {
		calls, tokens, prompt, completion, cacheRead, cacheWrite := sum.Totals()
		f.Usage = &headless.Usage{Calls: calls, TotalTokens: tokens, PromptTokens: prompt, CompletionTokens: completion,
			CacheReadTokens: cacheRead, CacheWriteTokens: cacheWrite}
	}
	switch {
	case runErr != nil:
		f.Subtype, f.IsError, f.Error = headless.SubtypeError, true, runErr.Error()
	case o.Res.StopReason == run.StopRunCap:
		f.Subtype, f.IsError, f.Error = headless.SubtypeRunCap, true, errRunCapReached.Error()
	case o.SchemaErr != nil:
		f.Subtype, f.IsError, f.Error = headless.SubtypeSchemaFailure, true, o.SchemaErr.Error()
	case ho.Format.Structured() && len(o.Res.AsksWithheld) > 0:
		f.Subtype, f.IsError = headless.SubtypeDenied, true
		f.Error = "tool calls needed a permission approval that nobody could give"
	}
	if ho.Format == headless.FormatText && o.Structured != nil {
		// A text run with a schema prints the validated value, which is what
		// the caller asked for, instead of whatever surrounded it.
		f.Reply = string(o.Structured)
	}
	return f
}

// newEmitter builds the run's emitter on stdout. -verbose adds a tool call's
// arguments to tool_start.
func newEmitter(ho headlessOptions, verbose bool, w io.Writer) *headless.Emitter {
	return headless.NewEmitter(ho.Format, w, headless.EmitterOptions{TextCap: ho.EventCap, Args: verbose})
}

// reportHeadless writes the result and returns the process exit code and the
// stderr message, if any. A run that failed before any result existed still
// gets one in a structured format.
func reportHeadless(em *headless.Emitter, f headless.Final) (int, error) {
	werr := em.Result(f)
	code := headless.ExitCode(f.Subtype)
	switch {
	case f.Subtype == headless.SubtypeRunCap:
		return code, errRunCapReached
	case f.Subtype == headless.SubtypeError:
		return code, errors.New(f.Error)
	case f.Subtype == headless.SubtypeSchemaFailure:
		return code, fmt.Errorf("the final answer did not satisfy -json-schema (%s)", f.SchemaError.Error())
	case f.Subtype == headless.SubtypeDenied:
		return code, fmt.Errorf("%d tool call(s) needed a permission approval that nobody could give: %v", len(f.AsksWithheld), f.AsksWithheld)
	case werr != nil:
		return headless.ExitError, fmt.Errorf("write result: %w", werr)
	}
	return code, nil
}

// withEvents chains the emitter onto an observer.
func withEvents(base func(agent.Event), em *headless.Emitter) func(agent.Event) {
	if em == nil || em.Format() != headless.FormatStreamJSON {
		return base
	}
	return func(e agent.Event) {
		base(e)
		em.Observe(e)
	}
}
