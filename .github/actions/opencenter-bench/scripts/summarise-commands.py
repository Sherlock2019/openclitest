#!/usr/bin/env python3
"""Summarise config/commands.json: how many commands per environment and stage."""
import json
import sys
from pathlib import Path

path = Path(sys.argv[1] if len(sys.argv) > 1 else "config/commands.json")
data = json.loads(path.read_text())

print(f"{data['total_commands']} commands · {data['version']}")
for environment in data["environments"]:
    stages = {}
    tasks = {}
    for command in environment["commands"]:
        stages[command["stage"]] = stages.get(command["stage"], 0) + 1
        tasks[command["task"]] = tasks.get(command["task"], 0) + 1
    print(f"\n  {environment['name']}: {len(environment['commands'])} commands")
    print("    stages: " + ", ".join(
        f"{stage} {stages[stage]}" for stage in data["stage_order"] if stage in stages))
    print("    tasks:  " + ", ".join(f"{task} {count}" for task, count in sorted(tasks.items())))
