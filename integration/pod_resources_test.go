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
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	runtime "k8s.io/cri-api/pkg/apis/runtime/v1"

	podapi "github.com/containerd/containerd/api/services/pod/v1"
	dialer "github.com/containerd/containerd/v2/integration/remote/util"
)

// newPodClient creates a Pod gRPC client connected to the containerd endpoint.
func newPodClient(t *testing.T) podapi.PodClient {
	t.Helper()
	addr, d, err := dialer.GetAddressAndDialer(*criEndpoint)
	require.NoError(t, err, "get dialer for endpoint %s", *criEndpoint)

	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(d),
	)
	require.NoError(t, err, "dial containerd gRPC endpoint")
	t.Cleanup(func() { conn.Close() })

	return podapi.NewPodClient(conn)
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
	podClient := newPodClient(t)
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
	podClient := newPodClient(t)
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
	podClient := newPodClient(t)
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
	podClient := newPodClient(t)
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

	podClient := newPodClient(t)

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
	podClient := newPodClient(t)

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
	podClient := newPodClient(t)
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
