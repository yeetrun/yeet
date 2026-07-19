// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestEffectiveSnapshotPolicyDefaults(t *testing.T) {
	got, err := effectiveSnapshotPolicy(nil, nil)
	if err != nil {
		t.Fatalf("effectiveSnapshotPolicy: %v", err)
	}
	if !got.Enabled || got.KeepLast != 5 || got.MaxAge != 7*24*time.Hour || !got.Required {
		t.Fatalf("policy = %#v", got)
	}
	if !got.Allows(snapshotEventRun) || !got.Allows(snapshotEventDockerUpdate) || !got.Allows(snapshotEventServiceRootMigration) || !got.Allows(snapshotEventServiceIdentityMigration) {
		t.Fatalf("default events = %#v", got.Events)
	}
}

func TestServiceIdentitySnapshotEventParsesAndHasStableOrder(t *testing.T) {
	events, err := effectiveSnapshotEvents([]string{"manual", "service-identity-migration", "run"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run", "service-identity-migration", "manual"}
	if got := effectiveSnapshotEventStrings(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("effectiveSnapshotEventStrings = %#v, want %#v", got, want)
	}
}

func TestServiceIdentitySnapshotIsMandatoryAndRecordedAtJournalSeal(t *testing.T) {
	stateRoot := t.TempDir()
	serviceRoot := filepath.Join(t.TempDir(), "api")
	if err := os.MkdirAll(serviceRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	j, err := createServiceIdentityJournal(stateRoot, serviceIdentityJournalHeader{
		Version: 1, ID: "tx", Service: "api", Root: serviceRoot,
		TargetIdentity: db.ServiceIdentity{UID: 1000, GID: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	var calls [][]string
	server := &Server{zfsRunner: func(_ context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string(nil), args...))
		return "", "", nil
	}}
	service := &db.Service{Name: "api", ServiceRoot: serviceRoot, ServiceRootZFS: "tank/api", Generation: 3,
		SnapshotPolicy: &db.SnapshotPolicy{Enabled: boolPtr(false)}}

	snapshot, err := server.createServiceIdentityMigrationSnapshot(context.Background(), service, j)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == "" || len(calls) != 1 {
		t.Fatalf("snapshot = %q, calls = %#v; want mandatory snapshot", snapshot, calls)
	}
	contents, err := loadServiceIdentityJournal(j.Path())
	if err != nil {
		t.Fatal(err)
	}
	last := contents.Phases[len(contents.Phases)-1]
	if last.Phase != serviceIdentityJournalSealPhase || last.ZFSSnapshot != snapshot {
		t.Fatalf("seal phase = %#v, want snapshot %q", last, snapshot)
	}
}

func TestEffectiveSnapshotPolicyServiceOverride(t *testing.T) {
	enabled := false
	keep := 3
	required := false
	got, err := effectiveSnapshotPolicy(&db.SnapshotPolicy{Enabled: boolPtr(true), KeepLast: intPtr(8), MaxAge: "14d"}, &db.SnapshotPolicy{
		Enabled:  &enabled,
		KeepLast: &keep,
		MaxAge:   "72h",
		Events:   []string{"run"},
		Required: &required,
	})
	if err != nil {
		t.Fatalf("effectiveSnapshotPolicy: %v", err)
	}
	if got.Enabled || got.KeepLast != 3 || got.MaxAge != 72*time.Hour || got.Required {
		t.Fatalf("policy = %#v", got)
	}
	if !got.Allows(snapshotEventRun) || got.Allows(snapshotEventDockerUpdate) {
		t.Fatalf("events = %#v", got.Events)
	}
}

func TestEffectiveSnapshotPolicyServerValuesInheritedByNilServiceOverride(t *testing.T) {
	got, err := effectiveSnapshotPolicy(&db.SnapshotPolicy{
		Enabled:  boolPtr(false),
		KeepLast: intPtr(9),
		MaxAge:   "48h",
		Events:   []string{"docker-update"},
		Required: boolPtr(false),
	}, &db.SnapshotPolicy{})
	if err != nil {
		t.Fatalf("effectiveSnapshotPolicy: %v", err)
	}
	if got.Enabled || got.KeepLast != 9 || got.MaxAge != 48*time.Hour || got.Required {
		t.Fatalf("policy = %#v", got)
	}
	if got.Allows(snapshotEventRun) || !got.Allows(snapshotEventDockerUpdate) {
		t.Fatalf("events = %#v", got.Events)
	}
}

func TestEffectiveSnapshotPolicyRejectsInvalidEvents(t *testing.T) {
	_, err := effectiveSnapshotPolicy(nil, &db.SnapshotPolicy{Events: []string{"bad-event"}})
	if err == nil || !strings.Contains(err.Error(), `invalid snapshot event "bad-event"`) {
		t.Fatalf("effectiveSnapshotPolicy error = %v, want invalid event", err)
	}
}

func TestEffectiveSnapshotPolicyRejectsEnabledKeepLastBelowOne(t *testing.T) {
	_, err := effectiveSnapshotPolicy(nil, &db.SnapshotPolicy{KeepLast: intPtr(0)})
	if err == nil || !strings.Contains(err.Error(), "snapshot keep-last must be at least 1") {
		t.Fatalf("effectiveSnapshotPolicy error = %v, want keep-last validation", err)
	}

	got, err := effectiveSnapshotPolicy(nil, &db.SnapshotPolicy{Enabled: boolPtr(false), KeepLast: intPtr(0)})
	if err != nil {
		t.Fatalf("effectiveSnapshotPolicy disabled: %v", err)
	}
	if got.Enabled || got.KeepLast != 0 {
		t.Fatalf("disabled policy = %#v, want disabled keep-last 0", got)
	}
}

func TestParseSnapshotMaxAge(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr string
	}{
		{in: "7d", want: 7 * 24 * time.Hour},
		{in: "72h", want: 72 * time.Hour},
		{in: "0", wantErr: "must be positive"},
		{in: "-1d", wantErr: "must be positive"},
		{in: "106752d", wantErr: "invalid snapshot max age"},
		{in: "bad", wantErr: "invalid snapshot max age"},
	}
	for _, tt := range tests {
		got, err := parseSnapshotMaxAge(tt.in)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("parseSnapshotMaxAge(%q) error = %v, want %q", tt.in, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Fatalf("parseSnapshotMaxAge(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
}

func TestCreateServiceSnapshotCommand(t *testing.T) {
	var calls [][]string
	runner := func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string{}, args...))
		return "", "", nil
	}
	req := snapshotCreateRequest{
		Service:    "svc-a",
		Dataset:    "tank/apps/svc-a",
		Event:      snapshotEventDockerUpdate,
		Generation: intPointer(12),
		Now:        time.Date(2026, 5, 24, 18, 42, 33, 0, time.UTC),
	}
	name, err := createServiceSnapshot(context.Background(), runner, req)
	if err != nil {
		t.Fatalf("createServiceSnapshot: %v", err)
	}
	if name != "tank/apps/svc-a@yeet-20260524T184233Z-docker-update-g12" {
		t.Fatalf("snapshot = %q", name)
	}
	wantPrefix := []string{
		"snapshot",
		"-o", "com.yeetrun:created-by=catch",
		"-o", "com.yeetrun:service=svc-a",
		"-o", "com.yeetrun:event=docker-update",
		"-o", "com.yeetrun:generation=12",
		"-o", "com.yeetrun:policy-version=1",
	}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0][:len(wantPrefix)], wantPrefix) {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestCreateServiceSnapshotCommandWithCommentAndCheckpoint(t *testing.T) {
	var calls [][]string
	runner := func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string{}, args...))
		return "", "", nil
	}
	req := snapshotCreateRequest{
		Service:    "devbox",
		Dataset:    "flash/yeet/vms/devbox/root",
		Event:      snapshotEventVMManual,
		Generation: intPointer(4),
		Now:        time.Date(2026, 6, 13, 18, 0, 0, 0, time.UTC),
		Comment:    " before upgrade ",
		Checkpoint: " disk ",
	}

	name, err := createServiceSnapshot(context.Background(), runner, req)
	if err != nil {
		t.Fatalf("createServiceSnapshot: %v", err)
	}
	if name != "flash/yeet/vms/devbox/root@yeet-20260613T180000Z-vm-manual-g4" {
		t.Fatalf("snapshot name = %q", name)
	}
	want := []string{
		"snapshot",
		"-o", "com.yeetrun:created-by=catch",
		"-o", "com.yeetrun:service=devbox",
		"-o", "com.yeetrun:event=vm-manual",
		"-o", "com.yeetrun:generation=4",
		"-o", "com.yeetrun:policy-version=1",
		"-o", "com.yeetrun:comment=before upgrade",
		"-o", "com.yeetrun:checkpoint=disk",
		"flash/yeet/vms/devbox/root@yeet-20260613T180000Z-vm-manual-g4",
	}
	if !reflect.DeepEqual(calls, [][]string{want}) {
		t.Fatalf("zfs args = %#v, want %#v", calls, [][]string{want})
	}
}

