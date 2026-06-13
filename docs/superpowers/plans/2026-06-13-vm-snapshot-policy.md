# VM Snapshot Policy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make VM snapshot policy in `yeet run --web` manual-retention focused and add `yeet vm snapshot` for ZFS zvol-backed VMs.

**Architecture:** Keep the shared snapshot policy model, but branch VM web-run semantics so VMs expose only retention fields. Persist snapshot policy during VM provisioning, then add a VM-specific catch command that snapshots the VM zvol, pauses/resumes Firecracker when running, and optionally writes full Firecracker state and memory checkpoint files. Existing service-root automatic snapshot behavior remains unchanged for non-VM payloads.

**Tech Stack:** Go, yargs command metadata, catch TTY command routing, ZFS command helpers, Firecracker HTTP-over-UNIX API, embedded HTML/CSS/JS assets, website MDX docs, GitButler (`but`) for version-control writes.

---

## Scope Check

The approved spec has two dependent surfaces:

- `yeet run --web` VM snapshot policy UX and draft validation.
- `yeet vm snapshot` runtime behavior on catch hosts.

These belong in one plan because the web UI should configure a policy that has an immediate VM command-backed meaning. Restore/rollback, raw disk snapshots, automatic VM pre-change snapshots, diff checkpoints, and guest freeze/thaw hooks remain out of scope.

## File Structure

- Modify `pkg/yeet/web_run_assets/index.html`: add stable DOM hooks around snapshot summary, required field, and events field.
- Modify `pkg/yeet/web_run_assets/app.js`: branch snapshot draft construction and labels for VM payloads.
- Modify `pkg/yeet/web_run_assets_test.go`: assert VM snapshot UI hooks and JavaScript behavior hooks exist.
- Modify `pkg/yeet/run_draft_validate.go`: reject VM snapshot `required` and `events` fields if submitted directly.
- Modify `pkg/yeet/run_draft_validate_test.go`: cover VM snapshot validation.
- Modify `pkg/catch/vm_provision.go`: persist VM snapshot policy flags during VM creation.
- Modify `pkg/catch/vm_provision_test.go`: cover VM creation policy persistence.
- Modify `pkg/cli/cli.go`: add `VMSnapshotFlags`, parser, metadata, and flag specs.
- Modify `pkg/cli/cli_test.go`: cover parser, registry, and flag specs.
- Modify `pkg/catch/tty_vm.go`: route `vm snapshot`.
- Create `pkg/catch/vm_snapshot.go`: VM snapshot command implementation and Firecracker API client.
- Create `pkg/catch/vm_snapshot_test.go`: VM snapshot planning, ordering, error, and metadata tests.
- Modify `pkg/catch/service_snapshots.go`: allow optional snapshot comment/checkpoint metadata and dataset-specific pruning helper reuse.
- Modify `pkg/catch/service_snapshots_test.go`: cover new metadata options without changing existing behavior.
- Modify website docs: `website/docs/payloads/vms.mdx`, `website/docs/concepts/zfs.mdx`, and `website/docs/cli/yeet-cli.mdx`.
- Update website web-run screenshot if the existing screenshot shows the snapshot section.

Use coherent GitButler checkpoint commits after each task. Start each task with `but status -fv`, collect the runtime change IDs, and commit only that task's files with `but commit codex/vm-snapshot-policy-design -m "<message>" --changes <ids from status>`.

## Task 1: VM Web Snapshot UI And Draft Validation

**Files:**
- Modify: `pkg/yeet/web_run_assets/index.html`
- Modify: `pkg/yeet/web_run_assets/app.js`
- Modify: `pkg/yeet/web_run_assets_test.go`
- Modify: `pkg/yeet/run_draft_validate.go`
- Modify: `pkg/yeet/run_draft_validate_test.go`

- [ ] **Step 1: Write failing VM snapshot validation tests**

Add this test near the other VM run draft tests in `pkg/yeet/run_draft_validate_test.go`:

```go
func TestValidateRunDraftRejectsVMRequiredAndEventsSnapshots(t *testing.T) {
	required := true
	draft := RunDraft{
		Service:     "devbox",
		Host:        "yeet-lab",
		Payload:     "vm://ubuntu/26.04",
		PayloadKind: serviceTypeVM,
		VM:          RunDraftVM{CPUs: 2, Memory: "2g", Disk: "64g"},
		Network:     RunDraftNetwork{Modes: []string{"svc"}},
		Snapshots: RunDraftSnapshots{
			Mode:     "on",
			KeepLast: 3,
			MaxAge:   "72h",
			Required: &required,
			Events:   []string{"run"},
		},
	}

	_, validation := validateRunDraft(context.Background(), draft, t.TempDir())

	if validation.OK {
		t.Fatal("validation OK = true, want false")
	}
	if got := validation.fieldError("snapshots.required"); !strings.Contains(got, "VM snapshot policy does not use required") {
		t.Fatalf("snapshots.required error = %q", got)
	}
	if got := validation.fieldError("snapshots.events"); !strings.Contains(got, "VM snapshot policy does not use events") {
		t.Fatalf("snapshots.events error = %q", got)
	}
}

func TestValidateRunDraftAcceptsVMRetentionSnapshotPolicy(t *testing.T) {
	draft := RunDraft{
		Service:     "devbox",
		Host:        "yeet-lab",
		Payload:     "vm://ubuntu/26.04",
		PayloadKind: serviceTypeVM,
		VM:          RunDraftVM{CPUs: 2, Memory: "2g", Disk: "64g"},
		Network:     RunDraftNetwork{Modes: []string{"svc"}},
		Snapshots: RunDraftSnapshots{
			Mode:     "on",
			KeepLast: 3,
			MaxAge:   "72h",
		},
	}

	normalized, validation := validateRunDraft(context.Background(), draft, t.TempDir())

	if !validation.OK {
		t.Fatalf("validation OK = false, errors = %#v", validation.Errors)
	}
	if normalized.Snapshots.Mode != "on" || normalized.Snapshots.KeepLast != 3 || normalized.Snapshots.MaxAge != "72h" {
		t.Fatalf("normalized snapshots = %#v", normalized.Snapshots)
	}
}
```

