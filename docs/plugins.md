# Plugins

**Status:** alpha-20260926. Shipped in an early form; the manifest may still
change.

A plugin bundles skills, hooks, saved prompts and agent profiles, so they can
be shared and installed together from a git repository or a local directory.

- [Manifest](#manifest)
- [Installing](#installing)
- [How components load](#how-components-load)
- [Security model](#security-model)
- [Commands](#commands)
- [Limitations](#limitations)

## Manifest

A plugin has `signet-plugin.json` at its root:

```json
{
  "name": "go-team",
  "version": "1.2.0",
  "description": "Release skill, vet hook and review prompts for our Go services",
  "skills": ["skills"],
  "hooks": ["hooks"],
  "prompts": ["prompts"],
  "agents": ["agents"]
}
```

Each component list names directories relative to the plugin root:

| Key | Directory holds |
| --- | --- |
| `skills` | `<name>/SKILL.md` [skills](skills.md) |
| `hooks` | `*.json` [hook files](hooks.md), with the commands they run |
| `prompts` | `*.md` prompts; the file name (lowercase letters, digits, hyphens) is the prompt name |
| `agents` | `*.json` [agent profiles](agent-profiles.md) |

`name` must be lowercase letters, digits and hyphens (at most 40), and not
`signet`, `user` or `builtin`. Any other manifest key fails validation. Every
component path must be a directory inside the plugin, after following
symlinks. Each component is checked with the validator Signet uses for your
own files, and one invalid component fails the whole plugin.

## Installing

```bash
signet plugin install https://github.com/example/go-team
signet plugin install https://github.com/example/go-team#v1.2.0
signet plugin install ./local/plugin
```

1. Signet fetches the plugin into a staging directory. A git source is cloned
   with a fixed command: no submodules, repository hooks disabled, the
   `file://` transport refused, no credential prompts, and the scrubbed
   environment. A `#ref` suffix checks out that tag, branch or commit. A local
   directory is copied without `.git`, symlinks or special files.
2. It validates the manifest and every component.
3. It prints everything the plugin would add, including each hook's event
   and the command it runs, and asks you to confirm. Without a terminal it
   installs only with `-yes`, and still prints the listing.
4. The plugin is stored at `~/.vulnetix/signet/plugins/<name>/`, and
   `plugins.json` beside it records the source and the commit it is pinned to
   (`local` for a directory copy).

`signet plugin update <name>` fetches the recorded source again (or a new one
given after the name), prints the new listing with a count of what the
installed version holds, and asks again before replacing it.

## How components load

Enabled plugins load next to your own files, namespaced by plugin name, so
they never shadow yours or Signet's:

| Component | Appears as |
| --- | --- |
| skill `release` | `go-team:release` in the skill list and the `Skill` tool |
| hook `vet` | `go-team:vet`, running from its own directory in the plugin |
| prompt `review.md` | `/prompt:go-team:review` in the slash popup |
| agent profile `reviewer` | `go-team:reviewer` in the agent strip and `/agent` |

Changes to plugins apply to sessions built afterwards; `/clear` starts one.

## Security model

- A plugin's hooks are executables. Install lists each one with its command,
  and each command must resolve inside the hook's directory in the plugin,
  exactly as your own hooks must resolve inside the hooks directory.
- Skill bodies and hook output from a plugin are classified like any other
  (`skill` and `hook` tool kinds).
- A plugin cannot add providers, credentials, MCP servers, settings or
  permission rules; the manifest has no key for them.
- Install and enable are yours alone. Plugins live in the global state
  directory, and nothing in a repository's settings can install, enable or
  propose one.
- Text shown at install (names, descriptions, commands) is stripped of
  control and bidi characters first.

## Commands

| CLI | TUI | Does |
| --- | --- | --- |
| `signet plugin list` | `/plugin` or `/plugin list` | installed plugins, version, state and commit |
| `signet plugin install [-yes] <url\|dir>` | | install after confirming |
| `signet plugin update [-yes] <name> [source]` | | fetch, show, confirm, replace |
| `signet plugin enable <name>` / `disable <name>` | `/plugin enable <name>` / `disable <name>` | stop or resume loading its components |
| `signet plugin remove <name>` | `/plugin remove <name>` | delete it |

Install and update run from the CLI only, because they need you to read the
listing before confirming.

## Limitations

- There is no plugin index or search; install from a URL or directory you
  trust.
- A repository cannot recommend plugins yet.
