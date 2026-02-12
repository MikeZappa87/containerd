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

	cnins "github.com/containernetworking/plugins/pkg/ns"

	"github.com/containerd/containerd/v2/pkg/netns"
)

// getIPsFromNetNS enters the given network namespace and returns all IP addresses
// assigned to the specified interface (e.g., "eth0").
func getIPsFromNetNS(ns *netns.NetNS, ifaceName string) ([]net.IP, error) {
	if ns == nil {
		return nil, fmt.Errorf("network namespace is nil")
	}

	var ips []net.IP

	err := ns.Do(func(_ cnins.NetNS) error {
		iface, err := net.InterfaceByName(ifaceName)
		if err != nil {
			return fmt.Errorf("failed to get interface %q: %w", ifaceName, err)
		}

		addrs, err := iface.Addrs()
		if err != nil {
			return fmt.Errorf("failed to get addresses for interface %q: %w", ifaceName, err)
		}

		for _, addr := range addrs {
			// addr is either *net.IPNet or *net.IPAddr
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil {
				ips = append(ips, ip)
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return ips, nil
}

// getIPStringsFromNetNS is a convenience wrapper that returns IP addresses as strings.
func getIPStringsFromNetNS(ns *netns.NetNS, ifaceName string) ([]string, error) {
	ips, err := getIPsFromNetNS(ns, ifaceName)
	if err != nil {
		return nil, err
	}

	var ipStrings []string
	for _, ip := range ips {
		ipStrings = append(ipStrings, ip.String())
	}

	return ipStrings, nil
}
