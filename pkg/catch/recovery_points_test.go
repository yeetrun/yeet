// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestRecoveryPointsListVMAndServiceRootSnapshots(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	if _, _, err := server.cfg.DB.MutateService("app", func(_ *db.Data, svc *db.Service) error {
		svc.Name = "app"
		svc.ServiceType = db.ServiceTypeDockerCompose
		svc.ServiceRootZFS = "tank/apps/app"
		return nil
	}); err != nil {
		t.Fatalf("seed app: %v", err)
	}
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		if args[0] != "list" {
			t.Fatalf("unexpected zfs args: %v", args)
		}
		switch args[len(args)-1] {
		case "flash/yeet/vms/devbox/vm/d-abc/root":
			return "flash/yeet/vms/devbox/vm/d-abc/root@yeet-20260613T203100Z-vm-manual-g0\t1781382660\tcatch\tdevbox\tvm-manual\t0\tbefore upgrade\tdisk\ttrue\n", "", nil
		case "tank/apps/app":
			return strings.Join([]string{
				"tank/apps/app@yeet-20260613T203200Z-manual-g3\t1781382720\tcatch\tapp\tmanual\t3\tbefore deploy\tservice-root\tfalse",
				"tank/apps/app@yeet-20260613T203300Z-manual-g3\t1781382780\tadmin\tapp\tmanual\t3\twrong owner\tservice-root\tfalse",
				"tank/apps/app@yeet-20260613T203400Z-manual-g3\t1781382840\tcatch\tother\tmanual\t3\twrong service\tservice-root\tfalse",
				"tank/apps/app@manual\t1781382900\tcatch\tapp\tmanual\t3\tnot yeet\tservice-root\tfalse",
			}, "\n"), "", nil
		default:
			return "", "", nil
		}
	}

	points, err := server.listRecoveryPoints(context.Background(), "")
	if err != nil {
		t.Fatalf("listRecoveryPoints: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("points = %#v, want two recovery points", points)
	}
	if points[0].Service != "app" || points[0].ServiceType != string(db.ServiceTypeDockerCompose) {
		t.Fatalf("first point = %#v, want app service-root point sorted newest first", points[0])
	}
	if points[0].StorageKind != recoveryStorageServiceRoot ||
		points[0].Dataset != "tank/apps/app" ||
		points[0].ShortName != "yeet-20260613T203200Z-manual-g3" ||
		points[0].Created != time.Unix(1781382720, 0).UTC() ||
		points[0].Event != "manual" ||
		points[0].Generation == nil ||
		*points[0].Generation != 3 ||
		points[0].Comment != "before deploy" ||
		points[0].Mode != recoveryModeServiceRoot ||
		points[0].Protected ||
		points[0].Retention != "managed" ||
		!reflect.DeepEqual(points[0].Actions, []string{"inspect", "clone", "restore", "protect", "rm"}) {
		t.Fatalf("app recovery point = %#v, want rich service-root metadata", points[0])
	}
	if points[1].Service != "devbox" || points[1].StorageKind != recoveryStorageVMZVOL {
		t.Fatalf("second point = %#v, want devbox VM zvol point", points[1])
	}
	if points[1].Generation != nil ||
		points[1].Mode != recoveryModeDisk ||
		!points[1].Protected ||
		points[1].Retention != "protected" ||
		!reflect.DeepEqual(points[1].Actions, []string{"inspect", "clone", "restore", "unprotect"}) {
		t.Fatalf("VM recovery point = %#v, want protected disk snapshot metadata", points[1])
	}
}

func TestRecoveryPointsExposeFullVMCheckpointPaths(t *testing.T) {
	server := newTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	snapshotName := "flash/yeet/vms/devbox/vm/d-abc/root@yeet-20260613T203100Z-vm-manual-g0"
	statePath, memoryPath := seedFullVMCheckpointMetadata(t, root, "devbox", snapshotName)
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		if args[0] != "list" {
			t.Fatalf("unexpected zfs args: %v", args)
		}
		return snapshotName + "\t1781382660\tcatch\tdevbox\tvm-manual\t0\tcheckpoint\tfull\tfalse\n", "", nil
	}

	points, err := server.listRecoveryPoints(context.Background(), "devbox")
	if err != nil {
		t.Fatalf("listRecoveryPoints: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("points = %#v, want one recovery point", points)
	}
	point := points[0]
	if point.Mode != recoveryModeFull ||
		point.StatePath != statePath ||
		point.MemoryPath != memoryPath {
		t.Fatalf("full VM recovery point = %#v, want checkpoint paths %q %q", point, statePath, memoryPath)
	}
}

