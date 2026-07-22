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
	"unicode/utf8"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/serviceid"
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

func TestVMStatusComponents(t *testing.T) {
	server := newTestServer(t)
	catalog := validVMRuntimeCatalog()
	configured := vmRuntimeCommandArtifact(catalog.Architectures["amd64"].Runtimes[0], "official")
	staged := configured
	staged.ID = "firecracker-v1.17.0-yeet-v1"
	staged.ManifestSHA256 = strings.Repeat("9", 64)
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: configured, Staged: &staged})
	guest := db.VMGuestBaseConfig{ID: "guest-ubuntu-26.04-amd64-v4", ManifestSHA256: strings.Repeat("a", 64), Source: "official", RootFSProvenance: strings.Repeat("b", 64)}
	kernel := db.VMKernelArtifactConfig{ID: "kernel-linux-7.1.1-yeet-v2", ManifestSHA256: strings.Repeat("c", 64), SHA256: strings.Repeat("d", 64), Path: "/srv/devbox/data/kernels/vmlinux", Source: "official"}
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.VM.Components.GuestBase = guest
		service.VM.Components.Kernel = kernel
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	server.vmRuntimeCommandDeps = vmRuntimeStatusTestDeps(catalog, vmRuntimeUnitState{ActiveState: "inactive"}, func(int) bool { return false })

	rows, err := server.vmRuntimeStatusRows(context.Background(), "devbox")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].GuestBase.ID != guest.ID || rows[0].GuestBase.ManifestSHA256 != guest.ManifestSHA256 ||
		rows[0].Kernel.ID != kernel.ID || rows[0].Kernel.ManifestSHA256 != kernel.ManifestSHA256 || rows[0].Kernel.SHA256 != kernel.SHA256 ||
		rows[0].Configured.ID != configured.ID || rows[0].Staged == nil || rows[0].Staged.ID != staged.ID {
		t.Fatalf("component status = %#v", rows)
	}
	running := rows[0].Configured
	rows[0].Running = &running
	rows[0].Previous = &vmRuntimeStatusIdentity{
		ID:             "legacy-firecracker-1-14-3-" + strings.Repeat("e", 64) + "-jailer-" + strings.Repeat("f", 64),
		ManifestSHA256: strings.Repeat("8", 64),
		Source:         "custom-legacy",
		Support:        "legacy-unlisted",
	}
	rows[0].State = "current"
	rows[0].LastTransition = "2026-07-22T15:40:39.509530928Z"

	var out bytes.Buffer
	if err := renderVMRuntimeStatus(&out, "table", rows, vmRuntimeStatusDetailView); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"devbox  healthy",
		"Guest base:  ubuntu-26.04-amd64-v4 (official)",
		"Kernel:      linux-7.1.1-yeet-v2 (official)",
		"Runtime",
		"Running:     v1.16.1 / yeet-v1",
		"Configured:  same as running",
		"Staged:      v1.17.0",
		"Previous:    v1.14.3 [888888888888] (custom legacy)",
		"Policy:      manual / stable",
		"Isolation:   jailer",
		"Last change: 2026-07-22 15:40:39 UTC",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("detail status missing %q:\n%s", want, out.String())
		}
	}
	for _, digest := range []string{guest.ManifestSHA256, kernel.ManifestSHA256, configured.ManifestSHA256, staged.ManifestSHA256, strings.Repeat("8", 64)} {
		if digest != "" && strings.Contains(out.String(), digest) {
			t.Fatalf("detail status contains full digest %q:\n%s", digest, out.String())
		}
	}
}

