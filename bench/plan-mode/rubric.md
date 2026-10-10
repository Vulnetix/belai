# Plan scoring rubric

You are scoring implementation plans that an AI planner wrote for a coding task. Each plan is a
markdown file. The plan is the artifact a human reviewer would be shown before approving. Score
the file as it is, including how it is laid out: text that is detached from the step it belongs
to, or that is garbled, counts against it.

Score each plan on six rows, 0 to 3 integers. Be strict and consistent: the same flaw costs the
same in every plan. Verify claims against the project directory you are given (read the files).
Do not reward length. Do not guess who wrote a plan or in what order; judge each on its own.

## Rows and anchors

**Q1 targets.** Every file the plan names exists in the project, or is marked as new; every file
the change needs is named.
- 3: all paths real or marked new, nothing needed is missing.
- 2: one unmarked path that does not exist, or one needed file missing.
- 1: several of either.
- 0: mostly wrong.

**Q2 completeness.** Every part of the request is covered by a step, and tests/docs are scheduled
when the request asks for them. Nothing in the request is silently dropped.
- 3: complete.
- 2: one part missing or only mentioned, not scheduled in any step.
- 1: several missing.
- 0: mostly missing.

**Q3 actionable.** Each step says what to change and where, and carries Files and a Verify that
is exactly one real command that could run at that point in the sequence: any test it runs
already exists by that step, and the code compiles at that step.
- 3: all steps meet this.
- 2: one flaw (a placeholder Verify, a command scoped too narrowly to exercise the change, a test
  that only a later step creates, a step that breaks the build until a later step, a hedged
  "or" alternative).
- 1: two or three flaws, or step bodies detached from their steps.
- 0: steps are bare headings or mostly unusable.

**Q4 proportional and clean.** The plan's size fits the task. No padding step, no description of
internals the change does not touch, no "Risks: none", no garbled or foreign-script tokens, no
leaked narration or unbalanced markup.
- 3: clean and sized right.
- 2: one flaw.
- 1: two or three.
- 0: bloated or full of noise.

**Q5 decisions.** Every open choice is decided once with one value (in a step or under
Assumptions), consistently. Nothing is left for the user inside a step. No contradiction between
a step and an assumption.
- 3: decided and consistent.
- 2: one contradiction, hedge or deferral.
- 1: several.
- 0: the plan asks the user to decide.

**Q6 tests.** The test plan names concrete test cases (what each asserts) and the command that
runs them, and every case is scheduled in some step's Files so someone would write it.
- 3: concrete, runnable, all scheduled.
- 2: concrete but some case is not scheduled in any step, or the cases are vague.
- 1: commands only, no cases.
- 0: no test plan.

## Clarifications (apply these when scoring)

- A Verify that chains several commands with `&&` is one Q3 flaw (the rubric asks for exactly one command).
- A documentation step whose Verify is "none" is acceptable only when nothing about it can be checked by a command; one that `grep` could check is one Q3 flaw.
- A task that needs no new tests, whose Test Plan names the command that runs the existing suite, scores 2 on Q6.
- A Verify that uses an npm form that does not work (a test path given as a flag instead of after `--`) is a Q3 flaw.
- A documentation path under a directory that does not exist and is not marked `(new)` is a Q1 flaw.

## Output

Write your scores as one JSON object to the output path you are given, with this shape, one entry
per plan id, and nothing else in the file:

{"p01": {"q1": 3, "q2": 3, "q3": 2, "q4": 3, "q5": 3, "q6": 3, "notes": "one sentence naming the main flaw, or 'clean'"}, ...}

