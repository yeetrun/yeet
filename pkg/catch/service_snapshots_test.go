// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
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
	if !got.Allows(snapshotEventRun) || !got.Allows(snapshotEventDockerUpdate) || !got.Allows(snapshotEventServiceRootMigration) {
		t.Fatalf("default events = %#v", got.Events)
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
		Generation: 12,
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

func TestCreateServiceSnapshotRetriesNameCollisionWithSuffix(t *testing.T) {
	oldSuffix := randomSnapshotSuffix
	randomSnapshotSuffix = func() (string, error) {
		return "a1b2c3", nil
	}
	t.Cleanup(func() {
		randomSnapshotSuffix = oldSuffix
	})

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
		Generation: 12,
		Now:        time.Date(2026, 5, 24, 18, 42, 33, 0, time.UTC),
	}

	name, err := createServiceSnapshot(context.Background(), runner, req)
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

func TestCreateServiceSnapshotDoesNotRetryNonCollisionError(t *testing.T) {
	oldSuffix := randomSnapshotSuffix
	randomSnapshotSuffix = func() (string, error) {
		t.Fatal("randomSnapshotSuffix should not be called")
		return "", nil
	}
	t.Cleanup(func() {
		randomSnapshotSuffix = oldSuffix
	})

	var calls [][]string
	runner := func(ctx context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string{}, args...))
		return "", "permission denied", errors.New("zfs failed")
	}
	req := snapshotCreateRequest{
		Service:    "svc",
		Dataset:    "tank/apps/svc",
		Event:      snapshotEventRun,
		Generation: 12,
		Now:        time.Date(2026, 5, 24, 18, 42, 33, 0, time.UTC),
	}

	_, err := createServiceSnapshot(context.Background(), runner, req)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("createServiceSnapshot error = %v, want permission denied", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %#v, want one call", calls)
	}
}

func TestSnapshotShortNameSanitizesEvent(t *testing.T) {
	req := snapshotCreateRequest{
		Event:      snapshotEvent("service root/migration"),
		Generation: 7,
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
	wantCall := []string{"list", "-H", "-p", "-t", "snapshot", "-o", "name,creation,com.yeetrun:created-by,com.yeetrun:service", "-s", "creation", "tank/apps/svc"}
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

func boolPtr(v bool) *bool {
	return &v
}

func intPtr(v int) *int {
	return &v
}