func TestVMRuntimeStatusDetailWrapsRecommendedAction(t *testing.T) {
	row := vmRuntimeStatusRow{
		Service:    "devbox",
		Configured: vmRuntimeStatusIdentity{ID: "runtime", Source: "future-source"},
		Policy:     "manual", Channel: "stable", JailerIsolation: "jailer",
		State:             "missing-or-untrusted-marker",
		RecommendedAction: "restart the VM when downtime is acceptable to establish a trusted runtime marker and verify the selected host runtime",
	}
	var out bytes.Buffer
	if err := renderVMRuntimeStatus(&out, "table", []vmRuntimeStatusRow{row}, vmRuntimeStatusDetailView); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if len([]rune(line)) > 100 {
			t.Fatalf("detail line is %d columns: %q", len([]rune(line)), line)
		}
	}
	if !strings.Contains(out.String(), "Action:") {
		t.Fatalf("detail status omitted action:\n%s", out.String())
	}
}

func TestVMRuntimeStatusDetailWrapsOversizedActionToken(t *testing.T) {
	row := vmRuntimeStatusRow{
		Service:           "devbox",
		Configured:        vmRuntimeStatusIdentity{ID: "runtime"},
		RecommendedAction: strings.Repeat("x", 92),
	}
	var out bytes.Buffer
	if err := renderVMRuntimeStatus(&out, "table", []vmRuntimeStatusRow{row}, vmRuntimeStatusDetailView); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if len([]rune(line)) > vmRuntimeStatusHumanWidth {
			t.Fatalf("detail line is %d columns: %q", len([]rune(line)), line)
		}
	}
}

func TestVMRuntimeStatusJSONIgnoresHumanView(t *testing.T) {
	row := vmRuntimeStatusRow{
		Service: "devbox",
		Configured: vmRuntimeStatusIdentity{
			ID:                "firecracker-v1.16.1-yeet-v1",
			ManifestSHA256:    strings.Repeat("1", 64),
			FirecrackerSHA256: strings.Repeat("2", 64),
			JailerSHA256:      strings.Repeat("3", 64),
			Source:            "official",
			UpstreamVersion:   "v1.16.1",
			Support:           "supported",
		},
	}

	var fleet, detail bytes.Buffer
	if err := renderVMRuntimeStatus(&fleet, "json", []vmRuntimeStatusRow{row}, vmRuntimeStatusFleetView); err != nil {
		t.Fatal(err)
	}
	if err := renderVMRuntimeStatus(&detail, "json", []vmRuntimeStatusRow{row}, vmRuntimeStatusDetailView); err != nil {
		t.Fatal(err)
	}
	if fleet.String() != detail.String() {
		t.Fatalf("JSON changed with human view:\nfleet: %s\ndetail: %s", fleet.String(), detail.String())
	}
	for _, exact := range []string{row.Configured.ID, row.Configured.ManifestSHA256, row.Configured.FirecrackerSHA256, row.Configured.JailerSHA256} {
		if !strings.Contains(fleet.String(), exact) {
			t.Fatalf("JSON omitted exact identity %q: %s", exact, fleet.String())
		}
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
	var out bytes.Buffer
	if err := renderVMRuntimeStatus(&out, "table", rows, vmRuntimeStatusDetailView); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Configured:  same as running") {
		t.Fatalf("divergent runtime rendered configured as running:\n%s", out.String())
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

func TestVMRuntimeStatusFleetRenderingIsCompactAndDeterministic(t *testing.T) {
	promoted := &vmRuntimeStatusIdentity{
		ID: "firecracker-v1.16.1-yeet-v1", ManifestSHA256: strings.Repeat("1", 64),
		Source: "official", UpstreamVersion: "v1.16.1", Support: "supported",
	}
	legacy := vmRuntimeStatusIdentity{
		ID:             "legacy-firecracker-1-14-3-" + strings.Repeat("2", 64) + "-jailer-" + strings.Repeat("3", 64),
		ManifestSHA256: strings.Repeat("4", 64), Source: "custom-legacy", Support: "legacy-unlisted",
	}
	official := *promoted
	rows := []vmRuntimeStatusRow{
		{
			Service: "alpha", Configured: legacy, Policy: "manual", Channel: "stable",
			LatestPromoted: promoted, JailerIsolation: "jailer", State: "missing-or-untrusted-marker",
			RecommendedAction: "restart the VM when downtime is acceptable to establish a trusted runtime marker",
		},
		{
			Service: "beta", Running: &official, Configured: official, Policy: "manual", Channel: "stable",
			LatestPromoted: promoted, JailerIsolation: "jailer", State: "current",
		},
	}
	var first, second bytes.Buffer
	if err := renderVMRuntimeStatus(&first, "table", rows, vmRuntimeStatusFleetView); err != nil {
		t.Fatal(err)
	}
	if err := renderVMRuntimeStatus(&second, "table", rows, vmRuntimeStatusFleetView); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatalf("fleet output is non-deterministic:\n%s\n%s", first.String(), second.String())
	}
	for _, want := range []string{
		"VM", "RUNNING", "CONFIGURED", "STAGED", "POLICY", "STATE",
		"alpha", "unverified", "1.14.3 legacy", "marker unverified",
		"beta", "1.16.1 official", "healthy",
		"Promoted stable runtime: 1.16.1 / yeet-v1",
		"Needs attention:", "alpha: Restart the VM",
	} {
		if !strings.Contains(first.String(), want) {
			t.Fatalf("fleet status missing %q:\n%s", want, first.String())
		}
	}
	for _, forbidden := range []string{strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64), "GUEST BASE", "KERNEL", "PREVIOUS", "ACTION"} {
		if strings.Contains(first.String(), forbidden) {
			t.Fatalf("fleet status contains %q:\n%s", forbidden, first.String())
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(first.String(), "\n"), "\n") {
		if len([]rune(line)) > vmRuntimeStatusHumanWidth {
			t.Fatalf("fleet line is %d columns: %q", len([]rune(line)), line)
		}
	}
}

func TestVMRuntimeStatusFleetOmitsUnsharedPromotion(t *testing.T) {
	stable := &vmRuntimeStatusIdentity{ID: "firecracker-v1.16.1-yeet-v1", ManifestSHA256: strings.Repeat("1", 64), UpstreamVersion: "v1.16.1", Source: "official"}
	candidate := &vmRuntimeStatusIdentity{ID: "firecracker-v1.17.0-yeet-v1", ManifestSHA256: strings.Repeat("2", 64), UpstreamVersion: "v1.17.0", Source: "official"}
	rows := []vmRuntimeStatusRow{
		{Service: "alpha", Configured: *stable, Policy: "manual", Channel: "stable", LatestPromoted: stable},
		{Service: "beta", Configured: *candidate, Policy: "manual", Channel: "candidate", LatestPromoted: candidate},
	}
	var out bytes.Buffer
	if err := renderVMRuntimeStatus(&out, "table", rows, vmRuntimeStatusFleetView); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Promoted ") {
		t.Fatalf("fleet status implied an unshared promotion:\n%s", out.String())
	}
}

