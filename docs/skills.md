# Skills

**Status:** alpha-20260926. Shipped in an early form; the tool shapes may
still change.

A skill is a `SKILL.md` file holding a procedure the agent can load when it is
relevant: how this team cuts a release, how to regenerate fixtures, how to
triage a scanner finding. Belai lists installed skills to the model by name
and description, the model loads one with the `Skill` tool, and it can offer
to save a new one with `SkillDraft`, which you approve.

- [Skill files](#skill-files)
- [Loading a skill](#loading-a-skill)
- [Builtin skills](#builtin-skills)
- [Self-authored skills](#self-authored-skills)
- [The library](#the-library)
- [Security model](#security-model)
- [Settings](#settings)
- [TUI](#tui)
- [Edge cases](#edge-cases)

## Skill files

Skills live in `~/.vulnetix/belai/skills/<name>/SKILL.md` (or
`$BELAI_HOME/skills/`). The file starts with front matter:

```markdown
---
name: release
description: Cut a Belai release, tag it, and check the Homebrew formula
allowed-tools: [Bash, Read, Grep]
---

1. Run `just check`.
2. …
```

| Field | Required | Meaning |
| --- | --- | --- |
| `name` | yes | the skill's name; a skill you write has lowercase letters, digits and single hyphens and lives in a directory of the same name |
| `description` | yes | what the skill does and when to use it, one line the model sees in the skill list |
| `allowed-tools` | no | the tools the procedure expects, separated by spaces (`[Bash, Read]` is also read); shown to the model with the body, never grants a tool |
| `disable-model-invocation` | no | `true` hides the skill from the model entirely |
| `license`, `compatibility` | no | informational |
| `metadata` | no | a map of string keys to string values; the `belai.` keys below are Belai's |

The format is the [Agent Skills specification](https://agentskills.io/specification).
Any other key fails validation, under the `skill_invalid` posture gate. A skill
that fails validation is not listed and cannot be loaded. A file written for the
earlier loader still loads: a front matter that is not valid YAML is read one
`key: value` line at a time, a scalar `metadata: text` is kept under the key
`note`, and `allowed-tools` may still be a `[a, b]` list.

### Metadata

`metadata` keys are yours, except the `belai.` prefix, which is Belai's and is
checked: a `belai.` key that is not listed here fails validation.

| Key | Value |
| --- | --- |
| `belai.role` | the profile the skill serves |
| `belai.niche` | one line naming the specialism |
| `belai.contexts` | environment names separated by commas (languages, platforms, techniques) |
| `belai.resources` | `https` documentation links separated by spaces, at most 40, no fragment |
| `belai.updated` | the date of the last review, `2026-01-31` |

In the library a skill holds at most 32 metadata entries, a key of at most 64
bytes, a value of at most 4096 and 16 KiB in all.

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

## Builtin skills

Belai ships one specialist skill for each of its six worker profiles
(`belai-scout`, `belai-builder`, `belai-reviewer`, `belai-vuln-scout`,
`belai-patcher`, `belai-verifier`). They are compiled into the binary, so they are
always the version that matches the profile, and they are listed and loaded only
in a session whose profile names them in `skills`
([agent-profiles.md](agent-profiles.md)). An ordinary session does not see them.

- Each is at most 100 lines and opens with a contents list. Its front matter
  names the tools it expects (`allowed-tools`) and, under `metadata`, the
  environments it covers (`belai.contexts`) and the documentation it points to
  (`belai.resources`).
- The `belai-` prefix is reserved: `SkillDraft`, the library and a profile cannot
  create a skill with that start, so a builtin is never shadowed or replaced.
- A builtin's text is classified like any other skill result.

## Self-authored skills

After the agent works out a procedure worth keeping, it can offer to save it
with `SkillDraft`:

```json
{"name": "fixtures", "description": "Regenerate test fixtures", "body": "1. …"}
```

1. The harness checks the name (lowercase letters, digits and single hyphens,
   at most 64), flattens the description to one line, sanitizes the body, and builds
   the complete `SKILL.md`, which must validate and stay under 32 KiB.
2. It shows you that exact file in a permission ask. When a skill of that
   name exists, the ask shows the diff against it.
3. Only if you approve is it written to
   `~/.vulnetix/belai/skills/<name>/SKILL.md`, and it is listed from the next
   turn.

`SkillDraft` always asks: an allow rule does not skip the ask, and neither
does turning the ask gate off. Where nobody can be asked (a headless run), the
call is withheld. It is a writing tool, so it is not available in plan mode or
in read-only sessions.

## The library

With the Vulnetix CLI signed in and `belai rc` running, your skills are kept in the
website's library like agents are: a skill you add or edit is pushed as a new
version, a skill saved on the website can be installed on any connected host, and
`sync.skills` (default on) switches all of it off. `belai skill list`, `validate`,
`import` and `export` do the same by hand. The library applies rules the loader
does not: a description is at most 1024 bytes, `license` at most 128, `compatibility`
at most 500 and `metadata` is bounded as above, a document holds no control character
but tab and line feed, and the whole document is at most 32 KiB. A skill that fails
them still loads here; it is only left out of the sync and reported as skipped. See
[library-items.md](library-items.md).

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

## Edge cases

- Plugin skills are named `plugin:name`, so they never clash with yours or
  with each other. Your skill named `release` and a plugin's `team:release`
  are two different skills.
- A skill that stopped validating after it was listed fails to load with an
  error naming it, and never returns a partial body.
- `SkillDraft` names are lowercase letters, digits and single hyphens, at most 64,
  so `../x` or `Name` is refused before any ask. A description longer than
  1024 bytes, an empty body, or a file over 32 KiB is refused the same way.
- A description that spans lines is flattened to one, so it cannot add a
  front-matter key.
- A new skill is listed from the next turn, not the one that wrote it.
- A skill body that opens with a list marker or a rule keeps it: only the line
  breaks after the closing `---` are trimmed from the start of the body.
