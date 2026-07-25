// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

type tailscaleResolverFleetTransactionFixture struct {
	server    *Server
	plan      tailscaleResolverFleetPlan
	active    map[string]bool
	originals map[string][]byte
	proofs    map[string]serviceIdentityPathProof
}

func TestReconcileTailscaleResolverIsolationUsesFleetTransaction(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
	defer restore()
	realSystemctl := catchSystemctl
	reloads := 0
	catchSystemctl = func(args ...string) error {
		if reflect.DeepEqual(args, []string{"daemon-reload"}) {
			reloads++
		}
		return realSystemctl(args...)
	}
	t.Cleanup(func() { catchSystemctl = realSystemctl })

	if err := fixture.server.reconcileTailscaleResolverIsolation(context.Background()); err != nil {
		t.Fatalf("reconcileTailscaleResolverIsolation: %v", err)
	}
	if reloads != 1 {
		t.Fatalf("daemon reloads = %d, want one fleet reload", reloads)
	}
	for _, service := range fixture.plan.Services {
		if got := fixture.active[service.UnitName]; got != service.WasActive {
			t.Fatalf("active state for %s = %v, want %v", service.UnitName, got, service.WasActive)
		}
		for _, file := range service.Files {
			raw, err := os.ReadFile(file.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(raw, file.Next) {
				t.Fatalf("reconciled bytes for %s diverged from fleet plan", file.Path)
			}
		}
	}
	if journals := tailscaleResolverJournalPaths(t, fixture.server); len(journals) != 0 {
		t.Fatalf("reconcile retained journals: %q", journals)
	}
}

func TestTailscaleResolverFleetTransactionRollsBackEveryWriteFailure(t *testing.T) {
	for failAt := 1; failAt <= 6; failAt++ {
		t.Run(fmt.Sprintf("write-%d", failAt), func(t *testing.T) {
			fixture := newTailscaleResolverFleetTransactionFixture(t)
			restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
			defer restore()
			realWrite := tailscaleResolverWriteManagedFile
			calls := 0
			tailscaleResolverWriteManagedFile = func(
				root, relative string,
				expected serviceIdentityPathProof,
				content []byte,
			) (serviceIdentityPathProof, error) {
				calls++
				if calls == failAt {
					return serviceIdentityPathProof{}, errors.New("injected unit write failure")
				}
				return realWrite(root, relative, expected, content)
			}
			t.Cleanup(func() { tailscaleResolverWriteManagedFile = realWrite })

			err := fixture.server.applyTailscaleResolverIsolationFleet(context.Background(), fixture.plan)
			if err == nil || !strings.Contains(err.Error(), "injected unit write failure") {
				t.Fatalf("applyTailscaleResolverIsolationFleet error = %v, want injected write failure", err)
			}
			fixture.assertOriginalFleet(t)
		})
	}
}

func TestTailscaleResolverFleetTransactionRollsBackDaemonReloadFailure(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
	defer restore()
	realSystemctl := catchSystemctl
	reloads := 0
	catchSystemctl = func(args ...string) error {
		if reflect.DeepEqual(args, []string{"daemon-reload"}) {
			reloads++
			if reloads == 1 {
				return errors.New("injected daemon reload failure")
			}
		}
		return realSystemctl(args...)
	}
	t.Cleanup(func() { catchSystemctl = realSystemctl })

	err := fixture.server.applyTailscaleResolverIsolationFleet(context.Background(), fixture.plan)
	if err == nil || !strings.Contains(err.Error(), "injected daemon reload failure") {
		t.Fatalf("applyTailscaleResolverIsolationFleet error = %v, want injected reload failure", err)
	}
	fixture.assertOriginalFleet(t)
}

func TestTailscaleResolverFleetTransactionRollsBackFirstMiddleAndLastVerificationFailure(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("verification-%d", failAt), func(t *testing.T) {
			fixture := newTailscaleResolverFleetTransactionFixture(t)
			for unit := range fixture.active {
				fixture.active[unit] = true
			}
			for i := range fixture.plan.Services {
				fixture.plan.Services[i].WasActive = true
			}
			restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
			defer restore()
			realVerify := verifyTailscaleSystemdSidecar
			verifications := 0
			verifyTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error {
				verifications++
				if verifications == failAt {
					return errors.New("injected sidecar verification failure")
				}
				return nil
			}
			t.Cleanup(func() { verifyTailscaleSystemdSidecar = realVerify })

			err := fixture.server.applyTailscaleResolverIsolationFleet(context.Background(), fixture.plan)
			if err == nil || !strings.Contains(err.Error(), "injected sidecar verification failure") {
				t.Fatalf("applyTailscaleResolverIsolationFleet error = %v, want injected verification failure", err)
			}
			fixture.assertOriginalFleet(t)
		})
	}
}

