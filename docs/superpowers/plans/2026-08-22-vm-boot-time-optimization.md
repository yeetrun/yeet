# VM Boot-Time Optimization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce Yeet VM time-to-SSH with measured, reversible kernel, readiness, and guest boot changes while retaining Firecracker and distribution compatibility.

**Architecture:** Establish a repeatable host-to-guest timing harness, remove the confirmed unused keyboard probe from an immutable kernel candidate, make Catch readiness event-driven, and then shorten the systemd path to SSH one dependency at a time. Publish candidates before stable promotion and use a pre-systemd SSH spike only when the measured systemd floor still exceeds the stretch target.

**Tech Stack:** Go, Rust, Bash, Linux kernel Kconfig, Firecracker, systemd, OpenSSH, GitHub Actions, GitHub Releases, jq, Ubuntu 26.04, NixOS 26.05, ZFS.

**Spec:** `docs/superpowers/specs/2026-08-22-vm-boot-time-optimization-design.md`

## Global Constraints

- Retain Firecracker; VMM replacement is outside this plan.
- Primary metric is host VM-unit start to successful public-key SSH command.
- Use at least 20 measured warm boots after one discarded warm-up; report p50, p95, minimum, maximum, and raw samples.
- Never drop global host caches on the shared canary.
- Never restart or mutate existing workload VMs; use names beginning with `yeet-bootbench-` and remove them after validation.
- Keep private hostnames, usernames, addresses, and filesystem paths out of committed files and evidence.
- Publish immutable candidate artifacts first; do not move stable catalog pointers until all acceptance gates pass.
- Preserve kernel mitigations, jailer isolation, checksums, signatures, and immutable release identity.
- Preserve Ubuntu package contracts and NixOS flake-first rebuild compatibility.
- New user-facing operation boundaries require permission classification; this plan adds none.

---

## File Structure

### Root repository (this checkout)

- Create: `scripts/vm-boot-benchmark.sh`
  - Run bounded restarts of one explicitly named disposable VM and write neutral JSON timing evidence.
- Create: `scripts/test-vm-boot-benchmark.sh`
  - Exercise timing parsing, percentile calculation, validation, redaction, and failure reporting with fixtures.
- Create: `scripts/testdata/vm-boot-benchmark/boot-complete.log`
  - Neutral journal fixture containing kernel, init, SSH, ready, and target markers.
- Create: `scripts/testdata/vm-boot-benchmark/boot-incomplete.log`
  - Neutral fixture missing SSH/readiness markers.
- Modify: `pkg/catch/vm_readiness.go`
  - Replace repeated journal polling with one cancelable journal follower while keeping vsock fallback.
- Modify: `pkg/catch/vm_readiness_test.go`
  - Cover immediate event delivery, cursor/timestamp freshness, cancellation, fallback, and timeout.
- Modify: `pkg/catch/vm_metadata.go`
  - Shorten only the proven-redundant Ubuntu `yeet-sshd.service` dependencies.
- Modify: `pkg/catch/vm_metadata_test.go`
  - Lock down the accepted early-SSH ordering and retained supervision/security settings.
- Modify conditionally: `pkg/catch/vm_boot.go`, `pkg/catch/vm_boot_test.go`
  - Add quieter console arguments only if the A/B gate shows at least 50 ms p50 improvement and preserves failure diagnostics.
- Modify conditionally: `guest/yeet-init/src/lib.rs`, `guest/yeet-init/src/main.rs`
  - Implement a disposable pre-systemd OpenSSH spike only if the systemd result misses the 1.25-second p95 gate.
- Create conditionally: `guest/yeet-init/src/early_sshd.rs`
  - Isolate spike-only process launch, readiness, and shutdown behavior from normal init preparation.

### Image repository (`../yeet-vm-images`)

- Modify: `scripts/build-linux-kernel.sh`
  - Disable i8042 and AT keyboard drivers and assert the final Kconfig state.
- Create: `scripts/verify-kernel-boot-policy.sh`
  - Verify boot-performance Kconfig invariants in an already rendered config.
