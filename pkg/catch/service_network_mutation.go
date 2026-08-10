// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	serviceenv "github.com/yeetrun/yeet/pkg/env"
	"github.com/yeetrun/yeet/pkg/iso"
	"github.com/yeetrun/yeet/pkg/netns"
	"github.com/yeetrun/yeet/pkg/svc"
)

var isServiceRunningForNetworkMutation = func(s *Server, name string) (bool, error) {
	return s.IsServiceRunning(name)
}

var migrateServiceNetworkIdentityLocked = func(ctx context.Context, s *Server, request serviceIdentityMigrationRequest, out io.Writer) (serviceIdentityMigrationResult, error) {
	return s.migrateServiceIdentityLocked(ctx, request, out)
}

var migrateServiceNetworkIdentityWithResolverGuardLocked = func(ctx context.Context, s *Server, request serviceIdentityMigrationRequest, out io.Writer) (serviceIdentityMigrationResult, error) {
	return s.migrateServiceIdentityLockedWithResolverGuard(ctx, request, out)
}

var prepareServiceNetworkIdentityReplacement = func(ctx context.Context, s *Server, plan *serviceNetworkMutationPlan, flags cli.ServiceSetFlags, out io.Writer) (*db.Service, resolvedServiceIdentity, string, *svc.SystemdService, error) {
	return s.prepareServiceNetworkIdentityReplacement(ctx, plan, flags, out)
}

var runRegularNetworkSystemctlForRuntime = func(ctx context.Context, args ...string) ([]byte, error) {
	return runISOSystemctlForRuntime(ctx, args...)
}

var inspectRegularNetworkUnitEnabled = func(ctx context.Context, unit string) (bool, error) {
	_, err := runRegularNetworkSystemctlForRuntime(ctx, "is-enabled", unit)
	if err == nil {
		return true, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	return false, nil
}

var inspectRegularNetworkUnitActive = func(ctx context.Context, unit string) (bool, error) {
	output, err := runRegularNetworkSystemctlForRuntime(ctx, "show", "--property=ActiveState", "--value", unit)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, fmt.Errorf("inspect active state for %s: %w: %s", unit, err, strings.TrimSpace(string(output)))
	}
	switch state := strings.TrimSpace(string(output)); state {
	case "inactive", "failed":
		return false, nil
	case "active", "activating", "reloading", "deactivating":
		return true, nil
	default:
		return false, fmt.Errorf("inspect active state for %s: unexpected state %q", unit, state)
	}
}

var enableRegularNetworkUnit = func(ctx context.Context, unit string) error {
	_, err := runRegularNetworkSystemctlForRuntime(ctx, "enable", unit)
	return err
}

var disableRegularNetworkUnit = func(ctx context.Context, unit string) error {
	_, err := runRegularNetworkSystemctlForRuntime(ctx, "disable", unit)
	return err
}

var withRegularNetworkResolverMutationGuard = func(s *Server, run func() error) error {
	return s.withTailscaleResolverMutationGuard(run)
}

var checkRegularNetworkResolverCanonicalReady = func(ctx context.Context, s *Server, service db.Service) error {
	return s.checkTailscaleResolverCanonicalReady(ctx, service)
}

type serviceNetworkMutationSteps interface {
	Stage(context.Context) error
	StopPrevious(context.Context) error
	Activate(context.Context) error
	Verify(context.Context) error
	Commit(context.Context) error
	Restore(context.Context) error
	FailClosed(context.Context) error
}

type serviceNetworkRecoveryOwnership uint8

const (
	serviceNetworkRecoveryOwnershipUnowned serviceNetworkRecoveryOwnership = iota
	serviceNetworkRecoveryOwnershipOwned
)

type serviceNetworkRecoveryError struct {
	Ownership serviceNetworkRecoveryOwnership
	Err       error
}

func (e *serviceNetworkRecoveryError) Error() string { return e.Err.Error() }
func (e *serviceNetworkRecoveryError) Unwrap() error { return e.Err }

func newServiceNetworkRecoveryError(ownership serviceNetworkRecoveryOwnership, err error) error {
	if err == nil {
		return nil
	}
	return &serviceNetworkRecoveryError{Ownership: ownership, Err: err}
}

func serviceNetworkRecoveryIsUnowned(err error) bool {
	var recoveryErr *serviceNetworkRecoveryError
	return errors.As(err, &recoveryErr) && recoveryErr.Ownership == serviceNetworkRecoveryOwnershipUnowned
}

type serviceNetworkResolverCanonicalTarget interface {
	ResolverCanonicalTarget() *db.Service
}

type serviceNetworkStagedArtifactCleanup interface {
	DiscardStagedArtifacts() error
}

type serviceNetworkCommittedArtifactCleanup interface {
	CleanupCommittedArtifacts(context.Context) error
}

type resolverReadyServiceNetworkMutationSteps struct {
	server *Server
	plan   *serviceNetworkMutationPlan
	steps  serviceNetworkMutationSteps
}

func (s *resolverReadyServiceNetworkMutationSteps) Stage(ctx context.Context) error {
	if err := s.steps.Stage(ctx); err != nil {
		return err
	}
	canonical := s.plan.previous
	if slices.Contains(s.plan.desired.Modes, "ts") {
		provider, ok := s.steps.(serviceNetworkResolverCanonicalTarget)
		if !ok || provider.ResolverCanonicalTarget() == nil {
			return errors.New("staged Tailscale service network has no canonical readiness target")
		}
		canonical = provider.ResolverCanonicalTarget()
	}
	if canonical == nil {
		return errors.New("service network mutation has no canonical resolver readiness target")
	}
	if err := checkRegularNetworkResolverCanonicalReady(ctx, s.server, *canonical); err != nil {
		if cleanup, ok := s.steps.(serviceNetworkStagedArtifactCleanup); ok {
			return errors.Join(err, cleanup.DiscardStagedArtifacts())
		}
		return err
	}
	return nil
}

func (s *resolverReadyServiceNetworkMutationSteps) StopPrevious(ctx context.Context) error {
	return s.steps.StopPrevious(ctx)
}

func (s *resolverReadyServiceNetworkMutationSteps) Activate(ctx context.Context) error {
	return s.steps.Activate(ctx)
}

func (s *resolverReadyServiceNetworkMutationSteps) Verify(ctx context.Context) error {
	return s.steps.Verify(ctx)
}

func (s *resolverReadyServiceNetworkMutationSteps) Commit(ctx context.Context) error {
	return s.steps.Commit(ctx)
}

func (s *resolverReadyServiceNetworkMutationSteps) Restore(ctx context.Context) error {
	return s.steps.Restore(ctx)
}

func (s *resolverReadyServiceNetworkMutationSteps) FailClosed(ctx context.Context) error {
	return s.steps.FailClosed(ctx)
}

func (s *resolverReadyServiceNetworkMutationSteps) DiscardStagedArtifacts() error {
	if cleanup, ok := s.steps.(serviceNetworkStagedArtifactCleanup); ok {
		return cleanup.DiscardStagedArtifacts()
	}
	return nil
}

func (s *resolverReadyServiceNetworkMutationSteps) CleanupCommittedArtifacts(ctx context.Context) error {
	if cleanup, ok := s.steps.(serviceNetworkCommittedArtifactCleanup); ok {
		return cleanup.CleanupCommittedArtifacts(ctx)
	}
	return nil
}

var serviceNetworkRecoveryTimeout = 30 * time.Second

const regularServiceNetworkAllocationLockName = "\x00regular-service-network-allocation"

func runServiceNetworkMutation(ctx context.Context, steps serviceNetworkMutationSteps) error {
	if err := steps.Stage(ctx); err != nil {
		return fmt.Errorf("stage service network replacement: %w", err)
	}
	for _, step := range []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "stop previous service network", run: steps.StopPrevious},
		{name: "activate service network replacement", run: steps.Activate},
		{name: "verify service network replacement", run: steps.Verify},
		{name: "commit service network replacement", run: steps.Commit},
	} {
		if err := step.run(ctx); err != nil {
			cause := fmt.Errorf("%s: %w", step.name, err)
			restoreCtx, cancelRestore := serviceNetworkRecoveryContext(ctx)
			restoreErr := steps.Restore(restoreCtx)
			cancelRestore()
			if restoreErr == nil {
				return cause
			}
			if serviceNetworkRecoveryIsUnowned(restoreErr) {
				return errors.Join(cause, fmt.Errorf("restore previous service network: %w", restoreErr))
			}
			failClosedCtx, cancelFailClosed := serviceNetworkRecoveryContext(ctx)
			failClosedErr := steps.FailClosed(failClosedCtx)
			cancelFailClosed()
			return errors.Join(cause, fmt.Errorf("restore previous service network: %w", restoreErr), failClosedErr)
		}
	}
	return nil
}

func serviceNetworkRecoveryContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), serviceNetworkRecoveryTimeout)
}

// serviceNetworkRecordsEqual compares the exact state that survives database
// publication. In-memory empty slices may be omitted by JSON and reload as nil;
// treating those representations as different would reject an unchanged record.
func serviceNetworkRecordsEqual(left, right *db.Service) bool {
	if left == nil || right == nil {
		return left == right
	}
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func serviceNetworkISOClaimRecordsEqual(current, expected *db.Service, pool *db.ISOPool, boundaryFailure error) bool {
	if serviceNetworkRecordsEqual(current, expected) {
		return true
	}
	if !serviceNetworkISOBoundaryFailureMatches(pool, boundaryFailure) {
		return false
	}
	if !serviceNetworkISOQuarantineMatches(current, expected, boundaryFailure) {
		return false
	}
	normalized := current.Clone()
	normalized.ISO.State = expected.ISO.State
	normalized.ISO.LastError = expected.ISO.LastError
	return serviceNetworkRecordsEqual(normalized, expected)
}

func serviceNetworkISOBoundaryFailureMatches(pool *db.ISOPool, boundaryFailure error) bool {
	if boundaryFailure == nil || pool == nil {
		return false
	}
	return pool.AggregateRouteState == "conflict" && pool.LastConflict == boundaryFailure.Error()
}

func serviceNetworkISOQuarantineMatches(current, expected *db.Service, boundaryFailure error) bool {
	if current == nil || current.ISO == nil || expected == nil || expected.ISO == nil || boundaryFailure == nil {
		return false
	}
	return current.ISO.State == string(iso.StateQuarantined) &&
		current.ISO.LastError == boundaryFailure.Error()
}

type serviceNetworkMutationPlan struct {
	name               string
	previous           *db.Service
	currentDesired     db.ServiceNetworkConfig
	desired            db.ServiceNetworkConfig
	network            NetworkOpts
	noOp               bool
	previousRunning    bool
	previousRuntime    []serviceIdentityRuntimeUnitState
	previousEnablement []serviceIdentityUnitEnablement
	artifactTxn        *regularNetworkArtifactTransaction
	deferSandbox       bool
}

type regularNetworkArtifactTransaction struct {
	root          string
	previousPaths map[string]serviceIdentityPathProof
	stagedPaths   map[string]serviceIdentityPathProof
	finished      bool
}

var retirableRegularNetworkArtifactNames = map[db.ArtifactName]bool{
	db.ArtifactSystemdUnit:          true,
	db.ArtifactDockerComposeNetwork: true,
	db.ArtifactNetNSService:         true,
	db.ArtifactNetNSEnv:             true,
	db.ArtifactNetNSResolv:          true,
	db.ArtifactTSService:            true,
	db.ArtifactTSEnv:                true,
	db.ArtifactTSConfig:             true,
}

func beginRegularNetworkArtifactTransaction(root string, previous *db.Service) (*regularNetworkArtifactTransaction, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("service network artifact root must be absolute: %q", root)
	}
	txn := &regularNetworkArtifactTransaction{
		root:          root,
		previousPaths: map[string]serviceIdentityPathProof{},
		stagedPaths:   map[string]serviceIdentityPathProof{},
	}
	if err := txn.capturePreviousPaths(previous); err != nil {
		return nil, err
	}
	return txn, nil
}

func (t *regularNetworkArtifactTransaction) capturePreviousPaths(previous *db.Service) error {
	if previous == nil {
		return nil
	}
	for artifactName, artifact := range previous.Artifacts {
		if artifact == nil || !retirableRegularNetworkArtifactNames[artifactName] {
			continue
		}
		for _, path := range artifact.Refs {
			path = filepath.Clean(path)
			if t.manages(path) {
				proof, err := t.capture(path)
				if err != nil {
					return fmt.Errorf("capture current %s artifact %s: %w", artifactName, path, err)
				}
				t.previousPaths[path] = proof
			}
		}
	}
	return nil
}

func (t *regularNetworkArtifactTransaction) registerStagedPath(path string) error {
	if t == nil || t.finished {
		return errors.New("service network artifact transaction is closed")
	}
	path = filepath.Clean(path)
	if !t.manages(path) {
		return fmt.Errorf("staged artifact is outside service root: %s", path)
	}
	proof, err := t.capture(path)
	if err != nil {
		return err
	}
	if !proof.Present {
		return fmt.Errorf("staged artifact %s is absent", path)
	}
	if !proof.Mode.IsRegular() {
		return fmt.Errorf("staged artifact %s is not a regular file", path)
	}
	return t.registerStagedProof(proof)
}

func (t *regularNetworkArtifactTransaction) registerStagedProof(proof serviceIdentityPathProof) error {
	if t == nil || t.finished {
		return errors.New("service network artifact transaction is closed")
	}
	path := filepath.Clean(proof.Path)
	if !t.manages(path) || !proof.Present || !proof.Mode.IsRegular() {
		return fmt.Errorf("staged artifact %s lacks valid transaction provenance", path)
	}
	t.stagedPaths[path] = proof
	return nil
}

func (t *regularNetworkArtifactTransaction) rollback(server *Server) error {
	if t == nil || t.finished {
		return nil
	}
	if server == nil || server.cfg.DB == nil {
		return errors.New("rollback service network artifacts without a config DB")
	}
	_, err := server.cfg.DB.MutateData(func(data *db.Data) error {
		return t.rollbackUnreferenced(regularNetworkReferencedArtifactPaths(data.View()))
	})
	return err
}

