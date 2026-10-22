# Turn latency

What runs between pressing enter and the first token, and between a tool
finishing and the next request, and how Belai keeps that short. Each rule
below names the code that holds it; a change that puts work back on the
critical path should change this page too.

## Classification

- **Code is read for its comments.** A Read of a code file classifies only its
  comments, docstrings and multi-line strings, and a file with none costs no
  call. Prose goes to a SystemOne decision backend when one is set, never
  through the BERT windows. See
  [architecture.md](architecture.md#how-much-of-a-read-is-classified).
- **One verdict per file.** A verdict is keyed on the file's path and digest,
  so reading another window of the same bytes costs nothing.
- **Docs are warmed.** With a SystemOne backend, the project's prose files are
  classified in the background at startup.
- **Checks are single unless you batch them.** `classifier.decision.batch:
  true` lets SystemOne checks arriving within 8 ms share one request, up to
  250 items ([role-manager.md](role-manager.md#batched-systemone-checks)).
  It is off by default because on Clef it measured slower (below).
- **One pipeline per session.** The classifier pipeline is built once per
  config (`internal/agent/pipeline.go`), not once per turn and once per tool
  result. A routed role classifier keeps its per-use-case route, so the router
  is not asked again every turn, and a remote gate's tokenizer is built once.
- **The SAFE cache holds 4096 verdicts**, so a session's warmed docs and the
  files it reads stay cached.

## Before the first token

- **Git sync runs beside admission.** The pre-turn sync (which may fetch)
  runs while the prompt is admitted and its intent detected, which read only
  the prompt. It is joined before anything reads the working tree: a
  `user_prompt_submit` hook, an `@file` attachment, or the rest of the turn.
- **Decision jobs start early.** The agent pick and the request-scale rating
  read only the prompt, so they start beside admission and intent detection.
  Tool selection starts as soon as the mode is known, beside exploration, the
  goal draft and prefetch, and is bounded by 8 seconds. An early answer that
  the turn does not use is dropped and never recorded; a tool selection whose
  surface changed in between is rated again.
- **Profiles are parsed once.** The agent pick lists your profiles every Auto
  turn; the parsed list is kept until a profile file's name, size or
  modification time changes (not the directory's time, which an edit in place
  leaves alone), and each call gets fresh copies
  (`internal/agentprofile/listcache.go`).
- **One git status per turn.** Prefetch and the turn's status block share one
  `git status`.
- **The sealed prompt survives a rebuild.** The TUI keeps the sealed system
  prompt beside the nonce pool it keeps across session rebuilds
  (`agent.SealMemo`), so the turn after a mode switch or a plan-mode turn
  sends the same prefix and the provider serves it from cache. The memo is
  keyed on the pool, so a prompt is never reused with another pool's nonces.

## Between passes

- **Nonces are topped up in the background.** A remote nonce pool fetches
  more when it falls to 16, so sealing a tool result does not wait on the
  endpoint (`internal/nonce/pool.go`).
- **Bash swaps are checked together.** The Bash calls of one response are
  checked for a builtin swap at the same time, within 8 seconds.
- **Decision jobs share connections.** The decision jobs use the shared HTTP
  client (16 idle connections per host), not one that keeps 2.
- **Score batches fit the backend.** A score batch never exceeds a backend's
  question or body cap, so none is refused and split.

- **The goal judge gets a head start, not a wait.** At a pass boundary the
  decision backend rates the goal alone for 1.5 seconds, so a pass it settles
  quickly costs no model call. Past that the model judge starts beside it, so
  an unavailable backend no longer holds the next pass for its 8 second
  timeout before the model is asked
  ([jev-jobs.md](jev-jobs.md#goal-judge)).

## What stays serial, on purpose

- **Admission.** The prompt is admitted before anything acts on it.

## Measured

### Live, against Clef

One headless turn (`belai -prompt … -mode agent`) that reads three Go files
and two markdown docs in one response, through the Vulnetix AI Firewall to
OpenRouter's free router, with Clef on Workers AI as the decision backend.
v0.154.0 (before) against this build with batching off and on, four runs
each, all built with the same `first_token` trace event. Each row gives the
range over the runs of when the first and the last of the five Read results
cleared the classifier, measured from the start of the classification.

| Build | First of five cleared | Last of five cleared | Pre-prompt | Checks that fell back to the chat model |
| ----- | --------------------- | -------------------- | ---------- | --------------------------------------- |
| v0.154.0 | 0.48 s to 0.80 s | 4.0 s to 4.6 s | 378 ms to 967 ms | 0 to 1 a run |
| this build, batching off (default) | 0.37 s to 0.53 s | 3.1 s to 3.3 s | 334 ms to 523 ms | none in three runs, 3 in one |
| this build, batching on | 2.3 s to 2.5 s | 4.7 s to 19.7 s | 345 ms to 675 ms | 1 to 4 a run |

Batching on Clef was slower: the batch request itself takes as long as the
largest item, a request with several items was answered less decisively, and
every undecided item sent its neighbours back alone. That is why batching is
opt-in. The live runs also found that Clef refuses question keys such as
`s:a1` with HTTP 422, which the transport now maps
([role-manager.md](role-manager.md#batched-systemone-checks)).

Time to first token is recorded as the trace event `agent/first_token`
(`BELAI_TRACE=<file>`). Against the free router it varied from 2.5 s to 21 s
in both builds, run to run, so these runs cannot show a difference in it;
the pre-prompt and classification times above are the part Belai controls.

### Offline, against a stand-in

Ten Go files and five markdown docs, read in one pass (15 Read results at
once), against a local SystemOne stand-in that costs 40 ms a request plus
1 ms a KiB, with batching on:

| Server | Before | After |
| ------ | ------ | ----- |
| Answers requests in parallel | 15 requests, 325 KiB, 116 ms | 4 requests, 202 KiB, 110 ms |
| Answers one request at a time | 15 requests, 325 KiB, 926 ms | 4 requests, 202 KiB, 366 ms |
| Parallel, flat 150 ms a request | 15 requests, 325 KiB, 156 ms | 4 requests, 202 KiB, 166 ms |

A stand-in answers a batch as decisively as single items, which Clef did
not, so batching looks better here than it was live. Reading the same files
again made no requests. Before the one-pass scanner, chroma's tokenising made
the new path slower on parallel servers; the scanner removed that.
