// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/registry"
)

const vmRuntimeIntegrationGate = "YEET_FIRECRACKER_RUNTIME_INTEGRATION"

var vmRuntimeIntegrationAssertions = []string{
	"api-ready",
	"boot",
	"natural-reboot",
	"network-ready",
	"disk-snapshot-restore",
	"cleanup",
	"jailer-uid-gid-drop",
	"no-memory-snapshot",
}

type vmRuntimeIntegrationConfig struct {
	Scenario              string
	Service               string
	RuntimeID             string
	RuntimeManifestSHA256 string
	Firecracker           string
	Jailer                string
	GuestDir              string
	Kernel                string
	Storage               string
	DataRoot              string
	ServiceRoot           string
	TestUser              string
	TestUID               uint32
	TestGID               uint32
	SSHPrivateKey         string
	SSHPublicKey          string
	Assertions            []string
}

type vmRuntimeIntegrationTarEntry struct {
	Name string
	Path string
	Data []byte
	Mode int64
}

// TestFirecrackerRuntimeIntegration is intentionally inert in the ordinary
// test suite. The repository integration driver supplies exact, already
// downloaded artifacts and opts in on a disposable Linux KVM host.
func TestFirecrackerRuntimeIntegration(t *testing.T) {
	if os.Getenv(vmRuntimeIntegrationGate) != "1" {
		t.Skip("Firecracker runtime integration gate is not enabled")
	}

	cfg := readVMRuntimeIntegrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 42*time.Minute)
	defer cancel()

	if _, _, err := cli.ParseSnapshotsCreate([]string{"--full", cfg.Service}); err == nil {
		t.Fatal("retired full-memory snapshot flag was accepted")
	}

	candidateManifestRaw, err := os.ReadFile(filepath.Join(filepath.Dir(cfg.Firecracker), vmRuntimeManifestFilename))
	if err != nil {
		t.Fatalf("read candidate runtime manifest: %v", err)
	}
	if got := vmRuntimeSHA256Bytes(candidateManifestRaw); got != cfg.RuntimeManifestSHA256 {
		t.Fatalf("candidate runtime manifest digest = %s, want %s", got, cfg.RuntimeManifestSHA256)
	}
	candidateManifest, err := decodeVMRuntimeManifest(candidateManifestRaw)
	if err != nil {
		t.Fatalf("decode candidate runtime manifest: %v", err)
	}
	if candidateManifest.RuntimeID != cfg.RuntimeID {
		t.Fatalf("candidate runtime ID = %q, want %q", candidateManifest.RuntimeID, cfg.RuntimeID)
	}

	guestManifestRaw, guestAsset := readVMRuntimeIntegrationGuest(t, cfg)
	server := newVMRuntimeIntegrationServer(t, cfg)
	serviceRootFlag, dataset := prepareVMRuntimeIntegrationStorage(t, ctx, cfg)
	datasetRemoved := false
	defer func() {
		if dataset != "" && !datasetRemoved {
			if cleanupErr := destroyVMRuntimeIntegrationDataset(context.Background(), dataset, cfg.Service); cleanupErr != nil {
				t.Errorf("clean up integration ZFS dataset: %v", cleanupErr)
			}
		}
	}()

	cleaned := false
	defer func() {
		if cleaned {
			return
		}
		if cleanupErr := removeVMRuntimeIntegrationService(server, cfg.Service); cleanupErr != nil && !strings.Contains(cleanupErr.Error(), "not found") {
			t.Errorf("clean up integration VM: %v", cleanupErr)
		}
	}()

	imageRef := importVMRuntimeIntegrationGuest(t, ctx, cfg, guestManifestRaw, guestAsset)
	provisionVMRuntimeIntegrationGuest(t, ctx, server, cfg, imageRef, serviceRootFlag)

	initialUnit, err := readVMRuntimeUnitState(ctx, vmSystemdUnitName(cfg.Service))
	if err != nil || initialUnit.ActiveState != "active" || initialUnit.MainPID <= 0 {
		t.Fatalf("initial VM unit state = %#v, err = %v", initialUnit, err)
	}
	initialService := vmRuntimeIntegrationService(t, server, cfg.Service)
	if initialService.VM.Components == nil {
		t.Fatal("new component VM has no component state before legacy-adoption simulation")
	}
	initialArtifact := initialService.VM.Components.Runtime.Configured
	initialVersion, err := probeMatchingVMRuntimePair(ctx, guestAsset.Paths.FirecrackerPath, guestAsset.Paths.JailerPath)
	if err != nil {
		t.Fatalf("probe initial runtime pair: %v", err)
	}
	if err := probeFirecrackerInstance(ctx, initialService.VM.Sockets.APISocketPath, vmJailerID(cfg.Service), initialVersion); err != nil {
		t.Fatalf("probe initial Firecracker API: %v", err)
	}
	initialIP := waitVMRuntimeIntegrationGuest(t, ctx, initialService.VM.Sockets.VsockSocketPath)
	if _, err := runVMRuntimeIntegrationSSH(ctx, cfg, initialService.VM.SSH.User, initialIP, "true"); err != nil {
		t.Fatalf("verify initial guest network and SSH readiness: %v", err)
	}

	adoption, err := PrepareVMRuntimeAdoption(ctx, &server.cfg)
	if err != nil {
		t.Fatalf("prepare VM runtime adoption: %v", err)
	}
	summary := adoption.Summary()
	if len(summary.AlreadyAdopted) != 1 || summary.AlreadyAdopted[0] != cfg.Service || len(summary.Adopting) != 0 || len(summary.Blocked) != 0 || summary.HasChanges {
		_ = adoption.Close()
		t.Fatalf("VM runtime adoption summary = %#v", summary)
	}
	if err := adoption.Commit(); err != nil {
		_ = adoption.Close()
		t.Fatalf("commit VM runtime adoption: %v", err)
	}
	if err := adoption.Close(); err != nil {
		t.Fatalf("close VM runtime adoption: %v", err)
	}
	adoptedUnit, err := readVMRuntimeUnitState(ctx, vmSystemdUnitName(cfg.Service))
	if err != nil {
		t.Fatalf("read adopted VM unit: %v", err)
	}
	if adoptedUnit.MainPID != initialUnit.MainPID {
		t.Fatalf("runtime adoption restarted VM: PID changed from %d to %d", initialUnit.MainPID, adoptedUnit.MainPID)
	}
	adoptedService := vmRuntimeIntegrationService(t, server, cfg.Service)
	if adoptedService.VM.Components == nil || adoptedService.VM.Components.Runtime.Configured != initialArtifact {
		t.Fatalf("runtime adoption did not restore the exact configured runtime: %#v", adoptedService.VM.Components)
	}

	candidate := importVMRuntimeIntegrationCandidate(t, ctx, server, cfg, "candidate-"+cfg.Scenario, candidateManifestRaw, cfg.Firecracker, cfg.Jailer)
	if candidate.ID != cfg.RuntimeID || candidate.ManifestSHA256 != cfg.RuntimeManifestSHA256 {
		t.Fatalf("imported candidate identity = %#v", candidate)
	}
	var stageOut bytes.Buffer
	if err := server.upgradeVMRuntime(ctx, &stageOut, cfg.Service, "local:candidate-"+cfg.Scenario, ""); err != nil {
		t.Fatalf("stage candidate runtime: %v", err)
	}
	stagedUnit, err := readVMRuntimeUnitState(ctx, vmSystemdUnitName(cfg.Service))
	if err != nil || stagedUnit.MainPID != initialUnit.MainPID {
		t.Fatalf("staging changed the running VM: state=%#v err=%v", stagedUnit, err)
	}

	if _, err := runVMRuntimeIntegrationSSH(ctx, cfg, initialService.VM.SSH.User, initialIP, "sudo systemctl reboot"); err != nil {
		// SSH normally reports the connection closing while the reboot is in
		// progress. The new unit PID and candidate marker below are the proof.
		t.Logf("natural reboot command closed the SSH session: %v", err)
	}
	candidateVersion, err := probeMatchingVMRuntimePair(ctx, candidate.Firecracker, candidate.Jailer)
	if err != nil {
		t.Fatalf("probe candidate runtime pair: %v", err)
	}
	marker, _ := waitVMRuntimeIntegrationRunning(t, ctx, server, cfg, candidate, candidateVersion, initialUnit.MainPID)

	// Recreate the control-plane object before consuming the result. This is
	// the same durable path Catch uses after its own process restarts.
	server = newVMRuntimeIntegrationServer(t, cfg)
	if err := RecoverVMRuntimeAdoptions(ctx, &server.cfg); err != nil {
		t.Fatalf("recover runtime transactions after Catch restart: %v", err)
	}
	waitVMRuntimeIntegrationTrial(t, ctx, server, cfg.Service, candidate, initialArtifact, vmRuntimeTrialHealthy)

	var rollbackOut bytes.Buffer
	if err := server.rollbackVMRuntime(ctx, &rollbackOut, cfg.Service, true); err != nil {
		t.Fatalf("restart into previous runtime: %v", err)
	}
	if cfg.Storage == "raw" && !strings.Contains(rollbackOut.String(), "only launcher rollback is available") {
		t.Fatalf("raw runtime restart omitted recovery limitation: %q", rollbackOut.String())
	}
	marker, _ = waitVMRuntimeIntegrationRunning(t, ctx, server, cfg, initialArtifact, initialVersion, marker.RunnerPID)

	if err := server.upgradeVMRuntime(ctx, io.Discard, cfg.Service, "local:candidate-"+cfg.Scenario, ""); err != nil {
		t.Fatalf("stage candidate for explicit restart: %v", err)
	}
	var restartOut bytes.Buffer
	if err := server.restartStagedVMRuntime(ctx, &restartOut, cfg.Service); err != nil {
		t.Fatalf("restart into candidate runtime: %v", err)
	}
	marker, candidateIP := waitVMRuntimeIntegrationRunning(t, ctx, server, cfg, candidate, candidateVersion, marker.RunnerPID)

	if cfg.Storage == "zfs" {
		verifyVMRuntimeIntegrationDiskRestore(t, ctx, server, cfg, candidateIP, marker.RunnerPID)
		marker, _ = waitVMRuntimeIntegrationRunning(t, ctx, server, cfg, candidate, candidateVersion, marker.RunnerPID)
	} else {
		err := server.createVMSnapshot(ctx, cfg.Service, cli.SnapshotsCreateFlags{Comment: "integration raw limitation"}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "requires a ZFS zvol-backed VM") {
			t.Fatalf("raw disk snapshot result = %v", err)
		}
	}

	brokenManifestRaw, brokenFirecracker := buildVMRuntimeIntegrationBrokenCandidate(t, cfg, candidateManifest)
	broken := importVMRuntimeIntegrationCandidate(t, ctx, server, cfg, "fallback-"+cfg.Scenario, brokenManifestRaw, brokenFirecracker, cfg.Jailer)
	if err := server.upgradeVMRuntime(ctx, io.Discard, cfg.Service, "local:fallback-"+cfg.Scenario, ""); err != nil {
		t.Fatalf("stage fallback exercise runtime: %v", err)
	}
	var fallbackOut bytes.Buffer
	err = server.restartStagedVMRuntime(ctx, &fallbackOut, cfg.Service)
	if err == nil || !strings.Contains(err.Error(), "failed host readiness") {
		t.Fatalf("candidate fallback result = %v, output = %q", err, fallbackOut.String())
	}
	if !strings.Contains(fallbackOut.String(), broken.ID) {
		t.Fatalf("candidate fallback output omitted candidate %s: %q", broken.ID, fallbackOut.String())
	}
	marker, candidateIP = waitVMRuntimeIntegrationRunning(t, ctx, server, cfg, candidate, candidateVersion, marker.RunnerPID)
	finalService := vmRuntimeIntegrationService(t, server, cfg.Service)
	finalRuntime := finalService.VM.Components.Runtime
	if finalRuntime.Configured != candidate || finalRuntime.Staged != nil || finalRuntime.Trial == nil || finalRuntime.Trial.State != string(vmRuntimeTrialFailedRolledBack) {
		t.Fatalf("final runtime lifecycle = %#v, want candidate configured after fallback", finalRuntime)
	}
	if _, err := runVMRuntimeIntegrationSSH(ctx, cfg, finalService.VM.SSH.User, candidateIP, "true"); err != nil {
		t.Fatalf("final guest readiness: %v", err)
	}

	serviceRoot := serviceRootFromConfig(server.cfg, *finalService)
	jailRoot := filepath.Join(vmJailerBaseForDataRoot(cfg.DataRoot), filepath.Base(candidate.Firecracker), vmJailerID(cfg.Service))
	if err := removeVMRuntimeIntegrationService(server, cfg.Service); err != nil {
		t.Fatalf("remove integration VM: %v", err)
	}
	cleaned = true
	if view, err := server.cfg.DB.Get(); err != nil {
		t.Fatalf("read database after cleanup: %v", err)
	} else if _, ok := view.Services().GetOk(cfg.Service); ok {
		t.Fatalf("service %q remains in database after cleanup", cfg.Service)
	}
	if state, err := readVMRuntimeUnitState(ctx, vmSystemdUnitName(cfg.Service)); err == nil && state.ActiveState == "active" {
		t.Fatalf("VM unit remains active after cleanup: %#v", state)
	}
	if _, err := os.Lstat(jailRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("jailer instance root remains after cleanup: %s (%v)", jailRoot, err)
	}
	if cfg.Storage == "raw" {
		if _, err := os.Lstat(serviceRoot); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("raw service root remains after clean-data removal: %s (%v)", serviceRoot, err)
		}
	} else if integrationZFSDatasetExists(context.Background(), dataset) {
		t.Fatalf("ZFS integration dataset remains after clean-data removal: %s", dataset)
	} else {
		datasetRemoved = true
	}
}

