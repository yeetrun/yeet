// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
	"golang.org/x/sys/unix"
)

func currentTestServiceIdentity() db.ServiceIdentity {
	return db.ServiceIdentity{
		RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
	}
}

func TestRestoreServiceIdentityRuntimeStateReconcilesOrderedUnits(t *testing.T) {
	oldActive, oldSystemctl := catchSystemdUnitActive, catchSystemctl
	active := map[string]bool{"api.service": true}
	catchSystemdUnitActive = func(unit string) bool { return active[unit] }
	catchSystemctl = func(args ...string) error {
		if len(args) != 2 {
			return fmt.Errorf("unexpected systemctl args: %v", args)
		}
		active[args[1]] = args[0] == "start"
		return nil
	}
	t.Cleanup(func() { catchSystemdUnitActive, catchSystemctl = oldActive, oldSystemctl })

	service := &db.Service{
		Name: "api", Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/timer"}},
			db.ArtifactTSService:        {Refs: map[db.ArtifactRef]string{db.Gen(1): "/ts"}},
			db.ArtifactNetNSService:     {Refs: map[db.ArtifactRef]string{db.Gen(1): "/netns"}},
		},
	}
	desired := []serviceIdentityRuntimeUnitState{
		{Unit: "api.timer", Active: true},
		{Unit: "api.service", Active: false},
		{Unit: "yeet-api-ts.service", Active: true},
		{Unit: "yeet-api-ns.service", Active: true},
	}
	if err := restoreServiceIdentityRuntimeState(context.Background(), service, "api", desired); err != nil {
		t.Fatalf("restoreServiceIdentityRuntimeState: %v", err)
	}
}

func TestClearServiceIdentityZFSDatasetMarkerVerifiesGUIDAndMarker(t *testing.T) {
	var inherited bool
	runner := func(_ context.Context, args ...string) (string, string, error) {
		switch {
		case len(args) == 5 && args[0] == "list":
			return "tank/api\n", "", nil
		case len(args) == 6 && args[0] == "get" && args[4] == "guid":
			return "guid-1\n", "", nil
		case len(args) == 6 && args[0] == "get" && args[4] == serviceIdentityZFSMarkerProperty:
			return "tx-1\n", "", nil
		case len(args) == 3 && args[0] == "inherit":
			inherited = true
			return "", "", nil
		default:
			return "", "", fmt.Errorf("unexpected zfs args: %v", args)
		}
	}
	if err := clearServiceIdentityZFSDatasetMarker(context.Background(), runner, "tank/api", "guid-1", "tx-1"); err != nil {
		t.Fatalf("clearServiceIdentityZFSDatasetMarker: %v", err)
	}
	if !inherited {
		t.Fatal("transaction marker was not inherited")
	}
}

