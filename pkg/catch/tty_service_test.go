// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
	"github.com/yeetrun/yeet/pkg/svc"
)

type failingStatusWriter struct {
	err error
}

func (w failingStatusWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestRenderServiceStatusesTableOutput(t *testing.T) {
	statuses := []ServiceStatusData{
		{
			ServiceName: "timer",
			ServiceType: ServiceDataTypeCron,
			ComponentStatus: []ComponentStatusData{
				{Name: "timer", Status: ComponentStatusStopped},
			},
		},
		{
			ServiceName: "web",
			ServiceType: ServiceDataTypeDocker,
			ComponentStatus: []ComponentStatusData{
				{Name: "api", Status: ComponentStatusRunning},
			},
		},
	}

	var out bytes.Buffer
	if err := renderServiceStatuses(&out, "", statuses); err != nil {
		t.Fatalf("renderServiceStatuses: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("rendered line count = %d, want 3\n%s", len(lines), out.String())
	}
	wantFields := [][]string{
		{"SERVICE", "TYPE", "CONTAINER", "STATUS"},
		{"timer", "cron", "-", "stopped"},
		{"web", "docker", "api", "running"},
	}
	for i, want := range wantFields {
		if got := strings.Fields(lines[i]); !reflect.DeepEqual(got, want) {
			t.Fatalf("line %d fields = %#v, want %#v\n%s", i, got, want, out.String())
		}
	}
}

func TestRenderServiceStatusesTableReturnsWriterError(t *testing.T) {
	writeErr := errors.New("write failed")
	err := renderServiceStatuses(failingStatusWriter{err: writeErr}, "", []ServiceStatusData{
		{
			ServiceName: "web",
			ServiceType: ServiceDataTypeDocker,
			ComponentStatus: []ComponentStatusData{
				{Name: "api", Status: ComponentStatusRunning},
			},
		},
	})
	if !errors.Is(err, writeErr) {
		t.Fatalf("renderServiceStatuses error = %v, want %v", err, writeErr)
	}
}

func TestWriteServiceStatusRowReturnsWriterError(t *testing.T) {
	writeErr := errors.New("row write failed")
	err := writeServiceStatusRow(
		failingStatusWriter{err: writeErr},
		ServiceStatusData{ServiceName: "web", ServiceType: ServiceDataTypeDocker},
		ComponentStatusData{Name: "api", Status: ComponentStatusRunning},
	)
	if !errors.Is(err, writeErr) {
		t.Fatalf("writeServiceStatusRow error = %v, want %v", err, writeErr)
	}
}

func TestSystemdLogArgs(t *testing.T) {
	got := systemdLogArgs("web", &svc.LogOptions{Follow: true, Lines: 25})
	want := []string{"--no-pager", "--output=cat", "--follow", "--lines=25", "--unit=web"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("systemdLogArgs = %#v, want %#v", got, want)
	}
}

func TestSystemdLogArgsWithNilOptions(t *testing.T) {
	got := systemdLogArgs("web", nil)
	want := []string{"--no-pager", "--output=cat", "--unit=web"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("systemdLogArgs = %#v, want %#v", got, want)
	}
}

func TestSelectPreviousGeneration(t *testing.T) {
	service := &db.Service{Generation: 3, LatestGeneration: 4}
	if err := selectPreviousGeneration(service); err != nil {
		t.Fatalf("selectPreviousGeneration: %v", err)
	}
	if service.Generation != 2 {
		t.Fatalf("Generation = %d, want 2", service.Generation)
	}
}

func TestSelectPreviousGenerationRejectsTooOldGeneration(t *testing.T) {
	service := &db.Service{Generation: 2, LatestGeneration: maxGenerations + 3}
	err := selectPreviousGeneration(service)
	if err == nil || !strings.Contains(err.Error(), "earliest rollback") {
		t.Fatalf("selectPreviousGeneration error = %v, want earliest rollback error", err)
	}
}

func TestRollbackCmdFuncSelectsPreviousGenerationAndInstallsWithHook(t *testing.T) {
	server := newTestServer(t)
	artifacts, staticVerifications := canonicalLegacyRollbackArtifacts(t, server, "svc-rollback", 1, 2, 3)
	seedService(t, server, "svc-rollback", db.ServiceTypeSystemd, artifacts)
	if _, _, err := server.cfg.DB.MutateService("svc-rollback", func(_ *db.Data, s *db.Service) error {
		s.Generation = 3
		s.LatestGeneration = 3
		return nil
	}); err != nil {
		t.Fatalf("seed generation: %v", err)
	}

	var installedGen int
	execer := &ttyExecer{
		ctx:      context.Background(),
		s:        server,
		sn:       "svc-rollback",
		rw:       &bytes.Buffer{},
		progress: catchrpc.ProgressQuiet,
		serviceInstallGenFunc: func(cfg InstallerCfg, gen int) error {
			if cfg.ServiceName != "svc-rollback" {
				t.Fatalf("install service = %q, want svc-rollback", cfg.ServiceName)
			}
			installedGen = gen
			return nil
		},
	}

	if err := execer.rollbackCmdFunc("svc-rollback"); err != nil {
		t.Fatalf("rollbackCmdFunc returned error: %v", err)
	}
	if installedGen != 2 {
		t.Fatalf("installed generation = %d, want 2", installedGen)
	}
	if *staticVerifications != 1 {
		t.Fatalf("sandbox static verifications = %d, want 1", *staticVerifications)
	}
	sv, err := server.serviceView("svc-rollback")
	if err != nil {
		t.Fatalf("serviceView: %v", err)
	}
	if got := sv.AsStruct().Generation; got != 2 {
		t.Fatalf("stored generation = %d, want 2", got)
	}
}

func TestRollbackRejectsScheduledToOrdinaryGenerationInsideServiceLock(t *testing.T) {
	server := newTestServer(t)
	addTestServices(t, server, db.Service{
		Name:             "scheduled-rollback",
		ServiceType:      db.ServiceTypeSystemd,
		Generation:       2,
		LatestGeneration: 2,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{
				db.Gen(1): "/tmp/ordinary.service",
				db.Gen(2): "/tmp/scheduled.service",
			}},
			db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{
				db.Gen(2): "/tmp/scheduled.timer",
				"latest":  "/tmp/scheduled.timer",
			}},
		},
	})
	installCalled := false
	execer := &ttyExecer{
		ctx: context.Background(), s: server, sn: "scheduled-rollback", rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
		serviceInstallGenFunc: func(InstallerCfg, int) error {
			installCalled = true
			return nil
		},
	}

	release := server.serviceOperationLocks.Lock("scheduled-rollback")
	done := make(chan error, 1)
	go func() { done <- execer.rollbackCmdFunc("scheduled-rollback") }()
	select {
	case err := <-done:
		release()
		t.Fatalf("rollback bypassed service mutation lock: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	release()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "scheduled service can only be updated with a native binary or script") {
			t.Fatalf("rollback error = %v, want scheduled-to-ordinary rejection", err)
		}
	case <-time.After(time.Second):
		t.Fatal("rollback did not resume after service mutation lock release")
	}
	if installCalled {
		t.Fatal("rollback reached generation install/start seam")
	}
	service, err := server.serviceView("scheduled-rollback")
	if err != nil {
		t.Fatal(err)
	}
	if service.Generation() != 2 {
		t.Fatalf("generation = %d, want unchanged generation 2", service.Generation())
	}
}

func TestRollbackDispatchLocksExplicitTargetService(t *testing.T) {
	server := newTestServer(t)
	addTestServices(t, server, db.Service{
		Name:             "scheduled-target",
		ServiceType:      db.ServiceTypeSystemd,
		Generation:       2,
		LatestGeneration: 2,
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{
				db.Gen(1): "/tmp/ordinary.service",
				db.Gen(2): "/tmp/scheduled.service",
			}},
			db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{
				db.Gen(2): "/tmp/scheduled.timer",
				"latest":  "/tmp/scheduled.timer",
			}},
		},
	})
	installCalled := false
	execer := &ttyExecer{
		ctx: context.Background(), s: server, sn: SystemService, rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
		serviceInstallGenFunc: func(InstallerCfg, int) error {
			installCalled = true
			return nil
		},
	}

	release := server.serviceOperationLocks.Lock("scheduled-target")
	done := make(chan error, 1)
	go func() { done <- execer.dispatch([]string{"service", "rollback", "scheduled-target"}) }()
	select {
	case err := <-done:
		release()
		t.Fatalf("rollback bypassed explicit target mutation lock: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	release()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), scheduledNativeOnlyMessage) {
			t.Fatalf("rollback error = %v, want scheduled-to-ordinary rejection", err)
		}
	case <-time.After(time.Second):
		t.Fatal("rollback did not resume after explicit target lock release")
	}
	if installCalled {
		t.Fatal("rollback reached generation install/start seam")
	}
	if execer.sn != SystemService {
		t.Fatalf("transport service = %q, want restored %q", execer.sn, SystemService)
	}
}

func TestRollbackAllowsScheduledToScheduledGeneration(t *testing.T) {
	server := newTestServer(t)
	artifacts, staticVerifications := canonicalLegacyRollbackArtifacts(t, server, "scheduled-rollback", 1, 2)
	artifacts[db.ArtifactSystemdTimerFile] = &db.Artifact{Refs: map[db.ArtifactRef]string{
		db.Gen(1): "/tmp/previous.timer",
		db.Gen(2): "/tmp/current.timer",
	}}
	addTestServices(t, server, db.Service{
		Name:             "scheduled-rollback",
		ServiceType:      db.ServiceTypeSystemd,
		Generation:       2,
		LatestGeneration: 2,
		Artifacts:        artifacts,
	})
	installed := 0
	execer := &ttyExecer{
		ctx: context.Background(), s: server, sn: "scheduled-rollback", rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
		serviceInstallGenFunc: func(_ InstallerCfg, gen int) error {
			installed = gen
			return nil
		},
	}
	if err := execer.rollbackCmdFunc("scheduled-rollback"); err != nil {
		t.Fatal(err)
	}
	if installed != 1 {
		t.Fatalf("installed generation = %d, want 1", installed)
	}
	if *staticVerifications != 1 {
		t.Fatalf("sandbox static verifications = %d, want 1", *staticVerifications)
	}
}

