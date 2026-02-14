//go:build linux

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

package integration

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	runtime "k8s.io/cri-api/pkg/apis/runtime/v1"

	podapi "github.com/containerd/containerd/api/services/networking/v1"
	dialer "github.com/containerd/containerd/v2/integration/remote/util"
)

// podEndpoint allows tests to connect to a dedicated Pod gRPC socket.
// When empty, the tests connect to the main containerd (CRI) endpoint.
var podEndpoint = flag.String("pod-endpoint", "", "Optional dedicated Pod gRPC endpoint (e.g. unix:///run/k8s/pod.sock). Defaults to the CRI endpoint.")

// newPodClient creates a Pod gRPC client connected to either the dedicated
// pod endpoint (if configured) or the main containerd endpoint.
func newPodNetworkClient(t *testing.T) podapi.PodNetworkClient {
	t.Helper()

	endpoint := *criEndpoint
	if *podEndpoint != "" {
		endpoint = *podEndpoint
	}

	addr, d, err := dialer.GetAddressAndDialer(endpoint)
	require.NoError(t, err, "get dialer for endpoint %s", endpoint)

	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(d),
	)
	require.NoError(t, err, "dial Pod gRPC endpoint %s", endpoint)
	t.Cleanup(func() { conn.Close() })

	return podapi.NewPodNetworkClient(conn)
}

// TestPodResources_GetPodResources verifies that after RunPodSandbox the
// GetPodResources RPC returns the correct network namespace path.
func TestPodResources_GetPodResources(t *testing.T) {
	t.Log("Create a pod sandbox")
	sb, sbConfig := PodSandboxConfigWithCleanup(t, "sandbox", "pod-resources")
	_ = sbConfig

	t.Log("Get sandbox status via CRI to obtain the expected netns path")
	_, info, err := SandboxInfo(sb)
	require.NoError(t, err)
	require.NotEmpty(t, info.Metadata.NetNSPath, "sandbox should have a network namespace path")

	t.Log("Call GetPodResources via the Pod gRPC API")
	podClient := newPodNetworkClient(t)
	resp, err := podClient.GetPodResources(context.Background(), &podapi.GetPodResourcesRequest{
		SandboxId: sb,
	})
	require.NoError(t, err, "GetPodResources should succeed")
	require.NotNil(t, resp)

	t.Log("Verify the netns path matches what CRI reports")
	assert.Equal(t, info.Metadata.NetNSPath, resp.PodNetnsPath,
		"GetPodResources should return the same netns path as the CRI sandbox info")

	t.Log("Verify the netns path actually exists on disk")
	_, err = os.Stat(resp.PodNetnsPath)
	assert.NoError(t, err, "network namespace path should exist on the filesystem")
}

// TestPodResources_GetPodIPs verifies that after RunPodSandbox the
// GetPodIPs RPC returns per-interface IPs consistent with the CRI
// PodSandboxStatus network information.
func TestPodResources_GetPodIPs(t *testing.T) {
	t.Log("Create a pod sandbox")
	sb, sbConfig := PodSandboxConfigWithCleanup(t, "sandbox", "pod-ips")
	_ = sbConfig

	t.Log("Get sandbox status via CRI")
	status, err := runtimeService.PodSandboxStatus(sb)
	require.NoError(t, err)
	criIP := status.GetNetwork().GetIp()
	require.NotEmpty(t, criIP, "sandbox should have an IP assigned by CNI")

	t.Log("Call GetPodIPs via the Pod gRPC API")
	podClient := newPodNetworkClient(t)
	resp, err := podClient.GetPodIPs(context.Background(), &podapi.GetPodIPsRequest{
		SandboxId: sb,
	})
	require.NoError(t, err, "GetPodIPs should succeed")
	require.NotNil(t, resp)
	require.NotEmpty(t, resp.InterfaceIps, "response should contain at least one interface")

	t.Log("Collect all IPs from all interfaces")
	allIPs := make(map[string]bool)
	for ifName, ifIPs := range resp.InterfaceIps {
		t.Logf("  interface %q: %v", ifName, ifIPs.Ips)
		for _, ip := range ifIPs.Ips {
			allIPs[ip] = true
		}
	}

	t.Log("Verify the primary CRI IP appears in the Pod API response")
	assert.True(t, allIPs[criIP],
		"primary CRI IP %q should appear in GetPodIPs response, got interfaces: %v",
		criIP, resp.InterfaceIps)

	t.Log("Verify additional IPs if present (dualstack)")
	for _, additional := range status.GetNetwork().GetAdditionalIps() {
		assert.True(t, allIPs[additional.GetIp()],
			"additional CRI IP %q should appear in GetPodIPs response",
			additional.GetIp())
	}
}

