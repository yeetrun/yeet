// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestVMRuntimePruneClassifiesEveryDurableReferenceAndFailsClosed(t *testing.T) {
	server := newTestServer(t)
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-runtimes")
	originalInspector := inspectVMRuntimeBinaryArchitecture
	inspectVMRuntimeBinaryArchitecture = func(string) (string, error) { return "amd64", nil }
	t.Cleanup(func() { inspectVMRuntimeBinaryArchitecture = originalInspector })

	configured, configuredRef := writeVMRuntimePruneLeaf(t, cacheRoot, "v1.10.0", "official")
	staged, stagedRef := writeVMRuntimePruneLeaf(t, cacheRoot, "v1.11.0", "official")
	previous, previousRef := writeVMRuntimePruneLeaf(t, cacheRoot, "v1.12.0", "official")
	running, runningRef := writeVMRuntimePruneLeaf(t, cacheRoot, "v1.13.0", "official")
	journaled, journaledRef := writeVMRuntimePruneLeaf(t, cacheRoot, "v1.14.0", "official")
	protected, protectedRef := writeVMRuntimePruneLeaf(t, cacheRoot, "v1.15.0", "official")
	revoked, revokedRef := writeVMRuntimePruneLeaf(t, cacheRoot, "v1.15.1", "official")
	stable, stableRef := writeVMRuntimePruneLeaf(t, cacheRoot, "v1.16.1", "official")
	unreferencedOfficial, unreferencedOfficialRef := writeVMRuntimePruneLeaf(t, cacheRoot, "v1.16.0", "official")
	unreferencedImported, _ := writeVMRuntimePruneLeaf(t, cacheRoot, "v1.9.0", "local:lab")

	malformed := filepath.Join(cacheRoot, "amd64", "firecracker-v1.8.0-yeet-v1", strings.Repeat("8", 64))
	if err := os.MkdirAll(malformed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(malformed, vmRuntimeManifestFilename), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(cacheRoot, "amd64", "firecracker-v1.7.0-yeet-v1", strings.Repeat("7", 64))
	if err := os.MkdirAll(filepath.Dir(symlink), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stable.Firecracker[:strings.LastIndex(stable.Firecracker, "/firecracker")], symlink); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(cacheRoot, "amd64", "notes")
	if err := os.MkdirAll(unknown, 0o755); err != nil {
		t.Fatal(err)
	}

	monolithic := filepath.Join(server.cfg.RootDir, "vm-images", "ubuntu-v15")
	if err := os.MkdirAll(monolithic, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := server.cfg.DB.Set(&db.Data{
		VMHost: &db.VMHostConfig{ProtectedRuntimeIDs: []string{protected.ID}},
		Services: map[string]*db.Service{
			"devbox": {
				Name: "devbox", ServiceType: db.ServiceTypeVM,
				ServiceRoot: server.defaultServiceRootDir("devbox"),
				VM: &db.VMConfig{
					Image: db.VMImageConfig{RootFS: filepath.Join(monolithic, "rootfs.ext4"), Kernel: filepath.Join(monolithic, "vmlinux")},
					Components: &db.VMComponentsConfig{Runtime: db.VMRuntimeLifecycleConfig{
						Configured: configured, Staged: vmRuntimeArtifactPtr(staged), Previous: vmRuntimeArtifactPtr(previous),
					}},
				},
			},
			"revoked": {
				Name: "revoked", ServiceType: db.ServiceTypeVM,
				VM: &db.VMConfig{Components: &db.VMComponentsConfig{Runtime: db.VMRuntimeLifecycleConfig{Configured: revoked}}},
			},
			"runner": {
				Name: "runner", ServiceType: db.ServiceTypeVM,
				ServiceRoot: server.defaultServiceRootDir("runner"),
				VM:          &db.VMConfig{Components: &db.VMComponentsConfig{Runtime: db.VMRuntimeLifecycleConfig{Configured: configured}}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	writeVMRuntimeStatusMarker(t, server, "runner", running, 4101, 4102)
	server.vmRuntimePruneDeps = &vmRuntimePruneDeps{
		unitState: func(_ context.Context, service string) (vmRuntimeUnitState, error) {
			if service == "runner" {
				return vmRuntimeUnitState{ActiveState: "active", MainPID: 4101}, nil
			}
			return vmRuntimeUnitState{ActiveState: "inactive"}, nil
		},
		processAlive: func(pid int) bool { return pid == 4101 || pid == 4102 },
		uid:          uint32(os.Geteuid()),
		gid:          uint32(os.Getegid()),
	}

	catalog := vmRuntimePruneCatalog(stableRef, []vmRuntimeCatalogRef{
		configuredRef, stagedRef, previousRef, runningRef, journaledRef, protectedRef,
		revokedRef, stableRef, unreferencedOfficialRef,
	})
	catalog.Revocations = []vmRuntimeRevocation{{
		RuntimeID: revoked.ID, ManifestSHA: revoked.ManifestSHA256,
		Reason: "withdrawn", RecordedAt: "2026-07-19T00:00:00Z",
	}}
	groups := []vmRuntimeJournalGroup{{Records: []vmRuntimeJournalRecord{{
		OldDB: vmRuntimeJournalDBProjection{Components: &db.VMComponentsConfig{Runtime: db.VMRuntimeLifecycleConfig{Configured: journaled}}},
		NewDB: vmRuntimeJournalDBProjection{Components: &db.VMComponentsConfig{Runtime: db.VMRuntimeLifecycleConfig{Configured: journaled}}},
	}}}}

	rows, err := server.planVMRuntimePruneWithCatalogAndGroups(context.Background(), catalog, groups)
	if err != nil {
		t.Fatalf("planVMRuntimePruneWithCatalogAndGroups: %v", err)
	}
	wants := map[string]struct{ action, reason string }{
		filepath.Dir(configured.Firecracker):           {vmRuntimePruneActionKeep, vmRuntimePruneReasonConfigured},
		filepath.Dir(staged.Firecracker):               {vmRuntimePruneActionKeep, vmRuntimePruneReasonStaged},
		filepath.Dir(previous.Firecracker):             {vmRuntimePruneActionKeep, vmRuntimePruneReasonPrevious},
		filepath.Dir(running.Firecracker):              {vmRuntimePruneActionKeep, vmRuntimePruneReasonRunning},
		filepath.Dir(journaled.Firecracker):            {vmRuntimePruneActionKeep, vmRuntimePruneReasonJournal},
		filepath.Dir(protected.Firecracker):            {vmRuntimePruneActionKeep, vmRuntimePruneReasonProtected},
		filepath.Dir(revoked.Firecracker):              {vmRuntimePruneActionKeep, vmRuntimePruneReasonConfigured},
		filepath.Dir(stable.Firecracker):               {vmRuntimePruneActionKeep, vmRuntimePruneReasonStable},
		filepath.Dir(unreferencedOfficial.Firecracker): {vmRuntimePruneActionRemove, vmRuntimePruneReasonUnreferenced},
		filepath.Dir(unreferencedImported.Firecracker): {vmRuntimePruneActionRemove, vmRuntimePruneReasonUnreferenced},
		malformed: {vmRuntimePruneActionKeep, vmRuntimePruneReasonUnknown},
		symlink:   {vmRuntimePruneActionKeep, vmRuntimePruneReasonUnknown},
		unknown:   {vmRuntimePruneActionKeep, vmRuntimePruneReasonUnknown},
	}
	for path, want := range wants {
		row, ok := vmRuntimePruneRowForPath(rows, path)
		if !ok {
			t.Errorf("missing prune row for %s; rows=%#v", path, rows)
			continue
		}
		if row.Action != want.action || row.Reason != want.reason {
			t.Errorf("row for %s = %#v, want action=%q reason=%q", path, row, want.action, want.reason)
		}
	}
	if _, err := os.Stat(monolithic); err != nil {
		t.Fatalf("monolithic guest/kernel base was touched: %v", err)
	}
}

func TestVMRuntimePruneDryRunAndApplyUseSamePlanAndQuarantineRemoval(t *testing.T) {
	server := newTestServer(t)
	cacheRoot := filepath.Join(server.cfg.RootDir, "vm-runtimes")
	originalInspector := inspectVMRuntimeBinaryArchitecture
	inspectVMRuntimeBinaryArchitecture = func(string) (string, error) { return "amd64", nil }
	t.Cleanup(func() { inspectVMRuntimeBinaryArchitecture = originalInspector })
	stable, stableRef := writeVMRuntimePruneLeaf(t, cacheRoot, "v1.16.1", "official")
	old, oldRef := writeVMRuntimePruneLeaf(t, cacheRoot, "v1.15.0", "official")
	aliasDir, err := ensureVMRuntimeLocalAliasDir(cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	alias := vmRuntimeLocalAlias{
		SchemaVersion: 1, Name: "old-lab", Architecture: "amd64", RuntimeID: old.ID,
		ManifestSHA256: old.ManifestSHA256, Source: "local:old-lab",
	}
	if err := publishVMRuntimeLocalAlias(aliasDir, alias); err != nil {
		t.Fatal(err)
	}
	catalog := vmRuntimePruneCatalog(stableRef, []vmRuntimeCatalogRef{stableRef, oldRef})
	server.vmRuntimePruneDeps = &vmRuntimePruneDeps{
		fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) { return catalog, nil },
		unitState:    func(context.Context, string) (vmRuntimeUnitState, error) { return vmRuntimeUnitState{}, nil },
		processAlive: func(int) bool { return false }, uid: uint32(os.Geteuid()), gid: uint32(os.Getegid()),
	}

	var dry bytes.Buffer
	if err := server.pruneVMRuntimes(context.Background(), &dry, true); err != nil {
		t.Fatalf("dry-run prune: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(old.Firecracker)); err != nil {
		t.Fatalf("dry-run removed old runtime: %v", err)
	}
	var applied bytes.Buffer
	if err := server.pruneVMRuntimes(context.Background(), &applied, false); err != nil {
		t.Fatalf("apply prune: %v", err)
	}
	if _, err := os.Lstat(filepath.Dir(old.Firecracker)); !os.IsNotExist(err) {
		t.Fatalf("old runtime still exists after prune: %v", err)
	}
	if _, err := os.Lstat(vmRuntimeLocalAliasPath(aliasDir, alias.Name)); !os.IsNotExist(err) {
		t.Fatalf("local alias still exists after its runtime was pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(stable.Firecracker)); err != nil {
		t.Fatalf("stable runtime was removed: %v", err)
	}
	if !strings.Contains(dry.String(), old.ID) || !strings.Contains(applied.String(), old.ID) {
		t.Fatalf("prune output missing old runtime\ndry=%s\napply=%s", dry.String(), applied.String())
	}
	if strings.Contains(dry.String(), vmRuntimePruneActionRemoved) || !strings.Contains(applied.String(), vmRuntimePruneActionRemoved) {
		t.Fatalf("dry/apply actions do not distinguish removal\ndry=%s\napply=%s", dry.String(), applied.String())
	}
}

func writeVMRuntimePruneLeaf(t *testing.T, root, version, source string) (db.VMRuntimeArtifactConfig, vmRuntimeCatalogRef) {
	t.Helper()
	manifest := validVMRuntimeManifest()
	manifest.RuntimeID = "firecracker-" + version + "-yeet-v1"
	manifest.Upstream.Version = version
	manifest.Upstream.Tag = version
	manifest.Upstream.ArchiveURL = "https://github.com/firecracker-microvm/firecracker/releases/download/" + version + "/firecracker-" + version + "-x86_64.tgz"
	manifest.Upstream.ChecksumURL = manifest.Upstream.ArchiveURL + ".sha256.txt"
	manifest.Components.Firecracker.VersionOutput = "Firecracker " + version
	manifest.Components.Jailer.VersionOutput = "Jailer " + version
	firecracker := vmRuntimeTestExecutable("Firecracker", version)
	jailer := vmRuntimeTestExecutable("Jailer", version)
	manifest.Components.Firecracker.SHA256 = vmRuntimeTestSHA256(firecracker)
	manifest.Components.Jailer.SHA256 = vmRuntimeTestSHA256(jailer)
	raw := marshalVMRuntimeTestJSON(t, manifest)
	manifestSHA := vmRuntimeTestSHA256(raw)
	dir := filepath.Join(root, "amd64", manifest.RuntimeID, manifestSHA)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string][]byte{
		vmRuntimeManifestFilename: raw,
		"firecracker":             firecracker,
		"jailer":                  jailer,
	} {
		mode := os.FileMode(0o755)
		if name == vmRuntimeManifestFilename {
			mode = 0o644
		}
		if err := os.WriteFile(filepath.Join(dir, name), contents, mode); err != nil {
			t.Fatal(err)
		}
	}
	artifact := db.VMRuntimeArtifactConfig{
		ID: manifest.RuntimeID, ManifestSHA256: manifestSHA,
		FirecrackerSHA256: manifest.Components.Firecracker.SHA256,
		JailerSHA256:      manifest.Components.Jailer.SHA256,
		Firecracker:       filepath.Join(dir, "firecracker"),
		Jailer:            filepath.Join(dir, "jailer"),
		Source:            source,
	}
	ref := vmRuntimeCatalogRef{
		RuntimeID: manifest.RuntimeID, ManifestSHA: manifestSHA,
		UpstreamVersion: version, Support: manifest.Support.State,
	}
	return artifact, ref
}

func vmRuntimePruneCatalog(stable vmRuntimeCatalogRef, refs []vmRuntimeCatalogRef) vmRuntimeCatalog {
	return vmRuntimeCatalog{SchemaVersion: 1, Architectures: map[string]vmRuntimeCatalogArchitecture{
		"amd64": {
			Runtimes: refs,
			Channels: map[string]*vmRuntimeCatalogIdentity{
				"stable": {RuntimeID: stable.RuntimeID, ManifestSHA: stable.ManifestSHA},
			},
		},
	}}
}

func vmRuntimePruneRowForPath(rows []vmRuntimePruneRow, path string) (vmRuntimePruneRow, bool) {
	for _, row := range rows {
		if row.Path == path {
			return row, true
		}
	}
	return vmRuntimePruneRow{}, false
}
