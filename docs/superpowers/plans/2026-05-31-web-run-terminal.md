# Web Run Terminal Stream Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `yeet run --web` show read-only deployment output in the browser, keep failures editable/retryable, and shut down only after a successful deploy or terminal interrupt.

**Architecture:** Keep the existing `RunDraft` deploy path as the source of truth, but add an output-writer seam so web deploy can mirror output to both the terminal and a local deploy job. Replace the blocking `/api/deploy` response with a background job plus authenticated SSE stream. Add a compact bottom-sheet terminal in the embedded web assets that follows the job stream and preserves form state on failure.

**Tech Stack:** Go `net/http`, embedded HTML/CSS/JS, Server-Sent Events, existing yeet `RunDraft`, existing catch RPC exec WebSocket client.

---

## File Structure

- Modify `pkg/yeet/run_draft.go`
  - Add `runDraftExecuteOptions` and `executeRunDraftWithOptions`.
  - Route run-change status and remote deploy output through a provided writer.
- Modify `pkg/yeet/exec_remote.go`
  - Add `execRemoteTo` so callers can choose the stdout writer.
  - Keep `execRemote` and `execRemoteFn` compatible for existing call sites.
- Modify `pkg/yeet/svc_cmd.go`
  - Add output-aware run helpers used by web deploy: `runRunContextWithOutput`, `runFilePayloadContextWithOutput`, `tryRunDockerContextWithOutput`, and related helpers.
  - Leave normal CLI helpers delegating to the output-aware versions with `os.Stdout`.
- Create `pkg/yeet/run_web_job.go`
  - Own local deploy job state, output replay buffer, subscribers, fan-out writer, status, and browser-close hint.
- Create `pkg/yeet/run_web_job_test.go`
  - Unit-test job buffering, replay, final status, non-blocking subscribers, and close-tab notices.
- Modify `pkg/yeet/run_web_api.go`
  - Convert `/api/deploy` into a job-start endpoint.
  - Add `/api/deploy/<job-id>/stream`, `/api/deploy/<job-id>/status`, and `/api/session/closed`.
- Modify `pkg/yeet/run_web_api_test.go`
  - Cover job start, SSE replay/live output, runtime failure retry state, success completion, and tab close notice.
- Modify `pkg/yeet/run_web.go`
  - Pass the terminal output writer into the web server config.
  - Keep `waitRunWebServer` completion behavior tied to successful deploy.
- Modify `pkg/yeet/web_run_assets/index.html`
  - Add the bottom-sheet terminal markup.
- Modify `pkg/yeet/web_run_assets/styles.css`
  - Style the compact terminal sheet, expanded state, status tones, and copy/expand controls.
- Modify `pkg/yeet/web_run_assets/app.js`
  - Start deploy jobs, follow SSE streams, render output, send tab-close notification, unlock the form on runtime failure, and replace the native host datalist with a custom on-theme host picker.
- Modify `pkg/yeet/web_run_assets_test.go`
  - Assert the embedded assets expose terminal UI and stream behavior hooks.
- Modify website docs after implementation:
  - `website/docs/cli/yeet-cli.mdx`
  - `website/docs/operations/workflows.mdx`
  - Refresh `website/public/images/web-run-deploy.png` if the homepage/manual screenshot shows the deploy flow.

## Task 1: Add Output-Aware Remote Exec

**Files:**
- Modify: `pkg/yeet/exec_remote.go`
- Test: `pkg/yeet/exec_remote_test.go`

- [ ] **Step 1: Write failing test for `execRemoteTo` using a provided writer**

Add this test to `pkg/yeet/exec_remote_test.go`:

```go
func TestExecRemoteToWritesClientOutputToProvidedWriter(t *testing.T) {
	oldClient := newRPCExecClientFn
	oldHost := hostOverride
	oldHostSet := hostOverrideSet
	defer func() {
		newRPCExecClientFn = oldClient
		hostOverride = oldHost
		hostOverrideSet = oldHostSet
	}()

	hostOverride = "host-a"
	hostOverrideSet = true
	newRPCExecClientFn = func(host string) rpcExecClient {
		if host != "host-a" {
			t.Fatalf("host = %q, want host-a", host)
		}
		return rpcExecClientFunc(func(ctx context.Context, req catchrpc.ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan catchrpc.Resize) (int, error) {
			if req.Service != "svc-a" {
				t.Fatalf("service = %q, want svc-a", req.Service)
			}
			if _, err := stdout.Write([]byte("remote output\n")); err != nil {
				t.Fatalf("write stdout: %v", err)
			}
			return 0, nil
		})
	}

	var out bytes.Buffer
	if err := execRemoteTo(context.Background(), "svc-a", []string{"status"}, nil, false, &out); err != nil {
		t.Fatalf("execRemoteTo: %v", err)
	}
	if got := out.String(); got != "remote output\n" {
		t.Fatalf("output = %q, want remote output", got)
	}
}

type rpcExecClientFunc func(context.Context, catchrpc.ExecRequest, io.Reader, io.Writer, <-chan catchrpc.Resize) (int, error)

func (f rpcExecClientFunc) Exec(ctx context.Context, req catchrpc.ExecRequest, stdin io.Reader, stdout io.Writer, resizeCh <-chan catchrpc.Resize) (int, error) {
	return f(ctx, req, stdin, stdout, resizeCh)
}

func (f rpcExecClientFunc) Events(context.Context, catchrpc.EventsRequest, func(catchrpc.Event)) error {
	return errors.New("events not implemented in test")
}
```

- [ ] **Step 2: Run the focused test and verify it fails**

Run:

```bash
go test ./pkg/yeet -run TestExecRemoteToWritesClientOutputToProvidedWriter -count=1
```

Expected: fail with `undefined: execRemoteTo`.

- [ ] **Step 3: Implement `execRemoteTo`**

In `pkg/yeet/exec_remote.go`, replace `execRemote` with a delegating wrapper and add `execRemoteTo`:

```go
func execRemote(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
	return execRemoteTo(ctx, service, args, stdin, tty, os.Stdout)
}

func execRemoteTo(ctx context.Context, service string, args []string, stdin io.Reader, tty bool, stdout io.Writer) (err error) {
	if stdout == nil {
		stdout = io.Discard
	}
	host := Host()
	client := newRPCExecClientFn(host)
	session, err := prepareRemoteExecSession(ctx, host, service, args, stdin, tty)
	if err != nil {
		return err
	}
	defer session.close(&err)

	out := &trackingWriter{w: stdout}
	code, err := client.Exec(ctx, session.req, session.stdin, out, session.resizeCh)
	if err != nil {
		return err
	}
	return remoteExecExitError(code, session.rawMode, out)
}
```

- [ ] **Step 4: Run the focused test and verify it passes**

Run:

```bash
go test ./pkg/yeet -run TestExecRemoteToWritesClientOutputToProvidedWriter -count=1
```

Expected: pass.

- [ ] **Step 5: Run existing remote exec tests**

Run:

```bash
go test ./pkg/yeet -run 'TestExecRemote|TestRemoteExec|TestErrorPrefix' -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add pkg/yeet/exec_remote.go pkg/yeet/exec_remote_test.go
git commit -m "yeet: allow remote exec output routing"
```

## Task 2: Add Output Seam To RunDraft Execution

**Files:**
- Modify: `pkg/yeet/run_draft.go`
- Modify: `pkg/yeet/svc_cmd.go`
- Test: `pkg/yeet/run_draft_test.go`

- [ ] **Step 1: Write failing test proving `executeRunDraftWithOptions` captures remote output**

Add this test to `pkg/yeet/run_draft_test.go`:

```go
func TestExecuteRunDraftWithOptionsWritesDeployOutputToProvidedWriter(t *testing.T) {
	preserveRunDraftGlobals(t)
	oldExecTo := execRemoteToFn
	oldArch := remoteCatchOSAndArchFn
	defer func() {
		execRemoteToFn = oldExecTo
		remoteCatchOSAndArchFn = oldArch
	}()

	root := t.TempDir()
	payload := filepath.Join(root, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	cfgLoc := &projectConfigLocation{Dir: root, Config: &ProjectConfig{Version: projectConfigVersion}}

	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return runtime.GOOS, runtime.GOARCH, nil
	}
	execRemoteToFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool, stdout io.Writer) error {
		if _, err := stdout.Write([]byte("installing service\n")); err != nil {
			t.Fatalf("write stdout: %v", err)
		}
		return nil
	}

	var out bytes.Buffer
	draft := RunDraft{Service: "svc-a", Host: "host-a", Payload: payload}
	err := executeRunDraftWithOptions(context.Background(), draft, cfgLoc, runDraftExecuteOptions{Stdout: &out})
	if err != nil {
		t.Fatalf("executeRunDraftWithOptions: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "installing service") {
		t.Fatalf("output = %q, want remote output", got)
	}
}
```

Add `bytes` and `runtime` to the `pkg/yeet/run_draft_test.go` imports.

- [ ] **Step 2: Run the focused test and verify it fails**

Run:

```bash
go test ./pkg/yeet -run TestExecuteRunDraftWithOptionsWritesDeployOutputToProvidedWriter -count=1
```

Expected: fail with undefined `execRemoteToFn`, `executeRunDraftWithOptions`, or `runDraftExecuteOptions`.

- [ ] **Step 3: Add `execRemoteToFn`**

In `pkg/yeet/exec_remote.go`, add this variable near the existing exec function variables:

```go
var execRemoteToFn = execRemoteTo
```

- [ ] **Step 4: Add `runDraftExecuteOptions` and delegating executor**

In `pkg/yeet/run_draft.go`, replace `executeRunDraft` with:

```go
type runDraftExecuteOptions struct {
	Stdout      io.Writer
	ForceDeploy bool
}

var executeRunDraftFn = executeRunDraft
var executeRunDraftWithOptionsFn = executeRunDraftWithOptions

func executeRunDraft(ctx context.Context, draft RunDraft, cfgLoc *projectConfigLocation, forceDeploy bool) error {
	return executeRunDraftWithOptions(ctx, draft, cfgLoc, runDraftExecuteOptions{Stdout: os.Stdout, ForceDeploy: forceDeploy})
}

func executeRunDraftWithOptions(ctx context.Context, draft RunDraft, cfgLoc *projectConfigLocation, opts runDraftExecuteOptions) error {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	normalized, validation := validateRunDraft(ctx, draft, cwd)
	if !validation.OK {
		return fmt.Errorf("invalid run draft: %s", validation.Errors[0].Message)
	}
	draft = normalized

	service := strings.TrimSpace(draft.Service)
	if service == "" {
		return fmt.Errorf("service name is required")
	}

	prevService := serviceOverride
	prevHost := hostOverride
	prevHostSet := hostOverrideSet
	prevPrefs := loadedPrefs
	serviceOverride = service
	host := strings.TrimSpace(draft.Host)
	if host != "" {
		hostOverride = host
		hostOverrideSet = true
		loadedPrefs.DefaultHost = host
	}
	defer func() {
		serviceOverride = prevService
		hostOverride = prevHost
		hostOverrideSet = prevHostSet
		loadedPrefs = prevPrefs
	}()

	runArgs := draft.runArgs()
	if err := runDraftWithChangesTo(ctx, opts.Stdout, draft, runArgs, opts.ForceDeploy || draft.ForceDeploy || draft.SnapshotChange); err != nil {
		return err
	}
	configRunArgs := runArgsWithoutSensitiveRunOptions(runArgs)
	if err := saveRunConfigWithPayloadKind(cfgLoc, host, draft.Payload, draft.PayloadKind, configRunArgs, draft.Storage.ServiceRoot, draft.Storage.ZFS); err != nil {
		return err
	}
	if draft.EnvFileSet {
		return saveEnvFileConfig(cfgLoc, host, draft.EnvFileArg)
	}
	return nil
}
```

- [ ] **Step 5: Add output-aware run helpers**

In `pkg/yeet/run_draft.go`, replace `runDraftWithChanges` with:

```go
func runDraftWithChanges(ctx context.Context, draft RunDraft, runArgs []string, forceDeploy bool) error {
	return runDraftWithChangesTo(ctx, os.Stdout, draft, runArgs, forceDeploy)
}

func runDraftWithChangesTo(ctx context.Context, stdout io.Writer, draft RunDraft, runArgs []string, forceDeploy bool) error {
	if stdout == nil {
		stdout = io.Discard
	}
	runner := func(ctx context.Context, payload string, args []string) error {
		return runRunContextWithOutput(ctx, stdout, payload, args)
	}
	if draft.PayloadKind == "local-image" {
		runner = func(ctx context.Context, payload string, args []string) error {
			return runLocalImagePayloadContextWithOutput(ctx, stdout, payload, args)
		}
	}
	return runWithChangesToWithContextRunner(ctx, stdout, draft.Payload, runArgs, draft.EnvFile, draft.ExistingEntry, forceDeploy, runner, draft.PayloadKind == "local-image")
}
```

In `pkg/yeet/svc_cmd.go`, add output-aware helper variants and keep existing helpers delegating:

```go
func runRunContextWithOutput(ctx context.Context, stdout io.Writer, payload string, args []string) error {
	if ok, err := tryRunDockerfileContextWithOutput(ctx, stdout, payload, args); err != nil {
		return err
	} else if ok {
		return nil
	}
	if ok, err := tryRunFileContextWithOutput(ctx, stdout, payload, args); err != nil {
		return err
	} else if ok {
		return nil
	}
	if ok, err := tryRunRemoteImageContextWithOutput(ctx, stdout, payload, args); err != nil {
		return err
	} else if ok {
		return nil
	}
	if ok, err := tryRunDockerContextWithOutput(ctx, stdout, payload, args); err != nil {
		return err
	} else if ok {
		return nil
	}
	return fmt.Errorf("unknown payload: %s", payload)
}

func tryRunDockerfileContextWithOutput(ctx context.Context, stdout io.Writer, path string, args []string) (bool, error) {
	if filepath.Base(path) != "Dockerfile" {
		return false, nil
	}
	if st, err := os.Stat(path); os.IsNotExist(err) || st != nil && st.IsDir() {
		return false, fmt.Errorf("dockerfile payload does not exist: %s", path)
	} else if err != nil {
		return false, err
	}
	svc := getService()
	tag := fmt.Sprintf("yeet-build-%d", time.Now().UnixNano())
	imageName := fmt.Sprintf("%s:%s", svc, tag)
	if err := buildDockerImageForRemoteFn(ctx, path, imageName); err != nil {
		return true, err
	}
	ok, err := tryRunDockerContextWithOutput(ctx, stdout, imageName, args)
	_ = removeDockerImageFn(ctx, imageName)
	return ok, err
}

func tryRunFileContextWithOutput(ctx context.Context, stdout io.Writer, file string, args []string) (bool, error) {
	if st, err := os.Stat(file); os.IsNotExist(err) || st != nil && st.IsDir() {
		if st != nil && st.IsDir() {
			fmt.Fprintf(os.Stderr, "%q is a directory, ignoring\n", file)
		}
		return false, nil
	} else if err != nil {
		return false, err
	}
	return runFilePayloadContextWithOutput(ctx, stdout, file, args, true)
}

func tryRunRemoteImageContextWithOutput(ctx context.Context, stdout io.Writer, image string, args []string) (bool, error) {
	if !looksLikeImageRef(image) {
		return false, nil
	}
	svc := getService()
	tmpDir, err := os.MkdirTemp("", "yeet-image-")
	if err != nil {
		return true, err
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to remove temporary directory %s: %v\n", tmpDir, err)
		}
	}()
	composePath := filepath.Join(tmpDir, "compose.yml")
	content := fmt.Sprintf(imageComposeTemplate, svc, image)
	if err := os.WriteFile(composePath, []byte(content), 0o644); err != nil {
		return true, err
	}
	return runFilePayloadContextWithOutput(ctx, stdout, composePath, args, false)
}

func runFilePayloadContextWithOutput(ctx context.Context, stdout io.Writer, file string, args []string, pushLocalImages bool) (bool, error) {
	upload, err := prepareRunFileUpload(file, args, pushLocalImages)
	if err != nil {
		return false, err
	}
	defer upload.cleanup()

	svc := getService()
	if err := pushRunFileLocalImages(ctx, svc, upload, pushLocalImages); err != nil {
		return false, err
	}
	if err := execRunFilePayloadWithOutput(ctx, stdout, svc, upload.payload, args); err != nil {
		return false, err
	}
	return true, nil
}

func execRunFilePayloadWithOutput(ctx context.Context, stdout io.Writer, svc string, payload io.Reader, args []string) error {
	runArgs := append([]string{"run"}, args...)
	tty := isTerminalFn(int(os.Stdout.Fd()))
	if err := execRemoteToFn(ctx, svc, runArgs, payload, tty, stdout); err != nil {
		return fmt.Errorf("failed to run service: %w", err)
	}
	return nil
}

func tryRunDockerContextWithOutput(ctx context.Context, stdout io.Writer, image string, args []string) (bool, error) {
	if !imageExistsFn(ctx, image) {
		return false, nil
	}
	svc := getService()
	if err := pushImageFn(ctx, svc, image, "latest"); err != nil {
		return false, fmt.Errorf("failed to push image: %w", err)
	}
	if err := stageDockerArgsWithOutput(ctx, stdout, svc, args); err != nil {
		return false, err
	}
	if err := commitDockerStageWithOutput(ctx, stdout, svc); err != nil {
		return false, err
	}
	return true, nil
}

func stageDockerArgsWithOutput(ctx context.Context, stdout io.Writer, svc string, args []string) error {
	if len(args) == 0 {
		return nil
	}
	stageArgs := append([]string{"stage"}, args...)
	if err := execRemoteToFn(ctx, svc, stageArgs, nil, true, stdout); err != nil {
		fmt.Fprintln(stdout, "failed to stage args:", err)
		return fmt.Errorf("failed to stage args: %w", err)
	}
	return nil
}

func commitDockerStageWithOutput(ctx context.Context, stdout io.Writer, svc string) error {
	if err := execRemoteToFn(ctx, svc, []string{"stage", "commit"}, nil, true, stdout); err != nil {
		return errors.New("failed to run service")
	}
	return nil
}
```

- [ ] **Step 6: Update existing helper bodies to delegate**

Change existing helper bodies in `pkg/yeet/svc_cmd.go`:

```go
func runRunContext(ctx context.Context, payload string, args []string) error {
	return runRunContextWithOutput(ctx, os.Stdout, payload, args)
}

func tryRunDockerfileContext(ctx context.Context, path string, args []string) (ok bool, _ error) {
	return tryRunDockerfileContextWithOutput(ctx, os.Stdout, path, args)
}

func tryRunRemoteImageContext(ctx context.Context, image string, args []string) (ok bool, _ error) {
	return tryRunRemoteImageContextWithOutput(ctx, os.Stdout, image, args)
}

func tryRunFileContext(ctx context.Context, file string, args []string) (ok bool, _ error) {
	return tryRunFileContextWithOutput(ctx, os.Stdout, file, args)
}

func runFilePayloadContext(ctx context.Context, file string, args []string, pushLocalImages bool) (ok bool, _ error) {
	return runFilePayloadContextWithOutput(ctx, os.Stdout, file, args, pushLocalImages)
}

func execRunFilePayload(ctx context.Context, svc string, payload io.Reader, args []string) error {
	return execRunFilePayloadWithOutput(ctx, os.Stdout, svc, payload, args)
}

func tryRunDockerContext(ctx context.Context, image string, args []string) (ok bool, _ error) {
	return tryRunDockerContextWithOutput(ctx, os.Stdout, image, args)
}

func stageDockerArgs(ctx context.Context, svc string, args []string) error {
	return stageDockerArgsWithOutput(ctx, os.Stdout, svc, args)
}

func commitDockerStage(ctx context.Context, svc string) error {
	return commitDockerStageWithOutput(ctx, os.Stdout, svc)
}
```

- [ ] **Step 7: Add local image output helper**

In `pkg/yeet/run_draft.go`, update local image helpers:

```go
func runLocalImagePayloadContext(ctx context.Context, payload string, args []string) error {
	return runLocalImagePayloadContextWithOutput(ctx, os.Stdout, payload, args)
}

func runLocalImagePayloadContextWithOutput(ctx context.Context, stdout io.Writer, payload string, args []string) error {
	if ok, err := tryRunDockerContextWithOutput(ctx, stdout, payload, args); err != nil {
		return err
	} else if ok {
		return nil
	}
	return fmt.Errorf("unknown local Docker image: %s", payload)
}
```

- [ ] **Step 8: Run focused draft tests**

Run:

```bash
go test ./pkg/yeet -run 'TestExecuteRunDraftWithOptionsWritesDeployOutputToProvidedWriter|TestExecuteRunDraft' -count=1
```

Expected: pass.

- [ ] **Step 9: Run broader run path tests**

Run:

