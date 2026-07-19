# Firecracker Runtime Publishing and Promotion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Automatically discover stable Firecracker releases and publish verified, immutable Firecracker+jailer runtime artifacts that move through integration, canary, and deliberate stable promotion.

**Architecture:** `yeet-vm-images` ingests official upstream release binaries rather than rebuilding Firecracker. Discovery can publish an unpromoted immutable candidate, but catalog channel changes require durable test attestations and a reviewed pull request; runtime artifacts are never rewritten during promotion.

**Tech Stack:** Bash, jq, curl, git/GPG verification, GitHub Releases API, GitHub Actions, self-hosted Linux KVM runners, GitHub Releases for immutable artifacts and attestations.

## Global Constraints

- Work in the sibling `yeet-vm-images` repository; paths below are relative to that repository.
- Accept only official `firecracker-microvm/firecracker` releases that are neither drafts nor prereleases and whose tag is exactly `vMAJOR.MINOR.PATCH`.
- One runtime artifact contains exactly one upstream Firecracker binary and its matching jailer from the same release archive.
- Record the upstream release tag, resolved commit, archive URL/digest, component digests, version-probe output, architecture, packaging revision, support classification, and ingest provenance.
- Treat downloaded archives, checksum files, API JSON, and extracted names as untrusted input.
- Reject absolute paths, `..` paths, symlinks, hardlinks, devices, unexpected members, unexpected architectures, and non-regular runtime binaries.
- Use upstream release binaries with default restrictive seccomp behavior; do not publish debug or locally rebuilt binaries as official runtimes.
- Candidate publication must not update the stable channel.
- Integration and canary evidence lives in separate immutable attestation releases; it is not appended to the runtime manifest.
- Promotion and revocation change catalog state through reviewed commits, never by overwriting an immutable runtime release.
- A security emergency may shorten canary duration only through an explicit reviewed override; artifact verification and KVM integration remain mandatory.
- Tasks 1-5 do not depend on Catch runtime-management commands. Their KVM harness injects exact candidate paths into the existing jailer-only launch path. Tasks 6-7 run only after Catch runtime-management Tasks 1-10 are complete.

---

### Task 1: Define runtime, catalog, and attestation contracts

**Files:**
- Create: `schemas/firecracker-runtime-manifest.schema.json`
- Create: `schemas/firecracker-runtime-catalog.schema.json`
- Create: `schemas/firecracker-runtime-attestation.schema.json`
- Create: `runtime-catalog.json`
- Create: `scripts/verify-runtime-catalog.sh`
- Create: `scripts/testdata/runtime-manifest-v1.16.1.json`
- Create: `scripts/testdata/runtime-catalog-empty.json`
- Create: `scripts/testdata/runtime-attestation-integration.json`
- Create: `scripts/test-firecracker-runtime-contracts.sh`
- Modify: `scripts/verify-catalog.sh`

**Interfaces:**
- Produces: manifest schema version 1, runtime catalog schema version 1, attestation schema version 1.
- Consumed later by: discovery/build workflows, Catch catalog/cache parsing, promotion workflows.

- [ ] **Step 1: Write failing contract tests**

The test script must validate the good fixtures and create mutations that reject a mismatched pair, invalid SHA-256, unsupported architecture, HTTP URL, duplicate revocation, missing attestation digest, and stable/candidate aliasing with different manifest digests:

```bash
#!/usr/bin/env bash
set -euo pipefail

scripts/verify-runtime-catalog.sh scripts/testdata/runtime-catalog-empty.json
jq -e '.schema_version == 1 and .runtime_id == "firecracker-v1.16.1-yeet-v1"' \
	scripts/testdata/runtime-manifest-v1.16.1.json >/dev/null
jq -e '.schema_version == 1 and .kind == "integration" and (.subject.manifest_sha256 | test("^[0-9a-f]{64}$"))' \
	scripts/testdata/runtime-attestation-integration.json >/dev/null
```

Use `mktemp -d` and `trap` for negative fixtures; each mutation must fail `verify-runtime-catalog.sh` or the matching jq schema validator.

- [ ] **Step 2: Run the contract test and verify failure**

Run:

```bash
scripts/test-firecracker-runtime-contracts.sh
```

Expected: FAIL because the schemas, catalog, and verifier do not exist.

- [ ] **Step 3: Add the exact initial catalog shape**

Seed `runtime-catalog.json` without claiming an untested stable runtime:

```json
{
  "schema_version": 1,
  "architectures": {
    "amd64": {
      "runtimes": [],
      "channels": {
        "stable": null,
        "candidate": null
      }
    }
  },
  "revocations": []
}
```

Define each immutable `architectures.<arch>.runtimes[]` entry with these required fields:

```json
{
  "runtime_id": "firecracker-v1.16.1-yeet-v1",
  "manifest_url": "https://github.com/yeetrun/yeet-vm-images/releases/download/firecracker-v1.16.1-yeet-v1/runtime-manifest.json",
  "manifest_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "upstream_version": "v1.16.1",
  "support": "supported",
  "integration_attestation_url": "https://github.com/yeetrun/yeet-vm-images/releases/download/firecracker-v1.16.1-yeet-v1-integration-123456789/runtime-attestation.json",
  "integration_attestation_sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "canary_attestation_url": "https://github.com/yeetrun/yeet-vm-images/releases/download/firecracker-v1.16.1-yeet-v1-canary-123456790/runtime-attestation.json",
  "canary_attestation_sha256": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
}
```

