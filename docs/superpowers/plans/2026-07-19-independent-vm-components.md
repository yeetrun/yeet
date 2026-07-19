# Independent VM Components Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish and consume guest bases, guest kernels, and host Firecracker+jailer runtimes as independent immutable artifacts while preserving every existing monolithic VM and old Catch client.

**Architecture:** `yeet-vm-images` gains additive guest-base and kernel catalogs beside the runtime catalog. New Catch versions resolve a three-component composition lock at provisioning time, cache each artifact under its own root, and persist exact manifest digests. The legacy schema-1 image catalog and immutable monolithic releases remain intact for old Catch and existing VMs; component-only guest releases start only after Catch dual-read and adoption support has shipped.

**Tech Stack:** Go, JSON-backed Catch database, Bash, jq, GitHub Actions/Releases, Nix/NixOS, Ubuntu image tooling, systemd, ZFS.

## Global Constraints

- Depends on the disk-only recovery, runtime-publishing, and Catch runtime-management plans.
- Guest bases contain the root filesystem and guest provenance only. They do not contain a host Firecracker binary, jailer, or bootable host kernel.
- Kernel releases contain the bootable host-side `vmlinux`, kernel config, manifest, and guest package metadata needed by the existing kernel-selector flow. They do not contain a root filesystem or VMM.
- Firecracker+jailer releases are owned exclusively by the runtime catalog and runtime-publishing plan.
- The composition lock records exact guest-base, kernel, and runtime manifest digests. A mutable alias or channel is never a launch input.
- Existing VMs keep their current disk and immutable monolithic bundle paths. Adoption records equivalent component identities without replacing the disk or restarting a running VM.
- The legacy root `catalog.json` remains schema 1 with its existing `images` array. New component-catalog references are additive fields so old decoders keep working.
- Existing immutable monolithic release tags/assets and legacy `latest` resolution remain available. Do not repoint an old version to component-only content.
- `yeet vm images update` refreshes guest image/catalog cache state only. It does not select or stage a host runtime for a VM and never restarts a VM.
- Guest kernel requests remain untrusted input: the host accepts only a canonical kernel ID found in the verified kernel catalog, then copies the exact verified artifact through the existing kernel-sync transaction.
- Support default/custom Catch data roots, default/custom/ZFS service roots, raw disks, and ZFS zvol disks.

---

### Task 1: Define additive guest-base and kernel catalog contracts

**Files:**
- Create in `yeet-vm-images`: `schemas/guest-base-manifest.schema.json`
- Create in `yeet-vm-images`: `schemas/guest-catalog.schema.json`
- Create in `yeet-vm-images`: `schemas/kernel-manifest.schema.json`
- Create in `yeet-vm-images`: `schemas/kernel-catalog.schema.json`
- Create in `yeet-vm-images`: `guest-catalog.json`
- Create in `yeet-vm-images`: `kernel-catalog.json`
- Modify in `yeet-vm-images`: `catalog.json`
- Create in `yeet-vm-images`: `scripts/verify-component-catalogs.sh`
- Create in `yeet-vm-images`: `scripts/testdata/guest-manifest-valid.json`
- Create in `yeet-vm-images`: `scripts/testdata/kernel-manifest-valid.json`
- Create in `yeet-vm-images`: `scripts/test-component-catalogs.sh`

**Interfaces:**
- Produces: signed-by-review catalog roots and immutable digest-addressed manifest references.
- Consumed by: Tasks 2-5 and Catch component resolution in Tasks 6-9.

- [ ] **Step 1: Write catalog contract tests**

Test that:

- the existing `catalog.json | .schema_version` remains `1` and `.images` remains an array;
- every component catalog reference is an HTTPS URL under the expected repository;
- IDs, architecture, release URLs, manifest SHA-256 values, and payload SHA-256 values are present and strictly formatted;
- catalog entries are unique and sorted deterministically;
- guest manifests cannot contain `firecracker`, `jailer`, or `vmlinux` payload roles;
- kernel manifests contain exactly one `vmlinux`, one config, and kernel package/selector metadata;
- every channel pointer resolves to an immutable catalog entry rather than carrying its own artifact URL.

The additive root contract is:

```json
{
  "schema_version": 1,
  "images": [],
  "component_catalogs": {
    "guest_bases": "https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/guest-catalog.json",
    "kernels": "https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/kernel-catalog.json",
    "runtimes": "https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/runtime-catalog.json"
  }
}
```

The `images` array above means “preserve all existing entries,” not replace it with an empty array.

- [ ] **Step 2: Run the contract tests and verify failure**

```bash
scripts/test-component-catalogs.sh
```

Expected: FAIL because the component schemas, catalogs, and verifier do not exist.

- [ ] **Step 3: Add strict manifest schemas**

