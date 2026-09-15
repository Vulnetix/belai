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

## Provider layer

`internal/provider` + `internal/wire` reach every provider through two knobs —
`base_url` and `api_key` — speaking the three ai-firewall surfaces:

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
`/profile`.

### Plan mode (read-only)

Mirrors Pi's plan-mode extension:

- Built-in edit/write tools are disabled; other tools remain active.
- `bash` is restricted to a read-only allowlist (`cat`, `grep`, `find`, `ls`,
  read-only `git` subcommands such as `status`/`log`/`diff`, `uname`, etc.).
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

## Session store

`internal/session` persists append-only JSONL session trees
(`id` + `parentId`) under `~/.vulnetix/signet/sessions/<workdir>/<session>.jsonl`, with
fork/resume reads (full or partial UUID) and display names.

## Credentials

`internal/credentials` implements layered credential resolution for the four
supported providers. The resolution order is:

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
happen to `cd` into.

When `~/.signet` exists and `~/.vulnetix/signet` does not, `config.Migrate()`
moves the directory on first startup. A cross-filesystem fallback copies
recursively and leaves a `.migrated` marker; nothing is deleted.

The TUI credential manager (`/credentials`) shows provenance for every field,
accepts `s` to set, `c` to clear, and `b` to cycle the write backend. The
default write backend is the keychain when available, otherwise the user file
with an explicit confirmation.

## System prompt

`internal/prompt` assembles the system prompt. It carries exactly one context
block at a time (active plan, goal, or profile) and rewrites assistant voice
guidance when `caveman` is on.

## TUI

`internal/tui` is a Bubble Tea app laid out Codex-style: message list,
streaming assistant/tool output, slash-command editor with autocomplete,
model/effort picker, and a status footer (session/tokens/cost/model).