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

## What stays serial, on purpose

- **The goal judge.** At a pass boundary the decision backend rates the goal
  first and the model judge runs only when it is unsure, with the backend's
  scores. Running them together would spend a model call on every pass,
  including the passes the backend settles alone.
- **Admission.** The prompt is admitted before anything acts on it.