func TestServiceIdentityMigrationVerifyChecksEffectiveUnit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, serviceBinDirForRoot(root), serviceDataDirForRoot(root), serviceEnvDirForRoot(root), serviceRunDirForRoot(root)} {
		if err := os.Chmod(path, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	identity := currentTestServiceIdentity()
	unitPath := filepath.Join(t.TempDir(), "api.service")
	unit := "[Service]\nUser=" + identity.RequestedUser + "\nGroup=" + identity.RequestedGroup + "\nWorkingDirectory=" + serviceDataDirForRoot(root) + "\n"
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	oldProperties, oldOwner := readServiceIdentitySystemdProperties, nativeServiceOwner
	readServiceIdentitySystemdProperties = func(context.Context, string) (serviceIdentitySystemdProperties, error) {
		return serviceIdentitySystemdProperties{
			User: identity.RequestedUser, Group: identity.RequestedGroup,
			Environment: map[string]string{
				"HOME": serviceDataDirForRoot(root), "USER": identity.RequestedUser,
				"LOGNAME": identity.RequestedUser, "SHELL": "/bin/sh",
			},
		}, nil
	}
	nativeServiceOwner = func(info os.FileInfo) (uint32, uint32, error) {
		if info.Name() == filepath.Base(root) || info.Name() == "bin" || info.Name() == "env" {
			return 0, identity.GID, nil
		}
		return identity.UID, identity.GID, nil
	}
	t.Cleanup(func() { readServiceIdentitySystemdProperties, nativeServiceOwner = oldProperties, oldOwner })
	migration := &serviceIdentityMigration{ops: serviceIdentityMigrationOps{
		isTargetRunning: func(context.Context, string) (bool, error) { return false, nil },
	}}
	if err := migration.verify(context.Background(), serviceIdentityMigrationVerification{
		Service: "api", UnitPath: unitPath, Identity: identity, Root: root,
	}); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestServiceIdentityCopyGuardCopiesIntoMountedRootAndSymlink(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	target := filepath.Join(t.TempDir(), "target")
	if err := ensureDirsForRoot(source, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceDataDirForRoot(source), "state"), []byte("state"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("state", filepath.Join(serviceDataDirForRoot(source), "current")); err != nil {
		t.Fatal(err)
	}
	guard := newTestServiceIdentityCopyGuard(t, source)
	if err := guard.copyIntoMountedRoot(target); err != nil {
		t.Fatalf("copyIntoMountedRoot: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(target, "data", "state")); err != nil || string(got) != "state" {
		t.Fatalf("copied state = %q, %v", got, err)
	}
	if got, err := os.Readlink(filepath.Join(target, "data", "current")); err != nil || got != "state" {
		t.Fatalf("copied symlink = %q, %v", got, err)
	}
}

func TestNativeArchiveCopyPublishesMergedTree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	identity := currentTestServiceIdentity()
	descriptors, err := openNativeCopyDescriptors(root, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer descriptors.close()
	var payload bytes.Buffer
	tw := tar.NewWriter(&payload)
	if err := tw.WriteHeader(&tar.Header{Name: "nested", Typeflag: tar.TypeDir, Mode: 0o750}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "nested/state", Typeflag: tar.TypeReg, Mode: 0o640, Size: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("state")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	execer := &ttyExecer{rw: bytes.NewBuffer(payload.Bytes())}
	if err := execer.copyArchiveToNativeRemote(false, "restore", descriptors); err != nil {
		t.Fatalf("copyArchiveToNativeRemote: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(serviceDataDirForRoot(root), "restore", "nested", "state"))
	if err != nil || string(got) != "state" {
		t.Fatalf("published archive state = %q, %v", got, err)
	}
}

func TestMergeAndRemoveNativeCopyTreeUsesStableDescriptors(t *testing.T) {
	source, destination := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(destination, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "new"), []byte("new"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "nested", "old"), []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	sourceFD, err := unix.Open(source, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(sourceFD) }()
	destinationFD, err := unix.Open(destination, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(destinationFD) }()
	if err := mergeNativeCopyTree(sourceFD, destinationFD); err != nil {
		t.Fatalf("mergeNativeCopyTree: %v", err)
	}
	if err := removeNativeCopyTreeAt(destinationFD, "nested"); err != nil {
		t.Fatalf("removeNativeCopyTreeAt: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(destination, "nested")); !os.IsNotExist(err) {
		t.Fatalf("removed tree remains: %v", err)
	}
}

func TestApplyPersistedNativeServiceLayoutOwnsManagedDirectories(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "api")
	identity := currentTestServiceIdentity()
	service := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root, Identity: &identity}
	oldLchown := nativeServiceLchown
	var paths []string
	nativeServiceLchown = func(path string, _, _ int) error {
		paths = append(paths, filepath.Base(path))
		return nil
	}
	t.Cleanup(func() { nativeServiceLchown = oldLchown })
	if err := (&Installer{s: server}).applyPersistedNativeServiceLayout(service); err != nil {
		t.Fatalf("applyPersistedNativeServiceLayout: %v", err)
	}
	if len(paths) != 5 || !strings.Contains(strings.Join(paths, ","), "data") {
		t.Fatalf("managed ownership paths = %v", paths)
	}
}

func TestPrepareCopyParentCreatesOwnedNestedDirectory(t *testing.T) {
	server := newTestServer(t)
	root := server.defaultServiceRootDir("api")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	identity := currentTestServiceIdentity()
	oldLchown := nativeServiceLchown
	var calls int
	nativeServiceLchown = func(string, int, int) error { calls++; return nil }
	t.Cleanup(func() { nativeServiceLchown = oldLchown })
	execer := &ttyExecer{s: server, sn: "api"}
	parent := filepath.Join(serviceDataDirForRoot(root), "one", "two")
	if err := execer.prepareCopyParent(parent, &identity); err != nil {
		t.Fatalf("prepareCopyParent: %v", err)
	}
	if calls != 2 {
		t.Fatalf("ownership calls = %d, want 2", calls)
	}
}

func TestServiceIdentityMaterializesFilesystemAndZFSTargets(t *testing.T) {
	for _, zfs := range []bool{false, true} {
		t.Run(map[bool]string{false: "filesystem", true: "zfs"}[zfs], func(t *testing.T) {
			server := newTestServer(t)
			parent := t.TempDir()
			oldRoot := filepath.Join(parent, "old")
			if err := ensureDirsForRoot(oldRoot, ""); err != nil {
				t.Fatal(err)
			}
			newRoot := filepath.Join(parent, "new")
			plan := serviceRootMigrationPlan{
				ServiceName: "api", OldRoot: oldRoot, NewRoot: newRoot, Mode: serviceRootMigrationEmpty,
				CreateNewRootZFS: zfs,
			}
			if zfs {
				plan.NewRootZFS = "tank/api"
				server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
					switch {
					case args[0] == "create":
						return "", "", os.Mkdir(newRoot, 0o755)
					case args[0] == "get" && args[4] == "mountpoint":
						return newRoot + "\n", "", nil
					case args[0] == "get" && args[4] == "guid":
						return "guid-api\n", "", nil
					default:
						return "", "", fmt.Errorf("unexpected zfs args: %v", args)
					}
				}
			}
			identity := currentTestServiceIdentity()
			journal, err := createServiceIdentityJournal(server.cfg.RootDir, serviceIdentityJournalHeader{
				ID: "tx-materialize-" + map[bool]string{false: "fs", true: "zfs"}[zfs], Service: "api", Root: oldRoot,
				TargetIdentity: identity,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = journal.Close() }()
			if err := journal.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseMaterializeIntent}); err != nil {
				t.Fatal(err)
			}
			migration := &serviceIdentityMigration{
				server: server, journal: journal, migrationID: journal.header.ID,
				target: &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd},
				req:    serviceIdentityMigrationRequest{Service: "api", RootPlan: &plan},
			}
			created, err := migration.materializeRoot(context.Background(), plan, os.Stdout)
			if err != nil {
				t.Fatalf("materializeRoot: %v", err)
			}
			if created != zfs {
				t.Fatalf("dataset created = %t, want %t", created, zfs)
			}
			for _, path := range serviceDirectoryPlan(newRoot) {
				if info, err := os.Stat(path); err != nil || !info.IsDir() {
					t.Fatalf("materialized directory %s: %v", path, err)
				}
			}
		})
	}
}

func TestServiceIdentityRecoveryMaterializationValidationBranches(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api")
	plan := &serviceRootMigrationPlan{ServiceName: "api", NewRoot: root}
	header := serviceIdentityJournalHeader{ID: "tx-proof", Service: "api", RootPlan: plan}
	stage := filepath.Join(filepath.Dir(root), ".yeet-service-root-"+header.ID)
	creation := serviceIdentityPhaseRecord{
		Phase: serviceIdentityPhaseMaterializeCreated, RootCreated: true, RootDev: 1, RootIno: 2, StagePath: stage,
	}
	publish := serviceIdentityPhaseRecord{
		Phase: serviceIdentityPhaseMaterializePublish, RootCreated: true, RootDev: 1, RootIno: 2,
		StagePath: stage, InventoryDigest: "digest", InventoryCount: 1,
	}
	materialized := serviceIdentityPhaseRecord{
		Phase: serviceIdentityPhaseMaterialize, RootCreated: true, RootDev: 1, RootIno: 2,
		InventoryDigest: "digest", InventoryCount: 1,
	}
	final := materialized
	final.Phase = serviceIdentityPhaseMaterializeFinal
	valid := serviceIdentityJournalContents{Header: header, Phases: []serviceIdentityPhaseRecord{
		{Phase: serviceIdentityPhaseMaterializeIntent}, creation, publish, materialized, final,
	}}
	if err := validateServiceIdentityRecoveryMaterialization(valid); err != nil {
		t.Fatalf("valid materialization: %v", err)
	}

	tests := []struct {
		name string
		err  error
	}{
		{"creation without intent", validateServiceIdentityRecoveryCreation(header, false, creation, true)},
		{"creation dataset mismatch", validateServiceIdentityRecoveryCreation(header, true, func() serviceIdentityPhaseRecord { p := creation; p.DatasetCreated = true; return p }(), true)},
		{"creation stage mismatch", validateServiceIdentityRecoveryCreation(header, true, func() serviceIdentityPhaseRecord { p := creation; p.StagePath = "/wrong"; return p }(), true)},
		{"publish without creation", validateServiceIdentityRecoveryPublish(header, creation, false, publish, true)},
		{"publish without inventory", validateServiceIdentityRecoveryPublish(header, creation, true, func() serviceIdentityPhaseRecord { p := publish; p.InventoryDigest = ""; return p }(), true)},
		{"missing materialization proof", validateServiceIdentityMaterializedRootProof(header, serviceIdentityPhaseRecord{}, true)},
		{"materialization plan mismatch", validateServiceIdentityMaterializedRootProof(header, func() serviceIdentityPhaseRecord { p := materialized; p.RootCreated = false; return p }(), true)},
		{"materialization dataset mismatch", validateServiceIdentityMaterializedRootProof(header, func() serviceIdentityPhaseRecord { p := materialized; p.DatasetGUID = "guid"; return p }(), true)},
		{"materialization missing publish", validateServiceIdentityMaterializedRootProof(header, materialized, false)},
		{"final without materialization", validateServiceIdentityRecoveryMaterializedRoot(header, materialized, false, false, final, true)},
		{"final identity mismatch", validateServiceIdentityFinalMaterialization(materialized, func() serviceIdentityPhaseRecord { p := final; p.RootIno++; return p }(), true)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestServiceIdentityGenerationAndRuntimeHelpers(t *testing.T) {
	unitPath := filepath.Join(t.TempDir(), "api.service")
	payloadPath := filepath.Join(filepath.Dir(unitPath), "env")
	if err := os.WriteFile(unitPath, []byte("[Service]\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payloadPath, []byte("A=B\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	unitProof, err := captureServiceIdentityPathProof(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := captureServiceIdentityPathProof(payloadPath)
	if err != nil {
		t.Fatal(err)
	}
	state := serviceIdentityStateFromProof(proof)
	paths := serviceIdentityGenerationIntentPaths([]string{unitPath, payloadPath}, unitPath)
	if len(paths) != 1 || paths[0] != payloadPath {
		t.Fatalf("intent paths = %v", paths)
	}
	if got, err := validateServiceIdentityGenerationIntentStates([]serviceIdentityPathState{state}); err != nil || len(got) != 1 || got[0] != payloadPath {
		t.Fatalf("validated intent = %v, %v", got, err)
	}
	if _, err := validateServiceIdentityGenerationIntentStates([]serviceIdentityPathState{{Path: "relative"}}); err == nil {
		t.Fatal("invalid generation intent was accepted")
	}
	migration := &serviceIdentityMigration{
		unitPath:          unitPath,
		previousUnitProof: unitProof,
		generationIntents: []serviceIdentityPathState{state},
		req: serviceIdentityMigrationRequest{
			GenerationPaths: []string{unitPath, payloadPath},
		},
	}
	if err := migration.validateServiceIdentityGenerationIntents(); err != nil {
		t.Fatalf("validate generation intents: %v", err)
	}
	if err := migration.captureStagedServiceIdentityGeneration(); err != nil {
		t.Fatalf("capture staged generation: %v", err)
	}
	migration.req.GenerationPaths = []string{unitPath, filepath.Join(filepath.Dir(unitPath), "other")}
	if err := migration.validateServiceIdentityGenerationIntents(); err == nil {
		t.Fatal("mismatched generation intent paths were accepted")
	}
	migration.req.GenerationPaths = []string{unitPath, payloadPath}
	if err := os.WriteFile(payloadPath, []byte("changed\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := migration.captureStagedServiceIdentityGeneration(); err == nil {
		t.Fatal("generation payload drift was accepted")
	}
	migration.generationIntents = nil
	if err := os.WriteFile(unitPath, []byte("[Service]\nUser=changed\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := migration.captureStagedServiceIdentityGeneration(); err == nil {
		t.Fatal("primary unit drift was accepted")
	}

	service := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd}
	oldActive, oldSystemctl := catchSystemdUnitActive, catchSystemctl
	active := map[string]bool{"api.service": true}
	catchSystemdUnitActive = func(unit string) bool { return active[unit] }
	catchSystemctl = func(args ...string) error {
		if len(args) != 2 {
			return errors.New("bad systemctl args")
		}
		active[args[1]] = false
		return nil
	}
	t.Cleanup(func() { catchSystemdUnitActive, catchSystemctl = oldActive, oldSystemctl })
	if running, err := serviceIdentityCombinedRunningAction(service, service)(context.Background(), "api"); err != nil || !running {
		t.Fatalf("combined running = %t, %v", running, err)
	}
	if err := serviceIdentityCombinedStopAction(service, service)(context.Background(), "api"); err != nil {
		t.Fatalf("combined stop: %v", err)
	}
	if got := uniqueServiceIdentityStopUnits([]*db.Service{service, service}, "api"); len(got) != 1 || got[0] != "api.service" {
		t.Fatalf("unique stop units = %v", got)
	}
	if _, err := serviceIdentityCombinedRunningAction(service)(func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }(), "api"); err == nil {
		t.Fatal("cancelled combined running action succeeded")
	}
	for _, serviceType := range []db.ServiceType{db.ServiceTypeVM, db.ServiceTypeDockerCompose, db.ServiceType("other")} {
		if err := serviceIdentityTypeError("api", serviceType); err == nil {
			t.Fatalf("serviceIdentityTypeError(%q) = nil", serviceType)
		}
	}
}

func TestServiceIdentitySmallAdapters(t *testing.T) {
	info := serviceIdentityCopyFileInfo{name: "state", stat: unix.Stat_t{Size: 42}}
	if info.Size() != 42 {
		t.Fatalf("copy file size = %d", info.Size())
	}
	tmp, err := os.CreateTemp(t.TempDir(), "identity-cleanup-")
	if err != nil {
		t.Fatal(err)
	}
	path := tmp.Name()
	wantErr := errors.New("write failed")
	if _, err := cleanupTemporaryServiceIdentityUnit(tmp, path, wantErr); !errors.Is(err, wantErr) {
		t.Fatalf("cleanup error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temporary unit remains: %v", err)
	}
}

func TestServiceIdentityZFSCreateAmbiguityBranches(t *testing.T) {
	migration := &serviceIdentityMigration{migrationID: "tx-zfs"}
	plan := serviceRootMigrationPlan{NewRootZFS: "tank/api"}
	createErr := errors.New("create interrupted")
	tests := []struct {
		name    string
		runner  zfsCommandRunner
		created bool
		wantErr bool
	}{
		{"missing", func(_ context.Context, args ...string) (string, string, error) {
			return "", "dataset does not exist", errors.New("missing")
		}, false, true},
		{"matching marker", func(_ context.Context, args ...string) (string, string, error) {
			if args[0] == "list" {
				return "tank/api\n", "", nil
			}
			return "tx-zfs\n", "", nil
		}, true, false},
		{"wrong marker", func(_ context.Context, args ...string) (string, string, error) {
			if args[0] == "list" {
				return "tank/api\n", "", nil
			}
			return "other\n", "", nil
		}, true, true},
		{"probe error", func(context.Context, ...string) (string, string, error) {
			return "", "permission denied", errors.New("denied")
		}, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			created, err := migration.resolveAmbiguousServiceIdentityZFSCreate(context.Background(), tt.runner, plan, createErr)
			if created != tt.created || (err != nil) != tt.wantErr {
				t.Fatalf("created/error = %t/%v, want %t/error=%t", created, err, tt.created, tt.wantErr)
			}
		})
	}
	if err := validateServiceIdentityZFSMountpoint(context.Background(), func(context.Context, ...string) (string, string, error) {
		return "/wrong\n", "", nil
	}, serviceRootMigrationPlan{NewRoot: "/right", NewRootZFS: "tank/api"}); err == nil {
		t.Fatal("mismatched ZFS mountpoint was accepted")
	}
}

func TestServiceIdentityMaterializesCopiedZFSTarget(t *testing.T) {
	server := newTestServer(t)
	parent := t.TempDir()
	oldRoot := filepath.Join(parent, "old")
	newRoot := filepath.Join(parent, "new")
	if err := ensureDirsForRoot(oldRoot, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceDataDirForRoot(oldRoot), "state"), []byte("state"), 0o640); err != nil {
		t.Fatal(err)
	}
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		switch {
		case args[0] == "list":
			return "", "", nil
		case args[0] == "create":
			return "", "", os.Mkdir(newRoot, 0o755)
		case args[0] == "get" && args[4] == "mountpoint":
			return newRoot + "\n", "", nil
		case args[0] == "get" && args[4] == "guid":
			return "guid-copy\n", "", nil
		default:
			return "", "", fmt.Errorf("unexpected zfs args: %v", args)
		}
	}
	plan := serviceRootMigrationPlan{
		ServiceName: "api", OldRoot: oldRoot, NewRoot: newRoot, NewRootZFS: "tank/api",
		Mode: serviceRootMigrationCopy, CreateNewRootZFS: true,
	}
	identity := currentTestServiceIdentity()
	journal, err := createServiceIdentityJournal(server.cfg.RootDir, serviceIdentityJournalHeader{
		ID: "tx-copy-zfs", Service: "api", Root: oldRoot, TargetIdentity: identity,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	if err := journal.AppendPhase(serviceIdentityPhaseRecord{Phase: serviceIdentityPhaseMaterializeIntent}); err != nil {
		t.Fatal(err)
	}
	prepared := false
	migration := &serviceIdentityMigration{
		server: server, journal: journal, migrationID: journal.header.ID,
		target: &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd},
		req:    serviceIdentityMigrationRequest{Service: "api", RootPlan: &plan},
		prepareGeneration: func(root string) error {
			prepared = root == newRoot
			return nil
		},
	}
	created, err := migration.materializeRoot(context.Background(), plan, os.Stdout)
	if err != nil || !created || !prepared {
		t.Fatalf("copy materialization = created:%t prepared:%t err:%v", created, prepared, err)
	}
	if raw, err := os.ReadFile(filepath.Join(serviceDataDirForRoot(newRoot), "state")); err != nil || string(raw) != "state" {
		t.Fatalf("copied state = %q, %v", raw, err)
	}
	proof, err := migration.captureMaterialization(context.Background(), true)
	if err != nil || proof.DatasetGUID != "guid-copy" || proof.InventoryDigest == "" {
		t.Fatalf("materialization proof = %#v, %v", proof, err)
	}
}

func TestServiceIdentityGenerationPlanValidation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api")
	unitPath := filepath.Join(t.TempDir(), "api.service")
	target := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, Generation: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(1): filepath.Join(root, "bin", "api.service")}},
			db.ArtifactEnvFile:     {Refs: map[db.ArtifactRef]string{db.Gen(1): filepath.Join(root, "env", "env")}},
		},
	}
	paths, units, err := serviceIdentityExpectedGenerationTargets(target, root, unitPath)
	if err != nil {
		t.Fatal(err)
	}
	migration := &serviceIdentityMigration{
		target: target, result: serviceIdentityMigrationResult{Root: root}, unitPath: unitPath,
		req: serviceIdentityMigrationRequest{
			StageGeneration: func(context.Context) error { return nil }, GenerationPaths: paths, GenerationUnits: units,
		},
	}
	if err := migration.validateGenerationTargets(); err != nil {
		t.Fatalf("valid generation plan: %v", err)
	}
	migration.req.GenerationPaths = []string{"/wrong"}
	if err := migration.validateGenerationTargets(); err == nil {
		t.Fatal("mismatched generation paths were accepted")
	}
	migration.req.GenerationPaths = paths
	migration.req.GenerationUnits = []string{"wrong.service"}
	if err := migration.validateGenerationTargets(); err == nil {
		t.Fatal("mismatched generation units were accepted")
	}
	migration.req.StageGeneration = nil
	if err := migration.validateGenerationTargets(); err == nil {
		t.Fatal("generation paths without staging were accepted")
	}
	migration.req.GenerationPaths = nil
	migration.req.GenerationUnits = nil
	if err := migration.validateGenerationTargets(); err != nil {
		t.Fatalf("empty generation plan: %v", err)
	}
	if got := cleanServiceIdentityPaths([]string{" /tmp/a/../b "}); len(got) != 1 || got[0] != "/tmp/b" {
		t.Fatalf("clean paths = %v", got)
	}
}

func TestServiceIdentityRollbackStopObservationBranches(t *testing.T) {
	t.Run("retry succeeds", func(t *testing.T) {
		stops := 0
		observations := 0
		err := stopServiceIdentityObserved(context.Background(), func(context.Context, string) error {
			stops++
			return nil
		}, func(context.Context, string) (bool, error) {
			observations++
			return observations == 1, nil
		}, "api")
		if err != nil || stops != 2 {
			t.Fatalf("retry stop = calls:%d err:%v", stops, err)
		}
	})
	t.Run("observation fails", func(t *testing.T) {
		err := stopServiceIdentityObserved(context.Background(), func(context.Context, string) error {
			return errors.New("stop failed")
		}, func(context.Context, string) (bool, error) {
			return false, errors.New("observe failed")
		}, "api")
		if err == nil || !strings.Contains(err.Error(), "observe service") {
			t.Fatalf("observation error = %v", err)
		}
	})
	t.Run("still running", func(t *testing.T) {
		err := stopServiceIdentityObserved(context.Background(), func(context.Context, string) error { return nil }, func(context.Context, string) (bool, error) {
			return true, nil
		}, "api")
		if err == nil || !strings.Contains(err.Error(), "still running") {
			t.Fatalf("running error = %v", err)
		}
	})
}

func TestPersistedNativeServiceIdentityEligibility(t *testing.T) {
	for _, service := range []*db.Service{
		nil,
		{Name: "api", ServiceType: db.ServiceTypeDockerCompose},
		{Name: CatchService, ServiceType: db.ServiceTypeSystemd, Identity: &db.ServiceIdentity{UID: 1, GID: 1}},
		{Name: "api", ServiceType: db.ServiceTypeSystemd},
	} {
		if _, ok, err := persistedNativeServiceIdentity(service); err != nil || ok {
			t.Fatalf("ineligible identity = ok:%t err:%v service:%#v", ok, err, service)
		}
	}
	invalid := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd,
		Identity: &db.ServiceIdentity{RequestedUser: "1000", RequestedGroup: "1000", UID: 1001, GID: 1000},
	}
	if _, _, err := persistedNativeServiceIdentity(invalid); err == nil {
		t.Fatal("drifted persisted identity was accepted")
	}
	server := newTestServer(t)
	server.setServiceIdentityGlobalMutationBlock(errors.New("blocked"))
	if err := server.checkServiceIdentityMutationAllowed("api"); err == nil {
		t.Fatal("global mutation block was not enforced")
	}
}

func TestRemoveProvisionalNativeServiceUsesExactCAS(t *testing.T) {
	server := newTestServer(t)
	expected := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, IdentityInstallPending: true}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": expected.Clone()}}); err != nil {
		t.Fatal(err)
	}
	installer := &FileInstaller{s: server, cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "api"}}}
	if err := installer.removeProvisionalNativeService(expected); err != nil {
		t.Fatalf("remove provisional: %v", err)
	}
	if _, err := server.serviceView("api"); !errors.Is(err, errServiceNotFound) {
		t.Fatalf("provisional service remains: %v", err)
	}
	if err := installer.removeProvisionalNativeService(expected); err != nil {
		t.Fatalf("remove missing provisional: %v", err)
	}
	changed := expected.Clone()
	changed.Generation = 2
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": changed}}); err != nil {
		t.Fatal(err)
	}
	if err := installer.removeProvisionalNativeService(expected); err == nil {
		t.Fatal("changed provisional service was removed")
	}
	if got, err := server.serviceView("api"); err != nil || got.Generation() != 2 {
		t.Fatalf("changed service = %#v, %v", got, err)
	}
}

