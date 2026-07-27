# Durable Web Terminal Design

## Context

`yeet run --web` mirrors deploy output to the invoking terminal and to a
read-only terminal sheet in the browser. The terminal write happens first, so a
deployment can continue successfully even when the browser output stream falls
behind.

VM provisioning makes the weakness easy to reproduce. It emits frequent
carriage-return, cursor, spinner, and progress updates. The current web job
disconnects a subscriber when its fixed event queue fills, while the browser
handles the resulting `EventSource` error by closing the stream and checking
job status once. If the job is still running, the UI gives up even though the
server retained output specifically for reconnect and replay.

The browser also uses a small custom parser that rewrites the entire text node
for every chunk. It cannot faithfully reproduce general terminal output,
Unicode cell widths, or the cursor behavior of the terminal application.

## Decision

Make the web terminal a durable, read-only rendering of the exact PTY stream
used by the terminal application:

- vendor a pinned `ghostty-web` release into Yeet's embedded web assets;
- pass terminal bytes to the emulator without text normalization;
- initialize and resize the emulator with the same terminal geometry used by
  the remote PTY;
- configure exactly 1,000 lines of browser scrollback;
- replace subscriber event queues with a replayable append-only job journal;
- let `EventSource` reconnect and resume from its last delivered event;
- acknowledge successful rendering before the local web server shuts down; and
- retain the active job journal long enough to recover from a stream reconnect
  or full page refresh.

SSE remains the transport. The stream is read-only, ordered, and local to the
machine running Yeet, so a WebSocket would add a second bidirectional protocol
without solving retention or rendering.

## Goals

- Render the same ANSI, cursor, carriage-return, erase, Unicode, and wrapping
  behavior in the browser and invoking terminal.
- Preserve output through rapid VM progress updates, a slow browser, transient
  stream loss, and browser refresh.
- Keep deploy execution independent of browser speed or connectivity.
- Present final success only after every preceding terminal byte has been
  parsed and rendered.
- Bound browser scrollback to 1,000 lines and bound server-side resource use.
- Preserve the existing single-binary, loopback-only, no-runtime-CDN model.
- Keep failed deployments editable and retryable.

## Non-Goals

- Browser keyboard input or an interactive browser shell.
- Letting the browser independently resize the remote PTY.
- Persisting deploy transcripts after the `yeet run --web` process exits.
- Making browser rendering failure cancel a deployment that is already
  running.
- Defining a new Catch RPC, TTY command, or remote permission boundary.

## Terminal Fidelity Contract

The invoking terminal and browser consume the same ordered byte sequence. Yeet
does not decode, normalize newlines, strip ANSI sequences, or convert invalid
UTF-8 before either sink receives it.

Each web job begins with a terminal profile containing:

- whether the invoking session has a TTY;
- the initial rows and columns used by the remote PTY;
- the invoking `TERM` value; and
- the browser scrollback limit of 1,000 lines.

For a TTY-backed run, Yeet observes the local terminal size for the job
lifetime and records resize events in the same order as output events. The
browser applies those rows and columns to the emulator. The existing remote
execution path continues sending the corresponding geometry to Catch.

Browser layout changes only the visible viewport. It must not call a fit
operation that changes the emulator grid independently, because a different
column count would change wrapping and cursor positions. A viewport narrower
than the PTY may scroll horizontally, and the existing expand control may
expose more of the fixed grid.

When the invoking session is not a TTY, the stream is still rendered by the
emulator but uses a stable fallback geometry. Plain progress remains plain
because the execution path already disables TTY output in that case.

## Embedded Emulator

Vendor the production JavaScript, CSS, WASM, license, version, and integrity
metadata for a pinned `ghostty-web` release under `pkg/yeet/web_run_assets`.
The browser must never fetch terminal code from a CDN.

Use an ES module adapter with a deliberately small Yeet-owned interface:

```js
terminal.open(element, profile)
terminal.write(bytes)
terminal.resize(cols, rows)
terminal.drain()
terminal.clear()
terminal.copyText()
terminal.dispose()
```

The adapter configures:

- `scrollback: 1000`;
- read-only input;
- no end-of-line conversion; and
- an xterm-compatible terminal mode appropriate for the PTY stream.

