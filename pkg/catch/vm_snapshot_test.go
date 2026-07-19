// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"golang.org/x/sys/unix"
)

func TestVMSnapshotRejectsRawDisk(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendRaw)

	err := server.createVMSnapshot(context.Background(), "devbox", cli.SnapshotsCreateFlags{}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "requires a ZFS zvol-backed VM") {
		t.Fatalf("createVMSnapshot error = %v, want zvol-backed rejection", err)
	}
}

func TestVMSnapshotDiskOnlyFlushesPausedDiskBeforeZFSSnapshot(t *testing.T) {
	server := newTestServer(t)
	expectVMCheckpointDelegation(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	var calls []string
	server.zfsRunner = recordingVMSnapshotZFSRunner(&calls, nil)
	oldRunning := vmSnapshotIsRunning
	oldController := vmSnapshotFirecracker
	controller := &recordingVMFirecracker{calls: &calls}
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return true, nil }
	vmSnapshotFirecracker = controller
	t.Cleanup(func() {
		vmSnapshotIsRunning = oldRunning
		vmSnapshotFirecracker = oldController
	})

	err := server.createVMSnapshot(context.Background(), "devbox", cli.SnapshotsCreateFlags{Comment: " before upgrade "}, io.Discard)
	if err != nil {
		t.Fatalf("createVMSnapshot: %v", err)
	}

	assertCallOrder(t, calls, "pause ", "full "+filepath.Join(serviceRunDirForRoot(root), "firecracker.sock"), "zfs snapshot", "resume ")
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "com.yeetrun:comment=before upgrade") {
		t.Fatalf("calls = %#v, want trimmed comment metadata", calls)
	}
	if !strings.Contains(joined, "com.yeetrun:checkpoint=disk") {
		t.Fatalf("calls = %#v, want disk checkpoint metadata", calls)
	}
	if !strings.Contains(joined, "flash/yeet/vms/devbox/vm/d-abc/root@yeet-") {
		t.Fatalf("calls = %#v, want zvol dataset snapshot", calls)
	}
	snapshotCall := vmSnapshotZFSCall(calls, "snapshot")
	if strings.Contains(snapshotCall, "-g0") || strings.Contains(snapshotCall, "com.yeetrun:generation=") {
		t.Fatalf("snapshot call = %q, want VM snapshot without generation suffix or property", snapshotCall)
	}
	if controller.fullStatePath == "" || controller.fullMemPath == "" {
		t.Fatalf("full snapshot paths empty: state=%q mem=%q", controller.fullStatePath, controller.fullMemPath)
	}
}

func TestVMSnapshotAttemptsResumeAfterSnapshotFailure(t *testing.T) {
	server := newTestServer(t)
	expectVMCheckpointDelegation(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	var calls []string
	server.zfsRunner = recordingVMSnapshotZFSRunner(&calls, errVMSnapshotTest)
	oldRunning := vmSnapshotIsRunning
	oldController := vmSnapshotFirecracker
	controller := &recordingVMFirecracker{calls: &calls}
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return true, nil }
	vmSnapshotFirecracker = controller
	t.Cleanup(func() {
		vmSnapshotIsRunning = oldRunning
		vmSnapshotFirecracker = oldController
	})

	err := server.createVMSnapshot(context.Background(), "devbox", cli.SnapshotsCreateFlags{}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "zfs snapshot") {
		t.Fatalf("createVMSnapshot error = %v, want zfs snapshot error", err)
	}
	assertCallOrder(t, calls, "pause ", "zfs snapshot", "resume ")
}

func TestVMSnapshotResumeIgnoresCanceledOperationContext(t *testing.T) {
	server := newTestServer(t)
	expectVMCheckpointDelegation(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		calls = append(calls, "zfs "+strings.Join(args, " "))
		if len(args) > 0 && args[0] == "snapshot" {
			cancel()
			return "", "zfs snapshot failed", errVMSnapshotTest
		}
		return "", "", nil
	}
	oldRunning := vmSnapshotIsRunning
	oldController := vmSnapshotFirecracker
	controller := &recordingVMFirecracker{calls: &calls}
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return true, nil }
	vmSnapshotFirecracker = controller
	t.Cleanup(func() {
		vmSnapshotIsRunning = oldRunning
		vmSnapshotFirecracker = oldController
	})

	err := server.createVMSnapshot(ctx, "devbox", cli.SnapshotsCreateFlags{}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "zfs snapshot") {
		t.Fatalf("createVMSnapshot error = %v, want zfs snapshot error", err)
	}
	if controller.resumeContextErr != nil {
		t.Fatalf("resume context error = %v, want independent non-canceled resume context", controller.resumeContextErr)
	}
	assertCallOrder(t, calls, "pause ", "zfs snapshot", "resume ")
}

func TestVMSnapshotDiskOnlySnapshotsStoppedVMWithoutPause(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	var calls []string
	server.zfsRunner = recordingVMSnapshotZFSRunner(&calls, nil)
	oldRunning := vmSnapshotIsRunning
	oldController := vmSnapshotFirecracker
	controller := &recordingVMFirecracker{calls: &calls}
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return false, nil }
	vmSnapshotFirecracker = controller
	t.Cleanup(func() {
		vmSnapshotIsRunning = oldRunning
		vmSnapshotFirecracker = oldController
	})
	var out bytes.Buffer

	err := server.createVMSnapshot(context.Background(), "devbox", cli.SnapshotsCreateFlags{}, &out)
	if err != nil {
		t.Fatalf("createVMSnapshot: %v", err)
	}

	joined := strings.Join(calls, "\n")
	if strings.Contains(joined, "pause ") || strings.Contains(joined, "resume ") {
		t.Fatalf("calls = %#v, want no pause/resume for stopped VM", calls)
	}
	if !strings.Contains(joined, "zfs snapshot") {
		t.Fatalf("calls = %#v, want zfs snapshot", calls)
	}
	if !strings.Contains(out.String(), "VM snapshot: flash/yeet/vms/devbox/vm/d-abc/root@yeet-") {
		t.Fatalf("output = %q, want snapshot line", out.String())
	}
}

