# Plan mode benchmark

The scripts behind [docs/plan-mode-tuning.md](../../docs/plan-mode-tuning.md): they
run plan-mode turns against throwaway CRUD projects, compute the gates and the
measured numbers, and prepare anonymised plans for blind scoring. Run them to
compare two builds or two prompts; read the page for the method and the numbers.

Needs `bash`, `jq`, `awk`, `shuf`, the languages' toolchains only if you want to
run the generated projects (the planner never changes them), and credentials for
the provider under test.

```sh
export PLAN_BENCH_DIR=/tmp/belai-plan-bench          # where everything goes
export WEBSITE_DIR=$HOME/GitHub/Vulnetix/website      # optional: adds S1 and S2

# 1. Three small projects (Go, Python, TypeScript), each with a project
#    settings file that routes the classifier and evaluator roles.
bash bench/plan-mode/mkprojects.sh "$PLAN_BENCH_DIR/proj" path/to/project-settings.json

# 2. One build over the matrix. Run it again with another label for a repeat.
just build
bash bench/plan-mode/matrix.sh baseline ./belai
bash bench/plan-mode/matrix.sh candidate ./belai-candidate

# 3. Gates and measured numbers, one TSV row per run.
bash bench/plan-mode/gates.sh baseline-T1-go-patch candidate-T1-go-patch

# 4. Blind scoring: put every revision's plans for one project in one group,
#    hand a judge the group's directory and bench/plan-mode/rubric.md, and keep
#    the .map file to yourself until the scores are in.
bash bench/plan-mode/blind.sh go baseline-T1-go-patch candidate-T1-go-patch
```

`tui_drive.sh PROJECT PROMPT approve|cancel|refine|edit OUTFILE` runs the real TUI in a
pseudo-terminal (`script`), types the prompt, presses a choice on the review pane
and records the screen stream; read it with the escape codes stripped. It needs
the `script` command from util-linux.

`PLAN_PROVIDER` and `PLAN_MODEL` choose the model (default
`cloudflare-workers-ai` and `@cf/moonshotai/kimi-k2.7-code`); `BELAI_BIN` the
build; `BELAI_HOME` belai's state directory, which is where the transcripts are
read from.

Notes:

- Plan mode is read-only, so the projects are never changed by a run; the plan
  files each run wrote are copied out of `.vulnetix/plans/` and the run's
  transcript is copied from the session store.
- Run repeats. The same build differs by up to two points of mean quality between
  repeats, and wall time depends on what else the machine is doing, so compare
  rounds and tokens before seconds.
- Delete `$PLAN_BENCH_DIR` when you have scored: the projects are throwaway and
  the transcripts hold the prompts and tool results of every run.
