// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestVMRuntimeRestartUpgradeReleasesRootLockAndUnprotectsHealthyRecoveryPoint(t *testing.T) {
	server := newTestServer(t)
	configured := vmRuntimeLaunchTestArtifact("v1.16.1", "/configured")
	candidate := vmRuntimeLaunchTestArtifact("v1.17.0", "/candidate")
	seedVMRuntimeRestartService(t, server, configured, &candidate, nil)

	var calls []string
	server.vmRuntimeRestartDeps = &vmRuntimeRestartDeps{
		now:  func() time.Time { return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) },
		wait: time.Millisecond,
		snapshot: func(context.Context, *db.Service, io.Writer) (string, error) {
			calls = append(calls, "snapshot")
			return "pool/vm@runtime-upgrade", nil
		},
		restart: func(string) error {
			calls = append(calls, "restart")
			lockCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := WithVMRuntimeTransactionLock(lockCtx, &server.cfg, func() error { return nil }); err != nil {
				t.Fatalf("runtime lock remained held across restart: %v", err)
			}
			return nil
		},
		consume: func(context.Context, string) (vmRuntimeTrialOutcome, error) {
			calls = append(calls, "consume")
			_, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
				trial := service.VM.Components.Runtime.Trial
				if trial == nil || trial.State != "pending" || trial.RecoveryPoint != "pool/vm@runtime-upgrade" {
					t.Fatalf("pending trial = %#v", trial)
				}
				service.VM.Components.Runtime.Previous = cloneVMRuntimeArtifact(&configured)
				service.VM.Components.Runtime.Configured = candidate
				service.VM.Components.Runtime.Staged = nil
				trial.State = string(vmRuntimeTrialHealthy)
				return nil
			})
			return vmRuntimeTrialHealthy, err
		},
		unprotect: func(_ context.Context, snapshot string) error {
			calls = append(calls, "unprotect:"+snapshot)
			return nil
		},
	}
	var out bytes.Buffer
	if err := server.restartStagedVMRuntime(context.Background(), &out, "devbox"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "snapshot,restart,consume,unprotect:pool/vm@runtime-upgrade" {
		t.Fatalf("restart calls = %v", calls)
	}
	if !strings.Contains(out.String(), "healthy") {
		t.Fatalf("restart output = %q", out.String())
	}
}

func TestVMRuntimeRestartUpgradeKeepsRecoveryPointProtectedAfterFallback(t *testing.T) {
	server := newTestServer(t)
	configured := vmRuntimeLaunchTestArtifact("v1.16.1", "/configured")
	candidate := vmRuntimeLaunchTestArtifact("v1.17.0", "/candidate")
	seedVMRuntimeRestartService(t, server, configured, &candidate, nil)
	unprotected := false
	server.vmRuntimeRestartDeps = &vmRuntimeRestartDeps{
		wait:     time.Millisecond,
		now:      time.Now,
		snapshot: func(context.Context, *db.Service, io.Writer) (string, error) { return "pool/vm@runtime-upgrade", nil },
		restart:  func(string) error { return nil },
		consume: func(context.Context, string) (vmRuntimeTrialOutcome, error) {
			_, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
				service.VM.Components.Runtime.Staged = nil
				service.VM.Components.Runtime.Trial.State = string(vmRuntimeTrialFailedRolledBack)
				service.VM.Components.Runtime.Trial.LastError = "candidate failed"
				return nil
			})
			return vmRuntimeTrialFailedRolledBack, err
		},
		unprotect: func(context.Context, string) error { unprotected = true; return nil },
	}
	var out bytes.Buffer
	err := server.restartStagedVMRuntime(context.Background(), &out, "devbox")
	if err == nil || !strings.Contains(err.Error(), "failed host readiness") {
		t.Fatalf("fallback error = %v", err)
	}
	if unprotected || !strings.Contains(out.String(), "protected runtime-upgrade recovery point") {
		t.Fatalf("fallback unprotected=%v output=%q", unprotected, out.String())
	}
}

