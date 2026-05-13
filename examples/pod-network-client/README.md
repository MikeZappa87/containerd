# Pod Network Client Example

A minimal gRPC client that connects to a server implementing the
`containerd.services.networking.v1.PodNetworkManagement` service and calls
**GetPodNetwork** to print the full network state of a pod sandbox.

## Build

```bash
cd examples/pod-network-client
go mod tidy
go build -o pod-network-client .
```

## Usage

```
pod-network-client [global flags] <command> [command flags]
```

### Global Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--address` | `unix:///run/containerd/networking.sock` | gRPC server address |
| `--timeout` | `10s` | RPC timeout |

### Commands

#### `get-network` — Show interfaces, routes, and rules

```bash
pod-network-client get-network --sandbox-id <ID>
pod-network-client get-network --sandbox-id <ID> --all   # show all interfaces, not just eth0
```

| Flag | Description |
|------|-------------|
| `--sandbox-id` | Sandbox (pod) ID *(required)* |
| `--all` | Show all interfaces (default: eth0 only) |

Example output:

```
Pod network for sandbox abc123

Interface: eth0
  MAC:   02:42:ac:11:00:02
  MTU:   1500
  State: UP
  Type:  NETDEV
  Addr:  10.244.0.5/24

Routes:
  default via 10.244.0.1 dev eth0 metric 0 scope global
  10.244.0.0/24 via (direct) dev eth0 metric 0 scope link
```

#### `get-ips` — Show IP addresses assigned to a pod

```bash
pod-network-client get-ips --sandbox-id <ID>
```

#### `get-resources` — Show pod resources (netns path)

```bash
pod-network-client get-resources --sandbox-id <ID>
```

#### `apply-route` — Add a route in the pod (or host) network namespace

```bash
pod-network-client apply-route --sandbox-id <ID> --destination 10.0.0.0/24 --gateway 10.244.0.1 --dev eth0
pod-network-client apply-route --sandbox-id <ID> --destination default --gateway 10.0.0.1 --host-network
```

| Flag | Description |
|------|-------------|
| `--sandbox-id` | Sandbox (pod) ID *(required)* |
| `--destination` | Destination CIDR or `default` *(required)* |
| `--gateway` | Gateway address (empty for direct) |
| `--dev` | Interface name |
| `--metric` | Route metric / priority |
| `--scope` | Route scope (`link`, `global`, `host`) |
| `--host-network` | Apply in the host namespace instead |

#### `apply-rule` — Add an IP rule in the pod (or host) network namespace

```bash
pod-network-client apply-rule --sandbox-id <ID> --src 10.0.0.0/24 --table 100 --priority 1000
```

| Flag | Description |
|------|-------------|
| `--sandbox-id` | Sandbox (pod) ID *(required)* |
| `--priority` | Rule priority |
| `--src` | Source prefix (CIDR) |
| `--dst` | Destination prefix (CIDR) |
| `--table` | Routing table (e.g. `main`, `254`) |
| `--iif` | Input interface |
| `--oif` | Output interface |
| `--host-network` | Apply in the host namespace instead |

#### `assign-ip` — Assign an IP address to an interface

```bash
pod-network-client assign-ip --sandbox-id <ID> --interface eth0 --address 10.0.0.5/24
```

| Flag | Description |
|------|-------------|
| `--sandbox-id` | Sandbox (pod) ID *(required)* |
| `--interface` | Interface name inside the pod *(required)* |
| `--address` | IP in CIDR notation *(required)* |

#### `create-netdev` — Create a network device

```bash
# Create a dummy device
pod-network-client create-netdev --sandbox-id <ID> --name dummy0 --type dummy

# Create a veth pair with peer attached to a bridge
pod-network-client create-netdev --sandbox-id <ID> --name eth1 --type veth \
  --peer-name veth-pod1 --peer-master br0

# Create a bridge in the host namespace
pod-network-client create-netdev --sandbox-id <ID> --name br0 --type bridge --host-network

# Create a vxlan
pod-network-client create-netdev --sandbox-id <ID> --name vxlan100 --type vxlan \
  --vni 100 --underlay-device eth0 --host-network
```