```bash
go test ./pkg/yeet -run 'TestRunDraft|TestRunWithChanges|TestRunFile|TestTryRun|TestHandleSvcRun' -count=1
```

Expected: pass.

- [ ] **Step 10: Commit**

```bash
git add pkg/yeet/run_draft.go pkg/yeet/run_draft_test.go pkg/yeet/svc_cmd.go
git commit -m "yeet: route run draft output"
```

## Task 3: Add Deploy Job And Replay Buffer

**Files:**
- Create: `pkg/yeet/run_web_job.go`
- Create: `pkg/yeet/run_web_job_test.go`

- [ ] **Step 1: Write tests for job output, replay, and final status**

Create `pkg/yeet/run_web_job_test.go`:

```go
package yeet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestRunWebJobWritesTerminalAndReplaysOutput(t *testing.T) {
	var terminal bytes.Buffer
	job := newRunWebJob("job-1", runWebJobConfig{Stdout: &terminal, BufferLimit: 1024})

	if _, err := job.Write([]byte("line one\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	job.finish(nil)

	events, done := job.subscribe(context.Background(), 0)
	var got []runWebStreamEvent
	for ev := range events {
		got = append(got, ev)
	}
	<-done

	if terminal.String() != "line one\n" {
		t.Fatalf("terminal = %q, want line one", terminal.String())
	}
	if len(got) != 2 {
		t.Fatalf("events = %#v, want output and status", got)
	}
	if got[0].Type != runWebStreamOutput || string(got[0].Chunk) != "line one\n" {
		t.Fatalf("first event = %#v, want output", got[0])
	}
	if got[1].Type != runWebStreamStatus || got[1].State != runWebJobSucceeded {
		t.Fatalf("last event = %#v, want succeeded status", got[1])
	}
}

func TestRunWebJobFailureReturnsReadyStateData(t *testing.T) {
	job := newRunWebJob("job-1", runWebJobConfig{Stdout: io.Discard, BufferLimit: 1024})

	job.finish(errors.New("deploy failed"))
	status := job.status()

	if status.State != runWebJobFailed {
		t.Fatalf("state = %q, want failed", status.State)
	}
	if !strings.Contains(status.Error, "deploy failed") {
		t.Fatalf("error = %q, want deploy failed", status.Error)
	}
}

func TestRunWebJobBufferLimitAddsOmissionEvent(t *testing.T) {
	job := newRunWebJob("job-1", runWebJobConfig{Stdout: io.Discard, BufferLimit: 16})

	_, _ = job.Write([]byte("first line\n"))
	_, _ = job.Write([]byte("second line\n"))
	job.finish(nil)

	events, done := job.subscribe(context.Background(), 0)
	var transcript string
	for ev := range events {
		if ev.Type == runWebStreamOutput {
			transcript += string(ev.Chunk)
		}
	}
	<-done

	if !strings.Contains(transcript, "[older output omitted]\n") {
		t.Fatalf("transcript = %q, want omission note", transcript)
	}
	if !strings.Contains(transcript, "second line\n") {
		t.Fatalf("transcript = %q, want retained newest output", transcript)
	}
}

func TestRunWebJobSubscriberDoesNotBlockWriter(t *testing.T) {
	job := newRunWebJob("job-1", runWebJobConfig{Stdout: io.Discard, BufferLimit: 1024, SubscriberBuffer: 1})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, done := job.subscribe(ctx, 0)
	_, _ = events, done

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for i := 0; i < 64; i++ {
			_, _ = job.Write([]byte("x"))
		}
	}()

	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Write blocked on slow subscriber")
	}
}

func TestRunWebJobTabCloseNoticePrintsOnce(t *testing.T) {
	var notices bytes.Buffer
	job := newRunWebJob("job-1", runWebJobConfig{Stdout: io.Discard, Notice: &notices, BufferLimit: 1024})

	job.browserClosed()
	job.browserClosed()

	if got := notices.String(); got != "Browser tab closed. Press Ctrl-C to quit.\n" {
		t.Fatalf("notice = %q, want one close notice", got)
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```bash
go test ./pkg/yeet -run 'TestRunWebJob' -count=1
```

Expected: fail with undefined job types/functions.

- [ ] **Step 3: Implement `run_web_job.go`**

Create `pkg/yeet/run_web_job.go` with these public-to-package shapes:

```go
package yeet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

type runWebJobState string

const (
	runWebJobRunning   runWebJobState = "running"
	runWebJobSucceeded runWebJobState = "succeeded"
	runWebJobFailed    runWebJobState = "failed"
)

type runWebStreamType string

const (
	runWebStreamOutput runWebStreamType = "output"
	runWebStreamStatus runWebStreamType = "status"
)

type runWebJobConfig struct {
	Stdout           io.Writer
	Notice           io.Writer
	BufferLimit      int
	SubscriberBuffer int
}

type runWebStreamEvent struct {
	ID    int64
	Type  runWebStreamType
	Chunk []byte
	State runWebJobState
	Error string
}

type runWebJobStatus struct {
	ID    string         `json:"jobId"`
	State runWebJobState `json:"state"`
	Error string         `json:"error,omitempty"`
}

