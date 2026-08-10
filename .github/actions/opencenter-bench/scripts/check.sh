#!/usr/bin/env bash
# Build and test everything in this repository.
set -uo pipefail
cd "$(dirname "$0")/.."

for candidate in "$HOME/.local/share/mise/installs/go"/*/bin; do
  [[ -x "$candidate/go" ]] && export PATH="$candidate:$PATH" && break
done

status=0
echo "== executable bits =="
# Editing over the Windows share resets scripts to 644, and the symptom is
# "./start.sh: Permission denied" rather than anything to do with the change.
bash scripts/fix-exec-bits.sh --check || status=1

echo
echo "== gofmt =="
# Formats, and then fails anyway.
#
# It used to format and exit zero, which made this check worse than useless: it
# rewrote the files, said "formatted: …", and reported success — so a green
# run here meant nothing about CI, where `gofmt -l .` is a hard failure. Two
# unformatted files reached main that way, and four CI runs failed on them
# before anybody looked.
#
# Fixing the files is still the useful thing to do, so it still does that. What
# changed is that it now says the tree was wrong, in a way that stops a push.
out="$(gofmt -l cmd internal pkg 2>&1)"
if [[ -n "$out" ]]; then
  gofmt -w cmd internal pkg
  echo "formatted (and failing, because CI would have):"
  echo "$out" | sed 's/^/    /'
  echo "    they are fixed now — review, then commit and run this again"
  status=1
else
  echo "clean"
fi

echo
echo "== go vet =="
go vet ./... 2>&1 && echo clean || status=1

echo
echo "== go build =="
go build ./... 2>&1 && echo clean || status=1

echo
echo "== go test =="
go test ./internal/... ./cmd/... 2>&1 | grep -Ev 'no test files' || status=1

exit "$status"
