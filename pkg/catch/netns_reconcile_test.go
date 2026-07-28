// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
	"tailscale.com/ipn"
	"tailscale.com/types/opt"
)

type fakeDockerNetNSReconciler struct {
	name      string
	reconcile func(context.Context) (bool, error)
}

func (f fakeDockerNetNSReconciler) ReconcileNetNS(ctx context.Context) (bool, error) {
	return f.reconcile(ctx)
}

func addTestServices(t *testing.T, s *Server, services ...db.Service) {
	t.Helper()
	for _, svc := range services {
		svc := svc
		if _, _, err := s.cfg.DB.MutateService(svc.Name, func(_ *db.Data, stored *db.Service) error {
			*stored = svc
			return nil
		}); err != nil {
			t.Fatalf("MutateService(%q): %v", svc.Name, err)
		}
	}
}

type safeLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureLogs(t *testing.T) *safeLogBuffer {
	t.Helper()
	buf := &safeLogBuffer{}
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	return buf
}

func waitForLogContains(t *testing.T, buf *safeLogBuffer, needle string) string {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		out := buf.String()
		if strings.Contains(out, needle) {
			return out
		}
		time.Sleep(10 * time.Millisecond)
	}
	return buf.String()
}

func stubDockerPrereqsInstaller(t *testing.T, f func(*Server) error) {
	t.Helper()
	prev := installDockerPrereqs
	installDockerPrereqs = f
	t.Cleanup(func() {
		installDockerPrereqs = prev
	})
}

func stubYeetDNSInstaller(t *testing.T, f func(string) error) {
	t.Helper()
	prev := installYeetDNSServiceForServer
	installYeetDNSServiceForServer = f
	t.Cleanup(func() {
		installYeetDNSServiceForServer = prev
	})
}

func TestReconcileNetNSBackedDockerServices(t *testing.T) {
	s := newTestServer(t)
	addTestServices(t, s,
		db.Service{
			Name:             "docker-netns",
			ServiceType:      db.ServiceTypeDockerCompose,
			Generation:       1,
			LatestGeneration: 1,
			Artifacts: db.ArtifactStore{
				db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/yeet-docker-netns-ns.service"}},
			},
		},
		db.Service{
			Name:             "docker-plain",
			ServiceType:      db.ServiceTypeDockerCompose,
			Generation:       1,
			LatestGeneration: 1,
		},
		db.Service{
			Name:             "systemd-netns",
			ServiceType:      db.ServiceTypeSystemd,
			Generation:       1,
			LatestGeneration: 1,
			Artifacts: db.ArtifactStore{
				db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/yeet-systemd-netns-ns.service"}},
			},
		},
	)

	var called []string
	s.newDockerComposeService = func(sv db.ServiceView) (dockerNetNSReconciler, error) {
		name := sv.Name()
		return fakeDockerNetNSReconciler{
			name: name,
			reconcile: func(context.Context) (bool, error) {
				called = append(called, name)
				return name == "docker-netns", nil
			},
		}, nil
	}

	if err := s.reconcileNetNSBackedDockerServices(context.Background()); err != nil {
		t.Fatalf("reconcileNetNSBackedDockerServices returned error: %v", err)
	}
	if diff := cmp.Diff([]string{"docker-netns"}, called); diff != "" {
		t.Fatalf("unexpected reconciled services (-want +got):\n%s", diff)
	}
}

func TestReconcileNetNSBackedDockerServicesRestartsTailscaleSidecar(t *testing.T) {
	fixture := newGuardedTailscaleResolverFixture(t, "docker-netns", tailscaleResolverGenerationCurrent)
	s := fixture.server
	if _, _, err := s.cfg.DB.MutateService("docker-netns", func(_ *db.Data, service *db.Service) error {
		service.ServiceType = db.ServiceTypeDockerCompose
		service.Artifacts[db.ArtifactNetNSService] = &db.Artifact{
			Refs: map[db.ArtifactRef]string{db.Gen(service.Generation): "/tmp/yeet-docker-netns-ns.service"},
		}
		return nil
	}); err != nil {
		t.Fatalf("add netns artifact: %v", err)
	}

	var calls []string
	s.newDockerComposeService = func(sv db.ServiceView) (dockerNetNSReconciler, error) {
		name := sv.Name()
		return fakeDockerNetNSReconciler{
			name: name,
			reconcile: func(context.Context) (bool, error) {
				calls = append(calls, "reconcile:"+name)
				return true, nil
			},
		}, nil
	}

	prevRestart := restartTailscaleSystemdSidecar
	restartTailscaleSystemdSidecar = func(_ context.Context, service *svc.SystemdService) error {
		calls = append(calls, "verified-restart:"+service.Name())
		return nil
	}
	t.Cleanup(func() {
		restartTailscaleSystemdSidecar = prevRestart
	})

	if err := s.reconcileNetNSBackedDockerServices(context.Background()); err != nil {
		t.Fatalf("reconcileNetNSBackedDockerServices returned error: %v", err)
	}
	want := []string{
		"reconcile:docker-netns",
		"verified-restart:docker-netns",
	}
	if diff := cmp.Diff(want, calls); diff != "" {
		t.Fatalf("unexpected reconciliation side effects (-want +got):\n%s", diff)
	}
}

func TestTailscaleResolverReadinessGatesAllStartupReconciliationRestarts(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T, tailscaleResolverPlanFixture) error
	}{
		{
			name: "DNS",
			run: func(t *testing.T, fixture tailscaleResolverPlanFixture) error {
				tuple := fixtureTuple(fixture.service, tailscaleResolverGenerationCurrent)
				unsafe := ipn.ConfigVAlpha{
					Version:  "alpha0",
					Hostname: ptrString(fixture.service.Name),
				}
				writeTailscaleTestConfig(t, tuple.configFile, unsafe)
				generationConfig := fixture.service.Artifacts[db.ArtifactTSConfig].Refs[db.Gen(fixture.service.Generation)]
				writeTailscaleTestConfig(t, generationConfig, unsafe)
				return fixture.server.reconcileTailscaleDNSConfigs(context.Background())
			},
		},
		{
			name: "mount",
			run: func(t *testing.T, fixture tailscaleResolverPlanFixture) error {
				oldActive := catchSystemdUnitActive
				catchSystemdUnitActive = func(string) bool { return true }
				t.Cleanup(func() { catchSystemdUnitActive = oldActive })
				oldVerify := verifyTailscaleSystemdSidecar
				verifyTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error {
					return errors.New("resolver mount drift")
				}
				t.Cleanup(func() { verifyTailscaleSystemdSidecar = oldVerify })
				return fixture.server.reconcileTailscaleResolverMounts(context.Background())
			},
		},
		{
			name: "netns",
			run: func(t *testing.T, fixture tailscaleResolverPlanFixture) error {
				netnsUnit := filepath.Join(fixture.service.ServiceRoot, "bin", "yeet-"+fixture.service.Name+"-ns.service")
				if err := os.WriteFile(netnsUnit, []byte("[Service]\n"), 0o640); err != nil {
					t.Fatal(err)
				}
				if _, _, err := fixture.server.cfg.DB.MutateService(
					fixture.service.Name,
					func(_ *db.Data, service *db.Service) error {
						service.Artifacts[db.ArtifactNetNSService] = &db.Artifact{
							Refs: map[db.ArtifactRef]string{db.Gen(service.Generation): netnsUnit},
						}
						return nil
					},
				); err != nil {
					t.Fatal(err)
				}
				fixture.server.newDockerComposeService = func(db.ServiceView) (dockerNetNSReconciler, error) {
					return fakeDockerNetNSReconciler{
						reconcile: func(context.Context) (bool, error) { return true, nil },
					}, nil
				}
				return fixture.server.reconcileNetNSBackedDockerServices(context.Background())
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTailscaleResolverPlanFixture(
				t,
				"reconcile-"+strings.ToLower(test.name),
				tailscaleResolverGenerationCurrent,
				"",
			)
			restarts := 0
			oldRestart := restartTailscaleSystemdSidecar
			restartTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error {
				restarts++
				return nil
			}
			t.Cleanup(func() { restartTailscaleSystemdSidecar = oldRestart })

			err := test.run(t, fixture)
			if err == nil || !strings.Contains(err.Error(), "resolver") {
				t.Fatalf("%s reconciliation error = %v, want resolver readiness rejection", test.name, err)
			}
			if restarts != 0 {
				t.Fatalf("%s reconciliation restart calls = %d, want 0", test.name, restarts)
			}
		})
	}
}

