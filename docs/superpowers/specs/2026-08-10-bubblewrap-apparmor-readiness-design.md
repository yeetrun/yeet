# Bubblewrap AppArmor Readiness

## Context

Native Bubblewrap sandboxing is available, but the host dependency probe is
not strong enough for Ubuntu 24.04's restricted unprivileged-user-namespace
policy. A fresh Catch installation runs the dependency probe as root. That
probe can pass even though the first ordinary native workload later fails
while Bubblewrap writes its UID map under the workload's non-root identity.

The existing service activation boundary is otherwise correct. Before service
state or runtime changes, Catch ensures the Bubblewrap dependency and then
probes the final sandbox plan under the exact workload UID and GID. A failure
leaves the service database, unit, artifacts, and runtime unchanged.

Two reference-host smoke tests establish the required host split:

- On Ubuntu 24.04 with
  `kernel.apparmor_restrict_unprivileged_userns=1`, the distro's generic
  `unprivileged_userns` profile denies Bubblewrap's `setpcap` and `uid_map`
  operations. A transient purpose-built profile makes the same non-root probe
  pass. The Bubblewrap child runs in the stacked restricted profile with zero
  effective capabilities, and nested user-namespace creation remains denied.
- On Debian 13 without the Ubuntu restricted-userns sysctl, installing only
  the distro Bubblewrap package makes the same non-root probe pass. No
  Bubblewrap-specific AppArmor profile is needed.

The selected solution extends Catch's existing progressive dependency
lifecycle. It does not migrate services, change sandbox state, or weaken a
host-wide security control.

## Goals

- Make a genuinely fresh `yeet init` verify that a normal non-root native
  workload can use Bubblewrap, not merely that root can use it.
- Make the first sandbox activation on an older supported Ubuntu host install
  and load one narrowly scoped Bubblewrap AppArmor profile when required.
- Keep Debian and Ubuntu hosts without the restriction on the existing
  package-only path.
- Preserve the existing behavior that an upgrade of an installed Catch does
  not install Bubblewrap or AppArmor policy.
- Preserve fail-before-service-mutation behavior for new runs, service-set
  mutations, start, restart, rollback, identity, network, schedule, and root
  operations that activate an `on` generation.
- Preserve operator-owned AppArmor policy and fail closed on ownership,
  content, parser, profile-selection, or rollback uncertainty.
- Ensure that granting Bubblewrap the permissions needed to construct a user
  namespace does not grant those capabilities to the workload it launches.

## Non-Goals

- Disabling AppArmor or changing
  `kernel.apparmor_restrict_unprivileged_userns`.
- Changing `kernel.unprivileged_userns_clone`, namespace-count sysctls, or
  another host-wide user-namespace control.
- Making `/usr/bin/bwrap` setuid or adding file capabilities.
- Installing Ubuntu's complete `apparmor-profiles` package. It contains many
  unrelated experimental profiles and is not an acceptable dependency side
  effect.
- Automatically migrating a `legacy` or `off` service to `on`.
- Installing Bubblewrap or AppArmor policy during an ordinary Catch upgrade,
  read-only command, or dormant `off` policy edit.
- Supporting an arbitrary AppArmor implementation or profile ABI in the first
  iteration. Automatic profile management is limited to compatible Ubuntu
  hosts that expose the restricted-userns sysctl and whose installed parser
  accepts the exact Yeet policy.
- Removing the host dependency when a service is removed. Bubblewrap and a
  successfully installed profile are host prerequisites, not service
  artifacts.

## Approaches Considered

### Yeet-managed hardened profile

Catch carries one deterministic profile, installs it only when a compatible
Ubuntu host's non-root probe demonstrates that it is needed, and validates the
loaded policy with functional and security probes. This is the chosen
approach. It is deterministic, progressive, and leaves global restrictions in
place.

### Install Ubuntu's `apparmor-profiles` package

Ubuntu publishes `bwrap-userns-restrict` under
`/usr/share/apparmor/extra-profiles`, disabled by default. Installing the whole
package would also add numerous unrelated experimental profiles and could
change unrelated services. Extracting one profile dynamically would make
Catch depend on changing package contents and additional package-extraction
tooling. Both variants are rejected.

