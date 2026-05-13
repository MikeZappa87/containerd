# OCI Pod Manifest Specification

## Abstract

This document defines the **OCI Pod Manifest**, a content-addressable document
that describes a group of OCI containers sharing a common set of resources such
as network namespaces, IPC namespaces, and volumes. It extends the OCI
ecosystem without modifying existing image or runtime specifications.

## Table of Contents

- [1. Introduction](#1-introduction)
- [2. Media Types](#2-media-types)
- [3. Pod Manifest](#3-pod-manifest)
  - [3.1 Metadata](#31-metadata)
  - [3.2 Shared Resources](#32-shared-resources)
  - [3.3 Containers](#33-containers)
- [4. Types](#4-types)
  - [4.1 Descriptor](#41-descriptor)
  - [4.2 SharedNamespace](#42-sharednamespace)
  - [4.3 NetworkConfig](#43-networkconfig)
  - [4.4 NetworkInterface](#44-networkinterface)
  - [4.5 Route](#45-route)
  - [4.6 DNSConfig](#46-dnsconfig)
  - [4.7 Volume](#47-volume)
  - [4.8 Container](#48-container)
  - [4.9 VolumeMount](#49-volumemount)
  - [4.10 Process](#410-process)
  - [4.11 User](#411-user)
  - [4.12 Resources](#412-resources)
  - [4.13 ResourceValue](#413-resourcevalue)
- [5. Lifecycle](#5-lifecycle)
- [6. Extensibility](#6-extensibility)
- [7. Relationship to Existing OCI Specifications](#7-relationship-to-existing-oci-specifications)
- [8. Example](#8-example)

---

## 1. Introduction

Container orchestrators such as Kubernetes implement the concept of a "pod" — a
group of containers that share certain OS-level resources. Today this concept
exists only at the orchestration layer, implemented through runtime-specific
mechanisms (pause containers, shared namespace paths, etc.).

This specification formalises the pod as an OCI-level construct so that:

1. Runtimes can natively understand multi-container groups.
2. Shared resources (network, IPC, PID namespaces; volumes) are expressed
   declaratively, not as implementation details.
3. Pod manifests are content-addressable and can be stored in OCI registries
   alongside image manifests and indexes.

### 1.1 Notational Conventions

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "NOT RECOMMENDED", "MAY", and "OPTIONAL" are to
be interpreted as described in [RFC 2119](https://tools.ietf.org/html/rfc2119).

### 1.2 Terminology

| Term | Definition |
|------|-----------|
| **Pod** | A group of one or more containers that share declared resources and are scheduled as a unit. |
| **Sandbox** | The runtime-level isolation boundary that hosts the shared namespaces. |
| **Init container** | A container that runs to completion before application containers start. |
| **App container** | A long-running container that forms part of the pod workload. |
| **Sidecar container** | An auxiliary container with special lifecycle ordering (starts before app containers, terminates after). |

---

## 2. Media Types

| Media Type | Description |
|---|---|
| `application/vnd.oci.pod.manifest.v1+json` | Pod Manifest |

This media type MAY appear as a referenced manifest within an
[OCI Image Index](https://github.com/opencontainers/image-spec/blob/main/image-index.md).
Runtimes that do not recognise this media type MUST ignore it.

---

## 3. Pod Manifest

The Pod Manifest is a JSON document with the following top-level properties:

| Property | Type | Required | Description |
|---|---|---|---|
| `schemaVersion` | `int` | REQUIRED | Must be `1`. |
| `mediaType` | `string` | REQUIRED | Must be `application/vnd.oci.pod.manifest.v1+json`. |
| `metadata` | [Metadata](#31-metadata) | REQUIRED | Pod-level metadata. |
| `sharedResources` | [SharedResources](#32-shared-resources) | REQUIRED | Resources shared across containers. |
| `containers` | array of [Container](#33-containers) | REQUIRED | One or more container definitions. MUST contain at least one entry. |

### 3.1 Metadata

| Property | Type | Required | Description |
|---|---|---|---|
| `name` | `string` | REQUIRED | A human-readable name for the pod. MUST match the regex `[a-z0-9]([-a-z0-9]*[a-z0-9])?` and be at most 253 characters. |
| `labels` | `map[string]string` | OPTIONAL | Arbitrary key-value pairs for selection and grouping. Keys MUST follow the OCI annotation key format. |
| `annotations` | `map[string]string` | OPTIONAL | Arbitrary key-value metadata. Keys MUST follow the [OCI annotation rules](https://github.com/opencontainers/image-spec/blob/main/annotations.md). |

### 3.2 Shared Resources

The `sharedResources` object declares the resources that containers in this pod
MAY join. Each property name is a **resource name** that containers reference in
their `join` array.

| Property | Type | Required | Description |
|---|---|---|---|
| `network` | [SharedNamespace](#42-sharednamespace) | OPTIONAL | Shared network namespace with optional network configuration. |
| `ipc` | [SharedNamespace](#42-sharednamespace) | OPTIONAL | Shared IPC namespace. |
| `pid` | [SharedNamespace](#42-sharednamespace) | OPTIONAL | Shared PID namespace. |
| `uts` | [SharedNamespace](#42-sharednamespace) | OPTIONAL | Shared UTS namespace (hostname/domainname). |
| `volumes` | array of [Volume](#47-volume) | OPTIONAL | Named volumes available for mounting into containers. |

At least one of `network`, `ipc`, `pid`, `uts`, or `volumes` MUST be present.

A runtime MUST create each declared namespace before starting any container.
The namespace MUST persist for the lifetime of the pod (i.e. until all
containers have terminated or the pod is explicitly destroyed).

### 3.3 Containers

The `containers` array defines the set of containers in the pod. Ordering
within the array determines startup order for init containers and is
RECOMMENDED as the startup order for app containers.

See [Container](#48-container) for the full property definition.

---

## 4. Types

### 4.1 Descriptor

An OCI content descriptor, as defined in the
[OCI Image Specification](https://github.com/opencontainers/image-spec/blob/main/descriptor.md).

| Property | Type | Required | Description |
|---|---|---|---|
| `mediaType` | `string` | REQUIRED | Media type of the referenced content. Typically `application/vnd.oci.image.manifest.v1+json`. |
| `digest` | `string` | REQUIRED | Digest of the referenced content. |
| `size` | `int64` | REQUIRED | Size in bytes of the referenced content. |
| `annotations` | `map[string]string` | OPTIONAL | Arbitrary metadata. The annotation `org.opencontainers.image.ref.name` SHOULD be used to record the human-readable image reference. |
| `platform` | object | OPTIONAL | Platform constraint as defined by the OCI Image Index specification. |

### 4.2 SharedNamespace

Defines a Linux namespace that is shared across containers.

| Property | Type | Required | Description |
|---|---|---|---|
| `type` | `string` | REQUIRED | Must be `"namespace"`. |
| `namespaceType` | `string` | REQUIRED | The Linux namespace type. One of: `"network"`, `"ipc"`, `"pid"`, `"uts"`. |
| `path` | `string` | OPTIONAL | Path to an existing namespace (e.g. `/var/run/netns/my-ns`). When set, the runtime MUST use the existing namespace instead of creating a new one. |
| `config` | object | OPTIONAL | Type-specific configuration. See below. |

#### Config by namespace type

| `namespaceType` | `config` type | Description |
|---|---|---|
| `"network"` | [NetworkConfig](#43-networkconfig) | Network interfaces, routes, and DNS. |
| `"uts"` | `{ "hostname": string, "domainname": string }` | Hostname and domain. |
| `"ipc"` | *none defined* | Reserved for future use. |
| `"pid"` | *none defined* | Reserved for future use. |

When `path` is set, `config` is informational (describes expected state) rather
than prescriptive. Runtimes SHOULD validate that the existing namespace matches
the config but MUST NOT fail if they cannot.

### 4.3 NetworkConfig

Desired network configuration for the shared network namespace.

| Property | Type | Required | Description |
|---|---|---|---|
| `interfaces` | array of [NetworkInterface](#44-networkinterface) | OPTIONAL | Network interfaces to configure. |
| `dns` | [DNSConfig](#46-dnsconfig) | OPTIONAL | DNS resolver configuration. |
| `hostname` | `string` | OPTIONAL | Hostname visible inside the namespace. |

A runtime MAY use any mechanism to realise this configuration (CNI, direct
netlink calls, userspace networking, etc.). The mechanism is not specified.

### 4.4 NetworkInterface

A single network interface within the shared network namespace.

| Property | Type | Required | Description |
|---|---|---|---|
| `name` | `string` | REQUIRED | Interface name (e.g. `"eth0"`, `"net1"`). |
| `network` | `string` | OPTIONAL | Logical network name. Interpretation is runtime-specific (e.g. a CNI network name). |
| `mac` | `string` | OPTIONAL | Desired MAC address in colon-separated hex notation. |
| `ips` | `[]string` | OPTIONAL | IP addresses in CIDR notation (e.g. `"10.244.1.5/24"`, `"fd00::5/64"`). |
| `gateway` | `string` | OPTIONAL | Default gateway IP for this interface. |
| `routes` | array of [Route](#45-route) | OPTIONAL | Routes associated with this interface. |
| `mtu` | `int` | OPTIONAL | Maximum transmission unit. |
| `annotations` | `map[string]string` | OPTIONAL | Arbitrary metadata (e.g. DPDK, SR-IOV parameters). |

### 4.5 Route

A routing table entry.

| Property | Type | Required | Description |
|---|---|---|---|
| `destination` | `string` | REQUIRED | Destination CIDR. Use `"0.0.0.0/0"` or `"::/0"` for the default route. |
| `gateway` | `string` | OPTIONAL | Next-hop gateway IP address. MUST be omitted for directly-connected routes. |
| `source` | `string` | OPTIONAL | Source address for the route. |
| `mtu` | `int` | OPTIONAL | MTU for this route. |
| `priority` | `int` | OPTIONAL | Route metric / priority. Lower values indicate higher priority. |
| `scope` | `string` | OPTIONAL | Route scope. One of: `"global"`, `"link"`, `"host"`. Defaults to `"global"`. |
| `table` | `int` | OPTIONAL | Routing table ID. Defaults to the main table (254). |

### 4.6 DNSConfig

DNS resolver settings for the pod.

| Property | Type | Required | Description |
|---|---|---|---|
| `nameservers` | `[]string` | OPTIONAL | DNS server IP addresses. |
| `searches` | `[]string` | OPTIONAL | DNS search domains. |
| `options` | `[]string` | OPTIONAL | Resolver options (e.g. `"ndots:5"`, `"timeout:2"`). |

Runtimes SHOULD write these values to `/etc/resolv.conf` inside each container
that joins the network namespace, unless the container provides its own.

### 4.7 Volume

A named storage volume that can be mounted into one or more containers.

| Property | Type | Required | Description |
|---|---|---|---|
| `name` | `string` | REQUIRED | Volume name. MUST be unique within the pod manifest. Referenced by [VolumeMount](#49-volumemount). |
| `type` | `string` | REQUIRED | Volume type. See below. |
| `sizeLimit` | `string` | OPTIONAL | Maximum size (e.g. `"1Gi"`, `"500Mi"`). Interpretation depends on `type`. |
| `medium` | `string` | OPTIONAL | Backing medium. `"memory"` for tmpfs; omit or `""` for disk. Only applicable to `"emptyDir"`. |
| `source` | `string` | OPTIONAL | Source path or identifier. Meaning depends on `type`. |
| `readOnly` | `bool` | OPTIONAL | If `true`, the volume MUST be mounted read-only in all containers. Defaults to `false`. |
| `data` | `map[string]string` | OPTIONAL | Inline data (base64-encoded values). Only applicable to `"configData"`. |

#### Volume types

| Type | Description |
|---|---|
| `"emptyDir"` | A temporary directory created when the pod starts and deleted when the pod terminates. |
| `"hostPath"` | A file or directory on the host. `source` MUST be an absolute path. |
| `"configData"` | Inline configuration data projected as files. Each key in `data` becomes a filename; the value (base64-encoded) becomes the file content. |
| `"secret"` | Like `configData` but the runtime SHOULD treat the data with higher confidentiality (e.g. avoid logging, use tmpfs). |
| `"persistentVolume"` | A reference to an external persistent volume. `source` contains the volume identifier; interpretation is runtime-specific. |

### 4.8 Container

A single container within the pod.

| Property | Type | Required | Description |
|---|---|---|---|
| `name` | `string` | REQUIRED | Unique name within this pod. MUST match `[a-z0-9]([-a-z0-9]*[a-z0-9])?`. |
| `type` | `string` | REQUIRED | Container lifecycle type. One of: `"init"`, `"app"`, `"sidecar"`. |
| `image` | [Descriptor](#41-descriptor) | REQUIRED | OCI content descriptor pointing to an OCI Image Manifest. |
| `join` | `[]string` | OPTIONAL | List of shared resource names to join (e.g. `["network", "ipc"]`). Each value MUST reference a key in `sharedResources`. Defaults to `[]` (private namespaces for everything). |
| `volumeMounts` | array of [VolumeMount](#49-volumemount) | OPTIONAL | Volumes to mount into this container. |
| `process` | [Process](#410-process) | OPTIONAL | Override the image's default process configuration. |
| `resources` | [Resources](#412-resources) | OPTIONAL | Resource constraints for this container. |
| `namespaces` | `map[string]string` | OPTIONAL | Per-container namespace overrides. Keys are namespace types (`"mount"`, `"user"`, `"cgroup"`); values are `"private"` (own namespace) or a path to an existing namespace. |
| `readonlyRootfs` | `bool` | OPTIONAL | Mount the container's root filesystem read-only. Defaults to `false`. |
| `annotations` | `map[string]string` | OPTIONAL | Arbitrary metadata for this container. |

#### Container types and lifecycle

| Type | Startup | Termination | Restart on failure |
|---|---|---|---|
| `"init"` | Sequential, in array order. Each MUST exit 0 before the next starts. | N/A (runs to completion) | Pod fails if any init container fails. |
| `"sidecar"` | After all init containers, before app containers. Concurrent with other sidecars. | After all app containers have terminated. | Runtime SHOULD restart. |
| `"app"` | After all init and sidecar containers are running. Concurrent with other app containers. | Pod begins termination when all app containers exit. | Policy-dependent (see `metadata.annotations`). |

### 4.9 VolumeMount

Mounts a named volume into a container's filesystem.

| Property | Type | Required | Description |
|---|---|---|---|
| `name` | `string` | REQUIRED | Name of the volume (MUST match a volume in `sharedResources.volumes`). |
| `mountPath` | `string` | REQUIRED | Absolute path inside the container where the volume is mounted. |
| `subPath` | `string` | OPTIONAL | Sub-directory within the volume to mount. Defaults to `""` (root of volume). |
| `readOnly` | `bool` | OPTIONAL | Mount the volume read-only. Defaults to `false`. Overrides the volume-level `readOnly`. |

### 4.10 Process

Overrides for the container's entrypoint and runtime environment.

| Property | Type | Required | Description |
|---|---|---|---|
| `args` | `[]string` | OPTIONAL | Command and arguments. Overrides the image's `Entrypoint` and `Cmd`. |
| `env` | `[]string` | OPTIONAL | Environment variables in `KEY=VALUE` format. Merged with (and overriding) the image's environment. |
| `cwd` | `string` | OPTIONAL | Working directory. Overrides the image's `WorkingDir`. |
| `user` | [User](#411-user) | OPTIONAL | User identity. Overrides the image's `User`. |
| `capabilities` | object | OPTIONAL | Linux capabilities. Properties: `bounding`, `effective`, `inheritable`, `permitted`, `ambient` — each an array of capability strings. |
| `noNewPrivileges` | `bool` | OPTIONAL | Set `PR_SET_NO_NEW_PRIVS` on the process. Defaults to `false`. |
| `apparmorProfile` | `string` | OPTIONAL | AppArmor profile name. |
| `selinuxLabel` | `string` | OPTIONAL | SELinux process label. |

### 4.11 User

| Property | Type | Required | Description |
|---|---|---|---|
| `uid` | `int` | OPTIONAL | User ID. |
| `gid` | `int` | OPTIONAL | Primary group ID. |
| `additionalGids` | `[]int` | OPTIONAL | Supplementary group IDs. |

### 4.12 Resources

Resource constraints for a container.

| Property | Type | Required | Description |
|---|---|---|---|
| `memory` | [ResourceValue](#413-resourcevalue) | OPTIONAL | Memory constraints. |
| `cpu` | [ResourceValue](#413-resourcevalue) | OPTIONAL | CPU constraints. |
| `devices` | array of object | OPTIONAL | Device access. Each entry: `{ "path": string, "type": string, "major": int, "minor": int, "access": string }`. |
| `pids` | [ResourceValue](#413-resourcevalue) | OPTIONAL | PID limit. |
| `annotations` | `map[string]string` | OPTIONAL | Extended resource requests (e.g. `"gpu.nvidia.com/count": "1"`). |

### 4.13 ResourceValue

| Property | Type | Required | Description |
|---|---|---|---|
| `limit` | `string` | OPTIONAL | Hard upper bound. |
| `request` | `string` | OPTIONAL | Minimum guaranteed amount. |

Values use standard suffixes: memory (`Ki`, `Mi`, `Gi`, `Ti`), CPU in
millicores (`"250m"` = 0.25 CPU) or whole cores (`"2"`).

---

## 5. Lifecycle

```
 ┌─────────────────────────────────────────────────────────────┐
 │                        POD LIFECYCLE                        │
 │                                                             │
 │  ┌──────────────┐                                           │
 │  │ Create Sandbox│  Create shared namespaces and volumes    │
 │  └──────┬───────┘                                           │
 │         │                                                   │
 │         ▼                                                   │
 │  ┌──────────────┐                                           │
 │  │ Init Phase   │  Run init containers sequentially.        │
 │  │              │  Each must exit 0 before the next starts. │
 │  └──────┬───────┘  Pod fails if any init container fails.   │
 │         │                                                   │
 │         ▼                                                   │
 │  ┌──────────────┐                                           │
 │  │ Sidecar Phase│  Start all sidecar containers.            │
 │  │              │  Wait until all are running.              │
 │  └──────┬───────┘                                           │
 │         │                                                   │
 │         ▼                                                   │
 │  ┌──────────────┐                                           │
 │  │ App Phase    │  Start all app containers concurrently.   │
 │  │              │  Pod is "Running".                        │
 │  └──────┬───────┘                                           │
 │         │                                                   │
 │         ▼                                                   │
 │  ┌──────────────┐                                           │
 │  │ Termination  │  1. Signal app containers (SIGTERM).      │
 │  │              │  2. Wait grace period.                    │
 │  │              │  3. Signal sidecar containers.            │
 │  │              │  4. Destroy sandbox.                      │
 │  └──────────────┘                                           │
 └─────────────────────────────────────────────────────────────┘
```

### 5.1 Sandbox Creation

The runtime MUST:

1. Create each namespace listed in `sharedResources` (unless `path` is set, in
   which case it MUST open the existing namespace).
2. Apply network configuration if `sharedResources.network.config` is present.
3. Create all volumes listed in `sharedResources.volumes`.

The sandbox MUST remain valid until explicit destruction, regardless of
individual container lifecycle events.

### 5.2 Container Startup

For each container, the runtime MUST:

1. Pull and unpack the image identified by the container's `image` descriptor.
2. Join the namespaces listed in the container's `join` array.
3. Mount the volumes listed in the container's `volumeMounts`.
4. Create any per-container private namespaces (all namespaces not listed in
   `join` are private by default; `mount` and `user` are always private unless
   explicitly shared via `namespaces`).
5. Start the container process.

### 5.3 Termination

When the pod is being terminated:

1. All `app` containers receive `SIGTERM`.
2. The runtime MUST wait a grace period (default: 30 seconds; configurable via
   `metadata.annotations["io.containerd.pod/termination-grace-period"]`).
3. Any containers still running receive `SIGKILL`.
4. `sidecar` containers receive `SIGTERM`, then `SIGKILL` after the grace
   period.
5. The sandbox (shared namespaces and volumes) is destroyed.

### 5.4 Failure Handling

| Condition | Behaviour |
|---|---|
| Init container exits non-zero | Pod enters `Failed` state. No further containers start. |
| App container exits non-zero | Runtime MAY restart per policy in annotations. |
| Sidecar container exits | Runtime SHOULD restart automatically. |
| All app containers exit 0 | Pod enters `Succeeded` state. Sidecars are terminated. |

---

## 6. Extensibility

### 6.1 Annotations

All objects that accept `annotations` follow the
[OCI annotation conventions](https://github.com/opencontainers/image-spec/blob/main/annotations.md).
Runtime-specific behaviour SHOULD be driven through annotations with a
vendor-specific prefix.

**Reserved annotation prefixes:**

| Prefix | Owner |
|---|---|
| `org.opencontainers.pod.*` | This specification. |
| `io.containerd.pod.*` | containerd runtime. |
| `io.kubernetes.pod.*` | Kubernetes. |

### 6.2 Custom Shared Resource Types

Runtimes MAY support additional shared resource types beyond `network`, `ipc`,
`pid`, and `uts`. Custom types MUST use a reverse-DNS key under
`sharedResources` (e.g. `"io.example.gpu-context"`) and MUST be ignored by
runtimes that do not recognise them.

### 6.3 Custom Volume Types

Runtimes MAY support additional volume types. Unknown volume types MUST cause
the runtime to reject the manifest with a descriptive error.

---

## 7. Relationship to Existing OCI Specifications

### 7.1 OCI Image Specification

The pod manifest **does not modify** the OCI Image Specification. Container
images are standard OCI images. The pod manifest references them via OCI
content descriptors.

### 7.2 OCI Runtime Specification

A pod manifest is a **higher-order document** that a runtime translates into
multiple OCI runtime bundles. Each container becomes one OCI runtime bundle
(`config.json`), with namespace paths pointing to the shared sandbox namespaces.

The translation is conceptually:

```
Pod Manifest
  └─ container[i].join = ["network", "ipc"]
       │
       ▼
OCI Runtime config.json for container[i]:
  "linux": {
    "namespaces": [
      { "type": "network", "path": "/proc/<sandbox-pid>/ns/net" },
      { "type": "ipc",     "path": "/proc/<sandbox-pid>/ns/ipc" },
      { "type": "pid"      /* no path → private */              },
      { "type": "mount"    /* always private */                  }
    ]
  }
```

### 7.3 OCI Distribution Specification

Pod manifests are content-addressable blobs. They MAY be pushed to and pulled
from OCI-compliant registries. They MAY be referenced from an OCI Image Index:

```jsonc
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.index.v1+json",
  "manifests": [
    {
      "mediaType": "application/vnd.oci.pod.manifest.v1+json",
      "digest": "sha256:abc123...",
      "size": 4096,
      "annotations": {
        "org.opencontainers.image.ref.name": "myapp-pod:v1.0"
      }
    }
  ]
}
```

### 7.4 Mapping to containerd Pod gRPC Service

The pod manifest's declared network state maps to the containerd Pod gRPC
service's observed state:

| Manifest (desired) | gRPC Response (observed) | Proto Type |
|---|---|---|
| `sharedResources.network` | `GetPodResources` → `pod_netns_path` | `GetPodResourcesResponse` |
| `interfaces[].name` | `interface_ips[].interface_name` | `PodInterfaceIPs` |
| `interfaces[].ips` | `interface_ips[].ips` | `PodInterfaceIPs` |
| `interfaces[].routes` | `routes[]` | `PodRoute` |
| `interfaces[].gateway` | `routes[].gateway` | `PodRoute` |

---

## 8. Example

A complete pod manifest for a web application with an envoy sidecar:

```json
{
  "schemaVersion": 1,
  "mediaType": "application/vnd.oci.pod.manifest.v1+json",
  "metadata": {
    "name": "web-frontend",
    "labels": {
      "app": "frontend",
      "version": "v3"
    },
    "annotations": {
      "io.containerd.pod/termination-grace-period": "60s"
    }
  },
  "sharedResources": {
    "network": {
      "type": "namespace",
      "namespaceType": "network",
      "config": {
        "interfaces": [
          {
            "name": "eth0",
            "network": "cluster-default",
            "ips": ["10.244.1.5/24"],
            "gateway": "10.244.1.1",
            "routes": [
              {
                "destination": "0.0.0.0/0",
                "gateway": "10.244.1.1"
              },
              {
                "destination": "10.96.0.0/12",
                "gateway": "10.244.1.1"
              }
            ],
            "mtu": 1450
          },
          {
            "name": "net1",
            "network": "sriov-dpdk",
            "ips": ["192.168.100.10/24"],
            "annotations": {
              "k8s.v1.cni.cncf.io/resourceName": "intel.com/sriov_netdevice"
            }
          }
        ],
        "dns": {
          "nameservers": ["10.96.0.10"],
          "searches": [
            "default.svc.cluster.local",
            "svc.cluster.local",
            "cluster.local"
          ],
          "options": ["ndots:5"]
        },
        "hostname": "web-frontend"
      }
    },
    "ipc": {
      "type": "namespace",
      "namespaceType": "ipc"
    },
    "volumes": [
      {
        "name": "shared-tmp",
        "type": "emptyDir",
        "medium": "memory",
        "sizeLimit": "64Mi"
      },
      {
        "name": "envoy-config",
        "type": "configData",
        "data": {
          "envoy.yaml": "c3RhdGljX3Jlc291cmNlczoKICBsaXN0ZW5lcnM6CiAgLSBuYW1lOiBsaXN0ZW5lcl8w..."
        }
      },
      {
        "name": "app-config",
        "type": "configData",
        "data": {
          "config.yaml": "c2VydmVyOgogIHBvcnQ6IDgwODAKICBob3N0OiAwLjAuMC4w"
        }
      }
    ]
  },
  "containers": [
    {
      "name": "db-migrate",
      "type": "init",
      "image": {
        "mediaType": "application/vnd.oci.image.manifest.v1+json",
        "digest": "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
        "size": 3210,
        "annotations": {
          "org.opencontainers.image.ref.name": "registry.example.com/web/migrate:v3"
        }
      },
      "join": ["network"],
      "process": {
        "args": ["/migrate", "--source", "file:///migrations", "--database", "postgres://db:5432/app", "up"],
        "env": ["DB_PASSWORD=secret"]
      }
    },
    {
      "name": "envoy",
      "type": "sidecar",
      "image": {
        "mediaType": "application/vnd.oci.image.manifest.v1+json",
        "digest": "sha256:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
        "size": 18200,
        "annotations": {
          "org.opencontainers.image.ref.name": "docker.io/envoyproxy/envoy:v1.30"
        }
      },
      "join": ["network", "ipc"],
      "volumeMounts": [
        { "name": "envoy-config", "mountPath": "/etc/envoy", "readOnly": true },
        { "name": "shared-tmp", "mountPath": "/tmp" }
      ],
      "process": {
        "args": ["/usr/local/bin/envoy", "-c", "/etc/envoy/envoy.yaml", "--service-cluster", "web-frontend"],
        "user": { "uid": 101, "gid": 101 }
      },
      "resources": {
        "memory": { "limit": "128Mi", "request": "64Mi" },
        "cpu": { "limit": "500m", "request": "100m" }
      },
      "readonlyRootfs": true
    },
    {
      "name": "web",
      "type": "app",
      "image": {
        "mediaType": "application/vnd.oci.image.manifest.v1+json",
        "digest": "sha256:f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1",
        "size": 24310,
        "annotations": {
          "org.opencontainers.image.ref.name": "registry.example.com/web/server:v3"
        }
      },
      "join": ["network", "ipc"],
      "volumeMounts": [
        { "name": "app-config", "mountPath": "/etc/app", "readOnly": true },
        { "name": "shared-tmp", "mountPath": "/tmp" }
      ],
      "process": {
        "args": ["/app", "serve", "--config", "/etc/app/config.yaml"],
        "env": ["PORT=8080", "LOG_LEVEL=info"],
        "user": { "uid": 1000, "gid": 1000 },
        "noNewPrivileges": true
      },
      "resources": {
        "memory": { "limit": "512Mi", "request": "256Mi" },
        "cpu": { "limit": "1000m", "request": "250m" }
      },
      "readonlyRootfs": true
    }
  ]
}
```