func TestServiceCommandDispatchesRollbackAndGenerations(t *testing.T) {
	server := newTestServer(t)
	artifacts, staticVerifications := canonicalLegacyRollbackArtifacts(t, server, "svc-rollback", 1, 2, 3)
	seedService(t, server, "svc-rollback", db.ServiceTypeSystemd, artifacts)
	if _, _, err := server.cfg.DB.MutateService("svc-rollback", func(_ *db.Data, s *db.Service) error {
		s.Generation = 3
		s.LatestGeneration = 3
		return nil
	}); err != nil {
		t.Fatalf("seed generation: %v", err)
	}

	var installedGen int
	execer := &ttyExecer{
		ctx:      context.Background(),
		s:        server,
		sn:       "svc-rollback",
		rw:       &bytes.Buffer{},
		progress: catchrpc.ProgressQuiet,
		serviceInstallGenFunc: func(cfg InstallerCfg, gen int) error {
			if cfg.ServiceName != "svc-rollback" {
				t.Fatalf("install service = %q, want svc-rollback", cfg.ServiceName)
			}
			installedGen = gen
			return nil
		},
	}

	if err := execer.serviceCmdFunc([]string{"rollback"}); err != nil {
		t.Fatalf("service rollback returned error: %v", err)
	}
	if installedGen != 2 {
		t.Fatalf("installed generation = %d, want 2", installedGen)
	}
	if *staticVerifications != 1 {
		t.Fatalf("sandbox static verifications = %d, want 1", *staticVerifications)
	}
	sv, err := server.serviceView("svc-rollback")
	if err != nil {
		t.Fatalf("serviceView: %v", err)
	}
	if got := sv.AsStruct().Generation; got != 2 {
		t.Fatalf("stored generation = %d, want 2", got)
	}

	var out bytes.Buffer
	execer.rw = &out
	if err := execer.serviceCmdFunc([]string{"generations", "--format=json"}); err != nil {
		t.Fatalf("service generations returned error: %v", err)
	}
	var got struct {
		Service           string `json:"service"`
		Type              string `json:"type"`
		CurrentGeneration int    `json:"currentGeneration"`
		LatestGeneration  int    `json:"latestGeneration"`
		RollbackSupported bool   `json:"rollbackSupported"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode generations JSON: %v\n%s", err, out.String())
	}
	if got.Service != "svc-rollback" || got.Type != "systemd" || got.CurrentGeneration != 2 || got.LatestGeneration != 3 || !got.RollbackSupported {
		t.Fatalf("service generations = %#v, want svc-rollback systemd current 2 latest 3 rollback supported", got)
	}

	out.Reset()
	if err := execer.serviceCmdFunc([]string{"generations", "--format=json-pretty"}); err != nil {
		t.Fatalf("service generations json-pretty returned error: %v", err)
	}
	if !strings.Contains(out.String(), "\n  \"service\": \"svc-rollback\"") {
		t.Fatalf("json-pretty output = %q, want indented service field", out.String())
	}
}

func TestServiceRollbackStartAndRestartPreflightSandboxBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		generation int
		run        func(*ttyExecer) error
		wantAction string
	}{
		{name: "rollback", generation: 2, run: func(e *ttyExecer) error { return e.rollbackCmdFunc("api") }, wantAction: "install"},
		{name: "start", generation: 3, run: (*ttyExecer).startCmdFunc, wantAction: "start"},
		{name: "restart", generation: 3, run: (*ttyExecer).restartCmdFunc, wantAction: "restart"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)
			seedService(t, server, "api", db.ServiceTypeSystemd, db.ArtifactStore{
				db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(2): "/tmp/api-2.service", db.Gen(3): "/tmp/api-3.service"}},
			})
			if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
				service.Generation = 3
				service.LatestGeneration = 3
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var events []string
			runner := &recordingServiceRunner{onCall: func(action string) { events = append(events, action) }}
			execer := &ttyExecer{
				ctx: context.Background(), s: server, sn: "api", rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
				serviceRunnerFn: func() (ServiceRunner, error) { return runner, nil },
				serviceInstallGenFunc: func(_ InstallerCfg, generation int) error {
					events = append(events, "install")
					if generation != 2 {
						t.Fatalf("installed generation = %d, want 2", generation)
					}
					return nil
				},
				preflightSandboxGenerationActivationFunc: func(_ context.Context, service *db.Service, generation int) error {
					events = append(events, "preflight")
					if service.Generation != 3 || generation != tc.generation {
						t.Fatalf("preflight service/target generation = %d/%d, want 3/%d", service.Generation, generation, tc.generation)
					}
					current, err := server.serviceView("api")
					if err != nil || current.Generation() != 3 {
						t.Fatalf("database generation changed before preflight: %d, %v", current.Generation(), err)
					}
					return nil
				},
			}
			if err := tc.run(execer); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if !reflect.DeepEqual(events, []string{"preflight", tc.wantAction}) {
				t.Fatalf("events = %v, want preflight before %s", events, tc.wantAction)
			}
		})
	}
}

func TestServiceRollbackTreatsAbsentHistoricalSandboxPolicyAsLegacy(t *testing.T) {
	server := newTestServer(t)
	artifacts, staticVerifications := canonicalLegacyRollbackArtifacts(t, server, "api", 1, 2)
	service := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: server.defaultServiceRootDir("api"),
		Generation: 2, LatestGeneration: 2, Artifacts: artifacts,
		Sandbox: &db.ServiceSandboxStore{Refs: map[db.ArtifactRef]*db.ServiceSandboxPolicy{
			db.Gen(2): {State: "off"},
			"latest":  {State: "off"},
		}},
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": service.Clone()}}); err != nil {
		t.Fatal(err)
	}
	oldEnsure, oldProbe := ensureBubblewrapForServiceSandboxMutation, probeServiceSandboxForMutation
	t.Cleanup(func() {
		ensureBubblewrapForServiceSandboxMutation = oldEnsure
		probeServiceSandboxForMutation = oldProbe
	})
	ensureBubblewrapForServiceSandboxMutation = func(context.Context) error {
		t.Fatal("legacy rollback ensured Bubblewrap")
		return nil
	}
	probeServiceSandboxForMutation = func(context.Context, serviceSandboxPlan, uint32, uint32) error {
		t.Fatal("legacy rollback probed Bubblewrap")
		return nil
	}
	installed := 0
	execer := &ttyExecer{
		ctx: context.Background(), s: server, sn: "api", rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
		serviceInstallGenFunc: func(_ InstallerCfg, generation int) error {
			installed = generation
			return nil
		},
	}

	if err := execer.rollbackCmdFunc("api"); err != nil {
		t.Fatalf("rollback to absent-policy legacy generation: %v", err)
	}
	current, err := server.serviceView("api")
	if err != nil || current.Generation() != 1 || installed != 1 {
		t.Fatalf("legacy rollback generation/install = %d/%d, %v; want 1/1", current.Generation(), installed, err)
	}
	if *staticVerifications != 1 {
		t.Fatalf("legacy rollback static verifications = %d, want 1", *staticVerifications)
	}
}

func TestPreflightSandboxGenerationActivationUsesExactTargetAndNeverExecutesTimerPayload(t *testing.T) {
	server, service, counter := serviceActivationSandboxFixture(t, "on", true)
	exactPolicy := service.Sandbox.Refs[db.Gen(service.Generation)].Clone()
	resolver, _ := service.Artifacts.Gen(db.ArtifactNetNSResolv, service.Generation)
	unit, _ := service.Artifacts.Gen(db.ArtifactSystemdUnit, service.Generation)
	payload, _ := service.Artifacts.Gen(db.ArtifactBinary, service.Generation)

	oldEnsure := ensureBubblewrapForServiceSandboxMutation
	oldValidate := validateServiceSandboxPolicyForMutation
	oldProbe := probeServiceSandboxForMutation
	oldVerify := verifyGeneratedSystemdUnitForSandboxMutation
	t.Cleanup(func() {
		ensureBubblewrapForServiceSandboxMutation = oldEnsure
		validateServiceSandboxPolicyForMutation = oldValidate
		probeServiceSandboxForMutation = oldProbe
		verifyGeneratedSystemdUnitForSandboxMutation = oldVerify
	})
	var order []string
	var capturedPolicy serviceSandboxPolicy
	ensureBubblewrapForServiceSandboxMutation = func(context.Context) error {
		order = append(order, "ensure")
		return nil
	}
	validateServiceSandboxPolicyForMutation = func(req serviceSandboxPlanRequest, active bool) (serviceSandboxPolicy, error) {
		order = append(order, fmt.Sprintf("validate:%t:%d:%d:%s", active, req.UID, req.GID, req.ResolverSource))
		if req.Payload != payload {
			t.Fatalf("validated payload = %q, want exact %q", req.Payload, payload)
		}
		capturedPolicy = req.Policy
		return req.Policy, nil
	}
	probeServiceSandboxForMutation = func(_ context.Context, plan serviceSandboxPlan, uid, gid uint32) error {
		order = append(order, fmt.Sprintf("probe:%d:%d", uid, gid))
		separator := slices.Index(plan.Arguments, "--")
		if separator < 0 || separator != len(plan.Arguments)-1 || slices.Contains(plan.Arguments[separator+1:], payload) || slices.Contains(plan.Arguments, "--serve") {
			t.Fatalf("timer probe would execute payload: %#v", plan.Arguments)
		}
		if !slicesContainAdjacent(plan.Arguments, "--ro-bind", resolver) {
			t.Fatalf("timer probe resolver = %#v, want exact %q", plan.Arguments, resolver)
		}
		return nil
	}
	verifyGeneratedSystemdUnitForSandboxMutation = func(_ context.Context, path string) error {
		order = append(order, "verify")
		if path != unit {
			t.Fatalf("verified unit = %q, want exact %q", path, unit)
		}
		return nil
	}

	if err := server.preflightSandboxGenerationActivation(context.Background(), service, service.Generation); err != nil {
		t.Fatalf("preflight exact sandbox generation: %v", err)
	}
	identity := effectiveServiceIdentity(service.View()).Persisted
	want := []string{
		"ensure",
		fmt.Sprintf("validate:true:%d:%d:%s", identity.UID, identity.GID, resolver),
		fmt.Sprintf("probe:%d:%d", identity.UID, identity.GID),
		"verify",
	}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("preflight order = %v, want %v", order, want)
	}
	if _, err := os.Stat(counter); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("timer payload counter exists after preflight: %v", err)
	}
	if len(capturedPolicy.ReadOnly) == 0 {
		t.Fatal("exact target policy was not passed to validation")
	}
	capturedPolicy.ReadOnly[0].Destination = "/mutated-after-preflight"
	if !reflect.DeepEqual(service.Sandbox.Refs[db.Gen(service.Generation)], exactPolicy) {
		t.Fatal("validated exact policy aliases persisted target generation")
	}
	if service.Sandbox.Refs["latest"].State != "off" || service.Sandbox.Refs["staged"].State != "off" {
		t.Fatalf("preflight selected or mutated latest/staged policy: %#v", service.Sandbox.Refs)
	}
}

func TestPreflightSandboxGenerationActivationAcceptsPreSandboxLegacyUnit(t *testing.T) {
	server, service, _ := serviceActivationSandboxFixture(t, "legacy", false)
	unit, _ := service.Artifacts.Gen(db.ArtifactSystemdUnit, service.Generation)
	raw, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	legacyLines := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(line, "BindReadOnlyPaths=") || line == "PrivateMounts=yes" {
			continue
		}
		legacyLines = append(legacyLines, line)
	}
	legacyRaw := []byte(strings.Join(legacyLines, "\n"))
	if err := os.WriteFile(unit, legacyRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	delete(service.Artifacts, db.ArtifactNetNSResolv)

	oldEnsure, oldValidate := ensureBubblewrapForServiceSandboxMutation, validateServiceSandboxPolicyForMutation
	oldProbe, oldVerify := probeServiceSandboxForMutation, verifyGeneratedSystemdUnitForSandboxMutation
	t.Cleanup(func() {
		ensureBubblewrapForServiceSandboxMutation = oldEnsure
		validateServiceSandboxPolicyForMutation = oldValidate
		probeServiceSandboxForMutation = oldProbe
		verifyGeneratedSystemdUnitForSandboxMutation = oldVerify
	})
	ensureBubblewrapForServiceSandboxMutation = func(context.Context) error {
		t.Fatal("pre-sandbox legacy activation ensured Bubblewrap")
		return nil
	}
	validateServiceSandboxPolicyForMutation = func(serviceSandboxPlanRequest, bool) (serviceSandboxPolicy, error) {
		t.Fatal("pre-sandbox legacy activation validated a policy")
		return serviceSandboxPolicy{}, nil
	}
	probeServiceSandboxForMutation = func(context.Context, serviceSandboxPlan, uint32, uint32) error {
		t.Fatal("pre-sandbox legacy activation probed Bubblewrap")
		return nil
	}
	verified := 0
	verifyGeneratedSystemdUnitForSandboxMutation = func(_ context.Context, path string) error {
		verified++
		if path != unit {
			t.Fatalf("legacy static verification path = %q, want %q", path, unit)
		}
		return nil
	}

	if err := server.preflightSandboxGenerationActivation(context.Background(), service, service.Generation); err != nil {
		t.Fatalf("preflight pre-sandbox legacy unit: %v", err)
	}
	if verified != 1 {
		t.Fatalf("legacy static verifications = %d, want 1", verified)
	}
	if got, readErr := os.ReadFile(unit); readErr != nil || !bytes.Equal(got, legacyRaw) {
		t.Fatalf("pre-sandbox legacy unit changed: %v\n%s", readErr, got)
	}
}

func TestPreflightSandboxGenerationActivationHonorsCancellationBoundaries(t *testing.T) {
	for _, stage := range []string{"before ensure", "after ensure", "after validation", "after probe", "after static verify"} {
		t.Run(stage, func(t *testing.T) {
			server, service, _ := serviceActivationSandboxFixture(t, "on", false)
			ctx, cancel := context.WithCancel(context.Background())
			if stage == "before ensure" {
				cancel()
			}
			oldEnsure := ensureBubblewrapForServiceSandboxMutation
			oldValidate := validateServiceSandboxPolicyForMutation
			oldProbe := probeServiceSandboxForMutation
			oldVerify := verifyGeneratedSystemdUnitForSandboxMutation
			t.Cleanup(func() {
				ensureBubblewrapForServiceSandboxMutation = oldEnsure
				validateServiceSandboxPolicyForMutation = oldValidate
				probeServiceSandboxForMutation = oldProbe
				verifyGeneratedSystemdUnitForSandboxMutation = oldVerify
			})
			var events []string
			ensureBubblewrapForServiceSandboxMutation = func(context.Context) error {
				events = append(events, "ensure")
				if stage == "after ensure" {
					cancel()
				}
				return nil
			}
			validateServiceSandboxPolicyForMutation = func(req serviceSandboxPlanRequest, _ bool) (serviceSandboxPolicy, error) {
				events = append(events, "validate")
				if stage == "after validation" {
					cancel()
				}
				return req.Policy, nil
			}
			probeServiceSandboxForMutation = func(context.Context, serviceSandboxPlan, uint32, uint32) error {
				events = append(events, "probe")
				if stage == "after probe" {
					cancel()
				}
				return nil
			}
			verifyGeneratedSystemdUnitForSandboxMutation = func(context.Context, string) error {
				events = append(events, "verify")
				if stage == "after static verify" {
					cancel()
				}
				return nil
			}
			err := server.preflightSandboxGenerationActivation(ctx, service, service.Generation)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("preflight error = %v, want context cancellation", err)
			}
			want := map[string][]string{
				"before ensure":       nil,
				"after ensure":        {"ensure"},
				"after validation":    {"ensure", "validate"},
				"after probe":         {"ensure", "validate", "probe"},
				"after static verify": {"ensure", "validate", "probe", "verify"},
			}[stage]
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events = %v, want %v", events, want)
			}
		})
	}
}

func TestPreflightSandboxGenerationActivationOffAndLegacySkipDependencyAndProbe(t *testing.T) {
	for _, state := range []string{"off", "legacy"} {
		t.Run(state, func(t *testing.T) {
			server, service, _ := serviceActivationSandboxFixture(t, state, false)
			unit, _ := service.Artifacts.Gen(db.ArtifactSystemdUnit, service.Generation)
			oldEnsure := ensureBubblewrapForServiceSandboxMutation
			oldValidate := validateServiceSandboxPolicyForMutation
			oldProbe := probeServiceSandboxForMutation
			oldVerify := verifyGeneratedSystemdUnitForSandboxMutation
			t.Cleanup(func() {
				ensureBubblewrapForServiceSandboxMutation = oldEnsure
				validateServiceSandboxPolicyForMutation = oldValidate
				probeServiceSandboxForMutation = oldProbe
				verifyGeneratedSystemdUnitForSandboxMutation = oldVerify
			})
			validated, verified := 0, 0
			ensureBubblewrapForServiceSandboxMutation = func(context.Context) error {
				t.Fatal("off/legacy activation installed Bubblewrap")
				return nil
			}
			validateServiceSandboxPolicyForMutation = func(req serviceSandboxPlanRequest, active bool) (serviceSandboxPolicy, error) {
				validated++
				if active {
					t.Fatal("off activation validated as active")
				}
				return req.Policy, nil
			}
			probeServiceSandboxForMutation = func(context.Context, serviceSandboxPlan, uint32, uint32) error {
				t.Fatal("off/legacy activation probed Bubblewrap")
				return nil
			}
			verifyGeneratedSystemdUnitForSandboxMutation = func(_ context.Context, path string) error {
				verified++
				if path != unit {
					t.Fatalf("verified path = %q, want %q", path, unit)
				}
				return nil
			}
			if err := server.preflightSandboxGenerationActivation(context.Background(), service, service.Generation); err != nil {
				t.Fatalf("preflight %s activation: %v", state, err)
			}
			wantValidated := 1
			if state == "legacy" {
				wantValidated = 0
			}
			if validated != wantValidated || verified != 1 {
				t.Fatalf("%s validation/static calls = %d/%d, want %d/1", state, validated, verified, wantValidated)
			}
		})
	}
}

func TestPreflightSandboxGenerationActivationRejectsExactRecordFailuresBeforeDependency(t *testing.T) {
	for _, policyCase := range []string{"nil", "malformed"} {
		t.Run("policy "+policyCase, func(t *testing.T) {
			server, service, _ := serviceActivationSandboxFixture(t, "on", false)
			switch policyCase {
			case "nil":
				service.Sandbox.Refs[db.Gen(service.Generation)] = nil
			case "malformed":
				service.Sandbox.Refs[db.Gen(service.Generation)].State = "sometimes"
			}
			assertActivationPreflightFailsBeforeEnsure(t, server, service)
		})
	}
	for _, artifact := range []db.ArtifactName{db.ArtifactSystemdUnit, db.ArtifactBinary, db.ArtifactNetNSResolv} {
		for _, failure := range []string{"missing", "nonregular", "unreadable"} {
			t.Run(string(artifact)+" "+failure, func(t *testing.T) {
				server, service, _ := serviceActivationSandboxFixture(t, "on", false)
				record := service.Artifacts[artifact]
				exact := db.Gen(service.Generation)
				switch failure {
				case "missing":
					delete(record.Refs, exact)
				case "nonregular":
					record.Refs[exact] = serviceDataDirForRoot(server.serviceRootFromView(service.View()))
				case "unreadable":
					path := record.Refs[exact]
					if err := os.Chmod(path, 0); err != nil {
						t.Fatal(err)
					}
				}
				assertActivationPreflightFailsBeforeEnsure(t, server, service)
			})
		}
	}
}

func TestStartAndRestartSandboxPreflightFailureDoesNotTouchRuntime(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*ttyExecer) error
		kind string
	}{
		{name: "start removed Bubblewrap", run: (*ttyExecer).startCmdFunc, kind: "removed"},
		{name: "restart untrusted Bubblewrap", run: (*ttyExecer).restartCmdFunc, kind: "untrusted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, service, _ := serviceActivationSandboxFixture(t, "on", false)
			if tc.kind == "untrusted" {
				unit, _ := service.Artifacts.Gen(db.ArtifactSystemdUnit, service.Generation)
				raw, err := os.ReadFile(unit)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(unit, []byte(strings.Replace(string(raw), bubblewrapPath, "/tmp/untrusted-bwrap", 1)), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			oldEnsure := ensureBubblewrapForServiceSandboxMutation
			oldValidate := validateServiceSandboxPolicyForMutation
			oldProbe := probeServiceSandboxForMutation
			oldVerify := verifyGeneratedSystemdUnitForSandboxMutation
			t.Cleanup(func() {
				ensureBubblewrapForServiceSandboxMutation = oldEnsure
				validateServiceSandboxPolicyForMutation = oldValidate
				probeServiceSandboxForMutation = oldProbe
				verifyGeneratedSystemdUnitForSandboxMutation = oldVerify
			})
			removed := errors.New("Bubblewrap disappeared")
			ensureBubblewrapForServiceSandboxMutation = func(context.Context) error {
				if tc.kind == "removed" {
					return removed
				}
				return nil
			}
			validateServiceSandboxPolicyForMutation = func(req serviceSandboxPlanRequest, _ bool) (serviceSandboxPolicy, error) { return req.Policy, nil }
			probeServiceSandboxForMutation = func(context.Context, serviceSandboxPlan, uint32, uint32) error {
				t.Fatal("failed activation reached probe")
				return nil
			}
			verifyGeneratedSystemdUnitForSandboxMutation = func(context.Context, string) error {
				t.Fatal("failed activation reached static verification")
				return nil
			}
			runner := &recordingServiceRunner{}
			execer := &ttyExecer{
				ctx: context.Background(), s: server, sn: service.Name, rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
				serviceRunnerFn: func() (ServiceRunner, error) { return runner, nil },
			}
			err := tc.run(execer)
			if err == nil {
				t.Fatal("activation unexpectedly succeeded")
			}
			if tc.kind == "removed" && !errors.Is(err, removed) {
				t.Fatalf("activation error = %v, want removed Bubblewrap", err)
			}
			if tc.kind == "untrusted" && !strings.Contains(err.Error(), "fixed "+bubblewrapPath) {
				t.Fatalf("activation error = %v, want untrusted Bubblewrap rejection", err)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("failed activation runtime calls = %v", runner.calls)
			}
			current, viewErr := server.serviceView(service.Name)
			if viewErr != nil || current.Generation() != service.Generation {
				t.Fatalf("failed activation generation = %d, %v, want %d", current.Generation(), viewErr, service.Generation)
			}
		})
	}
}

func assertActivationPreflightFailsBeforeEnsure(t *testing.T, server *Server, service *db.Service) {
	t.Helper()
	oldEnsure := ensureBubblewrapForServiceSandboxMutation
	t.Cleanup(func() { ensureBubblewrapForServiceSandboxMutation = oldEnsure })
	ensured := false
	ensureBubblewrapForServiceSandboxMutation = func(context.Context) error {
		ensured = true
		return nil
	}
	if err := server.preflightSandboxGenerationActivation(context.Background(), service, service.Generation); err == nil {
		t.Fatal("activation preflight unexpectedly accepted malformed exact target")
	}
	if ensured {
		t.Fatal("activation preflight performed dependency work before exact target rejection")
	}
}

func TestServiceRollbackExpectedCurrentGuardPreservesConcurrentGeneration(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "api", db.ServiceTypeSystemd, db.ArtifactStore{
		db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/api-1.service", db.Gen(2): "/tmp/api-2.service", db.Gen(3): "/tmp/api-3.service"}},
	})
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		service.Generation, service.LatestGeneration = 3, 3
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	installed := false
	execer := &ttyExecer{
		ctx: context.Background(), s: server, sn: "api", rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
		preflightSandboxGenerationActivationFunc: func(context.Context, *db.Service, int) error {
			_, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
				service.Generation = 1
				return nil
			})
			return err
		},
		serviceInstallGenFunc: func(InstallerCfg, int) error {
			installed = true
			return nil
		},
	}
	err := execer.rollbackCmdFunc("api")
	if err == nil || !strings.Contains(err.Error(), "changed from expected 3 to 1") {
		t.Fatalf("rollback error = %v, want expected-current rejection", err)
	}
	if installed {
		t.Fatal("rollback installed after concurrent generation change")
	}
	current, viewErr := server.serviceView("api")
	if viewErr != nil || current.Generation() != 1 {
		t.Fatalf("concurrent generation = %d, %v, want preserved 1", current.Generation(), viewErr)
	}
}

func TestServiceRollbackCancellationBeforeExpectedCurrentCommitPreservesGeneration(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "api", db.ServiceTypeSystemd, db.ArtifactStore{
		db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(2): "/tmp/api-2.service", db.Gen(3): "/tmp/api-3.service"}},
	})
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		service.Generation, service.LatestGeneration = 3, 3
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	installed := false
	execer := &ttyExecer{
		ctx: ctx, s: server, sn: "api", rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
		preflightSandboxGenerationActivationFunc: func(context.Context, *db.Service, int) error {
			cancel()
			return nil
		},
		serviceInstallGenFunc: func(InstallerCfg, int) error {
			installed = true
			return nil
		},
	}
	if err := execer.rollbackCmdFunc("api"); !errors.Is(err, context.Canceled) {
		t.Fatalf("rollback error = %v, want context cancellation", err)
	}
	if installed {
		t.Fatal("canceled rollback installed target generation")
	}
	current, err := server.serviceView("api")
	if err != nil || current.Generation() != 3 {
		t.Fatalf("canceled rollback generation = %d, %v, want 3", current.Generation(), err)
	}
}

func serviceActivationSandboxFixture(t *testing.T, state string, timer bool) (*Server, *db.Service, string) {
	t.Helper()
	server := newTestServer(t)
	root := server.defaultServiceRootDir("api")
	dataDir := serviceDataDirForRoot(root)
	binDir := serviceRunDirForRoot(root)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(t.TempDir(), "timer-payload-counter")
	payload := filepath.Join(binDir, "api-1")
	payloadContent := "#!/bin/sh\nprintf executed > " + counter + "\n"
	if err := os.WriteFile(payload, []byte(payloadContent), 0o755); err != nil {
		t.Fatal(err)
	}
	resolver := filepath.Join(binDir, "resolv-1.conf")
	if err := os.WriteFile(resolver, []byte("nameserver 127.0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exposure := filepath.Join(dataDir, "config")
	if err := os.WriteFile(exposure, []byte("config"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := db.ServiceIdentity{
		RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
	}
	policy := serviceSandboxPolicy{State: state}
	if state != "legacy" {
		policy.ReadOnly = []serviceSandboxExposure{{Source: exposure, Destination: "/config"}}
	}
	raw := "[Service]\nExecStart=" + payload + " --serve\nUser=" + identity.RequestedUser + "\nGroup=" + identity.RequestedGroup +
		"\nWorkingDirectory=" + dataDir + "\nEnvironment=HOME=" + dataDir + " USER=" + identity.RequestedUser + " LOGNAME=" + identity.RequestedUser + " SHELL=/bin/sh\n"
	unitRequest := nativeSandboxUnitRequest{
		CurrentPolicy: serviceSandboxPolicy{State: "legacy"}, TargetPolicy: policy, Identity: identity,
		Payload: payload, DataDir: dataDir, Resolver: resolver, Hostname: "api",
	}
	if state == "on" {
		plan, err := buildValidatedServiceSandboxPlan(serviceSandboxPlanRequest{
			Service: "api", Policy: policy, Payload: payload, DataDir: dataDir, ResolverSource: resolver,
			UID: identity.UID, GID: identity.GID, Hostname: "api",
		})
		if err != nil {
			t.Fatalf("build activation fixture plan: %v", err)
		}
		raw, _, err = renderNativeSandboxUnitWithPlan(raw, unitRequest, &plan)
		if err != nil {
			t.Fatalf("render activation fixture: %v", err)
		}
	} else {
		var err error
		raw, _, err = renderNativeSandboxUnitWithPlan(raw, unitRequest, nil)
		if err != nil {
			t.Fatalf("render direct activation fixture: %v", err)
		}
	}
	unit := filepath.Join(binDir, "api-1.service")
	if err := os.WriteFile(unit, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	service := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root, Generation: 1, LatestGeneration: 1,
		Identity: &identity,
		Artifacts: db.ArtifactStore{
			db.ArtifactBinary:      {Refs: map[db.ArtifactRef]string{db.Gen(1): payload, "latest": "/wrong/latest-payload", "staged": "/wrong/staged-payload"}},
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(1): unit, "latest": "/wrong/latest-unit", "staged": "/wrong/staged-unit"}},
			db.ArtifactNetNSResolv: {Refs: map[db.ArtifactRef]string{db.Gen(1): resolver, "latest": "/wrong/latest-resolver", "staged": "/wrong/staged-resolver"}},
		},
	}
	if timer {
		service.Artifacts[db.ArtifactSystemdTimerFile] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(1): filepath.Join(binDir, "api-1.timer")}}
	}
	if state != "legacy" {
		service.Sandbox = &db.ServiceSandboxStore{Refs: map[db.ArtifactRef]*db.ServiceSandboxPolicy{
			db.Gen(1): serviceSandboxPolicyToDB(policy), "latest": {State: "off"}, "staged": {State: "off"},
		}}
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{service.Name: service.Clone()}}); err != nil {
		t.Fatal(err)
	}
	return server, service, counter
}

func TestServiceRollbackRejectsVM(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "devbox", db.ServiceTypeVM, nil)
	if _, _, err := server.cfg.DB.MutateService("devbox", func(_ *db.Data, s *db.Service) error {
		s.Generation = 2
		s.LatestGeneration = 2
		return nil
	}); err != nil {
		t.Fatalf("seed generation: %v", err)
	}
	execer := &ttyExecer{
		ctx:      context.Background(),
		s:        server,
		sn:       "devbox",
		rw:       &bytes.Buffer{},
		progress: catchrpc.ProgressQuiet,
	}

	err := execer.serviceCmdFunc([]string{"rollback"})
	want := "VM services do not support generation rollback; use yeet snapshots restore for VM disk recovery"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("service rollback error = %v, want %q", err, want)
	}
	sv, err := server.serviceView("devbox")
	if err != nil {
		t.Fatalf("serviceView: %v", err)
	}
	if got := sv.AsStruct().Generation; got != 2 {
		t.Fatalf("stored generation = %d, want 2", got)
	}
}

func TestServiceActionCommandsUseRunner(t *testing.T) {
	tests := []struct {
		name string
		run  func(*ttyExecer) error
		want []string
	}{
		{name: "start", run: (*ttyExecer).startCmdFunc, want: []string{"start"}},
		{name: "stop", run: (*ttyExecer).stopCmdFunc, want: []string{"stop"}},
		{name: "restart", run: (*ttyExecer).restartCmdFunc, want: []string{"restart"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runner := &recordingServiceRunner{}
			execer := &ttyExecer{
				ctx:      context.Background(),
				sn:       "svc-a",
				rw:       &bytes.Buffer{},
				progress: catchrpc.ProgressQuiet,
				serviceRunnerFn: func() (ServiceRunner, error) {
					return runner, nil
				},
			}

			if err := tc.run(execer); err != nil {
				t.Fatalf("%s command returned error: %v", tc.name, err)
			}
			if !reflect.DeepEqual(runner.calls, tc.want) {
				t.Fatalf("runner calls = %#v, want %#v", runner.calls, tc.want)
			}
		})
	}
}

func TestServiceActionCommandsRecheckIdentityRecoveryAfterLock(t *testing.T) {
	tests := []struct {
		name string
		run  func(*ttyExecer) error
	}{
		{name: "start", run: (*ttyExecer).startCmdFunc},
		{name: "stop", run: (*ttyExecer).stopCmdFunc},
		{name: "restart", run: (*ttyExecer).restartCmdFunc},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)
			runner := &recordingServiceRunner{}
			execer := &ttyExecer{
				ctx:      context.Background(),
				s:        server,
				sn:       "api",
				rw:       &bytes.Buffer{},
				progress: catchrpc.ProgressQuiet,
				serviceRunnerFn: func() (ServiceRunner, error) {
					return runner, nil
				},
			}

			release := server.serviceOperationLocks.Lock("api")
			done := make(chan error, 1)
			go func() { done <- tc.run(execer) }()
			select {
			case err := <-done:
				release()
				t.Fatalf("%s bypassed service lock: %v", tc.name, err)
			case <-time.After(25 * time.Millisecond):
			}
			server.setServiceIdentityMutationBlock("api", errors.New("recovery required"))
			release()

			select {
			case err := <-done:
				if !errors.Is(err, errServiceIdentityRecoveryBlocked) {
					t.Fatalf("%s error = %v, want identity recovery block", tc.name, err)
				}
			case <-time.After(time.Second):
				t.Fatalf("%s did not resume after service lock release", tc.name)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("%s runner calls = %#v, want none", tc.name, runner.calls)
			}
		})
	}
}

func TestDispatchSerializesManageCommandWithoutAnInnerLock(t *testing.T) {
	server := newTestServer(t)
	runner := &recordingServiceRunner{}
	execer := &ttyExecer{
		ctx: context.Background(), s: server, sn: "api", rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
		serviceRunnerFn: func() (ServiceRunner, error) { return runner, nil },
	}

	release := server.serviceOperationLocks.Lock("api")
	done := make(chan error, 1)
	go func() { done <- execer.dispatch([]string{"disable"}) }()
	select {
	case err := <-done:
		release()
		t.Fatalf("disable bypassed service mutation lock: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	server.setServiceIdentityMutationBlock("api", errors.New("recovery required"))
	release()
	select {
	case err := <-done:
		if !errors.Is(err, errServiceIdentityRecoveryBlocked) {
			t.Fatalf("disable error = %v, want identity recovery block", err)
		}
	case <-time.After(time.Second):
		t.Fatal("disable did not resume after service mutation lock release")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("disable runner calls = %#v, want none", runner.calls)
	}
}

func TestStopISOServiceRetainsAllocationAndMarksStopped(t *testing.T) {
	server := newTestServer(t)
	allocation := testISORuntimeAllocation("app", iso.StateReady)
	allocation.Components = map[string]db.ISOComponent{
		"api": {Address: netip.MustParseAddr("172.30.128.2"), State: "reserved"},
	}
	allocation.RetiredComponents = map[string]db.ISOComponent{
		"worker": {Address: netip.MustParseAddr("172.30.128.3"), State: "retiring"},
	}
	wantAllocation := allocation.Clone()
	addTestServices(t, server, db.Service{Name: "app", ServiceType: db.ServiceTypeDockerCompose, ISO: allocation})
	runner := &recordingServiceRunner{}
	execer := &ttyExecer{
		ctx: context.Background(), s: server, sn: "app", rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
		serviceRunnerFn: func() (ServiceRunner, error) { return runner, nil },
	}

	if err := execer.stopCmdFunc(); err != nil {
		t.Fatal(err)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	got := dv.Services().Get("app").ISO().AsStruct()
	if got.State != string(iso.StateStopped) || got.LastError != "" {
		t.Fatalf("stopped ISO state = %#v, want stopped without error", got)
	}
	got.State = wantAllocation.State
	got.LastError = wantAllocation.LastError
	if !reflect.DeepEqual(got, wantAllocation) {
		t.Fatalf("ordinary stop changed stable ISO allocation:\n got %#v\nwant %#v", got, wantAllocation)
	}
	if !reflect.DeepEqual(runner.calls, []string{"stop"}) {
		t.Fatalf("runner calls = %#v, want stop", runner.calls)
	}
}

func TestStartAndRestartISOServiceUseFullInstallerLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*ttyExecer) error
	}{
		{name: "start", run: (*ttyExecer).startCmdFunc},
		{name: "restart", run: (*ttyExecer).restartCmdFunc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)
			allocation := testISORuntimeAllocation("app", iso.StateStopped)
			addTestServices(t, server, db.Service{
				Name: "app", ServiceType: db.ServiceTypeDockerCompose,
				Generation: 3, LatestGeneration: 3, ISO: allocation,
			})
			runner := &recordingServiceRunner{}
			installedGeneration := 0
			execer := &ttyExecer{
				ctx: context.Background(), s: server, sn: "app", rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
				serviceRunnerFn: func() (ServiceRunner, error) { return runner, nil },
				serviceInstallGenFunc: func(cfg InstallerCfg, generation int) error {
					if cfg.ServiceName != "app" {
						t.Fatalf("installer service = %q, want app", cfg.ServiceName)
					}
					installedGeneration = generation
					return nil
				},
			}

			if err := tc.run(execer); err != nil {
				t.Fatal(err)
			}
			if installedGeneration != 3 {
				t.Fatalf("installed generation = %d, want 3", installedGeneration)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("ISO %s used raw runner calls: %#v", tc.name, runner.calls)
			}
		})
	}
}

func TestStartAndRestartISOVMUseVMRunner(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*ttyExecer) error
		want []string
	}{
		{name: "start", run: (*ttyExecer).startCmdFunc, want: []string{"start"}},
		{name: "restart", run: (*ttyExecer).restartCmdFunc, want: []string{"restart"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previousEnsure := ensureVMNetworkForServiceAction
			defer func() { ensureVMNetworkForServiceAction = previousEnsure }()
			server := newTestServer(t)
			allocation := testISORuntimeAllocation("devbox", iso.StateStopped)
			addTestServices(t, server, db.Service{
				Name: "devbox", ServiceType: db.ServiceTypeVM, ISO: allocation,
			})
			runner := &recordingServiceRunner{}
			ensureCalls := 0
			ensureVMNetworkForServiceAction = func(got *Server, _ context.Context, service string) error {
				ensureCalls++
				if got != server || service != "devbox" {
					t.Fatalf("ensure VM network = (%p, %q), want (%p, devbox)", got, service, server)
				}
				return nil
			}
			execer := &ttyExecer{
				ctx: context.Background(), s: server, sn: "devbox", rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
				serviceRunnerFn: func() (ServiceRunner, error) { return runner, nil },
				serviceInstallGenFunc: func(InstallerCfg, int) error {
					t.Fatal("ISO VM action used the service installer")
					return nil
				},
			}

			if err := tc.run(execer); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(runner.calls, tc.want) {
				t.Fatalf("runner calls = %#v, want %#v", runner.calls, tc.want)
			}
			if ensureCalls != 1 {
				t.Fatalf("ensure calls = %d, want 1", ensureCalls)
			}
		})
	}
}

func TestStartISOVMReleasesRuntimeTransactionBeforeRunner(t *testing.T) {
	previousEnsure := ensureVMNetworkForServiceAction
	defer func() { ensureVMNetworkForServiceAction = previousEnsure }()
	server := newTestServer(t)
	addTestServices(t, server, db.Service{
		Name: "devbox", ServiceType: db.ServiceTypeVM,
		ISO: testISORuntimeAllocation("devbox", iso.StateStopped),
	})
	held := false
	ensureVMNetworkForServiceAction = func(*Server, context.Context, string) error {
		if !held {
			t.Fatal("VM network preflight ran outside the runtime transaction")
		}
		return nil
	}
	runner := &recordingServiceRunner{onCall: func(call string) {
		if call == "start" && held {
			t.Fatal("VM runner started while holding the runtime transaction lock")
		}
	}}
	execer := &ttyExecer{
		ctx: context.Background(), s: server, sn: "devbox", rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
		serviceRunnerFn: func() (ServiceRunner, error) { return runner, nil },
		vmRuntimeTransactionFunc: func(_ context.Context, _ *Config, operation func() error) error {
			held = true
			defer func() { held = false }()
			return operation()
		},
	}

	if err := execer.startCmdFunc(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runner.calls, []string{"start"}) {
		t.Fatalf("runner calls = %#v, want start", runner.calls)
	}
}

func TestStartISOVMStopsBeforeRunnerWhenNetworkEnsureFails(t *testing.T) {
	previousEnsure := ensureVMNetworkForServiceAction
	defer func() { ensureVMNetworkForServiceAction = previousEnsure }()
	server := newTestServer(t)
	allocation := testISORuntimeAllocation("devbox", iso.StateStopped)
	addTestServices(t, server, db.Service{Name: "devbox", ServiceType: db.ServiceTypeVM, ISO: allocation})
	wantErr := errors.New("policy verification failed")
	ensureVMNetworkForServiceAction = func(*Server, context.Context, string) error { return wantErr }
	runner := &recordingServiceRunner{}
	execer := &ttyExecer{
		ctx: context.Background(), s: server, sn: "devbox", rw: &bytes.Buffer{}, progress: catchrpc.ProgressQuiet,
		serviceRunnerFn: func() (ServiceRunner, error) { return runner, nil },
	}

	err := execer.startCmdFunc()
	if !errors.Is(err, wantErr) {
		t.Fatalf("start error = %v, want %v", err, wantErr)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %#v, want none", runner.calls)
	}
}

func TestVMActionCommandsUseVMProgressLabels(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "devbox", db.ServiceTypeVM, nil)

	tests := []struct {
		name string
		run  func(*ttyExecer) error
		want string
	}{
		{name: "start", run: (*ttyExecer).startCmdFunc, want: `step="Start VM"`},
		{name: "stop", run: (*ttyExecer).stopCmdFunc, want: `step="Stop VM"`},
		{name: "restart", run: (*ttyExecer).restartCmdFunc, want: `step="Restart VM"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			runner := &recordingServiceRunner{}
			execer := &ttyExecer{
				ctx:      context.Background(),
				s:        server,
				sn:       "devbox",
				rw:       &out,
				progress: catchrpc.ProgressPlain,
				serviceRunnerFn: func() (ServiceRunner, error) {
					return runner, nil
				},
			}

			if err := tc.run(execer); err != nil {
				t.Fatalf("%s command returned error: %v", tc.name, err)
			}
			if got := out.String(); !strings.Contains(got, tc.want) {
				t.Fatalf("progress output = %q, want %q", got, tc.want)
			}
			if got := out.String(); strings.Contains(got, "Start service") || strings.Contains(got, "Stop service") || strings.Contains(got, "Restart service") {
				t.Fatalf("progress output = %q, did not expect generic service step label", got)
			}
		})
	}
}

func TestServiceActionCommandsRejectReservedNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		sn   string
		run  func(*ttyExecer) error
		want string
	}{
		{name: "start sys", sn: SystemService, run: (*ttyExecer).startCmdFunc, want: "cannot start system service"},
		{name: "stop catch", sn: CatchService, run: (*ttyExecer).stopCmdFunc, want: "cannot stop system service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(&ttyExecer{sn: tc.sn, rw: &bytes.Buffer{}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestServiceActionCommandReturnsRunnerError(t *testing.T) {
	startErr := errors.New("start failed")
	runner := &recordingServiceRunner{errs: map[string]error{"start": startErr}}
	execer := &ttyExecer{
		ctx:      context.Background(),
		sn:       "svc-a",
		rw:       &bytes.Buffer{},
		progress: catchrpc.ProgressQuiet,
		serviceRunnerFn: func() (ServiceRunner, error) {
			return runner, nil
		},
	}

	err := execer.startCmdFunc()
	if err == nil {
		t.Fatal("expected start error")
	}
	if !errors.Is(err, startErr) {
		t.Fatalf("start error = %v, want %v", err, startErr)
	}
}

func TestEnableDisableCommandsUseServiceEnabler(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*ttyExecer) error
		want []string
	}{
		{name: "enable", run: (*ttyExecer).enableCmdFunc, want: []string{"enable"}},
		{name: "disable", run: (*ttyExecer).disableCmdFunc, want: []string{"disable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &recordingServiceRunner{}
			execer := &ttyExecer{
				sn: "svc-a",
				serviceRunnerFn: func() (ServiceRunner, error) {
					return runner, nil
				},
			}

			if err := tc.run(execer); err != nil {
				t.Fatalf("%s returned error: %v", tc.name, err)
			}
			if !reflect.DeepEqual(runner.calls, tc.want) {
				t.Fatalf("runner calls = %#v, want %#v", runner.calls, tc.want)
			}
		})
	}
}

