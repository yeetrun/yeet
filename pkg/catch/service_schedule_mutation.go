// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/yeetrun/yeet/pkg/cronutil"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/fileutil"
)

const scheduledServiceSetOnlyMessage = "--cron only updates an existing scheduled native service; deploy a scheduled native payload with `yeet run <svc> <payload> --cron=...`"

var (
	serviceScheduleTimerVersionPath          = fileutil.UpdateVersion
	newServiceScheduleTempSource             = newTempEditSource
	serviceScheduleMigrationRequestForUpdate = serviceScheduleMigrationRequest
)

type serviceScheduleMutationPlan struct {
	previous        *db.Service
	target          *db.Service
	identity        resolvedServiceIdentity
	replacement     string
	generationPaths []string
	intent          []serviceIdentityPathState
	activeIntent    []serviceIdentityPathState
	units           []string
	stage           func(context.Context) error
	stagedTimer     string
	timerPath       string
	timerUnit       string
	noOp            bool
}

type serviceScheduleJournalState struct {
	TimerPath      string `json:"timerPath"`
	TimerUnit      string `json:"timerUnit"`
	PreviousActive bool   `json:"previousActive"`
	Persistent     bool   `json:"persistent"`
}

type serviceScheduleTimerRuntimeState struct {
	ActiveState string
	SubState    string
	Active      bool
}

func (s *Server) planServiceScheduleMutation(name, cron string) (_ *serviceScheduleMutationPlan, retErr error) {
	return s.planServiceScheduleMutationWithContext(context.Background(), name, cron)
}

