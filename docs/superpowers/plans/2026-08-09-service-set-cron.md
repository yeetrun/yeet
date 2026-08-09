# Service Set Cron Updates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `yeet service set <svc> --cron="..."` as a payload-free way to change the schedule of an existing scheduled native service without allowing workload-type conversion.

**Architecture:** Extend the shared `pkg/cli` service-set schema so the existing command bridge carries the new flag unchanged and both client and Catch parse the same normalized five-field expression. Catch remains authoritative: under the existing `manage` permission and per-service operation lock, it verifies the active generation is scheduled native, clones every active-generation artifact into a new generation, replaces only the timer schedule, and applies that generation through the existing journaled native-generation transaction. The client updates only `ServiceEntry.Schedule` after remote success; docs, generated help, the disposable ISO smoke test, and the `v0.10.16` release are completed from the same candidate.

**Tech Stack:** Go, `pkg/cli`, command-shaped Catch RPC, Catch service-generation and systemd lifecycle code, TOML project configuration, systemd timers, GitButler, MDX documentation, GitHub Actions release assets.

## Global Constraints

- The command is exactly `yeet service set <svc> --cron="<five-field crontab>"`; do not add `yeet set`, restore `yeet cron`, or add a cron reset/clear flag.
- `--cron` is non-empty, once-only, normalized by the existing `parseRunCron`, and exclusive with every other `service set` mutation flag.
- Catch must decide eligibility from the active installed generation while holding the target service's existing operation lock.
- Only an active scheduled native service is eligible. Missing, ordinary native, Docker/Compose, VM, historical-only, staged-only, inconsistent, and unreadable services fail without mutation.
- Reuse the installed server-side payload and preserve payload checksum, kind, arguments, identity, roots, environment, publication, snapshots, network state, and every active-generation artifact other than the timer schedule.
- The mutation creates a rollback-safe generation; it must not edit the installed timer outside Catch's generation transaction.
- The client never uploads, resolves, or requires a local payload for this operation and never uses local config as eligibility proof.
- Remote success is authoritative. Update only `yeet.toml.schedule`; on missing or unsavable local config, preserve the remote result and print an exact `yeet service sync <svc>` recovery command.
- The existing `service set` route retains `manage`; no new RPC method or permission class is introduced.
- Verification must not run or target `<protected-scheduled-service>`. Live validation uses uniquely named disposable services, a far-future schedule, ISO networking, and complete cleanup proof.
- Public examples use generic service names and `yeetrun.com`; no private hosts, service names, usernames, or local paths enter committed documentation.
- Use `mise exec -- go ...`, normal pre-commit hooks, `mise run quality`, and one final `mise run quality:goal`; do not lower or refresh quality baselines.
- Root version-control writes use GitButler. Preserve the pending unpublished website changelog commit and commit/push the website repository before recording its root gitlink.
- The release version is `v0.10.16`, with an annotated tag whose message is exactly `v0.10.16`.

---

### Task 1: Shared CLI contract, help metadata, bridge, and parser fuzzing

**Files:**
- Modify: `pkg/cli/cli.go`
- Modify: `pkg/cli/cli_test.go`
- Create: `pkg/cli/service_set_cron_fuzz_test.go`
- Modify: `cmd/yeet/cli_bridge_test.go`
- Modify: `cmd/yeet/cli_test.go`

**Interfaces:**
- Consumes: existing `parseRunCron(args []string, value string) (string, bool, error)` and registry-derived `remoteGroupFlagSpecs["service"]["set"]`.
- Produces: `cli.ServiceSetFlags.Cron string`, `cli.ServiceSetFlags.CronSet bool`, and a `serviceSetFlagsParsed.Cron` schema field recognized identically by client and Catch.
- Produces: shared parser rejection for every `CronSet` plus non-cron mutation combination before remote execution.

- [ ] **Step 1: Add failing parser tests for the command contract**

Add a table to `TestParseServiceSetFlags` and a focused exclusivity test. The assertions must cover whitespace normalization, empty, repeated, short, invalid, and representative combinations from every mutation family:

```go
func TestParseServiceSetCron(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    ServiceSetFlags
		wantErr string
	}{
		{name: "cron only", args: []string{"reports", `--cron= 30  2 * * * `}, want: ServiceSetFlags{Cron: "30 2 * * *", CronSet: true}},
		{name: "empty", args: []string{"reports", "--cron="}, wantErr: "--cron requires a five-field expression"},
		{name: "repeated", args: []string{"reports", `--cron=0 2 * * *`, `--cron=0 3 * * *`}, wantErr: "--cron may only be supplied once"},
		{name: "short", args: []string{"reports", `--cron=0 2 * *`}, wantErr: "cron expression must have 5 fields"},
		{name: "invalid", args: []string{"reports", `--cron=99 99 * * *`}, wantErr: "invalid cron expression"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, args, err := ParseServiceSet(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseServiceSet error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tt.want) || !reflect.DeepEqual(args, []string{"reports"}) {
				t.Fatalf("ParseServiceSet = %#v %#v %v, want %#v reports nil", got, args, err, tt.want)
			}
		})
	}
}

func TestParseServiceSetCronRejectsOtherMutations(t *testing.T) {
	for _, args := range [][]string{
		{"reports", `--cron=30 2 * * *`, "--run-as=backup"},
		{"reports", `--cron=30 2 * * *`, "--net=iso"},
		{"reports", `--cron=30 2 * * *`, "--service-root=/srv/reports", "--copy"},
		{"reports", `--cron=30 2 * * *`, "--publish=8080:80"},
		{"reports", `--cron=30 2 * * *`, "--snapshots=off"},
	} {
		if _, _, err := ParseServiceSet(args); err == nil || !strings.Contains(err.Error(), "--cron cannot be combined with other service settings") {
			t.Fatalf("ParseServiceSet(%#v) error = %v", args, err)
		}
	}
}
```

- [ ] **Step 2: Add failing registry, leaf-help, and bridge tests**

Update the exact service-set usage/examples assertions in `pkg/cli/cli_test.go`, require `--cron` to consume a value, add inline and separate-value bridge cases, and require leaf help to contain the new syntax:

```go
{args: []string{"service", "set", "reports", `--cron=30 2 * * *`}, wantService: "reports", want: []string{"service", "set", `--cron=30 2 * * *`}},
{args: []string{"service", "set", "--cron", "30 2 * * *", "reports"}, wantService: "reports", want: []string{"service", "set", "--cron", "30 2 * * *"}},
```

Run:

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet -run 'TestParseServiceSetCron|TestBridgeServiceArgsServiceSet|TestCLIServiceSetHelp|TestCommandRegistry' -count=1
```

Expected: FAIL because `service set` does not yet register or parse `--cron`.

- [ ] **Step 3: Implement the shared parser fields and exclusivity rule**

Insert these fields into the existing structs and reuse the existing parser:

```go
// ServiceSetFlags
Cron    string
CronSet bool

// serviceSetFlagsParsed
Cron string `flag:"cron" help:"Update the schedule of an existing scheduled native service with a five-field cron expression"`
```

At the start of `serviceSetFlagsFromParsed`, call:

```go
cron, cronSet, err := parseRunCron(parseArgs, parsed.Cron)
if err != nil {
	return ServiceSetFlags{}, err
}
```

Populate `Cron` and `CronSet`, then make cron-only a change while keeping a separate non-cron predicate:

```go
func serviceSetHasNonCronChange(flags ServiceSetFlags, rootChange bool) bool {
	return flags.RunAsSet || flags.HasNetworkChange() || rootChange ||
		flags.SnapshotChange || hasServiceSetPublishChange(flags)
}

func serviceSetHasChange(flags ServiceSetFlags, rootChange bool) bool {
	return flags.CronSet || serviceSetHasNonCronChange(flags, rootChange)
}

