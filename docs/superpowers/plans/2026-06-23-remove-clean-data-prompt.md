# Remove Clean Data Prompt Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a default-no interactive data-deletion prompt to `yeet rm <svc>` while preserving the existing non-interactive `--yes` and `--clean-data` contracts.

**Architecture:** Keep the behavior on the catch side, where remote remove prompts, runner cleanup, and `RemoveServiceWithOptions` already live. Add a small helper that upgrades a local `cli.RemoveFlags` value to `CleanData=true` only when the user confirms the new data prompt, then let the existing cleanup path handle data deletion. Update CLI-facing docs and help text so automation users know `--yes` does not imply `--clean-data`.

**Tech Stack:** Go, `cmdutil.Confirm`, catch TTY exec tests, `mise exec -- go test`, Docusaurus MDX docs, GitButler (`but`) for commits.

---

## File Map

- Modify `pkg/catch/tty_service.go`: add the data prompt helper and call it after the existing removal confirmation succeeds.
- Modify `pkg/catch/remove_test.go`: cover prompt sequencing and data preservation/deletion through `removeCmdFunc`.
- Modify `pkg/catch/tty_service_test.go`: cover VM-specific prompt labeling on the new helper.
- Modify `pkg/cli/cli.go`: clarify remove flag help for `--yes` and `--clean-data`.
- Modify `website/docs/cli/yeet-cli.mdx`: document the new interactive prompt and automation contract.

Do not modify `pkg/yeet/svc_cmd.go` unless tests show the client wrapper is forwarding args incorrectly. The approved design keeps local `yeet.toml` cleanup separate from remote service-data cleanup.

---

### Task 1: Write Failing Catch Remove Prompt Tests

**Files:**
- Modify: `pkg/catch/remove_test.go`
- Modify: `pkg/catch/tty_service_test.go`

- [ ] **Step 1: Add data prompt integration tests to `pkg/catch/remove_test.go`**

Append these helpers and tests near the existing remove command tests:

```go
func seedRemovePromptService(t *testing.T, server *Server, name string, serviceType db.ServiceType) string {
	t.Helper()
	serviceRoot := filepath.Join(server.cfg.ServicesRoot, name)
	for _, dir := range []string{"bin", "data", "env", "run"} {
		if err := os.MkdirAll(filepath.Join(serviceRoot, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(serviceRoot, "data", "state.txt"), []byte("state"), 0o644); err != nil {
		t.Fatalf("write data: %v", err)
	}
	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		if d.Services == nil {
			d.Services = map[string]*db.Service{}
		}
		d.Services[name] = &db.Service{Name: name, ServiceType: serviceType}
		return nil
	}); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	return serviceRoot
}

func TestRemoveCmdDataPromptDefaultsNo(t *testing.T) {
	server := newTestServer(t)
	name := "svc-remove-data-default"
	serviceRoot := seedRemovePromptService(t, server, name, db.ServiceType("unknown"))

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  name,
		rw: readWriter{
			Reader: strings.NewReader("y\n\n"),
			Writer: &out,
		},
		serviceRunnerFn: func() (ServiceRunner, error) {
			return &fakeRunner{}, nil
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{}); err != nil {
		t.Fatalf("removeCmdFunc: %v", err)
	}
	if got := out.String(); !strings.Contains(got, `Delete all data for service "svc-remove-data-default"?`) {
		t.Fatalf("output = %q, want data prompt", got)
	}
	if _, err := os.Stat(filepath.Join(serviceRoot, "data", "state.txt")); err != nil {
		t.Fatalf("data should remain after default-no prompt: %v", err)
	}
	if _, err := server.serviceView(name); !errors.Is(err, errServiceNotFound) {
		t.Fatalf("serviceView error = %v, want service not found", err)
	}
}

func TestRemoveCmdDataPromptCanEnableCleanData(t *testing.T) {
	server := newTestServer(t)
	name := "svc-remove-data-yes"
	serviceRoot := seedRemovePromptService(t, server, name, db.ServiceType("unknown"))

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  name,
		rw: readWriter{
			Reader: strings.NewReader("y\ny\n"),
			Writer: &out,
		},
		serviceRunnerFn: func() (ServiceRunner, error) {
			return &fakeRunner{}, nil
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{}); err != nil {
		t.Fatalf("removeCmdFunc: %v", err)
	}
	if got := out.String(); !strings.Contains(got, `Delete all data for service "svc-remove-data-yes"?`) {
		t.Fatalf("output = %q, want data prompt", got)
	}
	if _, err := os.Stat(serviceRoot); !os.IsNotExist(err) {
		t.Fatalf("service root stat err = %v, want not exist", err)
	}
}

func TestRemoveCmdCleanDataSkipsDataPrompt(t *testing.T) {
	server := newTestServer(t)
	name := "svc-remove-clean-data"
	serviceRoot := seedRemovePromptService(t, server, name, db.ServiceType("unknown"))

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  name,
		rw: readWriter{
			Reader: strings.NewReader("y\n"),
			Writer: &out,
		},
		serviceRunnerFn: func() (ServiceRunner, error) {
			return &fakeRunner{}, nil
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{CleanData: true}); err != nil {
		t.Fatalf("removeCmdFunc: %v", err)
	}
	if got := out.String(); strings.Contains(got, "Delete all data") {
		t.Fatalf("output = %q, want no data prompt", got)
	}
	if _, err := os.Stat(serviceRoot); !os.IsNotExist(err) {
		t.Fatalf("service root stat err = %v, want not exist", err)
	}
}

func TestRemoveCmdYesSkipsDataPromptAndPreservesData(t *testing.T) {
	server := newTestServer(t)
	name := "svc-remove-yes-preserve-data"
	serviceRoot := seedRemovePromptService(t, server, name, db.ServiceType("unknown"))

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  name,
		rw: readWriter{
			Reader: strings.NewReader(""),
			Writer: &out,
		},
		serviceRunnerFn: func() (ServiceRunner, error) {
			return &fakeRunner{}, nil
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{Yes: true}); err != nil {
		t.Fatalf("removeCmdFunc: %v", err)
	}
	if got := out.String(); strings.Contains(got, "Are you sure") || strings.Contains(got, "Delete all data") {
		t.Fatalf("output = %q, want no prompts", got)
	}
	if _, err := os.Stat(filepath.Join(serviceRoot, "data", "state.txt")); err != nil {
		t.Fatalf("data should remain with --yes and no --clean-data: %v", err)
	}
}
```

