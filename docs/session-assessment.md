# Signet session assessment

This document records what the saved sessions under
`~/.vulnetix/signet/sessions` showed about Signet's orchestration, prompts and
UX. It also records the changes made in response. The goal is to make the
time to the first file mutation as short as possible, using deterministic
context and parallelism rather than asking the model to rediscover facts.

## Corpus

The corpus is 174 JSONL files from two projects, recorded 2026-09-22 to
2026-09-24. None of the files contains a successful `Write` or `Edit` tool
row.

## Findings and root causes

| What the sessions showed | Root cause |
|---|---|
| Agent mode reported "no write tool exposed". `go test` was refused as "not in read-only allowlist" and pipes as "shell metacharacters". | The project `.vulnetix/settings.json` had `read_only: true`, and the TUI built its one registry with `ReadOnly()`, which applied to **every** mode, goal included. The `signet:debug` profile prose also told the model "Bash (read-only)". |
| An approved plan ran as a single agent pass and then stopped. | `modes.RoutePlanOption` routed Execute to agent mode: one bounded pass plus continuations, `read_only` applied, and no write ledger, directives or evaluator. |
| Goal sessions contained only `user` and `goal_state` rows. | Persistence treated an assistant bubble as settled only when exactly N tool rows followed it contiguously. In goal mode, reasoning, system and role-manager rows land in between, so the bubble never settled. The next `echoUser` then moved the cursor past it, and the whole turn was lost. |
| `goal_state.tokensUsed` stayed at 0. | A pass kept only the last provider call's usage, and dropped it entirely on budget-exhausted passes (the normal case). Only `openai` asked for `stream_options.include_usage`. |
| "continue goal" started a new goal with the objective "continue goal". | Nothing resumed a goal in progress: every goal turn got `goals.NewGoalState` and a freshly drafted contract. |
| Contracts required `make test` (19 times) in a repository with no Makefile. Drafting took up to 10,017 s. | The contract drafter could invent commands, and `DraftGoalContract` ran serially before the first pass with no time limit. |
| `AGENTS.md`, `justfile`, `catalog.go` and `README.md` were withheld as prompt injection, and classification failed with `input sequence too long: 513 > 512`. | `mlclassify.sliceText` used the tokenizer's **rune** offsets as **byte** offsets. On non-ASCII text every window was misaligned, some went over 512 tokens, and the end of the file was never classified. Each false positive was then cached permanently. |
| The model re-read the same file about 12 times with growing offset and limit values, and read past EOF. | Read output had no size, EOF or next-offset information, and reading past EOF returned an error. |
| A withheld result was retried again and again. | Nothing told the model that a classifier verdict is a decision about the content, not a mistake in its arguments. The two-round repair directive said the opposite ("re-issue the calls with corrected arguments"). |
| Agent turns ended at a full context window. | Only the goal and plan loops compacted. Agent continuations never did, and edit nudges existed only in goal mode. |
| "Max subagents is 3 and cannot be changed." | The 3 was fixed in v0.49.x (`DefaultMaxAgents = 15`, and the resolver now applies `resilience`). Two problems remained. `/settings` always reopened in project scope, so a global edit was silently overridden by the project's `max_agents`. The CLI also never connected an agent pool. |

## Changes

- **Tool surface depends on mode.**
  - The registry is always built in full. `read_only` only narrows **agent-mode** turns, through `Registry.ReadOnlySurface`, which drops Write/Edit and swaps in an allowlisted Bash. It is applied per turn as `Session.turnReadOnly`, and `executeCall` enforces the same surface the request advertises.
  - Goal mode and approved plans are never narrowed by `read_only`. Plan mode before approval keeps its fail-closed surface.
  - The TUI shows a one-time `read-only tools: on (<source>)` notice, and read-only Bash rejections name the setting and point to Grep, Glob and Read.
- **Approved plans run through the goal pass loop.** The approved plan text is the objective and is carried verbatim, so no contract is drafted. The first pass is told to start on the first unfinished step. The TUI keeps `planExecuting` set until `GOAL_COMPLETE` or a mode change, so later sends continue the plan. The footer reads `plan · executing`.
- **Persistence.**
  - Tool results are matched to calls by `tool_call_id`, not position. The in-flight turn's bubble is held until the turn ends.
  - A new user turn force-flushes whatever an earlier turn left unsettled. Only answered calls are written, so nothing is left unpaired or dropped.
  - Truncated tool results are cut on a UTF-8 boundary.
- **Token accounting.** Each pass adds up the usage of every provider call it makes (`passOutcome.spent`). openrouter, groq, deepseek, fireworks, together and xai now request stream usage. Goal-state events carry a copy of the state rather than a shared pointer, which removes a data race.
- **Goal continuation.** A goal-mode prompt that `IsContinuation` recognises ("continue", "keep going", …) resumes the prior goal. The TUI passes the prior goal, live or restored after `/resume`, in `TurnInput.PriorGoal`. The resumed goal keeps the same id, contract and counters, and any extra instruction is appended as "Additional direction".
- **Time to first mutation.**
  - The contract draft runs alongside exploration. The goal loop waits at most a 45 s grace for it once it is needed, and the draft itself is capped at 2 minutes. Past either bound it falls back to the raw prompt.
  - The drafter may only use the detected commands. A deterministic post-check removes lines that name an undetected build runner.
  - The repo map now carries justfile recipe names, changed paths (refreshed every turn) and remotes with credentials removed. The `remotes()` field-index bug is fixed.
  - Up to `min(max_agents, 16)` read-only tool calls run in parallel.
  - Agent continuations compact the context, and push toward an edit when the turn has written nothing.
- **Classifier.** Windows are sliced by rune offset and lower-cased the same way cybertron does. The classifier identity includes a windowing version, so verdicts computed on misaligned windows are evaluated again instead of being trusted. Content is still always classified.
- **Guidance.** A withheld result now says it is a safety verdict and should not be retried. The work-discipline prompt tells the model the repo map already lists the commands and changed files, and not to re-read files. The `signet:debug` profile no longer describes which tools are available.
- **`/settings`.**
  - The scope chosen with `s` is kept across reopens.
  - A saved edit that a higher layer overrides shows `saved to global, but project wins (30)`.
  - The live pool resizes when `max_agents` is edited.
  - The CLI session gets the same settings-backed pool.

## How to re-check

Re-run the session analysis on new session files:

- Goal sessions should contain `assistant` and `tool` rows.
- `tokensUsed` should grow after every pass.
- "continue" should keep the same goal id.
- No contract should require a runner the repo does not have.
- The time from the user row to the first successful `Write` or `Edit` should be measurable, and should be short.