func TestRestartTailscaleSidecarForServiceUsesKeyedServiceLock(t *testing.T) {
	fixture := newGuardedTailscaleResolverFixture(
		t,
		"reconcile-lock",
		tailscaleResolverGenerationCurrent,
	)
	entered := make(chan struct{})
	releaseRestart := make(chan struct{})
	oldRestart := restartTailscaleSystemdSidecar
	restartTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error {
		close(entered)
		<-releaseRestart
		return nil
	}
	t.Cleanup(func() {
		restartTailscaleSystemdSidecar = oldRestart
		select {
		case <-releaseRestart:
		default:
			close(releaseRestart)
		}
	})
	releaseLock := fixture.server.serviceOperationLocks.Lock(fixture.service.Name)
	done := make(chan error, 1)
	go func() {
		done <- fixture.server.restartTailscaleSidecarForService(
			context.Background(),
			fixture.service.Name,
		)
	}()
	select {
	case <-entered:
		t.Fatal("reconciliation restart bypassed the keyed service lock")
	case <-time.After(25 * time.Millisecond):
	}
	releaseLock()
	awaitResolverTestSignal(t, entered, "serialized reconciliation restart")
	close(releaseRestart)
	if err := awaitResolverTestResult(t, done); err != nil {
		t.Fatalf("serialized reconciliation restart: %v", err)
	}
}

func TestReconcileNetNSBackedDockerServicesRepairsStaleTailscaleSidecar(t *testing.T) {
	fixture := newGuardedTailscaleResolverFixture(t, "docker-netns", tailscaleResolverGenerationCurrent)
	s := fixture.server
	if _, _, err := s.cfg.DB.MutateService("docker-netns", func(_ *db.Data, service *db.Service) error {
		service.ServiceType = db.ServiceTypeDockerCompose
		service.Artifacts[db.ArtifactNetNSService] = &db.Artifact{
			Refs: map[db.ArtifactRef]string{db.Gen(service.Generation): "/tmp/yeet-docker-netns-ns.service"},
		}
		return nil
	}); err != nil {
		t.Fatalf("add netns artifact: %v", err)
	}

	var calls []string
	s.newDockerComposeService = func(sv db.ServiceView) (dockerNetNSReconciler, error) {
		name := sv.Name()
		return fakeDockerNetNSReconciler{
			name: name,
			reconcile: func(context.Context) (bool, error) {
				calls = append(calls, "reconcile:"+name)
				return false, nil
			},
		}, nil
	}

	prevStale := tailscaleSidecarNetNSStale
	tailscaleSidecarNetNSStale = func(name string) (bool, error) {
		calls = append(calls, "stale-check:"+name)
		return true, nil
	}
	t.Cleanup(func() {
		tailscaleSidecarNetNSStale = prevStale
	})

	prevRestart := restartTailscaleSystemdSidecar
	restartTailscaleSystemdSidecar = func(_ context.Context, service *svc.SystemdService) error {
		calls = append(calls, "verified-restart:"+service.Name())
		return nil
	}
	t.Cleanup(func() {
		restartTailscaleSystemdSidecar = prevRestart
	})

	if err := s.reconcileNetNSBackedDockerServices(context.Background()); err != nil {
		t.Fatalf("reconcileNetNSBackedDockerServices returned error: %v", err)
	}
	want := []string{
		"reconcile:docker-netns",
		"stale-check:docker-netns",
		"verified-restart:docker-netns",
	}
	if diff := cmp.Diff(want, calls); diff != "" {
		t.Fatalf("unexpected reconciliation side effects (-want +got):\n%s", diff)
	}
}

func TestReconcileNetNSBackedDockerServicesSkipsCurrentTailscaleSidecar(t *testing.T) {
	s := newTestServer(t)
	addTestServices(t, s, db.Service{
		Name:             "docker-netns",
		ServiceType:      db.ServiceTypeDockerCompose,
		Generation:       1,
		LatestGeneration: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/yeet-docker-netns-ns.service"}},
			db.ArtifactTSService:    {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/yeet-docker-netns-ts.service"}},
		},
	})

	var calls []string
	s.newDockerComposeService = func(sv db.ServiceView) (dockerNetNSReconciler, error) {
		name := sv.Name()
		return fakeDockerNetNSReconciler{
			name: name,
			reconcile: func(context.Context) (bool, error) {
				calls = append(calls, "reconcile:"+name)
				return false, nil
			},
		}, nil
	}

	prevStale := tailscaleSidecarNetNSStale
	tailscaleSidecarNetNSStale = func(name string) (bool, error) {
		calls = append(calls, "stale-check:"+name)
		return false, nil
	}
	t.Cleanup(func() {
		tailscaleSidecarNetNSStale = prevStale
	})

	prevSystemctl := catchSystemctl
	catchSystemctl = func(args ...string) error {
		calls = append(calls, "systemctl:"+strings.Join(args, " "))
		return nil
	}
	t.Cleanup(func() {
		catchSystemctl = prevSystemctl
	})

	if err := s.reconcileNetNSBackedDockerServices(context.Background()); err != nil {
		t.Fatalf("reconcileNetNSBackedDockerServices returned error: %v", err)
	}
	want := []string{
		"reconcile:docker-netns",
		"stale-check:docker-netns",
	}
	if diff := cmp.Diff(want, calls); diff != "" {
		t.Fatalf("unexpected reconciliation side effects (-want +got):\n%s", diff)
	}
}

func TestReconcileTailscaleDNSConfigsDisablesDNSAndRestartsSidecar(t *testing.T) {
	fixture := newGuardedTailscaleResolverFixture(t, "api", tailscaleResolverGenerationCurrent)
	s := fixture.server
	tuple := fixtureTuple(fixture.service, tailscaleResolverGenerationCurrent)
	configPath := fixture.service.Artifacts[db.ArtifactTSConfig].Refs[db.Gen(fixture.service.Generation)]
	runtimeConfigPath := tuple.configFile
	writeTailscaleTestConfig(t, configPath, ipn.ConfigVAlpha{
		Version:  "alpha0",
		AuthKey:  ptrString("tskey-auth-test"),
		Hostname: ptrString("api"),
	})
	writeTailscaleTestConfig(t, runtimeConfigPath, ipn.ConfigVAlpha{
		Version:  "alpha0",
		AuthKey:  ptrString("tskey-auth-test"),
		Hostname: ptrString("api"),
	})
	addTestServices(t, s,
		db.Service{
			Name:             "plain",
			ServiceType:      db.ServiceTypeDockerCompose,
			Generation:       1,
			LatestGeneration: 1,
		},
	)

	var calls []string
	prevRestart := restartTailscaleSystemdSidecar
	restartTailscaleSystemdSidecar = func(_ context.Context, service *svc.SystemdService) error {
		calls = append(calls, "verified-restart:"+service.Name())
		return nil
	}
	t.Cleanup(func() {
		restartTailscaleSystemdSidecar = prevRestart
	})

	if err := s.reconcileTailscaleDNSConfigs(context.Background()); err != nil {
		t.Fatalf("reconcileTailscaleDNSConfigs returned error: %v", err)
	}

	cfg := readTailscaleTestConfig(t, configPath)
	if !cfg.AcceptDNS.EqualBool(false) {
		t.Fatalf("artifact AcceptDNS = %q, want explicit false", cfg.AcceptDNS)
	}
	runtimeCfg := readTailscaleTestConfig(t, runtimeConfigPath)
	if !runtimeCfg.AcceptDNS.EqualBool(false) {
		t.Fatalf("runtime AcceptDNS = %q, want explicit false", runtimeCfg.AcceptDNS)
	}
	if diff := cmp.Diff([]string{"verified-restart:api"}, calls); diff != "" {
		t.Fatalf("unexpected sidecar lifecycle calls (-want +got):\n%s", diff)
	}
}

func TestTailscaleDNSConfigPathsIncludesManagedAndLegacyRuntimeCopies(t *testing.T) {
	root := "/var/lib/yeet/services/api"
	artifact := filepath.Join(root, "tailscale", "tailscaled-3.json")
	service := &db.Service{
		Name:       "api",
		Generation: 3,
		Artifacts: db.ArtifactStore{
			db.ArtifactTSConfig: {Refs: map[db.ArtifactRef]string{db.Gen(3): artifact}},
		},
	}
	want := []string{
		artifact,
		filepath.Join(root, "env", "tailscaled.json"),
		filepath.Join(root, "run", "tailscaled.json"),
	}
	if diff := cmp.Diff(want, tailscaleDNSConfigPaths(service, root)); diff != "" {
		t.Fatalf("tailscale config paths mismatch (-want +got):\n%s", diff)
	}
}