Define each non-null channel as a digest-qualified pointer into that same architecture's `runtimes` array:

```json
{
  "runtime_id": "firecracker-v1.16.1-yeet-v1",
  "manifest_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
}
```

The candidate pointer requires a matching runtime entry with integration evidence and permits null canary fields. The stable pointer requires a matching entry with both integration and canary evidence. Exact-version selection searches the immutable runtime entries, never the channels alone.

- [ ] **Step 4: Implement strict validation**

`scripts/verify-runtime-catalog.sh [path]` must use `jq -e` to enforce:

```jq
.schema_version == 1 and
(.architectures | keys == ["amd64"]) and
all(.architectures[]; (.runtimes | type == "array") and (.channels | type == "object")) and
(.revocations | type == "array") and
all(.revocations[]; (
  (.runtime_id | test("^firecracker-v[0-9]+[.][0-9]+[.][0-9]+-yeet-v[1-9][0-9]*$")) and
  (.manifest_sha256 | test("^[0-9a-f]{64}$")) and
  (.reason | type == "string" and length > 0) and
  (.recorded_at | fromdateiso8601)
))
```

Validate only `https://github.com/yeetrun/yeet-vm-images/releases/download/` manifest and attestation URLs. Require lower-case SHA-256 values, exact `vMAJOR.MINOR.PATCH`, known support values `supported|deprecated|eol|revoked`, no duplicate `(runtime_id, manifest_sha256)` entries, and no duplicate runtime IDs in revocations. Every non-null channel must resolve to exactly one same-architecture runtime entry with the same manifest digest; every revoked entry must be absent from both channels and report support `revoked`.

- [ ] **Step 5: Run tests and commit**

Run:

```bash
scripts/test-firecracker-runtime-contracts.sh
scripts/verify-catalog.sh
```

Expected: PASS.

Commit:

```bash
git add schemas runtime-catalog.json scripts
git commit -m "runtime: define Firecracker artifact contracts"
```

### Task 2: Implement stable release discovery and packaging revision resolution

**Files:**
- Create: `scripts/resolve-latest-firecracker.sh`
- Create: `scripts/resolve-firecracker-runtime-release.sh`
- Create: `scripts/testdata/firecracker-releases-v1.16.1.json`
- Create: `scripts/testdata/firecracker-releases-page-1.json`
- Create: `scripts/testdata/firecracker-releases-page-2.json`
- Create: `scripts/testdata/firecracker-release-tags.txt`
- Create: `scripts/test-firecracker-release-discovery.sh`

**Interfaces:**
- Produces: one JSON discovery record and one packaging-release record.
- Consumes: GitHub Releases API JSON or a test fixture path.

- [ ] **Step 1: Write fixture-driven discovery tests**

Cover unordered releases, drafts, prereleases, malformed tags, duplicate versions, missing x86_64 assets, and the packaging revision increment:

```bash
result="$(scripts/resolve-latest-firecracker.sh scripts/testdata/firecracker-releases-v1.16.1.json)"
jq -e '
  .upstream_version == "v1.16.1" and
  .architecture == "amd64" and
  .archive_name == "firecracker-v1.16.1-x86_64.tgz" and
  (.archive_url | startswith("https://github.com/firecracker-microvm/firecracker/releases/download/v1.16.1/"))
' <<<"$result" >/dev/null

release="$(scripts/resolve-firecracker-runtime-release.sh v1.16.1 scripts/testdata/firecracker-release-tags.txt)"
jq -e '.next_release == "firecracker-v1.16.1-yeet-v2" and .next_revision == 2' <<<"$release" >/dev/null
```

- [ ] **Step 2: Run and verify failure**

Run:

```bash
scripts/test-firecracker-release-discovery.sh
```

Expected: FAIL because both resolvers are absent.

- [ ] **Step 3: Implement deterministic discovery**

`resolve-latest-firecracker.sh [releases-json]` must fetch this endpoint when no fixture is supplied:

```text
https://api.github.com/repos/firecracker-microvm/firecracker/releases?per_page=100
```

Follow GitHub's `Link: rel="next"` pagination until exhausted (with a 20-page safety cap), concatenate all pages, and fail rather than silently choose from a truncated result. Fixture tests include a two-page response whose newest stable release appears on page two.

Filter with this semantic rule:

```jq
map(select(
  (.draft == false) and
  (.prerelease == false) and
  (.tag_name | test("^v[0-9]+[.][0-9]+[.][0-9]+$"))
))
```

Sort numerically by major/minor/patch, select the highest, require exactly one `firecracker-TAG-x86_64.tgz` asset and its checksum sidecar, and emit the asset API digest when GitHub supplies one.

- [ ] **Step 4: Implement immutable packaging revision resolution**

`resolve-firecracker-runtime-release.sh <upstream-version> [tags-file]` must accept only `vMAJOR.MINOR.PATCH`, scan tags matching `firecracker-<upstream>-yeet-vN`, and emit:

```json
{
  "upstream_version": "v1.16.1",
  "current_revision": 1,
  "current_release": "firecracker-v1.16.1-yeet-v1",
  "next_revision": 2,
  "next_release": "firecracker-v1.16.1-yeet-v2"
}
```

Never choose an existing tag for overwrite; a changed ingest always uses `next_release`.

