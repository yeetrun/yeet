# Native Workload Sandboxing with Bubblewrap

## Context

Yeet runs native binaries, shebang scripts, and timer-backed commands directly
from generated systemd units. Systemd already supplies the workload identity,
environment, cgroup, restart policy, and optional Yeet network namespace, but
the process otherwise sees the host filesystem according to its Unix
permissions.

Native workloads should instead start with a small filesystem view containing
their immutable payload, writable service data, the runtime files needed to
start, and only operator-approved additional paths. Bubblewrap (`bwrap`) is a
small namespace builder suited to that job. It can construct the view directly
from the generated systemd `ExecStart` without introducing a persistent
container runtime or coupling workload startup to the Catch daemon.

Existing native services must not change behavior merely because Yeet or Catch
was upgraded. Operators need an explicit, one-service-at-a-time migration path
and an explicit escape hatch. Bubblewrap installation must likewise be
progressive: eager on a genuinely new Catch host, but otherwise delayed until
an operator first requests a sandboxed native workload.

## Goals

- Sandbox fresh native binary, script, and scheduled workloads by default.
- Give each sandboxed workload writable access to its service data directory
  and read-only access to its immutable payload without extra configuration.
- Let operators add read-only files or directories and writable directories,
  optionally remapped to another absolute path inside the sandbox.
- Preserve existing services as explicit `legacy` state until an operator
  migrates or opts them out through `yeet service set`.
- Provide `--sandbox=off` as a persisted escape hatch without conflating it
  with workload identity or network selection.
- Install and verify Bubblewrap at clear intent boundaries, never merely
  because Catch or Yeet was upgraded.
- Add user, PID, IPC, and UTS namespaces while preserving Yeet's existing
  systemd cgroup and selected network namespace.
- Make sandbox mutation generation-based, transactional, rollback-safe, and
  governed by the existing `manage` permission.
- Prove the feature first with disposable services, then migrate known native
  production services one at a time and stop on the first failure.

## Non-Goals

- Automatically migrating an existing native service during an upgrade,
  redeployment, restart, or configuration read.
- Sandboxing Catch itself, Docker or Compose workloads, VMs, DNS/network
  helpers, Tailscale helpers, or other Catch-managed host infrastructure.
- Replacing Yeet's existing network namespace or systemd cgroup lifecycle.
- Adding a cgroup namespace, an independent network namespace, custom seccomp
  policy, device passthrough, socket exposure, or writable single-file mounts
  in the first version.
- Presenting Bubblewrap as VM-grade isolation or as a safe way to execute
  hostile code with host-root consequences.
- Supporting every Bubblewrap-capable distribution in the automatic package
  installer. The first version follows Yeet's apt-managed Debian/Ubuntu host
  path and gives manual guidance elsewhere.

## Chosen Architecture

The generated native systemd unit invokes Bubblewrap directly:

```text
ExecStart=/usr/bin/bwrap <generated-policy> -- <payload> <arguments...>
```

There is no shell between systemd, Bubblewrap, and the payload. Catch renders
each argument with deterministic systemd-safe escaping and tests round trips
for spaces and other supported path and argument characters.

Systemd continues to apply `User=`, `Group=`, environment files, cgroup
placement, restart behavior, dependencies, and `NetworkNamespacePath=` before
starting Bubblewrap. It starts Bubblewrap with `/` as the host working
directory so the process does not carry a descriptor for the host-side service
data directory. Bubblewrap changes to the mounted service data directory only
after constructing the new root. `HOME` remains the service data directory.

The unit's executable condition continues to validate the immutable payload,
not `/usr/bin/bwrap`. Bubblewrap readiness is an activation prerequisite with
its own actionable errors.

Two alternatives were rejected:

- A long-lived Catch-owned sandbox launcher would make every native service
  depend on Catch's binary path and upgrade lifecycle, and could change an
  existing service's policy when Catch changed.
- Systemd hardening directives alone do not provide the same explicit empty
  filesystem construction, package lifecycle, or portable policy across the
  supported systemd versions.

