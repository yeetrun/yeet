# VM Shell And Terminfo Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make yeet Ubuntu VMs feel like normal Ubuntu shells in Ghostty by restoring stock Bash niceties, color defaults, and `xterm-ghostty` terminfo support.

**Architecture:** Keep this compatible with the current v6 image by repairing the guest during catch metadata injection. Fresh homes are seeded from `/etc/skel`; existing yeet-only homes receive a stronger managed block; Ghostty terminfo is compiled into the guest rootfs during metadata injection when `tic` is present on the host. The image builder gains verification so future bundles keep the same behavior baked in.

**Tech Stack:** Go metadata injection in `pkg/catch`, catch bootstrap prereq checks in `cmd/catch`, Bash startup files, ncurses `tic`/terminfo, Go unit tests, live lab-host VM test.

---

### Task 1: Shell Defaults Regression Tests

**Files:**
- Modify: `pkg/catch/vm_metadata_test.go`
- Modify: `pkg/catch/vm_metadata.go`

- [ ] **Step 1: Add failing tests**

Add tests that verify new VM metadata copies stock `/etc/skel/.bashrc` and `.profile` when the home lacks user files, preserves user-authored content, replaces old managed blocks, and injects color-friendly aliases and prompt logic.

- [ ] **Step 2: Run the tests to verify RED**

Run: `mise exec -- go test ./pkg/catch -run 'TestWriteVMGuestShellDefaults|TestWriteVMGuestMetadataFiles' -count=1`

Expected: FAIL because current metadata only writes the yeet block and does not seed `/etc/skel` or add `ls --color=auto`.

- [ ] **Step 3: Implement shell defaults**

Update `writeVMGuestShellDefaults` to seed missing home files from `/etc/skel`, replace yeet-only shell files with skel content plus the managed block, and expand `vmGuestBashRCBlock` with Ubuntu-compatible color prompt, `dircolors`, color aliases, history/checkwinsize, `lesspipe`, bash completion, and yeet hints.

- [ ] **Step 4: Run targeted tests to verify GREEN**

Run: `mise exec -- go test ./pkg/catch -run 'TestWriteVMGuestShellDefaults|TestWriteVMGuestMetadataFiles' -count=1`

Expected: PASS.

### Task 2: Ghostty Terminfo Regression Tests

**Files:**
- Modify: `pkg/catch/vm_metadata_test.go`
- Modify: `pkg/catch/vm_metadata.go`
- Modify: `cmd/catch/vm_prereqs.go`
- Modify: `cmd/catch/vm_prereqs_test.go`

- [ ] **Step 1: Add failing tests**

Add metadata tests that expect `injectVMMetadataIntoRootFSWith` to run `tic -x -o <root>/etc/terminfo <source>` and expect a missing `tic` command to be reported as package `ncurses-bin`.

- [ ] **Step 2: Run the tests to verify RED**

Run: `mise exec -- go test ./pkg/catch ./cmd/catch -run 'TestInjectVMMetadataIntoRootFSMountsAndUnmounts|TestSetupVMHost|TestInspectVMHost' -count=1`

Expected: FAIL because `tic` is not invoked and is not a VM host prereq.

- [ ] **Step 3: Implement terminfo support**

Embed the provided Ghostty terminfo source in `pkg/catch/vm_metadata.go`, write it to a temporary file during metadata injection, compile it into `<root>/etc/terminfo` with `tic -x`, and add `tic`/`ncurses-bin` to the VM host prereq set.

- [ ] **Step 4: Run targeted tests to verify GREEN**

Run: `mise exec -- go test ./pkg/catch ./cmd/catch -run 'TestInjectVMMetadataIntoRootFSMountsAndUnmounts|TestSetupVMHost|TestInspectVMHost|TestWriteVMGuestMetadataFiles' -count=1`

Expected: PASS.

### Task 3: Image Builder Guardrails

**Files:**
- Modify: `tools/vm-image/build-ubuntu-26.04.sh`

- [ ] **Step 1: Add builder checks**

Require `tic` for fast image builds and verify the customized rootfs recognizes `TERM=xterm-ghostty` with `infocmp`.

- [ ] **Step 2: Run syntax check**

Run: `bash -n tools/vm-image/build-ubuntu-26.04.sh`

Expected: PASS.

### Task 4: Full Verification And Live Test

**Files:**
- No extra source edits expected.

- [ ] **Step 1: Run local verification**

Run: `mise exec -- go test ./pkg/catch ./cmd/catch -count=1`

Run: `mise exec -- go test ./... -count=1`

Expected: PASS.

- [ ] **Step 2: Redeploy catch and live-test lab-host**

Run the updated catch on `yeet-lab`, create a fresh disposable VM, and verify:

```bash
TERM=xterm-ghostty infocmp -x xterm-ghostty >/dev/null
TERM=xterm-ghostty tput colors
bash -lic 'alias ls; alias grep; printf "%s\n" "$PS1"; mkdir -p ~/.ssh; ls -d ~/.ssh | sed -n l'
TERM=xterm-ghostty htop --version
```

Expected: `infocmp` succeeds, `tput colors` prints `256`, aliases include `--color=auto`, the prompt contains ANSI color escapes, directory listing emits color escapes, and `htop --version` succeeds under `xterm-ghostty`.

- [ ] **Step 3: Clean up disposable VM**

Run: `yeet rm <test-vm>@yeet-lab --yes --clean-data --clean-config`

Expected: service registry lookup fails afterward and no matching systemd unit or service root remains.
