#!/usr/bin/env bash
# Run commands that genuinely fail and show what the diagnosis says about them.
# Written as a script because the diagnosis is only worth anything if it is
# checked against real output from the real binary, not against a fixture.
set -uo pipefail
cd "$(dirname "$0")/.."

port="${1:-8731}"
./bin/testlab --addr "127.0.0.1:$port" --no-open >/tmp/verify-diagnosis.log 2>&1 &
server=$!
trap 'kill "$server" 2>/dev/null' EXIT

for _ in $(seq 40); do
  curl -sf "http://localhost:$port/api/catalogue" >/dev/null 2>&1 && break
  sleep 0.25
done

# args is the whole invocation, exactly as the page sends it.
run() {
  local id="$1" args="$2"
  printf '\n\033[1m── opencenter %s\033[0m\n' "$args"
  python3 - "$id" "$args" >/tmp/verify-body.json <<'PY'
import json, sys
print(json.dumps({"command": sys.argv[1], "env": "sandbox", "args": sys.argv[2]}))
PY
  curl -s -X POST "http://localhost:$port/api/run" \
    -H 'Content-Type: application/json' --data @/tmp/verify-body.json |
    sed -n '/^exit /,$p'
}

run "cluster validate"  "cluster validate no-such-cluster"
run "cluster show"      "cluster show no-such-cluster"
run "secrets list"      "secrets list --cluster no-such-cluster"
run "cluster"           "cluster not-a-subcommand"

printf '\n\033[1m── the CSV carries it too\033[0m\n'
curl -s "http://localhost:$port/api/results.csv" | cut -c1-200
