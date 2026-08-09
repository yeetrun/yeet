# Unified Scheduled Run Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the separate `yeet cron` deployment path with immutable native scheduling through `yeet run --cron=<five-field expression>`, while preserving identity, ISO networking, storage, snapshots, and payload argument boundaries.

**Architecture:** Scheduling becomes a presence-aware option on the shared `run` command and a `schedule` attribute on native run drafts and project entries. The client uses one validation, upload, preview, and persistence path; Catch converts an explicitly supplied cron expression to a systemd timer and reconstructs an omitted timer from the installed generation so a scheduled service cannot silently become ordinary. Runtime discovery may still describe a committed timer as a cron service, but `cron` is removed as a deploy command and payload kind.

**Tech Stack:** Go 1.24 via `mise`, yargs command parsing, SSH remote command transport, systemd service/timer generation, TOML project configuration, embedded HTML/CSS/JavaScript web deploy UI, GitButler, pre-commit, mise quality tasks.

## Global Constraints

- `--cron` accepts exactly one non-empty five-field crontab expression and must pass `cronutil.CronToCalender` validation.
- Arguments after `--` are payload arguments and are never parsed as Yeet control flags.
- Remove `yeet cron` completely: no alias, deprecation shim, parser, remote handler, help entry, example, or old wire-command compatibility.
- `schedule` is the only project-config marker for scheduled mode; `type = "cron"` is invalid.
- Once Catch has committed a timer, omitting `--cron` preserves it; removing scheduled mode requires removing and recreating the service.
- Scheduled workloads accept only native binary or script payloads.
- Scheduled native workloads retain the normal `run` fields, including `--run-as`, `--net=iso`, service roots, ZFS, snapshots, and payload arguments.
- `run` remains a `manage` operation; no RPC method or permission class is added.
- Status may call a service with a committed timer a `cron service`, but no deploy surface may treat `cron` as a payload kind.
- Do not manually execute the live `owesplit -live` workload during verification.
- Use `mise exec -- go ...` for Go commands and GitButler for repository version-control writes.

---

### Task 1: Make scheduling part of the shared `run` parser and delete the public cron command

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `cmd/yeet/cli_bridge_test.go`
- Modify: `cmd/yeet/cli_test.go`
- Regenerate: `cmd/catch/depaware.txt`
- Regenerate: `cmd/yeet/depaware.txt`

**Interfaces:**
- Produces: `cli.RunFlags.Cron string` and `cli.RunFlags.CronSet bool`.
- Removes: the public `cron` command registry entry and its public flag specifications. `cli.CronFlags` and `cli.ParseCron` remain compile-only migration scaffolding until Task 3 removes their final Catch/client callers and deletes them.
- Preserves: `cli.ParseRun(args []string) (cli.RunFlags, []string, error)` and the existing `--` split contract.

- [ ] **Step 1: Write parser and registry regressions**

Add table cases to `TestParseRun`/a focused `TestParseRunCron` in `pkg/cli/cli_test.go`:

```go
tests := []struct {
	name     string
	args     []string
	want     RunFlags
	wantArgs []string
	wantErr  string
}{
	{name: "scheduled", args: []string{`--cron=0 3 * * *`, "--net=iso", "--", "-live"}, want: RunFlags{Cron: "0 3 * * *", CronSet: true, Net: "iso", Restart: true}, wantArgs: []string{"-live"}},
	{name: "payload cron flag", args: []string{"--", `--cron=0 3 * * *`}, want: RunFlags{Restart: true}, wantArgs: []string{`--cron=0 3 * * *`}},
	{name: "empty", args: []string{"--cron="}, wantErr: "--cron requires a five-field expression"},
	{name: "repeated", args: []string{`--cron=0 3 * * *`, `--cron=0 4 * * *`}, wantErr: "--cron may only be supplied once"},
	{name: "short", args: []string{`--cron=0 3 * *`}, wantErr: "cron expression must have 5 fields"},
}
```

