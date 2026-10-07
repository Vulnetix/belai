# Remote control

Remote control lets the Vulnetix website start Belai sessions on your
machine. You run `belai rc` (or `/rc` inside Belai) and the machine shows up
under **Belai → Sessions** at
<https://www.vulnetix.com/resolve/belai-sessions>. From there you pick a
directory, write a prompt and start a session. The page then follows it live,
and you can prompt it again or stop it.

```sh
belai rc                      # run in this terminal; Ctrl+C stops it
belai rc --detach             # run in the background
belai rc --dir ~/src/api      # also offer a directory
belai rc --web-controls       # web sessions take the TUI's session controls
belai rc --web-project-settings  # the website edits project preferences here
belai rc --web-shell          # web sessions run a shell line here, under the TUI's sandbox
belai rc --status
belai rc --stop
```

## What it needs

`belai rc` checks everything before it starts and prints one line per check,
each failure with the one thing to do about it:

| Check | Needs |
| --- | --- |
| Vulnetix CLI | `vulnetix` on your PATH. It is how you log in; a credential from the environment works without it, so a missing CLI is only a warning then. |
| Vulnetix login | A browser login: `vulnetix auth login`. An API-token login (`--token`, `VULNETIX_API_TOKEN`) is refused, because the website does not accept it for session sync. |
| Session sync | `sync.enabled` on (the default). With `sync.remote_prompts` off, sessions still start from the website but take no follow-up prompts. |
| Guardrails | On. Remote control never runs a session nobody is watching without them. |
| Directories | At least one directory to offer. |
| Vulnetix website | The website accepts the credential. |

`/rc` walks the same list as a setup screen: it explains what remote control
is, shows the checklist, and offers a step for each gap (install the CLI, log
in through the browser, turn sync on, offer this directory). Once everything
passes it starts the daemon in the background and shows its status. `o` or
`ctrl+y` opens the Hosts page, `s` stops remote control.

## Where sessions run

A session runs only in a directory the daemon offers:

- every project already trusted on this machine (the ones you opened `belai`
  in and trusted), except scratch directories under `/tmp`;
- every `--dir` you pass. Passing one is your consent: it is trusted exactly as
  `belai -trust-dir` would, the directory only, never the workspace
  directories its settings propose.

A directory inside a git repository is offered only when that repository's
root is trusted too, because a worker takes its trust from the repository
root. The daemon prints each directory it leaves out for that reason, with
the root to trust. `--dir` directories come first in the list, then the
directory you ran `belai rc` in, then the rest by path. The website starts a
worker in the first one when you do not pick a directory and the card names
no project the host offers.

`/trusted` in the TUI (also `f1` then `d`) lists every trusted directory and
marks the ones the running daemon offers, the missing ones and the scratch ones
it hides. The list scrolls with the cursor, and `/` filters it by path (or by
`missing`, `temporary`, `offered`). `x` revokes one, `X` revokes every
directory the filter shows, `p` revokes every missing or scratch directory, and
`a` trusts a path the same way `-trust-dir` does. Revoking clears the trust
flag only: the next launch there asks again, and a session the daemon starts
there fails its trust check. The daemon keeps listing it until you restart
remote control.

The daemon sends the list to the website, which offers only those. A directory
that is a checkout of a forge repository also carries its git facts: `remote`
(owner/repo), the forge `host` and `provider`, the checked-out `branch` and the
`defaultBranch` the clone recorded. They are read from the repository's own files
without running git, and only identifier-shaped values are sent, never a URL or a
credential. The website uses them to show the directory under the same repository
its scans, sandboxes and sessions name. The daemon
checks each request against its own list again (an exact match after
resolving symlinks, never a prefix), and the session checks trust once more
where it runs.

## How a session runs

Each session is its own process, `belai rc-session`, started in the chosen
directory with the prompt on stdin. It is an ordinary Belai session:

- It writes the same JSONL transcript as the TUI, so `belai -r <id>` resumes
  it later and it syncs to the website like any other.
- Its prompts go through the same admission as a typed prompt: cleaning, the
  prompt classifier, the posture and the permission rules.
