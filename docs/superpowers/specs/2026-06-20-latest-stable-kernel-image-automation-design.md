# Latest Stable Kernel Image Automation Design

Date: 2026-06-20

## Summary

The `yeet-vm-images` repository should publish fresh official yeet VM image
bundles when kernel.org publishes a newer latest stable Linux kernel. The first
phase adds a daily GitHub Actions scheduler that detects the latest stable
kernel, builds and publishes updated Ubuntu and NixOS image bundles, and
refreshes the stable latest aliases used by catch. The second phase will add
guest-visible kernel package sources so users can upgrade kernel-related
artifacts through normal Ubuntu and NixOS mechanisms.

Phase 1 is the implementation target now. Phase 2 is documented here so Phase 1
records enough provenance to support it cleanly, but Phase 2 will get a
separate implementation plan before code changes.

## Current State

The `yeet-vm-images` repository already has manual workflows for:

- `ubuntu-26.04-amd64-*`
- `nixos-26.05-amd64-*`

Both workflows accept:

- `kernel_version`
- `kernel_source_url`
- `kernel_source_sha256`
- `kernel_config_url`

Both workflows build a yeet-managed Firecracker kernel, build the image bundle,
verify the bundle, publish an immutable GitHub release, and optionally refresh
the family latest alias:

- `ubuntu-26.04-amd64-latest`
- `nixos-26.05-amd64-latest`

At design time, the latest aliases point at image revisions on
`linux-7.0-yeet`. The kernel.org latest stable feed currently reports `7.1.1`,
but the automation must not hard-code that version.

The catch client in this repository resolves official images through the
`yeet-vm-images` catalog and latest manifest URLs. It treats VM families as
independent and can update cached VM images with `yeet vm images update`.

## Phase 1 Goals

- Run a daily scheduled GitHub Actions workflow and allow manual dispatch.
- Track kernel.org's official latest stable kernel, not mainline or longterm.
- Publish automatically when the latest stable kernel differs from the current
  latest manifest for a family.
- Preserve independent Ubuntu and NixOS image families.
- Use clear immutable release tags that include both kernel version and image
  revision.
- Keep the stable latest aliases unchanged so existing catch catalog URLs keep
  working.
- Record enough manifest provenance for users, verification, and future package
  repository work.
- Verify the result both in `yeet-vm-images` and through the yeet/catch runtime
  in this repository.

## Phase 1 Non-Goals

- Do not create apt repositories, Nix binary caches, Nix overlays, or guest
  package sources in Phase 1.
- Do not change the `vm://ubuntu/26.04` or `vm://nixos/26.05` payload names.
- Do not publish images for mainline, linux-next, or longterm kernels.
- Do not overwrite existing immutable releases from scheduled runs.
- Do not change the Firecracker kernel config source unless a kernel build
  failure requires a deliberate follow-up.

## Versioning Policy

Phase 1 will move immutable image releases to hybrid tags:

```text
ubuntu-26.04-amd64-kernel-7.1.1-v16
nixos-26.05-amd64-kernel-7.1.1-v15
```

The `kernel-<version>` segment identifies the upstream Linux kernel. The final
`v<N>` remains a per-family image revision. This permits multiple image
revisions for the same kernel when rootfs policy, Firecracker, yeet guest tools,
or verification changes without a kernel bump.

Manifests will keep `version` equal to the immutable release tag and add
explicit fields:

```json
{
  "version": "ubuntu-26.04-amd64-kernel-7.1.1-v16",
  "image_revision": 16,
  "kernel_version": "linux-7.1.1-yeet",
  "upstream_kernel_version": "7.1.1",
  "kernel_source_url": "https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.1.1.tar.xz",
  "kernel_source_sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}
```

`catalog.json` should continue to point at the `*-latest` release manifests.
Catalog validation and catch's catalog matching must accept both the current
`family-v<N>` format and the new `family-kernel-<kernel>-v<N>` format during
the transition.

