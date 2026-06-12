# Web Run Workload Builder Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Redesign `yeet run --web` into a workload-first, keyboard-friendly new-service builder with visible network and ZFS controls, catalog-first VM setup, and scheduled-job support.

**Architecture:** Keep Go authoritative for `RunDraft` validation, command preview, and deploy execution. The browser owns only interaction state and form-to-draft mapping; it posts the same typed draft shape to the existing web-run API and renders backend-normalized results. Cron is added to the draft execution path explicitly because it is currently a separate `yeet cron` command surface.

**Tech Stack:** Go `testing` and `httptest`, embedded static HTML/CSS/JS through `go:embed`, existing yeet `RunDraft` and catch RPC execution seams, website MDX documentation.

---

## Scope Check

The approved spec is one feature with three dependent layers:

- Backend metadata, validation, preview, and cron draft execution.
- Browser workload-builder UI and form-to-draft mapping.
- User documentation.

These layers should stay in one implementation plan because the browser cannot be usefully verified until the backend exposes the metadata and preview it needs.

One constraint to preserve: cron is currently implemented by `yeet cron`, not by `yeet run` flags. This plan adds cron to the web draft path with schedule/source/args support, while rejecting run-only controls for cron with field-specific errors. Network and ZFS controls remain prominent for run-backed workloads; cron support does not silently invent a service-root or network contract that the CLI does not support.

## File Structure

- Modify `pkg/yeet/run_web.go`: add workload metadata and catalog VM image hints to bootstrap options.
- Modify `pkg/yeet/run_web_api.go`: return structured validate responses containing normalized draft and backend command preview.
- Modify `pkg/yeet/run_draft.go`: add cron draft fields, cron execution branch, output-aware cron runner, and command preview helpers.
- Modify `pkg/yeet/run_draft_validate.go`: allow `payloadKind: "cron"`, validate cron schedules, and reject run-only draft fields for cron.
- Modify `pkg/yeet/run_web_test.go`: cover bootstrap workload metadata.
- Modify `pkg/yeet/run_web_api_test.go`: cover validate command preview and cron draft deployment through the web API.
- Modify `pkg/yeet/run_draft_test.go`: cover cron draft execution and command preview.
- Modify `pkg/yeet/run_draft_validate_test.go`: cover cron schedule validation and unsupported cron fields.
- Modify `pkg/yeet/web_run_assets/index.html`: replace payload-first markup with workload-first builder sections.
- Modify `pkg/yeet/web_run_assets/app.js`: add workload state, source mapping, backend preview use, and workload-specific UI behavior.
- Modify `pkg/yeet/web_run_assets/styles.css`: support workload selector, visible network/storage sections, and compact source layouts.
- Modify `pkg/yeet/web_run_assets_test.go`: assert required workload, network, ZFS, catalog VM, cron, and backend-preview hooks exist.
- Modify `website/docs/cli/yeet-cli.mdx`: update `yeet run --web` user documentation.

Use pathspec commits for every task because this workspace may already have unrelated staged files from the earlier `yeet logs` work:

```bash
git commit -m "<message>" -- path/changed-by-this-task path/also-changed
```

## Task 1: Expose Workload And Catalog Metadata

**Files:**
- Modify: `pkg/yeet/run_web.go`
- Modify: `pkg/yeet/run_web_test.go`

- [ ] **Step 1: Write the failing bootstrap metadata test**

Add this test to `pkg/yeet/run_web_test.go` near the existing bootstrap tests:

```go
func TestRunWebBootstrapExposesWorkloadsAndCatalogVMImages(t *testing.T) {
	boot := newRunWebBootstrap(nil, "", "", nil)

	wantKinds := []string{"compose", "vm", "dockerfile", "remote-image", "file", "cron"}
	if got := runWebWorkloadKinds(boot.Options.Workloads); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("workload kinds = %#v, want %#v", got, wantKinds)
	}
	if len(boot.Options.VMImages) != 2 {
		t.Fatalf("VMImages = %#v, want ubuntu and nixos catalog images", boot.Options.VMImages)
	}
	if boot.Options.VMImages[0].Payload != "vm://ubuntu/26.04" || boot.Options.VMImages[0].Label != "Ubuntu 26.04" {
		t.Fatalf("first VM image = %#v, want Ubuntu 26.04", boot.Options.VMImages[0])
	}
	if boot.Options.VMImages[1].Payload != "vm://nixos/26.05" || boot.Options.VMImages[1].Label != "NixOS 26.05" {
		t.Fatalf("second VM image = %#v, want NixOS 26.05", boot.Options.VMImages[1])
	}
}

func runWebWorkloadKinds(workloads []runWebWorkloadHint) []string {
	out := make([]string, 0, len(workloads))
	for _, workload := range workloads {
		out = append(out, workload.Kind)
	}
	return out
}
```

- [ ] **Step 2: Run the focused test to verify it fails**

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestRunWebBootstrapExposesWorkloadsAndCatalogVMImages -count=1
```

Expected: fail with errors that `runWebWorkloadHint`, `Options.Workloads`, or `Options.VMImages` are undefined.

- [ ] **Step 3: Add metadata types and defaults**

In `pkg/yeet/run_web.go`, replace the current `runWebOptionHints` type with:

```go
type runWebOptionHints struct {
	NetworkModes  []string             `json:"networkModes"`
	SnapshotModes []string             `json:"snapshotModes"`
	Workloads     []runWebWorkloadHint `json:"workloads"`
	VMImages      []runWebVMImageHint  `json:"vmImages"`
}

type runWebWorkloadHint struct {
	Kind        string   `json:"kind"`
	Label       string   `json:"label"`
	PayloadKind string   `json:"payloadKind,omitempty"`
	Networks    []string `json:"networks,omitempty"`
	Description string   `json:"description,omitempty"`
}

