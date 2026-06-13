// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestConfirmTSUpdateReturnsPromptWriteError(t *testing.T) {
	writeErr := errors.New("write failed")
	ok, err := confirmTSUpdate(readWriter{
		Reader: strings.NewReader("y\n"),
		Writer: failingWriter{err: writeErr},
	}, "1.90.0", "1.92.0")
	if err == nil {
		t.Fatal("expected prompt write error")
	}
	if ok {
		t.Fatal("confirmation should be false on prompt write error")
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected wrapped write error, got %v", err)
	}
}

func TestRunCmdDispatchesVMPayload(t *testing.T) {
	for _, payload := range []string{vmUbuntu2604Payload, "vm://foo/bar"} {
		t.Run(payload, func(t *testing.T) {
			server := newTestServer(t)
			var called bool
			oldRunVM := runVMCmdFunc
			defer func() { runVMCmdFunc = oldRunVM }()
			runVMCmdFunc = func(e *ttyExecer, flags cli.RunFlags, gotPayload string) error {
				called = true
				if gotPayload != payload {
					t.Fatalf("payload = %q, want %q", gotPayload, payload)
				}
				if flags.Net != "svc" || flags.CPUs != 4 {
					t.Fatalf("flags = %#v", flags)
				}
				return nil
			}

			execer := &ttyExecer{ctx: context.Background(), s: server, sn: "devbox", rw: &bytes.Buffer{}}
			if err := execer.runCmdFunc(cli.RunFlags{Net: "svc", CPUs: 4}, []string{payload}); err != nil {
				t.Fatalf("runCmdFunc: %v", err)
			}
			if !called {
				t.Fatal("VM run function was not called")
			}
		})
	}
}