func TestVMRuntimeStatusHumanIdentityLabels(t *testing.T) {
	official := &vmRuntimeStatusIdentity{
		ID:              "firecracker-v1.16.1-yeet-v1",
		ManifestSHA256:  strings.Repeat("a", 64),
		Source:          "official",
		UpstreamVersion: "v1.16.1",
		Support:         "supported",
	}
	legacy := &vmRuntimeStatusIdentity{
		ID:             "legacy-firecracker-1-14-3-" + strings.Repeat("b", 64) + "-jailer-" + strings.Repeat("c", 64),
		ManifestSHA256: strings.Repeat("d", 64),
		Source:         "custom-legacy",
		Support:        "legacy-unlisted",
	}
	unknown := &vmRuntimeStatusIdentity{
		ID:     strings.Repeat("unexpected-identity-", 5),
		Source: "future-source",
	}

	if got, want := vmRuntimeStatusRuntimeSummary(official), "1.16.1 official"; got != want {
		t.Fatalf("official summary = %q, want %q", got, want)
	}
	if got, want := vmRuntimeStatusRuntimeDetail(official, true), "v1.16.1 / yeet-v1 [aaaaaaaaaaaa] (official, supported)"; got != want {
		t.Fatalf("official detail = %q, want %q", got, want)
	}
	if got, want := vmRuntimeStatusPromotionSummary(official), "1.16.1 / yeet-v1"; got != want {
		t.Fatalf("promotion summary = %q, want %q", got, want)
	}
	if got, want := vmRuntimeStatusPromotionSummary(&vmRuntimeStatusIdentity{ID: "vendor-runtime"}), "vendor-runtime"; got != want {
		t.Fatalf("fallback promotion summary = %q, want %q", got, want)
	}
	if got, want := vmRuntimeStatusRuntimeSummary(legacy), "1.14.3 legacy"; got != want {
		t.Fatalf("legacy summary = %q, want %q", got, want)
	}
	if got, want := vmRuntimeStatusRuntimeDetail(legacy, true), "v1.14.3 [dddddddddddd] (custom legacy)"; got != want {
		t.Fatalf("legacy detail = %q, want %q", got, want)
	}
	if got := vmRuntimeStatusRuntimeSummary(nil); got != "-" {
		t.Fatalf("nil summary = %q, want -", got)
	}
	if got := vmRuntimeStatusRuntimeSummary(unknown); len(got) > 64 || !strings.Contains(got, "future source") {
		t.Fatalf("unknown summary is not bounded and attributable: %q", got)
	}
	malformedDigest := strings.Repeat("e", 64)
	malformed := &vmRuntimeStatusIdentity{
		ID:              "firecracker-v1.16.1-" + malformedDigest,
		ManifestSHA256:  strings.Repeat("f", 64),
		Source:          "official",
		UpstreamVersion: "v1.16.1",
	}
	for name, got := range map[string]string{
		"detail":    vmRuntimeStatusRuntimeDetail(malformed, true),
		"promotion": vmRuntimeStatusPromotionSummary(malformed),
	} {
		if strings.Contains(got, malformedDigest) {
			t.Fatalf("malformed %s leaked full digest: %q", name, got)
		}
	}
	overlongMetadata := strings.Repeat("future-source-", 8)
	overlongSupport := strings.Repeat("future-support-", 8)
	metadataIdentity := &vmRuntimeStatusIdentity{
		ID:              "firecracker-v1.16.1-yeet-v1",
		ManifestSHA256:  strings.Repeat("1", 64),
		Source:          overlongMetadata,
		UpstreamVersion: "v1.16.1",
		Support:         overlongSupport,
	}
	for name, got := range map[string]string{
		"source summary": vmRuntimeStatusSourceSummary(overlongMetadata),
		"source detail":  vmRuntimeStatusSourceDetail(overlongMetadata),
		"support detail": vmRuntimeStatusSupportDetail(overlongSupport),
	} {
		if len(got) > vmRuntimeStatusFallbackIDMax {
			t.Fatalf("%s is not bounded: %q", name, got)
		}
	}
	if got := vmRuntimeStatusRuntimeDetail(metadataIdentity, false); strings.Contains(got, overlongMetadata) || strings.Contains(got, overlongSupport) {
		t.Fatalf("runtime detail leaked unbounded metadata: %q", got)
	}
	untrustedVersion := strings.Repeat("9", 64)
	untrustedVersionIdentity := &vmRuntimeStatusIdentity{
		ID:              strings.Repeat("unexpected-runtime-", 4),
		UpstreamVersion: untrustedVersion,
	}
	for name, got := range map[string]string{
		"summary":   vmRuntimeStatusRuntimeSummary(untrustedVersionIdentity),
		"detail":    vmRuntimeStatusRuntimeDetail(untrustedVersionIdentity, false),
		"promotion": vmRuntimeStatusPromotionSummary(untrustedVersionIdentity),
	} {
		if strings.Contains(got, untrustedVersion) || len(got) > vmRuntimeStatusFallbackIDMax {
			t.Fatalf("untrusted upstream version leaked into %s: %q", name, got)
		}
	}
}

