#!/usr/bin/env bash
# Trigger a repository's Test Bench pipeline and report what it found.
#
# The end-to-end check that nothing else does: push a commit to a real
# repository, watch GitHub run the bench against it, and print which command
# failed. Everything else in this repository tests a piece — this tests the
# whole chain, from a commit to a verdict.
#
#   scripts/testgitaction.sh git@github.com:Sherlock2019/openCenter-cli-testDzoan.git
#   scripts/testgitaction.sh owner/name --branch ci-test
#   scripts/testgitaction.sh owner/name --dry-run
#
# An empty commit by default. The point is to trigger the pipeline, not to
# change anybody's code, and a commit that adds a file leaves litter somebody
# has to clean up later. `git commit --allow-empty` is the honest way to say
# "run CI against this tree".
set -uo pipefail

REPO=""
BRANCH=""
MESSAGE=""
DRY_RUN=0
NO_WATCH=0
TOUCH_FILE=0

usage() {
  cat <<'TEXT'
scripts/testgitaction.sh <repository> [options]

  <repository>     owner/name, an https:// URL, or an ssh remote

  --branch NAME    push to this branch instead of the default. Safer: a branch
                   still triggers the pipeline, and does not touch main.
  --message TEXT   commit message
  --file           write a timestamp into .test-bench-trigger instead of
                   pushing an empty commit. Use when a ruleset refuses empty
                   commits; leaves a file behind, so it is not the default.
  --dry-run        say what would happen and push nothing
  --no-watch       push and exit, rather than waiting for the run
  -h, --help       this

  Watching needs the GitHub CLI, authenticated: gh auth login
TEXT
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --branch)  BRANCH="${2:-}"; shift 2 ;;
    --message) MESSAGE="${2:-}"; shift 2 ;;
    --file)    TOUCH_FILE=1; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    --no-watch) NO_WATCH=1; shift ;;
    -h|--help) usage; exit 0 ;;
    -*)        echo "unknown option: $1" >&2; usage; exit 2 ;;
    *)         if [[ -z "$REPO" ]]; then REPO="$1"; else
                 echo "more than one repository given: $REPO and $1" >&2; exit 2
               fi
               shift ;;
  esac
done

if [[ -z "$REPO" ]]; then
  echo "no repository given" >&2
  echo >&2
  usage >&2
  exit 2
fi

# --- normalise ----------------------------------------------------------------
#
# The same three spellings the action and internal/gitopsupdate accept, so a URL
# that works in the console works here. A slug is needed separately because the
# GitHub CLI wants owner/name while git wants something it can clone.
slug_of() {
  local value="${1%/}"
  value="${value%.git}"
  case "$value" in
    git@*:*) value="${value#*:}" ;;
    *://*)   value="${value#*://}"; value="${value#*/}" ;;
  esac
  case "$value" in
    */*) local name="${value##*/}" rest="${value%/*}"
         printf '%s/%s\n' "${rest##*/}" "$name" ;;
    *)   printf '%s\n' "$value" ;;
  esac
}

