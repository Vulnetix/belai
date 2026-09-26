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
  repository text), `RepoRead` and the native tools that can print a file's
  contents — `Cat`, `Head`, `Tail`, `Strings` and the path-reading transforms
  `JQ`, `YQ`, `Sed`, `Awk`, `Cut`, `Sort`, `Uniq`, `Tr`, `Paste`, `Join`,
  `Diff` (all `KindRead`), `SubAgentLog` (`KindProcess`),
  `SearchSessions`/`ReadSession`/`SearchMemory` (`KindAgentStore`, other
  agents' transcript and memory text), `Task` subagent reports (`KindSubagent`, model-written arbitrary text), the `Vulnetix` tool (`KindRemote`,
  database advisory text and repository snippets), the dependency hook's Vulnetix CLI
  output (`KindRemote`) and its background agents' reports (`KindProcess`,
  `internal/tui/depwatch.go`), the recovery subagent's
  process-tail briefing and the plan/goal context prefetch
  (`agent/prefetch.go`: instruction and changed files, read through the
  session's own `Read` and gated exactly like its result) all classify,
  unconditionally. Do not add an exemption for any of them.
- **Shaped, controlled results are sanitized only.** `Grep`, `Glob`, `Write`,
  `Edit`, and the rest of the native catalogue (listings, `File`, `Cmp`,
  `Date`, …) return output whose shape the harness knows — `path:line:text`,
  a list of paths, a confirmation it composed itself, a fixed argv's
  metadata — so they skip the round trip. A native tool that can print a
  file's contents is not shaped, whatever its argv. `Grep`'s text
  column is still the file's own lines, so a Grep row from a file whose `Read`
  was withheld earlier in the session is withheld too (`agent.flaggedFiles`);
  the withheld placeholder must never point the model at another tool for the
  same content. A kind absent
  from `tools.classifierKinds` is sanitize-only, so adding a tool whose
  content is arbitrary means adding its kind there. The optional diagnostics
  block that rides back on `Write`/`Edit` is shaped the same way: no more
  than ten rows, each flattened to one line, stripped of control and bidi
  runes, with a restricted source field, sealed with a nonce and a SHA-256.
- **The read index holds facts, never contents.** `internal/readindex`
  answers a repeated `Read` of an unchanged file whose earlier result is still
  in the conversation with a harness-composed pointer (path, extent, size, git
  blob id) instead of the bytes, so the pointer is not a classification
  exemption: no file content crosses. It keys on the resolved path and window,
  checks the file's stat on every lookup, is invalidated by every harness
  mutation the file-diff recorder sees, and checks liveness by the SHA-256 of
  the delivered result — a tool turn, or a prefetched `file` attachment on a
  user turn — never the call id. A withheld read is never
  recorded, a flagged file never hits, and only a permission-allowed call on
  the advertised surface is answered from it. Its summary rides on the
  per-turn status, never the system block.
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
  system block. The per-turn forge facts (`prompt.ForgeStatusBlock`: upstream,
  ahead/behind, worktrees, PR/MR number and state, CI counts) follow the same
  rule and ride on the turn's directive; forge-supplied text (PR titles, check
  names, CLI errors) never renders there.
- **Agent-store search is path-free.** `SearchSessions`, `ReadSession` and
  `SearchMemory` read other agents' transcript and memory stores outside the
  confinement root set. They take no path argument: every path comes from the
  static registry in `internal/agentstore`, so they cannot be used as a general
  read primitive. Their content is written by other models, so `KindAgentStore`
  is in `tools.classifierKinds` unconditionally. Do not add a path argument and
  do not add an exemption.
- **The confinement boundary is a fixed root set unless the user widens it.**
  The primary working directory is the default confinement root. The only
  ways to add roots are an explicit `/add-dir` command confirmed by the user,
  or the user answering the confirm-root prompt for an `@` path outside the
  roots (for the session, or saved for the project like `/add-dir`). The `@`
  chooser may list entry names above the roots, for the user only. An
  outside path is never read until its directory has been adopted.
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
- **The dependency hook is deterministic up to one sentinel.** A file
  triggers it only by matching the manifest table ported from the Vulnetix
  CLI (`internal/depwatch`, kept in step by a test against `../cli`). The
  fast-tier `dep_change` role sees only a sanitized, bounded line digest of
  the change, answers `DEPS_CHANGED`/`DEPS_UNCHANGED`, and fails toward
  checking: a malformed reply, a transport error or an undiffable file is
  checked. The CLI argv is fixed by the harness. The per-ecosystem
  `signet:deps-*` background agents are read-only (`Read`, `Grep`, `Glob`):
  they never install or run a package manager, so a malicious package's
  install scripts never run on their account. A repo-visible project
  settings file may turn `vulnetix.dep_watch` on, never off.