func TestVMRecoveryPointOmitsGenerationInJSON(t *testing.T) {
	point := recoveryPoint{
		Service:     "devbox",
		ServiceType: string(db.ServiceTypeVM),
		StorageKind: recoveryStorageVMZVOL,
		Dataset:     "flash/yeet/vms/devbox/vm/d-abc/root",
		Name:        "flash/yeet/vms/devbox/vm/d-abc/root@yeet-20260613T203100Z-vm-manual",
		ShortName:   "yeet-20260613T203100Z-vm-manual",
		Created:     time.Date(2026, 6, 13, 20, 31, 0, 0, time.UTC),
		CreatedBy:   "catch",
		Event:       "vm-manual",
		Mode:        recoveryModeDisk,
		Actions:     []string{"inspect", "clone", "restore", "protect", "rm"},
		Retention:   "managed",
	}

	got := renderRecoveryPointJSONForTest(t, point)
	if strings.Contains(got, `"generation":0`) || strings.Contains(got, `"generation"`) {
		t.Fatalf("VM recovery point JSON = %s, want omitted generation", got)
	}
}

func TestServiceRootRecoveryPointKeepsGenerationInJSON(t *testing.T) {
	point := recoveryPoint{
		Service:     "plex",
		ServiceType: string(db.ServiceTypeDockerCompose),
		StorageKind: recoveryStorageServiceRoot,
		Dataset:     "tank/apps/plex",
		Name:        "tank/apps/plex@yeet-20260613T203100Z-run-g4",
		ShortName:   "yeet-20260613T203100Z-run-g4",
		Created:     time.Date(2026, 6, 13, 20, 31, 0, 0, time.UTC),
		CreatedBy:   "catch",
		Event:       "run",
		Generation:  intPointer(4),
		Mode:        recoveryModeServiceRoot,
		Actions:     []string{"inspect", "clone", "restore", "protect", "rm"},
		Retention:   "managed",
	}

	got := renderRecoveryPointJSONForTest(t, point)
	if !strings.Contains(got, `"generation":4`) {
		t.Fatalf("service-root recovery point JSON = %s, want generation 4", got)
	}
}

func TestServiceRootRecoveryPointInspectTextShowsGeneration(t *testing.T) {
	point := recoveryPoint{
		Service:     "plex",
		ServiceType: string(db.ServiceTypeDockerCompose),
		StorageKind: recoveryStorageServiceRoot,
		Name:        "tank/apps/plex@yeet-20260613T203100Z-run-g4",
		ShortName:   "yeet-20260613T203100Z-run-g4",
		Created:     time.Date(2026, 6, 13, 20, 31, 0, 0, time.UTC),
		CreatedBy:   "catch",
		Event:       "run",
		Generation:  intPointer(4),
		Mode:        recoveryModeServiceRoot,
		Actions:     []string{"inspect", "clone", "restore", "protect", "rm"},
		Retention:   "managed",
	}

	var out bytes.Buffer
	if err := renderRecoveryPointInspect(&out, "text", point); err != nil {
		t.Fatalf("render recovery point text: %v", err)
	}
	if !strings.Contains(out.String(), "Generation: 4") {
		t.Fatalf("inspect output = %q, want service generation", out.String())
	}
}

func renderRecoveryPointJSONForTest(t *testing.T, point recoveryPoint) string {
	t.Helper()
	var out bytes.Buffer
	if err := renderRecoveryPointInspect(&out, "json", point); err != nil {
		t.Fatalf("render recovery point JSON: %v", err)
	}
	return out.String()
}

func seedFullVMCheckpointMetadata(t *testing.T, root string, service string, snapshotName string) (string, string) {
	t.Helper()
	dir := vmCheckpointDir(root, vmSnapshotShortName(snapshotName))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir checkpoint dir: %v", err)
	}
	statePath := filepath.Join(dir, "firecracker-state.bin")
	memoryPath := filepath.Join(dir, "memory.bin")
	if err := os.WriteFile(statePath, []byte("state"), 0o644); err != nil {
		t.Fatalf("write checkpoint state: %v", err)
	}
	if err := os.WriteFile(memoryPath, []byte("memory"), 0o644); err != nil {
		t.Fatalf("write checkpoint memory: %v", err)
	}
	metadata := map[string]string{
		"service":           service,
		"zvolSnapshot":      snapshotName,
		"firecrackerState":  statePath,
		"firecrackerMemory": memoryPath,
		"createdBy":         "catch",
		"createdAt":         time.Unix(1781382660, 0).UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatalf("marshal checkpoint metadata: %v", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), raw, 0o644); err != nil {
		t.Fatalf("write checkpoint metadata: %v", err)
	}
	return statePath, memoryPath
}

