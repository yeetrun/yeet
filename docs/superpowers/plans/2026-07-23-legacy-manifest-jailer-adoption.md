# Legacy Manifest Jailer Adoption Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow safe runtime adoption of pre-jailer image manifests after Catch has installed and verified their matching sibling jailer.

**Architecture:** Extend only the installed-manifest runtime relation. A declared jailer remains manifest-bound; a missing legacy declaration permits only the conventional sibling `jailer`, whose trusted-file evidence, SHA-256, and matching runtime version are already required by the surrounding adoption inventory.

**Tech Stack:** Go, Catch VM runtime adoption, Go tests, GitButler, GitHub Actions.

## Global Constraints

- Do not rewrite legacy manifests or manually edit the service database.
- Do not weaken trusted-file, symlink, runtime-version, or transaction checks.
- Do not restart running VMs during Catch adoption.
- Keep private infrastructure identifiers out of tracked files and release notes.

---

### Task 1: Prove the legacy-manifest adoption gap

**Files:**
- Modify: `pkg/catch/vm_runtime_adoption_test.go`

**Interfaces:**
- Consumes: `newVMRuntimeAdoptionFixture`, `vmImageManifest`, and `inventoryVMRuntimeAdoptionFleet`
- Produces: regression coverage for the accepted sibling and rejected non-sibling cases

- [ ] **Step 1: Write the failing adoptable-sibling test**

Create a manifest-backed adoption fixture, clear `manifest.Jailer`, remove its
checksum, rewrite the fixture manifest, and assert that inventory classifies
the VM as `custom-legacy` with the measured sibling jailer in
`Components.Runtime.Configured`.

- [ ] **Step 2: Write the non-sibling rejection test**

Point the loaded unit at a trusted jailer outside the manifest directory and
assert that inventory remains `adoption-blocked` with a sibling-path error.

- [ ] **Step 3: Run the focused tests and verify RED**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestVMRuntimeAdoptionInventoryLegacyManifest -count=1
```

Expected: the sibling case fails because the current validator reports that
the installed manifest jailer contradicts the loaded unit path.

### Task 2: Add the narrow legacy compatibility rule

**Files:**
- Modify: `pkg/catch/vm_runtime_adoption.go`
- Test: `pkg/catch/vm_runtime_adoption_test.go`

**Interfaces:**
- Consumes: `validateVMRuntimeAdoptionManifestRuntimeRelation`
- Produces: legacy missing-jailer acceptance only for
  `filepath.Join(filepath.Dir(state.Path), "jailer")`

- [ ] **Step 1: Implement the minimal relation change**

Keep the Firecracker relation unchanged. If `manifest.Jailer` is empty, require
the loaded jailer path to equal the conventional sibling path. Otherwise retain
the existing exact manifest-relative jailer comparison.

- [ ] **Step 2: Run the focused tests and verify GREEN**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestVMRuntimeAdoptionInventoryLegacyManifest -count=1
```

Expected: both the accepted-sibling and rejected-non-sibling tests pass.

- [ ] **Step 3: Run the Catch package**

Run:

```bash
mise exec -- go test ./pkg/catch -count=1
```

Expected: PASS.

### Task 3: Verify, land, and release

**Files:**
- Modify: `website/docs/changelog.mdx`
- Modify: `website` gitlink

**Interfaces:**
- Consumes: the verified compatibility patch
- Produces: v0.10.6 release artifacts and a live upgrade path

- [ ] **Step 1: Run deterministic repository gates**

Run:

```bash
mise exec -- pre-commit run --all-files
mise run quality:goal
```

Expected: no new private-info, coverage, CRAP, lint, race, fuzz, or mutation
regressions. Any existing destination-goal debt must remain unchanged.

- [ ] **Step 2: Run the focused tests on Linux**

Cross-compile the Catch test binary and run
`TestVMRuntimeAdoptionInventoryLegacyManifest` on the authorized Linux host
with a root-owned test temporary directory.

- [ ] **Step 3: Publish the corrective changelog**

Add a standalone v0.10.6 entry explaining that Catch now adopts VMs created
from pre-jailer image manifests without changing the manifest or restarting the
VM. Commit and push the website repository, then commit its gitlink in Yeet.

- [ ] **Step 4: Tidy and land**

Use GitButler to produce one session commit based on current `origin/main`, run
`but pull --check`, and publish that exact commit to local and remote `main`.

- [ ] **Step 5: Tag and verify v0.10.6**

Create and push annotated tag `v0.10.6`. Watch Release and Nightly Release,
verify all checksums, and verify every binary is stamped `v0.10.6` at the landed
commit.

- [ ] **Step 6: Perform live validation**

Upgrade the authorized Catch host, confirm adoption/runtime status and workload
health, then retry the operator's runtime-upgrade command when the target host
is reachable through the intended control path.
