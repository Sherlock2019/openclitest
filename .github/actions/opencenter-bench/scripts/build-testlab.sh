#!/usr/bin/env bash
# Build and check the console: format, vet, build, then prove the page renders
# by running its own JavaScript.
set -uo pipefail
cd "$(dirname "$0")/.."

for candidate in "$HOME/.local/share/mise/installs/go"/*/bin; do
  [[ -x "$candidate/go" ]] && export PATH="$candidate:$PATH" && break
done

status=0
echo "go: $(go version)"

echo
echo "== gofmt =="
out="$(gofmt -l cmd/testlab 2>&1)"
if [[ -n "$out" ]]; then gofmt -w cmd/testlab; echo "formatted"; else echo "clean"; fi

echo
echo "== go vet =="
if go vet ./cmd/testlab/... 2>&1; then echo "clean"; else status=1; fi

echo
echo "== go build =="
mkdir -p bin
if go build -o bin/testlab ./cmd/testlab; then echo "built bin/testlab"; else status=1; fi

echo
echo "== page =="
if command -v node >/dev/null 2>&1; then
  node scripts/check-page.js || status=1
else
  echo "  skip  node is not installed"
fi

exit "$status"
