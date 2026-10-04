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
  `Read` (a file's bytes), `GH`/`Glab`/`AWS` results (`KindRemote`, third-party
  repository text, log events and bucket objects), `RepoRead` and the native tools that can print a file's
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
  The `kb+` rows that `Grep` and `Glob` append from the knowledge store are the
  one place arbitrary text rides in a shaped kind, and they are safe only
  because the store classified each chunk when it was ingested (the Knowledge
  bullet below). Never append anything to those two kinds that did not come from
  the store.
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
  interactive TUI session or a `belai rc --web-controls` session whose web
  user turned the language-server control on (`rolemanager.DiagnosticsGate`
  passed through `headless.Params.Diagnostics`, closed when the session is
  rebuilt or ends). The server is always started with a scrubbed
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
- **Facts point a tool and never widen it, and a role's credentials stay in
  memory.** `internal/factspec` is the one table behind a profile's `facts`
  (`docs/agent-profiles.md#facts`). The rules:
  - **Facts are the user's.** They come from a profile in the user's layers, so
    a repository cannot add one; the drafter never offers them. Any key of the
    right shape is accepted and listed to the model under framing that calls
    it data. A key that names a secret and a value shaped like an AWS access
    key id are refused when the profile loads, and a hidden fact
    (`aws_external_id`) is read by the harness and kept out of the prompt.
  - **Binding is the harness's table.** A well-known fact becomes a fixed
    environment variable or flag (`factspec.Bind`), appended after
    `proc.ScrubbedEnv`. No model argument names an environment variable or a
    flag, a value is shape-checked at load, and a fact never changes the
    `tools` allowlist, a permission rule, the sandbox or the confinement
    roots. A pinned fact refuses the flags that would override it. Flags that
    send a tool's credentials to another endpoint or identity
    (`--profile`, `--endpoint-url`, `--ca-bundle`, the kubeconfig, server,
    token and impersonation flags, `--impersonate-service-account`) are refused
    for `AWS`, `Kubectl` and `GCloud` with or without facts, matched in their
    `=value` and abbreviated forms. Do not add a second flag matcher.
  - **A role is declared or asked for.** The `AWS` tool's `role_arn` is
    validated as an IAM role ARN. A role in the `aws_role_arn` fact needs no
    prompt. Any other ARN asks on every call through `tools.CallAsker`, whatever
    the rules and the ask gate say, and is withheld where nobody can ask, an
    `autonomous` worker included. A role is assumed from the ambient identity
    (never chained from a held role), after checking whether it is already the
    caller. `aws_account_id` is checked against the resolved identity and fails
    closed when it cannot be read.
  - **Credentials never leave the process except as one subprocess's
    environment.** `tools.CloudHub` holds them in memory only, for the session;
    new facts drop them. Only the `AWS` and `Terraform` subprocesses receive
    them. They are never written, logged, put in a result, an error or the `Env`
    and `Bash` environment, and their strings are redacted from output. A
    failed assumption reaches the model as AWS's error code alone.
  - **`AWS` output classifies.** Log events and bucket objects are text other
    parties wrote, so `AWS` is `KindRemote`. Explore and handoff subagents build
    their own registry, so their hub holds no facts and no credentials.
