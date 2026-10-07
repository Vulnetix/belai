# Harness registry

**Status:** alpha-20261007. Shipped in an early form; the data may still grow.

Last Updated: 2026-10-08

Belai can look for commands, skills, prompts, agents, hooks and instruction documents
that you wrote for other agent harnesses (Claude Code, Cursor, Codex and the
rest). To do that it needs to know where each harness keeps them. The registry
in `internal/harness` is that knowledge: one record per harness, embedded in the
binary as `internal/harness/harnesses.json`.

- [What a record holds](#what-a-record-holds)
- [Shapes](#shapes)
- [Formats](#formats)
- [How it is generated](#how-it-is-generated)
- [Adding or fixing a harness](#adding-or-fixing-a-harness)
- [Rules the tests enforce](#rules-the-tests-enforce)
- [Supported harnesses](#supported-harnesses)

## What a record holds

| Field | Meaning |
| --- | --- |
| `id` | Stable lower-case id. It matches the id `gh skill install --agent` and the Vulnetix CLI use. |
| `aliases` | Other ids the CLI sources use for the same harness (`openai-codex` for `codex`). `harness.ByID` resolves them. |
| `name` | What the harness calls itself. |
| `detect` | Home-relative paths whose presence means the harness is installed. Empty for a harness that has no user directory of its own. |
| `format` | The `agentimport` format that reads the harness's files. |
| `dirs` | Per kind (`command`, `skill`, `prompt`, `agent`, `document`, `hook`): `user` directories (always `~/...`), `project` directories (relative to a repository root, `.` for the root itself), the `shape`, for documents the fixed `files`, and for a `json-key` hook the `key`. For a hook the `user` and `project` entries are JSON files, not directories. |

Nothing in the registry is an absolute path. A user directory starts with `~/`
and is resolved against the home directory with `harness.Expand`. A project
directory is joined to a repository root that the caller already trusts. A path
with `..`, a leading `/` or a `~` elsewhere fails the tests.

Belai's own kinds that no other harness stores (process, budget, rewrite,
provider, repo, crew) are not in the registry.

## Shapes

The shape says how the files in a directory are laid out.

| Shape | Layout | Used for |
| --- | --- | --- |
| `md-file` | one `<name>.md` per item, directly in the directory | commands, prompts, agents |
| `skill-dir` | one `<name>/SKILL.md` per item | skills |
| `doc-file` | the directory holds fixed file names listed in `files` (`CLAUDE.md`, `AGENTS.md`) | documents |
| `json-key` | each entry is a JSON file, and the item is the value of its `key` (`hooks`) while the rest of the file is never read | Claude Code hooks, in `settings.json` |
| `json-file` | each entry is a JSON file that is one item | Codex hooks, in `hooks.json` |
| `unsupported` | the harness stores this kind in a format Belai does not read, for example the TOML files of Gemini CLI commands, or hooks in a dialect nobody has verified | reported by a scan, never read |

A harness that keeps a kind in a non-Markdown format is listed with
`unsupported` so a scan can say "found, not importable" instead of skipping it
without a word. Continue prompts (`.prompt` files), and the commands of Gemini
CLI, Qwen Code and iFlow CLI (TOML) are in that state today.

Hooks are read only for the two dialects whose files have been checked: Claude Code (the `hooks` key of `~/.claude/settings.json`, `.claude/settings.json` and `.claude/settings.local.json`, shape `json-key`) and Codex (`~/.codex/hooks.json` and `.codex/hooks.json`, shape `json-file`). Cursor (`.cursor/hooks.json`), Windsurf (`.windsurf/hooks.json`), Cline (`.clinerules/hooks`) and Kiro (`.kiro/hooks`) keep hooks in layouts that are `unsupported`: a scan says so on the harness and opens nothing. Claude Code's `.claude/hooks/` is where its scripts live, not a hooks file, so the registry does not list it; a script there is carried when a command in `settings.json` names it ([agent-import.md](agent-import.md#hooks)). Belai's own hook bundles in `~/.vulnetix/belai/hooks/<name>/` are not in the registry either; a scan reads them as the harness `belai`.

Where a harness calls a kind something else, the registry uses the Belai kind
that behaves the same way: Windsurf and Cline workflows are `command`, Codex
custom prompts and Pi prompt templates are `prompt`, Factory droids are `agent`.

## Formats

`format` names the `agentimport` adapter. Eight harnesses have their own because
their files carry conventions the others do not (front matter keys, a missing
front matter, a file suffix such as `.prompt.md`). Every other harness uses
`generic-md`: Markdown with optional front matter.

| Format | Harness |
| --- | --- |
| `claude-code` | Claude Code |
| `cursor` | Cursor |
| `codex` | OpenAI Codex |
| `gemini-cli` | Gemini CLI |
| `opencode` | opencode |
| `windsurf` | Windsurf |
| `copilot` | GitHub Copilot |
| `cline` | Cline |
| `generic-md` | everything else |

## How it is generated

`harnesses.json` is not edited by hand. `tools/gen-harnesses` builds it from
three files in the Vulnetix CLI repository and one file in this one:

| Source | Gives |
| --- | --- |
| `cli/internal/agent/hosts.go` | ten verified hosts: id, name, user and project skill directories, detect paths |
| `cli/cmd/skills.go`, the unexported `agentDirs` map | about 65 ids and their user skill directories. The generator parses the Go source, it does not import it. |
| `cli/internal/aibom/catalog/tools.json` | project-scope `skills`, `commands`, `agents`, `prompts` and `hooks` entries, and the fixed instruction files, of every CLI agent, IDE and IDE extension. A `hooks` entry is `unsupported` unless the overlay names a dialect. |
| `tools/gen-harnesses/overlay.json` | what no source records: user-scope commands, agents, prompts and instruction directories, shapes, formats, detect paths, display names and the id mapping |

```sh
go run ./tools/gen-harnesses -cli ../cli -out internal/harness/harnesses.json
go run ./tools/gen-harnesses -cli ../cli -out internal/harness/harnesses.json -table -   # the table below
```

The merge rules:

- Ids from the three sources are joined on the canonical id. `overlay.json` has
  an `idMap` for the ones that differ. `agentDirs` (the id `gh skill install`
  uses) wins, so `openai-codex` becomes `codex`, `roo-code` becomes `roo` and
  `kilo-code` becomes `kilo`; the other spelling stays as an alias.
- Skill directories are the union of all three sources, in source order. An
  `agentDirs` entry without a `~/` prefix (`promptscript`) is a project directory.
- A `tools.json` glob such as `.claude/commands/**` becomes the directory
  `.claude/commands`. A glob that is a file (`skills-lock.json`) or starts with
  `**` is dropped. Instruction entries without a wildcard become document files;
  rules directories such as `.cursor/rules/**` are not a Belai kind and are left out. A `hooks` entry is a directory (`.kiro/hooks/**`) or a file (`.cursor/hooks.json`), and always starts as `unsupported`.
- Only `cli-agent`, `ide` and `ide-extension` entries of `tools.json` count.
  Services, cloud agents and conventions are not harnesses on this machine.
  An entry that records no directory or file is dropped, and `overlay.json`
  `exclude` names a harness that records a file which is not an instruction document.
- An overlay directory is added to the generated ones. `"replace": true` discards them.
- `detect` is the host's own list, else the overlay's, else the parent of the
  first user skill directory that no other harness shares. A harness on a
  shared directory (`~/.agents/skills`, `~/.config/agents/skills`) gets a detect
  path only from the overlay, so installing one of them does not mark the rest
  as installed.
- The output is sorted by id and indented, so two runs over the same inputs give
  the same bytes. A test runs the generator twice and compares the result with
  the embedded file.

Several harnesses share a skill directory by design. A scanner reads each
directory once, whichever harnesses name it.

## Adding or fixing a harness

1. Run the generator and look at the entry. A harness that is in the CLI sources
   already has its skill directories and project directories.
2. Add an entry to `tools/gen-harnesses/overlay.json` under `harnesses` for what
   is missing: `dirs.<kind>.user`, `project`, `shape`, `files`, a `format`, a
   `detect` list or a `name`. Only write a directory you have checked against
   the harness's own documentation or a real install.
3. If the CLI uses a different id for the same harness, add it to `idMap`.
4. If the files are not Markdown, set `"shape": "unsupported"`. A hooks dialect becomes
   `json-key` or `json-file` only after its layout is checked against the harness's own
   documentation or a real install, and after `agentimport` has a reader for it.
5. Regenerate, then paste the new table into the section below.
6. `go test ./internal/harness`. The parity test needs the CLI checkout beside
   this repository (or `HARNESS_CLI_DIR`) and is skipped without it.

A harness that has a new format needs an adapter in `internal/agentimport` first;
until then leave `format` as `generic-md`.

## Rules the tests enforce

- Every id in the CLI's `hosts.go`, `agentDirs` and `tools.json` (through the
  id map) is in the registry.
- Ids and aliases are unique, lower-case and kebab-case.
- Every user directory starts with `~/`. Every project directory is relative,
  clean, and has no `..`, no leading `/` and no `~`.
- Every shape is valid for its kind, a `doc-file` names its files, and a file
  name has no `/` or wildcard. A hook is `json-key` (with a `key`), `json-file` or
  `unsupported`, its entries are `.json` files, and only `claude-code` and `codex`
  are read.
- No two harnesses share a command, agent or prompt directory. Skill and
  document directories may be shared.
- This page names every harness.
- The embedded file equals a fresh run of the generator.

## Supported harnesses

Hand-written means the overlay adds directories beyond what the CLI sources
record. The document column lists the file names; the directories are in
`harnesses.json`. An entry marked "unsupported format" is reported and not read.

| ID | Name | Format | Source | Commands | Agents | Prompts | Skills | Documents | Hooks |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `adal` | AdaL | `generic-md` | generated |  |  |  | `~/.adal/skills`, `.adal/skills` | AGENTS.md |  |
| `aider` | Aider | `generic-md` | generated |  |  |  | `.aider/skills` |  |  |
| `aider-desk` | AiderDesk | `generic-md` | generated |  |  |  | `~/.aider-desk/skills` |  |  |
| `amazon-q` | Amazon Q Developer | `generic-md` | hand-written |  |  | `~/.aws/amazonq/prompts`, `.amazonq/prompts` | `.amazonq/skills` | AGENTS.md |  |
| `amp` | Amp | `generic-md` | hand-written | `~/.config/amp/commands`, `.agents/commands` |  |  | `~/.config/agents/skills`, `.agents/skills`, `.amp/skills` | AGENTS.md |  |
| `antigravity` | Google Antigravity | `generic-md` | hand-written | `~/.gemini/antigravity/global_workflows`, `.agents/workflows`, `.agent/workflows` |  |  | `~/.gemini/antigravity/skills`, `.agents/skills` | AGENTS.md, GEMINI.md |  |
| `antigravity-cli` | Antigravity CLI | `generic-md` | generated |  |  |  | `~/.gemini/antigravity-cli/skills` |  |  |
| `astrbot` | AstrBot | `generic-md` | generated |  |  |  | `~/.astrbot/data/skills` |  |  |
| `augment` | Augment | `generic-md` | hand-written | `~/.augment/commands`, `.augment/commands` |  |  | `~/.augment/skills`, `.augment/skills` | .augment-guidelines, AGENTS.md |  |
| `autohand-code` | Autohand Code | `generic-md` | generated |  |  |  | `~/.autohand/skills` |  |  |
| `bob` | IBM Bob | `generic-md` | generated |  |  |  | `~/.bob/skills`, `.bob/skills` | AGENTS.md |  |
| `claude-code` | Claude Code | `claude-code` | hand-written | `~/.claude/commands`, `.claude/commands` | `~/.claude/agents`, `.claude/agents` |  | `~/.claude/skills`, `.claude/skills` | CLAUDE.md, AGENTS.md, CLAUDE.local.md | `~/.claude/settings.json`, `.claude/settings.json`, `.claude/settings.local.json` |
| `cline` | Cline | `cline` | hand-written | `~/Documents/Cline/Workflows`, `.clinerules/workflows` |  |  | `~/.agents/skills`, `~/.cline/skills`, `.cline/skills` | .clinerules, AGENTS.md | `.clinerules/hooks` (unsupported format) |
| `codearts-agent` | CodeArts Agent | `generic-md` | generated |  |  |  | `~/.codeartsdoer/skills` |  |  |
| `codebuddy` | CodeBuddy | `generic-md` | generated |  |  |  | `~/.codebuddy/skills`, `.codebuddy/skills` | AGENTS.md |  |
| `codebuff` | Codebuff | `generic-md` | generated |  |  |  | `.codebuff/skills` | AGENTS.md |  |
| `codemaker` | CodeMaker | `generic-md` | generated |  |  |  | `~/.codemaker/skills` |  |  |
| `codestudio` | CodeStudio | `generic-md` | generated |  |  |  | `~/.codestudio/skills` |  |  |
| `codex` | OpenAI Codex | `codex` | hand-written |  |  | `~/.codex/prompts` | `~/.agents/skills`, `~/.codex/skills`, `.agents/skills`, `.codex/skills` | AGENTS.md, AGENTS.override.md | `~/.codex/hooks.json`, `.codex/hooks.json` |
| `command-code` | Command Code | `generic-md` | generated |  |  |  | `~/.commandcode/skills`, `.commandcode/skills` | AGENTS.md |  |
| `continue` | Continue | `generic-md` | hand-written |  |  | `~/.continue/prompts`, `.continue/prompts` (unsupported format) | `~/.continue/skills`, `.continue/skills` | AGENTS.md |  |
| `cortex` | Snowflake Cortex Code | `generic-md` | generated |  |  |  | `~/.snowflake/cortex/skills`, `.cortex/skills` | AGENTS.md |  |
| `crush` | Crush | `generic-md` | generated |  |  |  | `~/.config/crush/skills`, `.crush/skills` | CRUSH.md, AGENTS.md |  |
| `cursor` | Cursor | `cursor` | hand-written | `~/.cursor/commands`, `.cursor/commands` |  |  | `~/.agents/skills`, `~/.cursor/skills`, `.agents/skills`, `.cursor/skills` | .cursorrules, AGENTS.md | `.cursor/hooks.json` (unsupported format) |
| `deepagents` | Deep Agents | `generic-md` | generated |  |  |  | `~/.deepagents/agent/skills` |  |  |
| `devin` | Devin | `generic-md` | generated |  |  |  | `~/.config/devin/skills` |  |  |
| `dexto` | Dexto | `generic-md` | generated |  |  |  | `~/.agents/skills` |  |  |
| `droid` | Factory Droid | `generic-md` | hand-written | `~/.factory/commands`, `.factory/commands` | `~/.factory/droids`, `.factory/droids` |  | `~/.factory/skills`, `.factory/skills` | AGENTS.md |  |
| `firebender` | Firebender | `generic-md` | generated |  |  |  | `~/.firebender/skills` |  |  |
| `forgecode` | ForgeCode | `generic-md` | hand-written | `~/.forge/commands`, `.forge/commands` | `~/.forge/agents`, `.forge/agents` |  | `~/.forge/skills`, `.forge/skills` | AGENTS.md |  |
| `g3` | g3 | `generic-md` | generated |  |  |  | `.g3/skills` |  |  |
| `gemini-cli` | Gemini CLI | `gemini-cli` | hand-written | `~/.gemini/commands`, `.gemini/commands` (unsupported format) |  |  | `~/.agents/skills`, `~/.gemini/skills`, `.agents/skills`, `.gemini/skills` | GEMINI.md, AGENTS.md |  |
| `github-copilot` | GitHub Copilot | `copilot` | hand-written |  | `~/.copilot/agents`, `.github/agents` | `.github/prompts` | `~/.copilot/skills`, `.github/skills`, `.copilot/skills` | copilot-instructions.md, AGENTS.md |  |
| `goose` | Goose | `generic-md` | hand-written |  |  |  | `~/.config/goose/skills`, `.goose/skills` | .goosehints, AGENTS.md |  |
| `gptme` | gptme | `generic-md` | generated |  |  |  | `.gptme/skills` | AGENTS.md |  |
| `hermes-agent` | Hermes Agent | `generic-md` | generated |  |  |  | `~/.hermes/skills` |  |  |
| `iflow-cli` | iFlow CLI | `generic-md` | hand-written | `~/.iflow/commands`, `.iflow/commands` (unsupported format) |  |  | `~/.iflow/skills`, `.iflow/skills` | IFLOW.md, AGENTS.md |  |
| `inference-sh` | inference.sh | `generic-md` | generated |  |  |  | `~/.inferencesh/skills` |  |  |
| `jazz` | Jazz | `generic-md` | generated |  |  |  | `~/.jazz/skills` |  |  |
| `jetbrains-ai-assistant` | JetBrains AI Assistant | `generic-md` | generated |  |  |  |  | AGENTS.md |  |
| `junie` | Junie | `generic-md` | generated |  |  |  | `~/.junie/skills`, `.junie/skills` | guidelines.md, AGENTS.md |  |
| `kilo` | Kilo Code | `generic-md` | hand-written | `~/.kilocode/workflows`, `.kilocode/workflows` |  |  | `~/.kilocode/skills`, `.kilocode/skills` | AGENTS.md |  |
| `kimi-code-cli` | Kimi Code CLI | `generic-md` | generated |  |  |  | `~/.agents/skills`, `.kimi/skills` | AGENTS.md |  |
| `kiro-cli` | Kiro | `generic-md` | generated |  |  |  | `~/.kiro/skills`, `.kiro/skills` | AGENTS.md | `.kiro/hooks` (unsupported format) |
| `kode` | Kode | `generic-md` | generated |  |  |  | `~/.kode/skills`, `.kode/skills` | AGENTS.md |  |
| `letta-code` | Letta Code | `generic-md` | generated |  |  |  | `.letta/skills` | AGENTS.md |  |
| `lingma` | Tongyi Lingma | `generic-md` | generated |  |  |  | `~/.lingma/skills` | AGENTS.md |  |
| `loaf` | Loaf | `generic-md` | generated |  |  |  | `~/.agents/skills` |  |  |
| `mcpjam` | MCPJam | `generic-md` | generated |  |  |  | `~/.mcpjam/skills`, `.mcpjam/skills` | AGENTS.md |  |
| `mistral-vibe` | Mistral Vibe | `generic-md` | generated |  |  |  | `~/.vibe/skills`, `.vibe/skills` | AGENTS.md |  |
| `moxby` | Moxby | `generic-md` | generated |  |  |  | `~/.moxby/skills` |  |  |
| `mux` | Mux | `generic-md` | generated |  |  |  | `~/.mux/skills`, `.mux/skills` | AGENTS.md |  |
| `nanocoder` | Nanocoder | `generic-md` | generated |  |  |  |  | AGENTS.md |  |
| `neovate` | Neovate | `generic-md` | generated |  |  |  | `~/.neovate/skills`, `.neovate/skills` | AGENTS.md |  |
| `octofriend` | Octofriend | `generic-md` | generated |  |  |  |  | OCTO.md |  |
| `ona` | Ona | `generic-md` | generated |  |  |  | `~/.ona/skills` |  |  |
| `openclaw` | OpenClaw | `generic-md` | generated |  |  |  | `~/.openclaw/skills`, `.openclaw/skills` | AGENTS.md |  |
| `opencode` | opencode | `opencode` | hand-written | `~/.config/opencode/command`, `~/.config/opencode/commands`, `.opencode/command`, `.opencode/commands` | `~/.config/opencode/agent`, `~/.config/opencode/agents`, `.opencode/agent`, `.opencode/agents` |  | `~/.config/opencode/skills`, `.opencode/skills` | AGENTS.md |  |
| `openhands` | OpenHands | `generic-md` | generated |  |  |  | `~/.openhands/skills`, `.openhands/skills` | AGENTS.md |  |
| `pi` | Pi | `generic-md` | hand-written |  |  | `~/.pi/agent/prompts`, `.pi/prompts` | `~/.agents/skills`, `~/.pi/agent/skills`, `.agents/skills`, `.pi/skills` | AGENTS.md |  |
| `pochi` | Pochi | `generic-md` | generated |  |  |  | `~/.pochi/skills`, `.pochi/skills` | AGENTS.md |  |
| `promptscript` | PromptScript | `generic-md` | generated |  |  |  | `.agents/skills` |  |  |
| `qoder` | Qoder | `generic-md` | generated |  |  |  | `~/.qoder/skills`, `.qoder/skills` | AGENTS.md |  |
| `qoder-cn` | Qoder CN | `generic-md` | generated |  |  |  | `~/.qoder-cn/skills` |  |  |
| `qwen-code` | Qwen Code | `generic-md` | hand-written | `~/.qwen/commands`, `.qwen/commands` (unsupported format) |  |  | `~/.qwen/skills`, `.qwen/skills` | QWEN.md, AGENTS.md |  |
| `reasonix` | Reasonix | `generic-md` | generated |  |  |  | `~/.reasonix/skills` |  |  |
| `replit` | Replit | `generic-md` | generated |  |  |  | `~/.config/agents/skills` |  |  |
| `roo` | Roo Code | `generic-md` | hand-written | `~/.roo/commands`, `.roo/commands` |  |  | `~/.roo/skills`, `.roo/skills` | .roorules, AGENTS.md |  |
| `rovodev` | Rovo Dev | `generic-md` | generated |  |  |  | `~/.rovodev/skills` |  |  |
| `sourcegraph-cody` | Sourcegraph Cody | `generic-md` | generated |  |  |  | `.cody/skills` | AGENTS.md |  |
| `tabnine` | Tabnine | `generic-md` | generated |  |  |  | `.tabnine/skills` |  |  |
| `tabnine-cli` | Tabnine CLI | `generic-md` | generated |  |  |  | `~/.tabnine/agent/skills` |  |  |
| `terramind` | TerraMind | `generic-md` | generated |  |  |  | `~/.terramind/skills` |  |  |
| `tinycloud` | TinyCloud | `generic-md` | generated |  |  |  | `~/.tinycloud/skills` |  |  |
| `trae` | Trae | `generic-md` | generated |  |  |  | `~/.trae/skills`, `.trae/skills` | AGENTS.md |  |
| `trae-cn` | Trae CN | `generic-md` | generated |  |  |  | `~/.trae-cn/skills`, `.trae/skills` | AGENTS.md |  |
| `traycer` | Traycer | `generic-md` | generated |  | `.traycer/cli-agents` |  |  |  |  |
| `universal` | Universal agents directory | `generic-md` | generated |  |  |  | `~/.config/agents/skills` |  |  |
| `vtcode` | VT Code | `generic-md` | generated |  | `.vtcode/agents` |  | `.vtcode/skills` | AGENTS.md |  |
| `warp` | Warp | `generic-md` | generated |  |  |  | `~/.agents/skills` | WARP.md |  |
| `windsurf` | Windsurf | `windsurf` | hand-written | `~/.codeium/windsurf/global_workflows`, `.windsurf/workflows` |  |  | `~/.codeium/windsurf/skills`, `.windsurf/skills` | .windsurfrules, AGENTS.md, global_rules.md | `.windsurf/hooks.json` (unsupported format) |
| `zed` | Zed | `generic-md` | generated |  |  |  | `~/.agents/skills`, `.agents/skills`, `.zed/skills` | .rules, AGENT.md, AGENTS.md |  |
| `zencoder` | Zencoder | `generic-md` | generated |  |  |  | `~/.zencoder/skills`, `.zencoder/skills` | AGENTS.md |  |
| `zenflow` | Zencoder Zenflow | `generic-md` | generated |  |  |  | `~/.zencoder/skills` |  |  |