func TestVMRuntimeRestartReconcilesPriorHealthyRecoveryPointBeforeNewTrial(t *testing.T) {
	server := newTestServer(t)
	configured := vmRuntimeLaunchTestArtifact("v1.16.1", "/configured")
	candidate := vmRuntimeLaunchTestArtifact("v1.17.0", "/candidate")
	seedVMRuntimeRestartService(t, server, configured, &candidate, nil)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.VM.Components.Runtime.Trial = &db.VMRuntimeTrialConfig{
			State: string(vmRuntimeTrialHealthy), CandidateID: configured.ID,
			PreviousID: "firecracker-v1.15.0-yeet-v1", RecoveryPoint: "pool/vm@prior-runtime-upgrade",
			StartedAt: "2026-07-20T11:00:00Z",
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var calls []string
	server.vmRuntimeRestartDeps = &vmRuntimeRestartDeps{
		wait: time.Millisecond,
		now:  time.Now,
		unprotect: func(_ context.Context, snapshot string) error {
			calls = append(calls, "unprotect:"+snapshot)
			return nil
		},
		snapshot: func(context.Context, *db.Service, io.Writer) (string, error) {
			calls = append(calls, "snapshot")
			return "pool/vm@new-runtime-upgrade", nil
		},
		restart: func(string) error {
			calls = append(calls, "restart")
			return nil
		},
		consume: func(context.Context, string) (vmRuntimeTrialOutcome, error) {
			calls = append(calls, "consume")
			_, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
				service.VM.Components.Runtime.Previous = cloneVMRuntimeArtifact(&configured)
				service.VM.Components.Runtime.Configured = candidate
				service.VM.Components.Runtime.Staged = nil
				service.VM.Components.Runtime.Trial.State = string(vmRuntimeTrialHealthy)
				return nil
			})
			return vmRuntimeTrialHealthy, err
		},
	}
	if err := server.restartStagedVMRuntime(context.Background(), io.Discard, "devbox"); err != nil {
		t.Fatal(err)
	}
	want := "unprotect:pool/vm@prior-runtime-upgrade,snapshot,restart,consume,unprotect:pool/vm@new-runtime-upgrade"
	if got := strings.Join(calls, ","); got != want {
		t.Fatalf("restart calls = %s, want %s", got, want)
	}
	trial, err := server.vmRuntimeTrialState("devbox")
	if err != nil {
		t.Fatal(err)
	}
	if trial.RecoveryPoint != "" {
		t.Fatalf("new healthy trial retained recovery point: %#v", trial)
	}
}

func TestVMRuntimeRestartUpgradeSerializesConcurrentCallers(t *testing.T) {
	server := newTestServer(t)
	configured := vmRuntimeLaunchTestArtifact("v1.16.1", "/configured")
	candidate := vmRuntimeLaunchTestArtifact("v1.17.0", "/candidate")
	seedVMRuntimeRestartService(t, server, configured, &candidate, nil)
	restartEntered := make(chan struct{})
	releaseRestart := make(chan struct{})
	var restarts atomic.Int32
	server.vmRuntimeRestartDeps = &vmRuntimeRestartDeps{
		wait:     time.Millisecond,
		now:      time.Now,
		snapshot: func(context.Context, *db.Service, io.Writer) (string, error) { return "", nil },
		restart: func(string) error {
			if restarts.Add(1) == 1 {
				close(restartEntered)
			}
			<-releaseRestart
			return nil
		},
		consume: func(context.Context, string) (vmRuntimeTrialOutcome, error) {
			_, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
				service.VM.Components.Runtime.Previous = cloneVMRuntimeArtifact(&configured)
				service.VM.Components.Runtime.Configured = candidate
				service.VM.Components.Runtime.Staged = nil
				service.VM.Components.Runtime.Trial.State = string(vmRuntimeTrialHealthy)
				return nil
			})
			return vmRuntimeTrialHealthy, err
		},
		unprotect: func(context.Context, string) error { return nil },
	}
	errs := make(chan error, 2)
	go func() { errs <- server.restartStagedVMRuntime(context.Background(), io.Discard, "devbox") }()
	<-restartEntered
	go func() { errs <- server.restartStagedVMRuntime(context.Background(), io.Discard, "devbox") }()
	time.Sleep(20 * time.Millisecond)
	if got := restarts.Load(); got != 1 {
		t.Fatalf("concurrent restarts before release = %d", got)
	}
	close(releaseRestart)
	first, second := <-errs, <-errs
	if first != nil && second != nil {
		t.Fatalf("both serialized callers failed: %v; %v", first, second)
	}
	if got := restarts.Load(); got != 1 {
		t.Fatalf("serialized restart count = %d", got)
	}
}

func TestVMRuntimeRollbackStagesExactPersistedPrevious(t *testing.T) {
	server := newTestServer(t)
	configured := vmRuntimeLaunchTestArtifact("v1.17.0", "/configured")
	previous := vmRuntimeLaunchTestArtifact("v1.16.1", "/previous")
	seedVMRuntimeRestartService(t, server, configured, nil, &previous)
	var selected vmRuntimeResolvedTarget
	server.vmRuntimeCommandDeps = &vmRuntimeCommandDeps{
		stageRuntime: func(_ context.Context, _ *Config, service string, target vmRuntimeResolvedTarget) (vmRuntimeTransitionResult, error) {
			if service != "devbox" {
				t.Fatalf("service = %q", service)
			}
			selected = target
			return vmRuntimeTransitionResult{Service: service, Staged: cloneVMRuntimeArtifact(&target.Artifact)}, nil
		},
	}
	if err := server.rollbackVMRuntime(context.Background(), &bytes.Buffer{}, "devbox", false); err != nil {
		t.Fatal(err)
	}
	if selected.Selection != vmRuntimeTargetSelectionPrevious || selected.Artifact != previous {
		t.Fatalf("rollback target = %#v", selected)
	}
}

