// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"path/filepath"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestTranslateMountPathToUnitName(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "simple absolute", path: "/mnt/data", want: "mnt-data"},
		{name: "preserves safe chars", path: "/mnt/data.v1_host:share", want: "mnt-data.v1_host:share"},
		{name: "escapes spaces", path: "/mnt/my data", want: "mnt-my\\x20data"},
		{name: "collapses extra slashes", path: "//mnt///data//", want: "mnt-data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := translateMountPathToUnitName(tt.path); got != tt.want {
				t.Fatalf("translateMountPathToUnitName(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestSystemdMountFilesBuildsMountAndAutomountPaths(t *testing.T) {
	root := t.TempDir()
	vol := db.Volume{
		Name: "data",
		Src:  "host:/srv/data",
		Path: "/mnt/data",
		Type: "sshfs",
		Opts: "ro",
		Deps: "network-online.target",
	}
	files, err := systemdMountFiles(root, vol)
	if err != nil {
		t.Fatalf("systemdMountFiles failed: %v", err)
	}
	if files.unitName != "mnt-data" {
		t.Fatalf("unit name = %q, want mnt-data", files.unitName)
	}
	if files.mountPath != filepath.Join(root, "mnt-data.mount") {
		t.Fatalf("mount path = %q", files.mountPath)
	}
	if files.automountPath != filepath.Join(root, "mnt-data.automount") {
		t.Fatalf("automount path = %q", files.automountPath)
	}
	if len(files.mountContent) == 0 || len(files.automountContent) == 0 {
		t.Fatalf("expected rendered systemd unit contents")
	}
}

func TestSystemdUnitCommands(t *testing.T) {
	cmds := systemdUmountCommands("mnt-data", true, true)
	want := [][]string{
		{"systemctl", "disable", "--now", "mnt-data.automount"},
		{"systemctl", "stop", "mnt-data.mount"},
	}
	if len(cmds) != len(want) {
		t.Fatalf("command count = %d, want %d", len(cmds), len(want))
	}
	for i := range want {
		if len(cmds[i]) != len(want[i]) {
			t.Fatalf("command %d = %#v, want %#v", i, cmds[i], want[i])
		}
		for j := range want[i] {
			if cmds[i][j] != want[i][j] {
				t.Fatalf("command %d = %#v, want %#v", i, cmds[i], want[i])
			}
		}
	}
}