- **Asks are off.** Nobody is at the terminal, so a tool call that would ask
  is decided by the posture and permission rules, exactly as for
  `belai -prompt`. Web answers are off too. With `--web-controls` the ask
  control decides instead ([below](#session-controls-from-the-web)).
- Model and provider are the host's own, resolved the way `belai agent start`
  resolves them: `provider` and `model` in settings, then the model last chosen
  in the TUI (`state.json`), then the built-in default. When `routing.kind` is
  `routed` the routing table picks the model per turn, as in the TUI. The
  website picks the directory, the prompt and optionally the mode (agent, plan,
  goal or code), and may name a provider, a model and an effort for that one
  session (see below). A model named on the web overrides routing for the
  session. In agent mode it may also name an agent profile the host offers; the
  session then works as that profile does in the TUI (its instructions and the
  tools it allows), through the same `ForceAgent` a typed `@profile` uses.
- **What the website can offer.** The daemon advertises the model a session gets
  by default, whether it is routed, and the providers this host holds
  credentials for with the models each lists (the compiled-in catalogue plus
  the model last chosen here). Names only, never a key or an endpoint. The New
  session dialog shows them; a request that names a provider the host has no
  credentials for is refused, by the website and again by the daemon. An older
  daemon advertises nothing, and the dialog then offers no model choice. The
  same advertisement lists the **agent profiles** a session can be engaged with
  (`rc.SessionAgents`: the flat profiles the TUI's picker offers, built-ins
  first, and the single-mode agent definitions they do not shadow), by name,
  never a prompt or a tool list. The dialog shows the picker in agent mode only.
  A request names a profile only in agent mode and only one the host offered,
  and the daemon checks both again; the name is passed to `belai rc-session
  -profile`, which loads the profile on this host, narrows the tools to its
  allowlist and engages it for agent-mode turns. An older daemon offers none.
- Web prompts sent while a turn runs wait their turn and show as queued.
- It ends after `--idle` (default 30 minutes) without a prompt, when you press
  **Stop session** on the website, or when remote control stops.
- **The command is the daemon's, not yours.** `belai rc-session` needs
  `-session-id` (the id the daemon minted for the session) and `-dispatch` (the
  website request it answers) and refuses to start without both, with `-mode`
  (`agent`, `plan`, `goal` or `code`, otherwise classified from the prompt), `-idle`,
  `-provider`, `-model` and `-effort`, `-profile` (an agent profile, with `-mode
  agent`), `-git-sync on|off` and `-add-dir DIR` (repeatable: an extra workspace
  directory, as `/add-dir`; the daemon passes each only when the web request
  named it, and only if the host offers it) optional. A session with extra
  directories confines its tools to all of them and sends the model a repo map
  of each. `-controls` and
  `-allow-guardrails-off` are the daemon's own `--web-controls` and
  `--web-allow-guardrails-off`, and `-shell` its `--web-shell`, passed on to
  every session it starts. Typing it yourself starts nothing the website knows about. The
  prompt is read from stdin, cleaned like a web prompt and capped, and an empty
  prompt is refused.

`belai rc --detach` starts the daemon in the background with
`BELAI_RC_DETACHED=1` in its environment. That variable is how the daemon knows
to keep a log file, `~/.vulnetix/belai/rc/rc.log`; set by hand it makes a
foreground daemon log there too.

At most `--max` sessions (default 3) run at once; the website refuses more.
`--max-workers N` sets the fleet worker cap for this host on its own: the
daemon advertises N instead of `agents.max_workers` and starts workers with
`belai agent start -max-workers N`. Use it to size sessions and workers
separately, for example `belai rc --max 1 --max-workers 20` on a hosted
sandbox. Without `--max-workers`, an explicit `--max N` is also the worker
cap, as before. A repository whose settings lower `agents.max_workers` still
holds its own cap. With neither flag, `agents.max_workers` (default 4)
applies.

## Session controls from the web

`belai rc --web-controls` lets a web session change itself the way the TUI
does, with the same slash commands and keys
([session-controls.md](session-controls.md)): the mode (`shift+tab`, `f5`),
the model (`/model`, `ctrl+q`), the effort (`f6`), guardrails (`f3`), ask
(`f4`), caveman (`f2`), auto-commit, the reasoning, tool-call, edit and
decision displays (`ctrl+r`, `ctrl+t`), the post-end tests, language servers,
and Jev jobs and thresholds. The session page shows the controls as a strip,
a line typed in the composer that starts with one of their commands is sent as
a control rather than a prompt, and the keys work while the page has focus.

- **For that session only.** A control changes the running session and is
  never written to a settings file. A change to the model, guardrails, ask,
  caveman, language servers or Jev rebuilds the agent session, keeping the
  conversation: at once between turns, or when the running turn ends.
- **Only the table.** The host parses each command against the fixed table in
  `internal/sessionctl` and checks every value: a model must be one this host
  holds credentials for, a Jev threshold is held to the settings rules, a job
  or language must be a known one. Anything else is refused with a reason, and
  a line that is not a control (`/help`, `!ls`) is never run as a control. A
  shell line has its own channel, and only with `--web-shell` (see
  [Shell lines from the web](#shell-lines-from-the-web)).
- **Guardrails stay on** unless you also pass `--web-allow-guardrails-off`.
  Turning them back on never needs the flag.
- **Ask.** With ask on (the setting's value at start), a tool call that would
  ask is put to the web session as a permission ask and waits for an answer,
  and the agent's questions are asked there too. Allow-always is remembered for
  that session only. An unanswered ask is denied after ten minutes. With ask
  off, calls are decided by the rules, the classifier and the sandbox without
  asking, as `f4` does in the TUI. Turning ask off while a permission is open
  allows that one call (allow once, recorded in the transcript as the host's
  answer) and dismisses an open questionnaire, instead of leaving the turn
  waiting for an answer that can no longer be given: web answers go off with
  ask. A call the running turn asks about after that is allowed the same way
  until the turn ends and the session is rebuilt with ask off.
- **What the transcript shows.** Every applied control writes a harness line
  (`web: caveman: on`), and the turn facts carry the current mode, model,
  guardrails, ask and caveman.
- **Language servers** start only when the web turns them on, in the directory
  the host trusts, with the same scrubbed environment, process group and
  refused edits as in the TUI. They are closed when the session is rebuilt or
  ends.
- **Auto-commit and tests** run after a turn when their control is on: the
  commit covers the files a completed goal changed, and the test pass runs
  under the session's gates.
- **Fleet workers.** The workers the daemon starts (from the Board, the Crews
  tab or a schedule) take `/model`, `/effort`, `/guardrails` and `/caveman` too,
  each from the worker's next turn; the rest are refused for a worker, with a
  reason. The Crews tab sends such a line to every chosen worker of a crew. See
  [fleet.md](fleet.md#session-controls).

## Slash commands from the web

A host that holds [custom slash commands](library-items.md#slash-commands) lists them in
its advertisement as `commands: [{name, description, argumentHint}]` (names, descriptions and
hints only, never a template, and only while `sync.commands` is on; an empty list when it
holds none, and absent from a daemon that predates the kind). The session page offers them
in a picker and sends the choice as a remote command `{command, args}`: `command` is the
name without the slash and `args` the argument text. The command channel is the one session
controls use, so a session takes them when it runs with `belai rc --web-controls`.

- **The host expands its own file.** It resolves the name against the global commands
  directory and the project layer of the session's directory (project wins), expands the
  template with `$ARGUMENTS` and `$1`, `$2`, ..., and runs a normal model turn on the
  result, queued behind a running turn like any prompt. The page never sends a template.
- **Refusals.** A name this host does not hold, a name that is not a command name, arguments
  over 4 KiB, `sync.commands` off, `sync.remote_prompts` off, or a command mixed with a
  control line, key or shell line is refused with a reason. A fleet worker takes no
  commands.
- **Admission.** The expansion goes through the same sanitiser and classifier as any web
  prompt, and the transcript shows a harness line (`web: /triage`) and a user turn whose
  metadata names the command.
- **A prompt that starts with `/` is still plain text.** Only the structured form runs a
  command.

## Shell lines from the web

`belai rc --web-shell` lets a web session run one shell line at a time on this
host. The Pix Sandbox starts `belai rc` with it. A prompt is still never a shell
command: a prompt that starts with `!` is plain prompt text. A shell line is a
request on the same inbox as a control, with its own fields (`shell`, `cwd`,
`attach`), so the host can tell the two apart and a host without the flag
refuses it.

Two surfaces send one, and they differ only in what happens to the output:

| Surface | `attach` | Output |
|---------|----------|--------|
| The composer's `!cmd` | set | Shown in the transcript. When the classifier calls it safe it is attached to the model's next turn, as the TUI attaches a `!cmd`. |
| The console drawer's remote shell | never | Shown in the console and the transcript. Never offered to the model, whatever the verdict. |

- **Same rules as the TUI.** A line is refused in plan mode unless the
  read-only gate allows it, and by a deny rule, or by an ask rule since nobody
  can answer one on the host. With no matching rule it runs, as a typed `!cmd`
  does.
- **Same sandbox.** The line runs through the Bash tool under the OS sandbox
  profile from `sandbox` in settings (bubblewrap on Linux), with a 10 minute
  limit (the Bash tool's ceiling; a line that hits it exits 124). The page shows
  the output as it is produced, so a long build or a sign-in that waits for a
  browser is usable. Each line is a fresh process, so a `cd` is resolved by the host
  (`cd`, `cd PATH` and nothing else) and the page keeps the directory.
- **Confined to the session.** The directory must be the session's directory
  or below it, after symlinks. Anything else is refused.
- **Out of band.** A line runs while a turn is running and does not wait for
  it. It counts as activity, so a console left open keeps the session from
  idling out.
- **Classified.** The output is sanitised and classified like the TUI's
  `!cmd`. Only an `attach` line with a safe verdict is attached, once, to the
  next turn. The verdict is in the entry either way.
- **What the transcript shows.** Three entry types per line, all keyed by
  `shell_id` (the request id, how the page finds them):
  - `shell_run`, when the process starts: `command`, `cwd`, `source`
    (`composer` or `console`) and `attach` in `meta`.
  - `shell_out`, while it runs: a slice of the output as content and `n`, its
    order. Slices are batched (about every 300 ms or 8 KiB) and stop after
    256 KiB. A resumed TUI session ignores them.
  - `shell`, when it ends: the output as content and `command`, `shell_id`,
    `status`, `exit_code`, `duration_ms`, `cwd`, `source`, `attached`,
    `analysing` and `verdict` in `meta`. The request is acked after this entry
    is written: accepted when the line ran, refused with a reason when it did
    not (a refused line writes no `shell_run`).
- **A failed composer line is analysed.** When an `attach` line exits non-zero,
  or could not finish, the host raises a turn of its own (a `user` entry with
  `source: shell`): the exit status, the attached output when the classifier
  cleared it (withheld, with the verdict, when it did not), and an instruction
  to find the cause, fix it with the session's tools where permissions allow,
  or say what only the user can do. The console's lines never raise a turn.

## Project settings from the web

`belai rc --web-project-settings` advertises each offered directory's project
preferences and lets the console's project settings page change them. The page
lists every key with its resolved value and the settings layer it came from,
so you can see when a repository's settings or your global settings win.

- **Host-private.** Edits go to the host's preference file for the directory
  (`~/.vulnetix/belai/projectprefs/`), the same file the TUI's `f2`, `f3`
  and `f4` write. The repository is never touched.
- **Fixed keys.** The page can set guardrails, ask, the firewall switch,
  caveman, the starting mode, the displays, auto-commit, the post-end test
  trigger and fail branch, language servers (all, or per language) and Jev
  jobs and thresholds. A key outside that list is refused; values are booleans,
  fixed words or numbers between 0 and 1, and the result must pass the settings
  validators before it is written.
- **The next session.** A running session keeps its settings; sessions started
  afterwards, in the TUI or from the web, read the new preferences. A remote
  session still runs with guardrails on unless the host passed
  `--web-allow-guardrails-off`, whatever the preference says.

## Fleet workers

Remote control also tells the website about this host's
[fleet](fleet.md), so the Board page can assign cards and
predict what will happen to them:

- **Catalogue.** The host upsert's `rc` block carries `agents.max_workers`
  and every worker profile's routing (lists, labels, `on_success`,
  `on_failure`, handoffs, attempts, lease and budget) and every crew with its
  members. Never a system prompt, a tool list or a model. The daemon sends it
  again whenever a profile or crew changes.
- **Workers.** Each heartbeat carries the fleet registry records: every live
  worker, then those that stopped or failed in the last 15 minutes, newest
  first, at most 64. Each has its id, profile, crew, state, the item held,
  its transcript session and branch, done and failed counts, start and last
  heartbeat times, and for an ended worker when and why it stopped
  (`done: nothing left to claim for 1m0s`, `stopped`, `the worker process
  exited without stopping`, a startup error). It also carries the last 12
  lines of the worker's own log, startup included. The daemon reads that log
  by the worker's id from the fleet log directory, never from a path in the
  record. Each line is cleaned the way a web prompt is (delimiter markup,
  ANSI, control and bidi runes removed) and clipped to 240 bytes on a character
  boundary, with `…` marking the cut, and all the
  tails together stay under 48 KiB. The log holds harness lines and the
  worker process's stderr, never a transcript: model output goes to the
  item's session. The server cleans it again and applies the same caps.
- **Pause and resume.** The website can ask a live worker to pause or resume
  with a `pause` or `resume` request that names the worker's id. The daemon
  refuses an id that does not look like one, and a worker that is not live in
  this host's own fleet registry. Otherwise it sets or clears the worker's
  pause marker and acknowledges. The worker finishes the card it holds, then
  reports `paused` until resumed (see [fleet.md](fleet.md#pausing-a-worker)).
- **Crew messages.** A fleet worker's session takes web prompts when
  `sync.remote_prompts` is on, the same switch as any session. The Agents page
  sends a crew message as one prompt per live worker (no new request kind, so
  an older server needs no change). The worker steers it into the running turn
  or holds it for the next one (see [fleet.md](fleet.md#transcripts)). With the
  switch off the worker takes nothing from the web and acks nothing.
- **On the Hosts page.** Each host running remote control lists its
  fleet workers above its sessions: a state indicator (working, starting, paused,
  idle and looking for work, stopped, failed), the profile and crew, done and
  failed counts, and when it started or ended. Opening one shows the exit
  reason, the branch, a link to its last session and the log tail. A crew
  that went idle straight after it was sent reads `looking for …` then
  `nothing to claim: no …`, which says what the board lacked. Only live
  workers count against `agents.max_workers`, on the page and when it asks a
  host to start more. An older Belai sends no log, and the page says to run
  `belai agent logs ID` there instead.
- **Starting workers.** A `worker` or `crew` request starts one profile or
  one crew in an offered directory. The daemon checks the directory against
  its own list and the name against its own catalogue, then runs
  `belai agent start NAME` or `belai agent start -crew NAME` there, which
  applies the trust check, the preflight and the worker cap
  (`agents.max_workers`, or an explicit `--max`) as it does in a terminal. The ack is `started` with the command's report, or
  `refused` with its error. A request may name a `provider` and a `model`; the
  daemon checks them as it does a session's (a provider this host holds
  credentials for, values that cannot read as flags) and passes them as
  `belai agent start -provider P -model M`. Left out, the workers run on the
  host's own default, which is why the website starts a Pix sandbox's workers on
  that sandbox's own default (its launch config's model, else the first provider
  its vault grants, else Pix Smart) rather than on an OpenAI default it holds no
  key for. A `crew` request with `fill` set runs
  `belai agent start -crew NAME -fill` instead, which starts only the replicas the
  crew lacks in that directory (the Crews tab's "Start the missing worker").

## Scheduled agents

While `belai rc` runs it fires the host's stored schedules. A schedule is a
worker profile, a cron expression (five fields, or `@hourly`, `@daily`,
`@weekly`, `@monthly`) and one of the directories this host offers, plus an
on/off switch. The Hosts page lists each host's schedules and lets you add,
edit, pause and delete them; a change reaches the host within about 30 seconds.

- **Sync.** Schedules mirror to the website the way the kanban board does: the
  host keeps the durable copy in `schedules.json`, each side's change carries a
  version, and the host pulls everything after its cursor. The definition
  (profile, cron, directory, on/off) is last-writer-wins. The run record (when
  it last ran, how that went, when it runs next) is the host's alone: a pull
  never replaces it, so an edit on the page cannot lose a run. A delete is a
  tombstone that reaches every host.
- **The ticker.** Every 30 seconds the daemon fires each enabled schedule that
  is due, then syncs. Firing comes first, so an unreachable website never delays
  a run. Times are the host's local time. A schedule fires at most once per cron
  tick, and a run that came due while `belai rc` was not running is skipped, not
  fired late.
- **What a schedule can start.** Exactly what a `worker` request can: the
  daemon matches the directory against its own list and the profile against its
  own catalogue, then runs `belai agent start -drain NAME` there, which applies
  the trust check, the preflight and the worker cap. `-drain` makes the worker
  exit once nothing is left to claim, even when the profile has its own `schedule`,
  because the stored schedule is what starts it. The website supplies no prompt,
  model, posture or permission.
- **What it records.** Before it fires, the daemon writes the run time and the
  next run, so a crash can skip a run but never fire it twice. The immediate start
  outcome is one of `started`, `skipped_busy` (a worker of that profile is still
  working in that repository), `refused_cap` (the worker cap), `refused_disabled`
  (`agents.enabled` is off) or `error`. Once the worker stops, the daemon updates
  a `started` run to `worked` (it finished items), `drained` (nothing was left to
  claim) or `worker_failed` (it stopped with an error). A refused run waits for
  the next tick.
- **What it refuses.** A schedule whose cron does not parse, whose directory is
  not offered or whose profile is not a worker profile in this host's catalogue
  is turned off with `refused_cron`, `refused_dir` or `refused_profile`, and the
  page shows the reason. The page checks the same things first for a quick answer,
  and the host checks again.
- **Audit.** Each firing and each refusal is a `host.schedule` event with the
  schedule's id, the profile and the outcome (see [audit.md](audit.md)).

## Agent library

The website keeps a library of agent profiles and crews so they survive the
machine they came from. Four requests reach them on this host, all through the
dispatch queue and all carrying identifiers only: `profile_backup` and
`profile_install` for an agent, `crew_backup` and `crew_install` for a crew
(see [Crews](#crews) below). Two more, `teleport_backup` and `teleport_code`, are not
a person's action: the backend sends them to the origin host of a `belai
-teleport`, so the library holds the session's profile and the target gets its
uncommitted and unpushed code (see [teleport.md](teleport.md)):

- **`profile_backup`** names one of this host's profiles. The daemon exports it
  as markdown (the same form `belai agent import` reads, with its `id`, display
  name, palette and personality) and uploads it, and the website stores it as a
  new version. A backup writes nothing on this host. Built-in profiles ship with
  Belai, so a request for one is refused. The files the profile names go
  with it (see [Files an agent carries](#files-an-agent-carries)).

- **`teleport_backup`** names the profile a teleported session ran under. The
  daemon backs that profile up, then every crew on this host that lists it and
  those crews' members, through the same uploads as `profile_backup` and
  `crew_backup` while the request is delivered to this host. The crews and
  members come from this host's own definitions, never from the request, at most
  24 uploads. It writes nothing here. A crew or member that cannot be backed up
  is reported and does not stop the profile's backup, which is the part the
  teleport needs. Built-in profiles and crews are not uploaded.

- **`teleport_code`** names a teleport, the session's directory, whether the
  target's user agreed to a branch (`push`) and whether the forge is not to be
  tried (`replay`). The daemon re-checks the directory against its own list,
  reads the working tree with hardened git (nothing in the checkout, its index or
  its refs changes), and answers in the background so a model's hand-over never
  holds the queue: a `belai/teleport/<id>` branch when `teleport.push` and the
  user's agreement allow one, otherwise the patch with a summary and per-file
  instructions from the fast model's `teleport_distill` role. Credential files,
  binaries, large files, links and ignored files never leave. See the Code section
  of [teleport.md](teleport.md#code).

- **`profile_install`** names a library profile and one of its versions, and
  whether it may replace a profile here. The daemon reads that version from the
  library, and the server serves that one version only while the request is
  delivered to this host. An install is a person's action on the website, made
  with their own login, so it is not held to the rules a web prompt is: it does
  not need `sync.remote_prompts`, and a profile may be as permissive as the user
  chooses (guardrails off, autonomous, Bash). The profile is parsed strictly and
  validated whole like `belai agent import`, and written only if all of this
  holds:
  - it is a valid profile (an unknown key or tool, a missing field or a bad
    palette is refused, as import refuses it);
  - its `id` is the library profile's;
  - it does not take a built-in's name;
  - no profile of that name exists here, or the request set replace and that
    profile has the same `id`. A profile that holds the same `id` under another
    name is never replaced, and a display name another profile here holds is
    refused.

  The acknowledgement says what was installed, or the reason it was refused:
  harness words with a short cleaned excerpt, never the profile.
  Replacing a profile stops a worker running on the old one, because a worker
  pins its definition (see [fleet.md](fleet.md)); restart it to use the new one.

  A profile's `knowledge` and `workspace.sync` blocks are part of it: a backup
  carries them, an install keeps them, and a replace takes the library copy, or
  keeps the entries already on this host when that copy lists none. What the
  paths name is indexed, classified and copied on the host that runs the agent,
  under a fixed floor that holds whoever wrote the profile (see
  [knowledge.md](knowledge.md#profile-knowledge) and
  [fleet.md](fleet.md#files-placed-in-a-worktree)). The files themselves travel
  too, so retrieval works for an installed profile as it does for one written
  here: see below.

### Files an agent carries

A profile names files in two places: `knowledge.paths` (documents the agent may
search) and `workspace.sync` (files a crew shares). They used to be only paths, so
an agent restored on another host came back without the files it was written
around. They now travel with the profile, under the same floor as the index
(see [knowledge.md](knowledge.md#a-profiles-own-files)):

- **A backup captures them.** The daemon reads each listed path where this host
  has it: a relative path under each directory this daemon offers, then the home
  and absolute forms, then the profile's own files (below). It leaves out
  symlinks, anything under `.git`, credential files and key stores, files that
  hold a private key or a known token, binary files and anything past the limits
  (256 KiB a file, 32 files and 2 MiB a profile). Each file is uploaded under its
  SHA-256, and the profile is saved as a version that lists them. The
  acknowledgement counts what was left out and why, never a name or any text.
  A host that has none of the files keeps the library's copy of them: a backup
  never empties what another host saved.
- **An install writes them, and indexes them.** The daemon reads the version's
  files, checks that each one is bounded text that hashes to its listed hash, and
  writes them into `~/.vulnetix/belai/profiles/files/<profile id>/`, never into a
  repository, then saves the profile and indexes its documents through the same
  classifier as `belai agent knowledge -index`, from a trusted directory this
  daemon offers. A path that climbs out of that directory, a file that is not
  text or does not match its hash refuses the install before anything is saved.
  When no trusted directory is offered the documents are indexed the first time
  the agent runs.
- **The profile's paths resolve to them as a fallback.** A listed path that does
  not exist on this host resolves to the owned copy, so a project's own file of
  the same name always wins. A `workspace.sync` entry the repository does not
  hold yet is seeded from it.

You can attach files to an agent here too: `belai agent files add NAME FILE`,
`rm`, `adopt` and the listing (see [fleet.md](fleet.md#command-line)).

### Crews

A crew is a name, a description and its members (see [fleet.md](fleet.md)). It has
an `id` of its own, which `belai rc` stamps on a crew that has none, so a rename
does not make it another crew.

- **`crew_backup`** names one of this host's crews. The daemon exports it as JSON
  and uploads it, and the website stores it as a new version. Built-in crews ship
  with Belai and are refused.
- **`crew_install`** names a library crew and one of its versions. The website
  resolves each member to the library profile it names and refuses the request
  when one is missing, naming it. The daemon installs the members this host lacks
  first, each exactly as a `profile_install` (files and index included), leaves a
  member it already has under the same `id` alone unless the request says replace,
  and refuses the whole install when a member of that name here is a different
  profile. The crew is written only after every member is a worker profile on this
  host. A crew of the same name is replaced only with replace set and the same
  `id`.

A crew's definition is a few hundred bytes of JSON with no files, so the website
can also build one in the crew editor and save it to the library; `belai agent crew
import`, `export` and `delete` do the same by hand.

### Automatic sync of profiles and crews

With `sync.profiles` on (the default, and only while `sync.enabled` is) the daemon
also keeps the library current without a request. Every 30 seconds it hashes each
stored profile and crew and asks the server what to do with the ones it has not
settled yet:

| Answer | Meaning |
|---|---|
| `push` | the library has nothing newer, so the host pushes its copy as a new version |
| `current` | the library already holds this copy |
| `diverged` | the website saved a version this host has not installed, so the host's copy never overwrites it; back it up or install it from the website to settle it |
| `skip` | deleted from the library, or not this account's |

Only the profile markdown and the crew JSON travel this way, never file contents:
those leave the host only for a `profile_backup` request, so a person always
chooses to upload them. The server decides again when the host pushes, so a web
edit saved in between is never lost, and a host with no record of an item that
differs from the library is `diverged`, never overwritten. The Agents page shows
which agents have no files in their library copy. A diverged item is logged once;
the host asks again after ten minutes, in case it was resolved. Set
`sync.profiles` to `false` to leave backups to requests from the website.

The same check carries the host's [library items](#library-items) (one switch per
kind), in the same request, with the same answers and the same ten-minute rule for
`diverged` and `skip`.

### Library items

The library also keeps the other documents a host holds. Two more requests reach
them, through the same queue and carrying identifiers only (see
[library-items.md](library-items.md) for the formats, limits and rules):

- **`item_backup`** names a kind (`itemKind`, because `kind` already names the
  request) and one of this host's items by `name`. The daemon exports it as its
  canonical document and uploads it as a new version. A backup writes nothing here.
- **`item_install`** names a library item (`library`), a version, a kind and
  whether it may replace the host's item of the same name. The daemon fetches that
  version while the request is delivered to this host and writes it only if the
  document validates whole, carries the name the library item has, and either no
  such item exists here or the request set replace. A skill, a prompt or a command must also
  pass the sanitisation gate. Like a profile install it is a person's action and
  does not need `sync.remote_prompts`.

Both are refused, before the library is asked for anything, while the kind's own
switch is off (`sync.skills`, `sync.prompts`, `sync.commands`, `sync.processes`), and a kind this Belai predates is
refused with "update Belai on the host". The host also reports which items it holds
in its advertisement (`rc.items`: kind, name and hash, never a document) for the
kinds whose switch is on, and advertises again when they change.

While `sync.profiles` is on, each worker profile and crew in the advertisement also
carries `sha256`: the hash of the profile markdown or crew JSON exactly as the
library stores it (the same bytes a backup uploads and the automatic sync hashes),
so the website can tell which library version the host holds and whether it was
edited there. It is a hash only, never the text. A built-in or plugin profile, a
built-in crew, a profile or crew without an id or over the library size limit, and
any of them with `sync.profiles` off carry none, and an older daemon sends none, so
the website reads the version as not reported rather than current.

What the hash is, exactly: the library stores a profile's markdown as it was given,
so a version written in the website's editor is stored as the editor wrote it, while
Belai renders a profile back in its own key order and JSON escaping (Go writes `<`,
`>` and `&` inside a string as unicode escapes, which JavaScript's `JSON.stringify`
does not, and the editor puts `knowledge` before `kanban` where Belai puts it last).
The two differ for such a version,
so when the daemon installs a library profile it keeps, in
`~/.vulnetix/belai/rc/library-hashes.json`, the hash of the exact bytes the library
held next to the hash of what it rendered. While the profile still renders the same,
the advertisement and the sync report the library's hash, so an unedited install
reads as that library version and the server answers `current`, not `push`. Once the
profile is edited they report the hash of what is there, which no library version
has, so the website reads it as edited on the host. A profile with no record (written
by hand, backed up from this host, installed by an older Belai) reports the hash of
its render, which is the bytes a push stores. A crew needs no record: the library
stores a crew in Belai's canonical JSON. The golden vectors in
`internal/rc/golden_hash_test.go` pin the bytes and digests of a profile and a crew,
and vdb-site's TestBelaiGoldenLibraryHashes (`belai_golden_hash_test.go`) holds the same literals.

A **`library_sync`** request carries nothing but its id. The daemon runs one pass of
the automatic sync above at once, asking about every profile, crew and item whose
switch is on, settled or not, and acknowledges with counts only ("2 pushed, 14
already kept"), never a name. With every switch off it is refused and says so.

Every request in this section is a `host.dispatch` audit event with the request kind and outcome (see
[audit.md](audit.md)).

### Avatars

The website's agent builder can give an agent a customised Pix. The website runs
no model, so it asks a connected host with an `avatar` request that carries one
identifier, the id of the agent creator the website made. The daemon:

1. refuses unless `sync.remote_prompts` is on, and draws one avatar at a time (a
   second request meanwhile is refused with the reason). The slot is free as soon as
   the drawing is done, before the website is told, so a request sent right after
   an acknowledgement is never refused for a drawing that has finished;
2. reads the display name, the four colours and the optional personality from
   the website, cleans every string to one capped line, and sends the text through
   the security classifier under the effective posture, as it would a web prompt;
3. asks this host's **main** model, in a tool-less turn, to redraw Pix with them
   (and its fast model alongside it, as a stand-in; see below).
   The system text is Belai's own and carries the drawing and the rules; the
   website's words arrive only as labelled data;
4. admits the reply through `internal/svgguard` and posts back only the
   guard's own serialisation of it, or the reason it did not draw one.

The guard admits shapes, paths, gradients, clip paths and a blur filter, keeps
Pix's viewBox, and refuses the whole image for anything else: a script, style,
text, link, image, `use`, `foreignObject` or animation element, an event
attribute, a reference other than `url(#id)` to an id the image defines, an
entity, a comment or a size over 48 KB. Nothing is repaired. A refusal goes back
as a harness-worded reason, never model or provider text, and the model is told
only which kind of problem the guard found when it is asked to try once more.
The server and the website admit the image again before they serve or draw it.
When a fast model is configured, it draws at the same time and its drawing is
kept in reserve: the main model's is always preferred, and the fast model's
stands in only if the main model has failed, or has not answered within 100
seconds and the fast model has. The drawing takes at most 240 seconds on the host.

The catalogue carries each worker profile's `id`, display name, palette and
avatar id, so the website can draw the agent. They are presentation only.

### What the advertisement and library sync hold

| Rule | Tests |
| --- | --- |
| A `library_sync` request runs one pass of the automatic sync at once, asking about every profile, crew and item whose switch is on, including ones a recent answer settled, and acknowledges with counts only, never a name | `TestLibrarySyncNowAsksAboutSettledItemsAndReportsCountsOnly` |
| With every sync switch off a `library_sync` request is refused and says so; a website that does not know the sync routes is reported in plain words | `TestLibrarySyncNowRefusesWithEverySwitchOffAndSaysWhyOnError` |
| An offered directory carries `remote`, `host`, `provider`, `branch` and `defaultBranch` only when they have an identifier shape, read from the repository's files; a remote with credentials keeps none of them | `TestDirGitCarriesIdentifierFactsAndNoCredential` |
| A directory that is not a checkout, a local-path origin, a detached HEAD and an odd branch name add nothing; the scp form of a remote is read | `TestDirGitLeavesOutWhatIsNotAForgeCheckout` |
| A worker profile and a crew in the advertisement carry the hash of the document the library would store, built from the same bytes the automatic sync hashes, and none while `sync.profiles` is off or for a built-in | `TestInventoryCarriesTheHashOfAStoredProfileAndCrew`, `TestInventoryHashFollowsTheStoredBytes`, `TestInventoryHashIsOmittedWithProfileSyncOff`, `TestInventoryRendersNoProfileWithProfileSyncOff` |
| An installed library profile reports the library's hash for the bytes it installed while it is unedited, and the hash of what is there after an edit; the sync asks the server with the same hash; the render of a console-written agent is not the library's bytes | `TestARenderOfAConsoleAgentIsNotTheBytesTheLibraryHolds`, `TestAnInstalledAgentReportsTheLibraryHashUntilItIsEdited`, `TestSyncAsksWithTheLibraryHashForAnUneditedInstall`, `TestNoRecordIsKeptWhenTheRenderIsTheLibraryBytes` |
| A fixed profile and crew render to the golden bytes and digests that vdb-site holds as the same literals | `TestGoldenProfileRenderAndHash`, `TestGoldenCrewRenderAndHash` |
| A web command expands the host's own installed file with its arguments and runs it as a turn; the project layer wins, an unknown or malformed name, `sync.commands` off, oversized arguments and a mixed command are refused, and a web prompt that starts with `/` stays plain text | `TestWebCommandExpandsTheHostsOwnFile`, `TestWebCommandProjectLayerWinsAndIsReadAtInvocation`, `TestWebCommandRefusals`, `TestWebPromptStartingWithASlashIsStillPlainText` |
| The advertisement lists commands by name, description and argument hint only, never a template, and changes the catalogue hash when a command is added or `sync.commands` goes off | `TestInventoryAdvertisesCommandsWithoutTemplates`, `TestSlashCommandWireForm` |
| The knowledge catalogue carries each document's address, size, SHA-256, labels and topics, never text or a source path, and an unchanged index file is not loaded again | `TestKnowledgeCatalogueCarriesFactsAndNoText`, `TestKnowledgeDocsKeepOnlyHarnessShapes` |

## Files

- `~/.vulnetix/belai/schedules.json`: the host's schedules and the sync cursor.
- `~/.vulnetix/belai/rc/rc.json`: the running daemon's record (pid, sessions,
  directories). `belai rc --status`, `--stop` and the TUI footer read it. The
  footer shows `● rc N` while the daemon runs.
- `~/.vulnetix/belai/rc/rc.log`: the daemon's log when detached.
- `~/.vulnetix/belai/rc/sessions/<id>.log`: each session's stderr.
- `~/.vulnetix/belai/profiles/files/<profile id>/`: the files a library install
  wrote for an agent (`rel/`, `home/` and `abs/` hold the three forms of listed
  path).
- `~/.vulnetix/belai/profiles/crews/<name>.json`: a user crew.
- `~/.vulnetix/belai/skills/<name>/SKILL.md`, `~/.vulnetix/belai/prompts/`,
  `~/.vulnetix/belai/processes/` and `~/.vulnetix/belai/library/prompts.json`: what an item install writes (see
  [library-items.md](library-items.md)).

## Server side

- **API:** `vdb-site` `api/internal/handler/belai_rc.go`.
  - Host: the host upsert carries the `rc` block, `POST /hosts/{id}/rc/heartbeat`,
    `POST /hosts/{id}/rc/offline`, the long-poll `GET /hosts/{id}/dispatch`
    and `POST /dispatches/{id}/ack`.
  - Browser: `GET /hosts`, `POST /hosts/{id}/dispatches`,
    `GET|DELETE /dispatches/{id}` and `POST /sessions/{id}/stop`.
  - Schedules (`belai_schedules.go`): the host pulls with
    `GET /hosts/{id}/schedules?since=` and pushes with `PUT /hosts/{id}/schedules`;
    the page uses `GET|POST /hosts/{id}/schedules`,
    `PATCH|DELETE /hosts/{id}/schedules/{sid}` and `GET /schedules` (every host).
  - Library (`belai_library.go`): the host answers a backup with
    `POST /hosts/{id}/library/backups` and an install with
    `GET /hosts/{id}/library/profiles/{profile}/versions/{version}?dispatch=`.
    The page lists, saves and rewinds under `/library/profiles` and checks a
    display name with `/library/names/check`. A version is one write-once S3
    object under `belai/{tenant}/agents/{profile}/{YYYYMMDDHHMM}.md`, indexed in
    Postgres; a rewind saves an old version as the new latest one.
  - Files (`belai_library_files.go`): `PUT|GET /library/files/{sha256}` for the
    page; a host uploads with `PUT /hosts/{id}/library/files/{sha256}` while a
    `profile_backup` is delivered to it and reads with the same route while a
    `profile_install` or `crew_install` names a version that carries the file.
    A file is one write-once object under
    `belai/{tenant}/agent-files/{sha256}`, shared by every version that holds the
    same bytes; a version lists its files in `BelaiAgentProfileFile`.
  - Crews (`belai_crews.go`): the same shape under `/library/crews`, a host
    answers with `POST /hosts/{id}/library/crew-backups` and
    `GET /hosts/{id}/library/crews/{crew}/versions/{version}?dispatch=`; a version
    is `belai/{tenant}/crews/{crew}/{YYYYMMDDHHMM}.json`.
  - Automatic sync (`belai_library_sync.go`): `POST /hosts/{id}/library/sync`
    answers push, current, diverged or skip per item, and
    `PUT /hosts/{id}/library/sync/profiles|crews/{id}` takes a push the server
    decides on again. `BelaiHostLibrarySync` remembers the version each host last
    held.
  - Items (`belai_library_items*.go`): the same shape under `/library/items/{kind}`,
    a host answers with `POST /hosts/{id}/library/item-backups` and
    `GET /hosts/{id}/library/items/{kind}/{item}/versions/{version}?dispatch=`, and
    the automatic sync carries `items` in `POST /hosts/{id}/library/sync` and a
    push on `PUT /hosts/{id}/library/sync/items`. The host's advertisement carries
    `rc.items`. See [library-items.md](library-items.md#server-side).
- **Schema:** `BelaiDispatch`, the `rc*` columns on `BelaiHost` and
  `BelaiSession.dispatchUuid` (saas migration
  `20260930000001_add_belai_remote_control`); `rcWorkers`, `rcProfiles`,
  `rcMaxWorkers` and `BelaiDispatch.spec` (saas migration
  `20261001000001_belai_fleet_coordination`); `BelaiSchedule` (saas migration
  `20261004000001_add_belai_schedules`); `BelaiAgentProfile` and
  `BelaiAgentProfileVersion` (saas migration
  `20261004000002_add_belai_agent_library`); `BelaiAgentFileBlob`,
  `BelaiAgentProfileFile`, `BelaiCrew`, `BelaiCrewVersion` and
  `BelaiHostLibrarySync` (saas migration
  `20261005000001_add_belai_agent_files_and_crews`).
- **Inbox scope:** each Belai polls its inbox for its own session only, so a
  TUI on the same machine never takes an rc session's prompts. A poll without
  a session (an older Belai) never claims a prompt for an rc session.