func (t *regularNetworkArtifactTransaction) rollbackUnreferenced(referenced map[string]bool) error {
	var errs []error
	for path, proof := range t.stagedPaths {
		if referenced[path] {
			errs = append(errs, fmt.Errorf("staged service network artifact %s is referenced by a live service record", path))
			continue
		}
		actual, err := t.capture(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("inspect staged service network artifact %s: %w", path, err))
			continue
		}
		if !reflect.DeepEqual(actual, proof) {
			errs = append(errs, fmt.Errorf("transaction path %s changed from its durable provenance", path))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	for path, proof := range t.stagedPaths {
		if err := t.remove(proof); err != nil {
			errs = append(errs, fmt.Errorf("remove staged service network artifact %s: %w", path, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	t.finished = true
	return nil
}

func (t *regularNetworkArtifactTransaction) cleanupCommitted(server *Server) error {
	if t == nil || t.finished {
		return nil
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		return fmt.Errorf("load committed service artifact references: %w", err)
	}
	referenced := regularNetworkReferencedArtifactPaths(dv)
	candidates := regularNetworkArtifactCleanupCandidates(t.previousPaths, t.stagedPaths)
	if err := t.removeUnreferenced(candidates, referenced); err != nil {
		return err
	}
	t.finished = true
	return nil
}

func regularNetworkArtifactCleanupCandidates(pathSets ...map[string]serviceIdentityPathProof) map[string]serviceIdentityPathProof {
	count := 0
	for _, paths := range pathSets {
		count += len(paths)
	}
	candidates := make(map[string]serviceIdentityPathProof, count)
	for _, paths := range pathSets {
		for path, proof := range paths {
			candidates[path] = proof
		}
	}
	return candidates
}

func (t *regularNetworkArtifactTransaction) removeUnreferenced(candidates map[string]serviceIdentityPathProof, referenced map[string]bool) error {
	var errs []error
	for path, proof := range candidates {
		if referenced[path] {
			continue
		}
		if err := t.remove(proof); err != nil {
			errs = append(errs, fmt.Errorf("remove superseded service network artifact %s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

func (t *regularNetworkArtifactTransaction) committedArtifactReferenced(server *Server, service string) (bool, error) {
	if t == nil {
		return false, nil
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		return false, err
	}
	sv, ok := dv.Services().GetOk(service)
	if !ok {
		return false, nil
	}
	for _, artifact := range sv.AsStruct().Artifacts {
		if artifact == nil {
			continue
		}
		for _, path := range artifact.Refs {
			if _, generated := t.stagedPaths[filepath.Clean(path)]; generated {
				return true, nil
			}
		}
	}
	return false, nil
}

func regularNetworkReferencedArtifactPaths(dv db.DataView) map[string]bool {
	referenced := map[string]bool{}
	for _, service := range dv.Services().All() {
		for _, artifact := range service.AsStruct().Artifacts {
			if artifact == nil {
				continue
			}
			for _, path := range artifact.Refs {
				referenced[filepath.Clean(path)] = true
			}
		}
	}
	return referenced
}

func (t *regularNetworkArtifactTransaction) remove(proof serviceIdentityPathProof) error {
	path := filepath.Clean(proof.Path)
	if !t.manages(path) {
		return fmt.Errorf("refuse to remove artifact outside service root: %s", path)
	}
	actual, err := t.capture(path)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, proof) {
		return fmt.Errorf("transaction path %s changed from its durable provenance", path)
	}
	if !actual.Present {
		return nil
	}
	rel, err := filepath.Rel(t.root, path)
	if err != nil {
		return err
	}
	return removeServiceIdentityProofAt(t.root, rel, proof)
}

func (t *regularNetworkArtifactTransaction) manages(path string) bool {
	path = filepath.Clean(path)
	return pathWithinServiceIdentityRoot(t.root, path)
}

func (t *regularNetworkArtifactTransaction) capture(path string) (serviceIdentityPathProof, error) {
	path = filepath.Clean(path)
	if !t.manages(path) {
		return serviceIdentityPathProof{}, fmt.Errorf("artifact is outside service root: %s", path)
	}
	rel, err := filepath.Rel(t.root, path)
	if err != nil {
		return serviceIdentityPathProof{}, err
	}
	return captureServiceIdentityPathProofAt(t.root, rel, path)
}

var newRegularServiceNetworkMutationSteps = func(s *Server, plan *serviceNetworkMutationPlan) serviceNetworkMutationSteps {
	return &regularServiceNetworkMutation{server: s, plan: plan}
}

type serviceNetworkISOTransition string

const (
	serviceNetworkRegularToISO serviceNetworkISOTransition = "regular-to-iso"
	serviceNetworkISOToISO     serviceNetworkISOTransition = "iso-to-iso"
	serviceNetworkISOToRegular serviceNetworkISOTransition = "iso-to-regular"
)

var newISOServiceNetworkMutationSteps = func(s *Server, plan *serviceNetworkMutationPlan, direction serviceNetworkISOTransition) serviceNetworkMutationSteps {
	return &isoServiceNetworkMutation{server: s, plan: plan, direction: direction}
}

var activatePreviousISONetworkRuntimeForMutation = func(ctx context.Context, mutation *isoServiceNetworkMutation) error {
	return mutation.activatePreviousServiceNetworkRuntime(ctx)
}

var stageRegularNetworkSystemdArtifactsForMutation = func(service *svc.SystemdService) ([]string, error) {
	return service.StageInstallForReload()
}

var mutateServiceNetworkRestoreData = (*db.Store).MutateData

var verifyRegularNetworkComposeProjectAbsentForMutation = func(ctx context.Context, compose *svc.DockerComposeService) error {
	return compose.VerifyProjectAbsent(ctx)
}

type isoServiceNetworkMutation struct {
	server             *Server
	plan               *serviceNetworkMutationPlan
	direction          serviceNetworkISOTransition
	target             *db.Service
	staged             *db.Service
	reservationPending bool
	spec               isoRuntimeNetworkSpec
	committed          bool
	boundaryFailure    error
	regular            *regularServiceNetworkMutation
	compose            *svc.DockerComposeService
}

func (m *isoServiceNetworkMutation) ResolverCanonicalTarget() *db.Service { return m.target }

func (m *isoServiceNetworkMutation) DiscardStagedArtifacts() error {
	txn := m.artifactTransaction()
	if txn == nil {
		return nil
	}
	if m.shouldRollbackStagedISO() {
		return m.rollbackStagedISO()
	}
	return txn.rollback(m.server)
}

func (m *isoServiceNetworkMutation) artifactTransaction() *regularNetworkArtifactTransaction {
	if m == nil || m.plan == nil {
		return nil
	}
	return m.plan.artifactTxn
}

func (m *isoServiceNetworkMutation) shouldRollbackStagedISO() bool {
	return m.staged != nil && !m.reservationPending && !m.committed && m.direction != serviceNetworkISOToRegular
}

func (m *isoServiceNetworkMutation) CleanupCommittedArtifacts(context.Context) error {
	if m == nil || m.plan == nil || m.plan.artifactTxn == nil {
		return nil
	}
	return m.plan.artifactTxn.cleanupCommitted(m.server)
}

func (m *isoServiceNetworkMutation) Stage(ctx context.Context) error {
	if m.direction == serviceNetworkISOToRegular {
		return m.stageISOToRegular(ctx)
	}
	return m.stageDesiredISO(ctx)
}

func (m *isoServiceNetworkMutation) stageISOToRegular(ctx context.Context) error {
	spec, err := m.server.loadISORuntimeSpec(m.plan.name)
	if err != nil {
		return err
	}
	m.spec = spec
	m.regular = &regularServiceNetworkMutation{server: m.server, plan: m.plan}
	if err := m.regular.Stage(ctx); err != nil {
		return err
	}
	m.target = m.regular.target
	if err := m.loadCompose(m.plan.previous); err != nil {
		return errors.Join(err, m.regular.DiscardStagedArtifacts())
	}
	return nil
}

func (m *isoServiceNetworkMutation) stageDesiredISO(ctx context.Context) error {
	target, staged, err := m.server.stageISOServiceNetworkReplacement(ctx, m.plan)
	if err != nil {
		return err
	}
	m.target, m.staged = target, staged
	m.regular = &regularServiceNetworkMutation{server: m.server, plan: m.plan, target: target}
	m.spec, err = m.server.loadISORuntimeSpec(m.plan.name)
	if err != nil {
		return errors.Join(err, m.rollbackStagedISO())
	}
	if err := m.loadCompose(target); err != nil {
		return errors.Join(err, m.rollbackStagedISO())
	}
	return nil
}

func (m *isoServiceNetworkMutation) stagePlannedDesiredISO(ctx context.Context) error {
	target, staged, plannedData, err := m.server.planNativeISOServiceNetworkReplacement(ctx, m.plan)
	if err != nil {
		return err
	}
	m.target, m.staged = target, staged
	m.reservationPending = true
	m.regular = &regularServiceNetworkMutation{server: m.server, plan: m.plan, target: target}
	m.spec, err = m.server.isoRuntimeSpec(plannedData.View(), m.plan.name)
	return err
}

func (m *isoServiceNetworkMutation) publishPlannedISOReservation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !m.reservationPending || m.staged == nil || m.staged.ISO == nil {
		return fmt.Errorf("service %q has no pending preflighted ISO reservation", m.plan.name)
	}
	request := isoReservationRequest{Kind: iso.PayloadNative, Modes: slices.Clone(m.plan.desired.Modes)}
	_, err := m.server.cfg.DB.MutateData(func(data *db.Data) error {
		return m.publishPlannedISOReservationInData(data, request)
	})
	if err != nil {
		if dbMutationCommitted(err) {
			m.reservationPending = false
			return errors.Join(err, m.rollbackStagedISO())
		}
		return err
	}
	m.reservationPending = false
	return nil
}

func (m *isoServiceNetworkMutation) publishPlannedISOReservationInData(data *db.Data, request isoReservationRequest) error {
	current := data.Services[m.plan.name]
	if !serviceNetworkRecordsEqual(current, m.plan.previous) {
		return fmt.Errorf("service %q changed before publishing preflighted ISO reservation", m.plan.name)
	}
	if isoAllocationClaimedByPeer(data.Services, m.plan.name, m.staged.ISO) {
		return fmt.Errorf("service %q ISO allocation was claimed after sandbox preflight", m.plan.name)
	}
	candidate := current.Clone()
	allocation, err := reserveISOAllocationInData(m.plan.name, request, data, candidate)
	if err != nil {
		return err
	}
	if !serviceNetworkRecordsEqual(candidate, m.staged) || !reflect.DeepEqual(allocation, m.staged.ISO) {
		return fmt.Errorf("service %q ISO allocation changed after sandbox preflight", m.plan.name)
	}
	data.Services[m.plan.name] = m.staged.Clone()
	return nil
}

func isoAllocationClaimedByPeer(services map[string]*db.Service, name string, allocation *db.ISOAllocation) bool {
	if allocation == nil || !allocation.Link.IsValid() {
		return false
	}
	link := allocation.Link.Masked()
	for peerName, service := range services {
		if peerName == name || service == nil || service.ISO == nil || !service.ISO.Link.IsValid() {
			continue
		}
		if service.ISO.Link.Masked() == link {
			return true
		}
	}
	return false
}

func (m *isoServiceNetworkMutation) rollbackStagedISO() error {
	restoreErr := m.restorePreviousAfterISOReservation()
	if restoreErr != nil {
		return errors.Join(restoreErr, m.tombstoneFailedISOStageRestore(restoreErr))
	}
	if txn := m.artifactTransaction(); txn != nil {
		return txn.rollback(m.server)
	}
	return nil
}

func (m *isoServiceNetworkMutation) restorePreviousAfterISOReservation() error {
	_, err := m.server.cfg.DB.MutateData(func(data *db.Data) error {
		current := data.Services[m.plan.name]
		if err := m.validateStagedISOReservation(current); err != nil {
			return err
		}
		data.Services[m.plan.name] = m.plan.previous.Clone()
		return nil
	})
	return err
}

func (m *isoServiceNetworkMutation) validateStagedISOReservation(current *db.Service) error {
	if m.staged == nil || !serviceNetworkRecordsEqual(current, m.staged) {
		return fmt.Errorf("service %q changed while rolling back staged ISO network", m.plan.name)
	}
	return nil
}

func (m *isoServiceNetworkMutation) tombstoneFailedISOStageRestore(restoreErr error) error {
	if restoreErr == nil {
		return nil
	}
	return m.server.markISOStateExact(m.plan.name, m.staged, string(iso.StateTombstoned), restoreErr, "rolling back staged ISO network")
}

func (s *Server) markISOStateExact(name string, expected *db.Service, state string, cause error, operation string) error {
	if expected == nil {
		return fmt.Errorf("service %q cannot update ISO state during %s without an exact expected record", name, operation)
	}
	_, err := s.cfg.DB.MutateData(func(data *db.Data) error {
		current := data.Services[name]
		if !serviceNetworkRecordsEqual(current, expected) {
			return fmt.Errorf("service %q changed while %s", name, operation)
		}
		if current.ISO == nil {
			return fmt.Errorf("service %q has no ISO allocation while %s", name, operation)
		}
		current.ISO.State = state
		current.ISO.LastError = ""
		if cause != nil {
			current.ISO.LastError = cause.Error()
		}
		return nil
	})
	return err
}

func (m *isoServiceNetworkMutation) loadCompose(record *db.Service) error {
	if record == nil || record.ServiceType != db.ServiceTypeDockerCompose {
		return nil
	}
	compose, err := m.regular.composeService(record)
	if err != nil {
		return err
	}
	m.compose = compose
	return nil
}

func (m *isoServiceNetworkMutation) StopPrevious(ctx context.Context) error {
	if m.plan.previous.ISO == nil {
		return m.regular.StopPrevious(ctx)
	}
	return m.stopISORecord(ctx, m.plan.previous)
}

func (m *isoServiceNetworkMutation) stopISORecord(ctx context.Context, record *db.Service) error {
	switch record.ServiceType {
	case db.ServiceTypeSystemd:
		return stopAndVerifyISONativeUnits(ctx, record)
	case db.ServiceTypeDockerCompose:
		compose := m.compose
		if compose == nil {
			var err error
			compose, err = m.regular.composeService(record)
			if err != nil {
				return err
			}
		}
		return errors.Join(compose.StopProjectContainers(ctx), stopAndVerifyISOAuxiliaryUnits(ctx, record))
	default:
		return fmt.Errorf("stop ISO service type %q", record.ServiceType)
	}
}

func (m *isoServiceNetworkMutation) Activate(ctx context.Context) error {
	if m.direction == serviceNetworkISOToRegular {
		return removeISOTopologyForRuntime(ctx, m.spec.Topology)
	}
	if err := m.server.EnsureISONetworkBoundary(ctx, m.plan.name); err != nil {
		m.boundaryFailure = err
		return err
	}
	switch m.target.ServiceType {
	case db.ServiceTypeSystemd:
		return m.regular.activateSystemd(ctx)
	case db.ServiceTypeDockerCompose:
		return m.activateISOCompose(ctx)
	default:
		return fmt.Errorf("activate ISO service type %q", m.target.ServiceType)
	}
}

func (m *isoServiceNetworkMutation) activateISOCompose(ctx context.Context) error {
	if _, err := m.regular.installComposeDefinition(ctx, m.target); err != nil {
		return err
	}
	if err := m.compose.Create(ctx); err != nil {
		return err
	}
	if !m.plan.previousRunning {
		return nil
	}
	if err := m.compose.StartAuxiliaryUnits(); err != nil {
		return err
	}
	return m.compose.UpDetached(ctx, false)
}

func (m *isoServiceNetworkMutation) Verify(ctx context.Context) error {
	if m.direction == serviceNetworkISOToRegular {
		return m.verifyISOAbsent(ctx)
	}
	if err := m.server.EnsureISONetworkBoundary(ctx, m.plan.name); err != nil {
		m.boundaryFailure = err
		return err
	}
	if err := verifyISOTopologyForRuntime(ctx, m.spec.Topology); err != nil {
		return err
	}
	return m.verifyDesiredISORuntime(ctx)
}

func (m *isoServiceNetworkMutation) verifyDesiredISORuntime(ctx context.Context) error {
	switch m.target.ServiceType {
	case db.ServiceTypeSystemd:
		return m.verifyDesiredISOSystemdRuntime(ctx)
	case db.ServiceTypeDockerCompose:
		return m.verifyDesiredISOComposeRuntime(ctx)
	default:
		return fmt.Errorf("verify ISO service type %q", m.target.ServiceType)
	}
}

func (m *isoServiceNetworkMutation) verifyDesiredISOSystemdRuntime(ctx context.Context) error {
	if err := verifyRegularNetworkSystemdRuntime(ctx, m.server, m.plan.previous, m.target, regularNetworkTargetRuntimeIntent(m.plan, m.target)); err != nil {
		return err
	}
	return m.regular.verifyUnitEnablement(ctx, m.target)
}

func (m *isoServiceNetworkMutation) verifyDesiredISOComposeRuntime(ctx context.Context) error {
	running, err := m.compose.AnyRunningContext(ctx)
	if err != nil {
		return err
	}
	if running != m.plan.previousRunning {
		return fmt.Errorf("replacement running state is %t, want %t", running, m.plan.previousRunning)
	}
	return nil
}

func (m *isoServiceNetworkMutation) verifyISOAbsent(ctx context.Context) error {
	err := verifyISOTopologyAbsentForRuntime(ctx, m.spec.Topology)
	if m.compose != nil {
		err = errors.Join(err, m.compose.VerifyProjectAbsent(ctx), m.compose.VerifyDefaultNetworkAbsent(ctx))
	}
	return errors.Join(err, verifyISOAllocationDNetAbsent(m.server, m.spec.Topology.Allocation))
}

func (m *isoServiceNetworkMutation) Commit(ctx context.Context) error {
	if m.direction == serviceNetworkISOToRegular {
		return m.commitISOToRegular(ctx)
	}
	return m.commitDesiredISO()
}

func (m *isoServiceNetworkMutation) commitISOToRegular(ctx context.Context) error {
	prepared := isoReplacementNetwork{
		Modes: slices.Clone(m.plan.desired.Modes), Desired: m.plan.desired.Clone(), Expected: m.plan.previous.Clone(), SvcNetwork: cloneISOReplacementSvcNetwork(m.target.SvcNetwork),
		Macvlan: cloneISOReplacementMacvlan(m.target.Macvlan), Tailscale: m.target.TSNet.Clone(),
		Artifacts: stagedISOReplacementArtifacts(m.target),
	}
	if err := m.server.commitReplacementNetwork(m.plan.name, prepared); err != nil {
		return err
	}
	m.committed = true
	if err := m.regular.Activate(ctx); err != nil {
		return err
	}
	return m.regular.Verify(ctx)
}

func (m *isoServiceNetworkMutation) commitDesiredISO() error {
	target := m.target.Clone()
	target.ISO.State = m.desiredISOState()
	target.ISO.LastError = ""
	_, err := m.server.cfg.DB.MutateData(func(data *db.Data) error {
		if err := m.validateStagedISOCommit(data.Services[m.plan.name]); err != nil {
			return err
		}
		data.Services[m.plan.name] = target
		return nil
	})
	if err != nil {
		return err
	}
	m.target = target
	m.committed = true
	m.server.PublishEvent(Event{Type: EventTypeServiceConfigChanged, ServiceName: m.plan.name, Data: EventData{target.View()}})
	return nil
}

func (m *isoServiceNetworkMutation) desiredISOState() string {
	if m.plan.previousRunning || serviceIdentityAnyRuntimeActive(regularNetworkTargetRuntimeIntent(m.plan, m.target)) {
		return string(iso.StateReady)
	}
	return string(iso.StateStopped)
}

func (m *isoServiceNetworkMutation) validateStagedISOCommit(current *db.Service) error {
	if m.staged == nil || !serviceNetworkRecordsEqual(current, m.staged) {
		return fmt.Errorf("service %q changed during ISO network mutation", m.plan.name)
	}
	return nil
}

func stagedISOReplacementArtifacts(target *db.Service) db.ArtifactStore {
	artifacts := db.ArtifactStore{}
	if target == nil {
		return artifacts
	}
	for name := range isoReplacementArtifactNames {
		if artifact := target.Artifacts[name]; artifact != nil {
			artifacts[name] = artifact.Clone()
		}
	}
	return artifacts
}

func (m *isoServiceNetworkMutation) Restore(ctx context.Context) error {
	owned, claimErr := m.claimReplacementBeforeISORestore()
	if claimErr != nil && !owned {
		return newServiceNetworkRecoveryError(serviceNetworkRecoveryOwnershipUnowned, claimErr)
	}
	if err := m.stopReplacementBeforeISORestore(ctx); err != nil {
		return newServiceNetworkRecoveryError(serviceNetworkRecoveryOwnershipOwned, errors.Join(claimErr, err))
	}
	if m.direction == serviceNetworkISOToRegular && !owned {
		restoredOwned, warning, restoreErr := m.restoreUnclaimedISOToRegularRecordAndArtifacts()
		if !restoredOwned {
			return newServiceNetworkRecoveryError(serviceNetworkRecoveryOwnershipUnowned, errors.Join(claimErr, restoreErr))
		}
		claimErr = errors.Join(claimErr, warning)
		if restoreErr != nil {
			return newServiceNetworkRecoveryError(serviceNetworkRecoveryOwnershipOwned, errors.Join(claimErr, restoreErr))
		}
	} else if err := m.restorePreviousISORecordAndArtifacts(); err != nil {
		return newServiceNetworkRecoveryError(serviceNetworkRecoveryOwnershipOwned, errors.Join(claimErr, err))
	}
	return newServiceNetworkRecoveryError(serviceNetworkRecoveryOwnershipOwned, errors.Join(claimErr, m.restorePreviousISONetworkRuntime(ctx)))
}

func (m *isoServiceNetworkMutation) restoreUnclaimedISOToRegularRecordAndArtifacts() (owned bool, warning, restoreErr error) {
	recordErr := m.restorePreviousRecord()
	if recordErr != nil && !dbMutationCommitted(recordErr) {
		return false, nil, recordErr
	}
	if txn := m.artifactTransaction(); txn != nil {
		if err := txn.rollback(m.server); err != nil {
			return true, recordErr, err
		}
	}
	return true, recordErr, nil
}

func (m *isoServiceNetworkMutation) claimReplacementBeforeISORestore() (bool, error) {
	expected, claimed, err := m.isoRestoreClaimRecords()
	if err != nil || expected == nil {
		return false, err
	}
	_, err = mutateServiceNetworkRestoreData(m.server.cfg.DB, func(data *db.Data) error {
		if !serviceNetworkISOClaimRecordsEqual(data.Services[m.plan.name], expected, data.ISOPool, m.boundaryFailure) {
			return fmt.Errorf("service %q changed while rolling back staged ISO network; cannot claim exact record", m.plan.name)
		}
		data.Services[m.plan.name] = claimed.Clone()
		return nil
	})
	if err != nil && !dbMutationCommitted(err) {
		return false, err
	}
	m.staged = claimed
	return true, err
}

func (m *isoServiceNetworkMutation) isoRestoreClaimRecords() (expected, claimed *db.Service, err error) {
	if m.direction == serviceNetworkISOToRegular && !m.committed {
		return nil, nil, nil
	}
	expected = m.staged
	if m.committed {
		expected = m.target
	}
	if expected == nil || m.target == nil {
		return nil, nil, fmt.Errorf("service %q cannot claim ISO network rollback without an exact staged record", m.plan.name)
	}
	claimed = m.target.Clone()
	if m.direction == serviceNetworkISOToRegular {
		if m.plan.previous == nil || m.plan.previous.ISO == nil {
			return nil, nil, fmt.Errorf("service %q cannot claim ISO-to-regular rollback without the previous ISO allocation", m.plan.name)
		}
		claimed = m.plan.previous.Clone()
	}
	if claimed.ISO == nil {
		return nil, nil, fmt.Errorf("service %q cannot claim ISO network rollback without an ISO allocation", m.plan.name)
	}
	claimed.ISO.State = string(iso.StateTombstoned)
	claimed.ISO.LastError = "rolling back staged ISO network"
	return expected, claimed, nil
}

func (m *isoServiceNetworkMutation) stopReplacementBeforeISORestore(ctx context.Context) error {
	if m.direction != serviceNetworkISOToRegular {
		return errors.Join(m.stopISORecord(ctx, m.target), removeISOTopologyForRuntime(ctx, m.spec.Topology), m.verifyISOAbsent(ctx))
	}
	if m.committed {
		return m.regular.stopTarget(ctx)
	}
	return nil
}

func (m *isoServiceNetworkMutation) restorePreviousISORecordAndArtifacts() error {
	if m.direction != serviceNetworkISOToRegular {
		return m.rollbackStagedISO()
	}
	if err := m.restorePreviousRecord(); err != nil {
		return err
	}
	if txn := m.artifactTransaction(); txn != nil {
		return txn.rollback(m.server)
	}
	return nil
}

func (m *isoServiceNetworkMutation) restorePreviousISONetworkRuntime(ctx context.Context) error {
	if m.plan.previous.ISO != nil {
		if err := m.server.EnsureISONetworkBoundary(ctx, m.plan.name); err != nil {
			return err
		}
	}
	return activatePreviousISONetworkRuntimeForMutation(ctx, m)
}

func (m *isoServiceNetworkMutation) activatePreviousServiceNetworkRuntime(ctx context.Context) error {
	if m.plan.previous.ISO != nil {
		return m.restorePreviousISO(ctx)
	}
	previous := &regularServiceNetworkMutation{server: m.server, plan: m.plan, target: m.plan.previous}
	return previous.Activate(ctx)
}

func (m *isoServiceNetworkMutation) restorePreviousRecord() error {
	_, err := m.server.cfg.DB.MutateData(func(data *db.Data) error {
		current := data.Services[m.plan.name]
		switch {
		case serviceNetworkRecordsEqual(current, m.plan.previous):
			return nil
		case serviceNetworkRecordsEqual(current, m.staged):
			data.Services[m.plan.name] = m.plan.previous.Clone()
			return nil
		default:
			return fmt.Errorf("service %q changed while restoring ISO network mutation", m.plan.name)
		}
	})
	return err
}

func (m *isoServiceNetworkMutation) restorePreviousISO(ctx context.Context) error {
	restored := &isoServiceNetworkMutation{
		server: m.server, plan: m.plan, target: m.plan.previous,
		regular: &regularServiceNetworkMutation{server: m.server, plan: m.plan, target: m.plan.previous},
	}
	if err := restored.loadCompose(m.plan.previous); err != nil {
		return err
	}
	switch m.plan.previous.ServiceType {
	case db.ServiceTypeSystemd:
		return restored.regular.activateSystemd(ctx)
	case db.ServiceTypeDockerCompose:
		return restored.activateISOCompose(ctx)
	default:
		return fmt.Errorf("restore ISO service type %q", m.plan.previous.ServiceType)
	}
}

func (m *isoServiceNetworkMutation) FailClosed(ctx context.Context) error {
	if err := m.markISOTransitionTombstone(); err != nil {
		return err
	}
	return m.stopISORecordsFailClosed(ctx)
}

func (m *isoServiceNetworkMutation) stopISORecordsFailClosed(ctx context.Context) error {
	var errs []error
	for _, record := range []*db.Service{m.target, m.plan.previous} {
		if record == nil {
			continue
		}
		if record.ISO != nil {
			errs = append(errs, m.stopISORecord(ctx, record))
		} else {
			errs = append(errs, m.stopRegularRecordFailClosed(ctx, record))
		}
	}
	return errors.Join(errs...)
}

func (m *isoServiceNetworkMutation) stopRegularRecordFailClosed(ctx context.Context, record *db.Service) error {
	unitErr := stopRegularNetworkMutationUnitsFailClosed(ctx, record, record, m.plan.name)
	if record.ServiceType != db.ServiceTypeDockerCompose {
		return unitErr
	}
	regular := m.regular
	if regular == nil {
		regular = &regularServiceNetworkMutation{server: m.server, plan: m.plan, target: record}
	}
	return errors.Join(unitErr, regular.stopComposeRecordFailClosed(ctx, record))
}

func (m *isoServiceNetworkMutation) markISOTransitionTombstone() error {
	_, err := m.server.cfg.DB.MutateData(func(data *db.Data) error {
		current := data.Services[m.plan.name]
		if !m.failClosedRecordIsAttributable(current) {
			return fmt.Errorf("service %q changed before ISO fail-closed tombstone", m.plan.name)
		}
		record := m.failClosedISORecord(current)
		if record == nil {
			return fmt.Errorf("service %q has no ISO record for fail-closed recovery", m.plan.name)
		}
		data.Services[m.plan.name] = record
		if record.ISO != nil {
			record.ISO.State = string(iso.StateTombstoned)
			record.ISO.LastError = "service network restoration failed"
		}
		return nil
	})
	return err
}

func (m *isoServiceNetworkMutation) failClosedRecordIsAttributable(current *db.Service) bool {
	for _, expected := range []*db.Service{m.plan.previous, m.staged, m.target} {
		if expected != nil && serviceNetworkRecordsEqual(current, expected) {
			return true
		}
	}
	return false
}

func (m *isoServiceNetworkMutation) failClosedISORecord(current *db.Service) *db.Service {
	if serviceRecordHasISO(current) {
		return current
	}
	return m.failClosedISOFallbackRecord(current)
}

func serviceRecordHasISO(service *db.Service) bool {
	return service != nil && service.ISO != nil
}

func (m *isoServiceNetworkMutation) failClosedISOFallbackRecord(current *db.Service) *db.Service {
	if m.plan.previous.ISO != nil {
		return m.plan.previous.Clone()
	}
	if m.target != nil && m.target.ISO != nil {
		return m.target.Clone()
	}
	return current
}

func (s *Server) planServiceNetworkMutation(ctx context.Context, name string, flags cli.ServiceSetFlags) (*serviceNetworkMutationPlan, error) {
	sv, err := s.regularNetworkMutationService(name)
	if err != nil {
		return nil, err
	}
	current, desired, err := desiredRegularNetworkMutation(sv, flags)
	if err != nil {
		return nil, err
	}
	running, err := isServiceRunningForNetworkMutation(s, name)
	if err != nil {
		return nil, fmt.Errorf("inspect service %q runtime before network mutation: %w", name, err)
	}
	runtimeState, err := captureRegularNetworkRuntimeIntent(ctx, sv, name)
	if err != nil {
		return nil, err
	}
	enablement, err := captureRegularNetworkUnitEnablement(ctx, sv, name)
	if err != nil {
		return nil, err
	}
	return &serviceNetworkMutationPlan{
		name: name, previous: sv.AsStruct(), currentDesired: current, desired: desired,
		network:         networkOptsFromDesired(desired, flags.TsAuthKey),
		noOp:            reflect.DeepEqual(current, desired) && !flags.TsAuthKeySet && !flags.RunAsSet,
		previousRunning: running, previousRuntime: runtimeState, previousEnablement: enablement,
	}, nil
}

func (s *Server) regularNetworkMutationService(name string) (db.ServiceView, error) {
	sv, err := s.serviceView(name)
	if errors.Is(err, errServiceNotFound) {
		return db.ServiceView{}, fmt.Errorf("service %q not found", name)
	}
	if err != nil {
		return db.ServiceView{}, err
	}
	if sv.ServiceType() == db.ServiceTypeVM {
		return db.ServiceView{}, fmt.Errorf("service %q is a VM; use yeet vm set for VM networking", name)
	}
	if sv.ServiceType() != db.ServiceTypeSystemd && sv.ServiceType() != db.ServiceTypeDockerCompose {
		return db.ServiceView{}, fmt.Errorf("service %q has unsupported type %q for network mutation", name, sv.ServiceType())
	}
	return sv, nil
}

func desiredRegularNetworkMutation(sv db.ServiceView, flags cli.ServiceSetFlags) (db.ServiceNetworkConfig, db.ServiceNetworkConfig, error) {
	current, err := normalizeServiceNetworkConfig(desiredServiceNetworkConfig(sv))
	if err != nil {
		return db.ServiceNetworkConfig{}, db.ServiceNetworkConfig{}, fmt.Errorf("normalize current desired service network: %w", err)
	}
	desired, err := applyServiceNetworkPatch(current, flags)
	if err != nil {
		return db.ServiceNetworkConfig{}, db.ServiceNetworkConfig{}, err
	}
	payload := networkPayloadKind(sv.ServiceType())
	if sv.ServiceType() == db.ServiceTypeSystemd && hasTimerArtifact(sv) {
		payload = iso.PayloadCron
	}
	service := sv.AsStruct()
	if err := iso.ValidateNetwork(iso.NetworkRequest{
		Payload: payload, Modes: slices.Clone(desired.Modes), Published: service != nil && len(service.Publish) != 0,
	}); err != nil {
		return db.ServiceNetworkConfig{}, db.ServiceNetworkConfig{}, err
	}
	return current, desired, nil
}

func captureRegularNetworkRuntimeIntent(ctx context.Context, sv db.ServiceView, name string) ([]serviceIdentityRuntimeUnitState, error) {
	if sv.ServiceType() != db.ServiceTypeSystemd {
		return nil, nil
	}
	state, err := captureServiceIdentityRuntimeState(ctx, sv.AsStruct(), name)
	if err != nil {
		return nil, fmt.Errorf("capture service %q runtime intent: %w", name, err)
	}
	return state, nil
}

func captureRegularNetworkUnitEnablement(ctx context.Context, sv db.ServiceView, name string) ([]serviceIdentityUnitEnablement, error) {
	if sv.ServiceType() != db.ServiceTypeSystemd {
		return nil, nil
	}
	units := serviceIdentityEnabledUnits(sv.AsStruct(), name)
	states := make([]serviceIdentityUnitEnablement, 0, len(units))
	for _, unit := range units {
		enabled, err := inspectRegularNetworkUnitEnabled(ctx, unit)
		if err != nil {
			return nil, fmt.Errorf("inspect enablement for %s: %w", unit, err)
		}
		states = append(states, serviceIdentityUnitEnablement{Unit: unit, Enabled: enabled})
	}
	return states, nil
}

func regularNetworkAllocations(previous *db.Service, desired db.ServiceNetworkConfig, dv db.DataView) (*db.SvcNetwork, *db.MacvlanNetwork, *db.TailscaleNetwork, error) {
	svcNet, err := regularSvcNetworkAllocation(previous, desired, dv)
	if err != nil {
		return nil, nil, nil, err
	}
	macvlan, err := regularMacvlanNetworkAllocation(previous, desired)
	if err != nil {
		return nil, nil, nil, err
	}
	return svcNet, macvlan, regularTailscaleNetworkAllocation(previous, desired), nil
}

func regularSvcNetworkAllocation(previous *db.Service, desired db.ServiceNetworkConfig, dv db.DataView) (*db.SvcNetwork, error) {
	if !slices.Contains(desired.Modes, "svc") {
		return nil, nil
	}
	if previous != nil && previous.SvcNetwork != nil && previous.SvcNetwork.IPv4.IsValid() {
		clone := *previous.SvcNetwork
		return &clone, nil
	}
	return svcNetworkFromData(dv)
}

func regularMacvlanNetworkAllocation(previous *db.Service, desired db.ServiceNetworkConfig) (*db.MacvlanNetwork, error) {
	if !slices.Contains(desired.Modes, "lan") {
		return nil, nil
	}
	opts := MacvlanOpts{Parent: desired.MacvlanParent, VLAN: desired.MacvlanVLAN, Mac: desired.MacvlanMAC}
	if previous != nil && previous.Macvlan != nil && opts.Parent == "" {
		opts.Parent = previous.Macvlan.Parent
	}
	macvlan, err := macvlanNetworkFromOpts(opts)
	if err != nil || previous == nil {
		return macvlan, err
	}
	candidate := (&db.Data{Services: map[string]*db.Service{previous.Name: previous}}).View()
	if existing, ok := reusableExistingMacvlan(candidate, previous.Name, macvlan, opts); ok {
		return existing, nil
	}
	return macvlan, nil
}

func regularTailscaleNetworkAllocation(previous *db.Service, desired db.ServiceNetworkConfig) *db.TailscaleNetwork {
	if !slices.Contains(desired.Modes, "ts") {
		return nil
	}
	tsNet, _ := tailscaleNetworkFromOpts(TailscaleOpts{
		Version: desired.TSVersion, ExitNode: desired.TSExitNode, Tags: slices.Clone(desired.TSTags),
	})
	if previous != nil && previous.TSNet != nil && strings.TrimSpace(previous.TSNet.Interface) != "" {
		tsNet.Interface = previous.TSNet.Interface
		tsNet.StableID = previous.TSNet.StableID
	}
	return tsNet
}

func regularNetworkReplacement(previous *db.Service, desired db.ServiceNetworkConfig, svcNet *db.SvcNetwork, macvlan *db.MacvlanNetwork, tsNet *db.TailscaleNetwork, generated map[db.ArtifactName]string) *db.Service {
	target := previous.Clone()
	target.Network = desired.Clone()
	target.SvcNetwork = svcNet
	target.Macvlan = macvlan
	target.TSNet = tsNet
	target.ISO = nil
	for name := range isoNetworkArtifactNames {
		delete(target.Artifacts, name)
	}
	if target.Artifacts == nil && len(generated) != 0 {
		target.Artifacts = db.ArtifactStore{}
	}
	for name, path := range generated {
		artifact := target.Artifacts[name]
		if artifact == nil {
			artifact = &db.Artifact{Refs: map[db.ArtifactRef]string{}}
			target.Artifacts[name] = artifact
		}
		if artifact.Refs == nil {
			artifact.Refs = map[db.ArtifactRef]string{}
		}
		artifact.Refs[db.Gen(target.Generation)] = path
		artifact.Refs["latest"] = path
	}
	return target
}

func stopRegularNetworkMutationUnitsFailClosed(ctx context.Context, current, previous *db.Service, fallback string) error {
	units := uniqueServiceIdentityStopUnits([]*db.Service{current, previous}, fallback)
	var errs []error
	for _, unit := range units {
		if _, err := runRegularNetworkSystemctlForRuntime(ctx, "stop", unit); err != nil {
			errs = append(errs, fmt.Errorf("fail-closed stop %s: %w", unit, err))
		}
	}
	for _, unit := range units {
		active, err := inspectRegularNetworkUnitActive(ctx, unit)
		if err != nil {
			errs = append(errs, fmt.Errorf("fail-closed verify %s: %w", unit, err))
			continue
		}
		if active {
			errs = append(errs, fmt.Errorf("unit %s is still active after fail-closed stop", unit))
		}
	}
	return errors.Join(errs...)
}

func runRegularNetworkSystemctl(ctx context.Context, args ...string) error {
	output, err := runRegularNetworkSystemctlForRuntime(ctx, args...)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func stopRegularNetworkUnits(ctx context.Context, units []string, unconditionally bool) error {
	for _, unit := range units {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !unconditionally {
			active, err := inspectRegularNetworkUnitActive(ctx, unit)
			if err != nil {
				return err
			}
			if !active {
				continue
			}
		}
		if err := runRegularNetworkSystemctl(ctx, "stop", unit); err != nil {
			return fmt.Errorf("stop %s: %w", unit, err)
		}
		active, err := inspectRegularNetworkUnitActive(ctx, unit)
		if err != nil {
			return err
		}
		if active {
			return fmt.Errorf("unit %s is still active after stop", unit)
		}
	}
	return nil
}

func regularNetworkAuxiliaryUnits(record *db.Service) []string {
	if record == nil {
		return nil
	}
	_, ordered := serviceIdentityRuntimeUnits(record, record.Name)
	primary := serviceIdentityPrimaryRuntimeUnit(record, record.Name)
	auxiliary := make([]string, 0, len(ordered))
	for _, unit := range ordered {
		if unit != primary {
			auxiliary = append(auxiliary, unit)
		}
	}
	return auxiliary
}

func startRegularNetworkAuxiliaryUnits(ctx context.Context, record *db.Service, systemd *svc.SystemdService) error {
	units := regularNetworkAuxiliaryUnits(record)
	for _, unit := range units {
		if err := runRegularNetworkSystemctl(ctx, "start", unit); err != nil {
			return fmt.Errorf("start %s: %w", unit, err)
		}
		active, err := inspectRegularNetworkUnitActive(ctx, unit)
		if err != nil {
			return err
		}
		if !active {
			return fmt.Errorf("unit %s is not active after start", unit)
		}
	}
	tsUnit := tailscaleServiceIdentityUnit(record, record.Name)
	if serviceIdentityUnitIncluded(units, tsUnit) {
		return verifyTailscaleSystemdSidecar(ctx, systemd)
	}
	return nil
}

func regularNetworkTargetRuntimeIntent(plan *serviceNetworkMutationPlan, target *db.Service) []serviceIdentityRuntimeUnitState {
	previous := make(map[string]bool, len(plan.previousRuntime))
	workloadActive := plan.previousRunning
	for _, state := range plan.previousRuntime {
		previous[state.Unit] = state.Active
		if state.Unit == plan.name+".service" || state.Unit == plan.name+".timer" {
			workloadActive = workloadActive || state.Active
		}
	}
	previousPrimary := serviceIdentityPrimaryRuntimeUnit(plan.previous, plan.name)
	targetPrimary := serviceIdentityPrimaryRuntimeUnit(target, plan.name)
	stop, _ := serviceIdentityRuntimeUnits(target, plan.name)
	desired := make([]serviceIdentityRuntimeUnitState, 0, len(stop))
	for _, unit := range stop {
		active, existed := previous[unit]
		if !existed && unit == targetPrimary {
			active, existed = previous[previousPrimary]
		}
		if !existed && regularNetworkRuntimeAuxiliaryUnit(target, unit) {
			active = workloadActive
		}
		desired = append(desired, serviceIdentityRuntimeUnitState{Unit: unit, Active: active})
	}
	return desired
}

func regularNetworkRuntimeAuxiliaryUnit(record *db.Service, unit string) bool {
	return serviceIdentityUnitIncluded(regularNetworkAuxiliaryUnits(record), unit)
}

func reconcileRegularNetworkSystemdRuntime(ctx context.Context, service *db.Service, systemd *svc.SystemdService, desired []serviceIdentityRuntimeUnitState) error {
	stop, start := serviceIdentityRuntimeUnits(service, service.Name)
	want, err := validateRegularNetworkRuntimeIntent(stop, desired)
	if err != nil {
		return err
	}
	if err := stopUnexpectedRegularNetworkUnits(ctx, stop, want); err != nil {
		return err
	}
	if err := startRegularNetworkDependencies(ctx, service, systemd, want); err != nil {
		return err
	}
	if err := startRegularNetworkWorkloadUnits(ctx, service, start, stop, want); err != nil {
		return err
	}
	return verifyExactRegularNetworkRuntime(ctx, stop, want)
}

func validateRegularNetworkRuntimeIntent(units []string, desired []serviceIdentityRuntimeUnitState) (map[string]bool, error) {
	want := make(map[string]bool, len(desired))
	for _, state := range desired {
		if _, duplicate := want[state.Unit]; duplicate {
			return nil, fmt.Errorf("duplicate runtime intent for %s", state.Unit)
		}
		want[state.Unit] = state.Active
	}
	if len(want) != len(units) {
		return nil, fmt.Errorf("runtime intent has %d units, want %d", len(want), len(units))
	}
	for _, unit := range units {
		if _, ok := want[unit]; !ok {
			return nil, fmt.Errorf("runtime intent is missing %s", unit)
		}
	}
	return want, nil
}

func stopUnexpectedRegularNetworkUnits(ctx context.Context, units []string, want map[string]bool) error {
	for _, unit := range units {
		active, err := inspectRegularNetworkUnitActive(ctx, unit)
		if err != nil {
			return err
		}
		if active && !want[unit] {
			if err := runRegularNetworkSystemctl(ctx, "stop", unit); err != nil {
				return fmt.Errorf("restore stopped state for %s: %w", unit, err)
			}
		}
	}
	return nil
}

func startRegularNetworkDependencies(ctx context.Context, service *db.Service, systemd *svc.SystemdService, want map[string]bool) error {
	for _, unit := range regularNetworkAuxiliaryUnits(service) {
		if !want[unit] {
			continue
		}
		if err := startAndVerifyRegularNetworkUnit(ctx, unit, tailscaleServiceIdentityUnit(service, service.Name), systemd); err != nil {
			return err
		}
	}
	tsUnit := tailscaleServiceIdentityUnit(service, service.Name)
	if want[tsUnit] {
		return verifyTailscaleSystemdSidecar(ctx, systemd)
	}
	if regularNetworkWorkloadActive(service, want) && serviceIdentityUnitIncluded(regularNetworkAuxiliaryUnits(service), tsUnit) {
		return fmt.Errorf("cannot start Tailscale-backed workload while %s is inactive", tsUnit)
	}
	return nil
}

func startRegularNetworkWorkloadUnits(ctx context.Context, service *db.Service, start, stop []string, want map[string]bool) error {
	auxiliary := make(map[string]bool)
	for _, unit := range regularNetworkAuxiliaryUnits(service) {
		auxiliary[unit] = true
	}
	seen := make(map[string]bool)
	for _, units := range [][]string{start, stop} {
		for _, unit := range units {
			if auxiliary[unit] || seen[unit] || !want[unit] {
				continue
			}
			seen[unit] = true
			if err := startAndVerifyRegularNetworkUnit(ctx, unit, "", nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func regularNetworkWorkloadActive(service *db.Service, want map[string]bool) bool {
	return want[service.Name+".service"] || want[service.Name+".timer"]
}

func startAndVerifyRegularNetworkUnit(ctx context.Context, unit, tsUnit string, systemd *svc.SystemdService) error {
	active, err := inspectRegularNetworkUnitActive(ctx, unit)
	if err != nil {
		return err
	}
	if !active {
		if err := startRegularNetworkSystemdUnit(ctx, unit, tsUnit, systemd); err != nil {
			return err
		}
	}
	active, err = inspectRegularNetworkUnitActive(ctx, unit)
	if err != nil {
		return err
	}
	if !active {
		return fmt.Errorf("unit %s is not active after start", unit)
	}
	return nil
}

func captureRegularNetworkRuntimeState(ctx context.Context, units []string) ([]serviceIdentityRuntimeUnitState, error) {
	actual := make([]serviceIdentityRuntimeUnitState, 0, len(units))
	for _, unit := range units {
		active, err := inspectRegularNetworkUnitActive(ctx, unit)
		if err != nil {
			return nil, err
		}
		actual = append(actual, serviceIdentityRuntimeUnitState{Unit: unit, Active: active})
	}
	return actual, nil
}

func verifyExactRegularNetworkRuntime(ctx context.Context, units []string, want map[string]bool) error {
	actual, err := captureRegularNetworkRuntimeState(ctx, units)
	if err != nil {
		return err
	}
	for _, state := range actual {
		if state.Active != want[state.Unit] {
			return fmt.Errorf("unit %s active state is %t, want %t", state.Unit, state.Active, want[state.Unit])
		}
	}
	return nil
}

func (s *Server) stageRegularServiceNetworkReplacement(ctx context.Context, plan *serviceNetworkMutationPlan) (target *db.Service, retErr error) {
	if plan == nil || plan.previous == nil {
		return nil, errors.New("stage service network replacement without a plan")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root := s.serviceRootFromView(plan.previous.View())
	artifactTxn, err := beginRegularNetworkArtifactTransaction(root, plan.previous)
	if err != nil {
		return nil, fmt.Errorf("begin service network artifact transaction: %w", err)
	}
	plan.artifactTxn = artifactTxn
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, artifactTxn.rollback(s))
		}
	}()
	svcNet, macvlan, tsNet, err := s.allocateRegularServiceNetwork(plan)
	if err != nil {
		return nil, err
	}
	fi := s.newRegularNetworkFileInstaller(plan, svcNet, macvlan, tsNet)
	runtimeConfig, stageErr := stageRegularNetworkArtifacts(fi, plan)
	if stageErr != nil {
		return nil, stageErr
	}
	target = regularNetworkReplacement(plan.previous, plan.desired, svcNet, macvlan, tsNet, fi.artifacts)
	if err := stageRegularNetworkSystemdReplacement(ctx, plan, target, root, runtimeConfig, artifactTxn); err != nil {
		return nil, err
	}
	return target, nil
}

func stageRegularNetworkSystemdReplacement(
	ctx context.Context,
	plan *serviceNetworkMutationPlan,
	target *db.Service,
	root string,
	runtimeConfig *networkConfig,
	artifactTxn *regularNetworkArtifactTransaction,
) error {
	if plan.previous.ServiceType != db.ServiceTypeSystemd {
		return nil
	}
	runtimeConfig, err := regularNetworkTargetRuntimeConfig(target, runtimeConfig)
	if err != nil {
		return err
	}
	unit, err := stageOwnedRegularNetworkSystemdUnit(
		ctx, plan.previous, target, root, runtimeConfig, artifactTxn, !plan.deferSandbox,
	)
	if err != nil {
		return err
	}
	setRegularNetworkTargetArtifact(target, db.ArtifactSystemdUnit, unit)
	return nil
}

func regularNetworkTargetRuntimeConfig(target *db.Service, network *networkConfig) (*networkConfig, error) {
	runtime := &networkConfig{}
	if network != nil {
		*runtime = *network
		runtime.Deps = slices.Clone(network.Deps)
	}
	resolver, err := exactServiceSandboxResolver(target, target.Generation)
	if err != nil {
		return nil, err
	}
	runtime.ResolvConf = resolver
	return runtime, nil
}

func setRegularNetworkTargetArtifact(target *db.Service, name db.ArtifactName, path string) {
	if target.Artifacts == nil {
		target.Artifacts = db.ArtifactStore{}
	}
	artifact := target.Artifacts[name]
	if artifact == nil {
		artifact = &db.Artifact{Refs: map[db.ArtifactRef]string{}}
		target.Artifacts[name] = artifact
	}
	if artifact.Refs == nil {
		artifact.Refs = map[db.ArtifactRef]string{}
	}
	artifact.Refs[db.Gen(target.Generation)] = path
	artifact.Refs[db.ArtifactRef("latest")] = path
}

func (s *Server) stageISOServiceNetworkReplacement(ctx context.Context, plan *serviceNetworkMutationPlan) (target, staged *db.Service, retErr error) {
	if plan == nil || plan.previous == nil {
		return nil, nil, errors.New("stage ISO service network replacement without a plan")
	}
	switch plan.previous.ServiceType {
	case db.ServiceTypeSystemd:
		return s.stageNativeISOServiceNetworkReplacement(ctx, plan)
	case db.ServiceTypeDockerCompose:
		return s.stageComposeISOServiceNetworkReplacement(ctx, plan)
	default:
		return nil, nil, fmt.Errorf("stage ISO network for service type %q", plan.previous.ServiceType)
	}
}

func (s *Server) stageNativeISOServiceNetworkReplacement(ctx context.Context, plan *serviceNetworkMutationPlan) (target, staged *db.Service, retErr error) {
	stage, err := newISONativeNetworkStage(ctx, s, plan)
	if err != nil {
		return nil, nil, err
	}
	defer stage.rollbackOnError(&retErr)
	if err := stage.reserve(ctx); err != nil {
		return nil, nil, err
	}
	if err := stage.renderArtifacts(); err != nil {
		return nil, nil, err
	}
	return stage.target(), stage.staged, nil
}

func (s *Server) planNativeISOServiceNetworkReplacement(
	ctx context.Context,
	plan *serviceNetworkMutationPlan,
) (target, staged *db.Service, plannedData *db.Data, retErr error) {
	stage, err := newISONativeNetworkStage(ctx, s, plan)
	if err != nil {
		return nil, nil, nil, err
	}
	defer stage.rollbackOnError(&retErr)
	plannedData, err = stage.planReservation(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := stage.renderArtifacts(); err != nil {
		return nil, nil, nil, err
	}
	return stage.target(), stage.staged, plannedData, nil
}

type isoNativeNetworkStage struct {
	server     *Server
	ctx        context.Context
	plan       *serviceNetworkMutationPlan
	root       string
	txn        *regularNetworkArtifactTransaction
	reserved   bool
	allocation *db.ISOAllocation
	staged     *db.Service
	artifacts  map[db.ArtifactName]string
}

func newISONativeNetworkStage(ctx context.Context, server *Server, plan *serviceNetworkMutationPlan) (*isoNativeNetworkStage, error) {
	if plan == nil || plan.previous == nil {
		return nil, errors.New("stage ISO service network replacement without a plan")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root := server.serviceRootFromView(plan.previous.View())
	txn, err := beginRegularNetworkArtifactTransaction(root, plan.previous)
	if err != nil {
		return nil, fmt.Errorf("begin ISO service network artifact transaction: %w", err)
	}
	plan.artifactTxn = txn
	return &isoNativeNetworkStage{server: server, ctx: ctx, plan: plan, root: root, txn: txn}, nil
}

func (s *isoNativeNetworkStage) rollbackOnError(retErr *error) {
	if *retErr == nil {
		return
	}
	if !s.reserved {
		*retErr = errors.Join(*retErr, s.txn.rollback(s.server))
		return
	}
	restoreErr := restoreISOStageReservation(s.server, s.plan, s.staged, *retErr)
	*retErr = errors.Join(*retErr, restoreErr)
	if restoreErr == nil {
		*retErr = errors.Join(*retErr, s.txn.rollback(s.server))
	}
}

func (s *isoNativeNetworkStage) reserve(ctx context.Context) error {
	allocation, staged, err := s.server.reserveISOAllocationExact(ctx, s.plan.name, isoReservationRequest{
		Kind: iso.PayloadNative, Modes: slices.Clone(s.plan.desired.Modes),
	})
	if err != nil {
		return fmt.Errorf("reserve native ISO allocation: %w", err)
	}
	s.allocation = allocation
	s.staged = staged
	s.reserved = true
	return nil
}

func (s *isoNativeNetworkStage) planReservation(ctx context.Context) (*db.Data, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.server.ensureISOPool(ctx); err != nil {
		return nil, err
	}
	dv, err := s.server.cfg.DB.Get()
	if err != nil {
		return nil, err
	}
	data := dv.AsStruct()
	current := data.Services[s.plan.name]
	if !serviceNetworkRecordsEqual(current, s.plan.previous) {
		return nil, fmt.Errorf("service %q changed while planning ISO allocation", s.plan.name)
	}
	staged := current.Clone()
	allocation, err := reserveISOAllocationInData(s.plan.name, isoReservationRequest{
		Kind: iso.PayloadNative, Modes: slices.Clone(s.plan.desired.Modes),
	}, data, staged)
	if err != nil {
		return nil, err
	}
	s.allocation = allocation
	s.staged = staged
	data.Services[s.plan.name] = staged.Clone()
	return data, nil
}

func (s *isoNativeNetworkStage) renderArtifacts() error {
	s.artifacts = map[db.ArtifactName]string{}
	resolver, err := writeOwnedRegularNetworkArtifact(s.txn, db.ArtifactNetNSResolv, s.root, "bin", "iso-resolv-", ".conf", []byte("nameserver "+s.allocation.HostIP.String()+"\n"), 0o644)
	if err != nil {
		return fmt.Errorf("stage native ISO resolver: %w", err)
	}
	s.artifacts[db.ArtifactNetNSResolv] = resolver
	gate, err := stageFreshISOServiceNetworkGate(s.server, s.root, s.plan.name, s.txn)
	if err != nil {
		return err
	}
	s.artifacts[db.ArtifactNetNSService] = gate
	target := s.target()
	runtimeConfig, err := regularNetworkTargetRuntimeConfig(target, &networkConfig{
		NetNS: s.allocation.NetNS, Deps: []string{"yeet-" + s.plan.name + "-ns.service"}, ResolvConf: resolver,
	})
	if err != nil {
		return err
	}
	unit, err := stageOwnedRegularNetworkSystemdUnit(s.ctx, s.plan.previous, target, s.root, runtimeConfig, s.txn, !s.plan.deferSandbox)
	if err != nil {
		return err
	}
	s.artifacts[db.ArtifactSystemdUnit] = unit
	return nil
}

func (s *isoNativeNetworkStage) target() *db.Service {
	target := s.plan.previous.Clone()
	target.Network = s.plan.desired.Clone()
	target.ISO = s.allocation.Clone()
	target.SvcNetwork = nil
	target.Macvlan = nil
	target.TSNet = nil
	for name := range isoNetworkArtifactNames {
		delete(target.Artifacts, name)
	}
	for name, path := range s.artifacts {
		target.Artifacts[name] = &db.Artifact{Refs: map[db.ArtifactRef]string{
			db.Gen(target.Generation): path,
			"latest":                  path,
		}}
	}
	return target
}

var resolveISOComposeForNetworkMutation = svc.ResolveComposeJSON

func (s *Server) stageComposeISOServiceNetworkReplacement(ctx context.Context, plan *serviceNetworkMutationPlan) (target, staged *db.Service, retErr error) {
	stage, err := newISOComposeNetworkStage(ctx, s, plan)
	if err != nil {
		return nil, nil, err
	}
	defer stage.rollbackOnError(&retErr)
	if err := stage.resolveBaseAndReserve(ctx); err != nil {
		return nil, nil, err
	}
	if err := stage.renderAndAdmitOverlay(ctx); err != nil {
		return nil, nil, err
	}
	if err := stage.stageTailscaleArtifacts(); err != nil {
		return nil, nil, err
	}
	return stage.target(), stage.staged, nil
}

type isoComposeNetworkStage struct {
	server     *Server
	plan       *serviceNetworkMutationPlan
	root       string
	txn        *regularNetworkArtifactTransaction
	reserved   bool
	base       string
	options    svc.ComposeResolveOptions
	model      ISOComposeModel
	allocation *db.ISOAllocation
	staged     *db.Service
	artifacts  map[db.ArtifactName]string
	tailscale  *db.TailscaleNetwork
}

func newISOComposeNetworkStage(ctx context.Context, server *Server, plan *serviceNetworkMutationPlan) (*isoComposeNetworkStage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root := server.serviceRootFromView(plan.previous.View())
	txn, err := beginRegularNetworkArtifactTransaction(root, plan.previous)
	if err != nil {
		return nil, fmt.Errorf("begin ISO service network artifact transaction: %w", err)
	}
	plan.artifactTxn = txn
	return &isoComposeNetworkStage{server: server, plan: plan, root: root, txn: txn}, nil
}

func (s *isoComposeNetworkStage) rollbackOnError(retErr *error) {
	if *retErr == nil {
		return
	}
	if !s.reserved {
		*retErr = errors.Join(*retErr, s.txn.rollback(s.server))
		return
	}
	restoreErr := restoreISOStageReservation(s.server, s.plan, s.staged, *retErr)
	*retErr = errors.Join(*retErr, restoreErr)
	if restoreErr == nil {
		*retErr = errors.Join(*retErr, s.txn.rollback(s.server))
	}
}

func restoreISOStageReservation(server *Server, plan *serviceNetworkMutationPlan, staged *db.Service, cause error) error {
	if staged == nil {
		return fmt.Errorf("service %q cannot roll back ISO reservation without an exact reserved record", plan.name)
	}
	_, restoreErr := server.cfg.DB.MutateData(func(data *db.Data) error {
		current := data.Services[plan.name]
		if !serviceNetworkRecordsEqual(current, staged) {
			return fmt.Errorf("service %q changed while rolling back ISO reservation", plan.name)
		}
		data.Services[plan.name] = plan.previous.Clone()
		return nil
	})
	if restoreErr == nil {
		return nil
	}
	tombstoneErr := server.markISOStateExact(plan.name, staged, string(iso.StateTombstoned), errors.Join(cause, restoreErr), "rolling back ISO reservation")
	return errors.Join(restoreErr, tombstoneErr)
}

func (s *isoComposeNetworkStage) resolveBaseAndReserve(ctx context.Context) error {
	base, ok := s.plan.previous.Artifacts.Gen(db.ArtifactDockerComposeFile, s.plan.previous.Generation)
	if !ok {
		return fmt.Errorf("service %q generation %d has no Docker Compose artifact", s.plan.name, s.plan.previous.Generation)
	}
	s.base = base
	s.options = svc.ComposeResolveOptions{
		ProjectName: svc.ComposeProjectName(s.plan.name), ProjectDir: serviceDataDirForRoot(s.root), Files: []string{base},
	}
	baseJSON, err := resolveISOComposeForNetworkMutation(ctx, s.options)
	if err != nil {
		return fmt.Errorf("resolve base ISO Compose model: %w", err)
	}
	s.model, err = AdmitISOCompose(baseJSON, ISOComposeAdmissionOptions{
		ServiceRoot: s.root, ProjectName: svc.ComposeProjectName(s.plan.name), MaxComponents: iso.MaxComponents,
	})
	if err != nil {
		return fmt.Errorf("admit base ISO Compose model: %w", err)
	}
	s.allocation, s.staged, err = s.server.reserveISOAllocationExact(ctx, s.plan.name, isoReservationRequest{
		Kind: iso.PayloadCompose, Modes: slices.Clone(s.plan.desired.Modes), Components: slices.Clone(s.model.Components),
	})
	if err != nil {
		return fmt.Errorf("reserve ISO Compose allocation: %w", err)
	}
	s.reserved = true
	return nil
}

func (s *isoComposeNetworkStage) renderAndAdmitOverlay(ctx context.Context) error {
	s.artifacts = map[db.ArtifactName]string{}
	overlay, err := renderISOComposeOverlay(s.allocation, s.model)
	if err != nil {
		return err
	}
	overlayPath, err := writeOwnedRegularNetworkArtifact(s.txn, db.ArtifactDockerComposeNetwork, s.root, "bin", "iso-compose-network-", ".yml", []byte(overlay), 0o644)
	if err != nil {
		return err
	}
	s.artifacts[db.ArtifactDockerComposeNetwork] = overlayPath
	s.options.Files = []string{s.base, overlayPath}
	mergedJSON, err := resolveISOComposeForNetworkMutation(ctx, s.options)
	if err != nil {
		return fmt.Errorf("resolve merged ISO Compose model: %w", err)
	}
	merged, err := AdmitISOCompose(mergedJSON, ISOComposeAdmissionOptions{
		ServiceRoot: s.root, ProjectName: svc.ComposeProjectName(s.plan.name), MaxComponents: iso.MaxComponents, RequireISOOverlay: s.allocation,
	})
	if err != nil {
		return fmt.Errorf("admit merged ISO Compose model: %w", err)
	}
	if !slices.Equal(s.model.Components, merged.Components) {
		return fmt.Errorf("ISO overlay changed Compose components: base %v, merged %v", s.model.Components, merged.Components)
	}
	gate, err := stageFreshISOServiceNetworkGate(s.server, s.root, s.plan.name, s.txn)
	if err != nil {
		return err
	}
	s.artifacts[db.ArtifactNetNSService] = gate
	return nil
}

func (s *isoComposeNetworkStage) stageTailscaleArtifacts() error {
	if !slices.Contains(s.plan.desired.Modes, "ts") {
		return nil
	}
	s.tailscale = regularTailscaleNetworkAllocation(s.plan.previous, s.plan.desired)
	installer := s.server.newRegularNetworkFileInstaller(s.plan, nil, nil, s.tailscale)
	installer.isoAllocation = s.allocation.Clone()
	runInNetNS, _, tapMode, err := installer.tailscaleNetNSMode(&netns.Service{ServiceName: s.plan.name})
	if err != nil {
		return err
	}
	if err := stageFreshRegularTailscaleArtifacts(installer, runInNetNS, tapMode); err != nil {
		return err
	}
	for name, path := range installer.artifacts {
		s.artifacts[name] = path
	}
	return nil
}

func (s *isoComposeNetworkStage) target() *db.Service {
	target := s.plan.previous.Clone()
	target.Network = s.plan.desired.Clone()
	target.ISO = s.allocation.Clone()
	target.SvcNetwork = nil
	target.Macvlan = nil
	target.TSNet = s.tailscale
	for name := range isoNetworkArtifactNames {
		delete(target.Artifacts, name)
	}
	for name, path := range s.artifacts {
		target.Artifacts[name] = &db.Artifact{Refs: map[db.ArtifactRef]string{
			db.Gen(target.Generation): path,
			"latest":                  path,
		}}
	}
	return target
}

func stageFreshISOServiceNetworkGate(server *Server, root, service string, txn *regularNetworkArtifactTransaction) (string, error) {
	catchBin, err := catchExecutablePath()
	if err != nil {
		return "", fmt.Errorf("resolve catch binary for ISO network gate: %w", err)
	}
	unit, err := newISONetworkGateUnit(catchBin, server.cfg.RootDir, service)
	if err != nil {
		return "", err
	}
	tempDir, err := os.MkdirTemp("", "yeet-network-iso-gate-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tempDir) }()
	files, err := unit.WriteOutUnitFiles(tempDir)
	if err != nil {
		return "", fmt.Errorf("render ISO network gate unit: %w", err)
	}
	source := files[db.ArtifactSystemdUnit]
	if source == "" {
		return "", errors.New("ISO network gate did not render a systemd unit")
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	return writeOwnedRegularNetworkArtifact(txn, db.ArtifactNetNSService, root, "bin", "iso-gate-", ".service", raw, 0o644)
}

func (s *Server) allocateRegularServiceNetwork(plan *serviceNetworkMutationPlan) (*db.SvcNetwork, *db.MacvlanNetwork, *db.TailscaleNetwork, error) {
	dv, err := s.cfg.DB.Get()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load service network allocations: %w", err)
	}
	svcNet, macvlan, tsNet, err := regularNetworkAllocations(plan.previous, plan.desired, dv)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("allocate service network: %w", err)
	}
	return svcNet, macvlan, tsNet, nil
}

func (s *Server) newRegularNetworkFileInstaller(plan *serviceNetworkMutationPlan, svcNet *db.SvcNetwork, macvlan *db.MacvlanNetwork, tsNet *db.TailscaleNetwork) *FileInstaller {
	root := s.serviceRootFromView(plan.previous.View())
	return &FileInstaller{
		s: s,
		cfg: FileInstallerCfg{
			InstallerCfg: InstallerCfg{ServiceName: plan.name, ServiceRoot: root},
			Network:      plan.network,
		},
		existingService: plan.previous.View(), serviceRoot: root,
		svcNet: svcNet, macvlan: macvlan, tsNet: tsNet,
		tsAuthKey: plan.network.Tailscale.AuthKey,
		artifacts: map[db.ArtifactName]string{}, networkArtifactTxn: plan.artifactTxn,
	}
}

func stageRegularNetworkArtifacts(fi *FileInstaller, plan *serviceNetworkMutationPlan) (*networkConfig, error) {
	if compose, ok := plan.previous.Artifacts.Gen(db.ArtifactDockerComposeFile, plan.previous.Generation); ok {
		fi.artifacts[db.ArtifactDockerComposeFile] = compose
	}
	if !networkInterfacesEnabled(plan.network.Interfaces) {
		return nil, nil
	}
	return stageRegularNetworkNamespaceArtifacts(fi, plan.previous.ServiceType)
}

func stageRegularNetworkNamespaceArtifacts(fi *FileInstaller, serviceType db.ServiceType) (*networkConfig, error) {
	env, runTSInNetNS, tsTapMode, err := prepareRegularNetworkNamespace(fi)
	if err != nil {
		return nil, err
	}
	if err := stageRegularNetworkBase(fi, &env); err != nil {
		return nil, err
	}
	deps, err := stageRegularTailscaleDependency(fi, env, runTSInNetNS, tsTapMode)
	if err != nil {
		return nil, err
	}
	if err := stageRegularComposeNetworkIfNeeded(fi, env, serviceType); err != nil {
		return nil, err
	}
	return &networkConfig{NetNS: env.NetNS(), Deps: deps, ResolvConf: runtimeNetNSResolvConf(env.NetNS())}, nil
}

func prepareRegularNetworkNamespace(fi *FileInstaller) (netns.Service, string, bool, error) {
	env := fi.netNSServiceEnv()
	runTSInNetNS, _, tsTapMode, err := fi.tailscaleNetNSMode(&env)
	return env, runTSInNetNS, tsTapMode, err
}

func stageRegularNetworkBase(fi *FileInstaller, env *netns.Service) error {
	if err := checkRegularServiceSubnet(fi.svcNet); err != nil {
		return err
	}
	_, tailscaleResolvConf, _, err := fi.tailscaleNetNSMode(env)
	if err != nil {
		return err
	}
	if resolvConf := netNSResolvConfFor(env, tailscaleResolvConf); resolvConf != "" {
		path, err := writeOwnedRegularNetworkArtifact(fi.networkArtifactTxn, db.ArtifactNetNSResolv, fi.effectiveServiceRoot(), "bin", "resolv-", ".conf", []byte(resolvConf), 0o644)
		if err != nil {
			return fmt.Errorf("stage resolv.conf: %w", err)
		}
		fi.artifacts[db.ArtifactNetNSResolv] = path
		env.ResolvConf = path
	}
	return stageFreshRegularNetNSArtifacts(fi, *env)
}

func stageFreshRegularNetNSArtifacts(fi *FileInstaller, env netns.Service) error {
	tempDir, err := os.MkdirTemp("", "yeet-network-netns-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tempDir) }()
	files, err := netns.WriteServiceNetNS(tempDir, fi.serviceRunDir(), env)
	if err != nil {
		return fmt.Errorf("render netns artifacts: %w", err)
	}
	for _, name := range []db.ArtifactName{db.ArtifactNetNSEnv, db.ArtifactNetNSService} {
		source, ok := files[name]
		if !ok {
			continue
		}
		raw, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		path, err := writeOwnedRegularNetworkArtifact(fi.networkArtifactTxn, name, fi.effectiveServiceRoot(), "bin", strings.ReplaceAll(string(name), ".", "-")+"-", filepath.Ext(source), raw, 0o644)
		if err != nil {
			return err
		}
		fi.artifacts[name] = path
	}
	return nil
}

func stageRegularTailscaleDependency(fi *FileInstaller, env netns.Service, runInNetNS string, tsTapMode bool) ([]string, error) {
	deps := []string{env.ServiceUnit()}
	if fi.tsNet == nil {
		return deps, nil
	}
	if err := stageFreshRegularTailscaleArtifacts(fi, runInNetNS, tsTapMode); err != nil {
		return nil, err
	}
	return append(deps, "yeet-"+fi.cfg.ServiceName+"-ts.service"), nil
}

func stageFreshRegularTailscaleArtifacts(fi *FileInstaller, runInNetNS string, tsTapMode bool) error {
	root := fi.effectiveServiceRoot()
	if err := ensureFreshRegularTailscaleDirectory(root); err != nil {
		return err
	}
	plan, unit, err := freshRegularTailscaleUnit(fi, root, runInNetNS, tsTapMode)
	if err != nil {
		return err
	}
	authKey, err := fi.s.resolveTailscaleAuthKey(fi.tsNet, fi.tsAuthKey)
	if err != nil {
		return err
	}
	tailscaled, err := fi.s.getTailscaledBinary(fi.tsNet.Version)
	if err != nil {
		return err
	}
	if err := renderFreshRegularTailscaleArtifacts(fi, root, plan, unit, authKey); err != nil {
		return err
	}
	fi.artifacts[db.ArtifactTSBinary] = tailscaled
	return nil
}

func ensureFreshRegularTailscaleDirectory(root string) error {
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	if err := rootHandle.MkdirAll("tailscale", 0o755); err != nil {
		_ = rootHandle.Close()
		return err
	}
	if err := rootHandle.Close(); err != nil {
		return err
	}
	return nil
}

func freshRegularTailscaleUnit(fi *FileInstaller, root, runInNetNS string, tsTapMode bool) (tailscaleInstallPlan, svc.SystemdUnit, error) {
	plan := tailscaleInstallPlan{
		service: fi.cfg.ServiceName, runDir: fi.serviceRunDir(), serviceTSDir: filepath.Join(root, "tailscale"),
		runInNetNS: runInNetNS, interfaceName: fi.tsNet.Interface,
	}
	if !tsTapMode && strings.TrimSpace(runInNetNS) != "" {
		plan.resolvConf = runtimeNetNSResolvConf(runInNetNS)
	}
	var unit svc.SystemdUnit
	var err error
	if runInNetNS != "" {
		unit, err = fi.s.newGuardedTailscaleSystemdUnit(plan)
	} else {
		unit, err = newTailscaleSystemdUnit(plan)
	}
	if err != nil {
		return tailscaleInstallPlan{}, svc.SystemdUnit{}, err
	}
	return plan, unit, nil
}

func renderFreshRegularTailscaleArtifacts(fi *FileInstaller, root string, plan tailscaleInstallPlan, unit svc.SystemdUnit, authKey string) error {
	tempDir, err := os.MkdirTemp("", "yeet-network-tailscale-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tempDir) }()
	envPath := filepath.Join(tempDir, "tailscaled.env")
	err = serviceenv.Write(envPath, &tsEnv{LogsDir: plan.serviceTSDir})
	if err != nil {
		return err
	}
	unitFiles, err := unit.WriteOutUnitFiles(tempDir)
	if err != nil {
		return err
	}
	configPath, err := writeTailscaleConfig(tempDir, fi.cfg.ServiceName, authKey, fi.tsNet.ExitNode)
	if err != nil {
		return err
	}
	for _, artifact := range []struct {
		name   db.ArtifactName
		source string
		mode   os.FileMode
	}{
		{name: db.ArtifactTSEnv, source: envPath, mode: 0o644},
		{name: db.ArtifactTSService, source: unitFiles[db.ArtifactSystemdUnit], mode: 0o644},
		{name: db.ArtifactTSConfig, source: configPath, mode: 0o600},
	} {
		raw, err := os.ReadFile(artifact.source)
		if err != nil {
			return err
		}
		path, err := writeOwnedRegularNetworkArtifact(fi.networkArtifactTxn, artifact.name, root, "bin", strings.ReplaceAll(string(artifact.name), ".", "-")+"-", filepath.Ext(artifact.source), raw, artifact.mode)
		if err != nil {
			return err
		}
		fi.artifacts[artifact.name] = path
	}
	return nil
}

func stageRegularComposeNetworkIfNeeded(fi *FileInstaller, env netns.Service, serviceType db.ServiceType) error {
	if serviceType != db.ServiceTypeDockerCompose {
		return nil
	}
	return stageRegularDockerComposeNetwork(fi, env)
}

func checkRegularServiceSubnet(network *db.SvcNetwork) error {
	if network == nil {
		return nil
	}
	return checkSvcSubnetAvailableFn()
}

func stageRegularDockerComposeNetwork(installer *FileInstaller, env netns.Service) error {
	services, err := installer.composeDNSOverlayServices(env)
	if err != nil {
		return err
	}
	raw, err := renderDockerComposeNetwork(env, services)
	if err != nil {
		return err
	}
	path, err := writeOwnedRegularNetworkArtifact(installer.networkArtifactTxn, db.ArtifactDockerComposeNetwork, installer.effectiveServiceRoot(), "bin", "compose-network-", ".yml", []byte(raw), 0o644)
	if err != nil {
		return fmt.Errorf("stage docker compose network: %w", err)
	}
	installer.artifacts[db.ArtifactDockerComposeNetwork] = path
	return nil
}

func writeFreshRegularNetworkArtifact(root, dir, prefix, suffix string, raw []byte, mode os.FileMode) (string, error) {
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer func() { _ = rootHandle.Close() }()
	if err := rootHandle.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for range 16 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		name := prefix + hex.EncodeToString(random[:]) + suffix
		rel := filepath.Join(dir, name)
		file, err := rootHandle.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := file.Write(raw); err != nil {
			_ = file.Close()
			_ = rootHandle.Remove(rel)
			return "", err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			_ = rootHandle.Remove(rel)
			return "", err
		}
		if err := file.Close(); err != nil {
			_ = rootHandle.Remove(rel)
			return "", err
		}
		return filepath.Join(root, rel), nil
	}
	return "", errors.New("exhausted fresh service network artifact names")
}

var afterOwnedRegularNetworkArtifact = func(db.ArtifactName, string) error { return nil }

func writeOwnedRegularNetworkArtifact(txn *regularNetworkArtifactTransaction, artifact db.ArtifactName, root, dir, prefix, suffix string, raw []byte, mode os.FileMode) (string, error) {
	path, err := writeFreshRegularNetworkArtifact(root, dir, prefix, suffix, raw, mode)
	if err != nil || txn == nil {
		return path, err
	}
	if err := txn.registerStagedPath(path); err != nil {
		proof, captureErr := txn.capture(path)
		cleanupErr := error(nil)
		if captureErr == nil {
			cleanupErr = txn.remove(proof)
		}
		return "", errors.Join(fmt.Errorf("register fresh %s artifact: %w", artifact, err), captureErr, cleanupErr)
	}
	if err := afterOwnedRegularNetworkArtifact(artifact, path); err != nil {
		return "", fmt.Errorf("after fresh %s artifact: %w", artifact, err)
	}
	return path, nil
}

func stageOwnedRegularNetworkSystemdUnit(ctx context.Context, previous, target *db.Service, root string, network *networkConfig, txn *regularNetworkArtifactTransaction, preflight bool) (string, error) {
	if previous == nil || target == nil {
		return "", errors.New("stage regular network unit without previous and target services")
	}
	source, ok := previous.Artifacts.Gen(db.ArtifactSystemdUnit, previous.Generation)
	if !ok {
		return "", fmt.Errorf("service %q generation %d has no systemd unit artifact", previous.Name, previous.Generation)
	}
	raw, err := readRegularNetworkArtifact(root, source)
	if err != nil {
		return "", fmt.Errorf("read current systemd unit: %w", err)
	}
	content, plan, err := renderRegularNetworkSystemdUnit(ctx, string(raw), previous, target, root, network, preflight)
	if err != nil {
		return "", err
	}
	path, err := writeOwnedRegularNetworkArtifact(txn, db.ArtifactSystemdUnit, root, "bin", previous.Name+"-network-", ".service", []byte(content), 0o644)
	if err != nil {
		return "", fmt.Errorf("write replacement systemd unit: %w", err)
	}
	if err := verifyOwnedRegularNetworkSystemdUnit(ctx, target, path, plan, preflight); err != nil {
		return "", err
	}
	return path, nil
}

func verifyOwnedRegularNetworkSystemdUnit(
	ctx context.Context,
	target *db.Service,
	path string,
	plan *serviceSandboxPlan,
	preflight bool,
) error {
	if !preflight {
		return nil
	}
	if plan != nil {
		identity := effectiveServiceIdentity(target.View()).Persisted
		if err := probeServiceSandboxForMutation(ctx, *plan, identity.UID, identity.GID); err != nil {
			return fmt.Errorf("probe service %q network sandbox: %w", target.Name, err)
		}
	}
	if err := verifyGeneratedSystemdUnitForSandboxMutation(ctx, path); err != nil {
		return fmt.Errorf("verify service %q network unit: %w", target.Name, err)
	}
	return nil
}

func renderRegularNetworkSystemdUnit(ctx context.Context, raw string, previous, target *db.Service, root string, network *networkConfig, preflight bool) (string, *serviceSandboxPlan, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil, errors.New("current systemd unit is empty")
	}
	if previous == nil || target == nil {
		return "", nil, errors.New("render regular network unit without previous and target services")
	}
	targetPolicy, err := serviceSandboxPolicyForExactGeneration(target, target.Generation)
	if err != nil {
		return "", nil, fmt.Errorf("load target network sandbox policy: %w", err)
	}
	renderer := regularNetworkUnitRenderer{
		network: network,
		networkUnits: map[string]bool{
			"yeet-" + previous.Name + "-ns.service": true,
			"yeet-" + previous.Name + "-ts.service": true,
		},
		removePrivateMounts: regularNetworkUnitHadResolver(previous.Artifacts),
		targetSandboxOn:     targetPolicy.State == "on",
	}
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		renderer.appendLine(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", nil, err
	}
	renderer.flushSection()
	if !renderer.seenUnit || !renderer.seenService {
		return "", nil, errors.New("current systemd unit must contain [Unit] and [Service] sections")
	}
	content := strings.Join(renderer.out, "\n") + "\n"
	return renderRegularNetworkSandboxTarget(ctx, content, previous, target, root, preflight)
}

func renderRegularNetworkSandboxTarget(ctx context.Context, content string, previous, target *db.Service, root string, preflight bool) (string, *serviceSandboxPlan, error) {
	targetPolicy, err := serviceSandboxPolicyForExactGeneration(target, target.Generation)
	if err != nil {
		return "", nil, fmt.Errorf("load target network sandbox policy: %w", err)
	}
	if targetPolicy.State == "legacy" || !preflight {
		return content, nil, nil
	}
	if err := serviceSandboxMutationContext(ctx); err != nil {
		return "", nil, err
	}
	currentPolicy, err := serviceSandboxPolicyForExactGeneration(previous, previous.Generation)
	if err != nil {
		return "", nil, fmt.Errorf("load current network sandbox policy: %w", err)
	}
	payload, resolver, err := exactReadableNetworkSandboxArtifacts(target)
	if err != nil {
		return "", nil, err
	}
	identity := effectiveServiceIdentity(target.View()).Persisted
	request := serviceSandboxPlanRequest{
		Service: target.Name, Policy: targetPolicy, Payload: payload, DataDir: serviceDataDirForRoot(root),
		ResolverSource: resolver, UID: identity.UID, GID: identity.GID, Hostname: target.Name,
	}
	validatedPolicy, plan, err := prepareServiceSandboxMutationTarget(ctx, target.Name, request, targetPolicy.State == "on")
	if err != nil {
		return "", nil, fmt.Errorf("preflight service %q network sandbox: %w", target.Name, err)
	}
	rendered, renderedPlan, err := renderNativeSandboxUnitWithPlan(content, nativeSandboxUnitRequest{
		CurrentPolicy: currentPolicy, TargetPolicy: validatedPolicy, Identity: identity,
		Payload: payload, DataDir: request.DataDir, Resolver: resolver, Hostname: target.Name,
	}, plan)
	if err != nil {
		return "", nil, fmt.Errorf("render service %q network sandbox: %w", target.Name, err)
	}
	return rendered, renderedPlan, nil
}

func exactReadableNetworkSandboxArtifacts(target *db.Service) (string, string, error) {
	payload, ok := target.Artifacts.Gen(db.ArtifactBinary, target.Generation)
	if !ok || strings.TrimSpace(payload) == "" {
		return "", "", fmt.Errorf("target native service %q generation %d has no binary artifact", target.Name, target.Generation)
	}
	resolver, err := exactServiceSandboxResolver(target, target.Generation)
	if err != nil {
		return "", "", err
	}
	artifacts := []struct {
		name string
		path string
	}{
		{name: "payload", path: payload},
		{name: "resolver", path: resolver},
	}
	for _, artifact := range artifacts {
		if err := validateReadableServiceSandboxArtifact(artifact.path); err != nil {
			return "", "", fmt.Errorf("validate target network sandbox %s %s: %w", artifact.name, artifact.path, err)
		}
	}
	return payload, resolver, nil
}

func exactServiceSandboxResolver(service *db.Service, generation int) (string, error) {
	if service == nil {
		return "", errors.New("sandbox resolver requires a service")
	}
	artifact, exists := service.Artifacts[db.ArtifactNetNSResolv]
	if !exists {
		return "/etc/resolv.conf", nil
	}
	if artifact == nil || artifact.Refs == nil {
		return "", fmt.Errorf("service %q has an invalid resolver artifact record", service.Name)
	}
	resolver, ok := artifact.Refs[db.Gen(generation)]
	if !ok || strings.TrimSpace(resolver) == "" {
		return "", fmt.Errorf("service %q generation %d has no exact resolver artifact", service.Name, generation)
	}
	return resolver, nil
}

type regularNetworkUnitRenderer struct {
	out                 []string
	section             string
	seenUnit            bool
	seenService         bool
	network             *networkConfig
	networkUnits        map[string]bool
	removePrivateMounts bool
	targetSandboxOn     bool
}

func regularNetworkUnitHadResolver(artifacts db.ArtifactStore) bool {
	artifact := artifacts[db.ArtifactNetNSResolv]
	return artifact != nil && len(artifact.Refs) != 0
}

func (r *regularNetworkUnitRenderer) appendLine(line string) {
	trimmed := strings.TrimSpace(line)
	if isRegularNetworkUnitSection(trimmed) {
		r.appendSection(line, trimmed)
		return
	}
	if rewritten, handled := r.rewriteUnitDependency(trimmed); handled {
		if rewritten != "" {
			r.out = append(r.out, rewritten)
		}
		return
	}
	if r.skipServiceDirective(trimmed) {
		return
	}
	r.out = append(r.out, line)
}

func isRegularNetworkUnitSection(line string) bool {
	return strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]")
}

func (r *regularNetworkUnitRenderer) appendSection(line, section string) {
	r.flushSection()
	r.section = section
	if section == "[Unit]" {
		r.seenUnit = true
	}
	if section == "[Service]" {
		r.seenService = true
	}
	r.out = append(r.out, line)
}

func (r *regularNetworkUnitRenderer) flushSection() {
	r.out = appendRegularNetworkSection(r.out, r.section, r.network, r.targetSandboxOn)
}

func (r *regularNetworkUnitRenderer) rewriteUnitDependency(line string) (string, bool) {
	if r.section != "[Unit]" || !regularNetworkDependencyDirective(line) {
		return "", false
	}
	key, value, _ := strings.Cut(line, "=")
	kept := removeRegularNetworkUnitTokens(strings.Fields(value), r.networkUnits)
	if len(kept) == 0 {
		return "", true
	}
	return key + "=" + strings.Join(kept, " "), true
}

func regularNetworkDependencyDirective(line string) bool {
	return strings.HasPrefix(line, "Requires=") || strings.HasPrefix(line, "After=")
}

func (r *regularNetworkUnitRenderer) skipServiceDirective(line string) bool {
	if r.section != "[Service]" {
		return false
	}
	if strings.HasPrefix(line, "NetworkNamespacePath=") {
		return true
	}
	if strings.HasPrefix(line, "BindReadOnlyPaths=") && strings.HasSuffix(line, ":/etc/resolv.conf") {
		return true
	}
	return r.removePrivateMounts && line == "PrivateMounts=yes"
}

func appendRegularNetworkSection(out []string, section string, network *networkConfig, targetSandboxOn bool) []string {
	if network == nil {
		return out
	}
	switch section {
	case "[Unit]":
		return appendRegularNetworkUnitDependencies(out, network.Deps)
	case "[Service]":
		return appendRegularNetworkServiceRuntime(out, network, targetSandboxOn)
	}
	return out
}

func appendRegularNetworkUnitDependencies(out, dependencies []string) []string {
	if len(dependencies) == 0 {
		return out
	}
	deps := strings.Join(dependencies, " ")
	return append(out, "Requires="+deps, "After="+deps)
}

func appendRegularNetworkServiceRuntime(out []string, network *networkConfig, targetSandboxOn bool) []string {
	if strings.TrimSpace(network.NetNS) != "" {
		out = append(out, "NetworkNamespacePath=/var/run/netns/"+network.NetNS)
	}
	if !targetSandboxOn && strings.TrimSpace(network.ResolvConf) != "" {
		out = append(out, "BindReadOnlyPaths="+network.ResolvConf+":/etc/resolv.conf", "PrivateMounts=yes")
	}
	return out
}

func removeRegularNetworkUnitTokens(tokens []string, remove map[string]bool) []string {
	out := tokens[:0]
	for _, token := range tokens {
		if !remove[token] {
			out = append(out, token)
		}
	}
	return out
}

type regularServiceNetworkMutation struct {
	server  *Server
	plan    *serviceNetworkMutationPlan
	target  *db.Service
	claimed *db.Service
}

func (m *regularServiceNetworkMutation) ResolverCanonicalTarget() *db.Service {
	return m.target
}

func (m *regularServiceNetworkMutation) DiscardStagedArtifacts() error {
	if m == nil || m.plan == nil || m.plan.artifactTxn == nil {
		return nil
	}
	return m.plan.artifactTxn.rollback(m.server)
}

func (m *regularServiceNetworkMutation) CleanupCommittedArtifacts(context.Context) error {
	if m == nil || m.plan == nil || m.plan.artifactTxn == nil {
		return nil
	}
	return m.plan.artifactTxn.cleanupCommitted(m.server)
}

func (m *regularServiceNetworkMutation) Stage(ctx context.Context) error {
	target, err := m.server.stageRegularServiceNetworkReplacement(ctx, m.plan)
	if err != nil {
		return err
	}
	m.target = target
	return nil
}

func (m *regularServiceNetworkMutation) StopPrevious(ctx context.Context) error {
	switch m.plan.previous.ServiceType {
	case db.ServiceTypeSystemd:
		return stopRegularNetworkUnits(ctx, uniqueServiceIdentityStopUnits([]*db.Service{m.plan.previous}, m.plan.name), false)
	case db.ServiceTypeDockerCompose:
		return m.stopCompose(ctx, m.plan.previous)
	default:
		return fmt.Errorf("network mutation for service type %q is not supported", m.plan.previous.ServiceType)
	}
}

func (m *regularServiceNetworkMutation) Activate(ctx context.Context) error {
	switch m.target.ServiceType {
	case db.ServiceTypeSystemd:
		return m.activateSystemd(ctx)
	case db.ServiceTypeDockerCompose:
		return m.activateCompose(ctx)
	default:
		return fmt.Errorf("network mutation for service type %q is not supported", m.target.ServiceType)
	}
}

func (m *regularServiceNetworkMutation) activateSystemd(ctx context.Context) error {
	service, err := m.installSystemdDefinition(ctx, m.target)
	if err != nil {
		return err
	}
	return reconcileRegularNetworkSystemdRuntime(ctx, m.target, service, regularNetworkTargetRuntimeIntent(m.plan, m.target))
}

func (m *regularServiceNetworkMutation) activateCompose(ctx context.Context) error {
	compose, err := m.composeService(m.target)
	if err != nil {
		return err
	}
	systemd, err := m.installComposeDefinition(ctx, m.target)
	if err != nil {
		return err
	}
	if err := m.disableStaleUnits(ctx, m.target); err != nil {
		return err
	}
	return startRegularNetworkComposeIfNeeded(ctx, compose, m.target, systemd, m.plan.previousRunning)
}

type regularNetworkComposeStarter interface {
	ReconcileNetNS(context.Context) (bool, error)
	UpDetached(context.Context, bool) error
}

func startRegularNetworkComposeIfNeeded(ctx context.Context, compose regularNetworkComposeStarter, record *db.Service, systemd *svc.SystemdService, running bool) error {
	if !running {
		return nil
	}
	if err := startRegularNetworkAuxiliaryUnits(ctx, record, systemd); err != nil {
		return err
	}
	if _, err := compose.ReconcileNetNS(ctx); err != nil {
		return err
	}
	return compose.UpDetached(ctx, false)
}

func (m *regularServiceNetworkMutation) Verify(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch m.target.ServiceType {
	case db.ServiceTypeSystemd:
		if err := verifyRegularNetworkSystemdRuntime(ctx, m.server, m.plan.previous, m.target, regularNetworkTargetRuntimeIntent(m.plan, m.target)); err != nil {
			return err
		}
		return m.verifyUnitEnablement(ctx, m.target)
	case db.ServiceTypeDockerCompose:
		return m.verifyCompose(ctx)
	default:
		return fmt.Errorf("network mutation for service type %q is not supported", m.target.ServiceType)
	}
}

func (m *regularServiceNetworkMutation) verifyCompose(ctx context.Context) error {
	if err := verifyRegularNetworkComposeRuntime(ctx, m.server, m.plan.previous, m.target, m.plan.previousRunning); err != nil {
		return err
	}
	compose, err := m.composeService(m.target)
	if err != nil {
		return err
	}
	running, err := compose.AnyRunningContext(ctx)
	if err != nil {
		return err
	}
	if running != m.plan.previousRunning {
		return fmt.Errorf("replacement running state is %t, want %t", running, m.plan.previousRunning)
	}
	return nil
}

func verifyRegularNetworkComposeRuntime(ctx context.Context, server *Server, previous, target *db.Service, wantRunning bool) error {
	units := regularNetworkAuxiliaryUnits(target)
	if err := verifyRegularNetworkTargetUnits(ctx, units, wantRunning); err != nil {
		return err
	}
	if err := verifyRegularNetworkStaleUnits(ctx, previous, target.Name, units); err != nil {
		return err
	}
	if !regularNetworkTailscaleNeedsVerification(target, units, wantRunning) {
		return nil
	}
	return verifyRegularNetworkTailscaleRuntime(ctx, server, target)
}

func (m *regularServiceNetworkMutation) Commit(context.Context) error {
	_, err := m.server.cfg.DB.MutateData(func(data *db.Data) error {
		current := data.Services[m.plan.name]
		if !serviceNetworkRecordsEqual(current, m.plan.previous) {
			return fmt.Errorf("service %q changed during network mutation", m.plan.name)
		}
		data.Services[m.plan.name] = m.target.Clone()
		return nil
	})
	if err != nil {
		return err
	}
	m.server.PublishEvent(Event{Type: EventTypeServiceConfigChanged, ServiceName: m.plan.name, Data: EventData{m.target.View()}})
	return nil
}

func (m *regularServiceNetworkMutation) Restore(ctx context.Context) error {
	owned, claimErr := m.claimReplacementBeforeRestore()
	if claimErr != nil && !owned {
		return newServiceNetworkRecoveryError(serviceNetworkRecoveryOwnershipUnowned, claimErr)
	}
	if stopErr := m.stopReplacementBeforeRestore(ctx); stopErr != nil {
		return newServiceNetworkRecoveryError(serviceNetworkRecoveryOwnershipOwned, errors.Join(claimErr, stopErr))
	}
	artifactErr := m.DiscardStagedArtifacts()
	var runtimeErr error
	if artifactErr == nil {
		runtimeErr = m.restorePreviousRuntime(ctx)
	}
	return newServiceNetworkRecoveryError(serviceNetworkRecoveryOwnershipOwned, errors.Join(claimErr, artifactErr, runtimeErr))
}

func (m *regularServiceNetworkMutation) stopReplacementBeforeRestore(ctx context.Context) error {
	if m.target == nil {
		return nil
	}
	if err := m.stopTarget(ctx); err != nil {
		return fmt.Errorf("stop replacement before restore: %w", err)
	}
	return nil
}

func (m *regularServiceNetworkMutation) claimReplacementBeforeRestore() (bool, error) {
	_, err := mutateServiceNetworkRestoreData(m.server.cfg.DB, func(data *db.Data) error {
		current := data.Services[m.plan.name]
		switch {
		case serviceNetworkRecordsEqual(current, m.plan.previous):
			return nil
		case serviceNetworkRecordsEqual(current, m.target):
			data.Services[m.plan.name] = m.plan.previous.Clone()
			return nil
		default:
			return fmt.Errorf("service %q changed while restoring network mutation", m.plan.name)
		}
	})
	if err != nil && !dbMutationCommitted(err) {
		return false, err
	}
	m.claimed = m.plan.previous.Clone()
	return true, err
}

func (m *regularServiceNetworkMutation) restorePreviousRuntime(ctx context.Context) error {
	switch m.plan.previous.ServiceType {
	case db.ServiceTypeSystemd:
		return m.restoreSystemd(ctx)
	case db.ServiceTypeDockerCompose:
		return m.restoreCompose(ctx)
	default:
		return fmt.Errorf("network mutation for service type %q is not supported", m.plan.previous.ServiceType)
	}
}

func (m *regularServiceNetworkMutation) restoreSystemd(ctx context.Context) error {
	systemd, err := m.installSystemdDefinition(ctx, m.plan.previous)
	if err != nil {
		return err
	}
	return reconcileRegularNetworkSystemdRuntime(ctx, m.plan.previous, systemd, m.plan.previousRuntime)
}

func (m *regularServiceNetworkMutation) restoreCompose(ctx context.Context) error {
	compose, err := m.composeService(m.plan.previous)
	if err != nil {
		return err
	}
	if err := compose.StopProjectContainers(ctx); err != nil {
		return err
	}
	systemd, err := m.installComposeDefinition(ctx, m.plan.previous)
	if err != nil {
		return err
	}
	if err := m.disableStaleUnits(ctx, m.plan.previous); err != nil {
		return err
	}
	return startRegularNetworkComposeIfNeeded(ctx, compose, m.plan.previous, systemd, m.plan.previousRunning)
}

func (m *regularServiceNetworkMutation) FailClosed(ctx context.Context) error {
	claimErr := m.claimFailClosedOwnership()
	if claimErr != nil && !dbMutationCommitted(claimErr) {
		return claimErr
	}
	runtimeErr := m.stopFailClosedRuntime(ctx)
	artifactErr := m.DiscardStagedArtifacts()
	return errors.Join(claimErr, runtimeErr, artifactErr)
}

func (m *regularServiceNetworkMutation) claimFailClosedOwnership() error {
	// updateServiceNetworkLocked holds the per-service operation lock across
	// Restore and FailClosed. Once this exact check succeeds, normal service
	// mutations cannot replace the attributed record before runtime cleanup.
	_, err := mutateServiceNetworkRestoreData(m.server.cfg.DB, func(data *db.Data) error {
		current := data.Services[m.plan.name]
		for _, expected := range []*db.Service{m.plan.previous, m.target, m.claimed} {
			if expected != nil && serviceNetworkRecordsEqual(current, expected) {
				return nil
			}
		}
		return fmt.Errorf("service %q changed before regular network fail-closed recovery", m.plan.name)
	})
	return err
}

func (m *regularServiceNetworkMutation) stopFailClosedRuntime(ctx context.Context) error {
	unitErr := stopRegularNetworkMutationUnitsFailClosed(ctx, m.target, m.plan.previous, m.plan.name)
	var composeErrs []error
	for _, record := range []*db.Service{m.target, m.plan.previous} {
		if record == nil || record.ServiceType != db.ServiceTypeDockerCompose {
			continue
		}
		composeErrs = append(composeErrs, m.stopComposeRecordFailClosed(ctx, record))
	}
	return errors.Join(unitErr, errors.Join(composeErrs...))
}

func (m *regularServiceNetworkMutation) stopComposeRecordFailClosed(ctx context.Context, record *db.Service) error {
	compose, err := m.composeService(record)
	if err != nil {
		return err
	}
	stopErr := compose.StopProjectContainers(ctx)
	verifyErr := verifyRegularNetworkComposeProjectAbsentForMutation(ctx, compose)
	return errors.Join(stopErr, verifyErr)
}

func (m *regularServiceNetworkMutation) stopTarget(ctx context.Context) error {
	switch m.target.ServiceType {
	case db.ServiceTypeSystemd:
		return stopRegularNetworkUnits(ctx, uniqueServiceIdentityStopUnits([]*db.Service{m.target}, m.plan.name), false)
	case db.ServiceTypeDockerCompose:
		return m.stopCompose(ctx, m.target)
	default:
		return fmt.Errorf("network mutation for service type %q is not supported", m.target.ServiceType)
	}
}

func (m *regularServiceNetworkMutation) composeService(record *db.Service) (*svc.DockerComposeService, error) {
	installer, err := m.server.NewInstaller(InstallerCfg{ServiceName: m.plan.name})
	if err != nil {
		return nil, err
	}
	return installer.newDockerComposeService(record)
}

func (m *regularServiceNetworkMutation) stopCompose(ctx context.Context, record *db.Service) error {
	compose, err := m.composeService(record)
	if err != nil {
		return err
	}
	containerErr := compose.StopProjectContainers(ctx)
	unitErr := stopRegularNetworkUnits(ctx, regularNetworkAuxiliaryUnits(record), false)
	return errors.Join(containerErr, unitErr)
}

func (m *regularServiceNetworkMutation) installComposeDefinition(ctx context.Context, record *db.Service) (*svc.SystemdService, error) {
	installer, err := m.server.NewInstaller(InstallerCfg{ServiceName: m.plan.name})
	if err != nil {
		return nil, err
	}
	service, err := newSystemdInstallService(installer, record)
	if err != nil {
		return nil, err
	}
	units, err := service.StageInstallForReload()
	if err != nil {
		return nil, err
	}
	if err := reloadAndEnableRegularNetworkUnits(ctx, units); err != nil {
		return nil, err
	}
	return service, nil
}

func reloadAndEnableRegularNetworkUnits(ctx context.Context, units []string) error {
	if err := runRegularNetworkSystemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	for _, unit := range units {
		if err := runRegularNetworkSystemctl(ctx, "enable", unit); err != nil {
			return fmt.Errorf("enable %s: %w", unit, err)
		}
	}
	return nil
}

func (m *regularServiceNetworkMutation) installSystemdDefinition(ctx context.Context, record *db.Service) (*svc.SystemdService, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	installer, err := m.server.NewInstaller(InstallerCfg{ServiceName: m.plan.name})
	if err != nil {
		return nil, err
	}
	service, err := newSystemdInstallService(installer, record)
	if err != nil {
		return nil, err
	}
	if err := m.stageAndEnableSystemdDefinition(ctx, service, record); err != nil {
		return nil, err
	}
	return service, nil
}

func (m *regularServiceNetworkMutation) stageAndEnableSystemdDefinition(ctx context.Context, service *svc.SystemdService, record *db.Service) error {
	_, err := stageRegularNetworkSystemdArtifactsForMutation(service)
	if err != nil {
		return err
	}
	if err := runRegularNetworkSystemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	return m.reconcileUnitEnablement(ctx, record)
}

func (m *regularServiceNetworkMutation) targetUnitEnablement(record *db.Service) []serviceIdentityUnitEnablement {
	return regularNetworkTargetUnitEnablement(m.plan, m.target, record)
}

func regularNetworkTargetUnitEnablement(plan *serviceNetworkMutationPlan, replacement, record *db.Service) []serviceIdentityUnitEnablement {
	previous := make(map[string]serviceIdentityUnitEnablement, len(plan.previousEnablement))
	primaryEnabled := false
	previousPrimary := serviceIdentityPrimaryRuntimeUnit(plan.previous, plan.name)
	for _, state := range plan.previousEnablement {
		previous[state.Unit] = state
		if state.Unit == previousPrimary {
			primaryEnabled = state.Enabled
		}
	}
	target := make(map[string]bool)
	targetUnits := serviceIdentityEnabledUnits(record, plan.name)
	for _, unit := range targetUnits {
		target[unit] = true
	}
	allTargetUnits := append(slices.Clone(targetUnits), serviceIdentityEnabledUnits(replacement, plan.name)...)
	unitPlan := serviceIdentityGenerationUnitPlan(plan.previous, plan.name, allTargetUnits)
	states := make([]serviceIdentityUnitEnablement, 0, len(unitPlan))
	for _, unit := range unitPlan {
		prior, existed := previous[unit]
		targetEnabled := target[unit] && primaryEnabled
		if target[unit] && existed {
			targetEnabled = prior.Enabled
		}
		states = append(states, serviceIdentityUnitEnablement{Unit: unit, Enabled: prior.Enabled, TargetEnabled: targetEnabled})
	}
	return states
}

func (m *regularServiceNetworkMutation) reconcileUnitEnablement(ctx context.Context, record *db.Service) error {
	for _, state := range m.targetUnitEnablement(record) {
		enabled, err := inspectRegularNetworkUnitEnabled(ctx, state.Unit)
		if err != nil {
			return fmt.Errorf("inspect enablement for %s: %w", state.Unit, err)
		}
		if enabled != state.TargetEnabled {
			if err := setRegularNetworkUnitEnablement(ctx, state.Unit, state.TargetEnabled); err != nil {
				return err
			}
		}
	}
	return m.verifyUnitEnablement(ctx, record)
}

func setRegularNetworkUnitEnablement(ctx context.Context, unit string, enabled bool) error {
	if enabled {
		if err := enableRegularNetworkUnit(ctx, unit); err != nil {
			return fmt.Errorf("enable %s: %w", unit, err)
		}
		return nil
	}
	if err := disableRegularNetworkUnit(ctx, unit); err != nil {
		return fmt.Errorf("disable %s: %w", unit, err)
	}
	return nil
}

func (m *regularServiceNetworkMutation) verifyUnitEnablement(ctx context.Context, record *db.Service) error {
	for _, state := range m.targetUnitEnablement(record) {
		enabled, err := inspectRegularNetworkUnitEnabled(ctx, state.Unit)
		if err != nil {
			return fmt.Errorf("verify enablement for %s: %w", state.Unit, err)
		}
		if enabled != state.TargetEnabled {
			return fmt.Errorf("unit %s enabled=%t, want %t", state.Unit, enabled, state.TargetEnabled)
		}
	}
	return nil
}

func (m *regularServiceNetworkMutation) disableStaleUnits(ctx context.Context, record *db.Service) error {
	for _, unit := range m.staleUnits(record) {
		if err := runRegularNetworkSystemctl(ctx, "disable", unit); err != nil {
			return fmt.Errorf("disable stale unit %s: %w", unit, err)
		}
	}
	return nil
}

func (m *regularServiceNetworkMutation) staleUnits(record *db.Service) []string {
	_, start := serviceIdentityRuntimeUnits(record, m.plan.name)
	targetUnits := make(map[string]bool, len(start))
	for _, unit := range start {
		targetUnits[unit] = true
	}
	var stale []string
	for _, unit := range uniqueServiceIdentityStopUnits([]*db.Service{m.plan.previous, m.target}, m.plan.name) {
		if !targetUnits[unit] && unit != m.plan.name+".service" {
			stale = append(stale, unit)
		}
	}
	return stale
}

func startRegularNetworkSystemdUnit(ctx context.Context, unit, tsUnit string, systemd *svc.SystemdService) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if unit == tsUnit {
		if err := startTailscaleSystemdSidecar(ctx, systemd); err != nil {
			return fmt.Errorf("start %s: %w", unit, err)
		}
		return nil
	}
	if err := runRegularNetworkSystemctl(ctx, "start", unit); err != nil {
		return fmt.Errorf("start %s: %w", unit, err)
	}
	return nil
}

func verifyRegularNetworkSystemdRuntime(ctx context.Context, server *Server, previous, target *db.Service, desired []serviceIdentityRuntimeUnitState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stop, _ := serviceIdentityRuntimeUnits(target, target.Name)
	want, err := validateRegularNetworkRuntimeIntent(stop, desired)
	if err != nil {
		return err
	}
	if err := verifyExactRegularNetworkRuntime(ctx, stop, want); err != nil {
		return err
	}
	if err := verifyRegularNetworkStaleUnits(ctx, previous, target.Name, stop); err != nil {
		return err
	}
	tsUnit := tailscaleServiceIdentityUnit(target, target.Name)
	if !want[tsUnit] || !serviceIdentityUnitIncluded(stop, tsUnit) {
		return nil
	}
	return verifyRegularNetworkTailscaleRuntime(ctx, server, target)
}

func regularNetworkTailscaleNeedsVerification(target *db.Service, start []string, wantRunning bool) bool {
	return wantRunning && serviceIdentityUnitIncluded(start, tailscaleServiceIdentityUnit(target, target.Name))
}

func verifyRegularNetworkTargetUnits(ctx context.Context, units []string, wantRunning bool) error {
	for _, unit := range units {
		active, err := inspectRegularNetworkUnitActive(ctx, unit)
		if err != nil {
			return err
		}
		if active != wantRunning {
			return fmt.Errorf("unit %s active state does not match previous runtime intent %t", unit, wantRunning)
		}
	}
	return nil
}

func verifyRegularNetworkStaleUnits(ctx context.Context, previous *db.Service, name string, targetStart []string) error {
	started := make(map[string]bool, len(targetStart))
	for _, unit := range targetStart {
		started[unit] = true
	}
	for _, unit := range uniqueServiceIdentityStopUnits([]*db.Service{previous}, name) {
		active, err := inspectRegularNetworkUnitActive(ctx, unit)
		if err != nil {
			return err
		}
		if !started[unit] && active {
			return fmt.Errorf("stale unit %s remains active after network replacement", unit)
		}
	}
	return nil
}

func verifyRegularNetworkTailscaleRuntime(ctx context.Context, server *Server, target *db.Service) error {
	installer, err := server.NewInstaller(InstallerCfg{ServiceName: target.Name})
	if err != nil {
		return err
	}
	systemd, err := newSystemdInstallService(installer, target)
	if err != nil {
		return err
	}
	return verifyTailscaleSystemdSidecar(ctx, systemd)
}

func (s *Server) updateServiceNetworkAndIdentityLocked(ctx context.Context, plan *serviceNetworkMutationPlan, flags cli.ServiceSetFlags, out io.Writer) (retErr error) {
	if plan.previous.ServiceType != db.ServiceTypeSystemd {
		return fmt.Errorf("--run-as applies only to native systemd workloads; apply container identity in the image or Compose service user field")
	}
	committed := false
	defer func() {
		retErr = errors.Join(retErr, finishServiceNetworkIdentityArtifacts(s, plan, committed))
	}()
	target, resolved, replacement, generationService, err := prepareServiceNetworkIdentityReplacement(ctx, s, plan, flags, out)
	if err != nil {
		return err
	}
	guarded := regularNetworkMutationNeedsResolverGuard(plan)
	if err := checkServiceNetworkIdentityResolverReady(ctx, s, plan, target, guarded); err != nil {
		return err
	}
	request, err := buildServiceNetworkIdentityMigrationRequest(plan, flags, target, resolved, replacement, generationService)
	if err != nil {
		return err
	}
	migrationErr := runServiceNetworkIdentityMigration(ctx, s, request, out, guarded)
	if migrationErr != nil {
		return fmt.Errorf("mutate service network and identity atomically: %w", migrationErr)
	}
	committed = true
	if out != nil {
		_, _ = fmt.Fprintf(out, "Service %q network and identity updated\n", plan.name)
	}
	return nil
}

func finishServiceNetworkIdentityArtifacts(s *Server, plan *serviceNetworkMutationPlan, committed bool) error {
	if plan.artifactTxn == nil {
		return nil
	}
	if !committed {
		var err error
		committed, err = plan.artifactTxn.committedArtifactReferenced(s, plan.name)
		if err != nil {
			return fmt.Errorf("determine service network artifact commit state: %w", err)
		}
	}
	if committed {
		return plan.artifactTxn.cleanupCommitted(s)
	}
	return plan.artifactTxn.rollback(s)
}

func checkServiceNetworkIdentityResolverReady(ctx context.Context, s *Server, plan *serviceNetworkMutationPlan, target *db.Service, guarded bool) error {
	if !guarded {
		return nil
	}
	canonical := plan.previous
	if slices.Contains(plan.desired.Modes, "ts") {
		canonical = target
	}
	if canonical == nil {
		return errors.New("service network and identity mutation has no canonical resolver readiness target")
	}
	return checkRegularNetworkResolverCanonicalReady(ctx, s, *canonical)
}

func runServiceNetworkIdentityMigration(ctx context.Context, s *Server, request serviceIdentityMigrationRequest, out io.Writer, guarded bool) error {
	if guarded {
		_, err := migrateServiceNetworkIdentityWithResolverGuardLocked(ctx, s, request, out)
		return err
	}
	_, err := migrateServiceNetworkIdentityLocked(ctx, s, request, out)
	return err
}

func (s *Server) prepareServiceNetworkIdentityReplacement(ctx context.Context, plan *serviceNetworkMutationPlan, flags cli.ServiceSetFlags, out io.Writer) (*db.Service, resolvedServiceIdentity, string, *svc.SystemdService, error) {
	plan.deferSandbox = true
	target, err := s.stageRegularServiceNetworkReplacement(ctx, plan)
	if err != nil {
		return nil, resolvedServiceIdentity{}, "", nil, fmt.Errorf("stage service network replacement: %w", err)
	}
	resolved, err := resolveServiceIdentity(flags.RunAs)
	if err != nil {
		return nil, resolvedServiceIdentity{}, "", nil, err
	}
	replacement, err := s.applyServiceNetworkIdentityToUnit(ctx, plan, target, resolved.Persisted)
	if err != nil {
		return nil, resolvedServiceIdentity{}, "", nil, err
	}
	generationService, err := s.serviceNetworkGenerationInstaller(plan.name, target, out)
	if err != nil {
		return nil, resolvedServiceIdentity{}, "", nil, err
	}
	return target, resolved, replacement, generationService, nil
}

func (s *Server) applyServiceNetworkIdentityToUnit(ctx context.Context, plan *serviceNetworkMutationPlan, target *db.Service, identity db.ServiceIdentity) (string, error) {
	target.Identity = &identity
	unitPath, ok := target.Artifacts.Gen(db.ArtifactSystemdUnit, target.Generation)
	if !ok {
		return "", fmt.Errorf("staged native service %q has no generation %d systemd unit", plan.name, target.Generation)
	}
	root := s.serviceRootFromView(target.View())
	raw, err := readRegularNetworkArtifact(root, unitPath)
	if err != nil {
		return "", fmt.Errorf("read staged native systemd unit: %w", err)
	}
	replacement, err := rewriteServiceIdentityUnit(string(raw), identity, s.serviceRootFromView(target.View()))
	if err != nil {
		return "", fmt.Errorf("render atomic network and identity replacement: %w", err)
	}
	replacement, sandboxPlan, err := renderRegularNetworkSandboxTarget(ctx, replacement, plan.previous, target, root, true)
	if err != nil {
		return "", fmt.Errorf("render atomic network and identity sandbox replacement: %w", err)
	}
	replacementPath, err := writeOwnedRegularNetworkArtifact(plan.artifactTxn, db.ArtifactSystemdUnit, root, "bin", plan.name+"-network-identity-", ".service", []byte(replacement), 0o644)
	if err != nil {
		return "", fmt.Errorf("write atomic network and identity replacement: %w", err)
	}
	if plan.artifactTxn == nil {
		return "", errors.New("atomic network and identity replacement has no artifact transaction")
	}
	if sandboxPlan != nil {
		if err := probeServiceSandboxForMutation(ctx, *sandboxPlan, identity.UID, identity.GID); err != nil {
			return "", fmt.Errorf("probe atomic network and identity sandbox replacement: %w", err)
		}
	}
	if err := verifyGeneratedSystemdUnitForSandboxMutation(ctx, replacementPath); err != nil {
		return "", fmt.Errorf("verify atomic network and identity sandbox replacement: %w", err)
	}
	artifact := target.Artifacts[db.ArtifactSystemdUnit]
	artifact.Refs[db.Gen(target.Generation)] = replacementPath
	artifact.Refs["latest"] = replacementPath
	return replacement, nil
}

func readRegularNetworkArtifact(root, path string) ([]byte, error) {
	root, path = filepath.Clean(root), filepath.Clean(path)
	if !pathWithinServiceIdentityRoot(root, path) {
		return nil, fmt.Errorf("artifact %s is outside service root %s", path, root)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	proof, err := captureServiceIdentityPathProofAt(root, rel, path)
	if err != nil {
		return nil, err
	}
	if !proof.Present {
		return nil, os.ErrNotExist
	}
	file, err := openVerifiedServiceIdentityProofAt(root, rel, proof)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *Server) serviceNetworkGenerationInstaller(name string, target *db.Service, out io.Writer) (*svc.SystemdService, error) {
	installer, err := s.NewInstaller(InstallerCfg{ServiceName: name, ClientOut: out})
	if err != nil {
		return nil, err
	}
	return newSystemdInstallService(installer, target)
}

func buildServiceNetworkIdentityMigrationRequest(plan *serviceNetworkMutationPlan, flags cli.ServiceSetFlags, target *db.Service, resolved resolvedServiceIdentity, replacement string, generationService *svc.SystemdService) (serviceIdentityMigrationRequest, error) {
	units := generationService.InstallUnits()
	enablement := regularNetworkTargetUnitEnablement(plan, target, target)
	states, err := generationService.InstallTargetStatesExcluding(generationService.PrimaryUnitPath())
	if err != nil {
		return serviceIdentityMigrationRequest{}, fmt.Errorf("capture staged generation install intent: %w", err)
	}
	return serviceIdentityMigrationRequest{
		Service: plan.name, Requested: flags.RunAs, Target: resolved,
		ReplacementUnit: replacement, TargetService: target,
		GenerationPaths:   generationService.InstallTargetPaths(),
		GenerationIntents: serviceIdentityInstallTargetStates(states), GenerationUnits: units,
		GenerationEnablement:     &enablement,
		StageGeneration:          stagedNativeIdentityGeneration(generationService, units),
		SandboxPreflightComplete: true,
	}, nil
}

func (s *Server) updateServiceNetworkLocked(ctx context.Context, name string, flags cli.ServiceSetFlags, out io.Writer) error {
	plan, err := s.planServiceNetworkMutation(ctx, name, flags)
	if err != nil {
		return err
	}
	if plan.noOp {
		return nil
	}
	return s.withRegularServiceNetworkAllocationLock(plan, func() error {
		return s.applyServiceNetworkMutationLocked(ctx, plan, flags, out)
	})
}

func (s *Server) applyServiceNetworkMutationLocked(ctx context.Context, plan *serviceNetworkMutationPlan, flags cli.ServiceSetFlags, out io.Writer) error {
	if direction, ok := serviceNetworkISOTransitionForPlan(plan); ok {
		if flags.RunAsSet {
			return s.updateISOServiceNetworkAndIdentityLocked(ctx, plan, flags, out, direction)
		}
		if err := s.runISOServiceNetworkMutationLocked(ctx, plan, direction); err != nil {
			return err
		}
		if out != nil {
			_, _ = fmt.Fprintf(out, "Service %q network updated\n", plan.name)
		}
		return nil
	}
	if flags.RunAsSet {
		run := func() error { return s.updateServiceNetworkAndIdentityLocked(ctx, plan, flags, out) }
		if regularNetworkMutationNeedsResolverGuard(plan) {
			return withRegularNetworkResolverMutationGuard(s, run)
		}
		return run()
	}
	if err := s.runRegularServiceNetworkMutationLocked(ctx, plan); err != nil {
		return err
	}
	if out != nil {
		_, _ = fmt.Fprintf(out, "Service %q network updated\n", plan.name)
	}
	return nil
}

func (s *Server) updateISOServiceNetworkAndIdentityLocked(ctx context.Context, plan *serviceNetworkMutationPlan, flags cli.ServiceSetFlags, out io.Writer, direction serviceNetworkISOTransition) (retErr error) {
	if plan.previous.ServiceType != db.ServiceTypeSystemd {
		return fmt.Errorf("--run-as applies only to native systemd workloads; apply container identity in the image or Compose service user field")
	}
	operation := &isoNetworkIdentityMutation{
		server: s, ctx: ctx, plan: plan, flags: flags, out: out, direction: direction,
		mutation: &isoServiceNetworkMutation{server: s, plan: plan, direction: direction},
	}
	defer operation.finish(&retErr)
	return operation.run()
}

type isoNetworkIdentityMutation struct {
	server    *Server
	ctx       context.Context
	plan      *serviceNetworkMutationPlan
	flags     cli.ServiceSetFlags
	out       io.Writer
	direction serviceNetworkISOTransition
	mutation  *isoServiceNetworkMutation
	committed bool
}

func (m *isoNetworkIdentityMutation) finish(retErr *error) {
	*retErr = errors.Join(*retErr, finishServiceNetworkIdentityArtifacts(m.server, m.plan, m.committed))
}

func (m *isoNetworkIdentityMutation) run() error {
	m.plan.deferSandbox = true
	if m.direction == serviceNetworkRegularToISO || m.direction == serviceNetworkISOToISO {
		return m.runPlannedDesiredISO()
	}
	if err := m.mutation.Stage(m.ctx); err != nil {
		return fmt.Errorf("stage ISO service network replacement: %w", err)
	}
	return m.runAfterStage()
}

func (m *isoNetworkIdentityMutation) runPlannedDesiredISO() error {
	if err := m.mutation.stagePlannedDesiredISO(m.ctx); err != nil {
		return fmt.Errorf("plan ISO service network replacement: %w", err)
	}
	request, guarded, err := m.buildMigrationRequest()
	if err != nil {
		return err
	}
	if err := m.mutation.publishPlannedISOReservation(m.ctx); err != nil {
		return fmt.Errorf("publish preflighted ISO service network reservation: %w", err)
	}
	if err := m.prepareBoundary(); err != nil {
		return m.recover(err)
	}
	return m.runPrepared(request, guarded)
}

func (m *isoNetworkIdentityMutation) runAfterStage() error {
	if m.direction == serviceNetworkISOToRegular {
		return m.runISOToRegularAfterStage()
	}
	if err := m.prepareBoundary(); err != nil {
		return m.recover(err)
	}
	request, guarded, err := m.buildMigrationRequest()
	if err != nil {
		return m.recover(err)
	}
	return m.runPrepared(request, guarded)
}

func (m *isoNetworkIdentityMutation) runISOToRegularAfterStage() error {
	request, guarded, err := m.buildMigrationRequest()
	if err != nil {
		return err
	}
	if err := m.prepareISOToRegularBoundary(); err != nil {
		return m.recover(err)
	}
	return m.runPrepared(request, guarded)
}

func (m *isoNetworkIdentityMutation) prepareBoundary() error {
	if m.direction == serviceNetworkISOToRegular {
		return m.prepareISOToRegularBoundary()
	}
	if err := m.server.EnsureISONetworkBoundary(m.ctx, m.plan.name); err != nil {
		return fmt.Errorf("verify ISO boundary before identity replacement: %w", err)
	}
	return nil
}

func (m *isoNetworkIdentityMutation) prepareISOToRegularBoundary() error {
	if err := m.mutation.StopPrevious(m.ctx); err != nil {
		return fmt.Errorf("stop ISO service before identity replacement: %w", err)
	}
	if err := m.mutation.Activate(m.ctx); err != nil {
		return fmt.Errorf("clean ISO service before identity replacement: %w", err)
	}
	if err := m.mutation.Verify(m.ctx); err != nil {
		return fmt.Errorf("verify ISO absence before identity replacement: %w", err)
	}
	return nil
}

func (m *isoNetworkIdentityMutation) buildMigrationRequest() (serviceIdentityMigrationRequest, bool, error) {
	resolved, err := resolveServiceIdentity(m.flags.RunAs)
	if err != nil {
		return serviceIdentityMigrationRequest{}, false, err
	}
	replacement, err := m.server.applyServiceNetworkIdentityToUnit(m.ctx, m.plan, m.mutation.target, resolved.Persisted)
	if err != nil {
		return serviceIdentityMigrationRequest{}, false, err
	}
	generationService, err := m.server.serviceNetworkGenerationInstaller(m.plan.name, m.mutation.target, m.out)
	if err != nil {
		return serviceIdentityMigrationRequest{}, false, err
	}
	guarded := regularNetworkMutationNeedsResolverGuard(m.plan)
	if err := checkServiceNetworkIdentityResolverReady(m.ctx, m.server, m.plan, m.mutation.target, guarded); err != nil {
		return serviceIdentityMigrationRequest{}, false, err
	}
	request, err := buildServiceNetworkIdentityMigrationRequest(m.plan, m.flags, m.mutation.target, resolved, replacement, generationService)
	return request, guarded, err
}

func (m *isoNetworkIdentityMutation) runPrepared(request serviceIdentityMigrationRequest, guarded bool) error {
	if err := runServiceNetworkIdentityMigration(m.ctx, m.server, request, m.out, guarded); err != nil {
		return m.recover(fmt.Errorf("mutate ISO service network and identity atomically: %w", err))
	}
	if err := m.commitISOState(); err != nil {
		return err
	}
	m.committed = true
	if m.out != nil {
		_, _ = fmt.Fprintf(m.out, "Service %q network and identity updated\n", m.plan.name)
	}
	return nil
}

func (m *isoNetworkIdentityMutation) commitISOState() error {
	if m.mutation.target.ISO == nil {
		return nil
	}
	state := string(iso.StateStopped)
	if m.plan.previousRunning || serviceIdentityAnyRuntimeActive(m.plan.previousRuntime) {
		state = string(iso.StateReady)
	}
	if err := m.server.markISOStateExact(m.plan.name, m.mutation.target, state, nil, "committing combined ISO network and identity state"); err != nil {
		return errors.Join(err, m.mutation.FailClosed(context.WithoutCancel(m.ctx)))
	}
	return nil
}

func (m *isoNetworkIdentityMutation) recover(cause error) error {
	return m.server.recoverISOServiceNetworkIdentityMutation(m.ctx, m.mutation, cause)
}

func (s *Server) recoverISOServiceNetworkIdentityMutation(ctx context.Context, mutation *isoServiceNetworkMutation, cause error) error {
	restoreCtx, cancelRestore := serviceNetworkRecoveryContext(ctx)
	restoreErr := mutation.Restore(restoreCtx)
	cancelRestore()
	if restoreErr == nil {
		return cause
	}
	failClosedCtx, cancelFailClosed := serviceNetworkRecoveryContext(ctx)
	failClosedErr := mutation.FailClosed(failClosedCtx)
	cancelFailClosed()
	return errors.Join(cause, fmt.Errorf("restore previous service network: %w", restoreErr), failClosedErr)
}

func serviceNetworkISOTransitionForPlan(plan *serviceNetworkMutationPlan) (serviceNetworkISOTransition, bool) {
	if plan == nil {
		return "", false
	}
	currentISO := slices.Contains(plan.currentDesired.Modes, "iso")
	desiredISO := slices.Contains(plan.desired.Modes, "iso")
	switch {
	case !currentISO && desiredISO:
		return serviceNetworkRegularToISO, true
	case currentISO && desiredISO:
		return serviceNetworkISOToISO, true
	case currentISO && !desiredISO:
		return serviceNetworkISOToRegular, true
	default:
		return "", false
	}
}

func (s *Server) runISOServiceNetworkMutationLocked(ctx context.Context, plan *serviceNetworkMutationPlan, direction serviceNetworkISOTransition) error {
	steps := newISOServiceNetworkMutationSteps(s, plan, direction)
	if regularNetworkMutationNeedsResolverGuard(plan) {
		steps = &resolverReadyServiceNetworkMutationSteps{server: s, plan: plan, steps: steps}
	}
	run := func() error { return runRegularServiceNetworkMutationSteps(ctx, steps) }
	if regularNetworkMutationNeedsResolverGuard(plan) {
		return withRegularNetworkResolverMutationGuard(s, run)
	}
	return run()
}

func (s *Server) runRegularServiceNetworkMutationLocked(ctx context.Context, plan *serviceNetworkMutationPlan) error {
	steps := newRegularServiceNetworkMutationSteps(s, plan)
	if regularNetworkMutationNeedsResolverGuard(plan) {
		steps = &resolverReadyServiceNetworkMutationSteps{server: s, plan: plan, steps: steps}
	}
	run := func() error { return runRegularServiceNetworkMutationSteps(ctx, steps) }
	if regularNetworkMutationNeedsResolverGuard(plan) {
		return withRegularNetworkResolverMutationGuard(s, run)
	}
	return run()
}

func runRegularServiceNetworkMutationSteps(ctx context.Context, steps serviceNetworkMutationSteps) error {
	if err := runServiceNetworkMutation(ctx, steps); err != nil {
		return err
	}
	cleanup, ok := steps.(serviceNetworkCommittedArtifactCleanup)
	if !ok {
		return nil
	}
	cleanupCtx, cancelCleanup := serviceNetworkRecoveryContext(ctx)
	cleanupErr := cleanup.CleanupCommittedArtifacts(cleanupCtx)
	cancelCleanup()
	if cleanupErr != nil {
		return fmt.Errorf("service network mutation committed; clean replacement artifacts: %w", cleanupErr)
	}
	return nil
}

func (s *Server) withRegularServiceNetworkAllocationLock(plan *serviceNetworkMutationPlan, run func() error) error {
	required := plan != nil && (slices.Contains(plan.desired.Modes, "svc") || slices.Contains(plan.desired.Modes, "iso"))
	return s.withRegularServiceNetworkAllocation(required, run)
}

func (s *Server) withRegularServiceNetworkAllocation(required bool, run func() error) error {
	if !required {
		return run()
	}
	release := s.serviceOperationLocks.Lock(regularServiceNetworkAllocationLockName)
	defer release()
	return run()
}

func regularNetworkMutationNeedsResolverGuard(plan *serviceNetworkMutationPlan) bool {
	if plan == nil {
		return false
	}
	return slices.Contains(plan.currentDesired.Modes, "ts") || slices.Contains(plan.desired.Modes, "ts")
}
