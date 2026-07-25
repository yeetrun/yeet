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
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
	"github.com/yeetrun/yeet/pkg/svc"
)

func TestTailscaleResolverReadinessGatesStartRestartAndEnable(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*ttyExecer) error
	}{
		{name: "start", run: (*ttyExecer).startCmdFunc},
		{name: "restart", run: (*ttyExecer).restartCmdFunc},
		{name: "enable", run: (*ttyExecer).enableCmdFunc},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTailscaleResolverPlanFixture(
				t,
				"readiness-"+test.name,
				tailscaleResolverGenerationCurrent,
				"",
			)
			runner := &recordingServiceRunner{}
			execer := &ttyExecer{
				ctx: context.Background(), s: fixture.server, sn: fixture.service.Name,
				rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
				serviceRunnerFn: func() (ServiceRunner, error) { return runner, nil },
			}

			err := test.run(execer)
			if err == nil || !strings.Contains(err.Error(), "resolver") {
				t.Fatalf("%s error = %v, want resolver readiness rejection", test.name, err)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("%s runner calls = %v, want no start/restart/enable calls", test.name, runner.calls)
			}
		})
	}

	t.Run("global recovery block", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(
			t,
			"readiness-blocked",
			tailscaleResolverGenerationCurrent,
			"",
		)
		guardTailscaleResolverFixture(t, fixture)
		block := fixture.server.blockTailscaleResolverRecovery(errors.New("repair required"))
		runner := &recordingServiceRunner{}
		execer := &ttyExecer{
			ctx: context.Background(), s: fixture.server, sn: fixture.service.Name,
			rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
			serviceRunnerFn: func() (ServiceRunner, error) { return runner, nil },
		}

		err := execer.startCmdFunc()
		if !errors.Is(err, block) {
			t.Fatalf("start error = %v, want global resolver recovery block %v", err, block)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("blocked start runner calls = %v, want none", runner.calls)
		}
	})

	t.Run("selected Catch runner", func(t *testing.T) {
		fixture := newTailscaleResolverPlanFixture(
			t,
			"readiness-runner",
			tailscaleResolverGenerationCurrent,
			"",
		)
		guardTailscaleResolverFixture(t, fixture)
		if err := os.Remove(fixture.server.catchRunnerPath()); err != nil {
			t.Fatalf("remove selected Catch runner: %v", err)
		}
		runner := &recordingServiceRunner{}
		execer := &ttyExecer{
			ctx: context.Background(), s: fixture.server, sn: fixture.service.Name,
			rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
			serviceRunnerFn: func() (ServiceRunner, error) { return runner, nil },
		}

		err := execer.startCmdFunc()
		if err == nil || !strings.Contains(err.Error(), "Catch runner") {
			t.Fatalf("start error = %v, want missing selected Catch runner rejection", err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("missing-runner start calls = %v, want none", runner.calls)
		}
	})
}

func TestTailscaleResolverReadinessLinearizesOrdinaryActivationWithGlobalBlock(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*ttyExecer) error
	}{
		{name: "start", run: (*ttyExecer).startCmdFunc},
		{name: "enable", run: (*ttyExecer).enableCmdFunc},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGuardedTailscaleResolverFixture(
				t,
				"linearized-"+test.name,
				tailscaleResolverGenerationCurrent,
			)
			entered := make(chan struct{})
			releaseAction := make(chan struct{}, 1)
			runner := &blockingServiceActivationRunner{
				action:  test.name,
				entered: entered,
				release: releaseAction,
			}
			t.Cleanup(func() {
				select {
				case releaseAction <- struct{}{}:
				default:
				}
			})
			execer := &ttyExecer{
				ctx: context.Background(), s: fixture.server, sn: fixture.service.Name,
				rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
				serviceRunnerFn: func() (ServiceRunner, error) { return runner, nil },
			}
			actionDone := make(chan error, 1)
			go func() { actionDone <- test.run(execer) }()
			awaitResolverTestSignal(t, entered, test.name+" action")

			writerAttempted := make(chan struct{})
			writerAcquired := make(chan struct{})
			fixture.server.tailscaleResolverRecovery.afterBlockLock = func() {
				close(writerAcquired)
			}
			blockDone := make(chan error, 1)
			go func() {
				close(writerAttempted)
				blockDone <- fixture.server.blockTailscaleResolverRecovery(
					errors.New("block after readiness"),
				)
			}()
			awaitResolverTestSignal(t, writerAttempted, "resolver block attempt during "+test.name)
			select {
			case <-writerAcquired:
				t.Fatalf("resolver block acquired the global guard during %s", test.name)
			case <-time.After(25 * time.Millisecond):
			}
			releaseAction <- struct{}{}
			if err := awaitResolverTestResult(t, actionDone); err != nil {
				t.Fatalf("%s action: %v", test.name, err)
			}
			if err := awaitResolverTestResult(t, blockDone); !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
				t.Fatalf("resolver block after %s = %v", test.name, err)
			}
		})
	}
}

