# Legacy Manifest Jailer Adoption Design

## Problem

VM image releases created before jailer publishing contain a manifest that
binds the rootfs, kernel, and Firecracker, but has no `jailer` field or jailer
checksum. Catch can safely add the exact matching sibling jailer and launch the
VM through it, yet runtime adoption currently rejects the unchanged legacy
manifest. The VM remains runnable but component runtime commands report that it
is not adopted.

## Considered approaches

1. Keep rejecting the old manifest. This preserves the current validator but
   leaves supported pre-jailer VMs permanently outside the runtime lifecycle.
2. Rewrite the old manifest during upgrade. This would change an immutable
   published artifact and make its bytes diverge from the release provenance.
3. Accept a measured legacy sibling jailer without rewriting the manifest.
   This preserves the published manifest and records the locally added jailer
   in Yeet's measured legacy composition. This is the selected approach.

## Compatibility rule

When an installed image manifest declares a jailer, adoption remains strict:
the loaded unit must use that exact manifest-relative path, and the manifest
checksum must match the exact jailer bytes.

When the manifest predates jailer metadata, adoption accepts only
`<manifest-directory>/jailer`. The ordinary adoption inventory must still:

- open and measure the jailer as a trusted regular file;
- verify the loaded Firecracker and jailer report the same version;
- bind both SHA-256 values into the measured legacy composition;
- validate the complete loaded unit, disk, boot artifacts, runtime paths, and
  transaction preconditions.

A jailer outside the manifest directory, a differently named jailer, a
symlink, an untrusted path, or a version mismatch remains blocked.

## Transaction and operator behavior

No manifest or database file is edited by an operator. A Catch upgrade runs the
existing atomic adoption transaction, publishes the runtime descriptor and
descriptor-mode unit, and records component state without restarting the VM.
After adoption, `yeet vm runtime upgrade` works normally.

## Verification

Tests cover both sides of the compatibility boundary:

- a legacy manifest without jailer metadata becomes adoptable when the loaded
  unit uses the measured sibling jailer;
- the same manifest remains blocked when the unit points at a non-sibling
  jailer;
- manifests with jailer metadata retain their existing strict behavior.

The release is also validated with the full repository gate, a Linux test
binary, published artifact checksums and build metadata, and a live Catch
upgrade before retrying runtime status/upgrade.
