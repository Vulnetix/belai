# The workspace changes pane (`/diff`)

`/diff` opens a read-only pane of the working tree's git changes: staged,
unstaged and untracked files together, a one-line summary (file counts, lines
added and removed), a file list and the selected file's hunks, rendered by the
same diff renderer the transcript uses (`internal/filediff`,
`components.DiffView`).

| Key | Does |
| --- | --- |
| `left`, `right`, `p`, `n`, `tab`, `shift+tab` | previous or next changed file |
| `up`, `down`, `k`, `j` | scroll the diff a line |
| `pgup`, `pgdn`, `ctrl+u`, `ctrl+d`, `space` | scroll a page |
| `home`, `end`, `g`, `G` | top and bottom of the diff |
| `r` | reload from the repository |
| `q`, `esc` | back |

The list marks each file `S` (staged), `M` (unstaged), `±` (both), `?`
(untracked), `!` (conflict) or `◌` (hidden, below). A file's diff is the
committed `HEAD` content against the file on disk, so a file staged and then
edited again shows both changes together.

## What it reads, and how

`internal/workdiff` runs a fixed set of read-only git commands (`rev-parse`,
`status --porcelain=v1 -z`, `cat-file -s`, `show`) hardened like the fleet's
git calls: repository hooks and fsmonitor off, no `file://` transport, no
credential prompt, no optional locks (so `status` never rewrites the index),
the scrubbed environment (`proc.ScrubbedEnv`), its own process group and no
stdin. Output is bounded.

- The working-tree side is read by Belai, never through a symlink; a symbolic
  link, a special file or a file over 1 MiB is listed with a note and no diff.
- At most 300 files are expanded; the summary counts the rest.
- Binary files are listed as binary.
- File text and paths have terminal escape sequences, control and bidi runes
  removed before they are drawn.

## Where the text goes

Nowhere but the screen. It never reaches a model, the transcript, the session
record, telemetry, the audit log or session sync, and the command has no
model-facing tool. Because it is not model-bound it is not classified.

## Read deny rules

A path a `Read` deny rule covers is conservatively **hidden**: the pane lists
its name and status (`◌`) and never reads its content, from disk or from git,
so a denied file's text is not on the screen, and neither is its change count.
The rule is tried against the repository-relative path and the absolute path.
`ask` and `allow` rules do not hide a file, because the person looking is the
user. The names of denied files are shown on purpose so a change to one is not
invisible; a rule that should hide the name too is not supported.

## Outside a repository

The pane says the directory is not a git repository. A clean tree says so too.
