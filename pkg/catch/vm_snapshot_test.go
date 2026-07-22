// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

var errVMSnapshotTest = errors.New("snapshot test failure")

type recordingVMFirecrackerPauser struct {
	calls            []string
	resumeErr        error
	resumeContextErr error
}

func stubVMSnapshotDiskFlusher(t *testing.T) {
	t.Helper()
	old := vmSnapshotDiskFlusher
	vmSnapshotDiskFlusher = func(string) error { return nil }
	t.Cleanup(func() { vmSnapshotDiskFlusher = old })
}

func (r *recordingVMFirecrackerPauser) Pause(_ context.Context, _ string) error {
	r.calls = append(r.calls, "pause")
	return nil
}

func (r *recordingVMFirecrackerPauser) Resume(ctx context.Context, _ string) error {
	r.resumeContextErr = ctx.Err()
	r.calls = append(r.calls, "resume")
	return r.resumeErr
}

func TestCreateVMSnapshotRejectsRawDisk(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendRaw)
	if err := server.createVMSnapshot(context.Background(), "devbox", cli.SnapshotsCreateFlags{}, io.Discard); err == nil || !strings.Contains(err.Error(), "requires a ZFS zvol-backed VM") {
		t.Fatalf("createVMSnapshot error = %v", err)
	}
}

func TestCreateVMSnapshotPauseZFSSnapshotResumeUsesNoMemoryCheckpoint(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	pauser := &recordingVMFirecrackerPauser{}
	flushed := false
	var snapshotCall string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		if args[0] == "snapshot" {
			if !reflect.DeepEqual(pauser.calls, []string{"pause"}) {
				t.Fatalf("pause/resume calls at snapshot = %#v", pauser.calls)
			}
			if !flushed {
				t.Fatal("ZFS snapshot ran before the VM disk was flushed")
			}
			snapshotCall = strings.Join(args, " ")
		}
		return "", "", nil
	}
	oldRunning, oldController, oldFlusher := vmSnapshotIsRunning, vmSnapshotFirecracker, vmSnapshotDiskFlusher
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return true, nil }
	vmSnapshotFirecracker = pauser
	vmSnapshotDiskFlusher = func(path string) error {
		if path != "/dev/zvol/flash/yeet/vms/devbox/vm/d-abc/root" {
			t.Fatalf("flushed disk = %q", path)
		}
		if !reflect.DeepEqual(pauser.calls, []string{"pause"}) {
			t.Fatalf("pause/resume calls at flush = %#v", pauser.calls)
		}
		flushed = true
		return nil
	}
	t.Cleanup(func() {
		vmSnapshotIsRunning, vmSnapshotFirecracker, vmSnapshotDiskFlusher = oldRunning, oldController, oldFlusher
	})

	if err := server.createVMSnapshot(context.Background(), "devbox", cli.SnapshotsCreateFlags{Comment: "before upgrade"}, io.Discard); err != nil {
		t.Fatalf("createVMSnapshot: %v", err)
	}
	if !reflect.DeepEqual(pauser.calls, []string{"pause", "resume"}) {
		t.Fatalf("pause/resume calls = %#v", pauser.calls)
	}
	if !strings.Contains(snapshotCall, "com.yeetrun:checkpoint=disk") {
		t.Fatalf("snapshot call = %q, want disk checkpoint metadata", snapshotCall)
	}
	checkpointDirectory := "check" + "points"
	if _, err := os.Stat(filepath.Join(serviceDataDirForRoot(root), checkpointDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkpoint directory stat = %v, want absent", err)
	}
}

