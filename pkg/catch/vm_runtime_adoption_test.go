// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
	"golang.org/x/sys/unix"
)

func TestVMRuntimeAdoptionInventoryMeasuresEffectiveLaunchComposition(t *testing.T) {
	fixture := newVMRuntimeAdoptionFixture(t, false)
	decoyDir := filepath.Join(fixture.dataRoot, "decoy")
	writeVMRuntimeAdoptionTestFile(t, filepath.Join(decoyDir, "firecracker"), "wrong-firecracker", 0o755)
	writeVMRuntimeAdoptionTestFile(t, filepath.Join(decoyDir, "jailer"), "wrong-jailer", 0o755)
	fixture.service.VM.Image.RootFS = fixture.rootFS
	fixture.service.VM.Image.Kernel = fixture.kernel
	fixture.persist(t)

	first := fixture.inventory(t)
	second := fixture.inventory(t)
	if len(first.VMs) != 1 {
		t.Fatalf("VM count = %d, want 1", len(first.VMs))
	}
	vm := first.VMs[0]
	if vm.Classification != vmRuntimeAdoptionCustomLegacy || vm.BlockedReason != "" {
		t.Fatalf("classification = %q, blocked = %q", vm.Classification, vm.BlockedReason)
	}
	if vm.EffectiveKernel != fixture.kernel || vm.EffectiveRuntime.Firecracker != fixture.firecracker || vm.EffectiveRuntime.Jailer != fixture.jailer {
		t.Fatalf("effective composition = kernel %q runtime %#v", vm.EffectiveKernel, vm.EffectiveRuntime)
	}
	if vm.EffectiveUnit.Runner != fixture.unitExec[0] || vm.EffectiveUnit.JailerBase != vmJailerBaseForDataRoot(fixture.dataRoot) {
		t.Fatalf("loaded stable paths = runner %q jailer base %q", vm.EffectiveUnit.Runner, vm.EffectiveUnit.JailerBase)
	}
	rootSHA := sha256FileForVMRuntimeAdoptionTest(t, fixture.rootFS)
	kernelSHA := sha256FileForVMRuntimeAdoptionTest(t, fixture.kernel)
	fcSHA := sha256FileForVMRuntimeAdoptionTest(t, fixture.firecracker)
	jailerSHA := sha256FileForVMRuntimeAdoptionTest(t, fixture.jailer)
	if !strings.HasSuffix(vm.Components.GuestBase.ID, rootSHA) || !strings.HasSuffix(vm.Components.Kernel.ID, kernelSHA) {
		t.Fatalf("component IDs do not retain full component SHA-256: %#v", vm.Components)
	}
	wantRuntimeID := "legacy-firecracker-1-16-1-" + fcSHA + "-jailer-" + jailerSHA
	if vm.Components.Runtime.Configured.ID != wantRuntimeID {
		t.Fatalf("runtime ID = %q, want %q", vm.Components.Runtime.Configured.ID, wantRuntimeID)
	}
	if vm.CompositionSHA256 == "" || vm.PreconditionSHA256 == "" || vm.CompositionSHA256 == vm.PreconditionSHA256 {
		t.Fatalf("composition/precondition digests = %q / %q", vm.CompositionSHA256, vm.PreconditionSHA256)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated offline inventory is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if !slices.Equal(first.Summary.Adoptable, []string{"devbox"}) || len(first.Summary.Blocked) != 0 {
		t.Fatalf("summary = %#v", first.Summary)
	}
	fixture.service.VM.MemoryBytes++
	fixture.persist(t)
	if changed := fixture.onlyVM(t); changed.PreconditionSHA256 == vm.PreconditionSHA256 {
		t.Fatal("stored VM state drift did not change the adoption precondition")
	}
}

func TestVMRuntimeAdoptionInventoryUsesFirecrackerKernelSyncPath(t *testing.T) {
	fixture := newVMRuntimeAdoptionFixture(t, true)
	syncedDir := filepath.Join(serviceRunDirForRoot(fixture.serviceRoot), "kernels", fixture.service.Name, "linux-7.2.0-yeet")
	syncedKernel := filepath.Join(syncedDir, "vmlinux")
	writeVMRuntimeAdoptionTestFile(t, syncedKernel, "synced-kernel", 0o644)
	writeVMRuntimeAdoptionTestFile(t, filepath.Join(syncedDir, "kernel.config"), "synced-config", 0o644)
	fixture.kernel = syncedKernel
	fixture.writeFirecrackerConfig(t)
	fixture.persist(t) // deliberately retain the stale VM.Image.Kernel field

	vm := fixture.onlyVM(t)
	if vm.Classification != vmRuntimeAdoptionCustomLegacy {
		t.Fatalf("classification = %q, reason = %q", vm.Classification, vm.BlockedReason)
	}
	if vm.NewDB.ImageKernel != syncedKernel || vm.Components.Kernel.Path != syncedKernel || vm.Components.Kernel.Source != string(vmRuntimeAdoptionCustomLegacy) {
		t.Fatalf("synced kernel preparation = %#v", vm)
	}
	if vm.Components.GuestBase.ManifestSHA256 == vm.Components.Kernel.ManifestSHA256 {
		t.Fatalf("service-local kernel incorrectly inherited guest manifest digest %q", vm.Components.Kernel.ManifestSHA256)
	}
}

func TestVMRuntimeAdoptionInventoryBlocksUntrustedMetadataInsteadOfDowngrading(t *testing.T) {
	tests := []struct {
		name string
		edit func(*vmRuntimeAdoptionFixture)
		want string
	}{
		{
			name: "invalid manifest",
			edit: func(f *vmRuntimeAdoptionFixture) {
				writeVMRuntimeAdoptionTestFile(f.t, filepath.Join(f.imageDir, "manifest.json"), `{}`, 0o644)
			},
			want: "validate installed VM manifest",
		},
		{
			name: "invalid official receipt",
			edit: func(f *vmRuntimeAdoptionFixture) {
				writeVMRuntimeAdoptionTestFile(f.t, filepath.Join(f.imageDir, vmRuntimeAdoptionReceiptFileName), `{}`, 0o600)
			},
			want: "unsupported installed VM receipt schema",
		},
		{
			name: "invalid local ref",
			edit: func(f *vmRuntimeAdoptionFixture) {
				f.service.VM.Image.Payload = "vm://team/devbox"
				path := localVMImageRefPath(filepath.Join(f.dataRoot, "vm-images"), "team/devbox")
				writeVMRuntimeAdoptionTestFile(f.t, path, `{}`, 0o600)
				f.persist(f.t)
			},
			want: "content ID must be an exact lowercase SHA-256",
		},
		{
			name: "runtime inferred from sibling forbidden",
			edit: func(f *vmRuntimeAdoptionFixture) {
				f.unitExec = nil
			},
			want: "no effective ExecStart",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newVMRuntimeAdoptionFixture(t, true)
			tt.edit(fixture)
			vm := fixture.onlyVM(t)
			if vm.Classification != vmRuntimeAdoptionBlocked || vm.Components != nil || !strings.Contains(vm.BlockedReason, tt.want) {
				t.Fatalf("blocked VM = classification %q components %#v reason %q, want %q", vm.Classification, vm.Components, vm.BlockedReason, tt.want)
			}
		})
	}
}

