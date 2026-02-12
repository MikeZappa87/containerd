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

// Package pod defines the interface for querying pod-level resources.
package pod

import "context"

// Route holds a single routing table entry.
type Route struct {
	// Destination is the destination CIDR (e.g. "10.0.0.0/8", "0.0.0.0/0" for default).
	Destination string
	// Gateway is the next-hop gateway IP. Empty if directly connected.
	Gateway string
	// InterfaceName is the outgoing interface for this route.
	InterfaceName string
}

// PodNetworkStatus holds the full network state of a pod.
type PodNetworkStatus struct {
	// InterfaceIPs maps interface names to their IP addresses.
	InterfaceIPs map[string][]string
	// Routes is the routing table entries in the pod network namespace.
	Routes []Route
}

// PodResourcesClient is the interface for querying pod-level resources
// by sandbox ID. It is intended for use by external consumers such as
// the kubelet or DRA drivers.
type PodResourcesClient interface {
	// GetPodResources returns the network namespace path for the given pod sandbox.
	GetPodResources(ctx context.Context, sandboxID string) (string, error)
	// GetPodIPs returns the network status (interfaces, IPs, and routes) for the given pod sandbox.
	GetPodIPs(ctx context.Context, sandboxID string) (*PodNetworkStatus, error)
}
