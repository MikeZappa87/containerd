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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/core/networking"
	"github.com/containerd/log"
	"github.com/fsnotify/fsnotify"
)

// grpcPluginRegistration is the JSON schema for a plugin registration file
// dropped into the gRPC network plugin conf directory.
//
// Example file (/etc/containerd/net.d/10-calico.json):
//
//	{
//	    "name": "calico",
//	    "address": "/run/calico/network.sock",
//	    "management_address": "/run/calico/management.sock",
//	    "primary": true,
//	    "priority": 10,
//	    "dial_timeout": 5
//	}
type grpcPluginRegistration struct {
	// Name is a human-readable identifier for the plugin (used in logs).
	Name string `json:"name"`
	// Address is the unix socket path of the PodNetworkLifecycle gRPC server.
	Address string `json:"address"`
	// ManagementAddress is the unix socket path of the PodNetworkManagement gRPC server.
	// Optional — if not set, management RPCs are not available for this plugin.
	ManagementAddress string `json:"management_address,omitempty"`
	// Primary marks the plugin as the primary network provider.
	// At most one should be primary. If none is marked, the highest-priority
	// (lowest Priority value) plugin is treated as primary.
	Primary bool `json:"primary,omitempty"`
	// Priority controls ordering — lower values run earlier in the chain.
	// Default is 100.
	Priority int `json:"priority,omitempty"`
	// DialTimeout is how long (in seconds) to wait when dialing the plugin.
	// Defaults to 5 seconds.
	DialTimeout int `json:"dial_timeout,omitempty"`
}

// grpcPluginSyncer watches a directory for gRPC network plugin registration
// files (*.json) and dynamically (re)builds the chained network plugin.
//
// It follows the same lifecycle pattern as cniNetConfSyncer:
//
//   - Created in initPlatform / NewCRIService
//   - syncLoop() is started in Run() as a goroutine
//   - stop() is called in Close()
type grpcPluginSyncer struct {
	sync.RWMutex
	lastSyncStatus error

	watcher *fsnotify.Watcher
	confDir string

	// dynamicPlugin is the proxy plugin that is assigned to
	// criService.grpcNetPlugin. The syncer swaps its underlying
	// chain on each successful reload.
	dynamicPlugin *networking.DynamicPodNetworkPlugin

	// prevPlugin holds the last-built chain so it can be closed when a new
	// chain replaces it.
	prevPlugin networking.PodNetworkPlugin
}

// newGRPCPluginSyncer creates a syncer and performs an initial scan of confDir.
func newGRPCPluginSyncer(confDir string) (*grpcPluginSyncer, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}

	if err := os.MkdirAll(confDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create gRPC plugin conf dir=%s: %w", confDir, err)
	}

	if err := watcher.Add(confDir); err != nil {
		return nil, fmt.Errorf("failed to watch gRPC plugin conf dir %s: %w", confDir, err)
	}

	syncer := &grpcPluginSyncer{
		watcher:       watcher,
		confDir:       confDir,
		dynamicPlugin: networking.NewDynamicPlugin(nil),
	}

	// Perform initial load.
	if err := syncer.reload(); err != nil {
		log.L.WithError(err).Warn("failed to load gRPC network plugins during init; will retry on fs changes")
		syncer.updateLastStatus(err)
	}

	return syncer, nil
}

// plugin returns the DynamicPodNetworkPlugin that should be assigned to
// criService.grpcNetPlugin. It implements networking.PodNetworkPlugin and
// automatically delegates to the latest chain.
func (s *grpcPluginSyncer) plugin() *networking.DynamicPodNetworkPlugin {
	return s.dynamicPlugin
}

// syncLoop watches the conf directory for changes and reloads the plugin
// chain. It blocks until the watcher is closed or an unrecoverable error
// occurs — callers should run it in a goroutine.
func (s *grpcPluginSyncer) syncLoop() error {
	for {
		select {
		case event, ok := <-s.watcher.Events:
			if !ok {
				log.L.Debug("gRPC plugin watcher channel closed")
				return nil
			}

			// Only react to meaningful changes (same heuristic as
			// cniNetConfSyncer).
			if event.Has(fsnotify.Chmod) || event.Has(fsnotify.Create) {
				log.L.Debugf("ignore event from gRPC plugin conf dir: %s", event)
				continue
			}
			log.L.Debugf("gRPC plugin conf dir change: %s", event)

			if event.Name == s.confDir && (event.Has(fsnotify.Rename) || event.Has(fsnotify.Remove)) {
				return fmt.Errorf("gRPC plugin conf dir removed, stopping watcher")
			}

			lerr := s.reload()
			if lerr != nil {
				log.L.WithError(lerr).Error("failed to reload gRPC network plugins after fs change")
			}
			s.updateLastStatus(lerr)

		case err := <-s.watcher.Errors:
			if err != nil {
				log.L.WithError(err).Error("gRPC plugin watcher error")
				return err
			}
		}
	}
}

