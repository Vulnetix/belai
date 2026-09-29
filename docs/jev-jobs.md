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
  decision model, a self-hosted Jev provider, or OpenRouter's hosted Jev) no job
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
