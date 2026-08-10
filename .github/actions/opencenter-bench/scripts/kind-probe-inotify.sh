#!/usr/bin/env bash
# Prove the inotify limit is the cause by raising it and retrying the exact
# configuration that failed.
#
#   one node   -> works
#   three nodes -> fails, at any image version
#   fs.inotify.max_user_instances = 128, with three nodes already running
#
# kind's own troubleshooting guide names this value. If raising it makes the
# three-node cluster build, the cause is settled. The change is applied with
# sysctl, which does not survive a reboot, so nothing is left altered.
set -uo pipefail
export PATH="$HOME/.local/bin:$PATH"

before=$(cat /proc/sys/fs/inotify/max_user_instances)
echo "max_user_instances is currently $before"

if ! sudo -n true 2>/dev/null; then
  echo
  echo "sudo needs a password here, so this cannot be proven automatically."
  echo "To confirm it by hand:"
  echo "    sudo sysctl -w fs.inotify.max_user_instances=512"
  echo "then retry the three-node cluster."
  exit 0
fi

sudo sysctl -w fs.inotify.max_user_instances=512 >/dev/null
echo "raised to $(cat /proc/sys/fs/inotify/max_user_instances)"

name="probe4-$$"
cat >/tmp/kind-probe4.yaml <<'YAML'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  apiServerAddress: "127.0.0.1"
  apiServerPort: 6449
  podSubnet: "10.244.0.0/16"
  serviceSubnet: "10.96.0.0/16"
nodes:
  - role: control-plane
    image: "kindest/node:v1.35.0"
  - role: worker
    image: "kindest/node:v1.35.0"
  - role: worker
    image: "kindest/node:v1.35.0"
YAML

echo
echo "== the same three-node cluster that failed twice =="
start=$(date +%s)
kind create cluster --name "$name" --config /tmp/kind-probe4.yaml --wait 240s >/tmp/kind-probe4.log 2>&1
result=$?
echo "elapsed: $(( $(date +%s) - start ))s, exit $result"
[[ $result -ne 0 ]] && grep -E 'error execution phase|failed while waiting|could not' /tmp/kind-probe4.log | head -3

kind delete cluster --name "$name" >/dev/null 2>&1
sudo sysctl -w fs.inotify.max_user_instances="$before" >/dev/null
echo "restored max_user_instances to $(cat /proc/sys/fs/inotify/max_user_instances)"

echo
echo "== clusters (must be mockbank only) =="
kind get clusters

echo
if [[ $result -eq 0 ]]; then
  echo "PROVEN: fs.inotify.max_user_instances=128 was the cause. At 512 the"
  echo "        three-node cluster openCenter asks for builds without error."
else
  echo "NOT the whole story: it still fails at 512. Something else is also"
  echo "        involved."
fi
