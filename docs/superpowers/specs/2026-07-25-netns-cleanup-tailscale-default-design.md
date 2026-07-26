# Network Namespace Cleanup and Tailscale Default Design

## Goal

Make `yeet remove --clean` leave no per-service network namespace resolver
state, and make newly deployed Tailscale-enabled services use version
`1.101.284`.

## Confirmed failures

`service-ns cleanup` removes the named network namespace but leaves
`/etc/netns/yeet-<service>-ns/resolv.conf`. A live disposable deployment
reproduced the leak. A host audit found 47 stale resolver
directories from removed services and 21 directories belonging to live
services.

Existing services on the live Catch host were upgraded to Tailscale `1.101.284`, but
the installer still defaults new services to `1.77.33`.

## Design

The namespace script will expose an internal `NETNS_ETC_DIR` override that
defaults to `/etc/netns`. Cleanup will delete only the namespace's
`resolv.conf`, then remove the namespace directory only when it is empty. It
will not recursively delete the directory, so an unexpected file cannot be
silently destroyed. Cleanup remains idempotent.

The Catch installer will define one `defaultTailscaleVersion` constant with
the value `1.101.284` and use it when `--ts-version` is not provided. Explicit
version requests remain unchanged.

The historical host cleanup is an operator action, not a startup
migration. A directory is eligible only when its service root, systemd unit,
active namespace, and namespace mount are all absent and its only file is
`resolv.conf`.

## Tests

The namespace package will execute the real embedded shell script against a
temporary resolver root and a fake `ip` command. Tests will prove that cleanup
removes the expected resolver directory and preserves an unexpected sibling
file while deleting `resolv.conf`.

The Catch package will assert the observable version selected for a newly
constructed Tailscale network, including preservation of an explicit override.

Focused package tests run during development. The full Go suite, pre-commit
gate, and `quality:goal` run once on the stable release candidate.

## Live and release verification

A custom Catch build will be installed on the target host. A uniquely named
`nginx:latest` service will be deployed with `--net=ts,svc`, removed with
`--yes --clean`, and audited across Catch, ZFS, systemd, Docker, netns,
Tailscale, and authoritative DNS. The 47 proven-stale resolver directories
will then be removed with the same safety predicates.

The next patch release is `v0.10.10`. Its changelog will describe complete
namespace cleanup and the updated Tailscale default. After publication, the
official release artifact will replace the custom Catch build and the live
verification will be repeated.