func TestVMRuntimeStatusHumanLabelsBoundRecognizedTokensAndMetadata(t *testing.T) {
	const (
		humanSummaryMax = 48
		humanLabelMax   = 64
	)
	digest := strings.Repeat("7", 64)
	official := &vmRuntimeStatusIdentity{
		ID:              "firecracker-v" + digest + ".2.3-yeet-v" + digest,
		UpstreamVersion: "v" + digest + ".2.3",
		Source:          "official",
		Support:         "supported",
	}
	legacy := &vmRuntimeStatusIdentity{
		ID:      "legacy-firecracker-" + digest + "-2-3-" + strings.Repeat("a", 64) + "-jailer-" + strings.Repeat("b", 64),
		Source:  "custom-legacy",
		Support: "legacy-unlisted",
	}
	metadata := &vmRuntimeStatusIdentity{
		ID:              "firecracker-v1.16.1-yeet-v1",
		UpstreamVersion: "v1.16.1",
		Source:          "future-" + strings.Repeat("c", 64),
		Support:         "future-" + strings.Repeat("d", 64),
	}

	for name, identity := range map[string]*vmRuntimeStatusIdentity{
		"official": official,
		"legacy":   legacy,
		"metadata": metadata,
	} {
		for format, result := range map[string]struct {
			value string
			limit int
		}{
			"summary":   {value: vmRuntimeStatusRuntimeSummary(identity), limit: humanSummaryMax},
			"detail":    {value: vmRuntimeStatusRuntimeDetail(identity, false), limit: humanLabelMax},
			"promotion": {value: vmRuntimeStatusPromotionSummary(identity), limit: humanLabelMax},
		} {
			if len([]rune(result.value)) > result.limit {
				t.Fatalf("%s %s is %d runes, want at most %d: %q", name, format, len([]rune(result.value)), result.limit, result.value)
			}
			for _, forbidden := range []string{digest, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)} {
				if strings.Contains(result.value, forbidden) {
					t.Fatalf("%s %s leaked digest-shaped token %q: %q", name, format, forbidden, result.value)
				}
			}
		}
	}
	if got := vmRuntimeStatusRuntimeSummary(metadata); !strings.Contains(got, "future") {
		t.Fatalf("bounded metadata summary lost provenance: %q", got)
	}
	if got := vmRuntimeStatusRuntimeDetail(metadata, false); !strings.Contains(got, "future") {
		t.Fatalf("bounded metadata detail lost provenance: %q", got)
	}

	rows := []vmRuntimeStatusRow{
		{Service: "official", Configured: *official, State: "current"},
		{Service: "legacy", Configured: *legacy, State: "current"},
		{Service: "metadata", Configured: *metadata, State: "current"},
	}
	var out bytes.Buffer
	if err := renderVMRuntimeStatus(&out, "table", rows, vmRuntimeStatusFleetView); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if len([]rune(line)) > vmRuntimeStatusHumanWidth {
			t.Fatalf("oversized identity widened fleet line to %d runes: %q", len([]rune(line)), line)
		}
	}
	for _, forbidden := range []string{digest, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)} {
		if strings.Contains(out.String(), forbidden) {
			t.Fatalf("fleet output leaked digest-shaped token %q:\n%s", forbidden, out.String())
		}
	}
}

