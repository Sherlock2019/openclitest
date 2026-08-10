#!/usr/bin/env bash
# Make every shell script executable, on disk and in git.
#
# Editing a file through the Windows share (\\wsl$\...) rewrites it with mode
# 644, so a script that was executable yesterday is "Permission denied" today.
# That is how ./start.sh stopped working. Nothing here depends on the execute
# bit any more — every script is invoked as `bash script.sh` — but a person
# typing ./start.sh should not have to know that.
#
#   scripts/fix-exec-bits.sh          fix them
#   scripts/fix-exec-bits.sh --check  report, change nothing, exit 1 if wrong
set -uo pipefail
cd "$(dirname "$0")/.."

check_only=0
[[ "${1:-}" == "--check" ]] && check_only=1

wrong=0
while IFS= read -r file; do
  # git ls-files -s prints the mode; 100644 means not executable.
  mode=$(git ls-files -s -- "$file" 2>/dev/null | cut -d' ' -f1)
  disk_ok=1
  [[ -x "$file" ]] || disk_ok=0

  if [[ "$mode" == "100644" || $disk_ok -eq 0 ]]; then
    wrong=$((wrong + 1))
    if [[ $check_only -eq 1 ]]; then
      echo "  not executable: $file"
    else
      chmod +x "$file"
      # Tell git too, or the next clone gets 644 again.
      git update-index --chmod=+x -- "$file" 2>/dev/null || true
      echo "  fixed: $file"
    fi
  fi
done < <(git ls-files '*.sh' 2>/dev/null)

if [[ $wrong -eq 0 ]]; then
  echo "  every script is executable"
  exit 0
fi
if [[ $check_only -eq 1 ]]; then
  echo "  $wrong script(s) are not executable — run scripts/fix-exec-bits.sh"
  exit 1
fi
echo "  fixed $wrong script(s)"