func readVMRuntimeIntegrationConfig(t *testing.T) vmRuntimeIntegrationConfig {
	t.Helper()
	required := func(name string) string {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			t.Fatalf("missing integration environment variable %s", name)
		}
		return value
	}
	parseID := func(name string) uint32 {
		value := required(name)
		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil || parsed == 0 {
			t.Fatalf("invalid integration identity %s=%q", name, value)
		}
		return uint32(parsed)
	}
	cfg := vmRuntimeIntegrationConfig{
		Scenario:              required("YEET_RUNTIME_INTEGRATION_SCENARIO"),
		Service:               required("YEET_RUNTIME_INTEGRATION_SERVICE"),
		RuntimeID:             required("YEET_RUNTIME_INTEGRATION_RUNTIME_ID"),
		RuntimeManifestSHA256: required("YEET_RUNTIME_INTEGRATION_RUNTIME_MANIFEST_SHA256"),
		Firecracker:           required("YEET_RUNTIME_INTEGRATION_FIRECRACKER"),
		Jailer:                required("YEET_RUNTIME_INTEGRATION_JAILER"),
		GuestDir:              required("YEET_RUNTIME_INTEGRATION_GUEST_DIR"),
		Kernel:                required("YEET_RUNTIME_INTEGRATION_KERNEL"),
		Storage:               required("YEET_RUNTIME_INTEGRATION_STORAGE"),
		DataRoot:              required("YEET_RUNTIME_INTEGRATION_DATA_ROOT"),
		ServiceRoot:           required("YEET_RUNTIME_INTEGRATION_SERVICE_ROOT"),
		TestUser:              required("YEET_RUNTIME_INTEGRATION_TEST_USER"),
		TestUID:               parseID("YEET_RUNTIME_INTEGRATION_TEST_UID"),
		TestGID:               parseID("YEET_RUNTIME_INTEGRATION_TEST_GID"),
		SSHPrivateKey:         required("YEET_RUNTIME_INTEGRATION_SSH_PRIVATE_KEY"),
		SSHPublicKey:          required("YEET_RUNTIME_INTEGRATION_SSH_PUBLIC_KEY"),
		Assertions:            strings.Split(required("YEET_RUNTIME_INTEGRATION_ASSERTIONS"), ","),
	}
	if cfg.TestUser != vmRuntimeUser {
		t.Fatalf("integration runtime user = %q, want %q", cfg.TestUser, vmRuntimeUser)
	}
	if cfg.Storage != "raw" && cfg.Storage != "zfs" {
		t.Fatalf("integration storage = %q", cfg.Storage)
	}
	if len(cfg.Assertions) != len(vmRuntimeIntegrationAssertions) {
		t.Fatalf("integration assertions = %v", cfg.Assertions)
	}
	want := make(map[string]struct{}, len(vmRuntimeIntegrationAssertions))
	for _, assertion := range vmRuntimeIntegrationAssertions {
		want[assertion] = struct{}{}
	}
	for _, assertion := range cfg.Assertions {
		if _, ok := want[assertion]; !ok {
			t.Fatalf("unexpected or duplicate integration assertion %q", assertion)
		}
		delete(want, assertion)
	}
	if len(want) != 0 {
		t.Fatalf("missing integration assertions: %v", want)
	}
	return cfg
}

