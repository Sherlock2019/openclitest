#!/usr/bin/env python3
"""Add `Args: cobra.NoArgs` to command groups that only print help.

A group whose RunE prints help but accepts any argument reports a typo as
success: `opencenter secrets encryptt` runs the group, prints help and exits 0.
`cmd/cluster.go` already does this correctly; this brings the rest into line.

The root command is left alone: Cobra restricts unknown arguments there itself,
and it already fails correctly.

    python3 fix-missing-noargs.py <openCenter-cli checkout> [--apply]
"""
import re
import sys
from pathlib import Path

SKIP = {"root.go"}
RUNE = re.compile(r"^(\s*)RunE: func\(cmd \*cobra\.Command, args \[\]string\) error \{\s*$")


def fix(path: Path) -> str | None:
    lines = path.read_text().splitlines(keepends=True)

    for index, line in enumerate(lines):
        match = RUNE.match(line)
        if not match:
            continue
        # Only the group's own help-printing RunE.
        if index + 1 >= len(lines) or "return cmd.Help()" not in lines[index + 1]:
            continue
        # Already restricted?
        window = "".join(lines[max(0, index - 12):index])
        if "Args:" in window:
            return None

        indent = match.group(1)
        lines.insert(index, f"{indent}Args: cobra.NoArgs,\n")
        return "".join(lines)
    return None


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__)
        return 2

    root = Path(sys.argv[1])
    apply = "--apply" in sys.argv
    changed = 0

    for path in sorted((root / "cmd").glob("*.go")):
        if path.name in SKIP or path.name.endswith("_test.go"):
            continue
        updated = fix(path)
        if updated is None:
            continue
        changed += 1
        print(f"  {'fixed' if apply else 'would fix'}  {path.relative_to(root)}")
        if apply:
            path.write_text(updated)

    print(f"\n  {changed} command group(s)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