- [ ] **Step 2: Write failing web asset assertions**

In `pkg/yeet/web_run_assets_test.go`, extend `TestWebRunAssetsExposeFirstDeployFields` so the index snippets include:

```go
`id="snapshotDetails"`,
`id="snapshotSummaryText"`,
`id="snapshotModeLabel"`,
`id="snapshotRequiredField"`,
`id="snapshotEventsField"`,
```

Extend the app snippets with:

```go
"function snapshotDraftForPayloadKind(payloadKind)",
"function syncSnapshotUI(payloadKind)",
`snapshotEventsField`,
`snapshotRequiredField`,
`VM snapshots`,
`VM snapshot policy does not use events`,
```

- [ ] **Step 3: Run focused tests and verify failure**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestValidateRunDraftRejectsVMRequiredAndEventsSnapshots|TestValidateRunDraftAcceptsVMRetentionSnapshotPolicy|TestWebRunAssetsExposeFirstDeployFields' -count=1
```

Expected: fail because the validation helper and DOM/JS hooks do not exist yet.

- [ ] **Step 4: Add stable snapshot DOM hooks**

In `pkg/yeet/web_run_assets/index.html`, replace the snapshot `<details>` block opening and labels with this shape while preserving the existing fields:

```html
<details class="advanced-block" id="snapshotDetails">
  <summary><span id="snapshotSummaryText">Snapshots</span> <button type="button" class="help" tabindex="-1" data-help="Optional per-service snapshot policy stored with the run recipe." aria-label="Help for snapshots">?</button></summary>
  <div class="split-fields">
    <label for="snapshots">
      <span id="snapshotModeLabel">Mode</span>
      <select id="snapshots" name="snapshots"></select>
    </label>
    <label for="snapshotKeepLast">
      <span>Keep last</span>
      <input id="snapshotKeepLast" name="snapshotKeepLast" inputmode="numeric" autocomplete="off">
    </label>
  </div>
  <label for="snapshotMaxAge">
    <span>Max age</span>
    <input id="snapshotMaxAge" name="snapshotMaxAge" autocomplete="off" spellcheck="false" placeholder="7d or 72h">
  </label>
  <label for="snapshotRequired" id="snapshotRequiredField">
    <span>Required</span>
    <select id="snapshotRequired" name="snapshotRequired"></select>
  </label>
  <label for="snapshotEvents" id="snapshotEventsField">
    <span>Events</span>
    <input id="snapshotEvents" name="snapshotEvents" autocomplete="off" spellcheck="false" placeholder="run,docker-update">
  </label>
</details>
```

- [ ] **Step 5: Branch snapshot draft construction for VM payloads**

In `pkg/yeet/web_run_assets/app.js`, add this helper near `snapshotRequiredValue`:

```js
function snapshotDraftForPayloadKind(payloadKind) {
  if (payloadKind === "cron") return {};
  const vmPayload = payloadKind === "vm";
  return {
    mode: $("snapshots").value,
    keepLast: Number.parseInt($("snapshotKeepLast").value, 10) || 0,
    maxAge: $("snapshotMaxAge").value.trim(),
    required: vmPayload ? undefined : snapshotRequiredValue(),
    events: vmPayload ? [] : splitCSV($("snapshotEvents").value),
  };
}
```

Then replace the `snapshots:` object in `buildDraft()` with:

```js
snapshots: snapshotDraftForPayloadKind(payloadKind),
```

- [ ] **Step 6: Add VM-specific snapshot labels and hidden fields**

In `pkg/yeet/web_run_assets/app.js`, add this helper near `syncWorkloadUI()`:

```js
function syncSnapshotUI(payloadKind) {
  const isCron = payloadKind === "cron";
  const isVM = payloadKind === "vm";
  const details = $("snapshotDetails");
  details.hidden = isCron;
  if (isCron) return;

  $("snapshotSummaryText").textContent = isVM ? "VM snapshots" : "Snapshots";
  $("snapshotModeLabel").textContent = isVM ? "Policy" : "Mode";
  $("snapshotRequiredField").hidden = isVM;
  $("snapshotEventsField").hidden = isVM;

  const help = details.querySelector(".help");
  help.dataset.help = isVM
    ? "Retention policy for yeet vm snapshot. Disk snapshots use the VM zvol."
    : "Optional per-service snapshot policy stored with the run recipe.";
}
```

In `syncWorkloadUI()`, replace:

```js
$("snapshots").closest("details").hidden = isCron;
```

with:

```js
syncSnapshotUI(def.payloadKind);
```

- [ ] **Step 7: Add VM-specific snapshot validation**

In `pkg/yeet/run_draft_validate.go`, add:

```go
func validateRunDraftVMSnapshots(draft *RunDraft, result *RunDraftValidationResult) {
	if draft.PayloadKind != serviceTypeVM {
		return
	}
	if draft.Snapshots.Required != nil || draft.Snapshots.RequiredInherit {
		result.addError("snapshots.required", "VM snapshot policy does not use required; manual vm snapshots fail or succeed directly")
	}
	if len(draft.Snapshots.Events) != 0 || draft.Snapshots.EventsInherit {
		result.addError("snapshots.events", "VM snapshot policy does not use events; use mode, keep last, and max age for manual vm snapshots")
		draft.Snapshots.Events = nil
	}
}
```

Call it at the end of `validateRunDraftSnapshots()` after `validateRunDraftSnapshotEvents(draft, result)`:

```go
validateRunDraftVMSnapshots(draft, result)
```

Run `gofmt -w pkg/yeet/run_draft_validate.go pkg/yeet/run_draft_validate_test.go`.

- [ ] **Step 8: Run focused tests**

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestValidateRunDraftRejectsVMRequiredAndEventsSnapshots|TestValidateRunDraftAcceptsVMRetentionSnapshotPolicy|TestWebRunAssetsExposeFirstDeployFields' -count=1
```

Expected: pass.

