# MicroVM CLI and Web Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make experimental VM services behave consistently across yeet's CLI and web-run workflows, including status, run output, remove cleanup, SSH, console, docs, and verification.

**Architecture:** Keep VM behavior inside the existing service lifecycle instead of adding a parallel command surface. Catch remains authoritative for VM provisioning, status, and cleanup; the client and web runner only need to understand VM payloads, VM-only flags, and the correct follow-up commands.

**Tech Stack:** Go, existing yeet RPC/TTY bridge, catch service runner interfaces, JSON DB service roots, Firecracker VM runner, `pkg/yeet` web-run assets, README and website docs.

---

## File Structure

- Modify `pkg/catch/vm_runner.go`: quiet VM status probes and make VM systemd commands safe when catch has prewired stdout/stderr for RPC streaming.
- Modify `pkg/catch/vm_runner_test.go`: regression coverage for quiet `systemctl is-active --quiet` and remove commands with prewired command output.
- Modify `pkg/catch/catch.go`: add remove options so default removal preserves data and `--clean-data` removes it, including recursive ZFS dataset cleanup when a service root is ZFS-backed.
- Modify `pkg/catch/catch_test.go` and `pkg/catch/remove_test.go`: preserve-data and clean-data regression tests.
- Modify `pkg/cli/cli.go` and `pkg/cli/cli_test.go`: add `--clean-data` to `yeet remove` parsing and help metadata.
- Modify `pkg/yeet/run_changes.go` and `pkg/yeet/svc_cmd.go`: keep `--clean-data` remote while filtering only client-local `--clean-config`.
- Modify `pkg/catch/vm_provision.go` and `pkg/catch/vm_provision_test.go`: emit VM run progress and post-run next commands.
- Modify `pkg/yeet/web_run_assets/*` and `pkg/yeet/run_web_*`: expose VM payload and VM flags in the web run flow, while keeping non-VM payload validation unchanged.
- Modify docs: `README.md`, `website/docs/cli/yeet-cli.mdx`, `website/docs/concepts/service-types.mdx`, and `.codex/skills/yeet-cli/references/yeet-help-llm.md`.

## Task 1: Fix VM Status and Remove Command Plumbing

- [ ] **Step 1: Write failing VM runner tests**

Add tests in `pkg/catch/vm_runner_test.go`:

```go
func TestVMRunnerStatusUsesQuietSystemctl(t *testing.T) {
	runner, calls := newRecordingVMRunner("devbox")

	status, err := runner.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status != svc.StatusRunning {
		t.Fatalf("Status = %q, want running", status)
	}

	want := [][]string{{"systemctl", "is-active", "--quiet", "yeet-vm-devbox.service"}}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls = %#v, want %#v", *calls, want)
	}
}

func TestVMRunnerSystemctlIgnoresPrewiredCommandOutput(t *testing.T) {
	runner := &vmRunner{name: "devbox"}
	runner.SetNewCmd(func(string, ...string) *exec.Cmd {
		cmd := exec.Command("true")
		cmd.Stdout = &bytes.Buffer{}
		cmd.Stderr = &bytes.Buffer{}
		return cmd
	})

	if err := runner.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}
```

- [ ] **Step 2: Verify the tests fail**

Run:

```bash
go test ./pkg/catch -run 'TestVMRunner(StatusUsesQuietSystemctl|SystemctlIgnoresPrewiredCommandOutput)' -count=1
```

Expected before implementation: one test records `systemctl is-active yeet-vm-devbox.service` without `--quiet`; the other fails with `exec: Stdout already set`.

- [ ] **Step 3: Implement quiet and prewired-safe VM systemctl**

Update `pkg/catch/vm_runner.go`:

```go
func (r *vmRunner) systemctl(args ...string) error {
	cmd := r.command("systemctl", args...)
	clearCommandOutput(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl %v failed: %w\n%s", args, err, string(out))
	}
	return nil
}

func clearCommandOutput(cmd *exec.Cmd) {
	cmd.Stdout = nil
	cmd.Stderr = nil
}

func (r *vmRunner) Status() (svc.Status, error) {
	cmd := r.command("systemctl", "is-active", "--quiet", r.unit())
	clearCommandOutput(cmd)
	if err := cmd.Run(); err != nil {
		return svc.StatusStopped, nil
	}
	return svc.StatusRunning, nil
}
```

- [ ] **Step 4: Verify the targeted tests pass**

Run:

```bash
go test ./pkg/catch -run 'TestVMRunner(StatusUsesQuietSystemctl|SystemctlIgnoresPrewiredCommandOutput)' -count=1
```

Expected: PASS.

## Task 2: Add Explicit Data Cleanup for Remove

- [ ] **Step 1: Write failing parser and cleanup tests**

Add to `pkg/cli/cli_test.go`:

```go
func TestParseRemoveFlags(t *testing.T) {
	flags, outArgs, err := ParseRemove([]string{"-y", "--clean-config", "--clean-data"})
	if err != nil {
		t.Fatalf("ParseRemove failed: %v", err)
	}
	if !flags.Yes || !flags.CleanConfig || !flags.CleanData {
		t.Fatalf("flags = %#v, want yes clean-config clean-data", flags)
	}
	if len(outArgs) != 0 {
		t.Fatalf("expected no args, got %v", outArgs)
	}
}
```

Add to `pkg/catch/catch_test.go`:

```go
func TestRemoveServiceCleanDataRemovesDataDir(t *testing.T) {
	server := newTestServer(t)
	serviceRoot := filepath.Join(server.cfg.ServicesRoot, "api")
	for _, dir := range []string{"bin", "data", "env", "run"} {
		if err := os.MkdirAll(filepath.Join(serviceRoot, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(serviceRoot, "data", "rootfs.raw"), []byte("disk"), 0o644); err != nil {
		t.Fatalf("write disk: %v", err)
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"api": {Name: "api", ServiceType: db.ServiceType("unknown")},
	}}); err != nil {
		t.Fatalf("DB.Set: %v", err)
	}

	if _, err := server.RemoveServiceWithOptions("api", RemoveOptions{CleanData: true}); err != nil {
		t.Fatalf("RemoveServiceWithOptions: %v", err)
	}
	for _, removed := range []string{"bin", "data", "env", "run"} {
		if _, err := os.Stat(filepath.Join(serviceRoot, removed)); !os.IsNotExist(err) {
			t.Fatalf("%s stat err = %v, want not exist", removed, err)
		}
	}
}
```

- [ ] **Step 2: Verify the tests fail**

Run:

```bash
go test ./pkg/cli ./pkg/catch -run 'TestParseRemoveFlags|TestRemoveServiceCleanDataRemovesDataDir' -count=1
```

Expected before implementation: compile failure for `CleanData`, `RemoveOptions`, or `RemoveServiceWithOptions`.

- [ ] **Step 3: Implement `--clean-data`**

Update `pkg/cli/cli.go`:

```go
type RemoveFlags struct {
	Yes         bool
	CleanConfig bool
	CleanData   bool
}

type removeFlagsParsed struct {
	Yes         bool `flag:"yes" short:"y"`
	CleanConfig bool `flag:"clean-config"`
	CleanData   bool `flag:"clean-data"`
}
```

Update `ParseRemove`:

```go
flags := RemoveFlags{
	Yes:         parsed.Flags.Yes,
	CleanConfig: parsed.Flags.CleanConfig,
	CleanData:   parsed.Flags.CleanData,
}
```

Update `pkg/catch/catch.go`:

```go
type RemoveOptions struct {
	CleanData bool
}

func (s *Server) RemoveService(name string) (*RemoveReport, error) {
	return s.RemoveServiceWithOptions(name, RemoveOptions{})
}

func (s *Server) RemoveServiceWithOptions(name string, opts RemoveOptions) (*RemoveReport, error) {
	report := &RemoveReport{}
	s.addRunningServiceWarning(report, name)
	tsStableID := s.tailscaleStableIDForService(report, name)
	serviceRoot, err := s.serviceRootDir(name)
	removeDirs := true
	if err != nil {
		report.addWarning(fmt.Errorf("failed to resolve service root for %q: %w", name, err))
		removeDirs = false
	}
	if err := s.removeServiceFromDB(name); err != nil {
		return report, fmt.Errorf("failed to remove service from db: %w", err)
	}
	s.publishServiceDeleted(name)
	s.deleteTailscaleDevice(report, tsStableID)
	if removeDirs {
		s.removeServiceDirs(report, serviceRoot, opts.CleanData)
	}
	return report, nil
}
```

Update `pkg/catch/tty_service.go`:

```go
report, err := e.s.RemoveServiceWithOptions(e.sn, RemoveOptions{CleanData: flags.CleanData})
```

- [ ] **Step 4: Verify remove tests pass**

Run:

```bash
go test ./pkg/cli ./pkg/catch -run 'TestParseRemoveFlags|TestRemoveService' -count=1
```

Expected: PASS.

## Task 3: Add VM Run Progress and Next Commands

- [ ] **Step 1: Write failing output test**

Add to `pkg/catch/vm_provision_test.go`:

```go
func TestRunVMPrintsProgressAndNextCommands(t *testing.T) {
	server := newTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "devbox")
	var out bytes.Buffer
	execer.rw = &out

	if err := execer.runVM(cli.RunFlags{Net: "svc", CPUs: 2, Memory: "2g", Disk: "16g", Restart: true}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}

	text := out.String()
	for _, want := range []string{
		"VM devbox",
		"Image: vm://ubuntu/26.04",
		"Shape: 2 vCPU, 2.0 GB memory, 16.0 GB disk",
		"Network: svc",
		"Preparing disk",
		"Injecting guest metadata",
		"Starting VM",
		"SSH: yeet ssh devbox",
		"Console: yeet vm console devbox",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
}
```

- [ ] **Step 2: Verify the output test fails**

Run:

```bash
go test ./pkg/catch -run TestRunVMPrintsProgressAndNextCommands -count=1
```

