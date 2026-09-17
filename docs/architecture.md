# Signet architecture

Signet is a role-managed, injection-safe LLM coding harness with a Codex-style
terminal UI. This document describes its security model and the modes it
exposes.

## Role Manager

The Role Manager is Signet's central safety boundary: security classification,
the sanitize → classify → decision pipeline, system/agent block boundaries,
tool-call and permission invariants, skill/hook validation, and operating-mode
auto-detection.

Its full business rules — sentinel tables, decision trees, and flow diagrams —
live in [role-manager.md](role-manager.md).

### Security classifier configuration

The security classifier is configured independently of the main agent model
through a `classifier` settings block (resolved by the standard precedence
chain: default < state < global < project < env < flag):

```jsonc
"classifier": {
  "provider": "ollama",          // omit → main provider
  "model":    "qwen2.5-7b-instruct-q4_k_m",
  "effort":   "none",             // default: reasoning OFF
  "chunk": { "max_bytes": 1048576, "concurrency": 4 }
}
```

Flags `-classifier-provider`, `-classifier-model`, `-classifier-effort`, and
env vars `SIGNET_CLASSIFIER_PROVIDER/MODEL/EFFORT` set the same fields.

Business rules:

- **Default** (no block): the classifier reuses the main provider/model with
  reasoning off and a bounded `max_tokens` cap (1024), so a single-sentinel
  call never pays for extended thinking. A reasoning-effort of `"none"` is
  *omitted* from the OpenAI `reasoning_effort` field rather than sent verbatim
  (OpenAI rejects it).
- **Separate provider**: a `classifier.provider` that differs from the main
  provider is resolved through the same credential backends with its own
  credentials. Missing credentials fail closed with `ErrNotConfigured`.
- **Chunked classify-all**: content over `chunk.max_bytes` is split into
  overlapping chunks (default 1/8 overlap, aligned to rune boundaries) and
  classified concurrently (default 4). Verdicts fold fail-closed: any non-SAFE
  sentinel fails the whole content, and any malformed chunk makes the whole
  result malformed. Overlap guarantees an injection straddling a boundary is
  seen whole by at least one chunk.
- **Empty content**: content that is empty or whitespace-only after
  sanitization is SAFE without a classifier call. It carries nothing to
  classify — a shell command that printed nothing cannot hold an injection —
  and the round trip both costs latency per silent command and sends a user
  message with no content field, which OpenAI-compatible servers reject.
- **Verdict cache**: verdicts are memoised by the SHA-256 of the *sanitized*
  content. SAFE verdicts live in a bounded session LRU (512); non-SAFE hashes
  persist to `<GlobalDir>/bad-hashes.json` (written atomically) and load at
  session start. The bad-hash set stays small: memory is bounded and I/O is
  one read at startup plus an append per new bad verdict.

## Delimiter, nonce, and integrity model

Harness-generated blocks use tags such as:

```
<system nonce="…" integrity="…">content</system>
```

- **Nonce**: a 128-bit CSPRNG value from the nonce pool
  (`internal/nonce`). Only *reserved* nonces are valid; the pool supports
  reserve/release/rotate and falls back to local generation.
- **Known kinds**: `system`, `agent`, `plan`, `goal`, `tools`, `skills`,
  `hooks`, `attachment` (for `@file` / `!shell` contents), `exploration`
  (explore-subagent findings), and `directive` (harness continuation
  instructions injected into a running loop).
- **Integrity**: `integrity` is the lowercase hex SHA-256 of the enclosed
  content.
- **Egress verification** (`internal/delimiters`): before any payload leaves
  for a model provider, every block is checked. A block lacking a nonce,
  carrying an unknown nonce, or failing its integrity hash is stripped.
- **Sanitization** (`internal/sanitize`): untrusted Read/WebSearch/WebFetch
  output is stripped of any harness delimiter markup (and nonce/integrity
  attributes) *before* it is ever wrapped in a delimiter, so adversarial text
  cannot forge tags.

See [nonce-endpoint-spec.md](nonce-endpoint-spec.md) for the provider nonce GET
spec (`GET {base_url}/v1/nonces`).

## Resilience

Transient provider failures are recovered in layers: typed provider errors,
pre-first-byte transport retry (blocking and streaming), turn-level retry in
the agent loop, and semantic repair of malformed tool calls. The model is
documented in [resilience.md](resilience.md).

## Provider layer

`internal/provider` + `internal/wire` reach every provider through a base URL,
an API key, a wire surface, and an auth style — speaking the three
ai-firewall surfaces:

| Surface                 | Path                    | Auth header           |
| ----------------------- | ----------------------- | --------------------- |
| OpenAI chat             | `/chat/completions`     | `Authorization: Bearer` |
| OpenAI responses        | `/responses`            | `Authorization: Bearer` |
| Anthropic messages      | `/v1/messages`          | `x-api-key`           |

Anthropic base URLs carry no `/v1`; OpenAI-style base URLs do. Streaming and
non-streaming request/response shapes live in `internal/wire`.

Compiled-in providers are: `openai`, `anthropic`, `cloudflare-workers-ai`,
`cloudflare-ai-gateway`, `openrouter`, `google-gemini`, `ollama`, `llama`,
`github-copilot`, and `huggingface`. Custom provider profiles can speak any of
the three surfaces with `bearer`, `x-api-key`, or `cf-aig` auth.

Both `ollama` and `llama` are local providers that need no API key. `ollama`
speaks the Ollama native endpoint (default `http://localhost:11434/v1`) and
`llama` speaks a llama-server / llama.cpp OpenAI-compatible endpoint (default
`http://localhost:8080/v1`). Each resolves from a single `base_url` environment
variable (`OLLAMA_HOST` and `SIGNET_LLAMA_HOST` respectively) or from
individually-managed host, port, and protocol fields in `/credentials`.

## Modes

`internal/modes` defines three modes; agent is the default.

### Agent mode

Interactive default. Profiles (`internal/profiles`, stored under
`~/.vulnetix/signet/profiles/`) are selectable at startup and mid-session via
`/profile`. Built-in profiles live under the `signet:` namespace; the debug
profile (`signet:debug`) is automatically engaged for `!cmd` inline-shell
round-trips. User files cannot shadow a built-in name.

### Tool execution

Within one pass, tool calls execute in the order the model emitted them, with
one exception: the **leading run** of calls that are each parseable,
permission-allowed, and read-only runs concurrently, capped at 4 in flight —
unbounded fan-out against a rate-limited provider produces 429s, which is
worse than sequential. Anything else ends the run:

- a mutating kind — `Bash` (full mode), `Write`, or `Edit`: anything outside
  the read-only allowlist. A `Read` after a mutating tool that wrote the file
  must observe the write, so nothing reorders across a mutating call.
- A permission ask — two concurrent asks would race the UI, so the run stops
  at the first one and the tail runs sequentially with the ask.
- Malformed arguments or an unknown tool name — the call is answered
  (withheld) in order like the sequential tail.

Results match back to their transcript row by tool call id, so an
out-of-order completion from the concurrent group lands on its own row
rather than the newest tool row; legacy events without a call id fall back
to the last tool row.