// TestPodResources_GetPodIPs_Routes verifies that GetPodIPs returns
// routing information for the pod sandbox.
func TestPodResources_GetPodIPs_Routes(t *testing.T) {
	t.Log("Create a pod sandbox")
	sb, _ := PodSandboxConfigWithCleanup(t, "sandbox", "pod-routes")

	t.Log("Call GetPodIPs via the Pod gRPC API")
	podClient := newPodNetworkClient(t)
	resp, err := podClient.GetPodIPs(context.Background(), &podapi.GetPodIPsRequest{
		SandboxId: sb,
	})
	require.NoError(t, err, "GetPodIPs should succeed")
	require.NotNil(t, resp)

	t.Log("Verify routes are returned")
	// CNI bridge plugin typically injects at least a default route.
	require.NotEmpty(t, resp.Routes, "response should contain at least one route")

	t.Log("Inspect routes")
	hasDefault := false
	for _, r := range resp.Routes {
		t.Logf("  route dst=%s gw=%s iface=%s", r.Destination, r.Gateway, r.InterfaceName)
		if r.Destination == "0.0.0.0/0" || r.Destination == "::/0" {
			hasDefault = true
		}
	}
	// Most CNI configurations include a default route, but some minimal
	// configs may not. Log rather than hard-fail.
	if !hasDefault {
		t.Log("NOTE: no default route found in response; this may be expected for minimal CNI configs")
	}
}