func TestVMSnapshotPrunesCheckpointDirsForDestroyedSnapshots(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, svc *db.Service) error {
		svc.SnapshotPolicy = &db.SnapshotPolicy{KeepLast: intPointer(1)}
		return nil
	}); err != nil {
		t.Fatalf("set snapshot policy: %v", err)
	}
	oldName := "flash/yeet/vms/devbox/vm/d-abc/root@yeet-20200101T000000Z-vm-manual-g0"
	oldCheckpointDir := vmCheckpointDir(root, vmSnapshotShortName(oldName))
	if err := os.MkdirAll(oldCheckpointDir, 0o755); err != nil {
		t.Fatalf("mkdir old checkpoint dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldCheckpointDir, "metadata.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write old checkpoint metadata: %v", err)
	}
	var currentName string
	var destroyed []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		switch args[0] {
		case "snapshot":
			currentName = args[len(args)-1]
		case "list":
			if currentName == "" {
				t.Fatal("zfs list called before snapshot")
			}
			now := time.Now().Unix()
			return fmt.Sprintf("%s\t%d\tcatch\tdevbox\n%s\t%d\tcatch\tdevbox\n", oldName, now-86400, currentName, now), "", nil
		case "destroy":
			destroyed = append(destroyed, args[1])
		}
		return "", "", nil
	}
	oldRunning := vmSnapshotIsRunning
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return false, nil }
	t.Cleanup(func() { vmSnapshotIsRunning = oldRunning })

	if err := server.createVMSnapshot(context.Background(), "devbox", cli.SnapshotsCreateFlags{}, io.Discard); err != nil {
		t.Fatalf("createVMSnapshot: %v", err)
	}

	if len(destroyed) != 1 || destroyed[0] != oldName {
		t.Fatalf("destroyed = %#v, want old snapshot", destroyed)
	}
	if _, err := os.Stat(oldCheckpointDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old checkpoint dir stat = %v, want removed", err)
	}
}

func TestVMSnapshotPrunesCheckpointDirsAfterPartialZFSPruneFailure(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, svc *db.Service) error {
		svc.SnapshotPolicy = &db.SnapshotPolicy{KeepLast: intPointer(1)}
		return nil
	}); err != nil {
		t.Fatalf("set snapshot policy: %v", err)
	}
	oldNames := []string{
		"flash/yeet/vms/devbox/vm/d-abc/root@yeet-20200101T000000Z-vm-manual-g0",
		"flash/yeet/vms/devbox/vm/d-abc/root@yeet-20200102T000000Z-vm-manual-g0",
	}
	oldDirs := make([]string, 0, len(oldNames))
	for _, name := range oldNames {
		dir := vmCheckpointDir(root, vmSnapshotShortName(name))
		oldDirs = append(oldDirs, dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir old checkpoint dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write old checkpoint metadata: %v", err)
		}
	}
	var currentName string
	var destroyed []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		switch args[0] {
		case "snapshot":
			currentName = args[len(args)-1]
		case "list":
			if currentName == "" {
				t.Fatal("zfs list called before snapshot")
			}
			now := time.Now().Unix()
			return fmt.Sprintf("%s\t%d\tcatch\tdevbox\n%s\t%d\tcatch\tdevbox\n%s\t%d\tcatch\tdevbox\n", oldNames[0], now-86400, oldNames[1], now-43200, currentName, now), "", nil
		case "destroy":
			destroyed = append(destroyed, args[1])
			if args[1] == oldNames[1] {
				return "", "cannot destroy", errVMSnapshotTest
			}
		}
		return "", "", nil
	}
	oldRunning := vmSnapshotIsRunning
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return false, nil }
	t.Cleanup(func() { vmSnapshotIsRunning = oldRunning })
	var out bytes.Buffer

	if err := server.createVMSnapshot(context.Background(), "devbox", cli.SnapshotsCreateFlags{}, &out); err != nil {
		t.Fatalf("createVMSnapshot: %v", err)
	}

	if len(destroyed) != 2 || destroyed[0] != oldNames[0] || destroyed[1] != oldNames[1] {
		t.Fatalf("destroyed = %#v, want both old snapshots attempted", destroyed)
	}
	if _, err := os.Stat(oldDirs[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first old checkpoint dir stat = %v, want removed", err)
	}
	if _, err := os.Stat(oldDirs[1]); err != nil {
		t.Fatalf("second old checkpoint dir stat = %v, want kept after failed zfs destroy", err)
	}
	if !strings.Contains(out.String(), "warning: failed to prune VM snapshots") {
		t.Fatalf("output = %q, want zfs prune warning", out.String())
	}
}

