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
	"testing"
)

// ---------- DynamicPodNetworkPlugin tests ----------

func TestDynamicPlugin_NilInitial(t *testing.T) {
	d := NewDynamicPlugin(nil)

	if d.Current() != nil {
		t.Fatal("expected nil current with nil initial")
	}

	// All operations should return clear errors when no plugin is loaded.
	_, err := d.SetupPodNetwork(context.Background(), SetupPodNetworkRequest{SandboxID: "sb1"})
	if err == nil {
		t.Fatal("expected error with nil plugin")
	}
	if err := d.TeardownPodNetwork(context.Background(), TeardownPodNetworkRequest{SandboxID: "sb1"}); err == nil {
		t.Fatal("expected error with nil plugin")
	}
	if err := d.Status(); err == nil {
		t.Fatal("expected error with nil plugin")
	}
}

func TestDynamicPlugin_DelegatesToUnderlying(t *testing.T) {
	inner := &mockPlugin{
		setupResult: &SetupPodNetworkResult{
			Interfaces: []NetworkInterface{{Name: "eth0", Addresses: []string{"10.0.0.5/24"}}},
		},
	}

	d := NewDynamicPlugin(inner)

	// Setup should delegate.
	res, err := d.SetupPodNetwork(context.Background(), SetupPodNetworkRequest{SandboxID: "sb1", PodName: "pod1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Interfaces) != 1 || res.Interfaces[0].Name != "eth0" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if inner.getSetupCalls() != 1 {
		t.Fatalf("expected 1 setup call, got %d", inner.getSetupCalls())
	}

	// Status should delegate.
	if err := d.Status(); err != nil {
		t.Fatalf("expected nil status, got %v", err)
	}

	// Teardown should delegate.
	if err := d.TeardownPodNetwork(context.Background(), TeardownPodNetworkRequest{SandboxID: "sb1"}); err != nil {
		t.Fatal(err)
	}
	if inner.getTeardownCalls() != 1 {
		t.Fatalf("expected 1 teardown call, got %d", inner.getTeardownCalls())
	}
}

func TestDynamicPlugin_UpdateSwapsChain(t *testing.T) {
	old := &mockPlugin{
		setupResult: &SetupPodNetworkResult{Interfaces: []NetworkInterface{{Name: "old-eth0"}}},
	}
	d := NewDynamicPlugin(old)

	// Verify old plugin is used.
	res, _ := d.SetupPodNetwork(context.Background(), SetupPodNetworkRequest{SandboxID: "sb1"})
	if res.Interfaces[0].Name != "old-eth0" {
		t.Fatal("expected old plugin")
	}

	// Swap to new plugin.
	next := &mockPlugin{
		setupResult: &SetupPodNetworkResult{Interfaces: []NetworkInterface{{Name: "new-eth0"}}},
	}
	d.Update(next)

	res, _ = d.SetupPodNetwork(context.Background(), SetupPodNetworkRequest{SandboxID: "sb2"})
	if res.Interfaces[0].Name != "new-eth0" {
		t.Fatal("expected new plugin after Update")
	}

	// Old plugin should have received only 1 call (before swap).
	if old.getSetupCalls() != 1 {
		t.Fatalf("old plugin should have 1 call, got %d", old.getSetupCalls())
	}
}

func TestDynamicPlugin_UpdateToNil(t *testing.T) {
	inner := &mockPlugin{setupResult: &SetupPodNetworkResult{}}
	d := NewDynamicPlugin(inner)

	d.Update(nil)

	_, err := d.SetupPodNetwork(context.Background(), SetupPodNetworkRequest{SandboxID: "sb1"})
	if err == nil {
		t.Fatal("expected error after updating to nil")
	}
}

func TestDynamicPlugin_ConcurrentAccess(t *testing.T) {
	d := NewDynamicPlugin(nil)
	ctx := context.Background()

	var wg sync.WaitGroup
	errCount := 0
	var mu sync.Mutex

	// 50 goroutines calling Setup on a nil plugin (should get errors).
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := d.SetupPodNetwork(ctx, SetupPodNetworkRequest{SandboxID: "sb1"})
			if err != nil {
				mu.Lock()
				errCount++
				mu.Unlock()
			}
		}()
	}

	// While those are running, swap in a real plugin.
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.Update(&mockPlugin{setupResult: &SetupPodNetworkResult{}})
	}()

	wg.Wait()

	// At least some calls should have failed (nil plugin), at least some
	// may have succeeded (after Update). Just verify no panics / data races.
	if errCount == 0 {
		// This is possible but very unlikely with 50 goroutines; don't fail.
		t.Log("all concurrent calls succeeded (Update was very fast)")
	}
}

func TestDynamicPlugin_CloseNilsOut(t *testing.T) {
	inner := &mockPlugin{}
	d := NewDynamicPlugin(inner)

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if inner.getCloseCalls() != 1 {
		t.Fatal("expected Close to be called on underlying plugin")
	}

	// After Close, Current() should be nil.
	if d.Current() != nil {
		t.Fatal("expected nil after Close")
	}
}

func TestDynamicPlugin_CloseWithNilInner(t *testing.T) {
	d := NewDynamicPlugin(nil)
	if err := d.Close(); err != nil {
		t.Fatalf("Close with nil inner should not error, got %v", err)
	}
}

func TestDynamicPlugin_PropagatesErrors(t *testing.T) {
	inner := &mockPlugin{
		setupErr:    fmt.Errorf("setup boom"),
		teardownErr: fmt.Errorf("teardown boom"),
		statusErr:   fmt.Errorf("not ready"),
	}
	d := NewDynamicPlugin(inner)

	if _, err := d.SetupPodNetwork(context.Background(), SetupPodNetworkRequest{}); err == nil {
		t.Fatal("expected setup error")
	}
	if err := d.TeardownPodNetwork(context.Background(), TeardownPodNetworkRequest{}); err == nil {
		t.Fatal("expected teardown error")
	}
	if err := d.Status(); err == nil {
		t.Fatal("expected status error")
	}
}