Assert `RemoteCommandInfos()` and `RemoteFlagSpecs()` have no `cron` key, `run` exposes `--cron`, `yeet --help-agent` has no top-level cron command, and the bridge forwards:

```go
[]string{"run", "backup", "--cron=0 3 * * *", "./backup", "--", "--daily"}
```

as a remote `run` request for service `backup` without reinterpreting `--daily`.

- [ ] **Step 2: Run the focused tests and confirm they fail**

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet -run 'TestParseRunCron|Test.*Command.*Registry|Test.*Help|Test.*Bridge' -count=1
```

Expected: failures because `RunFlags` has no schedule fields, `run` has no `--cron`, and `cron` is still registered.

- [ ] **Step 3: Implement the shared parser contract**

Add these fields and parser tag:

```go
type RunFlags struct {
	Cron    string
	CronSet bool
	// existing fields remain unchanged
}

type runFlagsParsed struct {
	Cron string `flag:"cron" help:"Schedule a native binary or script with a five-field cron expression"`
	// existing fields remain unchanged
}
```

Add a presence-aware helper used by `ParseRun`:

```go
func parseRunCron(args []string, value string) (string, bool, error) {
	if countLongFlag(args, "--cron") > 1 {
		return "", true, fmt.Errorf("--cron may only be supplied once")
	}
	set := longFlagWasSupplied(args, "--cron")
	value = strings.TrimSpace(value)
	if !set {
		return "", false, nil
	}
	if value == "" {
		return "", true, fmt.Errorf("--cron requires a five-field expression")
	}
	fields := strings.Fields(value)
	if len(fields) != 5 {
		return "", true, fmt.Errorf("cron expression must have 5 fields, got %d", len(fields))
	}
	value = strings.Join(fields, " ")
	if _, err := cronutil.CronToCalender(value); err != nil {
		return "", true, fmt.Errorf("invalid cron expression: %w", err)
	}
	return value, true, nil
}
```

Populate `RunFlags.Cron`/`CronSet`, remove `cronPublicFlagsParsed`, and remove `cron` from both remote registries. Keep `CronFlags`, `cronFlagsParsed`, `ParseCron`, and `parseCronSchedule` only as temporary internal compile scaffolding; they receive no registry/help exposure and are deleted in Task 3 after their callers migrate. Update `run` usage/examples to include:

```text
yeet run <svc> ./job --cron="0 3 * * *" --run-as=backup --net=iso -- --daily
```

- [ ] **Step 4: Run the parser and CLI tests**

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet -count=1
```

Expected: PASS with `run --cron` present and no top-level `cron` command.

- [ ] **Step 5: Checkpoint the parser change**

Run `but pull --check`, inspect `but diff`, then:

```bash
but commit cron-run-unification -m "cli: schedule native workloads through run" pkg/cli/cli.go pkg/cli/cli_test.go cmd/yeet/cli_bridge_test.go cmd/yeet/cli_test.go cmd/catch/depaware.txt cmd/yeet/depaware.txt
```

### Task 2: Route schedules through Catch `run` and make committed timers immutable

**Files:**
- Modify: `pkg/catch/tty_exec.go`
- Modify: `pkg/catch/tty_authz.go`
- Modify: `pkg/catch/tty_install.go`
- Modify: `pkg/catch/installer_file.go`
- Modify: `pkg/catch/tty_test.go`
- Modify: `pkg/catch/tty_authz_test.go`
- Modify: `pkg/catch/tty_install_test.go`
- Modify: `pkg/catch/installer_file_test.go`

**Interfaces:**
- Consumes: `cli.RunFlags.Cron` and `cli.RunFlags.CronSet` from Task 1.
- Produces: `FileInstaller.resolveScheduledTimer(ftdetect.FileType) error`, which either preserves an installed timer, applies a replacement timer, rejects a non-native payload, or leaves an ordinary native service unchanged.
- Removes: Catch's `cron` dispatch handler and `cronCmdFunc`.

