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
- **Sanitisation is per sink and deterministic.** `internal/sanitize`,
  `internal/netguard` and `internal/shellsafe` clean or refuse a value by where
  it is going (text, line, identifier, decision state, path, header, URL, shell
  command, tool argument); see [docs/sanitization.md](docs/sanitization.md). No
  model is asked whether a value is safe. A decision request takes only a
  `sanitize.DecisionText`, so the compiler proves the decision-state sanitiser
  ran. A `Bash` permission rule is matched against every command the parsed
  line contains: a deny fires on any of them (quotes removed, wrappers such as
  `env`, `sudo`, `xargs`, `sh -c` and `find -exec` unwrapped), an allow must
  cover all of them, and a line that does not parse is never approved by a
  rule. Read-only `Bash` runs the argv it judged, never a second split. A
  fetched URL is checked and requested in its canonical form, and every
  redirect and resolved address is checked again. Do not add a second copy of
  a control or bidi stripper, a loopback test or a shell splitter.
- **Arbitrary content goes through the classifier.** `Bash` (an arbitrary
  command), `WebFetch` and `WebSearch` (text written off this machine),
  `Read` (a file's bytes), `GH`/`Glab` results (`KindRemote`, third-party
  repository text), `RepoRead` and the native tools that can print a file's
  contents — `Cat`, `Head`, `Tail`, `Strings` and the path-reading transforms
  `JQ`, `YQ`, `Sed`, `Awk`, `Cut`, `Sort`, `Uniq`, `Tr`, `Paste`, `Join`,
  `Diff` (all `KindRead`), `SubAgentLog` and `BashOutput` (`KindProcess`),
  `SearchSessions`/`ReadSession`/`SearchMemory` (`KindAgentStore`, other
  agents' transcript and memory text), `KanbanSearch` (`KindKanban`, board
  items other sessions' models and web users wrote), `Task` subagent reports (`KindSubagent`, model-written arbitrary text), the `Vulnetix` tool (`KindRemote`,
  database advisory text and repository snippets), the dependency hook's Vulnetix CLI
  output (`KindRemote`) and its background agents' reports (`KindProcess`,
  `internal/tui/depwatch.go`), `ReadResult` slices of offloaded results
  (`KindOffload`), a `WebFetch` answer drawn from a page (still
  `KindWebFetch`), the recovery subagent's
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
- **Asking the user leaves plan mode only through the user.** `AskUserQuestion`
  is offered in every mode. In plan mode it ends the plan loop, and only the
  user's admitted answers start the new agent-mode turn with the full
  surface, the same way a typed prompt would; declined or refused answers
  start nothing. A session that cannot ask (headless, background agents,
  explore subagents) never blocks and never widens: the model is told to
  proceed. A question already asked this session is never put to the user
  again.
- **Delimiters are sealed.** Every harness delimiter carries a random nonce
  plus a SHA-256 integrity hash of its enclosed content. On egress, any block
  lacking a nonce, carrying an unknown nonce, or failing its integrity hash is
  stripped before HTTP transport. Attachment, directive, and diagnostics
  blocks must also carry an integrity attribute. That includes the `<tools>`
  briefing, so a model cannot widen its own advertised tool surface by writing
  one.
- **Classifier turns are tool-less.** The classifier payload carries no tools,
  no skills, and no agent block.
