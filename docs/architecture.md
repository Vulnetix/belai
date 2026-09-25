# Signet architecture

Signet is a role-managed, injection-safe LLM coding harness with a Codex-style
terminal UI. This document describes its security model and the modes it
exposes.

## Role Manager

The Role Manager is Signet's central safety boundary: security classification,
the sanitize → classify → decision pipeline, system/agent block boundaries,
tool-call and permission invariants, skill/hook validation, and operating-mode
auto-detection.

Its full business rules — sentinel tables, decision trees, and flow diagrams —
live in [role-manager.md](role-manager.md).

### Security classifier configuration

The security classifier is configured independently of the main agent model
through a `classifier` settings block (resolved by the standard precedence
chain: default < state < global < project prefs < project < env < flag):

```jsonc
"classifier": {
  "kind":   "models",           // "llm" | "models"; default models when embedded, else llm
  "provider": "openrouter",      // llm: omit → main provider; models: phase 3 (extraction/jailbreak) sentinel
  "model":    "typesafe/jev-1.13", // openrouter → the Jev Decisions gate model; huggingface → a curated BERT id
  "effort":   "none",             // default: reasoning OFF
  "caveman":  false,              // voices PROSE payloads only
  "chunk": { "max_bytes": 1048576, "concurrency": 4 },
  "phase1": { "model": "GuardrailsAI/prompt-saturation-attack-detector",
              "source": "embedded", "threshold": 0.75 },
  "phase2": { "model": "leomaurodesenv/bert-base-uncased-trustairlab-jailbreak",
              "source": "disabled", "threshold": 0.5 }
}
```

On a BERT-only or no-classifier binary an unset phase 2 is not `disabled`: it
resolves to `phase2.deferred` ("deferred to phase 3"), and phase 3's sentinel
broadens to cover `JAILBREAK`. `source: "disabled"` is only meaningful on the
jailbreak variant, where the embedded gate is available to turn off.

`kind` selects the stack. `"llm"` is the full five-token LLM sentinel.
`"models"` runs small BERT sequence classifiers in-process (cybertron/spaGO,
pure Go, no cgo) as the phase-1 prompt-saturation and phase-2 jailbreak gates,
with an optional phase-3 narrowed LLM sentinel for the two extraction
categories no purpose-built model reaches. The default is `"models"` on a
binary that embeds the weights, `"llm"` otherwise.

Flags `-classifier-provider`, `-classifier-model`, `-classifier-effort`,
`-classifier-kind`, `-classifier-phase1-*`, `-classifier-phase2-*`, and env
vars `SIGNET_CLASSIFIER_PROVIDER/MODEL/EFFORT/KIND/PHASE1_*/PHASE2_*` set the
same fields. The whole block is also editable from the TUI's `/model` page (see
"Classifier picker").

Business rules:

- **Phase 3 is opt-in, and the switch is the existing provider+model choice.**
  On the `models` path phase 3 runs iff `classifier.provider` **and**
  `classifier.model` are both explicitly set; there is no inheritance from the
  main model. Clear either and phase 3 is gone. A zero-config embedded install
  therefore stays fully local, with no network call in the classify path — and
  no `DATA_EXTRACTION` / `MODEL_EXTRACTION` coverage, which is stated, not
  implied, on the `/model` phase-3 row. An inconclusive phase-3 reply (a
  non-token or empty reply from the extraction LLM) is not a block: phases 1
  and 2 already ruled on injection/jailbreak, so it proceeds as `SAFE` while
  the feed records "couldn't tell".
- **The role classifier and the guardrail are split.** The non-guardrail
  role-manager activities (mode select, goal contract, clarify, plan eval,
  goal eval, compaction, session name, agent eval) run on the *role*
  classifier: the main provider/model under `routing.kind: "defined"` (the
  default), or the Jev-routed winner under `routing.kind: "routed"`. The
  exception is the one-token sentinel roles and the goal contract: they go to
  the fast tier whenever one exists, and under `routed` they never reach Jev
  (see [Fast tier](#fast-tier)). The
  security *guardrail* runs separately: `rolemanager.Pipeline.Security` is the
  ML stack on the `models` path, or the full five-token LLM sentinel (built
  from `classifier.provider`/`classifier.model`) on the `llm` path.
  `classifier.provider`/`classifier.model` therefore configure the guardrail —
  phase 3 on the `models` path, the sentinel on the `llm` path — never the
  mode/goal evaluator.
- **Default** (no block, LLM path): the classifier reuses the main
  provider/model with reasoning off and a bounded `max_tokens` cap (1024), so a
  single-sentinel call never pays for extended thinking. A reasoning-effort of
  `"none"` is *omitted* from the OpenAI `reasoning_effort` field rather than
  sent verbatim (OpenAI rejects it).
- **Separate provider** (LLM path): a `classifier.provider` that differs from
  the main provider is resolved through the same credential backends with its
  own credentials. Missing credentials fail closed with `ErrNotConfigured`.
- **Thresholds are user-adjustable, with per-phase defaults.** Each phase gate
  fires only at or above its attack-probability threshold. Phase 1 defaults to
  0.75 (the saturation model is effectively binary); phase 2 defaults to 0.5
  (the trustairlab jailbreak model is calibrated lower: benign tool output
  scores ~0.0–0.12 unsafe while known jailbreaks score ~0.6–0.75, so 0.5
  separates them and 0.75 would miss the DAN jailbreak). Tune per phase via
  `classifier.phaseN.threshold`, `-classifier-phaseN-threshold`, or the `/model`
  phase-threshold rows.
- **Phase 2 is opt-in even on the jailbreak variant.** The jailbreak variant
  embeds the phase-2 model, but it runs only when `classifier.phase2.source` or
  `classifier.phase2.model` is set explicitly. Embedding the weights makes the
  gate *available*, never *on*. The original embedded jailbreak model,
  `jackhhao/jailbreak-classifier`, over-triggered on ordinary tool results —
  code, listings, JSON, help text and test output scored as "jailbreak" above
  0.95, higher than the canonical DAN jailbreak (more false positives than true
  negatives) — so it was replaced with
  `leomaurodesenv/bert-base-uncased-trustairlab-jailbreak` (see
  "Phase-2 jailbreak model selection" in docs/role-manager.md).
- **Phase 2 is deferred, not disabled, when it cannot run.** On a BERT-only or
  no-classifier binary an unset phase 2 resolves `Phase2Deferred = true` and
  the `/model` phase-2 row renders **"deferred to phase 3"**. Phase 3's
  sentinel then broadens to cover `JAILBREAK` (`BuildDeferredExtractionPayload`
  / `ParseDeferredExtractionSentinel`). `source: "disabled"` is only reachable
  on the jailbreak variant, where it means the user explicitly turned the
  embedded gate off — a deliberate drop of jailbreak coverage, never a silent
  deferral.
- **Classifier provider allowlist.** The `/model` classifier provider list is
  restricted to classifier-capable sources: custom profiles, `llama-server`,
  `ollama`, `huggingface` (when `HF_TOKEN` is configured), and `openrouter`
  (when configured). The classifier model picker filters `huggingface` to the
  five curated BERT ids and `openrouter` to the Jev Decisions model
  (`typesafe/jev-1.13`, seeded) plus any `typesafe/jev*` ids; custom,
  `llama-server` and `ollama` stay unfiltered and show the broad-model
  warning. The agent/provider picker is unchanged. The Jev choice is the
  guardrail security classifier and tool-call gate; Jev is a Decisions model,
  never a chat model, so it appears on the classifier picker only and never on
  the agent or routing pickers.
- **Embedded models fail closed.** A variant binary whose embedded model fails
  to load or verify is a hard startup error, never a silent downgrade to the
  LLM path. Extraction and load happen once, eagerly.
- **Windowing.** The local models hard-error past 512 tokens and do not
  truncate. The ML path windows by BERT tokens inside `internal/mlclassify`,
  independent of the LLM `chunk` bounds. Content is limited to 508 tokens per
  window (512 minus [CLS]/[SEP] minus a 2-token safety margin for rare
  wordpiece boundary effects) with 1/8 overlap and a 64-window ceiling. Beyond
  `max_windows` it fails closed.
- **Chunked classify-all** (LLM path only): content over `chunk.max_bytes` is
  split into overlapping chunks (default 1/8 overlap, aligned to rune
  boundaries) and classified concurrently (default 4). Verdicts fold
  fail-closed: any non-SAFE sentinel fails the whole content, and any malformed
  chunk makes the whole result malformed. Overlap guarantees an injection
  straddling a boundary is seen whole by at least one chunk.
- **Empty content**: content that is empty or whitespace-only after
  sanitization is SAFE without a classifier call. It carries nothing to
  classify — a shell command that printed nothing cannot hold an injection —
  and the round trip both costs latency per silent command and sends a user
  message with no content field, which OpenAI-compatible servers reject.
- **Verdict cache**: verdicts are memoised by the SHA-256 of the *sanitized*
  content plus the classifier identity (kind + model ids + thresholds +
  phase-3 on/off), so switching classifier never serves a verdict produced by
  a different one. SAFE verdicts live in a bounded session LRU (512); non-SAFE
  hashes persist to `<GlobalDir>/bad-hashes.json` (written atomically) and load
  at session start. The bad-hash set stays small: memory is bounded and I/O is
  one read at startup plus an append per new bad verdict.
- **Caveman is prose-only**: `classifier.caveman` voices the three payloads a
  human reads — the compaction summary, the session name, and the generated
  agent profile — and nothing else. Sentinel payloads (security, mode, goal,
  plan, agent-loop) and the strict-JSON clarify payload are never voiced,
  because their replies are matched exactly and a rewritten reply would fail
  the parse and refuse benign content. The voice always ships with a structure
  guard telling the model to keep every heading, path and identifier verbatim,
  so `ValidateSummary`'s heading contract and the profile's JSON contract
  survive it. It is independent of the agent's own `caveman` setting: either
  can be on without the other.
- **Tri-state**: `classifier.caveman` is a `*bool`. Unset means off and claims
  no provenance, so `SIGNET_CLASSIFIER_CAVEMAN` unset or unparseable never
  overrides a stored value, and a stored `false` survives a round trip through
  the settings file rather than being pruned as empty. `ClassifierSettings.
  IsZero` counts the field, so a caveman-only block still merges.

### Model routing

The optional `routing` settings block selects a different provider/model for
specific role-manager activities without changing the main agent model. It is
*not* a security boundary; it is a performance/cost knob.

```jsonc
"routing": {
  "kind": "routed",               // "defined" | "routed"; default "defined"
  "use_cases": {
    "main":          { "provider": "openai", "model": "gpt-5" },
    "mode_eval":     { "provider": "openrouter", "model": "openai/gpt-5-mini" },
    "goal_eval":     { "provider": "openrouter", "model": "openai/gpt-5-mini" },
    "plan_eval":     { "provider": "openrouter", "model": "openai/gpt-5-mini" },
    "goal_contract": { "provider": "openai", "model": "gpt-5-mini" },
    "clarify":       { "provider": "openai", "model": "gpt-5-mini" },
    "compaction":    { "provider": "openai", "model": "gpt-5-mini" },
    "session_name":  { "provider": "openai", "model": "gpt-5-mini" },
    "agent_eval":    { "provider": "openrouter", "model": "openai/gpt-5-mini" }
  }
}
```

Business rules and edge cases:

- **"defined" mode is the default.** A missing block or `"kind": "defined"`
  means the main provider/model serves every role-manager activity, exactly as
  before. `ResolveRouting` returns no candidates and the classifier path is the
  plain main classifier.
- **"routed" mode selects per activity through Jev.** Each payload builder tags
  its `ClassifierPayload.UseCase`. On the first call for a use case, the Jev
  Decisions API (`jev.Client.Route`) scores the `routing.use_cases` candidates
  against the use case and `jev.SelectRoute` picks the single highest-scoring
  candidate; the winner is cached for the session and later calls for that use
  case reuse it. An empty use case resolves as `main`. A transport error, an
  inconclusive verdict (no unique winner), or a winner missing from the pool
  falls back to the defined main classifier — routing degrades to the main
  model, never to a dropped turn.
- **Jev candidates fall back to the agent model.** A use-case target may still
  name a Jev Decisions model (`openrouter` + `typesafe/jev*`) and it loads
  without error, but a Jev model cannot chat, so a routed winner that is a Jev
  model resolves to the main classifier at runtime. The `/model` routing
  pickers never offer `typesafe/jev*` models for this reason; they stay
  available on the classifier picker only.
- **Use-case keys are the single source of truth.** The known keys are
  `main`, `mode_eval`, `goal_eval`, `plan_eval`, `goal_contract`, `clarify`,
  `compaction`, `session_name`, and `agent_eval`. Unknown keys may be stored
  but are not consulted.
- **Candidates are validated.** A use-case target must set at least one of
  `provider` or `model`. Provider names are validated against the built-in and
  custom-provider allowlists. Invalid routing settings fail config resolution
  with an error, so a malformed routing table cannot silently redirect traffic.
- **Project-layer merging is key-by-key.** A project file can override one
  use case without restating the global routing table.
- **Security payloads are not routed.** The security/ML classifier stack runs
  its own configured model and never uses the routing table; untrusted content
  classification must remain governed by explicit classifier settings rather
  than a general routing knob.

### Fast tier

A prompt that reads five files used to make about seven full-size model calls
before its first edit, most of them to get back one token. The fast tier is a
small model, separate from the model the user picked for the work, that
answers those one-token calls.

```jsonc
"routing": {
  "fast_model": { "provider": "anthropic", "model": "claude-haiku-4-5" }
},
"classifier": {
  "tier": "main"                  // "main" (default) | "fast"
}
```

Business rules and edge cases:

- **The fast tier is on by default.** With no `routing.fast_model`, it is the
  main provider's registry fast model (`provider.Descriptor.FastModel`:
  `gpt-5-mini` for openai, `claude-haiku-4-5` for anthropic,
  `gemini-2.0-flash` for google-gemini, `llama-3.1-8b-instant` for groq,
  `mistral-small-latest` for mistral, `grok-3-mini-latest` for xai), on the
  main provider's credentials. A provider with no fast model (openrouter, the
  local servers, custom profiles) has no fast tier, and a fast model equal to
  the main model is no fast tier either.
- **It may be a different provider.** `routing.fast_model.provider` resolves
  through the same credential backends as any provider. An *explicit* fast
  model that cannot be configured is a resolve error; the *implicit* default
  is simply absent when unusable.