Use this guest manifest shape:

```json
{
  "schema_version": 1,
  "guest_base_id": "guest-ubuntu-26.04-amd64-v1",
  "os": "ubuntu",
  "os_version": "26.04",
  "architecture": "amd64",
  "rootfs": {
    "url": "https://github.com/yeetrun/yeet-vm-images/releases/download/guest-ubuntu-26.04-amd64-v1/rootfs.ext4.zst",
    "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "uncompressed_bytes": 4294967296
  },
  "default_kernel_channel": "stable",
  "provenance": {
    "source_commit": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "workflow_run_url": "https://github.com/yeetrun/yeet-vm-images/actions/runs/123456789"
  }
}
```

Use this kernel manifest shape:

```json
{
  "schema_version": 1,
  "kernel_id": "kernel-linux-7.1.1-yeet-v1",
  "upstream_version": "7.1.1",
  "packaging_revision": 1,
  "architecture": "amd64",
  "vmlinux": {
    "url": "https://github.com/yeetrun/yeet-vm-images/releases/download/kernel-linux-7.1.1-yeet-v1/vmlinux",
    "sha256": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
  },
  "config": {
    "url": "https://github.com/yeetrun/yeet-vm-images/releases/download/kernel-linux-7.1.1-yeet-v1/kernel.config",
    "sha256": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
  },
  "guest_packages": {
    "catalog_url": "https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/kernel-packages/catalog.json",
    "selector_schema_version": 2,
    "release_id": "kernel-linux-7.1.1-yeet-v1"
  },
  "provenance": {
    "source_commit": "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
    "workflow_run_url": "https://github.com/yeetrun/yeet-vm-images/actions/runs/123456790"
  }
}
```

Reject unknown top-level and payload properties, unsafe URLs, non-canonical IDs, unsupported architectures, non-positive sizes/revisions, and malformed hashes.

- [ ] **Step 4: Seed empty component catalogs and additive root references**

`guest-catalog.json` starts with `schema_version`, an empty `guest_bases` array, and explicit per-OS `channels`. `kernel-catalog.json` starts with `schema_version`, an empty `kernels` array, and per-architecture `channels`. Add `component_catalogs` to the root catalog without modifying any existing `images` entry or alias.

- [ ] **Step 5: Run tests and commit**

```bash
scripts/test-component-catalogs.sh
jq -e '.schema_version == 1 and (.images | type == "array")' catalog.json
git diff --check
git add schemas guest-catalog.json kernel-catalog.json catalog.json scripts/verify-component-catalogs.sh scripts/testdata scripts/test-component-catalogs.sh
git commit -m "catalog: define independent VM component contracts"
```

### Task 2: Publish kernels without rebuilding guest bases

**Files:**
- Modify in `yeet-vm-images`: `.github/workflows/sync-latest-stable-kernel.yml`
- Modify in `yeet-vm-images`: `.github/workflows/build-kernel.yml`
- Modify in `yeet-vm-images`: `.github/workflows/publish-kernel-packages.yml`
- Modify in `yeet-vm-images`: `scripts/resolve-latest-kernel.sh`
- Modify in `yeet-vm-images`: `scripts/resolve-kernel-release.sh`
- Modify in `yeet-vm-images`: `scripts/download-kernel-release.sh`
- Modify in `yeet-vm-images`: `scripts/build-kernel-deb.sh`
- Modify in `yeet-vm-images`: `kernel-packages/yeet-kernel-package.nix`
- Create in `yeet-vm-images`: `scripts/render-kernel-manifest.sh`
- Create in `yeet-vm-images`: `scripts/update-kernel-catalog.sh`
- Create in `yeet-vm-images`: `scripts/test-kernel-component-release.sh`
- Modify in `yeet-vm-images`: `scripts/test-latest-kernel-automation.sh`
- Modify in `yeet-vm-images`: `scripts/test-kernel-release-workflows.sh`
- Modify in `yeet-vm-images`: `scripts/test-kernel-packages.sh`
- Modify in `yeet-vm-images`: `scripts/test-component-catalogs.sh`

**Interfaces:**
- Produces: immutable kernel release, guest packages, selector metadata, and a catalog PR.
- Consumed by: NixOS/Ubuntu guest builds and Catch kernel resolution.

- [ ] **Step 1: Add failing release-render and no-image-rebuild tests**

Add fixtures for upstream version `7.1.1` and packaging revision `1`. Assert that the renderer preserves the existing canonical release ID `kernel-linux-7.1.1-yeet-v1`, emits exact artifact digests and provenance, and declares selector schema 2. Assert generated Debian and Nix selectors carry `release_id` plus `manifest_sha256` while retaining the existing `version`, guest paths, and payload hashes. Assert the daily sync workflow invokes the kernel build/package workflows but contains no Ubuntu/NixOS guest build dispatch.

