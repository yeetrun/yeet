# Remote VM Image Catalog Design

## Goal

Move official VM image family discovery out of the compiled `catch` binary and
into `yeet-vm-images`, while keeping each concrete VM image release immutable.
After this change, publishing a new Ubuntu or NixOS VM image should only require
publishing the immutable image release and refreshing that family's stable
latest manifest. Adding a new family should require changing `yeet-vm-images`
catalog data, not `yeet` code, unless the family needs a new runtime metadata
driver or VM feature that `catch` does not yet understand.

## Current Problem

The runtime image path already fetches a stable per-family manifest URL such as
`ubuntu-26.04-amd64-latest/manifest.json`, validates the manifest, and verifies
downloaded artifacts by checksum. That part is directionally right.

The remaining release footguns are:

- `pkg/catch` still contains compiled official image families.
- `defaultVMImageVersion` makes version bumps look like yeet code changes.
- tests assert specific current image versions instead of dynamic resolution
  behavior.
- image workflow dispatch defaults contain stale immutable version names.
- helper paths such as VM image import, pruning, and catalog rendering depend on
  compiled payload constants and version prefixes.

## Repository Contract

`yeet-vm-images` owns a stable catalog document at a trusted GitHub URL:

```text
https://raw.githubusercontent.com/yeetrun/yeet-vm-images/main/catalog.json
```

This raw GitHub URL is the catalog URL compiled into `catch`. The catalog lists
image families and points each family at its own stable latest manifest URL.

Example schema:

```json
{
  "schema_version": 1,
  "images": [
    {
      "payload": "vm://ubuntu/26.04",
      "name": "Ubuntu 26.04",
      "architecture": "amd64",
      "manifest_url": "https://github.com/yeetrun/yeet-vm-images/releases/download/ubuntu-26.04-amd64-latest/manifest.json",
      "version_prefix": "ubuntu-26.04-amd64-",
      "default_user": "ubuntu",
      "metadata_driver": "ubuntu",
      "capabilities": ["guest_init", "guest_agent", "rsync"],
      "default": true
    },
    {
      "payload": "vm://nixos/26.05",
      "name": "NixOS 26.05",
      "architecture": "amd64",
      "manifest_url": "https://github.com/yeetrun/yeet-vm-images/releases/download/nixos-26.05-amd64-latest/manifest.json",
      "version_prefix": "nixos-26.05-amd64-",
      "default_user": "nixos",
      "metadata_driver": "nixos",
      "capabilities": ["guest_init", "guest_agent", "rsync"]
    }
  ]
}
```

The catalog does not name the latest immutable version. The latest version comes
from the family manifest at `manifest_url`.

## Trust And Validation

`catch` should trust the yeetrun VM image repository as the image authority, but
still validate data before using it:

- catalog URL must be HTTPS and point at `raw.githubusercontent.com/yeetrun/yeet-vm-images`
  or `github.com/yeetrun/yeet-vm-images`;
- every `payload` must be unique and start with `vm://`;
- every `manifest_url` must be HTTPS and point at the same trusted repo;
- every `version_prefix` must be a safe cache-name prefix and must not contain
  path separators;
- every supported `metadata_driver` must be one `catch` knows how to apply;
- every `default_user` must pass the existing VM user validation;
- at most one image should be marked `default`;
- each fetched family manifest must validate with the existing manifest rules;
- the manifest `version` must match the catalog `version_prefix`;
- downloaded image artifacts must keep using manifest checksums.

Security is intentionally pragmatic: this does not add signatures or a separate
root of trust. The GitHub repository URL is the trust boundary.

## Runtime Resolution

Resolution becomes:

```text
payload -> fetch catalog -> find family -> fetch family latest manifest
        -> validate manifest/version prefix -> compare cache -> download or use cache
```

Local imported images remain `vm://<name>` entries under the local image store.
They do not require the remote catalog unless an operation needs a managed
kernel/firecracker source for import.

The main command effects:

- `yeet run <svc> vm://ubuntu/26.04` fetches the catalog, resolves the family,
  fetches the stable latest manifest, and provisions the manifest version.
- `yeet vm images ls` fetches the catalog and reports each remote family plus
  local imported images.
