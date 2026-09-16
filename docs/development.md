# Development

Local development, build, and QA workflows for Signet.

## Prerequisites

| Tool | Version | Why |
| --- | --- | --- |
| [Go](https://go.dev/dl/) | 1.24.2 or newer (see `go.mod`) | build and test |
| [just](https://just.systems) | 1.11 or newer | task runner |
| git | any | version stamping via `git describe` |

Install `just`:

```bash
brew install just        # macOS / Linuxbrew
cargo install just       # any platform with Rust
sudo pacman -S just      # Arch
sudo apt install just    # Debian/Ubuntu (trixie+)
```

```bash
git clone https://github.com/vulnetix/signet.git
cd signet
just            # lists every recipe
just check      # gofmt + go vet + go test -race, exactly what CI runs
```

## Why `just` and not `make`

`make` cannot forward arbitrary arguments to a target, so `go run ./cmd/signet -provider … -prompt "…"` was impossible from a Makefile. The workaround was to build into `dist/` or `bin/` and run the artefact — which silently goes stale the moment you edit source, and produces confusing failures like `flag provided but not defined: -provider` from a binary built before that flag existed.

`just` takes recipe parameters, so **everything you run during development runs from source**. There is no build artefact to keep in sync. `bin/` is produced only by `just build-all`, which exists to verify release cross-compilation, and it is gitignored.

## Running from source

| Command | Does |
| --- | --- |
| `just tui` | launch the interactive TUI |
| `just prompt "what model is this"` | one noninteractive turn, quoting preserved |
| `just ask openai gpt-5 "summarise this repo"` | pin provider and model for one turn |
| `just detect-mode "refactor the parser"` | report the Role Manager mode decision only |
| `just run -version` | pass raw flags through |

`prompt`, `ask`, and `detect-mode` take their text as an exported shell variable, so quotes, apostrophes, and `@` characters in a model id survive intact:

```bash
just ask cloudflare-workers-ai '@cf/moonshotai/kimi-k2.6' "what model is this"
just prompt "it's fine" -verbose
```

`just run` splits its arguments on whitespace — `just run -prompt "two words"` reaches the binary as three separate arguments. Use `just prompt` for anything with a space in it.

Append extra flags to any of these; they land after the generated ones:

```bash
just prompt "explain internal/run" -verbose
just ask anthropic claude-sonnet-4-5 "review this diff" -detect-mode -verbose
```

### CLI flags

| Flag | Meaning |
| --- | --- |
| `-prompt` | send one turn noninteractively, print the reply, exit |
| `-provider` | `openai`, `anthropic`, `cloudflare-workers-ai`, `cloudflare-ai-gateway`, `openrouter`, `google-gemini`, `ollama`, `github-copilot`, or a custom name from `settings.json` |
| `-model` | model id; defaults are `gpt-5`, `claude-sonnet-4-5`, `@cf/moonshotai/kimi-k2.6` |
| `-effort` | thinking-effort level: `low`, `medium`, or `high` |
| `-caveman` | enable caveman voice rewrite for this run |
| `-session-retention-days` | idle session retention in days (default 28) |
| `-detect-mode` | run the operating-mode classifier and report the decision (`agent`, `plan`, `goal`) |
| `-tools` | enable tool execution (noninteractive agent mode) |
| `-agent` | start a background agent by name in foreground mode |
| `-agent-create` | create an agent profile from a description and save to disk |
| `-verbose` | print Role Manager decisions and the security sentinel to stderr |
| `-version` | print the version and exit |

Runtime flag values override the settings file for the current run but are never persisted.

Every non-TUI entry point (`-prompt`, `-agent`, `-agent-create`) runs under a
`signal.NotifyContext` root. Goal mode's pass loop is unbounded by design, so an
interruptible context is the only thing that can stop it: the first `SIGINT` or
`SIGTERM` cancels it and the loop unwinds at its next pass boundary, returning
the partial result. A second signal hard-exits with status 130, because that
boundary may still be seconds away.

With no `-prompt` and a TTY on both stdin and stdout, Signet starts the TUI. Set `SIGNET_NO_TUI=1` (or `CI=1`) to force the noninteractive path — useful when piping output or reproducing a CI failure locally.

## Credentials for QA

Provider selection order: the `-provider` flag, then `$SIGNET_PROVIDER`, then `$PI_PROVIDER`, then a default of `openai`. `$SIGNET_BASE_URL` overrides the provider base URL, which is how you point a QA run at a mock or a proxy.

Credentials resolve in this order, first hit wins:

1. environment
2. project file — `.vulnetix/signet/credentials.json` in the working directory
3. user file — `~/.vulnetix/signet/credentials.json`
4. `~/.netrc`
5. host keychain

| Provider | Required |
| --- | --- |
| `openai` | `OPENAI_API_KEY` |
| `anthropic` | `ANTHROPIC_API_KEY` |
| `cloudflare-workers-ai` | `CLOUDFLARE_API_KEY`, `CLOUDFLARE_ACCOUNT_ID` |
| `cloudflare-ai-gateway` | `CLOUDFLARE_API_KEY`, `CLOUDFLARE_ACCOUNT_ID`, `CLOUDFLARE_GATEWAY_ID` |
| `openrouter` | `OPENROUTER_API_KEY` |
| `google-gemini` | `GEMINI_API_KEY` or `GOOGLE_API_KEY` |
| `ollama` | none (local; honours `OLLAMA_HOST`) |
| `github-copilot` | `GITHUB_COPILOT_TOKEN` or `GH_TOKEN` (exchanged for a session token) |

For a throwaway QA shell, export into the environment so nothing is written to disk:

```bash
export CLOUDFLARE_API_KEY=… CLOUDFLARE_ACCOUNT_ID=…
just ask cloudflare-workers-ai '@cf/moonshotai/kimi-k2.6' "what model is this"
```

Inside the TUI, `/credentials` manages stored credentials and shows which backend each value came from. Its keys are `s` set value, `e` set env reference, `c` clear, `b` cycle backend, `i` import, and `esc` back.

## QA checklist

Manual passes worth running before a release, in addition to `just check`.

**Provider matrix.** One prompt per provider you have keys for:

```bash
just ask openai gpt-5 "reply with the single word OK"
just ask anthropic claude-sonnet-4-5 "reply with the single word OK"
just ask cloudflare-workers-ai '@cf/moonshotai/kimi-k2.6' "reply with the single word OK"
```

**Missing-credential path.** With a TTY, Signet should offer the credential manager; without one it must fail closed and name every location it searched:

```bash
env -u OPENAI_API_KEY SIGNET_NO_TUI=1 just prompt "hello"
# signet: openai requires OPENAI_API_KEY (looked in: environment)
```

**Clarify loop.** In the TUI, `shift+tab` to plan mode and send an ambiguous
prompt with an `@file` reference (for example, "plan how to refactor @README.md
into packages"). Confirm the questionnaire appears, `space` selects options,
`n` adds a note, `s` skips a question, `enter` submits and triggers a second
explore round, and planning finally proceeds. Press `esc` at the questionnaire
and confirm the turn cancels cleanly without a raw `context.Canceled` in the
transcript.

**Mode classification.** Confirm the Role Manager routes prompts as `docs/role-manager.md` specifies:

```bash
just detect-mode "add a retry to the HTTP client"   # agent
just detect-mode "how does the nonce sealing work"  # plan
```

**TUI smoke test.** `just tui`, then exercise slash-command autocomplete (`/p` → `/permissions`, `/plan`, `/profile`), `/credentials`, `/settings`, `/permissions`, `/help`, `/model`, `/compact`, `/clear`, `/rename`, `/agent list`, and streaming output. Confirm `shift+tab` cycles the mode chip, `ctrl+d` quits, `ctrl+c` copies the prompt (native or OSC 52), and `esc` escapes every full-screen view — including permissions back to settings. In the Ask prompt, confirm Enter echoes the prompt into the transcript as a `user prompt` instantly, that the composer shows the filled `role manager` pill with a `pre-prompt processing` caption while the classifier runs and a plain `working` label only for model/tool I/O, and that Enter while a turn is running queues a `user steering` message. In `/settings`, confirm the **bash read-only** toggle renders `off` by default, that `space` flips it on and persists it to the scoped settings file, and that `x` clears it. In `/permissions` with no rules, confirm the empty state reads "every tool call is allowed" and that a `Read x` preview shows the allowed-by-default wording (or blocked when `preferences.yaml` sets `permission_no_match: enforce`).

**Release parity.** `just build-all` cross-compiles all six release targets into `bin/` with the same ldflags the release workflow uses, and writes `bin/checksums.txt`. Run the host binary and check `-version` reports the git description.

## Tests

| Command | Does |
| --- | --- |
| `just test` | unit tests |
| `just test-race` | full suite under `-race`, as CI runs it |
| `just test-pkg ./internal/run -run TestStream -v` | one package, flags passed through |
| `just e2e` | builds the binary and drives it against a mock provider |
| `just cover` | writes `coverage.txt`, prints the per-function summary |
| `just cover-html` | opens the HTML coverage report |

The `e2e` package builds `./cmd/signet` into a temp dir and runs it against an `httptest` provider, so it catches exactly the class of bug a stale artefact hides: flags, exit codes, and the Role Manager pipeline as the shipped binary sees them.

## Lint and hygiene

| Command | Does |
| --- | --- |
| `just fmt` | `gofmt -w .` |
| `just fmt-check` | fails on unformatted files, modifies nothing (CI's check) |
| `just vet` | `go vet ./...` |
| `just cross` | build the windows/amd64 and darwin/arm64 targets CI cross-compiles |
| `just tidy` | `go mod tidy` then `go mod verify` |
| `just clean` | removes `signet`, `bin/`, `coverage.txt`, clears the test cache |

`just check` runs `fmt-check`, `vet`, `test-race`, and `cross` in CI's order. Green locally means green in `.github/workflows/ci.yml`.

## Versioning

`just build`, `just install`, and `just build-all` stamp `internal/version` from git:

```
Version   = git describe --tags --always --dirty
Commit    = git rev-parse --short HEAD
BuildDate = UTC RFC 3339
```

`just version` prints what the current tree would stamp. A build without those ldflags reports `dev`, which is the signal that a binary did not come from the release path.

## CI and release

- `.github/workflows/ci.yml` runs `go vet`, a `gofmt` check, `go test -race ./...`, and a windows/darwin cross-compile on every push and pull request.
- `.github/workflows/release.yml` fires on a `v*` tag, cross-compiles the six targets on the self-hosted runner, publishes a GitHub release with `checksums.txt`, then updates the Homebrew tap and Scoop bucket from those checksums.

Signet is pure Go with `CGO_ENABLED=0`, so every target cross-compiles from one Linux host. There is no goreleaser step; the release workflow builds directly and is mirrored locally by `just build-all`.

## Layout

- `cmd/signet` — entrypoint and flag parsing.
- `internal/…` — library code, one package per concern.
- `e2e/` — end-to-end tests that drive the built binary.
- `docs/architecture.md` — system design.
- `docs/role-manager.md` — Role Manager, operating-mode, and goal pass-loop
  business rules.
- `docs/resilience.md` — retry layers, budgets, and pass-boundary recovery.
- `docs/agent-profiles.md` — background agent schema and lifecycle.
- `docs/nonce-endpoint-spec.md` — provider nonce GET spec.

Security invariants that changes must not weaken are listed in [AGENTS.md](../AGENTS.md).
