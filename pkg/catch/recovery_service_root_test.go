// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

const (
	serviceRootRecoveryDataset  = "flash/yeet/services/app"
	serviceRootRecoveryRoot     = "/flash/yeet/services/app"
	serviceRootRecoverySnapshot = serviceRootRecoveryDataset + "@yeet-20260613T203100Z-run-g3"
)

func TestSnapshotsCloneServiceRootClonesDatasetAndCurrentDefinition(t *testing.T) {
	server := newTestServer(t)
	seedServiceRootRecoverySource(t, server)
	var installedService string
	withServiceRootCloneInstall(t, func(_ *Server, service *db.Service) error {
		installedService = service.Name
		return nil
	})
	withServiceRootCloneStop(t, func(_ *Server, _ *db.Service) error {
		return nil
	})
	withServiceRootCloneArtifactRewrite(t, func(_ db.ArtifactStore, _, _ string) error {
		return nil
	})
	targetDataset := "flash/yeet/services/app-restore"
	targetRoot := "/flash/yeet/services/app-restore"
	var calls []string
	server.zfsRunner = serviceRootRecoveryZFSRunner(t, &calls, map[string]string{
		serviceRootRecoveryDataset: serviceRootRecoverySnapshotLine(serviceRootRecoverySnapshot, "app", 3),
	}, map[string]string{
		targetDataset: targetRoot,
	})
	var out bytes.Buffer

	err := server.cloneRecoveryPoint(context.Background(), "app", "yeet-20260613T203100Z", "app-restore", cli.SnapshotsCloneFlags{}, &out)
	if err != nil {
		t.Fatalf("cloneRecoveryPoint: %v", err)
	}

	for _, want := range []string{
		"clone " + serviceRootRecoverySnapshot + " " + targetDataset,
		"get -H -o value mountpoint " + targetDataset,
	} {
		if !hasRecoveryCall(calls, want) {
			t.Fatalf("zfs calls = %#v, missing %q", calls, want)
		}
	}
	cloned := mustService(t, server, "app-restore")
	if cloned.ServiceType != db.ServiceTypeDockerCompose {
		t.Fatalf("clone type = %q, want docker-compose", cloned.ServiceType)
	}
	if cloned.ServiceRootZFS != targetDataset {
		t.Fatalf("clone ServiceRootZFS = %q, want %q", cloned.ServiceRootZFS, targetDataset)
	}
	if cloned.ServiceRoot != targetRoot {
		t.Fatalf("clone ServiceRoot = %q, want %q", cloned.ServiceRoot, targetRoot)
	}
	if cloned.Generation != 1 || cloned.LatestGeneration != 1 {
		t.Fatalf("clone generations = %d/%d, want 1/1", cloned.Generation, cloned.LatestGeneration)
	}
	if cloned.SvcNetwork != nil || cloned.Macvlan != nil || cloned.TSNet != nil {
		t.Fatalf("clone network fields = svc:%#v macvlan:%#v tsnet:%#v, want cleared", cloned.SvcNetwork, cloned.Macvlan, cloned.TSNet)
	}
	composeRefs := cloned.Artifacts[db.ArtifactDockerComposeFile].Refs
	if composeRefs["latest"] != targetRoot+"/compose.yml" {
		t.Fatalf("compose latest = %q, want cloned root path", composeRefs["latest"])
	}
	if composeRefs[db.Gen(1)] != targetRoot+"/compose.yml" {
		t.Fatalf("compose gen1 = %q, want cloned root path", composeRefs[db.Gen(1)])
	}
	if _, ok := composeRefs[db.Gen(3)]; ok {
		t.Fatalf("compose gen3 ref remains after clone remap: %#v", composeRefs)
	}
	envRefs := cloned.Artifacts[db.ArtifactEnvFile].Refs
	if envRefs["latest"] != "/etc/yeet/app.env" {
		t.Fatalf("external env path = %q, want unchanged", envRefs["latest"])
	}
	binaryRefs := cloned.Artifacts[db.ArtifactBinary].Refs
	if binaryRefs["latest"] != serviceRootRecoveryRoot+"-sibling/bin/app" {
		t.Fatalf("sibling root path = %q, want unchanged", binaryRefs["latest"])
	}
	if strings.Contains(out.String(), "Started service") {
		t.Fatalf("clone output = %q, clone without --start should not start", out.String())
	}
	if installedService != "app-restore" {
		t.Fatalf("installed service = %q, want app-restore", installedService)
	}
}

func TestSnapshotsCloneServiceRootMaterializesSelectedGenerationAsCloneGeneration(t *testing.T) {
	server := newTestServer(t)
	seedServiceRootRecoverySource(t, server)
	targetDataset := "flash/yeet/services/app-restore"
	targetRoot := "/flash/yeet/services/app-restore"
	var calls []string
	server.zfsRunner = serviceRootRecoveryZFSRunner(t, &calls, map[string]string{
		serviceRootRecoveryDataset: serviceRootRecoverySnapshotLine(serviceRootRecoverySnapshot, "app", 3),
	}, map[string]string{
		targetDataset: targetRoot,
	})
	withServiceRootCloneArtifactRewrite(t, func(_ db.ArtifactStore, _, _ string) error {
		return nil
	})
	withServiceRootCloneInstall(t, func(s *Server, service *db.Service) error {
		inserted := mustService(t, s, service.Name)
		if inserted.Generation != 1 || inserted.LatestGeneration != 1 {
			t.Fatalf("inserted generation/latest = %d/%d, want 1/1", inserted.Generation, inserted.LatestGeneration)
		}
		refs := inserted.Artifacts[db.ArtifactDockerComposeFile].Refs
		if refs["latest"] != targetRoot+"/compose.yml" || refs[db.Gen(1)] != targetRoot+"/compose.yml" {
			t.Fatalf("inserted compose refs = %#v, want latest/gen-1 under cloned root", refs)
		}
		if _, ok := refs["staged"]; ok {
			t.Fatalf("inserted compose refs include staged ref: %#v", refs)
		}
		if _, ok := refs[db.Gen(3)]; ok {
			t.Fatalf("inserted compose refs include source generation ref: %#v", refs)
		}
		return nil
	})
	withServiceRootCloneStop(t, func(_ *Server, _ *db.Service) error {
		return nil
	})

	if err := server.cloneRecoveryPoint(context.Background(), "app", "yeet-20260613T203100Z", "app-restore", cli.SnapshotsCloneFlags{}, ioDiscardReadWriter{}); err != nil {
		t.Fatalf("cloneRecoveryPoint: %v", err)
	}
}

func TestSnapshotsCloneServiceRootRewritesCopiedArtifactContents(t *testing.T) {
	server := newTestServer(t)
	targetRoot := t.TempDir()
	composePath := filepath.Join(targetRoot, "compose.yml")
	if err := os.WriteFile(composePath, []byte("services:\n  app:\n    image: busybox\n    volumes:\n      - "+serviceRootRecoveryRoot+"/data:/data\n"), 0o644); err != nil {
		t.Fatalf("write compose fixture: %v", err)
	}
	externalPath := filepath.Join(t.TempDir(), "external.service")
	externalContent := []byte("ExecStart=" + serviceRootRecoveryRoot + "/bin/app\n")
	if err := os.WriteFile(externalPath, externalContent, 0o644); err != nil {
		t.Fatalf("write external fixture: %v", err)
	}
	addTestServices(t, server, db.Service{
		Name:             "app",
		ServiceType:      db.ServiceTypeDockerCompose,
		ServiceRoot:      serviceRootRecoveryRoot,
		ServiceRootZFS:   serviceRootRecoveryDataset,
		Generation:       3,
		LatestGeneration: 3,
		Artifacts: db.ArtifactStore{
			db.ArtifactDockerComposeFile: {
				Refs: map[db.ArtifactRef]string{
					"latest":  serviceRootRecoveryRoot + "/compose.yml",
					db.Gen(3): serviceRootRecoveryRoot + "/compose.yml",
				},
			},
			db.ArtifactSystemdUnit: {
				Refs: map[db.ArtifactRef]string{
					"latest": externalPath,
				},
			},
		},
	})
	targetDataset := "flash/yeet/services/app-restore"
	var calls []string
	server.zfsRunner = serviceRootRecoveryZFSRunner(t, &calls, map[string]string{
		serviceRootRecoveryDataset: serviceRootRecoverySnapshotLine(serviceRootRecoverySnapshot, "app", 3),
	}, map[string]string{
		targetDataset: targetRoot,
	})
	withServiceRootCloneInstall(t, func(_ *Server, _ *db.Service) error {
		return nil
	})
	withServiceRootCloneStop(t, func(_ *Server, _ *db.Service) error {
		return nil
	})

	if err := server.cloneRecoveryPoint(context.Background(), "app", "yeet-20260613T203100Z", "app-restore", cli.SnapshotsCloneFlags{}, ioDiscardReadWriter{}); err != nil {
		t.Fatalf("cloneRecoveryPoint: %v", err)
	}

	rewritten, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read rewritten compose: %v", err)
	}
	if !strings.Contains(string(rewritten), targetRoot+"/data:/data") {
		t.Fatalf("compose content = %q, want cloned root path", rewritten)
	}
	outside, err := os.ReadFile(externalPath)
	if err != nil {
		t.Fatalf("read external artifact: %v", err)
	}
	if string(outside) != string(externalContent) {
		t.Fatalf("external artifact content = %q, want unchanged %q", outside, externalContent)
	}
}

