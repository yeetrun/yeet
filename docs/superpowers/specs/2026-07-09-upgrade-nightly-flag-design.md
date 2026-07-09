# `yeet upgrade --nightly` Design

## Goal

Let users explicitly upgrade local `yeet` and known catch hosts to the latest
nightly release without reinstalling through the install script first.

The command should be:

```bash
yeet upgrade --nightly
yeet upgrade check --nightly
yeet upgrade --nightly --force
yeet upgrade --host=catch-a --nightly
```

## Current Behavior

`yeet upgrade` chooses the target release channel from the current local binary:

- stable binaries fetch the latest stable GitHub release;
- nightly binaries fetch the moving `nightly` GitHub release;
- `--version` fetches an exact release tag.

This means a stable user cannot ask `yeet upgrade` to switch to nightly. They
must run the install script with `--nightly`.

## Design

Add `--nightly` to `yeet upgrade` as an explicit target-channel override.

When `--nightly` is present:

- upgrade checks fetch the GitHub `nightly` release;
- local `yeet` upgrades resolve the platform-specific `yeet-OS-ARCH.tar.gz` asset from that
  nightly release;
- catch upgrades reuse the existing init-based install path with the same
  behavior as `yeet init --from-github --nightly --no-workspace`;
- plan and check output show the target as `nightly`;
- JSON check output reports the same target release used by the plan.

`--nightly` conflicts with `--version`. `--version` is an exact tag target,
while `--nightly` asks for the latest moving nightly release. Requiring users to
choose one avoids ambiguous plan output and install behavior.

## Upgrade Classification

Nightly is a moving release tag and not semver-comparable. `--nightly` must not
depend on the existing semver comparison used for stable versions.

When the target channel is nightly:

- stable releases are actionable against `nightly`;
- dev builds are actionable when `--force` is present, matching the existing
  reinstall flow for non-release local builds;
- existing nightly builds are actionable when their reported version differs
  from the target nightly tag, because the moving nightly tag is not
  semver-comparable;
- unreachable catch hosts remain unreachable;
- unknown version rows remain unknown unless `--force` makes them actionable
  through the existing force reinstall path.

The important behavior is that `yeet upgrade --nightly` from a stable release
produces an install plan instead of silently treating `nightly` as a
non-semver target.

## CLI and Help

Update the upgrade flag model and help text:

- add `Nightly bool` to upgrade flags;
- include `[--nightly]` in upgrade usage;
- add examples for `yeet upgrade --nightly` and
  `yeet upgrade check --nightly`;
- keep `--version` examples as exact stable tag examples.

## Docs

Update user-facing docs where upgrade/install workflows are described:

- README quickstart should keep the install-script nightly example and mention
  `yeet upgrade --nightly` for switching an existing install to nightly;
- website host setup or upgrade docs should mirror the same guidance;
- generated CLI-help reference should be updated if the help output changes.

Do not add a changelog entry until a release is being prepared.

## Tests

Add focused tests for:

- parsing `--nightly`;
- rejecting `--nightly --version vX.Y.Z`;
- `upgrade check --nightly` fetching the nightly GitHub release from a stable
  local binary;
- `yeet upgrade --nightly` making the stable local binary actionable;
- catch upgrade reinstall passing nightly through to the init path;
- help output including the new flag and examples.

Run targeted CLI and upgrade tests, then the relevant package suites.