func newVMRuntimeIntegrationServer(t *testing.T, input vmRuntimeIntegrationConfig) *Server {
	t.Helper()
	servicesRoot := filepath.Join(input.DataRoot, "services")
	registryRoot := filepath.Join(input.DataRoot, "registry")
	storage, err := registry.NewFilesystemStorage(registryRoot)
	if err != nil {
		t.Fatalf("prepare integration registry: %v", err)
	}
	cfg := &Config{
		DB:                   db.NewStore(filepath.Join(input.DataRoot, "db.json"), servicesRoot),
		DefaultUser:          "root",
		InstallUser:          "root",
		RootDir:              input.DataRoot,
		ServicesRoot:         servicesRoot,
		MountsRoot:           filepath.Join(input.DataRoot, "mounts"),
		RegistryRoot:         registryRoot,
		RegistryStorage:      storage,
		InternalRegistryAddr: "127.0.0.1:0",
		AuthorizeFunc:        func(context.Context, string) error { return nil },
	}
	return NewUnstartedServer(cfg)
}

func readVMRuntimeIntegrationGuest(t *testing.T, cfg vmRuntimeIntegrationConfig) ([]byte, vmImageAsset) {
	t.Helper()
	manifestPath := filepath.Join(cfg.GuestDir, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read guest manifest: %v", err)
	}
	var manifest vmImageManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode guest manifest: %v", err)
	}
	if err := manifest.validate(); err != nil {
		t.Fatalf("validate guest manifest: %v", err)
	}
	if err := localVMImageVerifyManifestArtifacts(cfg.GuestDir, manifest); err != nil {
		t.Fatalf("verify guest artifacts: %v", err)
	}
	paths := cachedVMImagePaths(cfg.GuestDir, manifest)
	asset := vmImageAsset{Paths: paths, Manifest: manifest}
	if _, err := asset.RequireJailer(); err != nil {
		t.Fatalf("verify guest jailer: %v", err)
	}
	return raw, asset
}

