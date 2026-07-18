# Service Identity and `/var/lib/yeet` Migration

## Summary

Yeet currently installs Catch state and default service roots under the Catch
install user's home directory. On the usual root installation that becomes
`/root/yeet-data`. Native binaries, scripts, and cron-style timers also run as
root because their generated systemd units do not select another identity.

Both defaults should change, but they are different changes:

- A one-time host layout migration moves the complete Yeet-managed legacy tree
  to `/var/lib/yeet`.
- A per-service identity migration changes one native workload from root to a
  managed or operator-selected account.

Fresh installations use `/var/lib/yeet` immediately. The first interactive
Catch install or upgrade that sees the exact legacy layout offers a complete
host migration, shows what will move, and asks for confirmation. The migration
copies Catch state and affected default service roots, switches the host,
verifies Catch and previously running services, and only then removes the old
legacy tree. A failure restores the old configuration and leaves the old tree
available.

Fresh native workloads use a shared static system account named `yeet-svc`.
Existing workloads keep their current identity until the operator explicitly
supplies `--run-as` through `yeet run`, `yeet cron`, or
`yeet service set <svc>`. Docker and VM execution identities remain separate
concerns.

## Goals

- Make `/var/lib/yeet` the default Catch data directory on new installations.
- Make `/var/lib/yeet/services` the default root for new service directories.
- Offer one prompted, whole-host migration for the exact legacy
  `$HOME/yeet-data` layout during the first interactive install or upgrade.
- Move all Yeet-managed Catch state and default service roots beneath the
  legacy tree, including native, Docker, VM, and Catch service roots.
- Preserve operator-selected ZFS datasets and explicit service roots outside
  the legacy default tree.
- Remove the legacy home-directory tree after the new host state has been
  verified successfully.
- Run new native binaries, scripts, and cron-style timers as `yeet-svc` by
  default.
- Let an operator select an existing user and primary group, including numeric
  UID and GID forms.
- Let an existing native service migrate through either a redeploy with
  `--run-as` or `yeet service set <svc> --run-as=...`.
- Keep Catch, Docker, VMs, DNS, Tailscale helpers, network namespace helpers,
  and host storage helpers on their required privileged execution paths.
- Make identity changes rollback-safe, including ownership, systemd unit,
  persisted state, and previous running state.
- Give failures enough context that an operator can fix the actual permission
  problem instead of guessing at it.

## Non-Goals

- Do not support systemd `DynamicUser=`.
- Do not choose UID 1000 or another fixed login-style UID for `yeet-svc`.
- Do not manage the user inside a Docker container. Compose and the image remain
  authoritative for container identities.
- Do not apply generic `--run-as` behavior to VMs. Firecracker jailer isolation
  and its `yeet-vm` identity are a separate design and implementation.
- Do not automatically move explicit custom filesystem roots outside the old
  default services root.
- Do not convert existing filesystem roots to ZFS or ZFS roots to ordinary
  filesystems as part of the identity feature.
- Do not add supplementary-group management in the first version.
- Do not automatically grant Linux capabilities, modify the privileged-port
  sysctl, or make broad host permission changes when a workload needs more
  privilege.
- Do not promise isolation between native services that share `yeet-svc`.

## Workload Policy

The execution policy is based on the host process that runs the workload, not
the source file the operator happened to upload.

| Workload | New default | `--run-as` | Notes |
| --- | --- | --- | --- |
| Native binary | `yeet-svc` | yes | Primary systemd workload only |
| Native script | `yeet-svc` | yes | Same contract as a native binary |
| Cron-style systemd timer | `yeet-svc` | yes | The timer triggers a non-root service unit |
| Docker Compose | Docker-managed | no | Configure `user:` or image metadata in Compose |
| Remote image or Dockerfile | Docker-managed | no | Generated Compose remains container-backed |
| Python or TypeScript | Docker-managed | no | Yeet currently generates container-backed services |
| VM | `yeet-vm` jailer design | no | Generic service identity does not apply |
| Catch | root | no | Host control plane |
| DNS and network helpers | root | no | Host routing, namespace, and low-port work |
| Per-service Tailscale helper | root | no | Main native workload may still be non-root |
| Host storage and mount helpers | root | no | Host filesystem administration |

