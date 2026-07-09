// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
	"tailscale.com/tailcfg"
)

const (
	testUbuntuVMPayload         = "vm://ubuntu/26.04"
	testUbuntuVMImageVersion    = "ubuntu-26.04-amd64-v99"
	testNixOSVMPayload          = "vm://nixos/26.05"
	testNixOSVMImageVersion     = "nixos-26.05-amd64-v99"
	testDefaultVMImageManifest  = "https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-latest/manifest.json"
	testNixOSVMImageManifestURL = "https://github.com/yeetrun/yeet-vm-images/releases/download/nixos-26.05-amd64-latest/manifest.json"
)

func TestStatusesServiceNamesByTypeFiltersAndSorts(t *testing.T) {
	services := map[string]*db.Service{
		"worker": {Name: "worker", ServiceType: db.ServiceTypeSystemd},
		"api":    {Name: "api", ServiceType: db.ServiceTypeDockerCompose},
		"cache":  {Name: "cache", ServiceType: db.ServiceTypeDockerCompose},
		"unset":  {Name: "unset"},
	}

	got := serviceNamesByType(services, db.ServiceTypeDockerCompose)
	want := []string{"api", "cache"}
	if !slices.Equal(got, want) {
		t.Fatalf("serviceNamesByType = %v, want %v", got, want)
	}
}

func TestDockerComposeOutdatedAllScansWithBoundedConcurrency(t *testing.T) {
	serviceCount := dockerComposeOutdatedAllWorkerLimit + 3
	serviceNames := make([]string, 0, serviceCount)
	for i := 0; i < serviceCount; i++ {
		serviceNames = append(serviceNames, fmt.Sprintf("svc-%02d", i))
	}

	started := make(chan string, serviceCount)
	release := make(chan struct{})
	var active atomic.Int32
	var maxActive atomic.Int32

	scan := func(ctx context.Context, sn string, opts svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error) {
		if opts.IncludeInternal {
			t.Errorf("IncludeInternal = true, want false for host-wide scan")
		}
		current := active.Add(1)
		recordDockerOutdatedMaxActive(&maxActive, current)
		started <- sn
		select {
		case <-release:
		case <-ctx.Done():
			active.Add(-1)
			return nil, ctx.Err()
		}
		active.Add(-1)
		return []svc.DockerOutdatedRow{{
			ServiceName:   sn,
			ContainerName: "app",
			Image:         "ghcr.io/acme/" + sn + ":latest",
			Status:        svc.DockerOutdatedUpdateAvailable,
		}}, nil
	}

	type result struct {
		rows []svc.DockerOutdatedRow
		err  error
	}
	done := make(chan result, 1)
	go func() {
		rows, err := dockerComposeOutdatedAll(context.Background(), serviceNames, scan)
		done <- result{rows: rows, err: err}
	}()

	for i := 0; i < dockerComposeOutdatedAllWorkerLimit; i++ {
		waitForDockerOutdatedStart(t, started)
	}
	select {
	case sn := <-started:
		t.Fatalf("scan for %q started before worker limit released", sn)
	default:
	}
	select {
	case got := <-done:
		t.Fatalf("scan returned before release: rows=%#v err=%v", got.rows, got.err)
	default:
	}

	close(release)
	got := waitForDockerOutdatedResult(t, done)
	if got.err != nil {
		t.Fatalf("dockerComposeOutdatedAll: %v", got.err)
	}
	if gotMax := int(maxActive.Load()); gotMax != dockerComposeOutdatedAllWorkerLimit {
		t.Fatalf("max active scans = %d, want %d", gotMax, dockerComposeOutdatedAllWorkerLimit)
	}
	if len(got.rows) != serviceCount {
		t.Fatalf("rows = %d, want %d", len(got.rows), serviceCount)
	}
	for i, row := range got.rows {
		if row.ServiceName != serviceNames[i] {
			t.Fatalf("row %d service = %q, want %q", i, row.ServiceName, serviceNames[i])
		}
	}
}

func TestDockerComposeOutdatedAllPreservesErrorRowsAndSorts(t *testing.T) {
	serviceNames := []string{"zeta", "bad", "alpha"}
	scan := func(_ context.Context, sn string, _ svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error) {
		if sn == "bad" {
			return nil, errors.New("registry unavailable")
		}
		return []svc.DockerOutdatedRow{{
			ServiceName:   sn,
			ContainerName: "app",
			Image:         "ghcr.io/acme/" + sn + ":latest",
			Status:        svc.DockerOutdatedUpdateAvailable,
		}}, nil
	}

	rows, err := dockerComposeOutdatedAll(context.Background(), serviceNames, scan)
	if err != nil {
		t.Fatalf("dockerComposeOutdatedAll: %v", err)
	}
	gotServices := []string{rows[0].ServiceName, rows[1].ServiceName, rows[2].ServiceName}
	if !reflect.DeepEqual(gotServices, []string{"alpha", "bad", "zeta"}) {
		t.Fatalf("service order = %v", gotServices)
	}
	if rows[1].Status != svc.DockerOutdatedError || rows[1].Reason != "registry unavailable" {
		t.Fatalf("error row = %#v", rows[1])
	}
}

func TestDockerComposeOutdatedAllContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan string, 2)
	scan := func(ctx context.Context, sn string, _ svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error) {
		started <- sn
		<-ctx.Done()
		return nil, ctx.Err()
	}

	type result struct {
		rows []svc.DockerOutdatedRow
		err  error
	}
	done := make(chan result, 1)
	go func() {
		rows, err := dockerComposeOutdatedAll(ctx, []string{"alpha", "beta"}, scan)
		done <- result{rows: rows, err: err}
	}()

	waitForDockerOutdatedStart(t, started)
	cancel()
	got := waitForDockerOutdatedResult(t, done)
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", got.err)
	}
	if len(got.rows) != 0 {
		t.Fatalf("rows = %#v, want none", got.rows)
	}
}

func TestDockerComposeOutdatedAllEmptyServices(t *testing.T) {
	called := false
	rows, err := dockerComposeOutdatedAll(context.Background(), nil, func(context.Context, string, svc.DockerOutdatedOptions) ([]svc.DockerOutdatedRow, error) {
		called = true
		return nil, nil
	})
	if err != nil {
		t.Fatalf("dockerComposeOutdatedAll: %v", err)
	}
	if called {
		t.Fatal("scan called for empty service list")
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %#v, want none", rows)
	}
}

func recordDockerOutdatedMaxActive(maxActive *atomic.Int32, current int32) {
	for {
		previous := maxActive.Load()
		if current <= previous || maxActive.CompareAndSwap(previous, current) {
			return
		}
	}
}

func waitForDockerOutdatedStart(t *testing.T, started <-chan string) string {
	t.Helper()
	select {
	case sn := <-started:
		return sn
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for docker outdated scan to start")
		return ""
	}
}

func waitForDockerOutdatedResult[T any](t *testing.T, done <-chan T) T {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for docker outdated scan result")
		var zero T
		return zero
	}
}

func TestStatusesDockerComposeRunningDecision(t *testing.T) {
	if !dockerComposeStatusRunning(svc.DockerComposeStatus{
		"api":    svc.StatusStopped,
		"worker": svc.StatusRunning,
	}) {
		t.Fatalf("expected one running component to make the service running")
	}
	if dockerComposeStatusRunning(svc.DockerComposeStatus{
		"api":    svc.StatusStopped,
		"worker": svc.StatusUnknown,
	}) {
		t.Fatalf("expected no running components to make the service stopped")
	}
}

func TestCatchNodeIdentityValidationRules(t *testing.T) {
	tests := []struct {
		name       string
		serverTags []string
		wantErr    bool
	}{
		{
			name:       "tagged caller allowed by overlapping tagged server",
			serverTags: []string{"tag:prod", "tag:web"},
		},
		{
			name:       "tagged caller allowed by tagged server ACL without overlap",
			serverTags: []string{"tag:prod"},
		},
		{
			name:       "untagged caller allowed when server is tagged",
			serverTags: []string{"tag:prod"},
		},
		{
			name:    "untagged server rejected for same user",
			wantErr: true,
		},
		{
			name:    "untagged server rejected for different user",
			wantErr: true,
		},
		{
			name:    "untagged server rejected for tagged same user",
			wantErr: true,
		},
		{
			name:    "untagged server rejected for tagged different user",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCatchNodeIdentity(tt.serverTags)
			if tt.wantErr {
				if !errors.Is(err, errUnauthorized) {
					t.Fatalf("validateCallerIdentity error = %v, want errUnauthorized", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateCallerIdentity error = %v, want nil", err)
			}
		})
	}
}

func TestServiceDirectoryPlanUsesRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "custom-api")
	got := serviceDirectoryPlan(root)
	want := []string{
		filepath.Join(root, "bin"),
		filepath.Join(root, "data"),
		filepath.Join(root, "env"),
		filepath.Join(root, "run"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("serviceDirectoryPlan = %v, want %v", got, want)
	}
}

