#!/usr/bin/env bash
# tui_drive.sh PROJECT PROMPT CHOICE OUTFILE [belai flags...]
# Runs the real TUI in a pseudo-terminal in plan mode, types PROMPT, waits for
# the plan review pane, presses CHOICE (approve | cancel | refine | edit; set EDITOR to a script for edit),
# lets the turn run, then quits. The raw terminal stream is written to OUTFILE.
set -uo pipefail
PROJ="$1"; PROMPT="$2"; CHOICE="$3"; OUT="$4"; shift 4
BIN=${BELAI_BIN:-belai}
rm -f "$OUT" "$OUT.in"; mkfifo "$OUT.in"
DOWN=$'\033[B'
(
  sleep 12
  printf '%s\r' "$PROMPT"
  for i in $(seq 1 90); do grep -aq 'Review plan' "$OUT" && break; sleep 4; done
  sleep 3
  case "$CHOICE" in
    edit)    printf "%s%s\r" "$DOWN" "$DOWN"; sleep 8; printf "%s%s%s%s\r" "$DOWN" "$DOWN" "$DOWN" "$DOWN" ;;
    approve) printf '\r' ;;
    cancel)  printf '%s%s%s%s\r' "$DOWN" "$DOWN" "$DOWN" "$DOWN" ;;
    refine)  printf '%s%s%s\r' "$DOWN" "$DOWN" "$DOWN"; sleep 3; printf 'also state the default in a comment\r' ;;
  esac
  if [ "$CHOICE" = approve ]; then for i in $(seq 1 14); do sleep 10; printf "\r"; done; fi
  sleep "${DRIVE_WAIT:-100}"
  printf '\003'; sleep 1; printf '\004'
) > "$OUT.in" &
cd "$PROJ"
TERM=xterm-256color script -qfc "stty rows 50 cols 170; timeout 700 $BIN -trust-dir -plan -provider ${PLAN_PROVIDER:-cloudflare-workers-ai} -model ${PLAN_MODEL:-@cf/moonshotai/kimi-k2.7-code} -no-git-sync $*" "$OUT" < "$OUT.in" > /dev/null 2>&1
wait
echo "driven: $OUT ($(wc -c < "$OUT") bytes)"