func TestCreateVMSnapshotResumesAfterZFSSnapshotFailure(t *testing.T) {
	stubVMSnapshotDiskFlusher(t)
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendZVOL)
	pauser := &recordingVMFirecrackerPauser{}
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		if args[0] == "snapshot" {
			return "", "snapshot failed", errVMSnapshotTest
		}
		return "", "", nil
	}
	oldRunning, oldController := vmSnapshotIsRunning, vmSnapshotFirecracker
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return true, nil }
	vmSnapshotFirecracker = pauser
	t.Cleanup(func() { vmSnapshotIsRunning, vmSnapshotFirecracker = oldRunning, oldController })
	if err := server.createVMSnapshot(context.Background(), "devbox", cli.SnapshotsCreateFlags{}, io.Discard); err == nil || !strings.Contains(err.Error(), "zfs snapshot") {
		t.Fatalf("createVMSnapshot error = %v", err)
	}
	if !reflect.DeepEqual(pauser.calls, []string{"pause", "resume"}) {
		t.Fatalf("calls = %#v", pauser.calls)
	}
}

func TestCreateVMSnapshotResumesWithoutSnapshotAfterDiskFlushFailure(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendZVOL)
	pauser := &recordingVMFirecrackerPauser{}
	snapshotCalled := false
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		if args[0] == "snapshot" {
			snapshotCalled = true
		}
		return "", "", nil
	}
	oldRunning, oldController, oldFlusher := vmSnapshotIsRunning, vmSnapshotFirecracker, vmSnapshotDiskFlusher
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return true, nil }
	vmSnapshotFirecracker = pauser
	vmSnapshotDiskFlusher = func(string) error { return errVMSnapshotTest }
	t.Cleanup(func() {
		vmSnapshotIsRunning, vmSnapshotFirecracker, vmSnapshotDiskFlusher = oldRunning, oldController, oldFlusher
	})

	err := server.createVMSnapshot(context.Background(), "devbox", cli.SnapshotsCreateFlags{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "flush VM") {
		t.Fatalf("createVMSnapshot error = %v, want disk flush failure", err)
	}
	if snapshotCalled {
		t.Fatal("ZFS snapshot ran after disk flush failed")
	}
	if !reflect.DeepEqual(pauser.calls, []string{"pause", "resume"}) {
		t.Fatalf("pause/resume calls = %#v", pauser.calls)
	}
}

func TestCreateVMSnapshotResumeIgnoresCanceledOperationContext(t *testing.T) {
	stubVMSnapshotDiskFlusher(t)
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendZVOL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pauser := &recordingVMFirecrackerPauser{}
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		if args[0] == "snapshot" {
			cancel()
			return "", "snapshot failed", errVMSnapshotTest
		}
		return "", "", nil
	}
	oldRunning, oldController := vmSnapshotIsRunning, vmSnapshotFirecracker
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return true, nil }
	vmSnapshotFirecracker = pauser
	t.Cleanup(func() { vmSnapshotIsRunning, vmSnapshotFirecracker = oldRunning, oldController })
	_ = server.createVMSnapshot(ctx, "devbox", cli.SnapshotsCreateFlags{}, io.Discard)
	if pauser.resumeContextErr != nil {
		t.Fatalf("resume context error = %v", pauser.resumeContextErr)
	}
}

func TestCreateVMSnapshotStoppedVMDoesNotPause(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendZVOL)
	pauser := &recordingVMFirecrackerPauser{}
	server.zfsRunner = func(context.Context, ...string) (string, string, error) { return "", "", nil }
	oldRunning, oldController := vmSnapshotIsRunning, vmSnapshotFirecracker
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return false, nil }
	vmSnapshotFirecracker = pauser
	t.Cleanup(func() { vmSnapshotIsRunning, vmSnapshotFirecracker = oldRunning, oldController })
	if err := server.createVMSnapshot(context.Background(), "devbox", cli.SnapshotsCreateFlags{}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(pauser.calls) != 0 {
		t.Fatalf("calls = %#v, want none", pauser.calls)
	}
}

