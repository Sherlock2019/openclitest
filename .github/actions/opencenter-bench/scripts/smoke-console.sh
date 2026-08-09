#!/usr/bin/env bash
# Prove the console actually serves: start it on a spare port, ask for the
# page and every endpoint it depends on, then stop it. Run by scripts/dev.sh
# so a broken handler is caught before a person opens a browser on it.
set -uo pipefail
cd "$(dirname "$0")/.."

PORT="${1:-7699}"
BASE="http://127.0.0.1:$PORT"

./bin/bench ui --addr "127.0.0.1:$PORT" --no-open >/tmp/bench-console.log 2>&1 &
SERVER=$!
trap 'kill "$SERVER" 2>/dev/null' EXIT

for _ in $(seq 1 60); do
  curl -fsS "$BASE/api/state" >/dev/null 2>&1 && break
  sleep 0.25
done

status=0
probe() {
  local label="$1" url="$2" expect="$3"
  local body
  body="$(curl -fsS "$url" 2>&1)"
  if [[ $? -ne 0 ]]; then
    echo "  FAIL $label — no response"
    status=1
    return
  fi
  if [[ -n "$expect" ]] && ! grep -q "$expect" <<<"$body"; then
    echo "  FAIL $label — expected to find $expect"
    status=1
    return
  fi
  echo "  ok   $label (${#body} bytes)"
}

echo "console smoke test on $BASE"
probe "page"           "$BASE/"                          "Start Full A-to-Z Test"
probe "state"          "$BASE/api/state"                 '"categories"'
probe "workflow plan"  "$BASE/api/workflow/plan"         '"safety_agreement"'
probe "workflow runs"  "$BASE/api/workflow/runs"         ""
probe "plan local"     "$BASE/api/plan?env=local"        '"id"'
probe "plan sim"       "$BASE/api/plan?env=sim"          '"id"'
probe "preflight"      "$BASE/api/preflight?scope=local" '"results"'
probe "credentials"    "$BASE/api/credentials"           ""
probe "commands"       "$BASE/api/commands"              '"shell"'

# The command runner and the CSV export both need a real invocation to prove
# anything, so both are exercised rather than merely reachable.
if curl -fsS -X POST -H 'Content-Type: application/json' -d '{"id":"list"}' \
     "$BASE/api/commands/run" | grep -q 'environments'; then
  echo "  ok   command run streams output"
else
  echo "  FAIL command run"
  status=1
fi

LATEST="$(ls -1 artifacts/runs 2>/dev/null | tail -1)"
if [[ -n "$LATEST" ]]; then
  # Captured whole rather than piped into head: closing the pipe early makes
  # curl fail with a write error and the check would report a bug that is not
  # there.
  CSV="$(curl -fsS "$BASE/api/export/$LATEST/results.csv")"
  if [[ "$CSV" == run_id,module_order* ]]; then
    echo "  ok   csv export ($LATEST, $(wc -l <<<"$CSV") rows)"
  else
    echo "  FAIL csv export"
    status=1
  fi
else
  echo "  skip csv export — no run yet"
fi

# The event stream never ends, so ask for a second of it and check the shape.
if timeout 2 curl -fsS -N "$BASE/api/events" >/dev/null 2>&1 || [[ $? -eq 124 ]]; then
  echo "  ok   event stream"
else
  echo "  FAIL event stream"
  status=1
fi

exit "$status"