func TestSnapshotsCloneServiceRootInstallsAfterDBInsert(t *testing.T) {
	server := newTestServer(t)
	seedServiceRootRecoverySource(t, server)
	targetDataset := "flash/yeet/services/app-restore"
	targetRoot := "/flash/yeet/services/app-restore"
	var calls []string
	server.zfsRunner = serviceRootRecoveryZFSRunner(t, &calls, map[string]string{
		serviceRootRecoveryDataset: serviceRootRecoverySnapshotLine(serviceRootRecoverySnapshot, "app", 3),
	}, map[string]string{
		targetDataset: targetRoot,
	})
	withServiceRootCloneArtifactRewrite(t, func(_ db.ArtifactStore, _, _ string) error {
		return nil
	})
	var installedRoot string
	withServiceRootCloneInstall(t, func(s *Server, service *db.Service) error {
		if !serviceExists(t, s, service.Name) {
			t.Fatalf("service %q not inserted before install", service.Name)
		}
		installedRoot = service.ServiceRoot
		return nil
	})
	withServiceRootCloneStop(t, func(_ *Server, _ *db.Service) error {
		return nil
	})

	if err := server.cloneRecoveryPoint(context.Background(), "app", "yeet-20260613T203100Z", "app-restore", cli.SnapshotsCloneFlags{}, ioDiscardReadWriter{}); err != nil {
		t.Fatalf("cloneRecoveryPoint: %v", err)
	}

	if installedRoot != targetRoot {
		t.Fatalf("installed root = %q, want %q", installedRoot, targetRoot)
	}
}

func TestSnapshotsCloneServiceRootStopsAfterInstall(t *testing.T) {
	server := newTestServer(t)
	seedServiceRootRecoverySource(t, server)
	targetDataset := "flash/yeet/services/app-restore"
	targetRoot := "/flash/yeet/services/app-restore"
	var calls []string
	server.zfsRunner = serviceRootRecoveryZFSRunner(t, &calls, map[string]string{
		serviceRootRecoveryDataset: serviceRootRecoverySnapshotLine(serviceRootRecoverySnapshot, "app", 3),
	}, map[string]string{
		targetDataset: targetRoot,
	})
	withServiceRootCloneArtifactRewrite(t, func(_ db.ArtifactStore, _, _ string) error {
		return nil
	})
	var events []string
	withServiceRootCloneInstall(t, func(s *Server, service *db.Service) error {
		if !serviceExists(t, s, service.Name) {
			t.Fatalf("service %q not inserted before install", service.Name)
		}
		events = append(events, "install:"+service.Name)
		return nil
	})
	withServiceRootCloneStop(t, func(s *Server, service *db.Service) error {
		if !serviceExists(t, s, service.Name) {
			t.Fatalf("service %q not inserted before stop", service.Name)
		}
		events = append(events, "stop:"+service.Name)
		return nil
	})

	if err := server.cloneRecoveryPoint(context.Background(), "app", "yeet-20260613T203100Z", "app-restore", cli.SnapshotsCloneFlags{}, ioDiscardReadWriter{}); err != nil {
		t.Fatalf("cloneRecoveryPoint: %v", err)
	}

	if got, want := strings.Join(events, ","), "install:app-restore,stop:app-restore"; got != want {
		t.Fatalf("clone lifecycle events = %q, want %q", got, want)
	}
}

func TestSnapshotsCloneServiceRootCleansUpAfterInstallFailure(t *testing.T) {
	server := newTestServer(t)
	seedServiceRootRecoverySource(t, server)
	targetDataset := "flash/yeet/services/app-restore"
	targetRoot := "/flash/yeet/services/app-restore"
	var calls []string
	server.zfsRunner = serviceRootRecoveryZFSRunner(t, &calls, map[string]string{
		serviceRootRecoveryDataset: serviceRootRecoverySnapshotLine(serviceRootRecoverySnapshot, "app", 3),
	}, map[string]string{
		targetDataset: targetRoot,
	})
	withServiceRootCloneArtifactRewrite(t, func(_ db.ArtifactStore, _, _ string) error {
		return nil
	})
	installErr := errors.New("install cloned definition failed")
	withServiceRootCloneInstall(t, func(s *Server, service *db.Service) error {
		if !serviceExists(t, s, service.Name) {
			t.Fatalf("service %q not inserted before install", service.Name)
		}
		return installErr
	})

	err := server.cloneRecoveryPoint(context.Background(), "app", "yeet-20260613T203100Z", "app-restore", cli.SnapshotsCloneFlags{}, ioDiscardReadWriter{})

	if !errors.Is(err, installErr) {
		t.Fatalf("cloneRecoveryPoint error = %v, want install error", err)
	}
	if !hasRecoveryCall(calls, "destroy -r "+targetDataset) {
		t.Fatalf("zfs calls = %#v, want target dataset cleanup", calls)
	}
	if serviceExists(t, server, "app-restore") {
		t.Fatal("app-restore remains in DB after install failure")
	}
}

func TestSnapshotsCloneServiceRootCleanupPreservesMismatchedServiceRow(t *testing.T) {
	server := newTestServer(t)
	seedServiceRootRecoverySource(t, server)
	targetDataset := "flash/yeet/services/app-restore"
	targetRoot := "/flash/yeet/services/app-restore"
	var calls []string
	server.zfsRunner = serviceRootRecoveryZFSRunner(t, &calls, map[string]string{
		serviceRootRecoveryDataset: serviceRootRecoverySnapshotLine(serviceRootRecoverySnapshot, "app", 3),
	}, map[string]string{
		targetDataset: targetRoot,
	})
	withServiceRootCloneArtifactRewrite(t, func(_ db.ArtifactStore, _, _ string) error {
		return nil
	})
	installErr := errors.New("install cloned definition failed")
	withServiceRootCloneInstall(t, func(s *Server, service *db.Service) error {
		_, err := s.cfg.DB.MutateData(func(d *db.Data) error {
			d.Services[service.Name] = (&db.Service{
				Name:           service.Name,
				ServiceType:    db.ServiceTypeSystemd,
				ServiceRoot:    "/srv/unrelated",
				ServiceRootZFS: "flash/yeet/services/unrelated",
			}).Clone()
			return nil
		})
		if err != nil {
			t.Fatalf("replace cloned service with raced row: %v", err)
		}
		return installErr
	})

	err := server.cloneRecoveryPoint(context.Background(), "app", "yeet-20260613T203100Z", "app-restore", cli.SnapshotsCloneFlags{}, ioDiscardReadWriter{})

	if !errors.Is(err, installErr) {
		t.Fatalf("cloneRecoveryPoint error = %v, want install error", err)
	}
	if !hasRecoveryCall(calls, "destroy -r "+targetDataset) {
		t.Fatalf("zfs calls = %#v, want target dataset cleanup", calls)
	}
	got := mustService(t, server, "app-restore")
	if got.ServiceRootZFS != "flash/yeet/services/unrelated" {
		t.Fatalf("preserved service root zfs = %q, want unrelated row", got.ServiceRootZFS)
	}
}