### Native tool catalogue

`internal/tools/catalog.go` replaces the growing read-only Bash allowlist with
a library of first-class native tools. Each is a `tools.Native` whose
`Definition()` is sent to the model exactly like `Read` or `Bash`, but whose
execution is a **fixed command shape**, not an arbitrary command string:

- Local utilities — `Cat`, `Head`, `Tail`, `LS`, `Find`, `File`, `Strings`,
  `Git`, `JQ`, `YQ`, `Sed`, `Awk`, `Cut`, `Sort`, `Uniq`, `WC`, `Tr`,
  `Paste`, `Join`, `Echo`, `Date`, `Pwd`, `Env`, `Diff`, `Cmp` — are
  read-only by construction. `Grep` and `Glob` already existed and stay as
  their own kinds.
- Cloud/SaaS CLIs — `GH`, `AWS`, `AZ`, `GCloud`, `Kubectl`, `Terraform`,
  `Pulumi`, `Heroku`, `Fly`, `Vercel`, `Netlify`, `Doctl`, `Glab`, `Stripe`,
  `OnePassword`, `Bitwarden` — are offered only when capability detection
  finds the CLI installed **and** its read-only auth probe exits cleanly.
  Only read-only subcommands are offered: each CLI carries a prefix allowlist
  and anything outside it is rejected.

Security rules for native tools:

- Every tool is **read-only by construction** (`KindNative`), so it survives
  the read-only master switch and may run in the concurrent read-only fan-out.
- Arguments are Go `string`/`[]string` slices passed straight to
  `exec.Command(name, args...)` — never through a shell, so pipes,
  redirections, and `$` expansion are impossible.
- Path arguments go through `tools.SanitizePath` and cannot escape the
  working directory.
- Query-language arguments (`jq`/`yq` filters, `sed`/`awk` programs, `tr`
  sets) are data, not shell text; they are control-character gated and never
  interpolated into a shell `-c`.
- Output is capped (64 KiB default) and run through the normal classifier
  pipeline as untrusted content.

`internal/tools/capabilities.go` performs detection at session construction:
local utilities by `$PATH`, cloud CLIs by `$PATH` plus a short, read-only auth
probe (`aws sts get-caller-identity`, `gh auth status`, `gcloud config
  get-value account`, …). Detection is cheap, non-mutating, bounded (probes run
concurrently under one timeout), and silent: an unverifiable tool is simply
not offered. `tools.DefaultWithCaps` builds the registry from the detected
`tools.Capabilities`; `tools.Default` (no native tools) remains for the
profiles/permission-editor name lists.

### Write and Edit tools

`Write` and `Edit` are first-class mutating tools, alongside full-mode `Bash`.
Both confine paths to the workdir: `Write` resolves the deepest existing
ancestor and re-appends the new tail (`SanitizeNewPath`, so a file that does
not exist yet can still be written), while `Edit` requires the file to already
exist (`SanitizePath`). `Write` is bounded to 1 MiB, `Edit` to 1 MiB of file
content, and both write atomically (temp file + rename), so a failure never
leaves a half-written file. Results are terse confirmations —
`wrote src/x.go (412 bytes, 18 lines)` or `edited src/x.go (2 replacements)` —
and never echo file content back into the classifier round-trip.

`Edit` fails closed, in order: missing file; file over the size bound; binary
file (a NUL byte, matching `Read`); `old_string == new_string`; zero matches
(`old_string not found in <path>`); and multiple matches without
`replace_all=true` (`old_string appears N times in <path>; pass
replace_all=true or include more surrounding context to make it unique`). The
match is exact bytes with no whitespace or line-ending normalisation, and the
file is left byte-identical on every failure. This is deliberate: silent
normalisation is the classic source of "the edit landed somewhere else", so
the tool refuses rather than guess.

Every mutating call asks before it touches disk. In the TUI this is the
approval view (`viewPermissionAsk`): it shows the tool name, the normalised
subject path, and the diff the call would make (`filediff.Preview`, a pure
function over the row builder). Allow-once runs it, allow-always writes a
scoped `permissions.allow` rule first so the next matching call short-circuits,
and deny (or Esc) withholds without cancelling the turn. An explicit
`permissions.allow` rule skips the prompt; an explicit deny still blocks before
it. With no approver (the CLI, subagents) the existing
`permission_ask_no_tty` posture applies: enforce withholds naming
`-allow-ask-without-tty`, warn/ignore falls through to allow.

### Plan mode (read-only)

Mirrors Pi's plan-mode extension:

- Built-in edit/write tools (`Write`, `Edit`, and the denylisted family
  `apply_patch`/`patch`/…) are disabled; other tools remain active.
- `Bash` is registered by default and restricted to a read-only allowlist
  (`cat`, `grep`, `find`, `ls`, read-only `git` subcommands such as
  `status`/`log`/`diff`, `uname`, etc.).
  Mutating commands (`rm`, `mv`, `cp`, `mkdir`, `touch`, `git add/commit/push`,
  package installs, `sudo`/`kill`, editors) are blocked.
- Toggle via `/mode plan`, `Ctrl+Alt+P`, or `--plan`; `/todos` shows progress.
- After the agent emits a numbered plan under a `Plan:` header, the steps are
  extracted (`internal/plans/extract.go`) and the user is prompted with two
  options:
  - **Execute the plan** — `/execute` leaves plan mode (full tools restored);
    `[DONE:n]` markers advance the progress widget
    (`internal/plans/progress.go`).
  - **Refine the plan** — `/refine` opens the editor and sends the revision
    back as a user message while staying in plan mode.
- Plan-mode state (enabled/executing/todos) is persisted as session entries so
  it survives resume.

### Agentic exploration (plan-mode explore subagents)

When a plan-mode (or referenced goal-mode) prompt engages exploration, the
parent session runs a **grounding probe** first, then fans out **explore
subagents** that investigate with the native read-only tool catalogue before
any clarification questionnaire is shown.

The grounding probe (`internal/agent/grounding.go`) attaches always-useful,
read-only evidence: `git status`/branch/recent commits, a bounded top-level
directory listing, `AGENTS.md`, and the single-shot background agents that
are not scheduled/loop/monitor definitions. This evidence is untrusted — it
re-enters as part of each subagent prompt and is admitted through the Role
Manager like any user content, never promoted into a system/agent block.

Each explore subagent:

- receives the original prompt, the grounding evidence, and an investigation
  angle derived from the prompt's `@references` (or a codebase survey for
  goal mode);
- has its own read-only `agent.Session` (`PlanMode`, no further fan-out) with
  a dedicated iteration budget from `resilience.max_explore_iterations`
  (default 8, deeper than the historical 4), and a system-prompt preamble
  telling it to discover facts with the native tools rather than ask;
- runs read-only tools (`rg`/`Grep`, `find`/`Find`, `git`/`Git`, `cat`,
  `jq`, …) to investigate, returning a findings report;
- has its findings classified and, if SAFE, sealed as an `<exploration>`
  block that re-enters the parent as an untrusted user turn.

