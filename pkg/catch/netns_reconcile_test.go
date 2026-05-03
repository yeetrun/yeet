// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"log"
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

	if diff := cmp.Diff([]string{"install", "docker-prereqs", "nat-reconcile", "reconcile:docker-netns"}, calls); diff != "" {
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
