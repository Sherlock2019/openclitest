#!/usr/bin/env bash
# What Kind clusters and containers exist on this machine right now.
set -uo pipefail
export PATH="$HOME/.local/bin:$PATH"

echo "kind clusters:"
kind get clusters 2>&1 | sed 's/^/  /'

echo
echo "running containers:"
docker ps --format '{{.Names}}\t{{.Ports}}' 2>&1 | sed 's/^/  /'

echo
echo "port 6443:"
if ss -ltnp 2>/dev/null | grep -q ':6443'; then
  ss -ltn 2>/dev/null | grep ':6443' | sed 's/^/  /'
else
  echo "  free"
fi
