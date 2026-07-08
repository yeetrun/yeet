# XDG client config and host-scoped workspaces

## Goal

Yeet should remember the user's service workspace so normal commands work from
outside that directory without losing the visible `yeet.toml` project model.
The client should move local defaults from `~/.yeet/prefs.json` to an XDG TOML
config, associate catch hosts with workspaces through `yeet.toml`, and avoid
silently creating project config in arbitrary directories.

The primary workflow is a workstation with one durable workspace, such as
`~/yeet-services`, that may operate more than one catch host, such as
`morpheus-catch` and `trinity-catch`.

## Behavior

The canonical client config lives at:

```text
$XDG_CONFIG_HOME/yeet/config.toml
```

If `XDG_CONFIG_HOME` is unset, yeet uses:

```text
~/.config/yeet/config.toml
```

The config schema is:

```toml
default_host = "morpheus-catch"
workspaces = ["/srv/yeet-services"]
```

`default_host` is the catch hostname used when a command does not provide
`--host`, `CATCH_HOST`, or `<svc>@<host>`. Workspace paths are stored as
absolute, cleaned paths. Catch host names are normalized to lowercase before
they are saved or matched. Service names keep their existing validation and are
not changed by this feature.

`yeet.toml` remains the source of truth for which catch hosts belong to a
workspace:

```toml
version = 1
hosts = ["morpheus-catch", "trinity-catch"]
```

A workspace may contain many catch hosts. A catch host must not be owned by
more than one registered workspace. Host ownership is derived from `hosts` and
from service entries:

```toml
[[services]]
name = "web"
host = "morpheus-catch"
```

Service-entry hosts count so existing project files still resolve even if their
top-level `hosts` list is incomplete. When yeet writes a `yeet.toml` for another
reason, it should normalize all stored host names to lowercase and ensure
service hosts are reflected in `hosts`. Yeet should not rewrite files only to
normalize casing.

## Project config resolution

Project config lookup follows this order:

1. An explicit command-specific `--config` path, for commands that already have
   that flag.
2. The nearest `yeet.toml` found by walking upward from the current directory.
3. A registered workspace whose `yeet.toml` claims the effective catch host.

This change does not add a new global `--config` flag.

When the current directory already has a local/upward `yeet.toml`, that local
project context wins for the current command even if a registered workspace also
claims the same catch host. In that overlap case, interactive yeet may warn that
the local project overlaps a registered workspace, but it should continue using
the local file. It should not register the local path as a workspace unless the
user explicitly confirms that ownership change in a future workflow.

When no local project file exists, yeet determines the effective catch host from
the command:

1. `<svc>@<host>`
2. `--host`
3. `CATCH_HOST`
4. saved `default_host`
5. default `catch`

It then scans registered workspace paths. If exactly one workspace claims the
host, that workspace's `yeet.toml` is used and relative payload/env paths
resolve against the workspace directory.

If more than one registered workspace claims the host, yeet must error instead
of guessing. The error should list the conflicting paths and tell the user to
remove one with `yeet config --remove-workspace PATH` or edit one `yeet.toml`.

If no registered workspace claims the host, read-only commands continue using
the effective host without prompting. Write-capable commands may prompt to adopt
or choose a workspace.

## Workspace adoption

Yeet should only write `yeet.toml` in a known workspace:

- a configured workspace that claims the effective host
- a local/upward directory that already contains `yeet.toml`
- a directory the user just adopted as a workspace
- a directory passed through an explicit workspace flag during setup

If there is no configured workspace and a local/upward `yeet.toml` exists,
interactive yeet can ask whether to register that directory as a workspace. If
the user accepts, yeet adds the directory to XDG config. If the user declines,
the command continues using the local `yeet.toml` for this run.

If no local/upward `yeet.toml` exists and a write-capable command such as
`yeet run` or `yeet cron` needs project persistence, interactive yeet should ask
about a workspace before saving client-side config.

With no known bare workspaces, the prompt can be a simple confirmation:

```text
Use /current/directory as a yeet workspace?
```

