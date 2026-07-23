# Legacy VM Permission Preflight Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Catch upgrades repair v0.9-era managed VM image modes and prove the next jailer-backed start is valid before replacing a legacy VM unit.

**Architecture:** Extend the existing legacy jailer-upgrade preparation boundary, before unit staging, with two explicit operations: normalize verified artifacts only for manifest-backed Yeet-managed image bundles, then validate the complete next-start jailer transition with the `yeet-vm` identity. Run the same preparation for explicit-jailer VMs that remain unadopted after an earlier upgrade, but do not rewrite their units. Keep normalization descriptor-based and checksum-first; custom and manifest-less bundles remain read-only inputs and fail preflight without unit or VM changes.

**Tech Stack:** Go, Catch VM migration code, Linux file descriptors and `golang.org/x/sys/unix`, Go tests, systemd/KVM live validation, GitButler, GitHub Actions.

## Global Constraints

- Existing running VMs must not be stopped or restarted by Catch installation.
- VM disks, ZFS datasets, and guest filesystems must not be modified.
- Kernel and initrd become exactly `0644`; Firecracker and jailer become exactly `0755`.
- All checksum-bearing artifacts must be verified before the first permission mutation.
- Artifact opens and mutations must not follow symlinks and must verify that the directory entry still names the opened inode.
- Custom, local-imported, untrusted, and manifest-less bundles must not be normalized automatically.
- A failed next-start preflight must leave the installed unit and running VM unchanged.
- Explicit-jailer VMs that are still unadopted must be repaired and preflighted without staging their unit again.
- Keep Firecracker and jailer version matching and the non-root `yeet-vm` runtime checks strict.
- Do not publish private hostnames, VM names, filesystem paths, or infrastructure details in tracked files, commits, changelog text, or release notes.

---

### Task 1: Add regression tests for v0.9 artifact modes

**Files:**
- Create: `pkg/catch/vm_jailer_upgrade_permissions_test.go`
- Test: `pkg/catch/vm_jailer_upgrade_permissions_test.go`

**Interfaces:**
- Consumes: existing `vmImageManifest`, `vmImageTestManifest`, `vmImageTestContents`, `testSHA256Hex`, and `vmJailerUpgradeVM`.
- Produces: required behavior for `normalizeManagedVMJailerUpgradeArtifacts(vm vmJailerUpgradeVM) error`.

- [ ] **Step 1: Write a failing managed-bundle repair test**

Create a manifest-backed bundle whose kernel and initrd have the v0.9 `0600`
download mode, then require exact post-normalization modes:

```go
func TestNormalizeManagedVMJailerUpgradeArtifactsRepairsV09Modes(t *testing.T) {
	dir := t.TempDir()
	contents := vmImageTestContents()
	contents["jailer"] = []byte("jailer")
	manifest := vmImageTestManifest("ubuntu-test-v1", contents)
	manifest.Initrd = "initrd.img"
	manifest.Jailer = "jailer"
	manifest.Checksums[manifest.Initrd] = testSHA256Hex(contents[manifest.Initrd])
	manifest.Checksums[manifest.Jailer] = testSHA256Hex(contents[manifest.Jailer])
	if err := writeManifestFile(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	modes := map[string]os.FileMode{
		manifest.Kernel: 0o600, manifest.Initrd: 0o600,
		manifest.Firecracker: 0o755, manifest.Jailer: 0o755,
	}
	for name, mode := range modes {
		if err := os.WriteFile(filepath.Join(dir, name), contents[name], mode); err != nil {
			t.Fatal(err)
		}
	}
	vm := vmJailerUpgradeVM{
		Firecracker: filepath.Join(dir, manifest.Firecracker),
		Jailer: filepath.Join(dir, manifest.Jailer),
		Manifest: manifest,
		NormalizeManagedArtifacts: true,
	}
	if err := normalizeManagedVMJailerUpgradeArtifacts(vm); err != nil {
		t.Fatalf("normalizeManagedVMJailerUpgradeArtifacts: %v", err)
	}
	assertVMJailerUpgradeMode(t, filepath.Join(dir, manifest.Kernel), 0o644)
	assertVMJailerUpgradeMode(t, filepath.Join(dir, manifest.Initrd), 0o644)
	assertVMJailerUpgradeMode(t, filepath.Join(dir, manifest.Firecracker), 0o755)
	assertVMJailerUpgradeMode(t, filepath.Join(dir, manifest.Jailer), 0o755)
}
```

- [ ] **Step 2: Write failing checksum-before-mutation and custom-bundle tests**

