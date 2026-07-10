# Upgrade VM Warning Deduplication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `yeet init` and `yeet upgrade` show expected missing-VM-capability warnings once, in the final grouped installer warning block.

**Architecture:** Keep the client VM preflight probe for installation and prompt decisions, but make it status-only when the host cannot support VMs. Leave the catch prerequisite reporter and init install filter authoritative for the final user-facing capability and missing-tool warnings.

**Tech Stack:** Go, the existing init progress UI and install filter, Go's `testing` package, the repo-managed `mise` toolchain, and GitButler.

## Global Constraints

- Apply the output fix to the shared init path so both `yeet init` and `yeet upgrade` receive it.
- Keep `Check VM tools (not available)` when architecture, KVM, or TUN capability is missing.
- Do not print a client-side capability warning after a successful preflight probe determines that VM support is unavailable.
- Keep catch's canonical capability and missing-tool warnings and the install filter's final grouped formatting unchanged.
- Keep immediate preflight-probe failure warnings unchanged.
- Keep fatal SSH, download, and installation errors unchanged.
- Keep VM LAN bridge, cleanup, and other unrelated warnings unchanged.
- Do not change catch-side code, install-filter formatting, CLI syntax, README content, or website documentation.
- Use `/opt/homebrew/bin/mise exec -- go ...` for Go commands in this checkout and do not set or manage `GOCACHE`.
- Use GitButler for version-control writes; do not push or land the branch unless the user asks.

---

## File Structure

- Modify `pkg/yeet/init.go`: replace the warning-emitting client capability check with a pure availability predicate while preserving the existing preflight control flow.
- Modify `pkg/yeet/init_test.go`: cover status-only preflight output and the combined preflight-plus-installer warning count.
- Do not modify `cmd/catch/vm_prereqs.go` or `pkg/yeet/init_install_filter.go`; they remain the authoritative warning producer and formatter.

---

### Task 1: Give the Installer Sole Ownership of VM Capability Warnings

**Files:**
- Modify: `pkg/yeet/init.go:536-665`
- Modify: `pkg/yeet/init_test.go:591-625`

**Interfaces:**
- Consumes: `initVMHostStatus` with `UnsupportedArch string`, `KVM bool`, and `TUN bool`.
- Produces: `initVMHostCapabilityUnavailable(status initVMHostStatus) bool`.
- Preserves: `prepareInitVMToolsInstall(ui *initUI, userAtRemote, goarch string, opts initOptions) (bool, error)`.

- [ ] **Step 1: Write the failing output regression tests**

Replace `TestPrepareInitVMToolsInstallWarnsForNonVMCapableHost` in `pkg/yeet/init_test.go` with the following test, then add the combined-flow test immediately after it:

```go
func TestPrepareInitVMToolsInstallMarksNonVMCapableHostUnavailableWithoutWarning(t *testing.T) {
	oldRemoteVMHostStatus := remoteVMHostStatusFn
	oldPrompt := activePrompter
	t.Cleanup(func() {
		remoteVMHostStatusFn = oldRemoteVMHostStatus
		activePrompter = oldPrompt
	})
	remoteVMHostStatusFn = func(string, string) (initVMHostStatus, error) {
		return initVMHostStatus{
			AptGet: true,
			KVM:    false,
			TUN:    true,
			MissingCommands: []vmHostCommandRequirement{
				{Command: "qemu-img", Package: "qemu-utils"},
			},
		}, nil
	}
	activePrompter = fakePrompter{err: errors.New("prompt should not run")}
	var out bytes.Buffer
	ui := newInitUI(&out, true, false, "catch", "root@example.com", catchServiceName)

	installVMTools, err := prepareInitVMToolsInstall(ui, "root@example.com", "amd64", initOptions{})
	ui.Stop()
	if err != nil {
		t.Fatalf("prepareInitVMToolsInstall error = %v", err)
	}
	if installVMTools {
		t.Fatal("installVMTools = true for non-VM-capable host")
	}
	got := out.String()
	if !strings.Contains(got, "Check VM tools (not available)") {
		t.Fatalf("output = %q, want unavailable VM-tools status", got)
	}
	if strings.Contains(got, "/dev/kvm is missing") {
		t.Fatalf("output = %q, want capability warning deferred to installer", got)
	}
}

func TestInitVMWarningsAppearOnceAcrossPreflightAndInstaller(t *testing.T) {
	oldRemoteVMHostStatus := remoteVMHostStatusFn
	oldPrompt := activePrompter
	t.Cleanup(func() {
		remoteVMHostStatusFn = oldRemoteVMHostStatus
		activePrompter = oldPrompt
	})
	remoteVMHostStatusFn = func(string, string) (initVMHostStatus, error) {
		return initVMHostStatus{
			AptGet: true,
			KVM:    false,
			TUN:    true,
			MissingCommands: []vmHostCommandRequirement{
				{Command: "qemu-img", Package: "qemu-utils"},
			},
		}, nil
	}
	activePrompter = fakePrompter{err: errors.New("prompt should not run")}
	var out bytes.Buffer
	ui := newInitUI(&out, true, false, "catch", "root@example.com", catchServiceName)

	installVMTools, err := prepareInitVMToolsInstall(ui, "root@example.com", "amd64", initOptions{})
	if err != nil {
		t.Fatalf("prepareInitVMToolsInstall error = %v", err)
	}
	if installVMTools {
		t.Fatal("installVMTools = true for non-VM-capable host")
	}

	filter := newInitInstallFilter(io.Discard)
	installerOutput := strings.Join([]string{
		"Warning: VM support is unavailable on this host: /dev/kvm is missing. Containers, binaries, and cron jobs still work. See " + hostRequirementsDocsURL,
		"Warning: VM tools are incomplete: missing qemu-img. Install packages: qemu-utils. See " + hostRequirementsDocsURL,
	}, "\n") + "\n"
	if _, err := filter.Write([]byte(installerOutput)); err != nil {
		t.Fatalf("write installer output: %v", err)
	}
	ui.Warn(filter.WarningSummary())
	ui.Stop()

	got := out.String()
	if !strings.Contains(got, "Check VM tools (not available)") {
		t.Fatalf("output = %q, want unavailable VM-tools status", got)
	}
	for text, wantCount := range map[string]int{
		"/dev/kvm is missing": 1,
		"missing qemu-img":     1,
		hostRequirementsDocsURL: 1,
	} {
		if gotCount := strings.Count(got, text); gotCount != wantCount {
			t.Fatalf("strings.Count(output, %q) = %d, want %d\n%s", text, gotCount, wantCount, got)
		}
	}
}
```