func TestVMSnapshotFullCreatesCheckpointAndMetadata(t *testing.T) {
	server := newTestServer(t)
	expectVMCheckpointDelegation(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	firecrackerBinary := filepath.Join(root, "firecracker")
	const firecrackerVersion = "Firecracker v1.7.0-test"
	firecrackerBytes := []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo " + strconv.Quote(firecrackerVersion) + "; exit 0; fi\nexit 1\n")
	if err := os.WriteFile(firecrackerBinary, firecrackerBytes, 0o755); err != nil {
		t.Fatalf("write firecracker binary: %v", err)
	}
	systemdDir := t.TempDir()
	oldSystemdDir := vmSystemdSystemDir
	vmSystemdSystemDir = systemdDir
	t.Cleanup(func() { vmSystemdSystemDir = oldSystemdDir })
	unit, err := renderVMSystemdUnit(vmSystemdConfig{
		Service:          "devbox",
		Runner:           "/srv/catch/run/catch",
		DataDir:          "/srv/catch/data",
		ServicesRoot:     "/srv/services",
		ServiceRoot:      root,
		DiskPath:         filepath.Join(serviceDataDirForRoot(root), "rootfs.raw"),
		Firecracker:      firecrackerBinary,
		Jailer:           filepath.Join(root, "jailer"),
		JailerBase:       filepath.Join(root, "jails"),
		ConfigPath:       filepath.Join(serviceRunDirForRoot(root), "firecracker.json"),
		APISocket:        filepath.Join(serviceRunDirForRoot(root), "firecracker.sock"),
		ConsoleSocket:    filepath.Join(serviceRunDirForRoot(root), "serial.sock"),
		WorkingDirectory: root,
	})
	if err != nil {
		t.Fatalf("render VM systemd unit: %v", err)
	}
	assertJailerOnlyVMUnit(t, unit)
	if err := os.WriteFile(filepath.Join(systemdDir, vmSystemdUnitName("devbox")), []byte(unit), 0o644); err != nil {
		t.Fatalf("write VM systemd unit: %v", err)
	}
	firecrackerSum := sha256.Sum256(firecrackerBytes)
	wantFirecrackerSHA := "sha256:" + hex.EncodeToString(firecrackerSum[:])
	var calls []string
	server.zfsRunner = recordingVMSnapshotZFSRunner(&calls, nil)
	oldRunning := vmSnapshotIsRunning
	oldController := vmSnapshotFirecracker
	controller := &recordingVMFirecracker{calls: &calls}
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return true, nil }
	vmSnapshotFirecracker = controller
	t.Cleanup(func() {
		vmSnapshotIsRunning = oldRunning
		vmSnapshotFirecracker = oldController
	})
	var out bytes.Buffer

	err = server.createVMSnapshot(context.Background(), "devbox", cli.SnapshotsCreateFlags{Full: true, Comment: "checkpoint"}, &out)
	if err != nil {
		t.Fatalf("createVMSnapshot: %v", err)
	}

	assertCallOrder(t, calls, "pause ", "full "+filepath.Join(serviceRunDirForRoot(root), "firecracker.sock"), "zfs snapshot", "resume ")
	if controller.fullStatePath == "" || controller.fullMemPath == "" {
		t.Fatalf("full snapshot paths empty: state=%q mem=%q", controller.fullStatePath, controller.fullMemPath)
	}
	if !strings.Contains(controller.fullStatePath, filepath.Join(root, "data", "checkpoints")) {
		t.Fatalf("state path = %q, want service data checkpoint path", controller.fullStatePath)
	}
	metadataPaths, err := filepath.Glob(filepath.Join(root, "data", "checkpoints", "yeet-*", "metadata.json"))
	if err != nil {
		t.Fatalf("glob checkpoint metadata: %v", err)
	}
	if len(metadataPaths) != 1 {
		t.Fatalf("checkpoint metadata paths = %#v, want one published checkpoint", metadataPaths)
	}
	checkpointDir := filepath.Dir(metadataPaths[0])
	statePath := filepath.Join(checkpointDir, "firecracker-state.bin")
	memoryPath := filepath.Join(checkpointDir, "memory.bin")
	raw, err := os.ReadFile(filepath.Join(checkpointDir, "metadata.json"))
	if err != nil {
		t.Fatalf("read checkpoint metadata: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatalf("decode checkpoint metadata: %v", err)
	}
	if metadata["service"] != "devbox" || metadata["comment"] != "checkpoint" || metadata["createdBy"] != "catch" {
		t.Fatalf("metadata = %#v, want service/comment/createdBy", metadata)
	}
	zvolSnapshot, ok := metadata["zvolSnapshot"].(string)
	if !ok || !strings.Contains(zvolSnapshot, "flash/yeet/vms/devbox/vm/d-abc/root@yeet-") {
		t.Fatalf("zvolSnapshot = %q, want zvol snapshot", metadata["zvolSnapshot"])
	}
	if metadata["firecrackerState"] != statePath || metadata["firecrackerMemory"] != memoryPath {
		t.Fatalf("metadata paths = %#v, want %q %q", metadata, statePath, memoryPath)
	}
	for _, key := range []string{
		"mode",
		"machineConfigHash",
		"networkConfigHash",
		"balloonConfigHash",
		"diskPath",
		"vcpu",
		"memoryMiB",
		"vmConfigHash",
	} {
		if _, ok := metadata[key]; !ok {
			t.Fatalf("metadata = %#v, missing compatibility field %q", metadata, key)
		}
	}
	if metadata["mode"] != recoveryModeFull {
		t.Fatalf("metadata mode = %q, want full", metadata["mode"])
	}
	for _, key := range []string{"machineConfigHash", "networkConfigHash", "balloonConfigHash", "vmConfigHash"} {
		hash, ok := metadata[key].(string)
		if !ok || !strings.HasPrefix(hash, "sha256:") {
			t.Fatalf("metadata[%s] = %q, want sha256-prefixed hash", key, metadata[key])
		}
	}
	if metadata["diskPath"] != "/dev/zvol/flash/yeet/vms/devbox/vm/d-abc/root" ||
		metadata["vcpu"] != float64(4) ||
		metadata["memoryMiB"] != float64(4096) {
		t.Fatalf("metadata compatibility = %#v, want disk/vcpu/memory from current VM config", metadata)
	}
	if metadata["firecrackerSha256"] != wantFirecrackerSHA {
		t.Fatalf("metadata firecrackerSha256 = %q, want %q", metadata["firecrackerSha256"], wantFirecrackerSHA)
	}
	if metadata["firecrackerVersion"] != firecrackerVersion {
		t.Fatalf("metadata firecrackerVersion = %q, want %q", metadata["firecrackerVersion"], firecrackerVersion)
	}
	if !strings.Contains(out.String(), "Firecracker state: "+statePath) ||
		!strings.Contains(out.String(), "Firecracker memory: "+memoryPath) {
		t.Fatalf("output = %q, want checkpoint paths", out.String())
	}
}

func TestVMSnapshotFullRejectsReplacedTemporaryDirectory(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "custom", "services", "devbox")
	service := &db.Service{Name: "devbox", ServiceType: db.ServiceTypeVM, ServiceRoot: root}
	vm := db.VMConfig{Sockets: db.VMSocketConfig{APISocketPath: filepath.Join(serviceRunDirForRoot(root), "firecracker.sock")}}
	withCurrentVMCheckpointIdentity(t)

	var originalDir string
	controller := replacingVMCheckpointController{
		replace: func(dir string) error {
			originalDir = dir + ".original"
			if err := os.Rename(dir, originalDir); err != nil {
				return err
			}
			if err := os.Mkdir(dir, 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "attacker-marker"), []byte("replacement"), 0o600)
		},
	}
	var calls []string
	server.zfsRunner = recordingVMSnapshotZFSRunner(&calls, nil)
	now := time.Date(2026, 7, 19, 12, 34, 56, 0, time.UTC)

	result, err := server.createPausedFullVMSnapshot(
		context.Background(), service, vm, "tank/vms/devbox", cli.SnapshotsCreateFlags{Full: true},
		controller, vmCheckpointCompatibility{}, now, "full",
	)

	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Errorf("createPausedFullVMSnapshot error = %v, want temporary directory replacement rejection", err)
	}
	if result.Name != "" {
		t.Errorf("result name = %q, want unpublished checkpoint", result.Name)
	}
	finalDir := vmCheckpointDir(root, "yeet-20260719T123456Z-vm-manual")
	if _, statErr := os.Lstat(finalDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("published checkpoint stat = %v, want no publication", statErr)
	}
	if originalDir == "" {
		t.Fatal("controller did not replace the temporary directory")
	}
	if got := vmSnapshotZFSCall(calls, "destroy"); got != "zfs destroy tank/vms/devbox@yeet-20260719T123456Z-vm-manual" {
		t.Errorf("destroy call = %q, want rollback of incomplete ZFS snapshot", got)
	}
}

