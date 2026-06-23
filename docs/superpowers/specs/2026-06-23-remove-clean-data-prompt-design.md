# Interactive remove data prompt

## Goal

`yeet rm <svc>` should make the data-deletion choice visible during an
interactive removal without changing the safe automation contract. Plain removal
continues to preserve service data by default. Users can still delete data
non-interactively with `--clean-data`.

## Behavior

Interactive `yeet rm <svc>` keeps the current first confirmation:

```text
Are you sure you want to remove service "<svc>"? [y/N]:
```

If the user declines, removal stops and no data prompt is shown.

If the user accepts and `--clean-data` was not passed, catch asks a second
question before cleanup begins:

```text
Delete all data for service "<svc>"? [y/N]:
```

`y` opts into data deletion for that removal. Blank input, `n`, and any other
response preserve service data. The same label logic used by the first prompt
should apply, so VM removals say `VM` instead of `service`.

`yeet rm --clean-data <svc>` skips the new data prompt and deletes data. `yeet
rm --yes <svc>` remains prompt-free and preserves data unless `--clean-data` is
also present. `yeet rm --yes --clean-data <svc>` remains prompt-free and deletes
data.

## Architecture

Add the new prompt on the catch side in `pkg/catch/tty_service.go`, near
`ttyExecer.removeCmdFunc`. Catch already owns remote remove parsing, the
destructive confirmation prompt, runner cleanup, and the final
`RemoveServiceWithOptions` call that receives `CleanData`.

The prompt should update a local `cli.RemoveFlags` value before calling
`removeRunner` and `removeServiceConfig`. This keeps `RemoveServiceWithOptions`
unchanged and avoids adding a new RPC or client-side cleanup path.

The local `pkg/yeet` wrapper should continue filtering only local
`--clean-config` behavior and should continue prompting separately for removing
the matching `yeet.toml` entry after the remote removal succeeds.

## Data Flow

1. `cmd/yeet` routes `rm`/`remove` to `pkg/yeet`.
2. `pkg/yeet` parses local-only flags, forwards the remaining remove args to
   catch, and preserves the existing remote TTY session.
3. Catch parses `cli.RemoveFlags`.
4. Catch validates the service and gets its runner.
5. Catch asks the existing removal confirmation unless `--yes` was passed.
6. Catch asks the new data prompt only when the first prompt was accepted,
   `--yes` is false, and `--clean-data` is false.
7. Catch removes the runner and calls `RemoveServiceWithOptions` with the final
   `CleanData` value.
8. After remote success, the local wrapper keeps the existing optional
   `yeet.toml` cleanup prompt.

## Error Handling

Prompt read/write errors should abort removal with contextual errors, matching
the existing confirmation behavior. Declining the data prompt is not an error;
it only leaves `CleanData` false.

The existing fatal cleanup behavior for ZFS clean-data failures remains
unchanged: if data deletion was requested and the ZFS destroy fails,
`RemoveServiceWithOptions` must fail before removing the database entry.

## Testing

Add focused tests around catch-side remove prompt behavior:

- accepting removal and declining data preserves service data
- accepting removal and accepting data deletes service data
- `--clean-data` deletes data without asking the new prompt
- `--yes` preserves data and does not ask any prompt
- VM removal uses the VM label in the data prompt

Keep existing parser and local wrapper tests passing. Add a `pkg/yeet` routing
test only if the forwarded args or local prompt sequencing changes; the preferred
design should not require client-side routing changes.

## Documentation

Update CLI-facing documentation for `yeet remove`/`yeet rm`:

- interactive plain remove offers data deletion but defaults to no
- scripts and non-interactive cleanup should pass `--clean-data` explicitly
- `--yes` skips prompts and does not imply `--clean-data`

README smoke-test commands that intentionally delete disposable data can keep
using `--clean-data`.
