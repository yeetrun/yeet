# Service Identity Runtime and Account Contract Design

## Goal

Make `yeet service set <service> --run-as=<identity>` safely migrate a running
native service without losing its stable runtime artifacts, then unify the
static host-account contract for `yeet-svc` and `yeet-vm` so both accounts use
the absent `/nonexistent` home path.

## Context

The first live migration attempt used `rssbot` on `yeet-pve1`. Catch stopped
the service, backed up and removed the stable `run/rssbot` and `run/env`
artifacts, updated ownership and the systemd unit, and then tried to start the
service. Identity-only `service set` did not provide a generation staging
callback, so the runtime artifacts were never recreated. Systemd rejected the
start because `ConditionFileIsExecutable` failed. The migration transaction
then restored the old unit, files, ownership, database identity, and running
state.

The host account inspection also found two separate provisioning paths:

- `yeet-svc` is created with `/nonexistent`, no home directory, and a nologin
  shell.
- `yeet-vm` is created without an explicit home path, allowing `useradd` to
  record a distro-dependent path such as `/home/yeet-vm` even though the home
  directory is not created.

Both accounts should have the explicit `/nonexistent` passwd entry.

## Considered Approaches

### 1. Restage in the identity transaction engine

The migration engine could infer the current service generation whenever the
caller omits staging details. This centralizes the safeguard, but gives a
generic transaction layer hidden knowledge of service installation and makes
root-copy, network-plus-identity, and redeploy callers harder to reason about.

### 2. Prepare a complete identity migration request at `service set`

The identity-only `service set` path can load the persisted generation, clone
it with the target identity, build its native installer, capture its install
intent, and provide the existing staged-generation callback to the migration
engine. This matches the working redeploy and network-plus-identity paths and
keeps generation knowledge at the command orchestration boundary.

This is the selected approach.

### 3. Preserve stable runtime files during identity changes

Catch could stop backing up and removing `run/<service>` and `run/env`. That
would avoid the immediate start failure, but would retain stale generation
artifacts, weaken rollback symmetry, and diverge from the installation model.

For account creation, the selected approach is a small shared static-account
contract used by both identity paths. Merely adding one VM-specific
`--home-dir` argument would fix this host but leave the duplicated policy that
caused the drift. Fully merging the two validation implementations would erase
useful workload-specific safety checks and is outside this change.

## Service Identity Migration Design

For a native identity-only `service set`, Catch will:

1. Resolve the requested target identity and any requested service-root move.
2. Load the persisted service generation and clone it with the target identity.
3. Render legacy flat runtime references in generated systemd artifacts onto
   the managed layout: the immutable generation binary under `bin/` and the
   stable managed environment under `env/`.
4. Build a native systemd generation installer from that target record.
5. Capture generation paths, pre-change install intent, and expected units.
6. Pass the existing staged-generation callback to the identity migration
   transaction.
7. Let the transaction stop and back up the old runtime, stage the generation,
   apply the ownership contract, replace the primary unit, start and verify the
   service, persist the new identity, and remove the backup.

Root-copy identity migrations retain their existing copied-root generation
setup. Network-plus-identity and redeploy paths retain their existing staging.
The fix does not introduce a new CLI, RPC method, or permission boundary;
`service set` remains a `manage` operation.

If staging, ownership, unit replacement, start, or verification fails, the
existing transaction rollback remains authoritative and restores the previous
unit, runtime artifacts, ownership, service record, root, and running state.

## Static Host-Account Contract

Introduce one internal contract for Yeet-owned static system accounts:

- an explicit account name and matching primary group;
- host-allocated nonzero system UID and GID;
- home field `/nonexistent`;
- `--no-create-home`;
- shell `/usr/sbin/nologin`;
- no unrelated supplementary group access.

Both `yeet-svc` and `yeet-vm` creation will derive their `useradd` arguments
from this contract. Both validation paths will require the passwd home field to
equal `/nonexistent`, in addition to their existing checks. Managed native
service validation will continue to check password locking, shell file safety,
and exact group membership. VM runtime validation will continue to check NSS
uniqueness and dedicated UID/GID relationships.

The existing `yeet-vm` account on `yeet-pve1` will be changed surgically with
`usermod --home /nonexistent yeet-vm` only after confirming the old home path is
absent and recording the current UID, GID, shell, groups, and running process
state. The command will not move data or alter UID/GID ownership. The resulting
passwd entry and absence of `/nonexistent` will be verified before the updated
validation is exercised. `yeet-svc` needs no host mutation if its current entry
already matches the contract.

## Testing

### Identity migration regression

Add a focused `service set` test with a real persisted native generation. The
migration test boundary will simulate removal of the legacy flat runtime
artifacts, invoke the supplied staging callback, and prove the replacement unit
uses the immutable generation binary while the managed environment is staged
under `env/`. The shared systemd install boundary will apply the same legacy
path normalization during later installs and rollbacks. The test must fail on
the current identity-only request because no staging callback is supplied.

Existing transaction tests continue to cover rollback. Focused package tests,
the full Go suite, pre-commit, and the service-orchestration destination gate
must pass before live deployment.

### Account contract regression

Update account tests to prove both creation paths emit an explicit
`--home-dir /nonexistent --no-create-home` contract and both validators reject
any other passwd home field. Keep negative tests for privileged IDs, login
shells, unsafe group relationships, and existing home directories.

## Live Rollout and Success Criteria

1. Install the patched Catch build on `yeet-pve1`.
2. Retry `rssbot` first.
3. Verify its stored identity, systemd `User` and `Group`, live process UID/GID,
   stable runtime files, ownership contract, restart health, and logs.
4. Continue one at a time with `lemonsqueezy-bot`,
   `imap-back-to-google-watch`, and `tsidp`, stopping on the first failure.
5. Update `~/yeet-services/yeet.toml` through successful `yeet service set`
   operations and verify each entry records `run_as = "yeet-svc"`.
6. Implement and validate the shared account contract.
7. Correct `yeet-vm` on the host, deploy the account validation update, and
   verify both passwd entries use `/nonexistent` without changing their UIDs,
   GIDs, shells, groups, or workload health.

The task is complete only when all four selected native services run as
`yeet-svc`, their managed files match the ownership contract, both static
accounts use `/nonexistent`, and future account creation is covered by the
shared tested contract.
