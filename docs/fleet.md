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
   items, and a worker on a cron `schedule` always stays. `-drain` is the
   opposite: the worker exits once nothing is left to claim even when its
   profile has a `schedule`. `belai rc` passes it when a stored schedule fires
   a worker (see [remote-control.md](remote-control.md#scheduled-agents)),
   because the stored schedule is what starts it each time.

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
  `survey skipped` and does not survey. A start up to two minutes before the
  interval is up (a tenth of the interval at most) still surveys: the stamp is
  written after the item is filed, so a start exactly one `every` later, as an
  hourly schedule makes, always lands a moment short and would otherwise be
  skipped about as often as not.
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

### A survey on a schedule

A worker surveys at most once each time it starts, so a standing worker (a cron
`schedule` in its profile, or `-stay`) surveys once and then waits for items. Work
that should happen every hour is started every hour instead, by either of:

- a stored schedule in `belai rc` (the Hosts page), which runs
  `belai agent start -drain NAME` in a directory the host offers
  ([remote-control.md](remote-control.md#scheduled-agents));
- the operating system's cron, with the same command in the repository:
  `0 * * * * cd /path/to/repo && belai agent start -drain NAME`.

`-drain` makes the worker exit once nothing is left to claim, so each start is one
review. With `kanban.survey.every: 1h`:

- **Each start surveys.** Because of the two-minute grace above, the start at the
  top of every hour files and works one survey item. A start less than about 58
  minutes after the last survey is skipped, and a log line says so.
- **Filed work comes first.** If a card the worker can claim is waiting, the start
  works that card and does not survey, so the survey for that hour does not
  happen.
- **One title a day.** The survey item's title is the survey `title` plus the date,
  so the hours of one day share a title. A finished item never blocks the next
  hour's, because the board refuses only a second *open* item with the same title.
  An item still open (a long run that holds its lease, or one the harness blocked
  because the worker needed an ask) means the next start finds it, cannot claim it
  and files nothing until that item is finished or moved, or the date changes.
- **Handoffs wait for a person by default.** Every handoff made while working a
  survey goes to `survey.list` (`review` unless the profile says `backlog` or
  `auto`), whatever the model asks.
- **Missed hours are not made up.** A schedule that was not running at the hour
  skips it, and the worker reads its own notes for what it last covered.

### Facts

A worker profile may carry `facts`: structured key/value pairs about the
environment it works in, such as an AWS role, a Terraform directory or a
Kubernetes context ([Facts](agent-profiles.md#facts)). The worker's persona lists
them, and the cloud tools read the well-known ones, so a crew whose members
share an account declares the account once per profile and each member points at
the right place. A `terraform_dir` is relative to the worker's worktree. A worker
that runs under a role asks nobody: a role its profile lists is used. A role it
does not list always asks, so it is withheld even for an `autonomous` worker, and
the item moves to `blocked` as under [Asks](#asks). Facts are part of
the behaviour a running worker pins, so editing them stops and restarts it, like
`knowledge`.

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

### Where a worker may start

A worktree is a git checkout, so a profile with `workspace.isolation: worktree`
(every built-in worker, and any profile that writes) must start inside a git
repository. `belai agent run` and `belai agent start` check this before
anything else is claimed or filed and refuse with `profile NAME uses
workspace.isolation: worktree, which needs a git repository, and DIR is not
one`. The check follows the working directory up to its repository root, so a
subdirectory is fine. A folder that holds several repositories is not: change
into one of them, or start one worker set per repository. A schedule from
`belai rc` runs `belai agent start -drain` in its directory, so a schedule whose
directory is not a repository is refused the same way and its run is recorded as
`error`.

A profile with no `workspace` block, `isolation: none` or `isolation: shared` is
not checked, and it runs in the folder itself, git or not. The harness then
makes no worktree, branch or commit and touches nothing in git, which is how a
read-only worker such as an hourly log analyzer runs from a folder that holds
several repositories (see [Filing a handoff under another repository](#filing-a-handoff-under-another-repository)).
Only a profile that cannot write may omit the `workspace` block: one with
`Write`, `Edit` or `Bash` needs `worktree` or `shared`. A repository with no commit passes this check and then fails when
the harness prepares the first worktree, which the worker investigates as a
setup failure (the failure is attached to the item and the release note stays
harness facts).

### Filing a handoff under another repository

A worker started in a plain folder, or one that finds work for several
repositories, can send each handoff to the repository that owns the problem.
Set `kanban.handoff_repos: true` on its profile and `KanbanHandoff` gains an
optional `repo` argument:

```json
{"title": "Raise the queue's visibility timeout", "body": "…", "labels": ["infra"], "repo": "acme/website"}
```

The item is filed under that repository's project and directory, where the agents
started in that repository take it. Without `repo` it is filed under the
worker's own project, as before.

- **A name is a choice from the harness's index, never a path.** `repo` must
  match a checkout in the local repository index, the same list the `Repos` tool
  shows. The harness derives the project and directory from that checkout. A
  path, a URL, a project name or any other text is refused, and nothing is filed.
- **What the index holds.** Git checkouts found in the children of the working
  directory's parent, and in their children: the folders beside the worker and the
  repositories beneath them. It holds at most 200 checkouts from at most 500
  directories. Hidden directories and those named `node_modules`, `vendor`,
  `target` and `dist` are skipped, and symlinks are not followed.
- **How a name matches.** `owner/name`, from the checkout's origin remote, or a
  bare name, case-insensitively. A bare name that two checkouts share is refused.
  A checkout with no origin remote has no owner, so it is named by its directory.
  A refusal lists the names the index holds (at most twenty).
- **Everything else about the handoff is unchanged.** The labels must be ones
  `handoff_labels` lists, an assignee one of `handoff_to`, and the hop and
  per-item limits still apply. A survey's handoffs still go to the survey's
  `list`, whatever `repo` says. The item links to the item the worker holds, and
  the worker may add notes to it though it sits in another project.
- **Duplicates are per project.** The board refuses a second open item with the
  same title in the same project, so the same title filed under two repositories
  makes two items, and filed twice under one makes one.
- **An agent in that repository does the work.** A builder claims items in its
  own project, so it must be started in the repository the item was filed under
  (`belai agent start -crew NAME` from there). An item filed under a repository
  where no worker runs waits on the board.
- **Not with gates.** `handoff_repos` cannot be combined with a `gates` block, because
  a gate names a test suite of the worker's own repository.
- **No repositories beneath the worker.** Every name is refused, and the refusal
  lists nothing.

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
uploads only the lines the transcript wrote, and it takes no answers back.

With `sync.remote_prompts` on, a worker also takes text typed into its session
on the website. That is how a crew message works: the Agents page sends the
same text to each live worker of the crew as an ordinary web prompt. The text
is cleaned like any web prompt and enters a running turn as steering, admitted
by the role manager at the next pass boundary, as typed steering is. A message
that arrives while the worker is between cards is held (at most eight) and
steered into its next turn. A message is never a slash command, a shell command
or an answer. Each user line the worker writes carries `profile_facts`: the
profile name and hash, display name, palette, avatar, crew, model, provider,
effort and tool count, never the system prompt or the tool list, so a shared
thread can show which customisation produced each entry.

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

Each built-in member has a persona, so the console shows who is who. The
security crew is Rubber Duck (the scout), Kremvax (the patcher) and Dark Avenger
(the verifier); the delivery crew is Juniper Tallis (the scout), Odo Brannigan
(the builder) and Isadora Pell (the reviewer). See
[Built-in personas](agent-profiles.md#built-in-personas).

### The delivery crew

Start it with `belai agent start -crew belai:delivery` or `/fleet`. It runs
every time it is launched. You can file an item labelled `scout` ("survey
internal/foo for missing tests") and the scout works it. The scout also gets
its own work from the test suites: the harness runs the repository's detected
suites (or `tests.command`), ties what they show to HEAD, and files seed cards
for the scout to investigate. There is no daily limit and no cache. Two checks
stop double work:

1. **One crew per repository.** A second start is refused while a worker of
   the crew is live in the same repository.
2. **One run per commit, and the board.** A record of the run for HEAD is
   written to `.vulnetix/belai/quality/<commit>.json`. When one exists for
   HEAD, the suites do not run again. Every seed card is filed once per
   subject, so relaunching the crew on the same commit, or on a later one,
   never adds a card the board already has, whether the crew or a person filed
   it. A run in which no suite actually ran (every suite denied, a missing
   binary) is not recorded and is tried again at the next launch.

The suites run under your permission rules, the OS sandbox and the scrubbed
environment, exactly like the [post-end test pass](testing.md), and a Go suite
runs with `-cover`. Only identifiers and numbers are kept from the output: test
names, package paths, coverage percentages and exit codes. No output text
reaches a card, so a hostile test cannot write an instruction onto the board.

The seed cards, labelled `scout` and `quality`, focus the scout on:

| Seed | When | Priority |
| --- | --- | --- |
| a failing suite, with its failing tests and packages | a suite failed or timed out | 3 |
| the least covered packages | coverage was measured and a package is under 60% | 2 |
| packages with no test files | coverage was measured | 1 |
| `properties`: where property-based tests fit | once for each shape of the test setup (ecosystems, frameworks, suites, mutation tool files) | 0 |
| `fixtures`: better mocks, stubs and fixtures | the same | 0 |
| `contracts`: contract tests at the seams (flags, wire and file formats, plugin interfaces) | the same | 0 |
| `mutation`: check the tests with mutation testing (uses a configured tool, else proposes one) | the same | 0 |
| `docs`: docs, specs and site prose against the code | the same | 0 |
| `coverage`: measure test coverage | the harness could not measure it | 1 |
| `suite`: set up a test suite | none was detected (then only this and `docs`) | 0 |

A failing, coverage or untested card closes itself when a later run no longer
reports its subject. A category card is never reopened after it is done or
deleted, and changes only when the shape of the test setup changes. The
scout's handoffs from a `quality` card are routed by how clear they are
(`kanban.quality.list` is `auto` in the built-in scout). A clear, concise task
goes straight to Backlog for a builder; one that needs a person to confirm what
is meant, or to split it, waits in Review, and the model cannot ask it past that
gate. The harness decides from counts alone. A handoff is clear only when all of
these hold:

- the title is at most 100 characters;
- the body is 80 to 2000 characters, enough for a fresh agent to act on and not a
  sprawling brief;
- it has at least one runnable gate, so the harness can check it;
- it has at most 5 gates, since more says the task hides several outcomes.

The scout can also declare `clarity` as `needs_clarification` or `needs_split`,
which sends the card to Review whatever the counts say. Nothing moves a card the
other way: declaring a task clear never moves it out of Review. A card sent to
Review has a `review:` line appended to its body naming each reason in harness
words, for example `review: body under 80 characters; no runnable gate the harness
can check`. Set `kanban.quality.list` to `review` for every handoff to wait for
you, or to `backlog` to skip the gate for every handoff. `kanban.survey.list`
takes the same three values.

Builders implement each on its own branch and hand it on as `needs-review`.
The reviewer checks the branch out, runs the tests, and moves the item to
`done`, or back to `backlog` with its notes. With [gate verification](#verification)
on (the built-in builder and reviewer set it to `enforce`), the harness runs the
card's acceptance gates itself on the builder's branch and again on the
reviewer's, and a card is done only when they pass.

### Files placed in a worktree

Workers each work in a worktree of their own, outside the project, so the harness
puts into it what the profile names. Two blocks do it, and both are the profile's
to define, whoever wrote it.

**Reference documents** (`knowledge.paths`). The documents a profile lists are
indexed for search by meaning (see [Knowledge](knowledge.md)), and the harness
also copies them into the worktree, read only, before each turn: a relative path
at the same relative path, one under `~/` or absolute under
`.vulnetix/knowledge/<label>/`. The agent can find a passage with `Grep` as a
`kb+` row and open the file with `Read`. The copies are never written back or
committed, and a file the branch already has is never replaced.

**Shared files** (`workspace.sync`). A crew that needs a shared scratchpad or
long-term memory lists a file or directory there:

```json
"workspace": {
  "isolation": "worktree",
  "sync": [{"path": ".vulnetix/crews/delivery.md", "access": "write"}]
}
```

The harness does the copying, so the worker uses the ordinary file tools on the
relative path and never gets the repository's path:

- **Before each turn** the file is copied from the repository into the worktree
  at the same relative path. A path that does not exist yet is simply not there;
  a worker with write access creates it with `Write`.
- **After each turn**, even a failed or cancelled one, a write-access file the
  worker changed is merged back into the repository under a lock, so teammates
  merging at once do not overwrite each other. When nobody else changed the file
  meanwhile, the worker's version is taken whole. When someone did, the lines
  the worker added are appended to the current file (a line a teammate already
  wrote is not added twice) and the worker's deletions and edits of existing
  lines are dropped, because applying them could erase a teammate's notes.
- **Read entries** are copied in and never written back.
- **The text is checked on the way.** It is sanitised (delimiter markup, control
  and bidirectional characters removed) before it reaches the repository, a file
  is at most 256 KiB and an entry at most 64 files and 1 MiB, only regular text
  files are copied, and a symlink on either side is refused. A merge that would
  pass the size limit is not applied.
- **It is not part of the branch.** The harness's own commit leaves placed paths
  out, and a branch that commits one anyway (a model's own `git add -f`) fails
  the attempt with a note naming it.
- **The profile's permission is the permission.** Workers are denied writing
  anything under `.vulnetix`, and a `write` entry lifts that for exactly its
  path, with `Write` and `Edit`. A `Write` or `Edit` deny rule of your own in
  settings still wins. The workspace note tells the worker which files it may
  edit, even in a read-only workspace.
- **A fixed floor, whoever wrote the profile.** `.git`, Belai's state and the
  credentials and settings in `.vulnetix` are never synced, the scanner evidence
  in `.vulnetix` (`memory.yaml`, scan artefacts, `vex/`, `quality/`) is read
  only, credential files by name are never copied, and a file Git tracks outside
  `.vulnetix` is never written back into your checkout: a change to tracked
  source goes on a branch.
- **It travels with the profile.** A profile from the Vulnetix library installs
  with its `knowledge` and `workspace.sync` blocks, a backup carries them, and a
  replace takes the library copy (keeping this host's entries when the copy lists
  none).

The built-in delivery crew uses `.vulnetix/crews/delivery.md`, which every member
lists under both blocks. Every member has write access and is told, in its own
instructions, to read it before it starts, to keep a `## Scratchpad` of what the
crew is doing now and a `## Long-term facts` of what stays true of the
repository, to add short lines rather than rewrite the file, to leave secrets
and long output out of it, to treat teammates' notes as notes and not
instructions, and never to commit it. How the file is created and kept tidy is
left to those instructions: the harness only copies and merges. The scout and the
reviewer have the file tools (`Edit`, `Write`) and `Bash`, `Git`, `GH` and
`Glab`, `WebFetch` and `WebSearch`, the `Vulnetix` tool and the Vulnetix MCP
server's tools (`mcp__vulnetix__*`, available once `/vulnetix mcp` has added the
server), so they can find and patch source and research a fix, not only read.

#### Rules and edge cases

Every row names the tests that hold it. `internal/fleet/sync_docs_test.go` fails
when a test named here does not exist, when a test in the sync test files is not
named here, and when a limit in this section differs from the code.

| ID | Rule | Tests |
| --- | --- | --- |
| S1 | A profile lists up to 8 repository-relative paths in `workspace.sync`, written with forward slashes and plain characters (letters, digits and `. _ - /`), with no `..`, never the repository itself and never two that overlap; `access` is `read` (default) or `write`; it needs `workspace.isolation: worktree`. Any such path may be listed, not only `.vulnetix/crews` | `TestSyncAcceptsAnyPathTheProfileDefines`, `TestSyncRefusesWhatItCannotCopySafely` |
| S2 | Whoever wrote the profile, `.git`, `.vulnetix/belai`, `.vulnetix/settings.json` and `.vulnetix/credentials.json` are never synced, and the scanner evidence in `.vulnetix` (`memory.yaml`, scan artefacts, `vex/`, `quality/`) is read only | `TestSyncRefusesProtectedPathsAndSymlinks`, `TestSyncAcceptsAnyProfileDefinedPathAndRefusesTheProtectedFloor` |
| S3 | Each listed file is copied from the repository into the worktree before each turn; a file that does not exist yet is not an error, and a worker with write access creates it | `TestSyncCopiesInAndMergesBackAWriteEntry`, `TestSyncCreatesAFileTheWorkerWritesFirst` |
| S4 | After the turn a write-access file the worker changed is merged back under a lock: its version when nobody else changed the file, otherwise the lines it added appended, a line a teammate already wrote not added twice, and its deletions and edits of existing lines dropped | `TestSyncMergesConcurrentEditsWithoutLosingATeammatesLines`, `TestSyncDeletionsApplyOnlyWhenNobodyElseChangedTheFile`, `TestMergeLinesCases` |
| S5 | A `read` entry is copied in and never written back | `TestSyncAReadEntryIsNeverWrittenBack` |
| S6 | A directory entry copies every file under it, and a file the worker adds there is merged back; a `.part` file is never synced | `TestSyncDirectoryEntriesCopyEveryFileAndTakeNewOnesBack` |
| S7 | Text is sanitised (delimiter markup, control characters) before it reaches the repository | `TestSyncSanitisesWhatItWritesToTheRepository` |
| S8 | A file is at most 256 KiB, an entry at most 64 files and 1 MiB, and a merge that would pass the file limit is not applied | `TestSyncBoundsFileSizeAndMergedSize` |
| S9 | The harness's commit leaves placed files out, and a branch that commits one anyway is reported | `TestSyncedFilesAreNeverCommittedByTheHarness` |
| S10 | A `write` entry lifts the worker's deny on `Write` and `Edit` for exactly its path, matching the subjects the real file tools give, and nothing else under `.vulnetix` | `TestSyncPermitsLiftOnlyTheWriteEntries`, `TestSyncPermitsMatchTheFileToolsSubjects`, `TestHarnessDenyBlocksUnlessPermitted` |
| S11 | A `Deny` or `Block` rule of the user's still wins, a permit never exempts a shell line, and neither the harness rules nor the permits are read from a settings file | `TestPermitNeverOverridesAUsersDeny`, `TestPermitDoesNotExemptAShellLine`, `TestHarnessAndPermitAreNotReadFromSettingsFiles` |
| S12 | A file Git tracks outside `.vulnetix` is never written back into the checkout | `TestSyncWriteBackNeverOverwritesATrackedFile` |
| S13 | The documents in `knowledge.paths` are copied read only (a relative path in place, an outside path under `.vulnetix/knowledge/<label>/`), refreshed when the source changes, and never over a file the harness did not place | `TestReferenceDocumentsAreCopiedIntoTheWorktreeReadOnly`, `TestReferenceDocumentsRefreshAndNeverClobberTheBranch` |
| S14 | The workspace note names the files the worker may edit, even in a read-only workspace, and says never to commit them | `TestWorkspaceNoteNamesTheCrewFilesAndTheCommitRule` |
| S15 | The block survives the markdown form, rejects unknown keys, restarts a running worker when it changes, installs from the library with a profile, is carried by a backup, and a replace keeps this host's entries when the copy has none | `TestSyncSurvivesMarkdownRejectsUnknownKeysAndIsBehavioural`, `TestInstallKeepsTheFilesAProfileSyncs`, `TestBackupCarriesSyncAndAReplaceKeepsLocalSyncWhenTheCopyHasNone` |
| S16 | Every member of the delivery crew shares `.vulnetix/crews/delivery.md` with write access and is told how to use it; the scout and reviewer can research and patch | `TestDeliveryCrewMembersShareTheCrewNotesFile`, `TestScoutAndReviewerCanResearchAndPatch` |
| X1 | A symlink the worker plants in place of a synced file, or a symlinked directory in the repository, is refused and reported, and the repository file is left as it was | `TestSyncOutRefusesASymlinkTheWorkerPlanted` |
| X2 | A reference document under a credential store, under `.git`, through a symlink out of the repository, or the home directory itself is never listed or copied | `TestReferenceDocumentsNeverReachAProtectedPlaceOrCrossASymlink` |

### Acceptance gates

A gate is one observable outcome a card must show before it is done. The scout
files each handoff with the gates that prove it, so a builder knows what done
means and the harness has something to check.

A gate is a reference, never a command. No model writes a command line into
the board, and no gate can run anything the harness did not already detect:

| Part | Rule |
| --- | --- |
| `title` | The outcome, as a statement that is true when the task is done. Cleaned to one line and at most 120 characters. Two gates on one card cannot share a title. |
| `kind` | `runnable` (a detected test suite decides it) or `manual` (a reviewer decides it, for what no test can observe). |
| `suite` | Runnable only. The name of a suite the harness detected from marker files (`go`, `pytest`, `just-check` and so on). A name that was not detected is refused, and the refusal lists the detected ones. When nothing was detected, only manual gates are possible. |
| `dir`, `test` | Runnable and Go suites only. `dir` narrows the suite to one package directory of the repository (a relative path of letters, digits and `. _ - / @ +`, with no `..`, no leading `-` and no trailing `/`; it must exist and stay inside the repository after symlinks). `test` narrows it to one test function name (letters, digits and underscores). A test needs the `dir` of its package. Any other ecosystem runs the whole suite. |

The harness assigns ids by position (`G1`, `G2`, and so on, at most eight a
card) and starts every gate `unmet`. A model cannot file a gate as already met,
and cannot choose an id.

Rules and edge cases:

- **A bad gate refuses the whole handoff.** An undetected suite, an unsafe
  `dir` or `test`, a repeated title, a ninth gate or a manual gate that names a
  suite returns an error saying what to fix. No card is filed and nothing is
  dropped silently, so a scout cannot end up with half its plan on the board.
- **`kanban.gates.require`** makes a handoff with no gate an error. The
  built-in `belai:scout` sets it. A worker without a `gates` block keeps the
  handoff tool exactly as it was, and the repository is not scanned for suites.
- **A person files gates with `belai kanban import`.** A line of the JSONL file
  may carry `gates`, a list of objects with `title`, `kind` and, for a runnable
  gate, `suite` and optionally `dir` and `test`. They are checked exactly like a
  scout's (an unsafe `dir`, a bad `test`, a repeated title or a ninth gate refuses
  the line, with the line number), get harness ids and start `unmet`. The suite is
  checked against the detected suites when the card is worked: one that is not
  detected then is `unmet` with that note.
- **A worker sees its card's gates.** The card a worker is given lists each gate
  with its id, what decides it, its state and its note (`- G1 [runnable go
  internal/parse TestLast] unmet: title (exit 1: TestLast)`), so a builder knows
  what done means and reads why the last attempt failed.
- **A worker never edits gates.** Gates are set when the card is filed, and a pulled copy of a card from the Vulnetix
  website never carries or replaces them (they stay on the host that decided
  them, like a finding's fields).
- **The board file is version 3.** A Belai that predates gates refuses the
  board instead of rewriting it without them; a version 2 board still reads, with
  no gates. See [the board file](bkan.md).

#### Verification

With `kanban.gates.verify` set, the harness runs a card's gates itself. It does
this after a worker's turn ends and its work is committed, still holding the
card's claim (the lease keeps renewing while the suites run), and before the
card moves. The built-in builder and reviewer both set `enforce`.

| Mode | What the harness does |
| --- | --- |
| `off` (default) | Nothing. A card is done when the worker says so and the goal evaluator agrees. |
| `record` | Runs the gates on the branch and records each gate's state, without changing where the card goes. The result is added to the release note. |
| `enforce` | As `record`, and the result decides: a card whose gates are not all met is a failed attempt, so it goes back to the list it was claimed from (with the usual attempt limit, then `blocked`). A model's claim that it finished cannot override it. |

How a gate is decided:

- **The argv comes from the table.** A runnable gate runs the command of the
  detected suite it names. A Go suite with a `dir` runs `go test -count=1 ./DIR`
  and, with a `test`, adds `-run ^NAME$`; the identifiers were validated when the
  gate was filed and `dir` is anchored with `./`, so neither can be read as an
  option. Any other suite runs its own command. Nothing is run through a shell.
- **The exit code decides**, under your permission rules, the OS sandbox and the
  scrubbed environment, exactly like the [post-end test pass](testing.md). One
  more rule: a run that tested nothing is not a pass. A Go test name that
  selected no tests, or a package with no test files, leaves the gate `unmet`, so
  a mistyped name can never certify itself.
- **Identical commands run once.** Two gates, or a gate and a suite, that resolve
  to the same argv share one run.
- **A manual gate is never run.** It stays `unmet` until a reviewer records it.
- **Nothing to verify passes.** A card with no runnable gate, and no record for
  its base commit to compare against, has nothing to check and moves on.

The regression check compares with the quality record of the card's base commit,
which the quality sweep writes. With one, a suite that passed at the base and
fails on the branch is a regression, and so is a Go test that fails on the branch
in a suite that already failed at the base but did not fail there. A suite that
failed at the base with no test names to compare counts as failing already. With
no base record nothing is compared, and the note says nothing about regressions
rather than claiming there were none.

What the card and the next attempt see is harness facts only: the commit, the
gate ids, exit codes, suite names and test names, for example
`verification failed at 3fa9c1d20e44: G1 unmet (exit 1: TestA); regressed: go:TestB`.
No test output text reaches a card, a note or a model, so a hostile test cannot
write an instruction onto the board. A cleaned test name is at most 100
characters and a note names at most five tests.

Edge cases:

- **A gate that cannot run blocks the card.** A deny rule, an ask rule nobody can
  answer, a missing binary or a required sandbox that is unavailable says nothing
  about the work. The card goes to `blocked` for a person, like a permission the
  worker cannot ask for, and is not counted as a failed attempt.
- **A suite that is gone is unmet, not blocked.** A gate naming a suite the
  workspace no longer detects is `unmet` with that note, and nothing runs for it.
- **The item's budget ending during verification** fails the attempt with a note
  saying so; a stop request or a lost lease leaves the board as those always do.
- **The record.** Each verification that ran a suite is written to
  `.vulnetix/belai/quality/verify/<card>-<commit>.json` by the harness alone: gate
  states, exit codes and suite statuses, never output. The last 50 are kept, a
  symlinked directory is refused, and a name that is not a card id plus a commit
  id is refused.

#### Manual gates and the reviewer

A manual gate is for what no test can observe: wording, a product decision, a
behaviour only a person can judge. The harness never runs one. A worker whose
profile sets `kanban.gates.review` (the built-in reviewer does, and it needs
`verify` to be `enforce`) gets `KanbanGate`, bound to its claim like
`KanbanVerdict`: it takes a gate id, a state (`met`, `unmet` or `abandoned`) and
one line of evidence, writes only the card it holds, and refuses a runnable gate
with a message saying the harness decides those. The evidence is cleaned to one
line and at most 200 characters, and stays on the gate.

Under `enforce` with `review` on, the card is held to its manual gates when the
reviewer's turn ends:

| What the reviewer left | Where the card goes |
| --- | --- |
| every manual gate `met` and every runnable gate verified | `done` (and, per the profile, a draft pull request) |
| a manual gate not decided, or `unmet` | back to the builders as a failed attempt; the note names the gate ids |
| a manual gate `abandoned` | `blocked`, with `HANDOFF REQUIRED` and the gate ids in the note |

Rules and edge cases:

- **Each review decides afresh.** Before a reviewer works a card, the harness sets
  every manual gate that was `met` or `unmet` back to `unmet`, so a gate met for
  an earlier version of the branch never carries over. An `abandoned` gate stays
  abandoned: that is a person's decision to reverse.
- **Abandonment is never success.** A gate is abandoned only when the outcome is
  genuinely impossible within the task. The card stops in `blocked` as a handoff
  to a person, the note names gate ids and never the reviewer's words, and the
  reviewer's evidence stays on the gate for the person to read.
- **A reviewer cannot overrule the harness.** A reviewer that approves a card
  whose runnable gate fails is overruled: the card goes back with the harness's
  facts, whatever the model said.
- **A builder decides no manual gate.** It has no `KanbanGate`, and its own route
  never waits on manual gates.

#### Request coverage

A request can have several parts, and a plan that quietly leaves one out is
easy to miss. With `kanban.gates.coverage` (the built-in scout sets it) the
harness makes an omitted part visible.

For a request card, meaning one that is neither seeded from a test run (`quality`)
nor one the worker surveyed for itself (`survey`), whose facts the harness
already measured, the scout:

1. records the request's clauses with `KanbanContract`: the independently
   omittable parts of what was asked, at most 12, each one line of at most 160
   characters, numbered `C1`, `C2` and so on by the harness;
2. gives every handoff `covers`, the clause ids the task covers.

The harness holds the rest to that, and it is deterministic:

| Rule | What happens |
| --- | --- |
| A handoff before the clauses are recorded | refused, saying to record them first |
| A handoff that covers no clause, or an unknown one | refused; a task that covers no clause is not part of the request |
| Recording clauses after a handoff exists | refused, since re-planning would orphan the covers |
| A request the scout completed with no clauses recorded | a failed attempt: it was never planned |
| A clause no handoff covers when the scout finishes | a gap card, filed by the harness |

A gap card is titled `Part C2 of request K-xxxxxx is not covered by any task`.
It names ids only, never the clause text (the text is the scout's words and
stays on the request card), and it carries the `coverage` label, which no
built-in profile claims, so it waits in the Backlog for a person to plan it,
file it as a task or drop it. It is filed once for each clause: never reopened
after it is done, never doubled by a retry or a relaunch, and never recreated
after a person deletes it. The scout's own card then completes, with a note such
as `coverage: 1 of 2 clauses covered, gaps C2, 1 gap card(s) filed`. A deleted handoff
covers nothing.

#### Drafted gates and the delivery note

Two fast-model roles help a card that reaches the crew without much structure.
Neither can change what the harness decides.

**Drafted gates** (`kanban.gates.draft`, set by the built-in builder). A card a
person filed usually has no gates, so the reviewer has nothing to check it
against. When a worker with `draft` claims a card that has no gates, the
harness gates the card's text like any item text (a withheld card is not sent),
and the fast model's `gate_draft` role writes one to four outcomes a reviewer can
check by reading the change. Each becomes a manual gate.

- **Always manual.** A drafted line can never name a suite, a package or a
  command: the harness files it as a manual gate, and a line that looks like a
  command or a path (a backtick, `$`, `;`, `|`, `<`, `>`, or a leading `/`,
  `./` or `$ `) is dropped. The gates a reviewer then decides with `KanbanGate`
  are the ones that matter, and the builder's own route never waits on them.
- **Only when there are none.** A card that already has gates keeps them, costs
  no call, and is never given more.
- **A failure changes nothing.** No fast model, a transport error, an empty
  reply or nothing usable leaves the card without gates, exactly as before.
- **Size.** At most 4 gates, each at most 100 characters, a line under 10
  characters is dropped, and a repeat is dropped.

**The delivery note.** When the harness publishes a draft pull request at `done`,
it appends a short note on how the card's gates were checked. The fast model's
`delivery_report` role writes it from harness facts only: the file count, each
gate's id, kind and state, whether the harness verified the runnable gates, the
regression count against the base commit (or that none was compared) and how
many request clauses have a task. It never sees the card's words, a gate title, a
note or test output. With no fast model, an error or an empty reply, the harness
writes the same facts as plain sentences. The note is at most 500 characters.

### The security crew

Start it with `belai agent start -crew belai:security` or `/fleet`. It works
the repository's findings from scan to verified fix. Two checks stop double
work, and there is no other gate: no cache and no daily limit.

1. **One crew per repository.** A second start is refused while a worker of
   the crew is live in the same repository. The check and the spawns run
   under one lock, so two starts fired together cannot both get in.
2. **One review per commit.** The scout reads `.vulnetix/` (`memory.yaml`, the
   CycloneDX files and the SARIF files) for the full commit id of HEAD. When
   an artefact records it, no scan runs. When none does, the harness runs the
   [review scanners](vulnetix.md#review-evidence-and-vex-files) itself; no
   model is asked whether a scan ran. A review that leaves no artefact for
   HEAD files nothing, so stale artefacts never become cards.

The scout, the patchers and the verifier each list `.vulnetix` in their profile's
`knowledge` block, so the review's artifacts (SARIF, CycloneDX, OpenVEX, `memory.yaml`
and the `vex/` documents earlier verdicts left) are searchable by meaning with
`Grep` and `Glob` as `kb+` rows, whatever worktree a member works in. The index is
built from the repository's own `.vulnetix`, never the worktree's. See
[Knowledge](knowledge.md).

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

After a sweep that files new cards, the harness files one more item for that
commit, labelled `sweep`, and the scout works it as a model turn: it runs the
Vulnetix fix dry run for the manifests the new cards name and adds a note to
each card with the fixed version, the exact edit and the lockfile command, so a
patcher starts with the answer. The claim lists which cards it may annotate. The
harness derives them from fields it set (the commit the card was filed at, its
list, its labels, whether it is claimed), never from the item's text, and those
cards take notes but no edit or move. At most 20 cards are listed, highest priority first, and a card a patcher has claimed since is refused a note.

A patcher fixes one card on its branch in up to `rounds` turns (three for the
built-in patcher). After each turn the harness scans the worktree itself, with
the fixed `sca` argv, and while the scanner still reports the card's finding it
runs another turn with the result attached: the package, version, file, severity
and how many findings remain, never scanner text. If the scanner still reports
it after the last round, the attempt fails whatever the model said, and the
usual attempt limit applies. A scan that gives no answer (no Vulnetix CLI, a
failed scan) leaves a single turn. The scan's `.vulnetix` directory is removed
from the worktree, so scan output is never committed, and the rounds share the
item's token and wall budgets. The patcher can also
record a verdict with `KanbanVerdict` instead of a fix, which ends the rounds. The verifier checks
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

A crew runs 1 to 8 members, each 1 to 8 replicas, and every member must be a
worker profile. Set `"one_per_repo": true` on a crew (both built-in crews have
it) to refuse a start while one of its workers is live in the same repository.
The check, the worker cap and the spawns are one step under a lock, so two
starts fired together cannot both get in. A crew without it can be started
alongside itself.

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
| `belai agent knowledge [-index] [-json] [-trust-dir] [-provider P] [-model M] [NAME]` | the retrieval indexes: this project's `.vulnetix` output and, with NAME, that profile's listed documents, as counts and addresses. `-index` brings them up to date first, sending new text through the security classifier (see [Knowledge](knowledge.md)) |
| `belai agent status` | running workers and this project's board |
| `belai agent run NAME [-once] [-item K-…] [-stay \| -drain] [-trust-dir] [-provider P] [-model M]` | run a worker in the foreground. `-stay` and `-drain` contradict each other and together are refused. `agent start` also passes `-id`, `-crew`, `-detached` and `-max-workers` to the workers it launches; they are not for typing |
| `belai agent start NAME [-replicas N] \| -crew CREW [-max-workers N] [-stay \| -drain] [-trust-dir] [-provider P] [-model M]` | start detached workers. `-replicas` starts that many workers of one profile (1 to 8, default 1; any other number is refused) and is ignored with `-crew`, whose members set their own replicas; `-max-workers` replaces `agents.max_workers` for this start; `-drain` exits once nothing is left to claim even with a cron `schedule`; exactly one of NAME and `-crew` is required |
| `belai agent ps` | running and recently stopped workers |
| `belai agent logs ID [-f]` | a worker's log |
| `belai agent stop ID \| NAME \| -all` | stop workers; claims are released |
| `belai agent pause ID \| NAME` | finish the card in hand, then claim nothing until resumed |
| `belai agent resume ID \| NAME` | take cards again |
| `belai kanban add TITLE [-body TEXT \| -body-file FILE] [-list backlog\|review] [-label L] [-priority N] [-assignee NAME] [-depends K-…] [-project NAME] [-json]` | file an item. `-body-file` reads the details from a file and replaces `-body`; `-list` is `backlog` (default) or `review`, and anything else is refused; `-priority` runs -2 to 3; `-label` and `-depends` repeat. A card is not filed twice: when an unfinished card in the same project has the same title (compared ignoring case and extra spaces), the command prints that card's id marked `(already on the board)` and files nothing |
| `belai kanban list [-list L[,L…]] [-label L] [-assignee NAME] [-project all] [-limit N] [-json]` | list items. With no `-list` it shows every list but `done`. `-project` is `current` (default), `all` or a project name. `-limit` caps the rows at N (default 50) |
| `belai kanban show ID` | one item with its history |
| `belai kanban move ID LIST [-note TEXT]` | move an item |
| `belai kanban note ID TEXT` | add a note |
| `belai kanban release ID` | clear a claim |
| `belai kanban delete ID` | delete an item. The delete is a tombstone that syncs, so the website and other hosts drop it too. A claimed item is deleted as well, and the worker that held it finds its lease lost at the next renewal |
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
- **Files placed in a worktree are copied, not mounted.** A worker never sees the
  repository's path. The harness copies the files a profile lists under
  `.vulnetix/crews` in and, for write access, merges them back under a lock,
  sanitised and size-bounded, with no symlink on either side; see
  [Files placed in a worktree](#files-placed-in-a-worktree).
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
