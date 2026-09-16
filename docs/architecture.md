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

`internal/guardrails` auto-discovers the Vulnetix ai-firewall configuration and
writes the provider entry (base URL + key source) with no custom headers.

## Modes

`internal/modes` defines three modes; agent is the default.

### Agent mode

Interactive default. Profiles (`internal/profiles`, stored under
`~/.vulnetix/signet/profiles/`) are selectable at startup and mid-session via
`/profile`. Built-in profiles live under the `signet:` namespace; the debug
profile (`signet:debug`) is automatically engaged for `!cmd` inline-shell
round-trips. User files cannot shadow a built-in name.

### Plan mode (read-only)

Mirrors Pi's plan-mode extension:

- Built-in edit/write tools are disabled; other tools remain active.
- `Bash` is registered by default and restricted to a read-only allowlist
  (`cat`, `grep`, `find`, `ls`, read-only `git` subcommands such as
  `status`/`log`/`diff`, `uname`, etc.).
  Mutating commands (`rm`, `mv`, `cp`, `mkdir`, `touch`, `git add/commit/push`,
  package installs, `sudo`/`kill`, editors) are blocked.
- Toggle via `/plan`, `Ctrl+Alt+P`, or `--plan`; `/todos` shows progress.
- After the agent emits a numbered plan under a `Plan:` header, the steps are
  extracted (`internal/plans/extract.go`) and the user is prompted with three
  options:
  - **Execute the plan** — leaves plan mode (full tools restored);
    `[DONE:n]` markers advance the progress widget
    (`internal/plans/progress.go`).
  - **Stay in plan mode** — keeps read-only constraints.
  - **Refine the plan** — opens the editor and sends the revision back as a
    user message.
- Plan-mode state (enabled/executing/todos) is persisted as session entries so
  it survives resume.

### Goal mode

Reads and writes goals under `.vulnetix/goals/`. The "memorise" action saves a
goal; memorised goals are surfaced later through slash-command autocomplete
(replay).

A goal-mode prompt at the top level runs a **pass loop** instead of a single
bounded tool loop: when a pass exhausts its iteration budget, a goal evaluator
decides whether the work advanced, and the loop continues while it does. The
loop is unbounded by design — it is stopped by a stall, not a counter — and
`esc` (or `SIGINT` outside the TUI) returns the partial result cleanly. The
normative rules, including the verification gate and every termination
condition, are in [role-manager.md](role-manager.md), "Goal pass loop".

A prompt the classifier routes to goal mode carries the prompt itself as the
goal carrier. `CarrierOptions` only knows how to load a *memorised* goal, so
without this a classifier-routed goal would reach the evaluator with nothing to
evaluate against.

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

The TUI integrates background agents via `/agent create`, `/agent start`,
`/agent pause`, `/agent resume`, `/agent stop`, `/agent list`, and
`/agent log`. Events stream into the main transcript as system lines so the
user's session is never blocked.

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
guidance when `caveman` is on.

## TUI

`internal/tui` is a Bubble Tea app laid out Codex-style: a scrolling transcript
viewport, Pix banner, streaming assistant/tool output, slash-command editor
with autocomplete, `/model` provider/model/effort picker, `/settings` browser,
`/permissions` editor, and a two-line status footer. The Ask composer doubles
as the working indicator: it shows a Role Manager pill while classification
runs and a generic `working` label for plain I/O (see below).

### Status bar

The footer is a two-line status bar:
- Line 1: cwd (home collapsed to `~`) and git branch (`⎇ main`).
- Line 2: provider·model, mode chip (colored), session (name or short id),
  context-usage progress bar and remaining percentage.
- Truncates per segment, dropping the context bar then session before wrapping.

Context usage has three degraded renderings:
- `~` prefix — pure `chars/4` estimate (no provider usage anchor yet).
- `(?)` instead of a percentage — the window is unknown, or the anchor predates
  a `/compact` (stale), so the context bar is empty and muted.
