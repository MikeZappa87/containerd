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

// Package proxy provides a gRPC-backed implementation of the pod.PodResourcesClient
// interface. External consumers such as the kubelet or DRA drivers can use
// NewPodResourcesClient to obtain a client that communicates with the
// containerd Pod gRPC service.
package proxy

import (
	"context"

	api "github.com/containerd/containerd/api/services/pod/v1"
	"github.com/containerd/errdefs/pkg/errgrpc"
	"google.golang.org/grpc"

	"github.com/containerd/containerd/v2/core/pod"
)

// remotePodResourcesClient is a gRPC-backed implementation of pod.PodResourcesClient.
type remotePodResourcesClient struct {
	client api.PodClient
}

// NewPodResourcesClient creates a new PodResourcesClient backed by the given
// gRPC connection. This is the primary entry point for external consumers.
func NewPodResourcesClient(conn grpc.ClientConnInterface) pod.PodResourcesClient {
	return &remotePodResourcesClient{
		client: api.NewPodClient(conn),
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
func (r *remotePodResourcesClient) GetPodIPs(ctx context.Context, sandboxID string) (*pod.PodNetworkStatus, error) {
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
	routes := make([]pod.Route, len(resp.Routes))
	for i, r := range resp.Routes {
		routes[i] = pod.Route{
			Destination:   r.Destination,
			Gateway:       r.Gateway,
			InterfaceName: r.InterfaceName,
		}
	}
	return &pod.PodNetworkStatus{
		InterfaceIPs: ifaces,
		Routes:       routes,
	}, nil
}
