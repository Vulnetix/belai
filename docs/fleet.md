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
   `belai/K-xxxxxx-a<attempt>`, outside the repository
   (`~/.vulnetix/worktrees/<project>/…`, or `$BELAI_WORKTREES_DIR`). A worker
   that reviews an existing branch checks that branch out instead.
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
   model text.
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
| `belai kanban import FILE.jsonl` | file many items |

`agent run` and `agent start` refuse a directory you have not trusted; pass
`-trust-dir` to trust it, as with `-prompt`. `-provider` and `-model`
override the profile's model for that run.

In the TUI, `/fleet` opens the fleet tab of `/agents` (also `4` there):
workers with their state, the item each holds, items done and failed, and a
heartbeat; `l` shows the selected worker's log, `x` stops it and `X` stops
them all. `/fleet start NAME`, `/fleet crew NAME` and `/fleet stop ID|all`
do the same as the CLI, and `s` on a worker profile in the profiles tab
starts one.

## Publishing

A profile with `workspace.publish: draft_pr` pushes the item's branch and
opens a draft pull request (GitHub with `gh`, GitLab with `glab`) when the
item reaches `done`, and records the link on the item. The built-in reviewer
and verifier publish; the builders and patchers do not, so nothing is pushed
before review. `agents.publish: false` turns publishing off everywhere.

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
  repository's `.git` stays read-only inside the sandbox; the harness
  commits.

## Compared with other agent harnesses

| | Hermes Agent | OpenClaw | Belai fleet |
|---|---|---|---|
| Claims | atomic, TTL, stale reclaim | claim tokens | atomic under the board lock, renewed lease, reclaim on expiry or dead pid |
| Routing | assignee profile | bindings | labels, assignee, `handoff_to` allowlist |
| Workspaces | scratch, dir, worktree | per-agent workspace | worktree per item, outside the repo |
| Untrusted text | heuristic injection scan | random-boundary tags | security classifier on every item, memory and handoff |
| Isolation | "the OS is the only boundary" | optional sandbox | OS sandbox per command, required for Bash workers |
| Settings | per-profile home | per-agent config | global only; a project may only tighten |