func TestReconcileTailscaleDNSConfigsRepairsUnsafeRuntimeCopy(t *testing.T) {
	fixture := newGuardedTailscaleResolverFixture(t, "api", tailscaleResolverGenerationCurrent)
	s := fixture.server
	tuple := fixtureTuple(fixture.service, tailscaleResolverGenerationCurrent)
	configPath := fixture.service.Artifacts[db.ArtifactTSConfig].Refs[db.Gen(fixture.service.Generation)]
	runtimeConfigPath := tuple.configFile
	generationConfig := ipn.ConfigVAlpha{
		Version:   "alpha0",
		AuthKey:   ptrString("tskey-auth-test"),
		Hostname:  ptrString("api"),
		AcceptDNS: "true",
	}
	runtimeConfig := generationConfig
	runtimeConfig.AcceptDNS = ""
	writeTailscaleTestConfig(t, configPath, generationConfig)
	writeTailscaleTestConfig(t, runtimeConfigPath, runtimeConfig)
	var calls []string
	prevRestart := restartTailscaleSystemdSidecar
	restartTailscaleSystemdSidecar = func(_ context.Context, service *svc.SystemdService) error {
		calls = append(calls, "verified-restart:"+service.Name())
		return nil
	}
	t.Cleanup(func() {
		restartTailscaleSystemdSidecar = prevRestart
	})

	if err := s.reconcileTailscaleDNSConfigs(context.Background()); err != nil {
		t.Fatalf("reconcileTailscaleDNSConfigs returned error: %v", err)
	}

	cfg := readTailscaleTestConfig(t, configPath)
	if !cfg.AcceptDNS.EqualBool(false) {
		t.Fatalf("artifact AcceptDNS = %q, want explicit false", cfg.AcceptDNS)
	}
	runtimeCfg := readTailscaleTestConfig(t, runtimeConfigPath)
	if !runtimeCfg.AcceptDNS.EqualBool(false) {
		t.Fatalf("runtime AcceptDNS = %q, want explicit false", runtimeCfg.AcceptDNS)
	}
	if diff := cmp.Diff([]string{"verified-restart:api"}, calls); diff != "" {
		t.Fatalf("unexpected sidecar lifecycle calls (-want +got):\n%s", diff)
	}
}

func TestReconcileTailscaleDNSConfigsSkipsAlreadySafeConfig(t *testing.T) {
	s := newTestServer(t)
	root := filepath.Join(t.TempDir(), "services", "api")
	configPath := filepath.Join(root, "tailscale", "tailscaled-3.json")
	runtimeConfigPath := filepath.Join(serviceRunDirForRoot(root), "tailscaled.json")
	cfg := tailscaleConfig("api", "tskey-auth-test", "")
	writeTailscaleTestConfig(t, configPath, cfg)
	writeTailscaleTestConfig(t, runtimeConfigPath, cfg)
	addTestServices(t, s, db.Service{
		Name:             "api",
		ServiceType:      db.ServiceTypeDockerCompose,
		ServiceRoot:      root,
		Generation:       3,
		LatestGeneration: 3,
		Artifacts: db.ArtifactStore{
			db.ArtifactTSConfig:  {Refs: map[db.ArtifactRef]string{db.Gen(3): configPath}},
			db.ArtifactTSService: {Refs: map[db.ArtifactRef]string{db.Gen(3): "/tmp/yeet-api-ts.service"}},
		},
	})

	prevSystemctl := catchSystemctl
	catchSystemctl = func(args ...string) error {
		t.Fatalf("unexpected systemctl call: %v", args)
		return nil
	}
	t.Cleanup(func() {
		catchSystemctl = prevSystemctl
	})

	if err := s.reconcileTailscaleDNSConfigs(context.Background()); err != nil {
		t.Fatalf("reconcileTailscaleDNSConfigs returned error: %v", err)
	}
}

func TestReconcileTailscaleResolverIsolationRepairsMissingBind(t *testing.T) {
	testTailscaleResolverReconcileMissingBind(t)
}

func TestReconcileTailscaleResolverIsolationConvergesCanonicalArtifactAndInstalledUnit(t *testing.T) {
	testTailscaleResolverReconcileConvergesUnits(t)
}

func TestReconcileTailscaleResolverIsolationUsesStableConfiguredCatchRunnerWhenExecutableIsVersioned(t *testing.T) {
	testTailscaleResolverReconcileUsesStableRunner(t)
}

func TestReconcileTailscaleResolverIsolationMigratesHistoricalTailscaledRunner(t *testing.T) {
	testTailscaleResolverReconcileMigratesHistoricalRunner(t)
}

func TestReconcileTailscaleResolverIsolationUsesDefaultServiceRoot(t *testing.T) {
	testTailscaleResolverReconcileUsesDefaultServiceRoot(t)
}

func TestReconcileTailscaleResolverIsolationRejectsDivergentInstalledArgumentsWithoutWriting(t *testing.T) {
	testTailscaleResolverReconcileRejectsDivergentArguments(t)
}

func TestReconcileTailscaleResolverIsolationRejectsMultipleExecStartsBeforeWriting(t *testing.T) {
	testTailscaleResolverReconcileRejectsMultipleExecStarts(t)
}