- [ ] **Step 9: Commit Task 1**

Run:

```bash
but status -fv
```

Use the change IDs for the five files in this task and commit:

```bash
but commit codex/vm-snapshot-policy-design -m "web: scope vm snapshot policy controls"
```

If GitButler requires explicit IDs, re-run the command with `--changes` and the IDs reported by `but status -fv`.

## Task 2: Persist VM Snapshot Policy On VM Creation

**Files:**
- Modify: `pkg/catch/vm_provision.go`
- Modify: `pkg/catch/vm_provision_test.go`

- [ ] **Step 1: Write the failing VM provision policy test**

Add this test near the existing ZFS VM provision tests in `pkg/catch/vm_provision_test.go`:

```go
func TestRunVMPersistsSnapshotPolicyFlags(t *testing.T) {
	server := newVMProvisionTestServer(t)
	execer, _, _, _ := newVMProvisionTestExecer(t, server, "devbox")

	vmProvisionDiskRunner = func(context.Context, []string) error { return nil }
	t.Cleanup(func() { vmProvisionDiskRunner = nil })

	if err := execer.runVM(cli.RunFlags{
		Net:              "svc",
		Restart:          false,
		Snapshots:        "on",
		SnapshotKeepLast: "3",
		SnapshotMaxAge:   "72h",
		SnapshotChange:   true,
	}, vmUbuntu2604Payload); err != nil {
		t.Fatalf("runVM: %v", err)
	}

	sv := server.serviceViewForTest(t, "devbox")
	policy := sv.SnapshotPolicy()
	if !policy.Valid() {
		t.Fatal("SnapshotPolicy valid = false, want true")
	}
	if !policy.Enabled().Get() || policy.KeepLast().Get() != 3 || policy.MaxAge() != "72h" {
		t.Fatalf("SnapshotPolicy = %#v, want enabled keep=3 max=72h", policy.AsStruct())
	}
}
```

If `serviceViewForTest` is not available in this package, use the same DB read pattern used by nearby `vm_provision_test.go` assertions:

```go
dv, err := server.getDB()
if err != nil {
	t.Fatalf("getDB: %v", err)
}
sv, ok := dv.Services().GetOk("devbox")
if !ok {
	t.Fatal("devbox not found")
}
```

- [ ] **Step 2: Run the focused test and verify failure**

Run:

```bash
mise exec -- go test ./pkg/catch -run TestRunVMPersistsSnapshotPolicyFlags -count=1
```

Expected: fail because the VM provisioning path returns before `snapshotFlagsFromRunFlags` is applied to the service.

- [ ] **Step 3: Thread validated snapshot flags through VM provisioning**

In `pkg/catch/vm_provision.go`, update `provisionVM` so it validates snapshot flags before any VM disk or network mutation:

```go
snapshotPolicyFlags, err := snapshotFlagsFromRunFlags(flags)
if err != nil {
	return err
}
```

Place it after `validateAndCheckVMProvisionService(flags)` and before `vmProvisionInputs`.

Change these signatures:

```go
func (e *ttyExecer) finishVMProvision(ctx context.Context, plan vmProvisionPlan, payload string, restart bool, snapshotPolicyFlags *cli.ServiceSetFlags) error
func (e *ttyExecer) commitVMProvision(plan vmProvisionPlan, payload string, snapshotPolicyFlags *cli.ServiceSetFlags) error
```

Pass `snapshotPolicyFlags` from `provisionVM` into `finishVMProvision`, then from `finishVMProvision` into `commitVMProvision`.

- [ ] **Step 4: Apply snapshot policy during the VM DB commit**

In `commitVMProvision`, inside the existing `MutateService` callback after assigning `s.VM`, add:

```go
if snapshotPolicyFlags != nil {
	if err := applySnapshotFlagsToService(s, *snapshotPolicyFlags); err != nil {
		return err
	}
}
```

Run:

```bash
gofmt -w pkg/catch/vm_provision.go pkg/catch/vm_provision_test.go
```

