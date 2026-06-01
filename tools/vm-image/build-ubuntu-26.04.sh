#!/usr/bin/env bash
# Copyright (c) 2025 AUTHORS All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -euo pipefail

out_dir="${1:-dist/yeet-ubuntu-26.04-amd64-v0}"
mkdir -p "$out_dir"

cat >"$out_dir/README.txt" <<'TXT'
This directory is the yeet Ubuntu 26.04 VM bundle staging area.
Build hosts must provide debootstrap, qemu-img, e2fsprogs, zstd, curl, and a Firecracker release binary.
TXT

echo "Image build staging directory: $out_dir"
echo "The production rootfs build runs on Linux with KVM-capable validation and publishes manifest.json, vmlinux, rootfs.ext4.zst, firecracker, and checksums.txt."
