#!/usr/bin/env bash
#
# Start the openCenter CLI test bench.
#
#   ./start.sh                  build and open the console
#   ./start.sh --port 8080      listen somewhere else
#   ./start.sh --no-open        do not open a browser
#   ./start.sh --allow-mutate   permit deploy, destroy and bootstrap
#   ./start.sh --refresh        re-read the command list from the binary first
#   ./start.sh --kind           build the Kind cluster first, so the deploy and
#                               day-two commands have something to talk to
#
# Point it at the binary to test:
#   export OPENCLI_BIN=/path/to/openCenter-cli/bin/opencenter
#
set -euo pipefail
cd "$(dirname "$0")"

# The GitHub Actions panel may write to a repository. On by default, because
# this console is started by the person who would tick the box anyway, and a
# second gate they cannot see from the page only produced buttons that refused
# for no visible reason.
#
# Not unguarded: every write still needs its approval ticked on screen, still
# refuses to replace a workflow somebody has customised, and still may only
# touch .github/workflows/test-bench.yml.
export OPENCLI_ALLOW_ACTIONS_SETUP=1
export OPENCLI_ALLOW_GITOPS_UPDATE=1

PORT=7700
OPEN=1
REFRESH=0
KIND=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --port)         PORT="${2:?--port needs a number}"; shift 2 ;;
    --no-open)      OPEN=0; shift ;;
    --allow-mutate) export OPENCLI_ALLOW_MUTATE=1; shift ;;
    --refresh)      REFRESH=1; shift ;;
    --kind)         KIND=1; shift ;;
    # 3..15 is the comment block above; anything past it is code.
    -h|--help)      sed -n '3,15p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option $1 (try --help)" >&2; exit 1 ;;
  esac
done