func TestParseTailscaleResolverUnitRejectsInvalidStrictGrammar(t *testing.T) {
	const (
		namespace   = "NetworkNamespacePath=/var/run/netns/yeet-api-ns\n"
		environment = "EnvironmentFile=/srv/api/env/tailscaled.env\n"
		working     = "WorkingDirectory=/srv/api/tailscale\n"
		execStart   = "ExecStart=/srv/api/bin/tailscaled\n"
	)
	for _, tt := range []struct {
		name string
		unit string
		want string
	}{
		{
			name: "exec start outside service section",
			unit: "[Unit]\n" + execStart + "[Service]\n" + namespace,
			want: "ExecStart outside [Service]",
		},
		{
			name: "duplicate network namespace",
			unit: "[Service]\n" + execStart + namespace +
				"NetworkNamespacePath=/var/run/netns/yeet-other-ns\n",
			want: "multiple NetworkNamespacePath",
		},
		{
			name: "relative network namespace",
			unit: "[Service]\n" + execStart +
				"NetworkNamespacePath=var/run/netns/yeet-api-ns\n",
			want: "invalid NetworkNamespacePath",
		},
		{
			name: "empty exec start",
			unit: "[Service]\nExecStart=\n" + namespace,
			want: "ExecStart executable must be an absolute clean path",
		},
		{
			name: "relative guarded resolver source",
			unit: "[Service]\n" +
				"ExecStart=/srv/catch/run/catch tailscale-resolver-exec --source etc/netns/yeet-api-ns/resolv.conf -- /srv/api/bin/tailscaled\n" +
				namespace + environment + working,
			want: "invalid guarded Tailscale ExecStart",
		},
		{
			name: "empty then valid network namespace",
			unit: "[Service]\n" + execStart +
				"NetworkNamespacePath=\n" + namespace + environment + working,
			want: "multiple NetworkNamespacePath",
		},
		{
			name: "empty then valid environment file",
			unit: "[Service]\n" + execStart + namespace +
				"EnvironmentFile=\n" + environment + working,
			want: "multiple EnvironmentFile",
		},
		{
			name: "empty then valid working directory",
			unit: "[Service]\n" + execStart + namespace + environment +
				"WorkingDirectory=\n" + working,
			want: "multiple WorkingDirectory",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseTailscaleResolverUnit(tt.unit)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseTailscaleResolverUnit error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestReconcileTailscaleResolverIsolationRejectsUnsafeCanonicalExecStartBeforeWriting(t *testing.T) {
	testTailscaleResolverReconcileRejectsUnsafeExecStart(t)
}

func TestReconcileTailscaleResolverIsolationRejectsUnitStatFailureWithoutSideEffects(t *testing.T) {
	testTailscaleResolverReconcileRejectsMissingUnit(t)
}

func TestReconcileTailscaleResolverIsolationRejectsDivergentInstalledDaemonBeforeWriting(t *testing.T) {
	testTailscaleResolverReconcileRejectsDivergentInstalledDaemon(t)
}

func TestReconcileTailscaleResolverIsolationRejectsManagedDaemonLayoutDivergence(t *testing.T) {
	testTailscaleResolverReconcileRejectsMixedLayout(t)
}

func TestTailscaleResolverUnitRejectsUnmanagedDaemonPaths(t *testing.T) {
	service := db.Service{
		Name:        "api",
		ServiceRoot: "/srv/api",
		TSNet:       &db.TailscaleNetwork{Interface: "ts0"},
	}
	args := tailscaleSystemdArgs("/srv/api/run", "/srv/api/env", "ts0", false)
	for _, daemon := range []string{
		"/srv/api/data/tailscaled",
		"/srv/api/run/tailscaled-old",
		"run/tailscaled",
	} {
		t.Run(daemon, func(t *testing.T) {
			unit := tailscaleResolverUnit{
				networkNamespace: "/var/run/netns/yeet-api-ns",
				daemon:           daemon,
				args:             args,
			}
			err := unit.validateForService(service)
			if err == nil || !strings.Contains(err.Error(), "tailscaled path") {
				t.Fatalf("validateForService(%q) error = %v, want unmanaged daemon rejection", daemon, err)
			}
		})
	}
}

func TestReconcileTailscaleResolverIsolationMigratesInactiveFleetWithOneReloadAndNoStarts(t *testing.T) {
	testTailscaleResolverReconcileMigratesInactiveFleetWithOneReloadAndNoStarts(t)
}

func TestReconcileTailscaleResolverIsolationRejectsAliasedCanonicalAndInstalledUnitPaths(t *testing.T) {
	testTailscaleResolverReconcileRejectsAliasedUnitPaths(t)
}

func TestReconcileTailscaleResolverIsolationRollsBackPartialWriteAndStopsActiveSidecar(t *testing.T) {
	testTailscaleResolverReconcileRollsBackPartialWrite(t)
}

func TestReconcileTailscaleResolverIsolationSafeFleetIsNoop(t *testing.T) {
	testTailscaleResolverReconcileSafeFleetIsNoop(t)
}

func TestEnsureTailscaleResolverIsolationMigratesWritableBind(t *testing.T) {
	const writable = `[Unit]

[Service]
EnvironmentFile=/srv/api/run/tailscaled.env
WorkingDirectory=/srv/api/tailscale
ExecStart=/srv/api/run/tailscaled --tun=ts0
NetworkNamespacePath=/var/run/netns/yeet-api-ns
BindPaths=/etc/netns/yeet-api-ns/resolv.conf:/etc/resolv.conf
PrivateMounts=yes
`

	got, changed, err := ensureTailscaleUnitResolverIsolation(writable, "/srv/catch/run/catch")
	if err != nil {
		t.Fatalf("ensureTailscaleUnitResolverIsolation: %v", err)
	}
	if !changed {
		t.Fatal("ensureTailscaleUnitResolverIsolation changed = false, want writable bind migration")
	}
	if strings.Contains(got, "\nBindPaths=") {
		t.Fatalf("migrated unit retained writable resolver bind:\n%s", got)
	}
	if strings.Contains(got, "\nBindReadOnlyPaths=") {
		t.Fatalf("migrated unit retained replaceable read-only resolver bind:\n%s", got)
	}
	if !strings.Contains(got, "\nPrivateMounts=yes\n") {
		t.Fatalf("migrated unit missing private mount namespace:\n%s", got)
	}
}

func TestEnsureTailscaleResolverIsolationRequiresExactServiceMountGrammar(t *testing.T) {
	const guarded = `[Unit]
ConditionFileIsExecutable=/srv/api/run/tailscaled

[Service]
EnvironmentFile=/srv/api/run/tailscaled.env
WorkingDirectory=/srv/api/tailscale
ExecStart=/srv/catch/run/catch tailscale-resolver-exec --source /etc/netns/yeet-api-ns/resolv.conf -- /srv/api/run/tailscaled --tun=ts0
NetworkNamespacePath=/var/run/netns/yeet-api-ns
BindReadOnlyPaths=/etc/netns/yeet-api-ns/resolv.conf:/etc/resolv.conf
PrivateMounts=yes

[Install]
WantedBy=multi-user.target
	`
	const bind = "BindReadOnlyPaths=/etc/netns/yeet-api-ns/resolv.conf:/etc/resolv.conf"
	const condition = "ConditionFileIsExecutable=/srv/api/run/tailscaled"

	tests := []struct {
		name        string
		unit        string
		wantChanged bool
		wantErr     string
		preserved   []string
	}{
		{
			name: "mount directives outside service fail closed",
			unit: strings.ReplaceAll(
				strings.ReplaceAll(guarded, bind+"\n", ""),
				"PrivateMounts=yes\n",
				"",
			) + "\n[Unit]\n" + bind + "\nPrivateMounts=yes\n",
			wantErr: "outside [Service]",
		},
		{
			name: "duplicate managed directives normalize",
			unit: strings.Replace(
				guarded,
				bind+"\n",
				bind+"\n"+bind+"\nPrivateMounts=yes\nTemporaryFileSystem=/var/cache/private\n",
				1,
			),
			wantChanged: true,
		},
		{
			name:    "additional private mounts no fails closed",
			unit:    strings.Replace(guarded, "PrivateMounts=yes\n", "PrivateMounts=yes\nPrivateMounts=no\n", 1),
			wantErr: "conflicting PrivateMounts",
		},
		{
			name: "conflicting read only bind fails closed",
			unit: strings.Replace(
				guarded,
				bind+"\n",
				bind+"\nBindReadOnlyPaths=/unmanaged/resolv.conf:/etc/resolv.conf\n",
				1,
			),
			wantErr: "conflicting BindReadOnlyPaths",
		},
		{
			name: "unrelated writable bind fails closed",
			unit: strings.Replace(
				guarded,
				bind+"\n",
				bind+"\nBindPaths=/srv/private:/mnt/private\n",
				1,
			),
			wantErr: "conflicting BindPaths",
		},
		{
			name: "desired condition outside unit does not count",
			unit: strings.Replace(
				strings.Replace(guarded, condition+"\n", "", 1),
				"[Service]\n",
				"[Service]\n"+condition+"\n",
				1,
			),
			wantChanged: true,
		},
		{
			name:        "duplicate managed condition normalizes",
			unit:        strings.Replace(guarded, condition+"\n", condition+"\n"+condition+"\n", 1),
			wantChanged: true,
		},
		{
			name: "missing unit section synthesizes managed condition",
			unit: strings.TrimPrefix(
				guarded,
				"[Unit]\n"+condition+"\n\n",
			),
			wantChanged: true,
		},
		{
			name: "non unit section before service synthesizes managed condition",
			unit: "# preserve preface\n[Install]\nAlias=api-ts.service\n\n" + strings.TrimPrefix(
				guarded,
				"[Unit]\n"+condition+"\n\n",
			),
			wantChanged: true,
			preserved:   []string{"# preserve preface", "Alias=api-ts.service"},
		},
		{
			name: "conflicting condition fails closed",
			unit: strings.Replace(
				guarded,
				condition+"\n",
				condition+"\nConditionFileIsExecutable=/unmanaged/daemon\n",
				1,
			),
			wantErr: "conflicting ConditionFileIsExecutable",
		},
		{
			name: "exec start outside service fails closed",
			unit: strings.Replace(
				strings.Replace(
					guarded,
					"ExecStart=/srv/catch/run/catch tailscale-resolver-exec --source /etc/netns/yeet-api-ns/resolv.conf -- /srv/api/run/tailscaled --tun=ts0\n",
					"",
					1,
				),
				condition+"\n",
				condition+"\nExecStart=/srv/catch/run/catch tailscale-resolver-exec --source /etc/netns/yeet-api-ns/resolv.conf -- /srv/api/run/tailscaled --tun=ts0\n",
				1,
			),
			wantErr: "ExecStart outside [Service]",
		},
		{
			name:    "protect system in service fails closed",
			unit:    strings.Replace(guarded, "PrivateMounts=yes\n", "PrivateMounts=yes\nProtectSystem=strict\n", 1),
			wantErr: "ProtectSystem",
		},
		{
			name: "protect system outside service is unrelated",
			unit: strings.Replace(
				guarded,
				"ConditionFileIsExecutable=/srv/api/run/tailscaled\n",
				"ConditionFileIsExecutable=/srv/api/run/tailscaled\nProtectSystem=strict\n",
				1,
			),
			wantChanged: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			next, changed, err := ensureTailscaleUnitResolverIsolation(
				test.unit,
				"/srv/catch/run/catch",
			)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("ensure error = %v, want %q rejection", err, test.wantErr)
				}
				if changed {
					t.Fatal("rejected resolver unit changed = true, want false")
				}
				if next != test.unit {
					t.Fatalf("rejected resolver unit was modified:\n--- want\n%s\n--- got\n%s", test.unit, next)
				}
				return
			}
			if err != nil {
				t.Fatalf("ensure exact resolver guard: %v", err)
			}
			if changed != test.wantChanged {
				t.Fatalf("changed = %v, want %v", changed, test.wantChanged)
			}
			assertExactTailscaleResolverUnitGrammar(t, next, condition)
			for _, preserved := range test.preserved {
				if !strings.Contains(next, preserved) {
					t.Fatalf("rewriter removed %q:\n%s", preserved, next)
				}
			}
			if strings.Contains(test.unit, "TemporaryFileSystem=") &&
				!strings.Contains(next, "TemporaryFileSystem=/var/cache/private") {
				t.Fatalf("rewriter removed unrelated mount sandboxing:\n%s", next)
			}
		})
	}
}