func TestCreateServiceSnapshotCommandWithoutGenerationOmitsGenerationProperty(t *testing.T) {
	var calls [][]string
	runner := func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string{}, args...))
		return "", "", nil
	}
	req := snapshotCreateRequest{
		Service:    "devbox",
		Dataset:    "flash/yeet/vms/devbox/root",
		Event:      snapshotEventVMManual,
		Now:        time.Date(2026, 6, 13, 20, 31, 0, 0, time.UTC),
		Checkpoint: "disk",
	}

	name, err := createServiceSnapshot(context.Background(), runner, req)
	if err != nil {
		t.Fatalf("createServiceSnapshot: %v", err)
	}
	if name != "flash/yeet/vms/devbox/root@yeet-20260613T203100Z-vm-manual" {
		t.Fatalf("snapshot name = %q", name)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %#v, want one zfs call", calls)
	}
	for _, arg := range calls[0] {
		if strings.Contains(arg, "com.yeetrun:generation=") {
			t.Fatalf("zfs args = %#v, want generation property omitted", calls[0])
		}
	}
}

func TestCreateServiceSnapshotRetriesNameCollisionWithSuffix(t *testing.T) {
	var calls [][]string
	runner := func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string{}, args...))
		if len(calls) == 1 {
			return "", "cannot create snapshot 'tank/apps/svc@yeet-20260524T184233Z-run-g12': dataset already exists", errZFSCommandFailed
		}
		return "", "", nil
	}
	req := snapshotCreateRequest{
		Service:    "svc",
		Dataset:    "tank/apps/svc",
		Event:      snapshotEventRun,
		Generation: intPointer(12),
		Now:        time.Date(2026, 5, 24, 18, 42, 33, 0, time.UTC),
	}

	name, err := createServiceSnapshotWithSuffix(context.Background(), runner, req, staticSnapshotSuffix("a1b2c3"))
	if err != nil {
		t.Fatalf("createServiceSnapshot: %v", err)
	}
	if name != "tank/apps/svc@yeet-20260524T184233Z-run-g12-a1b2c3" {
		t.Fatalf("snapshot = %q", name)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %#v", calls)
	}
	if got := calls[0][len(calls[0])-1]; got != "tank/apps/svc@yeet-20260524T184233Z-run-g12" {
		t.Fatalf("first snapshot target = %q", got)
	}
	if got := calls[1][len(calls[1])-1]; got != name {
		t.Fatalf("second snapshot target = %q, want %q", got, name)
	}
}

