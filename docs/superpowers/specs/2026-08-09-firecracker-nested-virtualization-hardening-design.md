# Firecracker Nested-Virtualization Hardening

## Context

Zapscape (CVE-2026-64561) is a host-KVM escape that requires privileged guest
code and nested virtualization exposed to the guest. Yeet's generated
`firecracker.json` files previously contained no explicit CPU configuration,
so nested-virtualization visibility depended on current runtime behavior rather
than a Yeet contract. Private rollout inventory, guest checks, and host-kernel
evidence are retained separately from this public design.

An additional operator-owned VM service cannot be modified directly. Its
operator can upgrade Catch and run a targeted remediation after the next Yeet
release. This design refers to that target only by the placeholder
`deferred-vm-service` and to its guest as an operator-supplied endpoint.

Firecracker's broad static CPU templates are not appropriate for this fix. They
mask unrelated ISA and MSR state, can affect mitigation selection and
performance, and are deprecated in Firecracker v1.16.1. Yeet should retain the
broadest host-supported ordinary instruction set and express only its policy
that nested virtualization is unsupported.

## Goals

- Make the absence of Intel VMX and AMD SVM an explicit default for every new
  Yeet Firecracker VM.
- Preserve that policy whenever Catch rewrites a Firecracker configuration.
- Backfill the running VMs on the directly managed VM host one at a time, with
  rollback and health verification.
- Give the deferred VM operator an auditable, narrowly targeted backfill
  procedure after the release.
- Avoid broad CPU templates and avoid materially changing ordinary workload
  performance.
- Keep host-kernel patching visible as an independent requirement.

## Non-Goals

- Supporting nested virtualization inside Yeet VMs.
- Adding a user-facing CPU-template flag or opt-out.
- Automatically migrating or restarting existing VMs during Catch upgrade.
- Selecting a portable CPU model for live migration or snapshot portability.
- Treating CPUID masking as a substitute for a patched host KVM implementation.
- Automatically updating an unknown operator host's Linux kernel.

## CPU Policy

Catch emits a minimal top-level Firecracker `cpu-config` with two CPUID
modifiers:

| Vendor feature | Leaf | Register bit | Modifier bitmap |
| --- | --- | --- | --- |
| Intel VMX | `0x1`, subleaf `0x0` | ECX bit 5 | `0bxxxxxxxxxxxxxxxxxxxxxxxxxx0xxxxx` |
| AMD SVM | `0x80000001`, subleaf `0x0` | ECX bit 2 | `0bxxxxxxxxxxxxxxxxxxxxxxxxxxxxx0xx` |

Both modifiers use flags `0`. Every `x` leaves the host-supported bit unchanged;
only the nested-virtualization discovery bits are cleared. The policy does not
change ordinary ISA dispatch, mitigation features, vCPU topology, or the
steady-state KVM exit path, so it should have no material performance impact on
ordinary workloads.

This is an unsupported-feature contract and defense in depth. Firecracker notes
that hiding CPUID features is not by itself a complete instruction-execution
security boundary. Patched KVM, current Firecracker artifacts, and live Intel
and AMD validation remain required.

## Catch Design

`firecrackerConfig` gains a typed `CPUConfig` field and the small supporting
types needed to serialize Firecracker CPUID leaf and register modifiers.

`renderFirecrackerConfig` is the single enforcement point. Before marshaling,
it sets the exact nested-virtualization-disabled policy. Central enforcement
means the policy is present for:

- new VM provisioning;
- VM settings and resize rewrites;
- guest-kernel selection and kernel-path rewrites; and
- any other existing path that decodes and re-renders `firecracker.json`.

There is no configuration knob and no architecture-selection branch because
Yeet's VM catalogs currently accept only `amd64`; the two standard CPUID leaves
exist on both supported x86 vendors, with the other vendor's bit reserved.

Runtime adoption must continue to accept older configurations that lack
`cpu-config`, so Catch upgrade does not become an implicit migration. Tests also
cover adoption of the hardened form. The next legitimate Catch render adds the
policy to a legacy configuration, but Catch does not restart the VM merely to
do so.

