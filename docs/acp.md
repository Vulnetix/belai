# Editor integration (ACP)

**Status:** alpha-20260926. Shipped in an early form; the supported methods
may still change.

`belai acp` runs Belai as an [Agent Client
Protocol](https://agentclientprotocol.com) agent on stdin and stdout. Editors
that speak ACP (Zed, JetBrains IDEs, Neovim, Emacs and VS Code through a
plugin or extension) can then use Belai as their agent, with the same
classifier, permission rules, guardrails and budgets as the TUI.

- [Editor setup](#editor-setup)
- [What is supported](#what-is-supported)
- [Security model](#security-model)
- [Limitations](#limitations)
- [Edge cases](#edge-cases)

## Editor setup

Zed, in `settings.json`:

```json
{
  "agent_servers": {
    "Belai": {
      "command": "belai",
      "args": ["acp"]
    }
  }
}
```

Step-by-step tutorials: [Zed](acp-zed.md), [JetBrains IDEs](acp-jetbrains.md),
[Neovim](acp-neovim.md), [Emacs](acp-emacs.md) and [VS Code](acp-vscode.md).
Any other ACP client takes the same command. `belai acp -provider <name> -model <id>`
picks a provider and model; otherwise they resolve as they do for the TUI
(settings, `BELAI_PROVIDER`, available credentials). Everything else comes
from your normal Belai settings for the project directory.

## What is supported

| ACP method or update | Belai behaviour |
| --- | --- |
| `initialize` | protocol version 1; embedded file context and images accepted, audio not; no session loading; advertises `sessionCapabilities` `list` and `close`; reads `clientInfo` to name sessions; advertises no MCP transport (`http` and `sse` are both false) |
| `authenticate` | answered with an empty result. `initialize` advertises no `authMethods`, because Belai signs in through its own credentials, so a client has nothing to authenticate with |
| `session/new` | starts a session in the editor's project directory |
| `session/prompt` | runs one turn; text, file links and embedded file text become the prompt, and `image` blocks are attached as images |
| `session/cancel` | stops the turn; the prompt returns `cancelled` |
| `session/list` | the sessions open on this connection, newest first, optionally for one absolute `cwd`; no paging, and nothing from the session store |
| `session/set_config_option` | the editor's model picker and mode picker, both offered in `configOptions` on `session/new`. The `model` option lists the models of every provider you have credentials for, plus the one the session runs on; a pick must be one of the listed values, lasts for the session and is never saved to your settings. The session is rebuilt through the same builder as at start (trust check, permission rules, sandbox and classifier all apply again) and keeps its conversation. A change during a running turn is refused. Classifier and routing settings are not exposed. The same call flips three on/off options, `guardrails`, `ask` (off allows a call the rules would ask about) and `caveman`. They start from your settings, last for the session, are never saved, and rebuild the session like a model pick. Turning guardrails or ask off is your choice made in the editor, so use an editor you trust |
| `session/set_mode` | the editor's mode picker: `auto` (Belai chooses per prompt, the default), `agent`, `plan` (read only, no shell) or `goal`. A chosen mode engages that mode's own tool surface and gates, and the change is echoed as `current_mode_update` |
| `session/close` | stops a running turn, flushes the transcript and forgets the session |
| `available_commands_update` | sent once `session/new` has answered: the `/tree` command (see [Session tree](#session-tree)) |
| `session_info_update` | after each turn: a harness title (editor, folder, turn count) and the time |
| `agent_message_chunk` | streamed reply text. The first turn of a session opens with the TUI's header as markdown: `belai` and its tagline, then the version, build, provider and model |
| `agent_thought_chunk` | streamed reasoning, plus short status lines while Belai works before the first token: checking the prompt, retrying the model, the pass number, files read for context, the chosen mode, and "Still working" after ten quiet seconds. Status lines are fixed templates; provider error text never appears in them |
| `tool_call` | each tool call as it starts, with its kind (`read`, `edit`, `search`, `execute`, `fetch`, `think`, `other`) and arguments |
| `tool_call_update` | the file diff a call made, then its result, `completed` or `failed` |
| `tool_call` (pending) | a tool call announced from its first streamed fragment, filled in when it starts |
| `tool_call` for a subagent | each explore or `Task` subagent as a `think` call, with one-line activity (tool and argument excerpt) and its final state |
| `plan` | the goal-mode todo list |
| `session/request_permission` | a permission ask, offering allow once, allow for this session, or reject |

A turn stops with `end_turn`, `cancelled`, or `refusal` when the prompt
classifier refuses it.

## Security model

- Each session keeps a private transcript in the state directory, like a
  headless run (`belai acp -no-transcript` opts out): the turns, the tool
  calls and every role-manager decision, never anything on stdout. The first
  session on a connection also receives the role-manager decisions made in the
  process, which is normally the only session.
- A session is named after the editor that started it. The editor's declared
  name (`clientInfo.title`, else `name`) is cleaned to one short line and
  prefixed in square brackets, so a session reads `[VS Code] explore repo`; the
  first prompt's first line follows it. An editor that declares no name gets
  no prefix.
- With `sync.enabled` on and the Vulnetix CLI logged in, the transcript is
  mirrored to your Vulnetix account like a TUI session, through the same
  syncer and credential. The mirror is upload only: a prompt or answer typed on
  the website never reaches an editor session, because the editor owns the
  conversation. `-no-transcript` keeps nothing and so mirrors nothing.
- The provider and model follow your settings, then the model you last chose
  in the TUI, like a fleet worker; `-provider` and `-model` override them.
- The project directory must already be trusted. `session/new` in an
  untrusted directory fails with a message telling you to open it in the TUI
  once or run `belai -trust-dir` there. The trust prompt never runs over ACP.
- Each ACP session is built the way the headless CLI builds one: the same
  classifier, posture gates, permission rules, sandbox and token budgets. The
  guardrails switch follows your settings for that directory.
- Permission asks go to the editor. "Allow for this session" is remembered in
  memory for that session and tool only, and never written to a settings
  file. Any error, cancellation or other answer denies.
- MCP servers come from your global settings. Servers an editor offers in
  `session/new` are ignored.
- The [post-end test pass](testing.md) runs after a prompt turn that completes a
  goal, when your `tests.post_end` setting asks for it. An editor has no
  session-end moment to watch, so a completed goal is the only trigger over ACP.
  The result streams as agent messages, a failing run's fix loop streams like
  any turn with its permission asks going to the editor, and cancelling the
  prompt cancels the suites.

## Limitations

- Sessions last as long as the editor's connection. Each keeps a transcript in
  the Belai session store, named after the editor, but the editor cannot load
  one back (`loadSession` is false).
- Clarifying questions, the harness's and the model's `AskUserQuestion`, are
  answered as declined over ACP; the agent proceeds with its
  best reading of the prompt.
- Audio in prompts is not accepted. Images are: see below.
- `/tree` is the only slash command, and the TUI panels are not exposed.
  `/tree fork <id>` is TUI only: a forked session could not be opened from the
  editor without `loadSession`.

## Session tree

A session is a tree: each transcript entry points at the one before it.
`/tree` lists the branches as a code block, one row per user message or final
reply, each starting with a short id. `●` marks where the session is, `•` the
branch it is on, `○` other branches and `┬` a point where a branch was taken.

`/tree <id>` continues from that row and answers with where it went:

- A reply row continues from that reply.
- A user row continues from just before it, and Belai echoes the prompt so it
  can be reworded and sent again.

The transcript only grows: Belai appends a `branch` entry, and later entries
hang from the chosen row. The model then sees the history up to that point and
nothing from the abandoned branch. That history is the one kept when each turn
finished, so it holds the same cleaned prompts the model was given, never the
transcript's raw text. Only turns that finished on this connection can be
returned to. Files on disk are not rolled back.

`/tree` and `/tree <id>` are answered by Belai and never reach the model. While
a prompt is running they ask you to cancel it first.

## Edge cases

- A second `session/prompt` on a session whose turn is still running is
  refused.
- An empty prompt, an unknown session id, or a relative `cwd` is refused
  with invalid-params.
- A permission ask the editor fails to answer, answers with `cancelled`, or
  answers with an unknown option denies.
- "Allow for this session" covers that tool name only, and ends with the
  session.
- Cancelling a turn returns `cancelled` even when the turn also failed.
- A method outside the table is refused with method-not-found, naming the method.
- `session/new` needs an absolute `cwd`; a relative one is refused with
  invalid-params before any session is built.

## Images

Belai advertises `promptCapabilities.image: true`. An `image` content block in
`session/prompt` (`data` is base64, `mimeType` is the type the editor declares)
attaches an image to the turn under the same rules as an image attached in the
TUI (see [Images](image-attachments.md)).

- **Admitted by `internal/imageguard`, never the classifier.** The bytes must
  decode as PNG or JPEG inside the size and pixel budget and are re-encoded as
  a new PNG. The declared `mimeType` is not trusted: the decoder decides what
  the bytes are. The base64 length is checked before anything is decoded, so
  an oversize payload is refused without being allocated.
- **A block with only a `uri` is refused.** Belai never fetches an address an
  editor names.
- **At most eight images per prompt**, numbered `image-1.png`, `image-2.png`
  in the order they were admitted (a refused image does not use a number).
- **A refused image does not fail the prompt.** The prompt runs, and the model
  is told, in a sealed harness directive, which images were not admitted and
  why (the reason is cleaned and capped, and the refused bytes are never
  echoed). When the prompt has no text and no image could be admitted, the
  call fails with `invalid params: no image could be admitted: ...`.
- **An image-only prompt runs.** Its text is the harness placeholder
  `(image attached)`, so no words are put in the editor user's mouth.
- **Same gates.** The prompt text is admitted as before, the permission rules,
  sandbox and budgets apply, and a model without image input (a custom
  provider's `images` declaration, or `models.Vision`) is told the image was not
  sent instead of receiving it.
- **The sending turn only.** The image rides that prompt and its tool loop and
  is not replayed on later prompts. The private transcript records the image
  as a marker (name, type, size, dimensions, tokens) on the user entry, never
  the bytes.