- [ ] **Step 2: Run tests and verify failure**

```bash
scripts/test-kernel-component-release.sh
scripts/test-component-catalogs.sh
scripts/test-kernel-packages.sh
scripts/test-latest-kernel-automation.sh
scripts/test-kernel-release-workflows.sh
```

Expected: FAIL because the renderer/catalog updater are absent and the current stable-kernel sync rebuilds full images.

- [ ] **Step 3: Make the kernel build emit one canonical artifact set**

After the existing source resolution and build succeed:

1. hash `vmlinux` and `kernel.config`;
2. render and validate `kernel-manifest.json` from those measured values;
3. hash the final manifest, then build Debian/Nix guest selector packages that embed its immutable `release_id` and `manifest_sha256`;
4. publish the kernel assets under the existing immutable tag format `kernel-linux-<upstream>-yeet-v<revision>` and publish the corresponding guest packages/metadata; architecture remains a required manifest field;
5. re-download and verify kernel plus selector package metadata, and reject an existing tag whose manifest digest differs.

Do not derive release values from mutable `latest` URLs.

- [ ] **Step 4: Change stable discovery to open a catalog promotion PR**

The daily workflow must:

1. resolve the latest stable upstream kernel using the existing trusted source;
2. no-op if that upstream version and packaging revision already exist;
3. build and publish the canonical kernel plus guest packages;
4. run manifest/catalog verification against downloaded release assets;
5. open a PR that adds the immutable entry and moves only the `stable` channel pointer;
6. leave guest-base catalogs and legacy image aliases untouched.

- [ ] **Step 5: Run tests and commit**

```bash
scripts/test-kernel-component-release.sh
scripts/test-component-catalogs.sh
scripts/test-kernel-packages.sh
scripts/test-latest-kernel-automation.sh
scripts/test-kernel-release-workflows.sh
actionlint .github/workflows/sync-latest-stable-kernel.yml .github/workflows/build-kernel.yml .github/workflows/publish-kernel-packages.yml
git diff --check
git add .github/workflows scripts tests
git commit -m "kernel: publish independently from guest images"
```

### Task 3: Build component-only Ubuntu guest bases behind a release gate

**Files:**
- Modify in `yeet-vm-images`: `.github/workflows/build-ubuntu-26.04.yml`
- Modify in `yeet-vm-images`: `scripts/build-ubuntu-26.04.sh`
- Create in `yeet-vm-images`: `scripts/render-guest-manifest.sh`
- Create in `yeet-vm-images`: `scripts/update-guest-catalog.sh`
- Create in `yeet-vm-images`: `scripts/test-ubuntu-guest-base.sh`
- Modify in `yeet-vm-images`: `scripts/test-component-catalogs.sh`
- Modify in `yeet-vm-images`: `README.md`

- [ ] **Step 1: Add failing component-boundary tests**

Build or inspect the release staging directory and assert it contains exactly `rootfs.ext4.zst`, `guest-manifest.json`, checksums, and provenance. Fail if it contains `firecracker`, `jailer`, `vmlinux`, or `kernel.config`. Assert the workflow has no Firecracker-version input and consumes only a kernel selector/channel for guest package configuration, not for embedding a boot artifact.

- [ ] **Step 2: Run tests and verify failure**

```bash
scripts/test-ubuntu-guest-base.sh
scripts/test-component-catalogs.sh
```

Expected: FAIL because the Ubuntu release still assembles a monolithic bundle.

- [ ] **Step 3: Split the Ubuntu builder at the artifact boundary**

Keep rootfs creation, guest agent, package initialization, kernel-selector journal support, filesystem sizing, and compression. Remove host Firecracker download, jailer handling, boot-kernel copy, and their release checksums from the component-only path. Render the guest manifest from the measured compressed rootfs and build provenance.

Use immutable tags `guest-ubuntu-26.04-amd64-v<revision>`. A packaging-only rootfs change increments the guest revision without rebuilding or promoting a runtime or kernel.

Replace the old monolithic dispatch inputs with `guest_base_id`, `yeet_ref`, `kernel_release`, `kernel_manifest_sha256`, and `zstd_level`. Remove `firecracker_version`, kernel source/build inputs, `overwrite_release`, `publish_latest_alias`, and `latest_alias`; component releases are immutable and channel movement belongs to `guest-catalog.json` review.

- [ ] **Step 4: Prepare gated candidate publication without dispatching it**