## Sandbox State Model

Every native service has one of three authoritative states:

| State | Meaning | Execution |
| --- | --- | --- |
| `legacy` | The active generation predates sandbox metadata. | Direct payload execution. |
| `on` | Bubblewrap policy is explicitly active. | Bubblewrap starts the payload. |
| `off` | The operator explicitly selected the escape hatch. | Direct payload execution. |

Absence of sandbox metadata on an installed generation means `legacy`; it
must not be reinterpreted as the fresh-service default. A newly created native
service defaults to `on` when neither the command nor project configuration
selects a state. Catch determines whether the service is new from authoritative
installed state, so an old project file cannot accidentally migrate an
existing service.

Sandbox state and normalized exposure lists belong to the active service
generation. Replacement, redeployment, rollback, and synchronization use that
generation as the source of truth. Rolling back restores the previous
generation's state and exposures. Explicit `off` generations may retain
exposures as dormant configuration for a later activation.

Fresh successful deployments write the resolved explicit state to project
configuration. Synchronizing an older service writes `legacy`, `on`, or `off`
according to Catch instead of applying the fresh-service default locally.

## Command Surface

Initial native deployment accepts:

```text
yeet run <service> <payload> --sandbox=on|off
yeet run <service> <payload> --sandbox-ro=SOURCE[:DEST]
yeet run <service> <payload> --sandbox-rw=SOURCE[:DEST]
```

The exposure flags are repeatable. An exposure implies `--sandbox=on` unless
the same command explicitly supplies `--sandbox=off`, in which case the
exposures are stored but dormant.

Existing native services use the payload-free mutation surface:

```text
yeet service set <service> --sandbox=on|off
yeet service set <service> --sandbox-ro=SOURCE[:DEST]
yeet service set <service> --sandbox-rw=SOURCE[:DEST]
```

For a `legacy` service, exposure-only mutation is rejected; the operator must
choose `--sandbox=on` or `--sandbox=off`. Setting `off` records the explicit
choice even though direct execution is already effective.

An existing-service `yeet run` preserves Catch's active sandbox state and
exposures. If the resolved run would actually change either, the client stops
and prints the equivalent `yeet service set` guidance. Catch repeats the guard
so older or direct clients cannot bypass it. Ordinary payload redeployment
continues when the sandbox policy is unchanged.

The sandbox flags form one `service set` mutation family. They may combine
with each other but are exclusive with identity, network, storage, schedule,
publication, and snapshot mutations. Targeting and output options remain
available. Initial `yeet run` may combine sandbox configuration with the
ordinary initial-deployment flags, including `--run-as` and network selection.

The existing remote service-management route and `manage` permission apply;
no new top-level command, RPC permission class, or confirmation prompt is
introduced. Read-only inspection remains under the existing `read` boundary.

## Exposure Syntax and Replacement Safety

`SOURCE[:DEST]` follows the familiar container bind-mount shape:

- `SOURCE` is an absolute host path.
- `DEST`, when present, is an absolute path in the sandbox.
- Omitting `DEST` exposes the object at the same absolute path.
- Read-only exposures accept a regular file or directory.
- Writable exposures accept directories only.

The first colon is the source/destination separator. Literal colons in either
path are unsupported in the first version. Destinations must already be clean
absolute paths without `.` or `..` components; Catch does not silently rewrite
an operator's requested in-sandbox location.

Sources are resolved and canonicalized on Catch. When the resulting sandbox
state is `on`, sources must exist and be accessible under the workload's real
UID/GID. Dangling links, devices, sockets, FIFOs, relative paths, globbing,
writable regular files, and unsupported object types are rejected. An `off`
policy validates syntax and collisions but defers existence and accessibility
checks until activation.

Catch creates empty destination parent directories in the sandbox as needed.
User destinations cannot equal, contain, or be contained by another user
destination or a mandatory runtime, payload, or data mount. Exact duplicates
across or within access classes are invalid. These rules avoid policy meaning
that depends on Bubblewrap argument ordering.

