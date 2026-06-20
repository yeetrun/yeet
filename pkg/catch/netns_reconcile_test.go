// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
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
				return true, nil
			},
		}, nil
	}

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
		"systemctl:restart yeet-docker-netns-ts.service",
	}
	if diff := cmp.Diff(want, calls); diff != "" {
		t.Fatalf("unexpected reconciliation side effects (-want +got):\n%s", diff)
	}
}

func TestReconcileNetNSBackedDockerServicesRepairsStaleTailscaleSidecar(t *testing.T) {
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
		return true, nil
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
		"systemctl:restart yeet-docker-netns-ts.service",
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

func TestServerStartRunsNetNSReconciliation(t *testing.T) {
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

	var calls []string
	reconciled := make(chan struct{})
	prevInstall := installYeetNSService
	installYeetNSService = func() error {
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
	prevNAT := reconcileDockerNetNSPortForwards
	reconcileDockerNetNSPortForwards = func(*db.Store) error {
		calls = append(calls, "nat-reconcile")
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
				close(reconciled)
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

	if diff := cmp.Diff([]string{"install", "dns-install", "docker-prereqs", "reconcile:docker-netns", "nat-reconcile"}, calls); diff != "" {
		t.Fatalf("unexpected startup call order (-want +got):\n%s", diff)
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
	installYeetNSService = func() error { return nil }
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

	out := logs.String()
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
	installYeetNSService = func() error { return nil }
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

	reconciled := make(chan struct{})
	s.newDockerComposeService = func(sv db.ServiceView) (dockerNetNSReconciler, error) {
		return fakeDockerNetNSReconciler{
			name: sv.Name(),
			reconcile: func(context.Context) (bool, error) {
				close(reconciled)
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
	installYeetNSService = func() error { return nil }
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

	reconciled := make(chan struct{})
	s.newDockerComposeService = func(sv db.ServiceView) (dockerNetNSReconciler, error) {
		return fakeDockerNetNSReconciler{
			name: sv.Name(),
			reconcile: func(context.Context) (bool, error) {
				close(reconciled)
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
	installYeetNSService = func() error { return nil }
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
	installYeetNSService = func() error { return nil }
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
	installYeetNSService = func() error { return nil }
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
