# Compaction settings

**Status:** new.

A long session compacts its context at a pass boundary: first by pruning tool
calls and results the work ahead does not need ([Compaction
prune](jev-jobs.md#compaction-prune)), then, if that falls short, by replacing the
conversation with a summary. This page covers the numbers that steer that, a
circuit breaker for a summary that keeps failing, a focus for `/compact`, and the
state a session keeps across a resume. The harness decides all of it from
counts. No model is asked whether to compact, to pause or to resume.

## Settings

All six keys live in the `compaction` block. A value outside its range is
refused when the settings load, and the accessor falls back to the default for a
value that somehow gets past that.

| Key | Default | Range | Meaning |
| --- | --- | --- | --- |
| `compaction.threshold_pct` | 70 | 30 to 95 | Compact at a pass boundary when the estimated context passes this share of the model's window. |
| `compaction.keep_recent_turns` | 6 | 2 to 40 | The newest turns a prune never touches. |
| `compaction.keep_result_chars` | 300 | 0 to 2000 | How much of a tool result a prune keeps when it keeps only the call. |
| `compaction.keep_tool_results` | 2 | 0 to 20 | The newest tool results kept when a context overflow forces the clearing fallback. |
| `compaction.breaker_failures` | 3 | 1 to 20 | Summary failures in a row that pause the summary step. |
| `compaction.breaker_cooldown_passes` | 4 | 1 to 20 | Pass boundaries the summary step stays paused. |

The window is the one compaction always used: your `context_windows` override,
then the provider catalogue, then the built-in registry, then an assumed 128000
tokens.

### Which layer may set what

Your own layers (the global file, your preference file for a project, the
environment and flags) set any key to any value in range. A project settings
file, which comes with a cloned repository, can only move a value toward
compacting sooner or keeping more:

| Key | A project value applies only when it is |
| --- | --- |
| `threshold_pct` | lower than the value in force |
| `breaker_cooldown_passes` | lower than the value in force |
| `keep_recent_turns` | higher than the value in force |
| `keep_result_chars` | higher than the value in force |
| `keep_tool_results` | higher than the value in force |
| `breaker_failures` | higher than the value in force |

"In force" means what your layers resolved to, or the default when you set
nothing. A project value that is out of range, or that goes the other way, is
dropped and Resolve adds a note naming the keys (`project compaction.threshold_pct
ignored (a project may only compact sooner or keep more)`); it never stops Belai
from starting. The merge is
`mergeCompaction` in `internal/config/compaction.go`, called from both
`Settings.Override` and `Resolve`.

The always-kept tools of a prune (state and instruction tools, mutating calls,
failures) are a constant, not a setting.

## The summary circuit breaker

A summary is a long classifier call plus an admission call. When the classifier
cannot produce a valid, admitted one, retrying at every pass boundary only adds
latency and cost while the context stays over the limit.

- A summary **fails** when the classifier errors, the reply lacks the required
  sections (`ValidateServedSummary`), or the Role Manager does not admit it
  (an error, or anything but proceed). A turn that was cancelled is not a
  failure. A prune that falls short is not a failure either.
- After `breaker_failures` failures in a row the breaker opens. The summary step
  is then skipped for `breaker_cooldown_passes` pass boundaries. One warning
  line is shown when it opens, and the trace records `compaction_breaker`.
- **Pruning keeps running** while the breaker is open, because a prune keeps
  every word it leaves and needs no admission.
- After the cooldown the next boundary tries again. One more failure opens the
  breaker again at once. One success closes it and clears the count.
- A **forced** compaction, run once after the provider has rejected a request as
  over its window, ignores the breaker. Its outcome still counts.

Goal, plan and agent turns share one breaker per session. It is held in memory
by the session and is also saved, see below.

## `/compact`

| Form | What it does |
| --- | --- |
| `/compact` | Summarise the session into a new one. |
| `/compact <focus>` | The same, with a one-line steer for this summary. |
| `/compact status` | Show the threshold, the window, the session's estimate, the keep counts and the breaker. Read-only; no model is called. |

The focus is cleaned with `sanitize.Line`, capped at 500 characters
(`rolemanager.MaxCompactionFocusRunes`) and appended after the serialized
conversation as a labelled line in the user message. It never enters the system
text, has no harness authority, and does not change the section contract or the
validation of the summary. It applies to that one summary: automatic compaction
never reuses it. The session records only its length (`focus_len`), never the
text. The word `status` is the subcommand, so a focus cannot be exactly that
word.

A manual summary is admitted before it is stored, as the automatic one is. The
classifier call and the admission call both run off the screen's update loop.
If the summary is refused, the admission errors, or the verdict is anything but
proceed, `compact failed: ...` is shown with the reason and the session is left
exactly as it was. With guardrails off, admission is skipped by the same
posture rule as everywhere.

A successful manual compaction forks the session as before. Its first entry
records `trigger` (`manual`, or `resume` when you accepted the offer after a
resume) and `focus_len`.

## What a session keeps

The breaker's state is written to the session file as a `compaction_state`
entry whenever it changes: `fails`, `cooldown_left`, and the last `tokens` and
`window` the harness measured. It is numbers only.

- A resumed session loads the latest entry, so a paused summary step stays
  paused instead of retrying a failing classifier at once.
- A `/compact` fork is a new file, so the fork gets its own entry. A summary that
  was just admitted proves the step works, so the fork starts with the count
  and cooldown cleared.
- `/clear` and a new session start with a closed breaker.
- The session file is not trusted. On load every field is clamped (`fails` to
  1000, `cooldown_left` to 20, the others to a 32 bit count), and a value that is
  not a number reads as zero.

## Surfaces

| Surface | Thresholds and keep counts | Breaker | `/compact` focus and status |
| --- | --- | --- | --- |
| TUI | yes | yes, saved with the session | yes |
| `belai acp`, headless runs, `belai rc` sessions, fleet workers | yes | yes, in memory for the run | no slash command |

## Where it lives

- `internal/config/compaction.go`: `CompactionSettings`, the accessors,
  `ValidateCompaction` and `mergeCompaction`.
- `internal/agent/compactbreaker.go`: the breaker and its events.
- `internal/agent/passloop.go`: `compactBoundary`, `compactTurns`,
  `recoverOverflow` and `CompactWindow`. `internal/agent/prune.go` reads the keep
  counts.
- `internal/rolemanager/compact.go`: `BuildCompactionPayload` and
  `CompactionFocusLine`.
- `internal/session/compaction_state.go`: the `compaction_state` entry.
- `internal/tui/app.go`: `compactCommand`, `compactWithFocus`, `compactStatus`.
