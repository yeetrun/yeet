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
		`version="${YEET_VM_IMAGE_VERSION:-ubuntu-26.04-amd64-v14}"`,
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
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("build script missing %q", want)
		}
	}
}

func TestFastUbuntuImagePolicyUsesHostCompatibleExt4Features(t *testing.T) {
	script := readBuildUbuntuScript(t)

	for _, want := range []string{
		"normalize_fast_rootfs_ext4_features",
		"tune2fs -O ^orphan_file",
		"run_fast_rootfs_e2fsck",
		"rootfs ext4 features are not compatible with LTS host tooling",
		"FEATURE_",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("build script missing ext4 compatibility policy %q", want)
		}
	}
}

func TestFastUbuntuImagePolicySupportsRouterServices(t *testing.T) {
	script := readBuildUbuntuScript(t)

	for _, want := range []string{
		"apt-get install -y --no-install-recommends iptables nftables rsync",
		`chroot "$root" /usr/bin/rsync --version >/dev/null`,
		"99-yeet-vm-router.conf",
		"net.ipv4.ip_forward = 1",
		"net.ipv6.conf.all.forwarding = 1",
		"yeet-vm-tun.conf",
		"/dev/net/tun",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("build script missing router service policy %q", want)
		}
	}
}

func TestFastUbuntuImagePolicyOwnsGhosttyTerminfoAsset(t *testing.T) {
	script := readBuildUbuntuScript(t)

	if !strings.Contains(script, `tools/vm-image/assets/xterm-ghostty.terminfo`) {
		t.Fatalf("build script should default Ghostty terminfo to tools/vm-image assets")
	}
	if strings.Contains(script, `pkg/catch/xterm-ghostty.terminfo`) {
		t.Fatalf("build script should not depend on pkg/catch for image assets")
	}
}

func TestYeetKernelConfigSupportsRouterServicesWithoutModules(t *testing.T) {
	script := readBuildKernelScript(t)

	for _, want := range []string{
		"CONFIG_MODULES n",
		"CONFIG_IPV6 y",
		"CONFIG_IPV6_MULTIPLE_TABLES y",
		"CONFIG_TUN y",
		"CONFIG_NETFILTER y",
		"CONFIG_NETFILTER_XTABLES y",
		"CONFIG_NF_CONNTRACK y",
		"CONFIG_NF_CONNTRACK_MARK y",
		"CONFIG_NF_NAT y",
		"CONFIG_NF_TABLES_IPV4 y",
		"CONFIG_NF_TABLES_IPV6 y",
		"CONFIG_NF_TABLES_INET y",
		"CONFIG_NFT_CT y",
		"CONFIG_NFT_NAT y",
		"CONFIG_NFT_MASQ y",
		"CONFIG_NFT_REJECT y",
		"CONFIG_NFT_REJECT_IPV6 y",
		"CONFIG_NFT_REJECT_INET y",
		"CONFIG_NETFILTER_XT_TARGET_CONNMARK y",
		"CONFIG_NETFILTER_XT_TARGET_MASQUERADE y",
		"CONFIG_NETFILTER_XT_TARGET_MARK y",
		"CONFIG_NETFILTER_XT_NAT y",
		"CONFIG_NETFILTER_XT_MATCH_CONNMARK y",
		"CONFIG_NETFILTER_XT_MATCH_MARK y",
		"CONFIG_NETFILTER_XT_MATCH_COMMENT y",
		"CONFIG_NETFILTER_XT_MATCH_CONNTRACK y",
		"CONFIG_NETFILTER_XT_MATCH_ADDRTYPE y",
		"CONFIG_IP6_NF_IPTABLES y",
		"CONFIG_NF_TABLES y",
		"CONFIG_NFT_COMPAT y",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("kernel build script missing config assertion %q", want)
		}
	}
}

func TestFastUbuntuImagePolicyPreservesUbuntuSbinLayout(t *testing.T) {
	script := readBuildUbuntuScript(t)

	for _, forbidden := range []string{
		"merge_usr_sbin_into_usr_bin",
		"ln -s bin \"$usr_sbin\"",
		"ln -snf usr/bin \"$root/sbin\"",
		"unmergeable /usr/sbin collision",
	} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("build script still contains custom sbin merge fragment %q", forbidden)
		}
	}

	for _, want := range []string{
		"validate_fast_rootfs_ubuntu_compatibility",
		"/usr/sbin must remain an Ubuntu-owned directory",
		"/sbin must keep Ubuntu cloud image target usr/sbin",
		"/usr/sbin/sshd",
		"/usr/sbin/agetty",
		"/usr/sbin/unix_chkpwd",
		"/usr/sbin/iptables-nft",
		"/usr/sbin/xtables-nft-multi",
		"dpkg -S /usr/sbin/sshd",
		"update-alternatives --display iptables",
		"iptables --version",
		"nf_tables",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("build script missing Ubuntu compatibility validation %q", want)
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
