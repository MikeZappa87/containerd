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

package networking

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	api "github.com/containerd/containerd/api/services/networking/v1"
)

// fakePodNetworkLifecycleServer implements the PodNetworkLifecycleServer
// gRPC interface for integration testing. It records calls and returns
// configurable responses.
type fakePodNetworkLifecycleServer struct {
	api.UnimplementedPodNetworkLifecycleServer

	mu sync.Mutex

	setupCalls    int
	teardownCalls int
	healthCalls   int

	lastSetupReq    *api.SetupPodNetworkRequest
	lastTeardownReq *api.TeardownPodNetworkRequest

	setupResp   *api.SetupPodNetworkResponse
	setupErr    error
	teardownErr error
	healthReady bool
	healthMsg   string
}

func (f *fakePodNetworkLifecycleServer) SetupPodNetwork(_ context.Context, req *api.SetupPodNetworkRequest) (*api.SetupPodNetworkResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setupCalls++
	f.lastSetupReq = req
	if f.setupErr != nil {
		return nil, f.setupErr
	}
	return f.setupResp, nil
}

func (f *fakePodNetworkLifecycleServer) TeardownPodNetwork(_ context.Context, req *api.TeardownPodNetworkRequest) (*api.TeardownPodNetworkResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.teardownCalls++
	f.lastTeardownReq = req
	if f.teardownErr != nil {
		return nil, f.teardownErr
	}
	return &api.TeardownPodNetworkResponse{}, nil
}

func (f *fakePodNetworkLifecycleServer) CheckHealth(_ context.Context, _ *api.CheckHealthRequest) (*api.CheckHealthResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.healthCalls++
	return &api.CheckHealthResponse{
		Ready:   f.healthReady,
		Message: f.healthMsg,
	}, nil
}

// startFakeServer starts a gRPC server on a Unix socket in tmpDir and
// returns the socket path and a cleanup function.
func startFakeServer(t *testing.T, srv *fakePodNetworkLifecycleServer) string {
	t.Helper()

	socketPath := filepath.Join(t.TempDir(), "test-network.sock")
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to listen on unix socket: %v", err)
	}

	grpcServer := grpc.NewServer()
	api.RegisterPodNetworkLifecycleServer(grpcServer, srv)

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			// Serve returns after GracefulStop; ignore.
		}
	}()
	t.Cleanup(func() {
		grpcServer.GracefulStop()
		os.Remove(socketPath)
	})

	return socketPath
}

// ---------- Full gRPC round-trip tests ----------

