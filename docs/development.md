# Development

Local development, build, and QA workflows for Signet.

## Prerequisites

| Tool | Version | Why |
| --- | --- | --- |
| [Go](https://go.dev/dl/) | 1.25 (see `go.mod`) | build and test |
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
| `-provider` | `openai`, `anthropic`, `cloudflare-workers-ai`, `cloudflare-ai-gateway`, `openrouter`, `google-gemini`, `ollama`, `github-copilot`, `huggingface`, or a custom name from `settings.json` |
| `-model` | model id; defaults come from `run.DefaultModel` (see the table below) |
| `-effort` | thinking-effort level: `low`, `medium`, or `high` |
| `-classifier-provider` | security-classifier provider (default: the main provider) |
| `-classifier-model` | security-classifier model (default: the main model) |
| `-classifier-effort` | security-classifier thinking effort (default: `none`) |
| `-caveman` | enable caveman voice rewrite for this run |
| `-session-retention-days` | idle session retention in days (default 28) |
| `-detect-mode` | run the operating-mode classifier and report the decision (`agent`, `plan`, `goal`) |
| `-tools` | enable tool execution in the noninteractive agent path; **on by default**, pass `-tools=false` to disable. `-detect-mode` reports the classifier decision without executing anything whatever this is set to |
| `-agent` | start a background agent by name in foreground mode |
| `-agent-create` | create an agent profile from a description and save to disk |
| `-no-prune` | never prune idle sessions (overrides `-session-retention-days`) |
| `-plan` | start in plan mode: read-only tools only, no mutation |
| `-verbose` | print Role Manager decisions and the security sentinel to stderr |
| `-version` | print the version and exit |

Every posture gate also has a flag (`-allow-unsafe-tool-result`,
`-allow-malformed-tool-result`, `-allow-unsafe-prompt`,
`-allow-malformed-prompt`, `-tool-call-mismatch`, `-allow-unpermitted-tools`,
`-allow-ask-without-tty`, `-allow-invalid-skills`, `-allow-invalid-hooks`,
`-dangerously-yolo-everything`). They are documented with their gates in
[role-manager.md](role-manager.md#posture-gates).

### Model defaults

`-model` is optional. When it is empty the provider decides
(`run.DefaultModel`):

| Provider | Default model |
| --- | --- |
| `openai` (and any unrecognised provider) | `gpt-5` |
| `anthropic` | `claude-opus-4-5` |
| `cloudflare-workers-ai` | `@cf/moonshotai/kimi-k2.6` |
| `cloudflare-ai-gateway` | (inherits from selected upstream; default static list shows Workers AI models) |
| `openrouter` | `openrouter/auto` |
| `google-gemini` | `gemini-2.5-flash` |
| `ollama` | `llama3` |
| `github-copilot` | `gpt-4o` |
| `huggingface` | `meta-llama/Llama-3.2-3B-Instruct` |

A custom provider from `settings.json` falls through to the `gpt-5` default, so
a custom entry should carry its own model.

Runtime flag values override the settings file for the current run but are never persisted.

The classifier mirrors the main provider flags through `-classifier-*` and
`SIGNET_CLASSIFIER_PROVIDER/MODEL/EFFORT`; `SIGNET_CLASSIFIER_CHUNK_BYTES` /
`SIGNET_CLASSIFIER_CHUNK_CONCURRENCY` are not yet env-wired (the `classifier`
settings block sets them).

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
| `huggingface` | `HF_TOKEN` or `HUGGINGFACE_TOKEN` |

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
just ask huggingface 'meta-llama/Llama-3.2-3B-Instruct' "reply with the single word OK"
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

**TUI smoke test.** `just tui`, then exercise slash-command autocomplete (`/p` → `/permissions`, `/profile`), `/credentials`, `/settings`, `/permissions`, `/help`, `/model`, `/compact`, `/clear`, `/rename`, `/agent list`, `/local-model`, and streaming output. Confirm `shift+tab` cycles the mode chip, `ctrl+d` quits, `ctrl+c` copies the prompt (native or OSC 52), and `esc` escapes every full-screen view — including permissions back to settings. On a provider with a long model catalogue, confirm the `/model` list is windowed (chrome stays visible, `↓ N more` marks the overflow) and that `/` narrows the list by substring while `esc` clears the filter. Press `r` on providers with live model lists (`openai`, `anthropic`, etc.) to force a refresh; for `huggingface` the list is a conservative static catalog and `r` should not surface a fetch error, while `cloudflare-ai-gateway` refreshes from the Workers AI model-search API. In the Ask prompt, confirm Enter echoes the prompt into the transcript as a `user prompt` instantly, that the composer shows the filled `role manager` pill with a `pre-prompt processing` caption while the classifier runs and a plain `working` label only for model/tool I/O, and that Enter while a turn is running queues a `user steering` message. In `/settings`, confirm the **read-only tools** toggle renders `off` by default, that `space` flips it on and persists it to the scoped settings file, and that `x` clears it. Confirm the **caveman** toggle also shows `off` by default and that `ctrl+alt+c` from the chat view flips it on, emits a `caveman: on` system message, and immediately updates the footer indicator; a second press returns it to `off`. In `/permissions` with no rules, confirm the empty state reads "every tool call is allowed" and that a `Read x` preview shows the allowed-by-default wording (or blocked when `preferences.yaml` sets `permission_no_match: enforce`).

**Agent picker.** In the TUI in agent mode, confirm the strip above the prompt
lists your profiles, the `↻` background-agent definitions, and the `◈`
built-ins. Press `tab` repeatedly and confirm the highlight walks every
candidate, ends on `(none)`, wraps, and never writes into the prompt; that
`enter` engages the highlighted one and shows it in the footer chip rather
than sending the turn; that `enter` with nothing highlighted still sends; and
that `right` moves the cursor until something is highlighted. Type `@` and a
partial name to filter, engage, and confirm the `@…` text is removed from the
prompt. Press `ctrl+g` on a `↻` row and confirm the agent starts in the
background; on a flat profile, confirm it says so instead. Then `shift+tab`
into plan and goal mode and confirm the chip drops the agent name and the
strip disappears, and that returning to agent mode brings both back.

**Slash completion.** Type `/c`, then `tab` several times, and confirm the
highlight cycles through every match instead of sticking on the second one —
the prompt text must not change until `enter` or `right` accepts.

**Prompt library.** Type a prompt, press `alt+s`, name it, and confirm the
system line reports it saved to the project library and that the name appears
in `.vulnetix/prompts.json`. Press `up` and confirm the named prompt loads into
the composer, a chip strip of prompt *names* appears above it, and the meta line
reads `↑↓ cycle · tab name · → accept · ⏎ use · esc cancel`. Confirm `tab` walks
the names and wraps; `up`/`down` walk the whole list including unnamed session
history (where no chip is highlighted); `right` accepts the loaded prompt and
leaves the cycle with the cursor at the end; typing any character leaves the
cycle and edits the loaded prompt rather than clearing the composer; `esc`
restores what you had typed. With no saved prompts, confirm `up` still browses
session history and the strip is absent.

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
