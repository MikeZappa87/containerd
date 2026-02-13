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
	"os"
	"runtime"
	"strconv"
	"strings"

	cnins "github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"

	"github.com/containerd/containerd/v2/core/pod"
)

// GetPodNetwork returns the full network state (interfaces, routes, rules) for the sandbox.
func (c *criService) GetPodNetwork(ctx context.Context, sandboxID string) (*pod.PodNetworkState, error) {
	sb, err := c.sandboxStore.Get(sandboxID)
	if err != nil {
		return nil, fmt.Errorf("failed to find sandbox %q: %w", sandboxID, err)
	}

	if sb.NetNSPath == "" {
		return &pod.PodNetworkState{}, nil
	}

	return getPodNetworkState(sb.NetNSPath)
}

// MoveDevice moves a network device from the root netns into the pod's netns,
// preserving any IP addresses, routes, and routing rules associated with it.
func (c *criService) MoveDevice(ctx context.Context, sandboxID string, deviceName string, deviceType pod.DeviceType, targetName string) (*pod.MoveDeviceResult, error) {
	sb, err := c.sandboxStore.Get(sandboxID)
	if err != nil {
		return nil, fmt.Errorf("failed to find sandbox %q: %w", sandboxID, err)
	}
	if sb.NetNSPath == "" {
		return nil, fmt.Errorf("sandbox %q has no network namespace", sandboxID)
	}

	if deviceType == pod.RDMA {
		return moveRDMADevice(sb.NetNSPath, deviceName, targetName)
	}
	return moveNetDevice(sb.NetNSPath, deviceName, targetName)
}

// AssignIPAddress assigns an IP address (CIDR) to an interface inside the pod netns.
func (c *criService) AssignIPAddress(ctx context.Context, sandboxID string, interfaceName string, address string) error {
	sb, err := c.sandboxStore.Get(sandboxID)
	if err != nil {
		return fmt.Errorf("failed to find sandbox %q: %w", sandboxID, err)
	}
	if sb.NetNSPath == "" {
		return fmt.Errorf("sandbox %q has no network namespace", sandboxID)
	}

	addr, err := netlink.ParseAddr(address)
	if err != nil {
		return fmt.Errorf("invalid address %q: %w", address, err)
	}

	return cnins.WithNetNSPath(sb.NetNSPath, func(_ cnins.NetNS) error {
		link, err := netlink.LinkByName(interfaceName)
		if err != nil {
			return fmt.Errorf("interface %q not found: %w", interfaceName, err)
		}
		if err := netlink.AddrAdd(link, addr); err != nil {
			return fmt.Errorf("failed to add address %s to %s: %w", address, interfaceName, err)
		}
		return nil
	})
}

// ApplyRoute adds a route inside the pod's network namespace.
func (c *criService) ApplyRoute(ctx context.Context, sandboxID string, route pod.Route) error {
	sb, err := c.sandboxStore.Get(sandboxID)
	if err != nil {
		return fmt.Errorf("failed to find sandbox %q: %w", sandboxID, err)
	}
	if sb.NetNSPath == "" {
		return fmt.Errorf("sandbox %q has no network namespace", sandboxID)
	}

	return cnins.WithNetNSPath(sb.NetNSPath, func(_ cnins.NetNS) error {
		nlRoute := netlink.Route{}

		// Parse destination.
		if route.Destination != "" && route.Destination != "default" {
			_, dst, err := net.ParseCIDR(route.Destination)
			if err != nil {
				return fmt.Errorf("invalid destination %q: %w", route.Destination, err)
			}
			nlRoute.Dst = dst
		}

		// Parse gateway.
		if route.Gateway != "" {
			nlRoute.Gw = net.ParseIP(route.Gateway)
			if nlRoute.Gw == nil {
				return fmt.Errorf("invalid gateway %q", route.Gateway)
			}
		}

		// Resolve interface.
		if route.InterfaceName != "" {
			link, err := netlink.LinkByName(route.InterfaceName)
			if err != nil {
				return fmt.Errorf("interface %q not found: %w", route.InterfaceName, err)
			}
			nlRoute.LinkIndex = link.Attrs().Index
		}

		nlRoute.Priority = int(route.Metric)

		switch strings.ToLower(route.Scope) {
		case "link":
			nlRoute.Scope = netlink.SCOPE_LINK
		case "host":
			nlRoute.Scope = netlink.SCOPE_HOST
		case "global", "":
			nlRoute.Scope = netlink.SCOPE_UNIVERSE
		}

		if err := netlink.RouteAdd(&nlRoute); err != nil {
			return fmt.Errorf("failed to add route: %w", err)
		}
		return nil
	})
}