### Operator-managed profile only

Catch could retain the current failure and print installation instructions.
This has the smallest host-policy ownership surface, but a fresh server could
still report successful initialization and then reject its first default-on
native workload. It does not meet the fresh-install readiness goal.

## Host Readiness Operation

`EnsureBubblewrap` remains the public Catch operation and keeps the existing
host-global `/run/yeet/bubblewrap.ensure.lock`. Internally it becomes a complete
native-sandbox host readiness transaction:

1. Validate the lock directory and acquire the existing exclusive lock.
2. Inspect the fixed `/usr/bin/bwrap` path.
3. On supported apt hosts, install the `bubblewrap` package when the binary is
   absent, then reopen and revalidate the binary.
4. Run the exact dependency namespace plan using the fixed numeric credential
   UID `65534`, GID `65534`. Do not use the caller's effective root UID or
   require a passwd entry.
5. When that probe passes, return without inspecting or editing AppArmor
   policy.
6. When it fails and the host is a compatible Ubuntu system with
   `kernel.apparmor_restrict_unprivileged_userns=1`, enter the managed-profile
   transaction below.
7. Run the non-root dependency probe again after policy activation.
8. When Yeet owns the profile, additionally require the Bubblewrap child to
   report the exact stacked Yeet profile label and zero effective
   capabilities.
9. Release the host-global lock.

The fixed `65534:65534` readiness credential exists only to prove the
unprivileged host path. Each real activation continues to run the final
rendered sandbox plan under the service's exact UID and GID. The generic probe
never replaces that service-specific proof.

The dependency probe continues to use the same namespace and runtime mount
arguments as native sandbox activation, ending in a harmless executable from
the read-only runtime view. Profile-label and capability diagnostics use
direct argv execution inside Bubblewrap; no shell is introduced.

## AppArmor Compatibility Gate

Catch may manage the profile only when all of these conditions hold:

- `/etc/os-release` identifies Ubuntu.
- The AppArmor module is enabled.
- `kernel.apparmor_restrict_unprivileged_userns` exists and equals `1`.
- `/usr/sbin/apparmor_parser` and its parent directory chain are regular or
  directory objects as appropriate, root-owned, and not group- or
  world-writable.
- The parser accepts the exact candidate profile with
  `-Q -K --abort-on-error`, which skips kernel and cache writes.
- `/etc/apparmor.d` and its parent chain are trusted directories.

If any condition is absent, Catch returns the original functional-probe
diagnostic plus host-specific operator guidance. It does not attempt another
policy mechanism.

## Managed Profile Contract

The managed path is:

```text
/etc/apparmor.d/yeet-bwrap
```

It is a root-owned regular file, mode `0644`, one hard link, with no setid bits.
The file begins with a Yeet ownership marker and policy version. Catch
recognizes exact released content hashes; a divergent or otherwise unsafe file
is operator-owned and must never be replaced or removed automatically.

The first policy version is equivalent to the profile proven on Ubuntu 24.04:

```text
# Managed by Yeet. Policy version: 1
abi <abi/4.0>,
include <tunables/global>

profile yeet-bwrap /usr/bin/bwrap flags=(attach_disconnected) {
  allow capability,
  allow file rwlkm /{**,},
  allow network,
  allow unix,
  allow ptrace,
  allow signal,
  allow mqueue,
  allow io_uring,
  allow userns,
  allow mount,
  allow umount,
  allow pivot_root,
  allow dbus,
  allow px /** -> yeet-bwrap//&yeet-unpriv-bwrap,
}

profile yeet-unpriv-bwrap flags=(attach_disconnected) {
  allow file rwlkm /{**,},
  allow network,
  allow unix,
  allow ptrace,
  allow signal,
  allow mqueue,
  allow io_uring,
  allow userns,
  allow mount,
  allow umount,
  allow pivot_root,
  allow dbus,
  allow pix /** -> &yeet-unpriv-bwrap,
  audit deny capability,
}
```

