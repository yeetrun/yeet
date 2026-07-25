// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"golang.org/x/sys/unix"
)

func TestVMRuntimeStartupRecoveryHonorsResolverMutationBlockAtEveryBoundary(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*Server)
	}{
		{name: "blocked before startup", setup: func(server *Server) {
			server.blockTailscaleResolverRecovery(assertionError("blocked before startup"))
		}},
		{name: "blocked after install lock", setup: func(server *Server) {
			server.acquireCatchInstallLock = func(context.Context, string) (io.Closer, error) {
				server.blockTailscaleResolverRecovery(assertionError("blocked after install lock"))
				return resolverTestCloser{}, nil
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			var recoveries atomic.Int32
			server.acquireCatchInstallLock = func(context.Context, string) (io.Closer, error) {
				return resolverTestCloser{}, nil
			}
			server.recoverVMRuntimeState = func(context.Context, *Config) error {
				recoveries.Add(1)
				return nil
			}
			tt.setup(server)

			if err := server.recoverVMRuntimeStateAfterInstall(context.Background()); err == nil {
				t.Fatal("blocked VM runtime startup recovery succeeded")
			}
			if got := recoveries.Load(); got != 0 {
				t.Fatalf("VM runtime recoveries = %d, want zero", got)
			}
		})
	}
}

func TestVMRuntimeAdoptionMutationGuardLinearizesRecoveryBlock(t *testing.T) {
	server := newTestServer(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var recoveries atomic.Int32
	server.acquireCatchInstallLock = func(context.Context, string) (io.Closer, error) {
		return resolverTestCloser{}, nil
	}
	server.recoverVMRuntimeState = func(ctx context.Context, cfg *Config) error {
		return WithVMRuntimeTransactionLock(ctx, cfg, func() error {
			return server.withTailscaleResolverMutationGuard(func() error {
				recoveries.Add(1)
				close(entered)
				<-release
				return nil
			})
		})
	}

	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- server.recoverVMRuntimeStateAfterInstall(context.Background())
	}()
	<-entered

	blockDone := make(chan error, 1)
	go func() {
		blockDone <- server.blockTailscaleResolverRecovery(assertionError("concurrent recovery block"))
	}()
	assertResolverBlockStillWaiting(t, blockDone)
	close(release)
	if err := awaitResolverTestResult(t, mutationDone); err != nil {
		t.Fatalf("in-flight adoption mutation: %v", err)
	}
	if err := awaitResolverTestResult(t, blockDone); !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
		t.Fatalf("recovery block result = %v, want resolver recovery block", err)
	}

	if err := server.recoverVMRuntimeStateAfterInstall(context.Background()); !errors.Is(
		err,
		errTailscaleResolverRecoveryBlocked,
	) {
		t.Fatalf("adoption mutation after block = %v, want rejection", err)
	}
	if got := recoveries.Load(); got != 1 {
		t.Fatalf("adoption recovery calls = %d, want only the completed in-flight mutation", got)
	}
}