Subagents run in parallel bounded by `exploreConcurrency` (3) and
`explore.MaxTasks` (5). **Reset-on-steer**: an explore subagent that exhausts
its iteration budget does not hard-fail when new steering arrives — the
steering message is broadcast to the running subagents and each one restarts
its budget and keeps investigating. Only when no new steering exists does the
budget exhaustion surface.

Clarification is now **gated on findings**: the clarify loop runs only when
exploration produced non-empty findings and the planner classifier returns a
non-empty questionnaire. A zero-findings wave no longer triggers a
questionnaire.

### Goal mode

Goal-mode state is stored under `.vulnetix/goals/`. It is entered with
`/mode goal` or by a mode classifier routing the prompt to goal mode.

A goal-mode prompt at the top level runs a **pass loop** instead of a single
bounded tool loop: when a pass exhausts its iteration budget, a goal evaluator
decides whether the work advanced, and the loop continues while it does. The
loop is unbounded by design — it is stopped by a stall, not a counter — and
`esc` (or `SIGINT` outside the TUI) returns the partial result cleanly. The
normative rules, including the verification gate and every termination
condition, are in [role-manager.md](role-manager.md), "Goal pass loop".

A prompt the classifier routes to goal mode carries the prompt itself as the
goal carrier, so a goal-mode turn always has something to evaluate against.

Subagents never enter the pass loop: `AllowPassLoop` is a separate authority
from `AllowExplore` and only top-level session construction sets it, so an
unbounded loop can never spawn recursively.

### Todo list

`internal/todos` owns the single todo list a session tracks, whatever mode
produced it — goal mode, plan pursual, and agent loops all write into the same
structure, so the TUI has one thing to render and resume has one thing to
rehydrate.

- Items are 1-indexed and stable, because `[DONE:n]` markers refer to them.
  Exactly one not-done item is `active` at a time.
- `plans.ParseDoneMarkers` is the single definition of the marker syntax.
  Markers are applied **only** to model-authored assistant text; a `[DONE:1]`
  in a tool result or a repository file must never mark work complete.
- An empty list is not complete. "Nothing to do" is not "finished", and
  treating it as finished would let a goal loop stop before it wrote a plan.
- Lists persist as append-only `todo_list` session entries, latest wins — the
  same shape `modes.PlanState` uses. Completing a list does not delete it; a
  later entry supersedes it and the old one stays readable in the JSONL.

### Background agents

`internal/bgagent` runs named, reusable agents defined by `internal/agentprofile`
profiles in the background. Profiles are stored under
`~/.vulnetix/signet/profiles/agents/` and specify a system prompt, tool
allow-list, operating mode (`single`, `loop`, `scheduled`, `monitor`), and
autonomy level (`supervised` or `autonomous`).

The TUI integrates background agents via `/agent create`, `/agent edit`,
`/agent start`, `/agent pause`, `/agent resume`, `/agent stop`, `/agent list`,
and `/agent log`. `/agent list` discovers every stored profile, shows the
file path for each, and highlights running instances; pressing `e` opens an
editor where the profile's description, mode, schedule, monitor condition,
autonomy, max iterations, reflection, and system prompt can be changed. After
a successful `/agent create`, the new profile is selected and the editor is
opened automatically. Events stream into the main transcript as system lines
so the user's session is never blocked.

`loop` mode treats `max_iterations` as an *inner* budget: when it is exhausted,
an agent-loop evaluator decides whether to continue, pause, sleep one schedule
interval, or stop. Supervised profiles are paused rather than continued, and a
malformed or unreachable evaluator fails closed to pause. See
[agent-profiles.md](agent-profiles.md).

## Session store

`internal/session` persists append-only JSONL session trees
(`id` + `parentId`) under `~/.vulnetix/signet/sessions/<workdir>/<session>.jsonl`, with
fork/resume reads (full or partial UUID) and display names.

The TUI owns a live session: `App.sessionID` is minted at launch, entries are
appended lazily (no file until the first message), and the footer shows the
session name or short id. Entry types:

| Type | Role | Content |
| ---- | ---- | ------- |
| `user` | `user` | the prompt |
| `assistant` | `assistant` | the reply, with `prompt_tokens` / `completion_tokens` / `total_tokens` / `model` / `provider` in `meta` |
| `session_name` | *(empty)* | the name; append-only, latest wins, empty clears |
| `summary` | *(empty)* | a compaction summary; `meta.parent_session` links the source session |
| `todo_list` | *(empty)* | the tracked todo list as JSON; append-only, latest wins, `cleared` marks a superseded list |

`/compact` creates a **new** session whose root entry is the summary and links
the old id via `meta.parent_session`; the old file is never mutated, truncated,
or deleted. Naming is append-only: the last `session_name` entry wins. A
tracked todo list is re-appended under the new session id so the panel and the
new session file agree. `/clear` drops the list instead: it belongs to the
session that produced it.

## Credentials

`internal/credentials` implements layered credential resolution for the
built-in providers and any custom providers defined in `settings.json`. The
resolution order is:

1. **Environment** — preserves existing behaviour exactly.
2. **Project file** — `<workdir>/.vulnetix/signet/credentials.json`.
3. **User file** — `~/.vulnetix/signet/credentials.json`.
4. **`.netrc`** — read-only; never written by Signet.
5. **Host keychain** — via `zalando/go-keyring`, with a 5-second timeout so a
   locked D-Bus collection cannot block startup.

The user file may store inline secrets (JSON `{"source":"inline","value":"…"}`)
when the file mode is `0600` and the containing directory is `0700`. The
project file may store references (`{"source":"env","name":"OPENAI_API_KEY"}`)
but **never** inline secrets — a project file lives in whatever repository you
happen to `cd` into. A credential may be stored as the *name* of an
environment variable rather than a value, read at resolve time and never
written to disk; this is what keeps project credential files committable.

When `~/.signet` exists and `~/.vulnetix/signet` does not, `config.Migrate()`
moves the directory on first startup. A cross-filesystem fallback copies
recursively and leaves a `.migrated` marker; nothing is deleted.

The TUI credential manager (`/credentials`) shows provenance for every field,
accepts `s` to set a value, `e` to set an env reference, `c` to clear, `b` to
cycle the write backend, and `i` to open the import screen. The default write
backend is the keychain when available, otherwise the user file with an
explicit confirmation.

### Custom providers

`settings.json` may carry a `providers` block mapping a name to a profile with
`base_url`, `api` (one of `openai-chat`, `openai-responses`,
`anthropic-messages`), optional `auth` (`bearer`, `x-api-key`, `cf-aig`),
optional `api_key_env`, and a `models` catalogue. Secrets never live there.
Profiles merge key-by-key across layers, and a project-layer `providers` block
is ignored unless the global settings opt in with
`allow_project_providers: true`.

### Discovery

`internal/agentscan` is read-only credential discovery. It runs only on an
explicit key press (`i` inside `/credentials`), never at startup. It opens a
fixed list of paths for Pi, Codex, Claude Code, Goose, OpenCode, Copilot,
Gemini, Qwen, Crush, and Aider, caps every read at 1 MiB, honours
`XDG_CONFIG_HOME`/`XDG_DATA_HOME`, never follows symlinks out of home, and
never executes anything. Discovered secrets render masked and are never logged;
agents installed but holding no importable key are listed with the reason.

