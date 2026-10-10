#!/usr/bin/env bash
# blind.sh GROUP RUN... — prepares one anonymised scoring package.
#
# Copies each run's plan file to blind/GROUP/pNN.md under a random id, writes the
# id -> run map to blind/GROUP.map (give a judge the directory, never the map),
# and writes blind/GROUP/tasks.txt with one "pNN: task" line per plan. Mix every
# revision you want compared into one GROUP so a single judge scores them all
# against one standard (docs/plan-mode-tuning.md#how-runs-are-scored).
set -euo pipefail
S=${PLAN_BENCH_DIR:-/tmp/belai-plan-bench}
G="$1"; shift
D="$S/blind/$G"
rm -rf "$D" "$S/blind/$G.map"; mkdir -p "$D"
: > "$S/blind/$G.map"; : > "$D/tasks.txt"
ids=$(printf 'p%02d\n' $(seq 1 $#) | shuf)
i=0
for run in "$@"; do
  i=$((i+1))
  id=$(echo "$ids" | sed -n "${i}p")
  cp "$S"/results/$run/plans/*.md "$D/$id.md"
  echo "$id $run" >> "$S/blind/$G.map"
  echo "$id: $(cat "$S/results/$run/task.txt")" >> "$D/tasks.txt"
done
sort -o "$D/tasks.txt" "$D/tasks.txt"
echo "$G: $# plans"