- `yeet vm images catalog` fetches the catalog and renders remote families,
  then appends local imported images.
- `yeet vm images update` with no args updates every remote family from the
  catalog; with one arg it validates that the payload is in the catalog.
- `yeet vm images prune` uses catalog version prefixes to classify managed VM
  image cache directories and ZFS bases.

## Code Shape

Introduce a small catalog layer instead of growing more special cases inside
`vm_image.go`:

- `vm_image_catalog.go`: catalog structs, fetch, validation, lookup by payload,
  lookup by version prefix, trusted URL checks.
- `vm_image_registry.go`: either removed or reduced to the single catalog URL
  constant and compatibility helpers.
- `vm_image.go`: remote resolution accepts a catalog family instead of using
  compiled `officialVMImage` values.
- `vm_images_cmd.go`: catalog/list/update render remote catalog entries instead
  of `officialVMImages`.
- `vm_images_prune.go`: managed-version classification uses catalog prefixes.
- tests: use local HTTP catalog and manifest fixtures instead of asserting the
  currently published version.

Avoid hidden network calls in pure helpers where practical. Command/RPC paths
should load the catalog once per operation and pass the resulting catalog or
family through the lower-level helpers.

## Fallback Behavior

For this pass, dynamic remote catalog access is required for official remote
families. If GitHub/catalog fetch fails, official image operations should fail
with an actionable error naming the catalog URL and operation. This matches the
current behavior where the latest family manifest must be reachable for normal
image resolution.

Changing `--image-policy=cached` into true offline provisioning is out of scope
for this pass. That can be designed separately if offline VM creation becomes a
goal.

## Image Repository Changes

`yeet-vm-images` should add `catalog.json` and a lightweight validation script
that checks:

- valid JSON schema;
- unique payloads;
- trusted manifest URLs;
- current latest manifest URLs are reachable;
- latest manifest versions match the configured version prefixes;
- required capabilities such as `guest_agent` are listed for current official
  images.

The image build workflows should stop defaulting `version` to a specific old
immutable release. The version input should remain required, but without a
stale default. Documentation can show examples instead.

Publishing a new image version should not require touching `catalog.json`
unless the family metadata, manifest URL, capabilities, or default selection
changes.

## Cleanup Targets

Remove or replace:

- `defaultVMImageVersion`;
- compiled official image family arrays as the source of truth;
- tests that fail because the latest image version changed;
- workflow dispatch defaults that point at old immutable versions;
- hardcoded Ubuntu-as-default behavior in local image import, replacing it with
  the catalog image marked `default`.

Keep:

- immutable release names and cache directories;
- stable per-family latest manifest releases;
- manifest checksum verification;
- local imported image support;
- existing image-policy prompt/update/cached behavior unless a specific test
  shows it must change.

## Testing

Go tests should cover:

- catalog fetch and validation success;
- rejected untrusted catalog and manifest URLs;
- duplicate payloads and invalid version prefixes;
- payload resolution from a test catalog;
- latest manifest version matching the family prefix;
- `vm images catalog`, `ls`, `update`, and `prune` using catalog fixtures;
- local imported images still working;
- VM provision selecting manifest metadata without compiled family constants.

Image repo tests should cover:

- `catalog.json` schema validation;
- Ubuntu and NixOS entries point at reachable latest manifests;
- latest manifests include `guest_agent` metadata and artifact checksums;
- workflow files have no stale immutable version default.

Live validation should publish or use a current image release, run
`yeet vm images update vm://ubuntu/26.04` against `yeet-lab`, then provision a
small VM and verify `yeet ssh` obtains its address through the guest agent.

## Rollout

1. Add and validate `catalog.json` in `yeet-vm-images`.
2. Change `yeet` to resolve remote families through the catalog.
3. Remove compiled current-version and family release truth from `pkg/catch`.
4. Update tests and docs for dynamic catalog behavior.
5. Push `yeet-vm-images`, publish any needed image aliases, then land `yeet`.
6. Install updated `catch` on `lab-host` and run the VM SSH/IP smoke test.

Existing cached images and existing VMs stay valid because service records store
the concrete `ImageVersion` selected at creation time.
