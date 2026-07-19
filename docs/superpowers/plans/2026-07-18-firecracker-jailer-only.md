# Firecracker Jailer-Only Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove every supported root Firecracker launch path, migrate existing VM units without restarting running VMs, and make the next launch complete a one-way transition to the static `yeet-vm` jailer identity.

**Architecture:** Catch remains the root supervisor and always invokes the matching Firecracker jailer. A focused readiness helper interprets the existing marker as either ready or pending restart, a pre-start transition owns storage and TAP resources before writing that marker, and a transactional installer phase resolves old jailer artifacts and rewrites every VM unit without stopping a VM.

**Tech Stack:** Go, Firecracker jailer, systemd, Linux mount and network namespaces, TAP, KVM, ZFS ZVOLs, Unix sockets, GitButler, MDX.

## Global Constraints

- Every Firecracker process started by Yeet must run through a matching jailer as the non-root static `yeet-vm` account.
- There is no supported flag, stored value, missing-marker default, error fallback, or rollback that launches Firecracker directly as root.
- Catch, systemd VM units, network setup, storage preparation, snapshot and journal work, and guest-kernel synchronization remain root.
- VMs remain excluded from the generic native-service `--run-as` feature.
- An absent `<service-root>/run/vmm-isolation` marker means `jailer-pending-restart`; only the exact marker content `jailer` means ready.
- Catch upgrades rewrite VM units but do not stop, start, restart, or try-restart any VM.
- A Firecracker process already running during upgrade may finish its current lifetime; its next launch must use jailer or fail closed.
- Fresh Catch storage defaults to `/var/lib/yeet`, but every runtime and upgrade path must derive configured data roots, custom service roots, and ZFS-backed roots from configuration and database state.
- The shared `yeet-vm` account remains the supported runtime identity; per-VM UIDs and GIDs are outside this change.
- VM info remains a `read` operation; Catch installation and existing VM mutations retain their current high-trust boundaries.
- Public README and manual prose describe jailer as the product's steady state. Upgrade timing belongs only in focused compatibility text.
- Do not commit, rebase, unapply, or deploy unrelated ISO-network, host-storage, service-identity, or GitButler-skill workspace changes.

## File Structure

### New focused files

- `pkg/catch/vm_jailer_readiness.go`: readiness marker, `yeet-vm` account creation, and stable jailer IDs; replaces `vm_isolation.go` terminology.
- `pkg/catch/vm_jailer_transition.go`: plans and executes the one-way stopped-VM resource transition used by `vm-network-ensure`.
- `pkg/catch/vm_jailer_transition_test.go`: transition ordering, failure, retry, raw-disk, checkpoint, ZVOL, and ready fast-path tests.
- `pkg/catch/vm_jailer_upgrade.go`: upgrade inventory, trusted jailer resolution and backfill, unit staging, atomic commit, rollback, and summary.
- `pkg/catch/vm_jailer_upgrade_test.go`: default/custom/ZFS roots, local/remote jailer resolution, no-restart unit transaction, and rollback tests.

### Existing runtime files

- `pkg/catch/vm_jailer.go`, `pkg/catch/vm_console_proxy.go`: make jail preparation and jailer execution mandatory.
- `cmd/catch/catch.go`: keep `vm-run` and installer command wiring thin; require jailer arguments and call the package upgrade transaction.
- `pkg/catch/vm_systemd.go`: render only jailer-enabled units and regenerate them from effective roots.
- `pkg/catch/vm_network_reconcile.go`: always assign TAPs to `yeet-vm` and route pending VMs through the transition.
- `pkg/catch/vm_snapshot.go`: always delegate new checkpoint directories to `yeet-vm`.
- `pkg/catch/vm_provision.go`: retain jailer-only provisioning and use readiness names.
- `pkg/catch/vm_resize.go`: remove mode selection and mode rollback while retaining shape and network changes.

### Existing installer, CLI, RPC, and docs files

- `pkg/catch/installer_file.go`: expose rollback to the previously installed Catch generation.
- `pkg/cli/cli.go`: remove `--vmm-isolation` from `vm set` metadata and parsing.
- `pkg/catch/service_info.go`, `pkg/catchrpc/types.go`, `pkg/yeet/info_cmd.go`: preserve the additive info field and emit only `jailer` or `jailer-pending-restart`.
- `README.md`, `website/docs/cli/yeet-cli.mdx`, `website/docs/payloads/vms.mdx`, `.codex/skills/yeet-cli/references/yeet-help-agent.md`: remove the mode choice and document the invariant.
- Delete `docs/superpowers/specs/2026-07-18-firecracker-jailer-design.md` and `docs/superpowers/plans/2026-07-18-firecracker-jailer.md` after this plan exists; the approved jailer-only design remains authoritative.

### Overlap and sequencing risks

- `cmd/catch/catch.go` overlaps the active host-storage and ISO-network work. Land or checkpoint this task's installer and `vm-run` hunks before either task edits the same functions.
- `pkg/cli/cli.go`, `pkg/catchrpc/types.go`, `pkg/catch/service_info.go`, and `pkg/yeet/info_cmd.go` overlap the planned generic service-identity implementation. Complete this task's small CLI/info slice before that implementation begins, then hand off the exact landed commit.
- `pkg/catch/catch.go` and host-storage tests already have unrelated applied changes. Work from an isolated checkout of the jailer branch and verify the exact commit, not the GitButler union workspace.
- `website/` is a separate repository. Commit and push its docs first, then commit only the root gitlink.

---

### Task 1: Replace Isolation Mode with One-Way Readiness

**Files:**

- Rename: `pkg/catch/vm_isolation.go` to `pkg/catch/vm_jailer_readiness.go`
- Rename: `pkg/catch/vm_isolation_test.go` to `pkg/catch/vm_jailer_readiness_test.go`

**Interfaces:**

- Produces: `vmJailerReadinessForRoot(string) (vmJailerReadiness, error)`, `markVMJailerReady(string) error`, `ensureVMRuntimeIdentity() (vmRuntimeIdentity, error)`, and `vmJailerID(string) string`.
- Consumes: `serviceRunDirForRoot` and `writeVMFileAtomic`.

- [ ] **Step 1: Write the readiness tests**

Replace the mode table with these exact state cases and keep the existing identity and jailer-ID tests:

```go
func TestVMJailerReadinessForRoot(t *testing.T) {
	root := t.TempDir()
	got, err := vmJailerReadinessForRoot(root)
	if err != nil || got != vmJailerPendingRestart {
		t.Fatalf("readiness = %q, %v; want %q", got, err, vmJailerPendingRestart)
	}
	if err := markVMJailerReady(root); err != nil {
		t.Fatal(err)
	}
	got, err = vmJailerReadinessForRoot(root)
	if err != nil || got != vmJailerReady {
		t.Fatalf("readiness = %q, %v; want %q", got, err, vmJailerReady)
	}
}

func TestVMJailerReadinessRejectsLegacyAndUnknownValues(t *testing.T) {
	for _, value := range []string{"legacy-root\n", "dynamic\n", "\n"} {
		t.Run(strings.TrimSpace(value), func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(serviceRunDirForRoot(root), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(vmJailerReadinessMarkerPath(root), []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := vmJailerReadinessForRoot(root); err == nil {
				t.Fatalf("value %q was accepted", value)
			}
		})
	}
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMJailer(Readiness|ID)|TestVMRuntimeIdentity' -count=1
```

Expected: compile failure for the new readiness identifiers.

- [ ] **Step 3: Implement the one-way state**

Replace the two-mode constants and marker removal with this implementation while preserving `writeVMFileAtomic`, `ensureVMRuntimeIdentity`, and `vmJailerID`:

```go
type vmJailerReadiness string

const (
	vmJailerReady               vmJailerReadiness = "jailer"
	vmJailerPendingRestart      vmJailerReadiness = "jailer-pending-restart"
	vmJailerReadinessMarkerName                   = "vmm-isolation"
)

func vmJailerReadinessMarkerPath(root string) string {
	return filepath.Join(serviceRunDirForRoot(root), vmJailerReadinessMarkerName)
}

func vmJailerReadinessForRoot(root string) (vmJailerReadiness, error) {
	raw, err := os.ReadFile(vmJailerReadinessMarkerPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return vmJailerPendingRestart, nil
	}
	if err != nil {
		return "", fmt.Errorf("read VM jailer readiness: %w", err)
	}
	if strings.TrimSpace(string(raw)) != string(vmJailerReady) {
		return "", fmt.Errorf("unsupported VM jailer readiness marker %q", strings.TrimSpace(string(raw)))
	}
	return vmJailerReady, nil
}

func markVMJailerReady(root string) error {
	if err := writeVMFileAtomic(vmJailerReadinessMarkerPath(root), []byte("jailer\n"), 0o600); err != nil {
		return fmt.Errorf("write VM jailer readiness: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run the focused tests and verify GREEN**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMJailer(Readiness|ID)|TestVMRuntimeIdentity' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the readiness checkpoint**

Run `but diff`, confirm only the two renamed readiness files are present, then run:

```bash
but commit codex/firecracker-jailer-only -m "catch: make VM jailer readiness one way"
```

---

### Task 2: Make Launch and Unit Rendering Fail Closed

**Files:**

- Modify: `pkg/catch/vm_jailer.go`
- Modify: `pkg/catch/vm_jailer_test.go`
- Modify: `pkg/catch/vm_console_proxy.go`
- Modify: `pkg/catch/vm_console_test.go`
- Modify: `pkg/catch/vm_systemd.go`
- Modify: `pkg/catch/vm_systemd_test.go`
- Modify: `pkg/catch/vm_provision.go`
- Modify: `pkg/catch/vm_provision_test.go`
- Modify: `pkg/catch/vm_resize.go`
- Modify: `pkg/catch/vm_resize_test.go`
- Modify: `cmd/catch/catch.go`
- Modify: `cmd/catch/catch_test.go`

**Interfaces:**

- Produces: `prepareVMConsoleProcess(context.Context, VMConsoleProxyConfig, bool) (*exec.Cmd, func(), error)` with no direct-Firecracker branch.
- Changes: `renderVMSystemdUnit(vmSystemdConfig) (string, error)` so missing jailer inputs cannot produce a unit.
- Consumes: readiness-independent `validateVMJailerRuntimePair`, `buildVMJailPlan`, `prepareVMJail`, and `cleanupVMJail`.

- [ ] **Step 1: Add failing launch and rendering tests**

Add these assertions to the existing tables:

```go
func TestPrepareVMConsoleProcessRequiresJailer(t *testing.T) {
	_, _, err := prepareVMConsoleProcess(context.Background(), VMConsoleProxyConfig{
		Firecracker: "/opt/vm/firecracker",
		APISocket: "/run/vm/firecracker.sock",
		ConfigFile: "/run/vm/firecracker.json",
		ConsoleSocket: "/run/vm/serial.sock",
		Service: "devbox",
		ServiceRoot: "/srv/devbox",
	}, false)
	if err == nil || !strings.Contains(err.Error(), "jailer path is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestRenderVMSystemdUnitRequiresJailer(t *testing.T) {
	_, err := renderVMSystemdUnit(vmSystemdConfig{
		Service: "devbox", Runner: "/run/catch", DataDir: "/var/lib/yeet",
		ServiceRoot: "/srv/devbox", DiskPath: "/srv/devbox/data/rootfs.raw",
		Firecracker: "/opt/vm/firecracker", JailerBase: "/var/lib/yeet/vm-jailer",
		ConfigPath: "/srv/devbox/run/firecracker.json", APISocket: "/srv/devbox/run/firecracker.sock",
		ConsoleSocket: "/srv/devbox/run/serial.sock", WorkingDirectory: "/srv/devbox/data",
	})
	if err == nil || !strings.Contains(err.Error(), "jailer") {
		t.Fatalf("error = %v", err)
	}
}
```

Update the successful unit test to require both tokens in `ExecStart`:

```go
for _, want := range []string{
	"--jailer /opt/vm/jailer",
	"--jailer-base /var/lib/yeet/vm-jailer",
} {
	if !strings.Contains(unit, want) {
		t.Fatalf("unit missing %q:\n%s", want, unit)
	}
}
```

- [ ] **Step 2: Run launch tests and verify RED**

Run:

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -run 'Test(PrepareVMConsoleProcessRequiresJailer|RenderVMSystemdUnitRequiresJailer|HandleVMRun)' -count=1
```

Expected: the renderer signature and mandatory-jailer expectations fail.

- [ ] **Step 3: Remove direct Firecracker command construction**

Delete `vmFirecrackerCommand` and make both validation and preparation unconditional:

```go
func prepareVMConsoleProcess(ctx context.Context, cfg VMConsoleProxyConfig, restoreMode bool) (*exec.Cmd, func(), error) {
	identity, err := vmJailEnsureRuntimeIdentity()
	if err != nil {
		return nil, nil, err
	}
	if err := vmJailValidateRuntimePair(ctx, cfg.Firecracker, cfg.Jailer); err != nil {
		return nil, nil, err
	}
	plan, err := buildVMJailPlan(cfg, identity)
	if err != nil {
		return nil, nil, err
	}
	if err := vmJailPrepare(plan); err != nil {
		return nil, nil, err
	}
	cleanup := func() {
		if err := vmJailCleanup(plan); err != nil {
			fmt.Fprintf(os.Stderr, "warning: clean up VM jail %s: %v\n", plan.ID, err)
		}
	}
	cmd := exec.CommandContext(ctx, cfg.Jailer, vmJailerCommandArgs(cfg, identity, restoreMode)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 0, Gid: 0, Groups: []uint32{}}}
	return cmd, cleanup, nil
}
```

Make `validateVMConsoleProxyConfig` call `validateVMJailCanonicalInputs` unconditionally after checking the console socket. Extend `validateVMJailCanonicalInputs` to reject an empty jailer base instead of silently supplying one at the runtime boundary.

- [ ] **Step 4: Make every unit renderer require jailer inputs**

Change the renderer to validate all required fields and always append jailer flags:

```go
func renderVMSystemdUnit(cfg vmSystemdConfig) (string, error) {
	required := []struct{ label, value string }{
		{"service", cfg.Service}, {"runner", cfg.Runner}, {"service root", cfg.ServiceRoot},
		{"disk", cfg.DiskPath}, {"firecracker", cfg.Firecracker}, {"jailer", cfg.Jailer},
		{"jailer base", cfg.JailerBase}, {"config", cfg.ConfigPath},
		{"API socket", cfg.APISocket}, {"console socket", cfg.ConsoleSocket},
	}
	for _, input := range required {
		if strings.TrimSpace(input.value) == "" {
			return "", fmt.Errorf("VM systemd %s is required", input.label)
		}
	}
	launchArgs := []string{
		"vm-run", "--service", cfg.Service, "--service-root", cfg.ServiceRoot,
		"--disk-path", cfg.DiskPath, "--firecracker", cfg.Firecracker,
		"--jailer", cfg.Jailer, "--jailer-base", cfg.JailerBase,
		"--api-sock", cfg.APISocket, "--config-file", cfg.ConfigPath,
		"--console-sock", cfg.ConsoleSocket,
	}
	return renderVMSystemdUnitText(cfg, launchArgs), nil
}
```

Extract the existing `fmt.Sprintf` body unchanged into `renderVMSystemdUnitText`. Update provisioning, settings, host-storage regeneration, and their tests for the error-returning signature. In `regenerateHostStorageVMSystemdUnit`, always derive `jailer := filepath.Join(filepath.Dir(rootFS), "jailer")`; do not inspect readiness.

- [ ] **Step 5: Require both `vm-run` flags at the command boundary**

Keep the existing `flag.FlagSet`, but reject missing values before calling the proxy:

```go
if strings.TrimSpace(*jailer) == "" {
	return fmt.Errorf("vm-run requires --jailer")
}
if strings.TrimSpace(*jailerBase) == "" {
	return fmt.Errorf("vm-run requires --jailer-base")
}
```

- [ ] **Step 6: Run targeted launch tests and verify GREEN**

Run:

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -run 'Test.*(VMConsole|VMJail|VMRun|VMSystemd|HostStorage.*VM)' -count=1
```

Expected: PASS, and `rg -n 'vmFirecrackerCommand|exec\.CommandContext\(ctx, cfg\.Firecracker' pkg/catch` returns no matches.

- [ ] **Step 7: Commit the fail-closed launch checkpoint**

```bash
but commit codex/firecracker-jailer-only -m "catch: require jailer for every VM launch"
```

---

### Task 3: Complete Pending VMs in `vm-network-ensure`

**Files:**

- Create: `pkg/catch/vm_jailer_transition.go`
- Create: `pkg/catch/vm_jailer_transition_test.go`
- Modify: `pkg/catch/vm_network_reconcile.go`
- Modify: `pkg/catch/vm_network_test.go`
- Modify: `pkg/catch/vm_snapshot.go`
- Modify: `pkg/catch/vm_snapshot_test.go`

**Interfaces:**

- Produces: `newVMJailerTransitionPlan(*db.DataView, vmJailerTransitionInput, vmRuntimeIdentity) (vmJailerTransitionPlan, error)` and `executeVMJailerTransition(context.Context, vmJailerTransitionPlan, vmJailerTransitionDeps) error`.
- Changes: `ensureVMNetworkFromDataView(context.Context, *db.DataView, vmNetworkEnsureInput) error` receives the configured data root as well as the effective service root.
- Consumes: `validateVMJailerRuntimePair`, `buildVMJailPlan`, `cleanupVMJail`, `delegateVMJailStorage`, `vmNetworkPlan.CleanupCommands`, `vmNetworkPlan.SetupCommands`, and `markVMJailerReady`.

- [ ] **Step 1: Write the transition ordering and failure tests**

Use a dependency recorder with these exact operation names:

```go
func TestExecuteVMJailerTransitionMarksReadyLast(t *testing.T) {
	var got []string
	deps := vmJailerTransitionDeps{
		validate: func(context.Context, vmJailerTransitionPlan) error { got = append(got, "validate"); return nil },
		cleanupJail: func(vmJailPlan) error { got = append(got, "cleanup-jail"); return nil },
		delegateStorage: func(string, string, vmRuntimeIdentity) error { got = append(got, "delegate-storage"); return nil },
		runNetwork: func([][]string, vmNetworkCommandMode) error {
			got = append(got, "network")
			return nil
		},
		markReady: func(string) error { got = append(got, "mark-ready"); return nil },
	}
	if err := executeVMJailerTransition(context.Background(), testVMJailerTransitionPlan(), deps); err != nil {
		t.Fatal(err)
	}
	want := []string{"validate", "cleanup-jail", "delegate-storage", "network", "network", "mark-ready"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
}

func TestExecuteVMJailerTransitionFailureDoesNotMarkReady(t *testing.T) {
	marked := false
	deps := testVMJailerTransitionDeps()
	deps.runNetwork = func([][]string, vmNetworkCommandMode) error { return errors.New("network failed") }
	deps.markReady = func(string) error { marked = true; return nil }
	if err := executeVMJailerTransition(context.Background(), testVMJailerTransitionPlan(), deps); err == nil {
		t.Fatal("transition succeeded")
	}
	if marked {
		t.Fatal("readiness marker was written after failure")
	}
}

func testVMJailerTransitionPlan() vmJailerTransitionPlan {
	return vmJailerTransitionPlan{
		Service: "devbox",
		Root: "/srv/devbox",
		Disk: "/srv/devbox/data/rootfs.raw",
		Identity: vmRuntimeIdentity{UID: 987, GID: 987},
		Network: vmNetworkPlan{Service: "devbox", Interfaces: []vmNetworkInterfacePlan{{Mode: "lan", Tap: "yvm-devbox-l0", Bridge: "br0"}}},
		Jail: vmJailPlan{ID: "yeet-devbox", JailRoot: "/var/lib/yeet/vm-jailer/firecracker/yeet-devbox/root"},
	}
}

func testVMJailerTransitionDeps() vmJailerTransitionDeps {
	return vmJailerTransitionDeps{
		validate: func(context.Context, vmJailerTransitionPlan) error { return nil },
		cleanupJail: func(vmJailPlan) error { return nil },
		delegateStorage: func(string, string, vmRuntimeIdentity) error { return nil },
		runNetwork: func([][]string, vmNetworkCommandMode) error { return nil },
		markReady: func(string) error { return nil },
	}
}
```

Add table cases proving a raw disk is delegated, a `/dev/zvol/...` disk leaves the host node unchanged, checkpoint traversal rejects symlinks, and retry after a setup failure repeats safely.

- [ ] **Step 2: Run transition tests and verify RED**

```bash
mise exec -- go test ./pkg/catch -run 'Test.*VMJailerTransition' -count=1
```

Expected: compile failure for the new plan and dependency types.

- [ ] **Step 3: Implement the pure transition plan**

Use database paths rather than default directories:

```go
type vmJailerTransitionInput struct {
	DataRoot    string
	Service     string
	ServiceRoot string
}

type vmJailerTransitionPlan struct {
	Service    string
	Root       string
	Disk       string
	Runtime    VMConsoleProxyConfig
	Identity   vmRuntimeIdentity
	Network    vmNetworkPlan
	Jail       vmJailPlan
}

type vmJailerTransitionDeps struct {
	validate        func(context.Context, vmJailerTransitionPlan) error
	cleanupJail     func(vmJailPlan) error
	delegateStorage func(string, string, vmRuntimeIdentity) error
	runNetwork      func([][]string, vmNetworkCommandMode) error
	markReady       func(string) error
}
```

`newVMJailerTransitionPlan` must resolve the service, assert VM type, derive Firecracker and jailer beside `VM.Image.RootFS`, use `VM.Disk.Path` with the image rootfs fallback, use the stored API/vsock/console sockets, set `JailerBase` from `vmJailerBaseForDataRoot(input.DataRoot)`, set TAP ownership with `WithTapOwner(identity)`, and build the jail plan so config, disk, checkpoint, socket, and trusted-path errors happen before mutation.

- [ ] **Step 4: Implement the ordered transaction**

```go
func executeVMJailerTransition(ctx context.Context, plan vmJailerTransitionPlan, deps vmJailerTransitionDeps) error {
	if err := deps.validate(ctx, plan); err != nil {
		return err
	}
	if err := deps.cleanupJail(plan.Jail); err != nil {
		return fmt.Errorf("clean stale VM jail: %w", err)
	}
	if err := deps.delegateStorage(plan.Root, plan.Disk, plan.Identity); err != nil {
		return err
	}
	if err := deps.runNetwork(plan.Network.CleanupCommands(), vmNetworkCommandModeCleanup); err != nil {
		return fmt.Errorf("remove pre-jailer VM network: %w", err)
	}
	if err := deps.runNetwork(plan.Network.SetupCommands(), vmNetworkCommandModeSetup); err != nil {
		return fmt.Errorf("create jailed VM network: %w", err)
	}
	return deps.markReady(plan.Root)
}
```

The default validator runs `validateVMJailerRuntimePair` and re-runs `buildVMJailPlan` without mounting. The default network runner uses the existing idempotent command runner and cleanup semantics.

- [ ] **Step 5: Route pending and ready VMs correctly**

Change both server entry points to pass this struct:

```go
type vmNetworkEnsureInput struct {
	DataRoot    string
	Service     string
	ServiceRoot string
}
```

Inside `ensureVMNetworkFromDataView`, always ensure `yeet-vm`, read readiness, and branch only on transition work:

```go
identity, err := vmNetworkEnsureRuntimeIdentity()
if err != nil {
	return err
}
readiness, err := vmJailerReadinessForRoot(input.ServiceRoot)
if err != nil {
	return err
}
if readiness == vmJailerPendingRestart {
	plan, err := newVMJailerTransitionPlan(dv, vmJailerTransitionInput(input), identity)
	if err != nil {
		return err
	}
	return executeVMJailerTransition(ctx, plan, defaultVMJailerTransitionDeps())
}
plan, err := vmNetworkPlanForVMService(dv, input.Service)
if err != nil {
	return err
}
return ensureOwnedVMNetwork(plan.WithTapOwner(identity), input.Service)
```

Add the ready fast-path helper beside the existing lifecycle runner:

```go
func ensureOwnedVMNetwork(plan vmNetworkPlan, service string) error {
	setup, err := vmNetworkSetupCommands(plan)
	if err != nil {
		return err
	}
	return runVMNetworkLifecycleCommands(setup, nil, fmt.Sprintf("ensure VM network for %q", service))
}
```

Make reconciliation always transform every VM plan with the single resolved runtime identity; delete readiness checks from the desired-state builder.

- [ ] **Step 6: Make checkpoint ownership unconditional**

Replace `delegateVMCheckpointDirIfJailed` with:

```go
func delegateVMCheckpointDir(dir string) error {
	identity, err := vmSnapshotEnsureRuntimeIdentity()
	if err != nil {
		return err
	}
	if err := vmSnapshotChown(dir, identity.UID, identity.GID); err != nil {
		return fmt.Errorf("delegate VM checkpoint directory %s: %w", dir, err)
	}
	return nil
}
```

Call it immediately after `os.MkdirTemp` for every full checkpoint.

- [ ] **Step 7: Run transition, network, and snapshot tests**

```bash
mise exec -- go test ./pkg/catch -run 'Test.*(VMJailerTransition|EnsureVMNetwork|VMNetwork.*TapOwner|VMCheckpointDir)' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit the transition checkpoint**

```bash
but commit codex/firecracker-jailer-only -m "catch: transition pending VMs before jailer launch"
```

---

### Task 4: Remove the Mode Flag and Simplify VM Settings and Info

**Files:**

- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `pkg/catch/vm_resize.go`
- Modify: `pkg/catch/vm_resize_test.go`
- Modify: `pkg/catch/service_info.go`
- Modify: `pkg/catch/service_info_test.go`
- Modify: `pkg/catchrpc/types.go`
- Modify: `pkg/catchrpc/types_test.go`
- Modify: `pkg/yeet/info_cmd.go`
- Modify: `pkg/yeet/info_cmd_test.go`

**Interfaces:**

- Removes: `cli.VMSetFlags.VMMIsolation`, `vmSetFlagsParsed.VMMIsolation`, isolation fields in `vmSettingsPlan`, and every mode apply/restore helper.
- Preserves: `catchrpc.ServiceVM.VMMIsolation string` with JSON key `vmmIsolation`.
- Consumes: `vmJailerReadinessForRoot` for the two stable info values.

- [ ] **Step 1: Change parser tests to reject the removed flag**

Remove the accepted isolation row and assert normal unknown-flag behavior:

```go
func TestParseVMSetRejectsRemovedVMMIsolation(t *testing.T) {
	_, _, err := ParseVMSet([]string{"devbox", "--vmm-isolation=jailer"})
	if err == nil || !strings.Contains(err.Error(), "unknown flag --vmm-isolation") {
		t.Fatalf("error = %v", err)
	}
}
```

Update the expected `vm set` usage and examples so the final option is `--macvlan-mac=MAC`. Remove `--vmm-isolation` from the flag-spec list.

- [ ] **Step 2: Add pending-info tests**

```go
// Append this assertion to TestServiceInfoIncludesVMConfig after its ready-state checks.
if err := os.Remove(vmJailerReadinessMarkerPath(serviceRoot)); err != nil {
	t.Fatal(err)
}
pendingResp, err := server.serviceInfo("devbox")
if err != nil {
	t.Fatal(err)
}
if got := pendingResp.Info.VM.VMMIsolation; got != string(vmJailerPendingRestart) {
	t.Fatalf("pending VMM isolation = %q", got)
}

func TestFormatVMMIsolation(t *testing.T) {
	tests := map[string]string{
		"jailer": "jailer",
		"jailer-pending-restart": "jailer (pending restart)",
	}
	for input, want := range tests {
		if got := formatVMMIsolation(input); got != want {
			t.Fatalf("formatVMMIsolation(%q) = %q, want %q", input, got, want)
		}
	}
}
```

- [ ] **Step 3: Run CLI and info tests and verify RED**

```bash
mise exec -- go test ./pkg/cli ./pkg/catch ./pkg/catchrpc ./pkg/yeet -run 'Test.*(VMMIsolation|PendingJailer|PendingRestart|ParseVMSet)' -count=1
```

Expected: old flag acceptance and `legacy-root` status expectations fail.

- [ ] **Step 4: Remove mode syntax and state transitions**

Delete the parser field, struct field, normalization, validation, help, and `hasVMSetChange` clause. In `vm_resize.go`, remove `OldIsolationMode`, `NewIsolationMode`, `IsolationChanged`, unit backup fields, jail cleanup fields, `applyVMIsolationSettings`, `requestedVMIsolationMode`, `applyVMIsolationIdentity`, `requireVMIsolationJailer`, `planVMIsolationSystemdUnit`, `prepareVMServiceIsolationTransition`, `applyVMServiceIsolationSettings`, and `restoreVMServiceIsolationSettings`.

Resolve the runtime identity once in `baseVMSettingsPlan` and make both network plans owned:

```go
identity, err := vmServiceSetEnsureRuntimeIdentity()
if err != nil {
	return nil, nil, vmSettingsPlan{}, err
}
oldNetwork := vmNetworkPlanFromDB(name, oldVM.Networks).WithTapOwner(identity)
return dv, service, vmSettingsPlan{
	Service: name,
	Root: root,
	OldVM: oldVM,
	NewCPUs: oldVM.CPUs,
	NewMemoryBytes: oldVM.MemoryBytes,
	NewBalloon: oldVM.Balloon,
	NewDiskBytes: oldVM.Disk.Bytes,
	OldNetwork: oldNetwork,
	NewNetwork: oldNetwork,
	SvcNetwork: cloneSvcNetwork(service.SvcNetwork),
	FirecrackerConfigPath: filepath.Join(serviceRunDirForRoot(root), "firecracker.json"),
}, nil
```

Remove isolation rollback branches from `vmSettingsApplyResult`; disk, network, metadata, and Firecracker config rollback remain unchanged.

- [ ] **Step 5: Emit stable machine values and friendly plain text**

In `serviceInfoWithContext`, replace mode lookup with readiness lookup and pass `string(readiness)` to `serviceVMInfo`. Keep the RPC field for backward-compatible JSON shape. Render the pending value only at the plain-text boundary:

```go
func formatVMMIsolation(value string) string {
	switch strings.TrimSpace(value) {
	case "jailer-pending-restart":
		return "jailer (pending restart)"
	case "jailer":
		return "jailer"
	default:
		return strings.TrimSpace(value)
	}
}
```

Use `formatVMMIsolation(vm.VMMIsolation)` in `vmInfoRows`.

- [ ] **Step 6: Run parser, settings, RPC, and info tests**

```bash
mise exec -- go test ./pkg/cli ./pkg/catch ./pkg/catchrpc ./pkg/yeet -run 'Test.*(ParseVMSet|VMServiceSettings|ServiceVMInfo|VMInfoRows|ServiceVMJSON)' -count=1
```

Expected: PASS, and `rg -n 'legacy-root|vmm-isolation|VMMIsolation' pkg/cli pkg/catch/vm_resize.go` returns no matches.

- [ ] **Step 7: Commit the public-interface checkpoint**

```bash
but commit codex/firecracker-jailer-only -m "vm: remove selectable VMM isolation mode"
```

---

### Task 5: Resolve and Backfill Trusted Jailers During Upgrade

**Files:**

- Create: `pkg/catch/vm_jailer_upgrade.go`
- Create: `pkg/catch/vm_jailer_upgrade_test.go`
- Modify: `pkg/catch/vm_image.go`
- Modify: `pkg/catch/vm_image_test.go`

**Interfaces:**

- Produces: internal `planVMJailerUpgrade(context.Context, *Config, vmJailerUpgradeDeps) (vmJailerUpgradePlan, error)` and `resolveVMUpgradeJailer(context.Context, vmJailerUpgradeVM, vmJailerUpgradeDeps) (string, string, error)`.
- Reuses: `vmImageCache.FetchCatalog`, `vmImageCatalog.ImageByPayload`, `vmImageCache.fetchValidatedManifest`, `vmImageCache.ensureArtifact`, checksum verification, and `validateVMJailerRuntimePair`.
- Side effect allowed during prepare: a verified root-owned `0755` jailer may be atomically installed beside an old Firecracker binary.

- [ ] **Step 1: Write resolver tests for all source tiers**

Create table cases with injected filesystem and HTTP fixtures:

```go
func TestResolveVMUpgradeJailer(t *testing.T) {
	tests := []struct {
		name string
		configure func(*vmJailerUpgradeDeps)
		wantSource string
		wantErr string
	}{
		{name: "valid sibling", configure: func(d *vmJailerUpgradeDeps) {
			d.sibling = func(context.Context, vmJailerUpgradeVM) (string, bool, error) { return "/images/v1/jailer", true, nil }
		}, wantSource: "sibling"},
		{name: "verified managed cache", configure: func(d *vmJailerUpgradeDeps) {
			d.cached = func(context.Context, vmJailerUpgradeVM) (vmJailerCandidate, bool, error) { return vmJailerCandidate{Path: "/cache/v2/jailer"}, true, nil }
		}, wantSource: "cache"},
		{name: "official manifest downloads only jailer", configure: func(*vmJailerUpgradeDeps) {}, wantSource: "remote"},
		{name: "mismatched version fails", configure: func(d *vmJailerUpgradeDeps) {
			d.sibling = func(context.Context, vmJailerUpgradeVM) (string, bool, error) { return "", true, errors.New("jailer version does not match") }
		}, wantErr: "does not match"},
		{name: "custom image without jailer fails", configure: func(d *vmJailerUpgradeDeps) {
			d.localPayload = func(string) bool { return true }
		}, wantErr: "re-import"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vm := vmJailerUpgradeVM{Service: "devbox", Payload: "vm://ubuntu/26.04", Firecracker: "/images/v1/firecracker", Jailer: "/images/v1/jailer"}
			deps := vmJailerUpgradeDeps{
				sibling: func(context.Context, vmJailerUpgradeVM) (string, bool, error) { return "", false, nil },
				cached: func(context.Context, vmJailerUpgradeVM) (vmJailerCandidate, bool, error) { return vmJailerCandidate{}, false, nil },
				localPayload: func(string) bool { return false },
				official: func(context.Context, vmJailerUpgradeVM) (vmJailerCandidate, error) { return vmJailerCandidate{Path: "/stage/jailer"}, nil },
				install: func(_ context.Context, vm vmJailerUpgradeVM, _ vmJailerCandidate) (string, error) { return filepath.Join(filepath.Dir(vm.Firecracker), "jailer"), nil },
			}
			tt.configure(&deps)
			path, source, err := resolveVMUpgradeJailer(context.Background(), vm, deps)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) { t.Fatalf("error = %v", err) }
				return
			}
			if err != nil || source != tt.wantSource || path != filepath.Join(filepath.Dir(vm.Firecracker), "jailer") {
				t.Fatalf("path, source, error = %q, %q, %v", path, source, err)
			}
		})
	}
}
```

The remote fixture records requested URLs and asserts that neither the rootfs nor kernel URL was requested.

- [ ] **Step 2: Run resolver tests and verify RED**

```bash
mise exec -- go test ./pkg/catch -run 'TestResolveVMUpgradeJailer|TestPlanVMJailerUpgradeInventory' -count=1
```

Expected: compile failure for the upgrade types.

- [ ] **Step 3: Implement inventory from the database**

Define the focused records:

```go
type vmJailerUpgradeVM struct {
	Service      string
	Payload      string
	ImageVersion string
	Architecture string
	ServiceRoot  string
	Disk         string
	Firecracker  string
	Jailer       string
	UnitPath     string
	UnitContent  []byte
	Readiness    vmJailerReadiness
	Running      bool
}

type VMJailerUpgradeSummary struct {
	Ready          []string
	PendingRestart []string
}

type vmJailerUpgradePlan struct {
	VMs     []vmJailerUpgradeVM
	Summary VMJailerUpgradeSummary
}

type vmJailerCandidate struct {
	Path         string
	ArtifactName string
	SHA256       string
	Architecture string
}

type vmJailerUpgradeDeps struct {
	sibling      func(context.Context, vmJailerUpgradeVM) (string, bool, error)
	cached       func(context.Context, vmJailerUpgradeVM) (vmJailerCandidate, bool, error)
	localPayload func(string) bool
	official     func(context.Context, vmJailerUpgradeVM) (vmJailerCandidate, error)
	install      func(context.Context, vmJailerUpgradeVM, vmJailerCandidate) (string, error)
	readiness    func(string) (vmJailerReadiness, error)
	isRunning    func(*Server, string) (bool, error)
	renderUnit   func(vmSystemdConfig) (string, error)
}
```

`planVMJailerUpgrade` enumerates only valid VM services. Resolve each effective root through `serviceRootFromConfig`, use its stored image and disk paths, derive runtime siblings, use `cfg.RootDir` for image cache and jailer base, determine running state through the existing read-only service-status path, resolve or backfill the trusted jailer, and render the replacement unit through the mandatory renderer. Sort services and summary names for deterministic output. Planning must never call systemctl mutation commands.

- [ ] **Step 4: Implement the trusted resolver and atomic backfill**

Use this source order:

```go
func resolveVMUpgradeJailer(ctx context.Context, vm vmJailerUpgradeVM, deps vmJailerUpgradeDeps) (string, string, error) {
	if path, ok, err := deps.sibling(ctx, vm); err != nil {
		return "", "", err
	} else if ok {
		return path, "sibling", nil
	}
	if candidate, ok, err := deps.cached(ctx, vm); err != nil {
		return "", "", err
	} else if ok {
		path, err := deps.install(ctx, vm, candidate)
		return path, "cache", err
	}
	if !strings.HasPrefix(vm.Payload, vmImagePayloadPrefix) || deps.localPayload(vm.Payload) {
		return "", "", fmt.Errorf("VM %q has no trusted jailer for Firecracker %s; re-import the custom image with a matching jailer", vm.Service, vm.ImageVersion)
	}
	candidate, err := deps.official(ctx, vm)
	if err != nil {
		return "", "", fmt.Errorf("VM %q: refresh the official VM image cache: %w", vm.Service, err)
	}
	path, err := deps.install(ctx, vm, candidate)
	return path, "remote", err
}
```

`cachedCandidate` reads manifest-declared jailers only, validates manifest checksums and architecture, and probes the candidate Firecracker/jailer release against the target Firecracker. `fetchOfficialJailer` fetches the trusted catalog and current family manifest, then calls `ensureArtifact` only for `manifest.Jailer` into upgrade staging. `installUpgradeJailer` verifies the source checksum again, copies to a temp file in the target directory, applies owner `0:0` and mode `0755`, renames atomically to `jailer`, and calls the normal trusted pair validator on the installed target.

- [ ] **Step 5: Run resolver and inventory tests**

```bash
mise exec -- go test ./pkg/catch -run 'Test(ResolveVMUpgradeJailer|PlanVMJailerUpgradeInventory|InstallUpgradeJailer)' -count=1
```

Expected: PASS. The remote-only-jailer test records one artifact download.

- [ ] **Step 6: Commit the resolver checkpoint**

```bash
but commit codex/firecracker-jailer-only -m "catch: backfill matching jailers during upgrade"
```

---

### Task 6: Transactionally Rewrite VM Units Around Catch Installation

**Files:**

- Modify: `pkg/catch/vm_jailer_upgrade.go`
- Modify: `pkg/catch/vm_jailer_upgrade_test.go`
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/installer_file_test.go`
- Modify: `cmd/catch/catch.go`
- Modify: `cmd/catch/catch_test.go`

**Interfaces:**

- Produces: `(*VMJailerUpgrade).Commit() error`, `Close() error`, and `Summary() VMJailerUpgradeSummary`.
- Adds: `(*FileInstaller).RollbackInstalledGeneration() error` and the same method to `catchServiceInstaller`.
- Ordering: prepare and stage VM units, install Catch, commit VM units with one daemon reload, roll back both surfaces if unit commit fails.

- [ ] **Step 1: Write no-restart, rollback, and installer-order tests**

```go
func TestVMJailerUpgradeCommitNeverRestartsVMs(t *testing.T) {
	dir := t.TempDir()
	var units []vmJailerUnitReplacement
	for _, service := range []string{"alpha", "beta", "gamma"} {
		live := filepath.Join(dir, "yeet-vm-"+service+".service")
		staged := live + ".staged"
		if err := os.WriteFile(live, []byte("old-"+service), 0o644); err != nil { t.Fatal(err) }
		if err := os.WriteFile(staged, []byte("new-"+service), 0o644); err != nil { t.Fatal(err) }
		units = append(units, vmJailerUnitReplacement{Service: service, Path: live, Staged: staged, Previous: []byte("old-"+service), Existed: true})
	}
	var calls [][]string
	tx := &VMJailerUpgrade{units: units, deps: vmJailerUpgradeDeps{
		rename: os.Rename,
		writeUnit: writeVMSystemdUnitAtomic,
		remove: os.Remove,
		systemctl: func(args ...string) error { calls = append(calls, append([]string(nil), args...)); return nil },
	}}
	if err := tx.Commit(); err != nil { t.Fatal(err) }
	if !reflect.DeepEqual(calls, [][]string{{"daemon-reload"}}) {
		t.Fatalf("systemctl calls = %v", calls)
	}
}

type fakeCatchVMJailerUpgrade struct {
	commit func() error
	close func() error
	summary catch.VMJailerUpgradeSummary
}

func (f *fakeCatchVMJailerUpgrade) Commit() error { return f.commit() }
func (f *fakeCatchVMJailerUpgrade) Close() error {
	if f.close == nil { return nil }
	return f.close()
}
func (f *fakeCatchVMJailerUpgrade) Summary() catch.VMJailerUpgradeSummary { return f.summary }

func TestDoInstallOrdersJailerUpgradeAroundCatchInstall(t *testing.T) {
	var got []string
	ts := &fakeInstallTSNet{}
	inst := &fakeCatchInstaller{closeHook: func() { got = append(got, "install-catch") }}
	deps := catchInstallDeps{
		writeInstallMeta: func(string) error { return nil },
		initTSNet: func(string) (installTSNet, error) { return ts, nil },
		newInstaller: func(*catch.Config, catch.FileInstallerCfg) (catchServiceInstaller, error) { return inst, nil },
		executable: func() (string, error) { return "/tmp/catch-bin", nil },
		readFile: func(string) ([]byte, error) { return []byte("binary"), nil },
		logf: func(string, ...any) {},
		tsnetHost: func() string { return "catch-test" },
	}
	deps.prepareVMJailerUpgrade = func(context.Context, *catch.Config) (catchVMJailerUpgrade, error) {
		got = append(got, "prepare-vm-units")
		return &fakeCatchVMJailerUpgrade{commit: func() error { got = append(got, "commit-vm-units"); return nil }}, nil
	}
	if err := doInstallWith(&catch.Config{}, t.TempDir(), deps); err != nil { t.Fatal(err) }
	want := []string{"prepare-vm-units", "install-catch", "commit-vm-units"}
	if !reflect.DeepEqual(got, want) { t.Fatalf("order = %v, want %v", got, want) }
}
```

Extend the existing `fakeCatchInstaller` with `closeHook func()` and `rollbackHook func() error`; invoke them from `Close` and `RollbackInstalledGeneration`. Add a unit-rename failure test by making the second `rename` call fail; assert the first live unit is restored byte-for-byte, `daemon-reload` follows restoration, and `rollbackHook` is called once. Add a Catch-install failure case with `closeErr` set; assert the fake upgrade's `Commit` closure is never called and its `Close` closure observes that staged temp files were removed.

- [ ] **Step 2: Run transaction tests and verify RED**

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -run 'Test.*(VMJailerUpgradeCommit|DoInstall.*JailerUpgrade|RollbackInstalledGeneration)' -count=1
```

Expected: compile failure for transaction and rollback interfaces.

- [ ] **Step 3: Stage and atomically commit units**

Store each unit's original bytes and existence plus a temp file created in the live unit directory:

```go
type vmJailerUnitReplacement struct {
	Service  string
	Path     string
	Staged   string
	Previous []byte
	Existed  bool
}

type VMJailerUpgrade struct {
	units   []vmJailerUnitReplacement
	summary VMJailerUpgradeSummary
	deps    vmJailerUpgradeDeps
	closed  bool
	committed bool
}

// Extend the resolver dependencies from Task 5 with unit transaction hooks.
// Production defaults are os.Rename, writeVMSystemdUnitAtomic, os.Remove,
// and runVMSystemctl.
type vmJailerUpgradeDeps struct {
	sibling      func(context.Context, vmJailerUpgradeVM) (string, bool, error)
	cached       func(context.Context, vmJailerUpgradeVM) (vmJailerCandidate, bool, error)
	localPayload func(string) bool
	official     func(context.Context, vmJailerUpgradeVM) (vmJailerCandidate, error)
	install      func(context.Context, vmJailerUpgradeVM, vmJailerCandidate) (string, error)
	readiness    func(string) (vmJailerReadiness, error)
	isRunning    func(*Server, string) (bool, error)
	renderUnit   func(vmSystemdConfig) (string, error)
	rename       func(string, string) error
	writeUnit    func(string, []byte, os.FileMode) error
	remove       func(string) error
	systemctl    func(...string) error
}

func PrepareVMJailerUpgrade(ctx context.Context, cfg *Config) (*VMJailerUpgrade, error) {
	deps := defaultVMJailerUpgradeDeps()
	plan, err := planVMJailerUpgrade(ctx, cfg, deps)
	if err != nil {
		return nil, err
	}
	tx := &VMJailerUpgrade{summary: plan.Summary, deps: deps}
	for _, vm := range plan.VMs {
		replacement, err := stageVMJailerUnit(vm, deps)
		if err != nil {
			_ = tx.Close()
			return nil, err
		}
		tx.units = append(tx.units, replacement)
	}
	return tx, nil
}

func stageVMJailerUnit(vm vmJailerUpgradeVM, _ vmJailerUpgradeDeps) (vmJailerUnitReplacement, error) {
	replacement := vmJailerUnitReplacement{Service: vm.Service, Path: vm.UnitPath}
	raw, err := os.ReadFile(vm.UnitPath)
	if err == nil {
		replacement.Previous = raw
		replacement.Existed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return replacement, fmt.Errorf("read VM unit %s: %w", vm.UnitPath, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(vm.UnitPath), "."+filepath.Base(vm.UnitPath)+".jailer-*")
	if err != nil {
		return replacement, err
	}
	replacement.Staged = tmp.Name()
	if _, err := tmp.Write(vm.UnitContent); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return replacement, err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return replacement, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return replacement, err
	}
	return replacement, nil
}

func (tx *VMJailerUpgrade) Commit() error {
	if tx.committed {
		return nil
	}
	if len(tx.units) == 0 {
		tx.committed = true
		return nil
	}
	for i := range tx.units {
		if err := tx.deps.rename(tx.units[i].Staged, tx.units[i].Path); err != nil {
			return tx.rollbackUnits(i, fmt.Errorf("replace VM unit %s: %w", tx.units[i].Path, err))
		}
	}
	if err := tx.deps.systemctl("daemon-reload"); err != nil {
		return tx.rollbackUnits(len(tx.units), err)
	}
	tx.committed = true
	return nil
}

func (tx *VMJailerUpgrade) rollbackUnits(applied int, cause error) error {
	var rollbackErr error
	for i := applied - 1; i >= 0; i-- {
		unit := tx.units[i]
		if unit.Existed {
			rollbackErr = errors.Join(rollbackErr, tx.deps.writeUnit(unit.Path, unit.Previous, 0o644))
		} else if err := tx.deps.remove(unit.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErr = errors.Join(rollbackErr, err)
		}
	}
	rollbackErr = errors.Join(rollbackErr, tx.deps.systemctl("daemon-reload"))
	return errors.Join(cause, rollbackErr)
}

func (tx *VMJailerUpgrade) Close() error {
	if tx.closed {
		return nil
	}
	tx.closed = true
	var retErr error
	for _, unit := range tx.units {
		if err := tx.deps.remove(unit.Staged); err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = errors.Join(retErr, err)
		}
	}
	return retErr
}

func (tx *VMJailerUpgrade) Summary() VMJailerUpgradeSummary {
	return VMJailerUpgradeSummary{
		Ready: append([]string(nil), tx.summary.Ready...),
		PendingRestart: append([]string(nil), tx.summary.PendingRestart...),
	}
}
```

`PrepareVMJailerUpgrade` calls `planVMJailerUpgrade`, which has already resolved and validated every jailer, before staging any unit. It writes every rendered unit to a root-owned `0644` temp file beside the live path. `Commit` renames staged files in sorted order, calls only `systemctl daemon-reload`, and on any error restores all previously replaced paths with `writeVMSystemdUnitAtomic`, removes paths that did not previously exist, reloads systemd, and joins rollback errors with the original error. `Close` removes uncommitted temp files and is idempotent.

- [ ] **Step 4: Add Catch-generation rollback**

Capture the pre-install generation when `NewFileInstaller` resolves `existingService`, then expose:

```go
func (i *FileInstaller) RollbackInstalledGeneration() error {
	if !i.existingService.Valid() || i.existingService.Generation() <= 0 {
		return fmt.Errorf("no previous Catch generation to restore")
	}
	installer, err := i.s.NewInstaller(i.cfg.InstallerCfg)
	if err != nil {
		return err
	}
	return installer.InstallGen(i.existingService.Generation())
}
```

The test fixture must prove the previous artifact generation is reinstalled and Catch is restarted through the existing systemd install path. This method is used only after a successful new Catch install followed by a failed VM-unit commit.

- [ ] **Step 5: Wire the transaction into `doInstallWith`**

Extend dependencies with the package-level preparer and explicitly close the installer so ordering is observable:

```go
type catchVMJailerUpgrade interface {
	Commit() error
	Close() error
	Summary() catch.VMJailerUpgradeSummary
}

// Add to catchInstallDeps.
prepareVMJailerUpgrade func(context.Context, *catch.Config) (catchVMJailerUpgrade, error)

// Add to catchServiceInstaller.
RollbackInstalledGeneration() error

upgrade, err := deps.prepareVMJailerUpgrade(context.Background(), cfg)
if err != nil {
	return err
}
defer func() { err = errors.Join(err, upgrade.Close()) }()

inst, err := deps.newInstaller(cfg, catchFileInstallerConfig(plan))
if err != nil {
	return fmt.Errorf("failed to create installer: %w", err)
}
if err := writeCurrentExecutable(inst, deps); err != nil {
	_ = inst.Close()
	return err
}
if err := inst.Close(); err != nil {
	return err
}
if err := upgrade.Commit(); err != nil {
	rollbackErr := inst.RollbackInstalledGeneration()
	return errors.Join(err, rollbackErr)
}
summary := upgrade.Summary()
deps.logf("VM jailer upgrade: %d ready, %d pending restart", len(summary.Ready), len(summary.PendingRestart))
return nil
```

Set the production default and normalization fallback exactly once:

```go
prepareVMJailerUpgrade: func(ctx context.Context, cfg *catch.Config) (catchVMJailerUpgrade, error) {
	return catch.PrepareVMJailerUpgrade(ctx, cfg)
},
```

Ensure the real `FileInstaller.Close` remains idempotent so failure cleanup cannot install twice. Initial installs and hosts without VMs use an empty transaction and still succeed without calling `daemon-reload`.

- [ ] **Step 6: Run installer and transaction tests**

```bash
mise exec -- go test ./pkg/catch ./cmd/catch -run 'Test.*(VMJailerUpgrade|DoInstall|FileInstaller.*Rollback)' -count=1
```

Expected: PASS, with tests asserting no VM `stop`, `start`, `restart`, or `try-restart` systemctl calls.

- [ ] **Step 7: Commit the upgrade transaction checkpoint**

```bash
but commit codex/firecracker-jailer-only -m "catch: migrate VM units on upgrade without restart"
```

---

### Task 7: Regression Coverage, Evergreen Docs, and Obsolete Design Removal

**Files:**

- Modify: `pkg/catch/vm_provision_test.go`
- Modify: `pkg/catch/recovery_vm_test.go`
- Modify: `pkg/catch/vm_kernel_sync_test.go`
- Modify: `pkg/catch/host_storage_db_rewrite_test.go`
- Modify: `pkg/catch/remove_test.go`
- Modify: `pkg/catch/vm_console_test.go`
- Modify: `pkg/catch/vm_snapshot_test.go`
- Modify: `README.md`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/payloads/vms.mdx`
- Modify: `.codex/skills/yeet-cli/references/yeet-help-agent.md`
- Delete: `docs/superpowers/specs/2026-07-18-firecracker-jailer-design.md`
- Delete: `docs/superpowers/plans/2026-07-18-firecracker-jailer.md`
- Modify: root `website` gitlink after the website commit is pushed

**Interfaces:**

- Verifies: provisioning, full snapshot/restore, reboot, console, API/vsock, kernel sync, host-storage movement, removal, and custom/ZFS roots retain canonical paths under mandatory jailer.
- Documents: one invariant, one automatic account story, and one focused upgrade compatibility note.

- [ ] **Step 1: Add cross-feature regression assertions**

For each existing test fixture, assert the generated or regenerated unit contains both jailer arguments and never contains a direct `ExecStart=<firecracker>` form. Add this helper and call it from provisioning, recovery, and host-storage tests:

```go
func assertJailerOnlyVMUnit(t *testing.T, unit string) {
	t.Helper()
	for _, want := range []string{" vm-run ", " --jailer ", " --jailer-base "} {
		if !strings.Contains(unit, want) { t.Fatalf("unit missing %q:\n%s", want, unit) }
	}
	if strings.Contains(unit, "ExecStart=/") && strings.Contains(unit, "firecracker --api-sock") {
		t.Fatalf("unit directly launches Firecracker:\n%s", unit)
	}
}
```

Retain existing snapshot path, full restore request, guest reboot kernel sync, console, balloon, API, vsock, SSH metadata, and removal assertions. Update marker expectations to `vmJailerReady` or `vmJailerPendingRestart`; no test may expect `legacy-root`.

- [ ] **Step 2: Run the VM regression slice**

```bash
mise exec -- go test ./pkg/catch -run 'Test.*(VMProvision|VMRecovery|VMKernel|VMSnapshot|VMRestore|VMConsole|HostStorage.*VM|Remove.*VM)' -count=1
```

Expected: PASS.

- [ ] **Step 3: Rewrite README and manual prose as current fact**

Use this steady-state wording in README and the VM manual:

```markdown
Yeet launches Firecracker through the matching Firecracker jailer. Catch
prepares the VM's host resources as root, and the jailer runs the VMM as the
static, non-login `yeet-vm` host account. This host account is separate from
the VM guest login user and from native-service `--run-as` identities.
```

Document that Yeet creates `yeet-vm` automatically on the first VM preparation or during an upgrade that finds VMs. State that custom data roots, custom service roots, and ZFS-backed VM storage remain supported because paths are derived from stored configuration.

Remove every `vm set --vmm-isolation` example and every description of `legacy-root`. In one focused upgrade paragraph, state that an already running VM is not restarted during Catch upgrade and crosses the jailer boundary on its next restart; a preparation error leaves it stopped.

- [ ] **Step 4: Update CLI reference and remove superseded internal docs**

Run the help generator after removing the flag from shared metadata:

```bash
tools/generate-yeet-help-agent.sh
```

Delete the old selectable-mode spec and plan. Keep `docs/superpowers/specs/2026-07-18-firecracker-jailer-only-design.md` and this plan.

- [ ] **Step 5: Verify the docs and forbidden vocabulary**

```bash
git -C website diff --check
git diff --check
rg -n 'legacy-root|vmm-isolation' README.md website/docs .codex/skills/yeet-cli/references
rg -n 'vmIsolationLegacy|vmFirecrackerCommand|flag:"vmm-isolation"|--vmm-isolation' pkg cmd -g '!**/*_test.go'
rg -n 'root@pve1|yeet-pve1|/Users/' README.md website/docs
```

Expected: both diff checks pass; the runtime-code search returns no matches; the public-doc search returns no matches except historical changelog entries if a previously published release already contains the terms; the private-info search returns no matches. Do not rewrite old changelog history.

- [ ] **Step 6: Commit and push the website repository**

From `website/`, verify only the VM and CLI manual files changed, then use the permitted focused website-repository flow:

```bash
git -C website add docs/cli/yeet-cli.mdx docs/payloads/vms.mdx
git -C website commit -m "docs: describe jailer-only VM runtime"
git -C website push origin HEAD:main
```

Verify `git -C website status --short --branch` is clean and the pushed SHA is advertised by the website remote.

- [ ] **Step 7: Commit root docs, tests, generated help, deletions, and gitlink**

```bash
but commit codex/firecracker-jailer-only -m "docs: make Firecracker jailer the only runtime"
```

---

### Task 8: Repository Gates and Branch Review

**Files:** all files owned by Tasks 1-7 only.

**Interfaces:**

- Verifies the exact jailer branch independently from the applied GitButler workspace.

- [ ] **Step 1: Run targeted packages**

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catchrpc ./pkg/catch ./cmd/catch -count=1
```

Expected: PASS.

- [ ] **Step 2: Run the full deterministic gate**

```bash
mise exec -- go test ./... -count=1
mise exec -- pre-commit run --all-files
```

Expected: both commands exit zero.

- [ ] **Step 3: Run concurrency, fuzz, and destination quality gates**

```bash
mise run race
mise run fuzz
mise run quality:goal
```

Expected: race detector clean, all active fuzz targets complete, coverage at least 80%, zero CRAP hotspots, zero golangci findings, and the bounded mutation target at least 80%.

- [ ] **Step 4: Prove the exact branch contains no root launch path**

```bash
rg -n 'legacy-root|vmm-isolation' README.md website/docs .codex/skills/yeet-cli/references
rg -n 'vmIsolationLegacy|vmFirecrackerCommand|flag:"vmm-isolation"|--vmm-isolation' pkg cmd -g '!**/*_test.go'
rg -n 'exec\.Command(Context)?\([^\n]*firecracker' pkg/catch cmd/catch -g '!**/*_test.go'
git diff --check origin/main...HEAD
```

Expected: no supported mode or direct launch matches, with only historical changelog text exempted from the public-doc search; diff check is clean.

- [ ] **Step 5: Review and consolidate the GitButler branch**

Run `but pull --check`, `but status`, and `but diff`. Confirm every commit is based on current `origin/main`, the branch owns only this task, and shared-file changes do not contain ISO-network, host-storage, service-identity, or GitButler-skill hunks. Create a recovery snapshot before any squash:

```bash
but oplog snapshot -m "before jailer-only history cleanup"
```

Consolidate checkpoints into a clean reviewable stack or one final commit according to the finish workflow, then rerun the full Go suite and pre-commit against the exact resulting commit.

---

### Task 9: Live Upgrade, Pending-Restart Canary, and Host Cleanup

**Files:** no repository edits unless a live failure first receives an automated regression test.

**Interfaces:**

- Uses: committed `yeet init`, `yeet vm images prune`, VM status/info/SSH/console/snapshot commands, systemd, `/proc`, and the approved `root@pve1` host.
- Produces: verified live evidence that every future launch is jailed and no current VM was restarted by upgrade.

- [ ] **Step 1: Record the pre-upgrade VM and process inventory**

From the committed isolated tree, record service names, unit hashes, systemd `MainPID`, Firecracker PID/UID/GID, start time, readiness marker, image bundle, service root, disk backend, and reachability for every VM. Use `CATCH_HOST=yeet-pve1` for Yeet commands and `ssh root@pve1` only for host evidence. Save this inventory outside the public repository.

- [ ] **Step 2: Install the committed Catch build**

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet init root@pve1
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet version
```

Verify the installed binary's `go version -m` `vcs.revision` equals the committed jailer-only revision.

- [ ] **Step 3: Prove upgrade did not restart VMs**

Compare every recorded systemd `MainPID`, Firecracker PID, and process start time. Verify each live unit now contains `--jailer` and `--jailer-base`, each VM remains reachable, and no VM service received a stop/start/restart job during the install window.

- [ ] **Step 4: Exercise the pending transition with a disposable canary**

Create a uniquely named disposable catalog VM, verify normal boot and SSH, stop it, remove only its readiness marker to emulate an old VM, and confirm `yeet info` reports `jailer (pending restart)`. Start it and verify the marker is rewritten to `jailer`, the TAP is owned by `yeet-vm`, and the Firecracker PID is non-root. Exercise console, agent, SSH, guest reboot, disk snapshot, full checkpoint restore, and API/vsock paths, then remove the canary with config and data cleanup.

- [ ] **Step 5: Verify every durable VM boundary**

For every Firecracker PID, verify:

```text
UID/GID: yeet-vm and nonzero
CapEff: 0000000000000000
NoNewPrivs: 1
Seccomp: 2
Mount namespace: distinct from Catch
Network namespace: expected host namespace
TAP owner: yeet-vm
API, console, and vsock sockets: reachable through the jail links
```

Run `yeet status`, `yeet info`, and `yeet ssh <vm> -- true` for every VM and confirm no Firecracker PID has UID 0.

- [ ] **Step 6: Remove exact obsolete unit backups**

List the pre-jailer unit backup files, compare each corresponding live unit, and delete only the exact backup files after confirming the live unit is jailer-only and the VM is healthy. Report that these files are deleted and recoverable only from backups or Git history, not from the host filesystem.

- [ ] **Step 7: Prune unused pre-jailer image bundles through Yeet**

```bash
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet vm images prune --dry-run
CATCH_HOST=yeet-pve1 mise exec -- go run ./cmd/yeet vm images prune --yes
```

Review the dry run before the confirmed prune. Do not delete image directories by path. Verify no active VM references a pruned bundle and current managed bundles retain matching Firecracker and jailer versions.

- [ ] **Step 8: Perform final leak and rollback checks**

Confirm there are no stale jail mounts, socket symlinks, TAPs, Firecracker processes, or canary service records. Verify rolling back to the immediately previous jailer-capable build remains possible, and explicitly record that rolling back to a root-only build is unsupported after readiness transition.