func TestCreateServiceSnapshotRetriesSnapshotAlreadyExistsVariant(t *testing.T) {
	var calls [][]string
	runner := func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string{}, args...))
		if len(calls) == 1 {
			return "", "snapshot tank/apps/svc@yeet-20260524T184233Z-run-g12 already exists", errZFSCommandFailed
		}
		return "", "", nil
	}
	req := snapshotCreateRequest{
		Service:    "svc",
		Dataset:    "tank/apps/svc",
		Event:      snapshotEventRun,
		Generation: intPointer(12),
		Now:        time.Date(2026, 5, 24, 18, 42, 33, 0, time.UTC),
	}

	name, err := createServiceSnapshotWithSuffix(context.Background(), runner, req, staticSnapshotSuffix("d4e5f6"))
	if err != nil {
		t.Fatalf("createServiceSnapshot: %v", err)
	}
	if name != "tank/apps/svc@yeet-20260524T184233Z-run-g12-d4e5f6" {
		t.Fatalf("snapshot = %q", name)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestCreateServiceSnapshotDoesNotRetryNonCollisionError(t *testing.T) {
	var calls [][]string
	runner := func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string{}, args...))
		return "", "permission denied", errors.New("zfs failed")
	}
	req := snapshotCreateRequest{
		Service:    "svc",
		Dataset:    "tank/apps/svc",
		Event:      snapshotEventRun,
		Generation: intPointer(12),
		Now:        time.Date(2026, 5, 24, 18, 42, 33, 0, time.UTC),
	}

	_, err := createServiceSnapshotWithSuffix(context.Background(), runner, req, failSnapshotSuffix(t))
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("createServiceSnapshot error = %v, want permission denied", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %#v, want one call", calls)
	}
}