func TestVMSnapshotFullRejectsReplacedMetadataWithoutTouchingOutsideFile(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "custom", "services", "devbox")
	service := &db.Service{Name: "devbox", ServiceType: db.ServiceTypeVM, ServiceRoot: root}
	vm := db.VMConfig{Sockets: db.VMSocketConfig{APISocketPath: filepath.Join(serviceRunDirForRoot(root), "firecracker.sock")}}
	withCurrentVMCheckpointIdentity(t)

	outside := filepath.Join(t.TempDir(), "outside-metadata.json")
	wantOutside := []byte("outside must remain unchanged\n")
	if err := os.WriteFile(outside, wantOutside, 0o600); err != nil {
		t.Fatal(err)
	}
	controller := replacingVMCheckpointController{
		replace: func(dir string) error {
			return os.Symlink(outside, filepath.Join(dir, "metadata.json"))
		},
	}
	var calls []string
	server.zfsRunner = recordingVMSnapshotZFSRunner(&calls, nil)
	now := time.Date(2026, 7, 19, 12, 34, 56, 0, time.UTC)

	result, err := server.createPausedFullVMSnapshot(
		context.Background(), service, vm, "tank/vms/devbox", cli.SnapshotsCreateFlags{Full: true},
		controller, vmCheckpointCompatibility{}, now, "full",
	)

	if err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Errorf("createPausedFullVMSnapshot error = %v, want metadata replacement rejection", err)
	}
	if result.Name != "" {
		t.Errorf("result name = %q, want unpublished checkpoint", result.Name)
	}
	gotOutside, readErr := os.ReadFile(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(gotOutside, wantOutside) {
		t.Errorf("outside file = %q, want unchanged %q", gotOutside, wantOutside)
	}
	finalDir := vmCheckpointDir(root, "yeet-20260719T123456Z-vm-manual")
	if _, statErr := os.Lstat(finalDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("published checkpoint stat = %v, want no publication", statErr)
	}
	if got := vmSnapshotZFSCall(calls, "destroy"); got != "zfs destroy tank/vms/devbox@yeet-20260719T123456Z-vm-manual" {
		t.Errorf("destroy call = %q, want rollback of incomplete ZFS snapshot", got)
	}
}

func TestVMCheckpointDirDelegatesEveryFullCheckpoint(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	service := &db.Service{Name: "devbox", ServiceType: db.ServiceTypeVM, ServiceRoot: root}
	vm := db.VMConfig{Sockets: db.VMSocketConfig{APISocketPath: filepath.Join(serviceRunDirForRoot(root), "firecracker.sock")}}
	oldIdentity := vmSnapshotEnsureRuntimeIdentity
	oldChown := vmSnapshotFileChown
	oldChmod := vmSnapshotFileChmod
	vmSnapshotEnsureRuntimeIdentity = func() (vmRuntimeIdentity, error) {
		return vmRuntimeIdentity{UID: 812, GID: 813}, nil
	}
	var delegated string
	vmSnapshotFileChown = func(file *os.File, uid, gid int) error {
		if uid == 0 && gid == 0 {
			return nil
		}
		if uid != 812 || gid != 813 {
			t.Fatalf("chown identity = %d:%d", uid, gid)
		}
		delegated = file.Name()
		return nil
	}
	vmSnapshotFileChmod = func(file *os.File, mode os.FileMode) error { return file.Chmod(mode) }
	t.Cleanup(func() {
		vmSnapshotEnsureRuntimeIdentity = oldIdentity
		vmSnapshotFileChown = oldChown
		vmSnapshotFileChmod = oldChmod
	})
	var calls []string
	controller := &recordingVMFirecracker{calls: &calls}

	_, workspace, err := server.createTemporaryFullVMCheckpoint(context.Background(), service, vm, controller)
	if err != nil {
		t.Fatalf("createTemporaryFullVMCheckpoint: %v", err)
	}
	t.Cleanup(func() {
		_ = workspace.remove()
		_ = workspace.close()
	})
	if delegated != workspace.path() {
		t.Fatalf("delegated path = %q, want %q", delegated, workspace.path())
	}
}