If the user accepts, yeet adds the current directory to `workspaces`, creates or
seeds `yeet.toml` there, adds the effective host, and saves the service entry.

If the user declines, the deploy or operation continues without client-side
persistence and yeet prints a warning that the service config was not saved.
Future hostless reruns will need payload arguments or a workspace.

With one bare registered workspace, yeet should prompt before associating the
effective host with that workspace:

```text
No workspace is associated with trinity-catch.
Use /srv/yeet-services for trinity-catch?
```

With multiple bare registered workspaces and no host match, interactive yeet
should offer a selection:

```text
No workspace is associated with trinity-catch.
Choose a workspace:
  1. /srv/yeet-services
  2. /srv/lab-services
  3. /tmp/current-dir
  4. Run once without saving
```

If the user chooses a workspace, yeet associates the effective host by adding it
to that workspace's `yeet.toml`. If the user chooses to run once, the choice is
process-local only. Yeet should not persist an ignore list in this feature.

Non-interactive commands do not prompt. If no known workspace is available,
write-capable commands continue without saving client-side project config and
print the same warning.

`yeet run --web` should use the same workspace resolution rules as CLI
`yeet run`. Any terminal adoption prompt should happen before the browser UI
launches, and the selected config path should be passed into the web bootstrap
data.

## Init workflow

`yeet init` gets two new flags:

```bash
yeet init --workspace ~/yeet-services root@server.example
yeet init --no-workspace root@server.example
```

The positional init argument remains the SSH machine host. It is never used as
the catch hostname. For example:

```bash
yeet --host=trinity-catch init root@server.example
```

installs over SSH to `root@server.example`, but saves and seeds catch host `trinity-catch`.
The catch hostname comes from `--host`, `CATCH_HOST`, saved config, or default
`catch`.

Interactive init prompts for the workspace after remote catch installation
succeeds. The default is `~/yeet-services`; accepting the default creates that
directory, saves the absolute path in XDG config, saves the effective catch host
as `default_host`, and seeds or updates `<workspace>/yeet.toml`.

For a new seed file:

```toml
version = 1
hosts = ["trinity-catch"]

# [[services]]
# name = "hello"
# host = "trinity-catch"
# payload = "nginx:alpine"
# args = ["-p", "18080:80"]
```

The real persisted state is `version` and `hosts`; the service example is
commented and illustrative only.

If `yeet.toml` already exists, init parses it and adds the effective catch host
only if missing. It must not overwrite or append to a file that fails to parse.
When it does save the file, host names should be normalized to lowercase.

Non-interactive init does not create, seed, or save a workspace unless
`--workspace` is passed. `--workspace PATH` creates `PATH` if needed, registers
it, associates the effective catch host with its `yeet.toml`, and saves
`default_host`. It adds the workspace to the configured list; it does not
replace the list. `--no-workspace` suppresses the interactive workspace prompt.

Workspace setup is a post-install local phase. If remote catch installation
succeeds but local workspace setup fails, `yeet init` should return an error
that clearly says catch installed successfully but local workspace setup failed.

`yeet init` should print next steps after setup. If a workspace is saved, print
`cd <workspace>` and a disposable nginx command. If the user declined workspace
setup, print the disposable command and say it will run once without saving
unless they set a workspace with `yeet config --workspace PATH`.

## Config command

`yeet prefs` is removed. `yeet config` is the canonical command:

```bash
yeet config
yeet config --host morpheus-catch
yeet config --workspace ~/yeet-services
yeet config --add-workspace ~/lab-services
yeet config --remove-workspace ~/lab-services
yeet config --clear-workspaces
```

Mutations save immediately; there is no `--save` flag.

`yeet config` prints TOML to stdout. If running in a TTY, it may print the
config path to stderr so stdout remains parseable.

`yeet config --host HOST` updates only `default_host`. It must not associate
the host with any workspace.

`yeet config --workspace PATH` replaces the workspace list with exactly one
normalized path. `PATH` must already exist and be a directory.