- [ ] **Step 5: Run focused catch tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestRunVMPersistsSnapshotPolicyFlags|TestRunVMZVOLProvisionStoresDevicePath|TestRunVMRestartFlagControlsSystemctlRestart' -count=1
```

Expected: pass.

- [ ] **Step 6: Commit Task 2**

Run:

```bash
but status -fv
but commit codex/vm-snapshot-policy-design -m "catch: persist vm snapshot policy"
```

If GitButler requires explicit IDs, use the IDs for `pkg/catch/vm_provision.go` and `pkg/catch/vm_provision_test.go`.

## Task 3: Add `yeet vm snapshot` CLI Surface

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Modify: `pkg/catch/tty_vm.go`

- [ ] **Step 1: Write failing parser and registry tests**

Add this parser test to `pkg/cli/cli_test.go` near `TestParseVMSetFlags`:

```go
func TestParseVMSnapshot(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    VMSnapshotFlags
		wantOut []string
		wantErr string
	}{
		{name: "plain", args: []string{"devbox"}, wantOut: []string{"devbox"}},
		{
			name:    "full with comment",
			args:    []string{"--full", "--comment", "before upgrade", "devbox"},
			want:    VMSnapshotFlags{Full: true, Comment: "before upgrade"},
			wantOut: []string{"devbox"},
		},
		{name: "blank comment trims away", args: []string{"--comment", "  ", "devbox"}, wantOut: []string{"devbox"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, out, err := ParseVMSnapshot(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseVMSnapshot error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseVMSnapshot: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("flags = %#v, want %#v", got, tt.want)
			}
			if !reflect.DeepEqual(out, tt.wantOut) {
				t.Fatalf("args = %#v, want %#v", out, tt.wantOut)
			}
		})
	}
}
```

In `TestRemoteCommandRegistryAndFlagSpecs`, add:

```go
if reg.Groups["vm"].Commands["snapshot"].Info.Usage != "vm snapshot <vm> [--comment=TEXT] [--full]" {
	t.Fatalf("vm snapshot usage = %q", reg.Groups["vm"].Commands["snapshot"].Info.Usage)
}
if !RemoteGroupFlagSpecs()["vm"]["snapshot"]["--comment"].ConsumesValue {
	t.Fatal("vm snapshot --comment should consume a value")
}
if RemoteGroupFlagSpecs()["vm"]["snapshot"]["--full"].ConsumesValue {
	t.Fatal("vm snapshot --full should not consume a value")
}
```

- [ ] **Step 2: Run parser tests and verify failure**

Run:

```bash
mise exec -- go test ./pkg/cli -run 'TestParseVMSnapshot|TestRemoteCommandRegistryAndFlagSpecs' -count=1
```

Expected: fail because `VMSnapshotFlags`, `ParseVMSnapshot`, and metadata do not exist.

- [ ] **Step 3: Add parser types and function**

In `pkg/cli/cli.go`, add:

```go
type VMSnapshotFlags struct {
	Comment string
	Full    bool
}
```

Near `vmSetFlagsParsed`, add:

```go
type vmSnapshotFlagsParsed struct {
	Comment string `flag:"comment"`
	Full    bool   `flag:"full"`
}
```

Near `ParseVMSet`, add:

```go
func ParseVMSnapshot(args []string) (VMSnapshotFlags, []string, error) {
	specs := remoteGroupFlagSpecs["vm"]["snapshot"]
	parseArgs, extraArgs := splitArgsForParsing(args, specs)
	parsed, err := parseFlags[vmSnapshotFlagsParsed](parseArgs)
	if err != nil {
		return VMSnapshotFlags{}, nil, err
	}
	flags := VMSnapshotFlags{
		Comment: strings.TrimSpace(parsed.Flags.Comment),
		Full:    parsed.Flags.Full,
	}
	argsOut := append(parsed.Args, extraArgs...)
	return flags, argsOut, nil
}
```

- [ ] **Step 4: Add registry metadata and flag specs**

Under the `vm` group in `remoteGroupInfos`, add:

```go
"snapshot": {
	Name:        "snapshot",
	Description: "Snapshot a ZFS-backed VM disk",
	Usage:       "vm snapshot <vm> [--comment=TEXT] [--full]",
	Examples: []string{
		"yeet vm snapshot devbox",
		"yeet vm snapshot devbox --comment=\"before package upgrade\"",
		"yeet vm snapshot devbox --full --comment=\"checkpoint before risky change\"",
	},
	ArgsSchema:  ServiceArgs{},
	FlagsSchema: vmSnapshotFlagsParsed{},
},
```

In `remoteGroupFlagSpecs["vm"]`, add:

```go
"snapshot": flagSpecsFromStruct(vmSnapshotFlagsParsed{}),
```

Run:

```bash
gofmt -w pkg/cli/cli.go pkg/cli/cli_test.go
```

- [ ] **Step 5: Route `vm snapshot` on catch**

In `pkg/catch/tty_vm.go`, add a case in `vmCmdFunc`:

```go
case "snapshot":
	flags, rest, err := cli.ParseVMSnapshot(args[1:])
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("unexpected vm snapshot args: %s", strings.Join(rest, " "))
	}
	return e.s.createVMSnapshot(e.vmProvisionContext(), e.sn, flags, e.rw)
