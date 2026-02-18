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
	"fmt"
	"os"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	api "github.com/containerd/containerd/api/services/networking/v1"
)

// GRPCPluginConfig holds configuration for the gRPC-backed PodNetworkPlugin.
type GRPCPluginConfig struct {
	// Address is the unix socket address of the external PodNetwork gRPC server.
	Address string
	// DialTimeout is how long to wait for the initial connection.
	DialTimeout time.Duration
}

// grpcNetworkPlugin implements PodNetworkPlugin by calling an external gRPC
// service that implements the PodNetworkLifecycle service defined in networking.proto.
type grpcNetworkPlugin struct {
	address string
	conn    *grpc.ClientConn
	client  api.PodNetworkLifecycleClient

	mu     sync.Mutex
	ready  bool
	status error
}

// NewGRPCPlugin creates a PodNetworkPlugin backed by an external gRPC server.
func NewGRPCPlugin(cfg GRPCPluginConfig) (PodNetworkPlugin, error) {
	if cfg.Address == "" {
		return nil, fmt.Errorf("gRPC network plugin address is required")
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.DialTimeout)
	defer cancel()

	conn, err := grpc.DialContext(ctx, "unix://"+cfg.Address, //nolint:staticcheck
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(), //nolint:staticcheck
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to pod network service at %s: %w", cfg.Address, err)
	}

	return &grpcNetworkPlugin{
		address: cfg.Address,
		conn:    conn,
		client:  api.NewPodNetworkLifecycleClient(conn),
		ready:   true,
	}, nil
}

// SetupPodNetwork calls the external gRPC server to configure networking for a pod.
func (g *grpcNetworkPlugin) SetupPodNetwork(ctx context.Context, req SetupPodNetworkRequest) (*SetupPodNetworkResult, error) {
	// Convert to protobuf request.
	pbReq := &api.SetupPodNetworkRequest{
		SandboxId:    req.SandboxID,
		NetnsPath:    req.NetNSPath,
		PodName:      req.PodName,
		PodNamespace: req.PodNamespace,
		PodUid:       req.PodUID,
		Annotations:  req.Annotations,
		Labels:       req.Labels,
		CgroupParent: req.CgroupParent,
	}

	for _, pm := range req.PortMappings {
		pbReq.PortMappings = append(pbReq.PortMappings, &api.PortMapping{
			Protocol:      pm.Protocol,
			ContainerPort: pm.ContainerPort,
			HostPort:      pm.HostPort,
			HostIp:        pm.HostIP,
		})
	}

	if req.DNS != nil {
		pbReq.DnsConfig = &api.DNSConfig{
			Servers:  req.DNS.Servers,
			Searches: req.DNS.Searches,
			Options:  req.DNS.Options,
		}
	}

	// Pass the previous plugin's result for chaining.
	if req.PrevResult != nil {
		pbReq.PrevResult = resultToProto(req.PrevResult)
	}

	resp, err := g.client.SetupPodNetwork(ctx, pbReq)
	if err != nil {
		return nil, fmt.Errorf("gRPC SetupPodNetwork failed: %w", err)
	}

	result := &SetupPodNetworkResult{}

	for _, iface := range resp.Interfaces {
		ni := NetworkInterface{
			Name:       iface.Name,
			MACAddress: iface.MacAddress,
			MTU:        iface.Mtu,
			State:      iface.State,
			Addresses:  iface.Addresses,
		}
		if iface.Type == api.DeviceType_RDMA {
			ni.Type = RDMA
		}
		result.Interfaces = append(result.Interfaces, ni)
	}

	for _, rt := range resp.Routes {
		result.Routes = append(result.Routes, Route{
			Destination:   rt.Destination,
			Gateway:       rt.Gateway,
			InterfaceName: rt.InterfaceName,
			Metric:        rt.Metric,
			Scope:         rt.Scope,
		})
	}

	if resp.Dns != nil {
		result.DNS = &DNSConfig{
			Servers:  resp.Dns.Servers,
			Searches: resp.Dns.Searches,
			Options:  resp.Dns.Options,
		}
	}

	return result, nil
}