type runWebVMImageHint struct {
	Payload string `json:"payload"`
	Label   string `json:"label"`
}
```

Then add these helpers below `newRunWebBootstrap`:

```go
func defaultRunWebOptionHints() runWebOptionHints {
	return runWebOptionHints{
		NetworkModes:  []string{"svc", "ts", "lan"},
		SnapshotModes: []string{"inherit", "on", "off"},
		Workloads: []runWebWorkloadHint{
			{Kind: "compose", Label: "Compose app", PayloadKind: "compose", Networks: []string{"host", "svc", "ts", "lan"}, Description: "Deploy a Docker Compose file."},
			{Kind: "vm", Label: "Virtual machine", PayloadKind: serviceTypeVM, Networks: []string{"svc", "lan"}, Description: "Create a VM from a managed catalog image."},
			{Kind: "dockerfile", Label: "Dockerfile", PayloadKind: "dockerfile", Networks: []string{"host", "svc", "ts", "lan"}, Description: "Build and deploy a Dockerfile."},
			{Kind: "remote-image", Label: "Container image", PayloadKind: "remote-image", Networks: []string{"host", "svc", "ts", "lan"}, Description: "Deploy an image reference."},
			{Kind: "file", Label: "Binary/script", PayloadKind: "file", Networks: []string{"host", "svc", "ts", "lan"}, Description: "Upload and run a binary or script."},
			{Kind: serviceTypeCron, Label: "Scheduled job", PayloadKind: serviceTypeCron, Networks: []string{"host"}, Description: "Install a cron-style systemd timer."},
		},
		VMImages: []runWebVMImageHint{
			{Payload: "vm://ubuntu/26.04", Label: "Ubuntu 26.04"},
			{Payload: "vm://nixos/26.05", Label: "NixOS 26.05"},
		},
	}
}
```

Update `newRunWebBootstrap` so `Options` uses the helper:

```go
Options: defaultRunWebOptionHints(),
```

- [ ] **Step 4: Run the focused test to verify it passes**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunWebBootstrap(ExposesWorkloadsAndCatalogVMImages|NetworkModesMatchCatchModes)' -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

Run:

```bash
git add pkg/yeet/run_web.go pkg/yeet/run_web_test.go
git commit -m "yeet: expose web run workload metadata" -- pkg/yeet/run_web.go pkg/yeet/run_web_test.go
```

## Task 2: Return Backend Command Preview From Validation

**Files:**
- Modify: `pkg/yeet/run_web_api.go`
- Modify: `pkg/yeet/run_web_api_test.go`
- Modify: `pkg/yeet/run_draft.go`
- Modify: `pkg/yeet/run_draft_test.go`

- [ ] **Step 1: Write command preview unit tests**

Add these tests to `pkg/yeet/run_draft_test.go` near the existing `runArgs()` tests:

```go
func TestRunDraftCommandPreviewForRunWorkload(t *testing.T) {
	draft := RunDraft{
		Service: "app",
		Host:    "yeet-lab",
		Payload: "./compose.yml",
		Network: RunDraftNetwork{
			Modes:   []string{"svc", "lan"},
			Publish: []string{"8080:80"},
		},
		Storage: RunDraftStorage{ServiceRoot: "tank/apps/app", ZFS: true},
	}

	got := runDraftCommandPreview(draft)
	want := "yeet run app@yeet-lab ./compose.yml --service-root=tank/apps/app --zfs --net=svc,lan --publish=8080:80"
	if got != want {
		t.Fatalf("preview = %q, want %q", got, want)
	}
}

func TestRunDraftCommandPreviewRedactsTailscaleAuthKey(t *testing.T) {
	draft := RunDraft{
		Service: "app",
		Host:    "yeet-lab",
		Payload: "ghcr.io/example/app:latest",
		Network: RunDraftNetwork{
			Modes:     []string{"ts"},
			TSAuthKey: "tskey-secret",
		},
	}

	got := runDraftCommandPreview(draft)
	if strings.Contains(got, "tskey-secret") {
		t.Fatalf("preview leaked auth key: %s", got)
	}
	if !strings.Contains(got, "--ts-auth-key=<hidden>") {
		t.Fatalf("preview = %q, want hidden auth key marker", got)
	}
}
```

- [ ] **Step 2: Write validate API preview test**

Add this test to `pkg/yeet/run_web_api_test.go` near `TestRunWebAPIValidateAndDeploy`:

```go
func TestRunWebAPIValidateReturnsCommandPreview(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	defer func() { fetchRunDraftServiceInfoFn = oldInfo }()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}

	root := t.TempDir()
	payload := filepath.Join(root, "compose.yml")
	if err := os.WriteFile(payload, []byte("services:\n  app:\n    image: nginx\n"), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root})
	draft := RunDraft{
		Service: "app",
		Host:    "yeet-lab",
		Payload: "compose.yml",
		Network: RunDraftNetwork{
			Modes: []string{"svc", "lan"},
		},
		Storage: RunDraftStorage{ServiceRoot: "tank/apps/app", ZFS: true},
	}
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/validate", draft)
	if rec.Code != http.StatusOK {
		t.Fatalf("validate status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, want := range []string{"yeet run app@yeet-lab", "--net=svc,lan", "--service-root=tank/apps/app", "--zfs"} {
		if !strings.Contains(body.Command, want) {
			t.Fatalf("command = %q, missing %q", body.Command, want)
		}
	}
}
```

- [ ] **Step 3: Run focused tests to verify they fail**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunDraftCommandPreview|TestRunWebAPIValidateReturnsCommandPreview' -count=1
```

Expected: fail because `runDraftCommandPreview` and the `command` response field do not exist.

- [ ] **Step 4: Implement backend preview helpers**

Add these helpers to `pkg/yeet/run_draft.go` near `runArgsWithoutSensitiveRunOptions`:

```go
func runDraftCommandPreview(draft RunDraft) string {
	if draft.PayloadKind == serviceTypeCron {
		return runDraftCronCommandPreview(draft)
	}
	parts := []string{"yeet", "run"}
	if target := runDraftCommandTarget(draft); target != "" {
		parts = append(parts, target)
	}
	if strings.TrimSpace(draft.Payload) != "" {
		parts = append(parts, draft.Payload)
	}
	parts = append(parts, runArgsWithoutSensitiveRunOptions(draft.runArgs())...)
	return shellJoin(parts)
}

func runDraftCronCommandPreview(draft RunDraft) string {
	parts := []string{"yeet", serviceTypeCron}
	if target := runDraftCommandTarget(draft); target != "" {
		parts = append(parts, target)
	}
	if strings.TrimSpace(draft.Payload) != "" {
		parts = append(parts, draft.Payload)
	}
	if strings.TrimSpace(draft.Cron.Schedule) != "" {
		parts = append(parts, draft.Cron.Schedule)
	}
	if len(draft.PayloadArgs) != 0 {
		parts = append(parts, "--")
		parts = append(parts, draft.PayloadArgs...)
	}
	return shellJoin(parts)
}

func runDraftCommandTarget(draft RunDraft) string {
	service := strings.TrimSpace(draft.Service)
	host := strings.TrimSpace(draft.Host)
	if service == "" {
		return ""
	}
	if host == "" || strings.Contains(service, "@") {
		return service
	}
	return service + "@" + host
}
```