func TestTailscaleResolverVMActivationAcquiresRuntimeBeforeGlobalGuard(t *testing.T) {
	fixture := newGuardedTailscaleResolverFixture(
		t,
		"vm-lock-order",
		tailscaleResolverGenerationCurrent,
	)
	allocation := testISORuntimeAllocation(fixture.service.Name, iso.StateStopped)
	if _, _, err := fixture.server.cfg.DB.MutateService(fixture.service.Name, func(_ *db.Data, current *db.Service) error {
		current.ServiceType = db.ServiceTypeVM
		current.ISO = allocation
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	runtimeHeld := make(chan struct{})
	allowWriter := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- WithVMRuntimeTransactionLock(context.Background(), &fixture.server.cfg, func() error {
			close(runtimeHeld)
			<-allowWriter
			return fixture.server.blockTailscaleResolverRecovery(errors.New("queued VM activation block"))
		})
	}()
	awaitResolverTestSignal(t, runtimeHeld, "VM runtime holder")

	previousEnsure := ensureVMNetworkForServiceAction
	oldOrderUsed := make(chan struct{}, 1)
	ensureVMNetworkForServiceAction = func(*Server, context.Context, string) error {
		oldOrderUsed <- struct{}{}
		return errors.New("VM network ensure ran before acquiring V")
	}
	t.Cleanup(func() { ensureVMNetworkForServiceAction = previousEnsure })

	runtimeAttempted := make(chan struct{})
	runner := &recordingServiceRunner{}
	execer := &ttyExecer{
		ctx: context.Background(), s: fixture.server, sn: fixture.service.Name,
		rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
		serviceRunnerFn: func() (ServiceRunner, error) { return runner, nil },
		vmRuntimeTransactionFunc: func(ctx context.Context, cfg *Config, operation func() error) error {
			close(runtimeAttempted)
			return WithVMRuntimeTransactionLock(ctx, cfg, operation)
		},
	}
	actionDone := make(chan error, 1)
	go func() { actionDone <- execer.startCmdFunc() }()

	select {
	case <-runtimeAttempted:
	case <-oldOrderUsed:
		t.Fatal("VM activation entered resolver readiness before acquiring the runtime lock")
	case <-time.After(time.Second):
		t.Fatal("VM activation did not attempt the runtime lock")
	}
	close(allowWriter)
	if err := awaitResolverTestResult(t, holderDone); !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
		t.Fatalf("queued recovery block = %v, want resolver recovery block", err)
	}
	if err := awaitResolverTestResult(t, actionDone); !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
		t.Fatalf("VM activation = %v, want queued resolver recovery block", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("blocked VM activation runner calls = %v, want none", runner.calls)
	}
}

