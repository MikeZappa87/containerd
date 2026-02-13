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

package server

import (
	"context"
	"fmt"
	"net"

	cnins "github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"

	"github.com/containerd/containerd/v2/core/pod"
	sandboxstore "github.com/containerd/containerd/v2/internal/cri/store/sandbox"
)

// PodResourcesProvider is implemented by the CRI service to supply pod-level
// resource information. It is consumed by the Pod gRPC service plugin.
type PodResourcesProvider interface {
	// GetPodResources returns the network namespace path for the sandbox.
	GetPodResources(ctx context.Context, sandboxID string) (string, error)
	// GetPodIPs returns the network status (interfaces, IPs, and routes) for the sandbox.
	GetPodIPs(ctx context.Context, sandboxID string) (*pod.PodNetworkStatus, error)
	// GetPodNetwork returns the full network state of the sandbox.
	GetPodNetwork(ctx context.Context, sandboxID string) (*pod.PodNetworkState, error)
	// MoveDevice moves a network device into the sandbox's network namespace.
	MoveDevice(ctx context.Context, sandboxID string, deviceName string, deviceType pod.DeviceType, targetName string) (*pod.MoveDeviceResult, error)
	// AssignIPAddress assigns an IP address to an interface in the sandbox's network namespace.
	AssignIPAddress(ctx context.Context, sandboxID string, interfaceName string, address string) error
	// ApplyRoute adds a route in the sandbox's network namespace.
	ApplyRoute(ctx context.Context, sandboxID string, route pod.Route) error
}

// GetPodResources returns the network namespace path for the sandbox identified by sandboxID.
func (c *criService) GetPodResources(ctx context.Context, sandboxID string) (string, error) {
	sb, err := c.sandboxStore.Get(sandboxID)
	if err != nil {
		return "", fmt.Errorf("failed to find sandbox %q: %w", sandboxID, err)
	}
	return sb.NetNSPath, nil
}

// GetPodIPs returns the network status (interfaces, IPs, and routes) for the sandbox
// identified by sandboxID. When a CNI result is available it is used directly.
// Otherwise, the function falls back to inspecting the pod network namespace
// via netlink.
func (c *criService) GetPodIPs(ctx context.Context, sandboxID string) (*pod.PodNetworkStatus, error) {
	sb, err := c.sandboxStore.Get(sandboxID)
	if err != nil {
		return nil, fmt.Errorf("failed to find sandbox %q: %w", sandboxID, err)
	}

	if sb.CNIResult != nil {
		return podNetworkStatusFromCNI(sb), nil
	}

	// Host-network pods have no dedicated netns and no CNI result —
	// return an empty status rather than an error.
	if sb.NetNSPath == "" {
		return &pod.PodNetworkStatus{}, nil
	}

	// Fallback: inspect the pod network namespace directly via netlink.
	return podNetworkStatusFromNetNS(sb.NetNSPath)
}

// podNetworkStatusFromCNI builds a PodNetworkStatus from a stored CNI result.
func podNetworkStatusFromCNI(sb sandboxstore.Sandbox) *pod.PodNetworkStatus {
	ifaces := make(map[string][]string, len(sb.CNIResult.Interfaces))
	for name, cfg := range sb.CNIResult.Interfaces {
		ips := make([]string, 0, len(cfg.IPConfigs))
		for _, ipc := range cfg.IPConfigs {
			ips = append(ips, ipc.IP.String())
		}
		ifaces[name] = ips
	}

	routes := make([]pod.Route, 0, len(sb.CNIResult.Routes))
	for _, r := range sb.CNIResult.Routes {
		rt := pod.Route{
			Destination: r.Dst.String(),
		}
		if r.GW != nil {
			rt.Gateway = r.GW.String()
		}
		routes = append(routes, rt)
	}

	return &pod.PodNetworkStatus{
		InterfaceIPs: ifaces,
		Routes:       routes,
	}
}

// podNetworkStatusFromNetNS enters the given network namespace and discovers
// interfaces, IP addresses, and routes using netlink. This is the fallback
// path used when CNI results are not available (e.g. CNI is disabled).
func podNetworkStatusFromNetNS(netnsPath string) (*pod.PodNetworkStatus, error) {
	if netnsPath == "" {
		return nil, fmt.Errorf("network namespace path is empty")
	}

	var status pod.PodNetworkStatus

	err := cnins.WithNetNSPath(netnsPath, func(_ cnins.NetNS) error {
		// Build an index→name map so we can resolve link indexes in routes.
		links, err := netlink.LinkList()
		if err != nil {
			return fmt.Errorf("failed to list links: %w", err)
		}

		indexToName := make(map[int]string, len(links))
		for _, link := range links {
			attrs := link.Attrs()
			if attrs == nil {
				continue
			}
			indexToName[attrs.Index] = attrs.Name
		}

		// Enumerate IPs per interface (skip loopback).
		ifaceIPs := make(map[string][]string)
		for _, link := range links {
			attrs := link.Attrs()
			if attrs == nil {
				continue
			}
			if attrs.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, err := netlink.AddrList(link, netlink.FAMILY_ALL)
			if err != nil {
				return fmt.Errorf("failed to list addresses for %s: %w", attrs.Name, err)
			}
			for _, addr := range addrs {
				ifaceIPs[attrs.Name] = append(ifaceIPs[attrs.Name], addr.IP.String())
			}
		}
		status.InterfaceIPs = ifaceIPs

		// Enumerate routes.
		routes, err := netlink.RouteList(nil, netlink.FAMILY_ALL)
		if err != nil {
			return fmt.Errorf("failed to list routes: %w", err)
		}
		for _, r := range routes {
			rt := pod.Route{
				InterfaceName: indexToName[r.LinkIndex],
			}
			if r.Dst != nil {
				rt.Destination = r.Dst.String()
			} else {
				// Default route.
				rt.Destination = "0.0.0.0/0"
			}
			if r.Gw != nil {
				rt.Gateway = r.Gw.String()
			}
			status.Routes = append(status.Routes, rt)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to inspect network namespace %s: %w", netnsPath, err)
	}

	return &status, nil
}