Add one test that tampers with the initrd and proves the kernel remains `0600`
after the checksum failure, plus a table test proving disabled normalization and
manifest-less bundles remain byte-for-byte and mode-for-mode unchanged:

```go
func TestNormalizeManagedVMJailerUpgradeArtifactsVerifiesBeforeMutation(t *testing.T) {
	fixture := newVMJailerUpgradePermissionFixture(t)
	if err := os.WriteFile(fixture.initrd, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := normalizeManagedVMJailerUpgradeArtifacts(fixture.vm)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v, want checksum mismatch", err)
	}
	assertVMJailerUpgradeMode(t, fixture.kernel, 0o600)
}

func TestNormalizeManagedVMJailerUpgradeArtifactsSkipsUnmanagedBundle(t *testing.T) {
	fixture := newVMJailerUpgradePermissionFixture(t)
	fixture.vm.NormalizeManagedArtifacts = false
	if err := normalizeManagedVMJailerUpgradeArtifacts(fixture.vm); err != nil {
		t.Fatal(err)
	}
	assertVMJailerUpgradeMode(t, fixture.kernel, 0o600)
	assertVMJailerUpgradeMode(t, fixture.initrd, 0o600)
}
```

- [ ] **Step 3: Run the tests and verify RED**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestNormalizeManagedVMJailerUpgradeArtifacts' -count=1
```

Expected: build failure because `Manifest`,
`NormalizeManagedArtifacts`, and
`normalizeManagedVMJailerUpgradeArtifacts` do not exist.

---

### Task 2: Implement descriptor-safe managed artifact normalization

**Files:**
- Create: `pkg/catch/vm_jailer_upgrade_permissions.go`
- Modify: `pkg/catch/vm_jailer_upgrade.go`
- Test: `pkg/catch/vm_jailer_upgrade_permissions_test.go`

**Interfaces:**
- Consumes: `vmImageRuntimePermissionOpen`, `verifyOpenVMImageArtifactChecksum`, `verifyVMJailStorageEntryUnchanged`, and `validateVMImageArtifactName`.
- Produces:
  - `normalizeManagedVMJailerUpgradeArtifacts(vm vmJailerUpgradeVM) error`
  - `normalizeVMJailerUpgradeArtifact(spec vmJailerUpgradeArtifactSpec) error`
  - `Manifest vmImageManifest` and `NormalizeManagedArtifacts bool` inventory fields.

- [ ] **Step 1: Add the inventory fields and managed-source classifier**

Extend `vmJailerUpgradeVM`:

```go
type vmJailerUpgradeVM struct {
	// Existing fields remain unchanged.
	Manifest                  vmImageManifest
	NormalizeManagedArtifacts bool
}
```

When inventorying the VM, retain the validated manifest and set
`NormalizeManagedArtifacts` only when all of these hold:

```go
vm.Manifest = vmRuntime.manifest
vm.NormalizeManagedArtifacts = shouldNormalizeManagedVMJailerUpgradeArtifacts(
	cfg.RootDir, vm.Payload, vm.Firecracker, vm.Manifest, deps.localPayload,
)
```

`shouldNormalizeManagedVMJailerUpgradeArtifacts` must require a declared
kernel, a `vm://` payload that does not resolve to a local import, and an image
directory strictly inside `<data-root>/vm-images`.

- [ ] **Step 2: Verify every artifact through an open descriptor before chmod**

Implement a two-pass normalizer in
`pkg/catch/vm_jailer_upgrade_permissions.go`:

```go
type vmJailerUpgradeArtifactSpec struct {
	path     string
	name     string
	checksum string
	mode     os.FileMode
}

func normalizeManagedVMJailerUpgradeArtifacts(vm vmJailerUpgradeVM) error {
	if !vm.NormalizeManagedArtifacts {
		return nil
	}
	specs, err := managedVMJailerUpgradeArtifactSpecs(vm)
	if err != nil {
		return err
	}
	opened := make([]*vmJailerUpgradeOpenArtifact, 0, len(specs))
	defer func() {
		for _, artifact := range opened {
			artifact.close()
		}
	}()
	for _, spec := range specs {
		artifact, err := openVerifiedVMJailerUpgradeArtifact(spec)
		if err != nil {
			return err
		}
		opened = append(opened, artifact)
	}
	for _, artifact := range opened {
		if err := artifact.normalize(); err != nil {
			return err
		}
	}
	return nil
}
```

Each open artifact retains the file, parent descriptor, entry name, desired
mode, and canonical path. `openVerifiedVMJailerUpgradeArtifact` rejects
non-regular files, verifies the manifest checksum on the same descriptor when
one exists, and performs no mutation. `normalize` calls `file.Chmod(mode)` and
then `verifyVMJailStorageEntryUnchanged`.