Supplying entries for an access class means "this is my complete desired list
for that class," but an existing entry cannot disappear implicitly. An
unmentioned class is preserved. If a submitted class omits existing entries,
Catch rejects the request and prints both safe alternatives:

1. A command containing every existing entry plus the requested additions.
2. A command containing the necessary class-specific `reset` token followed by
   the desired replacement entries.

For example, if the active read-only list contains `/etc/myapp`:

```text
yeet service set api --sandbox-ro=/etc/myapp --sandbox-ro=/new/path
yeet service set api --sandbox-ro=reset --sandbox-ro=/new/path
```

`reset` is a reserved control value, never a host path. Reset alone clears the
class. Reset may be combined with new values in the same command. Catch prints
`--sandbox-ro=reset`, `--sandbox-rw=reset`, or both based only on the classes
whose existing entries the requested replacement would remove. Project
configuration stores the resolved lists, never the `reset` token. The token is
valid only for `yeet service set`; an initial deployment has no list to reset.

## Default Filesystem View

Bubblewrap begins with an empty tmpfs-backed root. Catch constructs the
following view for each sandboxed service:

- Read-only host runtime trees when present: `/usr`, `/bin`, `/sbin`, `/lib`,
  and `/lib64`.
- A small, distro-aware read-only `/etc` allowlist needed for dynamic linking,
  identity lookup, DNS, time zones, OS identification, and CA trust. This
  includes the effective resolver and hosts files rather than the complete host
  `/etc` tree.
- The immutable payload at its existing absolute path, read-only.
- The service data directory at its existing absolute path, writable.
- A private tmpfs `/tmp` and `/run`.
- A private `/proc` and Bubblewrap's minimal `/dev`.
- Each validated operator exposure at its normalized destination.

Systemd reads the service environment file before Bubblewrap starts; the file
itself is not mounted. `/root`, `/home`, `/var`, `/sys`, other services' roots,
and arbitrary host files remain absent unless required by the fixed runtime
policy or explicitly exposed.

Keeping runtime trees read-only is not equivalent to mounting the host root
read-only: paths outside the constructed view do not exist from the workload's
perspective.

## Namespace and Runtime Policy

The generated policy creates new user, PID, IPC, and UTS namespaces, assigns a
stable sandbox hostname, starts a new session, and enables Bubblewrap's parent
death and PID 1 reaping behavior. The workload's configured numeric UID/GID is
preserved inside the user namespace so ownership and identity-sensitive code
do not unexpectedly observe a different account. Creation of further user
namespaces inside the sandbox is disabled.

Bubblewrap deliberately does not create a network namespace. It inherits the
host or Yeet-managed network namespace already selected by systemd. It also
inherits the systemd cgroup; there is no cgroup namespace in the first version.
Network policy and sandbox policy therefore remain independent settings.

`--run-as=root` does not disable Bubblewrap. A fresh root-run native service is
still sandboxed unless the operator separately selects `--sandbox=off`.

## Bubblewrap Dependency Lifecycle

Catch owns a host-global, serialized `ensureBubblewrap` operation. It accepts
only the trusted `/usr/bin/bwrap` path and verifies that it is a regular binary,
root-owned, and not group- or world-writable. Version presence alone is not
sufficient: a minimal functional namespace probe is authoritative so Yeet
detects kernel user-namespace restrictions, distribution differences, and
AppArmor denial on the actual host.

Catch automatically installs and probes Bubblewrap without an additional
prompt at these intent boundaries:

1. A genuinely fresh Catch installation.
2. A new native `yeet run`, or another activation, whose resulting sandbox
   state is `on`.
3. `yeet service set` when the resulting sandbox state is `on`.

Installing or upgrading Yeet or upgrading an existing Catch installation does
not install Bubblewrap. A `legacy` or `off` service does not require it, and
editing dormant exposures while remaining `off` does not install it. If the
package is later removed, the next sandbox activation or replacement runs the
ensure operation again.

