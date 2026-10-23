# A2A messaging

Agents running in parallel on the same repositories (TUI sessions, fleet
workers, `belai rc` and ACP runs) used to know nothing of each other. They
duplicated work, fought over files and opened conflicting pull requests. A2A
messaging lets them coordinate:

- **Broadcasts.** The harness, not a model, broadcasts what each session is
  working on: the repository, branch, files, pull request, ticket and the
  agent's identity. Every session on the host receives them; with a Vulnetix
  login, every session in the organisation does.
- **Filtered receipt.** By default a session only hears broadcasts about its
  own repository. On a match the harness tells the model what is going on and
  offers it options.
- **Direct messages.** `A2AMessage` sends a question to another agent. The
  receiver answers in a side-turn without being interrupted, and the reply
  comes back asynchronously, tied to the question.
- **Repo memory.** `RepoMemoryRead` and `RepoMemoryWrite` keep the agreements
  agents reach, per repository, on both ends of an exchange.
- **Records.** Every A2A item is in the transcripts, in its own colour, and in
  a local hash-chained audit.
- **A2A blackout.** An organisation switch next to the Software Factory kill
  switch stops relay between hosts. Each host keeps coordinating locally and
  keeps audit-logging.

Code: `internal/a2a` (envelope, bus, mailbox, broadcaster, filter, relay,
audit), `internal/repomemory`, `internal/tools/a2a.go` and
`internal/tools/repomemory.go`, `agent.SideReply` (`internal/agent/a2a.go`),
`internal/tui/a2a.go`, and the relay half of `internal/sessionsync`.

## Terminology

The names follow the industry vocabulary: the Linux Foundation **A2A
(Agent2Agent) protocol**, and the *teammates / mailbox / broadcast* vocabulary
of multi-agent coding tools.

| Concept | Term | Code |
| --- | --- | --- |
| The feature | A2A messaging | `a2a` package and prefix |
| A source agent's identity | Agent Card | `a2a.AgentCard` |
| The harness's "I'm working on X" | Broadcast | kinds `broadcast`, `broadcast_end` |
| A session's store of what it received | Mailbox | `a2a.Mailbox` |
| A model's question and the answer | Direct message and reply | kinds `message`, `reply` |
| A model's adjust / todo / wait / ignore | Acknowledgement | kind `ack` |
| Stopping relay between hosts | A2A blackout | `a2aBlackout` |

"ACP" in Belai is the Agent Client Protocol (editor integration), unrelated.
Bare "blackout" is the schedule's `blackoutActive`, and "messaging" alone is
the notification centre, so this feature is always prefixed `a2a`.

**The wire format is A2A-shaped but not A2A JSON-RPC.** Envelope field names
mirror the A2A `Message` and `AgentCard`, so an A2A gateway can be added later.

## The envelope

| Field | What it holds |
| --- | --- |
| `messageId` | `a2a-` and a lower-case ULID |
| `kind` | `broadcast`, `broadcast_end`, `message`, `reply` or `ack` |
| `ts` | milliseconds since the epoch |
| `contextId` | the thread: the first message's id for a message/reply chain, or the broadcast's own id |
| `agent` | the Agent Card: `id` (`<hostId>:<sessionId>`), `hostId`, `sessionId`, `profile`, `hostname`, `capabilities` (`a2a.message`) |
| `work` | `repo` (the remote's `owner/name`, or `path:` and 16 hex of the root path's SHA-256 with no remote), `branch`, `files` (repo-relative, at most 50), `pr`, `ticket` (a fleet claim, or a reference such as `ABC-123` in the branch) |
| `to`, `referenceMessageId`, `parts` | a message's, reply's or ack's: the addressee, the message answered, the text parts (`[{text}]`, at most 4 KB together) |
| `decision` | an ack's: `adjust`, `todo`, `wait` or `ignore` |

An agent is shown as `@<profile|belai>·<6 hex>`: the first six hex characters
of the session part of its id, lower-cased, every non-hex character stripped
(`a2a.DisplayID`). The website computes the same handle.

Every identifier is reduced to a fixed shape by `Envelope.Clean` before it is
stored, sent or shown; a field that does not fit is dropped, an envelope whose
kind, ids or card do not fit is refused. The text parts are the only free text:
model-written, sanitised, capped, and **classified on receipt** as
`tools.KindPeer` before any model reads them.

## Same host, with no login