// TestGRPCPlugin_SetupTeardownRoundTrip exercises the full Setup→Teardown
// flow over a real gRPC connection to a fake PodNetworkLifecycleServer.
func TestGRPCPlugin_SetupTeardownRoundTrip(t *testing.T) {
	fakeSrv := &fakePodNetworkLifecycleServer{
		setupResp: &api.SetupPodNetworkResponse{
			Interfaces: []*api.NetworkInterface{
				{
					Name:       "eth0",
					MacAddress: "aa:bb:cc:dd:ee:ff",
					Addresses:  []string{"10.244.1.5/24", "fd00::5/128"},
					Mtu:        1500,
					State:      "UP",
				},
			},
			Routes: []*api.RouteEntry{
				{
					Destination:   "0.0.0.0/0",
					Gateway:       "10.244.1.1",
					InterfaceName: "eth0",
				},
			},
			Dns: &api.DNSConfig{
				Servers:  []string{"10.96.0.10"},
				Searches: []string{"default.svc.cluster.local"},
			},
		},
		healthReady: true,
	}
	socketPath := startFakeServer(t, fakeSrv)

	// Create the real gRPC plugin pointing at the fake server.
	plugin, err := NewGRPCPlugin(GRPCPluginConfig{
		Address:     socketPath,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewGRPCPlugin: %v", err)
	}
	defer plugin.Close()

	ctx := context.Background()

	// --- CheckHealth ---
	if err := plugin.CheckHealth(ctx); err != nil {
		t.Fatalf("CheckHealth should succeed: %v", err)
	}
	if fakeSrv.healthCalls != 1 {
		t.Fatalf("expected 1 health call, got %d", fakeSrv.healthCalls)
	}

	// --- Status ---
	if err := plugin.Status(); err != nil {
		t.Fatalf("Status should return nil: %v", err)
	}

	// --- SetupPodNetwork ---
	setupReq := SetupPodNetworkRequest{
		SandboxID:    "sandbox-123",
		NetNSPath:    "/var/run/netns/cni-test",
		PodName:      "my-pod",
		PodNamespace: "default",
		PodUID:       "uid-456",
		Annotations:  map[string]string{"app": "test"},
		Labels:       map[string]string{"tier": "backend"},
		CgroupParent: "/kubepods/pod-uid-456",
		PortMappings: []PortMapping{
			{Protocol: "tcp", ContainerPort: 80, HostPort: 8080, HostIP: "0.0.0.0"},
		},
		DNS: &DNSConfig{
			Servers:  []string{"8.8.8.8"},
			Searches: []string{"example.com"},
		},
	}

	result, err := plugin.SetupPodNetwork(ctx, setupReq)
	if err != nil {
		t.Fatalf("SetupPodNetwork: %v", err)
	}

	// Verify the result was correctly deserialized from proto.
	if len(result.Interfaces) != 1 {
		t.Fatalf("expected 1 interface, got %d", len(result.Interfaces))
	}
	iface := result.Interfaces[0]
	if iface.Name != "eth0" {
		t.Errorf("expected interface name eth0, got %s", iface.Name)
	}
	if iface.MACAddress != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("expected MAC aa:bb:cc:dd:ee:ff, got %s", iface.MACAddress)
	}
	if len(iface.Addresses) != 2 {
		t.Errorf("expected 2 addresses, got %d", len(iface.Addresses))
	}
	if iface.Addresses[0] != "10.244.1.5/24" {
		t.Errorf("expected first address 10.244.1.5/24, got %s", iface.Addresses[0])
	}
	if iface.MTU != 1500 {
		t.Errorf("expected MTU 1500, got %d", iface.MTU)
	}

	// Verify routes.
	if len(result.Routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(result.Routes))
	}
	if result.Routes[0].Gateway != "10.244.1.1" {
		t.Errorf("expected gateway 10.244.1.1, got %s", result.Routes[0].Gateway)
	}

	// Verify DNS.
	if result.DNS == nil {
		t.Fatal("expected DNS config")
	}
	if len(result.DNS.Servers) != 1 || result.DNS.Servers[0] != "10.96.0.10" {
		t.Errorf("expected DNS server 10.96.0.10, got %v", result.DNS.Servers)
	}

	// Verify the request was correctly serialized to proto on the server side.
	fakeSrv.mu.Lock()
	lastReq := fakeSrv.lastSetupReq
	fakeSrv.mu.Unlock()

	if lastReq.SandboxId != "sandbox-123" {
		t.Errorf("server received wrong sandbox_id: %s", lastReq.SandboxId)
	}
	if lastReq.PodName != "my-pod" {
		t.Errorf("server received wrong pod_name: %s", lastReq.PodName)
	}
	if lastReq.PodNamespace != "default" {
		t.Errorf("server received wrong pod_namespace: %s", lastReq.PodNamespace)
	}
	if lastReq.NetnsPath != "/var/run/netns/cni-test" {
		t.Errorf("server received wrong netns_path: %s", lastReq.NetnsPath)
	}
	if len(lastReq.PortMappings) != 1 || lastReq.PortMappings[0].ContainerPort != 80 {
		t.Errorf("server received wrong port mappings: %v", lastReq.PortMappings)
	}
	if lastReq.DnsConfig == nil || lastReq.DnsConfig.Servers[0] != "8.8.8.8" {
		t.Errorf("server received wrong dns config: %v", lastReq.DnsConfig)
	}
	if lastReq.Annotations["app"] != "test" {
		t.Errorf("server received wrong annotations: %v", lastReq.Annotations)
	}
	if fakeSrv.setupCalls != 1 {
		t.Errorf("expected 1 setup call, got %d", fakeSrv.setupCalls)
	}

	// --- TeardownPodNetwork ---
	teardownReq := TeardownPodNetworkRequest{
		SandboxID:    "sandbox-123",
		NetNSPath:    "/var/run/netns/cni-test",
		PodName:      "my-pod",
		PodNamespace: "default",
		PodUID:       "uid-456",
		PortMappings: []PortMapping{
			{Protocol: "tcp", ContainerPort: 80, HostPort: 8080, HostIP: "0.0.0.0"},
		},
	}

	if err := plugin.TeardownPodNetwork(ctx, teardownReq); err != nil {
		t.Fatalf("TeardownPodNetwork: %v", err)
	}

	fakeSrv.mu.Lock()
	lastTeardown := fakeSrv.lastTeardownReq
	fakeSrv.mu.Unlock()

	if lastTeardown.SandboxId != "sandbox-123" {
		t.Errorf("teardown server received wrong sandbox_id: %s", lastTeardown.SandboxId)
	}
	if fakeSrv.teardownCalls != 1 {
		t.Errorf("expected 1 teardown call, got %d", fakeSrv.teardownCalls)
	}

	// --- Close ---
	if err := plugin.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestGRPCPlugin_CheckHealthNotReady verifies that a server reporting
