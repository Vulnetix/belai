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

`-teleport-push` agrees that the origin host may push the session's uncommitted
and unpushed changes to the forge as one `belai/teleport/<id>` branch, which this
host then fetches. Without it (and without `teleport.push: allow` in your own
settings) the changes are sent as a patch and replayed here
([Code](#code)).

## What moves

The session transcript, the JSONL that session sync already mirrors to the
backend, the agent profile the session ran under, and the code the session
changed: the commits it has not pushed and the work it has not committed
([Code](#code)). Nothing else comes with it: no provider, credential or
setting, and no schedule.

| Moves | Stays behind |
| --- | --- |
| The transcript, up to the last line the backend held when you asked | Providers, API keys, firewall and sandbox settings |
| The agent profile, and every crew that lists it with those crews' members, when this host lacks them | Schedules, on a profile or a crew |
| The session's mode and active profile, as the new session's own record | Plan and goal names, guardrails settings |
| The session's model, as a hint | The directory the session ran in |
| The session's unpushed commits and uncommitted work, as a forge branch or a replayed patch | Credential files, binaries, oversized files, links and anything git ignores |

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
   tells the backend, and, when the code went by replay, replays it
   ([Code](#code)) before it opens the terminal UI on the session. The session
   record remembers the origin id as `teleportedFrom`.

## Code

A session that ran in a repository may hold changes the target cannot get from
the forge: commits it has not pushed, and work it has not committed. When the
session's git facts show some (uncommitted files, commits ahead of the upstream,
or no upstream to compare with) and the origin host is running `belai rc`, the
backend asks it, after the profile backup, with a `teleport_code` request
(status `syncing_code`). The request names the teleport, the session's directory,
whether you agreed to a branch and whether the forge is not to be tried. The
origin host re-checks the directory against its own list, reads its own working
tree, and answers one of three ways. The teleport never fails because of it: the
transcript and the profile move regardless.

**Branch.** When the forge can carry it, the origin host builds one commit
holding its final state on top of its HEAD, without touching its checkout, and
pushes exactly that commit as `belai/teleport/<id>` (an explicit refspec to
`origin`). The target fetches the branch and makes a worktree at that commit. Only
a branch name and a commit id pass through the backend. The branch stays on the
forge: delete it when you no longer need it. A push needs two things: the origin
host's `teleport.push` setting and your agreement.

| `teleport.push` on the origin host | What it does |
| --- | --- |
| `ask` (the default) | Pushes only when you passed `-teleport-push` (or have `teleport.push: allow` on this host) |
| `allow` | Pushes whenever a teleport asks |
| `never` | Never pushes, so the changes always go as a patch |

The setting is read from the user's own settings layers only; a repository
cannot make a host publish to its forge. The origin host runs as a daemon with
nobody to ask, so the question is answered by the flag and the settings, not at
the moment of the push.

**Replay.** When the branch is not allowed, the push fails, or this host cannot
fetch the branch (it asks once for a replay instead), the origin host sends:

- the patch from the last pushed commit it builds on to its final state, with
  that commit, the tree object it ends with and the file list;
- a summary of the change and one instruction per file, written by the origin
  host's fast model (the `teleport_distill` role), or, when no model gives
  anything usable, a list of the files the harness wrote itself.

The user is told, in the terminal and in the session's opening notices: *"Coordination
over GitHub was not done because …, so the changes from the origin host are being
replayed on this host now. Please wait."* The reason is the origin's or the
target's own (it was not allowed to push, the push was refused, the branch could
not be fetched here). Then, on this host and before the terminal UI opens:

1. A worktree is made at the commit the changes build on, under the worktrees
   directory. The checkout you are in is never touched.
2. The harness applies the patch file by file with git, checks each part first,
   and reports any that does not apply.
3. It builds the checkout's tree the way the origin built its own and compares.
   An exact match is verified and no model runs.
4. For what is left, this host's own model runs in **code mode**, with the
   origin's summary and instructions and the parts of the patch that did not
   apply as one attachment. It has only file tools (`Read`, `Write`, `Edit`,
   `Grep`, `Glob`, `LS`): no shell and no network, so no word of the hand-over
   can run a command.
5. After each pass the harness compares again. When the trees still differ, a
   decision model rates the checkout against the origin's summary (the
   [`teleport_verify`](jev-jobs.md) job), and when it cannot settle it the model
   verifier answers with one sentinel (`TELEPORT_VERIFIED`, `TELEPORT_INCOMPLETE`
   or `TELEPORT_FAILED`). A rating of verified is never accepted for a file the
   checkout lacks. At most three model passes run, and a failed rating stops
   them.
6. The outcome is a notice in the new session: matched exactly, matched the
   origin's summary, or which files still differ and need your check.

What never leaves the origin host: a credential-bearing file (`.env`, key and
certificate stores, `credentials*`, SSH keys), a binary, a file over 256 KiB, a
symbolic link, a path that is not plain ASCII, and anything git ignores. They are
listed by name and reason on the target, so the session knows what to recreate
by hand. A patch over 512 KiB or over 200 files is not sent, and the target is told why.

## What the backend keeps

A teleport row holds the origin session, origin host, target host, the new
session, a status, a reason and times. It outlives the sessions and hosts it
names, so the audit record stays whole. For a sandbox, the Activity tab lists
each session teleported away from it.

The git facts, the profile manifest, the session's overrides and the code
result (a branch's name and commit, or a replay's patch, summary and
instructions) are held only while the teleport is open. They are cleared when it
completes, fails or expires, which it does after ten minutes. The patch is
bounded to 600 KiB and the server checks only shape and size; the target and the
origin host decide what it may contain.

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
- Changes reach the target only when the origin host is running `belai rc`. When it
  is not, the target says so and the session opens with the transcript alone.
- The replay needs the commit the changes build on in this repository (it is
  fetched from origin if missing). When that commit was never pushed, the
  teleport stops and says to push the branch from the origin host.
- `-teleport-push` needs `-teleport`.
- The command needs a terminal, and cannot be combined with `-resume`,
  `-continue` or `-prompt`.

## How the transcript is treated

The code half is untrusted too. The patch is parsed against a closed grammar
before git sees it: each file is one `diff --git a/P b/P` header with the same
safe path on both sides, then only index lines, mode lines (100644 or 100755),
the `---` and `+++` names and hunks whose line counts add up exactly. A rename,
copy, binary patch, symlink or submodule mode, a path outside the repository,
inside `.git` or inside Belai's own `.vulnetix` directory, or any extra line is
refused. Git applies it in the replay's worktree and itself refuses a path
through a symbolic link. The origin's summary and instructions are text from
another host's model: they are sanitised, ride only as a classified attachment
on the model's user turn (never as prompt text or part of the system block), and
the model that reads them has no tool that can run a command or reach the network.

The transcript came through the backend from another host, so Belai reads it as
untrusted text. Every line is checked for its shape, and its text and meta
strings go through the same sanitiser as any other text bound for a model.
Delimiter markup is removed. The origin's working directory and its plan, goal
and repository records are not used. The directory comes from where you ran the
command. A profile or crew goes through the library installer, which parses it
strictly and refuses one that would replace something it was not told to.
