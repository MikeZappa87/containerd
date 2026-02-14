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

// Package proxy provides a gRPC-backed implementation of the networking.PodResourcesClient
// interface. External consumers such as the kubelet or DRA drivers can use
// NewPodResourcesClient to obtain a client that communicates with the
// containerd PodNetwork gRPC service.
package proxy

import (
	"context"

	api "github.com/containerd/containerd/api/services/networking/v1"
	"github.com/containerd/errdefs/pkg/errgrpc"
	"google.golang.org/grpc"

	"github.com/containerd/containerd/v2/core/networking"
)

// remotePodResourcesClient is a gRPC-backed implementation of networking.PodResourcesClient.
type remotePodResourcesClient struct {
	client api.PodNetworkClient
}

// NewPodResourcesClient creates a new PodResourcesClient backed by the given
// gRPC connection. This is the primary entry point for external consumers.
func NewPodResourcesClient(conn grpc.ClientConnInterface) networking.PodResourcesClient {
	return &remotePodResourcesClient{
		client: api.NewPodNetworkClient(conn),
	}
}

// GetPodResources returns the network namespace path for the given sandbox.
func (r *remotePodResourcesClient) GetPodResources(ctx context.Context, sandboxID string) (string, error) {
	resp, err := r.client.GetPodResources(ctx, &api.GetPodResourcesRequest{
		SandboxId: sandboxID,
	})
	if err != nil {
		return "", errgrpc.ToNative(err)
	}
	return resp.PodNetnsPath, nil
}

// GetPodIPs returns the network status (interfaces, IPs, and routes) for the given sandbox.
func (r *remotePodResourcesClient) GetPodIPs(ctx context.Context, sandboxID string) (*networking.PodNetworkStatus, error) {
	resp, err := r.client.GetPodIPs(ctx, &api.GetPodIPsRequest{
		SandboxId: sandboxID,
	})
	if err != nil {
		return nil, errgrpc.ToNative(err)
	}
	ifaces := make(map[string][]string, len(resp.InterfaceIps))
	for name, iface := range resp.InterfaceIps {
		ifaces[name] = iface.Ips
	}
	routes := make([]networking.Route, len(resp.Routes))
	for i, r := range resp.Routes {
		routes[i] = networking.Route{
			Destination:   r.Destination,
			Gateway:       r.Gateway,
			InterfaceName: r.InterfaceName,
		}
	}
	return &networking.PodNetworkStatus{
		InterfaceIPs: ifaces,
		Routes:       routes,
	}, nil
}

// GetPodNetwork returns the full network state of the given sandbox.
func (r *remotePodResourcesClient) GetPodNetwork(ctx context.Context, sandboxID string) (*networking.PodNetworkState, error) {
	resp, err := r.client.GetPodNetwork(ctx, &api.GetPodNetworkRequest{
		SandboxId: sandboxID,
	})
	if err != nil {
		return nil, errgrpc.ToNative(err)
	}

	state := &networking.PodNetworkState{}

	for _, iface := range resp.Interfaces {
		devType := networking.NetDev
		if iface.Type == api.DeviceType_RDMA {
			devType = networking.RDMA
		}
		state.Interfaces = append(state.Interfaces, networking.NetworkInterface{
			Name:       iface.Name,
			MACAddress: iface.MacAddress,
			Type:       devType,
			MTU:        iface.Mtu,
			State:      iface.State,
			Addresses:  iface.Addresses,
		})
	}

	for _, rt := range resp.Routes {
		state.Routes = append(state.Routes, networking.Route{
			Destination:   rt.Destination,
			Gateway:       rt.Gateway,
			InterfaceName: rt.InterfaceName,
			Metric:        rt.Metric,
			Scope:         rt.Scope,
		})
	}

	for _, rl := range resp.Rules {
		state.Rules = append(state.Rules, networking.RoutingRule{
			Priority: rl.Priority,
			Src:      rl.Src,
			Dst:      rl.Dst,
			Table:    rl.Table,
			IIF:      rl.Iif,
			OIF:      rl.Oif,
		})
	}

	return state, nil
}

// MoveDevice moves a network device into the pod sandbox's network namespace.
func (r *remotePodResourcesClient) MoveDevice(ctx context.Context, sandboxID string, deviceName string, deviceType networking.DeviceType, targetName string) (*networking.MoveDeviceResult, error) {
	apiType := api.DeviceType_NETDEV
	if deviceType == networking.RDMA {
		apiType = api.DeviceType_RDMA
	}

	resp, err := r.client.MoveDevice(ctx, &api.MoveDeviceRequest{
		SandboxId:  sandboxID,
		DeviceName: deviceName,
		DeviceType: apiType,
		TargetName: targetName,
	})
	if err != nil {
		return nil, errgrpc.ToNative(err)
	}

	result := &networking.MoveDeviceResult{
		DeviceName: resp.DeviceName,
		Addresses:  resp.Addresses,
	}

	for _, rt := range resp.Routes {
		result.Routes = append(result.Routes, networking.Route{
			Destination:   rt.Destination,
			Gateway:       rt.Gateway,
			InterfaceName: rt.InterfaceName,
			Metric:        rt.Metric,
			Scope:         rt.Scope,
		})
	}

	for _, rl := range resp.Rules {
		result.Rules = append(result.Rules, networking.RoutingRule{
			Priority: rl.Priority,
			Src:      rl.Src,
			Dst:      rl.Dst,
			Table:    rl.Table,
			IIF:      rl.Iif,
			OIF:      rl.Oif,
		})
	}

	return result, nil
}

