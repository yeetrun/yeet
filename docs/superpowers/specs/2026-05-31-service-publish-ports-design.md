# Service Publish Ports Design

## Summary

Add persistent publish-port management to `yeet service set` and make it
compatible with ports supplied during `yeet run`.

The user-facing model is:

```bash
yeet run nginx nginx:latest -p 80:80
yeet service set nginx -p 80:80 -p 443:443
yeet service set nginx --publish-reset -p 443:443
yeet service set nginx --publish-reset
```

`-p` remains shorthand for `--publish`, matching `yeet run`, `yeet stage`, and
Docker. The command does not add a separate `yeet ports` group in v1.

## Goals

- Let users change published ports after a service has already been deployed.
- Keep catch as the authority for the live deployed service.
- Keep `yeet.toml` in sync after successful client-initiated changes.
- Store publish ports in one durable client config shape, regardless of whether
  they came from `yeet run -p` or `yeet service set -p`.
- Avoid accidentally dropping existing published ports when a user supplies only
  a subset of the desired list.
- Show published ports in `yeet info` human output and JSON output.

## Non-Goals

- No separate `yeet ports add/list/remove/set/clear` command group in v1.
- No automatic port detection by probing containers or image metadata.
- No `-P`/publish-all support in v1.
- No new reverse-proxy, Tailscale Serve, or domain routing behavior.
- No broad rewrite of compose-file ownership. Yeet only manages the publish list
  for the primary service it already deploys.

## Existing Context

`yeet run` and `yeet stage` already accept repeated `-p`/`--publish` values.
For generated compose payloads, catch renders those values into `ports:`. For
raw compose payloads, catch already has a helper that can rewrite the selected
compose service's `ports:` list, but the client and docs currently discourage
using `--publish` for compose files.

`yeet.toml` currently stores replay arguments in `ServiceEntry.Args`. Existing
configs can therefore contain `--publish` values in `args`, but there is no
first-class field for publish ports.

Tailscale provides the closest safety precedent. `tailscale up` refuses partial
settings changes that would silently revert existing non-default settings and
prints a rerunnable command with the missing flags, or tells the user to use
`--reset`. `tailscale set` applies only explicitly mentioned fields. Yeet should
borrow the safety message pattern for publish ports.

## CLI

Extend `yeet service set <svc>` with:

- `-p PORT`, `--publish PORT`: desired publish mapping. Repeatable.
- `--publish-reset`: acknowledge replacement of the current publish list. With
  no `--publish` values, clears all publish mappings.

Examples:

```bash
yeet service set nginx -p 80:80
yeet service set nginx -p 80:80 -p 443:443
yeet service set nginx --publish-reset -p 443:443
yeet service set nginx --publish-reset
```

`--publish-reset` has no short alias. It is intentionally explicit because it
can remove externally reachable ports.

## Publish List Semantics

`--publish` on `service set` represents the complete desired publish list, not
an append operation.

If the service currently has published ports and the new list omits any of
them, yeet must refuse the change unless `--publish-reset` is present.

For example, if `nginx` currently has `80:80`:

```bash
yeet service set nginx -p 443:443
```

should fail with a message like:

```text
Error: changing published ports would remove existing mappings:
  80:80

To keep them, include them explicitly:
  yeet service set nginx -p 80:80 -p 443:443

To replace the published port list, re-run with --publish-reset:
  yeet service set nginx --publish-reset -p 443:443
```

Accepted forms:

- `yeet service set nginx -p 80:80` when no ports are currently published.
- `yeet service set nginx -p 80:80 -p 443:443` to keep 80 and add 443.
- `yeet service set nginx --publish-reset -p 443:443` to replace 80 with 443.
- `yeet service set nginx --publish-reset` to clear all published ports.

The same missing-port guard applies to `yeet run` when a redeploy supplies
explicit `--publish` values.

## `yeet run` Compatibility

`yeet run -p ...` and `yeet service set -p ...` must converge on the same
client config:

```toml
[[services]]
name = "nginx"
host = "yeet-pve1"
payload = "nginx:latest"
ports = ["80:80"]
```

Rules:

1. First deploy with `yeet run -p 80:80 nginx nginx:latest` deploys with
   `80:80` and saves `ports = ["80:80"]`.
2. Redeploy without publish flags preserves saved ports:
   `yeet run nginx nginx:latest` replays `ports`.
3. Redeploy with explicit publish flags uses the same missing-port guard:
   `yeet run nginx nginx:latest -p 443:443` errors if `80:80` is currently
   configured and `--publish-reset` is absent.
4. Redeploy with the full desired list succeeds:
   `yeet run nginx nginx:latest -p 80:80 -p 443:443`.
5. Redeploy with `--publish-reset -p 443:443` replaces the saved list.
6. Redeploy with `--publish-reset` and no publish values clears the saved list.

When saving config, publish flags are removed from `args` and written to the new
`ports` field. This prevents duplicate sources of truth.

For backward compatibility, existing configs with publish flags in `args` remain
valid. The client should read those legacy values as saved ports. The next
successful `yeet run` or `yeet service set` that touches publish ports should
migrate the entry to `ports = [...]` and remove publish/reset flags from `args`.

## Config Model

Add a first-class field to `ServiceEntry`:

```go
Ports []string `toml:"ports,omitempty"`
```

`ProjectConfig.ServiceEntry` and `SetServiceEntry` must clone and preserve this
field consistently with `Args` and snapshot fields.

The client-side effective run args are built from:

1. stored service root flags
2. stored snapshot flags
3. stored `ports`
4. remaining stored `args`
5. explicit command-line flags

Publish flags in `args` are treated as legacy input and normalized into the
same internal port list before command execution.

## Catch Model

Catch should store the normalized publish list with the service record so it can
report and sync the live desired state without parsing YAML as its primary
source:

```go
Publish []string `json:",omitempty"`
```

On install or redeploy, catch records the effective publish list that it applies
to the compose file.

On `service set -p`, catch:

1. loads the service
2. verifies it is Docker compose-backed
3. determines the current publish list
4. applies the missing-port guard unless `--publish-reset` is present
5. writes the new publish list to the compose artifact
6. records it on the service record
7. recreates/restarts the compose project so Docker and netns DNAT converge

For older services that do not yet have `Publish` set, catch must fall back to
parsing the current compose artifact's primary service `ports:` field when the
artifact is available. That fallback is for migration and display; the DB field
becomes authoritative after the next successful deploy or service setting
update.

## Compose Behavior

Yeet manages the `ports:` list for the primary compose service associated with
the yeet service name. For generated compose payloads, this is already the only
service in the file. For raw compose payloads, the compose file must contain a
service with the yeet service name if `--publish` is used.

If the primary service cannot be found or the compose file is malformed, catch
returns a clear error and makes no DB update.

When ports are changed after deployment, catch writes a new durable compose
artifact for a new service generation and installs that generation as current.
It must not mutate only transient Docker state. Restarts and catch startup
reconciliation must use the same desired port list that `service set` applied.

## Port Syntax And Normalization

V1 accepts the Docker compose short syntax already used by `--publish`:

- `HOST_PORT:CONTAINER_PORT`
- `HOST_IP:HOST_PORT:CONTAINER_PORT`
- optional `/tcp` or `/udp`

Examples:

```text
80:80
127.0.0.1:8080:80
8443:443/tcp
5353:5353/udp
```

Normalization trims whitespace, removes empty values, defaults protocol to TCP
for comparison and display, and preserves host IP when provided.

The missing-port guard compares normalized mappings. Display can omit `/tcp`
for TCP if that matches existing yeet output style, but JSON should include the
protocol explicitly.

## Info Output

Extend service info RPC with publish port data under `ServiceNetwork.Ports`:

```go
type ServicePort struct {
    HostIP        string `json:"hostIp,omitempty"`
    HostPort      uint16 `json:"hostPort"`
    ContainerPort uint16 `json:"containerPort"`
    Protocol      string `json:"protocol,omitempty"`
    Raw           string `json:"raw,omitempty"`
}
```

`yeet info <svc>` should show a compact `Ports` row in the `Network` section:

```text
Network
  IPs:
    service: 192.168.100.12
  Ports:
    80/tcp -> 80/tcp
    443/tcp -> 443/tcp
  Tailscale: disabled
  Macvlan: disabled
```

`yeet info <svc> --format=json` and `json-pretty` include the exact structured
port mappings.

## Error Handling

The client should update `yeet.toml` only after catch successfully applies the
server-side change.

If catch succeeds but `yeet.toml` cannot be updated, return a non-zero error
that states the split result:

```text
updated catch service settings, but failed to update yeet.toml: <error>
```

The command should not attempt to roll back catch automatically in v1. The
server is the live authority and the user can rerun `service sync` or the same
`service set` command after fixing the local config issue.

If there is no matching `yeet.toml` entry, follow the current `service set`
pattern: apply catch settings, then print the sync hint rather than creating an
unrelated local entry.

## Service Sync

`yeet service sync <svc>` should update the local `ports` field from catch when
catch reports port data.

If catch omits port data because the server is older, sync preserves local
`ports` and legacy publish args.

`service sync --all` follows the same update/skip behavior used for service
roots and snapshots.

## Documentation

Update user-facing docs in the website submodule:

- CLI page: document `yeet service set -p/--publish` and `--publish-reset`.
- Service Types: say `-p/--publish` can be used for image, Dockerfile, Python,
  TypeScript, and compose payloads where the primary compose service matches
  the yeet service name.
- Networking: mention that `yeet info` reports published ports and that yeet
  owns the netns DNAT state for compose-backed services.
- Troubleshooting: add the missing-port guard and `--publish-reset` recovery
  examples.

Keep README examples consistent if they mention publish ports.

## Testing

Focused test coverage should include:

- CLI parsing:
  - `service set -p 80:80`
  - repeated `-p`
  - `--publish-reset`
  - `run --publish-reset`
- Project config:
  - `ports` round trip in TOML
  - cloning/preserving `Ports`
  - saving `run -p` to `ports`, not `args`
  - legacy publish flags in `args` are honored and migrated
- Client service set:
  - no existing ports, `-p` succeeds
  - existing port omitted without reset errors with a full suggested command
  - full desired list succeeds
  - reset replaces
  - reset without ports clears
  - catch success plus local config write failure reports split result
- Catch service set:
  - rejects non-compose services
  - updates compose `ports:`
  - records service publish list
  - restarts/recreates compose service
  - old compose artifact fallback discovers existing ports
- Service info:
  - RPC includes ports
  - plain output renders ports
  - JSON output includes structured mappings
- Service sync:
  - writes `ports`
  - preserves local ports when older catch omits port data

Before PR, run targeted package tests for `pkg/cli`, `pkg/yeet`, `pkg/catch`,
`pkg/catchrpc`, and then `go test ./...`.

## Workspace

Implementation can continue in the main checkout now that the parallel agent
work has finished.