- [ ] **Step 5: Run tests and commit**

```bash
scripts/test-firecracker-release-discovery.sh
git add scripts
git commit -m "runtime: discover stable Firecracker releases"
```

### Task 3: Build and verify immutable runtime artifacts

**Files:**
- Create: `security/firecracker-trusted-signers.txt`
- Create: `scripts/download-firecracker-release.sh`
- Create: `scripts/build-firecracker-runtime.sh`
- Create: `scripts/publish-firecracker-runtime-assets.sh`
- Create: `scripts/test-firecracker-runtime-build.sh`
- Create: `scripts/testdata/firecracker-release-v1.16.1.json`
- Create: `scripts/testdata/firecracker-v1.16.1-x86_64.tgz`
- Create: `scripts/testdata/firecracker-v1.16.1-x86_64.tgz.sha256.txt`

**Interfaces:**
- Produces: `firecracker`, `jailer`, `runtime-manifest.json`, and `runtime-checksums.txt` in a caller-supplied output directory.
- Consumed by: GitHub publishing workflow and Catch runtime cache.

- [ ] **Step 1: Write archive-adversary tests**

Generate test archives containing an absolute member, parent traversal, symlink, hardlink, extra executable, mismatched jailer version, wrong ELF architecture, wrong sidecar digest, and wrong GitHub API digest. Require each case to fail before the output directory is published.

The successful fixture assertion is:

```bash
scripts/build-firecracker-runtime.sh \
	--release-json scripts/testdata/firecracker-release-v1.16.1.json \
	--archive scripts/testdata/firecracker-v1.16.1-x86_64.tgz \
	--checksum scripts/testdata/firecracker-v1.16.1-x86_64.tgz.sha256.txt \
	--runtime-id firecracker-v1.16.1-yeet-v1 \
	--out "$tmp_dir/out"
jq -e '
  .schema_version == 1 and
  .runtime_id == "firecracker-v1.16.1-yeet-v1" and
  .upstream.version == "v1.16.1" and
  .architecture == "amd64" and
  .components.firecracker.path == "firecracker" and
  .components.jailer.path == "jailer" and
  (.components.firecracker.sha256 | test("^[0-9a-f]{64}$")) and
  (.components.jailer.sha256 | test("^[0-9a-f]{64}$"))
' "$tmp_dir/out/runtime-manifest.json" >/dev/null
```

- [ ] **Step 2: Run and verify failure**

```bash
scripts/test-firecracker-runtime-build.sh
```

Expected: FAIL because the downloader/builder do not exist.

- [ ] **Step 3: Implement download, source verification, and safe extraction**

`download-firecracker-release.sh` must:

1. require the official repository and exact release/tag;
2. compare the downloaded archive against the sidecar and GitHub asset digest;
3. fetch the exact tag into a temporary Git repository and record its commit;
4. run `git verify-tag` when signed and match the fingerprint against `security/firecracker-trusted-signers.txt`;
5. emit `signed`, `unsigned-approved`, or `signer-rotation-approved` only when workflow inputs explicitly authorize the latter two states;
6. inspect every tar member before extraction and allow only regular files beneath `release-TAG-x86_64/` needed for `SHA256SUMS`, Firecracker, and jailer;
7. run the archive's `SHA256SUMS` check after extraction.

Use a private `mktemp -d`, `umask 077`, `tar --no-same-owner --no-same-permissions`, and cleanup traps.

- [ ] **Step 4: Implement manifest creation and final verification**

The manifest must have this stable field layout:

```json
{
  "schema_version": 1,
  "runtime_id": "firecracker-v1.16.1-yeet-v1",
  "architecture": "amd64",
  "upstream": {
    "repository": "firecracker-microvm/firecracker",
    "version": "v1.16.1",
    "tag": "v1.16.1",
    "commit": "0123456789abcdef0123456789abcdef01234567",
    "archive_url": "https://github.com/firecracker-microvm/firecracker/releases/download/v1.16.1/firecracker-v1.16.1-x86_64.tgz",
    "archive_sha256": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
    "checksum_url": "https://github.com/firecracker-microvm/firecracker/releases/download/v1.16.1/firecracker-v1.16.1-x86_64.tgz.sha256.txt",
    "tag_signature": {"status": "signed", "fingerprint": "0123456789ABCDEF0123456789ABCDEF01234567"}
  },
  "components": {
    "firecracker": {"path": "firecracker", "sha256": "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "version_output": "Firecracker v1.16.1"},
    "jailer": {"path": "jailer", "sha256": "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "version_output": "Jailer v1.16.1"}
  },
  "classification": {"production_release": true, "default_seccomp": true},
  "support": {"state": "supported", "policy_url": "https://github.com/firecracker-microvm/firecracker/blob/main/docs/RELEASE_POLICY.md"},
  "provenance": {"repository": "yeetrun/yeet-vm-images", "commit": "89abcdef0123456789abcdef0123456789abcdef", "workflow_run": "123456789"}
}
```

Probe both binaries with `--version`, parse exact versions, and require equality with the selected upstream tag. Verify `file` reports x86-64 ELF executables. Write final files to a staging directory and rename it into the requested output only after all checks pass.

- [ ] **Step 5: Publish without overwrite and commit**

`publish-firecracker-runtime-assets.sh` must create a draft release, upload exactly the four assets, verify their sizes/digests through the GitHub API, and publish the draft. If the tag or release exists, exit nonzero and instruct the caller to use the next packaging revision; never accept `--clobber` or delete an existing runtime tag.

