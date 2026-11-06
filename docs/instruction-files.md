# Instruction files

**Status:** new.

A repository can keep notes for coding agents in `AGENTS.md` or `CLAUDE.md`.
Belai attaches the ones at the repository root to every agent-mode turn, and
attaches one in a subdirectory the first time a tool touches a path beneath it.
`/init` drafts a root file when there is none. The harness decides which files
exist, which apply, when each is delivered and how many bytes ride. The model
only reads the text. No model is asked whether a file is worth sending.

## What rides, and when

| File | Delivered | How |
| --- | --- | --- |
| `AGENTS.md`, `CLAUDE.md` at the repository root | at the start of each agent-mode or code-mode turn, and on plan and goal turns as before | the context prefetch (`internal/agent/prefetch.go`) |
| `AGENTS.md`, `CLAUDE.md` in a subdirectory | at the next round boundary after a tool call touched a path beneath it | a user turn that carries the files as attachments and a directive naming them |

A call touches a directory when it names a path under it as `file_path` or
`path` to `Read`, `Edit`, `Write`, `Grep`, `Glob` or `LS`, or when the diff
recorder saw a file there change (which covers `apply_patch`). A path that does not exist yet, such as a new file for `Write`,
stands for its parent directory. A call that was refused, withheld or failed
touched nothing.

For a touched directory the harness lists the instruction files in every
directory from the repository root down to it, outermost first, `AGENTS.md`
before `CLAUDE.md` in one directory. The root is left out because the prefetch
already covers it. A file is delivered only when it passes the same test as a
prefetched file (`prefetchEligible`): it is a regular, non-empty file within
`instructions.file_bytes`, it was not withheld earlier in the session, its
content is not already in the conversation, and the permission rules allow a
`Read` of it without asking.

There is no delivered-once table. The read index answers whether the model
still holds a file: it vouches only for content that is live in the
conversation. A file is therefore attached again after compaction, `/clear` or a
new turn whose history was rebuilt without attachments, and not while its
attachment is still above. A file that changed on disk is attached again.

## What the harness checks

- **Root-relative walk.** The walk starts at the touched directory and goes up to
  the root, never above it. An absolute path, a `..` that climbs out, a NUL byte
  and a path through one of `.git`, `node_modules`, `vendor`, `target`, `dist`,
  `.vulnetix`, `.idea` or `.vscode` list nothing (`repomap.NestedAgentsFiles`).
- **No links.** Every directory step and the file itself are checked with
  `lstat`. A symbolic link to a directory ends the walk, and a link in place of
  a file is not listed. The `Read` that follows confines the path to the session
  roots after symlinks as well.
- **Primary root only.** Nested delivery covers the primary working directory.
  A directory added with `/add-dir` is not walked.
- **Same gate as a `Read`.** The text is classified as `tools.KindRead` unless
  the posture ignores tool results, and then it is sanitised only and the
  classifier is not called. A withheld file is flagged like a withheld `Read`
  (so a `Grep` row from it is withheld too), counted in the turn's withheld
  verdicts and never attached. Nothing new is added to `tools.classifierKinds`.
- **Never the system block.** The text rides on a user turn as a sealed `file`
  attachment, and the directive names paths only, sanitised.
- **Never a permission.** A file does not widen the tool allowlist, a
  permission rule, the sandbox or the confinement roots, and the directive says
  it cannot override the user's request or the harness's rules.
- **Bounded.** At most `instructions.max_files` files and
  `instructions.total_bytes` on-disk bytes in one turn, the root files
  included, and at most 64 directories queued between two rounds.
- **Who gets it.** A session that is an explore subagent, or is scoped to a
  handoff's paths, gets neither the root attachment nor the nested one.

## Settings

| Key | Default | Range |
| --- | --- | --- |
| `instructions.max_files` | 12 | 1 to 40 |
| `instructions.file_bytes` | 49152 | 1024 to 262144 |
| `instructions.total_bytes` | 131072 | 1024 to 1048576 |

| Key | Default | Meaning |
| --- | --- | --- |
| `instructions.enabled` | on | `false` stops the harness attaching instruction files. The model can still `Read` one. |
| `instructions.nested` | on | `false` keeps the root files and stops the per-directory delivery. Off whenever `enabled` is off. |

Your own settings layers set any key within its range. A project settings file
may turn `enabled` or `nested` off and may lower a number; its `true` and any
higher or out-of-range number are ignored and noted at startup
(`mergeInstructions` in `internal/config/instructions.go`, called from both
`Settings.Override` and `Resolve`). A value outside its range in your own
layers fails the load. There is no flag, environment variable or tool argument
for these keys, and a model cannot change them.

The changed-file prefetch of plan and goal turns keeps its own constants (12
files, 48 KiB each, 128 KiB together). The `instructions.*` caps apply to the
instruction files in that prefetch too.

## `/init`

`/init` drafts a root `AGENTS.md`. It runs in the TUI only.

1. If the repository root already holds an `AGENTS.md` or a `CLAUDE.md` (a
   symbolic link counts), nothing is sent and the command says so. A file you
   wrote is never the target of a rewrite.
2. In plan mode it declines and asks you to switch with `/mode agent`, because
   it writes a file.
3. Otherwise it sends one ordinary agent turn as your typed prompt: a fixed
   request plus the repository map's facts (detected commands, test suites,
   layout, languages, entry points), sanitised. The facts are paths and counts,
   never file contents. The turn goes through admission, mode selection and the
   agent picker as any prompt does.

The model drafts the prose and writes it with the `Write` tool to `AGENTS.md`.
The file reaches disk only through that call, so the ask gate, permission
rules, hooks, hazard rails and the sandbox apply unchanged, and you approve the
write.

## Where it lives

- `internal/repomap/repomap.go`: `AgentsFile.Dir`, `InstructionFileNames`,
  `NestedAgentsFiles`.
- `internal/agent/instructions.go`: the touch queue, the per-turn caps,
  `deliverNestedInstructions`.
- `internal/agent/prefetch.go`: `prefetchEligible` (shared by every
  attachment), `prefetchCandidates`, `startPrefetch`.
- `internal/agent/pass.go`: where touches are noted and the files are delivered.
- `internal/config/instructions.go`: `InstructionsSettings`, the accessors and
  the project-layer merge.
- `internal/tui/init_cmd.go`: `/init`.

## Edge cases

- A repository with 200 nested instruction files delivers at most
  `instructions.max_files` in a turn; the rest are left for the model to read.
- A file the classifier withholds is not retried in the same session: it is
  flagged, and the next touch skips it.
- An instruction file that is also a changed file is attached once.
- A file the user `@`-attached is skipped by the root prefetch.
- Out-of-range numbers never reach the delivery code: they read as the default.