func validateServiceSetCronExclusivity(flags ServiceSetFlags) error {
	if flags.CronSet && serviceSetHasNonCronChange(flags, hasServiceSetRootChange(flags)) {
		return fmt.Errorf("--cron cannot be combined with other service settings; apply them with separate service set commands")
	}
	return nil
}
```

Call the exclusivity validator before root/migration validation so mixed cron mutations receive the schedule-specific error instead of an unrelated root error.

Update `CommandInfo.Usage` and examples with:

```go
"yeet service set <svc> --cron=\"30 2 * * *\"",
```

- [ ] **Step 4: Add a parser fuzz target**

Create `pkg/cli/service_set_cron_fuzz_test.go`:

```go
func FuzzParseServiceSetCron(f *testing.F) {
	for _, seed := range []string{"0 3 * * *", " 30  2 * * * ", "", "* * * * *"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		flags, args, err := ParseServiceSet([]string{"reports", "--cron=" + raw})
		if err != nil {
			return
		}
		if !flags.CronSet || flags.Cron == "" || !reflect.DeepEqual(args, []string{"reports"}) {
			t.Fatalf("successful parse lost cron state: %#v %#v", flags, args)
		}
		reparsed, reparsedArgs, err := ParseServiceSet([]string{"reports", "--cron=" + flags.Cron})
		if err != nil || reparsed.Cron != flags.Cron || !reparsed.CronSet || !reflect.DeepEqual(reparsedArgs, args) {
			t.Fatalf("canonical parse is unstable: %#v %#v %v", reparsed, reparsedArgs, err)
		}
	})
}
```

- [ ] **Step 5: Run focused tests and fuzzing**

```bash
mise exec -- go test ./pkg/cli ./cmd/yeet -count=1
mise exec -- go test ./pkg/cli -run '^$' -fuzz '^FuzzParseServiceSetCron$' -fuzztime=10s
```

Expected: PASS with no minimized crash corpus.

- [ ] **Step 6: Create the Task 1 checkpoint with GitButler**

Run `but diff --format agent`, select only the five Task 1 paths listed above, and commit those printed file IDs to the existing `service-set-cron` branch with message:

```text
cli: parse service set cron updates
```

The returned GitButler status must still show `website` as uncommitted and must not assign it to the feature branch.

---

### Task 2: Catch active-generation schedule transaction

**Files:**
- Create: `pkg/catch/service_schedule_mutation.go`
- Create: `pkg/catch/service_schedule_mutation_test.go`
- Modify: `pkg/catch/tty_service_set.go`
- Modify: `pkg/catch/tty_service_set_test.go`
- Modify: `pkg/catch/tty_authz_test.go`

**Interfaces:**
- Consumes: `cli.ServiceSetFlags.Cron`, `CronSet`, `cronutil.CronToCalender`, `activeGenerationArtifactPath`, `effectiveServiceIdentity`, `newSystemdInstallService`, `stagedNativeIdentityGeneration`, and the existing journaled `migrateServiceIdentityLocked` transaction.
- Produces: `(*Server).updateServiceScheduleLocked(ctx context.Context, name, cron string, out io.Writer) error`.
- Produces: `serviceScheduleMutationPlan` containing the exact previous record, a next-generation target record, the replacement primary unit, install paths/intents/units, and a cleanup path for the newly staged timer source.
- Invariant: the target record is built only from refs at `db.Gen(previous.Generation)`; `latest`, `staged`, and historical refs never establish eligibility or leak into the new active generation.

- [ ] **Step 1: Add failing eligibility and routing tests**

Add table-driven tests that seed exact database records and require failure before the mutation callback for missing, ordinary native, Compose, VM, historical-only timer, and staged-only timer cases. The eligible fixture must have both active-generation service and timer refs:

```go
service := &db.Service{
	Name: "reports", ServiceType: db.ServiceTypeSystemd,
	Generation: 4, LatestGeneration: 4,
	Artifacts: db.ArtifactStore{
		db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(4): unitPath, "latest": unitPath}},
		db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{db.Gen(4): timerPath, "latest": timerPath}},
	},
}
```

Add a dispatch-level test that calls:

```go
err := execer.dispatch([]string{"service", "set", "--cron=30 2 * * *"})
```

and asserts the schedule callback observes `execer.serviceOperationLockHeld == true`. Extend `tty_authz_test.go` to require exactly `permissionManage` for the same argv.

Run:

```bash
mise exec -- go test ./pkg/catch -run 'TestServiceSetCron|TestTTYAuthorizationCommandPermissions' -count=1
```

Expected: FAIL because Catch does not route a schedule change.

- [ ] **Step 2: Add failing timer rewrite and target-generation preservation tests**

Test a timer containing `[Unit]`, `[Timer]`, `Persistent=true`, accuracy/randomization directives, and `[Install]`. Rewriting must replace exactly one `OnCalendar` line and preserve every other byte-level line and final newline. Reject missing or repeated `OnCalendar` directives.

Test target construction with identity, service roots, snapshot policy, publish settings, desired/effective network records, ISO allocation, payload/env/unit/timer/netns/Tailscale artifacts, and stale staged refs. Require:

```go
if target.Generation != 5 || target.LatestGeneration != 5 {
	t.Fatalf("target generation = %d/%d, want 5/5", target.Generation, target.LatestGeneration)
}
```

For every artifact active at gen 4, gen 5 must point at the same path except `systemd.timer`, which must point at the newly versioned timer source. Historical-only artifacts must not gain gen 5. The original record must remain deeply equal to its pre-call clone.

Run the same focused command and verify these tests fail before implementation.

- [ ] **Step 3: Implement fail-closed active-generation planning**

Create `service_schedule_mutation.go` with a focused planner:

```go
const scheduledServiceSetOnlyMessage = "--cron only updates an existing scheduled native service; deploy a scheduled native payload with `yeet run <svc> <payload> --cron=...`"

