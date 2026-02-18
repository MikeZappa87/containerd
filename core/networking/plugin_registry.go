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
	"sync"
)

// DynamicPodNetworkPlugin implements PodNetworkPlugin by delegating to a
// dynamically-swappable underlying plugin chain. It is designed to be used
// with directory-based plugin discovery: as registration files appear or
// disappear in a watched directory, the underlying chain is rebuilt and
// atomically swapped in without restarting containerd.
//
// Callers assign this to criService.grpcNetPlugin; sandbox_run and
// sandbox_stop call SetupPodNetwork / TeardownPodNetwork through the
// normal PodNetworkPlugin interface — they are unaware that the
// underlying chain may be rebuilt at any time.
type DynamicPodNetworkPlugin struct {
	mu      sync.RWMutex
	current PodNetworkPlugin // may be nil when no plugins are registered
}

// NewDynamicPlugin creates a DynamicPodNetworkPlugin with an optional
// initial delegate. Pass nil if no plugins are available yet; they will
// be loaded asynchronously once the directory watcher starts.
func NewDynamicPlugin(initial PodNetworkPlugin) *DynamicPodNetworkPlugin {
	return &DynamicPodNetworkPlugin{current: initial}
}

// Update atomically replaces the active plugin chain. The caller is
// responsible for closing the previous plugin (if any) after calling
// Update — typically this is handled by the syncer that owns the
// lifecycle.
//
// Passing nil is valid and means "no plugins are currently registered";
// subsequent calls to SetupPodNetwork / TeardownPodNetwork will return
// an error until a non-nil plugin is set.
func (d *DynamicPodNetworkPlugin) Update(p PodNetworkPlugin) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.current = p
}

// Current returns the active plugin, or nil if none is loaded.
func (d *DynamicPodNetworkPlugin) Current() PodNetworkPlugin {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.current
}

// SetupPodNetwork delegates to the currently active plugin chain.
func (d *DynamicPodNetworkPlugin) SetupPodNetwork(ctx context.Context, req SetupPodNetworkRequest) (*SetupPodNetworkResult, error) {
	d.mu.RLock()
	p := d.current
	d.mu.RUnlock()
	if p == nil {
		return nil, fmt.Errorf("no gRPC network plugins are currently registered")
	}
	return p.SetupPodNetwork(ctx, req)
}

// TeardownPodNetwork delegates to the currently active plugin chain.
func (d *DynamicPodNetworkPlugin) TeardownPodNetwork(ctx context.Context, req TeardownPodNetworkRequest) error {
	d.mu.RLock()
	p := d.current
	d.mu.RUnlock()
	if p == nil {
		return fmt.Errorf("no gRPC network plugins are currently registered")
	}
	return p.TeardownPodNetwork(ctx, req)
}

// Status returns nil when the underlying chain is ready.
func (d *DynamicPodNetworkPlugin) Status() error {
	d.mu.RLock()
	p := d.current
	d.mu.RUnlock()
	if p == nil {
		return fmt.Errorf("no gRPC network plugins are currently registered")
	}
	return p.Status()
}

// CheckHealth performs a live health check by delegating to the current plugin.
func (d *DynamicPodNetworkPlugin) CheckHealth(ctx context.Context) error {
	d.mu.RLock()
	p := d.current
	d.mu.RUnlock()
	if p == nil {
		return fmt.Errorf("no gRPC network plugins are currently registered")
	}
	return p.CheckHealth(ctx)
}

// Close closes the currently active plugin chain (if any).
func (d *DynamicPodNetworkPlugin) Close() error {
	d.mu.Lock()
	p := d.current
	d.current = nil
	d.mu.Unlock()
	if p != nil {
		return p.Close()
	}
	return nil
}