func TestVMConsoleRejectsNonVMService(t *testing.T) {
	server := newTestServer(t)
	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		d.Services = map[string]*db.Service{
			"api": {Name: "api", ServiceType: db.ServiceTypeDockerCompose},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	execer := &ttyExecer{ctx: context.Background(), s: server, sn: "api", rw: &bytes.Buffer{}}
	err := execer.vmCmdFunc([]string{"console"})
	if err == nil || !strings.Contains(err.Error(), `vm console requires type "vm"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestVMConsoleRejectsMissingSocket(t *testing.T) {
	server := newTestServer(t)
	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		d.Services = map[string]*db.Service{
			"devbox": {Name: "devbox", ServiceType: db.ServiceTypeVM, VM: &db.VMConfig{}},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	execer := &ttyExecer{ctx: context.Background(), s: server, sn: "devbox", rw: &bytes.Buffer{}}
	err := execer.vmCmdFunc([]string{"console"})
	if err == nil || !strings.Contains(err.Error(), `service "devbox" has no VM console socket`) {
		t.Fatalf("error = %v", err)
	}
}

func TestVMConsoleSocketNotConnectedIncludesPath(t *testing.T) {
	server := newTestServer(t)
	socketPath := filepath.Join(t.TempDir(), "serial.sock")
	if err := os.WriteFile(socketPath, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("write socket placeholder: %v", err)
	}
	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		d.Services = map[string]*db.Service{
			"devbox": {
				Name:        "devbox",
				ServiceType: db.ServiceTypeVM,
				VM:          &db.VMConfig{Console: db.VMConsoleConfig{SocketPath: socketPath}},
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	execer := &ttyExecer{ctx: context.Background(), s: server, sn: "devbox", rw: &bytes.Buffer{}}
	err := execer.vmCmdFunc([]string{"console"})
	if err == nil || !strings.Contains(err.Error(), "connect VM console socket "+socketPath) {
		t.Fatalf("error = %v", err)
	}
}

func TestSnapshotsDefaultsShow(t *testing.T) {
	server := newTestServer(t)
	keep := 3
	enabled := false
	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		d.SnapshotDefaults = &db.SnapshotPolicy{Enabled: &enabled, KeepLast: &keep, MaxAge: "72h"}
		return nil
	}); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.snapshotsCmdFunc([]string{"defaults", "show"}); err != nil {
		t.Fatalf("snapshotsCmdFunc: %v", err)
	}
	want := strings.Join([]string{
		"# effective snapshot defaults",
		"enabled = false",
		"keep_last = 3",
		"max_age = \"72h\"",
		"events = [\"run\", \"docker-update\", \"service-root-migration\"]",
		"required = true",
		"",
	}, "\n")
	if got := out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestSnapshotsDefaultsSetPersistsPolicy(t *testing.T) {
	server := newTestServer(t)
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	err := execer.snapshotsCmdFunc([]string{"defaults", "set", "--enabled=false", "--keep-last=3", "--max-age=72h", "--required=false"})
	if err != nil {
		t.Fatalf("snapshotsCmdFunc: %v", err)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	def := dv.SnapshotDefaults()
	if !def.Valid() || def.Enabled().Get() || def.KeepLast().Get() != 3 || def.MaxAge() != "72h" || def.Required().Get() {
		t.Fatalf("defaults = %#v", def.AsStruct())
	}
}

func TestSnapshotsDefaultsSetInvalidKeepLastRollsBack(t *testing.T) {
	server := newTestServer(t)
	seedSnapshotDefaults(t, server)
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	err := execer.snapshotsCmdFunc([]string{"defaults", "set", "--enabled=false", "--keep-last=0"})
	if err == nil {
		t.Fatal("snapshotsCmdFunc succeeded with invalid keep-last")
	}
	assertSeedSnapshotDefaults(t, server)
}

func TestSnapshotsDefaultsSetInvalidEventsRollsBack(t *testing.T) {
	server := newTestServer(t)
	seedSnapshotDefaults(t, server)
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	err := execer.snapshotsCmdFunc([]string{"defaults", "set", "--enabled=false", "--events=run,bad-event"})
	if err == nil {
		t.Fatal("snapshotsCmdFunc succeeded with invalid events")
	}
	assertSeedSnapshotDefaults(t, server)
}

func TestSnapshotsListCommandRendersRecoveryPoints(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendZVOL)
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		if args[0] == "list" {
			return "flash/yeet/vms/devbox/vm/d-abc/root@yeet-20260613T203100Z-vm-manual-g0\t1781382660\tcatch\tdevbox\tvm-manual\t0\tbefore upgrade\tdisk\tfalse\n", "", nil
		}
		return "", "", nil
	}
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.snapshotsCmdFunc([]string{"list", "devbox"}); err != nil {
		t.Fatalf("snapshots list: %v", err)
	}
	if !strings.Contains(out.String(), "devbox") ||
		!strings.Contains(out.String(), "yeet-20260613T203100Z-vm-manual-g0") ||
		!strings.Contains(out.String(), "before upgrade") {
		t.Fatalf("output = %q, want recovery point row", out.String())
	}
}

func TestSnapshotsInspectCommandRendersRecoveryPoint(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendZVOL)
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		if args[0] == "list" {
			return "flash/yeet/vms/devbox/vm/d-abc/root@yeet-20260613T203100Z-vm-manual-g0\t1781382660\tcatch\tdevbox\tvm-manual\t0\tbefore upgrade\tdisk\tfalse\n", "", nil
		}
		return "", "", nil
	}
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.snapshotsCmdFunc([]string{"inspect", "devbox", "yeet-20260613T203100Z"}); err != nil {
		t.Fatalf("snapshots inspect: %v", err)
	}
	for _, want := range []string{
		"Service: devbox",
		"Snapshot: flash/yeet/vms/devbox/vm/d-abc/root@yeet-20260613T203100Z-vm-manual-g0",
		"Short name: yeet-20260613T203100Z-vm-manual-g0",
		"Mode: disk",
		"Retention: managed",
		"Actions: inspect, protect, rm",
		"Comment: before upgrade",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("inspect output missing %q:\n%s", want, out.String())
		}
	}
}

func TestSnapshotsInspectCommandRendersFullVMCheckpointPaths(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	snapshotName := "flash/yeet/vms/devbox/vm/d-abc/root@yeet-20260613T203100Z-vm-manual-g0"
	statePath, memoryPath := seedFullVMCheckpointMetadata(t, root, "devbox", snapshotName)
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		if args[0] == "list" {
			return snapshotName + "\t1781382660\tcatch\tdevbox\tvm-manual\t0\tfull checkpoint\tfull\tfalse\n", "", nil
		}
		return "", "", nil
	}
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.snapshotsCmdFunc([]string{"inspect", "devbox", "yeet-20260613T203100Z"}); err != nil {
		t.Fatalf("snapshots inspect: %v", err)
	}
	for _, want := range []string{
		"Mode: full",
		"Firecracker state: " + statePath,
		"Firecracker memory: " + memoryPath,
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("inspect output missing %q:\n%s", want, out.String())
		}
	}
}

func TestSnapshotsInspectCommandRendersJSON(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendZVOL)
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		if args[0] == "list" {
			return "flash/yeet/vms/devbox/vm/d-abc/root@yeet-20260613T203100Z-vm-manual-g0\t1781382660\tcatch\tdevbox\tvm-manual\t0\tbefore upgrade\tdisk\ttrue\n", "", nil
		}
		return "", "", nil
	}
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.snapshotsCmdFunc([]string{"inspect", "devbox", "yeet-20260613T203100Z", "--format=json"}); err != nil {
		t.Fatalf("snapshots inspect json: %v", err)
	}
	if !strings.Contains(out.String(), `"service":"devbox"`) ||
		!strings.Contains(out.String(), `"retention":"protected"`) ||
		!strings.Contains(out.String(), `"actions":["inspect","unprotect"]`) {
		t.Fatalf("json output = %s", out.String())
	}
}

func TestSnapshotsProtectAndRemoveCommands(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendZVOL)
	var calls [][]string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "list" {
			return "flash/yeet/vms/devbox/vm/d-abc/root@yeet-20260613T203100Z-vm-manual-g0\t1781382660\tcatch\tdevbox\tvm-manual\t0\tnote\tdisk\tfalse\n", "", nil
		}
		return "", "", nil
	}
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.snapshotsCmdFunc([]string{"protect", "devbox", "yeet-20260613T203100Z"}); err != nil {
		t.Fatalf("snapshots protect: %v", err)
	}
	if err := execer.snapshotsCmdFunc([]string{"rm", "devbox", "yeet-20260613T203100Z", "--yes"}); err != nil {
		t.Fatalf("snapshots rm: %v", err)
	}
	joined := joinedZFSCalls(calls)
	wantDestroy := []string{"destroy", "flash/yeet/vms/devbox/vm/d-abc/root@yeet-20260613T203100Z-vm-manual-g0"}
	if !strings.Contains(joined, "set com.yeetrun:protected=true") || !hasZFSCall(calls, wantDestroy) {
		t.Fatalf("calls = %#v, want protect and destroy", calls)
	}
}

func joinedZFSCalls(calls [][]string) string {
	var lines []string
	for _, call := range calls {
		lines = append(lines, strings.Join(call, " "))
	}
	return strings.Join(lines, "\n")
}

func hasZFSCall(calls [][]string, want []string) bool {
	for _, call := range calls {
		if reflect.DeepEqual(call, want) {
			return true
		}
	}
	return false
}

func TestSnapshotsUnprotectCommand(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendZVOL)
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "list" {
			return "flash/yeet/vms/devbox/vm/d-abc/root@yeet-20260613T203100Z-vm-manual-g0\t1781382660\tcatch\tdevbox\tvm-manual\t0\tnote\tdisk\ttrue\n", "", nil
		}
		return "", "", nil
	}
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.snapshotsCmdFunc([]string{"unprotect", "devbox", "yeet-20260613T203100Z"}); err != nil {
		t.Fatalf("snapshots unprotect: %v", err)
	}
	if joined := strings.Join(calls, "\n"); !strings.Contains(joined, "set com.yeetrun:protected=false") {
		t.Fatalf("calls = %#v, want unprotect property set", calls)
	}
}

func TestSnapshotsRemoveRejectsProtectedRecoveryPoint(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendZVOL)
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "list" {
			return "flash/yeet/vms/devbox/vm/d-abc/root@yeet-20260613T203100Z-vm-manual-g0\t1781382660\tcatch\tdevbox\tvm-manual\t0\tnote\tdisk\ttrue\n", "", nil
		}
		return "", "", nil
	}
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &bytes.Buffer{}}
	err := execer.snapshotsCmdFunc([]string{"rm", "devbox", "yeet-20260613T203100Z", "--yes"})
	if err == nil || !strings.Contains(err.Error(), "is protected; unprotect it before removing") {
		t.Fatalf("snapshots rm error = %v, want protected rejection", err)
	}
	if strings.Contains(strings.Join(calls, "\n"), "destroy ") {
		t.Fatalf("calls = %#v, protected snapshot should not be destroyed", calls)
	}
}

func TestSnapshotsRemoveConfirmSkip(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendZVOL)
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "list" {
			return "flash/yeet/vms/devbox/vm/d-abc/root@yeet-20260613T203100Z-vm-manual-g0\t1781382660\tcatch\tdevbox\tvm-manual\t0\tnote\tdisk\tfalse\n", "", nil
		}
		return "", "", nil
	}
	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		rw:  readWriter{Reader: strings.NewReader("\n"), Writer: &out},
	}
	if err := execer.snapshotsCmdFunc([]string{"rm", "devbox", "yeet-20260613T203100Z"}); err != nil {
		t.Fatalf("snapshots rm: %v", err)
	}
	if strings.Contains(strings.Join(calls, "\n"), "destroy ") {
		t.Fatalf("calls = %#v, skipped removal should not destroy", calls)
	}
	if !strings.Contains(out.String(), "Skipped recovery point") {
		t.Fatalf("output = %q, want skipped message", out.String())
	}
}

func TestApplySnapshotDefaultsFlagsRejectsNilPolicy(t *testing.T) {
	err := applySnapshotDefaultsFlags(nil, cli.SnapshotDefaultsSetFlags{Enabled: "false"})
	if err == nil {
		t.Fatal("applySnapshotDefaultsFlags succeeded with nil policy")
	}
}

func seedSnapshotDefaults(t *testing.T, server *Server) {
	t.Helper()
	enabled := true
	keep := 4
	required := true
	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		d.SnapshotDefaults = &db.SnapshotPolicy{
			Enabled:  &enabled,
			KeepLast: &keep,
			MaxAge:   "48h",
			Events:   []string{"run"},
			Required: &required,
		}
		return nil
	}); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}
}

func assertSeedSnapshotDefaults(t *testing.T, server *Server) {
	t.Helper()
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	def := dv.SnapshotDefaults()
	if !def.Valid() || !def.Enabled().Get() || def.KeepLast().Get() != 4 || def.MaxAge() != "48h" || !def.Required().Get() {
		t.Fatalf("defaults = %#v", def.AsStruct())
	}
	if got := def.Events().AsSlice(); len(got) != 1 || got[0] != "run" {
		t.Fatalf("events = %#v", got)
	}
}

func TestMountCmdListReturnsFlushError(t *testing.T) {
	server := newTestServer(t)
	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		d.Volumes = map[string]*db.Volume{
			"data": {
				Name: "data",
				Src:  "host:/srv/data",
				Path: "/mnt/data",
				Type: "sshfs",
				Opts: "ro",
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed volume: %v", err)
	}

	writeErr := errors.New("write failed")
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		rw: readWriter{
			Reader: strings.NewReader(""),
			Writer: failingWriter{err: writeErr},
		},
	}

	err := execer.mountCmdFunc(cli.MountFlags{}, nil)
	if err == nil {
		t.Fatal("expected mount listing write error")
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected wrapped write error, got %v", err)
	}
}

func TestMountCmdCreatePersistsVolumeAndRunsMount(t *testing.T) {
	server := newTestServer(t)

	oldCheckMountCommand := checkMountCommand
	oldMountVolume := mountVolume
	defer func() {
		checkMountCommand = oldCheckMountCommand
		mountVolume = oldMountVolume
	}()

	var checkedType string
	checkMountCommand = func(mountType string) error {
		checkedType = mountType
		return nil
	}
	var mounted db.Volume
	mountVolume = func(vol db.Volume) error {
		mounted = vol
		return nil
	}

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		rw:  &out,
	}

	flags := cli.MountFlags{
		Type: "sshfs",
		Opts: "ro",
		Deps: []string{"network-online.target"},
	}
	if err := execer.mountCmdFunc(flags, []string{"host:/srv/data", "data"}); err != nil {
		t.Fatalf("mountCmdFunc: %v", err)
	}

	if checkedType != "sshfs" {
		t.Fatalf("checked mount type = %q, want sshfs", checkedType)
	}
	wantPath := server.cfg.MountsRoot + "/data"
	if mounted.Name != "data" || mounted.Src != "host:/srv/data" || mounted.Path != wantPath || mounted.Type != "sshfs" || mounted.Opts != "ro" || mounted.Deps != "network-online.target" {
		t.Fatalf("mounted volume = %+v", mounted)
	}
	if got := out.String(); !strings.Contains(got, "Mounted host:/srv/data at "+wantPath) {
		t.Fatalf("mount output = %q", got)
	}

	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("get db: %v", err)
	}
	vol, ok := dv.Volumes().GetOk("data")
	if !ok {
		t.Fatal("expected persisted data volume")
	}
	if got := vol.Path(); got != wantPath {
		t.Fatalf("persisted volume path = %q, want %q", got, wantPath)
	}
}

func TestUmountNameFromArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "one mount name", args: []string{"data"}, want: "data"},
		{name: "missing mount name", args: nil, wantErr: true},
		{name: "too many args", args: []string{"data", "extra"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := umountNameFromArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("mount name = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUmountCmdFuncRunsUnmountAndDeletesVolume(t *testing.T) {
	server := newTestServer(t)
	seedTTYOpsVolumes(t, server)

	oldUnmountVolume := unmountVolume
	defer func() { unmountVolume = oldUnmountVolume }()
	var gotVolume db.Volume
	unmountVolume = func(_ *ttyExecer, vol db.Volume) error {
		gotVolume = vol
		return nil
	}

	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		rw:  &bytes.Buffer{},
	}
	if err := execer.umountCmdFunc([]string{"data"}); err != nil {
		t.Fatalf("umountCmdFunc: %v", err)
	}
	if gotVolume.Name != "data" || gotVolume.Path != "/mnt/data" {
		t.Fatalf("unmounted volume = %+v", gotVolume)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("get db: %v", err)
	}
	if dv.Volumes().Contains("data") {
		t.Fatal("expected data volume to be removed")
	}
	if !dv.Volumes().Contains("logs") {
		t.Fatal("expected unrelated logs volume to remain")
	}
}

func TestUmountCmdFuncKeepsVolumeWhenUnmountFails(t *testing.T) {
	server := newTestServer(t)
	seedTTYOpsVolumes(t, server)

	oldUnmountVolume := unmountVolume
	defer func() { unmountVolume = oldUnmountVolume }()
	unmountErr := errors.New("busy")
	unmountVolume = func(_ *ttyExecer, vol db.Volume) error {
		if vol.Name != "data" {
			t.Fatalf("unmounted volume name = %q, want data", vol.Name)
		}
		return unmountErr
	}

	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		rw:  &bytes.Buffer{},
	}
	err := execer.umountCmdFunc([]string{"data"})
	if err == nil {
		t.Fatal("expected unmount error")
	}
	if !errors.Is(err, unmountErr) {
		t.Fatalf("expected wrapped unmount error, got %v", err)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("get db: %v", err)
	}
	if !dv.Volumes().Contains("data") {
		t.Fatal("expected data volume to remain")
	}
}

func TestIPCmdFuncUsesSystemArgsAndPrintsIPs(t *testing.T) {
	oldListIPv4Addrs := listIPv4AddrsFn
	defer func() { listIPv4AddrsFn = oldListIPv4Addrs }()
	var gotArgs []string
	listIPv4AddrsFn = func(args []string) ([]ifaceIP, error) {
		gotArgs = append([]string(nil), args...)
		return []ifaceIP{
			{Interface: "eth0", IP: "10.0.0.5"},
			{Interface: "tailscale0", IP: "100.64.0.2"},
		}, nil
	}

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   newTestServer(t),
		sn:  SystemService,
		rw:  &out,
	}
	if err := execer.ipCmdFunc(); err != nil {
		t.Fatalf("ipCmdFunc: %v", err)
	}

	wantArgs := []string{"-o", "-4", "addr", "list"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("ip args = %#v, want %#v", gotArgs, wantArgs)
	}
	if got := out.String(); got != "10.0.0.5\n100.64.0.2\n" {
		t.Fatalf("ip output = %q", got)
	}
}

func TestIPCmdFuncUsesNetNSArgsForServiceArtifact(t *testing.T) {
	server := newTestServer(t)
	if _, _, err := server.cfg.DB.MutateService("svc-ip", func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeSystemd
		s.Generation = 3
		s.LatestGeneration = 3
		s.Artifacts = db.ArtifactStore{
			db.ArtifactNetNSService: {
				Refs: map[db.ArtifactRef]string{
					db.Gen(3): "/tmp/netns.service",
				},
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed service: %v", err)
	}

	oldListIPv4Addrs := listIPv4AddrsFn
	defer func() { listIPv4AddrsFn = oldListIPv4Addrs }()
	var gotArgs []string
	listIPv4AddrsFn = func(args []string) ([]ifaceIP, error) {
		gotArgs = append([]string(nil), args...)
		return []ifaceIP{{Interface: "eth0", IP: "10.0.0.8"}}, nil
	}

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  "svc-ip",
		rw:  &out,
	}
	if err := execer.ipCmdFunc(); err != nil {
		t.Fatalf("ipCmdFunc: %v", err)
	}

	wantArgs := []string{"netns", "exec", "yeet-svc-ip-ns", "ip", "-o", "-4", "addr", "list"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("ip args = %#v, want %#v", gotArgs, wantArgs)
	}
	if got := out.String(); got != "10.0.0.8\n" {
		t.Fatalf("ip output = %q", got)
	}
}

func TestNormalizeTailscaleTrack(t *testing.T) {
	tests := []struct {
		name    string
		track   string
		want    string
		wantErr bool
	}{
		{name: "stable", track: " stable ", want: "stable"},
		{name: "unstable", track: "unstable", want: "unstable"},
		{name: "invalid", track: "latest", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeTailscaleTrack(tt.track)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("track = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTailscaleTrackVersionFromMeta(t *testing.T) {
	tests := []struct {
		name    string
		meta    tailscaleTrackMeta
		want    string
		wantErr bool
	}{
		{name: "valid", meta: tailscaleTrackMeta{TarballsVersion: " 1.94.2 "}, want: "1.94.2"},
		{name: "empty", meta: tailscaleTrackMeta{}, wantErr: true},
		{name: "invalid", meta: tailscaleTrackMeta{TarballsVersion: "not-semver"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tailscaleTrackVersionFromMeta(tt.meta)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("version = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTailscaleLatestVersionForTrackUsesLookupURL(t *testing.T) {
	var gotTrack string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/meta" {
			t.Fatalf("path = %q, want /meta", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"TarballsVersion":"1.94.2"}`))
	}))
	defer ts.Close()

	oldURL := tailscaleTrackMetaURL
	oldClient := tailscaleTrackHTTPClient
	defer func() {
		tailscaleTrackMetaURL = oldURL
		tailscaleTrackHTTPClient = oldClient
	}()
	tailscaleTrackMetaURL = func(track string) string {
		gotTrack = track
		return ts.URL + "/meta"
	}
	tailscaleTrackHTTPClient = ts.Client()

	got, err := tailscaleLatestVersionForTrack(" stable ")
	if err != nil {
		t.Fatalf("tailscaleLatestVersionForTrack: %v", err)
	}
	if got != "1.94.2" {
		t.Fatalf("version = %q, want 1.94.2", got)
	}
	if gotTrack != "stable" {
		t.Fatalf("lookup track = %q, want stable", gotTrack)
	}
}

