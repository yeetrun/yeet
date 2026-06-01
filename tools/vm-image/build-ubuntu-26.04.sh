#!/usr/bin/env bash
# Copyright (c) 2025 AUTHORS All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -euo pipefail

version="${YEET_VM_IMAGE_VERSION:-ubuntu-26.04-amd64-v1}"
out_dir="${1:-dist/$version}"
work_dir="${YEET_VM_IMAGE_WORK_DIR:-}"

ubuntu_base_url="${UBUNTU_CLOUD_BASE_URL:-https://cloud-images.ubuntu.com/resolute/current}"
ubuntu_image="${UBUNTU_CLOUD_IMAGE:-resolute-server-cloudimg-amd64.tar.gz}"
extract_vmlinux_url="${LINUX_EXTRACT_VMLINUX_URL:-https://raw.githubusercontent.com/torvalds/linux/v7.0/scripts/extract-vmlinux}"
firecracker_version="${FIRECRACKER_VERSION:-v1.14.3}"
firecracker_arch="${FIRECRACKER_ARCH:-x86_64}"
firecracker_tgz="firecracker-${firecracker_version}-${firecracker_arch}.tgz"
firecracker_url="${FIRECRACKER_URL:-https://github.com/firecracker-microvm/firecracker/releases/download/${firecracker_version}/${firecracker_tgz}}"
zstd_level="${ZSTD_LEVEL:-10}"

require() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 1
	fi
}

for cmd in awk chmod cp curl date debugfs file find grep install mkdir mktemp sha256sum stat tar zstd; do
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

echo "Downloading Ubuntu cloud image..."
curl -fL --retry 3 -o "$work_dir/$ubuntu_image" "$ubuntu_base_url/$ubuntu_image"
curl -fsSL --retry 3 -o "$work_dir/SHA256SUMS" "$ubuntu_base_url/SHA256SUMS"

expected_image_sha="$(awk -v image="$ubuntu_image" '$2 == "*" image || $2 == image { print $1; exit }' "$work_dir/SHA256SUMS")"
if [ -z "$expected_image_sha" ]; then
	echo "could not find $ubuntu_image in $ubuntu_base_url/SHA256SUMS" >&2
	exit 1
fi
actual_image_sha="$(sha256sum "$work_dir/$ubuntu_image" | awk '{ print $1 }')"
if [ "$actual_image_sha" != "$expected_image_sha" ]; then
	echo "Ubuntu cloud image checksum mismatch: got $actual_image_sha, want $expected_image_sha" >&2
	exit 1
fi

echo "Extracting Ubuntu rootfs image..."
tar xzf "$work_dir/$ubuntu_image" -C "$work_dir"
rootfs_source="$(find "$work_dir" -maxdepth 1 -name '*.img' -type f -print -quit)"
if [ -z "$rootfs_source" ]; then
	echo "Ubuntu cloud image tarball did not contain an .img file" >&2
	exit 1
fi

kernel_version="${UBUNTU_KERNEL_VERSION:-}"
if [ -z "$kernel_version" ]; then
	kernel_version="$(
		debugfs -R "ls -p /boot" "$rootfs_source" 2>/dev/null |
			awk -F/ '$6 ~ /^vmlinuz-[0-9].*-generic$/ { sub(/^vmlinuz-/, "", $6); print $6; exit }'
	)"
fi
if [ -z "$kernel_version" ]; then
	echo "could not detect Ubuntu kernel version in $rootfs_source" >&2
	exit 1
fi

echo "Extracting Ubuntu kernel $kernel_version..."
debugfs -R "dump -p /boot/vmlinuz-$kernel_version $work_dir/vmlinuz-$kernel_version" "$rootfs_source" >/dev/null 2>&1
debugfs -R "dump -p /boot/initrd.img-$kernel_version $work_dir/initrd.img" "$rootfs_source" >/dev/null 2>&1
curl -fsSL --retry 3 -o "$work_dir/extract-vmlinux" "$extract_vmlinux_url"
chmod +x "$work_dir/extract-vmlinux"
"$work_dir/extract-vmlinux" "$work_dir/vmlinuz-$kernel_version" >"$work_dir/vmlinux"
if ! file "$work_dir/vmlinux" | grep -q "ELF 64-bit"; then
	echo "extracted vmlinux is not an x86_64 ELF kernel" >&2
	file "$work_dir/vmlinux" >&2
	exit 1
fi

echo "Downloading Firecracker $firecracker_version..."
curl -fL --retry 3 -o "$work_dir/$firecracker_tgz" "$firecracker_url"
tar xzf "$work_dir/$firecracker_tgz" -C "$work_dir"
fc_dir="$work_dir/release-${firecracker_version}-${firecracker_arch}"
(
	cd "$fc_dir"
	sha256sum -c --ignore-missing SHA256SUMS
)

install -m 0644 "$rootfs_source" "$out_dir/rootfs.ext4"
install -m 0644 "$work_dir/vmlinux" "$out_dir/vmlinux"
install -m 0644 "$work_dir/initrd.img" "$out_dir/initrd.img"
install -m 0755 "$fc_dir/firecracker-${firecracker_version}-${firecracker_arch}" "$out_dir/firecracker"

echo "Compressing rootfs..."
zstd -T0 "-$zstd_level" -f --no-progress -o "$out_dir/rootfs.ext4.zst" "$out_dir/rootfs.ext4"

rootfs_size="$(stat -c %s "$out_dir/rootfs.ext4")"
rootfs_sha="$(sha256sum "$out_dir/rootfs.ext4.zst" | awk '{ print $1 }')"
kernel_sha="$(sha256sum "$out_dir/vmlinux" | awk '{ print $1 }')"
initrd_sha="$(sha256sum "$out_dir/initrd.img" | awk '{ print $1 }')"
firecracker_sha="$(sha256sum "$out_dir/firecracker" | awk '{ print $1 }')"
source_image_sha="$(sha256sum "$out_dir/rootfs.ext4" | awk '{ print $1 }')"
build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

cat >"$out_dir/manifest.json" <<JSON
{
  "name": "yeet-ubuntu-26.04",
  "version": "$version",
  "architecture": "x86_64",
  "kernel": "vmlinux",
  "initrd": "initrd.img",
  "rootfs": "rootfs.ext4.zst",
  "firecracker": "firecracker",
  "rootfs_size": $rootfs_size,
  "ubuntu_kernel_version": "$kernel_version",
  "provenance": {
    "build_time": "$build_time",
    "ubuntu_cloud_image_url": "$ubuntu_base_url/$ubuntu_image",
    "ubuntu_cloud_image_sha256": "$actual_image_sha",
    "ubuntu_cloud_sha256sums_url": "$ubuntu_base_url/SHA256SUMS",
    "ubuntu_rootfs_sha256": "$source_image_sha",
    "extract_vmlinux_url": "$extract_vmlinux_url",
    "firecracker_version": "$firecracker_version",
    "firecracker_url": "$firecracker_url"
  },
  "checksums": {
    "vmlinux": "$kernel_sha",
    "initrd.img": "$initrd_sha",
    "rootfs.ext4.zst": "$rootfs_sha",
    "firecracker": "$firecracker_sha"
  }
}
JSON

(
	cd "$out_dir"
	sha256sum manifest.json vmlinux initrd.img rootfs.ext4.zst firecracker >checksums.txt
)

rm -f "$out_dir/rootfs.ext4"

echo "Wrote VM image bundle to $out_dir"
echo "Version: $version"
echo "Ubuntu kernel: $kernel_version"