- [ ] **Step 1: Write Catch routing and installer regressions**

Replace the old `cronCmdFunc` tests with a `runCmdFunc` test whose installer seam captures one request:

```go
err := execer.dispatch([]string{"run", "--cron=0 3 * * *", "--run-as=backup", "--net=iso", "--", "--daily"})
if err != nil { t.Fatal(err) }
if gotCfg.Timer == nil || gotCfg.Timer.OnCalendar != "*-*-* 03:00" || !gotCfg.Timer.Persistent { t.Fatalf("timer = %#v", gotCfg.Timer) }
if gotCfg.RunAs != "backup" || !gotCfg.RunAsSet || gotCfg.Network.Interfaces != "iso" || !reflect.DeepEqual(gotCfg.Args, []string{"--daily"}) { t.Fatalf("cfg = %#v", gotCfg) }
```

In `pkg/catch/installer_file_test.go`, construct an existing generation with a real timer artifact containing:

```ini
[Timer]
OnCalendar=*-*-15 09:00:00
Persistent=true
```

Then assert a native redeploy with `cfg.Timer == nil` stages a timer containing the same `OnCalendar`, while a Compose payload fails with `scheduled service can only be updated with a native binary or script`. Also assert an ordinary native service stays ordinary and an explicit timer replaces the previous calendar.

Update authorization and PTY tests so `run --cron=...` is `permissionManage`, while bare `cron` is unknown and no longer gets a bypass/handler.