func TestSnapshotsCreateServiceRootManualSnapshot(t *testing.T) {
	server := newTestServer(t)
	if _, _, err := server.cfg.DB.MutateService("app", func(_ *db.Data, svc *db.Service) error {
		svc.Name = "app"
		svc.ServiceType = db.ServiceTypeDockerCompose
		svc.ServiceRootZFS = "tank/apps/app"
		return nil
	}); err != nil {
		t.Fatalf("seed app: %v", err)
	}
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "", "", nil
	}
	var out bytes.Buffer
	execer := &ttyExecer{ctx: context.Background(), s: server, rw: &out}
	if err := execer.snapshotsCmdFunc([]string{"create", "app", "--comment=manual note"}); err != nil {
		t.Fatalf("snapshots create: %v", err)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "snapshot ") || !strings.Contains(joined, "com.yeetrun:event=manual") || !strings.Contains(joined, "com.yeetrun:comment=manual note") {
		t.Fatalf("zfs calls = %#v, want manual service-root snapshot", calls)
	}
	if !strings.Contains(out.String(), "Recovery point: tank/apps/app@yeet-") {
		t.Fatalf("output = %q, want recovery point", out.String())
	}
}

func TestRecoveryPointsGlobalSkipsUnsupportedVMTargets(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "raw-vm", t.TempDir(), vmDiskBackendRaw)
	if _, _, err := server.cfg.DB.MutateService("app", func(_ *db.Data, svc *db.Service) error {
		svc.Name = "app"
		svc.ServiceType = db.ServiceTypeDockerCompose
		svc.ServiceRootZFS = "tank/apps/app"
		return nil
	}); err != nil {
		t.Fatalf("seed app: %v", err)
	}
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		if got := args[len(args)-1]; got != "tank/apps/app" {
			t.Fatalf("zfs list target = %q, want only supported app dataset", got)
		}
		return "tank/apps/app@yeet-20260613T203200Z-manual-g3\t1781382720\tcatch\tapp\tmanual\t3\tbefore deploy\tservice-root\tfalse\n", "", nil
	}

	points, err := server.listRecoveryPoints(context.Background(), "")
	if err != nil {
		t.Fatalf("listRecoveryPoints: %v", err)
	}
	if len(points) != 1 || points[0].Service != "app" {
		t.Fatalf("points = %#v, want only app recovery point", points)
	}
}

func TestRecoveryPointsExplicitUnsupportedVMTargetReturnsError(t *testing.T) {
	server := newTestServer(t)
	seedVMForResize(t, server, "raw-vm", t.TempDir(), vmDiskBackendRaw)
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		t.Fatalf("zfs runner called for unsupported VM target: %v", args)
		return "", "", nil
	}

	_, err := server.listRecoveryPoints(context.Background(), "raw-vm")
	if err == nil || !strings.Contains(err.Error(), "VM snapshot requires a ZFS zvol-backed VM") {
		t.Fatalf("listRecoveryPoints error = %v, want unsupported VM target", err)
	}
}

func TestRecoveryPointsSkipSnapshotsOutsideTargetDataset(t *testing.T) {
	server := newTestServer(t)
	if _, _, err := server.cfg.DB.MutateService("app", func(_ *db.Data, svc *db.Service) error {
		svc.Name = "app"
		svc.ServiceType = db.ServiceTypeDockerCompose
		svc.ServiceRootZFS = "tank/apps/app"
		return nil
	}); err != nil {
		t.Fatalf("seed app: %v", err)
	}
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "list" {
			return "other/dataset@yeet-20260613T203100Z-manual-g0\t1781382660\tcatch\tapp\tmanual\t0\twrong dataset\tservice-root\tfalse\n", "", nil
		}
		return "", "", nil
	}

	points, err := server.listRecoveryPoints(context.Background(), "app")
	if err != nil {
		t.Fatalf("listRecoveryPoints: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("points = %#v, want dataset-mismatched snapshot hidden", points)
	}
	if err := server.setRecoveryPointProtected(context.Background(), "app", "yeet-20260613T203100Z", true, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("setRecoveryPointProtected error = %v, want not found", err)
	}
	if strings.Contains(strings.Join(calls, "\n"), "set ") {
		t.Fatalf("calls = %#v, dataset-mismatched snapshot should not be mutated", calls)
	}
}

func TestSnapshotsProtectSkipsRetentionPrune(t *testing.T) {
	now := time.Unix(1781383000, 0).UTC()
	snaps := []listedSnapshot{
		{Name: "tank/app@yeet-old", Created: now.Add(-48 * time.Hour), CreatedBy: "catch", Service: "app", Protected: true},
		{Name: "tank/app@yeet-new", Created: now, CreatedBy: "catch", Service: "app"},
	}
	policy := effectivePolicy{Enabled: true, KeepLast: 1, MaxAge: 24 * time.Hour}
	prune := snapshotsToPrune(snaps, "app", policy, now, "")
	if len(prune) != 0 {
		t.Fatalf("prune = %#v, want protected old snapshot skipped", prune)
	}
}