- a coloured bar and percentage — only when anchored and fresh; `<20%`
  remaining reads red, `<50%` remaining reads amber, otherwise teal.

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
| `ctrl+j` / `alt+enter` | Insert a newline in the prompt editor |
| `shift+enter` | Insert a newline on terminals that support the kitty keyboard protocol |
| `up` / `down` | Browse prompt history and prompt library (type to filter; any edit key leaves the browse cycle) |
| `alt+s` | Save the current prompt to the project prompt library |
| `tab` | Cycle slash-command autocomplete hints |
| `right` | Accept the first slash-command autocomplete hint |
| `enter` (while working) | Steer the running turn with a new user message |
| mouse wheel / `pgup` / `pgdown` / `shift+up` / `shift+down` | Scroll the transcript (detaches auto-follow) |
| left drag over the transcript | Select a character range (highlighted live); release copies the clean text |
| `esc` (with a live selection) | Clear the selection first; a second `esc` cancels/pre-send as usual |

`ctrl+l` clears the transcript view; `/clear` (or `/new`) starts a *new* session.
They are deliberately different: one is cosmetic, the other changes what is
persisted.

### Prompt syntax

- `@path` or `@"path with spaces"` attaches the contents of a file after
  classification. Use `@agent:name` to engage a named agent instead.
- `!cmd` executes a local `Bash` command (full shell by default; read-only
  in plan mode, or whenever `bash_readonly` is set) and sends the output to
  the model under the `signet:debug` profile.

### Prompt library

Named prompts live in a library that merges a global file
(`~/.vulnetix/signet/prompts.json`) with a project override
(`<workdir>/.vulnetix/prompts.json`). Project entries win by name.

Library entries are ranked before session history when cycling with the Up
arrow. Typing while cycling filters both sources case-insensitively (name
and prompt text), with library matches appearing first.

Browsing and editing are distinct states, and every key resolves to exactly
one of them:

| Key | While browsing |
| --- | -------------- |
| `up` / `down` | Move through results. `down` past the newest result restores the text you had before browsing |
| printable characters, space | Narrow the filter and reload the first match |
| `enter` | Accept the loaded prompt (submits it) |
| `esc` | Cancel: restore the text you had before browsing |
| anything else (`backspace`, `←`/`→`, `home`, `delete`, …) | Leave the browse cycle **keeping the loaded prompt**, and apply the key as an ordinary edit |

The last row is what makes a recalled prompt editable: backspace deletes one
character of it rather than clearing the composer, and the arrow keys move the
cursor through it. The trade-off is that backspace no longer walks the filter
back — narrowing is forward-only.

`alt+s` in the chat composer enters a naming mode: type a name and press
Enter to save the current editor text to the project library. Esc cancels.

### Slash commands

| Command | Description |
| ------- | ----------- |
| `/help` | Show available commands |
| `/model` | Show current provider and model |
| `/mode` | Show or set operating mode |
| `/plan` | Toggle plan mode |
| `/settings` | View and edit settings |
| `/credentials` | Manage provider credentials |
| `/permissions` | Edit tool permissions |
| `/goal` | Memorise or replay a goal |
| `/profile` | Switch agent profile |
| `/clear` (or `/new`) | Start a new session |
| `/compact` | Summarise the session into a new one |
| `/rename` | Rename the current session |
| `/todos` | Show progress of the tracked todo list |
| `/agent` | Manage background agents (`create`, `list`, `start`, `stop`, `pause`, `resume`, `log`) |

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
include `provider`, `model`, `effort`, `caveman`, `bash_readonly`,
`permissions` (structured `allow`/`ask`/`deny`), `session_retention_days`,
`ui.banner`, `ui.status_bar`, `ui.spinner`, `ui.show_reasoning`,
`ui.show_tool_calls`, `ui.show_todos`, `ui.mouse` (all default on),
`show_session_names` (default on), `context_windows`,
`resilience` (`max_attempts`, `max_iterations`, `max_passes`), `providers`,
and `allow_project_providers`. Permission
rules merge by union — a project file can add rules but never remove a
global rule. Provider profiles merge key-by-key the same way. Resilience
budgets merge to the *minimum* of global and project, so a project file can
tighten a budget but never raise one.

Tool availability defaults to allow: a call matching no permission rule
proceeds (unregistered tool names are still rejected by the agent's registry
check first). Opt-outs, in order of strength: a `permissions.deny` rule
always blocks; `bash_readonly: true` (settings file or the `/settings` "bash
read-only" toggle) confines Bash to its read-only allowlist; and
`postures: {permission_no_match: enforce}` in `preferences.yaml` restores the
legacy no-match-block. Bash otherwise runs full shell commands via `sh -c`
(timeout, env scrubbing, and output truncation still apply); plan mode keeps
Bash read-only regardless of `bash_readonly`.