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
   `depends_on` is `done`, and only when its `assignee` is empty, names this worker's
   profile or crew. The item moves to `in_progress` with a lease.
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
7. **Survey.** A profile with a `kanban.survey` block finds its own work
   when the board has none for it; see [Finding work](#finding-work-kanbansurvey).
   The survey comes before the quiet window below.
8. **Done.** A worker exits by itself once the board has nothing left for
   it. With no claimable item and no teammate in its crew (same repository)
   starting or working, it waits a quiet window of two polls, so a handoff
   made just before is claimed first, then stops. Its registry record reads
   `stopped` with the reason `done: nothing left to claim`, and its
   `agents.max_workers` slot is free. A teammate that is working holds the
   window open, because it may still hand an item over. `-stay` on
   `belai agent run` or `start` keeps a standing worker that waits for new
   items, and a worker on a cron `schedule` always stays.

### Finding work: `kanban.survey`

A worker normally takes only items someone filed. A `kanban.survey` block
lets it file its own:

```json
"survey": {
  "title": "Survey {project} for work",
  "body": "What to look for …",
  "list": "review",
  "every": "24h"
}
```

When the worker has nothing to claim, the harness files one survey item and
claims it at once:

- **Title.** The title is the survey `title` with `{project}` replaced by the
  project name (or "this repository"), plus the date:
  `Survey belai for work (2026-09-29)`.
- **Body and labels.** The body is the survey `body`. The labels are the
  worker's own claim labels plus `survey`.
- **Worked like any item.** It runs through the same classifier, budgets,
  lease and routing as any other item, and goes to `on_success` when done.
- **Handoffs.** Every `KanbanHandoff` made while working a `survey` item goes
  to the survey's `list`, whatever list the model asks for. The default is
  `review`, where a human confirms self-found work before any agent takes it.
  The harness enforces this, not the prompt. Any item labelled `survey` gets
  this treatment, so adding the label by hand can only make handoffs more
  cautious.

Limits:

- **Filed work comes first.** A survey runs only when no item is claimable.
- **Once per start.** A worker surveys at most once each time it starts.
- **Once per `every`.** It surveys at most once per `every` (at least `1h`,
  default `24h`) for the same profile and repository on the same machine. The
  last survey's time is the mtime of a stamp file under the registry
  (`agents/run/surveys/`). A worker restarted within the interval logs
  `survey skipped` and does not survey.
- **Never for a targeted run.** `-once` and `-item` never survey.
- **Shared across hosts.** The dated title makes hosts that survey the same
  project on the same day share one item. The board refuses a second open
  item with the same title, and a survey another worker already holds is
  left to it.
- **Handoffs required.** A survey needs `handoff_to` or `handoff_labels`,
  because it reports what it finds as handoffs.

After the survey the worker is idle again. It exits after the quiet window
unless `-stay` or a `schedule` keeps it.

The built-in `belai:scout` surveys every 24 hours into `review`. It runs the
project's tests, build and lint to find failures. It compares specs, PRDs,
design notes, READMEs, `docs/` and the static site's prose with the code, to
find flags, defaults, limits, commands and behaviours that disagree. It also
looks for untested business rules, missing or stale docs, and site prose to
add or update. It files at most five `build` handoffs, discrepancies before
gaps. Moving one from Review to Backlog is the confirmation: a builder then
claims it by its `build` label.

### Pausing a worker

`belai agent pause ID|NAME` (or `p` on the workers tab of `/agents`) asks a
worker to pause. It finishes the card it holds, so no change is left half made,
then claims nothing and reports `paused` until `belai agent resume` (or `p`
again). The request is an empty marker file beside the worker's registry
record, so it carries no text. A paused worker keeps its slot and does not
count the wait as a dry board, so it is not exited by the quiet window. To
take a card over, pause the worker, release the claim (`u` in `/kanban`, or
`belai kanban release`) and continue the card's transcript in your terminal.

### Read-only workspaces

`"workspace": {"isolation": "worktree", "read_only": true}` is for a worker
that runs checks but changes nothing, such as the scout running tests:

- It gets a worktree like any other, so its commands cannot touch your
  checkout.
- The harness commits nothing the turn leaves behind (build output, coverage
  files), and the release note names no branch.
- The worktree and its branch are deleted after every item. A commit the
  model made anyway keeps the branch; see [Git in the worktree](#git-in-the-worktree).
- Its workspace note says the checkout is throwaway and that it must not
  edit, create or commit files.
- `read_only` needs `isolation: worktree`, and cannot be combined with
  `keep` or a `publish` other than `none`.

A read-only worker with `Bash` and `autonomy: autonomous` still needs the OS
sandbox and a pass budget, like any autonomous `Bash` worker.

### What a worker's model may do on the board

| Tool | In a worker |
|---|---|
| `KanbanSearch` | yes |
| `KanbanUpdate` | a note on the claimed item only |
| `KanbanHandoff` | files a new item with this item as its parent, to a profile in `handoff_to`; under a `survey` item always to the survey's list |
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

A worker classifies exactly as the TUI does with the same settings. With
`classifier.kind: "models"` in a binary built without embedded models (and no
explicit phase model), both use the full LLM sentinel instead and say so once
on startup, so one settings file serves every build variant.

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
`agent.pinHost`, and the website's Board page sets it when you
assign a card to a host there; with [remote control](remote-control.md#fleet-workers)
running, the page can also start the worker on that host. With no pin, any
host may claim the card: hosts pull, so the first matching worker wins, and
nothing chooses a host for it.

### Who an assignee names

An assignee is a bare profile name (`belai:builder`), or one of three prefixed
forms:

| Value | Names | Who may claim it |
|---|---|---|
| `NAME` or `worker:NAME` | a worker profile | workers of that profile |
| `crew:NAME` | a crew | any worker started in that crew (`Record.Crew`) |
| `person:HANDLE` | someone on the Vulnetix website | no worker, ever |

Rules and edge cases:

- **A bare name is a profile.** Boards written before the prefixes existed keep
  working. A profile whose own name starts with `crew:` or `person:` must be
  written `worker:crew:…`.
- **A crew assignee needs a crew member.** A worker started alone (no crew)
  never takes a `crew:` card, and a worker of another crew does not either.
  `belai kanban assign -crew` still routes by labels with no assignee; set
  `crew:NAME` with `kanban assign` or `-assignee` when the card must stay in
  the crew.
- **A person is not a worker.** The card waits for that person. The HANDLE is
  opaque: the website writes `person:<member id>` for someone in the
  organization, or `person:invite-<id>` while an invitation is pending, and the
  board shows both as `@person`. The address never reaches the board or a host.
- **`assigned_only` still applies.** A profile with `kanban.assigned_only`
  takes only cards assigned to it or to its crew, never an unassigned card.
- **The shape is checked on both sides.** Letters, digits and `. _ : -`, at
  most 64 characters. Anything else is refused by the CLI, the TUI and the API.

## Crews

A crew is a named set of profiles started together:

| Crew | Members |
|---|---|
| `belai:delivery` | `belai:scout` ×1, `belai:builder` ×2, `belai:reviewer` ×1 |
| `belai:security` | `belai:vuln-scout` ×1, `belai:patcher` ×2, `belai:verifier` ×1 |

Delivery: file an item labelled `scout` ("survey internal/foo for missing
tests"). The scout reads, runs the project's checks in a read-only worktree,
and hands off one `build` item per concrete task to backlog. With no `scout`
item on the board it [surveys the repository itself](#finding-work-kanbansurvey)
at most once a day, and its handoffs from that survey go to Review for you to
confirm.
Builders implement each on its own branch and hand it on as `needs-review`.
The reviewer checks the branch out, runs the tests, and moves the item to
`done`, or back to `backlog` with its notes.

### The security crew

Start it with `belai agent start -crew belai:security` or `/fleet`. It works
the repository's findings from scan to verified fix. Two checks stop double
work, and there is no other gate: no cache and no daily limit.

1. **One crew per repository.** A second start is refused while a worker of
   the crew is live in the same repository.
2. **One review per commit.** The scout reads `.vulnetix/` (`memory.yaml`, the
   CycloneDX files and the SARIF files) for the full commit id of HEAD. When
   an artefact records it, no scan runs. When none does, the harness runs the
   [review scanners](vulnetix.md#review-evidence-and-vex-files) itself; no
   model is asked whether a scan ran. A review that leaves no artefact for
   HEAD files nothing, so stale artefacts never become cards.

Every finding then has one card for the repository, titled
`[sca] GHSA-… package`, labelled `vuln`, carrying the finding id and the commit
whose scan last showed it. The body holds identifiers, versions, paths and a
severity word, never scanner or advisory text. A finding that returns after its
card was done gets a new card linked to the old one.

On a later HEAD, a card whose finding has left the report becomes a *gone*
card: it moves to `review` labelled `gone` and `needs-verify`, with the verdict
`fixed`. Patchers make this comparison from the artefacts on disk before every
claim and never scan themselves. A kind whose scanner produced nothing for HEAD
is not compared, so a missing report never closes a card.

A patcher fixes one card on its branch. After each attempt it re-runs the
scanner and reads the result before choosing what to try next. It can also
record a verdict with `KanbanVerdict` instead of a fix. The verifier checks
every claim itself: it re-runs the scanner, repeats a false positive's
evidence, looks again for a fix, and for a gone card works out why the finding
left the report. It then records its own verdict, and the harness moves the
card and writes a VEX for it:

| Verdict | Recorded by | Result |
| --- | --- | --- |
| `fixed` | patcher | `review` for the verifier |
| `false_positive`, `no_fix`, `needs_human` | patcher | `review` for the verifier, no branch to publish |
| `fixed`, `false_positive` | verifier | `done`, VEX `fixed` or `not_affected` |
| `no_fix`, `needs_human` | verifier | `blocked`, VEX `affected` or `under_investigation` |
| `rejected` | verifier | back to `backlog` labelled `vuln`, one failed attempt, no VEX |

A false positive needs evidence anyone can check independently and one of the
five OpenVEX justifications; `no_fix` needs the list of what was tried. A
verifier that records no verdict closes nothing. You can still file an item
labelled `vuln-scan` to ask the scout for a targeted look. See
[Review evidence and VEX files](vulnetix.md#review-evidence-and-vex-files).

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
| `belai agent start NAME \| -crew CREW [-max-workers N] [-stay]` | start detached workers; `-max-workers` replaces `agents.max_workers` for this start |
| `belai agent ps` | running and recently stopped workers |
| `belai agent logs ID [-f]` | a worker's log |
| `belai agent stop ID \| NAME \| -all` | stop workers; claims are released |
| `belai agent pause ID \| NAME` | finish the card in hand, then claim nothing until resumed |
| `belai agent resume ID \| NAME` | take cards again |
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

The `/agents` hub has four tabs, `1` to `4` or `tab` to move: **live** (agents
running now in this session, including helpers), **profiles**, **audit** and
**workers**. `/agents` opens the first useful one, and `/agents live`,
`profiles`, `audit` and `workers` open a tab directly. `/agents running` and
`/agents fleet` are the tabs' earlier names and still work. A helper (a
subagent a session starts for one job) appears only under its session in the
live tab; it is never a worker and cannot be assigned or paused.

`/fleet` opens the workers tab of `/agents` (also `4` there):
workers with their state, the item each holds, items done and failed, and a
heartbeat; `l` shows the selected worker's log, `p` pauses or resumes it, `x` stops it and `X` stops
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
| `v` | open the workers tab of `/agents` |
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
ends. It first checks that the worktree is still on the item's branch. A
`read_only` worker is the exception: nothing is committed for it.

When the item is released the worktree is removed, unless `workspace.keep` is
set. Its branch stays in the repository while it holds work. A `belai/`
branch that still sits at its base commit, with no commit of its own, is
deleted with the worktree, so a turn that changed nothing does not leave an
empty branch behind.

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

A branch with no changes beyond its base is never pushed. When the base
already does what the item asks, `PublishBranch` says so and names the base,
rather than asking for a commit, so the agent records that on the item. The
harness skips its own publish at `done` for such a branch, and the release
note reads "no files changed on" the branch. The empty branch itself is then
deleted with the worktree.

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
pid, directory, state (`starting`, `idle`, `working`, `paused`, `stopping`, `stopped`,
`failed`), the item it holds, heartbeat time and counts. `belai agent ps`, the
TUI's `/agents` screen and the other workers read it. A record whose process
has died is marked `failed` and its claim is released. Logs are
`~/.vulnetix/belai/agents/logs/<id>.log`.

A detached start (`belai agent start`, the TUI's `/fleet`, remote control)
writes the `starting` record itself, with the child's pid, before the worker
runs. A worker that dies before it registers, for example on a bad settings
file, is therefore swept to `failed` like any other and keeps its log, rather
than vanishing. The starter reaps the child, so a long-lived starter (the TUI)
never keeps a crashed worker as a zombie that looks alive and holds its slot.

The log says what the worker does, in harness words only:

- at start, what it looks for: `looking for backlog items labelled scout
  unassigned or assigned to belai:scout in project belai; polling every 30s,
  exiting after 1m0s with nothing to claim`;
- once per dry spell, `nothing to claim: no …` with the same description, so
  an idle worker says why;
- each claim, release and publish, and the exit reason on stop.

Anything the detached process prints to stderr before it registers (a bad
flag, an untrusted directory, a settings error) lands in the same file. With
remote control running, the tail of this log reaches the website's Sessions
page ([remote control](remote-control.md#fleet-workers)).

## Settings

```json
{ "agents": { "enabled": true, "max_workers": 4, "publish": true } }
```

- `enabled` turns workers on or off. A project may turn it off, never on.
- `max_workers` caps running workers on this machine (default 4). A project
  may lower it, never raise it. `belai agent start -max-workers N` replaces
  the user's cap for that one start; `belai rc` passes it when started with
  an explicit `--max N` (see [Remote control](remote-control.md#fleet-workers)).
  A project that lowered the cap still holds it: the start uses the lower of
  the two. Without the flag, `max_workers` applies.
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
- **One decision server for all workers.** When the classifier is the local
  decision model, workers share the machine's one llama-server
  (`internal/decisionserver`). The first process that needs it launches it
  under a file lock; a worker never downloads weights, and while the server
  is missing or restarting its checks go to the agent-model fallback.

## Compared with other agent harnesses

| | Hermes Agent | OpenClaw | Belai fleet |
|---|---|---|---|
| Claims | atomic, TTL, stale reclaim | claim tokens | atomic under the board lock, renewed lease, reclaim on expiry or dead pid |
| Routing | assignee profile | bindings | labels, assignee, `handoff_to` allowlist |
| Workspaces | scratch, dir, worktree | per-agent workspace | worktree per item, outside the repo |
| Untrusted text | heuristic injection scan | random-boundary tags | security classifier on every item, memory and handoff |
| Isolation | "the OS is the only boundary" | optional sandbox | OS sandbox per command, required for Bash workers |
| Settings | per-profile home | per-agent config | global only; a project may only tighten |
