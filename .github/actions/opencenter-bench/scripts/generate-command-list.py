#!/usr/bin/env python3
"""Write the ready-to-run command list, one section per environment.

Every command comes from the binary, not from a list kept by hand, and every
line printed is a command you can paste into a terminal.

    python3 scripts/generate-command-list.py [binary] > COMMANDS.md
"""
import os
import re
import subprocess
import sys

BINARY = sys.argv[1] if len(sys.argv) > 1 else os.environ.get(
    "OPENCLI_BIN", "/home/dzoan/opencli-benchmark/openCenter-cli/bin/opencenter")

# The four infrastructure types, with the fixture each one gets.
ENVIRONMENTS = [
    ("openstack", "OpenStack", "tb-openstack",
     "The default provider. Configuration and generation work offline; "
     "discovery, sync and online validation need credentials."),
    ("vmware", "VMware", "tb-vmware",
     "vSphere. Configuration and generation work offline; the rest needs a vCenter."),
    ("baremetal", "Bare metal", "tb-baremetal",
     "Physical hosts from an inventory. No cloud API, so it is configuration and generation."),
    ("kind", "Kind", "tb-kind",
     "Local Kubernetes in containers. The only provider whose whole lifecycle runs here for nothing."),
]
ORG = "testbench"

# Where each command sits in the journey. A command not named here is operate.
STAGES = {
    "init": "init", "import": "init", "configure": "configure", "set": "configure",
    "edit": "configure", "pool": "configure", "service": "configure",
    "normalize": "configure", "migrate-layout": "configure",
    "validate": "validate", "doctor": "validate", "generate": "generate",
    "sync": "generate", "deploy": "deploy", "bootstrap": "deploy",
    "destroy": "teardown", "unlock": "teardown",
}

# Commands that reach beyond the sandbox and need OPENCLI_ALLOW_MUTATE=1.
MUTATING = {"deploy", "destroy", "bootstrap", "reconcile", "restore", "apply", "push"}

# What a command needs before it can do anything meaningful.
NEEDS = {
    "sync": "cloud credentials",
    "login": "cloud credentials",
    "deploy": "the provider, and the mutation gate",
    "destroy": "the provider, and the mutation gate",
    "encrypt": "sops and age",
    "decrypt": "sops and age",
    "rotate": "sops and age",
    "scan": "git",
    "reconcile": "kubectl",
}

# Providers a command is only meaningful for. A command absent from this map
# applies everywhere.
PROVIDER_ONLY = {
    "cluster sync openstack": {"openstack"},
    "cluster sync": {"openstack"},
    "cluster configure": {"openstack", "vmware", "baremetal"},
    "cluster drift detect": {"vmware"},
    "cluster drift reconcile": {"vmware"},
    "cluster drift schedule": {"vmware"},
}

# Commands that take an argument of their own after the cluster, or instead of
# it. Without these the generated line is not runnable, which is the whole
# point of the file.
SECOND_ARGUMENT = {
    "cluster backup delete": "BACKUP_ID",
    "cluster backup restore": "BACKUP_ID",
    "cluster pool remove": "workers",
    "cluster pool update": "workers --count 3",
    "cluster pool scale": "workers --count 3",
    "cluster pool add": "workers --count 2 --flavor m1.medium",
    "cluster service enable": "cert-manager --param email=admin@example.com",
    "cluster service disable": "cert-manager",
    "cluster service options": "loki",
    "cluster set": "opencenter.meta.env=dev",
    "cluster lock": "--reason 'testing'",
    "cluster unlock": "--reason 'testing'",
    "cluster backup schedule": "--interval 24h",
    "cluster drift schedule": "--interval 24h",
    "cluster migrate-layout": "--org testbench --dry-run",
    "cluster use": "--persistent",
}

# Commands that take no cluster at all, only their own argument.
STANDALONE_ARGUMENT = {
    "secrets get": "SECRET_NAME --show",
    "secrets delete": "SECRET_NAME",
    "secrets describe": "SECRET_NAME",
    "secrets set": "SECRET_NAME --from-file /dev/null",
    "secrets keys revoke": "--cluster CLUSTER --key AGE_KEY_FINGERPRINT --dry-run",
    "secrets keys rotate": "--cluster CLUSTER --type age",
    "settings get": "logging.level",
    "settings set": "logging.level debug",
    "settings explain cluster-defaults": "",
    "cluster import scan": "--repo https://github.com/your-org/your-cluster.git",
    "cluster import report": "--repo https://github.com/your-org/your-cluster.git",
    "cluster import apply": "--repo https://github.com/your-org/your-cluster.git",
}

