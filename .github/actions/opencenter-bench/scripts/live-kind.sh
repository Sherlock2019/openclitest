#!/usr/bin/env bash
# A real deployment: create a Kind cluster with the CLI, verify it from outside
# with kubectl, destroy it, and confirm it is actually gone.
#
#   bash scripts/live-kind.sh
#
# This creates real containers on this machine. It refuses to run without the
# gate, and it registers its cleanup before it creates anything, so an
# interrupted run still takes the cluster down.
set -uo pipefail
cd "$(dirname "$0")/.."

if [[ "${OPENCLI_ALLOW_MUTATE:-}" != "1" ]]; then
  echo "This creates a real Kubernetes cluster. Run it deliberately:" >&2
  echo "  OPENCLI_ALLOW_MUTATE=1 bash scripts/live-kind.sh" >&2
  exit 1
fi

export PATH="$HOME/.local/bin:$PATH"
BIN="${OPENCLI_BIN:-/home/dzoan/opencli-benchmark/openCenter-cli/bin/opencenter}"

for tool in kind kubectl docker; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed" >&2; exit 1; }
done
docker info >/dev/null 2>&1 || { echo "the docker daemon is not running" >&2; exit 1; }

ORG=livelab
CLUSTER="live-$(date +%s | tail -c 6)"
REF="$ORG/$CLUSTER"

R="$(mktemp -d /tmp/live-kind-XXXXXX)"
export HOME="$R/home" OPENCENTER_CONFIG_DIR="$R/cfg" OPENCENTER_STATE_DIR="$R/state"
export XDG_CONFIG_HOME="$R/xdg" NO_COLOR=1 TERM=dumb EDITOR=true PAGER=cat
mkdir -p "$HOME" "$OPENCENTER_CONFIG_DIR" "$OPENCENTER_STATE_DIR" "$R/work"

# Registered before anything is created, so an interrupt still cleans up.
cleanup() {
  echo
  echo "cleaning up..."
  "$BIN" cluster destroy "$REF" --force --break-lock >/dev/null 2>&1 || true
  kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  rm -rf "$R"
}
trap cleanup EXIT INT TERM

cd "$R/work"
status=0
step() { printf '\n== %s ==\n' "$1"; }
ok()   { printf '  ok    %s\n' "$1"; }
bad()  { printf '  FAIL  %s\n     %s\n' "$1" "$2"; status=1; }

echo "cluster $REF · binary $BIN"
echo "kind clusters before: $(kind get clusters 2>/dev/null | tr '\n' ' ')"

step "init"
if "$BIN" cluster init "$CLUSTER" --org "$ORG" --type kind --no-keygen --no-sops-keygen; then
  ok "configuration created"
else
  bad "cluster init" "see above"; exit 1
fi

step "avoid colliding with an existing cluster"
# openCenter's kind configuration defaults the API server to 127.0.0.1:6443.
# Any Kind cluster already on this machine holds that port, and the deploy then
# fails deep inside docker with "port is already allocated" rather than saying
# so. Pick a free one.
API_PORT=6443
while ss -ltn 2>/dev/null | grep -q ":$API_PORT "; do
  API_PORT=$(( API_PORT + 1 ))
done
if [[ "$API_PORT" != "6443" ]]; then
  echo "  6443 is taken; using $API_PORT"
  "$BIN" cluster set "$REF" "opencenter.infrastructure.kind.api_server_port=$API_PORT" \
    >/dev/null 2>&1 && ok "api_server_port set to $API_PORT" \
    || bad "could not change the API port" "the deploy will collide"
else
  ok "6443 is free"
fi

step "validate"
"$BIN" cluster validate "$REF" 2>&1 | tail -6
echo "  (a fresh configuration reporting work to do is expected)"

step "generate"
if "$BIN" cluster generate "$REF" >/dev/null 2>&1; then ok "manifests generated"
else bad "cluster generate" "see the log"; fi

step "deploy — this creates real containers"
deploy_start=$(date +%s)
if timeout 1800 "$BIN" cluster deploy "$REF"; then
  ok "deploy returned 0 after $(( $(date +%s) - deploy_start ))s"
else
  bad "cluster deploy" "exit $? after $(( $(date +%s) - deploy_start ))s"
fi

step "verify from outside the CLI"
clusters="$(kind get clusters 2>/dev/null)"
if grep -q "$CLUSTER" <<<"$clusters"; then ok "kind lists the cluster"
else bad "kind does not list the cluster" "$clusters"; fi

KUBECONFIG_PATH="$OPENCENTER_CONFIG_DIR/clusters/state/$ORG/$CLUSTER/kubeconfig.yaml"
if [[ -f "$KUBECONFIG_PATH" ]]; then
  ok "a kubeconfig was produced"
else
  kind get kubeconfig --name "$CLUSTER" > "$R/kubeconfig.yaml" 2>/dev/null && \
    KUBECONFIG_PATH="$R/kubeconfig.yaml"
fi
if [[ -f "$KUBECONFIG_PATH" ]]; then
  nodes="$(KUBECONFIG="$KUBECONFIG_PATH" kubectl get nodes 2>&1)"
  if grep -q Ready <<<"$nodes"; then ok "nodes are Ready"; else bad "no Ready node" "$nodes"; fi
  KUBECONFIG="$KUBECONFIG_PATH" kubectl get nodes 2>/dev/null | sed 's/^/      /'
else
  bad "no kubeconfig anywhere" "cannot verify the cluster"
fi

step "status and doctor"
"$BIN" cluster status "$REF" >/dev/null 2>&1 && ok "status" || bad "cluster status" ""
"$BIN" cluster doctor "$REF" >/dev/null 2>&1 && ok "doctor" || bad "cluster doctor" ""

step "destroy"
if timeout 900 "$BIN" cluster destroy "$REF" --force; then ok "destroy returned 0"
else bad "cluster destroy" "exit $?"; fi

step "confirm it is gone"
remaining="$(kind get clusters 2>/dev/null)"
if grep -q "$CLUSTER" <<<"$remaining"; then
  bad "kind still lists the cluster" "$remaining"
else
  ok "kind no longer lists it"
fi
containers="$(docker ps -a --format '{{.Names}}' 2>/dev/null | grep "$CLUSTER" || true)"
if [[ -n "$containers" ]]; then
  bad "containers survive" "$containers"
else
  ok "no container named after the cluster survives"
fi

echo
[[ $status -eq 0 ]] && echo "  the real lifecycle works" || echo "  problems above"
exit "$status"