`write` accepts `Uint8Array` data. It queues and coalesces adjacent output up to
a bounded batch size before passing it to the emulator. Rendering is scheduled
at most once per animation frame while output is arriving rapidly. `drain`
resolves only when every accepted byte has passed through the emulator's write
callback, so terminal status cannot overtake output.

The copy button uses the emulator's selection and buffer APIs. The custom
line/cursor/ANSI parser and whole-`textContent` rerender path are removed.

Emulator initialization occurs before Deploy becomes available. If the
embedded module or WASM cannot initialize, the page reports the browser
compatibility error and does not start a deployment whose output it cannot
render.

Vendoring is deterministic: a repository tool records the exact package
version and upstream integrity, extracts only production artifacts, preserves
the upstream license, and can reproduce the committed assets. Tests verify
that the WASM file is embedded and served with a usable content type.

## Replay Journal

Each active deploy job owns an append-only temporary journal rather than a
bounded per-subscriber event queue. The journal stores terminal profiles,
output bytes, resize records, warnings, and final status in their original
order.

The file:

- is created before remote execution starts;
- is readable and writable only by the current user;
- uses framed records so arbitrary terminal bytes cannot corrupt framing;
- assigns each committed record a monotonically increasing SSE event ID;
- is capped at 64 MiB per job; and
- is deleted on retry, acknowledged success, server shutdown, or cancellation.

The SSE event ID is sufficient to resume reading at the next framed record.
The stream handler reads committed records from the journal at its own pace.
Live writers only append and send a coalesced wake-up notification, so a slow
subscriber never blocks deployment and never needs an event-sized channel.

Consecutive output records may be combined into one SSE output event when
streamed, provided the combined event retains the last included record ID and
does not cross a terminal-profile, resize, warning, or status record. This
reduces browser event pressure without changing the byte sequence.

A newly opened page starts at event zero and reconstructs terminal state from
the beginning of the active job. A reconnecting `EventSource` sends its
`Last-Event-ID` and receives only later records.

The 64 MiB cap is a safety boundary, not silent truncation. The journal reserves
space for one terminal warning before accepting the record that would cross
the cap. Browser output then stops with that visible warning rather than
presenting an incomplete transcript as exact. Direct terminal output and the
deployment continue unaffected. Status polling reports the eventual job result,
and the CLI's bounded completion fallback handles a success that the degraded
browser cannot acknowledge.

Journal creation failure prevents deployment from starting and is returned as
a normal web error. A journal write failure after deployment has started does
not cancel the deployment; it makes one best-effort degraded notification,
stops browser output, and preserves direct terminal output.

## SSE Lifecycle

The stream protocol gains these ordered event types:

```text
event: terminal
id: <record-id>
data: {"tty":true,"cols":120,"rows":40,"term":"xterm-256color","scrollback":1000}

event: output
id: <record-id>
data: {"encoding":"base64","chunk":"..."}

event: resize
id: <record-id>
data: {"cols":132,"rows":44}

event: warning
id: <record-id>
data: {"message":"..."}

event: status
id: <record-id>
data: {"state":"succeeded"}
```

The handler emits a short SSE retry interval before journal records. On a
transient `error`, the browser displays `Reconnecting` and leaves the same
`EventSource` open so native reconnect includes `Last-Event-ID`. It does not
unlock the form, clear the terminal, or infer deployment failure from stream
loss.

If the browser observes repeated connection failures, status polling may
update the connection message, but it cannot skip output replay or finalize a
job. The browser keeps reconnecting while the job is running and the local
server is available.

Duplicate event IDs are ignored defensively. A malformed payload or impossible
event sequence changes the browser terminal to an explicit degraded state; it
does not mutate the deployment or silently discard the rest of the stream.

## Completion Handshake

Successful remote execution appends the final status record but does not
immediately stop the local server.

The browser processes success in this order:

1. receive the ordered success status;
2. wait for the terminal adapter to drain all preceding writes;
3. update the terminal status to `Deployed`;
4. `POST /api/deploy/<job-id>/ack` with the existing CSRF protection; and
5. leave the rendered terminal available in the loaded page.

The acknowledgement is idempotent and valid only for the active succeeded job.
It closes the CLI completion channel once. The CLI then performs its normal
short graceful shutdown.

If the tab closes or never acknowledges, a bounded ten-second fallback after
success releases the CLI and shuts down the server. The session-closed beacon
may release a succeeded job earlier. This prevents an abandoned browser from
leaving `yeet run --web` waiting indefinitely while still allowing local
reconnect and replay to repair the common stream race.

