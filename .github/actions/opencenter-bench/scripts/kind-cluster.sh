#!/usr/bin/env bash
# Create, inspect and remove the test bench's own Kind cluster.
#
#   scripts/kind-cluster.sh up       create it if it is not there
#   scripts/kind-cluster.sh status   say whether it is there, and where
#   scripts/kind-cluster.sh down     remove it
#   scripts/kind-cluster.sh kubeconfig  print the path to its kubeconfig
#
# Design follows what was measured, not what openCenter defaults to
# (docs/kind-node-count.md):
#
#   - ONE node. Three nodes failed on this machine at either image version;
#     one node built in 34 seconds every time. One node exercises every
#     stage of the lifecycle.
#   - NOT port 6443. openCenter hardcodes it, so a second cluster collides
#     with the first and reports a raw docker "port is already allocated".
#     A free port is chosen here and written into the kubeconfig.
#   - Its own name, and deletion only ever by that name. Any other cluster
#     on this machine belongs to someone else.
set -uo pipefail
cd "$(dirname "$0")/.."

# kind is not on PATH in a non-login shell.
export PATH="$HOME/.local/bin:$PATH"

CLUSTER="${OPENCLI_KIND_CLUSTER:-opencli-testbench}"
STATE_DIR="${OPENCLI_KIND_STATE:-$HOME/.cache/opencli-testbench}"
KUBECONFIG_PATH="$STATE_DIR/kubeconfig"

die() { echo "$*" >&2; exit 1; }

need_kind() {
  command -v kind >/dev/null || die "kind is not installed. Put it on PATH, or install it from https://kind.sigs.k8s.io"
  command -v docker >/dev/null || die "docker is not installed, and kind needs it."
  docker info >/dev/null 2>&1 || die "docker is installed but not responding. Is the daemon running?"
}

exists() { kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; }

# A free port, so a cluster already holding 6443 is not disturbed.
free_port() {
  local port
  for port in $(seq 6450 6520); do
    if ! (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
      echo "$port"; return 0
    fi
    exec 3>&- 2>/dev/null
  done
  die "no free port between 6450 and 6520"
}

# Refuse to build a cluster that would fail, and say why rather than letting
# it time out for four minutes. This is the finding from docs/kind-node-count.md
# turned into a precondition.
preflight() {
  local instances running
  instances=$(cat /proc/sys/fs/inotify/max_user_instances 2>/dev/null || echo 0)
  running=$(docker ps --filter 'ancestor=kindest/node' -q 2>/dev/null | wc -l)

  echo "  inotify instances: $instances (kind asks for 512)"
  echo "  kind nodes already running: $running"

  if [[ "$instances" -lt 256 && "$running" -ge 3 ]]; then
    echo
    echo "  WARNING: $running kind nodes are already running and"
    echo "  fs.inotify.max_user_instances is only $instances. A single node is"
    echo "  usually still fine, but if this hangs at 'waiting for the kubelet'"
    echo "  the fix is:  sudo sysctl -w fs.inotify.max_user_instances=512"
    echo
  fi
}

up() {
  need_kind
  mkdir -p "$STATE_DIR"

  if exists; then
    echo "already running: $CLUSTER"
    status
    return 0
  fi

  preflight

  local port; port=$(free_port)
  echo "creating $CLUSTER — one node, API server on 127.0.0.1:$port"

  cat >"$STATE_DIR/kind.yaml" <<YAML
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  apiServerAddress: "127.0.0.1"
  apiServerPort: $port
nodes:
  - role: control-plane
YAML

  local start; start=$(date +%s)
  if ! kind create cluster \
      --name "$CLUSTER" \
      --config "$STATE_DIR/kind.yaml" \
      --kubeconfig "$KUBECONFIG_PATH" \
      --wait 180s; then
    echo
    echo "creation failed after $(( $(date +%s) - start ))s." >&2
    echo "Check fs.inotify.max_user_instances (currently $(cat /proc/sys/fs/inotify/max_user_instances 2>/dev/null)) —" >&2
    echo "kind asks for 512. See docs/kind-node-count.md." >&2
    # Leave nothing half-built behind.
    kind delete cluster --name "$CLUSTER" >/dev/null 2>&1
    return 1
  fi

  echo "ready in $(( $(date +%s) - start ))s"
  status
}

status() {
  command -v kind >/dev/null || { echo "kind is not installed"; return 1; }
  if ! exists; then
    echo "not running: $CLUSTER"
    return 1
  fi
  echo "running:    $CLUSTER"
  echo "kubeconfig: $KUBECONFIG_PATH"
  if command -v kubectl >/dev/null && [[ -f "$KUBECONFIG_PATH" ]]; then
    local nodes
    nodes=$(KUBECONFIG="$KUBECONFIG_PATH" kubectl get nodes --no-headers 2>/dev/null | wc -l)
    echo "nodes:      $nodes"
  fi
  return 0
}

down() {
  need_kind
  if ! exists; then
    echo "not running: $CLUSTER — nothing to remove"
    return 0
  fi
  # Always by name. Never a bare delete, never a loop over every cluster:
  # anything else on this machine belongs to someone else.
  echo "removing $CLUSTER"
  kind delete cluster --name "$CLUSTER"
  rm -f "$KUBECONFIG_PATH" "$STATE_DIR/kind.yaml"
  echo "remaining clusters (untouched): $(kind get clusters 2>/dev/null | tr '\n' ' ')"
}

case "${1:-status}" in
  up)         up ;;
  down)       down ;;
  status)     status ;;
  kubeconfig) echo "$KUBECONFIG_PATH" ;;
  *)          die "usage: $0 [up|down|status|kubeconfig]" ;;
esac
