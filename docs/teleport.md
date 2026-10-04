# Teleport

`belai -teleport <session-id>` continues a session of your account on the host
you are at. The session may have run on another host, in a Pix Sandbox or on the
web. The original keeps running where it is. The copy gets a new session id and
opens in the terminal UI.

```
cd ~/src/belai
belai -teleport 3f2a9c10-6b7e-4c1d-8a52-0d9e4f1b7a33
```

A session card on the website, and the session page itself, copy this command
with one click.

## What moves

Only the session transcript, the JSONL that session sync already mirrors to the
backend, and the agent profile the session ran under. Nothing else comes with
it: no provider, credential or setting, and no schedule.

| Moves | Stays behind |
| --- | --- |
| The transcript, up to the last line the backend held when you asked | Providers, API keys, firewall and sandbox settings |
| The agent profile, and every crew that lists it with those crews' members, when this host lacks them | Schedules, on a profile or a crew |
| The session's mode and active profile, as the new session's own record | Plan and goal names, guardrails settings, uncommitted files |
| The session's model, as a hint | The directory the session ran in |

The model is a hint. The new session uses it only when this host already has
that provider's credentials, and otherwise keeps its own default. A profile in
`scheduled` mode is not installed, because a teleport carries no schedule.

## Direction

A teleport always lands on a host, because the target is a terminal. The origin
can be a host, a Pix Sandbox or a web session. Nothing teleports to a sandbox or
to the web, and the backend refuses a target that is a sandbox.

## The sequence

1. Belai checks that the directory is a git checkout, then asks the backend for
   a teleport. Every run asks again and gets a new teleport row.
2. When the session ran under an agent profile that is not a built-in, the
   backend asks the origin host, with a `teleport_backup` request, to back up
   that profile, every crew that lists it and those crews' members into your
   library. The origin host must be running `belai rc` for that. When it is not
   running and the library already holds the profile, the teleport goes ahead
   with what the library has. When it is not and the library lacks the profile,
   the teleport stops and says to start `belai rc` there.
3. Belai reads the transcript from the backend, which freezes it at the line the
   session had reached when you asked. It checks that every line from the first
   to that one arrived, once each and in order. A gap is a refusal, never a
   partial session.
4. Belai checks the repository. The checkout must be the same repository the
   session was in (matched by owner, name and host). When the commit the session
   was at is not what this checkout has, the session gets a git worktree at that
   commit under the worktrees directory, on a new `teleport/<branch>-<id>`
   branch, and the checkout you are in is left alone. A commit this repository
   lacks is fetched from origin. When it is still missing, because it was never
   pushed, the teleport stops and says to push that branch from the origin host.
   `-teleport-ref <ref>` picks a ref or commit yourself instead.
5. Belai installs the profile and crews this host lacks, validated whole like
   any library install. One that cannot be installed is reported in the session
   and does not stop the teleport.
6. Belai writes the new session under a new id in this host's session store,
   tells the backend, and opens the terminal UI on it. The session record
   remembers the origin id as `teleportedFrom`.

An uncommitted change on the origin is not part of any of this. The session
opens with a note when the origin had one.

## What the backend keeps

A teleport row holds the origin session, origin host, target host, the new
session, a status, a reason and times. It outlives the sessions and hosts it
names, so the audit record stays whole. For a sandbox, the Activity tab lists
each session teleported away from it.

The git facts, the profile manifest and the session's overrides are held only
while the teleport is open. They are cleared when it completes, fails or
expires, which it does after ten minutes.

A web page that opens a teleported session shows where it came from and links
to the origin session. The target host records a `host.teleport` event in its
audit log.

## Limits and refusals

- The session must be mirrored. A session that ran with `sync.enabled` off has
  no transcript on the backend and cannot be teleported.
- The id may be a unique prefix of at least eight characters.
- A transcript is refused when it holds more lines than a session Belai writes,
  or a line, type or role Belai does not write.
- A scheduled profile is not teleported.
- The command needs a terminal, and cannot be combined with `-resume`,
  `-continue` or `-prompt`.

## How the transcript is treated

The transcript came through the backend from another host, so Belai reads it as
untrusted text. Every line is checked for its shape, and its text and meta
strings go through the same sanitiser as any other text bound for a model.
Delimiter markup is removed. The origin's working directory and its plan, goal
and repository records are not used. The directory comes from where you ran the
command. A profile or crew goes through the library installer, which parses it
strictly and refuses one that would replace something it was not told to.