func TestVMRuntimeAdoptionInventoryRejectsUntrustedArtifactAncestors(t *testing.T) {
	t.Run("writable ancestor", func(t *testing.T) {
		fixture := newVMRuntimeAdoptionFixture(t, false)
		if err := os.Chmod(fixture.imageDir, 0o777); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chmod(fixture.imageDir, 0o755) }()

		vm := fixture.onlyVM(t)
		if vm.Classification != vmRuntimeAdoptionBlocked || !strings.Contains(vm.BlockedReason, "ancestor") || !strings.Contains(vm.BlockedReason, "group or other writable") {
			t.Fatalf("writable ancestor result = %#v", vm)
		}
	})

	t.Run("symbolic-link ancestor", func(t *testing.T) {
		fixture := newVMRuntimeAdoptionFixture(t, false)
		link := filepath.Join(fixture.dataRoot, "linked-image")
		if err := os.Symlink(fixture.imageDir, link); err != nil {
			t.Fatal(err)
		}
		fixture.service.VM.Image.RootFS = filepath.Join(link, filepath.Base(fixture.rootFS))
		fixture.persist(t)

		vm := fixture.onlyVM(t)
		if vm.Classification != vmRuntimeAdoptionBlocked || !strings.Contains(vm.BlockedReason, "refusing symbolic link") {
			t.Fatalf("symbolic-link ancestor result = %#v", vm)
		}
	})
}

func TestVMRuntimeAdoptionInventoryRejectsDuplicateAuthoritativeJSON(t *testing.T) {
	t.Run("Firecracker config", func(t *testing.T) {
		fixture := newVMRuntimeAdoptionFixture(t, false)
		path := filepath.Join(serviceRunDirForRoot(fixture.serviceRoot), "firecracker.json")
		raw := readVMRuntimeAdoptionTestFile(t, path)
		raw = bytes.Replace(raw, []byte(`"kernel_image_path":`), []byte(`"kernel_image_path":"/decoy","kernel_image_path":`), 1)
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}

		vm := fixture.onlyVM(t)
		if vm.Classification != vmRuntimeAdoptionBlocked || !strings.Contains(vm.BlockedReason, "duplicate field") {
			t.Fatalf("duplicate Firecracker config result = %#v", vm)
		}
	})

	t.Run("installed manifest", func(t *testing.T) {
		fixture := newVMRuntimeAdoptionFixture(t, true)
		path := filepath.Join(fixture.imageDir, "manifest.json")
		raw := readVMRuntimeAdoptionTestFile(t, path)
		raw = bytes.Replace(raw, []byte(`"name":`), []byte(`"name":"decoy","name":`), 1)
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}

		vm := fixture.onlyVM(t)
		if vm.Classification != vmRuntimeAdoptionBlocked || !strings.Contains(vm.BlockedReason, "duplicate field") {
			t.Fatalf("duplicate manifest result = %#v", vm)
		}
	})
}

