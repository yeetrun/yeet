# Durable Web Terminal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `yeet run --web` render the same terminal byte stream and PTY geometry as the invoking terminal, with 1,000 lines of scrollback and lossless recovery from normal browser-stream backpressure, reconnects, and refreshes.

**Architecture:** Replace the in-memory subscriber queues with an append-only temporary event journal that SSE readers follow independently. Vendor Ghostty's browser terminal runtime into the embedded assets and feed it the ordered terminal, output, resize, warning, and status events. Successful jobs wait for the browser to drain and acknowledge the final render, with a bounded fallback so the CLI cannot hang.

**Tech Stack:** Go 1.26 via `mise`, `net/http` SSE, framed temporary journal files, `golang.org/x/term`, vanilla ES modules, `ghostty-web` 0.4.0, WebAssembly, Playwright.

## Global Constraints

- Pin `ghostty-web` at version `0.4.0` with npm integrity `sha512-0puDBik2qapbD/QQBW9o5ZHfXnZBqZWx/ctBiVtKZ6ZLds4NYb+wZuw1cRLXZk9zYovIQ908z3rvFhexAvc5Hg==`.
- Vendor all runtime assets and the MIT license; the browser must make no CDN or package-registry request.
- Configure exactly `scrollback: 1000`.
- Preserve terminal bytes byte-for-byte; do not normalize UTF-8, CRLF, ANSI, cursor, or erase sequences.
- Use the invoking TTY's rows, columns, and resize events; browser layout must not resize the remote PTY.
- Keep SSE as the read-only transport; do not add browser terminal input or a WebSocket.
- Cap each temporary replay journal at 64 MiB and reserve space for a visible degradation warning and final status.
- Wait up to ten seconds for successful browser rendering acknowledgement before the CLI completion fallback fires.
- Browser disconnection, rendering failure, journal exhaustion, or a slow reader must never cancel an already-running deployment.
- The acknowledgement is a local CSRF-protected web action and adds no Catch RPC, TTY command, or remote permission.
- Preserve unrelated GitButler branches and the existing uncommitted `.codex/skills/gitbutler` update; every checkpoint must select only files listed in its task.
- Do not push the GitButler branch or website submodule. Publication requires a separate explicit user request.

---

## File Map

- `tools/vendor-ghostty-web.sh`: reproducibly download, verify, slim, and copy the pinned browser runtime.
- `pkg/yeet/web_run_assets/ghostty-web.js`: vendored ES module with the inline WASM data URL replaced by the local WASM asset.
- `pkg/yeet/web_run_assets/ghostty-vt.wasm`: pinned Ghostty terminal parser.
- `pkg/yeet/web_run_assets/ghostty-web.LICENSE`: upstream MIT license.
- `pkg/yeet/web_run_assets/ghostty-web.manifest.json`: version, tarball, integrity, and deterministic transformation record.
- `pkg/yeet/web_run_assets/terminal.js`: Yeet-owned read-only terminal adapter and bounded browser write queue.
- `pkg/yeet/run_web_journal.go`: framed temporary journal, replay cursor, coalesced wake-up, cap, and cleanup.
- `pkg/yeet/run_web_journal_test.go`: journal framing, concurrency, cap, permissions, and cleanup tests.
- `pkg/yeet/run_web_job.go`: terminal profile, ordered event production, journal-following subscriptions, degradation, and job cleanup.
- `pkg/yeet/run_web_job_test.go`: byte fidelity, slow-reader, resize, replay, status, and degradation tests.
- `pkg/yeet/run_web_api.go`: job creation errors, SSE retry/replay, acknowledgement endpoint, and completion timer.
- `pkg/yeet/run_web_api_test.go`: HTTP stream, authorization, acknowledgement, retry, and cleanup tests.
- `pkg/yeet/run_web.go`: local TTY profile source and acknowledgement-driven server shutdown.
- `pkg/yeet/run_web_test.go`: end-to-end local server completion timing.
- `pkg/yeet/web_run_assets/app.js`: module imports, stream lifecycle, refresh recovery, render drain, acknowledgement, and copy behavior.
- `pkg/yeet/web_run_assets/index.html`: module script and terminal container markup.
- `pkg/yeet/web_run_assets/styles.css`: Ghostty canvas viewport, horizontal overflow, and accessible connection states.
- `pkg/yeet/web_run_assets_test.go`: embedded asset, module, content type, and static contract tests.
- `tools/web-run-terminal.spec.cjs`: real-asset Playwright server and emulator/reconnect/refresh regressions.
- `website/docs/operations/workflows.mdx`: evergreen guided-deploy terminal behavior.
- `website/docs/cli/yeet-cli.mdx`: `--web` scrollback and reconnect semantics.

---

### Task 1: Vendor the Ghostty browser runtime

**Files:**

- Create: `tools/vendor-ghostty-web.sh`
- Create: `pkg/yeet/web_run_assets/ghostty-web.js`
- Create: `pkg/yeet/web_run_assets/ghostty-vt.wasm`
- Create: `pkg/yeet/web_run_assets/ghostty-web.LICENSE`
- Create: `pkg/yeet/web_run_assets/ghostty-web.manifest.json`
- Modify: `pkg/yeet/web_run_assets_test.go`

**Interfaces:**

- Consumes: npm tarball `https://registry.npmjs.org/ghostty-web/-/ghostty-web-0.4.0.tgz`.
- Produces: local ES exports `Ghostty` and `Terminal`, plus `/ghostty-vt.wasm`.
- Produces: a rerunnable `tools/vendor-ghostty-web.sh` that fails on integrity drift or an unexpected upstream bundle shape.

