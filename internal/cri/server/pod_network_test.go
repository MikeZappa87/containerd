//go:build linux

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
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"

	cnins "github.com/containernetworking/plugins/pkg/ns"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

// requireRoot skips the test if not running as root (needed for netns/netlink).
func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("test requires root privileges")
	}
}

// newTestNetNS creates a new network namespace and returns its /proc path and
// a cleanup function. The calling goroutine is locked to its OS thread for the
// duration (the cleanup function unlocks it).
func newTestNetNS(t *testing.T) (string, func()) {
	t.Helper()

	runtime.LockOSThread()

	origNS, err := netns.Get()
	if err != nil {
		runtime.UnlockOSThread()
		t.Fatalf("failed to get current netns: %v", err)
	}

	newNS, err := netns.New()
	if err != nil {
		origNS.Close()
		runtime.UnlockOSThread()
		t.Fatalf("failed to create new netns: %v", err)
	}

	// Switch back to the original namespace; the new one stays open via fd.
	if err := netns.Set(origNS); err != nil {
		newNS.Close()
		origNS.Close()
		runtime.UnlockOSThread()
		t.Fatalf("failed to restore netns: %v", err)
	}

	// Use /proc/self/fd/<N> as the namespace path — WithNetNSPath and
	// netns.GetFromPath both open the path, so this works without bind-mounts.
	nsPath := fmt.Sprintf("/proc/self/fd/%d", int(newNS))

	cleanup := func() {
		newNS.Close()
		origNS.Close()
		runtime.UnlockOSThread()
	}

	return nsPath, cleanup
}

// createVethPair creates a veth pair with one end named peerName. The host end
// name is returned. Both ends start in the root namespace.
func createVethPair(t *testing.T, hostName, peerName string) {
	t.Helper()

	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{
			Name: hostName,
		},
		PeerName: peerName,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		t.Fatalf("failed to create veth pair (%s, %s): %v", hostName, peerName, err)
	}

	// Bring both ends up.
	for _, name := range []string{hostName, peerName} {
		link, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatalf("failed to find link %s: %v", name, err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatalf("failed to bring up %s: %v", name, err)
		}
	}
}

// cleanupLink removes a link if it exists, ignoring errors.
func cleanupLink(name string) {
	if link, err := netlink.LinkByName(name); err == nil {
		_ = netlink.LinkDel(link)
	}
}

