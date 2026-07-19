// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestVMRuntimeStatusUsesTrustedHostMarkerAndReportsExactDigests(t *testing.T) {
	server := newTestServer(t)
	catalog := validVMRuntimeCatalog()
	ref := catalog.Architectures["amd64"].Runtimes[0]
	artifact := vmRuntimeCommandArtifact(ref, "official")
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: artifact})
	writeVMRuntimeStatusMarker(t, server, "devbox", artifact, 4211, 4212)
	server.vmRuntimeCommandDeps = vmRuntimeStatusTestDeps(catalog, vmRuntimeUnitState{ActiveState: "active", MainPID: 4211}, func(pid int) bool {
		return pid == 4211 || pid == 4212
	})

	var out bytes.Buffer
	if err := server.printVMRuntimeStatus(context.Background(), &out, "devbox", "json"); err != nil {
		t.Fatal(err)
	}
	var rows []vmRuntimeStatusRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("decode status: %v\n%s", err, out.String())
	}
	if len(rows) != 1 || rows[0].Running == nil {
		t.Fatalf("rows = %#v", rows)
	}
	row := rows[0]
	if row.State != "current" || row.Running.ID != artifact.ID || row.Running.ManifestSHA256 != artifact.ManifestSHA256 ||
		row.Running.FirecrackerSHA256 != artifact.FirecrackerSHA256 || row.Running.JailerSHA256 != artifact.JailerSHA256 {
		t.Fatalf("status row = %#v", row)
	}
	if !strings.Contains(out.String(), artifact.FirecrackerSHA256) || !strings.Contains(out.String(), artifact.JailerSHA256) {
		t.Fatalf("status omitted exact component digests: %s", out.String())
	}
}

func TestVMRuntimeStatusDoesNotConsultGuestAgent(t *testing.T) {
	oldReady := queryVMGuestReadyFn
	oldNetwork := queryVMNetworkStateFn
	queryVMGuestReadyFn = func(context.Context, string) (vmAgentGuestReadyState, error) {
		t.Fatal("runtime status queried the guest readiness agent")
		return vmAgentGuestReadyState{}, nil
	}
	queryVMNetworkStateFn = func(context.Context, string) (vmAgentNetworkState, error) {
		t.Fatal("runtime status queried guest network state")
		return vmAgentNetworkState{}, nil
	}
	t.Cleanup(func() {
		queryVMGuestReadyFn = oldReady
		queryVMNetworkStateFn = oldNetwork
	})

	server := newTestServer(t)
	catalog := validVMRuntimeCatalog()
	artifact := vmRuntimeCommandArtifact(catalog.Architectures["amd64"].Runtimes[0], "official")
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: artifact})
	server.vmRuntimeCommandDeps = vmRuntimeStatusTestDeps(catalog, vmRuntimeUnitState{ActiveState: "inactive"}, func(int) bool { return false })
	if _, err := server.vmRuntimeStatusRows(context.Background(), "devbox"); err != nil {
		t.Fatal(err)
	}
}

func TestVMRuntimeStatusRejectsRunningMarkerDivergence(t *testing.T) {
	server := newTestServer(t)
	catalog := validVMRuntimeCatalog()
	artifact := vmRuntimeCommandArtifact(catalog.Architectures["amd64"].Runtimes[0], "official")
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: artifact})
	markerArtifact := artifact
	markerArtifact.JailerSHA256 = strings.Repeat("c", 64)
	writeVMRuntimeStatusMarker(t, server, "devbox", markerArtifact, 4211, 4212)
	server.vmRuntimeCommandDeps = vmRuntimeStatusTestDeps(catalog, vmRuntimeUnitState{ActiveState: "active", MainPID: 4211}, func(int) bool { return true })

	rows, err := server.vmRuntimeStatusRows(context.Background(), "devbox")
	if err != nil {
		t.Fatal(err)
	}
	if got := rows[0].State; got != "running-config-diverged" {
		t.Fatalf("state = %q, want running-config-diverged", got)
	}
	if rows[0].Running == nil || rows[0].Running.JailerSHA256 != markerArtifact.JailerSHA256 || rows[0].Running.Source != "host-marker" {
		t.Fatalf("divergent host marker identity was hidden: %#v", rows[0].Running)
	}
}

