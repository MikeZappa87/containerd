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
	"net"
	"os"
	"path/filepath"

	"google.golang.org/grpc"

	api "github.com/containerd/containerd/api/services/networking/v1"
	"github.com/containerd/errdefs/pkg/errgrpc"
	"github.com/containerd/log"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"

	corenetworking "github.com/containerd/containerd/v2/core/networking"
	"github.com/containerd/containerd/v2/internal/cri/server"
	"github.com/containerd/containerd/v2/plugins"
)

// Config holds the configuration for the PodNetwork gRPC service plugin.
type Config struct {
	// Address is the unix socket address for a dedicated Pod API listener.
	// When empty (default), the Pod service is registered on the main
	// containerd gRPC socket. When set (e.g. "/run/k8s/pod.sock"),
	// a standalone gRPC server is started on that socket instead.
	Address string `toml:"address" json:"address"`

	// UID is the unix socket owner user id when using a dedicated address.
	UID int `toml:"uid" json:"uid"`

	// GID is the unix socket owner group id when using a dedicated address.
	GID int `toml:"gid" json:"gid"`
}

func init() {
	defaultConfig := Config{
		Address: "/run/k8s/network.sock",
	}

	registry.Register(&plugin.Registration{
		Type: plugins.GRPCPlugin,
		ID:   "networking",
		Requires: []plugin.Type{
			plugins.GRPCPlugin,
		},
		Config: &defaultConfig,
		InitFn: func(ic *plugin.InitContext) (interface{}, error) {
			config := ic.Config.(*Config)

			criPlugin, err := ic.GetByID(plugins.GRPCPlugin, "cri")
			if err != nil {
				return nil, fmt.Errorf("unable to load CRI gRPC plugin: %w", err)
			}

			type podResourcesExposer interface {
				PodResources() server.PodResourcesProvider
			}

			exposer, ok := criPlugin.(podResourcesExposer)
			if !ok {
				return nil, fmt.Errorf("CRI gRPC plugin does not expose PodResourcesProvider")
			}

			svc := &podService{provider: exposer.PodResources()}

			// When an explicit address is configured, start a dedicated
			// gRPC server on that socket instead of registering on the
			// main containerd socket.
			if config.Address != "" {
				if err := svc.startDedicatedServer(ic.Context, config); err != nil {
					return nil, fmt.Errorf("failed to start dedicated pod gRPC server: %w", err)
				}
				log.G(ic.Context).WithField("address", config.Address).Info("networking gRPC service listening on dedicated socket")
			}

			return svc, nil
		},
	})
}

type podService struct {
	provider server.PodResourcesProvider
	api.UnimplementedPodNetworkLifecycleServer
	api.UnimplementedPodNetworkManagementServer

	// dedicated is non-nil when the plugin runs its own gRPC server.
	dedicated *grpc.Server
	listener  net.Listener
}

var (
	_ api.PodNetworkLifecycleServer  = (*podService)(nil)
	_ api.PodNetworkManagementServer = (*podService)(nil)
)

// Register registers the Pod gRPC service with the shared containerd
// gRPC server. The service is always registered on the main socket.
// When a dedicated address is also configured, it is available on both.
func (s *podService) Register(srv *grpc.Server) error {
	api.RegisterPodNetworkLifecycleServer(srv, s)
	api.RegisterPodNetworkManagementServer(srv, s)
	return nil
}