The manually dispatched workflow is capable of publishing immutable assets, verifying a clean re-download, and opening a catalog PR. The PR may add/move `guest-catalog.json` Ubuntu `candidate`; moving `stable` is a separate reviewed change after provisioning tests. Remove the component builder from automatic kernel-sync dispatches and do not dispatch it in this task. The first component-only release is blocked until Catch Tasks 5-9 have shipped. Do not mutate any legacy monolithic `catalog.json.images` version or alias.

- [ ] **Step 5: Run tests and commit**

```bash
scripts/test-ubuntu-guest-base.sh
scripts/test-component-catalogs.sh
actionlint .github/workflows/build-ubuntu-26.04.yml
git diff --check
git add .github/workflows/build-ubuntu-26.04.yml scripts tests README.md
git commit -m "images: publish Ubuntu guest bases independently"
```

### Task 4: Build component-only NixOS guest bases behind the same release gate

**Files:**
- Modify in `yeet-vm-images`: `.github/workflows/build-nixos-26.05.yml`
- Modify in `yeet-vm-images`: `scripts/build-nixos-26.05.sh`
- Modify in `yeet-vm-images`: `nixos/system.nix`
- Modify in `yeet-vm-images`: `nixos/yeet/vm.nix`
- Create in `yeet-vm-images`: `scripts/test-nixos-guest-base.sh`
- Modify in `yeet-vm-images`: `scripts/test-component-catalogs.sh`
- Modify in `yeet-vm-images`: `README.md`

- [ ] **Step 1: Add failing NixOS boundary tests**

Assert the release contains a rootfs manifest and no host VMM or boot kernel. Assert the guest still includes the kernel selector metadata/package needed to request a canonical catalog kernel, and that the Nix build does not interpret the selector as authority to fetch or execute a host artifact.

- [ ] **Step 2: Run tests and verify failure**

```bash
scripts/test-nixos-guest-base.sh
scripts/test-component-catalogs.sh
```

Expected: FAIL while NixOS still emits a monolithic image bundle.

- [ ] **Step 3: Split the NixOS builder**

Keep the guest filesystem, agent, kernel-selector journal, NixOS provenance, and package metadata. Remove Firecracker/jailer and `vmlinux` from the release payload. The Nix evaluation may consume the canonical kernel package metadata to configure the guest selector, but the produced guest manifest records only `default_kernel_channel` and does not embed a host artifact URL.

Prepare manual publication under `guest-nixos-26.05-amd64-v<revision>` using the same manifest renderer and candidate/stable review boundary as Ubuntu. Its dispatch inputs match Ubuntu plus `yeet_vm_images_ref` for the shipped flake lock. Remove Firecracker/kernel-build/overwrite/latest-alias inputs. Do not dispatch it until Catch Tasks 5-9 have shipped.

- [ ] **Step 4: Run tests and commit**

```bash
scripts/test-nixos-guest-base.sh
scripts/test-component-catalogs.sh
actionlint .github/workflows/build-nixos-26.05.yml
git diff --check
git add .github/workflows/build-nixos-26.05.yml scripts/build-nixos-26.05.sh nixos/system.nix nixos/yeet/vm.nix scripts/test-nixos-guest-base.sh scripts/test-component-catalogs.sh README.md
git commit -m "images: publish NixOS guest bases independently"
```

### Task 5: Add Catch dual-read component catalogs and independent caches

**Files:**
- Modify in `yeet`: `pkg/catch/vm_image_catalog.go`
- Modify in `yeet`: `pkg/catch/vm_image_catalog_test.go`
- Create in `yeet`: `pkg/catch/vm_component_catalog.go`
- Create in `yeet`: `pkg/catch/vm_component_catalog_test.go`
- Create in `yeet`: `pkg/catch/vm_guest_base.go`
- Create in `yeet`: `pkg/catch/vm_guest_base_test.go`
- Create in `yeet`: `pkg/catch/vm_kernel_artifact.go`
- Create in `yeet`: `pkg/catch/vm_kernel_artifact_test.go`
- Modify in `yeet`: `pkg/catch/catch.go`

**Interfaces:**
- Consumes: component catalog URLs and exact manifest refs from Tasks 1-4.
- Produces: verified `vmGuestBaseArtifact` and `vmKernelArtifact` for provisioning and kernel sync.

- [ ] **Step 1: Add legacy/additive parsing and cache tests**

Cover:

- a historical schema-1 root catalog with no `component_catalogs` still parses and provisions through the monolithic path;
- the additive root resolves guest/kernel/runtime catalogs from HTTPS only;
- manifests and payloads are downloaded to temporary siblings, hashed, schema-checked, fsynced, and renamed atomically;
- corrupt or conflicting cache content is quarantined rather than reused;
- cache paths derive from the configured Catch data root;
- aliases/channels resolve once to immutable IDs plus manifest digests;
- an unknown component field fails closed in component manifests while old root-catalog readers ignore the additive root field.

