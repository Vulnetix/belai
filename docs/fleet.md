# Agent fleet

A fleet is a set of Belai agents that work from the [kanban board](kanban.md)
without anyone watching. Each worker is its own `belai` process. It claims an
item off the board, works on it in a git worktree of its own, and moves the
item on when it is done — to another worker, to a human, or to `done`. Work is
handed between agents on the board and nowhere else, so every step is visible
in `/kanban`, on the Vulnetix website, and in `belai kanban list`.

```sh
belai kanban add "Add a --json flag to belai agent ps" --label build
belai agent start --crew belai:delivery
belai agent ps
belai kanban list
belai agent stop --all
```

## Workers

A worker runs a [profile](agent-profiles.md) whose `mode` is `worker`. The
profile's `kanban` block says which items it takes and where they go next:

```json
{
  "name": "builder",
  "description": "Implements backlog items labelled build",
  "system_prompt": "You implement one kanban item per turn …",
  "mode": "worker",
  "autonomy": "supervised",
  "kanban": {
    "lists": ["backlog"],
    "labels": ["build"],
    "on_success": {"list": "review", "labels": ["needs-review"]},
    "on_failure": {"list": "blocked"},
    "handoff_to": ["reviewer"],
    "max_attempts": 2,
    "lease": "20m",
    "poll": "30s"
  },
  "workspace": {"isolation": "worktree"},
  "budget": {"max_passes_per_item": 8, "max_wall_per_item": "45m"}
}
```

One pass of the worker loop:

1. **Claim.** The harness, not the model, claims the best matching item: highest
   priority first, then oldest. The claim is one locked read-modify-write of
   the board file, so two workers never hold the same item. An item is
   claimable only from `backlog` or `review`, only when every item it
   `depends_on` is `done`, and only when its `assignee` is empty or names this
   profile. The item moves to `in_progress` with a lease.
2. **Workspace.** A `worktree` worker gets a fresh git worktree on the branch
   `belai/K-xxxxxx/a<attempt>`, outside the repository
   (`~/.vulnetix/worktrees/<project>/…`, or `$BELAI_WORKTREES_DIR`). A worker
   that reviews an existing branch checks that branch out instead. When that
   branch is one of the item's own `a<n>` branches and it was deleted, the item
   starts fresh on the next free attempt branch; any other missing branch is
   refused. When the workspace cannot be prepared, the worker runs a
   **setup-debug turn** before it releases the item. The turn runs in the
   repository itself on the profile's tools, read-only: mutating tools are
   dropped and `Bash` becomes the read-only `Bash`. The prompt is a fixed
   harness sentence. The failure rides as an attachment classified as process
   output (`KindProcess`), and a withheld failure is not investigated. The
   model records what it finds on the item with `KanbanUpdate`. The release
   note adds only harness facts: the stop reason and the pass count. The turn
   uses the item's lease and budgets, and it is not a separate attempt.
3. **Work.** The item is worked as a goal-mode turn. The prompt is a fixed
   harness sentence; the item's title, body and recent notes ride as a
   `kanban` attachment that went through the security classifier, exactly
   like a `KanbanSearch` result. A withheld item is moved to `blocked` and is
   never worked.
4. **Lease.** While the turn runs, the harness renews the lease. A worker
   that crashes stops renewing; when the lease expires the item returns to the
   list it was claimed from and another worker may take it.
5. **Outcome.** The goal verdict decides. On `GOAL_COMPLETE` the harness
   commits the worktree's changes to the branch and releases the item to
   `on_success`. On a stall, a budget stop or an error the item goes back to
   its source list until `max_attempts`, then to `on_failure`. The release
   note is harness-composed: counts, the verdict and the stop reason, never
   model text. The worker that failed an item leaves it to other workers
   until someone touches it. Moving it back, assigning it or editing it
   counts as a deliberate retry, and that worker may take it again.
6. **Memory.** An optional reflection turn distils a few lessons into the
   profile's memory file, which later items receive as a classified
   attachment.