SLUG="$(slug_of "$REPO")"
if [[ ! "$SLUG" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  echo "not a repository: $REPO" >&2
  exit 2
fi
# A bare owner/name is not something git can clone; https is the safe assumption
# because it needs no local key.
CLONE_URL="$REPO"
case "$REPO" in
  */*) [[ "$REPO" == *:* || "$REPO" == *//* ]] || CLONE_URL="https://github.com/$SLUG.git" ;;
esac

[[ -n "$MESSAGE" ]] || MESSAGE="ci: trigger the Test Bench ($(date -u +%Y-%m-%dT%H:%M:%SZ))"

# Only GitHub has an Actions tab. A local path or another host normalises to a
# plausible-looking owner/name, and printing a github.com link for it sends
# somebody to a 404 wondering why their run is missing.
IS_GITHUB=0
case "$REPO" in
  *github.com[:/]*|*/*) IS_GITHUB=1 ;;
esac
case "$REPO" in
  file://*|/*|./*|../*) IS_GITHUB=0 ;;
  *://*) [[ "$REPO" == *github.com* ]] || IS_GITHUB=0 ;;
esac
actions_url() { [[ $IS_GITHUB -eq 1 ]] && echo "  https://github.com/$SLUG/actions"; }

echo
echo "  repository : $SLUG"
echo "  clone via  : $CLONE_URL"
echo "  branch     : ${BRANCH:-<default>}"
echo "  commit     : $([[ $TOUCH_FILE -eq 1 ]] && echo 'a file with a timestamp' || echo 'empty')"
echo "  message    : $MESSAGE"
echo

if [[ $DRY_RUN -eq 1 ]]; then
  echo "  --dry-run: nothing was cloned, committed or pushed."
  exit 0
fi

# --- clone, commit, push ------------------------------------------------------
#
# A throwaway checkout every time, removed however this ends. Using an existing
# working copy would risk pushing whatever somebody had left uncommitted in it.
WORK="$(mktemp -d)"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

echo "  cloning…"
if ! git clone --quiet --depth 1 ${BRANCH:+--branch "$BRANCH"} \
     "$CLONE_URL" "$WORK/repo" 2>"$WORK/err"; then
  # A branch that does not exist yet is an ordinary thing to ask for, not a
  # failure: clone the default and create it.
  if [[ -n "$BRANCH" ]] && grep -qi "not found in upstream\|Remote branch" "$WORK/err"; then
    echo "  $BRANCH does not exist yet — creating it from the default branch"
    git clone --quiet --depth 1 "$CLONE_URL" "$WORK/repo" || {
      cat "$WORK/err" >&2; exit 5; }
    git -C "$WORK/repo" checkout -q -b "$BRANCH"
  else
    sed 's/^/    /' "$WORK/err" >&2
    exit 5
  fi
fi

cd "$WORK/repo" || exit 5
BEFORE="$(git rev-parse --short HEAD)"
TARGET="${BRANCH:-$(git rev-parse --abbrev-ref HEAD)}"

# Identity only for this checkout, so a machine with no global git config still
# works and nobody's ~/.gitconfig is touched.
git config user.name  "openCenter Test Bench"
git config user.email "test-bench@opencenter.local"

if [[ $TOUCH_FILE -eq 1 ]]; then
  date -u +%Y-%m-%dT%H:%M:%SZ > .test-bench-trigger
  git add .test-bench-trigger
  git commit --quiet -m "$MESSAGE"
else
  git commit --quiet --allow-empty -m "$MESSAGE"
fi

AFTER="$(git rev-parse --short HEAD)"
echo "  committed  : $BEFORE -> $AFTER on $TARGET"

echo "  pushing…"
if ! git push --quiet origin "HEAD:$TARGET" 2>"$WORK/perr"; then
  sed 's/^/    /' "$WORK/perr" >&2
  echo >&2
  echo "  The push was refused. Common causes:" >&2
  echo "    - no write access with the credential this machine uses" >&2
  echo "    - a branch protection rule on $TARGET" >&2
  echo "    - the repository requires signed commits" >&2
  exit 5
fi
echo "  pushed     : $AFTER -> $SLUG@$TARGET"

if [[ $NO_WATCH -eq 1 ]]; then
  echo
  actions_url
  exit 0
fi

# --- watch --------------------------------------------------------------------
if [[ $IS_GITHUB -eq 0 ]]; then
  echo
  echo "  $SLUG is not a GitHub repository, so there is no Actions run to follow."
  exit 0
fi
if ! command -v gh >/dev/null 2>&1; then
  echo
  echo "  The GitHub CLI is not installed, so the run cannot be followed here."
  actions_url
  exit 0
fi
if ! gh auth status >/dev/null 2>&1; then
  echo
  echo "  gh is not authenticated (gh auth login), so the run cannot be followed."
  actions_url
  exit 0
fi

echo
echo "  waiting for GitHub to register the run…"
RUN_ID=""
for _ in $(seq 1 20); do
  sleep 3
  RUN_ID="$(gh run list --repo "$SLUG" --workflow=test-bench.yml --limit 5 \
    --json databaseId,headSha --jq \
    "[.[] | select(.headSha | startswith(\"$AFTER\"))][0].databaseId" 2>/dev/null)"
  [[ -n "$RUN_ID" && "$RUN_ID" != "null" ]] && break
  RUN_ID=""
done

if [[ -z "$RUN_ID" ]]; then
  echo "  No Test Bench run appeared for $AFTER after a minute."
  echo "  Either the workflow is not installed in $SLUG, or it does not run on push."
  actions_url
  exit 1
fi

echo "  run        : $RUN_ID"
echo "  https://github.com/$SLUG/actions/runs/$RUN_ID"
echo
gh run watch "$RUN_ID" --repo "$SLUG" --exit-status
WATCHED=$?

# --- what it found ------------------------------------------------------------
#
# The exit status alone says pass or fail. Which command failed is in the
# uploaded evidence, and `bench actions runs` already knows how to read it —
# so this asks that rather than parsing a log.
echo
BENCH="$(cd "$(dirname "$0")/.." && pwd)/bin/bench"
if [[ -x "$BENCH" ]]; then
  echo "  reading the evidence…"
  OPENCLI_ACTIONS_REPOSITORY="$SLUG" \
  OPENCLI_ACTIONS_TOKEN="$(gh auth token 2>/dev/null)" \
    "$BENCH" actions runs || true
else
  echo "  bin/bench is not built, so the failing commands cannot be listed here."
  echo "  Build it with: go build -o bin/bench ./cmd/bench"
fi

exit "$WATCHED"
