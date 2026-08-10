#!/usr/bin/env bash
# Start the console, run the served page against the live catalogue, stop it.
set -uo pipefail
cd "$(dirname "$0")/.."

PORT="${1:-7715}"
BASE="http://127.0.0.1:$PORT"
LOG=/tmp/testlab-live.log

./bin/testlab --addr "127.0.0.1:$PORT" --no-open >"$LOG" 2>&1 &
SERVER=$!
cleanup() { kill "$SERVER" 2>/dev/null; wait "$SERVER" 2>/dev/null; }
trap cleanup EXIT

for _ in $(seq 1 100); do
  if curl -fsS "$BASE/api/catalogue" -o /dev/null 2>/dev/null; then break; fi
  sleep 0.2
done

if ! curl -fsS "$BASE/api/catalogue" -o /dev/null 2>/dev/null; then
  echo "the console never answered. its log:"
  cat "$LOG"
  exit 1
fi

echo "console up on $BASE"
echo "  page:      $(curl -fsS "$BASE/" | wc -c) bytes"
echo "  catalogue: $(curl -fsS "$BASE/api/catalogue" | wc -c) bytes"
echo

if ! command -v node >/dev/null 2>&1; then
  echo "  skip  node is not installed"
  exit 0
fi

node scripts/check-live-page.js "$BASE"