func TestSnapshotsCloneServiceRootPreservesMatchingServiceRowWhenCleanupDestroyFails(t *testing.T) {
	server := newTestServer(t)
	seedServiceRootRecoverySource(t, server)
	targetDataset := "flash/yeet/services/app-restore"
	targetRoot := "/flash/yeet/services/app-restore"
	var calls []string
	baseRunner := serviceRootRecoveryZFSRunner(t, &calls, map[string]string{
		serviceRootRecoveryDataset: serviceRootRecoverySnapshotLine(serviceRootRecoverySnapshot, "app", 3),
	}, map[string]string{
		targetDataset: targetRoot,
	})
	destroyErr := errors.New("dataset busy")
	server.zfsRunner = func(ctx context.Context, args ...string) (string, string, error) {
		if strings.Join(args, " ") == "destroy -r "+targetDataset {
			calls = append(calls, strings.Join(args, " "))
			return "", "", destroyErr
		}
		return baseRunner(ctx, args...)
	}
	withServiceRootCloneArtifactRewrite(t, func(_ db.ArtifactStore, _, _ string) error {
		return nil
	})
	installErr := errors.New("install cloned definition failed")
	withServiceRootCloneInstall(t, func(_ *Server, _ *db.Service) error {
		return installErr
	})

	err := server.cloneRecoveryPoint(context.Background(), "app", "yeet-20260613T203100Z", "app-restore", cli.SnapshotsCloneFlags{}, ioDiscardReadWriter{})

	if !errors.Is(err, installErr) || !errors.Is(err, destroyErr) {
		t.Fatalf("cloneRecoveryPoint error = %v, want install and destroy errors", err)
	}
	if !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatalf("cloneRecoveryPoint error = %v, want cleanup failure context", err)
	}
	got := mustService(t, server, "app-restore")
	if got.ServiceRootZFS != targetDataset {
		t.Fatalf("preserved service root zfs = %q, want %q", got.ServiceRootZFS, targetDataset)
	}
}

func TestSnapshotsCloneServiceRootPreservesCloneAfterStopFailure(t *testing.T) {
	server := newTestServer(t)
	seedServiceRootRecoverySource(t, server)
	targetDataset := "flash/yeet/services/app-restore"
	targetRoot := "/flash/yeet/services/app-restore"
	var calls []string
	server.zfsRunner = serviceRootRecoveryZFSRunner(t, &calls, map[string]string{
		serviceRootRecoveryDataset: serviceRootRecoverySnapshotLine(serviceRootRecoverySnapshot, "app", 3),
	}, map[string]string{
		targetDataset: targetRoot,
	})
	withServiceRootCloneArtifactRewrite(t, func(_ db.ArtifactStore, _, _ string) error {
		return nil
	})
	withServiceRootCloneInstall(t, func(s *Server, service *db.Service) error {
		if !serviceExists(t, s, service.Name) {
			t.Fatalf("service %q not inserted before install", service.Name)
		}
		return nil
	})
	stopErr := errors.New("stop cloned service failed")
	withServiceRootCloneStop(t, func(s *Server, service *db.Service) error {
		if !serviceExists(t, s, service.Name) {
			t.Fatalf("service %q not inserted before stop", service.Name)
		}
		return stopErr
	})

	err := server.cloneRecoveryPoint(context.Background(), "app", "yeet-20260613T203100Z", "app-restore", cli.SnapshotsCloneFlags{}, ioDiscardReadWriter{})

	if !errors.Is(err, stopErr) {
		t.Fatalf("cloneRecoveryPoint error = %v, want stop error", err)
	}
	if hasRecoveryCall(calls, "destroy -r "+targetDataset) {
		t.Fatalf("zfs calls = %#v, stop failure should preserve cloned dataset", calls)
	}
	got := mustService(t, server, "app-restore")
	if got.ServiceRootZFS != targetDataset {
		t.Fatalf("preserved service root zfs = %q, want %q", got.ServiceRootZFS, targetDataset)
	}
}

func TestSnapshotsCloneServiceRootRejectsNonZFSRoot(t *testing.T) {
	server := newTestServer(t)
	addTestServices(t, server, db.Service{
		Name:        "app",
		ServiceType: db.ServiceTypeDockerCompose,
		ServiceRoot: "/srv/app",
	})
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "", "", nil
	}

	err := server.cloneRecoveryPoint(context.Background(), "app", "yeet-a", "app-restore", cli.SnapshotsCloneFlags{}, ioDiscardReadWriter{})

	if err == nil || !strings.Contains(err.Error(), "service app is not backed by a ZFS service root") {
		t.Fatalf("cloneRecoveryPoint error = %v, want non-ZFS service root rejection", err)
	}
	if len(calls) != 0 {
		t.Fatalf("zfs calls = %#v, non-ZFS service should not query ZFS", calls)
	}
	if serviceExists(t, server, "app-restore") {
		t.Fatal("app-restore exists after non-ZFS rejection")
	}
}

func TestSnapshotsCloneServiceRootRejectsStartBeforeMutation(t *testing.T) {
	server := newTestServer(t)
	seedServiceRootRecoverySource(t, server)
	var calls []string
	server.zfsRunner = serviceRootRecoveryZFSRunner(t, &calls, map[string]string{
		serviceRootRecoveryDataset: serviceRootRecoverySnapshotLine(serviceRootRecoverySnapshot, "app", 3),
	}, nil)

	err := server.cloneRecoveryPoint(context.Background(), "app", "yeet-20260613T203100Z", "app-restore", cli.SnapshotsCloneFlags{Start: true}, ioDiscardReadWriter{})

	if err == nil || !strings.Contains(err.Error(), "starting service-root clones is not supported yet; run snapshots clone without --start") {
		t.Fatalf("cloneRecoveryPoint error = %v, want unsupported --start rejection", err)
	}
	if hasRecoveryCall(calls, "clone ") {
		t.Fatalf("zfs calls = %#v, --start rejection should not clone", calls)
	}
	if serviceExists(t, server, "app-restore") {
		t.Fatal("app-restore exists after --start rejection")
	}
}