func TestVMCheckpointWorkspaceNormalizesRootAndClosesDescriptors(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(root, "custom", "services", "devbox", "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	baseDir := filepath.Join(dataDir, "checkpoints")
	oldChown := vmSnapshotFileChown
	oldChmod := vmSnapshotFileChmod
	owners := make(map[string]vmRuntimeIdentity)
	modes := make(map[string]os.FileMode)
	vmSnapshotFileChown = func(file *os.File, uid, gid int) error {
		owners[file.Name()] = vmRuntimeIdentity{UID: uid, GID: gid}
		return nil
	}
	vmSnapshotFileChmod = func(file *os.File, mode os.FileMode) error {
		modes[file.Name()] = mode
		return file.Chmod(mode)
	}
	t.Cleanup(func() {
		vmSnapshotFileChown = oldChown
		vmSnapshotFileChmod = oldChmod
	})

	workspace, err := newVMCheckpointWorkspace(baseDir, vmRuntimeIdentity{UID: 812, GID: 813})
	if err != nil {
		t.Fatalf("newVMCheckpointWorkspace: %v", err)
	}
	dataParentFD := int(workspace.dataParent.Fd())
	parentFD := int(workspace.parent.Fd())
	dirFD := int(workspace.dir.Fd())
	if got := owners[baseDir]; got != (vmRuntimeIdentity{UID: 0, GID: 0}) {
		t.Fatalf("checkpoint root owner = %#v, want root:root", got)
	}
	if got := modes[baseDir].Perm(); got != 0o755 {
		t.Fatalf("checkpoint root mode = %#o, want 0755", got)
	}
	if got := owners[workspace.path()]; got != (vmRuntimeIdentity{UID: 812, GID: 813}) {
		t.Fatalf("temporary checkpoint owner = %#v, want 812:813", got)
	}
	if got := modes[workspace.path()].Perm(); got != 0o700 {
		t.Fatalf("temporary checkpoint mode = %#o, want 0700", got)
	}
	nestedDir := filepath.Join(workspace.path(), "runtime", "nested")
	if err := os.MkdirAll(nestedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "state.bin"), []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(nestedDir, "outside-link")); err != nil {
		t.Fatal(err)
	}
	if err := workspace.remove(); err != nil {
		t.Fatalf("remove workspace: %v", err)
	}
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "outside" {
		t.Fatalf("outside file after cleanup = %q, %v; want unchanged", raw, err)
	}
	if err := workspace.close(); err != nil {
		t.Fatalf("close workspace: %v", err)
	}
	for _, fd := range []int{dataParentFD, parentFD, dirFD} {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
			t.Errorf("workspace descriptor %d remains open: %v", fd, err)
		}
	}
}

func TestVMCheckpointWorkspaceRejectsMetadataReplacementBeforePublication(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldChown := vmSnapshotFileChown
	oldChmod := vmSnapshotFileChmod
	vmSnapshotFileChown = func(*os.File, int, int) error { return nil }
	vmSnapshotFileChmod = func(file *os.File, mode os.FileMode) error { return file.Chmod(mode) }
	t.Cleanup(func() {
		vmSnapshotFileChown = oldChown
		vmSnapshotFileChmod = oldChmod
	})
	workspace, err := newVMCheckpointWorkspace(filepath.Join(dataDir, "checkpoints"), vmRuntimeIdentity{UID: 812, GID: 813})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = workspace.remove()
		_ = workspace.close()
	})
	if err := workspace.writeMetadata([]byte("{\"service\":\"devbox\"}\n")); err != nil {
		t.Fatal(err)
	}

	metadataPath := filepath.Join(workspace.path(), "metadata.json")
	if err := os.Rename(metadataPath, metadataPath+".original"); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.json")
	wantOutside := []byte("outside must remain unchanged\n")
	if err := os.WriteFile(outside, wantOutside, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, metadataPath); err != nil {
		t.Fatal(err)
	}

	err = workspace.publish("yeet-final")
	if err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Errorf("publish error = %v, want metadata replacement rejection", err)
	}
	if _, statErr := os.Lstat(filepath.Join(dataDir, "checkpoints", "yeet-final")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("published checkpoint stat = %v, want no publication", statErr)
	}
	gotOutside, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotOutside, wantOutside) {
		t.Errorf("outside file = %q, want unchanged %q", gotOutside, wantOutside)
	}
}

func expectVMCheckpointDelegation(t *testing.T) {
	t.Helper()
	oldIdentity := vmSnapshotEnsureRuntimeIdentity
	oldChown := vmSnapshotFileChown
	oldChmod := vmSnapshotFileChmod
	delegations := 0
	vmSnapshotEnsureRuntimeIdentity = func() (vmRuntimeIdentity, error) {
		return vmRuntimeIdentity{UID: 812, GID: 813}, nil
	}
	vmSnapshotFileChown = func(file *os.File, uid, gid int) error {
		if strings.TrimSpace(file.Name()) == "" {
			t.Fatal("delegated an empty VM checkpoint path")
		}
		if uid == 0 && gid == 0 {
			return nil
		}
		if uid != 812 || gid != 813 {
			t.Fatalf("chown identity = %d:%d, want 812:813", uid, gid)
		}
		delegations++
		return nil
	}
	vmSnapshotFileChmod = func(file *os.File, mode os.FileMode) error { return file.Chmod(mode) }
	t.Cleanup(func() {
		vmSnapshotEnsureRuntimeIdentity = oldIdentity
		vmSnapshotFileChown = oldChown
		vmSnapshotFileChmod = oldChmod
		if delegations == 0 {
			t.Error("VM checkpoint directory was not delegated")
		}
	})
}

