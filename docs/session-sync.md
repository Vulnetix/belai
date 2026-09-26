# Session sync

While Belai is logged in with the Vulnetix CLI, each session is mirrored to
the Vulnetix website. Two console pages show it:

- **Belai → History** (`/resolve/belai-history`) lists every synced session
  that is no longer attached to a running host.
- **Belai → Sessions** (`/resolve/belai-sessions`) lists the sessions running
  now. Opening one follows it live and accepts prompts, which run on the host
  exactly as if they had been typed there.

## The host is the source of truth

- **The mirror is the file.** The website receives the session's JSONL lines,
  each keyed by its line index (`seq`). Belai never composes an entry for the
  website: `appendEntry` writes the line to disk, then nudges the syncer, and
  the syncer tails the file.
  - Uploads are idempotent (the server ignores a `seq` it already holds).
  - A failed upload is simply re-read and re-sent.
  - A restarted host asks for the server's high-water mark and sends only what
    is missing.
  - A malformed or oversized line still takes its `seq` as a placeholder, so
    the sequence never has a hole.
- **A web prompt is a request, not a message.** Delivery works like this:
  1. The website stores the prompt, and the host long-polls its inbox and
     claims it.
  2. The TUI admits it like a typed prompt.
  3. The TUI writes the user line with `meta.source = "web"` and
     `meta.remote_prompt_id`, then acks the request.
  4. The line reaches the website through the same tail.

  Until that line arrives, the website shows only a status chip, never a
  message bubble. Chip states: *sent*, *delivered*, *queued behind the running
  turn*, *running*, *refused* or *expired*.
- **The website renders only committed lines, in `seq` order.** A line that
  arrives past a gap triggers a refetch of the gap, not an out-of-order render.

## What it sends, and where

- **Content:** every line of the session JSONL, including tool calls and tool
  results (already capped at 32 KiB by the session writer).
- **Host details:**
  - The host's name, reduced to identifier characters.
  - Its OS and the Belai version.
  - A random host id kept in `~/.vulnetix/belai/sync/host-id`.
- **Destination:** requests go to `https://www.vulnetix.com/api/site/v1/belai/*`.
  - `$VULNETIX_WEB_URL` overrides the origin.
  - The credential is only sent to `https://*.vulnetix.com` or a loopback
    origin for local development.
- **Credential:** the Vulnetix CLI's own credential in the `Authorization`
  header. That is `ApiKey <org>:<hmac>` from `vulnetix auth login`, read the
  same way as the MCP `vulnetix:cli` reference and cached for five minutes.
  - An opaque API-token login (`--token`, `VULNETIX_API_TOKEN`) is not
    accepted by the console, so sync stays off with that reason.
- **Visibility:** only the principal whose credential uploaded a session can
  see or prompt it on the website.

## Web prompts

A web prompt runs through the same path as a typed prompt: the same mode
selection, the same admission gate (sanitize plus the prompt classifier under
the effective posture), the same permission rules and the same tool surface.
Beyond that:

- **Cleaned first.** `sessionsync.CleanPrompt` strips harness delimiter markup,
  terminal control sequences, other control runes and bidi overrides, and
  caps it at 32 KiB.
- **Never a local command.** A leading `/` or `!` is prompt text: the website
  cannot run a slash command, a shell command or an `@` attachment from the
  composer.
- **Never touches the composer.** A draft or pending attachment you are typing
  on the host is left alone.
- **Queued while busy.** It waits (FIFO) while a turn is running or being
  prepared, or while you are on a screen other than the transcript. It is
  acked *queued* until then.
- **Refused when it cannot run.** Two cases:
  - The session is no longer the active one on the host.
  - The host is in agent mode with no agent chosen: the agent picker is the
    host user's to answer.
- **Asks stay on the host.** Permission asks and questions are answered on
  the host only. The website cannot approve a tool call.
- **Refusals show in the transcript.** If admission refuses the prompt, the
  refusal is written to the transcript like any other, and the website shows
  it.

Code: `internal/sessionsync` (client, syncer, inbox, prompt cleaning) and
`internal/tui/session_sync.go` (wiring, queue, `/sync`).

## Settings

```json
{ "sync": { "enabled": true, "remote_prompts": true } }
```

- **`sync.enabled`** — mirror sessions. Default on whenever a Vulnetix CLI
  credential resolves.
- **`sync.remote_prompts`** — accept web prompts. Default on; `false` shares
  sessions view-only.
- **Project layer:** a project settings file may turn either off, never on.
  The guardrails switch does not change sync; it is a data-egress setting,
  not a guardrail.

## `/sync`

- **`/sync` or `/sync status`** — shows whether sync is on, why it is off
  when it is, how many lines of this session the website holds, the last
  error, and queued web prompts.
- **`/sync off` / `/sync on`** — writes `sync.enabled` to the global settings
  and stops or starts the mirror. Stopping ends the live session on the
  website.
- **`/sync backfill`** — uploads this project's earlier sessions straight into
  History.

## Lifecycle

- **When a session appears.** It appears on the website after its first line
  is written; a session that never gets a line is never registered.
- **Liveness.** The host sends a heartbeat every 15 s. A session is live while
  its last heartbeat is under 45 s old, so a host that crashes drops into
  History on its own.
- **Moving to History.** Quitting the TUI, `/clear`, a resume of another
  session and `/sync off` all end the session, which moves it to History.
- **Compaction.** It starts a new session whose parent is the summarised one;
  the website links the two.
- **Headless and ACP.** `belai -prompt` keeps no transcript and ACP sessions
  are not persisted, so neither is synced.

## Server side

- **API:** `vdb-site` (`api/internal/handler/belai_*.go`) serves `/v1/belai/*`.
  - Host endpoints: host and session upsert, entry upload, heartbeat, end,
    inbox long-poll and prompt ack.
  - Browser endpoints: list, detail, paged entries, an SSE stream and prompt
    create/cancel.
- **Schema:** it lives in `saas` (`prisma/models/belai.prisma`, migration
  `20260926000001_add_belai_session_sync`).
- **Website pages:** `src/pages/resolve/belai-*.vue`, in the sidebar's
  **Belai** group.