### What a worker's model may do on the board

| Tool | In a worker |
|---|---|
| `KanbanSearch` | yes |
| `KanbanUpdate` | a note on the claimed item only |
| `KanbanHandoff` | files a new item with this item as its parent, to a profile in `handoff_to` |
| `KanbanMove`, `KanbanAdd` | not offered; the harness moves the claimed item |

There is no claim tool. In every session, a model's `KanbanMove` or
`KanbanUpdate` is refused on an item another worker holds.

### Asks

A worker cannot ask anyone. A `supervised` worker's tool call that needs a
permission ask is withheld, and the item moves to `blocked` with the note
`needs permission: <tool>`. Unblock it by adding a permission rule, or do
that step yourself, and move the item back. An `autonomous` worker resolves
asks to allow; it must use a worktree, and a worker that has `Bash` must have
a working [OS sandbox](sandbox.md) backend, or it refuses to start.

Any worker also refuses to start when its security classifier cannot run in
this build. For example, `classifier.kind: "models"` in a binary built without
embedded models fails every turn. Refusing at startup means the error is not
charged to each item it claims.

### Transcripts

Each item's turn is written as a session under the repository's project, so
the session id on the item's notes lists, resumes and searches like any
other. With `sync.enabled` on and a Vulnetix CLI credential, the worker
mirrors that session to the website the same way the TUI does. The mirror
uploads only the lines the transcript wrote, and it takes no prompts or
answers back.

## Assigning and pinning

`belai kanban assign` routes an item the way a worker will look for it:

```sh
belai kanban assign K-7f3a21 belai:builder            # any host
belai kanban assign K-7f3a21 belai:builder -host this # only this machine
belai kanban assign K-2b9e40 -crew belai:delivery -start
```

For a profile it sets the assignee, adds the profile's claim labels and
moves the item to the first list the profile claims from. For a crew it
routes to the crew's first member by labels only, with no assignee, so
every member can claim what the crew hands on. `-start` then runs
`belai agent start` in this directory.