| Flag | Description |
|------|-------------|
| `--sandbox-id` | Sandbox (pod) ID *(required)* |
| `--name` | Device name *(required)* |
| `--type` | Device type: `dummy`, `veth`, `vxlan`, `bridge`, `ipvlan`, `macvlan` |
| `--mtu` | MTU (0 = kernel default) |
| `--addresses` | Comma-separated IPs in CIDR notation |
| `--host-network` | Create in the host namespace |
| `--master` | Master device to attach to after creation |
| `--peer-name` | (veth) Peer name in the host namespace |
| `--peer-master` | (veth) Master device for the peer end |
| `--vni` | (vxlan) VXLAN Network Identifier |
| `--group` | (vxlan) Multicast group or remote IP |
| `--port` | (vxlan) UDP destination port |
| `--underlay-device` | (vxlan) Underlay physical device |
| `--stp` | (bridge) Enable STP |
| `--vlan-filtering` | (bridge) Enable VLAN filtering |
| `--parent` | (macvlan/ipvlan) Parent interface |
| `--macvlan-mode` | (macvlan) Mode: `bridge`, `vepa`, `private`, `passthru`, `source` |
| `--ipvlan-mode` | (ipvlan) Mode: `l2`, `l3`, `l3s` |

#### `move-device` — Move a device from the host into the pod namespace

```bash
pod-network-client move-device --sandbox-id <ID> --device ens4f0
pod-network-client move-device --sandbox-id <ID> --device mlx5_0 --type rdma --target-name net1
```

| Flag | Description |
|------|-------------|
| `--sandbox-id` | Sandbox (pod) ID *(required)* |
| `--device` | Device name in the host namespace *(required)* |
| `--type` | Device type: `netdev` or `rdma` (default: `netdev`) |
| `--target-name` | Rename device inside the pod |

#### `attach` — Attach an interface to a master device (e.g. bridge)

```bash
pod-network-client attach --interface vxlan100 --master br0 --host-network
```

| Flag | Description |
|------|-------------|
| `--sandbox-id` | Sandbox (pod) ID |
| `--interface` | Interface to attach *(required)* |
| `--master` | Master device (e.g. bridge) *(required)* |
| `--host-network` | Operate in the host namespace |

## Kind Cluster with Custom Containerd

The `kind/` directory contains everything needed to spin up a Kind cluster
whose nodes run the **local build** of containerd and have `pod-network-client`
pre-installed.

### Quick Start

```bash
# From the repository root:
./examples/pod-network-client/kind/setup.sh
```

This will:

1. Build containerd binaries (`make binaries`)
2. Build the `pod-network-client` binary
3. Build a Kind base node image from a Kubernetes release
4. Layer the local containerd + pod-network-client on top
5. Create a 2-node Kind cluster (1 control-plane + 1 worker)

### Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `CLUSTER_NAME` | `containerd-dev` | Kind cluster name |
| `K8S_VERSION` | `v1.32.0` | Kubernetes release to build node image from |
| `NODE_IMAGE` | `kindest/node:containerd-dev` | Name of the final node image |

```bash
# Example: custom cluster name and Kubernetes version
CLUSTER_NAME=my-cluster K8S_VERSION=v1.31.4 ./examples/pod-network-client/kind/setup.sh
```

### Running pod-network-client on a node

```bash
# Exec into the control-plane node
docker exec containerd-dev-control-plane pod-network-client --help

# Query a sandbox's full network state
docker exec containerd-dev-control-plane pod-network-client \
  --address unix:///run/containerd/networking.sock \
  get-network --sandbox-id <SANDBOX_ID>

# Show IP addresses
docker exec containerd-dev-control-plane pod-network-client \
  get-ips --sandbox-id <SANDBOX_ID>

# Add a route
docker exec containerd-dev-control-plane pod-network-client \
  apply-route --sandbox-id <SANDBOX_ID> --destination 10.0.0.0/24 --gateway 10.244.0.1
```

### Verifying the containerd version

```bash
docker exec containerd-dev-control-plane containerd --version
```

### Cleanup

```bash
kind delete cluster --name containerd-dev
```