func TestCreateServiceSnapshotDoesNotRetryNearCollisionError(t *testing.T) {
	var calls [][]string
	runner := func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string{}, args...))
		return "", "filesystem tank/apps/svc already exists", errZFSCommandFailed
	}
	req := snapshotCreateRequest{
		Service:    "svc",
		Dataset:    "tank/apps/svc",
		Event:      snapshotEventRun,
		Generation: intPointer(12),
		Now:        time.Date(2026, 5, 24, 18, 42, 33, 0, time.UTC),
	}

	_, err := createServiceSnapshotWithSuffix(context.Background(), runner, req, failSnapshotSuffix(t))
	if err == nil || !strings.Contains(err.Error(), "filesystem tank/apps/svc already exists") {
		t.Fatalf("createServiceSnapshot error = %v, want filesystem already exists", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %#v, want one call", calls)
	}
}

func TestSnapshotShortNameCanOmitGeneration(t *testing.T) {
	req := snapshotCreateRequest{
		Event: snapshotEventVMManual,
		Now:   time.Date(2026, 6, 13, 20, 31, 0, 0, time.UTC),
	}
	got := snapshotShortName(req)
	want := "yeet-20260613T203100Z-vm-manual"
	if got != want {
		t.Fatalf("snapshotShortName = %q, want %q", got, want)
	}
}

func TestSnapshotShortNameKeepsServiceGeneration(t *testing.T) {
	req := snapshotCreateRequest{
		Event:      snapshotEventRun,
		Generation: intPointer(4),
		Now:        time.Date(2026, 6, 13, 20, 31, 0, 0, time.UTC),
	}
	got := snapshotShortName(req)
	want := "yeet-20260613T203100Z-run-g4"
	if got != want {
		t.Fatalf("snapshotShortName = %q, want %q", got, want)
	}
}

func TestSnapshotShortNameSanitizesEvent(t *testing.T) {
	req := snapshotCreateRequest{
		Event:      snapshotEvent("service root/migration"),
		Generation: intPointer(7),
		Now:        time.Date(2026, 5, 24, 18, 42, 33, 0, time.UTC),
	}
	got := snapshotShortName(req)
	want := "yeet-20260524T184233Z-service_root_migration-g7"
	if got != want {
		t.Fatalf("snapshotShortName = %q, want %q", got, want)
	}
}

