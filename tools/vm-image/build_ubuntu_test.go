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
		`version="${YEET_VM_IMAGE_VERSION:-ubuntu-26.04-amd64-v8}"`,
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
