# Copy Multiple Sources Design

## Summary

Make `yeet copy` accept the common scp/rsync command shape where every operand
except the last is a source and the last operand is the destination.

This directly fixes commands such as:

```bash
yeet copy ~/.ssh/id* hermes:.ssh/
```

The user's shell expands `~/.ssh/id*` before yeet runs, so yeet currently sees
more than two operands and returns `copy requires exactly two paths`. The CLI
should instead treat the expanded local files as multiple sources and copy them
to the final remote destination.

For VM endpoints, yeet already delegates to real `rsync` over the VM SSH path.
The multiple-source feature should preserve that model and let rsync own VM
guest path and remote glob behavior. For regular service endpoints, keep the
current yeet/catch service-data copy protocol and add only local multi-source
uploads in the first implementation.

## Goals

- Support local multi-source uploads:
  `yeet copy ~/.ssh/id* hermes:.ssh/`.
- Support VM remote-glob downloads through rsync:
  `yeet copy hermes:".ssh/id*" .ssh/`.
- Preserve existing two-operand `yeet copy <src> <dst>` behavior.
- Preserve service-data copy semantics for non-VM services.
- Reject ambiguous or unsupported shapes with clear errors.
- Keep the implementation small enough that VM behavior stays rsync-like and
  service-data behavior stays rooted in the existing catch protocol.
- Update CLI help and user docs so users understand the service-vs-VM split.

## Non-Goals

- No remote-to-remote copy.
- No local-to-local copy.
- No catch-side shell expansion.
- No regular-service remote glob support in the first implementation.
- No multi-remote-source downloads for regular services in the first
  implementation.
- No multi-service VM downloads. If VM remote multi-source downloads are
  supported, all remote sources must name the same VM service and host.
- No new catch RPC or catch-side copy protocol unless a later feature needs
  uniform service-data remote glob support.

## Command Surface

`yeet copy` should accept:

```bash
yeet copy [OPTIONS] <src>... <dst>
```

Parsing rules:

1. If there are fewer than two operands, return a clear usage error.
2. If there are exactly two operands, keep today's behavior.
3. If there are more than two operands, the last operand is the destination and
   all earlier operands are sources.
4. Exactly one side of the copy may be remote.
5. Multiple sources require a directory-like destination.

Directory-like destination hints:

- Remote destination is empty, dot-like, or trailing-slash: `svc:`,
  `svc:.`, `svc:dir/`.
- Local destination exists and is a directory.
- Local destination string ends in `/`.

Examples:

```bash
yeet copy ./a ./b svc:incoming/
yeet copy ~/.ssh/id* devbox:.ssh/
yeet copy devbox:".ssh/id*" .ssh/
```

## Endpoint Model

The current `copyRequest` has exactly one `Src` and one `Dst`. The new request
shape should keep the single destination but allow multiple sources:

```go
type copyRequest struct {
    Sources []copyEndpoint
    Dst     copyEndpoint
    // existing flags...
}
```

Compatibility helpers can keep `Src` temporarily if that minimizes churn, but
the command should have one authoritative multi-source representation before
the feature is complete. Tests should cover both parsing and execution behavior
so the internal shape does not leak into the public contract.

## VM Behavior

For VM endpoints, use the existing rsync backend.

Upload:

```bash
yeet copy ./a ./b devbox:~/incoming/
```

should invoke one rsync command shaped like:

```text
rsync -avz -e "<yeet VM ssh transport>" ./a ./b user@target:~/incoming/
```

Download:

```bash
yeet copy devbox:".ssh/id*" .ssh/
```

should invoke:

```text
rsync -avz -e "<yeet VM ssh transport>" user@target:.ssh/id* .ssh/
```

Do not pre-expand or reinterpret VM remote globs. Yeet should pass the remote
path to rsync exactly as the user supplied it after normal shell quote removal.
This keeps VM copy behavior aligned with rsync and avoids a separate yeet glob
language.

VM downloads may also accept multiple explicit remote sources only when every
source names the same VM service and host:

```bash
yeet copy devbox:.ssh/id_ed25519 devbox:.ssh/id_ed25519.pub .ssh/
```

That should become one rsync command with both remote source specs. Do not
support a single command that pulls from multiple VM services.

## Regular Service Behavior

For non-VM service endpoints, keep service-data semantics:

- Remote paths are relative to the service data root.
- Absolute remote paths remain invalid.
- `data/` prefixes continue to normalize to the data root.

Supported first-pass shape:

```bash
yeet copy ./a ./b svc:incoming/
```

Implementation should upload sources sequentially through the existing
single-item copy stream. If one source fails, stop and report which source
failed. This is simple, preserves current catch behavior, and avoids
introducing a new multi-entry catch protocol before it is needed.

Unsupported first-pass shapes:

```bash
yeet copy svc:"logs/*.txt" ./logs/
yeet copy svc:one.txt svc:two.txt ./out/
```

These should fail with a clear message such as:

```text
remote globs are only supported for VM endpoints; copy an exact service path or directory
```

## Error Handling

Important errors:

- Fewer than two operands:
  `copy requires at least one source and one destination`.
- Multiple sources with a non-directory destination:
  `copy with multiple sources requires a directory destination`.
- Remote-to-remote:
  keep `copy does not support remote-to-remote`.
- Local-to-local:
  keep `copy requires a service endpoint (svc:path)`.
- Mixed local and remote sources:
  `copy sources must all be local or all be from the same VM endpoint`.
- Multiple VM services in one download:
  `copy sources must come from one VM endpoint`.
- Regular-service remote glob:
  `remote globs are only supported for VM endpoints; copy an exact service path or directory`.

Local unmatched globs are shell-specific. For example, zsh may reject an
unmatched `~/.ssh/id*` before yeet starts. Yeet only handles the operands it
receives.

## Documentation

Update user-facing docs and CLI help:

- Show multi-source examples.
- Explain that VM endpoints use rsync into the guest filesystem.
- Explain that regular service endpoints copy to the service data root.
- Mention that quoted remote globs are VM-only in the first implementation.

The website manual should stay concise and user-facing. It should not describe
the internal catch streaming protocol beyond explaining the observable service
data root behavior.

## Verification

Unit tests:

- Parser accepts more than two operands and treats the last as destination.
- Parser rejects fewer than two operands.
- Parser rejects mixed local/remote source sets.
- Directory destination validation covers remote trailing slash, empty remote
  path, dot remote path, existing local directory, and non-directory local
  paths.
- VM rsync arg builder includes multiple local sources before the remote
  destination.
- VM rsync arg builder passes a remote glob source through for downloads.
- Regular service multi-source upload calls the existing upload path once per
  local source.
- Regular service remote glob download is rejected.

Targeted commands:

```bash
mise exec -- go test ./pkg/yeet ./cmd/yeet -count=1
```

Before integration:

```bash
mise exec -- go test ./... -count=1
pre-commit run --all-files
```

Live smoke tests, when implementation is ready:

```bash
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet copy ~/.ssh/id* <disposable-vm>:.ssh/
CATCH_HOST=yeet-lab mise exec -- go run ./cmd/yeet copy <disposable-vm>:".ssh/id*" ./.tmp-copy-smoke/
```

Use a disposable VM on `yeet-lab` for guest filesystem validation and clean it
up after the smoke test.