- **Task subagent reports are arbitrary content.** The result of the `Task` tool is model-written text, so it is added to `tools.classifierKinds` as `KindSubagent` and classified before promotion.
- **Jev intent detection sees only harness facts.** The detector payload carries the sanitized prompt, the current mode, and derived metadata such as a plan-file task count. It never carries attachment bytes or file contents.
- **Sticky mode changes only with the user's choice.** When a confident detected intent disagrees with a mode the user set, the deterministic mode-choice panel asks before leaving the sticky mode. In headless mode the sticky mode is preserved.
- **Handoff subagents are path-scoped.** `explore.Task.Scope` restrict a handoff subagent to the paths the plan names; read-kind tool calls outside that scope are refused.
- **The plan text never enters the system block.** An attached plan remains a classified attachment on the user turn; only harness-computed metadata reaches the intent detector and directive.
- **The goal contract is classifier-drafted but harness-sealed.** The
  classifier-routed goal path asks the goal-contract role for the five
  sections beneath the verbatim objective line. The draft is sanitized before
  sealing, and on any failure the raw user prompt is carried instead — a weak
  drafting model must never cost the turn. The goal never waits for it: a
  draft still running when the loop starts is adopted at a later pass
  boundary as a sealed directive, never as unsealed turn text. A memorised goal is user-authored
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
  front-matter schema validation; hooks load only after strict schema
  validation (unknown keys rejected) with no arbitrary code-path injection.
- **Hooks only narrow, and their text classifies.** Hooks come from the
  global hooks directory only, never a project directory, and each command
  must resolve inside its own directory. A hook runs after the permission
  rules: its `deny` withholds, its `ask` asks, and its `allow` never skips an
  ask or overrides a Deny rule. A blocking hook (`user_prompt_submit`,
  `pre_tool`, `pre_edit`) that fails, times out or prints anything but a
  decision denies. Hook text for the model is `KindHook`, which is in
  `tools.classifierKinds` unconditionally, and is classified separately from
  the tool result it rides on; a prompt-hook note joins the prompt before
  admission. A prompt-hook denial reason is shown to the user only. The
  project layer may turn `hooks.enabled` off, never on.
- **Skills load by name and are drafted only with approval.** `Skill` takes a
  name, never a path, and reads only a registered, re-validated `SKILL.md`;
  its result is `KindSkill`, in `tools.classifierKinds` unconditionally. Only
  a skill's name and sanitized description reach the system block, and only
  while `Skill` is on the surface; `disable-model-invocation` skills are
  hidden and answer like missing ones. `SkillDraft` is an `AlwaysAsker`: it
  asks on every call whatever the rules or the ask gate say, is withheld when
  nobody can be asked, and writes exactly the previewed file. The project
  layer may turn `skills.self_authoring` off, never on.
- **Plugins are installed by the user, validated whole, and namespaced.**
  `internal/plugins` installs only after the user confirms a full listing
  (every hook's event and command included), or `-yes` on the CLI; the TUI
  cannot install. Git runs a fixed argv with hooks disabled, no submodules,
  no `file://` transport and the scrubbed environment; a local copy skips
  `.git`, symlinks and special files. The manifest is strict, every component
  path must stay inside the plugin after symlinks, and one invalid component
  fails the plugin. Components load as `plugin:name` and never shadow the
  user's or built-ins; plugin hooks resolve inside their own directory.
  Plugins live in the global state directory only: no repository setting can
  install, enable or propose one, and the manifest has no key for providers,
  credentials, settings or permissions.
- **The OS sandbox only tightens from a repository.** `internal/sandbox`
  wraps `Bash`, inline `!cmd` and supervised processes (bubblewrap on Linux,
  sandbox-exec on macOS). The policy is built per call by
  `sandbox.FromSettings` from the settings, the current workspace roots and
  the effective posture, and rides on the call's context; guardrails off
  turns it off. Signet's state directory is always hidden inside it. The
  project layer may raise `sandbox.mode`, set `network` to `deny` and
  `caches` to `false`, never the reverse, and its `extra_writable` is
  dropped. `required` with no working backend refuses the command; it never
  falls back to running it bare.
- **MCP servers are the user's, and their text classifies.** `mcp.servers`
  is read from the user's own settings layers only; `resolve.go` drops the
  project layer's `mcp` key outright. Servers start only after the trust
  gate. A stdio server gets the scrubbed environment plus only its declared
  `env`, its own process group, and the OS sandbox when it opts in. Every
  server tool is `mcp__<server>__<tool>` with `tools.KindMCP`: mutating (so it
  asks without an allow rule and never reaches plan mode) and in
  `tools.classifierKinds` unconditionally. Server names, descriptions and
  schema text are sanitized and capped and reach the model only through the
  sealed tools briefing. Signet answers a server's `ping` and nothing else.
- **ACP never widens what an editor can do.** `signet acp` builds each
  session with `newCLISession`, the headless path, so every gate, rule,
  sandbox and budget applies. `session/new` fails closed in a directory
  without `trusted:true`; the trust prompt never runs over ACP. Permission
  asks become `session/request_permission`; anything but an explicit allow
  denies, and "allow for this session" lives in memory for that session and
  tool only. Editor-supplied `mcpServers` are ignored. Nothing but protocol
  messages is written to stdout.
- **Notifications carry harness text only.** `internal/notify` composes
  every notification from a fixed template; the one variable is a tool or
  agent name reduced to an identifier. Model output, tool output and paths
  never reach a notification. The external backends run a fixed argv with
  the scrubbed environment. `notifications` is a per-user key: the project
  layer is dropped.

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
