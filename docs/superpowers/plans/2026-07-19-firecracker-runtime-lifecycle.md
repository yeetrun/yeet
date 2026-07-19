# Firecracker Runtime Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver independent guest-base, guest-kernel, and host Firecracker+jailer lifecycles while removing Firecracker memory checkpoints and preserving safe, restart-free Catch upgrades.

**Architecture:** This is the coordination plan for four independently reviewable implementation plans. Recovery becomes disk-only first. The runtime publisher then creates an immutable, low-level-tested candidate in the opt-in candidate channel; Catch lifecycle management consumes that exact candidate; the completed Catch path supplies the full canary evidence needed for stable runtime promotion. Guest/kernel separation follows only after Catch dual-read support ships. Each phase leaves the product usable and has its own tests, release gate, and rollback boundary.

**Tech Stack:** Go, JSON-backed Catch database, systemd, Firecracker+jailer, ZFS, Bash, jq, GitHub Actions, GitHub Releases, Nix/NixOS, Ubuntu image tooling.

## Global Constraints

- Every Firecracker process launches through the matching jailer and runs as the host `yeet-vm` account; there is no direct-Firecracker or legacy-root fallback.
- Firecracker and jailer are one immutable runtime artifact and can never be selected independently.
- Existing VMs keep their active guest disk. Adoption records component identities and rewrites derived launch state without replacing the disk or restarting a running VM.
- Normal Catch upgrades and `yeet vm images update` never restart a VM.
- The default runtime policy is `manual` on the stable channel. `stage-on-restart` is opt-in and never causes a restart.
- The guest is untrusted. It cannot authorize or influence a runtime URL, version, digest, path, channel, download, switch, rollback, cache prune, host policy, or restart.
- Guest package upgrades remain guest operations. Kernel selection continues through the validated guest journal and host-side kernel synchronization path.
- Yeet supports only crash-consistent ZFS VM disk recovery points. It does not create, retain, load, or advertise Firecracker memory/VMM-state snapshots.
- Raw VM disks have no Yeet snapshot backend.
- All cache, descriptor, transaction, unit, and migration paths must work with `/var/lib/yeet`, custom Catch data roots, custom service roots, and ZFS-backed roots.
- Observation requires `read`; runtime update, import, upgrade, rollback, policy, protection, and prune operations require `manage`.
- Do not redesign native-service `--run-as`.
- Keep tracked plans, docs, examples, tests, and commit messages free of private hostnames, usernames, and infrastructure paths.

---

## Plan Suite

Use the cross-plan execution order below; runtime candidate publication and stable promotion intentionally bracket Catch implementation.

1. [Disk-Only VM Recovery](2026-07-19-disk-only-vm-recovery.md)
   - Removes persistent and transient Firecracker snapshot creation and all memory-state restore machinery.
   - Adds the rollout-blocking inventory for legacy `full` checkpoint data.
   - Leaves ZFS disk create/list/inspect/protect/unprotect/clone/restore/remove/retention intact.

2. [Firecracker Runtime Publishing and Promotion](2026-07-19-firecracker-runtime-publishing.md)
   - Publishes immutable Firecracker+jailer pairs with manifests and provenance.
   - Adds stable-release discovery, static verification, KVM integration, canary evidence, deliberate candidate/stable promotion, and revocation.
   - Bootstraps the currently approved upstream runtime only after the same gates pass.

3. [Catch Firecracker Runtime Management](2026-07-19-catch-firecracker-runtime-management.md)
   - Adds component state, legacy adoption, trusted runtime cache/import, runtime descriptors, CLI/status/policy, staged apply, trial fallback, rollback, and pruning.
   - Reuses the jailer-only trusted launch path and generalized unit transaction machinery.
   - Leaves running VMs on their current process until an operator restart or natural cold-start boundary.

4. [Independent VM Components](2026-07-19-independent-vm-components.md)
   - Publishes guest bases and kernels independently from the runtime.
   - Resolves new VM composition locks from component catalogs.
   - Preserves monolithic bundles for already-pinned VMs and old Catch compatibility while new releases stop embedding Firecracker.

