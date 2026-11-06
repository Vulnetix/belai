# File checkpoints (`/undo` and `/rewind`)

**Status:** new.

Before a prompt first changes a file, Belai keeps a copy of what the file held.
`/undo` puts back the files your last prompt changed. `/rewind` lets you pick
any earlier prompt and restore the code, the conversation or both to just
before it. The copies belong to you: no model has a tool for them, no model
sees them, and nothing is restored until you confirm.

Checkpoints are on by default in the interactive session. They are separate
from git: they cover uncommitted work, untracked files and files outside a
repository, and they never commit, stash or reset anything.

## Using it

| Command | Effect |
| --- | --- |
| `/undo` | Show what the last prompt that changed a file changed, and restore it after you press `y`. A second `/undo` goes one prompt further back. |
| `/rewind` | List the checkpoints, newest first. Pick one, then pick `code`, `conversation` or `both`, then confirm. |

Both commands refuse while a turn is running and when checkpoints are off. A
checkpoint exists only for a prompt that changed a file, so a prompt that only
read things has nothing to undo.

The confirmation lists every path: `restore` (write the earlier bytes),
`delete` (remove a file the prompt created) or `skip` with the reason. A line
tells you how many effects of shell commands could not be seen. A change you
made yourself in an editor since that prompt is overwritten by a restore, so
read the list first.

What `conversation` does: it is `/tree` continuing from the entry before that
prompt. The session file only grows, a branch marker records the move, and the
prompt goes back into the composer so you can reword it. It is not offered for
the first prompt of a session, which has no earlier entry.

### Undoing an undo

Before a restore writes anything it takes a safety checkpoint of every path it
is about to overwrite or remove. It appears in `/rewind` marked "safety copy".
If that copy cannot be kept, the restore stops before changing a file.

## What is kept

A checkpoint is one prompt. The first time the prompt touches a path, the
harness records what the path held: its bytes, or that it did not exist. Later
touches of the same path in the same prompt record nothing, so the checkpoint
holds the state from before the prompt.

| Change made by | How it is captured |
| --- | --- |
| `Write`, `Edit`, `apply_patch` | The files the call names are copied before it runs. If a copy cannot be kept the call is withheld with "checkpoint unavailable". |
| `Bash` | The harness reads, before the command, the files git already shows as changed (up to 20 MiB each and 64 MiB per command). After the command it compares git's status, takes the earlier bytes of each changed file from that read or from git's index, and records them. A file the command created is recorded as absent, so a restore deletes it. Outside a repository it uses the targets the harness can parse from the command. |
| Writes inside a `Code` script | Each nested `Write`, `Edit`, `apply_patch` and `Bash` call is captured like a direct call. |
| MCP tools, `SkillDraft`, anything else | Not captured. |

A shell command is observed after the fact, so before it runs the store is only
checked for room. If the store fails afterwards, the next mutating call is
withheld. Where the harness cannot see what a command did (git too slow, more
than 200 changed files already in the working tree, a command whose targets it
cannot parse outside git, changes above the working directory, more than 500
changed paths), the checkpoint is marked incomplete and the confirmation says so.
The note waits for the first file the prompt does record: a prompt whose only
changes went unseen leaves no checkpoint at all.

### Recorded by name only

A file is copied only if a model could have read it. These are recorded by name
and listed as "not restorable":

- a path a `Read` deny rule covers
- a credential file by name (`.env`, key and token files, the names
  `internal/locate` treats as sensitive)
- a binary file (a NUL byte in the first 8000 bytes, or a known binary extension)
- a symbolic link or a file that is not a regular file
- a file over 20 MiB
- a file that would not fit in the store's byte limit

## Settings

| Key | Meaning | Default |
| --- | --- | --- |
| `checkpoints.enabled` | Keep file checkpoints. | `true` |
| `checkpoints.max_per_session` | Checkpoints kept per session. The oldest are dropped first. 1 to 500. | `50` |
| `checkpoints.max_bytes` | File bytes kept per session. The oldest checkpoints are dropped to fit a new copy. 1 MiB to 10 GiB. | `209715200` (200 MiB) |
| `checkpoints.retain_days` | Days a session's checkpoints are kept after its last change. 1 to 365. | `7` |

Your own settings layers and the flags set every key. A project settings file
can only switch checkpoints off: it cannot turn them on or change a limit or the
retention, because a lower limit would delete history you already have. A
project value that would do more is ignored with a note. `-no-checkpoints`
turns checkpoints off for one run. The per-file limit (20 MiB) is fixed.

An out-of-range value in your own file fails validation and names the key.

## Storage and privacy

The store is `<state dir>/checkpoints/<project key>/<session id>/`, with mode
0700 directories and 0600 files: `blobs/<sha256>` for file bytes (identical
content is stored once) and `index.jsonl` for the records.

- It is part of the state directory, so the OS sandbox hides it from every
  command.
- The file tools refuse a path under it, for reads and writes. `Glob` leaves it
  out of listings.
- Its text goes to no model, transcript, telemetry, audit log or sync. The
  index holds absolute paths, hashes and sizes, and a prompt's first line
  (cleaned to one line, 80 characters).
- Each blob is checked against its hash before it is written back, and an index
  line that does not validate is skipped.
- Sessions whose index has not changed for `retain_days` are deleted when the
  next session starts. The running session is never deleted. A link in the store
  is not followed.

## Restoring safely

A restore writes through a temporary file and a rename, and keeps the recorded
permission bits (a file a command deleted comes back with the mode git records,
or `0644` where unknown). Each path is re-checked at the moment of writing:

- its real location, after resolving symbolic links, must be inside a
  confinement root (the working directory and any added directory)
- a path a `Write` or `Edit` deny rule covers is skipped
- a path that has become a directory or a symbolic link is skipped
- a created file is deleted only if it is still a file or link; a directory is
  never removed

`/undo` and `/rewind` use only your own confirmation. They are never put to the
decision model, and a web session, a headless run and an ACP session do not have
them: the commands are not in the remote-control table, and those surfaces keep
no checkpoints.

## Surfaces

| Surface | Checkpoints |
| --- | --- |
| Interactive TUI | Kept, with `/undo` and `/rewind`. |
| `belai -prompt`, `belai acp`, `belai rc` sessions, fleet workers | Not kept: they have no command that could restore a file. |
| Subagents | Read-only, so they change no file. |

## Reference

These are the exported functions of `internal/checkpoint`:

- `Open` opens a session's store and `Unavailable` returns a store that refuses
  every save, which is what a store that cannot be opened becomes.
- `Tracker.BeginTurn` marks a prompt, `Tracker.Seal` closes it, `Tracker.Ready`
  checks the store can take a copy, `Tracker.Save` copies one file before a
  call, and `Tracker.SaveBaselines` records the earlier state of the files a
  shell command changed.
- `Tracker.List` and `Tracker.Latest` list checkpoints, `Tracker.Plan` computes
  a restore without changing anything, `Plan.Count` totals it and
  `Tracker.Apply` carries it out after taking the safety checkpoint.
- `Tracker.SetPolicy`, `Tracker.SetLimits` and `Tracker.Dir` configure and
  locate the store, and `PolicyFor` builds the policy from a session's roots and
  rules.
- `Sweep` deletes the sessions older than the retention.

The settings live in `internal/config/checkpoints.go`; the capture is in
`internal/agent/checkpoints.go` and `internal/filediff/baselines.go`; the
commands are in `internal/tui/checkpoints.go` and `internal/tui/rewind_view.go`.
