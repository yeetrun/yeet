#!/usr/bin/env bash
# Copyright (c) 2025 AUTHORS All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -euo pipefail

kernel_version="${YEET_KERNEL_VERSION:-7.0}"
out_dir="${1:-dist/kernel-linux-$kernel_version}"
work_dir="${YEET_KERNEL_WORK_DIR:-}"
source_url="${YEET_KERNEL_SOURCE_URL:-https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-${kernel_version}.tar.xz}"
source_sha="${YEET_KERNEL_SOURCE_SHA256:-bb7f6d80b387c757b7d14bb93028fcb90f793c5c0d367736ee815a100b3891f0}"
# Firecracker's v1.14.3 config regressed no-initrd direct boot for this kernel
# path, so default to the older upstream-known-good microVM config revision.
config_url="${YEET_KERNEL_CONFIG_URL:-https://raw.githubusercontent.com/firecracker-microvm/firecracker/86a2559b26a4b9a05405aeaa58bab0f7261d71bc/resources/guest_configs/microvm-kernel-ci-x86_64-6.1.config}"
localversion="${YEET_KERNEL_LOCALVERSION:--yeet}"

require() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 1
	fi
}

for cmd in awk curl file grep install make mkdir mktemp nproc sha256sum tar xz; do
	require "$cmd"
done

if [ -z "$work_dir" ]; then
	work_dir="$(mktemp -d)"
	cleanup_work=1
else
	mkdir -p "$work_dir"
	cleanup_work=0
fi

cleanup() {
	if [ "${cleanup_work:-0}" = 1 ]; then
		rm -rf "$work_dir"
	fi
}
trap cleanup EXIT

mkdir -p "$out_dir"

source_tgz="$work_dir/linux-${kernel_version}.tar.xz"
echo "Downloading Linux $kernel_version..."
curl -fL --retry 3 -o "$source_tgz" "$source_url"
if [ -n "$source_sha" ]; then
	actual_sha="$(sha256sum "$source_tgz" | awk '{ print $1 }')"
	if [ "$actual_sha" != "$source_sha" ]; then
		echo "Linux source checksum mismatch: got $actual_sha, want $source_sha" >&2
		exit 1
	fi
fi

echo "Extracting Linux source..."
tar xJf "$source_tgz" -C "$work_dir"
src_dir="$(find "$work_dir" -maxdepth 1 -name "linux-*" -type d -print -quit)"
if [ -z "$src_dir" ]; then
	echo "Linux source tarball did not contain a linux-* directory" >&2
	exit 1
fi

echo "Downloading Firecracker guest kernel config..."
curl -fsSL --retry 3 -o "$src_dir/.config" "$config_url"

echo "Configuring yeet Firecracker kernel..."
(
	cd "$src_dir"
	scripts/config \
		--disable MODULES \
		--enable VIRTIO \
		--enable VIRTIO_MMIO \
		--enable VIRTIO_MMIO_CMDLINE_DEVICES \
		--enable VIRTIO_BLK \
		--enable VIRTIO_NET \
		--enable IP_PNP \
		--enable IP_PNP_DHCP \
		--enable EXT4_FS \
		--enable EXT4_FS_POSIX_ACL \
		--enable EXT4_FS_SECURITY \
		--enable SERIAL_8250 \
		--enable SERIAL_8250_CONSOLE \
		--enable DEVTMPFS \
		--enable DEVTMPFS_MOUNT \
		--enable CGROUPS \
		--enable SECURITY_APPARMOR \
		--enable SECURITY_APPARMOR_HASH \
		--enable SECURITY_APPARMOR_HASH_DEFAULT \
		--enable TUN \
		--enable NETFILTER \
		--enable NETFILTER_ADVANCED \
		--enable NETFILTER_XTABLES \
		--enable NF_CONNTRACK \
		--enable NF_CONNTRACK_MARK \
		--enable NF_NAT \
		--enable NF_NAT_MASQUERADE \
		--enable NF_TABLES \
		--enable NF_TABLES_IPV4 \
		--enable NFT_CT \
		--enable NFT_NAT \
		--enable NFT_MASQ \
		--enable NFT_COMPAT \
		--enable NETFILTER_XT_TARGET_CONNMARK \
		--enable NETFILTER_XT_TARGET_MASQUERADE \
		--enable NETFILTER_XT_TARGET_MARK \
		--enable NETFILTER_XT_NAT \
		--enable NETFILTER_XT_MATCH_CONNMARK \
		--enable NETFILTER_XT_MATCH_MARK \
		--enable NETFILTER_XT_MATCH_COMMENT \
		--enable NETFILTER_XT_MATCH_CONNTRACK \
		--enable NETFILTER_XT_MATCH_ADDRTYPE \
		--set-str LOCALVERSION "$localversion"
	make olddefconfig
)