func TestTailscaleLatestVersionForTrackRejectsHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	oldURL := tailscaleTrackMetaURL
	oldClient := tailscaleTrackHTTPClient
	defer func() {
		tailscaleTrackMetaURL = oldURL
		tailscaleTrackHTTPClient = oldClient
	}()
	tailscaleTrackMetaURL = func(track string) string {
		return ts.URL
	}
	tailscaleTrackHTTPClient = ts.Client()

	if _, err := tailscaleLatestVersionForTrack("unstable"); err == nil {
		t.Fatal("expected HTTP status error")
	}
}

func seedTTYOpsVolumes(t *testing.T, server *Server) {
	t.Helper()
	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		d.Volumes = map[string]*db.Volume{
			"data": {
				Name: "data",
				Src:  "host:/srv/data",
				Path: "/mnt/data",
				Type: "sshfs",
				Opts: "ro",
			},
			"logs": {
				Name: "logs",
				Src:  "host:/srv/logs",
				Path: "/mnt/logs",
				Type: "sshfs",
				Opts: "ro",
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed volumes: %v", err)
	}
}

func TestDockerCmdFuncRejectsInvalidForms(t *testing.T) {
	execer := &ttyExecer{}
	for _, args := range [][]string{
		nil,
		{"bogus"},
		{"pull", "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if err := execer.dockerCmdFunc(args); err == nil {
				t.Fatalf("dockerCmdFunc(%v) returned nil error", args)
			}
		})
	}
}

