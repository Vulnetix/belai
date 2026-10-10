#!/usr/bin/env bash
# run_task.sh RUN_ID PROJECT_DIR "task text" [extra belai flags...]
#
# Runs one headless plan-mode turn and collects everything about it into
# $PLAN_BENCH_DIR/results/RUN_ID: the task, the plan file(s) the turn wrote, the
# session transcript, -usage-json, stdout, stderr, the exit code and the wall
# seconds. See docs/plan-mode-tuning.md.
#
# Environment:
#   PLAN_BENCH_DIR  where results go (default /tmp/belai-plan-bench)
#   BELAI_BIN       the belai binary to measure (default: belai on PATH)
#   BELAI_HOME      belai's state directory (default ~/.vulnetix/belai)
#   PLAN_PROVIDER, PLAN_MODEL  the provider and model (defaults:
#                   cloudflare-workers-ai and @cf/moonshotai/kimi-k2.7-code)
set -uo pipefail
S=${PLAN_BENCH_DIR:-/tmp/belai-plan-bench}
RUN="$1"; PROJ="$2"; TASK="$3"; shift 3
OUT="$S/results/$RUN"
BIN="${BELAI_BIN:-belai}"
HOME_DIR="${BELAI_HOME:-$HOME/.vulnetix/belai}"
PROVIDER="${PLAN_PROVIDER:-cloudflare-workers-ai}"
MODEL="${PLAN_MODEL:-@cf/moonshotai/kimi-k2.7-code}"

rm -rf "$OUT"; mkdir -p "$OUT"
echo "$TASK" > "$OUT/task.txt"
echo "$PROJ" > "$OUT/project.txt"
# Plans already in the project's directory are not this run's.
mkdir -p "$PROJ/.vulnetix/plans"; ls "$PROJ/.vulnetix/plans" > "$OUT/plans-before.txt"

start=$(date +%s.%N)
(cd "$PROJ" && BELAI_NO_TUI=1 timeout 1500 "$BIN" -trust-dir -plan -mode plan \
  -provider "$PROVIDER" -model "$MODEL" \
  -verbose -usage-json "$OUT/usage.json" "$@" -prompt "$TASK" \
  >"$OUT/stdout.txt" 2>"$OUT/stderr.txt")
rc=$?
end=$(date +%s.%N)
echo "$rc" > "$OUT/exit.txt"
awk -v a="$start" -v b="$end" 'BEGIN{printf "%.1f\n", b-a}' > "$OUT/seconds.txt"

# The plan files this run wrote.
mkdir -p "$OUT/plans"
for f in "$PROJ"/.vulnetix/plans/*.md; do
  [ -f "$f" ] && ! grep -qx "$(basename "$f")" "$OUT/plans-before.txt" && cp "$f" "$OUT/plans/"
done

# The newest session transcript of this project.
base=$(basename "$PROJ")
latest=$(ls -t "$HOME_DIR"/sessions/"$base"-*/*.jsonl 2>/dev/null | head -1)
if [ -n "$latest" ]; then cp "$latest" "$OUT/transcript.jsonl"; fi
echo "run=$RUN rc=$rc seconds=$(cat "$OUT/seconds.txt") plans=$(ls "$OUT/plans" 2>/dev/null | wc -l) transcript=${latest:-none}"