Failed jobs never use the completion acknowledgement. Their journal remains
available until the user retries or exits, and a retry creates a fresh journal
and terminal session.

## Browser State and Recovery

The active job ID is stored in session state so a page refresh can query its
status and reconnect to its stream from event zero. Form values remain in the
existing browser state path.

The terminal sheet distinguishes:

- `Streaming`: connected and following live output;
- `Reconnecting`: deployment continues while SSE reconnects;
- `Degraded`: output durability failed and the terminal explains the gap;
- `Deployed`: final output drained and success acknowledged; and
- `Failed`: final output drained, fields unlocked, and retry available.

The output area remains read-only and selectable. Frequent terminal changes
are not announced through an ARIA live region; the concise connection and
completion status remains accessible without flooding assistive technology.

## Error Handling and Cleanup

Browser stream failure is never returned through the deploy writer and cannot
cancel Catch work. Direct terminal write errors keep their current behavior.

Server shutdown closes subscribers, stops terminal resize observation, closes
the journal, and removes its file. Cleanup is idempotent and runs for success,
retry, context cancellation, listener failure, and normal process exit.

Errors and warnings contain no terminal bytes, environment values, auth token,
CSRF token, or filesystem path to the journal. The journal is never exposed as
a static file or arbitrary download endpoint.

## Authorization Boundary

The acknowledgement endpoint is a local web-session lifecycle action. It uses
the existing loopback listener, session token, cookie, and CSRF checks. It
cannot start, stop, retry, or otherwise mutate Catch or a deployed service.

No new Catch operation exists, so the remote permission mapping remains
unchanged. Tests must prove that acknowledgement is method-restricted,
CSRF-protected, job-scoped, success-only, and idempotent.

## Testing

Go tests cover:

- byte-for-byte terminal and journal output;
- journal permissions, framing, replay, reconnect offsets, size cap, and
  cleanup;
- a deliberately slow subscriber receiving all output without blocking the
  writer;
- terminal profile and resize ordering;
- final status ordering after output;
- success acknowledgement and fallback shutdown;
- failed-job retry journal replacement;
- malformed or unauthorized acknowledgement requests; and
- journal failures degrading browser output without canceling a running job.

Browser tests use the existing Playwright web-run harness with the real
vendored emulator and WASM. They cover:

- carriage returns, cursor movement, erase sequences, SGR styling, split UTF-8,
  wide Unicode, and terminal wrapping;
- VM-style rapid spinner updates beyond the old 64-event subscriber limit;
- disconnect and native reconnect without duplicates or missing output;
- full page refresh while running and during the success-acknowledgement
  window;
- status waiting for the emulator write queue to drain;
- fixed PTY geometry through collapsed and expanded browser layouts;
- exactly 1,000 lines of scrollback;
- copy behavior; and
- explicit initialization and journal-degradation errors.

Verification includes targeted `pkg/yeet` tests, race-enabled web job tests, the
browser harness, the full Go suite, pre-commit, and the repository's meaningful
concurrency quality gate. Public web-run documentation is updated to describe
the read-only terminal mirror, reconnect behavior, and 1,000-line scrollback.

## Alternatives Considered

### Vendor xterm.js

xterm.js is mature and widely deployed, and its conventional scrollback
default is 1,000 lines. It is a viable fallback if `ghostty-web` proves
incompatible with Yeet's embedded build. The selected approach prefers
Ghostty's shared terminal parser and stronger cell/sequence fidelity.

### Keep the Custom Renderer

Increasing the subscriber queue and adding reconnect would reduce this specific
failure but would not make cursor, wrapping, Unicode, or ANSI behavior match a
real terminal. Continuing to extend a second terminal parser is outside Yeet's
core purpose.

### Replace SSE with WebSocket

WebSocket would still require ordered replay, backpressure isolation, durable
retention, and a completion barrier. It adds bidirectional protocol and
heartbeat behavior without helping this read-only use case.

### Keep Only an In-Memory Ring

A larger ring would likely hide current VM bursts but cannot reliably rebuild
terminal state after arbitrary cursor sequences or a page refresh once the
prefix is evicted. The bounded temporary journal provides stronger recovery
without retaining the transcript after the local Yeet process exits.