On supported hosts the installer uses the established apt path to update
package metadata when needed and install the `bubblewrap` package
non-interactively. Unsupported package managers, apt failures, unsafe binary
ownership, disabled user namespaces, and failed functional probes produce
actionable manual guidance. Yeet never disables AppArmor or relaxes global
user-namespace controls to make the probe pass.

A fresh Catch installation performs package readiness before committing the
Catch installation. Service activation additionally probes the constructed
policy under the intended workload UID/GID. Dependency or policy failure is
detected before stopping the active service or committing a replacement.

## Mutation and Activation Transaction

Sandbox mutation uses the existing per-service operation lock and normal
generation replacement lifecycle. A normalized request equal to the active
policy is a no-op. Every other successful sandbox mutation creates a new
generation. For a running service this deliberately means a restart, including
`legacy` to explicit `off`; one predictable transaction is preferred over a
special metadata-only path.

Catch performs the following steps:

1. Load the active generation and validate workload eligibility.
2. Apply the presence-aware sandbox patch in memory.
3. Validate state, reset/omission rules, paths, mount collisions, and the
   complete resulting policy before runtime mutation.
4. Ensure Bubblewrap when the result is `on`.
5. Build the exact mount and namespace argument plan.
6. Probe that plan under the workload UID/GID with a harmless command in place
   of the payload, and statically verify the generated systemd unit.
7. Stage the replacement generation while preserving the previous generation,
   unit, timer, enablement, and running intent.
8. Stop the old runtime, install and activate the replacement, and perform the
   existing service-start verification.
9. Commit the new generation and synchronize local project configuration only
   after remote success.

If activation fails, Catch restores the previous generation, units, timer,
enablement, and previous running or inactive state. The dependency package is
not uninstalled if a later service transaction fails; service state is the
transactional boundary.

For a scheduled native workload, Catch replaces and verifies both the service
and timer units but never invokes the payload as a migration test. Success
means the timer is loaded and has the expected enabled/waiting intent and the
payload unit passes static verification.

If the matching project entry is absent or local synchronization fails after
remote success, Catch remains authoritative and the client prints the existing
`yeet service sync <service>` recovery guidance. Local configuration is never
changed before remote success.

## Errors and Operator Visibility

Validation and dependency errors identify the service, source or destination,
and reason without weakening the requested policy. Important failures include:

- Bubblewrap missing, untrusted, or nonfunctional.
- User namespace or AppArmor restrictions.
- Missing or inaccessible active sources.
- Unsupported source types or writable files.
- Mandatory-mount, duplicate, or overlapping destination collisions.
- Existing entries omitted without an explicit reset.
- Unit verification, activation, or rollback failure.

Omission errors include directly runnable preservation and explicit replacement
commands. Dependency errors include the exact manual package/probe guidance
appropriate to the detected host. Errors never fall back from `on` to direct
execution.

`yeet service info` reports the authoritative `legacy`, `on`, or `off` state
and every normalized read-only and writable exposure. Legacy output includes a
concise hint to use `yeet service set <service> --sandbox=on` or
`--sandbox=off`. Generation-aware reporting makes the effect of rollback
visible and unambiguous.

## Compatibility and Release Sequence

The pending compatibility point release is completed and installed on the two
known Catch hosts before sandbox implementation begins. That establishes a
common service-generation and configuration baseline for later explicit
mutation.

The sandbox feature then ships in a later release. Upgrading those Catch hosts
to the feature release neither installs Bubblewrap nor changes any service's
sandbox state. Older Catch versions reject unknown sandbox flags through the
normal client/server upgrade guidance; clients must not emulate sandbox
mutation with a redeployment.

The live rollout is intentionally small and manual:

1. Upgrade both known Catch hosts to the feature release and verify that all
   existing services remain direct and Bubblewrap remains uninstalled unless
   already present independently.