func TestSnapshotsProtectSkipsKeepLastAccounting(t *testing.T) {
	now := time.Unix(1781383000, 0).UTC()
	snaps := []listedSnapshot{
		{Name: "tank/app@yeet-protected-newest", Created: now, CreatedBy: "catch", Service: "app", Protected: true},
		{Name: "tank/app@yeet-second", Created: now.Add(-time.Hour), CreatedBy: "catch", Service: "app"},
		{Name: "tank/app@yeet-third", Created: now.Add(-2 * time.Hour), CreatedBy: "catch", Service: "app"},
	}
	policy := effectivePolicy{Enabled: true, KeepLast: 1}
	prune := snapshotsToPrune(snaps, "app", policy, now, "")
	want := []string{"tank/app@yeet-third"}
	if !reflect.DeepEqual(prune, want) {
		t.Fatalf("prune = %#v, want %#v", prune, want)
	}
}

func TestResolveRecoveryPointSelector(t *testing.T) {
	points := []recoveryPoint{
		{Service: "devbox", Name: "tank/root@yeet-20260613T203100Z-vm-manual-g0", ShortName: "yeet-20260613T203100Z-vm-manual-g0"},
		{Service: "devbox", Name: "tank/root@yeet-20260613T203200Z-vm-manual-g0", ShortName: "yeet-20260613T203200Z-vm-manual-g0"},
	}

	got, err := resolveRecoveryPointSelector(points, "tank/root@yeet-20260613T203100Z-vm-manual-g0")
	if err != nil {
		t.Fatalf("resolveRecoveryPointSelector full name: %v", err)
	}
	if got.Name != points[0].Name {
		t.Fatalf("resolved full name = %#v, want first point", got)
	}

	got, err = resolveRecoveryPointSelector(points, "yeet-20260613T2032")
	if err != nil {
		t.Fatalf("resolveRecoveryPointSelector prefix: %v", err)
	}
	if got.Name != points[1].Name {
		t.Fatalf("resolved prefix = %#v, want second point", got)
	}
	if _, err := resolveRecoveryPointSelector(points, "yeet-20260613"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous error = %v", err)
	}
	if _, err := resolveRecoveryPointSelector(points, ""); err == nil || !strings.Contains(err.Error(), "selector is required") {
		t.Fatalf("empty selector error = %v", err)
	}
	if _, err := resolveRecoveryPointSelector(points, "missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("not found error = %v", err)
	}
}

func TestRecoveryPointActionsExposeCloneAndRestoreForZFSBackedPoints(t *testing.T) {
	for _, point := range []recoveryPoint{
		{Service: "app", StorageKind: recoveryStorageServiceRoot},
		{Service: "devbox", StorageKind: recoveryStorageVMZVOL},
	} {
		got := recoveryPointActions(point)
		want := []string{"inspect", "clone", "restore", "protect", "rm"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("recoveryPointActions(%#v) = %#v, want %#v", point, got, want)
		}
	}
}

func TestRecoveryPointActionsHideCloneAndRestoreForUnsupportedPoints(t *testing.T) {
	got := recoveryPointActions(recoveryPoint{Service: "raw-vm"})
	want := []string{"inspect", "protect", "rm"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recoveryPointActions unsupported = %#v, want %#v", got, want)
	}
}

func TestRenderRecoveryPointsTableAndJSON(t *testing.T) {
	points := []recoveryPoint{{
		Service: "devbox", ServiceType: "vm", StorageKind: recoveryStorageVMZVOL,
		Name: "tank/root@yeet-20260613T203100Z-vm-manual-g0", ShortName: "yeet-20260613T203100Z-vm-manual-g0",
		Created: time.Unix(1781382660, 0).UTC(), Event: "vm-manual", Mode: "disk",
		Protected: true, Comment: "before upgrade",
	}}
	var table bytes.Buffer
	if err := renderRecoveryPoints(&table, "table", points); err != nil {
		t.Fatalf("render table: %v", err)
	}
	for _, want := range []string{"SERVICE", "SNAPSHOT", "devbox", "yeet-20260613T203100Z-vm-manual-g0", "disk", "protected", "before upgrade"} {
		if !strings.Contains(table.String(), want) {
			t.Fatalf("table output missing %q:\n%s", want, table.String())
		}
	}
	var jsonOut bytes.Buffer
	if err := renderRecoveryPoints(&jsonOut, "json", points); err != nil {
		t.Fatalf("render json: %v", err)
	}
	if !strings.Contains(jsonOut.String(), `"service":"devbox"`) {
		t.Fatalf("json output = %s", jsonOut.String())
	}
}
