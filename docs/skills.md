# Skills

**Status:** Roadmap. This feature is designed but not yet in a release, so the
page describes the planned behaviour.

A skill is a `SKILL.md` file holding a procedure the agent can load when it is
relevant: how this team cuts a release, how to regenerate fixtures, how to
triage a scanner finding. Signet already lists installed skills to the model by
name and description. This page covers loading a skill's body, and letting the
agent write new skills for you to approve.

- [Skill files](#skill-files)
- [Loading a skill](#loading-a-skill)
- [Self-authored skills](#self-authored-skills)
- [Security model](#security-model)
- [Settings](#settings)
- [TUI](#tui)

## Skill files

Skills live in `~/.vulnetix/signet/skills/<name>/SKILL.md`, or come from a
[plugin](plugins.md). The file starts with front matter:

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
| `allowed-tools` | no | narrows the tools the model may use while following the skill |
| `disable-model-invocation` | no | `true` hides the skill from the model; only you can load it |
| `license`, `compatibility`, `metadata` | no | informational |

Any other key fails validation, under the `skill_invalid` posture gate.

## Loading a skill

The model loads a skill with the `Skill` tool (`{"name": "release"}`). The
harness looks the name up in its own registry and reads that file. The tool
takes no path, so it cannot be used to read anything else. You can load one
yourself with `/skill:<name>`, which attaches it to your next prompt.

`allowed-tools` only ever narrows. A skill cannot grant a tool the session
does not already have, and permission rules still apply.

## Self-authored skills

After the agent works out a procedure that took real effort, it can offer to
save it as a skill with the `SkillDraft` tool:

1. The model proposes `name`, `description` and a body.
2. The harness validates the front matter, sanitizes the body, and shows you
   the complete file in a permission ask.
3. Only if you approve is it written to
   `~/.vulnetix/signet/skills/<name>/SKILL.md`. Replacing an existing skill is
   a separate ask that shows the diff.

When a goal completes, the goal evaluator may suggest drafting a skill from
the work. That suggestion is a sealed directive, and the draft still goes
through the ask.

`SkillDraft` is a mutating tool, so it is not available in plan mode.

## Security model

- A skill body is text that may come from a third party (a plugin), so
  `Skill` results carry the `skill` tool kind, which is always sanitized and
  always classified. A body never enters the system block.
- `SkillDraft` never writes without your approval, including with the ask
  gate off: turning the ask gate off does not skip this ask.
- Skills are read from your global directory and enabled plugins only. There
  is no project-level skills directory.

## Settings

```json
{
  "skills": {
    "self_authoring": true
  }
}
```

The project layer may set `self_authoring` to `false`, never to `true`.

## TUI

`/skills` lists installed skills with their source (yours or a plugin's). From
there you can view a skill, enable or disable it, or delete one you own.
