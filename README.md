# Signet

Signet is a role-managed, injection-safe LLM coding harness. It pairs a Codex-style terminal UI with a safety-first architecture: every harness delimiter carries a cryptographic nonce and integrity hash, untrusted content is classified before it reaches the model, and multiple interaction modes (agent, plan, goal) let you choose the right level of autonomy for the task.

## Installation

### macOS & Linux (Homebrew)

```bash
brew install vulnetix/tap/signet
```

### Windows (Scoop)

```powershell
scoop bucket add vulnetix https://github.com/Vulnetix/scoop-bucket
scoop install signet
```

### Shell installer

```bash
curl -sSL https://raw.githubusercontent.com/vulnetix/signet/main/install.sh | sh
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

Inside the UI, `/` opens slash-command autocomplete — `/credentials` to configure providers, `/settings`, `/plan`, `/goal`, `/todos`, `/profile`, `/code-review`.

For a single answer without the UI:

```bash
signet -prompt "what does internal/run do?"
signet -provider anthropic -model claude-sonnet-4-5 -prompt "review this diff"
```

| Flag | Meaning |
| --- | --- |
| `-prompt` | send one turn, print the reply, exit |
| `-provider` | `openai`, `anthropic`, `cloudflare-workers-ai`, `cloudflare-ai-gateway` |
| `-model` | model id; each provider has a default |
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

`/credentials` in the UI stores them for you instead, in your host keychain or in `~/.vulnetix/signet/credentials.json`. Signet resolves credentials from the environment first, then a project-local `.vulnetix/signet/credentials.json`, then the user file, then `~/.netrc`, then the keychain — and tells you which one each value came from.

Pick a default provider without passing `-provider` every time by setting `SIGNET_PROVIDER`.

For a custom state directory, set `SIGNET_HOME`. To disable the TUI and keep the old one-shot behaviour, set `SIGNET_NO_TUI=1` or run in CI (`CI` is honoured).

Signet creates `~/.vulnetix/signet/` at mode `0700` and credential files at mode `0600`. If you already have a `~/.signet/` directory, it is migrated automatically on the next run. We recommend adding `.vulnetix/signet/` to your repository `.gitignore` — even though the project file only holds references, defence in depth is cheap.

## Documentation

- [docs/architecture.md](docs/architecture.md) — system design.
- [docs/role-manager.md](docs/role-manager.md) — operating-mode business rules.
- [docs/nonce-endpoint-spec.md](docs/nonce-endpoint-spec.md) — provider nonce GET spec.
- [docs/development.md](docs/development.md) — local development, build, and QA workflows.