func TestCreateVMSnapshotReportsResumeFailureAfterSuccessfulZFSSnapshot(t *testing.T) {
	stubVMSnapshotDiskFlusher(t)
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendZVOL)
	pauser := &recordingVMFirecrackerPauser{resumeErr: errVMSnapshotTest}
	server.zfsRunner = func(context.Context, ...string) (string, string, error) { return "", "", nil }
	oldRunning, oldController := vmSnapshotIsRunning, vmSnapshotFirecracker
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return true, nil }
	vmSnapshotFirecracker = pauser
	t.Cleanup(func() { vmSnapshotIsRunning, vmSnapshotFirecracker = oldRunning, oldController })

	err := server.createVMSnapshot(context.Background(), "devbox", cli.SnapshotsCreateFlags{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "created VM snapshot") || !strings.Contains(err.Error(), "failed to resume VM") {
		t.Fatalf("createVMSnapshot error = %v, want created-snapshot resume failure", err)
	}
	if !reflect.DeepEqual(pauser.calls, []string{"pause", "resume"}) {
		t.Fatalf("calls = %#v, want pause then resume", pauser.calls)
	}
}

func TestCreateVMRuntimeUpgradeRecoveryPointForStoppedZVOL(t *testing.T) {
	server := newTestServer(t)
	service := &db.Service{
		Name: "devbox", ServiceType: db.ServiceTypeVM,
		VM: &db.VMConfig{Disk: db.VMDiskConfig{Backend: vmDiskBackendZVOL, Path: "/dev/zvol/pool/devbox"}},
	}
	var snapshotArgs []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		snapshotArgs = append([]string(nil), args...)
		return "", "", nil
	}
	oldRunning := vmSnapshotIsRunning
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return false, nil }
	t.Cleanup(func() { vmSnapshotIsRunning = oldRunning })

	name, err := server.createVMRuntimeUpgradeRecoveryPoint(context.Background(), service, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if name == "" || !strings.Contains(strings.Join(snapshotArgs, " "), "com.yeetrun:protected=true") {
		t.Fatalf("recovery point = %q args=%q", name, snapshotArgs)
	}
}

func TestCreatePausedVMRuntimeUpgradeRecoveryPointCombinesFailures(t *testing.T) {
	stubVMSnapshotDiskFlusher(t)
	pauser := &recordingVMFirecrackerPauser{resumeErr: errors.New("resume failed")}
	oldController := vmSnapshotFirecracker
	vmSnapshotFirecracker = pauser
	t.Cleanup(func() { vmSnapshotFirecracker = oldController })

	name, err := createPausedVMRuntimeUpgradeRecoveryPoint(
		context.Background(), "devbox", "/run/devbox/firecracker.sock", "/dev/zvol/pool/devbox",
		func(context.Context) (string, error) {
			return "pool/devbox@runtime-upgrade", errors.New("snapshot failed")
		},
	)
	if name != "" || err == nil || !strings.Contains(err.Error(), "snapshot failed") || !strings.Contains(err.Error(), "resume failed") {
		t.Fatalf("combined recovery point failure = %q, %v", name, err)
	}
	if !reflect.DeepEqual(pauser.calls, []string{"pause", "resume"}) {
		t.Fatalf("pause/resume calls = %#v", pauser.calls)
	}
}

