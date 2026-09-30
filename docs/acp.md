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
| `initialize` | protocol version 1; embedded file context and images accepted, audio not; no session loading |
| `session/new` | starts a session in the editor's project directory |
| `session/prompt` | runs one turn; text, file links and embedded file text become the prompt, and `image` blocks are attached as images |
| `session/cancel` | stops the turn; the prompt returns `cancelled` |
| `agent_message_chunk` | streamed reply text |
| `agent_thought_chunk` | streamed reasoning |
| `tool_call` | each tool call as it starts, with its kind (`read`, `edit`, `search`, `execute`, `fetch`, `think`, `other`) and arguments |
| `tool_call_update` | the file diff a call made, then its result, `completed` or `failed` |
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

- Sessions last as long as the editor's connection and are not written to
  the Belai session store, so they cannot be resumed from the TUI.
- Clarifying questions, the harness's and the model's `AskUserQuestion`, are
  answered as declined over ACP; the agent proceeds with its
  best reading of the prompt.
- Audio in prompts is not accepted. Images are: see below.
- Slash commands, modes and the TUI panels are not exposed.

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