The publisher must fail closed unless it is running inside the repository-owned
publishing job added by Task 4. Identify the called reusable workflow with the
documented `job.workflow_repository`, `job.workflow_file_path`,
`job.workflow_ref`, and `job.workflow_sha` context values plus the native
`GITHUB_JOB`; do not use `GITHUB_WORKFLOW_REF` as called-workflow identity because
it represents the caller in a reusable-workflow invocation. Require
`job.workflow_ref` in its documented full
`owner/repository/.github/workflows/file.yml@ref` form. The only approved caller
is the same-repository scheduled/manual discovery workflow, so also require its
native full `GITHUB_WORKFLOW_REF`. That caller must use the local
`./.github/workflows/build-firecracker-runtime.yml` syntax, which GitHub resolves
from the same commit as the caller; consequently `job.workflow_sha`,
`GITHUB_SHA`, and the release target must be identical. That job is part of
the serialization boundary: all tag and release writes use one fixed concurrency
group with `cancel-in-progress: false` and one protected GitHub Environment. The
environment, concurrency settings, and effective token permissions are reviewed
workflow configuration, not facts exposed by documented runtime variables. Do
not accept caller-provided marker variables as proof of those settings, and do
not depend on undocumented conditional `PATCH` behavior. Immediately before
publication, require the exact expected asset set and tag target; after
publication, verify the same set, target, and immutable-release state again.
The immutable-release settings preflight requires repository
`Administration: read`, which the Actions `GITHUB_TOKEN` cannot express. The
publishing job must therefore use a repository-scoped GitHub App installation
token issued inside the protected environment with only `Administration: read`
and `Contents: write`; there is no personal-token or `GITHUB_TOKEN` fallback.

Run:

```bash
scripts/test-firecracker-runtime-build.sh
scripts/test-firecracker-runtime-contracts.sh
git add security scripts
git commit -m "runtime: build verified Firecracker pairs"
```

### Task 4: Automate discovery and candidate publication

**Files:**
- Create: `.github/workflows/build-firecracker-runtime.yml`
- Create: `.github/workflows/sync-latest-stable-firecracker.yml`
- Create: `scripts/test-firecracker-runtime-workflows.sh`
- Create: `scripts/verify-published-firecracker-runtime.sh`
- Create: `scripts/test-published-firecracker-runtime.sh`
- Modify: `README.md`

**Interfaces:**
- Produces: an immutable, unpromoted runtime GitHub release, its native
  `release: published` event, and a workflow summary.
- Consumes: Tasks 1-3 scripts and schemas.

- [ ] **Step 1: Add workflow structure tests**

Require the reusable build workflow to expose required `upstream_version` and
`runtime_id` inputs plus `allow_unsigned_tag` and `allow_signer_rotation`; the
serialized job must always re-resolve and compare the required runtime ID.
Require the scheduled workflow and default Actions token to remain
`contents: read`; only the protected publishing job may mint a
repository-scoped GitHub App installation token with `Administration: read` and
`Contents: write`. Require every runtime tag/release write path to use the fixed
`firecracker-runtime-publish` concurrency group with
`cancel-in-progress: false`, enter the protected
`firecracker-runtime-publish` GitHub Environment, and invoke the same publishing
job. The scheduled/manual discovery workflow must be the only caller and must
use exactly `./.github/workflows/build-firecracker-runtime.yml`, so caller and
called workflow resolve from the same commit. The workflow tests must inspect
every `uses` reference containing `build-firecracker-runtime.yml`, not only the
approved literal, and reject all but that one local call. They must also inspect
the YAML declarations, full-SHA-pinned GitHub App token action, its
repository/permission restrictions, and the publication step's token wiring
directly; custom environment variables cannot prove them at runtime. Reject
`overwrite`, `--clobber`, direct catalog edits, mutable release aliases,
personal-token/default-token publication fallback, alternate runtime-workflow
callers, and any publisher invocation that lacks the documented caller,
called-workflow, and job identity. Also reject a second `repository_dispatch`
or `workflow_dispatch` API write after publication: the App-token release
publication itself emits the native `release: published` event consumed by
Task 5.

Add fixture-backed tests for `verify-published-firecracker-runtime.sh`. A valid
no-op must query the exact release by ID/tag, require `draft=false`,
`prerelease=false`, `immutable=true`, a publication timestamp, the exact four
uploaded assets with unique names/sizes/SHA-256 digests, a schema-valid and
cross-field-valid bundle, and a tag that resolves to the manifest provenance
commit. Missing/mutable/prerelease releases, extra/missing/duplicate assets,
wrong size/digest, malformed manifest, and wrong tag target must fail.

- [ ] **Step 2: Run and verify failure**

```bash
scripts/test-firecracker-runtime-workflows.sh
```

Expected: FAIL while both workflows are absent.

- [ ] **Step 3: Add the reusable build workflow**

`.github/workflows/build-firecracker-runtime.yml` must:

- resolve a fresh packaging revision;
- verify the release API record and tag commit;
- serialize the complete tag/release transaction with concurrency group
  `firecracker-runtime-publish` and `cancel-in-progress: false`;
- keep the default `GITHUB_TOKEN` at `contents: read` and bind the publishing job
  to the protected `firecracker-runtime-publish` GitHub Environment;