func TestVMRuntimeStatusReportsStaleFirecrackerChild(t *testing.T) {
	server := newTestServer(t)
	catalog := validVMRuntimeCatalog()
	artifact := vmRuntimeCommandArtifact(catalog.Architectures["amd64"].Runtimes[0], "official")
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: artifact})
	writeVMRuntimeStatusMarker(t, server, "devbox", artifact, 4211, 4212)
	server.vmRuntimeCommandDeps = vmRuntimeStatusTestDeps(catalog, vmRuntimeUnitState{ActiveState: "active", MainPID: 4211}, func(pid int) bool {
		return pid == 4211
	})
	rows, err := server.vmRuntimeStatusRows(context.Background(), "devbox")
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].State != "stale-child" || rows[0].Running != nil {
		t.Fatalf("stale child status = %#v", rows[0])
	}
}

func TestVMRuntimeStatusChecksChildBeforeDivergentMarkerIdentity(t *testing.T) {
	server := newTestServer(t)
	catalog := validVMRuntimeCatalog()
	artifact := vmRuntimeCommandArtifact(catalog.Architectures["amd64"].Runtimes[0], "official")
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: artifact})
	divergent := artifact
	divergent.JailerSHA256 = strings.Repeat("d", 64)
	writeVMRuntimeStatusMarker(t, server, "devbox", divergent, 4211, 4212)
	server.vmRuntimeCommandDeps = vmRuntimeStatusTestDeps(catalog, vmRuntimeUnitState{ActiveState: "active", MainPID: 4211}, func(pid int) bool {
		return pid == 4211
	})
	rows, err := server.vmRuntimeStatusRows(context.Background(), "devbox")
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].State != "stale-child" || rows[0].Running != nil {
		t.Fatalf("dead divergent child status = %#v", rows[0])
	}
}

func TestVMRuntimeStatusClassifiesMissingAndStaleMarkers(t *testing.T) {
	catalog := validVMRuntimeCatalog()
	for _, test := range []struct {
		name      string
		unit      vmRuntimeUnitState
		alive     bool
		wantState string
	}{
		{name: "missing marker", unit: vmRuntimeUnitState{ActiveState: "active", MainPID: 4211}, alive: true, wantState: "missing-or-untrusted-marker"},
		{name: "stale unit PID", unit: vmRuntimeUnitState{ActiveState: "active", MainPID: 4211}, alive: false, wantState: "stale-runner"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t)
			artifact := vmRuntimeCommandArtifact(catalog.Architectures["amd64"].Runtimes[0], "official")
			seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: artifact})
			server.vmRuntimeCommandDeps = vmRuntimeStatusTestDeps(catalog, test.unit, func(int) bool { return test.alive })
			rows, err := server.vmRuntimeStatusRows(context.Background(), "devbox")
			if err != nil {
				t.Fatal(err)
			}
			if rows[0].State != test.wantState {
				t.Fatalf("state = %q, want %q", rows[0].State, test.wantState)
			}
		})
	}
}

func TestVMRuntimeStatusReportsLegacyAndCatalogUnavailableWithoutGuestInput(t *testing.T) {
	server := newTestServer(t)
	catalog := validVMRuntimeCatalog()
	artifact := vmRuntimeCommandArtifact(catalog.Architectures["amd64"].Runtimes[0], "official-legacy")
	artifact.ManifestSHA256 = ""
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: artifact})
	deps := vmRuntimeStatusTestDeps(catalog, vmRuntimeUnitState{ActiveState: "inactive"}, func(int) bool { return false })
	server.vmRuntimeCommandDeps = deps
	rows, err := server.vmRuntimeStatusRows(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].State != "legacy-unlisted" || rows[0].Configured.Support != "legacy-unlisted" {
		t.Fatalf("legacy status = %#v", rows[0])
	}

	server.vmRuntimeCommandDeps.fetchCatalog = func(context.Context) (vmRuntimeCatalog, error) {
		return vmRuntimeCatalog{}, os.ErrDeadlineExceeded
	}
	rows, err = server.vmRuntimeStatusRows(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Configured.Support != "catalog-unavailable" || rows[0].RecommendedAction == "" {
		t.Fatalf("catalog-unavailable status = %#v", rows[0])
	}
}