func TestVMRuntimeTrialMutationGuardLinearizesRecoveryBlock(t *testing.T) {
	fixture, coordinator, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	adoptVMRuntimeTransitionFixture(t, fixture, coordinator)
	target, resolved := newVMRuntimeTransitionTarget(t, fixture)
	if _, err := stageVMRuntimeWithDeps(
		context.Background(),
		&fixture.cfg,
		"devbox",
		resolved,
		coordinator,
	); err != nil {
		t.Fatal(err)
	}
	configured := readLatestVMRuntimeAdoptionData(
		t,
		fixture.store,
	).Services["devbox"].VM.Components.Runtime.Configured
	deps, _ := writeVMRuntimeTrialReconcileFixture(
		t,
		fixture,
		coordinator,
		vmRuntimeTrialHealthy,
		target,
		configured,
		"",
	)
	realUnlink := deps.control.unlink
	entered := make(chan struct{})
	release := make(chan struct{})
	var unlinks atomic.Int32
	deps.control.unlink = func(parent int, name string, flags int) error {
		unlinks.Add(1)
		close(entered)
		<-release
		return realUnlink(parent, name, flags)
	}
	view, err := fixture.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	service := view.AsStruct().Services["devbox"]
	server := &Server{cfg: fixture.cfg}

	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- server.consumeVMRuntimeTrialService(
			context.Background(),
			service,
			deps,
		)
	}()
	<-entered

	blockDone := make(chan error, 1)
	go func() {
		blockDone <- server.blockTailscaleResolverRecovery(assertionError("concurrent trial block"))
	}()
	assertResolverBlockStillWaiting(t, blockDone)
	close(release)
	if err := awaitResolverTestResult(t, mutationDone); err != nil {
		t.Fatalf("in-flight trial mutation: %v", err)
	}
	if err := awaitResolverTestResult(t, blockDone); !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
		t.Fatalf("recovery block result = %v, want resolver recovery block", err)
	}

	latest, err := fixture.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	err = server.consumeVMRuntimeTrialService(
		context.Background(),
		latest.AsStruct().Services["devbox"],
		deps,
	)
	if !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
		t.Fatalf("trial mutation after block = %v, want rejection", err)
	}
	if got := unlinks.Load(); got != 1 {
		t.Fatalf("trial result removals = %d, want only the completed in-flight mutation", got)
	}
}

func TestVMRuntimeResolverMutationLockOrderDoesNotDeadlockQueuedBlock(t *testing.T) {
	fixture, deps, _ := newResolverTrialConsumptionFixture(t)
	server := &Server{cfg: fixture.cfg}
	writerAcquired := make(chan struct{})
	allowWriter := make(chan struct{})
	server.tailscaleResolverRecovery.afterBlockLock = func() {
		close(writerAcquired)
		<-allowWriter
	}
	serviceView, err := fixture.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	service := serviceView.AsStruct().Services["devbox"]

	flockAttempted := make(chan struct{})
	var signalFlock sync.Once
	deps.coordinator.journal.flock = func(fd, operation int) error {
		signalFlock.Do(func() { close(flockAttempted) })
		return unix.Flock(fd, operation)
	}

	holderEntered := make(chan struct{})
	holderGuard := make(chan struct{})
	holderGuardAttempted := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- WithVMRuntimeTransactionLock(context.Background(), &fixture.cfg, func() error {
			close(holderEntered)
			<-holderGuard
			close(holderGuardAttempted)
			return server.withTailscaleResolverMutationGuard(func() error { return nil })
		})
	}()
	awaitResolverTestSignal(t, holderEntered, "VM runtime lock holder")

	consumerCtx, cancelConsumer := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelConsumer()
	consumerDone := make(chan error, 1)
	go func() {
		consumerDone <- server.consumeVMRuntimeTrialService(consumerCtx, service, deps)
	}()
	awaitResolverTestSignal(t, flockAttempted, "trial consumer VM runtime lock attempt")

	blockDone := make(chan error, 1)
	go func() {
		blockDone <- server.blockTailscaleResolverRecovery(assertionError("queued recovery block"))
	}()
	awaitResolverTestSignal(t, writerAcquired, "resolver block writer to acquire G")
	close(holderGuard)
	awaitResolverTestSignal(t, holderGuardAttempted, "V holder to attempt G")
	assertResolverTestPending(t, holderDone, "V holder while writer owns G")
	assertResolverTestPending(t, consumerDone, "trial consumer while holder owns V")
	close(allowWriter)

	if err := awaitResolverTestResult(t, blockDone); !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
		t.Fatalf("recovery block result = %v, want resolver recovery block", err)
	}
	if err := awaitResolverTestResult(t, holderDone); !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
		t.Fatalf("V-to-G holder result = %v, want resolver recovery block", err)
	}
	if err := awaitResolverTestResult(t, consumerDone); !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
		t.Fatalf("trial consumer result = %v, want resolver recovery block rather than lock timeout", err)
	}
}

