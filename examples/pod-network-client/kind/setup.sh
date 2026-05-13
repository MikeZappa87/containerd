#!/usr/bin/env bash
# Build a custom Kind node image that ships:
#   1. The local (workspace) build of containerd
#   2. The pod-network-client example binary
#
# Then create (or recreate) a Kind cluster using that image.
#
# Usage:
#   ./examples/pod-network-client/kind/setup.sh [--cluster-name NAME] [--k8s-version VERSION]
#
# Environment:
#   CLUSTER_NAME   – Kind cluster name      (default: containerd-dev)
#   K8S_VERSION    – Kubernetes release tag  (default: v1.32.0)
#   NODE_IMAGE     – resultant image name    (default: kindest/node:containerd-dev)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
cd "$REPO_ROOT"

CLUSTER_NAME="${CLUSTER_NAME:-containerd-dev}"
K8S_VERSION="${K8S_VERSION:-v1.32.0}"
NODE_IMAGE="${NODE_IMAGE:-kindest/node:containerd-dev}"
KIND_DIR="examples/pod-network-client/kind"

echo "==> Building containerd binaries …"
make binaries GO_BUILD_FLAGS="-mod=vendor"

echo "==> Building containerd-shim-runc-v2 …"
make bin/containerd-shim-runc-v2

echo "==> Building pod-network-client …"
(cd examples/pod-network-client && go build -o "$REPO_ROOT/bin/pod-network-client" .)

echo "==> Building Kind base node image (Kubernetes ${K8S_VERSION}) …"
kind build node-image --image "${NODE_IMAGE}-base" --type release "${K8S_VERSION}"

echo "==> Building custom node image with local containerd + pod-network-client …"
docker build \
  -t "${NODE_IMAGE}" \
  -f "${KIND_DIR}/Dockerfile" \
  --build-arg "BASE_IMAGE=${NODE_IMAGE}-base" \
  .

# Delete existing cluster if one exists with the same name
if kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
  echo "==> Deleting existing Kind cluster '${CLUSTER_NAME}' …"
  kind delete cluster --name "${CLUSTER_NAME}"
fi

echo "==> Creating Kind cluster '${CLUSTER_NAME}' …"
kind create cluster \
  --name "${CLUSTER_NAME}" \
  --image "${NODE_IMAGE}" \
  --config "${KIND_DIR}/kind-config.yaml"

echo ""
echo "==> Cluster ready!"
echo "    kubectl cluster-info --context kind-${CLUSTER_NAME}"
echo "    To run pod-network-client on a node:"
echo "      docker exec ${CLUSTER_NAME}-control-plane pod-network-client --help"