- [ ] **Step 2: Run the focused Catch tests and confirm they fail**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRunCmdFunc.*Cron|TestInstaller.*Scheduled|TestTTYAuthorization|TestShouldBypassPtyInput' -count=1
```

Expected: failures because schedules do not enter `run`, omitted timers are not reconstructed, and Catch still registers `cron`.

- [ ] **Step 3: Install an explicit schedule through `runCmdFunc`**

In `runCmdFunc`, before `runInstall`, convert the normalized expression:

```go
if flags.CronSet {
	onCalendar, err := cronutil.CronToCalender(flags.Cron)
	if err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}
	cfg.Timer = &svc.TimerConfig{OnCalendar: onCalendar, Persistent: true}
}
```

Remove `cronCmdFunc`, remove `cron` from the TTY handler map, PTY bypass list, and authorization map, and retain `run` as `permissionManage`.

- [ ] **Step 4: Preserve and validate scheduled mode in the file installer**

Call `resolveScheduledTimer(binFT)` immediately after payload type detection and before network normalization. Its native branch uses the latest committed `db.ArtifactSystemdTimerFile` when `i.cfg.Timer == nil`:

```go
func (i *FileInstaller) resolveScheduledTimer(binFT ftdetect.FileType) error {
	var path string
	installed := false
	if i.existingService.Valid() {
		path, installed = i.existingService.AsStruct().Artifacts.Latest(db.ArtifactSystemdTimerFile)
	}
	scheduled := i.cfg.Timer != nil || installed
	if scheduled && !systemdPayloadType(binFT) {
		return fmt.Errorf("scheduled service can only be updated with a native binary or script")
	}
	if i.cfg.Timer != nil || !installed {
		return nil
	}
	timer, err := readSystemdTimerConfig(path)
	if err != nil {
		return fmt.Errorf("preserve installed timer: %w", err)
	}
	i.cfg.Timer = timer
	return nil
}
```

Implement `readSystemdTimerConfig` as a scanner for exactly one non-empty `OnCalendar=` line and an optional `Persistent=` boolean:

```go
func readSystemdTimerConfig(path string) (*svc.TimerConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	timer := &svc.TimerConfig{Persistent: true}
	seenCalendar := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "OnCalendar="):
			if seenCalendar {
				return nil, fmt.Errorf("timer has repeated OnCalendar")
			}
			timer.OnCalendar = strings.TrimSpace(strings.TrimPrefix(line, "OnCalendar="))
			seenCalendar = true
		case strings.HasPrefix(line, "Persistent="):
			persistent, err := strconv.ParseBool(strings.TrimSpace(strings.TrimPrefix(line, "Persistent=")))
			if err != nil {
				return nil, fmt.Errorf("invalid Persistent value: %w", err)
			}
			timer.Persistent = persistent
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !seenCalendar || timer.OnCalendar == "" {
		return nil, fmt.Errorf("timer is missing OnCalendar")
	}
	return timer, nil
}
```

Because `i.cfg.Timer` is restored before `normalizeNetworkForServiceType`, native ISO validation continues using `iso.PayloadCron`; `ensureSystemdUnit` emits both the oneshot service and timer into the new generation.

- [ ] **Step 5: Run the Catch package tests**

Run:

```bash
mise exec -- go test ./pkg/catch -count=1
```

Expected: PASS, including rollback/generation tests and native timer ISO tests.

- [ ] **Step 6: Checkpoint the Catch change**

Run `but diff`, then:

```bash
but commit cron-run-unification -m "catch: preserve schedules in native run installs" pkg/catch/tty_exec.go pkg/catch/tty_authz.go pkg/catch/tty_install.go pkg/catch/installer_file.go pkg/catch/tty_test.go pkg/catch/tty_authz_test.go pkg/catch/tty_install_test.go pkg/catch/installer_file_test.go
```

### Task 3: Unify client drafts, project configuration, and service routing

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `pkg/yeet/constants.go`
- Modify: `pkg/yeet/svc_cmd.go`
- Modify: `pkg/yeet/run_draft.go`
- Modify: `pkg/yeet/run_draft_validate.go`
- Modify: `pkg/yeet/project_config.go`
- Modify: `pkg/yeet/info_cmd.go`
- Modify: `pkg/yeet/handle_svc_cmd_test.go`
- Modify: `pkg/yeet/handle_svc_cmd_config_test.go`
- Modify: `pkg/yeet/svc_cmd_branch_test.go`
- Modify: `pkg/yeet/svc_cmd_helpers_test.go`
- Modify: `pkg/yeet/project_config_test.go`
- Modify: `pkg/yeet/run_draft_test.go`
- Modify: `pkg/yeet/run_draft_validate_test.go`
- Modify: `pkg/yeet/info_cmd_test.go`
- Modify: `pkg/yeet/run_web_api_test.go` only to migrate the executor-boundary scheduled draft from cron payload kind to native file plus schedule; Task 4 owns the remaining web form/UI migration.

**Interfaces:**
- Consumes: shared `run --cron` parsing and Catch's immutable timer behavior.
- Produces: native `RunDraft` values with `Cron.Schedule` independent of `PayloadKind`; project entries with `Schedule` and no cron type; one ordinary run upload/execution path.
- Removes: `handleSvcCron`, `runCronFromProjectConfig`, `runCron*`, `saveCronConfig*`, `splitCronArgs`, the cron-only draft executor/preview, and Task 1's temporary internal `cli.CronFlags`/`cli.ParseCron` scaffolding after all callers are migrated. Retain only the `serviceTypeCron = "cron"` constant as compile-only web migration scaffolding until Task 4 deletes the final web card/tests and removes the constant.

- [ ] **Step 1: Write draft, config, and routing regressions**

Add focused tests proving:

```go
draft, err := runDraftFromCLI([]string{payload, `--cron=0 9 15 * *`, "--net=iso", "--", "-live"}, loc, "yeet-pve1")
if err != nil { t.Fatal(err) }
if draft.PayloadKind != "file" || draft.Cron.Schedule != "0 9 15 * *" || !reflect.DeepEqual(draft.PayloadArgs, []string{"-live"}) { t.Fatalf("draft = %#v", draft) }
```

For a stored entry with `Schedule: "0 9 15 * *"`, `RunAs: "yeet-svc"`, and `Args: []string{"--net=iso", "--", "-live"}`, assert both explicit-payload and config-only `yeet run owesplit` send one remote command beginning:

```go
[]string{"run", "--cron=0 9 15 * *", "--run-as=yeet-svc", "--net=iso", "--", "-live"}
```

Assert saving round-trips `Schedule`, emits no `type = "cron"`, and preserves `Args == []string{"--net=iso", "--", "-live"}`. Add a load failure for `type = "cron"`. Add missing-file coverage where a scheduled path returns `stat ... no such file or directory` and never mentions container image or `--run-as` limitations.

Replace cron preview/executor tests with an ordinary `yeet run ... --cron=...` preview and the standard run output seam. Migrate the web API executor regression at the same boundary to a native file draft with `Cron.Schedule`, while leaving workload hints and assets for Task 4. Retain status tests that describe server data type `cron` as `cron service`.

- [ ] **Step 2: Run focused client tests and confirm they fail**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'Test.*Scheduled|Test.*Cron|TestRunDraft|TestSaveRunConfig|TestProjectConfig|TestInfo' -count=1
```

