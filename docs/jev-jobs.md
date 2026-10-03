# Jev jobs

A decision backend (Jev) answers with numbers: one probability per question,
no generated text. Belai uses that for three security-adjacent decisions (the
security guard, intent detection and routing; see [role manager](role-manager.md#decision-backends))
and for a set of **relevance jobs** that make a session faster or cheaper
without approving anything. This page is the reference for the relevance jobs.

**Local first.** Every job on this page runs on whichever decision backend is
selected, and Strands Decider-2B can be that backend on your own machine
(`classifier.provider: strands-decider`). Then the security guard, intent
detection, routing and every relevance job are answered by a process on
loopback: no API key, no network, and no state leaves the host. See
[role manager](role-manager.md#decision-backends).

## The contract every job keeps

- **A job narrows or reorders. It never approves.** It works inside the surface
  the mode already allows. Permissions, hooks, the ask gate, the classifier and
  the OS sandbox all still apply to whatever it produces.
- **Failure means the ordinary behaviour.** A timeout, a refused credential, an
  oversized state, a low-confidence local answer or an answer that is not a
  probability in the range 0 to 1 is *unknown*, never a low score, and the job
  falls back to what runs without it. A job never falls back to asking a chat
  model, which would add latency.
- **Its input is harness facts and cleaned text.** Every string sent to a
  decision backend is a `DecisionText`, built only by `sanitize.ForDecision`
  ([sanitisation](sanitization.md#decision-backends-fordecision)). File
  contents, attachment bytes and tool output are not sent, with one exception:
  [knowledge topics](#knowledge-topics) sends a bounded sample of an indexed
  document's admitted text.
- **It needs a backend.** With no decision backend configured (the local
  decision model, Strands Decider-2B on this machine, the hosted `typesafe`
  provider, a `systemone` provider, or OpenRouter's hosted Jev) no job
  runs and none appears in `/settings`.

## Settings

Every job defaults on and has its own switch. The rows appear in `/settings`
(labelled `jev ...`) only while a decision backend is configured as the
classifier; the space key toggles a row and `x` returns it to its default.

```json
{
  "jev": {
    "jobs": { "bash_swap": true },
    "locate_previews": "local"
  }
}
```

| Key | Meaning |
| --- | --- |
| `jev.jobs.<job>` | `true` or `false` for a job in the list below. A missing key means on. An unknown job name is an error. |
| `jev.locate_previews` | Where declared names from your files may go when files are ranked: `local` (the local decision model, Strands Decider-2B and a self-hosted server only, the default), `hosted` (also OpenRouter and TypeSafe's hosted API) or `off` (paths only, everywhere). |

| `jev.thresholds.<key>` | A score cut-off, a number from 0 to 1, for a gate or job. A missing key keeps its default. The keys are in [Scores and thresholds](#scores-and-thresholds). An out-of-range value or an inverted pair is an error, and settings that fail to validate are never used. |

The project layer may turn a job **off**, never on, and may only narrow
`locate_previews`. It cannot set `thresholds`: a repository's value is ignored,
because moving a gate is your call. In `/settings` each cut-off is a slider row
(shown while a decision backend is configured): left and right move it by 0.05,
shift with them by 0.01, and `x` restores the default. A move that would break
one of the rules below is refused with the rule's message, and a change is saved
to your user settings and applies at once. Changing a switch drops the cached agent session so the next
prompt runs with it.

| Job | What it does | Status |
| --- | --- | --- |
| `bash_swap` | Runs a builtin tool instead of a Bash call when one is a clear match | Shipped |
| `prune_compaction` | Prunes tool calls and results by relevance before writing a summary | Shipped |
| `tool_selection` | Preloads the tools and lists the skills a request needs | Shipped |
| `tool_search` | Ranks `ToolSearch` matches with the backend and searches skills too | Shipped |
| `lsp_triage` | Files a board bug when another edit is unlikely to clear language server errors | Shipped |
| `option_order` | Puts the likeliest option first, marked (Recommended), when the model asks you to choose | Shipped |
| `explore_locate` | Ranks the files a question is about, seeds the explore subagents with them, and offers a `Locate` tool | Shipped |
| `voice_command` | Matches a short spoken instruction to one skill, crew, process, prompt, agent profile, the security review or a mode, and runs it only on a very close match | Shipped |
| `request_scale` | Rates a request as simple or staged, and starts a simple one at once without a goal contract, file prefetch or test run | Shipped |
| `goal_judge` | Rates each goal pass as complete, partial or not started, settles a clear verdict without the model judge, and otherwise hands the scores to the model judge as a hint | Shipped |
| `handoff_clarity` | Sends a delivery handoff to review when it rates as unclear, instead of straight to backlog | Shipped |
| `gate_alignment` | Flags a runnable gate whose suite and test may not show its stated outcome, and sends its card to review | Shipped |
| `request_coverage` | Files a gap card for a request clause whose covering tasks do not seem to do it | Shipped |
| `knowledge_topics` | Labels each indexed document with the topics it is about, by scoring a sample of its text against a vocabulary of about three hundred in one request | Shipped |

## Scores and thresholds

`Client.Score` in `internal/rolemanager/jev` rates a list of items against one
criterion. A request holds the long criterion once, in the state, and one short
question per item that points at it. Every cut-off is a `jev.thresholds` key
with the default shown; a job reads the value from your settings:

| Constant | Setting | Default | Use |
| --- | --- | --- | --- |
| `DropAt` | `drop_at` | 0.10 | An item scoring below this is dropped from a list it was on |
| `KeepAt` | `keep_at` | 0.50 | An item scoring at or above this is kept |
| `StrongAt` | `strong_at` | 0.80 | An item scoring at or above this is added to a list |
| `SwapAt` | `swap_at` | 0.95 | A single candidate at or above this replaces the call it rates |
| | `voice_at` | 0.95 | Exactly one target at or above this runs a spoken instruction; it cannot be set below 0.5 |
| | `simple_at` | 0.80 | A request at or above this, and not rated staged, is worked as a simple one; it cannot be set below 0.5 |
| | `goal_complete_at`, `goal_rival_max`, `goal_not_started_at` | 0.90, 0.20, 0.85 | A goal pass is clearly complete at `goal_complete_at` with both other options at or below `goal_rival_max`, and clearly not started at `goal_not_started_at` with the same limit; `goal_complete_at` cannot be set below 0.5 and `goal_rival_max` cannot exceed 0.5 |
| | `topic_at` | 0.70 | The knowledge index labels a document with a topic the backend scores at or above this; it cannot be set below 0.5 |
| | `clear_at`, `align_at`, `cover_at` | 0.50, 0.40, 0.40 | A delivery handoff rated below `clear_at` goes to review, a gate below `align_at` is flagged, and a clause whose tasks all rate below `cover_at` gets a gap card. They only narrow, so raising one means more review |
| `TriageAt` | `triage_at` | 0.30 | Below this another edit pass is judged unlikely to help |
| `HitAt` | `hit_at` | 0.50 | A located file at or above this is a hit |
| `LeadAt` | `lead_at` | 0.25 | A located file from here to `hit_at` is a lead, and below it is dropped |
| | `option_hit`, `option_margin`, `option_lead` | 0.50, 0.10, 0.25 | The first option is a clear winner at `option_hit` with a lead of `option_margin`, or with a lead of `option_lead` |
| | `mode_confident`, `mode_margin`, `mode_headless` | 0.80, 0.25, 0.50 | A detected intent is confident at `mode_confident` with a lead of `mode_margin`; a non-interactive run accepts one at `mode_headless` |

Two more cut-offs belong to the decision gates rather than a job:

| Setting | Default | Use |
| --- | --- | --- |
| `allow_at` | 0.10 | The security gate lets a call or content through at or below this. It cannot exceed 0.5. |
| `deny_at` | 0.90 | The security gate blocks at or above this. It cannot be below 0.5. |
| `route_at` | 0.50 | A routing candidate must score above this to be chosen |

`allow_at` and `deny_at` must be at least 0.05 apart, so a band always remains
where the answer goes to the agent model or to you. Lowering `deny_at` makes the
gate stricter; raising `allow_at` makes it looser. `drop_at` cannot exceed
`keep_at`, `keep_at` cannot exceed `strong_at`, and `lead_at` cannot exceed
`hit_at`.

How a request is sent:

- **Batching.** Items are packed by size. A remote backend takes up to 128 items
  and about 38,000 bytes per request, with up to 8 requests in flight. The local
  model answers one question per request, so its batches are at most 24 items
  and about 12,000 bytes and run one at a time.
- **Failures.** A batch that fails is split in half and retried, except after
  HTTP 429, where retrying harder makes it worse. An item whose own request
  fails is unknown. A refused credential (401 or 403) stops the call. A call
  makes at most 64 requests unless the job says otherwise.
- **Cache.** With a cache attached, a score is remembered for 30 minutes,
  keyed by the job, the backend identity, the criterion, the context, the extra
  facts and the item. Only numbers are stored, at most 4096 of them.
- **Validation.** Only a noul answer that is a finite number in the range 0 to 1
  counts. Anything else, and any item the backend did not answer, is unknown.
- **Options.** `Client.Pick` ranks 2 to 16 options. A local or self-hosted
  backend answers one choice question; OpenRouter answers noul questions only,
  so each option is scored and the scores are normalised. Probabilities sum to
  1, and a tie keeps the caller's order.

## Bash swap

When the model asks for a Bash command that one builtin tool does exactly, the
harness runs the builtin instead, and tells the model it did.

**What qualifies.** The command must be a single plain command: no pipe, chain,
redirect, substitution, expansion, assignment or line break (the same parse the
[read-only gate](sanitization.md#shell-commands-shellsafe) uses). Its program
selects a shortlist of builtins, for example `grep` and `rg` select `Grep`,
`cat` selects `Read` and `Cat`, `find` selects `Glob` and `Find`, `curl`
selects `WebFetch`, `git` selects `Git`. Only tools that are on the current
surface and allowed in the current mode are considered.

**How it decides.** In order, and any step that fails leaves the Bash call to
run as written:

1. Bash rules: a `Bash` deny rule that matches the command stops the swap, so a
   swap can never launder a denied command.
2. Jev rates each shortlisted tool against the criterion that one call of the
   tool produces exactly the output the command would print and has no other
   effect. Exactly one tool must score at or above 0.95. Two or more, or none,
   means no swap.
3. The fast-tier model is asked to reconsider the command. It answers either
   one JSON object of arguments for that tool, or the token `KEEP_BASH`. A
   reply that names `KEEP_BASH` and also offers arguments is treated as
   ambiguous and keeps Bash. A reply that is neither is malformed and keeps Bash.
4. The arguments must pass the tool's schema (`CheckArgs`: no undeclared key, the
   declared types, enums and formats, every required argument present) and must
   be grounded in the command: every string and number in them appears in the
   command, is `.`, or is one of the tool's enum values. Booleans always pass.
   The fast model cannot choose its own target.
5. The builtin must be allowed in this mode (plan mode's surface included) and
   must not be denied by a permission rule. A Bash allow rule does not carry
   over; the builtin's own rules and ask gate apply.

**What the model and the user see.**

- The tool result for the model's Bash call id starts with a harness line:
  `[harness: your Bash call was replaced by Grep (Jev rated it 97% equivalent);
  the Bash command was not run; the output below is from Grep]`. It holds tool
  names and a percentage only, then the builtin's result.
- The provider still receives exactly one result for the Bash call id, and the
  assistant turn keeps the model's Bash call, so the transcript stays valid.
- The TUI tool row reads `Grep ← Bash`. The start and result events carry
  `SwappedFrom`.
- The result is classified as Bash output would be, even when the builtin's own
  kind is shaped like `Grep`. A Grep row from a file whose read was withheld
  earlier is still withheld first. Swapping never lowers the scrutiny a
  command's output gets.

**Edge cases.**

- A command sent again after a swap runs as Bash: one swap per command per
  session (keyed by a hash of the command).
- Explore and other subagent sessions run no jobs.
- If the fast tier is missing, the ordinary role classifier serves the replan at
  low effort; any failure of the replan keeps Bash.
- The score, the tool name and the outcome are recorded as `bash_swap` and
  `bash_replan` activity events. They never carry the command.

Recorded outcomes: `swapped` (the builtin ran), `kept` (Bash ran; either no tool
rated high enough, or the fast model kept it) and `refused` (the replanned call
failed a check).

## Option order

When the model asks you to choose (`AskUserQuestion`), the option you are most
likely to pick comes first, and it is marked **(Recommended)** when the choice is
clear.

- **When.** In `handleAskUser`, after questions asked earlier are dropped and
  before the questionnaire is remembered, shown, recorded or sent to the
  website. The answers name positions in the questionnaire, so ordering it once,
  up front, means the TUI, the recorded ask, the website and the text the model
  reads back all agree.
- **How.** Each group is ranked separately (at most three at a time). The
  backend sees the question, your request for the turn and each option's label
  and description, all as `DecisionText`, and returns one probability per
  option: a choice question on the local model or a self-hosted server, or a
  noul question per option on OpenRouter, normalised. Options sort by
  probability, highest first; ties keep the model's order. The number of
  options, their wording and the question never change.
- **The marker.** The top option is marked when it holds at least 50 percent
  with a lead of at least 10 points over the runner-up, or leads the runner-up
  by 25 points. A group that allows several answers is ordered but never marked.
  The marker is always ` (Recommended)`, and a label is shortened so it still
  fits the 80-character limit.
- **The model cannot forge it.** Any `(recommended)` the model wrote into a
  label, in any case, in parentheses or brackets, is removed from every option.
- **Without a backend.** With the job off, or the backend unavailable for a
  group, the model's order is kept. If the model marked one option, that option
  moves first and gets the canonical marker, so the marker always reads the
  same and at most one option carries it.
- **Answers.** The answer text returned to the model uses the labels it wrote,
  without the marker. The repeat check is keyed on the question, so a reordered
  question is still recognised as asked.
- **In the TUI.** The cursor starts on the first option, which is the
  recommended one; the mode-choice panel finds its recommended row whatever the
  case of the marker.

Recorded as an `option_order` event: `ordered` when every group was ranked,
`fallback` when some were not, with the number of groups. Never the question or
the options.

## Compaction prune

When the context passes 70 percent of the model window at a pass boundary,
Belai used to write a summary. With a decision backend it first tries to prune:
remove the tool calls and results the work ahead no longer needs and keep
everything else word for word. A pruned conversation contains no new text, so
there is nothing new to admit and no long model call to wait for.

- **Pairing.** Each call is paired with its result by call id, in order, with a
  short id (`t1`, `t2`, ...). A call with no result, or a result with no call, is
  left alone, so the provider never sees a mismatch that was not already there.
- **Pinned, never judged.** The first request; the newest 6 turns; a call whose
  turn carries signed thinking; anything that changed something (a mutating
  tool); a failed or withheld result or a command that exited non-zero;
  `update_plan`, `ExitPlanMode`, `AskUserQuestion`, `Skill`, `SkillDraft`,
  `Task`, `ToolSearch`, `ReadResult`, the Kanban tools and MCP tools.
- **What the backend sees.** The user's last three requests as the goal, and a
  digest of the conversation: each message's text and, for each call, its tool,
  its input and the size of its result (`ok, 2000 chars (omitted)`), never the
  result itself. The digest is fitted to a byte budget (about 7,000 for the
  local model, 24,000 for a remote backend) by shrinking the oldest first: call
  inputs to 1,000, then 200, then 60 characters; long messages abridged; old
  messages collapsed to their length; old calls reduced to one line each; old
  messages with no calls left out. The first request and the newest turns are
  never shrunk. A digest that cannot fit means no prune.
- **Two statements per call.** "The call should stay in the history" and "the
  full output should stay verbatim, and running the tool again would not help",
  each scored against the conversation and the goal.
- **Decision per call.** A result at or above 0.50 stays whole, with its call.
  Otherwise a call at or above 0.50 stays and its result is cut to its first 300
  characters plus a note (only if it is more than 120 characters longer than
  that, and never a preview that points at `ReadResult`). Otherwise the call and
  its result both go. A question the backend did not answer keeps.
- **Cleanup.** An assistant message left with no text and no calls is removed.
  One that still has text keeps it. The rebuilt messages are new values, so no
  cached rendering of the old ones carries over.
- **When it counts.** The prune is accepted only if the estimated context is
  then below the compaction trigger. Otherwise, and whenever the job is off, the
  backend is unavailable or refuses, or there is nothing to judge, the summary
  runs on the original conversation as before.
- **After a prune** the request is not restated (the conversation, request
  included, is still there); after a summary it is.

Recorded as a `prune_compaction` event: `pruned` with the number of pairs kept,
shortened and dropped, the questions left unanswered and the tokens freed, or
`fallback` when the prune fell short. Never any conversation text.

## Tool and skill selection

A request carries the core tools in full and the model loads the rest with
`ToolSearch`, and every installed skill's name and description rides in the
system block. With a decision backend the harness rates the deferred tools and
the skills against your request at the start of each turn, so the likely ones
are ready before the model has to ask and only the relevant skills are listed.

- **What is rated.** The deferred tools on the current surface and the
  installed skills a model may invoke (a skill marked `disable-model-invocation`
  is not offered). Each is described by its name and the first sentence of its
  description. With more candidates than one round trip should carry, the
  candidates that match the request best by keyword are rated: 24 for the local
  model, 200 for a remote backend.
- **What happens.** Deferred tools rated at or above 0.50 are loaded, best
  first, at most 6, through the same loader `ToolSearch` uses, so only tools on
  the current mode's surface load (plan mode cannot load a writer). Skills rated
  at or above 0.50 are listed, best first, at most 5, plus any skill the request
  names outright. The rest are left out of the system block, which says how many
  ("N more skills are installed but not listed. Find one with ToolSearch, then
  load it with the Skill tool.").
- **Nothing is disabled.** An unlisted skill is found by `ToolSearch` and loaded
  with the `Skill` tool; a deferred tool still runs when called by name.
  `ToolSearch` stays advertised whenever skills are installed, even when no tool
  is deferred. With deferral off, or no `ToolSearch` in the registry, every skill
  is listed as before.
- **Falls back** to listing every skill and loading nothing when the job is off,
  the request is empty, or the backend cannot answer.
- **Cache-friendly.** The choice is made once per turn, before the system block
  is sealed. Loading only appends to the tool list, so the cached prefix holds.

Recorded as a `tool_select` event: the tools loaded, the skills listed and left
out, and an estimate of the tokens saved by the omitted skill lines. Never the
request.

## Tool search

`ToolSearch` finds tools and skills. A keyword query is ranked deterministically
first; with a decision backend the list is then refined.

- **The deterministic list.** BM25 over each candidate's name (weighted three
  times) and description, after splitting camelCase and snake_case words,
  dropping stopwords and applying a light stem so "reading" and "read" meet. A
  query that names a candidate outright scores higher. Ties sort by name, so the
  same query always gives the same list. `select:Name1,Name2` matches exact names
  (tools and skills) and never goes through the backend.
- **The merge, with a backend.** The backend rates the deterministic list and a
  wider pool. An entry rated below 0.10 is dropped from the list; an entry it did
  not rate stays; any other candidate rated at or above 0.80 is added. The result
  is ordered by the backend's score (an unrated entry counts as 0.50), ties in
  the deterministic order, and capped at `max_results`.
- **What comes back.** Names only, never a description. Tools are loaded into the
  request. A skill is named ("found 1 skill(s): git-ops. Load a skill by calling
  the Skill tool with its name.") and the model loads it.
- **Falls back** to the deterministic list when the job is off or the backend
  cannot answer. If every entry is rejected the answer is "no match".

Recorded as a `tool_search` event with the size of the deterministic list, the
merged list and the candidates left unrated. Never the query.

## Explore locate

Before the explore subagents start, the harness ranks the files the goal is
about and tells each subagent where to begin, so they search less. The model can
also ask for the same ranking with the deferred `Locate` tool. The result is
paths, line numbers and percentages the harness computed; no file text is in it,
so it is sanitised like a `Glob` result and not classified.

**Eligibility comes first.** Before anything is scored, labelled or sent, a
file must be eligible. Left out for good: dependency and build directories
(`node_modules`, `vendor`, `venv`, `.venv`, `.tox`, `__pycache__`, `dist`,
`build`, `coverage`, `target`, `.next`, `.nuxt`, `.turbo`), every hidden file and
directory, `.git`, symlinks and special files, files that hold credentials
(`.env` and `.env.*`, `.netrc`, `.npmrc`, `.pypirc`, `.htpasswd`, `.pgpass`, `credentials*`, `secrets.*`,
`id_rsa*` and the other `id_*` keys, and `.pem`, `.key`, `.p12`, `.pfx` and
similar), files with a known binary extension, files over 16 MiB, and anything a
`.gitignore` or `.ignore` excludes. A `.gitignore` counts at every directory
level; a nested repository starts its own git scope; `.ignore` is read after
`.gitignore` in the same directory and wins. A file whose first bytes contain a
NUL is dropped when it is read for declarations. At most 100,000 files are
listed. A file whose read was withheld earlier in the session is never ranked
or shown to the backend. These rules are a floor, not a setting.

**Ranking, in order.**

1. Every file is scored by the words of its path (camelCase, snake_case and
   kebab-case split, with a light plural trim) using BM25.
2. Up to 48 directories (12 for the local model) go to the backend: those whose
   paths match first, then the ones holding the most source files. A directory
   the backend rates below 0.25 is dropped; one it gave no answer for is kept.
3. Files in the directories that survive are read for the names they declare
   (Go files are parsed, other languages are scanned line by line) and the best
   160 (24 for the local model) go to the backend.
4. A file at or above 0.50 is a hit, one from 0.25 to 0.50 is a lead. There is
   no fixed count: every file above 0.25 is returned, up to 25. A file with no
   answer keeps its keyword score, because unknown is not low. If the backend
   answers but rates everything below 0.25, the three best keyword matches are
   returned as leads.
5. Without a backend answer the keyword order stands (at most 12 files).

**What the backend sees.** The question (as `DecisionText`) and one label per
item: `directory internal/auth (4 files: .go x3, .md x1)`, or
`file internal/auth/login.go (Go, 2 KB)`. The labels include
`declares Login, VerifyPassword` (up to 12 names) only when previews are
allowed for that backend: the local decision model and a self-hosted server get
them, OpenRouter and TypeSafe's hosted API get paths only, and
`jev.locate_previews` moves that line either way (`off` means paths only
everywhere). File contents beyond declared names never leave the machine.

**What the subagents get.** Each task prompt gets one line naming up to 8 ranked
files, strong matches first, as `path:line`. Only paths made of letters, digits
and `. _ - / @ +` are named, so a file name with a space, quote or markup
character never enters a prompt. A task that was told where to look loses one
tool round when the ranking found a strong match. When the backend answered and
none of its files is a test, the "tests" survey task is dropped; likewise "docs
and config" when none is a document or configuration file.

**The Locate tool.** `Locate` takes `query` and an optional `max_results`
(default 12, at most 25) and returns lines such as
`1. internal/auth/login.go:4  97%`, with `(lead)` on weaker ones. It is deferred
behind `ToolSearch` and exists only while the job is on.

**/locate --dry-run** lists, with no request made, the eligible files and bytes
per top-level directory, what was left out and why, and where questions would
go: the backend, and whether declared names would go with them.

Recorded as an `explore_locate` event: `ranked` or `lexical`, with the hits
found and the directories, files and unknowns the backend was asked about.
Never the question or a path. The inventory is reused for 90 seconds and a walk
stops after 4 seconds; a partial inventory is still ranked.

## LSP triage

After a `Write` or `Edit` the language server's errors ride back on the result
and the model tries again. Some errors do not yield: the same ones return pass
after pass, or the fix lies outside the file. From the second pass on, with
errors still present, the backend is asked how likely another pass is to clear
them. When it is unlikely, the harness files a bug on the kanban board itself
and tells the model, so the model carries on with its goal, plan and todo steps
instead of circling.

- **What is tracked.** Per file, for the session: the pass count, the number of
  errors after the first, previous and current pass, and a signature of the
  error set (source, code and message of each error, sorted, with no line
  numbers, so moving code is not a new error). Only errors count, not warnings,
  and a report where the server had not answered (warming, timed out) is
  ignored. A pass with no errors clears the file's history.
- **What the backend sees.** How the passes went ("Edit pass 3 on this file.
  Errors now: 2. Errors after the first pass: 5. Errors after the previous pass:
  2. The errors are the same as after the last pass.") and up to 10 errors as
  `line N source code: message`, as `DecisionText`. It rates "another edit pass
  will clear the remaining errors in this file".
- **Unlikely** means a score below 0.30. Without an answer the pass count
  decides: at `lsp.max_repair_attempts` passes (default 4, from 2 to 20) with the
  error count not below the previous pass, the bug is filed anyway. Errors that
  are shrinking never trigger the fallback.
- **The filing** is the harness's, with no model text: an item titled "LSP
  errors remain in <path>" in the review list, labelled `bug` and `lsp`, with
  the errors in its body, stamped with the session's provenance. A live item
  with the same title is reused, so one file has one item.
- **What the model reads**, after the diagnostics block: "[harness: another edit
  is unlikely to clear the 2 error(s) still reported in pkg/main.go after 3
  passes, so a bug was filed on the board as K-abc123. Do not keep editing this
  file for them. Continue with your remaining goal, plan and todo steps, and say
  in your report that K-abc123 is open.]" Later edits with the same errors get a
  short reminder that the item exists. If the errors change shape, counting
  starts again.
- **Never filed** in plan mode, by a fleet worker (which cannot write the board
  freely), with no board, or with the job off.

Recorded as an `lsp_triage` event: `filed`, `retry` or `unfiled`, with the pass
count, the error count and the backend's score in percent (-1 when it did not
answer). Never a path or a message.

## Voice command

With [voice input](voice.md#spoken-instructions) on, a short utterance that is
not a spoken keyword and (with `voice.wake_word` on) follows the wake word is
matched to something you could have asked for by name, and that thing runs.

- **What is offered.** Up to 48 targets, each a plain identifier: plan mode and
  goal mode, the security review, your crews, agent profiles, saved processes,
  saved prompts and installed skills. Only the kind and the name go to the
  backend, plus one fixed line for the mode and review targets. No description
  from a skill file, prompt body or process command is sent.
- **What the backend sees.** The speech, cleaned, as `DecisionText`, and the
  names. It rates, for each target, "the speech is an explicit instruction to
  start exactly the thing named", so a sentence that only mentions a name, asks
  about it or is about something else does not count.
- **When it runs.** Only at 24 words or fewer, with `voice.commands` and the
  job switch on, and a backend configured. The match waits at most 4 seconds.
  Typing cancels a pending match.
- **The decision.** Exactly one target at or above `voice_at` (0.95) runs. Two
  at or above it is ambiguous and nothing runs, the same as none. A score of
  0.94 is a miss. Any failure, a missing backend or a timeout leaves the speech
  as ordinary dictation, exactly as if the job were off. The job never asks a
  chat model.
- **What running means.** The target's own function, the one its slash command
  calls. A process still refuses in plan mode, a crew still passes fleet
  preflight and the worker cap, an agent profile still has to be one the picker
  offers, and a prompt only fills the composer for you to send. A skill is
  submitted as "use the NAME skill" through the typed-prompt path, so every
  admission gate applies. A shell line said aloud is never run.

Recorded as a `voice_command` event: `matched` or `none`, the kind of target
and the score in percent. Never the speech or a name.

## Request scale

Goal mode wraps a turn in ceremony that pays for itself on a large request and
only delays a small one: a contract drafted by the fast model, a prefetch of
every changed file, a planning list, a verification pass before the goal may
end, and the repository's test suite as the verification surface. With a
decision backend the harness rates the request once at the start of a goal
turn, so a request such as "commit and push" starts at once.

- **What the backend sees.** The cleaned prompt as `DecisionText`, nothing
  else. It rates two items against one criterion: a simple request (a few
  direct actions, no investigation or design) and a staged request (dependent
  stages, investigation, design or many files).
- **The decision.** A request is simple only when it rates at or above
  `simple_at` (0.80) as simple and below `keep_at` (0.50) as staged. An
  unanswered item, a timeout (8 seconds), a missing backend or a switch that is
  off is unknown, and the goal runs exactly as before.
- **What a simple verdict drops.** The goal contract draft (the prompt is the
  objective as written), the prefetch of changed files (the agent instruction
  files still ride along), the planning list and test-suite verification
  surface in the first-pass directive, the verification pass that gates
  completion, and the no-write escalation. The goal loop and its evaluator still
  run, so the harness still ends the goal.
- **What it never changes.** The mode you chose stays. Permissions, hooks, the
  ask gate, the classifier and the sandbox apply to every call as usual. The job
  never approves anything and never asks a chat model.
- **Without a backend.** The first-pass directive and the contract prompt say
  the test commands verify code changes, and that a request which only runs
  commands needs no test run unless it asks for one.

Recorded as a `request_scale` event: `simple`, `staged` or `unknown` and the
score in percent. Never the request.

## Goal judge

A goal ends a pass when a judge says whether the goal is complete, partial or
not started. The model judge (`goal_eval`) reads a digest of the pass. With a
decision backend the harness asks that backend first, on three options scored
against one criterion, so a clear case needs no chat-model call.

- **What the backend sees.** The goal, the rendered todo list and the pass
  ledger's own facts (pass number, counts of changed files, their paths, and
  whether a verification pass has run), all as `DecisionText`. It never sees the
  pass digest, the reply or a file's contents.
- **The decision.** The pass is clearly complete when `complete` rates at or
  above `goal_complete_at` (0.90) and both other options are at or below
  `goal_rival_max` (0.20). It is clearly not started at `goal_not_started_at`
  (0.85) under the same limit. An unanswered option is unknown, never a low
  score.
- **When it is not clear.** A contested score, a timeout (8 seconds), an error
  or a missing backend sends the pass to the model judge exactly as before. When
  the backend answered, its percentages ride on the model judge's input as a
  harness line, and the judge is told to treat them as a prior and to answer
  complete only when the digest shows a check made with tools, such as a passing
  test or build, or the changed files read back after the last edit. Otherwise
  it answers partial and the agent is sent back to verify.
- **A stronger verification pass.** If the model judge then says complete, the
  verification directive that gates completion also asks for a proper check with
  tools: run the tests or build, read back changed files, and compare each
  requirement with what is on disk.
- **What it never changes.** The verification gate is unchanged, so a clear
  complete verdict still runs one verification pass when none has run. The mode,
  permissions, hooks, the ask gate, the classifier and the sandbox apply as
  usual. The job never approves a call.

Recorded as a `goal_judge` event: `complete`, `not_started` or `unclear`, and
the three scores in percent. Never the goal, the list or any evidence.

## Delivery crew jobs

Three jobs work inside the [delivery crew](fleet.md#the-delivery-crew). All three
only narrow. Each can send a handoff to review, flag a gate or file a gap card;
none can move a card out of review, mark a gate met or a request clause covered,
or skip a check the harness runs. The rules they sit on (the counted clarity
rule, the harness-run gates, the recorded clause coverage) decide alone when the
backend is off, slow or unsure, and only titles and identifiers ever reach a
backend, never file contents, test output or attachments.

### Handoff clarity

With `kanban.quality.list` or `kanban.survey.list` set to `auto`, the harness
routes a scout's handoff by counted facts. When those facts say backlog, the
backend rates the task once more.

- **What the backend sees.** The handoff's title and body as `DecisionText`. It
  rates one item: a clear task an agent can start at once, against the criterion
  that the task says what to change, where, and how to tell it is done.
- **The decision.** A task rated below `clear_at` (0.50) goes to review, with the
  line `review: the decision model rated it unclear` on its body. A rating at or
  above it changes nothing: it is only the absence of an objection.
- **Edge cases.** A handoff the counted rule already sent to review makes no call.
  A worker whose list is fixed makes no call. An unanswered item, a timeout (8
  seconds), a missing backend or a switch that is off is unknown, and the counted
  rule stands. The job can never move a handoff toward backlog.

Recorded as a `handoff_clarity` event: `clear`, `unclear` or `unknown` and the
score in percent. Never the task.

### Gate alignment

A gate is a test reference, so a title can promise more than the test shows, for
example `docs are updated` against a whole Go suite.

- **What the backend sees.** For each runnable gate, its own title and the
  identifiers of its test (suite, package directory, test name), as `DecisionText`.
  It rates each against one criterion: the outcome named is something that test
  would show to be true or false.
- **The decision.** A gate rated below `align_at` (0.40) is flagged, and the
  handoff goes to review with `review: gate G1 may not measure its title`. Only a
  handoff the counted rule would have sent to backlog is rated.
- **Edge cases.** A manual gate is never rated. A gate the backend did not answer
  is not flagged: unknown is not a low score. The gate itself is unchanged and the
  harness still runs it.

Recorded as a `gate_alignment` event: `aligned`, `flagged` or `unknown`, and
counts.

### Request coverage

The harness files a gap card for a clause no handoff covers. This job doubts the
other case, a clause a handoff claims to cover but does not do.

- **What the backend sees.** Each clause and the title of each task that covers
  it, as `DecisionText`, rated against one criterion: the task would accomplish
  the request part.
- **The decision.** A clause whose covering tasks all rate below `cover_at` (0.40)
  gets a gap card titled `Part C2 of request K-xxxxxx may not be done by its
  tasks`, filed once. The card names ids only. The scout's card note lists the
  doubted clauses.
- **Edge cases.** One unanswered pair leaves the clause alone, and so does one
  well-rated task among several. A clause is never marked covered or uncovered by
  the model: the recorded coverage stands either way. The gap card of the same
  clause is never doubled.

Recorded as a `request_coverage` event: `sound`, `suspect` or `unknown`, and
counts.

### Knowledge topics

The [knowledge index](knowledge.md#labels-and-topics) labels every document with
the topics it is about. A pattern detector does this on its own and needs no
model. With a decision backend this job refines it, so a topic the patterns
missed is found and one they over-read is dropped.

- **What the backend sees. This is the one job that sends document text.** A
  sample of the document's chunks, as `DecisionText`, and the labels of the
  topics asked about. The text has already been sanitised and admitted by the
  security classifier at ingestion, and a flagged chunk is never stored, so it is
  never sent. A document of at most `knowledge.topic_chunks` chunks (default 12)
  is sent whole when it fits the request. A longer one is sampled: the first
  chunk, the last and evenly spaced ones between, as many as the request holds.
  Scanner artifacts are labelled from their kind and tool and are never sent.
- **One request.** The question carries as many topics as the backend takes in a
  request (128 for OpenRouter, TypeSafe and a self-hosted server, 24 for the local
  model), the ones the pattern detector found first and then a spread across the
  vocabulary's domains. The document text gets the rest of the request's byte
  budget; if the topics leave too little, the lowest ranked are dropped first. A
  failed request is not retried or split. Each topic is rated against one
  criterion: the topic is a main subject of the document, not a passing mention.
- **The decision.** A topic the backend was asked about and answered keeps the
  label when it scores at or above `topic_at` (0.70), and loses it below that,
  even if the patterns found it. A topic it was not asked about, or did not
  answer, keeps the pattern detector's verdict. The label is a search aid. It
  does not admit, permit or approve anything.
- **Bounds.** At most `knowledge.topic_budget_docs` documents (default 40) are
  sent per refresh, so a first index of a large corpus is filled over several
  refreshes. After three failed requests in a refresh the backend is left alone
  until the next one. With a budget of 0, no backend or the switch off, nothing is
  sent and the pattern detector labels alone. Both `knowledge` keys and
  `topic_at` are read from your own settings layers only.
- **Edge cases.** A document the backend did not answer keeps its pattern topics
  and is tried again at a later refresh while there is budget. The call stays out
  of the score cache, since a document is not asked about twice.

Recorded as a `knowledge_topics` event: `scored` or `unknown`, and counts of
topics asked and labelled, never the document or a topic.