- [ ] **Step 3: Run the focused tests and verify GREEN**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestNormalizeManagedVMJailerUpgradeArtifacts' -count=1
```

Expected: PASS.

---

### Task 3: Preflight the complete next jailer start before unit staging

**Files:**
- Modify: `pkg/catch/vm_jailer_upgrade.go`
- Modify: `pkg/catch/vm_jailer_upgrade_test.go`
- Modify: `pkg/catch/vm_jailer_upgrade_permissions.go`
- Test: `pkg/catch/vm_jailer_upgrade_test.go`

**Interfaces:**
- Consumes: `newVMJailerTransitionPlan`, `validateVMJailerTransition`, `vmRuntimeIdentity`, and the normalized `vmJailerUpgradeVM`.
- Produces:
  - `validateVMJailerUpgradeNextStart(context.Context, *Config, vmJailerUpgradeVM, vmRuntimeIdentity) error`
  - injectable `normalizeArtifacts` and `validateNextStart` dependencies.

- [ ] **Step 1: Write a failing ordering and rollback-safety test**

Add a test based on `newVMJailerUpgradeIdentityFixture`:

```go
func TestPrepareVMJailerUpgradeValidatesNextStartBeforeStaging(t *testing.T) {
	fixture := newVMJailerUpgradeIdentityFixture(t)
	preflightErr := errors.New("kernel is not readable by runtime")
	var calls []string
	fixture.deps.normalizeArtifacts = func(vm vmJailerUpgradeVM) error {
		calls = append(calls, "normalize:"+vm.Service)
		return nil
	}
	fixture.deps.validateNextStart = func(
		context.Context, *Config, vmJailerUpgradeVM, vmRuntimeIdentity,
	) error {
		calls = append(calls, "validate:alpha")
		return preflightErr
	}

	tx, err := prepareVMJailerUpgradeWithDeps(context.Background(), fixture.cfg, fixture.deps)
	if tx != nil {
		_ = tx.Close()
		t.Fatal("preflight failure returned a unit transaction")
	}
	if !errors.Is(err, preflightErr) {
		t.Fatalf("error = %v, want %v", err, preflightErr)
	}
	if !reflect.DeepEqual(calls, []string{"normalize:alpha", "validate:alpha"}) {
		t.Fatalf("calls = %v", calls)
	}
	if raw, readErr := os.ReadFile(fixture.unitPath); readErr != nil || string(raw) != "old-alpha" {
		t.Fatalf("live unit = %q, %v; want old-alpha", raw, readErr)
	}
	if entries, readErr := os.ReadDir(fixture.systemdDir); readErr != nil ||
		!reflect.DeepEqual(vmJailerUpgradeEntryNames(entries), []string{filepath.Base(fixture.unitPath)}) {
		t.Fatalf("systemd entries = %v, %v; want live unit only", entries, readErr)
	}
}
```

- [ ] **Step 2: Run the test and verify RED**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestPrepareVMJailerUpgradeValidatesNextStartBeforeStaging -count=1
```

Expected: build failure because the new dependencies do not exist.

- [ ] **Step 3: Add the preparation dependencies and ordering**

Extend `vmJailerUpgradeDeps`:

```go
normalizeArtifacts func(vmJailerUpgradeVM) error
validateNextStart  func(context.Context, *Config, vmJailerUpgradeVM, vmRuntimeIdentity) error
```

Complete them from defaults, then change
`prepareVMJailerUpgradeWithDeps` so the order is:

```go
identity, err := deps.ensureRuntimeIdentity()
if err != nil {
	return nil, fmt.Errorf("ensure VM runtime identity for jailer upgrade: %w", err)
}
for _, vm := range plan.VMs {
	if err := deps.normalizeArtifacts(vm); err != nil {
		return nil, fmt.Errorf("normalize managed VM image artifacts for %q: %w", vm.Service, err)
	}
	if err := deps.validateNextStart(ctx, cfg, vm, identity); err != nil {
		return nil, fmt.Errorf("validate next jailer start for %q: %w", vm.Service, err)
	}
}
return prepareVMJailerUnitTransaction(ctx, plan.VMs, plan.Summary, deps)
```

The default next-start validator reloads the current data view, builds a
`vmJailerTransitionPlan` from the stored service root, and calls
`validateVMJailerTransition`. It does not call
`executeVMJailerTransition` and therefore does not touch the disk, network,
jail directory, readiness marker, or systemd.

- [ ] **Step 4: Add the full default-preflight regression**