- **Knowledge is classified once, at ingestion, and a search calls no model.**
  `internal/knowledge` indexes an agent profile's listed documents, the
  project's `.vulnetix` output and a session's `@` files for `Grep`, `Glob` and
  `Read`. The session installs a `tools.Knowledge` in its registry's
  `KnowledgeHub`, and a tool consults it only on a model's own call
  (`tools.WithKnowledge`, set in `runTool`'s caller): the harness's `Read` of an
  `@` attachment or a prefetched file never carries it. The rules:
  - **Ingestion is the gate.** Every chunk is sanitised and then admitted by
    `kbgate.New` through the ordinary classifier pipeline (a profile's documents
    as `KindRead`, scanner text as `KindRemote`). The posture level is checked
    before the call and guardrails off is sanitise-only. A flagged chunk is never
    stored, a gate error stores nothing for that document, and a flagged batch is
    halved until the chunk stands alone. Do not index text that did not pass it.
  - **Search is a lookup.** The `kb+` rows in a `KindGrep`/`KindGlob` result are
    sanitise-only because of the rule above; `Read` stays `KindRead` and is
    classified again. A search for a profile or the project never leaves the
    process: no provider, no embedding service, no model file, no cgo.
  - **Profile documents are the profile's to list, under a floor.** They come
    from `knowledge.paths` in a profile (a file on this host, a built-in or a
    library profile, which installs and backs up with the block), absolute, under
    `~/`, or relative to the trusted repository root and never allowed to leave
    it, enumerated by the `internal/locate` eligibility rules (no symlink, hidden,
    binary, oversized or credential-bearing file) and refused outright under
    `knowledge.BlockedAbsolute` and for `.git`. A listed `.vulnetix` is read as
    scanner output, through the scanner gate. A web draft never offers the block.
  - **`.vulnetix` is read by the harness.** It walks `scanartifacts.Enumerate` at
    the trusted repository root (a worker's repository, never its worktree), with
    no path from a model and no symlink followed. SARIF, CycloneDX and OpenVEX
    become one chunk per record composed from identifier fields only
    (`scanartifacts.Records`: never a message, a snippet or a matched secret).
    Native third-party reports, tool logs and Belai's own state are never
    indexed. A session's `@` files are indexed in memory after the attachment
    path admitted them, and are never written.
  - **Permissions still apply.** A `Read` deny rule on a chunk's source path
    hides it from search and listing. The registry's file tools carry the hub, so
    a profile's tools allowlist decides which of them see it. A handoff-scoped
    subagent and an Explore subagent build their own registry and get none.
  - **Labels and topics are harness facts.** Every document is tagged at
    ingestion (`internal/knowledge/tags`). Labels come from the path, size and
    structure through fixed tables, topics from a fixed vocabulary matched by a
    deterministic detector (no model, no network, at most 64 KiB read), refined
    only by the `knowledge_topics` job. The per-document label line indexed
    beside the passages is composed by the harness from those tables and the
    vocabulary, holds none of the document's text, and is sanitise-only for that
    reason. Never add document text to it, and never let a model name a label.
  - **`/knowledge` is the user's view.** It opens indexes read-only through
    the catalogue (`knowledge.LoadProject`, `LoadProfile`: a corrupt file is listed
    and left alone, nothing is written), has no model-facing tool, and sends nothing to a model, a
    transcript, telemetry, audit or sync. Its search calls the same helpers as
    `Grep`, `Glob` and `Read` (`tools.PreviewGrep`, `PreviewGlob`,
    `PreviewRead`) so it can never show more than a model would be shown from the
    same store. The project scope applies the `Read` deny rules; the global and
    agents scopes show what the user's own host stored.
  - **The index is facts the user's host owns.** One file per profile and per
    project in the state directory (hidden from the sandbox), mode 0600, with a
    magic, a version and a SHA-256 trailer; a file that fails the check is used
    for nothing and never overwritten. `knowledge.*` sizes are read from the
    user's layers only, and no passage text reaches telemetry, the audit log,
    session sync or the session record. The `belai rc` advertisement carries the
    catalogue of the host's profile and offered-directory indexes (each
    document's knowledge address, size, SHA-256 and harness labels and topics,
    `rc/knowledge.go`) for the Library's Documents view, and nothing else from
    the store.
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
  only to `https://api.typesafe.ai`), Strands Decider-2B on this machine (the
  built-in classifier-only `strands-decider` provider: upstream's
  `strands-decider serve` on loopback, `internal/deciderserver`), Cloudflare's
  Clef on Workers AI (`@cf/cloudflare/clef` or `clef-flash` under the
  `cloudflare-workers-ai` or `cloudflare-ai-gateway` provider), Together AI's
  Tev1 (`together/Tev1-4B-experimental` under the `together` provider, read
  from the answer letter's log-probabilities on chat completions, or a `tev1:`
  tag on Ollama's own `/v1/systemone` under the `ollama` provider), a server
  speaking the `/v1/systemone` API (a provider profile of kind `systemone`;
  these four are `classifier.kind` `systemone`, and `jev`, the kind's name
  before, is read as `systemone` and never written), or the local decision
  model (`decision-local`: Decider-4B, Plumb-4B or Tev1 4B read from letter
  log-probabilities, or Clef-flash and Clef on llama-server's own
  `/v1/systemone`, `internal/decisionserver`). The rules:
  - A `systemone` profile is a provider profile, so the project layer cannot
    add one; its URL is https or loopback http with no credentials in it, and
    its key rides only in the `Authorization` header to that URL, never across
    a redirect. No decision backend is firewall-routed or asked to chat.
  - The Strands Decider server is used only on a loopback address whose
    `/health` names a Strands Decider checkpoint (`deciderserver.ParseHealth`,
    identifiers only). When none answers, it is launched from PATH with a
    harness-fixed argv (`deciderserver.Args`: the checkpoint directory, the
    loopback host, the port and the served name), after the trust gate, with
    the scrubbed environment plus `HF_HOME` pointing at Belai's own snapshot
    and `HF_HUB_OFFLINE`, in its own process group; only the process that
    launched it stops it. Its checkpoint and base model are pinned by revision
    and SHA-256 (`decisions.Decider2B`), downloaded only in a `/model` test
    after the user confirms the size, and an LFS file must also match the
    SHA-256 Hugging Face reports. The server never downloads. A question with
    more options than the head reads is never sent. OpenRouter and Hugging
    Face are asked only whether they serve it; a remote decider is used only
    through OpenRouter's Decisions API or a `systemone` profile.
  - Clef on Workers AI uses the user's own Cloudflare API token, resolved
    from the credentials store and never from a firewall route
    (`run.resolveClef` builds the request itself). The token rides only in
    the `Authorization` header to
    `api.cloudflare.com/client/v4/accounts/{account}/ai/run/@cf/cloudflare/clef*`,
    or to `gateway.ai.cloudflare.com/v1/{account}/{gateway}/workers-ai/...`
    with the gateway token only in `cf-aig-authorization`; the account id is
    32 hex characters and the gateway URL is checked to be on that host, and
    a redirect is refused. Cloudflare's envelope error text is sanitised and
    capped. A Clef model is never offered or asked to chat (the agent, fast
    and routing pickers drop every decision model), and no image is sent.
  - Tev1 on Together uses the user's own Together key, resolved from the
    credentials store and never from a firewall route (`run.resolveTev1`
    builds the request itself), sent only in the `Authorization` header to
    the `together` provider's base URL (https, or loopback http, with no
    credentials in it), and a redirect is refused. Each question is one
    tool-less chat completion for one token at temperature 0 with thinking
    off (`decisions.ChatLetters`), so the model is never asked to chat; the
    state is JSON-escaped into the payload Tev1 was trained on, so it cannot
    forge prompt structure. An answer is read only from the option letters'
    log-probabilities: a reply without them, or one whose letters hold too
    little of the probability, is unavailable and never read from its text.
    A Tev1 id or tag is never offered or asked to chat, like Clef, and no
    image is sent. Ollama's Tev1 is a `/v1/systemone` server on the
    `ollama` provider's address, https or loopback http.
  - A local Clef runs on llama-server build 11371 or later (`Ensure` refuses
    an older build before launching) with the harness-fixed argv plus
    `--batch-size` and `--ubatch-size` equal to the context, so a decision
    prompt is evaluated in one micro-batch.
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
  new `systemone` provider) runs the `internal/modeltest` ladder first and writes
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
  only, never file contents, attachment bytes or tool output, with one named
  exception: `knowledge_topics` (`internal/knowledge/kbgate/tagger.go`) sends a
  bounded sample of an indexed document's chunks, as `DecisionText`, to the
  decision backend the user set up. Those chunks were sanitised and admitted by
  the security classifier at ingestion, a flagged chunk is never stored so never
  sent, and a scanner artifact is never sent. The sample is at most
  `knowledge.topic_chunks` chunks and what one request holds
  (`jev.TopicLimits`), the call is one request, never retried or split, and at
  most `knowledge.topic_budget_docs` documents go per refresh. It only tags: a
  topic label helps a search find a document and never admits, permits or
  approves anything. Its failure is the pattern detector's result. Do not add a
  second job that sends file text. An unavailable
  backend, an unanswered item or an answer outside 0 to 1 is *unknown*, and the
  job falls back to the ordinary behaviour, never to a chat model. Each job is a
  `jev.jobs.<job>` switch that defaults on, runs only with a decision backend
  configured, and that a project layer may turn off but never on. The score
  cut-offs (`jev.thresholds`, defaults in `config.DefaultJevThresholds`) are
  read from the user's own settings layers only: `mergeJev` drops the project
  layer's `thresholds`, an invalid set fails validation and is never used, and
  the allow and deny cut-offs stay on opposite sides of 0.5 at least 0.05
  apart. A `Bash` call
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
  The delivery crew's three jobs (`handoff_clarity`, `gate_alignment`,
  `request_coverage`, `internal/rolemanager/jev/delivery.go`, called from
  `internal/fleet/relevance.go`) only narrow. They can send an auto-routed
  handoff to review, flag a gate or file a gap card, and cannot move a card out
  of review, mark a gate met or a clause covered, or skip a harness check. A
  handoff the counted rule already sent to review makes no backend call, and the
  cut-offs `clear_at`, `align_at` and `cover_at` follow the same user-layer-only
  rule as every threshold. Only titles, clauses and gate identifiers reach a
  backend, and a card the harness files from a rating holds ids only.
  Request scale (`request_scale`, `agent/scale.go`) sees only the sanitized prompt.
  A simple verdict (at or above `simple_at`, default 0.80, never below 0.5, and
  staged below `keep_at`) only drops goal-turn ceremony: the contract draft, the
  changed-file prefetch (instruction files stay), the planning list and test
  verification surface, the completion verification pass and the no-write
  escalation. It never changes the mode, a permission or a gate, the goal
  evaluator still ends the goal, and any unknown answer runs the ordinary goal.
  Goal judge (`goal_judge`, `agent/goaljudge.go`) sees the goal, the todo list and
  the pass ledger's facts only, never the evidence, the reply or file contents. It
  settles a pass only when one option is at or above `goal_complete_at` (default
  0.90, never below 0.5) or `goal_not_started_at` and both rivals are at or below
  `goal_rival_max` (never above 0.5); those keys are read from the user's layers
  only. Anything else, an error or a timeout goes to the model judge with the
  scores as a harness line and an instruction to accept completion only when
  tools showed a check. A complete verdict still meets the verification gate.