func TestTailscaleResolverFleetTransactionPreservesExactActiveAndInactiveState(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
	defer restore()

	if err := fixture.server.applyTailscaleResolverIsolationFleet(context.Background(), fixture.plan); err != nil {
		t.Fatalf("applyTailscaleResolverIsolationFleet: %v", err)
	}
	for _, service := range fixture.plan.Services {
		if got := fixture.active[service.UnitName]; got != service.WasActive {
			t.Fatalf("active state for %s = %v, want %v", service.UnitName, got, service.WasActive)
		}
		for _, file := range service.Files {
			raw, err := os.ReadFile(file.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(raw, file.Next) {
				t.Fatalf("committed bytes for %s diverged from plan", file.Path)
			}
			proof, err := captureServiceIdentityPathProof(file.Path)
			if err != nil {
				t.Fatal(err)
			}
			want := serviceIdentityDesiredFileState(
				file.Path,
				file.Next,
				file.Proof.Mode,
				file.Proof.UID,
				file.Proof.GID,
			)
			if !serviceIdentityPathMatchesState(proof, want) {
				t.Fatalf("committed metadata for %s = %#v, want %#v", file.Path, proof, want)
			}
		}
	}
	if journals := tailscaleResolverJournalPaths(t, fixture.server); len(journals) != 0 {
		t.Fatalf("committed transaction retained journals: %q", journals)
	}
}

func TestTailscaleResolverFleetTransactionRevalidatesBeforeFirstWrite(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
	defer restore()
	if err := os.WriteFile(fixture.plan.Services[1].Files[0].Path, []byte("stale\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	realWrite := tailscaleResolverWriteManagedFile
	writes := 0
	tailscaleResolverWriteManagedFile = func(
		root, relative string,
		expected serviceIdentityPathProof,
		content []byte,
	) (serviceIdentityPathProof, error) {
		writes++
		return realWrite(root, relative, expected, content)
	}
	t.Cleanup(func() { tailscaleResolverWriteManagedFile = realWrite })

	err := fixture.server.applyTailscaleResolverIsolationFleet(context.Background(), fixture.plan)
	if err == nil || !strings.Contains(err.Error(), "stale tailscale resolver fleet plan") {
		t.Fatalf("applyTailscaleResolverIsolationFleet error = %v, want stale-plan rejection", err)
	}
	if writes != 0 {
		t.Fatalf("managed writes = %d, want zero", writes)
	}
	if journals := tailscaleResolverJournalPaths(t, fixture.server); len(journals) != 0 {
		t.Fatalf("stale plan created journals: %q", journals)
	}
}

func TestTailscaleResolverFleetTransactionBlocksMutationsWhenJournalCreationFails(t *testing.T) {
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
	defer restore()
	realSync := tailscaleResolverJournalSync
	tailscaleResolverJournalSync = func(*os.File) error {
		return errors.New("injected journal header sync failure")
	}
	t.Cleanup(func() {
		tailscaleResolverJournalSync = realSync
	})

	err := fixture.server.applyTailscaleResolverIsolationFleet(context.Background(), fixture.plan)
	if err == nil || !strings.Contains(err.Error(), "injected journal header sync failure") {
		t.Fatalf("applyTailscaleResolverIsolationFleet error = %v, want journal sync failure", err)
	}
	if !errors.Is(err, errTailscaleResolverRecoveryBlocked) {
		t.Fatalf("apply error = %v, want recovery mutation block", err)
	}
	if allowed := fixture.server.checkTailscaleResolverMutationAllowed(); !errors.Is(
		allowed,
		errTailscaleResolverRecoveryBlocked,
	) {
		t.Fatalf("mutation block = %v, want resolver recovery block", allowed)
	}
	if journals := tailscaleResolverJournalPaths(t, fixture.server); len(journals) != 1 {
		t.Fatalf("failed creation journals = %q, want one retained for recovery", journals)
	}
	tailscaleResolverJournalSync = realSync
	if err := fixture.server.recoverTailscaleResolverIsolation(context.Background()); err != nil {
		t.Fatalf("recover failed journal creation: %v", err)
	}
	fixture.assertOriginalFleet(t)
}

func testTailscaleResolverReconcileMissingBind(t *testing.T) {
	t.Helper()
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	reconcileTailscaleResolverFixtureSuccessfully(t, fixture)
}

func testTailscaleResolverReconcileConvergesUnits(t *testing.T) {
	t.Helper()
	fixture := newTailscaleResolverFleetTransactionFixtureForLayout(
		t,
		tailscaleResolverGenerationCurrent,
	)
	reconcileTailscaleResolverFixtureSuccessfully(t, fixture)
}

func testTailscaleResolverReconcileUsesStableRunner(t *testing.T) {
	t.Helper()
	fixture := newTailscaleResolverFleetTransactionFixtureForLayout(
		t,
		tailscaleResolverGenerationCurrent,
	)
	customCatchRoot := filepath.Join(fixture.server.cfg.ServicesRoot, "custom-catch-root")
	stableRunner := filepath.Join(customCatchRoot, "run", "catch")
	if err := os.MkdirAll(filepath.Dir(stableRunner), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stableRunner, []byte("stable catch runner\n"), 0o750); err != nil {
		t.Fatal(err)
	}
	addTestServices(t, fixture.server, db.Service{
		Name:        CatchService,
		ServiceRoot: customCatchRoot,
		Artifacts:   make(db.ArtifactStore),
	})
	previousExecutablePath := catchExecutablePath
	executableLookups := 0
	catchExecutablePath = func() (string, error) {
		executableLookups++
		return filepath.Join(t.TempDir(), "versioned-catch"), errors.New("versioned executable discovery must not run")
	}
	t.Cleanup(func() { catchExecutablePath = previousExecutablePath })
	restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
	defer restore()
	if err := fixture.server.reconcileTailscaleResolverIsolation(context.Background()); err != nil {
		t.Fatalf("reconcileTailscaleResolverIsolation: %v", err)
	}
	if executableLookups != 0 {
		t.Fatalf("versioned Catch executable lookups = %d, want zero", executableLookups)
	}
	for _, service := range fixture.plan.Services {
		for _, file := range service.Files {
			raw, err := os.ReadFile(file.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "ExecStart="+stableRunner+" tailscale-resolver-exec ") {
				t.Fatalf("reconciled unit %s does not use stable configured Catch runner %s", file.Path, stableRunner)
			}
		}
	}
}

func testTailscaleResolverReconcileMigratesHistoricalRunner(t *testing.T) {
	t.Helper()
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	reconcileTailscaleResolverFixtureSuccessfully(t, fixture)
	for _, service := range fixture.plan.Services {
		for _, file := range service.Files {
			raw, err := os.ReadFile(file.Path)
			if err != nil {
				t.Fatal(err)
			}
			unit, err := parseTailscaleResolverUnit(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			if unit.daemon != service.Generation.Daemon ||
				unit.guardRunner != fixture.server.catchRunnerPath() {
				t.Fatalf("historical resolver unit = %#v, want daemon %q and stable guard", unit, service.Generation.Daemon)
			}
		}
	}
}

func testTailscaleResolverReconcileUsesDefaultServiceRoot(t *testing.T) {
	t.Helper()
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	dv, err := fixture.server.getDB()
	if err != nil {
		t.Fatal(err)
	}
	service := *dv.Services().Get("alpha").AsStruct()
	service.ServiceRoot = ""
	replaceTailscaleResolverPlanService(t, fixture.server, service)
	reconcileTailscaleResolverFixtureSuccessfully(t, fixture)
}

func testTailscaleResolverReconcileRejectsDivergentArguments(t *testing.T) {
	t.Helper()
	fixture := newTailscaleResolverPlanFixtureWithMutation(
		t,
		"api",
		tailscaleResolverGenerationCurrent,
		func(tuple *tailscaleResolverTuple, _ string) {
			tuple.args[3] = "--tun=wrong"
		},
	)
	assertTailscaleResolverReconcileRejected(t, fixture.server, "exact managed generation")
}

func testTailscaleResolverReconcileRejectsMultipleExecStarts(t *testing.T) {
	t.Helper()
	fixture := newTailscaleResolverPlanFixture(
		t,
		"api",
		tailscaleResolverGenerationCurrent,
		"",
	)
	for _, path := range []string{fixture.canonical, fixture.installed} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		raw = []byte(strings.Replace(
			string(raw),
			"ExecStart=",
			"ExecStart=/bin/false\nExecStart=",
			1,
		))
		if err := os.WriteFile(path, raw, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	assertTailscaleResolverReconcileRejected(t, fixture.server, "require exactly one [Service] ExecStart")
}

func testTailscaleResolverReconcileRejectsUnsafeExecStart(t *testing.T) {
	t.Helper()
	fixture := newTailscaleResolverPlanFixtureWithMutation(
		t,
		"api",
		tailscaleResolverGenerationCurrent,
		func(tuple *tailscaleResolverTuple, _ string) {
			tuple.daemon = "/tmp/tailscaled"
		},
	)
	assertTailscaleResolverReconcileRejected(t, fixture.server, "exact managed generation")
}

func testTailscaleResolverReconcileRejectsMissingUnit(t *testing.T) {
	t.Helper()
	fixture := newTailscaleResolverPlanFixture(
		t,
		"api",
		tailscaleResolverGenerationCurrent,
		"",
	)
	if err := os.Remove(fixture.installed); err != nil {
		t.Fatal(err)
	}
	assertTailscaleResolverReconcileRejected(t, fixture.server, "is missing")
}

func testTailscaleResolverReconcileRejectsDivergentInstalledDaemon(t *testing.T) {
	t.Helper()
	fixture := newTailscaleResolverPlanFixture(
		t,
		"api",
		tailscaleResolverGenerationCurrent,
		"",
	)
	raw := strings.Replace(fixture.unit, "/bin/tailscaled", "/run/tailscaled", 1)
	if err := os.WriteFile(fixture.installed, []byte(raw), 0o640); err != nil {
		t.Fatal(err)
	}
	assertTailscaleResolverReconcileRejected(t, fixture.server, "diverges from canonical")
}

func testTailscaleResolverReconcileRejectsMixedLayout(t *testing.T) {
	t.Helper()
	fixture := newTailscaleResolverPlanFixtureWithMutation(
		t,
		"api",
		tailscaleResolverGenerationHistorical,
		func(tuple *tailscaleResolverTuple, root string) {
			tuple.daemon = filepath.Join(root, "bin", "tailscaled")
		},
	)
	assertTailscaleResolverReconcileRejected(t, fixture.server, "exact managed generation")
}

func testTailscaleResolverReconcileMigratesInactiveFleetWithOneReloadAndNoStarts(t *testing.T) {
	t.Helper()
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	for unit := range fixture.active {
		fixture.active[unit] = false
	}
	for i := range fixture.plan.Services {
		fixture.plan.Services[i].WasActive = false
	}
	restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
	defer restore()
	stubSystemctl := catchSystemctl
	stubStart := startTailscaleSystemdSidecar
	stubRestart := restartTailscaleSystemdSidecar
	reloads := 0
	starts := 0
	catchSystemctl = func(args ...string) error {
		if reflect.DeepEqual(args, []string{"daemon-reload"}) {
			reloads++
		}
		return stubSystemctl(args...)
	}
	startTailscaleSystemdSidecar = func(ctx context.Context, service *svc.SystemdService) error {
		starts++
		return stubStart(ctx, service)
	}
	restartTailscaleSystemdSidecar = func(ctx context.Context, service *svc.SystemdService) error {
		starts++
		return stubRestart(ctx, service)
	}
	if err := fixture.server.reconcileTailscaleResolverIsolation(context.Background()); err != nil {
		t.Fatalf("reconcileTailscaleResolverIsolation: %v", err)
	}
	if reloads != 1 {
		t.Fatalf("inactive fleet daemon reloads = %d, want one", reloads)
	}
	if starts != 0 {
		t.Fatalf("inactive fleet start/restart calls = %d, want zero", starts)
	}
	for _, service := range fixture.plan.Services {
		if fixture.active[service.UnitName] {
			t.Fatalf("inactive sidecar %s was started", service.UnitName)
		}
		for _, file := range service.Files {
			raw, err := os.ReadFile(file.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(raw, file.Next) {
				t.Fatalf("inactive fleet bytes for %s diverged from plan", file.Path)
			}
		}
	}
}

func testTailscaleResolverReconcileRejectsAliasedUnitPaths(t *testing.T) {
	t.Helper()
	fixture := newTailscaleResolverPlanFixture(
		t,
		"api",
		tailscaleResolverGenerationCurrent,
		"",
	)
	fixture.service.Artifacts[db.ArtifactTSService].Refs[db.Gen(fixture.service.Generation)] = fixture.installed
	replaceTailscaleResolverPlanService(t, fixture.server, fixture.service)
	assertTailscaleResolverReconcileRejected(t, fixture.server, "distinct")
}

func testTailscaleResolverReconcileRollsBackPartialWrite(t *testing.T) {
	t.Helper()
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
	defer restore()
	realWrite := tailscaleResolverWriteManagedFile
	calls := 0
	tailscaleResolverWriteManagedFile = func(
		root, relative string,
		expected serviceIdentityPathProof,
		content []byte,
	) (serviceIdentityPathProof, error) {
		calls++
		if calls == 2 {
			return serviceIdentityPathProof{}, errors.New("injected partial write failure")
		}
		return realWrite(root, relative, expected, content)
	}
	t.Cleanup(func() { tailscaleResolverWriteManagedFile = realWrite })
	if err := fixture.server.reconcileTailscaleResolverIsolation(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "partial write failure") {
		t.Fatalf("reconcile error = %v, want partial write failure", err)
	}
	fixture.assertOriginalFleet(t)
}

func testTailscaleResolverReconcileSafeFleetIsNoop(t *testing.T) {
	t.Helper()
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	reconcileTailscaleResolverFixtureSuccessfully(t, fixture)
	restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
	defer restore()
	realSystemctl := catchSystemctl
	calls := 0
	catchSystemctl = func(args ...string) error {
		calls++
		return realSystemctl(args...)
	}
	t.Cleanup(func() { catchSystemctl = realSystemctl })
	if err := fixture.server.reconcileTailscaleResolverIsolation(context.Background()); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if calls != 0 {
		t.Fatalf("safe fleet systemctl calls = %d, want zero", calls)
	}
}

func testTailscaleResolverReconcileRejectsMissingNetworkNamespace(t *testing.T) {
	t.Helper()
	fixture := newTailscaleResolverPlanFixture(
		t,
		"api",
		tailscaleResolverGenerationCurrent,
		"",
	)
	for _, path := range []string{fixture.canonical, fixture.installed} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(raw), "\n")
		var kept []string
		for _, line := range lines {
			if !strings.HasPrefix(line, "NetworkNamespacePath=") {
				kept = append(kept, line)
			}
		}
		if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	assertTailscaleResolverReconcileRejected(t, fixture.server, "no network namespace")
}

func testTailscaleResolverReconcileRollsBackDaemonReloadFailure(t *testing.T) {
	t.Helper()
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
	defer restore()
	realSystemctl := catchSystemctl
	reloads := 0
	catchSystemctl = func(args ...string) error {
		if reflect.DeepEqual(args, []string{"daemon-reload"}) {
			reloads++
			if reloads == 1 {
				return errors.New("injected daemon reload failure")
			}
		}
		return realSystemctl(args...)
	}
	t.Cleanup(func() { catchSystemctl = realSystemctl })
	if err := fixture.server.reconcileTailscaleResolverIsolation(context.Background()); err == nil {
		t.Fatal("reconcile unexpectedly survived daemon reload failure")
	}
	fixture.assertOriginalFleet(t)
}

func testTailscaleResolverReconcileRollsBackRestartFailure(t *testing.T) {
	t.Helper()
	fixture := newTailscaleResolverFleetTransactionFixture(t)
	restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
	defer restore()
	realRestart := restartTailscaleSystemdSidecar
	restarts := 0
	restartTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error {
		restarts++
		if restarts == 1 {
			return errors.New("injected restart failure")
		}
		return nil
	}
	t.Cleanup(func() { restartTailscaleSystemdSidecar = realRestart })
	if err := fixture.server.reconcileTailscaleResolverIsolation(context.Background()); err == nil {
		t.Fatal("reconcile unexpectedly survived restart failure")
	}
	fixture.assertOriginalFleet(t)
}

func testTailscaleResolverReconcileSkipsNonTailscaleServices(t *testing.T) {
	t.Helper()
	server := newTestServer(t)
	addTestServices(t, server, db.Service{
		Name:        "api",
		ServiceType: db.ServiceTypeDockerCompose,
		Artifacts:   make(db.ArtifactStore),
	})
	realSystemctl := catchSystemctl
	catchSystemctl = func(args ...string) error {
		t.Fatalf("unexpected systemctl call: %v", args)
		return nil
	}
	t.Cleanup(func() { catchSystemctl = realSystemctl })
	if err := server.reconcileTailscaleResolverIsolation(context.Background()); err != nil {
		t.Fatalf("reconcile non-Tailscale service: %v", err)
	}
}

func reconcileTailscaleResolverFixtureSuccessfully(
	t *testing.T,
	fixture *tailscaleResolverFleetTransactionFixture,
) {
	t.Helper()
	restore := stubTailscaleResolverFleetLifecycle(t, fixture.active)
	defer restore()
	if err := fixture.server.reconcileTailscaleResolverIsolation(context.Background()); err != nil {
		t.Fatalf("reconcileTailscaleResolverIsolation: %v", err)
	}
	for _, service := range fixture.plan.Services {
		if fixture.active[service.UnitName] != service.WasActive {
			t.Fatalf("active state for %s = %v, want %v", service.UnitName, fixture.active[service.UnitName], service.WasActive)
		}
		for _, file := range service.Files {
			raw, err := os.ReadFile(file.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(raw, file.Next) {
				t.Fatalf("reconciled bytes for %s diverged from plan", file.Path)
			}
		}
	}
}

func assertTailscaleResolverReconcileRejected(t *testing.T, server *Server, want string) {
	t.Helper()
	realWrite := tailscaleResolverWriteManagedFile
	writes := 0
	tailscaleResolverWriteManagedFile = func(
		root, relative string,
		expected serviceIdentityPathProof,
		content []byte,
	) (serviceIdentityPathProof, error) {
		writes++
		return realWrite(root, relative, expected, content)
	}
	t.Cleanup(func() { tailscaleResolverWriteManagedFile = realWrite })
	err := server.reconcileTailscaleResolverIsolation(context.Background())
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("reconcileTailscaleResolverIsolation error = %v, want %q", err, want)
	}
	if writes != 0 {
		t.Fatalf("rejected reconcile performed %d managed writes", writes)
	}
}

func newTailscaleResolverFleetTransactionFixture(t *testing.T) *tailscaleResolverFleetTransactionFixture {
	return newTailscaleResolverFleetTransactionFixtureForLayout(
		t,
		tailscaleResolverGenerationHistorical,
	)
}

func newTailscaleResolverFleetTransactionFixtureForLayout(
	t *testing.T,
	layout tailscaleResolverGenerationLayout,
) *tailscaleResolverFleetTransactionFixture {
	t.Helper()
	stubTailscaleResolverJournalOwner(t)
	s := newTestServer(t)
	useTestSystemdSystemDir(t)
	active := map[string]bool{
		"yeet-alpha-ts.service":   true,
		"yeet-bravo-ts.service":   false,
		"yeet-charlie-ts.service": true,
	}
	stubTailscaleResolverActive(t, active)
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		addTailscaleResolverPlanService(
			t,
			s,
			name,
			layout,
			"",
			nil,
		)
	}
	dv, err := s.getDB()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.planTailscaleResolverIsolationFleet(context.Background(), dv)
	if err != nil {
		t.Fatalf("planTailscaleResolverIsolationFleet: %v", err)
	}
	fixture := &tailscaleResolverFleetTransactionFixture{
		server: s, plan: plan, active: active,
		originals: make(map[string][]byte),
		proofs:    make(map[string]serviceIdentityPathProof),
	}
	for serviceIndex := range plan.Services {
		for fileIndex := range plan.Services[serviceIndex].Files {
			file := &plan.Services[serviceIndex].Files[fileIndex]
			mode := os.FileMode(0o600 + serviceIndex*0o10 + fileIndex*0o4)
			if err := os.Chmod(file.Path, mode); err != nil {
				t.Fatal(err)
			}
			proof, err := captureServiceIdentityPathProof(file.Path)
			if err != nil {
				t.Fatal(err)
			}
			file.Proof = proof
			fixture.originals[file.Path] = append([]byte(nil), file.Original...)
			fixture.proofs[file.Path] = proof
		}
	}
	return fixture
}

func stubTailscaleResolverFleetLifecycle(t *testing.T, active map[string]bool) func() {
	t.Helper()
	realSystemctl := catchSystemctl
	realRestart := restartTailscaleSystemdSidecar
	realStart := startTailscaleSystemdSidecar
	realVerify := verifyTailscaleSystemdSidecar
	catchSystemctl = func(args ...string) error {
		if len(args) == 2 && args[0] == "stop" {
			active[args[1]] = false
		}
		return nil
	}
	restartTailscaleSystemdSidecar = func(_ context.Context, service *svc.SystemdService) error {
		active["yeet-"+service.Name()+"-ts.service"] = true
		return nil
	}
	startTailscaleSystemdSidecar = func(_ context.Context, service *svc.SystemdService) error {
		active["yeet-"+service.Name()+"-ts.service"] = true
		return nil
	}
	verifyTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error { return nil }
	return func() {
		catchSystemctl = realSystemctl
		restartTailscaleSystemdSidecar = realRestart
		startTailscaleSystemdSidecar = realStart
		verifyTailscaleSystemdSidecar = realVerify
	}
}

func (f *tailscaleResolverFleetTransactionFixture) assertOriginalFleet(t *testing.T) {
	t.Helper()
	for _, service := range f.plan.Services {
		if got := f.active[service.UnitName]; got != service.WasActive {
			t.Fatalf("active state for %s = %v, want original %v", service.UnitName, got, service.WasActive)
		}
		for _, file := range service.Files {
			raw, err := os.ReadFile(file.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(raw, f.originals[file.Path]) {
				t.Fatalf("rollback bytes for %s = %q, want %q", file.Path, raw, f.originals[file.Path])
			}
			proof, err := captureServiceIdentityPathProof(file.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !serviceIdentityPathStateEqual(proof, f.proofs[file.Path]) {
				t.Fatalf("rollback proof for %s = %#v, want payload %#v", file.Path, proof, f.proofs[file.Path])
			}
		}
	}
	if journals := tailscaleResolverJournalPaths(t, f.server); len(journals) != 0 {
		t.Fatalf("proven rollback retained journals: %q", journals)
	}
}

func stubTailscaleResolverJournalOwner(t *testing.T) {
	t.Helper()
	old := tailscaleResolverJournalOwnerUID
	tailscaleResolverJournalOwnerUID = uint32(os.Geteuid())
	t.Cleanup(func() { tailscaleResolverJournalOwnerUID = old })
}

func tailscaleResolverJournalPaths(t *testing.T, s *Server) []string {
	t.Helper()
	paths, err := discoverTailscaleResolverJournals(s.cfg.RootDir)
	if err != nil {
		t.Fatalf("discoverTailscaleResolverJournals: %v", err)
	}
	return paths
}
