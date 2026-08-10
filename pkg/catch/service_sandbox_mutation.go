// Copyright (c) 2026 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/fileutil"
)

var (
	ensureBubblewrapForServiceSandboxMutation        = EnsureBubblewrap
	validateServiceSandboxPolicyForMutation          = validateServiceSandboxPolicy
	probeServiceSandboxForMutation                   = probeServiceSandbox
	verifyGeneratedSystemdUnitForSandboxMutation     = verifyGeneratedSystemdUnit
	renderNativeSandboxUnitForServiceSandboxMutation = renderNativeSandboxUnitWithPlan
	captureServiceSandboxEnablementForMutation       = captureServiceSandboxMutationEnablement
	removeServiceSandboxMutationUnitForUpdate        = removeServiceSandboxMutationUnit
	afterServiceSandboxMutationCleanupRename         = func(string, string) {}
	migrateServiceSandboxGeneration                  = func(ctx context.Context, s *Server, req serviceIdentityMigrationRequest, out io.Writer) (serviceIdentityMigrationResult, error) {
		return s.migrateServiceIdentityLocked(ctx, req, out)
	}
)

type serviceSandboxMutationPlan struct {
	previous        *db.Service
	target          *db.Service
	identity        resolvedServiceIdentity
	replacement     string
	generationPaths []string
	intent          []serviceIdentityPathState
	units           []string
	stage           func(context.Context) error
	stagedUnit      string
	stagedUnitProof serviceIdentityPathProof
	noOp            bool
}

type serviceSandboxMutationPreflight struct {
	identity     resolvedServiceIdentity
	targetPolicy serviceSandboxPolicy
	stagedUnit   string
	stagedProof  serviceIdentityPathProof
}

func (s *Server) planServiceSandboxMutation(ctx context.Context, name string, options cli.SandboxOptions) (*serviceSandboxMutationPlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sv, err := s.serviceView(name)
	if err != nil {
		return nil, fmt.Errorf("inspect service %q for sandbox update: %w", name, err)
	}
	if err := validateServiceSandboxMutationRecord(name, sv); err != nil {
		return nil, err
	}
	previous := sv.AsStruct()
	current, err := serviceSandboxPolicyForExactGeneration(previous, previous.Generation)
	if err != nil {
		return nil, fmt.Errorf("load service %q active sandbox policy: %w", name, err)
	}
	targetPolicy, err := applyServiceSandboxPolicyPatch(name, current, false, options)
	if err != nil {
		return nil, err
	}
	if reflect.DeepEqual(current, targetPolicy) {
		return &serviceSandboxMutationPlan{previous: previous, identity: effectiveServiceIdentity(sv), noOp: true}, nil
	}
	return s.buildServiceSandboxMutationPlan(ctx, sv, previous, current, targetPolicy)
}

func (s *Server) buildServiceSandboxMutationPlan(
	ctx context.Context,
	sv db.ServiceView,
	previous *db.Service,
	current, targetPolicy serviceSandboxPolicy,
) (_ *serviceSandboxMutationPlan, retErr error) {
	preflight, err := s.preflightServiceSandboxMutation(ctx, sv, previous, current, targetPolicy)
	if err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup && retErr != nil {
			retErr = errors.Join(retErr, removeServiceSandboxMutationUnit(preflight.stagedProof))
		}
	}()
	plan, err := s.buildServiceSandboxGeneration(previous, preflight)
	if err != nil {
		return nil, err
	}
	cleanup = false
	return plan, nil
}

