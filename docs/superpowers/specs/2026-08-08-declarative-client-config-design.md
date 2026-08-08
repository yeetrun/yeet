# Declarative Client Config Persistence Design

## Goal

Allow Yeet commands to succeed when they attempt to persist client configuration
that is already semantically present in `~/.config/yeet/config.toml`, including
when that path is a symlink to an immutable declaratively managed file.

An actual configuration change must retain the current fail-safe behavior: Yeet
must not unlink or replace a managed symlink. If writing through the symlink
fails, the error should retain the operating-system cause and explain that the
configuration may need to be changed through its declarative manager.

## Decision

The semantic no-op belongs in `clientConfig.saveTo`, the shared persistence
boundary used by init, explicit `yeet config` mutations, project persistence,
and legacy migration. An init-only bypass would leave other callers with the
same failure, while a byte comparison would mistake comments, formatting, and
workspace ordering for changes.

Before writing, `saveTo` reads and decodes an existing TOML file into the
persisted client-config model. It normalizes both the existing and desired
models and compares only persisted semantic fields:

- the normalized default host;
- normalized, deduplicated, sorted workspace paths.

When those fields match, `saveTo` returns success without creating directories,
changing permissions, rewriting bytes, or replacing a symlink. Existing
comments, formatting, key order, and unrecognized fields remain byte-for-byte
untouched.

If the file is absent, unreadable, or invalid TOML, Yeet retains its existing
save behavior and attempts to write the desired canonical TOML. The semantic
check must not turn a readable-state optimization into a new refusal to save a
repairable config.

## Managed Symlink Failure

For a real semantic change, Yeet continues to call `os.WriteFile` on the
configured path. This deliberately follows an existing symlink rather than
replacing it. When that write fails and the configured path is a symlink, Yeet
wraps the original error with an actionable explanation that the client config
may be declaratively managed and should be changed through that manager.

The operation still fails. The explanation is guidance, not permission to
silently ignore a requested change. Non-symlink write failures retain their
existing error behavior.

## Compatibility

Legacy JSON preference migration continues to create TOML when no TOML exists
and to remove the legacy file only after a successful save. Normal writable
config changes continue to serialize canonical TOML. Runtime host overrides
remain excluded from persisted state through `clientConfigForTOML`.

No CLI syntax, RPC boundary, or public documentation changes are required.

## Verification

Tests cover:

- equivalent TOML with different formatting, comments, host case, duplicate or
  reordered workspaces returning success without changing bytes;
- an equivalent immutable symlink remaining a symlink with its target
  unchanged;
- a real change still invoking the write path and returning the underlying
  permission error plus declarative-management guidance without replacing the
  symlink;
- `finishInitWorkspaceSetup` succeeding against an already-equivalent managed
  config;
- ordinary writable changes and legacy migration continuing to save normally.