## Cross-Plan Execution Order

1. Complete every task in Disk-Only VM Recovery.
2. Complete Tasks 1-5 in Firecracker Runtime Publishing and Promotion. The result is one immutable candidate with static and low-level KVM integration evidence in the opt-in candidate channel; stable remains unchanged.
3. Complete Tasks 1-10 in Catch Firecracker Runtime Management against that exact candidate manifest digest.
4. Complete Tasks 6-7 in Firecracker Runtime Publishing and Promotion. Exercise the new Catch upgrade/trial/rollback paths during canary, then promote stable by reviewed catalog PR.
5. Complete Task 11 in Catch Firecracker Runtime Management against the promoted stable entry.
6. Complete Independent VM Components. Tasks 3-4 prepare gated component builders, and Task 10 performs the first candidate publication only after Tasks 5-9 prove Catch dual-read/adoption support.

Do not collapse these boundaries into one release: each numbered boundary is a review and rollback checkpoint.

## Cross-Plan Interfaces

The plans share these stable names. Do not rename them in one plan without updating all later plans before implementation:

```go
// pkg/db/db.go
type VMComponentsConfig struct {
	GuestBase VMGuestBaseConfig
	Kernel    VMKernelArtifactConfig
	Runtime   VMRuntimeLifecycleConfig
}

type VMRuntimeArtifactConfig struct {
	ID                string
	ManifestSHA256    string
	FirecrackerSHA256 string
	JailerSHA256      string
	Firecracker       string
	Jailer            string
	Source            string
}
```

```go
// pkg/catch/vm_runtime_manifest.go
type vmRuntimeCatalogRef struct {
	RuntimeID   string `json:"runtime_id"`
	ManifestURL string `json:"manifest_url"`
	ManifestSHA string `json:"manifest_sha256"`
}
```

```text
<catch-data-root>/vm-runtimes/<architecture>/<runtime-id>/<manifest-sha256>/
<service-data-root>/vmm-runtime.json
<service-run-root>/vmm-runtime-running.json
<service-run-root>/vmm-runtime-trial-result.json
```

`vmm-runtime.json` is the root-owned, durable descriptor used by `catch vm-run`. The database is authoritative; the descriptor, running marker, systemd unit, and transaction journal are reconciled derived state.

## Release Gates

- Gate 1: no `CreateSnapshot`, `LoadSnapshot`, memory path, VMM-state path, `--full`, or full-restore mode remains reachable; disk-only KVM validation passes.
- Gate 2a: one immutable runtime candidate survives static verification and low-level KVM jailer/boot/storage integration, then enters the opt-in candidate channel by reviewed PR; stable remains unchanged.
- Gate 2b: after Catch management exists, that exact candidate survives the full Catch canary matrix; promotion records point to durable attestation URLs and digests.
- Gate 3: existing monolithic VMs adopt logical runtime identities without restart; manual staging, natural restart, explicit trial, fallback, rollback, and prune pass on raw and ZFS storage with default/custom roots before Gate 2b promotes stable.
- Gate 4: new Ubuntu and NixOS VMs provision from independent guest-base, kernel, and runtime catalogs; old monolithic VMs still boot through their exact matching jailer.
- Final gate: a normal Catch upgrade on a host with running VMs changes no running PID and causes no VM restart; each VM's status reports distinct running, configured, staged, previous, kernel, and guest-base identities.

## Commit and Integration Order

- Keep each subplan on its own reviewable branch or stack.
- Land the disk-only plan before runtime management so snapshot-version compatibility is not carried into the new runtime model.
- Land runtime publishing contracts/candidate integration before Catch consumption; land canary/stable promotion only after Catch trial/rollback validation.
- Land Catch dual-read/adoption before changing guest releases to component-only artifacts.
- Do not delete monolithic release assets or old immutable tags.
- Do not enable stable automatic staging until canary evidence exists for the exact runtime manifest digest.