func TestVMRuntimeAdoptionInventoryOfficialReceiptIsExactAndOffline(t *testing.T) {
	fixture := newVMRuntimeAdoptionFixture(t, true)
	manifestRaw := readVMRuntimeAdoptionTestFile(t, filepath.Join(fixture.imageDir, "manifest.json"))
	receipt := vmRuntimeAdoptionReceipt{
		Schema: vmRuntimeAdoptionReceiptSchema, SchemaVersion: vmRuntimeAdoptionReceiptSchemaVersion,
		Payload: fixture.service.VM.Image.Payload, Version: fixture.service.VM.Image.Version,
		CatalogURL: defaultVMImageCatalogURL, CatalogSHA256: strings.Repeat("a", 64),
		ManifestURL:    "https://github.com/yeetrun/yeet-vm-images/releases/download/test/manifest.json",
		ManifestSHA256: vmLegacySHA256Bytes(manifestRaw), PreparedRootFS: fixture.rootFS,
		PreparedRootFSSHA256: sha256FileForVMRuntimeAdoptionTest(t, fixture.rootFS),
	}
	writeVMRuntimeAdoptionTestJSON(t, filepath.Join(fixture.imageDir, vmRuntimeAdoptionReceiptFileName), receipt, 0o600)
	vm := fixture.onlyVM(t)
	if vm.Classification != vmRuntimeAdoptionOfficialLegacy {
		t.Fatalf("classification = %q, reason = %q", vm.Classification, vm.BlockedReason)
	}
	if vm.Components.GuestBase.ManifestSHA256 != receipt.ManifestSHA256 || vm.Components.Runtime.Configured.Source != string(vmRuntimeAdoptionOfficialLegacy) {
		t.Fatalf("official component provenance = %#v", vm.Components)
	}

	receipt.PreparedRootFSSHA256 = strings.Repeat("b", 64)
	writeVMRuntimeAdoptionTestJSON(t, filepath.Join(fixture.imageDir, vmRuntimeAdoptionReceiptFileName), receipt, 0o600)
	blocked := fixture.onlyVM(t)
	if blocked.Classification != vmRuntimeAdoptionBlocked || !strings.Contains(blocked.BlockedReason, "does not bind the effective prepared rootfs") {
		t.Fatalf("contradictory receipt was downgraded: %#v", blocked)
	}
}

func TestVMRuntimeAdoptionInventoryDoesNotAssignCompressedManifestDigestToPreparedRootFS(t *testing.T) {
	fixture := newVMRuntimeAdoptionFixture(t, true)
	compressed := filepath.Join(fixture.imageDir, "rootfs.ext4.zst")
	writeVMRuntimeAdoptionTestFile(t, compressed, "compressed-source-bytes", 0o644)
	manifest := fixture.manifest(t)
	delete(manifest.Checksums, manifest.RootFS)
	manifest.RootFS = filepath.Base(compressed)
	manifest.Checksums[manifest.RootFS] = sha256FileForVMRuntimeAdoptionTest(t, compressed)
	writeVMRuntimeAdoptionTestJSON(t, filepath.Join(fixture.imageDir, "manifest.json"), manifest, 0o644)
	manifestRaw := readVMRuntimeAdoptionTestFile(t, filepath.Join(fixture.imageDir, "manifest.json"))

	vm := fixture.onlyVM(t)
	if vm.Classification != vmRuntimeAdoptionCustomLegacy {
		t.Fatalf("classification = %q, reason = %q", vm.Classification, vm.BlockedReason)
	}
	if vm.Components.GuestBase.ManifestSHA256 != vm.CompositionSHA256 {
		t.Fatalf("prepared rootfs manifest digest = %q, want synthetic composition %q", vm.Components.GuestBase.ManifestSHA256, vm.CompositionSHA256)
	}
	if vm.Components.GuestBase.ManifestSHA256 == vmLegacySHA256Bytes(manifestRaw) {
		t.Fatal("compressed manifest digest was incorrectly assigned to decompressed prepared rootfs")
	}
}

func TestVMRuntimeAdoptionInventoryBlocksManifestChecksumContradiction(t *testing.T) {
	fixture := newVMRuntimeAdoptionFixture(t, true)
	manifest := fixture.manifest(t)
	manifest.Checksums[manifest.Firecracker] = strings.Repeat("f", 64)
	writeVMRuntimeAdoptionTestJSON(t, filepath.Join(fixture.imageDir, "manifest.json"), manifest, 0o644)
	vm := fixture.onlyVM(t)
	if vm.Classification != vmRuntimeAdoptionBlocked || !strings.Contains(vm.BlockedReason, "checksum") {
		t.Fatalf("manifest checksum contradiction was downgraded: %#v", vm)
	}
}