## System prompt

`internal/prompt` assembles the system prompt. It carries exactly one context
block at a time (active plan, goal, or profile) and rewrites assistant voice
guidance when `caveman` is on. Caveman is off by default (`nil` or `false`).
Toggling it from the chat view with `ctrl+alt+c` persists the setting to the
current scope (project by default), invalidates the cached agent session so
the next turn picks up the new system prompt, and emits a `caveman: on/off`
system message for immediate feedback.

## TUI

`internal/tui` is a Bubble Tea app laid out Codex-style: a scrolling transcript
viewport, Pix banner, streaming assistant/tool output, slash-command editor
with autocomplete, the agent picker, `/model` provider/model/effort picker (a
windowed list that scrolls with the cursor and a `/` search filter),
`/settings` browser, `/permissions` editor, and a two-line status footer. The Ask composer doubles
as the working indicator: it shows a Role Manager pill while classification
runs and a generic `working` label for plain I/O (see below).

### Status bar

The footer is a two-line status bar:
- Line 1: cwd (home collapsed to `~`) and git branch (`⎇ main`).
- Line 2: provider·model·`caveman: on/off`, mode chip (colored), session (name
  or short id), context-usage progress bar and remaining percentage. The mode
  chip carries the engaged agent when there is one and the mode is agent —
  `agent · reviewer` — so what is carrying the turn is visible without opening
  anything. The caveman segment renders in muted style and is always present
  so the voice-rewrite state cannot be mistaken.
- Segments are never truncated or wrapped: when the terminal is narrower
  than the content, the padding between the mode chip and the right-hand
  segments clamps to one cell and the line overflows instead.

A mode decision writes a transcript line only when it changes something: a
classifier result that lands on the mode already selected repeats what the
chip is showing, so it stays silent. A decision that also launches explore
agents still says so, since that describes the turn rather than the chip.

Effort, when set, renders subtly next to the model id in muted style
(`gpt-5 · high`). An explicit `none` (reasoning off) is a real value and
renders; an empty effort means the provider default and renders nothing, and
without a configured model the effort is not shown at all.

Context usage has three degraded renderings:
- `~` prefix — pure `chars/4` estimate (no provider usage anchor yet).
- `(?)` instead of a percentage — the window is unknown, or the anchor predates
  a `/compact` (stale), so the context bar is empty and muted.
- a coloured bar and percentage — only when anchored and fresh; `<20%`
  remaining reads red, `<50%` remaining reads amber, otherwise teal.

Progress-bar business rules: the bar is 10 cells, filled by
`tokens / context window` clamped to [0, 1] at eighth-cell resolution
(`▏`–`▉`), so partial cells step in 2% increments and a fraction past 7/8
carries into the next cell. The fill shares the percentage's colour rule,
so bar and number can never disagree. When the window is unknown or the
usage is stale the bar is empty and muted — the harness draws no fill it
cannot stand behind; the `(?)` in the text segment carries that state. An
unanchored (estimated) token count still fills the bar normally; the `~`
in the text marks it as an estimate.

### Submit flow and working indicator

Pressing Enter in the Ask prompt echoes the text into the transcript as a
`user prompt` panel **instantly** — before mode classification and before any
provider I/O — and the composer's top edge switches to a working state:

- **Role Manager phases** (teal). While the Role Manager is classifying
  content, a filled `role manager` pill carries a sub-phase caption:
  *pre-prompt processing* (mode classification, prompt admission, and mode
  selection before the first model turn), *classifying steering*, or
  *classifying tool result*.
- **Generic working** (amber). Plain `working` is used only for disk or
  network I/O without a Role Manager signal: model streaming, tool execution,
  and retry back-off.

The agent emits `EventRoleManagerKind` (with the sub-phase) at every Role
Manager classification point; model and tool events switch the composer to the
generic phase, and done/error return it to idle. In the pre-send window — the
prompt is echoed but classification is still running — Enter is held with a
"still preparing" hint and `esc` cancels the turn without sending. While a
turn is running, Enter instead queues the text as a `user steering` prompt:
steered turns pass through the same Role Manager admission as the original
prompt, and a full queue drops the newest message.

### Todo panel

When a session is tracking a todo list, a flat `todo` panel sits between the
transcript and the composer showing a three-item window: the last completed
item, the current one (highlighted), the next one, and `… N more` for the
remainder. The counts never include the shown items, so `+N more` is always
literally true, and a finished list still shows the item it finished on rather
than rendering blank.

The agent owns the list and emits it (`EventTodosKind`, and at pass
boundaries); the TUI renders and persists it. `setTodos` is the single write
path, so a list change is never rendered without being durable. Toggle with
`ui.show_todos` (default on); an empty list renders nothing either way.

Mode is cycled with `shift+tab`. An explicitly chosen mode is carried into the
agent session as `ForceMode` and is not re-classified — suppressing the TUI's
own classification is not enough, because the session classifies again
internally.

Provider-streamed reasoning renders in a dim `reasoning` panel (toggle with
`ctrl+r`, `ui.show_reasoning`); tool rows toggle with `ctrl+t`
(`ui.show_tool_calls`). The transcript auto-follows the tail; scrolling up
(mouse wheel or `pgup`/`shift+up`) detaches and returns to the bottom
re-attach. Mouse capture is on by default (`ui.mouse`); with capture on the
terminal's own selection is unavailable, so the TUI implements its own (see
below). With `ui.mouse` off no mouse events arrive and the feature is inert.

### Rendered-line provenance

`Panel.Render` returns the rendered string *and* a `LineMap`: one `SourceLine`
per emitted row recording the clean text, the screen column it starts at, its
cell width, and whether the row is pure chrome. `Panel.View` is the string-only
half.

This exists so hit-testing and copying can recover clean text without
pattern-matching rendered output — the `│` panel bar and the `│` system-row
marker are the same glyph, and only the renderer knows which columns are
decoration. The invariant each entry guarantees is
`ansi.Cut(ansi.Strip(line), Col, Col+Width) == Text`, and the map is always the
same length as the frame's line count.

A line may also carry a truncation marker (`… N more lines`) plus the `Hidden`
text it stands for, so a selection overlapping the marker copies the hidden
remainder instead of the hint. `Highlight` reverse-videos a cell range without
changing any line's visible characters or width, and refuses to touch a frame
whose map length does not match — a desynced map must never corrupt the frame.

Slicing is by terminal cell, never by rune index, so CJK and emoji stay on
cluster boundaries.

### Styled rows

Every transcript row that carries colour is built from `Seg` and `Row`
(`styledline.go`) rather than assembled by hand. A `Row` is clipped and
measured while its text is still plain, and only then turned into escape
sequences, with its `SourceLine` derived from that plain twin — so both
`LineMap` invariants hold by construction instead of by review. `renderRows`
is the only path from rows to the transcript, and it guarantees one map entry
per emitted line.

