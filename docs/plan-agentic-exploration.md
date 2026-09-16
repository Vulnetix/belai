# Plan: Agentic Exploration in Plan Mode

## Objective

Make Signet's plan-mode *explore* phase genuinely agentic: instead of asking
the user to clarify an ambiguous prompt almost immediately, Signet should
first **investigate the workspace on its own using native, read-only tools**.
The user's clarify questionnaire should only appear when exploration has
produced findings and the model decides there are still decisions only the
user can make.

This plan also upgrades the tool layer from a raw "read-only Bash" allowlist
into a **library of first-class native tools** with hardened execution,
beautiful TUI UX, and capability detection for local/cloud/SaaS binaries.

## Current pain

1. `explore.Plan` runs with no actual tool budget. It turns `@file`
   references into subagent tasks, but those subagents only call `Read` — they do
   not search the repo, inspect git state, or run `rg`/`grep`/`jq`.
2. The read-only Bash tool is limited by `ShellMetacharacters`: no pipes,
   no redirections, no `$` (so `awk '{print $1}'` fails), and no proper quote
   handling. Many useful one-liners are impossible.
3. Clarify fires **after** the first explore wave, and the first wave often
   returns zero findings. The questionnaire therefore appears before the model
   has enough context to ask meaningful questions.
4. The explore wave has a hard cap of `MaxIterations=4` per subagent, but
   there is no concept of "keep exploring until the user steers".
5. Cloud/SaaS CLIs (`gh`, `aws`, `az`, `gcloud`, etc.) are not exposed as
   tools at all, even though many workspaces contain deployment configs,
   issues, PRs, and resources the user wants to reason about.

## Target state

When the user sends a plan-mode prompt, Signet should:

1. Run a short, fully automatic **grounding probe** (git state, repo layout,
   `AGENTS.md`, relevant known background agents) and attach findings as
   untrusted evidence.
2. Dispatch one or more **explore subagents**. Each subagent sees the
   original prompt + grounding evidence + a catalogue of native read-only
   tools, and **uses tools to investigate** rather than ask questions.
3. Each explore subagent iterates up to a bounded number of tool calls. The
   budget is deeper than today (at least 8–10 calls) and the subagent is
   expected to actually run commands like `rg`, `find`, `jq`, `cat`.
4. Explore subagents may run **in parallel**. If the user's prompt decomposes
   into several independent investigations, Signet can launch several agents
   with different angles.
5. The model is asked, **after exploration**, whether it still needs user
   clarification. The clarify loop is entered only when:
   - exploration produced findings, AND
   - the model can articulate a concrete question only the user can answer.
6. Network-reachable tools (`gh`, `aws`, cloud CLIs, web search/fetch) are
   exposed as native tools only when capability detection finds the CLI installed
   and configured. Each carries vendor-aware colour and a TUI card that fits the
   tool's output style (JSON, tabular, diff, log tail, etc.).

## High-level design

### 1. Native tool catalogue

Replace the growing read-only Bash allowlist with a **tool catalogue** of
first-class tools. Each entry in the catalogue is a Go `tools.Tool` whose
`Definition()` is sent to the model the same way `Read` or `Bash` is today.

The catalogue has two categories:

| Category | Members | Notes |
|----------|---------|-------|
| **Local utilities** | `awk`, `sed`, `rg`, `jq`, `cat`, `head`, `tail`, `less`, `find`, `cut`, `tr`, `yq`, `xsv`, `uniq`, `git`, `gh`?, `ls`, `file`, `strings`, `pwd`, `diff`, `cmp`, `sort`, `env`, `wc`, `paste`, `join`, `date`, `echo` | Read-only by construction. Each gets its own schema and sanitiser. |
| **Cloud / SaaS CLIs** | `aws`, `az`, `gcloud`, `doctl`, `linode-cli`, `flyctl`, `vercel`, `netlify`, `heroku`, `eksctl`, `kubectl`, `terraform`, `pulumi`, `stripe`, `twilio`, `1password`, `bitwarden`, `gh`, `glab`, `azure-devops`, `circleci`, `github` | Capability-detected at session init. Only read-only subcommands are offered. |

Why not keep the Bash allowlist? The Bash tool runs without a shell in plan
mode. That means no pipes, no `$` variables, no redirection, no proper quote
handling. `rg x | head -50` is literally impossible. A first-class `rg` tool
can expose `pattern`, `path`, `max_results`, `glob`, and `context_lines` and
internally call `rg` safely. A first-class `jq` tool can accept a JSON blob
and expression and run `jq --argfile /dev/stdin` safely. Each tool becomes a
known, sanitised, renderable unit.

