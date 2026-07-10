# Upgrade Warning Deduplication Design

## Goal

Keep expected VM host warnings visible during catch installation without
printing the same warning twice. On a host such as `yeet-hetz`, where
`/dev/kvm` is intentionally absent, an upgrade should show:

- `Check VM tools (not available)` during preflight; and
- one final grouped warning block containing the missing KVM and VM-tool
  details, with one shared documentation link.

The behavior should be consistent for both `yeet init` and `yeet upgrade`,
because catch upgrades reuse the init workflow.

## Current Behavior and Root Cause

The client-side init preflight probes VM host capabilities before installing
catch. When it finds missing KVM, TUN, or architecture support, it prints a
warning immediately and marks the VM-tools step unavailable.

The newly installed catch binary then performs its own authoritative host
prerequisite check. Its warning is captured by the init install filter and
printed in the final grouped warning summary. Missing KVM is therefore reported
once by the client preflight and again by catch.

This is duplicated warning ownership, not terminal spinner corruption or a
repeated remote log read. The install filter already deduplicates warnings
within the remote installer output.

## Design

Make the completed catch installer the sole owner of user-facing VM capability
warnings during a successful init or upgrade.

The client preflight will continue to probe the remote host because it needs
the result to decide whether VM tools can be installed or prompted for. When
the probe finds an unsupported architecture or missing KVM or TUN device, the
preflight will:

1. complete `Check VM tools` with the detail `not available`;
2. skip VM-tool installation and related prompts; and
3. continue without printing its own capability warning.

The catch installer will keep emitting its canonical capability and missing
tool warnings. The existing init install filter will keep collecting those
lines and rendering one final warning block, including a single shared docs
link when multiple warnings point to the same page.

No catch-side warning text or install-filter formatting needs to change.

## Error Handling

Preflight probe failures will retain their immediate warning because they
describe a client-side check failure that the later catch prerequisite report
may not explain.

Fatal SSH, download, and installation failures will retain their existing
behavior. If installation fails before catch reports host capabilities, the
fatal error remains the primary result; an expected missing-KVM advisory does
not need to precede it.

Warnings unrelated to VM capability detection, including VM LAN bridge and
cleanup warnings, remain unchanged.

## Testing

Add a regression test for the combined init flow where:

- the client preflight probe returns missing `/dev/kvm` and `qemu-img`;
- the simulated catch installer emits the corresponding canonical warnings;
- the rendered output contains `Check VM tools (not available)`;
- the `/dev/kvm` warning appears exactly once;
- the missing `qemu-img` warning appears exactly once; and
- the shared host-requirements link appears exactly once in the final warning
  block.

Update the focused preflight test to assert the unavailable decision and status
line without expecting an immediate capability warning. Keep the existing
catch prerequisite and install-filter tests to preserve their independent
warning and grouping behavior.

Run the focused init and install-filter tests, the full `pkg/yeet` suite, and
the repository's required pre-commit gate before committing implementation.

## Scope

This change only removes duplicate terminal output. It does not suppress
expected host warnings, change VM availability rules, alter catch installation,
or require user-facing documentation updates.
