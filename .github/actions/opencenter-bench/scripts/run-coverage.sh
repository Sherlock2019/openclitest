#!/usr/bin/env bash
# Run the coverage checks: the commands that no check executed before.
#
#   bash scripts/run-coverage.sh [environment]
set -uo pipefail
cd "$(dirname "$0")/.."

ENVIRONMENT="${1:-local}"
export OPENCLI_BIN="${OPENCLI_BIN:-/home/dzoan/opencli-benchmark/openCenter-cli/bin/opencenter}"

for candidate in "$HOME/.local/share/mise/installs/go"/*/bin; do
  [[ -x "$candidate/go" ]] && export PATH="$candidate:$PATH" && break
done

go build -o bin/bench ./cmd/bench || exit 1

echo "running the coverage checks against $OPENCLI_BIN"
echo
./bin/bench run --env "$ENVIRONMENT" --only \
  coverage-cluster-backup,coverage-cluster-drift,coverage-cluster-import,coverage-cluster-pool,coverage-cluster-service,coverage-cluster-misc,coverage-secrets,coverage-secrets-keys,coverage-settings,coverage-shell-init