- **Decision backends are the user's, and a failing one is never an
  approval.** Jev jobs (the security guard, intent detection, routing) go to
  exactly one decision backend: OpenRouter's Decisions API
  (`classifier.kind` `openrouter-decisions`), TypeSafe's hosted API (the
  built-in classifier-only `typesafe` provider, key `TYPESAFE_API_KEY`, sent
  only to `https://api.typesafe.ai`), a self-hosted server speaking TypeSafe's
  `/v1/systemone` (a provider profile of kind `jev`; the last two are
  `classifier.kind` `jev`), or the local decision model (`decision-local`:
  Decider-4B or Plumb-4B on llama-server, `internal/decisionserver`). The
  rules:
  - A `jev` profile is a provider profile, so the project layer cannot add
    one; its URL is https or loopback http with no credentials in it, and its
    key rides only in the `Authorization` header to that URL, never across a
    redirect. No decision backend is firewall-routed or asked to chat.
  - The local server's binary comes from PATH and its argv is harness-fixed
    (`localinfer.DecisionArgs`: loopback host, `-m` path, alias). It starts
    only after the trust gate, with the scrubbed environment, in its own
    process group, and only the process that launched it stops it. Weights
    are downloaded only in a `/model` test after the user confirms the size,
    and are checked against the SHA-256 Hugging Face reports.
  - A decision request carries a state and typed questions only. State text
    cannot forge prompt structure: chat special-token openers are split, and
    decider-layout markup lines are prefixed.
  - A timeout, an oversized state, a low letter mass, 429 or 5xx hands the
    unchanged payload to the agent-model classifier; any other failure is an
    error and the pipeline fails closed.
- **A model selection is saved only after its test passes.** A `/model` edit
  that selects a model (and the providers view's assign-as-classifier, and a
  new `jev` provider) runs the `internal/modeltest` ladder first and writes
  nothing when it fails. The ladder writes no settings itself; probes see
  only harness-built content, and every step detail is harness-composed or a
  cleaned, capped excerpt. Knobs that pick no model write at once.
- **Local inference servers run scrubbed.** Every server `internal/localinfer`
  launches (chat llama-server, the decision server) gets `proc.ScrubbedEnv`,
  plus `HF_TOKEN` only on a download path, and its own process group. A
  launch's context bounds its startup only; a healthy server runs until its
  stop function is called.
- **The dependency hook is deterministic up to one sentinel.** A file
  triggers it only by matching the manifest table ported from the Vulnetix
  CLI (`internal/depwatch`, kept in step by a test against `../cli`). The
  fast-tier `dep_change` role sees only a sanitized, bounded line digest of
  the change, answers `DEPS_CHANGED`/`DEPS_UNCHANGED`, and fails toward
  checking: a malformed reply, a transport error or an undiffable file is
  checked. The CLI argv is fixed by the harness. The per-ecosystem
  `belai:deps-*` background agents are read-only (`Read`, `Grep`, `Glob`):
  they never install or run a package manager, so a malicious package's
  install scripts never run on their account. A repo-visible project
  settings file may turn `vulnetix.dep_watch` on, never off.
- **The post-end test pass is the user's opt-in and decided by exit code.**
  `internal/testdetect` names suites from a fixed marker-file table and
  package.json script names, never file bodies or prose, so the `tests:` line
  in the repository map and `last test run: pass|fail, N suites` on the turn
  directive are harness facts. `internal/testrun` runs an argv from that table
  or the user's `tests.command`, never a model's, without a shell, under the
  permission rules (a deny refuses, an ask skips), the OS sandbox and the
  scrubbed environment; pass or fail is the exit code. The suite output is
  `KindProcess`: it is sanitised and classified through `testpass.NewGate`
  (level checked before the classifier, guardrails off means sanitise only),
  reaches a model only as an attachment, and a withheld output is never
  replaced by a pointer to the same text. The fast `test_report` role sees
  harness facts and gated output only and falls back to a harness-composed
  line. The fail branch runs on the ordinary session with every gate applied
  (`fix` is goal mode, `diagnose` the read-only plan surface), is bounded by
  `tests.max_fix_passes`, and is never entered when no suite actually ran. An
  unknown `tests.on_fail` or `post_end` value is off. The `tests` block is read
  from the user's layers: a project layer may set `post_end` off and lower the
  budgets, never turn the pass on or set `command`, `scope`, `on_fail` or
  `report`.
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
  `cmd/belai`; never read `a.posture` directly and never hand-roll the
  all-ignore loop. A gated path checks the level **before** calling the
  classifier, never after — a verdict that cannot change the outcome is a
  request nobody asked for and sends the content anyway. Sanitising is not
  part of the switch and always runs.
