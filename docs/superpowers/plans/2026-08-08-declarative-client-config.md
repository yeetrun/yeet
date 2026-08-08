# Declarative Client Config Persistence Implementation Plan

> **Goal:** Make semantically identical client-config saves true no-ops while
> preserving clear failures for real changes to immutable managed symlinks.

## Task 1: Lock the persistence contract with failing tests

**Files:**

- Modify: `pkg/yeet/prefs_test.go`
- Modify: `pkg/yeet/init_test.go`

1. Add a persistence-boundary test whose existing TOML differs in comments,
   formatting, host case, duplicate entries, and workspace order but normalizes
   to the desired config.
2. Assert that `saveTo` returns nil and leaves both the symlink identity and
   target bytes unchanged.
3. Add a real-change test that makes the target unwritable and asserts the
   write fails with the underlying permission cause and managed-config guidance
   while preserving the symlink.
4. Add an init test proving an already-equivalent managed config no longer
   makes successful Catch installation report a local-setup failure.
5. Run the new focused tests and record the expected RED result.

## Task 2: Implement semantic no-op persistence

**Files:**

- Modify: `pkg/yeet/prefs.go`

1. Build the normalized desired persisted model with the existing
   `clientConfigForTOML` path.
2. Add a small helper that reads, decodes, normalizes, and semantically compares
   an existing TOML config.
3. Return before filesystem mutation when the models match.
4. Preserve the existing write path for missing, unreadable, malformed, or
   semantically different files.
5. On write failure, inspect the configured path without following it and add
   declarative-management guidance only when it is a symlink.
6. Run the focused tests and `mise exec -- go test ./pkg/yeet -count=1`.

## Task 3: Verify compatibility and quality

**Files:**

- Modify tests only if a newly demonstrated regression requires it.

1. Confirm existing migration, writable save, host override, and config-command
   tests still pass.
2. Run `mise exec -- go test ./... -count=1` once on the stable candidate.
3. Run `mise run quality` and address only findings caused by this task.
4. Run `mise exec -- pre-commit run --all-files` once before committing.
5. Review `but diff`, assign only this task's files to
   `codex/declarative-client-config`, and commit with GitButler.
