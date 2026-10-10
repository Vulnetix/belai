# Plan mode tuning log

This page tracks the work to make plan mode end every turn the same way: a plan
file under `.vulnetix/plans/`, shaped like the plans a reviewer expects, offered
to the user to approve, refine or leave. It records the failure that started the
work, what plan mode does now, the scoring rubric fixed before any prompt was
changed, the test matrix, one row per measured run, and the revision that
changed each result, so the effect of a prompt or harness change is measured
rather than remembered.

All runs use the provider `cloudflare-workers-ai` and the model
`@cf/moonshotai/kimi-k2.7-code`, headless (`belai -plan -mode plan -prompt …`),
against throwaway CRUD projects created fresh for the work and deleted after
scoring. The projects are described under [Test matrix](#test-matrix) so a round
can be reproduced; the scripts that drove it are in
[`bench/plan-mode/`](../bench/plan-mode/README.md).

## What plan mode does now

1. **The contract.** Pass 1 carries a planning contract (`prompt.PlanContract`).
   It says the turn ends when `ExitPlanMode` is called, names the 12-round read
   budget and tells the model to batch reads and stop once it can name the files
   and the changes. It fixes the plan's shape: a `# title`, `## Summary`,
   `## Key Changes` (numbered steps in an order that keeps the build working,
   each with `Files` and exactly one `Verify` command), `## Test Plan`,
   `## Assumptions`, and `## Risks` only when one exists.
2. **The read budget.** Four rounds before a pass's budget ends, and again on
   its last round, a sealed directive tells the model how many rounds are spent
   and to call `ExitPlanMode` now. The last round of every pass offers only the
   finishing tools (`ExitPlanMode`, `AskUserQuestion`), so the plan
   is written there. A pass that still ends with no plan makes the next pass a
   finishing pass with the same tools.
3. **The end of the turn.** `ExitPlanMode` ends it. The first call of a turn is
   checked by the [plan lint](role-manager.md#plan-lint) and sent back once if a
   step has nothing to run, verifies with a test a later step creates, the Test
   Plan names tests no step writes, or the plan names a file, directory, npm
   script or test that does not exist in the repository, chains commands in a
   `Verify`, or leaks thinking-out-loud; on a pass's last round the pass gets up to two more rounds for the
   correction (a refused `update_plan` on that round gets one too), and on the finishing pass it is accepted as written. A plan of five
   or more steps with no flaw is sent back once with a fixed review checklist.
   If no corrected plan arrives, the plan that was sent back is the one recorded.
   A plan written as the reply instead of
   the tool argument is accepted too, without an evaluator call, with any
   narration before its first heading dropped. An evaluator that says
   `PLAN_COMPLETE` when no pass produced a plan is not believed.
4. **The file.** The plan is written to `.vulnetix/plans/<name>.md` with a
   title, and every line under a step stays with that step. Text that is not a
   plan is never recorded as one.
5. **The review.** The TUI opens its review pane (approve here, approve in a new
   session, edit, refine, cancel); `belai -plan` now starts it in plan mode, and
   the permission decision model is told when a call targets a file the approved
   plan lists. A `belai rc` session, with or without web controls, records a
   `plan_review` ask that carries the plan, takes the web's answer while idle
   (approve runs the plan on the full tool surface, refine sends the notes back
   to the planner, stay leaves it) and is documented in
   [remote control](remote-control.md). A headless `-prompt` run prints
   `plan written: <path>` to stderr with the commands that approve or refine it.

## The failure that started this

Session `d9292a3d-9fc3-4f85-a530-7d02e7d83d6a` (project `website`, source
web, mode forced to plan). The user asked for a structured nix-package field
with registry lookups in the launch editor. What happened, from the transcript:

| Step | What the harness recorded |
| --- | --- |
| Pass 1 | Ten assistant rounds, 25 `Read`, `Grep` and `Glob` calls, three `update_plan` calls, no `ExitPlanMode`. |
| Budget | The pass spent its 12-round read budget; the harness logged "planning pass 1 spent its read budget with no plan drafted; the next pass writes it". |
| Last text | The pass ended on garbled prose ending in a stray `</think>` with no plan in it. |
| Evaluator | `plan_eval` answered `PLAN_COMPLETE` because every research step in the `update_plan` checklist was marked completed. |
| Result | The loop returned the garbled text as the plan. The plan file (273 bytes) holds that text. The turn offered no review the web could answer: the remote-control runner handled permission and clarify asks and passed the plan-file event to the log. The session then idled for 30 minutes. |

Defects, in order of weight:

1. **The evaluator could declare a turn complete without a plan.** Its prompt
   said `PLAN_COMPLETE` meant "every item in the plan todo list is researched",
   so a finished research checklist read as a finished plan, and the write-only
   pass the harness had just scheduled was discarded.
2. **The model never wrote the plan while it had reading tools.** The contract
   described the plan's shape but never said that the turn ends at
   `ExitPlanMode`, that reading is bounded, or when to stop.
3. **A non-plan reply was recorded as the plan.** `recordPlan` fell back to the
   reply text and wrote it verbatim even when it had no numbered steps.
4. **A web session was never asked.** The remote-control turn runner had no
   plan-review ask, so Approve, Refine and Cancel existed only in the TUI.
. **`belai -plan` did not start the TUI in plan mode.** Found by driving the TUI:
   the flag marked the mode sticky and left the mode as saved, normally agent, so
   the first prompt ran with every tool and edited files with no plan.
7. **An approved plan's own edits were denied.** Found by approving a plan in the
   TUI: the permission decision model saw only "Execute the approved plan." and
   denied the edit of a file the plan listed, with high confidence, without asking.

8. **A plain remote session could never take a review answer.** Found by reading how
   `belai rc` is wired: only sessions started with web controls had an answer
   channel, and the failing session's turn facts show it was not one. The plan
   review added for web sessions therefore could not have helped that exact
   session until plain sessions were given the channel and a way to run an
   approved plan.

**Reproduction.** The session ran against website commit `71c04d7e`. Running its
own prompt against an export of that commit (task R1 below), the old build spent
its first pass's whole read budget with no plan drafted in 3 of 3 runs, the
condition behind `d9292a3d`. In those runs the evaluator answered
`PLAN_PARTIAL`, so the finishing pass wrote a plan, and the file had no title
and detached step bodies. The evaluator-says-complete path that recorded a
sentence in `d9292a3d` did not occur in a live run; it is covered by a test with
a scripted evaluator (`TestPlanPassLoopRejectsCompleteVerdictWithoutAPlan`).

## How runs are scored

The rubric was fixed before any prompt changed. A run is scored on four gates
and six quality rows.

**Gates (all four must pass for a score)**

| Gate | Pass when |
| --- | --- |
| G1 file | A plan file exists under `.vulnetix/plans/` when the turn ends. |
| G2 shape | The file has a title, `## Summary`, numbered steps, `## Test Plan` and `## Assumptions`. |
| G3 exit | The turn ended through `ExitPlanMode`, never through the pass ceiling or an evaluator verdict. |
| G4 clean | The file holds no reasoning leakage (`</think>`, "I are"), no narration after the last section, and no implementation claimed as done. |

G1, G2 and G4 are computed from the file, G3 from the transcript, by one script
for every run. A run that fails a gate scores 0 whatever its quality; its
quality marks are still shown.

**Quality (0 to 3 for each row, 18 at most)**

| Row | 3 means |
| --- | --- |
| Q1 targets | Every file named exists or is marked new; every file the change needs is named. |
| Q2 completeness | Every part of the request is covered by a step; tests and docs are scheduled when asked for. |
| Q3 actionable | Each step says what and where, with `Files` and exactly one `Verify` that could run at that point (its tests exist by then, the code compiles). |
| Q4 proportional and clean | Size fits the task; no padding, no "Risks: none", no garbled tokens, no leaked narration. |
| Q5 decisions | Every open choice is decided once, consistently; nothing is left for the user inside a step. |
| Q6 tests | Concrete named cases and the command that runs them, each scheduled in a step's `Files`. |

**Who scores.** The quality rows are scored blind by an independent model
(Claude Sonnet) that is given the rubric, the task, the plan and the project
directory to check claims against. The plans of every revision for a project
are mixed under random ids and scored in one pass, so one judge applies one
standard to all of them and cannot see which revision wrote which plan. The
revision-to-id map is not shown to it. My own first scores of `v0` to `v3`
(made before this method) ran one to two points higher per plan and are
superseded. Scoring was repeated as builds were added; the last round scored all
thirty-one builds together with clarifications the judges' notes had shown to be
needed: a `Verify` that chains commands with `&&` is a Q3 flaw, a documentation
step that `grep` could check but says `Verify: none` is a Q3 flaw, and a task
that needs no new tests scores 2 on Q6 when the Test Plan names the command that
runs the existing suite. They are in `bench/plan-mode/rubric.md`.

**Measured, not judged.** Rounds are the model's tool rounds in the turn; the
rest come from the transcript and `-usage-json`.

| Metric | Source |
| --- | --- |
| Wall seconds | Process start to exit. **Noisy:** projects run in parallel and some rounds overlapped test suites and builds on the same machine, so the same revision varies by a factor of two or more. Read rounds and tokens first. |
| Rounds | Assistant turns in the transcript. |
| Evaluator calls | `plan_eval` entries (0 when the turn ended on `ExitPlanMode` in its first pass). |
| Tokens | Provider-reported total from `-usage-json`, every role. |
| Lint rejections | Tool results beginning *plan not accepted yet*. |

Limits of the method: one model under test; one judge model; five small tasks
and two large ones, run once or twice per revision; a judge that is
strict about `Verify` commands, which lowers every build alike; and a `website` repository that other sessions were editing
during the runs. Repeats of the same revision differ by up to 2 points in mean
quality, so differences smaller than that are not findings.

## Test matrix

| Id | Project | Size | Task |
| --- | --- | --- | --- |
| T1 | go-crud (net/http, in-memory store) | simple | Add `PATCH /notes/{id}` updating only the fields present; keep PUT. |
| T2 | py-crud (Flask, SQLite) | simple | Add limit/offset pagination to `GET /items` with an `X-Total-Count` header. |
| T3 | ts-crud (Express, TypeScript, vitest) | simple | Validate `POST /tasks` title (required, 1–100 characters) and return a JSON 400. |
| T4 | go-crud | trivial | Change the default port from 8080 to 9090 and update the README. |
| T5 | ts-crud | large | Add user accounts: register and login with JWT, tasks owned by their creator, owner-only read/update/delete, tests, README. |
| S1 | `website` (Vue, Cloudflare Worker) | large, real repository | Per-tenant sliding-window rate limiting in the sandbox Worker, limits per route group, 429 with `Retry-After`, counters in the Durable Object or D1, tests and docs. |
| S2 | `website` | vague, real repository | "make the pix sandbox launch flow more robust and easier to debug". |
| W1 | `website` | the failing session's prompt | The nix-packages prompt from `d9292a3d`. Only `v0` and `v1` ran it: the checkout has since gained the structured Nix extension work, so a good plan has to notice that. |
| R1 | `website` at commit `71c04d7e` | the failing session's own conditions | The same prompt as W1 against an export of the code the session saw, three runs per build, to see whether pass 1 runs out of reading budget with no plan. Only the number of passes and the gates are read from it. |

The three small projects are created by `bench/plan-mode/mkprojects.sh`:
`go-crud` has `go.mod`, `main.go`, `store/store.go`, `store/store_test.go` and a
README; `py-crud` has `app.py`, `models.py`, `tests/test_app.py`,
`requirements.txt` and a README; `ts-crud` has `src/app.ts`, `src/server.ts`,
`src/store.ts`, `src/routes/tasks.ts`, `test/tasks.test.ts`, `package.json`,
`tsconfig.json` and a README. Each has a project `.vulnetix/settings.json` that
routes the classifier and the evaluator roles to `cloudflare-workers-ai`.

## Results

Fifty-six builds ran: `v0` is the code as found, `v1` to `v16` are the revisions in
the [revision log](#revision-log), and `b` to `g` mark further runs of the same
build (`v0`, `v1` and `v2` ran once; `v9c` to `v9g`, `v10c` to `v10e`, `v11c`
to `v11e`, `v12c` to `v12e`, `v13c` to `v13e`, `v14c` to `v14e`, `v15` to `v15e` and
`v16c` to `v16e` ran only the large prompts).
Each build ran T1–T5 and S1–S2 unless it is in that list, and `v0`,
`v6` to `v11` and `v13` to `v16` also ran the reproduction R1. "Gates" is the number of gates passed.
All builds' plans were scored together in the last scoring round, with the
rubric's clarifications, so the numbers on this page replace those of any earlier
round. The same plans score about 0.8 points differently from one scoring round
to the next, so only comparisons made inside one round are used. One `v13` run
produced no plan and is in the tables as a failed run with quality 0.

### By revision

Small tasks T1–T5 (five runs per row; gates out of 20):

| Revision | Gates | Quality (of 18) | Score | Rounds | Wall s | Tokens | Lint rejections |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v0 | 15/20 | 13.0 | 0.0 | 4.6 | 45.0 | 15970 | 0 |
| v1 | 20/20 | 15.8 | 15.8 | 5.0 | 39.7 | 20814 | 0 |
| v2 | 18/20 | 15.6 | 12.4 | 4.6 | 34.4 | 20183 | 0 |
| v3 | 20/20 | 15.8 | 15.8 | 4.4 | 31.7 | 20357 | 0 |
| v3b | 20/20 | 15.4 | 15.4 | 3.4 | 28.5 | 14631 | 0 |
| v4 | 20/20 | 16.4 | 16.4 | 5.4 | 33.6 | 27065 | 1 |
| v5 | 20/20 | 16.8 | 16.8 | 5.2 | 48.8 | 27519 | 0 |
| v5b | 20/20 | 15.0 | 15.0 | 5.4 | 83.0 | 28406 | 0 |
| v6 | 20/20 | 16.8 | 16.8 | 4.8 | 35.8 | 26609 | 0 |
| v6b | 20/20 | 15.8 | 15.8 | 4.0 | 20.7 | 20068 | 0 |
| v7 | 20/20 | 16.6 | 16.6 | 4.2 | 54.3 | 21582 | 0 |
| v7b | 20/20 | 16.4 | 16.4 | 5.4 | 32.9 | 27711 | 0 |
| v8 | 20/20 | 16.2 | 16.2 | 3.8 | 44.5 | 19187 | 0 |
| v8b | 20/20 | 15.8 | 15.8 | 3.4 | 38.0 | 18659 | 1 |
| v9 | 20/20 | 16.2 | 16.2 | 4.8 | 55.4 | 24886 | 2 |
| v9b | 20/20 | 16.4 | 16.4 | 4.8 | 33.5 | 24320 | 0 |
| v10 | 20/20 | 16.4 | 16.4 | 3.4 | 53.9 | 18204 | 1 |
| v10b | 20/20 | 15.2 | 15.2 | 5.4 | 47.3 | 29164 | 2 |
| v11 | 20/20 | 16.8 | 16.8 | 4.2 | 33.7 | 22506 | 2 |
| v11b | 20/20 | 17.0 | 17.0 | 4.2 | 52.9 | 22632 | 1 |
| v12 | 20/20 | 17.2 | 17.2 | 5.4 | 49.0 | 28736 | 3 |
| v12b | 20/20 | 16.0 | 16.0 | 5.4 | 58.0 | 31840 | 3 |
| v13 | 20/20 | 17.2 | 17.2 | 5.2 | 41.5 | 27358 | 1 |
| v13b | 20/20 | 16.4 | 16.4 | 5.4 | 39.5 | 28888 | 1 |
| v14 | 20/20 | 15.6 | 15.6 | 5.2 | 52.3 | 28308 | 1 |
| v14b | 20/20 | 16.4 | 16.4 | 4.4 | 37.6 | 24499 | 1 |
| v16 | 20/20 | 17.2 | 17.2 | 4.0 | 22.5 | 19778 | 1 |
| v16b | 20/20 | 15.8 | 15.8 | 5.0 | 70.1 | 26537 | 1 |

Large `website` prompts S1–S2 (two runs per row; gates out of 8):

| Revision | Gates | Quality (of 18) | Score | Rounds | Wall s | Tokens | Lint rejections |
| --- | --- | --- | --- | --- | --- | --- | --- |
| v0 | 6/8 | 8.0 | 0.0 | 13.5 | 167.9 | 711691 | 0 |
| v1 | 8/8 | 11.5 | 11.5 | 12.0 | 146.0 | 722729 | 0 |
| v2 | 8/8 | 13.0 | 13.0 | 10.5 | 101.7 | 325884 | 0 |
| v3 | 8/8 | 12.5 | 12.5 | 11.0 | 156.8 | 739321 | 0 |
| v3b | 8/8 | 12.5 | 12.5 | 10.5 | 165.6 | 802914 | 0 |
| v4 | 8/8 | 12.5 | 12.5 | 12.0 | 156.2 | 658399 | 2 |
| v5 | 8/8 | 12.5 | 12.5 | 12.0 | 163.8 | 686309 | 0 |
| v5b | 8/8 | 14.0 | 14.0 | 9.0 | 310.2 | 462244 | 0 |
| v6 | 8/8 | 13.5 | 13.5 | 10.5 | 174.9 | 467224 | 0 |
| v6b | 8/8 | 12.0 | 12.0 | 11.5 | 141.0 | 732736 | 1 |
| v7 | 8/8 | 9.5 | 9.5 | 13.0 | 127.6 | 673837 | 0 |
| v7b | 8/8 | 12.0 | 12.0 | 12.0 | 189.5 | 642020 | 1 |
| v8 | 8/8 | 11.0 | 11.0 | 12.0 | 136.4 | 695606 | 0 |
| v8b | 8/8 | 12.5 | 12.5 | 13.0 | 224.5 | 780288 | 0 |
| v9 | 8/8 | 16.0 | 16.0 | 11.0 | 154.1 | 770333 | 1 |
| v9b | 8/8 | 14.5 | 14.5 | 10.5 | 189.5 | 707482 | 0 |
| v9c | 8/8 | 15.0 | 15.0 | 11.0 | 177.3 | 709937 | 1 |
| v9d | 8/8 | 12.0 | 12.0 | 12.0 | 108.4 | 577440 | 0 |
| v9e | 8/8 | 13.5 | 13.5 | 11.0 | 124.4 | 680800 | 0 |
| v9f | 8/8 | 12.5 | 12.5 | 12.0 | 105.8 | 562069 | 0 |
| v9g | 8/8 | 12.5 | 12.5 | 12.0 | 113.1 | 756943 | 0 |
| v10 | 8/8 | 13.0 | 13.0 | 12.0 | 129.3 | 560134 | 0 |
| v10b | 8/8 | 12.5 | 12.5 | 11.5 | 159.6 | 745552 | 1 |
| v10c | 8/8 | 16.0 | 16.0 | 11.5 | 175.8 | 652098 | 2 |
| v10d | 8/8 | 12.0 | 12.0 | 12.0 | 142.1 | 669365 | 1 |
| v10e | 8/8 | 13.0 | 13.0 | 12.0 | 126.5 | 628141 | 0 |
| v11 | 8/8 | 14.0 | 14.0 | 12.0 | 140.4 | 631164 | 0 |
| v11b | 8/8 | 14.0 | 14.0 | 12.0 | 153.4 | 527399 | 1 |
| v11c | 8/8 | 14.0 | 14.0 | 12.0 | 126.6 | 643237 | 1 |
| v11d | 8/8 | 13.0 | 13.0 | 15.0 | 138.0 | 765293 | 1 |
| v11e | 8/8 | 15.0 | 15.0 | 12.0 | 152.7 | 556438 | 0 |
| v12 | 8/8 | 12.5 | 12.5 | 12.0 | 124.5 | 489822 | 1 |
| v12b | 8/8 | 12.5 | 12.5 | 12.5 | 176.0 | 735289 | 0 |
| v12c | 7/8 | 12.5 | 5.0 | 12.5 | 123.7 | 707903 | 1 |
| v12d | 8/8 | 16.5 | 16.5 | 11.0 | 459.8 | 778046 | 1 |
| v12e | 8/8 | 14.5 | 14.5 | 12.0 | 159.6 | 717501 | 0 |
| v13 | 8/8 | 14.5 | 14.5 | 12.0 | 194.6 | 547399 | 1 |
| v13b | 8/8 | 15.5 | 15.5 | 12.5 | 229.9 | 691989 | 1 |
| v13c | 8/8 | 14.0 | 14.0 | 12.0 | 191.0 | 779407 | 2 |
| v13d | 5/8 | 6.5 | 6.5 | 13.0 | 158.4 | 628195 | 2 |
| v13e | 8/8 | 15.0 | 15.0 | 11.5 | 182.3 | 558291 | 1 |
| v14 | 8/8 | 12.5 | 12.5 | 13.0 | 206.4 | 646964 | 2 |
| v14b | 8/8 | 16.0 | 16.0 | 12.5 | 312.0 | 756641 | 1 |
| v14c | 8/8 | 14.5 | 14.5 | 13.0 | 145.5 | 567820 | 2 |
| v14d | 8/8 | 16.5 | 16.5 | 11.0 | 221.5 | 441121 | 1 |
| v14e | 8/8 | 14.5 | 14.5 | 12.5 | 233.3 | 877536 | 0 |
| v15 | 8/8 | 15.5 | 15.5 | 12.0 | 214.8 | 502839 | 2 |
| v15b | 8/8 | 13.5 | 13.5 | 13.0 | 216.8 | 696286 | 2 |
| v15c | 8/8 | 13.0 | 13.0 | 19.0 | 240.7 | 822034 | 2 |
| v15d | 8/8 | 14.5 | 14.5 | 12.0 | 177.7 | 571537 | 2 |
| v15e | 8/8 | 16.0 | 16.0 | 11.5 | 130.7 | 678149 | 2 |
| v16 | 8/8 | 14.5 | 14.5 | 12.5 | 175.4 | 593564 | 1 |
| v16b | 8/8 | 15.0 | 15.0 | 13.0 | 205.7 | 714772 | 1 |
| v16c | 8/8 | 17.5 | 17.5 | 11.0 | 193.7 | 607368 | 1 |
| v16d | 8/8 | 17.0 | 17.0 | 14.0 | 235.9 | 857238 | 1 |
| v16e | 8/8 | 16.0 | 16.0 | 13.0 | 153.1 | 813622 | 1 |

Pooled over `v1` to `v16b`, mean quality is 16.2 on the small tasks (135 runs,
revision means from 15.0 to 17.2). On the large prompts it is 12.2 for `v1` to
`v8b` (26 runs, 7 scored 14 or above), 13.6 for `v9` to `v13` (54 runs, 26 of
them 14 or above) and 15.1 for `v14` to `v16` (30 runs, from 11 to 18, 25 of them
14 or above). The last six groups of five builds average 14.0 (`v11`), 13.7
(`v12`), 13.1 (`v13`, with the run that produced no plan), 14.8 (`v14`), 14.5
(`v15`) and 16.0 (`v16`). Of the 245 runs from `v1` on, three failed a gate: a
`v2` run that wrote its plan as the reply, a `v12` run whose plan reached the
file through the finishing pass's reply rather than the tool, and the `v13` run
with no plan.

### The original failing conditions (R1)

The prompt of session `d9292a3d`, run three times per build against an export of
website commit `71c04d7e`, the code the session saw:

| Build | Pass 1 spent its whole read budget with no plan | Gates passed | Evaluator calls | Rounds | Wall s |
| --- | --- | --- | --- | --- | --- |
| `v0` | 3 of 3 | 9/12 (no title, every run) | 3 | 13–14 | 129–223 |
| `v6` | 2 of 3 | 12/12 | 2 | 12–13 | 120–260 |
| `v7` | 1 of 3 | 12/12 | 1 | 12–14 | 129–304 |
| `v8` | 0 of 3 | 12/12 | 0 | 12 | 109–182 |
| `v9` | 0 of 3 | 12/12 | 0 | 12 | 182–264 |
| `v10` | 2 of 3 | 12/12 | 2 | 12–14 | 161–341 |
| `v11` | 0 of 3 | 12/12 | 0 | 12 | 122–162 |
| `v13` | 0 of 3 | 12/12 | 0 | 10–13 | 140–341 |
| `v14` | 1 of 3 | 12/12 | 1 | 13 | 251–278 |
| `v15` | 0 of 6 | 24/24 | 1 | 13–23 | 127–405 |
| `v16` | 0 of 6 | 24/24 | 0 | 13–14 | 134–537 |

`v6` added the nudges and the lint; `v7` narrowed the last round of every pass to
the finishing tools; `v8` stopped the lint from sending a plan back on that last
round (in `v7` a plan sent back on round 12 of 12 used up the round and needed a
second pass). With `v8` the plan arrives in the first pass every time. With `v13` and `v14`, which let a last-round rejection use one more round, 5 of 6 runs wrote the plan in the first pass; the sixth (`v14`) spent the read budget and the finishing pass wrote the plan the same turn.

### By run

| Rev | Task | Gates | Q1 | Q2 | Q3 | Q4 | Q5 | Q6 | Quality | Score | Wall s | Rounds | Calls | Tokens | Lint | Judge's main flaw |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| v0 | S1 | 3/4 | 1 | 2 | 0 | 1 | 1 | 1 | 6 | 0 | 214.8 | 15 | 33 | 595032 | 0 | bare steps with prose Verifies, unmarked new paths, README or .repo hedge, garbled trailing text detached from steps |
| v1 | S1 | 4/4 | 2 | 2 | 1 | 2 | 1 | 3 | 11 | 11 | 155.6 | 12 | 30 | 400307 | 0 | counters in Postgres rather than DO or D1, Prisma schema location unspecified, hedged/placeholder Verifies, hedged integration test setup |
| v2 | S1 | 4/4 | 3 | 3 | 1 | 3 | 3 | 3 | 16 | 16 | 120.3 | 9 | 25 | 322653 | 0 | steps 2-3 verify test created in step 6, step 3 uses binding added in step 4, docs Verify none |
| v3 | S1 | 4/4 | 3 | 3 | 1 | 3 | 3 | 2 | 15 | 15 | 192.2 | 12 | 31 | 965909 | 0 | Verifies lack cd sandbox-worker, docs Verify none, env var read in DO not typed, test plan thin |
| v3b | S1 | 4/4 | 3 | 3 | 1 | 3 | 3 | 2 | 15 | 15 | 160.2 | 9 | 26 | 469167 | 0 | step 3 uses RATE_LIMITER before step 4 adds it, docs Verify none though grep works, DO fetch test not scheduled |
| v4 | S1 | 4/4 | 3 | 3 | 1 | 2 | 1 | 2 | 12 | 12 | 110.0 | 10 | 34 | 447647 | 1 | -w form invalid (no workspaces), steps 2-3 verify tests created later, hedged paths and test file, musing in assumptions, no command in test plan |
| v5 | S1 | 4/4 | 3 | 3 | 1 | 0 | 1 | 3 | 11 | 11 | 141.3 | 12 | 29 | 516827 | 0 | every Files list is split into garbled fragments, wrong npm flag form, verifies need later tests, hedged key (sub or principalUuid) |
| v5b | S1 | 4/4 | 3 | 3 | 1 | 2 | 2 | 2 | 13 | 13 | 364.4 | 9 | 22 | 329542 | 0 | verify commands lack cd sandbox-worker, step 1 verify needs later test and duplicates step 2, index.test.ts not in any Files |
| v6 | S1 | 4/4 | 2 | 3 | 1 | 2 | 1 | 2 | 11 | 11 | 251.2 | 9 | 26 | 415561 | 0 | env.ts hedged and not in Files, step 1 verify needs step 2 test, chained wrangler types verify, webhook synthetic subject contradiction, DO test unscheduled |
| v6b | S1 | 4/4 | 3 | 3 | 1 | 1 | 1 | 2 | 11 | 11 | 90.6 | 9 | 26 | 455476 | 0 | step 1 verify needs step 6 test, && chain, key is tenant+group not subject, extra penalty/token-bucket scope, rate-limit-do test not in any Files |
| v7 | S1 | 4/4 | 2 | 2 | 1 | 1 | 1 | 2 | 9 | 9 | 80.6 | 12 | 25 | 432313 | 0 | summary says nothing in src changes, garbled Files in step 3, Postgres db.ts used for D1, no migration, verifies need later tests |
| v7b | S1 | 4/4 | 3 | 2 | 1 | 2 | 1 | 2 | 11 | 11 | 115.4 | 12 | 34 | 483095 | 0 | step 3 verify needs the test file created in step 4; counter is a fixed window not sliding; hedged test double and conflicting group lists |
| v8 | S1 | 4/4 | 3 | 3 | 1 | 2 | 1 | 3 | 13 | 13 | 104.6 | 12 | 29 | 467473 | 0 | wrong grep string, two && chains, step 4 verify unrelated tests, fail-open vs fail-until-recovers contradiction |
| v8b | S1 | 4/4 | 3 | 3 | 1 | 3 | 3 | 3 | 16 | 16 | 190.3 | 12 | 29 | 566145 | 0 | step 1 verify runs test created in step 5, && chain in step 4 |
| v9 | S1 | 4/4 | 3 | 3 | 1 | 2 | 2 | 3 | 14 | 14 | 138.6 | 10 | 31 | 418428 | 0 | steps 1 and 2 duplicate the same new file, step 3 verify unrelated auth test, group exemption muddle |
| v9b | S1 | 4/4 | 3 | 3 | 1 | 3 | 3 | 3 | 16 | 16 | 179.2 | 9 | 18 | 512462 | 0 | DO class in the tested module, step 4 verify runs unrelated auth test |
| v9c | S1 | 4/4 | 3 | 3 | 1 | 1 | 1 | 3 | 12 | 12 | 87.1 | 10 | 28 | 497595 | 0 | step 2 contains abandoned approaches and 'simpler final approach' narration, && chain, DO migration hedged (renamed_classes or no-op) |
| v9d | S1 | 4/4 | 2 | 2 | 1 | 2 | 2 | 3 | 12 | 12 | 82.4 | 12 | 30 | 510535 | 0 | sandbox.ts DO handling never scheduled, config deferred to later, step 3 verify needs step 4 test, group list muddled |
| v9e | S1 | 4/4 | 3 | 3 | 1 | 1 | 1 | 3 | 12 | 12 | 141.0 | 12 | 25 | 640202 | 0 | step 3 contains leaked self-correcting narration, Env lacks the binding it uses, DO class tested via module importing cloudflare:workers |
| v9f | S1 | 4/4 | 3 | 3 | 1 | 2 | 2 | 3 | 14 | 14 | 124.8 | 12 | 35 | 756253 | 0 | steps 1, 2 and 4 verify tests created in later steps, && chain, noise about auth.test.ts, key mismatch |
| v9g | S1 | 4/4 | 3 | 2 | 1 | 2 | 1 | 3 | 12 | 12 | 85.8 | 12 | 28 | 500455 | 0 | KV rather than DO/D1 contradicts summary, step 1 verify needs step 2 test, step 3 uses KV binding before step 4, && chain, placeholder id, false claims |
| v10 | S1 | 4/4 | 3 | 3 | 1 | 2 | 1 | 3 | 13 | 13 | 148.4 | 12 | 25 | 570042 | 0 | step 3 verify needs step 4 test, && chain in step 6, contradictory config (vars vs hard-coded) and public-route grouping, stray .repo text |
| v10b | S1 | 4/4 | 2 | 2 | 1 | 2 | 1 | 3 | 11 | 11 | 148.8 | 12 | 24 | 609552 | 1 | README.md (if it exists) is a nonexistent unmarked path, fixed-minute buckets, step 1 imports a class from step 2, several optional/hedged decisions |
| v10c | S1 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 209.1 | 11 | 32 | 580950 | 1 | clean |
| v10d | S1 | 4/4 | 2 | 3 | 1 | 2 | 1 | 3 | 12 | 12 | 182.1 | 12 | 28 | 557549 | 1 | needs auth.ts change to expose sub but never names it, anonymous bucket contradicts after-auth keying, tests import DO module and index.ts |
| v10e | S1 | 4/4 | 3 | 3 | 1 | 3 | 1 | 3 | 14 | 14 | 144.6 | 12 | 23 | 667196 | 0 | two && chained Verifies, placeholder D1 id left to implementer, webhook group in defaults contradicts exemption |
| v11 | S1 | 4/4 | 3 | 3 | 1 | 3 | 3 | 3 | 16 | 16 | 158.8 | 12 | 34 | 535890 | 0 | DO class is in the module the unit test imports (cloudflare:workers), && chain in docs verify |
| v11b | S1 | 4/4 | 3 | 3 | 1 | 2 | 1 | 2 | 12 | 12 | 128.3 | 12 | 28 | 469396 | 0 | ??? placeholder in step 2, step 4 verify needs step 5 test, && chain, vague/non-test test-plan entries |
| v11c | S1 | 4/4 | 1 | 2 | 1 | 3 | 2 | 3 | 12 | 12 | 98.3 | 12 | 32 | 558120 | 0 | sandbox.ts and env.ts needed but not named, DO counter code unscheduled, step 2 verify needs step 6 test, dry-run piped verify |
| v11d | S1 | 4/4 | 3 | 3 | 1 | 2 | 1 | 2 | 12 | 12 | 178.0 | 18 | 45 | 902293 | 1 | Postgres sql helpers in db.ts mixed with a D1 binding, placeholder database_id, tests importing index.ts, verify scoped to an unrelated test |
| v11e | S1 | 4/4 | 2 | 3 | 1 | 2 | 1 | 3 | 12 | 12 | 200.9 | 12 | 24 | 526878 | 0 | env.ts not scheduled, PIX_SANDBOX_DO vs new PixRateLimiter binding contradiction, verifies need later tests, chain with || true, leaked instruction |
| v12 | S1 | 4/4 | 2 | 2 | 1 | 2 | 1 | 3 | 11 | 11 | 130.7 | 12 | 27 | 455753 | 1 | DO storage method in sandbox.ts is never scheduled; step 2 contains ??? and uses env before step 3 |
| v12b | S1 | 4/4 | 3 | 3 | 1 | 2 | 1 | 3 | 13 | 13 | 88.8 | 12 | 28 | 489242 | 0 | verifies run nonexistent/later tests, risks leave DO choice and migration undecided, webhook exempt vs own group |
| v12c | S1 | 4/4 | 3 | 2 | 1 | 1 | 1 | 2 | 10 | 10 | 112.0 | 12 | 28 | 561279 | 1 | ten fragmented steps, fixed buckets, nonexistent wrangler config verify, tests deferred to other steps, public routes limited before auth |
| v12d | S1 | 4/4 | 3 | 3 | 2 | 2 | 2 | 3 | 15 | 15 | 134.7 | 10 | 22 | 466124 | 1 | step 3 verify is a weak grep, optional extra schema step, /v1/admin path and product group contradict the stated scope |
| v12e | S1 | 4/4 | 2 | 2 | 2 | 3 | 2 | 2 | 13 | 13 | 139.6 | 12 | 36 | 431483 | 0 | no D1 table schema or migration scheduled, step 4 verify runs unrelated auth test, in-memory fallback and operator-set database_id, index cases unscheduled |
| v13 | S1 | 4/4 | 2 | 3 | 1 | 3 | 3 | 3 | 15 | 15 | 160.2 | 11 | 30 | 548282 | 1 | existing index.ts marked (new), test file name mismatch rate-limits vs rate-limit, worker flow test imports index.ts |
| v13b | S1 | 4/4 | 3 | 3 | 2 | 2 | 3 | 2 | 15 | 15 | 156.7 | 11 | 32 | 601802 | 1 | do and index tests in the test plan are not in any Files, wrangler dry-run verify, trivial separate export step |
| v13c | S1 | 4/4 | 3 | 3 | 1 | 3 | 3 | 2 | 15 | 15 | 244.7 | 11 | 31 | 741069 | 1 | env.ts imports a class created in step 3, step 4 verify does not exercise index.ts, rateLimitsFromEnv case not scheduled |
| v13d | S1 | 4/4 | 3 | 3 | 1 | 2 | 2 | 2 | 13 | 13 | 191.2 | 13 | 29 | 658923 | 1 | step 1 test needs worker fetch code from step 3, step 3 uses env bindings added in step 4; JSON-override hedge; DO case not scheduled |
| v13e | S1 | 4/4 | 2 | 2 | 1 | 2 | 3 | 3 | 13 | 13 | 121.8 | 10 | 25 | 462480 | 1 | Worker cannot touch DO storage directly and no sandbox.ts method is scheduled, groups are by role not route, steps 3-4 verify unrelated auth test |
| v14 | S1 | 4/4 | 2 | 2 | 1 | 2 | 1 | 3 | 11 | 11 | 245.8 | 13 | 33 | 506084 | 1 | no D1 migration scheduled, fixed window, garbled Files lines in step 6, sub vs principalUuid inconsistency, step 3 verify unrelated |
| v14b | S1 | 4/4 | 3 | 3 | 1 | 2 | 2 | 3 | 14 | 14 | 226.9 | 12 | 29 | 642826 | 1 | rate-limit check called with namespace but takes storage, step 4 and 6 verifies do not exercise change, integration test imports index.ts |
| v14c | S1 | 4/4 | 3 | 3 | 1 | 2 | 3 | 3 | 15 | 15 | 177.9 | 13 | 31 | 533397 | 1 | DO class lives in the module the step 1 test imports (cloudflare:workers fails in node); step 4 verify does not exercise index.ts |
| v14d | S1 | 4/4 | 3 | 3 | 1 | 2 | 3 | 3 | 15 | 15 | 272.1 | 13 | 21 | 460947 | 1 | step 2 env.ts imports a class created in step 3, test imports DO module, step 6 is a padding full-suite step |
| v14e | S1 | 4/4 | 3 | 3 | 1 | 2 | 2 | 3 | 14 | 14 | 81.9 | 12 | 31 | 673716 | 0 | summary claims existing rate-limit DO namespace while assumptions create it, step 2 verify unrelated, tests import DO/index |
| v15 | S1 | 4/4 | 3 | 3 | 1 | 2 | 3 | 3 | 15 | 15 | 223.1 | 11 | 29 | 494569 | 1 | env vars used in step 5 are never added to Env, tests import a module that pulls in cloudflare:workers, telemetry step is padding |
| v15b | S1 | 4/4 | 3 | 2 | 1 | 1 | 1 | 3 | 11 | 11 | 126.0 | 13 | 31 | 520405 | 1 | Worker has no path to DO storage so persistence is unwired, padding step 3, fixed-window strategy, && chain, webhook group contradicts exemption |
| v15c | S1 | 4/4 | 1 | 3 | 1 | 3 | 1 | 2 | 11 | 11 | 244.9 | 26 | 55 | 887873 | 1 | D1 table migration file and Env binding not scheduled, D1 vs Hyperdrive contradiction, mutating wrangler command as Verify, docs path with or |
| v15d | S1 | 4/4 | 3 | 3 | 2 | 3 | 3 | 3 | 17 | 17 | 206.3 | 11 | 31 | 606591 | 1 | step 4 verify runs the whole suite and never typechecks or exercises the index.ts change |
| v15e | S1 | 4/4 | 3 | 3 | 2 | 3 | 3 | 3 | 17 | 17 | 135.3 | 10 | 25 | 463351 | 1 | DO class lives in the module the unit test imports (cloudflare:workers) |
| v16 | S1 | 4/4 | 3 | 3 | 1 | 2 | 2 | 3 | 14 | 14 | 204.5 | 12 | 39 | 574717 | 1 | padding full-suite step with && chain, step 2 verify unrelated metrics test, inconsistent config source, README docs |
| v16b | S1 | 4/4 | 2 | 2 | 1 | 3 | 2 | 3 | 13 | 13 | 221.8 | 14 | 26 | 751333 | 1 | DO handler in sandbox.ts is never scheduled, step 2 verify does not exercise index.ts, image/README.md conditional |
| v16c | S1 | 4/4 | 3 | 3 | 2 | 3 | 3 | 3 | 17 | 17 | 256.0 | 10 | 23 | 516108 | 1 | DO class lives in the module the unit test imports (cloudflare:workers) |
| v16d | S1 | 4/4 | 3 | 2 | 2 | 3 | 3 | 3 | 16 | 16 | 144.2 | 14 | 28 | 436071 | 1 | configuration vars claimed but never scheduled in Env/wrangler, DO class inside the unit-tested module |
| v16e | S1 | 4/4 | 3 | 3 | 1 | 3 | 3 | 3 | 16 | 16 | 148.7 | 13 | 30 | 593665 | 1 | step 2 verify does not exercise sandbox.ts, step 3 uses env vars added in step 4, index test cannot import index.ts |
| v0 | S2 | 3/4 | 2 | 3 | 1 | 1 | 1 | 2 | 10 | 0 | 120.9 | 12 | 26 | 828351 | 0 | step bodies are detached trailing bullets with no Files, hedged 'if it exists' test and doc steps, Verify none for docs |
| v1 | S2 | 4/4 | 2 | 3 | 1 | 2 | 2 | 2 | 12 | 12 | 136.4 | 12 | 30 | 1045152 | 0 | verify uses nonexistent root typecheck script and chains two commands; stalled-launch recovery is not in maintenance-cron.ts; Activity component named with an 'or' |
| v2 | S2 | 4/4 | 1 | 3 | 1 | 2 | 1 | 2 | 10 | 10 | 83.0 | 12 | 17 | 329115 | 0 | unmarked nonexistent doc dir and index.test.ts, many 'or' Verify alternatives, a Verify of none, and test files named only in Verify |
| v3 | S2 | 4/4 | 1 | 3 | 1 | 2 | 1 | 2 | 10 | 10 | 121.5 | 10 | 23 | 512734 | 0 | nonexistent BelaiSandboxCard.test.ts, doc step hedged and Verify none, Worker contract assumed, test files for steps not in Files |
| v3b | S2 | 4/4 | 1 | 3 | 1 | 2 | 1 | 2 | 10 | 10 | 171.0 | 12 | 29 | 1136662 | 0 | existing launch.ts and sandbox.ts marked new, '(assumed)' files, wrong root-relative worker test commands, a .vue run as a test, a doc step hedged with Verify none |
| v4 | S2 | 4/4 | 3 | 3 | 1 | 2 | 2 | 2 | 13 | 13 | 202.5 | 14 | 25 | 869151 | 1 | worker verify commands use wrong root-relative vitest path, root lint/test do not exercise worker steps, invents a state union that contradicts real states; docs dir hedged |
| v5 | S2 | 4/4 | 3 | 3 | 1 | 2 | 3 | 2 | 14 | 14 | 186.2 | 12 | 24 | 855791 | 0 | worker verify commands use wrong root-relative vitest path, admin page verified by an unrelated route test, duplicates existing follow stream with an EventSource |
| v5b | S2 | 4/4 | 3 | 3 | 1 | 3 | 3 | 2 | 15 | 15 | 256.0 | 9 | 18 | 594947 | 0 | worker tests run as npx vitest run sandbox-worker/test/... which the root config cannot find, and a root npm run build does not cover worker code |
| v6 | S2 | 4/4 | 3 | 3 | 2 | 2 | 3 | 3 | 16 | 16 | 98.7 | 12 | 20 | 518888 | 0 | step 4 is a padding lint and build step with a chained Verify |
| v6b | S2 | 4/4 | 2 | 3 | 1 | 2 | 3 | 2 | 13 | 13 | 191.4 | 14 | 27 | 1009997 | 1 | step 4 verify targets nonexistent __tests__/BelaiSandboxLaunch.spec.ts; useSandboxWorker.test.ts marked new but exists; component test not scheduled |
| v7 | S2 | 4/4 | 2 | 3 | 1 | 1 | 1 | 2 | 10 | 10 | 174.7 | 14 | 25 | 915361 | 0 | step 1 Files lines are garbled fragments, doc file 'new if missing otherwise append', conditional AGENTS.md and logger module, tests not scheduled in Files |
| v7b | S2 | 4/4 | 1 | 3 | 2 | 2 | 2 | 3 | 13 | 13 | 263.6 | 12 | 29 | 800946 | 1 | two existing test files marked new (would overwrite), buy-dialog logging step is off-topic and build-verified, docs dir hedged |
| v8 | S2 | 4/4 | 2 | 2 | 1 | 1 | 1 | 2 | 9 | 9 | 168.3 | 12 | 24 | 923740 | 0 | targets the checkout flow not launch, step 1 Files list garbled, several 'if test exists' Verify hedges, nonexistent buy-dialog test |
| v8b | S2 | 4/4 | 2 | 2 | 1 | 1 | 1 | 2 | 9 | 9 | 258.7 | 14 | 28 | 994432 | 0 | step 1 Files garbled, sandbox-worker has no npm run build, bare step bodies, wrongly says useSandboxWorker has no test, step 7 analytics composable does not exist |
| v9 | S2 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 169.6 | 12 | 30 | 1122239 | 1 | clean |
| v9b | S2 | 4/4 | 3 | 3 | 2 | 2 | 1 | 2 | 13 | 13 | 199.8 | 12 | 31 | 902503 | 0 | step 5 contains leaked self-correcting narration and a hacky UUID fallback; step 1 verify runs a non-test .ts file with an 'or'; test files for steps 1 not scheduled |
| v9c | S2 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 267.5 | 12 | 21 | 922279 | 1 | clean |
| v9d | S2 | 4/4 | 2 | 3 | 1 | 2 | 2 | 2 | 12 | 12 | 134.3 | 12 | 35 | 644346 | 0 | foreign-script garble in the summary, launch.ts missing from Files, step 6 chains with &&, launch tests not scheduled, re-adds an existing failed-state guard |
| v9e | S2 | 4/4 | 3 | 3 | 1 | 3 | 3 | 2 | 15 | 15 | 107.8 | 10 | 28 | 721399 | 0 | sandbox.ts steps verified by launch.test.ts which cannot run them, step 6 chains with &&, launch.test.ts cases not in any step Files |
| v9f | S2 | 4/4 | 2 | 3 | 1 | 2 | 1 | 2 | 11 | 11 | 86.7 | 12 | 25 | 367885 | 0 | invents sandbox-worker/migrations, doc target hedged, step 6 chains with &&, sandbox.ts steps verified by unrelated tests, npm run lint does not exist in the worker |
| v9g | S2 | 4/4 | 3 | 3 | 1 | 2 | 2 | 2 | 13 | 13 | 140.4 | 12 | 28 | 1013431 | 0 | steps 1, 2, 3, 7 verified by unrelated or narrow worker tests, extension retry is scope creep, test cases not in step Files, step 6 SSE source vague |
| v10 | S2 | 4/4 | 3 | 3 | 1 | 2 | 2 | 2 | 13 | 13 | 110.3 | 12 | 17 | 550227 | 0 | step 4 verified by maintenance-cron test; step 6 hedges on VxNotice and its UI test is not in Files; dual resolveConfig API is heavy |
| v10b | S2 | 4/4 | 3 | 3 | 1 | 3 | 2 | 2 | 14 | 14 | 170.4 | 11 | 27 | 881552 | 0 | sandbox.ts steps verified by launch.test.ts or a file created twice as new; progress UI step verified by one test of three files; test cases not in Files |
| v10c | S2 | 4/4 | 2 | 3 | 2 | 2 | 2 | 3 | 14 | 14 | 142.5 | 12 | 32 | 723246 | 1 | step 4 calls a nonexistent worker.rawLog and hedges to a UI placeholder verified only by build; start-model and bare-name steps drift off the launch flow |
| v10d | S2 | 4/4 | 3 | 3 | 1 | 1 | 2 | 2 | 12 | 12 | 102.0 | 12 | 29 | 781182 | 0 | 11 steps with several unrelated verifies (rc-health, container-watch for sandbox.ts changes), doc 'create if absent'; bloated for the request |
| v10e | S2 | 4/4 | 3 | 3 | 1 | 1 | 1 | 3 | 12 | 12 | 108.4 | 12 | 25 | 589086 | 0 | steps 1 and 7 verify tests created by later steps, unused Deploy wrapper and optional component are padding, doc 'or nearest existing doc' hedge and polling fallback assumption |
| v11 | S2 | 4/4 | 2 | 3 | 1 | 3 | 1 | 2 | 12 | 12 | 122.0 | 12 | 26 | 726438 | 0 | nonexistent admin-pix-sandboxes.test.ts, 'if file exists otherwise' Verify hedges in steps 1 and 4, docs dir hedged, test cases not in Files |
| v11b | S2 | 4/4 | 2 | 3 | 3 | 2 | 3 | 3 | 16 | 16 | 178.6 | 12 | 23 | 585403 | 1 | useBelaiStartModel.test.ts marked new but exists; New-session polling and start-model steps are tangential |
| v11c | S2 | 4/4 | 3 | 3 | 2 | 3 | 3 | 2 | 16 | 16 | 154.9 | 12 | 24 | 728354 | 1 | card step verified by build only; BelaiSandboxLaunch and BelaiSandboxes tests in the test plan are not in any step Files |
| v11d | S2 | 4/4 | 3 | 3 | 1 | 3 | 2 | 2 | 14 | 14 | 98.0 | 12 | 20 | 628293 | 0 | sandbox.ts steps verified by launch.test.ts which cannot exercise them; admin page verified by build; test file choice hedged |
| v11e | S2 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 104.4 | 12 | 20 | 585999 | 0 | clean |
| v12 | S2 | 4/4 | 2 | 3 | 1 | 3 | 2 | 3 | 14 | 14 | 118.4 | 12 | 23 | 523892 | 0 | adds launch.failed to UserEventKind but events.ts not in Files; sandbox.ts step verified by launch.test.ts; step 5 files hedged with 'or any test' |
| v12b | S2 | 4/4 | 3 | 3 | 1 | 2 | 1 | 2 | 12 | 12 | 263.1 | 13 | 29 | 981337 | 0 | step bodies are bare titles, step 6 chains with &&, doc directory left to be chosen, and sandbox-check retry targets a pure parser |
| v12c | S2 | 3/4 | 3 | 3 | 1 | 3 | 3 | 2 | 15 | 0 | 135.4 | 13 | 25 | 854527 | 0 | step bodies are bare titles; steps 1-2 verified by an unrelated component test; build-only verifies |
| v12d | S2 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 784.8 | 12 | 29 | 1089968 | 0 | clean |
| v12e | S2 | 4/4 | 3 | 3 | 2 | 2 | 3 | 3 | 16 | 16 | 179.7 | 12 | 28 | 1003519 | 0 | sandbox.ts instrumentation verified by launch.test.ts which cannot exercise it; step 6 is a padding full-suite step |
| v13 | S2 | 4/4 | 2 | 3 | 2 | 3 | 2 | 2 | 14 | 14 | 229.1 | 13 | 23 | 546516 | 0 | parent handling of the new relaunch payload is unnamed; docs dir hedged against (new); useSandboxWorker test case not scheduled |
| v13b | S2 | 4/4 | 3 | 3 | 2 | 3 | 2 | 3 | 16 | 16 | 303.2 | 14 | 25 | 782176 | 0 | invented composable does not match the real Worker-side launch; step 3 build-only; assumptions hedge on cancellation and the admin page host |
| v13c | S2 | 4/4 | 3 | 3 | 1 | 2 | 2 | 2 | 13 | 13 | 137.3 | 13 | 23 | 817746 | 1 | steps 2-3 verified by unrelated tests, step 6 verified by a grep, schema migration deferred, activity lookup hedged, activity test not scheduled |
| v13d | S2 | 1/4 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 125.6 | 13 | 24 | 597467 | 1 | No plan was recorded: the plan the lint sent back was lost when the extra round was spent on a tool the finishing surface withholds (fixed in v14). |
| v13e | S2 | 4/4 | 3 | 3 | 3 | 2 | 3 | 3 | 17 | 17 | 242.8 | 13 | 20 | 654103 | 0 | lint-only step 3 is padding and saveAndRelaunch already blocks on save failure |
| v14 | S2 | 4/4 | 2 | 2 | 3 | 2 | 2 | 3 | 14 | 14 | 166.9 | 13 | 23 | 787845 | 1 | BelaiSandboxes.test.ts marked new but exists; relies on Worker returning diagnostics while declaring Worker out of scope |
| v14b | S2 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 397.1 | 13 | 23 | 870456 | 0 | clean |
| v14c | S2 | 4/4 | 3 | 3 | 1 | 2 | 2 | 3 | 14 | 14 | 113.1 | 13 | 23 | 602243 | 1 | step 3 verify runs a test only step 5 creates, steps 4 and 6 verified by unrelated tests, migration deferred and circular-import hedge |
| v14d | S2 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 170.8 | 9 | 26 | 421295 | 0 | clean |
| v14e | S2 | 4/4 | 3 | 3 | 1 | 2 | 3 | 3 | 15 | 15 | 384.7 | 13 | 29 | 1081356 | 0 | step 1 verify is a grep for the symbol and step 4 is a files-none build step |
| v15 | S2 | 4/4 | 3 | 3 | 2 | 3 | 2 | 3 | 16 | 16 | 206.6 | 13 | 22 | 511110 | 1 | sandbox.ts steps verified by launch.test.ts which cannot exercise them; docs step skipped if dir absent contradicts (new) |
| v15b | S2 | 4/4 | 2 | 3 | 3 | 3 | 2 | 3 | 16 | 16 | 307.5 | 13 | 24 | 872167 | 1 | useBelaiSandboxes.test.ts marked new but exists (wrongly assumed absent); docs dir and BelaiNotice-or-paragraph hedged |
| v15c | S2 | 4/4 | 3 | 3 | 1 | 3 | 3 | 2 | 15 | 15 | 236.4 | 12 | 22 | 756195 | 1 | three console steps verified by npm run build only and the UI test case is a build, with no test file scheduled |
| v15d | S2 | 4/4 | 3 | 3 | 1 | 2 | 1 | 2 | 12 | 12 | 149.0 | 13 | 19 | 536484 | 1 | steps 4-5 verified by lint only, 'prop or emit' hedge, keeps @relaunch contract unchanged while changing when it is emitted, BelaiSandboxLaunch test not scheduled |
| v15e | S2 | 4/4 | 3 | 3 | 2 | 2 | 2 | 3 | 15 | 15 | 126.1 | 13 | 20 | 892947 | 1 | sandbox.ts steps verified by launch.test.ts or container-watch test; rc-health change is tangential; doc step hedges on linking from worker docs |
| v16 | S2 | 4/4 | 3 | 3 | 1 | 3 | 3 | 2 | 15 | 15 | 146.4 | 13 | 33 | 612411 | 0 | bare-title steps, sandbox.ts steps all verified by launch.test.ts, ensure and runLaunch cases not in step Files |
| v16b | S2 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 189.5 | 12 | 30 | 678212 | 0 | test plan lists cases but gives no command; commands only in step Verify |
| v16c | S2 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 131.4 | 12 | 25 | 698629 | 0 | clean |
| v16d | S2 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 327.7 | 14 | 31 | 1278406 | 0 | clean |
| v16e | S2 | 4/4 | 3 | 3 | 3 | 3 | 2 | 2 | 16 | 16 | 157.4 | 13 | 33 | 1033580 | 0 | relies on a nonexistent logSnapshot column with migration deferred; launch.test.ts cases not in step Files |
| v0 | T1 | 3/4 | 3 | 3 | 1 | 1 | 3 | 2 | 13 | 0 | 52 | 6 | 9 | 21979 | 0 | no title, steps lack Files and use non-command Verifies, step details detached at end after Assumptions |
| v1 | T1 | 4/4 | 3 | 3 | 2 | 1 | 3 | 3 | 15 | 15 | 52.9 | 5 | 9 | 21364 | 0 | README Verify none; escaped backticks; Risk says no risk |
| v2 | T1 | 2/4 | 3 | 3 | 3 | 2 | 2 | 3 | 16 | 0 | 53.9 | 3 | 5 | 14689 | 0 | leaked narration at end; null assumption contradicts *string decoding |
| v3 | T1 | 4/4 | 3 | 3 | 1 | 3 | 2 | 3 | 15 | 15 | 23.2 | 4 | 9 | 17700 | 0 | README Verify none; step 5 hedges handler tests or extend existing |
| v3b | T1 | 4/4 | 3 | 3 | 1 | 1 | 3 | 2 | 13 | 13 | 27.4 | 3 | 6 | 12794 | 0 | step 1 calls Patch before step 2 adds it; README Verify none; unbalanced bold/backticks and filler Risk; tests not scheduled in any Files |
| v4 | T1 | 4/4 | 3 | 3 | 3 | 2 | 3 | 3 | 17 | 17 | 35.8 | 5 | 10 | 29937 | 0 | escaped backticks in README bullet |
| v5 | T1 | 4/4 | 3 | 3 | 3 | 2 | 3 | 3 | 17 | 17 | 107.3 | 7 | 13 | 43978 | 0 | escaped backticks around struct tags |
| v5b | T1 | 4/4 | 3 | 3 | 1 | 2 | 1 | 3 | 13 | 13 | 55.0 | 5 | 10 | 26019 | 0 | step 2 and step 3 hedge with or alternatives, Risk defers null handling; garbled README line |
| v6 | T1 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 19.2 | 3 | 6 | 15544 | 0 | integration smoke case not scheduled in any step Files |
| v6b | T1 | 4/4 | 3 | 3 | 3 | 2 | 3 | 3 | 17 | 17 | 18.6 | 3 | 7 | 15737 | 0 | escaped backticks garble struct tag |
| v7 | T1 | 4/4 | 3 | 3 | 3 | 2 | 3 | 3 | 17 | 17 | 75.3 | 3 | 5 | 18254 | 0 | escaped backticks and unbalanced markup in struct tag and README bullet |
| v7b | T1 | 4/4 | 3 | 3 | 3 | 3 | 2 | 2 | 16 | 16 | 42.7 | 8 | 13 | 47093 | 0 | integration test case hedged and not scheduled in any step |
| v8 | T1 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 88.5 | 3 | 6 | 17866 | 0 | clean |
| v8b | T1 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 24.8 | 4 | 7 | 22181 | 1 | clean; odd sentinel design and store test asserting 404 |
| v9 | T1 | 4/4 | 3 | 3 | 3 | 2 | 3 | 3 | 17 | 17 | 36.9 | 6 | 10 | 32509 | 1 | escaped backticks garble struct tag and README snippet |
| v9b | T1 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 54.6 | 5 | 9 | 28114 | 0 | clean |
| v10 | T1 | 4/4 | 3 | 3 | 1 | 1 | 3 | 3 | 14 | 14 | 28.6 | 3 | 8 | 14481 | 0 | steps 1 and 2 verify tests created only in later steps; bogus rename step; garbled Risk word |
| v10b | T1 | 4/4 | 3 | 3 | 2 | 3 | 2 | 2 | 15 | 15 | 23.5 | 5 | 9 | 25796 | 0 | step 3 hedges extend TestCRUD or new TestPatch, lists unchanged main.go, verify cannot show reachability |
| v11 | T1 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 16.9 | 3 | 8 | 13732 | 0 | clean; terse steps |
| v11b | T1 | 4/4 | 3 | 3 | 3 | 3 | 2 | 2 | 16 | 16 | 33.9 | 5 | 9 | 27298 | 0 | Risk contradicts omitted-field semantics; TestPatchHTTP not scheduled in any Files |
| v12 | T1 | 4/4 | 3 | 3 | 3 | 3 | 2 | 3 | 17 | 17 | 59.2 | 5 | 9 | 27104 | 1 | hedge on detecting omitted fields (pointer or zero-value) and passes &in.Title which is never nil |
| v12b | T1 | 4/4 | 3 | 3 | 2 | 2 | 2 | 3 | 15 | 15 | 57.9 | 6 | 9 | 33662 | 1 | step 4 hedges httptest.NewServer or direct handler calls; escaped backticks |
| v13 | T1 | 4/4 | 3 | 3 | 3 | 2 | 3 | 3 | 17 | 17 | 26.6 | 8 | 8 | 39965 | 0 | false claim that project has no documented API surface, README skipped |
| v13b | T1 | 4/4 | 3 | 3 | 3 | 2 | 3 | 3 | 17 | 17 | 48.5 | 6 | 10 | 33780 | 0 | escaped quotes in handler test step |
| v14 | T1 | 4/4 | 3 | 3 | 2 | 2 | 3 | 2 | 15 | 15 | 29.5 | 4 | 8 | 20558 | 0 | step 2 Verify chains go run . (blocks) and go test; escaped backticks; handler test not scheduled in any Files |
| v14b | T1 | 4/4 | 3 | 3 | 3 | 3 | 2 | 2 | 16 | 16 | 22.8 | 5 | 9 | 24791 | 0 | assumption hedges omitempty or pointer; HTTP test hedged and not scheduled in any Files |
| v16 | T1 | 4/4 | 3 | 3 | 3 | 2 | 3 | 3 | 17 | 17 | 27.0 | 4 | 8 | 19721 | 0 | escaped backticks in steps 3 and 4 |
| v16b | T1 | 4/4 | 3 | 3 | 2 | 2 | 2 | 2 | 14 | 14 | 153.0 | 5 | 12 | 25903 | 0 | step 3 hedges on invented patchStoreUpdate or inline; escaped backticks; TestHTTPPatch not scheduled in any Files |
| v0 | T2 | 3/4 | 3 | 3 | 1 | 2 | 3 | 2 | 14 | 0 | 26.2 | 3 | 7 | 9417 | 0 | Step bodies are detached below Assumptions, steps carry no Files or real Verify, and test cases are vague |
| v1 | T2 | 4/4 | 3 | 3 | 1 | 2 | 3 | 3 | 15 | 15 | 43.6 | 7 | 9 | 28321 | 0 | Step 1 breaks app.py via tuple return, step 1 Verify is a doc-print check, step 2 Verify runs a test created in step 3; vague extra test-edit line |
| v2 | T2 | 4/4 | 3 | 3 | 1 | 2 | 1 | 3 | 13 | 13 | 22.9 | 3 | 7 | 12517 | 0 | Steps 1 and 2 verify tests not created until later (test_pagination_headers never exists), step 1 breaks app, 400-vs-clamp contradictions, garbled 2004 |
| v3 | T2 | 4/4 | 3 | 3 | 1 | 3 | 3 | 3 | 16 | 16 | 48.7 | 4 | 8 | 21067 | 0 | README step has Verify none (grep could check) and step 2 lumps tests into Files with no description |
| v3b | T2 | 4/4 | 3 | 3 | 2 | 3 | 3 | 3 | 17 | 17 | 35.0 | 3 | 6 | 13123 | 0 | Step 1 puts limit/offset before path so the existing caller breaks until step 2 |
| v4 | T2 | 4/4 | 3 | 3 | 1 | 3 | 3 | 3 | 16 | 16 | 23.0 | 4 | 7 | 18794 | 0 | Step 1 returns a dict breaking the route until step 2, with import-only and non-asserting python -c Verifies |
| v5 | T2 | 4/4 | 3 | 3 | 3 | 2 | 2 | 3 | 16 | 16 | 26.3 | 5 | 10 | 24694 | 0 | Garbled 'listItems' name and 'if needed' hedge in README assumption |
| v5b | T2 | 4/4 | 3 | 3 | 1 | 3 | 2 | 3 | 15 | 15 | 92.9 | 6 | 11 | 33025 | 0 | Import-only Verify in step 1 plus 'or' hedges in steps 2 and 3 |
| v6 | T2 | 4/4 | 3 | 3 | 3 | 2 | 3 | 3 | 17 | 17 | 36.5 | 5 | 8 | 24211 | 0 | Garbled clamp assumption and false claim that int ValueError yields 400 |
| v6b | T2 | 4/4 | 3 | 3 | 1 | 3 | 2 | 3 | 15 | 15 | 23.2 | 5 | 13 | 25634 | 0 | Step 1 edits the route to use a model API not created until step 2, step 4 Verify chains with &&, 'available/wrapped' hedge |
| v7 | T2 | 4/4 | 3 | 3 | 2 | 2 | 2 | 3 | 15 | 15 | 26.9 | 6 | 10 | 28051 | 0 | Step 1 omits the count function, 'max(limit, 100)' contradicts the cap, summary describes nonexistent Python filtering |
| v7b | T2 | 4/4 | 3 | 3 | 2 | 2 | 3 | 3 | 16 | 16 | 24.3 | 4 | 8 | 19677 | 0 | Step 1 changes signature and return shape breaking app.py until step 2; step 3 contains a no-op 'change X to X' line |
| v8 | T2 | 4/4 | 3 | 3 | 1 | 3 | 2 | 3 | 15 | 15 | 45.6 | 4 | 11 | 20536 | 0 | Step 1 'Rename/extend' hedge and implied signature change breaks app.py until step 2 |
| v8b | T2 | 4/4 | 3 | 3 | 2 | 3 | 3 | 3 | 17 | 17 | 21.4 | 4 | 13 | 19682 | 0 | Step 1 reorders list_items parameters so the existing caller breaks until step 2 |
| v9 | T2 | 4/4 | 3 | 3 | 2 | 3 | 2 | 3 | 16 | 16 | 21.2 | 4 | 9 | 19763 | 0 | Step 1 breaks app.py until step 2; offset 'passed directly' contradicts 'clamped' and step 2 does not clamp it |
| v9b | T2 | 4/4 | 3 | 3 | 3 | 2 | 3 | 3 | 17 | 17 | 37.6 | 7 | 15 | 34792 | 0 | Summary misdescribes tests as 'in-memory' |
| v10 | T2 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 36.9 | 4 | 11 | 22899 | 0 | clean |
| v10b | T2 | 4/4 | 3 | 3 | 1 | 2 | 2 | 2 | 13 | 13 | 117.0 | 7 | 13 | 43704 | 1 | Step 1 breaks app.py until step 2, step 3 'or' hedge on the max-limit test, padded risks |
| v11 | T2 | 4/4 | 3 | 3 | 1 | 3 | 3 | 3 | 16 | 16 | 43.0 | 5 | 12 | 27175 | 1 | Step 1 changes list_items to return a tuple, breaking app.py until step 2, and its Verify is stated as runnable only after step 3 |
| v11b | T2 | 4/4 | 3 | 3 | 2 | 3 | 3 | 3 | 17 | 17 | 39.8 | 3 | 8 | 15367 | 0 | Step 1 Verify is an import-only check |
| v12 | T2 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 60.7 | 7 | 12 | 35835 | 1 | clean |
| v12b | T2 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 43.8 | 4 | 9 | 19875 | 0 | Test cases are loosely described (clamping and 400 cases not tied to a named test) |
| v13 | T2 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 45.6 | 4 | 9 | 21746 | 0 | clean |
| v13b | T2 | 4/4 | 3 | 3 | 2 | 2 | 3 | 3 | 16 | 16 | 28.5 | 7 | 14 | 36287 | 0 | Step 1 names no function or count helper; Risks says 'no risk identified' |
| v14 | T2 | 4/4 | 3 | 3 | 1 | 3 | 3 | 3 | 16 | 16 | 25.1 | 5 | 9 | 25895 | 0 | Step 1 replaces list_items without defining count_items, breaks the route, and has a hedged import-only Verify |
| v14b | T2 | 4/4 | 3 | 3 | 1 | 3 | 2 | 3 | 15 | 15 | 29.5 | 5 | 14 | 24666 | 0 | Step 1 breaks app.py until step 2 and step 2 hedges 'or clamp negative offset' against the 400 assumption |
| v16 | T2 | 4/4 | 3 | 3 | 3 | 2 | 2 | 3 | 16 | 16 | 20.0 | 3 | 7 | 14318 | 0 | Assumptions contradict (invalid offset returns 400 vs offset floored to 0) and a vacuous Risks entry |
| v16b | T2 | 4/4 | 3 | 3 | 2 | 2 | 2 | 3 | 15 | 15 | 94.2 | 4 | 9 | 19267 | 0 | Step 1 reorders list_items parameters breaking the caller; floor-at-1 vs fall-back-to-default contradiction; padded assumptions |
| v0 | T3 | 3/4 | 3 | 3 | 1 | 1 | 3 | 2 | 13 | 0 | 37.6 | 3 | 8 | 9494 | 0 | Steps are bare headings with bodies detached after Risks, padding build step, placeholder verifies, vague test plan |
| v1 | T3 | 4/4 | 3 | 3 | 3 | 1 | 3 | 3 | 16 | 16 | 44.3 | 5 | 11 | 20491 | 0 | Garbled test-plan line and 'Risks: None identified' |
| v2 | T3 | 4/4 | 3 | 3 | 3 | 2 | 3 | 3 | 17 | 17 | 29.4 | 9 | 14 | 36969 | 0 | Assumption about converting existing tests to beforeEach is not scheduled in any step |
| v3 | T3 | 4/4 | 3 | 3 | 3 | 2 | 3 | 3 | 17 | 17 | 25.2 | 5 | 11 | 22331 | 0 | Korean token in summary |
| v3b | T3 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 28.9 | 4 | 8 | 17224 | 0 | clean |
| v4 | T3 | 4/4 | 3 | 3 | 3 | 3 | 2 | 3 | 17 | 17 | 36.7 | 10 | 10 | 47076 | 0 | Assumption says all-whitespace invalid only if length is zero (self-contradictory) |
| v5 | T3 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 21.2 | 4 | 9 | 17830 | 0 | clean |
| v5b | T3 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 57.6 | 5 | 11 | 25745 | 0 | clean |
| v6 | T3 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 29.8 | 7 | 13 | 45093 | 0 | clean |
| v6b | T3 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 23.0 | 5 | 9 | 25180 | 0 | clean |
| v7 | T3 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 18.5 | 5 | 10 | 24633 | 0 | clean |
| v7b | T3 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 33.3 | 4 | 11 | 18873 | 0 | clean |
| v8 | T3 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 32.6 | 4 | 10 | 19155 | 0 | clean |
| v8b | T3 | 4/4 | 1 | 3 | 1 | 2 | 3 | 2 | 12 | 12 | 28.4 | 1 | 1 | 5170 | 0 | Targets nonexistent test/app.test.ts and taskService.createTask instead of test/tasks.test.ts |
| v9 | T3 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 17.9 | 4 | 9 | 19714 | 0 | clean |
| v9b | T3 | 4/4 | 3 | 3 | 3 | 3 | 2 | 3 | 17 | 17 | 18.8 | 4 | 8 | 19647 | 0 | Assumption says 0 to 100 characters, contradicting the 1 to 100 rule |
| v10 | T3 | 4/4 | 3 | 3 | 3 | 1 | 2 | 3 | 15 | 15 | 24.2 | 3 | 9 | 13612 | 0 | Irrelevant .js-extension risk, filler import-path sentence, and an 'if needed' hedge |
| v10b | T3 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 30.1 | 4 | 13 | 21387 | 0 | clean |
| v11 | T3 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 23.6 | 3 | 9 | 13840 | 0 | clean |
| v11b | T3 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 15.8 | 3 | 8 | 14100 | 0 | clean |
| v12 | T3 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 26.7 | 4 | 9 | 18155 | 0 | clean |
| v12b | T3 | 4/4 | 3 | 3 | 3 | 3 | 2 | 3 | 17 | 17 | 33.9 | 5 | 12 | 26844 | 0 | Step says trimmed length is checked but the assumption says length is checked before trimming |
| v13 | T3 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 24.5 | 7 | 10 | 38463 | 0 | clean |
| v13b | T3 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 26.5 | 4 | 11 | 20508 | 0 | clean |
| v14 | T3 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 36.3 | 5 | 9 | 29247 | 0 | clean |
| v14b | T3 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 20.8 | 4 | 7 | 21543 | 0 | clean |
| v16 | T3 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 18.1 | 5 | 9 | 23189 | 0 | clean |
| v16b | T3 | 4/4 | 3 | 3 | 3 | 3 | 2 | 3 | 17 | 17 | 22.9 | 5 | 11 | 27562 | 0 | Assumption says no trimming yet whitespace-only is invalid |
| v0 | T4 | 3/4 | 3 | 3 | 1 | 1 | 3 | 2 | 13 | 0 | 18.3 | 2 | 4 | 5957 | 0 | no title, steps lack Files, replacement details detached at end after Assumptions |
| v1 | T4 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 24.0 | 4 | 7 | 14639 | 0 | clean; existing suite only |
| v2 | T4 | 4/4 | 3 | 3 | 2 | 3 | 3 | 2 | 16 | 16 | 16.7 | 3 | 5 | 11172 | 0 | README step Verify none though grep could check |
| v3 | T4 | 4/4 | 3 | 3 | 2 | 3 | 3 | 2 | 16 | 16 | 24.5 | 4 | 7 | 16220 | 0 | README step Verify none though grep could check |
| v3b | T4 | 4/4 | 3 | 3 | 2 | 2 | 3 | 2 | 15 | 15 | 17.2 | 3 | 6 | 11775 | 0 | README Verify none; Risks contains leaked ticket-search narration |
| v4 | T4 | 4/4 | 3 | 3 | 3 | 2 | 3 | 2 | 16 | 16 | 27.4 | 4 | 7 | 18043 | 1 | step 1 lists unchanged store_test.go and run tests padding |
| v5 | T4 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 50.4 | 5 | 7 | 24230 | 0 | clean; existing suite only |
| v5b | T4 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 19.9 | 6 | 9 | 25614 | 0 | clean; existing suite only |
| v6 | T4 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 27.3 | 4 | 8 | 18520 | 0 | clean; no new tests, suite command named |
| v6b | T4 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 20.4 | 3 | 8 | 13549 | 0 | clean; existing suite only |
| v7 | T4 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 12.9 | 3 | 6 | 12693 | 0 | clean; existing suite only |
| v7b | T4 | 4/4 | 3 | 3 | 3 | 2 | 3 | 2 | 16 | 16 | 20.4 | 6 | 9 | 26182 | 0 | escaped backticks garble README line |
| v8 | T4 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 14.5 | 4 | 8 | 17770 | 0 | clean; existing suite only |
| v8b | T4 | 4/4 | 3 | 3 | 2 | 3 | 3 | 1 | 15 | 15 | 15.7 | 4 | 7 | 17555 | 0 | chained && Verify; Test Plan names no command |
| v9 | T4 | 4/4 | 3 | 3 | 3 | 2 | 3 | 2 | 16 | 16 | 87.7 | 4 | 9 | 18218 | 0 | escaped backticks garble README step; Test Plan only reruns existing suite |
| v9b | T4 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 24.1 | 4 | 6 | 18199 | 0 | clean; existing suite only |
| v10 | T4 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 21.4 | 4 | 6 | 18958 | 1 | clean; existing suite only |
| v10b | T4 | 4/4 | 3 | 3 | 3 | 2 | 3 | 2 | 16 | 16 | 15.7 | 4 | 6 | 18755 | 0 | garbled assumption sentence about env var or flag |
| v11 | T4 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 12.7 | 4 | 8 | 18585 | 0 | clean; existing suite only |
| v11b | T4 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 22.9 | 6 | 11 | 28230 | 0 | clean; existing suite only |
| v12 | T4 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 19.2 | 3 | 6 | 12984 | 0 | clean; existing suite only |
| v12b | T4 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 19.5 | 5 | 10 | 23540 | 1 | clean; existing suite only |
| v13 | T4 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 13.3 | 3 | 6 | 12997 | 0 | clean; existing suite only |
| v13b | T4 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 47.5 | 6 | 9 | 29405 | 0 | clean; existing suite only |
| v14 | T4 | 4/4 | 3 | 3 | 3 | 1 | 3 | 2 | 15 | 15 | 18.3 | 3 | 7 | 13269 | 0 | typo README.mdis, README log output claim, muddled Test Plan |
| v14b | T4 | 4/4 | 3 | 3 | 3 | 2 | 3 | 2 | 16 | 16 | 15.6 | 3 | 6 | 13120 | 0 | Risks: None padding |
| v16 | T4 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 13.9 | 4 | 7 | 17852 | 0 | clean; existing suite only |
| v16b | T4 | 4/4 | 3 | 3 | 3 | 3 | 3 | 2 | 17 | 17 | 26.6 | 5 | 8 | 22777 | 0 | clean; existing suite only |
| v0 | T5 | 3/4 | 2 | 3 | 1 | 2 | 3 | 1 | 12 | 0 | 90.8 | 9 | 15 | 33006 | 0 | Bare-heading steps with detached bodies, chained verify, package-lock.json and types.d.ts unmarked, test plan has no cases |
| v1 | T5 | 4/4 | 3 | 3 | 1 | 3 | 3 | 3 | 16 | 16 | 33.5 | 4 | 10 | 19255 | 0 | Chained verifies, build breaks after step 2, step 4 verify runs auth tests not yet written |
| v2 | T5 | 4/4 | 3 | 3 | 1 | 3 | 3 | 3 | 16 | 16 | 49.2 | 5 | 15 | 25570 | 0 | Chained verifies, store change breaks build until step 6, step 6 verify before tests rewritten |
| v3 | T5 | 4/4 | 3 | 3 | 1 | 2 | 3 | 3 | 15 | 15 | 37.0 | 5 | 12 | 24470 | 0 | Step 4 npm test runs before tests are updated; README verify none; 'placeholder' wording and garbled risk |
| v3b | T5 | 4/4 | 2 | 3 | 1 | 3 | 3 | 2 | 14 | 14 | 33.8 | 4 | 10 | 18240 | 0 | test/users.test.ts is verified but never created; build breaks at step 6; chained verify; README verify none |
| v4 | T5 | 4/4 | 3 | 3 | 1 | 3 | 3 | 3 | 16 | 16 | 45.1 | 4 | 12 | 21477 | 0 | Chained verifies, store signature change breaks build until step 5, step 5 verify runs before tests rewritten |
| v5 | T5 | 4/4 | 3 | 3 | 1 | 3 | 3 | 3 | 16 | 16 | 39.0 | 5 | 13 | 26863 | 0 | Chained verifies, store change breaks build until step 5, step 5 verify before tests rewritten |
| v5b | T5 | 4/4 | 3 | 3 | 1 | 1 | 1 | 3 | 12 | 12 | 189.5 | 5 | 12 | 31629 | 0 | Chained verifies, step 6 verify before tests updated; hedged store design and garbled constructor sentence; /register vs /auth/register; false native-deps risk |
| v6 | T5 | 4/4 | 3 | 3 | 1 | 3 | 2 | 3 | 15 | 15 | 66.3 | 5 | 12 | 29680 | 0 | Chained verifies and step 6 verify before tests updated; userId vs ownerId inconsistency |
| v6b | T5 | 4/4 | 2 | 3 | 1 | 1 | 3 | 2 | 12 | 12 | 18.3 | 4 | 11 | 20244 | 0 | test/auth.test.ts never created but verified; step 3 mounts a nonexistent router; step 5 verify before tests rewritten; garbled summary |
| v7 | T5 | 4/4 | 3 | 3 | 1 | 3 | 3 | 3 | 16 | 16 | 138.1 | 4 | 11 | 24283 | 0 | Two chained verifies |
| v7b | T5 | 4/4 | 3 | 3 | 2 | 2 | 3 | 3 | 16 | 16 | 43.7 | 5 | 11 | 26732 | 0 | Step 5 verifies tasks.test.ts before step 6 updates it; step 5 Files list is garbled |
| v8 | T5 | 4/4 | 2 | 3 | 2 | 2 | 1 | 3 | 13 | 13 | 41.4 | 4 | 10 | 20611 | 0 | package.json never scheduled; bcrypt vs bcryptjs contradiction; '409 or 400' hedge; step 6 verify before tests updated |
| v8b | T5 | 4/4 | 3 | 3 | 2 | 3 | 3 | 3 | 17 | 17 | 99.8 | 4 | 10 | 28711 | 0 | Step 1 verify chains npm install and build |
| v9 | T5 | 4/4 | 3 | 3 | 1 | 2 | 2 | 3 | 14 | 14 | 113.1 | 6 | 12 | 34226 | 1 | Chained verifies, step 6 verify before tests updated; padded tsconfig step with 'if necessary' hedge |
| v9b | T5 | 4/4 | 3 | 3 | 1 | 2 | 1 | 3 | 13 | 13 | 32.5 | 4 | 12 | 20849 | 0 | Build breaks at steps 3-5; /users vs /register contradiction and 403-or-404 hedge |
| v10 | T5 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 158.2 | 3 | 10 | 21072 | 0 | clean |
| v10b | T5 | 4/4 | 3 | 3 | 2 | 1 | 2 | 3 | 14 | 14 | 50.1 | 7 | 15 | 36182 | 1 | Store change breaks build until step 7; duplicated user store steps, garbled secret assumption, 'or we cast' hedge |
| v11 | T5 | 4/4 | 3 | 3 | 2 | 3 | 1 | 3 | 15 | 15 | 72.5 | 6 | 12 | 39199 | 1 | Step 5 verify before tests updated; 403-or-404 hedge and JWT_ENV vs JWT_SECRET contradiction |
| v11b | T5 | 4/4 | 3 | 3 | 3 | 3 | 2 | 3 | 17 | 17 | 152.3 | 4 | 11 | 28165 | 1 | '400 or 401' hedge for invalid credentials |
| v12 | T5 | 4/4 | 3 | 3 | 1 | 3 | 3 | 3 | 16 | 16 | 79.0 | 8 | 16 | 49603 | 1 | Step 4 auth tests run before wiring, build breaks steps 5-6, verify uses nonexistent npm run lint |
| v12b | T5 | 4/4 | 2 | 2 | 2 | 3 | 3 | 2 | 14 | 14 | 134.9 | 7 | 15 | 55280 | 1 | test/auth.test.ts never scheduled; step 2 signature change breaks build until step 5 |
| v13 | T5 | 4/4 | 3 | 3 | 2 | 3 | 2 | 3 | 16 | 16 | 97.4 | 4 | 11 | 23621 | 1 | Step 6 verify runs tasks.test.ts before step 7 updates it; 'createdBy/userId' hedge |
| v13b | T5 | 4/4 | 2 | 3 | 1 | 3 | 3 | 2 | 14 | 14 | 46.6 | 4 | 14 | 24460 | 1 | TaskStore signature change breaks routes until step 6, step 6 verify runs the unrewritten tasks.test.ts, and tasks.test.ts is never scheduled |
| v14 | T5 | 4/4 | 3 | 3 | 1 | 3 | 1 | 3 | 14 | 14 | 152.1 | 9 | 16 | 52571 | 1 | Build breaks after step 2, step 4 verify before tests updated; 'or split' hedge and /register vs /auth contradiction |
| v14b | T5 | 4/4 | 3 | 3 | 2 | 3 | 3 | 3 | 17 | 17 | 99.5 | 5 | 11 | 38378 | 1 | Step 2 verifies auth.test.ts before the auth router is wired in step 3 |
| v16 | T5 | 4/4 | 3 | 3 | 3 | 3 | 3 | 3 | 18 | 18 | 33.7 | 4 | 10 | 23812 | 1 | clean |
| v16b | T5 | 4/4 | 3 | 3 | 1 | 3 | 3 | 3 | 16 | 16 | 54.0 | 6 | 15 | 37176 | 1 | Store change breaks build until step 4, and step 4 verifies tasks.test.ts before step 5 rewrites it for auth |
| v0 | W1 | 3/4 | 1 | 1 | 1 | 1 | 1 | 1 | 6 | 0 | 228.4 | 14 | 28 | 651744 | 0 | plans to build the structured Nix extension feature that already exists, step bodies are detached from step headings, no concrete test cases |
| v1 | W1 | 4/4 | 3 | 3 | 1 | 2 | 2 | 2 | 13 | 13 | 74.6 | 7 | 17 | 258590 | 0 | notices the feature exists but step 1 verified by lint only, step 5 is a no-op check with an 'or' Verify, and test plan cases mostly already exist |

## Revision log

| Rev | What changed | Why |
| --- | --- | --- |
| v0 | Baseline at commit `a1094957`. | As found. |
| v1 | **Contract** rewritten: the turn ends at `ExitPlanMode`, the 12-round budget is named, the plan shape is fixed (title, Summary, Key Changes with Files and Verify, Test Plan, Assumptions, Risks), decide or record an assumption. Directives for the three pass types and the `ExitPlanMode` description say the same. **Harness:** read-budget nudges; a plan-less `PLAN_COMPLETE` is not accepted; the evaluator prompt defines complete as "the plan document is in the evidence"; a non-plan reply is not written as a plan file; step bodies, `Files touched:`, `Verification:` and bolded field names parse and stay under their step; a plan with no title gets `# Plan: <request>`; the CLI prints `plan written: <path>`. | Session `d9292a3d`, and the detached step bodies seen in every `v0` plan. |
| v2 | **Contract:** `Verify` is one real command, scoped, never a placeholder; the Test Plan is flat; an empty Risks section is left out. | `v1` plans with a placeholder `Verify` that printed a docstring, nested test bullets, "Risks: None identified". |
| v3 | **Harness:** a finished plan document written as the reply is accepted on that pass, without an evaluator call, with narration before its first heading dropped (`plans.ExtractDoc`). **Contract:** do not write the plan in the reply; a path is one you saw or is marked `(new)`; a step's tests go in that step; a decision is stated once. | `v2-T1`: the model wrote the plan as its reply, an evaluator call added about nine seconds and a narration sentence ended up in the file. A docs path under a directory that does not exist in a stress plan. |
| v4 | **Harness:** the plan lint (first `ExitPlanMode` of a turn sent back with up to five issues); the headless approve/refine/cancel hint. **Contract:** a documentation step verifies mechanically; steps are ordered so the build keeps working; every Test Plan test is written by a step. | The judges' notes: a step that changes a signature before its callers, a README step with `Verify: none`, tests named in the Test Plan that no step writes. |
| v5 | **Harness:** the parser reads `Files:` and `Verify:` written as a nested bullet list; the lint's test rule fires only when the Test Plan names new tests. | A `v4` run: the lint asked a plan whose Test Plan said "the existing suite still passes" for a test file, and the planner added an unrelated one. A `v3b` plan wrote `Files:` as a nested list and had no files recorded. |
| v6 | **Harness:** a nested `Files` bullet is one entry and is no longer split on commas. **Contract:** `Verify` runs from the directory that owns the file with the scripts and flags its manifest defines. | The `v5` stress plan scored 11: "Files bullets are shattered into dozens of fragments". That was a regression from `v5`'s own nested-list parsing, found by the judge. Malformed `npm test --path` forms and root-level commands for sub-package tests in several large-prompt plans. |
| v7 | **Harness:** the last round of every pass offers only the finishing tools (`planBudgetNudge` says so). | R1: the old build spent its first pass's whole read budget in 3 of 3 runs, and `v6` in 2 of 3, each costing a second pass of 2 to 4 minutes. |
| v8 | **Harness:** the lint does not send a plan back on a pass's last round or on the finishing pass. | R1 with `v7`: the model called `ExitPlanMode` on round 12 of 12, the lint sent the plan back, and that used up the round. |
| v9 | **Harness:** the plan lint also checks the plan against the repository, read-only: a `Files` entry that does not exist and is not marked `(new)`, a `Verify` that changes into a missing directory, runs `npm` where there is no `package.json` or no such script, passes a test path as a flag, or names a test file or Go package that does not exist. | The judges' notes on the large-repository plans: an invented docs directory, sub-package tests run from the repository root, an undefined npm script, `npm test --path`. In use the checks sent back two real flaws (a test file another step creates, and a test path that exists nowhere). |
| v10 | **Harness:** the plan lint also flags a `Verify` that chains commands, thinking-out-loud in the plan text ("Wait…", "Re-evaluate", "Simpler final approach") and stray foreign-script words. **Contract:** put only the final decision in the plan. | The judges' notes on `v9`: leaked deliberation in 5 of 14 plans, chained `Verify` lines, a stray Korean word. |
| v11 | **Harness:** the finishing surface (the last round of every pass and the final pass) offers only `ExitPlanMode` and `AskUserQuestion`; `update_plan` is gone. | R1 with `v10`: on the narrowed last round the model called `update_plan` instead of `ExitPlanMode` in 2 of 3 runs, spending the round and needing a second pass. |
| v12 | **Harness:** a plan of five or more steps that the lint finds nothing wrong with is sent back once with a fixed review checklist (files listed, one testing `Verify` per step, the build kept working, tests scheduled, nothing speculative). | The flaws left in large plans are ones a program cannot see. In `v12` the checklist never fired: those plans arrived on a last round, where the lint did not send plans back. |
| v13 | **Harness:** a plan sent back on a pass's last round gives that pass one more round (once) for the correction; only the loop's finishing pass accepts a plan as written. | In `v12` the lint and the review could only act on a plan that arrived before the last round; plans from large prompts mostly arrive on it. |
| v14 | **Harness:** the plan the lint sent back is kept, and is what the turn records if no corrected plan arrives. | One `v13` run, given its extra round, called a tool the finishing surface withholds and ended the turn with no plan file at all (`TestRejectedPlanIsRecordedWhenNoCorrectionArrives`). |
| v15 | **Harness:** a last round spent on a tool the finishing surface withholds (the model calls `update_plan` out of habit) is given back once, so the plan is still written in the pass. | R1 with `v14`: one run spent the narrowed last round of pass 1 on `update_plan`, which is refused, and the finishing pass wrote the plan. |
| v16 | **Harness:** a pass may give back up to two rounds (`planExtraRounds`), so a plan the lint sends back after the extra round can still be corrected in the same pass. | R1 with `v15`: one run used the extra round for the plan, the lint sent it back with no round left, and a second pass of ten rounds re-read the repository. |

Three fixes are not builds in this table because the headless runs cannot see them;
both were found by driving the real TUI (below) and are covered by tests. `belai
-plan` now starts the TUI in plan mode (`TestPlanFlagStartsInPlanMode`). The
permission decision model is told, by the harness, when a call targets a file
the approved plan lists (`TestApprovedPlanFactNamesOnlyListedTargets`); it
receives no model-written plan text. A plain remote session (no web controls,
the shape of the failing session) now offers the plan, takes the answer, and
runs an approved plan on a session that resolves asks to allow
(`TestPlainRemoteSessionOffersAndExecutesThePlan`). The remote-control plan
review and the corrected headless hint are covered by
`internal/rc/planreview_test.go` and `cmd/belai/planhint_test.go`.

## Checked outside the matrix

- **The TUI, with the real model, in a pseudo-terminal.** A prompt typed into
  `belai -plan` produced a plan file and the review pane (approve here, approve
  in a new session, edit, refine, cancel). Cancel returned to the chat in plan
  mode. Approve printed *Execute the approved plan.*, ran it in `plan ·
  executing` state, edited `main.go` and `README.md` with the Edit tool, and the
  session's auto-commit committed them. Refine took notes, started a new planning
  turn and the planner asked its own clarifying question. The edit option opened a stand-in
  `$EDITOR` on a copy of the plan, the review pane showed the edited plan and the
  plan file on disk held the editor's change. The first attempt ran in
  agent mode and went straight to an edit approval with no plan; that was the
  `-plan` bug. The next
  approve was denied by the decision model; that was the second TUI fix. 
- **The headless commands, verbatim.** A plan for the port change printed
  `approve: belai -allow-ask-without-tty -prompt "@<plan>"`. That command, run as
  printed with the same provider flags, edited both files, built and ran the
  tests. Without the flag a headless run cannot change files and says so. The
  hint's first version omitted the flag and did nothing.
- **A scoring slip.** An early approve test edited the shared Go project, so later
  builds planned the port change against code that already said 9090. The Go
  judge noticed; the port task was rerun for the seven affected builds on a clean
  copy and the Go plans were scored again.

## What the measurements say

- **The biggest quality change is `v0` to `v1`.** Gates passed went from 15/20 to
  20/20 on the small tasks and from 6/8 to 8/8 on the large prompts; mean quality
  rose from 13.0 to 15.8 and from 8.5 to 11.5. Most of it is the plan file itself:
  every `v0` plan failed G2 (no title) and had its step bodies after the last
  section, which the judge scores as unusable steps. The `v0` plan for the failing
  prompt also proposed to build a picker and a search route that already exist.
- **On the small tasks quality then sits at 15.0 to 17.2.** Repeats of the same
  build differ by up to 2.2, and there is no trend from `v2` to `v16b`: the
  contract refinements were each aimed at flaws the judges named and those flaws
  did fall afterwards, but the effect on the mean is smaller than the spread.
- **The checks against the repository helped on the large prompts.** For `v1` to
  `v8b` the large prompts averaged 12.2 over 26 runs (7 of them scored 14 or
  above); the 54 runs of the builds from `v9` to `v13` averaged 13.6 (26 scored 14
  or above). The lift of about one and a half points held as the sample grew,
  and the checks added after `v9` did not add to it until the last-round changes
  of `v14` to `v16` (below). What the checks
  caught in use were real: a test file another step creates, and a test path that
  exists nowhere. A lift of that size on a spread of about three is a modest gain,
  not a large one.
- **The last two harness changes are the only ones since `v11` with a visible
  effect, and they act on the same thing.** The groups of five builds from `v11`
  to `v16` average 14.0, 13.7, 13.1, 14.8, 14.5 and 16.0 on the large prompts.
  `v13` moved the lint and the review check onto a pass's last round, where most
  large plans arrive; `v14`, `v15` and `v16` made sure that round, and up to two
  more, are not lost to a rejected plan or to a refused `update_plan`. Inside the
  last scoring round `v14` to `v16` average 15.1 over 30 runs against 13.6 for
  `v9` to `v13` over 54, with 25 of the 30 at 14 or above. That is a lift of
  about one and a half points on a spread of about three, seen in one scoring round,
  so it is a likely gain and not a settled one. The review checklist fired 5
  times across `v12` to `v14` and is unproven.
- **What did move for certain is the failure the user reported.** In R1 the first
  pass ran out of reading budget without a plan in 3 of 3 runs on the old build
  and in 0 of 3 on each of `v8`, `v9` and `v11` (`v10` slipped to 2 of 3 until `update_plan` was taken off the last round), and a plan written in the first pass
  needs no extra pass and no evaluator call.
- **The guards fired where they should and rarely.** Across the 245 runs from `v1`
  on, the lint sent a plan back in 66, three runs failed a gate, and 12 runs used
  an evaluator call.
- **Rounds are low and steady.** A small task takes 3 to 5 rounds, a large prompt
  9 to 13, against a 12-round budget per pass. Tokens per small run rose from
  about 20 000 to about 25 000 to 27 000 in several builds from `v4` on, because
  the contract is longer and plans carry more detail.
- **Wall time is not a finding.** The same task (T1) took 19 seconds in one build
  and 107 in another with the same number of rounds, because of what else the
  machine was doing.
- **Model glitches remain.** A few plans carry a stray token (a half word, a
  number, one Korean word) or a mid-step self-correction; nothing in the harness
  removes them.

## Not measured, and what to do next

- **A live round trip with the real website.** The website side exists and was
  read, not run: `BelaiAskCard.vue` renders a `plan_review` ask and posts
  `{"choice": …, "notes": …}`, and the answers API accepts the `plan_review`
  kind. Belai's side was exercised against a fake web in tests, for a session
  with web controls and for a plain one. A live check would put real
  credentials and a real session on production, so it was not done. One small
  website change would help: the card always shows "Approve in a new session",
  which a remote session refuses (it cannot fork); the ask carries an `options`
  list the card could use to hide it.
- **Quality on large tasks.** At 15.1 of 18 over the 30 runs of `v14` to `v16` (14.1 over the 84 runs from `v9` on) the large prompts are
  better than before and still the weakest result. What the judges still note is
  mostly design judgement, which a harness check cannot supply: a plan that
  verifies a test that cannot load under the repository's node test runner, a
  design that does not fit the existing Worker, steps that go beyond a vague
  request. Some `Verify` chains and hedges survive the one send-back. Running each step's
  `Verify` in a throwaway copy would catch what reading the repository cannot
  (a test that fails, a build that breaks mid-sequence).
- **One model, one judge.** Everything here is `kimi-k2.7-code` scored by one judge
  model. Repeats differ by up to two points, so a difference smaller than that
  needs more repeats before it is a finding, and the same matrix should run on
  another model before the contract is called model-independent.
- **The TUI under a terminal other than the one used here.**