func TestVMRuntimeRestartDefaultConsumeHonorsResolverBlockWithoutSideEffects(t *testing.T) {
	fixture, deps, resultPath := newResolverTrialConsumptionFixture(t)
	server := &Server{cfg: fixture.cfg, vmRuntimeTrialDeps: &deps}
	if err := WithVMRuntimeTransactionLock(
		context.Background(),
		&fixture.cfg,
		func() error { return nil },
	); err != nil {
		t.Fatal(err)
	}
	beforeResult, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeRuntime := readLatestVMRuntimeAdoptionData(
		t,
		fixture.store,
	).Services["devbox"].VM.Components.Runtime
	beforeJournals := readResolverTestDirNames(
		t,
		filepath.Join(fixture.dataRoot, vmRuntimeJournalDirName),
	)
	server.blockTailscaleResolverRecovery(assertionError("default consume blocked"))

	outcome, err := server.runtimeRestartDependencies().consume(
		context.Background(),
		"devbox",
	)
	if !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
		t.Fatalf("default consume outcome=%q error=%v, want resolver recovery block", outcome, err)
	}
	afterResult, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read blocked trial result: %v", err)
	}
	if !bytes.Equal(afterResult, beforeResult) {
		t.Fatal("blocked default consume changed the trial result")
	}
	afterRuntime := readLatestVMRuntimeAdoptionData(
		t,
		fixture.store,
	).Services["devbox"].VM.Components.Runtime
	if !reflect.DeepEqual(afterRuntime, beforeRuntime) {
		t.Fatalf("blocked default consume changed runtime state:\nbefore: %#v\nafter:  %#v", beforeRuntime, afterRuntime)
	}
	afterJournals := readResolverTestDirNames(
		t,
		filepath.Join(fixture.dataRoot, vmRuntimeJournalDirName),
	)
	if !reflect.DeepEqual(afterJournals, beforeJournals) {
		t.Fatalf("blocked default consume changed journal artifacts: before=%v after=%v", beforeJournals, afterJournals)
	}
}

