# Web Run Terminal Design

## Goal

Improve `yeet run --web` so clicking Deploy gives the user visible deploy
progress in the browser while preserving the terminal as the authoritative
session.

The browser should mirror the same output the user would see in the local
terminal, then clearly tell the user to close the tab and return to the
terminal when deployment succeeds. If deployment fails, the web UI must stay
alive, preserve the form values, and let the user adjust fields and retry
without starting over.

## Scope

V1 covers new-service web deployments only, matching the current `yeet run
--web` scope.

In scope:

- Start deploys as background jobs instead of holding one blocking HTTP request.
- Stream deploy output into a read-only browser terminal.
- Fan out deploy output to both the real terminal and browser stream.
- Keep failed deploys editable and retryable inside the same browser session.
- Detect tab close well enough to print a terminal hint that the web UI is gone.
- Keep successful deploys single-use and shut down the local web server after
  success.

Out of scope:

- Browser keyboard input to the remote command.
- A full interactive PTY in the browser.
- Existing-service redeploy support.
- Runtime loading of terminal libraries from a public CDN.

## Current Behavior

The current web deploy endpoint is `POST /api/deploy`. It validates the posted
`RunDraft`, calls `executeRunDraft`, and returns one JSON or error response when
the entire deploy is finished.

The deploy path writes progress to `os.Stdout`, so the browser can only show a
coarse "Deploying" state while waiting for the request to complete. This is
confusing because the user has moved into the browser but the meaningful
progress is still in the terminal.

## Approach

Use a local deploy job plus read-only output stream.

`POST /api/deploy` becomes a start endpoint:

1. Decode the `RunDraft`.
2. Enforce CSRF/session authorization as it does today.
3. Re-run validation with `NewServiceOnly = true`.
4. If validation fails, return the current structured validation error response.
5. If validation succeeds, create a deploy job, start it in a goroutine, and
   return `{ "ok": true, "jobId": "..." }`.

The browser then connects to:

```text
GET /api/deploy/<job-id>/stream
```

The stream replays buffered output, follows live output, and ends with a final
status event. Deployment continues if the stream disconnects.

This keeps the normal yeet/catch deploy path intact. The web feature only adds
output routing and a local job lifecycle around the existing shared `RunDraft`
executor.

## Deploy Job Model

The local web server owns one active deploy job at a time.

A job contains:

- job ID
- normalized draft
- state: `running`, `succeeded`, or `failed`
- final error text, if failed
- monotonically increasing event IDs
- bounded output replay buffer
- active stream subscriber count
- completion channel used by `yeet run --web`

Only one job may run at a time. A successful job makes the session complete and
prevents further deploys. A failed job returns the server to the ready state so
the user can edit and retry.

Starting a retry creates a new job. The previous failed output remains visible
in the browser until the new job starts, then the terminal sheet is cleared for
the new deploy.

## Output Capture

Introduce a narrow execution option seam so web deploy can pass an output
writer without changing normal CLI behavior.

Conceptually:

```go
type RunDraftExecuteOptions struct {
	Stdout      io.Writer
	ForceDeploy bool
}
```

Normal CLI execution defaults `Stdout` to `os.Stdout`.

The web deploy job passes a fan-out writer:

```text
deploy output -> terminal stdout
              -> job output buffer
              -> live browser subscribers
```

Lower-level code on the `RunDraft` path that currently writes directly to
`os.Stdout` should accept the configured writer instead. The important paths are
run-change status lines and remote exec output. The catch server remains
authoritative for remote command parsing and execution.

The fan-out writer must tolerate browser subscriber failures. A broken browser
stream must not fail the deploy or block terminal output.

## Stream Protocol

Use Server-Sent Events for V1. The terminal is read-only, so SSE is simpler than
a bidirectional WebSocket and gives natural reconnect behavior.

Events:

```text
event: output
id: <number>
data: {"encoding":"base64","chunk":"..."}

event: status
id: <number>
data: {"state":"running"}

event: status
id: <number>
data: {"state":"succeeded"}

event: status
id: <number>
data: {"state":"failed","error":"..."}
```

Output chunks should be base64 encoded so ANSI sequences and carriage returns
are preserved byte-for-byte. The browser decodes bytes with `TextDecoder` and
feeds them into the terminal renderer.

The replay buffer should be bounded to avoid unbounded memory growth. Use a
fixed byte budget, initially 1 MiB per job. If older chunks are dropped, insert
one synthetic output note such as:

```text
[older output omitted]
```

SSE reconnect should use `Last-Event-ID` when available. If the requested event
is no longer in the buffer, replay from the earliest retained event plus the
omission note.

## Browser Terminal UI

Deploy output should become the focus after submit without taking over the
entire page.

Use a bottom-sheet terminal that slides up from the fixed action bar and covers
the lower portion of the form. It should be compact by default, sized for the
common case of a few short progress lines, with scrollback for longer failures.
An expand control may increase the sheet height for wider or noisier output.

The terminal header shows:

- `service@host`
- status: running, succeeded, or failed
- controls: expand/collapse and copy output

Read-only behavior:

- Do not send keyboard input to the deploy.
- Allow normal browser scrolling, text selection, copy, and focus navigation.
- Auto-scroll while the user is at the bottom.
- Stop auto-scrolling when the user scrolls up.
- Resume auto-scroll when the user scrolls back to the bottom.

