// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestVMSnapshotRejectsRawDisk(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendRaw)

	err := server.createVMSnapshot(context.Background(), "devbox", cli.VMSnapshotFlags{}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "requires a ZFS zvol-backed VM") {
		t.Fatalf("createVMSnapshot error = %v, want zvol-backed rejection", err)
	}
}

func TestVMSnapshotDiskOnlyPausesSnapshotsAndResumesRunningVM(t *testing.T) {
	server := newTestServer(t)
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

	err := server.createVMSnapshot(context.Background(), "devbox", cli.VMSnapshotFlags{Comment: " before upgrade "}, io.Discard)
	if err != nil {
		t.Fatalf("createVMSnapshot: %v", err)
	}

	assertCallOrder(t, calls, "pause ", "zfs snapshot", "resume ")
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
	if controller.fullStatePath != "" || controller.fullMemPath != "" {
		t.Fatalf("full snapshot paths = %q %q, want none", controller.fullStatePath, controller.fullMemPath)
	}
}

func TestVMSnapshotAttemptsResumeAfterSnapshotFailure(t *testing.T) {
	server := newTestServer(t)
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

	err := server.createVMSnapshot(context.Background(), "devbox", cli.VMSnapshotFlags{}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "zfs snapshot") {
		t.Fatalf("createVMSnapshot error = %v, want zfs snapshot error", err)
	}
	assertCallOrder(t, calls, "pause ", "zfs snapshot", "resume ")
}

func TestVMSnapshotResumeIgnoresCanceledOperationContext(t *testing.T) {
	server := newTestServer(t)
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

	err := server.createVMSnapshot(ctx, "devbox", cli.VMSnapshotFlags{}, io.Discard)

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

	err := server.createVMSnapshot(context.Background(), "devbox", cli.VMSnapshotFlags{}, &out)
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

	if err := server.createVMSnapshot(context.Background(), "devbox", cli.VMSnapshotFlags{}, io.Discard); err != nil {
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

	if err := server.createVMSnapshot(context.Background(), "devbox", cli.VMSnapshotFlags{}, &out); err != nil {
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
	var out bytes.Buffer

	err := server.createVMSnapshot(context.Background(), "devbox", cli.VMSnapshotFlags{Full: true, Comment: "checkpoint"}, &out)
	if err != nil {
		t.Fatalf("createVMSnapshot: %v", err)
	}

	assertCallOrder(t, calls, "pause ", "zfs snapshot", "full "+filepath.Join(serviceRunDirForRoot(root), "firecracker.sock"), "resume ")
	if controller.fullStatePath == "" || controller.fullMemPath == "" {
		t.Fatalf("full snapshot paths empty: state=%q mem=%q", controller.fullStatePath, controller.fullMemPath)
	}
	if !strings.Contains(controller.fullStatePath, filepath.Join(root, "data", "checkpoints")) {
		t.Fatalf("state path = %q, want service data checkpoint path", controller.fullStatePath)
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(controller.fullStatePath), "metadata.json"))
	if err != nil {
		t.Fatalf("read checkpoint metadata: %v", err)
	}
	var metadata map[string]string
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatalf("decode checkpoint metadata: %v", err)
	}
	if metadata["service"] != "devbox" || metadata["comment"] != "checkpoint" || metadata["createdBy"] != "catch" {
		t.Fatalf("metadata = %#v, want service/comment/createdBy", metadata)
	}
	if !strings.Contains(metadata["zvolSnapshot"], "flash/yeet/vms/devbox/vm/d-abc/root@yeet-") {
		t.Fatalf("zvolSnapshot = %q, want zvol snapshot", metadata["zvolSnapshot"])
	}
	if metadata["firecrackerState"] != controller.fullStatePath || metadata["firecrackerMemory"] != controller.fullMemPath {
		t.Fatalf("metadata paths = %#v, want %q %q", metadata, controller.fullStatePath, controller.fullMemPath)
	}
	if !strings.Contains(out.String(), "Firecracker state: "+controller.fullStatePath) ||
		!strings.Contains(out.String(), "Firecracker memory: "+controller.fullMemPath) {
		t.Fatalf("output = %q, want checkpoint paths", out.String())
	}
}

func TestVMSnapshotFullRequiresRunningVM(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	oldRunning := vmSnapshotIsRunning
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return false, nil }
	t.Cleanup(func() { vmSnapshotIsRunning = oldRunning })

	err := server.createVMSnapshot(context.Background(), "devbox", cli.VMSnapshotFlags{Full: true}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "full VM checkpoints require") {
		t.Fatalf("createVMSnapshot error = %v, want full requires running", err)
	}
}

func TestVMSnapshotFullFailureCleansIncompleteCheckpoint(t *testing.T) {
	server := newTestServer(t)
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

	err := server.createVMSnapshot(context.Background(), "devbox", cli.VMSnapshotFlags{Full: true}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "create full VM checkpoint for snapshot flash/yeet/vms/devbox/vm/d-abc/root@yeet-") {
		t.Fatalf("createVMSnapshot error = %v, want full checkpoint snapshot context", err)
	}
	assertCallOrder(t, calls, "pause ", "zfs snapshot", "full "+filepath.Join(serviceRunDirForRoot(root), "firecracker.sock"), "zfs destroy flash/yeet/vms/devbox/vm/d-abc/root@yeet-", "resume ")
	if controller.fullStatePath == "" {
		t.Fatal("fullStatePath empty, expected checkpoint path before failure")
	}
	if _, statErr := os.Stat(filepath.Dir(controller.fullStatePath)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("checkpoint dir stat = %v, want removed", statErr)
	}
}

func TestVMCmdSnapshotRoutesAndParsesFlags(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	var calls []string
	server.zfsRunner = recordingVMSnapshotZFSRunner(&calls, nil)
	oldRunning := vmSnapshotIsRunning
	oldController := vmSnapshotFirecracker
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return false, nil }
	vmSnapshotFirecracker = &recordingVMFirecracker{calls: &calls}
	t.Cleanup(func() {
		vmSnapshotIsRunning = oldRunning
		vmSnapshotFirecracker = oldController
	})
	var out bytes.Buffer
	execer := &ttyExecer{s: server, sn: "devbox", rw: &out}

	if err := execer.vmCmdFunc([]string{"snapshot", "--comment", " route comment "}); err != nil {
		t.Fatalf("vm snapshot: %v", err)
	}

	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "com.yeetrun:comment=route comment") {
		t.Fatalf("calls = %#v, want routed trimmed comment", calls)
	}
	if !strings.Contains(out.String(), "VM snapshot: flash/yeet/vms/devbox/vm/d-abc/root@yeet-") {
		t.Fatalf("output = %q, want VM snapshot line", out.String())
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

var errVMSnapshotTest = errors.New("vm snapshot test error")