There is no socket IPC between Belai processes, so the bus follows the file
patterns already in use (fleet's registry, crewlog, `config.AcquireFileLock`),
under `~/.vulnetix/belai/a2a/`:

- `cards/<session>.json`: each live session's Agent Card and last broadcast. A
  card whose process is gone or whose beat is two minutes stale is removed by
  the next reader, like a fleet worker's record.
- `bus.jsonl`: every envelope, appended under `bus.lock` and rotated to
  `bus.jsonl.1` at 4 MiB. Each process tails it from its own offset, starting
  at the end, so a new session hears what comes next and learns of existing
  peers from their cards.
- `audit.jsonl`: the local audit (below).

Same-host messaging works with no Vulnetix login; only relay between hosts
needs one. The status bar says `A2A: local only` then.

## Broadcasts

The broadcaster is harness-only. It composes a session's work from what the
harness already knows: the repository, branch and open pull request from the
git reading (`gitsync.Info`), the files the session's tools changed (the
TUI's diff observer), and the ticket. It broadcasts when the work changes, at
most once every 10 seconds, and sends `broadcast_end` when the session ends.

`a2a.filter` decides what a session receives: `repo` (the default) a
broadcast about the same repository, `all` every one, `off` none (it still
broadcasts). A self-echo is always dropped, and a message, reply or ack is
received only by the agent it names.

A matching broadcast becomes a harness directive built from cleaned
identifiers only (the coordinator-fact precedent): the sender's card, its
repo, branch, PR, ticket and files, and the overlap with the files this
session changed. It offers the options (adjust the tool calls, add a todo,
wait, `A2AMessage`, ignore), points at `RepoMemoryRead` and `RepoMemoryWrite`,
and gives the acknowledgement's shape:

```
A2AAck{messageId:"a2a-…", decision:"ignore", reason:"I only edit docs/; no overlap."}
```

A running loop hears it at its next round (`Session.Peer`); an idle session
hears it with the next prompt. The same work from the same peer is told once.

## Tools

- **`A2AMessage{to, text}`** sends a question to an agent whose broadcast
  reached the session (by its `@profile·hex` or full id). It is always
  asynchronous: it returns `Sent a2a-… to @agent. The reply will arrive in a
  later message.` at once.
- **`A2AAck{messageId, decision, reason}`** records a decision (`adjust`,
  `todo`, `wait` or `ignore`) with a reason of at most 140 characters. Every
  decision but `ignore` is also sent to the sender as an `ack`.

Both are `KindA2A`: read-only, sanitise-only, offered in plan mode, and added
to every main session with A2A on, after the profile's allowlist (like the
board). `a2a.enabled` off removes them.

## Side-turn replies

A received message is classified first. A withheld one is answered with a
fixed harness refusal and its agent never reads it. An admitted one is
answered in a **side-turn** (`agent.SideReply`): a separate model call on the
receiver's model with a read-only view, its system prompt, a harness summary
of the session (repository, latest task, todos, files changed, latest reply)
and the message as a classified attachment. Its only tools are
`RepoMemoryRead` and `RepoMemoryWrite`, scoped to the receiver's repository,
for at most four rounds. It never touches the main loop's history, and the
main turn is not interrupted. The answer goes back as a `reply`; the calls it
made are recorded on the `a2a_reply_out` entry's `meta.calls`.

The sender receives the reply as a classified attachment that quotes its own
question, under a directive naming the ids: mid-loop at the next round, or as
a turn of its own when the session is idle (like a finished background
check).

## Repo memory

Notes agents keep about a repository: who owns which files, what to wait for,
what was agreed over A2A. **`RepoMemoryRead{query?, limit?}`** returns the
notes holding every word of the query, or the most recent; **`RepoMemoryWrite{note,
tags?}`** appends one note of at most 1 KB.

- **Always available.** They are core tools: every registry carries them
  (`tools.Default`), for every model and surface (TUI, headless, rc, ACP,
  fleet workers, subagents, Pix Sandbox). A profile's allowlist, `Without` and
  plan mode never remove them. A2A being off does not either.
- **Scope.** The repository key the envelope uses: the origin's `owner/name`,
  or the root path's hash, or outside a repository the trusted project root's
  hash. No argument names a path or another repository.