func TestEnableCommandRejectsRunnerWithoutEnableSupport(t *testing.T) {
	execer := &ttyExecer{
		sn: "svc-a",
		serviceRunnerFn: func() (ServiceRunner, error) {
			return basicServiceRunner{}, nil
		},
	}

	err := execer.enableCmdFunc()
	if err == nil || !strings.Contains(err.Error(), "service does not support enable") {
		t.Fatalf("enable error = %v, want unsupported enable", err)
	}
}

func TestEnableDisableCommandsRejectReservedNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		sn   string
		run  func(*ttyExecer) error
		want string
	}{
		{name: "enable system", sn: SystemService, run: (*ttyExecer).enableCmdFunc, want: "cannot install, reserved service name"},
		{name: "disable catch", sn: CatchService, run: (*ttyExecer).disableCmdFunc, want: "cannot disable system service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(&ttyExecer{sn: tc.sn})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDisableCommandRejectsRunnerWithoutDisableSupport(t *testing.T) {
	execer := &ttyExecer{
		sn: "svc-a",
		serviceRunnerFn: func() (ServiceRunner, error) {
			return basicServiceRunner{}, nil
		},
	}

	err := execer.disableCmdFunc()
	if err == nil || !strings.Contains(err.Error(), "service does not support disable") {
		t.Fatalf("disable error = %v, want unsupported disable", err)
	}
}

func TestLogsCommandPassesOptionsToRunner(t *testing.T) {
	runner := &recordingServiceRunner{}
	execer := &ttyExecer{
		sn: "svc-a",
		serviceRunnerFn: func() (ServiceRunner, error) {
			return runner, nil
		},
	}

	if err := execer.logsCmdFunc(cli.LogsFlags{Follow: true, Lines: 42}); err != nil {
		t.Fatalf("logsCmdFunc returned error: %v", err)
	}
	if runner.logOptions == nil {
		t.Fatal("expected log options")
	}
	if !runner.logOptions.Follow || runner.logOptions.Lines != 42 {
		t.Fatalf("log options = %#v, want follow and 42 lines", runner.logOptions)
	}
}

func TestLogsCommandPropagatesRunnerError(t *testing.T) {
	logErr := errors.New("logs failed")
	runner := &recordingServiceRunner{errs: map[string]error{"logs": logErr}}
	execer := &ttyExecer{
		sn: "svc-a",
		serviceRunnerFn: func() (ServiceRunner, error) {
			return runner, nil
		},
	}

	err := execer.logsCmdFunc(cli.LogsFlags{})
	if err == nil || !errors.Is(err, logErr) {
		t.Fatalf("logsCmdFunc error = %v, want %v", err, logErr)
	}
}

func TestLogsCommandRejectsSystemService(t *testing.T) {
	err := (&ttyExecer{sn: SystemService}).logsCmdFunc(cli.LogsFlags{})
	if err == nil || !strings.Contains(err.Error(), "cannot show logs for system service") {
		t.Fatalf("logs error = %v, want system service error", err)
	}
}

func TestStatusCmdFuncRendersSystemStatusesWithoutLiveCommands(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "timer", db.ServiceTypeSystemd, db.ArtifactStore{
		db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/tmp/timer.timer", "latest": "/tmp/timer.timer"}},
	})
	seedService(t, server, "devbox", db.ServiceTypeVM, nil)
	seedService(t, server, "web", db.ServiceTypeDockerCompose, db.ArtifactStore{
		db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/compose.yml"}},
	})

	var out bytes.Buffer
	execer := &ttyExecer{
		s:  server,
		sn: SystemService,
		rw: &out,
		systemdStatusesFunc: func() (map[string]svc.Status, error) {
			return map[string]svc.Status{"timer": svc.StatusStopped}, nil
		},
		systemdStatusFunc: func(sn string) (svc.Status, error) {
			if sn != "yeet-vm-devbox.service" {
				t.Fatalf("systemd status service = %q, want yeet-vm-devbox.service", sn)
			}
			return svc.StatusRunning, nil
		},
		dockerComposeStatusesFunc: func() (map[string]svc.DockerComposeStatus, error) {
			return map[string]svc.DockerComposeStatus{
				"web": {"api": svc.StatusRunning},
			}, nil
		},
	}

	if err := execer.statusCmdFunc(cli.StatusFlags{}); err != nil {
		t.Fatalf("statusCmdFunc returned error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("status lines = %d, want 4\n%s", len(lines), out.String())
	}
	if got := strings.Fields(lines[1]); !reflect.DeepEqual(got, []string{"devbox", "vm", "-", "running"}) {
		t.Fatalf("devbox row = %#v\n%s", got, out.String())
	}
	if got := strings.Fields(lines[2]); !reflect.DeepEqual(got, []string{"timer", "cron", "-", "stopped"}) {
		t.Fatalf("timer row = %#v\n%s", got, out.String())
	}
	if got := strings.Fields(lines[3]); !reflect.DeepEqual(got, []string{"web", "docker", "api", "running"}) {
		t.Fatalf("web row = %#v\n%s", got, out.String())
	}
}

func TestSystemStatusDataUsesSnapshotCollectorWithoutLegacyHooks(t *testing.T) {
	oldNewStatusSnapshotCommand := newStatusSnapshotCommand
	t.Cleanup(func() { newStatusSnapshotCommand = oldNewStatusSnapshotCommand })

	server := newTestServer(t)
	seedService(t, server, "web", db.ServiceTypeDockerCompose, db.ArtifactStore{
		db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/web.yml"}},
	})
	seedService(t, server, "api", db.ServiceTypeSystemd, nil)
	seedService(t, server, "devbox", db.ServiceTypeVM, nil)

	newStatusSnapshotCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch name {
		case "docker":
			if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, s *db.Service) error {
				s.Artifacts = db.ArtifactStore{
					db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/api.timer"}},
				}
				return nil
			}); err != nil {
				t.Fatalf("mutate api after snapshot DB read: %v", err)
			}
			return fakeStatusSnapshotCommand(t, `{"State":"running","Labels":"com.docker.compose.project=catch-web,com.docker.compose.service=app"}`)
		case "systemctl":
			got := append([]string{name}, args...)
			joined := strings.Join(got, " ")
			if !strings.Contains(joined, "api.service") || !strings.Contains(joined, "yeet-vm-devbox.service") {
				t.Fatalf("systemctl args = %q, want api and VM units", joined)
			}
			return fakeStatusSnapshotCommand(t, strings.Join([]string{
				"Id=api.service",
				"LoadState=loaded",
				"ActiveState=active",
				"SubState=running",
				"",
				"Id=yeet-vm-devbox.service",
				"LoadState=loaded",
				"ActiveState=inactive",
				"SubState=dead",
			}, "\n"))
		default:
			t.Fatalf("unexpected command %s %v", name, args)
			return fakeStatusSnapshotCommand(t, "")
		}
	}

	statuses, err := (&ttyExecer{s: server, sn: SystemService}).systemStatusData()
	if err != nil {
		t.Fatalf("systemStatusData returned error: %v", err)
	}
	got := statusByName(statuses)
	assertComponents(t, got["web"], []ComponentStatusData{{Name: "app", Status: ComponentStatusRunning}})
	assertComponents(t, got["api"], []ComponentStatusData{{Name: "api", Status: ComponentStatusRunning}})
	assertComponents(t, got["devbox"], []ComponentStatusData{{Name: "devbox", Status: ComponentStatusStopped}})
}

