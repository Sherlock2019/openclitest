#!/usr/bin/env python3
"""Print what the v2 schema allows under `secrets`.

Used to decide whether `secrets.etcd-backup` is a field the schema already
knows about (so the Go struct is missing it) or a path the sync command
invented (so the sync command is wrong).
"""
import json
import sys
from pathlib import Path


def find_secrets(node, path=()):
    if isinstance(node, dict):
        properties = node.get("properties")
        if isinstance(properties, dict) and path and path[-1] == "secrets":
            yield path, sorted(properties.keys())
        for key, value in node.items():
            step = path if key in ("properties", "definitions", "$defs") else path + (key,)
            yield from find_secrets(value, step)
    elif isinstance(node, list):
        for value in node:
            yield from find_secrets(value, path)


def main() -> int:
    schema = json.loads(Path(sys.argv[1]).read_text())
    for path, keys in find_secrets(schema):
        print("/".join(path))
        for key in keys:
            print("   ", key)
        print()
    return 0


if __name__ == "__main__":
    sys.exit(main())
