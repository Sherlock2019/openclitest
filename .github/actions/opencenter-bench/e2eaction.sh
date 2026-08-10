#!/usr/bin/env bash
# Wire a repository to the cluster lifecycle workflow, and prove it runs.
#
# The counterpart of gitaction.sh, which does the same for the command bench.
# Both use exactly the inputs already in config/credentials.local.yaml —
# OPENCLI_ACTIONS_REPOSITORY and OPENCLI_ACTIONS_SSH_KEY (or _TOKEN) — so there
# is nothing new to fill in.
#
#   bash e2eaction.sh                 print the workflow, and the diff. Sends nothing.
#   bash e2eaction.sh install         push a branch and open a pull request.
#   bash e2eaction.sh run             start a run on GitHub.
#   bash e2eaction.sh results         read back what GitHub found.
#
# install and run write to somebody else's repository, so they need both gates:
# OPENCLI_ALLOW_ACTIONS_SETUP=1 in the environment, and --approve, which this
# script passes for you once you have asked for one of those verbs.
set -uo pipefail
cd "$(dirname "$0")"

BENCH=./bin/bench
[ -x "$BENCH" ] || { echo "build first:  mise run build"; exit 1; }

# What the generated workflow will say. All optional — left empty, the schedule
# and the real-provider job are simply not written, which is the safe default.
#
# OPENCLI_E2E_CLI_REPO is deliberately empty. This workflow is installed *into*
# the openCenter CLI repository, so actions/checkout has already put the commit
# that triggered the run on the runner, and blank means "test whoever called
# me". It used to default to opencenter-cloud/openCenter-cli, which is a
# different repository from the one being tested and pinned every run to a fixed
# ref — CI would have gone green against an unchanging upstream commit while the
# change under review was never built. The action's own documentation calls a
# concrete default here a trap; this was it.
export OPENCLI_E2E_CLI_REPO="${OPENCLI_E2E_CLI_REPO:-}"
export OPENCLI_E2E_NIGHTLY="${OPENCLI_E2E_NIGHTLY:-}"
export OPENCLI_E2E_REAL_ENVIRONMENT="${OPENCLI_E2E_REAL_ENVIRONMENT:-}"
export OPENCLI_E2E_TIMEOUT_MINUTES="${OPENCLI_E2E_TIMEOUT_MINUTES:-60}"

KIND=--kind=opencenter-e2e

case "${1:-preview}" in
  workflow)
    $BENCH actions workflow $KIND
    ;;
  preview|"")
    echo "== what is configured =="
    $BENCH actions config $KIND
    echo
    echo "== what would change in the repository (nothing is sent) =="
    $BENCH actions preview $KIND
    ;;
  install)
    $BENCH actions install $KIND --approve
    ;;
  run)
    $BENCH actions trigger $KIND --approve
    ;;
  results)
    $BENCH actions results $KIND
    ;;
  *)
    echo "usage: bash e2eaction.sh [workflow|preview|install|run|results]"
    exit 2
    ;;
esac