func TestNonTailscaleISOVMActivationBypassesResolverRecoveryBlock(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*ttyExecer) error
		want string
	}{
		{name: "start", run: (*ttyExecer).startCmdFunc, want: "start"},
		{name: "restart", run: (*ttyExecer).restartCmdFunc, want: "restart"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t)
			allocation := testISORuntimeAllocation("devbox", iso.StateStopped)
			addTestServices(t, server, db.Service{
				Name: "devbox", ServiceType: db.ServiceTypeVM, ISO: allocation,
			})
			server.blockTailscaleResolverRecovery(errors.New("unrelated resolver recovery"))

			previousEnsure := ensureVMNetworkForServiceAction
			ensureCalls := 0
			ensureVMNetworkForServiceAction = func(*Server, context.Context, string) error {
				ensureCalls++
				return nil
			}
			t.Cleanup(func() { ensureVMNetworkForServiceAction = previousEnsure })

			runtimeCalls := 0
			runner := &recordingServiceRunner{}
			execer := &ttyExecer{
				ctx: context.Background(), s: server, sn: "devbox",
				rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
				serviceRunnerFn: func() (ServiceRunner, error) { return runner, nil },
				vmRuntimeTransactionFunc: func(ctx context.Context, cfg *Config, operation func() error) error {
					runtimeCalls++
					return WithVMRuntimeTransactionLock(ctx, cfg, operation)
				},
			}

			if err := test.run(execer); err != nil {
				t.Fatalf("non-Tailscale ISO VM %s: %v", test.name, err)
			}
			if runtimeCalls != 1 || ensureCalls != 1 {
				t.Fatalf("non-Tailscale ISO VM %s runtime=%d ensure=%d, want 1 each", test.name, runtimeCalls, ensureCalls)
			}
			if !reflect.DeepEqual(runner.calls, []string{test.want}) {
				t.Fatalf("non-Tailscale ISO VM %s runner calls = %v, want [%s]", test.name, runner.calls, test.want)
			}
		})
	}
}

func TestOrdinaryNonTailscaleActivationRetainsResolverRecoveryBlock(t *testing.T) {
	for _, serviceType := range []db.ServiceType{
		db.ServiceTypeSystemd,
		db.ServiceTypeDockerCompose,
		db.ServiceTypeVM,
	} {
		for _, action := range []struct {
			name string
			run  func(*ttyExecer) error
		}{
			{name: "start", run: (*ttyExecer).startCmdFunc},
			{name: "restart", run: (*ttyExecer).restartCmdFunc},
		} {
			t.Run(string(serviceType)+"/"+action.name, func(t *testing.T) {
				server := newTestServer(t)
				addTestServices(t, server, db.Service{
					Name: "ordinary", ServiceType: serviceType,
				})
				block := server.blockTailscaleResolverRecovery(errors.New("resolver recovery retained"))
				runner := &recordingServiceRunner{}
				execer := &ttyExecer{
					ctx: context.Background(), s: server, sn: "ordinary",
					rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
					serviceRunnerFn: func() (ServiceRunner, error) { return runner, nil },
				}

				err := action.run(execer)
				if !errors.Is(err, block) {
					t.Fatalf("ordinary non-Tailscale %s %s error = %v, want %v", serviceType, action.name, err, block)
				}
				if len(runner.calls) != 0 {
					t.Fatalf("ordinary non-Tailscale %s %s runner calls = %v, want none", serviceType, action.name, runner.calls)
				}
			})
		}
	}
}

type blockingServiceActivationRunner struct {
	action  string
	entered chan<- struct{}
	release <-chan struct{}
}

func (r *blockingServiceActivationRunner) block(action string) error {
	if r.action != action {
		return nil
	}
	close(r.entered)
	<-r.release
	return nil
}

func (*blockingServiceActivationRunner) SetNewCmd(func(string, ...string) *exec.Cmd) {}
func (r *blockingServiceActivationRunner) Start() error {
	return r.block("start")
}
func (*blockingServiceActivationRunner) Stop() error { return nil }
func (r *blockingServiceActivationRunner) Restart() error {
	return r.block("restart")
}
func (*blockingServiceActivationRunner) Logs(*svc.LogOptions) error { return nil }
func (*blockingServiceActivationRunner) Remove() error              { return nil }
func (r *blockingServiceActivationRunner) Enable() error {
	return r.block("enable")
}
func (*blockingServiceActivationRunner) Disable() error { return nil }

