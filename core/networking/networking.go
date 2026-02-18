/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

// Package networking defines the interface for querying and managing pod-level network resources.
package networking

import "context"

// DeviceType indicates the kind of network device.
type DeviceType int

const (
	// NetDev is a standard Linux network device.
	NetDev DeviceType = iota
	// RDMA is an RDMA device.
	RDMA
)

// Route holds a single routing table entry.
type Route struct {
	// Destination is the destination CIDR (e.g. "10.0.0.0/8", "0.0.0.0/0" for default).
	Destination string
	// Gateway is the next-hop gateway IP. Empty if directly connected.
	Gateway string
	// InterfaceName is the outgoing interface for this route.
	InterfaceName string
	// Metric is the route metric / priority.
	Metric uint32
	// Scope is the route scope (e.g. "link", "global", "host").
	Scope string
}

// RoutingRule holds a single ip rule entry.
type RoutingRule struct {
	Priority uint32
	Src      string
	Dst      string
	Table    string
	IIF      string
	OIF      string
}

// NetworkInterface describes a single interface inside a pod netns.
type NetworkInterface struct {
	Name       string
	MACAddress string
	Type       DeviceType
	MTU        uint32
	State      string
	Addresses  []string
}

// PodNetworkStatus holds the full network state of a pod.
type PodNetworkStatus struct {
	// InterfaceIPs maps interface names to their IP addresses.
	InterfaceIPs map[string][]string
	// Routes is the routing table entries in the pod network namespace.
	Routes []Route
}

// PodNetworkState holds the complete network state of a pod including
// interfaces, routes, and routing rules.
type PodNetworkState struct {
	Interfaces []NetworkInterface
	Routes     []Route
	Rules      []RoutingRule
}

// MoveDeviceResult holds the result of moving a device into a pod netns.
type MoveDeviceResult struct {
	DeviceName string
	Addresses  []string
	Routes     []Route
	Rules      []RoutingRule
}

// NetdevType identifies a kind of Linux network device.
type NetdevType int

const (
	NetdevVeth    NetdevType = iota // virtual Ethernet pair
	NetdevVxlan                     // VXLAN tunnel endpoint
	NetdevDummy                     // dummy interface
	NetdevIPVlan                    // IP-VLAN subordinate device
	NetdevMacvlan                   // MAC-VLAN subordinate device
	NetdevBridge                    // Linux bridge
)

// VethConfig holds parameters for creating a veth pair.
type VethConfig struct {
	PeerName      string
	PeerNetNSPath string
	// PeerMaster is the optional master (bridge) device to attach the
	// host-side peer end to. For example, setting this to "br0" enslaves
	// the veth peer to the bridge.
	PeerMaster string
}

// VxlanConfig holds parameters for creating a VXLAN device.
type VxlanConfig struct {
	VNI            uint32
	Group          string
	Port           uint32
	UnderlayDevice string
	Local          string
	TTL            uint32
	Learning       bool
}

// DummyConfig holds parameters for creating a dummy device (no extra fields).
type DummyConfig struct{}

// IPVlanMode selects the forwarding behavior for ipvlan devices.
type IPVlanMode int

const (
	IPVlanL2 IPVlanMode = iota
	IPVlanL3
	IPVlanL3S
)

// IPVlanFlag selects the isolation mode for ipvlan devices.
type IPVlanFlag int

const (
	IPVlanFlagBridge IPVlanFlag = iota
	IPVlanFlagPrivate
	IPVlanFlagVEPA
)

// IPVlanConfig holds parameters for creating an ipvlan device.
type IPVlanConfig struct {
	Parent string
	Mode   IPVlanMode
	Flag   IPVlanFlag
}

// MacvlanMode selects the forwarding behavior for macvlan devices.
type MacvlanMode int

const (
	MacvlanBridge MacvlanMode = iota
	MacvlanVEPA
	MacvlanPrivate
	MacvlanPassthru
	MacvlanSource
)

// MacvlanConfig holds parameters for creating a macvlan device.
type MacvlanConfig struct {
	Parent     string
	Mode       MacvlanMode
	MACAddress string
}

// BridgeConfig holds parameters for creating a Linux bridge device.
type BridgeConfig struct {
	// STPEnabled enables Spanning Tree Protocol.
	STPEnabled bool
	// VLANFiltering enables VLAN filtering on the bridge.
	VLANFiltering bool
	// ForwardDelay is the forward delay in centiseconds (kernel default 1500).
	ForwardDelay uint32
	// DefaultPVID is the default port VLAN ID for untagged traffic (default 1).
	DefaultPVID uint32
}

// CreateNetdevRequest describes a network device to create.
type CreateNetdevRequest struct {
	SandboxID string
	Name      string
	MTU       uint32
	Addresses []string

	// HostNetwork, when true, creates the device in the host (root)
	// network namespace instead of the pod's netns. This is necessary
	// for infrastructure devices like bridges and vxlan tunnel endpoints.
	HostNetwork bool

	// Master is the optional master (bridge) device to attach this device
	// to after creation. Both the new device and the master must be in
	// the same namespace.
	Master string

	// Exactly one of the following config fields must be non-nil.
	Veth    *VethConfig
	Vxlan   *VxlanConfig
	Dummy   *DummyConfig
	IPVlan  *IPVlanConfig
	Macvlan *MacvlanConfig
	Bridge  *BridgeConfig
}

