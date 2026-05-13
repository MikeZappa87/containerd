# Custom Kind node image that replaces containerd with our local build
# and includes the pod-network-client binary.
#
# This is NOT a from-scratch image — it layers on top of a Kind node
# image that was pre-built with `kind build node-image`.
ARG BASE_IMAGE=kindest/node:containerd-dev-base
FROM ${BASE_IMAGE}

# ── Replace containerd binaries ─────────────────────────────────
# Kind node images store containerd at /usr/local/bin.  We overwrite
# the existing binaries with the ones we just built from source.
COPY bin/containerd          /usr/local/bin/containerd
COPY bin/containerd-shim-runc-v2 /usr/local/bin/containerd-shim-runc-v2
COPY bin/ctr                 /usr/local/bin/ctr

# ── Add pod-network-client ──────────────────────────────────────
COPY bin/pod-network-client  /usr/local/bin/pod-network-client