`sgr.go` is the only place in the TUI that writes SGR by hand. lipgloss closes
every styled span with a full reset, which clears the background as well as the
foreground; inside a row with a background that would cancel the wash at the
first coloured token and render the rest of the line bare. Rows therefore close
foregrounds with `\x1b[39m` and the background once with `\x1b[49m`, and use
reverse video (`\x1b[7m`) for intra-line emphasis because it composes with a
background the segment cannot see. **Never emit `\x1b[0m` inside a `Row`, and
never hand styled text to lipgloss for wrapping** — its wrap path re-emits a
full reset at every break.

`NewSeg` strips ESC, C0, C1 and DEL from its text. This is load-bearing, not
hygiene: tool output and file contents are attacker-controlled bytes on their
way to a terminal, and an OSC 52 sequence that survived would write the user's
clipboard. Tabs are expanded before anything measures the text, since the
terminal resolves its own tab stops from the screen edge and a gutter has
already shifted the code.

### Tool row previews

Collapsed tool rows show a different amount per tool, because the useful part
is in a different place:

| Tool | Collapsed | Anchored |
|---|---|---|
| `Bash` | 3 lines | tail — a command's verdict is at the end |
| `Read` | 3 lines, numbered | head — a file's identity is at the start |
| a diff | 6 rows | first change — leading context is wasted rows |
| everything else | 1 line | head — already a summary |

A tail-anchored preview puts its hint *above* the content, since the hint
summarises what came before it. `ctrl+o` expands everything.

The invocation line next to a tool name is one argument, chosen per tool by
`formatToolInvocation`'s key order: `Bash` shows `command`, `Read`/`Write`/`Edit`
show `path`, `Grep`/`Glob` show `pattern`, `WebSearch` shows `query`, `WebFetch`
shows `url`, and anything unlisted tries `command`, `path`, `pattern`, `query`,
`url`, `args` in that order. `Write` and `Edit` deliberately list `path` alone,
so a row shows what was written to and never the file body, the `old_string`, or
the `new_string`. The value is clipped to 120 runes (60 for args that would not
parse as JSON).

Read rows are numbered at render time, never by the Read tool itself: the
tool's `offset` is a byte count, so a model that read a line number out of the
output and passed it back as an offset would silently get the wrong region.
For a partial read (`offset > 0`) the Read tool emits `Result.Meta` with
`start_line`, derived by counting newlines in the `[0, offset)` prefix up to
1 MiB. The TUI receives this via `EventToolMetaKind` and numbers the visible
lines starting from `start_line`. Without the metadata the partial read is
left unnumbered rather than numbered wrongly.

Syntax highlighting (chroma, mapped onto the palette in `theme.go`, lexer
chosen by filename only) applies to expanded rows alone — collapsed, the diff
and status colours are the whole signal.

### Diffs

`Write` and `Edit` report their targets through `tools.Targeter`, so
`internal/filediff` snapshots exactly those paths via `BeforePaths` around the
call. `Bash`'s changes are still observed rather than reported: inside a git
repository discovery is by `git status`, which sees what happened however it
happened — `sed`, a heredoc, a formatter, `make`, a test that rewrites its own
fixtures. Outside one it falls back to inferring targets from the command, and
refuses far more than it accepts: reporting "nothing changed" for a command
that changed everything would be worse than showing nothing.

The recorder hooks `executeCall` around every mutating tool (which always runs
on the sequential path, so no locking is needed) and emits
`EventToolDiffKind`. Like `EventToolProgressKind` it is render-only: neither
enters the conversation nor reaches a model, and tests assert the
provider-facing turns are unchanged by their presence.

Collapsed diff rows are foreground-only. Expanded rows carry a background wash
instead, which marks the row without using the foreground — freeing it for
syntax colour, so a changed line reads as code rather than as a stripe.

Selection highlight (`Highlight` in `linemap.go`) wraps the selected cells in
reverse video. Because `ansi.Cut` strips every escape sequence before the cut
point, the right-hand fragment of a partially-selected washed row would be left
bare after the selection's full reset. Each `SourceLine` therefore carries a
`Reopen` string — the SGR prefix that restores the row's original background
(and the first segment's foreground) — which is inserted between the reset and
the right fragment.

Costs are bounded throughout: a git timeout latches the feature off for the
session rather than being paid per command, plus per-file byte caps and a file
count cap.

### Transcript selection

Left-button press-drag-release over the transcript selects a character range
and copies the clean underlying text on release: no borders, no ANSI, no `⌁`
tool-row prefix, no right-aligned status word, and `… N more lines` markers
expanded to their hidden remainder (`LineMap.Text` substitutes the `Hidden`
text whenever the selection overlaps the marker cells).

Business rules:

- **Content coordinates.** The selection is stored in content lines, not
  screen rows, so it survives scrolling — and the wheel works *mid-drag*
  (press, wheel, keep dragging, release). Edge auto-scroll is not implemented;
  the wheel is the documented gesture.
- **One content compare clears.** A selection is cleared whenever the rendered
  body changes — new message, streaming delta, `ctrl+l`, `ctrl+o`, `ctrl+r`,
  `ctrl+t`, or a width change — because the line map can shift underneath it.
  A height-only `WindowSizeMsg` leaves the body identical, so it clears the
  selection explicitly.
- **Bare click clears.** A press outside the viewport, or a release at the
  anchor cell, clears the selection and copies nothing. Release never tests
  the button (X10 reports `Button==None`; SGR keeps `Left`), and wheel events
  are checked before press (a wheel tick is `Action==Press`) or every tick
  would re-anchor the selection.
- **`esc` clears first.** With a live selection, `esc` dismisses the highlight
  and nothing else; a second `esc` does the usual cancel/pre-send work.
- **Copy feedback.** Release runs the same `clipboard.Copy` path as `ctrl+c`
  and reports `copied N lines to clipboard (method)` through the footer. That
  notice changes the body, so the highlight clears on the next frame — that is
  the intended “copied, done” feel. For large OSC 52 payloads a caveat is
  appended, because xterm's `maxStringParseSize` and tmux without
  `set-clipboard on` drop them silently.
- **Highlight is visible-window only.** `Highlight` rewrites at most the
  viewport's height of lines (off-screen rows are re-rendered every frame
  anyway) and must never change a line's visible characters or cell width —
  a width change would trip the viewport's `MaxWidth` and shift the frame.

### Keybindings

