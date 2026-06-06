# Fresh VPS E2E Friction Test Design

## Context

Yeet has gained a lot of new surface area recently: first-class VMs, VM image
cache management, VM resource updates, custom VM image imports, service-root
sync, snapshots, and continued Docker, binary, cron, and Tailscale workflows.
The goal of this pass is to test `main` against a fresh Ubuntu VPS before the
next patch release, using the product as a public user would after visiting the
website.

The fresh host has only a WAN interface by default. It may not support nested
virtualization, LAN-style macvlan networking, or ZFS. Those are not failures by
themselves; they should be recorded as host limitations when they block a test.
The committed plan must stay free of private infrastructure details. The actual
SSH target is provided at execution time and belongs only in local run notes,
not in committed docs.

## Goals

- Validate that a fresh Ubuntu host can be bootstrapped from local `main` with
  minimal friction.
- Exercise the public docs surface across install, init, service deployment,
  lifecycle commands, cleanup, and user-facing diagnostics.
- Build a friction log that separates yeet bugs, docs gaps, host limitations,
  and external dependency failures.
- Leave the VPS clean enough to reuse or destroy after the pass.
- Identify fixes needed before the next patch release.

## Non-Goals

- Exhaustively test every flag combination.
- Force host features that the VPS provider does not expose, such as nested KVM
  or LAN DHCP.
- Install or configure ZFS unless explicitly needed for a follow-up pass.
- Commit private target details, auth material, service names, or raw logs that
  include host-specific identifiers.

## Approach

Use a guided smoke matrix on `main`. This gives better pre-release signal than
testing only the latest public release, while still following the public user
workflow: install a yeet client, bootstrap catch, deploy representative payloads,
inspect state, mutate a few settings, and remove everything cleanly.

If `main` exposes friction in the install path, record whether a stable release
user would hit the same issue. If a problem is clearly a `main` regression,
prioritize fixing it before release.

## Test Phases

### 1. Fresh Host Baseline

Collect a minimal host profile before changing anything:

- Ubuntu version, kernel, architecture, systemd state, disk, memory, and CPU.
- Network interfaces, default route, public IP shape, and whether a LAN-like
  parent interface exists.
- Presence or absence of Docker, Tailscale, ZFS, KVM, TUN/TAP, `ip`, `zstd`,
  `qemu-img`, `e2fsck`, `resize2fs`, `mount`, and `umount`.
- SSH host key prompt behavior from the workstation.

This phase establishes which later skips are environment limitations.

### 2. Client Install And Catch Bootstrap

Use the local repo on `main` as the client under test. Run the normal bootstrap
flow against the fresh host:

- Build or run yeet from `main`.
- Run `yeet init <ssh-target>`.
- Record prompts, package offers, install duration, and any required manual
  choices.
- Verify catch is installed and running with `systemctl`, catch logs, and
  `yeet version` through the catch RPC host.
- Confirm the user-visible difference between the SSH machine host and catch RPC
  host is clear in output or docs.

Expected result: a fresh Ubuntu VPS can run catch after one `yeet init` pass, or
the friction log captures exactly what blocked it.

### 3. Core Service Workflows

Exercise one representative service for each primary payload type:

- Docker image with published ports, for example an HTTP echo/nginx-style image.
- Docker Compose file with a simple HTTP service.
- Binary or script payload that runs under systemd.
- Cron job installed as a systemd timer.

For each service, test the same lifecycle shape:

- `yeet run` or `yeet cron`.
- `yeet status`, `yeet info`, and logs.
- Reachability from the workstation when applicable.
- Idempotent redeploy with the same command.
- One small `yeet service set` mutation relevant to that service.
- `yeet service sync` where the live server state should mirror into config.
- `yeet rm --clean-data`, followed by host-side evidence that units, data, and
  generated artifacts were removed.

Expected result: common public examples deploy, inspect, update, and remove
without surprising prompts or stale local config.

### 4. Networking

The default and WAN-safe network modes are primary for this VPS. Test:

- Default service networking and published ports.
- Tailscale RPC reachability used by catch.
- Service IP reporting and any doc claims that assume LAN availability.

Attempt `--net=lan` only if the host baseline shows a plausible parent
interface and provider behavior does not obviously forbid it. If there is no
traditional LAN or DHCP path, mark LAN networking as not tested on this host and
include that limitation in the report.

Expected result: WAN-safe networking works. LAN-specific tests are either
validated or clearly marked host-limited.

### 5. VM Workflows

Check KVM before attempting VM creation:

- If KVM is unavailable, record VM testing as blocked by host capability and
  verify that `yeet init` and `yeet run vm://...` communicate the limitation
  clearly.
- If KVM is available, test a raw-disk VM with default networking:
  - first image download or cache update,
  - boot readiness,
  - `yeet ssh <vm>`,
  - `yeet vm console <vm>`,
  - image cache listing and prune dry-run,
  - stopped VM resize for CPU, memory, and disk growth,
  - clean removal with `--clean-data`.

ZFS-backed VMs are deferred unless ZFS is already present and healthy, because a
generic Hetzner-style VPS is not representative of the homelab ZFS path.

Expected result: VM support either works end-to-end or fails with a clear,
actionable host-capability message.

### 6. Cleanup And Report

At the end of the pass:

- Remove all test services with `--clean-data`.
- Confirm no `yeet-<test>` systemd units, containers, networks, VM disks, image
  imports, or local config entries remain unintentionally.
- Capture final catch status and logs.
- Produce a friction report with a pass/fail matrix and prioritized fixes.

## Friction Log Format

Each finding should use this shape:

- Phase:
- Command:
- Expected:
- Actual:
- Severity: blocker, sharp edge, doc gap, host limitation, or cleanup issue.
- Classification: yeet bug, docs bug, main regression, host limitation,
  external dependency, or unknown.
- Reproduction:
- Evidence:
- Cleanup action:
- Proposed next step:

Use local untracked notes for raw command output. Commit only sanitized specs or
follow-up docs.

## Success Criteria

- `yeet init` from `main` either works on a fresh Ubuntu VPS or produces a
  short, actionable error.
- Container, compose, binary/script, and cron payloads each complete one full
  deploy/inspect/update/remove cycle.
- WAN-safe networking works without assuming LAN DHCP.
- VM tests are run if KVM is available; otherwise, the limitation is explicit
  and user-facing messages are judged for clarity.
- Cleanup leaves no unexpected remote services or local test config.
- Every failure or rough edge is captured with enough detail to create a fix
  task.

## Open Risks

- The VPS may not expose nested virtualization, which limits VM validation.
- LAN and VLAN tests may not be meaningful on a WAN-only provider network.
- Installing Docker or Tailscale on a fresh host may require package repository
  setup that has its own external failure modes.
- Public docs may still imply LAN or homelab assumptions that need clearer VPS
  caveats.