// reload scans the conf directory, parses registration files, builds gRPC
// connections, and swaps the plugin chain in dynamicPlugin.
func (s *grpcPluginSyncer) reload() error {
	entries, err := s.scanDir()
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		// No registration files — clear the chain.
		s.swapPlugin(nil)
		log.L.Info("No gRPC network plugin registration files found; clearing plugin chain")
		return nil
	}

	// Build individual gRPC plugins from entries.
	var (
		plugins []networking.PodNetworkPlugin
		names   []string
	)
	for _, reg := range entries {
		dialTimeout := time.Duration(reg.DialTimeout) * time.Second
		if dialTimeout == 0 {
			dialTimeout = 5 * time.Second
		}
		p, err := networking.NewGRPCPlugin(networking.GRPCPluginConfig{
			Address:     reg.Address,
			DialTimeout: dialTimeout,
		})
		if err != nil {
			// Close any plugins we already created in this reload attempt.
			for _, pp := range plugins {
				_ = pp.Close()
			}
			return fmt.Errorf("failed to connect to gRPC plugin %q at %s: %w", reg.Name, reg.Address, err)
		}
		plugins = append(plugins, p)
		name := reg.Name
		if name == "" {
			name = reg.Address
		}
		names = append(names, name)
	}

	chain, err := networking.NewChainedPlugin(plugins, names)
	if err != nil {
		for _, pp := range plugins {
			_ = pp.Close()
		}
		return fmt.Errorf("failed to create chained plugin: %w", err)
	}

	s.swapPlugin(chain)
	log.L.Infof("Loaded %d gRPC network plugin(s) from %s (primary: %s)", len(plugins), s.confDir, names[0])
	return nil
}

// scanDir reads all *.json files in the conf directory, parses them into
// registrations, and returns them sorted: primary first, then by priority,
// then by filename.
func (s *grpcPluginSyncer) scanDir() ([]grpcPluginRegistration, error) {
	pattern := filepath.Join(s.confDir, "*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to glob %s: %w", pattern, err)
	}

	var regs []grpcPluginRegistration
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read plugin file %s: %w", path, err)
		}
		var reg grpcPluginRegistration
		if err := json.Unmarshal(data, &reg); err != nil {
			return nil, fmt.Errorf("invalid plugin file %s: %w", path, err)
		}
		if reg.Address == "" {
			return nil, fmt.Errorf("plugin file %s: address is required", path)
		}
		if reg.Priority == 0 {
			reg.Priority = 100
		}
		if reg.Name == "" {
			reg.Name = filepath.Base(path)
		}
		regs = append(regs, reg)
	}

	// Sort: primary plugins first, then by priority (ascending), then
	// lexicographically by name.
	sort.Slice(regs, func(i, j int) bool {
		if regs[i].Primary != regs[j].Primary {
			return regs[i].Primary
		}
		if regs[i].Priority != regs[j].Priority {
			return regs[i].Priority < regs[j].Priority
		}
		return regs[i].Name < regs[j].Name
	})

	return regs, nil
}

// swapPlugin replaces the active chain in the DynamicPodNetworkPlugin and
// closes the previously active chain (if any).
func (s *grpcPluginSyncer) swapPlugin(newPlugin networking.PodNetworkPlugin) {
	s.Lock()
	old := s.prevPlugin
	s.prevPlugin = newPlugin
	s.Unlock()

	// Update the proxy so new calls use the new chain immediately.
	s.dynamicPlugin.Update(newPlugin)

	// Close old connections in the background to avoid blocking.
	if old != nil {
		go func() {
			if err := old.Close(); err != nil {
				log.L.WithError(err).Warn("failed to close previous gRPC plugin chain")
			}
		}()
	}
}

// lastStatus returns the most recent reload error (nil on success).
func (s *grpcPluginSyncer) lastStatus() error {
	s.RLock()
	defer s.RUnlock()
	return s.lastSyncStatus
}

func (s *grpcPluginSyncer) updateLastStatus(err error) {
	s.Lock()
	defer s.Unlock()
	s.lastSyncStatus = err
}

// stop closes the watcher, the dynamic plugin, and any active chain.
func (s *grpcPluginSyncer) stop() error {
	werr := s.watcher.Close()
	perr := s.dynamicPlugin.Close()
	if werr != nil {
		return werr
	}
	return perr
}