func TestVMRuntimeAdoptionInventoryAcceptsExactLocalRef(t *testing.T) {
	fixture := newVMRuntimeAdoptionFixture(t, true)
	name := "team/devbox"
	manifest := fixture.manifest(t)
	manifest.KernelPolicy = localVMImageKernelPolicyLocal
	writeVMRuntimeAdoptionTestJSON(t, filepath.Join(fixture.imageDir, "manifest.json"), manifest, 0o644)
	capabilities := localVMImageCapabilitiesFromManifest(manifest)
	contentID, err := localVMImageContentID(name, fixture.rootFS, fixture.kernel, fixture.firecracker, fixture.jailer, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	blobDir := filepath.Join(fixture.dataRoot, "vm-images", "local", "blobs", contentID)
	if err := os.MkdirAll(filepath.Dir(blobDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.imageDir, blobDir); err != nil {
		t.Fatal(err)
	}
	fixture.imageDir = blobDir
	fixture.rootFS = filepath.Join(blobDir, manifest.RootFS)
	fixture.kernel = filepath.Join(blobDir, manifest.Kernel)
	fixture.firecracker = filepath.Join(blobDir, manifest.Firecracker)
	fixture.jailer = filepath.Join(blobDir, manifest.Jailer)
	fixture.service.VM.Image.Payload = vmImagePayloadPrefix + name
	fixture.service.VM.Image.Version = manifest.Version
	fixture.service.VM.Image.RootFS = fixture.rootFS
	fixture.service.VM.Image.Kernel = fixture.kernel
	fixture.writeFirecrackerConfig(t)
	fixture.unitExec = fixture.execStart()
	ref := localVMImageRef{
		Name: name, Payload: vmImagePayloadPrefix + name, Version: manifest.Version, ContentID: contentID,
		Root: blobDir, RootFS: manifest.RootFS, Kernel: manifest.Kernel, Firecracker: manifest.Firecracker,
		Jailer: manifest.Jailer, KernelPolicy: localVMImageKernelPolicyLocal, CreatedAt: "2026-07-20T00:00:00Z",
	}
	writeVMRuntimeAdoptionTestJSON(t, localVMImageRefPath(filepath.Join(fixture.dataRoot, "vm-images"), name), ref, 0o600)
	fixture.persist(t)

	vm := fixture.onlyVM(t)
	if vm.Classification != vmRuntimeAdoptionLocalLegacy {
		t.Fatalf("classification = %q, reason = %q", vm.Classification, vm.BlockedReason)
	}
	if vm.Components.GuestBase.Source != string(vmRuntimeAdoptionLocalLegacy) || vm.Components.Runtime.Configured.Source != string(vmRuntimeAdoptionLocalLegacy) {
		t.Fatalf("local sources = %#v", vm.Components)
	}
}

func TestVMRuntimeAdoptionInventoryNeverHashesActiveDisk(t *testing.T) {
	fixture := newVMRuntimeAdoptionFixture(t, false)
	before := readVMRuntimeAdoptionTestFile(t, fixture.disk)
	var copied int64
	fixture.deps.evidence.copy = func(dst io.Writer, src io.Reader) (int64, error) {
		n, err := io.Copy(dst, src)
		copied += n
		return n, err
	}
	vm := fixture.onlyVM(t)
	if vm.Classification != vmRuntimeAdoptionCustomLegacy {
		t.Fatalf("classification = %q, reason = %q", vm.Classification, vm.BlockedReason)
	}
	wantCopied := fileSizesForVMRuntimeAdoptionTest(t, fixture.rootFS, fixture.kernel, filepath.Join(filepath.Dir(fixture.kernel), "kernel.config"), fixture.firecracker, fixture.jailer, fixture.unitPath)
	if copied != wantCopied {
		t.Fatalf("hashed bytes = %d, want immutable component bytes %d; active disk may have been read", copied, wantCopied)
	}
	if after := readVMRuntimeAdoptionTestFile(t, fixture.disk); !bytes.Equal(after, before) {
		t.Fatal("active disk changed during inventory")
	}
}

func TestVMRuntimeAdoptionInventoryUsesPersistedZFSServiceMountpointWithoutResolution(t *testing.T) {
	fixture := newVMRuntimeAdoptionFixture(t, false)
	fixture.service.ServiceRoot = fixture.serviceRoot
	fixture.service.ServiceRootZFS = "tank/services/devbox"
	zvolPath := "/dev/zvol/tank/vms/devbox/root"
	fixture.service.VM.Disk.Path = "tank/vms/devbox/root"
	fixture.service.VM.Disk.Backend = vmDiskBackendZVOL
	fixture.disk = zvolPath
	fixture.writeFirecrackerConfig(t)
	fixture.unitExec = fixture.execStart()
	fixture.persist(t)
	resolverCalls := 0
	fixture.deps.evidence.resolveZVOL = func(path string) (vmRuntimeAdoptionZVOLResolution, error) {
		resolverCalls++
		if path != zvolPath {
			return vmRuntimeAdoptionZVOLResolution{}, fmt.Errorf("unexpected zvol %s", path)
		}
		return vmRuntimeAdoptionZVOLResolution{
			Dataset: "tank/vms/devbox/root", ResolvedPath: "/dev/zd77",
			LinkDevice: 1, LinkInode: 2, LinkMode: unix.S_IFLNK | 0o777, LinkUID: 0, LinkGID: 0,
			Metadata: vmRuntimeAdoptionFileMetadata{Device: 3, Inode: 4, RDevice: 5, Mode: unix.S_IFBLK | 0o660, UID: 0},
		}, nil
	}
	vm := fixture.onlyVM(t)
	if vm.Classification != vmRuntimeAdoptionCustomLegacy || resolverCalls != 1 || vm.ServiceRoot != fixture.serviceRoot {
		t.Fatalf("ZFS inventory = classification %q reason %q calls %d root %q", vm.Classification, vm.BlockedReason, resolverCalls, vm.ServiceRoot)
	}
}

func TestVMRuntimeAdoptionInventoryBlocksMutableDiskAsPreparedBase(t *testing.T) {
	fixture := newVMRuntimeAdoptionFixture(t, false)
	fixture.service.VM.Image.RootFS = fixture.disk
	fixture.persist(t)
	vm := fixture.onlyVM(t)
	if vm.Classification != vmRuntimeAdoptionBlocked || !strings.Contains(vm.BlockedReason, "active mutable disk") {
		t.Fatalf("active disk reuse = %#v", vm)
	}
}

func TestVMRuntimeAdoptionInventoryRequiresCurrentLoadedUnitGeneration(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  string
	}{
		{value: "yes", want: "requires daemon-reload"},
		{value: "maybe", want: `NeedDaemonReload "maybe" is invalid`},
	} {
		t.Run(tt.value, func(t *testing.T) {
			fixture := newVMRuntimeAdoptionFixture(t, false)
			load := fixture.deps.loadUnit
			fixture.deps.loadUnit = func(ctx context.Context, name string) (vmRuntimeAdoptionLoadedUnit, error) {
				unit, err := load(ctx, name)
				unit.NeedDaemonReload = tt.value
				return unit, err
			}
			vm := fixture.onlyVM(t)
			if vm.Classification != vmRuntimeAdoptionBlocked || !strings.Contains(vm.BlockedReason, tt.want) {
				t.Fatalf("loaded generation result = %#v", vm)
			}
		})
	}
}

func TestVMRuntimeAdoptionInventoryAcceptsStoppedUnitEvidence(t *testing.T) {
	fixture := newVMRuntimeAdoptionFixture(t, false)
	load := fixture.deps.loadUnit
	fixture.deps.loadUnit = func(ctx context.Context, name string) (vmRuntimeAdoptionLoadedUnit, error) {
		unit, err := load(ctx, name)
		unit.ActiveState = "inactive"
		unit.MainPID = 0
		return unit, err
	}
	vm := fixture.onlyVM(t)
	if vm.Classification != vmRuntimeAdoptionCustomLegacy || vm.EffectiveUnit.ActiveState != "inactive" || vm.EffectiveUnit.MainPID != 0 {
		t.Fatalf("stopped VM inventory = %#v", vm)
	}
}

func TestVMRuntimeAdoptionInventoryRejectsEveryDescriptorModeFlag(t *testing.T) {
	for _, name := range []string{"--runtime-descriptor", "--runtime-running-marker", "--runtime-trial-result"} {
		t.Run(name, func(t *testing.T) {
			fixture := newVMRuntimeAdoptionFixture(t, false)
			fixture.unitExec = append(fixture.unitExec, name, "")
			vm := fixture.onlyVM(t)
			if vm.Classification != vmRuntimeAdoptionBlocked || !strings.Contains(vm.BlockedReason, "descriptor-mode flag "+name) {
				t.Fatalf("descriptor flag result = %#v", vm)
			}
		})
	}
}

func TestVMRuntimeAdoptionInventoryRejectsWrongJailerBaseAndRunner(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*vmRuntimeAdoptionFixture)
		want string
	}{
		{
			name: "wrong jailer base",
			edit: func(f *vmRuntimeAdoptionFixture) {
				for index := range f.unitExec {
					if f.unitExec[index] == "--jailer-base" {
						f.unitExec[index+1] = filepath.Join(f.dataRoot, "other-jailer")
					}
				}
			},
			want: "does not match configured data-root jailer base",
		},
		{
			name: "non-absolute runner",
			edit: func(f *vmRuntimeAdoptionFixture) { f.unitExec[0] = "catch" },
			want: "loaded unit runner must be a clean, absolute",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newVMRuntimeAdoptionFixture(t, false)
			tt.edit(fixture)
			vm := fixture.onlyVM(t)
			if vm.Classification != vmRuntimeAdoptionBlocked || !strings.Contains(vm.BlockedReason, tt.want) {
				t.Fatalf("invalid loaded unit = %#v", vm)
			}
		})
	}
}