func TestDockerCmdFuncOutdatedParsesFormat(t *testing.T) {
	server := newTestServer(t)
	addTestService(t, server, "web", db.ServiceTypeDockerCompose)
	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  "web",
		rw:  &out,
		dockerOutdatedFunc: func(ctx context.Context, service string, opts svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error) {
			if service != "web" {
				t.Fatalf("service = %q, want web", service)
			}
			if !opts.IncludeInternal {
				t.Fatal("scoped docker outdated should include internal-image unknown rows")
			}
			return []svc.DockerOutdatedRow{{
				ServiceName:   "web",
				ContainerName: "app",
				Image:         "ghcr.io/acme/app:latest",
				RunningDigest: "sha256:old",
				LatestDigest:  "sha256:new",
				Status:        svc.DockerOutdatedUpdateAvailable,
			}}, nil
		},
	}
	if err := execer.dockerCmdFunc([]string{"outdated", "--format=json"}); err != nil {
		t.Fatalf("dockerCmdFunc outdated: %v", err)
	}
	var rows []svc.DockerOutdatedRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("outdated JSON invalid: %v\n%s", err, out.String())
	}
	if len(rows) != 1 || rows[0].ServiceName != "web" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestDockerCmdFuncOutdatedRejectsInvalidFormatBeforeScan(t *testing.T) {
	server := newTestServer(t)
	addTestService(t, server, "web", db.ServiceTypeDockerCompose)
	called := false
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  "web",
		rw:  &bytes.Buffer{},
		dockerOutdatedFunc: func(context.Context, string, svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error) {
			called = true
			return nil, nil
		},
	}
	err := execer.dockerCmdFunc([]string{"outdated", "--format=jsn"})
	if err == nil || !strings.Contains(err.Error(), `unsupported docker outdated format "jsn"`) {
		t.Fatalf("docker outdated invalid format error = %v", err)
	}
	if called {
		t.Fatal("dockerOutdatedFunc called for invalid format")
	}

	execer.sn = SystemService
	execer.dockerOutdatedFunc = nil
	execer.dockerOutdatedAllFunc = func(context.Context) ([]svc.DockerOutdatedRow, error) {
		called = true
		return nil, nil
	}
	err = execer.dockerCmdFunc([]string{"outdated", "--format=jsn"})
	if err == nil || !strings.Contains(err.Error(), `unsupported docker outdated format "jsn"`) {
		t.Fatalf("sys docker outdated invalid format error = %v", err)
	}
	if called {
		t.Fatal("dockerOutdatedAllFunc called for invalid format")
	}
}