- Create: `scripts/test-kernel-boot-policy.sh`
  - Prove enabled keyboard drivers fail and the intended disabled form passes.
- Modify: `.github/workflows/build-kernel.yml`
  - Run the policy verifier against the built `kernel.config` before publication.
- Modify conditionally: `scripts/build-ubuntu-26.04.sh`
  - Apply candidate-only udev masks when the post-kernel profile meets the experiment gate.
- Modify conditionally: `nixos/yeet/vm.nix`
  - Express accepted NixOS SSH ordering or udev policy through the native module.
- Modify conditionally: `scripts/test-ubuntu-fast-rootfs-policy.sh`
  - Verify accepted Ubuntu service masks and retained device/runtime behavior.
- Modify conditionally: `scripts/test-nixos-guest-base.sh`
  - Verify accepted NixOS unit ordering and rebuild contract.
- Modify as candidates are published: `kernel-catalog.json`, `guest-catalog.json`
  - Add exact candidate identities without moving stable until validation passes.
- Modify: `docs/component-release-runbook.md`
  - Add the measured boot-performance evidence fields and promotion gate.

## Interfaces

The benchmark command must expose this stable CLI:

```text
scripts/vm-boot-benchmark.sh \
  --catch-host <catch-alias> \
  --machine-host <ssh-target> \
  --service <yeet-bootbench-name> \
  --iterations <count> \
  --output <directory>
```

It produces `samples.json`, `summary.json`, and one neutralized log per boot.
`summary.json` contains these integer millisecond fields:

```json
{
  "schema_version": 1,
  "iterations": 20,
  "ssh_ms": {"min": 0, "p50": 0, "p95": 0, "max": 0},
  "ready_observation_ms": {"min": 0, "p50": 0, "p95": 0, "max": 0},
  "multi_user_ms": {"min": 0, "p50": 0, "p95": 0, "max": 0},
  "failed_boots": 0
}
```

Catch readiness adds this internal test seam:

```go
type vmGuestReadyJournalFollowFunc func(
    context.Context,
    string,
    vmGuestReadyBoundary,
    chan<- vmGuestReadyReport,
) error
```

The production implementation follows the VM unit journal until cancellation;
tests replace it with an in-memory sender. The vsock agent query remains the
fallback and does not become a component-selection trust boundary.

---

### Task 1: Add the Boot Benchmark Harness

**Files:**
- Create: `scripts/vm-boot-benchmark.sh`
- Create: `scripts/test-vm-boot-benchmark.sh`
- Create: `scripts/testdata/vm-boot-benchmark/boot-complete.log`
- Create: `scripts/testdata/vm-boot-benchmark/boot-incomplete.log`

- [ ] **Step 1: Add fixtures with explicit phase markers**

Create `boot-complete.log` with neutral monotonic timestamps and these messages:

```text
__MONOTONIC_TIMESTAMP=1000000
MESSAGE=Running Firecracker
__MONOTONIC_TIMESTAMP=1100000
MESSAGE=[    0.000000] Linux version test
__MONOTONIC_TIMESTAMP=1850000
MESSAGE=[    0.750000] Run /usr/local/lib/yeet-vm/yeet-init as init process
__MONOTONIC_TIMESTAMP=2500000
MESSAGE=Started yeet early SSH daemon.
__MONOTONIC_TIMESTAMP=2550000
MESSAGE=yeet-ready eth0 192.0.2.10
__MONOTONIC_TIMESTAMP=2600000
MESSAGE=Reached target Multi-User System.
```

Create `boot-incomplete.log` with the first three records only.

- [ ] **Step 2: Write failing fixture tests**

`scripts/test-vm-boot-benchmark.sh` must source the benchmark script in library
mode and assert:

```bash
assert_eq 1500 "$(phase_ms 1000000 ssh scripts/testdata/vm-boot-benchmark/boot-complete.log)"
assert_eq 1550 "$(phase_ms 1000000 ready scripts/testdata/vm-boot-benchmark/boot-complete.log)"
assert_eq 1600 "$(phase_ms 1000000 multi-user scripts/testdata/vm-boot-benchmark/boot-complete.log)"
assert_fails phase_ms 1000000 ssh scripts/testdata/vm-boot-benchmark/boot-incomplete.log
assert_eq 100 "$(percentile 95 10 20 30 40 50 60 70 80 90 100)"
```

Also assert that `--service production-name` is rejected, `--iterations 1` is
rejected, and generated JSON contains none of the supplied machine/catch host
strings.

- [ ] **Step 3: Run the tests and confirm failure**

Run:

```bash
bash scripts/test-vm-boot-benchmark.sh
```

Expected: fail because the benchmark functions and command do not exist.

- [ ] **Step 4: Implement bounded measurement**

Implement strict argument validation, a `YEET_VM_BOOT_BENCHMARK_LIBRARY=1`
library mode, integer nearest-rank percentile calculation, fresh journal cursor
capture, one discarded warm-up, the requested measured restarts, and JSON
rendering through `jq -n`. Require `iterations >= 5`, service prefix
`yeet-bootbench-`, and a new empty output directory.

For each measured boot:

1. Capture the host unit journal cursor.
2. Record the unit's `ExecMainStartTimestampMonotonic`.
3. Restart only `yeet-vm-<service>.service` through the explicit machine host.
4. Follow journal records after the cursor until `yeet-ready` or timeout.
5. Run `mise exec -- go run ./cmd/yeet --host=<alias> ssh <service> -- true`.
6. Capture guest `systemd-analyze time`, `critical-chain`, `blame`, and dmesg.
7. Convert phase timestamps into durations relative to the unit start.
8. Replace service names, hosts, and addresses with neutral tokens before
   writing evidence.

- [ ] **Step 5: Run deterministic harness checks**

Run:

```bash
bash -n scripts/vm-boot-benchmark.sh scripts/test-vm-boot-benchmark.sh
bash scripts/test-vm-boot-benchmark.sh
```

Expected: all checks pass without contacting a live host.

- [ ] **Step 6: Record the stable live baseline**

Provision fresh Ubuntu and NixOS disposable services using the current stable
component catalogs. Bind the generic harness arguments to the authorized KVM
canary aliases from `AGENTS.local.md`, run 21 iterations per guest, and retain
the last 20 samples. Repeat for one raw and one ZFS-backed Ubuntu service, and
for service plus LAN networking.

Expected: evidence reproduces the approximate existing Ubuntu 2.6-second SSH
boundary and has zero failed boots. A material mismatch pauses optimization
until the new bottleneck is explained.

- [ ] **Step 7: Commit the harness and neutral fixtures**

Use GitButler to create a `vm-boot-performance` branch with commit message:

```text
vm: add repeatable boot timing harness
```

Before committing, inspect every output/fixture for private infrastructure
details; commit no live evidence containing them.

---

### Task 2: Remove the Confirmed Keyboard Probe

**Files:**
- Modify: `../yeet-vm-images/scripts/build-linux-kernel.sh`
- Create: `../yeet-vm-images/scripts/verify-kernel-boot-policy.sh`
- Create: `../yeet-vm-images/scripts/test-kernel-boot-policy.sh`
- Modify: `../yeet-vm-images/.github/workflows/build-kernel.yml`

- [ ] **Step 1: Write the failing Kconfig policy test**

Create temporary configs representing the accepted and rejected forms:

```bash
cat >"$tmp/good.config" <<'EOF'
# CONFIG_SERIO_I8042 is not set
# CONFIG_KEYBOARD_ATKBD is not set
EOF

cat >"$tmp/bad-i8042.config" <<'EOF'
CONFIG_SERIO_I8042=y
# CONFIG_KEYBOARD_ATKBD is not set
EOF

cat >"$tmp/bad-atkbd.config" <<'EOF'
# CONFIG_SERIO_I8042 is not set
CONFIG_KEYBOARD_ATKBD=y
EOF
```

Assert the verifier accepts `good.config` and rejects both bad configs with the
offending symbol in stderr.