func (s *Server) preflightServiceSandboxMutation(
	ctx context.Context,
	sv db.ServiceView,
	previous *db.Service,
	current, targetPolicy serviceSandboxPolicy,
) (*serviceSandboxMutationPreflight, error) {
	unitPath, _ := activeGenerationArtifactPath(sv, db.ArtifactSystemdUnit)
	payload, _ := activeGenerationArtifactPath(sv, db.ArtifactBinary)
	raw, err := os.ReadFile(unitPath)
	if err != nil {
		return nil, fmt.Errorf("read active sandbox unit: %w", err)
	}
	identity := effectiveServiceIdentity(sv)
	resolver := serviceSandboxMutationResolver(sv)
	request := serviceSandboxPlanRequest{
		Service: previous.Name, Policy: targetPolicy, Payload: payload,
		DataDir: s.serviceDataDir(previous.Name), ResolverSource: resolver,
		UID: identity.Persisted.UID, GID: identity.Persisted.GID, Hostname: previous.Name,
	}
	active := targetPolicy.State == "on"
	targetPolicy, validatedPlan, err := prepareServiceSandboxMutationTarget(ctx, previous.Name, request, active)
	if err != nil {
		return nil, err
	}
	unitRequest := nativeSandboxUnitRequest{
		CurrentPolicy: current, TargetPolicy: targetPolicy, Identity: identity.Persisted,
		Payload: payload, DataDir: request.DataDir, Resolver: resolver, Hostname: previous.Name,
	}
	rendered, sandboxPlan, err := renderNativeSandboxUnitForServiceSandboxMutation(string(raw), unitRequest, validatedPlan)
	if err != nil {
		return nil, fmt.Errorf("render service %q sandbox unit: %w", previous.Name, err)
	}
	stagedProof, err := stageServiceSandboxMutationUnit(previous, unitPath, rendered)
	if err != nil {
		return nil, fmt.Errorf("stage service %q sandbox unit: %w", previous.Name, err)
	}
	if err := verifyStagedServiceSandboxMutation(ctx, previous.Name, stagedProof.Path, sandboxPlan, identity, active); err != nil {
		return nil, errors.Join(err, removeServiceSandboxMutationUnit(stagedProof))
	}
	return &serviceSandboxMutationPreflight{
		identity: identity, targetPolicy: targetPolicy, stagedUnit: stagedProof.Path, stagedProof: stagedProof,
	}, nil
}

func prepareServiceSandboxMutationTarget(
	ctx context.Context,
	name string,
	request serviceSandboxPlanRequest,
	active bool,
) (serviceSandboxPolicy, *serviceSandboxPlan, error) {
	if active {
		if err := serviceSandboxMutationContext(ctx); err != nil {
			return serviceSandboxPolicy{}, nil, err
		}
		if err := ensureBubblewrapForServiceSandboxMutation(ctx); err != nil {
			return serviceSandboxPolicy{}, nil, fmt.Errorf("ensure Bubblewrap for service %q: %w", name, err)
		}
		if err := serviceSandboxMutationContext(ctx); err != nil {
			return serviceSandboxPolicy{}, nil, err
		}
	}
	targetPolicy, err := validateServiceSandboxPolicyForMutation(request, active)
	if err != nil {
		return serviceSandboxPolicy{}, nil, fmt.Errorf("validate service %q sandbox policy: %w", name, err)
	}
	request.Policy = targetPolicy
	if !active {
		return targetPolicy, nil, nil
	}
	built, err := buildValidatedServiceSandboxPlan(request)
	if err != nil {
		return serviceSandboxPolicy{}, nil, fmt.Errorf("build service %q sandbox plan: %w", name, err)
	}
	return targetPolicy, &built, nil
}

func verifyStagedServiceSandboxMutation(
	ctx context.Context,
	name, stagedUnit string,
	plan *serviceSandboxPlan,
	identity resolvedServiceIdentity,
	active bool,
) error {
	if active {
		if plan == nil {
			return errors.New("active sandbox render returned no probe plan")
		}
		if err := serviceSandboxMutationContext(ctx); err != nil {
			return err
		}
		if err := probeServiceSandboxForMutation(ctx, *plan, identity.Persisted.UID, identity.Persisted.GID); err != nil {
			return fmt.Errorf("probe service %q sandbox: %w", name, err)
		}
	}
	if err := serviceSandboxMutationContext(ctx); err != nil {
		return err
	}
	if err := verifyGeneratedSystemdUnitForSandboxMutation(ctx, stagedUnit); err != nil {
		return fmt.Errorf("verify service %q sandbox unit: %w", name, err)
	}
	return serviceSandboxMutationContext(ctx)
}