func TestFullVMCheckpointMetadataIncludesBalloonConfigHash(t *testing.T) {
	server := newTestServer(t)
	expectVMCheckpointDelegation(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.VM.Balloon = db.VMBalloonConfig{Mode: vmBalloonModeAuto, MinBytes: 1 << 30, StatsIntervalSeconds: vmBalloonDefaultStatsIntervalSeconds}
		return nil
	}); err != nil {
		t.Fatalf("seed VM balloon config: %v", err)
	}
	var calls []string
	server.zfsRunner = recordingVMSnapshotZFSRunner(&calls, nil)
	oldRunning := vmSnapshotIsRunning
	oldController := vmSnapshotFirecracker
	controller := &recordingVMFirecracker{calls: &calls}
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return true, nil }
	vmSnapshotFirecracker = controller
	t.Cleanup(func() {
		vmSnapshotIsRunning = oldRunning
		vmSnapshotFirecracker = oldController
	})

	if err := server.createVMSnapshot(context.Background(), "devbox", cli.SnapshotsCreateFlags{Full: true}, io.Discard); err != nil {
		t.Fatalf("createVMSnapshot: %v", err)
	}

	metadataPaths, err := filepath.Glob(filepath.Join(root, "data", "checkpoints", "yeet-*", "metadata.json"))
	if err != nil {
		t.Fatalf("glob checkpoint metadata: %v", err)
	}
	if len(metadataPaths) != 1 {
		t.Fatalf("checkpoint metadata paths = %#v, want one published checkpoint", metadataPaths)
	}
	raw, err := os.ReadFile(metadataPaths[0])
	if err != nil {
		t.Fatalf("read checkpoint metadata: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatalf("decode checkpoint metadata: %v", err)
	}
	hash, ok := metadata["balloonConfigHash"].(string)
	if !ok || !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("balloonConfigHash = %q, want sha256-prefixed hash in full checkpoint metadata", metadata["balloonConfigHash"])
	}
}

func TestVMSnapshotFullRequiresRunningVM(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	oldRunning := vmSnapshotIsRunning
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return false, nil }
	t.Cleanup(func() { vmSnapshotIsRunning = oldRunning })

	err := server.createVMSnapshot(context.Background(), "devbox", cli.SnapshotsCreateFlags{Full: true}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "full VM checkpoints require") {
		t.Fatalf("createVMSnapshot error = %v, want full requires running", err)
	}
}

func TestVMSnapshotFullFailureCleansIncompleteCheckpoint(t *testing.T) {
	server := newTestServer(t)
	expectVMCheckpointDelegation(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	var calls []string
	server.zfsRunner = recordingVMSnapshotZFSRunner(&calls, nil)
	oldRunning := vmSnapshotIsRunning
	oldController := vmSnapshotFirecracker
	controller := &recordingVMFirecracker{calls: &calls, fullErr: errVMSnapshotTest}
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return true, nil }
	vmSnapshotFirecracker = controller
	t.Cleanup(func() {
		vmSnapshotIsRunning = oldRunning
		vmSnapshotFirecracker = oldController
	})

	err := server.createVMSnapshot(context.Background(), "devbox", cli.SnapshotsCreateFlags{Full: true}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "create full VM checkpoint") {
		t.Fatalf("createVMSnapshot error = %v, want full checkpoint snapshot context", err)
	}
	assertCallOrder(t, calls, "pause ", "full "+filepath.Join(serviceRunDirForRoot(root), "firecracker.sock"), "resume ")
	joined := strings.Join(calls, "\n")
	if strings.Contains(joined, "zfs snapshot") || strings.Contains(joined, "zfs destroy") {
		t.Fatalf("calls = %#v, want full checkpoint failure before ZFS mutation", calls)
	}
	if controller.fullStatePath == "" {
		t.Fatal("fullStatePath empty, expected checkpoint path before failure")
	}
	if _, statErr := os.Stat(filepath.Dir(controller.fullStatePath)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("checkpoint dir stat = %v, want removed", statErr)
	}
}

func TestFailFullVMSnapshotDestroysIncompleteArtifacts(t *testing.T) {
	server := newTestServer(t)
	checkpointDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(checkpointDir, "memory.bin"), []byte("checkpoint"), 0o644); err != nil {
		t.Fatalf("write checkpoint file: %v", err)
	}
	var calls []string
	server.zfsRunner = recordingVMSnapshotZFSRunner(&calls, nil)

	err := server.failFullVMSnapshot(context.Background(), "tank/vms/devbox@yeet-full", checkpointDir, errVMSnapshotTest)

	if err == nil || !strings.Contains(err.Error(), "create full VM checkpoint for snapshot tank/vms/devbox@yeet-full") {
		t.Fatalf("failFullVMSnapshot error = %v, want snapshot context", err)
	}
	if got := vmSnapshotZFSCall(calls, "destroy"); got != "zfs destroy tank/vms/devbox@yeet-full" {
		t.Fatalf("destroy call = %q, want zfs destroy", got)
	}
	if _, statErr := os.Stat(checkpointDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("checkpoint dir stat = %v, want removed", statErr)
	}
}

func TestFailFullVMSnapshotReportsCleanupFailure(t *testing.T) {
	server := newTestServer(t)
	server.zfsRunner = func(context.Context, ...string) (string, string, error) {
		return "", "dataset busy", errVMSnapshotTest
	}

	err := server.failFullVMSnapshot(context.Background(), "tank/vms/devbox@yeet-full", "", errVMSnapshotTest)

	if err == nil || !strings.Contains(err.Error(), "cleanup failed") || !strings.Contains(err.Error(), "dataset busy") {
		t.Fatalf("failFullVMSnapshot error = %v, want cleanup failure context", err)
	}
}