func TestDockerCmdFuncOutdatedSystemServiceUsesAllHook(t *testing.T) {
	var out bytes.Buffer
	called := false
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   newTestServer(t),
		sn:  SystemService,
		rw:  &out,
		dockerOutdatedAllFunc: func(context.Context) ([]svc.DockerOutdatedRow, error) {
			called = true
			return []svc.DockerOutdatedRow{
				{ServiceName: "zeta", ContainerName: "app", Image: "ghcr.io/acme/zeta:latest", Status: svc.DockerOutdatedCurrent},
				{ServiceName: "alpha", ContainerName: "app", Image: "ghcr.io/acme/alpha:latest", Status: svc.DockerOutdatedUpdateAvailable},
			}, nil
		},
	}
	if err := execer.dockerCmdFunc([]string{"outdated", "--format=json"}); err != nil {
		t.Fatalf("dockerCmdFunc sys outdated: %v", err)
	}
	if !called {
		t.Fatal("dockerOutdatedAllFunc was not called")
	}
	var rows []svc.DockerOutdatedRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("sys outdated JSON invalid: %v\n%s", err, out.String())
	}
	if got := []string{rows[0].ServiceName, rows[1].ServiceName}; !reflect.DeepEqual(got, []string{"alpha", "zeta"}) {
		t.Fatalf("row service order = %v", got)
	}
}

