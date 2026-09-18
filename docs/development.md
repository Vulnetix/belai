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
| `-provider` | `openai`, `anthropic`, `cloudflare-workers-ai`, `cloudflare-ai-gateway`, `openrouter`, `google-gemini`, `ollama`, `llama-server`, `github-copilot`, `huggingface`, or a custom name from `settings.json` |
| `-model` | model id; defaults come from `run.DefaultModel` (see the table below) |
| `-effort` | thinking-effort level: `low`, `medium`, or `high` |
| `-classifier-provider` | security-classifier provider (default: the main provider) |
| `-classifier-model` | security-classifier model (default: the main model) |
| `-classifier-effort` | security-classifier thinking effort (default: `none`) |
| `-caveman` | enable caveman voice rewrite for this run |
| `-guardrails` | posture guardrails, **on by default**; `-guardrails=false` forces every gate to `ignore` for this run, overriding both the project `preferences.yaml` and any per-gate flag |
| `-ask-permission` | the permission-ask gate, **on by default**; `-ask-permission=false` resolves every `ask` decision to allow with no prompt |
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
[role-manager.md](role-manager.md#gates-and-defaults).

Precedence between the three blanket switches, since they overlap:
`-dangerously-yolo-everything` turns **both** gates off and short-circuits the
per-flag branch entirely, so `-dangerously-yolo-everything -ask-permission`
still leaves ask off. Otherwise `-guardrails=false` and `-ask-permission=false`
act independently. `-guardrails=false` is applied twice over: once to the
settings the TUI reads, and once to the posture policy itself, where it
replaces the whole resolved policy — defaults, project `preferences.yaml` and
per-gate CLI flags alike — with every gate set to `ignore` (`posture.AllIgnore`).
Passing `-guardrails=false` alongside a per-gate flag is therefore not a
conflict; the per-gate flag simply has nothing left to affect.

The switch is read from the **settings**, not the flag, so
`"guardrails": false` written into a `settings.json` turns the gates off on the
CLI path too. The flag is folded into those settings first, so it still works;
reading the flag alone used to mean a settings file that disabled guardrails
was honoured by the TUI and ignored by the CLI.

### Model defaults

`-model` is optional. When it is empty the provider decides
(`run.DefaultModel`):

| Provider | Default model |
| --- | --- |
| `openai` (and any unrecognised provider) | `gpt-5` |
| `anthropic` | `claude-opus-4-5` |
| `cloudflare-workers-ai` | `@cf/moonshotai/kimi-k2.6` |
| `cloudflare-ai-gateway` | `claude-sonnet-4-5` |
| `openrouter` | `openrouter/auto` |
| `google-gemini` | `gemini-2.5-flash` |
| `ollama` | `llama3` |
| `llama-server` | `default` (the server was started with a single model) |
| `github-copilot` | `gpt-4o` |
| `huggingface` | none (user must type a model id; requires enabled providers in HuggingFace dashboard) |

A custom provider from `settings.json` falls through to the `gpt-5` default, so
a custom entry should carry its own model.

### Model catalog and live fetch

The TUI model picker (`e model`) shows a catalogue per provider. Sources are:

- **Static fallback** — a hard-coded default catalogue for built-ins that have
  no live endpoint or where live fetch is disabled for quality reasons.
- **Live fetch** — on first entry to a provider tab the harness contacts the
  provider's `/models` endpoint (or equivalent) and caches the result for the
  session. A `○ Fetching models from GET <url>…` indicator is shown while the
  request is in flight. The request timeout is 30 seconds so slow networks or
  large catalogues have room to complete.

| Provider | Live fetch |
| --- | --- |
| `anthropic` | ✅ `/v1/models` |
| `cloudflare-workers-ai` | ✅ v4 API search |
| `cloudflare-ai-gateway` | ✅ reuses the account's Workers AI catalogue (fetched with `CLOUDFLARE_API_KEY`/`CLOUDFLARE_ACCOUNT_ID`); falls back to the static catalogue without Workers AI credentials |
| `google-gemini` | ✅ `/models` |
| `ollama` | ✅ `/models` |
| `llama-server` | ✅ `/models` |
| `openrouter` | ✅ `/models` |
| `github-copilot` | ✅ `/models` |
| `openai` | ❌ disabled — `/v1/models` includes deprecated, preview and internal identifiers that confuse the picker and fail at request time; only the curated static catalogue is shown |
| `huggingface` | ✅ `router.huggingface.co/v1/models` (requires `HF_TOKEN`) |

Business rules and edge cases:
- **Workers AI through the gateway** — when a model id from the
  `cloudflare-workers-ai` family (starting with `@cf/`) is selected on the
  `cloudflare-ai-gateway` provider, the model is automatically prefixed with
  `workers-ai/` in the outbound request so the gateway routes it to the
  Workers AI backend (`workers-ai/@cf/...`). Already-prefixed ids are not
  double-prefixed. The TUI footer shows this prefixed (wire) form so the
  effective model name is visible next to the provider.
- **Gateway authentication & endpoint** — `cloudflare-ai-gateway`
  authenticates with a gateway token via `cf-aig-authorization: Bearer`. Set
  `CF_AIG_TOKEN`, plus `CF_ACCOUNT_ID` (or `CLOUDFLARE_ACCOUNT_ID`). The
  default base URL is
  `https://gateway.ai.cloudflare.com/v1/{CF_ACCOUNT_ID}/default/compat`; use
  `CF_AIG_URL` to override it when your account has multiple gateways (it must
  end in `/compat`). Chat is sent to the compatibility surface by appending
  `/v1/chat/completions` — the `/openai/chat/completions` form is rejected
  with `Compatibility endpoint: openai/chat/completions is not supported`.
- **HuggingFace enablement** — the router's `/v1/models` list is live-fetched
  and may include models from third-party Inference Providers the account has
  not enabled. Selecting an un-enabled model returns `model_not_supported`
  from HuggingFace; enable the corresponding provider in the HuggingFace
  dashboard before calling it. The picker does not pre-filter because the
  router exposes no enabled-only list.
- **Empty live fetch** — if the live request fails or returns nothing, the
  picker silently falls back to the static catalogue (when one exists) and
  still allows typing any model id directly.
- **Custom providers** — a custom provider from `settings.json` whose `api`
  field is `anthropic-messages` uses `/v1/models`; every other surface uses
  `/models`.

Runtime flag values override the settings file for the current run but are never persisted.

The classifier mirrors the main provider flags through `-classifier-*` and
`SIGNET_CLASSIFIER_PROVIDER/MODEL/EFFORT`; `SIGNET_CLASSIFIER_CHUNK_BYTES` /
`SIGNET_CLASSIFIER_CHUNK_CONCURRENCY` are not yet env-wired (the `classifier`
settings block sets them).

`SIGNET_CLASSIFIER_CAVEMAN` and `classifier.caveman` voice the classifier's
**prose** payloads only — the compaction summary, the session name, and the
agent-profile designer. Sentinel payloads are never voiced: their replies are
matched exactly, so a voice rewrite would break the parse. It is independent of
the agent's own `caveman` setting.

In the TUI the whole `classifier` block is editable from `/classifier` (also
reachable with `g` from `/model`), which writes to the global or project
settings file — never to session state, because which model guards tool output
is a decision that stays visible and provenanced.

Every non-TUI entry point (`-prompt`, `-agent`, `-agent-create`) runs under a
`signal.NotifyContext` root. Goal mode's pass loop is unbounded by design, and
plan mode's pass loop is bounded but still multi-pass, so an interruptible
context is what stops either of them cleanly: the first `SIGINT` or `SIGTERM`
cancels the loop and it unwinds at its next pass boundary, returning the
partial result. A second signal hard-exits with status 130, because that
boundary may still be seconds away.

With no `-prompt` and a TTY on both stdin and stdout, Signet starts the TUI. Set `SIGNET_NO_TUI=1` (or `CI=1`) to force the noninteractive path — useful when piping output or reproducing a CI failure locally.

## Tool surface

The tools the model is offered are built by `tools.Default` /
`tools.DefaultWithCaps` and narrowed per mode. The narrowing happens in two
places that must agree — `Registry.Plan()` decides what is *advertised*,
`modes.ToolAllowed` decides what is *executed* — and both are exercised by
`internal/tools` and `internal/modes` tests.

Business rules and edge cases:

- **Plan mode has no `Bash`.** Not "restricted `Bash`": the tool is absent
  from the plan-mode registry, and the gate refuses it whatever the command
  says. Read-only `Bash` survives the read-only master switch
  (`Registry.ReadOnly()`) but not the plan narrowing. Investigation there
  goes through `Read`, `Grep`, `Glob`, `Cd`, and the native read-only
  catalogue.
- **The surface follows the turn.** The mode classifier can route a single
  prompt to plan mode inside an agent-mode session. Both tool surfaces are
  built at session construction and `Session.toolSurface` picks per turn, so
  the advertised list, the sealed `<tools>` block, and the execution gate
  never disagree.
- **Descriptions are the model's only manual.** Each builtin's `Description`
  opens with a one-sentence summary — `prompt.Summarise` takes exactly that
  sentence for the `<tools>` index — and then states its bounds, its failure
  modes, and which sibling tool to prefer. Keep that shape when adding one.
- **`Glob` matches in-process.** `fd` only enumerates; `matchGlob` applies the
  pattern. Handing `fd` a pattern containing `/` makes it error out, which is
  what made every recursive glob return nothing. See
  [architecture.md](architecture.md#search-and-file-location-tools).
- **Arbitrary content classifies; shaped output does not.** Every result is
  sanitized. On top of that, `Bash`, `WebFetch`, `WebSearch`, and `Read` go to
  the classifier, because a command's output, a page, and a file's bytes are
  all content the harness cannot predict the shape of. `Grep`, `Glob`,
  `Write`, `Edit`, and the natives are sanitize-only. A kind absent from
  `tools.classifierKinds` is sanitize-only, so a new arbitrary-content tool
  has to add its kind there. See
  [architecture.md](architecture.md#tool-result-trust).
- **`Cd` moves the spelling, not the reach.** The session root stays the
  confinement boundary; the working directory moves inside it. A path
  starting with `/` is root-relative, anything else is relative to the
  working directory, and a refused move leaves the working directory exactly
  where it was. See
  [architecture.md](architecture.md#working-directory-cd).

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
| `cloudflare-ai-gateway` | `CF_AIG_TOKEN`, `CF_ACCOUNT_ID` (or `CLOUDFLARE_ACCOUNT_ID`). Optional: `CF_AIG_URL` to override the default base URL. |
| `openrouter` | `OPENROUTER_API_KEY` |
| `google-gemini` | `GEMINI_API_KEY` or `GOOGLE_API_KEY` |
| `ollama` | none (local; honours `OLLAMA_HOST`, or host/port/protocol managed in `/credentials`) |
| `llama-server` | none (local; honours `SIGNET_LLAMA_HOST/PATH/PROTOCOL` or host/port/protocol managed in `/credentials`) |
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

**TUI smoke test.** `just tui`, then exercise slash-command autocomplete (`/p` → `/permissions`, `/profile`), `/credentials`, `/settings`, `/permissions`, `/help`, `/model`, `/compact`, `/clear`, `/rename`, `/agent list`, `/local-model`, and streaming output. Confirm `shift+tab` cycles the mode chip, `ctrl+d` quits, `ctrl+c` copies the prompt (native or OSC 52), and `esc` escapes every full-screen view — including permissions back to settings. On a provider with a long model catalogue, confirm the `/model` list is windowed (chrome stays visible, `↓ N more` marks the overflow) and that `/` narrows the list by substring while `esc` clears the filter. Press `r` on providers with live model lists (`anthropic`, `openrouter`, `huggingface`, etc.) to force a refresh; for `openai` the list is a conservative static catalog and `r` should not surface a fetch error, while `cloudflare-ai-gateway` refreshes from the Workers AI model-search API. In the Ask prompt, confirm Enter echoes the prompt into the transcript as a `user prompt` instantly, that the composer shows the filled `role manager` pill with a `pre-prompt processing` caption while the classifier runs and a plain `working` label only for model/tool I/O, and that Enter while a turn is running queues a `user steering` message. In `/settings`, confirm the **read-only tools** toggle renders `off` by default, that `space` flips it on and persists it to the scoped settings file, and that `x` clears it. Confirm the **caveman** toggle also shows `off` by default and that `f2` from the chat view flips it on, emits a `caveman: on` system message, and immediately updates the footer indicator; a second press returns it to `off`. In `/permissions` with no rules, confirm the empty state reads "every tool call is allowed" and that a `Read x` preview shows the allowed-by-default wording (or blocked when `preferences.yaml` sets `permission_no_match: enforce`). In `/model`, confirm the provider tabs list only providers whose credentials resolve — with a local server stopped, `ollama` and `llama-server` drop out of `/model` but stay in `/credentials`, and a committed-but-unavailable provider stays on screen with an amber chip and an `unavailable` note rather than disappearing. Press `g` (or run `/classifier`) and confirm the classifier page opens with the security warning, that the **reasoning** toggle greys the effort row and writes `classifier.effort: none`, that toggling it back restores the previous effort, that `space` on **model** opens a picker over the classifier's own provider catalogue, that **caveman** persists and is labelled *prose payloads only*, and that `x` on every row removes the `classifier` block entirely so the classifier follows the main model again.

**Operator toggles.** These live on the function-key row precisely because
`ctrl+alt+<key>` never reaches the TUI (see the Keybindings section of
[architecture.md](architecture.md)), so the smoke test is the only place the
terminal's own handling gets exercised. From the chat view *and* from inside
`/settings`, confirm `f2` flips caveman, `f3` flips guardrails, `f4` flips ask,
and `f5` cycles the mode chip — all four are global and must fire on a
full-screen view, not just in chat. `f6` also works from any screen: it cycles
reasoning effort default → low → medium → high → default and writes the choice
to session state, not to the settings file. Confirm the footer reflects each
toggle in the same frame: `caveman: on|off` always shows, `guardrails`/`ask`
show as two chips, turning *both* gates off collapses them into one gold
`YOLO` chip while the caveman slot stays put, and the effort segment reads
`default` when empty and cycles through `low`/`medium`/`high` when set.
`f7` is the exception: it is chat-scoped and must do nothing from a
full-screen view. If a terminal or multiplexer swallows a function key,
`/settings`, `/yolo`, `/model` and `/mode` are the equivalent paths.

**Guardrails actually off.** With `SIGNET_TRACE` set, press `f3` to turn
guardrails off and send a prompt that reads a file. Confirm the trace shows no
`security_sentinel` records and the composer never shows the `role manager`
pill — off means the classifier is not called, not called and ignored. Repeat
with `!ls` and with an `@file` reference and confirm the same. Turn guardrails
back on and confirm the classifier reappears on all three. Start a background
agent, toggle `f3` while it runs, and confirm its *next* turn follows the new
setting. See
[architecture.md](architecture.md#the-guardrails-switch).

**Newline keys.** Confirm `ctrl+j` and `shift+enter` both insert a newline
rather than sending, and that plain `enter` still sends. `shift+enter` has no
key type of its own, so it reaches the composer either as `ctrl+j` (kitty
protocol) or as ESC+CR (without it) — test it in a terminal of each kind, or
force the second path with `SIGNET_NO_KITTY=1`, because a change that handles
only one encoding looks correct in the terminal you happen to use.

**Composer cursor motion.** Type `foo.bar baz_qux (a, b)` into the prompt and
walk it with `ctrl+left` / `ctrl+right`. Confirm each press crosses exactly one
run: the dot in `foo.bar` is its own stop, `(a` stops after the paren, and
`baz_qux` is a single word. Confirm `ctrl+right` lands on the *end* of a word
and `ctrl+left` on the *start*. Then add a second line and confirm `ctrl+left`
at column 0 steps to the end of the line above, `ctrl+right` at the end of a
line steps to the start of the one below, and that neither wraps around at the
very start or end of the text. Confirm `home`/`end` (`fn+left`/`fn+right`)
still jump to the ends of the logical line, and that a long soft-wrapped line
is walked by word without the wrap points acting as boundaries.

**Mouse hover hints.** With `ui.mouse` on, ask the agent to `Read` a source
file and hover over the resulting `⌁ Read` row. Confirm the footer's third
line shows `ctrl+s save <name> · ctrl+c copy`. Press `ctrl+c` and confirm the
feedback says it copied the *file*, not the prompt; press `ctrl+s` and confirm
the composer relabels `save file` with `⏎ save · esc cancel`. Type a relative
path, press `enter`, and confirm the file is written under the working
directory; repeat with an absolute path and confirm it is honoured. Confirm an
empty path and `esc` both cancel without writing. Hover the footer's
`session: …` text and confirm the hint shows `ctrl+x copy session id`; press
`ctrl+x` and confirm the feedback copies the session id. Hover a truncated
turn or tool row and confirm the hint shows `ctrl+o expand all`; press `ctrl+o`
and confirm every truncated panel expands, the hint clears (the panel is no
longer collapsed), and a second `ctrl+o` collapses them again. Confirm a
collapsed `Read` row shows save, copy, *and* expand all together. Scroll the
transcript while hovering a panel and confirm the hint re-derives from the
frame rather than sticking to a stale target. With `ui.mouse` off, confirm no
hint appears.

**Agent picker.** In the TUI in agent mode, press `enter` while no agent is
engaged and confirm the strip above the prompt opens with `signet:debug`
selected. Confirm `/agent` (no argument) also opens it. The strip lists
built-ins first (`◈`), then user profiles, then `↻` background-agent
definitions. Press `tab` repeatedly and confirm the highlight walks every
candidate, ends on `(none)`, wraps, and never writes into the prompt; that
`enter` engages the highlighted one and shows it in the footer chip rather
than sending the turn; and that `right` moves the cursor until something is
highlighted. Type `@` and confirm the file chooser opens instead of the agent
picker; type `@agent:` and confirm it is treated as a file-chooser filter,
not as the agent picker. Press `ctrl+g` on a `↻` row and confirm the agent
starts in the background; on a flat profile, confirm it says so instead. Then
`shift+tab` into plan and goal mode and confirm the chip drops the agent name
and the strip disappears, and that returning to agent mode brings both back.

**Slash completion.** Type `/c`, then `tab` several times, and confirm the
highlight cycles through every match instead of sticking on the second one —
the prompt text must not change until `enter` or `right` accepts.

**Prompt library.** Type a prompt, press `f7`, name it, and confirm the
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

**Plan-mode tool surface.** `shift+tab` to plan mode and ask for something
that would tempt a shell (for example, "what does CI run on push?"). Confirm
the model reaches for `Read`/`Grep`/`Glob`/`Git` rather than `Bash`, and that
it does not announce a `Bash` call being refused — if it does, the sealed
`<tools>` block and the advertised registry have drifted apart.

**Glob.** In the TUI, ask for "every Go file under internal" and confirm the
result is a non-empty list. Then run the same thing with `fd` off `$PATH`
(`env PATH=/usr/bin:/bin` with `fd` elsewhere, or temporarily rename it) and
confirm the answer is identical — the two enumerators must not disagree.

**Working directory.** Ask the agent to move into a subdirectory and read a
file by its short name. Confirm one `working directory: /…` line appears in
the thread, the footer's first line follows it, and that asking it to leave
the repository (`cd ..` past the root) is refused without moving. Change the
model or toggle a mode to force a session rebuild and confirm the footer
returns to the session root.

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
