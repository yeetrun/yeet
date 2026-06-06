// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"fmt"
	"net/netip"
	"strings"
)

const vmGuestInitPath = "/usr/local/lib/yeet-vm/yeet-init"
const vmLegacyKernelBootArgs = "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw"

func vmKernelBootArgs(service string, network vmNetworkPlan) (string, error) {
	if err := validateVMKernelBootHostname(service); err != nil {
		return "", err
	}
	args := []string{
		"console=ttyS0",
		"reboot=k",
		"panic=1",
		"init=" + vmGuestInitPath,
	}
	if ipArg := vmKernelIPArg(service, network); ipArg != "" {
		args = append(args, ipArg)
	}
	if service != "" {
		args = append(args, "yeet.hostname="+service)
	}
	if len(network.Interfaces) > 0 && network.Interfaces[0].GuestName != "" {
		args = append(args, "yeet.iface="+network.Interfaces[0].GuestName)
	}
	return strings.Join(args, " "), nil
}

func validateVMKernelBootHostname(service string) error {
	if !vmHostnamePattern.MatchString(service) || strings.Contains(service, "..") {
		return fmt.Errorf("invalid VM hostname %q for kernel boot args", service)
	}
	return nil
}

func vmKernelIPArg(service string, network vmNetworkPlan) string {
	if len(network.Interfaces) == 0 {
		return ""
	}
	iface := network.Interfaces[0]
	if iface.DHCP {
		return "ip=dhcp"
	}
	if iface.GuestIP == "" {
		return ""
	}
	prefix, err := netip.ParsePrefix(iface.GuestIP)
	if err != nil || !prefix.Addr().Is4() {
		return ""
	}
	mask, ok := ipv4PrefixMask(prefix.Bits())
	if !ok {
		return ""
	}
	return fmt.Sprintf("ip=%s::%s:%s:%s:%s:none", prefix.Addr(), iface.Gateway, mask, service, iface.GuestName)
}

func ipv4PrefixMask(bits int) (string, bool) {
	if bits < 0 || bits > 32 {
		return "", false
	}
	var mask uint32
	if bits > 0 {
		mask = ^uint32(0) << uint(32-bits)
	}
	return fmt.Sprintf("%d.%d.%d.%d", byte(mask>>24), byte(mask>>16), byte(mask>>8), byte(mask)), true
}
