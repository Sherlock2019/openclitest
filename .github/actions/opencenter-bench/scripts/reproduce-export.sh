#!/usr/bin/env bash
# Reproduce, outside the bench, the export finding from Module 20: after which
# step of the journey does `cluster export` stop producing valid YAML?
#
# Kept because a finding nobody can reproduce by hand is a finding nobody will
# act on.
set -uo pipefail
B="${OPENCLI_BIN:?set OPENCLI_BIN to the opencenter binary}"
R="$(mktemp -d /tmp/export-repro-XXXXXX)"
CHECKER="$(mktemp /tmp/yamlcheck-XXXXXX.py)"
trap 'rm -rf "$R" "$CHECKER"' EXIT

cat >"$CHECKER" <<'PY'
import sys, yaml

path = sys.argv[1]
with open(path) as handle:
    text = handle.read()

try:
    yaml.safe_load(text)
    print("valid YAML")
except yaml.YAMLError as error:
    mark = getattr(error, "problem_mark", None)
    print("INVALID:", getattr(error, "problem", error))
    if mark:
        lines = text.splitlines()
        for number in range(max(0, mark.line - 3), min(len(lines), mark.line + 3)):
            flag = ">>" if number == mark.line else "  "
            print(f"   {flag} {number + 1:>4}: {lines[number]!r}")
    sys.exit(1)
PY

export HOME="$R/home" OPENCENTER_CONFIG_DIR="$R/cfg" OPENCENTER_STATE_DIR="$R/state"
export XDG_CONFIG_HOME="$R/xdg" NO_COLOR=1 TERM=dumb
mkdir -p "$HOME" "$OPENCENTER_CONFIG_DIR" "$OPENCENTER_STATE_DIR" "$R/work"
cd "$R/work"

"$B" cluster init az-test --org az-test-org --type kind --no-keygen --no-sops-keygen >/dev/null

for step in init use validate generate normalize; do
  case "$step" in
    use)       "$B" cluster use az-test-org/az-test >/dev/null 2>&1 ;;
    validate)  "$B" cluster validate az-test-org/az-test >/dev/null 2>&1 ;;
    generate)  "$B" cluster generate az-test-org/az-test >/dev/null 2>&1 ;;
    normalize) "$B" cluster normalize az-test-org/az-test >/dev/null 2>&1 ;;
  esac
  "$B" cluster export az-test-org/az-test > "$R/export.yaml" 2>/dev/null
  printf 'after %-10s %4s lines  ' "$step" "$(wc -l < "$R/export.yaml")"
  python3 "$CHECKER" "$R/export.yaml" || exit 1
done
