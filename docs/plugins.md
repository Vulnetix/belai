# Plugins

**Status:** Roadmap. This feature is designed but not yet in a release, so the
page describes the planned behaviour.

A plugin bundles skills, hooks, saved prompts and agent profiles so they can be
shared and installed together from a git repository or a local directory.

- [Manifest](#manifest)
- [Installing](#installing)
- [How components load](#how-components-load)
- [Project-proposed plugins](#project-proposed-plugins)
- [Security model](#security-model)
- [Commands](#commands)

## Manifest

A plugin has `signet-plugin.json` at its root:

```json
{
  "name": "go-team",
  "version": "1.2.0",
  "description": "Release skill, vet hook and review prompts for our Go services",
  "skills": ["skills/release"],
  "hooks": ["hooks/vet.json"],
  "prompts": ["prompts"],
  "agents": ["agents/reviewer.json"]
}
```

Every path is relative to the plugin root and must stay inside it. Each
component is checked with the same validator Signet uses for its own files:
skill front matter, the hook schema, prompt names, and the agent profile
schema. A plugin with any invalid component does not install.

## Installing

```bash
signet plugin install https://github.com/example/go-team
signet plugin install ./local/plugin
```

or `/plugin install <url|path>` in the TUI.

1. Signet clones the repository with a fixed git command: no submodules, git
   hooks disabled, the scrubbed environment.
2. It validates the manifest and every component.
3. It shows you the full list of what will be added, including each hook's
   event and command, and asks you to confirm. From the CLI, pass `-yes`;
   without a terminal and without `-yes`, install fails.
4. The plugin is stored at `~/.vulnetix/signet/plugins/<name>@<commit>`,
   pinned to that commit.

`update` fetches, shows what changed between the pinned commit and the new one,
and asks again before moving the pin.

## How components load

Components from enabled plugins are loaded next to your own, namespaced by the
plugin name: the skill `release` from `go-team` is `go-team:release`, and its
prompts appear as `/prompt:go-team:<name>`. Your own files win on a name clash.

## Project-proposed plugins

A repository may list plugins it recommends in `.vulnetix/settings.json`:

```json
{ "plugins": ["https://github.com/example/go-team@4f2c9e1"] }
```

This works like `workspace_dirs`: the list is only a proposal. It is dropped
unless you have set `allow_project_plugins` in your global settings, and even
then each plugin is installed only after you accept it by name in the trust
prompt. A plugin the project adds later prompts again.

## Security model

- A plugin's hooks are executables. They are shown by command at install and
  must resolve inside the plugin's directory, as your own hooks must resolve
  inside the hooks directory.
- A plugin's skill bodies and hook output are third-party text and are
  classified (see [skills](skills.md) and [hooks](hooks.md)).
- Plugins cannot add providers, credentials, MCP servers, settings, or
  permission rules.

## Commands

| Command | Does |
| --- | --- |
| `/plugin list` | installed plugins, version, commit, enabled state |
| `/plugin install <url\|path>` | install after confirmation |
| `/plugin update <name>` | fetch, show the change, confirm, move the pin |
| `/plugin disable <name>` / `enable` | stop or resume loading its components |
| `/plugin remove <name>` | delete it |

The same subcommands exist as `signet plugin …`.
