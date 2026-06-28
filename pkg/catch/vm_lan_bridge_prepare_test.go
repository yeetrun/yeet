// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPrepareVMLANBridgeWritesReadyStatusAfterValidation(t *testing.T) {
	root := t.TempDir()
	runner := newFakeVMLANBridgeRunner()
	runner.plan = vmLANBridgePlan{NeedsPrepare: true, Bridge: "br0", Parent: "eno1", Renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true}}
	runner.netplan = []byte(eno1DHCPNetplan)

	status, err := prepareVMLANBridge(root, runner, vmLANBridgePrepareOptions{Yes: true})
	if err != nil {
		t.Fatalf("prepareVMLANBridge: %v", err)
	}
	if status.Phase != vmLANBridgePhaseReady || status.Bridge != "br0" || status.Parent != "eno1" {
		t.Fatalf("status = %#v, want ready br0 eno1", status)
	}
	if !runner.scheduledRollback || runner.rollbackCanceled != true || !runner.applied {
		t.Fatalf("runner = %#v, want scheduled rollback, applied, canceled rollback", runner)
	}
	if got, want := runner.calls, []string{"plan", "read", "backup", "overlay", "schedule-rollback", "generate", "apply", "validate", "cancel-rollback"}; !slices.Equal(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	if runner.readParent != "eno1" || runner.validatedBridge != "br0" || runner.validatedParent != "eno1" {
		t.Fatalf("runner args = read %q validate %q/%q, want eno1 br0/eno1", runner.readParent, runner.validatedBridge, runner.validatedParent)
	}
	if runner.backupPath == "" || !strings.HasPrefix(runner.backupPath, filepath.Join(root, vmLANBridgePrepareStateDir, "netplan-")) {
		t.Fatalf("backupPath = %q, want timestamped state-dir backup", runner.backupPath)
	}
	if !strings.HasSuffix(runner.backupPath, status.ID+".yaml") {
		t.Fatalf("backupPath = %q, want operation id %q", runner.backupPath, status.ID)
	}
	if string(runner.backupContent) != eno1DHCPNetplan {
		t.Fatalf("backupContent = %q, want original netplan", runner.backupContent)
	}
	if runner.overlayPath != vmLANBridgeNetplanOverlay || !strings.Contains(string(runner.overlayContent), "br0:") {
		t.Fatalf("overlay path/content = %q\n%s", runner.overlayPath, runner.overlayContent)
	}
	if runner.scheduledID != status.ID || runner.canceledID != status.ID || runner.rollbackDelay != vmLANBridgeRollbackDelay {
		t.Fatalf("rollback schedule/cancel = id %q cancel %q delay %s, want status id %q delay %s", runner.scheduledID, runner.canceledID, runner.rollbackDelay, status.ID, vmLANBridgeRollbackDelay)
	}
	fileStatus := readVMLANBridgePrepareStatus(t, root)
	if fileStatus.Phase != vmLANBridgePhaseReady || fileStatus.ID != status.ID || fileStatus.Bridge != "br0" || fileStatus.Parent != "eno1" {
		t.Fatalf("status file = %#v, want ready status %#v", fileStatus, status)
	}
	if fileStatus.BackupPath != runner.backupPath || fileStatus.OverlayPath != vmLANBridgeNetplanOverlay {
		t.Fatalf("status paths = %q/%q, want %q/%q", fileStatus.BackupPath, fileStatus.OverlayPath, runner.backupPath, vmLANBridgeNetplanOverlay)
	}
}

func TestPrepareVMLANBridgeRequiresYes(t *testing.T) {
	_, err := PrepareVMLANBridge(t.TempDir(), false)
	if err == nil || !strings.Contains(err.Error(), "--yes is required") {
		t.Fatalf("PrepareVMLANBridge error = %v, want --yes is required", err)
	}
}