Expected: failures from cron payload-kind branches, config-only cron redirects, lost schedule state, and the missing-path image heuristic.

- [ ] **Step 3: Rehydrate and persist schedule in the normal run argument path**

Extend `effectiveSvcRunArgs` so a stored schedule is restored only when the caller did not supply `--cron`:

```go
func runArgsWithConfiguredSchedule(args []string, schedule string) ([]string, error) {
	flags, _, err := cli.ParseRun(args)
	if err != nil || flags.CronSet || strings.TrimSpace(schedule) == "" {
		return args, err
	}
	return appendRunControlFlagBeforeBoundary(args, "--cron="+strings.TrimSpace(schedule)), nil
}

func appendRunControlFlagBeforeBoundary(args []string, flag string) []string {
	for i, arg := range args {
		if arg == "--" {
			out := append([]string{}, args[:i]...)
			out = append(out, flag)
			return append(out, args[i:]...)
		}
	}
	return append(append([]string{}, args...), flag)
}
```

Have `saveRunConfigWithPayloadKind` parse the effective run flags, copy `runFlags.Cron` to `ServiceEntry.Schedule`, preserve an existing schedule when the flag is omitted, and remove the `--cron` control token before normalizing `ServiceEntry.Args`. Validate loaded entries with:

```go
if strings.EqualFold(strings.TrimSpace(entry.Type), "cron") {
	return fmt.Errorf("service %s uses removed type %q; remove type and keep schedule", entry.Name, entry.Type)
}
```

Keep `Type` only for VM entries.

Add `service set` and `service sync` regressions using a scheduled entry with `Args: []string{"--net=iso", "--", "-live"}`. After each rewrite, require `Schedule == "0 9 15 * *"` and the same argument boundary so `--net=iso` cannot move into the payload-argument suffix.

- [ ] **Step 4: Make draft scheduling orthogonal to native payload kind**

Populate `Cron: RunDraftCron{Schedule: effectiveParsed.Cron}` in `runDraftFromCLI`, prepend `--cron=<schedule>` in `RunDraft.runArgs`, and delete the cron-only executor and preview. `executeRunDraftWithOptions` always invokes `executeRunDraftOutput` and `saveRunDraftExecutionConfig`.

In validation, normalize a non-empty `draft.Cron.Schedule` first, force the payload through local-file existence/type detection before image heuristics, and require the result to be `file` with a binary or script type. Reject VM, Compose, Dockerfile, generated Python/TypeScript, remote image, and local image drafts with:

```text
scheduled workloads require a native binary or script payload
```

Select `iso.PayloadCron` for network validation when `draft.Cron.Schedule != ""`; otherwise select the native payload kind. Do not reject storage, snapshot, environment, identity, or native ISO fields merely because a schedule exists.