The shared account reduces the blast radius from host root to Yeet-managed
native service data and the resources the host grants that account. It does
not isolate one native service from another native service using the same UID.
Operators that need that boundary create separate accounts and select them with
`--run-as`.

## Managed Service Account

Catch installation and upgrade ensure that `yeet-svc` exists before a new
native service can use it. The account has:

- a host-allocated system UID and GID;
- a locked password;
- no login shell;
- no home directory;
- no supplementary groups by default;
- a primary group named `yeet-svc`.

Yeet never claims UID 1000. That UID commonly belongs to the first human login
on an Ubuntu host, and borrowing it would turn "drop root" into "give every
service the operator's files." That is not an improvement.

If a compatible `yeet-svc` account already exists, Yeet reuses it. If the name
exists with an incompatible or privileged identity, Yeet does not rewrite the
account behind the operator's back. Installation reports the mismatch and the
properties that must be corrected.

Compatibility is deliberately strict. The existing account must resolve to a
nonzero UID, use the nonzero `yeet-svc` group as its primary group, have a
locked password and a nologin shell, point at an absent or non-created home
directory, and have no supplementary groups. This prevents an old local
account with the same convenient name from silently granting native workloads
unrelated host access.

Named identities supplied by the operator must already exist. Yeet creates
only its managed `yeet-svc` account.

## Host Layout

The default filesystem layout becomes:

```text
/var/lib/yeet/
  db.json
  registry/
  mounts/
  tsnet/
  vm-images/
  migrations/
  services/
    <service>/
      bin/
      env/
      run/
      data/
```

Catch control-plane state remains root-only. The parent paths that lead to a
service root are traversable so an operator-selected UID can reach its service,
but they are not made generally readable. Individual service roots provide the
actual access boundary.

The default top-level modes are concrete:

- `/var/lib/yeet` and `/var/lib/yeet/services` are `root:root` mode `0711`;
- root-only state directories are mode `0700`, with regular secret and state
  files mode `0600`;
- a service root is `root:<runtime-group>` mode `0750`;
- `bin/` and `env/` are `root:<runtime-group>` mode `0750`; regular generated
  files are mode `0640`, and managed executables are mode `0750`;
- `data/` and mutable `run/` state are
  `<runtime-user>:<runtime-group>` mode `0750`.

The ownership contract is:

| Path or artifact | Owner | Purpose |
| --- | --- | --- |
| Catch database, registry state, credentials, migration journals | root | Control-plane state |
| Service root | root:`<runtime-group>` | Prevent replacement of managed directories |
| `bin/` and generated executable/config artifacts | root:`<runtime-group>` | Workload can read or execute, not replace |
| `env/` and env files | root:`<runtime-group>` | Workload can read secrets, not rewrite them |
| `data/` | `<runtime-user>`:`<runtime-group>` | Persistent workload-owned state |
| Mutable runtime state | `<runtime-user>`:`<runtime-group>` | Sockets, PID files, and temporary runtime data |

Today Yeet copies managed executables and generated files into `run/`. The
implementation must stop mixing root-owned deployment artifacts with a
service-writable runtime directory. Managed artifacts stay in a root-owned
location; mutable runtime state gets its own service-writable location.

Yeet does not recursively make application files group- or world-writable. It
assigns the owners required by the contract, applies the safe defaults to
Yeet-managed paths, and preserves stricter application modes when they still
allow the selected workload identity to operate.

## One-Time Legacy Layout Migration

### Detection

The guided migration is offered when all of the following are true:

- the command is installing or upgrading Catch interactively;
- Catch reports a filesystem-backed data directory;
- that directory is the exact historical default derived from the recorded
  install user's home, such as `/root/yeet-data` or
  `/home/ubuntu/yeet-data`;