func TestVMRuntimeResolverMutationLocksReleaseOnContextAndError(t *testing.T) {
	t.Run("context while waiting for V", func(t *testing.T) {
		fixture, deps, _ := newResolverTrialConsumptionFixture(t)
		server := &Server{cfg: fixture.cfg}
		serviceView, err := fixture.cfg.DB.Get()
		if err != nil {
			t.Fatal(err)
		}
		service := serviceView.AsStruct().Services["devbox"]

		holderEntered := make(chan struct{})
		releaseHolder := make(chan struct{})
		holderDone := make(chan error, 1)
		go func() {
			holderDone <- WithVMRuntimeTransactionLock(
				context.Background(),
				&fixture.cfg,
				func() error {
					close(holderEntered)
					<-releaseHolder
					return nil
				},
			)
		}()
		awaitResolverTestSignal(t, holderEntered, "VM runtime context-test holder")

		flockAttempted := make(chan struct{})
		var signalFlock sync.Once
		deps.coordinator.journal.flock = func(fd, operation int) error {
			signalFlock.Do(func() { close(flockAttempted) })
			return unix.Flock(fd, operation)
		}
		ctx, cancel := context.WithCancel(context.Background())
		consumerDone := make(chan error, 1)
		go func() {
			consumerDone <- server.consumeVMRuntimeTrialService(ctx, service, deps)
		}()
		awaitResolverTestSignal(t, flockAttempted, "cancelled trial consumer VM runtime lock attempt")
		cancel()
		if err := awaitResolverTestResult(t, consumerDone); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled trial consumer = %v, want context cancellation", err)
		}

		blockDone := make(chan error, 1)
		go func() {
			blockDone <- server.blockTailscaleResolverRecovery(assertionError("block after cancellation"))
		}()
		if err := awaitResolverTestResult(t, blockDone); !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
			t.Fatalf("resolver block after cancellation = %v", err)
		}
		close(releaseHolder)
		if err := awaitResolverTestResult(t, holderDone); err != nil {
			t.Fatalf("VM runtime holder after cancellation: %v", err)
		}
	})

	t.Run("mutation error while holding V and G", func(t *testing.T) {
		server := newTestServer(t)
		configured := vmRuntimeLaunchTestArtifact("v1.17.0", "/configured")
		seedVMRuntimeRestartService(t, server, configured, nil, nil)
		if _, _, err := server.cfg.DB.MutateService(
			"devbox",
			func(_ *db.Data, service *db.Service) error {
				service.VM.Components.Runtime.Trial = &db.VMRuntimeTrialConfig{
					State:         string(vmRuntimeTrialHealthy),
					CandidateID:   configured.ID,
					RecoveryPoint: "pool/vm@runtime-upgrade",
				}
				return nil
			},
		); err != nil {
			t.Fatal(err)
		}
		mutationErr := assertionError("unprotect failed")
		err := server.reconcileHealthyVMRuntimeRecoveryPoint(
			context.Background(),
			"devbox",
			func(context.Context, string) error { return mutationErr },
		)
		if !errors.Is(err, mutationErr) {
			t.Fatalf("recovery-point mutation error = %v, want %v", err, mutationErr)
		}

		lockCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := WithVMRuntimeTransactionLock(lockCtx, &server.cfg, func() error {
			return server.withTailscaleResolverMutationGuard(func() error { return nil })
		}); err != nil {
			t.Fatalf("locks retained after mutation error: %v", err)
		}
	})
}

