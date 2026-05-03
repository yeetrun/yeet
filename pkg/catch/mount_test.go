// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestSystemdMounterMountWritesUnitFilesAndEnablesAutomount(t *testing.T) {
	root := t.TempDir()
	systemdRoot := filepath.Join(root, "systemd")
	if err := os.Mkdir(systemdRoot, 0o755); err != nil {
		t.Fatalf("mkdir systemd root: %v", err)
	}
	mountTarget := filepath.Join(root, "mnt", "data")
	if err := os.Mkdir(filepath.Dir(mountTarget), 0o755); err != nil {
		t.Fatalf("mkdir mount parent: %v", err)
	}

	var commands [][]string
	stubSystemdMountHooks(t, systemdRoot, &commands, nil)

	vol := db.Volume{
		Name: "data",
		Src:  "host:/srv/data",
		Path: mountTarget,
		Type: "sshfs",
		Opts: "ro",
		Deps: "network-online.target",
	}
	if err := (&systemdMounter{v: vol}).mount(); err != nil {
		t.Fatalf("mount failed: %v", err)
	}

	if info, err := os.Stat(mountTarget); err != nil || !info.IsDir() {
		t.Fatalf("mount target stat = %v, %v; want directory", info, err)
	}
	unitName := translateMountPathToUnitName(mountTarget)
	mountUnit, err := os.ReadFile(filepath.Join(systemdRoot, unitName+".mount"))
	if err != nil {
		t.Fatalf("read mount unit: %v", err)
	}
	if got := string(mountUnit); !strings.Contains(got, "Where="+mountTarget) || !strings.Contains(got, "What=host:/srv/data") {
		t.Fatalf("mount unit content missing volume fields:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(systemdRoot, unitName+".automount")); err != nil {
		t.Fatalf("stat automount unit: %v", err)
	}

	wantCommands := [][]string{
		{"daemon-reload"},
		{"enable", "--now", unitName + ".automount"},
	}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("systemd commands = %#v, want %#v", commands, wantCommands)
	}
}

func TestSystemdMounterUmountStopsActiveUnitsAndRemovesFiles(t *testing.T) {
	root := t.TempDir()
	systemdRoot := filepath.Join(root, "systemd")
	if err := os.Mkdir(systemdRoot, 0o755); err != nil {
		t.Fatalf("mkdir systemd root: %v", err)
	}
	mountTarget := filepath.Join(root, "mnt", "data")
	if err := os.MkdirAll(mountTarget, 0o755); err != nil {
		t.Fatalf("mkdir mount target: %v", err)
	}
	unitName := translateMountPathToUnitName(mountTarget)
	for _, suffix := range []string{".mount", ".automount"} {
		if err := os.WriteFile(filepath.Join(systemdRoot, unitName+suffix), []byte("unit\n"), 0o644); err != nil {
			t.Fatalf("write unit %s: %v", suffix, err)
		}
	}

	var commands [][]string
	stubSystemdMountHooks(t, systemdRoot, &commands, func(args ...string) bool {
		if len(args) != 3 || args[1] != "--quiet" {
			return false
		}
		return args[0] == "is-enabled" && args[2] == unitName+".automount" ||
			args[0] == "is-active" && args[2] == unitName+".mount"
	})

	if err := (&systemdMounter{v: db.Volume{Path: mountTarget}}).umount(); err != nil {
		t.Fatalf("umount failed: %v", err)
	}

	wantCommands := [][]string{
		{"disable", "--now", unitName + ".automount"},
		{"stop", unitName + ".mount"},
		{"daemon-reload"},
	}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("systemd commands = %#v, want %#v", commands, wantCommands)
	}
	for _, path := range []string{
		filepath.Join(systemdRoot, unitName+".mount"),
		filepath.Join(systemdRoot, unitName+".automount"),
		mountTarget,
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err = %v", path, err)
		}
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

func stubSystemdMountHooks(t *testing.T, systemdRoot string, commands *[][]string, status func(args ...string) bool) {
	t.Helper()
	oldSystemdDir := systemdSystemDir
	oldRunSystemdCommand := runSystemdCommand
	oldSystemdQuietStatus := systemdQuietStatus
	systemdSystemDir = systemdRoot
	runSystemdCommand = func(args ...string) error {
		*commands = append(*commands, append([]string(nil), args...))
		return nil
	}
	if status == nil {
		status = func(args ...string) bool { return false }
	}
	systemdQuietStatus = status
	t.Cleanup(func() {
		systemdSystemDir = oldSystemdDir
		runSystemdCommand = oldRunSystemdCommand
		systemdQuietStatus = oldSystemdQuietStatus
	})
}