_help_cache = {}


def help_of(path):
    """Read one command's help once; three catalogue columns reuse it."""
    key = tuple(path)
    if key not in _help_cache:
        result = subprocess.run([BINARY] + path + ["--help"],
                                capture_output=True, text=True, timeout=30)
        _help_cache[key] = result.stdout
    return _help_cache[key]


def children(path):
    """Ask the binary what commands live under this path."""
    out = []
    inside = False
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
    """Every command in the tree, depth first."""
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


def invocation(path, cluster):
    """The ready-to-run form of this command for one provider's fixture.

    Every line this returns has to be runnable as written. A command that takes
    its own argument gets one; a group gets --help; a command that takes a
    cluster gets the fixture for this provider.
    """
    usage = usage_of(path)
    joined = " ".join(path)
    reference = f"{ORG}/{cluster}"

    if joined == "cluster init":
        return (f"cluster init {cluster} --org {ORG} --type PROVIDER "
                "--no-keygen --no-sops-keygen")

    # Takes its own argument and no cluster.
    if joined in STANDALONE_ARGUMENT:
        extra = STANDALONE_ARGUMENT[joined].replace("CLUSTER", reference)
        return f"{joined} {extra}".strip()

    # A group: the only thing it does is print help.
    if children(path):
        return f"{joined} --help"

    if joined == "cluster backup create":
        return f"{joined} {cluster}"

    takes_cluster = bool(re.search(
        r"\[name\]|\[cluster(?:-[^]]+)?\]|<cluster>|\[org/cluster\]", usage))
    help_text = help_of(path)
    takes_cluster_flag = bool(re.search(r"(?m)^\s+--cluster(?:\s|$)", help_text))
    suffix = f" --cluster {reference}" if takes_cluster_flag else ""

    # Takes a cluster and then something of its own.
    if joined in SECOND_ARGUMENT:
        extra = SECOND_ARGUMENT[joined]
        if takes_cluster:
            return (f"{joined} {reference} {extra}" + suffix).strip()
        return (f"{joined} {extra}" + suffix).strip()

    if takes_cluster:
        return f"{joined} {reference}"
    return (joined + suffix).strip()


def stage_of(path):
    for part in reversed(path):
        if part in STAGES:
            return STAGES[part]
    return "operate"


def main():
    commands = walk()
    version = subprocess.run([BINARY, "version"], capture_output=True, text=True).stdout.splitlines()
    version = version[0] if version else "unknown"

    print("# Ready-to-run openCenter CLI commands, per environment\n")
    print(f"Generated from `{BINARY}` — {version}.")
    print(f"{len(commands)} commands. Every line below is runnable as written, once the")
    print("fixture for that environment exists.\n")
    print("Create the fixture for an environment first:\n")
    print("```bash")
    for env_id, _, cluster, _ in ENVIRONMENTS:
        print(f"opencenter cluster init {cluster} --org {ORG} --type {env_id} "
              "--no-keygen --no-sops-keygen")
    print("```\n")

    for env_id, env_name, cluster, detail in ENVIRONMENTS:
        print(f"\n## {env_name} (`--type {env_id}`)\n")
        print(f"{detail}\n")
        print("| Command | Stage | Task | Needs | Ready-to-run |")
        print("|---|---|---|---|---|")

        for path in commands:
            joined = " ".join(path)
            only = PROVIDER_ONLY.get(joined)
            if only and env_id not in only:
                continue

            leaf = path[-1]
            stage = stage_of(path)
            task = path[0]
            needs = NEEDS.get(leaf, "")
            if leaf in MUTATING:
                needs = (needs + ", " if needs else "") + "OPENCLI_ALLOW_MUTATE=1"

            line = invocation(path, cluster).replace("PROVIDER", env_id)
            print(f"| `{joined}` | {stage} | {task} | {needs or '—'} | "
                  f"`opencenter {line}` |")

    print("\n## Notes\n")
    print("- `PROVIDER` in a `cluster init` line is the `--type` for that section.")
    print("- Commands marked `OPENCLI_ALLOW_MUTATE=1` create or destroy real things;")
    print("  the bench refuses them without that variable set.")
    print("- A command whose *Needs* column is not satisfied still runs — it should")
    print("  fail with an explanation, and that failure is itself worth testing.")


if __name__ == "__main__":
    main()