# The Go this repository was developed against. Some distributions ship one too
# old to parse a modern go.mod, and mise is not always on PATH here.
if ! command -v go >/dev/null 2>&1 || ! go version 2>/dev/null | grep -qE 'go1\.(2[4-9]|[3-9][0-9])'; then
  for candidate in "$HOME/.local/share/mise/installs/go"/*/bin; do
    [[ -x "$candidate/go" ]] && export PATH="$candidate:$PATH" && break
  done
fi
if ! command -v go >/dev/null 2>&1; then
  echo "No Go 1.24+ found. Install Go, or: curl https://mise.run | sh && mise install go" >&2
  exit 1
fi

# The command list is generated from the binary, never kept by hand. Regenerate
# it when asked, or when it does not exist yet.
if [[ $REFRESH -eq 1 || ! -f config/commands.json ]]; then
  echo "reading the command list from the binary..."
  python3 scripts/generate-commands-json.py > config/commands.json.tmp
  mv config/commands.json.tmp config/commands.json
  python3 scripts/summarise-commands.py
  echo
fi

mkdir -p bin

# The container runtime, checked here rather than discovered three phases into a
# deploy.
#
# Local runs use this machine's Docker. GitHub runs use GitHub's, on a
# GitHub-hosted runner where it is preinstalled — the two never share anything,
# which is worth knowing before reading a green tick from one as evidence about
# the other.
#
# Not fatal. Everything up to `generate` works against files, and the emulated
# and configuration-only profiles create nothing at all. What a missing runtime
# blocks is the Kind profile and the Deploy, Health and Operate bands, and this
# says exactly that instead of letting a run get to phase eleven and stop.
container_runtime() {
  local runtime=""
  for candidate in docker podman; do
    command -v "$candidate" >/dev/null 2>&1 && { runtime="$candidate"; break; }
  done

  if [[ -z "$runtime" ]]; then
    echo
    echo "No docker or podman on this machine." >&2
    echo "  The Kind profile and the Deploy, Health and Operate stages need one." >&2
    echo "  Everything up to Generate works without it, and so do the emulated" >&2
    echo "  and configuration-only profiles." >&2
    echo "    sudo apt install -y docker.io && sudo usermod -aG docker \$USER" >&2
    echo
    return
  fi

  if "$runtime" info >/dev/null 2>&1; then
    echo "container runtime: $runtime, running"
    return
  fi

  # Installed but not answering. Try to start it without a password — if the
  # machine wants one, print the command rather than hanging on a prompt this
  # script cannot answer.
  echo "container runtime: $runtime is installed but not responding; starting it..."
  if sudo -n service "$runtime" start >/dev/null 2>&1 || sudo -n systemctl start "$runtime" >/dev/null 2>&1; then
    sleep 2
  fi

  if "$runtime" info >/dev/null 2>&1; then
    echo "container runtime: $runtime, running"
  else
    echo
    echo "$runtime is installed but the daemon is not running, and starting it" >&2
    echo "needs a password this script will not ask for. Run one of:" >&2
    echo "    sudo service $runtime start" >&2
    echo "    sudo systemctl start $runtime" >&2
    echo "  Until then the Kind profile and the Deploy, Health and Operate" >&2
    echo "  stages will report BLOCKED — which is the bench saying this machine" >&2
    echo "  cannot answer the question, not that openCenter is broken." >&2
    echo
  fi
}
container_runtime

# The cluster is built before the server starts rather than in the background,
# so a failure is seen here rather than discovered later by pressing Run on a
# command that had nothing to talk to. It is not fatal: everything up to
# "generate" works against files and is still worth running without a cluster.
if [[ $KIND -eq 1 ]]; then
  echo
  # Invoked through bash, not as ./script. Editing a file over the Windows
  # share strips the execute bit, and a missing +x should not be the reason
  # a cluster does not get built.
  if ! bash scripts/kind-cluster.sh up; then
    echo
    echo "The cluster could not be built. Starting anyway — the commands that" >&2
    echo "do not need one still work, and the page has a button to retry." >&2
  fi
  echo
fi

# Stop whatever this launcher left on this port last time.
#
# A previous run that was backgrounded, or Ctrl-C'd in a way that missed the
# child, keeps the port and the next start dies with a raw Go message:
#   listen tcp 127.0.0.1:7700: bind: address already in use
# That says nothing about what to do. Rather than print advice, take the port
# back — it belongs to this launcher.
#
# Only this program, only this port, only this user. Anything else listening
# here belongs to someone else and is reported rather than killed.
stop_previous() {
  local holder pid name

  # Ask who holds the port, then ask what that process is — rather than
  # guessing at the command line it was started with.
  #
  # It used to pgrep for the exact string "bin/testlab --addr 127.0.0.1:$PORT",
  # which only matches a console this script started itself. Start the same
  # binary any other way — `./bin/testlab`, or under nohup, which is how it gets
  # restarted after a rebuild — and the pattern misses. The launcher then found
  # its own program on its own port, did not recognise it, and reported:
  #
  #     Port 7700 is held by something this launcher did not start:
  #       users:(("testlab",pid=468616,fd=3))
  #
  # naming testlab in the very message claiming not to know what it was. The
  # advice was to move to another port, which leaves the old console serving a
  # stale page on the one the reader has open.
  #
  # Identity comes from the executable now, not from how it was invoked.
  holder=$(ss -ltnp 2>/dev/null | awk -v p=":$PORT" '$4 ~ p"$"' | head -1)
  [[ -z "$holder" ]] && return 0

  pid=$(sed -n 's/.*pid=\([0-9]\+\).*/\1/p' <<<"$holder" | head -1)

  # /proc/<pid>/comm, not readlink on /proc/<pid>/exe.
  #
  # The previous version resolved the exe symlink and took its basename, and it
  # refused to stop a console it had started itself:
  #
  #     Port 7700 is held by something this launcher did not start:
  #       users:(("testlab",pid=487967,fd=3))
  #
  # naming testlab in the message claiming not to recognise it.
  #
  # An honest note on why: I do not have a confirmed cause. The obvious
  # candidate was the kernel's " (deleted)" suffix — this script rebuilt
  # bin/testlab before looking, so the running console's exe link should have
  # been annotated and basename would have produced "testlab (deleted)". I
  # reproduced that exact sequence and readlink came back clean, so that story
  # is unproven and should not be repeated as fact.
  #
  # What is certain is that the chain was long — readlink, symlink resolution
  # through /proc, basename, string compare — and every link could fail quietly
  # to an empty string that then does not equal "testlab". comm is one read of
  # one file containing the process name. It is never annotated, needs no
  # resolution, and does not care what happened to the file underneath.
  #
  # Fewer steps, each of which can be wrong, beats a better theory about which
  # of the steps was.
  if [[ -n "$pid" && -r "/proc/$pid/comm" ]]; then
    name=$(<"/proc/$pid/comm")
  fi

  # Ours, and this user's. Both conditions: another user's testlab is not this
  # launcher's to kill, however it was started.
  if [[ "$name" == "testlab" && "$(stat -c %u "/proc/$pid" 2>/dev/null)" == "$(id -u)" ]]; then
    echo "stopping the console already on $PORT (pid $pid)"
    kill "$pid" 2>/dev/null
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      ss -ltn 2>/dev/null | awk -v p=":$PORT" '$4 ~ p"$"' | grep -q . || return 0
      sleep 0.3
    done
    kill -9 "$pid" 2>/dev/null
    sleep 0.5
    ss -ltn 2>/dev/null | awk -v p=":$PORT" '$4 ~ p"$"' | grep -q . || return 0
  fi

  echo >&2
  echo "Port $PORT is held by something this launcher did not start:" >&2
  echo "  $holder" >&2
  echo "Use --port to listen somewhere else." >&2
  exit 1
}
stop_previous

# Built after the old console is stopped, not before.
#
# Replacing bin/testlab while the previous one is still running is what made
# /proc/<pid>/exe read "(deleted)" and broke the reclaim above. It is also just
# wrong on its own: pulling the file out from under a running process to start a
# copy of it is a race with nothing to gain. Stop it, then build, then exec.
go build -o bin/testlab ./cmd/testlab

ARGS=(--addr "127.0.0.1:$PORT")
[[ $OPEN -eq 0 ]] && ARGS+=(--no-open)

exec ./bin/testlab "${ARGS[@]}"
