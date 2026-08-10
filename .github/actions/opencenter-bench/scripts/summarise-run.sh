#!/usr/bin/env bash
# Print the non-passing modules of a finished run, with the assertions that
# failed. The HTML report is the readable one; this is the one to paste.
#
#   bash scripts/summarise-run.sh <run-id>
set -euo pipefail
cd "$(dirname "$0")/.."

RUN="${1:?usage: summarise-run.sh <run-id>}"
REPORT="artifacts/runs/$RUN/reports/report.json"
[[ -f "$REPORT" ]] || { echo "no report at $REPORT" >&2; exit 1; }

READER="$(mktemp /tmp/summarise-XXXXXX.py)"
trap 'rm -f "$READER"' EXIT

cat >"$READER" <<'PY'
import json, sys

run = json.load(open(sys.argv[1]))
print(f"{run['state']}  ·  {run['passed']} passed, {run['failed']} failed, "
      f"{run['blocked']} blocked, {run['skipped']} skipped, {run['not_run']} not run")
print()

for module in run["modules"]:
    if module["state"] == "passed":
        continue
    print(f"{module['order']:>2}. {module['name']} — {module['state']} {module.get('message', '')}")
    for result in module.get("results") or []:
        if result["status"] not in ("fail", "error"):
            continue
        print(f"      {result['name']}")
        for assertion in result["assertions"]:
            if assertion["status"] == "fail":
                detail = assertion.get("detail", "").replace("\n", " ")[:160]
                print(f"        - {assertion['name']}: {detail}")
    print()
PY

python3 "$READER" "$REPORT"
