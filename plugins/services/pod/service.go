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
	"net"
	"os"
	"path/filepath"

	"google.golang.org/grpc"

	api "github.com/containerd/containerd/api/services/pod/v1"
	"github.com/containerd/errdefs/pkg/errgrpc"
	"github.com/containerd/log"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"

	"github.com/containerd/containerd/v2/internal/cri/server"
	"github.com/containerd/containerd/v2/plugins"
)

// Config holds the configuration for the Pod gRPC service plugin.
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
		Address: "/run/k8s/pod.sock",
	}

	registry.Register(&plugin.Registration{
		Type: plugins.GRPCPlugin,
		ID:   "pod",
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
				log.G(ic.Context).WithField("address", config.Address).Info("pod gRPC service listening on dedicated socket")
			}

			return svc, nil
		},
	})
}

type podService struct {
	provider server.PodResourcesProvider
	api.UnimplementedPodServer

	// dedicated is non-nil when the plugin runs its own gRPC server.
	dedicated *grpc.Server
	listener  net.Listener
}

var _ api.PodServer = (*podService)(nil)

// Register registers the Pod gRPC service with the shared containerd
// gRPC server. It is a no-op when a dedicated address is configured
// (the service is already running on its own socket).
func (s *podService) Register(srv *grpc.Server) error {
	if s.dedicated != nil {
		// Already running on a dedicated socket; skip shared registration.
		return nil
	}
	api.RegisterPodServer(srv, s)
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
	api.RegisterPodServer(srv, s)

	s.dedicated = srv
	s.listener = l

	go func() {
		if err := srv.Serve(l); err != nil {
			log.G(ctx).WithError(err).WithField("address", addr).Error("pod dedicated gRPC server exited")
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