func TestTailscaleResolverReadinessGatesServiceIdentityActivation(t *testing.T) {
	fixture := newTailscaleResolverPlanFixture(
		t,
		"identity-readiness",
		tailscaleResolverGenerationHistorical,
		"",
	)
	startCalls := 0
	migration := &serviceIdentityMigration{
		server: fixture.server,
		req: serviceIdentityMigrationRequest{
			Service: fixture.service.Name,
		},
		target: &fixture.service,
		result: serviceIdentityMigrationResult{WasRunning: true},
		ops: serviceIdentityMigrationOps{
			phase: func(string) error { return nil },
			start: func(context.Context, string) error {
				startCalls++
				return nil
			},
		},
	}

	err := migration.startReplacementServiceIdentity(context.Background())
	if err == nil || !strings.Contains(err.Error(), "resolver") {
		t.Fatalf("identity activation error = %v, want resolver readiness rejection", err)
	}
	if startCalls != 0 {
		t.Fatalf("tailscale sidecar start calls = %d, want 0", startCalls)
	}
}

func TestTailscaleResolverReadinessGatesEntireServiceIdentityTransaction(t *testing.T) {
	fixture := newTailscaleResolverPlanFixture(
		t,
		"identity-transaction-readiness",
		tailscaleResolverGenerationCurrent,
		"",
	)
	fixture.service.ServiceType = db.ServiceTypeSystemd
	if _, _, err := fixture.server.cfg.DB.MutateService(
		fixture.service.Name,
		func(_ *db.Data, service *db.Service) error {
			service.ServiceType = db.ServiceTypeSystemd
			return nil
		},
	); err != nil {
		t.Fatalf("set native service type: %v", err)
	}
	identity := db.ServiceIdentity{
		RequestedUser:  strconv.Itoa(os.Geteuid()),
		RequestedGroup: strconv.Itoa(os.Getegid()),
		UID:            uint32(os.Geteuid()),
		GID:            uint32(os.Getegid()),
	}
	target := fixture.service.Clone()
	target.Identity = &identity
	unitPath := filepath.Join(t.TempDir(), fixture.service.Name+".service")
	var installCalls, enableCalls, disableCalls, startCalls, commitCalls int
	enabled := map[string]bool{"yeet-" + fixture.service.Name + "-ts.service": false}
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return false, nil },
		start: func(context.Context, string) error {
			startCalls++
			return nil
		},
		snapshot: func(context.Context, *db.Service) (string, error) { return "", nil },
		inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			return serviceIdentityInspection{}, nil
		},
		apply:  func(serviceIdentityInspection, *serviceIdentityJournal) error { return nil },
		reload: func(context.Context) error { return nil },
		verify: func(context.Context, serviceIdentityMigrationVerification) error { return nil },
		commit: func(*db.Service, *db.Service) error {
			commitCalls++
			return nil
		},
		isEnabled: func(_ context.Context, unit string) (bool, error) {
			return enabled[unit], nil
		},
		enable: func(_ context.Context, unit string) error {
			enableCalls++
			enabled[unit] = true
			return nil
		},
		disable: func(_ context.Context, unit string) error {
			disableCalls++
			enabled[unit] = false
			return nil
		},
	}

	_, err := fixture.server.migrateServiceIdentity(
		context.Background(),
		serviceIdentityMigrationRequest{
			Service:         fixture.service.Name,
			Requested:       identity.RequestedUser + ":" + identity.RequestedGroup,
			Target:          resolvedServiceIdentity{Persisted: identity},
			TargetService:   target,
			ReplacementUnit: "[Service]\nUser=" + identity.RequestedUser + "\nGroup=" + identity.RequestedGroup + "\n",
			StageGeneration: func(context.Context) error {
				installCalls++
				return nil
			},
			GenerationUnits: []string{"yeet-" + fixture.service.Name + "-ts.service"},
			ops:             &ops,
		},
		os.Stderr,
	)
	if err == nil || !strings.Contains(err.Error(), "resolver") {
		t.Fatalf("identity transaction error = %v, want resolver readiness rejection", err)
	}
	if installCalls != 0 || enableCalls != 0 || disableCalls != 0 || startCalls != 0 || commitCalls != 0 {
		t.Fatalf(
			"rejected identity transaction calls install=%d enable=%d disable=%d start=%d commit=%d, want all zero",
			installCalls,
			enableCalls,
			disableCalls,
			startCalls,
			commitCalls,
		)
	}
}