type runWebJob struct {
	id         string
	stdout     io.Writer
	notice     io.Writer
	limit      int
	subBuf     int
	done       chan struct{}
	noticeOnce sync.Once

	mu          sync.Mutex
	nextID      int64
	state       runWebJobState
	errText     string
	bufferBytes int
	omitted     bool
	events      []runWebStreamEvent
	subscribers map[chan runWebStreamEvent]struct{}
}
```

Implement these methods:

```go
func newRunWebJob(id string, cfg runWebJobConfig) *runWebJob
func (j *runWebJob) Write(p []byte) (int, error)
func (j *runWebJob) finish(err error)
func (j *runWebJob) status() runWebJobStatus
func (j *runWebJob) subscribe(ctx context.Context, lastID int64) (<-chan runWebStreamEvent, <-chan struct{})
func (j *runWebJob) browserClosed()
func (ev runWebStreamEvent) ssePayload() (eventName string, data []byte, err error)
```

Use these details:

```go
const defaultRunWebJobBufferLimit = 1 << 20
const defaultRunWebSubscriberBuffer = 64
const runWebOutputOmittedMessage = "[older output omitted]\n"
const runWebBrowserClosedMessage = "Browser tab closed. Press Ctrl-C to quit.\n"
```

`Write` must write to `stdout` first, then append/broadcast the output event. If
the terminal write fails, return that error. Subscriber sends must be
non-blocking:

```go
select {
case ch <- ev:
default:
}
```

`finish` must append one status event, close `done`, and close all subscribers.
If `err != nil`, append `Error: <message>\n` to the output stream before the
failed status when the previous output does not already contain the error text.

`ssePayload` must encode output events as:

```json
{"encoding":"base64","chunk":"..."}
```

and status events as:

```json
{"state":"succeeded"}
```

- [ ] **Step 4: Run job tests**

Run:

```bash
go test ./pkg/yeet -run 'TestRunWebJob' -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/yeet/run_web_job.go pkg/yeet/run_web_job_test.go
git commit -m "yeet: add web deploy job stream"
```

## Task 4: Convert Web Deploy API To Background Jobs And SSE

**Files:**
- Modify: `pkg/yeet/run_web_api.go`
- Modify: `pkg/yeet/run_web.go`
- Modify: `pkg/yeet/run_web_api_test.go`

- [ ] **Step 1: Update tests for job-start response and failure retry**

In `pkg/yeet/run_web_api_test.go`, update `executeRunDraftFn` stubs to
`executeRunDraftWithOptionsFn` where the web API starts jobs.

Add this test:

```go
func TestRunWebAPIDeployStartsJobAndStreamsOutput(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		_, _ = opts.Stdout.Write([]byte("deploy output\n"))
		return nil
	}

	root := t.TempDir()
	payload := filepath.Join(root, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	var terminal bytes.Buffer
	completed := make(chan struct{}, 1)
	s := newRunWebServer(runWebServerConfig{
		Token: "secret",
		Root:  root,
		Out:   &terminal,
		OnComplete: func() {
			completed <- struct{}{}
		},
	})

	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"})
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy status = %d body=%s", rec.Code, rec.Body.String())
	}
	var started struct {
		OK    bool   `json:"ok"`
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !started.OK || started.JobID == "" {
		t.Fatalf("started = %#v, want ok job", started)
	}

	stream := runWebAPIRequest(t, s, http.MethodGet, "/api/deploy/"+started.JobID+"/stream", nil)
	if stream.Code != http.StatusOK {
		t.Fatalf("stream status = %d body=%s", stream.Code, stream.Body.String())
	}
	body := stream.Body.String()
	if !strings.Contains(body, "event: output") || !strings.Contains(body, "event: status") {
		t.Fatalf("stream body = %s, want output and status events", body)
	}
	if !strings.Contains(terminal.String(), "deploy output\n") {
		t.Fatalf("terminal = %q, want deploy output", terminal.String())
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("OnComplete was not called")
	}
}
```

Add this test:

```go
func TestRunWebAPIDeployFailureKeepsServerRetryable(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	calls := 0
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		calls++
		if calls == 1 {
			_, _ = opts.Stdout.Write([]byte("bad deploy\n"))
			return errors.New("deploy failed")
		}
		_, _ = opts.Stdout.Write([]byte("retry deploy\n"))
		return nil
	}

	root := t.TempDir()
	payload := filepath.Join(root, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root, Out: io.Discard})
	draft := RunDraft{Service: "svc-a", Host: "host-a", Payload: "run.sh"}

	first := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	firstJob := decodeRunWebStartedJob(t, first)
	waitRunWebJobState(t, s, firstJob.JobID, runWebJobFailed)

	second := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", draft)
	secondJob := decodeRunWebStartedJob(t, second)
	if secondJob.JobID == firstJob.JobID {
		t.Fatalf("retry job id reused: %q", secondJob.JobID)
	}
	waitRunWebJobState(t, s, secondJob.JobID, runWebJobSucceeded)
}
```

Add helper functions in the same test file:

```go
type runWebStartedJob struct {
	OK    bool   `json:"ok"`
	JobID string `json:"jobId"`
}

func decodeRunWebStartedJob(t *testing.T, rec *httptest.ResponseRecorder) runWebStartedJob {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var started runWebStartedJob
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode started job: %v", err)
	}
	if !started.OK || started.JobID == "" {
		t.Fatalf("started = %#v, want ok job id", started)
	}
	return started
}

func waitRunWebJobState(t *testing.T, handler http.Handler, jobID string, want runWebJobState) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		rec := runWebAPIRequest(t, handler, http.MethodGet, "/api/deploy/"+jobID+"/status", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status response = %d body=%s", rec.Code, rec.Body.String())
		}
		var status runWebJobStatus
		if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		if status.State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach state %s", jobID, want)
}
```

- [ ] **Step 2: Run focused API tests and verify they fail**

Run:

```bash
go test ./pkg/yeet -run 'TestRunWebAPIDeployStartsJobAndStreamsOutput|TestRunWebAPIDeployFailureKeepsServerRetryable' -count=1
```

Expected: fail because `/api/deploy` still blocks and returns the old response shape.

- [ ] **Step 3: Extend server config and server state**

In `pkg/yeet/run_web_api.go`, update `runWebServerConfig`:

```go
type runWebServerConfig struct {
	Token      string
	CSRFToken  string
	Root       string
	Bootstrap  runWebBootstrap
	Config     *projectConfigLocation
	Context    context.Context
	Out        io.Writer
	Err        io.Writer
	OnComplete func()
}
```

Update `runWebServer` fields:

```go
type runWebServer struct {
	cfg      runWebServerConfig
	mux      *http.ServeMux
	deployMu sync.Mutex
	active   *runWebJob
	complete bool
	nextJob  int64
}
```

Add routes in `newRunWebServer`:

```go
s.mux.HandleFunc("/api/session/closed", s.handleSessionClosed)
s.mux.HandleFunc("/api/deploy/", s.handleDeployJob)
```

- [ ] **Step 4: Pass terminal writers from `run_web.go`**

In `pkg/yeet/run_web.go`, when building `runWebServerConfig`, add:

```go
Out: req.Out,
Err: req.Err,
```

Before creating the server, default `req.Out` to `os.Stdout` and `req.Err` to
`os.Stderr` if nil.

- [ ] **Step 5: Replace deploy handler with job start**

In `pkg/yeet/run_web_api.go`, rewrite `handleDeploy` so after validation it calls:

```go
job, ok, status, message := s.startDeployJob(normalized)
if !ok {
	http.Error(w, message, status)
	return
}
writeRunWebJSON(w, http.StatusOK, map[string]any{"ok": true, "jobId": job.id})
```

Implement:

```go
func (s *runWebServer) startDeployJob(draft RunDraft) (*runWebJob, bool, int, string) {
	s.deployMu.Lock()
	defer s.deployMu.Unlock()
	if s.complete {
		return nil, false, http.StatusConflict, "deployment already completed"
	}
	if s.active != nil && s.active.status().State == runWebJobRunning {
		return nil, false, http.StatusConflict, "deployment already in progress"
	}
	s.nextJob++
	job := newRunWebJob(fmt.Sprintf("%d", s.nextJob), runWebJobConfig{
		Stdout:      defaultRunWebWriter(s.cfg.Out, os.Stdout),
		Notice:      defaultRunWebWriter(s.cfg.Err, os.Stderr),
		BufferLimit: defaultRunWebJobBufferLimit,
	})
	s.active = job
	go s.runDeployJob(job, draft)
	return job, true, http.StatusOK, ""
}

