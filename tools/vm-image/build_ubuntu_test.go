// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vm_image_test

import (
	"os"
	"strings"
	"testing"
)

func TestFastUbuntuImagePolicyCleansFirecrackerGuestStatus(t *testing.T) {
	script := readBuildUbuntuScript(t)

	for _, want := range []string{
		`version="${YEET_VM_IMAGE_VERSION:-ubuntu-26.04-amd64-v10}"`,
		"fwupd$",
		"fwupd-signed$",
		"update-notifier-common$",
		"update-manager-core$",
		"xfsprogs$",
		"fwupd.service",
		"fwupd-refresh.service",
		"fwupd-refresh.timer",
		"update-notifier-download.service",
		"update-notifier-download.timer",
		"update-notifier-motd.service",
		"update-notifier-motd.timer",
		"xfs_scrub_all.service",
		"xfs_scrub_all.timer",
		"proc-sys-fs-binfmt_misc.automount",
		"proc-sys-fs-binfmt_misc.mount",
		"merge_usr_sbin_into_usr_bin",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("build script missing %q", want)
		}
	}
}

func TestFastUbuntuImagePolicySupportsRouterServices(t *testing.T) {
	script := readBuildUbuntuScript(t)

	for _, want := range []string{
		"apt-get install -y --no-install-recommends iptables nftables",
		"99-yeet-vm-router.conf",
		"net.ipv4.ip_forward = 1",
		"yeet-vm-tun.conf",
		"/dev/net/tun",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("build script missing router service policy %q", want)
		}
	}
}

func TestYeetKernelConfigSupportsRouterServicesWithoutModules(t *testing.T) {
	script := readBuildKernelScript(t)

	for _, want := range []string{
		"CONFIG_MODULES n",
		"CONFIG_TUN y",
		"CONFIG_NETFILTER y",
		"CONFIG_NETFILTER_XTABLES y",
		"CONFIG_NF_CONNTRACK y",
		"CONFIG_NF_CONNTRACK_MARK y",
		"CONFIG_NF_NAT y",
		"CONFIG_NF_TABLES_IPV4 y",
		"CONFIG_NFT_CT y",
		"CONFIG_NFT_NAT y",
		"CONFIG_NFT_MASQ y",
		"CONFIG_NETFILTER_XT_TARGET_CONNMARK y",
		"CONFIG_NETFILTER_XT_TARGET_MASQUERADE y",
		"CONFIG_NETFILTER_XT_TARGET_MARK y",
		"CONFIG_NETFILTER_XT_NAT y",
		"CONFIG_NETFILTER_XT_MATCH_CONNMARK y",
		"CONFIG_NETFILTER_XT_MATCH_MARK y",
		"CONFIG_NETFILTER_XT_MATCH_COMMENT y",
		"CONFIG_NETFILTER_XT_MATCH_CONNTRACK y",
		"CONFIG_NETFILTER_XT_MATCH_ADDRTYPE y",
		"CONFIG_NF_TABLES y",
		"CONFIG_NFT_COMPAT y",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("kernel build script missing config assertion %q", want)
		}
	}
}

func TestFastUbuntuImagePolicyGuardsSbinMerge(t *testing.T) {
	script := readBuildUbuntuScript(t)

	for _, want := range []string{
		"find \"$usr_sbin\" -mindepth 1 -maxdepth 1 -print",
		"resolve_guest_path",
		"chroot \"$root\" /usr/bin/readlink -f \"$guest_path\"",
		"unmergeable /usr/sbin collision",
		"ln -s bin \"$usr_sbin\"",
		"ln -snf usr/bin \"$root/sbin\"",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("sbin merge helper missing %q", want)
		}
	}
}

func readBuildUbuntuScript(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("build-ubuntu-26.04.sh")
	if err != nil {
		t.Fatalf("read build script: %v", err)
	}
	return string(raw)
}

func readBuildKernelScript(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("build-linux-kernel.sh")
	if err != nil {
		t.Fatalf("read kernel build script: %v", err)
	}
	return string(raw)
}
