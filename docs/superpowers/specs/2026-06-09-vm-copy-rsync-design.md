# VM Copy Rsync Design

## Summary

Make `yeet copy` target the VM guest filesystem when the remote endpoint is a
VM service.

Today `yeet copy ./file svc:path` always copies to the catch-side service data
directory. That is useful for Docker, binary, and cron payloads, but it is the
wrong mental model for VMs. A user who writes `devbox:/etc/nginx/nginx.conf`
expects `devbox` to mean the machine, not yeet's host-side VM service root that
contains implementation artifacts such as `rootfs.raw`.

The VM behavior should use real `rsync` over the same SSH transport planning as
`yeet ssh <vm>`. Regular services keep the existing service-data copy behavior.

## Goals

- Make `yeet copy` copy into and out of VM guest filesystems.
- Preserve the current `yeet copy` behavior for non-VM services.
- Reuse VM SSH transport selection so copy works across `svc`, `svc,lan`, and
  LAN-only VM network modes.
- Support `--force-proxy` for VM copy, matching `yeet ssh --force-proxy`.
- Use real `rsync` for VM copy so transfers have familiar rsync semantics and
  delta behavior.
- Allow absolute guest paths for VM endpoints.
- Keep regular service endpoints relative-only and rooted at service `data/`.
- Make missing local or guest `rsync` failures clear and actionable.

## Non-Goals

- No public workflow for copying to host-side VM service data.
- No remote-to-remote copy.
- No tar-over-SSH fallback in the first implementation.
- No catch-side guest file proxy.
- No VM guest agent.
- No copy support before the VM has an SSH address.

## Command Surface

The command stays `yeet copy [OPTIONS] <src> <dst>`.

Existing examples for regular services remain valid:

```bash
yeet copy ./config.yml web:config/config.yml
yeet copy ./configs/ web:config/
yeet copy web:config ./config
```

VM endpoints use the same `svc:path` syntax but target the guest filesystem:

```bash
yeet copy ./app devbox:~/app
yeet copy ./nginx.conf devbox:/etc/nginx/nginx.conf
yeet copy devbox:/var/log/cloud-init.log ./logs/
yeet copy ./configs/ devbox:~/configs/
yeet copy --force-proxy ./configs/ devbox:~/configs/
```

For VM endpoints:

- `devbox:/absolute/path` is allowed and means the guest absolute path.
- `devbox:~/path` is passed through to rsync/SSH for guest shell expansion.
- `devbox:relative/path` is allowed and uses rsync's normal remote-path
  behavior for the guest user.
- trailing slash behavior should match rsync.

For regular service endpoints:

- paths remain relative to the service `data/` directory.
- absolute paths remain invalid.
- `data/` path prefixes continue to normalize to the data root.

`--force-proxy` is a yeet copy flag only for VM endpoints. It should be rejected
with a clear message for regular services because it has no service-data
meaning.

## Endpoint Resolution

`yeet copy` should parse endpoints before deciding transport, then inspect the
remote endpoint's service with `ServiceInfo`.

Routing:

1. Reject local-to-local and remote-to-remote as today.
2. Resolve the remote endpoint's host from explicit `<svc>@<host>`, `--host`,
   `CATCH_HOST`, or `yeet.toml`.
3. Fetch `ServiceInfo` for the remote service.
4. If the service type is `vm`, use VM rsync copy.
5. Otherwise use the existing service-data copy path.

This keeps the current command surface while making the behavior match the
service type.

## VM Transport

VM copy should share the VM SSH planning logic used by `yeet ssh`.

That gives copy the same network behavior:

- `svc` and `svc,lan` VMs proxy through catch to the VM management IP.
- LAN-only VMs connect directly to the guest LAN IP.
- `--force-proxy` forces the catch proxy path when a proxy-capable address is
  available.
- VM-specific known-hosts options and stale-key repair remain consistent with
  `yeet ssh`.

Before running rsync, print the same transport notice shape as `yeet ssh`:

```text
Proxying VM SSH through yeet-pve1 to 192.168.100.12
```

or:

```text
Connecting directly to VM LAN IP 10.0.4.178
```

The implementation should refactor the SSH planning code enough that `ssh` and
VM `copy` both consume a shared VM connection plan instead of duplicating
network-mode logic.

## Rsync Execution

VM copy uses the local `rsync` executable.

Default behavior should map to the current documented `yeet copy` defaults:

```text
rsync -avz
```

That preserves archive mode, compression, recursion, metadata preservation, and
the current file-listing output shape.

