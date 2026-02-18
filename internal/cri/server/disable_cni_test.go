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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runtime "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/containerd/containerd/v2/core/networking"
	sandboxstore "github.com/containerd/containerd/v2/internal/cri/store/sandbox"
)

// mockNetPlugin is a test double for networking.PodNetworkPlugin.
type mockNetPlugin struct {
	mu sync.Mutex

	setupResult *networking.SetupPodNetworkResult
	setupErr    error
	teardownErr error
	statusErr   error
	healthErr   error
	closeErr    error

	setupCalls    int
	teardownCalls int
	healthCalls   int
	closeCalls    int

	lastSetupReq    networking.SetupPodNetworkRequest
	lastTeardownReq networking.TeardownPodNetworkRequest
}

func (m *mockNetPlugin) SetupPodNetwork(_ context.Context, req networking.SetupPodNetworkRequest) (*networking.SetupPodNetworkResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setupCalls++
	m.lastSetupReq = req
	return m.setupResult, m.setupErr
}

func (m *mockNetPlugin) TeardownPodNetwork(_ context.Context, req networking.TeardownPodNetworkRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.teardownCalls++
	m.lastTeardownReq = req
	return m.teardownErr
}

func (m *mockNetPlugin) Status() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusErr
}

func (m *mockNetPlugin) CheckHealth(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.healthCalls++
	return m.healthErr
}

func (m *mockNetPlugin) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeCalls++
	return m.closeErr
}

// --- test option helpers ---

func withDisableCNI() testOpt {
	return func(s *criService) {
		s.config.DisableCNI = true
		// Clear the default fake CNI plugin so it's clear CNI is unused.
		s.netPlugin = nil
	}
}

func withGRPCNetPlugin(p networking.PodNetworkPlugin) testOpt {
	return func(s *criService) {
		s.grpcNetPlugin = p
	}
}

// --- Status tests ---

func TestStatus_DisableCNI_GRPCPluginHealthy(t *testing.T) {
	plugin := &mockNetPlugin{}
	c := newTestCRIService(withDisableCNI(), withGRPCNetPlugin(plugin))
	c.client = newFakeContainerdClient()
	c.runtimeHandlers = make(map[string]*runtime.RuntimeHandler)

	resp, err := c.Status(context.Background(), &runtime.StatusRequest{})
	require.NoError(t, err)

	// NetworkReady should be true when gRPC plugin health check passes.
	for _, cond := range resp.Status.Conditions {
		if cond.Type == runtime.NetworkReady {
			assert.True(t, cond.Status, "expected NetworkReady=true")
			assert.Empty(t, cond.Message)
		}
	}
	assert.Equal(t, 1, plugin.healthCalls, "expected CheckHealth to be called once")
}

func TestStatus_DisableCNI_GRPCPluginUnhealthy(t *testing.T) {
	plugin := &mockNetPlugin{healthErr: fmt.Errorf("socket not found")}
	c := newTestCRIService(withDisableCNI(), withGRPCNetPlugin(plugin))
	c.client = newFakeContainerdClient()
	c.runtimeHandlers = make(map[string]*runtime.RuntimeHandler)

	resp, err := c.Status(context.Background(), &runtime.StatusRequest{})
	require.NoError(t, err)

	for _, cond := range resp.Status.Conditions {
		if cond.Type == runtime.NetworkReady {
			assert.False(t, cond.Status, "expected NetworkReady=false")
			assert.Contains(t, cond.Message, "socket not found")
			assert.Equal(t, networkNotReadyReason, cond.Reason)
		}
	}
}

func TestStatus_DisableCNI_NoGRPCPlugin(t *testing.T) {
	// DisableCNI=true but no gRPC plugin. Network should report ready since
	// there's nothing to check (the error surfaces at RunPodSandbox time).
	c := newTestCRIService(withDisableCNI())
	c.client = newFakeContainerdClient()
	c.runtimeHandlers = make(map[string]*runtime.RuntimeHandler)

	resp, err := c.Status(context.Background(), &runtime.StatusRequest{})
	require.NoError(t, err)

	for _, cond := range resp.Status.Conditions {
		if cond.Type == runtime.NetworkReady {
			assert.True(t, cond.Status, "expected NetworkReady=true when no plugin is configured")
		}
	}
}

func TestStatus_CNIEnabled_SkipsGRPCHealthCheck(t *testing.T) {
	// When DisableCNI=false, CheckHealth should NOT be called even if
	// a gRPC plugin is configured.
	plugin := &mockNetPlugin{}
	c := newTestCRIService(withGRPCNetPlugin(plugin))
	c.client = newFakeContainerdClient()
	c.runtimeHandlers = make(map[string]*runtime.RuntimeHandler)

	_, err := c.Status(context.Background(), &runtime.StatusRequest{})
	require.NoError(t, err)

	assert.Equal(t, 0, plugin.healthCalls, "CheckHealth should not be called when CNI is enabled")
}

// --- setupPodNetwork / teardownPodNetwork tests ---

func TestSetupPodNetwork_DisableCNI_NoGRPCPlugin_ReturnsError(t *testing.T) {
	// DisableCNI=true but no gRPC plugin → must fail.
	c := newTestCRIService(withDisableCNI())

	sandbox := makeFakeSandbox("sb-1", "/var/run/netns/test")
	err := c.setupPodNetwork(context.Background(), &sandbox)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "CNI is disabled but no gRPC network plugin is configured")
}