- [ ] **Step 2: Run tests and verify failure**

```bash
mise exec -- go test ./pkg/catch -run 'TestVM(ComponentCatalog|GuestBase|KernelArtifact|ImageCatalogLegacy)' -count=1
```

Expected: FAIL because component parsing/cache types do not exist.

- [ ] **Step 3: Extend the root catalog without changing legacy image parsing**

Add only this optional field to `vmImageCatalog`:

```go
type vmImageComponentCatalogs struct {
	GuestBases string `json:"guest_bases"`
	Kernels    string `json:"kernels"`
	Runtimes   string `json:"runtimes"`
}

type vmImageCatalog struct {
	SchemaVersion     int                       `json:"schema_version"`
	Images            []vmImageCatalogEntry     `json:"images"`
	ComponentCatalogs *vmImageComponentCatalogs `json:"component_catalogs,omitempty"`
}
```

Do not require component catalogs for legacy operations and do not reinterpret a monolithic image version as a component release.

- [ ] **Step 4: Add immutable component cache objects**

Use:

```text
<catch-data-root>/vm-guest-bases/<architecture>/<guest-base-id>/<manifest-sha256>/
<catch-data-root>/vm-kernels/<architecture>/<kernel-id>/<manifest-sha256>/
```

Apply the same path-containment, owner/mode, digest, atomic publication, and concurrent-download locking rules as the runtime cache. Guest-base extraction may write a provisioning temporary file but must never mutate a published cache directory.

- [ ] **Step 5: Run tests and commit**

```bash
mise exec -- go test ./pkg/catch -run 'TestVM(ComponentCatalog|GuestBase|KernelArtifact|ImageCatalogLegacy)' -count=1
mise exec -- go test ./pkg/catch ./pkg/db -count=1
but commit independent-vm-components -m "catch: add independent VM component catalogs"
```

### Task 6: Resolve and persist a three-component composition for new VMs

**Files:**
- Modify in `yeet`: `pkg/catch/vm_provision.go`
- Modify in `yeet`: `pkg/catch/vm_provision_test.go`
- Modify in `yeet`: `pkg/catch/vm_image.go`
- Modify in `yeet`: `pkg/catch/vm_image_test.go`
- Modify in `yeet`: `pkg/catch/vm_systemd.go`
- Modify in `yeet`: `pkg/catch/vm_systemd_test.go`
- Modify in `yeet`: `pkg/catch/vm_runtime_descriptor.go`

**Interfaces:**
- Consumes: verified guest, kernel, and runtime cache entries.
- Persists: `VMConfig.Components` composition lock defined by the runtime-management plan.

- [ ] **Step 1: Add resolution/provisioning tests**

Test these cases:

1. component guest alias + default stable kernel + host runtime default resolves three exact manifest digests before disk creation;
2. an explicitly selected guest/kernel/runtime immutable ID resolves unchanged;
3. any failed component verification aborts before the service record or disk is published;
4. provisioning copies/decompresses the guest rootfs and copies the kernel into service data, but the runtime remains in its shared immutable cache and is referenced through `vmm-runtime.json`;
5. a legacy image version still provisions through its existing immutable monolithic bundle, derives an adopted component lock from that verified bundle, and publishes a descriptor unit in the same transaction;
6. persisted `VMConfig.Image` remains populated for legacy callers while `VMConfig.Components` is authoritative for both component-provisioned and newly adopted legacy VMs;
7. custom/ZFS service roots produce identical logical locks and valid derived paths.

- [ ] **Step 2: Run tests and verify failure**

```bash
mise exec -- go test ./pkg/catch -run 'TestProvisionVM(ComponentComposition|ComponentFailureAtomic|LegacyMonolithic)' -count=1
```

Expected: FAIL because provisioning resolves only a monolithic image.

- [ ] **Step 3: Add one immutable resolver result**

```go
type vmProvisionArtifacts struct {
	GuestBase db.VMGuestBaseConfig
	Kernel    db.VMKernelArtifactConfig
	Runtime   db.VMRuntimeArtifactConfig
	Legacy    *vmCachedImage
}
```

Resolve all aliases/channels to immutable manifest digests first, then download/verify all artifacts, then begin the existing service/disk transaction. Never write a mutable channel or catalog URL into `VMConfig.Components`.

- [ ] **Step 4: Publish DB, disk, kernel, descriptor, and unit atomically**

For component VMs:

1. create the rootfs/disk from the verified guest base;
2. copy the verified kernel through the existing atomic kernel-copy helper;
3. persist the component lock in the same provisioning transaction as the VM record;
4. render the root-owned runtime descriptor from the exact runtime lock;
5. render the stable jailer-only unit from the descriptor;
6. roll back all newly published service artifacts if any pre-start step fails.

