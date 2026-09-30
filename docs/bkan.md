# The BKAN file format

`BKAN` is the on-disk format of Belai's global kanban board: the single file
`~/.vulnetix/belai/kanban`, or `$BELAI_HOME/kanban` when `BELAI_HOME` is set.
This page covers:

- how that file is built, byte by byte
- why it is built that way
- the Go types it holds
- how it is read and written safely

The feature itself (lists, tools, the TUI and sync) is in
[kanban.md](kanban.md). The code is `internal/kanban`: `format.go` for the
encoding and `kanban.go` for the types and the store.

`BKAN` is Belai's own format, not a standard one. The name is "**B**elai
**KAN**ban".

## Layout

A board file is four sections, back to back, with no padding:

```
offset  size  field
0       4     magic      the ASCII bytes "BKAN" (42 4b 41 4e)
4       2     version    uint16, big endian (currently 00 03; 00 01 and 00 02 are still read)
6       n     payload    encoding/gob stream of one kanban.Board
6+n     32    checksum   SHA-256 of the payload bytes only
```

- **Header.** The magic and the version together make up the 6-byte header.
- **Payload.** There is no length field. The payload is everything between
  the header and the last 32 bytes of the file.
- **Minimum size.** A file shorter than 38 bytes (header plus checksum) is
  rejected before anything else is read.

The constants are in `format.go`:

```go
const (
	magic         = "BKAN"
	formatVersion = 1
	headerLen     = len(magic) + 2
)
```

### Why each section exists

| Section | Question it answers | What happens on a mismatch |
|---|---|---|
| magic | Is this a kanban board at all? | `not a kanban board (bad magic)` |
| version | Can this build of Belai read it? | `unsupported board version N (this Belai reads 1 to 3)` |
| payload | What is on the board? | a gob decode error |
| checksum | Is the payload exactly what was written? | `checksum mismatch` |

A gob stream has no integrity check of its own. A flipped bit inside a
string's bytes still decodes, and simply yields different text. A truncated
stream usually fails to decode, but not always at a useful boundary. The
checksum turns both kinds of damage into one clear error.