```

The call will not compile until Task 4 adds `createVMSnapshot`.

- [ ] **Step 6: Run the CLI tests**

Run:

```bash
mise exec -- go test ./pkg/cli -run 'TestParseVMSnapshot|TestRemoteCommandRegistryAndFlagSpecs' -count=1
```

Expected: pass for `pkg/cli`.

- [ ] **Step 7: Commit Task 3 after Task 4 compiles**

Do not commit this task while `pkg/catch/tty_vm.go` references a missing method. After Task 4 compiles, commit these files together with Task 4 or as a separate commit if GitButler shows the workspace builds cleanly:

```bash
but status -fv
but commit codex/vm-snapshot-policy-design -m "cli: add vm snapshot command"
```

## Task 4: Implement Disk-Only VM Zvol Snapshots

**Files:**
- Create: `pkg/catch/vm_snapshot.go`
- Create: `pkg/catch/vm_snapshot_test.go`
- Modify: `pkg/catch/service_snapshots.go`
- Modify: `pkg/catch/service_snapshots_test.go`
- Modify: `pkg/catch/tty_vm.go`

- [ ] **Step 1: Write service snapshot metadata tests**

In `pkg/catch/service_snapshots_test.go`, extend `TestCreateServiceSnapshotCommand` or add this test:

```go
func TestCreateServiceSnapshotCommandWithCommentAndCheckpoint(t *testing.T) {
	var got [][]string
	runner := func(_ context.Context, args ...string) (string, string, error) {
		got = append(got, append([]string(nil), args...))
		return "", "", nil
	}
	req := snapshotCreateRequest{
		Service:    "devbox",
		Dataset:    "flash/yeet/vms/devbox/root",
		Event:      snapshotEventVMManual,
		Generation: 4,
		Now:        time.Date(2026, 6, 13, 18, 0, 0, 0, time.UTC),
		Comment:    "before upgrade",
		Checkpoint: "disk",
	}

	name, err := createServiceSnapshot(context.Background(), runner, req)
	if err != nil {
		t.Fatalf("createServiceSnapshot: %v", err)
	}
	if name != "flash/yeet/vms/devbox/root@yeet-20260613T180000Z-vm-manual-g4" {
		t.Fatalf("snapshot name = %q", name)
	}
	want := []string{
		"snapshot",
		"-o", "com.yeetrun:created-by=catch",
		"-o", "com.yeetrun:service=devbox",
		"-o", "com.yeetrun:event=vm-manual",
		"-o", "com.yeetrun:generation=4",
		"-o", "com.yeetrun:policy-version=1",
		"-o", "com.yeetrun:comment=before upgrade",
		"-o", "com.yeetrun:checkpoint=disk",
		"flash/yeet/vms/devbox/root@yeet-20260613T180000Z-vm-manual-g4",
	}
	if !reflect.DeepEqual(got, [][]string{want}) {
		t.Fatalf("zfs args = %#v, want %#v", got, [][]string{want})
	}
}
```

- [ ] **Step 2: Update snapshot helper metadata support**

In `pkg/catch/service_snapshots.go`, add the constant:

```go
snapshotEventVMManual snapshotEvent = "vm-manual"
```

Do not add `snapshotEventVMManual` to `effectiveSnapshotEvents`; it is metadata for the manual VM command, not a configurable automatic event.

Extend `snapshotCreateRequest`:

```go
Comment    string
Checkpoint string
```

In `runZFSSnapshot`, append optional properties before `snapshotName`:

```go
if comment := strings.TrimSpace(req.Comment); comment != "" {
	args = append(args, "-o", "com.yeetrun:comment="+comment)
}
if checkpoint := strings.TrimSpace(req.Checkpoint); checkpoint != "" {
	args = append(args, "-o", "com.yeetrun:checkpoint="+checkpoint)
}
args = append(args, snapshotName)
```

Because the existing code currently appends `snapshotName` inside the literal, change the literal to end before the snapshot target:

```go
args := []string{
	"snapshot",
	"-o", "com.yeetrun:created-by=catch",
	"-o", "com.yeetrun:service=" + req.Service,
	"-o", "com.yeetrun:event=" + string(req.Event),
	"-o", "com.yeetrun:generation=" + strconv.Itoa(req.Generation),
	"-o", "com.yeetrun:policy-version=1",
}
```

- [ ] **Step 3: Add dataset-specific prune helper**

In `pkg/catch/service_snapshots.go`, add:

```go
func (s *Server) pruneServiceSnapshotsForDataset(ctx context.Context, dataset string, service *db.Service, policy effectivePolicy, now time.Time, current string) error {
	if service == nil || strings.TrimSpace(dataset) == "" {
		return nil
	}
	snaps, err := listServiceSnapshots(ctx, s.zfsRunner, dataset)
	if err != nil {
		return err
	}
	for _, name := range snapshotsToPrune(snaps, service.Name, policy, now, current) {
		if err := destroySnapshot(ctx, s.zfsRunner, name); err != nil {
			return err
		}
	}
	return nil
}
```

Then reduce `pruneServiceSnapshots` to:

```go
func (s *Server) pruneServiceSnapshots(ctx context.Context, service *db.Service, policy effectivePolicy, now time.Time, current string) error {
	if service == nil || strings.TrimSpace(service.ServiceRootZFS) == "" {
		return nil
	}
	return s.pruneServiceSnapshotsForDataset(ctx, service.ServiceRootZFS, service, policy, now, current)
}
```

- [ ] **Step 4: Write failing VM snapshot tests**

Create `pkg/catch/vm_snapshot_test.go` with package `catch`. Add tests for these names and assertions:

```go
func TestVMSnapshotRejectsRawDisk(t *testing.T) {
	server := newVMProvisionTestServer(t)
	seedVMForResize(t, server, "devbox", t.TempDir(), vmDiskBackendRaw)

	err := server.createVMSnapshot(context.Background(), "devbox", cli.VMSnapshotFlags{}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "requires a ZFS zvol-backed VM") {
		t.Fatalf("createVMSnapshot error = %v, want zvol-backed rejection", err)
	}
}

func TestVMSnapshotDiskOnlyPausesSnapshotsAndResumesRunningVM(t *testing.T) {
	server := newVMProvisionTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		calls = append(calls, "zfs "+strings.Join(args, " "))
		return "", "", nil
	}
	oldRunning := vmSnapshotIsRunning
	oldController := vmSnapshotFirecracker
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return true, nil }
	vmSnapshotFirecracker = recordingVMFirecracker{calls: &calls}
	t.Cleanup(func() {
		vmSnapshotIsRunning = oldRunning
		vmSnapshotFirecracker = oldController
	})

	err := server.createVMSnapshot(context.Background(), "devbox", cli.VMSnapshotFlags{Comment: "before upgrade"}, io.Discard)
	if err != nil {
		t.Fatalf("createVMSnapshot: %v", err)
	}

	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "pause ") || !strings.Contains(joined, "zfs snapshot") || !strings.Contains(joined, "resume ") {
		t.Fatalf("calls = %#v, want pause, zfs snapshot, resume", calls)
	}
	if strings.Index(joined, "pause ") > strings.Index(joined, "zfs snapshot") || strings.Index(joined, "zfs snapshot") > strings.Index(joined, "resume ") {
		t.Fatalf("call order = %#v, want pause before snapshot before resume", calls)
	}
}
```

Add the recording fake used by the test:

```go
type recordingVMFirecracker struct {
	calls *[]string
}

func (r recordingVMFirecracker) Pause(_ context.Context, socket string) error {
	*r.calls = append(*r.calls, "pause "+socket)
	return nil
}

func (r recordingVMFirecracker) Resume(_ context.Context, socket string) error {
	*r.calls = append(*r.calls, "resume "+socket)
	return nil
}

func (r recordingVMFirecracker) CreateFullSnapshot(_ context.Context, socket string, statePath string, memPath string) error {
	*r.calls = append(*r.calls, "full "+socket+" "+statePath+" "+memPath)
	return nil
}
```

- [ ] **Step 5: Implement the VM snapshot command core**

Create `pkg/catch/vm_snapshot.go`:

```go
package catch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

type vmFirecrackerSnapshotter interface {
	Pause(context.Context, string) error
	Resume(context.Context, string) error
	CreateFullSnapshot(context.Context, string, string, string) error
}

var (
	vmSnapshotIsRunning   = (*Server).IsServiceRunning
	vmSnapshotFirecracker vmFirecrackerSnapshotter = firecrackerSnapshotAPI{}
)