- [ ] **Step 5: Remove the duplicate client command path and update info drift**

Delete `cron` from `HandleSvcCmd` dispatch and delete every cron-only upload/config helper. Remove every non-web use of `serviceTypeCron`, retaining the constant itself only until Task 4 migrates the web surface; preserve `ServiceDataTypeCron` handling from Catch responses.

After the Catch and client callers are gone, delete `cli.CronFlags`, `cronFlagsParsed`, `ParseCron`, and `parseCronSchedule` from `pkg/cli/cli.go` together with their obsolete tests from `pkg/cli/cli_test.go`. This is the final removal; no compile-only or wire compatibility remains.

Render client type from `Schedule` rather than `Type`, keep a saved schedule row, and when server data type is cron but local schedule is empty append drift guidance containing:

```text
schedule = "<five-field expression>"
```

Never suggest `yeet cron`.

- [ ] **Step 6: Run all client tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -count=1
```

Expected: PASS with config-only and explicit-payload scheduled runs using the same execution path.

- [ ] **Step 7: Checkpoint the client change**

Run `but diff`, then commit the files touched by this task:

```bash
but commit cron-run-unification -m "yeet: unify scheduled services with run" pkg/yeet
```

### Task 4: Make schedule an optional native-file field in web deploy

**Files:**
- Modify: `pkg/yeet/constants.go`
- Modify: `pkg/yeet/run_web.go`
- Modify: `pkg/yeet/web_run_assets/index.html`
- Modify: `pkg/yeet/web_run_assets/app.js`
- Modify: `pkg/yeet/run_web_test.go`
- Modify: `pkg/yeet/run_web_api_test.go`
- Modify: `pkg/yeet/web_run_assets_test.go`

**Interfaces:**
- Consumes: `RunDraft.Cron.Schedule` and shared validation/execution from Task 3.
- Produces: a `file` workload form with optional `cron.schedule`; no web payload kind named `cron`.
- Removes: the last `serviceTypeCron` constant after all web references are gone.

- [ ] **Step 1: Write bootstrap, asset, and API regressions**

Assert workload hints are exactly:

```go
[]string{"compose", "vm", "dockerfile", "remote-image", "python", "typescript", "file"}
```

Assert the file hint supports `iso`, the HTML has schedule and run-as fields associated with native binary/script, and JavaScript serializes them as `draft.cron.schedule` plus the shared `draft.runAs`/presence contract while always rendering a `yeet run` preview. Preserve and extend Task 3's native-file-plus-schedule web API regression while removing the remaining cron workload expectations.

- [ ] **Step 2: Run focused web tests and confirm they fail**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestRunWeb|TestWebRun' -count=1
```

Expected: failures because the cron workload card and cron-only JavaScript branches still exist.

- [ ] **Step 3: Implement the native-plus-schedule form**

Remove the `serviceTypeCron` workload hint, add `iso` to the file hint networks, and update its description to `Upload and run, or schedule, a native binary or script.` In `index.html`, display `cron.schedule` and native run-as only for `payloadKind === "file"`; label schedule `Schedule (optional five-field cron)` and map run-as validation to the native identity field.

In `app.js`, delete all `payloadKind === "cron"` branches. Include:

```js
cron: payloadKind === "file" && schedule.trim()
  ? { schedule: schedule.trim() }
  : {},
```

and build previews by appending `--cron=${schedule}` to `yeet run` before the `--` payload boundary.