// Close shuts down the dedicated gRPC server, if one was started.
func (s *podService) Close() error {
	if s.dedicated != nil {
		s.dedicated.GracefulStop()
	}
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// startDedicatedServer creates a unix socket at config.Address and starts
// serving the Pod gRPC service in a background goroutine.
func (s *podService) startDedicatedServer(ctx context.Context, config *Config) error {
	addr := config.Address

	// Ensure the parent directory exists.
	dir := filepath.Dir(addr)
	if err := os.MkdirAll(dir, 0o770); err != nil {
		return fmt.Errorf("failed to create socket directory %s: %w", dir, err)
	}

	// Remove stale socket if present.
	if err := os.Remove(addr); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove existing socket %s: %w", addr, err)
	}

	l, err := net.Listen("unix", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	// Apply permissions.
	if err := os.Chmod(addr, 0o660); err != nil {
		l.Close()
		return fmt.Errorf("failed to chmod socket %s: %w", addr, err)
	}
	if err := os.Chown(addr, config.UID, config.GID); err != nil {
		l.Close()
		return fmt.Errorf("failed to chown socket %s: %w", addr, err)
	}

	srv := grpc.NewServer()
	api.RegisterPodNetworkLifecycleServer(srv, s)
	api.RegisterPodNetworkManagementServer(srv, s)

	s.dedicated = srv
	s.listener = l

	go func() {
		if err := srv.Serve(l); err != nil {
			log.G(ctx).WithError(err).WithField("address", addr).Error("networking dedicated gRPC server exited")
		}
	}()

	return nil
}

// GetPodResources returns the network namespace path for a sandbox.
func (s *podService) GetPodResources(ctx context.Context, req *api.GetPodResourcesRequest) (*api.GetPodResourcesResponse, error) {
	log.G(ctx).WithField("sandbox_id", req.SandboxId).Debug("get pod resources")

	netnsPath, err := s.provider.GetPodResources(ctx, req.SandboxId)
	if err != nil {
		return nil, errgrpc.ToGRPC(err)
	}

	return &api.GetPodResourcesResponse{
		PodNetnsPath: netnsPath,
	}, nil
}

// SetupPodNetwork is handled internally by the CRI sandbox lifecycle.
// This RPC is exposed for external orchestration use cases where the caller
// manages sandbox creation independently.
func (s *podService) SetupPodNetwork(ctx context.Context, req *api.SetupPodNetworkRequest) (*api.SetupPodNetworkResponse, error) {
	log.G(ctx).WithField("sandbox_id", req.SandboxId).Debug("SetupPodNetwork called via gRPC service (no-op: handled by CRI lifecycle)")
	// Setup is managed by the CRI sandbox lifecycle using the gRPC network
	// plugin client directly. Return unimplemented for external callers.
	return nil, fmt.Errorf("SetupPodNetwork is managed by the CRI sandbox lifecycle and cannot be invoked externally")
}

// TeardownPodNetwork is handled internally by the CRI sandbox lifecycle.
func (s *podService) TeardownPodNetwork(ctx context.Context, req *api.TeardownPodNetworkRequest) (*api.TeardownPodNetworkResponse, error) {
	log.G(ctx).WithField("sandbox_id", req.SandboxId).Debug("TeardownPodNetwork called via gRPC service (no-op: handled by CRI lifecycle)")
	return nil, fmt.Errorf("TeardownPodNetwork is managed by the CRI sandbox lifecycle and cannot be invoked externally")
}

// CheckHealth reports the health of the pod network service.
func (s *podService) CheckHealth(ctx context.Context, req *api.CheckHealthRequest) (*api.CheckHealthResponse, error) {
	return &api.CheckHealthResponse{
		Ready:   true,
		Message: "containerd networking service is running",
	}, nil
}

// GetPodIPs returns the network status (interfaces, IPs, and routes) for a sandbox.
func (s *podService) GetPodIPs(ctx context.Context, req *api.GetPodIPsRequest) (*api.GetPodIPsResponse, error) {
	log.G(ctx).WithField("sandbox_id", req.SandboxId).Debug("get pod ips")

	status, err := s.provider.GetPodIPs(ctx, req.SandboxId)
	if err != nil {
		return nil, errgrpc.ToGRPC(err)
	}

	if status == nil {
		return &api.GetPodIPsResponse{}, nil
	}

	ifaces := make(map[string]*api.PodInterfaceIPs, len(status.InterfaceIPs))
	for name, ips := range status.InterfaceIPs {
		ifaces[name] = &api.PodInterfaceIPs{
			Ips: ips,
		}
	}

	routes := make([]*api.PodRoute, len(status.Routes))
	for i, rt := range status.Routes {
		routes[i] = &api.PodRoute{
			Destination:   rt.Destination,
			Gateway:       rt.Gateway,
			InterfaceName: rt.InterfaceName,
		}
	}

	return &api.GetPodIPsResponse{
		InterfaceIps: ifaces,
		Routes:       routes,
	}, nil
}

// GetPodNetwork returns the full network state of a sandbox.
func (s *podService) GetPodNetwork(ctx context.Context, req *api.GetPodNetworkRequest) (*api.GetPodNetworkResponse, error) {
	log.G(ctx).WithField("sandbox_id", req.SandboxId).Debug("get pod network")

	state, err := s.provider.GetPodNetwork(ctx, req.SandboxId)
	if err != nil {
		return nil, errgrpc.ToGRPC(err)
	}

	if state == nil {
		return &api.GetPodNetworkResponse{}, nil
	}

	resp := &api.GetPodNetworkResponse{}

	for _, iface := range state.Interfaces {
		devType := api.DeviceType_NETDEV
		if iface.Type == corenetworking.RDMA {
			devType = api.DeviceType_RDMA
		}
		resp.Interfaces = append(resp.Interfaces, &api.NetworkInterface{
			Name:       iface.Name,
			MacAddress: iface.MACAddress,
			Type:       devType,
			Mtu:        iface.MTU,
			State:      iface.State,
			Addresses:  iface.Addresses,
		})
	}

	for _, rt := range state.Routes {
		resp.Routes = append(resp.Routes, &api.RouteEntry{
			Destination:   rt.Destination,
			Gateway:       rt.Gateway,
			InterfaceName: rt.InterfaceName,
			Metric:        rt.Metric,
			Scope:         rt.Scope,
		})
	}

	for _, rl := range state.Rules {
		resp.Rules = append(resp.Rules, &api.RoutingRule{
			Priority: rl.Priority,
			Src:      rl.Src,
			Dst:      rl.Dst,
			Table:    rl.Table,
			Iif:      rl.IIF,
			Oif:      rl.OIF,
		})
	}

	return resp, nil
}

// MoveDevice moves a network device from the root netns into the pod's netns.
func (s *podService) MoveDevice(ctx context.Context, req *api.MoveDeviceRequest) (*api.MoveDeviceResponse, error) {
	log.G(ctx).WithField("sandbox_id", req.SandboxId).WithField("device", req.DeviceName).Debug("move device")

	var devType corenetworking.DeviceType
	switch req.DeviceType {
	case api.DeviceType_RDMA:
		devType = corenetworking.RDMA
	default:
		devType = corenetworking.NetDev
	}

	result, err := s.provider.MoveDevice(ctx, req.SandboxId, req.DeviceName, devType, req.TargetName)
	if err != nil {
		return nil, errgrpc.ToGRPC(err)
	}

	resp := &api.MoveDeviceResponse{
		DeviceName: result.DeviceName,
		Addresses:  result.Addresses,
	}

	for _, rt := range result.Routes {
		resp.Routes = append(resp.Routes, &api.RouteEntry{
			Destination:   rt.Destination,
			Gateway:       rt.Gateway,
			InterfaceName: rt.InterfaceName,
			Metric:        rt.Metric,
			Scope:         rt.Scope,
		})
	}

	for _, rl := range result.Rules {
		resp.Rules = append(resp.Rules, &api.RoutingRule{
			Priority: rl.Priority,
			Src:      rl.Src,
			Dst:      rl.Dst,
			Table:    rl.Table,
			Iif:      rl.IIF,
			Oif:      rl.OIF,
		})
	}

	return resp, nil
}

// AssignIPAddress assigns an IP address to an interface inside the pod netns.
func (s *podService) AssignIPAddress(ctx context.Context, req *api.AssignIPAddressRequest) (*api.AssignIPAddressResponse, error) {
	log.G(ctx).WithField("sandbox_id", req.SandboxId).WithField("interface", req.InterfaceName).Debug("assign ip address")

	if err := s.provider.AssignIPAddress(ctx, req.SandboxId, req.InterfaceName, req.Address); err != nil {
		return nil, errgrpc.ToGRPC(err)
	}

	return &api.AssignIPAddressResponse{}, nil
}

// ApplyRoute adds a route inside the pod's network namespace.
func (s *podService) ApplyRoute(ctx context.Context, req *api.ApplyRouteRequest) (*api.ApplyRouteResponse, error) {
	log.G(ctx).WithField("sandbox_id", req.SandboxId).Debug("apply route")

	if req.Route == nil {
		return nil, fmt.Errorf("route is required")
	}

	rt := corenetworking.Route{
		Destination:   req.Route.Destination,
		Gateway:       req.Route.Gateway,
		InterfaceName: req.Route.InterfaceName,
		Metric:        req.Route.Metric,
		Scope:         req.Route.Scope,
	}

	if err := s.provider.ApplyRoute(ctx, req.SandboxId, rt, req.HostNetwork); err != nil {
		return nil, errgrpc.ToGRPC(err)
	}

	return &api.ApplyRouteResponse{}, nil
}

// ApplyRule adds an ip rule inside the pod's network namespace.
func (s *podService) ApplyRule(ctx context.Context, req *api.ApplyRuleRequest) (*api.ApplyRuleResponse, error) {
	log.G(ctx).WithField("sandbox_id", req.SandboxId).Debug("apply rule")

	if req.Rule == nil {
		return nil, fmt.Errorf("rule is required")
	}

	rl := corenetworking.RoutingRule{
		Priority: req.Rule.Priority,
		Src:      req.Rule.Src,
		Dst:      req.Rule.Dst,
		Table:    req.Rule.Table,
		IIF:      req.Rule.Iif,
		OIF:      req.Rule.Oif,
	}

	if err := s.provider.ApplyRule(ctx, req.SandboxId, rl, req.HostNetwork); err != nil {
		return nil, errgrpc.ToGRPC(err)
	}

	return &api.ApplyRuleResponse{}, nil
}

// CreateNetdev creates a new network device inside the pod's network namespace.
func (s *podService) CreateNetdev(ctx context.Context, req *api.CreateNetdevRequest) (*api.CreateNetdevResponse, error) {
	log.G(ctx).WithField("sandbox_id", req.SandboxId).WithField("name", req.Name).Debug("create netdev")

	domReq := corenetworking.CreateNetdevRequest{
		SandboxID:   req.SandboxId,
		Name:        req.Name,
		MTU:         req.Mtu,
		Addresses:   req.Addresses,
		HostNetwork: req.HostNetwork,
		Master:      req.Master,
	}

	switch cfg := req.Config.(type) {
	case *api.CreateNetdevRequest_Veth:
		domReq.Veth = &corenetworking.VethConfig{
			PeerName:      cfg.Veth.PeerName,
			PeerNetNSPath: cfg.Veth.PeerNetnsPath,
			PeerMaster:    cfg.Veth.PeerMaster,
		}
	case *api.CreateNetdevRequest_Vxlan:
		domReq.Vxlan = &corenetworking.VxlanConfig{
			VNI:            cfg.Vxlan.Vni,
			Group:          cfg.Vxlan.Group,
			Port:           cfg.Vxlan.Port,
			UnderlayDevice: cfg.Vxlan.UnderlayDevice,
			Local:          cfg.Vxlan.Local,
			TTL:            cfg.Vxlan.Ttl,
			Learning:       cfg.Vxlan.Learning,
		}
	case *api.CreateNetdevRequest_Dummy:
		domReq.Dummy = &corenetworking.DummyConfig{}
	case *api.CreateNetdevRequest_Ipvlan:
		domReq.IPVlan = &corenetworking.IPVlanConfig{
			Parent: cfg.Ipvlan.Parent,
			Mode:   corenetworking.IPVlanMode(cfg.Ipvlan.Mode),
			Flag:   corenetworking.IPVlanFlag(cfg.Ipvlan.Flag),
		}
	case *api.CreateNetdevRequest_Macvlan:
		domReq.Macvlan = &corenetworking.MacvlanConfig{
			Parent:     cfg.Macvlan.Parent,
			Mode:       corenetworking.MacvlanMode(cfg.Macvlan.Mode),
			MACAddress: cfg.Macvlan.MacAddress,
		}
	case *api.CreateNetdevRequest_Bridge:
		domReq.Bridge = &corenetworking.BridgeConfig{
			STPEnabled:    cfg.Bridge.StpEnabled,
			VLANFiltering: cfg.Bridge.VlanFiltering,
			ForwardDelay:  cfg.Bridge.ForwardDelay,
			DefaultPVID:   cfg.Bridge.DefaultPvid,
		}
	default:
		return nil, fmt.Errorf("exactly one device config must be specified")
	}

	result, err := s.provider.CreateNetdev(ctx, domReq)
	if err != nil {
		return nil, errgrpc.ToGRPC(err)
	}

	resp := &api.CreateNetdevResponse{
		Interface: networkInterfaceToAPI(result.Interface),
	}
	if result.PeerInterface != nil {
		resp.PeerInterface = networkInterfaceToAPI(*result.PeerInterface)
	}

	return resp, nil
}

// networkInterfaceToAPI converts a domain NetworkInterface to the API type.
func networkInterfaceToAPI(iface corenetworking.NetworkInterface) *api.NetworkInterface {
	devType := api.DeviceType_NETDEV
	if iface.Type == corenetworking.RDMA {
		devType = api.DeviceType_RDMA
	}
	return &api.NetworkInterface{
		Name:       iface.Name,
		MacAddress: iface.MACAddress,
		Type:       devType,
		Mtu:        iface.MTU,
		State:      iface.State,
		Addresses:  iface.Addresses,
	}
}

// AttachInterface attaches an existing interface to a master device.
func (s *podService) AttachInterface(ctx context.Context, req *api.AttachInterfaceRequest) (*api.AttachInterfaceResponse, error) {
	log.G(ctx).WithField("sandbox_id", req.SandboxId).WithField("interface", req.InterfaceName).WithField("master", req.Master).Debug("attach interface")

	if err := s.provider.AttachInterface(ctx, req.SandboxId, req.InterfaceName, req.Master, req.HostNetwork); err != nil {
		return nil, errgrpc.ToGRPC(err)
	}

	return &api.AttachInterfaceResponse{}, nil
}