#### Security rules for native tools

- Every native tool is **read-only by construction**: it shells out to a
  specific command with a fixed argument shape. Arbitrary command strings are
  rejected.
- Arguments are passed as Go `string` and `[]string` slices, not through a
  shell. Use `exec.Command(name, args...)` directly.
- Path arguments are run through `tools.SanitizePath` so they cannot escape the
  working directory.
- For tools that accept a query language (`jq`, `yq`, `awk`, `sed`), the query
  is the *data* the model provides. It is still shell-metacharacter-gated
  before being passed to the binary, and never interpolated into `-c` of a
  shell.
- Output is capped (64 KiB by default) and run through the normal classifier
  pipeline as untrusted content.

### 2. Capability detection

At session construction (`agent.NewSession` or `tui` init), Signet probes:

- Which local utilities exist in `$PATH`.
- Which cloud/SaaS CLIs exist in `$PATH`.
- For network CLIs, whether a minimal no-op authentication check succeeds
  (e.g. `aws sts get-caller-identity`, `gh auth status`, `gcloud config get-value account`).

The result is a `tools.Capabilities` struct stored in the session. The registry
is built from capabilities + the plan-mode read-only switch. Detected tools
get native schemas; undetected tools are absent, so the model cannot call them.

Capability detection must be **cheap and non-mutating**. Only read-only probe
commands may run. Every probe command has a short timeout. Failure to detect
a tool is silent — the tool simply is not offered.

### 3. Tool-native TUI UX

Each native tool renders its tool-call card and result in a way that matches
its output.

| Tool | UX idea |
|------|---------|
| `rg` / `grep` | Result list with filename/line/marked match. Keyboard shortcut to open file at line. |
| `find` | Collapsible tree of paths. |
| `jq` / `yq` | Collapsible JSON/YAML tree with path highlights. |
| `git log` / `git status` / `git diff` | Existing diff/log renderers reused. |
| `aws` / `gh` / cloud | Vendor-coloured header, tabular output, truncation notice. |
| `awk` / `sed` / `cut` | Plain text block with preview of input and output. |

Tool cards should use the vendor colour when known (e.g. AWS orange, GitHub
purple, Azure blue) or the standard tool colour otherwise. This is purely
cosmetic but orients the user.

### 4. Explore agent loop

Create a new internal package `internal/explore/agent.go` (or extend
`internal/agent/explore.go`) with an **ExploreSubagent** type.

Each explore subagent:

- Receives: original prompt, user-selected references, grounding evidence,
  and an investigation angle.
- Has its own `agent.Session` with:
  - `PlanMode: true`
  - `AllowExplore: true` — the subagent may continue exploring on its own.
  - `MaxIterations: N` where `N` is larger than today (default 8, configurable via
    `resilience.max_explore_iterations`).
- Runs the bounded tool loop until either:
  - it produces a final text finding,
  - it exhausts its iteration budget,
  - it emits a special "request user clarification" signal that the parent
    notices.

The parent (`agent.Session.run`) collects all subagent findings, classifies
and seals them, and only then asks the main model whether clarification is
needed.