type vmSnapshotResult struct {
	Name       string
	StatePath  string
	MemoryPath string
}
```

Add `createVMSnapshot`:

```go
func (s *Server) createVMSnapshot(ctx context.Context, name string, flags cli.VMSnapshotFlags, w io.Writer) error {
	service, vm, err := s.vmSnapshotService(name)
	if err != nil {
		return err
	}
	dataset, err := vmSnapshotDataset(vm.Disk)
	if err != nil {
		return err
	}
	policy, err := s.serviceSnapshotPolicy(service)
	if err != nil {
		return err
	}
	if !policy.Enabled {
		return fmt.Errorf("VM snapshots are disabled for %q; enable snapshots for the service or inherit enabled defaults", name)
	}
	runningCheck := vmSnapshotIsRunning
	if runningCheck == nil {
		runningCheck = (*Server).IsServiceRunning
	}
	running, err := runningCheck(s, name)
	if err != nil {
		return err
	}
	if flags.Full && !running {
		return fmt.Errorf("full VM checkpoints require %q to be running", name)
	}
	controller := vmSnapshotFirecracker
	if controller == nil {
		controller = firecrackerSnapshotAPI{}
	}
	socket := strings.TrimSpace(vm.Sockets.APISocketPath)
	paused := false
	if running {
		if socket == "" {
			return fmt.Errorf("service %q has no Firecracker API socket", name)
		}
		if err := controller.Pause(ctx, socket); err != nil {
			return fmt.Errorf("pause VM %q: %w", name, err)
		}
		paused = true
	}
	result, snapErr := s.createPausedVMSnapshot(ctx, service, vm, dataset, flags, controller)
	if paused {
		if err := controller.Resume(ctx, socket); err != nil {
			if snapErr != nil {
				return fmt.Errorf("%v; additionally failed to resume VM %q: %w", snapErr, name, err)
			}
			return fmt.Errorf("created VM snapshot %s but failed to resume VM %q: %w", result.Name, name, err)
		}
	}
	if snapErr != nil {
		return snapErr
	}
	if err := s.pruneServiceSnapshotsForDataset(ctx, dataset, service, policy, time.Now(), result.Name); err != nil {
		writeSnapshotWarning(w, "warning: failed to prune VM snapshots for %q: %v\n", name, err)
	}
	printVMSnapshotResult(w, result)
	return nil
}
```

Add service/dataset helpers:

```go
func (s *Server) vmSnapshotService(name string) (*db.Service, db.VMConfig, error) {
	dv, err := s.getDB()
	if err != nil {
		return nil, db.VMConfig{}, err
	}
	sv, ok := dv.Services().GetOk(name)
	if !ok {
		return nil, db.VMConfig{}, fmt.Errorf("service %q not found", name)
	}
	service := sv.AsStruct()
	if service.ServiceType != db.ServiceTypeVM || service.VM == nil {
		return nil, db.VMConfig{}, fmt.Errorf("service %q is not a VM service", name)
	}
	if strings.TrimSpace(service.VM.SetupState) != "ready" {
		return nil, db.VMConfig{}, fmt.Errorf("VM %q is not ready", name)
	}
	return service, *service.VM.Clone(), nil
}

func vmSnapshotDataset(disk db.VMDiskConfig) (string, error) {
	if disk.Backend != vmDiskBackendZVOL {
		return "", fmt.Errorf("VM snapshot requires a ZFS zvol-backed VM")
	}
	dataset := strings.TrimPrefix(strings.TrimSpace(disk.Path), "/dev/zvol/")
	dataset = strings.TrimPrefix(dataset, "/")
	if dataset == "" {
		return "", fmt.Errorf("VM zvol path is required")
	}
	return dataset, nil
}
```

Add snapshot creation and output helpers:

```go
func (s *Server) createPausedVMSnapshot(ctx context.Context, service *db.Service, vm db.VMConfig, dataset string, flags cli.VMSnapshotFlags, controller vmFirecrackerSnapshotter) (vmSnapshotResult, error) {
	now := time.Now()
	checkpoint := "disk"
	if flags.Full {
		checkpoint = "full"
	}
	name, err := createServiceSnapshot(ctx, s.zfsRunner, snapshotCreateRequest{
		Service:    service.Name,
		Dataset:    dataset,
		Event:      snapshotEventVMManual,
		Generation: service.Generation,
		Now:        now,
		Comment:    flags.Comment,
		Checkpoint: checkpoint,
	})
	if err != nil {
		return vmSnapshotResult{}, err
	}
	result := vmSnapshotResult{Name: name}
	if !flags.Full {
		return result, nil
	}
	dir := vmCheckpointDir(s.serviceRootFromService(service), snapshotShortName(snapshotCreateRequest{Event: snapshotEventVMManual, Generation: service.Generation, Now: now}))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return result, fmt.Errorf("create VM checkpoint directory: %w", err)
	}
	result.StatePath = filepath.Join(dir, "firecracker-state.bin")
	result.MemoryPath = filepath.Join(dir, "memory.bin")
	if err := controller.CreateFullSnapshot(ctx, vm.Sockets.APISocketPath, result.StatePath, result.MemoryPath); err != nil {
		return result, fmt.Errorf("create full VM checkpoint: %w", err)
	}
	return result, writeVMCheckpointMetadata(dir, service.Name, flags.Comment, name, result)
}

func vmCheckpointDir(root string, shortName string) string {
	return filepath.Join(serviceDataDirForRoot(root), "checkpoints", shortName)
}

func printVMSnapshotResult(w io.Writer, result vmSnapshotResult) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, "VM snapshot: %s\n", result.Name)
	if result.StatePath != "" {
		fmt.Fprintf(w, "Firecracker state: %s\n", result.StatePath)
		fmt.Fprintf(w, "Firecracker memory: %s\n", result.MemoryPath)
	}
}
```

If `serviceRootFromService` does not exist, add:

```go
func (s *Server) serviceRootFromService(service *db.Service) string {
	if service == nil {
		return ""
	}
	if strings.TrimSpace(service.ServiceRoot) != "" {
		return service.ServiceRoot
	}
	return s.defaultServiceRootDir(service.Name)
}
```

- [ ] **Step 6: Add Firecracker HTTP-over-UNIX client**

In `pkg/catch/vm_snapshot.go`, add:

```go
type firecrackerSnapshotAPI struct{}