func importVMRuntimeIntegrationGuest(t *testing.T, ctx context.Context, cfg vmRuntimeIntegrationConfig, manifestRaw []byte, managed vmImageAsset) localVMImageRef {
	t.Helper()
	name := "runtime-integration/" + cfg.Scenario
	entries := []vmRuntimeIntegrationTarEntry{
		{Name: "manifest.json", Data: manifestRaw, Mode: 0o644},
		{Name: filepath.Base(managed.Paths.RootFSPath), Path: managed.Paths.RootFSPath, Mode: 0o644},
		{Name: "vmlinux", Path: cfg.Kernel, Mode: 0o644},
	}
	importer := localVMImageImporter{
		CacheRoot: filepath.Join(cfg.DataRoot, "vm-images"),
		EnsureManagedAsset: func(context.Context) (vmImageAsset, error) {
			return managed, nil
		},
	}
	var ref localVMImageRef
	err := withVMRuntimeIntegrationTar(ctx, entries, func(reader io.Reader) error {
		var importErr error
		ref, importErr = importer.Import(ctx, localVMImageImportRequest{Name: name, Reader: reader, AllowLocalKernel: true})
		return importErr
	})
	if err != nil {
		t.Fatalf("import integration guest: %v", err)
	}
	asset, err := resolveLocalVMImageAssetForPayload(ctx, filepath.Join(cfg.DataRoot, "vm-images"), name)
	if err != nil {
		t.Fatalf("resolve imported integration guest: %v", err)
	}
	oldInspect, oldEnsure := vmImageInspectFunc, vmImageEnsureFunc
	vmImageInspectFunc = func(context.Context, vmImageCache, string) (vmImageCacheState, vmImageManifest, error) {
		return vmImageCacheState{Payload: ref.Payload, CachedVersion: ref.Version, LatestVersion: ref.Version, State: vmImageCacheMissing}, asset.Manifest, nil
	}
	vmImageEnsureFunc = func(context.Context, vmImageCache, string, ProgressUI) (vmImageAsset, error) {
		return asset, nil
	}
	t.Cleanup(func() {
		vmImageInspectFunc = oldInspect
		vmImageEnsureFunc = oldEnsure
	})
	return ref
}