func TestVMSnapshotFullFailsWhenKnownFirecrackerVersionUnavailable(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	firecrackerBinary := filepath.Join(root, "firecracker")
	if err := os.WriteFile(firecrackerBinary, []byte("#!/bin/sh\necho version failed >&2\nexit 2\n"), 0o755); err != nil {
		t.Fatalf("write firecracker binary: %v", err)
	}
	systemdDir := t.TempDir()
	oldSystemdDir := vmSystemdSystemDir
	vmSystemdSystemDir = systemdDir
	t.Cleanup(func() { vmSystemdSystemDir = oldSystemdDir })
	unit, err := renderVMSystemdUnit(vmSystemdConfig{
		Service:          "devbox",
		Runner:           "/srv/catch/run/catch",
		DataDir:          "/srv/catch/data",
		ServicesRoot:     "/srv/services",
		ServiceRoot:      root,
		DiskPath:         filepath.Join(serviceDataDirForRoot(root), "rootfs.raw"),
		Firecracker:      firecrackerBinary,
		Jailer:           filepath.Join(root, "jailer"),
		JailerBase:       filepath.Join(root, "jails"),
		ConfigPath:       filepath.Join(serviceRunDirForRoot(root), "firecracker.json"),
		APISocket:        filepath.Join(serviceRunDirForRoot(root), "firecracker.sock"),
		ConsoleSocket:    filepath.Join(serviceRunDirForRoot(root), "serial.sock"),
		WorkingDirectory: root,
	})
	if err != nil {
		t.Fatalf("render VM systemd unit: %v", err)
	}
	assertJailerOnlyVMUnit(t, unit)
	if err := os.WriteFile(filepath.Join(systemdDir, vmSystemdUnitName("devbox")), []byte(unit), 0o644); err != nil {
		t.Fatalf("write VM systemd unit: %v", err)
	}
	var calls []string
	server.zfsRunner = recordingVMSnapshotZFSRunner(&calls, nil)
	oldRunning := vmSnapshotIsRunning
	oldController := vmSnapshotFirecracker
	controller := &recordingVMFirecracker{calls: &calls}
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return true, nil }
	vmSnapshotFirecracker = controller
	t.Cleanup(func() {
		vmSnapshotIsRunning = oldRunning
		vmSnapshotFirecracker = oldController
	})

	err = server.createVMSnapshot(context.Background(), "devbox", cli.SnapshotsCreateFlags{Full: true}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "read Firecracker version") {
		t.Fatalf("createVMSnapshot error = %v, want Firecracker version failure", err)
	}
	joined := strings.Join(calls, "\n")
	for _, unexpected := range []string{"pause ", "zfs snapshot", "full "} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf("calls = %#v, want Firecracker version failure before %q", calls, unexpected)
		}
	}
	if controller.fullStatePath != "" || controller.fullMemPath != "" {
		t.Fatalf("full snapshot paths = %q %q, want none before identity failure", controller.fullStatePath, controller.fullMemPath)
	}
}

func TestVMSnapshotFullPlansFirecrackerIdentityBeforePause(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	firecrackerBinary := filepath.Join(root, "firecracker")
	if err := os.WriteFile(firecrackerBinary, []byte("firecracker-test-binary"), 0o755); err != nil {
		t.Fatalf("write firecracker binary: %v", err)
	}
	var calls []string
	server.zfsRunner = recordingVMSnapshotZFSRunner(&calls, nil)
	oldRunning := vmSnapshotIsRunning
	oldController := vmSnapshotFirecracker
	oldPathFunc := vmCheckpointFirecrackerPathFunc
	oldVersionFunc := vmCheckpointFirecrackerVersionFunc
	controller := &recordingVMFirecracker{calls: &calls}
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return true, nil }
	vmSnapshotFirecracker = controller
	vmCheckpointFirecrackerPathFunc = func(*db.Service, db.VMConfig) string {
		calls = append(calls, "firecracker path")
		return firecrackerBinary
	}
	vmCheckpointFirecrackerVersionFunc = func(string) (string, error) {
		calls = append(calls, "firecracker version")
		return "", fmt.Errorf("read Firecracker version: %w", errVMSnapshotTest)
	}
	t.Cleanup(func() {
		vmSnapshotIsRunning = oldRunning
		vmSnapshotFirecracker = oldController
		vmCheckpointFirecrackerPathFunc = oldPathFunc
		vmCheckpointFirecrackerVersionFunc = oldVersionFunc
	})

	err := server.createVMSnapshot(context.Background(), "devbox", cli.SnapshotsCreateFlags{Full: true}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "read Firecracker version") {
		t.Fatalf("createVMSnapshot error = %v, want Firecracker version failure", err)
	}
	assertCallOrder(t, calls, "firecracker path", "firecracker version")
	joined := strings.Join(calls, "\n")
	for _, unexpected := range []string{"pause ", "zfs snapshot", "full ", "resume "} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf("calls = %#v, want identity failure before %q", calls, unexpected)
		}
	}
	if controller.fullStatePath != "" || controller.fullMemPath != "" {
		t.Fatalf("full snapshot paths = %q %q, want none before identity failure", controller.fullStatePath, controller.fullMemPath)
	}
}

func TestVMCmdSnapshotIsNotRegistered(t *testing.T) {
	execer := &ttyExecer{}

	err := execer.vmCmdFunc([]string{"snapshot", "--comment", "route comment"})

	if err == nil || !strings.Contains(err.Error(), `unknown vm command "snapshot"`) {
		t.Fatalf("vm snapshot error = %v, want unknown command", err)
	}
}

func TestFirecrackerPatchVMStateUsesUnixHTTP(t *testing.T) {
	socket, requests := newFirecrackerUnixHTTPTestServer(t, http.StatusNoContent)

	if err := firecrackerPatchVMState(context.Background(), socket, "Paused"); err != nil {
		t.Fatalf("firecrackerPatchVMState: %v", err)
	}

	got := <-requests
	if got.Method != http.MethodPatch || got.Path != "/vm" {
		t.Fatalf("request = %s %s, want PATCH /vm", got.Method, got.Path)
	}
	if got.ContentType != "application/json" || got.Accept != "application/json" {
		t.Fatalf("headers content-type=%q accept=%q, want json", got.ContentType, got.Accept)
	}
	if !strings.Contains(got.Body, `"state":"Paused"`) {
		t.Fatalf("body = %q, want paused state", got.Body)
	}
}

func TestFirecrackerFullSnapshotUsesUnixHTTP(t *testing.T) {
	socket, requests := newFirecrackerUnixHTTPTestServer(t, http.StatusNoContent)

	err := firecrackerSnapshotAPI{}.CreateFullSnapshot(context.Background(), socket, "/tmp/state.bin", "/tmp/mem.bin")
	if err != nil {
		t.Fatalf("CreateFullSnapshot: %v", err)
	}

	got := <-requests
	if got.Method != http.MethodPut || got.Path != "/snapshot/create" {
		t.Fatalf("request = %s %s, want PUT /snapshot/create", got.Method, got.Path)
	}
	for _, want := range []string{`"snapshot_type":"Full"`, `"snapshot_path":"/tmp/state.bin"`, `"mem_file_path":"/tmp/mem.bin"`} {
		if !strings.Contains(got.Body, want) {
			t.Fatalf("body = %q, missing %s", got.Body, want)
		}
	}
}

