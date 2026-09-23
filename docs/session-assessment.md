# Signet session assessment

This document summarises what the saved sessions under `~/.vulnetix/signet/sessions` revealed about Signet's agent/orchestration UX, and the concrete changes made as a result.

## What the sessions show

- **Zero file mutations across the whole corpus.** A scan of 166 session JSONL files found zero `Edit(...)` or `Write(...)` tool-call events. Goal-mode sessions produced many `goal_state` / `todo_list` entries and several assistant turns, but almost no disk changes. Agent-mode sessions accumulated large numbers of `rolemanager` and `reasoning` rows and burned context without making edits.
- **Goal mode spends passes planning instead of editing.** Several long goal sessions updated `passes` repeatedly but never called a mutating tool; the loop was driving the model to refine plans and todo lists rather than edit files.
- **Agent mode reasons until the context window fills.** Sessions with reasoning enabled contained very long `reasoning` blocks and many read-only tool results, but the work discipline guidance was not strong enough to force the model back onto an edit.
- **Subagent fan-out was effectively capped at 3 and not adjustable.** The `resilience.max_agents` setting defaulted to 3 and, more importantly, was **not being loaded by the settings resolver**. Even if a user wrote `max_agents: 15` into `.vulnetix/settings.json`, the running app ignored it because `config.Resolve` never applied the `Resilience` block.

## Root causes

1. **Prompts did not make time-to-first-mutation explicit.** The work-discipline guidance said "edit when clear" but did not demand at least one file mutation on the opening pass, did not limit reasoning length, and did not warn against turns that end with reasoning alone.
2. **`config.Resolve` dropped the entire `Resilience` block.** `resolve.go` had no `apply` path for `s.Resilience`, so `max_agents`, `max_attempts`, `max_iterations`, etc. were always zero in the effective settings and every caller fell back to hard-coded defaults.
3. **`MaxAgents` was treated as a tighten-only safety budget.** The global/project merge took `min(global, project)`, so a project-level raise above a global 3 would silently be ignored. For a concurrency cap this is the wrong invariant.
4. **Explore fan-out was capped at 5 tasks.** Even after raising the pool ceiling, `internal/explore.MaxTasks` limited wide parallel exploration.

## Changes made

- `internal/config/settings.go`
  - Added `DefaultMaxAgents = 15`.
  - Added `(*ResilienceSettings).merge()` with tighten-only for retry/iteration budgets and project-override for `MaxAgents`.
  - Refactored `Settings.Override` to use the new `merge()`.
- `internal/config/resolve.go`
  - Added the missing `apply` path for `s.Resilience`, so the resolver actually loads `max_agents` and records `origin["resilience"]`.
- `internal/tui/app.go`
  - Pool creation and `reloadSettings` now use `config.DefaultMaxAgents` instead of the hard-coded 3.
- `internal/tui/settings_view.go`
  - The `/settings` default display now shows 15 and `rawValue`/`commitTextRow`/unset handling write through correctly.
- `internal/tui/commands.go`
  - `/settings` now initialises `settingsState.scope = config.ScopeProject` so the persistence target is explicit.
- `internal/explore/explore.go`
  - Raised `MaxTasks` from 5 to 12 so the 15-slot pool can be used for broad exploration.
- `internal/prompt/system.go`
  - Strengthened `workDiscipline()` to prioritise time-to-first-mutation, parallel read-only batching, and a hard cap on reasoning crowding out edits.
- `internal/agent/passloop.go`
  - Tightened `goalAckDirective` and `planDirective` to require a file mutation in the opening pass unless the task is explicitly read-only.
- Tests and docs updated across `internal/config`, `internal/tui`, `internal/explore`, `README.md`, and `docs/architecture.md`.
- `docs/session-assessment.md` (this file) captures the assessment.

## Expected user-visible impact

- `max_agents` changes in `/settings` now persist and take effect immediately (the pool is resized live).
- A new project starts with a 15-agent fan-out ceiling instead of 3, so parallel exploration/background work is no longer artificially queued.
- Goal/agent mode prompts now repeatedly instruct the model to mutate a file on the first pass and to keep reasoning short relative to tool calls.
