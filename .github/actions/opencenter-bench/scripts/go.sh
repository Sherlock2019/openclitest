#!/usr/bin/env bash
# Run the Go toolchain this repository was developed against.
#
#   bash scripts/go.sh build ./...
#
# The system Go on some machines is too old to parse a modern go.mod, and mise
# is not always on PATH in a non-login shell, so the version is located here
# rather than assumed everywhere.
set -uo pipefail
cd "$(dirname "$0")/.."

for candidate in \
  "$HOME/.local/share/mise/installs/go/1.26.4/bin" \
  "$HOME/.local/share/mise/installs/go/latest/bin"; do
  if [[ -x "$candidate/go" ]]; then
    export PATH="$candidate:$PATH"
    break
  fi
done

exec go "$@"
