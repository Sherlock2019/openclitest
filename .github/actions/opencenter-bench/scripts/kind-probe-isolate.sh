#!/usr/bin/env bash
# Which of the two variables is it: the node image, or the node count?
#
#   probe 1: kind defaults (v1.36.1, 1 node)          -> worked, 34s
#   probe 2: openCenter's shape (v1.35.0, 3 nodes)    -> failed, 251s
#
# This runs v1.35.0 with a single control plane. Success means the node count
# or the resources it needs; failure means the pinned image.
set -uo pipefail
export PATH="$HOME/.local/bin:$PATH"

run_probe() {
  local label="$1" name="probe-$2-$$" port="$3" config="$4"
  echo
  echo "=== $label ==="
  printf '%s\n' "$config" >/tmp/kind-$2.yaml
  local start=$(date +%s)
  kind create cluster --name "$name" --config /tmp/kind-$2.yaml --wait 240s >/tmp/kind-$2.log 2>&1
  local result=$?
  local elapsed=$(( $(date +%s) - start ))
  if [[ $result -eq 0 ]]; then
    echo "  WORKED in ${elapsed}s"
  else
    echo "  FAILED in ${elapsed}s"
    grep -E 'error execution phase|failed while waiting|Unfortunately|could not|timed out' /tmp/kind-$2.log |
      head -4 | sed 's/^/    /'
  fi
  kind delete cluster --name "$name" >/dev/null 2>&1
  return $result
}

echo "== before =="
kind get clusters

run_probe "v1.35.0, ONE control plane, no workers" "v135solo" 6447 'kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  apiServerAddress: "127.0.0.1"
  apiServerPort: 6447
nodes:
  - role: control-plane
    image: "kindest/node:v1.35.0"'
solo=$?

run_probe "v1.36.1, THREE nodes (kind default image)" "v136three" 6448 'kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  apiServerAddress: "127.0.0.1"
  apiServerPort: 6448
nodes:
  - role: control-plane
  - role: worker
  - role: worker'
three=$?

echo
echo "== after (must match before) =="
kind get clusters

echo
echo "=== VERDICT ==="
if [[ $solo -ne 0 && $three -eq 0 ]]; then
  echo "The pinned image kindest/node:v1.35.0 is the problem on this machine."
  echo "openCenter defaults to Kubernetes 1.35.0 in internal/config/v2/defaults.go."
elif [[ $solo -eq 0 && $three -ne 0 ]]; then
  echo "The node count is the problem, not the image: three nodes fail whatever"
  echo "the version. openCenter defaults to 1 control plane + 2 workers."
elif [[ $solo -eq 0 && $three -eq 0 ]]; then
  echo "Both work alone, so it is the combination of the pinned image AND the"
  echo "three-node layout that openCenter asks for."
else
  echo "Both fail, so this machine cannot currently build a second kind cluster"
  echo "beside the one already running."
fi