func TestTailscaleResolverReadinessLinearizesIdentityTransactionWithGlobalBlock(t *testing.T) {
	fixture := newGuardedTailscaleResolverFixture(
		t,
		"identity-transaction-linearized",
		tailscaleResolverGenerationCurrent,
	)
	fixture.service.ServiceType = db.ServiceTypeSystemd
	if _, _, err := fixture.server.cfg.DB.MutateService(
		fixture.service.Name,
		func(_ *db.Data, service *db.Service) error {
			service.ServiceType = db.ServiceTypeSystemd
			return nil
		},
	); err != nil {
		t.Fatalf("set native service type: %v", err)
	}
	identity := db.ServiceIdentity{
		RequestedUser:  strconv.Itoa(os.Geteuid()),
		RequestedGroup: strconv.Itoa(os.Getegid()),
		UID:            uint32(os.Geteuid()),
		GID:            uint32(os.Getegid()),
	}
	target := fixture.service.Clone()
	target.Identity = &identity
	unitPath := filepath.Join(t.TempDir(), fixture.service.Name+".service")
	ops := serviceIdentityMigrationOps{
		unitPath:  func(string) string { return unitPath },
		isRunning: func(context.Context, string) (bool, error) { return false, nil },
		snapshot:  func(context.Context, *db.Service) (string, error) { return "", nil },
		inspect: func(context.Context, serviceIdentityInspectionRequest) (serviceIdentityInspection, error) {
			return serviceIdentityInspection{}, nil
		},
		apply:  func(serviceIdentityInspection, *serviceIdentityJournal) error { return nil },
		reload: func(context.Context) error { return nil },
		verify: func(context.Context, serviceIdentityMigrationVerification) error {
			return nil
		},
		commit: func(*db.Service, *db.Service) error { return nil },
	}
	stageEntered := make(chan struct{})
	releaseStage := make(chan struct{}, 1)
	t.Cleanup(func() {
		select {
		case releaseStage <- struct{}{}:
		default:
		}
	})
	migrationDone := make(chan error, 1)
	go func() {
		_, err := fixture.server.migrateServiceIdentity(
			context.Background(),
			serviceIdentityMigrationRequest{
				Service:         fixture.service.Name,
				Requested:       identity.RequestedUser + ":" + identity.RequestedGroup,
				Target:          resolvedServiceIdentity{Persisted: identity},
				TargetService:   target,
				ReplacementUnit: "[Service]\nUser=" + identity.RequestedUser + "\nGroup=" + identity.RequestedGroup + "\n",
				StageGeneration: func(context.Context) error {
					close(stageEntered)
					<-releaseStage
					return nil
				},
				ops: &ops,
			},
			io.Discard,
		)
		migrationDone <- err
	}()
	select {
	case <-stageEntered:
	case err := <-migrationDone:
		t.Fatalf("identity migration before generation stage: %v", err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for identity generation stage")
	}

	writerAttempted := make(chan struct{})
	writerAcquired := make(chan struct{})
	fixture.server.tailscaleResolverRecovery.afterBlockLock = func() {
		close(writerAcquired)
	}
	blockDone := make(chan error, 1)
	go func() {
		close(writerAttempted)
		blockDone <- fixture.server.blockTailscaleResolverRecovery(errors.New("block during identity migration"))
	}()
	awaitResolverTestSignal(t, writerAttempted, "resolver block attempt during identity migration")
	select {
	case <-writerAcquired:
		t.Fatal("resolver block acquired the global guard during identity migration")
	case <-time.After(25 * time.Millisecond):
	}
	releaseStage <- struct{}{}
	if err := awaitResolverTestResult(t, migrationDone); err != nil {
		t.Fatalf("identity migration: %v", err)
	}
	if err := awaitResolverTestResult(t, blockDone); !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
		t.Fatalf("resolver block after identity migration = %v", err)
	}
}

