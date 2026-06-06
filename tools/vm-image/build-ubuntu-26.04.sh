#!/usr/bin/env bash
# Copyright (c) 2025 AUTHORS All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -euo pipefail

profile="${YEET_VM_IMAGE_PROFILE:-fast}"
version="${YEET_VM_IMAGE_VERSION:-ubuntu-26.04-amd64-v11}"
out_dir="${1:-dist/$version}"
work_dir="${YEET_VM_IMAGE_WORK_DIR:-}"
kernel_path="${YEET_VM_KERNEL_PATH:-}"
kernel_version_override="${YEET_VM_KERNEL_VERSION:-}"
guest_init_path="${YEET_VM_INIT_PATH:-}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"
ghostty_terminfo_source="${YEET_VM_GHOSTTY_TERMINFO:-$repo_root/pkg/catch/xterm-ghostty.terminfo}"

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

for cmd in awk chmod cmp cp curl date debugfs file find grep install mkdir mktemp sha256sum stat tar zstd; do
	require "$cmd"
done

case "$profile" in
fast | stock)
	;;
*)
	echo "unsupported YEET_VM_IMAGE_PROFILE=$profile (expected fast or stock)" >&2
	exit 1
	;;
esac

if [ "$profile" = "fast" ]; then
	for cmd in chroot id infocmp mount mountpoint tic umount; do
		require "$cmd"
	done
	if [ -z "$kernel_path" ]; then
		echo "YEET_VM_KERNEL_PATH is required for the fast profile" >&2
		echo "Set YEET_VM_IMAGE_PROFILE=stock to build the old Ubuntu-kernel/initrd image." >&2
		exit 1
	fi
	if [ ! -r "$kernel_path" ]; then
		echo "YEET_VM_KERNEL_PATH is not readable: $kernel_path" >&2
		exit 1
	fi
	if [ -z "$guest_init_path" ]; then
		echo "YEET_VM_INIT_PATH is required for the fast profile" >&2
		exit 1
	fi
	if [ ! -x "$guest_init_path" ]; then
		echo "YEET_VM_INIT_PATH is not executable: $guest_init_path" >&2
		exit 1
	fi
	if [ ! -r "$ghostty_terminfo_source" ]; then
		echo "YEET_VM_GHOSTTY_TERMINFO is not readable: $ghostty_terminfo_source" >&2
		exit 1
	fi
	if [ "$(id -u)" != 0 ]; then
		echo "the fast profile must run as root so it can mount and customize the rootfs" >&2
		exit 1
	fi
fi

if [ -z "$work_dir" ]; then
	work_dir="$(mktemp -d)"
	cleanup_work=1
else
	mkdir -p "$work_dir"
	cleanup_work=0
fi

rootfs_mount=""
cleanup_rootfs_mount() {
	if [ -z "$rootfs_mount" ]; then
		return
	fi
	for rel in run sys proc dev; do
		if mountpoint -q "$rootfs_mount/$rel" 2>/dev/null; then
			umount "$rootfs_mount/$rel" || true
		fi
	done
	if mountpoint -q "$rootfs_mount" 2>/dev/null; then
		umount "$rootfs_mount" || true
	fi
}

