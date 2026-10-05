# Importing agent definitions

**Status:** alpha-20261006. Shipped in an early form; the mapping may still change.

Four other agent harnesses describe an agent in files Belai can read. None of them
is a standard, so Belai keeps its own [profile schema](agent-profiles.md) and has one
adapter per format that converts a definition into an ordinary profile:

| `-from` | What it reads |
| --- | --- |
| `claws` | a Claws package: a directory holding `CLAW.md`, or the file itself |
| `nemoclaw` | an NVIDIA NeMo Fabric agent configuration, a YAML or JSON file with `schema_version`, `metadata` and `runtime` |
| `hermes` | a Hermes profile: its directory, or a `.tar.gz` made by `hermes profile export` |
| `mini-swe` | a mini-SWE-agent YAML configuration |

- [Using it](#using-it)
- [What is mapped](#what-is-mapped)
- [What an import never does](#what-an-import-never-does)
- [Edge cases](#edge-cases)

## Using it

```sh
belai agent import -from claws ./release-helper          # preview
belai agent import -from hermes -yes ./research.tar.gz   # save
belai agent import -from auto -name triage ./agent.yaml  # detect the format
belai agent validate -from mini-swe ./default.yaml       # convert, print, save nothing
```

Without `-yes` an import is a preview: it prints what became of every part of the
source and writes nothing. With `-yes` it installs the skills that came with the
definition (under the [library's rules](library-items.md#skills)) and saves the
profile. A profile of the same name needs `-force`, or `-name` to save it under
another one. `-from auto` (or `-from` with no format) picks the format from the files.

The report has four parts: what was **mapped** to a profile field, what is **kept in
the profile's metadata**, what was **not imported**, and anything to **read before
saving**. The tools line marks any tool that can change files or run commands.

## What is mapped

| Profile field | From |
| --- | --- |
| `name` | the agent id (Claws), `metadata.name` (NeMo Fabric), the profile name or directory (Hermes), the file name (mini-SWE), or `-name` |
| `description` | the agent name, `metadata.description`, `profile.yaml`; a plain sentence when the source has none |
| `system_prompt` | the `CLAW.md` body (or `SOUL.md`), `instructions.system.content`, `SOUL.md`, `agent.system_template` with `instance_template` |
| `provider`, `model` | only when the source names a provider Belai has built in; otherwise the model is kept in metadata and the profile uses the session's |
| `tools` | the source's tool names through a fixed table (`read`, `grep`, `exec`, `web_fetch` and the like); a name with no match is reported and dropped |
| `max_iterations` | `runtime.max_turns`, `agent.step_limit` (at most 200) |
| `skills` | each `skills/<name>/SKILL.md` that the library accepts; the skill is installed and named in the profile |
| `metadata` | what has no field: fallbacks, sampling settings, cost limits, package references, cron expressions, subagent lists (see [the profile's `metadata`](agent-profiles.md#profile-schema)) |

A definition that names no tool Belai knows, or none at all, gets `Read`, `Grep` and
`Glob`: an import never ends up with every tool because a source was silent.

## What an import never does

- It makes a single-mode, supervised profile. There is no worker block, schedule,
  `guardrails` or `ask_permission` override, `facts` or `knowledge`. A cron job in the
  source is kept as text; turning on a schedule is your decision.
- It carries no endpoint, credential or environment variable name. `auth.json`,
  `.env`, key files and similar are not read, even from an archive.
- It takes nothing from an MCP server entry. MCP servers are your own settings.
- It does not copy memories, workspace files or packages.
- Everything from the source is cleaned (control, escape and delimiter markup removed)
  and the result goes through the profile validator, so a definition that would not
  be a valid profile is refused whole.

## Edge cases

- A symbolic link given as the source is refused. A link inside a directory or an
  archive is skipped and counted in the report.
- Files over 1 MiB, more than 300 files, more than 8 MiB in all and archive entries
  that leave the archive (`..` or an absolute path) are refused.
- A Claws `CLAW.md` with an empty body uses the `SOUL.md` it points to.
- mini-SWE templates keep their `{{ }}` placeholders; Belai does not fill them, and the
  report says so.
- A skill whose name starts with `belai-` is not imported: that prefix is Belai's own.
