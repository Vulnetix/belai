# AGENTS

Guidance for coding agents (and humans) working in this repository.

## Build and test

Tasks live in the `justfile`; run `just` to list them. There is no Makefile —
`make` cannot forward arguments, which forced developers onto stale prebuilt
binaries. Everything below runs from source.

- `just check` must pass. It is `gofmt` + `go vet ./...` + `go test -race ./...`,
  in the same order as `.github/workflows/ci.yml`.
- `just build` must succeed.
- `just prompt "…"` / `just ask PROVIDER MODEL "…"` drive the CLI from source.
- `just e2e` drives the built binary against a mock provider.

See [docs/development.md](docs/development.md) for the full local and QA workflow.

## Security invariants — do not weaken

- **Untrusted content stays untrusted.** Every tool result is sanitized
  (delimiter markup removed) before it can be promoted, and none of them ever
  enters a system/agent/tools block.
- **Arbitrary content goes through the classifier.** `Bash` (an arbitrary
  command), `WebFetch` and `WebSearch` (text written off this machine),
  `Read` (a file's bytes), `GH`/`Glab` results (`KindRemote`, third-party
  repository text), `RepoRead` (`KindRead`), `SubAgentLog` (`KindProcess`),
  `SearchSessions`/`ReadSession`/`SearchMemory` (`KindAgentStore`, other
  agents' transcript and memory text) and the recovery subagent's
  process-tail briefing all classify, unconditionally. Do not add an
  exemption for any of them.
- **Shaped, controlled results are sanitized only.** `Grep`, `Glob`, `Write`,
  `Edit`, and the native catalogue return output whose shape the harness
  knows — `path:line:text`, a list of paths, a confirmation it composed
  itself, a fixed argv's output — so they skip the round trip. A kind absent
  from `tools.classifierKinds` is sanitize-only, so adding a tool whose
  content is arbitrary means adding its kind there. The optional diagnostics
  block that rides back on `Write`/`Edit` is shaped the same way: no more
  than ten rows, each flattened to one line, stripped of control and bidi
  runes, with a restricted source field, sealed with a nonce and a SHA-256.
- **Language servers are a trusted-root feature.** A language server is only
  spawned under a directory the user has already trusted, and only in an
  interactive TUI session. The server is always started with a scrubbed
  environment and its own process group. It is never asked to perform a
  `workspace/applyEdit` (every such request receives `{"applied":false}`),
  `initializationOptions` is always `null`, and binary overrides from a
  project-layer `lsp.servers` key are dropped unconditionally.
- **The repo map is harness-computed facts only.** It may contain paths,
  counts, detected commands, git metadata and file sizes, and never repository
  file contents. Repository prose reaching the model stays on the
  `RepoRead`/`Read` path, which classifies. This is what permits the map in the
  system block.
- **Agent-store search is path-free.** `SearchSessions`, `ReadSession` and
  `SearchMemory` read other agents' transcript and memory stores outside the
  confinement root set. They take no path argument: every path comes from the
  static registry in `internal/agentstore`, so they cannot be used as a general
  read primitive. Their content is written by other models, so `KindAgentStore`
  is in `tools.classifierKinds` unconditionally. Do not add a path argument and
  do not add an exemption.
- **The confinement boundary is a fixed root set unless the user widens it.**
  The primary working directory is the default confinement root. The only way
  to add roots is an explicit `/add-dir` command confirmed by the user.
  Project-level `workspace_dirs` settings propose directories. They never
  activate from the settings layer (`resolve.go` still drops them without the
  global `allow_project_workspace_dirs` opt-in). They activate only when the
  user accepts them by name — in the first-run trust confirmation for that
  directory, or with `/add-dir`. The accepted set is recorded per project; a
  directory the project adds afterwards is not covered and prompts again.
  A path outside every root is refused outright, and roots cannot overlap so
  a single path is never resolvable two ways.
- **First-run directories are gated on explicit trust.** A directory with no
  `trusted:true` entry in the global registry blocks startup with a
  confirmation before any repo content is read, any process is auto-started,
  or any model turn runs. Headless invocations fail closed; `-trust-dir` is the
  only bypass and grants trust to the directory only, never its proposed
  `workspace_dirs`. Guardrails-off does not skip the gate.
- **Path resolution is root-relative, not process-relative.** A path argument
  may be an absolute filesystem path under any root (primary or added, longest
  match first), a path relative to the working directory, or — when it starts
  with `/` and lands in no root — a path relative to the session root. A
  leading `~/` expands to the user's home before the root match. `Read`,
  `Glob`, `Grep`, `Cd`, and the native catalogue all share this rule through
  the one `*Cwd`; do not add an `IsAbs` bypass to `SanitizePath` — the
  confinement check stays the last word.
- **Plan mode has no Bash by default.** Plan mode advertises and enforces
  the fail-closed surface (`Registry.PlanWith` and `modes.ToolAllowed`):
  no mutating tools and no `Bash`. A read-only `Bash` returns only when an
  explicit permission allow rule opts into it, and guardrails off restores the
  full surface.
  An approved plan is no longer plan mode: its execute turn runs the goal
  pass loop on the full surface (the human approval is the gate), and the
  `read_only` setting narrows agent-mode turns only — never goal mode or an
  approved plan.
- **Delimiters are sealed.** Every harness delimiter carries a random nonce
  plus a SHA-256 integrity hash of its enclosed content. On egress, any block
  lacking a nonce, carrying an unknown nonce, or failing its integrity hash is
  stripped before HTTP transport. Attachment, directive, and diagnostics
  blocks must also carry an integrity attribute. That includes the `<tools>`
  briefing, so a model cannot widen its own advertised tool surface by writing
  one.
- **Classifier turns are tool-less.** The classifier payload carries no tools,
  no skills, and no agent block.
- **The goal contract is classifier-drafted but harness-sealed.** The
  classifier-routed goal path asks the goal-contract role for the five
  sections beneath the verbatim objective line. The draft is sanitized before
  sealing, and on any failure the raw user prompt is carried instead — a weak
  drafting model must never cost the turn. A memorised goal is user-authored
  and is carried verbatim, never drafted.
- **The guardrails switch reaches every surface.** Off means
  `posture.AllIgnore()` everywhere — agent session, inline `!cmd`, `@file`
  admission, background agents, and the CLI. Derive it from
  `App.effectivePosture()` in the TUI or `settings.GuardrailsEnabled()` in
  `cmd/signet`; never read `a.posture` directly and never hand-roll the
  all-ignore loop. A gated path checks the level **before** calling the
  classifier, never after — a verdict that cannot change the outcome is a
  request nobody asked for and sends the content anyway. Sanitising is not
  part of the switch and always runs.
- **Tool-call mismatch defaults to abort.** Stripping or ignoring mismatches
  requires explicit user opt-in.
- **Recovery subagent authority is bounded.** The recovery subagent sees the
  read-only plan surface plus `SubAgentLog` and `ProcessRestart`; it has no
  other tools. `ProcessRestart` may only change flags, not the binary, so the
  `argv[0]` basename is pinned to the original command. Every restart call
  consumes one `resilience.max_process_recoveries` slot and Deny rules still
  apply. When the cap is reached the process is marked `failed` with no
  further model calls.
- **Skills and hooks validate first.** Skills load only after strict
  front-matter schema validation; hooks load only after schema validation with
  no arbitrary code-path injection.

## Layout

- `cmd/signet` — entrypoint.
- `internal/...` — library code, one package per concern.
- `e2e/` — end-to-end tests that drive the built binary.
- `docs/` — architecture, specs, and the development workflow.

## Sentinel values

`internal/rolemanager` defines the strict single-token outputs for the
security classifier, mode classifier, plan evaluator, and goal evaluator.
Each sentinel has a corresponding human-readable label used by the TUI:

- **Security sentinels:** `internal/rolemanager/labels.go` — `SentinelLabels`
- **Plan sentinels:** `internal/rolemanager/labels.go` — `PlanSentinelLabels`
- **Goal sentinels:** `internal/rolemanager/labels.go` — `GoalSentinelLabels`

When adding or editing a sentinel constant, update the matching label map and
its `Label()` method test in `internal/rolemanager/labels_test.go` so the
TUI never falls back to the raw token. The TUI should call `.Label()` rather
than `string()` or `%s` on the sentinel value.

## Conventions

- Fail closed by default; relaxation is an explicit user opt-in.
- Every new package gets a package doc comment and unit tests.
- Keep package boundaries narrow; reuse `internal/config` for paths and state.
- **Align tool names and schemas to existing harnesses.** Models are trained
  on `ExitPlanMode`, `update_plan`, `Read`, `Grep`, `WebFetch`. A trained name
  with a trained argument shape is obeyed more reliably than an equivalent
  invented one, so a new tool takes the established name and schema unless no
  equivalent exists. Document any deliberate divergence in the tool description
  (as `update_plan`-in-plan-mode does).