The parent profile is intentionally broad enough for Bubblewrap to construct
the requested namespaces and mounts. Every executed child is stacked into
`yeet-unpriv-bwrap`, where AppArmor denies capabilities. The existing
Bubblewrap argument plan also uses `--disable-userns`, so a workload cannot
create another user namespace after startup.

The profile does not use `flags=(unconfined)`, does not include an operator
override file, and does not grant a general shell a new profile entry point.
Its attachment is the already fixed and independently trusted
`/usr/bin/bwrap` path.

## Profile Installation and Ownership

When the final path is absent, Catch:

1. Creates an exclusive temporary file in `/etc/apparmor.d` with a trusted,
   collision-free name.
2. Writes the exact content, sets the final mode, synchronizes the file, and
   captures stable device, inode, ownership, mode, link-count, and content
   provenance.
3. Parses the temporary file with kernel and cache writes disabled.
4. Publishes it to `/etc/apparmor.d/yeet-bwrap` with an atomic no-replace
   operation and synchronizes the directory.
5. Loads it with the trusted parser using replace semantics and no cache write.
6. Confirms both profile names appear in the kernel profile inventory.
7. Runs the non-root functional, profile-label, and zero-capability probes.

If the path contains exact current managed content, Catch validates its
provenance, ensures the two profiles are loaded, and repeats the security
probes. It is safe and idempotent for concurrent callers because the complete
operation is under the existing host lock.

Future policy updates may replace only a recognized older Yeet policy. Catch
must retain exact prior file and loaded-policy provenance, validate and load
the replacement, and restore the prior file and parser state on any failure.
Unknown content is never treated as an older Yeet version.

When another operator or distribution profile already makes the generic
non-root probe pass, Catch accepts the functional host state and writes
nothing. When the generic probe fails, Catch may install its profile only if
the Yeet path is absent or recognized. The post-load child-label check makes
an ambiguous competing attachment fail closed. Catch does not unload or
rewrite another profile.

## Rollback and Durable Errors

Before profile publication, failures remove only the exact temporary file by
provenance and leave the kernel unchanged.

After a new profile is loaded, any inventory, label, capability, or functional
probe failure causes Catch to:

1. Remove the just-loaded profile definitions with the same trusted parser.
2. Confirm the two Yeet profile names are absent from the kernel inventory.
3. Remove only the exact managed file through a same-parent quarantine and
   durable-provenance check.
4. Preserve a changed or unverifiable pathname and return a joined recovery
   error rather than deleting it.

An update failure restores and reloads the exact recognized prior policy. If
the prior kernel state cannot be proven restored, Catch returns a durable
policy-recovery error and does not continue to a service mutation.

Once host readiness succeeds, the package and profile are committed
host-level prerequisites. A later service-specific source, unit, or runtime
failure does not remove them. A later Catch-install failure likewise does not
uninstall a successfully verified dependency, matching the existing
Bubblewrap package lifecycle.

## Fresh `yeet init`

For a genuinely fresh Catch installation, the existing Catch installation
lock remains the outer transaction boundary. Before constructing the Catch
installer, Catch runs the complete host-readiness operation, including the
fixed non-root probe and conditional AppArmor transaction.

A dependency failure still prevents Catch installer construction. A
successful fresh initialization therefore proves that a normal non-root
native workload can create the sandbox on that host.

When Catch already has an active generation, `yeet init` remains an upgrade:
it does not call the readiness operation and does not install Bubblewrap or
AppArmor policy. This preserves the progressive upgrade contract.

## Progressive Older-Host Activation

Every operation whose resulting active policy is `on` continues to call
`EnsureBubblewrap` before service publication or runtime change. On the first
such operation after upgrading an older host:

1. The host readiness operation installs Bubblewrap if needed.
2. It installs the AppArmor profile only when the compatible Ubuntu restriction
   makes the non-root probe fail.
3. The caller validates the complete sandbox policy and renders the final unit.
4. The caller probes that exact plan under the target service UID and GID.
5. Static unit verification runs.
6. Only then may the existing generation transaction publish or change
   runtime state.