func TestSingleDockerComposeStatusUnknownRendersUnknownComponent(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "web", db.ServiceTypeDockerCompose, db.ArtifactStore{
		db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/compose.yml"}},
	})
	var out bytes.Buffer
	execer := &ttyExecer{
		s:  server,
		sn: "web",
		rw: &out,
		dockerComposeStatusFunc: func(string) (svc.DockerComposeStatus, error) {
			return nil, svc.ErrDockerStatusUnknown
		},
	}

	if err := execer.statusCmdFunc(cli.StatusFlags{}); err != nil {
		t.Fatalf("statusCmdFunc returned error: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "web") || !strings.Contains(got, "unknown") {
		t.Fatalf("status output = %q, want unknown web component", got)
	}
}

func TestSingleDockerComposeStatusWithNoComponentsSkipsRender(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "web", db.ServiceTypeDockerCompose, db.ArtifactStore{
		db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/compose.yml"}},
	})
	var out bytes.Buffer
	execer := &ttyExecer{
		s:  server,
		sn: "web",
		rw: &out,
		dockerComposeStatusFunc: func(string) (svc.DockerComposeStatus, error) {
			return svc.DockerComposeStatus{}, nil
		},
	}

	if err := execer.statusCmdFunc(cli.StatusFlags{}); err != nil {
		t.Fatalf("statusCmdFunc returned error: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("status output = %q, want no render", got)
	}
}

