# Jev jobs

A decision backend (Jev) answers with numbers: one probability per question,
no generated text. Belai uses that for three security-adjacent decisions (the
security guard, intent detection and routing; see [role manager](role-manager.md#decision-backends))
and for a set of **relevance jobs** that make a session faster or cheaper
without approving anything. This page is the reference for the relevance jobs.

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
  contents, attachment bytes and tool output are not sent.
- **It needs a backend.** With no decision backend configured (the local
  decision model, the hosted `typesafe` provider, a self-hosted Jev provider, or OpenRouter's hosted Jev) no job
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
| `jev.locate_previews` | Where file previews for file ranking may go: `local` (the local or self-hosted backend only, the default), `hosted` (also OpenRouter) or `off` (paths only). Reserved for the explore ranking job. |

The project layer may turn a job **off**, never on, and may only narrow
`locate_previews`. Changing a switch drops the cached agent session so the next
prompt runs with it.

| Job | What it does | Status |
| --- | --- | --- |
| `bash_swap` | Runs a builtin tool instead of a Bash call when one is a clear match | Shipped |
| `prune_compaction` | Prunes tool calls and results by relevance before writing a summary | Shipped |
| `tool_selection` | Preloads the tools and lists the skills a request needs | Shipped |
| `tool_search` | Ranks `ToolSearch` matches with the backend and searches skills too | Shipped |
| `lsp_triage` | Files a board bug when another edit is unlikely to clear language server errors | Shipped |
| `option_order` | Puts the likeliest option first, marked (Recommended), when the model asks you to choose | Shipped |

## Scores and thresholds

`Client.Score` in `internal/rolemanager/jev` rates a list of items against one
criterion. A request holds the long criterion once, in the state, and one short
question per item that points at it. Thresholds are constants that jobs read:

| Constant | Value | Use |
| --- | --- | --- |
| `DropAt` | 0.10 | An item scoring below this is dropped from a list it was on |
| `KeepAt` | 0.50 | An item scoring at or above this is kept |
| `StrongAt` | 0.80 | An item scoring at or above this is added to a list |
| `SwapAt` | 0.95 | A single candidate at or above this replaces the call it rates |

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
