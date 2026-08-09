#!/usr/bin/env bash
# Start the console and prove it end to end: the page serves, the catalogue has
# every command per environment, the fixture can be created, and a real command
# runs and reports a real exit code.
set -uo pipefail
cd "$(dirname "$0")/.."

PORT="${1:-7702}"
BASE="http://127.0.0.1:$PORT"

./bin/testlab --addr "127.0.0.1:$PORT" --no-open >/tmp/testlab-smoke.log 2>&1 &
SERVER=$!
trap 'kill "$SERVER" 2>/dev/null' EXIT

for _ in $(seq 1 80); do
  curl -fsS "$BASE/api/catalogue" >/dev/null 2>&1 && break
  sleep 0.25
done
if ! curl -fsS "$BASE/api/catalogue" >/dev/null 2>&1; then
  echo "the console did not start:"; cat /tmp/testlab-smoke.log; exit 1
fi

status=0
ok()  { printf '  ok    %s\n' "$1"; }
bad() { printf '  FAIL  %s\n     %s\n' "$1" "$2"; status=1; }

echo "smoke test on $BASE"

PAGE="$(curl -fsS "$BASE/")"
# The strip is rendered from the API, so the static HTML holds the container
# and the API holds the words. Check the right thing in the right place.
grep -q 'id="strip"' <<<"$PAGE" && ok "the page has the top strip" \
  || bad "the top strip container" "missing from the served page"
grep -q 'id="envtabs"' <<<"$PAGE" && ok "the page has the environment tabs" \
  || bad "the environment tabs" "missing from the served page"

# Written to a file, not passed as an argument: the catalogue is far larger
# than an argv can hold.
CATFILE="$(mktemp)"
READER="$(mktemp)"
trap 'kill "$SERVER" 2>/dev/null; rm -f "$CATFILE" "$READER"' EXIT
curl -fsS "$BASE/api/catalogue" >"$CATFILE"

cat >"$READER" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
catalogue = data["catalogue"]
problems = 0

total = catalogue["total_commands"]
print(f"  ok    the catalogue holds {total} commands")

for environment in catalogue["environments"]:
    commands = environment["commands"]
    stages = {c["stage"] for c in commands}
    tasks = {c["task"] for c in commands}
    unready = [c["id"] for c in commands if not c.get("ready")]
    if unready:
        print(f"  FAIL  {environment['id']}: {len(unready)} commands with no ready-to-run line")
        problems += 1
    else:
        print(f"  ok    {environment['name']}: {len(commands)} commands, "
              f"{len(stages)} stages, {len(tasks)} tasks, all ready to run")

if not any(c["id"] == "cluster init" for c in catalogue["environments"][0]["commands"]):
    print("  FAIL  cluster init is not in the catalogue")
    problems += 1

sys.exit(1 if problems else 0)
PY
python3 "$READER" "$CATFILE" || status=1

# The four panels come from the API, so that is where to check their words.
for want in "What we test" "Why we test" "Where we test" "How we test"; do
  grep -qF "$want" "$CATFILE" && ok "the API carries \"$want\"" \
    || bad "the API is missing \"$want\"" ""
done

# Create the fixture, then run a real command against it.
FIXTURE="$(curl -fsS -X POST -H 'Content-Type: application/json' \
  -d '{"env":"kind"}' "$BASE/api/fixture")"
grep -q '\[exit 0' <<<"$FIXTURE" && ok "the kind fixture is created" \
  || bad "creating the kind fixture" "$(head -3 <<<"$FIXTURE")"

ACTIVE="$(curl -fsS -X POST -H 'Content-Type: application/json' \
  -d '{"env":"kind","command":"cluster active","args":"cluster active"}' "$BASE/api/run")"
grep -q 'tb-kind' <<<"$ACTIVE" && grep -q '\[exit 0' <<<"$ACTIVE" \
  && ok "the fixture is selected for active-cluster commands" \
  || bad "the fixture was not made active" "$ACTIVE"

RUN="$(curl -fsS -X POST -H 'Content-Type: application/json' \
  -d '{"env":"kind","command":"cluster list","args":"cluster list --output json"}' "$BASE/api/run")"
grep -q 'opencenter cluster list' <<<"$RUN" && ok "a command runs and echoes itself" \
  || bad "running a command" "$RUN"
grep -q 'tb-kind' <<<"$RUN" && ok "it sees the fixture it just created" \
  || bad "the fixture is not visible to the next command" "$RUN"
grep -q '\[exit 0' <<<"$RUN" && ok "it reports a real exit code" \
  || bad "no exit code reported" "$RUN"

# A mutating command must be refused without the gate.
CODE="$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d '{"env":"kind","command":"cluster deploy","args":"cluster deploy testbench/tb-kind"}' \
  "$BASE/api/run")"
if [[ "${OPENCLI_ALLOW_MUTATE:-}" == "1" ]]; then
  ok "the mutation gate is open, deliberately"
elif [[ "$CODE" == "403" ]]; then
  ok "deploy is refused without the mutation gate"
else
  bad "deploy should be refused without the gate" "got HTTP $CODE"
fi

# The same command with the CLI's explicit preview flag is safe and should be
# executable even while the real mutation gate remains closed.
PREVIEW="$(curl -fsS -X POST -H 'Content-Type: application/json' \
  -d '{"env":"kind","command":"cluster deploy","args":"cluster deploy testbench/tb-kind --dry-run"}' \
  "$BASE/api/run")"
grep -q '\[exit 0' <<<"$PREVIEW" && ok "deploy dry-run is allowed without the mutation gate" \
  || bad "deploy dry-run was blocked or failed" "$PREVIEW"

CSV="$(curl -fsS "$BASE/api/results.csv")"
[[ "$CSV" == at,cli_version,cli_repository,cli_ref,cli_commit,platform,environment* ]] \
  && ok "CSV export includes the tested Git repository and ref" \
  || bad "CSV export" "unexpected header"

echo
[[ $status -eq 0 ]] && echo "  all good" || echo "  problems above"
exit "$status"