func (s *Server) planServiceScheduleMutationWithContext(ctx context.Context, name, cron string) (_ *serviceScheduleMutationPlan, retErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	sv, timerPath, err := s.activeServiceSchedule(name)
	if err != nil {
		return nil, err
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
	active, err := s.preflightInstalledServiceSchedule(ctx, name, previous)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(current.OnCalendar) == strings.TrimSpace(desired) {
		return &serviceScheduleMutationPlan{
			previous: previous, identity: identity, activeIntent: active.intent, units: active.units,
			timerPath: active.timerPath, timerUnit: active.timerUnit, noOp: true,
		}, nil
	}
	raw, err := os.ReadFile(timerPath)
	if err != nil {
		return nil, fmt.Errorf("read active timer bytes for service %q: %w", name, err)
	}
	rewritten, err := rewriteSystemdTimerCalendar(string(raw), desired)
	if err != nil {
		return nil, fmt.Errorf("rewrite active timer for service %q: %w", name, err)
	}
	stagedTimer, err := stageServiceScheduleTimer(previous, timerPath, rewritten)
	if err != nil {
		return nil, fmt.Errorf("stage timer for service %q: %w", name, err)
	}
	return s.buildServiceScheduleMutationPlan(name, previous, identity, stagedTimer, active)
}

type installedServiceSchedulePreflight struct {
	intent    []serviceIdentityPathState
	units     []string
	timerPath string
	timerUnit string
}

func (s *Server) preflightInstalledServiceSchedule(
	ctx context.Context,
	name string,
	previous *db.Service,
) (installedServiceSchedulePreflight, error) {
	installer, err := s.NewInstaller(InstallerCfg{ServiceName: name, ClientOut: io.Discard})
	if err != nil {
		return installedServiceSchedulePreflight{}, fmt.Errorf("prepare active schedule generation for service %q: %w", name, err)
	}
	activeService, err := newSystemdInstallService(installer, previous)
	if err != nil {
		return installedServiceSchedulePreflight{}, err
	}
	states, err := activeService.InstallTargetStatesExcluding()
	if err != nil {
		return installedServiceSchedulePreflight{}, fmt.Errorf("capture active schedule generation intent: %w", err)
	}
	preflight := installedServiceSchedulePreflight{
		intent: serviceIdentityInstallTargetStates(states), units: activeService.InstallUnits(),
		timerPath: filepath.Join(systemdSystemDir, name+".timer"), timerUnit: name + ".timer",
	}
	probe := &serviceScheduleMutationPlan{
		previous: previous, activeIntent: preflight.intent, units: preflight.units,
		timerPath: preflight.timerPath, timerUnit: preflight.timerUnit,
	}
	if err := validateInstalledServiceScheduleGeneration(probe); err != nil {
		return installedServiceSchedulePreflight{}, err
	}
	timerSource, ok := previous.Artifacts.Gen(db.ArtifactSystemdTimerFile, previous.Generation)
	if !ok || strings.TrimSpace(timerSource) == "" {
		return installedServiceSchedulePreflight{}, errors.New("active schedule generation has no timer source")
	}
	probe.stagedTimer = timerSource
	if _, _, err := inspectServiceScheduleMigrationState(ctx, defaultServiceScheduleLifecycleOps(), probe); err != nil {
		return installedServiceSchedulePreflight{}, err
	}
	return preflight, nil
}

func stageServiceScheduleTimer(previous *db.Service, currentPath, content string) (stagedTimer string, retErr error) {
	currentMode, err := activeServiceScheduleTimerMode(currentPath)
	if err != nil {
		return "", err
	}
	source, err := newServiceScheduleTempSource(func(w io.Writer) error {
		if _, err := io.WriteString(w, content); err != nil {
			return fmt.Errorf("failed to write schedule timer temp file: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if err := os.Chmod(source.path, currentMode.Perm()); err != nil {
		source.cleanupInto(&err)
		return "", fmt.Errorf("preserve active timer mode on staged source: %w", err)
	}
	defer func() {
		source.cleanupInto(&retErr)
		if retErr != nil && stagedTimer != "" {
			retErr = errors.Join(retErr, removeStagedServiceScheduleTimer(stagedTimer))
			stagedTimer = ""
		}
	}()

	base, err := serviceScheduleTimerVersionBase(currentPath)
	if err != nil {
		return "", err
	}
	candidate, err := reserveServiceScheduleTimerPath(base, serviceScheduleArtifactRefPaths(previous))
	if err != nil {
		return "", err
	}
	if err := fileutil.CopyFile(source.path, candidate); err != nil {
		return "", errors.Join(fmt.Errorf("copy schedule timer to %s: %w", candidate, err), removeStagedServiceScheduleTimer(candidate))
	}
	return candidate, nil
}

func serviceScheduleTimerVersionBase(path string) (string, error) {
	base := filepath.Clean(serviceScheduleTimerVersionPath(path))
	path = filepath.Clean(path)
	if filepath.Dir(base) != filepath.Dir(path) || fileutil.RemoveVersion(base) != fileutil.RemoveVersion(path) {
		return "", fmt.Errorf("invalid versioned timer path %s for %s", base, path)
	}
	return base, nil
}

func activeServiceScheduleTimerMode(path string) (os.FileMode, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, fmt.Errorf("inspect active timer mode: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return 0, fmt.Errorf("active timer %s is not a regular file", path)
	}
	return info.Mode(), nil
}

func reserveServiceScheduleTimerPath(base string, forbidden map[string]struct{}) (string, error) {
	for attempt := 0; attempt < 1024; attempt++ {
		candidate := serviceScheduleTimerAttemptPath(base, attempt)
		if _, exists := forbidden[candidate]; exists {
			continue
		}
		reserved, err := os.OpenFile(candidate, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("reserve versioned timer path %s: %w", candidate, err)
		}
		if err := reserved.Close(); err != nil {
			return "", errors.Join(fmt.Errorf("close reserved timer path %s: %w", candidate, err), removeStagedServiceScheduleTimer(candidate))
		}
		return candidate, nil
	}
	return "", fmt.Errorf("reserve collision-free versioned timer path from %s", base)
}

func serviceScheduleArtifactRefPaths(service *db.Service) map[string]struct{} {
	paths := make(map[string]struct{})
	if service == nil {
		return paths
	}
	for _, artifact := range service.Artifacts {
		if artifact == nil {
			continue
		}
		for _, path := range artifact.Refs {
			if strings.TrimSpace(path) != "" {
				paths[filepath.Clean(path)] = struct{}{}
			}
		}
	}
	return paths
}

func serviceScheduleTimerAttemptPath(base string, attempt int) string {
	if attempt == 0 {
		return base
	}
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext) + fmt.Sprintf(".%d", attempt) + ext
}

func (s *Server) activeServiceSchedule(name string) (db.ServiceView, string, error) {
	sv, err := s.serviceView(name)
	if err != nil {
		return db.ServiceView{}, "", fmt.Errorf("inspect service %q for schedule update: %w", name, err)
	}
	if err := validateServiceScheduleRecord(name, sv); err != nil {
		return db.ServiceView{}, "", err
	}
	unitPath, unitOK := activeGenerationArtifactPath(sv, db.ArtifactSystemdUnit)
	timerPath, timerOK := activeGenerationArtifactPath(sv, db.ArtifactSystemdTimerFile)
	if !unitOK || !timerOK || strings.TrimSpace(unitPath) == "" || strings.TrimSpace(timerPath) == "" {
		return db.ServiceView{}, "", fmt.Errorf("service %q: %s", name, scheduledServiceSetOnlyMessage)
	}
	if err := validateActiveScheduleArtifact(unitPath); err != nil {
		return db.ServiceView{}, "", fmt.Errorf("validate active service artifact for %q: %w", name, err)
	}
	if err := validateActiveScheduleArtifact(timerPath); err != nil {
		return db.ServiceView{}, "", fmt.Errorf("validate active timer artifact for %q: %w", name, err)
	}
	return sv, timerPath, nil
}

func validateServiceScheduleRecord(name string, sv db.ServiceView) error {
	if sv.Name() != name {
		return fmt.Errorf("service %q record name is %q", name, sv.Name())
	}
	if sv.ServiceType() != db.ServiceTypeSystemd || sv.Generation() <= 0 {
		return fmt.Errorf("service %q: %s", name, scheduledServiceSetOnlyMessage)
	}
	if sv.LatestGeneration() < sv.Generation() {
		return fmt.Errorf("service %q latest generation %d is behind active generation %d", name, sv.LatestGeneration(), sv.Generation())
	}
	if sv.LatestGeneration() == int(^uint(0)>>1) {
		return fmt.Errorf("service %q next generation overflow after %d", name, sv.LatestGeneration())
	}
	return nil
}

func (s *Server) buildServiceScheduleMutationPlan(
	name string,
	previous *db.Service,
	identity resolvedServiceIdentity,
	stagedTimer string,
	active installedServiceSchedulePreflight,
) (_ *serviceScheduleMutationPlan, retErr error) {
	cleanupStaged := true
	defer func() {
		if cleanupStaged && retErr != nil {
			retErr = errors.Join(retErr, removeStagedServiceScheduleTimer(stagedTimer))
		}
	}()

	target, err := cloneActiveServiceGeneration(previous, stagedTimer)
	if err != nil {
		return nil, fmt.Errorf("clone active generation for service %q: %w", name, err)
	}
	installer, err := s.NewInstaller(InstallerCfg{ServiceName: name, ClientOut: io.Discard})
	if err != nil {
		return nil, fmt.Errorf("prepare schedule generation for service %q: %w", name, err)
	}
	generationService, err := newSystemdInstallService(installer, target)
	if err != nil {
		return nil, err
	}
	replacement, err := generationService.RenderedPrimaryUnit()
	if err != nil {
		return nil, fmt.Errorf("render schedule migration systemd unit: %w", err)
	}
	units := generationService.InstallUnits()
	states, err := generationService.InstallTargetStatesExcluding(generationService.PrimaryUnitPath())
	if err != nil {
		return nil, fmt.Errorf("capture schedule migration generation intent: %w", err)
	}
	cleanupStaged = false
	return &serviceScheduleMutationPlan{
		previous: previous, target: target, identity: identity, replacement: replacement,
		generationPaths: generationService.InstallTargetPaths(), intent: serviceIdentityInstallTargetStates(states),
		activeIntent: active.intent, units: units, stage: stagedNativeIdentityGeneration(generationService, units), stagedTimer: stagedTimer,
		timerPath: active.timerPath, timerUnit: active.timerUnit,
	}, nil
}

func validateActiveScheduleArtifact(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	_, readErr := io.Copy(io.Discard, f)
	return errors.Join(readErr, f.Close())
}

func rewriteSystemdTimerCalendar(raw, desired string) (string, error) {
	lines := strings.Split(raw, "\n")
	seen := false
	for index, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "OnCalendar=") {
			continue
		}
		if seen {
			return "", fmt.Errorf("timer has repeated OnCalendar")
		}
		seen = true
		lines[index] = "OnCalendar=" + strings.TrimSpace(desired)
	}
	if !seen {
		return "", fmt.Errorf("timer has missing OnCalendar")
	}
	return strings.Join(lines, "\n"), nil
}

func cloneActiveServiceGeneration(previous *db.Service, stagedTimer string) (*db.Service, error) {
	if previous == nil {
		return nil, errors.New(scheduledServiceSetOnlyMessage)
	}
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

func removeStagedServiceScheduleTimer(path string) error {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("remove staged service schedule timer %s: %w", path, err)
	}
	return fileutil.SyncDir(filepath.Dir(path))
}

func serviceScheduleMigrationRequest(plan *serviceScheduleMutationPlan) serviceIdentityMigrationRequest {
	return serviceIdentityMigrationRequest{
		Service:   plan.previous.Name,
		Requested: plan.identity.Persisted.RequestedUser + ":" + plan.identity.Persisted.RequestedGroup,
		Target:    plan.identity, TargetService: plan.target, ReplacementUnit: plan.replacement,
		StageGeneration: plan.stage, GenerationPaths: plan.generationPaths,
		GenerationIntents: plan.intent, GenerationUnits: plan.units,
		PreserveTargetServiceIdentity: true,
		Schedule:                      &serviceScheduleJournalState{TimerPath: plan.timerPath, TimerUnit: plan.timerUnit},
	}
}

func (s *Server) updateServiceScheduleLocked(ctx context.Context, name, cron string, out io.Writer) error {
	plan, err := s.planServiceScheduleMutationWithContext(ctx, name, cron)
	if err != nil || plan.noOp {
		return err
	}
	return s.applyServiceScheduleMutationPlanLocked(ctx, plan, serviceScheduleMigrationRequestForUpdate(plan), out)
}

func (s *Server) applyServiceScheduleMutationPlanLocked(
	ctx context.Context,
	plan *serviceScheduleMutationPlan,
	request serviceIdentityMigrationRequest,
	out io.Writer,
) (retErr error) {
	committed := false
	defer func() {
		if !committed {
			retErr = errors.Join(retErr, s.cleanupFailedServiceScheduleTimer(plan.previous, plan.stagedTimer))
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	if out == nil {
		out = io.Discard
	}
	if err := s.prepareServiceScheduleMigration(ctx, plan, &request); err != nil {
		return fmt.Errorf("prepare schedule lifecycle for service %q: %w", plan.previous.Name, err)
	}
	if _, err := s.migrateServiceIdentityLocked(ctx, request, out); err != nil {
		return fmt.Errorf("update schedule for service %q: %w", plan.previous.Name, err)
	}
	committed = true
	return nil
}

func (s *Server) prepareServiceScheduleMigration(
	ctx context.Context,
	plan *serviceScheduleMutationPlan,
	request *serviceIdentityMigrationRequest,
) error {
	if err := validateServiceScheduleMigrationPlan(plan, request); err != nil {
		return err
	}
	if err := validateInstalledServiceScheduleGeneration(plan); err != nil {
		return err
	}
	ops := defaultServiceScheduleLifecycleOps()
	if request.ops != nil {
		ops.merge(*request.ops)
	}
	schedule, enablement, err := inspectServiceScheduleMigrationState(ctx, ops, plan)
	if err != nil {
		return err
	}
	request.GenerationEnablement = &enablement
	request.Schedule = &schedule
	return nil
}

func validateServiceScheduleMigrationPlan(plan *serviceScheduleMutationPlan, request *serviceIdentityMigrationRequest) error {
	if plan == nil || request == nil || request.Schedule == nil || plan.previous == nil || plan.target == nil {
		return errors.New("schedule mutation is missing its journaled lifecycle plan")
	}
	if strings.TrimSpace(plan.timerUnit) == "" || strings.TrimSpace(plan.timerPath) == "" ||
		plan.timerUnit != plan.previous.Name+".timer" || filepath.Base(plan.timerPath) != plan.timerUnit {
		return errors.New("schedule mutation has an invalid timer lifecycle target")
	}
	return nil
}

func inspectServiceScheduleMigrationState(
	ctx context.Context,
	ops serviceIdentityMigrationOps,
	plan *serviceScheduleMutationPlan,
) (serviceScheduleJournalState, []serviceIdentityUnitEnablement, error) {
	timerState, err := ops.inspectScheduleTimer(ctx, plan.timerUnit)
	if err != nil {
		return serviceScheduleJournalState{}, nil, fmt.Errorf("inspect exact timer runtime state: %w", err)
	}
	enablement, err := captureServiceScheduleEnablement(ctx, ops, plan.previous, plan.units)
	if err != nil {
		return serviceScheduleJournalState{}, nil, err
	}
	timerConfig, err := readSystemdTimerConfig(plan.stagedTimer)
	if err != nil {
		return serviceScheduleJournalState{}, nil, fmt.Errorf("inspect staged timer persistence: %w", err)
	}
	return serviceScheduleJournalState{
		TimerPath: plan.timerPath, TimerUnit: plan.timerUnit, PreviousActive: timerState.Active,
		Persistent: timerConfig.Persistent,
	}, enablement, nil
}

func validateInstalledServiceScheduleGeneration(plan *serviceScheduleMutationPlan) error {
	if plan == nil || len(plan.activeIntent) == 0 {
		return errors.New("active schedule generation has no stable artifact inventory")
	}
	timerFound := false
	for _, expected := range plan.activeIntent {
		if err := validateServiceIdentityPathState(expected, expected.Path); err != nil {
			return fmt.Errorf("validate active generation artifact intent: %w", err)
		}
		if filepath.Clean(expected.Path) == filepath.Clean(plan.timerPath) {
			timerFound = expected.Present
		}
		actual, err := captureServiceIdentityPathProof(expected.Path)
		if err != nil {
			return fmt.Errorf("inspect installed generation artifact %s: %w", expected.Path, err)
		}
		if err := validateServiceIdentityTransactionPath(actual); err != nil {
			return fmt.Errorf("validate installed generation artifact %s: %w", expected.Path, err)
		}
		if !serviceIdentityPathMatchesState(actual, expected) {
			return fmt.Errorf("installed generation artifact %s does not exactly match the active generation source", expected.Path)
		}
	}
	if !timerFound {
		return errors.New("active schedule generation stable artifact inventory has no timer")
	}
	return nil
}

func captureServiceScheduleEnablement(
	ctx context.Context,
	ops serviceIdentityMigrationOps,
	previous *db.Service,
	targetUnits []string,
) ([]serviceIdentityUnitEnablement, error) {
	plan := serviceIdentityGenerationUnitPlan(previous, previous.Name, targetUnits)
	states := make([]serviceIdentityUnitEnablement, 0, len(plan))
	for _, unit := range plan {
		enabled, err := ops.isEnabled(ctx, unit)
		if err != nil {
			return nil, fmt.Errorf("inspect exact enablement for %s: %w", unit, err)
		}
		states = append(states, serviceIdentityUnitEnablement{Unit: unit, Enabled: enabled, TargetEnabled: enabled})
	}
	return states, nil
}

func defaultServiceScheduleLifecycleOps() serviceIdentityMigrationOps {
	return serviceIdentityMigrationOps{
		isEnabled:            serviceScheduleSystemdUnitEnabled,
		inspectScheduleTimer: inspectServiceScheduleTimer,
		scheduleSystemctl:    runServiceScheduleSystemctl,
	}
}

func serviceScheduleSystemdUnitEnabled(ctx context.Context, unit string) (bool, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "is-enabled", unit).CombinedOutput()
	var exitErr *exec.ExitError
	return parseServiceScheduleSystemdUnitEnabled(unit, string(out), errors.As(err, &exitErr), err)
}

func parseServiceScheduleSystemdUnitEnabled(unit, raw string, exited bool, commandErr error) (bool, error) {
	state := strings.TrimSpace(raw)
	knownEnabled := slices.Contains([]string{"enabled", "enabled-runtime", "linked", "linked-runtime", "alias", "static", "indirect", "generated", "transient"}, state)
	knownDisabled := slices.Contains([]string{"disabled", "masked", "masked-runtime"}, state)
	switch {
	case commandErr == nil && knownEnabled:
		return true, nil
	case exited && knownDisabled:
		return false, nil
	}
	if commandErr == nil {
		commandErr = errors.New("unsupported successful is-enabled result")
	}
	return false, fmt.Errorf("systemctl is-enabled %s returned %q: %w", unit, state, commandErr)
}

func serviceIdentitySystemdUnitEnabled(ctx context.Context, unit string) (bool, error) {
	err := exec.CommandContext(ctx, "systemctl", "is-enabled", unit).Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, err
}

func inspectServiceScheduleTimer(ctx context.Context, unit string) (serviceScheduleTimerRuntimeState, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "show", unit, "--property=ActiveState", "--property=SubState", "--no-pager").CombinedOutput()
	if err != nil {
		return serviceScheduleTimerRuntimeState{}, fmt.Errorf("systemctl show %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
	}
	return parseServiceScheduleTimerState(unit, string(out))
}

func parseServiceScheduleTimerState(unit, raw string) (serviceScheduleTimerRuntimeState, error) {
	values, err := parseServiceScheduleTimerProperties(raw)
	if err != nil {
		return serviceScheduleTimerRuntimeState{}, err
	}
	state := serviceScheduleTimerRuntimeState{ActiveState: values["ActiveState"], SubState: values["SubState"]}
	return classifyServiceScheduleTimerState(unit, state)
}

func parseServiceScheduleTimerProperties(raw string) (map[string]string, error) {
	values := make(map[string]string, 2)
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || (key != "ActiveState" && key != "SubState") {
			return nil, fmt.Errorf("unsupported systemctl timer state line %q", line)
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("duplicate systemctl timer state %s", key)
		}
		values[key] = strings.TrimSpace(value)
	}
	return values, nil
}

func classifyServiceScheduleTimerState(unit string, state serviceScheduleTimerRuntimeState) (serviceScheduleTimerRuntimeState, error) {
	switch {
	case state.ActiveState == "active" && slices.Contains([]string{"waiting", "running", "elapsed"}, state.SubState):
		state.Active = true
	case state.ActiveState == "inactive" && state.SubState == "dead":
		state.Active = false
	default:
		return serviceScheduleTimerRuntimeState{}, fmt.Errorf("timer %s is in unsupported state %s/%s", unit, state.ActiveState, state.SubState)
	}
	return state, nil
}

func runServiceScheduleSystemctl(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func cloneServiceScheduleJournalState(state *serviceScheduleJournalState) *serviceScheduleJournalState {
	if state == nil {
		return nil
	}
	clone := *state
	return &clone
}

func reconcileServiceScheduleTimerRuntime(
	ctx context.Context,
	ops serviceIdentityMigrationOps,
	state serviceScheduleJournalState,
	wantActive bool,
) error {
	if err := quiesceServiceScheduleTimer(ctx, ops, state); err != nil {
		return err
	}
	if !wantActive {
		return nil
	}
	return startServiceScheduleTimerWithoutCatchUp(ctx, ops, state)
}

func startServiceScheduleTimerWithoutCatchUp(
	ctx context.Context,
	ops serviceIdentityMigrationOps,
	state serviceScheduleJournalState,
) error {
	// systemd's supported timer state cleanup removes the Persistent= stamp
	// while the timer is inactive. Starting only after that cleanup means a
	// calendar instant crossed during this transaction cannot be caught up.
	if state.Persistent {
		if err := ops.scheduleSystemctl(ctx, "clean", "--what=state", state.TimerUnit); err != nil {
			return fmt.Errorf("clear persistent state for timer %s: %w", state.TimerUnit, err)
		}
	}
	if err := ops.scheduleSystemctl(ctx, "start", state.TimerUnit); err != nil {
		return fmt.Errorf("start timer %s after persistent-state reset: %w", state.TimerUnit, err)
	}
	current, err := ops.inspectScheduleTimer(ctx, state.TimerUnit)
	if err != nil {
		return fmt.Errorf("inspect restarted timer %s: %w", state.TimerUnit, err)
	}
	if !current.Active {
		return fmt.Errorf("timer %s is inactive after schedule lifecycle", state.TimerUnit)
	}
	return nil
}

func quiesceServiceScheduleTimer(
	ctx context.Context,
	ops serviceIdentityMigrationOps,
	state serviceScheduleJournalState,
) error {
	if ops.inspectScheduleTimer == nil || ops.scheduleSystemctl == nil {
		return errors.New("schedule timer lifecycle operations are unavailable")
	}
	current, err := ops.inspectScheduleTimer(ctx, state.TimerUnit)
	if err != nil {
		return fmt.Errorf("inspect timer %s before stop: %w", state.TimerUnit, err)
	}
	if !current.Active {
		return nil
	}
	if err := ops.scheduleSystemctl(ctx, "stop", state.TimerUnit); err != nil {
		return fmt.Errorf("stop timer %s for schedule lifecycle: %w", state.TimerUnit, err)
	}
	current, err = ops.inspectScheduleTimer(ctx, state.TimerUnit)
	if err != nil {
		return fmt.Errorf("inspect stopped timer %s: %w", state.TimerUnit, err)
	}
	if current.Active {
		return fmt.Errorf("timer %s remained active after stop", state.TimerUnit)
	}
	return nil
}

func restoreServiceScheduleRuntimeState(
	ctx context.Context,
	ops serviceIdentityMigrationOps,
	schedule serviceScheduleJournalState,
	desired []serviceIdentityRuntimeUnitState,
) error {
	current, err := ops.captureRuntime(ctx, scheduleTimerServiceName(schedule.TimerUnit))
	if err != nil {
		return fmt.Errorf("verify untouched schedule runtime: %w", err)
	}
	if err := verifyUntouchedServiceScheduleRuntime(current, desired, schedule.TimerUnit); err != nil {
		return err
	}
	wantTimer, err := desiredServiceScheduleTimerState(desired, schedule)
	if err != nil {
		return err
	}
	if err := reconcileServiceScheduleTimerRuntime(ctx, ops, schedule, wantTimer); err != nil {
		return err
	}
	current, err = ops.captureRuntime(ctx, scheduleTimerServiceName(schedule.TimerUnit))
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, desired) {
		return fmt.Errorf("schedule runtime state is %#v, want %#v", current, desired)
	}
	return nil
}

func verifyUntouchedServiceScheduleRuntime(
	current, desired []serviceIdentityRuntimeUnitState,
	timerUnit string,
) error {
	for _, state := range desired {
		if state.Unit == timerUnit {
			continue
		}
		actual, ok := serviceIdentityRuntimeStateForUnit(current, state.Unit)
		if !ok || actual != state.Active {
			return fmt.Errorf("schedule-only transaction found unrelated runtime change for %s", state.Unit)
		}
	}
	return nil
}

func desiredServiceScheduleTimerState(
	desired []serviceIdentityRuntimeUnitState,
	schedule serviceScheduleJournalState,
) (bool, error) {
	wantTimer, ok := serviceIdentityRuntimeStateForUnit(desired, schedule.TimerUnit)
	if !ok || wantTimer != schedule.PreviousActive {
		return false, errors.New("journaled timer runtime state is inconsistent")
	}
	return wantTimer, nil
}

func serviceIdentityRuntimeStateForUnit(states []serviceIdentityRuntimeUnitState, unit string) (bool, bool) {
	for _, state := range states {
		if state.Unit == unit {
			return state.Active, true
		}
	}
	return false, false
}

func scheduleTimerServiceName(unit string) string {
	return strings.TrimSuffix(unit, ".timer")
}

func (s *Server) cleanupFailedServiceScheduleTimer(previous *db.Service, candidate string) error {
	if previous == nil || strings.TrimSpace(candidate) == "" {
		return nil
	}
	if err := s.checkServiceIdentityRecoveryMutationAllowed(previous.Name); err != nil {
		return nil
	}
	current, err := s.serviceView(previous.Name)
	if err != nil {
		return fmt.Errorf("inspect service %q before failed schedule cleanup: %w", previous.Name, err)
	}
	if !reflect.DeepEqual(current.AsStruct(), previous) || !isOwnedServiceScheduleTimerCandidate(previous, candidate) {
		return nil
	}
	return removeStagedServiceScheduleTimer(candidate)
}

func (s *Server) cleanupRecoveredServiceScheduleTimer(header serviceIdentityJournalHeader) error {
	previous, candidate, ok := recoveredServiceScheduleTimerSource(header)
	if !ok {
		return nil
	}
	current, err := s.serviceView(previous.Name)
	if err != nil {
		return fmt.Errorf("inspect service %q before recovered schedule cleanup: %w", previous.Name, err)
	}
	if !reflect.DeepEqual(current.AsStruct(), previous) {
		return fmt.Errorf("service %q changed before recovered schedule cleanup", previous.Name)
	}
	return removeStagedServiceScheduleTimer(candidate)
}

func recoveredServiceScheduleTimerSource(header serviceIdentityJournalHeader) (*db.Service, string, bool) {
	previous := header.PreviousService
	target := header.TargetService
	if !serviceScheduleRecoveryRecordsMatch(header, previous, target) {
		return nil, "", false
	}
	if err := validateServiceScheduleRecord(header.Service, previous.View()); err != nil {
		return nil, "", false
	}
	candidate, ok := activeGenerationArtifactPath(target.View(), db.ArtifactSystemdTimerFile)
	if !ok || strings.TrimSpace(candidate) == "" {
		return nil, "", false
	}
	if _, existed := serviceScheduleArtifactRefPaths(previous)[filepath.Clean(candidate)]; existed {
		return nil, "", false
	}
	expected, err := cloneActiveServiceGeneration(previous, candidate)
	if err != nil || !reflect.DeepEqual(expected, target) || !isOwnedServiceScheduleTimerCandidate(previous, candidate) {
		return nil, "", false
	}
	return previous.Clone(), candidate, true
}

func serviceScheduleRecoveryRecordsMatch(header serviceIdentityJournalHeader, previous, target *db.Service) bool {
	return header.Schedule != nil && header.PreviousServicePresent && previous != nil && target != nil &&
		previous.Name == header.Service && target.Name == header.Service
}

func validateServiceScheduleRecoveryHeader(header serviceIdentityJournalHeader) error {
	if header.Schedule == nil {
		return nil
	}
	schedule := header.Schedule
	if err := validateServiceScheduleRecoveryTarget(header, *schedule); err != nil {
		return err
	}
	if _, _, ok := recoveredServiceScheduleTimerSource(header); !ok {
		return fmt.Errorf("journaled schedule lifecycle does not match an exact schedule-only generation")
	}
	if err := validateServiceScheduleRecoveryRuntime(header, *schedule); err != nil {
		return err
	}
	return validateServiceScheduleRecoveryPersistence(header, *schedule)
}

func validateServiceScheduleRecoveryTarget(
	header serviceIdentityJournalHeader,
	schedule serviceScheduleJournalState,
) error {
	if schedule.TimerUnit != header.Service+".timer" || filepath.Base(schedule.TimerPath) != schedule.TimerUnit ||
		!filepath.IsAbs(schedule.TimerPath) {
		return fmt.Errorf("journaled schedule timer lifecycle target is invalid")
	}
	if !slices.ContainsFunc(header.GenerationBackups, func(backup serviceIdentityGenerationBackup) bool {
		return filepath.Clean(backup.Path) == filepath.Clean(schedule.TimerPath)
	}) {
		return errors.New("journaled schedule timer lifecycle target is outside the generation transaction")
	}
	return nil
}

func validateServiceScheduleRecoveryRuntime(
	header serviceIdentityJournalHeader,
	schedule serviceScheduleJournalState,
) error {
	wantActive, ok := serviceIdentityRuntimeStateForUnit(header.PreviousRuntimeUnits, schedule.TimerUnit)
	if !ok || wantActive != schedule.PreviousActive {
		return fmt.Errorf("journaled schedule timer runtime state is inconsistent")
	}
	return nil
}

func validateServiceScheduleRecoveryPersistence(
	header serviceIdentityJournalHeader,
	schedule serviceScheduleJournalState,
) error {
	previousTimer, ok := activeGenerationArtifactPath(header.PreviousService.View(), db.ArtifactSystemdTimerFile)
	if !ok {
		return fmt.Errorf("journaled schedule has no previous timer source")
	}
	config, err := readSystemdTimerConfig(previousTimer)
	if err != nil {
		return fmt.Errorf("journaled schedule persistence does not match the previous timer source: %w", err)
	}
	if config.Persistent != schedule.Persistent {
		return errors.New("journaled schedule persistence does not match the previous timer source")
	}
	return nil
}

func isOwnedServiceScheduleTimerCandidate(previous *db.Service, candidate string) bool {
	active, ok := activeGenerationArtifactPath(previous.View(), db.ArtifactSystemdTimerFile)
	if !ok {
		return false
	}
	active = filepath.Clean(active)
	candidate = filepath.Clean(candidate)
	return candidate != active && filepath.Dir(candidate) == filepath.Dir(active) &&
		fileutil.RemoveVersion(candidate) == fileutil.RemoveVersion(active)
}