The same boundary applies to a fresh native run, `service set --sandbox=on`,
start, restart, rollback, identity mutation, network mutation, root mutation,
and schedule activation when they would activate an `on` generation.

`legacy`, `off`, dormant exposure edits that explicitly remain `off`, and
non-native workloads do not call the host readiness operation.

## Permissions and User Experience

No new RPC permission is introduced. Fresh host initialization already owns
host dependency installation. Native run and service mutations that can
install the profile use the existing `manage` permission.

Successful commands may report that Yeet installed Bubblewrap and, separately,
that it installed the restricted Ubuntu Bubblewrap profile. Failure messages
name:

- the failed package, parser, file-provenance, kernel-load, functional, label,
  or capability boundary;
- whether no persistent policy was changed or rollback was completed;
- the exact managed profile path;
- an operator recovery action when a divergent profile or uncertain rollback
  requires manual inspection.

Errors never suggest disabling AppArmor, changing a global user-namespace
sysctl, or making Bubblewrap setuid.

## Testing Strategy

### Deterministic unit tests

- A root probe that passes while the fixed non-root probe fails must enter the
  AppArmor compatibility branch.
- Debian and unrestricted Ubuntu probe success must execute no parser or
  profile-file operation.
- Missing, unsafe, replaced, linked, writable, or untrusted parser and profile
  paths fail before command execution or kernel load.
- The exact candidate parses, publishes no-clobber, loads, inventories both
  names, reports the required child label, reports zero effective
  capabilities, and is idempotent.
- Parser dry-run, publication, load, inventory, functional, label, and
  capability failures each restore exact prior file and loaded state.
- A divergent managed-path file and a competing attachment are preserved and
  rejected.
- Cancellation while waiting for the host lock or between transaction phases
  performs the same exact cleanup.
- A recognized older Yeet policy updates transactionally; an unknown version
  does not.

### Catch integration tests

- Fresh Catch installation calls readiness before installer construction and
  fails when only the root probe works.
- Existing Catch upgrade never ensures the package or profile.
- Fresh native `on` and `service set` resulting in `on` use readiness followed
  by the exact target UID/GID probe before service staging.
- `legacy`, `off`, and dormant `off` edits invoke neither package nor profile
  work.
- Start, restart, rollback, identity, network, root, and schedule activation
  retain their existing fail-before-DB/runtime ordering.
- Concurrent first activations serialize to one profile publication and load.

### Linux and live proof

- On a disposable Ubuntu 24.04 host with restricted user namespaces, prove the
  unprofiled non-root failure, managed-profile success, exact stacked child
  label, zero child capabilities, denied nested user namespace, reboot-time
  profile loading, and exact cleanup/rollback branches.
- On a disposable Debian host without the restriction, prove package-only
  success and absence of profile changes.
- After automated and release gates pass, repeat one disposable native service
  on each reference host. Stop on the first mismatch. Do not migrate an
  existing production service as part of this feature proof.

## Documentation

Update the README, generated CLI help reference checks, native sandbox manual,
host setup, and troubleshooting pages to state:

- fresh initialization and first `on` activation can install the narrowly
  scoped profile on compatible Ubuntu hosts;
- ordinary Catch upgrades remain inert;
- the exact managed path and conflict behavior;
- Yeet never disables the global AppArmor or user-namespace controls;
- other distributions and operator-owned policy remain manual when the
  compatibility gate does not pass.

## Acceptance Criteria

- A fresh compatible Ubuntu host cannot complete Catch initialization while
  only root can use Bubblewrap.
- The same host completes after the exact managed profile is transactionally
  installed and the non-root security probes pass.
- A compatible Debian host uses only its Bubblewrap package.
- An older host changes package/profile state only on its first resulting-on
  activation, never on upgrade.
- Every service-specific activation still probes the exact UID/GID and final
  mount plan before DB or runtime mutation.
- The workload runs in the restricted child profile with zero effective
  capabilities and cannot create a nested user namespace.
- Global AppArmor and user-namespace policy remain unchanged.
- Divergent operator policy is preserved.
- All failure and cancellation paths either restore exact prior profile state
  or return a durable recovery error while leaving service state untouched.
