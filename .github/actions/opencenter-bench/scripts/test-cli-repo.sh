#!/usr/bin/env bash
# Run the openCenter CLI's own test suite, so a fix to it is checked against
# the tests its authors wrote and not only against the bench.
#
#   bash scripts/test-cli-repo.sh [/path/to/openCenter-cli]
set -uo pipefail

CLI="${1:-/home/dzoan/opencli-benchmark/openCenter-cli}"

for candidate in "$HOME/.local/share/mise/installs/go/1.26.4/bin" \
                 "$HOME/.local/share/mise/installs/go/latest/bin"; do
  [[ -x "$candidate/go" ]] && export PATH="$candidate:$PATH" && break
done

cd "$CLI"
OUT=/tmp/cli-repo-tests.txt

echo "== go build =="
go build ./... 2>&1 | head -20 && echo "  clean"

echo
echo "== go vet =="
go vet ./cmd/... ./internal/... >/tmp/cli-repo-vet.txt 2>&1
if [[ -s /tmp/cli-repo-vet.txt ]]; then head -20 /tmp/cli-repo-vet.txt; else echo "  clean"; fi

echo
echo "== go test ./cmd/... ./internal/... =="
go test ./cmd/... ./internal/... >"$OUT" 2>&1
code=$?

passed="$(grep -cE '^ok' "$OUT")"
failed="$(grep -cE '^FAIL' "$OUT")"
echo "  $passed packages ok, $failed failed"

if [[ $failed -gt 0 ]]; then
  echo
  echo "  failing packages:"
  grep -E '^FAIL' "$OUT" | head -20 | sed 's/^/    /'
  echo
  echo "  first failures:"
  grep -E '^\s*--- FAIL' "$OUT" | head -25 | sed 's/^/    /'
  echo
  echo "  full output: $OUT"
fi

exit "$code"
