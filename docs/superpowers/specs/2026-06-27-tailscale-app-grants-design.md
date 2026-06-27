# Tailscale App Grants for Yeet Access Control

## Summary

Yeet should enforce its own operation-level permissions from Tailscale app
capabilities. catch remains the security boundary because clients are not
trusted. yeet CLI and web flows can improve errors and guidance, but catch must
decide whether a caller can read state, manage services, or open catch-mediated
shells.

This is a hard breaking change. After upgrading, catch denies yeet RPC access
until the caller has the required app capability grant.

## Goals

- Enforce yeet-specific permissions from Tailscale grants.
- Use the public capability key `yeetrun.com/app/yeet`.
- Support three independent permissions: `read`, `manage`, and `ssh`.
- Keep first setup simple by recommending that `autogroup:admin` receive all
  three permissions.
- Allow operators to split permissions later with least-privilege grants.
- Produce user-friendly authorization errors that name the missing permission
  and link to yeet docs.
- Document the permission model on a dedicated security page.
- Require future feature work to classify and confirm permission requirements.

## Non-Goals

- Do not preserve the old "Tailscale ACL reachability is enough" behavior.
- Do not add a compatibility flag or legacy fallback.
- Do not build a separate yeet role system outside Tailscale grants.
- Do not use Tailscale SSH policy as the source of truth for yeet shell access.
- Do not require the `ssh` permission for VM guest SSH. VM guest login remains
  protected by SSH keys and guest network reachability.

## Permission Contract

The public grant shape is:

```json
{
  "yeetrun.com/app/yeet": [
    { "allow": ["read", "manage", "ssh"] }
  ]
}
```

The `allow` array contains independent permission strings. Permissions are not a
hierarchy: `manage` does not imply `read`, and `ssh` does not imply either.
When Tailscale returns multiple matching capability values, catch unions all
`allow` arrays before checking the operation.

Unknown permission strings should be ignored for forward compatibility. Missing,
malformed, or empty capability data should deny by default.

## Permission Semantics

`read` covers observation:

- catch and service info
- service lists
- status
- logs
- event streams
- artifact hashes
- ZFS service-root candidate discovery
- VM defaults and VM SSH endpoint metadata

`read` can expose service names, event timing, status, log output, service
metadata, and some service view data. The docs should be explicit about that.

`manage` covers high-trust mutation:

- the manage portion of first setup and `yeet init`
- deploy, run, update, and remove
- service config writes
- `rm --clean` and the individual clean-data/config paths
- catch upgrades
- registry push over the tailnet
- snapshot defaults, manual snapshots, restore, and delete operations
- VM create/start/stop/restart/resize/delete/recover operations
- Tailscale service configuration changes
- any future operation that persists, mutates, deletes, or destroys state

`ssh` covers only catch-mediated shell access:

- host shell over catch RPC
- host command over catch RPC
- non-VM service shell over catch RPC
- non-VM service command over catch RPC

VM guest SSH is the deliberate exception. `yeet ssh <vm>` needs `read` to fetch
VM service info and endpoint metadata, then the normal SSH key controls guest
login. The SSH proxy path still requires the guest key because catch only proxies
the TCP stream.

## Architecture

catch remains the authoritative access-control boundary. yeet CLI and web may
preflight or polish messages, but catch must enforce every permission.

The authorization flow:

1. Preserve the existing Tailscale caller identity and catch-node validation.
2. Use catch's Tailscale LocalAPI client to call `WhoIs` for the request peer.
3. Read app capabilities from `yeetrun.com/app/yeet`.
4. Decode all capability values and union their `allow` arrays.
5. Map the requested operation to a required permission set. Most operations
   require one permission; first setup requires all three.
6. Deny by default if the method, exec target, registry path, or TTY command is
   unknown or unclassified.

The implementation should keep capability decoding separate from operation
classification. That makes it easy to unit test malformed caps, union semantics,
and future operation mappings independently.

## Operation Mapping

Read operations:

- `catch.Info`
- `catch.ServiceInfo`
- `catch.ServicesList`
- `catch.ArtifactHashes`
- `catch.ZFSServiceRootCandidates`
- `catch.VMDefaults`
- `/rpc/events`
- logs, status, info, and other non-mutating TTY commands
- VM SSH metadata lookup

Manage operations:

- `catch.TailscaleSetup`
- service install, run, update, restart, remove, and config mutation
- snapshot and recovery mutations
- VM lifecycle and storage mutations
- catch upgrade and the manage portions of init flows
- tailnet `/v2/` registry access

Shell operations:

- `/rpc/exec` with `ExecTargetHostShell`
- `/rpc/exec` with `ExecTargetServiceShell`

Admin setup operations:

- first setup and `yeet init` require `read`, `manage`, and `ssh`

For `/rpc/exec`, catch should authorize after reading `ExecRequest`, because
the required permission depends on whether the request is a normal remote
command, host shell, service shell, or VM-related lookup. Unknown exec targets
must fail closed.

For the registry, tailnet callers need `manage` for the entire `/v2/` surface.
Docker push uses read-shaped HTTP methods such as `HEAD` and `GET`, so splitting
tailnet registry access by method would create fragile partial access. The
existing catch-local loopback read-only behavior remains for containerd and
other catch internals.

## User Experience

Authorization failures should be actionable and stable:

```text
missing yeet permission "manage"; update your Tailscale grant for yeetrun.com/app/yeet:
https://yeetrun.com/docs/security/tailscale-access-grants
```

The message should name the missing permission and explain that the fix belongs
in Tailscale grants. yeet CLI and web flows should preserve this catch-side
message instead of replacing it with a generic auth, connection, or job failure.

If an operation requires multiple permissions, the failure should report the
first missing permission clearly. First setup requires all three permissions,
but should still tell the user which one is missing.

## Documentation

Add a dedicated docs page:

```text
https://yeetrun.com/docs/security/tailscale-access-grants
```

The page should start with the simple recommended grant:

- `autogroup:admin` gets `read`, `manage`, and `ssh`.
- This is the recommended first setup because it keeps the initial model easy to
  reason about.

The same page should then show advanced examples:

- read-only operators
- deploy/manage operators without shell access
- shell admins
- host-specific or group-specific scoping

Docs should link to the page from quick start, Tailscale setup, and relevant CLI
or web pages.

The page must also call out:

- local `yeet` still needs network reachability to catch, usually by running
  Tailscale on the workstation
- `read` may expose service names, events, logs, and service details
- `manage` includes registry push and destructive clean/remove operations
- `ssh` is only catch-mediated host or service shell access
- VM SSH is different and uses `read` plus normal SSH key authorization

## Future Feature Policy

Update `AGENTS.md` so future feature work explicitly handles authorization. Any
new CLI command, web action, RPC method, registry path, TTY command, background
operation, or service-management behavior must identify the required yeet
permission before implementation.

If a feature changes user-facing behavior or exposes a new operation boundary,
the agent should ask the user to confirm the intended permission unless the
permission is already specified in the request or an approved design. Tests and
docs should be updated with the new permission mapping.

## Testing

Unit tests:

- decode `yeetrun.com/app/yeet` capability values
- union multiple `allow` arrays
- ignore unknown permission strings
- deny missing, malformed, or empty caps
- classify RPC methods to `read` or `manage`
- require `read` for `/rpc/events`
- require `manage` for tailnet `/v2/`
- preserve loopback read-only registry behavior
- require `ssh` for host and service shell exec targets
- require only `read` for VM SSH metadata lookup
- fail closed for unknown RPC methods, exec targets, and TTY commands
- preserve docs-link authorization messages through CLI and web paths

Integration or smoke checks:

- with no grant, yeet RPC access fails with a useful missing-permission message
- with `read`, status/info/events work and manage/shell operations fail
- with `manage`, deploy/remove/registry push work and shell fails if `ssh` is
  absent
- with `ssh`, catch-mediated host/service shell works when granted
- with all three permissions, first setup and normal admin workflows work

Docs checks:

- examples avoid private hostnames, service names, and local paths
- generated docs audits remain clean
- the dedicated security page is linked from getting-started and Tailscale docs

## Migration

This is a hard break. Operators must update Tailscale grants before or during
the upgrade. The recommended migration is:

1. Add the documented `autogroup:admin` grant with `read`, `manage`, and `ssh`.
2. Upgrade catch and yeet.
3. Verify `yeet status`, `yeet run`, and `yeet ssh` from an admin workstation.
4. Optionally split grants into narrower groups after first setup is working.

Release notes should state the required action plainly. They should not describe
internal refactors or implementation mechanics.
