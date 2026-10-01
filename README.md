# Vulnetix | Belai

A safer LLM coding harness.

Every delimiter carries a cryptographic nonce and integrity hash, untrusted content is sanitised, and a role manager classifies untrusted inputs while segmenting benign tasks from risky agentic actions.

It pairs a safety-first architecture with multiple interaction modes (agent, plan, goal) let you choose the right level of autonomy for the task.
Inspiration for agents is taken from harnesses like Hermes, while the agentic loop and context management is designed to minimise token usage while maximising model alignment over longer sessions.
This is not a model provider coding harness that incentivises token maxing, or a tool used as a gimmick "look! it has agency!".
No, Belai holds models to sensible constraints and the result is a tool that leaves an impression of confidence and assurance.

## Installation

### macOS & Linux (Homebrew)

```bash
brew install vulnetix/tap/belai
```

Homebrew also knows an unrelated cask called `belai`, so upgrade by the tapped
name to avoid the ambiguity:

```bash
brew upgrade --formula vulnetix/tap/belai
```

### Windows (Scoop)

```powershell
scoop bucket add vulnetix https://github.com/Vulnetix/scoop-bucket
scoop install belai
```

### Shell installer

```bash
curl -fsSL https://raw.githubusercontent.com/Vulnetix/belai/main/install.sh | sh
```

The script detects your platform, verifies the download against the release
`checksums.txt`, and refuses to install on a mismatch. It installs to
`/usr/local/bin`, falling back to `~/.local/bin` when that is not writable.

```bash
# choose the directory or the version
curl -fsSL https://raw.githubusercontent.com/Vulnetix/belai/main/install.sh | sh -s -- --install-dir ~/.local/bin
curl -fsSL https://raw.githubusercontent.com/Vulnetix/belai/main/install.sh | sh -s -- --version v0.1.1
```

### GitHub Releases