func TestTailscaleResolverReadinessGatesServiceRootCommit(t *testing.T) {
	server := newTestServer(t)
	systemctlLog := installFakeSystemctl(t)
	service := db.Service{
		Name:        "root-readiness",
		ServiceType: db.ServiceTypeSystemd,
		ServiceRoot: filepath.Join(server.cfg.ServicesRoot, "root-readiness"),
		TSNet:       &db.TailscaleNetwork{Interface: "ts0", Version: "1.92.3"},
		Generation:  1,
	}

	err := applyServiceRootMigrationRuntimeChangesForConfigs(
		context.Background(),
		server.cfg,
		server.cfg,
		service,
		service,
		bytes.NewBuffer(nil),
	)
	if err == nil || !strings.Contains(err.Error(), "resolver") {
		t.Fatalf("service-root activation error = %v, want resolver readiness rejection", err)
	}
	if raw, readErr := os.ReadFile(systemctlLog); readErr == nil && len(bytes.TrimSpace(raw)) != 0 {
		t.Fatalf("service-root rejection invoked systemctl: %s", raw)
	} else if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read systemctl log: %v", readErr)
	}
}

func TestTailscaleResolverCanonicalReadinessRejectsNewDropInBeforeActivation(t *testing.T) {
	fixture := newTailscaleResolverPlanFixture(
		t,
		"canonical-drop-in",
		tailscaleResolverGenerationCurrent,
		"",
	)
	guardTailscaleResolverFixture(t, fixture)

	oldDropIns := tailscaleResolverUnitDropInPaths
	inspections := 0
	tailscaleResolverUnitDropInPaths = func(_ context.Context, unit string) ([]string, error) {
		if unit != "yeet-canonical-drop-in-ts.service" {
			t.Fatalf("drop-in unit = %q, want yeet-canonical-drop-in-ts.service", unit)
		}
		inspections++
		if inspections == 1 {
			return nil, nil
		}
		return []string{"/private/operator/late-canonical.conf"}, nil
	}
	t.Cleanup(func() { tailscaleResolverUnitDropInPaths = oldDropIns })

	activationCalls := 0
	oldSystemctl := catchSystemctl
	catchSystemctl = func(args ...string) error {
		activationCalls++
		t.Fatalf("canonical readiness invoked activation systemctl %q", args)
		return nil
	}
	t.Cleanup(func() { catchSystemctl = oldSystemctl })

	err := fixture.server.checkTailscaleResolverCanonicalReady(
		context.Background(),
		fixture.service,
	)
	if err == nil || !strings.Contains(err.Error(), "effective systemd drop-ins") {
		t.Fatalf("canonical readiness error = %v, want newly added drop-in rejection", err)
	}
	if inspections != 2 {
		t.Fatalf("canonical readiness drop-in inspections = %d, want initial and immediate checks", inspections)
	}
	if activationCalls != 0 {
		t.Fatalf("canonical readiness activation calls = %d, want zero", activationCalls)
	}
}

func TestTailscaleResolverReadinessRejectsRootMigrationBeforeInstallOrCommit(t *testing.T) {
	fixture := newTailscaleResolverPlanFixture(
		t,
		"root-transaction-readiness",
		tailscaleResolverGenerationCurrent,
		"",
	)
	useTestSystemdSystemDir(t)
	withServiceSetRootStopped(t)
	installCalls := 0
	withServiceSetRootSystemdInstall(
		t,
		func(*Server, *db.Service, *db.Service, string) error {
			installCalls++
			return nil
		},
	)
	oldRoot := fixture.service.ServiceRoot
	newRoot := filepath.Join(t.TempDir(), "migrated")

	err := fixture.server.migrateServiceRoot(
		fixture.service.Name,
		serviceRootMigrationRequest{Root: newRoot},
		serviceRootMigrationCopy,
	)
	if err == nil || !strings.Contains(err.Error(), "resolver") {
		t.Fatalf("root migration error = %v, want resolver readiness rejection", err)
	}
	if installCalls != 0 {
		t.Fatalf("rejected root migration install calls = %d, want 0", installCalls)
	}
	service, readErr := fixture.server.serviceView(fixture.service.Name)
	if readErr != nil {
		t.Fatalf("read service after rejected root migration: %v", readErr)
	}
	if got := fixture.server.serviceRootFromView(service); filepath.Clean(got) != filepath.Clean(oldRoot) {
		t.Fatalf("rejected root migration committed root %q, want %q", got, oldRoot)
	}
}

