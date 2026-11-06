# Patch tool

**Status:** new.

`apply_patch` changes several files in one call, all or none. The model writes
one patch; the harness parses it, finds every hunk in its file, builds every
file in memory, checks each against the session's record of what was read, and
only then writes. A failure at any step leaves every file byte-identical.
`Edit` is still the tool for one exact replacement. Use a patch when a change
spans hunks or files and a half-applied result would leave the tree broken.

## The envelope

The argument is `input`, one string:

```
*** Begin Patch
*** Update File: internal/a.go
@@ func Run
 context line
-line to remove
+line to add
 context line
*** Add File: docs/new.md
+every line of the new file
*** Delete File: old.txt
*** End Patch
```

The grammar is closed (`internal/patchfmt`). A line that fits no rule is an
error, never a guess.

| Part | Rule |
| --- | --- |
| Sections | `*** Update File: PATH`, `*** Add File: PATH`, `*** Delete File: PATH`. A path appears in one section only. |
| Update | One or more hunks. A hunk line starts with a space (context), `-` (remove) or `+` (add). A hunk needs at least one `-` or `+` line, and old and new text must differ. |
| Hunk marker | `@@` starts a further hunk. `@@ some line` names one whole line of the file that occurs once; the hunk is searched for after it. A `-1,3 +1,4 @@` header is accepted and its numbers are ignored. |
| End of file | `*** End of File` after a hunk says it sits at the end of the file. A hunk with only added lines must have it. |
| Add | Every line starts with `+`. The path must not exist. |
| Delete | Nothing follows. |
| Not supported | Rename and move. Binary files, symbolic links and modes. |

An empty line inside a hunk is a blank context line. Blank lines at a hunk's edge
are separator spacing and shorten the context only.

Limits: 64 files, 256 hunks, 1 MiB of patch, valid UTF-8 and no NUL byte. A file
is held to the same size limit as `Write` before and after the change.

## Anchoring

A hunk's old lines (context and `-` lines) must occur in exactly one place in the
file, compared as whole lines, never inside a longer line. The search walks a
ladder and the first rung that finds anything decides. One place applies. Two or
more fail with the count, and zero fail with a hint.

| Rung | Name in the result | Lines count as equal when | Needs `patch_tolerance` |
| --- | --- | --- | --- |
| exact | (not named) | the bytes are equal | any |
| line endings | `line endings folded` | CRLF, LF and a lone CR are one terminator | `whitespace` |
| trailing | `trailing whitespace ignored` | trailing spaces and tabs are also ignored | `whitespace` |
| indent | `indentation shifted` | one and the same leading whitespace is added to, or removed from, every non-blank line | `whitespace` |
| punctuation | `Unicode punctuation folded` | curly quotes, dashes, no-break and other wide spaces and the ellipsis are folded to ASCII | `unicode` |

The first three rungs are the same ladder `Edit` uses ([Tolerant Edit
matching](edit.md)). The punctuation rung maps a closed table of single
characters and never touches a letter, a digit, a line terminator or an
invisible character, so a hidden character in a file is not matched away. It can
match text that differs from the file in visible characters, which is why it
needs `patch_tolerance` set to `unicode`.

The new lines are fitted to the file: line terminators become the file's, and
under the indent rung each non-blank line gains or loses the same prefix. Mixed
line endings, a tab and space indentation delta, and new lines that lack the
indentation to be removed all fail the patch instead of guessing.

Hunks are located in the file as it is now, so their order in the patch does not
matter and an earlier hunk never moves a later one. Two hunks whose lines
overlap fail; merge them. A patch that leaves a file unchanged fails.

## Checks before any write

For every file, in order: the path resolves under a session root with symbolic
links followed (the same rule `Read`, `Write` and `Edit` use), an Update or
Delete target exists and is a regular file, an Add target does not exist, the
file was read in this session and has not changed since (the `Edit` rule), it is
text and within the size limit, and every hunk anchors. A symbolic link is never
written through. Only then are the files written, one atomic rename each.

If a write fails part way, every file already written is restored from memory,
created files are removed and the directories the patch made are removed. The
error says the files were restored, or lists any that could not be and tells the
model to `Read` them. Updates and adds are written before deletes.

After a successful patch each written file counts as read, so a follow-up edit
needs no new `Read`. The write-time diagnostics and the post-edit hook see every
written file (at most four files carry a diagnostics block).