func (s *Server) buildServiceSandboxGeneration(
	previous *db.Service,
	preflight *serviceSandboxMutationPreflight,
) (*serviceSandboxMutationPlan, error) {
	target, err := cloneServiceSandboxMutationGeneration(previous, preflight.stagedUnit, preflight.targetPolicy)
	if err != nil {
		return nil, err
	}
	installer, err := s.NewInstaller(InstallerCfg{ServiceName: previous.Name, ClientOut: io.Discard})
	if err != nil {
		return nil, fmt.Errorf("prepare service %q sandbox generation: %w", previous.Name, err)
	}
	generationService, err := newSystemdInstallService(installer, target)
	if err != nil {
		return nil, err
	}
	replacement, err := generationService.RenderedPrimaryUnit()
	if err != nil {
		return nil, fmt.Errorf("render service %q sandbox generation: %w", previous.Name, err)
	}
	states, err := generationService.InstallTargetStatesExcluding(generationService.PrimaryUnitPath())
	if err != nil {
		return nil, fmt.Errorf("capture service %q sandbox generation intent: %w", previous.Name, err)
	}
	units := generationService.InstallUnits()
	return &serviceSandboxMutationPlan{
		previous: previous, target: target, identity: preflight.identity, replacement: replacement,
		generationPaths: generationService.InstallTargetPaths(), intent: serviceIdentityInstallTargetStates(states),
		units: units, stage: stagedNativeIdentityGeneration(generationService, units),
		stagedUnit: preflight.stagedUnit, stagedUnitProof: preflight.stagedProof,
	}, nil
}

func serviceSandboxMutationContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func serviceSandboxMutationResolver(sv db.ServiceView) string {
	if path, ok := activeGenerationArtifactPath(sv, db.ArtifactNetNSResolv); ok && strings.TrimSpace(path) != "" {
		return path
	}
	return "/etc/resolv.conf"
}

func cloneServiceSandboxMutationGeneration(previous *db.Service, stagedUnit string, policy serviceSandboxPolicy) (*db.Service, error) {
	if previous == nil {
		return nil, errors.New("sandbox mutation requires an active service")
	}
	target := previous.Clone()
	for name, artifact := range target.Artifacts {
		if artifact == nil || artifact.Refs == nil {
			return nil, fmt.Errorf("active sandbox generation has an invalid %s artifact record", name)
		}
		delete(artifact.Refs, db.ArtifactRef("staged"))
		path, ok := artifact.Refs[db.Gen(previous.Generation)]
		if !ok || strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("active sandbox generation has no exact %s artifact", name)
		}
		artifact.Refs[db.ArtifactRef("staged")] = path
	}
	target.Artifacts[db.ArtifactSystemdUnit].Refs[db.ArtifactRef("staged")] = stagedUnit
	if target.Sandbox == nil {
		target.Sandbox = &db.ServiceSandboxStore{}
	}
	if target.Sandbox.Refs == nil {
		target.Sandbox.Refs = make(map[db.ArtifactRef]*db.ServiceSandboxPolicy)
	}
	delete(target.Sandbox.Refs, db.ArtifactRef("staged"))
	target.Sandbox.Refs[db.ArtifactRef("staged")] = serviceSandboxPolicyToDB(policy)
	commitGeneratedServiceRefs(nil, target, target.Name, generatedServiceCommitForGen(0, target.LatestGeneration))
	return target, nil
}

func stageServiceSandboxMutationUnit(previous *db.Service, activePath, content string) (serviceIdentityPathProof, error) {
	info, err := os.Lstat(activePath)
	if err != nil {
		return serviceIdentityPathProof{}, err
	}
	base := filepath.Clean(fileutil.UpdateVersion(activePath))
	if filepath.Dir(base) != filepath.Dir(filepath.Clean(activePath)) {
		return serviceIdentityPathProof{}, fmt.Errorf("versioned unit path %s escaped %s", base, filepath.Dir(activePath))
	}
	forbidden := serviceScheduleArtifactRefPaths(previous)
	for attempt := 0; attempt < 1024; attempt++ {
		candidate := serviceScheduleTimerAttemptPath(base, attempt)
		if _, exists := forbidden[candidate]; exists {
			continue
		}
		file, openErr := os.OpenFile(candidate, os.O_CREATE|os.O_EXCL|os.O_RDWR, info.Mode().Perm())
		if errors.Is(openErr, os.ErrExist) {
			continue
		}
		if openErr != nil {
			return serviceIdentityPathProof{}, openErr
		}
		proof, finishErr := finishServiceSandboxMutationUnit(file, candidate, content)
		if finishErr != nil {
			return serviceIdentityPathProof{}, finishErr
		}
		return proof, nil
	}
	return serviceIdentityPathProof{}, fmt.Errorf("reserve collision-free versioned sandbox unit from %s", base)
}

