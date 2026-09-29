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
`ctrl+y` opens the Sessions page, `s` stops remote control.

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

At most `--max` sessions (default 3) run at once; the website refuses more.
An explicit `--max N` is also the fleet worker cap for this host: the daemon
advertises N instead of `agents.max_workers` and starts workers with
`belai agent start -max-workers N`. A repository whose settings lower
`agents.max_workers` still holds its own cap. Without `--max`,
`agents.max_workers` (default 4) applies.

## Fleet workers

Remote control also tells the website about this host's
[fleet](fleet.md), so the Agent Coordination page can assign cards and
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
  ANSI, control and bidi runes removed) and clipped to 240 bytes, and all the
  tails together stay under 48 KiB. The log holds harness lines and the
  worker process's stderr, never a transcript: model output goes to the
  item's session. The server cleans it again and applies the same caps.
- **On the Sessions page.** Each machine running remote control lists its
  fleet workers above its sessions: a state indicator (working, starting,
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

## Files

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
- **Schema:** `BelaiDispatch`, the `rc*` columns on `BelaiHost` and
  `BelaiSession.dispatchUuid` (saas migration
  `20260930000001_add_belai_remote_control`); `rcWorkers`, `rcProfiles`,
  `rcMaxWorkers` and `BelaiDispatch.spec` (saas migration
  `20261001000001_belai_fleet_coordination`).
- **Inbox scope:** each Belai polls its inbox for its own session only, so a
  TUI on the same machine never takes an rc session's prompts. A poll without
  a session (an older Belai) never claims a prompt for an rc session.