This uses the existing `shellJoin` helper in `pkg/yeet/ssh_cmd.go`.

- [ ] **Step 5: Add structured validate response**

In `pkg/yeet/run_web_api.go`, add:

```go
type runWebValidateResponse struct {
	Draft      RunDraft                 `json:"draft"`
	Validation RunDraftValidationResult `json:"validation"`
	Command    string                   `json:"command,omitempty"`
}

func runWebValidationResponse(draft RunDraft, result RunDraftValidationResult) runWebValidateResponse {
	return runWebValidateResponse{
		Draft:      redactRunWebDraftSecrets(draft),
		Validation: result,
		Command:    runDraftCommandPreview(draft),
	}
}
```

Replace both validate response map literals:

```go
writeRunWebJSON(w, http.StatusOK, map[string]any{"draft": redactRunWebDraftSecrets(normalized), "validation": result})
```

with:

```go
writeRunWebJSON(w, http.StatusOK, runWebValidationResponse(normalized, result))
```

Replace the deploy validation failure response:

```go
writeRunWebJSON(w, http.StatusBadRequest, map[string]any{"draft": redactRunWebDraftSecrets(normalized), "validation": result})
```

with:

```go
writeRunWebJSON(w, http.StatusBadRequest, runWebValidationResponse(normalized, result))
```

- [ ] **Step 6: Run focused tests to verify they pass**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunDraftCommandPreview|TestRunWebAPIValidateReturnsCommandPreview|TestRunWebAPIRedactsSecrets' -count=1
```

Expected: pass.

- [ ] **Step 7: Commit**

Run:

```bash
git add pkg/yeet/run_draft.go pkg/yeet/run_draft_test.go pkg/yeet/run_web_api.go pkg/yeet/run_web_api_test.go
git commit -m "yeet: return web run command previews" -- pkg/yeet/run_draft.go pkg/yeet/run_draft_test.go pkg/yeet/run_web_api.go pkg/yeet/run_web_api_test.go
```

## Task 3: Add Cron To The Draft Execution Path

**Files:**
- Modify: `pkg/yeet/run_draft.go`
- Modify: `pkg/yeet/run_draft_validate.go`
- Modify: `pkg/yeet/run_draft_test.go`
- Modify: `pkg/yeet/run_draft_validate_test.go`
- Modify: `pkg/yeet/run_web_api_test.go`

- [ ] **Step 1: Write cron validation tests**

Add these tests to `pkg/yeet/run_draft_validate_test.go`:

```go
func TestValidateRunDraftCronSchedule(t *testing.T) {
	tmp := t.TempDir()
	payload := filepath.Join(tmp, "job.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	normalized, validation := validateRunDraft(context.Background(), RunDraft{
		Service:     "backup",
		Host:        "yeet-lab",
		Payload:     "job.sh",
		PayloadKind: serviceTypeCron,
		Cron:        RunDraftCron{Schedule: "0 3 * * *"},
	}, tmp)
	if !validation.OK {
		t.Fatalf("validation = %#v, want OK", validation)
	}
	if normalized.PayloadKind != serviceTypeCron || normalized.Cron.Schedule != "0 3 * * *" {
		t.Fatalf("normalized cron = kind %q schedule %q", normalized.PayloadKind, normalized.Cron.Schedule)
	}
}

func TestValidateRunDraftCronRejectsBadSchedule(t *testing.T) {
	_, validation := validateRunDraft(context.Background(), RunDraft{
		Service:     "backup",
		Host:        "yeet-lab",
		Payload:     "job.sh",
		PayloadKind: serviceTypeCron,
		Cron:        RunDraftCron{Schedule: "daily"},
	}, t.TempDir())
	if got := validation.fieldError("cron.schedule"); !strings.Contains(got, "cron expression must have 5 fields") {
		t.Fatalf("cron.schedule error = %q, want 5-field error", got)
	}
}

