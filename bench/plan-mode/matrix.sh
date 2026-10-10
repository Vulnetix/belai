#!/usr/bin/env bash
# matrix.sh LABEL BELAI_BIN — the small-task matrix T1..T5 for one build, then,
# when WEBSITE_DIR is set, the two large-repository prompts S1 and S2.
#
# Tasks on the same project run one after another so their plan files and
# transcripts never mix; the three small projects run in parallel. Create the
# projects first:  mkprojects.sh $PLAN_BENCH_DIR/proj SETTINGS.json
# (docs/plan-mode-tuning.md#test-matrix).
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
S=${PLAN_BENCH_DIR:-/tmp/belai-plan-bench}
R="$1"; export BELAI_BIN="$2"
run() { bash "$HERE/run_task.sh" "$@"; }
(
  run "$R-T1-go-patch" "$S/proj/go-crud" "Add a PATCH /notes/{id} endpoint that updates only the fields present in the JSON body (title and/or body), returning the updated note. Keep PUT as it is."
  run "$R-T4-go-port" "$S/proj/go-crud" "Change the default listen port from 8080 to 9090 and update the README to match."
) &
run "$R-T2-py-pagination" "$S/proj/py-crud" "Add pagination to GET /items using limit and offset query parameters (defaults limit=20, max 100), and return the total count in a X-Total-Count response header." &
(
  run "$R-T3-ts-validate" "$S/proj/ts-crud" "Validate POST /tasks: title is required and must be 1 to 100 characters; respond 400 with a JSON error body otherwise."
  run "$R-T5-ts-auth" "$S/proj/ts-crud" "Add user accounts to this API: register and login endpoints issuing JWTs, every task owned by the user who created it, only the owner may read, update or delete a task, with tests and README updates."
) &
if [ -n "${WEBSITE_DIR:-}" ]; then
  (
    run "$R-S1-web-ratelimit" "$WEBSITE_DIR" "Add per-tenant rate limiting to the sandbox worker's HTTP API: a sliding window per Authentik subject with limits configurable per route group, 429 responses carrying Retry-After, and the counters kept in the Durable Object or D1 so they survive restarts. Include tests and docs." -no-git-sync
    run "$R-S2-web-vague" "$WEBSITE_DIR" "make the pix sandbox launch flow more robust and easier to debug" -no-git-sync
  ) &
fi
wait
echo "matrix $R done"
