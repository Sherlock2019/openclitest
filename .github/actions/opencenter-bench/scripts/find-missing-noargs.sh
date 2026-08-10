#!/usr/bin/env bash
# List command groups whose RunE only prints help but which do not restrict
# their arguments.
#
# Cobra treats the root command specially: an unknown first argument there is
# an error. A command *group* gets no such treatment, so `opencenter secrets
# definitely-not-a-command` is parsed as an argument to `secrets`, runs its
# RunE, prints help and exits 0 — a typo reported as success.
#
#   bash scripts/find-missing-noargs.sh /path/to/openCenter-cli
set -euo pipefail
CLI="${1:?usage: find-missing-noargs.sh <openCenter-cli checkout>}"

cd "$CLI"
for file in $(grep -rl 'return cmd.Help()' cmd/*.go); do
  # The Args line, when present, sits within a few lines of the RunE.
  line="$(grep -n 'return cmd.Help()' "$file" | head -1 | cut -d: -f1)"
  start=$(( line > 12 ? line - 12 : 1 ))
  if sed -n "${start},${line}p" "$file" | grep -q 'Args:'; then
    printf '  ok    %s\n' "$file"
  else
    use="$(sed -n "1,${line}p" "$file" | grep -o 'Use:[[:space:]]*"[^"]*"' | tail -1)"
    printf '  MISS  %-40s %s\n' "$file" "$use"
  fi
done