func assertExactTailscaleResolverUnitGrammar(
	t *testing.T,
	unit, wantCondition string,
) {
	t.Helper()
	section := ""
	var conditions []string
	var mounts []string
	for _, line := range strings.Split(unit, "\n") {
		line = strings.TrimSpace(line)
		if isSystemdUnitSection(line) {
			section = line
			continue
		}
		if strings.HasPrefix(line, "ConditionFileIsExecutable=") {
			conditions = append(conditions, section+":"+line)
		}
		if isTailscaleResolverMountDirective(line) ||
			(section == "[Service]" && strings.HasPrefix(line, "ProtectSystem=")) {
			mounts = append(mounts, section+":"+line)
		}
	}
	if diff := cmp.Diff([]string{"[Unit]:" + wantCondition}, conditions); diff != "" {
		t.Fatalf("effective resolver condition grammar (-want +got):\n%s\nunit:\n%s", diff, unit)
	}
	if diff := cmp.Diff(
		[]string{"[Service]:PrivateMounts=yes"},
		mounts,
	); diff != "" {
		t.Fatalf("effective resolver mount grammar (-want +got):\n%s\nunit:\n%s", diff, unit)
	}
}

func FuzzParseTailscaleResolverUnit(f *testing.F) {
	for _, seed := range []string{
		"[Service]\nExecStart=/srv/api/run/tailscaled --tun=ts0\nNetworkNamespacePath=/var/run/netns/yeet-api-ns\nEnvironmentFile=/srv/api/run/tailscaled.env\nWorkingDirectory=/srv/api/tailscale\n",
		"[Service]\nExecStart=/srv/catch/run/catch tailscale-resolver-exec --source /etc/netns/yeet-api-ns/resolv.conf -- /srv/api/run/tailscaled --tun=ts0\nNetworkNamespacePath=/var/run/netns/yeet-api-ns\nEnvironmentFile=/srv/api/run/tailscaled.env\nWorkingDirectory=/srv/api/tailscale\nBindReadOnlyPaths=/etc/netns/yeet-api-ns/resolv.conf:/etc/resolv.conf\nPrivateMounts=yes\n",
		"[Unit]\nBindReadOnlyPaths=/ignored:/etc/resolv.conf\n[Service]\nProtectSystem=strict\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, unit string) {
		_, _ = parseTailscaleResolverUnit(unit)
	})
}

func TestReconcileTailscaleResolverMountsRestartsUnhealthySidecar(t *testing.T) {
	fixture := newGuardedTailscaleResolverFixture(t, "api", tailscaleResolverGenerationCurrent)
	s := fixture.server
	oldActive := catchSystemdUnitActive
	catchSystemdUnitActive = func(string) bool { return true }
	t.Cleanup(func() { catchSystemdUnitActive = oldActive })
	previousVerify := verifyTailscaleSystemdSidecar
	verifyTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error {
		return errors.New("resolver mount is unhealthy")
	}
	t.Cleanup(func() { verifyTailscaleSystemdSidecar = previousVerify })

	dir := t.TempDir()
	currentInfo := writeNetNSTestFile(t, filepath.Join(dir, "current"))
	staleInfo := writeNetNSTestFile(t, filepath.Join(dir, "stale"))
	var pidCalls int
	prevPID := tailscaleSidecarMainPID
	tailscaleSidecarMainPID = func(string) (int, error) {
		pidCalls++
		if pidCalls == 1 {
			return 1234, nil
		}
		if pidCalls == 2 {
			return 0, nil
		}
		return 5678, nil
	}
	t.Cleanup(func() {
		tailscaleSidecarMainPID = prevPID
	})

	prevStat := statNetNSPath
	statNetNSPath = func(path string) (os.FileInfo, error) {
		switch path {
		case "/etc/netns/yeet-api-ns/resolv.conf", "/proc/5678/root/etc/resolv.conf":
			return currentInfo, nil
		case "/proc/1234/root/etc/resolv.conf":
			return staleInfo, nil
		default:
			return nil, os.ErrNotExist
		}
	}
	t.Cleanup(func() {
		statNetNSPath = prevStat
	})

	var calls []string
	prevRestart := restartTailscaleSystemdSidecar
	restartTailscaleSystemdSidecar = func(_ context.Context, service *svc.SystemdService) error {
		calls = append(calls, "verified-restart:"+service.Name())
		return nil
	}
	t.Cleanup(func() {
		restartTailscaleSystemdSidecar = prevRestart
	})

	if err := s.reconcileTailscaleResolverMounts(context.Background()); err != nil {
		t.Fatalf("reconcileTailscaleResolverMounts returned error: %v", err)
	}
	if diff := cmp.Diff([]string{"verified-restart:api"}, calls); diff != "" {
		t.Fatalf("sidecar lifecycle calls (-want +got):\n%s", diff)
	}
}

func TestReconcileTailscaleResolverIsolationRejectsUnitWithoutNetworkNamespacePath(t *testing.T) {
	testTailscaleResolverReconcileRejectsMissingNetworkNamespace(t)
}

func TestReconcileTailscaleResolverIsolationRollsBackExactOriginalsAfterDaemonReloadFailure(t *testing.T) {
	testTailscaleResolverReconcileRollsBackDaemonReloadFailure(t)
}

func TestReconcileTailscaleResolverIsolationRollsBackExactOriginalsAfterRestartFailure(t *testing.T) {
	testTailscaleResolverReconcileRollsBackRestartFailure(t)
}

func TestReconcileTailscaleResolverIsolationSkipsNonTailscaleServices(t *testing.T) {
	testTailscaleResolverReconcileSkipsNonTailscaleServices(t)
}

func useTestSystemdSystemDir(t *testing.T) string {
	t.Helper()
	withTailscaleResolverCatchPath(t, "/srv/catch/run/catch")
	old := systemdSystemDir
	systemdDir := filepath.Join(t.TempDir(), "systemd")
	systemdSystemDir = systemdDir
	oldActive := catchSystemdUnitActive
	catchSystemdUnitActive = func(string) bool { return true }
	oldRestart := restartTailscaleSystemdSidecar
	restartTailscaleSystemdSidecar = func(_ context.Context, service *svc.SystemdService) error {
		return catchSystemctl("restart", "yeet-"+service.Name()+"-ts.service")
	}
	oldVerify := verifyTailscaleSystemdSidecar
	verifyTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error { return nil }
	oldDropIns := tailscaleResolverUnitDropInPaths
	tailscaleResolverUnitDropInPaths = func(context.Context, string) ([]string, error) {
		return nil, nil
	}
	t.Cleanup(func() {
		systemdSystemDir = old
		catchSystemdUnitActive = oldActive
		restartTailscaleSystemdSidecar = oldRestart
		verifyTailscaleSystemdSidecar = oldVerify
		tailscaleResolverUnitDropInPaths = oldDropIns
	})
	return systemdDir
}

func ptrString(s string) *string {
	return &s
}

func writeTailscaleTestConfig(t *testing.T, path string, cfg ipn.ConfigVAlpha) {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal tailscale config: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir tailscale config parent: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write tailscale config: %v", err)
	}
}

func readTailscaleTestConfig(t *testing.T, path string) ipn.ConfigVAlpha {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tailscale config: %v", err)
	}
	var cfg ipn.ConfigVAlpha
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal tailscale config: %v", err)
	}
	return cfg
}