- the configured target is not already `/var/lib/yeet`;
- no incomplete earlier migration requires recovery first.

Yeet does not classify every non-ZFS path outside `/var/lib` as legacy. An
explicit `/srv/database` root may be exactly where the operator intended it to
be. The persisted host data directory, services root, and per-service root
metadata provide enough information to distinguish defaults from explicit
placement without guessing.

### Prompt

The prompt shows:

- old and new Catch data directories;
- old and new default services roots;
- services that will move;
- services and explicit roots that will remain where they are;
- estimated bytes to copy and free space at the target;
- which running services will stop and restart;
- that the old legacy tree will be removed only after verification;
- the exact recovery state if the migration fails.

The recommended action is yes, but silence is not consent. A non-interactive
install or upgrade does not migrate storage. It prints the exact `yeet host
set` command needed to perform the same migration explicitly.

### Scope

The migration moves the complete Yeet-managed legacy tree:

- `db.json` and other Catch state;
- registry and Tailscale state;
- VM image cache and Yeet-generated VM host paths;
- mount metadata, without recursively copying the contents of externally
  mounted filesystems;
- the Catch service root;
- every service whose effective root is under the old default services root.

An explicit service root outside the old default services root is recorded as
preserved. A ZFS-backed data directory, services root, or service root is also
preserved unless the operator explicitly uses the existing ZFS host-storage
migration controls.

This guided legacy-layout migration is a narrow exception to the normal host
storage cleanup rule. The operator is explicitly confirming removal of the
exact historical default tree, so cleanup is the final phase of this one
transaction. Other `yeet host set` migrations continue to preserve their old
files until the operator runs the separate storage cleanup operation. A
non-interactive upgrade prints both the explicit `yeet host set` migration
command and the follow-up cleanup command; it does not infer consent to either.

### Transaction

The guided flow reuses the host storage planner and applier rather than growing
a second migration engine:

1. Inspect the live Catch configuration and build a complete path map.
2. Reject a stale or incomplete earlier migration.
3. Preflight target space, path ancestry, mounts, and affected service state.
4. Create `/var/lib/yeet` with the final safe ownership skeleton.
5. Stop affected non-Catch services, recording which were running.
6. Copy Catch state and materialize affected service roots at the new paths.
7. Rewrite persisted absolute paths and Yeet-generated artifacts.
8. Reinstall or regenerate affected units and reload systemd.
9. Start services that were running before the migration.
10. Reinstall and restart Catch using `/var/lib/yeet`.
11. Reconnect and verify Catch paths, service states, units, and active
    references.
12. Remove the old legacy tree only after every required verification passes.

If removal fails after the host has switched successfully, the migration is
reported as incomplete rather than rolled back to the old host. The error names
the old tree and explains that it is inactive but still contains sensitive
state. The next migration attempt resumes cleanup after reconfirming that the
new host state is healthy.

### Failure and Recovery

Before the final switch, failure restores old units and previously running
services and leaves the old tree authoritative. A partially populated target
is either removed when Yeet can prove it created it or marked as an incomplete
migration target for the next attempt.

After the Catch switch but before final validation, failure reinstalls the old
Catch unit, restarts Catch from the old tree, and restores affected service
units and running state. The old tree is never deleted before this recovery
window closes.

Every error reports:

- the phase that failed;
- old and target paths;
- services stopped, restarted, or still stopped;
- whether Catch is running from the old or new tree;
- whether rollback completed;
- the safe retry or manual recovery command.

## Run-As Interface

The initial interface is:

```bash
yeet run api ./api --run-as=yeet-svc
yeet run api ./api --run-as=app:app
yeet run api ./api --run-as=1001:1001
yeet cron job ./backup "0 3 * * *" --run-as=backup
yeet service set api --run-as=yeet-svc
yeet service set api --run-as=root
```

The canonical value is `USER[:GROUP]`:

- a named user without a group uses the account's primary group;
- a named or numeric group may be supplied explicitly;
- a numeric UID that resolves to a local account may use that account's primary
  group;
