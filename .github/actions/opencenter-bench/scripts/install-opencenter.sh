#!/usr/bin/env bash
# Install the openCenter CLI into ~/.local/bin.
#
# This lives in a file rather than in config/quickstart.yaml because the card
# in the console shows whatever the setup command is, verbatim. A hundred and
# thirty lines of release-asset matching and checksum verification is the right
# amount of care and the wrong thing to put in front of somebody who just wants
# the tool — they cannot read it, cannot check it, and cannot tell which of the
# two paths through it applies to them.
#
# Two ways in:
#
#   OPENCLI_BRANCH set    clone that branch and build it. The only way to test
#                         something that has not been released.
#   OPENCLI_BRANCH empty  download the published release named by
#                         OPENCLI_VERSION, checksum it, install it.
#
# Every value comes from the environment, set by the fields on the card. None
# of it is interpolated into a command.
set -eu

install_dir="$HOME/.local/bin"
branch="${OPENCLI_BRANCH:-}"

# ---------------------------------------------------------------- from source
if [ -n "$branch" ]; then
  repo="${OPENCLI_REPO:-https://github.com/opencenter-cloud/openCenter-cli.git}"
  case "$branch" in
    *[!A-Za-z0-9._/-]*) echo "invalid branch: $branch" >&2; exit 1 ;;
  esac

  echo "building from source"
  echo "  repository: $repo"
  echo "  branch:     $branch"
  echo

  export GIT_TERMINAL_PROMPT=0 MISE_YES=1
  work="${OPENCLI_WORK:-$HOME/.cache/opencli-bench}"
  mkdir -p "$work"
  cd "$work"

  if [ -d openCenter-cli/.git ]; then
    # Check out what was asked for even though a copy exists. Reporting
    # "already built" and keeping the previous branch would make the field a
    # lie — the whole point of choosing a branch is that the build changes.
    cd openCenter-cli
    git remote set-url origin "$repo"
    git fetch --depth 1 origin "$branch"
    git checkout -B "$branch" FETCH_HEAD
  else
    git clone --depth 1 --branch "$branch" "$repo" openCenter-cli
    cd openCenter-cli
  fi

  mise trust
  mise install
  mise run build

  mkdir -p "$install_dir"
  install -m 0755 bin/opencenter "$install_dir/opencenter"
  git rev-parse --short HEAD > "$install_dir/opencenter.release"
  echo
  "$install_dir/opencenter" version
  exit 0
fi

# ------------------------------------------------------- a published release
repository=opencenter-cloud/opencenter-cli
requested="${OPENCLI_VERSION:-latest}"
case "$requested" in
  *[!A-Za-z0-9._-]*) echo "invalid release tag: $requested" >&2; exit 1 ;;
esac

case "$(uname -s | tr '[:upper:]' '[:lower:]')" in
  linux) os=linux ;;
  darwin) os=darwin ;;
  *) echo "unsupported operating system: $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "unsupported CPU architecture: $(uname -m)" >&2; exit 1 ;;
esac

api="https://api.github.com/repos/$repository/releases"
if [ "$requested" = latest ]; then
  endpoint="$api?per_page=1"
else
  endpoint="$api/tags/$requested"
fi

# A token only raises the rate limit. The repository is public and this works
# without one.
if [ -n "${GITHUB_TOKEN:-}" ]; then
  release=$(curl -fsSL -H "Accept: application/vnd.github+json" \
    -H "Authorization: Bearer $GITHUB_TOKEN" "$endpoint")
else
  release=$(curl -fsSL -H "Accept: application/vnd.github+json" "$endpoint")
fi

field() { sed -n "s/^[[:space:]]*\"$1\":[[:space:]]*\"\([^\"]*\)\".*/\1/p"; }

tag=$(printf '%s\n' "$release" | field tag_name | head -1)
url=$(printf '%s\n' "$release" | field browser_download_url \
  | grep '/opencenter-' | grep -v '/opencenter-local-' \
  | grep -- "-${os}-${arch}$" | head -1)
checksum_url=$(printf '%s\n' "$release" | field browser_download_url \
  | grep '/checksums.txt$' | head -1)

if [ -z "$tag" ] || [ -z "$url" ] || [ -z "$checksum_url" ]; then
  echo "no openCenter CLI release asset found for ${os}/${arch}" >&2
  exit 1
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
asset=${url##*/}

echo "release:  $tag"
echo "asset:    $asset"
echo "install:  $install_dir/opencenter"

curl -fL "$url" -o "$tmp/$asset"
curl -fsSL "$checksum_url" -o "$tmp/checksums.txt"

# Verified, not trusted. A binary fetched over the network and put on PATH
# without checking it is a binary somebody else chose for you.
expected=$(awk -v a="$asset" '$2 == a { print $1; exit }' "$tmp/checksums.txt")
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$asset" | awk '{ print $1 }')
else
  actual=$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')
fi
if [ -z "$expected" ] || [ "$actual" != "$expected" ]; then
  echo "checksum verification failed for $asset" >&2
  exit 1
fi
echo "checksum: verified"

mkdir -p "$install_dir"
install -m 0755 "$tmp/$asset" "$install_dir/opencenter"
printf '%s\n' "$tag" > "$install_dir/opencenter.release"
echo
"$install_dir/opencenter" version