cleanup() {
	cleanup_rootfs_mount
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

ubuntu_kernel_version=""
kernel_version=""
kernel_source=""
initrd_artifact=""

detect_ubuntu_kernel_version() {
	local source="$1"
	if [ -n "${UBUNTU_KERNEL_VERSION:-}" ]; then
		printf '%s\n' "$UBUNTU_KERNEL_VERSION"
		return
	fi
	debugfs -R "ls -p /boot" "$source" 2>/dev/null |
		awk -F/ '$6 ~ /^vmlinuz-[0-9].*-generic$/ { sub(/^vmlinuz-/, "", $6); print $6; exit }'
}

extract_ubuntu_kernel() {
	ubuntu_kernel_version="$(detect_ubuntu_kernel_version "$rootfs_source")"
	if [ -z "$ubuntu_kernel_version" ]; then
		echo "could not detect Ubuntu kernel version in $rootfs_source" >&2
		exit 1
	fi
	kernel_version="$ubuntu_kernel_version"
	kernel_source="ubuntu-cloud-image"
	initrd_artifact="initrd.img"

	echo "Extracting Ubuntu kernel $ubuntu_kernel_version..."
	debugfs -R "dump -p /boot/vmlinuz-$ubuntu_kernel_version $work_dir/vmlinuz-$ubuntu_kernel_version" "$rootfs_source" >/dev/null 2>&1
	debugfs -R "dump -p /boot/initrd.img-$ubuntu_kernel_version $work_dir/initrd.img" "$rootfs_source" >/dev/null 2>&1
	curl -fsSL --retry 3 -o "$work_dir/extract-vmlinux" "$extract_vmlinux_url"
	chmod +x "$work_dir/extract-vmlinux"
	"$work_dir/extract-vmlinux" "$work_dir/vmlinuz-$ubuntu_kernel_version" >"$work_dir/vmlinux"
}

install_provided_kernel() {
	kernel_source="yeet-managed"
	kernel_version="$kernel_version_override"
	if [ -z "$kernel_version" ]; then
		kernel_version="$(basename "$kernel_path")"
	fi

	echo "Installing yeet-managed kernel $kernel_version..."
	install -m 0644 "$kernel_path" "$work_dir/vmlinux"
}

case "$profile" in
fast)
	install_provided_kernel
	;;
stock)
	extract_ubuntu_kernel
	;;
esac

if ! file "$work_dir/vmlinux" | grep -q "ELF 64-bit"; then
	echo "extracted vmlinux is not an x86_64 ELF kernel" >&2
	file "$work_dir/vmlinux" >&2
	exit 1
fi

write_fast_rootfs_policy_files() {
	local root="$1"
	install -d -m 0755 \
		"$root/etc/apt/preferences.d" \
		"$root/etc/sysctl.d" \
		"$root/etc/tmpfiles.d" \
		"$root/usr/share/doc/yeet-vm-image"
	cat >"$root/etc/apt/preferences.d/99-yeet-managed-kernel" <<'EOF'
Package: linux-image-* linux-modules-* linux-modules-extra-* linux-headers-* linux-generic* linux-virtual* grub-* shim-signed initramfs-tools initramfs-tools-* snapd snap-confine squashfs-tools
Pin: version *
Pin-Priority: -1
EOF
	cat >"$root/usr/share/doc/yeet-vm-image/kernel.md" <<'EOF'
# Yeet VM Kernel

This image boots with Firecracker direct kernel boot. The kernel is supplied by
the yeet VM image bundle manifest, not by packages installed inside the guest.

Guest apt upgrades intentionally do not install Ubuntu kernel, bootloader, or
initramfs packages. To update the boot kernel, publish a new yeet VM image
bundle and create VMs from that image version.

The fast yeet VM image profile intentionally does not support loadable kernel
modules or snap packages. Router-oriented kernel features such as TUN,
netfilter, conntrack, nftables, nft NAT/masquerade, and Ubuntu iptables-nft
compatibility are built into the yeet-managed kernel instead of loaded from
guest module packages.
EOF
	cat >"$root/usr/share/doc/yeet-vm-image/init.md" <<'EOF'
# Yeet VM Init

Fast yeet VM images boot through `/usr/local/lib/yeet-vm/yeet-init`.
The init shim performs small pre-systemd setup, reports the first kernel
configured IPv4 address on the serial console, and then execs systemd as PID 1.

SSH remains managed by systemd through `yeet-sshd.service`; `yeet run` waits for
the systemd-backed `yeet-guest-ready.service` marker before returning.
EOF
	cat >"$root/etc/sysctl.d/99-yeet-vm-router.conf" <<'EOF'
# Yeet VMs should be ready to run guest-managed routers and exit nodes.
net.ipv4.ip_forward = 1
net.ipv6.conf.all.forwarding = 1
EOF
	cat >"$root/etc/tmpfiles.d/yeet-vm-tun.conf" <<'EOF'
d /dev/net 0755 root root -
c /dev/net/tun 0666 root root 10:200
EOF
}