type serviceScheduleMutationPlan struct {
	previous       *db.Service
	target         *db.Service
	identity       resolvedServiceIdentity
	replacement    string
	generationPaths []string
	intent         []serviceIdentityPathState
	units          []string
	stage          func(context.Context) error
	stagedTimer    string
	noOp           bool
}
```

The planner must:

```go
func (s *Server) planServiceScheduleMutation(name, cron string) (*serviceScheduleMutationPlan, error) {
	sv, err := s.serviceView(name)
	if err != nil {
		return nil, fmt.Errorf("inspect service %q for schedule update: %w", name, err)
	}
	if sv.ServiceType() != db.ServiceTypeSystemd {
		return nil, fmt.Errorf("service %q: %s", name, scheduledServiceSetOnlyMessage)
	}
	if sv.Generation() <= 0 {
		return nil, fmt.Errorf("service %q: %s", name, scheduledServiceSetOnlyMessage)
	}
	unitPath, unitOK := activeGenerationArtifactPath(sv, db.ArtifactSystemdUnit)
	timerPath, timerOK := activeGenerationArtifactPath(sv, db.ArtifactSystemdTimerFile)
	if !unitOK || !timerOK || strings.TrimSpace(unitPath) == "" || strings.TrimSpace(timerPath) == "" {
		return nil, fmt.Errorf("service %q: %s", name, scheduledServiceSetOnlyMessage)
	}
	if err := validateActiveScheduleArtifact(unitPath); err != nil {
		return nil, fmt.Errorf("validate active service artifact for %q: %w", name, err)
	}
	if err := validateActiveScheduleArtifact(timerPath); err != nil {
		return nil, fmt.Errorf("validate active timer artifact for %q: %w", name, err)
	}
	desired, err := cronutil.CronToCalender(cron)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression: %w", err)
	}
	current, err := readSystemdTimerConfig(timerPath)
	if err != nil {
		return nil, fmt.Errorf("read active timer for service %q: %w", name, err)
	}
	previous := sv.AsStruct()
	identity := effectiveServiceIdentity(sv)
	if strings.TrimSpace(current.OnCalendar) == strings.TrimSpace(desired) {
		return &serviceScheduleMutationPlan{previous: previous, identity: identity, noOp: true}, nil
	}
	raw, err := os.ReadFile(timerPath)
	if err != nil {
		return nil, fmt.Errorf("read active timer bytes for service %q: %w", name, err)
	}
	rewritten, err := rewriteSystemdTimerCalendar(string(raw), desired)
	if err != nil {
		return nil, fmt.Errorf("rewrite active timer for service %q: %w", name, err)
	}
	stagedTimer, err := stageEditedSystemdUnit(timerPath, rewritten)
	if err != nil {
		return nil, fmt.Errorf("stage timer for service %q: %w", name, err)
	}
	return &serviceScheduleMutationPlan{
		previous: previous, identity: identity, stagedTimer: stagedTimer,
	}, nil
}
```

Implement `validateActiveScheduleArtifact` with `os.Lstat`, reject symlinks and non-regular files, and open/read the artifact so permission and I/O failures are returned before a no-op can succeed. Do not fall back to `Artifacts.Latest`, `Artifacts.Staged`, service data type, or local config. If planning fails after creating `stagedTimer`, remove only that newly created versioned source before returning.

- [ ] **Step 4: Build a complete next generation with only the timer changed**

Implement `rewriteSystemdTimerCalendar(raw, desired)` and create the versioned timer source with the same host-controlled copy/provenance helpers used by systemd edit staging. Build the target from a deep clone:

```go
func cloneActiveServiceGeneration(previous *db.Service, stagedTimer string) (*db.Service, error) {
	target := previous.Clone()
	for _, artifact := range target.Artifacts {
		if artifact == nil {
			continue
		}
		delete(artifact.Refs, db.ArtifactRef("staged"))
		if path, ok := artifact.Refs[db.Gen(previous.Generation)]; ok {
			artifact.Refs[db.ArtifactRef("staged")] = path
		}
	}
	timer := target.Artifacts[db.ArtifactSystemdTimerFile]
	if timer == nil || timer.Refs == nil {
		return nil, errors.New(scheduledServiceSetOnlyMessage)
	}
	timer.Refs[db.ArtifactRef("staged")] = stagedTimer
	commitGeneratedServiceRefs(nil, target, target.Name, generatedServiceCommitForGen(0, target.LatestGeneration))
	return target, nil
}
```

Create a `svc.SystemdService` from the target record and fill the plan with:

```go
replacement, err := generationService.RenderedPrimaryUnit()
units := generationService.InstallUnits()
states, err := generationService.InstallTargetStatesExcluding(generationService.PrimaryUnitPath())
plan.replacement = replacement
plan.generationPaths = generationService.InstallTargetPaths()
plan.intent = serviceIdentityInstallTargetStates(states)
plan.units = units
plan.stage = stagedNativeIdentityGeneration(generationService, units)
```

This copies the complete generation inventory, including ISO/netns/Tailscale artifacts, while changing only the timer source.

- [ ] **Step 5: Apply the plan through the journaled native-generation transaction**

Implement:

```go
func (s *Server) updateServiceScheduleLocked(ctx context.Context, name, cron string, out io.Writer) (retErr error) {
	plan, err := s.planServiceScheduleMutation(name, cron)
	if err != nil || plan.noOp {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			retErr = errors.Join(retErr, s.cleanupFailedServiceScheduleTimer(plan.previous, plan.stagedTimer))
		}
	}()
	_, err = s.migrateServiceIdentityLocked(ctx, serviceIdentityMigrationRequest{
		Service: plan.previous.Name,
		Requested: plan.identity.Persisted.RequestedUser + ":" + plan.identity.Persisted.RequestedGroup,
		Target: plan.identity,
		TargetService: plan.target,
		ReplacementUnit: plan.replacement,
		StageGeneration: plan.stage,
		GenerationPaths: plan.generationPaths,
		GenerationIntents: plan.intent,
		GenerationUnits: plan.units,
	}, out)
	if err != nil {
		return fmt.Errorf("update schedule for service %q: %w", name, err)
	}
	committed = true
	return nil
}
```

`cleanupFailedServiceScheduleTimer` is deliberately conservative. It may remove the newly created source only when all of the following are true after the migration returns:

- `checkServiceIdentityRecoveryMutationAllowed(previous.Name)` succeeds, proving no retained recovery journal or global recovery block still owns the failed transaction;
- the current database record exists and is deeply equal to `previous`; and
- the candidate path is the exact versioned timer source created by this plan.

If recovery is blocked, the database differs, or either state cannot be inspected, retain the staged source for restart recovery and join an explanatory cleanup error only for inspection failures. Never remove the source while an incomplete journal may still reference it. A later successful recovery or retry owns final cleanup.

Route `serviceSetChanges.schedule` before other mutation handlers and repeat exclusivity validation Catch-side even though `ParseServiceSet` already enforces it. `applyServiceSetScheduleChange` must call `e.withLockedServiceMutation`; this acquires the keyed lock for direct/internal callers and recognizes `serviceOperationLockHeld` during normal dispatch, avoiding a nested lock. Inside that closure call only `migrateServiceIdentityLocked`, never the unlocked migration entrypoint.

Extend the orchestration fields explicitly:

```go
type serviceSetChanges struct {
	schedule bool
	identity bool
	network  bool
	root     bool
	publish  bool
	snapshot bool
}