func TestTailscaleResolverReadinessLinearizesRootMigrationWithGlobalBlock(t *testing.T) {
	fixture := newGuardedTailscaleResolverFixture(
		t,
		"root-transaction-linearized",
		tailscaleResolverGenerationCurrent,
	)
	useTestSystemdSystemDir(t)
	withServiceSetRootStopped(t)
	installEntered := make(chan struct{})
	releaseInstall := make(chan struct{}, 1)
	t.Cleanup(func() {
		select {
		case releaseInstall <- struct{}{}:
		default:
		}
	})
	withServiceSetRootSystemdInstall(
		t,
		func(*Server, *db.Service, *db.Service, string) error {
			close(installEntered)
			<-releaseInstall
			return nil
		},
	)
	migrationDone := make(chan error, 1)
	go func() {
		migrationDone <- fixture.server.migrateServiceRoot(
			fixture.service.Name,
			serviceRootMigrationRequest{Root: filepath.Join(t.TempDir(), "migrated")},
			serviceRootMigrationCopy,
		)
	}()
	select {
	case <-installEntered:
	case err := <-migrationDone:
		t.Fatalf("root migration before install: %v", err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for root migration install")
	}

	writerAttempted := make(chan struct{})
	writerAcquired := make(chan struct{})
	fixture.server.tailscaleResolverRecovery.afterBlockLock = func() {
		close(writerAcquired)
	}
	blockDone := make(chan error, 1)
	go func() {
		close(writerAttempted)
		blockDone <- fixture.server.blockTailscaleResolverRecovery(errors.New("block during root migration"))
	}()
	awaitResolverTestSignal(t, writerAttempted, "resolver block attempt during root migration")
	select {
	case <-writerAcquired:
		t.Fatal("resolver block acquired the global guard during root migration")
	case <-time.After(25 * time.Millisecond):
	}
	releaseInstall <- struct{}{}
	if err := awaitResolverTestResult(t, migrationDone); err == nil || !strings.Contains(err.Error(), "resolver") {
		t.Fatalf("root migration after stubbed install = %v, want post-install resolver rejection", err)
	}
	if err := awaitResolverTestResult(t, blockDone); !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
		t.Fatalf("resolver block after root migration = %v", err)
	}
}

func TestTailscaleResolverReadinessAcceptsHistoricalAndCurrentGuardedTuples(t *testing.T) {
	for _, layout := range []tailscaleResolverGenerationLayout{
		tailscaleResolverGenerationHistorical,
		tailscaleResolverGenerationCurrent,
	} {
		t.Run(string(layout), func(t *testing.T) {
			fixture := newTailscaleResolverPlanFixture(t, "ready-"+string(layout), layout, "")
			guardTailscaleResolverFixture(t, fixture)
			runner := &recordingServiceRunner{}
			execer := &ttyExecer{
				ctx: context.Background(), s: fixture.server, sn: fixture.service.Name,
				rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
				serviceRunnerFn: func() (ServiceRunner, error) { return runner, nil },
			}

			if err := execer.startCmdFunc(); err != nil {
				t.Fatalf("guarded %s start: %v", layout, err)
			}
			if !reflect.DeepEqual(runner.calls, []string{"start"}) {
				t.Fatalf("guarded %s runner calls = %v, want [start]", layout, runner.calls)
			}
		})
	}
}

func guardTailscaleResolverFixture(t *testing.T, fixture tailscaleResolverPlanFixture) {
	t.Helper()
	guarded, changed, err := ensureTailscaleUnitResolverIsolation(
		fixture.unit,
		fixture.server.catchRunnerPath(),
	)
	if err != nil {
		t.Fatalf("guard fixture unit: %v", err)
	}
	if !changed {
		t.Fatal("direct fixture unit unexpectedly already guarded")
	}
	for _, path := range []string{fixture.canonical, fixture.installed} {
		if err := os.WriteFile(path, []byte(guarded), 0o640); err != nil {
			t.Fatalf("write guarded fixture unit %s: %v", path, err)
		}
	}
}

func newGuardedTailscaleResolverFixture(
	t *testing.T,
	name string,
	layout tailscaleResolverGenerationLayout,
) tailscaleResolverPlanFixture {
	t.Helper()
	fixture := newTailscaleResolverPlanFixture(t, name, layout, "")
	guardTailscaleResolverFixture(t, fixture)
	return fixture
}