The SSH transport should be passed through rsync's remote-shell option:

```text
rsync -avz -e "<ssh command and options>" <src> <user>@<target>:<path>
```

The generated SSH command must include the same VM options as `yeet ssh`,
including:

- guest user
- target host/IP
- proxy command when needed
- yeet VM known-hosts file
- host-key alias
- strict host key mode

For downloads, invert source and destination:

```text
rsync -avz -e "<ssh command and options>" <user>@<target>:<path> <local>
```

The implementation should pass argv directly to `exec.CommandContext` and avoid
constructing a shell command, except inside SSH `ProxyCommand` where the current
SSH planner already renders a shell-safe command.

## Dependencies

VM copy depends on:

- local `ssh`
- local `rsync`
- guest `rsync`
- guest SSH readiness

Official yeet VM images should include `rsync`. Custom images are allowed to
omit it, but the failure should tell the user what is missing:

```text
guest rsync is not available on VM "devbox"; install rsync in the guest or use an official yeet VM image
```

Local missing dependency errors should be direct:

```text
rsync CLI not found in PATH
```

No tar fallback should be added in the first implementation. Rsync is the
contract for VM copy, and falling back to a separate protocol would make
behavior harder to reason about.

## Error Handling

Important errors should be explicit:

- Missing service: keep the existing service-not-found style.
- VM has no SSH address: reuse the `yeet ssh` message that points to
  `yeet vm console <svc>`.
- Local `ssh` missing: `ssh CLI not found in PATH`.
- Local `rsync` missing: `rsync CLI not found in PATH`.
- Guest `rsync` missing: add a short guest-rsync hint to rsync's failure.
- Regular service absolute path: keep rejecting it.
- VM absolute path: allow it.
- `--force-proxy` with a regular service: reject with
  `copy --force-proxy only applies to VM services`.

Known-host repair should mirror `yeet ssh`: if a yeet-managed VM alias has a
stale key, remove the stale key and retry once.

## Data Flow

Client:

- Parse copy args and identify the remote endpoint.
- Resolve host and fetch `ServiceInfo`.
- If non-VM, use existing tar stream over catch exec.
- If VM, build a VM SSH connection plan.
- Run local rsync with generated SSH transport.

Catch:

- No new catch-side copy command is needed for VM guest files.
- Existing copy command remains responsible for regular service data copies.

Guest:

- Runs OpenSSH.
- Runs rsync server-side through the normal rsync-over-SSH mechanism.

## Tests

Add focused unit tests for:

- VM endpoint routes to rsync instead of the catch service-data copy path.
- Regular service endpoint keeps the existing copy behavior.
- VM absolute paths are accepted.
- Regular service absolute paths are rejected.
- `--force-proxy` is parsed and passed into VM SSH planning.
- `--force-proxy` with a regular service returns a clear error.
- Generated rsync args preserve trailing slash semantics.
- Generated rsync args include the VM SSH remote-shell options.
- Missing local rsync reports `rsync CLI not found in PATH`.
- Guest missing rsync adds the guest-rsync hint.
- VM copy prints the SSH transport notice.

Add live coverage on pve1 with a disposable VM:

```bash
yeet run copy-vm-test@yeet-pve1 vm://ubuntu/26.04 --net=svc,lan
yeet copy ./some-file copy-vm-test:~/some-file
yeet ssh copy-vm-test -- test -f ~/some-file
yeet copy copy-vm-test:~/some-file ./downloaded-file
yeet rm copy-vm-test --clean-data
```

Before release, also verify an official Ubuntu and NixOS VM image has `rsync`
installed:

```bash
yeet ssh copy-vm-test -- command -v rsync
```

## Documentation

Update the user manual copy section to explain service-type behavior:

- regular service endpoints target service `data/`.
- VM endpoints target the guest filesystem.
- VM copy uses rsync over the same transport as `yeet ssh`.
- `--force-proxy` is available for VM copy.
- official VM images include rsync; custom images need rsync installed.

Update VM docs with a short example for copying app files and logs.

## Acceptance Criteria

- `yeet copy ./file <vm>:~/file` writes into the running VM guest.
- `yeet copy <vm>:/path/file ./file` downloads from the VM guest.
- `yeet copy ./file <regular-service>:path` still writes into service data.
- VM copy uses the same proxy/direct decision as `yeet ssh`.
- `--force-proxy` works for VM copy.
- Official Ubuntu and NixOS VM images have `rsync`.
- Local tests cover VM and regular-service routing.
- Live pve1 VM copy succeeds in both upload and download directions.