func TestTailscaleSidecarNetNSStaleOnHost(t *testing.T) {
	dir := t.TempDir()
	currentInfo := writeNetNSTestFile(t, filepath.Join(dir, "current"))
	staleInfo := writeNetNSTestFile(t, filepath.Join(dir, "stale"))

	cases := []struct {
		name    string
		pid     int
		stats   map[string]os.FileInfo
		statErr error
		want    bool
		wantErr string
	}{
		{
			name: "inactive sidecar",
			pid:  0,
			want: false,
		},
		{
			name: "current namespace",
			pid:  1234,
			stats: map[string]os.FileInfo{
				"/proc/1234/ns/net":           currentInfo,
				"/var/run/netns/yeet-demo-ns": currentInfo,
			},
			want: false,
		},
		{
			name: "stale namespace",
			pid:  1234,
			stats: map[string]os.FileInfo{
				"/proc/1234/ns/net":           staleInfo,
				"/var/run/netns/yeet-demo-ns": currentInfo,
			},
			want: true,
		},
		{
			name: "missing process namespace",
			pid:  1234,
			stats: map[string]os.FileInfo{
				"/var/run/netns/yeet-demo-ns": currentInfo,
			},
			want: false,
		},
		{
			name:    "stat error",
			pid:     1234,
			statErr: errors.New("stat failed"),
			wantErr: "stat tailscale sidecar netns",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prevPID := tailscaleSidecarMainPID
			tailscaleSidecarMainPID = func(unit string) (int, error) {
				if unit != "yeet-demo-ts.service" {
					t.Fatalf("unit = %q, want yeet-demo-ts.service", unit)
				}
				return tc.pid, nil
			}
			t.Cleanup(func() {
				tailscaleSidecarMainPID = prevPID
			})

			prevStat := statNetNSPath
			statNetNSPath = func(path string) (os.FileInfo, error) {
				if tc.statErr != nil {
					return nil, tc.statErr
				}
				info, ok := tc.stats[path]
				if !ok {
					return nil, os.ErrNotExist
				}
				return info, nil
			}
			t.Cleanup(func() {
				statNetNSPath = prevStat
			})

			got, err := tailscaleSidecarNetNSStaleOnHost("demo")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("tailscaleSidecarNetNSStaleOnHost returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("tailscaleSidecarNetNSStaleOnHost = %v, want %v", got, tc.want)
			}
		})
	}
}

func writeNetNSTestFile(t *testing.T, path string) os.FileInfo {
	t.Helper()
	if err := os.WriteFile(path, []byte(path), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}

func TestReconcileNetNSBackedDockerServicesContinuesAfterServiceError(t *testing.T) {
	s := newTestServer(t)
	logs := captureLogs(t)
	addTestServices(t, s,
		db.Service{
			Name:             "docker-fail",
			ServiceType:      db.ServiceTypeDockerCompose,
			Generation:       1,
			LatestGeneration: 1,
			Artifacts: db.ArtifactStore{
				db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/yeet-docker-fail-ns.service"}},
			},
		},
		db.Service{
			Name:             "docker-later",
			ServiceType:      db.ServiceTypeDockerCompose,
			Generation:       1,
			LatestGeneration: 1,
			Artifacts: db.ArtifactStore{
				db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/yeet-docker-later-ns.service"}},
			},
		},
	)

	wantErr := errors.New("boom")
	var called []string
	restarted := map[string]bool{}
	s.newDockerComposeService = func(sv db.ServiceView) (dockerNetNSReconciler, error) {
		name := sv.Name()
		return fakeDockerNetNSReconciler{
			name: name,
			reconcile: func(context.Context) (bool, error) {
				called = append(called, name)
				if name == "docker-fail" {
					return false, wantErr
				}
				restarted[name] = true
				return true, nil
			},
		}, nil
	}

	err := s.reconcileNetNSBackedDockerServices(context.Background())
	if err == nil {
		t.Fatal("reconcileNetNSBackedDockerServices returned nil error")
	}
	if !strings.Contains(err.Error(), `docker-fail`) {
		t.Fatalf("aggregate error missing failing service name: %v", err)
	}
	if len(called) != 2 {
		t.Fatalf("expected two eligible services to be attempted, got %v", called)
	}
	gotCalled := map[string]int{}
	for _, name := range called {
		gotCalled[name]++
	}
	wantCalled := map[string]int{
		"docker-fail":  1,
		"docker-later": 1,
	}
	if diff := cmp.Diff(wantCalled, gotCalled); diff != "" {
		t.Fatalf("unexpected reconciled services (-want +got):\n%s", diff)
	}
	if !restarted["docker-later"] {
		t.Fatalf("expected later eligible service to still reconcile successfully; restarted=%v called=%v", restarted, called)
	}
	out := logs.String()
	if !strings.Contains(out, `netns reconciliation failed for service "docker-fail"`) {
		t.Fatalf("missing per-service failure log:\n%s", out)
	}
	if !strings.Contains(out, `reconciled stale docker netns for service "docker-later"; restarted containers`) {
		t.Fatalf("missing restarted-service log:\n%s", out)
	}
}

func TestReconcileRuntimeStateRunsResolverIsolationBeforeNetNSReconciliation(t *testing.T) {
	stubTailscaleResolverJournalOwner(t)
	s := newTestServer(t)
	useTestSystemdSystemDir(t)
	fixture := addTailscaleResolverPlanService(
		t,
		s,
		"docker-netns",
		tailscaleResolverGenerationHistorical,
		"",
		nil,
	)
	service := fixture.service
	service.Artifacts[db.ArtifactNetNSService] = &db.Artifact{
		Refs: map[db.ArtifactRef]string{db.Gen(service.Generation): "/tmp/yeet-docker-netns-ns.service"},
	}
	replaceTailscaleResolverPlanService(t, s, service)

	var calls []string
	reconciled := make(chan struct{})
	prevInstall := installYeetNSService
	installYeetNSService = func(string) error {
		calls = append(calls, "install")
		return nil
	}
	defer func() {
		installYeetNSService = prevInstall
	}()
	stubYeetDNSInstaller(t, func(root string) error {
		if root != s.cfg.RootDir {
			t.Fatalf("dns installer root = %q, want %q", root, s.cfg.RootDir)
		}
		calls = append(calls, "dns-install")
		return nil
	})
	stubDockerPrereqsInstaller(t, func(*Server) error {
		calls = append(calls, "docker-prereqs")
		return nil
	})
	prevSystemctl := catchSystemctl
	catchSystemctl = func(args ...string) error {
		calls = append(calls, "systemctl:"+strings.Join(args, " "))
		return nil
	}
	t.Cleanup(func() {
		catchSystemctl = prevSystemctl
	})
	stubTailscaleResolverActive(t, map[string]bool{
		"yeet-docker-netns-ts.service": true,
	})
	prevRestart := restartTailscaleSystemdSidecar
	restartTailscaleSystemdSidecar = func(_ context.Context, service *svc.SystemdService) error {
		calls = append(calls, "systemctl:restart yeet-"+service.Name()+"-ts.service")
		return nil
	}
	t.Cleanup(func() {
		restartTailscaleSystemdSidecar = prevRestart
	})
	prevVerify := verifyTailscaleSystemdSidecar
	verifyTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error {
		return nil
	}
	t.Cleanup(func() {
		verifyTailscaleSystemdSidecar = prevVerify
	})
	prevStale := tailscaleSidecarNetNSStale
	tailscaleSidecarNetNSStale = func(string) (bool, error) {
		return false, nil
	}
	t.Cleanup(func() {
		tailscaleSidecarNetNSStale = prevStale
	})
	prevNAT := reconcileDockerNetNSPortForwards
	reconcileDockerNetNSPortForwards = func(*db.Store) error {
		calls = append(calls, "nat-reconcile")
		close(reconciled)
		return nil
	}
	defer func() {
		reconcileDockerNetNSPortForwards = prevNAT
	}()

	s.newDockerComposeService = func(sv db.ServiceView) (dockerNetNSReconciler, error) {
		name := sv.Name()
		return fakeDockerNetNSReconciler{
			name: name,
			reconcile: func(context.Context) (bool, error) {
				calls = append(calls, "reconcile:"+name)
				return false, nil
			},
		}, nil
	}

	s.Start()
	t.Cleanup(s.Shutdown)

	select {
	case <-reconciled:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reconciliation to run")
	}

	if diff := cmp.Diff([]string{
		"install",
		"dns-install",
		"docker-prereqs",
		"systemctl:daemon-reload",
		"systemctl:restart yeet-docker-netns-ts.service",
		"reconcile:docker-netns",
		"nat-reconcile",
	}, calls); diff != "" {
		t.Fatalf("unexpected startup call order (-want +got):\n%s", diff)
	}
}

func TestServerStartRepairsTailscaleDNSBeforeResolverFleetMigration(t *testing.T) {
	for _, layout := range []tailscaleResolverGenerationLayout{
		tailscaleResolverGenerationHistorical,
		tailscaleResolverGenerationCurrent,
	} {
		for _, forms := range []struct {
			name        string
			runtimeDNS  opt.Bool
			artifactDNS opt.Bool
		}{
			{name: "runtime-true-generation-omitted", runtimeDNS: "true"},
			{name: "runtime-omitted-generation-true", artifactDNS: "true"},
		} {
			t.Run(string(layout)+"/"+forms.name, func(t *testing.T) {
				stubTailscaleResolverJournalOwner(t)
				fixture := newTailscaleResolverPlanFixture(
					t,
					"startup-"+string(layout)+"-"+forms.name,
					layout,
					"",
				)
				s := fixture.server
				s.recoverVMRuntimeState = func(context.Context, *Config) error { return nil }
				tuple := fixtureTuple(fixture.service, layout)
				generationConfig := fixture.service.Artifacts[db.ArtifactTSConfig].
					Refs[db.Gen(fixture.service.Generation)]
				writeTailscaleTestConfig(t, tuple.configFile, ipn.ConfigVAlpha{
					Version: "alpha0", AcceptDNS: forms.runtimeDNS,
				})
				writeTailscaleTestConfig(t, generationConfig, ipn.ConfigVAlpha{
					Version: "alpha0", AcceptDNS: forms.artifactDNS,
				})

				previousInstall := installYeetNSService
				installYeetNSService = func(string) error { return nil }
				t.Cleanup(func() { installYeetNSService = previousInstall })
				stubYeetDNSInstaller(t, func(string) error { return nil })
				stubDockerPrereqsInstaller(t, func(*Server) error { return nil })
				previousSystemctl := catchSystemctl
				catchSystemctl = func(...string) error { return nil }
				t.Cleanup(func() { catchSystemctl = previousSystemctl })
				stubTailscaleResolverActive(t, map[string]bool{
					"yeet-" + fixture.service.Name + "-ts.service": true,
				})
				restarts := 0
				previousRestart := restartTailscaleSystemdSidecar
				restartTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error {
					restarts++
					return nil
				}
				t.Cleanup(func() { restartTailscaleSystemdSidecar = previousRestart })
				previousVerify := verifyTailscaleSystemdSidecar
				verifyTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error {
					return nil
				}
				t.Cleanup(func() { verifyTailscaleSystemdSidecar = previousVerify })
				previousActive := catchSystemdUnitActive
				catchSystemdUnitActive = func(string) bool { return false }
				t.Cleanup(func() { catchSystemdUnitActive = previousActive })
				reconciled := make(chan struct{})
				previousNAT := reconcileDockerNetNSPortForwards
				reconcileDockerNetNSPortForwards = func(*db.Store) error {
					close(reconciled)
					return nil
				}
				t.Cleanup(func() { reconcileDockerNetNSPortForwards = previousNAT })
				logs := captureLogs(t)

				s.Start()
				t.Cleanup(s.Shutdown)
				select {
				case <-reconciled:
				case <-time.After(time.Second):
					t.Fatal("timed out waiting for startup reconciliation")
				}

				for label, path := range map[string]string{
					"runtime": tuple.configFile, "generation": generationConfig,
				} {
					if cfg := readTailscaleTestConfig(t, path); !cfg.AcceptDNS.EqualBool(false) {
						t.Fatalf("%s config AcceptDNS = %q, want explicit false", label, cfg.AcceptDNS)
					}
				}
				if err := s.checkTailscaleResolverReady(context.Background(), fixture.service); err != nil {
					t.Fatalf("startup resolver readiness: %v", err)
				}
				if restarts != 1 {
					t.Fatalf("startup verified restarts = %d, want one fleet-migration restart", restarts)
				}
				if out := logs.String(); strings.Contains(out, "tailscale resolver isolation startup migration failed") ||
					strings.Contains(out, "after DNS config reconciliation") {
					t.Fatalf("startup emitted misleading resolver failure:\n%s", out)
				}
			})
		}
	}
}

func TestServerStartLogsNATReconciliationFailureNonFatally(t *testing.T) {
	s := newTestServer(t)
	logs := captureLogs(t)
	addTestServices(t, s, db.Service{
		Name:             "docker-netns",
		ServiceType:      db.ServiceTypeDockerCompose,
		Generation:       1,
		LatestGeneration: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/yeet-docker-netns-ns.service"}},
		},
	})

	prevInstall := installYeetNSService
	installYeetNSService = func(string) error { return nil }
	defer func() {
		installYeetNSService = prevInstall
	}()
	stubYeetDNSInstaller(t, func(string) error { return nil })
	stubDockerPrereqsInstaller(t, func(*Server) error { return nil })

	prevNAT := reconcileDockerNetNSPortForwards
	reconciledNAT := make(chan struct{})
	reconcileDockerNetNSPortForwards = func(*db.Store) error {
		close(reconciledNAT)
		return errors.New("nat exploded")
	}
	defer func() {
		reconcileDockerNetNSPortForwards = prevNAT
	}()

	reconciledLinks := make(chan struct{})
	s.newDockerComposeService = func(sv db.ServiceView) (dockerNetNSReconciler, error) {
		return fakeDockerNetNSReconciler{
			name: sv.Name(),
			reconcile: func(context.Context) (bool, error) {
				close(reconciledLinks)
				return false, nil
			},
		}, nil
	}

	s.Start()
	t.Cleanup(s.Shutdown)

	select {
	case <-reconciledNAT:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for NAT reconciliation to run")
	}
	select {
	case <-reconciledLinks:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for link reconciliation to run")
	}

	out := waitForLogContains(t, logs, "docker netns NAT reconciliation failed: nat exploded")
	if !strings.Contains(out, "docker netns NAT reconciliation failed: nat exploded") {
		t.Fatalf("missing NAT failure log:\n%s", out)
	}
}