func provisionVMRuntimeIntegrationGuest(t *testing.T, ctx context.Context, server *Server, cfg vmRuntimeIntegrationConfig, image localVMImageRef, serviceRoot string) {
	t.Helper()
	publicKey, err := os.ReadFile(cfg.SSHPublicKey)
	if err != nil {
		t.Fatalf("read integration SSH public key: %v", err)
	}
	var output bytes.Buffer
	execer := &ttyExecer{
		ctx: ctx, s: server, sn: cfg.Service, user: "root",
		vmSSHAuthorizedKey: strings.TrimSpace(string(publicKey)),
		progress:           catchrpc.ProgressQuiet, rawRW: &output, rw: &output,
	}
	flags := cli.RunFlags{
		CPUs: 1, Memory: "512m", Balloon: "off", Disk: "8g", Net: "svc",
		Restart: true, ImagePolicy: "cached", ServiceRoot: serviceRoot, ZFS: cfg.Storage == "zfs",
		Snapshots: "on", SnapshotChange: true,
	}
	if err := execer.provisionVM(flags, image.Payload); err != nil {
		t.Fatalf("provision integration VM: %v\n%s", err, output.String())
	}
}

func importVMRuntimeIntegrationCandidate(t *testing.T, ctx context.Context, server *Server, cfg vmRuntimeIntegrationConfig, alias string, manifest []byte, firecracker, jailer string) db.VMRuntimeArtifactConfig {
	t.Helper()
	entries := []vmRuntimeIntegrationTarEntry{
		{Name: vmRuntimeManifestFilename, Data: manifest, Mode: 0o644},
		{Name: "firecracker", Path: firecracker, Mode: 0o755},
		{Name: "jailer", Path: jailer, Mode: 0o755},
	}
	var output bytes.Buffer
	err := withVMRuntimeIntegrationTar(ctx, entries, func(reader io.Reader) error {
		return server.importVMRuntime(ctx, &output, alias, reader)
	})
	if err != nil {
		t.Fatalf("import runtime %q: %v\n%s", alias, err, output.String())
	}
	artifact, err := resolveLocalVMRuntime(ctx, filepath.Join(cfg.DataRoot, "vm-runtimes"), alias)
	if err != nil {
		t.Fatalf("resolve imported runtime %q: %v", alias, err)
	}
	return artifact
}