func TestSetupPodNetwork_DisableCNI_GRPCPluginSuccess(t *testing.T) {
	plugin := &mockNetPlugin{
		setupResult: &networking.SetupPodNetworkResult{
			Interfaces: []networking.NetworkInterface{
				{
					Name:      "eth0",
					Addresses: []string{"10.244.0.5/24"},
				},
			},
		},
	}
	c := newTestCRIService(withDisableCNI(), withGRPCNetPlugin(plugin))

	sandbox := makeFakeSandbox("sb-2", "/var/run/netns/test")
	err := c.setupPodNetwork(context.Background(), &sandbox)

	require.NoError(t, err)
	assert.Equal(t, 1, plugin.setupCalls)
	assert.Equal(t, "10.244.0.5", sandbox.IP)
	assert.NotNil(t, sandbox.CNIResult, "CNIResult should be set for teardown tracking")
}

func TestSetupPodNetwork_DisableCNI_GRPCPluginFailure(t *testing.T) {
	plugin := &mockNetPlugin{
		setupErr: fmt.Errorf("network setup timeout"),
	}
	c := newTestCRIService(withDisableCNI(), withGRPCNetPlugin(plugin))

	sandbox := makeFakeSandbox("sb-3", "/var/run/netns/test")
	err := c.setupPodNetwork(context.Background(), &sandbox)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "network setup timeout")
	assert.Empty(t, sandbox.IP)
}

func TestTeardownPodNetwork_DisableCNI_NoGRPCPlugin_ReturnsError(t *testing.T) {
	c := newTestCRIService(withDisableCNI())

	sandbox := makeFakeSandbox("sb-4", "/var/run/netns/test")
	err := c.teardownPodNetwork(context.Background(), sandbox)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "CNI is disabled but no gRPC network plugin is configured")
}

func TestTeardownPodNetwork_DisableCNI_GRPCPluginSuccess(t *testing.T) {
	plugin := &mockNetPlugin{}
	c := newTestCRIService(withDisableCNI(), withGRPCNetPlugin(plugin))

	sandbox := makeFakeSandbox("sb-5", "/var/run/netns/test")
	err := c.teardownPodNetwork(context.Background(), sandbox)

	require.NoError(t, err)
	assert.Equal(t, 1, plugin.teardownCalls)
	assert.Equal(t, "sb-5", plugin.lastTeardownReq.SandboxID)
}

func TestTeardownPodNetwork_DisableCNI_GRPCPluginFailure(t *testing.T) {
	plugin := &mockNetPlugin{
		teardownErr: fmt.Errorf("teardown rpc unavailable"),
	}
	c := newTestCRIService(withDisableCNI(), withGRPCNetPlugin(plugin))

	sandbox := makeFakeSandbox("sb-6", "/var/run/netns/test")
	err := c.teardownPodNetwork(context.Background(), sandbox)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "teardown rpc unavailable")
}

func TestSetupPodNetwork_CNIEnabled_DoesNotUseGRPC(t *testing.T) {
	// When DisableCNI=false, grpcNetPlugin should NOT be used even if set.
	// The FakeCNIPlugin returns (nil, nil) from Setup which causes a nil
	// deref in the CNI path — that's fine, we only care that the gRPC
	// plugin was NOT called.
	plugin := &mockNetPlugin{
		setupResult: &networking.SetupPodNetworkResult{
			Interfaces: []networking.NetworkInterface{
				{Name: "eth0", Addresses: []string{"10.0.0.1/24"}},
			},
		},
	}
	c := newTestCRIService(withGRPCNetPlugin(plugin))

	sandbox := makeFakeSandbox("sb-7", "/var/run/netns/test")

	func() {
		defer func() { recover() }() //nolint:errcheck // panic from FakeCNIPlugin nil result is expected
		_ = c.setupPodNetwork(context.Background(), &sandbox)
	}()

	assert.Equal(t, 0, plugin.setupCalls, "gRPC plugin should not be called when CNI is enabled")
}

// --- UpdateRuntimeConfig tests ---

func TestUpdateRuntimeConfig_DisableCNI_SkipsPodCIDR(t *testing.T) {
	c := newTestCRIService(withDisableCNI())

	resp, err := c.UpdateRuntimeConfig(context.Background(), &runtime.UpdateRuntimeConfigRequest{
		RuntimeConfig: &runtime.RuntimeConfig{
			NetworkConfig: &runtime.NetworkConfig{
				PodCidr: "10.244.0.0/16",
			},
		},
	})
	require.NoError(t, err)
	assert.NotNil(t, resp)
}

// --- helper ---

func makeFakeSandbox(id, netnsPath string) sandboxstore.Sandbox {
	s := sandboxstore.NewSandbox(
		sandboxstore.Metadata{
			ID:        id,
			NetNSPath: netnsPath,
			Config: &runtime.PodSandboxConfig{
				Metadata: &runtime.PodSandboxMetadata{
					Name:      "test-pod",
					Uid:       "test-uid",
					Namespace: "test-ns",
				},
				Linux: &runtime.LinuxPodSandboxConfig{},
			},
		},
		sandboxstore.Status{
			State: sandboxstore.StateReady,
		},
	)
	return s
}