func TestServiceIdentityDefaultOpsAdapters(t *testing.T) {
	server := newTestServer(t)
	previous := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd}
	target := previous.Clone()
	migration := &serviceIdentityMigration{server: server, previous: previous, target: target}
	oldActive, oldSystemctl := catchSystemdUnitActive, catchSystemctl
	active := map[string]bool{"api.service": true}
	var systemctlCalls []string
	catchSystemdUnitActive = func(unit string) bool { return active[unit] }
	catchSystemctl = func(args ...string) error {
		systemctlCalls = append(systemctlCalls, strings.Join(args, " "))
		if len(args) == 2 && args[0] == "stop" {
			active[args[1]] = false
		}
		if len(args) == 2 && args[0] == "start" {
			active[args[1]] = true
		}
		return nil
	}
	t.Cleanup(func() { catchSystemdUnitActive, catchSystemctl = oldActive, oldSystemctl })
	ops := migration.defaultOps()
	if err := ops.phase("test"); err != nil || filepath.Base(ops.unitPath("api")) != "api.service" {
		t.Fatalf("default phase/unit = %v %q", err, ops.unitPath("api"))
	}
	state, err := ops.captureRuntime(context.Background(), "api")
	if err != nil || len(state) != 1 || !state[0].Active {
		t.Fatalf("captured runtime = %#v, %v", state, err)
	}
	if err := ops.stop(context.Background(), "api"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := ops.start(context.Background(), "api"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := ops.restoreRuntime(context.Background(), "api", []serviceIdentityRuntimeUnitState{{Unit: "api.service", Active: false}}); err != nil {
		t.Fatalf("restore runtime: %v", err)
	}
	if snapshot, err := ops.snapshot(context.Background(), previous); err != nil || snapshot != "" {
		t.Fatalf("snapshot = %q, %v", snapshot, err)
	}
	if err := ops.reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if err := ops.enable(context.Background(), "api.service"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := ops.disable(context.Background(), "api.service"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "journal")
	if err := os.WriteFile(path, []byte("journal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ops.remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := ops.newGenerationStager(target, filepath.Join(t.TempDir(), "api")); err != nil {
		t.Fatalf("generation stager: %v", err)
	}
	if len(systemctlCalls) < 5 {
		t.Fatalf("systemctl calls = %v", systemctlCalls)
	}
}