func TestSnapshotsRestoreServiceRootClonesSelectedSnapshotAndReplacesActiveRoot(t *testing.T) {
	server := newTestServer(t)
	activeRoot := t.TempDir()
	cloneRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(activeRoot, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale active root file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(activeRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir active data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(activeRoot, "data", "old.db"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write stale active data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(activeRoot, "compose.yml"), []byte("services:\n  app:\n    image: busybox\n"), 0o644); err != nil {
		t.Fatalf("write active compose: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cloneRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir clone data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cloneRoot, "compose.yml"), []byte("services:\n  app:\n    image: busybox\n"), 0o644); err != nil {
		t.Fatalf("write clone compose: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cloneRoot, "data", "snapshot.db"), []byte("snapshot"), 0o644); err != nil {
		t.Fatalf("write clone data: %v", err)
	}
	addTestServices(t, server, db.Service{
		Name:             "app",
		ServiceType:      db.ServiceTypeDockerCompose,
		ServiceRoot:      activeRoot,
		ServiceRootZFS:   serviceRootRecoveryDataset,
		Generation:       3,
		LatestGeneration: 3,
		Artifacts: db.ArtifactStore{
			db.ArtifactDockerComposeFile: {
				Refs: map[db.ArtifactRef]string{
					"latest":  filepath.Join(activeRoot, "compose.yml"),
					db.Gen(3): filepath.Join(activeRoot, "compose.yml"),
				},
			},
		},
	})
	installServiceRootRecoveryFakeCommands(t)
	var calls []string
	var tempDataset string
	rollbackErr := errors.New("cannot rollback to selected snapshot: newer snapshots exist")
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		command := strings.Join(args, " ")
		calls = append(calls, command)
		if isRecoverySnapshotList(args) {
			return serviceRootRecoverySnapshotLine(serviceRootRecoverySnapshot, "app", 3), "", nil
		}
		switch args[0] {
		case "snapshot":
			return "", "", nil
		case "rollback":
			return "", rollbackErr.Error(), rollbackErr
		case "clone":
			if len(args) != 3 || args[1] != serviceRootRecoverySnapshot {
				return "", "unexpected clone", errZFSCommandFailed
			}
			tempDataset = args[2]
			return "", "", nil
		case "get":
			if strings.Join(args[:5], " ") == "get -H -o value mountpoint" && args[5] == tempDataset {
				return cloneRoot + "\n", "", nil
			}
			return "", "unexpected get", errZFSCommandFailed
		case "destroy":
			if strings.Join(args, " ") == "destroy -r "+tempDataset {
				return "", "", nil
			}
			return "", "unexpected destroy", errZFSCommandFailed
		default:
			return "", "unexpected zfs command", errZFSCommandFailed
		}
	}
	var out bytes.Buffer

	err := server.restoreRecoveryPoint(context.Background(), "app", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true}, &out)
	if err != nil {
		t.Fatalf("restoreRecoveryPoint: %v", err)
	}

	if tempDataset == "" {
		t.Fatalf("zfs calls = %#v, want temporary clone dataset", calls)
	}
	for _, want := range []string{
		"snapshot ",
		"clone " + serviceRootRecoverySnapshot + " " + tempDataset,
		"get -H -o value mountpoint " + tempDataset,
		"destroy -r " + tempDataset,
	} {
		if !hasRecoveryCall(calls, want) {
			t.Fatalf("zfs calls = %#v, missing %q", calls, want)
		}
	}
	if hasRecoveryCall(calls, "rollback ") {
		t.Fatalf("zfs calls = %#v, service-root restore should not use zfs rollback", calls)
	}
	if _, err := os.Stat(filepath.Join(activeRoot, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale active root file stat err = %v, want removed", err)
	}
	if _, err := os.Stat(filepath.Join(activeRoot, "data", "old.db")); !os.IsNotExist(err) {
		t.Fatalf("stale active root data stat err = %v, want removed", err)
	}
	gotSnapshot, err := os.ReadFile(filepath.Join(activeRoot, "data", "snapshot.db"))
	if err != nil {
		t.Fatalf("read restored snapshot data: %v", err)
	}
	if string(gotSnapshot) != "snapshot" {
		t.Fatalf("restored snapshot data = %q, want snapshot", gotSnapshot)
	}
	if !strings.Contains(out.String(), "Pre-restore recovery point:") {
		t.Fatalf("output = %q, want pre-restore snapshot progress", out.String())
	}
	if !strings.Contains(out.String(), "Restored service root: "+serviceRootRecoverySnapshot) {
		t.Fatalf("output = %q, want restored service root progress", out.String())
	}
}

func TestSnapshotsRestoreServiceRootPreservesRecoveredStageBeforeDestroyingTempClone(t *testing.T) {
	server := newTestServer(t)
	activeRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(activeRoot, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write active stale file: %v", err)
	}
	cloneRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cloneRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir clone data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cloneRoot, "data", "snapshot.db"), []byte("snapshot"), 0o644); err != nil {
		t.Fatalf("write clone data: %v", err)
	}
	service := &db.Service{
		Name:           "app",
		ServiceType:    db.ServiceTypeDockerCompose,
		ServiceRoot:    activeRoot,
		ServiceRootZFS: serviceRootRecoveryDataset,
	}
	point := recoveryPoint{
		Name:    serviceRootRecoverySnapshot,
		Dataset: serviceRootRecoveryDataset,
	}
	moveErr := errors.New("final placement failed")
	var stagedPath string
	withServiceRootRestorePlacement(t, func(stage, _ string) error {
		stagedPath = stage
		return moveErr
	})
	var tempDataset string
	var destroyedTemp bool
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		switch args[0] {
		case "clone":
			if len(args) != 3 || args[1] != serviceRootRecoverySnapshot {
				return "", "unexpected clone", errZFSCommandFailed
			}
			tempDataset = args[2]
			return "", "", nil
		case "get":
			if strings.Join(args[:5], " ") == "get -H -o value mountpoint" && args[5] == tempDataset {
				return cloneRoot + "\n", "", nil
			}
			return "", "unexpected get", errZFSCommandFailed
		case "destroy":
			if strings.Join(args, " ") == "destroy -r "+tempDataset {
				destroyedTemp = true
				return "", "", nil
			}
			return "", "unexpected destroy", errZFSCommandFailed
		default:
			return "", "unexpected zfs command", errZFSCommandFailed
		}
	}

	err := server.restoreServiceRootFromSnapshot(context.Background(), service, point)

	if !errors.Is(err, moveErr) {
		t.Fatalf("restoreServiceRootFromSnapshot error = %v, want final placement error", err)
	}
	if stagedPath == "" || !strings.Contains(err.Error(), stagedPath) {
		t.Fatalf("restoreServiceRootFromSnapshot error = %v, want preserved stage path %q", err, stagedPath)
	}
	gotSnapshot, readErr := os.ReadFile(filepath.Join(stagedPath, "data", "snapshot.db"))
	if readErr != nil {
		t.Fatalf("read preserved staged snapshot data after temp clone cleanup: %v", readErr)
	}
	if string(gotSnapshot) != "snapshot" {
		t.Fatalf("preserved staged snapshot data = %q, want snapshot", gotSnapshot)
	}
	if !destroyedTemp {
		t.Fatal("temporary clone was not destroyed after recovered data was staged under active root")
	}
}