- inside that job, use a full-SHA-pinned GitHub-owned App-token action and its
  recommended environment-protected App client ID plus private-key secret to
  mint a token restricted to
  this repository with `Administration: read` and `Contents: write`; expose it
  as `GH_TOKEN` only to the publication step, never log it, and provide no
  personal-token or default-token fallback;
- pass `job.workflow_repository`, `job.workflow_file_path`, `job.workflow_ref`,
  and `job.workflow_sha` expressions into uniquely named publisher environment
  variables; require the publisher to match them to this repository, the called
  reusable workflow path, the full
  `yeetrun/yeet-vm-images/.github/workflows/build-firecracker-runtime.yml@refs/heads/main`
  ref, and the exact target commit; also require the native publishing
  `GITHUB_JOB` ID and the native caller `GITHUB_WORKFLOW_REF` for
  `sync-latest-stable-firecracker.yml@refs/heads/main`;
- require an approved GitHub Environment when unsigned/signer-rotation overrides are true;
- build and verify the four assets;
- verify the exact tag target and asset set immediately before and after
  publishing an immutable runtime release;
- output `runtime_id`, manifest URL, manifest SHA-256, and release URL;
- rely on the native `release: published` event from the App-token publication
  to start Task 5 integration; do not make a second post-publication dispatch
  API call, and never edit `runtime-catalog.json`.

The repository workflow is the exclusive automation writer for runtime tags and
releases. Repository administrators remain inside the trusted repository
boundary; the workflow does not claim to serialize independent administrator
actions. Document that boundary explicitly. Do not model serialization with
undocumented `If-Match` support on release updates. Do not claim runtime proof of
the protected environment, concurrency group, cancellation behavior, or
effective `contents` permission: GitHub exposes those as workflow configuration,
so the structure tests must verify the declarations statically.

Provisioning the repository GitHub App, installing it on `yeet-vm-images`,
configuring the protected environment, and storing its App client ID/private key are
operator prerequisites for live publication. Until they exist, the workflow must
build and verify locally but fail closed before creating a tag or release.
Do not provision those credentials or enable scheduled publication until the
Task 5 integration workflow is present on `main` with both the native release
trigger and its manual recovery entrypoint.

Pin third-party actions to full commit SHAs before landing.

- [ ] **Step 4: Add scheduled discovery**

`.github/workflows/sync-latest-stable-firecracker.yml` runs daily and on manual dispatch. It compares the newest official stable version with all existing `firecracker-v*-yeet-v*` releases. If the upstream version is absent, it calls the reusable build workflow. If present, it reports a verified no-op.

Use all immutable Git tag refs—not only non-draft releases—to allocate the next
packaging revision, so a preserved tag/draft consumes its revision and recovery
advances. Separately identify published candidates for the selected upstream
version. If none exists, pass the next free runtime ID to the serialized job; if
one exists, run `verify-published-firecracker-runtime.sh` before reporting a
no-op. A matching but invalid release is an error, not a no-op. Tests must cover
tag-only and draft partial states. Every discovery, publication, or output
failure must append an actionable workflow summary before exiting nonzero.

The call must use exactly
`./.github/workflows/build-firecracker-runtime.yml`, with no branch, tag, or
external repository reference, so GitHub resolves both workflows from the same
commit. No other workflow may call the runtime publisher.

Concurrency must be:

```yaml
concurrency:
  group: sync-latest-stable-firecracker
  cancel-in-progress: false
```

- [ ] **Step 5: Test, document, and commit**

```bash
scripts/test-firecracker-runtime-workflows.sh
scripts/test-published-firecracker-runtime.sh
scripts/test-firecracker-release-discovery.sh
scripts/test-firecracker-runtime-build.sh
git add .github README.md scripts
git commit -m "runtime: automate Firecracker candidate publication"
```

### Task 5: Add KVM integration and immutable attestations

The repository-local artifact, workflow, evidence, and promotion machinery may
land before Catch runtime management, but live integration must remain disabled
until Catch Tasks 1-10 and its Task 11 repository-owned integration driver are
complete. The image repository prepares and verifies exact artifacts; the
driver from the exact `tested_yeet.commit` exercises the real Catch lifecycle.
Do not substitute a second launcher or an unversioned helper installed on the
self-hosted runner.

**Files:**
- Create: `runtime-integration.json`
- Create: `.github/workflows/test-firecracker-runtime-kvm.yml`
- Create: `scripts/download-published-firecracker-runtime.sh`
- Create: `scripts/test-download-published-firecracker-runtime.sh`
- Create: `scripts/download-published-kernel-release.sh`
- Create: `scripts/test-download-published-kernel-release.sh`
- Create: `scripts/download-vm-image-release.sh`
- Create: `scripts/test-download-vm-image-release.sh`
- Create: `scripts/test-firecracker-runtime-kvm.sh`
- Create: `scripts/write-firecracker-runtime-attestation.sh`
- Create: `scripts/publish-firecracker-runtime-attestation.sh`
- Create: `scripts/test-firecracker-runtime-attestations.sh`
- Create: `.github/workflows/promote-firecracker-runtime.yml`
- Create: `scripts/promote-firecracker-runtime.sh`
- Create: `scripts/test-firecracker-runtime-promotion.sh`
- Modify: `scripts/verify-published-firecracker-runtime.sh`
- Modify: `scripts/test-firecracker-runtime-workflows.sh`
- Modify: `README.md`