- [ ] **Step 2: Run the tests and verify the duplicate is reproduced**

Run:

```bash
/opt/homebrew/bin/mise exec -- go test ./pkg/yeet -run 'TestPrepareInitVMToolsInstallMarksNonVMCapableHostUnavailableWithoutWarning|TestInitVMWarningsAppearOnceAcrossPreflightAndInstaller' -count=1
```

Expected: FAIL. The focused test finds the immediate `/dev/kvm` warning, and the combined-flow test counts the KVM text and host-requirements URL twice.

- [ ] **Step 3: Replace the warning-emitting preflight helper with a pure predicate**

In `pkg/yeet/init.go`, change the capability branch in `prepareInitVMToolsInstall` to:

```go
	if initVMHostCapabilityUnavailable(status) {
		ui.DoneStep("not available")
		return false, nil
	}
```

Replace `warnInitVMHostCapability` with:

```go
func initVMHostCapabilityUnavailable(status initVMHostStatus) bool {
	return status.UnsupportedArch != "" || !status.KVM || !status.TUN
}
```

Do not change `formatInitVMToolsPreflightWarning`, the catch prerequisite warnings, or install-filter summary formatting.

- [ ] **Step 4: Run the focused regression tests and verify they pass**

Run:

```bash
/opt/homebrew/bin/mise exec -- go test ./pkg/yeet -run 'TestPrepareInitVMToolsInstallMarksNonVMCapableHostUnavailableWithoutWarning|TestInitVMWarningsAppearOnceAcrossPreflightAndInstaller|TestInitInstallFilterSummarizesVisibleWarningsOnce' -count=1
```

Expected: PASS. The preflight renders `Check VM tools (not available)`, while KVM, `qemu-img`, and the shared docs URL each appear once after installer output is summarized.

- [ ] **Step 5: Run package and repository verification**

Run:

```bash
/opt/homebrew/bin/mise exec -- go test ./pkg/yeet -count=1
/opt/homebrew/bin/mise exec -- go test ./... -count=1
/opt/homebrew/bin/mise exec -- pre-commit run --all-files
```

Expected: all commands exit zero. Do not proceed to the checkpoint commit if any command fails.

- [ ] **Step 6: Create the implementation checkpoint commit**

First run:

```bash
/opt/homebrew/bin/but pull --check
/opt/homebrew/bin/but diff
```

Expected: the base is current, and the only uncommitted files are `pkg/yeet/init.go` and `pkg/yeet/init_test.go`. If any unrelated file appears, leave it out and use the exact file or hunk IDs printed by `but diff` with `--changes`.

When the diff contains only those two files, run:

```bash
PATH=/opt/homebrew/bin:$PATH /opt/homebrew/bin/but commit codex/upgrade-warning-deduplication -m "init: deduplicate VM capability warnings"
```

Expected: GitButler creates the checkpoint on `codex/upgrade-warning-deduplication`, hooks pass, and no uncommitted session changes remain. Do not push or land the branch.