func TestVMRuntimeStatusHumanSanitizesUntrustedText(t *testing.T) {
	invalid := string([]byte{'b', 'a', 'd', 0xff, 'v', 'a', 'l', 'u', 'e'})
	control := "first\tsecond\nforged\rline\x1b[31m"
	identity := &vmRuntimeStatusIdentity{
		ID:             control + invalid,
		ManifestSHA256: "abcdef012345-not-a-sha256",
		Source:         control + invalid,
		Support:        control + invalid,
	}
	component := vmComponentStatusIdentity{ID: control + invalid, Source: control + invalid}
	row := vmRuntimeStatusRow{
		Service: control + invalid, Configured: *identity,
		Policy: control + invalid, Channel: control + invalid,
		JailerIsolation: control + invalid, State: control + invalid,
		LastTransition: control + invalid, RecommendedAction: control + invalid,
	}

	values := map[string]string{
		"summary":    vmRuntimeStatusRuntimeSummary(identity),
		"detail":     vmRuntimeStatusRuntimeDetail(identity, true),
		"promotion":  vmRuntimeStatusPromotionSummary(identity),
		"component":  vmRuntimeStatusComponentSummary(component),
		"state":      vmRuntimeStatusHumanState(control + invalid),
		"policy":     vmRuntimeStatusPolicyDisplay(row),
		"transition": vmRuntimeStatusTransitionDisplay(control + invalid),
		"value":      vmRuntimeStatusValueOrDash(control + invalid),
		"action":     sentenceCaseVMRuntimeStatusAction(control + invalid),
	}
	for name, got := range values {
		if !utf8.ValidString(got) {
			t.Fatalf("%s emitted invalid UTF-8: %q", name, got)
		}
		if strings.ContainsAny(got, "\t\n\r\x1b") {
			t.Fatalf("%s emitted line/table control characters: %q", name, got)
		}
	}
	if strings.Contains(vmRuntimeStatusRuntimeDetail(identity, true), "[abcdef012345]") {
		t.Fatalf("invalid manifest digest emitted a fingerprint: %q", vmRuntimeStatusRuntimeDetail(identity, true))
	}

	var out bytes.Buffer
	if err := renderVMRuntimeStatus(&out, "table", []vmRuntimeStatusRow{row}, vmRuntimeStatusFleetView); err != nil {
		t.Fatal(err)
	}
	if !utf8.Valid(out.Bytes()) || strings.ContainsAny(out.String(), "\r\x1b") {
		t.Fatalf("fleet output was not safely encoded on one line per field: %q", out.String())
	}
}