// getPodNetworkState enters the pod netns and collects full network state.
func getPodNetworkState(netnsPath string) (*pod.PodNetworkState, error) {
	var state pod.PodNetworkState

	err := cnins.WithNetNSPath(netnsPath, func(_ cnins.NetNS) error {
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

		// Enumerate interfaces.
		for _, link := range links {
			attrs := link.Attrs()
			if attrs == nil {
				continue
			}
			if attrs.Flags&net.FlagLoopback != 0 {
				continue
			}

			iface := pod.NetworkInterface{
				Name:       attrs.Name,
				MACAddress: attrs.HardwareAddr.String(),
				Type:       pod.NetDev,
				MTU:        uint32(attrs.MTU),
			}

			if attrs.OperState == netlink.OperUp || attrs.Flags&net.FlagUp != 0 {
				iface.State = "UP"
			} else {
				iface.State = "DOWN"
			}

			addrs, err := netlink.AddrList(link, netlink.FAMILY_ALL)
			if err != nil {
				return fmt.Errorf("failed to list addresses for %s: %w", attrs.Name, err)
			}
			for _, addr := range addrs {
				iface.Addresses = append(iface.Addresses, addr.IPNet.String())
			}

			state.Interfaces = append(state.Interfaces, iface)
		}

		// Enumerate routes.
		routes, err := netlink.RouteList(nil, netlink.FAMILY_ALL)
		if err != nil {
			return fmt.Errorf("failed to list routes: %w", err)
		}
		for _, r := range routes {
			rt := pod.Route{
				InterfaceName: indexToName[r.LinkIndex],
				Metric:        uint32(r.Priority),
			}
			if r.Dst != nil {
				rt.Destination = r.Dst.String()
			} else {
				rt.Destination = "0.0.0.0/0"
			}
			if r.Gw != nil {
				rt.Gateway = r.Gw.String()
			}
			switch r.Scope {
			case netlink.SCOPE_LINK:
				rt.Scope = "link"
			case netlink.SCOPE_HOST:
				rt.Scope = "host"
			default:
				rt.Scope = "global"
			}
			state.Routes = append(state.Routes, rt)
		}

		// Enumerate ip rules.
		rules, err := netlink.RuleList(netlink.FAMILY_ALL)
		if err != nil {
			return fmt.Errorf("failed to list rules: %w", err)
		}
		for _, r := range rules {
			rule := pod.RoutingRule{
				Priority: uint32(r.Priority),
				Table:    strconv.Itoa(r.Table),
				IIF:      r.IifName,
				OIF:      r.OifName,
			}
			if r.Src != nil {
				rule.Src = r.Src.String()
			}
			if r.Dst != nil {
				rule.Dst = r.Dst.String()
			}
			state.Rules = append(state.Rules, rule)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to inspect network namespace %s: %w", netnsPath, err)
	}
	return &state, nil
}

// moveNetDevice moves a Linux net device from the root netns into the target netns,
// preserving its addresses, routes and routing rules.
func moveNetDevice(netnsPath string, deviceName string, targetName string) (*pod.MoveDeviceResult, error) {
	if targetName == "" {
		targetName = deviceName
	}

	result := &pod.MoveDeviceResult{DeviceName: targetName}

	// Open the target netns fd.
	targetNS, err := netns.GetFromPath(netnsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open target netns %s: %w", netnsPath, err)
	}
	defer targetNS.Close()

	// Snapshot addresses, routes, and rules from the root netns.
	link, err := netlink.LinkByName(deviceName)
	if err != nil {
		return nil, fmt.Errorf("device %q not found in root namespace: %w", deviceName, err)
	}
	linkIndex := link.Attrs().Index

	addrs, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return nil, fmt.Errorf("failed to list addresses for %s: %w", deviceName, err)
	}
	for _, a := range addrs {
		result.Addresses = append(result.Addresses, a.IPNet.String())
	}

	allRoutes, err := netlink.RouteList(nil, netlink.FAMILY_ALL)
	if err != nil {
		return nil, fmt.Errorf("failed to list routes: %w", err)
	}
	for _, r := range allRoutes {
		if r.LinkIndex != linkIndex {
			continue
		}
		rt := pod.Route{
			InterfaceName: targetName,
			Metric:        uint32(r.Priority),
		}
		if r.Dst != nil {
			rt.Destination = r.Dst.String()
		} else {
			rt.Destination = "0.0.0.0/0"
		}
		if r.Gw != nil {
			rt.Gateway = r.Gw.String()
		}
		switch r.Scope {
		case netlink.SCOPE_LINK:
			rt.Scope = "link"
		case netlink.SCOPE_HOST:
			rt.Scope = "host"
		default:
			rt.Scope = "global"
		}
		result.Routes = append(result.Routes, rt)
	}

	allRules, err := netlink.RuleList(netlink.FAMILY_ALL)
	if err != nil {
		return nil, fmt.Errorf("failed to list rules: %w", err)
	}
	for _, r := range allRules {
		if r.IifName != deviceName && r.OifName != deviceName {
			continue
		}
		rule := pod.RoutingRule{
			Priority: uint32(r.Priority),
			Table:    strconv.Itoa(r.Table),
		}
		if r.IifName == deviceName {
			rule.IIF = targetName
		} else {
			rule.IIF = r.IifName
		}
		if r.OifName == deviceName {
			rule.OIF = targetName
		} else {
			rule.OIF = r.OifName
		}
		if r.Src != nil {
			rule.Src = r.Src.String()
		}
		if r.Dst != nil {
			rule.Dst = r.Dst.String()
		}
		result.Rules = append(result.Rules, rule)
	}

	// Bring link down before moving.
	if err := netlink.LinkSetDown(link); err != nil {
		return nil, fmt.Errorf("failed to set link down: %w", err)
	}

	// Move the device into the target netns.
	if err := netlink.LinkSetNsFd(link, int(targetNS)); err != nil {
		// Try to bring it back up on failure.
		_ = netlink.LinkSetUp(link)
		return nil, fmt.Errorf("failed to move device %s to netns: %w", deviceName, err)
	}

	// Enter the target netns to rename, re-add addresses/routes/rules, and bring up.
	err = cnins.WithNetNSPath(netnsPath, func(_ cnins.NetNS) error {
		movedLink, err := netlink.LinkByName(deviceName)
		if err != nil {
			return fmt.Errorf("device %q not found in target netns: %w", deviceName, err)
		}

		// Rename if needed.
		if targetName != deviceName {
			if err := netlink.LinkSetName(movedLink, targetName); err != nil {
				return fmt.Errorf("failed to rename device to %s: %w", targetName, err)
			}
			movedLink, err = netlink.LinkByName(targetName)
			if err != nil {
				return fmt.Errorf("failed to find renamed device %s: %w", targetName, err)
			}
		}

		// Re-add addresses.
		for _, a := range addrs {
			a.Label = "" // Clear label to avoid issues with new namespace.
			if err := netlink.AddrAdd(movedLink, &a); err != nil {
				// Address might already exist if the kernel preserved it.
				if !strings.Contains(err.Error(), "exists") {
					return fmt.Errorf("failed to re-add address %s: %w", a.IPNet.String(), err)
				}
			}
		}

		// Bring link up.
		if err := netlink.LinkSetUp(movedLink); err != nil {
			return fmt.Errorf("failed to bring link up: %w", err)
		}

		// Re-add routes.
		for _, r := range allRoutes {
			if r.LinkIndex != linkIndex {
				continue
			}
			r.LinkIndex = movedLink.Attrs().Index
			if err := netlink.RouteAdd(&r); err != nil {
				if !strings.Contains(err.Error(), "exists") {
					return fmt.Errorf("failed to re-add route: %w", err)
				}
			}
		}

		// Re-add rules.
		for _, r := range allRules {
			if r.IifName != deviceName && r.OifName != deviceName {
				continue
			}
			if r.IifName == deviceName {
				r.IifName = targetName
			}
			if r.OifName == deviceName {
				r.OifName = targetName
			}
			if err := netlink.RuleAdd(&r); err != nil {
				if !strings.Contains(err.Error(), "exists") {
					return fmt.Errorf("failed to re-add rule: %w", err)
				}
			}
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to configure device in target netns: %w", err)
	}

	return result, nil
}

// moveRDMADevice moves an RDMA device into the target network namespace.
// The behavior depends on the RDMA subsystem netns mode:
//   - shared:    RDMA devices are visible in all namespaces. We move the
//     underlying net device; the RDMA device is already accessible.
//   - exclusive: RDMA devices are bound to their netns. Moving the
//     underlying net device causes the RDMA device to follow.
func moveRDMADevice(netnsPath string, deviceName string, targetName string) (*pod.MoveDeviceResult, error) {
	if targetName == "" {
		targetName = deviceName
	}

	// Lock the OS thread because we manipulate network namespaces.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Validate that the RDMA device exists.
	if _, err := netlink.RdmaLinkByName(deviceName); err != nil {
		return nil, fmt.Errorf("RDMA device %s not found: %w", deviceName, err)
	}

	// Detect the RDMA subsystem netns mode. In shared mode the RDMA device
	// is already reachable from every namespace; in exclusive mode it will
	// follow the net device we are about to move.
	mode, err := netlink.RdmaSystemGetNetnsMode()
	if err != nil {
		return nil, fmt.Errorf("failed to query RDMA netns mode: %w", err)
	}

	// Find the underlying net device for this RDMA device via sysfs.
	netDevName, err := rdmaToNetDev(deviceName)
	if err != nil {
		return nil, fmt.Errorf("failed to find net device for RDMA device %s: %w", deviceName, err)
	}

	// Move the underlying net device into the target namespace.
	result, err := moveNetDevice(netnsPath, netDevName, targetName)
	if err != nil {
		return nil, fmt.Errorf("failed to move net device %s (for RDMA %s): %w", netDevName, deviceName, err)
	}

	// In exclusive mode the kernel moved the RDMA device along with the
	// net device. In shared mode the RDMA device was already visible in
	// all namespaces — nothing extra to do.
	_ = mode

	return result, nil
}

// rdmaToNetDev discovers the net device associated with an RDMA device by
// reading /sys/class/infiniband/<rdma_dev>/device/net/.
func rdmaToNetDev(rdmaDevice string) (string, error) {
	sysPath := fmt.Sprintf("/sys/class/infiniband/%s/device/net", rdmaDevice)
	entries, err := os.ReadDir(sysPath)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", sysPath, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		return e.Name(), nil
	}
	return "", fmt.Errorf("no net device found for RDMA device %s", rdmaDevice)
}