func defaultRunWebWriter(primary io.Writer, fallback io.Writer) io.Writer {
	if primary != nil {
		return primary
	}
	return fallback
}
```

Implement `runDeployJob`:

```go
func (s *runWebServer) runDeployJob(job *runWebJob, draft RunDraft) {
	ctx, cancel := runWebDeployContext(s.cfg.Context)
	defer cancel()
	if normalizedEnv := strings.TrimSpace(draft.EnvFile); normalizedEnv != "" {
		draft.EnvFileSet = true
		draft.EnvFileArg = normalizedEnv
	}
	err := executeRunDraftWithOptionsFn(ctx, draft, s.cfg.Config, runDraftExecuteOptions{Stdout: job})
	job.finish(err)
	if err != nil {
		return
	}
	s.deployMu.Lock()
	s.complete = true
	s.deployMu.Unlock()
	if s.cfg.OnComplete != nil {
		go s.cfg.OnComplete()
	}
}
```

- [ ] **Step 6: Add job lookup/status/stream handlers**

Implement:

```go
func (s *runWebServer) lookupJob(id string) (*runWebJob, bool) {
	s.deployMu.Lock()
	defer s.deployMu.Unlock()
	if s.active == nil || s.active.id != id {
		return nil, false
	}
	return s.active, true
}

func (s *runWebServer) handleDeployJob(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/deploy/")
	id, action, ok := strings.Cut(rest, "/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	job, found := s.lookupJob(id)
	if !found {
		http.NotFound(w, r)
		return
	}
	switch action {
	case "stream":
		s.handleDeployStream(w, r, job)
	case "status":
		writeRunWebJSON(w, http.StatusOK, job.status())
	default:
		http.NotFound(w, r)
	}
}
```

Implement `handleDeployStream`:

```go
func (s *runWebServer) handleDeployStream(w http.ResponseWriter, r *http.Request, job *runWebJob) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	lastID, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	events, done := job.subscribe(r.Context(), lastID)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	for ev := range events {
		if err := writeRunWebSSE(w, ev); err != nil {
			return
		}
		flusher.Flush()
	}
	<-done
}

func writeRunWebSSE(w io.Writer, ev runWebStreamEvent) error {
	name, data, err := ev.ssePayload()
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\nid: %d\n", name, ev.ID); err != nil {
		return err
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	_, err = fmt.Fprint(w, "\n")
	return err
}
```

- [ ] **Step 7: Add session closed handler**

Implement:

```go
func (s *runWebServer) handleSessionClosed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.deployMu.Lock()
	job := s.active
	complete := s.complete
	s.deployMu.Unlock()
	if job != nil && !complete {
		job.browserClosed()
	}
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 8: Remove old `beginDeploy`/`finishDeploy` or make them unused-free**

Delete the old `runWebDeployState`, `beginDeploy`, and `finishDeploy` code if
no call sites remain. Keep the "one running deploy" and "single successful
deploy" behavior in `startDeployJob`.

- [ ] **Step 9: Run focused API tests**

Run:

```bash
go test ./pkg/yeet -run 'TestRunWebAPI.*Deploy|TestRunWebAPIUnsafe|TestRunWebAPIStatic|TestRunWebAPIValidate' -count=1
```

Expected: pass.

- [ ] **Step 10: Commit**

```bash
git add pkg/yeet/run_web.go pkg/yeet/run_web_api.go pkg/yeet/run_web_api_test.go
git commit -m "yeet: stream web deploy jobs"
```

## Task 5: Add Browser Terminal Sheet And Stream Client

**Files:**
- Modify: `pkg/yeet/web_run_assets/index.html`
- Modify: `pkg/yeet/web_run_assets/styles.css`
- Modify: `pkg/yeet/web_run_assets/app.js`
- Modify: `pkg/yeet/web_run_assets_test.go`

- [ ] **Step 1: Update asset test for terminal sheet hooks**

In `pkg/yeet/web_run_assets_test.go`, extend `TestWebRunAssetsExposeFirstDeployFields` with checks:

```go
for _, want := range []string{
	`id="terminalSheet"`,
	`id="terminalOutput"`,
	`id="terminalStatus"`,
	`id="terminalCopy"`,
	`id="terminalExpand"`,
	`id="hostPicker"`,
	`EventSource`,
	`/api/session/closed`,
	`TextDecoder`,
	`setDeployMode`,
	`createTerminalRenderer`,
	`showHostPicker`,
} {
	if !strings.Contains(index+app, want) {
		t.Fatalf("web run assets missing %q", want)
	}
}
if strings.Contains(index, `<datalist id="hostOptions"`) {
	t.Fatal("web run host picker should not use native datalist prefix filtering")
}
```

- [ ] **Step 2: Run the asset test and verify it fails**

Run:

```bash
go test ./pkg/yeet -run TestWebRunAssetsExposeFirstDeployFields -count=1
```

Expected: fail because terminal markup and stream client are missing.

- [ ] **Step 3: Replace host datalist with custom host picker markup**

In `pkg/yeet/web_run_assets/index.html`, replace the host input and datalist
with a picker field that always shows all known hosts:

```html
          <label for="host" data-field="host">
            <span>Host <button type="button" class="help" data-help="Target catch host from CATCH_HOST, yeet.toml, or prefs." aria-label="Help for host">?</button></span>
            <div class="picker-field host-picker-field">
              <input id="host" name="host" autocomplete="off" spellcheck="false" required aria-haspopup="listbox" aria-expanded="false">
              <button type="button" id="hostPickerButton" class="quiet-button picker-trigger" aria-label="Choose host">Choose</button>
            </div>
            <div class="host-picker" id="hostPicker" role="listbox" hidden></div>
            <span class="field-error" id="hostError"></span>
          </label>
```

Do not keep `<datalist id="hostOptions">`. Native datalist prefix-filters and
renders as an unthemed browser popup, which is not acceptable for this UI.

- [ ] **Step 4: Add host picker JS**

In `pkg/yeet/web_run_assets/app.js`, remove all code that populates
`hostOptions`. Add these helpers:

```js
function renderHostPicker(hosts) {
  const picker = $("hostPicker");
  const rows = (hosts || []).map((host) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "host-option";
    button.setAttribute("role", "option");
    button.textContent = host;
    button.addEventListener("click", () => {
      $("host").value = host;
      hideHostPicker();
      update();
    });
    return button;
  });
  picker.replaceChildren(...rows);
}

function showHostPicker() {
  const picker = $("hostPicker");
  picker.hidden = false;
  $("host").setAttribute("aria-expanded", "true");
}

function hideHostPicker() {
  $("hostPicker").hidden = true;
  $("host").setAttribute("aria-expanded", "false");
}
```

In `bootstrap()`, replace the `hostOptions` datalist population with:

```js
renderHostPicker(state.bootstrap.hosts || []);
```

Wire events:

```js
$("host").addEventListener("focus", showHostPicker);
$("host").addEventListener("click", showHostPicker);
$("hostPickerButton").addEventListener("click", showHostPicker);
document.addEventListener("click", (event) => {
  if (event.target.closest("#hostPicker") || event.target.closest("#host") || event.target.closest("#hostPickerButton")) return;
  hideHostPicker();
});
```

The picker must always display every known host from bootstrap, regardless of
the current text in the host input.

- [ ] **Step 5: Add host picker CSS**

In `pkg/yeet/web_run_assets/styles.css`, add:

```css
.host-picker-field {
  position: relative;
}

.host-picker {
  margin-top: -5px;
  overflow: hidden;
  border: 1px solid var(--line-strong);
  border-top: 0;
  border-radius: 0 0 8px 8px;
  background: oklch(0.15 0.012 165);
  box-shadow: 0 14px 32px color-mix(in oklch, var(--canvas) 72%, transparent);
}

.host-picker[hidden] {
  display: none;
}

.host-option {
  width: 100%;
  min-height: 36px;
  display: block;
  padding: 8px 10px;
  border: 0;
  color: var(--text);
  background: transparent;
  text-align: left;
}

.host-option:hover,
.host-option:focus-visible {
  background: var(--surface-2);
}
```

The visual result should look like the host field extending downward into a
small themed option list, not a white browser tooltip.

- [ ] **Step 6: Add terminal sheet markup**

In `pkg/yeet/web_run_assets/index.html`, add this before the file picker:

```html
    <section class="terminal-sheet" id="terminalSheet" aria-labelledby="terminalTitle" hidden>
      <div class="terminal-head">
        <div>
          <h2 id="terminalTitle">Deploy output</h2>
          <p id="terminalSubtitle">Waiting for deployment</p>
        </div>
        <span id="terminalStatus" class="terminal-status">Idle</span>
        <button id="terminalCopy" type="button" class="quiet-button">Copy output</button>
        <button id="terminalExpand" type="button" class="quiet-button" aria-expanded="false">Expand</button>
      </div>
      <pre id="terminalOutput" class="terminal-output" aria-live="polite"></pre>
    </section>
```

- [ ] **Step 7: Add terminal sheet CSS**

In `pkg/yeet/web_run_assets/styles.css`, add:

```css
.terminal-sheet {
  position: fixed;
  right: 24px;
  bottom: 74px;
  left: 24px;
  z-index: 12;
  max-width: 1180px;
  margin: 0 auto;
  border: 1px solid var(--line-strong);
  border-radius: 8px;
  background: oklch(0.12 0.012 165);
  box-shadow: 0 22px 60px color-mix(in oklch, black 42%, transparent);
}

.terminal-sheet[hidden] {
  display: none;
}

.terminal-sheet[data-expanded="true"] {
  top: 84px;
}

.terminal-head {
  min-height: 48px;
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto auto auto;
  gap: 10px;
  align-items: center;
  padding: 10px 12px;
  border-bottom: 1px solid var(--line);
}

.terminal-head h2 {
  font-size: 15px;
}

.terminal-head p {
  overflow: hidden;
  color: var(--quiet);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.terminal-status {
  color: var(--accent);
  font-size: 13px;
  white-space: nowrap;
}

.terminal-status[data-tone="failed"] {
  color: var(--danger);
}

.terminal-status[data-tone="done"] {
  color: var(--brand);
}

.terminal-output {
  height: min(240px, 38vh);
  margin: 0;
  overflow: auto;
  padding: 12px;
  color: oklch(0.91 0.018 155);
  background: oklch(0.105 0.01 165);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 13px;
  line-height: 1.45;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.terminal-sheet[data-expanded="true"] .terminal-output {
  height: calc(100vh - 210px);
}
```

- [ ] **Step 8: Add terminal renderer and stream client**

In `pkg/yeet/web_run_assets/app.js`, add state:

```js
  deployJobId: "",
  deployEvents: null,
  terminal: null,
```

Add the renderer:

```js
function createTerminalRenderer(output) {
  const decoder = new TextDecoder();
  let transcript = "";
  let currentLine = "";
  let autoScroll = true;

  output.addEventListener("scroll", () => {
    autoScroll = output.scrollTop + output.clientHeight >= output.scrollHeight - 8;
  });

  function render() {
    output.textContent = transcript + currentLine;
    if (autoScroll) output.scrollTop = output.scrollHeight;
  }

  return {
    write(bytes) {
      const text = decoder.decode(bytes, { stream: true });
      for (const char of stripANSI(text)) {
        if (char === "\r") {
          currentLine = "";
        } else if (char === "\n") {
          transcript += currentLine + "\n";
          currentLine = "";
        } else {
          currentLine += char;
        }
      }
      render();
    },
    clear() {
      transcript = "";
      currentLine = "";
      render();
    },
    text() {
      return transcript + currentLine;
    },
  };
}

function stripANSI(text) {
  return text.replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, "");
}
```

Add helpers:

```js
function showTerminal(draft) {
  if (!state.terminal) state.terminal = createTerminalRenderer($("terminalOutput"));
  state.terminal.clear();
  $("terminalSubtitle").textContent = `${draft.service}@${draft.host}`;
  setTerminalStatus("Running", "");
  $("terminalSheet").hidden = false;
}

function setTerminalStatus(message, tone = "") {
  $("terminalStatus").textContent = message;
  if (tone) $("terminalStatus").dataset.tone = tone;
  else delete $("terminalStatus").dataset.tone;
}

function setDeployMode(enabled) {
  document.querySelectorAll("#deployForm input, #deployForm select, #deployForm button").forEach((el) => {
    if (el.id === "deployButton") return;
    el.disabled = enabled;
  });
}

function decodeOutputChunk(data) {
  const payload = JSON.parse(data);
  const raw = atob(payload.chunk || "");
  const bytes = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i);
  return bytes;
}
```

Rewrite successful deploy handling in `deploy(event)`:

```js
    const data = await res.json();
    state.deployJobId = data.jobId || "";
    showTerminal(draft);
    followDeployStream(state.deployJobId);
    setDeployMode(true);
    setStatus("Deploying", "ready");
```

Add stream handling:

```js
function followDeployStream(jobId) {
  if (state.deployEvents) state.deployEvents.close();
  const events = new EventSource(`/api/deploy/${encodeURIComponent(jobId)}/stream`);
  state.deployEvents = events;
  events.addEventListener("output", (event) => {
    state.terminal.write(decodeOutputChunk(event.data));
  });
  events.addEventListener("status", (event) => {
    const status = JSON.parse(event.data);
    if (status.state === "succeeded") {
      state.phase = "done";
      events.close();
      setTerminalStatus("Deployed", "done");
      setStatus("Deployed. Close this tab and return to the terminal.", "done");
      $("hostStatus").textContent = "Deployed";
      return;
    }
    if (status.state === "failed") {
      state.phase = "editing";
      events.close();
      setTerminalStatus("Failed", "failed");
      setStatus(status.error || "Deploy failed", "error");
      setDeployMode(false);
      update();
    }
  });
  events.onerror = () => {
    if (state.phase === "deploying") {
      setStatus("Lost deploy output stream. Deployment is still running in the terminal.", "error");
    }
  };
}
```

Add page close notification:

```js
window.addEventListener("pagehide", () => {
  if (state.phase === "done") return;
  const headers = {"X-Yeet-Run-CSRF": csrfToken};
  const body = new Blob(["{}"], { type: "application/json" });
  if (navigator.sendBeacon) {
    navigator.sendBeacon("/api/session/closed", body);
    return;
  }
  fetch("/api/session/closed", { method: "POST", headers, body, keepalive: true }).catch(() => {});
});
```

Wire controls:

```js
$("terminalCopy").addEventListener("click", async () => {
  if (!state.terminal) return;
  await navigator.clipboard.writeText(state.terminal.text());
});
$("terminalExpand").addEventListener("click", () => {
  const sheet = $("terminalSheet");
  const expanded = sheet.dataset.expanded !== "true";
  sheet.dataset.expanded = String(expanded);
  $("terminalExpand").setAttribute("aria-expanded", String(expanded));
  $("terminalExpand").textContent = expanded ? "Collapse" : "Expand";
});
```

- [ ] **Step 9: Run JS and asset checks**

Run:

```bash
node --check pkg/yeet/web_run_assets/app.js
go test ./pkg/yeet -run TestWebRunAssetsExposeFirstDeployFields -count=1
```

Expected: both pass.

- [ ] **Step 10: Commit**

```bash
git add pkg/yeet/web_run_assets/index.html pkg/yeet/web_run_assets/styles.css pkg/yeet/web_run_assets/app.js pkg/yeet/web_run_assets_test.go
git commit -m "yeet: show web deploy terminal output"
```

## Task 6: Browser Close Fallback And Integration Polish

**Files:**
- Modify: `pkg/yeet/run_web_job.go`
- Modify: `pkg/yeet/run_web_api.go`
- Modify: `pkg/yeet/run_web_api_test.go`
- Modify: `pkg/yeet/web_run_assets/app.js`

- [ ] **Step 1: Add server test for explicit tab close**

Add to `pkg/yeet/run_web_api_test.go`:

```go
func TestRunWebAPISessionClosedPrintsHintOnce(t *testing.T) {
	var notice bytes.Buffer
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: t.TempDir(), Err: &notice})

	srv := s.(*runWebServer)
	srv.active = newRunWebJob("job-1", runWebJobConfig{Stdout: io.Discard, Notice: &notice, BufferLimit: 1024})

	first := runWebAPIRequest(t, s, http.MethodPost, "/api/session/closed", map[string]string{})
	second := runWebAPIRequest(t, s, http.MethodPost, "/api/session/closed", map[string]string{})
	if first.Code != http.StatusNoContent || second.Code != http.StatusNoContent {
		t.Fatalf("statuses = %d/%d, want 204/204", first.Code, second.Code)
	}
	if got := notice.String(); got != "Browser tab closed. Press Ctrl-C to quit.\n" {
		t.Fatalf("notice = %q, want one close hint", got)
	}
}
```

- [ ] **Step 2: Run the close test**

Run:

```bash
go test ./pkg/yeet -run TestRunWebAPISessionClosedPrintsHintOnce -count=1
```

Expected: pass if Task 4 and Task 5 are complete. If it fails because `newRunWebServer` returns `http.Handler`, add a package helper `runWebServerForTest` in the test file that type-asserts and fails clearly.

- [ ] **Step 3: Add stream subscriber close fallback**

In `run_web_job.go`, add subscriber count tracking and a debounced close notice:

```go
func (j *runWebJob) noteSubscriberClosed() {
	go func() {
		time.Sleep(750 * time.Millisecond)
		j.mu.Lock()
		noSubscribers := len(j.subscribers) == 0 && j.state != runWebJobSucceeded
		j.mu.Unlock()
		if noSubscribers {
			j.browserClosed()
		}
	}()
}
```

Call `j.noteSubscriberClosed()` when `subscribe` removes a subscriber because
the stream context ended before success.

- [ ] **Step 4: Run close and stream tests**

Run:

```bash
go test ./pkg/yeet -run 'TestRunWebJob|TestRunWebAPI.*Session|TestRunWebAPI.*Stream' -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/yeet/run_web_job.go pkg/yeet/run_web_api.go pkg/yeet/run_web_api_test.go pkg/yeet/web_run_assets/app.js
git commit -m "yeet: handle web run tab close"
```

## Task 7: Update Docs And Screenshot

**Files:**
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/operations/workflows.mdx`
- Possibly modify: `website/public/images/web-run-deploy.png`
- Modify root submodule pointer: `website`

- [ ] **Step 1: Update CLI docs wording**

In `website/docs/cli/yeet-cli.mdx`, under the `yeet run --web` section, add:

```mdx
After you click Deploy, yeet opens a compact read-only terminal in the browser
that mirrors the local terminal output. Successful deploys tell you to close the
tab and return to the terminal. Failed deploys keep the form open so you can
adjust fields and try again.
```

- [ ] **Step 2: Update workflow docs wording**

In `website/docs/operations/workflows.mdx`, near the web deploy example, add:

```mdx
The web flow keeps the browser form alive until a deploy succeeds. Runtime
errors appear in the browser terminal and the local terminal, and you can edit
the form and retry without restarting `yeet run --web`.
```

- [ ] **Step 3: Refresh screenshot if the old screenshot shows the deploy screen**

Run a local fake web-run screenshot flow and write the updated image to:

```text
website/public/images/web-run-deploy.png
```

Use sanitized labels and no private hostnames. The screenshot should show the
terminal sheet with generic output such as:

```text
[+] yeet run demo@catch
Service deployed
```

- [ ] **Step 4: Run website docs checks**

Run:

```bash
git -C website diff --check
npm run build:next
```

Run `git -C website diff --check` from the repo root. Run
`npm run build:next` from `website/`.

Expected: diff check passes and Next build completes.

- [ ] **Step 5: Commit website changes inside submodule**

```bash
cd website
git add docs/cli/yeet-cli.mdx docs/operations/workflows.mdx public/images/web-run-deploy.png
git commit -m "docs: show web deploy terminal output"
```

- [ ] **Step 6: Commit root submodule pointer**

```bash
cd ..
git add website
git commit -m "docs: update web deploy terminal docs"
```

## Task 8: Final Verification And Optional Live E2E

**Files:**
- No code changes expected.

- [ ] **Step 1: Run package tests**

Run:

```bash
go test ./pkg/yeet -count=1
go test ./pkg/catchrpc ./pkg/catch ./pkg/yeet -count=1
```

Expected: both pass.

- [ ] **Step 2: Run full test suite and JS syntax check**

Run:

```bash
go test ./... -count=1
node --check pkg/yeet/web_run_assets/app.js
```

Expected: both pass.

- [ ] **Step 3: Run pre-commit**

Run:

```bash
pre-commit run --all-files
```

Expected: all hooks pass.

- [ ] **Step 4: Optional live E2E on a configured catch host**

Use a disposable service name and clean it up. Use the `yeet-cli` skill before
running live commands.

Example shape:

```bash
GOBIN=$HOME/go/bin go install ./cmd/yeet
cd example/helloworld
yeet run --web webterm-smoke-$(date +%s) ./helloworld
```

Verify:

- browser terminal shows deploy output
- local terminal mirrors the same output
- success tells the user to close the tab
- failed deploy can be retried from the same form
- tab close prints `Browser tab closed. Press Ctrl-C to quit.`

Cleanup:

```bash
yeet rm <disposable-service-name>
```

- [ ] **Step 5: Record verification in final response**

Include exact commands run and whether live E2E was performed. If live E2E was
not run, say that plainly.