2. Create disposable native services on each host. Their first sandboxed run
   exercises dependency installation and the host-specific functional probe.
3. Verify hidden host paths, writable service data and `/tmp`, read-only files
   and directories, writable directory remapping, PID isolation, existing
   network modes, root-plus-sandbox behavior, explicit `off`, replacement
   guards, rollback, and a timer that must not execute during migration.
4. Remove the disposable services and their generated runtime state.
5. Migrate each real native production service individually with
   `yeet service set <service> --sandbox=on`, adding only paths proven necessary
   by that workload.
6. Verify status, logs, and workload behavior after each migration and stop on
   the first failure. No remaining service is migrated automatically.

Catch itself and non-native workloads remain outside this rollout.

## Verification

Tests cover:

- CLI and RPC parsing, help, repeated flags, explicit presence, state defaults,
  `reset`, guarded replacement, and mutation-family exclusivity.
- Table-driven and fuzz coverage for bind syntax, path normalization,
  destination collisions, and systemd argument escaping.
- Fresh-service `on`, installed-generation `legacy`, explicit `off`, dormant
  exposures, config synchronization, and rollback restoration.
- Deterministic Bubblewrap argument plans for binaries, shebang scripts, and
  scheduled native payloads.
- Mandatory runtime, payload, data, resolver, temporary, device, and process
  mounts, plus rejection of unsupported objects and writable files.
- Namespace selection proving user/PID/IPC/UTS isolation while inheriting the
  selected network namespace and systemd cgroup.
- Trusted-binary validation, serialized apt installation, no-prompt behavior,
  functional probes, unsupported-host guidance, and AppArmor/user-namespace
  failures.
- Every dependency trigger and explicit proof that ordinary Yeet/Catch upgrades,
  legacy operations, `off`, and dormant exposure edits do not install it.
- `manage` permission enforcement for mutation and `read` behavior for info.
- Failure injection before stop, during unit installation, during activation,
  and during rollback, including restoration of prior running intent.
- Scheduled replacement that verifies the timer without executing the payload.
- Existing-service `yeet run` preservation and Catch-side mutation guards.
- Real-Bubblewrap Linux integration coverage on supported host families.

Implementation uses focused package tests while iterating, then the complete Go
suite, pre-commit, and the relevant repository quality gates at the coherent
task boundary. Parser and path-handler changes receive fuzz coverage as
required by repository policy.

## Documentation

CLI help, README examples, the website manual, and the feature release
changelog describe:

- The fresh-native default and unchanged legacy behavior.
- Explicit one-service migration and the `off` escape hatch.
- Read-only file/directory and writable-directory exposure rules.
- Optional destination remapping and guarded `reset` replacement.
- Progressive dependency installation triggers.
- The interaction with `--run-as` and existing network modes.
- Troubleshooting for package installation, user namespaces, and Ubuntu
  AppArmor without weakening host-wide security controls.

Evergreen manual content states current behavior directly. Upgrade and
migration framing stays in the changelog and dedicated compatibility guidance.

## Security Boundary

The first version combines a constructed filesystem view with user, PID, IPC,
and UTS namespaces, the existing workload UID/GID, a systemd cgroup, and the
independently selected Yeet network mode. This materially limits normal
filesystem discovery and modification, process visibility and signaling, and
namespace-global state available to native workloads.

It is not a VM or hardware boundary. The workload shares the host kernel and
retains access to its environment credentials, explicitly exposed paths,
minimal devices, and whatever network Yeet selected. Kernel vulnerabilities,
Bubblewrap vulnerabilities, and intentionally exposed resources remain part of
the threat model. Custom seccomp, arbitrary devices, sockets, and independent
network isolation are deliberately excluded from the first version.

A non-root service identity remains the expected boundary for untrusted code.
`--run-as=root --sandbox=on` is a useful filesystem and namespace visibility
guard, but an escape has host-root consequences; it does not make hostile root
code safe. Operators needing direct host-root behavior must make the separate,
auditable `--sandbox=off` choice.
