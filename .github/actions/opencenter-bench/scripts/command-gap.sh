#!/usr/bin/env bash
# Which openCenter commands does this bench actually run, and which does it not?
#
#   bash scripts/command-gap.sh [/path/to/opencenter]
#
# The command list comes from the binary. Coverage comes from grepping the
# checks for the argument sequence each command would be invoked with. A
# command nobody invokes is a command nobody tests, however many checks exist.
set -uo pipefail
cd "$(dirname "$0")/.."

BIN="${1:-${OPENCLI_BIN:-/home/dzoan/opencli-benchmark/openCenter-cli/bin/opencenter}}"
[[ -x "$BIN" ]] || { echo "no binary at $BIN" >&2; exit 1; }

WORK="$(mktemp -d /tmp/gap-XXXXXX)"
trap 'rm -rf "$WORK"' EXIT

# Walk the tree, three deep, the way the CLI presents it.
list_children() {
  "$BIN" "$@" --help 2>/dev/null | awk '
    /^Available Commands:/ { inside = 1; next }
    inside && NF == 0 { exit }
    inside && !/external plugin/ {
      if ($1 != "completion" && $1 != "help") print $1
    }'
}

: > "$WORK/commands.txt"
for top in $(list_children); do
  echo "$top" >> "$WORK/commands.txt"
  for second in $(list_children "$top"); do
    echo "$top $second" >> "$WORK/commands.txt"
    for third in $(list_children "$top" "$second"); do
      echo "$top $second $third" >> "$WORK/commands.txt"
    done
  done
done

sort -u "$WORK/commands.txt" -o "$WORK/commands.txt"
total="$(wc -l < "$WORK/commands.txt")"

covered=0
missing=0
: > "$WORK/missing.txt"

while IFS= read -r command; do
  # A check invokes a command as t.Run("cluster", "init", ...) or
  # {"cluster", "init"}, so look for the quoted words in sequence.
  pattern="$(sed 's/ /", *"/g' <<<"$command")"
  if grep -rqE "\"$pattern\"" internal/checks/ 2>/dev/null; then
    covered=$((covered + 1))
  else
    missing=$((missing + 1))
    echo "$command" >> "$WORK/missing.txt"
  fi
done < "$WORK/commands.txt"

echo "openCenter commands: $total"
echo "  run by a check:    $covered"
echo "  never run:         $missing"
echo
if [[ $missing -gt 0 ]]; then
  echo "never run by any check:"
  sed 's/^/  opencenter /' "$WORK/missing.txt"
fi

cp "$WORK/commands.txt" /tmp/all-commands.txt
cp "$WORK/missing.txt" /tmp/missing-commands.txt 2>/dev/null || true