`yeet config --add-workspace PATH` appends a normalized path if absent. `PATH`
must already exist and be a directory.

`yeet config --remove-workspace PATH` removes the normalized path. It does not
need the directory to exist.

`yeet config --clear-workspaces` removes all workspace paths.

The config command is local only and does not require catch RPC authorization.

## Migration

If the XDG TOML config exists, it wins. If `~/.yeet/prefs.json` also exists,
yeet must not merge old values into the existing TOML config, but it should
best-effort delete the old JSON file and remove `~/.yeet` if it becomes empty.

If XDG TOML does not exist and `~/.yeet/prefs.json` exists, yeet migrates the
old JSON value literally:

```json
{"defaultHost":"morpheus-catch"}
```

becomes:

```toml
default_host = "morpheus-catch"
```

Migration does not try to discover workspaces from the filesystem. After a
successful TOML write, yeet deletes `~/.yeet/prefs.json`, then best-effort
removes `~/.yeet` if it is empty. Failure to delete the old file should be
reported as a warning, but the migrated config remains usable.

Only the TOML-native snake_case schema is supported in the new config file.
Old camelCase is handled only through JSON migration.

Config parse errors are fatal for commands that need host or workspace
resolution. Pure help and schema output should still work. Implementation
should avoid surfacing config parse errors from package `init()` before the
command has been routed.

## Prompt layer

Introduce a shared local prompt layer backed by Charm `huh`. The layer should
support:

- yes/no confirmations
- text input with defaults
- secret input
- selection lists

Use this shared layer for workspace adoption and all `yeet init` prompts in
this work. The prompt conversion should be separate from the functional
config/workspace commit, but it should happen before the feature is considered
complete.

Existing non-interactive behavior remains unchanged. Destructive runtime
confirmations such as `yeet rm --clean` are out of scope unless implementation
finds they must share helper code; this design targets setup/config prompts.

Tests should use fake prompt implementations instead of brittle terminal
automation.

## Architecture

Add a client config module in `pkg/yeet` for:

- XDG path resolution
- TOML load/save
- legacy JSON migration and cleanup
- normalized default host access
- normalized workspace path management
- delayed error reporting for commands that need config

Replace the current prefs globals with the new config model. `Host()` and host
override behavior should continue to work for callers, but ordinary runtime
overrides must not update saved config. `--host`, `CATCH_HOST`, and
`foo@host` affect the current command only. Saved host changes come from
`yeet config --host` and successful init workspace setup.

Extend project config loading centrally so existing call sites keep most of
their shape:

- local/upward lookup behavior remains available
- fallback workspace lookup uses effective host ownership
- write locations are resolved through a known-workspace helper
- commands that decline workspace adoption can run with nil project persistence
  and a warning

The implementation should be careful around existing helpers such as
`loadProjectConfigFromCwd`, `loadOrCreateProjectConfigFromCwd`, `runConfigLocation`,
`saveRunConfig`, and `saveCronConfig`, because those are where silent arbitrary
`yeet.toml` creation currently happens.

`yeet status` with no args preserves project-host behavior. Once a workspace is
resolved, no-arg status uses all hosts in that workspace's `yeet.toml`; the
effective host is only used to pick the fallback workspace. `yeet upgrade check`
follows the same rule.

Authorization mapping: `yeet config` is local-only and requires no catch
permission. Workspace resolution does not add a new remote operation.
`yeet init` continues to use its existing SSH setup path and does not add a new
catch RPC permission boundary.

## Error handling

Duplicate host ownership across registered workspaces is an error. The message
should include the host and every conflicting workspace path.

Malformed XDG config is an error for commands that need config. The error
should include the config path and parse failure.

Malformed workspace `yeet.toml` is an error when that workspace is selected,
registered, or associated with a host. Yeet must not overwrite malformed files.

If a workspace path in XDG config no longer exists, resolution should not claim
hosts from it. For `yeet config --remove-workspace`, the missing path can still
be removed. For resolution, the implementation can warn or ignore missing
registered paths, but duplicate detection should only use readable workspace
configs.