func TestPrintVMRuntimeStatusUsesCommandIntentForHumanView(t *testing.T) {
	server := newTestServer(t)
	catalog := validVMRuntimeCatalog()
	artifact := vmRuntimeCommandArtifact(catalog.Architectures["amd64"].Runtimes[0], "official")
	seedRuntimeCommandVM(t, server, db.VMRuntimeLifecycleConfig{Configured: artifact})
	server.vmRuntimeCommandDeps = vmRuntimeStatusTestDeps(catalog, vmRuntimeUnitState{ActiveState: "inactive"}, func(int) bool { return false })

	var fleet bytes.Buffer
	if err := server.printVMRuntimeStatus(context.Background(), &fleet, "", "table"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fleet.String(), "VM") || strings.Contains(fleet.String(), "Guest base:") {
		t.Fatalf("unfiltered one-VM status did not use fleet view:\n%s", fleet.String())
	}

	var detail bytes.Buffer
	if err := server.printVMRuntimeStatus(context.Background(), &detail, "devbox", "table"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail.String(), "Guest base:") || strings.HasPrefix(detail.String(), "VM") {
		t.Fatalf("selected VM status did not use detail view:\n%s", detail.String())
	}
}

func TestVMRuntimeStatusHumanOutputPreservesDistinctServiceIDs(t *testing.T) {
	shared := "a" + strings.Repeat("b", 44)
	services := []string{
		shared + strings.Repeat("c", serviceid.MaxLength-len(shared)),
		shared + strings.Repeat("d", serviceid.MaxLength-len(shared)),
	}
	rows := make([]vmRuntimeStatusRow, 0, len(services))
	for _, service := range services {
		if err := serviceid.Validate(service); err != nil {
			t.Fatalf("test service ID %q is invalid: %v", service, err)
		}
		rows = append(rows, vmRuntimeStatusRow{
			Service: service, Configured: vmRuntimeStatusIdentity{ID: "runtime"},
			State: "current", RecommendedAction: "inspect this VM",
		})
	}

	var detail bytes.Buffer
	if err := renderVMRuntimeStatus(&detail, "table", rows, vmRuntimeStatusDetailView); err != nil {
		t.Fatal(err)
	}
	var fleet bytes.Buffer
	if err := renderVMRuntimeStatus(&fleet, "table", rows, vmRuntimeStatusFleetView); err != nil {
		t.Fatal(err)
	}
	fleetTable, _, found := strings.Cut(fleet.String(), "\nNeeds attention:")
	if !found {
		t.Fatalf("fleet output omitted attention section:\n%s", fleet.String())
	}
	for _, service := range services {
		if !strings.Contains(detail.String(), service+"  healthy") {
			t.Fatalf("detail output omitted full service ID %q:\n%s", service, detail.String())
		}
		if !strings.Contains(fleetTable, service) {
			t.Fatalf("fleet VM column omitted full service ID %q:\n%s", service, fleet.String())
		}
		if !strings.Contains(fleet.String(), "  "+service+": Inspect this VM.") {
			t.Fatalf("attention output omitted full service ID %q:\n%s", service, fleet.String())
		}
	}
	malformed := "service\nforged\tname\r\x1b[31m" + string([]byte{0xff})
	if got := vmRuntimeStatusServiceDisplay(malformed); !utf8.ValidString(got) || strings.ContainsAny(got, "\t\n\r\x1b") {
		t.Fatalf("malformed service display was not single-line valid UTF-8: %q", got)
	}
}

