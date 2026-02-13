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

// Package pod defines the interface for querying and managing pod-level network resources.
package pod

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
	ApplyRoute(ctx context.Context, sandboxID string, route Route) error
}
