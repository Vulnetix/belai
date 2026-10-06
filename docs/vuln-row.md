# Vulnerability row

**Status:** new; the identifier table may grow.

When a tool result or a turn's reply names a vulnerability identifier, the
thread gets one small row for it: a link to the Vulnetix console page, a hint
for the Vulnetix CLI's `vdb` command, a button that sends a prepared remediation
prompt, and a button that starts the built-in `belai:triage` agent on that
identifier. The website shows the same row for a synced session.

```
─ vulnerability  CVE-2021-44228                                drag to copy ─
  console  https://www.vulnetix.com/vuln/CVE-2021-44228
  vdb      vulnetix vdb vuln CVE-2021-44228
           vulnetix vdb --help lists the remediation lookups
           [ remediate ]  [ launch belai:triage ]
           [ open link ]  [ copy link ]  [ copy command ]
```

- [What is recognised](#what-is-recognised)
- [The row](#the-row)
- [Remediate](#remediate)
- [On the website](#on-the-website)
- [belai:triage](#belaitriage)
- [Security model](#security-model)
- [Limitations](#limitations)

## What is recognised

`internal/vulnid` is the one recognizer. An identifier is a prefix and the
shape that prefix allows:

| Prefix | Shape |
|---|---|
| `CVE` | `CVE-2021-44228` (four digit year, 4 to 12 digits) |
| `GHSA` | `GHSA-jfh8-c2jp-5v3q` (three groups of four from GitHub's alphabet) |
| `OSV`, `PYSEC`, `GSD`, `MAL` | `PREFIX-YYYY-N` |
| `RUSTSEC` | `RUSTSEC-2020-0071` |
| `GO` | `GO-2022-0969` |
| `EUVD` | `EUVD-2024-12345` |
| `VND` | `VND-2025-123` |
| `DSA`, `DLA`, `USN` | `DSA-5432-1`, `USN-6001-2` |
| `RHSA`, `RHBA`, `RHEA`, `ALSA`, `RLSA` | `RHSA-2023:1234` |

Detection is strict. The prefix is upper case in running text, only ASCII
counts (a full-width digit, a Cyrillic letter or the long s is not a letter of
an identifier), and a control, bidi or zero-width character inside a would-be
identifier ends the match. The identifier must stand alone: `xCVE-2021-44228`,
`CVE-2021-44228-extra`, `CVE-2021-44228.json`, a path, a URL or a query value
is not a mention. Sentence punctuation after it is fine. The same identifier
gets one row per session, at most three rows one turn adds and 40 a session.
`vulnid.Valid` is the lenient check for one value (any case, outer space
trimmed) and returns the canonical form.

`internal/audit` and `internal/kanban` keep their own charset check because
they also carry scanner rule ids, which have no prefix. Everything about a
prefixed advisory identifier is in `vulnid`.

## The row

The row is added when the turn ends, so it never lands inside a streaming
reply. It is built from the canonical identifier and nothing around it:

- **Link.** `https://www.vulnetix.com/vuln/` plus the identifier as one escaped
  path component (`vulnid.URL`). It is plain text: drag across it to select and
  copy it (a release copies, as everywhere in the thread). Click `[ open link ]`,
  or hover the row and press `ctrl+y`, to open it in your browser; only `https`
  is ever opened. `[ copy link ]` copies it without selecting.
- **Command.** `vulnetix vdb vuln <id>` (`vulnid.Command`), also plain text to
  select, with `[ copy command ]`. The line under it names `vulnetix vdb --help`
  for the other remediation lookups. A selection over the whole card copies the
  link and the command and nothing else: the labels, the note and the buttons
  are interface.
- **Launch.** Click `launch belai:triage` to start the agent on the identifier.
  The row then reads `belai:triage started`; a second click does nothing.

## Remediate

`[ remediate ]` sends a prepared prompt (`vulnid.RemediationPrompt`) as a prompt
of your own: it is echoed in the thread, admitted (sanitised and classified) and
run like one you typed, in the mode the session is in. The prompt names the
identifier as one `vulnerability_id:` line it calls data, then asks the model to
look the advisory up (the Vulnetix tool or `vulnetix vdb vuln <id>`), find out
whether this repository's manifests and lockfiles are affected, apply the
smallest safe fix and run the tests when they are, and finish with the verdict,
the evidence, what changed and how it was checked. The identifier is its only
variable part.

- **The agent.** With no agent engaged in agent mode, the session moves to Auto
  first (for this session only, nothing saved), so the role manager picks the
  mode and the agent profile for the prompt instead of the turn waiting on the
  picker. An engaged agent, or plan or goal mode, is left as it is: a plan mode
  session answers with a plan.
- **The edit is the deliverable.** The prepared prompt, a request that names an
  identifier and asks to fix, patch, bump or remediate it, and the bare link and
  `vdb` command the copy buttons produce are recognised by the harness
  (`vulnid.IsRemediation`, no model; a question about an advisory is not one).
  Such a turn is not put to the mode-choice panel or the agent pick: it runs as an
  agent turn with the full tool surface and a sealed directive that orders the
  work (one lookup, read the manifest, edit, check) and keeps the board out of it
  until the fix lands. Three rounds without a changed file, not eight, draw the
  firm edit directive, and a final answer with no change is sent back once. The
  turn may still end with no edit when the reply quotes the file and line that show
  the repository is not affected or already fixed. Plan mode, a read-only turn and
  a profile without `Edit` keep their surface and get none of this
  (`agent/remediation.go`).
- **Once.** The row then reads `remediation started`. A click while a turn is
  running does nothing but say so; it never steers or queues.
- **Your draft.** The composer's text and attachments are left alone.

## On the website

For each row the session also records a `vuln` entry (`vulnid.EntryType`): the
canonical identifier as `content` and, in `meta`, `vuln_id`, `url`, `command`
and `prompt`, each composed from the identifier by the harness. Session sync
mirrors the line like any other, so the website draws the same row from it: the
link and the command to select or copy, an open button, and a remediate button
that sends `prompt` as an ordinary website prompt (the typed-prompt path, so
`sync.remote_prompts` and admission apply as for any web prompt). A TUI session
writes it when the turn ends, and a headless or remote-control session writes it
in its transcript at the turn's end (`internal/turnlog`), so the row appears
there too. A resume skips the entry and no model sees it. The terminal row stays
render-only: a resumed session does not redraw it.

## belai:triage

A built-in, read-only background agent (`internal/agentprofile/builtin/triage.json`).
Its tools are `Vulnetix`, `Read`, `Grep` and `Glob`; it cannot edit, write,
install or run a shell. It looks the identifier up with `vdb`, finds whether this
repository's manifests and lockfiles name an affected package, and replies with a
verdict, the evidence (file and line) and the recommended fix. The main session
applies it. (`belai:triage-vulns` is the different agent that triages a scan's
`.vulnetix` artifacts.)

The launch is the ordinary background-agent path
(`bgagent.Manager.StartTask`): the permission rules, the trust gate, the OS
sandbox, hooks and the pass budget all apply as for any background agent, and it
shows in the runs panel (`f8`). The identifier is passed as the task's one
`vulnerability_id:` line, which the prompt calls data. The instance name is
`belai:triage#N`, so the identifier is not in the start line or the notification.

## Security model

- The row on screen is **render-only and ephemeral**: `buildTurns` never
  promotes it, so no model sees it, and it adds nothing to telemetry, the audit
  log or a notification. The website's copy is a `vuln` session entry
  (identifier, link, command and prompt, all composed from the identifier), which
  session sync mirrors and a resume skips.
- Every field is composed from `vulnid.Valid`'s canonical string. The URL is a
  fixed https host and path with one escaped component; the click handler
  validates the identifier again, so a card that does not validate opens nothing
  and starts nothing.
- Detection is a harness check on text the user already sees. It calls no model.
- The launch can only start `belai:triage`, with the user's click as the
  trigger. A reply that says "launch the agent" does nothing. The same holds for
  remediate: it sends one fixed prompt built from the identifier, only on the
  user's click (or the website user's), and a reply cannot press it.
- The copy buttons put the link or the command on the clipboard, both composed
  from the identifier; the click handler validates the identifier again.

## Limitations

- The identifier table is fixed in `internal/vulnid`; an advisory scheme it does
  not list gets no row.
- The row is drawn in the terminal's usual way (a click opens the link), not as
  an OSC 8 hyperlink: the transcript renderer strips escape sequences from
  everything it draws.
- A result the guardrails withheld shows a placeholder, so it adds no row.