require_config() {
	local key="$1"
	local want="$2"
	local got
	if [ "$want" = "n" ]; then
		got="$(grep -E "^(# ${key} is not set|${key}=n)$" "$src_dir/.config" | tail -n 1 || true)"
		if [ -z "$got" ]; then
			echo "kernel config $key = missing or enabled, want disabled" >&2
			exit 1
		fi
		return
	fi
	got="$(grep -E "^${key}=" "$src_dir/.config" | tail -n 1 || true)"
	if [ "$got" != "${key}=${want}" ]; then
		echo "kernel config $key = ${got:-missing}, want ${key}=${want}" >&2
		exit 1
	fi
}

require_config CONFIG_MODULES n
require_config CONFIG_VIRTIO_MMIO y
require_config CONFIG_VIRTIO_MMIO_CMDLINE_DEVICES y
require_config CONFIG_VIRTIO_BLK y
require_config CONFIG_VIRTIO_NET y
require_config CONFIG_IP_PNP y
require_config CONFIG_IP_PNP_DHCP y
require_config CONFIG_EXT4_FS y
require_config CONFIG_SERIAL_8250_CONSOLE y
require_config CONFIG_DEVTMPFS y
require_config CONFIG_DEVTMPFS_MOUNT y
require_config CONFIG_TUN y
require_config CONFIG_NETFILTER y
require_config CONFIG_NETFILTER_ADVANCED y
require_config CONFIG_NETFILTER_XTABLES y
require_config CONFIG_NF_CONNTRACK y
require_config CONFIG_NF_CONNTRACK_MARK y
require_config CONFIG_NF_NAT y
require_config CONFIG_NF_NAT_MASQUERADE y
require_config CONFIG_NF_TABLES y
require_config CONFIG_NF_TABLES_IPV4 y
require_config CONFIG_NFT_CT y
require_config CONFIG_NFT_NAT y
require_config CONFIG_NFT_MASQ y
require_config CONFIG_NFT_COMPAT y
require_config CONFIG_NETFILTER_XT_TARGET_CONNMARK y
require_config CONFIG_NETFILTER_XT_TARGET_MASQUERADE y
require_config CONFIG_NETFILTER_XT_TARGET_MARK y
require_config CONFIG_NETFILTER_XT_NAT y
require_config CONFIG_NETFILTER_XT_MATCH_CONNMARK y
require_config CONFIG_NETFILTER_XT_MATCH_MARK y
require_config CONFIG_NETFILTER_XT_MATCH_COMMENT y
require_config CONFIG_NETFILTER_XT_MATCH_CONNTRACK y
require_config CONFIG_NETFILTER_XT_MATCH_ADDRTYPE y

echo "Building vmlinux..."
make -C "$src_dir" -j"$(nproc)" vmlinux
if ! file "$src_dir/vmlinux" | grep -q "ELF 64-bit"; then
	echo "built vmlinux is not an x86_64 ELF kernel" >&2
	file "$src_dir/vmlinux" >&2
	exit 1
fi

install -m 0644 "$src_dir/vmlinux" "$out_dir/vmlinux"
install -m 0644 "$src_dir/.config" "$out_dir/kernel.config"
sha256sum "$out_dir/vmlinux" "$out_dir/kernel.config" >"$out_dir/kernel-checksums.txt"

echo "Wrote kernel artifacts to $out_dir"
echo "Kernel version: linux-$kernel_version$localversion"