func finishServiceSandboxMutationUnit(file *os.File, path, content string) (serviceIdentityPathProof, error) {
	_, writeErr := io.WriteString(file, content)
	syncErr := file.Sync()
	proof, proofErr := captureServiceIdentityOpenFileProof(file, path)
	closeErr := file.Close()
	if proofErr != nil {
		return serviceIdentityPathProof{}, errors.Join(
			writeErr, syncErr, closeErr,
			fmt.Errorf("leave staged sandbox unit %s because its provenance is unavailable: %w", path, proofErr),
		)
	}
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return serviceIdentityPathProof{}, errors.Join(err, removeServiceSandboxMutationUnit(proof))
	}
	if err := fileutil.SyncDir(filepath.Dir(path)); err != nil {
		return serviceIdentityPathProof{}, errors.Join(err, removeServiceSandboxMutationUnit(proof))
	}
	actual, err := captureServiceIdentityPathProof(path)
	if err != nil {
		return serviceIdentityPathProof{}, err
	}
	if !reflect.DeepEqual(actual, proof) {
		return serviceIdentityPathProof{}, fmt.Errorf("staged sandbox unit %s changed from its durable provenance", path)
	}
	return proof, nil
}

func removeServiceSandboxMutationUnit(proof serviceIdentityPathProof) error {
	return removeProvenanceSafeArtifact(proof, "staged sandbox unit")
}

func removeProvenanceSafeArtifact(proof serviceIdentityPathProof, label string) error {
	if strings.TrimSpace(proof.Path) == "" {
		return nil
	}
	quarantineDir, quarantinePath, moved, transitionErr := quarantineProvenanceSafeArtifactCleanup(proof.Path, label)
	if !moved {
		return transitionErr
	}
	return errors.Join(transitionErr, finishProvenanceSafeArtifactCleanup(proof, label, quarantineDir, quarantinePath))
}

func quarantineProvenanceSafeArtifactCleanup(path, label string) (string, string, bool, error) {
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	quarantineDir, err := os.MkdirTemp(parent, "."+filepath.Base(path)+".sandbox-cleanup-")
	if err != nil {
		return "", "", false, fmt.Errorf("create %s cleanup quarantine beside %s: %w", label, path, err)
	}
	quarantinePath := filepath.Join(quarantineDir, filepath.Base(path))
	if err := os.Rename(path, quarantinePath); err != nil {
		cleanupErr := removeProvenanceSafeArtifactQuarantineDir(quarantineDir, label)
		if errors.Is(err, os.ErrNotExist) {
			return "", "", false, cleanupErr
		}
		return "", "", false, errors.Join(fmt.Errorf("quarantine %s %s: %w", label, path, err), cleanupErr)
	}
	afterServiceSandboxMutationCleanupRename(path, quarantinePath)
	return quarantineDir, quarantinePath, true, errors.Join(
		fileutil.SyncDir(quarantineDir),
		fileutil.SyncDir(parent),
	)
}

func finishProvenanceSafeArtifactCleanup(proof serviceIdentityPathProof, label, quarantineDir, quarantinePath string) error {
	actual, err := captureServiceIdentityPathProof(quarantinePath)
	if err != nil {
		return fmt.Errorf(
			"%s %s changed from its durable provenance; preserve unverified quarantine at recovery location %s: %w",
			label, proof.Path, quarantinePath, err,
		)
	}
	expected := proof
	expected.Path = quarantinePath
	if reflect.DeepEqual(actual, expected) {
		return removeOwnedProvenanceSafeArtifactQuarantine(proof.Path, label, quarantineDir, actual)
	}
	return restoreDivergentProvenanceSafeArtifactQuarantine(proof.Path, label, quarantineDir, actual)
}

func removeOwnedProvenanceSafeArtifactQuarantine(original, label, quarantineDir string, actual serviceIdentityPathProof) error {
	if err := removeServiceIdentityProofAt(quarantineDir, filepath.Base(actual.Path), actual); err != nil {
		return fmt.Errorf("remove owned %s quarantine %s: %w", label, actual.Path, err)
	}
	cleanupErr := removeProvenanceSafeArtifactQuarantineDir(quarantineDir, label)
	_, inspectErr := os.Lstat(original)
	if inspectErr == nil {
		return errors.Join(cleanupErr, fmt.Errorf(
			"%s %s changed from its durable provenance; external replacement preserved at original path",
			label, original,
		))
	}
	if !errors.Is(inspectErr, os.ErrNotExist) {
		return errors.Join(cleanupErr, fmt.Errorf("inspect %s original path %s: %w", label, original, inspectErr))
	}
	return cleanupErr
}

