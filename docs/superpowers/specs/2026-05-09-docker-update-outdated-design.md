# Docker Update Outdated Design

## Goal

Make Docker update discovery and batch updating usable in normal terminals.

## Outdated Output

`yeet docker outdated` keeps JSON as the exact machine-readable output,
including full running and upstream digests.

The default table output becomes compact:

```text
SERVICE  HOST       CONTAINER  IMAGE                         UPDATE
media    host-a     media      linuxserver/media:latest      update
```

Scoped remote table output uses the same columns without `HOST`.

The table hides raw digest columns and formats image references for display:

- remove a registry hostname when present
- show an implicit `:latest` tag when no tag is declared
- shorten digest-pinned references to a short digest suffix
- cap long image text so rows stay readable in a reasonable terminal

## Update Outdated

`yeet docker update --outdated` is a host-wide batch mode. It scans the selected
host set, finds rows whose status is `update available`, deduplicates by service,
and runs the existing scoped `docker update` workflow once per outdated service.
Before each service update it prints a short `==> host/service` marker, then
streams the same remote output as `yeet docker update <svc>`.

`--host` narrows the host set to one host. Project config without `--host` fans
out across configured hosts, matching `yeet status` and `yeet docker outdated`.

`yeet docker update --outdated <svc>` is intentionally invalid. Individual
services should use the existing `yeet docker update <svc>` command.

Rows with `unknown` or `error` status are not updated. A host with no outdated
services is a no-op and reports that no updates were found.

## Errors

Discovery failures and per-service update failures are reported inline with the
same short marker format. The command returns a non-zero error if any host scan
or service update fails after attempting the rest of the batch.
