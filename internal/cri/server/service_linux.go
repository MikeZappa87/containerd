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
	"time"

	"github.com/moby/sys/userns"
	"github.com/opencontainers/selinux/go-selinux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"tags.cncf.io/container-device-interface/pkg/cdi"

	"github.com/containerd/containerd/v2/core/networking"
	"github.com/containerd/containerd/v2/core/networking/proxy"
	criconfig "github.com/containerd/containerd/v2/internal/cri/config"
	"github.com/containerd/containerd/v2/pkg/cap"
	"github.com/containerd/containerd/v2/pkg/kernelversion"
	"github.com/containerd/go-cni"
	"github.com/containerd/log"
)

func init() {
	var err error
	kernelSupportsRRO, err = kernelversion.GreaterEqualThan(kernelversion.KernelVersion{Kernel: 5, Major: 12})
	if err != nil {
		panic(fmt.Errorf("failed to check kernel version: %w", err))
	}
}

// initPlatform handles linux specific initialization for the CRI service.
func (c *criService) initPlatform() (err error) {
	if userns.RunningInUserNS() {
		if c.apparmorEnabled() || !c.config.RestrictOOMScoreAdj {
			log.L.Warn("Running CRI plugin in a user namespace typically requires disable_apparmor and restrict_oom_score_adj to be true")
		}
	}

	if c.config.EnableSelinux {
		if !selinux.GetEnabled() {
			log.L.Warn("Selinux is not supported")
		}
		if r := c.config.SelinuxCategoryRange; r > 0 {
			selinux.CategoryRange = uint32(r)
		}
	} else {
		selinux.SetDisabled()
	}

	pluginDirs := map[string]string{
		defaultNetworkPlugin: c.config.NetworkPluginConfDir,
	}
	for name, conf := range c.config.Runtimes {
		if conf.NetworkPluginConfDir != "" {
			pluginDirs[name] = conf.NetworkPluginConfDir
		}
	}

	// Skip CNI initialization if CNI is disabled.
	if !c.config.DisableCNI {
		networkAttachCount := 2

		if c.Config().UseInternalLoopback {
			networkAttachCount = 1
		}

		c.netPlugin = make(map[string]cni.CNI)
		for name, dir := range pluginDirs {
			max := c.config.NetworkPluginMaxConfNum
			if name != defaultNetworkPlugin {
				if m := c.config.Runtimes[name].NetworkPluginMaxConfNum; m != 0 {
					max = m
				}
			}
			// Pod needs to attach to at least loopback network and a non host network,
			// hence networkAttachCount is 2 if the CNI plugin is used and
			// 1 if the internal mechanism for setting lo to up is used.
			// If there are more network configs the pod will be attached to all the networks
			// but we will only use the ip of the default network interface as the pod IP.
			i, err := cni.New(cni.WithMinNetworkCount(networkAttachCount),
				cni.WithPluginConfDir(dir),
				cni.WithPluginMaxConfNum(max),
				cni.WithPluginDir(c.config.NetworkPluginBinDirs))
			if err != nil {
				return fmt.Errorf("failed to initialize cni: %w", err)
			}
			c.netPlugin[name] = i
		}
	}

	// When a dynamic plugin conf directory is set, use directory-based
	// plugin discovery. The syncer watches the directory for *.json
	// registration files and rebuilds the chain on any change.
	if confDir := c.config.CniConfig.GRPCNetworkPluginConfDir; confDir != "" {
		syncer, err := newGRPCPluginSyncer(confDir)
		if err != nil {
			return fmt.Errorf("failed to initialise gRPC plugin syncer for %s: %w", confDir, err)
		}
		c.grpcNetPlugin = syncer.plugin()
		c.grpcPluginSyncer = syncer
		log.L.Infof("Using dynamic gRPC network plugin discovery from %s", confDir)
	} else if entries := c.config.CniConfig.GRPCNetworkPlugins; len(entries) > 0 {
		var (
			plugins []networking.PodNetworkPlugin
			names   []string
		)

		// Sort so the primary plugin is first.
		// Find the primary index (first with Primary==true, else 0).
		primaryIdx := 0
		for i, e := range entries {
			if e.Primary {
				primaryIdx = i
				break
			}
		}
		// Build the ordered list: primary first, then the rest in config order.
		ordered := make([]criconfig.GRPCNetworkPluginEntry, 0, len(entries))
		ordered = append(ordered, entries[primaryIdx])
		for i, e := range entries {
			if i != primaryIdx {
				ordered = append(ordered, e)
			}
		}

		for _, entry := range ordered {
			dialTimeout := time.Duration(entry.DialTimeout) * time.Second
			if dialTimeout == 0 {
				dialTimeout = 5 * time.Second
			}
			p, err := networking.NewGRPCPlugin(networking.GRPCPluginConfig{
				Address:     entry.Address,
				DialTimeout: dialTimeout,
			})
			if err != nil {
				return fmt.Errorf("failed to initialize gRPC network plugin %q at %s: %w", entry.Name, entry.Address, err)
			}
			plugins = append(plugins, p)
			name := entry.Name
			if name == "" {
				name = entry.Address
			}
			names = append(names, name)
		}

		chained, err := networking.NewChainedPlugin(plugins, names)
		if err != nil {
			return fmt.Errorf("failed to create chained network plugin: %w", err)
		}
		c.grpcNetPlugin = chained
		log.L.Infof("Using %d chained gRPC network plugins (primary: %s), replacing CNI", len(plugins), names[0])
	} else if addr := c.config.CniConfig.GRPCNetworkPluginAddress; addr != "" {
		// Legacy single-address configuration.
		dialTimeout := time.Duration(c.config.CniConfig.GRPCNetworkPluginDialTimeout) * time.Second
		if dialTimeout == 0 {
			dialTimeout = 5 * time.Second
		}
		plugin, err := networking.NewGRPCPlugin(networking.GRPCPluginConfig{
			Address:     addr,
			DialTimeout: dialTimeout,
		})
		if err != nil {
			return fmt.Errorf("failed to initialize gRPC network plugin at %s: %w", addr, err)
		}
		c.grpcNetPlugin = plugin
		log.L.Infof("Using gRPC network plugin at %s (replacing CNI)", addr)
	}

	// Initialize PodNetworkManagement client if configured.
	if mgmtAddr := c.config.CniConfig.GRPCNetworkManagementAddress; mgmtAddr != "" {
		dialTimeout := time.Duration(c.config.CniConfig.GRPCNetworkManagementDialTimeout) * time.Second
		if dialTimeout == 0 {
			dialTimeout = 5 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
		defer cancel()

		conn, err := grpc.DialContext(ctx, "unix://"+mgmtAddr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock(),
		)
		if err != nil {
			return fmt.Errorf("failed to connect to gRPC network management service at %s: %w", mgmtAddr, err)
		}
		c.grpcNetMgmtClient = proxy.NewPodResourcesClient(conn)
		log.L.Infof("Using gRPC network management plugin at %s", mgmtAddr)
	}

	if c.allCaps == nil {
		c.allCaps, err = cap.Current()
		if err != nil {
			return fmt.Errorf("failed to get caps: %w", err)
		}
	}

	if c.config.EnableCDI == nil || *c.config.EnableCDI {
		err := cdi.Configure(cdi.WithSpecDirs(c.config.CDISpecDirs...))
		if err != nil {
			return fmt.Errorf("failed to configure CDI registry")
		}
	}

	return nil
}

// cniLoadOptions returns cni load options for the linux.
func (c *criService) cniLoadOptions() []cni.Opt {
	if c.config.UseInternalLoopback {
		return []cni.Opt{cni.WithDefaultConf}
	}

	return []cni.Opt{cni.WithLoNetwork, cni.WithDefaultConf}
}
