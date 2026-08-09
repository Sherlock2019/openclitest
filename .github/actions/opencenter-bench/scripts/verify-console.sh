#!/usr/bin/env bash
# NOTE: scripts/check-run-button.js is the one that presses the button. The
# page checks counted eighty Run buttons while pressing one produced nothing,
# because rendering a button and wiring a button are different facts.
# The full check: build, start the console, and run the page the server serves
# against the data the server returns.
#
#   bash scripts/verify-console.sh
#
# This exists because "the API answers" and "the page works" are different
# claims, and passing the first while failing the second is exactly what
# produced a blank screen.
set -uo pipefail
cd "$(dirname "$0")/.."

PORT="${1:-7709}"
BASE="http://127.0.0.1:$PORT"

bash scripts/build-testlab.sh || exit 1

./bin/testlab --addr "127.0.0.1:$PORT" --no-open >/tmp/testlab-verify.log 2>&1 &
SERVER=$!
trap 'kill "$SERVER" 2>/dev/null' EXIT

for _ in $(seq 1 80); do
  curl -fsS "$BASE/api/catalogue" >/dev/null 2>&1 && break
  sleep 0.25
done
if ! curl -fsS "$BASE/api/catalogue" >/dev/null 2>&1; then
  echo "the console did not start:"; cat /tmp/testlab-verify.log; exit 1
fi

echo
echo "== the served page, against the live catalogue =="
if command -v node >/dev/null 2>&1; then
  node scripts/check-live-page.js "$BASE" || exit 1
else
  echo "  skip  node is not installed, so the served page was not exercised"
fi

echo
echo "== the API and a real command =="
bash scripts/smoke-testlab.sh "$((PORT + 1))"
