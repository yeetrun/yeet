// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

func TestVMRunnerSystemctlCommands(t *testing.T) {
	withTempVMSystemdSystemDir(t)
	runner, calls := newRecordingVMRunner("devbox")

	for _, step := range []struct {
		name string
		fn   func() error
	}{
		{name: "start", fn: runner.Start},
		{name: "stop", fn: runner.Stop},
		{name: "restart", fn: runner.Restart},
		{name: "enable", fn: runner.Enable},
		{name: "disable", fn: runner.Disable},
		{name: "remove", fn: runner.Remove},
	} {
		if err := step.fn(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
	}

	want := [][]string{
		{"systemctl", "start", "yeet-vm-devbox.service"},
		{"systemctl", "stop", "yeet-vm-devbox.service"},
		{"systemctl", "restart", "yeet-vm-devbox.service"},
		{"systemctl", "enable", "yeet-vm-devbox.service"},
		{"systemctl", "disable", "yeet-vm-devbox.service"},
		{"systemctl", "disable", "--now", "yeet-vm-devbox.service"},
		{"systemctl", "daemon-reload"},
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls = %#v, want %#v", *calls, want)
	}
}

func TestVMRunnerStatusUsesQuietSystemctl(t *testing.T) {
	runner, calls := newRecordingVMRunner("devbox")

	status, err := runner.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status != svc.StatusRunning {
		t.Fatalf("Status = %q, want running", status)
	}

	want := [][]string{{"systemctl", "is-active", "--quiet", "yeet-vm-devbox.service"}}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls = %#v, want %#v", *calls, want)
	}
}

func TestVMRunnerSystemctlIgnoresPrewiredCommandOutput(t *testing.T) {
	runner := &vmRunner{name: "devbox"}
	runner.SetNewCmd(func(string, ...string) *exec.Cmd {
		cmd := exec.Command("true")
		cmd.Stdout = &bytes.Buffer{}
		cmd.Stderr = &bytes.Buffer{}
		return cmd
	})

	if err := runner.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

func TestVMRunnerRemoveDeletesUnitAndReloads(t *testing.T) {
	tmp := withTempVMSystemdSystemDir(t)

	unitPath := filepath.Join(tmp, "yeet-vm-devbox.service")
	if err := os.WriteFile(unitPath, []byte("[Service]\n"), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}

	runner, calls := newRecordingVMRunner("devbox")
	if err := runner.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := os.Stat(unitPath); !os.IsNotExist(err) {
		t.Fatalf("unit stat error = %v, want not exist", err)
	}
	want := [][]string{
		{"systemctl", "disable", "--now", "yeet-vm-devbox.service"},
		{"systemctl", "daemon-reload"},
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls = %#v, want %#v", *calls, want)
	}
}

func TestVMRunnerLogsUsesJournalctlArgs(t *testing.T) {
	runner, calls := newRecordingVMRunner("devbox")

	if err := runner.Logs(&svc.LogOptions{Follow: true, Lines: 50}); err != nil {
		t.Fatalf("Logs: %v", err)
	}

	want := [][]string{{
		"journalctl",
		"--no-pager",
		"--output=cat",
		"--follow",
		"--lines=50",
		"--unit=yeet-vm-devbox.service",
	}}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls = %#v, want %#v", *calls, want)
	}
}

func TestServiceRunnerForTypeVM(t *testing.T) {
	execer := &ttyExecer{sn: "devbox"}
	runner, err := execer.serviceRunnerForType(db.ServiceTypeVM)
	if err != nil {
		t.Fatalf("serviceRunnerForType: %v", err)
	}
	vm, ok := runner.(*vmRunner)
	if !ok {
		t.Fatalf("runner = %T, want *vmRunner", runner)
	}
	if vm.name != "devbox" {
		t.Fatalf("runner name = %q, want devbox", vm.name)
	}
}

func newRecordingVMRunner(name string) (*vmRunner, *[][]string) {
	var calls [][]string
	runner := &vmRunner{name: name}
	runner.SetNewCmd(func(name string, args ...string) *exec.Cmd {
		call := append([]string{name}, args...)
		calls = append(calls, call)
		return exec.Command("true")
	})
	return runner, &calls
}

func withTempVMSystemdSystemDir(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	oldSystemdDir := vmSystemdSystemDir
	vmSystemdSystemDir = tmp
	t.Cleanup(func() { vmSystemdSystemDir = oldSystemdDir })
	return tmp
}