- [ ] **Step 2: Add the VM label test to `pkg/catch/tty_service_test.go`**

Append this test near `TestConfirmVMRemovalUsesVMLabel`:

```go
func TestConfirmRemoveDataUsesVMLabel(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "devbox", db.ServiceTypeVM, nil)

	var out bytes.Buffer
	execer := &ttyExecer{
		s:  server,
		sn: "devbox",
		rw: readWriter{Reader: strings.NewReader("n\n"), Writer: &out},
	}

	flags, err := execer.confirmRemoveData(cli.RemoveFlags{})
	if err != nil {
		t.Fatalf("confirmRemoveData returned error: %v", err)
	}
	if flags.CleanData {
		t.Fatal("CleanData = true, want false")
	}
	if got := out.String(); !strings.Contains(got, `Delete all data for VM "devbox"?`) {
		t.Fatalf("prompt output = %q, want VM data prompt", got)
	}
}
```

- [ ] **Step 3: Run the focused tests and confirm they fail for the missing helper/behavior**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRemoveCmd(DataPromptDefaultsNo|DataPromptCanEnableCleanData|CleanDataSkipsDataPrompt|YesSkipsDataPromptAndPreservesData)|TestConfirmRemoveDataUsesVMLabel' -count=1
```

Expected: FAIL. `TestConfirmRemoveDataUsesVMLabel` should fail to compile because `confirmRemoveData` does not exist yet. The remove command tests should also fail until the data prompt is wired into `removeCmdFunc`.

---

### Task 2: Implement the Catch-Side Data Prompt

**Files:**
- Modify: `pkg/catch/tty_service.go`

- [ ] **Step 1: Add the prompt helper below `confirmServiceRemoval`**

Insert this function in `pkg/catch/tty_service.go` after `confirmServiceRemoval`:

```go
func (e *ttyExecer) confirmRemoveData(flags cli.RemoveFlags) (cli.RemoveFlags, error) {
	if flags.Yes || flags.CleanData {
		return flags, nil
	}
	ok, err := cmdutil.Confirm(e.rw, e.rw, fmt.Sprintf("Delete all data for %s %q?", e.managedTargetLabel(), e.sn))
	if err != nil {
		return flags, fmt.Errorf("failed to confirm data removal: %w", err)
	}
	if ok {
		flags.CleanData = true
	}
	return flags, nil
}
```

- [ ] **Step 2: Call the helper after the existing removal confirmation succeeds**

Change `removeCmdFunc` from:

```go
	doneConfirm := e.traceBlock("remove confirm")
	ok, err := e.confirmServiceRemoval(flags.Yes)
	doneConfirm()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	doneRunnerRemove := e.traceBlock("remove runner")
	e.removeRunner(runner)
