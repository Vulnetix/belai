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

The daemon sends the list to the website, which offers only those. The daemon
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
  `belai -prompt`. Web answers are off too.
- Model and provider are the host's own settings. The website picks only the
  directory, the prompt and optionally the mode (agent, plan or goal).
- Web prompts sent while a turn runs wait their turn and show as queued.
- It ends after `--idle` (default 30 minutes) without a prompt, when you press
  **Stop session** on the website, or when remote control stops.
- **The command is the daemon's, not yours.** `belai rc-session` needs
  `-session-id` (the id the daemon minted for the session) and `-dispatch` (the
  website request it answers) and refuses to start without both, with `-mode`
  (`agent`, `plan` or `goal`, otherwise classified from the prompt) and `-idle`
  optional. Typing it yourself starts nothing the website knows about. The
  prompt is read from stdin, cleaned like a web prompt and capped, and an empty
  prompt is refused.

`belai rc --detach` starts the daemon in the background with
`BELAI_RC_DETACHED=1` in its environment. That variable is how the daemon knows
to keep a log file, `~/.vulnetix/belai/rc/rc.log`; set by hand it makes a
foreground daemon log there too.

At most `--max` sessions (default 3) run at once; the website refuses more.
An explicit `--max N` is also the fleet worker cap for this host: the daemon
advertises N instead of `agents.max_workers` and starts workers with
`belai agent start -max-workers N`. A repository whose settings lower
`agents.max_workers` still holds its own cap. Without `--max`,
`agents.max_workers` (default 4) applies.

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
  `refused` with its error.

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
  next run, so a crash can skip a run but never fire it twice. The outcome is one
  of `started`, `skipped_busy` (a worker of that profile is still working in that
  repository), `refused_cap` (the worker cap), `refused_disabled`
  (`agents.enabled` is off) or `error`. A refused run waits for the next tick.
- **What it refuses.** A schedule whose cron does not parse, whose directory is
  not offered or whose profile is not a worker profile in this host's catalogue
  is turned off with `refused_cron`, `refused_dir` or `refused_profile`, and the
  page shows the reason. The page checks the same things first for a quick answer,
  and the host checks again.
- **Audit.** Each firing and each refusal is a `host.schedule` event with the
  schedule's id, the profile and the outcome (see [audit.md](audit.md)).

## Agent library

The website keeps a library of agent profiles so a profile survives the machine
it came from. Two requests reach a profile on this host, both through the
dispatch queue and both carrying identifiers only:

- **`profile_backup`** names one of this host's profiles. The daemon exports it
  as markdown (the same form `belai agent import` reads, with its `id`, display
  name, palette and personality) and uploads it, and the website stores it as a
  new version. A backup writes nothing on this host. Built-in profiles ship with
  Belai, so a request for one is refused.
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

Both are `host.dispatch` audit events with the request kind and outcome (see
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
3. asks this host's **main** model, in a tool-less turn, to redraw Pix with them.
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
The drawing takes at most 100 seconds on the host.

The catalogue carries each worker profile's `id`, display name, palette and
avatar id, so the website can draw the agent. They are presentation only.

## Files

- `~/.vulnetix/belai/schedules.json`: the host's schedules and the sync cursor.
- `~/.vulnetix/belai/rc/rc.json`: the running daemon's record (pid, sessions,
  directories). `belai rc --status`, `--stop` and the TUI footer read it. The
  footer shows `● rc N` while the daemon runs.
- `~/.vulnetix/belai/rc/rc.log`: the daemon's log when detached.
- `~/.vulnetix/belai/rc/sessions/<id>.log`: each session's stderr.

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
- **Schema:** `BelaiDispatch`, the `rc*` columns on `BelaiHost` and
  `BelaiSession.dispatchUuid` (saas migration
  `20260930000001_add_belai_remote_control`); `rcWorkers`, `rcProfiles`,
  `rcMaxWorkers` and `BelaiDispatch.spec` (saas migration
  `20261001000001_belai_fleet_coordination`); `BelaiSchedule` (saas migration
  `20261004000001_add_belai_schedules`); `BelaiAgentProfile` and
  `BelaiAgentProfileVersion` (saas migration
  `20261004000002_add_belai_agent_library`).
- **Inbox scope:** each Belai polls its inbox for its own session only, so a
  TUI on the same machine never takes an rc session's prompts. A poll without
  a session (an older Belai) never claims a prompt for an rc session.