func withVMRuntimeIntegrationTar(ctx context.Context, entries []vmRuntimeIntegrationTarEntry, consume func(io.Reader) error) error {
	reader, writer := io.Pipe()
	written := make(chan error, 1)
	go func() {
		err := writeVMRuntimeIntegrationTar(ctx, writer, entries)
		_ = writer.CloseWithError(err)
		written <- err
	}()
	consumeErr := consume(reader)
	_ = reader.CloseWithError(consumeErr)
	return errors.Join(consumeErr, <-written)
}

func writeVMRuntimeIntegrationTar(ctx context.Context, writer io.Writer, entries []vmRuntimeIntegrationTarEntry) (retErr error) {
	archive := tar.NewWriter(writer)
	defer func() { retErr = errors.Join(retErr, archive.Close()) }()
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		size := int64(len(entry.Data))
		if entry.Path != "" {
			info, err := os.Stat(entry.Path)
			if err != nil {
				return err
			}
			size = info.Size()
		}
		header := &tar.Header{
			Name: entry.Name, Typeflag: tar.TypeReg, Mode: entry.Mode, Size: size,
			Uid: 0, Gid: 0, ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatUSTAR,
		}
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if entry.Path == "" {
			if _, err := archive.Write(entry.Data); err != nil {
				return err
			}
			continue
		}
		file, err := os.Open(entry.Path)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(archive, file, size)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			return errors.Join(copyErr, closeErr)
		}
	}
	return nil
}

func prepareVMRuntimeIntegrationStorage(t *testing.T, ctx context.Context, cfg vmRuntimeIntegrationConfig) (serviceRootFlag, dataset string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(cfg.ServiceRoot), 0o755); err != nil {
		t.Fatalf("prepare service-root parent: %v", err)
	}
	if cfg.Storage == "raw" {
		return cfg.ServiceRoot, ""
	}
	output, err := exec.CommandContext(ctx, "zpool", "list", "-H", "-o", "name").Output()
	if err != nil {
		t.Fatalf("list integration ZFS pools: %v", err)
	}
	pool := strings.TrimSpace(strings.SplitN(string(output), "\n", 2)[0])
	if pool == "" || strings.Contains(pool, "/") {
		t.Fatalf("invalid integration ZFS pool %q", pool)
	}
	dataset = pool + "/yeet-runtime-" + cfg.Service
	if integrationZFSDatasetExists(ctx, dataset) {
		t.Fatalf("integration ZFS dataset already exists: %s", dataset)
	}
	command := exec.CommandContext(ctx, "zfs", "create", "-o", "mountpoint="+cfg.ServiceRoot, dataset)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create integration ZFS dataset: %v: %s", err, strings.TrimSpace(string(output)))
	}
	return dataset, dataset
}

func integrationZFSDatasetExists(ctx context.Context, dataset string) bool {
	if strings.TrimSpace(dataset) == "" {
		return false
	}
	return exec.CommandContext(ctx, "zfs", "list", "-H", "-o", "name", dataset).Run() == nil
}