| Key | Behaviour |
| --- | --------- |
| `ctrl+c` | Copy the current prompt to the clipboard (native, then OSC 52) |
| `ctrl+d` | Quit, unconditionally |
| `shift+tab` | Cycle mode: agent → plan → goal |
| `esc` | Close any full-screen view (nested views pop to their parent); cancels a held submit or an in-flight pre-send |
| `space` / `n` / `s` / `enter` | Use in the **Clarify** questionnaire view: select, add a note, skip the question, submit |
| `ctrl+l` | Clear the transcript *view* — the session is kept |
| `ctrl+o` | Toggle full output for all truncated turns and tool results |
| `ctrl+r` / `ctrl+t` | Toggle reasoning-panel / tool-row display for the session |
| `ctrl+alt+c` | Toggle the caveman voice rewrite, persisting to the scoped settings file |
| `ctrl+alt+p` | Cycle mode and re-sync plan mode, from any screen |
| `ctrl+home` / `ctrl+end` | Jump the transcript to the top / bottom |
| `ctrl+j` / `alt+enter` | Insert a newline in the prompt editor |
| `shift+enter` | Insert a newline on terminals that support the kitty keyboard protocol |
| `up` / `down` | Browse prompt history and prompt library. Library entries come first and their names show as a chip strip above the composer: `tab` cycles the named prompts, `right` accepts the loaded one into the composer, `enter` sends it. Typing — like any edit key — leaves the browse cycle and edits the loaded prompt |
| `alt+s` | Save the current prompt to the project prompt library |
| `tab` | Move the highlight through the slash-command hints, or — with no `/` popup, in agent mode — through the agent picker. It never writes into the prompt. While browsing the prompt library it loads the next named prompt instead |
| `right` / `enter` | Accept the highlighted hint (or the first, for `right` with nothing highlighted); in the agent picker, engage the highlighted agent; while browsing the prompt library, accept the loaded prompt into the composer (`right`) or send it (`enter`). Without a highlight, `right` is the cursor key and `enter` sends |
| `ctrl+g` | Start the highlighted `↻` background-agent definition as a background agent |
| `esc` (with a highlight) | Drop the highlight, keeping the popup or strip on screen |
| `enter` (while working) | Steer the running turn with a new user message |
| mouse wheel / `pgup` / `pgdown` / `shift+up` / `shift+down` | Scroll the transcript (detaches auto-follow) |
| left drag over the transcript | Select a character range (highlighted live); release copies the clean text |
| `esc` (with a live selection) | Clear the selection first; a second `esc` cancels/pre-send as usual |

Highlighting and accepting are deliberately separate in both the `/` popup and
the agent picker. `tab` moves a highlight only; it must not write the
candidate into the prompt, because the completion list is recomputed from the
prompt text on every message — including the cursor blink — so writing `/mode`
into the editor narrowed the list to that one command and pinned the cycle to
a single entry. `refreshAutocomplete` drops the highlight only when the
candidate list actually changes, so a blink can never move it either.

`ctrl+l` clears the transcript view; `/clear` (or `/new`) starts a *new* session.
They are deliberately different: one is cosmetic, the other changes what is
persisted.

The user-facing inventory of every binding, including the per-view keys the
`HelpBar` footers advertise, lives in `keySections()` in
`internal/tui/help.go`; `/help` renders it under the command list.
`TestHelpTextCoversEveryHandledKey` scans the `case "<key>":` literals across
the package and fails when a handled key is missing from that table, so a new
binding needs a line there as well as in this document.

### Prompt syntax

- `@path` or `@"path with spaces"` attaches the contents of a file after
  classification. Use `@agent:name` to engage a named agent instead.
- `!cmd` executes a local `Bash` command (full shell by default; read-only
  in plan mode, or whenever `read_only` is set) and sends the output to
  the model under the `signet:debug` profile.

### Agent picker

In agent mode the composer carries a strip of the agents that can carry the
turn, drawn from both trees:

| Row | Source | Marker |
| --- | ------ | ------ |
| user profile | `internal/profiles` (flat `Name`/`Content`) | none, keycap bright |
| background definition | `internal/agentprofile` | `↻`, amber |
| built-in | embedded `signet:` profile | `◈`, muted |

A flat profile owns a shared name — it is what `CarrierOptions` resolves
first — so a background definition of the same name is not offered twice.

The strip is the slash popup's sibling and shares its keys: `tab` highlights
the next candidate (ending on a `(none)` entry that clears the selection),
`enter` or `right` engages the highlighted one, `esc` drops the highlight.
Typing `@name` — or `@agent:name` — filters the strip, and the filter text is
removed from the prompt when a candidate is engaged. The slash popup wins the
strip and `tab` whenever both could show.

`ctrl+g` is the second verb, and only background definitions answer it: it
starts the highlighted definition as a background agent (`bgagent.Manager`),
exactly as `/agent start <name>` does. Starting a loop does not answer the
prompt in the composer, so it is deliberately not `enter`.

The engaged agent is session state: it applies to every following turn
(`App.namedAgent` → `agent.TurnInput.ForceAgent`), shows in the footer chip
next to the mode, and is cleared by `/clear`. It is **agent mode only**. Plan
and goal mode carry Signet's own plan or goal — `resolveCarrier` admits
exactly one carrier, and `ForceAgent` also forces the mode, so sending an
engaged agent from plan mode would silently drop the mode the user picked.
Outside agent mode the selection goes dormant rather than being discarded
(`App.engagedAgent`, `App.engagedAgentTools` both return nothing): the footer
hides it, the picker hides, the tool allow-list does not apply, and cycling
`agent → plan → goal → agent` gets it back. `/profile <name>` engages the
same field. The system prompt keeps its shape — the engaged text is the
single carrier block (`prompt.CarrierProfile`), so the identity block naming
Signet, the provider and the model still opens the prompt. For a background
definition the carrier text is its `system_prompt`, resolved by
`CarrierOptions` falling back to `agentprofile.Load`, and its `tools`
allow-list narrows the foreground session's registry the same way
`bgagent.buildSession` narrows it. Its `mode`, `schedule` and
`max_iterations` are background-loop settings and do not apply in the
foreground.

### Prompt library

Named prompts live in a library that merges a global file
(`~/.vulnetix/signet/prompts.json`, `config.GlobalPromptsPath`) with a project
override (`<workdir>/.vulnetix/prompts.json`, `config.ProjectPromptsPath`).
Project entries win by name, and the merge keeps the global file's order for
names it already had, appending project-only names after it. A missing file is
an empty library, not an error: the library is chrome, and a fresh checkout has
no reason to fail the composer.

Browsing is entered with `up`. The result list is built once, when the cycle
starts:

1. every library entry matching the composer text, project overrides applied;
2. then this workdir's session-history prompts, newest first, that match the
   same text and are not already in the list by identical prompt text.

The composer's text at the moment `up` is pressed is the filter — a partial
prompt narrows what browsing offers — and the match is a case-insensitive
substring test against both the entry name and the prompt text
(`promptlib.Match`). The list is **not** rebuilt mid-cycle; the filter is fixed
for the life of the cycle.

Because library entries lead the list, the named ones are always a prefix of
it, and the TUI draws that prefix as a chip strip above the composer — the
agent picker's sibling, one chip per prompt *name*, the loaded one highlighted.
The strip is what replaced a "type to search" hint: typing during a cycle
never appeared in the composer (the loaded prompt occupied it), so the filter
it was narrowing was invisible. Names are visible, and `tab` walks them.

Browsing and editing are distinct states, and every key resolves to exactly
one of them:

| Key | While browsing |
| --- | -------------- |
| `up` / `down` | Move through **all** results, library entries then session history. `down` past the newest result restores the text you had before browsing |
| `tab` | Load the next **named** prompt, wrapping at the end of the named prefix. With no library match the strip is absent and `tab` does nothing |
| `right` | Accept the loaded prompt into the composer and leave the cycle, cursor at the end |
| `enter` | Accept the loaded prompt and send it (`/command` and `!shell` text dispatches as usual; while a turn runs it steers) |
| `esc` | Cancel: restore the text you had before browsing |
| anything else (printable characters, `backspace`, `←`, `home`, `delete`, …) | Leave the browse cycle **keeping the loaded prompt**, and apply the key as an ordinary edit |

The last row is what makes a recalled prompt editable: typing appends to it and
backspace deletes one character of it rather than clearing the composer. `right`
is the only edit-adjacent key with a browse meaning of its own, because
accepting and leaving is what the cursor key would have done anyway at the end
of the line.

Edge cases:

- **No results** — the cycle still opens with the typed text intact and
  `historyIndex` at `-1`; `up` and `tab` have nothing to move to, and `esc`
  or an edit key leaves the text as it was.
- **Duplicate prompt text** — a session-history prompt identical to a library
  prompt is dropped, so a saved prompt is offered once, under its name.
- **An unnamed entry is loaded** — `up` can walk past the named prefix into
  session history, where no chip is highlighted; `tab` returns to the first
  named prompt rather than continuing into the unnamed tail.
- **Composer hint** — the meta line reads `↑↓ cycle · tab name · → accept ·
  ⏎ use · esc cancel`, dropping the `tab name` segment when the library
  contributed nothing to this cycle.

`alt+s` in the chat composer enters a naming mode: type a name and press
Enter to save the current editor text to the **project** library
(`promptlib.Add`, which replaces an entry of the same name in place and stamps
`created_at` when it is zero). An empty name cancels with `save cancelled: name
required`, and Esc cancels. Saving always writes the project file; the global
file is edited by hand.

### Model picker

`/model` shows provider tabs, a windowed model list, the effort chips and the
write scope. The list's row budget is *measured*, never guessed: the pre-list
chrome (header, tabs, search line), the post-list chrome (effort, scope, any
error, help bar), the one-row counter/overflow line under the list, and the
frame's one-cell padding are each subtracted from the terminal height, and the
remainder is how many model rows are drawn. The view therefore fills the
terminal exactly — a row short would waste a model row, a row over would scroll
the help bar off the bottom. With no `WindowSizeMsg` yet (height 0) it falls
back to 10 rows, and it never draws fewer than 3.

Business rules:

- **The window follows the cursor** (`windowStart`): moving below the last
  visible row scrolls by one, moving above the first scrolls back, and wrapping
  from the last id to the first (or back) re-anchors the window at that end.
- **The counter is always shown**, as `<cursor>/<total>`, with `↑ N more` and
  `↓ N more` added only when there is something off-screen in that direction.
- **`/` filters** the catalogue by case-insensitive substring; the counter then
  reads `<cursor>/<matches> (of <total>)`, `esc` clears the filter rather than
  leaving the view, and committing selects from the *filtered* list — the row
  under the cursor is the row that is saved.
- **An empty catalogue** renders `no models in this profile — type or import a
  model id` instead of a list, and the counter line is omitted with it.

#### Catalogue sources

The `/model` list is built by merging three sources, each one lower priority
than the last, and the merged list is de-duplicated by model id:

1. **Live fetch** — when the provider exposes a model-list endpoint, Signet
   queries it on first entry and caches the result per session. The provider
   tab shows `⊙ loading` while a fetch is in flight. Live fetched models are
   not persisted; the cache is an in-memory map keyed by provider name.
2. **Profile models** — custom or saved models declared in the provider profile
   (`settings.json`) are merged next.
3. **Static fallback** — a hard-coded default catalogue for built-ins that have
   no endpoint or when the live fetch fails. Users can still type any model id
   and commit it.

Live fetch is available for `openai`, `anthropic`, `cloudflare-workers-ai`,
`openrouter`, `google-gemini`, `ollama`, `github-copilot`, and
`cloudflare-ai-gateway`. `r` clears the cache and re-fetches for the selected
provider; fetch errors are rendered under the list as `✗ fetch: ...` so silent
failures are visible.

Provider-specific edge cases:

- **`huggingface`** does **not** live-fetch. Although HuggingFace's routing
  layer exposes `GET https://router.huggingface.co/v1/models`, the response
  lists more models than the free `hf-inference` serverless provider can
  actually run, so presenting it causes users to select models that immediately
  fail with `400 Model not supported by provider hf-inference`. Instead,
  Signet ships a small, conservative static catalog of models that are widely
  available on the free Serverless Inference API. Users can still type and
  commit any model id if their token tier supports it.
- **`cloudflare-ai-gateway`** has no gateway-side `/models` endpoint, but every
  gateway can run any Workers AI model. Signet extracts the `account_id` from
  the configured gateway base URL
  (`https://gateway.ai.cloudflare.com/v1/{account_id}/{gateway_id}`) and
  queries the Cloudflare v4 API at
  `https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/models/search`,
  using the Cloudflare `CF_API_KEY` with standard `Authorization: Bearer` auth.
  For inference, the gateway can operate in two modes:
  1. **Gateway-token mode** — the upstream provider key is stored in the
     gateway configuration; Signet sends `cf-aig-authorization: Bearer CF_API_KEY`
     and the gateway injects its own upstream key.
  2. **Pass-through mode** — the gateway forwards the upstream provider key.
     Set the optional `UPSTREAM_API_KEY` (or `OPENAI_API_KEY`) credential.
     When present, Signet sends `Authorization: Bearer UPSTREAM_API_KEY` and
     the request reaches the upstream provider directly. If `UPSTREAM_API_KEY` is
     absent, Signet falls back to gateway-token mode.
  Production gateway hosts are mapped to `api.cloudflare.com`; hosts other than
  `gateway.ai.cloudflare.com` are followed as-is so tests and private gateways
  can be mocked.

### Slash commands

| Command | Description |
| ------- | ----------- |
| `/profile` | Switch agent profile |
| `/local-model` | Assess, download, launch, or stop a local classifier model |
| `/model` | Pick provider and model |
| `/mode` | Show or set operating mode (e.g. `/mode plan`) |
| `/todos` | Show plan progress |
| `/execute` | Leave plan mode and execute the plan |
| `/refine` | Refine the extracted plan |
| `/code-review` | Run a Vulnetix code review |
| `/settings` | View and edit settings |
| `/credentials` | Manage provider credentials |
| `/permissions` | Edit tool permissions |
| `/help` | Show the commands and every keyboard shortcut |
| `/clear` | Start a new session |
| `/compact` | Summarise the session into a new one |
| `/rename` | Rename this session |
| `/agent` | Manage background agents (`create`, `list`, `edit <name>`, `start`, `stop`, `pause`, `resume`, `log`) |

The table is the whole set registered by `internal/tui.NewRegistry`. Two
aliases exist but are not table rows: `/new` is a visible alias of `/clear`
(`RegisterAlias`, appears in `Names()` and autocomplete), and `/provider` is a
hidden alias of `/model` (`RegisterHiddenAlias`, dispatchable but absent from
`Names()` and autocomplete).