// TeardownPodNetwork calls the external gRPC server to remove networking for a pod.
func (g *grpcNetworkPlugin) TeardownPodNetwork(ctx context.Context, req TeardownPodNetworkRequest) error {
	pbReq := &api.TeardownPodNetworkRequest{
		SandboxId:    req.SandboxID,
		NetnsPath:    req.NetNSPath,
		PodName:      req.PodName,
		PodNamespace: req.PodNamespace,
		PodUid:       req.PodUID,
		Annotations:  req.Annotations,
		Labels:       req.Labels,
		CgroupParent: req.CgroupParent,
	}

	// Pass the final network result to teardown plugins.
	if req.PrevResult != nil {
		pbReq.PrevResult = resultToProto(req.PrevResult)
	}

	for _, pm := range req.PortMappings {
		pbReq.PortMappings = append(pbReq.PortMappings, &api.PortMapping{
			Protocol:      pm.Protocol,
			ContainerPort: pm.ContainerPort,
			HostPort:      pm.HostPort,
			HostIp:        pm.HostIP,
		})
	}

	_, err := g.client.TeardownPodNetwork(ctx, pbReq)
	if err != nil {
		return fmt.Errorf("gRPC TeardownPodNetwork failed: %w", err)
	}
	return nil
}

// Status returns nil when the plugin is connected and ready.
func (g *grpcNetworkPlugin) Status() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.ready {
		return fmt.Errorf("gRPC network plugin not ready: %w", g.status)
	}
	return nil
}

// CheckHealth performs a live health check against the gRPC network plugin.
// It first verifies the socket file exists, then calls the CheckHealth RPC
// to confirm the server is responsive and healthy.
func (g *grpcNetworkPlugin) CheckHealth(ctx context.Context) error {
	// Verify the socket file exists on disk.
	if _, err := os.Stat(g.address); err != nil {
		return fmt.Errorf("gRPC network plugin socket %s not available: %w", g.address, err)
	}

	// Call the CheckHealth RPC to verify the server is responsive.
	resp, err := g.client.CheckHealth(ctx, &api.CheckHealthRequest{})
	if err != nil {
		return fmt.Errorf("gRPC network plugin health check failed at %s: %w", g.address, err)
	}

	if !resp.Ready {
		msg := resp.Message
		if msg == "" {
			msg = "plugin reported not ready"
		}
		return fmt.Errorf("gRPC network plugin at %s is not ready: %s", g.address, msg)
	}

	return nil
}

// Close shuts down the gRPC connection.
func (g *grpcNetworkPlugin) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ready = false
	if g.conn != nil {
		return g.conn.Close()
	}
	return nil
}

// resultToProto converts a SetupPodNetworkResult to a protobuf
// SetupPodNetworkResponse for passing as prev_result in chained calls.
func resultToProto(r *SetupPodNetworkResult) *api.SetupPodNetworkResponse {
	if r == nil {
		return nil
	}
	resp := &api.SetupPodNetworkResponse{}
	for _, iface := range r.Interfaces {
		pbIface := &api.NetworkInterface{
			Name:       iface.Name,
			MacAddress: iface.MACAddress,
			Mtu:        iface.MTU,
			State:      iface.State,
			Addresses:  iface.Addresses,
		}
		if iface.Type == RDMA {
			pbIface.Type = api.DeviceType_RDMA
		}
		resp.Interfaces = append(resp.Interfaces, pbIface)
	}
	for _, rt := range r.Routes {
		resp.Routes = append(resp.Routes, &api.RouteEntry{
			Destination:   rt.Destination,
			Gateway:       rt.Gateway,
			InterfaceName: rt.InterfaceName,
			Metric:        rt.Metric,
			Scope:         rt.Scope,
		})
	}
	if r.DNS != nil {
		resp.Dns = &api.DNSConfig{
			Servers:  r.DNS.Servers,
			Searches: r.DNS.Searches,
			Options:  r.DNS.Options,
		}
	}
	return resp
}

// ChainedPodNetworkPlugin wraps multiple PodNetworkPlugin instances and
// calls them sequentially. The first plugin in the chain is the "primary"
// plugin — it is responsible for setting up the default pod network
// (assigning the pod IP, creating the main interface, etc.). Subsequent
// plugins are "chained" and each receives the accumulated result from
// all preceding plugins via PrevResult, similar to CNI chaining.
//
// During teardown, plugins are called in reverse order so that chained
// plugins can clean up before the primary network is removed.
type ChainedPodNetworkPlugin struct {
	// plugins is the ordered list of plugins. Index 0 is the primary.
	plugins []PodNetworkPlugin
	// names are human-readable identifiers matching each plugin.
	names []string
}