**Interfaces:**
- Produces: immutable `runtime-attestation.json` and `runtime-attestation.sha256` assets in a release tagged `<runtime-id>-integration-<run-id>`, plus a reviewed candidate-channel catalog PR.
- Consumes: exact runtime manifest digest, current Ubuntu/NixOS guest artifacts, current/previous promoted kernels, and a Yeet source ref.

- [x] **Step 1: Write attestation and workflow tests**

Require the integration workflow to consume `release: published` and expose a
manual recovery dispatch for an exact runtime ID and manifest digest. It must
ignore non-runtime tags and re-download and verify the immutable release before
using event data.

Add a closed, reviewed `runtime-integration.json` activation record. It starts
with `release_event.enabled=false` and null guest/kernel/Yeet selections. Native
release events report a dormant no-op while it is disabled; manual recovery
still requires every exact input. Enabling native integration is a separate
reviewed commit that atomically supplies all four immutable artifact IDs and
the full Yeet driver commit. Tests must reject partial, mutable, or unknown
activation fields.

Require the integration attestation to contain:

```json
{
  "schema_version": 1,
  "kind": "integration",
  "subject": {"runtime_id": "firecracker-v1.16.1-yeet-v1", "manifest_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
  "runner": {"class": "self-hosted-linux-kvm", "architecture": "amd64"},
  "source": {"repository": "yeetrun/yeet-vm-images", "commit": "89abcdef0123456789abcdef0123456789abcdef", "workflow_run": "123456789"},
  "tested_yeet": {"repository": "yeetrun/yeet", "commit": "fedcba9876543210fedcba9876543210fedcba98"},
  "artifacts": {"ubuntu_guest_release": "ubuntu-26.04-amd64-kernel-7.1.1-v29", "nixos_guest_release": "nixos-26.05-amd64-kernel-7.1.1-v29", "current_kernel_release": "kernel-linux-7.1.1-yeet-v1", "previous_kernel_release": "kernel-linux-7.1.0-yeet-v1"},
  "matrix": {"ubuntu": "passed", "nixos": "passed", "current_kernel": "passed", "previous_kernel": "passed", "raw": "passed", "zfs": "passed", "custom_roots": "passed", "jailer_drop": "passed"},
  "started_at": "2026-07-19T14:00:00Z",
  "completed_at": "2026-07-19T14:37:00Z",
  "result": "passed"
}
```

`source.commit` is the exact `yeet-vm-images` workflow/harness commit. The
closed `tested_yeet` object records the exact Yeet commit exercised by that
harness; neither value may be inferred from the release event's `GITHUB_SHA`.
The closed `artifacts` object records the four exact immutable guest and kernel
release IDs verified by the harness; mutable aliases are forbidden.
Tests must reject a missing matrix cell, non-passed result, subject digest
mismatch, tested-Yeet mismatch, missing or mutable artifact ID, or an
attestation tag that does not include the runtime ID and run ID. Add promotion
tests proving `unlisted -> candidate` requires matching passed integration
evidence, appends one immutable runtime entry, points `candidate` at the same
ID/digest, leaves `stable` unchanged, and cannot overwrite an existing
conflicting entry.

- [x] **Step 2: Run and verify failure**

```bash
scripts/test-firecracker-runtime-attestations.sh
scripts/test-firecracker-runtime-workflows.sh
scripts/test-firecracker-runtime-promotion.sh
```

Expected: FAIL because the integration workflow and attestation scripts do not exist.

- [x] **Step 3: Implement the KVM test harness**

The harness interface is:

```text
scripts/test-firecracker-runtime-kvm.sh \
  --runtime-release RUNTIME_ID \
  --runtime-manifest-sha256 SHA256 \
  --ubuntu-guest-release RELEASE \
  --nixos-guest-release RELEASE \
  --current-kernel-release RELEASE \
  --previous-kernel-release RELEASE \
  --yeet-ref COMMIT \
  --work-dir DIRECTORY \
  --matrix-out FILE
```

It must require Linux x86-64, `/dev/kvm`, root or passwordless sudo for jailer setup, ZFS for the ZFS cases, and a dedicated non-root test identity. For each matrix row it verifies exact runtime digests, matching version probes, jailer launch, drop to the configured UID/GID, API socket readiness, guest boot, guest natural reboot, network readiness, disk snapshot/restore, raw-disk boot, custom data root, custom service root, and cleanup. It must never fall back to launching Firecracker directly.

After artifact verification and host preflight, check out the exact full
`--yeet-ref`, verify the checkout's `HEAD`, and invoke that checkout's
repository-owned `scripts/test-firecracker-runtime-integration.sh`. The driver
is delivered by the Catch runtime-management plan after its production command
surface exists. It must use the real Catch provisioning, unit, readiness,
runtime trial/fallback, disk-only recovery, and cleanup paths. A missing driver
is an actionable failure; there is no external `/usr/local` helper fallback.

All four guest/kernel arguments are exact immutable release IDs, never mutable
`latest` aliases. Use the hardened published-kernel downloader for the current
and previous kernels, and verify each release record is itself immutable before
accepting its exact manifest, asset set, URLs, sizes, digests, and checksums.
Verify the same exact release properties for Ubuntu and NixOS before boot. The matrix keys are evidence
dimensions rather than an implicit Cartesian product: exercise Ubuntu and
NixOS with the current kernel, a representative previous-kernel compatibility
case, raw and ZFS storage, custom data and service roots, and the jailer UID/GID
drop on every launch. The shared lifecycle assertions cover readiness, natural
reboot, networking, disk-only snapshot/restore, and cleanup in each applicable
scenario.

