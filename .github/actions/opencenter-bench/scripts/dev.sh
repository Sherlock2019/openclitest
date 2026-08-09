#!/usr/bin/env bash
# Developer loop for the bench itself: build, vet, format check, and a quick
# self-test. Kept in the repository because "how do I run this?" should not be
# something only the person who wrote it knows.
set -uo pipefail
cd "$(dirname "$0")/.."

# Prefer the Go the repository was developed against, but do not insist on mise.
for candidate in "$HOME/.local/share/mise/installs/go/1.26.4/bin" "$HOME/.local/share/mise/installs/go/latest/bin"; do
  if [[ -x "$candidate/go" ]]; then
    export PATH="$candidate:$PATH"
    break
  fi
done

echo "go: $(command -v go) $(go version)"

status=0
echo
echo "== gofmt =="
unformatted="$(gofmt -l cmd internal 2>&1)"
if [[ -n "$unformatted" ]]; then
  echo "$unformatted"
  status=1
else
  echo "clean"
fi

echo
echo "== go vet =="
if go vet ./... 2>&1; then
  echo "clean"
else
  status=1
fi

echo
echo "== go build =="
if go build -o bin/bench ./cmd/bench 2>&1; then
  echo "built bin/bench"
else
  status=1
fi

echo
echo "== go test =="
if ! go test ./... 2>&1 | tail -25; then
  status=1
fi

echo
echo "== console =="
bash scripts/check-ui.sh || status=1
if [[ -x bin/bench ]]; then
  bash scripts/smoke-console.sh || status=1
fi

exit "$status"