func TestFirecrackerBinaryVersionKeepsStableVersionLine(t *testing.T) {
	dir := t.TempDir()
	firecracker := filepath.Join(dir, "firecracker")
	script := "#!/bin/sh\nprintf 'Firecracker v1.14.3\\n\\n2026-06-14T11:38:52.280711996 [anonymous-instance:main] Firecracker exiting successfully. exit_code=0\\n'\n"
	if err := os.WriteFile(firecracker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake firecracker: %v", err)
	}

	version, err := firecrackerBinaryVersion(firecracker)
	if err != nil {
		t.Fatalf("firecrackerBinaryVersion: %v", err)
	}
	if version != "Firecracker v1.14.3" {
		t.Fatalf("version = %q, want stable version line", version)
	}
}

func TestFirecrackerJSONReportsNonSuccessStatus(t *testing.T) {
	socket, _ := newFirecrackerUnixHTTPTestServer(t, http.StatusInternalServerError)

	err := firecrackerPatchVMState(context.Background(), socket, "Paused")

	if err == nil || !strings.Contains(err.Error(), "returned 500 Internal Server Error") {
		t.Fatalf("firecrackerPatchVMState error = %v, want 500 status", err)
	}
}

type recordingVMFirecracker struct {
	calls            *[]string
	fullStatePath    string
	fullMemPath      string
	fullErr          error
	resumeErr        error
	resumeContextErr error
}

type replacingVMCheckpointController struct {
	replace func(string) error
}

func (replacingVMCheckpointController) Pause(context.Context, string) error  { return nil }
func (replacingVMCheckpointController) Resume(context.Context, string) error { return nil }

func (r replacingVMCheckpointController) CreateFullSnapshot(_ context.Context, _ string, statePath, memPath string) error {
	if err := os.WriteFile(statePath, []byte("state"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(memPath, []byte("memory"), 0o600); err != nil {
		return err
	}
	if r.replace == nil {
		return nil
	}
	return r.replace(filepath.Dir(statePath))
}

func withCurrentVMCheckpointIdentity(t *testing.T) {
	t.Helper()
	oldIdentity := vmSnapshotEnsureRuntimeIdentity
	oldChown := vmSnapshotFileChown
	oldChmod := vmSnapshotFileChmod
	vmSnapshotEnsureRuntimeIdentity = func() (vmRuntimeIdentity, error) {
		uid, gid := os.Getuid(), os.Getgid()
		if uid == 0 || gid == 0 {
			uid, gid = 812, 813
		}
		return vmRuntimeIdentity{UID: uid, GID: gid}, nil
	}
	vmSnapshotFileChown = func(*os.File, int, int) error { return nil }
	vmSnapshotFileChmod = func(file *os.File, mode os.FileMode) error { return file.Chmod(mode) }
	t.Cleanup(func() {
		vmSnapshotEnsureRuntimeIdentity = oldIdentity
		vmSnapshotFileChown = oldChown
		vmSnapshotFileChmod = oldChmod
	})
}

func (r *recordingVMFirecracker) Pause(_ context.Context, socket string) error {
	*r.calls = append(*r.calls, "pause "+socket)
	return nil
}

func (r *recordingVMFirecracker) Resume(ctx context.Context, socket string) error {
	r.resumeContextErr = ctx.Err()
	*r.calls = append(*r.calls, "resume "+socket)
	if r.resumeErr != nil {
		return r.resumeErr
	}
	return r.resumeContextErr
}

func (r *recordingVMFirecracker) CreateFullSnapshot(_ context.Context, socket string, statePath string, memPath string) error {
	r.fullStatePath = statePath
	r.fullMemPath = memPath
	*r.calls = append(*r.calls, "full "+socket+" "+statePath+" "+memPath)
	return r.fullErr
}

func recordingVMSnapshotZFSRunner(calls *[]string, snapshotErr error) zfsCommandRunner {
	return func(_ context.Context, args ...string) (string, string, error) {
		*calls = append(*calls, "zfs "+strings.Join(args, " "))
		if len(args) > 0 && args[0] == "snapshot" && snapshotErr != nil {
			return "", "zfs snapshot failed", snapshotErr
		}
		if len(args) > 0 && args[0] == "list" {
			return "", "", nil
		}
		return "", "", nil
	}
}

func vmSnapshotZFSCall(calls []string, command string) string {
	prefix := "zfs " + command + " "
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			return call
		}
	}
	return ""
}

func assertCallOrder(t *testing.T, calls []string, want ...string) {
	t.Helper()
	last := -1
	for _, needle := range want {
		idx := -1
		for i, call := range calls {
			if strings.Contains(call, needle) {
				idx = i
				break
			}
		}
		if idx == -1 {
			t.Fatalf("calls = %#v, missing %q", calls, needle)
		}
		if idx <= last {
			t.Fatalf("calls = %#v, want %q after prior matched calls", calls, needle)
		}
		last = idx
	}
}

type firecrackerUnixHTTPRequest struct {
	Method      string
	Path        string
	ContentType string
	Accept      string
	Body        string
}

func newFirecrackerUnixHTTPTestServer(t *testing.T, status int) (string, <-chan firecrackerUnixHTTPRequest) {
	t.Helper()
	socketPath := filepath.Join(shortUnixSocketDirForTest(t), "firecracker.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	requests := make(chan firecrackerUnixHTTPRequest, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		requests <- firecrackerUnixHTTPRequest{
			Method:      r.Method,
			Path:        r.URL.Path,
			ContentType: r.Header.Get("Content-Type"),
			Accept:      r.Header.Get("Accept"),
			Body:        string(raw),
		}
		w.WriteHeader(status)
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
	return socketPath, requests
}

var errVMSnapshotTest = errors.New("VM snapshot test error")