func restoreDivergentProvenanceSafeArtifactQuarantine(original, label, quarantineDir string, actual serviceIdentityPathProof) error {
	if actual.Nlink != 1 {
		return provenanceSafeArtifactRecoveryError(original, actual.Path, label, fmt.Errorf("divergent quarantine has %d links, want one", actual.Nlink))
	}
	if err := os.Link(actual.Path, original); err != nil {
		return provenanceSafeArtifactRecoveryError(original, actual.Path, label, err)
	}
	if err := fileutil.SyncDir(filepath.Dir(original)); err != nil {
		return provenanceSafeArtifactRecoveryError(original, actual.Path, label, fmt.Errorf("sync restored original path: %w", err))
	}
	if err := os.Remove(actual.Path); err != nil {
		return provenanceSafeArtifactRecoveryError(original, actual.Path, label, fmt.Errorf("remove restored quarantine link: %w", err))
	}
	quarantineSyncErr := fileutil.SyncDir(quarantineDir)
	cleanupErr := removeProvenanceSafeArtifactQuarantineDir(quarantineDir, label)
	return errors.Join(
		quarantineSyncErr,
		cleanupErr,
		fmt.Errorf("%s %s changed from its durable provenance; divergent file restored to original path", label, original),
	)
}

func provenanceSafeArtifactRecoveryError(original, recovery, label string, cause error) error {
	return fmt.Errorf(
		"%s %s changed from its durable provenance; divergent file preserved at recovery location %s: %w",
		label, original, recovery, cause,
	)
}

func removeProvenanceSafeArtifactQuarantineDir(path, label string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s cleanup quarantine %s: %w", label, path, err)
	}
	if err := fileutil.SyncDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync %s cleanup quarantine parent for %s: %w", label, path, err)
	}
	return nil
}

func validateServiceSandboxMutationRecord(name string, sv db.ServiceView) error {
	if err := validateServiceSandboxMutationEligibility(name, sv); err != nil {
		return err
	}
	service := sv.AsStruct()
	if err := validateServiceSandboxMutationGeneration(name, service); err != nil {
		return err
	}
	if err := validateServiceSandboxMutationArtifactRefs(name, service); err != nil {
		return err
	}
	return validateRequiredServiceSandboxMutationArtifacts(name, sv)
}

func validateServiceSandboxMutationEligibility(name string, sv db.ServiceView) error {
	if name == CatchService || name == SystemService {
		return fmt.Errorf("service %q is privileged host infrastructure and cannot use sandbox settings", name)
	}
	if sv.ServiceType() != db.ServiceTypeSystemd || sv.Generation() <= 0 {
		return fmt.Errorf("service %q: %s", name, sandboxNativePayloadOnlyMessage)
	}
	return nil
}

func validateServiceSandboxMutationGeneration(name string, service *db.Service) error {
	if service.LatestGeneration < service.Generation {
		return fmt.Errorf("service %q latest generation %d is behind active generation %d", name, service.LatestGeneration, service.Generation)
	}
	if service.LatestGeneration == int(^uint(0)>>1) {
		return fmt.Errorf("service %q next generation overflow after %d", name, service.LatestGeneration)
	}
	if service.Sandbox != nil {
		policy, ok := service.Sandbox.Refs[db.Gen(service.Generation)]
		if !ok {
			return fmt.Errorf("service %q active generation has no exact sandbox policy", name)
		}
		if policy == nil {
			return fmt.Errorf("service %q active generation has a nil exact sandbox policy", name)
		}
	}
	return nil
}

func validateServiceSandboxMutationArtifactRefs(name string, service *db.Service) error {
	for artifact, record := range service.Artifacts {
		if record == nil || record.Refs == nil {
			return fmt.Errorf("service %q active generation has an invalid %s artifact record", name, artifact)
		}
		path, ok := record.Refs[db.Gen(service.Generation)]
		if !ok || strings.TrimSpace(path) == "" {
			return fmt.Errorf("service %q active generation has no exact %s artifact", name, artifact)
		}
	}
	return nil
}

func validateRequiredServiceSandboxMutationArtifacts(name string, sv db.ServiceView) error {
	for _, artifact := range []db.ArtifactName{db.ArtifactSystemdUnit, db.ArtifactBinary} {
		path, ok := activeGenerationArtifactPath(sv, artifact)
		if !ok || strings.TrimSpace(path) == "" {
			return fmt.Errorf("service %q active generation has no %s artifact", name, artifact)
		}
		if err := validateReadableServiceSandboxArtifact(path); err != nil {
			return fmt.Errorf("validate service %q active %s artifact: %w", name, artifact, err)
		}
	}
	return nil
}