func (firecrackerSnapshotAPI) Pause(ctx context.Context, socket string) error {
	return firecrackerPatchVMState(ctx, socket, "Paused")
}

func (firecrackerSnapshotAPI) Resume(ctx context.Context, socket string) error {
	return firecrackerPatchVMState(ctx, socket, "Resumed")
}

func (firecrackerSnapshotAPI) CreateFullSnapshot(ctx context.Context, socket string, statePath string, memPath string) error {
	body := map[string]string{
		"snapshot_type": "Full",
		"snapshot_path": statePath,
		"mem_file_path": memPath,
	}
	return firecrackerJSON(ctx, socket, http.MethodPut, "http://unix/snapshot/create", body)
}

func firecrackerPatchVMState(ctx context.Context, socket string, state string) error {
	return firecrackerJSON(ctx, socket, http.MethodPatch, "http://unix/vm", map[string]string{"state": state})
}

func firecrackerJSON(ctx context.Context, socket string, method string, url string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Firecracker API %s %s returned %s", method, url, resp.Status)
	}
	return nil
}
```

- [ ] **Step 7: Add checkpoint metadata writer**

In `pkg/catch/vm_snapshot.go`, add:

```go
func writeVMCheckpointMetadata(dir string, service string, comment string, zvolSnapshot string, result vmSnapshotResult) error {
	metadata := map[string]string{
		"service":          service,
		"comment":          strings.TrimSpace(comment),
		"zvolSnapshot":     zvolSnapshot,
		"firecrackerState": result.StatePath,
		"firecrackerMemory": result.MemoryPath,
		"createdBy":        "catch",
	}
	raw, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(filepath.Join(dir, "metadata.json"), raw, 0o644)
}
```

Run:

```bash
gofmt -w pkg/catch/service_snapshots.go pkg/catch/service_snapshots_test.go pkg/catch/vm_snapshot.go pkg/catch/vm_snapshot_test.go pkg/catch/tty_vm.go
```

- [ ] **Step 8: Run focused catch tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestCreateServiceSnapshotCommandWithCommentAndCheckpoint|TestVMSnapshot' -count=1
```

Expected: pass.

- [ ] **Step 9: Commit Tasks 3 and 4**

Run:

```bash
but status -fv
but commit codex/vm-snapshot-policy-design -m "catch: add vm snapshot command"
```

If GitButler requires explicit IDs, use the IDs for `pkg/cli/cli.go`, `pkg/cli/cli_test.go`, `pkg/catch/tty_vm.go`, `pkg/catch/vm_snapshot.go`, `pkg/catch/vm_snapshot_test.go`, `pkg/catch/service_snapshots.go`, and `pkg/catch/service_snapshots_test.go`.

## Task 5: Full Checkpoint Hardening And Documentation

**Files:**
- Modify: `pkg/catch/vm_snapshot_test.go`
- Modify: `website/docs/payloads/vms.mdx`
- Modify: `website/docs/concepts/zfs.mdx`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify website screenshot if the current web-run screenshot shows VM snapshot controls.

- [ ] **Step 1: Add focused full-checkpoint tests**

In `pkg/catch/vm_snapshot_test.go`, add tests for:

```go
func TestVMSnapshotFullRequiresRunningVM(t *testing.T) {
	server := newVMProvisionTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	oldRunning := vmSnapshotIsRunning
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return false, nil }
	t.Cleanup(func() { vmSnapshotIsRunning = oldRunning })

	err := server.createVMSnapshot(context.Background(), "devbox", cli.VMSnapshotFlags{Full: true}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "full VM checkpoints require") {
		t.Fatalf("createVMSnapshot error = %v, want full requires running", err)
	}
}

func TestVMSnapshotAttemptsResumeAfterSnapshotFailure(t *testing.T) {
	server := newVMProvisionTestServer(t)
	root := t.TempDir()
	seedVMForResize(t, server, "devbox", root, vmDiskBackendZVOL)
	var calls []string
	server.zfsRunner = func(_ context.Context, args ...string) (string, string, error) {
		calls = append(calls, "zfs "+strings.Join(args, " "))
		return "", "permission denied", errors.New("zfs failed")
	}
	oldRunning := vmSnapshotIsRunning
	oldController := vmSnapshotFirecracker
	vmSnapshotIsRunning = func(*Server, string) (bool, error) { return true, nil }
	vmSnapshotFirecracker = recordingVMFirecracker{calls: &calls}
	t.Cleanup(func() {
		vmSnapshotIsRunning = oldRunning
		vmSnapshotFirecracker = oldController
	})

	err := server.createVMSnapshot(context.Background(), "devbox", cli.VMSnapshotFlags{}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "zfs snapshot") {
		t.Fatalf("createVMSnapshot error = %v, want zfs snapshot error", err)
	}
	if !strings.Contains(strings.Join(calls, "\n"), "resume ") {
		t.Fatalf("calls = %#v, want resume after failure", calls)
	}
}
```

