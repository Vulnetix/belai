# Vulnetix | Signet

A safer LLM coding harness.

Every delimiter carries a cryptographic nonce and integrity hash, untrusted content is sanitised, and a role manager classifies untrusted inputs while segmenting benign tasks from risky agentic actions.

It pairs a safety-first architecture with multiple interaction modes (agent, plan, goal) let you choose the right level of autonomy for the task.
Inspiration for agents is taken from harnesses like Hermes, while the agentic loop and context management is designed to minimise token usage while maximising model alignment over longer sessions.
This is not a model provider coding harness that incentivises token maxing, or a tool used as a gimmick "look! it has agency!".
No, Signet holds models to sensible constraints and the result is a tool that leaves an impression of confidence and assurance.

## Installation

### macOS & Linux (Homebrew)

```bash
brew install vulnetix/tap/signet
```

Homebrew also knows an unrelated cask called `signet`, so upgrade by the tapped
name to avoid the ambiguity:

```bash
brew upgrade --formula vulnetix/tap/signet
```

### Windows (Scoop)

```powershell
scoop bucket add vulnetix https://github.com/Vulnetix/scoop-bucket
scoop install signet
```

### Shell installer

```bash
curl -fsSL https://raw.githubusercontent.com/Vulnetix/signet/main/install.sh | sh
```

The script detects your platform, verifies the download against the release
`checksums.txt`, and refuses to install on a mismatch. It installs to
`/usr/local/bin`, falling back to `~/.local/bin` when that is not writable.

```bash
# choose the directory or the version
curl -fsSL https://raw.githubusercontent.com/Vulnetix/signet/main/install.sh | sh -s -- --install-dir ~/.local/bin
curl -fsSL https://raw.githubusercontent.com/Vulnetix/signet/main/install.sh | sh -s -- --version v0.1.1
```

### GitHub Releases