For a newly provisioned legacy monolithic bundle, derive the same adopted
`VMConfig.Components` identity used by the existing-VM adoption transaction,
then publish its descriptor and descriptor unit atomically with the VM record.
Do not leave new legacy VMs dependent on a future install-time adoption pass.
Keep explicit-pair launch compatibility as a durable recovery path, not the
steady-state unit for a successfully adopted provision.

- [ ] **Step 5: Run tests and commit**

```bash
mise exec -- go test ./pkg/catch -run 'TestProvisionVM(ComponentComposition|ComponentFailureAtomic|LegacyMonolithic)' -count=1
mise exec -- go test ./pkg/catch ./pkg/db ./pkg/dnet -count=1
but commit independent-vm-components -m "vm: provision immutable component compositions"
```

### Task 7: Make kernel sync update the component lock atomically

**Files:**
- Modify in `yeet`: `pkg/catch/vm_kernel_sync.go`
- Modify in `yeet`: `pkg/catch/vm_kernel_sync_test.go`
- Modify in `yeet`: `pkg/catch/vm_kernel_selection.go`
- Modify in `yeet`: `pkg/catch/vm_kernel_selection_test.go`
- Modify in `yeet`: `pkg/catch/vm_runtime_reconcile.go`
- Modify in `yeet`: `cmd/catch/catch.go`

- [ ] **Step 1: Add untrusted-selector and lock-update tests**

Evolve `/etc/yeet-vm/kernel/selected.json` to schema 2 by adding canonical `release_id` and `manifest_sha256` while retaining `version`, guest-local kernel/config paths, and payload hashes. Verify that the host resolves the untrusted `release_id` plus manifest digest to exactly one verified kernel-catalog entry; it rejects guest URLs/channels and any mismatch among catalog, selector, or copied payload digests. A successful copy updates `Components.Kernel` and the compatibility `Image.Kernel` fields atomically before the reboot proceeds; a failed copy changes neither. Keep schema-1 selectors valid only for adopted legacy monolithic VMs and validate them through their pinned legacy manifest rather than a mutable catalog.

- [ ] **Step 2: Run tests and verify failure**

```bash
mise exec -- go test ./pkg/catch -run 'TestAutoSyncVMGuestKernel(ComponentLock|RejectsGuestAuthority|AtomicFailure)' -count=1
```

Expected: FAIL because kernel sync updates only monolithic image-derived state.

- [ ] **Step 3: Resolve guest selector through the verified host catalog**

Keep `AutoSyncVMGuestKernelOnReboot` as the sole reboot hook. Parse schema-2 selection as an untrusted canonical release ID/digest, resolve it from already verified host catalog metadata, copy the guest-local `vmlinux` and optional config with no-follow path handling, require their hashes to match both selector and canonical manifest, publish the service kernel atomically, then commit both compatibility and component fields. Do not add a guest-agent runtime-upgrade method and do not fetch a URL supplied by the guest.

- [ ] **Step 4: Reconcile old component VMs and run tests**

At Catch startup, if a component VM has a verified service kernel but lacks the corresponding derived compatibility field, repair only that derived field. Never infer a new kernel selection from file contents alone.

```bash
mise exec -- go test ./pkg/catch -run 'TestAutoSyncVMGuestKernel|TestReconcileVMComponentKernel' -count=1
mise exec -- go test ./cmd/catch ./pkg/catch -count=1
but commit independent-vm-components -m "vm: sync verified kernels into component locks"
```

### Task 8: Make image update, inspect, and prune component-aware

**Files:**
- Modify in `yeet`: `pkg/catch/vm_images_cmd.go`
- Modify in `yeet`: `pkg/catch/vm_images_cmd_test.go`
- Modify in `yeet`: `pkg/catch/vm_images_prune.go`
- Modify in `yeet`: `pkg/catch/vm_runtime_status.go`
- Modify in `yeet`: `pkg/catch/vm_runtime_status_test.go`
- Modify in `yeet`: `pkg/cli/cli.go`
- Modify in `yeet`: `pkg/cli/cli_test.go`

- [ ] **Step 1: Add update/status/prune tests**

Cover:

- `images update` refreshes the legacy root plus guest-base/kernel catalog caches and the guest `latest` display, but never invokes runtime staging, unit rewrite, service restart, or guest-disk replacement;
- status prints distinct guest-base, kernel, configured runtime, staged runtime, running runtime, previous runtime, policy, and channel fields;
- image prune protects guest/kernel manifests referenced by any VM lock, pending provisioning transaction, or retained legacy monolithic VM;
- runtime artifacts are excluded and remain owned by `vm runtime prune`;
- dry-run and real prune produce the same candidate set;
- component caches under custom data roots and service references under custom/ZFS roots are discovered without hard-coded paths.

