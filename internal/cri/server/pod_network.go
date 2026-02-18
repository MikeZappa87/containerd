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

	"github.com/containerd/containerd/v2/core/networking"
)

// GetPodNetwork returns the full network state (interfaces, routes, rules) for the sandbox.
func (c *criService) GetPodNetwork(ctx context.Context, sandboxID string) (*networking.PodNetworkState, error) {
	sb, err := c.sandboxStore.Get(sandboxID)
	if err != nil {
		return nil, fmt.Errorf("failed to find sandbox %q: %w", sandboxID, err)
	}

	if sb.NetNSPath == "" {
		return &networking.PodNetworkState{}, nil
	}

	return getPodNetworkState(sb.NetNSPath)
}

// MoveDevice moves a network device from the root netns into the pod's netns,
// preserving any IP addresses, routes, and routing rules associated with it.
func (c *criService) MoveDevice(ctx context.Context, sandboxID string, deviceName string, deviceType networking.DeviceType, targetName string) (*networking.MoveDeviceResult, error) {
	sb, err := c.sandboxStore.Get(sandboxID)
	if err != nil {
		return nil, fmt.Errorf("failed to find sandbox %q: %w", sandboxID, err)
	}
	if sb.NetNSPath == "" {
		return nil, fmt.Errorf("sandbox %q has no network namespace", sandboxID)
	}

	if deviceType == networking.RDMA {
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

// ApplyRoute adds a route. By default it operates inside the pod's network
// namespace. When hostNetwork is true the route is applied in the host (root)
// network namespace instead; the sandbox ID is still validated for authorization.
func (c *criService) ApplyRoute(ctx context.Context, sandboxID string, route networking.Route, hostNetwork bool) error {
	sb, err := c.sandboxStore.Get(sandboxID)
	if err != nil {
		return fmt.Errorf("failed to find sandbox %q: %w", sandboxID, err)
	}
	if !hostNetwork && sb.NetNSPath == "" {
		return fmt.Errorf("sandbox %q has no network namespace", sandboxID)
	}

	addRoute := func() error {
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
	}

	if hostNetwork {
		return addRoute()
	}
	return cnins.WithNetNSPath(sb.NetNSPath, func(_ cnins.NetNS) error {
		return addRoute()
	})
}

// ApplyRule adds an ip rule. By default it operates inside the pod's network
// namespace. When hostNetwork is true the rule is applied in the host (root)
// network namespace instead; the sandbox ID is still validated for authorization.
func (c *criService) ApplyRule(ctx context.Context, sandboxID string, rule networking.RoutingRule, hostNetwork bool) error {
	sb, err := c.sandboxStore.Get(sandboxID)
	if err != nil {
		return fmt.Errorf("failed to find sandbox %q: %w", sandboxID, err)
	}
	if !hostNetwork && sb.NetNSPath == "" {
		return fmt.Errorf("sandbox %q has no network namespace", sandboxID)
	}

	addRule := func() error {
		nlRule := netlink.NewRule()

		nlRule.Priority = int(rule.Priority)

		if rule.Src != "" {
			_, src, err := net.ParseCIDR(rule.Src)
			if err != nil {
				return fmt.Errorf("invalid rule src %q: %w", rule.Src, err)
			}
			nlRule.Src = src
		}

		if rule.Dst != "" {
			_, dst, err := net.ParseCIDR(rule.Dst)
			if err != nil {
				return fmt.Errorf("invalid rule dst %q: %w", rule.Dst, err)
			}
			nlRule.Dst = dst
		}

		if rule.Table != "" {
			table, err := strconv.Atoi(rule.Table)
			if err != nil {
				return fmt.Errorf("invalid rule table %q: %w", rule.Table, err)
			}
			nlRule.Table = table
		}

		nlRule.IifName = rule.IIF
		nlRule.OifName = rule.OIF

		if err := netlink.RuleAdd(nlRule); err != nil {
			return fmt.Errorf("failed to add rule: %w", err)
		}
		return nil
	}

	if hostNetwork {
		return addRule()
	}
	return cnins.WithNetNSPath(sb.NetNSPath, func(_ cnins.NetNS) error {
		return addRule()
	})
}

// getPodNetworkState enters the pod netns and collects full network state.
func getPodNetworkState(netnsPath string) (*networking.PodNetworkState, error) {
	var state networking.PodNetworkState

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

			iface := networking.NetworkInterface{
				Name:       attrs.Name,
				MACAddress: attrs.HardwareAddr.String(),
				Type:       networking.NetDev,
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
			rt := networking.Route{
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
			rule := networking.RoutingRule{
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
func moveNetDevice(netnsPath string, deviceName string, targetName string) (*networking.MoveDeviceResult, error) {
	if targetName == "" {
		targetName = deviceName
	}

	result := &networking.MoveDeviceResult{DeviceName: targetName}

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
		rt := networking.Route{
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
		rule := networking.RoutingRule{
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
func moveRDMADevice(netnsPath string, deviceName string, targetName string) (*networking.MoveDeviceResult, error) {
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

	// In shared mode the RDMA device is already visible in all namespaces.
	// Moving the underlying net device would remove it from the root
	// namespace, potentially breaking other containers that rely on it.
	if mode == "shared" {
		return &networking.MoveDeviceResult{DeviceName: targetName}, nil
	}

	// Find the underlying net device for this RDMA device via sysfs.
	netDevName, err := rdmaToNetDev(deviceName)
	if err != nil {
		return nil, fmt.Errorf("failed to find net device for RDMA device %s: %w", deviceName, err)
	}

	// Move the underlying net device into the target namespace.
	// In exclusive mode the kernel moves the RDMA device along with
	// the net device automatically.
	result, err := moveNetDevice(netnsPath, netDevName, targetName)
	if err != nil {
		return nil, fmt.Errorf("failed to move net device %s (for RDMA %s): %w", netDevName, deviceName, err)
	}

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

// CreateNetdev creates a new Linux network device. When HostNetwork is true
// the device is created in the host (root) namespace; otherwise it is created
// inside the pod's network namespace.
func (c *criService) CreateNetdev(ctx context.Context, req networking.CreateNetdevRequest) (*networking.CreateNetdevResult, error) {
	sb, err := c.sandboxStore.Get(req.SandboxID)
	if err != nil {
		return nil, fmt.Errorf("failed to find sandbox %q: %w", req.SandboxID, err)
	}
	if !req.HostNetwork && sb.NetNSPath == "" {
		return nil, fmt.Errorf("sandbox %q has no network namespace", req.SandboxID)
	}

	netnsPath := sb.NetNSPath
	if req.HostNetwork {
		netnsPath = ""
	}

	return createNetdev(netnsPath, req)
}

// createNetdev performs the actual netlink device creation. When netnsPath is
// empty the device is created in the current (host) namespace.
func createNetdev(netnsPath string, req networking.CreateNetdevRequest) (*networking.CreateNetdevResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("device name is required")
	}

	switch {
	case req.Veth != nil:
		return createVethDevice(netnsPath, req)
	case req.Vxlan != nil:
		return createVxlanDevice(netnsPath, req)
	case req.Dummy != nil:
		return createDummyDevice(netnsPath, req)
	case req.IPVlan != nil:
		return createIPVlanDevice(netnsPath, req)
	case req.Macvlan != nil:
		return createMacvlanDevice(netnsPath, req)
	case req.Bridge != nil:
		return createBridgeDevice(netnsPath, req)
	default:
		return nil, fmt.Errorf("exactly one device config must be specified")
	}
}

// createVethDevice creates a veth pair with one end in the pod netns.
func createVethDevice(netnsPath string, req networking.CreateNetdevRequest) (*networking.CreateNetdevResult, error) {
	cfg := req.Veth
	if cfg.PeerName == "" {
		return nil, fmt.Errorf("veth peer_name is required")
	}

	result := &networking.CreateNetdevResult{}

	// Open the target netns fd.
	targetNS, err := netns.GetFromPath(netnsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open target netns %s: %w", netnsPath, err)
	}
	defer targetNS.Close()

	// Create the veth pair in the root namespace.
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{
			Name: req.Name,
			MTU:  int(req.MTU),
		},
		PeerName: cfg.PeerName,
	}
	if req.MTU > 0 {
		veth.LinkAttrs.MTU = int(req.MTU)
	}

	if err := netlink.LinkAdd(veth); err != nil {
		return nil, fmt.Errorf("failed to create veth pair (%s, %s): %w", req.Name, cfg.PeerName, err)
	}

	// Move the pod end into the target netns.
	podLink, err := netlink.LinkByName(req.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to find veth %s: %w", req.Name, err)
	}
	if err := netlink.LinkSetNsFd(podLink, int(targetNS)); err != nil {
		// Clean up on failure.
		_ = netlink.LinkDel(podLink)
		return nil, fmt.Errorf("failed to move veth %s to pod netns: %w", req.Name, err)
	}

	// Configure the pod end inside the target netns.
	err = cnins.WithNetNSPath(netnsPath, func(_ cnins.NetNS) error {
		link, err := netlink.LinkByName(req.Name)
		if err != nil {
			return fmt.Errorf("veth %s not found in pod netns: %w", req.Name, err)
		}

		// Assign addresses.
		for _, addrStr := range req.Addresses {
			addr, err := netlink.ParseAddr(addrStr)
			if err != nil {
				return fmt.Errorf("invalid address %q: %w", addrStr, err)
			}
			if err := netlink.AddrAdd(link, addr); err != nil {
				return fmt.Errorf("failed to add address %s: %w", addrStr, err)
			}
		}

		// Bring up and snapshot.
		iface, err := bringUpAndSnapshot(req.Name)
		if err != nil {
			return err
		}
		result.Interface = iface
		return nil
	})
	if err != nil {
		return nil, err
	}

	// If a peer netns is specified, move the peer there; otherwise leave in root ns.
	peerLink, err := netlink.LinkByName(cfg.PeerName)
	if err != nil {
		return nil, fmt.Errorf("failed to find peer veth %s: %w", cfg.PeerName, err)
	}

	// Attach peer to a master (bridge) if requested.
	if cfg.PeerMaster != "" {
		if err := enslaveToMaster(peerLink, cfg.PeerMaster); err != nil {
			return nil, fmt.Errorf("failed to attach peer %s to master %s: %w", cfg.PeerName, cfg.PeerMaster, err)
		}
	}

	if cfg.PeerNetNSPath != "" {
		peerNS, err := netns.GetFromPath(cfg.PeerNetNSPath)
		if err != nil {
			return nil, fmt.Errorf("failed to open peer netns %s: %w", cfg.PeerNetNSPath, err)
		}
		defer peerNS.Close()
		if err := netlink.LinkSetNsFd(peerLink, int(peerNS)); err != nil {
			return nil, fmt.Errorf("failed to move peer veth %s: %w", cfg.PeerName, err)
		}
		// Read peer info from its new namespace.
		err = cnins.WithNetNSPath(cfg.PeerNetNSPath, func(_ cnins.NetNS) error {
			iface, err := bringUpAndSnapshot(cfg.PeerName)
			if err != nil {
				return err
			}
			result.PeerInterface = &iface
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		// Bring up the peer in the root namespace.
		iface, err := bringUpAndSnapshot(cfg.PeerName)
		if err != nil {
			return nil, err
		}
		result.PeerInterface = &iface
	}

	return result, nil
}

// createVxlanDevice creates a VXLAN tunnel endpoint. When netnsPath is empty
// the device is created in the host namespace.
func createVxlanDevice(netnsPath string, req networking.CreateNetdevRequest) (*networking.CreateNetdevResult, error) {
	cfg := req.Vxlan

	result := &networking.CreateNetdevResult{}

	create := func() error {
		vxlan := &netlink.Vxlan{
			LinkAttrs: netlink.LinkAttrs{
				Name: req.Name,
			},
			VxlanId:  int(cfg.VNI),
			Learning: cfg.Learning,
		}

		if req.MTU > 0 {
			vxlan.LinkAttrs.MTU = int(req.MTU)
		}

		if cfg.Group != "" {
			vxlan.Group = net.ParseIP(cfg.Group)
			if vxlan.Group == nil {
				return fmt.Errorf("invalid vxlan group address %q", cfg.Group)
			}
		}

		if cfg.Port > 0 {
			vxlan.Port = int(cfg.Port)
		}

		if cfg.UnderlayDevice != "" {
			parent, err := netlink.LinkByName(cfg.UnderlayDevice)
			if err != nil {
				return fmt.Errorf("underlay device %q not found: %w", cfg.UnderlayDevice, err)
			}
			vxlan.VtepDevIndex = parent.Attrs().Index
		}

		if cfg.Local != "" {
			vxlan.SrcAddr = net.ParseIP(cfg.Local)
			if vxlan.SrcAddr == nil {
				return fmt.Errorf("invalid vxlan local address %q", cfg.Local)
			}
		}

		if cfg.TTL > 0 {
			vxlan.TTL = int(cfg.TTL)
		}

		if err := netlink.LinkAdd(vxlan); err != nil {
			return fmt.Errorf("failed to create vxlan device %s: %w", req.Name, err)
		}

		link, err := netlink.LinkByName(req.Name)
		if err != nil {
			return fmt.Errorf("failed to find vxlan %s after creation: %w", req.Name, err)
		}

		for _, addrStr := range req.Addresses {
			addr, err := netlink.ParseAddr(addrStr)
			if err != nil {
				return fmt.Errorf("invalid address %q: %w", addrStr, err)
			}
			if err := netlink.AddrAdd(link, addr); err != nil {
				return fmt.Errorf("failed to add address %s: %w", addrStr, err)
			}
		}

		// Attach to master if requested.
		if req.Master != "" {
			if err := enslaveToMaster(link, req.Master); err != nil {
				return fmt.Errorf("failed to attach %s to master %s: %w", req.Name, req.Master, err)
			}
		}

		// Bring up and snapshot.
		iface, err := bringUpAndSnapshot(req.Name)
		if err != nil {
			return err
		}
		result.Interface = iface
		return nil
	}

	if netnsPath == "" {
		// Host namespace.
		if err := create(); err != nil {
			return nil, err
		}
	} else {
		if err := cnins.WithNetNSPath(netnsPath, func(_ cnins.NetNS) error {
			return create()
		}); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// createDummyDevice creates a dummy interface inside the pod netns.
func createDummyDevice(netnsPath string, req networking.CreateNetdevRequest) (*networking.CreateNetdevResult, error) {
	result := &networking.CreateNetdevResult{}

	err := cnins.WithNetNSPath(netnsPath, func(_ cnins.NetNS) error {
		dummy := &netlink.Dummy{
			LinkAttrs: netlink.LinkAttrs{
				Name: req.Name,
			},
		}

		if req.MTU > 0 {
			dummy.LinkAttrs.MTU = int(req.MTU)
		}

		if err := netlink.LinkAdd(dummy); err != nil {
			return fmt.Errorf("failed to create dummy device %s: %w", req.Name, err)
		}

		link, err := netlink.LinkByName(req.Name)
		if err != nil {
			return fmt.Errorf("failed to find dummy %s after creation: %w", req.Name, err)
		}

		for _, addrStr := range req.Addresses {
			addr, err := netlink.ParseAddr(addrStr)
			if err != nil {
				return fmt.Errorf("invalid address %q: %w", addrStr, err)
			}
			if err := netlink.AddrAdd(link, addr); err != nil {
				return fmt.Errorf("failed to add address %s: %w", addrStr, err)
			}
		}

		// Bring up and snapshot.
		iface, err := bringUpAndSnapshot(req.Name)
		if err != nil {
			return err
		}
		result.Interface = iface
		return nil
	})

	if err != nil {
		return nil, err
	}
	return result, nil
}

// createIPVlanDevice creates an ipvlan subordinate device inside the pod netns.
func createIPVlanDevice(netnsPath string, req networking.CreateNetdevRequest) (*networking.CreateNetdevResult, error) {
	cfg := req.IPVlan
	if cfg.Parent == "" {
		return nil, fmt.Errorf("ipvlan parent is required")
	}

	result := &networking.CreateNetdevResult{}

	// Resolve the parent link index from the root namespace.
	parentLink, err := netlink.LinkByName(cfg.Parent)
	if err != nil {
		return nil, fmt.Errorf("parent device %q not found: %w", cfg.Parent, err)
	}
	parentIndex := parentLink.Attrs().Index

	err = cnins.WithNetNSPath(netnsPath, func(_ cnins.NetNS) error {
		ipvlan := &netlink.IPVlan{
			LinkAttrs: netlink.LinkAttrs{
				Name:        req.Name,
				ParentIndex: parentIndex,
			},
			Mode: toNetlinkIPVlanMode(cfg.Mode),
			Flag: toNetlinkIPVlanFlag(cfg.Flag),
		}

		if req.MTU > 0 {
			ipvlan.LinkAttrs.MTU = int(req.MTU)
		}

		if err := netlink.LinkAdd(ipvlan); err != nil {
			return fmt.Errorf("failed to create ipvlan device %s: %w", req.Name, err)
		}

		link, err := netlink.LinkByName(req.Name)
		if err != nil {
			return fmt.Errorf("failed to find ipvlan %s after creation: %w", req.Name, err)
		}

		for _, addrStr := range req.Addresses {
			addr, err := netlink.ParseAddr(addrStr)
			if err != nil {
				return fmt.Errorf("invalid address %q: %w", addrStr, err)
			}
			if err := netlink.AddrAdd(link, addr); err != nil {
				return fmt.Errorf("failed to add address %s: %w", addrStr, err)
			}
		}

		// Bring up and snapshot.
		iface, err := bringUpAndSnapshot(req.Name)
		if err != nil {
			return err
		}
		result.Interface = iface
		return nil
	})

	if err != nil {
		return nil, err
	}
	return result, nil
}

// createMacvlanDevice creates a macvlan subordinate device inside the pod netns.
func createMacvlanDevice(netnsPath string, req networking.CreateNetdevRequest) (*networking.CreateNetdevResult, error) {
	cfg := req.Macvlan
	if cfg.Parent == "" {
		return nil, fmt.Errorf("macvlan parent is required")
	}

	result := &networking.CreateNetdevResult{}

	// Resolve the parent link index from the root namespace.
	parentLink, err := netlink.LinkByName(cfg.Parent)
	if err != nil {
		return nil, fmt.Errorf("parent device %q not found: %w", cfg.Parent, err)
	}
	parentIndex := parentLink.Attrs().Index

	err = cnins.WithNetNSPath(netnsPath, func(_ cnins.NetNS) error {
		macvlan := &netlink.Macvlan{
			LinkAttrs: netlink.LinkAttrs{
				Name:        req.Name,
				ParentIndex: parentIndex,
			},
			Mode: toNetlinkMacvlanMode(cfg.Mode),
		}

		if req.MTU > 0 {
			macvlan.LinkAttrs.MTU = int(req.MTU)
		}

		if cfg.MACAddress != "" {
			hwAddr, err := net.ParseMAC(cfg.MACAddress)
			if err != nil {
				return fmt.Errorf("invalid MAC address %q: %w", cfg.MACAddress, err)
			}
			macvlan.LinkAttrs.HardwareAddr = hwAddr
		}

		if err := netlink.LinkAdd(macvlan); err != nil {
			return fmt.Errorf("failed to create macvlan device %s: %w", req.Name, err)
		}

		link, err := netlink.LinkByName(req.Name)
		if err != nil {
			return fmt.Errorf("failed to find macvlan %s after creation: %w", req.Name, err)
		}

		for _, addrStr := range req.Addresses {
			addr, err := netlink.ParseAddr(addrStr)
			if err != nil {
				return fmt.Errorf("invalid address %q: %w", addrStr, err)
			}
			if err := netlink.AddrAdd(link, addr); err != nil {
				return fmt.Errorf("failed to add address %s: %w", addrStr, err)
			}
		}

		// Bring up and snapshot.
		iface, err := bringUpAndSnapshot(req.Name)
		if err != nil {
			return err
		}
		result.Interface = iface
		return nil
	})

	if err != nil {
		return nil, err
	}
	return result, nil
}

// linkToNetworkInterface converts a netlink.Link to a networking.NetworkInterface.
// The link should be re-fetched after any state changes (e.g. LinkSetUp) to
// ensure the returned state is current.
func linkToNetworkInterface(link netlink.Link) networking.NetworkInterface {
	attrs := link.Attrs()
	iface := networking.NetworkInterface{
		Name:       attrs.Name,
		MACAddress: attrs.HardwareAddr.String(),
		Type:       networking.NetDev,
		MTU:        uint32(attrs.MTU),
	}

	if attrs.OperState == netlink.OperUp || attrs.Flags&net.FlagUp != 0 {
		iface.State = "UP"
	} else {
		iface.State = "DOWN"
	}

	addrs, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err == nil {
		for _, addr := range addrs {
			iface.Addresses = append(iface.Addresses, addr.IPNet.String())
		}
	}

	return iface
}

// bringUpAndSnapshot brings a link up, re-reads it, and returns a snapshot.
func bringUpAndSnapshot(name string) (networking.NetworkInterface, error) {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return networking.NetworkInterface{}, fmt.Errorf("link %s not found: %w", name, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return networking.NetworkInterface{}, fmt.Errorf("failed to bring up %s: %w", name, err)
	}
	// Re-read link to capture updated flags.
	link, err = netlink.LinkByName(name)
	if err != nil {
		return networking.NetworkInterface{}, fmt.Errorf("link %s not found after up: %w", name, err)
	}
	return linkToNetworkInterface(link), nil
}

// toNetlinkIPVlanMode converts a domain IPVlanMode to a netlink.IPVlanMode.
func toNetlinkIPVlanMode(mode networking.IPVlanMode) netlink.IPVlanMode {
	switch mode {
	case networking.IPVlanL3:
		return netlink.IPVLAN_MODE_L3
	case networking.IPVlanL3S:
		return netlink.IPVLAN_MODE_L3S
	default:
		return netlink.IPVLAN_MODE_L2
	}
}

// toNetlinkIPVlanFlag converts a domain IPVlanFlag to a netlink.IPVlanFlag.
func toNetlinkIPVlanFlag(flag networking.IPVlanFlag) netlink.IPVlanFlag {
	switch flag {
	case networking.IPVlanFlagPrivate:
		return netlink.IPVLAN_FLAG_PRIVATE
	case networking.IPVlanFlagVEPA:
		return netlink.IPVLAN_FLAG_VEPA
	default:
		return netlink.IPVLAN_FLAG_BRIDGE
	}
}

// toNetlinkMacvlanMode converts a domain MacvlanMode to a netlink.MacvlanMode.
func toNetlinkMacvlanMode(mode networking.MacvlanMode) netlink.MacvlanMode {
	switch mode {
	case networking.MacvlanVEPA:
		return netlink.MACVLAN_MODE_VEPA
	case networking.MacvlanPrivate:
		return netlink.MACVLAN_MODE_PRIVATE
	case networking.MacvlanPassthru:
		return netlink.MACVLAN_MODE_PASSTHRU
	case networking.MacvlanSource:
		return netlink.MACVLAN_MODE_SOURCE
	default:
		return netlink.MACVLAN_MODE_BRIDGE
	}
}

// createBridgeDevice creates a Linux bridge device. When netnsPath is empty
// the bridge is created in the host namespace (the typical topology for
// node-level bridges shared across pods).
func createBridgeDevice(netnsPath string, req networking.CreateNetdevRequest) (*networking.CreateNetdevResult, error) {
	cfg := req.Bridge

	result := &networking.CreateNetdevResult{}

	create := func() error {
		bridge := &netlink.Bridge{
			LinkAttrs: netlink.LinkAttrs{
				Name: req.Name,
			},
		}

		if req.MTU > 0 {
			bridge.LinkAttrs.MTU = int(req.MTU)
		}

		if err := netlink.LinkAdd(bridge); err != nil {
			// If the bridge already exists and we're in the host namespace,
			// treat it as success (idempotent).
			if netnsPath == "" {
				existing, lookupErr := netlink.LinkByName(req.Name)
				if lookupErr == nil {
					iface := linkToNetworkInterface(existing)
					result.Interface = iface
					return nil
				}
			}
			return fmt.Errorf("failed to create bridge %s: %w", req.Name, err)
		}

		link, err := netlink.LinkByName(req.Name)
		if err != nil {
			return fmt.Errorf("failed to find bridge %s after creation: %w", req.Name, err)
		}

		// Apply STP setting.
		if cfg.STPEnabled {
			if err := setSTPEnabled(req.Name, true); err != nil {
				return fmt.Errorf("failed to enable STP on %s: %w", req.Name, err)
			}
		}

		// Apply VLAN filtering.
		if cfg.VLANFiltering {
			if err := setVLANFiltering(req.Name, true); err != nil {
				return fmt.Errorf("failed to enable VLAN filtering on %s: %w", req.Name, err)
			}
		}

		// Apply forward delay.
		if cfg.ForwardDelay > 0 {
			if err := setForwardDelay(req.Name, cfg.ForwardDelay); err != nil {
				return fmt.Errorf("failed to set forward delay on %s: %w", req.Name, err)
			}
		}

		// Assign addresses.
		for _, addrStr := range req.Addresses {
			addr, err := netlink.ParseAddr(addrStr)
			if err != nil {
				return fmt.Errorf("invalid address %q: %w", addrStr, err)
			}
			if err := netlink.AddrAdd(link, addr); err != nil {
				return fmt.Errorf("failed to add address %s: %w", addrStr, err)
			}
		}

		// Bring up and snapshot.
		iface, err := bringUpAndSnapshot(req.Name)
		if err != nil {
			return err
		}
		result.Interface = iface
		return nil
	}

	if netnsPath == "" {
		// Host namespace.
		if err := create(); err != nil {
			return nil, err
		}
	} else {
		if err := cnins.WithNetNSPath(netnsPath, func(_ cnins.NetNS) error {
			return create()
		}); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// setSTPEnabled writes to /sys/class/net/<bridge>/bridge/stp_state.
func setSTPEnabled(bridge string, enabled bool) error {
	val := "0"
	if enabled {
		val = "1"
	}
	path := fmt.Sprintf("/sys/class/net/%s/bridge/stp_state", bridge)
	return os.WriteFile(path, []byte(val), 0644)
}

// setVLANFiltering writes to /sys/class/net/<bridge>/bridge/vlan_filtering.
func setVLANFiltering(bridge string, enabled bool) error {
	val := "0"
	if enabled {
		val = "1"
	}
	path := fmt.Sprintf("/sys/class/net/%s/bridge/vlan_filtering", bridge)
	return os.WriteFile(path, []byte(val), 0644)
}

// setForwardDelay writes to /sys/class/net/<bridge>/bridge/forward_delay.
func setForwardDelay(bridge string, centiseconds uint32) error {
	path := fmt.Sprintf("/sys/class/net/%s/bridge/forward_delay", bridge)
	return os.WriteFile(path, []byte(strconv.FormatUint(uint64(centiseconds), 10)), 0644)
}

// enslaveToMaster attaches a link to a master (bridge) device by name.
func enslaveToMaster(link netlink.Link, masterName string) error {
	master, err := netlink.LinkByName(masterName)
	if err != nil {
		return fmt.Errorf("master device %q not found: %w", masterName, err)
	}
	if err := netlink.LinkSetMaster(link, master); err != nil {
		return fmt.Errorf("failed to set master %s on %s: %w", masterName, link.Attrs().Name, err)
	}
	return nil
}

// AttachInterface attaches an existing interface to a master device (e.g. a
// Linux bridge). When hostNetwork is true the operation is performed in the
// host (root) network namespace; otherwise it targets the pod's netns.
func (c *criService) AttachInterface(ctx context.Context, sandboxID string, interfaceName string, master string, hostNetwork bool) error {
	sb, err := c.sandboxStore.Get(sandboxID)
	if err != nil {
		return fmt.Errorf("failed to find sandbox %q: %w", sandboxID, err)
	}
	if !hostNetwork && sb.NetNSPath == "" {
		return fmt.Errorf("sandbox %q has no network namespace", sandboxID)
	}

	attach := func() error {
		link, err := netlink.LinkByName(interfaceName)
		if err != nil {
			return fmt.Errorf("interface %q not found: %w", interfaceName, err)
		}
		return enslaveToMaster(link, master)
	}

	if hostNetwork {
		return attach()
	}
	return cnins.WithNetNSPath(sb.NetNSPath, func(_ cnins.NetNS) error {
		return attach()
	})
}