`-host` pins the item (`this`, a sync host id, or `none` to unpin). A pinned
item is claimable only by a worker whose host id matches, including through
`agent run -item`. Handoffs filed from it are not pinned. The pin syncs as
`agent.pinHost`, and the website's Agent Coordination page sets it when you
assign a card to a host there; with [remote control](remote-control.md#fleet-workers)
running, the page can also start the worker on that host.

## Crews

A crew is a named set of profiles started together:

| Crew | Members |
|---|---|
| `belai:delivery` | `belai:scout` ×1, `belai:builder` ×2, `belai:reviewer` ×1 |
| `belai:security` | `belai:vuln-scout` ×1, `belai:patcher` ×2, `belai:verifier` ×1 |

Delivery: file an item labelled `scout` ("survey internal/foo for missing
tests"). The scout reads and hands off one `build` item per concrete task.
Builders implement each on its own branch and hand it on as `needs-review`.
The reviewer checks the branch out, runs the tests, and moves the item to
`done`, or back to `backlog` with its notes.

Security: file an item labelled `vuln-scan`. The vuln scout runs the
[Vulnetix](vulnetix.md) scans and hands off one `vuln` item per finding.
Patchers remediate on a branch; the verifier rescans the branch and closes the
item or sends it back.

Your own crews live in `~/.vulnetix/belai/profiles/crews/<name>.json`:

```json
{"name": "docs", "description": "Docs writers", "members": [{"profile": "writer", "replicas": 2}]}
```

## Command line

| Command | Effect |
|---|---|
| `belai agent list` | profiles, with their mode |
| `belai agent show NAME` | one profile as JSON |
| `belai agent validate FILE` | validate a `.json` or `.md` profile |
| `belai agent import [-force] FILE` | validate and save a profile |
| `belai agent draft [-json] [-o FILE] PREMISE` | draft a profile from a premise (every offer taken) as markdown for `import`; `-json` prints each offer with its reason |
| `belai agent crews` | crews and their members |
| `belai agent memory NAME [-clear]` | a worker's lessons |
| `belai agent status` | running workers and this project's board |
| `belai agent run NAME [-once] [-item K-…]` | run a worker in the foreground |
| `belai agent start NAME \| -crew CREW` | start detached workers |
| `belai agent ps` | running and recently stopped workers |
| `belai agent logs ID [-f]` | a worker's log |
| `belai agent stop ID \| NAME \| -all` | stop workers; claims are released |
| `belai kanban add TITLE [-label L] [-priority N] [-assignee NAME] [-depends K-…]` | file an item |
| `belai kanban list [-list L] [-label L] [-project all] [-json]` | list items |
| `belai kanban show ID` | one item with its history |
| `belai kanban move ID LIST [-note TEXT]` | move an item |
| `belai kanban note ID TEXT` | add a note |
| `belai kanban release ID` | clear a claim |
| `belai kanban assign ID PROFILE \| -crew CREW [-host this\|ID\|none] [-start]` | route an item to a profile or crew, optionally pinned |
| `belai kanban import FILE.jsonl` | file many items |

`agent run` and `agent start` refuse a directory you have not trusted; pass
`-trust-dir` to trust it, as with `-prompt`. `-provider` and `-model`
override the profile's model for that run.

## In the TUI

`/fleet` opens the fleet tab of `/agents` (also `4` there):
workers with their state, the item each holds, items done and failed, and a
heartbeat; `l` shows the selected worker's log, `x` stops it and `X` stops
them all. `/fleet start NAME`, `/fleet crew NAME` and `/fleet stop ID|all`
do the same as the CLI, and `s` on a worker profile in the profiles tab
starts one.

The runs panel (`f9`, then `tab`) has a **crew** tab beside its
[kanban tab](kanban.md#the-runs-panel), so the board and its workers are
managed from the chat. Its label counts the running workers (`crew 3`), and
its summary names the chosen crew and its members, the running workers
against `agents.max_workers`, and the board's counts. The list refreshes
every two seconds while the tab is open.

| Key | Action |
|---|---|
| `c` | choose the next crew |
| `s` | start the chosen crew, with the same preflight and `max_workers` check as `/fleet crew` |
| `x` / `X` | stop the selected worker, or every worker |
| `l` | show the selected worker's log tail |
| `enter` | show the worker's item on the kanban tab |
| `v` | open the fleet tab of `/agents` |
| `r` | refresh |

On the kanban tab, `w` hands an item to the chosen crew: it adds the crew's
entry labels, moves the item to backlog, and starts the crew when no worker
is running.

## Git in the worktree

A worker's working directory is a git worktree on its item's branch. Inside
the OS sandbox the agent can use git normally there: `status`, `diff`, `log`,
`show`, `add` and `commit`. The sandbox layers the repository's `.git`:

- git may create files at the top of `.git` (it takes `packed-refs.lock` on
  every commit), but every entry already there is read-only: `config`,
  `HEAD`, the main checkout's `index`, `hooks`, `info`, `refs`, `logs`,
  `objects` and the other worktrees. A read-only entry cannot be written or
  renamed over. After each turn the harness removes anything the agent
  created there, such as a `MERGE_HEAD` or `shallow` that would change your
  main checkout.
- The agent's git writes new objects to a private store in its worktree's
  admin directory, reading the shared store as a read-only alternate. After
  the turn, and before any push, the harness copies the new objects in,
  never overwriting one. An agent cannot delete or rewrite your objects.
- Writable are only the item's own branch directory
  (`refs/heads/belai/K-xxxxxx/` and its reflogs), the worktree's admin
  directory (its `HEAD` and index), and the worktree itself. No agent can
  touch `main`, another branch, or another item's branch.

Each turn carries a workspace note (harness facts only) with:

- the branch and the base commit, so `git diff <base>..HEAD` shows the
  item's work;
- that it must stay on its branch;
- whether it may publish.

Whatever the agent leaves uncommitted, the harness commits when the goal
ends. It first checks that the worktree is still on the item's branch.

## Publishing

`workspace.publish` decides whether a worker pushes:

| Value | Effect |
|---|---|
| `none` (default) | Nothing is pushed; the branch moves on through the board. |
| `agent` | The agent gets the `PublishBranch` tool. It pushes exactly the item's branch to `origin` and opens a draft pull request, or returns the one already open. It can be called again after more commits. |
| `draft_pr` | When the item reaches `done`, the harness does the same itself. |

`git push`, `gh pr create`/`merge`/`ready`, `glab mr create`/`merge`,
`git switch`, `git checkout -b`, `git worktree`, `git config` and
`git remote` are denied in a worker's Bash wherever they appear in a command
line. `PublishBranch` is the only way to push, because a raw push could send
any ref, `main` included. Publishing needs a GitHub or GitLab `origin` and
committed work on the branch. The link is recorded on the item.

The built-in builders and patchers use `agent`; the reviewer and verifier use
`draft_pr`, which finds the builder's pull request rather than opening a
second one. `agents.publish: false` turns publishing off everywhere: the
workers still run, and nothing is pushed.

## Memory

A profile with `memory.enabled` keeps a short lessons file
(`~/.vulnetix/belai/agents/memory/<profile>.md`). After each item a
tool-less turn distils at most three lessons from the report. The file is
capped, oldest lines first. It is classified when written and again when the
next item reads it, and `SearchMemory` can find it.

## Registry

Each worker writes `~/.vulnetix/belai/agents/run/<id>.json`: its profile,
pid, directory, state (`starting`, `idle`, `working`, `stopping`, `stopped`,
`failed`), the item it holds, heartbeat time and counts. `belai agent ps`, the
TUI's `/agents` screen and the other workers read it. A record whose process
has died is marked `failed` and its claim is released. Logs are
`~/.vulnetix/belai/agents/logs/<id>.log`.

## Settings

```json
{ "agents": { "enabled": true, "max_workers": 4, "publish": true } }
```

- `enabled` turns workers on or off. A project may turn it off, never on.
- `max_workers` caps running workers on this machine. A project may lower
  it, never raise it.
- `publish` allows `workspace.publish: draft_pr`. A project may turn it off,
  never on.

## Security

- **The harness claims.** No model can claim, and provenance is stamped by
  the harness.
- **Item text is untrusted.** It is classified before a worker sees it and
  never enters the system block or a directive. Handoffs are allowlisted by
  `handoff_to`, capped per item, and carry a hop count that ends a ping-pong
  in `blocked`.
- **Settings come from the trusted repository.** A worker resolves settings,
  posture, credentials and trust from the repository root, never from its
  worktree, so one agent cannot plant settings for the next.
- **Git runs hardened.** Every git call a worker makes has hooks and
  fsmonitor off, no file transport, no credential prompts, the scrubbed
  environment, and an explicit `--git-dir` the harness computed. The
  agent's own git may write only its worktree, the object store and the
  `belai/` branches; config, hooks and every other ref stay read-only.

## Compared with other agent harnesses

| | Hermes Agent | OpenClaw | Belai fleet |
|---|---|---|---|
| Claims | atomic, TTL, stale reclaim | claim tokens | atomic under the board lock, renewed lease, reclaim on expiry or dead pid |
| Routing | assignee profile | bindings | labels, assignee, `handoff_to` allowlist |
| Workspaces | scratch, dir, worktree | per-agent workspace | worktree per item, outside the repo |
| Untrusted text | heuristic injection scan | random-boundary tags | security classifier on every item, memory and handoff |
| Isolation | "the OS is the only boundary" | optional sandbox | OS sandbox per command, required for Bash workers |
| Settings | per-profile home | per-agent config | global only; a project may only tighten |
