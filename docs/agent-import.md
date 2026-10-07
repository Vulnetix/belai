# Importing from other agent harnesses

**Status:** alpha-20261007. Shipped in an early form; the mapping may still change.

Last Updated: 2026-10-08

Other agent harnesses keep commands, skills, prompts, agents, hooks and instruction
documents as files. None of their layouts is a standard, so Belai keeps its own
schemas and has one adapter per layout that converts a file into the document the
[library](library-items.md) stores. The same converter does three jobs:

| Job | Where | What it does |
| --- | --- | --- |
| `belai agent import` | the command line | converts one agent definition in one of four formats into a profile ([agent definitions](#importing-an-agent-definition)) |
| `belai library scan` | the command line | searches this host for items of every kind kept by other harnesses and by Belai, and says what an import would make of each |
| Scan hosts and Import | the website | the same scan and the same conversion, started by a person from the library page ([from the website](#from-the-website)) |

Nothing here is written to disk by the converter. A scan reads files and reports;
an import reads one file again and uploads the converted document to the library.

- [Formats](#formats)
- [Scanning a host](#scanning-a-host)
- [What a scan reports](#what-a-scan-reports)
- [Mapping by kind](#mapping-by-kind)
- [From the website](#from-the-website)
- [What a scan and an import never do](#what-a-scan-and-an-import-never-do)
- [Importing an agent definition](#importing-an-agent-definition)
- [Edge cases](#edge-cases)

## Formats

A format names how a harness writes its files. Nine are harness formats, read by
`agentimport.ImportItem`; the [harness registry](harnesses.md) says which harness
uses which and where it keeps each kind. `belai` is Belai's own layout and the four
at the bottom are the agent formats of `belai agent import`.

| Format | Files it reads | What is particular |
| --- | --- | --- |
| `claude-code` | commands and subagents as Markdown, skills as `<name>/SKILL.md`, `CLAUDE.md`, and the `hooks` key of `settings.json` ([hooks](#hooks)) | front matter with `description`, `argument-hint`, `allowed-tools`, `model`, `hooks`; `` !`command` `` lines in a body; `$ARGUMENTS` and `$1` |
| `cursor` | commands as plain Markdown with no front matter, `.cursorrules`, `AGENTS.md` | the first line of the body becomes the description |
| `codex` | custom prompts as Markdown, `AGENTS.md`, and `hooks.json` ([hooks](#hooks)) | `description` and `argument-hint` in front matter, `$1` placeholders |
| `gemini-cli` | `GEMINI.md`, Markdown files | commands are TOML and are listed as unsupported; `{{args}}` is Gemini's placeholder and stays as written |
| `opencode` | commands and agents as Markdown | `agent`, `model`, `subtask`, a `tools` map of tool name to on or off, a `permission` block |
| `windsurf` | workflows as Markdown | `auto_execution_mode` is not imported |
| `copilot` | `.prompt.md`, `.agent.md`, `copilot-instructions.md` | `mode`, `tools` and `model` keys; the `.prompt` and `.agent` suffixes are not part of the name |
| `cline` | workflows as plain Markdown | no front matter |
| `generic-md` | Markdown with optional front matter | what the registry points every other harness at |
| `belai` | Belai's own files | commands, skills, prompts, agents, crews, processes, hook bundles and the settings file |
| `claws`, `nemoclaw`, `hermes`, `mini-swe` | the agent formats of [`belai agent import`](#importing-an-agent-definition) | agents only |

## Scanning a host

```sh
belai library scan                     # every kind
belai library scan -kind command       # one kind
belai library scan -json               # the report as it is uploaded
```

`-kind` is one of `command`, `skill`, `prompt`, `agent`, `crew`, `process`, `budget`,
`rewrite`, `provider`, `repo`, `hook` or `document`. The scan prints each item with its
verdict, then what it searched. It writes nothing and sends nothing.

A scan looks in three places.

1. **The user directories of every installed harness.** A harness is installed when
   one of its `detect` paths exists under your home directory. A harness that is not
   installed is counted, not searched.
2. **Belai's own directories**: commands, skills, prompts, processes, agent profiles,
   crews and hook bundles (`hooks/<name>/hooks.json` and the scripts beside it) under
   `~/.vulnetix/belai`, and its `settings.json` for budgets, the
   rewrite table, provider sets and repositories. Belai is listed as the harness
   `belai`.
3. **Each repository you trust** (`belai` asks the first time you open a directory;
   the choice is kept in `projects.json`, and a repository cannot trust itself). In
   each one the scan looks at the project directories of every harness, installed
   or not, plus `.vulnetix/belai/commands`, `skills`, `profiles/agents` and
   `profiles/crews`, `.vulnetix/prompts`, `.vulnetix/processes` and
   `.vulnetix/settings.json`. For hooks that means the project files of Claude Code
   (`.claude/settings.json`, `.claude/settings.local.json`) and Codex (`.codex/hooks.json`). A repository with nothing is still listed, with the
   folders it was searched in.

The reader is bounded. It never follows a symbolic link, reads no file whose name
looks like a credential store (`.env`, `auth.json`, key files and the rest of
`secretName`), skips a file over 1 MiB, looks at 500 entries of a directory at most
and about 6000 files in all, and stops after 25 seconds. A scan that stops early
says `partial`. Every link, credential file or oversize file it passed over is
counted in `skipped`.

## What a scan reports

Each candidate goes through the converter in preview mode, so the verdict is what
an import would do:

| Verdict | Meaning |
| --- | --- |
| `valid` | the converter took the file whole |
| `warning` | it imports, but part was not imported or needs a look: a key with no Belai field, an agent that named no tool and got the read-only set, a skill name that had to be changed |
| `invalid` | the converter refused it, and `reason` says why (delimiter markup, an invisible character, a missing body, a name that cannot be made into one) |

An item carries its `id` (the first twelve hex digits of the SHA-256 of
`kind|path`), `kind`, `name`, `path`, `harness`, `format`, `scope` (`user` or
`project`), `repo` for a project item, the `sha256` and size of the canonical
document, the item's own `description` when it has one (at most 200 bytes), the verdict and `reason`, `converted` (true when Belai front matter had to
be added or changed), and up to eight `notes` of 160 bytes each, taken from the
converter's report. A note is `mapped`, `metadata`, `dropped` or `warning`; warnings
and drops come first when there are more than eight.

The report also has `harnesses` (each installed harness with counts per kind and
any kind it keeps in a format Belai does not read), `checked` and `notInstalled`
(how many harnesses the registry knows, and how many are not installed here),
`repos`, `skipped`, `partial`, `scannedAt` and `durationMs`. The report is under 1
MiB and 2000 items; past that notes are trimmed first, then the least useful items
are dropped (invalid first) and the report is marked partial.

An item in a settings file has `#name` after the file in its `path`
(`~/.vulnetix/belai/settings.json#my-repo`), because one file holds several. The hooks
of a Claude Code settings file have `#hooks` after the file
(`~/.claude/settings.json#hooks`); the website passes the path back unchanged.

A hook item also has `files`, the scripts the import would carry, at most 32:

```json
"files": [{"path": "guard.sh", "bytes": 123, "sha256": "<hex>"}]
```

The `sha256` of a hook is `libitem.HashBundle` of its document and these files, so a
script edited after the scan changes it. The bytes of a script leave the host only in
an import.

## Mapping by kind

### Commands, prompts and skills

The name comes from the file (a command or prompt) or from the front matter and
then the folder (a skill), lower-cased, and is changed with a warning if it holds
anything but letters, digits and hyphens (a command may also keep `.` and `_`). A
file one folder down (`commands/git/commit.md`) is named `git-commit`. The
description comes from the front matter, else from the first line of the body.

| Source | Becomes |
| --- | --- |
| `description` | `description` |
| `argument-hint`, `argument_hint` | a command's `argument-hint` (at most 256 bytes); kept in metadata for a skill; not imported for a prompt |
| `allowed-tools`, `tools` | **text only**: kept in metadata as `source.allowed-tools`, never as a Belai `allowed-tools`, so an imported file never grants a tool. Not imported for a prompt |
| `license`, `compatibility`, `metadata`, `disable-model-invocation` | kept by a skill or command; keys starting `belai.` are dropped |
| `order`, `enabled` | kept by a prompt |
| `model`, `hooks`, `agent`, `mode`, `subtask`, `context`, any other key | named in the report as not imported (a `hooks` key in an item's front matter is never read; hook files are the [hook](#hooks) kind) |
| no front matter | added; `converted` is true |

`$ARGUMENTS`, `$1`, `$2` work as they do in Claude Code. `` !`command` `` lines
and `{{args}}` have no meaning in Belai and stay as plain text, with a warning. A
file that already is a Belai document keeps its own bytes, so its hash matches the
library's copy.

### Agents

A subagent file is Markdown with front matter and the instructions as the body. It
becomes a single-mode, supervised profile through the same builder
`belai agent import` uses: `tools` go through the fixed table (a name with no match is
dropped and named), no tool at all gets `Read`, `Grep` and `Glob`, a `provider/model`
value is used only when Belai has that provider built in, and `maxTurns` is capped
at 200. `skills`, `mode` and `temperature` are kept in metadata. `permission`,
`permissionMode`, `hooks`, `mcpServers` and the rest are not imported. A file from
Belai's own `profiles/agents` is read strictly and kept whole. The canonical document
is the profile Markdown the agent library stores.

### Crews

A crew file is decoded strictly (no unknown key), with the checks that need no
profile on this host: a lower-case name, 1 to 8 members, 0 to 8 replicas each. A
member that names no agent is a warning. The canonical document is the crew JSON.

### Processes

A file named `<NNN>-<name>.json` or `.sh` (a leading `_` means disabled) in the
global or project processes directory. A `.sh` file is offered as one `sh -c`
command. Belai's own validator decides.

### Budgets, rewrites, providers and repositories

Read from the `token_budgets`, `bash_rewrite`, `providers` and `firewall`, and
`repos` settings of a settings file, through the library validators. A file gives one
budget set, one rewrite table and one provider set, and one item per repository. No
other harness stores these, so Belai is their only source. A provider document never
holds a key: `api_key_env` names a variable, and a key is never read from settings.

### Hooks

A hook item is one hooks file or one `hooks` block of a settings file, never a single
event. Two dialects are read, because their layouts are verified: `claude-code` (the
`hooks` key of `~/.claude/settings.json`, `.claude/settings.json` and
`.claude/settings.local.json`) and `codex` (`~/.codex/hooks.json`, `.codex/hooks.json`).
Cursor, Windsurf, Cline and Kiro keep hooks in other layouts; a scan names them under
the harness as `hook: unsupported format, not read` and opens nothing. Belai's own
bundles (`~/.vulnetix/belai/hooks/<name>/hooks.json` and the files beside it) are read
as the harness `belai` and kept as they are.

The name is made from the harness and where the file is: `claude-code-user`,
`codex-user`, `claude-code-<repo-name>` for a project file, with `-local` added for
`settings.local.json`, folded to the library name rule. The document is
`{"name", "description", "hooks"}`.

**A settings file is read for one key.** The file is decoded into raw values and only
`hooks` is looked at. Nothing else in it (`env`, `apiKeyHelper`, MCP headers, permission
rules) is parsed, kept, logged or named in a report, because `settings.json` holds
credentials. In a hooks file of its own, a key starting `_` is a comment and is dropped
with a note, Codex's `description` becomes the description, and any other key is counted
and dropped.

**What is refused as invalid**, each with its reason: an event the library does not name,
a handler whose `type` is not `command`, a command over 512 bytes or over more than one
line, a matcher on `UserPromptSubmit`, `Stop` or `SubagentStop`, more than 8 groups in an
event or 8 handlers in a group, more than 64 commands, and a timeout outside 1 to 600.
A handler key Belai has no field for (`statusMessage`, `async`) is dropped with a note,
once per key name.

**What is carried.** The first word of each command decides. It may be written with
quotes, `~/`, `$HOME`, `${HOME}`, `$CLAUDE_PROJECT_DIR` or `${CLAUDE_PROJECT_DIR}`
(against the trusted repository), `${CLAUDE_PLUGIN_ROOT}`, or as a path relative to the
hooks file or the repository.

| First word | Result |
| --- | --- |
| a file under the harness's own directory (`~/.claude`, `~/.codex`) or, for a project file, under the trusted repository | the file is carried into the bundle and the first word becomes its bundle name (`guard.sh`); the same file named twice is carried once and two files with one name get `-2`, `-3` |
| a bare name (`curl`, `python3`, `vulnetix`) | kept, with a warning to add it to `hooks.allowed_programs` on the host |
| an absolute path, `~` path or variable outside those directories, or a file that does not exist | kept, with a warning that it is not carried |
| a file that exists but cannot be carried (a symbolic link, a link in the path, not a regular file, empty, over 256 KiB, not UTF-8 text, a control or invisible character, or a known token) | the item is `invalid` |

Arguments are kept exactly as written. An argument a bundle cannot run (`$`, `~`, a
quote, a glob or a redirect) earns one warning naming the first word. A bundle holds at
most 32 files of 256 KiB and 2 MiB in all, and more makes the item `invalid`. A command
starting with an assignment (`KEY=value cmd`) or using `&&`, `|` or `;` is kept with a
warning, because the host runs a command as a fixed argument list and never through a
shell. The install check on the host (`hooks.ParseDefinition`, the same one a bundle
will pass when it is installed) is the authority; the scan warns about what it can see.

**A report names a command by its first word only.** Each note says what happened to it
(`mapped command: ~/.claude/hooks/guard.sh carried as guard.sh`, `warning command: curl
is not a bundle file; add it to hooks.allowed_programs on the host`, `dropped _comment:
comment key dropped`). A full command line can hold a secret, so it never appears in a
report. The hooks document and every carried script also go through the secret check
(`agentfiles.HoldsSecret`, a private key block or a known token) and through
`libstore.UntrustedText`; a hit makes the item `invalid` ("looks like it holds a
secret"). A script is read and never run.

The converted document goes through `libitem.Validate` for a hook before it is reported.

### Documents

`AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, `.cursorrules` and the like, as the registry
lists them. A document is UTF-8 text with no NUL, at most 256 KiB, whose name is not a
credential store's, with no private key block or known token anywhere in it and no
delimiter markup or invisible character. A leading byte order mark is removed with a
warning and CRLF becomes LF. The importing person says which agent receives it.

## From the website

The library page has one **Scan hosts** action per tab. It sends a `library_scan`
request to every online host, each host scans for up to 25 seconds and uploads its
report, and the page groups what was found by host. When nothing is found it shows
what was searched. Selected items are imported with one `library_import` request
each.

The two requests are [dispatch kinds](remote-control.md#scanning-and-importing-from-other-harnesses)
and are always on. There is no `sync.*` switch: nothing is read until a person asks
on the website, a scan sends names, paths, hashes, verdicts and short notes and no
document, and a document leaves the host only when that person imports that item.

An import does not trust the request. The host:

1. re-derives where it may read from its own harness list and trusted repositories
   and refuses a path that is not exactly a file a scan would list;
2. checks that no part of the path below the root is a symbolic link;
3. reads the file itself and runs the same converter;
4. refuses the item when the SHA-256 of the canonical document is not the one the scan
   reported (`that item changed since the scan; scan again`);
5. uploads the canonical document, a string for Markdown kinds and documents, an
   object for JSON kinds, with an agent's bundled skills, a document's target agent and,
   for a hook, its scripts as `files` (`{"path", "content"}` with the content in base64).

For a hook the host re-reads the hooks file, converts it again, reads the scripts again
under the same rules, and recomputes the bundle hash. A change to the file or to any
script makes it differ from the scan's `sha256`, and the import is refused as changed.

## What a scan and an import never do

- Follow a symbolic link, read a credential file or a file over 1 MiB.
- Write to disk. The only writes are by the library after an upload.
- Grant a tool. `allowed-tools` from a source is text in metadata. An agent with no
  mappable tool gets the read-only set.
- Carry an MCP server entry, a permission rule, an endpoint, a credential or an
  environment variable name from a source. A hook is carried only by the hook reader
  ([Hooks](#hooks)), which takes the `hooks` key and the scripts it names and nothing else.
- Install, enable or run a hook. A scanned or imported hook is a library item and nothing
  more: it reaches a host only through `item_install`, on request, as before, and
  `sync.hooks` still closes that. A script is never executed by a scan or an import.
- Turn on a schedule, a worker block or an autonomy level.
- Repair text. A file with delimiter markup, a control or escape character, a
  bidirectional override or an invisible rune is `invalid`, not cleaned.
- Read a path the request names that the host would not have listed itself.

## Importing an agent definition

`belai agent import` converts an agent definition written for one of four other
harnesses into an ordinary profile. Belai keeps its own [profile schema](agent-profiles.md)
and has one adapter per format:

| `-from` | What it reads |
| --- | --- |
| `claws` | a Claws package: a directory holding `CLAW.md`, or the file itself |
| `nemoclaw` | an NVIDIA NeMo Fabric agent configuration, a YAML or JSON file with `schema_version`, `metadata` and `runtime` |
| `hermes` | a Hermes profile: its directory, or a `.tar.gz` made by `hermes profile export` |
| `mini-swe` | a mini-SWE-agent YAML configuration |

### Using it

```sh
belai agent import -from claws ./release-helper          # preview
belai agent import -from hermes -yes ./research.tar.gz   # save
belai agent import -from auto -name triage ./agent.yaml  # detect the format
belai agent validate -from mini-swe ./default.yaml       # convert, print, save nothing
```

Without `-yes` an import is a preview: it prints what became of every part of the
source and writes nothing. With `-yes` it installs the skills that came with the
definition (under the [library's rules](library-items.md#skills)) and saves the
profile. A profile of the same name needs `-force` (which is for the profile only), or `-name` to
save it under another one. `-from auto` picks the format from the files.

The report has four parts: what was **mapped** to a profile field, what is **kept in
the profile's metadata**, what was **not imported**, and anything to **read before
saving**. The tools line marks any tool that can change files or run commands, and any that sends
text off this machine (`WebFetch`, `WebSearch`).

### What is mapped

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

### What an agent import never does

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

### Agent definition edge cases

- A symbolic link given as the source is refused. A link, a special file, a credential
  file, a file over 1 MiB or a file that cannot be read inside a directory or an archive
  is skipped and counted in the report; a single file over 1 MiB given by name is
  refused, and so is a required file (`SOUL.md`, `CLAW.md`) that was skipped for size.
- More than 1000 files kept, more than 8 MiB kept, an archive that expands past 256 MiB
  and archive entries that leave the archive (`..` or an absolute path) are refused.
- Skills that come with a definition are installed only when the host has no skill of
  that name, whatever `-force` says; one the host already has is left alone and is not
  named in the profile. The profile is saved first, so a refused name leaves nothing
  installed.
- A tool the source denies that Belai has no name for makes the import hold back the
  tools that change things (`Write`, `Edit`, `Bash`, `Task`), since it cannot tell what
  the deny covers; the report says which.
- When the source has several model roles, the profile takes the one that chats
  (`default`, `main`, `chat`...) and never an embedding or reranking role; with no clear
  choice it inherits the session's model.
- A Claws `CLAW.md` with an empty body uses the `SOUL.md` it points to.
- mini-SWE templates keep their `{{ }}` placeholders; Belai does not fill them, and the
  report says so.
- A skill whose name starts with `belai-` is not imported: that prefix is Belai's own.

## Edge cases

- A file several harnesses list (a repository's `AGENTS.md`, a shared `~/.agents/skills`)
  is one item. It goes under the installed harness that has its own format, then under
  the first one the registry names.
- Two items of the same kind and name in different places are two items with different
  paths. The website asks which one to import.
- A harness that keeps a kind in a format Belai does not read (Gemini CLI commands
  are TOML) is named in the report under that harness, and nothing in it is read.
- A directory that is itself a symbolic link (a `commands` folder pointing at a
  dotfiles checkout) is skipped and counted, as is any link inside one. Name the real
  folder in a trusted repository, or copy the files, if you want them scanned.
- A repository the registry lists as missing, or one that is not trusted, is not
  searched, and an import never reads from it.
- A file edited, replaced or swapped for a link between the scan and the import is
  refused; scan again.
- A host that predates the scan answers `library_scan` and `library_import` with
  `update Belai on the host`.
- A scan does not stop for one bad file. An unreadable directory counts for nothing and
  the scan goes on.
- `belai library scan` and the website scan the same way, so a result seen at the
  command line is what the website will show for that host.