func TestServiceRootDirUsesDBServiceRoot(t *testing.T) {
	server := newTestServer(t)
	customRoot := filepath.Join(t.TempDir(), "custom-api")
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"api": {Name: "api", ServiceRoot: customRoot},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}

	got, err := server.serviceRootDir("api")
	if err != nil {
		t.Fatalf("serviceRootDir: %v", err)
	}
	if got != customRoot {
		t.Fatalf("serviceRootDir = %q, want %q", got, customRoot)
	}

	got, err = server.serviceRootDir("missing")
	if err != nil {
		t.Fatalf("serviceRootDir missing: %v", err)
	}
	wantDefault := filepath.Join(server.cfg.ServicesRoot, "missing")
	if got != wantDefault {
		t.Fatalf("serviceRootDir missing = %q, want %q", got, wantDefault)
	}
}

func TestPrepareServiceRootForInstallZFSExistingSameDataset(t *testing.T) {
	server := newTestServer(t)
	mountpoint := filepath.Join(t.TempDir(), "svc")
	if err := os.MkdirAll(mountpoint, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mountpoint, "existing-service-file"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	server.zfsRunner = fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/apps/svc": {Mountpoint: mountpoint, Exists: true},
	}).Run
	addTestServices(t, server, db.Service{Name: "svc", ServiceRoot: "/old/mount", ServiceRootZFS: "tank/apps/svc"})
	got, err := server.prepareServiceRootForInstall("svc", "tank/apps/svc", true)
	if err != nil {
		t.Fatalf("prepareServiceRootForInstall: %v", err)
	}
	if got.Root != mountpoint || got.Dataset != "tank/apps/svc" || !got.ZFS {
		t.Fatalf("resolved = %#v", got)
	}
}

func TestPrepareServiceRootForInstallDefaultUsesServicesRootDataset(t *testing.T) {
	server := newTestServer(t)
	if err := os.MkdirAll(server.cfg.ServicesRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll services root: %v", err)
	}
	serviceRoot := filepath.Join(server.cfg.ServicesRoot, "api")
	server.zfsRunner = fakeZFSRunner(map[string]fakeZFSDataset{
		"tank/yeet/services":     {Mountpoint: server.cfg.ServicesRoot, Exists: true},
		"tank/yeet/services/api": {Mountpoint: serviceRoot},
	}).Run

	got, err := server.prepareServiceRootForInstall("api", "", false)
	if err != nil {
		t.Fatalf("prepareServiceRootForInstall: %v", err)
	}
	if got.Root != serviceRoot || got.Dataset != "tank/yeet/services/api" || !got.ZFS || !got.Created {
		t.Fatalf("resolved = %#v, want root %q dataset %q ZFS created", got, serviceRoot, "tank/yeet/services/api")
	}
}

func TestPrepareServiceRootForInstallZFSExistingDifferentDatasetRejects(t *testing.T) {
	server := newTestServer(t)
	addTestServices(t, server, db.Service{
		Name:           "svc",
		ServiceRoot:    "/tank/old/svc",
		ServiceRootZFS: "tank/apps/svc",
	})

	_, err := server.prepareServiceRootForInstall("svc", "tank/apps/other", true)
	if err == nil {
		t.Fatal("expected service root mismatch error")
	}
	wantHint := "yeet service set svc --service-root=tank/apps/other --zfs"
	if !strings.Contains(err.Error(), wantHint) {
		t.Fatalf("prepareServiceRootForInstall error = %v, want hint %q", err, wantHint)
	}
}

func TestPrepareServiceRootForInstallZFSExistingFilesystemPathRejects(t *testing.T) {
	server := newTestServer(t)
	addTestServices(t, server, db.Service{
		Name:           "svc",
		ServiceRoot:    "/tank/old/svc",
		ServiceRootZFS: "tank/apps/svc",
	})
	requestedRoot := filepath.Join(t.TempDir(), "svc")

	_, err := server.prepareServiceRootForInstall("svc", requestedRoot, false)
	if err == nil {
		t.Fatal("expected service root type mismatch error")
	}
	wantHint := "yeet service set svc --service-root=" + requestedRoot
	if !strings.Contains(err.Error(), wantHint) {
		t.Fatalf("prepareServiceRootForInstall error = %v, want hint %q", err, wantHint)
	}
}

func TestPrepareServiceRootForInstallFilesystemExistingZFSRequestRejects(t *testing.T) {
	server := newTestServer(t)
	addTestServices(t, server, db.Service{
		Name:        "svc",
		ServiceRoot: "/srv/apps/svc",
	})

	_, err := server.prepareServiceRootForInstall("svc", "tank/apps/svc", true)
	if err == nil {
		t.Fatal("expected service root type mismatch error")
	}
	wantHint := "yeet service set svc --service-root=tank/apps/svc --zfs"
	if !strings.Contains(err.Error(), wantHint) {
		t.Fatalf("prepareServiceRootForInstall error = %v, want hint %q", err, wantHint)
	}
}