func TestVMRuntimeStatusClassifiesCatalogSupportAndPromotion(t *testing.T) {
	for _, test := range []struct {
		name       string
		support    string
		trial      *db.VMRuntimeTrialConfig
		wantState  string
		wantAction bool
	}{
		{name: "supported update", support: "supported", wantState: "update-available", wantAction: true},
		{name: "deprecated", support: "deprecated", wantState: "deprecated", wantAction: true},
		{name: "end of life", support: "eol", wantState: "eol", wantAction: true},
		{name: "revoked", support: "revoked", wantState: "revoked", wantAction: true},
		{name: "failed trial", support: "supported", trial: &db.VMRuntimeTrialConfig{State: "failed", StartedAt: "2026-07-20T00:00:00Z", LastError: "host readiness failed"}, wantState: "failed", wantAction: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t)
			catalog := validVMRuntimeCatalog()
			stable := catalog.Architectures["amd64"].Runtimes[0]
			old := stable
			old.RuntimeID = "firecracker-v1.15.0-yeet-v1"
			old.UpstreamVersion = "v1.15.0"
			old.ManifestSHA = strings.Repeat("4", 64)
			old.Support = test.support
			architecture := catalog.Architectures["amd64"]
			architecture.Runtimes = append(architecture.Runtimes, old)
			catalog.Architectures["amd64"] = architecture
			seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{
				Configured: vmRuntimeCommandArtifact(old, "official"),
				Trial:      test.trial,
			})
			server.vmRuntimeCommandDeps = vmRuntimeStatusTestDeps(catalog, vmRuntimeUnitState{ActiveState: "inactive"}, func(int) bool { return false })
			rows, err := server.vmRuntimeStatusRows(context.Background(), "devbox")
			if err != nil {
				t.Fatal(err)
			}
			if rows[0].State != test.wantState || (rows[0].RecommendedAction != "") != test.wantAction {
				t.Fatalf("status = %#v, want state %q action=%v", rows[0], test.wantState, test.wantAction)
			}
		})
	}
}

func TestVMRuntimeStatusReportsRunningStagedTrial(t *testing.T) {
	server := newTestServer(t)
	catalog := validVMRuntimeCatalog()
	configuredRef := catalog.Architectures["amd64"].Runtimes[0]
	configured := vmRuntimeCommandArtifact(configuredRef, "official")
	staged := configured
	staged.ID = "firecracker-v1.17.0-yeet-v1"
	staged.ManifestSHA256 = strings.Repeat("5", 64)
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: configured, Staged: &staged})
	writeVMRuntimeStatusMarker(t, server, "devbox", staged, 4211, 4212)
	server.vmRuntimeCommandDeps = vmRuntimeStatusTestDeps(catalog, vmRuntimeUnitState{ActiveState: "active", MainPID: 4211}, func(int) bool { return true })
	rows, err := server.vmRuntimeStatusRows(context.Background(), "devbox")
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].State != "trial" || rows[0].Running == nil || rows[0].Running.ID != staged.ID {
		t.Fatalf("trial status = %#v", rows[0])
	}
}

func TestVMRuntimeStatusKeepsJailerReadinessSeparateFromCurrency(t *testing.T) {
	for _, test := range []struct {
		name       string
		readiness  vmJailerReadiness
		readErr    error
		wantJailer string
	}{
		{name: "pending", readiness: vmJailerPendingRestart, wantJailer: string(vmJailerPendingRestart)},
		{name: "error", readErr: os.ErrPermission, wantJailer: "error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t)
			catalog := validVMRuntimeCatalog()
			artifact := vmRuntimeCommandArtifact(catalog.Architectures["amd64"].Runtimes[0], "official")
			seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: artifact})
			deps := vmRuntimeStatusTestDeps(catalog, vmRuntimeUnitState{ActiveState: "inactive"}, func(int) bool { return false })
			deps.jailerState = func(string, uint32, uint32) (vmJailerReadiness, error) { return test.readiness, test.readErr }
			server.vmRuntimeCommandDeps = deps
			rows, err := server.vmRuntimeStatusRows(context.Background(), "devbox")
			if err != nil {
				t.Fatal(err)
			}
			if rows[0].State != "stopped" || rows[0].JailerIsolation != test.wantJailer || rows[0].RecommendedAction == "" {
				t.Fatalf("jailer-separate status = %#v", rows[0])
			}
		})
	}
}

func TestVMRuntimeStatusUsesEffectiveCandidatePromotion(t *testing.T) {
	server := newTestServer(t)
	catalog := validVMRuntimeCatalog()
	stable := catalog.Architectures["amd64"].Runtimes[0]
	candidate := stable
	candidate.RuntimeID = "firecracker-v1.17.0-yeet-v1"
	candidate.ManifestSHA = strings.Repeat("6", 64)
	candidate.UpstreamVersion = "v1.17.0"
	architecture := catalog.Architectures["amd64"]
	architecture.Runtimes = append(architecture.Runtimes, candidate)
	architecture.Channels["candidate"] = &vmRuntimeCatalogIdentity{RuntimeID: candidate.RuntimeID, ManifestSHA: candidate.ManifestSHA}
	catalog.Architectures["amd64"] = architecture
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{
		Configured: vmRuntimeCommandArtifact(stable, "official"),
		Policy:     "manual",
		Channel:    "candidate",
	})
	server.vmRuntimeCommandDeps = vmRuntimeStatusTestDeps(catalog, vmRuntimeUnitState{ActiveState: "inactive"}, func(int) bool { return false })
	rows, err := server.vmRuntimeStatusRows(context.Background(), "devbox")
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Channel != "candidate" || rows[0].LatestPromoted == nil || rows[0].LatestPromoted.ID != candidate.RuntimeID || rows[0].State != "update-available" {
		t.Fatalf("candidate status = %#v", rows[0])
	}
}