func TestVMRuntimeAdoptionInventorySortsFleetAndSkipsAdoptedVMs(t *testing.T) {
	fixture := newVMRuntimeAdoptionFixture(t, false)
	data := &db.Data{Services: map[string]*db.Service{
		"zeta": fixture.service,
		"alpha": {
			Name: "alpha", ServiceType: db.ServiceTypeVM,
			VM: &db.VMConfig{Components: &db.VMComponentsConfig{GuestBase: db.VMGuestBaseConfig{ID: "existing"}}},
		},
		"native": {Name: "native", ServiceType: db.ServiceTypeSystemd},
	}}
	fixture.service.Name = "zeta"
	fixture.unitName = vmSystemdUnitName("zeta")
	fixture.unitExec = fixture.execStart()
	if err := fixture.store.Set(data); err != nil {
		t.Fatal(err)
	}
	result := fixture.inventory(t)
	if got := []string{result.VMs[0].Service, result.VMs[1].Service}; !slices.Equal(got, []string{"alpha", "zeta"}) {
		t.Fatalf("VM order = %v", got)
	}
	if !slices.Equal(result.Summary.AlreadyAdopted, []string{"alpha"}) || !slices.Equal(result.Summary.Adoptable, []string{"zeta"}) {
		t.Fatalf("summary = %#v", result.Summary)
	}
}

func TestVMRuntimeAdoptionInventoryAcceptsServiceNamedVMRun(t *testing.T) {
	fixture := newVMRuntimeAdoptionFixture(t, false)
	fixture.service.Name = "vm-run"
	fixture.unitName = vmSystemdUnitName(fixture.service.Name)
	fixture.unitExec = fixture.execStart()
	fixture.persist(t)

	vm := fixture.onlyVM(t)
	if vm.Classification != vmRuntimeAdoptionCustomLegacy || vm.BlockedReason != "" {
		t.Fatalf("vm-run service inventory = classification %q reason %q", vm.Classification, vm.BlockedReason)
	}
}

func TestParseVMRuntimeAdoptionUnitUsesEffectiveDropInExecStart(t *testing.T) {
	raw := []byte(`# /usr/lib/systemd/system/yeet-devbox.service
[Service]
ExecStart=/old/catch vm-run --service old
# /etc/systemd/system/yeet-devbox.service.d/runtime.conf
[Service]
ExecStart=
ExecStart="/new catch" vm-run --service devbox --service-root /srv/devbox \
 --disk-path /srv/devbox/disk --firecracker /opt/firecracker --jailer /opt/jailer --config-file /srv/devbox/run/firecracker.json --jailer-base /var/lib/yeet/vm-jailer --api-sock /run/a --console-sock /run/c
`)
	argv, paths, err := parseVMRuntimeAdoptionUnit(raw)
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "/new catch" || argv[1] != "vm-run" || !slices.Equal(paths, []string{"/usr/lib/systemd/system/yeet-devbox.service", "/etc/systemd/system/yeet-devbox.service.d/runtime.conf"}) {
		t.Fatalf("parsed unit = argv %#v paths %#v", argv, paths)
	}
	if _, _, err := parseVMRuntimeAdoptionUnit([]byte("[Service]\nExecStart=/one\nExecStart=/two\n")); err == nil {
		t.Fatal("accepted ambiguous effective ExecStart")
	}
	if _, _, err := parseVMRuntimeAdoptionUnit([]byte("# /etc/unit\n# /etc/unit\n[Service]\nExecStart=/one\n")); err == nil {
		t.Fatal("accepted duplicate fragment header")
	}
}