- [ ] **Step 2: Run full checkpoint tests**

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestVMSnapshotFullRequiresRunningVM|TestVMSnapshotAttemptsResumeAfterSnapshotFailure|TestVMSnapshotDiskOnlyPausesSnapshotsAndResumesRunningVM' -count=1
```

Expected: pass after Task 4 implementation is adjusted for these failure paths.

- [ ] **Step 3: Update VM payload docs**

In `website/docs/payloads/vms.mdx`, add a section after `ZFS-backed VM disks`:

````mdx
## VM snapshots

ZFS-backed VMs can be snapshotted manually:

```bash
yeet vm snapshot <vm>
yeet vm snapshot <vm> --comment "before package upgrade"
yeet vm snapshot <vm> --full --comment "checkpoint before risky change"
```

The default command snapshots the VM zvol. If the VM is running, catch pauses
the Firecracker VM, snapshots the zvol, then resumes it. `--full` also writes
Firecracker state and memory checkpoint files so advanced workflows can keep a
complete VM checkpoint.

Manual VM snapshots use the same retention policy fields as other yeet
snapshots: `--snapshots`, `--snapshot-keep-last`, and `--snapshot-max-age`.
The Docker-style snapshot event list does not apply to VMs.
````

- [ ] **Step 4: Update ZFS and CLI docs**

In `website/docs/concepts/zfs.mdx`, add a VM-specific note in the snapshot section:

```mdx
For VM payloads, the policy controls retention for manual `yeet vm snapshot`
commands. VM snapshots target the VM zvol instead of the service-root dataset.
`--snapshot-events` and `--snapshot-required` are for automatic service-root
snapshots and are not shown in the VM web-run snapshot controls.
```

In `website/docs/cli/yeet-cli.mdx`, add `yeet vm snapshot` to the VM commands section:

````mdx
### vm snapshot

Create a manual snapshot for a ZFS-backed VM:

```bash
yeet vm snapshot devbox
yeet vm snapshot devbox --comment "before package upgrade"
yeet vm snapshot devbox --full --comment "checkpoint before risky change"
```

Disk snapshots are the default. `--full` also stores Firecracker state and
memory checkpoint files.
````

- [ ] **Step 5: Refresh screenshot if needed**

Search for the current web-run screenshot reference:

```bash
rg -n "web-run|run --web|screenshot|png|jpg|webp" website/docs website/public
```

If the screenshot includes the snapshot section, start the local docs/app workflow used in this repo, open `yeet run --web` with a VM workload, capture a new screenshot after the `VM snapshots` UI change, replace the referenced image, and verify the website page renders. Commit inside `website/` first, then commit the root submodule pointer.

- [ ] **Step 6: Run docs and focused package tests**

Run:

```bash
mise exec -- go test ./pkg/yeet ./pkg/cli ./pkg/catch -count=1
```

Expected: pass.

Run the website verification command documented in `website/AGENTS.md`. If no specific command exists, run the website package manager's lint/build command from `website/`.

- [ ] **Step 7: Commit Task 5**

In `website/`, use GitButler if configured there; otherwise follow that repo's `AGENTS.md` version-control instructions. Commit website docs and screenshot changes first.

In the root repo, run:

```bash
but status -fv
but commit codex/vm-snapshot-policy-design -m "docs: document vm snapshots"
```

If GitButler requires explicit IDs, use the IDs for the docs files and website submodule pointer.

## Task 6: Final Verification And Live Disposable VM Test

**Files:**
- No planned code changes. Fix any real failures in the relevant files and commit them coherently.

- [ ] **Step 1: Run targeted tests**

Run:

```bash
mise exec -- go test ./pkg/yeet ./pkg/cli ./pkg/catch -count=1
```

Expected: pass.

- [ ] **Step 2: Run the deterministic local gate**

Run:

```bash
mise exec -- pre-commit run --all-files
```

Expected: pass. If it reports issues, fix real issues instead of refreshing baselines.

- [ ] **Step 3: Live test on `yeet-lab` with a disposable ZFS-backed VM**

Use a disposable service name that does not collide with existing services, for example:

```bash
export CATCH_HOST=yeet-lab
mise exec -- go run ./cmd/yeet status
mise exec -- go run ./cmd/yeet run codex-vm-snap-test vm://ubuntu/26.04 --net=svc --service-root=flash/yeet/vms/codex-vm-snap-test --zfs --snapshots=on --snapshot-keep-last=2 --snapshot-max-age=24h --restart=false
mise exec -- go run ./cmd/yeet vm snapshot codex-vm-snap-test --comment "codex disk snapshot smoke"
ssh root@lab-host "zfs list -H -t snapshot -o name,com.yeetrun:service,com.yeetrun:event,com.yeetrun:comment | grep codex-vm-snap-test"
```

Expected:

- VM creation succeeds without starting the VM.
- `yeet vm snapshot` prints a `VM snapshot:` line.
- `zfs list` shows a `@yeet-...-vm-manual-...` snapshot with service `codex-vm-snap-test` and the smoke-test comment.

If full checkpoint is safe to test on the disposable VM, start it and run:

```bash
mise exec -- go run ./cmd/yeet start codex-vm-snap-test
mise exec -- go run ./cmd/yeet vm snapshot codex-vm-snap-test --full --comment "codex full checkpoint smoke"
```

Expected:

- Command pauses and resumes the running VM.
- Output includes Firecracker state and memory file paths.

Clean up:

```bash
mise exec -- go run ./cmd/yeet rm --clean-data codex-vm-snap-test --yes
```

- [ ] **Step 4: Commit any verification fixes**

If verification exposed real bugs, commit the fix with GitButler:

```bash
but status -fv
but commit codex/vm-snapshot-policy-design -m "fix: harden vm snapshot verification"
```

- [ ] **Step 5: Final status**

Run:

```bash
but status -fv
```

Expected:

- No unassigned changes.
- The `codex/vm-snapshot-policy-design` branch contains coherent local commits.
- Do not push or merge unless the user asks.

## Self-Review

Spec coverage:

- VM web UI hides `Events` and `Required`: Task 1.
- VM web UI keeps mode, keep last, max age: Task 1.
- VM web validation rejects direct events/required submissions: Task 1.
- Existing policy model reused and persisted for VM creation: Task 2.
- `yeet vm snapshot` CLI surface: Task 3.
- Disk-only zvol snapshots by default: Task 4.
- Firecracker pause/resume for running VMs: Task 4.
- `--comment` metadata: Task 4.
- `--full` state+memory checkpoints: Tasks 4 and 5.
- Retention pruning by keep-last/max-age: Task 4.
- Raw-disk rejection and stopped-full rejection: Tasks 4 and 5.
- Docs and live disposable VM verification: Tasks 5 and 6.

Placeholder scan: this plan intentionally uses CLI metavariables like `<vm>` only in user-facing command examples. It does not contain unresolved implementation placeholders.

Type consistency: `cli.VMSnapshotFlags`, `ParseVMSnapshot`, `createVMSnapshot`, `vmFirecrackerSnapshotter`, `snapshotEventVMManual`, and `snapshotCreateRequest.Comment/Checkpoint` are introduced before later tasks use them.