If the user declines workspace adoption during a write-capable command, yeet
continues the remote operation without project persistence and prints a warning.

## Testing

Add focused tests for client config:

- XDG path selection with and without `XDG_CONFIG_HOME`
- TOML load/save with snake_case fields
- old `~/.yeet/prefs.json` migration to TOML
- old JSON file deletion and empty directory cleanup
- parse errors surfaced only for commands that need config
- lowercase host normalization
- absolute workspace path normalization

Add workspace resolution tests:

- local/upward `yeet.toml` wins
- registered workspace selected by top-level `hosts`
- registered workspace selected by service-entry `host`
- duplicate host ownership errors with both paths
- bare workspace prompts before host association
- multiple bare workspaces produce a selection prompt
- read-only commands do not prompt to create a workspace
- write-capable command can run without saving after prompt decline
- `status` and `upgrade check` use all hosts from a resolved workspace

Add command tests:

- `yeet config` prints TOML
- `--host`, `--workspace`, `--add-workspace`, `--remove-workspace`, and
  `--clear-workspaces` mutate and save immediately
- `--workspace` and `--add-workspace` require an existing directory
- `yeet prefs` is no longer registered

Add init tests:

- interactive init saves `default_host` and workspace only after remote success
- non-interactive init without `--workspace` has no workspace side effects
- `--workspace` creates missing directory, registers it, and seeds `yeet.toml`
- `--no-workspace` suppresses the prompt
- existing `yeet.toml` preserves content except for adding the host when needed
- malformed existing `yeet.toml` stops local workspace setup
- post-install workspace failure returns a local setup error
- all init prompts use the shared prompt abstraction

Use fake prompt implementations for prompt paths.

## Verification

Run targeted tests first:

```bash
mise exec -- go test ./pkg/yeet ./cmd/yeet ./pkg/cli -count=1
```

Run the full suite before integration:

```bash
mise exec -- go test ./... -count=1
```

Run pre-commit before committing implementation:

```bash
mise exec -- pre-commit run --all-files
```

For release-grade completion, run the repo quality gate required for meaningful
client orchestration changes:

```bash
mise run quality:goal
```

Manual smoke checks should include:

```bash
XDG_CONFIG_HOME="$(mktemp -d)" yeet config --host morpheus-catch
XDG_CONFIG_HOME="$(mktemp -d)" yeet init --workspace /tmp/yeet-services root@example
yeet run hello nginx:alpine
yeet run hello@trinity-catch nginx:alpine
yeet status
yeet upgrade check
```

Use fake or isolated hosts for manual checks unless intentionally validating
against live catch hosts.

## Documentation

Update README and website docs:

- `yeet config` replaces `yeet prefs`
- config moved to `$XDG_CONFIG_HOME/yeet/config.toml`
- old prefs migrate automatically
- workspaces can be registered and resolved from outside the workspace
- `cd ~/yeet-services` is still useful for natural relative payload paths and
  examples, but it is no longer required for yeet to find a configured
  workspace
- `yeet init --workspace` and `--no-workspace`

The changelog should include a user-facing breaking-change bullet:

```text
Client preferences moved from ~/.yeet/prefs.json to
$XDG_CONFIG_HOME/yeet/config.toml, and yeet config replaces yeet prefs.
Existing prefs migrate automatically on first run.
```

## Commit sequencing

Implement this as at least two commits:

1. XDG TOML config, workspace resolution, config command, init workspace setup,
   migration, docs, and tests.
2. Shared Charm `huh` prompt layer and conversion of all `yeet init` prompts.

The prompt conversion is part of this work, but keeping it separate makes review
easier and isolates UI dependency churn from the workspace semantics.

## Out of scope

This design does not add a global `--config` flag, a persistent "never ask
here" ignore list, interactive duplicate-workspace repair, automatic workspace
discovery during migration, or changes to remote catch authorization. It also
does not move every runtime destructive confirmation to Charm; only setup and
config prompts are in scope.