### Startup credential message

When the selected provider is unconfigured but other providers are, the TUI
points the user at `/model` instead of claiming the selected provider's
credentials are missing. When nothing is configured, it says so clearly.

### Context accounting

`internal/transcript` implements hybrid token accounting with no tokenizer
dependency: anchor on the last assistant message carrying provider-reported
usage, then add a conservative `chars/4` estimate only for messages after that
anchor. `internal/modelinfo` maps model ids to context-window sizes; unlisted
models render `(?)` rather than a guessed denominator. The `context_windows`
setting overrides the registry for models Signet does not know. After
`/compact` the anchor describes the pre-compaction conversation, so the footer
renders `(?)` until a fresh assistant response lands.

### Settings

The effective settings view merges, lowest to highest: defaults, `state.json`,
global `settings.json`, project `settings.json`, environment, then CLI flags.
`/settings` shows the effective value and provenance for each key. Settings
include `provider`, `model`, `effort`, `caveman`, `read_only`,
`permissions` (structured `allow`/`ask`/`deny`), `session_retention_days`,
`ui.banner`, `ui.status_bar`, `ui.spinner`, `ui.show_reasoning`,
`ui.show_tool_calls`, `ui.show_todos`, `ui.mouse`, `ui.colors`,
`ui.kitty_keyboard` (all default on when unset except `ui.show_reasoning`,
which defaults off unless explicitly true; `ui.kitty_keyboard` is overridden
off by `SIGNET_NO_KITTY=1`),
`show_session_names` (default on), `context_windows`,
`resilience` (`max_attempts`, `max_iterations`, `max_passes`, `max_clarify_rounds`), `providers`,
`caveman` (default off; toggled from any screen with `ctrl+alt+c`),
`allow_project_providers`, and the `classifier` block
(`provider`, `model`, `effort`, `chunk.max_bytes`, `chunk.concurrency`) covered
in the Security classifier section above. That enumeration is the whole
`config.Settings` struct, plus two keys that are accepted on read and never
written back:

- `bash_readonly` — the deprecated alias for `read_only`. `Settings.UnmarshalJSON`
  folds it into `read_only` only when the canonical key is absent, then clears
  it, so a file Signet rewrites emits `read_only` alone and a file carrying both
  keys resolves to the canonical one.
- the legacy flat `permissions` map (`{"Bash": "deny"}`) — accepted and
  converted to the structured `allow`/`ask`/`deny` shape on read, never written.

Permission
rules merge by union — a project file can add rules but never remove a
global rule. Provider profiles merge key-by-key the same way. Resilience
budgets merge to the *minimum* of global and project, so a project file can
tighten a budget but never raise one.

Tool availability defaults to allow: a call matching no permission rule
proceeds (unregistered tool names are still rejected by the agent's registry
check first). Opt-outs, in order of strength: a `permissions.deny` rule
always blocks; `read_only: true` (settings file or the `/settings` "read-only
tools" toggle) removes every mutating tool from the registry — `Write`,
`Edit`, and full `Bash` are not registered, though a read-only `Bash` remains
for inspection; and `postures: {permission_no_match: enforce}` in
`preferences.yaml` restores the legacy no-match-block. `Bash` otherwise runs
full shell commands via `sh -c` (timeout, env scrubbing, and output truncation
still apply); plan mode keeps `Bash` read-only regardless of `read_only`.
## Local inference

The classifier can run against a local model through the existing `ollama`
provider seam: set `classifier.provider` to `ollama`, `classifier.model` to the
local model id, and `OLLAMA_HOST` to the server's base URL
(`http://127.0.0.1:18080/v1`). Routing is all-or-nothing: once a local
classifier is configured it handles every classification. The same applies to
the `llama` provider for a `llama-server` endpoint (default
`http://localhost:8080/v1`), configured via `SIGNET_LLAMA_HOST` or the
per-field host/port/protocol managed in `/credentials`.

Supporting pieces:

- `internal/machineprobe` measures CPU threads, RAM, GPU backend/VRAM (via
  `llama-server --list-devices`), and free disk, then produces a plain-language
  suitability verdict. The verdict states the iGPU prefill caveat honestly:
  an integrated GPU shares LPDDR bandwidth with the CPU, so the security
  classifier — prefill-bound on large tool results — may classify *slower*
  than a small frontier model despite free VRAM. Chunked classification is what
  makes this tolerable.
- `internal/localinfer` detects a launchable server (`llama-server`, `ollama`,
  `vllm`), probes common ports (`11434`, `18080`, `8000`) for an already-running
  server, launches with the default args for this device, health-checks
  `GET {base}/v1/models`, and resolves HuggingFace model metadata (largest GGUF
  file, size, sha256) for download. Models download under
  `<GlobalDir>/models/`.
- The HuggingFace token resolves as provider `huggingface` (`HF_TOKEN` /
  `HUGGINGFACE_TOKEN`) through the same credential stack as providers.
- `huggingface` is also a built-in chat provider using the OpenAI-compatible
  Serverless Inference API at `https://router.huggingface.co/hf-inference/v1`,
  authenticated with the same token.
- The TUI exposes this through `/local-model` (assess the machine and server),
  `/local-model download <repo>` (resumable, checksummed download with live
  progress), `/local-model launch <repo>` (launch llama-server with the default
  args and health-check `/v1/models`), and `/local-model stop`. Quitting the TUI
  stops a launched server.

## Performance

The TUI's perceived-latency path is tuned at several layers:

- **Stream coalescing**: both stream hops are buffered (256) and the TUI's
  `nextAgent` drains a run of same-kind text/reasoning deltas into one update,
  so a long reply repaints once per drain rather than once per token.
- **Builder accumulation**: streamed text appends into a `strings.Builder`
  (`Message.AppendText`/`Text`), avoiding O(n²) string concatenation.
- **Per-message render memoisation**: `MessageList.Render` caches each
  message's rendered text + `LineMap`, keyed on the fields that affect it
  (content length, width, expand, role, status, …). The streaming tail and
  running tool rows re-render; everything else renders once per change.
- **Concurrent read-only tools**: a leading run of read-only, permission-allowed
  tool calls executes concurrently (bounded by 4), never reordering across a
  mutating call, and results re-enter in call order keyed by `ToolCallID`.
- **Shared HTTP client**: one tuned `httpclient.Default()` transport
  (`MaxIdleConnsPerHost: 16`, `ResponseHeaderTimeout: 30s`, no blanket
  `Client.Timeout`) serves provider, tool, credential and catalogue I/O. SSE
  streams carry an idle-gap watchdog instead. WebFetch uses a dedicated
  transport whose validating `DialContext` resolves once and pins the address,
  closing the DNS-rebinding TOCTOU.
- **Timing**: opt-in `SIGNET_TRACE=<path>` writes JSONL `{phase, event,
  duration}` records; the composer meta shows a live `working · N.Ns` elapsed
  label and running tool rows show live elapsed time.
