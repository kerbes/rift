#!/usr/bin/env bash
#
# teardown-clusters.sh — deletes the k3d clusters created by setup-clusters.sh.
#
# Usage:
#   ./tools/k3d/teardown-clusters.sh

set -euo pipefail

CLUSTERS=(rift-dev rift-staging rift-prod)

echo "=== Rift k3d cluster teardown ==="
echo ""

for name in "${CLUSTERS[@]}"; do
  if k3d cluster list -o json 2>/dev/null | grep -q "\"$name\""; then
    echo "  deleting $name..."
    k3d cluster delete "$name"
  else
    echo "  $name not found, skipping"
  fi
done

echo ""
echo "=== teardown complete ==="