func (c serviceSetChanges) any() bool {
	return c.schedule || c.identity || c.network || c.root || c.publish || c.snapshot
}
```

When `schedule` is true and any other field is true, return the same separate-command guidance as the shared parser. Update the no-change error to list `--cron` among accepted settings.

- [ ] **Step 6: Add failure-injection tests for rollback and no execution**

Use the migration transaction's injected ops to fail after generation staging and during activation. Require:

- the previous database record remains exact;
- the installed timer and service bytes/modes/ownership are restored;
- previous timer/service/auxiliary enablement and active state are restored;
- a fully rolled-back failure removes the uncommitted staged timer source;
- an incomplete rollback with a retained recovery journal keeps that source, blocks mutation, and succeeds on recovery/retry before the source is cleaned;
- a retry succeeds;
- timer-active/service-inactive input never invokes the service-unit start seam; and
- no-op schedule input creates no generation, file, systemctl call, or DB write.

Retain a success test that reads the new active timer, asserts the requested `OnCalendar`, verifies `Persistent` and all other directives survived, and checks all non-timer target refs and service fields against the previous record.

- [ ] **Step 7: Run Catch-focused tests**

```bash
mise exec -- go test ./pkg/catch -run 'TestServiceSetCron|TestServiceSchedule|TestTTYAuthorizationCommandPermissions' -count=1
mise exec -- go test ./pkg/catch -count=1
```

Expected: PASS. Do not run a live service in this task.

- [ ] **Step 8: Create the Task 2 checkpoint with GitButler**

Run `but diff --format agent`, select only the five Task 2 paths, and commit those printed IDs to `service-set-cron` with message:

```text
catch: update installed timer schedules safely
```

The returned status must leave the website gitlink uncommitted.

---

### Task 3: Client configuration synchronization and compatibility guidance

**Files:**
- Modify: `pkg/yeet/svc_cmd.go`
- Modify: `pkg/yeet/svc_cmd_branch_test.go`
- Modify: `pkg/yeet/svc_cmd_routing_test.go`

**Interfaces:**
- Consumes: `cli.ServiceSetFlags.Cron/CronSet`, existing remote-first `handleServiceSet`, `saveServiceSetConfig`, `serviceSetSyncCommand`, and `printServiceSetSyncHint`.
- Produces: schedule-only `ServiceEntry.Schedule` persistence after Catch success.
- Produces: `serviceScheduleConfigWriteError(req svcCommandRequest, err error) error` with host/config-qualified recovery guidance.
- Produces: old-Catch schedule-update guidance without client-side redeploy fallback.

- [ ] **Step 1: Add failing remote-first and config-isolation tests**

Add tests that seed an entry with payload, type, run-as, service root/ZFS, args including the `--` boundary, ports, snapshots, and schedule. Execute `handleServiceSet` with `--cron=30 2 * * *` and require:

```go
if entry.Schedule != "30 2 * * *" {
	t.Fatalf("schedule = %q", entry.Schedule)
}
if !reflect.DeepEqual(entry.Args, original.Args) || entry.Payload != original.Payload || entry.RunAs != original.RunAs {
	t.Fatalf("schedule update changed unrelated config: %#v", entry)
}
```

Also require:

- remote argv is exactly `[]string{"service", "set", "--cron=30 2 * * *"}`;
- no stdin/payload reader is supplied;
- a remote error leaves the local file byte-identical;
- no matching config entry prints the existing host-qualified sync command and creates no file; and
- config save failure reports remote partial success and the exact quoted `service sync` recovery command.

Run:

```bash
mise exec -- go test ./pkg/yeet -run 'TestServiceSetCron|TestServiceSetSchedule' -count=1
```

Expected: FAIL because schedule flags are not applied or reported specially.

- [ ] **Step 2: Persist only the normalized schedule**

Update `applyServiceSetConfigFlags`:

```go
if flags.CronSet {
	entry.Schedule = strings.TrimSpace(flags.Cron)
}
```

Keep this independent of `entry.Args`; do not insert a stored `--cron` run argument because `ServiceEntry.Schedule` is the canonical config field.

- [ ] **Step 3: Add schedule-specific partial-success and old-Catch errors**

In `saveServiceSetResult`, route a schedule save error before generic errors:

```go
if flags.CronSet {
	return serviceScheduleConfigWriteError(req, err)
}
```

Implement:

```go
func serviceScheduleConfigWriteError(req svcCommandRequest, err error) error {
	configPath := ""
	if req.Config != nil {
		configPath = strings.TrimSpace(req.Config.Path)
	}
	return fmt.Errorf("remote schedule changed, but failed to update %s: %w; recover with `%s`",
		projectConfigName, err, serviceSetSyncCommand(req.Service, serviceSetSyncHintHost(req), configPath))
}
```

Extend `wrapServiceSetRemoteError` only for a real `remoteExitError` and `CronSet`; append concise `yeet init`/Catch-upgrade guidance. Do not retry as `yeet run`, open a local payload, or modify config after remote failure.

- [ ] **Step 4: Run client and cross-boundary tests**

```bash
mise exec -- go test ./pkg/yeet -count=1
mise exec -- go test ./pkg/cli ./cmd/yeet ./pkg/yeet ./pkg/catch ./pkg/catchrpc -count=1
```

Expected: PASS.

- [ ] **Step 5: Create the Task 3 checkpoint with GitButler**

Run `but diff --format agent`, select only the three Task 3 paths, and commit those printed IDs to `service-set-cron` with message:

```text
yeet: sync service schedule updates
```

The website gitlink remains uncommitted.

---

### Task 4: README, manual, generated help, release assets, and changelog

**Files:**
- Modify: `README.md`
- Modify: `.codex/skills/yeet-cli/references/yeet-help-agent.md` (generated)
- Modify: `pkg/yeet/release_assets_test.go`
- Modify: `website/docs/payloads/cron-jobs.mdx`
- Modify: `website/docs/cli/yeet-cli.mdx`
- Modify: `website/docs/operations/workflows.mdx`
- Modify: `website/docs/changelog.mdx`

**Interfaces:**
- Consumes: final `pkg/cli` command registry and `tools/generate-yeet-help-agent.sh`.
- Produces: evergreen documentation distinguishing deployment/redeployment (`run --cron`) from payload-free schedule mutation (`service set --cron`).
- Produces: amended unpublished website release commit for `v0.10.16`; it remains unpublished until Task 5.

- [ ] **Step 1: Add a failing release-surface assertion**

Extend `TestReleaseAssetsMatchCurrentCLI` so the generated help's `service set` section and README schedule section both contain `service set` plus `--cron`, while the current-surface scan still rejects standalone `yeet cron`:

```go
serviceSetStart := strings.Index(help, "### `service set`")
serviceSetEnd := strings.Index(help[serviceSetStart+1:], "\n### `")
if serviceSetStart < 0 || serviceSetEnd < 0 || !strings.Contains(help[serviceSetStart:serviceSetStart+1+serviceSetEnd], "--cron") {
	t.Error("generated service set help does not document --cron")
}
```

Run:

```bash
mise exec -- go test ./pkg/yeet -run TestReleaseAssetsMatchCurrentCLI -count=1
```

Expected: FAIL because generated help and docs have not been regenerated/updated.

- [ ] **Step 2: Update evergreen README and manual wording**

Use these exact public semantics in each surface:

```bash
# Deploy or redeploy a scheduled payload
yeet run backup ./backup --cron="0 3 * * *" --run-as=backup --net=iso -- --full