Parallelism is bounded by `explore.Concurrency` (keep today's semaphore) and
`MaxTasks` limits.

### 5. Reset-on-steer iterations

Introduce a distinction between **inner iterations** and **steering resets**.

- `MaxIterations` counts how many tool calls a single explore subagent may
  perform **without a steering message from the user**.
- If the user sends a steering message while explore is running, the loop
  restarts the iteration budget for the affected subagents.
- This lets the model say "explore more" or "now check X" and have the
  subagent continue, while still preventing runaway loops.

Implementation sketch: in `passLoop`, instead of returning
`max iterations reached` immediately when the budget is exhausted, check
whether this is an explore subagent and whether new steering exists. If so,
reset the counter and continue; otherwise return the error.

### 6. Grounding probe

Before launching explore subagents, signet should attach a small set of
**always-useful** evidence:

- `git status --short` + current branch + recent commits (safe read-only).
- Bounded top-level directory listing (e.g. `ls -la` capped to 200 entries).
- `AGENTS.md` content (read + classify).
- List of available user background agents from `.vulnetix/agents/` or equivalent
  that are *not* scheduled/loop-based, filtered heuristically to those likely
  relevant to the prompt.

This evidence is treated as untrusted and classified before being sealed as
`<attachment>` or `<exploration>` blocks.

### 7. Clarify gating

Change the clarify trigger from the naive "after first explore wave" to a
**conditional classifier call**.

After explore findings are gathered, build a classifier payload:

- System prompt: "You are a planner. Given the user's original prompt and the
  read-only exploration findings below, decide whether the user still needs
  to answer a clarification questionnaire. If you can proceed without the
  user, return {"proceed": true}. If you need user input, return a JSON
  questionnaire."
- User content: original prompt + sanitized findings digest.

The clarify loop is entered only if the classifier returns a non-empty
questionnaire. Empty questionnaire, `proceed: true`, or validation failure all
mean *continue to planning with current evidence*.

We keep `resilience.max_clarify_rounds` semantics; rounds now follow *real*
exploration findings, not zero-findings waves.

### 8. System prompt guidance

Add a dedicated system prompt section for plan-mode explore subagents:

> "You are in plan-mode exploration. You may use the read-only tools listed
> below to investigate the repository. Prefer to discover facts yourself with
> `rg`, `find`, `git`, `jq`, `cat`, `head`, `tail` and the other provided
> tools. Only ask the user a clarifying question when you have exhausted the
> available evidence and the decision genuinely requires user judgment."

Also include a short "grounding" preamble with the repo state.

## Files to change

| File | Change |
|------|--------|
| `internal/tools/catalog.go` | New file. Native tool definitions for local utilities and cloud/SaaS CLIs. |
| `internal/tools/capabilities.go` | Capability detection: probe `$PATH` and auth state; build a registry subset. |
| `internal/tools/bash.go` | Keep read-only Bash for truly arbitrary safe commands, but shrink its role. |
| `internal/explore/agent.go` | New explore subagent loop with reset-on-steer semantics. |
| `internal/explore/plan.go` | Add grounding-task generation, richer task planning. |
| `internal/agent/explore.go` | Wire explore subagent launch, collect findings, gate clarify. |
| `internal/agent/agent.go` | Accept capabilities/registry from caller; pass into explore. |
| `internal/agent/passloop.go` | Implement reset-on-steer for explore subagents. |
| `internal/tui/` | Tool-native rendering cards, vendor colours, capability-aware registry wiring. |
| `internal/config/settings.go` | Add `max_explore_iterations` under resilience settings. |
| `internal/rolemanager/clarify.go` | New "should we clarify?" classifier prompt. |
| `docs/role-manager.md` | Update clarify loop, tool trust model, capability detection. |
| `docs/architecture.md` | Document native tool catalogue and explore subagent design. |

## Verification plan

1. Unit tests:
   - `tools/catalog_test.go`: each native tool rejects mutations, escapes paths,
     and caps output.
   - `tools/capabilities_test.go`: mock `$PATH` entries produce correct capability
     set; missing tools are absent.
   - `explore/agent_test.go`: subagent exhausts budget, restarts on steering,
     returns findings.
2. E2E:
   - `just e2e` plus a new plan-mode test prompt with an ambiguous request but a
     file reference. Confirm the clarify questionnaire does **not** appear until
     after tool output is visible in the transcript.
   - Mock provider sequence that expects `rg`/`find`/`git` tool calls and returns
     synthetic results; assert clarify only fires when findings are present.
3. Manual QA (from `docs/development.md`):
   - In the TUI, shift+tab to plan mode, send "what is the security boundary
     around tool results?". Confirm `rg`, `git`, `cat`, etc. are called before
     any questionnaire.
   - With `gh` installed, send "what open PRs relate to @internal/rolemanager".
   - Confirm `gh` tool uses vendor colour and produces a tabular PR card.

## Open questions

1. Should the explore subagent share the main model or use the classifier
   provider to save cost? Today subagents use the main model; with deeper budgets
   this becomes expensive. A separate `explore-provider` setting may be needed.
2. Do we want a sandbox timeout/cgroup per native tool beyond the command-level
   timeout? The current Bash timeout is 30s; native tools should inherit that.
3. How should the TUI show parallel subagents? A single "Exploring…" spinner
   or per-subagent progress strips? Start with one aggregate spinner plus a
   collapsible log.

## Outcome

When this plan is implemented, plan-mode prompts will feel meaningfully
agentic: Signet will investigate first, ask only when necessary, and expose a
rich set of safe, native tools with first-class TUI support.