- [ ] **Step 2: Run the test and confirm failure**

Run:

```bash
cd ../yeet-vm-images
bash scripts/test-kernel-boot-policy.sh
```

Expected: fail because the verifier does not exist.

- [ ] **Step 3: Implement and apply the policy**

Create `verify-kernel-boot-policy.sh <kernel.config>` with exact checks for:

```text
CONFIG_SERIO_I8042=n
CONFIG_KEYBOARD_ATKBD=n
```

In `build-linux-kernel.sh`, add both `scripts/config --disable` operations
before `make olddefconfig`, call the verifier after `olddefconfig`, and retain
the existing required Firecracker/network/security Kconfig checks.

- [ ] **Step 4: Add publication-time verification**

In `build-kernel.yml`, run:

```bash
scripts/verify-kernel-boot-policy.sh "$KERNEL_OUT_DIR/kernel.config"
```

in the existing asset-verification phase so a workflow cannot publish a kernel
whose final dependency resolution re-enabled either driver.

- [ ] **Step 5: Run repository gates**

Run:

```bash
cd ../yeet-vm-images
bash -n scripts/build-linux-kernel.sh scripts/verify-kernel-boot-policy.sh scripts/test-kernel-boot-policy.sh
bash scripts/test-kernel-boot-policy.sh
bash scripts/test-kernel-release-workflows.sh
bash scripts/test-kernel-component-release.sh
bash scripts/test-component-catalogs.sh
```

Expected: all checks pass.

- [ ] **Step 6: Land the build policy before publication**

Commit only the kernel build, verifier, test, and workflow changes with:

```text
kernel: remove unused legacy keyboard probe
```

Land that commit on `yeet-vm-images/main`, verify the remote SHA, and do not
change `kernel-catalog.json` in this commit.

---

### Task 3: Publish and Validate an Immutable Kernel Candidate

**Files:**
- Modify: `../yeet-vm-images/kernel-catalog.json`
- Runtime-only: disposable Ubuntu and NixOS guest selector files
- Evidence-only: a private local benchmark output directory

- [ ] **Step 1: Resolve an immutable release identity**

From current `yeet-vm-images/main`, run:

```bash
kernel_json="$(scripts/resolve-latest-kernel.sh)"
kernel_version="$(jq -r .version <<<"$kernel_json")"
kernel_source_url="$(jq -r .source_url <<<"$kernel_json")"
kernel_source_sha256="$(jq -r .source_sha256 <<<"$kernel_json")"
release_json="$(scripts/resolve-kernel-release.sh "$kernel_version")"
kernel_release="$(jq -r .next_release <<<"$release_json")"
```

Expected: `kernel_release` matches
`kernel-linux-<version>-yeet-v<positive revision>` and does not already exist.

- [ ] **Step 2: Publish through the canonical workflow**

Dispatch `build-kernel.yml` from `main` with the resolved values, the pinned
Firecracker baseline config URL already used by the repository, and
`overwrite_release=false`. Watch the workflow to completion.

Expected: the immutable release contains exactly `vmlinux`, `kernel.config`,
`kernel-manifest.json`, and `kernel-checksums.txt`; downloaded checksums pass;
the manifest source commit is the landed Kconfig change.

- [ ] **Step 3: Add only the candidate catalog pointer**

Download `kernel-manifest.json`, compute its SHA-256, and run:

```bash
scripts/update-kernel-catalog.sh \
  --manifest "$manifest" \
  --manifest-sha256 "$manifest_sha256" \
  --channel candidate \
  --catalog-in kernel-catalog.json \
  --catalog-out "$tmp/kernel-catalog.json"
mv "$tmp/kernel-catalog.json" kernel-catalog.json
scripts/verify-component-catalogs.sh
```

Assert `.channels.amd64.stable` is byte-for-byte unchanged and candidate points
to the new exact ID and digest. Commit and land only `kernel-catalog.json`, then
wait until the public raw catalog exposes the candidate identity.

- [ ] **Step 4: Install the candidate selector in disposable guests**