- **Jev relevance jobs narrow or reorder and never approve.** The jobs in
  [docs/jev-jobs.md](docs/jev-jobs.md) work inside the surface the mode already
  allows; permissions, hooks, the ask gate, the classifier and the sandbox still
  apply to what they produce. Their input is harness facts and `DecisionText`
  only, never file contents, attachment bytes or tool output. An unavailable
  backend, an unanswered item or an answer outside 0 to 1 is *unknown*, and the
  job falls back to the ordinary behaviour, never to a chat model. Each job is a
  `jev.jobs.<job>` switch that defaults on, runs only with a decision backend
  configured, and that a project layer may turn off but never on. A `Bash` call
  is replaced by a builtin only when the command is one plain command, no
  deny rule matches it, exactly one shortlisted builtin rates at or above 0.95,
  the fast model's arguments pass the tool's schema and appear in the command,
  and the builtin is allowed and not denied; the provider still receives one
  result for the Bash call id, the model is told in the result, and the result
  is classified as Bash output would be.
  Explore locate (`internal/locate`) ranks files from an inventory that is eligible
  before anything is scored: dependency, hidden, ignored, symlinked, binary,
  oversized, credential-bearing and read-withheld files are never listed, ranked
  or sent. Declared names from a file reach a backend only when it is the local
  model or one the user runs, or when the user set `jev.locate_previews` to
  `hosted`; file contents never do. Its output (the `Locate` tool and the seed
  line in an explore task's prompt) is paths, line numbers and percentages, so
  it is sanitise-only like `Glob`, and a path enters a prompt only if it is made
  of letters, digits and `. _ - / @ +`.
- **Every role-manager decision is written to the session record.**
  `Activity.Record` builds a `rolemanager` entry for every event, shown or
  hidden, and `rolemanager.AddSink` delivers activities losslessly and in
  order to the writers (TUI, rc, fleet, headless and ACP transcripts). The
  render-only observer may drop; a sink never does. An entry holds the event,
  verdict, label, subject, pass, model, duration, sequence and time, plus the
  summary and outcome for a described event, and never `Detail`, a prompt, a
  command, a path or classified text. Headless and ACP runs keep a private
  transcript unless `-no-transcript` is set; it is never synced.
- **ACP resolves its model like a worker.** With no `-provider`/`-model`,
  `acpConfig` takes the settings file, then the saved TUI selection
  (`workerModel`), never the built-in OpenAI fallback.
- **Tool-call mismatch defaults to abort.** Stripping or ignoring mismatches
  requires explicit user opt-in.
- **Recovery subagent authority is bounded.** The recovery subagent sees the
  read-only plan surface plus `SubAgentLog` and `ProcessRestart`; it has no
  other tools. `ProcessRestart` may only change flags, not the binary, so the
  `argv[0]` basename is pinned to the original command. Every restart call
  consumes one `resilience.max_process_recoveries` slot and Deny rules still
  apply. When the cap is reached the process is marked `failed` with no
  further model calls.
- **Model-started processes are the model's own and never recovered.** `Bash`
  `run_in_background` goes through the ordinary permission, ask, hook and
  Vulnetix-redirect path and runs under the sandbox policy on the call's
  context; it is refused on read-only Bash and where no launcher exists, never
  run in the foreground instead. `BashOutput` (`KindProcess`, classified),
  `KillShell` and `ProcessList` reach only processes started that way, never a
  user's `!!cmd` process. Such a process is not handed to the recovery
  subagent, and `ProcessRestart` cannot restart it.
  `resilience.max_background_processes` caps the running ones and a project
  layer may only lower it.
- **Pixels are admitted by `internal/imageguard`, never by the text
  classifier, and only a capture tool or the user may supply them.**
  `tools.Result.Images` is honoured for `KindScreenshot` alone and dropped for
  every other kind; a user attaches an image with `@path` (confined to the
  session roots after symlinks, never read through `tools.Read`, at most eight
  a prompt), by pasting one from the clipboard (`clipboard.ReadImage`, a fixed
  program with no argument from a model, the scrubbed environment, five
  seconds and 32 MiB; `ui.clipboard_images`, which a project layer may only turn
  off) by dropping a file whose path becomes an `@` token, or as an `image` block in an
  ACP prompt (never fetched by address). An
  image must decode as PNG or JPEG inside the pixel and byte budget, is
  bounded to a long edge, flattened and re-encoded as a new PNG; any failure
  refuses it, and a withheld text result carries no image. Only the newest
  image is sent, whichever role holds it, and a model that neither a provider
  profile's `images` declaration nor `models.Vision` accepts gets a harness
  note instead. Bytes never enter the session record, sync or telemetry: a
  user prompt records only a marker (name, type, size, dimensions). The classifier payload never carries an image. Do not
  add a second admission path or an image-carrying kind without this gate.
- **Screenshot runs harness-fixed programs, on loopback pages or after an ask.**
  A `url` capture must pass `netguard.CheckURL` and name a loopback host, runs a
  headless browser with a throwaway profile, the scrubbed environment and its
  own process group, never disables the browser's sandbox, and blocks every
  request that is not to this machine. A `desktop` capture asks on every call
  (`tools.AlwaysAsksCall`) whatever a rule or the ask gate says, and is
  withheld when nobody can be asked. The model chooses only the target, the
  URL and a size; no argument reaches a program's argv. The tool is registered
  in the interactive session only, and a project layer may turn
  `screenshot.enabled` or `screenshot.desktop` off, never on.
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
  turns it off. Belai's state directory is always hidden inside it. The
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
  sealed tools briefing. Belai answers a server's `ping` and nothing else.
  The `vulnetix:cli` header reference (written by `/vulnetix mcp`) resolves
  to the Vulnetix CLI's credential at dial time, only in the `Authorization`
  header and only for `https://*.vulnetix.com`; it is never written out
  resolved.
- **Onboarding sends secrets only where they belong.** The Getting started
  sign-up posts to `auth.vulnetix.com` alone (redirects off it are refused);
  its password fields are cleared once the post returns or the form is left,
  and never reach a transcript, log, setting or model. The device-login key
  reaches `vulnetix auth login --noninteractive` through that child's
  environment, never its argv. The Vulnetix CLI is installed only after the
  user picks *Install*, with a fixed brew/scoop argv and the scrubbed
  environment. A provider key reaches the Vulnetix AI Firewall (BYOK) only
  while it is the active firewall and on, only through
  `vulnetix ai-firewall key set --stdin`, and a failed push keeps that
  provider unrouted rather than sent to a gateway that would refuse it.
- **Firewalls are the user's, and their verdicts are facts.**
  `internal/firewall` adapters (Vulnetix; beta Fastly, Kong, AI Security
  Gateway; custom) are configured from the user's own layers only:
  `resolve.go` and `Settings.Override` drop the project layer's
  `firewall.instances` and `firewall.active`, and the project layer may set
  `firewall.enabled` to false, never true. A firewall URL is https or
  loopback http. A firewall key lives in the credentials resolver under
  `firewall:<name>` (env, user file or keychain, never the project
  credentials file or netrc). It is sent only to its instance's URL, only
  in its configured header, and never across a redirect (model calls, model
  lists and nonce fetches all refuse one). A classifier that is
  OpenRouter's Decisions model is never firewall-routed: `ResolveClassifier`
  resolves it with routing off and the user's own OpenRouter key (no such key
  is `ErrNotConfigured`, never the firewall's key), so the direct Decisions
  call and the `/model` test never carry a firewall credential
  (`securityGuard` still refuses a config that arrives with a route). BYOK modes drop the provider's
  auth headers. Copilot, Kiro and the Cloudflare AI Gateway provider are
  never routed. The provider-native OpenRouter and Cloudflare entries are
  read-only: they never alter a request. A `firewall.Verdict` is parsed by
  the harness from response headers and error bodies, and every string on
  it is cleaned (delimiter markup, ANSI, control and bidi runes) and capped.
  It renders only as a report card or a headless stderr line. It never
  reaches a model turn, a system block or telemetry, and matched text is
  never carried.
- **Kiro tokens go only to pinned AWS hosts.** `internal/kiroauth` sends
  the AWS sign-in only to `oidc.<region>.amazonaws.com` and the access token
  only to `q.`/`codewhisperer.<region>.amazonaws.com` (or a loopback mock),
  over https, in the `Authorization` header (SSO-OIDC, model-list and
  profile calls never follow a redirect); a region must match the AWS region
  shape. The live model catalogue (`internal/kiromodels`) is facts only —
  ids, limits, effort enums, an image flag — and additionalModelRequestFields
  carries only what a model's schema declares. The device code, client secret,
  refresh and access tokens never reach a transcript, log, setting,
  notification or model, and AWS error text is reduced to its code. A
  rotated refresh token is written back only to the keychain or credentials
  file that held the old one. `AuthKiro` is reserved for the built-in `kiro`
  provider, which is never routed through the AI Firewall.
- **ACP never widens what an editor can do.** `belai acp` builds each
  session with `newCLISession`, the headless path, so every gate, rule,
  sandbox and budget applies. `session/new` fails closed in a directory
  without `trusted:true`; the trust prompt never runs over ACP. Permission
  asks become `session/request_permission`; anything but an explicit allow
  denies, and "allow for this session" lives in memory for that session and
  tool only. Editor-supplied `mcpServers` are ignored. An editor's
  `image` blocks are untrusted bytes: each is admitted by `imageguard` alone
  (never a classifier, never fetched by address), at most eight a prompt, and
  the transcript keeps a marker and no bytes. Nothing but protocol
  messages is written to stdout.
- **Kanban text is other sessions' and the web's text.** The global board
  (`internal/kanban`, `~/.vulnetix/belai/kanban`) holds items written by
  other sessions' models and by users on the Vulnetix website, so
  `KindKanban` is in `tools.classifierKinds` unconditionally. The writers'
  confirmations (`KindKanbanWrite`) are harness-composed. The per-turn board
  directive carries counts and item ids only, never item text. The tools take
  no path. Provenance (session, host, project, directory) is stamped by the
  harness from `kanban.Source` and never taken from arguments. Explore, Task
  and fan-out subagents get `KanbanSearch` only, so repository text they read
  cannot persist into the board. A main session's kanban tools are added
  after any tools allowlist (an engaged definition, a background agent, a
  headless profile), because the board is how agents hand work to one
  another. `KanbanMove` and `KanbanAdd` exist only on their
  phase's surface (the working loop, the post-report wrap-up), and plan mode
  never moves items. Every write, local or pulled, is cleaned (delimiter
  markup, ANSI, control and bidi runes) and capped. A board file failing its
  magic, version or SHA-256 is refused and never overwritten. Sync rides on
  the session-sync client (same origin allowlist, same credential, same
  `sync.enabled` switch). The project layer may turn `kanban` off, never on.
  A prefilled composer prompt from the pane is text in the editor and takes
  the typed-prompt path like anything the user types.
- **Fleet workers claim by harness, work in isolation, and hand back facts.**
  `internal/fleet` workers (`belai agent run|start`, `/fleet`) take work
  only from the kanban board. The rules:
  - **Claims.** The harness claims (`kanban.Store.Claim`, one locked
    read-modify-write). There is no model-facing claim tool, and the claim
    fields (`ClaimedBy`, `LeaseUntil`, …) are never taken from a model
    argument. The website may clear a claim, never set one.
  - **Item text.** The claimed item's text reaches the model only as a
    `kanban` attachment gated exactly like a `KanbanSearch` result
    (`agent.TurnInput.KanbanItem`): sanitised, and classified unless the
    posture ignores tool results. It never reaches the prompt, the carrier,
    the system block or a directive; a withheld item is blocked, never worked.
  - **Worker memory.** Lessons are classified when written and again when
    read, and ride only as a user-turn attachment.
  - **Board writes.** A worker's session gets `KanbanSearch`, and
    `KanbanUpdate` and `KanbanHandoff` bound to its `tools.WorkerClaim`:
    - it writes only its own item (notes only) and the items it handed off;
    - handoffs follow the profile's `handoff_to`/`handoff_labels`
      allowlist, at most five per item and six hops per chain;
    - it gets no `KanbanMove` and no wrap-up.
    In every session, `KanbanMove`/`KanbanUpdate` refuse another worker's
    live claim.
  - **Release notes.** The harness releases the item; its notes are harness
    facts (stop reason, counts, tool names), never model text.
  - **Settings and paths.** Settings, posture, credentials, trust, provenance
    and the transcript key come from the trusted repository root, never
    from a worktree. Worktrees live outside the repository and outside the
    state directory (`config.WorktreesDir`). Every git call is hardened:
    - hooks and fsmonitor off, no file transport, no credential prompt;
    - the scrubbed environment;
    - an explicit `--git-dir`/`--work-tree`, with the worktree's `.git`
      pointer checked unchanged before each call.
  - **The model's own git.** It may commit on the item's branch inside the
    OS sandbox (`fleet.Workspace.Sandbox`, layered mounts):
    - every existing entry of the git common dir is read-only, including
      config, hooks, refs, objects and the main checkout's index;
    - new objects go to a private store (`GIT_OBJECT_DIRECTORY`) that the
      harness copies in, never overwriting, before any push;
    - only the item's own `refs/heads/belai/K-xxxxxx/` and reflogs, and the
      worktree's admin dir, are writable;
    - `Settle` removes anything the model created at the top of the common
      dir after each turn.
    The harness commits what is left, after checking HEAD is still the
    item's branch.
  - **Pushing.** Only `PublishBranch` pushes (`KindPublish`,
    mutating, sanitise-only): exactly the item's branch, by an explicit
    refspec, to a GitHub/GitLab `origin`, then a draft pull request. It is on
    the surface only for `workspace.publish` and `agents.publish` on. A
    worker's Bash denies `git push`, `gh`/`glab` PR creation and merging,
    `git switch`, `git checkout -b`, `git worktree`, `git config` and
    `git remote` (`fleet.workerGitDeny`).
  - **Workspace note.** The per-turn workspace directive carries harness
    facts only: branch, base commit and the publishing rule.
  - **Preflight.** It fails closed:
    - a worker cannot turn guardrails off;
    - an autonomous worker needs a pass budget;
    - a writing worker needs isolation;
    - an autonomous worker with Bash needs a working OS sandbox.
  - **Asks.** A worker that cannot ask blocks the item with the tool names;
    it never widens.
  - **Setup failures.** A workspace that cannot be prepared gets one
    setup-debug turn (`Worker.investigate`) in the trusted repository on the
    profile's `ReadOnlySurface`, because no worktree isolates it. The failure
    is gated as `KindProcess` and rides only as an attachment. Findings go
    through `KanbanUpdate`, and the release note stays harness facts. A
    missing branch starts fresh only when it is one of the item's own
    `belai/K-xxxxxx/a<n>` names.
  - **Project layer.** It may turn `agents.enabled` and `agents.publish` off
    and lower `agents.max_workers`, never the reverse, and it can define no
    profile or crew.
- **Deferred tools change what is advertised, never what may run.** With
  `defer_tools` on (the default) a request carries the core tools
  (`tools.CoreTools`) in full, and the sealed tools briefing names the rest of
  the current surface. `ToolSearch` loads a deferred definition into later
  requests. It is harness-composed and sanitise-only (`KindToolSearch`), it
  returns tool and skill names only and never descriptions, so MCP and skill text still reaches
  the model only through the sealed briefing and the tool definitions. It can
  only load tools on the current mode's surface, so plan mode cannot load a
  writer. Deferred tools stay registered and permission-checked, and they run
  when called by name.
- **Offload stores admitted content only, and reads it back through the
  classifier.** `internal/offload` replaces an oversized result of an
  arbitrary-size kind (`Bash`, `WebFetch`, `WebSearch`, `KindRemote`, MCP,
  `KindProcess`, `KindSubagent`) with a head-and-tail preview and a
  reference, in `Session.promoteResult`, only after the result was sanitised
  and admitted; a withheld result is never stored. `Read` is never offloaded.
  The store is per session and in memory only, so no untrusted bytes land in
  the state directory. `ReadResult` is path-free: it takes a reference that
  resolves only in its session's store, and its slices are `KindOffload`, in
  `tools.classifierKinds` unconditionally. The preview is written into the
  turn once, so the cached prefix stays byte-identical. `offload` changes how
  much admitted content rides on a request, never what is admitted, so any
  settings layer may set it.
- **A WebFetch answer is drawn by a tool-less role and still classifies.** A
  `WebFetch` call with a `prompt` sends the sanitised page (capped) and the
  prompt to the fast-tier `web_fetch` role, which carries no tools, skills or
  agent block. Its answer replaces the page as the `KindWebFetch` result and
  goes through the classifier like the page would; the page never reaches the
  conversation. Any role failure falls back to the page itself. Guardrails off
  skips only the classification, never the role or sanitising.
- **Telemetry carries facts, never content.** `internal/otel` exports only
  attribute keys on its fixed allowlist, and reduces every string value to
  identifier characters, capped. Never add a key that can hold a prompt,
  reply, argument, output, path, command or URL; `TestAllowlistIsClosed` pins
  the set and `TestTelemetryCarriesNoContent` runs a real turn against a
  collector. `telemetry` is read from the user's own settings layers only.
  Export is best effort and never fails or slows a turn.
- **Session intelligence draws numbers, never provider text.**
  `internal/run/planlimits.go` is the only reader of response headers for plan
  limits, and it returns `budget.PlanLimit` values: a provider id and a window
  (harness constants), a used share, a limit and remaining count, and two
  times. Every value must parse as a number or time of the expected shape and
  pass `PlanLimit.Valid` (used share 0 to 1, remaining not above the limit, a
  reset from an hour ago to eight days ahead); anything else is dropped and the
  header string is never stored, logged or rendered. The intel surface (footer
  slot, runs-panel tab, full screen) renders from the `budget.Recorder` alone:
  token counts, hour buckets, provider and model identifiers, times and those
  readings. It is never referenced by the prompt, a directive, the system block,
  a tool result or telemetry. `intel.plan_limits` is read from the user's own
  settings layers only (`resolve.go` drops the project layer's `intel` key), and
  `ui.intel` only switches the surface off. Do not add a string field to
  `PlanLimit` or `UsageEvent.Limits`.
- **Notifications carry harness text only.** `internal/notify` composes
  every notification from a fixed template; the one variable is a tool or
  agent name reduced to an identifier. Model output, tool output and paths
  never reach a notification. The external backends run a fixed argv with
  the scrubbed environment. `notifications` is a per-user key: the project
  layer is dropped.
- **Voice input is local audio and typed-grade text.** `internal/voice`
  captures the microphone through a helper the harness launches (fixed argv,
  scrubbed environment, own process group, never through `sandbox.Wrap` or a
  tool call; `voice.device` is a plain identifier that cannot add an option)
  and recognises it with `internal/voice/asr`, pure Go, so the release stays
  `CGO_ENABLED=0`. Audio stays in memory: it is never written, logged, sent
  or put in telemetry. Every release family except plain `belai` embeds the speech model (the `belai_voice`
  build tag, fetched by `tools/voiceprep`) and hash it against the SHA-256
  pinned in `internal/voice/models.go` on every load. A build without it
  downloads the file only after `/voice download`, checks it against the
  SHA-256 Hugging Face reports and the pinned one, and refuses a mismatch,
  including for a file found in a shared cache. `voice` is read from the
  user's settings layers only (the project layer is dropped), and voice runs
  only in the interactive TUI, never headless or over ACP. A transcript is
  sanitised, tidied by the tool-less `voice_cleanup` role (its reply must be
  non-empty and not much longer than the input, else the raw transcript is
  used), sanitised again and inserted at the composer cursor only while
  `voiceComposerReady` holds; when it does not, the engine closes the
  microphone and drops audio in progress. `delivery: submit` goes through the
  Enter path and never fires for text that starts with `/` or `!` or holds an
  `@path`. Voice starts by itself only when the model is built in, in push to
  talk, where the microphone is open only while the key is held or a tap
  recording (which ends when the speaker stops) runs. `/voice debug` opens the
  microphone in its own engine while it is on screen, probes hardware with
  fixed programs and the scrubbed environment (`voice.Devices`), and writes
  nothing to disk. Dictation is written live: a running guess, the recognised
  text at once, then the fast model's reply streamed over it (`voice_cleanup`
  over the streaming classifier, cut off if it runs away), and any key typed in
  the composer cancels all of it, including the Enter that submit delivery
  would press (`voiceCancelFlow`, `Engine.Cancel`). Do not add a model-facing
  tool that starts capture.
- **Session sync mirrors the file and admits web prompts as prompts.**
  `internal/sessionsync` uploads only the lines `appendEntry` already wrote to
  the session JSONL, keyed by line index; it never composes an entry. It sends
  them only to `https://*.vulnetix.com` (or a loopback origin), with the
  Vulnetix CLI's credential in the `Authorization` header only. A prompt from
  the website is untrusted input: `sessionsync.CleanPrompt` strips delimiter
  markup, control and bidi runes, and the prompt then takes the typed-prompt
  path (`dispatchPrompt`) — the same mode selection, admission gate, posture,
  permission rules and tool surface. It never runs a slash command, `!cmd` or
  composer `@` attachment, never touches the composer, never answers a
  permission ask or question, and in agent mode with no carrier it is refused
  rather than answering the picker. The project layer may turn `sync.enabled`,
  `sync.remote_prompts` and `sync.remote_answers` off, never on.
- **Remote control starts sessions only where the host said, with asks off.**
  `belai rc` (`internal/rc`) offers only trusted projects and `--dir`
  directories (a `--dir` is trusted like `-trust-dir`: the directory only).
  Every website request is untrusted: the daemon re-checks the directory
  against its own list (exact match after resolving symlinks, never a
  prefix), the `rc-session` child checks trust again and fails closed, and
  the prompt is cleaned and admitted like a typed one. The website never
  picks the provider, model, posture or permissions. rc refuses to run with
  guardrails off, sessions run with `AllowAsk` false and web answers off
  (never `AskDisabled`, which would allow every ask), and the prompt reaches
  the child on stdin, never argv. The heartbeat's worker entries are
  registry facts plus a tail of each worker's own log, read by id from the
  fleet log directory (never a path from a record), cleaned with
  `sessionsync.CleanLogLine` and capped per line, per worker and in total.
  That log holds harness lines and the worker process's stderr only; nothing
  may write model output or item text to it.
- **A web answer resolves only the ask that is open, through the host's own
  code.** Asks, answers, turn boundaries, tool starts and role-manager
  verdicts are written to the session JSONL by `appendEntry` like any line
  (`internal/tui/web_asks.go`); the syncer still only mirrors the file. A web
  answer (`sessionsync.RemoteAnswer`) is untrusted input: it is applied only
  when its ask id is the ask open now and its kind matches, only after it
  validates against that ask (indices in range, one choice for a
  single-choice group, a known decision, notes cleaned by `CleanPrompt` and
  capped), and only through the functions the host's keys call
  (`settlePermissionAsk`, `settleClarify`, the plan-review arms), so clarify
  answers are still admitted on the agent side and allow-always writes the
  same rule. The first answer wins; a late one is refused. Plan-review notes
  take the web-prompt path. The agent picker and the trust gate are never
  answered from the web. A decision's `Detail` is never persisted.
- **A web agent draft is a prompt-grade request that writes nothing.** A
  draft (`sessionsync.RemoteDraft`, `internal/tui/web_drafts.go`) arrives only
  while `sync.remote_prompts` is on. Its premise is cleaned by `CleanPrompt`
  and admitted through the security classifier under the effective posture
  before `internal/agentdraft` sees it. The drafter is a tool-less classifier
  turn that sees the premise and harness facts only (tool names, worker
  names, labels). Every drafted value is shape-checked and cleaned, and a
  draft never offers `guardrails` or `ask_permission`. Crew offers are
  computed from the host's own profiles, never drafted. The host posts offers
  back and writes no profile, setting or file. A refusal reason is harness
  text, never model or provider output.

## Layout

- `cmd/belai` — entrypoint.
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