## What the model sees

The result is composed by the harness from counts, paths and the rung each
relaxed hunk used:

```
applied the patch to 3 files (2 updated, 1 added, 0 deleted)
  update internal/a.go (2 hunks; hunk 2 matched with indentation shifted)
  update internal/b.go (1 hunk)
  add docs/new.md
```

An error names the hunk, the path and a line number or a measured kind of
difference (`line-ending`, `trailing-whitespace`, `indent`, `content`). It never
quotes file text or hunk text: file bytes are arbitrary and only `Read`
classifies them. Like `Edit`, the result is sanitised and not sent through the
classifier ([sanitization](sanitization.md)).

## Permissions

A patch touches many files, so the permission layer judges each one. Each file
is named as the single-file tool that would make the same change: an Update or
Delete is `Edit(path)` and an Add is `Write(path)`. A rule is also matched
under the name `apply_patch`. `permissions.Settings.ExplainAll` runs the
ordinary rule evaluation once per file and combines the answers:

- a deny, block or harness rule on any file withholds the whole call;
- an allow approves the call only when every file has one, so `Edit(src/**)`
  does not approve a patch that also edits `go.mod`;
- an ask rule on any file asks;
- with no rule on a file the default for a mutating tool applies, which is to ask.

The approval prompt names every file and shows one diff per file, computed from
the same plan the write runs. A decision-model answer to the ask sees the file
list. Pre-edit and post-edit hooks fire for `apply_patch` as for `Write` and
`Edit`. `apply_patch` is a mutating tool: plan mode and a `read_only` turn drop
it (guardrails off restores the full surface, as for `Edit`), and a profile
lists it by name in `tools` to allow it.

## Settings

| Key | Values | Meaning |
| --- | --- | --- |
| `patch_tool` | `true` (default), `false` | Offers `apply_patch`. `false` removes it from every surface. |
| `patch_tolerance` | `strict`, `whitespace` (default), `unicode` | The loosest rung a hunk may use. `strict` is exact whole lines only. An unknown word is read as `strict`. |

Your own settings layers set either key freely. A project settings file can only
narrow: it may set `patch_tool` to `false`, and its `patch_tolerance` applies
only when it is stricter than what your layers resolved to (`mergePatchTool` and
`mergePatchTolerance` in `internal/config/patch.go`, called from both
`Settings.Override` and `Resolve`). No flag, environment variable or tool
argument changes either key.

`/patch-tolerance` shows the level in the TUI, and `/patch-tolerance strict`,
`whitespace` or `unicode` sets it for this session. The choice is yours, so it
may be looser than a project file allowed. It lasts across settings reloads, is
never written to a settings file, and rebuilds the agent session so the next
turn uses it. It is not a web control.

## Surfaces

| Surface | Works |
| --- | --- |
| TUI | yes, with the multi-file approval diff and `/patch-tolerance` |
| Headless (`belai -prompt`), ACP, fleet workers, Pix sandbox sessions | yes: the same tool and gates. ACP reports it as an edit. A worker or profile with a `tools` allowlist must list `apply_patch`. |
| Code mode | callable from a script as a nested call, through the same permission and approval pipeline |
| Plan mode, read-only turns | no |

The tool is deferred: its definition is loaded through `ToolSearch` when the model asks
for it, and it runs when called by name either way (`defer_tools`).

## Limits

- No rename or move. Add the new file and delete the old one in one patch.
- A binary file, a file over the size limit and a symbolic link are refused.
- Nothing is persisted: no undo store and no state directory. The rollback uses
  memory for the length of one call.

## Where it lives

- `internal/patchfmt/patchfmt.go`: `Parse` and the bounds.
- `internal/patchfmt/anchor.go`, `fold.go`: `Plan`, the ladder, `Tolerance`.
- `internal/fuzzy/lines.go`: whole-line `Locate` (`Options.Lines`) shared with `Edit`.
- `internal/tools/patch.go`: `ApplyPatch`, the preview, the commit and rollback.
- `internal/permissions/permissions.go`: `Subject` and `ExplainAll`.
- `internal/agent/agent.go`: `decideCall`, `askSubject` and the settings applied in `NewSession`.
- `internal/config/patch.go`: the settings, accessors and merges.
- `internal/tui/patch_cmd.go`: `/patch-tolerance`.
