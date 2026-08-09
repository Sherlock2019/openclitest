#!/usr/bin/env python3
"""Emit every openCenter command, per environment, as JSON the console reads.

    python3 scripts/generate-commands-json.py [binary] > config/commands.json

Same source as COMMANDS.md — the binary itself — so the page and the document
can never disagree. Each entry is ready to run as written.
"""
import json
import os
import re
import subprocess
import sys

BINARY = sys.argv[1] if len(sys.argv) > 1 else os.environ.get(
    "OPENCLI_BIN", "/home/dzoan/opencli-benchmark/openCenter-cli/bin/opencenter")

ORG = "testbench"

ENVIRONMENTS = [
    {"id": "openstack", "name": "OpenStack", "cluster": "tb-openstack",
     "detail": "Configuration and generation work offline. Discovery, sync and online "
               "validation need credentials."},
    {"id": "vmware", "name": "VMware", "cluster": "tb-vmware",
     "detail": "vSphere. Configuration and generation work offline; the rest needs a vCenter."},
    {"id": "baremetal", "name": "Bare metal", "cluster": "tb-baremetal",
     "detail": "Physical hosts from an inventory. No cloud API, so configuration and generation."},
    {"id": "kind", "name": "Kind", "cluster": "tb-kind",
     "detail": "Local Kubernetes in containers. The whole lifecycle runs here for nothing."},
]

STAGE_ORDER = ["init", "configure", "validate", "generate", "deploy", "operate", "teardown"]
STAGES = {
    "init": "init", "import": "init", "scan": "init", "apply": "init", "report": "init",
    "configure": "configure", "set": "configure", "edit": "configure", "pool": "configure",
    "service": "configure", "normalize": "configure", "migrate-layout": "configure",
    "add": "configure", "remove": "configure", "update": "configure", "scale": "configure",
    "enable": "configure", "disable": "configure", "options": "configure",
    "validate": "validate", "doctor": "validate", "check": "validate",
    "generate": "generate", "sync": "generate", "encrypt": "generate",
    "deploy": "deploy", "bootstrap": "deploy",
    "destroy": "teardown", "unlock": "teardown", "reset": "teardown",
}

MUTATING = {"deploy", "destroy", "bootstrap", "reconcile", "restore", "apply", "push", "rotate"}

NEEDS = {
    "sync": ["cloud credentials"], "login": ["cloud credentials"],
    "deploy": ["the provider"], "destroy": ["the provider"],
    "encrypt": ["sops", "age"], "decrypt": ["sops", "age"], "rotate": ["sops", "age"],
    "scan": ["git"], "reconcile": ["kubectl"], "status": [],
}

PROVIDER_ONLY = {
    "cluster sync openstack": ["openstack"],
    "cluster sync": ["openstack"],
    # The current CLI has guided configuration for these providers, but not
    # Kind. Advertising the Kind invocation produces a guaranteed failure.
    "cluster configure": ["openstack", "vmware", "baremetal"],
    # Drift's provider factory currently implements VMware only.
    "cluster drift detect": ["vmware"],
    "cluster drift reconcile": ["vmware"],
    "cluster drift schedule": ["vmware"],
}

SECOND_ARGUMENT = {
    "cluster backup delete": "BACKUP_ID", "cluster backup restore": "BACKUP_ID",
    "cluster pool remove": "workers", "cluster pool update": "workers --count 3",
    "cluster pool scale": "workers --count 3",
    "cluster pool add": "workers --count 2 --flavor m1.medium",
    "cluster service enable": "cert-manager --param email=admin@example.com",
    "cluster service disable": "cert-manager",
    "cluster service options": "loki", "cluster set": "opencenter.meta.env=dev",
    "cluster lock": "--reason testing", "cluster unlock": "--reason testing",
    "cluster backup schedule": "--interval 24h", "cluster drift schedule": "--interval 24h",
    "cluster migrate-layout": "--org testbench --dry-run",
    # A session-scoped selection dies with the child process. The bench runs
    # one process per click, so persistence is required for later commands.
    "cluster use": "--persistent",
}

STANDALONE = {
    "secrets get": "SECRET_NAME --show", "secrets delete": "SECRET_NAME",
    "secrets describe": "SECRET_NAME",
    "secrets set": "SECRET_NAME --from-file /dev/null",
    "secrets keys revoke": "--cluster CLUSTER --key AGE_KEY_FINGERPRINT --dry-run",
    "secrets keys rotate": "--cluster CLUSTER --type age",
    "settings get": "logging.level", "settings set": "logging.level debug",
    "cluster import scan": "--repo https://github.com/your-org/your-cluster.git",
    "cluster import report": "--repo https://github.com/your-org/your-cluster.git",
    "cluster import apply": "--repo https://github.com/your-org/your-cluster.git",
}