func TestVMRuntimeUpgradeRecoveryPointWarnsForRawDiskWithoutSnapshot(t *testing.T) {
	server := newTestServer(t)
	service := &db.Service{
		Name: "devbox", ServiceType: db.ServiceTypeVM,
		VM: &db.VMConfig{Disk: db.VMDiskConfig{Backend: vmDiskBackendRaw, Path: "/srv/devbox/rootfs.raw"}},
	}
	var out bytes.Buffer
	name, err := server.createVMRuntimeUpgradeRecoveryPoint(context.Background(), service, &out)
	if err != nil {
		t.Fatal(err)
	}
	if name != "" || !strings.Contains(out.String(), "only launcher rollback is available") {
		t.Fatalf("raw recovery point = %q output=%q", name, out.String())
	}
}

func TestRetryTerminalVMRuntimeTrialWithoutResult(t *testing.T) {
	service := &db.Service{Name: "devbox"}
	retried, err := retryTerminalVMRuntimeTrial(
		t.TempDir(), service, db.VMRuntimeLifecycleConfig{},
		vmRuntimeRestartDeps{}, nil,
	)
	if err != nil || retried {
		t.Fatalf("retry without terminal result = %t, %v", retried, err)
	}
}

func TestPendingVMRuntimeLaunchMarkerWithoutMarker(t *testing.T) {
	root := t.TempDir()
	descriptorPath := filepath.Join(serviceDataDirForRoot(root), vmRuntimeDescriptorFileName)
	if err := os.MkdirAll(filepath.Dir(descriptorPath), 0o700); err != nil {
		t.Fatal(err)
	}
	descriptorDeps := defaultVMRuntimeDescriptorFileDeps()
	descriptorDeps.uid = uint32(os.Geteuid())
	descriptorDeps.gid = uint32(os.Getegid())
	if err := writeVMRuntimeDescriptorWithDeps(descriptorPath, validVMRuntimeDescriptor(), descriptorDeps); err != nil {
		t.Fatal(err)
	}
	control := defaultVMRuntimeControlFileDeps()
	control.uid = uint32(os.Geteuid())
	control.gid = uint32(os.Getegid())
	started, err := pendingVMRuntimeLaunchMarkerStarted(
		root, &db.Service{Name: "devbox"}, db.VMRuntimeLifecycleConfig{},
		vmRuntimeRestartDeps{processAlive: func(int) bool { return true }}, control,
	)
	if err != nil || started {
		t.Fatalf("pending launch without marker = %t, %v", started, err)
	}
}

func TestWatchVMRuntimeTrialResultsUnprotectsHealthyRecoveryPointAfterClientExit(t *testing.T) {
	server := newTestServer(t)
	configured := vmRuntimeLaunchTestArtifact("v1.17.0", "/configured")
	seedVMRuntimeRestartService(t, server, configured, nil, nil)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.VM.Components.Runtime.Trial = &db.VMRuntimeTrialConfig{
			State: string(vmRuntimeTrialHealthy), CandidateID: configured.ID,
			PreviousID: "firecracker-v1.16.1-yeet-v1", RecoveryPoint: "pool/vm@runtime-upgrade",
			StartedAt: "2026-07-20T12:00:00Z",
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var unprotected string
	server.vmRuntimeRestartDeps = &vmRuntimeRestartDeps{
		unprotect: func(_ context.Context, snapshot string) error { unprotected = snapshot; return nil },
	}
	if err := server.consumeVMRuntimeTrialResults(context.Background()); err != nil {
		t.Fatal(err)
	}
	if unprotected != "pool/vm@runtime-upgrade" {
		t.Fatalf("unprotected recovery point = %q", unprotected)
	}
	trial, err := server.vmRuntimeTrialState("devbox")
	if err != nil {
		t.Fatal(err)
	}
	if trial.RecoveryPoint != "" {
		t.Fatalf("reconciled trial still references recovery point: %#v", trial)
	}
}

func seedVMRuntimeRestartService(t *testing.T, server *Server, configured db.VMRuntimeArtifactConfig, staged, previous *db.VMRuntimeArtifactConfig) {
	t.Helper()
	_, err := server.cfg.DB.MutateData(func(data *db.Data) error {
		if data.Services == nil {
			data.Services = map[string]*db.Service{}
		}
		data.Services["devbox"] = &db.Service{
			Name: "devbox", ServiceType: db.ServiceTypeVM,
			VM: &db.VMConfig{
				SetupState: "ready", Disk: db.VMDiskConfig{Backend: vmDiskBackendRaw, Path: "/srv/devbox/rootfs.raw"},
				Components: &db.VMComponentsConfig{Runtime: db.VMRuntimeLifecycleConfig{
					Configured: configured, Staged: cloneVMRuntimeArtifact(staged), Previous: cloneVMRuntimeArtifact(previous),
				}},
			},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