func TestReadVMRuntimeAdoptionUnitState(t *testing.T) {
	binDir := t.TempDir()
	systemctl := filepath.Join(binDir, "systemctl")
	script := `#!/bin/sh
case "$2" in
  --property=ActiveState) printf '%s\n' "${SYSTEMCTL_ACTIVE:-active}" ;;
  --property=MainPID) printf '%s\n' "${SYSTEMCTL_MAINPID:-42}" ;;
  --property=NeedDaemonReload) printf '%s\n' "${SYSTEMCTL_RELOAD:-no}" ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(systemctl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	active, pid, reload, err := readVMRuntimeAdoptionUnitState(context.Background(), "yeet-vm-devbox.service")
	if err != nil || active != "active" || pid != 42 || reload != "no" {
		t.Fatalf("unit state = %q %d %q, %v", active, pid, reload, err)
	}
	t.Setenv("SYSTEMCTL_MAINPID", "invalid")
	if _, _, _, err := readVMRuntimeAdoptionUnitState(context.Background(), "yeet-vm-devbox.service"); err == nil {
		t.Fatal("invalid MainPID accepted")
	}
	t.Setenv("SYSTEMCTL_MAINPID", "42")
	t.Setenv("SYSTEMCTL_RELOAD", "maybe")
	if _, _, _, err := readVMRuntimeAdoptionUnitState(context.Background(), "yeet-vm-devbox.service"); err == nil {
		t.Fatal("invalid NeedDaemonReload accepted")
	}
}

func TestVMRuntimeAdoptionUnitLoadingRejectsInvalidInputs(t *testing.T) {
	if _, err := loadEffectiveVMRuntimeAdoptionUnit(context.Background(), "../devbox.service"); err == nil {
		t.Fatal("invalid unit name accepted")
	}
	if _, _, err := readVMRuntimeAdoptionUnitFragments(nil); err == nil {
		t.Fatal("empty unit fragment set accepted")
	}
}

func TestParseVMRuntimeAdoptionExecStartRejectsUnresolvedExpansion(t *testing.T) {
	for _, value := range []string{"/bin/catch $RUNTIME", "/bin/catch %i", `"unterminated`} {
		if _, err := parseVMRuntimeAdoptionExecStart(value); err == nil {
			t.Fatalf("accepted ambiguous ExecStart %q", value)
		}
	}
	argv, err := parseVMRuntimeAdoptionExecStart(`/bin/catch "space\svalue" "100%%" "$$HOME"`)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(argv, []string{"/bin/catch", "space value", "100%", "$HOME"}) {
		t.Fatalf("argv = %#v", argv)
	}
}

func TestParseVMRuntimeAdoptionExecStartRoundTripsRendererEscapes(t *testing.T) {
	for name, control := range map[string]byte{
		"bell": '\a', "backspace": '\b', "form-feed": '\f', "vertical-tab": '\v',
	} {
		t.Run(name, func(t *testing.T) {
			value := "/srv/custom" + string(control) + "root"
			raw := "/bin/catch vm-run --service-root " + systemdVMExecArgument(value)
			argv, err := parseVMRuntimeAdoptionExecStart(raw)
			if err != nil {
				t.Fatalf("parse renderer output %q: %v", raw, err)
			}
			if !slices.Equal(argv, []string{"/bin/catch", "vm-run", "--service-root", value}) {
				t.Fatalf("renderer round-trip argv = %#v", argv)
			}
		})
	}
}

func TestDecodeStrictVMRuntimeAdoptionJSONRejectsDuplicateFields(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		target func() any
	}{
		{name: "receipt", raw: `{"schema":"first","schema":"second"}`, target: func() any { return &vmRuntimeAdoptionReceipt{} }},
		{name: "case-insensitive receipt", raw: `{"schema":"first","Schema":"second"}`, target: func() any { return &vmRuntimeAdoptionReceipt{} }},
		{name: "local ref", raw: `{"name":"first","name":"second"}`, target: func() any { return &localVMImageRef{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := decodeStrictVMRuntimeAdoptionJSON([]byte(test.raw), test.target())
			if err == nil || !strings.Contains(err.Error(), "duplicate field") {
				t.Fatalf("duplicate JSON error = %v", err)
			}
		})
	}
}

func TestVMRuntimeAdoptionValidationErrorsAreDeterministic(t *testing.T) {
	flags := map[string]string{
		"--service": "wrong", "--service-root": "wrong", "--config-file": "wrong", "--disk-path": "wrong",
	}
	for range 100 {
		err := validateVMRuntimeAdoptionRequiredFlags(flags, "devbox", "/srv/devbox", "/srv/devbox/run/firecracker.json", "/srv/devbox/data/rootfs.raw")
		if err == nil || !strings.Contains(err.Error(), "loaded VM unit --service") {
			t.Fatalf("required-flag validation error = %v", err)
		}
	}

	receipt := vmRuntimeAdoptionReceipt{}
	for range 100 {
		err := validateVMRuntimeAdoptionReceiptDigests(receipt, vmRuntimeAdoptionManifestState{}, vmRuntimeAdoptionFileEvidence{})
		if err == nil || !strings.Contains(err.Error(), "receipt catalog digest") {
			t.Fatalf("receipt validation error = %v", err)
		}
	}
}

