# Preferred Shells for Non-VM Service Sessions

## Summary

Interactive `yeet ssh <service>` sessions should use a usable preferred shell
from the host instead of always starting `/bin/sh`. Shell selection must remain
separate from execution identity: native service shells continue to run as the
service's persisted UID and GID, while Docker Compose service shells continue
to run as Catch's host identity. `/bin/sh` remains the universal fallback.

This changes only interactive non-VM service sessions with no explicit remote
command. Host shells, VM guest SSH, and `yeet ssh <service> -- <command>` keep
their current behavior.

## Goals

- Give interactive non-VM service sessions the host's preferred interactive
  shell when it is safe and executable.
- Preserve native service credentials, supplementary-group clearing, working
  directory, home directory, and authorization behavior.
- Preserve Docker Compose service-shell host identity and working directory.
- Keep `/bin/sh` as a reliable fallback on minimal hosts.
- Make the host-side nature of regular service shells clear in user-facing
  documentation.

## Non-Goals

- Do not enter a Docker container automatically.
- Do not change Docker Compose service shells from host root to a container or
  inferred file-owner identity.
- Do not hide the root `#` prompt or otherwise customize shell prompts.
- Do not add a `--shell` flag, preference, RPC field, or client-side shell
  negotiation.
- Do not change VM guest shell selection.
- Do not change explicit command execution after `--`.

## Current Behavior

The client routes host and non-VM service sessions through Catch's RPC-backed
PTY transport. Catch treats them differently:

- A host session resolves the configured Catch install user's login shell,
  then root's login shell, then `/bin/sh`.
- A non-VM service session always starts `/bin/sh` in the service data
  directory.
- A native service session additionally adopts its persisted UID and GID,
  clears supplementary groups, and sets its service data directory as `HOME`.
- A Docker Compose service session does not have a persisted host workload
  identity, so it retains Catch's host identity, normally root.

On `yeet-pve1`, `/bin/sh` resolves to `dash`. Dash's default prompt is `$` for
the native `yeet-svc` identity and `#` for the Compose root identity. The
different prompt characters therefore describe privilege, not different
service types or container shells.

## Shell Selection

Shell selection applies only when the service-shell request has no explicit
command.

Catch resolves candidates in this order:

1. For a native systemd service, the persisted service account's configured
   login shell.
2. The configured Catch install user's login shell.
3. Root's configured login shell.
4. `/bin/sh`.

Docker Compose services have no persisted host workload account, so their
selection starts at step 2.

A configured shell candidate is usable only when its path is non-empty, exists,
is not a directory, and has at least one executable mode bit. Catch must reject
known non-interactive account shells whose basename is `nologin` or `false`.
An unusable candidate is skipped rather than returned as an error. `/bin/sh` is
the final compatibility fallback and preserves the current error behavior if it
cannot be started.

The selected program starts as an ordinary interactive shell attached to the
existing PTY. Service shells do not force login-shell mode. This preserves the
existing distinction from host shells and avoids introducing login-profile
semantics into service data contexts.

## Identity and Environment

Shell choice never chooses process credentials.

For native services, Catch continues to:

- validate the persisted service identity against the host account database;
- set the persisted UID and GID;
- clear supplementary groups;
- use the service data directory for both the working directory and `HOME`;
- set `USER` and `LOGNAME` to the requested service account.

`SHELL` changes from the hardcoded `/bin/sh` to the selected shell path.

For Docker Compose services, Catch continues to use its existing host identity,
environment, and service data working directory. Catch replaces only `SHELL`
with the selected shell path. It does not invent a container user or change
`HOME`. The selected shell may resolve missing identity environment from the
host account database as it normally would.

The root `#` prompt for a Compose session remains correct and desirable because
it signals host-root authority. Prompt rendering, history, completion, and
startup-file behavior remain owned by the selected shell rather than Yeet.

## Explicit Commands and Other Targets

When command arguments are present, Catch continues to execute the supplied
program and argv directly. It does not wrap them in the selected shell and does
not apply interactive shell selection. Native explicit commands retain their
current service environment, including `SHELL=/bin/sh`; Compose explicit
commands retain Catch's existing environment.

Plain `yeet ssh` continues to use the Catch install user's or root's login shell.
VM services continue through guest OpenSSH and use the guest operating system's
shell policy.

## Authorization

The operation remains an `ssh`-permission operation. This change adds no new
user-facing operation boundary and does not change `read` or `manage`
permissions.

## Documentation

The CLI manual should state that:

- interactive regular service sessions select a usable service or host
  preferred shell with `/bin/sh` fallback;
- native sessions retain the service UID and GID;
- Docker Compose sessions are host-side shells in the service data directory,
  not shells inside a container, and normally retain Catch's root identity;
- a `#` prompt therefore accurately signals root authority.

## Testing

Focused Catch tests should prove:

- a native service with an interactive configured account shell uses it;
- the managed `yeet-svc` `nologin` shell falls back to the Catch install user's
  preferred shell while retaining the native UID, GID, environment, and PTY
  attributes;
- a Docker Compose service uses the Catch install user's preferred shell while
  retaining Catch's identity and the service data working directory;
- `nologin`, `false`, missing, directory, and non-executable candidates are
  skipped;
- missing account records ultimately select `/bin/sh`;
- `SHELL` matches the selected shell for interactive sessions;
- explicit service commands remain exact and bypass interactive shell
  selection;
- host and VM routing remain unchanged through their existing tests.

Run the focused `pkg/catch` shell tests while iterating, then the complete
`pkg/catch` package tests. Because this is user-facing service-shell behavior,
run the repository's normal pre-commit gate once the implementation and manual
update are stable.

## Expected Live Result on `yeet-pve1`

With root configured for `/usr/bin/fish` and `yeet-svc` configured for
`/usr/sbin/nologin`:

- `yeet ssh rssbot` starts fish as UID 997/GID 993 with the RSS bot data
  directory as its working directory and home;
- `yeet ssh sonarr` starts fish as UID 0 in the Sonarr data directory;
- the native prompt remains non-root and the Compose prompt remains root;
- `yeet ssh <service> -- /bin/sh -c ...` continues to run exactly that command.
