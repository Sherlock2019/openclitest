#!/usr/bin/env bash
# Rebuild the CLI under test and run the full A-to-Z workflow against it.
# The loop to use after changing the product.
#
#   bash scripts/rebuild-and-run.sh [/path/to/openCenter-cli]
set -uo pipefail
cd "$(dirname "$0")/.."

CLI="${1:-/home/dzoan/opencli-benchmark/openCenter-cli}"
for candidate in "$HOME/.local/share/mise/installs/go/1.26.4/bin" \
                 "$HOME/.local/share/mise/installs/go/latest/bin"; do
  [[ -x "$candidate/go" ]] && export PATH="$candidate:$PATH" && break
done

BIN=/tmp/opencenter-fixed
echo "building the CLI under test..."
(cd "$CLI" && go build -o "$BIN" .) || { echo "CLI build failed"; exit 1; }

echo "building the bench..."
go build -o bin/bench ./cmd/bench || { echo "bench build failed"; exit 1; }

export OPENCLI_BIN="$BIN"
echo
./bin/bench run full --source "$CLI" "${@:2}"