func TestValidateVMRuntimeAdoptionLoadedCommandAcceptsTrustedGlobalRoots(t *testing.T) {
	runner, flags, err := validateVMRuntimeAdoptionLoadedCommand([]string{
		"/run/catch", "-data-dir", "/srv/yeet data", "-services-root=/srv/services", "vm-run",
		"--service", "devbox", "--service-root", "/srv/services/devbox",
	})
	if err != nil {
		t.Fatal(err)
	}
	if runner != "/run/catch" || flags["-data-dir"] != "/srv/yeet data" || flags["-services-root"] != "/srv/services" || flags["--service"] != "devbox" {
		t.Fatalf("runner=%q flags=%v", runner, flags)
	}
}

func TestValidateVMRuntimeAdoptionLoadedCommandRejectsPartialOrUnexpectedGlobals(t *testing.T) {
	for _, argv := range [][]string{
		{"/run/catch", "-data-dir", "/srv/yeet", "vm-run", "--service", "devbox"},
		{"/run/catch", "-config", "/tmp/config", "vm-run", "--service", "devbox"},
	} {
		if _, _, err := validateVMRuntimeAdoptionLoadedCommand(argv); err == nil {
			t.Fatalf("accepted loaded command %v", argv)
		}
	}
}

func FuzzParseVMRuntimeAdoptionUnit(f *testing.F) {
	f.Add([]byte("# /etc/systemd/system/yeet-devbox.service\n[Service]\nExecStart=/bin/catch vm-run --service devbox\n"))
	f.Add([]byte("[Service]\nExecStart=\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _, _ = parseVMRuntimeAdoptionUnit(raw)
	})
}

