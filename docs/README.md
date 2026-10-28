# Belai documentation

This directory is the technical reference for Belai. Start with the
[user guide](../README.md) for installation, configuration, and everyday use;
then follow the links below for the security model, operating modes, retry
behaviour, and implementation details.

## Learn Belai

| Topic | Guide | Key sections |
| --- | --- | --- |
| Installation, providers, TUI, CLI, settings, and sessions | [User guide](../README.md) | [Installation](../README.md#installation), [Usage](../README.md#usage), [Configuration](../README.md#configuration), [Settings files](../README.md#settings-files) |
| Security architecture and trust boundaries | [Architecture](architecture.md) | [Delimiter, nonce, and integrity model](architecture.md#delimiter-nonce-and-integrity-model), [Tool-result trust](architecture.md#tool-result-trust), [TUI](architecture.md#tui) |
| Deterministic input cleaning per destination: text, decision state, paths, URLs, headers, shell commands, tool arguments | [Sanitisation](sanitization.md) | [The sinks](sanitization.md#the-sinks), [Shell commands](sanitization.md#shell-commands-shellsafe), [URLs](sanitization.md#urls-netguard), [Tool arguments](sanitization.md#tool-arguments-checkargs-and-format) |
| Relevance jobs that use a decision backend (Jev): scoring, thresholds, settings, and running a builtin tool in place of Bash | [Jev jobs](jev-jobs.md) | [The contract](jev-jobs.md#the-contract-every-job-keeps), [Scores and thresholds](jev-jobs.md#scores-and-thresholds), [Bash swap](jev-jobs.md#bash-swap), [Option order](jev-jobs.md#option-order), [Compaction prune](jev-jobs.md#compaction-prune), [Tool and skill selection](jev-jobs.md#tool-and-skill-selection), [Tool search](jev-jobs.md#tool-search), [LSP triage](jev-jobs.md#lsp-triage) |
| Classification, posture gates, permissions, and mode decisions | [Role Manager](role-manager.md) | [Security classification](role-manager.md#security-classification), [Gates and defaults](role-manager.md#gates-and-defaults), [Operating-mode classification](role-manager.md#operating-mode-classification) |
| Prompt to policy: permissions you state in a prompt become AI Firewall guardrails, offered on a card and applied on a click | [Firewall permissions](firewall-permissions.md) | [When it runs](firewall-permissions.md#when-it-runs), [How a rule is written](firewall-permissions.md#how-a-rule-is-written), [Allow and deny variants](firewall-permissions.md#allow-and-deny-variants), [The console's Permissions tab](firewall-permissions.md#the-consoles-permissions-tab), [Settings](firewall-permissions.md#settings) |
| Mode, model, guardrails, ask, display and other per-session switches as slash commands and keys, in the TUI and from the web | [Session controls](session-controls.md) | [What a control changes](session-controls.md#what-a-control-changes) |
| Language-server diagnostics | [LSP](lsp.md) | [Supported languages](lsp.md#supported-languages), [Security model](lsp.md#security-model), [Settings](lsp.md#settings) |
| The settings, language server and permissions screens: groups, keys, where a change is saved, and the edge cases | [Settings screens](settings.md) | [Layout](settings.md#layout), [Groups](settings.md#groups), [Keys](settings.md#keys), [Business rules](settings.md#business-rules), [Edge cases](settings.md#edge-cases) |
| Provider retries and recovery | [Resilience](resilience.md) | [Error classification](resilience.md#error-classification-internalresilience), [Turn retry and state invariants](resilience.md#turn-retry-and-state-invariants), [Semantic repair](resilience.md#semantic-repair) |
| Token budgets per provider and model, and session intelligence (plan limits, pace, trend, runway) | [Token budgets](token-budgets.md) | [Settings](token-budgets.md#settings), [Business rules](token-budgets.md#business-rules), [Edge cases](token-budgets.md#edge-cases), [Session intelligence](token-budgets.md#session-intelligence) |
| Test-suite detection, what the model is told about them, and the post-end test pass (report on a pass, diagnose and fix on a failure) | [Testing](testing.md) | [Detection](testing.md#detection), [Settings](testing.md#settings), [When the pass runs](testing.md#when-the-pass-runs), [Failure: diagnose and fix](testing.md#failure-diagnose-and-fix), [Surfaces](testing.md#surfaces), [Edge cases](testing.md#edge-cases) |
| Linter detection from checked-in rule files, the lint sweep that files `lint-drift` cards, and lint gates | [Lint](lint.md) | [Detection](lint.md#detection), [The lint sweep](lint.md#the-lint-sweep), [Lint gates](lint.md#lint-gates), [Fixing drift](lint.md#fixing-drift) |
| Resolving a pull request's merge conflicts in up to five fresh checkouts that never repeat a variation, with build, lint and test checks before a fast-forward push | [Merge conflicts](merge-conflicts.md) | [The loop](merge-conflicts.md#the-loop), [Strategies](merge-conflicts.md#strategies), [Checks](merge-conflicts.md#checks), [The ledger](merge-conflicts.md#the-ledger), [Pushing](merge-conflicts.md#pushing) |
| Keeping tool output out of the context window | [Context offload](context-offload.md) | [Offloading oversized results](context-offload.md#offloading-oversized-results), [WebFetch answers the question](context-offload.md#webfetch-answers-the-question), [Settings](context-offload.md#settings) |
| Caching fetched web pages and searching them later without fetching again | [WebFetch cache and search](web-fetch.md) | [The cache](web-fetch.md#the-cache), [Searching what was fetched](web-fetch.md#searching-what-was-fetched), [Settings](web-fetch.md#settings) |
| Measuring token cost and accuracy | [Benchmarks](benchmarks.md) | [What a run records](benchmarks.md#what-a-run-records), [Running a benchmark](benchmarks.md#running-a-benchmark), [Method](benchmarks.md#method) |

## Agents

- [Agent Profiles](agent-profiles.md): reusable foreground and background agent
  definitions, schema, lifecycle, autonomy, the profile builder, and
  [facts](agent-profiles.md#facts) that point the cloud tools at an account, a
  cluster or a Terraform directory.
- [Importing from other harnesses](agent-import.md): `belai library scan` searches a
  host for commands, skills, prompts, agents, crews, documents and Belai's own
  settings kept by other agent harnesses and says what an import would make of
  each; `belai agent import` converts one agent definition into a profile.
- [Harness registry](harnesses.md): where Claude Code, Cursor, Codex and about 80
  other harnesses keep commands, skills, prompts, agents and instruction
  documents, and how the registry is generated.
- [Agent Stores](agent-stores.md): read-only search across Belai and other
  agents' session, prompt, and memory stores, including attribution and
  confinement guarantees.
- [A2A messaging](a2a.md): agents broadcast what they work on, message each
  other and answer in a side-turn, keep repo memory (`RepoMemoryRead`,
  `RepoMemoryWrite`), and the A2A blackout that stops relay between hosts.
- [Forges](forges.md): github.com, GitHub Enterprise Server, Gitea, Forgejo and
  GitLab: how a remote's forge is found, the `forges` setting, tokens, and what
  works on each.
- [Kanban](kanban.md): the global board, its lists and tools, the TUI pane and
  how it syncs with the website.
- [The BKAN file format](bkan.md): how the board file is built byte by byte, the
  types it holds, and how it is read and written safely.

## Integrations and protocols

- [Kiro](kiro.md): the `kiro` provider, AWS Builder ID and IAM Identity
  Center sign-in, token refresh, and the Kiro wire surface.
- [Vulnetix code review](vulnetix-review.md): `/vulnetix review`, what it runs and
  costs, the report it writes, its settings and troubleshooting.
- [Vulnetix](vulnetix.md): review scanners, artifact handling, project history,
  and the Vulnetix AI Firewall.
- [AI Firewall](firewall.md): the adapter-based firewall (Vulnetix, beta
  Fastly ARC, Kong and AI Security Gateway, custom proxies, read-only
  OpenRouter and Cloudflare), modes, key storage and event cards.
- [Firewall permissions](firewall-permissions.md): permissions stated in a
  prompt, offered as AI Firewall guardrail rules on a card you apply.
- [Turn latency](performance.md): what runs before the first token and between
  passes, what is classified of a file, and what stays serial on purpose.
- [Bot attribution trailers](bot-attribution.md): the `Co-authored-by` and
  `Signed-off-by: Pix [bot]` trailers Belai adds to commits inside a Pix sandbox.
- [Git sync](git-sync.md): rebasing a session's branch onto upstream main before
  its turns, turning it off per session or globally, and the git state the
  website shows
- [Session sync](session-sync.md): mirroring sessions to the Vulnetix website
- [Session measures](session-measures.md): the cache tokens, cost, `check_run`
  and `commit` entries and the finish rollup a session records for the
  Software Factory
- [Audit log](audit.md): the hash-chained record of host and agent actions, facts only
  (History and live Sessions) and prompting a live session from the browser.
- [Remote control](remote-control.md): `belai rc`, which lets the website
  start and drive sessions on this machine.
- [Teleport](teleport.md): `belai -teleport`, which continues a session from another
  host, sandbox or the web on this host, under a new id.
- [Pix Sandbox](pix-sandbox.md): the contract between Belai and the hosted machine Vulnetix
  runs: host id, settings, credentials, trusted directories and Jev.
- [Vault environment variables](vault-env.md): the environment variables a Pix sandbox gets
  from the organisation's secrets vault, how they reach a tool call, and how they are
  scrubbed from output, transcripts and logs.
- [Library items](library-items.md): the skills, prompts, processes, repositories, token
  budgets, Bash rewrite table and provider sets the website's library keeps for an
  account, and how provider keys reach a host: formats, canonical bytes, limits, install
  and sync rules, and `belai skill|prompt|process|repo|budget|rewrite|provider import|export`.
- [Nonce endpoint spec](nonce-endpoint-spec.md): the provider/gateway
  `GET /v1/nonces` contract and verification semantics.
- [Screenshots](screenshots.md): the `Screenshot` tool, which captures a
  loopback page or the desktop for the model.
- [Images](image-attachments.md): how a tool-returned image is admitted and
  sent to each provider, plus the deferred attached-image design and candidate
  terminal-rendering approaches.

## Design

- [TUI design system](tui-design.md): colour roles, glyphs, rhythm and the
  surfaces they apply to.

## Roadmap

Features are documented here before they are built. A row reads `Roadmap`
until the feature ships, then `alpha-YYYYMMDD`, the date it landed.

| Feature | Guide | Status |
| --- | --- | --- |
| Hook events with allow and deny decisions | [Hooks](hooks.md) | alpha-20260926 |
| Desktop notifications | [Notifications](notifications.md) | alpha-20260926 |
| Skill loading and self-authored skills | [Skills](skills.md) | alpha-20260926 |
| Importing agent definitions | [Agent import](agent-import.md) | alpha-20261006 |
| Harness registry (where other harnesses keep their files) | [Harness registry](harnesses.md) | alpha-20261007 |
| Plugin packages | [Plugins](plugins.md) | alpha-20260926 |
| OS sandbox for Bash | [Sandbox](sandbox.md) | alpha-20260926 |
| MCP client | [MCP servers](mcp.md) | alpha-20260926 |
| Code mode | [Code mode](code-mode.md) | alpha |
| Editor integration over ACP | [ACP](acp.md) | alpha-20260926 |
| ACP setup in Zed | [Zed](acp-zed.md) | alpha-20260926 |
| ACP setup in JetBrains IDEs | [JetBrains IDEs](acp-jetbrains.md) | alpha-20260926 |
| ACP setup in Neovim | [Neovim](acp-neovim.md) | alpha-20260926 |
| ACP setup in Emacs | [Emacs](acp-emacs.md) | alpha-20260926 |
| ACP setup in VS Code | [VS Code](acp-vscode.md) | alpha-20260926 |
| OpenTelemetry export | [Telemetry](telemetry.md) | alpha-20260926 |
| Quiet TUI redesign | [TUI design system](tui-design.md) | alpha-20260927 (in part) |
| Autonomous kanban agent fleet | [Agent fleet](fleet.md) | alpha-20260928 |
| Remote control from the website | [Remote control](remote-control.md) | alpha-20260930 |
| Continuing a session from another host, sandbox or the web | [Teleport](teleport.md) | alpha-20261004 |
| Library items: skills, prompts, processes, repositories, budgets, the rewrite table and provider sets synced with the website | [Library items](library-items.md) | alpha-20261002 |
| Voice input for the composer | [Voice input](voice.md) | alpha-20260930 |
| Reading replies aloud, with a player card | [Read aloud](tts.md) | alpha-20260930 |
| Rewriting a model's Bash command word before permission matching | [Bash rewrite](bash-rewrite.md) | alpha-20261002 |
| Searching profile documents and project scanner output by meaning | [Knowledge](knowledge.md) | alpha-20261001 |
| A row for each vulnerability identifier, with a console link and a triage launch | [Vulnerability row](vuln-row.md) | alpha-20261002 |
| Reviewing the working tree's staged, unstaged and untracked changes read-only | [Workspace changes (`/diff`)](diff.md) | alpha-20261001 |

## Analysis

- [Git directories analytics](git-directories-analytics.md): how Belai finds the
  repository, the layout and language summaries, nearby checkouts and the
  directories offered for remote control, with their bounds.
- [Session assessment](session-assessment.md): what saved sessions showed about
  orchestration, prompts and UX, and the changes made in response.
- [Plan mode tuning](plan-mode-tuning.md): the scoring rubric, the test matrix and
  one row per measured run for the planning prompt and the plan loop, with the
  revision that changed each result.

## Build, test, and publish

- [Development](development.md): prerequisites, source-running commands, QA
  checklist, tests, versioning, CI, release, and the marketing site.
- [Marketing site](site.md): the Astro site under `site/`, the sealed block
  device, shot captures, Terraform and deploy.
- The public documentation starts at this index. Repository contributors
  should also read [AGENTS.md](../AGENTS.md) before changing security
  invariants.

## Topic map

- **Safety first:** [trust model](role-manager.md#trust-model) →
  [pipeline](role-manager.md#pipeline) →
  [posture gates](role-manager.md#gates-and-defaults) →
  [tool-result trust](architecture.md#tool-result-trust).
- **Choose autonomy deliberately:** [mode classification](role-manager.md#operating-mode-classification) →
  [plan pass loop](role-manager.md#plan-pass-loop) or
  [goal pass loop](role-manager.md#goal-pass-loop).
- **Run an agent safely:** [profile schema](agent-profiles.md#profile-schema) →
  [background lifecycle](agent-profiles.md#background-agent-lifecycle) →
  [precedence](agent-profiles.md#per-agent-defaults-and-precedence).
- **Recover from provider trouble:** [classification](resilience.md#error-classification-internalresilience) →
  [transport retry](resilience.md#pre-first-byte-boundary) →
  [turn retry](resilience.md#turn-retry-and-state-invariants).
- **Integrate Vulnetix:** [code review](vulnetix-review.md) →
  [business rules](vulnetix.md#business-rules) →
  [command surface](vulnetix.md#command-surface) →
  [AI Firewall](vulnetix.md#ai-firewall-vulnetix-firewall-and-f10).
