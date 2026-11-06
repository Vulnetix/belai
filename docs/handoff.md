# Reviewed handoff into a fresh session

**Status:** new.

`/relay` writes a short handoff note from the current session, shows it to you
for review and edit, and on your accept starts a fresh session that begins from
that note. The old session is left as it was and becomes the new one's parent.
Use it when a session has drifted or filled up and you want a clean start that
keeps the goal, the state of the work and the next steps, with you checking the
note first.

It is not `/compact`. `/compact` summarises on its own and never asks you. It is
also not `/handoff <plan file>`, which executes a plan. `/relay` is yours alone:
no model can start it, accept the note or skip the review.

## Using it

```
/relay
/relay keep the auth rewrite and drop the logging cleanup
```

Text after the command is a direction for the drafter. The review that opens
shows the note, its size against the cap and whether you edited it.

| Key | Action |
| --- | --- |
| `enter` | Accept: validate the note again and continue in a fresh session. |
| `e` | Edit the note in `$VISUAL` or `$EDITOR`. An edit that fails validation is refused and the previous draft stays. |
| `r` | Redraft from the same transcript and direction. |
| `esc` | Discard. The session is unchanged. |
| `pgup`, `pgdn`, arrows | Scroll. |

`/relay` refuses, and says why, while a turn is running, when the transcript is
empty, when `handoff.enabled` is false, when no classifier is configured, and
while another draft is in flight. A draft written from a transcript that has
since moved on (you sent another prompt while it was drafted) is dropped, and
accept refuses a session that moved.

## What the harness does and what the model does

The harness collects the facts, enforces the size cap and the structure, runs
the checks and switches sessions. The model writes prose only. No model is asked
whether the note is safe.

The drafter sees the serialized transcript (the same document `/compact` reads,
fenced with a random nonce) and a second fenced block of harness facts:

| Fact | Source | Cleaning |
| --- | --- | --- |
| Goal objective and status | the latest goal state | one line, angle brackets removed, 500 characters |
| Todo list | the session's todo list, at most 30 items | one line each, angle brackets removed, 200 characters |
| Branch and whether the tree is dirty | the session's git facts | one line, angle brackets removed, 100 characters |
| Files the session changed | the diff observer, at most 50 listed | a path that fails `sanitize.PathText`, or holds `<`, `>` or `&` or delimiter markup, is dropped |

`handoff.include_goal` off leaves the goal and the todo list out of the facts.
The direction is cleaned to one line of at most 500 characters and rides after
the blocks, never in the system text.

The note must have four sections, in any order: `## Goal`, `## State`,
`## Next Steps` and `## Open Questions`. An empty reply, a missing heading, or a
reply over `handoff.max_bytes` is refused, not repaired. Delimiter markup is
removed from what is kept.

## Fail closed

- The note is model text derived from tool output, so it goes through the
  security classifier (the same admission as a `/compact` summary) before it is
  shown. A refusal or an error shows nothing and changes nothing.
- When you edit the note, accept validates and sanitises the edited text again,
  and admits it again if it differs from the text the classifier last saw.
- Nothing is written to disk before accept. The only file is the scratch copy
  `$EDITOR` opens, in a private temporary directory removed when the editor
  returns or the flow ends.
- Any error leaves the old session untouched.

## What the new session is

Accept mirrors a compaction fork. The new session has a new id and a
`parent_session`, its first entry is a `summary` entry with `kind` `handoff`
(and the source message count, `edited` and the length of the direction, never
its text), and it keeps the session name and the todo list. The goal state, a
running plan, the changed-file set, the test pass and the compaction breaker
belong to the old session and do not carry; the note carries what the next
session needs of them.

The note re-enters the conversation only through the summary wrapper, as an
untrusted user turn followed by a fixed assistant acknowledgement. It is never
put in a system, agent or tools block, and never sent to a decision backend. The
next turn runs with normal admission, permissions, sandbox, plan mode and hooks.
Resuming the new session restores the note the same way.

The `pre_compact` hook event fires before the draft, as it does for `/compact`.
The role manager records a `handoff_draft` activity (`valid` or `invalid`) with
the answering model, shown at the `decisions` level.

## Settings

| Key | Default | Range | Meaning |
| --- | --- | --- | --- |
| `handoff.enabled` | on | on or off | Turns `/relay` off. |
| `handoff.max_bytes` | 16384 | 2048 to 65536 | The size cap of the note, both what the drafter is told and what review accepts. A value outside the range is clamped. |
| `handoff.include_goal` | on | on or off | Gives the drafter the goal objective and the todo list as facts. |

Your own layers set any key. A project settings file, which comes with a cloned
repository, may only turn `/relay` off, turn `include_goal` off or lower
`max_bytes` (a lower value is applied, an equal or higher one is dropped with a
note). It can never enable, raise or set a switch to true. The merge is
`mergeHandoff` in `internal/config/handoff.go`, called from `Resolve` and
`Settings.Override`. `/settings` has rows for the two switches; `max_bytes` is a
settings file key. There is no flag and no environment variable.

## Routing

The drafter is the `handoff_draft` use case. It stays on the main model, as
compaction does, because the note is the next session's whole memory of this
one. A routed setup can send it elsewhere with `routing.use_cases.handoff_draft`.