// Ready=false causes CheckHealth to return an error.
func TestGRPCPlugin_CheckHealthNotReady(t *testing.T) {
	fakeSrv := &fakePodNetworkLifecycleServer{
		setupResp:   &api.SetupPodNetworkResponse{},
		healthReady: false,
		healthMsg:   "backend initializing",
	}
	socketPath := startFakeServer(t, fakeSrv)

	plugin, err := NewGRPCPlugin(GRPCPluginConfig{
		Address:     socketPath,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewGRPCPlugin: %v", err)
	}
	defer plugin.Close()

	err = plugin.CheckHealth(context.Background())
	if err == nil {
		t.Fatal("expected error from CheckHealth when server is not ready")
	}
	if got := err.Error(); !contains(got, "backend initializing") && !contains(got, "not ready") {
		t.Errorf("unexpected error: %s", got)
	}
}

// TestGRPCPlugin_SetupPrevResultChaining verifies that PrevResult from a
// chained plugin setup is correctly forwarded over the wire.
func TestGRPCPlugin_SetupPrevResultChaining(t *testing.T) {
	fakeSrv := &fakePodNetworkLifecycleServer{
		setupResp: &api.SetupPodNetworkResponse{
			Interfaces: []*api.NetworkInterface{
				{Name: "net1", Addresses: []string{"192.168.1.100/24"}},
			},
		},
		healthReady: true,
	}
	socketPath := startFakeServer(t, fakeSrv)

	plugin, err := NewGRPCPlugin(GRPCPluginConfig{
		Address:     socketPath,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewGRPCPlugin: %v", err)
	}
	defer plugin.Close()

	// Simulate a chained call where a previous plugin already set up eth0.
	prevResult := &SetupPodNetworkResult{
		Interfaces: []NetworkInterface{
			{Name: "eth0", Addresses: []string{"10.0.0.2/24"}, MACAddress: "00:11:22:33:44:55"},
		},
		DNS: &DNSConfig{Servers: []string{"10.96.0.10"}},
	}

	req := SetupPodNetworkRequest{
		SandboxID:    "sb-chain",
		NetNSPath:    "/var/run/netns/chain",
		PodName:      "chained-pod",
		PodNamespace: "kube-system",
		PrevResult:   prevResult,
	}

	result, err := plugin.SetupPodNetwork(context.Background(), req)
	if err != nil {
		t.Fatalf("SetupPodNetwork with PrevResult: %v", err)
	}

	// The secondary returned net1.
	if len(result.Interfaces) != 1 || result.Interfaces[0].Name != "net1" {
		t.Errorf("expected net1 interface, got %v", result.Interfaces)
	}

	// Verify the server received the PrevResult.
	fakeSrv.mu.Lock()
	pbPrev := fakeSrv.lastSetupReq.PrevResult
	fakeSrv.mu.Unlock()

	if pbPrev == nil {
		t.Fatal("server should have received PrevResult")
	}
	if len(pbPrev.Interfaces) != 1 || pbPrev.Interfaces[0].Name != "eth0" {
		t.Errorf("server received wrong PrevResult: %v", pbPrev.Interfaces)
	}
	if pbPrev.Dns == nil || len(pbPrev.Dns.Servers) != 1 || pbPrev.Dns.Servers[0] != "10.96.0.10" {
		t.Errorf("server received wrong PrevResult DNS: %v", pbPrev.Dns)
	}
}

// TestGRPCPlugin_MultipleInterfacesAndDualStack verifies that multiple
// interfaces with dual-stack addresses are correctly round-tripped.
func TestGRPCPlugin_MultipleInterfacesAndDualStack(t *testing.T) {
	fakeSrv := &fakePodNetworkLifecycleServer{
		setupResp: &api.SetupPodNetworkResponse{
			Interfaces: []*api.NetworkInterface{
				{
					Name:       "eth0",
					MacAddress: "aa:bb:cc:dd:ee:01",
					Addresses:  []string{"10.244.0.10/24", "fd00::10/128"},
				},
				{
					Name:       "net1",
					MacAddress: "aa:bb:cc:dd:ee:02",
					Addresses:  []string{"192.168.1.10/24"},
					Type:       api.DeviceType_RDMA,
				},
			},
		},
		healthReady: true,
	}
	socketPath := startFakeServer(t, fakeSrv)

	plugin, err := NewGRPCPlugin(GRPCPluginConfig{
		Address:     socketPath,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewGRPCPlugin: %v", err)
	}
	defer plugin.Close()

	result, err := plugin.SetupPodNetwork(context.Background(), SetupPodNetworkRequest{
		SandboxID: "sb-dual",
		NetNSPath: "/var/run/netns/dual",
		PodName:   "dual-stack-pod",
	})
	if err != nil {
		t.Fatalf("SetupPodNetwork: %v", err)
	}

	if len(result.Interfaces) != 2 {
		t.Fatalf("expected 2 interfaces, got %d", len(result.Interfaces))
	}

	// First interface: eth0, dual-stack.
	eth0 := result.Interfaces[0]
	if eth0.Name != "eth0" {
		t.Errorf("expected eth0, got %s", eth0.Name)
	}
	if len(eth0.Addresses) != 2 {
		t.Errorf("expected 2 addresses on eth0, got %d", len(eth0.Addresses))
	}

	// Second interface: net1, RDMA.
	net1 := result.Interfaces[1]
	if net1.Name != "net1" {
		t.Errorf("expected net1, got %s", net1.Name)
	}
	if net1.Type != RDMA {
		t.Errorf("expected RDMA type, got %d", net1.Type)
	}
}

// contains is a simple helper; avoids importing strings for one use.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