Footer/action behavior:

- While running: show a running state and disable fields.
- On success: show "Deployed. Close this tab and return to the terminal."
- On failure: show "Deploy failed", keep the terminal visible, unlock fields,
  resume validation, and allow retry.

The form stays mounted behind the terminal sheet and keeps all values.

## Terminal Renderer

V1 should use a small embedded read-only renderer behind a JavaScript adapter.
The adapter API should be intentionally narrow:

```js
terminal.open(element)
terminal.write(bytes)
terminal.clear()
terminal.copyText()
terminal.dispose()
```

The first renderer can be simple:

- preserve line breaks
- handle `\r` by replacing the current line
- strip or lightly render common ANSI color/style sequences
- preserve a plain-text transcript for copy output

The adapter boundary leaves room to replace the renderer later with
`ghostty-web` or another terminal emulator.

Research notes:

- `ghostty-web` provides an xterm-compatible browser terminal using a
  WASM-compiled Ghostty parser and is designed to connect to WebSocket-style
  terminal backends.
- It ships JS plus a WASM asset, so yeet should not load it from `esm.sh` or
  another runtime CDN by default.
- If adopted later, pin and vendor the JS/WASM assets into `web_run_assets`, or
  add a deterministic build step that embeds them into the binary.

References:

- <https://github.com/coder/ghostty-web>
- <https://www.jsdelivr.com/package/npm/ghostty-web>

## Tab Close Detection

Closing the browser tab should not kill a running deploy. The job continues and
the real terminal keeps receiving output.

The browser should notify the local server on `pagehide` with
`navigator.sendBeacon` or a best-effort fetch to:

```text
POST /api/session/closed
```

When the server sees the tab close while the session is not successfully
complete, print a concise terminal message once:

```text
Browser tab closed. Press Ctrl-C to quit.
```

Because browser close events are best-effort, also treat "no active stream
subscribers" as a fallback signal. Debounce it briefly so refreshes and SSE
reconnects do not print the message. The message is informational only; it must
not cancel validation, deployment, or the local server.

## Shutdown Semantics

Successful deploy:

- job state becomes `succeeded`
- final status event is sent
- browser shows close-tab guidance
- `OnComplete` fires
- `yeet run --web` prints the terminal completion message and shuts down the
  local web server

Runtime failure:

- job state becomes `failed`
- final status event includes the error
- append the error text to the terminal stream if the lower-level path did not
  already print it
- `OnComplete` does not fire
- web server stays open
- form unlocks and retry is possible

Validation failure:

- no job is created
- response remains structured validation JSON
- inline field errors are shown
- terminal sheet does not open

Terminal interrupt:

- cancel the local web server context
- cancel any active deploy job through the existing context path
- close streams
- return the interrupt/cancellation error as the CLI already does

## API Summary

Existing:

- `GET /api/bootstrap`
- `GET /api/files?dir=<relative-dir>`
- `POST /api/validate`

Changed:

- `POST /api/deploy`
  - returns validation errors before job creation
  - returns `{ok:true, jobId}` after job start

New:

- `GET /api/deploy/<job-id>/stream`
  - authenticated SSE output/status stream
- `GET /api/deploy/<job-id>/status`
  - optional JSON fallback for tests and reconnect diagnostics
- `POST /api/session/closed`
  - best-effort browser-close notification

All unsafe endpoints keep the existing token/CSRF rules.

## Error Handling

Runtime errors should be visible in two places:

- final terminal output line
- concise form status

The full error stays in the terminal sheet because that is where command output
and context are visible. Inline form errors should be reserved for validation
errors that map to fields.

If stream setup fails after the job starts, the deployment still runs and the
real terminal still shows output. The browser should show a stream error and
offer a reconnect/retry viewing action without resubmitting the deploy.

## Testing Plan

Unit and integration tests:

- `POST /api/deploy` returns quickly with a job ID after valid submission.
- Valid deploy creates exactly one running job.
- A running deploy rejects a second deploy.
- A successful deploy marks the session complete and triggers `OnComplete`.
- A failed deploy resets deploy state to ready and does not trigger
  `OnComplete`.
- Job output is written to both the configured terminal writer and replay
  buffer.
- SSE subscribers receive replayed chunks, live chunks, and final status.
- Request/browser cancellation does not cancel the deploy job.
- `POST /api/session/closed` prints the terminal hint once and does not cancel
  work.
- Existing validation, CSRF, static auth, and new-service-only behavior remain
  intact.
- JS asset tests verify the terminal sheet exists, deploy starts then follows a
  stream, success shows close-tab guidance, and failure returns to editing mode.

Manual smoke:

- Run `yeet run --web` against a disposable service on a real catch host.
- Confirm deploy output appears in both the local terminal and browser sheet.
- Confirm success tells the user to close the tab and the CLI exits.
- Confirm a forced runtime failure leaves the form editable and permits retry.
- Confirm closing the tab prints the Ctrl-C hint in the terminal.
- Remove the disposable service after testing.

Verification commands:

```bash
go test ./pkg/yeet -count=1
go test ./pkg/catchrpc ./pkg/catch ./pkg/yeet -count=1
go test ./... -count=1
node --check pkg/yeet/web_run_assets/app.js
pre-commit run --all-files
```

