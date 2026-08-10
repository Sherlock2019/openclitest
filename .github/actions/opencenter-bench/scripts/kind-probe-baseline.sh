#!/usr/bin/env bash
# Is the kubelet failure openCenter's, or this machine's?
#
# openCenter's Kind deploy reached control-plane init and then died on
# "waiting for the kubelet to start ... context deadline exceeded". That was
# called "probably environmental, unproven". This proves it either way, by
# asking plain kind to build a cluster with no openCenter involved at all.
# If plain kind fails the same way, the machine is at fault. If it succeeds,
# openCenter is.
set -uo pipefail

# kind is not on PATH in a non-login shell here.
export PATH="$HOME/.local/bin:$PATH"

echo "== the machine =="
echo "kernel:  $(uname -r)"
echo "cgroups: $(stat -fc %T /sys/fs/cgroup 2>/dev/null)"
echo "memory:  $(free -m | awk '/^Mem:/{print $2" MB total, "$7" MB available"}')"
command -v kind >/dev/null && echo "kind:    $(kind version 2>&1 | head -1)" || echo "kind:    NOT INSTALLED"
command -v docker >/dev/null && echo "docker:  $(docker --version)" || echo "docker:  NOT INSTALLED"
echo "docker cgroup driver: $(docker info 2>/dev/null | awk -F': ' '/Cgroup Driver/{print $2}')"
echo "docker cgroup version: $(docker info 2>/dev/null | awk -F': ' '/Cgroup Version/{print $2}')"

echo
echo "== clusters already here (must not be touched) =="
kind get clusters 2>&1

echo
if ! command -v kind >/dev/null; then
  echo "kind is not installed, so this cannot be settled here."
  exit 0
fi

name="probe-$$"
echo "== plain kind, no openCenter: $name on port 6445 =="
cat >/tmp/kind-probe.yaml <<YAML
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  apiServerAddress: "127.0.0.1"
  apiServerPort: 6445
nodes:
  - role: control-plane
YAML

start=$(date +%s)
kind create cluster --name "$name" --config /tmp/kind-probe.yaml --wait 300s 2>&1 | tail -25
result=${PIPESTATUS[0]}
echo "elapsed: $(( $(date +%s) - start ))s, exit $result"

echo
echo "== cleaning up the probe =="
kind delete cluster --name "$name" 2>&1 | tail -3

echo
echo "== clusters after (must match the list above) =="
kind get clusters 2>&1

echo
if [[ $result -eq 0 ]]; then
  echo "VERDICT: plain kind works here. The kubelet failure is openCenter's,"
  echo "         not the machine's."
else
  echo "VERDICT: plain kind fails here too, with no openCenter involved."
  echo "         The kubelet failure is environmental."
fi
