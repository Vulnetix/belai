# Symbol map

**Status:** new.

Belai can tell a model where a name is declared and what a file declares
without the model reading the file. The harness parses the declarations of the
working directory's eligible files into an index. Two read-only tools, `Outline`
and `Symbol`, serve it. The model chooses only which file or name to ask about;
the harness parses, indexes, ranks and bounds, and no model is asked whether
anything is safe.

The index is built the first time either tool is called. A session that never
calls one pays nothing, and an index that is off, missing or stale degrades to
`Grep` and never blocks a turn.

## What is indexed

`internal/symindex` builds the index over the inventory `internal/locate`
already keeps. A file that is ignored by `.gitignore` or `.ignore`, hidden,
symlinked, binary, over the inventory's size limit, credential-bearing or under
a dependency directory is not in the inventory, so it is never parsed, listed or
named. Data and prose files (Markdown, JSON, YAML, TOML, HTML, CSS and the like)
are left out too, because a declaration scan over them finds lines that only
resemble code.

A Go file is parsed with the standard library's parser. Any other text file is
scanned line by line for a declaration keyword (`func`, `function`, `def`, `fn`,
`class`, `struct`, `interface`, `trait`, `type`, `enum`, `impl`, `const`, `let`,
`var`, `module`, `object`). The scanner can miss a declaration or name a
commented-out one; that affects what a lookup finds, never what is allowed.

An entry holds a name, a kind, a container, a line and an end line, and nothing
else: no body, no comment, no signature. The kind is one of a closed set:
`func`, `method`, `type`, `struct`, `interface`, `class`, `enum`, `trait`,
`impl`, `const`, `var`, `module` and `object`. The container is the receiver
type of a Go method. The end line is known for Go and is absent for a scanned
file.

## The tools

`Outline` takes `file_path` (relative to the working directory, or absolute
under the session root) and lists the file's declarations in source order with
their line spans:

```text
internal/srv/server.go: 3 declaration(s)
3-9  struct Server
11  method Server.Handle
20  func helper
```

`Symbol` takes `name`, an optional `kind` and an optional `limit`, and lists
where the name is declared, best match first: an exact name, then the same name
in another case, then names that start with it, then names that contain it. Rows
of one rank are ordered by path and line. `Type.Method` finds a method of that
receiver.

```text
2 declaration(s) matching "Handle":
internal/srv/client.go:5  method Client.Handle
internal/srv/server.go:11  method Server.Handle
```

Both tools are read-only. They are offered in plan mode and on `read_only`
turns, they run without an ask, they run concurrently with other reads, and a
code-mode script may call them as `tools.Outline(...)` and `tools.Symbol(...)`.
A permission rule can name them: `Outline(internal/secret/*)` or `Symbol`. They
are deferred behind `ToolSearch` like most tools, and the work-discipline text
tells the model they exist, and only when the session has them.

A call fails closed:

- `Outline` resolves the path through the session's working-directory rules
  first. A path outside every root, through a symlink out of the root, or in an
  added directory (only the session root is indexed) is refused before the index
  is asked.
- A `name` that is not letters, digits and underscore (with at most one dot for
  `Type.Method`), longer than 120 bytes, or a `kind` outside the closed set is
  refused.
- `limit` can lower the result count and never raise it above
  `symbol_index.max_results`.
- If the index cannot be built within its time bound, the result is one line,
  `symbol index unavailable for this call: use Grep to search for the name`,
  with no partial rows. This is the only result that points at another tool.

## Bounds

| Bound | Value |
| ----- | ----- |
| Time for one build or refresh | 5 seconds |
| Files in one index | `symbol_index.max_files`, 2000 by default |
| Declarations kept for one file | 200 |
| Declarations in one index | 20000 |
| Bytes read to find a file's declarations | 256 KiB |
| Length of a name | 120 bytes |

When the file or declaration bound stops the index short, every result says how
many of the source files it covers, so the model knows a missing name may be in
a file the index does not hold. A build that runs past the time bound is not
served partly. A refresh re-parses only files whose size or modification time
changed. Within `symbol_index.refresh_secs` a built index is reused, but a file
named by a result row is checked again before the row is printed, and an
`Outline` of a file the index does not hold forces a refresh at most every two
seconds, so a file the model has just written is found.

## Trust model

A declared name is the repository's own text, so a hostile file can put an
instruction in one. Three rules follow, and each is pinned by a test.

