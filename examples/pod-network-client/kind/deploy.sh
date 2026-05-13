#!/usr/bin/env bash
# Rebuild pod-network-client and copy it into all Kind nodes.
#
# Usage:
#   ./examples/pod-network-client/kind/deploy.sh [--cluster-name NAME]
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
CLUSTER_NAME="${CLUSTER_NAME:-containerd-dev}"

echo "==> Building pod-network-client …"
(cd "$REPO_ROOT/examples/pod-network-client" && go build -o "$REPO_ROOT/bin/pod-network-client" .)

NODES=$(kind get nodes --name "$CLUSTER_NAME" 2>/dev/null)
if [ -z "$NODES" ]; then
  echo "error: no nodes found for cluster '$CLUSTER_NAME'" >&2
  exit 1
fi

for node in $NODES; do
  echo "==> Copying pod-network-client -> $node:/usr/local/bin/"
  docker cp "$REPO_ROOT/bin/pod-network-client" "$node:/usr/local/bin/pod-network-client"
done

echo "==> Done. Verify with:"
echo "    docker exec ${CLUSTER_NAME}-control-plane pod-network-client --help"