func TestSingleSystemdStatusUsesStatusHook(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "timer", db.ServiceTypeSystemd, db.ArtifactStore{
		db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/timer.timer"}},
	})
	var out bytes.Buffer
	execer := &ttyExecer{
		s:  server,
		sn: "timer",
		rw: &out,
		systemdStatusFunc: func(sn string) (svc.Status, error) {
			if sn != "timer" {
				t.Fatalf("systemd status service = %q, want timer", sn)
			}
			return svc.StatusRunning, nil
		},
	}

	if err := execer.statusCmdFunc(cli.StatusFlags{}); err != nil {
		t.Fatalf("statusCmdFunc returned error: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "timer") || !strings.Contains(got, "running") {
		t.Fatalf("status output = %q, want running timer", got)
	}
}

func TestSingleDockerComposeStatusPropagatesUnexpectedError(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "web", db.ServiceTypeDockerCompose, db.ArtifactStore{
		db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/compose.yml"}},
	})
	statusErr := errors.New("docker failed")
	execer := &ttyExecer{
		s:  server,
		sn: "web",
		rw: &bytes.Buffer{},
		dockerComposeStatusFunc: func(string) (svc.DockerComposeStatus, error) {
			return nil, statusErr
		},
	}

	err := execer.statusCmdFunc(cli.StatusFlags{})
	if err == nil || !errors.Is(err, statusErr) {
		t.Fatalf("statusCmdFunc error = %v, want %v", err, statusErr)
	}
}