func TestPruneSnapshotSelection(t *testing.T) {
	now := time.Date(2026, 5, 24, 20, 0, 0, 0, time.UTC)
	snaps := []listedSnapshot{
		{Name: "tank/apps/svc@yeet-old", Created: now.Add(-8 * 24 * time.Hour), CreatedBy: "catch", Service: "svc"},
		{Name: "tank/apps/svc@yeet-other-service-old", Created: now.Add(-9 * 24 * time.Hour), CreatedBy: "catch", Service: "other"},
		{Name: "tank/apps/svc@manual", Created: now.Add(-30 * 24 * time.Hour), CreatedBy: "", Service: ""},
		{Name: "tank/apps/svc@yeet-new-1", Created: now.Add(-1 * time.Hour), CreatedBy: "catch", Service: "svc"},
		{Name: "tank/apps/svc@yeet-new-2", Created: now.Add(-2 * time.Hour), CreatedBy: "catch", Service: "svc"},
		{Name: "tank/apps/svc@yeet-new-3", Created: now.Add(-3 * time.Hour), CreatedBy: "catch", Service: "svc"},
		{Name: "tank/apps/svc@yeet-new-4", Created: now.Add(-4 * time.Hour), CreatedBy: "catch", Service: "svc"},
		{Name: "tank/apps/svc@yeet-new-5", Created: now.Add(-5 * time.Hour), CreatedBy: "catch", Service: "svc"},
		{Name: "tank/apps/svc@yeet-new-6", Created: now.Add(-6 * time.Hour), CreatedBy: "catch", Service: "svc"},
	}
	got := snapshotsToPrune(snaps, "svc", effectivePolicy{KeepLast: 5, MaxAge: 7 * 24 * time.Hour}, now, "tank/apps/svc@yeet-new-1")
	want := []string{"tank/apps/svc@yeet-old", "tank/apps/svc@yeet-new-6"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshotsToPrune = %#v, want %#v", got, want)
	}
}

func TestPruneSnapshotSelectionUsesNameTieBreaker(t *testing.T) {
	now := time.Date(2026, 5, 24, 20, 0, 0, 0, time.UTC)
	created := now.Add(-time.Hour)
	snaps := []listedSnapshot{
		{Name: "tank/apps/svc@yeet-c", Created: created, CreatedBy: "catch", Service: "svc"},
		{Name: "tank/apps/svc@yeet-a", Created: created, CreatedBy: "catch", Service: "svc"},
		{Name: "tank/apps/svc@yeet-b", Created: created, CreatedBy: "catch", Service: "svc"},
	}

	got := snapshotsToPrune(snaps, "svc", effectivePolicy{KeepLast: 2, MaxAge: 7 * 24 * time.Hour}, now, "")
	want := []string{"tank/apps/svc@yeet-c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshotsToPrune = %#v, want %#v", got, want)
	}
}

func TestPruneSnapshotSelectionKeepsNewestWhenCurrentEmpty(t *testing.T) {
	now := time.Date(2026, 5, 24, 20, 0, 0, 0, time.UTC)
	snaps := []listedSnapshot{
		{Name: "tank/apps/svc@yeet-oldest", Created: now.Add(-10 * time.Hour), CreatedBy: "catch", Service: "svc"},
		{Name: "tank/apps/svc@yeet-newest", Created: now.Add(-8 * time.Hour), CreatedBy: "catch", Service: "svc"},
	}

	got := snapshotsToPrune(snaps, "svc", effectivePolicy{KeepLast: 1, MaxAge: time.Hour}, now, "")
	want := []string{"tank/apps/svc@yeet-oldest"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshotsToPrune = %#v, want %#v", got, want)
	}
}