func destroyVMRuntimeIntegrationDataset(ctx context.Context, dataset, service string) error {
	wantSuffix := "/yeet-runtime-" + service
	if !strings.HasSuffix(dataset, wantSuffix) || strings.TrimSuffix(dataset, wantSuffix) == "" {
		return fmt.Errorf("refuse unexpected integration dataset %q", dataset)
	}
	if !integrationZFSDatasetExists(ctx, dataset) {
		return nil
	}
	output, err := exec.CommandContext(ctx, "zfs", "destroy", "-r", dataset).CombinedOutput()
	if err != nil {
		return fmt.Errorf("zfs destroy %s: %w: %s", dataset, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func waitVMRuntimeIntegrationGuest(t *testing.T, ctx context.Context, socket string) netip.Addr {
	t.Helper()
	var lastErr error
	for {
		state, err := queryVMGuestReady(ctx, socket)
		if err == nil && state.SSHReady {
			for _, iface := range state.Network.Interfaces {
				for _, raw := range iface.IPs {
					ip, parseErr := netip.ParseAddr(strings.SplitN(raw, "/", 2)[0])
					if parseErr == nil && ip.IsValid() && !ip.IsLoopback() {
						return ip
					}
				}
			}
		}
		lastErr = err
		select {
		case <-ctx.Done():
			t.Fatalf("wait for guest readiness: %v (last error: %v)", ctx.Err(), lastErr)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func waitVMRuntimeIntegrationRunning(t *testing.T, ctx context.Context, server *Server, cfg vmRuntimeIntegrationConfig, artifact db.VMRuntimeArtifactConfig, version string, priorPID int) (vmRuntimeRunningMarker, netip.Addr) {
	t.Helper()
	var lastErr error
	for {
		service := vmRuntimeIntegrationService(t, server, cfg.Service)
		root := serviceRootFromConfig(server.cfg, *service)
		unit, unitErr := readVMRuntimeUnitState(ctx, vmSystemdUnitName(cfg.Service))
		marker, markerErr := readTrustedVMRuntimeRunningMarker(filepath.Join(serviceRunDirForRoot(root), vmRuntimeRunningMarkerFileName), cfg.Service, 0, 0)
		if unitErr == nil && markerErr == nil && unit.ActiveState == "active" && unit.MainPID > 0 && unit.MainPID != priorPID && marker.RunnerPID == unit.MainPID && vmRuntimeMarkerMatchesArtifact(marker, artifact) {
			if err := probeFirecrackerInstance(ctx, service.VM.Sockets.APISocketPath, vmJailerID(cfg.Service), version); err == nil {
				if err := verifyVMRuntimeIntegrationProcessIdentity(marker.ChildPID, cfg.TestUID, cfg.TestGID); err == nil {
					if readiness, err := vmJailerReadinessForRootWithOwner(root, 0, 0); err == nil && readiness == vmJailerReady {
						return marker, waitVMRuntimeIntegrationGuest(t, ctx, service.VM.Sockets.VsockSocketPath)
					}
				}
			}
		}
		lastErr = errors.Join(unitErr, markerErr)
		select {
		case <-ctx.Done():
			t.Fatalf("wait for runtime %s: %v (last error: %v)", artifact.ID, ctx.Err(), lastErr)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func verifyVMRuntimeIntegrationProcessIdentity(pid int, uid, gid uint32) error {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return err
	}
	values := map[string]uint32{"Uid:": uid, "Gid:": gid}
	for label, want := range values {
		found := false
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 5 || fields[0] != label {
				continue
			}
			found = true
			for _, field := range fields[1:] {
				got, parseErr := strconv.ParseUint(field, 10, 32)
				if parseErr != nil || uint32(got) != want {
					return fmt.Errorf("process %d %s values = %v, want all %d", pid, label, fields[1:], want)
				}
			}
		}
		if !found {
			return fmt.Errorf("process %d status has no %s field", pid, label)
		}
	}
	return nil
}

func waitVMRuntimeIntegrationTrial(t *testing.T, ctx context.Context, server *Server, serviceName string, configured, previous db.VMRuntimeArtifactConfig, outcome vmRuntimeTrialOutcome) {
	t.Helper()
	var lastErr error
	for {
		lastErr = server.consumeVMRuntimeTrialResults(ctx)
		service := vmRuntimeIntegrationService(t, server, serviceName)
		runtime := service.VM.Components.Runtime
		if lastErr == nil && runtime.Configured == configured && runtime.Staged == nil && runtime.Previous != nil && *runtime.Previous == previous && runtime.Trial != nil && runtime.Trial.State == string(outcome) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for runtime trial result: %v (last error: %v, state: %#v)", ctx.Err(), lastErr, runtime)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func vmRuntimeIntegrationService(t *testing.T, server *Server, name string) *db.Service {
	t.Helper()
	view, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("read integration database: %v", err)
	}
	service := view.AsStruct().Services[name]
	if service == nil || service.VM == nil {
		t.Fatalf("integration VM %q is absent", name)
	}
	return service
}

func removeVMRuntimeIntegrationService(server *Server, service string) error {
	var output bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(), s: server, sn: service, user: "root",
		progress: catchrpc.ProgressQuiet, rawRW: &output, rw: &output,
	}
	if err := execer.removeCmdFunc(cli.RemoveFlags{Yes: true, CleanData: true}); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(output.String()))
	}
	return nil
}

func runVMRuntimeIntegrationSSH(ctx context.Context, cfg vmRuntimeIntegrationConfig, user string, ip netip.Addr, command string) (string, error) {
	args := []string{
		"-i", cfg.SSHPrivateKey,
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=5",
		user + "@" + ip.String(), command,
	}
	output, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("ssh %s: %w: %s", command, err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func verifyVMRuntimeIntegrationDiskRestore(t *testing.T, ctx context.Context, server *Server, cfg vmRuntimeIntegrationConfig, ip netip.Addr, priorPID int) {
	t.Helper()
	service := vmRuntimeIntegrationService(t, server, cfg.Service)
	if _, err := runVMRuntimeIntegrationSSH(ctx, cfg, service.VM.SSH.User, ip, "printf 'before\\n' > /var/tmp/yeet-runtime-integration-state && sync"); err != nil {
		t.Fatalf("write pre-snapshot guest marker: %v", err)
	}
	var snapshotOut bytes.Buffer
	if err := server.createVMSnapshot(ctx, cfg.Service, cli.SnapshotsCreateFlags{Comment: "runtime integration disk restore"}, &snapshotOut); err != nil {
		t.Fatalf("create disk recovery point: %v", err)
	}
	snapshot := strings.TrimSpace(strings.TrimPrefix(snapshotOut.String(), "VM snapshot:"))
	if snapshot == "" || snapshot == snapshotOut.String() {
		t.Fatalf("parse VM snapshot output: %q", snapshotOut.String())
	}
	if _, err := runVMRuntimeIntegrationSSH(ctx, cfg, service.VM.SSH.User, ip, "printf 'after\\n' > /var/tmp/yeet-runtime-integration-state && sync"); err != nil {
		t.Fatalf("write post-snapshot guest marker: %v", err)
	}
	var restoreIO bytes.Buffer
	if err := server.restoreRecoveryPoint(ctx, cfg.Service, snapshot, cli.SnapshotsRestoreFlags{Stop: true, Start: true, Yes: true}, &restoreIO); err != nil {
		t.Fatalf("restore disk recovery point: %v\n%s", err, restoreIO.String())
	}
	service = vmRuntimeIntegrationService(t, server, cfg.Service)
	ip = waitVMRuntimeIntegrationGuest(t, ctx, service.VM.Sockets.VsockSocketPath)
	value, err := runVMRuntimeIntegrationSSH(ctx, cfg, service.VM.SSH.User, ip, "cat /var/tmp/yeet-runtime-integration-state")
	if err != nil {
		t.Fatalf("read restored guest marker: %v", err)
	}
	if strings.TrimSpace(value) != "before" {
		t.Fatalf("restored guest marker = %q, want before", strings.TrimSpace(value))
	}
	unit, err := readVMRuntimeUnitState(ctx, vmSystemdUnitName(cfg.Service))
	if err != nil || unit.MainPID == priorPID {
		t.Fatalf("restored VM did not restart: state=%#v err=%v", unit, err)
	}
}

func buildVMRuntimeIntegrationBrokenCandidate(t *testing.T, cfg vmRuntimeIntegrationConfig, manifest vmRuntimeManifest) ([]byte, string) {
	t.Helper()
	dash := strings.LastIndex(manifest.RuntimeID, "-yeet-v")
	if dash < 0 {
		t.Fatalf("candidate runtime ID has no Yeet revision: %s", manifest.RuntimeID)
	}
	revision, err := strconv.Atoi(strings.TrimPrefix(manifest.RuntimeID[dash:], "-yeet-v"))
	if err != nil {
		t.Fatalf("parse candidate Yeet revision: %v", err)
	}
	manifest.RuntimeID = manifest.RuntimeID[:dash] + "-yeet-v" + strconv.Itoa(revision+1)

	dir, err := os.MkdirTemp(cfg.DataRoot, ".runtime-fallback-build-")
	if err != nil {
		t.Fatalf("create fallback build directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	source := filepath.Join(dir, "main.go")
	program := fmt.Sprintf("package main\nimport (\"fmt\"; \"os\")\nfunc main() { if len(os.Args) == 2 && os.Args[1] == \"--version\" { fmt.Println(%q); return }; os.Exit(78) }\n", "Firecracker "+manifest.Upstream.Version)
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatalf("write fallback runtime source: %v", err)
	}
	binary := filepath.Join(dir, "firecracker")
	command := exec.Command("go", "build", "-trimpath", "-o", binary, source)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fallback runtime fixture: %v: %s", err, strings.TrimSpace(string(output)))
	}
	digest, err := sha256VMRuntimeIntegrationFile(binary)
	if err != nil {
		t.Fatalf("digest fallback runtime fixture: %v", err)
	}
	manifest.Components.Firecracker.SHA256 = digest
	manifest.Components.Firecracker.VersionOutput = "Firecracker " + manifest.Upstream.Version
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("encode fallback runtime manifest: %v", err)
	}
	raw = append(raw, '\n')
	if _, err := decodeVMRuntimeManifest(raw); err != nil {
		t.Fatalf("validate fallback runtime manifest: %v", err)
	}
	return raw, binary
}

func sha256VMRuntimeIntegrationFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