For each stopped canary guest, download the candidate `vmlinux` and
`kernel.config` into:

```text
/usr/lib/yeet-vm/kernels/linux-<version>-yeet/
```

Verify both manifest hashes, write `release.json` with `release_id` and
`manifest_sha256`, then execute:

```bash
sudo /usr/lib/yeet-vm-kernel/select-kernel "linux-${kernel_version}-yeet"
```

From the client, run:

```bash
CATCH_HOST="$CATCH_HOST" mise exec -- go run ./cmd/yeet vm kernel sync "$service" --restart
```

Expected: Catch resolves the candidate through the public trusted catalog,
copies the exact guest assets into the service-local host cache, updates only
the kernel component lock, and reboots successfully.

- [ ] **Step 5: Run kernel smoke tests**

For Ubuntu and NixOS, verify:

```text
uname -r
/proc/config.gz or installed kernel.config
systemd-analyze time
yeet ssh <service> -- true
yeet copy round trip
/dev/vsock
/dev/net/tun
sudo reboot
```

Also verify service and LAN networking, raw and ZFS-backed root disks, root
growth, console recovery, guest agent state, nftables, iptables, and Tailscale's
required TUN primitives. Confirm dmesg contains no `i8042`, `atkbd`, or keyboard
initialization records.

- [ ] **Step 6: Measure the candidate**

Run the harness for 21 boots on the candidate and compare the last 20 with the
matching stable samples. Accept the kernel wave when it has zero failures, no
regression gate violation, and removes the observed keyboard delay. Record the
exact release ID, manifest digest, Yeet/Catch commit, runtime ID, guest base ID,
disk backend, and neutral capability labels.

Expected: approximately 0.5 seconds of kernel-path improvement. Treat the
measured result, not that estimate, as authoritative.

---

### Task 4: Make Catch Readiness Event-Driven

**Files:**
- Modify: `pkg/catch/vm_readiness.go`
- Modify: `pkg/catch/vm_readiness_test.go`

- [ ] **Step 1: Add failing event-delivery tests**

Add tests proving that:

- a follower-sent valid report returns before any poll timer fires;
- cursor freshness becomes `journalctl --after-cursor <cursor>`;
- timestamp freshness becomes `journalctl --since @<unix-seconds>`;
- cancellation stops and joins the follower;
- an invalid interface is ignored;
- a follower error still permits the vsock fallback;
- timeout retains the console hint and most useful underlying error.

Use a channel controlled by the test and a one-second poll interval so an
immediate result proves the event path, not a shortened timer.

- [ ] **Step 2: Run focused tests and confirm failure**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestWaitVMGuestReady.*Follow|TestFollowVMGuestReadyJournal' -count=1
```

Expected: fail because journal following is not implemented.

- [ ] **Step 3: Implement the journal follower**

Start one `journalctl` process using `exec.CommandContext`, the VM unit, no
historical tail, export or JSON output, and the captured cursor/timestamp.
Decode complete records, pass only valid `yeet-ready` reports to the channel,
and return on context cancellation or process error.

Update `waitVMGuestReady` to:

1. Start the journal follower once.
2. Poll the existing vsock agent fallback on a 25 ms ticker.
3. Return the first valid journal or agent report.
4. Cancel and join the losing path.
5. Perform one final journal/agent read at the timeout boundary before failing.

Do not busy-loop, spawn repeated `journalctl` processes, or trust a guest
interface outside the provisioned network plan.

- [ ] **Step 4: Run focused and race tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'VMGuestReady|GuestReadiness' -count=1
mise exec -- go test -race ./pkg/catch -run 'VMGuestReady|GuestReadiness' -count=20
```

Expected: all tests pass with no goroutine leaks or race findings.

- [ ] **Step 5: Install the candidate Catch on the canary**

Using the authorized machine/catch aliases, install the current source Catch,
verify its version/commit, and run the same 20-boot matrix against the already
validated candidate kernel.

Expected: actual SSH-listen time is unchanged, Catch's readiness observation
moves to within 50 ms of the guest marker, and immediate post-run SSH succeeds
20/20 times.