- **Sentinel roles and the goal contract move.** Mode select, session name,
  the goal, plan and agent evaluator verdicts, and goal-contract drafting
  (`run.IsFastUseCase`) default to the fast tier. The contract draft runs
  alongside exploration, and the goal loop waits only a bounded grace for it
  once it is needed (see [Goal mode](#goal-mode)). A slow reasoning
  main model used to miss that bound every time, and the goal ran on the raw
  prompt. Compaction,
  clarify and the final report stay on the main model: their output shapes
  the agent's later work.
- **Precedence per use case:** a fast use case goes to the fast tier whenever
  one exists, under `defined` and `routed` alike. Under `routed` it skips Jev
  entirely: no Decisions call, no `route_fallback`, and no pool candidate,
  even one keyed with the use case's name. A one-token verdict or a draft with
  a deadline is never handed to a slower pool model. Every other use case,
  and a fast one when there is no fast tier, takes the Jev-routed candidate
  under `routed`. If Jev does not settle it (a failed call, an inconclusive
  reply, a winner outside the pool, or a Jev Decisions model as the winner),
  it falls back to the main model. Under `defined` it uses the main model.
- **The security guard stays on the main model by default.** A smaller guard
  is less robust against prompt injection, and relaxation is an explicit
  opt-in, so `classifier.tier: "fast"` is required to move it
  (`run.GuardConfig`). An explicit `classifier.provider`/`classifier.model`
  outranks the tier. Classification still runs on every required kind; only
  the answering model changes. On the `models` path the tier moves phase 3.
- **The `/model` screen says what is in effect.** It opens with a four-line
  summary (work, verdicts, drafting, security) resolved from the live config,
  gives every group a one-line description, and adds a FAST TIER group
  (provider, model; stored in the routing block and sharing its scope) and a
  classifier `tier` row. Clearing the fast provider drops the whole target;
  clearing only the model keeps the provider and its registry default.
- **`tab` cycles the model mode.** From anywhere on the `/model` screen, and
  from the chat view when no slash popup, picker or history cycle claims the
  key, `tab` toggles the routing `kind` between `defined` and `routed` (the
  same row the `enter` key edits in place). In chat, a `model mode: <kind>`
  line names the result. The change is written to the routing block's
  scope and the footer's provider/model segment swaps to the "Smart model
  router · n models active" label the moment `routed` resolves to a non-empty
  pool, and back when `defined` or an empty pool resolves to the single main
  model. `s` still cycles the routing block's scope so `tab` saves where the
  user pointed it.

## Delimiter, nonce, and integrity model

Harness-generated blocks use tags such as:

```
<system nonce="…" integrity="…">content</system>
```

- **Nonce**: a 128-bit CSPRNG value from the nonce pool
  (`internal/nonce`). Only *reserved* nonces are valid; the pool supports
  reserve/release/rotate and falls back to local generation.
- **Known kinds**: `system`, `agent`, `plan`, `goal`, `tools`, `skills`,
  `hooks`, `attachment` (for `@file` / `!shell` contents), `exploration`
  (explore-subagent findings), and `directive` (harness continuation
  instructions injected into a running loop).
- **Integrity**: `integrity` is the lowercase hex SHA-256 of the enclosed
  content.
- **Egress verification** (`internal/delimiters`): before any payload leaves
  for a model provider, every block is checked. A block lacking a nonce,
  carrying an unknown nonce, or failing its integrity hash is stripped.
- **Sanitization** (`internal/sanitize`): every tool result is stripped of any
  harness delimiter markup (and nonce/integrity attributes) *before* it is
  ever wrapped in a delimiter, so adversarial text cannot forge tags. It is
  unconditional — results that skip the classifier are sanitized exactly like
  those that do not; see [Tool-result trust](#tool-result-trust).
- **Per-block kinds**: `rolemanager.SystemBlock` carries an optional `Kind`,
  defaulting to `system`. A kind that is not in `delimiters.KnownKinds` is
  refused at the boundary rather than sealed, because Egress leaves an
  unknown tag untouched and the block would reach the provider unsealed. The
  tool briefing uses this to seal as its own `<tools>` block.

See [nonce-endpoint-spec.md](nonce-endpoint-spec.md) for the provider nonce GET
spec (`GET {base_url}/v1/nonces`).

## Resilience

Transient provider failures are recovered in layers: typed provider errors,
pre-first-byte transport retry (blocking and streaming), turn-level retry in
the agent loop, and semantic repair of malformed tool calls. The model is
documented in [resilience.md](resilience.md).

## Provider layer

`internal/provider` + `internal/wire` reach every provider through a base URL,
an API key, a wire surface, and an auth style — speaking the three
ai-firewall surfaces:

| Surface                 | Path                    | Auth header           |
| ----------------------- | ----------------------- | --------------------- |
| OpenAI chat             | `/chat/completions`     | `Authorization: Bearer` |
| OpenAI responses        | `/responses`            | `Authorization: Bearer` |
| Anthropic messages      | `/v1/messages`          | `x-api-key`           |

Anthropic base URLs carry no `/v1`; OpenAI-style base URLs do. Streaming and
non-streaming request/response shapes live in `internal/wire`.

Compiled-in providers are: `openai`, `anthropic`, `cloudflare-workers-ai`,
`cloudflare-ai-gateway`, `openrouter`, `google-gemini`, `ollama`, `llama-server`,
`github-copilot`, and `huggingface`. Custom provider profiles can speak any of
the three surfaces with `bearer`, `x-api-key`, or `cf-aig` auth.

Both `ollama` and `llama-server` are local providers that need no API key.
`ollama` speaks the Ollama native endpoint (default `http://localhost:11434/v1`)
and `llama-server` speaks a llama-server / llama.cpp OpenAI-compatible endpoint
(default `http://localhost:8080/v1`). Each resolves from a single `base_url`
environment variable (`OLLAMA_HOST` and `SIGNET_LLAMA_HOST` respectively) or
from individually-managed host, port, and protocol fields in `/providers`.

### Request shape

`run.newRequestFactory` builds every provider request. The rules below are
what keeps a request both valid and cacheable.

- **Completion cap.** A request that sets no `MaxTokens` (the main agent)
  sends the model's ceiling from the catalogue (`ModelSpec.MaxOutput`),
  bounded to 64000 when streaming and 16384 when blocking. A Claude id not in
  any catalogue (a live-fetched or gateway-routed one) resolves by id rule
  (`models.MaxOutput`): Opus 4/4.1 32000, Sonnet/Haiku/Opus 4.5 64000,
  4.6+ and 5.x 64000–128000, 3.5 8192, older 3.x 4096. An unknown model on
  the Anthropic surface keeps 4096. The OpenAI-style route sends a cap only
  from a catalogue entry written for that exact provider (a relay may cap a
  model lower than its vendor), and OpenAI itself takes it as
  `max_completion_tokens` — its reasoning models reject `max_tokens`. Role
  calls keep their own small caps. A cap below a large `Write` truncated the
  arguments and cost a "re-issue with complete arguments" round trip.
- **Thinking follows the model generation** (`ModelSpec.Thinking`,
  `models.Thinking`), on the native Anthropic dialect only:
  - *budget* (Haiku 4.5 and older): `{type:"enabled", budget_tokens}` from
    the effort (1024 / 4096 / 16384), clamped to stay below `max_tokens`;
    below the 1024 minimum, thinking is left off.
  - *adaptive* (Opus/Sonnet 4.6+, Sonnet 5, Opus 5 before 5.5):
    `{type:"adaptive"}` plus `output_config.effort`. `budget_tokens` is a 400
    on these models. Effort `none` or unset leaves thinking off.
  - *always* (Opus 5.5+, Fable): thinking cannot be disabled, so only
    `output_config.effort` is sent. A role call's `none` asks for `low`, so a
    one-token verdict does not spend its small cap on thinking.
- **Signed thinking is replayed.** Thinking and redacted-thinking blocks are
  captured with their signatures (streamed `thinking_delta` +
  `signature_delta`, or the blocking response) and stored on the assistant
  turn (`Turn.Thinking`, `Turn.ThinkingModel`) as opaque provider data —
  model output, never sanitised into or promoted as a harness block. The next
  request of the tool loop echoes them, unchanged, ahead of the turn's text
  and `tool_use` blocks, as Anthropic requires. They are replayed only to the
  provider/model that produced them and only while thinking is on; any other
  model never sees them.
- **No per-request elision.** Tool results are sent exactly as recorded.
  The old sliding elision truncated every result older than three iterations
  on every request, so the model lost the bytes it had read and no provider
  could cache a history whose middle changed each time. Context is bounded by
  compaction at 70% of the window and, before that, by a one-step clearing at
  50%: every tool result but the newest ten is replaced in place with
  `[result cleared — re-Read if needed]`. The replacement is written to the
  conversation, so the prefix stays byte-stable between jumps, and a jump
  needs at least ten new results since the last one.
- **Cache breakpoints** (native Anthropic, `Descriptor.PromptCache`): the
  system prompt is sent as a block array with `cache_control:
  {type:"ephemeral"}`, and the last tool definition and the last content
  block of the newest message carry one too — three of the four allowed
  breakpoints. The shared tool slice is copied, never marked in place. A
  gateway or custom Anthropic-shaped server gets no `cache_control`.

### Outbound identification and trace headers

Every outbound HTTP request Signet makes identifies itself with one
`User-Agent`, built by `version.UserAgent()`:

```
User-Agent: signet/<version> (+https://github.com/Vulnetix/signet)
```

That covers provider and classifier turns, nonce fetches, the release check,
`WebFetch` and `WebSearch`. On top of it, `internal/calltrace` stamps trace
headers onto provider requests (`run.roundTrip`, the streaming `openStream`,
model-list fetches) and onto the `WebFetch`/`WebSearch` tool requests, so a
server, gateway or proxy can tie each request to its session, tool call and
build:

| Header                    | Value                                          | Sent on                      |
| ------------------------- | ---------------------------------------------- | ---------------------------- |
| `X-Signet-Session-Id`     | the transcript session id (as in `/resume`)    | provider + tool requests     |
| `X-Signet-Tool`           | registered tool name, e.g. `WebFetch`          | tool requests only           |
| `X-Signet-Tool-Call-Id`   | the model's tool-call id                       | tool requests only           |
| `X-Signet-Client-Version` | `version.Version`                              | every stamped request        |
| `X-Signet-Client-Build`   | `<commit>; <build date>[; <variant>]`          | every stamped request        |
| `traceparent`             | W3C Trace Context `00-<trace-id>-<span-id>-01` | provider + tool requests     |

The trace-id is the first 16 bytes of SHA-256 of the session id, so every
request in a session belongs to one trace. Each tool call gets a fresh random
span-id, and so does each provider request. Headers whose value is empty are
omitted. A request made without a session (the model list, the release check)
carries only the client version and build. The tool name and call id are
reduced to visible ASCII and capped at 128 bytes, so a model-chosen id can
never form an invalid header or split an environment entry.

Edge cases:

- **Tool with no session.** The tool name and call id are still stamped, but
  no `X-Signet-Session-Id` and no `traceparent`, because the trace-id is derived
  from the session.
- **Tool name spelling.** `X-Signet-Tool` carries the registered spelling
  (`Read`), not the model's (`read`). Tool lookup is case-insensitive, so the
  model's spelling can differ.
- **Span per tool call.** All requests within one tool call (a WebSearch
  backend query, say) share that call's span-id. Outside a tool call, each
  provider request mints its own span-id.
- **Nested contexts.** A later `WithTool` replaces the tool identity and span.
  The session is kept.
- **Redirects.** Go's HTTP client copies request headers onto a redirect, and
  only drops `Authorization`/`Cookie` when the host changes. So a
  `WebFetch` redirect (at most five) carries the same `X-Signet-*`,
  `traceparent` and `User-Agent` headers to the redirect target.
- **Reachability probe.** `WebSearch.Available()` runs with no context, so its
  `HEAD` carries only `User-Agent` and the client version/build.
- **Credentials.** None of these headers or variables ever carries a key or
  token. Auth headers stay with the provider layer.

Tool subprocesses (`Bash`, the native catalogue including `GH`/`Glab`, and the
`rg`/`grep`/`fd` behind `Grep` and `Glob`) receive the same identity through
environment variables. These are appended after `proc.ScrubbedEnv()`, so
scrubbing still removes every credential first:

| Variable              | Value                                   |
| --------------------- | --------------------------------------- |
| `SIGNET`              | `1`                                     |
| `SIGNET_VERSION`      | `version.Version`                       |
| `SIGNET_SESSION_ID`   | session id                              |
| `SIGNET_TOOL`         | tool name                               |
| `SIGNET_TOOL_CALL_ID` | tool-call id                            |
| `TRACEPARENT`         | same value as the `traceparent` header  |

`TRACEPARENT` follows the OpenTelemetry environment-variable propagation
convention, so an instrumented child process joins the session's trace.
Language servers and supervised processes are not tool calls and get no trace
variables.

**Where the session id comes from.** The TUI wraps each turn's context with its
current session id and pushes it into the background agent and process
managers (`SetSessionID`), including after `/new`, resume and plan fork.
Explore subagents inherit the parent's id. A headless `signet -p` run mints a
fresh id per invocation. The TUI's direct tool runs (inline `!cmd`, `@file`
admission, the file picker) carry it too.

**Privacy.** The session id is sent as-is. Any site `WebFetch` reaches and any
search backend `WebSearch` queries can see it, and can link together every
request from one session. It is an opaque random id and carries no credential,
path or prompt content.

**Conventions followed.** The scheme mirrors the ones other coding agents use:
Claude Code's `X-Claude-Code-Session-Id`, Codex's `session_id`/`originator`/`version`,
and Copilot's `editor-version`, namespaced under `X-Signet-*`, plus the
vendor-neutral W3C `traceparent`. Web Bot Auth (`Signature-Agent` with RFC 9421
message signatures) is not implemented. It is still an individual IETF draft
and is tracked as future work.

## Modes

`internal/modes` defines three modes; agent is the default.

### Agent mode

Interactive default. Profiles (`internal/profiles`, stored under
`~/.vulnetix/signet/profiles/`) are selectable at startup and mid-session via
`/profile`. Built-in profiles live under the `signet:` namespace; the debug
profile (`signet:debug`) is automatically engaged for `!cmd` inline-shell
round-trips. User files cannot shadow a built-in name.

Agent and goal mode carry a short *work-discipline* section in the system
prompt that tells the model to start editing as soon as the change is clear and
to interleave exploration with the edits. Plan mode deliberately omits it: plan
mode has a read-only contract and must not be told to start editing.

### Tool execution

Within one pass, tool calls execute in the order the model emitted them, with
one exception: the **leading run** of calls that are each parseable,
permission-allowed, and read-only runs concurrently, capped at 4 in flight —
unbounded fan-out against a rate-limited provider produces 429s, which is
worse than sequential. Anything else ends the run:

- a mutating kind — `Bash` (full mode), `Write`, or `Edit`: anything outside
  the read-only allowlist. A `Read` after a mutating tool that wrote the file
  must observe the write, so nothing reorders across a mutating call.
- A permission ask — two concurrent asks would race the UI, so the run stops
  at the first one and the tail runs sequentially with the ask.
- Malformed arguments or an unknown tool name — the call is answered
  (withheld) in order like the sequential tail.

Before the permission gate, `tools.CheckArgs` rejects any argument key the
tool's schema does not declare, naming it and the accepted keys. A key the
tool silently ignored (Bash's `run_in_background`, Read's `pages`) would
answer a different question than the model asked. The `file_path`/`path`
alias is accepted wherever either is declared. Grep's trained flags and
Bash's `timeout` and `description` are declared arguments (see below), so
they are honoured rather than tolerated.

`Bash` takes the trained `timeout` in milliseconds: default 120000, capped
at 600000, and a non-positive value is refused. A timed-out command still
returns what it printed, followed by `… command timed out after <d>`. The
read-only Bash on plan/explore surfaces keeps a 30-second default but
accepts the same argument. `description` labels the call for the user and
changes nothing about its execution.

Each tool result event carries the call's execution time (classification
included), which the transcript records as `duration_ms`; see
[Session store](#session-store).

Results match back to their transcript row by tool call id, so an
out-of-order completion from the concurrent group lands on its own row
rather than the newest tool row; legacy events without a call id fall back
to the last tool row.

### Tool-result trust

Every tool result is sanitized (`internal/sanitize`) before it can be
promoted: harness delimiter markup and nonce/integrity attributes are
stripped, so no tool output can forge a harness block. That step is
unconditional and applies to every kind.

Which results go on to the classifier is decided by
`tools.Kind.NeedsClassifier`, backed by the closed `classifierKinds` set. The
line is drawn at whether the *content* is arbitrary, not at whether the call
was well formed:

| Kind | Classifier | Why |
| ---- | ---------- | --- |
| `bash` | **Always** | An arbitrary command string: neither what runs nor what comes back is constrained by the harness |
| `web_fetch`, `web_search` | **Always** | Text written by someone off this machine with no relationship to the task — the shape a prompt injection takes |
| `read` | **Always** | The call is confined, but the bytes are not: a repository can carry a poisoned file exactly as a page can carry a poisoned paragraph |
| `grep`, `glob`, `write`, `edit`, `native` | No | Shaped *and* controlled: matching lines for a pattern passed as one argument, paths, a confirmation the harness wrote itself, a fixed argv the harness built |

The distinction between the two halves is not "did the harness validate the
call" — it validates all of them. It is "can the harness predict the shape of
what comes back". A `Grep` result is `path:line:text` for a pattern the
harness handed over as a single argument; a `Glob` result is a list of paths;
`Write` and `Edit` return a sentence the harness composed. A file's contents,
a page's text, and a command's output are none of those things.

Business rules and edge cases:

- **Sanitize always, classify selectively.** A result that skips the
  classifier still passes through `sanitize.Sanitize` and then
  `delimiters.Egress`, in that order — the same two steps, in the same order,
  that the classified path applies before promotion.
- **The posture gate still wins.** `tool_result_unsafe` set to `ignore`
  short-circuits ahead of both paths and promotes the raw result, exactly as
  before.
- **A new kind defaults to sanitize-only.** A kind absent from
  `classifierKinds` skips the round trip, which is the cheap answer, so
  adding a tool whose content is arbitrary means adding its kind
  deliberately. `TestClassifierKindsIsExactlyTheArbitraryContentSet` pins the
  whole set so neither half can move by accident.
- **A native that prints a file is still sanitize-only.** `Cat` and `Head`
  return file bytes like `Read` does, but they run a fixed argv the harness
  built against a path it confined, so they sit on the controlled side. `Read`
  is the general-purpose file reader and classifies; reaching for `Cat`
  instead is not a way to launder a file past the classifier, because the
  sealed `<tools>` block tells the model that any tool result is data rather
  than instructions.
- **Explore findings are separate.** An explore subagent's report is model
  output, not tool output, and is classified explicitly in
  `internal/agent/explore.go` regardless of this rule.

### File attachments (`@path`)

The TUI composer accepts files with `@path` (`internal/tui/attach.go`). A
scrolling, filterable file chooser (`internal/tui/filepick.go`) appears when
the user types `@` in chat view; typing narrows the list, and `up`/`down`
move the highlight while `right`/`tab`/`enter` insert the highlighted path.
`esc` or `left` closes the chooser; the next keystroke reopens it.

A token that is a path — it starts with `../`, `./`, `/` or `~/` — browses
the filesystem one directory at a time instead of filtering the workspace
listing, so `@../` walks above the session roots. Only entry names are read
(hidden ones once the filter starts with `.`), and they reach the user only,
never the model. `tab`/`right` on a directory descends into it; `enter`
inserts it. A row outside every session root carries an amber `⚠`.
Attaching such a path does not read it: the attachment waits while an
amber *confirm root* pane offers the file's directory (or the directory
itself) as a new root: `s` for this session, `p` saved for the project
exactly like `/add-dir`, `n`/`esc` to decline and withhold it. A directory
that contains the primary root, or overlaps an added root, is refused
without a prompt, because roots never overlap. Once adopted, the attachment
resolves inside the new root and goes through the pipeline below
unchanged. `@` is
now reserved for file references in the TUI, so `@agent:` is filtered like any
other prefix and is only interpreted as a named-agent directive by the role
manager when it appears in the submitted prompt.

Attachment admission pipeline:

1. **Path confinement** — `tools.SanitizePath` resolves the token against
   `App.workdir` and any workspace directories added with `/add-dir`; a
   traversal outside every root is rejected.
2. **Read, or list for directories** — a file is fetched by `tools.Read`
   (up to 64 KiB, binary rejected on NUL bytes). A directory is not read —
   it is listed in-process, entries sorted, one per line, subdirectories
   marked with a trailing slash, capped at 64 KiB with a truncation marker.
   That is the answer an `Ls` call would give, and a directory is never
   withheld as a malformed read.
3. **Guardrails gate** — `App.effectivePosture()` is checked **before** the
   classifier. With guardrails off, the file is admitted with no classifier
   round trip; sanitisation still runs.
4. **Classifier** — for `KindRead` file bytes the classifier runs
   unconditionally (an arbitrary repository file can carry an injection
   exactly like a web page). A directory listing is never classified: entry
   names are shaped, harness-known output (like `Grep`/`Glob`/`LS` results),
   so it is sanitised and admitted directly, with guardrails on or off.
5. **Decision** — safe attachments become `run.Attachment{Kind:"file"}`
   (directories: `Kind:"directory"`); rejected files are withheld from the
   model, and a sealed `<directive>` in the same user turn tells the model
   which tokens were withheld, why, and how to proceed.

Transcript preview rows are appended at submit time, immediately before the
user prompt echo, so they do not have to be removed if the user deletes the
`@token` before sending. A safe file renders as a `Read` tool row with a `✓`
status, line numbers, and syntax highlighting when expanded; a safe
directory renders as an `Ls` tool row with the listing; if a file's
worktree copy differs from the git index, a diff row renders beneath it via
`filediff.WorktreeChange` (directories have no diff). A rejected attachment
renders as a red `withheld` row with its sentinel.

Business rules and edge cases:

- **`@file` admission tries the primary workdir, then each added workspace
  directory.** The first root that legally contains the path is used for
  reading; a path outside every root is rejected.
- **The file chooser (`@`) searches every root.** Matches in the primary
  workdir are shown as root-relative paths; matches in an added workspace
  directory are shown as absolute paths so they can be passed straight back
  to `Read`.
- **A directory is listed, not read.** `@docs/` produces a sorted entry
  listing rendered as an `Ls` row — it is the one attachment that never goes
  to the classifier, because it contains no file content, only names the
  harness can account for.
- **An empty directory answers "(empty directory)"** rather than a
  zero-byte body that would be dropped from the model's turn while its
  preview row still promised a listing.

- **Rejected attachments never reach the model.** Neither their bytes nor a
  description of their contents is sent; only the harness-authored directive
  is sealed into the turn.
- **Images are not listed** in this round. `filepick.go` drops common image
  extensions; see `docs/image-attachments.md` for the deferred design.
- **Quoted paths** — paths containing spaces are inserted as `@"path with spaces" `,
  the same syntax `parseTokens` already accepts.
- **Guardrails off is all-ignore, not all-trust.** Sanitisation and egress
  verification still run; the file is simply not sent to the classifier.
- **Diffs are computed in the validation goroutine.** `filediff.NewRecorder`
  is created per attachment and `WorktreeChange` runs git against the index
  outside the Update loop, so the UI never blocks on a subprocess.

### The guardrails switch

`guardrails` is the operator's blanket control over the posture gates. Off, it
replaces the whole resolved policy with `posture.AllIgnore()` — every gate in
`posture.AllGates` set to `ignore`, defaults, project `preferences.yaml`, and
per-gate flags alike.

There is one definition of "off" (`posture.AllIgnore`) and one place the TUI
derives it (`App.effectivePosture`). That matters because the switch has to
reach several surfaces that each used to hold their own copy of the policy:

| Surface | How it gets the switch |
| ------- | ---------------------- |
| Agent session | A shared `posture.Live` held by `App` for the process, read at every gate (`s.live.Level` / `s.live.Policy` / `s.live.AskDisabled`) — a toggle mid-turn lands on the next gate check |
| Inline `!cmd` shell | `App.effectivePosture()` read per command |
| `@file` attachment admission | `App.effectivePosture()` read per attachment |
| Background agents | `Manager.SetPosture`, pushed by `App.syncPosture` whenever the switch moves; each running instance's own `posture.Live` is updated in place |
| `/permissions` preview | `App.effectivePosture()`, so the preview matches what would actually happen |
| CLI | `settings.GuardrailsEnabled()` in `cmd/signet`, which the `-guardrails` flag folds into first |

Business rules and edge cases:

- **Off means the classifier is not called, not called-and-ignored.** Every
  gated path checks the level *before* the round trip. Calling the classifier
  and then discarding its verdict would spend a request per prompt, per
  attachment, per `!cmd` and per tool result, and would send that content to
  the classifier turn anyway — the opposite of what turning guardrails off
  asks for. With every gate ignored a whole turn makes **zero**
  security-classifier calls.
- **Off never disables sanitising.** Delimiter markup and nonce/integrity
  attributes are stripped on every path regardless, and egress verification
  still runs. The switch turns off model-based judgement, not the structural
  guarantee that a tool result cannot forge a harness block.
- **A running session follows the switch.** `posture.Live` replaces the
  value snapshot `Session` used to take at construction. Posture gates, the
  permission-ask gate, and the explore subagents' gates read the holder at
  each check, so `f3`/`f4`/`/yolo` land on the next gate inside the running
  loop — foreground session, its explore fan-out, and live background agents
  alike.
- **The advertised tool surface stays frozen for the in-flight turn.**
  `Registry.PlanWith` and the sealed `<tools>` block are the advertisement
  half of the "advertisement and enforcement surfaces agree" invariant.
  Re-deriving them mid-turn would leave the model's already-sent briefing
  disagreeing with `modes.ToolAllowed`. The wider surface arrives on the next
  send, which `invalidateAgentSession` already rebuilds.
- **A pending approval prompt follows the ask switch.** Turning ask off (`f4`
  or `/yolo`) while the approval view is on screen resolves it as allow-once
  and dismisses it, so the blocked agent loop is not left waiting on a gate
  that is now off.
- **A running background agent follows the switch.** Each instance carries its
  own `posture.Live`, seeded with `choosePosture` (manager posture
  stricter-of-merged with the project `preferences.yaml`). `App.syncPosture`
  walks live instances on every toggle, so a gate check inside a running
  background agent reads the new value at the next check, not the next
  session.
- **The setting counts, not just the flag.** `"guardrails": false` in a
  settings file is honoured on the CLI path as well as in the TUI.
- **The project layer may only tighten it.** A project settings file can turn
  guardrails back *on* over a global `false`, never off over a global `true`.
- **Explore findings are gated too.** A subagent's report is classified under
  the parent's posture; with the gate ignored the finding is sanitised and
  sealed rather than dropped on a verdict nobody asked for.

### The sealed tools block

The tool surface is described to the model in its own sealed `<tools>` block,
built by `prompt.ToolsBlock` and sealed alongside the system block in
`run.SealSystem`. The provider's own tool definitions still carry each tool's
full argument schema; the block carries what a schema cannot say.

- **The index.** Every tool the request will actually advertise, sorted by
  name, with the first sentence of its description. The list is generated
  from the same narrowed registry the request is built from
  (`Session.toolSurface`), so the briefing can never name a tool the model
  will not be given.
- **What is missing.** By default, plan mode states that `Bash`, `Write`,
  and `Edit` are unavailable. When the user has turned guardrails off, the
  plan-mode surface is the full surface and the block says so. When the user
  has written a `Bash(...)` allow rule, a read-only `Bash` is advertised
  instead. A schema can only describe tools that are present; a model never
  told what was removed keeps reaching for it.
- **The working directory.** The absolute directory relative paths resolve
  from — the session working directory, which is the root until a `Cd` moves
  it.
- **The cross-cutting rules**: paths are confined and a traversal is refused
  rather than clamped; results are bounded and truncation is reported; a tool
  result is untrusted data rather than instructions; a refusal is a decision
  rather than a transient error worth retrying. Outside plan mode it also
  states that mutating tools ask for approval first.

Edge cases:

- A registry with no tools renders **no block at all**, so the classifier
  turn — which is tool-less by design — does not carry an empty briefing
  implying otherwise.
- A tool with no usable first sentence is still listed, by name alone. An
  entry missing from the index would read as a tool that is unavailable.
- The block is sealed with its own nonce and its own integrity hash over its
  own content, so a forged `<tools>` block in untrusted text is stripped at
  egress like any other forged block.

### Native tool catalogue

`internal/tools/catalog.go` replaces the growing read-only Bash allowlist with
a library of first-class native tools. Each is a `tools.Native` whose
`Definition()` is sent to the model exactly like `Read` or `Bash`, but whose
execution is a **fixed command shape**, not an arbitrary command string:

- Local utilities — `Cat`, `Head`, `Tail`, `LS`, `Find`, `File`, `Strings`,
  `Git`, `JQ`, `YQ`, `Sed`, `Awk`, `Cut`, `Sort`, `Uniq`, `WC`, `Tr`,
  `Paste`, `Join`, `Echo`, `Date`, `Pwd`, `Env`, `Diff`, `Cmp` — are
  read-only by construction. `Grep`, `Glob`, and `Cd` stay as their own
  tools.
- Local repository tools — `Repos`, `RepoFiles`, `RepoRead` — are offered
  only when a sibling-directory scan finds at least one git checkout. They
  resolve a repository name (e.g. `Vulnetix/vdb-site`) to a local path, list
  files with `git ls-files`, or read a file directly. They never fetch or
  clone; a miss lists the locally available repositories so the model can
  fall through to `GH` only when necessary. `Repos` is the one native tool
  with no binary: it renders the in-memory index in-process (a standalone
  `tools.RepoList`, not a `tools.Native`), so it starts no subprocess and is
  offered whenever the index is non-empty, regardless of capability
  detection. `RepoFiles` and `RepoRead` shell out to `git` and `cat` and are
  offered only when that binary was detected: they carry no capability entry
  of their own, so a machine without `git` never sees a `RepoFiles` it cannot
  run.
  An owner filter that matches nothing answers explicitly ("no repositories
  owned by …") rather than returning an empty listing that would read as
  "no repositories at all".
- Cloud/SaaS CLIs — `GH`, `AWS`, `AZ`, `GCloud`, `Kubectl`, `Terraform`,
  `Pulumi`, `Heroku`, `Fly`, `Vercel`, `Netlify`, `Doctl`, `Glab`, `Stripe`,
  `OnePassword`, `Bitwarden` — are offered only when capability detection
  finds the CLI installed **and** its read-only auth probe exits cleanly.
  Only read-only subcommands are offered: each CLI carries a prefix allowlist
  and anything outside it is rejected.
- `GH` has a second gate for `gh api`: only provable GET/HEAD requests to
  allowed path families (`repos/`, `orgs/`, `users/`, `user`, `search/`,
  `rate_limit`, `meta`, `gitignore/`, `licenses/`) are permitted, `graphql`
  must not begin with a `mutation`, and any write flag (`-f`, `-F`, `--field`,
  `--raw-field`, `--input`, `-X POST`) is refused. This is the gate that
  makes `gh api repos/OWNER/REPO/contents/...` available in plan mode.

Security rules for native tools:

- Every local tool is **read-only by construction** (`KindNative`), so it
  survives the read-only master switch and may run in the concurrent read-only
  fan-out.
- `GH` and `Glab` return arbitrary third-party text (PR bodies, issue
  comments, file contents), so they report `KindRemote`. Like `Read`,
  `WebFetch`, `WebSearch`, and `Bash`, `KindRemote` results go through the
  classifier before promotion. `RepoRead` is `KindRead` for the same reason.
- The `Repos` listing is harness-composed from the index — one
  `host/owner/name path` line per checkout — so it is shaped, controlled
  output on the sanitize-only side with `Grep`, `Glob`, and `LS`; the
  classifier round trip is reserved for the arbitrary bytes `RepoRead`
  returns.
- Arguments are Go `string`/`[]string` slices passed straight to
  `exec.Command(name, args...)` — never through a shell, so pipes,
  redirections, and `$` expansion are impossible.
- Path arguments go through `tools.SanitizePath` and cannot escape the
  working directory. Repository tools confine paths to the resolved checkout.
- Query-language arguments (`jq`/`yq` filters, `sed`/`awk` programs, `tr`
  sets) are data, not shell text; they are control-character gated and never
  interpolated into a shell `-c`.
- Output is capped (64 KiB default) and sanitized as untrusted content. A
  local native's argv is built by the harness from a fixed shape, so it is on
  the sanitize-only side of [Tool-result trust](#tool-result-trust).

### Search and file-location tools

`Grep` searches file **contents**; `Glob` finds files by **path**. Both report
paths relative to the session root so a result can be handed straight back to
`Read`, both are recursive from the search base, and both are capped (200
matches or paths by default) with the cap applied after sorting so the
truncated head is stable.

`Glob` has two enumerators but one matcher. `fd` enumerates when it is
installed and an in-process `filepath.WalkDir` does otherwise, but the pattern
is always applied by the in-house `matchGlob` — `*`, `?`, and `[…]` inside one
segment, `**` spanning zero or more segments.

That split is not an optimisation, it is the fix for a bug that made **every
recursive Glob return nothing**. `fd --glob` matches a separator-free pattern
against the file *name* and rejects a pattern containing `/` outright:

```
[fd error]: The search pattern '**/*.go' contains a path-separation character
and will not lead to any search results.
```

`fd` then exited non-zero, the error was swallowed, and the empty match list
looked like a legitimate "no matches". Keeping the matcher in-process means
the two backends cannot disagree about what a pattern means.

Business rules and edge cases:

- **The enumerators are made to agree.** `fd` runs with `--hidden`,
  `--no-ignore`, and `--exclude .git`, so it enumerates exactly what the walk
  enumerates. Without `--no-ignore` the same pattern would answer differently
  depending on whether `fd` happened to be installed.
- **`.git` is never enumerated** by either backend.
- **`fd` failing falls back to the walk** — not installed, killed, or
  erroring on this tree — so a real match can never be turned into an empty
  answer. An empty tree is distinguished from a failure and is not re-walked.
- **`path` scopes the pattern, not the report.** The pattern is matched
  relative to `path`; the results are still reported relative to the session
  root. With no `path`, the base is the current working directory.
- **A `path` that escapes the root is an error**, not a silently clamped
  search of the parent.
- The `backend` meta field (`fd` or `walk`) records which enumerator ran.

`Grep` takes the argument shape trained harnesses use, so a trained call is
honoured instead of rejected:

| Argument | Meaning |
| --- | --- |
| `glob` | only files matching the glob (`*.go`, `**/*_test.go`) |
| `type` | only files of a type (`go`, `py`, `js`, `ts`, `rust`, …) |
| `-i` | case-insensitive |
| `-n` | line numbers in content mode (default true) |
| `-A` / `-B` / `-C` | context lines after / before / around each match, capped at 20 |
| `output_mode` | `content` (default), `files_with_matches`, or `count` |
| `head_limit` | at most N lines or entries (default 200, max 1000) |

Business rules and edge cases:

- **The default mode is `content`, a deliberate divergence.** Claude Code
  defaults to `files_with_matches`; a model that omits `output_mode` here
  gets the matching lines, which is a superset of what it expected. The tool
  description says so.
- **The output stays shaped.** Content is `path:line:text`, context lines are
  `path-line-text` with `--` between groups, file mode is one path per line,
  count mode is `path:N` for matching files only (POSIX grep's `path:0` rows
  are dropped). Grep stays sanitize-only.
- **Context output is not re-sorted.** ripgrep runs with `--sort path`, so
  each file's context blocks arrive grouped and in order; sorting the lines
  would scatter a match's context.
- **Both backends honour every argument.** Without ripgrep, `type` maps to a
  fixed table of `--include` globs; an unknown type is an error rather than a
  silently unfiltered search.
- **Bounds are enforced, not trusted.** Negative context, a non-positive
  `head_limit`, or an unknown `output_mode` is refused.

`internal/tools/capabilities.go` performs detection at session construction:
local utilities by `$PATH`, cloud CLIs by `$PATH` plus a short, read-only auth
probe (`aws sts get-caller-identity`, `gh auth status`, `gcloud config
  get-value account`, …). Detection is cheap, non-mutating, bounded (probes run
concurrently under one timeout), and silent: an unverifiable tool is simply
not offered. `tools.DefaultWithCaps` builds the registry from the detected
`tools.Capabilities`; `tools.Default` (no native tools) remains for the
profiles/permission-editor name lists. The repository tools are the one
exception to "tool name → detection entry": `RepoFiles` and `RepoRead` have
no entry of their own and gate on `Capabilities.HasBinary`, offered only when
the binary they shell out to (`git`, `cat`) was detected as a local utility;
the in-process `Repos` listing needs no binary and is offered whenever the
index is non-empty.

### Working directory (`Cd`)

Two directories are in play and they are not the same thing:

- The **session root** is the confinement boundary. It is fixed for the life
  of the session and nothing moves it. Every path a tool touches resolves
  inside it or the call is refused.
- The **working directory** is where relative paths are interpreted from. It
  starts at the root and moves within it, so an agent working in a subtree
  can name files the way someone standing in that subtree would.

Moving the working directory is therefore **not a widening of authority**: the
set of reachable files is identical before and after, only the spelling of a
relative path changes. That is the whole reason the move is allowed.

`tools.Cwd` is the tracker. `tools.Default` builds one per registry and hands
the same pointer to every path-taking tool, and `Registry.Cwd()` exposes it;
every narrowing (`ReadOnly`, `Plan`, an agent profile's allowlist via
`Registry.Only`) carries the same pointer forward, because two trackers would
mean two answers to "where am I". It is mutex-guarded, since read-only tools
resolve paths from the concurrent fan-out.

**Resolution rule**, which the `Cd` description states to the model:

- a path beginning with `/` is matched against the primary session root **and**
  every added workspace root, longest match first; a path inside the primary
  root resolves there, and a path inside an added root resolves against that
  root.
- a leading `~/` expands to the user's home directory before that match, so
  `~/src/signet/README.md` is the absolute filesystem path the model meant,
  not a literal `~` segment.
- a path beginning with `/` that lands in no root is still session-root-
  relative (the `/`-is-root convention), so `/internal/tools` keeps meaning
  the primary root's `internal/tools`.
- any other path is relative to the **current working directory**;
- there is no fourth case. An absolute filesystem path outside every root has
  no spelling here and is refused outright.

The session root set can be widened during a session with `/add-dir`, or
by confirming an `@` path outside the roots (for the session only, or saved
for the project like `/add-dir`). Both check the directory against the live
root set (`Cwd.CheckRoot`) before anything is saved, so a directory the
tools would refuse is never persisted. A
confirmed added directory becomes an additional workspace root: absolute
paths that prefix-match it resolve there, and the tool briefing lists the
extra roots so the model knows the boundary. `Cd` still moves only inside
the primary root; added roots are reached by naming absolute paths. This is
a deliberate, user-confirmed relaxation of the default single-root invariant.

### First-run workspace trust

Signet refuses to touch a directory it has never seen without an explicit
"yes". `internal/trustgate` computes the decision from
`internal/projectregistry`: an `Entry` carries `trusted` / `trusted_at` plus
`accepted_project_dirs` / `declined_project_dirs` for the project-proposed
`workspace_dirs` the user has already ruled on. The gate runs in
`cmd/signet/main.go` immediately after `os.Getwd()` and **before**
`config.LoadMerged`, so no settings merge, posture load, repo-map scan, or
`autoStartProcesses` can happen in an untrusted directory.

- An unknown directory (no entry, or `trusted:false`) blocks startup with the
  absolute path and any proposed `workspace_dirs` listed in an amber panel;
  the cursor defaults to *No*. Esc is decline.
- A trusted directory whose settings grew a new `workspace_dirs` entry prompts
  for **only** the new directories. Declining records them in
  `declined_project_dirs` and continues normally; it does not exit.
- Headless invocations (`-prompt`, `-agent`, `-agent-create`, or any non-TTY
  run) fail closed with a message naming the directory and how to trust it.
- `-trust-dir` trusts the directory only — never its proposed
  `workspace_dirs`, which are printed as skipped — so the flag can never
  silently widen the sandbox. Guardrails-off does not skip the gate.
- Accepting trust is the same standing confirmation `/add-dir` has: the
  accepted directories run through the registry's `Abs → EvalSymlinks →
  IsDir → overlap` checks and land in both `workspace_dirs` and
  `accepted_project_dirs`. `internal/tui` installs them as real confinement
  roots (`Registry.Cwd().AddRoot`) when the agent session is built, so a
  trust-activated directory is not merely advertised to the model.
- Trust is keyed by `session.WorkdirKey(abs)` without resolving symlinks, so
  reaching the same repository through a symlinked path prompts again. That
  errs closed and is deliberate.

The `Cd` tool takes one `path` and reports where it landed
(`working directory: /internal/tools`, or `working directory: / (session
root)`). It reads nothing and writes nothing, so it is `KindNative` —
read-only by construction — and survives both the read-only master switch and
plan mode: an agent that may only look still needs to be able to look
somewhere else.

Business rules and edge cases:

- **A refused move changes nothing.** The target must exist and be a
  directory; a file, a missing path, a blank path, or a traversal out of the
  root all leave the working directory exactly where it was. A half-applied
  move would silently change what every following relative path means.
- **No sequence of moves escapes the root.** `..` from the root, `/..`, and an
  absolute path elsewhere on the filesystem are all refused.
- **Confinement is unchanged after a move.** A path argument that escapes the
  root is still refused; the move changed the spelling, never the reach.
- **Permission subjects are root-relative.** A path argument is rebased onto
  the working directory *before* the permission rule is matched, so a rule
  written against `secrets/**` keeps matching after a `Cd` into `secrets`.
- **`RepoRead`'s path is the exception to the rebase.** Its `path` is
  relative to the repository *checkout* — a different location on the
  machine — not to the session working directory. Joining it with the
  working directory after a `Cd` would read a file the model did not name, so
  the rebase skips tools whose path resolves against its own base (marked
  `noPathRebase` in the catalogue). The checkout-relative spelling, and the
  permission subject derived from it, are unchanged by a move, and a `..`
  escape is still refused — against the checkout, not the working directory.
- **An omitted optional path means "here".** `Grep` and `Glob` with no `path`
  search the working directory; a native listing (`LS`) with no `path` lists
  it. Results stay relative to the root either way.
- **A required path is never filled in.** A native that declares `path` as
  required and is called without one still reports the missing argument
  rather than being handed the working directory.
- **A nil tracker is the old behaviour.** A tool constructed without one
  resolves against the root directly, so nothing built by hand in a test
  changes meaning.
- **A new agent session starts at the root.** Rebuilding the session (a
  config, posture, mode, or profile change) builds a fresh tracker, and the
  TUI's displayed directory returns to the root with it.

The TUI follows the move: `EventCwdKind` is emitted after the tool call that
moved it, carrying the new location both root-relative and absolute. The
footer's first line shows the directory, and one informational system line
(`working directory: /internal/tools`) lands in the thread, so a reader
scrolling back can tell which directory the relative paths around it were
resolved against. A move to where the session already is is not announced.

### Write and Edit tools

`Write` and `Edit` are first-class mutating tools, alongside full-mode `Bash`.
Both confine paths to the workdir: `Write` resolves the deepest existing
ancestor and re-appends the new tail (`SanitizeNewPath`, so a file that does
not exist yet can still be written), while `Edit` requires the file to already
exist (`SanitizePath`). `Write` is bounded to 1 MiB, `Edit` to 1 MiB of file
content, and both write atomically (temp file + rename), so a failure never
leaves a half-written file. Results are terse confirmations —
`wrote src/x.go (412 bytes, 18 lines)` or `edited src/x.go (2 replacements)` —
and never echo file content back into the classifier round-trip.

`Edit` fails closed, in order: missing file; file over the size bound; binary
file (a NUL byte, matching `Read`); `old_string == new_string`; zero matches
(`old_string not found in <path>`); and multiple matches without
`replace_all=true` (`old_string appears N times in <path>; pass
replace_all=true or include more surrounding context to make it unique`). The
match is exact bytes with no whitespace or line-ending normalisation, and the
file is left byte-identical on every failure. This is deliberate: silent
normalisation is the classic source of "the edit landed somewhere else", so
the tool refuses rather than guess.

**Read before you change.** `Read`, `Edit` and `Write` share one
`tools.ReadState` per session (`tools.Default` wires it). `Read` records each
file it returns — a partial read with `offset`/`limit` counts — with its
modification time and size. Before touching an existing file, `Edit` and
`Write` check the record:

- a file never read this session is refused: *`<path> has not been read in
  this session; Read it first`*. An edit built on a guessed `old_string` is
  the failure this prevents.
- a file whose modification time or size changed since the read (a
  formatter, a Bash command, the user) is refused: *`<path> changed on disk
  since you last read it`*.
- a **new** file needs no read: there is nothing to be stale against.
- a successful `Edit` or `Write` records the bytes it wrote, so consecutive
  edits to the same file need no re-read.
- the refusal happens before any byte is written, so the file is unchanged.
- a tool built without a `ReadState` (a hand-built test registry) has no
  guard. Explore subagents have no `Edit`/`Write` at all.
- a `Read` whose result the classifier withholds still counts as a read: the
  record is taken when the tool runs, before classification. The guard is
  about stale or guessed bytes, not about what reached the model.

Every mutating call asks before it touches disk. In the TUI this is the
approval view (`viewPermissionAsk`): it shows the tool name, the normalised
subject path, and the diff the call would make (`filediff.Preview`, a pure
function over the row builder). Allow-once runs it, allow-always writes a
scoped `permissions.allow` rule first so the next matching call short-circuits,
and deny (or Esc) withholds without cancelling the turn. An explicit
`permissions.allow` rule skips the prompt; an explicit deny still blocks before
it. With no approver (the CLI, subagents) the existing
`permission_ask_no_tty` posture applies: enforce withholds naming
`-allow-ask-without-tty`, warn/ignore falls through to allow.

### Plan mode (read-only)

Plan mode narrows the tool surface in two places that must agree:

- `Registry.PlanWith` is the **advertisement** half. It removes every
  mutating tool and then removes `Bash` as well, so the request the model
  receives does not list a tool it may not call. The plan-only
  `ExitPlanMode` tool is registered in the base registry but filtered out
  of non-plan briefings by `Registry.WithoutPlanOnly()`, so agent and goal
  mode never see a tool that only makes sense during planning.
- `modes.ToolAllowed` is the **enforcement** half. It refuses the write-tool
  denylist (`write`, `edit`, `apply_patch`, `patch`, …) and refuses `Bash`
  outright, case-folded, whatever the command says. The advertisement and
  enforcement surfaces agree: a tool promised to the model is a tool the
  harness will run.

Business rules and edge cases:

- **`Bash` is gone, not restricted.** Read-only `Bash` is not mutating, so
  `Registry.ReadOnly()` keeps it; `Registry.Plan()` does not. An arbitrary
  command string is the one tool call whose effect cannot be read off the
  call itself, and its read-only guarantee rests on a command allowlist
  rather than on the tool's shape. Everything plan mode needs is covered by
  tools whose argument shape is fixed, so the exception is not worth its
  blast radius. `cat x` is refused there alongside `rm -rf /`.
- **`modes.BashAllowed` is still exported** — the read-only `Bash` tool and
  the `Git` native gate on it — but plan mode no longer consults it.
- **The surface follows the turn, not the session.** The mode classifier can
  route a single prompt to plan mode inside a session constructed in agent
  mode. Both tool surfaces are built once at session construction and
  `Session.toolSurface` picks per turn, so the advertised list, the sealed
  `<tools>` block, and the execution gate all move together.
- **Mode choices are sticky.** Any explicit user mode choice (`/mode`,
  `shift+tab`, `f5`, `--plan`, or Approve/Refine/Cancel in the plan review
  pane) holds for every following turn until the user explicitly chooses
  again. The classifier must not silently reroute a plan session into the
  unbounded goal loop.
- **Explore subagents build the plan surface directly**
  (`DefaultWithCaps(...).Plan()`) rather than relying on the gate alone, so
  the exploration preamble cannot promise a `Bash` the gate will refuse.
- **The briefing names the substitutes.** When the plan surface has no
  `Bash`, the sealed `<tools>` block says so and points at the read-only
  `Git` tool for git state and at `Read` with `offset`/`limit` for part of a
  file — each only when that tool is actually advertised. `Head` and `Tail`
  take no offset, and their descriptions point to `Read` for ranges. A
  session showed four Bash refusals and two rejected `Head` offsets in one
  plan turn before this.

**The plan is a structured document.** `ExitPlanMode` takes a required `plan`
argument — the full plan in markdown with `## Summary`, `## Steps` (numbered,
with optional `- Files:` and `- Verify:` sub-bullets), `## Test Plan`,
`## Assumptions`, and `## Risks`. The tool sanitises it, parses it with
`plans.ParseDoc` (which fails closed on an empty or zero-step plan), and only
then returns the sentinel; the pass loop threads the plan text to
`plans.Record`, which canonicalises it through `Doc.Render` so the file on
 disk has one stable shape regardless of the model's formatting. `update_plan`
(the Codex checklist tool, accepted here in plan mode too) drives the
planning todo list; the `[DONE:n]` marker convention remains as a fallback.

**`update_plan` is parsed leniently and counts as executed work.**
`tools.ParsePlanArg` is the single definition of the accepted shape, shared by
the tool and by the pass loop that adopts the list, so the two can never
disagree about whether a call was usable. It is lenient about spelling and
strict about substance: the checklist may arrive under `plan`, `steps`,
`todos`, `items`, `tasks` or `checklist` (as an array, or as a JSON string,
which is how some providers serialise tool arguments); an entry may name its
text with `step`, `description`, `content`, `text`, `title` and the other
paraphrases models reach for, or be a bare string; and a status the harness
cannot read is `pending` rather than a rejection. An entry with no text is
still an error, because there is nothing to track. A rejected `update_plan`
costs a whole iteration and teaches the model nothing, and the checklist is
bookkeeping — progress is measured from files on disk, never from this list.
A pass whose only successful call was `update_plan` is therefore *not* an
empty pass: counting it as one used to fail the whole goal loop with
*pass N executed no tools* even though the call succeeded.

### Repository map

Every session's system prompt may carry a **harness-computed repository map**
(`internal/repomap`), scanned once at startup. It holds facts only — module
root, remotes, language counts by extension, detected build/test/fmt/lint
commands from a fixed file table (`justfile`, `Makefile`, `go.mod`,
`package.json`, `Cargo.toml`, `pyproject.toml`), entrypoints, top-level
layout, and the presence/size of `AGENTS.md`/`CLAUDE.md` — and never
repository file contents. That is the invariant that lets the map enter the
system block: repository prose still reaches the model only through
`RepoRead`/`Read`, which classify. The map is an accelerant, never a gate; a
failed or empty scan renders nothing.

The facts that move with every commit or edit — branch, HEAD, the dirty flag
and the changed paths — are **not** in the system block. They are refreshed
before each turn (`Map.RefreshStatus`: one bounded `git status --porcelain
--branch` and one `git rev-parse --short HEAD`) and rendered by
`prompt.RepoStatusBlock` into that turn's sealed `<directive>` on the user
message, with nonce and integrity like any directive. They are still
harness-computed facts (porcelain codes and sanitised paths), so the repo-map
invariant holds; they moved only so the system block's bytes stay the same
from turn to turn (see [System prompt](#system-prompt)). A detached HEAD
reports no branch; a repository with no commits reports the branch it is on.

### Release check

Alongside the repo map and the Vulnetix CLI probe, startup runs one read-only
release check (`internal/selfupdate`): a `GET` of
`/repos/Vulnetix/signet/releases/latest` compared against the `-ldflags`
version stamp. It never downloads or executes anything — a newer release adds
an amber note to the banner's version row and one signet-panel notice naming
both versions, the upgrade command, and the release page.

The command is chosen from how the running binary was installed
(`vulnetixcli.DetectInstall` over `os.Executable()`): the Homebrew tap, the
Scoop bucket, `go install`, or the installer script plus the
`signet-<goos>-<goarch>` release asset. Detection is path-based, so an
unrecognised path falls back to the installer script and the asset URL rather
than guessing a package manager.

Three things keep the check quiet. The answer is cached six hours in
`signet-release.json` under the global config directory, so repeated sessions
make at most four requests a day. An unstamped build (`dev`) skips the check
entirely — there is nothing to compare, and telling a source tree to run
`brew upgrade` would be wrong. A failed fetch is stored and never rendered:
the banner and panel stay silent when GitHub is unreachable. The check does
not run at all when `update_check` is false or `SIGNET_NO_UPDATE_CHECK=1` is
set, so no request leaves the machine.

The release payload never reaches the model: it is harness chrome, rendered
in the TUI only, like the banner itself.
- Investigation in plan mode goes through `Read`, `Grep`, `Glob`, `Cd`, and
  the native read-only catalogue (`Cat`, `LS`, `Find`, `Git`, `JQ`, …).
- Toggle via `/mode plan`, `shift+tab`, `f5`, or `--plan`; `/todos` shows
  progress.
- After the agent emits a numbered plan, a full-screen review pane opens.
  It shows the persisted markdown file by absolute path and offers three
  actions:
  - **Approve** — sets `State.ActivePlan`, leaves plan mode, and immediately
    starts executing the plan with the full agent-mode tool surface. The
    approved plan text is loaded into the system prompt as the execution
    carrier.
  - **Refine** — stays in plan mode, sends the user's notes back to the
    planner, and writes a new revision (`<name>-rN.md`) of the plan file.
  - **Cancel** (or `esc`) — keeps the file on disk, does not set
    `State.ActivePlan`, and stays in plan mode.
- The three actions all route through `modes.RoutePlanOption` so `/execute`,
  `/refine`, and the pane agree on the resulting mode and action.
- Every plan-mode turn writes the model's full reply verbatim (after
  `sanitize.Sanitize`) to `<workdir>/.vulnetix/plans/<name>.md` at `0o600`.
  The file is written by the harness, not by a model tool call, because
  plan mode denies all write tools. The sanitized content is what can later
  re-enter the system prompt via `prompt.CarrierPlan` after a human approves
  it.
- Plan-mode state (enabled/executing/todos) is persisted as session entries so
  it survives resume. `rehydrateTodos` falls back to the latest `plan_state`
  entry when no `todo_list` entry exists.
- A top-level plan-mode prompt runs a **plan pass loop** instead of the
  bounded continuation path: when a pass exhausts its iteration budget — or
  ends naturally — a **plan evaluator** decides whether the plan is ready to
  execute. It is shown the exploration context the explore agents gathered
  for the session (never a goal definition: plan mode has none), the plan
  todo list, and the pass evidence, and it answers with `PLAN_*` sentinels,
  never `GOAL_*` ones. The loop is bounded by `resilience.max_passes`
  (default `defaultPlanContinuations` = 5) and returns the plan so far at the
  ceiling. The planning model can also declare completion by calling the
  read-only `ExitPlanMode` tool, which short-circuits the evaluator.
  The normative rules are in [role-manager.md](role-manager.md),
  "Plan pass loop".

### Agentic exploration (plan-mode explore subagents)

When a plan-mode (or referenced goal-mode) prompt engages exploration, the
parent session runs a **grounding probe** first, then fans out **explore
subagents** that investigate with the native read-only tool catalogue before
any clarification questionnaire is shown.

The grounding probe (`internal/agent/grounding.go`) attaches always-useful,
read-only evidence: `git status`/branch/recent commits, a bounded top-level
directory listing, `AGENTS.md`, and the single-shot background agents that
are not scheduled/loop/monitor definitions. When workspace directories have
been added, the probe gathers the same evidence from each root. This evidence
is untrusted — it re-enters as part of each subagent prompt and is admitted
through the Role Manager like any user content, never promoted into a
system/agent block.

An explore subagent runs its turn with a forced agent mode
(`TurnInput.ForceMode`), so it never spends a mode-select call or drafts a
goal contract it would not use. It is still a read-only session on the plan
surface, and a read-only session never receives the work-discipline
guidance.

Each explore subagent:

- receives the original prompt, the grounding evidence, and an investigation
  angle derived from the prompt's `@references` (or a codebase survey for
  goal mode);
- has its own read-only `agent.Session` (`PlanMode`, no further fan-out) that
  inherits the parent's added workspace roots, with
  a dedicated iteration budget from `resilience.max_explore_iterations`
  (default 8, deeper than the historical 4), and a system-prompt preamble
  telling it to discover facts with the native tools rather than ask;
- runs read-only tools (`rg`/`Grep`, `find`/`Find`, `git`/`Git`, `cat`,
  `jq`, …) to investigate, returning a findings report;
- has its findings classified and, if SAFE, sealed as an `<exploration>`
  block that re-enters the parent as an untrusted user turn.

Subagents run in parallel bounded by the shared FIFO agent pool
(`resilience.max_agents`, default 15) and `explore.MaxTasks` (12). The pool is a `container/list` FIFO queue, not a
buffered-channel semaphore, so waiters are admitted in arrival order; `esc`
drops queued work at once and `x` on a running chip cancels it through the
pool. `resilience.plan_explore: false` skips the survey entirely so plan mode
starts planning immediately. **Reset-on-steer**: an explore subagent that
exhausts its iteration budget does not hard-fail when new steering arrives —
the steering message is broadcast to the running subagents and each one
restarts its budget and keeps investigating. Only when no new steering exists
does the budget exhaustion surface.

Clarification is now **gated on findings**: the clarify loop runs only when
exploration produced non-empty findings and the planner classifier returns a
non-empty questionnaire. A zero-findings wave no longer triggers a
questionnaire.

### Goal mode

Goal-mode state is stored under `.vulnetix/goals/`. It is entered with
`/mode goal` or by a mode classifier routing the prompt to goal mode.

A goal-mode prompt at the top level runs a **pass loop** instead of a single
bounded tool loop: when a pass exhausts its iteration budget, a goal evaluator
decides whether the work advanced, and the loop continues while it does. The
loop is unbounded by design — it is stopped by a stall, not a counter — and
`esc` (or `SIGINT` outside the TUI) returns the partial result cleanly. The
normative rules, including the verification gate and every termination
condition, are in [role-manager.md](role-manager.md), "Goal pass loop".

A prompt the classifier routes to goal mode drafts a **goal contract** from
the sanitized prompt and the repo map's detected test commands. The user's
prompt is kept verbatim as the Objective line, and the drafted sections
(verification surface, constraints, boundaries, iteration policy, blocked-stop
condition) are appended beneath it. The draft is sanitized before sealing so
it cannot forge a harness block; on transport failure, a timeout, an empty
draft, or a draft missing the objective, the raw prompt is carried instead and
a warning naming the cause is emitted (a timeout, a provider status code, or an
unusable draft — never the provider's response body). The draft runs on the
fast tier (see [Fast tier](#fast-tier)). A memorised goal is user-authored and
is carried verbatim — never drafted.

The draft has two bounds (`internal/agent/goalstart.go`):

- **Grace, 45s.** It starts when exploration and clarify are done and the
  goal loop needs the contract, not when the draft starts. Time spent
  exploring is free: a draft that finishes during exploration is used at
  once, whatever it took. When the grace expires, the draft is cancelled with
  a deadline cause and the raw prompt is carried. The warning reports the
  draft's total age (`timed out after <d>`), and the `goal_draft` activity
  records `timeout`, not `error`.
- **Ceiling, 2 minutes.** It bounds the draft itself, from its start. It
  guards against the stalls that once held a goal back for hours behind a
  slow routed model.

The previous single 20s deadline, counted from the start, killed a reasoning
model's ~30s draft even while a minute of exploration was still running. A
turn that ends before the join cancels the draft.

The first goal pass is a work pass, not an acknowledgement pass: the directive
asks for one `update_plan` call and the first real change in the same pass.

**A pass that changes no file has not advanced the goal.** The loop's primary
progress signal is the file-diff recorder's observation of each pass, not the
model's prose or its checklist: two consecutive passes with no file change
inject a directive naming the next step and asking for the smallest correct
edit, the `GOAL_COMPLETE` verification gate asks for the edit rather than a
read-only re-check while nothing has been written, and the goal evaluator is
shown the change counts as harness facts. When the pass also ended with every
tool result withheld, the loop injects the tool-repair directive instead of
the no-write directive, because a broken path resolver is not fixed by telling
the model to stop investigating. A goal whose every pass ended all-withheld
terminates with a tool-failure error rather than looping unbounded.
costs a model call — the loop evaluates and starts the next pass.

Subagents never enter a pass loop — plan or goal: `AllowPassLoop` is a
separate authority from `AllowExplore` and only top-level session
construction sets it, so an unbounded loop can never spawn recursively.

#### Run-time goal state

Two different things are called "goal state", and they live in different
places:

- **Memorised goals** — `internal/goals`, files under
  `<workdir>/.vulnetix/goals/<name>.md`. A name is slugged to
  `[a-zA-Z0-9._-]`, runs of anything else collapse to `_`, and leading or
  trailing `.`, `_`, `-` are trimmed; a name that slugs to nothing is
  rejected rather than written. These survive across sessions and feed
  slash-command autocomplete.
- **Run-time goal state** — `goals.GoalState`, persisted as a `goal_state`
  **session entry** (`EntryTypeGoalState`), mirroring how `plan_state` is
  stored. It belongs to one run of the pass loop and is rebuilt per turn.

`passloop` creates a `GoalState` when the loop starts (`NewGoalState`:
version 1, a fresh session id, status `active`, `createdAt`/`updatedAt` in
unix millis) and re-emits it at **every pass boundary**, after the pass
returns and after the todo list is advanced, carrying `passes`, cumulative
`tokensUsed`, and `timeUsedSeconds` measured from loop start. The TUI turns
each `EventGoalStateKind` into a session entry via `ToEntry`, which stamps a
fresh `updatedAt` and a fresh entry id — so the session log holds the whole
history of the goal, not just its last value, and `LatestGoalState` reads the
**last** parsable `goal_state` entry back.

Business rules and edge cases:

- **Progress is reported at pass boundaries only.** A pass that fails and is
  retried after a compaction emits one state for the pass, not two, because
  the emit sits after the retry.
- **A cancelled or errored loop emits no terminal state.** `esc` returns the
  partial result immediately; the last `goal_state` entry is therefore the
  one from the final completed pass, and its status stays `active`. Reading
  `active` back does not mean the loop is still running.
- **`complete` is written twice over, deliberately** — once on the natural
  exit after the verification pass, and once on the exhausted-budget path
  that reaches the same verdict. Both go through the same `ToEntry`, so a
  reader only ever sees the last one.
- **Unparsable entries are skipped, not fatal.** `LatestGoalState` ignores
  any `goal_state` entry whose JSON does not parse and keeps scanning, so one
  corrupt line cannot hide an earlier good state.
- **`tokenBudget`, `paused` and `budget_limited` are reserved.** They exist to
  leave room for a budgeted, pausable goal and are never written by this
  implementation; `tokenBudget` omits itself from the JSON when nil
  (unbounded), which is always. Do not branch on them yet.
- **`tokensUsed` counts only passes that reported usage.** A provider that
  returns no usage block contributes zero rather than an estimate — the field
  is an anchored count, not a guess.

### Todo list

`internal/todos` owns the single todo list a session tracks, whatever mode
produced it — goal mode, plan pursual, and agent loops all write into the same
structure, so the TUI has one thing to render and resume has one thing to
rehydrate.

- Items are 1-indexed and stable, because `[DONE:n]` markers refer to them.
  Exactly one not-done item is `active` at a time.
- `plans.ParseDoneMarkers` is the single definition of the marker syntax.
  Markers are applied **only** to model-authored assistant text; a `[DONE:1]`
  in a tool result or a repository file must never mark work complete.
- An empty list is not complete. "Nothing to do" is not "finished", and
  treating it as finished would let a goal loop stop before it wrote a plan.
- Lists persist as append-only `todo_list` session entries, latest wins — the
  same shape `modes.PlanState` uses. Completing a list does not delete it; a
  later entry supersedes it and the old one stays readable in the JSONL.

### Background agents

`internal/bgagent` runs named, reusable agents defined by `internal/agentprofile`
profiles in the background. Profiles are stored under
`~/.vulnetix/signet/profiles/agents/` and specify a system prompt, tool
allow-list, operating mode (`single`, `loop`, `scheduled`, `monitor`), and
autonomy level (`supervised` or `autonomous`).

The TUI integrates background agents via `/agent create <name>`, `/agent edit`,
`/agent start`, `/agent pause`, `/agent resume`, `/agent stop`, `/agent list`,
and `/agent log`, and through the `/agents` screen. `/agents` has three tabs,
switched with `1`, `2`, `3` or `tab`: **running** lists every subagent and
background agent seen this session with its state, running tool, iteration,
tool count, elapsed time and latest output line; **profiles** is the profile
list and editor; **audit** is the step-by-step trail (state changes, tool
calls, results, replies, errors) held in a bounded in-memory ledger of 1000
rows. On the running tab `enter` follows an agent's thread in chat, `p` pauses
or resumes a background agent, `x` stops it (or cancels or dismisses a
subagent), and `a` narrows the audit tab to it. `/agent list` opens the
profiles tab and `/agent log <name>` opens that agent's audit trail. Audit rows
are flattened to one line with control and bidi runes removed, and the ledger
is display-only: nothing in it reaches a model.

The profiles tab discovers every stored profile, shows the
file path for each, and highlights running instances; `s` starts the
highlighted profile in the background, and pressing `e` (or `enter`)
opens a sectioned editor covering the full `AgentProfile` schema — name, file
name, description, system prompt, tools, mode, schedule, monitor condition,
provider, model, effort, autonomy, guardrails, ask permission, reflection, and
max iterations. `n` creates a valid stub, `d` duplicates (and thereby makes an
editable copy of a read-only built-in), and `esc` returns to the list.
`/agent create <name>` is name-first: the name is validated locally, the
designer runs visibly behind a `Silent` activity row and a composer phase, and
after the builder returns the profile's `Name` is forced back to the requested
name before save. On success the new profile is selected and the editor opens
automatically; on failure a valid stub is saved and the editor still opens.
A background agent's events go to its own thread, keyed `bg:<name>`: streamed
reply deltas are joined into one row, and each tool call and its result share
a row. The main view hides these threads; it shows one line when the agent
starts, one when its loop ends, and one per error. Following the thread (from
`/agents`, the `f8` runs panel, or the audit tab) shows it in full, and `esc`
returns to main. Thread rows carry a `SubagentID`, so they never enter
`buildTurns` and survive `/resume` like explore rows do.

`loop` mode treats `max_iterations` as an *inner* budget: when it is exhausted,
an agent-loop evaluator decides whether to continue, pause, sleep one schedule
interval, or stop. Supervised profiles are paused rather than continued, and a
malformed or unreachable evaluator fails closed to pause. See
[agent-profiles.md](agent-profiles.md).

## Session store

`internal/session` persists append-only JSONL session trees
(`id` + `parentId`) under `~/.vulnetix/signet/sessions/<workdir>/<session>.jsonl`, with
fork/resume reads (full or partial UUID) and display names.

The TUI owns a live session: `App.sessionID` is minted at launch, entries are
appended lazily (no file until the first message), and the footer shows the
session name or short id. Entry types:

| Type | Role | Content |
| ---- | ---- | ------- |
| `user` | `user` | the prompt |
| `assistant` | `assistant` | the reply, with `prompt_tokens` / `completion_tokens` / `total_tokens` / `model` / `provider` / `mode` / `effort` / `tool_calls` / `duration_ms` / `model_calls_ms` in `meta` |
| `tool` | `tool` | a tool result, with `tool_call_id` / `tool_name` / `tool_args` / `status` / `duration_ms` in `meta` |
| `reasoning` | `reasoning` | streamed chain-of-thought shown in the `model · reasoning` panel |
| `system` | `system` | a TUI system notice |
| `rolemanager` | `rolemanager` | a role-manager decision line, with `summary` / `outcome` / `tone` / `level` in `meta` |
| `completion` | `completion` | the harness-composed agent-mode completion panel body (never sent to a model) |
| `session_name` | *(empty)* | the name; append-only, latest wins, empty clears |
| `session_meta` | *(empty)* | per-session JSON: `schema`, `cwd`, `version`, `createdAt`, `resumedFrom`, `originCwd`, `activePlan`, `activeGoal`, `activeProfile`, `mode` |
| `summary` | *(empty)* | a compaction summary; `meta.parent_session` links the source session |
| `todo_list` | *(empty)* | the tracked todo list as JSON; append-only, latest wins, `cleared` marks a superseded list |

`/compact` creates a **new** session whose root entry is the summary and links
the old id via `meta.parent_session`; the old file is never mutated, truncated,
or deleted. Naming is append-only: the last `session_name` entry wins. A
tracked todo list is re-appended under the new session id so the panel and the
new session file agree. `/clear` drops the list instead: it belongs to the
session that produced it.

Resume reads a session back into the running TUI in place: `signet -r <id>`
opens the transcript, todos, plan/goal state and model, and `/resume` browses
every session on disk (current project first). Tool turns are persisted as
they happen so a resumed history keeps its tool activity; assistant `tool_calls`
and `tool` result entries pair by id on rehydrate, and unpaired records are
dropped rather than sent to a provider. A session's project is addressed by a
`Key` (`<basename>-<8 hex sha256>`), distinct from a workdir, and the recorded
`session_meta.cwd` is trusted only when it hashes back to that key. Same-project
resume appends in place; cross-project resume rehydrates the origin read-only
and forks the continuation into the current project, recording `resumedFrom`
and `originCwd`, because `App.workdir` is the tool-confinement boundary and
must never be silently widened to another tree. Legacy schema-1 files rehydrate
text-only (tool history predates persistence) and gain a backfilled
`session_meta` on first same-key resume.

Entries are appended by `persistTail`, which writes only *settled* messages: a
tool row waits for its result, and an assistant `tool_calls` entry waits for
exactly its own result rows so the file never holds an unpaired call. A
trailing assistant is written once the turn finalises (`Materialise`), and a
failed turn writes the settled tail too, so a terminal agent error still leaves
the model's streamed replies and completed tool results on disk. Reasoning
rows are written once they finalise; system and role-manager rows are written
as they are appended, so the session file holds the whole TUI transcript. They
rehydrate as render-only rows and are never promoted into provider turns.
Multi-pass goal/plan turns persist every finished assistant reply, not only
the final one: a natural-exit reply is finalised at the pass boundary (its
buffered text counts even before the turn ends), and a tool-call reply is
written once its results land.

**Timestamps are event times, not flush times.** Rows are written in batches
(an assistant bubble is held until its turn ends), so an entry stamped at
write time said nothing: one 894-second session had all 373 entries within
9 ms of each other. Now every agent event carries `At`, stamped once on the
agent's emit path (`stampEvents`), and every role-manager activity carries its
own `At`. A transcript row takes the time of the event that created it
(`Message.CreatedAt`), and `timestamp` on disk is that time. Rows appended
immediately (the user prompt, session name) use the write time, which is the
same thing.

**Durations say where the time went.** `meta.duration_ms` is recorded for:

| Entry | Measured |
| --- | --- |
| `tool` | the call's execution, classification included (`EventToolResultKind.Duration`); falls back to the span from the row's start to its result |
| `assistant` | the sum of its provider calls, with each one in `meta.model_calls_ms` (`EventModelCallKind`, one per call; one bubble spans a whole tool loop) |
| `rolemanager` | the classifier or evaluator model call behind the decision (security sentinel, ML phases 1+2 together and phase 3, mode select, goal and plan evaluators) |

A provider call that finishes before its turn has an assistant bubble is
held and adopted by the next bubble, so no call is dropped. Cache hits,
forced modes and phases that did not run carry no duration.

## Credentials

`internal/credentials` implements layered credential resolution for the
built-in providers and any custom providers defined in `settings.json`. The
resolution order is:

1. **Environment** — preserves existing behaviour exactly.
2. **Project file** — `<workdir>/.vulnetix/signet/credentials.json`.
3. **User file** — `~/.vulnetix/signet/credentials.json`.
4. **`.netrc`** — read-only; never written by Signet.
5. **Host keychain** — via `zalando/go-keyring`, with a 5-second timeout so a
   locked D-Bus collection cannot block startup.

The user file may store inline secrets (JSON `{"source":"inline","value":"…"}`)
when the file mode is `0600` and the containing directory is `0700`. The
project file may store references (`{"source":"env","name":"OPENAI_API_KEY"}`)
but **never** inline secrets — a project file lives in whatever repository you
happen to `cd` into. A credential may be stored as the *name* of an
environment variable rather than a value, read at resolve time and never
written to disk; this is what keeps project credential files committable.

When `~/.signet` exists and `~/.vulnetix/signet` does not, `config.Migrate()`
moves the directory on first startup. A cross-filesystem fallback copies
recursively and leaves a `.migrated` marker; nothing is deleted.

The TUI credential manager (`/providers`) shows provenance for every field,
accepts `s` to set a value, `e` to set an env reference, `c` to clear, `b` to
cycle the write backend, and `i` to open the import screen. The default write
backend is the keychain when available, otherwise the user file with an
explicit confirmation.

### Custom providers

`settings.json` may carry a `providers` block mapping a name to a profile with
`base_url`, `api` (one of `openai-chat`, `openai-responses`,
`anthropic-messages`), optional `auth` (`bearer`, `x-api-key`, `cf-aig`),
optional `api_key_env`, and a `models` catalogue. Secrets never live there.
Profiles merge key-by-key across layers, and a project-layer `providers` block
is ignored unless the global settings opt in with
`allow_project_providers: true`.

### Discovery

`internal/agentscan` is read-only credential discovery. It runs only on an
explicit key press (`i` inside `/providers`), never at startup. It opens a
fixed list of paths for Pi, Codex, Claude Code, Goose, OpenCode, Copilot,
Gemini, Qwen, Crush, and Aider, caps every read at 1 MiB, honours
`XDG_CONFIG_HOME`/`XDG_DATA_HOME`, never follows symlinks out of home, and
never executes anything. Discovered secrets render masked and are never logged;
agents installed but holding no importable key are listed with the reason.

## System prompt

`internal/prompt` assembles the system prompt. It carries exactly one context
block at a time (active plan, goal, or profile) and rewrites assistant voice
guidance when `caveman` is on. `run.SealSystem` seals it as a `<system>` block
and, when the turn carries tools, seals the tool briefing beside it as its own
`<tools>` block — see [The sealed tools block](#the-sealed-tools-block). Caveman is off by default (`nil` or `false`).
Toggling it from the chat view with `f2` persists the setting to the
current scope (project by default), invalidates the cached agent session so
the next turn picks up the new system prompt, and emits a `caveman: on/off`
system message for immediate feedback.

**The sealed system prompt is reused across turns.** `Session.sealSystem`
keys the sealed bytes on the provider, the model and every prompt input
(carrier, mode wording, tools briefing, skills, repo map, workspace roots,
working directory). An unchanged key reuses the previous turn's sealed
system and tools blocks and their nonces — safe because the nonce pool never
rotates within a session — so the first bytes of every request stay
identical and a provider can cache them. A mode switch, a skills change, an
added root, a `Cd` or a model switch changes the key and re-seals. The
volatile repository facts ride the user turn instead (see
[Repository map](#repository-map)).

**Work discipline** (agent and goal mode only; never plan mode or a read-only
session such as an explore subagent) is Claude Code-style guidance: read the
code you will change and its callers first; make the smallest change that
fully solves the task in the surrounding style; batch independent reads in
one response; run the detected test or lint command after changing code;
don't narrate plans. It keeps the repo-map, read-once and withheld-result
lines. It no longer measures "time to first file mutation" or caps
reasoning, which pushed edits ahead of reading.

## TUI

`internal/tui` is a Bubble Tea app laid out Codex-style: a scrolling transcript
viewport, Pix banner, streaming assistant/tool output, slash-command editor
with autocomplete, the agent picker, `/model` provider/model/effort picker (a
windowed list that scrolls with the cursor and a `/` search filter),
`/settings` browser, `/permissions` editor, and a status footer. The Ask composer doubles
as the working indicator: it shows a Role Manager pill while classification
runs and a generic `working` label for plain I/O (see below).

### Status bar

The footer is a rule plus three content lines:
- Line 1: the mode chip (coloured — teal for agent, soft teal for plan, amber
  for goal), cwd (home collapsed to `~`) and git branch (`⎇ main`), joined by
  `·`. The mode chip carries the engaged agent when there is one —
  `agent · reviewer` — so what is carrying the turn is visible without opening
  anything. cwd and branch are omitted entirely when unset, so a non-git
  directory shows the chip alone — until the agent moves, at which point the
  directory renders even outside a repository, because a footer that
  disagreed with the paths in the transcript would be worse than no footer.
  The cwd shown is the live [working directory](#working-directory-cd), not
  the directory the session started in; it returns to the root whenever the
  agent session is rebuilt. The footer's memoised height is invalidated on a
  move, since line 1 appears or disappears with the directory.
- Line 2, left: provider · model (with effort) · the permission chips ·
  `caveman: on|off`. When smart model routing is engaged (`routing.kind:
  "routed"` resolving to a non-empty pool), the provider/model segment is
  replaced by a cream "Smart model router" label plus a muted count of the
  distinct provider/model pairs it can choose between (main model included),
  because no single model serves the turn. `tab` cycles the routing `kind`
  (`defined` ↔ `routed`) from anywhere on the `/model` screen, and from the
  chat view when no slash popup, picker or history cycle claims the key; it
  saves to the `/model` routing scope (project by default), names the new mode
  in the transcript, and updates this segment in the same frame. `routed` with
  an empty candidate pool keeps the single provider/model segment.
- Line 2, right: session (name or short id), the context-usage text segment,
  and the context progress bar.
- Only the **session** segment is truncated, rune-safely with an ellipsis, to
  a budget computed from the width left over after the left group, the context
  segment and the bar. The budget floors at 12 cells, so a very narrow
  terminal overflows rather than erasing the session id.
- Nothing else is truncated or wrapped: when the terminal is narrower than the
  content, the padding between left and right clamps to one cell and the line
  overflows instead.
- Line 3 is the hover-hint line. It is always emitted (empty when the pointer
  is over nothing actionable) so the footer's height never changes with the
  mouse; a height change would shift the viewport under a stationary pointer
  and could make the hint oscillate. When the pointer is over an actionable
  region it shows a `HelpBar` of keycap/action pairs (see
  [Mouse hover hints](#mouse-hover-hints)).

**Permission chips.** Guardrails and ask render as two chips, `guardrails:
on|off` and `ask: on|off`, teal when on and red when off. When *both* are off
they collapse into a single amber `YOLO` chip — one unmissable marker beats two
red ones. Any other combination renders the pair.

The guardrails chip is a statement about behaviour, not a decoration — see
[The guardrails switch](#the-guardrails-switch) for what it actually turns
off and how it reaches every surface.

**Caveman slot.** `caveman: on|off` always renders, on and off alike: the
rewrite silently changes how every reply is written, and the footer is the only
standing statement of that. It does not collapse the way the permission chips
do, and it is not a chip — the label is muted and only the value goes teal when
on, so the safety chips keep the visual lead. `f2` and the `/settings` toggle
both flow through `App.refreshFooter`, so the slot updates in the same frame as
the `caveman: on/off` system message.

A mode decision writes a transcript line only when it changes something: a
classifier result that lands on the mode already selected repeats what the
chip is showing, so it stays silent. A decision that also launches explore
agents still says so, since that describes the turn rather than the chip.

Effort, when set, renders subtly next to the model id in muted style
(`gpt-5 · high`). An explicit `none` (reasoning off) is a real value and
renders; an empty effort means the provider default and renders nothing, and
without a configured model the effort is not shown at all.

Context usage has three degraded renderings:
- `~` prefix — pure `chars/4` estimate (no provider usage anchor yet).
- `(?)` instead of a percentage — the window is unknown, or the anchor predates
  a `/compact` (stale), so the context bar is empty and muted.
- a coloured bar and percentage — only when anchored and fresh; `<20%`
  remaining reads red, `<50%` remaining reads amber, otherwise teal.

Progress-bar business rules: the bar is 10 cells, filled by
`tokens / context window` clamped to [0, 1] at eighth-cell resolution
(`▏`–`▉`), so partial cells step in 2% increments and a fraction past 7/8
carries into the next cell. The fill shares the percentage's colour rule,
so bar and number can never disagree. When the window is unknown or the
usage is stale the bar is empty and muted — the harness draws no fill it
cannot stand behind; the `(?)` in the text segment carries that state. An
unanchored (estimated) token count still fills the bar normally; the `~`
in the text marks it as an estimate.

### Submit flow and working indicator

Pressing Enter in the Ask prompt echoes the text into the transcript as a
`user prompt` panel **instantly** — before mode classification and before any
provider I/O — and the composer's top edge switches to a working state:

- **Role Manager phases** (teal). While the Role Manager is classifying
  content, a filled `role manager` pill carries a sub-phase caption:
  *pre-prompt processing* (mode classification, prompt admission, and mode
  selection before the first model turn), *classifying steering*, or
  *classifying tool result*.
- **Generic working** (amber). Plain `working` is used only for disk or
  network I/O without a Role Manager signal: model streaming, tool execution,
  and retry back-off.
- **Exploring** (teal-soft). While explore subagents fan out, the composer
  reads `exploring N/M · <reference>` with a live spinner. The explore pill is
  *not* a Role Manager signal — it stays up until a real parent stream event
  lands, because the fan-out emits only subagent events, never parent text.

The agent emits `EventRoleManagerKind` (with the sub-phase) at every Role
Manager classification point; model and tool events switch the composer to the
generic phase, and done/error return it to idle.

**Mode selection runs inside the agent.** A prompt whose mode the user has
not fixed starts its turn at once with no mode decision; the agent runs mode
selection concurrently with prompt admission (as the CLI always has) and
reports the result with `EventModeDecidedKind`. The TUI applies it without
rebuilding the running session (`applyLiveModeDecision`): the chip, the
announcement line and the plan-mode flag update, while the session already
latched the mode for the turn. This removed one full serial model call from
every TUI prompt. Before sending, the previous turn's classifier decisions are
released — a classifier-engaged agent profile (unless the user engaged it) and
a classifier-inferred plan-mode baseline — so they cannot narrow this turn.

A prompt that names an agent profile (`@agent:NAME`) still classifies
*before* the turn, because the profile decides the session's tool allowlist
and model. In that pre-send window — the prompt is echoed but classification
is still running — Enter is held with a "still preparing" hint and `esc`
cancels the turn without sending. While a
turn is running, Enter instead queues the text as a `user steering` prompt:
steered turns pass through the same Role Manager admission as the original
prompt, and a full queue drops the newest message.

### Subagent roster and the footer pulse

Every fan-out subagent the harness launches, explore tasks and background
agents alike, has a chip on the footer's roster line: `[main] [chip…]`. Each
chip carries a state glyph (a spinner while running, `○` queued, `‖` paused,
`◌` idle between scheduled runs, `✓` done, `✗` failed or stopped) and, when
there is room, a muted note naming the running tool and the iteration. When
the line is too narrow the notes go first, then chips collapse into `→ N
more`. A one-second tick drives the glyph and refreshes background loop state
only while an agent is working.

While a thread is followed, the roster line becomes the pulse for that agent:
its chip, state, running tool or `thinking`, `iter N/M`, tool and error
counts, elapsed time, as much of its latest output as fits, and `esc main` on
the right. `esc` in chat leaves the followed thread before it would cancel the
main turn.

The roster itself is driven from the runs panel: `f8` opens it on the
subagents tab, `⏎` follows the selected subagent (main clears the filter), and
`x` cancels a running chip or dismisses a finished one. `tab` cycles through the
activity, subagents, and processes tabs. Chips persist across
turns and are removed only by an explicit dismiss; running chips are never
removed automatically.

The fan-out is capped by one settings-backed FIFO pool
(`resilience.max_agents`, default 15) that the Role Manager owns and reaches
through `rolemanager.Pipeline`. Explore subagents and background-agent turns
alike acquire a lease; queued work shows a muted chip, running a teal chip,
done a teal-soft `✓`, and cancelled/failed an amber chip. Subagent tool
activity streams into the transcript as render-only rows tagged with a dim
`explore N` gutter; those rows never enter `buildTurns`, so raw subagent
output can never be promoted into the parent conversation.

### Runs panel

Signet runs the Vulnetix CLI, `!shell` commands and background agents on the
user's behalf; the bottom **runs panel** is the honest register of those
processes plus the roster of subagents pinned to the conversation. `f8` opens
and focuses the panel on the **subagents** tab, and `f9` opens it on the
**activity** tab. `tab` cycles through **activity → subagents → processes**, so
the processes tab is always one `tab` away from either entry point. The panel
is bounded: it never consumes more than one third of the terminal height and
refuses to open when fewer than six rows are available, so the chat composer
always remains usable. Each row is truncated to fit the width of the panel;
a full-screen **output view** (`v` or `enter`) is used to read long activity or
process output.

Each tab keeps its own selection. Keys while focused: `↑`/`↓` select,
`tab` switches between the three tabs, `esc` returns focus to the composer,
and `f9` closes the panel. Activity tab: `x` kills the selected activity
(running or queued), `t` starts `signet:triage-vulns` on its project, and
`enter` sends its output to the model. Subagents tab: `enter` filters the
conversation to the selected subagent (or clears the filter on `main`), and
`x` cancels a running chip or dismisses a finished one. Processes tab:
`enter`/`v` open the live log for the selected running process, `x` stops it,
and `r` restarts it.

The register is `internal/activity`, a process-agnostic FIFO registry with no
TUI imports. Subprocess output is arbitrary content, so it classifies
unconditionally before a finished activity's output round-trips to the model —
skipping the classifier only when the guardrails gate is ignored (sanitising
still runs) — and a non-SAFE sentinel is shown locally and not sent. The output
seals as a `Kind: "shell"` attachment, which forces the `signet:debug` profile
exactly like a `!shell` result. When a turn is already in flight the finished
activity queues and flushes as one batched turn once the transcript is idle.

### Vulnetix AI Firewall

When the user toggles it on (`F10`, `/vulnetix firewall`, or
`SIGNET_FIREWALL=1`), Signet routes eligible provider traffic through the
Vulnetix AI Firewall gateway. The toggle is fail-closed: `run.Prepare` asks
`credentials.Resolver` for the firewall source via `internal/aifirewall`, which
maps the provider to a gateway slug and constructs
`<gateway>/<slug>/<org>/v1`. The resolver also loads the Vulnetix CLI
credential through `internal/vulnetixcreds`. If credentials are missing or the
provider has no gateway slug, the on-toggle is remembered but the run falls
back to the native provider.

The project-level `vulnetix.firewall_enabled` setting overrides the global
profile value; the CLI flag/environment variable overrides the project value.
`SIGNET_BASE_URL` overrides the gateway URL for any single run, which lets
tests and local gateways observe the routing without contacting the live
gateway.

### Todo panel

When a session is tracking a todo list, a flat `todo` panel sits between the
transcript and the composer showing a three-item window: the last completed
item, the current one (highlighted), the next one, and `… N more` for the
remainder. The counts never include the shown items, so `+N more` is always
literally true, and a finished list still shows the item it finished on rather
than rendering blank.

The agent owns the list and emits it (`EventTodosKind`, and at pass
boundaries); the TUI renders and persists it. `setTodos` is the single write
path, so a list change is never rendered without being durable. Toggle with
`ui.show_todos` (default on); an empty list renders nothing either way.

Mode is cycled with `shift+tab`. An explicitly chosen mode is carried into the
agent session as `ForceMode` and is not re-classified — suppressing the TUI's
own classification is not enough, because the session classifies again
internally.

Provider-streamed reasoning renders in a dim `reasoning` panel (toggle with
`ctrl+r`, `ui.show_reasoning`); tool rows toggle with `ctrl+t`, which cycles
four states — auto (the resolved settings), all, edits only, none — and names
the resolved pair in a system line. `ui.show_tool_calls` gates every tool row
except `Write`/`Edit`; `ui.show_edits` gates the `Write`/`Edit` rows only, so
hiding tool chatter keeps the file diffs and vice versa. `ui.show_internal_work`
gates the role-manager activity feed (see
[role-manager.md](role-manager.md#tui-activity-signal)): `hidden`, `decisions`,
`security`, or `all`, additive in that order. Rows that control whether
something is *displayed* say `shown`/`hidden`; rows that control whether a
behaviour *runs* keep `on`/`off`. The transcript
auto-follows the tail; scrolling up
(mouse wheel or `pgup`/`shift+up`) detaches and returns to the bottom
re-attach. Mouse capture is on by default (`ui.mouse`); with capture on the
terminal's own selection is unavailable, so the TUI implements its own (see
below). With `ui.mouse` off no mouse events arrive and the feature is inert.

### Rendered-line provenance

`Panel.Render` returns the rendered string *and* a `LineMap`: one `SourceLine`
per emitted row recording the clean text, the screen column it starts at, its
cell width, and whether the row is pure chrome. `Panel.View` is the string-only
half.

This exists so hit-testing and copying can recover clean text without
pattern-matching rendered output — the `│` panel bar, the `·` signet gutter
and the list/blockquote markers are all glyphs that also appear as content,
and only the renderer knows which columns are decoration. The invariant each
entry guarantees is `ansi.Cut(ansi.Strip(line), Col, Col+Width) == Text`, and
the map is always the same length as the frame's line count.

A line may also carry a truncation marker (`… N more lines`) plus the `Hidden`
text it stands for, so a selection overlapping the marker copies the hidden
remainder instead of the hint. `Highlight` reverse-videos a cell range without
changing any line's visible characters or width, and refuses to touch a frame
whose map length does not match — a desynced map must never corrupt the frame.

Every line also carries hover provenance, stamped after every render (cache
hits included) by `tagProvenance`:
- `Owner` is the index of the message that rendered the line, or `-1` for the
  blank separator between framed panels.
- `File` marks a line whose panel is a `Read` result carrying a path — the
  thread has output a file, and hovering it offers save and copy.
- `Collapsed` marks a line whose panel is currently truncated, i.e. it carries
  a `… N more lines` marker. Hovering it offers `ctrl+o`.

These three are derived, not stored: `tagProvenance` reads the message and the
marker shape, so a late-arriving path or an expand can never leave a stale
flag behind.

Slicing is by terminal cell, never by rune index, so CJK and emoji stay on
cluster boundaries.

### Styled rows

Every transcript row that carries colour is built from `Seg` and `Row`
(`styledline.go`) rather than assembled by hand. A `Row` is clipped and
measured while its text is still plain, and only then turned into escape
sequences, with its `SourceLine` derived from that plain twin — so both
`LineMap` invariants hold by construction instead of by review. `renderRows`
is the only path from rows to the transcript, and it guarantees one map entry
per emitted line.

`sgr.go` is the only place in the TUI that writes SGR by hand. lipgloss closes
every styled span with a full reset, which clears the background as well as the
foreground; inside a row with a background that would cancel the wash at the
first coloured token and render the rest of the line bare. Rows therefore close
foregrounds with `\x1b[39m` and the background once with `\x1b[49m`, and use
reverse video (`\x1b[7m`) for intra-line emphasis because it composes with a
background the segment cannot see. **Never emit `\x1b[0m` inside a `Row`, and
never hand styled text to lipgloss for wrapping** — its wrap path re-emits a
full reset at every break.

`NewSeg` strips ESC, C0, C1 and DEL from its text. This is load-bearing, not
hygiene: tool output and file contents are attacker-controlled bytes on their
way to a terminal, and an OSC 52 sequence that survived would write the user's
clipboard. Tabs are expanded before anything measures the text, since the
terminal resolves its own tab stops from the screen edge and a gutter has
already shifted the code.

### Tool row previews

Collapsed tool rows show a different amount per tool, because the useful part
is in a different place:

| Tool | Collapsed | Anchored |
|---|---|---|
| `Bash` | 3 lines | tail — a command's verdict is at the end |
| `Read` | 3 lines, numbered | head — a file's identity is at the start |
| a diff | 6 rows | first change — leading context is wasted rows |
| everything else | 1 line | head — already a summary |

A tail-anchored preview puts its hint *above* the content, since the hint
summarises what came before it. `ctrl+o` expands everything.

The invocation line next to a tool name is chosen per tool by
`formatToolInvocation`. File tools (`Read`, `Write`, `Edit`) show `path`/`file_path`;
`Grep`/`Glob` show `pattern`; `WebSearch` shows `query`; `WebFetch` shows `url`;
and tools whose context is a script or expression (`JQ`, `YQ`, `Sed`, `Awk`,
`Cut`) show both the expression and the target file when both are present. A
value is clipped to 120 runes, with extra headroom when two arguments are shown.
`Write` and `Edit` deliberately list the path alone, so a row shows what was
written to and never the file body, the `old_string`, or the `new_string`.
`Bash` is special: its command is wrapped as `Exec(<command>)` so a bare command
string is unmistakably a shell invocation. Anything unlisted tries `command`,
`path`, `file_path`, `pattern`, `filter`, `query`, `url` in that order, and raw
args that do not parse as JSON are clipped to 60 runes.

When an assistant turn has no body text and only requested tool calls, the TUI
renders a one-line summary: each call is shown as `ToolName invocation`
(e.g. `Read foo.txt` or `Bash Exec(go test ./...)`), the list is deduped in
order by the full label, and the line is clipped to the panel's inner width.

The TUI derives this display from the parsed tool arguments, so the
`EventToolStartKind` emitted by the agent must carry `Tool.Args` populated by
`parseToolArgs`. Raw tool arguments stay on the wire as `RawArgs`; without the
parsed map the TUI cannot show the command or path next to a tool row.

### Expanded panel context

Each assistant/reasoning panel normally carries the generic `model` title.
When the user presses `ctrl+o` to expand all panels, the title switches to
`provider/model` (or `provider/model · reasoning`) so it is clear which model
produced the turn. The provider and model are recorded per-message, persisted
in the session JSONL under `meta.provider`/`meta.model`, and restored on resume
so old transcripts keep showing the model that generated them.

The `signet` panel's role-manager activity rows also expose extra context when
expanded: each line is prefixed with `[activity]` and `[provider/model]`
(e.g. `[security_phase] [cloudflare/deepseek-v4] Checked whether the content
floods the prompt...`), surfacing the internal activity key and the model
behind it. The activity key and provider/model are persisted under
`meta.activity`, `meta.provider`, and `meta.model` for role-manager entries.

The Read tool follows the trained contract: `offset` is a 1-based line
number (so a `Grep` line number works as-is), `limit` is a line count
(default 2000), and the output is `cat -n` formatted — each line prefixed with
its right-aligned number and a tab. Lines over 2000 bytes are clipped, and the
whole result stays under the byte cap (64 KiB), cut at a line boundary. A
partial read ends with `[Read: lines A–B of N; truncated — continue with
offset=B+1]` or `[Read: lines A–B of N; end of file]`; a whole-file read has
no trailer, and an offset past the end is answered rather than errored. Edit
names the prefix in its not-found error when an `old_string` still carries it.

The tool also emits `Result.Meta` with `start_line` and `numbered`, delivered
via `EventToolMetaKind`. The TUI strips the tool's gutter and trailer
(`splitReadGutter`, which sniffs the shape so a resumed transcript renders
like a live one), redraws the numbers in its own gutter, and shows the
trailer as a muted row. Copying or saving a file panel hands out the file
text without the gutter (`Message.FileText`). `@file` attachments use
`tools.Read{Verbatim: true}`: exact bytes, no gutter or trailer, because the
body is diffed against the index and handed over as the file itself.

Syntax highlighting (chroma, mapped onto the palette in `theme.go`, lexer
chosen by filename only) applies to expanded rows alone — collapsed, the diff
and status colours are the whole signal.

### Transcript panels and markdown

Every framed panel is titled by its speaker, never by the harness:

- **`model`** — assistant turns. The body is rendered as markdown (see below),
  teal-accented.
- **`user prompt`** / **`user steering`** — the user's turns; steering is
  amber.
- **`model · reasoning`** — streamed chain-of-thought, dim and plain (shown
  only when `ctrl+r` reasoning is on).
- **`signet`** — Signet's own notices. Adjacent system entries coalesce into
  one panel, one body line per notice with a muted `·` gutter, the frame's
  edges in the line colour and the title in the brand accent.
- **`✓ done`** — the completion panel that closes every agent-mode turn (see
  below). Teal-framed, one line, composed by the harness.

#### How a turn ends

Every successful turn ends on something that says it ended:

| Turn | Ends on |
| ---- | ------- |
| Agent mode | The `✓ done` completion panel: *agent turn complete · N tool calls · N edits · elapsed*. |
| Goal mode | The final report turn, streamed into its own `model` panel after *goal complete — writing the final report* (see [role-manager.md](role-manager.md#final-report)). |
| Approved plan | The same final report (an approved plan runs the goal loop), then *approved plan complete*. |
| Plan mode | The plan review pane. |

The completion panel is deterministic: `agentTurnCompleted` decides from harness
state only — no goal sentinel, no plan sentinel, a resolved mode that is neither
`plan` nor `goal`, and a session not in plan mode — and never from reply text.
Its figures come from the transcript rows of the turn: main-thread tool rows
after the last non-steering user prompt (subagent rows are not counted), edits
being the `Write`/`Edit` family, and the elapsed time from the turn's start.
It is render-only: `buildTurns` skips the `completion` role, so it never
reaches a provider. It is persisted as a `completion` entry and restored on
resume; a blank one is dropped. A failed turn gets no panel — it ends on its
error.

Assistant bodies go through `RenderMarkdown` (`markdown.go`, `markdown_inline.go`,
`markdown_table.go`), a hand-rolled, line-oriented, single-pass renderer with
no new dependency. It covers ATX headings, wrapped paragraphs with hard breaks,
bullet/ordered/task lists (nested, hanging-indented, ordered items renumbered),
blockquotes, fenced and indented code blocks (chroma-highlighted by language
name via `HighlightedLang`), thematic breaks, and GFM pipe tables (alignment
from the `:---:` row, box-drawn, over-wide columns shrink and wrap, below the
minimum they fall back to plain rows). `` ```mermaid `` fences render as a
labelled, unhighlighted block behind a `mermaidRenderer` hook so an external
renderer can be dropped in without touching the parser.

Rendering markdown is done against the existing `Seg`/`Row`/`renderRows`
primitives rather than a string-emitting library, because rendered lines must
keep the `LineMap` invariant above. Wrapping happens on the plain concatenation
and segments are re-sliced with `ansi.Cut`, so no segment is ever measured with
escapes in it — and `NewSeg`'s escape sanitising still applies to `Read`
contents, which are untrusted file bytes. Inline spans map to the palette:
bold is cream, italic teal-soft, both add reverse video, `code` is amber,
strikethrough carries its own SGR span, and links render their text teal with
the URL muted and dropped when it cannot share the text's line. Unterminated
fences run to end of input so a streaming reply never flickers.

Truncation moves to rendered rows: `truncateMarkdown` keeps the first
`assistantPreviewLines` rows and sets the hint's hidden remainder to the raw
markdown source from the first dropped row's `MD.Src` line, so a fence or table
spanning many source lines but few screen rows truncates cleanly, and a
selection over the hint still copies the original markdown. The signet panel
truncates at `signetPreviewLines` (6) like any other panel — a deliberate
change from the old never-truncated system rows — and recovers the hidden
notices from the per-line owner slice.

`Read` results whose path has a markdown extension (`.md`, `.markdown`, `.mdx`)
render through the same renderer once expanded, instead of the numbered-source
path. Collapsed `Read` rows keep the numbered three-line head, matching the
highlight-only-when-expanded rule.

### Diffs

`Write` and `Edit` report their targets through `tools.Targeter`, so
`internal/filediff` snapshots exactly those paths via `BeforePaths` around the
call. `Bash`'s changes are still observed rather than reported: inside a git
repository discovery is by `git status`, which sees what happened however it
happened — `sed`, a heredoc, a formatter, `make`, a test that rewrites its own
fixtures. Outside one it falls back to inferring targets from the command, and
refuses far more than it accepts: reporting "nothing changed" for a command
that changed everything would be worse than showing nothing.

The recorder hooks `executeCall` around every mutating tool (which always runs
on the sequential path, so no locking is needed) and emits
`EventToolDiffKind`. Like `EventToolProgressKind` it is render-only: neither
enters the conversation nor reaches a model, and tests assert the
provider-facing turns are unchanged by their presence.

Collapsed diff rows are foreground-only. Expanded rows carry a background wash
instead, which marks the row without using the foreground — freeing it for
syntax colour, so a changed line reads as code rather than as a stripe.

Selection highlight (`Highlight` in `linemap.go`) wraps the selected cells in
reverse video. Because `ansi.Cut` strips every escape sequence before the cut
point, the right-hand fragment of a partially-selected washed row would be left
bare after the selection's full reset. Each `SourceLine` therefore carries a
`Reopen` string — the SGR prefix that restores the row's original background
(and the first segment's foreground) — which is inserted between the reset and
the right fragment.

Costs are bounded throughout: a git timeout latches the feature off for the
session rather than being paid per command, plus per-file byte caps and a file
count cap.

### Transcript selection

Left-button press-drag-release over the transcript selects a character range
and copies the clean underlying text on release: no borders, no ANSI, no `⌁`
tool-row prefix, no right-aligned status word, and `… N more lines` markers
expanded to their hidden remainder (`LineMap.Text` substitutes the `Hidden`
text whenever the selection overlaps the marker cells).

Business rules:

- **Content coordinates.** The selection is stored in content lines, not
  screen rows, so it survives scrolling — and the wheel works *mid-drag*
  (press, wheel, keep dragging, release). Edge auto-scroll is not implemented;
  the wheel is the documented gesture.
- **One content compare clears.** A selection is cleared whenever the rendered
  body changes — new message, streaming delta, `ctrl+l`, `ctrl+o`, `ctrl+r`,
  `ctrl+t`, or a width change — because the line map can shift underneath it.
  A height-only `WindowSizeMsg` leaves the body identical, so it clears the
  selection explicitly.
- **Bare click clears.** A press outside the viewport, or a release at the
  anchor cell, clears the selection and copies nothing. Release never tests
  the button (X10 reports `Button==None`; SGR keeps `Left`), and wheel events
  are checked before press (a wheel tick is `Action==Press`) or every tick
  would re-anchor the selection.
- **`esc` clears first.** With a live selection, `esc` dismisses the highlight
  and nothing else; a second `esc` does the usual cancel/pre-send work.
- **Copy feedback.** Release runs the same `clipboard.Copy` path as `ctrl+c`
  and reports `copied N lines to clipboard (method)` through the footer. That
  notice changes the body, so the highlight clears on the next frame — that is
  the intended “copied, done” feel. For large OSC 52 payloads a caveat is
  appended, because xterm's `maxStringParseSize` and tmux without
  `set-clipboard on` drop them silently.
- **Highlight is visible-window only.** `Highlight` rewrites at most the
  viewport's height of lines (off-screen rows are re-rendered every frame
  anyway) and must never change a line's visible characters or cell width —
  a width change would trip the viewport's `MaxWidth` and shift the frame.

### Mouse hover hints

The transcript and the footer's session segment are hover hit-tested, and the
footer's third content line advertises what the pointer can do. The target is
re-derived every frame (`App.recomputeHover`) from the last mouse position and
the rendered frame's provenance, not stored on mouse motion, so it can never
point at a panel the frame no longer shows: expanding with `ctrl+o`, clearing
with `ctrl+l`, or a streaming delta all re-derive the target without another
mouse event. With `ui.mouse` off no mouse events arrive, so the feature is
inert — the same gate as drag-selection.

Three regions are actionable:

- **Any panel with text** — user, assistant, reasoning, signet, and non-Read
  tool rows alike. Hovering shows `ctrl+s save <name> · ctrl+c copy`. A `Read`
  file panel names its real file's basename; everything else gets a generated
  default name `signet-<short-id>-<idx>.<ext>` (`.md` for text panels, `.txt`
  for tool results; the short-id segment is dropped when no session id
  exists). The name is truncated at 40 runes. `ctrl+s` turns the composer into
  a destination-path prompt (`save file`, `⏎ save · esc cancel`) pre-filled
  with that name, and enter writes the panel's *full* content — a collapsed
  panel saves the hidden remainder too, not just what is on screen. A relative
  path resolves against the working directory, an absolute one is used as-is,
  and the path is deliberately not confined because the *user*, not the model,
  chose it. Empty input cancels with `save cancelled: path required`. `ctrl+c`
  over a panel copies its content instead of the prompt; with the pointer off
  the transcript it keeps its prompt-copy meaning. A running tool row with no
  output yet has no text and stays un-hoverable.
- **User/assistant/model panel** — user prompts, assistant turns and streamed
  reasoning that carry text advertise `ctrl+c copy` in their title bar.
  Assistant panels also show the provider-metered token count when available;
  user and reasoning panels show an estimated token count (`~N tok`) because
  they are not metered by the provider. The title bar mirrors the helper text
  pattern used by the ask/composer and collapsed signet frames; hovering the
  panel still offers the same action in the footer.
- **Session segment** — the footer's `session: …` text (name when shown, else
  the short id). Hovering shows `ctrl+x copy session id`, and `ctrl+x` copies
  the full id from the chat view whether or not the pointer is there.
- **Collapsed panel** — any truncated turn, reasoning panel, or tool row.
  Hovering shows `ctrl+o expand all`; the key is the same global toggle that
  collapses again when already expanded. A collapsed text panel shows all
  three offers together (`save`, `copy`, `expand all`). **Signet** panels
  advertise the same binding in their title bar whenever they contain any
  collapsed content — whether the panel itself truncated a run of system
  notices or a nested tool row (for example a long `Bash` result) is hiding
  lines. This mirrors the helper text the ask/composer frame carries in its
  own top edge, so the shortcut is visible without moving the pointer.

The session hit-test is exact: `Footer.SessionSpan` mirrors the same layout
math `Footer.View` uses, so the column range it reports is the rendered
segment, right-aligned on footer line 2. The footer's height is constant
(rule + two info lines + hint line), so showing or hiding a hint never shifts
the viewport.

### Keybindings

**No binding uses `alt`, and none ever will.** Two independent reasons:

1. Under the kitty keyboard protocol Signet pushes (`ui.kitty_keyboard`,
   default on), `internal/tui/keys.Translate` maps a `ctrl`-modified letter
   onto the legacy `tea.KeyCtrlA…KeyCtrlZ` constants. Those constants carry no
   alt bit, so `ctrl+alt+x` and `ctrl+x` are indistinguishable by the time the
   `switch m.String()` in `app.go` sees them — a `ctrl+alt+…` case is dead
   code that can never match.
2. Without the kitty protocol, `alt` is an ESC prefix, which is ambiguous
   against a real `esc` press and is swallowed or remapped by many terminals,
   tmux and macOS Terminal among them.

The session toggles therefore live on the **function-key row** (`f2`–`f6`)
rather than on `ctrl+<letter>`. Every `ctrl+<letter>` that is not already a
global is claimed by the prompt editor (`ctrl+a`, `ctrl+e`, `ctrl+f`, `ctrl+b`,
`ctrl+k`, `ctrl+u`, `ctrl+w`, `ctrl+n`, `ctrl+p`, `ctrl+v`), and taking one
from the editor would cost a text-editing key for every user in exchange for a
toggle most reach through `/settings` or `/yolo` anyway. The two hover keys
are exceptions that follow the same rule in reverse: `ctrl+s` and `ctrl+x` are
not editor keys, so they are free for the hover actions, and binding them
costs nothing a text editor needs.

**An alt *chord* is banned; an alt *flag* is not.** The rule above is about
keys a user presses. Several terminals encode an unrelated keypress in a form
bubbletea decodes with `Alt: true` — ESC+CR for `shift+enter`, urxvt's
`\x1b[Od` and xterm's `\x1b[1;7D` for `ctrl+left`. Those are accepted, matched
on the bubbletea key **type** so the stray flag is ignored, rather than bound
as an `"alt+…"` keycap that `String()` would have to produce. The test guard
enforces the keycap ban, which is why it scans for string literals and not for
the `Alt` field.

`f2`–`f6` are handled in the global `tea.KeyMsg` switch, before view
dispatch, so they work on **every** screen — including inside `/model`,
`/settings` and the clarify questionnaire. `f7` is chat-scoped: it is handled
in `handleChatKey`, so it does nothing on a full-screen view.

| Key | Behaviour |
| --- | --------- |
| `ctrl+c` | Copy the current prompt to the clipboard (native, then OSC 52); over a hovered panel, copies the panel's content instead |
| `ctrl+d` | Quit, unconditionally |
| `shift+tab` | Cycle mode: agent → plan → goal |
| `esc` | Close any full-screen view (nested views pop to their parent); cancels a held submit or an in-flight pre-send |
| `space` / `n` / `s` / `enter` | Use in the **Clarify** questionnaire view: select, add a note, skip the question, submit |
| `ctrl+l` | Clear the transcript *view* — the session is kept |
| `ctrl+o` | Toggle full output for all truncated turns and tool results |
| `ctrl+s` | Over a hovered panel with text, save its content to a path typed into the composer; with a loaded library prompt, open the overwrite/delete action bar; otherwise save the prompt to the library |
| `ctrl+x` | Copy the session id to the clipboard (hinted when hovering the footer's session segment) |
| `ctrl+r` / `ctrl+t` | `ctrl+r` toggles the reasoning panel for the session (`shown`/`hidden`); `ctrl+t` cycles tool-row display auto → all → edits only → none |
| `f2` | Toggle the caveman voice rewrite, persisting to the per-project preference file; the footer `caveman:` slot updates in the same frame |
| `f3` | Toggle guardrails (the posture gates), from any screen |
| `f4` | Toggle the permission-ask gate, from any screen |
| `f5` | Cycle mode and re-sync plan mode, from any screen |
| `f6` | Cycle reasoning effort: default → low → medium → high → default, from any screen |
| `f7` | Save the current prompt to the project prompt library, from the chat view — a save-as alias of `ctrl+s` with no loaded entry |
| `f1` | Open the screen switcher from chat or any screen. One letter opens a screen: `a` agents, `m` model, `p` providers, `s` settings, `k` permissions, `r` prompts, `x` processes, `l` lsp, `v` vulnetix, `h` sessions. A screen already open further down the stack is returned to, so `esc` walks back through distinct screens. It does nothing on a permission ask, a clarifying question, plan review or while an inline field edit holds text, and a chat draft is kept while it is open |
| `f8` | Open and focus the bottom runs panel on the subagents tab (chat); press `tab` twice to reach the processes tab |
| `f9` | Open and focus the bottom runs panel on the activity tab (chat); press `tab` twice to cycle to the processes tab |
| `f10` | Toggle the Vulnetix AI Firewall from any screen |
| `ctrl+home` / `ctrl+end` | Jump the transcript to the top / bottom |
| `ctrl+j` | Insert a newline in the prompt editor |
| `ctrl+left` / `ctrl+right` | Move the cursor one word left / right, crossing into the neighbouring line at a line boundary |
| `home` / `end` | Jump to the start / end of the logical line (`fn+left` / `fn+right` on a laptop keyboard) |
| `shift+enter` | Insert a newline. bubbletea has no shift+enter key type, so it arrives one of two ways and `Editor.Update` accepts both: under the kitty protocol the CSI-u translator folds every modified enter onto `ctrl+j`, and without it the terminal sends ESC+CR, which decodes as `enter` carrying the alt flag. That flag is a terminal encoding, not a chord anyone presses, so it is matched by key type rather than bound as an alt keycap |
| `up` / `down` | Browse prompt, `!cmd`, `!!cmd` and `/command` history and the prompt library. Library entries come first and their names show as a chip strip above the composer: `tab` cycles the named prompts, `right` accepts the loaded one into the composer, `enter` sends it. Typing — like any edit key — leaves the browse cycle and edits the loaded prompt |
| `f7` | Save the current prompt to the project prompt library — a save-as alias of `ctrl+s` with no loaded entry |
| `tab` | Move the highlight through the slash-command hints, or — when the agent picker is open in agent mode — through the agent candidates. It never writes into the prompt. While browsing the prompt library it loads the next named prompt instead |
| `right` / `enter` | Accept the highlighted hint (or the first, for `right` with nothing highlighted). A `/prompt:`, `/agent:` or `/process:` library hint acts immediately instead of filling the prompt; when the agent picker is open, engage the highlighted agent — and also send the prompt when it was a submit that opened the picker; while browsing the prompt library, accept the loaded prompt into the composer (`right`) or send it (`enter`). Without a highlight, `right` is the cursor key and `enter` sends, or opens the agent picker in agent mode if no agent is engaged |
| `ctrl+g` | Start the highlighted `↻` background-agent definition as a background agent |
| `esc` (with a highlight) | Drop the highlight, keeping the popup on screen; close the agent picker |
| `enter` (while working) | Steer the running turn with a new user message |
| mouse wheel / `pgup` / `pgdown` / `shift+up` / `shift+down` | Scroll the transcript (detaches auto-follow) |
| left drag over the transcript | Select a character range (highlighted live); release copies the clean text |
| `esc` (with a live selection) | Clear the selection first; a second `esc` cancels/pre-send as usual |

Highlighting and accepting are deliberately separate in both the `/` popup and
the agent picker. `tab` moves a highlight only; it must not write the
candidate into the prompt, because the completion list is recomputed from the
prompt text on every message — including the cursor blink — so writing `/mode`
into the editor narrowed the list to that one command and pinned the cycle to
a single entry. `refreshAutocomplete` drops the highlight only when the
candidate list actually changes, so a blink can never move it either.

#### Slash popup: fuzzy matching and library entries

The text after `/` is a **fuzzy** filter, not a prefix. `internal/fuzzy`
matches a candidate when every query rune appears in it in order,
case-insensitive. Candidates are ranked by score:

- A match at the start of the candidate scores highest.
- A match right after a word boundary (`-`, `_`, `:`, `/`, `.`, space) and a
  run of consecutive matches score next.
- Each rune skipped between matches costs a point. Leading skipped runes cost
  a point each, capped at eight.
- Equal scores keep the input order (stable sort). An empty query scores every
  candidate 0, so a bare `/` shows the list in its natural order.
- No match gives no chips. `Complete` returns nil, as it did before.

Examples: `/pmt` finds `/prompts`, `/dir` finds `/add-dir`, and `/p` still
lists every `/p…` command first, in name order, followed by weaker matches
such as `/help`. The same ranking filters a command's arguments after the
space, so `/agent stp` offers `/agent stop`.

The popup also offers the saved libraries next to the commands, each under
its own prefix. `App.slashCompletions` ranks everything as one list, in this
order: the commands, then the enabled prompts from the merged prompt library
(`/prompt:<name>`), then the agent picker's profiles (`/agent:<name>`), then
**every** entry of the merged process library (`/process:<name>`).

- Each group is sorted by name.
- Disabled processes are offered as well: disabled only turns off auto-start,
  and starting one by hand is the point of the chip.
- A library entry matches on both its prefixed form and its bare name, so
  `/deploy` finds `/prompt:deploy` and `/a:rev` finds `/agent:reviewer`.
- A line with a space falls through to `Registry.Complete`, which does
  argument completion only.
- The prompt and process libraries are read once, when the composer line
  starts with `/` (`slashLibCache`), not on every keystroke. A library edited
  in the middle of a slash line shows up on the next slash line.

A library chip names an item rather than a command line to edit. So `enter`
or `right` on a highlighted library chip **acts at once**, the way the agent
picker does. A command chip still fills the composer and waits for a second
`enter`. Typing a library line out in full and pressing `enter` goes through
`handleCommand`, which checks the three prefixes before the registry, so it
does the same thing. Only the first prefix is cut, because agent names can
contain a colon (`/agent:signet:debug`).

| Line | Effect |
| ---- | ------ |
| `/prompt:<name>` | Loads the prompt body into the composer and sets `loadedPrompt`, exactly as `up`-browsing does, so `ctrl+s` overwrites that file. Nothing is sent |
| `/agent:<name>` | Engages the profile and switches to agent mode from any mode. It sets `agentExplicit`, `modeExplicit` and `modeSticky` and persists the mode. The name must be one the agent picker offers |
| `/process:<name>` | If the process is `running`, `recovering` or `restarted`, it is not started again. `bgproc.Manager.Start` would let this Signet start a second copy, so the check happens here. Otherwise it starts through `startSupervised`, which applies the same plan-mode `ToolAllowed` gate as `!!`. Either way it prints a status line: `process <name> (<id>) <state> · pid · up …` for a live process, or `· exit N · ended … ago` for one that has stopped, followed by the command and the log path |

An unknown name prints a one-line refusal and changes nothing.

The chip row is windowed so the highlighted chip is always drawn.
`chipWindow` grows the row rightward from the highlighted chip, then leftward,
and marks each clipped end with a muted `…`.

`ctrl+l` clears the transcript view; `/clear` (or `/new`) starts a *new* session.
They are deliberately different: one is cosmetic, the other changes what is
persisted.

The user-facing inventory of every binding, including the per-view keys the
`HelpBar` footers advertise, lives in `keySections()` in
`internal/tui/help.go`; `/help` renders it under the command list.
`TestHelpTextCoversEveryHandledKey` scans the `case "<key>":` literals across
the package and fails when a handled key is missing from that table, so a new
binding needs a line there as well as in this document.

### Prompt syntax

- `@path` or `@"path with spaces"` attaches the contents of a file after
  classification. `@agent:name` in the submitted prompt is interpreted by
  the role manager as a named-agent directive; it no longer opens the TUI
  agent picker.
- `!cmd` executes a local `Bash` command (full shell by default; read-only
  in plan mode, or whenever `read_only` is set) and sends the output to
  the model under the `signet:debug` profile. The command gets its own
  **shell panel** in the transcript (`components.ShellRole`). The panel is
  not a tool row, is not part of the signet panel, and is not a runs-panel
  activity, and ctrl+t does not hide it. It shows the command's raw output,
  with a live tail while the command runs. When collapsed it shows the last
  six lines; ctrl+o expands it, and ctrl+c / ctrl+s copy or save it.
  Terminal control sequences are stripped on screen, while the copy keeps
  the command's bytes. The panel is render-only: `buildTurns` and
  compaction never read it. The model receives one sanitised, classified
  `shell` attachment instead. A verdict that withholds that attachment still
  leaves the raw output in the panel, marked `not sent`. That covers a
  classifier error (`not sent: classifier failed`) and an unsafe verdict
  (`not sent: <label>`). A command that fails before producing output shows
  `failed: <error>` with a `✗` status. The panel is written to the session
  file as a `shell` entry (`meta.command`, `meta.shell_id`, `meta.status`).
  Output over 32 KiB is truncated there, with `meta.truncated` and
  `meta.orig_len` recorded. A resumed session restores the panel. A shell
  row that is still running is never persisted: the next user turn drops it
  rather than writing an orphan. The panel saves with `ctrl+s` as a `.txt`
  file, like a tool row.

### Agent picker

In agent mode the composer can show a strip of the agents that can carry the
turn, drawn from both trees:

| Row | Source | Marker |
| --- | ------ | ------ |
| built-in | embedded `signet:` profile | `◈`, muted |
| user profile | `internal/profiles` (flat `Name`/`Content`) | none, keycap bright |
| background definition | `internal/agentprofile` | `↻`, amber |

`signet:debug` is the default agent and is selected first whenever the picker
opens. A flat profile owns a shared name — it is what `CarrierOptions`
resolves first — so a background definition of the same name is not offered
twice.

The picker is opened in two ways:

- Type `/agent` with no argument and press `enter`.
- Press `enter` in agent mode while no agent is engaged.

Once open, the strip behaves like the slash popup's sibling: `tab` highlights
the next candidate (ending on a `(none)` entry that clears the selection),
`enter` or `right` engages the highlighted one, `esc` closes the picker.
Typing no longer filters the strip; `@` in the composer belongs to the file
chooser, so pressing `enter` with no agent engaged opens the picker instead
of sending a turn without a carrier. The slash popup wins the strip and
`tab` whenever both could show, and the file chooser wins whenever an
`@` prefix is active.

A submit that opens the picker is **deferred, not dropped**. `App`
records `agentPickerSubmit` when `enter` opened the picker over a non-empty
composer, and `acceptAgent` finishes that submit once a carrier exists. Two
keystrokes therefore send the turn — the first chooses the carrier the picker
exists to demand, the second engages it and sends — rather than three. Without
the flag both keystrokes return `nil`, the prompt sits in the composer, and
the user waits on a turn that was never started: no classification runs, no
session name is derived, and nothing appears in the transcript.

`ctrl+g` is the second verb, and only background definitions answer it: it
starts the highlighted definition as a background agent (`bgagent.Manager`),
exactly as `/agent start <name>` does. Starting a loop does not answer the
prompt in the composer, so it is deliberately not `enter`.

The engaged agent is session state: it applies to every following turn
(`App.namedAgent` → `agent.TurnInput.ForceAgent`), shows in the footer chip
next to the mode, and is cleared by `/clear`. It is **agent mode only**. Plan
and goal mode carry Signet's own plan or goal — `resolveCarrier` admits
exactly one carrier, and `ForceAgent` also forces the mode, so sending an
engaged agent from plan mode would silently drop the mode the user picked.
Outside agent mode the selection goes dormant rather than being discarded
(`App.engagedAgent`, `App.engagedAgentTools` both return nothing): the footer
hides it, the picker hides, the tool allow-list does not apply, and cycling
`agent → plan → goal → agent` gets it back. `/profile <name>` and `/agent`
engage the same field. The system prompt keeps its shape — the engaged text
is the single carrier block (`prompt.CarrierProfile`), so the identity block
naming Signet, the provider and the model still opens the prompt. For a
background definition the carrier text is its `system_prompt`, resolved by
`CarrierOptions` falling back to `agentprofile.Load`, and its `tools`
allow-list narrows the foreground session's registry the same way
`bgagent.buildSession` narrows it. Its `mode`, `schedule` and
`max_iterations` are background-loop settings and do not apply in the
foreground.

**Edge cases and business rules.**

- The picker is only visible in agent mode, only on the chat view, and only
  when no slash-completion popup or file chooser is active. An active `@`
  prefix hides the agent picker because the file chooser owns `@`.
- Opening the picker sets the highlight to `signet:debug` when it exists; if
  the default built-in is missing, the first available candidate is selected.
- Tab from a cold state (no highlight) lands on the first candidate; tab from
  the last candidate lands on `(none)`; tab again wraps to the first
  candidate. `(none)` only renders once an agent is engaged, or after the
  user has tabbed onto it.
- Accepting an agent closes the picker. When the picker was opened by a
  submit attempt over a non-empty composer, accepting also sends that prompt;
  otherwise the prompt text is left untouched and a later `enter` sends it.
- A picker opened by `/agent` is never a pending submit, so engaging an agent
  there leaves a half-written prompt in the composer alone.
- `(none)` clears an engaged agent and leaves the picker closed. It never
  sends a pending submit: agent mode with no agent has no carrier, which is
  the exact state the picker exists to prevent, so the prompt stays in the
  composer and the next `enter` reopens the picker.
- `esc` on an open picker cancels the pending submit along with the
  highlight; the prompt stays in the composer and is not sent later.
- `/clear` and `/resume` drop the pending submit with the rest of the
  session's picker state, so a prompt left unsent in one session can never be
  sent into another.
- If the picker is open and the user keeps typing, the highlight is preserved
  so the selection is stable. The picker only closes on `esc`, on accepting a
  candidate, or when the file chooser takes precedence.
- `loadAgents` reloads the candidate list from disk; a new user profile or
  background definition appears the next time the picker opens, or
  immediately after `/clear` starts a new session.

### Cursor motion in the composer

`components.Editor` wraps a bubbles textarea, which supplies character motion,
line motion (`home`/`end`, reached as `fn+left`/`fn+right` on a laptop
keyboard) and the readline-style editing keys. Word motion is Signet's own,
handled in `Editor.Update` *before* the message reaches the textarea.

It is handled at the Editor rather than in the `App` key switch so that every
text-entry context gets it — the composer, the credential fields, the agent
editor — rather than chat alone. The textarea's own `WordForward`/
`WordBackward` bindings are cleared in `NewEditor`: they were bound to alt
chords that never arrive, and their boundaries are whitespace-only, so leaving
them would mean two word motions that disagree.

**Boundary rules.** The motion matches the Pi coding agent's editor, which
segments a line and then splits word-like segments again at internal
punctuation. The observable result is a three-class model, and that is what
`wordmotion.go` implements: runs of whitespace, runs of word runes (letters,
digits, marks and `_`), and runs of everything else. One motion skips any
whitespace in its path and then crosses exactly **one** run:

| Line | From | `ctrl+right` | Why |
| ---- | ---- | ------------ | --- |
| `hello world` | 0 | 5 | to the end of the word, not the start of the next |
| `hello world` | 5 | 11 | the gap is skipped, then one word is crossed |
| `foo.bar` | 0 | 3 | the dot is a different class, so the word stops there |
| `foo();` | 3 | 6 | `();` is one run, so one hop |
| `foo_bar` | 0 | 7 | `_` is a word rune: one identifier |
| `3.14` | 0 | 1 | the point is punctuation here too |

`ctrl+left` is the mirror image: it skips whitespace behind the cursor and
crosses one run, landing on the *start* of a word where `ctrl+right` lands on
the *end*. That asymmetry is deliberate and matches both Pi and readline.

**Edge cases.**

- **Line boundaries.** `ctrl+left` at column 0 steps to the end of the
  previous line; `ctrl+right` at the end of a line steps to the start of the
  next. Holding the key therefore walks the whole prompt instead of stalling
  at a newline. At the very start or end of the text the cursor stays put — it
  never wraps around.
- **Soft wrap.** The textarea tracks its column against the wrapped grid, so
  the logical column is rebuilt as `LineInfo.StartColumn + ColumnOffset`. Word
  motion is defined on logical lines; a soft-wrapped row is not a boundary.
- **Focus.** A blurred textarea ignores every key, so word motion is gated on
  focus too. Otherwise these two keys would drive a cursor nothing else can.
- **Terminal encodings.** The match is on the bubbletea key *type*, not on
  `String()`. Several terminals encode ctrl+arrow in a form that decodes with
  the alt flag set (urxvt's `\x1b[Od`, xterm's `\x1b[1;7D`), which `String()`
  renders as an alt chord that no case would catch. A stray alt bit on these
  two keys is treated as noise.
- **Indices are runes, not bytes**, so the motion cannot land inside a
  multi-byte character.
- **Scripts without spaces.** A run of CJK is one word here, where Pi's
  segmenter would find boundaries inside it. Go's standard library has no word
  segmenter, and a coarse boundary beats a wrong one.

### Prompt library

Named prompts live in a directory of plain-text `.md` files, not a JSON blob.
Metadata is encoded in the filename: `NNN-slug.md` is an enabled entry at order
`NNN`, `_NNN-slug.md` is a disabled one. The global directory is
`~/.vulnetix/signet/prompts` (`config.GlobalPromptsDir`) and the project
override is `<workdir>/.vulnetix/prompts` (`config.ProjectPromptsDir`). The old
`prompts.json` format is abandoned outright — not read, not migrated, not
deleted. A leftover `prompts.json` is a one-line system notice (once per
session) telling the user the library moved; nothing ever touches the file.

**Filename grammar.** One strict regexp:
`^(_?)(\d{3})-([a-z0-9]+(?:-[a-z0-9]+)*)\.md$`. Exactly three digits
`001`–`999`; the slug is lowercase alnum with single interior hyphens, ≤64
runes. `_` is never legal inside a slug — slugging uses `-` as the separator,
not `_`, so a slug can never collide with the disabled marker. Anything that
does not parse — `README.md`, `.swp` files, sub-directories, crashed-renumber
temps — goes into `Listing.Strays` and is never read, renamed, or deleted:
fail closed, do not guess. The whole file is the prompt, verbatim; one trailing
newline is trimmed on read and appended on write, and `.md` is for editor
syntax highlighting only.

Browsing is entered with `up`. The result list is built once, when the cycle
starts: load both scopes, `promptlib.Merge` them, then `promptlib.Enabled`
(disabled entries never reach the cycle), then `promptlib.Filter`; then this
workdir's recent inputs, newest first, that match the same text and are not
already in the list by identical prompt text. The composer's text at
the moment `up` is pressed is the filter, and the match is a case-insensitive
substring test against name and prompt (`promptlib.Match`). The list is **not**
rebuilt mid-cycle.

**Recent inputs.** The unnamed tail of the list merges two sources by
timestamp (`inputhistory.Newest`):

- **Session prompts.** Every `user` entry in this workdir's session files
  (`session.Store.TimedUserPrompts`). An entry with no timestamp takes its
  session file's modification time.
- **Local commands.** Every `!cmd`, `!!cmd` and `/command` line run from the
  composer, including a line sent from the browse cycle and the `/agent …`
  line the agent-argument picker completes. These lines are not model turns,
  so they never become `user` entries. They are recorded
  (`App.runLocalInput`) in a per-project file,
  `<GlobalDir>/inputhistory/<WorkdirKey>.json` (`internal/inputhistory`),
  written atomically at `0600`. They are kept out of the session file on
  purpose: a first-line `/resume` or `/clear` would otherwise leave a session
  with no turns in the `/resume` list.

Rules for the input history file:

- A line is recorded **before** it runs, so a command that fails, is
  refused, or is unknown is still recalled, as a shell's history would.
- Text is trimmed and blank lines are skipped. Re-running a line removes its
  earlier copy, so each line is recalled once, at its newest position.
- The file keeps the newest `inputhistory.Max` (500) lines. The oldest are
  dropped first.
- Ties on timestamp keep list order (session prompts before local commands)
  and, within one source, treat the later entry as newer.
- A missing file is an empty history. An unreadable file is replaced on the
  next record. A failed write costs only the recall, never the command.
- Lines run before this file existed are not recoverable.
- The file sits beside the session files under the user's global directory,
  never in the repository. A `!cmd` carrying a secret is stored as typed,
  just as the shell panel already persists its output.

**Merge ordering.** Each scope sorts by `(Order, Name)`. The merged list is the
global block in global order, then project-only names in project order. A
project entry sharing a global name replaces the global entry's *content* but
keeps the **global slot** — and keeps its own project identity, so a disabled
project `_020-deploy.md` vetoes an enabled global `010-deploy.md` for that
workdir (enabled filtering runs after merge). A project entry shadowing a
global name is marked `override` in the manager, because the shadowing is
otherwise invisible in the `up` cycle.

Because library entries lead the list, the named ones are always a prefix of
it, and the TUI draws that prefix as a chip strip above the composer — one chip
per prompt *name*, the loaded one highlighted. `tab` walks the named prefix;
`up`/`down` walk the whole list including the unnamed recent inputs.

| Key | While browsing |
| --- | -------------- |
| `up` / `down` | Move through **all** results, library entries then recent inputs (prompts, `!cmd`, `!!cmd`, `/command`). `down` past the newest result restores the text you had before browsing |
| `tab` | Load the next **named** prompt, wrapping at the end of the named prefix. With no library match the strip is absent and `tab` does nothing |
| `right` | Accept the loaded prompt into the composer and leave the cycle, cursor at the end |
| `enter` | Accept the loaded prompt and send it (`/command` and `!shell` text dispatches as usual; while a turn runs it steers) |
| `esc` | Cancel: restore the text you had before browsing |
| anything else (printable characters, `backspace`, `←`, `home`, `delete`, …) | Leave the browse cycle **keeping the loaded prompt**, and apply the key as an ordinary edit |

**The composer badge.** When the loaded text came from a library entry, the
composer title carries a `✎ <name>` chip — with a `g` marker for a global
entry and a `*` dirty marker when the editor text differs from the entry's
body. `App.loadedPrompt` outlives the browse cycle and survives subsequent
edits, so `ctrl+s` always overwrites the right file in the right scope; it is
dropped the moment the composer stops representing that entry (submit, steer,
`!shell`, `/command`, save-as, cancel-save-file, new session, resume, and
`push()`/`pop()`, which reset the editor).

**`ctrl+s`** is the one guarded key that overwrites or deletes the entry
currently loaded. Hover wins first (save-file needs a live mouse position);
then a loaded entry opens the action bar; otherwise it is save-as, the same
shape as `f7`. The action bar reads `⏎ overwrite · d delete · esc cancel`;
the destructive confirm puts the question in the title (`overwrite existing
prompt 'deploy'?` / `delete prompt 'deploy'?`) with `y/N` in the meta, amber for
overwrite and red for delete. Overwrite calls `promptlib.Update` in place — a
global entry stays global, which fixes the old `f7` bug where re-saving a
global prompt minted a project entry. Delete removes the file. Belt and braces:
`ctrl+s` re-checks `loadedPrompt != nil`, `os.Stat(Path)` and a non-empty
composer before acting.

`f7` survives unchanged as a save-as alias (project scope): type a name and
Enter saves. `promptlib.Create` returns `ErrNameExists` rather than silently
upserting, and the TUI routes that into a `overwrite existing prompt 'deploy'?
y/N` confirm instead of quiet data loss. `Create` assigns `order = last + 10`,
clamped to 999, renumbering the scope first if the next slot would overflow; a
999-entry scope returns `ErrLibraryFull`.

**The `/prompts` manager** is a full-screen list over one scope (`s` toggles
global/project). Rows derive from `promptlib.Load` on entry and after every
mutation, so no cached slice drifts from the filesystem. Keys: `↑`/`k`,
`↓`/`j` move · `space` toggle · `J`/`K` reorder · `e` edit in `$VISUAL`/
`$EDITOR` · `a` new (ask a name, create the file empty, hand it straight to the
editor) · `d` delete (confirm) · `s` scope · `esc` leaves the sub-mode, else
back. Disabled rows get a muted style **plus an explicit `○` marker** — grey
alone is not a signal on a monochrome terminal. A muted footnote reports the
stray count.

**Reorder** renumbers the whole scope to `010, 020, 030…` after each move
(`step = max(1, 999/n)` past 99 entries): one pass, no gap arithmetic, and it
self-heals a directory hand-edited into collisions. Hand-picked numbers are
lost on the first `J` — the grid is what makes moves collision-free. Per-scope
only; the cross-scope order falls out of the merge rule. Renames are two-phase
(everything moving goes to a dot-prefixed `.signet-tmp-<i>-<slug>.md`, which
reads as a stray, then to its final name) because a swap collides in one phase;
`os.Lstat` every target before phase two and abort the whole reorder if
anything is there, since `os.Rename` overwrites silently on POSIX. A crashed
renumber therefore reads short, never wrong, and the stray count shows the
debris.

**`$EDITOR` hand-off.** `e` (and `a`) resolve `$VISUAL` then `$EDITOR`, split
on spaces so `code -w` and `emacsclient -nw` work, and resolve the binary with
`exec.LookPath` — never through `sh -c`. Empty or missing binary falls back to
the in-TUI field editor (`⏎ save · ctrl+j newline · esc cancel`). The hand-off
runs through `tea.ExecProcess` (a test seam on `App`), refuses while a turn
runs, and on return re-lists and re-finds the selection by `Path`. Two
non-obvious requirements: **restore the mouse** — `Program.exec` releases the
terminal and `RestoreTerminal` does not re-enable it, so the handler batches
`tea.EnableMouseCellMotion` gated on `mouseEnabled` (or it turns capture on for
a user who deliberately turned it off); and an editor that does not block
(`code` without `-w`) reloads a half-written file — documented, not defended
against.

Writes are atomic (temp file in the same directory + `os.Rename`), closing the
truncate-on-crash hole in the old JSON save. Directories are `0o755`; files
`0o600` in the global scope (personal) and `0o644` in the project scope
(committed, team-readable).

### Model roles

`/model` is the role screen. It opens with an **IN EFFECT** summary —
`work` (the agent model), `verdicts` (the fast tier, which serves the goal
contract too, even under `routed`; only with no fast tier does it read "Jev
picks from pool, else work model"), `drafting` (the
agent model for compaction and clarify, or Jev picking from the pool when
routing is live) and `security` (`run.GuardConfig`, or the local gates) —
resolved from the live config. Below it are labelled groups for the
**agent**, **fast tier**, **classifier**, **routing** and **session
posture** roles, each with a one-line description of what it decides (see
[Fast tier](#fast-tier)). The classifier group also carries a standing
warning that it is the security gate for tool output, because a weaker
classifier weakens detection everywhere.

The screen keeps two words for two places. **saves to** is where an edit on
a group is written — the group's scope (`session`, `global` or `project`),
shown once on its header, so the header never rewrites itself when the
cursor moves between roles. The fast tier saves *with routing*, since it is
stored in the routing block. The posture toggles (guardrails, ask, firewall,
caveman) always write the per-project preference file, so their group
carries that fixed target and `s` does nothing there. **set in** is a
value's provenance (`config.Source`). It is shown only when it differs from
the save target: hoisted onto the header when a whole group shares it, per
row when rows disagree, and in amber with a warning line when that layer
outranks the save target, because then an edit here lasts only until
restart.

The routing group lists the use-case entries as a **candidate pool**: under
`routed` Jev picks from the whole pool for each use case, so an entry's key
is a label, not an assignment; under `defined` the pool is unused and shown
dimmed. A use case with no entry reads `not in pool`.

Rows reuse the `/settings` declarative row table (`settingsRow`). The label
column is sized to the longest label and values keep their tail (the model
id), so no row overflows or collides. The selected row is highlighted; `⏎`
edits it, `s` cycles the save target for the active group, `c` clears the
row, `p` jumps to `/providers`, and `esc` returns to chat.

#### Fast tier role

**Provider** cycles the providers and clears the fast model, so the
provider's registry fast model applies until one is picked; **model** opens
the picker over that provider (Jev Decisions models are filtered out). The
value column shows the default in force, and says when the default's
provider is not configured. The target is stored as `routing.fast_model`
and follows the routing scope. `c` on provider drops the target; `c` on
model keeps the provider. The classifier group gains a **tier** row
(`main` / `fast`) for `classifier.tier`.

#### Agent role

The agent role is the global model settings section. Its rows are
**provider**, **model**, **effort**, **reasoning**, **caveman**,
**guardrails**, **ask**, **firewall** and **scope**:

- **Provider** cycles through the full list of available providers, in
  canonical order, wrapping from the last back to the first — every
  authenticated provider is reachable from any starting point. Changing
  provider clears the model, because a model id is only meaningful to its
  own provider. There is no unset stop in the agent cycle: the running
  configuration always carries a concrete provider name (`run.Prepare`
  normalises an empty provider to the default, so an unset stop would
  bounce on the next wrap and leave the providers sorting before the
  default unreachable). Unsetting is the `c` key's job.
- **Model** opens an embedded sub-picker over the selected provider's
  catalogue.
- **Reasoning drives effort.** Off writes `effort: "none"` and greys the
  effort row; on restores the previously selected chip (or the provider
  default).
- **Effort** cycles the model's advertised effort chips, or
  `low`/`medium`/`high` when none are advertised.
- **Caveman** is the agent's voice rewrite (`f2`), stored per project.
- **Guardrails**, **ask** and **firewall** are the same posture toggles as
  `f3`, `f4` and `f10`, surfaced here as rows so the global model settings
  show every stored global toggle.
- **Scope** is `session`, `global` or `project`. Session writes the agent
  provider/model/effort to `state.json` and updates the running
  configuration, so a fresh TUI opens with the same agent role unless an
  environment variable or CLI flag outranks it. `global` and `project` mutate
  the settings file for the active scope and reload the merged settings.

#### Classifier role

The classifier role rows are **kind**, **provider**, **model**, the phase
rows (when `kind` is `models`), **reasoning**, **effort**, **chunk** and
**scope**:

- **Kind** toggles `llm` ↔ `models`. On a binary that embeds a model it is
  locked to `models`; on a no-classifier binary it is editable, and choosing
  `models` expands the phase rows instead of silently downgrading to the LLM
  sentinel.
- **Provider** is restricted to classifier-capable sources (see the
  allowlist rule above): custom profiles, `llama-server`, `ollama`,
  `huggingface` when an HF token is configured, and `openrouter` when
  configured. It cycles with an inherit stop:
  `— (main: X)` means the classifier follows the main model.
- **Model** opens the sub-picker filtered by provider: `huggingface` shows
  only the five curated BERT ids, `openrouter` the Jev Decisions model
  (`typesafe/jev-1.13`, seeded) plus any `typesafe/jev*` ids, and the
  broad-model providers (custom, `llama-server`, `ollama`) show every model
  plus a warning line — *"Classifier provider: choose a classifier-specific
  model or switch to kind LLM for general chat models."* The routing
  use-case pickers, by contrast, leave out `typesafe/jev*` models: routed
  use cases need chat, and Jev cannot chat.
- **Phase rows** appear only when `kind` is `models`. Phase 1 is locked when
  embedded, shows the remote model when an HF token resolves it, or a
  "set HF token / provider" hint. Phase 2 renders the model when running,
  `disabled` when the user explicitly turned an embedded gate off (jailbreak
  variant), or **"deferred to phase 3"** when no local jailbreak gate can
  run. Phase 3 is a locked derived row: off until both classifier provider
  and model are set, then `extraction only` (or `jailbreak + extraction` when
  phase 2 is deferred).
- **Reasoning drives effort.** There is no separate reasoning key. Toggling
  reasoning off writes `classifier.effort: "none"` and greys the effort row;
  toggling it back on restores the previously selected chip.
- **Changing provider clears the model.**
- **Clear (`c`)** clears one field; clearing provider or model drops them both,
  and clearing every field removes the `classifier` block so the classifier
  falls back to the main model.
- **Scope is `global` or `project` only** — never session. `config.State`
  carries only the agent model/provider/effort, and a transient override of
  the security gate must be provenanced. Every row shows
  `Origin["classifier"]`.
- **Changes take effect immediately.** Each successful write re-resolves the
  classifier and reinstalls it, so the next turn uses it. A resolve failure
  (for example an unconfigured classifier provider) is reported on the page
  instead of silently falling back to the main model.

#### Model sub-picker

The model sub-picker is entered from the **model** row of either role. It
lists the provider's catalogue with windowing, `/` substring filtering, and a
`<cursor>/<total>` counter. `enter` selects the model under the cursor and
`esc` cancels.

The catalogue is built by merging three sources, lower priority last, and
 de-duplicating by model id:

1. **Live fetch** — when the provider exposes a model-list endpoint, Signet
   queries it on first entry and caches the result per session. The picker
   shows `○ Fetching models from GET <url>…` while a fetch is in flight.
   Live fetched models are not persisted.
2. **Profile models** — custom or saved models declared in the provider
   profile (`settings.json`) are merged next.
3. **Static fallback** — a hard-coded default catalogue for built-ins that
   have no endpoint or when the live fetch fails. Users can still type any
   model id and commit it.

`r` in `/providers` (models tab) clears the cache and re-fetches for the
selected provider; fetch errors render as `✗ fetch: ...` so silent failures
are visible.

#### Provider availability

`availableProviders` answers which providers a picker may offer. It is a UX
affordance, not a security boundary: `run.Prepare` and `credentials.Resolve`
remain the fail-closed gate on actually using a provider, so this filter
degrades *open* rather than closed.

Business rules:

- **Availability = credentials resolve.** The answer comes from
  `Resolver.ConfiguredProviders()`, so a provider with a missing required
  field is dropped.
- **Local providers need a live server, not a key.** `ollama` and
  `llama-server` declare every credential field optional, so they always
  report configured. They are therefore additionally probed with a bare
  `GET {base}/v1/models` (`localinfer.ProbeRunning`, no credentials, no
  content, 5s ceiling) and dropped when nothing answers.
- **Keyless custom providers also answer by liveness.** A custom profile's
  `api_key` is always optional; when no key resolves the provider is probed
  exactly like a local server and offered only when its models endpoint
  answers. `run.Prepare` injects a harmless placeholder key so the wire
  layer's non-empty-key check passes, and a server that really requires a
  key rejects the request with a 401 at runtime.
- **Pinned names always survive.** The committed agent provider and the
  committed classifier provider stay in the list even when unavailable, so
  a picker can never silently move the user off their own model. An
  unavailable pinned provider renders as an amber chip with an
  `unavailable` note instead of vanishing.
- **The list is never empty.** With no resolver, with no probe result yet, or
  with nothing available, the full provider list is returned together with a
  note explaining why — `checking providers…` or
  `no configured providers — showing all`.
- **Canonical order is preserved.** The filtered list is a subsequence of
  `providerNames()`, which keeps the `/model` provider cycles, the
  `/providers` master list and the by-name `/providers` jump consistent.
- **Caching.** One probe fills the cache; it is re-run when older than 30s,
  and never twice concurrently. Any credential mutation — store, clear, or
  import — invalidates it through `refreshCredentials`, and `r` in
  `/providers` forces a fresh probe. The probe runs on a `tea.Cmd`, never in
  the Update loop, and captures the resolver into a local rather than touching
  `*App` from the goroutine.

### Slash commands

| Command | Description |
| ------- | ----------- |
| `/profile` | Switch agent profile |
| `/providers` | Manage providers, credentials and local model servers |
| `/model` | Pick provider and model for the agent and classifier roles |
| `/mode` | Show or set operating mode (e.g. `/mode plan`) |
| `/todos` | Show plan progress |
| `/execute` | Leave plan mode and execute the plan |
| `/refine` | Refine the extracted plan |
| `/vulnetix` | Vulnetix code review and firewall (`review`, `configure`, `list`, `status`, `firewall`, `help`) |
| `/settings` | View and edit settings |
| `/permissions` | Edit tool permissions |
| `/help` | Show the commands and every keyboard shortcut |
| `/clear` | Start a new session |
| `/compact` | Summarise the session into a new one |
| `/resume` | Resume a session by id, or browse every session on disk |
| `/rename` | Rename this session |
| `/agent` | Manage background agents (`create`, `list`, `edit <name>`, `start`, `stop`, `pause`, `resume`, `log`); `log` opens the agent's audit trail |
| `/agents` | Open the agents screen: `running`, `profiles` or `audit` (bare `/agents` opens running once any agent has run, profiles before that) |

The table is the whole set registered by `internal/tui.NewRegistry`. Two
aliases exist but are not table rows: `/new` is a visible alias of `/clear`
(`RegisterAlias`, appears in `Names()` and autocomplete), and `/provider` is a
hidden alias of `/providers` (`RegisterHiddenAlias`, dispatchable but absent
from `Names()` and autocomplete).

The popup also lists `/prompt:<name>`, `/agent:<name>` and
`/process:<name>` library entries. These are not registered commands:
`handleCommand` dispatches them before the registry is consulted. Completion
over both commands and library entries is fuzzy. See *Slash popup: fuzzy
matching and library entries* above.

### Startup credential message

When the selected provider is unconfigured but other providers are, the TUI
points the user at `/providers` instead of claiming the selected provider's
credentials are missing. When nothing is configured, it says so clearly.

### Context accounting

`internal/transcript` implements hybrid token accounting with no tokenizer
dependency: anchor on the last assistant message carrying provider-reported
usage, then add a conservative `chars/4` estimate only for messages after that
anchor. The denominator is resolved by `internal/modelinfo.ResolveWith`:
user-provided `context_windows` overrides take precedence, then any
live-fetched window from the selected model, then the built-in registry.
`internal/modelinfo` therefore stays as an offline fallback rather than the
sole source of truth, and unlisted models render `(?)` rather than a guessed
denominator. After `/compact` the anchor describes the pre-compaction
conversation, so the footer renders `(?)` until a fresh assistant response lands.

### Settings

The effective settings view merges, lowest to highest: defaults, `state.json`,
global `settings.json`, the per-project user preference file
(`project_prefs`), project `settings.json`, environment, then CLI flags.
`/settings` shows the effective value and provenance for each key. Settings
include `provider`, `model`, `effort`, `caveman`, `read_only`,
`permissions` (structured `allow`/`ask`/`deny`), `session_retention_days`,
`ui.banner`, `ui.status_bar`, `ui.spinner`, `ui.show_reasoning`,
`ui.show_tool_calls`, `ui.show_edits`, `ui.show_todos`, `ui.show_internal_work`,
`ui.mouse`, `ui.colors`,
`ui.kitty_keyboard` (all default on when unset except `ui.show_reasoning`,
which defaults off unless explicitly true; `ui.show_internal_work` defaults to
`hidden`, the four-level role-manager feed described in
[role-manager.md](role-manager.md#tui-activity-signal); `ui.kitty_keyboard` is
overridden off by `SIGNET_NO_KITTY=1`),
`show_session_names` (default on),
`update_check` (default on; overridden off by `SIGNET_NO_UPDATE_CHECK=1`),
`context_windows`,
`resilience` (`max_attempts`, `max_iterations`, `max_passes`,
`max_clarify_rounds`, `max_explore_iterations`, `max_agents`,
`plan_explore`), `providers`,
`caveman` (default off; toggled from any screen with `f2`),
`guardrails` and `ask_permission` (both default on; toggled with `f3` and
`f4`, or together with `/yolo` — the repo-visible project layer may only
tighten them, never loosen a global `true` back to `false`; the per-project
user preference file may set them both ways),
`vulnetix.firewall_enabled` (default off; toggled with `f10` — the project
layer may only turn it off, the preference file may turn it on),
`allow_project_providers`, and the `classifier` block
(`provider`, `model`, `effort`, `chunk.max_bytes`, `chunk.concurrency`) covered
in the Security classifier section above. That enumeration is the whole
`config.Settings` struct, plus two keys that are accepted on read and never
written back:

- `bash_readonly` — the deprecated alias for `read_only`. `Settings.UnmarshalJSON`
  folds it into `read_only` only when the canonical key is absent, then clears
  it, so a file Signet rewrites emits `read_only` alone and a file carrying both
  keys resolves to the canonical one.
- the legacy flat `permissions` map (`{"Bash": "deny"}`) — accepted and
  converted to the structured `allow`/`ask`/`deny` shape on read, never written.

Permission
rules merge by union — a project file can add rules but never remove a
global rule. Provider profiles merge key-by-key the same way. Resilience
budgets merge to the *minimum* of global and project, so a project file can
tighten a budget but never raise one.

Tool availability defaults to allow: a call matching no permission rule
proceeds (unregistered tool names are still rejected by the agent's registry
check first). Opt-outs, in order of strength: a `permissions.deny` rule
always blocks; `read_only: true` (settings file or the `/settings` "read-only
tools" toggle) narrows **agent-mode** turns to `Registry.ReadOnlySurface` —
`Write`, `Edit`, and full `Bash` are withheld, though a read-only `Bash`
remains for inspection, and the TUI says so on the first agent-mode send.
Goal mode and an approved plan never honour `read_only`: they exist to change
files, and sessions showed goals stalling for their whole budget behind a
project file that had it set. The session always holds the full registry and
latches the narrowed surface per turn (`Session.turnReadOnly`), so what the
request advertises and what `executeCall` runs never diverge; and `postures: {permission_no_match: enforce}` in
`preferences.yaml` restores the legacy no-match-block. `Bash` otherwise runs
full shell commands via `sh -c` (timeout, env scrubbing, and output truncation
still apply); plan mode keeps `Bash` read-only regardless of `read_only`.

### Project-sticky session toggles

The session toggles — `f2` caveman, `f3` guardrails, `f4` ask, `f5`/`shift+tab`
mode, and `f10` firewall — persist to a **per-project user preference file**
under `<GlobalDir>/projectprefs/<workdir-key>.json`, never to
`.vulnetix/settings.json` and never into the repository. A repository can
therefore never ship a relaxation, but the user's own toggle does stick to the
project across sessions. Persistence is always on; there is no opt-out.

Business rules:

- **The allowlist is narrow.** The file carries only `guardrails`,
  `ask_permission`, `firewall_enabled`, `caveman`, `mode`, and `agent`. It can
  never define a provider, a permission rule, or a workspace directory.
- **Precedence sits above global, below everything repo-visible.** The merge
  order is defaults < `state.json` < global < project prefs < project
  `settings.json` < environment < CLI flags. When the agent role is set from
  `/model` with `session` scope, provider and model stay in `state.json`;
  with `global` or `project` scope they are written to the corresponding
  `settings.json` and follow the normal precedence rules. The session
  toggles (`f2`–`f5`, `f10`) never make provider/model project-sticky.
- **The only-tighten invariant still holds for the repo layer.**
  `.vulnetix/settings.json` may still only turn `guardrails`/`ask_permission`
  back on and only turn the firewall off. The user's own prefs set all three
  both ways. `Settings.Override` and `LoadMerged` are untouched; only the
  TUI's `config.Resolve` learns the prefs layer.
- **Env and flags outrank prefs.** `SIGNET_GUARDRAILS`,
  `SIGNET_ASK_PERMISSION`, `SIGNET_FIREWALL`, and the CLI posture flags beat a
  persisted pref. When a toggle is outranked, the TUI says so and names the
  winning source (`a.eff.Origin[...]`) rather than silently appearing not to
  stick.
- **`/yolo` off clears the guardrails/ask prefs**, otherwise leaving yolo
  would re-inherit a stale persisted relaxation.
- **Mode and the engaged agent restore per project.** A fresh session in the
  project reopens in the persisted mode (sticky and explicit, so the first
  turn's classifier cannot overwrite it) and with the persisted agent engaged,
  including its tool allowlist. The engaged agent name and its tool allowlist
  are written together in one place (`App.setNamedAgent`), so a profile's
  allowlist never outlives the profile it came from; an agent the user chose
  by hand (`/agent` picker, `/profile`) is not wiped by a classifier turn that
  names no agent.

## Local inference

Local inference is served by `llama-server` from llama.cpp. Signet treats the
local server as a first-class provider: `/providers launch <repo>` allocates a
port, downloads the GGUF through the Hugging Face CLI when it is present (or
lets `llama-server` fetch it when the CLI is absent), launches the server,
health-checks `GET {base}/v1/models`, persists host/port/protocol under the
`llama-server` credential, registers the process in the activity registry, and
lands on `/providers` with `llama-server` selected. From there `/model` and `/providers`
list the provider once availability re-probes.

Port allocation order is: an explicit `--port N` argument; the persisted
`llama-server:port` credential if that port is free or already serving a local
model; otherwise a fresh port returned by `net.Listen("tcp", "127.0.0.1:0")`.
`internal/localinfer.BaseURL` is the single source of truth for the
OpenAI-surface base URL, so the probe, the launch, and the stored credential
can never disagree. The old hard-coded `18080` and duplicate port lists are
gone.

Supporting pieces:

- `internal/localinfer/port.go` owns port allocation and base-URL spelling.
- `internal/localinfer/hfcli.go` wraps the `hf` / `huggingface-cli` binary:
  `HFBinary`, `HFDownload`, `HFCacheScan`, and `HFWhoami`. The token is passed
  through the child environment (`HF_TOKEN`) and never through argv.
- `internal/localinfer` launches the server through `proc.SetProcessGroup` and
  `proc.NewLineTee`, registers it in `internal/activity`, writes a pidfile
  under `GlobalDir()`, and stops gracefully with SIGTERM followed by SIGKILL
  after a short grace. Killing the TUI leaves the pidfile so a restart can
  detect and adopt or report the orphan.
- `internal/machineprobe` measures CPU threads, RAM, GPU backend/VRAM (via
  `llama-server --list-devices`), and free disk, then produces a plain-language
  suitability verdict. The verdict states the iGPU prefill caveat honestly:
  an integrated GPU shares LPDDR bandwidth with the CPU, so the security
  classifier — prefill-bound on large tool results — may classify *slower*
  than a small frontier model despite free VRAM. Chunked classification is what
  makes this tolerable.
- The HuggingFace token resolves as provider `huggingface` (`HF_TOKEN` /
  `HUGGINGFACE_TOKEN`) through the same credential stack as providers.
- `huggingface` is also a built-in chat provider using the OpenAI-compatible
  Serverless Inference API at `https://router.huggingface.co/v1`,
  authenticated with the same token.
- Probe base URLs are accepted with or without their `/v1` suffix:
  `ProbeRunning` trims a trailing `/v1` before appending `/v1/models`. Every
  in-tree caller holds the OpenAI-surface base (`run.Prepare` returns
  `…/v1`), so appending unconditionally probed `/v1/v1/models` — a path no
  server serves, which made both the already-running probe and `Launch`'s
  health check fail.
- The provider-availability filter behind `/model` and `/providers` uses the
  same probe: a configured-but-unreachable local provider is hidden from the
  pickers while staying visible in `/providers`.
- The TUI exposes this through `/providers` (the provider-management screen),
  `/providers report` (probe the machine and list running servers),
  `/providers status` (running servers and persisted port), `/providers launch
  <repo> [--port N] [--quant Q]` (download/launch/persist/land on credentials),
  `/providers download <repo> [--quant Q]` (download with `hf`), and
  `/providers stop [--port N]` (graceful stop via the activity registry).
  The same controls are reachable from the provider detail screen: `p` probes,
  `l` launches, `d` downloads and `x` stops. Quitting the TUI stops every
  managed `llama-server`.

## Supervised processes

The composer accepts `!!cmd` to start a **supervised process**. Unlike the
one-shot `!cmd` path, `!!cmd` runs the command through `sh -c` with **no
output cap and no timeout**, so it is suitable for long-lived servers, dev
servers, and watchers. The process gets its own process group, runs with a
credential-scrubbed environment (`proc.ScrubbedEnv`), and streams output to a
live tool row and a log file under `<GlobalDir>/logs` without contacting
the model while it runs.

When a supervised process exits without the user having stopped it, Signet
dispatches a **recovery subagent** with the command, the exit code, the run
duration, the attempt count, and the tail of the log. The subagent's registry
is intentionally narrow: the read-only plan surface plus `SubAgentLog` (to
search the process log) and `ProcessRestart` (to restart the process). It
has no other tools, may not fan out, may not clarify, and may not ask the user.
`ProcessRestart` accepts an amended command only when `argv[0]` matches the
original binary basename; the model may fix flags, but it may not swap the
executable. Deny permission rules are evaluated against the effective
command, and each restart call consumes one
`resilience.max_process_recoveries` slot (default 3).

The recovery subagent's tail input is untrusted and is classified when
guardrails are enabled. If the classifier declines it, recovery proceeds from
the harness-owned facts alone. The subagent's prose result is sanitised and
classified before it reaches the transcript. Success is determined by whether
the restarted process is still alive 10 seconds later, not by a sentinel, so
no new role-manager label is needed.

## Process library

Supervised processes share the prompt library's file-backed library shape via
`internal/filelib`: global and project scopes, filename grammar
`NNN-slug.sh` / `_NNN-slug.sh` for enabled/disabled, global entries overlayed
by project entries of the same name. The whole file body is the command,
verbatim. `!!cmd` writes the command to the project scope with a slug derived
from `argv[0]` and starts it. The manager screen (`/processes`) allows the
user to toggle auto-start, reorder, edit in `$VISUAL/$EDITOR`, create, delete,
run, stop, and view the log tail. `enter` on any process opens its full log
in the same full-screen output reader that the F9 runs panel uses.

The F9 runs panel has a dedicated **processes** tab that lists only currently
running processes from the merged process library, in library order. It shows
the command, the running state, and the PID. `enter` or `v` opens the live log
full-screen, `x` stops the selected process, and `r` restarts it (stop then
start with the same library command). The tab is running-only: a stopped
process disappears until it is started again, either from `/processes` or
with `/process:<name>`. Process output is also appended to the activity
registry as it arrives, so the reader stays live even when the process is
still running. Enabled entries
auto-start when Signet opens the workdir; a lock file per `(workdir-hash, slug)`
prevents a second Signet instance from launching a duplicate copy.

## Performance

The TUI's perceived-latency path is tuned at several layers:

- **Stream coalescing**: both stream hops are buffered (256) and the TUI's
  `nextAgent` drains a run of same-kind text/reasoning deltas into one update,
  so a long reply repaints once per drain rather than once per token.
- **Builder accumulation**: streamed text appends into a `strings.Builder`
  (`Message.AppendText`/`Text`), avoiding O(n²) string concatenation.
- **Per-message render memoisation**: `MessageList.Render` caches each
  message's rendered text + `LineMap`, keyed on the fields that affect it
  (content length, width, expand, role, status, …). The streaming tail and
  running tool rows re-render; everything else renders once per change.
- **Concurrent read-only tools**: a leading run of read-only, permission-allowed
  tool calls executes concurrently (bounded by 4), never reordering across a
  mutating call, and results re-enter in call order keyed by `ToolCallID`.
- **Shared HTTP client**: one tuned `httpclient.Default()` transport
  (`MaxIdleConnsPerHost: 16`, `ResponseHeaderTimeout: 30s`, no blanket
  `Client.Timeout`) serves provider, tool, credential and catalogue I/O. SSE
  streams carry an idle-gap watchdog instead. WebFetch uses a dedicated
  transport whose validating `DialContext` resolves once and pins the address,
  closing the DNS-rebinding TOCTOU.
- **Timing**: opt-in `SIGNET_TRACE=<path>` writes JSONL `{phase, event,
  duration}` records; the composer meta shows a live `working · N.Ns` elapsed
  label and running tool rows show live elapsed time.