func TestServerStartLogsReconciliationFailureNonFatally(t *testing.T) {
	s := newTestServer(t)
	logs := captureLogs(t)
	addTestServices(t, s, db.Service{
		Name:             "docker-netns",
		ServiceType:      db.ServiceTypeDockerCompose,
		Generation:       1,
		LatestGeneration: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/yeet-docker-netns-ns.service"}},
		},
	})

	prevInstall := installYeetNSService
	installYeetNSService = func(string) error { return nil }
	defer func() {
		installYeetNSService = prevInstall
	}()
	stubYeetDNSInstaller(t, func(string) error { return nil })
	stubDockerPrereqsInstaller(t, func(*Server) error { return nil })
	reconciled := make(chan struct{})
	prevNAT := reconcileDockerNetNSPortForwards
	reconcileDockerNetNSPortForwards = func(*db.Store) error {
		close(reconciled)
		return nil
	}
	defer func() {
		reconcileDockerNetNSPortForwards = prevNAT
	}()

	s.newDockerComposeService = func(sv db.ServiceView) (dockerNetNSReconciler, error) {
		return fakeDockerNetNSReconciler{
			name: sv.Name(),
			reconcile: func(context.Context) (bool, error) {
				return false, errors.New("reconcile exploded")
			},
		}, nil
	}

	s.Start()
	t.Cleanup(s.Shutdown)

	select {
	case <-reconciled:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reconciliation failure to run")
	}

	out := waitForLogContains(t, logs, `netns reconciliation failed:`)
	if !strings.Contains(out, `netns reconciliation failed for service "docker-netns"`) {
		t.Fatalf("missing per-service failure log:\n%s", out)
	}
	if !strings.Contains(out, `netns reconciliation failed:`) {
		t.Fatalf("missing startup summary log:\n%s", out)
	}
}

func TestServerStartLogsRestartedNetNSService(t *testing.T) {
	s := newTestServer(t)
	logs := captureLogs(t)
	addTestServices(t, s, db.Service{
		Name:             "docker-netns",
		ServiceType:      db.ServiceTypeDockerCompose,
		Generation:       1,
		LatestGeneration: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/yeet-docker-netns-ns.service"}},
		},
	})

	prevInstall := installYeetNSService
	installYeetNSService = func(string) error { return nil }
	defer func() {
		installYeetNSService = prevInstall
	}()
	stubYeetDNSInstaller(t, func(string) error { return nil })
	stubDockerPrereqsInstaller(t, func(*Server) error { return nil })
	reconciled := make(chan struct{})
	prevNAT := reconcileDockerNetNSPortForwards
	reconcileDockerNetNSPortForwards = func(*db.Store) error {
		close(reconciled)
		return nil
	}
	defer func() {
		reconcileDockerNetNSPortForwards = prevNAT
	}()

	s.newDockerComposeService = func(sv db.ServiceView) (dockerNetNSReconciler, error) {
		return fakeDockerNetNSReconciler{
			name: sv.Name(),
			reconcile: func(context.Context) (bool, error) {
				return true, nil
			},
		}, nil
	}

	s.Start()
	t.Cleanup(s.Shutdown)

	select {
	case <-reconciled:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reconciliation to run")
	}

	out := waitForLogContains(t, logs, `reconciled stale docker netns for service "docker-netns"; restarted containers`)
	if !strings.Contains(out, `reconciled stale docker netns for service "docker-netns"; restarted containers`) {
		t.Fatalf("missing restarted-service log:\n%s", out)
	}
}