func FuzzDecodeVMRuntimeAdoptionReceipt(f *testing.F) {
	receipt := vmRuntimeAdoptionReceipt{Schema: vmRuntimeAdoptionReceiptSchema, SchemaVersion: 1}
	raw, err := json.Marshal(receipt)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(raw)
	f.Add([]byte(`{"schema":"x","unknown":true}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		var decoded vmRuntimeAdoptionReceipt
		_ = decodeStrictVMRuntimeAdoptionJSON(raw, &decoded)
	})
}

type vmRuntimeAdoptionFixture struct {
	t            *testing.T
	dataRoot     string
	servicesRoot string
	serviceRoot  string
	imageDir     string
	rootFS       string
	kernel       string
	firecracker  string
	jailer       string
	disk         string
	unitPath     string
	unitName     string
	unitExec     []string
	service      *db.Service
	store        *db.Store
	cfg          Config
	deps         vmRuntimeAdoptionInventoryDeps
}

func newVMRuntimeAdoptionFixture(t *testing.T, withManifest bool) *vmRuntimeAdoptionFixture {
	t.Helper()
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dataRoot, err := os.MkdirTemp(workingDir, ".vm-runtime-adoption-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dataRoot); err != nil {
			t.Errorf("remove VM runtime adoption fixture: %v", err)
		}
	})
	servicesRoot := filepath.Join(dataRoot, "custom-services")
	serviceRoot := filepath.Join(dataRoot, "zfs-mounts", "devbox")
	imageDir := filepath.Join(dataRoot, "images", "ubuntu-26.04-amd64-v29")
	f := &vmRuntimeAdoptionFixture{
		t: t, dataRoot: dataRoot, servicesRoot: servicesRoot, serviceRoot: serviceRoot, imageDir: imageDir,
		rootFS: filepath.Join(imageDir, "rootfs.ext4"), kernel: filepath.Join(imageDir, "vmlinux"),
		firecracker: filepath.Join(imageDir, "firecracker"), jailer: filepath.Join(imageDir, "jailer"),
		disk:     filepath.Join(serviceRoot, "data", "rootfs.raw"),
		unitPath: filepath.Join(dataRoot, "systemd", vmSystemdUnitName("devbox")), unitName: vmSystemdUnitName("devbox"),
	}
	writeVMRuntimeAdoptionTestFile(t, f.rootFS, "immutable-prepared-rootfs", 0o644)
	writeVMRuntimeAdoptionTestFile(t, f.kernel, "kernel", 0o644)
	writeVMRuntimeAdoptionTestFile(t, filepath.Join(imageDir, "kernel.config"), "config", 0o644)
	writeVMRuntimeAdoptionTestFile(t, f.firecracker, "firecracker", 0o755)
	writeVMRuntimeAdoptionTestFile(t, f.jailer, "jailer", 0o755)
	writeVMRuntimeAdoptionTestFile(t, f.disk, "mutable-active-disk-must-not-be-hashed", 0o600)
	writeVMRuntimeAdoptionTestFile(t, f.unitPath, "[Service]\n", 0o644)
	f.service = &db.Service{
		Name: "devbox", ServiceType: db.ServiceTypeVM, ServiceRoot: serviceRoot,
		VM: &db.VMConfig{
			Runtime: vmRuntimeFirecracker,
			Image:   db.VMImageConfig{Payload: testUbuntuVMPayload, Version: "ubuntu-26.04-amd64-v29", Distro: "ubuntu", RootFS: f.rootFS, Kernel: f.kernel},
			Disk:    db.VMDiskConfig{Backend: vmDiskBackendRaw, Bytes: 8 << 30, Path: f.disk}, SetupState: "ready",
		},
	}
	f.writeFirecrackerConfig(t)
	f.unitExec = f.execStart()
	if withManifest {
		manifest := f.newManifest(t)
		writeVMRuntimeAdoptionTestJSON(t, filepath.Join(imageDir, "manifest.json"), manifest, 0o644)
	}
	f.store = db.NewStore(filepath.Join(dataRoot, "db.json"), servicesRoot)
	f.cfg = Config{DB: f.store, RootDir: dataRoot, ServicesRoot: servicesRoot}
	f.deps = defaultVMRuntimeAdoptionInventoryDeps()
	f.deps.architecture = "amd64"
	f.deps.evidence.trustedUID = uint32(os.Geteuid())
	f.deps.loadUnit = func(_ context.Context, unit string) (vmRuntimeAdoptionLoadedUnit, error) {
		if unit != f.unitName {
			return vmRuntimeAdoptionLoadedUnit{}, fmt.Errorf("unexpected unit %s", unit)
		}
		evidence, err := collectTrustedVMRuntimeAdoptionFileEvidence(f.unitPath, true, f.deps.evidence)
		if err != nil {
			return vmRuntimeAdoptionLoadedUnit{}, err
		}
		return vmRuntimeAdoptionLoadedUnit{
			Name: unit, ExecStart: append([]string(nil), f.unitExec...), ActiveState: "active", MainPID: 4242,
			NeedDaemonReload: "no",
			Fragments:        []vmRuntimeAdoptionUnitFragment{{Path: f.unitPath, Evidence: evidence}},
		}, nil
	}
	f.deps.runtimePair = func(context.Context, string, string) (string, error) { return "1.16.1", nil }
	f.persist(t)
	return f
}

func (f *vmRuntimeAdoptionFixture) persist(t *testing.T) {
	t.Helper()
	if err := f.store.Set(&db.Data{Services: map[string]*db.Service{f.service.Name: f.service}}); err != nil {
		t.Fatal(err)
	}
}

func (f *vmRuntimeAdoptionFixture) inventory(t *testing.T) vmRuntimeAdoptionInventory {
	t.Helper()
	result, err := inventoryVMRuntimeAdoptionFleet(context.Background(), &f.cfg, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (f *vmRuntimeAdoptionFixture) onlyVM(t *testing.T) vmRuntimeAdoptionPreparation {
	t.Helper()
	result := f.inventory(t)
	if len(result.VMs) != 1 {
		t.Fatalf("VM count = %d, want 1", len(result.VMs))
	}
	return result.VMs[0]
}

func (f *vmRuntimeAdoptionFixture) writeFirecrackerConfig(t *testing.T) {
	t.Helper()
	config := firecrackerConfig{
		BootSource:    firecrackerBootSource{KernelImagePath: f.kernel, BootArgs: "console=ttyS0"},
		Drives:        []firecrackerDrive{{DriveID: "rootfs", PathOnHost: f.disk, IsRootDevice: true}},
		MachineConfig: firecrackerMachineConfig{VCPUCount: 2, MemSizeMib: 2048},
	}
	writeVMRuntimeAdoptionTestJSON(t, filepath.Join(serviceRunDirForRoot(f.serviceRoot), "firecracker.json"), config, 0o644)
}

func (f *vmRuntimeAdoptionFixture) execStart() []string {
	return []string{
		filepath.Join(f.dataRoot, "run", "catch"), "vm-run", "--service", f.service.Name,
		"--service-root", f.serviceRoot, "--disk-path", f.disk,
		"--firecracker", f.firecracker, "--jailer", f.jailer,
		"--jailer-base", vmJailerBaseForDataRoot(f.dataRoot),
		"--api-sock", filepath.Join(serviceRunDirForRoot(f.serviceRoot), "firecracker.sock"),
		"--config-file", filepath.Join(serviceRunDirForRoot(f.serviceRoot), "firecracker.json"),
		"--console-sock", filepath.Join(serviceRunDirForRoot(f.serviceRoot), "serial.sock"),
	}
}

func (f *vmRuntimeAdoptionFixture) newManifest(t *testing.T) vmImageManifest {
	t.Helper()
	return vmImageManifest{
		Name: "ubuntu", Version: f.service.VM.Image.Version, Architecture: "amd64", Distro: "ubuntu",
		Kernel: filepath.Base(f.kernel), RootFS: filepath.Base(f.rootFS), Firecracker: filepath.Base(f.firecracker), Jailer: filepath.Base(f.jailer),
		RootFSSize: 8 << 30, KernelVersion: "linux-7.1.1-yeet",
		Checksums: map[string]string{
			filepath.Base(f.kernel):      sha256FileForVMRuntimeAdoptionTest(t, f.kernel),
			filepath.Base(f.rootFS):      sha256FileForVMRuntimeAdoptionTest(t, f.rootFS),
			filepath.Base(f.firecracker): sha256FileForVMRuntimeAdoptionTest(t, f.firecracker),
			filepath.Base(f.jailer):      sha256FileForVMRuntimeAdoptionTest(t, f.jailer),
		},
	}
}

func (f *vmRuntimeAdoptionFixture) manifest(t *testing.T) vmImageManifest {
	t.Helper()
	raw := readVMRuntimeAdoptionTestFile(t, filepath.Join(f.imageDir, "manifest.json"))
	var manifest vmImageManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func writeVMRuntimeAdoptionTestFile(t testing.TB, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func writeVMRuntimeAdoptionTestJSON(t testing.TB, path string, value any, mode os.FileMode) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeVMRuntimeAdoptionTestFile(t, path, string(raw), mode)
}

func readVMRuntimeAdoptionTestFile(t testing.TB, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func sha256FileForVMRuntimeAdoptionTest(t testing.TB, path string) string {
	t.Helper()
	raw := readVMRuntimeAdoptionTestFile(t, path)
	return vmLegacySHA256Bytes(raw)
}

func fileSizesForVMRuntimeAdoptionTest(t testing.TB, paths ...string) int64 {
	t.Helper()
	var total int64
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		total += info.Size()
	}
	return total
}