func TestReadTrustedVMRuntimeRunningMarkerRejectsSymlinkAndWrongMode(t *testing.T) {
	root := t.TempDir()
	runDir := serviceRunDirForRoot(root)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	artifact := db.VMRuntimeArtifactConfig{
		ID: "firecracker-v1.16.1-yeet-v1", ManifestSHA256: strings.Repeat("1", 64),
		FirecrackerSHA256: strings.Repeat("2", 64), JailerSHA256: strings.Repeat("3", 64),
	}
	raw := marshalVMRuntimeStatusMarker(t, "devbox", artifact, 11, 12)
	target := filepath.Join(runDir, "target")
	if err := os.WriteFile(target, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runDir, vmRuntimeRunningMarkerFileName)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := readTrustedVMRuntimeRunningMarker(path, "devbox", uint32(os.Geteuid()), uint32(os.Getegid())); err == nil {
		t.Fatal("symlink marker accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readTrustedVMRuntimeRunningMarker(path, "devbox", uint32(os.Geteuid()), uint32(os.Getegid())); err == nil {
		t.Fatal("wrong-mode marker accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readTrustedVMRuntimeRunningMarker(path, "devbox", uint32(os.Geteuid()+1), uint32(os.Getegid())); err == nil {
		t.Fatal("wrong-owner marker accepted")
	}
}

func TestVMRuntimeStatusRenderingIsDeterministic(t *testing.T) {
	rows := []vmRuntimeStatusRow{
		{Service: "alpha", Configured: vmRuntimeStatusIdentity{ID: "runtime", ManifestSHA256: strings.Repeat("1", 64)}, Policy: "manual", Channel: "stable", JailerIsolation: "jailer", State: "stopped"},
	}
	var first, second bytes.Buffer
	if err := renderVMRuntimeStatus(&first, "table", rows); err != nil {
		t.Fatal(err)
	}
	if err := renderVMRuntimeStatus(&second, "table", rows); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() || !strings.Contains(first.String(), "runtime@"+strings.Repeat("1", 64)) {
		t.Fatalf("non-deterministic or incomplete table:\n%s", first.String())
	}
}

func vmRuntimeStatusTestDeps(catalog vmRuntimeCatalog, unit vmRuntimeUnitState, alive func(int) bool) *vmRuntimeCommandDeps {
	return &vmRuntimeCommandDeps{
		fetchCatalog: func(context.Context) (vmRuntimeCatalog, error) { return catalog, nil },
		unitState:    func(context.Context, string) (vmRuntimeUnitState, error) { return unit, nil },
		processAlive: alive,
		jailerState: func(string, uint32, uint32) (vmJailerReadiness, error) {
			return vmJailerReady, nil
		},
		expectedUID: uint32(os.Geteuid()),
		expectedGID: uint32(os.Getegid()),
	}
}

func writeVMRuntimeStatusMarker(t *testing.T, server *Server, service string, artifact db.VMRuntimeArtifactConfig, runnerPID, childPID int) {
	t.Helper()
	root, err := server.serviceRootDir(service)
	if err != nil {
		t.Fatal(err)
	}
	runDir := serviceRunDirForRoot(root)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runDir, vmRuntimeRunningMarkerFileName)
	if err := os.WriteFile(path, marshalVMRuntimeStatusMarker(t, service, artifact, runnerPID, childPID), 0o600); err != nil {
		t.Fatal(err)
	}
}

func marshalVMRuntimeStatusMarker(t *testing.T, service string, artifact db.VMRuntimeArtifactConfig, runnerPID, childPID int) []byte {
	t.Helper()
	raw, err := json.Marshal(vmRuntimeRunningMarker{
		SchemaVersion: vmRuntimeRunningMarkerSchemaVersion, Service: service,
		DescriptorSHA256: strings.Repeat("d", 64), LaunchID: strings.Repeat("e", 64),
		RuntimeID: artifact.ID, ManifestSHA256: artifact.ManifestSHA256,
		FirecrackerSHA256: artifact.FirecrackerSHA256, JailerSHA256: artifact.JailerSHA256,
		RunnerPID: runnerPID, ChildPID: childPID, StartedAt: time.Unix(1_700_000_000, 0).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}