func TestDockerOutdatedCmdFuncRejectsNonDockerScopedService(t *testing.T) {
	server := newTestServer(t)
	addTestService(t, server, "worker", db.ServiceTypeSystemd)
	execer := &ttyExecer{ctx: context.Background(), s: server, sn: "worker", rw: &bytes.Buffer{}}
	err := execer.dockerCmdFunc([]string{"outdated"})
	if err == nil || !strings.Contains(err.Error(), `service "worker" is not a docker compose service`) {
		t.Fatalf("docker outdated non-docker error = %v", err)
	}
}

func TestRenderDockerOutdatedRowsTableAndJSON(t *testing.T) {
	rows := []svc.DockerOutdatedRow{
		{ServiceName: "web", ContainerName: "app", Image: "ghcr.io/acme/app:latest", RunningDigest: "sha256:old", LatestDigest: "sha256:new", Status: svc.DockerOutdatedUpdateAvailable},
		{ServiceName: "api", Status: svc.DockerOutdatedError, Reason: "scan failed"},
	}
	var out bytes.Buffer
	if err := renderDockerOutdatedRows(&out, "table", rows); err != nil {
		t.Fatalf("render table: %v", err)
	}
	if !strings.Contains(out.String(), "SERVICE") || !strings.Contains(out.String(), "UPDATE") || !strings.Contains(out.String(), "update") {
		t.Fatalf("table output = %q", out.String())
	}
	for _, unwanted := range []string{"RUNNING", "LATEST", "sha256:"} {
		if strings.Contains(out.String(), unwanted) {
			t.Fatalf("compact table output contains %q:\n%s", unwanted, out.String())
		}
	}
	if !strings.Contains(out.String(), "acme/app:latest") {
		t.Fatalf("compact image missing from table output = %q", out.String())
	}
	if !strings.Contains(out.String(), "api") || !strings.Contains(out.String(), "-") || !strings.Contains(out.String(), "error: scan failed") {
		t.Fatalf("service error row output = %q", out.String())
	}

	out.Reset()
	if err := renderDockerOutdatedRows(&out, "json", rows); err != nil {
		t.Fatalf("render json: %v", err)
	}
	var decoded []svc.DockerOutdatedRow
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("json output invalid: %v", err)
	}
	if len(decoded) != 2 || decoded[0].ServiceName != "web" || decoded[1].ServiceName != "api" {
		t.Fatalf("decoded rows = %#v", decoded)
	}
}