```

to:

```go
	doneConfirm := e.traceBlock("remove confirm")
	ok, err := e.confirmServiceRemoval(flags.Yes)
	doneConfirm()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	doneDataConfirm := e.traceBlock("remove data confirm")
	flags, err = e.confirmRemoveData(flags)
	doneDataConfirm()
	if err != nil {
		return err
	}

	doneRunnerRemove := e.traceBlock("remove runner")
	e.removeRunner(runner)
```

- [ ] **Step 3: Format the touched Go files**

Run:

```bash
mise exec -- gofmt -w pkg/catch/tty_service.go pkg/catch/remove_test.go pkg/catch/tty_service_test.go
```

Expected: no command output.

- [ ] **Step 4: Run the focused tests and confirm they pass**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRemoveCmd(DataPromptDefaultsNo|DataPromptCanEnableCleanData|CleanDataSkipsDataPrompt|YesSkipsDataPromptAndPreservesData)|TestConfirmRemoveDataUsesVMLabel' -count=1
```

Expected: PASS.

- [ ] **Step 5: Run the full catch package tests**

Run:

```bash
mise exec -- go test ./pkg/catch -count=1
```

Expected: PASS.

---

### Task 3: Update CLI Help Text and User Manual

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Verify: `website/docs/payloads/containers.mdx`
- Verify: `website/docs/payloads/binaries.mdx`
- Verify: `website/docs/payloads/cron-jobs.mdx`
- Verify: `website/docs/payloads/vms.mdx`

- [ ] **Step 1: Clarify remove flag help in `pkg/cli/cli.go`**

Change `removeFlagsParsed` from:

```go
type removeFlagsParsed struct {
	Yes         bool `flag:"yes" short:"y" help:"Skip the removal prompt"`
	CleanConfig bool `flag:"clean-config" help:"Delete the matching yeet.toml entry without prompting"`
	CleanData   bool `flag:"clean-data" help:"Delete service data instead of preserving data/"`
}
```

to:

```go
type removeFlagsParsed struct {
	Yes         bool `flag:"yes" short:"y" help:"Skip removal prompts; does not imply --clean-data"`
	CleanConfig bool `flag:"clean-config" help:"Delete the matching yeet.toml entry without prompting"`
	CleanData   bool `flag:"clean-data" help:"Delete service data instead of preserving data/ or prompting interactively"`
}
```

- [ ] **Step 2: Update the CLI manual in `website/docs/cli/yeet-cli.mdx`**

Replace the current remove flag block:

```mdx
- `-y`/`--yes`: skip the removal prompt.
- `--clean-config`: delete the `yeet.toml` entry for the service. Without this
  flag, yeet prompts to keep or remove the local config after the service is
  removed.
- `--clean-data`: delete service data, including VM guest disks. Without this
  flag, yeet preserves `data/`.
```

with:

```mdx
- `-y`/`--yes`: skip removal prompts. This does not imply `--clean-data`.
- `--clean-config`: delete the `yeet.toml` entry for the service. Without this
  flag, yeet prompts to keep or remove the local config after the service is
  removed.
- `--clean-data`: delete service data, including VM guest disks. Without this
  flag, interactive removals ask whether to delete data and default to no;
  non-interactive removals preserve `data/`.
```

- [ ] **Step 3: Add one explanatory sentence after the remove flag list**

Immediately after the flag list in `website/docs/cli/yeet-cli.mdx`, add:

```mdx
For scripted cleanup of disposable services, pass both `--yes` and
`--clean-data` when you want prompts skipped and data deleted.
```

- [ ] **Step 4: Audit payload cleanup docs for conflicting wording**

Run:

```bash
rg -n "yeet rm|--clean-data|--yes" website/docs/payloads website/docs/getting-started website/docs/operations README.md
```