- [ ] **Step 6: Commit the readiness change**

Run targeted tests, `mise exec -- go test ./...`, and
`pre-commit run --all-files` once on the stable task state. Commit with:

```text
vm: observe guest readiness without polling
```

---

### Task 5: Shorten the Systemd Path to SSH

**Files:**
- Modify: `pkg/catch/vm_metadata.go`
- Modify: `pkg/catch/vm_metadata_test.go`
- Modify conditionally: `../yeet-vm-images/nixos/yeet/vm.nix`
- Modify conditionally: `../yeet-vm-images/scripts/test-nixos-guest-base.sh`

- [ ] **Step 1: Capture dependency evidence after Tasks 3 and 4**

Collect `systemd-analyze critical-chain yeet-sshd.service`,
`systemd-analyze plot`, `systemctl show` ordering properties, SSH daemon start
timestamps, and PAM/session journal output from both guests. Confirm the root,
account, host key, authorized key, `/run/sshd`, and kernel-configured IP are
available before each dependency being considered.

- [ ] **Step 2: Write the Ubuntu ordering test**

Change `TestWriteVMGuestMetadataFiles` expectations so the Yeet-owned SSH unit
must retain:

```text
DefaultDependencies=no
Before=multi-user.target
Type=exec
ExecStartPre=/usr/sbin/sshd -t
ExecStart=/usr/sbin/sshd -D -e -f /etc/ssh/sshd_config
Restart=always
```

and must not contain `systemd-sysusers.service` or `network.target`. Keep the
readiness service ordered after `yeet-sshd.service`.

- [ ] **Step 3: Run the test and confirm failure**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestWriteVMGuestMetadataFiles -count=1
```

Expected: fail because the current unit includes both dependencies.

- [ ] **Step 4: Remove only proven-redundant dependencies**

Update `vmGuestSSHDService` to remove `systemd-sysusers.service` and
`network.target` ordering/wants while retaining `local-fs.target` for the first
A/B candidate. Do not alter sshd configuration, PAM, authentication methods,
or systemd supervision in this step.

- [ ] **Step 5: Run the Ubuntu live A/B**

Install the candidate Catch and run 20 boots. Verify public-key login, `sudo`,
PTY and non-PTY sessions, SFTP/rsync, concurrent logins, logout cleanup, reboot,
and package reinstall. Accept each removed dependency only when all checks pass
and p50 improves by at least 50 ms or the critical path is demonstrably
simplified without regression.

- [ ] **Step 6: Test removal of `local-fs.target` separately**

Create a second candidate that removes `local-fs.target`. Repeat the same
matrix, including a guest with an additional local filesystem entry. Keep the
removal only when SSH, PAM, and filesystem availability are correct for all 20
boots. Otherwise restore `local-fs.target` and retain the accepted first A/B.

- [ ] **Step 7: Apply equivalent NixOS ordering only when needed**

If NixOS remains above the target because its `sshd` ordering waits for
networkd despite kernel-configured networking, express the reduced `after` and
`wants` lists in `nixos/yeet/vm.nix`. Add evaluation assertions to
`test-nixos-guest-base.sh`, build a guest candidate, and run
`nixos-rebuild dry-build --flake /etc/nixos#yeet-vm` plus the full SSH matrix.

Do not patch generated NixOS unit files in the rootfs.

- [ ] **Step 8: Commit accepted systemd changes**

Commit Yeet changes with:

```text
vm: start guest SSH after required boot state
```

When NixOS image changes are accepted, publish them as a separate immutable
guest-base candidate and update only the guest catalog candidate channel.

---

### Task 6: Evaluate Remaining Kernel and Udev Work

**Files:**
- Modify conditionally: `pkg/catch/vm_boot.go`
- Modify conditionally: `pkg/catch/vm_boot_test.go`
- Modify conditionally: `../yeet-vm-images/scripts/build-ubuntu-26.04.sh`
- Modify conditionally: `../yeet-vm-images/scripts/test-ubuntu-fast-rootfs-policy.sh`
- Modify conditionally: `../yeet-vm-images/nixos/yeet/vm.nix`