func TestRenderDockerOutdatedRowsPropagatesWriteErrors(t *testing.T) {
	writeErr := errors.New("write failed")
	rows := []svc.DockerOutdatedRow{
		{ServiceName: "web", ContainerName: "app", Image: "ghcr.io/acme/app:latest", Status: svc.DockerOutdatedCurrent},
	}
	if err := renderDockerOutdatedRows(failingWriter{err: writeErr}, "table", rows); !errors.Is(err, writeErr) {
		t.Fatalf("table write error = %v", err)
	}
	if err := renderDockerOutdatedRows(failingWriter{err: writeErr}, "json", rows); !errors.Is(err, writeErr) {
		t.Fatalf("json write error = %v", err)
	}
}

func TestDockerUpdateSnapshotsBeforeComposeUpdate(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"svc": {
				Name:             "svc",
				ServiceType:      db.ServiceTypeDockerCompose,
				ServiceRootZFS:   "tank/apps/svc",
				Generation:       3,
				LatestGeneration: 3,
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}

	var calls []string
	snapshotCreated := false
	server.zfsRunner = func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "snapshot":
			snapshotCreated = true
			return "", "", nil
		case "list":
			return "", "", nil
		default:
			return "", "unexpected zfs command: " + strings.Join(args, " "), errZFSCommandFailed
		}
	}
	oldDockerComposeUpdate := dockerComposeUpdate
	dockerComposeUpdate = func(_ *svc.DockerComposeService) error {
		if !snapshotCreated {
			t.Fatal("docker compose update ran before snapshot was created")
		}
		return nil
	}
	t.Cleanup(func() {
		dockerComposeUpdate = oldDockerComposeUpdate
	})

	execer := &ttyExecer{ctx: context.Background(), s: server, sn: "svc", rw: &bytes.Buffer{}}
	if err := execer.dockerUpdateCmdFunc(); err != nil {
		t.Fatalf("dockerUpdateCmdFunc: %v", err)
	}
	if len(calls) == 0 || !strings.HasPrefix(calls[0], "snapshot ") {
		t.Fatalf("zfs calls = %#v, want snapshot first", calls)
	}
}

func TestDockerUpdateCmdFuncFailsBeforeDockerForNonComposeService(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"svc": {Name: "svc", ServiceType: db.ServiceTypeSystemd},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, sn: "svc", rw: &out}

	err := execer.dockerUpdateCmdFunc()
	if err == nil || !strings.Contains(err.Error(), "not a docker compose service") {
		t.Fatalf("dockerUpdateCmdFunc error = %v", err)
	}
	if !strings.Contains(out.String(), `status=err`) {
		t.Fatalf("progress output = %q", out.String())
	}
}

func TestEventsCmdFuncReturnsWhenContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	execer := &ttyExecer{
		ctx: ctx,
		s:   newTestServer(t),
		sn:  "svc",
		rw:  &bytes.Buffer{},
	}
	if err := execer.eventsCmdFunc(cli.EventsFlags{}); err != nil {
		t.Fatalf("eventsCmdFunc: %v", err)
	}
}