// NewChainedPlugin creates a ChainedPodNetworkPlugin from an ordered slice
// of plugins. The first entry is the primary plugin; the rest are chained.
// At least one plugin must be provided.
func NewChainedPlugin(plugins []PodNetworkPlugin, names []string) (*ChainedPodNetworkPlugin, error) {
	if len(plugins) == 0 {
		return nil, fmt.Errorf("at least one network plugin is required")
	}
	if len(plugins) != len(names) {
		return nil, fmt.Errorf("plugins and names must have the same length")
	}
	return &ChainedPodNetworkPlugin{
		plugins: plugins,
		names:   names,
	}, nil
}

// SetupPodNetwork calls each plugin in order. The primary plugin is called
// first with PrevResult == nil. Each subsequent plugin receives the
// accumulated result from the previous plugin. The final result is
// returned to the caller.
func (c *ChainedPodNetworkPlugin) SetupPodNetwork(ctx context.Context, req SetupPodNetworkRequest) (*SetupPodNetworkResult, error) {
	var prevResult *SetupPodNetworkResult

	for i, p := range c.plugins {
		// Clone the request and set the previous result for chaining.
		chainReq := req
		chainReq.PrevResult = prevResult

		result, err := p.SetupPodNetwork(ctx, chainReq)
		if err != nil {
			// On failure, tear down any plugins that already succeeded
			// (in reverse order).
			for j := i - 1; j >= 0; j-- {
				tearReq := TeardownPodNetworkRequest{
					SandboxID:    req.SandboxID,
					NetNSPath:    req.NetNSPath,
					PodName:      req.PodName,
					PodNamespace: req.PodNamespace,
					PodUID:       req.PodUID,
					Annotations:  req.Annotations,
					Labels:       req.Labels,
					PortMappings: req.PortMappings,
					CgroupParent: req.CgroupParent,
					PrevResult:   prevResult,
				}
				_ = c.plugins[j].TeardownPodNetwork(ctx, tearReq)
			}
			return nil, fmt.Errorf("network plugin %q (index %d) setup failed: %w", c.names[i], i, err)
		}

		// Merge the result: each subsequent plugin's result is accumulated.
		prevResult = mergeResults(prevResult, result)
	}

	return prevResult, nil
}

// TeardownPodNetwork calls each plugin in reverse order so that chained
// plugins are torn down before the primary network.
func (c *ChainedPodNetworkPlugin) TeardownPodNetwork(ctx context.Context, req TeardownPodNetworkRequest) error {
	var firstErr error
	for i := len(c.plugins) - 1; i >= 0; i-- {
		if err := c.plugins[i].TeardownPodNetwork(ctx, req); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("network plugin %q (index %d) teardown failed: %w", c.names[i], i, err)
			}
		}
	}
	return firstErr
}

// Status returns nil only when all plugins are ready.
func (c *ChainedPodNetworkPlugin) Status() error {
	for i, p := range c.plugins {
		if err := p.Status(); err != nil {
			return fmt.Errorf("network plugin %q (index %d) not ready: %w", c.names[i], i, err)
		}
	}
	return nil
}

// CheckHealth performs a live health check against all plugins in the chain.
// All plugins must pass the health check for the chain to be considered healthy.
func (c *ChainedPodNetworkPlugin) CheckHealth(ctx context.Context) error {
	for i, p := range c.plugins {
		if err := p.CheckHealth(ctx); err != nil {
			return fmt.Errorf("network plugin %q (index %d) health check failed: %w", c.names[i], i, err)
		}
	}
	return nil
}

// Close closes all plugins, returning the first error encountered.
func (c *ChainedPodNetworkPlugin) Close() error {
	var firstErr error
	for _, p := range c.plugins {
		if err := p.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// mergeResults combines two SetupPodNetworkResult values. Interfaces and
// routes from the newer result are appended. DNS from the newer result
// takes precedence if set.
func mergeResults(prev, next *SetupPodNetworkResult) *SetupPodNetworkResult {
	if prev == nil {
		return next
	}
	if next == nil {
		return prev
	}
	merged := &SetupPodNetworkResult{
		Interfaces: append(append([]NetworkInterface{}, prev.Interfaces...), next.Interfaces...),
		Routes:     append(append([]Route{}, prev.Routes...), next.Routes...),
		DNS:        prev.DNS,
	}
	if next.DNS != nil {
		merged.DNS = next.DNS
	}
	return merged
}