## Phase 1 Architecture

Add a scheduled orchestration workflow in `yeet-vm-images`:

```text
.github/workflows/sync-latest-stable-kernel.yml
```

The workflow has one detection job and one build/publish job per image family.
The detection job fetches kernel.org release metadata, resolves the latest
stable source tarball and SHA-256, downloads the current latest manifests, and
decides which families need publication. Build jobs run only for outdated
families.

The existing Ubuntu and NixOS build workflows remain responsible for image
construction, validation, release creation, and latest-alias publication. The
scheduled workflow should call them through reusable workflow jobs or dispatch
equivalent inputs after the existing workflows are made reusable. Reusing the
current builders avoids two separate release implementations.

## Phase 1 Components

### `scripts/resolve-latest-kernel.sh`

Fetch kernel.org `releases.json`, select `latest_stable.version`, find the
matching stable release entry, and emit normalized JSON:

```json
{
  "moniker": "stable",
  "version": "7.1.1",
  "source_url": "https://cdn.kernel.org/pub/linux/kernel/v7.x/linux-7.1.1.tar.xz",
  "source_sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
  "released": "2026-06-19"
}
```

The script should derive the checksum from the kernel.org checksum file in the
same source directory. If the latest stable entry or checksum cannot be
resolved, it must fail without producing build inputs.

### `scripts/next-image-version.sh`

Compute the next immutable release tag for a family. Inputs include a family
prefix such as `ubuntu-26.04-amd64`, an upstream kernel version, and the current
GitHub release tags. The script must understand both old and new release
formats:

```text
ubuntu-26.04-amd64-v15
ubuntu-26.04-amd64-kernel-7.1.1-v16
```

It emits the next hybrid version and numeric image revision.

### Build Workflows

Update the existing build workflows so they can be invoked by the scheduler and
continue to support manual dispatch. They should accept and propagate:

- `upstream_kernel_version`
- `kernel_source_url`
- `kernel_source_sha256`

They should preserve the current safety behavior: scheduled builds must not set
`overwrite_release=true`, and immutable release conflicts must fail.

### Build Scripts And Manifests

Update the Ubuntu and NixOS build scripts to record:

- `image_revision`
- `upstream_kernel_version`
- `kernel_source_url`
- `kernel_source_sha256`
- existing `kernel.config` checksum data

These fields should be data-only provenance. They should not change how Ubuntu
or NixOS configures packages inside the rootfs.

## Phase 1 Data Flow

```text
kernel.org releases.json
  -> select latest_stable
  -> resolve source tarball SHA-256
  -> fetch ubuntu latest manifest
  -> fetch nixos latest manifest
  -> compare upstream_kernel_version
  -> compute next per-family hybrid tags
  -> build and publish outdated families
  -> refresh only the matching latest aliases
  -> summarize detected kernel, previous manifests, new tags, and run links
```

If `upstream_kernel_version` is absent in an older latest manifest, the
scheduler should fall back to parsing `kernel_version` values like
`linux-7.0-yeet`. That fallback is transition-only.

## Phase 1 Error Handling

- Network, JSON, checksum, or manifest resolution failures fail the detection
  job and publish nothing.
- If any current latest manifest cannot be fetched, the detection job fails and
  publishes nothing.
- If one family is current and the other is outdated, publish only the outdated
  family.
- If both families are outdated, build them independently after detection.
- If a computed immutable tag already exists, fail rather than overwriting.
- If a family build fails validation, that family publishes nothing.
- If latest alias replacement fails after immutable publication, the workflow
  fails visibly and leaves the immutable release available for inspection.
- The workflow summary must make partial publication obvious.

## Phase 1 Verification

Local repository verification in `yeet-vm-images`:

- Unit-test or shell-test kernel resolution with fixture JSON.
- Unit-test or shell-test next-version computation with old and new tag
  formats.