func TestServerStartReturnsBeforeNetNSReconciliationFinishes(t *testing.T) {
	s := newTestServer(t)
	addTestServices(t, s, db.Service{
		Name:             "docker-netns",
		ServiceType:      db.ServiceTypeDockerCompose,
		Generation:       1,
		LatestGeneration: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/yeet-docker-netns-ns.service"}},
		},
	})

	prevInstall := installYeetNSService
	installYeetNSService = func(string) error { return nil }
	defer func() {
		installYeetNSService = prevInstall
	}()
	stubYeetDNSInstaller(t, func(string) error { return nil })
	stubDockerPrereqsInstaller(t, func(*Server) error { return nil })
	prevNAT := reconcileDockerNetNSPortForwards
	reconcileDockerNetNSPortForwards = func(*db.Store) error { return nil }
	defer func() {
		reconcileDockerNetNSPortForwards = prevNAT
	}()

	started := make(chan struct{})
	release := make(chan struct{})
	releaseOnce := sync.Once{}
	releaseFn := func() {
		releaseOnce.Do(func() {
			close(release)
		})
	}
	t.Cleanup(releaseFn)

	s.newDockerComposeService = func(sv db.ServiceView) (dockerNetNSReconciler, error) {
		return fakeDockerNetNSReconciler{
			name: sv.Name(),
			reconcile: func(context.Context) (bool, error) {
				select {
				case <-started:
				default:
					close(started)
				}
				<-release
				return true, nil
			},
		}, nil
	}

	startDone := make(chan struct{})
	go func() {
		s.Start()
		close(startDone)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("reconciliation never started")
	}

	select {
	case <-startDone:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("Start did not return promptly while reconciliation was blocked")
	}

	sawCleanup := false
	t.Cleanup(func() {
		if !sawCleanup {
			s.Shutdown()
		}
	})
	releaseFn()
	s.Shutdown()
	sawCleanup = true
}

func TestServerStartReturnsBeforeVMRuntimeRecoveryAndDelaysLaterReconciliation(t *testing.T) {
	s := newTestServer(t)
	prevInstall := installYeetNSService
	installYeetNSService = func(string) error { return nil }
	defer func() { installYeetNSService = prevInstall }()
	stubYeetDNSInstaller(t, func(string) error { return nil })
	stubDockerPrereqsInstaller(t, func(*Server) error { return nil })

	recoveryStarted := make(chan struct{})
	releaseRecovery := make(chan struct{})
	s.recoverVMRuntimeState = func(ctx context.Context, _ *Config) error {
		close(recoveryStarted)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-releaseRecovery:
			return nil
		}
	}
	reconciled := make(chan struct{})
	prevNAT := reconcileDockerNetNSPortForwards
	reconcileDockerNetNSPortForwards = func(*db.Store) error {
		close(reconciled)
		return nil
	}
	defer func() { reconcileDockerNetNSPortForwards = prevNAT }()

	startDone := make(chan struct{})
	go func() {
		s.Start()
		close(startDone)
	}()
	select {
	case <-recoveryStarted:
	case <-time.After(time.Second):
		t.Fatal("VM runtime recovery did not start")
	}
	select {
	case <-startDone:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("Start waited for VM runtime recovery")
	}
	select {
	case <-reconciled:
		t.Fatal("later reconciliation ran before VM runtime recovery completed")
	default:
	}

	close(releaseRecovery)
	select {
	case <-reconciled:
	case <-time.After(time.Second):
		t.Fatal("later reconciliation did not run after VM runtime recovery")
	}
	s.Shutdown()
}

func TestVMRuntimeRecoveryBarrierPrefersCancellationAfterCompletion(t *testing.T) {
	done := make(chan struct{})
	close(done)
	barrier := &vmRuntimeRecoveryBarrier{done: done}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if barrier.Wait(ctx) {
		t.Fatal("completed recovery barrier released work after cancellation")
	}
}

func TestServerStartLogsVMRuntimeRecoveryFailureAndBlocksLaterReconciliation(t *testing.T) {
	s := newTestServer(t)
	logs := captureLogs(t)
	prevInstall := installYeetNSService
	installYeetNSService = func(string) error { return nil }
	defer func() { installYeetNSService = prevInstall }()
	stubYeetDNSInstaller(t, func(string) error { return nil })
	stubDockerPrereqsInstaller(t, func(*Server) error { return nil })

	s.recoverVMRuntimeState = func(context.Context, *Config) error {
		return errors.New("recovery state retained")
	}
	reconciled := make(chan struct{}, 1)
	prevNAT := reconcileDockerNetNSPortForwards
	reconcileDockerNetNSPortForwards = func(*db.Store) error {
		reconciled <- struct{}{}
		return nil
	}
	defer func() { reconcileDockerNetNSPortForwards = prevNAT }()

	s.Start()
	t.Cleanup(s.Shutdown)
	if out := waitForLogContains(t, logs, "VM runtime adoption recovery failed: recovery state retained"); !strings.Contains(out, "VM runtime adoption recovery failed: recovery state retained") {
		t.Fatalf("missing VM runtime recovery failure log:\n%s", out)
	}
	select {
	case <-reconciled:
		t.Fatal("later reconciliation ran after VM runtime recovery failed")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestServerShutdownCancelsNetNSReconciliation(t *testing.T) {
	s := newTestServer(t)
	addTestServices(t, s, db.Service{
		Name:             "docker-netns",
		ServiceType:      db.ServiceTypeDockerCompose,
		Generation:       1,
		LatestGeneration: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/yeet-docker-netns-ns.service"}},
		},
	})

	prevInstall := installYeetNSService
	installYeetNSService = func(string) error { return nil }
	defer func() {
		installYeetNSService = prevInstall
	}()
	stubYeetDNSInstaller(t, func(string) error { return nil })
	stubDockerPrereqsInstaller(t, func(*Server) error { return nil })
	prevNAT := reconcileDockerNetNSPortForwards
	reconcileDockerNetNSPortForwards = func(*db.Store) error { return nil }
	defer func() {
		reconcileDockerNetNSPortForwards = prevNAT
	}()

	started := make(chan struct{})
	canceled := make(chan struct{})

	s.newDockerComposeService = func(sv db.ServiceView) (dockerNetNSReconciler, error) {
		return fakeDockerNetNSReconciler{
			name: sv.Name(),
			reconcile: func(ctx context.Context) (bool, error) {
				select {
				case <-started:
				default:
					close(started)
				}
				<-ctx.Done()
				close(canceled)
				return false, ctx.Err()
			},
		}, nil
	}

	startDone := make(chan struct{})
	go func() {
		s.Start()
		close(startDone)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("reconciliation never started")
	}

	select {
	case <-startDone:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("Start did not return promptly while reconciliation was blocked")
	}

	shutdownDone := make(chan struct{})
	go func() {
		s.Shutdown()
		close(shutdownDone)
	}()

	select {
	case <-shutdownDone:
	case <-time.After(50 * time.Millisecond):
	}

	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("reconciliation was not canceled by shutdown")
	}

	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not return after reconciliation was canceled")
	}
}

func TestServerShutdownDoesNotLogCancellationAsFailure(t *testing.T) {
	s := newTestServer(t)
	logs := captureLogs(t)
	addTestServices(t, s, db.Service{
		Name:             "docker-netns",
		ServiceType:      db.ServiceTypeDockerCompose,
		Generation:       1,
		LatestGeneration: 1,
		Artifacts: db.ArtifactStore{
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/yeet-docker-netns-ns.service"}},
		},
	})

	prevInstall := installYeetNSService
	installYeetNSService = func(string) error { return nil }
	defer func() {
		installYeetNSService = prevInstall
	}()
	stubYeetDNSInstaller(t, func(string) error { return nil })
	stubDockerPrereqsInstaller(t, func(*Server) error { return nil })
	prevNAT := reconcileDockerNetNSPortForwards
	reconcileDockerNetNSPortForwards = func(*db.Store) error { return nil }
	defer func() {
		reconcileDockerNetNSPortForwards = prevNAT
	}()

	started := make(chan struct{})
	s.newDockerComposeService = func(sv db.ServiceView) (dockerNetNSReconciler, error) {
		return fakeDockerNetNSReconciler{
			name: sv.Name(),
			reconcile: func(ctx context.Context) (bool, error) {
				close(started)
				<-ctx.Done()
				return false, ctx.Err()
			},
		}, nil
	}

	s.Start()
	<-started
	s.Shutdown()

	if strings.Contains(logs.String(), "netns reconciliation failed") {
		t.Fatalf("unexpected cancellation failure log:\n%s", logs.String())
	}
}