func validateReadableServiceSandboxArtifact(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	_, readErr := io.Copy(io.Discard, file)
	return errors.Join(readErr, file.Close())
}

func (s *Server) updateServiceSandboxLocked(ctx context.Context, name string, options cli.SandboxOptions, out io.Writer) (retErr error) {
	plan, err := s.planServiceSandboxMutation(ctx, name, options)
	if err != nil || plan.noOp {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			retErr = errors.Join(retErr, removeServiceSandboxMutationUnitForUpdate(plan.stagedUnitProof))
		}
	}()
	if err := serviceSandboxMutationContext(ctx); err != nil {
		return err
	}
	request, err := serviceSandboxMutationRequest(ctx, plan)
	if err != nil {
		return fmt.Errorf("prepare service %q sandbox transaction: %w", name, err)
	}
	if err := serviceSandboxMutationContext(ctx); err != nil {
		return err
	}
	if out == nil {
		out = io.Discard
	}
	result, err := migrateServiceSandboxGeneration(ctx, s, request, out)
	committed = result.Committed
	if err != nil {
		return fmt.Errorf("update sandbox for service %q: %w", name, err)
	}
	committed = true
	if result.Restarted {
		_, err = fmt.Fprintf(out, "Updated sandbox for %s and restarted its runtime.\n", name)
	} else {
		_, err = fmt.Fprintf(out, "Updated sandbox for %s.\n", name)
	}
	return err
}

func serviceSandboxMutationRequest(ctx context.Context, plan *serviceSandboxMutationPlan) (serviceIdentityMigrationRequest, error) {
	if plan == nil || plan.previous == nil || plan.target == nil || plan.stage == nil {
		return serviceIdentityMigrationRequest{}, errors.New("sandbox mutation plan is incomplete")
	}
	diagnostic, err := serviceSandboxGenerationDiagnostic(plan.previous.Name, plan.target)
	if err != nil {
		return serviceIdentityMigrationRequest{}, err
	}
	enablement, err := captureServiceSandboxEnablementForMutation(ctx, plan.previous, plan.units)
	if err != nil {
		return serviceIdentityMigrationRequest{}, err
	}
	identity := plan.identity.Persisted
	return serviceIdentityMigrationRequest{
		Service: plan.previous.Name, Requested: identity.RequestedUser + ":" + identity.RequestedGroup,
		Target: plan.identity, TargetService: plan.target, ReplacementUnit: plan.replacement,
		StageGeneration: plan.stage, GenerationPaths: plan.generationPaths,
		GenerationIntents: plan.intent, GenerationUnits: plan.units,
		GenerationDiagnostic:          diagnostic,
		GenerationEnablement:          &enablement,
		PreserveTargetServiceIdentity: true,
	}, nil
}

func serviceSandboxGenerationDiagnostic(name string, target *db.Service) (serviceIdentityGenerationDiagnostic, error) {
	policy, err := serviceSandboxPolicyForExactGeneration(target, target.Generation)
	if err != nil {
		return serviceIdentityGenerationDiagnostic{}, fmt.Errorf("load service %q target sandbox policy for recovery: %w", name, err)
	}
	args := []string{"yeet", "service", "set", name, "--sandbox=" + policy.State, "--sandbox-ro=reset"}
	for _, exposure := range policy.ReadOnly {
		args = append(args, "--sandbox-ro="+formatServiceSandboxExposure(exposure))
	}
	args = append(args, "--sandbox-rw=reset")
	for _, exposure := range policy.Writable {
		args = append(args, "--sandbox-rw="+formatServiceSandboxExposure(exposure))
	}
	return serviceIdentityGenerationDiagnostic{
		Mutation: "sandbox service generation mutation",
		Retry:    shellJoinHostStorageRecoveryArgs(args),
	}, nil
}

func captureServiceSandboxMutationEnablement(ctx context.Context, previous *db.Service, units []string) ([]serviceIdentityUnitEnablement, error) {
	ops := serviceIdentityMigrationOps{isEnabled: serviceIdentitySystemdUnitEnabled}
	if serviceIdentityUsesTimer(previous) {
		ops.isEnabled = serviceScheduleSystemdUnitEnabled
	}
	return captureServiceScheduleEnablement(ctx, ops, previous, units)
}