- **Every role-manager decision is written to the session record.**
  `Activity.Record` builds a `rolemanager` entry for every event, shown or
  hidden, and `rolemanager.AddSink` delivers activities losslessly and in
  order to the writers (TUI, rc, fleet, headless and ACP transcripts). The
  render-only observer may drop; a sink never does. An entry holds the event,
  verdict, label, subject, pass, model, duration, sequence and time, plus the
  summary and outcome for a described event, and never `Detail`, a prompt, a
  command, a path or classified text. Headless and ACP runs keep a private
  transcript unless `-no-transcript` is set. A headless run's is never synced.
  An ACP session's is mirrored upload-only under the same `sync.enabled`
  switch and credential as the TUI (`cmd/belai/acpsync.go`): no remote
  prompts, no remote answers, and the session name carries only the editor's
  declared name, cleaned to one bracket-free line, and the prompt's first line.
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
- **Bash rewrite rules are the user's, and permissions judge what runs.**
  `bash_rewrite` (`internal/config/bashrewrite.go`, docs/bash-rewrite.md) is
  read from the user's own settings layers only; the project layer may switch it
  off, never on and never add a rule. A rule is plain words on both sides
  (`shellsafe.ValidRewriteRule`) and `shellsafe.Rewrite` changes only the
  program words of commands written in command position, found by parsing, never
  by substring, and not those held by a wrapper. The result is re-parsed and
  must keep the line's flags and command count, else the line runs as sent. It
  runs once, before permission matching (`agent.rewriteBashArgs`): an explicit
  deny or block rule on the model's own line stops the call before any rewrite,
  and the rewritten line is then judged against every rule like any other line.
  The result begins with a harness-composed note (`tools.RewriteBash`). No model
  is asked.
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
- **ACP toggles are the user's session-only choice.** The `guardrails`, `ask`
  and `caveman` config options, like the model pick, are matched against the
  fixed option ids and the values `on`/`off` or a value Belai listed, rebuild
  the session through the original builder (`buildACPSessionWith`, trust check
  included) and are never written to a settings file. They start from the
  user's settings, and a change is refused while a turn runs. Guardrails off
  is `posture.AllIgnore()` through `settings.Guardrails`, as everywhere.
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
  harness from `kanban.Source` and never taken from arguments. The one choice a
  model has is `KanbanHandoff`'s `repo`, offered only to a profile with
  `kanban.handoff_repos`: it must match a checkout in the harness's own
  repository index (`WorkerClaim.UseRepoIndex`), and the project and directory
  are derived from that checkout (`kanban.ProvenanceFor`), so no model text
  becomes provenance. An unknown, ambiguous or path-shaped name is refused and
  nothing is filed, and the survey list, labels, hop limit and per-item cap
  still apply. Explore, Task
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
- **The delivery crew's test run and seed cards are harness work.**
  `belai:delivery` is `one_per_repo` too. A worker with `kanban.quality.sweep`
  (the scout) runs the detected suites or `tests.command` itself
  (`Worker.qualitySweep`, over `internal/testrun`: an argv from the table, no
  shell, the user's permission rules, the OS sandbox and the scrubbed
  environment; a Go suite gains `-cover` from a fixed rewrite, never a model's
  argument). Launching the crew is the opt-in; `tests.post_end` is not read. No
  model runs a suite or judges whether one ran, and suite output never reaches
  a card, a record or a model: `internal/quality` keeps only test and package
  identifiers (cleaned, capped), coverage percentages, exit codes and marker-file
  presence. The record `.vulnetix/belai/quality/<commit>.json` is written by
  the harness alone, refuses a symlinked directory, is read back only for the
  commit it names, and is written only when a suite actually ran, so a denied
  or unstartable run is retried at the next launch. Its existence for HEAD is
  the gate that stops the suites running twice on a commit. Seed cards are
  filed with `FindingInput.Once`: never reopened after done, never doubled over
  an open card with the same title (including a person's), and never
  recreated after a person deletes one. `Store.CloseAbsent` closes only
  unclaimed failure, coverage and untested cards whose subject is gone from a
  run that looked for it. A card labelled `quality` forces the scout's handoffs
  to the profile's `kanban.quality.list` (review by default) whatever the model
  asks, as a survey does. The delivery scout has no daily survey and no
  `every` stamp.
- **Acceptance gates are references, and the harness decides them.**
  A gate on a card (`kanban.Gate`) names a test suite the harness detected
  (`internal/testdetect`) and, for a Go suite, a package directory and a test
  name, each a plain identifier. It never holds a command, and there is no
  custom oracle command now or later: the argv is built by
  `quality.GateArgv` from the suite table, `dir` anchored with `./`, run without
  a shell. `KanbanHandoff` refuses the whole handoff for an undetected suite, an
  unsafe identifier, a missing directory or a symlink out of the repository, and
  never drops a gate. The harness assigns ids and the `unmet` state; no model
  argument reaches a gate's id, state, ref or note, and a pulled copy of a card
  never carries or replaces them. With `kanban.gates.verify` the harness runs
  the gates on the branch after the turn is committed, with the claim still
  held and renewed, under the permission rules, the OS sandbox and the scrubbed
  environment, and decides each by exit code (`quality.JudgeGate`). A run that
  tested nothing is never a pass. `enforce` makes the result decide the route,
  so a model's completion claim cannot close a card; a gate that could not run
  (deny or ask rule, no binary, no sandbox) blocks the card for a person and is
  not a failed attempt. Only harness facts reach a card, a note or a model: gate
  ids, suite names, cleaned test identifiers, exit codes and commits, never test
  output text. The verification record under `.vulnetix/belai/quality/verify`
  is written by the harness alone with the quality record's symlink refusal,
  and an abandoned gate is never success. A project layer cannot add a profile,
  so it cannot turn verification on.
  `KanbanGate` is a reviewer's tool for manual gates, bound to the claim like
  `KanbanVerdict` (no item id, the claimed item only) and refusing a runnable
  gate, and its evidence is cleaned to one capped line. A card is held to its
  manual gates only under `enforce` with `review`: an undecided or unmet one is
  a failed attempt, and an abandoned one is `blocked` with `HANDOFF REQUIRED`
  and gate ids in the note, never model text. A reviewer's manual gates are set
  back to `unmet` before each review. Handoff routing by clarity (`auto`) is
  deterministic (`tools.ClearTitleRunes`, `ClearBodyMin`, `ClearBodyMax`,
  `ClearMaxGates`) and can only send a handoff to review: a model's own doubt
  narrows, and no declaration or score moves a card out of the human gate.
  The delivery crew's two fast roles are tool-less turns with a harness
  fallback. `gate_draft` sees only a card's text after the same gate as item
  text, and everything it writes becomes a manual gate (a line that looks like a
  command or a path is dropped), so a drafted gate can never reference a suite.
  `delivery_report` sees harness facts only (ids, kinds, states, counts), never
  a card's words, a note or test output, and its text rides only on the pull
  request description.
  Request coverage is deterministic and the harness's, not a model's:
  `KanbanContract` records at most 12 clauses on the claimed request card
  (`kanban.MaxClauses`), a handoff must cover at least one of them, and a clause
  no handoff covers gets a gap card composed by the harness from ids only
  (`FindingInput.Once`, label `coverage`, no clause text), so a model's words
  never reach a card the harness files and no clause can go quietly missing.
- **The security crew's review, cards, verdicts and VEX are harness work.**
  `belai:security` is marked `one_per_repo`: a start is refused while a worker
  of it is live in the repository (`Registry.CheckCrewFree`). Whether a review
  ran is decided by `scanartifacts.ReviewedAt`, which matches the full commit
  id of HEAD against `memory.yaml`, a CycloneDX property or a SARIF property,
  with no clock and no cache. `Worker.sweep` runs the fixed review table itself
  when none matches, then files and reconciles cards from parsed identifiers.
  The model is never asked whether a scan ran or a finding exists, and a card
  body holds ids, versions, paths and a severity word, never scanner, advisory
  or matched text (a secret's value never reaches a card). A reconcile compares
  only a kind whose own artefact records HEAD, so a missing report never closes
  a card, and a patcher's reconcile never scans. `Finding`, `SeenRef`,
  `Verdict` and `VEX` on a card are harness-set and never taken from a model
  argument. They sync with the card, but a pulled copy is only ever offered to an
  empty field: `Merge` validates each value against the shape the harness gives
  it (`CleanFinding`, `CleanRef`, `Verdict.Valid`, a `.vulnetix/vex/*.openvex.json`
  path) and never replaces or clears a value this host holds, and the website
  cannot set them. `KanbanVerdict` is bound to the claim: it takes no item id,
  writes only the claimed item's verdict and note, and moves no list; the
  harness routes the card from the recorded verdict. Only the verifier's profile
  (`kanban.security.vex`) may reject or close a card, and it closes one only
  with a verdict. The VEX is composed by `internal/vex` from an enum verdict, an
  OpenVEX justification enum and cleaned one-line statements, written to the
  trusted repository root (never a worktree) under a plain-identifier file name
  with a symlink check and an atomic rename. A crew start (the
  one-per-repository check, the worker cap and the spawns) runs under
  `Registry.WithCrewStart`, one lock, so concurrent starts cannot both pass.
  The enrichment item that has the scout plan fixes is filed by the harness
  (`Once`, `sweep:<commit>`); the cards its claim may annotate
  (`WorkerClaim.Notable`, note-only, no edit or move) come from harness-set
  fields (`Finding`, `SeenRef`, list, labels, claim), never from item text.
  Feedback rounds (`kanban.security.rounds`) scan the worktree with the fixed
  `sca` argv, read only identifiers, versions and counts from the artefacts, put
  a harness-composed account on the next turn, delete the `.vulnetix` directory
  the scan created (never following a link) so scan output is never committed,
  and fail the attempt when the scanner still reports the finding after the last
  round, whatever the model said. No scanner text reaches a model.
- **Fleet workers claim by harness, work in isolation, and hand back facts.**
  `internal/fleet` workers (`belai agent run|start`, `/fleet`) take work
  only from the kanban board. The rules:
  - **Claims.** The harness claims (`kanban.Store.Claim`, one locked
    read-modify-write). There is no model-facing claim tool, and the claim
    fields (`ClaimedBy`, `LeaseUntil`, …) are never taken from a model
    argument. The website may clear a claim, never set one. An assignee
    naming a person (`person:`) matches no worker, and `crew:NAME` matches
    only workers started in that crew.
  - **Pause.** A pause is an empty marker file (`<id>.pause`) beside the
    worker's registry record, set only by the CLI or the TUI. It carries no
    text, is checked between cards so a turn is never cut off, and a
    valid worker id is the only path it can name.
  - **Web prompts to a worker.** Only with the user's `sync.remote_prompts`
    (`Worker.RemotePrompts`; a project layer may only turn it off). A prompt
    typed into a worker's session, such as a crew message the Agents page sends
    to each live worker, is cleaned by `sessionsync.CleanPrompt`, enters the
    running turn only through `Session.Steer` (sanitised and admitted at the
    pass boundary like typed steering) or is held, at most eight, for the next
    turn. It is never a slash command, a shell command or an answer, and it
    writes no file. The `profile_facts` on a worker's user entries are harness
    facts (names, presentation, hash, model, tool count), never the prompt or
    the tool list.
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
  - **Files are placed by the harness and permitted by the profile.** A
    worker's profile defines what is put in its worktree
    (`internal/fleet/sync.go`), and the harness does the copying, so a worker is
    never given the repository path and has no mount of it. The documents in
    `knowledge.paths` are copied read-only (`knowledge.EnumerateProfile` and
    `CopyDocs`: a relative path in place, an outside path under
    `.vulnetix/knowledge/<label>/`, never over a file the harness did not place).
    `workspace.sync` entries are copied in before each turn and, for `write`,
    merged back after it under a lock (`config.AcquireFileLock`, the lockfile in
    the state directory): the worker's version when nobody else changed the file,
    otherwise the lines it added appended, and never its deletions. The text is
    sanitised, files and entries are bounded, only regular text files are copied,
    a symlink on either side is refused, and a path is plain characters so a
    permission rule built from it is exact. Whoever wrote the profile (a library
    install is allowed and keeps both blocks), a fixed floor holds:
    `agentprofile.ProtectedRead` and `ProtectedWrite` (Git's files, Belai's state,
    credentials and settings, the scanner evidence), `knowledge.BlockedAbsolute`
    (the filesystem root, the home directory itself, credential stores, the
    kernel's pseudo filesystems, the system files that hold secrets, Belai's
    state), credential files by name, and no write-back over a
    file Git tracks outside `.vulnetix`. The harness's commit leaves placed paths
    out (`Workspace.changedPaths`) and a branch that commits one fails the
    attempt. A worker's `Write(*.vulnetix/*)` and `Edit(*.vulnetix/*)` denies are
    `permissions.Settings.Harness` rules, and a `write` entry adds a `Permit` rule
    for exactly its path (`fleet.SyncPermits`); `Permit` exempts a call from
    `Harness` rules only, never from the user's `Deny` or `Block`, never a shell
    line, and neither field is read from a settings file. The text is model output
    that other workers read, so it reaches a model only through `Read` (classified)
    or the knowledge index (classified at ingestion).
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
- **The WebFetch cache and fetched-page index hold admitted text only, and a
  hit or a search never skips classification.** `tools.WebPages`
  (`internal/tools/webpages.go`, docs/web-fetch.md) stages a fresh page with its
  `WebFetch` result, sanitised; `Session.promoteResult` settles it only after the
  gate: admitted (classifier proceed, or guardrails off) moves it into an
  in-memory, bounded, TTL'd cache and hands it to the index, withheld drops the
  staged page and evicts anything held for that URL from both. A hit is returned
  as an ordinary `KindWebFetch` result, so it is sanitised and classified like a
  fresh one (with a `prompt`, the answer is classified); the URL policy runs
  before the lookup and a miss runs every redirect and address check. Only 2xx
  pages are held and nothing is written to disk. The index
  (`internal/agent/webindex.go`) reuses `internal/knowledge`: each chunk is
  sanitised and admitted by `kbgate.New(…, KindWebFetch)` at ingestion, a flagged
  chunk is never stored, a gate error stores nothing, and a search calls no model
  or network. `SearchFetched` is path- and URL-free and its `KindFetched` result
  is sanitise-only only because of that ingestion rule: never put text in that
  kind that did not come from the index. A page indexed while guardrails were off
  is never served once they are on and is re-classified on re-fetch; a `WebFetch`
  deny rule hides a page. A registry built for a subagent has a page store that
  is off, and only a top-level session (`agent.Options.WebPages`) switches it on.
  `web_fetch` settings may be set by any layer, are clamped to fixed ceilings, and
  never change what is admitted.
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
- **Read aloud sends text off the machine only with the user's agreement.**
  `internal/tts` reads a message aloud through Microsoft's read-aloud service
  (`speech.platform.bing.com`), so the text leaves the machine. It is off until
  the user runs `/tts on`, which is also the consent (`tts.consented`); the
  `/settings` row never turns it on for the first time. The `tts` key is read
  from the user's own settings layers only (`resolve.go` drops the project
  layer's), so a repository cannot turn it on, name a voice or size the cache.
  The endpoint is `wss` to that one host under `netguard.Endpoint`, follows no
  redirect and carries only a public client constant: no user credential, and
  it is never firewall-routed. Only the sanitised, prepared text of one message
  is sent (`tts.Prepare`: fenced code is not read, URLs become "link"); the
  voice name is `[A-Za-z0-9-]{1,64}` and the text is escaped into the request.
  The service answers with MP3, decoded in process by a pure-Go decoder as
  untrusted-input code: a panic becomes an error, the decoded size is bounded
  and non-audio is refused.
  The text, the audio and the player card never enter telemetry, a notification,
  the session record, sync or a model's transcript: the card is an `Ephemeral`
  render-only message holding numbers, never audio. No model-facing tool starts
  playback. The playback helper (`paplay`, `aplay`, `ffplay`, `play`) gets a
  fixed argv, `proc.ScrubbedEnv` and its own process group, outside the OS
  sandbox, and only audio on stdin. The audio cache is the one place audio
  touches disk (the microphone's never does): under the user cache directory,
  mode 0700, SHA-256 key names that hold no text, a size cap with least recently
  used eviction, written whole through a `.part` name, and a key that is not a
  SHA-256 hex string is refused. A spoken "stop" stops playback before it
  touches a turn, and a transcript that mostly repeats the words being read is
  dropped as echo (`ttsEcho`), never matched or dictated.
- **The vulnerability row is composed from one validated identifier.**
  `internal/vulnid` is the one recognizer of prefixed advisory identifiers
  (CVE, GHSA, OSV, PYSEC, RUSTSEC, GO, GSD, EUVD, VND and the distribution
  advisories): strict ASCII shapes, bounded on both sides, nothing matched
  across a control, bidi or zero-width rune. The TUI scans the tool results and
  the reply it already shows (`internal/tui/vulnwatch.go`, no model call) and adds
  one `Ephemeral` row per identifier per session, so it is never a session
  entry, never in sync, telemetry, audit or a notification, and never promoted
  to a model. Its link (a fixed https host with the identifier as one escaped
  path component), its `vdb` hint and its `belai:triage` launch take only
  `vulnid.Valid`'s canonical string, never the text around it; the click handler
  validates again. `belai:triage` is read-only (`Vulnetix`, `Read`, `Grep`,
  `Glob`), starts through `bgagent.Manager.StartTask` under the ordinary
  permissions, trust gate and sandbox, and receives the identifier as one
  `vulnerability_id:` data line, never in its instance name. Do not add a second
  identifier recognizer or let any other text into the row. See
  [docs/vuln-row.md](docs/vuln-row.md).
- **The `/diff` pane is the user's own view and shows no denied file.**
  `internal/workdiff` collects the working tree's changes with read-only,
  hardened git calls (hooks and fsmonitor off, no optional locks, the scrubbed
  environment) and `internal/tui/diff_view.go` draws them. The text goes to the
  screen only: never a model, transcript, telemetry, audit or sync, and there is
  no model-facing tool. A path a `Read` deny rule covers is listed by name and
  status and its content is never read; a symlink is never followed and sizes
  are bounded. See [docs/diff.md](docs/diff.md).
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
  tool that starts capture. The wake word (`voice.wake_word`, listen mode only)
  gates on the final transcript: speech without "Hey, Belay" is dropped in
  `voiceGate` before it is queued, shown, cleaned, sent to Jev or recorded, and
  partials never show while it is on. Spoken keywords (`internal/voicecmd`, a
  fixed vocabulary matched on the whole utterance, never a model) act only on
  the ask or running turn they name, through `settlePermissionAsk`,
  `settleClarify`, the plan-review arms and `cancelTurn`, with source `voice`;
  an open ask keeps the microphone ready for keywords alone (`voiceCommandReady`),
  and nothing else said there is shown or sent. `allow always` needs its exact
  phrase. The `voice_command` Jev job sees the cleaned speech and plain target
  names only, needs exactly one target at or above `voice_at` (default 0.95, at
  least 0.5, user layers only), and runs only the function the matching slash
  command runs, never a spoken command line; it falls back to dictation on any
  miss, failure or timeout. `voice.wake_word` and `voice.commands` are
  per-user keys, dropped from the project layer.
- **The audit log is harness facts, hash-chained, and never composed from the
  transcript.** `internal/audit` records what a host and its agents did (a
  claim, a commit, a gate decided by exit code, a VEX written, a request the
  website made of the host) and `sessionsync.AuditSyncer` uploads it. It is not
  the session mirror: it never reads or writes the session JSONL, and it has
  its own endpoint, because the mirror is content and this is not. Every string
  on an event is reduced to `[A-Za-z0-9._:/@+-]` and capped (`audit.Clean`), a
  commit id is kept only as full lowercase hex, an advisory id only in its
  identifier shape, and a verdict and an actor kind only from their enums.
  The event's keys and the `data` map's keys are closed sets
  (`TestEventKeysClosed`, `TestDataKeysAllowlist`); never add a key that can
  hold a prompt, reply, argument, output, command, file content, commit message
  or model-written note. The recorder stamps seq, time, hostname, scope and the
  chain hash, so an emitter cannot set them and no model argument reaches an
  event. Each process run appends to its own stream file under the state
  directory, each event's hash covers it and the previous hash
  (`audit.Canonical`, pinned to the server's by a golden vector), and the server
  stores a chain break flagged, never dropped. A card links to a vulnerability
  only when it is a security card (`kanban.Item.VulnID`). With no recorder
  installed (sync off) `audit.Emit` does nothing, the audit is best effort and
  never fails or slows what it records, and the project layer may turn sync
  off, never on. See [docs/audit.md](docs/audit.md).
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
- **Teleport moves a transcript and a profile, and trusts neither.**
  `belai -teleport` (`internal/teleport`, docs/teleport.md) continues a session of
  the account on this host. The rules:
  - **What moves.** The session transcript and the agent profile the session ran
    under, with the crews that list it and their members when this host lacks
    them. No provider, credential, setting or schedule. A scheduled profile is
    not installed. The session's mode and active profile become the new
    session's own record; its model is a hint applied only where this host
    already has that provider credentialed; its plan and goal names, guardrails
    state and working directory never carry (`session.Meta` is rebuilt, and the
    directory is where the command ran or a worktree teleport made).
  - **The transcript is untrusted.** It came through the backend from another
    host. `teleport.verify` requires every line from 0 to the frozen snapshot,
    once each and in order, with shaped ids, types and roles and bounded sizes;
    `teleport.build` runs every content and meta string through `sanitize.Text`,
    drops the origin's `session_meta` lines and rebuilds one root. A gap or a
    short page is a refusal, never a partial session. `session.Store.Import` is
    O_EXCL, so a teleport never replaces a session, and a failed teleport removes
    what it wrote.
  - **A profile and a crew are installed like any library install.** The one
    installer is `internal/libinstall`, shared with the rc daemon: strict parse,
    validated whole, no replace without the request's say-so, members before the
    crew. Do not add a second installer.
  - **Git is facts only and hardened.** The backend holds the origin's remote,
    branch, abbreviated HEAD and dirty flag, and nothing of the code. The remote
    must match this checkout; a missing commit is fetched from origin and refused
    when still missing (`-teleport-ref` is the user's explicit override, shape
    checked). A mismatched checkout gets a worktree under `config.WorktreesDir`
    and is never moved. Every git call is `forge.HardenedGit`, with ids passed
    after `--end-of-options` or `--`. The worktree is trusted for the directory
    only, after the repository was.
  - **The backend gates and forgets.** Every read is answered only for a ready
    teleport row naming this target host, the same principal, and a manifest
    entry (`belai_teleport.go`). A sandbox is never a target. Each request makes
    a new row and a new session. A `teleport_backup` request is made by the
    backend alone, never by the browser, and the origin host uploads only while it
    is delivered. The row keeps ids, status and times for audit and outlives the
    sessions it names; its `coord` column (git facts, manifest, overrides) is
    cleared when the teleport completes, fails or expires. A session's origin is
    read from the row, never from a field the host sends.
  - **Ack before opening.** The new session is kept only once the backend has
    recorded the teleport, so a session that came from another never exists
    without its audit record. The target's `host.teleport` audit event names the
    new session and the origin id only.
- **Remote control starts sessions only where the host said, with asks off.**
  `belai rc` (`internal/rc`) offers only trusted projects and `--dir`
  directories (a `--dir` is trusted like `-trust-dir`: the directory only).
  Every website request is untrusted: the daemon re-checks the directory
  against its own list (exact match after resolving symlinks, never a
  prefix), the `rc-session` child checks trust again and fails closed, and
  the prompt is cleaned and admitted like a typed one. Without
  `--web-controls` the website never changes a session's provider, model,
  posture or permissions after the start request, rc refuses to run with
  guardrails off, sessions run with `AllowAsk` false and web answers off
  (never `AskDisabled`, which would allow every ask); a guardrails-off
  project preference is overridden to on for a remote session, never honoured
  without the host's flag. The prompt reaches the child on stdin, never argv. The heartbeat's worker entries are
  registry facts plus a tail of each worker's own log, read by id from the
  fleet log directory (never a path from a record), cleaned with
  `sessionsync.CleanLogLine` and capped per line, per worker and in total.
  That log holds harness lines and the worker process's stderr only; nothing
  may write model output or item text to it. An offered directory carries only
  identifier-shaped git facts read from its files (`rc/dirgit.go`: owner/repo,
  forge host and kind, branch, default branch), never a URL or a credential. A
  `library_sync` request runs one pass of the automatic library sync and is
  acknowledged with counts only. A website `pause` or `resume`
  request names a worker id only: the daemon accepts it when it has the shape
  of an id (`fleet.ValidID`) and the worker is live in the host's own
  registry, and otherwise refuses it with a reason. It sets or clears the
  worker's empty pause marker and does nothing else.
- **Web session controls are the host's opt-in and a fixed table.**
  `belai rc --web-controls` passes `-controls` to each `rc-session` as fixed
  argv, and only then does the syncer take `commands` from the inbox
  (`sessionsync.RemoteCommand`: one slash line or one key). Each is parsed by
  `internal/sessionctl`, the same table the TUI's control commands use, and
  nothing outside it runs: never a free-form slash command, `!cmd` or `@`. Every
  value is checked there or by an existing validator (a model through
  `rc.CheckModel` against the host's credentialed providers, Jev thresholds by
  `JevThresholdSettings.Validate`, known job and language names). A control
  changes that session only (`sessionctl.State.Apply` on a copy of the
  settings) and writes no settings file; a change that needs it rebuilds the
  agent session between turns (`rc.Controller`), keeping the history. Guardrails
  may be turned off only with `--web-allow-guardrails-off` (refused without
  `--web-controls`), and off means `posture.AllIgnore()` as everywhere. Ask on
  routes permission and clarify asks to the web through `rc`'s ask bridge,
  recorded and validated as the TUI does (`internal/webask`): only an explicit
  allow runs a call, allow-always is remembered in memory for that session,
  and an unanswered ask is denied after `rc.DefaultAskWait`. Ask off mirrors
  `f4` (`AskDisabled`), so it is part of the same opt-in. Each applied control
  writes a harness-composed transcript line and its ack carries the state;
  refusal reasons are harness text. Auto-commit commits only the paths a
  completed goal's tools changed through `forge.CommitPaths`, and the post-end
  test pass is `headless.RunPostEnd` under the session's gates.
- **Web project settings write the host's preferences only.**
  `belai rc --web-project-settings` advertises each offered directory's
  `config.ProjectPrefs` as flat keys with their resolved value and origin, and
  takes `project_prefs` requests naming an offered directory (the same exact
  match as a start) and flat keys to set or clear. Only `config.PrefKeys` can be
  named, each value is a boolean, a word from the key's fixed set or a number in
  [0,1], and the result must pass `ProjectPrefs.ValidateOver` before it is
  written to the host-private preference file, never the repository. A
  preference that later fails validation is dropped by `config.Resolve` with a
  note rather than stopping Belai. The acknowledgement holds counts and the
  directory's base name only.
- **A scheduled agent starts only what a worker request could.**
  `internal/schedule` records (profile, cron, directory, on/off) sync with the
  website like kanban cards, and the website may edit them but never run one or
  supply a prompt, model, posture or permission. The `belai rc` ticker fires a
  due record only through `startWorkers`: the daemon matches the directory
  against its own list, the profile must be a worker profile in its own
  catalogue, and `belai agent start -drain` applies the trust check, the
  preflight and the worker cap. A record the host cannot accept (a cron that
  does not parse, a directory it does not offer, an unknown profile) is turned
  off with a status, never applied. The run record (last run, status, next run)
  is written only by the host and a pull never replaces it. A run missed while
  the daemon was down is skipped, a schedule fires at most once per cron tick,
  and the run is recorded before it fires so a crash cannot double fire. Text
  pulled from the website is cleaned before it is stored, and `schedules.json`
  is private (0600). Every firing and refusal is a `host.schedule` audit event.
- **A web profile install is the user's own action, and is validated as a
  profile.** The
  website keeps a library of agent profiles (versions in S3, indexed per
  tenant) and asks a host to back one up or install one through the dispatch
  queue, `profile_backup` and `profile_install`. A request carries identifiers
  only, and the server answers a host's upload or fetch only for the request that
  names it, while it is delivered to that host. A backup writes nothing on the
  host. An install is made by a person with their own login, so it is not held to
  the rules for web prompts: it does not need `sync.remote_prompts` and a
  profile may be as permissive as the user chooses. The fetched markdown is still
  parsed strictly and validated whole (`agentprofile`), and it is refused if it
  is not a valid profile, names a built-in, would replace a profile without an
  explicit replace and the same `id`, or reuses another profile's display name.
  The reason sent back is harness text with a short cleaned excerpt, never the
  profile. The server checks shape and size only; the host's validation is the
  authority. Do not add a content rule here that a prompt would need.
- **A library item is a document that validates whole, and is the user's to
  switch off per kind.** The website keeps skills, prompts and the other
  documents in [docs/library-items.md](docs/library-items.md) and asks a host
  to back one up or install one through the dispatch queue (`item_backup`,
  `item_install`), identifiers only. `internal/libitem` is the one validator:
  canonical bytes, a closed schema (an unknown or wrongly cased key is refused),
  bounded sizes and a name that cannot name a path, reusing the loader's and the
  settings validators rather than copying them. `internal/libstore` is the one
  writer: it never replaces an item without the request's say-so, never writes
  through a symbolic link, and writes atomically at `0600`/`0700`. A skill or a
  prompt is untrusted text and is refused, not repaired, when
  `libstore.untrustedGate` finds delimiter markup, a control or escape character,
  a bidirectional override or an invisible rune, so what is stored is what the
  library holds. Each kind has its own `sync.<kinds>` switch (a project layer may
  turn it off, never on): off means nothing is hashed, advertised or sent for it
  and its requests are refused. A refusal reason is harness text and never the
  document, only the user's global layers are read or written, and a host never
  advertises more than kind, name and hash. A structured process is an argv run
  with no shell: a secret-looking `env` name takes only `env:OTHER` (the value is
  copied from the host at start, an unset one refuses the start), `user` is
  honoured only when Belai is root and never falls back to the current user, a
  redirect file is `0600` and never opened through a link, the sandbox's writable
  roots stay the project directory whatever `cwd` says, and the recovery subagent
  restarts it exactly as defined, never amended. A repository item is configuration only: `belai repo sync` runs
  git as an argv with hooks off, no prompts and only the https and ssh transports,
  never puts a token in a URL or an argument (a private https repository goes
  through the GitHub CLI's credential helper or fails with that reason), moves a
  branch by fast-forward only, refuses a dirty tracked tree or a diverged branch
  instead of resetting, never touches an untracked file, and never follows a
  symbolic link in the checkout path; the project layer's `repos` is dropped. A budget set and the Bash rewrite table are the host's one
  configuration of their kind: an install over an existing one needs the request's
  replace flag, writes only the user's global settings file (atomically, keeping
  every other key), and runs Belai's own validators (`ValidateTokenBudgets`,
  `ValidateBashRewrite`) before anything is written, so a document the library
  accepts is never written as a setting Belai refuses to load. A provider set holds no key (the schema refuses one):
  keys reach a host only through `provider_keys_install`, which the library answers
  once, over TLS, for the slugs the request names; the host checks each key (not
  empty, at most 4096 bytes, no control character), stores it only in the credentials
  resolver under the provider's own name (`credentials.NewGlobalResolver`), never in
  settings, a log, an acknowledgement, an audit event, a session record or an error,
  and `sessionsync.ProviderKey` redacts itself through every `fmt` verb and JSON.
- **A web avatar is a tool-less main-model turn, and `internal/svgguard` admits
  what comes back.** An `avatar` request names an agent creator by id and is
  accepted only while `sync.remote_prompts` is on, one drawing at a time. The
  display name, colours and personality are the website's text: cleaned to capped
  lines, admitted through the security classifier under the effective posture,
  and sent to the model only as labelled data under a system text that is the
  harness's own. The reply is parsed against a closed list of drawing elements
  and attributes; any element, attribute, reference, entity, comment or size
  outside the list refuses the whole image, nothing is repaired, and the bytes
  posted back are the guard's own serialisation. A refusal is a harness-worded
  reason, never model or provider text. The image is never read back into a model
  and is rendered only as an image; the server and the website admit it again.
  Never add an element to the list that can run code or fetch.
  A built-in agent's avatar is a drawing shipped with the profile
  (`internal/agentprofile/builtin/avatars`), never one made at run time, and
  `TestPersonaAvatarsAreAdmitted` holds each to the same guard, unchanged.
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