- [ ] **Step 2: Run tests and verify failure**

```bash
mise exec -- go test ./pkg/catch ./pkg/cli -run 'TestVMImages(UpdateComponentsNoRuntimeMutation|PruneComponentReferences)|TestVMStatusComponents' -count=1
```

Expected: FAIL because update/status/prune understand only monolithic image bundles.

- [ ] **Step 3: Extend operations without changing their authority boundary**

`images update` may fetch and verify guest/kernel metadata and cache the artifacts required to provision the displayed guest `latest`, but it must not alter an existing VM's component lock. `images prune` independently marks guest and kernel cache objects, then sweeps only unreferenced immutable objects. Keep local monolithic imports usable; do not silently decompose or relabel a custom imported bundle.

- [ ] **Step 4: Run tests and commit**

```bash
mise exec -- go test ./pkg/catch ./pkg/cli -run 'TestVMImages|TestVMStatusComponents' -count=1
mise exec -- go test ./pkg/catch ./pkg/cli ./pkg/yeet -count=1
but commit independent-vm-components -m "images: manage independent guest and kernel caches"
```

### Task 9: Preserve monolithic compatibility and gate component-only promotion

**Files:**
- Modify in `yeet`: `pkg/catch/vm_runtime_adoption.go`
- Modify in `yeet`: `pkg/catch/vm_runtime_adoption_test.go`
- Modify in `yeet`: `pkg/catch/vm_runtime_reconcile.go`
- Modify in `yeet`: `pkg/catch/catch_test.go`
- Modify in `yeet` website submodule: `website/docs/payloads/vms.mdx`
- Modify in `yeet-vm-images`: `README.md`
- Create in `yeet-vm-images`: `docs/component-release-runbook.md`

- [ ] **Step 1: Add adoption and mixed-fleet tests**

Use fixtures representing old v11/v15 and current v29 monolithic bundles. The runtime-management Task 4 transaction already adopts a full guest/kernel/runtime composition; verify those exact legacy identity and provenance rules across the representative historical bundles, preserves the old image version/path as provenance, rewrites only derived descriptor/unit state, and does not restart a running VM or replace its disk. Verify a mixed host can boot an adopted monolithic VM and a component VM concurrently through matching jailers.

- [ ] **Step 2: Run tests and verify failure**

```bash
mise exec -- go test ./pkg/catch -run 'TestAdoptMonolithicVMComponents|TestMixedMonolithicAndComponentFleet' -count=1
```

Expected: FAIL because representative historical-bundle and mixed-fleet release-gate coverage do not exist.

- [ ] **Step 3: Validate and complete the existing full-composition adoption transaction**

Exercise the runtime-management startup journal's existing guest-base provenance, installed-kernel hash, and matching Firecracker+jailer adoption against all representative bundles. Add only compatibility handling revealed by those fixtures. If any artifact cannot be verified, leave the legacy VM on its existing immutable launch paths and report `adoption-blocked`; never guess a component identity or switch to a catalog `latest`.

- [ ] **Step 4: Document the release compatibility gate**

The runbook requires this order:

1. ship Catch dual-read, independent caches, adoption, and component provisioning;
2. validate adoption on representative older and current monolithic bundles;
3. publish kernel and guest candidate entries;
4. provision Ubuntu and NixOS candidates through an exact promoted runtime;
5. reboot after a verified kernel-selector update;
6. validate raw and ZFS disks plus default/custom roots;
7. promote guest/kernel stable pointers by reviewed PR;
8. retain legacy catalog entries, tags, and assets indefinitely until a separately approved deprecation policy exists.

- [ ] **Step 5: Run tests and commit**

In `yeet`:

```bash
mise exec -- go test ./pkg/catch -run 'TestAdoptMonolithicVMComponents|TestMixedMonolithicAndComponentFleet' -count=1
mise exec -- go test ./... -count=1
mise exec -- pre-commit run --all-files
git -C website diff --check
git diff --check
```

In `yeet-vm-images`:

```bash
scripts/test-component-catalogs.sh
git diff --check
git add README.md docs/component-release-runbook.md
git commit -m "docs: define component release compatibility gate"
```

Commit the website documentation inside the submodule and push that website commit only after explicit push authorization; the website repository requires its commit to be reachable before the root gitlink is committed. Then commit the root tests, docs pointer, and adoption changes:

```bash
git -C website add docs/payloads/vms.mdx
git -C website commit -m "docs: explain independent VM components"
git -C website push origin HEAD
but commit independent-vm-components -m "vm: preserve monolithic component compatibility"
```

### Task 10: Run live component validation and record promotion evidence

