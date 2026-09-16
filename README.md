# Signet

Signet is a role-managed, injection-safe LLM coding harness. It pairs a Codex-style terminal UI with a safety-first architecture: every harness delimiter carries a cryptographic nonce and integrity hash, untrusted content is classified before it reaches the model, and multiple interaction modes (agent, plan, goal) let you choose the right level of autonomy for the task.

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

Inside the UI, `/` opens slash-command autocomplete — `/credentials` to configure providers, `/model` to pick provider/model/effort, `/settings` to edit settings, `/permissions` to edit tool rules, `/plan`, `/goal`, `/todos`, `/profile`, `/agent` to run background agents, `/code-review`, `/compact` to summarise a long session into a new one, `/clear` (or `/new`) to start a fresh session, and `/rename` to name the session. `/help` lists everything.

`shift+tab` cycles the mode. **Goal mode** is the one that keeps going: instead of stopping when the tool budget runs out, an evaluator checks whether the work advanced and grants another pass while it does, tracking a todo list in a panel above the prompt. It is stopped by a stall, not a counter — press `esc` (or `ctrl+c` outside the UI) to stop it and keep the partial result. Set `resilience.max_passes` if you want a hard ceiling. The rules are in [docs/role-manager.md](docs/role-manager.md).

For a single answer without the UI:

```bash
signet -prompt "what does internal/run do?"
signet -provider anthropic -model claude-sonnet-4-5 -prompt "review this diff"
```

| Flag | Meaning |
| --- | --- |
| `-prompt` | send one turn, print the reply, exit |
| `-provider` | `openai`, `anthropic`, `cloudflare-workers-ai`, `cloudflare-ai-gateway`, `openrouter`, `google-gemini`, `ollama`, `github-copilot`, or a custom name from `settings.json` |
| `-model` | model id; each provider has a default |
| `-effort` | thinking-effort level: `low`, `medium`, or `high` |
| `-caveman` | enable caveman voice rewrite for this run |
| `-tools` | enable tool execution for this run |
| `-session-retention-days` | idle session retention in days (default 28) |
| `-detect-mode` | report which operating mode the prompt selects |
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
| `cloudflare-ai-gateway` | `CLOUDFLARE_API_KEY`, `CLOUDFLARE_ACCOUNT_ID`, `CLOUDFLARE_GATEWAY_ID` |
| `openrouter` | `OPENROUTER_API_KEY` |
| `google-gemini` | `GEMINI_API_KEY` or `GOOGLE_API_KEY` |
| `ollama` | none (local; honours `OLLAMA_HOST`) |
| `github-copilot` | `GITHUB_COPILOT_TOKEN` or `GH_TOKEN` (OAuth, exchanged for a session token) |

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

**Bash permissions.** Because `Bash` is now a registered tool, permission
rules use the same `Tool(spec)` shape as other tools. Common rules include
`Bash(git status *)`, `Bash(git diff *)`, `Bash(ls *)`, and `Bash(echo *)`.
All Bash executions run without a shell, so pipes, redirections, and command
substitution are rejected structurally. The legacy flat-map form
(`"bash": "ask"`) is still accepted on read but files self-upgrade to the
structured form on first write.

### Session storage

The TUI writes an append-only JSONL session per workdir under
`~/.vulnetix/signet/sessions/`. `/compact` never mutates the old file — it
writes a new session whose root entry links `meta.parent_session` to the old
id, and carries the old name forward. `/clear` starts a new session and leaves
the previous one on disk untouched.

## Documentation

- [docs/architecture.md](docs/architecture.md) — system design.
- [docs/role-manager.md](docs/role-manager.md) — operating-mode business rules.
- [docs/nonce-endpoint-spec.md](docs/nonce-endpoint-spec.md) — provider nonce GET spec.
- [docs/development.md](docs/development.md) — local development, build, and QA workflows.