func TestValidateRequestedServiceRoot(t *testing.T) {
	parent := t.TempDir()
	emptyExisting := filepath.Join(parent, "empty-existing")
	if err := os.Mkdir(emptyExisting, 0o755); err != nil {
		t.Fatalf("mkdir empty existing root: %v", err)
	}
	fileRoot := filepath.Join(parent, "file-root")
	if err := os.WriteFile(fileRoot, []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("write file root: %v", err)
	}
	nonEmptyRoot := filepath.Join(parent, "non-empty-root")
	if err := os.Mkdir(nonEmptyRoot, 0o755); err != nil {
		t.Fatalf("mkdir non-empty root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nonEmptyRoot, "existing"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write non-empty root file: %v", err)
	}
	retrySkeleton := filepath.Join(parent, "retry-skeleton")
	for _, dir := range []string{"bin", "data", "env", "run"} {
		if err := os.MkdirAll(filepath.Join(retrySkeleton, dir), 0o755); err != nil {
			t.Fatalf("mkdir retry skeleton %s: %v", dir, err)
		}
	}
	skeletonWithExtraFile := filepath.Join(parent, "skeleton-extra-file")
	for _, dir := range []string{"bin", "data", "env", "run"} {
		if err := os.MkdirAll(filepath.Join(skeletonWithExtraFile, dir), 0o755); err != nil {
			t.Fatalf("mkdir extra file skeleton %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(skeletonWithExtraFile, "extra"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write extra file: %v", err)
	}
	skeletonWithExtraDir := filepath.Join(parent, "skeleton-extra-dir")
	for _, dir := range []string{"bin", "data", "env", "run", "logs"} {
		if err := os.MkdirAll(filepath.Join(skeletonWithExtraDir, dir), 0o755); err != nil {
			t.Fatalf("mkdir extra dir skeleton %s: %v", dir, err)
		}
	}
	skeletonWithNonEmptyManagedDir := filepath.Join(parent, "skeleton-non-empty-managed")
	for _, dir := range []string{"bin", "data", "env", "run"} {
		if err := os.MkdirAll(filepath.Join(skeletonWithNonEmptyManagedDir, dir), 0o755); err != nil {
			t.Fatalf("mkdir non-empty managed skeleton %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(skeletonWithNonEmptyManagedDir, "bin", "payload"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write managed dir payload: %v", err)
	}
	cleanRoot := filepath.Join(parent, "dirty", "..", "clean-root")

	tests := []struct {
		name    string
		root    string
		want    string
		wantErr string
	}{
		{name: "empty", root: "", want: ""},
		{name: "relative", root: "relative/root", wantErr: "absolute"},
		{name: "cleans missing final root", root: cleanRoot, want: filepath.Join(parent, "clean-root")},
		{name: "missing parent", root: filepath.Join(parent, "missing-parent", "svc"), wantErr: "parent"},
		{name: "final root is file", root: fileRoot, wantErr: "file"},
		{name: "final root is non-empty dir", root: nonEmptyRoot, wantErr: "empty"},
		{name: "final root is empty existing dir", root: emptyExisting, want: emptyExisting},
		{name: "retry skeleton is allowed", root: retrySkeleton, want: retrySkeleton},
		{name: "retry skeleton rejects extra file", root: skeletonWithExtraFile, wantErr: "empty"},
		{name: "retry skeleton rejects extra dir", root: skeletonWithExtraDir, wantErr: "empty"},
		{name: "retry skeleton rejects non-empty managed dir", root: skeletonWithNonEmptyManagedDir, wantErr: "empty"},
		{name: "final root is missing", root: filepath.Join(parent, "missing-root"), want: filepath.Join(parent, "missing-root")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateRequestedServiceRoot(tt.root)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("validateRequestedServiceRoot error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateRequestedServiceRoot returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("validateRequestedServiceRoot = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRemoveServiceDirectoryRemovalPlanSkipsData(t *testing.T) {
	root := filepath.Join(t.TempDir(), "svc")
	paths := []string{
		filepath.Join(root, "bin"),
		filepath.Join(root, "data"),
		filepath.Join(root, "env"),
		filepath.Join(root, "run"),
	}

	got := serviceChildDirsToRemove(paths, false)
	want := []string{
		filepath.Join(root, "bin"),
		filepath.Join(root, "env"),
		filepath.Join(root, "run"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("serviceChildDirsToRemove = %v, want %v", got, want)
	}
}

func TestRemoveServiceDirectoryRemovalPlanIncludesDataWhenCleanData(t *testing.T) {
	root := filepath.Join(t.TempDir(), "svc")
	paths := []string{
		filepath.Join(root, "bin"),
		filepath.Join(root, "data"),
		filepath.Join(root, "env"),
		filepath.Join(root, "run"),
	}

	got := serviceChildDirsToRemove(paths, true)
	if !slices.Equal(got, paths) {
		t.Fatalf("serviceChildDirsToRemove = %v, want %v", got, paths)
	}
}

func TestRemoveServiceCleanDataRemovesDataDir(t *testing.T) {
	server := newTestServer(t)
	serviceRoot := filepath.Join(server.cfg.ServicesRoot, "api")
	for _, dir := range []string{"bin", "data", "env", "run"} {
		if err := os.MkdirAll(filepath.Join(serviceRoot, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(serviceRoot, "data", "rootfs.raw"), []byte("disk"), 0o644); err != nil {
		t.Fatalf("write disk: %v", err)
	}
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"api": {Name: "api", ServiceType: db.ServiceType("unknown")},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}

	report, err := server.RemoveServiceWithOptions("api", RemoveOptions{CleanData: true})
	if err != nil {
		t.Fatalf("RemoveServiceWithOptions: %v", err)
	}
	if report == nil {
		t.Fatal("expected remove report")
	}
	for _, removed := range []string{"bin", "data", "env", "run"} {
		if _, err := os.Stat(filepath.Join(serviceRoot, removed)); !os.IsNotExist(err) {
			t.Fatalf("%s stat err = %v, want not exist", removed, err)
		}
	}
	if _, err := os.Stat(serviceRoot); !os.IsNotExist(err) {
		t.Fatalf("service root stat err = %v, want not exist", err)
	}
}

func TestRemoveServiceCleanDataDestroysZFSServiceRoot(t *testing.T) {
	server := newTestServer(t)
	var zfsCalls [][]string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		zfsCalls = append(zfsCalls, append([]string(nil), args...))
		return "", "", nil
	}
	serviceRoot := filepath.Join(server.cfg.ServicesRoot, "api")
	if err := os.MkdirAll(filepath.Join(serviceRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"api": {
				Name:           "api",
				ServiceType:    db.ServiceType("unknown"),
				ServiceRoot:    serviceRoot,
				ServiceRootZFS: "tank/apps/api",
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}

	if _, err := server.RemoveServiceWithOptions("api", RemoveOptions{CleanData: true}); err != nil {
		t.Fatalf("RemoveServiceWithOptions: %v", err)
	}
	want := [][]string{{"destroy", "-R", "tank/apps/api"}}
	if !reflect.DeepEqual(zfsCalls, want) {
		t.Fatalf("zfs calls = %#v, want %#v", zfsCalls, want)
	}
}

func TestRemoveVMCleanDataDestroysServiceRootNotSharedImageBase(t *testing.T) {
	server := newTestServer(t)
	name := "devbox"
	serviceRoot := filepath.Join(server.cfg.ServicesRoot, name)
	if err := os.MkdirAll(serviceRoot, 0o755); err != nil {
		t.Fatalf("mkdir service root: %v", err)
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		name: {
			Name:           name,
			ServiceType:    db.ServiceTypeVM,
			ServiceRoot:    serviceRoot,
			ServiceRootZFS: "flash/yeet/vms/devbox",
			VM: &db.VMConfig{
				Image: db.VMImageConfig{Payload: testUbuntuVMPayload, Version: testUbuntuVMImageVersion},
				Disk: db.VMDiskConfig{
					Backend: vmDiskBackendZVOL,
					Bytes:   128 << 30,
					Path:    "/dev/zvol/flash/yeet/vms/devbox/vm/d-ea1055/root",
				},
			},
		},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	var calls [][]string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		calls = append(calls, append([]string(nil), args...))
		return "", "", nil
	}

	if _, err := server.RemoveServiceWithOptions(name, RemoveOptions{CleanData: true}); err != nil {
		t.Fatalf("RemoveServiceWithOptions: %v", err)
	}

	want := [][]string{{"destroy", "-R", "flash/yeet/vms/devbox"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("zfs calls = %#v, want %#v", calls, want)
	}
	for _, call := range calls {
		if strings.Contains(strings.Join(call, " "), "flash/yeet/vm-images") {
			t.Fatalf("cleanup touched shared image base: %#v", calls)
		}
	}
}

func TestRemoveServiceCleanDataFailsBeforeDBRemovalWhenZFSDestroyFails(t *testing.T) {
	server := newTestServer(t)
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		return "", "permission denied", errors.New("zfs failed")
	}
	serviceRoot := filepath.Join(server.cfg.ServicesRoot, "api")
	if err := os.MkdirAll(filepath.Join(serviceRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"api": {
				Name:           "api",
				ServiceType:    db.ServiceType("unknown"),
				ServiceRoot:    serviceRoot,
				ServiceRootZFS: "tank/apps/api",
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}

	_, err := server.RemoveServiceWithOptions("api", RemoveOptions{CleanData: true})
	if err == nil {
		t.Fatal("RemoveServiceWithOptions returned nil error")
	}
	if !strings.Contains(err.Error(), "zfs destroy -R tank/apps/api") || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("RemoveServiceWithOptions error = %v, want zfs destroy failure", err)
	}
	if _, err := server.serviceView("api"); err != nil {
		t.Fatalf("serviceView after failed cleanup = %v, want service retained for retry", err)
	}
}

func TestDestroyServiceRootZFSRetriesBusyDestroy(t *testing.T) {
	server := newTestServer(t)
	oldDelay := zfsDestroyRetryDelay
	zfsDestroyRetryDelay = 0
	t.Cleanup(func() { zfsDestroyRetryDelay = oldDelay })

	var zfsCalls [][]string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		zfsCalls = append(zfsCalls, append([]string(nil), args...))
		if len(zfsCalls) == 1 {
			return "", "cannot destroy 'tank/apps/api': dataset is busy", errors.New("zfs failed")
		}
		return "", "", nil
	}

	if err := server.destroyServiceRootZFS("tank/apps/api"); err != nil {
		t.Fatalf("destroyServiceRootZFS: %v", err)
	}
	want := [][]string{
		{"destroy", "-R", "tank/apps/api"},
		{"destroy", "-R", "tank/apps/api"},
	}
	if !reflect.DeepEqual(zfsCalls, want) {
		t.Fatalf("zfs calls = %#v, want %#v", zfsCalls, want)
	}
}

func TestDestroyServiceRootZFSTreatsMissingDatasetAsCleaned(t *testing.T) {
	server := newTestServer(t)
	var zfsCalls [][]string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		zfsCalls = append(zfsCalls, append([]string(nil), args...))
		return "", "cannot open 'tank/apps/api': dataset does not exist", errors.New("zfs failed")
	}

	if err := server.destroyServiceRootZFS("tank/apps/api"); err != nil {
		t.Fatalf("destroyServiceRootZFS: %v", err)
	}
	want := [][]string{{"destroy", "-R", "tank/apps/api"}}
	if !reflect.DeepEqual(zfsCalls, want) {
		t.Fatalf("zfs calls = %#v, want %#v", zfsCalls, want)
	}
}

func TestDestroyServiceRootZFSSucceedsWhenBusyRetryFindsDatasetMissing(t *testing.T) {
	server := newTestServer(t)
	oldDelay := zfsDestroyRetryDelay
	zfsDestroyRetryDelay = 0
	t.Cleanup(func() { zfsDestroyRetryDelay = oldDelay })

	var zfsCalls [][]string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		zfsCalls = append(zfsCalls, append([]string(nil), args...))
		if len(zfsCalls) == 1 {
			return "", "cannot destroy 'tank/apps/api/root': dataset is busy", errors.New("zfs failed")
		}
		return "", "cannot open 'tank/apps/api': dataset does not exist", errors.New("zfs failed")
	}

	if err := server.destroyServiceRootZFS("tank/apps/api"); err != nil {
		t.Fatalf("destroyServiceRootZFS: %v", err)
	}
	want := [][]string{
		{"destroy", "-R", "tank/apps/api"},
		{"destroy", "-R", "tank/apps/api"},
	}
	if !reflect.DeepEqual(zfsCalls, want) {
		t.Fatalf("zfs calls = %#v, want %#v", zfsCalls, want)
	}
}

func TestDestroyServiceRootZFSSettlesAndKeepsRetryingBusyDestroy(t *testing.T) {
	server := newTestServer(t)
	oldDelay := zfsDestroyRetryDelay
	zfsDestroyRetryDelay = 0
	oldMaxAttempts := zfsDestroyMaxAttempts
	zfsDestroyMaxAttempts = 4
	var settleCalls int
	oldSettle := zfsDestroySettleFunc
	zfsDestroySettleFunc = func(context.Context) {
		settleCalls++
	}
	t.Cleanup(func() {
		zfsDestroyRetryDelay = oldDelay
		zfsDestroyMaxAttempts = oldMaxAttempts
		zfsDestroySettleFunc = oldSettle
	})

	var zfsCalls [][]string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		zfsCalls = append(zfsCalls, append([]string(nil), args...))
		if len(zfsCalls) < 4 {
			return "", "cannot destroy 'tank/apps/api/root': dataset is busy", errors.New("zfs failed")
		}
		return "", "", nil
	}

	if err := server.destroyServiceRootZFS("tank/apps/api"); err != nil {
		t.Fatalf("destroyServiceRootZFS: %v", err)
	}
	if len(zfsCalls) != 4 {
		t.Fatalf("zfs calls = %d, want 4: %#v", len(zfsCalls), zfsCalls)
	}
	if settleCalls != 3 {
		t.Fatalf("settle calls = %d, want 3", settleCalls)
	}
}

func TestRemoveServiceTailscaleStableIDDecision(t *testing.T) {
	stableID := tailcfg.StableNodeID("node-123")
	withID := (&db.Service{
		Name:  "api",
		TSNet: &db.TailscaleNetwork{StableID: stableID},
	}).View()
	if got := tailscaleStableIDForRemoval(withID); got != string(stableID) {
		t.Fatalf("tailscaleStableIDForRemoval = %q, want %q", got, stableID)
	}

	withoutID := (&db.Service{Name: "api"}).View()
	if got := tailscaleStableIDForRemoval(withoutID); got != "" {
		t.Fatalf("tailscaleStableIDForRemoval without TSNet = %q, want empty", got)
	}
}

func TestEventDataMarshalJSON(t *testing.T) {
	raw, err := json.Marshal(EventData{})
	if err != nil {
		t.Fatalf("marshal nil event data: %v", err)
	}
	if string(raw) != "null" {
		t.Fatalf("nil event data json = %s", raw)
	}
	raw, err = json.Marshal(EventData{Data: map[string]string{"k": "v"}})
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	if string(raw) != `{"k":"v"}` {
		t.Fatalf("event data json = %s", raw)
	}
}

func TestServerVerifyCallerUsesAuthorizeFunc(t *testing.T) {
	server := newTestServer(t)
	wantErr := errors.New("denied")
	var gotRemote string
	server.cfg.AuthorizeFunc = func(ctx context.Context, remoteAddr string) error {
		gotRemote = remoteAddr
		return wantErr
	}

	err := server.verifyCaller(context.Background(), "100.64.0.1:1234")
	if !errors.Is(err, wantErr) {
		t.Fatalf("verifyCaller error = %v, want %v", err, wantErr)
	}
	if gotRemote != "100.64.0.1:1234" {
		t.Fatalf("remote = %q", gotRemote)
	}
}

func TestServerRegistryHandlerAndClosedRegistryListener(t *testing.T) {
	server := newTestServer(t)
	if server.RegistryHandler() != server.registry {
		t.Fatal("RegistryHandler did not return server registry")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("Close listener: %v", err)
	}
	if err := server.ServeInternalRegistry(ln); err == nil {
		t.Fatal("expected serving closed listener to return error")
	}
}

func TestEnsureServiceDirCreatesDirAndSkipsRootChown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "svc", "bin")
	if err := ensureServiceDir(dir, "root"); err != nil {
		t.Fatalf("ensureServiceDir root: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("dir stat = %v, %v", info, err)
	}
	if err := ensureServiceDir(filepath.Join(t.TempDir(), "svc", "bin"), "definitely-not-a-local-user"); err == nil {
		t.Fatal("expected lookup error for unknown user")
	}
}

func TestIsServiceTypeRunningRejectsUnknownType(t *testing.T) {
	if _, err := newTestServer(t).isServiceTypeRunning("svc", db.ServiceType("bogus")); err == nil {
		t.Fatal("expected unknown service type error")
	}
}

func TestIsServiceTypeRunningSupportsVM(t *testing.T) {
	old := serverVMStatusFunc
	defer func() { serverVMStatusFunc = old }()
	var gotName string
	serverVMStatusFunc = func(name string) (svc.Status, error) {
		gotName = name
		return svc.StatusRunning, nil
	}

	running, err := newTestServer(t).isServiceTypeRunning("devbox", db.ServiceTypeVM)
	if err != nil {
		t.Fatalf("isServiceTypeRunning: %v", err)
	}
	if !running {
		t.Fatal("running = false, want true")
	}
	if gotName != "devbox" {
		t.Fatalf("vm status name = %q, want devbox", gotName)
	}
}

func TestRemoveServiceRemovesConfigDirsPreservesDataAndPublishesEvent(t *testing.T) {
	server := newTestServer(t)
	serviceRoot := filepath.Join(t.TempDir(), "custom-api")
	for _, dir := range []string{"bin", "data", "env", "run"} {
		if err := os.MkdirAll(filepath.Join(serviceRoot, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	defaultRoot := filepath.Join(server.cfg.ServicesRoot, "api")
	if err := os.MkdirAll(filepath.Join(defaultRoot, "run"), 0o755); err != nil {
		t.Fatalf("mkdir default run: %v", err)
	}
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"api": {Name: "api", ServiceType: db.ServiceType("unknown"), ServiceRoot: serviceRoot},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	ch := make(chan Event, 1)
	handle := server.AddEventListener(ch, nil)
	defer server.RemoveEventListener(handle)

	report, err := server.RemoveService("api")
	if err != nil {
		t.Fatalf("RemoveService: %v", err)
	}
	if len(report.Warnings) == 0 {
		t.Fatal("expected running-check warning for unknown service type")
	}
	event := <-ch
	if event.Type != EventTypeServiceDeleted || event.ServiceName != "api" {
		t.Fatalf("event = %#v", event)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	if dv.Services().Contains("api") {
		t.Fatal("service remains in db")
	}
	for _, removed := range []string{"bin", "env", "run"} {
		if _, err := os.Stat(filepath.Join(serviceRoot, removed)); !os.IsNotExist(err) {
			t.Fatalf("%s stat err = %v, want not exist", removed, err)
		}
	}
	if _, err := os.Stat(filepath.Join(serviceRoot, "data")); err != nil {
		t.Fatalf("data dir should remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(defaultRoot, "run")); err != nil {
		t.Fatalf("default root should not be removed: %v", err)
	}
}

func TestRemoveServiceDoesNotWarnForEmptyServiceType(t *testing.T) {
	server := newTestServer(t)
	serviceRoot := filepath.Join(t.TempDir(), "staged-only")
	for _, dir := range []string{"bin", "data", "env", "run"} {
		if err := os.MkdirAll(filepath.Join(serviceRoot, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"staged-only": {
				Name:        "staged-only",
				ServiceRoot: serviceRoot,
				Artifacts: db.ArtifactStore{
					db.ArtifactEnvFile: {Refs: map[db.ArtifactRef]string{
						db.ArtifactRef("staged"): filepath.Join(serviceRoot, "env", "env-20260619111327"),
					}},
				},
			},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}

	report, err := server.RemoveService("staged-only")
	if err != nil {
		t.Fatalf("RemoveService: %v", err)
	}
	for _, warning := range report.Warnings {
		if strings.Contains(warning.Error(), "failed to check if service") {
			t.Fatalf("unexpected running check warning for empty service type: %v", warning)
		}
	}
}

func TestRemoveServiceSkipsDirectoryCleanupWhenRootResolutionFails(t *testing.T) {
	server := newTestServer(t)
	defaultRoot := filepath.Join(server.cfg.ServicesRoot, "api")
	for _, dir := range []string{"bin", "data", "env", "run"} {
		if err := os.MkdirAll(filepath.Join(defaultRoot, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := server.cfg.DB.Set(&db.Data{
		Services: map[string]*db.Service{
			"api": {Name: "api", ServiceType: db.ServiceType("unknown")},
		},
	}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}
	rootErr := errors.New("root lookup failed")
	server.serviceRootDirFunc = func(string) (string, error) {
		return "", rootErr
	}

	report, err := server.RemoveService("api")
	if err != nil {
		t.Fatalf("RemoveService: %v", err)
	}
	if report == nil || len(report.Warnings) == 0 {
		t.Fatalf("expected warning for root lookup failure, got %#v", report)
	}
	foundRootWarning := false
	for _, warn := range report.Warnings {
		if errors.Is(warn, rootErr) {
			foundRootWarning = true
			break
		}
	}
	if !foundRootWarning {
		t.Fatalf("warnings = %#v, want root lookup warning", report.Warnings)
	}
	for _, preserved := range []string{"bin", "data", "env", "run"} {
		if _, err := os.Stat(filepath.Join(defaultRoot, preserved)); err != nil {
			t.Fatalf("%s dir should remain after root lookup failure: %v", preserved, err)
		}
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("DB.Get: %v", err)
	}
	if dv.Services().Contains("api") {
		t.Fatal("service remains in db")
	}
}

func TestAddWarningIgnoresNil(t *testing.T) {
	var report RemoveReport
	report.addWarning(nil)
	report.addWarning(errors.New("warn"))
	if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0].Error(), "warn") {
		t.Fatalf("warnings = %#v", report.Warnings)
	}
}