- **Store.** `~/.vulnetix/belai/memory/repos/<key>.jsonl`, outside every
  repository, appended under a lockfile so the sessions on a host share it, at
  most 500 live notes per key (the server's cap), oldest pruned. Each note has
  a stable `noteId` (a ULID) and harness-stamped facts: time, host, Agent Card
  and session, and the A2A `messageId` and `contextId` when written during an
  exchange.
- **Trust.** A note is model-written (or console-written) text. It is
  sanitised and capped, classified (`KindRepoMemory`) before it is stored, and
  a read is `KindRepoMemory` again, which classifies before any model sees it.
- **Replication.** While sync is on, `sessionsync.MemoryReplicator` uploads
  every local change (`POST /v1/belai/hosts/{hostId}/memory`, notes and
  tombstones) and pulls the organisation's (`GET /v1/belai/memory?after=`).
  A pulled note is upserted by `noteId`, the newest `updatedAt` winning; a
  tombstone (`deletedAt`) removes it. A machine with an empty store (a Pix
  Sandbox) hydrates before its first read, waiting at most a few seconds. The
  A2A blackout does not stop replication: it is persistence, like the session
  mirror.
- **The console.** The Software Factory web console is where people manage
  memory: add, edit and delete notes, or clear a scope. A console note has
  `hostId` `web` and is classified on read like any other.

The fleet's profile lessons ([fleet](fleet.md#memory)) live in the same store
under the `profile` scope and replicate the same way.

## Transcripts

| Entry type | What it records |
| --- | --- |
| `a2a_broadcast_out` | a broadcast this session sent |
| `a2a_broadcast_in` | a matching broadcast it received |
| `a2a_message_out`, `a2a_message_in` | a direct message sent or received |
| `a2a_reply_out`, `a2a_reply_in` | a reply sent (with the side-turn's `calls`) or received |
| `a2a_ack` | an acknowledgement, sent or received (`meta.direction`) |
| `a2a_state` | the relay state: local only, relaying, blackout |

`meta` carries `messageId`, `contextId`, `referenceMessageId`, `peerAgentId`,
`peerSessionId`, `peerHostId`, `peerDisplay`, `repo`, `branch`, `pr` and
`ticket`, so the website links both ends of a thread. These entries are never
replayed as model messages on resume, and the syncer mirrors them like any
line.

In the TUI an A2A row is a panel in `ColorA2A` with a glyph (`⇄` broadcast,
`→` message out, `←` message in, `↩` reply, `·` ack), the peer's handle and
repo@branch, PR and ticket. `ui.show_a2a` (default on) hides them.

## Across hosts

When sync is on and no blackout holds, the relay forwards this process's
envelopes to `POST /v1/belai/hosts/{hostId}/a2a` (`{envelopes:[…]}`, answered
`{accepted, blackout}`) and writes the organisation's into the local bus,
de-duplicated by `messageId`, from the inbox poll's `a2a` items after
`?a2aAfter=<seq>`. An envelope from this host, or older than an hour, is
skipped. The cursor is host-wide (`relay.json`) and also takes the server's `a2aCursor`, which moves past envelopes the server held back from this host (its own, and direct messages to other hosts), so a poll that returns none still advances.

## A2A blackout

The organisation's switch, next to the Software Factory kill switch. The
server sends it as `controls.a2aBlackout` on session registration and
heartbeat, on the rc heartbeat's answer, and as `blackout:true` on a relay
post. The host records it in `a2a/blackout.json` for every process. While on:

- nothing is relayed in either direction, and the inbox asks for no `a2a`;
- the local bus goes on, so agents on the host still coordinate;
- the local audit records `a2a.withheld` for each envelope held back and
  `a2a.blackout` when it switches;
- the status bar says `A2A: blackout`.

## Audit

`audit.jsonl` in the A2A directory is a crewlog-style hash-chained audit of
every envelope sent, received, withheld or filtered. It holds facts only (the
kinds `a2a.sent`, `a2a.received`, `a2a.withheld`, `a2a.blackout` and the data
keys `message_id`, `kind`, `peer`, `decision`), never text parts. It is written
with or without a login and during a blackout. The same facts, except
filtered receipts, go to the uploaded audit ([audit](audit.md)) when sync is
on.

## Surfaces

Every agent run is an A2A agent: the TUI, a headless `belai -prompt` run,
every `belai rc` session (Pix Sandbox personas included), every ACP editor
session and every fleet worker's item. Outside the TUI, `internal/a2alink`
runs the same loop without a screen (`headless.OpenA2A`): it broadcasts the
session's work, delivers a matching broadcast's directive and a reply through
`Session.Peer` (a note that lands while no loop runs is told at the next
round), answers a message in a side-turn while the main turn keeps running,
and writes the `a2a_*` entries to the run's transcript (`turnlog.Log.A2A`).
The files a broadcast names come from every tool's diff and, in goal mode,
from the pass ledger's touched paths (`Session.SetFilesHook`); a fleet
worker's ticket is the card it claimed. Without a TUI the relay polls the
inbox's `a2a` items itself, naming its own session so it never claims
another's requests. An ACP connection keeps one endpoint per editor session
across model switches, and a one-shot headless run broadcasts its last change
and `broadcast_end` as it exits. The rc daemon and fleet workers also
replicate the memory, and the rc daemon records the blackout for the host.

`e2e/a2a_test.go` drives two headless runs on one host against the mock
provider: the edit's broadcast, the message, the side-turn reply tied to the
question, an ignore acknowledgement, and the blackout (no relay call, local
audit still written).