**Files:**
- Create in `yeet-vm-images`: `attestations/components/<guest-base-id>/<manifest-sha256>/validation.json`
- Create in `yeet-vm-images`: `attestations/components/<kernel-id>/<manifest-sha256>/validation.json`
- Modify in `yeet-vm-images`: `guest-catalog.json`
- Modify in `yeet-vm-images`: `kernel-catalog.json`

- [ ] **Step 1: Build and install disposable Catch and Yeet artifacts**

```bash
mise exec -- go build -o ./bin/catch ./cmd/catch
mise exec -- go build -o ./bin/yeet ./cmd/yeet
```

Expected: both binaries build from the exact reviewed commit.

- [ ] **Step 2: Pass the Catch-availability gate and publish component candidates**

First prove the canary host is running a Catch build with component catalog parsing, composition locks, and legacy adoption. Then, with the exact reviewed refs and promoted kernel digest supplied by the operator, dispatch the manual component workflows:

```bash
test -n "${YEET_COMPONENT_YEET_REF:?set YEET_COMPONENT_YEET_REF}"
test -n "${YEET_COMPONENT_IMAGES_REF:?set YEET_COMPONENT_IMAGES_REF}"
test -n "${YEET_COMPONENT_KERNEL_RELEASE:?set YEET_COMPONENT_KERNEL_RELEASE}"
test -n "${YEET_COMPONENT_KERNEL_MANIFEST_SHA256:?set YEET_COMPONENT_KERNEL_MANIFEST_SHA256}"

gh workflow run build-ubuntu-26.04.yml --ref "$YEET_COMPONENT_IMAGES_REF" \
  -f guest_base_id=guest-ubuntu-26.04-amd64-v1 \
  -f yeet_ref="$YEET_COMPONENT_YEET_REF" \
  -f kernel_release="$YEET_COMPONENT_KERNEL_RELEASE" \
  -f kernel_manifest_sha256="$YEET_COMPONENT_KERNEL_MANIFEST_SHA256"

gh workflow run build-nixos-26.05.yml --ref "$YEET_COMPONENT_IMAGES_REF" \
  -f guest_base_id=guest-nixos-26.05-amd64-v1 \
  -f yeet_ref="$YEET_COMPONENT_YEET_REF" \
  -f kernel_release="$YEET_COMPONENT_KERNEL_RELEASE" \
  -f kernel_manifest_sha256="$YEET_COMPONENT_KERNEL_MANIFEST_SHA256" \
  -f yeet_vm_images_ref="$YEET_COMPONENT_IMAGES_REF"
```

Wait for both workflows, verify their immutable release assets/manifests by re-download, and merge only the reviewed candidate catalog PRs. Do not move stable or any legacy alias.

- [ ] **Step 3: Validate new component VMs**

On an approved KVM canary host, using operator-supplied endpoint and credentials:

1. provision one Ubuntu and one NixOS VM from candidate guest/kernel/runtime manifest digests;
2. confirm status reports all three immutable IDs/digests;
3. prove Firecracker runs only as the host `yeet-vm` user beneath the matching jailer;
4. reboot both VMs and verify readiness;
5. request a newer kernel through the guest package/selector path, reboot, and prove only the kernel component changed;
6. run `images update` and prove no existing composition, PID, unit, or running VM changed;
7. exercise ZFS disk create/restore/clone on one VM and a normal restart on raw storage;
8. repeat provisioning with custom Catch data and service roots.

- [ ] **Step 4: Validate legacy coexistence**

Start representative old and current monolithic VMs, run adoption/reconciliation, and prove no active disk changed and no running PID restarted. Restart one only with explicit operator authorization and verify it still uses its exact matching jailer/runtime. Run dry-run prune and prove every active/staged/previous/component/legacy reference remains protected.

- [ ] **Step 5: Publish evidence and promote deliberately**

Write immutable validation records containing commit, manifest digests, host architecture/class, test names, start/end times, and results. Open reviewed PRs that move guest and kernel `stable` pointers only after every required result is green. Do not change runtime promotion or legacy monolithic aliases in these PRs.

- [ ] **Step 6: Run final repository checks and commit evidence**

In `yeet`:

```bash
mise exec -- go test ./... -count=1
mise exec -- pre-commit run --all-files
git diff --check
```

In `yeet-vm-images`:

```bash
scripts/test-component-catalogs.sh
scripts/test-kernel-component-release.sh
scripts/test-kernel-packages.sh
scripts/test-latest-kernel-automation.sh
scripts/test-kernel-release-workflows.sh
scripts/test-ubuntu-guest-base.sh
scripts/test-nixos-guest-base.sh
actionlint
./scripts/verify-component-catalogs.sh
git diff --check
git add attestations/components guest-catalog.json kernel-catalog.json
git commit -m "catalog: promote validated VM components"
```