- [ ] **Step 1: Reprofile the accepted candidate stack**

Generate new dmesg, blame, critical-chain, device-unit, and serial-output timing
reports. Rank remaining contributors by p50 time before SSH, not by one boot's
`systemd-analyze blame` output.

- [ ] **Step 2: A/B quieter kernel logging**

On disposable VM config only, compare current serial logging with
`quiet loglevel=4`. Exercise a successful boot and deliberate failures for a
missing root disk, invalid init path, and guest panic. Accept a source change to
`vmKernelBootArgs` only when p50 improves by at least 50 ms and the console
still shows an actionable reason for every failure.

Add tests that the accepted arguments appear once and that root, init,
networking, reboot, and panic arguments remain unchanged.

- [ ] **Step 3: Gate the udev experiment**

Proceed only when udev or device-unit work remains at least 100 ms p50 before
SSH. Otherwise record `udev experiment skipped: below 100 ms gate` and move to
Task 7.

- [ ] **Step 4: Build an Ubuntu udev-masked candidate**

In the Ubuntu image builder, mask the udev daemon, control/kernel sockets,
trigger, settle, and coldplug units as one candidate-only change. Extend the
rootfs policy test to require those masks and to require devtmpfs support,
Yeet agent enablement, networkd, SSH, and TUN setup.

- [ ] **Step 5: Run the device compatibility matrix**

Verify root disk discovery, service and LAN interfaces, vsock agent, TUN,
console, root growth, reboot/kernel sync, snapshot/restore where supported,
and an additional disk configuration. Reject the entire udev mask set on any
missing device, naming, permissions, or rebuild regression.

- [ ] **Step 6: Publish only a passing guest candidate**

When the mask saves at least 100 ms p50 and passes every device gate, publish a
new immutable Ubuntu guest-base candidate and update only the candidate
channel. Do not automatically apply the same policy to NixOS; evaluate it
through the NixOS module as a separate candidate with identical gates.

---

### Task 7: Conditionally Spike Pre-Systemd SSH

**Files:**
- Modify conditionally: `guest/yeet-init/src/lib.rs`
- Modify conditionally: `guest/yeet-init/src/main.rs`
- Create conditionally: `guest/yeet-init/src/early_sshd.rs`
- Modify conditionally: inline tests in `guest/yeet-init/src/lib.rs`
- Modify conditionally: guest-base workflow inputs that pin the Yeet commit

- [ ] **Step 1: Apply the 1.25-second decision gate**

Skip this task when the accepted Tasks 2-6 stack reaches SSH p95 at or below
1.25 seconds. Record the achieved boundary and proceed to stable promotion.

Begin the spike only when p95 remains above 1.25 seconds and the remaining
critical path is systemd rather than kernel, storage, or host orchestration.

- [ ] **Step 2: Write failing init-process tests**

Add injected-command tests proving the spike:

- validates host keys/config before launch;
- launches `/usr/sbin/sshd -D -e -f /etc/ssh/sshd_config` once;
- waits for port 22 before emitting an early serial readiness marker;
- terminates the child on init failure;
- never enables passwords, keyboard-interactive auth, or root password login;
- does not start a second listener during systemd handoff.

- [ ] **Step 3: Implement the disposable spike**

Put command construction and child lifecycle in `early_sshd.rs`. Keep the
normal init preparation in `lib.rs`. Start the distro daemon after hostname,
runtime directory, keys, and kernel network state exist, then exec systemd.
For the spike image, prevent the normal `yeet-sshd.service` from racing for the
port and label the resulting daemon unsupervised in benchmark evidence.

This implementation is deliberately not eligible for stable promotion.

- [ ] **Step 4: Measure feasibility and security behavior**

Run 20 boots plus authentication-negative tests, concurrent sessions, reboot,
shutdown, OOM, daemon crash, package upgrade, key rotation, PAM behavior, and
systemd process ownership inspection. Compare SSH-listen time with the exe.dev
shape and record whether sub-second readiness is achieved.