- [ ] **Step 4: Run web and full client tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -count=1
```

Expected: PASS.

- [ ] **Step 5: Checkpoint the web change**

Run:

```bash
but commit cron-run-unification -m "web: schedule native run payloads" pkg/yeet/run_web.go pkg/yeet/web_run_assets/index.html pkg/yeet/web_run_assets/app.js pkg/yeet/run_web_test.go pkg/yeet/run_web_api_test.go pkg/yeet/web_run_assets_test.go
```

### Task 5: Update help, README, and the website manual

**Files:**
- Modify: `README.md`
- Regenerate: `.codex/skills/yeet-cli/references/yeet-help-agent.md`
- Modify: `website/docs/payloads/cron-jobs.mdx`
- Modify: other current website manual pages returned by `rg -n 'yeet cron' website/docs`
- Test: `pkg/yeet/release_assets_test.go`
- Test: documentation link/build checks already wired into pre-commit

**Interfaces:**
- Consumes: the final CLI syntax and immutable scheduled-mode behavior.
- Produces: no current docs/help occurrence of `yeet cron`; public guidance for `yeet run --cron`, `--run-as`, native ISO, and remove/recreate semantics.

- [ ] **Step 1: Read the documentation skill and style guide**

Read `.codex/skills/yeet-docs/SKILL.md` completely, then read `website/STYLEGUIDE.md` and only the linked references required for CLI/manual changes.

- [ ] **Step 2: Add/update doc-surface assertions**

Extend an existing asset/help test to fail if current generated help or README contains `yeet cron`, and to require `--cron` in the `run` help. Historical changelog entries are excluded from this assertion.

- [ ] **Step 3: Run the doc-focused test and confirm it fails**

Run:

```bash
mise exec -- go test ./cmd/yeet ./pkg/yeet -run 'Test.*Help|Test.*ReleaseAssets' -count=1
```

Expected: failure while README/generated help/manual still document `yeet cron`.

- [ ] **Step 4: Rewrite current documentation and regenerate help**

Use this canonical public example:

```bash
yeet run backup ./backup --cron="0 3 * * *" --run-as=backup --net=iso -- --full
```

State that omitting `--cron` preserves an installed schedule, schedule updates use another non-empty `--cron`, and returning to ordinary service mode requires `yeet rm` followed by recreation. Explain that scheduling is limited to native binaries/scripts and that native-compatible network modes include ISO.

Regenerate agent help with:

```bash
tools/generate-yeet-help-agent.sh
```

Verify current surfaces:

```bash
rg -n 'yeet cron' README.md .codex/skills/yeet-cli website/docs --glob '!**/changelog.mdx'
rg -n -- '--cron' README.md .codex/skills/yeet-cli website/docs/payloads/cron-jobs.mdx
```

Expected: the first command has no matches; the second finds the unified examples.

- [ ] **Step 5: Run docs/help verification**

Run:

```bash
mise exec -- go test ./cmd/yeet ./pkg/yeet -run 'Test.*Help|Test.*ReleaseAssets' -count=1
git -C website diff --check
```

Expected: PASS and no website whitespace errors.

- [ ] **Step 6: Checkpoint documentation without publishing**

Commit the website documentation in the submodule with a succinct local commit, but do not push it without a separate publication request:

```bash
git -C website add docs
git -C website commit -m "docs: deploy scheduled jobs through run"
```

Then checkpoint README, generated help, and the resulting website gitlink on the GitButler branch:

```bash
but commit cron-run-unification -m "docs: document unified scheduled runs" README.md .codex/skills/yeet-cli/references/yeet-help-agent.md website
```

### Task 6: Verify the complete implementation and recover `owesplit`

**Files:**
- Modify: `/Users/shayne/yeet-services/owesplit/owesplit` by copying the known active generation from the host.
- Modify: `/Users/shayne/yeet-services/yeet.toml` only in the `owesplit` entry.
- Inspect: generated service/timer/network artifacts on `root@pve1`.

**Interfaces:**
- Consumes: a locally built matching `yeet`/`catch` pair and the unified run workflow.
- Produces: a reproducible `owesplit` project entry and a healthy waiting timer running as `yeet-svc:yeet-svc` in the existing ISO network.

- [ ] **Step 1: Run formatting and focused cross-package tests**

Run:

```bash
mise exec -- gofmt -w pkg/cli pkg/catch pkg/yeet cmd/yeet
mise exec -- go test ./pkg/cli ./cmd/yeet ./pkg/catch ./pkg/yeet -count=1
```

Expected: PASS.

- [ ] **Step 2: Run full repository and policy gates**

Run once on the stable candidate:

```bash
mise exec -- go test ./... -count=1
pre-commit run --all-files
mise run quality:goal
```

Expected: all commands succeed without lowering thresholds or refreshing baselines.

- [ ] **Step 3: Review and checkpoint any final coherent corrections**

Run `but pull --check`, `but diff`, and `but status --short`. If verification required source corrections, absorb them into the matching unpublished checkpoint rather than adding a tiny fixup commit. Confirm no unrelated branch or user changes are included.

- [ ] **Step 4: Restore the payload from the active host generation**

Create only the exact target directory, copy the deployed binary, and verify it before any deployment:

```bash
mkdir -p /Users/shayne/yeet-services/owesplit
scp root@pve1:/flash/yeet/services/owesplit/bin/owesplit-20260103153229 /Users/shayne/yeet-services/owesplit/owesplit
shasum -a 256 /Users/shayne/yeet-services/owesplit/owesplit
```

Expected SHA-256:

```text
bf3625a463858bde7e4b367fbf6eaa43d63d35f8bafbbbec950606d70c7a39ee
```

- [ ] **Step 5: Canonicalize only the `owesplit` project entry**

Use `apply_patch` so the entry has no `type = "cron"` and includes exactly:

```toml
name = "owesplit"
host = "yeet-pve1"
payload = "owesplit/owesplit"
run_as = "yeet-svc"
service_root = "flash/yeet/services/owesplit"
service_root_zfs = true
schedule = "0 9 15 * *"
args = ["--net=iso", "--", "-live"]
```

- [ ] **Step 6: Build and install the matching client/server pair**

Build local binaries:

```bash
mise exec -- go build -o bin/yeet ./cmd/yeet
mise exec -- go build -o bin/catch ./cmd/catch
```

Use the repository's `yeet init`/Catch installation workflow from `.codex/skills/yeet-cli` to install this exact Catch build on `yeet-pve1`, then verify the remote executable's build metadata matches the local source revision before deploying `owesplit`.

- [ ] **Step 7: Deploy through unified `run` without executing the workload**

From `/Users/shayne/yeet-services`, with `CATCH_HOST=yeet-pve1` and the just-built client first on `PATH`, run:

```bash
yeet run owesplit owesplit/owesplit --cron="0 9 15 * *" --run-as=yeet-svc --net=iso -- -live
```

Do not invoke `yeet start owesplit`, `systemctl start owesplit.service`, or any direct binary execution.

- [ ] **Step 8: Verify the live acceptance boundary**

Verify with `yeet info owesplit`, `yeet status owesplit`, `systemctl cat owesplit.service owesplit.timer`, `systemctl list-timers owesplit.timer`, artifact checksums, service-root ownership/access checks as `yeet-svc`, and ISO inspection. Required evidence:

```text
User=yeet-svc
Group=yeet-svc
NetworkNamespacePath=/var/run/netns/yeet-214bd7ab8a-ns
OnCalendar corresponding to 0 9 15 * *
timer state waiting
ISO state ready
IP 172.16.0.14
public-only DNS
binary SHA-256 bf3625a463858bde7e4b367fbf6eaa43d63d35f8bafbbbec950606d70c7a39ee
```

Confirm `/Users/shayne/yeet-services/yeet.toml` remains canonical after the run and `yeet info` reports no local schedule/identity/network drift.

- [ ] **Step 9: Final repository and operational status**

Report separately: uncommitted state, GitButler checkpoint commits, whether anything was pushed or landed on `origin/main`, website submodule publication state, local/remote binary revisions, and the live timer/network verification. Do not claim publication when only local checkpoints exist.