- **Names never ride unclassified.** They never enter the repository map, the
  system block or a directive (`TestRepoMapBlockNeverCarriesADeclarationName`).
  They reach a model only in a `Outline` or `Symbol` result, whose kind
  `tools.KindSymbol` is in `tools.classifierKinds`, so the result goes through
  the security classifier before promotion, as a `Read` of the same file would
  (`TestSymbolResultIsAlwaysClassified`). With guardrails off the classifier is
  skipped and the sanitiser still runs.
- **One sanitiser sink per destination.** A name and a container are reduced to
  ASCII identifier characters with `sanitize.Ident` (120 bytes), a kind outside
  the closed set is dropped, a path that fails `sanitize.PathText` drops its
  row, and the whole result then goes through `sanitize.Sanitize`. The index
  also refuses at build time any name that is not an identifier.
- **A withheld file stays withheld.** When the classifier withheld an earlier
  `Read` of a file, its rows are replaced by a count: `3 declaration(s) from
  x.go withheld: an earlier read of this file was classified ...`. The line
  points at no other tool. A file a `Read` deny rule covers is not listed at all.
  Both filters run at query time, in the session
  (`TestSymbolRowsFromAWithheldFileAreReplacedByACountThatPointsNowhere`,
  `TestReadDenyRuleHidesAFileFromSymbolAndOutline`), and the cache never stores
  either state.

The index calls no model and no network.

## The cache

A built index is cached at `<state dir>/symbols/<directory key>.idx`, in the
user's state directory and never in the repository. The file is mode `0600`
in a `0700` directory, is written through a temporary name and a rename under
the state directory's file lock, and is refused if the path or the directory is
a symlink. It has a magic line, one JSON document and a SHA-256 trailer.

The cache is untrusted on load. A file with the wrong magic or checksum, an
unknown field, another root, another `symbol_index.max_files`, a path that is
absolute, climbs out, or names a file the inventory would refuse, a name that is
not an identifier, a kind outside the closed set, an impossible line, or more
rows than the bounds allow is discarded as a whole and the index is built again
(`TestDecodeRefusesAnythingItCannotTrust`). Cache files are never written to
sync, telemetry or the session record.

## Settings

The `symbol_index` block is read from your own settings layers. A project file
can only turn the index off; its numbers are dropped with a note, so a
repository cannot make a host index or return more.

| Key | Meaning | Default |
| --- | ------- | ------- |
| `symbol_index.enabled` | Build the index and offer `Outline` and `Symbol`. | `true` |
| `symbol_index.max_files` | Files one index covers. 100 to 10000. | `2000` |
| `symbol_index.max_results` | Rows one `Symbol` call returns at most. 1 to 200. | `50` |
| `symbol_index.refresh_secs` | Seconds a built index is reused before changed files are looked for. 5 to 3600. | `60` |

A value outside its range fails validation and names the key. There is no flag
or environment variable.

## The `/symbols` command

`/symbols` shows the index to you, in the TUI. With no argument (or `status`) it
prints the size. `/symbols <name>` runs the `Symbol` lookup, `/symbols <path>`
runs `Outline` for a file, and `/symbols refresh` re-reads the index now. The
answer is drawn by the same tools over the same source, so it never shows more
than a model would be shown, a `Read` deny rule hides a file from it, and it is
written to the screen only: an ephemeral row that never reaches a model turn,
the session record, sync or telemetry (`TestSymbolsRowsAreEphemeral`). No model
can start it. Headless and ACP sessions have the tools but no slash command.

## Surfaces

The tools are on the surface of any session whose tool allowlist does not
exclude them: the interactive TUI, `belai -prompt`, `belai acp`, fleet workers
and `belai rc` sessions. A profile's `tools` list must name `Outline` and
`Symbol` to keep them. A Pix assistant persona has a fixed read-only list that
leaves them out. Explore subagents have them on their read-only surface and the
shared index, with their own withheld-file state. A handoff subagent's path scope is
checked against the call's subject, which for `Symbol` is a name and not a
path, so it is refused there.

## Files

- `internal/locate/decls.go`: the declaration parser and scanner, with `Decl`
  and the closed `Kinds` set.
- `internal/symindex`: `Build`, `Index.Outline`, `Index.Lookup`, `Index.Touch`
  and the cache (`Encode`, `Decode`, `Load`, `Save`).
- `internal/tools/symbol.go`: `Outline`, `Symbol`, `SymbolHub` and the answer
  formatting.
- `internal/agent/symbols.go`: the session's source, the shared per-directory
  holder, and the withheld-file and deny filters.
- `internal/config/symbolindex.go`: the settings block.
- `internal/tui/symbols_cmd.go`: `/symbols`.