- a numeric UID with no account entry must include a numeric GID;
- `root` is a supported explicit choice.

The requested identity is validated on Catch, because Catch owns the host
account database and is authoritative for remote command execution.

### New Services

A new native binary, script, or timer with no `--run-as` value is created as
`yeet-svc`. Catch persists that effective identity when the service record is
created. Docker-backed and VM services do not receive a generic service
identity record.

A new native service with `--run-as` is created directly with the selected
identity. There is no ownership migration because the service root is prepared
with the final ownership from the start.

### Existing Services

An existing service rerun without `--run-as` preserves its current identity.
An existing native service rerun with a different `--run-as` value performs the
identity migration before the new generation starts. It is not ignored and it
does not silently keep running as root.

`yeet service set <svc> --run-as=...` performs the same migration without a new
payload. The two entry points call one Catch-side migration engine.

If the service still lives under an inaccessible legacy path, the command does
not quietly begin a large storage copy. Normally the one-time host migration
has already removed that case. If it has not, the error provides either the
whole-host migration command or an explicit combined service command:

```bash
yeet service set api \
  --service-root=/var/lib/yeet/services/api \
  --copy \
  --run-as=yeet-svc
```

Combining `--service-root` and `--run-as` is one transaction. Root migration,
ownership migration, unit replacement, and service restart either all commit
or all roll back.

### Docker and VM Rejection

Supplying `--run-as` for Docker-backed work fails with guidance to configure
the image or Compose `user:` setting. Supplying it for a VM fails with guidance
that guest users and the Firecracker jailer are separate identities.

Yeet rejects these combinations rather than accepting and ignoring them. A
security flag that does nothing is worse than no flag because it gives the
operator a false answer.

## Persisted Identity and Compatibility

Catch stores one conceptual execution identity on each native service. It
contains the requested user and primary group plus the numeric UID and GID that
were resolved when ownership was applied. Names remain useful to operators;
numeric values let Catch detect account drift before it starts a workload with
data owned by a different identity.

An old native service record has no identity because the field did not exist.
Catch interprets that missing value as legacy root. This is not a user-visible
mode and it does not change the service during upgrade. Once the operator
migrates it, Catch stores the selected identity.

A new native service never relies on a missing value to mean today's default.
It stores `yeet-svc` explicitly. That distinction prevents a future default
change from rewriting history.

After a successful run or `service set`, the matching `yeet.toml` entry stores
the requested `run_as` value. Catch remains authoritative for live state. An
old `yeet.toml` entry with no `run_as` value does not change an existing
service; a service that does not yet exist receives the new `yeet-svc` default.

Service info and sync output expose:

- requested user and group;
- effective UID and GID;
- whether the service is legacy root, managed `yeet-svc`, operator-selected,
  or explicitly root;
- any mismatch between persisted identity and the host account database.

The schema change uses the normal Catch database version and migration path.
Generated DB view and clone files are regenerated rather than edited by hand.

## Per-Service Identity Migration

### Preflight

Before changing anything, Catch:

1. Locks the service against concurrent run, root migration, restore, remove,
   or another identity migration.
2. Confirms the service is a supported native workload and is not Catch.
3. Resolves and validates the requested user and group.
4. Resolves the effective filesystem or ZFS service root.
5. Confirms every parent path is traversable by the target identity.
6. Inventories the managed data and runtime trees without following symlinks or
   crossing an unapproved mount or nested dataset boundary.
7. Detects POSIX ACLs, file capabilities, setuid or setgid files, and hard links
   whose ownership change could affect a path outside the service root.
8. Checks declared privileged ports and other known incompatible resources.
9. Records current unit content, persisted identity, ownership, and running
   state.

Nested mounts and datasets are not recursively claimed. If they already grant
the target identity the required access, they may remain untouched. Otherwise
preflight stops and names the exact mount and permission the operator must fix.

### Ownership Journal

