#!/usr/bin/env bash
# Syntax-check the console's JavaScript. The page is served as one file, so a
# stray comma is a blank browser window with nothing in any log — worth
# catching before a person opens it.
#
# Skips quietly when no JavaScript engine is installed; this is a Go project
# and Node is not a prerequisite.
set -uo pipefail
cd "$(dirname "$0")/.."

ENGINE=""
for candidate in node nodejs deno; do
  if command -v "$candidate" >/dev/null 2>&1; then ENGINE="$candidate"; break; fi
done
if [[ -z "$ENGINE" ]]; then
  echo "  skip  no JavaScript engine installed"
  exit 0
fi

EXTRACT="$(mktemp /tmp/ui-XXXXXX.js)"
trap 'rm -f "$EXTRACT"' EXIT

awk '/^<script>/{inside=1; next} /^<\/script>/{inside=0} inside' cmd/bench/ui.html > "$EXTRACT"

if [[ ! -s "$EXTRACT" ]]; then
  echo "  FAIL  no script block found in cmd/bench/ui.html"
  exit 1
fi

case "$ENGINE" in
  deno) deno check "$EXTRACT" >/dev/null 2>&1 ;;
  *)    "$ENGINE" --check "$EXTRACT" ;;
esac

if [[ $? -eq 0 ]]; then
  echo "  ok    console JavaScript parses ($(wc -l < "$EXTRACT") lines)"
else
  echo "  FAIL  console JavaScript does not parse"
  exit 1
fi