- [ ] **Step 1: Add failing embedded-runtime tests**

Append tests that require every vendored file, validate the manifest, and reject the upstream bundle's inline WASM payload:

```go
func TestWebRunGhosttyAssetsEmbeddedAndPinned(t *testing.T) {
	for _, name := range []string{
		"ghostty-web.js",
		"ghostty-vt.wasm",
		"ghostty-web.LICENSE",
		"ghostty-web.manifest.json",
	} {
		b, err := fs.ReadFile(webRunAssets, "web_run_assets/"+name)
		if err != nil {
			t.Fatalf("read embedded %s: %v", name, err)
		}
		if len(b) == 0 {
			t.Fatalf("embedded %s is empty", name)
		}
	}

	manifest, err := fs.ReadFile(webRunAssets, "web_run_assets/ghostty-web.manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"version": "0.4.0"`,
		`"integrity": "sha512-0puDBik2qapbD/QQBW9o5ZHfXnZBqZWx/ctBiVtKZ6ZLds4NYb+wZuw1cRLXZk9zYovIQ908z3rvFhexAvc5Hg=="`,
	} {
		if !strings.Contains(string(manifest), want) {
			t.Fatalf("manifest missing %s", want)
		}
	}

	js, err := fs.ReadFile(webRunAssets, "web_run_assets/ghostty-web.js")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(js), "data:application/wasm;base64") {
		t.Fatal("vendored Ghostty JS still embeds the WASM payload")
	}
	if !strings.Contains(string(js), "./ghostty-vt.wasm") {
		t.Fatal("vendored Ghostty JS does not load the local WASM asset")
	}
}
```

- [ ] **Step 2: Run the focused test and verify it fails**

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestWebRunGhosttyAssetsEmbeddedAndPinned -count=1
```

Expected: FAIL because `ghostty-web.js` is not embedded.

- [ ] **Step 3: Add the deterministic vendoring tool**

Implement `tools/vendor-ghostty-web.sh` with these fixed values and checks:

```bash
#!/usr/bin/env bash
# Copyright (c) 2025 AUTHORS All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -euo pipefail

ghostty_version=0.4.0
ghostty_integrity='sha512-0puDBik2qapbD/QQBW9o5ZHfXnZBqZWx/ctBiVtKZ6ZLds4NYb+wZuw1cRLXZk9zYovIQ908z3rvFhexAvc5Hg=='
ghostty_url="https://registry.npmjs.org/ghostty-web/-/ghostty-web-${ghostty_version}.tgz"
ghostty_tmp=$(mktemp -d -t yeet-ghostty-web.XXXXXX)
trap 'rm -rf "$ghostty_tmp"' EXIT

curl --fail --location --silent --show-error "$ghostty_url" -o "$ghostty_tmp/package.tgz"
ghostty_actual="sha512-$(openssl dgst -sha512 -binary "$ghostty_tmp/package.tgz" | openssl base64 -A)"
test "$ghostty_actual" = "$ghostty_integrity"
tar -xzf "$ghostty_tmp/package.tgz" -C "$ghostty_tmp"

cp "$ghostty_tmp/package/dist/ghostty-vt.wasm" pkg/yeet/web_run_assets/ghostty-vt.wasm
cp "$ghostty_tmp/package/LICENSE" pkg/yeet/web_run_assets/ghostty-web.LICENSE

node - "$ghostty_tmp/package/dist/ghostty-web.js" pkg/yeet/web_run_assets/ghostty-web.js <<'NODE'
const fs = require("node:fs");
const [sourcePath, targetPath] = process.argv.slice(2);
const source = fs.readFileSync(sourcePath, "utf8");
const pattern = /new URL\("data:application\/wasm;base64,[A-Za-z0-9+/=]+", self\.location\)/g;
const matches = source.match(pattern) || [];
if (matches.length !== 1) throw new Error(`expected one inline WASM URL, found ${matches.length}`);
const output = source.replace(pattern, 'new URL("./ghostty-vt.wasm", self.location)');
fs.writeFileSync(targetPath, output);
NODE

node - pkg/yeet/web_run_assets/ghostty-web.manifest.json <<'NODE'
const fs = require("node:fs");
const targetPath = process.argv[2];
const manifest = {
  package: "ghostty-web",
  version: "0.4.0",
  tarball: "https://registry.npmjs.org/ghostty-web/-/ghostty-web-0.4.0.tgz",
  integrity: "sha512-0puDBik2qapbD/QQBW9o5ZHfXnZBqZWx/ctBiVtKZ6ZLds4NYb+wZuw1cRLXZk9zYovIQ908z3rvFhexAvc5Hg==",
  license: "MIT",
  inlineWasmReplacedWith: "./ghostty-vt.wasm",
};
fs.writeFileSync(targetPath, `${JSON.stringify(manifest, null, 2)}\n`);
NODE
```

Write the manifest with the pinned version, URL, integrity, MIT license, and the statement `inlineWasmReplacedWith: "./ghostty-vt.wasm"`. Do not copy README, declarations, UMD, or duplicate WASM files.

- [ ] **Step 4: Generate the assets and verify determinism**

Run the script twice, hashing the five outputs after each run:

```bash
tools/vendor-ghostty-web.sh
shasum -a 256 pkg/yeet/web_run_assets/ghostty-web.js pkg/yeet/web_run_assets/ghostty-vt.wasm pkg/yeet/web_run_assets/ghostty-web.LICENSE pkg/yeet/web_run_assets/ghostty-web.manifest.json
tools/vendor-ghostty-web.sh
shasum -a 256 pkg/yeet/web_run_assets/ghostty-web.js pkg/yeet/web_run_assets/ghostty-vt.wasm pkg/yeet/web_run_assets/ghostty-web.LICENSE pkg/yeet/web_run_assets/ghostty-web.manifest.json
```

Expected: both hash sets are identical and the JS no longer contains `data:application/wasm;base64`.

- [ ] **Step 5: Run embedded-asset tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestWebRun(GhosttyAssetsEmbeddedAndPinned|AssetsEmbedded)' -count=1
```

Expected: PASS.

- [ ] **Step 6: Checkpoint the vendored runtime**

Run `but status --format agent`, collect only the CLI change IDs for the six files in this task, and pass those IDs as repeated `--changes` arguments:

```bash
but commit codex/durable-web-terminal -m "web: vendor Ghostty terminal runtime"
```

Expected: one local commit containing no `.codex/skills/gitbutler` changes.

---

### Task 2: Build the framed replay journal

**Files:**

- Create: `pkg/yeet/run_web_journal.go`
- Create: `pkg/yeet/run_web_journal_test.go`

**Interfaces:**

- Produces:

```go
const defaultRunWebJournalLimit int64 = 64 << 20

var errRunWebJournalFull = errors.New("web terminal journal is full")

type runWebJournal struct { /* private state */ }