func TestPrepareVMLANBridgeExportedRunsStateMachine(t *testing.T) {
	root := t.TempDir()
	runner := newPreparedVMLANBridgeRunner()
	oldFactory := vmLANBridgePrepareRunnerFactory
	factoryRoot := ""
	t.Cleanup(func() {
		vmLANBridgePrepareRunnerFactory = oldFactory
	})
	vmLANBridgePrepareRunnerFactory = func(root string) vmLANBridgePrepareRunner {
		factoryRoot = root
		return runner
	}

	status, err := PrepareVMLANBridge(root, true)
	if err != nil {
		t.Fatalf("PrepareVMLANBridge: %v", err)
	}
	if factoryRoot != root {
		t.Fatalf("factory root = %q, want %q", factoryRoot, root)
	}
	if status.Phase != string(vmLANBridgePhaseReady) || status.Bridge != "br0" || status.Parent != "eno1" {
		t.Fatalf("status = %#v, want ready br0 eno1", status)
	}
	if got, want := runner.calls, []string{"plan", "read", "backup", "overlay", "schedule-rollback", "generate", "apply", "validate", "cancel-rollback"}; !slices.Equal(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	fileStatus := readVMLANBridgePrepareStatus(t, root)
	if fileStatus.ID != status.ID || fileStatus.Phase != vmLANBridgePhaseReady || fileStatus.OverlayPath != vmLANBridgeNetplanOverlay {
		t.Fatalf("status file = %#v, exported status = %#v", fileStatus, status)
	}
}

func TestPrepareVMLANBridgeReturnsReadyWithoutMutationWhenPlanReady(t *testing.T) {
	root := t.TempDir()
	runner := newFakeVMLANBridgeRunner()
	runner.plan = vmLANBridgePlan{Ready: true, Bridge: "br0", Parent: "eno1"}

	status, err := prepareVMLANBridge(root, runner, vmLANBridgePrepareOptions{})
	if err != nil {
		t.Fatalf("prepareVMLANBridge: %v", err)
	}
	if status.Phase != vmLANBridgePhaseReady || status.Bridge != "br0" || status.Parent != "eno1" {
		t.Fatalf("status = %#v, want ready br0 eno1", status)
	}
	if got, want := runner.calls, []string{"plan"}; !slices.Equal(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	if runner.readParent != "" || runner.backupPath != "" || runner.overlayPath != "" || runner.generated || runner.scheduledRollback || runner.applied || runner.validatedBridge != "" || runner.validatedParent != "" || runner.rollbackCanceled || runner.rolledBack {
		t.Fatalf("runner = %#v, want no mutation after plan", runner)
	}
	if runner.scheduledID != "" || runner.canceledID != "" || runner.rolledBackID != "" {
		t.Fatalf("rollback IDs = schedule %q cancel %q rollback %q, want empty", runner.scheduledID, runner.canceledID, runner.rolledBackID)
	}
	fileStatus := readVMLANBridgePrepareStatus(t, root)
	if fileStatus.Phase != vmLANBridgePhaseReady {
		t.Fatalf("status file = %#v, want ready", fileStatus)
	}
	if fileStatus.BackupPath != "" || fileStatus.OverlayPath != "" {
		t.Fatalf("status paths = %q/%q, want empty", fileStatus.BackupPath, fileStatus.OverlayPath)
	}
}

func TestPrepareVMLANBridgeRollsBackWhenValidationFails(t *testing.T) {
	root := t.TempDir()
	runner := newFakeVMLANBridgeRunner()
	runner.plan = vmLANBridgePlan{NeedsPrepare: true, Bridge: "br0", Parent: "eno1", Renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true}}
	runner.netplan = []byte(eno1DHCPNetplan)
	runner.validateErr = errors.New("default route missing")

	status, err := prepareVMLANBridge(root, runner, vmLANBridgePrepareOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "default route missing") {
		t.Fatalf("error = %v, want validation failure", err)
	}
	if status.Phase != vmLANBridgePhaseRolledBack || !runner.rolledBack {
		t.Fatalf("status = %#v runner = %#v, want rolled back", status, runner)
	}
	if got, want := runner.calls, []string{"plan", "read", "backup", "overlay", "schedule-rollback", "generate", "apply", "validate", "rollback"}; !slices.Equal(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	if runner.rolledBackID != status.ID || runner.rollbackCanceled {
		t.Fatalf("rollback id/cancel = %q/%v, want rollback with status id %q and no cancel", runner.rolledBackID, runner.rollbackCanceled, status.ID)
	}
	fileStatus := readVMLANBridgePrepareStatus(t, root)
	if fileStatus.Phase != vmLANBridgePhaseRolledBack || !strings.Contains(fileStatus.Error, "default route missing") {
		t.Fatalf("status file = %#v, want rolled-back validation error", fileStatus)
	}
	if fileStatus.BackupPath == "" || fileStatus.OverlayPath != vmLANBridgeNetplanOverlay {
		t.Fatalf("status paths = %q/%q, want backup and overlay", fileStatus.BackupPath, fileStatus.OverlayPath)
	}
}

func TestPrepareVMLANBridgeRollsBackWhenApplyFails(t *testing.T) {
	root := t.TempDir()
	runner := newPreparedVMLANBridgeRunner()
	runner.applyErr = errors.New("apply failed")

	status, err := prepareVMLANBridge(root, runner, vmLANBridgePrepareOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "apply failed") {
		t.Fatalf("error = %v, want apply failure", err)
	}
	if status.Phase != vmLANBridgePhaseRolledBack || !runner.rolledBack {
		t.Fatalf("status = %#v runner = %#v, want rolled back", status, runner)
	}
	if got, want := runner.calls, []string{"plan", "read", "backup", "overlay", "schedule-rollback", "generate", "apply", "rollback"}; !slices.Equal(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	fileStatus := readVMLANBridgePrepareStatus(t, root)
	if fileStatus.Phase != vmLANBridgePhaseRolledBack || !strings.Contains(fileStatus.Error, "apply failed") {
		t.Fatalf("status file = %#v, want rolled-back apply error", fileStatus)
	}
}

func TestPrepareVMLANBridgeRollsBackWhenGenerateFailsAfterOverlay(t *testing.T) {
	root := t.TempDir()
	runner := newPreparedVMLANBridgeRunner()
	runner.generateErr = errors.New("generate failed")

	status, err := prepareVMLANBridge(root, runner, vmLANBridgePrepareOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "generate failed") {
		t.Fatalf("error = %v, want generate failure", err)
	}
	if status.Phase != vmLANBridgePhaseRolledBack || !runner.rolledBack {
		t.Fatalf("status = %#v runner = %#v, want rolled back", status, runner)
	}
	if got, want := runner.calls, []string{"plan", "read", "backup", "overlay", "schedule-rollback", "generate", "rollback"}; !slices.Equal(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	fileStatus := readVMLANBridgePrepareStatus(t, root)
	if fileStatus.Phase != vmLANBridgePhaseRolledBack || fileStatus.OverlayPath != vmLANBridgeNetplanOverlay {
		t.Fatalf("status file = %#v, want rolled-back with overlay path", fileStatus)
	}
}

func TestPrepareVMLANBridgeDoesNotApplyWhenScheduleRollbackFails(t *testing.T) {
	root := t.TempDir()
	runner := newPreparedVMLANBridgeRunner()
	runner.scheduleErr = errors.New("timer unavailable")

	status, err := prepareVMLANBridge(root, runner, vmLANBridgePrepareOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "timer unavailable") {
		t.Fatalf("error = %v, want schedule failure", err)
	}
	if status.Phase != vmLANBridgePhaseRolledBack || !runner.rolledBack || runner.applied {
		t.Fatalf("status = %#v rolledBack = %v applied = %v, want rolled back before apply", status, runner.rolledBack, runner.applied)
	}
	if got, want := runner.calls, []string{"plan", "read", "backup", "overlay", "schedule-rollback", "rollback"}; !slices.Equal(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	fileStatus := readVMLANBridgePrepareStatus(t, root)
	if fileStatus.Phase != vmLANBridgePhaseRolledBack || fileStatus.BackupPath == "" || fileStatus.OverlayPath != vmLANBridgeNetplanOverlay {
		t.Fatalf("status file = %#v, want rolled-back with backup and overlay paths", fileStatus)
	}
}

func TestPrepareVMLANBridgeFailsWhenScheduleRollbackAndRollbackFail(t *testing.T) {
	root := t.TempDir()
	runner := newPreparedVMLANBridgeRunner()
	runner.scheduleErr = errors.New("timer unavailable")
	runner.rollbackErr = errors.New("rollback unavailable")

	status, err := prepareVMLANBridge(root, runner, vmLANBridgePrepareOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "timer unavailable") || !strings.Contains(err.Error(), "rollback unavailable") {
		t.Fatalf("error = %v, want schedule and rollback failures", err)
	}
	if status.Phase != vmLANBridgePhaseFailed || !runner.rolledBack || runner.applied {
		t.Fatalf("status = %#v rolledBack = %v applied = %v, want failed rollback before apply", status, runner.rolledBack, runner.applied)
	}
	fileStatus := readVMLANBridgePrepareStatus(t, root)
	if fileStatus.Phase != vmLANBridgePhaseFailed || !strings.Contains(fileStatus.Error, "timer unavailable") || !strings.Contains(fileStatus.Error, "rollback unavailable") {
		t.Fatalf("status file = %#v, want schedule and rollback errors", fileStatus)
	}
}

func TestPrepareVMLANBridgeFailsWhenCancelRollbackFails(t *testing.T) {
	root := t.TempDir()
	runner := newPreparedVMLANBridgeRunner()
	runner.cancelErr = errors.New("cancel failed")

	status, err := prepareVMLANBridge(root, runner, vmLANBridgePrepareOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "cancel failed") {
		t.Fatalf("error = %v, want cancel failure", err)
	}
	if status.Phase != vmLANBridgePhaseFailed {
		t.Fatalf("status = %#v, want failed", status)
	}
	if got, want := runner.calls, []string{"plan", "read", "backup", "overlay", "schedule-rollback", "generate", "apply", "validate", "cancel-rollback"}; !slices.Equal(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	fileStatus := readVMLANBridgePrepareStatus(t, root)
	if fileStatus.Phase == vmLANBridgePhaseReady || !strings.Contains(fileStatus.Error, "cancel failed") {
		t.Fatalf("status file = %#v, want failed cancel error", fileStatus)
	}
}

func TestPrepareVMLANBridgeFailsWhenRollbackFails(t *testing.T) {
	root := t.TempDir()
	runner := newPreparedVMLANBridgeRunner()
	runner.applyErr = errors.New("apply failed")
	runner.rollbackErr = errors.New("rollback failed")

	status, err := prepareVMLANBridge(root, runner, vmLANBridgePrepareOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("error = %v, want rollback failure", err)
	}
	if status.Phase != vmLANBridgePhaseFailed {
		t.Fatalf("status = %#v, want failed", status)
	}
	fileStatus := readVMLANBridgePrepareStatus(t, root)
	if fileStatus.Phase != vmLANBridgePhaseFailed || !strings.Contains(fileStatus.Error, "rollback failed") || !strings.Contains(fileStatus.Error, "apply failed") {
		t.Fatalf("status file = %#v, want failed apply and rollback errors", fileStatus)
	}
}

func TestSystemVMLANBridgeRunnerReadNetplanSelectsParent(t *testing.T) {
	stubVMLANBridgeSystem(t, vmLANBridgeCommandFixtures{
		netplan: map[string][]byte{
			"/etc/netplan/10-other.yaml": []byte(`
network:
  version: 2
  renderer: networkd
  ethernets:
    enp2s0:
      dhcp4: true
`),
			"/etc/netplan/50-cloud-init.yaml": []byte(eno1DHCPNetplan),
		},
	})
	runner := &systemVMLANBridgePrepareRunner{}

	raw, err := runner.ReadNetplan("eno1")
	if err != nil {
		t.Fatalf("ReadNetplan: %v", err)
	}
	if string(raw) != eno1DHCPNetplan {
		t.Fatalf("netplan = %q, want eno1 source", raw)
	}
	if runner.netplanSourcePath != "/etc/netplan/50-cloud-init.yaml" {
		t.Fatalf("source path = %q, want parent-defining netplan", runner.netplanSourcePath)
	}
}

func TestSystemVMLANBridgeRunnerReadNetplanRejectsDuplicateParent(t *testing.T) {
	stubVMLANBridgeSystem(t, vmLANBridgeCommandFixtures{
		netplan: map[string][]byte{
			"/etc/netplan/50-cloud-init.yaml": []byte(eno1DHCPNetplan),
			"/run/netplan/90-runtime.yaml": []byte(`
network:
  version: 2
  renderer: networkd
  ethernets:
    eno1:
      dhcp4: true
`),
		},
	})
	runner := &systemVMLANBridgePrepareRunner{}

	_, err := runner.ReadNetplan("eno1")
	if err == nil || !strings.Contains(err.Error(), "multiple supported netplan configs define network.ethernets.eno1") {
		t.Fatalf("ReadNetplan error = %v, want duplicate parent rejection", err)
	}
}

func TestSystemVMLANBridgeRunnerReadNetplanRejectsUnsupportedParentOverlay(t *testing.T) {
	stubVMLANBridgeSystem(t, vmLANBridgeCommandFixtures{
		netplan: map[string][]byte{
			"/etc/netplan/50-cloud-init.yaml": []byte(eno1DHCPNetplan),
			"/etc/netplan/90-unsupported.yaml": []byte(`
network:
  version: 2
  renderer: networkd
  ethernets:
    eno1:
      gateway4: 192.168.50.1
`),
		},
	})
	runner := &systemVMLANBridgePrepareRunner{}

	_, err := runner.ReadNetplan("eno1")
	if err == nil || !strings.Contains(err.Error(), "unsupported netplan config defines network.ethernets.eno1") {
		t.Fatalf("ReadNetplan error = %v, want unsupported parent rejection", err)
	}
}

func TestSystemVMLANBridgeRunnerValidateRetriesUntilReady(t *testing.T) {
	oldSleep := vmLANBridgeSleepFn
	t.Cleanup(func() {
		vmLANBridgeSleepFn = oldSleep
	})
	sleepCount := 0
	vmLANBridgeSleepFn = func(time.Duration) {
		sleepCount++
	}
	plans := []vmLANBridgePlan{
		{NeedsPrepare: true, Bridge: "br0", Parent: "eno1"},
		{Ready: true, Bridge: "br0", Parent: "eno1"},
	}
	runner := &systemVMLANBridgePrepareRunner{
		planFn: func() (vmLANBridgePlan, error) {
			plan := plans[0]
			plans = plans[1:]
			return plan, nil
		},
	}

	if err := runner.Validate("br0", "eno1"); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if sleepCount != 1 {
		t.Fatalf("sleepCount = %d, want one retry delay", sleepCount)
	}
	if len(plans) != 0 {
		t.Fatalf("remaining plans = %#v, want consumed", plans)
	}
}

func TestSystemVMLANBridgeRunnerValidateReportsLastError(t *testing.T) {
	oldSleep := vmLANBridgeSleepFn
	t.Cleanup(func() {
		vmLANBridgeSleepFn = oldSleep
	})
	sleepCount := 0
	vmLANBridgeSleepFn = func(time.Duration) {
		sleepCount++
	}
	planErr := errors.New("route missing")
	calls := 0
	runner := &systemVMLANBridgePrepareRunner{
		planFn: func() (vmLANBridgePlan, error) {
			calls++
			return vmLANBridgePlan{}, planErr
		},
	}

	err := runner.Validate("br0", "eno1")
	if err == nil || !strings.Contains(err.Error(), "route missing") {
		t.Fatalf("Validate error = %v, want last plan error", err)
	}
	if calls != vmLANBridgeValidationRetries {
		t.Fatalf("calls = %d, want %d", calls, vmLANBridgeValidationRetries)
	}
	if sleepCount != vmLANBridgeValidationRetries-1 {
		t.Fatalf("sleepCount = %d, want %d", sleepCount, vmLANBridgeValidationRetries-1)
	}
}

func TestSystemVMLANBridgeRunnerValidateRejectsReadyBridgeWithoutExpectedParent(t *testing.T) {
	stubVMLANBridgeSystem(t, vmLANBridgeCommandFixtures{
		link: []byte(`[
			{"ifname":"br0","operstate":"UP","link_type":"ether","linkinfo":{"info_kind":"bridge"}},
			{"ifname":"eno1","operstate":"UP","address":"52:54:00:12:34:56","link_type":"ether"},
			{"ifname":"eno2","operstate":"UP","address":"52:54:00:65:43:21","link_type":"ether","master":"br0"}
		]`),
	})
	oldSleep := vmLANBridgeSleepFn
	t.Cleanup(func() {
		vmLANBridgeSleepFn = oldSleep
	})
	vmLANBridgeSleepFn = func(time.Duration) {}
	runner := &systemVMLANBridgePrepareRunner{
		planFn: func() (vmLANBridgePlan, error) {
			return vmLANBridgePlan{Ready: true, Bridge: "br0"}, nil
		},
	}

	err := runner.Validate("br0", "eno1")
	if err == nil || !strings.Contains(err.Error(), `parent "eno1" is not attached to bridge "br0"`) {
		t.Fatalf("Validate error = %v, want missing parent topology", err)
	}
}

func TestSystemVMLANBridgeRunnerValidateAcceptsParentMasterTopology(t *testing.T) {
	stubVMLANBridgeSystem(t, vmLANBridgeCommandFixtures{
		link: []byte(`[
			{"ifname":"br0","operstate":"UP","link_type":"ether","linkinfo":{"info_kind":"bridge"}},
			{"ifname":"eno1","operstate":"UP","address":"52:54:00:12:34:56","link_type":"ether","master":"br0"}
		]`),
	})
	runner := &systemVMLANBridgePrepareRunner{
		planFn: func() (vmLANBridgePlan, error) {
			return vmLANBridgePlan{Ready: true, Bridge: "br0"}, nil
		},
	}

	if err := runner.Validate("br0", "eno1"); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestSystemVMLANBridgeRunnerCommands(t *testing.T) {
	oldRun := vmLANBridgeRunCommandFn
	oldRead := vmLANBridgeReadFileFn
	oldRemove := vmLANBridgeRemoveFileFn
	t.Cleanup(func() {
		vmLANBridgeRunCommandFn = oldRun
		vmLANBridgeReadFileFn = oldRead
		vmLANBridgeRemoveFileFn = oldRemove
	})
	commands := []string{}
	removed := ""
	vmLANBridgeRunCommandFn = func(name string, args ...string) error {
		commands = append(commands, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	vmLANBridgeReadFileFn = func(path string) ([]byte, error) {
		if path != vmLANBridgeNetplanOverlay {
			return nil, os.ErrNotExist
		}
		return []byte("# Managed by yeet VM LAN bridge preparation; do not edit.\nnetwork:\n  version: 2\n"), nil
	}
	vmLANBridgeRemoveFileFn = func(path string) error {
		removed = path
		return nil
	}
	runner := &systemVMLANBridgePrepareRunner{}
	id := "vm-lan-bridge-20260628T010203.123Z"

	if err := runner.Generate(); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := runner.ScheduleRollback(id, vmLANBridgeRollbackDelay); err != nil {
		t.Fatalf("ScheduleRollback: %v", err)
	}
	if err := runner.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := runner.CancelRollback(id); err != nil {
		t.Fatalf("CancelRollback: %v", err)
	}
	if err := runner.Rollback(id); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	unit := "yeet-vm-lan-bridge-20260628T010203-123Z-rollback"
	want := []string{
		"netplan generate",
		"systemd-run --unit " + unit + " --on-active=120s --collect /bin/sh -c p=/etc/netplan/99-yeet-vm-lan-bridge.yaml; marker='# Managed by yeet VM LAN bridge preparation; do not edit.'; if [ ! -e \"$p\" ] || { IFS= read -r first < \"$p\" && [ \"$first\" = \"$marker\" ]; }; then rm -f \"$p\" && netplan apply; else echo 'refusing to remove unmanaged VM LAN bridge netplan overlay' >&2; exit 1; fi",
		"netplan apply",
		"systemctl stop " + unit + ".timer " + unit + ".service",
		"netplan apply",
		"systemctl stop " + unit + ".timer " + unit + ".service",
	}
	if !slices.Equal(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
	if removed != vmLANBridgeNetplanOverlay {
		t.Fatalf("removed = %q, want overlay", removed)
	}
}

func TestSystemVMLANBridgeRunnerWriteNetplanOverlayRefusesUnmanagedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "99-yeet-vm-lan-bridge.yaml")
	if err := os.WriteFile(path, []byte("network:\n  version: 2\n"), 0644); err != nil {
		t.Fatalf("write existing overlay: %v", err)
	}
	runner := &systemVMLANBridgePrepareRunner{}

	err := runner.WriteNetplanOverlay(path, []byte("# Managed by yeet VM LAN bridge preparation; do not edit.\nnetwork:\n  version: 2\n"))
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite unmanaged VM LAN bridge netplan overlay") {
		t.Fatalf("WriteNetplanOverlay error = %v, want unmanaged refusal", err)
	}
}

func TestSystemVMLANBridgeRunnerWriteNetplanOverlayRefusesBuriedMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "99-yeet-vm-lan-bridge.yaml")
	oldContent := []byte("network:\n  version: 2\n# Managed by yeet VM LAN bridge preparation; do not edit.\n")
	if err := os.WriteFile(path, oldContent, 0644); err != nil {
		t.Fatalf("write existing overlay: %v", err)
	}
	runner := &systemVMLANBridgePrepareRunner{}

	err := runner.WriteNetplanOverlay(path, []byte("# Managed by yeet VM LAN bridge preparation; do not edit.\nnetwork:\n  version: 2\n"))
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite unmanaged VM LAN bridge netplan overlay") {
		t.Fatalf("WriteNetplanOverlay error = %v, want unmanaged refusal", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read overlay: %v", err)
	}
	if string(raw) != string(oldContent) {
		t.Fatalf("overlay content changed to %q, want original %q", raw, oldContent)
	}
}

func TestSystemVMLANBridgeRunnerWriteNetplanOverlayReplacesManagedFileWith0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "99-yeet-vm-lan-bridge.yaml")
	oldContent := []byte("# Managed by yeet VM LAN bridge preparation; do not edit.\nnetwork:\n  version: 2\n")
	if err := os.WriteFile(path, oldContent, 0644); err != nil {
		t.Fatalf("write existing overlay: %v", err)
	}
	newContent := []byte("# Managed by yeet VM LAN bridge preparation; do not edit.\nnetwork:\n  version: 2\n  bridges: {}\n")
	runner := &systemVMLANBridgePrepareRunner{}

	if err := runner.WriteNetplanOverlay(path, newContent); err != nil {
		t.Fatalf("WriteNetplanOverlay: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read overlay: %v", err)
	}
	if string(raw) != string(newContent) {
		t.Fatalf("overlay content = %q, want %q", raw, newContent)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat overlay: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("overlay mode = %v, want 0600", got)
	}
}

func TestSystemVMLANBridgeRunnerRollbackRefusesUnmanagedOverlay(t *testing.T) {
	oldRun := vmLANBridgeRunCommandFn
	oldRead := vmLANBridgeReadFileFn
	oldRemove := vmLANBridgeRemoveFileFn
	t.Cleanup(func() {
		vmLANBridgeRunCommandFn = oldRun
		vmLANBridgeReadFileFn = oldRead
		vmLANBridgeRemoveFileFn = oldRemove
	})
	removed := false
	vmLANBridgeReadFileFn = func(path string) ([]byte, error) {
		if path != vmLANBridgeNetplanOverlay {
			return nil, os.ErrNotExist
		}
		return []byte("network:\n  version: 2\n"), nil
	}
	vmLANBridgeRemoveFileFn = func(path string) error {
		removed = true
		return nil
	}
	vmLANBridgeRunCommandFn = func(name string, args ...string) error {
		t.Fatalf("unexpected command %s %v", name, args)
		return nil
	}
	runner := &systemVMLANBridgePrepareRunner{}

	err := runner.Rollback("vm-lan-bridge-test")
	if err == nil || !strings.Contains(err.Error(), "refusing to remove unmanaged VM LAN bridge netplan overlay") {
		t.Fatalf("Rollback error = %v, want unmanaged refusal", err)
	}
	if removed {
		t.Fatalf("rollback removed unmanaged overlay")
	}
}

func TestSystemVMLANBridgeRunnerRollbackRefusesBuriedMarker(t *testing.T) {
	oldRun := vmLANBridgeRunCommandFn
	oldRead := vmLANBridgeReadFileFn
	oldRemove := vmLANBridgeRemoveFileFn
	t.Cleanup(func() {
		vmLANBridgeRunCommandFn = oldRun
		vmLANBridgeReadFileFn = oldRead
		vmLANBridgeRemoveFileFn = oldRemove
	})
	removed := false
	vmLANBridgeReadFileFn = func(path string) ([]byte, error) {
		if path != vmLANBridgeNetplanOverlay {
			return nil, os.ErrNotExist
		}
		return []byte("network:\n  version: 2\n# Managed by yeet VM LAN bridge preparation; do not edit.\n"), nil
	}
	vmLANBridgeRemoveFileFn = func(path string) error {
		removed = true
		return nil
	}
	vmLANBridgeRunCommandFn = func(name string, args ...string) error {
		t.Fatalf("unexpected command %s %v", name, args)
		return nil
	}
	runner := &systemVMLANBridgePrepareRunner{}

	err := runner.Rollback("vm-lan-bridge-test")
	if err == nil || !strings.Contains(err.Error(), "refusing to remove unmanaged VM LAN bridge netplan overlay") {
		t.Fatalf("Rollback error = %v, want unmanaged refusal", err)
	}
	if removed {
		t.Fatalf("rollback removed buried-marker overlay")
	}
}

func TestSystemVMLANBridgeRunnerRollbackRemovesManagedOverlay(t *testing.T) {
	oldRun := vmLANBridgeRunCommandFn
	oldRead := vmLANBridgeReadFileFn
	oldRemove := vmLANBridgeRemoveFileFn
	t.Cleanup(func() {
		vmLANBridgeRunCommandFn = oldRun
		vmLANBridgeReadFileFn = oldRead
		vmLANBridgeRemoveFileFn = oldRemove
	})
	commands := []string{}
	removed := false
	vmLANBridgeReadFileFn = func(path string) ([]byte, error) {
		if path != vmLANBridgeNetplanOverlay {
			return nil, os.ErrNotExist
		}
		return []byte("# Managed by yeet VM LAN bridge preparation; do not edit.\nnetwork:\n  version: 2\n"), nil
	}
	vmLANBridgeRemoveFileFn = func(path string) error {
		if path != vmLANBridgeNetplanOverlay {
			t.Fatalf("removed path = %q, want overlay", path)
		}
		removed = true
		return nil
	}
	vmLANBridgeRunCommandFn = func(name string, args ...string) error {
		commands = append(commands, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	runner := &systemVMLANBridgePrepareRunner{}

	if err := runner.Rollback("vm-lan-bridge-test"); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if !removed {
		t.Fatalf("rollback did not remove managed overlay")
	}
	unit := "yeet-vm-lan-bridge-test-rollback"
	want := []string{
		"netplan apply",
		"systemctl stop " + unit + ".timer " + unit + ".service",
	}
	if !slices.Equal(commands, want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

type fakeVMLANBridgeRunner struct {
	plan              vmLANBridgePlan
	netplan           []byte
	validateErr       error
	generateErr       error
	applyErr          error
	scheduleErr       error
	cancelErr         error
	rollbackErr       error
	calls             []string
	readParent        string
	validatedBridge   string
	validatedParent   string
	scheduledID       string
	canceledID        string
	rolledBackID      string
	rollbackDelay     time.Duration
	backupPath        string
	backupContent     []byte
	overlayPath       string
	overlayContent    []byte
	generated         bool
	applied           bool
	scheduledRollback bool
	rollbackCanceled  bool
	rolledBack        bool
}

func newFakeVMLANBridgeRunner() *fakeVMLANBridgeRunner {
	return &fakeVMLANBridgeRunner{}
}

func newPreparedVMLANBridgeRunner() *fakeVMLANBridgeRunner {
	runner := newFakeVMLANBridgeRunner()
	runner.plan = vmLANBridgePlan{NeedsPrepare: true, Bridge: "br0", Parent: "eno1", Renderer: vmLANRenderer{Name: "netplan-networkd", Supported: true}}
	runner.netplan = []byte(eno1DHCPNetplan)
	return runner
}

func (r *fakeVMLANBridgeRunner) Plan() (vmLANBridgePlan, error) {
	r.calls = append(r.calls, "plan")
	return r.plan, nil
}

func (r *fakeVMLANBridgeRunner) ReadNetplan(parent string) ([]byte, error) {
	r.calls = append(r.calls, "read")
	r.readParent = parent
	return append([]byte(nil), r.netplan...), nil
}

func (r *fakeVMLANBridgeRunner) WriteNetplanBackup(path string, content []byte) error {
	r.calls = append(r.calls, "backup")
	r.backupPath = path
	r.backupContent = append([]byte(nil), content...)
	return nil
}

func (r *fakeVMLANBridgeRunner) WriteNetplanOverlay(path string, content []byte) error {
	r.calls = append(r.calls, "overlay")
	r.overlayPath = path
	r.overlayContent = append([]byte(nil), content...)
	return nil
}

func (r *fakeVMLANBridgeRunner) Generate() error {
	r.calls = append(r.calls, "generate")
	r.generated = true
	return r.generateErr
}

func (r *fakeVMLANBridgeRunner) Apply() error {
	r.calls = append(r.calls, "apply")
	r.applied = true
	return r.applyErr
}

func (r *fakeVMLANBridgeRunner) Validate(bridge, parent string) error {
	r.calls = append(r.calls, "validate")
	r.validatedBridge = bridge
	r.validatedParent = parent
	return r.validateErr
}

func (r *fakeVMLANBridgeRunner) ScheduleRollback(id string, after time.Duration) error {
	r.calls = append(r.calls, "schedule-rollback")
	r.scheduledID = id
	r.rollbackDelay = after
	r.scheduledRollback = true
	return r.scheduleErr
}

func (r *fakeVMLANBridgeRunner) CancelRollback(id string) error {
	r.calls = append(r.calls, "cancel-rollback")
	r.canceledID = id
	r.rollbackCanceled = true
	return r.cancelErr
}

func (r *fakeVMLANBridgeRunner) Rollback(id string) error {
	r.calls = append(r.calls, "rollback")
	r.rolledBackID = id
	r.rolledBack = true
	return r.rollbackErr
}

func readVMLANBridgePrepareStatus(t *testing.T, root string) vmLANBridgePrepareStatus {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "host-network", "vm-lan-bridge", "status.json"))
	if err != nil {
		t.Fatalf("read status file: %v", err)
	}
	var status vmLANBridgePrepareStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatalf("decode status file: %v", err)
	}
	return status
}