Catch writes a root-only, durable migration journal outside the service root
before the first ownership change. The journal records every inode whose UID,
GID, or Yeet-managed mode will change. It is fsynced in bounded batches so a
Catch crash or host reboot cannot turn a half-finished recursive chown into
invisible state.

For ZFS-backed service roots, Catch also creates a mandatory migration recovery
snapshot before mutation. The ownership journal remains the normal rollback
mechanism because blindly rolling a dataset backward can collide with newer or
externally created snapshots. The ZFS snapshot is the recovery handle if the
journal cannot restore the filesystem completely.

### Apply and Commit

The migration proceeds as follows:

1. Stop the service if it is running.
2. Create and seal the ownership journal and ZFS recovery snapshot when
   applicable.
3. Apply the ownership contract to service data, mutable runtime paths, and
   root-managed readable artifacts.
4. Generate and validate the replacement primary systemd unit with `User=` and
   `Group=`. Privileged auxiliary units remain root.
5. Install the replacement unit and reload systemd.
6. Start the service only if it was running before the migration.
7. Verify the primary unit and expected running state.
8. Commit the new identity to Catch state.
9. Mark the journal complete and remove it after successful verification and
   Catch-state commit. A ZFS recovery snapshot enters the normal snapshot
   retention policy rather than being destroyed inline.

The client updates local `yeet.toml` only after the remote operation succeeds.
If that local write fails, the error says that the remote identity committed
and gives the exact config repair; it does not roll back a healthy remote
service merely because the client filesystem could not be updated.

For a stopped service, verification checks unit validity and access as the
target identity but leaves the service stopped.

### Rollback

Any failure before commit triggers rollback:

1. Stop a partially started replacement unit.
2. Reinstall the previous unit and reload systemd.
3. Restore ownership and modes from the durable journal.
4. Restore the previous Catch identity record.
5. Restart the old service only if it was previously running.
6. Verify and report the restored state.

If rollback itself fails, Catch does not hide that behind the original error.
It reports both failures, preserves the journal and ZFS snapshot, names the
current unit and identity, and gives the operator the exact recovery paths.

## Systemd and Operational Paths

Only the primary native workload unit receives `User=` and `Group=`. These
remain root:

- Catch;
- network namespace setup and teardown;
- DNS and firewall helpers;
- per-service Tailscale daemons;
- mount and storage helpers.

The main service can still depend on those units. systemd prepares namespace,
mount, and dependency state before starting the unprivileged process.

The selected identity also applies to operational paths that otherwise create
root-owned surprises:

- `yeet copy` writes application data with the service's runtime ownership;
- `yeet service shell` drops UID and GID, uses the service data directory as
  its working directory and home, and does not grant Catch's supplementary
  groups;
- binary, generated configuration, and environment updates remain root-owned
  and readable by the runtime group;
- restore and rollback paths reapply the persisted ownership contract;
- ordinary redeploys preserve the existing identity unless `--run-as` is
  explicitly supplied.

Service shell continues to require the existing `ssh` permission. Changing
identity, ownership, or service configuration requires `manage` permission.

## Privileged Ports and Other Privilege Failures

A declared native host port below 1024 is rejected before deployment when the
target identity is non-root. The error names the port and offers practical
choices:

- configure the application to use an unprivileged port;
- place a root-owned reverse proxy in front of it;
- explicitly use `--run-as=root`.

Yeet does not automatically grant `CAP_NET_BIND_SERVICE` or change
`net.ipv4.ip_unprivileged_port_start`. Those are useful tools, but silently
granting or changing them would turn a simple default into a second privilege
policy.

A binary can bind a privileged port without declaring it to Yeet, so static
validation cannot catch every case. When systemd reports a permission failure,
the deployment error includes recent journal output and points to the selected
identity, service root, privileged ports, devices, and host paths as likely
causes. The service remains on its previous generation or is rolled back.

## Error Reporting

Migration errors are structured around the operator's actual recovery job.
They include:

- service and workload type;
- previous and requested user, group, UID, and GID;
- service root and ZFS dataset when present;
- failed phase;
- first blocking path, mount, ACL, capability, or port;
- whether ownership rollback completed;
- whether the previous unit was restored;
- whether the service was restarted or remains stopped;
- durable journal and ZFS recovery snapshot when manual recovery is required;
- one safe retry command.

The command should not end with a generic `permission denied` after changing
ten thousand inodes. If Catch knows which state changed, it should say so.

## Testing

### Parsing and Routing

- Parse `USER`, `USER:GROUP`, `UID:GID`, and explicit root forms.
- Reject missing, malformed, unknown, Docker, VM, and Catch targets.
- Route `yeet run`, `yeet cron`, and `yeet service set` to one Catch-side
  identity migration path.
- Preserve identity when an existing service is rerun without `--run-as`.
- Update `yeet.toml` only after remote success.
- Verify permission mapping: `manage` for mutation and `ssh` for service shell.

### Persisted State

- Migrate old DB records to legacy-root semantics without changing units.
- Persist explicit `yeet-svc`, operator-selected, and root identities.
- Detect name-to-UID or group-to-GID drift.
- Regenerate DB views and clone helpers.
- Sync live identity into matching project config entries.

### Filesystem and Systemd

- Verify the `/var/lib/yeet` ownership skeleton and private Catch state.
- Verify root-owned executable and env artifacts cannot be replaced by the
  runtime user.
- Verify data, runtime writes, copy, shell, restore, and redeploy use the
  persisted identity.
- Verify primary units receive `User=` and `Group=` while auxiliary units stay
  root.
- Verify timers execute their service unit as the selected identity.
- Verify declared privileged ports fail before mutation for non-root services.

### Migration and Recovery

- Migrate root to `yeet-svc`, one operator user to another, and back to root.
- Preserve previous stopped or running state.
- Cover failures during journal creation, ownership change, unit install,
  daemon reload, start, verification, DB commit, and rollback.
- Recover an interrupted migration from a durable journal after Catch restart.
- Reject or explain symlinks, nested mounts, nested datasets, ACLs,
  capabilities, setid files, and external hard links.
- Cover ordinary filesystems and ZFS-backed service roots.
- Verify a combined root and identity migration commits or rolls back as one
  operation.

### One-Time Host Migration

- Offer the prompt only for the exact legacy default layout.
- Do not prompt for `/var/lib/yeet`, ZFS roots, or explicit custom roots.
- In non-interactive mode, print the explicit host migration command without
  changing state.
- Copy all Catch state and affected default service roots.
- Preserve explicit custom roots and ZFS datasets.
- Do not traverse external mounts while copying host state.
- Restore the old host on every failure before final verification.
- Remove the old legacy tree only after Catch, units, references, and affected
  service state validate successfully.
- Resume cleanup safely when deletion alone was interrupted.

### Live Verification

Use a disposable systemd host for end-to-end coverage of account creation,
native service execution, shell/copy ownership, migration rollback, privileged
port diagnostics, and the prompted `/root/yeet-data` to `/var/lib/yeet`
migration. Add a ZFS-capable host test for dataset preservation, recovery
snapshots, and nested dataset handling before release.

## Rollout

Implement this in bounded stages:

1. Add the `/var/lib/yeet` layout and managed `yeet-svc` account for fresh
   installs without changing existing service identities.
2. Persist native service identities and render primary systemd units with
   `User=` and `Group=`.
3. Apply the ownership contract to new native services and operational copy,
   shell, restore, and redeploy paths.
4. Add rollback-safe per-service identity migration through `service set`,
   rerun, and cron.
5. Add the prompted one-time legacy host migration using the existing host
   storage planner and applier.
6. Add privileged-resource diagnostics, service info, sync behavior, public
   documentation, and live migration verification.

Existing services remain root throughout stages one through three unless the
operator explicitly selects another identity. The guided host layout migration
moves storage, not execution identity. Keeping those state transitions separate
is what makes both of them explainable and recoverable.