func TestCreateVMSnapshotPrunesDiskOnlyRetention(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendZVOL)
	setVMSnapshotRetentionPolicy(t, server, "devbox", 1)
	oldSnapshot := "flash/yeet/vms/devbox/vm/d-abc/root@yeet-old"
	var currentSnapshot string
	var listedDataset string
	var destroyed []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		switch args[0] {
		case "snapshot":
			currentSnapshot = args[len(args)-1]
			if !strings.Contains(strings.Join(args, " "), "com.yeetrun:checkpoint=disk") {
				t.Fatalf("snapshot args = %#v, want disk checkpoint metadata", args)
			}
		case "list":
			listedDataset = args[len(args)-1]
			if currentSnapshot == "" {
				t.Fatal("listed snapshots before creating the current VM snapshot")
			}
			now := time.Now().Unix()
			return fmt.Sprintf(
				"%s\t%d\tcatch\tdevbox\tvm-manual\t-\told\tdisk\tfalse\n%s\t%d\tcatch\tdevbox\tvm-manual\t-\tcurrent\tdisk\tfalse\n",
				oldSnapshot, now-1, currentSnapshot, now,
			), "", nil
		case "destroy":
			destroyed = append(destroyed, args[len(args)-1])
		}
		return "", "", nil
	}
	oldRunning := vmSnapshotIsRunning
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return false, nil }
	t.Cleanup(func() { vmSnapshotIsRunning = oldRunning })

	if err := server.createVMSnapshot(context.Background(), "devbox", cli.SnapshotsCreateFlags{}, io.Discard); err != nil {
		t.Fatalf("createVMSnapshot: %v", err)
	}
	if got, want := listedDataset, "flash/yeet/vms/devbox/vm/d-abc/root"; got != want {
		t.Fatalf("listed dataset = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(destroyed, []string{oldSnapshot}) {
		t.Fatalf("destroyed snapshots = %#v, want only older snapshot", destroyed)
	}
	if currentSnapshot == "" || currentSnapshot == oldSnapshot {
		t.Fatalf("current snapshot = %q, want newly created snapshot", currentSnapshot)
	}
}

func TestCreateVMSnapshotWarningWhenRetentionPruneFails(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendZVOL)
	setVMSnapshotRetentionPolicy(t, server, "devbox", 1)
	oldSnapshot := "flash/yeet/vms/devbox/vm/d-abc/root@yeet-old"
	var currentSnapshot string
	var destroyed []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		switch args[0] {
		case "snapshot":
			currentSnapshot = args[len(args)-1]
		case "list":
			if currentSnapshot == "" {
				t.Fatal("listed snapshots before creating the current VM snapshot")
			}
			now := time.Now().Unix()
			return fmt.Sprintf(
				"%s\t%d\tcatch\tdevbox\tvm-manual\t-\told\tdisk\tfalse\n%s\t%d\tcatch\tdevbox\tvm-manual\t-\tcurrent\tdisk\tfalse\n",
				oldSnapshot, now-1, currentSnapshot, now,
			), "", nil
		case "destroy":
			destroyed = append(destroyed, args[len(args)-1])
			return "", "destroy failed", errVMSnapshotTest
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
	if !strings.Contains(out.String(), "warning: failed to prune VM snapshots") {
		t.Fatalf("output = %q, want prune warning", out.String())
	}
	if !strings.Contains(out.String(), "VM snapshot: "+currentSnapshot) {
		t.Fatalf("output = %q, want created snapshot result", out.String())
	}
	if !reflect.DeepEqual(destroyed, []string{oldSnapshot}) {
		t.Fatalf("destroyed snapshots = %#v, want only older snapshot attempt", destroyed)
	}
}

func setVMSnapshotRetentionPolicy(t *testing.T, server *Server, serviceName string, keepLast int) {
	t.Helper()
	enabled := true
	if _, _, err := server.cfg.DB.MutateService(serviceName, func(_ *db.Data, service *db.Service) error {
		service.SnapshotPolicy = &db.SnapshotPolicy{Enabled: &enabled, KeepLast: &keepLast}
		return nil
	}); err != nil {
		t.Fatalf("set VM snapshot retention policy: %v", err)
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

func TestFirecrackerJSONReportsNonSuccessStatus(t *testing.T) {
	socket, _ := newFirecrackerUnixHTTPTestServer(t, http.StatusInternalServerError)

	err := firecrackerPatchVMState(context.Background(), socket, "Paused")

	if err == nil || !strings.Contains(err.Error(), "returned 500 Internal Server Error") {
		t.Fatalf("firecrackerPatchVMState error = %v, want 500 status", err)
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