- [ ] **Step 5: Decide based on supervision quality**

Discard the spike unless a follow-up production design provides one supervised
listener, deterministic handoff, normal key/account behavior, and clean
shutdown without maintaining a second SSH protocol implementation. A fast but
unsupervised daemon is evidence, not a shippable optimization.

---

### Task 8: Promote, Verify, and Clean Up

**Files:**
- Modify: `../yeet-vm-images/kernel-catalog.json`
- Modify when accepted: `../yeet-vm-images/guest-catalog.json`
- Modify: `../yeet-vm-images/docs/component-release-runbook.md`
- Modify as required: user-facing changelog/docs for shipped behavioral changes

- [ ] **Step 1: Update the component release runbook**

Document the boot-performance evidence contract: exact component identities,
unit-start-to-SSH p50/p95, readiness-observation p50/p95, raw sample count,
failed-boot count, storage/network matrix, security checks, and rollback
identity. State that a candidate cannot move stable when evidence is incomplete
or contains private infrastructure details.

- [ ] **Step 2: Run final Yeet quality gates**

On the final accepted Yeet commit set, run once:

```bash
mise exec -- go test ./...
pre-commit run --all-files
mise run quality
```

Run `mise run quality:goal` only when the accepted changes invalidate its broad
guarantees or form a release candidate under repository policy.

- [ ] **Step 3: Run final image repository gates**

Run:

```bash
cd ../yeet-vm-images
bash scripts/test-kernel-boot-policy.sh
bash scripts/test-kernel-release-workflows.sh
bash scripts/test-kernel-component-release.sh
bash scripts/test-component-catalogs.sh
bash scripts/test-ubuntu-fast-rootfs-policy.sh
bash scripts/test-nixos-guest-base.sh
nix flake check
```

Run image-family tests only for families whose source changed, but always run
the kernel and catalog checks.

- [ ] **Step 4: Complete the final canary matrix**

Use current main candidates and exact public immutable artifacts. Require zero
failed boots across the 20-boot matrix, immediate SSH after `yeet run`, reboot
to the selected kernel, successful package/rebuild checks, and clean raw/ZFS
service removal.

- [ ] **Step 5: Promote the kernel stable pointer**

Render `kernel-catalog.json` from the accepted manifest with `--channel stable`.
Assert only the intended exact stable identity changes, land it, and verify:

- local `main`, remote `main`, and advertised main agree;
- the public raw catalog shows the exact kernel ID and manifest digest;
- the immutable release remains downloadable and checksum-valid;
- package publication selects the same release and digest.

- [ ] **Step 6: Promote accepted guest candidates separately**

For each accepted Ubuntu or NixOS guest candidate, update only that family's
stable pointer after its independent matrix passes. Never combine a rejected
udev or pre-systemd SSH experiment with a passing kernel/systemd promotion.

- [ ] **Step 7: Land accepted Yeet code**

Use GitButler to tidy the session into reviewer-sized commits, run
`but pull --check`, verify each commit is based on current `origin/main`, and
land only the accepted harness/readiness/SSH changes. Verify local main,
origin/main, and the remote advertised SHA according to repository policy.

- [ ] **Step 8: Reverify stable resolution**

Provision new disposable Ubuntu and NixOS services through ordinary stable
catalog URLs with no local overrides. Run five boots each, immediate SSH,
reboot, kernel identity, agent, copy, and console checks.

- [ ] **Step 9: Clean live state and report results**

Remove every `yeet-bootbench-*` service with data and configuration cleanup,
verify no unit, DB record, ZFS dataset, disk, jail, or local service config
remains, and retain only redacted measurement evidence.

The final report must include:

- before/after p50 and p95 for kernel entry, SSH, readiness observation, and
  multi-user;
- exact promoted kernel and guest identities/digests;
- accepted and rejected experiments with measured reasons;
- commands/gates run and their outcomes;
- local, remote-main, workflow, release, catalog, package, and canary state;
- explicit confirmation that existing workload VMs were not restarted or
  mutated.
