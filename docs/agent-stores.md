# Agent stores

`SearchSessions`, `ReadSession` and `SearchMemory` recover context that was
already gathered — by signet or by any other coding agent on this machine —
without a fresh Explore round trip. They are read-only, path-free tools backed
by a curated, deterministic registry of known agent store locations in
`internal/agentstore`.

A match is a **record of what some agent once wrote**, never a fact about the
current repository. Confirm it against the live files before acting on it.

## The confinement property

The confinement boundary is a fixed root set (`internal/tools/cwd.go`). These
three tools read other agents' stores *outside* that root set, but they do not
widen the boundary:

- **No path argument crosses from the model into these tools.** Their schemas
  accept a regex, an agent name, a session id, a turn range, and filters. Every
  filesystem path is resolved from the static registry in
  `internal/agentstore/registry.go`.
- **There is no code path that turns model input into a path.** So the tools
  cannot be used as a general read primitive for `/etc/shadow` or anything else
  outside the registry.
- **The registry is reachable only from these three tools.** `Read`, `Grep`,
  `Glob`, `Bash` and the natives are untouched.

Session text *is* arbitrary content written by other models — the textbook
prompt-injection carrier. `KindAgentStore` is therefore in
`tools.classifierKinds` unconditionally, alongside `KindRead` and `KindRemote`.
Every result is sanitised (delimiter markup stripped) and then classified
before it can be promoted.

## Registry

One entry per agent, in deterministic order. `~` is the user's home directory;
a memory pattern that does not start with `~` is resolved against the session
working directory.

| Agent | Sessions | Prompts | Memory |
| ----- | -------- | ------- | ------ |
| `signet` | `~/.vulnetix/signet/sessions/*/*.jsonl` | — | `.vulnetix/goals`, `.vulnetix/prompts`, `.vulnetix/plans` |
| `claude-code` | `~/.claude/projects/*/*.jsonl` | `~/.claude/history.jsonl` | `~/.claude/CLAUDE.md`, `~/.claude/projects/*/memory/*.md` |
| `codex` | `~/.codex*/sessions/**/rollout-*.jsonl` | `~/.codex*/history.jsonl` | `~/.codex*/memories/**`, `~/.codex*/AGENTS.md` |
| `pi` | `~/.pi/agent/sessions/*/*.jsonl` | — | `~/.pi/agent/plans/*/*.md` |
| `goose` | `~/.local/share/goose/sessions/sessions.db` | — | `~/.config/goose/**/*.md` |
| `opencode` | `~/.local/share/opencode/opencode.db`, `~/.opencode/opencode.db` | — | `~/.config/opencode/AGENTS.md` |
| `copilot-vscode` | `~/.config/Code*/User/workspaceStorage/*/chatSessions/*.json`, `~/.config/Code*/User/globalStorage/emptyWindowChatSessions/*.json` | — | — |
| `copilot-cli` | `~/.copilot/session-state/*.json` | — | — |
| `antigravity` | `~/.gemini/antigravity-cli/conversations/*` | `~/.gemini/antigravity-cli/history.jsonl` | `~/.gemini/antigravity-cli/brain/*/*.md` |
| `generic` | — | — | `~/CLAUDE.md`, `~/AGENTS.md`, `~/.cursorrules`, `~/.config/*/AGENTS.md`, workspace-root `CLAUDE.md`/`AGENTS.md` |

Absent paths are skipped. A `sqlite3` binary missing on `$PATH` marks the
SQLite agents `unavailable` with that reason rather than failing the call —
no new Go dependency, matching the shell-out precedent in `Grep`.

## Dialects

- **Claude Code** — every line self-describing: `type`, `message.role`,
  `message.content[]`, `sessionId`, `cwd`, `timestamp`, `gitBranch`. Project is
  also in the directory slug (`-home-chris-GitHub-signet`).
- **Codex rollout** — `cwd` and session id appear **only on line 1**
  (`type:"session_meta"`). Turns are `type:"response_item"` with
  `payload.type=="message"`, `payload.role`, `payload.content[].text`. A
  streaming scanner reads line 1 before it can attribute a match.
- **pi** — same shape: line 1 `{type:"session", id, cwd}`, then `type:"message"`
  with `message.role`/`message.content[]`. Project also in the dir slug
  (`--home-chris-GitHub-signet--`).
- **signet** — `session.Entry` records; the project key is `session.WorkdirKey`
  and files are read through the existing `session.Store` rather than a new
  parser.
- **goose** — `messages(role, content_json)` joined to `sessions(working_dir)`.
- **opencode** — text lives in `part.data` (opaque JSON), joined via `message`
  to `session(directory)`.
- **VS Code Copilot Chat** — `requests[]` in a single JSON object; each request
  is a user `message` and an assistant `response`.

## Prompt indexes

`prompts_only=true` searches only the `history.jsonl` prompt indexes — a fast
tier. Each line is one prompt and carries its own session id and project, so
matches stay attributed.

## Attribution

Every hit carries `agent`, absolute `path`, `session id`, `turn`, `role`,
`timestamp`, and `project` (the recorded working directory), so the true source
can be cited.

## Caps

Constants in `internal/agentstore/adapter.go`, tunable:

| Cap | Value |
| --- | ----- |
| Matches | 200 |
| Snippet | 2 KiB |
| Total payload | 256 KiB |
| Files scanned | 5000 |
| Deadline | 10 s |

Truncation and deadline hits are stated in the result, never silent. The rg
prefilter is a first pass over the narrowed file set (matching raw JSONL
lines); the adapter is authoritative for extracted text, so a regex that
depends on JSON-escaping differences can fall back to the streaming scanner.

## Search shape

1. Narrow the file set first, from path and mtime only — agent filter, project
   filter (default: current workspace root), `since`/`until`.
2. Prefilter with `rg` over the narrowed set when `rg` is on `$PATH`; fall back
   to `bufio.Scanner` + `regexp` streaming otherwise.
3. Parse only the files rg flagged, through the adapter, to attach role/turn/
   timestamp/cwd attribution.
4. Apply caps and a wall-clock deadline.

## Non-goals

- `~/agent-transcripts-*.tar.zst` archives are not searched (would need
  decompression on every call).
- No write path; nothing here modifies another agent's store.
- No new Go module dependency; SQLite is the `sqlite3` binary or nothing.
- No index or cache in v1; add one only if measurement says the rg prefilter is
  too slow.