Expected: examples that intentionally delete disposable test data still use
`--clean-data`. Existing payload docs should remain valid because they already
describe `--clean-data` as the explicit data-deletion flag. If the audit finds a
sentence that says plain `yeet rm` or `--yes` deletes data, stop and update only
that sentence to match the CLI manual wording from Step 2.

- [ ] **Step 5: Format Go help text changes**

Run:

```bash
mise exec -- gofmt -w pkg/cli/cli.go
```

Expected: no command output.

---

### Task 4: Verify Parser, Help, Docs, and Full Go Suite

**Files:**
- Verify: `pkg/cli/cli.go`
- Verify: `cmd/yeet/cli_test.go`
- Verify: `pkg/catch/tty_service.go`
- Verify: `website/docs/cli/yeet-cli.mdx`

- [ ] **Step 1: Run CLI parser and help tests**

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet -count=1
```

Expected: PASS. The existing help tests should keep passing because they check for the flag names, not the exact help wording.

- [ ] **Step 2: Run client remove routing tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestHandleSvcCmdRemoveRoutes -count=1
```

Expected: PASS. This confirms local `--clean-config` filtering and `--yes` forwarding still behave as before.

- [ ] **Step 3: Run catch tests**

Run:

```bash
mise exec -- go test ./pkg/catch -count=1
```

Expected: PASS.

- [ ] **Step 4: Run docs whitespace check**

Run:

```bash
git -C website diff --check
```

Expected: no output.

- [ ] **Step 5: Run the full Go suite**

Run:

```bash
mise exec -- go test ./... -count=1
```

Expected: PASS.

---

### Task 5: Commit the Implementation and Docs

**Files:**
- Commit Go implementation and tests in the root repo.
- Commit website docs inside the `website/` submodule if any website files changed.
- Commit the root `website` gitlink only after the website commit is pushed and the user has explicitly authorized finishing/pushing the root repo.

- [ ] **Step 1: Inspect root workspace state**

Run:

```bash
but status -fv
```

Expected: implementation changes are assigned to the current session branch or appear as unassigned changes. Ignore unrelated `.codex/skills/gitbutler/*` changes from the automatic GitButler skill update unless the user explicitly asks to manage them.

- [ ] **Step 2: If website docs changed, commit inside the website submodule**

Run:

```bash
git -C website status --short --branch
git -C website diff --check
```

Expected: only intended docs files are modified and `diff --check` is clean.

If docs changed, commit in the website repository with:

```bash
git -C website add docs/cli/yeet-cli.mdx docs/payloads/containers.mdx docs/payloads/binaries.mdx docs/payloads/cron-jobs.mdx docs/payloads/vms.mdx
git -C website commit -m "docs: clarify remove data prompt"
```

If only `docs/cli/yeet-cli.mdx` changed, stage only that file:

```bash
git -C website add docs/cli/yeet-cli.mdx
git -C website commit -m "docs: clarify remove data prompt"
```

Do not push the website commit or root gitlink unless the user authorizes push/finish work.

- [ ] **Step 3: Inspect root diff and commit only this session's root changes**

Run:

```bash
but diff
but status -fv
```

Expected: the root diff contains only the committed spec/plan, Go
implementation/tests, CLI help text, and possibly the `website` gitlink. Use
the change IDs from the current `but status -fv` output for files that belong to
this task. The commit message should be:

```text
rm: prompt before deleting service data
```

Do not include unrelated `.codex/skills/gitbutler/*` change IDs in the commit.

- [ ] **Step 4: Report local commit state without pushing**

Run:

```bash
but status -fv
```

Expected: the session branch has the new implementation commit and no uncommitted changes from this task. If unrelated `.codex/skills/gitbutler/*` changes remain unassigned, mention that they were left untouched.

---

## Self-Review Notes

- Spec coverage: Tasks 1 and 2 cover interactive default-no, explicit data deletion, `--clean-data`, `--yes`, and VM labels. Task 3 covers user-facing docs. Task 4 covers package and full-suite verification.
- Placeholder scan: Angle-bracket tokens only appear in user-facing CLI syntax such as `yeet rm <svc>`. Dynamic GitButler change IDs are described procedurally because hardcoding those IDs would be incorrect.
- Type consistency: The plan uses existing `cli.RemoveFlags`, `cmdutil.Confirm`, `ttyExecer`, `RemoveServiceWithOptions`, `readWriter`, `fakeRunner`, `newTestServer`, and `seedService` names already present in the repo.
