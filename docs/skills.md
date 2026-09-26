# Skills

**Status:** alpha-20260926. Shipped in an early form; the tool shapes may
still change.

A skill is a `SKILL.md` file holding a procedure the agent can load when it is
relevant: how this team cuts a release, how to regenerate fixtures, how to
triage a scanner finding. Signet lists installed skills to the model by name
and description, the model loads one with the `Skill` tool, and it can offer
to save a new one with `SkillDraft`, which you approve.

- [Skill files](#skill-files)
- [Loading a skill](#loading-a-skill)
- [Self-authored skills](#self-authored-skills)
- [Security model](#security-model)
- [Settings](#settings)
- [TUI](#tui)

## Skill files

Skills live in `~/.vulnetix/signet/skills/<name>/SKILL.md` (or
`$SIGNET_HOME/skills/`). The file starts with front matter:

```markdown
---
name: release
description: Cut a Signet release, tag it, and check the Homebrew formula
allowed-tools: [Bash, Read, Grep]
---

1. Run `just check`.
2. …
```

| Field | Required | Meaning |
| --- | --- | --- |
| `name` | yes | the skill's name |
| `description` | yes | one line the model sees in the skill list |
| `allowed-tools` | no | the tools the procedure expects; shown to the model with the body, never grants a tool |
| `disable-model-invocation` | no | `true` hides the skill from the model entirely |
| `license`, `compatibility`, `metadata` | no | informational |

Any other key fails validation, under the `skill_invalid` posture gate. A skill
that fails validation is not listed and cannot be loaded.

## Loading a skill

The system prompt lists each skill as `name: description` whenever the `Skill`
tool is on the session's surface. The model loads one with:

```json
{"skill": "release"}
```

The harness looks the name up among the installed skills, re-validates the
file, and returns its body with a one-line header naming the skill and its
source. The tool takes no path, so it cannot read any other file. A skill with
`disable-model-invocation: true` answers exactly like a missing one.

`Skill` is read-only, so it is also available in plan mode and to explore
subagents. A background agent with a tool allowlist gets it only if the
allowlist names it.

## Self-authored skills

After the agent works out a procedure worth keeping, it can offer to save it
with `SkillDraft`:

```json
{"name": "fixtures", "description": "Regenerate test fixtures", "body": "1. …"}
```

1. The harness checks the name (lowercase letters, digits and hyphens, at most
   64), flattens the description to one line, sanitizes the body, and builds
   the complete `SKILL.md`, which must validate and stay under 32 KiB.
2. It shows you that exact file in a permission ask. When a skill of that
   name exists, the ask shows the diff against it.
3. Only if you approve is it written to
   `~/.vulnetix/signet/skills/<name>/SKILL.md`, and it is listed from the next
   turn.

`SkillDraft` always asks: an allow rule does not skip the ask, and neither
does turning the ask gate off. Where nobody can be asked (a headless run), the
call is withheld. It is a writing tool, so it is not available in plan mode or
in read-only sessions.

## Security model

- A skill body is text that may come from someone else, so `Skill` results
  carry their own tool kind, `skill`, which is always sanitized and always
  classified. A body never enters the system block; only the name and a
  sanitized description do.
- The file you approve is the file that is written: the preview and the write
  are built by the same code from the same sanitized input.
- Skills are read from your global directory and from enabled
  [plugins](plugins.md), whose skills are namespaced `plugin:name`. There is
  no project-level skills directory.

## Settings

```json
{
  "skills": {
    "self_authoring": true
  }
}
```

With `self_authoring` off, `SkillDraft` is removed from every surface. The
project layer may set it to `false`, never to `true`.

## TUI

`/skills` lists the installed skills with their source, marking the ones
hidden from the model as `(user only)`. To remove a skill, delete its
directory.