func TestTSCmdFuncRejectsUnsupportedServicesAndMissingTSNet(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"svc": {Name: "svc", ServiceType: db.ServiceTypeSystemd},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	for _, name := range []string{SystemService, CatchService} {
		err := (&ttyExecer{s: server, sn: name}).tsCmdFunc(nil)
		if err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("tsCmdFunc(%s) error = %v", name, err)
		}
	}
	err := (&ttyExecer{s: server, sn: "svc"}).tsCmdFunc(nil)
	if err == nil || !strings.Contains(err.Error(), "not connected to tailscale") {
		t.Fatalf("tsCmdFunc missing tsnet error = %v", err)
	}
}

func TestRunRawTailscaleCmdReportsMissingSocketBeforeDownload(t *testing.T) {
	server := newTestServer(t)
	sv := (&db.Service{
		Name:  "svc",
		TSNet: &db.TailscaleNetwork{Version: "1.92.3"},
	}).View()
	err := (&ttyExecer{s: server, sn: "svc"}).runRawTailscaleCmd(sv, []string{"status"})
	if err == nil || !strings.Contains(err.Error(), "tailscaled socket not found") {
		t.Fatalf("runRawTailscaleCmd error = %v", err)
	}
}

func TestApplyTSUpdateCopiesBinaryPersistsVersionAndRestarts(t *testing.T) {
	server := newTestServer(t)
	const (
		service = "svc"
		current = "1.92.3"
		latest  = "1.94.0"
	)
	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatalf("mkdir tsd: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tsdDir, "tailscaled-"+latest), []byte("daemon-new"), 0o755); err != nil {
		t.Fatalf("write tailscaled: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tsdDir, "tailscale-"+latest), []byte("client-new"), 0o755); err != nil {
		t.Fatalf("write tailscale: %v", err)
	}
	if err := os.MkdirAll(server.serviceRunDir(service), 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			service: {
				Name:       service,
				Generation: 4,
				TSNet:      &db.TailscaleNetwork{Version: current},
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	logPath := filepath.Join(binDir, "systemctl.log")
	systemctl := filepath.Join(binDir, "systemctl")
	if err := os.WriteFile(systemctl, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SYSTEMCTL_LOG\"\n"), 0o755); err != nil {
		t.Fatalf("write systemctl shim: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SYSTEMCTL_LOG", logPath)

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  service,
		rw:  readWriter{Reader: strings.NewReader(""), Writer: &out},
	}
	if err := execer.applyTSUpdate(current, latest); err != nil {
		t.Fatalf("applyTSUpdate: %v", err)
	}

	assertFileContent(t, filepath.Join(server.serviceRunDir(service), "tailscaled"), "daemon-new")
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	svcData := dv.AsStruct().Services[service]
	if svcData.TSNet.Version != latest {
		t.Fatalf("persisted version = %q, want %q", svcData.TSNet.Version, latest)
	}
	refs := svcData.Artifacts[db.ArtifactTSBinary].Refs
	if refs["latest"] != filepath.Join(tsdDir, "tailscaled-"+latest) || refs[db.Gen(4)] != filepath.Join(tsdDir, "tailscaled-"+latest) {
		t.Fatalf("tailscale binary refs = %#v", refs)
	}
	logRaw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read systemctl log: %v", err)
	}
	if strings.TrimSpace(string(logRaw)) != "restart yeet-svc-ts.service" {
		t.Fatalf("systemctl log = %q", logRaw)
	}
	if !strings.Contains(out.String(), "Updated tailscale for svc: 1.92.3 -> 1.94.0") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestPrintLinesAndIfaceIPsReturnWriteErrors(t *testing.T) {
	writeErr := errors.New("write failed")
	if err := printLines(failingWriter{err: writeErr}, []string{"a"}); !errors.Is(err, writeErr) {
		t.Fatalf("printLines error = %v", err)
	}
	if err := printIfaceIPs(failingWriter{err: writeErr}, []ifaceIP{{IP: "10.0.0.1"}}); !errors.Is(err, writeErr) {
		t.Fatalf("printIfaceIPs error = %v", err)
	}
}

func TestApplyTSUpdatePropagatesRestartFailure(t *testing.T) {
	server := newTestServer(t)
	const latest = "1.94.0"
	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatalf("mkdir tsd: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tsdDir, "tailscaled-"+latest), []byte("daemon-new"), 0o755); err != nil {
		t.Fatalf("write tailscaled: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tsdDir, "tailscale-"+latest), []byte("client-new"), 0o755); err != nil {
		t.Fatalf("write tailscale: %v", err)
	}
	if err := os.MkdirAll(server.serviceRunDir("svc"), 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"svc": {Name: "svc", Generation: 1, TSNet: &db.TailscaleNetwork{Version: "1.92.3"}},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	systemctl := filepath.Join(binDir, "systemctl")
	if err := os.WriteFile(systemctl, []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
		t.Fatalf("write systemctl shim: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var out bytes.Buffer
	err := (&ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  "svc",
		rw:  readWriter{Reader: strings.NewReader(""), Writer: &out},
	}).applyTSUpdate("1.92.3", latest)
	if err == nil || !strings.Contains(err.Error(), "failed to restart tailscaled service") {
		t.Fatalf("applyTSUpdate error = %v", err)
	}
}
