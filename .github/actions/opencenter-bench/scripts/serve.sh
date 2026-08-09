#!/usr/bin/env bash
# Rebuild and (re)start the console on one port, replacing whatever is there.
#
# There were two consoles on this machine and an old one left running on 7700,
# so what a browser showed had nothing to do with what had just been built.
# This makes "the thing on that port" and "the thing in this directory" the same.
set -uo pipefail
cd "$(dirname "$0")/.."

port="${1:-7700}"

bash scripts/build-testlab.sh >/tmp/serve-build.log 2>&1
if [[ $? -ne 0 ]]; then
  echo "build failed:" >&2
  tail -25 /tmp/serve-build.log >&2
  exit 1
fi
echo "built  $(tail -1 /tmp/serve-build.log)"

# Anything already on this port is stale by definition — this build replaces it.
for pid in $(pgrep -f "testlab --addr 127.0.0.1:$port" 2>/dev/null); do
  echo "stopping stale server pid $pid ($(readlink -f /proc/"$pid"/exe 2>/dev/null))"
  kill "$pid" 2>/dev/null
done
sleep 1

nohup ./bin/testlab --addr "127.0.0.1:$port" --no-open >"/tmp/console-$port.log" 2>&1 &
for _ in $(seq 40); do
  curl -sf "http://127.0.0.1:$port/api/catalogue" >/dev/null 2>&1 && break
  sleep 0.25
done

sed -n '1,8p' "/tmp/console-$port.log"
echo "  open http://127.0.0.1:$port"
