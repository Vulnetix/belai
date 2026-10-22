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
- **Checks are batched.** SystemOne checks arriving within 8 ms share one
  request, up to 250 items
  ([role-manager.md](role-manager.md#batched-systemone-checks)).
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

Ten Go files and five markdown docs from this repository, read in one pass
(15 Read results at once), against a local SystemOne stand-in that costs
40 ms a request plus 1 ms a KiB:

| Server | Before | After |
| ------ | ------ | ----- |
| Answers requests in parallel | 15 requests, 325 KiB, 116 ms | 4 requests, 202 KiB, 110 ms |
| Answers one request at a time | 15 requests, 325 KiB, 926 ms | 4 requests, 202 KiB, 366 ms |
| Parallel, flat 150 ms a request | 15 requests, 325 KiB, 156 ms | 4 requests, 202 KiB, 166 ms |

Reading the same files again afterwards made no requests. The gain is
largest on a server that queues requests (a single local model, a rate
limit); on one that answers everything at once in a flat time, batching costs
its 8 ms gathering wait. Before the one-pass scanner, chroma's tokenising made
the new path slower on parallel servers; the scanner removed that. Time to
first token against a live provider was not measured here.