func TestRestoreServiceRootFromCloneMountpointStagesBesideActiveRootAndCleansScratch(t *testing.T) {
	parent := t.TempDir()
	activeRoot := filepath.Join(parent, "app")
	if err := os.MkdirAll(filepath.Join(activeRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir active data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(activeRoot, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write active stale file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(activeRoot, "data", "old.db"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write active stale data: %v", err)
	}
	cloneRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cloneRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir clone data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cloneRoot, "compose.yml"), []byte("services:\n  app:\n    image: busybox\n"), 0o644); err != nil {
		t.Fatalf("write clone compose: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cloneRoot, "data", "snapshot.db"), []byte("snapshot"), 0o644); err != nil {
		t.Fatalf("write clone snapshot data: %v", err)
	}

	if err := restoreServiceRootFromCloneMountpoint(cloneRoot, activeRoot); err != nil {
		t.Fatalf("restoreServiceRootFromCloneMountpoint: %v", err)
	}

	if _, err := os.Stat(filepath.Join(activeRoot, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale active root file stat err = %v, want removed", err)
	}
	if _, err := os.Stat(filepath.Join(activeRoot, "data", "old.db")); !os.IsNotExist(err) {
		t.Fatalf("stale active root data stat err = %v, want removed", err)
	}
	gotSnapshot, err := os.ReadFile(filepath.Join(activeRoot, "data", "snapshot.db"))
	if err != nil {
		t.Fatalf("read restored snapshot data: %v", err)
	}
	if string(gotSnapshot) != "snapshot" {
		t.Fatalf("restored snapshot data = %q, want snapshot", gotSnapshot)
	}
	activeEntries, err := os.ReadDir(activeRoot)
	if err != nil {
		t.Fatalf("read active root entries: %v", err)
	}
	for _, entry := range activeEntries {
		if strings.HasPrefix(entry.Name(), ".yeet-service-root-restore-") {
			t.Fatalf("restore stage %q remains in active root", entry.Name())
		}
	}
	parentEntries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read active root parent entries: %v", err)
	}
	for _, entry := range parentEntries {
		if strings.HasPrefix(entry.Name(), ".yeet-service-root-restore-") {
			t.Fatalf("restore stage %q was created beside active root", entry.Name())
		}
	}
}

func TestRestoreServiceRootFromCloneMountpointPreservesStageWhenFinalPlacementFailsAfterPartialWrite(t *testing.T) {
	parent := t.TempDir()
	activeRoot := filepath.Join(parent, "app")
	if err := os.MkdirAll(filepath.Join(activeRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir active data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(activeRoot, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write active stale file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(activeRoot, "data", "old.db"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write active stale data: %v", err)
	}
	cloneRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cloneRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir clone data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cloneRoot, "compose.yml"), []byte("services:\n  app:\n    image: busybox\n"), 0o644); err != nil {
		t.Fatalf("write clone compose: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cloneRoot, "data", "snapshot.db"), []byte("snapshot"), 0o644); err != nil {
		t.Fatalf("write clone snapshot data: %v", err)
	}
	moveErr := errors.New("final placement failed")
	var stagedPath string
	withServiceRootRestorePlacement(t, func(stage, target string) error {
		stagedPath = stage
		compose, err := os.ReadFile(filepath.Join(stage, "compose.yml"))
		if err != nil {
			t.Fatalf("read staged compose before partial placement: %v", err)
		}
		if err := os.WriteFile(filepath.Join(target, "compose.yml"), compose, 0o644); err != nil {
			t.Fatalf("partially copy staged compose: %v", err)
		}
		return moveErr
	})

	err := restoreServiceRootFromCloneMountpoint(cloneRoot, activeRoot)

	if !errors.Is(err, moveErr) {
		t.Fatalf("restoreServiceRootFromCloneMountpoint error = %v, want final placement error", err)
	}
	if stagedPath == "" {
		t.Fatal("move hook did not observe staged path")
	}
	if !strings.Contains(err.Error(), stagedPath) {
		t.Fatalf("restoreServiceRootFromCloneMountpoint error = %v, want preserved stage path %q", err, stagedPath)
	}
	gotStale, err := os.ReadFile(filepath.Join(activeRoot, "stale.txt"))
	if err != nil {
		t.Fatalf("read original active root file after partial placement failure: %v", err)
	}
	if string(gotStale) != "stale" {
		t.Fatalf("active stale file = %q, want original stale content", gotStale)
	}
	gotOld, err := os.ReadFile(filepath.Join(activeRoot, "data", "old.db"))
	if err != nil {
		t.Fatalf("read original active data after partial placement failure: %v", err)
	}
	if string(gotOld) != "old" {
		t.Fatalf("active old data = %q, want original old content", gotOld)
	}
	if _, err := os.Stat(filepath.Join(activeRoot, "compose.yml")); !os.IsNotExist(err) {
		t.Fatalf("partial restored compose stat err = %v, want removed after failed placement", err)
	}
	gotSnapshot, err := os.ReadFile(filepath.Join(stagedPath, "data", "snapshot.db"))
	if err != nil {
		t.Fatalf("read preserved staged snapshot data: %v", err)
	}
	if string(gotSnapshot) != "snapshot" {
		t.Fatalf("preserved staged snapshot data = %q, want snapshot", gotSnapshot)
	}
	if _, err := os.Stat(filepath.Join(stagedPath, "compose.yml")); err != nil {
		t.Fatalf("stat preserved staged compose file after partial active-root write: %v", err)
	}
}

func TestRestoreServiceRootFromCloneMountpointPreservesActiveRootAndCompleteStageWhenFinalPlacementFails(t *testing.T) {
	parent := t.TempDir()
	activeRoot := filepath.Join(parent, "app")
	if err := os.MkdirAll(filepath.Join(activeRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir active data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(activeRoot, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write active stale file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(activeRoot, "data", "old.db"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write active stale data: %v", err)
	}
	cloneRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cloneRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir clone data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cloneRoot, "compose.yml"), []byte("services:\n  app:\n    image: busybox\n"), 0o644); err != nil {
		t.Fatalf("write clone compose: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cloneRoot, "data", "snapshot.db"), []byte("snapshot"), 0o644); err != nil {
		t.Fatalf("write clone snapshot data: %v", err)
	}
	placeErr := errors.New("final placement failed")
	var stagedPath string
	withServiceRootRestorePlacement(t, func(stage, target string) error {
		stagedPath = stage
		compose, err := os.ReadFile(filepath.Join(stage, "compose.yml"))
		if err != nil {
			t.Fatalf("read staged compose before partial placement: %v", err)
		}
		if err := os.WriteFile(filepath.Join(target, "compose.yml"), compose, 0o644); err != nil {
			t.Fatalf("partially copy staged compose: %v", err)
		}
		return placeErr
	})

	err := restoreServiceRootFromCloneMountpoint(cloneRoot, activeRoot)

	if !errors.Is(err, placeErr) {
		t.Fatalf("restoreServiceRootFromCloneMountpoint error = %v, want final placement error", err)
	}
	if stagedPath == "" || !strings.Contains(err.Error(), stagedPath) {
		t.Fatalf("restoreServiceRootFromCloneMountpoint error = %v, want preserved stage path %q", err, stagedPath)
	}
	gotStale, err := os.ReadFile(filepath.Join(activeRoot, "stale.txt"))
	if err != nil {
		t.Fatalf("read original active root file after failed placement: %v", err)
	}
	if string(gotStale) != "stale" {
		t.Fatalf("active stale file = %q, want original stale content", gotStale)
	}
	gotOld, err := os.ReadFile(filepath.Join(activeRoot, "data", "old.db"))
	if err != nil {
		t.Fatalf("read original active data after failed placement: %v", err)
	}
	if string(gotOld) != "old" {
		t.Fatalf("active old data = %q, want original old content", gotOld)
	}
	gotSnapshot, err := os.ReadFile(filepath.Join(stagedPath, "data", "snapshot.db"))
	if err != nil {
		t.Fatalf("read preserved staged snapshot data: %v", err)
	}
	if string(gotSnapshot) != "snapshot" {
		t.Fatalf("preserved staged snapshot data = %q, want snapshot", gotSnapshot)
	}
	if _, err := os.Stat(filepath.Join(stagedPath, "compose.yml")); err != nil {
		t.Fatalf("stat preserved staged compose file after failed placement: %v", err)
	}
}

func TestRestoreServiceRootFromCloneMountpointDoesNotRenameActiveRootMountpoint(t *testing.T) {
	parent := t.TempDir()
	activeRoot := filepath.Join(parent, "app")
	if err := os.MkdirAll(filepath.Join(activeRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir active data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(activeRoot, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write active stale file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(activeRoot, "data", "old.db"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write active stale data: %v", err)
	}
	cloneRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cloneRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir clone data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cloneRoot, "compose.yml"), []byte("services:\n  app:\n    image: busybox\n"), 0o644); err != nil {
		t.Fatalf("write clone compose: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cloneRoot, "data", "snapshot.db"), []byte("snapshot"), 0o644); err != nil {
		t.Fatalf("write clone snapshot data: %v", err)
	}
	before, err := os.Stat(activeRoot)
	if err != nil {
		t.Fatalf("stat active root before restore: %v", err)
	}
	withServiceRootRestoreRename(t, func(oldPath, newPath string) error {
		if filepath.Clean(oldPath) == filepath.Clean(activeRoot) || filepath.Clean(newPath) == filepath.Clean(activeRoot) {
			t.Fatalf("attempted to rename active service-root mountpoint: %s -> %s", oldPath, newPath)
		}
		return os.Rename(oldPath, newPath)
	})

	if err := restoreServiceRootFromCloneMountpoint(cloneRoot, activeRoot); err != nil {
		t.Fatalf("restoreServiceRootFromCloneMountpoint: %v", err)
	}
	after, err := os.Stat(activeRoot)
	if err != nil {
		t.Fatalf("stat active root after restore: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("active service-root mountpoint directory identity changed after restore")
	}
	if _, err := os.Stat(filepath.Join(activeRoot, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale active root file stat err = %v, want removed", err)
	}
	if _, err := os.Stat(filepath.Join(activeRoot, "data", "old.db")); !os.IsNotExist(err) {
		t.Fatalf("stale active root data stat err = %v, want removed", err)
	}
	gotSnapshot, err := os.ReadFile(filepath.Join(activeRoot, "data", "snapshot.db"))
	if err != nil {
		t.Fatalf("read restored snapshot data: %v", err)
	}
	if string(gotSnapshot) != "snapshot" {
		t.Fatalf("restored snapshot data = %q, want snapshot", gotSnapshot)
	}
}

func TestRestoreServiceRootFromCloneMountpointHandlesCrossDeviceActiveBackup(t *testing.T) {
	parent := t.TempDir()
	activeRoot := filepath.Join(parent, "app")
	if err := os.MkdirAll(filepath.Join(activeRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir active data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(activeRoot, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write active stale file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(activeRoot, "data", "old.db"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write active stale data: %v", err)
	}
	cloneRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cloneRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir clone data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cloneRoot, "compose.yml"), []byte("services:\n  app:\n    image: busybox\n"), 0o644); err != nil {
		t.Fatalf("write clone compose: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cloneRoot, "data", "snapshot.db"), []byte("snapshot"), 0o644); err != nil {
		t.Fatalf("write clone snapshot data: %v", err)
	}
	before, err := os.Stat(activeRoot)
	if err != nil {
		t.Fatalf("stat active root before restore: %v", err)
	}
	withServiceRootRestoreRename(t, func(oldPath, newPath string) error {
		if filepath.Clean(oldPath) == filepath.Clean(activeRoot) || filepath.Clean(newPath) == filepath.Clean(activeRoot) {
			t.Fatalf("attempted to rename active service-root mountpoint: %s -> %s", oldPath, newPath)
		}
		if pathWithinRoot(oldPath, activeRoot) && !pathWithinRoot(newPath, activeRoot) {
			return syscall.EXDEV
		}
		return os.Rename(oldPath, newPath)
	})

	if err := restoreServiceRootFromCloneMountpoint(cloneRoot, activeRoot); err != nil {
		t.Fatalf("restoreServiceRootFromCloneMountpoint: %v", err)
	}
	after, err := os.Stat(activeRoot)
	if err != nil {
		t.Fatalf("stat active root after restore: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("active service-root mountpoint directory identity changed after restore")
	}
	if _, err := os.Stat(filepath.Join(activeRoot, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale active root file stat err = %v, want removed", err)
	}
	if _, err := os.Stat(filepath.Join(activeRoot, "data", "old.db")); !os.IsNotExist(err) {
		t.Fatalf("stale active root data stat err = %v, want removed", err)
	}
	gotSnapshot, err := os.ReadFile(filepath.Join(activeRoot, "data", "snapshot.db"))
	if err != nil {
		t.Fatalf("read restored snapshot data: %v", err)
	}
	if string(gotSnapshot) != "snapshot" {
		t.Fatalf("restored snapshot data = %q, want snapshot", gotSnapshot)
	}
	assertNoServiceRootRestoreScratch(t, activeRoot)
	assertNoServiceRootRestoreScratch(t, parent)
}

func TestPlaceAndCleanupServiceRootRestoreStageReportsStageWhenSetupFails(t *testing.T) {
	parent := t.TempDir()
	activeRoot := filepath.Join(parent, "app")
	if err := os.MkdirAll(activeRoot, 0o755); err != nil {
		t.Fatalf("mkdir active root: %v", err)
	}
	stage := filepath.Join(parent, ".yeet-service-root-restore-complete")
	if err := os.MkdirAll(filepath.Join(stage, "data"), 0o755); err != nil {
		t.Fatalf("mkdir completed stage data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stage, "data", "snapshot.db"), []byte("snapshot"), 0o644); err != nil {
		t.Fatalf("write completed stage data: %v", err)
	}
	if err := os.Chmod(activeRoot, 0o555); err != nil {
		t.Fatalf("chmod active root: %v", err)
	}
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatalf("chmod active parent: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(parent, 0o755)
		_ = os.Chmod(activeRoot, 0o755)
	})

	err := placeAndCleanupServiceRootRestoreStage(stage, activeRoot)

	if err == nil {
		t.Fatal("placeAndCleanupServiceRootRestoreStage succeeded, want setup failure")
	}
	if !strings.Contains(err.Error(), stage) {
		t.Fatalf("placeAndCleanupServiceRootRestoreStage error = %v, want completed stage path %q", err, stage)
	}
	gotSnapshot, readErr := os.ReadFile(filepath.Join(stage, "data", "snapshot.db"))
	if readErr != nil {
		t.Fatalf("read completed stage after setup failure: %v", readErr)
	}
	if string(gotSnapshot) != "snapshot" {
		t.Fatalf("completed stage data = %q, want snapshot", gotSnapshot)
	}
}

func TestPlaceAndCleanupServiceRootRestoreStageKeepsScratchOutOfActiveContents(t *testing.T) {
	parent := t.TempDir()
	activeRoot := filepath.Join(parent, "app")
	if err := os.MkdirAll(filepath.Join(activeRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir active data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(activeRoot, "data", "old.db"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write active stale data: %v", err)
	}
	stage := filepath.Join(parent, ".yeet-service-root-restore-complete")
	if err := os.MkdirAll(filepath.Join(stage, "data"), 0o755); err != nil {
		t.Fatalf("mkdir completed stage data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stage, "data", "snapshot.db"), []byte("snapshot"), 0o644); err != nil {
		t.Fatalf("write completed stage data: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(stage, ".yeet-service-root-restore-stale"), 0o755); err != nil {
		t.Fatalf("mkdir completed stage scratch: %v", err)
	}
	var sawScratch bool
	withServiceRootRestorePlacement(t, func(_, target string) error {
		entries, err := os.ReadDir(target)
		if err != nil {
			t.Fatalf("read active root during placement: %v", err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".yeet-service-root-restore-") {
				sawScratch = true
			}
		}
		return nil
	})

	if err := placeAndCleanupServiceRootRestoreStage(stage, activeRoot); err != nil {
		t.Fatalf("placeAndCleanupServiceRootRestoreStage: %v", err)
	}

	if !sawScratch {
		t.Fatal("placement did not create transient scratch inside active root")
	}
	if _, err := os.Stat(filepath.Join(activeRoot, "data", "old.db")); !os.IsNotExist(err) {
		t.Fatalf("stale active root data stat err = %v, want removed", err)
	}
	gotSnapshot, err := os.ReadFile(filepath.Join(activeRoot, "data", "snapshot.db"))
	if err != nil {
		t.Fatalf("read restored snapshot data: %v", err)
	}
	if string(gotSnapshot) != "snapshot" {
		t.Fatalf("restored snapshot data = %q, want snapshot", gotSnapshot)
	}
	assertNoServiceRootRestoreScratch(t, activeRoot)
	assertNoServiceRootRestoreScratch(t, parent)
}

func TestPlaceAndCleanupServiceRootRestoreStageReplacesActiveRootAndCleansScratch(t *testing.T) {
	parent := t.TempDir()
	activeRoot := filepath.Join(parent, "app")
	if err := os.MkdirAll(filepath.Join(activeRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir active data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(activeRoot, "stale.txt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write active stale file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(activeRoot, "data", "old.db"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write active stale data: %v", err)
	}
	stage := filepath.Join(parent, ".yeet-service-root-restore-complete")
	if err := os.MkdirAll(filepath.Join(stage, "data"), 0o755); err != nil {
		t.Fatalf("mkdir completed stage data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stage, "compose.yml"), []byte("services:\n  app:\n    image: busybox\n"), 0o644); err != nil {
		t.Fatalf("write completed stage compose: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stage, "data", "snapshot.db"), []byte("snapshot"), 0o644); err != nil {
		t.Fatalf("write completed stage data: %v", err)
	}

	if err := placeAndCleanupServiceRootRestoreStage(stage, activeRoot); err != nil {
		t.Fatalf("placeAndCleanupServiceRootRestoreStage: %v", err)
	}

	if _, err := os.Stat(filepath.Join(activeRoot, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale active root file stat err = %v, want removed", err)
	}
	if _, err := os.Stat(filepath.Join(activeRoot, "data", "old.db")); !os.IsNotExist(err) {
		t.Fatalf("stale active root data stat err = %v, want removed", err)
	}
	gotSnapshot, err := os.ReadFile(filepath.Join(activeRoot, "data", "snapshot.db"))
	if err != nil {
		t.Fatalf("read restored snapshot data: %v", err)
	}
	if string(gotSnapshot) != "snapshot" {
		t.Fatalf("restored snapshot data = %q, want snapshot", gotSnapshot)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("completed stage stat err = %v, want removed after success", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read active root parent: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".yeet-service-root-restore-") {
			t.Fatalf("restore scratch %q remains beside active root", entry.Name())
		}
	}
}

func TestSnapshotsCloneServiceRootDefaultDefinitionInstallDoesNotStartCompose(t *testing.T) {
	server := newTestServer(t)
	seedServiceRootRecoverySource(t, server)
	targetDataset := "flash/yeet/services/app-restore"
	targetRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(targetRoot, "data"), 0o755); err != nil {
		t.Fatalf("mkdir cloned data root: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(targetRoot, "run"), 0o755); err != nil {
		t.Fatalf("mkdir cloned run root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetRoot, "compose.yml"), []byte("services:\n  app:\n    image: busybox\n"), 0o644); err != nil {
		t.Fatalf("write cloned compose: %v", err)
	}
	commandLog := installServiceRootRecoveryFakeCommands(t)
	var calls []string
	server.zfsRunner = serviceRootRecoveryZFSRunner(t, &calls, map[string]string{
		serviceRootRecoveryDataset: serviceRootRecoverySnapshotLine(serviceRootRecoverySnapshot, "app", 3),
	}, map[string]string{
		targetDataset: targetRoot,
	})

	err := server.cloneRecoveryPoint(context.Background(), "app", "yeet-20260613T203100Z", "app-restore", cli.SnapshotsCloneFlags{}, ioDiscardReadWriter{})
	if err != nil {
		t.Fatalf("cloneRecoveryPoint: %v", err)
	}

	log := readRecoveryLog(t, commandLog)
	if strings.Contains(log, " compose ") && strings.Contains(log, " up") {
		t.Fatalf("command log = %q, clone definition install should not run docker compose up", log)
	}
	if strings.Contains(log, "systemctl restart") || strings.Contains(log, "systemctl start") {
		t.Fatalf("command log = %q, clone definition install should not start/restart units", log)
	}
}

func TestSnapshotsRestoreServiceRootGenerationSnapshotDoesNotRestartSystemd(t *testing.T) {
	server := newTestServer(t)
	addTestServices(t, server, db.Service{
		Name:             "app",
		ServiceType:      db.ServiceTypeSystemd,
		ServiceRoot:      t.TempDir(),
		ServiceRootZFS:   serviceRootRecoveryDataset,
		Generation:       4,
		LatestGeneration: 4,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {
				Refs: map[db.ArtifactRef]string{
					db.Gen(2): serviceRootRecoveryRoot + "/app-g2.service",
					"latest":  serviceRootRecoveryRoot + "/app-g4.service",
				},
			},
		},
	})
	oldInstall := installServiceRootRestoreGeneration
	installServiceRootRestoreGeneration = func(_ *Server, service *db.Service, gen int) error {
		if service.Generation != 2 || gen != 2 {
			t.Fatalf("install generation args = service:%d gen:%d, want 2/2", service.Generation, gen)
		}
		return nil
	}
	t.Cleanup(func() { installServiceRootRestoreGeneration = oldInstall })
	commandLog := installServiceRootRecoveryFakeCommands(t)
	var calls []string
	server.zfsRunner = serviceRootRecoveryZFSRunner(t, &calls, map[string]string{
		serviceRootRecoveryDataset: serviceRootRecoverySnapshotLine(serviceRootRecoverySnapshot, "app", 2),
	}, nil)

	err := server.restoreRecoveryPoint(context.Background(), "app", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true, Generation: "snapshot"}, ioDiscardReadWriter{})
	if err != nil {
		t.Fatalf("restoreRecoveryPoint: %v", err)
	}

	log := readRecoveryLog(t, commandLog)
	if strings.Contains(log, "systemctl restart") || strings.Contains(log, "systemctl start") {
		t.Fatalf("command log = %q, restore generation install should not start/restart units", log)
	}
	if strings.Contains(log, " compose ") && strings.Contains(log, " up") {
		t.Fatalf("command log = %q, restore generation install should not run docker compose up", log)
	}
}

func TestSnapshotsRestoreServiceRootGenerationSnapshotRestoresRecordedGeneration(t *testing.T) {
	server := newTestServer(t)
	seedServiceRootRecoveryRestoreSource(t, server, 4, 4)
	var installedGen int
	oldInstall := installServiceRootRestoreGeneration
	installServiceRootRestoreGeneration = func(_ *Server, service *db.Service, gen int) error {
		if service.Name != "app" {
			t.Fatalf("install service = %q, want app", service.Name)
		}
		installedGen = gen
		if service.Generation != 2 || service.LatestGeneration != 4 {
			t.Fatalf("install generation/latest = %d/%d, want 2/4", service.Generation, service.LatestGeneration)
		}
		if got := service.Artifacts[db.ArtifactSystemdUnit].Refs["latest"]; got != serviceRootRecoveryRoot+"/app-g2.service" {
			t.Fatalf("install latest unit = %q, want generation 2 ref", got)
		}
		return nil
	}
	t.Cleanup(func() { installServiceRootRestoreGeneration = oldInstall })
	var calls []string
	server.zfsRunner = serviceRootRecoveryZFSRunner(t, &calls, map[string]string{
		serviceRootRecoveryDataset: serviceRootRecoverySnapshotLine(serviceRootRecoverySnapshot, "app", 2),
	}, nil)
	var out bytes.Buffer

	err := server.restoreRecoveryPoint(context.Background(), "app", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true, Generation: "snapshot"}, &out)
	if err != nil {
		t.Fatalf("restoreRecoveryPoint: %v", err)
	}

	got := mustService(t, server, "app")
	if got.Generation != 2 {
		t.Fatalf("generation = %d, want 2", got.Generation)
	}
	if installedGen != 2 {
		t.Fatalf("installed generation = %d, want 2", installedGen)
	}
	if got.LatestGeneration != 4 {
		t.Fatalf("latest generation = %d, want 4", got.LatestGeneration)
	}
	unitRefs := got.Artifacts[db.ArtifactSystemdUnit].Refs
	if unitRefs["latest"] != serviceRootRecoveryRoot+"/app-g2.service" {
		t.Fatalf("latest unit ref = %q, want generation 2 ref", unitRefs["latest"])
	}
	if !strings.Contains(out.String(), "Restored service definition generation: 2") {
		t.Fatalf("output = %q, want generation restore progress", out.String())
	}
}

func TestSnapshotsRestoreServiceRootGenerationSnapshotRejectsMissingRequiredArtifactBeforeInstall(t *testing.T) {
	for _, tt := range []struct {
		name         string
		serviceType  db.ServiceType
		artifactName db.ArtifactName
		artifacts    db.ArtifactStore
	}{
		{
			name:         "systemd missing artifact entry",
			serviceType:  db.ServiceTypeSystemd,
			artifactName: db.ArtifactSystemdUnit,
			artifacts: db.ArtifactStore{
				db.ArtifactEnvFile: {
					Refs: map[db.ArtifactRef]string{
						"latest": "/etc/yeet/app.env",
					},
				},
			},
		},
		{
			name:         "docker compose missing artifact entry",
			serviceType:  db.ServiceTypeDockerCompose,
			artifactName: db.ArtifactDockerComposeFile,
			artifacts: db.ArtifactStore{
				db.ArtifactEnvFile: {
					Refs: map[db.ArtifactRef]string{
						"latest": "/etc/yeet/app.env",
					},
				},
			},
		},
		{
			name:         "systemd nil artifact",
			serviceType:  db.ServiceTypeSystemd,
			artifactName: db.ArtifactSystemdUnit,
			artifacts: db.ArtifactStore{
				db.ArtifactSystemdUnit: nil,
			},
		},
		{
			name:         "systemd nil artifact refs",
			serviceType:  db.ServiceTypeSystemd,
			artifactName: db.ArtifactSystemdUnit,
			artifacts: db.ArtifactStore{
				db.ArtifactSystemdUnit: {},
			},
		},
		{
			name:         "systemd missing generation ref",
			serviceType:  db.ServiceTypeSystemd,
			artifactName: db.ArtifactSystemdUnit,
			artifacts: db.ArtifactStore{
				db.ArtifactSystemdUnit: {
					Refs: map[db.ArtifactRef]string{
						db.Gen(4): serviceRootRecoveryRoot + "/app-g4.service",
						"latest":  serviceRootRecoveryRoot + "/app-g4.service",
					},
				},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			addTestServices(t, server, db.Service{
				Name:             "app",
				ServiceType:      tt.serviceType,
				ServiceRoot:      t.TempDir(),
				Generation:       4,
				LatestGeneration: 4,
				Artifacts:        tt.artifacts,
			})
			before := mustService(t, server, "app")
			var installCalled bool
			oldInstall := installServiceRootRestoreGeneration
			installServiceRootRestoreGeneration = func(_ *Server, _ *db.Service, _ int) error {
				installCalled = true
				return nil
			}
			t.Cleanup(func() { installServiceRootRestoreGeneration = oldInstall })

			err := server.restoreServiceRootSnapshotGeneration("app", recoveryPoint{
				ShortName:  "yeet-20260613T203100Z",
				Generation: intPointer(2),
			}, "snapshot")

			wantErr := "snapshot generation 2 is missing retained artifact ref " + string(tt.artifactName)
			if err == nil || !strings.Contains(err.Error(), wantErr) {
				t.Fatalf("restoreRecoveryPoint error = %v, want %q", err, wantErr)
			}
			if installCalled {
				t.Fatal("installer called despite missing snapshot generation artifact ref")
			}
			got := mustService(t, server, "app")
			if got.Generation != before.Generation || got.LatestGeneration != before.LatestGeneration {
				t.Fatalf("generation/latest after rejected restore = %d/%d, want %d/%d", got.Generation, got.LatestGeneration, before.Generation, before.LatestGeneration)
			}
			if !reflect.DeepEqual(got.Artifacts, before.Artifacts) {
				t.Fatalf("artifacts after rejected restore = %#v, want unchanged %#v", got.Artifacts, before.Artifacts)
			}
		})
	}
}

func TestSnapshotsRestoreServiceRootGenerationSnapshotRollsBackDBOnInstallFailure(t *testing.T) {
	server := newTestServer(t)
	seedServiceRootRecoveryRestoreSource(t, server, 4, 4)
	installErr := errors.New("install restored generation failed")
	oldInstall := installServiceRootRestoreGeneration
	installServiceRootRestoreGeneration = func(_ *Server, service *db.Service, gen int) error {
		if service.Generation != 2 || gen != 2 {
			t.Fatalf("install generation args = service:%d gen:%d, want 2/2", service.Generation, gen)
		}
		return installErr
	}
	t.Cleanup(func() { installServiceRootRestoreGeneration = oldInstall })
	var calls []string
	server.zfsRunner = serviceRootRecoveryZFSRunner(t, &calls, map[string]string{
		serviceRootRecoveryDataset: serviceRootRecoverySnapshotLine(serviceRootRecoverySnapshot, "app", 2),
	}, nil)

	err := server.restoreRecoveryPoint(context.Background(), "app", "yeet-20260613T203100Z", cli.SnapshotsRestoreFlags{Yes: true, Generation: "snapshot"}, ioDiscardReadWriter{})

	if !errors.Is(err, installErr) {
		t.Fatalf("restoreRecoveryPoint error = %v, want install error", err)
	}
	got := mustService(t, server, "app")
	if got.Generation != 4 || got.LatestGeneration != 4 {
		t.Fatalf("generation/latest after failed install = %d/%d, want rolled back to 4/4", got.Generation, got.LatestGeneration)
	}
	unitRefs := got.Artifacts[db.ArtifactSystemdUnit].Refs
	if unitRefs["latest"] != serviceRootRecoveryRoot+"/app-g4.service" {
		t.Fatalf("latest unit ref after failed install = %q, want generation 4 ref", unitRefs["latest"])
	}
}

func seedServiceRootRecoverySource(t *testing.T, server *Server) {
	t.Helper()
	addTestServices(t, server, db.Service{
		Name:             "app",
		ServiceType:      db.ServiceTypeDockerCompose,
		ServiceRoot:      serviceRootRecoveryRoot,
		ServiceRootZFS:   serviceRootRecoveryDataset,
		Generation:       3,
		LatestGeneration: 3,
		Artifacts: db.ArtifactStore{
			db.ArtifactDockerComposeFile: {
				Refs: map[db.ArtifactRef]string{
					"latest":  serviceRootRecoveryRoot + "/compose.yml",
					db.Gen(3): serviceRootRecoveryRoot + "/compose.yml",
				},
			},
			db.ArtifactEnvFile: {
				Refs: map[db.ArtifactRef]string{
					"latest": "/etc/yeet/app.env",
				},
			},
			db.ArtifactBinary: {
				Refs: map[db.ArtifactRef]string{
					"latest": serviceRootRecoveryRoot + "-sibling/bin/app",
				},
			},
		},
		SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.42.10")},
		Macvlan:    &db.MacvlanNetwork{Interface: "macvlan0", Mac: "02:00:00:00:00:01", Parent: "eth0"},
		TSNet:      &db.TailscaleNetwork{Interface: "tailscale0", Version: "1.92.3"},
	})
}

func withServiceRootCloneInstall(t *testing.T, install serviceRootCloneDefinitionInstaller) {
	t.Helper()
	oldInstall := installServiceRootCloneDefinition
	installServiceRootCloneDefinition = install
	t.Cleanup(func() { installServiceRootCloneDefinition = oldInstall })
}

func withServiceRootCloneStop(t *testing.T, stop serviceRootCloneStopper) {
	t.Helper()
	oldStop := stopServiceRootClone
	stopServiceRootClone = stop
	t.Cleanup(func() { stopServiceRootClone = oldStop })
}

func withServiceRootCloneArtifactRewrite(t *testing.T, rewrite serviceRootCloneArtifactRewriter) {
	t.Helper()
	oldRewrite := rewriteServiceRootCloneArtifacts
	rewriteServiceRootCloneArtifacts = rewrite
	t.Cleanup(func() { rewriteServiceRootCloneArtifacts = oldRewrite })
}

func withServiceRootRestorePlacement(t *testing.T, place func(string, string) error) {
	t.Helper()
	oldPlace := placeServiceRootRestoreStage
	placeServiceRootRestoreStage = place
	t.Cleanup(func() { placeServiceRootRestoreStage = oldPlace })
}

func withServiceRootRestoreRename(t *testing.T, rename func(string, string) error) {
	t.Helper()
	oldRename := renameServiceRoot
	renameServiceRoot = rename
	t.Cleanup(func() { renameServiceRoot = oldRename })
}

func pathWithinRoot(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".."
}

func assertNoServiceRootRestoreScratch(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read %s entries: %v", root, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".yeet-service-root-restore-") {
			t.Fatalf("restore scratch %q remains under %s", entry.Name(), root)
		}
	}
}

func seedServiceRootRecoveryRestoreSource(t *testing.T, server *Server, generation int, latestGeneration int) {
	t.Helper()
	activeRoot := t.TempDir()
	addTestServices(t, server, db.Service{
		Name:             "app",
		ServiceType:      db.ServiceTypeSystemd,
		ServiceRoot:      activeRoot,
		ServiceRootZFS:   serviceRootRecoveryDataset,
		Generation:       generation,
		LatestGeneration: latestGeneration,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {
				Refs: map[db.ArtifactRef]string{
					db.Gen(2): serviceRootRecoveryRoot + "/app-g2.service",
					db.Gen(3): serviceRootRecoveryRoot + "/app-g3.service",
					db.Gen(4): serviceRootRecoveryRoot + "/app-g4.service",
					"latest":  serviceRootRecoveryRoot + "/app-g4.service",
				},
			},
		},
	})
}

func serviceRootRecoverySnapshotLine(snapshot string, serviceName string, generation int) string {
	return snapshot + "\t1781382660\tcatch\t" + serviceName + "\trun\t" + strconv.Itoa(generation) + "\tbefore deploy\tservice-root\tfalse\n"
}

func serviceRootRecoveryZFSRunner(t *testing.T, calls *[]string, lists map[string]string, mountpoints map[string]string) zfsCommandRunner {
	t.Helper()
	return func(_ context.Context, args ...string) (string, string, error) {
		command := strings.Join(args, " ")
		*calls = append(*calls, command)
		if isRecoverySnapshotList(args) {
			if out, ok := lists[args[len(args)-1]]; ok {
				return out, "", nil
			}
			return "", "dataset does not exist", errors.New("dataset does not exist")
		}
		if strings.HasPrefix(command, "list -H -o name ") {
			return "", "dataset does not exist", errors.New("dataset does not exist")
		}
		if strings.HasPrefix(command, "get -H -o value mountpoint ") {
			dataset := strings.TrimPrefix(command, "get -H -o value mountpoint ")
			if mountpoint, ok := mountpoints[dataset]; ok {
				return mountpoint + "\n", "", nil
			}
			if strings.Contains(dataset, "-restore-") {
				return t.TempDir() + "\n", "", nil
			}
			return "", "mountpoint unavailable", errors.New("mountpoint unavailable")
		}
		return "", "", nil
	}
}

func installServiceRootRecoveryFakeCommands(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "commands.log")
	systemctlScript := "#!/bin/sh\nprintf 'systemctl %s\\n' \"$*\" >> " + strconv.Quote(logPath) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(systemctlScript), 0o755); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
	}
	dockerScript := "#!/bin/sh\nprintf 'docker %s\\n' \"$*\" >> " + strconv.Quote(logPath) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(dockerScript), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}
