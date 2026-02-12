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

package pod

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	api "github.com/containerd/containerd/api/services/pod/v1"
	"github.com/containerd/errdefs/pkg/errgrpc"
	"github.com/containerd/log"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"

	"github.com/containerd/containerd/v2/internal/cri/server"
	"github.com/containerd/containerd/v2/plugins"
)

func init() {
	registry.Register(&plugin.Registration{
		Type: plugins.GRPCPlugin,
		ID:   "pod",
		Requires: []plugin.Type{
			plugins.GRPCPlugin,
		},
		InitFn: func(ic *plugin.InitContext) (interface{}, error) {
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

			return &podService{provider: exposer.PodResources()}, nil
		},
	})
}

type podService struct {
	provider server.PodResourcesProvider
	api.UnimplementedPodServer
}

var _ api.PodServer = (*podService)(nil)

// Register registers the Pod gRPC service with the server.
func (s *podService) Register(server *grpc.Server) error {
	api.RegisterPodServer(server, s)
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