func TestSystemStatusDataPropagatesStatusErrors(t *testing.T) {
	systemdErr := errors.New("systemd failed")
	execer := &ttyExecer{
		systemdStatusesFunc: func() (map[string]svc.Status, error) {
			return nil, systemdErr
		},
	}
	if _, err := execer.systemStatusData(); err == nil || !errors.Is(err, systemdErr) {
		t.Fatalf("systemStatusData systemd error = %v, want %v", err, systemdErr)
	}

	dockerErr := errors.New("docker failed")
	execer = &ttyExecer{
		s: newTestServer(t),
		systemdStatusesFunc: func() (map[string]svc.Status, error) {
			return map[string]svc.Status{}, nil
		},
		dockerComposeStatusesFunc: func() (map[string]svc.DockerComposeStatus, error) {
			return nil, dockerErr
		},
	}
	if _, err := execer.systemStatusData(); err == nil || !errors.Is(err, dockerErr) {
		t.Fatalf("systemStatusData docker error = %v, want %v", err, dockerErr)
	}
}

func TestServiceDataTypeOrDockerFallsBackForMissingService(t *testing.T) {
	execer := &ttyExecer{s: newTestServer(t)}
	if got := execer.serviceDataTypeOrDocker("missing"); got != ServiceDataTypeDocker {
		t.Fatalf("serviceDataTypeOrDocker = %s, want docker", got)
	}
}

func TestStatusCmdFuncWithEmptyDBRendersEmptyTable(t *testing.T) {
	var out bytes.Buffer
	execer := &ttyExecer{
		s:  newTestServer(t),
		sn: SystemService,
		rw: &out,
	}

	if err := execer.statusCmdFunc(cli.StatusFlags{}); err != nil {
		t.Fatalf("statusCmdFunc returned error: %v", err)
	}
	if got := strings.Fields(out.String()); !reflect.DeepEqual(got, []string{"SERVICE", "TYPE", "CONTAINER", "STATUS"}) {
		t.Fatalf("status output fields = %#v, want header only\n%s", got, out.String())
	}
}

func TestSingleServiceStatusDataForVMUsesVMSystemdUnit(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "devbox", db.ServiceTypeVM, nil)

	execer := &ttyExecer{
		s:  server,
		sn: "devbox",
		systemdStatusFunc: func(sn string) (svc.Status, error) {
			if sn != "yeet-vm-devbox.service" {
				t.Fatalf("systemd status service = %q, want yeet-vm-devbox.service", sn)
			}
			return svc.StatusRunning, nil
		},
	}

	status, render, err := execer.singleServiceStatusData()
	if err != nil {
		t.Fatalf("singleServiceStatusData: %v", err)
	}
	if !render {
		t.Fatal("render = false, want true")
	}
	if status.ServiceType != ServiceDataTypeVM {
		t.Fatalf("service type = %s, want vm", status.ServiceType)
	}
	want := []ComponentStatusData{{Name: "devbox", Status: ComponentStatusRunning}}
	if !reflect.DeepEqual(status.ComponentStatus, want) {
		t.Fatalf("component status = %#v, want %#v", status.ComponentStatus, want)
	}
}

func TestRenderServiceStatusesJSON(t *testing.T) {
	statuses := []ServiceStatusData{
		serviceStatusWithComponent("web", ServiceDataTypeDocker, "api", svc.StatusRunning),
	}
	var out bytes.Buffer
	if err := renderServiceStatuses(&out, "json", statuses); err != nil {
		t.Fatalf("render json: %v", err)
	}
	if got := out.String(); !strings.Contains(got, `"serviceName":"web"`) {
		t.Fatalf("json output = %q, want web service", got)
	}
}