Expected before implementation: output is empty or missing the requested lines.

- [ ] **Step 3: Add progress helpers and calls**

Add helper methods to `pkg/catch/vm_provision.go`:

```go
func (e *ttyExecer) vmProgressf(format string, args ...any) {
	if e.rw == nil {
		return
	}
	e.printf(format, args...)
}

func (e *ttyExecer) printVMProvisionSummary(plan vmProvisionPlan, payload string) {
	e.vmProgressf("VM %s\n", plan.Service)
	e.vmProgressf("  Image: %s (%s)\n", payload, plan.Image.Manifest.Version)
	e.vmProgressf("  Shape: %d vCPU, %s memory, %s disk, %s\n",
		plan.Shape.CPUs,
		formatBytes(plan.Shape.MemoryBytes),
		formatBytes(plan.Shape.DiskBytes),
		plan.Shape.DiskBackend,
	)
	if len(plan.Network.Modes()) == 0 {
		e.vmProgressf("  Network: none\n")
		return
	}
	e.vmProgressf("  Network: %s\n", strings.Join(plan.Network.Modes(), ","))
}

func (e *ttyExecer) printVMNextCommands(service string) {
	e.vmProgressf("VM %s is ready.\n", service)
	e.vmProgressf("SSH: yeet ssh %s\n", service)
	e.vmProgressf("Console: yeet vm console %s\n", service)
}
```

Call these in `provisionVM`, `applyVMProvisionArtifacts`, and `finishVMProvision` around the existing disk, metadata, network, systemd, and restart steps.

- [ ] **Step 4: Verify VM progress tests pass**

Run:

```bash
go test ./pkg/catch -run 'TestRunVM(PrintsProgressAndNextCommands|ProvisionSuccessWritesArtifactsAndDB)' -count=1
```

Expected: PASS.

## Task 4: Make Web Run VM-Aware

- [ ] **Step 1: Inspect and write web validation tests**

Add tests to the relevant `pkg/yeet/run_web_*_test.go` file so a VM draft accepts:

```json
{
  "service": "devbox",
  "host": "yeet-lab",
  "payload": "vm://ubuntu/26.04",
  "network": {"modes": ["svc"]},
  "vm": {"cpus": 4, "memory": "4g", "disk": "128g"}
}
```

The test should assert the generated command includes:

```text
run devbox vm://ubuntu/26.04 --net=svc --cpus=4 --memory=4g --disk=128g
```

- [ ] **Step 2: Verify the web test fails**

Run:

```bash
go test ./pkg/yeet -run 'TestRunWeb.*VM|TestValidateRunDraft.*VM' -count=1
```

Expected before implementation: web draft or command builder ignores VM options.

- [ ] **Step 3: Add VM controls to the web assets**

Update the web form and JS so `vm://ubuntu/26.04` shows VM CPU, memory, disk, and `svc`/`lan` network options. Hide Docker-only controls for VM payloads.

- [ ] **Step 4: Verify web tests pass**

Run:

```bash
go test ./pkg/yeet -run 'TestRunWeb|TestValidateRunDraft|TestBuildRun' -count=1
```

Expected: PASS.

## Task 5: Docs and Broad Verification

- [ ] **Step 1: Update docs**

Update:

- `README.md`
- `website/docs/cli/yeet-cli.mdx`
- `website/docs/concepts/service-types.mdx`
- `.codex/skills/yeet-cli/references/yeet-help-llm.md`

Docs must mention:

- `yeet run <svc> vm://ubuntu/26.04 --net=svc`
- `yeet ssh <svc>` for guest SSH
- `yeet vm console <svc>` as a serial-output stream, currently exited with `ctrl-c`
- `yeet remove <svc> --clean-data` to delete service data
- default remove preserves `data/`

- [ ] **Step 2: Run focused tests**

Run:

```bash
go test ./pkg/catch ./pkg/cli ./pkg/yeet ./cmd/yeet -count=1
```

Expected: PASS.

- [ ] **Step 3: Run full verification**

Run:

```bash
go test ./... -count=1
git diff --check
git -C website diff --check
pre-commit run --all-files
```

Expected: all commands exit 0.

- [ ] **Step 4: Run live read-only and VM smoke checks on lab-host**

Run:

```bash
CATCH_HOST=yeet-lab go run ./cmd/yeet status
CATCH_HOST=yeet-lab go run ./cmd/yeet info devbox
CATCH_HOST=yeet-lab go run ./cmd/yeet ssh -o BatchMode=yes -o ConnectTimeout=10 devbox -- hostname
```

Expected: status renders a table without JSON errors, `info devbox` reports a running VM, and SSH returns `devbox`.

## Self-Review

- Spec coverage: the plan covers the observed status JSON failure, VM remove failure, explicit data cleanup, VM run progress, web-run VM awareness, docs, and verification.
- Placeholder scan: no task contains placeholder instructions; each task names files, test commands, and expected outcomes.
- Type consistency: `RemoveOptions`, `CleanData`, `RemoveServiceWithOptions`, and VM progress helper names are consistent across tasks.