Create a realistic stopped legacy VM fixture with a valid raw disk,
Firecracker config, service directories, network record, matching fake runtime
pair probes, and a `0600` managed kernel. Assert that the default preparation
normalizes the kernel and stages the unit; then make the config unreadable and
assert preparation fails before staging.

- [ ] **Step 5: Run the Catch package tests and verify GREEN**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestPrepareVMJailerUpgrade|TestNormalizeManagedVMJailerUpgradeArtifacts' -count=1
mise exec -- go test ./pkg/catch -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit the implementation**

Inspect with `but diff`, then commit only the Catch implementation and tests:

```bash
but commit codex/vm-legacy-permissions -m "catch: preflight legacy VM jailer migration"
```

Expected: the existing design/plan commit remains below one focused
implementation commit.

---

### Task 4: Run release-grade and Linux validation

**Files:**
- No source changes expected.
- Test artifacts: temporary files under a `mktemp -d` directory on the approved Linux validation host.

**Interfaces:**
- Consumes: the targeted Catch test binary and repository quality tasks.
- Produces: evidence that the v0.9 regression passes on Linux and the repository remains release-grade.

- [ ] **Step 1: Run repository verification**

Run:

```bash
mise exec -- go test ./... -count=1
mise exec -- pre-commit run --all-files
mise run quality:goal
```

Expected: all commands exit 0 with no private-info, coverage, CRAP,
golangci, race, fuzz, or mutation regressions.

- [ ] **Step 2: Build and run the focused test binary on Linux**

Build the package test binary with the repository toolchain:

```bash
GOOS=linux GOARCH=amd64 mise exec -- go test -c ./pkg/catch -o /tmp/yeet-catch-v0105.test
```

Copy it to the approved host under a fresh temporary directory and run:

```bash
./yeet-catch-v0105.test -test.run 'TestNormalizeManagedVMJailerUpgradeArtifacts|TestPrepareVMJailerUpgradeValidatesNextStartBeforeStaging' -test.count=1 -test.v
```

Expected: PASS on Linux. Remove only the fresh temporary directory afterward.

- [ ] **Step 3: Review the branch**

Use `superpowers:requesting-code-review`, inspect `but diff`/`but status -fv`,
and resolve any correctness or security findings before landing.

---

### Task 5: Land the fix and publish v0.10.5

**Files:**
- Modify: `website/docs/changelog.mdx`
- Modify: `website` gitlink in the root repository.

**Interfaces:**
- Consumes: verified implementation commits and the `yeet-release`,
  `yeet-docs`, and GitButler workflows.
- Produces: `origin/main`, annotated tag `v0.10.5`, public release artifacts,
  checksums, and verified Catch/Yeet binaries.

- [ ] **Step 1: Write the corrective changelog entry**

Add a date-first `v0.10.5` section with one standalone user-facing bullet:

```mdx
## v0.10.5

- Existing VMs created by older Yeet releases now migrate safely to the
  jailer-backed runtime: Catch repairs trusted legacy image permissions and
  validates the next start before changing the VM unit, without restarting
  running workloads.
```

Carry forward any still-relevant v0.10.4 user-visible runtime-upgrade behavior
required for the latest entry to stand alone. Do not mention private hosts,
incident-specific VM names, commit hashes, or release mechanics.

- [ ] **Step 2: Verify and publish the website commit**

Run:

```bash
git -C website diff --check
rg -n "private[-]host|/User[s]/" README.md website/docs .codex/skills
```

Commit and push only the website change inside the website repository, then
verify its branch advertises the exact commit.

- [ ] **Step 3: Commit and land the root release state**

Commit the website gitlink to `codex/vm-legacy-permissions`, squash/tidy the
session to a clean release history if necessary, run `but pull --check`, and
publish the verified session commit directly to `origin/main` using the root
`AGENTS.md` finish-to-main flow.

Verify:

```bash
git rev-parse main
git rev-parse origin/main
git ls-remote origin refs/heads/main
git rev-parse HEAD:website
git -C website rev-parse HEAD
```

Expected: root SHAs agree and the root gitlink equals the published website
commit.

- [ ] **Step 4: Tag and verify v0.10.5**

Create and push:

```bash
git tag -a v0.10.5 -m "v0.10.5"
git push origin v0.10.5
```

Watch the release workflow to completion. Verify the public GitHub release,
all expected Linux/macOS Yeet and Catch artifacts, every checksum pair, and
embedded version/source metadata. Confirm:

```bash
git ls-remote --tags origin v0.10.5
gh release view v0.10.5
```

- [ ] **Step 5: End clean**

Run `but pull`, preview with `but clean --dry-run`, clean only this integrated
session branch, and verify the root repository, website repository,
GitButler workspace, local `main`, and `origin/main` are clean and aligned.
