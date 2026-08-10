#!/usr/bin/env bash
# Print one check's assertions and commands in full. Handy when a result is
# surprising and the summary line is not enough.
#
#   bash scripts/inspect.sh <env> <check-id>
set -euo pipefail
cd "$(dirname "$0")/.."

ENVIRONMENT="${1:?usage: inspect.sh <env> <check-id>}"
CHECK="${2:?usage: inspect.sh <env> <check-id>}"

REPORT="$(mktemp)"
trap 'rm -f "$REPORT" "$REPORT.py"' EXIT

# The report is written to a file rather than piped: the reader below arrives
# on stdin as a here-document, and the two cannot share it.
./bin/bench run --env "$ENVIRONMENT" --only "$CHECK" --json >"$REPORT" 2>/dev/null || true

cat >"$REPORT.py" <<'PY'
import json, sys

report = json.load(open(sys.argv[1]))
for result in report["results"]:
    print(f'{result["status"].upper()}  {result["name"]}  ({result["millis"]} ms)')
    if result.get("message"):
        print(f'  message: {result["message"]}')
    print("  assertions:")
    for assertion in result["assertions"]:
        print(f'    [{assertion["status"]}] {assertion["name"]}')
        # The detail explains a failure; printing it beside a pass reads as a
        # contradiction. Notes carry the skip status and are always shown.
        if assertion.get("detail") and assertion["status"] != "pass":
            print(f'          {assertion["detail"]}')
    print("  commands:")
    for command in result["commands"]:
        print(f'    $ {command["command"]}   -> exit {command["exit_code"]}')
        for stream in ("stdout", "stderr"):
            body = command.get(stream) or ""
            for line in body.splitlines():
                print(f'      {stream[:3]}| {line}')
PY

python3 "$REPORT.py" "$REPORT"