// TestMoveNetDevice_Basic creates a veth pair, adds an address to the peer
// end, moves it into a new netns, and verifies the device, address, and state.
func TestMoveNetDevice_Basic(t *testing.T) {
	requireRoot(t)

	hostEnd := "pntest-h0"
	peerEnd := "pntest-p0"

	createVethPair(t, hostEnd, peerEnd)
	defer cleanupLink(hostEnd)
	defer cleanupLink(peerEnd)

	// Add an address to the peer end (the one we will move).
	peerLink, err := netlink.LinkByName(peerEnd)
	if err != nil {
		t.Fatalf("failed to find peer link: %v", err)
	}
	addr, _ := netlink.ParseAddr("10.99.0.2/24")
	if err := netlink.AddrAdd(peerLink, addr); err != nil {
		t.Fatalf("failed to add address to peer: %v", err)
	}

	nsPath, cleanup := newTestNetNS(t)
	defer cleanup()

	result, err := moveNetDevice(nsPath, peerEnd, peerEnd)
	if err != nil {
		t.Fatalf("moveNetDevice failed: %v", err)
	}

	// Verify result contains the device name.
	if result.DeviceName != peerEnd {
		t.Errorf("expected DeviceName=%s, got %s", peerEnd, result.DeviceName)
	}

	// Verify the address was captured.
	foundAddr := false
	for _, a := range result.Addresses {
		if strings.HasPrefix(a, "10.99.0.2/") {
			foundAddr = true
		}
	}
	if !foundAddr {
		t.Errorf("expected address 10.99.0.2/24 in result, got %v", result.Addresses)
	}

	// The peer should no longer exist in the root namespace.
	if _, err := netlink.LinkByName(peerEnd); err == nil {
		t.Errorf("peer %s should not exist in root ns after move", peerEnd)
	}

	// Verify the device exists in the target namespace with the address and is UP.
	err = cnins.WithNetNSPath(nsPath, func(_ cnins.NetNS) error {
		link, err := netlink.LinkByName(peerEnd)
		if err != nil {
			return fmt.Errorf("device %s not found in target netns: %w", peerEnd, err)
		}
		if link.Attrs().Flags&net.FlagUp == 0 {
			return fmt.Errorf("device %s is not UP in target netns", peerEnd)
		}
		addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
		if err != nil {
			return fmt.Errorf("failed to list addrs: %w", err)
		}
		found := false
		for _, a := range addrs {
			if a.IPNet.IP.Equal(net.ParseIP("10.99.0.2")) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("address 10.99.0.2 not found on %s in target netns", peerEnd)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestMoveNetDevice_Rename tests that a device is properly renamed when moved.
func TestMoveNetDevice_Rename(t *testing.T) {
	requireRoot(t)

	hostEnd := "pntest-h1"
	peerEnd := "pntest-p1"
	renamedTo := "eth0"

	createVethPair(t, hostEnd, peerEnd)
	defer cleanupLink(hostEnd)
	defer cleanupLink(peerEnd)
	defer cleanupLink(renamedTo) // in case it leaks back

	nsPath, cleanup := newTestNetNS(t)
	defer cleanup()

	result, err := moveNetDevice(nsPath, peerEnd, renamedTo)
	if err != nil {
		t.Fatalf("moveNetDevice with rename failed: %v", err)
	}

	if result.DeviceName != renamedTo {
		t.Errorf("expected DeviceName=%s, got %s", renamedTo, result.DeviceName)
	}

	// Verify the renamed device is present in the target ns.
	err = cnins.WithNetNSPath(nsPath, func(_ cnins.NetNS) error {
		if _, err := netlink.LinkByName(renamedTo); err != nil {
			return fmt.Errorf("renamed device %s not found in target netns: %w", renamedTo, err)
		}
		// The old name should not exist.
		if _, err := netlink.LinkByName(peerEnd); err == nil {
			return fmt.Errorf("old name %s should not exist in target netns after rename", peerEnd)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestMoveNetDevice_PreservesRoutes tests that routes associated with the device
// are re-added in the target namespace.
func TestMoveNetDevice_PreservesRoutes(t *testing.T) {
	requireRoot(t)

	hostEnd := "pntest-h2"
	peerEnd := "pntest-p2"

	createVethPair(t, hostEnd, peerEnd)
	defer cleanupLink(hostEnd)
	defer cleanupLink(peerEnd)

	// Add an address and a route on the peer.
	peerLink, _ := netlink.LinkByName(peerEnd)
	addr, _ := netlink.ParseAddr("10.88.0.2/24")
	if err := netlink.AddrAdd(peerLink, addr); err != nil {
		t.Fatalf("failed to add addr: %v", err)
	}

	_, dst, _ := net.ParseCIDR("10.200.0.0/16")
	route := &netlink.Route{
		LinkIndex: peerLink.Attrs().Index,
		Dst:       dst,
		Scope:     netlink.SCOPE_LINK,
	}
	if err := netlink.RouteAdd(route); err != nil {
		t.Fatalf("failed to add route: %v", err)
	}

	nsPath, cleanup := newTestNetNS(t)
	defer cleanup()

	result, err := moveNetDevice(nsPath, peerEnd, peerEnd)
	if err != nil {
		t.Fatalf("moveNetDevice failed: %v", err)
	}

	// Verify the route was captured in the result.
	foundRoute := false
	for _, r := range result.Routes {
		if strings.HasPrefix(r.Destination, "10.200.0.0/") {
			foundRoute = true
		}
	}
	if !foundRoute {
		t.Errorf("expected route 10.200.0.0/16 in result, got %v", result.Routes)
	}

	// Verify the route exists in the target namespace.
	err = cnins.WithNetNSPath(nsPath, func(_ cnins.NetNS) error {
		link, err := netlink.LinkByName(peerEnd)
		if err != nil {
			return fmt.Errorf("device not found: %w", err)
		}
		routes, err := netlink.RouteList(link, netlink.FAMILY_V4)
		if err != nil {
			return fmt.Errorf("failed to list routes: %w", err)
		}
		for _, r := range routes {
			if r.Dst != nil && r.Dst.String() == "10.200.0.0/16" {
				return nil
			}
		}
		return fmt.Errorf("route 10.200.0.0/16 not found on %s in target netns (got %d routes)", peerEnd, len(routes))
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestGetPodNetworkState tests that getPodNetworkState correctly enumerates
// interfaces, addresses, and routes inside a netns.
func TestGetPodNetworkState(t *testing.T) {
	requireRoot(t)

	nsPath, cleanup := newTestNetNS(t)
	defer cleanup()

	// Create a veth pair and move one end into the namespace.
	hostEnd := "pntest-h3"
	peerEnd := "pntest-p3"

	createVethPair(t, hostEnd, peerEnd)
	defer cleanupLink(hostEnd)
	defer cleanupLink(peerEnd)

	peerLink, _ := netlink.LinkByName(peerEnd)
	addr, _ := netlink.ParseAddr("172.16.0.2/24")
	if err := netlink.AddrAdd(peerLink, addr); err != nil {
		t.Fatalf("failed to add addr: %v", err)
	}

	// Move peer into the namespace.
	if _, err := moveNetDevice(nsPath, peerEnd, peerEnd); err != nil {
		t.Fatalf("moveNetDevice failed: %v", err)
	}

	state, err := getPodNetworkState(nsPath)
	if err != nil {
		t.Fatalf("getPodNetworkState failed: %v", err)
	}

	// Should have at least the moved interface (loopback is filtered out).
	foundIface := false
	for _, iface := range state.Interfaces {
		if iface.Name == peerEnd {
			foundIface = true
			// Check that the address is listed.
			foundAddr := false
			for _, a := range iface.Addresses {
				if strings.HasPrefix(a, "172.16.0.2/") {
					foundAddr = true
				}
			}
			if !foundAddr {
				t.Errorf("address 172.16.0.2/24 not found on interface %s", peerEnd)
			}
			if iface.State != "UP" {
				t.Errorf("expected interface %s to be UP, got %s", peerEnd, iface.State)
			}
		}
	}
	if !foundIface {
		t.Errorf("interface %s not found in pod network state", peerEnd)
	}
}

// TestMoveNetDevice_NonExistent verifies that moving a device that does not
// exist returns an error.
func TestMoveNetDevice_NonExistent(t *testing.T) {
	requireRoot(t)

	nsPath, cleanup := newTestNetNS(t)
	defer cleanup()

	_, err := moveNetDevice(nsPath, "doesnotexist0", "")
	if err == nil {
		t.Fatal("expected error when moving a non-existent device")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

// TestMoveNetDevice_MultipleAddresses verifies that multiple addresses on a
// device are all preserved after a move.
func TestMoveNetDevice_MultipleAddresses(t *testing.T) {
	requireRoot(t)

	hostEnd := "pntest-h4"
	peerEnd := "pntest-p4"

	createVethPair(t, hostEnd, peerEnd)
	defer cleanupLink(hostEnd)
	defer cleanupLink(peerEnd)

	peerLink, _ := netlink.LinkByName(peerEnd)
	addrs := []string{"10.77.0.1/24", "10.77.1.1/24", "fd00::1/64"}
	for _, a := range addrs {
		addr, _ := netlink.ParseAddr(a)
		if err := netlink.AddrAdd(peerLink, addr); err != nil {
			t.Fatalf("failed to add address %s: %v", a, err)
		}
	}

	nsPath, cleanup := newTestNetNS(t)
	defer cleanup()

	result, err := moveNetDevice(nsPath, peerEnd, peerEnd)
	if err != nil {
		t.Fatalf("moveNetDevice failed: %v", err)
	}

	// Verify all addresses appear in the result.
	for _, expected := range []string{"10.77.0.1", "10.77.1.1", "fd00::1"} {
		found := false
		for _, a := range result.Addresses {
			if strings.HasPrefix(a, expected) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected address %s in result, got %v", expected, result.Addresses)
		}
	}

	// Verify addresses exist inside the namespace.
	err = cnins.WithNetNSPath(nsPath, func(_ cnins.NetNS) error {
		link, err := netlink.LinkByName(peerEnd)
		if err != nil {
			return fmt.Errorf("device not found: %w", err)
		}
		nsAddrs, err := netlink.AddrList(link, netlink.FAMILY_ALL)
		if err != nil {
			return fmt.Errorf("failed to list addrs: %w", err)
		}
		addrMap := make(map[string]bool)
		for _, a := range nsAddrs {
			addrMap[a.IPNet.IP.String()] = true
		}
		for _, expected := range []string{"10.77.0.1", "10.77.1.1", "fd00::1"} {
			if !addrMap[expected] {
				return fmt.Errorf("address %s not found in target netns (have %v)", expected, addrMap)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