# Change only the schedule; no payload is required
yeet service set backup --cron="30 2 * * *"
```

State directly that `service set --cron`:

- works only for an already scheduled native binary or script;
- never turns an ordinary, container, or VM service into a scheduled service;
- cannot clear a schedule and cannot combine with another service mutation;
- preserves the server-side payload and other settings; and
- updates a matching `yeet.toml`, with `yeet service sync <svc>` as recovery when local config is absent or unsavable.

Apply this to `README.md`, `cron-jobs.mdx`, the `service set` and scheduled-run sections of `yeet-cli.mdx`, and the scheduled workflow in `workflows.mdx`. Do not describe ordinary evergreen behavior as “new.”

- [ ] **Step 3: Amend the pending `v0.10.16` changelog**

Keep the August 9 section at three or fewer bullets. Amend the first bullet so it stands alone and covers both forms, for example:

```mdx
- Native scheduled jobs now deploy through `yeet run <svc> <payload> --cron="..."`,
  and existing jobs can change timing without a local payload through
  `yeet service set <svc> --cron="..."`; both paths preserve the job's native
  identity, arguments, storage, and supported network isolation.
```

Retain the reliability and cleanup bullets from the existing `v0.10.16` draft. Do not mention commits, submodule pointers, smoke services, or CI.

- [ ] **Step 4: Regenerate help and verify public content**

```bash
tools/generate-yeet-help-agent.sh
git -C website diff --check
rg -n "private[-]host|/User[s]/" README.md website/docs .codex/skills
mise exec -- go test ./pkg/cli ./cmd/yeet ./pkg/yeet -count=1
```

Expected: diff check and tests PASS; private-info scan returns no new private content.

- [ ] **Step 5: Amend the unpublished website commit and checkpoint root docs**

Inside `website`, verify the diff contains only the four website files listed in this task, then amend the existing local `docs: release v0.10.16` commit without pushing it. Raw Git is permitted only inside the website repository:

```bash
git -C website add docs/payloads/cron-jobs.mdx docs/cli/yeet-cli.mdx docs/operations/workflows.mdx docs/changelog.mdx
git -C website commit --amend --no-edit
```

Then run `but diff --format agent`, select only `README.md`, generated help, and `pkg/yeet/release_assets_test.go`, and commit those root paths to `service-set-cron` with message:

```text
docs: explain payload-free schedule updates
```

Leave the root `website` gitlink uncommitted because the amended website commit is not published yet. Root policy requires publishing and verifying the website commit before recording that pointer.

---

### Task 5: Destination gates, disposable ISO smoke, landing, and `v0.10.16` publication

**Files:**
- Verify: all root and website changes from Tasks 1–4
- Write ignored evidence only: `.superpowers/sdd/2026-08-09-service-set-cron/`
- Publish: website `main`, root `main`, annotated tag `v0.10.16`, GitHub release assets

**Interfaces:**
- Consumes: the exact committed candidate from Tasks 1–4 and the already-authorized finish-to-main/release workflow.
- Produces: a clean reviewed release candidate, live disposable-service evidence, landed `origin/main`, public website commit, published `v0.10.16`, verified artifacts, and Catch hosts upgraded from published assets.
- Safety boundary: the coordinator performs external host, website, main, tag, and release mutations. Sub-agents may review evidence but must not independently push, tag, deploy, or run live services.

- [ ] **Step 1: Run the stable candidate gates once**

```bash
mise exec -- gofmt -w pkg/cli/cli.go pkg/cli/service_set_cron_fuzz_test.go pkg/catch/service_schedule_mutation.go pkg/catch/service_schedule_mutation_test.go pkg/catch/tty_service_set.go pkg/catch/tty_service_set_test.go pkg/catch/tty_authz_test.go pkg/yeet/svc_cmd.go pkg/yeet/svc_cmd_branch_test.go pkg/yeet/svc_cmd_routing_test.go pkg/yeet/release_assets_test.go cmd/yeet/cli_bridge_test.go cmd/yeet/cli_test.go
mise exec -- go test ./... -count=1
mise exec -- pre-commit run --all-files
mise run quality
mise run quality:goal
```

Expected: coverage at least 80%, zero CRAP hotspots, zero golangci findings, race clean, all active fuzz targets clean, and mutation score at least 80%. Fix real findings with focused RED/GREEN tests, then rerun only invalidated gates and one final destination gate.

- [ ] **Step 2: Obtain an independent whole-branch review**

Generate a review package from `a88e10f698d70dde24b4acdbbf1bf7997e95bdb5` through the current feature head. The reviewer must check the approved spec, active-generation eligibility, artifact preservation, lock ownership, rollback/retry, no-payload client flow, config partial success, permission mapping, docs, and release surface. Resolve every Critical/Important finding with one focused fix/re-review loop before continuing.

- [ ] **Step 3: Build and source-install the exact smoke candidate**

Create a disposable directory and build both binaries from the reviewed tree:

```bash
smoke_dir="$(mktemp -d)"
mise exec -- go build -o "$smoke_dir/yeet" ./cmd/yeet
GOOS=linux GOARCH=amd64 mise exec -- go build -o "$smoke_dir/catch-linux-amd64" ./cmd/catch
```

Use `<smoke-catch-host>` because its ISO topology was already proven by the preceding scheduled-run work. Record the exact pre-smoke `<protected-scheduled-service>` service/timer state and journal cursor without starting, restarting, or mutating it. Install the matching candidate Catch with the candidate client and prove the active remote binary is byte-identical to `catch-linux-amd64` before creating disposable services.

- [ ] **Step 4: Run the disposable scheduled-service smoke without executing it**

In a disposable project directory, create a uniquely named shebang payload whose only action writes `executed` in its working directory. Deploy it with a far-future schedule and ISO networking:

```bash
"$smoke_dir/yeet" --host '<smoke-catch-host>' run "$cron_svc" ./job.sh --cron="17 23 31 12 *" --net=iso
mv ./job.sh ./job.sh.removed
"$smoke_dir/yeet" --host '<smoke-catch-host>' service set "$cron_svc" --cron="18 23 31 12 *"
```

Require all of the following evidence:

- the command succeeds after the expected payload path was removed;
- `yeet.toml` changes only `schedule` from `17 23 31 12 *` to `18 23 31 12 *`;
- Catch advances exactly one generation and preserves payload SHA, kind, args boundary, identity, root/ZFS, snapshots, network desired/effective state, ISO namespace/IP, and auxiliary artifacts;
- the installed timer contains the normalized new `OnCalendar` and remains enabled/active/waiting;
- the service unit remains inactive with blank start timestamp/monotonic start zero;
- the marker is absent and the journal has no service execution since the pre-smoke cursor; and
- the update made no upload or local payload lookup.

Deploy a second unique ordinary native service, record its DB/info/unit/config state, and require the same `service set --cron` command to fail with scheduled-native-only guidance. Its generation, service type, timer absence, unit, and config must remain unchanged.

- [ ] **Step 5: Clean every disposable artifact and re-prove `<protected-scheduled-service>` was untouched**

Remove both disposable services with the candidate CLI using `--yes --clean-data --clean-config`. Verify their service/timer/auxiliary units, namespaces, IP allocations, ZFS/filesystem roots, database records, config entries, marker files, and local disposable directory are absent. Re-read `<protected-scheduled-service>` and require its generation, inactive/dead service state, active/waiting timer, next trigger, identity, ISO namespace/IP, payload SHA, and no-new-journal-entry evidence to match the pre-smoke snapshot.

- [ ] **Step 6: Publish the website, record its verified gitlink, and tidy feature history**

Push the amended website commit first:

```bash
git -C website push origin HEAD:main
git -C website status --short --branch
git -C website rev-parse HEAD
git -C website ls-remote origin refs/heads/main
```

The website worktree must be clean, not ahead, and its remote `main` SHA must match both website HEAD and the root gitlink. If the root gitlink remained uncommitted because of GitButler's known limitation, use the root policy's narrow signed gitlink-only publication exception now—no other raw root tree change is allowed.

After that proof, make one focused GitButler attempt to commit only the root `website` gitlink to `service-set-cron`. If GitButler cannot materialize it, use the root policy's narrow signed gitlink-only publication exception; no other raw root tree change is allowed. Create a GitButler oplog recovery snapshot, squash the complete `service-set-cron` branch into one signed release-candidate commit based on current `origin/main`, and verify it contains only this feature/spec/plan/docs plus the published website gitlink.

- [ ] **Step 7: Land the exact signed candidate on `origin/main`**

Run `but pull --check`. Verify the single session commit is based on current `origin/main`, has a valid signature, and its tree matches the reviewed candidate. Use the explicitly authorized fast path:

```bash
git push origin "$release_commit:main"
```

Treat non-fast-forward rejection as the race check: run `but pull`, retest if the base changed, and retry only when clean. Then reconcile GitButler and verify local `main`, `origin/main`, and `git ls-remote origin refs/heads/main` all equal the landed release commit. Clean only this session's integrated branch; preserve all unrelated branches.

- [ ] **Step 8: Tag and publish `v0.10.16`**

Tag the landed commit, never the synthetic GitButler workspace commit:

```bash
git tag -a v0.10.16 "$release_commit" -m "v0.10.16"
git push origin v0.10.16
```

Verify `git ls-remote --tags origin v0.10.16` resolves to the annotated tag whose peeled commit is exactly the landed release commit.

- [ ] **Step 9: Watch workflows and verify every published artifact**

Watch the tag-triggered Release workflow and the main-triggered Nightly workflow to successful completion. Use `gh release view v0.10.16` to require a public, non-draft, non-prerelease release targeting the landed commit. Download all five platform tarballs and all five `.sha256` files from the release, verify every checksum, archive member name, executable bit, version output, and `go version -m` VCS revision/modified state. The published binaries must report `v0.10.16`, the landed revision, and `vcs.modified=false`.

- [ ] **Step 10: Upgrade live Catch hosts from published assets and close out**

Use the extracted published `yeet` client—not a workspace build—to upgrade configured live Catch hosts `<smoke-catch-host>` and `<second-catch-host>` to `v0.10.16`. Verify each RPC version, copy each active remote Catch binary back read-only, check it against the matching published archive checksum, and inspect `go version -m` for the landed revision and clean release metadata. Reconfirm `<protected-scheduled-service>` remains inactive/dead with its timer active/waiting and no execution journal entry. Record final remote refs, website ref, tag, release URL, workflow IDs, asset evidence, live versions, cleanup proof, and GitButler cleanliness in the SDD report.
