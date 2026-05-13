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

// Example client for the PodNetworkManagement gRPC service.
//
// Usage:
//
//	pod-network-client [global flags] <command> [command flags]
//
// Commands:
//
//	get-network     Show interfaces, routes, and rules for a pod
//	get-ips         Show IP addresses assigned to a pod
//	get-resources   Show resources (netns path) for a pod
//	apply-route     Add a route in the pod (or host) network namespace
//	apply-rule      Add an IP rule in the pod (or host) network namespace
//	assign-ip       Assign an IP address to an interface in the pod
//	create-netdev   Create a network device in the pod (or host) namespace
//	delete-netdev   Delete a network device from the pod (or host) namespace
//	move-device     Move a device from the host into the pod namespace
//	attach          Attach an interface to a master device (e.g. bridge)
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	networking "github.com/containerd/containerd/api/services/networking/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ── Global flags ───────────────────────────────────────────────

var (
	globalAddress   = "unix:///run/containerd/networking.sock"
	globalTimeout   = 10 * time.Second
	globalSandboxID string
)

// ── Helpers ────────────────────────────────────────────────────

func connect(ctx context.Context) (*grpc.ClientConn, error) {
	return grpc.DialContext(ctx, globalAddress,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
}

func requireFlag(val, name string) {
	if val == "" {
		fmt.Fprintf(os.Stderr, "error: --%s is required\n", name)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: pod-network-client [global flags] <command> [command flags]

Global flags:
  --address      gRPC server address (default %q)
  --sandbox-id   sandbox (pod) identifier (required for most commands)
  --timeout      RPC timeout (default %s)

Commands:
  get-network     Show interfaces, routes, and rules for a pod
  get-ips         Show IP addresses assigned to a pod
  get-resources   Show resources (netns path) for a pod
  apply-route     Add a route in the pod (or host) network namespace
  apply-rule      Add an IP rule in the pod (or host) network namespace
  assign-ip       Assign an IP address to an interface in the pod
  create-netdev   Create a network device in the pod (or host) namespace
  delete-netdev   Delete a network device from the pod (or host) namespace
  move-device     Move a device from the host into the pod namespace
  attach          Attach an interface to a master device (e.g. bridge)

Run 'pod-network-client <command> --help' for command-specific flags.
`, globalAddress, globalTimeout)
	os.Exit(1)
}

// ── get-network ────────────────────────────────────────────────

func cmdGetNetwork(args []string) {
	fs := flag.NewFlagSet("get-network", flag.ExitOnError)
	//all := fs.Bool("all", false, "show all interfaces (default: eth0 only)")
	fs.Parse(args)
	requireFlag(globalSandboxID, "sandbox-id")
	sandboxID := &globalSandboxID

	ctx, cancel := context.WithTimeout(context.Background(), globalTimeout)
	defer cancel()

	conn, err := connect(ctx)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	client := networking.NewPodNetworkManagementClient(conn)
	resp, err := client.GetPodNetwork(ctx, &networking.GetPodNetworkRequest{
		SandboxId: *sandboxID,
	})
	if err != nil {
		log.Fatalf("GetPodNetwork: %v", err)
	}

	fmt.Printf("Pod network for sandbox %s\n\n", *sandboxID)

	if len(resp.Interfaces) == 0 {
		fmt.Println("  (no interfaces)")
	}
	for _, iface := range resp.Interfaces {
		//if !*all && iface.Name != "eth0" {
		//	continue
		//}
		fmt.Printf("Interface: %s\n", iface.Name)
		fmt.Printf("  MAC:   %s\n", iface.MacAddress)
		fmt.Printf("  MTU:   %d\n", iface.Mtu)
		fmt.Printf("  State: %s\n", iface.State)
		fmt.Printf("  Type:  %s\n", iface.Type)
		for _, addr := range iface.Addresses {
			fmt.Printf("  Addr:  %s\n", addr)
		}
		fmt.Println()
	}

	printRoutes(resp.Routes)
	printRules(resp.Rules)
}

// ── get-ips ────────────────────────────────────────────────────

func cmdGetIPs(args []string) {
	fs := flag.NewFlagSet("get-ips", flag.ExitOnError)
	fs.Parse(args)
	requireFlag(globalSandboxID, "sandbox-id")
	sandboxID := &globalSandboxID

	ctx, cancel := context.WithTimeout(context.Background(), globalTimeout)
	defer cancel()

	conn, err := connect(ctx)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	client := networking.NewPodNetworkManagementClient(conn)
	resp, err := client.GetPodIPs(ctx, &networking.GetPodIPsRequest{
		SandboxId: *sandboxID,
	})
	if err != nil {
		log.Fatalf("GetPodIPs: %v", err)
	}

	fmt.Printf("IP addresses for sandbox %s\n\n", *sandboxID)
	for ifaceName, ips := range resp.InterfaceIps {
		fmt.Printf("  %s: %s\n", ifaceName, strings.Join(ips.Ips, ", "))
	}
	if len(resp.Routes) > 0 {
		fmt.Println()
		for _, r := range resp.Routes {
			gw := r.Gateway
			if gw == "" {
				gw = "(direct)"
			}
			fmt.Printf("  route %s via %s dev %s\n", r.Destination, gw, r.InterfaceName)
		}
	}
}

// ── get-resources ──────────────────────────────────────────────

func cmdGetResources(args []string) {
	fs := flag.NewFlagSet("get-resources", flag.ExitOnError)
	fs.Parse(args)
	requireFlag(globalSandboxID, "sandbox-id")
	sandboxID := &globalSandboxID

	ctx, cancel := context.WithTimeout(context.Background(), globalTimeout)
	defer cancel()

	conn, err := connect(ctx)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	client := networking.NewPodNetworkManagementClient(conn)
	resp, err := client.GetPodResources(ctx, &networking.GetPodResourcesRequest{
		SandboxId: *sandboxID,
	})
	if err != nil {
		log.Fatalf("GetPodResources: %v", err)
	}

	fmt.Printf("Resources for sandbox %s\n", *sandboxID)
	fmt.Printf("  Pod network namespace: %s\n", resp.PodNetnsPath)
}

// ── apply-route ────────────────────────────────────────────────

func cmdApplyRoute(args []string) {
	fs := flag.NewFlagSet("apply-route", flag.ExitOnError)
	destination := fs.String("destination", "", "destination CIDR (e.g. 10.0.0.0/24 or default) (required)")
	gateway := fs.String("gateway", "", "gateway address (empty for direct)")
	dev := fs.String("dev", "", "interface name (e.g. eth0)")
	metric := fs.Uint("metric", 0, "route metric/priority")
	scope := fs.String("scope", "", "route scope (link, global, host)")
	hostNetwork := fs.Bool("host-network", false, "apply in the host namespace instead of the pod")
	fs.Parse(args)
	requireFlag(globalSandboxID, "sandbox-id")
	requireFlag(*destination, "destination")
	sandboxID := &globalSandboxID

	ctx, cancel := context.WithTimeout(context.Background(), globalTimeout)
	defer cancel()

	conn, err := connect(ctx)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	client := networking.NewPodNetworkManagementClient(conn)
	_, err = client.ApplyRoute(ctx, &networking.ApplyRouteRequest{
		SandboxId: *sandboxID,
		Route: &networking.RouteEntry{
			Destination:   *destination,
			Gateway:       *gateway,
			InterfaceName: *dev,
			Metric:        uint32(*metric),
			Scope:         *scope,
		},
		HostNetwork: *hostNetwork,
	})
	if err != nil {
		log.Fatalf("ApplyRoute: %v", err)
	}
	fmt.Println("Route applied successfully.")
}

// ── apply-rule ─────────────────────────────────────────────────

func cmdApplyRule(args []string) {
	fs := flag.NewFlagSet("apply-rule", flag.ExitOnError)
	priority := fs.Uint("priority", 0, "rule priority")
	src := fs.String("src", "", "source prefix (CIDR)")
	dst := fs.String("dst", "", "destination prefix (CIDR)")
	table := fs.String("table", "", "routing table (e.g. main, 254)")
	iif := fs.String("iif", "", "input interface")
	oif := fs.String("oif", "", "output interface")
	hostNetwork := fs.Bool("host-network", false, "apply in the host namespace instead of the pod")
	fs.Parse(args)
	requireFlag(globalSandboxID, "sandbox-id")
	sandboxID := &globalSandboxID

	ctx, cancel := context.WithTimeout(context.Background(), globalTimeout)
	defer cancel()

	conn, err := connect(ctx)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	client := networking.NewPodNetworkManagementClient(conn)
	_, err = client.ApplyRule(ctx, &networking.ApplyRuleRequest{
		SandboxId: *sandboxID,
		Rule: &networking.RoutingRule{
			Priority: uint32(*priority),
			Src:      *src,
			Dst:      *dst,
			Table:    *table,
			Iif:      *iif,
			Oif:      *oif,
		},
		HostNetwork: *hostNetwork,
	})
	if err != nil {
		log.Fatalf("ApplyRule: %v", err)
	}
	fmt.Println("Rule applied successfully.")
}

// ── assign-ip ──────────────────────────────────────────────────

func cmdAssignIP(args []string) {
	fs := flag.NewFlagSet("assign-ip", flag.ExitOnError)
	iface := fs.String("interface", "", "interface name inside the pod (required)")
	address := fs.String("address", "", "IP address in CIDR notation, e.g. 10.0.0.5/24 (required)")
	fs.Parse(args)
	requireFlag(globalSandboxID, "sandbox-id")
	requireFlag(*iface, "interface")
	requireFlag(*address, "address")
	sandboxID := &globalSandboxID

	ctx, cancel := context.WithTimeout(context.Background(), globalTimeout)
	defer cancel()

	conn, err := connect(ctx)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	client := networking.NewPodNetworkManagementClient(conn)
	_, err = client.AssignIPAddress(ctx, &networking.AssignIPAddressRequest{
		SandboxId:     *sandboxID,
		InterfaceName: *iface,
		Address:       *address,
	})
	if err != nil {
		log.Fatalf("AssignIPAddress: %v", err)
	}
	fmt.Printf("Assigned %s to %s successfully.\n", *address, *iface)
}

// ── create-netdev ──────────────────────────────────────────────

func cmdCreateNetdev(args []string) {
	fs := flag.NewFlagSet("create-netdev", flag.ExitOnError)
	name := fs.String("name", "", "device name (required)")
	devType := fs.String("type", "dummy", "device type: dummy, veth, vxlan, bridge, ipvlan, macvlan")
	mtu := fs.Uint("mtu", 0, "MTU (0 = kernel default)")
	addresses := fs.String("addresses", "", "comma-separated IP addresses in CIDR notation")
	hostNetwork := fs.Bool("host-network", false, "create in the host namespace")
	master := fs.String("master", "", "master (bridge) device to attach to after creation")

	// veth-specific
	peerName := fs.String("peer-name", "", "(veth) name of the peer end in the host namespace")
	peerMaster := fs.String("peer-master", "", "(veth) master device to attach the peer to")

	// vxlan-specific
	vni := fs.Uint("vni", 0, "(vxlan) VXLAN Network Identifier")
	vxlanGroup := fs.String("group", "", "(vxlan) multicast group or remote IP")
	vxlanPort := fs.Uint("port", 0, "(vxlan) UDP destination port")
	underlayDev := fs.String("underlay-device", "", "(vxlan) underlay physical device")

	// bridge-specific
	stpEnabled := fs.Bool("stp", false, "(bridge) enable STP")
	vlanFiltering := fs.Bool("vlan-filtering", false, "(bridge) enable VLAN filtering")

	// macvlan / ipvlan
	parent := fs.String("parent", "", "(macvlan/ipvlan) parent interface")
	macvlanMode := fs.String("macvlan-mode", "bridge", "(macvlan) mode: bridge, vepa, private, passthru, source")
	ipvlanMode := fs.String("ipvlan-mode", "l2", "(ipvlan) mode: l2, l3, l3s")

	fs.Parse(args)
	requireFlag(globalSandboxID, "sandbox-id")
	requireFlag(*name, "name")

	req := &networking.CreateNetdevRequest{
		SandboxId:   globalSandboxID,
		Name:        *name,
		Mtu:         uint32(*mtu),
		HostNetwork: *hostNetwork,
		Master:      *master,
	}
	if *addresses != "" {
		req.Addresses = strings.Split(*addresses, ",")
	}

	switch strings.ToLower(*devType) {
	case "dummy":
		req.Config = &networking.CreateNetdevRequest_Dummy{Dummy: &networking.DummyConfig{}}
	case "veth":
		req.Config = &networking.CreateNetdevRequest_Veth{Veth: &networking.VethConfig{
			PeerName:   *peerName,
			PeerMaster: *peerMaster,
		}}
	case "vxlan":
		req.Config = &networking.CreateNetdevRequest_Vxlan{Vxlan: &networking.VxlanConfig{
			Vni:            uint32(*vni),
			Group:          *vxlanGroup,
			Port:           uint32(*vxlanPort),
			UnderlayDevice: *underlayDev,
		}}
	case "bridge":
		req.Config = &networking.CreateNetdevRequest_Bridge{Bridge: &networking.BridgeConfig{
			StpEnabled:    *stpEnabled,
			VlanFiltering: *vlanFiltering,
		}}
	case "ipvlan":
		mode := networking.IpvlanMode_IPVLAN_L2
		switch strings.ToLower(*ipvlanMode) {
		case "l3":
			mode = networking.IpvlanMode_IPVLAN_L3
		case "l3s":
			mode = networking.IpvlanMode_IPVLAN_L3S
		}
		req.Config = &networking.CreateNetdevRequest_Ipvlan{Ipvlan: &networking.IpvlanConfig{
			Parent: *parent,
			Mode:   mode,
		}}
	case "macvlan":
		mode := networking.MacvlanMode_MACVLAN_BRIDGE
		switch strings.ToLower(*macvlanMode) {
		case "vepa":
			mode = networking.MacvlanMode_MACVLAN_VEPA
		case "private":
			mode = networking.MacvlanMode_MACVLAN_PRIVATE
		case "passthru":
			mode = networking.MacvlanMode_MACVLAN_PASSTHRU
		case "source":
			mode = networking.MacvlanMode_MACVLAN_SOURCE
		}
		req.Config = &networking.CreateNetdevRequest_Macvlan{Macvlan: &networking.MacvlanConfig{
			Parent: *parent,
			Mode:   mode,
		}}
	default:
		log.Fatalf("unknown device type %q (valid: dummy, veth, vxlan, bridge, ipvlan, macvlan)", *devType)
	}

	ctx, cancel := context.WithTimeout(context.Background(), globalTimeout)
	defer cancel()

	conn, err := connect(ctx)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	client := networking.NewPodNetworkManagementClient(conn)
	resp, err := client.CreateNetdev(ctx, req)
	if err != nil {
		log.Fatalf("CreateNetdev: %v", err)
	}
	if resp.Interface != nil {
		printInterface(resp.Interface)
	}
	if resp.PeerInterface != nil {
		fmt.Println("Peer:")
		printInterface(resp.PeerInterface)
	}
}

// ── move-device ────────────────────────────────────────────────

func cmdDeleteNetdev(args []string) {
	fs := flag.NewFlagSet("delete-netdev", flag.ExitOnError)
	name := fs.String("name", "", "device name to delete (required)")
	hostNetwork := fs.Bool("host-network", false, "delete from the host namespace instead of the pod")
	fs.Parse(args)
	requireFlag(globalSandboxID, "sandbox-id")
	requireFlag(*name, "name")

	ctx, cancel := context.WithTimeout(context.Background(), globalTimeout)
	defer cancel()

	conn, err := connect(ctx)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	client := networking.NewPodNetworkManagementClient(conn)
	_, err = client.DeleteNetdev(ctx, &networking.DeleteNetdevRequest{
		SandboxId:   globalSandboxID,
		Name:        *name,
		HostNetwork: *hostNetwork,
	})
	if err != nil {
		log.Fatalf("DeleteNetdev: %v", err)
	}
	fmt.Printf("Deleted device %s successfully.\n", *name)
}

// ── move-device ────────────────────────────────────────────────

func cmdMoveDevice(args []string) {
	fs := flag.NewFlagSet("move-device", flag.ExitOnError)
	device := fs.String("device", "", "device name in the host namespace (required)")
	devType := fs.String("type", "netdev", "device type: netdev or rdma")
	targetName := fs.String("target-name", "", "rename the device inside the pod (optional)")
	fs.Parse(args)
	requireFlag(globalSandboxID, "sandbox-id")
	requireFlag(*device, "device")

	dt := networking.DeviceType_NETDEV
	if strings.ToLower(*devType) == "rdma" {
		dt = networking.DeviceType_RDMA
	}

	ctx, cancel := context.WithTimeout(context.Background(), globalTimeout)
	defer cancel()

	conn, err := connect(ctx)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	client := networking.NewPodNetworkManagementClient(conn)
	resp, err := client.MoveDevice(ctx, &networking.MoveDeviceRequest{
		SandboxId:  globalSandboxID,
		DeviceName: *device,
		DeviceType: dt,
		TargetName: *targetName,
	})
	if err != nil {
		log.Fatalf("MoveDevice: %v", err)
	}

	fmt.Printf("Device moved → %s\n", resp.DeviceName)
	if len(resp.Addresses) > 0 {
		fmt.Printf("  Addresses: %s\n", strings.Join(resp.Addresses, ", "))
	}
	printRoutes(resp.Routes)
	printRules(resp.Rules)
}

// ── attach ─────────────────────────────────────────────────────

func cmdAttach(args []string) {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	iface := fs.String("interface", "", "interface to attach (required)")
	masterDev := fs.String("master", "", "master device to attach to (required)")
	hostNetwork := fs.Bool("host-network", false, "operate in the host namespace")
	fs.Parse(args)
	requireFlag(*iface, "interface")
	requireFlag(*masterDev, "master")

	ctx, cancel := context.WithTimeout(context.Background(), globalTimeout)
	defer cancel()

	conn, err := connect(ctx)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	client := networking.NewPodNetworkManagementClient(conn)
	_, err = client.AttachInterface(ctx, &networking.AttachInterfaceRequest{
		SandboxId:     globalSandboxID,
		InterfaceName: *iface,
		Master:        *masterDev,
		HostNetwork:   *hostNetwork,
	})
	if err != nil {
		log.Fatalf("AttachInterface: %v", err)
	}
	fmt.Printf("Attached %s to %s successfully.\n", *iface, *masterDev)
}

// ── Output helpers ─────────────────────────────────────────────

func printInterface(iface *networking.NetworkInterface) {
	fmt.Printf("Interface: %s\n", iface.Name)
	fmt.Printf("  MAC:   %s\n", iface.MacAddress)
	fmt.Printf("  MTU:   %d\n", iface.Mtu)
	fmt.Printf("  State: %s\n", iface.State)
	fmt.Printf("  Type:  %s\n", iface.Type)
	for _, addr := range iface.Addresses {
		fmt.Printf("  Addr:  %s\n", addr)
	}
}

func printRoutes(routes []*networking.RouteEntry) {
	if len(routes) == 0 {
		return
	}
	fmt.Println("Routes:")
	for _, r := range routes {
		gw := r.Gateway
		if gw == "" {
			gw = "(direct)"
		}
		fmt.Printf("  %s via %s dev %s metric %d scope %s\n",
			r.Destination, gw, r.InterfaceName, r.Metric, r.Scope)
	}
	fmt.Println()
}

func printRules(rules []*networking.RoutingRule) {
	if len(rules) == 0 {
		return
	}
	fmt.Println("Routing rules:")
	for _, rule := range rules {
		fmt.Printf("  prio %d src=%s dst=%s table=%s iif=%s oif=%s\n",
			rule.Priority, rule.Src, rule.Dst, rule.Table, rule.Iif, rule.Oif)
	}
}

// ── main ───────────────────────────────────────────────────────

func main() {
	// Parse global flags that appear before the subcommand.
	var args []string
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--address", "-address":
			i++
			if i < len(os.Args) {
				globalAddress = os.Args[i]
			}
		case "--sandbox-id", "-sandbox-id":
			i++
			if i < len(os.Args) {
				globalSandboxID = os.Args[i]
			}
		case "--timeout", "-timeout":
			i++
			if i < len(os.Args) {
				d, err := time.ParseDuration(os.Args[i])
				if err != nil {
					log.Fatalf("invalid --timeout: %v", err)
				}
				globalTimeout = d
			}
		default:
			// First non-global-flag token is the subcommand.
			args = os.Args[i:]
			goto dispatch
		}
	}

dispatch:
	if len(args) == 0 {
		usage()
	}

	cmd, cmdArgs := args[0], args[1:]

	switch cmd {
	case "get-network":
		cmdGetNetwork(cmdArgs)
	case "get-ips":
		cmdGetIPs(cmdArgs)
	case "get-resources":
		cmdGetResources(cmdArgs)
	case "apply-route":
		cmdApplyRoute(cmdArgs)
	case "apply-rule":
		cmdApplyRule(cmdArgs)
	case "assign-ip":
		cmdAssignIP(cmdArgs)
	case "create-netdev":
		cmdCreateNetdev(cmdArgs)
	case "delete-netdev":
		cmdDeleteNetdev(cmdArgs)
	case "move-device":
		cmdMoveDevice(cmdArgs)
	case "attach":
		cmdAttach(cmdArgs)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		usage()
	}
}