- Run `scripts/verify-catalog.sh`.
- Validate workflow syntax and reusable workflow wiring.
- Run the existing Nix checks when Nix is available.

Published artifact verification:

- Fetch both latest manifests after publication.
- Verify `version` matches the new hybrid tag format.
- Verify `image_revision`, `upstream_kernel_version`, `kernel_version`,
  `kernel_source_url`, and `kernel_source_sha256`.
- Verify all checksums listed in `checksums.txt` and `manifest.json`.
- Verify the immutable releases and latest aliases both expose the expected
  assets.

Live yeet/catch verification from this repository:

```bash
CATCH_HOST=yeet-lab go run ./cmd/yeet vm images update vm://ubuntu/26.04
CATCH_HOST=yeet-lab go run ./cmd/yeet vm images update vm://nixos/26.05
```

Boot smoke VMs for each family, then check:

- `uname -r` reports the yeet kernel for the detected upstream version.
- guest readiness succeeds.
- `systemctl --failed --no-pager` reports no failed units.
- `systemctl show -p SystemState -p Tainted -p NFailedUnits` is acceptable for
  the family policy.
- default user is correct (`ubuntu` or `nixos`).
- networking basics work.
- `nft`, `iptables`, and `/dev/net/tun` are available.
- NixOS can run its expected Nix tooling and preserve `nixos-rebuild`
  compatibility.

Clean up smoke VMs after verification.

## Phase 2 Goals

Phase 2 will make the yeet-managed kernel available to guests through normal
guest package mechanisms:

- Ubuntu guests should be able to consume a repository with kernel-related deb
  packages through apt.
- NixOS guests should be able to consume kernel package definitions through a
  Nix-native source such as a flake package, overlay, or binary cache.
- Package publication should use the same upstream kernel metadata and kernel
  config as the image bundles.
- Guests should remain distribution-compatible. Ubuntu package ownership rules
  and NixOS rebuild compatibility must remain intact.

## Phase 2 Constraints

- Do not make Ubuntu guests depend on moving files outside Ubuntu package
  ownership.
- Do not patch NixOS package-owned files in the rootfs.
- Do not hide NixOS behavior behind appliance-only scripts. Users should be
  able to inspect and rebuild normal configuration.
- Do not preinstall long-running application services merely to support kernel
  package updates.
- Do not mix package-source publication into the Phase 1 image scheduler unless
  the package source design is approved separately.

## Phase 2 Candidate Design

The likely shape is a second publishing workflow that consumes the same
resolved kernel metadata as Phase 1.

For Ubuntu:

- Build signed or checksummed deb artifacts for the yeet-managed kernel and
  supporting metadata.
- Publish an apt repository, likely through GitHub Pages or release assets plus
  an index.
- Add optional documentation for enabling that apt source inside guests.
- Preserve the image's current policy that the boot kernel is supplied by the
  VM image bundle unless a later approved design changes the boot contract.

For NixOS:

- Publish a flake or package overlay exposing the yeet kernel package and config
  as normal Nix derivations.
- Consider a binary cache if build time or guest rebuild cost is too high.
- Document how a user opts into the kernel package through
  `/etc/nixos/configuration.nix`.
- Keep `/etc/yeet-vm` data-only.

Phase 2 needs a dedicated plan because it changes guest-facing package
contracts and may require signing, repository retention policy, and client
documentation decisions.

## Plan Handoff

Phase 1 implementation plan should cover:

- Script fixtures and tests.
- Manifest schema additions.
- Build workflow reuse.
- Scheduled orchestration workflow.
- README and validation updates.
- GitHub release and live yeet/catch verification.

Phase 2 implementation plan should cover:

- Ubuntu apt repository design and signing/checksum policy.
- Nix flake, overlay, or binary-cache design.
- Guest opt-in instructions.
- Package retention and compatibility policy.
- Verification inside fresh Ubuntu and NixOS guests.