func TestVMBalloonMutationBoundaryStopsAfterDynamicResolverBlock(t *testing.T) {
	server := newTestServer(t)
	for _, name := range []string{"alpha", "bravo"} {
		_, err := server.cfg.DB.MutateData(func(data *db.Data) error {
			if data.Services == nil {
				data.Services = map[string]*db.Service{}
			}
			data.Services[name] = &db.Service{
				Name: name,
				VM: &db.VMConfig{
					Balloon: db.VMBalloonConfig{},
				},
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	api := &blockingVMBalloonAPI{server: server}
	errs := server.applyVMBalloonTargets(
		context.Background(),
		api,
		vmBalloonControllerPlan{Targets: map[string]int64{"alpha": 1, "bravo": 2}},
		map[string]vmBalloonReconcileCandidate{
			"alpha": {service: "alpha", socket: "alpha.sock"},
			"bravo": {service: "bravo", socket: "bravo.sock"},
		},
	)
	if len(errs) == 0 {
		t.Fatal("dynamic balloon mutation block produced no error")
	}
	if got := api.calls.Load(); got != 1 {
		t.Fatalf("balloon SetTarget calls = %d, want one before dynamic block", got)
	}
	dv, err := server.getDB()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "bravo"} {
		if got := dv.Services().Get(name).VM().Balloon().LastTargetBytes; got != 0 {
			t.Fatalf("%s persisted balloon target %d after dynamic block", name, got)
		}
	}
}

func TestVMRuntimeTrialConsumerHonorsResolverMutationBlock(t *testing.T) {
	server := newTestServer(t)
	configured := vmRuntimeLaunchTestArtifact("v1.17.0", "/configured")
	seedVMRuntimeRestartService(t, server, configured, nil, nil)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.VM.Components.Runtime.Trial = &db.VMRuntimeTrialConfig{
			State:         string(vmRuntimeTrialHealthy),
			CandidateID:   configured.ID,
			RecoveryPoint: "pool/vm@runtime-upgrade",
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var unprotectCalls atomic.Int32
	server.vmRuntimeRestartDeps = &vmRuntimeRestartDeps{
		unprotect: func(context.Context, string) error {
			unprotectCalls.Add(1)
			return nil
		},
	}
	server.blockTailscaleResolverRecovery(assertionError("trial mutation blocked"))

	if err := server.consumeVMRuntimeTrialResults(context.Background()); err == nil {
		t.Fatal("blocked trial consumer succeeded")
	}
	if got := unprotectCalls.Load(); got != 0 {
		t.Fatalf("blocked trial consumer unprotect calls = %d, want zero", got)
	}
}

func TestVMRuntimeTrialWatcherHonorsResolverBlockSetAfterLaunch(t *testing.T) {
	server := newTestServer(t)
	configured := vmRuntimeLaunchTestArtifact("v1.17.0", "/configured")
	seedVMRuntimeRestartService(t, server, configured, nil, nil)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.VM.Components.Runtime.Trial = &db.VMRuntimeTrialConfig{
			State:         string(vmRuntimeTrialHealthy),
			CandidateID:   configured.ID,
			RecoveryPoint: "pool/vm@runtime-upgrade",
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var unprotectCalls atomic.Int32
	server.vmRuntimeRestartDeps = &vmRuntimeRestartDeps{
		unprotect: func(context.Context, string) error {
			unprotectCalls.Add(1)
			return nil
		},
	}
	server.vmRuntimeTrialDeps = &vmRuntimeTrialConsumerDeps{interval: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.runVMRuntimeTrialWatcher(ctx)
	}()
	server.blockTailscaleResolverRecovery(assertionError("blocked after watcher launch"))
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	if got := unprotectCalls.Load(); got != 0 {
		t.Fatalf("dynamically blocked watcher unprotect calls = %d, want zero", got)
	}
}

type blockingVMBalloonAPI struct {
	server *Server
	calls  atomic.Int32
}

func (api *blockingVMBalloonAPI) SetTarget(context.Context, string, int64) error {
	if api.calls.Add(1) == 1 {
		api.server.blockTailscaleResolverRecovery(assertionError("blocked after first balloon target"))
	}
	return nil
}

func (*blockingVMBalloonAPI) Stats(context.Context, string) (vmBalloonStats, error) {
	return vmBalloonStats{}, nil
}

type assertionError string

func (err assertionError) Error() string { return string(err) }

type resolverTestCloser struct{}

func (resolverTestCloser) Close() error { return nil }

func assertResolverBlockStillWaiting(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		t.Fatalf("recovery block returned while mutation was in flight: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
}

func awaitResolverTestResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for resolver mutation test result")
		return nil
	}
}

func awaitResolverTestSignal(t *testing.T, signal <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}

func assertResolverTestPending(t *testing.T, result <-chan error, operation string) {
	t.Helper()
	select {
	case err := <-result:
		t.Fatalf("%s completed before its held lock was released: %v", operation, err)
	default:
	}
}

func newResolverTrialConsumptionFixture(
	t *testing.T,
) (*vmRuntimeAdoptionFixture, vmRuntimeTrialConsumerDeps, string) {
	t.Helper()
	fixture, coordinator, _ := newVMRuntimeAdoptionTransactionFixture(t, false)
	adoptVMRuntimeTransitionFixture(t, fixture, coordinator)
	target, resolved := newVMRuntimeTransitionTarget(t, fixture)
	if _, err := stageVMRuntimeWithDeps(
		context.Background(),
		&fixture.cfg,
		"devbox",
		resolved,
		coordinator,
	); err != nil {
		t.Fatal(err)
	}
	configured := readLatestVMRuntimeAdoptionData(
		t,
		fixture.store,
	).Services["devbox"].VM.Components.Runtime.Configured
	deps, resultPath := writeVMRuntimeTrialReconcileFixture(
		t,
		fixture,
		coordinator,
		vmRuntimeTrialHealthy,
		target,
		configured,
		"",
	)
	return fixture, deps, resultPath
}

func readResolverTestDirNames(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}
