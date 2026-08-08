# Isolated Network Service Information Implementation Plan

**Goal:** Show the effective isolated-network endpoint and namespace clearly,
without duplicate modes or internal lifecycle terminology.

**Architecture:** Keep the existing `iso` RPC envelope for compatibility, add
an optional namespace field, project endpoint data authoritatively in Catch,
and render it as ordinary network information in the client.

**Tooling:** Go, `catchrpc`, Catch database views, Yeet plain/JSON info output,
GitButler, and the repository quality gates.

## Task 1: RPC compatibility contract

**Files:**

- Modify: `pkg/catchrpc/types.go`
- Modify: `pkg/catchrpc/types_test.go`

1. Add failing round-trip tests for the optional namespace field.
2. Add compatibility tests showing old JSON without `namespace` still decodes.
3. Add the optional field without changing existing JSON names or port-presence
   behavior.
4. Run the focused `pkg/catchrpc` tests.

## Task 2: Authoritative Catch projection

**Files:**

- Modify: `pkg/catch/service_info.go`
- Modify: `pkg/catch/service_info_test.go`

1. Add failing tests for native/timer peer endpoints and namespace.
2. Preserve VM peer projection and sorted Compose component projection.
3. Assert that host-side addresses/interfaces and secrets are absent.
4. Implement the kind-aware endpoint projection and namespace assignment.
5. Run focused Catch tests, then `go test ./pkg/catch ./pkg/catchrpc`.

## Task 3: Plain and JSON presentation

**Files:**

- Modify: `pkg/yeet/info_cmd.go`
- Modify: `pkg/yeet/info_cmd_test.go`

1. Add failing tests for the exact healthy native rows.
2. Add failing tests for Compose multi-component and VM peer rows.
3. Add failing tests proving `ready` is hidden and abnormal state/error use
   `Network state` and `Network error`.
4. Replace the duplicated isolation rows with one isolation-detail renderer.
5. Keep non-isolated and desired/effective drift rendering unchanged.
6. Run focused and full `pkg/yeet` tests.

## Task 4: Public terminology audit

**Files:**

- Modify as required: `pkg/yeet/info_cmd.go`, `pkg/yeet/host_set.go`,
  `pkg/cli/cli.go`, their tests, generated agent help, and `README.md`
- Audit: `website/docs/**`

1. Add or update user-facing contract tests before production string changes.
2. Replace directly related uppercase public labels and prose with “isolated
   network” or “isolation”, retaining the literal `iso` token and `--iso-pool`.
3. Regenerate checked-in CLI help if its source changes.
4. Do not modify the shared website gitlink while it is owned by another task;
   record any manual pages that still require a separate safe website commit.
5. Run docs checks and the affected package tests.

## Task 5: Final verification and local checkpoint

1. Run `gofmt` on touched Go files.
2. Run affected package tests and `go test ./... -count=1`.
3. Run the required race/fuzz checks for changed codec and info surfaces.
4. Run `mise run quality` and `mise run quality:goal` on the stable candidate.
5. Run `pre-commit run --all-files` once at the final boundary.
6. Review the final diff for secrets, private host/service names, and unrelated
   files.
7. Commit only this task’s dynamic GitButler change IDs on
   `codex/isolated-network-info`. Do not push or mutate a live service.
