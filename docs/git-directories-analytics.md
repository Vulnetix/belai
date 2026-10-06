# Git directories analytics

This report describes how Belai analyzes directories in and around a git
checkout: how it detects the repository, discovers neighbouring checkouts,
computes per-directory file counts (the `layout` summary), and chooses the
directories a remote-control daemon offers. All of it is read-only: none of
these packages write, fetch, or follow symlinks.

## 1. The directory analytics: `repomap.layout`

The summary a session sees as its `layout: bench(4) bin(25) cmd(22) …` line is
computed by `layout` in `internal/repomap/repomap.go`. For every top-level
directory of the repository root it produces one `DirSummary`:

```go
// DirSummary describes a top-level directory and how many entries it holds.
type DirSummary struct {
	Name  string
	Files int
}
```

`Files` is a recursive count of the files under that directory. The walk is
bounded and skip-dir aware:

- `scanTimeout` 5 s caps the whole scan, shared by every walk in it. `maxFiles`
  2000 caps each top-level directory separately, so a directory with more
  files reports exactly 2000 and never a larger number; the count is then a
  floor, not a total. `filepath.SkipAll` stops a walk outright rather than
  stat-ing the rest of the tree;
- `skipDirs` — `.git`, `node_modules`, `vendor`, `target`, `dist`, `.vulnetix`,
  `.idea`, `.vscode` — are never descended;
- hidden directories (`.` prefix) are excluded from the listing.

Results are sorted by name and then cut to the first 24, so a repository with
more than 24 top-level directories shows the 24 that sort first, and the layout
line stays a compact summary, never a full listing.

The companion `languages` walk in the same package makes one pass over the whole
repository, stopping after 2000 files in total, and counts file extensions into
`LangCount{Ext, Files}` (lower-cased, `(none)` when there is no extension),
sorted by count descending and capped at the top 12 — the `languages: go(1190)
json(45) md(41) …` line.

## 2. Detecting the repository: `internal/gitinfo`

Before any directory is analyzed, `gitinfo.Detect(workdir)` (`internal/gitinfo/
gitinfo.go`) walks up from the working directory to find a `.git` entry
(`findGit`):

- a `.git` **directory** is accepted only when it contains a `HEAD`, so a stray
  empty `.git` in a parent directory cannot make that whole parent the
  repository;
- a `.git` **file** is a linked worktree and is read for its `gitdir: <path>`
  line.

It then reads `HEAD`: `ref: refs/heads/<branch>` sets `Info.Branch`; anything
else is detached and `Info.Head` carries the short SHA (first 7 characters).
`gitinfo.OriginURL` reads `remote.origin.url` from the repo config (or the
`commondir` for a linked worktree) without shelling out.

## 3. Discovering nearby checkouts: `internal/repoindex`

`repoindex.Scan` (`internal/repoindex/repoindex.go`) indexes the git checkouts
among the sibling directories of the working directory (depth two). It is
bounded and never follows symlinks:

- `scanTimeout` 3 s, `maxDirs` 500, `maxEntries` 200, `probeTimeout` 500 ms;
- `skipDirs`: `node_modules`, `vendor`, `target`, `dist`;
- `tryIndex` rejects a symlinked `.git` (`os.Lstat`).

Each checkout yields an `Entry{Owner, Name, Host, Path, Branch}`; `Owner` and
`Name` are parsed from `git config --get remote.origin.url` via the scrubbed,
bounded `repoindex.RunProbe`. `Index.Lookup` resolves `owner/repo`, or a bare
`repo` name only when it is unique across the index (case-insensitive).

## 4. Directories offered for remote control: `internal/rc`

`rc.Collect` (`internal/rc/dirs.go`) decides which directories the `belai rc`
daemon advertises to the website: every trusted project that still exists, plus
`--dir` arguments, with the directory `rc` was started in placed first when it
is offered. A directory is offered only when a worker would actually start
there — `rc.Startable` accepts a directory outside any git repo, the repository
root itself, or a directory whose repository root is trusted. The result is
uploaded as `RCInfo.Dirs` (`RCDir{Path, Name, Source}`) by
`internal/sessionsync`.

## Bounds summary

| Concern | Where | Limit |
| --- | --- | --- |
| repository map scan | `repomap.Scan` | 5 s overall / 2000 files per top-level dir and for the language walk |
| layout listing | `repomap.layout` | 24 top-level dirs |
| language counts | `repomap.languages` | 12 extensions |
| checkout discovery | `repoindex.Scan` | 3 s / 500 dirs / 200 entries |
| remote probe | `repoindex.RunProbe` | 500 ms / 4 KiB output |