// CreateNetdevResult holds the result of creating a netdev inside a pod netns.
type CreateNetdevResult struct {
	Interface     NetworkInterface
	PeerInterface *NetworkInterface // non-nil only for veth
}

// PodResourcesClient is the interface for querying pod-level resources
// by sandbox ID. It is intended for use by external consumers such as
// the kubelet or DRA drivers.
type PodResourcesClient interface {
	// GetPodResources returns the network namespace path for the given pod sandbox.
	GetPodResources(ctx context.Context, sandboxID string) (string, error)
	// GetPodIPs returns the network status (interfaces, IPs, and routes) for the given pod sandbox.
	GetPodIPs(ctx context.Context, sandboxID string) (*PodNetworkStatus, error)
	// GetPodNetwork returns the full network state of a pod sandbox.
	GetPodNetwork(ctx context.Context, sandboxID string) (*PodNetworkState, error)
	// MoveDevice moves a network device into the pod sandbox's network namespace.
	MoveDevice(ctx context.Context, sandboxID string, deviceName string, deviceType DeviceType, targetName string) (*MoveDeviceResult, error)
	// AssignIPAddress assigns an IP address to an interface inside the pod sandbox's network namespace.
	AssignIPAddress(ctx context.Context, sandboxID string, interfaceName string, address string) error
	// ApplyRoute adds a route inside the pod sandbox's network namespace.
	// When hostNetwork is true, the route is applied in the host (root)
	// network namespace instead.
	ApplyRoute(ctx context.Context, sandboxID string, route Route, hostNetwork bool) error
	// ApplyRule adds an ip rule inside the pod sandbox's network namespace.
	// When hostNetwork is true, the rule is applied in the host (root)
	// network namespace instead.
	ApplyRule(ctx context.Context, sandboxID string, rule RoutingRule, hostNetwork bool) error
	// CreateNetdev creates a new network device inside the pod sandbox's
	// network namespace (or in the host namespace when HostNetwork is set).
	CreateNetdev(ctx context.Context, req CreateNetdevRequest) (*CreateNetdevResult, error)
	// AttachInterface attaches an existing interface to a master device
	// (e.g. a Linux bridge). Both devices must be in the same network
	// namespace.
	AttachInterface(ctx context.Context, sandboxID string, interfaceName string, master string, hostNetwork bool) error
}

// PortMapping describes a port mapping for a pod sandbox.
type PortMapping struct {
	Protocol      string
	ContainerPort uint32
	HostPort      uint32
	HostIP        string
}

// DNSConfig holds DNS configuration for a pod.
type DNSConfig struct {
	Servers  []string
	Searches []string
	Options  []string
}

// SetupPodNetworkRequest holds all information needed to set up networking
// for a pod sandbox. It replaces the CNI ADD invocation.
type SetupPodNetworkRequest struct {
	SandboxID    string
	NetNSPath    string
	PodName      string
	PodNamespace string
	PodUID       string
	Annotations  map[string]string
	Labels       map[string]string
	PortMappings []PortMapping
	DNS          *DNSConfig
	CgroupParent string
	// PrevResult is the result from the previous plugin in a chain.
	// The primary (first) plugin receives this as nil. Subsequent
	// chained plugins receive the accumulated result from all
	// preceding plugins so they can inspect or augment it.
	PrevResult *SetupPodNetworkResult
}

// SetupPodNetworkResult holds the result of setting up pod networking.
type SetupPodNetworkResult struct {
	Interfaces []NetworkInterface
	Routes     []Route
	DNS        *DNSConfig
}

// TeardownPodNetworkRequest holds all information needed to tear down
// networking for a pod sandbox. It replaces the CNI DEL invocation.
type TeardownPodNetworkRequest struct {
	SandboxID    string
	NetNSPath    string
	PodName      string
	PodNamespace string
	PodUID       string
	Annotations  map[string]string
	Labels       map[string]string
	PortMappings []PortMapping
	CgroupParent string
	// PrevResult is the final network result from the setup phase.
	// This allows teardown plugins to know what interfaces, IPs,
	// and routes were configured so they can clean up appropriately.
	PrevResult *SetupPodNetworkResult
}

// PodNetworkPlugin is the interface that replaces the CNI plugin for
// pod-level network setup and teardown. It is backed by a gRPC client
// that calls an external PodNetwork service.
type PodNetworkPlugin interface {
	// SetupPodNetwork configures the network for a pod sandbox.
	SetupPodNetwork(ctx context.Context, req SetupPodNetworkRequest) (*SetupPodNetworkResult, error)
	// TeardownPodNetwork removes the network configuration for a pod sandbox.
	TeardownPodNetwork(ctx context.Context, req TeardownPodNetworkRequest) error
	// Status returns nil when the plugin is ready to accept requests.
	Status() error
	// CheckHealth performs a live health check against the network plugin.
	// It verifies both that the plugin's socket exists and that the
	// gRPC server is responsive by calling the CheckHealth RPC.
	CheckHealth(ctx context.Context) error
	// Close releases any resources held by the plugin.
	Close() error
}
