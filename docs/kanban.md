# Kanban board

Belai keeps one global kanban board, shared by every session on the machine
and mirrored to the Vulnetix website while you are logged in with the
Vulnetix CLI. Work a session leaves open lands on it instead of scrolling away
in a report. A later session, or you, picks it up from there.

The board has five lists:

| List | Meaning |
|---|---|
| `backlog` | work nobody has started |
| `review` | work a session noted as open, or a scout's survey found; confirm it before doing it |
| `in_progress` | work a session is doing now |
| `blocked` | work that cannot continue; the last note says why |
| `done` | finished and verified, or confirmed obsolete |

A scout that works a seeded `quality` card, or [surveys on its own](fleet.md#finding-work-kanbansurvey),
files what it finds into `review` with a `build` label. Moving such an item to
`backlog` confirms it, and a builder takes it from there. The harness files the
`quality` cards from a [test run on HEAD](fleet.md#the-delivery-crew).

Each item records the session that added it, the project (the `origin`
remote's repository name, or the repository directory's name), the directory
it was filed from, and a history of moves and notes.

## Where the model gets the tools

Belai offers the model different kanban tools at different points in a turn:

| When | Tools |
|---|---|
| Every call of a main session, plan mode included | `KanbanSearch`, `KanbanUpdate` |
| While an agent, goal or plan-execute loop works | plus `KanbanMove` (to `in_progress`, `blocked` or `done`) |
| After the report of a work turn (the wrap-up) | only `KanbanSearch`, `KanbanUpdate`, `KanbanAdd` (to `review`) and `KanbanMove` (to `done`) |
| Explore, Task and fan-out subagents | `KanbanSearch` only |
| A [fleet worker](fleet.md) working its claimed item | `KanbanSearch`, `KanbanUpdate` (notes on its own item and its handoffs only) and `KanbanHandoff` (plus `KanbanGate` for a reviewer); no `KanbanMove`, no wrap-up |
| A [fleet worker](fleet.md) working its claimed item | `KanbanSearch`, `KanbanUpdate` (notes on its own item and handoffs only) and `KanbanHandoff` (plus `KanbanGate` for a reviewer); no `KanbanMove`, no wrap-up |

- **`KanbanSearch`** lists items by text, lists and project (`current` by
  default, `all`, or a name). A single match also shows its body and history.
- **`KanbanUpdate`** edits an item's title or body, or appends a note.
- **`KanbanMove`** moves an item between lists with a note saying why.
- **`KanbanAdd`** files one piece of open work into `review`. If the same
  title is already open in the project, it returns that item's id instead.
- **`KanbanHandoff`** (fleet workers only) files a follow-on item for
  another agent, linked to the claimed item and routed by the labels and
  profiles the worker's profile allows. A worker may file at most five per
  item, and a chain of handoffs stops after six hops.
  A worker whose profile has a `kanban.gates` block also gives each handoff
  acceptance gates, references to test suites the harness detected; see
  [acceptance gates](fleet.md#acceptance-gates).
- **`KanbanGate`** (a fleet worker whose profile sets `kanban.gates.review`, the
  reviewer) records `met`, `unmet` or `abandoned` with one line of evidence on a
  manual gate of the item it holds. It takes no item id, writes only the claimed
  item, and refuses a runnable gate: the harness decides those. See
  [manual gates](fleet.md#manual-gates-and-the-reviewer).
- **`KanbanContract`** (a fleet worker that plans a request, the scout) records the
  clauses of the request card it holds, before its first handoff. Each handoff then
  names the clauses it covers, and the harness files a gap card for one nothing
  covers. See [request coverage](fleet.md#request-coverage).

In every session, `KanbanMove` and `KanbanUpdate` refuse an item another
worker has claimed. `KanbanMove` is also a compare-and-set: it refuses the
move if the item changed list since the model read it.
- **`KanbanHandoff`** (fleet workers only) files a follow-on item for
  another agent, linked to the claimed item, routed by the labels and
  profiles the worker's profile allows (at most five per item, and a chain
  of handoffs stops after six hops).

In every session, `KanbanMove` and `KanbanUpdate` refuse an item another
worker has claimed. `KanbanMove` is also a compare-and-set: it refuses a move
if the item changed list since the model read it.

While the loop runs, the turn's instructions include the board's counts for
the project and the ids of its in_progress and blocked items, plus any
`K-xxxxxx` id named in the prompt. The model keeps the items it touches
current as it works: in_progress when it starts one, blocked with the blocker
as the note, done once it is verified.

### The wrap-up

A work turn gets one short wrap-up pass (at most four tool rounds) after its
report. A work turn is one of: goal mode, running an approved plan, a
`/vulnetix review`, or an agent turn that ran a tool. Plain question-and-answer
turns, plan mode, subagents, cancelled turns and failed turns skip it.

The wrap-up asks the model to do two things:

1. File every distinct piece of open work into `review` with `KanbanAdd`,
   searching first so nothing is added twice.
2. Move to `done` every item in backlog, review, in_progress or blocked that
   the turn completed or showed was already resolved.

The wrap-up's reply is discarded, so the report stays the turn's answer. The
transcript shows one line such as `kanban: 2 added to review · 1 moved to done`.

"Open work" covers these categories, each listed with its signals in the
wrap-up directive:

- **unfinished**: not done, partial, remaining, next steps, stubs,
  placeholders, `not implemented`, skipped or disabled tests.
- **markers**: TODO, FIXME, XXX, HACK or BUG markers added or found.
- **deferred**: out of scope, follow-up, later, phase 2, nice-to-have,
  recommendations and suggestions.
- **unverified**: tests not run or failing, build not run, lint warnings,
  manual QA needed, CI pending or red, coverage gaps.
- **blocked**:
  - needs a decision, credentials or access
  - waiting on upstream, review or a release
  - permission denied or content withheld
  - the goal stalled or the turn budget ran out
- **found-not-fixed**: bugs, pre-existing failures, vulnerabilities (including
  unremediated scanner and dependency findings), deprecations, performance and
  concurrency risks, edge cases, known limitations.
- **temporary**: workarounds, hard-coded values, debug code, flags to clean
  up, shims to delete.
- **tech-debt**: duplication, refactors, dead code, the same change needed
  elsewhere.
- **docs**: README, CHANGELOG, comments or examples to update; release notes.
- **operational**: deploys, migrations, backfills, monitoring, secret
  rotation, environment and infrastructure changes.
- **question**: assumptions to confirm, unanswered questions, guesses, parts
  of the request not addressed.

## In the TUI

### The pane

When nothing is running and the composer is empty, a pane above the composer
lists this project's backlog, review and blocked items:

- Each row is colour-coded: backlog grey, review amber, blocked red.
- Long titles are shortened with an ellipsis.
- When the project has no open items, the pane lists every project's.
- A `⤴` marks an item filed from a directory outside the session's roots.

Keys:

| Key | Action |
|---|---|
| `↓` on an empty composer | focus the pane (`↑` stays prompt history) |
| `↑` `↓` | select an item |
| `tab` | cycle all, backlog, review, blocked |
| `p` | toggle this project or all projects |
| typing | filter titles |
| `enter` | put the item's prompt in the composer |
| `f9` | open the runs panel's kanban tab on the same filter and item |
| `esc` | clear the filter, then leave the pane |

The prompt depends on the item's list:

- **backlog**: move the item to in_progress, implement it, verify it, and move
  it to done.
- **review**: first confirm the work is still outstanding. Close the item if it
  is resolved; otherwise do the work.
- **blocked**: re-check the blocker. Continue if it has cleared; otherwise note
  what still blocks it.

The prompt is only text in the composer. You can edit it before sending, and it
is admitted like anything you type.

### `/kanban`

`/kanban` (also `f1` then `t`) opens the full board. `/kanban K-xxxxxx`
opens it on that item, across every project; the website's card view and
terminal commands use this form.

| Key | Action |
|---|---|
| `←` `→` | switch list |
| `↑` `↓` | select an item |
| `enter` | work on the item: prefill the composer |
| `n` | add an item to the current list |
| `e` | edit the title |
| `b` | edit the details |
| `o` | add a note |
| `m` then `1`–`5` | move the item |
| `d` then `y` | delete the item |
| `/` | filter |
| `p` | toggle this project or all projects |
| `r` | sync now |

### The runs panel

The **kanban** tab of the runs panel (`f9`, then `tab`) manages the board
without leaving the chat. It draws the items exactly as the composer pane
does, with each item's labels, priority, assignee and claim beside it, under
the filters open, backlog, review, in progress, blocked and done. Its
actions call the same store methods as `/kanban`:

| Key | Action |
|---|---|
| `enter` | put the item's prompt in the composer |
| `1`–`5` | move to backlog, review, in progress, blocked or done |
| `n` | add an item to the filtered list (backlog under open) |
| `o` / `a` / `L` | add a note, assign a profile, set labels |
| `+` / `-` | raise or lower the priority |
| `u` | release a worker's claim |
| `w` | hand the item to the crew chosen on the crew tab |
| `[` / `]` | previous or next filter |
| `p` | toggle this project or all projects |
| `/` | filter by text |
| `K` | open the item on `/kanban` |

Text the actions ask for (a title, a note, an assignee, labels) is typed in
the composer, which says what it is asking for. `esc` cancels. Deleting stays
on `/kanban`, behind its confirmation.

`w` adds the chosen crew's entry labels (those of its first member that
claims from backlog, `#scout` for `belai:delivery`), moves the item to
backlog, and starts the crew when no worker is running. A claimed item is
left alone until its claim is released. The **crew** tab is described in
[Fleet](fleet.md#in-the-tui).

## Routing and claims

An item can carry routing that decides which [fleet worker](fleet.md) takes
it:

- **labels** (lower-case `[a-z0-9:_-]`, up to eight): a worker claims only
  items carrying all of its profile's labels;
- **priority**, -2 to 3: claims take the highest first, then the oldest;
- **assignee**: who takes it. `NAME` or `worker:NAME` is a worker profile, `crew:NAME` is any worker started in that crew, and `person:HANDLE` is someone on the website, which no worker ever claims. Only the named worker or crew may claim it;
- **depends on**: items that must be `done` first.

A claim moves the item to `in_progress` under a lease the worker renews while
it works. The harness makes the claim under the board's lock, so two workers
never hold the same item, and a lease that lapses (a crashed worker) returns
the item to the list it came from. The claim, the branch holding the work and
any draft pull request show on the item.

A security card also carries four fields only the harness sets, never a model
argument: the **finding** id, the **seen ref** (the commit whose scan last
showed it), the recorded **verdict** and the **VEX** path. They sync with the
card so the website can link it to its vulnerability and a second host finds the
card instead of filing another. Only a host sets them: the website ignores them
on an edit, and a pulled copy fills a field this host holds none for, after the
value is checked against the shape the harness gives it, and never replaces or
clears one. The `vuln`, `gone` and
`needs-verify` labels route a card through the
[security crew](fleet.md#the-security-crew).

From the command line:

```sh
belai kanban add "Add a -json flag to agent ps" -label build -priority 2
belai kanban list -label build
belai kanban show K-3f9a2c
belai kanban move K-3f9a2c done -note "shipped"
belai kanban release K-3f9a2c        # clear a claim
belai kanban import items.jsonl      # one {"title", "labels", "gates", …} per line
```

In `/kanban`, `a` assigns, `L` edits labels, `+` and `-` change priority, and
`u` releases a claim. Rows show `#labels`, `▲priority`, `@assignee`, `on HOST` for a pinned host and
`⚙ worker lease` (`⚠` when the lease has lapsed).

## Storage

The board is the file `~/.vulnetix/belai/kanban` (under `$BELAI_HOME` when
set), mode 0600. It has four parts:

1. the magic `BKAN`
2. a two-byte format version
3. a Go `encoding/gob` payload
4. the payload's SHA-256

Each write is atomic (a temp file, then rename) and serialised by
`kanban.lock`, so several Belai processes share the board safely. A board that
fails its magic, version or checksum is refused, and it is never overwritten.
Move the file aside to start a fresh one. [bkan.md](bkan.md) documents the
file format byte by byte, with the Go types it holds.

Limits:

- a title is at most 200 characters
- details are at most 4 KiB
- a note is at most 1 KiB
- an item keeps its last 50 history entries
- a card carries at most 8 acceptance gates, each title at most 120 characters
- the board holds at most 5000 items

## Sync with the Vulnetix website

The board syncs whenever session sync can run: the `sync.enabled` setting is
on and the Vulnetix CLI is logged in (see [session-sync.md](session-sync.md)).
It uses the same client, so it only goes to `https://*.vulnetix.com` with the
CLI's credential.

- Every change is written to the local file first. The sync worker then pushes
  it in the background, so no tool call waits on the network.
- The TUI pulls the website's changes every 15 seconds, when `/kanban` opens,
  and on `r`.
- A headless `-prompt` run, or an ACP connection, pushes once as it exits.
- Conflicts resolve per item: the most recent change wins. A newer local change
  that has not been pushed yet survives a pull. Histories are merged.

The website's **Belai → Kanban** page shows the same board with the same
colours. You can move, edit, add and delete items there.

## Setting

```json
{ "kanban": false }
```

`kanban` turns the whole feature on or off: the tools, the directive, the
wrap-up, the pane, `/kanban` and sync. It is on by default. A project's
`.vulnetix/settings.json` may turn it off, but never on.

## Security

- Item text is written by other sessions' models and by web users, so
  `KanbanSearch` results are `KindKanban` and always go through the security
  classifier. The instructions the harness writes into a turn carry counts and
  ids, never item text.
- The tools take no path. Provenance (session, host, project, directory) is
  stamped by the harness and never taken from the model's arguments.
- Subagents may search the board but never write to it. Repository text they
  read therefore cannot persist into the global board.
- Everything written to the board, locally or pulled from the website, has its
  delimiter markup, ANSI sequences, control characters and bidi characters
  removed and is capped.
