# Docker Update Multiple Services Design

## Goal

Make `yeet docker update` accept one or more explicit services:

```bash
yeet docker update foo bar baz
yeet docker update foo bar@qux baz
```

Each requested service should use the same compose update behavior as the
existing single-service command. Qualified targets pin an individual service to
a specific host. Unqualified targets keep the current host resolution behavior:
active host override first, then a single matching `yeet.toml` service host,
then the default catch host.

## CLI Shape

`docker update` becomes a first-class variadic service command in `pkg/cli`:

```go
type DockerUpdateArgs struct {
	Services []ServiceName `pos:"0+" help:"Service names"`
}
```

The command metadata should show:

```text
docker update <svc...> | docker update --outdated
```

Yargs already supports variadic positional tags (`pos:"0+"`), so the yargs
library does not need a behavior change for this feature. Yeet should update
its command-schema helper so `[]ServiceName` is recognized as a service
argument, not only `ServiceName`.

## Routing

The generic single-service bridge should continue handling ordinary commands
with exactly one service argument. Variadic `docker update` should not be forced
through the global single-service override path. Instead, it should reach the
client command handler with its explicit service list intact.

This keeps the bridge from becoming a hidden batch executor and makes
`docker update` own its command-specific target resolution.

`yeet docker update --outdated` keeps the existing host-wide batch behavior and
continues to reject service arguments.

## Target Resolution

The client resolves each explicit service token into a `host/service` target.

- `svc@host` resolves directly to that host and service.
- `svc` with an active host override resolves to the override host.
- `svc` with exactly one configured host in `yeet.toml` resolves to that host.
- `svc` with multiple configured hosts records an ambiguity error for that
  target and tells the user to use `svc@host`.
- `svc` with no configured host resolves to the current default catch host.

Exact duplicate resolved targets are skipped after the first occurrence.

## Execution

Updates run sequentially. The existing host preference state is process-global,
so sequential execution is the simplest correct model and matches the current
`docker update --outdated` implementation.

For multiple targets, the client prints a short marker before each service:

```text
==> host/service
```

Then it runs the same remote scoped `docker update` used by the current
single-service command. Catch remains unchanged and still receives
`docker update` with no positional service arguments.

For a single explicit service, keep the current output and do not add a marker.
Markers are only for commands with more than one resolved target.

## Errors

The command attempts every resolvable target. Per-target resolution failures and
remote update failures are collected and returned with `errors.Join`.

One failed target must not prevent later targets from updating. If any target
fails, the overall command exits non-zero after the batch finishes.

## Tests

Add focused tests for:

- `pkg/cli` metadata recognizing `docker update` as a variadic service command.
- Bridge behavior leaving variadic `docker update` service lists intact.
- Single-service `docker update` preserving existing behavior.
- Mixed targets such as `foo bar@qux baz`.
- Host resolution precedence for explicit host, active host override,
  `yeet.toml`, and default catch host.
- Ambiguous configured service names producing target errors while other targets
  continue.
- Duplicate resolved targets being skipped.
- Partial remote failures continuing and returning a joined error.
- `docker update --outdated` still rejecting service arguments.

Update README and website CLI docs to show `<svc...>` and a mixed-host example.