## Direct Managed-Host Backfill

The selected VMs on the directly managed VM host are updated sequentially. For
each VM:

1. Record the systemd unit state, Firecracker PID, configuration, guest CPU
   flags, `/dev/kvm` state, and current service health.
2. Save a timestamped copy of that VM's `firecracker.json` alongside the active
   file.
3. Atomically insert the exact CPU policy and validate the resulting JSON.
4. Restart only the corresponding `yeet-vm-<service>.service`.
5. Verify the unit is active with a new Firecracker PID, the guest is reachable,
   VMX/SVM remain absent, `/dev/kvm` remains absent, and prior service health is
   restored.

The rollout stops on the first failure. The failed VM's original configuration
is restored atomically and its unit is restarted before investigation. No guest
disk mutation is involved.

Until the patched Catch is installed, the existing Catch binary may discard the
manually added field during a resize or kernel-configuration rewrite. All
managed configs and guests are therefore rechecked after the release upgrade.

## Remote Operator Backfill

After the release, the deferred VM operator performs these distinct actions:

1. Run `yeet upgrade` to install the patched Catch.
2. Apply the host vendor's CVE-2026-64561 kernel update and schedule its required
   reboot.
3. Download a pinned-revision remediation gist, verify its published SHA-256,
   and run it as root with `deferred-vm-service` as the explicit placeholder
   service argument, replaced with the operator's actual service name.

The helper script fails closed unless it identifies exactly one matching
systemd unit and Firecracker config. It rejects an unexpected existing CPU
configuration, saves the original file, writes the minimal policy atomically,
restarts only the explicit target service, verifies the unit and Firecracker
process, and restores the original file automatically if restart fails.

The gist URL is pinned to a specific revision rather than a mutable latest URL.
The operator downloads and verifies the script before execution; an unverified
`curl | bash` command is not used. The script reports the running host kernel
but does not guess the distribution or automate OS package installation.

After the host-side operation, guest-visible verification runs through the
operator-supplied guest endpoint (shown as the placeholder
`vm-guest.example`): the guest must remain healthy, neither `vmx` nor `svm` may
appear in `/proc/cpuinfo`, and `/dev/kvm` must remain absent.

## Error Handling

- Firecracker JSON is validated before replacing an active configuration.
- Writes are atomic and preserve the original file mode and ownership.
- Each VM has an independent backup and rollback boundary.
- A failed restart prevents work from continuing to another VM.
- An unexpected pre-existing CPU policy is investigated, not overwritten.
- Host-kernel state is reported separately so a successful CPUID mask cannot be
  mistaken for complete CVE remediation.

## Verification

Repository tests prove that:

- plain rendering contains exactly the two minimal masks;
- new provisioning emits the policy;
- resize and settings rewrites retain it;
- kernel selection and kernel-path rewrites retain it;
- legacy configurations gain it on their next render;
- runtime adoption accepts the hardened configuration; and
- no static CPU template or unrelated CPUID/MSR modifier is introduced.

Development uses focused `pkg/catch` tests. Once the implementation is stable,
run the full Go suite and `pre-commit run --all-files` once before committing
the code. Live verification records the exact config, unit, PID, guest CPU
flags, `/dev/kvm` state, and service health for every backfilled VM.

Real vendor validation occurs during the direct rollout. Validation for the
other x86 vendor uses a safe disposable VM on a suitable KVM host when one is
available; absence of such a host is reported explicitly rather than inferred
from the first vendor's results.

## Release Boundary and Completion

This work does not cut or publish a release. Once the implementation and direct
live backfill are complete, the user will separately authorize the patch
release and upgrade the affected Catch hosts.

The hardening is complete when:

- the directly managed VMs have the explicit policy and pass their
  post-restart checks;
- the repository generates and preserves the policy for every new VM;
- the pinned, checksummed deferred-operator procedure is ready;
- the deferred VM service is subsequently backfilled and verified through the
  operator-supplied guest endpoint; and
- host-kernel patching remains separately tracked until every VM-capable host
  has booted a vendor kernel containing the upstream KVM fix.