// AssignIPAddress assigns an IP address to an interface inside the pod sandbox's network namespace.
func (r *remotePodResourcesClient) AssignIPAddress(ctx context.Context, sandboxID string, interfaceName string, address string) error {
	_, err := r.client.AssignIPAddress(ctx, &api.AssignIPAddressRequest{
		SandboxId:     sandboxID,
		InterfaceName: interfaceName,
		Address:       address,
	})
	if err != nil {
		return errgrpc.ToNative(err)
	}
	return nil
}

// ApplyRoute adds a route inside the pod sandbox's network namespace.
func (r *remotePodResourcesClient) ApplyRoute(ctx context.Context, sandboxID string, route networking.Route) error {
	_, err := r.client.ApplyRoute(ctx, &api.ApplyRouteRequest{
		SandboxId: sandboxID,
		Route: &api.RouteEntry{
			Destination:   route.Destination,
			Gateway:       route.Gateway,
			InterfaceName: route.InterfaceName,
			Metric:        route.Metric,
			Scope:         route.Scope,
		},
	})
	if err != nil {
		return errgrpc.ToNative(err)
	}
	return nil
}

// ApplyRule adds an ip rule inside the pod sandbox's network namespace.
func (r *remotePodResourcesClient) ApplyRule(ctx context.Context, sandboxID string, rule networking.RoutingRule) error {
	_, err := r.client.ApplyRule(ctx, &api.ApplyRuleRequest{
		SandboxId: sandboxID,
		Rule: &api.RoutingRule{
			Priority: rule.Priority,
			Src:      rule.Src,
			Dst:      rule.Dst,
			Table:    rule.Table,
			Iif:      rule.IIF,
			Oif:      rule.OIF,
		},
	})
	if err != nil {
		return errgrpc.ToNative(err)
	}
	return nil
}

// CreateNetdev creates a new network device inside the pod sandbox's network namespace.
func (r *remotePodResourcesClient) CreateNetdev(ctx context.Context, req networking.CreateNetdevRequest) (*networking.CreateNetdevResult, error) {
	apiReq := &api.CreateNetdevRequest{
		SandboxId: req.SandboxID,
		Name:      req.Name,
		Mtu:       req.MTU,
		Addresses: req.Addresses,
	}

	switch {
	case req.Veth != nil:
		apiReq.Config = &api.CreateNetdevRequest_Veth{
			Veth: &api.VethConfig{
				PeerName:      req.Veth.PeerName,
				PeerNetnsPath: req.Veth.PeerNetNSPath,
			},
		}
	case req.Vxlan != nil:
		apiReq.Config = &api.CreateNetdevRequest_Vxlan{
			Vxlan: &api.VxlanConfig{
				Vni:            req.Vxlan.VNI,
				Group:          req.Vxlan.Group,
				Port:           req.Vxlan.Port,
				UnderlayDevice: req.Vxlan.UnderlayDevice,
				Local:          req.Vxlan.Local,
				Ttl:            req.Vxlan.TTL,
				Learning:       req.Vxlan.Learning,
			},
		}
	case req.Dummy != nil:
		apiReq.Config = &api.CreateNetdevRequest_Dummy{
			Dummy: &api.DummyConfig{},
		}
	case req.IPVlan != nil:
		apiReq.Config = &api.CreateNetdevRequest_Ipvlan{
			Ipvlan: &api.IpvlanConfig{
				Parent: req.IPVlan.Parent,
				Mode:   api.IpvlanMode(req.IPVlan.Mode),
				Flag:   api.IpvlanFlag(req.IPVlan.Flag),
			},
		}
	case req.Macvlan != nil:
		apiReq.Config = &api.CreateNetdevRequest_Macvlan{
			Macvlan: &api.MacvlanConfig{
				Parent:     req.Macvlan.Parent,
				Mode:       api.MacvlanMode(req.Macvlan.Mode),
				MacAddress: req.Macvlan.MACAddress,
			},
		}
	}

	resp, err := r.client.CreateNetdev(ctx, apiReq)
	if err != nil {
		return nil, errgrpc.ToNative(err)
	}

	result := &networking.CreateNetdevResult{}
	if resp.Interface != nil {
		result.Interface = apiToNetworkInterface(resp.Interface)
	}
	if resp.PeerInterface != nil {
		pi := apiToNetworkInterface(resp.PeerInterface)
		result.PeerInterface = &pi
	}

	return result, nil
}

// apiToNetworkInterface converts an API NetworkInterface to a domain type.
func apiToNetworkInterface(iface *api.NetworkInterface) networking.NetworkInterface {
	devType := networking.NetDev
	if iface.Type == api.DeviceType_RDMA {
		devType = networking.RDMA
	}
	return networking.NetworkInterface{
		Name:       iface.Name,
		MACAddress: iface.MacAddress,
		Type:       devType,
		MTU:        iface.Mtu,
		State:      iface.State,
		Addresses:  iface.Addresses,
	}
}