- [x] **Step 4: Publish durable integration evidence**

Run the workflow on labels:

```yaml
runs-on: [self-hosted, linux, x64, kvm, yeet-runtime-integration]
```

On success, generate and hash the attestation, create a new immutable GitHub release tag `<runtime-id>-integration-<run-id>`, upload both assets, and expose their URL/digest as outputs. Existing attestation tags are errors.

The evidence publisher runs in the protected
`firecracker-runtime-integration-publish` environment under one fixed
non-cancelling concurrency group. The default token remains read-only; a
repository-scoped GitHub App token is exposed only to the publication step.
Bind the publisher to the exact repository workflow/job identity and target the
checked-out `yeet-vm-images` harness commit. Publish exactly
`runtime-attestation.json` and `runtime-attestation.sha256`; the checksum file
contains `<sha256>  runtime-attestation.json`. Use the same
draft/upload/reverify/publish/final-immutability transaction as runtime
publication. Preserve any partial tag/draft on failure. Because a rerun of the
same Actions run would reuse its run ID, recovery starts a new manual workflow
run and therefore a new evidence tag.

- [x] **Step 5: Add reviewed candidate promotion**

`promote-firecracker-runtime.sh --channel candidate` re-downloads and verifies the manifest and integration attestation, appends the immutable runtime entry, and moves only the candidate pointer. The workflow creates `promote/<runtime-id>/candidate`, commits only `runtime-catalog.json`, and opens a pull request; it never pushes directly to `main` or auto-merges.

The manual promotion inputs are the exact runtime ID, manifest digest,
integration-attestation URL, and integration-attestation digest. Run the
workflow in the protected `firecracker-runtime-promotion` environment with a
non-cancelling per-runtime concurrency group. Keep the default token read-only;
scope the publication token to this repository with only Contents write and
Pull requests write. Require the exact reviewed main workflow/job identity,
check out its full workflow SHA, freshly fetch `origin/main`, and fail if main
has advanced rather than executing different scripts. Write and verify the
catalog atomically, require the diff to contain only
`runtime-catalog.json`, push the new branch without force, and open the PR
against `main`. An already-promoted identical entry/pointer is a verified
no-op; conflicting entries and existing branch/PR collisions fail with
actionable recovery rather than overwriting state.

Run static tests and commit:

```bash
scripts/test-firecracker-runtime-attestations.sh
scripts/test-firecracker-runtime-workflows.sh
scripts/test-firecracker-runtime-promotion.sh
git add .github runtime-catalog.json scripts
git commit -m "runtime: integrate and list Firecracker candidates"
```

- [ ] **Step 6: Bootstrap the exact candidate used by Catch**

Dispatch `sync-latest-stable-firecracker.yml` for the approved upstream version;
the build workflow remains `workflow_call`-only and this sync workflow remains
its sole caller. Do not enable its credentials or schedule until the exact Yeet
commit containing the repository-owned integration driver is ready. In the
same reviewed activation change, set `runtime-integration.json` to enabled with
all exact artifact IDs and that driver commit. Then run KVM integration against
the published candidate's exact manifest digest. After
immutable passed evidence exists, run candidate promotion and merge only the
reviewed catalog PR. Record the candidate runtime ID, manifest digest, release
URL, integration attestation URL/digest, tested Yeet commit, and catalog commit.
Stable must remain unchanged.

### Task 6: Add canary, promotion, and revocation workflows

**Files:**
- Create: `.github/workflows/canary-firecracker-runtime.yml`
- Create: `.github/workflows/revoke-firecracker-runtime.yml`
- Modify: `.github/workflows/promote-firecracker-runtime.yml`
- Modify: `scripts/promote-firecracker-runtime.sh`
- Create: `scripts/revoke-firecracker-runtime.sh`
- Modify: `scripts/test-firecracker-runtime-promotion.sh`
- Modify: `schemas/firecracker-runtime-attestation.schema.json`
- Create: `scripts/testdata/runtime-attestation-canary.json`
- Modify: `scripts/verify-runtime-catalog.sh`
- Modify: `README.md`

**Interfaces:**
- Produces: reviewed catalog pull requests for candidate/stable promotion or revocation.
- Consumes: immutable runtime manifest plus integration/canary attestation URL and digest.
- Depends on: Catch Firecracker Runtime Management Tasks 1-10 for full upgrade/trial/fallback/rollback canary coverage.

- [ ] **Step 1: Write promotion state-machine tests**

Before writing or publishing a canary attestation, extend attestation schema
version 1 with a closed `kind: canary` branch and add an exact canary fixture.
The branch must retain the common immutable subject, runner, source, timestamps,
and passed result contract; close every canary-specific object; record the full
matrix and soak counters required below; and conditionally require the emergency
approver and reason when `emergency_override` is true. Tests must validate the
fixture through the Draft 2020-12 schema and reject unknown fields, insufficient
cycle counts, insufficient soak without the protected override, and incomplete
override metadata. Do not publish or consume canary evidence until this schema
work is complete.

Cover these exact transitions:

```text
unlisted -> candidate  requires passed integration attestation
candidate -> stable    requires passed integration and canary attestations
stable -> candidate    forbidden
any -> revoked         requires non-empty reason and timestamp
revoked -> any         forbidden for the same runtime ID
```

Require promotion to reject a runtime whose manifest digest differs from either attestation subject.

`unlisted -> candidate` appends one immutable runtime entry and points `candidate` at its `(runtime_id, manifest_sha256)`. `candidate -> stable` adds the immutable canary attestation URL/digest to that same entry and moves `stable`; it does not remove older entries or rewrite their manifests. Support/EOL classification updates mutate only catalog metadata through reviewed PRs.

- [ ] **Step 2: Run and verify failure**

```bash
scripts/test-firecracker-runtime-promotion.sh
```

Expected: FAIL because stable/canary and revocation transitions are not implemented yet.

- [ ] **Step 3: Implement canary validation**

The canary workflow is manual and runs on labels:

```yaml
runs-on: [self-hosted, linux, x64, kvm, yeet-runtime-canary]
environment: firecracker-runtime-canary
```

It downloads the candidate by exact manifest digest and performs at least 25 boot cycles, 10 natural reboots, kernel synchronization with current and previous kernels, five disk snapshot/restore cycles, networking checks, explicit runtime rollback, Catch restart, and interrupted transaction recovery. Default soak is 24 hours. `shorten_soak=true` requires the protected `firecracker-runtime-emergency` environment and records `emergency_override: true` plus the approver and reason in the canary attestation.

- [ ] **Step 4: Implement reviewed promotion and revocation**

`promote-firecracker-runtime.sh` verifies all remote assets and writes only `runtime-catalog.json`. The workflow creates a branch named `promote/<runtime-id>/<channel>`, commits the catalog change, and opens a pull request. It never pushes directly to `main` and never auto-merges.

`revoke-firecracker-runtime.sh` removes the runtime from active channels, marks its retained catalog entry `revoked`, appends one digest-qualified revocation record, and creates a reviewed pull request. It does not delete the runtime entry or release.

- [ ] **Step 5: Test and commit**

```bash
scripts/test-firecracker-runtime-promotion.sh
scripts/test-firecracker-runtime-attestations.sh
scripts/verify-runtime-catalog.sh
git add .github README.md runtime-catalog.json scripts
git commit -m "runtime: add canary and deliberate promotion"
```

### Task 7: Prove and promote the first stable runtime

**Files:**
- Modify through reviewed workflow: `runtime-catalog.json`
- Modify: `README.md`

**Interfaces:**
- Consumes: all prior publishing, integration, canary, and promotion machinery.
- Produces: the first stable runtime catalog entry for the exact approved manifest digest.

- [ ] **Step 1: Run all repository-local tests**

```bash
scripts/test-firecracker-runtime-contracts.sh
scripts/test-firecracker-release-discovery.sh
scripts/test-firecracker-runtime-build.sh
scripts/test-firecracker-runtime-workflows.sh
scripts/test-firecracker-runtime-attestations.sh
scripts/test-firecracker-runtime-promotion.sh
scripts/test-kernel-release-workflows.sh
scripts/test-latest-kernel-automation.sh
scripts/verify-catalog.sh
git diff --check
```

Expected: PASS.

- [ ] **Step 2: Pin the candidate and completed Catch implementation**

Read the candidate entry created in Task 5 and require its exact runtime ID/manifest digest as workflow inputs. Record the reviewed Yeet commit containing Catch runtime-management Tasks 1-10. Fail if the candidate moved, was revoked, lost its integration attestation, or differs from the digest exercised during Catch implementation.

- [ ] **Step 3: Run the full Catch canary**

Dispatch the canary workflow with the pinned candidate and Catch commit. Run the full soak, runtime upgrade/trial/fallback/rollback paths, kernel synchronization, disk-only recovery, raw/ZFS storage, custom roots, and interrupted transaction recovery. Require a new immutable passed canary attestation for that exact subject.

- [ ] **Step 4: Promote stable deliberately**

Run the stable promotion workflow only after reviewing both immutable attestations. Merge its pull request, then verify `runtime-catalog.json` points to the same runtime manifest digest tested by integration and canary.

- [ ] **Step 5: Verify public artifacts without mutation**

```bash
scripts/verify-runtime-catalog.sh runtime-catalog.json
stable_manifest_url="$(jq -r '
  .architectures.amd64 as $a
  | $a.channels.stable as $s
  | $a.runtimes[]
  | select(.runtime_id == $s.runtime_id and .manifest_sha256 == $s.manifest_sha256)
  | .manifest_url
' runtime-catalog.json)"
stable_manifest_sha="$(jq -r '.architectures.amd64.channels.stable.manifest_sha256' runtime-catalog.json)"
verify_dir="$(mktemp -d)"
trap 'rm -rf "$verify_dir"' EXIT
curl -fsSL "$stable_manifest_url" -o "$verify_dir/runtime-manifest.json"
test "$(sha256sum "$verify_dir/runtime-manifest.json" | awk '{print $1}')" = "$stable_manifest_sha"
jq -e '.runtime_id and .components.firecracker and .components.jailer' "$verify_dir/runtime-manifest.json"
```

Expected: the stable manifest is readable, immutable, and matches the catalog digest. Do not delete prior image-bundle runtimes or rewrite old releases.