install_fast_rootfs_terminfo() {
	local root="$1"
	install -d -m 0755 "$root/etc/terminfo"
	tic -x -o "$root/etc/terminfo" "$ghostty_terminfo_source"
	TERMINFO="$root/etc/terminfo" infocmp -x xterm-ghostty >/dev/null
}

resolve_guest_path() {
	local root="$1"
	local guest_path="$2"
	chroot "$root" /usr/bin/readlink -f "$guest_path" 2>/dev/null || true
}

same_guest_file() {
	local root="$1"
	local left="$2"
	local right="$3"
	if [ -z "$left" ] || [ -z "$right" ]; then
		return 1
	fi
	if [ "$left" = "$right" ]; then
		return 0
	fi
	cmp -s "$root$left" "$root$right"
}

merge_usr_sbin_into_usr_bin() {
	local root="$1"
	local usr_bin="$root/usr/bin"
	local usr_sbin="$root/usr/sbin"
	if [ -L "$usr_sbin" ] || [ ! -d "$usr_sbin" ]; then
		return
	fi
	install -d -m 0755 "$usr_bin"
	while IFS= read -r src; do
		local name dst src_guest dst_guest src_real dst_real
		name="$(basename "$src")"
		dst="$usr_bin/$name"
		src_guest="/usr/sbin/$name"
		dst_guest="/usr/bin/$name"
		if [ ! -e "$dst" ] && [ ! -L "$dst" ]; then
			mv "$src" "$usr_bin/"
			continue
		fi
		src_real="$(resolve_guest_path "$root" "$src_guest")"
		dst_real="$(resolve_guest_path "$root" "$dst_guest")"
		case "$dst_real" in
		/usr/sbin/* | /sbin/*)
			rm -f "$dst"
			mv "$src" "$usr_bin/"
			;;
		/usr/bin/*)
			if same_guest_file "$root" "$src_real" "$dst_real"; then
				rm -f "$src"
			else
				echo "unmergeable /usr/sbin collision: $src_guest -> ${src_real:-?}, $dst_guest -> ${dst_real:-?}" >&2
				exit 1
			fi
			;;
		*)
			echo "unmergeable /usr/sbin collision: $src_guest -> ${src_real:-?}, $dst_guest -> ${dst_real:-?}" >&2
			exit 1
			;;
		esac
	done < <(find "$usr_sbin" -mindepth 1 -maxdepth 1 -print)
	rmdir "$usr_sbin"
	ln -s bin "$usr_sbin"
	ln -snf usr/bin "$root/sbin"
}

customize_fast_rootfs() {
	local rootfs="$1"
	rootfs_mount="$work_dir/rootfs-mount"
	mkdir -p "$rootfs_mount"

	echo "Customizing rootfs for yeet-managed kernel policy..."
	mount -o loop,rw "$rootfs" "$rootfs_mount"
	for rel in dev proc sys run; do
		mkdir -p "$rootfs_mount/$rel"
	done
	mount --bind /dev "$rootfs_mount/dev"
	mount -t proc proc "$rootfs_mount/proc"
	mount -t sysfs sysfs "$rootfs_mount/sys"
	mount --bind /run "$rootfs_mount/run"

	write_fast_rootfs_policy_files "$rootfs_mount"
	install -d -m 0755 "$rootfs_mount/usr/local/lib/yeet-vm"
	install -m 0755 "$guest_init_path" "$rootfs_mount/usr/local/lib/yeet-vm/yeet-init"
	install_fast_rootfs_terminfo "$rootfs_mount"
	cat >"$rootfs_mount/usr/sbin/policy-rc.d" <<'EOF'
#!/bin/sh
exit 101
EOF
	chmod +x "$rootfs_mount/usr/sbin/policy-rc.d"

	chroot "$rootfs_mount" /bin/sh <<'EOF'
set -e
export DEBIAN_FRONTEND=noninteractive
packages="$(dpkg-query -W -f='${binary:Package}\n' 2>/dev/null | awk '/^(linux-image-|linux-modules-|linux-modules-extra-|linux-headers-|linux-generic|linux-virtual|grub-|shim-signed$|initramfs-tools|snapd$|snap-confine$|squashfs-tools$|cloud-init$|pollinate$|apport$|apport-symptoms$|fwupd$|fwupd-signed$|update-notifier-common$|update-manager-core$|xfsprogs$|modemmanager$|udisks2$|multipath-tools$|lvm2$|rsyslog$|ufw$|unattended-upgrades$|open-vm-tools$|open-vm-tools-desktop$|vgauth$|netplan.io$|networkd-dispatcher$|sysstat$|chrony$|plymouth$|plymouth-|keyboard-configuration$|console-setup$)/ { print }')"
if [ -n "$packages" ]; then
	apt-get purge -y $packages
fi
apt-get update
apt-get install -y --no-install-recommends iptables nftables
apt-get autoremove -y --purge
apt-get clean
rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb /var/cache/snapd/* /var/lib/snapd/cache/*
mkdir -p /etc/systemd/system
ln -sf /dev/null /etc/systemd/system/snapd.service
ln -sf /dev/null /etc/systemd/system/snapd.socket
ln -sf /dev/null /etc/systemd/system/snapd.seeded.service
rm -rf /etc/netplan
mkdir -p /etc/systemd/system/multi-user.target.wants /etc/systemd/system/timers.target.wants /etc/systemd/network
ln -sf /usr/lib/systemd/system/systemd-networkd.service /etc/systemd/system/multi-user.target.wants/systemd-networkd.service
ln -sf /usr/lib/systemd/system/systemd-resolved.service /etc/systemd/system/multi-user.target.wants/systemd-resolved.service
if [ -e /usr/lib/systemd/system/systemd-timesyncd.service ]; then
	ln -sf /usr/lib/systemd/system/systemd-timesyncd.service /etc/systemd/system/multi-user.target.wants/systemd-timesyncd.service
fi
ln -sf /usr/lib/systemd/system/multi-user.target /etc/systemd/system/default.target
mkdir -p /etc/default
printf 'EXTRA_OPTS=""\n' >/etc/default/cron
for unit in \
	apt-daily.timer \
	apt-daily-upgrade.timer \
	e2scrub_all.timer \
	e2scrub_reap.service \
	fstrim.timer \
	fwupd.service \
	fwupd-refresh.service \
	fwupd-refresh.timer \
	man-db.timer \
	motd-news.timer \
	proc-sys-fs-binfmt_misc.automount \
	proc-sys-fs-binfmt_misc.mount \
	pollinate.service \
	cloud-init.service \
	cloud-config.service \
	cloud-final.service \
	update-notifier-download.service \
	update-notifier-download.timer \
	update-notifier-motd.service \
	update-notifier-motd.timer \
	xfs_scrub_all.service \
	xfs_scrub_all.timer \
	NetworkManager.service \
	NetworkManager-wait-online.service \
	systemd-modules-load.service \
	systemd-networkd-wait-online.service \
	modprobe@.service \
	modprobe@configfs.service \
	modprobe@drm.service \
	modprobe@efi_pstore.service \
	modprobe@fuse.service \
	netplan-configure.service \
	networkd-dispatcher.service \
	sysstat.service \
	sysstat-collect.timer \
	sysstat-summary.timer \
	chrony.service \
	ldconfig.service \
	keyboard-setup.service \
	console-setup.service \
	plymouth-start.service \
	plymouth-read-write.service \
	plymouth-quit.service \
	plymouth-quit-wait.service \
	plymouth-halt.service \
	plymouth-kexec.service \
	plymouth-poweroff.service \
	plymouth-reboot.service \
	plymouth-switch-root.service \
	plymouth-switch-root-initramfs.service
do
	ln -sf /dev/null "/etc/systemd/system/$unit"
done
ldconfig
EOF
	merge_usr_sbin_into_usr_bin "$rootfs_mount"
	rm -f "$rootfs_mount/usr/sbin/policy-rc.d"
	cleanup_rootfs_mount
	rootfs_mount=""
}

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
if [ "$profile" = "fast" ] && [ -r "$(dirname "$kernel_path")/kernel.config" ]; then
	install -m 0644 "$(dirname "$kernel_path")/kernel.config" "$out_dir/kernel.config"
fi
if [ -n "$initrd_artifact" ]; then
	install -m 0644 "$work_dir/initrd.img" "$out_dir/initrd.img"
fi
install -m 0755 "$fc_dir/firecracker-${firecracker_version}-${firecracker_arch}" "$out_dir/firecracker"

if [ "$profile" = "fast" ]; then
	customize_fast_rootfs "$out_dir/rootfs.ext4"
fi

echo "Compressing rootfs..."
zstd -T0 "-$zstd_level" -f --no-progress -o "$out_dir/rootfs.ext4.zst" "$out_dir/rootfs.ext4"

rootfs_size="$(stat -c %s "$out_dir/rootfs.ext4")"
rootfs_sha="$(sha256sum "$out_dir/rootfs.ext4.zst" | awk '{ print $1 }')"
kernel_sha="$(sha256sum "$out_dir/vmlinux" | awk '{ print $1 }')"
firecracker_sha="$(sha256sum "$out_dir/firecracker" | awk '{ print $1 }')"
source_image_sha="$(sha256sum "$out_dir/rootfs.ext4" | awk '{ print $1 }')"
build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
snap_support=false
kernel_policy="yeet-managed"
guest_init="/usr/local/lib/yeet-vm/yeet-init"
guest_init_sha=""
if [ "$profile" = "fast" ]; then
	guest_init_sha="$(sha256sum "$guest_init_path" | awk '{ print $1 }')"
fi
if [ "$profile" = "stock" ]; then
	snap_support=true
	kernel_policy="ubuntu-kernel-with-initrd"
	guest_init=""
fi
initrd_manifest_line=""
initrd_checksum_line=""
if [ -n "$initrd_artifact" ]; then
	initrd_sha="$(sha256sum "$out_dir/initrd.img" | awk '{ print $1 }')"
	initrd_manifest_line='  "initrd": "initrd.img",'
	initrd_checksum_line='    "initrd.img": "'"$initrd_sha"'",'
fi

cat >"$out_dir/manifest.json" <<JSON
{
  "name": "yeet-ubuntu-26.04",
  "version": "$version",
  "architecture": "x86_64",
  "image_profile": "$profile",
  "kernel_policy": "$kernel_policy",
  "snap_support": $snap_support,
  "guest_init": "$guest_init",
  "guest_init_sha256": "$guest_init_sha",
  "kernel": "vmlinux",
$initrd_manifest_line
  "rootfs": "rootfs.ext4.zst",
  "firecracker": "firecracker",
  "rootfs_size": $rootfs_size,
  "kernel_version": "$kernel_version",
  "ubuntu_kernel_version": "$ubuntu_kernel_version",
  "provenance": {
    "build_time": "$build_time",
    "ubuntu_cloud_image_url": "$ubuntu_base_url/$ubuntu_image",
    "ubuntu_cloud_image_sha256": "$actual_image_sha",
    "ubuntu_cloud_sha256sums_url": "$ubuntu_base_url/SHA256SUMS",
    "ubuntu_rootfs_sha256": "$source_image_sha",
    "kernel_source": "$kernel_source",
    "extract_vmlinux_url": "$extract_vmlinux_url",
    "firecracker_version": "$firecracker_version",
    "firecracker_url": "$firecracker_url"
  },
  "checksums": {
    "vmlinux": "$kernel_sha",
$initrd_checksum_line
    "rootfs.ext4.zst": "$rootfs_sha",
    "firecracker": "$firecracker_sha"
  }
}
JSON

(
	cd "$out_dir"
	checksum_files=(manifest.json vmlinux)
	if [ -n "$initrd_artifact" ]; then
		checksum_files+=(initrd.img)
	fi
	checksum_files+=(rootfs.ext4.zst firecracker)
	if [ -f kernel.config ]; then
		checksum_files+=(kernel.config)
	fi
	sha256sum "${checksum_files[@]}" >checksums.txt
)

rm -f "$out_dir/rootfs.ext4"

echo "Wrote VM image bundle to $out_dir"
echo "Version: $version"
echo "Profile: $profile"
echo "Kernel: $kernel_version"