func TestVMRuntimeStatusHumanComponentAndStateLabels(t *testing.T) {
	guest := vmComponentStatusIdentity{
		ID:             "legacy-guest-ubuntu-26-04-ubuntu-26-04-amd64-v11-" + strings.Repeat("a", 64),
		ManifestSHA256: strings.Repeat("b", 64),
		Source:         "custom-legacy",
	}
	kernel := vmComponentStatusIdentity{
		ID:             "legacy-kernel-linux-7-1-2-yeet-" + strings.Repeat("c", 64),
		ManifestSHA256: strings.Repeat("d", 64),
		Source:         "custom-legacy",
	}
	if got, want := vmRuntimeStatusComponentSummary(guest), "ubuntu-26-04-amd64-v11 (custom legacy)"; got != want {
		t.Fatalf("guest label = %q, want %q", got, want)
	}
	if got, want := vmRuntimeStatusComponentSummary(kernel), "linux-7-1-2-yeet (custom legacy)"; got != want {
		t.Fatalf("kernel label = %q, want %q", got, want)
	}
	officialGuest := vmComponentStatusIdentity{ID: "guest-ubuntu-26.04-amd64-v4", Source: "official"}
	officialKernel := vmComponentStatusIdentity{ID: "kernel-linux-7.1.1-yeet-v2", Source: "official"}
	if got, want := vmRuntimeStatusComponentSummary(officialGuest), "ubuntu-26.04-amd64-v4 (official)"; got != want {
		t.Fatalf("official guest label = %q, want %q", got, want)
	}
	if got, want := vmRuntimeStatusComponentSummary(officialKernel), "linux-7.1.1-yeet-v2 (official)"; got != want {
		t.Fatalf("official kernel label = %q, want %q", got, want)
	}
	for raw, want := range map[string]string{
		"current":                     "healthy",
		"missing-or-untrusted-marker": "marker unverified",
		"running-config-diverged":     "config diverged",
		"failed-rolled-back":          "failed, rolled back",
	} {
		if got := vmRuntimeStatusHumanState(raw); got != want {
			t.Fatalf("state %q = %q, want %q", raw, got, want)
		}
	}
	row := vmRuntimeStatusRow{Policy: "manual", Channel: "stable"}
	if got, want := vmRuntimeStatusPolicySummary(row), "manual/stable"; got != want {
		t.Fatalf("policy summary = %q, want %q", got, want)
	}
	if got, want := vmRuntimeStatusPolicyDisplay(row), "manual / stable"; got != want {
		t.Fatalf("policy detail = %q, want %q", got, want)
	}
	if got, want := sentenceCaseVMRuntimeStatusAction("restart when ready"), "Restart when ready."; got != want {
		t.Fatalf("action = %q, want %q", got, want)
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
