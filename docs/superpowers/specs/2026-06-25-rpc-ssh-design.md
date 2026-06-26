# RPC-Backed `yeet ssh`

## Summary

`yeet ssh` should behave like a Tailscale-authenticated shell into catch without
depending on the host SSH daemon, host passwords, or local SSH keys. The v0
implementation will reuse catch's existing tsnet RPC connection and WebSocket PTY
transport. It will move host and non-VM service shells to catch RPC while keeping
VM guest SSH on the current VM-specific OpenSSH path.

## Goals

- Make plain `yeet ssh` open a catch host shell over catch RPC and PTY.
- Make `yeet ssh -- <cmd>` run a catch host command over catch RPC.
- Make `yeet ssh <service>` and `yeet ssh <service> -- <cmd>` use catch RPC for
  non-VM services, running in the service context.
- Keep `yeet ssh <vm>` on the existing VM guest SSH behavior.
- Use the same catch RPC authorization boundary as `yeet run`, `yeet status`,
  and other privileged yeet operations.
- Remove catch-host OpenSSH from `yeet ssh`; do not add an OpenSSH fallback flag.

## Non-Goals

- Do not integrate official Tailscale SSH policy or check mode in v0.
- Do not add a separate `yeet ssh` allowlist, role system, or per-command RBAC.
- Do not add session recording.
- Do not change VM guest SSH behavior.
- Do not resurrect the private `tsnet` fork unless implementation proves upstream
  `tailscale.com/tsnet` cannot support the required behavior.

## Existing Context

Current catch already exposes `/rpc/exec` over tsnet and supports WebSocket
stdin/stdout, terminal resize messages, raw terminal handling on the client, and
catch-side PTY allocation. This is the right transport for shell sessions.

The private `yeetrun/private` repo contains two relevant historical ideas:

- HTTP-over-SSH (`sshttp`) for API transport when tsnet was unavailable. This is
  not needed for v0 because current catch already requires tsnet.
- `WhoIs`-based caller inspection for tsnet HTTP requests. That is useful for
  audit and future catch-wide policy, but v0 should not make shell access stricter
  than the rest of the catch RPC API.

Current catch already uses upstream `tailscale.com/tsnet`. The fallback TCP
handler that existed in the private fork is available upstream and is already used
by catch, so the fork is not part of this design.

## Architecture

### Client Routing

`pkg/yeet/ssh_cmd.go` remains the user-facing entry point for `yeet ssh`.

Routing rules:

- No service target: use RPC shell target `host-shell`.
- No service target plus `-- <cmd>`: use RPC shell target `host-shell` with
  command.
- Non-VM service target: use RPC shell target `service-shell`.
- Non-VM service target plus `-- <cmd>`: use RPC shell target `service-shell`
  with command.
- VM service target: keep the existing VM SSH execution plan.
- `--force-proxy` remains valid only for VM SSH behavior.

The client should reuse the existing remote exec session machinery for terminal
raw mode, terminal size discovery, resize notifications, stdin proxying, output
tracking, and remote exit handling.

### RPC Shape

Extend `catchrpc.ExecRequest` with an explicit target field:

```go
Target string `json:"target,omitempty"`
```

Supported values:

- empty: execute an existing catch service command. This is the current behavior
  and continues to require `Service`.
- `host-shell`: execute a host shell session or host command. This allows empty
  `Service`.
- `service-shell`: execute a non-VM service shell session or service-context
  command. This requires `Service`.

Preserving empty target for existing service commands avoids overloading the
normal remote command dispatcher.

### Catch Execution

`/rpc/exec` remains the WebSocket endpoint.

Request validation changes:

- `Target == "host-shell"` allows empty `Service`.
- `Target == "service-shell"` requires `Service`.
- Empty `Target` keeps the existing service command dispatcher and requires
  `Service`.
- Unknown targets are rejected before command execution.

Host target behavior:

- Interactive shell starts the configured install user's login shell when
  `InstallUser` is set and can be resolved with the local OS user database.
- If no install user shell can be resolved, use root's configured shell. If root
  cannot be resolved or has no shell, use `/bin/sh`.
- Host command mode runs the provided command directly through the PTY path when
  TTY is enabled, and through the non-PTY path otherwise.

Service target shell behavior:

- Resolve the service and reject missing services with the existing
  service-not-found wording.
- Reject VM services so they continue through the VM SSH path.
- Run interactive shells and commands from the service data/root context used by
  today's OpenSSH service shell behavior.

## Authorization

v0 uses the same authorization boundary as the existing privileged catch RPC API.
If a caller can reach and use catch RPC for `yeet run`, it can use RPC-backed
`yeet ssh`.

Catch may perform best-effort `WhoIs` lookup for logging/audit, but `WhoIs`
failure must not block shell startup when the normal RPC authorization path has
already passed.

Future catch-wide authorization can use `WhoIs` identities, but it should apply
to all privileged catch operations rather than only to `yeet ssh`.

## CLI Behavior

```bash
yeet ssh
```

Open an interactive shell on the catch host over catch RPC.

```bash
yeet ssh -- uname -a
```

Run a host command over catch RPC.

```bash
yeet ssh jellyfin
```

Open an interactive shell in the non-VM service context over catch RPC.

```bash
yeet ssh jellyfin -- ls -la
```

Run a command in the non-VM service context over catch RPC.

```bash
yeet ssh hermes
```

Keep the existing guest SSH behavior for VM services.

```bash
yeet ssh --force-proxy hermes
```

Keep the existing VM-specific proxy behavior.

There is no `--host-ssh` fallback. Bootstrap and recovery through traditional SSH
remain outside `yeet ssh`, primarily through `yeet init root@host` and manual
operator access.

## Error Handling

- If the local client supports RPC shell but catch is too old, fail with a clear
  upgrade message such as:
  `catch on yeet-lab is too old for RPC shell; run yeet upgrade`.
- If a target service is missing, preserve existing service-not-found wording.
- If a service context path cannot be resolved, return the path/config error
  directly.
- If shell startup fails, include the shell or command that failed.
- If the WebSocket disconnects, preserve current terminal cleanup and remote exit
  behavior.
- Do not silently fall back to OpenSSH.

## Testing

Unit tests:

- Client routing:
  - `yeet ssh` creates a host-target RPC shell request.
  - `yeet ssh -- <cmd>` creates a host-target RPC command request.
  - `yeet ssh <non-vm-service>` creates a service-target RPC shell request.
  - `yeet ssh <non-vm-service> -- <cmd>` creates a service-target RPC command
    request.
  - `yeet ssh <vm>` keeps the VM SSH plan.
  - `--force-proxy` remains VM-only.
- RPC validation:
  - host target allows empty service.
  - service target requires service.
  - empty target plus service remains compatible.
  - unknown target is rejected.
- Catch execution:
  - host shell command construction chooses a usable shell.
  - host command execution wires stdin/stdout/stderr through existing PTY helpers.
  - service shell resolves cwd/context correctly.
  - VM service shell target is rejected catch-side if reached directly.

Live smoke on `yeet-lab`:

- `yeet ssh -- pwd`
- `yeet ssh -- whoami`
- `yeet ssh <compose-service> -- pwd`
- `yeet ssh <vm>` still reaches the guest.

## Documentation

Update user-facing docs and CLI help to say:

- `yeet ssh` uses catch over Tailscale for host and non-VM service shells.
- `yeet init` still uses traditional SSH for bootstrap.
- VM services still use guest SSH.
- Password prompts from the catch host should no longer be part of normal
  `yeet ssh` use.
