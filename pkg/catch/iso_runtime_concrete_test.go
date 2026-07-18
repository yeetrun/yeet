// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
	"github.com/yeetrun/yeet/pkg/netns"
	"github.com/yeetrun/yeet/pkg/svc"
)

func TestISOConcreteReconcilePolicyAndTopologyLifecycle(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app": testISORuntimeAllocation("app", iso.StateReady),
	})
	withISORuntimeBackend(t, netns.BackendNFT)

	oldAcquire := acquireISOOperationLockForRuntime
	oldInstallDNS := installISODNSServiceForServer
	oldEnsurePolicy := ensureISOPolicyForRuntime
	oldVerifyPolicy := verifyISOPolicyForRuntime
	oldEnsureTopology := ensureISOTopologyForRuntime
	oldVerifyTopology := verifyISOTopologyForRuntime
	t.Cleanup(func() {
		acquireISOOperationLockForRuntime = oldAcquire
		installISODNSServiceForServer = oldInstallDNS
		ensureISOPolicyForRuntime = oldEnsurePolicy
		verifyISOPolicyForRuntime = oldVerifyPolicy
		ensureISOTopologyForRuntime = oldEnsureTopology
		verifyISOTopologyForRuntime = oldVerifyTopology
	})

	var events []string
	acquireISOOperationLockForRuntime = func(context.Context, string) (func(), error) {
		events = append(events, "lock")
		return func() { events = append(events, "unlock") }, nil
	}
	installISODNSServiceForServer = func(root string) error {
		if root != server.cfg.RootDir {
			t.Fatalf("install DNS root = %q", root)
		}
		events = append(events, "dns")
		return nil
	}
	ensureISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error {
		events = append(events, "ensure-policy")
		return nil
	}
	verifyISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error {
		events = append(events, "verify-policy")
		return nil
	}
	ensureISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error {
		events = append(events, "ensure-topology")
		return nil
	}
	verifyISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error {
		events = append(events, "verify-topology")
		return nil
	}

	steps := &isoConcreteReconcileSteps{server: server}
	if err := steps.ValidatePool(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := steps.InstallDNS(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := steps.EnsurePolicy(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	if err := steps.VerifyPolicy(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	if err := steps.EnsureTopology(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	if err := steps.VerifyTopology(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	want := []string{"dns", "lock", "ensure-policy", "verify-policy", "ensure-topology", "verify-topology", "unlock"}
	if !slices.Equal(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}

	events = nil
	steps.unlock = func() { events = append(events, "release-old") }
	if err := steps.VerifyGlobalPolicy(context.Background()); err != nil {
		t.Fatal(err)
	}
	want = []string{"release-old", "lock", "verify-policy", "unlock"}
	if !slices.Equal(events, want) {
		t.Fatalf("global policy events = %#v, want %#v", events, want)
	}
}

func TestISOConcreteReconcileVMLifecycle(t *testing.T) {
	server, _ := newISOReconcileVMTestServer(t, iso.StateReady)
	oldStatus := serverVMStatusFunc
	oldVMSystemctl := runISOReconcileVMSystemctl
	t.Cleanup(func() {
		serverVMStatusFunc = oldStatus
		runISOReconcileVMSystemctl = oldVMSystemctl
	})

	status := svc.StatusStopped
	statusErr := error(nil)
	serverVMStatusFunc = func(name string) (svc.Status, error) {
		if name != "devbox" {
			t.Fatalf("VM status name = %q", name)
		}
		return status, statusErr
	}
	var calls []string
	runISOReconcileVMSystemctl = func(_ context.Context, action, service string) error {
		calls = append(calls, action+":"+service)
		return nil
	}

	steps := &isoConcreteReconcileSteps{server: server, unlock: func() { calls = append(calls, "unlock") }}
	state, err := steps.InspectRuntime(context.Background(), "devbox")
	if err != nil || state != isoReconcileRuntimeAbsent {
		t.Fatalf("InspectRuntime stopped = %q, %v", state, err)
	}
	if err := steps.VerifyStopped(context.Background(), "devbox"); err != nil {
		t.Fatal(err)
	}
	status = svc.StatusRunning
	state, err = steps.InspectRuntime(context.Background(), "devbox")
	if err != nil || state != isoReconcileRuntimeRunning {
		t.Fatalf("InspectRuntime running = %q, %v", state, err)
	}
	if err := steps.VerifyStopped(context.Background(), "devbox"); err == nil {
		t.Fatal("VerifyStopped accepted running VM")
	}
	if err := steps.StopUntrusted(context.Background(), "devbox"); err != nil {
		t.Fatal(err)
	}
	steps.unlock = func() { calls = append(calls, "unlock-restart") }
	if err := steps.RestartTrusted(context.Background(), "devbox"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"unlock", "stop:devbox", "unlock-restart", "start:devbox"}) {
		t.Fatalf("VM lifecycle calls = %#v", calls)
	}

	statusErr = errors.New("status unavailable")
	if _, err := steps.InspectRuntime(context.Background(), "devbox"); !errors.Is(err, statusErr) {
		t.Fatalf("InspectRuntime error = %v", err)
	}
}

func TestISOTailscaleAndAuxiliaryUnitVerification(t *testing.T) {
	oldSystemctl := runISOSystemctlForRuntime
	t.Cleanup(func() { runISOSystemctlForRuntime = oldSystemctl })

	service := &db.Service{
		Name:       "app",
		Generation: 3,
		Artifacts: db.ArtifactStore{
			db.ArtifactTSService:    {Refs: map[db.ArtifactRef]string{db.Gen(3): "/tmp/ts.service"}},
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(3): "/tmp/ns.service"}},
		},
	}
	if got := isoAuxiliaryUnits(service); !slices.Equal(got, []string{"yeet-app-ts.service", "yeet-app-ns.service"}) {
		t.Fatalf("isoAuxiliaryUnits = %#v", got)
	}

	var calls [][]string
	runISOSystemctlForRuntime = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "show" && strings.Contains(args[len(args)-1], "ts") {
			return []byte("failed\n"), nil
		}
		return []byte("inactive\n"), nil
	}
	if err := stopAndVerifyISOAuxiliaryUnits(context.Background(), service); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || !reflect.DeepEqual(calls[0], []string{"stop", "yeet-app-ts.service", "yeet-app-ns.service"}) {
		t.Fatalf("systemctl calls = %#v", calls)
	}

	runISOSystemctlForRuntime = func(context.Context, ...string) ([]byte, error) { return []byte("active\n"), nil }
	if err := verifyISOTailscaleUnit(context.Background(), "yeet-app-ts.service"); err != nil {
		t.Fatal(err)
	}
	runISOSystemctlForRuntime = func(context.Context, ...string) ([]byte, error) { return []byte("inactive\n"), nil }
	if err := verifyISOTailscaleUnit(context.Background(), "yeet-app-ts.service"); err == nil {
		t.Fatal("verifyISOTailscaleUnit accepted inactive unit")
	}
	wantErr := errors.New("systemctl unavailable")
	runISOSystemctlForRuntime = func(context.Context, ...string) ([]byte, error) { return []byte("detail"), wantErr }
	if err := verifyISOTailscaleUnit(context.Background(), "yeet-app-ts.service"); !errors.Is(err, wantErr) {
		t.Fatalf("verifyISOTailscaleUnit error = %v", err)
	}

	socketDir, err := os.MkdirTemp("/tmp", "yeet-iso-socket-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "ts.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := verifyISOTailscaleSocket(socketPath); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyISOTailscaleSocket(regular); err == nil {
		t.Fatal("verifyISOTailscaleSocket accepted regular file")
	}
	if err := verifyISOTailscaleSocket(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("verifyISOTailscaleSocket accepted missing path")
	}
}

func TestISOAuxiliaryUnitFailuresAreFailClosed(t *testing.T) {
	oldSystemctl := runISOSystemctlForRuntime
	t.Cleanup(func() { runISOSystemctlForRuntime = oldSystemctl })
	service := &db.Service{Name: "app", Generation: 1, Artifacts: db.ArtifactStore{
		db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/ns.service"}},
	}}
	wantErr := errors.New("systemctl failed")
	runISOSystemctlForRuntime = func(context.Context, ...string) ([]byte, error) { return []byte("detail"), wantErr }
	if err := stopAndVerifyISOAuxiliaryUnits(context.Background(), service); !errors.Is(err, wantErr) {
		t.Fatalf("stopAndVerifyISOAuxiliaryUnits error = %v", err)
	}
	runISOSystemctlForRuntime = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "stop" {
			return nil, nil
		}
		return []byte("active\n"), nil
	}
	if err := stopAndVerifyISOAuxiliaryUnits(context.Background(), service); err == nil || !strings.Contains(err.Error(), "remains active") {
		t.Fatalf("stopAndVerifyISOAuxiliaryUnits error = %v", err)
	}
	if err := stopAndVerifyISOAuxiliaryUnits(context.Background(), &db.Service{Name: "plain"}); err != nil {
		t.Fatalf("no auxiliary units: %v", err)
	}
}

func TestISOConcreteVMRemovalStepsCleanEveryBoundary(t *testing.T) {
	server, allocation := newISOReconcileVMTestServer(t, iso.StateReady)
	rootFS := filepath.Join(server.cfg.RootDir, "services", "devbox", "rootfs.ext4")
	_, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.VM.Image.RootFS = rootFS
		service.VM.Sockets.APISocketPath = filepath.Join(server.cfg.RootDir, "api.sock")
		service.VM.Sockets.VsockSocketPath = filepath.Join(server.cfg.RootDir, "vsock.sock")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	withISORuntimeBackend(t, netns.BackendNFT)

	oldVMSystemctl := runISOReconcileVMSystemctl
	oldNetworkRunner := vmRemovalNetworkRunner
	oldJailCleanup := vmRemovalJailCleanup
	oldVerifyAbsent := verifyISOTopologyAbsentForRuntime
	oldEnsurePolicy := ensureISOPolicyForRuntime
	oldVerifyPolicy := verifyISOPolicyForRuntime
	t.Cleanup(func() {
		runISOReconcileVMSystemctl = oldVMSystemctl
		vmRemovalNetworkRunner = oldNetworkRunner
		vmRemovalJailCleanup = oldJailCleanup
		verifyISOTopologyAbsentForRuntime = oldVerifyAbsent
		ensureISOPolicyForRuntime = oldEnsurePolicy
		verifyISOPolicyForRuntime = oldVerifyPolicy
	})
	var events []string
	runISOReconcileVMSystemctl = func(_ context.Context, action, service string) error {
		events = append(events, action+":"+service)
		return nil
	}
	vmRemovalNetworkRunner = func(command []string) error {
		events = append(events, "network:"+strings.Join(command, " "))
		return nil
	}
	vmRemovalJailCleanup = func(plan vmJailPlan) error {
		events = append(events, "jail:"+plan.ID)
		return nil
	}
	verifyISOTopologyAbsentForRuntime = func(context.Context, netns.ISOTopologySpec) error {
		events = append(events, "topology-absent")
		return nil
	}
	ensureISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error {
		events = append(events, "render-policy")
		return nil
	}
	verifyISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error {
		events = append(events, "verify-policy")
		return nil
	}

	stepSet, err := server.newConcreteISORemoveSteps("devbox", RemoveOptions{}, &RemoveReport{}, "")
	if err != nil {
		t.Fatal(err)
	}
	steps := stepSet.(*isoConcreteRemoveSteps)
	ctx := context.Background()
	for name, run := range map[string]func() error{
		"stop workload":           func() error { return steps.StopWorkload(ctx, "devbox") },
		"stop tailscale":          func() error { return steps.StopTailscale(ctx, "devbox") },
		"remove docker endpoints": func() error { return steps.RemoveDockerEndpoints(ctx, "devbox") },
		"clean topology":          func() error { return steps.CleanTopology(ctx, "devbox") },
		"verify topology":         func() error { return steps.VerifyTopologyAbsent(ctx, "devbox") },
		"verify docker":           func() error { return steps.VerifyDockerAbsent(ctx, "devbox") },
		"verify dnet":             func() error { return steps.VerifyDNetAbsent(ctx, "devbox") },
		"render policy":           func() error { return steps.RenderGlobalPolicy(ctx, "devbox") },
		"verify policy":           func() error { return steps.VerifyGlobalPolicy(ctx, "devbox") },
		"before delete":           func() error { return steps.BeforeDelete(ctx, "devbox") },
	} {
		if err := run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if !slices.Contains(events, "stop:devbox") || !slices.Contains(events, "topology-absent") || !slices.Contains(events, "jail:yeet-devbox") {
		t.Fatalf("removal events = %#v", events)
	}
	if allocation.PeerIP.IsValid() == false {
		t.Fatal("test allocation lost peer address")
	}
}

func TestISODNetAbsenceChecksActiveAndRetiredAddresses(t *testing.T) {
	server := newISORuntimeTestServer(t, nil)
	active := netip.MustParseAddr("172.30.128.2")
	retired := netip.MustParseAddr("172.30.128.3")
	allocation := db.ISOAllocation{
		Components:        map[string]db.ISOComponent{"api": {Address: active}},
		RetiredComponents: map[string]db.ISOComponent{"old": {Address: retired}},
	}
	_, err := server.cfg.DB.MutateData(func(data *db.Data) error {
		data.DockerNetworks = map[string]*db.DockerNetwork{
			"network": {Endpoints: map[string]*db.DockerEndpoint{
				"nil": nil,
				"old": {IPv4: netip.PrefixFrom(retired, 32)},
			}},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = verifyISOAllocationDNetAbsent(server, allocation)
	if err == nil || !strings.Contains(err.Error(), retired.String()) {
		t.Fatalf("verifyISOAllocationDNetAbsent error = %v", err)
	}
	if endpoint, addr, found := dnetAddressOwner(nil, map[netip.Addr]bool{active: true}); found || endpoint != "" || addr.IsValid() {
		t.Fatalf("nil dnet owner = %q %v %v", endpoint, addr, found)
	}
	if err := verifyDNetAddressesAbsent(map[string]*db.DockerNetwork{"nil": nil}, map[netip.Addr]bool{active: true}, "test"); err != nil {
		t.Fatal(err)
	}
}

func TestISOComposeInspectionArtifactsAndComponentAddresses(t *testing.T) {
	record := &db.Service{Name: "app", Generation: 4, Artifacts: db.ArtifactStore{
		db.ArtifactDockerComposeFile:    {Refs: map[db.ArtifactRef]string{db.Gen(4): "/srv/app/compose.yml"}},
		db.ArtifactDockerComposeNetwork: {Refs: map[db.ArtifactRef]string{db.Gen(4): "/srv/app/compose.network.yml"}},
	}}
	base, overlay, err := isoComposeInspectionArtifacts(record)
	if err != nil || base != "/srv/app/compose.yml" || overlay != "/srv/app/compose.network.yml" {
		t.Fatalf("isoComposeInspectionArtifacts = %q %q %v", base, overlay, err)
	}
	delete(record.Artifacts, db.ArtifactDockerComposeNetwork)
	if _, _, err := isoComposeInspectionArtifacts(record); err == nil {
		t.Fatal("missing overlay was accepted")
	}
	delete(record.Artifacts, db.ArtifactDockerComposeFile)
	if _, _, err := isoComposeInspectionArtifacts(record); err == nil {
		t.Fatal("missing base was accepted")
	}
	components := map[string]db.ISOComponent{"api": {Address: netip.MustParseAddr("172.30.128.2")}}
	if got := isoComponentAddresses(components); !reflect.DeepEqual(got, map[string]netip.Addr{"api": netip.MustParseAddr("172.30.128.2")}) {
		t.Fatalf("isoComponentAddresses = %#v", got)
	}
}

func TestISOConcreteComposeRuntimeInspectionAndStoppedProof(t *testing.T) {
	allocation := testISORuntimeAllocation("app", iso.StateReady)
	allocation.Components = map[string]db.ISOComponent{"api": {Address: allocation.Project.Addr().Next().Next()}}
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"app": allocation})
	base := filepath.Join(t.TempDir(), "compose.yml")
	overlay := filepath.Join(t.TempDir(), "compose.network.yml")
	for _, path := range []string{base, overlay} {
		if err := os.WriteFile(path, []byte("services: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := server.cfg.DB.MutateService("app", func(_ *db.Data, service *db.Service) error {
		service.ServiceType = db.ServiceTypeDockerCompose
		service.Generation = 2
		service.Artifacts = db.ArtifactStore{
			db.ArtifactDockerComposeFile:    {Refs: map[db.ArtifactRef]string{db.Gen(2): base}},
			db.ArtifactDockerComposeNetwork: {Refs: map[db.ArtifactRef]string{db.Gen(2): overlay}},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	dockerDir := t.TempDir()
	dockerPath := filepath.Join(dockerDir, "docker")
	if err := os.WriteFile(dockerPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	oldCompose := dockerComposeServiceForISO
	oldInspect := inspectISOProjectForRuntime
	t.Cleanup(func() {
		dockerComposeServiceForISO = oldCompose
		inspectISOProjectForRuntime = oldInspect
	})
	statusOutput := ""
	compose, err := server.dockerComposeService("app")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(compose.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	compose.NewCmdContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		output := ""
		if slices.Contains(args, "ps") {
			output = statusOutput
		}
		cmd := exec.CommandContext(ctx, "sh", "-c", "printf '%s' \"$ISO_TEST_OUTPUT\"")
		cmd.Env = append(os.Environ(), "ISO_TEST_OUTPUT="+output)
		return cmd
	}
	dockerComposeServiceForISO = func(*Server, string) (*svc.DockerComposeService, error) { return compose, nil }
	inspectCalls := 0
	inspectISOProjectForRuntime = func(_ context.Context, opts svc.ISOInspectOptions) (svc.ISOInspection, error) {
		inspectCalls++
		if opts.ProjectName != "catch-app" || len(opts.ComposeFiles) != 2 || opts.Components["api"] != allocation.Project.Addr().Next().Next() {
			t.Fatalf("inspection options = %#v", opts)
		}
		return svc.ISOInspection{}, nil
	}

	steps := &isoConcreteReconcileSteps{server: server}
	state, err := steps.InspectRuntime(context.Background(), "app")
	if err != nil || state != isoReconcileRuntimeAbsent || inspectCalls != 0 {
		t.Fatalf("absent InspectRuntime = %q, %v, calls=%d", state, err, inspectCalls)
	}
	if err := steps.VerifyStopped(context.Background(), "app"); err != nil {
		t.Fatalf("VerifyStopped absent Compose: %v", err)
	}

	statusOutput = "api,running\n"
	state, err = steps.InspectRuntime(context.Background(), "app")
	if err != nil || state != isoReconcileRuntimeRunning || inspectCalls != 1 {
		t.Fatalf("running InspectRuntime = %q, %v, calls=%d", state, err, inspectCalls)
	}
	if err := steps.VerifyStopped(context.Background(), "app"); err == nil {
		t.Fatal("VerifyStopped accepted running Compose workload")
	}

	inspectISOProjectForRuntime = func(context.Context, svc.ISOInspectOptions) (svc.ISOInspection, error) {
		return svc.ISOInspection{Findings: []string{"unexpected host network"}}, nil
	}
	if _, err := steps.InspectRuntime(context.Background(), "app"); err == nil || !strings.Contains(err.Error(), "unexpected host network") {
		t.Fatalf("drifted InspectRuntime error = %v", err)
	}
	wantErr := errors.New("inspect failed")
	inspectISOProjectForRuntime = func(context.Context, svc.ISOInspectOptions) (svc.ISOInspection, error) {
		return svc.ISOInspection{}, wantErr
	}
	if _, err := steps.InspectRuntime(context.Background(), "app"); !errors.Is(err, wantErr) {
		t.Fatalf("inspection error = %v", err)
	}
}

func TestISOConcreteVerifyTailscaleUsesPersistedSocket(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app": testISORuntimeAllocation("app", iso.StateReady),
	})
	serviceRoot, err := os.MkdirTemp("/tmp", "yeet-iso-service-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(serviceRoot) })
	_, _, err = server.cfg.DB.MutateService("app", func(_ *db.Data, service *db.Service) error {
		service.ServiceRoot = serviceRoot
		service.TSNet = &db.TailscaleNetwork{Interface: isoTailscaleInterface}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	runDir := serviceRunDirForRoot(serviceRoot)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(runDir, "tailscaled.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	oldSystemctl := runISOSystemctlForRuntime
	runISOSystemctlForRuntime = func(context.Context, ...string) ([]byte, error) { return []byte("active\n"), nil }
	t.Cleanup(func() { runISOSystemctlForRuntime = oldSystemctl })

	steps := &isoConcreteReconcileSteps{server: server}
	if err := steps.VerifyTailscale(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	_, _, err = server.cfg.DB.MutateService("app", func(_ *db.Data, service *db.Service) error {
		service.TSNet = nil
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := steps.VerifyTailscale(context.Background(), "app"); err == nil {
		t.Fatal("VerifyTailscale accepted missing persisted state")
	}
}

func TestISORuntimeComponentValidationRejectsCollisionsAndReservedAddresses(t *testing.T) {
	project := netip.MustParsePrefix("172.30.128.0/27")
	valid := map[string]db.ISOComponent{
		"api": {Address: netip.MustParseAddr("172.30.128.2")},
		"web": {Address: netip.MustParseAddr("172.30.128.30")},
	}
	if err := validateISORuntimeComponents("app", project, valid); err != nil {
		t.Fatal(err)
	}
	for _, address := range []netip.Addr{
		netip.MustParseAddr("172.30.128.1"),
		netip.MustParseAddr("172.30.128.31"),
		netip.MustParseAddr("172.30.129.2"),
		netip.MustParseAddr("2001:db8::2"),
	} {
		if isUsableISOComponentAddress(project, address) {
			t.Fatalf("reserved or foreign address %s was accepted", address)
		}
	}
	if err := validateISORuntimeComponents("app", project,
		map[string]db.ISOComponent{"api": {Address: netip.MustParseAddr("172.30.128.2")}},
		map[string]db.ISOComponent{"api": {Address: netip.MustParseAddr("172.30.128.3")}},
	); err == nil || !strings.Contains(err.Error(), "duplicate or empty component") {
		t.Fatalf("duplicate name error = %v", err)
	}
	if err := validateISORuntimeComponents("app", project,
		map[string]db.ISOComponent{"api": {Address: netip.MustParseAddr("172.30.128.2")}},
		map[string]db.ISOComponent{"old": {Address: netip.MustParseAddr("172.30.128.2")}},
	); err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("duplicate address error = %v", err)
	}
	if err := validateISORuntimeComponents("app", project,
		map[string]db.ISOComponent{"bad": {Address: netip.MustParseAddr("172.30.128.1")}},
	); err == nil || !strings.Contains(err.Error(), "outside usable") {
		t.Fatalf("reserved address error = %v", err)
	}
}

func TestISONetworkPublicWrappersAndFailClosedStartup(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app": testISORuntimeAllocation("app", iso.StateReserved),
	})
	withISORuntimeBackend(t, netns.BackendNFT)
	oldEnsurePolicy := ensureISOPolicyForRuntime
	oldEnsureTopology := ensureISOTopologyForRuntime
	oldInstallDNS := installISODNSServiceForServer
	oldVerifyPolicy := verifyISOPolicyForRuntime
	oldVMSystemctl := runISOReconcileVMSystemctl
	t.Cleanup(func() {
		ensureISOPolicyForRuntime = oldEnsurePolicy
		ensureISOTopologyForRuntime = oldEnsureTopology
		installISODNSServiceForServer = oldInstallDNS
		verifyISOPolicyForRuntime = oldVerifyPolicy
		runISOReconcileVMSystemctl = oldVMSystemctl
	})
	ensureISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error { return nil }
	ensureISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error { return nil }
	verifyISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error { return nil }
	installISODNSServiceForServer = func(string) error { return nil }
	if err := server.EnsureISONetworkBoundary(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if state := dv.Services().Get("app").ISO().State(); state != string(iso.StateReserved) {
		t.Fatalf("boundary wrapper changed state to %q", state)
	}

	empty := newISORuntimeTestServer(t, nil)
	if err := empty.reconcileISONetworks(context.Background()); err != nil {
		t.Fatalf("empty reconcileISONetworks: %v", err)
	}

	vmServer, _ := newISOReconcileVMTestServer(t, iso.StateReady)
	runISOReconcileVMSystemctl = func(context.Context, string, string) error { return nil }
	wantErr := errors.New("startup prerequisite failed")
	if err := vmServer.failClosedISONetworks(context.Background(), wantErr); !errors.Is(err, wantErr) {
		t.Fatalf("failClosedISONetworks error = %v", err)
	}
	vmView, err := vmServer.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if state := vmView.Services().Get("devbox").ISO().State(); state != string(iso.StateQuarantined) {
		t.Fatalf("fail-closed state = %q", state)
	}
}
