#!/usr/bin/env bash
# gates.sh RUN_ID... — one TSV row per run: the four gates and the measured
# efficiency numbers, computed the same way for every run
# (docs/plan-mode-tuning.md#how-runs-are-scored).
#
# columns: run G1 G2 G3 G4 steps stepsWithFiles stepsWithVerify detachedLines
#          seconds evalCalls rounds toolCalls tokens
set -uo pipefail
S=${PLAN_BENCH_DIR:-/tmp/belai-plan-bench}
for RUN in "$@"; do
  OUT="$S/results/$RUN"
  T="$OUT/transcript.jsonl"
  P=$(ls "$OUT"/plans/*.md 2>/dev/null | head -1)
  g1=FAIL; g2=FAIL; g3=FAIL; g4=FAIL
  steps=0; sf=0; sv=0; detached=0
  if [ -n "$P" ]; then
    g1=ok
    hasTitle=$(head -1 "$P" | grep -c '^# ')
    hasSum=$(grep -c '^## Summary' "$P")
    steps=$(grep -c '^[0-9]\+\. ' "$P")
    hasTest=$(grep -c '^## Test Plan' "$P")
    hasAss=$(grep -c '^## Assumptions' "$P")
    if [ "$hasTitle" -ge 1 ] && [ "$hasSum" -ge 1 ] && [ "$steps" -ge 1 ] && [ "$hasTest" -ge 1 ] && [ "$hasAss" -ge 1 ]; then g2=ok; fi
    # Steps whose block (up to the next numbered step or ## heading) has the field.
    sf=$(awk '/^[0-9]+\. /{if(n)f+=h;n=1;h=0} /^## /{if(n)f+=h;n=0;h=0} /- Files:/{h=1} END{if(n)f+=h;print f+0}' "$P")
    sv=$(awk '/^[0-9]+\. /{if(n)f+=h;n=1;h=0} /^## /{if(n)f+=h;n=0;h=0} /- Verify:/{h=1} END{if(n)f+=h;print f+0}' "$P")
    # Files/Verify lines after the Test Plan, Assumptions or Risks heading: step
    # detail that was detached from its step.
    detached=$(awk '/^## (Risks|Assumptions|Test Plan)/{post=1} /^## (Steps|Key Changes)/{post=0} post&&/- \*?\*?(Files|Verify)/{c++} END{print c+0}' "$P")
    # Clean: no reasoning leakage, and the last line is part of the plan
    # (a bullet, a numbered line, a heading or an indented line), not narration.
    last=$(grep -v '^\s*$' "$P" | tail -1)
    if ! grep -qE '</think>|<think>|\bI are\b' "$P" && echo "$last" | grep -qE '^(\s*[-*]|\s*[0-9]+\.|#|\s{2,})'; then g4=ok; fi
  fi
  if [ -f "$T" ] && jq -r 'select(.type=="assistant") | .meta.tool_calls[]?.name' "$T" | grep -qx ExitPlanMode; then g3=ok; fi
  secs=$(cat "$OUT/seconds.txt" 2>/dev/null || echo 0)
  evals=$(jq -r 'select(.meta.activity=="plan_eval")|1' "$T" 2>/dev/null | wc -l)
  rounds=$(jq -c 'select(.type=="assistant")' "$T" 2>/dev/null | wc -l)
  calls=$(jq -r 'select(.type=="assistant") | .meta.tool_calls[]?.name' "$T" 2>/dev/null | wc -l)
  tokens=$(jq -r '.tokens // 0' "$OUT/usage.json" 2>/dev/null || echo 0)
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$RUN" "$g1" "$g2" "$g3" "$g4" "$steps" "$sf" "$sv" "$detached" "$secs" "$evals" "$rounds" "$calls" "$tokens"
done