Any of these failures makes the load return `ErrCorrupt` (`kanban.ErrCorrupt`), wrapped with
the path and the reason. See [Failing closed](#failing-closed) for what Belai
does next.

## Walking through a real file

This board holds one item, which has one history entry:

```go
kanban.Board{Cursor: 7, Items: []kanban.Item{{
	ID: "3f9a2c00-0000-4000-8000-000000000000", Title: "Run the tests", List: kanban.Review,
	Project: "belai", Created: 1790498651688, Updated: 1790498651688, Dirty: true,
	History: []kanban.Move{{ID: "m1", To: kanban.Review, At: 1790498651688}},
}}}
```

`kanban.Encode` turns it into 519 bytes. Here they are, with the sections
marked.

**Header** (bytes 0–5):

```
00000000  42 4b 41 4e 00 01                                 |BKAN..|
```

The magic `BKAN`, then version `00 01`. This walk-through is a version 1
file, before the routing and claim fields existed; a version 2 file has the
same shape with more fields in the `Item` definition.

**Gob type definitions** (bytes 6–0x170). Before sending any value, gob
describes every type the value uses: each type's name, and each field's name
and type. This is why the file is self-describing. It takes 367 bytes, the
bulk of a small board:

```
00000006  28 7f 03 01 01 05 42 6f 61 72 64 ...   |(.....Board...|
            ^  ^ type id -64: "definition of type 64" (Board)
            message length 0x28 = 40 bytes
0000002e  1c ff 87 ... 5b 5d 6b 61 6e 62 61 6e 2e 49 74 65 6d   |...[]kanban.Item|
0000004c  ff be ff 81 03 01 01 04 49 74 65 6d ...                |....Item...|
            ^ length 190  ^ -65: the definition of Item, all 15 fields
0000010c  1c ff 85 ... 5b 5d 6b 61 6e 62 61 6e 2e 4d 6f 76 65   |...[]kanban.Move|
00000129  47 ff 83 03 01 01 04 4d 6f 76 65 ...                   |G...Move...|
```

Inside those definitions, each field is stored as its name plus a type id:

| Type id | Hex | Type |
|---|---|---|
| 2 | `02` | bool |
| 4 | `04` | int |
| 12 | `0c` | string |

The dump confirms this: `ID 01 0c`, `Created 01 04`, `Dirty 01 02`.

**The value** (bytes 0x171–0x1e6). One message holding the board itself:

```
00000171  75                       message length 117
00000172  ff 80                    type id 64: a Board value
00000174  01 0e                    field +1 (Cursor)   int 7 (zig-zag: 0x0e >> 1)
00000176  01 01                    field +1 (Items)    slice of 1 element
00000178  01 24 "3f9a2c00-…"       field +1 (ID)       36-byte string
0000019e  01 0d "Run the tests"    field +1 (Title)
000001ad  02 06 "review"           field +2 (List)     Body is empty, so it is skipped
000001b5  01 05 "belai"            field +1 (Project)
000001bc  05 fa 03 41 c4 12 7c 50  field +5 (Created)  6-byte int = 1790498651688
000001c4  01 fa 03 41 c4 12 7c 50  field +1 (Updated)
000001cc  01 01                    field +1 (History)  slice of 1 element
000001ce  01 02 "m1"               field +1 (Move.ID)
000001d2  02 06 "review"           field +2 (Move.To)  From is empty, so it is skipped
000001da  01 fa 03 41 c4 12 7c 50  field +1 (Move.At)
000001e2  00                       end of Move
000001e3  02 01                    field +2 (Dirty)    true; ServerVersion is 0, so skipped
000001e5  00 00                    end of Item, end of Board
```

**Checksum** (the last 32 bytes):

```
000001e7  f6 8d f4 cc 99 1f 97 64 1f 96 9a fc 6d 2d a9 2c
          19 0d 78 fc 09 9c 9b 06 75 76 29 e1 9d ad 92 81   SHA-256(payload)
```

The gob wire rules that matter here:

- **Every message is length-prefixed.** A message starts with its byte
  length as an unsigned integer.
- **Unsigned integers.** A value below 128 is one byte. A larger value is a
  byte holding the negated byte count, followed by the value's big-endian
  bytes. For example, `fa` means "6 bytes follow".
- **Signed integers are zig-zag encoded.** The value is shifted left by one,
  and the low bit carries the sign. Type ids are signed: a negative id opens
  a type definition, and a positive id starts a value of that type.
- **Struct fields are sent as deltas.** Each field is the difference from the
  previous field number, then the field's value, and `00` ends the struct.
  **Fields holding their zero value are not sent**, which is why an empty
  `Body` or `Deleted: false` costs nothing.
- **Strings and slices are length-prefixed.** A string is its byte length then
  its bytes. A slice is its element count then its elements.

The type ids (64 for `Board`, 65 for `Item`, and so on) belong to one encoder
session, not to the format. Nothing may depend on them.

The full specification is the Go package documentation for `encoding/gob`.

### Size

| Board | Bytes |
|---|---|
| empty | 405: 6 header, 367 payload (the schema alone), 32 checksum |
| the one-item example above | 519 |
| a typical item after that | roughly 150–600 more, mostly its text |

The schema is written once per file, not once per item. Each `Encode`
starts a new gob stream, so every file is complete and can be read on its
own.

The theoretical worst case is large. The board holds up to 5000 items. Each
has a title of up to 200 runes and a body of up to 4 KiB, and keeps up to 50
history entries with notes of up to 1 KiB each. That is about 56 KiB per item,
or roughly 280 MB for a full board. Every write rewrites the whole file (see
[Writing](#writing)), so a board anywhere near that size would make each write
slow. Real boards are several orders of magnitude smaller. If boards ever grow
toward the limits, an append-only log format behind a new version number is
the way forward.

## The Go data structures

The payload is exactly one `kanban.Board`. gob stores every **exported** field
of these types, matched **by field name**:

```go
// Board is the whole persisted board.
type Board struct {
	// Cursor is the highest backend version pulled so far.
	Cursor int64
	Items  []Item
}

// Item is one card on the board.
type Item struct {
	ID    string // UUIDv4, unique across hosts
	Title string
	Body  string
	List  List
	// Provenance, stamped by the harness when the item was added.
	Project    string
	ProjectKey string
	Dir        string
	HostID     string
	SessionID  string

	Created int64 // unix ms
	Updated int64 // unix ms
	History []Move

	// Routing (version 2).
	Labels    []string
	Priority  int
	Assignee  string
	Parent    string
	DependsOn []string
	Hops      int

	// Claim (version 2), set only by the harness.
	ClaimedBy  string
	ClaimHost  string
	ClaimFrom  List
	LeaseUntil int64
	Attempts   int
	Branch     string
	PR         string

	// Security card, set only by the harness and never pushed.
	Finding string
	SeenRef string
	Verdict Verdict
	VEX     string

	// Acceptance gates (version 3), set only by the harness and never pushed.
	Gates []Gate

	remoteAgent bool // in memory only; gob never stores it

	// Sync state. ServerVersion is the backend's version of the item (0 when
	// never pushed); Dirty marks a local change not yet pushed; Deleted is a
	// tombstone kept until the delete is pushed.
	ServerVersion int64
	Dirty         bool
	Deleted       bool
}

// Move is one entry in an item's history: a move between lists, or a note
// (From == To).
type Move struct {
	ID        string
	From, To  List
	At        int64 // unix ms
	SessionID string
	Note      string
}

// List is one of the five board lists.
type List string // backlog | review | in_progress | blocked | done
```

### `Board`

| Field | Meaning |
|---|---|
| `Cursor` | The highest backend `version` pulled so far. Sync asks the backend for changes after it. It is 0 on a board that has never synced. |
| `Items` | Every item, including tombstones, in insertion order. Readers sort as they need to; the file has no order of its own. |

### `Item`

| Field | Meaning | Set by |
|---|---|---|
| `ID` | UUIDv4. It is unique across every host that syncs to the same account. The short form `K-3f9a2c` is its first six hex digits, derived and never stored. | the harness on `Add` |
| `Title` | One line, at most 200 runes. | model, user or web |
| `Body` | Multi-line details, at most 4 KiB. | model, user or web |
| `List` | The current list. | model, user or web |
| `Project` | The `origin` repository name, or the repository directory's name. | the harness |
| `ProjectKey` | The repository root's workdir key: its basename plus a hash, the same key session sync uses. | the harness |
| `Dir` | The absolute directory the item was filed from. | the harness |
| `HostID` | The install's sync host id (`sync/host-id`), or empty. | the harness |
| `SessionID` | The session that added the item, or empty when it was added on the web. | the harness |
| `Created`, `Updated` | Unix milliseconds. `Updated` changes on every change and decides sync conflicts (the most recent change wins). | the harness |
| `History` | The last 50 moves and notes, oldest first. | the harness, from moves and notes |
| `Labels` | Routing labels: lower-case `[a-z0-9:_-]`, at most 8 of at most 32 runes, sorted. A worker claims only items carrying all of its profile's labels. | user, web, or a worker's handoff |
| `Priority` | -2 to 3, 0 normal. Claims take the highest first. | user, web, or a handoff |
| `Assignee` | The agent profile the item is routed to; empty means any matching worker. | user, web, or a handoff (from its allowlist) |
| `Parent` | The item this one was handed off from. | the harness |
| `DependsOn` | Items that must be `done` before this one can be claimed. | user, web, or a handoff |
| `Hops` | Handoffs from the root item; a chain stops at 6. | the harness |
| `ClaimedBy` | The worker instance holding the item, or empty. | the harness (`Claim`, `Release`, `Unclaim`) |
| `ClaimHost` | The claiming worker's sync host id. | the harness |
| `ClaimFrom` | The list the item was claimed from, and returns to. | the harness |
| `LeaseUntil` | Unix milliseconds the claim lapses at. Renewals are host-local: they change neither `Updated` nor the history, and are never pushed on their own. | the harness |
| `Attempts` | Claims that ended without success. | the harness |
| `Branch` | The git branch holding the item's work. | the harness |
| `PR` | The draft pull request opened for the branch. | the harness |
| `Finding` | The advisory id (or `<kind>:<rule>:<hash>` for a SARIF result) a security card is about. At most 64 characters of `[A-Za-z0-9._:-]`. | the harness (`UpsertFinding`) |
| `SeenRef` | The full commit id of the latest scan that still showed the finding. | the harness (`UpsertFinding`) |
| `Verdict` | A security worker's recorded verdict: `fixed`, `false_positive`, `no_fix`, `needs_human` or `rejected`. | the harness (`Reconcile`, `SetVerdict`, `Release`) |
| `VEX` | The repository-relative path of the VEX written for the verdict. | the harness (`Release`) |
| `Gates` | The card's acceptance gates, at most 8: each has an id (`G1`, `G2`, by position), a title, a kind (`runnable` or `manual`), for a runnable gate the detected suite it references (plus, for a Go suite, a package directory and a test name), a state (`unmet`, `met` or `abandoned`), the commit the state was decided at, and a one-line note. See [acceptance gates](fleet.md#acceptance-gates). A pulled copy never carries or replaces them. | the harness (`Add`, `SetGate`) |
| `remoteAgent` | Not stored (unexported). Set on a pulled item that carried the `agent` block, so a backend that predates it cannot clear the local routing and claim. | sync |
| `ServerVersion` | The backend's version of the item, or 0 if it has never been pushed. | sync |
| `Dirty` | A local change not yet pushed. | local writes; cleared by sync |
| `Deleted` | A tombstone: the item is gone but the delete has not been pushed yet. | `Delete`; sync |

### `Move`

| Field | Meaning |
|---|---|
| `ID` | A UUID, which is how two hosts' histories are merged without duplicates. |
| `From`, `To` | The lists moved between. `From` is empty for "added to", and `From == To` makes the entry a note. |
| `At` | Unix milliseconds. |
| `SessionID` | Who made the move, or empty for the web. |
| `Note` | Why, at most 1 KiB. |

### What is not in the file

- **The short id.** `K-xxxxxx` is computed by `Item.Short()`.
- **The store's in-memory state.** The loaded copy, the file stamp and the
  listeners live only in memory.
- **Sync status.** The last push and pull times and the error text live only
  in memory.
- **`Provenance`.** It is the input `Add` stamps an item with, not a stored
  type.

## Why this format

The board is read and written by Go, and read by a model only through
tools that render text. The design follows from that:

- **The model never parses the file.** `KanbanSearch` renders items as lines
  such as `K-3f9a2c [review] belai · title…`. Being friendly to a model is a
  job for that rendering, not for the storage format, so the storage format
  can be binary.
- **Go needs no schema code.** gob is in the standard library, adds no
  dependency, and decodes straight into the typed structs above: no
  `json:"…"` tags, no generated code, no migration code for additive changes.
- **gob tolerates growth.** A field added in a newer Belai is ignored by an
  older one. A field removed from the structs is ignored when an old file is
  read. See [Changing the schema](#changing-the-schema).
- **The wrapper covers what gob does not.** gob cannot say "this is not a
  kanban file", "this came from a newer Belai" or "these bytes changed". The
  magic, the version and the SHA-256 do.
- **It is a single file.** There is no daemon and no database. The whole board
  fits in memory, and a write is one atomic rename.

Alternatives considered:

| Format | Why not |
|---|---|
| JSON | Twice the size for the same data, and no schema check. It invites hand-editing a file whose integrity Belai relies on. |
| Protocol Buffers or MessagePack | A dependency and generated code for no gain; nothing but Go reads this file. |
| SQLite | A cgo or large pure-Go dependency, plus journal files beside the board. It would be worth it only for boards far beyond the current limits. |

The website and the backend never see this format. Sync sends items as JSON,
using `sessionsync.KanbanItem`; see [Sync mapping](#sync-mapping).

## Reading

`Store.refreshLocked` runs before every read and every write:

1. **Check the file.** `os.Stat` the file.
   - Missing: the board is empty. This is not an error.
   - Modification time and size the same as the last load: use the copy in
     memory.
2. **Otherwise reload.** Read the whole file and `Decode` it:
   1. The length must be at least 38 bytes.
   2. The magic must be `BKAN`.
   3. The version must be 1, 2 or 3 (`minVersion` to `formatVersion`).
   4. The SHA-256 of the payload must equal the last 32 bytes.
   5. The payload must gob-decode into a `Board`.
3. **On success,** replace the copy in memory and remember the file's
   modification time and size.
4. **On failure,** keep the error. Every read and write returns it until the
   file changes on disk.

Checking the modification time and size is what lets several Belai processes
share one board: a write by another process is picked up on the next read.

## Writing

Every change goes through `Store.mutate`, the only write path:

1. **Create the directory** if needed (`MkdirAll`, mode 0700).
2. **Take the cross-process lock.** This is `config.AcquireFileLock` on
   `kanban.lock` in the same directory. The lock file is created with
   `O_CREATE|O_EXCL` and holds the writer's pid.
   - A waiter polls every 200 ms and gives up after 10 s.
   - A lock file older than 30 s is treated as left behind by a crash, and is
     removed and taken.
   - An in-process mutex is taken first, so goroutines in one process queue
     without touching the file system.
3. **Reload** through the read path, so the change applies to the newest board
   on disk and not a stale copy. An unreadable board aborts the write here.
4. **Apply the change** to a deep copy of the board.
5. **Prune tombstones.** A deleted item is dropped once it has been pushed
   (not `Dirty`), or once it has waited 30 days for a push that never came. A
   board that never syncs still sheds its deletes.
6. **Encode** the board: header, gob, checksum.
7. **Write atomically:**
   1. Create a temp file `.kanban-*` in the same directory, so the rename
      stays within one file system.
   2. `chmod` it to 0600 before any content is written.
   3. Write the bytes, `fsync`, and close.
   4. `rename` it over `kanban`.
8. **Remember** the new file's modification time and size, and release the
   lock.

A crash at any step leaves either the old file or the new one, never a mix.
The directory itself is not `fsync`ed after the rename. After a power loss on
some file systems, the rename can therefore be lost, which means the last
change is lost, but the file is never corrupted.

Local changes then notify the `OnChange` listeners, which nudge the sync
worker. This happens after the lock is released, on the writer's goroutine.

## Failing closed

A board Belai cannot read is never written over:

- **Tools.** Model tool calls return the error.
- **TUI.** `/kanban` shows it, and the pane stays hidden.
- **Sync.** Sync stops pushing and pulling for that board.
- **Headless runs.** A headless run reports it on the tool result.

Rewriting an unreadable board as empty would silently destroy every item on
it. An unsupported version is treated the same way: an older Belai must not
downgrade a board a newer Belai wrote.

To recover, move the file aside, for example
`mv ~/.vulnetix/belai/kanban ~/kanban.broken`. Belai starts a fresh board on
its next write. If sync is on, the next pull brings back everything the
website holds.

## Changing the schema

Under gob's rules, changes to `Board`, `Item` or `Move` fall into four cases:

| Change | Effect on existing files | Version bump? |
|---|---|---|
| Add an exported field | Old files decode, and the field is its zero value. An older Belai ignores the field. | No |
| Remove a field | Old files decode, and the stored values are ignored. | No |
| Rename a field | **The data is silently lost.** gob matches fields by name, so the old name's values are dropped. | Yes, with a migration |
| Change a field's type | The decode fails, so the board reads as corrupt and is refused. | Yes, with a migration |

Version 2 added the routing and claim fields. Adding fields needs no bump
by itself, but a Belai that predates them would decode a newer board,
silently drop the fields and write it back without them, releasing every
claim. Raising the version makes that older Belai refuse the board instead.
Version 1 files still decode, with every new field at its zero value:
unrouted and unclaimed.

Version 3 added the acceptance gates for the same reason: a Belai that
predates `Gates` would decode a version 3 board, drop every gate and write it
back, so an older Belai must refuse it. Version 2 files still decode, with no
gates.

The zero-value rule shapes new fields. A new field's zero value must mean
"not set" or "the old behaviour", because a file written before the field
existed will decode it as zero. For example, `ServerVersion == 0` means
"never pushed".

A change that gob cannot express needs a new format version. That includes a
rename, a type change, or a change of meaning for an existing field. The steps:

1. Raise `formatVersion`.
2. Teach `Decode` to accept the previous version and convert it. Today it
   accepts versions 1, 2 and 3 and rejects every other.
3. Keep writing only the new version.
4. Add a test that decodes a file written in the previous version.

## Inspecting a board

There is no dump command. To confirm that a file is a board and see which
version wrote it:

```sh
xxd -l 6 ~/.vulnetix/belai/kanban    # 42 4b 41 4e 00 01  BKAN..
```

To read it from Go inside this module, for example in a throwaway test:

```go
data, _ := os.ReadFile(path)
board, err := kanban.Decode(data) // checks magic, version and checksum
```

Otherwise, use `/kanban` in the TUI, or `KanbanSearch` with
`{"project":"all","lists":["backlog","review","in_progress","blocked","done"]}`.

## Security notes

- **The checksum is for integrity, not authenticity.** It catches damage, not
  forgery: anyone who can write the file can write a valid one. The protection
  against that is where the file lives:
  - It is mode 0600, and its directory is 0700.
  - It is in Belai's state directory, which the OS sandbox hides from every
    command Belai runs.
- **Contents are untrusted whatever the checksum says.** Items were written by
  models and by web users, so everything the tools read back goes through the
  security classifier. Text is also cleaned before it is stored: delimiter
  markup, ANSI sequences, control characters and bidi characters are
  stripped, and fields are capped. That applies to local writes and to pulled
  items alike.
- **The file holds no secrets.** No credentials and no tokens, only item text
  and provenance: paths, project names and session and host ids.

## Sync mapping

Sync sends items as JSON (`sessionsync.KanbanItem`), converted by
`kanban.ToWire` and `kanban.FromWire`:

| Go field (`Item`) | JSON field | Notes |
|---|---|---|
| `ID` | `id` | |
| `Title` | `title` | |
| `Body` | `body` | |
| `List` | `list` | |
| `Project` | `project` | |
| `ProjectKey` | `projectKey` | |
| `Dir` | `cwd` | |
| `HostID` | `hostId` | |
| `SessionID` | `sessionId` | |
| `Created` | `createdAt` | |
| `Updated` | `updatedAt` | |
| `History` | `history` | Each `Move` becomes `{id, from, to, at, sessionId, note}`. |
| `ServerVersion` | `version` | |
| `Deleted` | `deleted` | |
| `Labels`, `Priority`, `Assignee`, `Parent`, `DependsOn`, `Hops` | `agent.labels`, `agent.priority`, `agent.assignee`, `agent.parent`, `agent.dependsOn`, `agent.hops` | Always sent. |
| `PinHost` | `agent.pinHost` | A sync host id; the website may set or clear it. Only a worker on that host claims the item. |
| `ClaimedBy`, `ClaimHost`, `ClaimFrom`, `LeaseUntil`, `Attempts`, `Branch`, `PR` | `agent.claimedBy`, `agent.claimHost`, `agent.claimFrom`, `agent.leaseUntil`, `agent.attempts`, `agent.branch`, `agent.pr` | The website may clear a claim, never set one. |
| `Finding`, `SeenRef`, `Verdict`, `VEX` | — | Local only; never sent, and a pulled copy keeps the local values. |
| `Dirty` | — | Local only; never sent. |

The routing and claim fields travel in one optional `agent` object
(`sessionsync.KanbanAgent`). A pull without it keeps the host's own values.
When a pulled copy of the same claim wins last-writer-wins, the later of the
two leases stands, because renewals are never pushed.

`Board.Cursor` is local only as well. It is the `since` parameter of
`GET /v1/belai/kanban/items`.