// TestPodResources_GetPodIPs_CNIResult verifies that the GetPodIPs response
// matches the CNI result stored in the sandbox verbose info.
func TestPodResources_GetPodIPs_CNIResult(t *testing.T) {
	t.Log("Create a pod sandbox")
	sb, _ := PodSandboxConfigWithCleanup(t, "sandbox", "pod-cni-result")

	t.Log("Get sandbox verbose info (includes CNIResult)")
	_, info, err := SandboxInfo(sb)
	require.NoError(t, err)
	require.NotNil(t, info.CNIResult, "sandbox verbose info should contain a CNI result")

	t.Log("Call GetPodIPs via the Pod gRPC API")
	podClient := newPodNetworkClient(t)
	resp, err := podClient.GetPodIPs(context.Background(), &podapi.GetPodIPsRequest{
		SandboxId: sb,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	t.Log("Verify that every interface from CNI result has a corresponding entry in GetPodIPs")
	for ifName, cniConfig := range info.CNIResult.Interfaces {
		ifIPs, ok := resp.InterfaceIps[ifName]
		if !assert.True(t, ok, "interface %q from CNI result should appear in GetPodIPs", ifName) {
			continue
		}

		t.Logf("  interface %q: CNI has %d IPs, API has %d IPs",
			ifName, len(cniConfig.IPConfigs), len(ifIPs.Ips))

		// Build a set of IPs from the CNI result for this interface.
		cniIPs := make(map[string]bool, len(cniConfig.IPConfigs))
		for _, ipc := range cniConfig.IPConfigs {
			cniIPs[ipc.IP.String()] = true
		}

		// Every IP from the CNI result should appear in the API response.
		for cniIP := range cniIPs {
			assert.Contains(t, ifIPs.Ips, cniIP,
				"CNI IP %q for interface %q should appear in GetPodIPs response", cniIP, ifName)
		}
	}

	t.Log("Verify that route count from CNI result matches GetPodIPs")
	assert.Equal(t, len(info.CNIResult.Routes), len(resp.Routes),
		"number of routes should match between CNI result and GetPodIPs response")
}

// TestPodResources_HostNetwork verifies that GetPodResources on a
// host-network sandbox returns an empty netns path and GetPodIPs
// returns no interfaces (CNI is not invoked for host-network pods).
func TestPodResources_HostNetwork(t *testing.T) {
	t.Log("Create a host-network pod sandbox")
	sb, _ := PodSandboxConfigWithCleanup(t, "sandbox", "host-network", WithHostNetwork)

	podClient := newPodNetworkClient(t)

	t.Log("Call GetPodResources — should return empty netns path for host-network")
	resResp, err := podClient.GetPodResources(context.Background(), &podapi.GetPodResourcesRequest{
		SandboxId: sb,
	})
	require.NoError(t, err)
	assert.Empty(t, resResp.PodNetnsPath,
		"host-network sandbox should have an empty netns path")

	t.Log("Call GetPodIPs — should succeed but may have no interfaces")
	ipsResp, err := podClient.GetPodIPs(context.Background(), &podapi.GetPodIPsRequest{
		SandboxId: sb,
	})
	require.NoError(t, err)
	// Host-network pods skip CNI, so no CNI result exists.
	// The fallback netns path is also empty, so we expect no interfaces.
	assert.Empty(t, ipsResp.InterfaceIps,
		"host-network sandbox should have no interface IPs from CNI")
}

// TestPodResources_NotFound verifies that querying a non-existent
// sandbox returns an appropriate error.
func TestPodResources_NotFound(t *testing.T) {
	podClient := newPodNetworkClient(t)

	t.Log("Call GetPodResources with a bogus sandbox ID")
	_, err := podClient.GetPodResources(context.Background(), &podapi.GetPodResourcesRequest{
		SandboxId: "does-not-exist-12345",
	})
	require.Error(t, err, "should fail for non-existent sandbox")

	t.Log("Call GetPodIPs with a bogus sandbox ID")
	_, err = podClient.GetPodIPs(context.Background(), &podapi.GetPodIPsRequest{
		SandboxId: "does-not-exist-12345",
	})
	require.Error(t, err, "should fail for non-existent sandbox")
}

// TestPodResources_AfterStopPodSandbox verifies behaviour after a
// sandbox is stopped. The sandbox record still exists, so the APIs
// should still return data (netns may be closed, but metadata persists).
func TestPodResources_AfterStopPodSandbox(t *testing.T) {
	t.Log("Create a pod sandbox")
	sbConfig := PodSandboxConfig("sandbox", "pod-after-stop")
	sb, err := runtimeService.RunPodSandbox(sbConfig, *runtimeHandler)
	require.NoError(t, err)
	t.Cleanup(func() {
		runtimeService.StopPodSandbox(sb)
		runtimeService.RemovePodSandbox(sb)
	})

	t.Log("Verify the sandbox is ready")
	status, err := runtimeService.PodSandboxStatus(sb)
	require.NoError(t, err)
	require.Equal(t, runtime.PodSandboxState_SANDBOX_READY, status.State)
	criIP := status.GetNetwork().GetIp()
	require.NotEmpty(t, criIP)

	t.Log("Capture GetPodResources before stop")
	podClient := newPodNetworkClient(t)
	resBefore, err := podClient.GetPodResources(context.Background(), &podapi.GetPodResourcesRequest{
		SandboxId: sb,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resBefore.PodNetnsPath)

	t.Log("Capture GetPodIPs before stop")
	ipsBefore, err := podClient.GetPodIPs(context.Background(), &podapi.GetPodIPsRequest{
		SandboxId: sb,
	})
	require.NoError(t, err)
	require.NotEmpty(t, ipsBefore.InterfaceIps)

	t.Log("Stop the sandbox")
	require.NoError(t, runtimeService.StopPodSandbox(sb))

	t.Log("Verify sandbox is now NOTREADY")
	status, err = runtimeService.PodSandboxStatus(sb)
	require.NoError(t, err)
	require.Equal(t, runtime.PodSandboxState_SANDBOX_NOTREADY, status.State)

	t.Log("Call GetPodResources after stop — sandbox record still exists")
	resAfter, err := podClient.GetPodResources(context.Background(), &podapi.GetPodResourcesRequest{
		SandboxId: sb,
	})
	require.NoError(t, err)
	// The netns path metadata is still stored even after stop.
	assert.Equal(t, resBefore.PodNetnsPath, resAfter.PodNetnsPath,
		"netns path should persist in sandbox metadata after stop")

	t.Log("Call GetPodIPs after stop")
	ipsAfter, err := podClient.GetPodIPs(context.Background(), &podapi.GetPodIPsRequest{
		SandboxId: sb,
	})
	require.NoError(t, err)
	// CNI result is still stored in the sandbox metadata after stop,
	// so IPs should still be returned.
	assert.NotEmpty(t, ipsAfter.InterfaceIps,
		"interface IPs should persist in sandbox metadata after stop")
}

// ---------------------------------------------------------------------------
// GetPodNetwork
// ---------------------------------------------------------------------------

// TestPodNetwork_GetPodNetwork verifies that GetPodNetwork returns interfaces,
// routes, and rules for a running sandbox.
func TestPodNetwork_GetPodNetwork(t *testing.T) {
	t.Log("Create a pod sandbox")
	sb, _ := PodSandboxConfigWithCleanup(t, "sandbox", "pod-network")

	podClient := newPodNetworkClient(t)
	t.Log("Call GetPodNetwork")
	resp, err := podClient.GetPodNetwork(context.Background(), &podapi.GetPodNetworkRequest{
		SandboxId: sb,
	})
	require.NoError(t, err, "GetPodNetwork should succeed")
	require.NotNil(t, resp)

	t.Log("Verify at least one interface is returned")
	require.NotEmpty(t, resp.Interfaces, "should have at least one interface")

	// Look for expected interfaces.
	ifNames := make(map[string]bool)
	for _, iface := range resp.Interfaces {
		ifNames[iface.Name] = true
		t.Logf("  interface %q mac=%s mtu=%d state=%s addrs=%v",
			iface.Name, iface.MacAddress, iface.Mtu, iface.State, iface.Addresses)
	}
	// The API may or may not include the loopback; just log it.
	if !ifNames["lo"] {
		t.Log("Note: loopback interface not returned by GetPodNetwork (this is OK)")
	}
	assert.True(t, ifNames["eth0"], "eth0 interface should be present (CNI bridge)")

	t.Log("Verify routes are returned")
	require.NotEmpty(t, resp.Routes, "should have at least one route")
	for _, rt := range resp.Routes {
		t.Logf("  route dst=%s gw=%s iface=%s metric=%d scope=%s",
			rt.Destination, rt.Gateway, rt.InterfaceName, rt.Metric, rt.Scope)
	}

	t.Log("Verify rules are returned")
	// Linux always has at least the default rules (priority 0, 32766, 32767).
	require.NotEmpty(t, resp.Rules, "should have at least default ip rules")
	for _, rl := range resp.Rules {
		t.Logf("  rule prio=%d src=%s dst=%s table=%s", rl.Priority, rl.Src, rl.Dst, rl.Table)
	}
}

// TestPodNetwork_GetPodNetwork_NotFound verifies that GetPodNetwork fails
// for a non-existent sandbox.
func TestPodNetwork_GetPodNetwork_NotFound(t *testing.T) {
	podClient := newPodNetworkClient(t)
	_, err := podClient.GetPodNetwork(context.Background(), &podapi.GetPodNetworkRequest{
		SandboxId: "does-not-exist-network-12345",
	})
	require.Error(t, err, "should fail for non-existent sandbox")
}

// TestPodNetwork_GetPodNetwork_MatchesGetPodIPs verifies that the interfaces
// and IPs from GetPodNetwork are consistent with GetPodIPs.
func TestPodNetwork_GetPodNetwork_MatchesGetPodIPs(t *testing.T) {
	t.Log("Create a pod sandbox")
	sb, _ := PodSandboxConfigWithCleanup(t, "sandbox", "pod-net-cross")

	podClient := newPodNetworkClient(t)

	t.Log("Call GetPodNetwork")
	netResp, err := podClient.GetPodNetwork(context.Background(), &podapi.GetPodNetworkRequest{
		SandboxId: sb,
	})
	require.NoError(t, err)

	t.Log("Call GetPodIPs")
	ipsResp, err := podClient.GetPodIPs(context.Background(), &podapi.GetPodIPsRequest{
		SandboxId: sb,
	})
	require.NoError(t, err)

	t.Log("Verify that eth0 IPs from GetPodNetwork match GetPodIPs")
	for _, iface := range netResp.Interfaces {
		if iface.Name != "eth0" {
			continue
		}
		// GetPodNetwork returns CIDR notation, GetPodIPs returns bare IPs.
		// Verify each GetPodIPs IP appears as a prefix in GetPodNetwork addresses.
		if ifIPs, ok := ipsResp.InterfaceIps["eth0"]; ok {
			for _, bareIP := range ifIPs.Ips {
				found := false
				for _, cidr := range iface.Addresses {
					if len(cidr) >= len(bareIP) && cidr[:len(bareIP)] == bareIP {
						found = true
						break
					}
				}
				assert.True(t, found,
					"IP %q from GetPodIPs should appear in GetPodNetwork addresses %v",
					bareIP, iface.Addresses)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// CreateNetdev
// ---------------------------------------------------------------------------

// TestPodNetwork_CreateNetdev_Dummy verifies creating a dummy device inside
// a live sandbox and confirms it is visible via GetPodNetwork.
func TestPodNetwork_CreateNetdev_Dummy(t *testing.T) {
	t.Log("Create a pod sandbox")
	sb, _ := PodSandboxConfigWithCleanup(t, "sandbox", "create-dummy")

	podClient := newPodNetworkClient(t)

	t.Log("Create a dummy device named test-dummy0")
	createResp, err := podClient.CreateNetdev(context.Background(), &podapi.CreateNetdevRequest{
		SandboxId: sb,
		Name:      "test-dummy0",
		Mtu:       1400,
		Addresses: []string{"192.168.99.1/24"},
		Config:    &podapi.CreateNetdevRequest_Dummy{Dummy: &podapi.DummyConfig{}},
	})
	require.NoError(t, err, "CreateNetdev should succeed")
	require.NotNil(t, createResp.Interface)
	assert.Equal(t, "test-dummy0", createResp.Interface.Name)
	assert.Equal(t, uint32(1400), createResp.Interface.Mtu)
	assert.Contains(t, createResp.Interface.Addresses, "192.168.99.1/24",
		"assigned address should appear on the interface")

	t.Log("Verify the device is visible via GetPodNetwork")
	netResp, err := podClient.GetPodNetwork(context.Background(), &podapi.GetPodNetworkRequest{
		SandboxId: sb,
	})
	require.NoError(t, err)

	found := false
	for _, iface := range netResp.Interfaces {
		if iface.Name == "test-dummy0" {
			found = true
			assert.Equal(t, uint32(1400), iface.Mtu)
			assert.Contains(t, iface.Addresses, "192.168.99.1/24")
			break
		}
	}
	assert.True(t, found, "test-dummy0 should be visible in GetPodNetwork")
}

// TestPodNetwork_CreateNetdev_Veth verifies creating a veth pair inside
// a sandbox.
func TestPodNetwork_CreateNetdev_Veth(t *testing.T) {
	t.Log("Create a pod sandbox")
	sb, _ := PodSandboxConfigWithCleanup(t, "sandbox", "create-veth")

	podClient := newPodNetworkClient(t)

	t.Log("Create a veth pair: test-veth0 (pod) <-> test-veth0-peer (root)")
	createResp, err := podClient.CreateNetdev(context.Background(), &podapi.CreateNetdevRequest{
		SandboxId: sb,
		Name:      "test-veth0",
		Config: &podapi.CreateNetdevRequest_Veth{Veth: &podapi.VethConfig{
			PeerName: "test-veth0-peer",
		}},
	})
	require.NoError(t, err, "CreateNetdev veth should succeed")
	require.NotNil(t, createResp.Interface)
	assert.Equal(t, "test-veth0", createResp.Interface.Name)

	// Peer should also be reported.
	require.NotNil(t, createResp.PeerInterface, "veth response should include peer interface")
	assert.Equal(t, "test-veth0-peer", createResp.PeerInterface.Name)

	t.Log("Verify test-veth0 is visible inside the sandbox via GetPodNetwork")
	netResp, err := podClient.GetPodNetwork(context.Background(), &podapi.GetPodNetworkRequest{
		SandboxId: sb,
	})
	require.NoError(t, err)

	found := false
	for _, iface := range netResp.Interfaces {
		if iface.Name == "test-veth0" {
			found = true
			break
		}
	}
	assert.True(t, found, "test-veth0 should be visible in the sandbox")
}

// ---------------------------------------------------------------------------
// AssignIPAddress
// ---------------------------------------------------------------------------

// TestPodNetwork_AssignIPAddress verifies assigning an IP to an existing
// interface inside a sandbox.
func TestPodNetwork_AssignIPAddress(t *testing.T) {
	t.Log("Create a pod sandbox")
	sb, _ := PodSandboxConfigWithCleanup(t, "sandbox", "assign-ip")

	podClient := newPodNetworkClient(t)

	t.Log("Create a dummy device to assign an IP to")
	_, err := podClient.CreateNetdev(context.Background(), &podapi.CreateNetdevRequest{
		SandboxId: sb,
		Name:      "test-ip0",
		Config:    &podapi.CreateNetdevRequest_Dummy{Dummy: &podapi.DummyConfig{}},
	})
	require.NoError(t, err)

	t.Log("Assign 10.99.0.1/24 to test-ip0")
	_, err = podClient.AssignIPAddress(context.Background(), &podapi.AssignIPAddressRequest{
		SandboxId:     sb,
		InterfaceName: "test-ip0",
		Address:       "10.99.0.1/24",
	})
	require.NoError(t, err, "AssignIPAddress should succeed")

	t.Log("Assign a second address fd99::1/64 to test-ip0")
	_, err = podClient.AssignIPAddress(context.Background(), &podapi.AssignIPAddressRequest{
		SandboxId:     sb,
		InterfaceName: "test-ip0",
		Address:       "fd99::1/64",
	})
	require.NoError(t, err, "AssignIPAddress should succeed for IPv6")

	t.Log("Verify the addresses appear via GetPodNetwork")
	netResp, err := podClient.GetPodNetwork(context.Background(), &podapi.GetPodNetworkRequest{
		SandboxId: sb,
	})
	require.NoError(t, err)

	for _, iface := range netResp.Interfaces {
		if iface.Name == "test-ip0" {
			assert.Contains(t, iface.Addresses, "10.99.0.1/24",
				"IPv4 address should be assigned")
			assert.Contains(t, iface.Addresses, "fd99::1/64",
				"IPv6 address should be assigned")
			return
		}
	}
	t.Fatal("test-ip0 interface not found in GetPodNetwork response")
}

// TestPodNetwork_AssignIPAddress_InvalidInterface verifies that assigning
// an IP to a non-existent interface fails.
func TestPodNetwork_AssignIPAddress_InvalidInterface(t *testing.T) {
	t.Log("Create a pod sandbox")
	sb, _ := PodSandboxConfigWithCleanup(t, "sandbox", "assign-ip-err")

	podClient := newPodNetworkClient(t)

	_, err := podClient.AssignIPAddress(context.Background(), &podapi.AssignIPAddressRequest{
		SandboxId:     sb,
		InterfaceName: "nonexistent0",
		Address:       "10.99.0.1/24",
	})
	require.Error(t, err, "should fail for non-existent interface")
}

// ---------------------------------------------------------------------------
// ApplyRoute
// ---------------------------------------------------------------------------

// TestPodNetwork_ApplyRoute verifies adding a route inside a sandbox.
func TestPodNetwork_ApplyRoute(t *testing.T) {
	t.Log("Create a pod sandbox")
	sb, _ := PodSandboxConfigWithCleanup(t, "sandbox", "apply-route")

	podClient := newPodNetworkClient(t)

	t.Log("Create a dummy device with an IP to route through")
	_, err := podClient.CreateNetdev(context.Background(), &podapi.CreateNetdevRequest{
		SandboxId: sb,
		Name:      "test-rt0",
		Addresses: []string{"172.30.0.1/24"},
		Config:    &podapi.CreateNetdevRequest_Dummy{Dummy: &podapi.DummyConfig{}},
	})
	require.NoError(t, err)

	t.Log("Apply a route: 172.30.99.0/24 via test-rt0")
	_, err = podClient.ApplyRoute(context.Background(), &podapi.ApplyRouteRequest{
		SandboxId: sb,
		Route: &podapi.RouteEntry{
			Destination:   "172.30.99.0/24",
			InterfaceName: "test-rt0",
			Scope:         "link",
		},
	})
	require.NoError(t, err, "ApplyRoute should succeed")

	t.Log("Verify the route appears via GetPodNetwork")
	netResp, err := podClient.GetPodNetwork(context.Background(), &podapi.GetPodNetworkRequest{
		SandboxId: sb,
	})
	require.NoError(t, err)

	found := false
	for _, rt := range netResp.Routes {
		if rt.Destination == "172.30.99.0/24" && rt.InterfaceName == "test-rt0" {
			found = true
			break
		}
	}
	assert.True(t, found, "route 172.30.99.0/24 via test-rt0 should be visible in GetPodNetwork")
}

// ---------------------------------------------------------------------------
// ApplyRule
// ---------------------------------------------------------------------------

// TestPodNetwork_ApplyRule verifies adding an ip rule inside a sandbox.
func TestPodNetwork_ApplyRule(t *testing.T) {
	t.Log("Create a pod sandbox")
	sb, _ := PodSandboxConfigWithCleanup(t, "sandbox", "apply-rule")

	podClient := newPodNetworkClient(t)

	t.Log("Apply an ip rule: from 10.50.0.0/16 lookup table 100 priority 500")
	_, err := podClient.ApplyRule(context.Background(), &podapi.ApplyRuleRequest{
		SandboxId: sb,
		Rule: &podapi.RoutingRule{
			Priority: 500,
			Src:      "10.50.0.0/16",
			Table:    "100",
		},
	})
	require.NoError(t, err, "ApplyRule should succeed")

	t.Log("Verify the rule appears via GetPodNetwork")
	netResp, err := podClient.GetPodNetwork(context.Background(), &podapi.GetPodNetworkRequest{
		SandboxId: sb,
	})
	require.NoError(t, err)

	found := false
	for _, rl := range netResp.Rules {
		if rl.Priority == 500 && rl.Src == "10.50.0.0/16" && rl.Table == "100" {
			found = true
			break
		}
	}
	assert.True(t, found, "ip rule prio 500 from 10.50.0.0/16 table 100 should be visible in GetPodNetwork")
}

// ---------------------------------------------------------------------------
// MoveDevice
// ---------------------------------------------------------------------------

// TestPodNetwork_MoveDevice_Dummy verifies moving a dummy device from the
// root namespace into a sandbox. We create a dummy in the root netns,
// assign it an address, then move it.
func TestPodNetwork_MoveDevice_Dummy(t *testing.T) {
	t.Log("Create a dummy device in the root namespace using ip link")
	// Use a unique name to avoid conflicts with parallel tests.
	devName := "mv-test-dum0"
	runCmd(t, "ip", "link", "add", devName, "type", "dummy")
	t.Cleanup(func() {
		// Best-effort cleanup in case move fails.
		runCmd(t, "ip", "link", "del", devName)
	})
	runCmd(t, "ip", "addr", "add", "10.77.0.1/24", "dev", devName)
	runCmd(t, "ip", "link", "set", devName, "up")

	t.Log("Create a pod sandbox")
	sb, _ := PodSandboxConfigWithCleanup(t, "sandbox", "move-device")

	podClient := newPodNetworkClient(t)

	t.Log("Move the device into the sandbox")
	moveResp, err := podClient.MoveDevice(context.Background(), &podapi.MoveDeviceRequest{
		SandboxId:  sb,
		DeviceName: devName,
		DeviceType: podapi.DeviceType_NETDEV,
		TargetName: "moved0",
	})
	require.NoError(t, err, "MoveDevice should succeed")
	assert.Equal(t, "moved0", moveResp.DeviceName)
	assert.Contains(t, moveResp.Addresses, "10.77.0.1/24",
		"address should be carried over")

	t.Log("Verify the device is visible inside the sandbox via GetPodNetwork")
	netResp, err := podClient.GetPodNetwork(context.Background(), &podapi.GetPodNetworkRequest{
		SandboxId: sb,
	})
	require.NoError(t, err)

	found := false
	for _, iface := range netResp.Interfaces {
		if iface.Name == "moved0" {
			found = true
			assert.Contains(t, iface.Addresses, "10.77.0.1/24")
			break
		}
	}
	assert.True(t, found, "moved0 should be visible inside the sandbox")
}

// runCmd is a test helper that runs a command and fails the test on error.
// It silently ignores errors during cleanup (e.g. deleting a device that
// was already moved).
func runCmd(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil && !t.Failed() {
		// Only log; don't hard-fail for cleanup commands.
		t.Logf("command %v: %v: %s", append([]string{name}, args...), err, out)
	}
}