func TestValidateRunDraftCronRejectsRunOnlyFields(t *testing.T) {
	_, validation := validateRunDraft(context.Background(), RunDraft{
		Service:     "backup",
		Host:        "yeet-lab",
		Payload:     "job.sh",
		PayloadKind: serviceTypeCron,
		Cron:        RunDraftCron{Schedule: "0 3 * * *"},
		Network:     RunDraftNetwork{Modes: []string{"svc"}},
		Storage:     RunDraftStorage{ServiceRoot: "tank/apps/backup", ZFS: true},
	}, t.TempDir())
	if got := validation.fieldError("network.modes"); !strings.Contains(got, "network modes are not supported for scheduled jobs") {
		t.Fatalf("network.modes error = %q, want cron network rejection", got)
	}
	if got := validation.fieldError("serviceRoot"); !strings.Contains(got, "service root is not supported for scheduled jobs during web deploy") {
		t.Fatalf("serviceRoot error = %q, want cron service root rejection", got)
	}
}
```

- [ ] **Step 2: Write cron execution test**

Add this test to `pkg/yeet/run_draft_test.go`:

```go
func TestExecuteRunDraftCronUsesCronRunnerAndSavesConfig(t *testing.T) {
	preserveRunDraftGlobals(t)
	oldCron := runCronWithOutputFn
	defer func() { runCronWithOutputFn = oldCron }()

	var gotFile string
	var gotFields []string
	var gotArgs []string
	runCronWithOutputFn = func(ctx context.Context, stdout io.Writer, file string, cronFields []string, binArgs []string) error {
		gotFile = file
		gotFields = append([]string{}, cronFields...)
		gotArgs = append([]string{}, binArgs...)
		_, _ = io.WriteString(stdout, "cron installed\n")
		return nil
	}

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "job.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	loc := &projectConfigLocation{
		Path:   filepath.Join(tmp, projectConfigName),
		Dir:    tmp,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}

	var out strings.Builder
	err := executeRunDraftWithOptions(context.Background(), RunDraft{
		Service:     "backup",
		Host:        "yeet-lab",
		Payload:     payload,
		PayloadKind: serviceTypeCron,
		Cron:        RunDraftCron{Schedule: "0 3 * * *"},
		PayloadArgs: []string{"--full"},
	}, loc, runDraftExecuteOptions{Stdout: &out})
	if err != nil {
		t.Fatalf("executeRunDraftWithOptions: %v", err)
	}
	if gotFile != payload || !reflect.DeepEqual(gotFields, []string{"0", "3", "*", "*", "*"}) || !reflect.DeepEqual(gotArgs, []string{"--full"}) {
		t.Fatalf("cron runner got file=%q fields=%#v args=%#v", gotFile, gotFields, gotArgs)
	}
	if !strings.Contains(out.String(), "cron installed") {
		t.Fatalf("stdout = %q, want cron output", out.String())
	}
	entry, ok := loc.Config.ServiceEntry("backup", "yeet-lab")
	if !ok {
		t.Fatal("saved config missing backup@yeet-lab")
	}
	if entry.Type != serviceTypeCron || entry.Payload != "job.sh" || entry.Schedule != "0 3 * * *" || !reflect.DeepEqual(entry.Args, []string{"--full"}) {
		t.Fatalf("saved cron entry = %#v", entry)
	}
}
```

- [ ] **Step 3: Write web API cron deploy test**

Add this test to `pkg/yeet/run_web_api_test.go`:

```go
func TestRunWebAPIDeployCronDraft(t *testing.T) {
	oldInfo := fetchRunDraftServiceInfoFn
	oldExecDraft := executeRunDraftWithOptionsFn
	defer func() {
		fetchRunDraftServiceInfoFn = oldInfo
		executeRunDraftWithOptionsFn = oldExecDraft
	}()
	fetchRunDraftServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	var deployed RunDraft
	done := make(chan struct{})
	executeRunDraftWithOptionsFn = func(ctx context.Context, draft RunDraft, cfg *projectConfigLocation, opts runDraftExecuteOptions) error {
		deployed = draft
		close(done)
		return nil
	}

	root := t.TempDir()
	payload := filepath.Join(root, "job.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	s := newRunWebServer(runWebServerConfig{Token: "secret", Root: root})
	rec := runWebAPIRequest(t, s, http.MethodPost, "/api/deploy", RunDraft{
		Service:     "backup",
		Host:        "yeet-lab",
		Payload:     "job.sh",
		PayloadKind: serviceTypeCron,
		Cron:        RunDraftCron{Schedule: "0 3 * * *"},
		PayloadArgs: []string{"--full"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cron deploy")
	}
	if deployed.PayloadKind != serviceTypeCron || deployed.Cron.Schedule != "0 3 * * *" || !reflect.DeepEqual(deployed.PayloadArgs, []string{"--full"}) {
		t.Fatalf("deployed cron draft = %#v", deployed)
	}
}
```

- [ ] **Step 4: Run focused tests to verify they fail**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestValidateRunDraftCron|TestExecuteRunDraftCron|TestRunWebAPIDeployCronDraft' -count=1
```

Expected: fail because `RunDraftCron`, `RunDraft.Cron`, and `runCronWithOutputFn` do not exist, and `payloadKind: "cron"` is unknown.

- [ ] **Step 5: Add cron draft fields and validation**

In `pkg/yeet/run_draft.go`, add a field to `RunDraft`:

```go
	Cron           RunDraftCron      `json:"cron,omitempty"`
```

Add the type near `RunDraftSnapshots`:

```go
type RunDraftCron struct {
	Schedule string `json:"schedule,omitempty"`
}
```

In `pkg/yeet/run_draft_validate.go`, update `trimRunDraftFields`:

```go
	draft.Cron.Schedule = strings.Join(strings.Fields(draft.Cron.Schedule), " ")
```

Call cron validation from `validateRunDraftLocal` after storage validation and before snapshots validation:

```go
	validateRunDraftCron(&draft, &result)
```

Add:

```go
func validateRunDraftCron(draft *RunDraft, result *RunDraftValidationResult) {
	if draft.PayloadKind != serviceTypeCron {
		if strings.TrimSpace(draft.Cron.Schedule) != "" {
			result.addError("cron.schedule", "cron schedule is only valid for scheduled jobs")
		}
		return
	}
	fields, err := parseCronSchedule(draft.Cron.Schedule)
	if err != nil {
		result.addError("cron.schedule", "%v", err)
	} else {
		draft.Cron.Schedule = strings.Join(fields, " ")
	}
	if len(draft.Network.Modes) != 0 {
		result.addError("network.modes", "network modes are not supported for scheduled jobs during web deploy")
	}
	if draft.Storage.ServiceRoot != "" || draft.Storage.ZFS {
		result.addError("serviceRoot", "service root is not supported for scheduled jobs during web deploy")
	}
	if runDraftSnapshotsHasFieldOverrides(draft.Snapshots) || strings.TrimSpace(draft.Snapshots.Mode) != "" {
		result.addError("snapshots.mode", "snapshot overrides are not supported for scheduled jobs during web deploy")
	}
}
```

Update `unknownPayloadKind` to allow `serviceTypeCron`:

```go
case "", "auto", "file", "compose", "dockerfile", "remote-image", "local-image", serviceTypeVM, serviceTypeCron:
```

Update `normalizeRunDraftPayload` so `serviceTypeCron` uses file normalization:

```go
case serviceTypeCron:
	return normalizeFileRunDraftPayloadKind(cwd, payload, serviceTypeCron)
```

If the function is map-based, add:

```go
serviceTypeCron: normalizeFileRunDraftPayloadKind,
```

- [ ] **Step 6: Add cron execution branch**

In `pkg/yeet/run_draft.go`, add package seam:

```go
var runCronWithOutputFn = runCronWithOutput
```

Update `executeRunDraftWithOptions` after the service/host override setup and before run args are built:

```go
	if draft.PayloadKind == serviceTypeCron {
		return executeCronRunDraft(ctx, opts.Stdout, cfgLoc, host, draft)
	}
```

Add:

```go
func executeCronRunDraft(ctx context.Context, stdout io.Writer, cfgLoc *projectConfigLocation, host string, draft RunDraft) error {
	if stdout == nil {
		stdout = io.Discard
	}
	fields, err := parseCronSchedule(draft.Cron.Schedule)
	if err != nil {
		return err
	}
	if err := runCronWithOutputFn(ctx, stdout, draft.Payload, fields, draft.PayloadArgs); err != nil {
		return err
	}
	return saveCronConfig(cfgLoc, host, draft.Payload, fields, draft.PayloadArgs)
}

func runCronWithOutput(ctx context.Context, stdout io.Writer, file string, cronFields []string, binArgs []string) error {
	goos, goarch, err := remoteCatchOSAndArchFn()
	if err != nil {
		return err
	}
	payload, cleanup, _, err := openPayloadForUpload(file, goos, goarch)
	if err != nil {
		return err
	}
	defer cleanup()
	if len(cronFields) != 5 {
		return fmt.Errorf("cron expression must have 5 fields, got %d", len(cronFields))
	}
	svc := getService()
	nargs := append([]string{"cron"}, cronFields...)
	if len(binArgs) > 0 {
		nargs = append(nargs, binArgs...)
	}
	tty := isTerminalFn(int(os.Stdout.Fd()))
	return execRemoteToFn(ctx, svc, nargs, payload, tty, stdout)
}
```

- [ ] **Step 7: Run focused tests to verify they pass**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestValidateRunDraftCron|TestExecuteRunDraftCron|TestRunWebAPIDeployCronDraft|TestRunDraftCommandPreview' -count=1
```

Expected: pass.

- [ ] **Step 8: Commit**

Run:

```bash
git add pkg/yeet/run_draft.go pkg/yeet/run_draft_validate.go pkg/yeet/run_draft_test.go pkg/yeet/run_draft_validate_test.go pkg/yeet/run_web_api_test.go
git commit -m "yeet: support cron drafts in web run" -- pkg/yeet/run_draft.go pkg/yeet/run_draft_validate.go pkg/yeet/run_draft_test.go pkg/yeet/run_draft_validate_test.go pkg/yeet/run_web_api_test.go
```

## Task 4: Add Workload-First HTML Structure

**Files:**
- Modify: `pkg/yeet/web_run_assets/index.html`
- Modify: `pkg/yeet/web_run_assets_test.go`

- [ ] **Step 1: Write static asset expectations**

Update `TestWebRunAssetsExposeFirstDeployFields` in `pkg/yeet/web_run_assets_test.go` by adding these required snippets to the `index` snippet list:

```go
`id="workloadSelector"`,
`name="workload"`,
`value="compose"`,
`value="vm"`,
`value="dockerfile"`,
`value="remote-image"`,
`value="file"`,
`value="cron"`,
`id="sourceTitle"`,
`id="vmCatalog"`,
`id="manualVMSource"`,
`id="cronSchedule"`,
`id="deploySettingsTitle"`,
`id="storageModeLabel"`,
`id="zfsHelp"`,
```

Add these forbidden snippets to the same test:

```go
`<summary>VM settings`,
`<summary>LAN settings`,
```

The VM and LAN controls will stay visible or conditionally visible in primary deploy settings, not buried behind those old summaries.

- [ ] **Step 2: Run the static asset test to verify it fails**

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestWebRunAssetsExposeFirstDeployFields -count=1
```

Expected: fail because the new workload and source IDs do not exist.

- [ ] **Step 3: Replace payload-first source markup**

In `pkg/yeet/web_run_assets/index.html`, inside the first panel after host selection, insert the workload selector:

```html
          <fieldset class="workload-selector" id="workloadSelector" aria-labelledby="workloadTitle">
            <legend id="workloadTitle">Workload</legend>
            <label class="workload-option">
              <input type="radio" name="workload" value="compose" checked>
              <span>Compose app</span>
            </label>
            <label class="workload-option">
              <input type="radio" name="workload" value="vm">
              <span>Virtual machine</span>
            </label>
            <label class="workload-option">
              <input type="radio" name="workload" value="dockerfile">
              <span>Dockerfile</span>
            </label>
            <label class="workload-option">
              <input type="radio" name="workload" value="remote-image">
              <span>Container image</span>
            </label>
            <label class="workload-option">
              <input type="radio" name="workload" value="file">
              <span>Binary/script</span>
            </label>
            <label class="workload-option">
              <input type="radio" name="workload" value="cron">
              <span>Scheduled job</span>
            </label>
          </fieldset>

          <div class="source-head">
            <h2 id="sourceTitle">Source</h2>
            <span id="sourceHint">Choose a Compose file.</span>
          </div>
```

Keep the existing payload field, but change its label text to:

```html
            <span id="payloadLabel">Compose file <button type="button" class="help" data-help="A compose.yml or docker-compose.yml file in this project." aria-label="Help for source">?</button></span>
```

Add VM catalog and manual VM source controls near the payload field:

```html
          <div id="vmCatalogBlock" class="catalog-block" hidden>
            <label for="vmCatalog" data-field="payload">
              <span>Catalog image <button type="button" class="help" data-help="Managed VM images from the yeet catalog." aria-label="Help for VM catalog image">?</button></span>
              <select id="vmCatalog" name="vmCatalog"></select>
              <span class="field-error" id="vmCatalogError"></span>
            </label>
            <details class="advanced-block">
              <summary>Manual VM image</summary>
              <label for="manualVMSource" data-field="payload">
                <span>Image reference</span>
                <input id="manualVMSource" name="manualVMSource" autocomplete="off" spellcheck="false" placeholder="vm://ubuntu/26.04">
              </label>
            </details>
          </div>
```

Add cron schedule near source fields:

```html
          <label for="cronSchedule" data-field="cron.schedule" id="cronScheduleField" hidden>
            <span>Schedule <button type="button" class="help" data-help="Five-field cron expression. Presets fill this raw value." aria-label="Help for cron schedule">?</button></span>
            <input id="cronSchedule" name="cronSchedule" autocomplete="off" spellcheck="false" placeholder="0 3 * * *">
            <span class="field-error" id="cronScheduleError"></span>
          </label>
```

- [ ] **Step 4: Make deploy settings headings explicit**

In `pkg/yeet/web_run_assets/index.html`, rename the deploy options heading:

```html
            <h2 id="deploySettingsTitle">Deploy settings</h2>
            <p>Network and storage</p>
```

In the service root label, give the span an ID:

```html
              <span id="storageModeLabel">Service root <button type="button" class="help" data-help="Leave empty to use the catch default root. Enter an absolute filesystem path, or a dataset name when ZFS is enabled." aria-label="Help for service root">?</button></span>
```

In the ZFS toggle, give the text span an ID:

```html
              <span id="zfsHelp">ZFS dataset</span>
```

Move the VM CPU/memory/disk controls out of an advanced `<details>` block by changing:

```html
          <details class="advanced-block" id="vmOptions" hidden>
            <summary>VM settings <button type="button" class="help" data-help="Optional CPU, memory, and disk overrides for vm:// payloads." aria-label="Help for VM settings">?</button></summary>
```

to:

```html
          <div class="conditional-options" id="vmOptions" hidden>
            <div class="subsection-label">VM shape</div>
```

and replace the closing `</details>` for that block with `</div>`.

Move the LAN controls out of an advanced `<details>` block by changing:

```html
          <details class="advanced-block" id="lanOptions" hidden>
            <summary>LAN settings <button type="button" class="help" data-help="Optional macvlan settings used when the LAN network mode is selected." aria-label="Help for LAN settings">?</button></summary>
```

to:

```html
          <div class="conditional-options" id="lanOptions" hidden>
            <div class="subsection-label">LAN settings <button type="button" class="help" data-help="Optional macvlan settings used when the LAN network mode is selected." aria-label="Help for LAN settings">?</button></div>
```

and replace the closing `</details>` for that block with `</div>`.

- [ ] **Step 5: Run the static asset test to verify it passes**

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestWebRunAssetsExposeFirstDeployFields -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

Run:

```bash
git add pkg/yeet/web_run_assets/index.html pkg/yeet/web_run_assets_test.go
git commit -m "yeet: add workload-first web run markup" -- pkg/yeet/web_run_assets/index.html pkg/yeet/web_run_assets_test.go
```

## Task 5: Implement Workload State And Draft Mapping In JavaScript

**Files:**
- Modify: `pkg/yeet/web_run_assets/app.js`
- Modify: `pkg/yeet/web_run_assets_test.go`

- [ ] **Step 1: Add JavaScript contract snippets to the asset test**

In `pkg/yeet/web_run_assets_test.go`, add these snippets to the app hook list:

```go
"const workloadDefinitions =",
"function selectedWorkload()",
"function syncWorkloadUI()",
"function workloadPayloadKind(workload)",
"function sourcePayloadForWorkload(workload)",
"function defaultNetworkModesForWorkload(workload)",
"function renderVMCatalog(images)",
"data.command",
"cron: {",
"schedule:",
"manualVMSource",
"vmCatalog",
```

- [ ] **Step 2: Run the static asset test to verify it fails**

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestWebRunAssetsExposeFirstDeployFields -count=1
```

Expected: fail because the new JavaScript hooks do not exist.

- [ ] **Step 3: Add workload helpers**

In `pkg/yeet/web_run_assets/app.js`, add near the current helper functions:

```js
state.workload = "";
state.networkSelections = {};

const workloadDefinitions = {
  compose: {
    payloadKind: "compose",
    payloadLabel: "Compose file",
    payloadHelp: "A compose.yml or docker-compose.yml file in this project.",
    sourceHint: "Choose a Compose file.",
    placeholder: "compose.yml",
    networkModes: ["host", "svc", "ts", "lan"],
    defaultModes: [],
  },
  vm: {
    payloadKind: "vm",
    payloadLabel: "VM image",
    payloadHelp: "A managed catalog VM image, or a manual vm:// reference under advanced.",
    sourceHint: "Choose a catalog image.",
    placeholder: "vm://ubuntu/26.04",
    networkModes: ["svc", "lan"],
    defaultModes: ["svc"],
  },
  dockerfile: {
    payloadKind: "dockerfile",
    payloadLabel: "Dockerfile",
    payloadHelp: "Dockerfile to build and deploy.",
    sourceHint: "Choose a Dockerfile.",
    placeholder: "Dockerfile",
    networkModes: ["host", "svc", "ts", "lan"],
    defaultModes: [],
  },
  "remote-image": {
    payloadKind: "remote-image",
    payloadLabel: "Image",
    payloadHelp: "Container image reference such as ghcr.io/example/app:latest.",
    sourceHint: "Enter an image reference.",
    placeholder: "ghcr.io/example/app:latest",
    networkModes: ["host", "svc", "ts", "lan"],
    defaultModes: [],
  },
  file: {
    payloadKind: "file",
    payloadLabel: "Binary/script",
    payloadHelp: "Local binary or script to upload and run.",
    sourceHint: "Choose a local executable or script.",
    placeholder: "./run.sh",
    networkModes: ["host", "svc", "ts", "lan"],
    defaultModes: [],
  },
  cron: {
    payloadKind: "cron",
    payloadLabel: "Job file",
    payloadHelp: "Local binary or script to install as a scheduled job.",
    sourceHint: "Choose a job file and schedule.",
    placeholder: "./job.sh",
    networkModes: ["host"],
    defaultModes: [],
  },
};

function selectedWorkload() {
  return document.querySelector("input[name='workload']:checked")?.value || "compose";
}

function workloadDefinition(workload = selectedWorkload()) {
  return workloadDefinitions[workload] || workloadDefinitions.compose;
}

function workloadPayloadKind(workload) {
  return workloadDefinition(workload).payloadKind;
}

function defaultNetworkModesForWorkload(workload) {
  return [...workloadDefinition(workload).defaultModes];
}

function sourcePayloadForWorkload(workload) {
  if (workload === "vm") {
    const manual = $("manualVMSource").value.trim();
    return manual || $("vmCatalog").value.trim();
  }
  return $("payload").value.trim();
}
```

- [ ] **Step 4: Update draft construction**

Replace the payload-kind part of `buildDraft()` with workload-aware values:

```js
  const workload = selectedWorkload();
  const payload = sourcePayloadForWorkload(workload);
  const payloadKind = workloadPayloadKind(workload);
  const vmPayload = payloadKind === "vm";
  const cronPayload = payloadKind === "cron";
```

Set these top-level draft fields:

```js
    payload,
    payloadKind,
    cron: cronPayload ? {
      schedule: $("cronSchedule").value.trim(),
    } : {},
```

Change payload args so VM stays empty and cron keeps args:

```js
    payloadArgs: vmPayload ? [] : splitArgs($("payloadArgs").value),
```

Change network fields to avoid sending run-only network fields for cron:

```js
    network: cronPayload ? {} : {
      modes,
      tsVersion: hasTailscale ? $("tsVersion").value.trim() : "",
      tsExitNode: hasTailscale ? $("tsExitNode").value.trim() : "",
      tsTags: hasTailscale ? splitCSV($("tsTags").value) : [],
      tsAuthKey: hasTailscale ? $("tsAuthKey").value : "",
      macvlanMac: hasLAN ? $("macvlanMac").value.trim() : "",
      macvlanVlan: hasLAN ? Number.parseInt($("macvlanVlan").value, 10) || 0 : 0,
      macvlanParent: hasLAN ? $("macvlanParent").value.trim() : "",
      publish: splitCSV($("publish").value),
      restart,
    },
```

Change storage and snapshots to avoid sending unsupported cron values:

```js
    storage: cronPayload ? {} : {
      serviceRoot: $("serviceRoot").value.trim(),
      zfs: $("zfs").checked,
    },
    snapshots: cronPayload ? {} : {
      mode: $("snapshots").value,
      keepLast: Number.parseInt($("snapshotKeepLast").value, 10) || 0,
      maxAge: $("snapshotMaxAge").value.trim(),
      required: snapshotRequired,
      events: splitCSV($("snapshotEvents").value),
    },
```

- [ ] **Step 5: Add catalog rendering and workload UI sync**

Add:

```js
function renderVMCatalog(images) {
  const rows = images.length ? images : [{ payload: "vm://ubuntu/26.04", label: "Ubuntu 26.04" }];
  $("vmCatalog").replaceChildren(...rows.map((image) => option(image.label, image.payload)));
}

function syncWorkloadUI() {
  const workload = selectedWorkload();
  const def = workloadDefinition(workload);
  const previousWorkload = state.workload;
  const workloadChanged = previousWorkload !== workload;
  if (previousWorkload && workloadChanged) {
    state.networkSelections[previousWorkload] = selectedNetworkModes();
  }
  state.workload = workload;
  const isVM = workload === "vm";
  const isCron = workload === "cron";
  $("sourceHint").textContent = def.sourceHint;
  $("payloadLabel").firstChild.textContent = `${def.payloadLabel} `;
  $("payloadLabel").querySelector(".help").dataset.help = def.payloadHelp;
  $("payload").placeholder = def.placeholder;
  $("payload").closest("label").hidden = isVM;
  $("vmCatalogBlock").hidden = !isVM;
  $("cronScheduleField").hidden = !isCron;
  $("envFile").closest("label").hidden = isVM || isCron;
  $("publish").closest("label").hidden = isVM || isCron;
  $("serviceRoot").disabled = isCron;
  $("zfs").disabled = isCron;
  $("snapshots").closest("details").hidden = isCron;
  $("storageModeLabel").firstChild.textContent = $("zfs").checked ? "ZFS dataset " : "Service root ";
  $("zfsHelp").textContent = $("zfs").checked ? "Using ZFS dataset" : "ZFS dataset";
  if (workloadChanged) {
    renderNetworkModes(def.networkModes.filter((mode) => mode !== "host"));
    applyDefaultNetworkModes(workload);
  }
}

function applyDefaultNetworkModes(workload) {
  const defaults = state.networkSelections[workload] || defaultNetworkModesForWorkload(workload);
  document.querySelectorAll("input[name='net']").forEach((input) => {
    input.checked = defaults.includes(input.value);
  });
}
```

Update `bootstrap()` so it renders the catalog:

```js
  renderVMCatalog(state.bootstrap.options?.vmImages || []);
```

Update `update()` so it calls workload sync before network sync:

```js
  syncWorkloadUI();
  syncNetworkUI();
```

Add a change listener:

```js
document.querySelectorAll("input[name='workload']").forEach((input) => {
  input.addEventListener("change", update);
});
```

- [ ] **Step 6: Use backend command preview**

Change `validate(draft)` so after decoding JSON it updates command preview from the backend:

```js
    const data = await res.json();
    if (data.command) $("commandPreview").textContent = data.command;
```

Keep the local `updatePreview(draft)` call in `update()` as a fast provisional preview while validation is pending.

- [ ] **Step 7: Run static asset tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestWebRunAssetsExposeFirstDeployFields|TestWebRunAssetsRecognizeAllVMPayloads' -count=1
```

Expected: pass.

- [ ] **Step 8: Commit**

Run:

```bash
git add pkg/yeet/web_run_assets/app.js pkg/yeet/web_run_assets_test.go
git commit -m "yeet: map web run workloads to drafts" -- pkg/yeet/web_run_assets/app.js pkg/yeet/web_run_assets_test.go
```

## Task 6: Polish Workload Builder Layout And Keyboard Behavior

**Files:**
- Modify: `pkg/yeet/web_run_assets/styles.css`
- Modify: `pkg/yeet/web_run_assets/index.html`
- Modify: `pkg/yeet/web_run_assets_test.go`

- [ ] **Step 1: Add static checks for layout hooks**

Read `styles.css` near the existing `index.html` and `app.js` reads in `TestWebRunAssetsExposeFirstDeployFields`:

```go
styles, err := fs.ReadFile(webRunAssets, "web_run_assets/styles.css")
if err != nil {
	t.Fatalf("read styles: %v", err)
}
```

Then add these style snippets to `TestWebRunAssetsExposeFirstDeployFields`:

```go
for _, snippet := range []string{
	".workload-selector",
	".workload-option",
	".source-head",
	".catalog-block",
	".subsection-label",
	".deploy-settings-grid",
} {
	if !strings.Contains(string(styles), snippet) {
		t.Fatalf("styles missing %s", snippet)
	}
}
```

- [ ] **Step 2: Run the asset test to verify it fails**

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestWebRunAssetsExposeFirstDeployFields -count=1
```

Expected: fail because the new style hooks are absent.

- [ ] **Step 3: Add layout CSS**

Append this block to `pkg/yeet/web_run_assets/styles.css` before the media queries:

```css
.workload-selector {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px;
  margin: 14px 0;
  padding: 12px;
}

.workload-option {
  min-width: 0;
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  align-items: center;
  gap: 8px;
  margin: 0;
  padding: 9px 10px;
  border: 1px solid var(--line);
  border-radius: 8px;
  color: var(--text);
  background: oklch(0.15 0.012 165);
}

.workload-option:has(input:checked) {
  border-color: var(--brand);
  background: color-mix(in oklch, var(--brand) 14%, var(--surface));
}

.workload-option input {
  width: 16px;
  min-height: 16px;
}

.source-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  margin-top: 12px;
}

.source-head h2 {
  font-size: 16px;
}

.source-head span {
  color: var(--quiet);
  font-size: 13px;
  text-align: right;
}

.catalog-block {
  margin: 12px 0;
}

.subsection-label {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  margin: 14px 0 4px;
  color: var(--text);
  font-size: 14px;
  font-weight: 700;
}

.deploy-settings-grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 12px;
}
```

Inside the existing narrow viewport media query, add:

```css
  .workload-selector {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
```

- [ ] **Step 4: Add deploy settings wrapper if needed**

In `pkg/yeet/web_run_assets/index.html`, wrap the network fieldset and storage root control in:

```html
          <div class="deploy-settings-grid">
```

and close it before the Tailscale conditional block. Keep Tailscale, LAN, VM shape, snapshots, and payload args below that wrapper so advanced/conditional controls do not resize the main network/storage rows.

- [ ] **Step 5: Run asset tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestWebRunAssetsExposeFirstDeployFields -count=1
```

Expected: pass.

- [ ] **Step 6: Commit**

Run:

```bash
git add pkg/yeet/web_run_assets/styles.css pkg/yeet/web_run_assets/index.html pkg/yeet/web_run_assets_test.go
git commit -m "yeet: polish web run builder layout" -- pkg/yeet/web_run_assets/styles.css pkg/yeet/web_run_assets/index.html pkg/yeet/web_run_assets_test.go
```

## Task 7: Document The Redesigned Web Run Flow

**Files:**
- Modify: `website/docs/cli/yeet-cli.mdx`

- [ ] **Step 1: Inspect the current web-run docs section**

Run:

```bash
rg -n "web|--web|browser" website/docs/cli/yeet-cli.mdx
```

Expected: output includes the current `yeet run --web` documentation lines.

- [ ] **Step 2: Update the web-run section**

Replace the current `yeet run --web` paragraph in `website/docs/cli/yeet-cli.mdx` with:

```mdx
### Web new-service builder

`yeet run --web` opens a localhost-only browser UI for creating a new service.
It is a faster way to build the first deploy command when you want to choose a
workload type, network mode, storage root, ZFS dataset, or VM catalog image
without writing every flag by hand.

Supported workload types:

- Compose app.
- Virtual machine from the yeet VM image catalog.
- Dockerfile.
- Container image.
- Binary or script.
- Scheduled job with a five-field cron expression.

The web builder is for new services only. Existing service changes still use the
normal CLI commands such as `yeet run`, `yeet service set`, `yeet vm set`,
`yeet cron`, `yeet logs`, and `yeet status`.

Network and storage controls are visible in the builder because they are common
first-deploy choices. VM deploys default to `svc` networking, and `svc,lan` is
the recommended choice when you want LAN presence plus a reliable yeet-managed
SSH path. ZFS mode changes the service-root field from a filesystem path to a
dataset name.

```bash
yeet run --web
yeet run --web <svc>
yeet run --web <svc> <payload>
```
```

- [ ] **Step 3: Run docs-focused checks**

Run:

```bash
mise exec -- pre-commit run --files website/docs/cli/yeet-cli.mdx
```

Expected: pass.

- [ ] **Step 4: Commit website docs in the submodule**

Run:

```bash
git -C website add docs/cli/yeet-cli.mdx
git -C website commit -m "docs: document web run workload builder" -- docs/cli/yeet-cli.mdx
```

Expected: website commit succeeds.

- [ ] **Step 5: Commit root submodule pointer**

Run:

```bash
git add website
git commit -m "docs: update web run manual" -- website
```

Expected: root commit records only the website submodule pointer.

## Task 8: Final Verification

**Files:**
- No planned edits.

- [ ] **Step 1: Run targeted Go tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunWeb|TestWebRunAssets|TestRunDraftCron|TestValidateRunDraftCron|TestRunDraftCommandPreview' -count=1
```

Expected: pass.

- [ ] **Step 2: Run full Go test suite**

Run:

```bash
mise exec -- go test ./... -count=1
```

Expected: pass.

- [ ] **Step 3: Run pre-commit**

Run:

```bash
mise exec -- pre-commit run --all-files
```

Expected: pass.

- [ ] **Step 4: Run the web UI locally**

Run:

```bash
mise exec -- go run ./cmd/yeet run --web
```

Expected: terminal prints `Opening http://127.0.0.1:<port>/...`.

Manual browser checks:

- Tab order starts at service, host, workload, then source fields.
- Compose is selected by default.
- Network and ZFS controls are visible without opening advanced sections.
- VM workload shows catalog images and VM shape fields.
- VM workload defaults to `svc` networking and does not allow `ts`.
- Cron workload shows schedule and payload args, and disables run-only network/ZFS controls.
- Validation updates the command preview from the backend response.

- [ ] **Step 5: Stop the local web run server**

Use `Ctrl-C` in the terminal running `yeet run --web`.

Expected: process exits cleanly.

- [ ] **Step 6: Confirm workspace state**

Run:

```bash
git status --short
git -C website status --short
```

Expected: the root repo and `website` submodule are clean, except for any explicitly deferred local work the user has asked not to commit.
