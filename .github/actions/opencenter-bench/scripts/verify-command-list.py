#!/usr/bin/env python3
"""Execute every ready-to-run line in COMMANDS.md and report what happened.

    python3 scripts/verify-command-list.py [openstack|vmware|baremetal|kind]

"Ready to run" is a claim. This is the check on it: each line is taken from the
file and executed against the real binary in a throwaway sandbox. A line that
cannot run is a defect in the list, not in the CLI.

A non-zero exit is not automatically a defect — several lines use placeholders
(BACKUP_ID, SECRET_NAME) or need credentials, and failing clearly is correct
behaviour. A command that hangs always is.
"""
import os
import re
import shlex
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SECTION = (sys.argv[1] if len(sys.argv) > 1 else "kind").lower()
BINARY = os.environ.get("OPENCLI_BIN",
                        "/home/dzoan/opencli-benchmark/openCenter-cli/bin/opencenter")

ROW = re.compile(r"^\|\s*`([^`]+)`\s*\|.*\|\s*`([^`]+)`\s*\|\s*$")


def lines_for(section):
    """The ready-to-run column of one section, in order."""
    inside = False
    found = []
    for line in (ROOT / "COMMANDS.md").read_text().splitlines():
        if line.startswith("## "):
            inside = section in line.lower()
            continue
        if not inside:
            continue
        match = ROW.match(line)
        if match:
            found.append((match.group(1), match.group(2)))
    return found


def main():
    if not Path(BINARY).exists():
        print(f"no binary at {BINARY}", file=sys.stderr)
        return 2

    rows = lines_for(SECTION)
    if not rows:
        print(f"no rows found for section {SECTION!r}", file=sys.stderr)
        return 2

    work = tempfile.mkdtemp(prefix="verify-list-")
    env = {
        "HOME": f"{work}/home",
        "OPENCENTER_CONFIG_DIR": f"{work}/cfg",
        "OPENCENTER_STATE_DIR": f"{work}/state",
        "XDG_CONFIG_HOME": f"{work}/xdg",
        "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
        "NO_COLOR": "1", "TERM": "dumb",
        # An editor or pager with no terminal would block for ever.
        "EDITOR": "true", "VISUAL": "true", "PAGER": "cat",
    }
    for directory in (env["HOME"], env["OPENCENTER_CONFIG_DIR"],
                      env["OPENCENTER_STATE_DIR"], f"{work}/work"):
        os.makedirs(directory, exist_ok=True)

    print(f"verifying the {SECTION} section — {len(rows)} lines — against {BINARY}\n")

    ok = failed = hung = 0
    problems = []

    for command, ready in rows:
        args = shlex.split(ready)
        if args and args[0] == "opencenter":
            args = args[1:]

        try:
            result = subprocess.run([BINARY] + args, capture_output=True, text=True,
                                    timeout=25, cwd=f"{work}/work", env=env)
            code = result.returncode
            output = (result.stdout + result.stderr).strip()
        except subprocess.TimeoutExpired:
            code, output = 124, "did not return within 25s"

        if code == 0:
            ok += 1
            verdict = "ok"
        elif code == 124:
            hung += 1
            verdict = "HUNG"
            problems.append((command, ready, "hangs"))
        else:
            failed += 1
            verdict = f"exit {code}"
            first = output.splitlines()[0] if output else "(no output)"
            if not output:
                problems.append((command, ready, "failed silently"))
            elif "panic" in output or "goroutine " in output:
                problems.append((command, ready, "panicked: " + first[:120]))

        print(f"  {verdict:<9} opencenter {' '.join(args)}")

    print(f"\n  {len(rows)} lines · {ok} succeeded · {failed} failed · {hung} hung")

    if problems:
        print(f"\n  defects ({len(problems)}):")
        for command, ready, why in problems:
            print(f"    {command}: {why}")
            print(f"      opencenter {ready}")
        return 1

    print("\n  no line hangs, fails silently or panics.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