Pre-built binaries for Linux, macOS, and Windows are available on the [Releases](https://github.com/vulnetix/signet/releases) page.

### Build from source

Signet is pure Go with no cgo, so a clean build needs only a Go toolchain.

```bash
git clone https://github.com/vulnetix/signet.git
cd signet
go build -o signet ./cmd/signet
./signet -version
```

To install it onto your `PATH` instead:

```bash
go install github.com/vulnetix/signet/cmd/signet@latest
```

A binary built this way reports its version as `dev`. To stamp the real version — and to get the same flags the release builds use — install [just](https://just.systems) and run `just build` or `just install` from a clone. See [docs/development.md](docs/development.md).

## Usage

Run `signet` in a project directory to start the terminal UI:

```bash
cd ~/code/my-project
signet
```

Inside the UI, `/` opens slash-command autocomplete — `/credentials` to configure providers, `/model` to pick provider/model/effort, `/classifier` to pick the role manager's own classifier model, `/settings` to edit settings, `/permissions` to edit tool rules, `/prompts` to manage the prompt library, `/mode` to set the operating mode, `/todos`, `/profile`, `/agent` to create, edit, and run background agents, `/vulnetix` (with `run`, `configure`, `list`, and `status` subcommands), `/compact` to summarise a long session into a new one, `/clear` (or `/new`) to start a fresh session, `/yolo` to turn guardrails and the ask gate off together (`/yolo off` restores the settings-file values), and `/rename` to name the session. `/help` lists every command and keyboard shortcut.

Operator safety controls live in the footer: `guardrails: on|off` (posture gates) and `ask: on|off` (the permission-ask gate). `f3` toggles guardrails, `f4` toggles ask, and when both are off the two chips collapse into a single gold `YOLO`. These are explicit opt-ins: turning them off is announced in the transcript and traced under `SIGNET_TRACE`. Guardrails off sets every posture gate to `ignore` across every surface — the agent loop, inline `!cmd`, `@file` attachments, background agents and the CLI — and the classifier is then not called at all rather than called and ignored, so a turn costs no extra requests. Sanitising is not part of the switch: delimiter markup is stripped either way. Next to them the footer always states `caveman: on|off`, so the voice rewrite (`f2`) can never be on without saying so.

In the composer, `ctrl+left` and `ctrl+right` move the cursor by word, crossing into the neighbouring line at a line boundary, and `home`/`end` (`fn+left`/`fn+right`) jump to the ends of the line. A word stops at punctuation, so `foo.bar` is three hops and `foo_bar` is one.

Shortcuts use `ctrl`, `shift` and the function-key row — never `alt`. `alt` chords are unreliable across terminals, and under the kitty keyboard protocol a `ctrl+alt+<key>` press is indistinguishable from `ctrl+<key>` by the time it reaches the UI, so it could never have worked. The session toggles are `f2` caveman, `f3` guardrails, `f4` ask, `f5` cycle mode, and `f6` cycle reasoning effort — all five from any screen — `f7` saves the prompt to the library from the composer, and `ctrl+s` saves the hovered panel, overwrites/deletes a loaded library prompt, or saves the prompt. If your terminal eats a function key, `/settings`, `/yolo`, `/model` and `/mode` do the same jobs.

In agent mode a strip above the prompt lists the agents that can carry your turns — your own profiles, the background-agent definitions (`↻`), and the built-ins (`◈`). `tab` moves the highlight, `enter` engages, `ctrl+g` starts a `↻` definition in the background instead, and typing `@name` filters the strip. The engaged agent shows in the footer chip and carries every turn until you pick another or `(none)`. It applies to agent mode only: plan and goal mode run Signet's own logic and cannot be steered by an agent.

`shift+tab` cycles the mode. **Goal mode** is the one that keeps going: instead of stopping when the tool budget runs out, an evaluator checks whether the work advanced and grants another pass while it does, tracking a todo list in a panel above the prompt. It is stopped by a stall, not a counter — press `esc` (or `ctrl+c` outside the UI) to stop it and keep the partial result. Set `resilience.max_passes` if you want a hard ceiling. The rules are in [docs/role-manager.md](docs/role-manager.md).

For a single answer without the UI:

```bash
signet -prompt "what does internal/run do?"
signet -provider anthropic -model claude-sonnet-4-5 -prompt "review this diff"
```

| Flag | Meaning |
| --- | --- |
| `-prompt` | send one turn, print the reply, exit |
| `-provider` | `openai`, `anthropic`, `cloudflare-workers-ai`, `cloudflare-ai-gateway`, `openrouter`, `google-gemini`, `ollama`, `github-copilot`, `huggingface`, or a custom name from `settings.json` |
| `-model` | model id; each provider has a default |
| `-effort` | thinking-effort level: `low`, `medium`, or `high` |
| `-caveman` | enable caveman voice rewrite for this run |
| `-guardrails` | posture guardrails (default on); `-guardrails=false` turns every gate off for this run |
| `-ask-permission` | the permission-ask gate (default on); `-ask-permission=false` resolves asks to allow |
| `-tools` | enable tool execution for this run |
| `-session-retention-days` | idle session retention in days (default 28) |
| `-detect-mode` | report which operating mode the prompt selects |
| `-resume`, `-r` | resume a session by id or unique id prefix in the interactive TUI |
| `-continue`, `-c` | continue the most recent session for the current project |
| `-verbose` | print mode and security decisions to stderr |
| `-version` | print the version and exit |

Signet starts the UI only when both stdin and stdout are a terminal, so it is safe in pipelines and CI.

## Configuration

Set the API key for your provider and Signet picks it up:

| Provider | Environment |
| --- | --- |
| `openai` | `OPENAI_API_KEY` |
| `anthropic` | `ANTHROPIC_API_KEY` |
| `cloudflare-workers-ai` | `CLOUDFLARE_API_KEY`, `CLOUDFLARE_ACCOUNT_ID` |
| `cloudflare-ai-gateway` | `CF_AIG_TOKEN`, `CF_ACCOUNT_ID` (or `CLOUDFLARE_ACCOUNT_ID`). Optional: `CF_AIG_URL` |
| `openrouter` | `OPENROUTER_API_KEY` |
| `google-gemini` | `GEMINI_API_KEY` or `GOOGLE_API_KEY` |
| `ollama` | none (local; honours `OLLAMA_HOST`) |
| `github-copilot` | `GITHUB_COPILOT_TOKEN` or `GH_TOKEN` (OAuth, exchanged for a session token) |
| `huggingface` | `HF_TOKEN` or `HUGGINGFACE_TOKEN` |

A custom provider defined in `settings.json` resolves its key from its
`api_key_env` variable or `SIGNET_<NAME>_API_KEY`.

`/credentials` in the UI stores them for you instead, in your host keychain or in `~/.vulnetix/signet/credentials.json`. Signet resolves credentials from the environment first, then a project-local `.vulnetix/signet/credentials.json`, then the user file, then `~/.netrc`, then the keychain — and tells you which one each value came from. A credential may be stored as the *name* of an environment variable rather than a value, which is how project credential files stay committable.

Pick a default provider without passing `-provider` every time by setting `SIGNET_PROVIDER`.

For a custom state directory, set `SIGNET_HOME`. To disable the TUI and keep the old one-shot behaviour, set `SIGNET_NO_TUI=1` or run in CI (`CI` is honoured).

Signet creates `~/.vulnetix/signet/` at mode `0700` and credential files at mode `0600`. If you already have a `~/.signet/` directory, it is migrated automatically on the next run. We recommend adding `.vulnetix/signet/` to your repository `.gitignore` — even though the project file only holds references, defence in depth is cheap.

### Settings files

Declared intent lives in `settings.json`; last-used runtime values live in
`state.json`. The global file is `~/.vulnetix/signet/settings.json` (or
`$SIGNET_HOME/settings.json`); the project file is
`<workdir>/.vulnetix/settings.json`. Precedence, lowest to highest:
`defaults` < `state.json` < global `settings.json` < project `settings.json` <
environment < CLI flags. The `/settings` browser shows the effective value and
its provenance for every key.

| Key | Meaning |
| --- | --- |
| `provider` | default provider name |
| `model` | default model id |
| `effort` | default thinking effort: `low`, `medium`, `high` |
| `caveman` | toggle the caveman voice rewrite |
| `permissions` | structured `allow` / `ask` / `deny` tool rule arrays |
| `session_retention_days` | idle session retention (default 28) |
| `ui.banner` / `ui.status_bar` | TUI presentation toggles |
| `show_session_names` | show session names in the status bar (default on) |
| `context_windows` | per-model context-window overrides, in tokens |
| `providers` | custom provider profiles (see below) |
| `allow_project_providers` | opt in to project-layer `providers` (default off) |
| `resilience.max_agents` | fan-out ceiling for explore subagents + background agents (default 3) |
| `resilience.plan_explore` | plan-mode repository survey on/off (default on) |

**Custom providers.** A `providers` block defines a provider by name, with
`base_url`, `api` (`openai-chat`, `openai-responses`, or
`anthropic-messages`), optional `auth` (`bearer`, `x-api-key`, or `cf-aig`),
optional `api_key_env`, and a `models` catalogue. Secrets never live here; a
profile references the key via `api_key_env` or the credential backends.
Project-layer `providers` blocks are ignored unless the global settings set
`allow_project_providers: true`, because a hostile repo defining a provider is
an API-key exfiltration primitive.

**Permissions merge is a union, never a replacement.** A project file can add
rules but can never remove a rule you set globally, and a deny from either
scope wins.

**Bash permissions.** Because `Bash` is a registered tool, permission rules
use the same `Tool(spec)` shape as other tools. Common rules include
`Bash(git status *)`, `Bash(git diff *)`, `Bash(ls *)`, and `Bash(echo *)`.
Under the read-only master switch Bash executes without a shell, so pipes,
redirections, and command substitution are rejected structurally. The legacy
flat-map form (`"bash": "ask"`) is still accepted on read but files
self-upgrade to the structured form on first write.

**Bash is unavailable in plan mode.** Plan mode neither advertises nor
executes it, read-only or otherwise — investigation there goes through
`Read`, `Grep`, `Glob`, `Cd`, and the native read-only tools, whose argument
shapes are fixed.

**What goes to the security classifier.** Every tool result is sanitized.
Results whose content is arbitrary are classified on top of that: `Bash` (an
arbitrary command), `WebFetch` and `WebSearch` (text written off your
machine), and `Read` (a file's bytes). The tools whose output shape the
harness already knows — `Grep`, `Glob`, `Write`, `Edit`, and the native
read-only tools — are sanitized and promoted directly. See
[docs/architecture.md](docs/architecture.md#tool-result-trust).

**Path rules are relative to the working directory.** Tools share one working
directory that starts at the session root and can move within it with `Cd`. A
path beginning with `/` means the session root; anything else is relative to
the current working directory. Nothing reaches outside the root either way —
a move changes how a path is spelled, never what it can reach — and the TUI
footer shows where the session currently is.

### Session storage

The TUI writes an append-only JSONL session per workdir under
`~/.vulnetix/signet/sessions/`. `/compact` never mutates the old file — it
writes a new session whose root entry links `meta.parent_session` to the old
id, and carries the old name forward. `/clear` starts a new session and leaves
the previous one on disk untouched.

Quitting (`ctrl+d` twice, or `/exit`) prints a branded exit card below the
restored shell prompt: the session's display name, turn/duration/token facts,
its on-disk path, and the exact `signet --resume <id>` command that returns to
it. `signet --continue` (`-c`) reopens the most recent session for the current
project without remembering an id.

## Documentation

- [docs/architecture.md](docs/architecture.md) — system design.
- [docs/role-manager.md](docs/role-manager.md) — operating-mode business rules.
- [docs/nonce-endpoint-spec.md](docs/nonce-endpoint-spec.md) — provider nonce GET spec.
- [docs/development.md](docs/development.md) — local development, build, and QA workflows.