func newRunWebJournal(dir string, limit int64) (*runWebJournal, error)
func (j *runWebJournal) append(ev runWebStreamEvent, control bool) (int64, error)
func (j *runWebJournal) readAfter(cursor int64, maxOutputBytes int) ([]runWebStreamEvent, int64, <-chan struct{}, bool, error)
func (j *runWebJournal) close() error
```

- Produces the narrow injection seam used by job tests:

```go
type runWebEventJournal interface {
	append(runWebStreamEvent, bool) (int64, error)
	readAfter(int64, int) ([]runWebStreamEvent, int64, <-chan struct{}, bool, error)
	close() error
}
```

- Event IDs are end-of-frame byte offsets. A cursor of zero starts at the first frame.
- `readAfter` coalesces adjacent output records up to `maxOutputBytes`, never across control records, and returns `sealed=true` only after the final status is committed.

- [ ] **Step 1: Write framing, replay, and byte-fidelity tests**

Create table tests that append profile, arbitrary output, resize, and status events, then replay from zero and from the first output event ID. Include NUL, invalid UTF-8, CR, ESC, and split multibyte bytes:

```go
func TestRunWebJournalReplaysOrderedBinaryEvents(t *testing.T) {
	journal, err := newRunWebJournal(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.close() })

	profile := runWebStreamEvent{
		Type: runWebStreamTerminal,
		Profile: runWebTerminalProfile{
			TTY: true, Cols: 120, Rows: 40, Term: "xterm-256color", Scrollback: 1000,
		},
	}
	if _, err := journal.append(profile, true); err != nil {
		t.Fatal(err)
	}
	firstID, err := journal.append(runWebStreamEvent{
		Type: runWebStreamOutput,
		Chunk: []byte{0, '\r', 0x1b, '[', '2', 'K', 0xe2},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.append(runWebStreamEvent{
		Type: runWebStreamOutput,
		Chunk: []byte{0x9c, 0x94, '\n'},
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.append(runWebStreamEvent{
		Type: runWebStreamStatus, State: runWebJobSucceeded,
	}, true); err != nil {
		t.Fatal(err)
	}

	events, _, _, sealed, err := journal.readAfter(firstID, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	if !sealed || len(events) != 2 {
		t.Fatalf("events=%#v sealed=%v", events, sealed)
	}
	if got, want := events[0].Chunk, []byte{0x9c, 0x94, '\n'}; !bytes.Equal(got, want) {
		t.Fatalf("output=%x want=%x", got, want)
	}
}
```

- [ ] **Step 2: Run the journal test and verify it fails**

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestRunWebJournal -count=1
```

Expected: FAIL because the journal types do not exist.

- [ ] **Step 3: Implement append-only framing**

Use a fixed binary header and raw payload:

```go
const (
	runWebJournalHeaderSize     = 9
	runWebJournalControlReserve = 64 << 10
	runWebJournalReadBatch      = 64 << 10
	runWebStatusErrorLimit      = 32 << 10
)

// Header: one byte event type followed by an unsigned big-endian payload size.
type runWebJournal struct {
	mu        sync.Mutex
	file      *os.File
	path      string
	committed int64
	limit     int64
	wake      chan struct{}
	sealed    bool
	closed    bool
}
```

Encode output as raw bytes. Encode profile, resize, warning, and status payloads as JSON. Write the complete frame before advancing `committed`, then replace and close `wake` so any number of readers are notified without per-event queues.

Limit the status JSON error string to `runWebStatusErrorLimit` bytes with a
visible `…` suffix; the full error remains in the raw terminal stream. This
keeps the reserved control budget sufficient for the degradation warning and
final status.

Create files with `os.CreateTemp(dir, "yeet-web-run-*.journal")`, immediately enforce mode `0o600`, and remove the exact recorded path in idempotent `close`.

- [ ] **Step 4: Add slow-reader, cap, corruption, permission, and cleanup tests**

Cover these cases with explicit assertions:

- a writer appends 10,000 single-byte events while a reader waits and never reads; writes finish within one second;
- adjacent output records coalesce, but resize and status stay ordered boundaries;
- normal output cannot consume the 4 KiB control reserve;
- a control warning and status still fit after `errRunWebJournalFull`;
- a cursor outside `[0, committed]` returns `errRunWebJournalCursor`;
- truncated or invalid frames return `errRunWebJournalCorrupt`;
- journal mode is exactly `0o600`; and
- `close` twice succeeds and the path no longer exists.

- [ ] **Step 5: Run journal tests under the race detector**

Run:

```bash
mise exec -- go test -race ./pkg/yeet -run TestRunWebJournal -count=1
```

Expected: PASS with no race report.

- [ ] **Step 6: Checkpoint the replay journal**

Select only `pkg/yeet/run_web_journal.go` and `pkg/yeet/run_web_journal_test.go` in `but status --format agent`, then commit their CLI change IDs:

```bash
but commit codex/durable-web-terminal -m "web: add durable terminal replay journal"
```

---

### Task 3: Move web jobs onto the journal and record terminal geometry

**Files:**

- Modify: `pkg/yeet/run_web_job.go`
- Modify: `pkg/yeet/run_web_job_test.go`
- Modify: `pkg/yeet/run_web.go`
- Modify: `pkg/yeet/run_web_test.go`

**Interfaces:**

- Consumes: `runWebJournal` from Task 2 and the existing `watchResize`.
- Produces:

```go
const runWebTerminalScrollback = 1000

type runWebTerminalProfile struct {
	TTY        bool   `json:"tty"`
	Cols       int    `json:"cols"`
	Rows       int    `json:"rows"`
	Term       string `json:"term,omitempty"`
	Scrollback int    `json:"scrollback"`
}

const (
	runWebStreamTerminal runWebStreamType = "terminal"
	runWebStreamOutput   runWebStreamType = "output"
	runWebStreamResize   runWebStreamType = "resize"
	runWebStreamWarning  runWebStreamType = "warning"
	runWebStreamStatus   runWebStreamType = "status"
)

func currentRunWebTerminalProfile(fd int) runWebTerminalProfile
func newRunWebJob(id string, cfg runWebJobConfig) (*runWebJob, error)
func (j *runWebJob) subscribe(ctx context.Context, lastID int64) (<-chan runWebStreamEvent, <-chan struct{})
func (j *runWebJob) close() error
```

- `runWebJobConfig` gains `JournalDir`, `JournalLimit`, `Profile`, `Resize`, and
  `NewJournal func(string, int64) (runWebEventJournal, error)`. Production
  defaults `NewJournal` to `newRunWebJournal`; tests inject creation and
  append failures without corrupting a real file.
- `runWebStreamEvent` gains `Profile`, `Cols`, `Rows`, and `Warning`.

- [ ] **Step 1: Replace queue-behavior tests with durable-reader tests**

Delete tests that expect a subscriber to be disconnected when its 64-event channel fills. Add a test that does not consume the subscription until after hundreds of writes:

```go
func TestRunWebJobSlowSubscriberReplaysEveryByteWithoutBlockingWriter(t *testing.T) {
	var terminal bytes.Buffer
	job, err := newRunWebJob("job-a", runWebJobConfig{
		Stdout:    &terminal,
		JournalDir: t.TempDir(),
		Profile: runWebTerminalProfile{
			TTY: true, Cols: 100, Rows: 30, Term: "xterm-256color", Scrollback: 1000,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = job.close() })

	ch, done := job.subscribe(context.Background(), 0)
	for i := 0; i < 512; i++ {
		if _, err := job.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	job.finish(nil)

	var output []byte
	for ev := range ch {
		if ev.Type == runWebStreamOutput {
			output = append(output, ev.Chunk...)
		}
	}
	<-done
	if len(output) != 512 || terminal.Len() != 512 {
		t.Fatalf("browser=%d terminal=%d", len(output), terminal.Len())
	}
}
```

- [ ] **Step 2: Add profile and resize ordering tests**

Inject a resize channel, write output on both sides of a resize, close the channel, and assert this exact event sequence:

```text
terminal(120x40) -> output("before") -> resize(132x44) -> output("after") -> status
```

Also test `currentRunWebTerminalProfile` with the existing `isTerminalFn` and `termGetSizeFn` hooks:

- TTY: configured dimensions and `TERM`, scrollback 1000;
- non-TTY: `TTY=false`, stable `80x24`, scrollback 1000; and
- size lookup error: stable `80x24`.

- [ ] **Step 3: Run the job tests and verify failures**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunWeb(Job|TerminalProfile)' -count=1
```

Expected: FAIL on the new constructor, terminal event, and slow-reader expectations.

- [ ] **Step 4: Refactor job event production**

Implement these rules:

1. `newRunWebJob` creates the journal and appends the terminal profile before returning.
2. `Write` writes to `stdout` first, then journals exactly `p[:n]`.
3. A terminal short write returns `io.ErrShortWrite`.
4. Journal-full or journal-write errors set `degraded`, append one best-effort warning control event, and return `n, nil` so deployment continues.
5. Resize observation appends ordered resize control events until job completion or close.
6. `finish` appends the existing error line when absent, then exactly one final status control event and seals the journal.
7. `subscribe` follows `readAfter` and blocks only its own goroutine while delivering to a slow HTTP reader.
8. `close` stops resize observation and removes the journal exactly once.

Keep a bounded 64 KiB `outputTail` only for duplicate failure-line detection; do not decode or normalize it.

- [ ] **Step 5: Add degradation and cleanup tests**

Use a tiny `JournalLimit` to force the cap, then inject a journal whose
`append` returns a sentinel write error. Assert:

- terminal receives every byte;
- browser receives the warning before output stops;
- `status().Degraded` is true;
- job success remains success;
- the injected append error is not returned through `Write`;
- `close` removes the journal after failed retry and successful completion; and
- `ssePayload` serializes terminal, resize, warning, raw base64 output, and status fields exactly.

- [ ] **Step 6: Run focused and race tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunWeb(Job|TerminalProfile)' -count=1
mise exec -- go test -race ./pkg/yeet -run 'TestRunWeb(Job|Journal)' -count=1
```

Expected: PASS.

- [ ] **Step 7: Checkpoint journal-backed jobs**

Select only the four files in this task and commit their GitButler CLI IDs:

```bash
but commit codex/durable-web-terminal -m "web: replay terminal jobs without subscriber drops"
```

---

### Task 4: Add acknowledgement-driven completion and durable HTTP replay

**Files:**

- Modify: `pkg/yeet/run_web_api.go`
- Modify: `pkg/yeet/run_web_api_test.go`
- Modify: `pkg/yeet/run_web.go`
- Modify: `pkg/yeet/run_web_test.go`

**Interfaces:**

- Consumes: journal-backed `runWebJob`.
- Produces:

```text
POST /api/deploy/<job-id>/ack
```

- `runWebServerConfig` gains:

```go
CompletionAckTimeout time.Duration
TerminalProfile      func() runWebTerminalProfile
TerminalResize       func(context.Context) <-chan catchrpc.Resize
JournalDir           string
JournalLimit         int64
```

- `runWebJobStatus` gains `Degraded bool`.
- `runWebJob` gains:

```go
func (j *runWebJob) acknowledge() bool
func (j *runWebJob) acknowledged() <-chan struct{}
```

- `runWebServer` gains idempotent `close() error` for active-job journal
  cleanup.
- Successful completion calls `OnComplete` after an eligible acknowledgement, successful-tab close, or the ten-second fallback, exactly once.

- [ ] **Step 1: Write failing acknowledgement authorization tests**

Add table tests for:

```go
tests := []struct {
	name   string
	method string
	path   string
	want   int
}{
	{name: "unknown job", method: http.MethodPost, path: "/api/deploy/missing/ack", want: http.StatusNotFound},
	{name: "wrong method", method: http.MethodGet, path: "/api/deploy/1/ack", want: http.StatusMethodNotAllowed},
	{name: "running job", method: http.MethodPost, path: "/api/deploy/1/ack", want: http.StatusConflict},
	{name: "failed job", method: http.MethodPost, path: "/api/deploy/1/ack", want: http.StatusConflict},
	{name: "degraded success", method: http.MethodPost, path: "/api/deploy/1/ack", want: http.StatusConflict},
}
```

Add a cookie-authenticated POST without `X-Yeet-Run-CSRF` and expect `403`; add the header and expect `204`.

Also inject a `NewJournal` creation error into the server, assert `POST
/api/deploy` returns `500`, and prove the executor was never called.

- [ ] **Step 2: Write failing completion timing tests**

Replace the immediate `OnComplete` expectation with:

- successful job reaches `succeeded`, but `OnComplete` remains blocked for at least 50 ms;
- first eligible ack returns `204` and closes `OnComplete`;
- duplicate ack returns `204` and does not call `OnComplete` twice;
- a 25 ms injected fallback closes `OnComplete` without ack;
- `/api/session/closed` releases a succeeded non-degraded job;
- failed jobs never trigger the fallback; and
- status is not emitted before server state is marked complete.

- [ ] **Step 3: Run API tests and verify they fail**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunWebAPI.*(Ack|Complete|Fallback|SessionClosed|Stream)' -count=1
```

Expected: FAIL because `/ack` does not exist and success still completes immediately.

- [ ] **Step 4: Implement acknowledgement lifecycle**

In `runDeployJob`, mark the server single-use before `job.finish(nil)`, then start one completion waiter:

```go
func (s *runWebServer) awaitSuccessfulRender(job *runWebJob) {
	timer := time.NewTimer(s.completionAckTimeout())
	defer timer.Stop()
	select {
	case <-job.acknowledged():
	case <-timer.C:
	case <-s.ctx.Done():
		return
	}
	s.completeOnce.Do(func() {
		if s.cfg.OnComplete != nil {
			s.cfg.OnComplete()
		}
	})
}
```

Make `job.acknowledge()` idempotent and false for running, failed, or degraded jobs. Route `ack` beside `status` and `stream`. Return `204` only when success is eligible or was already acknowledged.

Normalize a nil configured context to `context.Background()` in
`newRunWebServer` and store it as `s.ctx`; completion waiters, deploy jobs, and
cleanup all use that non-nil context.

Keep `OnComplete` off the HTTP request goroutine so the 204 response can flush before server shutdown.

- [ ] **Step 5: Harden the SSE handler**

Before events, write and flush:

```text
retry: 250

```

Parse `Last-Event-ID` as a nonnegative journal offset. Return `400` for malformed values and `409` for impossible cursors. Stream journal events in order, with `Cache-Control: no-cache`, `X-Accel-Buffering: no`, and no subscriber queue.

- [ ] **Step 6: Wire the real terminal profile and cleanup**

At job start, derive the profile from `os.Stdin.Fd()` and create a separate `watchResize` channel for browser journal events. Inject functions in tests.

Ensure retry closes the failed job's journal before replacing it. Ensure server context cancellation, graceful shutdown, and hard close all invoke idempotent job cleanup.

- [ ] **Step 7: Run API, lifecycle, and race tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunWeb(API|Server|WaitRunWeb)' -count=1
mise exec -- go test -race ./pkg/yeet -run 'TestRunWeb(API|Job|Journal|Server)' -count=1
```

Expected: PASS.

- [ ] **Step 8: Checkpoint durable HTTP completion**

Select only the four task files and commit their GitButler CLI IDs:

```bash
but commit codex/durable-web-terminal -m "web: wait for terminal render acknowledgement"
```

---

### Task 5: Add the read-only Ghostty terminal adapter

**Files:**

- Create: `pkg/yeet/web_run_assets/terminal.js`
- Modify: `pkg/yeet/web_run_assets/index.html`
- Modify: `pkg/yeet/web_run_assets/styles.css`
- Modify: `pkg/yeet/web_run_assets_test.go`
- Modify: `tools/web-run-terminal.spec.cjs`

**Interfaces:**

- Consumes: `Ghostty` and `Terminal` from `./ghostty-web.js`.
- Produces:

```js
export function loadTerminalRuntime()
export async function createTerminalAdapter(element, profile)
```

- The returned adapter implements:

```js
write(bytes)
resize(cols, rows)
drain()
clear()
copyText()
dispose()
```

- [ ] **Step 1: Convert the Playwright harness to serve real modules**

Replace `page.setContent` in the harness with a loopback fixture server or Playwright routes that serve:

- `/` as the embedded `index.html` with the session script removed;
- `/app.js`, `/terminal.js`, and `/ghostty-web.js` as JavaScript;
- `/styles.css` as CSS;
- `/ghostty-vt.wasm` as `application/wasm`; and
- existing mocked JSON endpoints and `MockEventSource` through `page.addInitScript`.

Use `page.goto("http://yeet.test/")` so relative ES module and WASM URLs resolve normally.

- [ ] **Step 2: Add failing adapter fidelity and scrollback tests**

In Playwright, dynamically import `/terminal.js`, create an adapter in a fixed element, and write:

```js
const bytes = new TextEncoder().encode([
  "phase 1\\r",
  "\\x1b[2Kphase 2\\r\\n",
  "\\x1b[31mred\\x1b[0m ",
  "界 e\\u0301\\r\\n",
].join(""));
adapter.write(bytes);
await adapter.drain();
```

Assert `copyText()` contains `phase 2`, `red`, `界`, and the composed grapheme, but not the erased `phase 1`.

Write 1,101 numbered CRLF lines, drain, then assert `copyText()` contains line 1100 and does not contain line 0000. Resize to `132x44` and assert the adapter's reported geometry remains exact when its parent CSS width changes.

- [ ] **Step 3: Run Playwright and verify failure**

Run:

```bash
browser_test_root=$(mktemp -d -t yeet-playwright.XXXXXX)
npm install --prefix "$browser_test_root" @playwright/test@1.51.1
NODE_PATH="$browser_test_root/node_modules" "$browser_test_root/node_modules/.bin/playwright" test tools/web-run-terminal.spec.cjs
```

Expected: FAIL because `/terminal.js` is missing.

- [ ] **Step 4: Implement the terminal adapter**

Load the external WASM explicitly and cache the shared runtime:

```js
import { Ghostty, Terminal } from "./ghostty-web.js";

let runtimePromise;

export function loadTerminalRuntime() {
  if (!runtimePromise) {
    runtimePromise = Ghostty.load(new URL("./ghostty-vt.wasm", import.meta.url).href);
  }
  return runtimePromise;
}
```

Construct `Terminal` with:

```js
{
  ghostty,
  cols: profile.cols,
  rows: profile.rows,
  scrollback: 1000,
  disableStdin: true,
  convertEol: false,
  cursorBlink: false,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace",
}
```

The adapter owns a FIFO `Uint8Array` queue. Coalesce at most 64 KiB per terminal write, schedule at most one animation frame, and resolve `drain()` after the final `Terminal.write(batch, callback)` callback. Copy input bytes before queuing them.

Implement `copyText()` by returning the current selection when nonempty; otherwise call `selectAll()`, read `getSelection()`, then `clearSelection()`.

- [ ] **Step 5: Update markup and styles**

Change the app script to:

```html
<script type="module" src="/app.js"></script>
```

Replace the `<pre>` with a focusable terminal viewport:

```html
<div id="terminalOutput" class="terminal-output" tabindex="0" role="log" aria-label="Deploy output"></div>
```

Do not use `aria-live` for terminal bytes. Keep `terminalStatus` as the concise live status. Style the viewport for Ghostty's canvas, vertical scrollback, and horizontal overflow without fitting the grid.

- [ ] **Step 6: Add static asset contract tests**

Require the module script, terminal adapter imports, `scrollback: 1000`, `disableStdin: true`, no custom `handleCSI` parser, and WASM content type from `handleStatic`.

- [ ] **Step 7: Run adapter and asset tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestWebRunAssets -count=1
NODE_PATH="$browser_test_root/node_modules" "$browser_test_root/node_modules/.bin/playwright" test tools/web-run-terminal.spec.cjs
```

Expected: PASS.

- [ ] **Step 8: Checkpoint the terminal emulator**

Select only the five task files and commit their GitButler CLI IDs:

```bash
but commit codex/durable-web-terminal -m "web: render deploy output with Ghostty"
```

---

### Task 6: Make browser stream recovery and final rendering durable

**Files:**

- Modify: `pkg/yeet/web_run_assets/app.js`
- Modify: `pkg/yeet/web_run_assets/styles.css`
- Modify: `pkg/yeet/web_run_assets_test.go`
- Modify: `tools/web-run-terminal.spec.cjs`

**Interfaces:**

- Consumes: ordered `terminal`, `output`, `resize`, `warning`, and `status` SSE events.
- Consumes: terminal adapter from Task 5.
- Produces: `sessionStorage["yeet.run.activeJob"]` and idempotent `POST /api/deploy/<id>/ack`.

- [ ] **Step 1: Add failing reconnect and render-order browser tests**

Extend `MockEventSource` so tests can:

- dispatch 256 one-byte VM spinner/output events;
- dispatch `error` without closing the instance;
- later dispatch `open`, replay events with monotonically increasing `lastEventId`, and finish;
- delay the terminal write callback until the test releases it; and
- count `/ack` requests.

Assert:

- the form remains disabled and phase remains `deploying` during `error`;
- the same `EventSource` instance is retained;
- status reads `Reconnecting`, then returns to `Streaming`;
- no byte is duplicated after replay;
- `/ack` is absent before terminal drain and occurs exactly once afterward; and
- final status is `Deployed`.

- [ ] **Step 2: Add failing refresh and degraded-state tests**

After starting a job, reload the page while preserving session storage. Mock status as `running`, replay from terminal event ID zero, then finish. Assert reconstructed copy text matches the uninterrupted byte stream.

Dispatch a warning before success and assert:

- terminal status is `Degraded`;
- the warning remains visible;
- no acknowledgement is sent; and
- the form is not unlocked as though deployment failed.

- [ ] **Step 3: Run browser tests and verify failure**

Run:

```bash
NODE_PATH="$browser_test_root/node_modules" "$browser_test_root/node_modules/.bin/playwright" test tools/web-run-terminal.spec.cjs
```

Expected: FAIL because the current error handler closes the stream and the current status handler does not await rendering.

- [ ] **Step 4: Replace the custom renderer lifecycle**

Import:

```js
import { createTerminalAdapter, loadTerminalRuntime } from "./terminal.js";
```

Preload the runtime during bootstrap. Keep Deploy disabled and show a compatibility error if initialization fails.

On the first `terminal` event, validate `scrollback === 1000`, create the adapter, and open the sheet. Reject output before the terminal profile as a degraded sequence.

Pass decoded `Uint8Array` values directly to `terminal.write`. Apply resize events only through `terminal.resize(cols, rows)`.

- [ ] **Step 5: Preserve native EventSource recovery**

Remove `recoverDeployStream` behavior that closes the stream and unlocks the form. The `error` listener must only:

```js
if (state.phase === "deploying") {
  setTerminalStatus("Reconnecting", "warning");
}
```

Leave the same `EventSource` open so the browser sends `Last-Event-ID`. On `open`, restore `Streaming`. Track numeric event IDs and ignore duplicate or decreasing IDs defensively.

- [ ] **Step 6: Drain, acknowledge, and persist the active job**

After `POST /api/deploy` succeeds:

```js
sessionStorage.setItem("yeet.run.activeJob", jobId);
```

On final status:

```js
await state.terminal.drain();
if (status.state === "succeeded" && !state.terminalDegraded) {
  setTerminalStatus("Deployed", "done");
  await api(`/api/deploy/${encodeURIComponent(jobId)}/ack`, { method: "POST" });
}
```

Close the stream only after drain. Clear active-job session state after
acknowledged success. Keep a failed job ID until retry replaces it or the
session closes, so refreshing a failure can replay its terminal before
unlocking the form. On bootstrap, if a stored job exists and status returns
running or final, reopen the terminal sheet and stream from event zero so
refresh reconstructs state.

If acknowledgement fails, leave success visible, show that the local terminal session is finishing, and retry only while the local server remains reachable; the backend fallback remains authoritative.

- [ ] **Step 7: Restore copy, expand, and accessibility behavior**

Make Copy call `await navigator.clipboard.writeText(state.terminal.copyText())`. Preserve expand/collapse. Do not resize terminal columns when the sheet changes size. Keep connection and final state in `terminalStatus`, not the terminal canvas ARIA stream.

- [ ] **Step 8: Run browser and static tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestWebRunAssets -count=1
NODE_PATH="$browser_test_root/node_modules" "$browser_test_root/node_modules/.bin/playwright" test tools/web-run-terminal.spec.cjs
```

Expected: PASS.

- [ ] **Step 9: Checkpoint durable browser recovery**

Select only the four task files and commit their GitButler CLI IDs:

```bash
but commit codex/durable-web-terminal -m "web: reconnect and drain terminal streams"
```

---

### Task 7: Add end-to-end backpressure, refresh, and completion regressions

**Files:**

- Modify: `pkg/yeet/run_web_api_test.go`
- Modify: `pkg/yeet/run_web_test.go`
- Modify: `tools/web-run-terminal.spec.cjs`

**Interfaces:**

- Consumes: all backend and browser interfaces from Tasks 1-6.
- Produces: regression proof for the original VM disconnect and shutdown race.

- [ ] **Step 1: Add a real HTTP reconnect test**

Start the real loopback server with an executor that writes more than 64 rapid terminal chunks, pauses, then writes a unique tail and succeeds.

Open one stream, read an initial event ID, cancel the request, reconnect with `Last-Event-ID`, acknowledge after the final status, and assert the concatenated output equals the executor's bytes exactly once.

The test must also assert the writer completed while the first reader was paused.

- [ ] **Step 2: Add a shutdown barrier test**

Use a real HTTP client whose ack handler blocks before returning. Assert `waitRunWebServer` does not close the listener until the handler writes its `204`, then completes within the normal grace period.

Add the ten-second behavior through an injected 25 ms timeout rather than sleeping ten seconds.

- [ ] **Step 3: Add a VM control-sequence browser fixture**

Stream representative VM output:

```js
[
  "\\x1b[?25l",
  "⠋ Preparing VM image\\r",
  "\\x1b[2K⠙ Preparing VM image\\r",
  "\\x1b[2K✔ Prepare VM image\\r\\n",
  "  Downloaded 512 MiB / 512 MiB\\r\\n",
  "\\x1b[?25h",
]
```

Disconnect between spinner updates, reconnect, and finish. Assert copy text contains one final prepare line, one download line, no stale spinner frames, and `Deployed`.

- [ ] **Step 4: Run all focused web terminal tests repeatedly**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunWeb(API|Job|Journal|Server|TerminalProfile)|TestWaitRunWebServer' -count=10
mise exec -- go test -race ./pkg/yeet -run 'TestRunWeb(API|Job|Journal|Server)' -count=3
NODE_PATH="$browser_test_root/node_modules" "$browser_test_root/node_modules/.bin/playwright" test tools/web-run-terminal.spec.cjs --repeat-each=5
```

Expected: every repetition passes with no race report or flaky timeout.

- [ ] **Step 5: Checkpoint regression coverage**

Select only the three task files and commit their GitButler CLI IDs:

```bash
but commit codex/durable-web-terminal -m "test: cover durable web terminal recovery"
```

---

### Task 8: Update public docs and run repository gates

**Files:**

- Modify: `website/docs/operations/workflows.mdx`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: root `website` gitlink

**Interfaces:**

- Produces: evergreen public documentation; no changelog entry because no release was requested.

- [ ] **Step 1: Update evergreen documentation**

In the guided deploy workflow, replace the two-line terminal description with:

```markdown
Deploy progress appears in both the browser terminal and the local terminal.
The browser keeps 1,000 lines of scrollback and reconnects to an in-progress
deploy without restarting it. Runtime errors leave the form editable so you
can correct the inputs and retry.
```

In the `--web` flag description, state:

```markdown
- `--web`: open the local guided deploy form with a read-only, reconnecting
  terminal mirror and 1,000 lines of scrollback.
```

Do not add release, migration, or “new” language.

- [ ] **Step 2: Check website scope and content**

Run:

```bash
git -C website status --short --branch
git -C website diff --check
rg -n "private[-]host|/User[s]/" README.md website/docs .codex/skills
```

Expected: only the two intended website files differ, diff check passes, and no private path or host is introduced.

- [ ] **Step 3: Commit the website documentation locally**

Because GitButler resolves submodule commands to the parent workspace, use the allowed website-only raw Git path:

```bash
git -C website add docs/operations/workflows.mdx docs/cli/yeet-cli.mdx
git -C website commit -m "docs: describe durable web terminal"
```

Do not push. Record the website commit SHA for the handoff.

- [ ] **Step 4: Commit the root website pointer**

Verify:

```bash
git diff --submodule=log -- website
```

Then select only the `website` gitlink CLI ID in GitButler:

```bash
but commit codex/durable-web-terminal -m "docs: update web terminal guidance"
```

Do not use raw root Git and do not include `.codex/skills/gitbutler`.

- [ ] **Step 5: Run formatting and targeted verification**

Run:

```bash
git diff --check
mise exec -- gofmt -w pkg/yeet/run_web_journal.go pkg/yeet/run_web_journal_test.go pkg/yeet/run_web_job.go pkg/yeet/run_web_job_test.go pkg/yeet/run_web_api.go pkg/yeet/run_web_api_test.go pkg/yeet/run_web.go pkg/yeet/run_web_test.go pkg/yeet/web_run_assets_test.go
mise exec -- go test ./pkg/yeet -count=1
mise exec -- go test -race ./pkg/yeet -run 'TestRunWeb(API|Job|Journal|Server)' -count=3
NODE_PATH="$browser_test_root/node_modules" "$browser_test_root/node_modules/.bin/playwright" test tools/web-run-terminal.spec.cjs
```

Expected: PASS.

- [ ] **Step 6: Run repository-wide verification**

Run:

```bash
mise exec -- go test ./... -count=1
pre-commit run --all-files
mise run quality
mise run quality:goal
```

Expected: every gate passes. If `govulncheck` still resolves `golang.org/x/text v0.38.0` from another applied GitButler branch, verify `origin/main` remains on `v0.39.0`, report that unrelated composite-workspace failure, and do not modify or commit the other branch.

- [ ] **Step 7: Inspect final branch scope**

Run:

```bash
but diff codex/durable-web-terminal --format agent
but status
git -C website status --short --branch
```

Expected:

- the durable-terminal branch contains only this design, plan, implementation, tests, vendored assets, docs pointer, and no other active branch changes;
- website is clean but locally ahead by the documentation commit;
- no branch or submodule was pushed; and
- all temporary journal files created by tests were removed.

- [ ] **Step 8: Create a final local checkpoint only if verification edits remain**

If formatting or verification produced changes in the task files, use `but status --format agent` and commit only those exact CLI IDs:

```bash
but commit codex/durable-web-terminal -m "web: finish durable terminal support"
```

If there are no changes, do not create an empty commit.
