# Update Awareness And Upgrade Design

## Context

Yeet has two public binaries that need to move together:

- `yeet`, the local CLI a user runs from their workstation.
- `catch`, the daemon installed on one or more remote hosts.

Today a user can install yeet from the public install script and can update
catch by running `yeet init` again. That is functional, but it leaves a user
who is several patch releases behind with no clear signal that a newer public
version exists and no single workflow that answers, "what is outdated and how
do I update it?"

The repository already has the important distribution pieces: GitHub release
assets for yeet and catch, per-asset SHA256 files, `yeet init --from-github`
for catch installation, catch build versions injected by release workflows, and
project configs that know about multiple catch hosts.

## Goals

- Tell interactive users when a newer public yeet release is available without
  slowing down normal commands or making GitHub availability part of the command
  success path.
- Provide an explicit upgrade workflow that can update the local yeet CLI and
  one or more catch hosts.
- Support multiple catch hosts discovered from the current project
  `yeet.toml`, default prefs, and explicit `--host` arguments.
- Reuse GitHub release assets and checksums instead of introducing a second
  distribution channel.
- Keep source/dev builds predictable: report that they are not self-updatable
  as release binaries instead of overwriting unknown local state.
- Document the public user workflow clearly.

## Non-Goals

- Automatically upgrade catch hosts in the background.
- Scan every Tailscale catch host after every command.
- Replace package-manager workflows if a future Homebrew or system package
  install method is detected.
- Add signed releases in this pass. SHA256 verification from release assets is
  required; signatures can be added later.
- Support downgrades as a default workflow.

## Recommended Approach

Build a small, reusable upgrade subsystem with two user-facing surfaces:

1. Passive update awareness: after normal interactive commands, show a short
   stderr notice when cached release data says the local yeet binary or a
   recently observed catch host is behind.
2. Explicit upgrade commands: `yeet upgrade check` reports current/latest state,
   and `yeet upgrade` performs a deliberate update.

This keeps the main CLI fast and resilient while giving users a clear path when
they are behind.

## Version Model

Introduce a shared build/version package, for example `pkg/buildinfo`, used by
both `cmd/yeet` and `cmd/catch`.

It should expose:

- release version, injected as `vX.Y.Z` by release workflows;
- commit fallback for dev builds;
- dirty state for dev builds;
- channel classification: stable release, nightly, dev, unknown.

Release workflow changes should stamp both yeet and catch with the same tag.
Today catch is stamped and yeet is not. That gap should be closed before yeet
can accurately decide whether the local CLI is behind.

Stable semver releases are compared against GitHub's latest release. Nightly
builds compare only against the `nightly` release. Dev builds may show latest
stable in `yeet upgrade check`, but passive banners should avoid telling source
build users to replace their local binary.

## Commands

### `yeet version`

Keep `yeet version` as the quick version view, but make it more useful:

- local yeet version and channel;
- selected catch host version if reachable;
- latest stable version if cached or fetched quickly;
- JSON output with the same fields.

The command should remain useful even when GitHub or catch is unreachable.
Unreachable fields should be shown as unknown with a concise reason.

### `yeet upgrade check`

Add an explicit, non-mutating check:

```text
yeet upgrade check
yeet upgrade check --all
yeet upgrade check --host catch-a
yeet upgrade check --json
```

Default scope checks the local yeet binary and the currently selected catch
host. `--all` checks the local yeet binary plus all known project/prefs hosts.
Each remote probe should have a short timeout and should produce a per-host row:

```text
COMPONENT       CURRENT    LATEST     STATUS
yeet            v0.5.10    v0.5.13    update available
catch@edge-a    v0.5.13    v0.5.13    current
catch@edge-b    v0.5.8     v0.5.13    update available
catch@lab       unknown    v0.5.13    unreachable: dial timeout
```

### `yeet upgrade`

Add the mutating workflow:

```text
yeet upgrade
yeet upgrade --host catch-a
yeet upgrade --all
yeet upgrade --check
yeet upgrade --yes
```

Default scope upgrades the local yeet CLI if it is a release-installed binary,
then upgrades the current catch host. `--all` upgrades project/prefs catch hosts
after upgrading the local CLI.

The command should present a concise plan and ask for confirmation unless
`--yes` is set:

```text
Upgrade plan:
  yeet: v0.5.10 -> v0.5.13
  catch@edge-a: v0.5.10 -> v0.5.13
  catch@edge-b: v0.5.8 -> v0.5.13

Proceed? [y/N]:
```

Local self-upgrade should download the matching `yeet-<goos>-<goarch>.tar.gz`
asset, verify the `.sha256` file, install atomically, and re-exec the new
binary before catch upgrades when possible.

Catch upgrades should reuse the existing `yeet init --from-github` path. The
preferred SSH target comes from catch's recorded `installUser` and
`installHost`. If that metadata is absent, the command should stop for that
host with an actionable message asking the user to run `yeet init
root@<machine-host> --from-github` to refresh catch and record the install
target.

## Passive Advisory

After normal interactive commands complete successfully, yeet should be able to
print one short notice to stderr:

```text
Update available: yeet v0.5.10 -> v0.5.13; catch@edge-b is behind.
Run: yeet upgrade check --all
```

Rules:

- Never block command execution on GitHub.
- Never show during JSON output, help output, `version`, `upgrade`, shell
  completion, non-TTY output, or when `YEET_NO_UPDATE_CHECK=1` is set.
- Do not live-probe every project host from the passive path.
- Use local version, cached latest release data, and any catch version already
  observed by the current command.
- Rate-limit notices per latest version, for example once per day.
- Keep text to one or two lines.

If the current project has multiple known hosts but they have not been probed,
the banner should avoid guessing. It can say:

```text
Update available: yeet v0.5.10 -> v0.5.13.
Run: yeet upgrade check --all to scan 3 project catch hosts.
```

## Host Discovery

Host discovery should use existing user intent signals in this order:

- explicit `--host` or host-qualified service arguments for the current command;
- `CATCH_HOST`;
- prefs default host;
- hosts in the current project `yeet.toml`, using `ProjectConfig.AllHosts()`;
- service entries in `yeet.toml`, also covered by `AllHosts()`.

`yeet list-hosts` remains the Tailscale discovery command. Upgrade should not
implicitly scan the whole tailnet. A future `yeet upgrade check --tailnet` can
compose with `list-hosts` if that becomes useful.

## Cache And State

Store update metadata under the existing yeet config directory, for example:

```text
~/.yeet/update-check.json
```

The cache should contain:

- latest stable tag, published timestamp, and release asset metadata;
- latest nightly tag when the current binary is nightly;
- ETag or last-modified values if GitHub provides them;
- last successful check time;
- last advisory display time per latest version;
- cached catch host observations with timestamp and version.

The cache is advisory only. Corrupt or unreadable cache files should be ignored
and rewritten. A failed update check should not make unrelated commands fail.

## Release Assets And Verification

The existing release workflows already publish yeet and catch tarballs plus
per-asset SHA256 files. The upgrade code should use those same assets.

Workflow changes needed:

- stamp yeet release binaries with the release version;
- stamp nightly yeet binaries with the nightly version string;
- keep publishing yeet and catch tarballs plus SHA256 files;
- optionally publish a compact `manifest.json` later, but do not require it for
  this pass because the GitHub Releases API already lists assets.

All downloaded binaries must be checksum-verified before install. Local binary
replacement should be atomic on the same filesystem and should preserve execute
permissions.

## Error Handling

Normal commands:

- update-check failures are silent unless debug logging is enabled;
- passive advisory failures never change exit status.

Explicit checks:

- GitHub failures are shown clearly and return non-zero only when the user asked
  for a check;
- unreachable catch hosts are per-host failures in `--all`, not a reason to hide
  successful rows for other hosts.

Mutating upgrades:

- show a plan before changing anything;
- fail before mutation if the local asset or checksum cannot be resolved;
- report partial success explicitly;
- do not remove or rewrite catch service data;
- if the local CLI updates successfully but a catch host fails, print the exact
  retry command for that host.

## Security And Robustness

- Verify SHA256 for every downloaded asset.
- Refuse to self-update unknown install shapes unless an explicit safe path is
  known.
- Avoid shelling through unescaped host, path, or asset values.
- Keep GitHub API timeouts short.
- Respect `GITHUB_TOKEN` for higher API limits, matching existing release-fetch
  behavior.
- Do not leak private project hostnames into public docs or committed examples.

## Documentation

Update public docs in the website submodule:

- installation page: explain first install and upgrade;
- CLI reference: document `yeet upgrade`, `yeet upgrade check`, flags, and JSON;
- getting started or operations page: show the normal public workflow:
  `yeet upgrade check`, then `yeet upgrade`;
- changelog: user-facing release note with no internal process framing.

README install notes should mention the same upgrade command once it exists.

## Testing

Unit tests:

- semver and channel parsing;
- release asset resolution for yeet and catch;
- cache read/write, corrupt cache recovery, TTL handling, and advisory
  rate-limiting;
- host discovery from prefs, env, explicit host, and `yeet.toml`;
- advisory suppression for JSON, non-TTY, help, version, upgrade, and opt-out;
- upgrade plan rendering;
- checksum verification failure paths.

Integration-style tests with fakes:

- `yeet upgrade check --all` with current, stale, and unreachable hosts;
- local binary install plan for release, nightly, dev, and unknown binaries;
- catch upgrade plan using recorded install metadata;
- partial failure reporting.

Live validation before release:

- install a public yeet release that is intentionally behind;
- confirm passive advisory appears after an interactive command and is
  rate-limited;
- run `yeet upgrade check`;
- run `yeet upgrade` against a test catch host;
- verify `yeet version` and catch `version --json` report the new version.

## Success Criteria

- A user several patch releases behind sees a short, actionable update notice in
  normal interactive use.
- `yeet upgrade check --all` clearly reports local yeet plus all project catch
  hosts.
- `yeet upgrade` updates a release-installed local yeet binary and catch host
  using verified GitHub assets.
- Dev/source builds are not destructively overwritten.
- Normal commands stay fast and do not fail because GitHub is unreachable.
- Public docs explain how to upgrade yeet and catch together.
