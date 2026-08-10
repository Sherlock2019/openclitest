#!/usr/bin/env bash
# Build the openCenter CLI from source and check the specific defects the
# bench reported, one at a time, in isolation.
#
#   bash scripts/verify-cli-fixes.sh [/path/to/openCenter-cli]
#
# This is the quick loop while fixing. The real answer comes from a full
# `bench run full` against the rebuilt binary.
set -uo pipefail

CLI="${1:-/home/dzoan/opencli-benchmark/openCenter-cli}"
for candidate in "$HOME/.local/share/mise/installs/go/1.26.4/bin" \
                 "$HOME/.local/share/mise/installs/go/latest/bin"; do
  [[ -x "$candidate/go" ]] && export PATH="$candidate:$PATH" && break
done

BIN=/tmp/opencenter-fixed
echo "building $CLI ..."
if ! (cd "$CLI" && go build -o "$BIN" .); then
  echo "BUILD FAILED"
  exit 1
fi
echo "built $BIN"
echo

R="$(mktemp -d /tmp/verify-fixes-XXXXXX)"
trap 'rm -rf "$R"' EXIT
export HOME="$R/home" OPENCENTER_CONFIG_DIR="$R/cfg" OPENCENTER_STATE_DIR="$R/state"
export XDG_CONFIG_HOME="$R/xdg" NO_COLOR=1 TERM=dumb
mkdir -p "$HOME" "$OPENCENTER_CONFIG_DIR" "$OPENCENTER_STATE_DIR" "$R/work"
cd "$R/work"

status=0
pass() { printf '  ok    %s\n' "$1"; }
fail() { printf '  FAIL  %s\n     %s\n' "$1" "$2"; status=1; }

# --- 1. missing cluster: exit 3 and recovery text --------------------------
out="$("$BIN" cluster validate no-such-cluster-anywhere 2>&1)"; code=$?
if [[ $code -eq 3 ]]; then pass "missing cluster exits 3"
else fail "missing cluster exits 3" "got exit $code"; fi
grep -q 'cluster list' <<<"$out" \
  && pass "recovery suggests cluster list" \
  || fail "recovery suggests cluster list" "$(head -2 <<<"$out")"
grep -q 'cluster init' <<<"$out" \
  && pass "recovery suggests cluster init" \
  || fail "recovery suggests cluster init" "$(head -2 <<<"$out")"

# --- 2. unknown subcommand of a group --------------------------------------
for group in secrets "secrets keys" "cluster service" "cluster backup" \
             "cluster drift" "cluster import" "settings explain"; do
  # shellcheck disable=SC2086
  out="$("$BIN" $group definitely-not-a-command 2>&1)"; code=$?
  if [[ $code -ne 0 ]] && grep -qi 'unknown command' <<<"$out"; then
    pass "\"$group <typo>\" fails with unknown command"
  else
    fail "\"$group <typo>\" fails" "exit $code: $(head -1 <<<"$out")"
  fi
done

# The bare group must still print its help and succeed.
for group in secrets "cluster service"; do
  # shellcheck disable=SC2086
  out="$("$BIN" $group 2>&1)"; code=$?
  if [[ $code -eq 0 ]] && grep -q 'Usage:' <<<"$out"; then
    pass "\"$group\" alone still prints help"
  else
    fail "\"$group\" alone still prints help" "exit $code"
  fi
done

# --- 3. unsupported provider ------------------------------------------------
out="$("$BIN" cluster init prov-bogus --org provorg --type not-a-real-provider \
  --no-keygen --no-sops-keygen 2>&1)"; code=$?
if [[ $code -ne 0 ]]; then pass "unsupported --type is refused"
else fail "unsupported --type is refused" "exit 0: $(head -1 <<<"$out")"; fi

for provider in kind openstack vmware baremetal; do
  out="$("$BIN" cluster init "ok-$provider" --org provorg --type "$provider" \
    --no-keygen --no-sops-keygen 2>&1)"; code=$?
  [[ $code -eq 0 ]] && pass "--type $provider still accepted" \
    || fail "--type $provider still accepted" "exit $code: $(head -1 <<<"$out")"
done

# --- 4. secrets encrypt reporting success after encrypting nothing ----------
mkdir -p "$R/fakebin" "$R/enc"
cat > "$R/fakebin/sops" <<'SH'
#!/usr/bin/env bash
echo "sops: refusing" >&2
exit 1
SH
chmod +x "$R/fakebin/sops"
printf 'apiVersion: v1\nkind: Secret\nstringData:\n  password: PLAINTEXT_HERE\n' > "$R/enc/secret.yaml"
(cd "$R/enc" && "$BIN" secrets keys generate >/dev/null 2>&1)
out="$(PATH="$R/fakebin:$PATH" "$BIN" secrets encrypt --path "$R/enc" 2>&1)"; code=$?
if [[ $code -ne 0 ]]; then
  pass "encrypt fails when no file could be encrypted"
else
  fail "encrypt fails when no file could be encrypted" "exit 0: $(grep -i completed <<<"$out" | head -1)"
fi

# --- 5. sync must not write a config the loader rejects ---------------------
# Needs a far end, so it runs against the bench's simulator when one can be
# started, and is skipped rather than faked when it cannot.
BENCH=/home/dzoan/openclitestsimple/bin/bench
if [[ -x "$BENCH" ]]; then
  "$BENCH" sim --addr 127.0.0.1:5099 --write-clouds "$R/clouds.yaml" --quiet \
    >"$R/sim.log" 2>&1 &
  SIM=$!
  for _ in $(seq 1 40); do
    curl -fsS http://127.0.0.1:5099/ >/dev/null 2>&1 && break
    sleep 0.25
  done
  if curl -fsS http://127.0.0.1:5099/ >/dev/null 2>&1; then
    export OS_CLOUD=flex-sim OS_CLIENT_CONFIG_FILE="$R/clouds.yaml"
    "$BIN" cluster init synced --org syncorg --type openstack \
      --no-keygen --no-sops-keygen >/dev/null 2>&1
    "$BIN" cluster sync openstack syncorg/synced --os-cloud flex-sim --yes >/dev/null 2>&1
    out="$("$BIN" cluster export syncorg/synced 2>&1)"; code=$?
    if [[ $code -eq 0 ]]; then
      pass "a synced configuration still loads"
    else
      fail "a synced configuration still loads" "$(head -2 <<<"$out")"
    fi
    unset OS_CLOUD OS_CLIENT_CONFIG_FILE
  else
    printf '  skip  sync check — the simulator did not start\n'
  fi
  kill "$SIM" 2>/dev/null
else
  printf '  skip  sync check — build the bench first\n'
fi

# --- 6. doctor and a broken tool -------------------------------------------
"$BIN" cluster init doc-test --org docorg --type kind --no-keygen --no-sops-keygen >/dev/null 2>&1
healthy="$("$BIN" cluster doctor docorg/doc-test 2>&1)"
cat > "$R/fakebin/kubectl" <<'SH'
#!/usr/bin/env bash
echo "kubectl: broken" >&2
exit 1
SH
chmod +x "$R/fakebin/kubectl"
broken="$(PATH="$R/fakebin:$PATH" "$BIN" cluster doctor docorg/doc-test 2>&1)"
if [[ "$healthy" != "$broken" ]]; then
  pass "doctor notices a tool that cannot run"
else
  fail "doctor notices a tool that cannot run" "identical output either way"
fi

echo
[[ $status -eq 0 ]] && echo "  all checked defects are fixed" || echo "  some defects remain"
exit "$status"
