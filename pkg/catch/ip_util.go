// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"os/exec"
	"strings"
)

type ifaceIP struct {
	Interface string
	IP        string
}

func parseIPv4Addrs(text string) []ifaceIP {
	lines := strings.Split(text, "\n")
	ips := make([]ifaceIP, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		iface := fields[1]
		ip := ""
		for i := 0; i < len(fields)-1; i++ {
			if fields[i] == "inet" {
				ip = fields[i+1]
				break
			}
		}
		if ip == "" {
			continue
		}
		ip = strings.Split(ip, "/")[0]
		if ip == "" {
			continue
		}
		ips = append(ips, ifaceIP{Interface: iface, IP: ip})
	}
	return ips
}

func listIPv4Addrs(args []string) ([]ifaceIP, error) {
	cmd := exec.Command("ip", args...)
	bs, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}
	raw := parseIPv4Addrs(string(bs))
	seen := make(map[ifaceIP]struct{}, len(raw))
	ips := make([]ifaceIP, 0, len(raw))
	for _, entry := range raw {
		if entry.IP == "127.0.0.1" {
			continue
		}
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		ips = append(ips, entry)
	}
	return ips, nil
}
