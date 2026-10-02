# Vulnerability row

**Status:** new; the identifier table may grow.

When a tool result or a turn's reply names a vulnerability identifier, the
thread gets one small row for it: a link to the Vulnetix console page, a hint
for the Vulnetix CLI's `vdb` command, and a button that starts the built-in
`belai:triage` agent on that identifier.

```
─ vulnerability  CVE-2021-44228                                click to open ─
  console  https://www.vulnetix.com/vuln/CVE-2021-44228
  vdb      remediation: vulnetix vdb vuln CVE-2021-44228  (vulnetix vdb --help lists the remediation lookups)
           [ launch belai:triage ]
```

- [What is recognised](#what-is-recognised)
- [The row](#the-row)
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
  path component (`vulnid.URL`). Click it, or hover the row and press `ctrl+y`,
  to open it in your browser; only `https` is ever opened.
- **Hint.** One line pointing at `vulnetix vdb vuln <id>` and `vulnetix vdb --help`
  for the remediation lookups (`vulnid.Hint`).
- **Launch.** Click `launch belai:triage` to start the agent on the identifier.
  The row then reads `belai:triage started`; a second click does nothing.

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

- The row is **render-only and ephemeral**: it never produces a session entry,
  so it is not in the transcript, not in session sync and not in the audit log;
  `buildTurns` never promotes it, so no model sees it; it adds nothing to
  telemetry or a notification.
- Every field is composed from `vulnid.Valid`'s canonical string. The URL is a
  fixed https host and path with one escaped component; the click handler
  validates the identifier again, so a card that does not validate opens nothing
  and starts nothing.
- Detection is a harness check on text the user already sees. It calls no model.
- The launch can only start `belai:triage`, with the user's click as the
  trigger. A reply that says "launch the agent" does nothing.

## Limitations

- The identifier table is fixed in `internal/vulnid`; an advisory scheme it does
  not list gets no row.
- The row is drawn in the terminal's usual way (a click opens the link), not as
  an OSC 8 hyperlink: the transcript renderer strips escape sequences from
  everything it draws.
- A result the guardrails withheld shows a placeholder, so it adds no row.
