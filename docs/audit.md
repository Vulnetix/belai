# Audit log

While sync is on, Belai keeps an audit log of what this host and its agents
did, and uploads it to the Vulnetix website. **Belai → History** shows it in
two tabs:

- **Hosts** is the audit of a machine: when it was first seen, each Belai
  version and hostname it has run under, when remote control came online or
  went offline, and every request the website made of it.
- **Agents** is the audit trail of the fleet's work: which agent claimed and
  released which card, the commit it left on the item's branch, each gate it was
  verified against, the pull request and the VEX it produced. Work that was
  about a vulnerability carries the advisory id, so it links to that
  vulnerability.

The **Sessions** tab is the existing session history and is unchanged. See
[session-sync.md](session-sync.md).

## Facts only

The session mirror carries content: prompts, replies, tool output and diffs.
The audit log carries none of that. An event names a kind and a few identifiers
the harness read itself, and nothing else can be recorded.

- **Identifiers and enums.** Every string is reduced to `[A-Za-z0-9._:/@+-]` and
  capped at 256 characters (`audit.Clean`). A commit id is kept only as
  lowercase hex of 40 or 64 digits, an advisory id only in its identifier shape,
  a verdict and an actor kind only from their enums.
- **A closed set of keys.** The event has a fixed set of fields
  (`TestEventKeysClosed`), and its `data` map accepts only the keys listed
  below (`TestDataKeysAllowlist`). A prompt, a reply, a command, tool output, a
  file's contents or a commit message has nowhere to go.
- **The harness stamps it.** The sequence number, time, hostname, scope and
  chain hash belong to the recorder. An emitter supplies a `Fact` and cannot set
  them, and a model's argument never reaches one.
- **Best effort.** With sync off no recorder exists and `audit.Emit` does
  nothing. A write error drops the event. The audit never fails or slows the
  work it records.

### Events

| Scope | Kind | Recorded when |
|---|---|---|
| host | `host.first_seen` | this machine reports for the first time |
| host | `host.version_changed` | the Belai version differs from the last one reported |
| host | `host.rc_online`, `host.rc_offline` | `belai rc` starts and stops |
| host | `host.dispatch` | the host answers a request from the website (`start`, `stop`, `worker`, `crew`, `pause`, `resume`, `profile_backup`, `profile_install`, `crew_backup`, `crew_install`, `avatar`, `item_backup`, `item_install`, `provider_keys_install`, `provider_keys_remove`, `library_sync`, `project_prefs`, `teleport_backup`, `teleport_code`) |
| host | `host.teleport` | this host opened a session teleported in from another (`belai -teleport`); the event names the new session, and `from` is the origin session |
| host | `host.schedule` | `belai rc` fires a stored schedule, skips or is refused a run, or turns a schedule off because it cannot accept it |
| agent | `worker.started`, `worker.state`, `worker.stopped` | a worker starts, is paused or resumed, and stops |
| agent | `card.claimed`, `card.released` | a worker claims a card and hands it back |
| agent | `card.lease_lapsed` | the harness reaps a claim whose lease ran out |
| agent | `repo.commit` | the tip of an item's work, or an auto-commit in the TUI |
| agent | `repo.publish` | a draft pull request is opened for the item's branch |
| agent | `gate.verified` | a gate, or a regression check, is decided by its exit code |
| agent | `vex.written` | the harness writes the VEX for a verdict |
| agent | `finding.carded`, `finding.reconciled` | the sweep files a finding card, or finds it gone or closed |

`data` keys: `attempt`, `change`, `commit`, `dispatch`, `ecosystem`, `exit`,
`files`, `forge`, `from`, `gate`, `hops`, `justification`, `list`, `os`,
`package`, `passes`, `path`, `pr`, `previous`, `profile`, `reason`,
`regressed`, `rule`, `schedule`, `severity`, `state`, `status`, `suite`, `to`,
`version`, `worker`. `schedule` is a schedule's UUID. Adding one is adding something that can leave the machine, so it
needs a line here and a change to `TestDataKeysAllowlist`.

A card links to a vulnerability (`vulnId`) only when it is a security card: one
the sweep labelled `vuln`, `gone` or `needs-verify`, or one a verifier has
recorded a verdict or VEX on (`kanban.Item.VulnID`). A failing-test or coverage
card also carries a finding id, but it is not vulnerability work and does not
link.

## Tamper evidence

Each process run (the TUI, a worker, the rc daemon) is one **stream**. It
appends to its own file, `~/.vulnetix/belai/sync/audit/<pid>.<stream-id>.jsonl`,
so there is no lock between processes. Each line is an event with:

- `seq`, its line index, which makes an upload idempotent;
- `prev`, the previous event's `hash` (empty for the first);
- `hash`, the SHA-256 of the event's canonical text (`audit.Canonical`).

Editing, dropping or reordering a line breaks every hash after it. The server
recomputes the chain on upload and stores an event that does not continue it
with the stream flagged, never dropping it, so the log records that a break
happened. The website's verify check also re-walks the stored rows, so a row
edited or removed in the database reads as broken at its `seq`.

The canonical text is pinned on both sides by a golden vector
(`internal/audit/audit_test.go` here, `belaiAuditGolden` in vdb-site), so a
change to either side fails both.

## Upload

The upload is the session mirror's machinery on a second stream
(`sessionsync.AuditSyncer`): it tails the stream file and sends each line by
line index, in batches of up to 100 events or 2 MiB, gzipped above 8 KiB, with
the same client, origin allowlist and credential as sessions.

1. `PUT /hosts/{host}/audit/{stream}` registers the stream and returns the
   server's cursor, so a restarted process sends only what is missing.
2. `POST` on the same path uploads events. A repeat is ignored.
3. A failure backs off and retries. The file is re-read, never rewritten.

A stream that uploaded completely is removed when its process exits. A crashed
or killed process leaves its file, named for its pid, and a later run finishes
it: a file whose pid is no longer running is uploaded from the server's cursor
and removed once the server holds all of it. A file whose process is alive is
never touched. A stream stops recording after 100,000 events.

## Settings

The audit log goes where session sync goes: it runs only with `sync.enabled`,
a usable Vulnetix CLI credential and an allowed origin. The project layer may
turn sync off, never on, so a repository cannot start it.