func TestRenderServiceStatusesJSONPretty(t *testing.T) {
	statuses := []ServiceStatusData{
		serviceStatusWithComponent("web", ServiceDataTypeDocker, "api", svc.StatusRunning),
	}
	var out bytes.Buffer
	if err := renderServiceStatuses(&out, "json-pretty", statuses); err != nil {
		t.Fatalf("render json-pretty: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "\n  {") || !strings.Contains(got, `"serviceName": "web"`) {
		t.Fatalf("json-pretty output = %q, want indented web service", got)
	}
}

func TestRenderServiceStatusesJSONReturnsWriterError(t *testing.T) {
	writeErr := errors.New("json write failed")
	err := renderServiceStatuses(failingStatusWriter{err: writeErr}, "json", []ServiceStatusData{
		serviceStatusWithComponent("web", ServiceDataTypeDocker, "api", svc.StatusRunning),
	})
	if !errors.Is(err, writeErr) {
		t.Fatalf("render json error = %v, want %v", err, writeErr)
	}
}

func TestSortServiceStatusesSortsServicesAndComponents(t *testing.T) {
	statuses := []ServiceStatusData{
		{
			ServiceName: "web",
			ServiceType: ServiceDataTypeDocker,
			ComponentStatus: []ComponentStatusData{
				{Name: "worker", Status: ComponentStatusRunning},
				{Name: "api", Status: ComponentStatusStopped},
			},
		},
		{
			ServiceName:     "api",
			ServiceType:     ServiceDataTypeService,
			ComponentStatus: []ComponentStatusData{{Name: "api", Status: ComponentStatusRunning}},
		},
	}

	sortServiceStatuses(statuses)

	if statuses[0].ServiceName != "api" || statuses[1].ServiceName != "web" {
		t.Fatalf("service order = %#v", statuses)
	}
	if got := statuses[1].ComponentStatus[0].Name; got != "api" {
		t.Fatalf("first web component = %q, want api", got)
	}
}

func TestRemoveServiceWithoutRunnerCleansConfig(t *testing.T) {
	server := newTestServer(t)
	var out bytes.Buffer
	execer := &ttyExecer{
		s:  server,
		sn: "missing",
		rw: &out,
	}

	if err := execer.removeServiceWithoutRunner(cli.RemoveFlags{}, errNoServiceConfigured); err != nil {
		t.Fatalf("removeServiceWithoutRunner returned error: %v", err)
	}
	if got := out.String(); !strings.Contains(got, `service "missing" not found`) {
		t.Fatalf("output = %q, want not found message", got)
	}
}

func TestRemoveCmdFuncWithYesRemovesRunnerAndConfig(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "svc-remove", db.ServiceType("unknown"), db.ArtifactStore{})
	runner := &recordingServiceRunner{}
	var out bytes.Buffer
	execer := &ttyExecer{
		s:  server,
		sn: "svc-remove",
		rw: &out,
		serviceRunnerFn: func() (ServiceRunner, error) {
			return runner, nil
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{Yes: true}); err != nil {
		t.Fatalf("removeCmdFunc returned error: %v", err)
	}
	if !reflect.DeepEqual(runner.calls, []string{"remove"}) {
		t.Fatalf("runner calls = %#v, want remove", runner.calls)
	}
	if _, err := server.serviceView("svc-remove"); !errors.Is(err, errServiceNotFound) {
		t.Fatalf("serviceView error = %v, want service not found", err)
	}
	if got := out.String(); !strings.Contains(got, "warning:") {
		t.Fatalf("remove output = %q, want cleanup warning", got)
	}
}

func TestRemoveCmdFuncISODelegatesWithoutGenericRunnerRemoval(t *testing.T) {
	server := newTestServer(t)
	allocation := testISORuntimeAllocation("app", iso.StateReady)
	addTestServices(t, server, db.Service{Name: "app", ServiceType: db.ServiceTypeDockerCompose, ISO: allocation})
	runner := &recordingServiceRunner{}
	delegated := false
	execer := &ttyExecer{
		ctx: context.Background(), s: server, sn: "app", rw: &bytes.Buffer{},
		serviceRunnerFn: func() (ServiceRunner, error) { return runner, nil },
		removeServiceFunc: func(name string, opts RemoveOptions) (*RemoveReport, error) {
			delegated = true
			if name != "app" {
				t.Fatalf("removed service = %q, want app", name)
			}
			return &RemoveReport{}, nil
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if !delegated {
		t.Fatal("ISO removal did not delegate to authoritative server coordinator")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("ISO removal called generic runner before coordinator: %#v", runner.calls)
	}
}

func TestRemoveCmdFuncReturnsRunnerSetupError(t *testing.T) {
	runnerErr := errors.New("runner failed")
	execer := &ttyExecer{
		sn: "svc-remove",
		serviceRunnerFn: func() (ServiceRunner, error) {
			return nil, runnerErr
		},
	}

	err := execer.removeCmdFunc(cli.RemoveFlags{Yes: true})
	if err == nil || !errors.Is(err, runnerErr) {
		t.Fatalf("removeCmdFunc error = %v, want %v", err, runnerErr)
	}
}

func TestRemoveCmdFuncRemovesPartialUnknownService(t *testing.T) {
	server := newTestServer(t)
	if _, _, err := server.cfg.DB.MutateService("partial", func(_ *db.Data, s *db.Service) error {
		s.SvcNetwork = &db.SvcNetwork{IPv4: netipMustParseAddr(t, "192.168.100.10")}
		return nil
	}); err != nil {
		t.Fatalf("seed partial service: %v", err)
	}
	var out bytes.Buffer
	execer := &ttyExecer{s: server, sn: "partial", rw: &out}

	if err := execer.removeCmdFunc(cli.RemoveFlags{Yes: true}); err != nil {
		t.Fatalf("removeCmdFunc returned error: %v", err)
	}
	if _, err := server.serviceView("partial"); !errors.Is(err, errServiceNotFound) {
		t.Fatalf("serviceView error = %v, want service not found", err)
	}
	if !strings.Contains(out.String(), "warning:") {
		t.Fatalf("output = %q, want warning for partial service", out.String())
	}
}

func TestRemoveCmdFuncDeclineSkipsRunnerRemoval(t *testing.T) {
	runner := &recordingServiceRunner{}
	execer := &ttyExecer{
		sn: "svc-decline",
		rw: readWriter{Reader: strings.NewReader("n\n"), Writer: &bytes.Buffer{}},
		serviceRunnerFn: func() (ServiceRunner, error) {
			return runner, nil
		},
	}

	if err := execer.removeCmdFunc(cli.RemoveFlags{}); err != nil {
		t.Fatalf("removeCmdFunc returned error: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %#v, want none", runner.calls)
	}
}

func TestConfirmServiceRemovalCanDecline(t *testing.T) {
	var out bytes.Buffer
	execer := &ttyExecer{
		sn: "svc-decline",
		rw: readWriter{Reader: strings.NewReader("n\n"), Writer: &out},
	}

	ok, err := execer.confirmServiceRemoval(false)
	if err != nil {
		t.Fatalf("confirmServiceRemoval returned error: %v", err)
	}
	if ok {
		t.Fatal("confirmServiceRemoval = true, want false")
	}
	if got := out.String(); !strings.Contains(got, "Are you sure") {
		t.Fatalf("prompt output = %q, want confirmation prompt", got)
	}
}

func TestConfirmVMRemovalUsesVMLabel(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "devbox", db.ServiceTypeVM, nil)

	var out bytes.Buffer
	execer := &ttyExecer{
		s:  server,
		sn: "devbox",
		rw: readWriter{Reader: strings.NewReader("n\n"), Writer: &out},
	}

	ok, err := execer.confirmServiceRemoval(false)
	if err != nil {
		t.Fatalf("confirmServiceRemoval returned error: %v", err)
	}
	if ok {
		t.Fatal("confirmServiceRemoval = true, want false")
	}
	if got := out.String(); !strings.Contains(got, `remove VM "devbox"`) {
		t.Fatalf("prompt output = %q, want VM removal prompt", got)
	}
}

func TestConfirmRemoveDataUsesVMLabel(t *testing.T) {
	server := newTestServer(t)
	seedService(t, server, "devbox", db.ServiceTypeVM, nil)

	var out bytes.Buffer
	execer := &ttyExecer{
		s:  server,
		sn: "devbox",
		rw: readWriter{Reader: strings.NewReader("n\n"), Writer: &out},
	}

	flags, err := execer.confirmRemoveData(cli.RemoveFlags{})
	if err != nil {
		t.Fatalf("confirmRemoveData returned error: %v", err)
	}
	if flags.CleanData {
		t.Fatal("CleanData = true, want false")
	}
	if got := out.String(); !strings.Contains(got, `Delete all data for VM "devbox"?`) {
		t.Fatalf("prompt output = %q, want VM data prompt", got)
	}
}

func TestRemoveRunnerPrintsWarnings(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{name: "not installed", err: svc.ErrNotInstalled, want: "was not installed"},
		{name: "other error", err: errors.New("stop failed"), want: "failed to stop/remove"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &recordingServiceRunner{errs: map[string]error{"remove": tc.err}}
			var out bytes.Buffer
			execer := &ttyExecer{sn: "svc-remove", rw: &out}

			execer.removeRunner(runner)
			if got := out.String(); !strings.Contains(got, tc.want) {
				t.Fatalf("remove warning = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestServiceRunnerUsesOverride(t *testing.T) {
	runner := &recordingServiceRunner{}
	execer := &ttyExecer{
		serviceRunnerFn: func() (ServiceRunner, error) {
			return runner, nil
		},
	}

	got, err := execer.serviceRunner()
	if err != nil {
		t.Fatalf("serviceRunner returned error: %v", err)
	}
	if got != runner {
		t.Fatalf("serviceRunner = %T, want override runner", got)
	}
}

func TestServiceRunnerForTypeRejectsUnknownType(t *testing.T) {
	execer := &ttyExecer{}
	if _, err := execer.serviceRunnerForType(db.ServiceType("unknown")); err == nil || !strings.Contains(err.Error(), "unhandled service type") {
		t.Fatalf("serviceRunnerForType error = %v, want unhandled type", err)
	}
}

func canonicalLegacyRollbackArtifacts(
	t *testing.T,
	server *Server,
	serviceName string,
	generations ...int,
) (db.ArtifactStore, *int) {
	t.Helper()
	root := server.defaultServiceRootDir(serviceName)
	runDir := serviceRunDirForRoot(root)
	dataDir := serviceDataDirForRoot(root)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binaryRefs := make(map[db.ArtifactRef]string, len(generations)+1)
	unitRefs := make(map[db.ArtifactRef]string, len(generations)+1)
	writtenUnits := make(map[string]bool, len(generations))
	identity := db.ServiceIdentity{RequestedUser: "root", RequestedGroup: "root"}
	for _, generation := range generations {
		payload := filepath.Join(runDir, fmt.Sprintf("%s-%d", serviceName, generation))
		if err := os.WriteFile(payload, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		raw := "[Service]\nExecStart=" + payload + "\n"
		rendered, _, err := renderNativeSandboxUnitWithPlan(raw, nativeSandboxUnitRequest{
			CurrentPolicy: serviceSandboxPolicy{State: "legacy"},
			TargetPolicy:  serviceSandboxPolicy{State: "legacy"},
			Identity:      identity,
			Payload:       payload,
			DataDir:       dataDir,
			Resolver:      "/etc/resolv.conf",
			Hostname:      serviceName,
		}, nil)
		if err != nil {
			t.Fatalf("render generation %d rollback fixture: %v", generation, err)
		}
		unit := filepath.Join(runDir, fmt.Sprintf("%s-%d.service", serviceName, generation))
		if err := os.WriteFile(unit, []byte(rendered), 0o644); err != nil {
			t.Fatal(err)
		}
		ref := db.Gen(generation)
		binaryRefs[ref] = payload
		unitRefs[ref] = unit
		binaryRefs["latest"] = payload
		unitRefs["latest"] = unit
		writtenUnits[unit] = true
	}

	staticVerifications := new(int)
	previousVerify := verifyGeneratedSystemdUnitForSandboxMutation
	t.Cleanup(func() { verifyGeneratedSystemdUnitForSandboxMutation = previousVerify })
	verifyGeneratedSystemdUnitForSandboxMutation = func(_ context.Context, path string) error {
		if !writtenUnits[path] {
			t.Fatalf("verified unit = %q, want an exact rollback generation unit", path)
		}
		*staticVerifications++
		return nil
	}
	return db.ArtifactStore{
		db.ArtifactBinary:      {Refs: binaryRefs},
		db.ArtifactSystemdUnit: {Refs: unitRefs},
	}, staticVerifications
}

func seedService(t *testing.T, server *Server, name string, serviceType db.ServiceType, artifacts db.ArtifactStore) {
	t.Helper()
	if _, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, s *db.Service) error {
		s.ServiceType = serviceType
		s.Generation = 1
		s.LatestGeneration = 1
		s.Artifacts = artifacts
		return nil
	}); err != nil {
		t.Fatalf("seed service %q: %v", name, err)
	}
}

func fakeStatusSnapshotCommand(t *testing.T, output string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestTTYServiceStatusSnapshotFakeCommand", "--", output)
	cmd.Env = append(os.Environ(), "GO_WANT_STATUS_SNAPSHOT_HELPER=1")
	return cmd
}

func TestTTYServiceStatusSnapshotFakeCommand(t *testing.T) {
	if os.Getenv("GO_WANT_STATUS_SNAPSHOT_HELPER") != "1" {
		return
	}
	if len(os.Args) > 0 {
		fmt.Print(os.Args[len(os.Args)-1])
	}
	os.Exit(0)
}

type recordingServiceRunner struct {
	calls      []string
	errs       map[string]error
	logOptions *svc.LogOptions
	onCall     func(string)
}

func (r *recordingServiceRunner) SetNewCmd(func(string, ...string) *exec.Cmd) {}

func (r *recordingServiceRunner) Start() error {
	r.calls = append(r.calls, "start")
	if r.onCall != nil {
		r.onCall("start")
	}
	return r.errs["start"]
}

func (r *recordingServiceRunner) Stop() error {
	r.calls = append(r.calls, "stop")
	if r.onCall != nil {
		r.onCall("stop")
	}
	return r.errs["stop"]
}

func (r *recordingServiceRunner) Restart() error {
	r.calls = append(r.calls, "restart")
	if r.onCall != nil {
		r.onCall("restart")
	}
	return r.errs["restart"]
}

func (r *recordingServiceRunner) Logs(opts *svc.LogOptions) error {
	r.calls = append(r.calls, "logs")
	r.logOptions = opts
	return r.errs["logs"]
}

func (r *recordingServiceRunner) Remove() error {
	r.calls = append(r.calls, "remove")
	return r.errs["remove"]
}

func (r *recordingServiceRunner) Enable() error {
	r.calls = append(r.calls, "enable")
	return r.errs["enable"]
}

func (r *recordingServiceRunner) Disable() error {
	r.calls = append(r.calls, "disable")
	return r.errs["disable"]
}

type basicServiceRunner struct{}

func (basicServiceRunner) SetNewCmd(func(string, ...string) *exec.Cmd) {}
func (basicServiceRunner) Start() error                                { return nil }
func (basicServiceRunner) Stop() error                                 { return nil }
func (basicServiceRunner) Restart() error                              { return nil }
func (basicServiceRunner) Logs(*svc.LogOptions) error                  { return nil }
func (basicServiceRunner) Remove() error                               { return nil }