_help_cache = {}


def help_of(path):
    key = tuple(path)
    if key not in _help_cache:
        try:
            result = subprocess.run([BINARY] + list(path) + ["--help"],
                                    capture_output=True, text=True, timeout=30)
            _help_cache[key] = result.stdout
        except Exception:
            _help_cache[key] = ""
    return _help_cache[key]


def children(path):
    out, inside = [], False
    for line in help_of(path).splitlines():
        stripped = line.strip()
        if stripped == "Available Commands:":
            inside = True
            continue
        if not inside:
            continue
        if not stripped:
            break
        parts = stripped.split()
        if len(parts) < 2 or parts[0] in ("completion", "help") or "external plugin" in stripped:
            continue
        out.append(parts[0])
    return out


def usage_of(path):
    lines = help_of(path).splitlines()
    for index, line in enumerate(lines):
        if line.strip() == "Usage:":
            for candidate in lines[index + 1:]:
                if candidate.strip():
                    return candidate.strip()
    return ""


def short_of(path):
    for line in help_of(path).splitlines():
        if line.strip():
            return line.strip()
    return ""


def walk():
    commands = []

    def visit(path, depth):
        if depth > 3:
            return
        for child in children(path):
            full = path + [child]
            commands.append(full)
            visit(full, depth + 1)

    visit([], 0)
    return commands


def stage_of(path):
    for part in reversed(path):
        if part in STAGES:
            return STAGES[part]
    return "operate"


def ready_line(path, cluster, provider):
    joined = " ".join(path)
    reference = f"{ORG}/{cluster}"

    if joined == "cluster init":
        return (f"cluster init {cluster} --org {ORG} --type {provider} "
                "--no-keygen --no-sops-keygen")
    if joined in STANDALONE:
        extra = STANDALONE[joined].replace("CLUSTER", reference)
        return f"{joined} {extra}".strip()
    if children(path):
        return f"{joined} --help"

    # Backup create currently documents and accepts a bare cluster name while
    # rejecting org/cluster as an invalid name. Keep this line runnable until
    # that CLI inconsistency is resolved.
    if joined == "cluster backup create":
        return f"{joined} {cluster}"

    takes_cluster = bool(re.search(r"\[name\]|\[cluster(?:-[^]]+)?\]|<cluster>|\[org/cluster\]",
                                   usage_of(path)))
    takes_cluster_flag = bool(re.search(r"(?m)^\s+--cluster(?:\s|$)", help_of(path)))
    suffix = f" --cluster {reference}" if takes_cluster_flag else ""
    if joined in SECOND_ARGUMENT:
        extra = SECOND_ARGUMENT[joined]
        line = f"{joined} {reference} {extra}" if takes_cluster else f"{joined} {extra}"
        return (line + suffix).strip()
    if takes_cluster:
        return f"{joined} {reference}"
    return (joined + suffix).strip()


def main():
    commands = walk()
    version = subprocess.run([BINARY, "version"], capture_output=True, text=True).stdout
    version = version.splitlines()[0] if version.strip() else "unknown"

    out = {"binary": BINARY, "version": version, "org": ORG,
           "stage_order": STAGE_ORDER, "environments": [], "total_commands": len(commands)}

    for environment in ENVIRONMENTS:
        entries = []
        for path in commands:
            joined = " ".join(path)
            only = PROVIDER_ONLY.get(joined)
            if only and environment["id"] not in only:
                continue

            leaf = path[-1]
            needs = list(NEEDS.get(leaf, []))
            mutating = leaf in MUTATING

            entries.append({
                "id": joined,
                "name": leaf,
                "task": path[0],
                "stage": stage_of(path),
                "short": short_of(path),
                "usage": usage_of(path),
                "needs": needs,
                "mutating": mutating,
                "is_group": bool(children(path)),
                "ready": ready_line(path, environment["cluster"], environment["id"]),
            })

        out["environments"].append({
            **environment,
            "fixture": (f"cluster init {environment['cluster']} --org {ORG} "
                        f"--type {environment['id']} --no-keygen --no-sops-keygen"),
            "commands": entries,
        })

    json.dump(out, sys.stdout, indent=2)
    print()


if __name__ == "__main__":
    main()