Pre-built binaries for Linux, macOS, and Windows are available on the [Releases](https://github.com/vulnetix/belai/releases) page.

Release assets ship in four families. The default install (`install.sh`,
Homebrew, Scoop) is **`belai-bert-guardrails`**, which embeds the phase-1
prompt-saturation model so guardrails work with no provider or API key
configured. The others:

| Asset family | What is embedded | Approx size |
| ------------ | ---------------- | ----------- |
| `belai` | nothing (LLM sentinel only, exactly the pre-embed behaviour; voice input downloads its speech model on `/voice download`) | ~24 MB |
| `belai-bert-guardrails` **(default)** | phase-1 prompt-saturation model, and the speech model for voice input | ~77 MB |
| `belai-bert-guardrails-jailbreak` | phase-1 + phase-2 jailbreak model, and the speech model | ~517 MB |
| `belai-no-classifier` | the speech model only | ~56 MB |

Sizes are for Linux amd64; other platforms differ by a few megabytes. The speech
model is a 32 MB file that voice input runs in pure Go: every family except the
plain `belai` build carries it, so voice works with no download. Plain `belai`
stays small and fetches it only when you run `/voice download`.

`install.sh` accepts `--variant` (or `BELAI_VARIANT`) to pick one of
`no-classifier`, `bert-guardrails`, or `bert-guardrails-jailbreak`. The on-disk
command name is `belai` regardless of variant, and a variant binary updates to
its own family.

### Update checks

At startup Belai compares its own version against the newest GitHub release.
When a newer one exists the banner says so and the belai panel prints the
upgrade command for how this binary was installed — the Homebrew tap, the
Scoop bucket, `go install`, or the installer script plus the release asset for
your OS and architecture. Nothing is downloaded or installed; you run the
command yourself.

The answer is cached for six hours, an unstamped source build never checks,
and the check is off with `update_check: false` in settings or
`BELAI_NO_UPDATE_CHECK=1` for a single run.

### Build from source

Belai is pure Go with no cgo, so a clean build needs only a Go toolchain.

```bash
git clone https://github.com/vulnetix/belai.git
cd belai
go build -o belai ./cmd/belai
./belai -version
```

To install it onto your `PATH` instead:

```bash
go install github.com/vulnetix/belai/cmd/belai@latest
```

A binary built this way reports its version as `dev`. To stamp the real version — and to get the same flags the release builds use — install [just](https://just.systems) and run `just build` or `just install` from a clone. See [docs/development.md](docs/development.md).

## Usage

Run `belai` in a project directory to start the terminal UI:

```bash
cd ~/code/my-project
belai
```

The first launch opens **Getting started**. It covers the main keys and commands, then offers to install the Vulnetix CLI with Homebrew or Scoop (only if you pick *Install*). It then offers to create a Vulnetix account or log in, and once the CLI has credentials it turns on the Vulnetix AI Firewall and the Vulnetix MCP server. `esc` skips it; `/vulnetix setup` or `/welcome` reopens it. Details are in [docs/vulnetix.md](docs/vulnetix.md#getting-started).

Inside the UI, `/` opens slash-command autocomplete — `/providers` (the provider list) to manage providers, credentials and local models, `/providers report` to probe local model servers, `/model` to pick provider/model/effort for the agent and classifier roles, `/settings` to edit settings, `/budgets` to set token budgets per provider and model (also `f1` then `b`), `/intel` for session intelligence: plan limits, pace, trend, runway and usage over time (also `f12` or `f1` then `i`), `/tts` to read replies aloud (`ctrl+b` reads one, see [docs/tts.md](docs/tts.md)), `/paste-image` to attach the image on the clipboard (also `ctrl+v`, and `@file.png` attaches an image file), `/permissions` to edit tool rules, `/prompts` to manage the prompt library, `/locate --dry-run` to see which files explore locate may look at and where it would send its questions (see [docs/jev-jobs.md](docs/jev-jobs.md#explore-locate)), `/kanban` to view and manage the global kanban board (also `f1` then `t`; see [docs/kanban.md](docs/kanban.md)), `/knowledge` to browse the documents Belai has indexed, with their types, labels and topics, for this project, every project or the agents, and to search them as a model's `Grep`, `Glob` and `Read` would (also `f1` then `n`; see [docs/knowledge.md](docs/knowledge.md#the-knowledge-screen)), `/processes` (or `/process`) to manage supervised long-lived processes, `/mode` to set the operating mode (`agent`, `plan`, `goal` or `auto`), `/handoff <plan file>` to execute a plan file directly with no intent detection, `/todos`, `/profile`, `/agent` to create, edit, and run background agents, `/agents` for the running agents, profiles, audit trail and fleet, `/fleet` to start (`/fleet start NAME`, `/fleet crew NAME`), stop and watch kanban worker agents (see [docs/fleet.md](docs/fleet.md)), `/resume` to resume a session by id or browse saved sessions, `/tree` to see the session as branches, continue from an earlier reply or prompt, or fork a branch into a new session (the file only grows; files on disk are not rolled back), `/lsp` to manage language-server diagnostics, `/execute` and `/refine` to run or refine an extracted plan, `/add-dir` to add another directory to the workspace, `/vulnetix` (with `review`, `configure`, `list`, `status`, `firewall`, `mcp` and `setup` subcommands), `/firewall` to configure the AI Firewall — Vulnetix, the beta Fastly, Kong and AI Security Gateway adapters, or any custom proxy — and pick the active one (also `f1` then `f`; see [docs/firewall.md](docs/firewall.md)), `/compact` to summarise a long session into a new one, `/clear` (or `/new`, `/reset`) to start a fresh session, `/yolo` to turn guardrails and the ask gate off together (`/yolo off` restores the settings-file values), `/export` to write the current session (or another by id) as Markdown under `.vulnetix/exports`, `/rename` to name the session, `/sync` to show or change session sync to the Vulnetix website (`on`, `off`, `backfill`; see [docs/session-sync.md](docs/session-sync.md)), `/rc` to set up and run remote control, which lets the Vulnetix website start and drive sessions on this machine (`start`, `stop`, `status`; see [docs/remote-control.md](docs/remote-control.md)), `/trusted` to list the directories you trust, which remote control offers, and revoke stale or temporary ones or trust a new one (also `f1` then `d`), `/voice` to dictate into the composer with a hold-to-talk key (`status`, `debug`, `on`, `off`, `download`, `push`, `listen`, `insert`, `submit`, `cleanup`; see [docs/voice.md](docs/voice.md)), `/skills` to list installed skills, `/sandbox` to see what commands may touch, `/mcp` to list MCP servers and their tools, and `/plugin` to list, enable, disable or remove plugins (install them with `belai plugin install`; see [docs/plugins.md](docs/plugins.md)). `/help` lists every command and keyboard shortcut. The popup matches fuzzily (`/pmt` finds `/prompts`) and also offers saved prompts as `/prompt:<name>` (loads it into the composer), agent profiles as `/agent:<name>` (switches to agent mode with that profile) and saved processes as `/process:<name>` (starts it unless it is already running, then shows its status).

Operator safety controls live in the footer: `guardrails: on|off` (posture gates) and `ask: on|off` (the permission-ask gate). `f3` toggles guardrails, `f4` toggles ask, and when both are off the two chips collapse into a single gold `YOLO`. These are explicit opt-ins: turning them off is announced in the transcript and traced under `BELAI_TRACE`. Guardrails off sets every posture gate to `ignore` across every surface — the agent loop, inline `!cmd`, `@file` attachments, background agents and the CLI — and the classifier is then not called at all rather than called and ignored, so a turn costs no extra requests. Sanitising is not part of the switch: delimiter markup is stripped either way. Next to them the footer always states `caveman: on|off`, so the voice rewrite (`f2`) can never be on without saying so.

In the composer, `ctrl+left` and `ctrl+right` move the cursor by word, crossing into the neighbouring line at a line boundary, and `home`/`end` (`fn+left`/`fn+right`) jump to the ends of the line. A word stops at punctuation, so `foo.bar` is three hops and `foo_bar` is one.

Shortcuts use `ctrl`, `shift` and the function-key row — never `alt`. `alt` chords are unreliable across terminals, and under the kitty keyboard protocol a `ctrl+alt+<key>` press is indistinguishable from `ctrl+<key>` by the time it reaches the UI, so it could never have worked. The session toggles are `f2` caveman, `f3` guardrails, `f4` ask, `f5` cycle mode, and `f6` cycle reasoning effort — all five from any screen — `f7` saves the prompt to the library from the composer, and `ctrl+s` saves the hovered panel, overwrites/deletes a loaded library prompt, or saves the prompt. If your terminal eats a function key, `/settings`, `/yolo`, `/model` and `/mode` do the same jobs.

In agent mode a strip above the prompt lists the agents that can carry your turns — your own profiles, the background-agent definitions (`↻`), and the built-ins (`◈`). `tab` moves the highlight, `enter` engages, `ctrl+g` starts a `↻` definition in the background instead, and typing `@name` filters the strip. The engaged agent shows in the footer chip and carries every turn until you pick another or `(none)`. An agent with a display name and palette (the built-in ones have both) is shown by that name, and the interface takes on its colours; `ctrl+p` cycles the agents and re-themes live. It applies to agent mode only: plan and goal mode run Belai's own logic and cannot be steered by an agent.

`shift+tab` cycles the mode: agent, plan, goal, then **auto**, where no mode is held and the classifier picks the mode and intent for each prompt. `/handoff <plan file>` runs a plan file as a handoff without that detection step; prompt admission, attachment classification and every permission gate still apply. **Goal mode** is the one that keeps going: instead of stopping when the tool budget runs out, an evaluator checks whether the work advanced and grants another pass while it does, tracking a todo list in a panel above the prompt. It is stopped by a stall, not a counter — press `esc` (or `ctrl+c` outside the UI) to stop it and keep the partial result. Set `resilience.max_passes` if you want a hard ceiling. The rules are in [docs/role-manager.md](docs/role-manager.md).

For a single answer without the UI:

```bash
belai -prompt "what does internal/run do?"
belai -provider anthropic -model claude-sonnet-4-5 -prompt "review this diff"
```

| Flag | Meaning |
| --- | --- |
| `-prompt` | send one turn, print the reply, exit |
| `-provider` | `openai`, `anthropic`, `cloudflare-workers-ai`, `cloudflare-ai-gateway`, `openrouter`, `google-gemini`, `ollama`, `llama-server`, `github-copilot`, `huggingface`, `kiro`, a custom name from `settings.json`, or a configured display label |
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
| `-usage-json` | with `-prompt`, write a JSON summary of the run's token usage (per role, per model, cache reads and writes, request composition) to a file on exit — see [docs/benchmarks.md](docs/benchmarks.md) |
| `-version` | print the version and exit |
| `-trust-dir` | trust the current directory without prompting (grants the directory only, not its proposed workspace dirs) |

Belai starts the UI only when both stdin and stdout are a terminal, so it is safe in pipelines and CI.

Run `belai -help` for every command, a set of examples and the flags grouped by purpose. `belai help <command>` (or `belai <command> -h`) prints one command's own usage. Subcommands sit beside the flags: `belai acp` serves the Agent Client Protocol so an editor such as Zed can use Belai as its agent ([docs/acp.md](docs/acp.md)), `belai agent` lists, runs and manages background agents and fleet workers ([docs/fleet.md](docs/fleet.md)), `belai kanban` reads and edits the global board ([docs/kanban.md](docs/kanban.md)), `belai login kiro` signs in to Kiro ([docs/kiro.md](docs/kiro.md)), `belai plugin` installs and manages plugins ([docs/plugins.md](docs/plugins.md)), and `belai rc` runs remote control so the Vulnetix website can start sessions here ([docs/remote-control.md](docs/remote-control.md)).

## Configuration

No provider configured? Belai defaults to OpenRouter's free router
(`openrouter` / `openrouter/free`). Sign up at <https://openrouter.ai/>, where a
one-time signup credit unlocks the free model, then store the key with
`/providers` (or export `OPENROUTER_API_KEY`). If exactly one *other* provider's
credentials resolve, Belai adopts that provider instead.

Set the API key for your provider and Belai picks it up:

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
| `kiro` | none: run `belai login kiro` (AWS Builder ID or IAM Identity Center sign-in; see [Kiro](docs/kiro.md)). `KIRO_LOGIN` holds a stored login |
| `groq` | `GROQ_API_KEY` |
| `deepseek` | `DEEPSEEK_API_KEY` |
| `fireworks` | `FIREWORKS_API_KEY` |
| `mistral` | `MISTRAL_API_KEY` |
| `together` | `TOGETHER_API_KEY` |
| `xai` | `XAI_API_KEY` |
| `moonshot` | `MOONSHOT_API_KEY`; optional `base_url` for `.cn` |
| `minimax` | `MINIMAX_API_KEY`; optional `base_url` for `.cn` |
| `alibaba` | `DASHSCOPE_API_KEY` or `ALIBABA_API_KEY` |

A custom provider defined in `settings.json` resolves its key from its
`api_key_env` variable or `BELAI_<NAME>_API_KEY`.

`/providers` in the UI opens the provider list where you can store them instead, in your host keychain or in `~/.vulnetix/belai/credentials.json`. Belai resolves credentials from the environment first, then a project-local `.vulnetix/belai/credentials.json`, then the user file, then `~/.netrc`, then the keychain — and tells you which one each value came from. A credential may be stored as the *name* of an environment variable rather than a value, which is how project credential files stay committable.

Pick a default provider without passing `-provider` every time by setting `BELAI_PROVIDER`.

For a custom state directory, set `BELAI_HOME`. To disable the TUI and keep the old one-shot behaviour, set `BELAI_NO_TUI=1` or run in CI (`CI` is honoured).

Belai creates `~/.vulnetix/belai/` at mode `0700` and credential files at mode `0600`. If you already have a `~/.belai/` directory, it is migrated automatically on the next run. We recommend adding `.vulnetix/belai/` to your repository `.gitignore` — even though the project file only holds references, defence in depth is cheap.

### Settings files

Declared intent lives in `settings.json`; last-used runtime values live in
`state.json`. The global file is `~/.vulnetix/belai/settings.json` (or
`$BELAI_HOME/settings.json`); the project file is
`<workdir>/.vulnetix/settings.json`. Precedence, lowest to highest:
`defaults` < `state.json` < global `settings.json` < project `settings.json` <
environment < CLI flags. The `/settings` browser shows the effective value and
its provenance for every key. From `/model` the agent provider/model/effort can
be written to `session` scope (`state.json`), `global` scope, or `project`
scope, following the same precedence rules.

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
| `update_check` | check GitHub for a newer Belai release at startup (default on) |
| `tests` | the post-end test pass: `post_end` (`off` default, `goal`, `goal_plan`, `session`), `command` (an argv that replaces the detected suites), `scope` (`affected` default or `full`), `on_fail` (`fix` default, `diagnose` or `off`), `max_fix_passes` (default 3), `timeout_seconds` (default 300) and `report` (default on). A completed goal, plan or session runs the suites, a passing run gets a fast-model report and a failing run a bounded diagnose-and-fix loop. A project may only turn it off and lower the budgets, never on — see [docs/testing.md](docs/testing.md) |
| `defer_tools` | advertise the core tools in full and load the rest (native catalogue, cloud CLIs, repo and agent-store tools, MCP tools) on demand with `ToolSearch`, keeping every request small (default on; `-defer-tools=false` for one run). Deferred tools stay callable by name. `ToolSearch` also finds installed skills, and with a decision backend it ranks its matches and the request's likely tools and skills are ready before the model asks (see [docs/jev-jobs.md](docs/jev-jobs.md)) |
| `offload` | keep oversized tool output out of the context: a head-and-tail preview stays inline and `ReadResult` reads the rest; `enabled` (default on), `threshold_tokens` (default 4000), `preview_tokens` (default 1500) — see [docs/context-offload.md](docs/context-offload.md) |
| `jev` | relevance jobs that use a decision backend: `jobs.<job>` switches (bash swap, compaction prune, tool selection and search, LSP triage, option order, explore locate, voice command, request scale and goal judge; default on, and only while a decision backend is the classifier, when `/settings` shows them) `locate_previews` and `thresholds` (the score cut-offs of the security gate and each job, defaults 0.10 allow and 0.90 deny); a project may only turn a job off, and cannot set `thresholds` — see [docs/jev-jobs.md](docs/jev-jobs.md) |
| `voice` | speech input to the composer: `enabled` (on by default when the speech model is built into the binary, as in every release family except the plain `belai` build; otherwise off), `mode` (`push_to_talk` default or `listen`), `delivery` (`insert` default or `submit`), `cleanup` (fast-model tidy-up streamed over the words as you speak, default on), `log` (voice notices and cleanup rows in the thread, default on), `wake_word` (act only on speech that starts with "Hey, Belay", needs `listen`, default off), `commands` (spoken keywords such as stop, option 2, submit, skip, approve and deny, plus Jev voice command, default on), `key` (`f11` default; `ctrl+space` if your terminal keeps it) and `device`. `/voice debug` checks the microphone, shows a live level meter, the raw and tidied transcripts and a key event log. The speech model is built into release binaries and run as pure Go (a build without it downloads 32 MB after `/voice download`); a capture helper (`parecord`, `arecord`, `ffmpeg` or `sox`) supplies the audio. Per-user only: a project file cannot turn it on — see [docs/voice.md](docs/voice.md) |
| `tts` | read replies aloud with a player card in the thread: `enabled` and `consented` (off by default; `/tts on` is the agreement to send the text to Microsoft's read-aloud service), `read_reports` (read each turn's final reply, default off), `voice` (default `en-US-AndrewMultilingualNeural`), `speed` (0.5 to 3, default 1) and `cache_mb` (the local audio cache for replay, default 256, 0 off); `ctrl+b` reads the reply under the pointer or the last one, and saying "stop" stops it; the project layer cannot set it - see [docs/tts.md](docs/tts.md) |
| `knowledge` | sizes of the retrieval store behind agent profile documents and the project's `.vulnetix` output, which `Grep`, `Glob` and `Read` search by meaning: `max_index_tokens` (per profile, default 200000), `max_project_tokens` (the project, default 200000) and `max_result_tokens` (one search, default 3000); the project layer cannot set it - see [docs/knowledge.md](docs/knowledge.md) |
| `kanban` | the global kanban board: tools, wrap-up, composer pane, `/kanban` and sync (default on; a project may only turn it off) — see [docs/kanban.md](docs/kanban.md) |
| `agents` | [fleet](docs/fleet.md) workers: `enabled` (default on), `max_workers` (default 4), `publish` (default on); a project may only turn them off or lower the cap |
| `token_budgets` | global only: token allowances per provider, model and scope (`session`, `day`, `month`) — see [Token budgets](docs/token-budgets.md) |
| `ui.budget_cycle_seconds` | seconds the footer shows each budget of the selected model before cycling (default 10, minimum 2) |
| `ui.budget_warn` | print a warning line on each call to the selected model while one of its budgets is amber or red (default off) |
| `ui.intel` | session intelligence: the footer slot, the intel tab (`f12`) and `/intel` (default on) — see [Token budgets](docs/token-budgets.md#session-intelligence) |
| `ui.clipboard_images` | let `ctrl+v` and `/paste-image` attach an image from the clipboard (default on; a project file can turn it off, never on) — see [Images](docs/image-attachments.md) |
| `intel.plan_limits` | global only: read provider rate-limit response headers into plan-limit readings (default on) |
| `context_windows` | per-model context-window overrides, in tokens |
| `providers` | custom provider profiles (see below) |
| `provider_labels` | display labels keyed by provider name (see below) |
| `allow_project_providers` | opt in to project-layer `providers` (default off) |
| `resilience.max_agents` | fan-out ceiling for explore subagents + background agents (default 15) |
| `resilience.plan_explore` | survey the repository before the first planning pass (default off) |
| `resilience.goal_explore` | survey a goal with references before its first pass (default off) |

**Custom providers.** A `providers` block defines a provider by name, with
`base_url`, `api` (`openai-chat`, `openai-responses`, or
`anthropic-messages`), optional `auth` (`bearer`, `x-api-key`, or `cf-aig`),
optional `api_key_env`, and a `models` catalogue. Secrets never live here; a
profile references the key via `api_key_env` or the credential backends.
Project-layer `providers` blocks are ignored unless the global settings set
`allow_project_providers: true`, because a hostile repo defining a provider is
an API-key exfiltration primitive.

A profile may also carry a `kind` (`ollama`, `llama-server`, or
`openai-compatible`) that templates it from a built-in descriptor, plus
`protocol` / `host` / `port` for the editor and the default display label.
`base_url` stays the authoritative wire value. A custom provider's API key is
always optional: with no key stored, Belai offers the provider when its
`/models` endpoint answers (the same liveness probe local servers get), so a
keyless self-hosted OpenAI-compatible endpoint works with no credential at
all. Add as many named instances as you need — a second Ollama, a second
llama-server — and give each a friendly name in `provider_labels`, which the
TUI shows in `/providers`, `/model` and the footer and accepts as a
`-provider` selector:

```json
{
  "providers": {
    "ollama-gpu": {
      "base_url": "http://localhost:11435/v1",
      "api": "openai-chat",
      "auth": "bearer",
      "kind": "ollama",
      "protocol": "http",
      "host": "localhost",
      "port": "11435"
    }
  },
  "provider_labels": {
    "ollama-gpu": "GPU Ollama"
  }
}
```

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

**Outbound requests identify themselves.** Provider calls, `WebFetch` and
`WebSearch` send `User-Agent: belai/<version>` plus `X-Belai-Session-Id`,
`X-Belai-Tool`, `X-Belai-Tool-Call-Id`, `X-Belai-Client-Version`,
`X-Belai-Client-Build` and a W3C `traceparent`. Tool subprocesses get the
same identity as `BELAI_*` and `TRACEPARENT` environment variables. The
session id is sent as-is, so any site you fetch can see it. See
[docs/architecture.md](docs/architecture.md#outbound-identification-and-trace-headers).

**Path rules are relative to the working directory.** Tools share one working
directory that starts at the session root and can move within it with `Cd`. A
path beginning with `/` means the session root; anything else is relative to
the current working directory. Nothing reaches outside the root either way —
a move changes how a path is spelled, never what it can reach — and the TUI
footer shows where the session currently is.

### Session storage

The TUI writes an append-only JSONL session per workdir under
`~/.vulnetix/belai/sessions/`. `/compact` never mutates the old file — it
writes a new session whose root entry links `meta.parent_session` to the old
id, and carries the old name forward. `/clear` starts a new session and leaves
the previous one on disk untouched.

Quitting (`ctrl+d` twice, `/exit`, or its alias `/quit`) prints a branded exit card below the
restored shell prompt: the session's display name, turn/duration/token facts,
its on-disk path, and the exact `belai --resume <id>` command that returns to
it. `belai --continue` (`-c`) reopens the most recent session for the current
project without remembering an id.

## Documentation

Highlights — full reference in [docs/README.md](docs/README.md):

- [Architecture](docs/architecture.md): [delimiter/nonce integrity model](docs/architecture.md#delimiter-nonce-and-integrity-model), [tool-result trust](docs/architecture.md#tool-result-trust), [TUI keybindings](docs/architecture.md#keybindings)
- [Role Manager](docs/role-manager.md): [security classification](docs/role-manager.md#security-classification), [posture gates](docs/role-manager.md#gates-and-defaults), [operating-mode classification](docs/role-manager.md#operating-mode-classification)
- [Resilience](docs/resilience.md): [error classification](docs/resilience.md#error-classification-internalresilience), [provider retries](docs/resilience.md#pre-first-byte-boundary), [semantic repair](docs/resilience.md#semantic-repair)
- [Token budgets](docs/token-budgets.md): per-model session/day/month budgets, the footer gauge, warnings, the usage ledger, and session intelligence (plan limits, pace, trend, runway, the `f12` pane)
- [Development](docs/development.md): prerequisites, `just` recipes, QA checklist, CI/release
- [Agent Profiles](docs/agent-profiles.md): reusable agent definitions, [schema](docs/agent-profiles.md#profile-schema), [background lifecycle](docs/agent-profiles.md#background-agent-lifecycle), [precedence](docs/agent-profiles.md#per-agent-defaults-and-precedence)
- [Agent Stores](docs/agent-stores.md): read-only context search with confinement guarantees
- [Vulnetix](docs/vulnetix.md): review scanners, the Vulnetix AI Firewall, project history
- [AI Firewall](docs/firewall.md): adapters (Vulnetix, Fastly ARC, Kong, AI Security Gateway, custom), modes, cards
- [Nonce endpoint](docs/nonce-endpoint-spec.md): provider/gateway `GET /v1/nonces` contract
- [Image attachments (deferred)](docs/image-attachments.md): multimodal design considerations
- [Marketing site](docs/site.md): source for belai.vulnetix.com