func TestListServiceSnapshotsCommandAndParse(t *testing.T) {
	var calls [][]string
	runner := func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string{}, args...))
		return strings.Join([]string{
			"tank/apps/svc@yeet-one\t1779664800\tcatch\tsvc",
			"tank/apps/svc@manual\t1779668400\t-\t-",
		}, "\n"), "", nil
	}

	got, err := listServiceSnapshots(context.Background(), runner, "tank/apps/svc")
	if err != nil {
		t.Fatalf("listServiceSnapshots: %v", err)
	}
	wantCall := []string{"list", "-H", "-p", "-t", "snapshot", "-o", "name,creation,com.yeetrun:created-by,com.yeetrun:service,com.yeetrun:event,com.yeetrun:generation,com.yeetrun:comment,com.yeetrun:checkpoint,com.yeetrun:protected", "-s", "creation", "tank/apps/svc"}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], wantCall) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCall)
	}
	want := []listedSnapshot{
		{Name: "tank/apps/svc@yeet-one", Created: time.Unix(1779664800, 0).UTC(), CreatedBy: "catch", Service: "svc"},
		{Name: "tank/apps/svc@manual", Created: time.Unix(1779668400, 0).UTC()},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshots = %#v, want %#v", got, want)
	}
}

func TestParseListedSnapshotsNineFieldOptionalProperties(t *testing.T) {
	got, err := parseListedSnapshots("tank/apps/svc@yeet-one\t1779664800\tcatch\tsvc\t-\t-\t-\t-\t-\n")
	if err != nil {
		t.Fatalf("parseListedSnapshots: %v", err)
	}
	want := []listedSnapshot{
		{Name: "tank/apps/svc@yeet-one", Created: time.Unix(1779664800, 0).UTC(), CreatedBy: "catch", Service: "svc"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshots = %#v, want %#v", got, want)
	}
}

func TestParseListedSnapshotsParsesGenerationProperty(t *testing.T) {
	got, err := parseListedSnapshots("tank/apps/svc@yeet-one\t1779664800\tcatch\tsvc\trun\t4\t-\tservice-root\tfalse\n")
	if err != nil {
		t.Fatalf("parseListedSnapshots: %v", err)
	}
	if len(got) != 1 || got[0].Generation == nil || *got[0].Generation != 4 {
		t.Fatalf("snapshots = %#v, want generation 4", got)
	}
}

func TestListServiceSnapshotsWrapsRunnerFailure(t *testing.T) {
	runner := func(ctx context.Context, args ...string) (string, string, error) {
		return "", "dataset does not exist", errors.New("zfs failed")
	}

	_, err := listServiceSnapshots(context.Background(), runner, "tank/apps/svc")
	want := "zfs list snapshots tank/apps/svc failed: dataset does not exist"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("listServiceSnapshots error = %v, want %q", err, want)
	}
}

func TestDestroySnapshotCommand(t *testing.T) {
	var calls [][]string
	runner := func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string{}, args...))
		return "", "", nil
	}

	if err := destroySnapshot(context.Background(), runner, "tank/apps/svc@yeet-old"); err != nil {
		t.Fatalf("destroySnapshot: %v", err)
	}
	want := [][]string{{"destroy", "tank/apps/svc@yeet-old"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestDestroySnapshotWrapsRunnerFailure(t *testing.T) {
	runner := func(ctx context.Context, args ...string) (string, string, error) {
		return "", "permission denied", errors.New("zfs failed")
	}

	err := destroySnapshot(context.Background(), runner, "tank/apps/svc@yeet-old")
	want := "zfs destroy tank/apps/svc@yeet-old failed: permission denied"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("destroySnapshot error = %v, want %q", err, want)
	}
}

func boolPtr(v bool) *bool {
	return &v
}

func intPtr(v int) *int {
	return &v
}

func staticSnapshotSuffix(suffix string) func() (string, error) {
	return func() (string, error) {
		return suffix, nil
	}
}

func failSnapshotSuffix(t *testing.T) func() (string, error) {
	return func() (string, error) {
		t.Fatal("snapshot suffix should not be requested")
		return "", nil
	}
}
