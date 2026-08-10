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
	"io"
	"io/fs"
	"log"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/fileutil"
	"github.com/yeetrun/yeet/pkg/iso"
	"github.com/yeetrun/yeet/pkg/netns"
	"github.com/yeetrun/yeet/pkg/svc"
)

type recordingServiceNetworkMutationSteps struct {
	events   []string
	failures map[string]error
	target   *db.Service
}

type artifactCleanupRecordingNetworkMutationSteps struct {
	*recordingServiceNetworkMutationSteps
	discarded bool
	committed bool
}

type recordingRegularNetworkComposeStarter struct {
	events []string
}

type functionServiceNetworkMutationSteps struct {
	stage        func(context.Context) error
	stopPrevious func(context.Context) error
	activate     func(context.Context) error
	verify       func(context.Context) error
	commit       func(context.Context) error
	restore      func(context.Context) error
	failClosed   func(context.Context) error
}

func (s *functionServiceNetworkMutationSteps) Stage(ctx context.Context) error { return s.stage(ctx) }
func (s *functionServiceNetworkMutationSteps) StopPrevious(ctx context.Context) error {
	return s.stopPrevious(ctx)
}
func (s *functionServiceNetworkMutationSteps) Activate(ctx context.Context) error {
	return s.activate(ctx)
}
func (s *functionServiceNetworkMutationSteps) Verify(ctx context.Context) error { return s.verify(ctx) }
func (s *functionServiceNetworkMutationSteps) Commit(ctx context.Context) error { return s.commit(ctx) }
func (s *functionServiceNetworkMutationSteps) Restore(ctx context.Context) error {
	return s.restore(ctx)
}
func (s *functionServiceNetworkMutationSteps) FailClosed(ctx context.Context) error {
	return s.failClosed(ctx)
}

func (s *recordingRegularNetworkComposeStarter) ReconcileNetNS(context.Context) (bool, error) {
	s.events = append(s.events, "reconcile")
	return false, nil
}

func (s *recordingRegularNetworkComposeStarter) UpDetached(context.Context, bool) error {
	s.events = append(s.events, "up")
	return nil
}

func (s *artifactCleanupRecordingNetworkMutationSteps) DiscardStagedArtifacts() error {
	s.discarded = true
	return nil
}

func (s *artifactCleanupRecordingNetworkMutationSteps) CleanupCommittedArtifacts(context.Context) error {
	s.committed = true
	return nil
}

func (s *recordingServiceNetworkMutationSteps) run(name string) error {
	s.events = append(s.events, name)
	return s.failures[name]
}

func (s *recordingServiceNetworkMutationSteps) Stage(context.Context) error {
	return s.run("stage")
}

func (s *recordingServiceNetworkMutationSteps) StopPrevious(context.Context) error {
	return s.run("stop-previous")
}

func (s *recordingServiceNetworkMutationSteps) Activate(context.Context) error {
	return s.run("activate")
}

func (s *recordingServiceNetworkMutationSteps) Verify(context.Context) error {
	return s.run("verify")
}

func (s *recordingServiceNetworkMutationSteps) Commit(context.Context) error {
	return s.run("commit")
}

func (s *recordingServiceNetworkMutationSteps) Restore(context.Context) error {
	return s.run("restore")
}

func (s *recordingServiceNetworkMutationSteps) FailClosed(context.Context) error {
	return s.run("fail-closed")
}

func (s *recordingServiceNetworkMutationSteps) ResolverCanonicalTarget() *db.Service {
	return s.target
}

func TestRunServiceNetworkMutationOrdersSuccessfulTransaction(t *testing.T) {
	steps := &recordingServiceNetworkMutationSteps{}
	if err := runServiceNetworkMutation(context.Background(), steps); err != nil {
		t.Fatalf("runServiceNetworkMutation: %v", err)
	}
	want := []string{"stage", "stop-previous", "activate", "verify", "commit"}
	if !slices.Equal(steps.events, want) {
		t.Fatalf("events = %v, want %v", steps.events, want)
	}
}

func TestRunServiceNetworkMutationStageFailureLeavesPreviousRuntimeUntouched(t *testing.T) {
	stageErr := errors.New("stage failed")
	steps := &recordingServiceNetworkMutationSteps{failures: map[string]error{"stage": stageErr}}
	err := runServiceNetworkMutation(context.Background(), steps)
	if !errors.Is(err, stageErr) {
		t.Fatalf("error = %v, want stage failure", err)
	}
	if want := []string{"stage"}; !slices.Equal(steps.events, want) {
		t.Fatalf("events = %v, want %v", steps.events, want)
	}
}

func TestRunServiceNetworkMutationRestoresEveryPostStageFailure(t *testing.T) {
	for _, failedStep := range []string{"stop-previous", "activate", "verify", "commit"} {
		t.Run(failedStep, func(t *testing.T) {
			stepErr := errors.New(failedStep + " failed")
			steps := &recordingServiceNetworkMutationSteps{failures: map[string]error{failedStep: stepErr}}
			err := runServiceNetworkMutation(context.Background(), steps)
			if !errors.Is(err, stepErr) {
				t.Fatalf("error = %v, want %v", err, stepErr)
			}
			if len(steps.events) == 0 || steps.events[len(steps.events)-1] != "restore" {
				t.Fatalf("events = %v, want restore last", steps.events)
			}
			if slices.Contains(steps.events, "fail-closed") {
				t.Fatalf("events = %v, fail-closed must not run after a successful restore", steps.events)
			}
		})
	}
}

func TestRunServiceNetworkMutationRestoreFailureFailsClosedAndJoinsErrors(t *testing.T) {
	activateErr := errors.New("activate failed")
	restoreErr := errors.New("restore failed")
	failClosedErr := errors.New("fail-closed stop failed")
	steps := &recordingServiceNetworkMutationSteps{failures: map[string]error{
		"activate": activateErr, "restore": restoreErr, "fail-closed": failClosedErr,
	}}
	err := runServiceNetworkMutation(context.Background(), steps)
	for _, want := range []error{activateErr, restoreErr, failClosedErr} {
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want joined %v", err, want)
		}
	}
	wantEvents := []string{"stage", "stop-previous", "activate", "restore", "fail-closed"}
	if !slices.Equal(steps.events, wantEvents) {
		t.Fatalf("events = %v, want %v", steps.events, wantEvents)
	}
}

type cancelledServiceNetworkRecoverySteps struct {
	cancel             context.CancelFunc
	restoreErr         error
	restoreContextErr  error
	failClosedCtxErr   error
	composeProjectStop bool
}

type cancelledConcreteNetworkRecoverySteps struct {
	cancel                context.CancelFunc
	mutation              *regularServiceNetworkMutation
	restoreContextLive    bool
	restoreHasDeadline    bool
	failClosedContextLive bool
	failClosedHasDeadline bool
}

func (s *cancelledConcreteNetworkRecoverySteps) Stage(context.Context) error        { return nil }
func (s *cancelledConcreteNetworkRecoverySteps) StopPrevious(context.Context) error { return nil }
func (s *cancelledConcreteNetworkRecoverySteps) Activate(context.Context) error {
	s.cancel()
	return errors.New("activation interrupted")
}
func (s *cancelledConcreteNetworkRecoverySteps) Verify(context.Context) error { return nil }
func (s *cancelledConcreteNetworkRecoverySteps) Commit(context.Context) error { return nil }
func (s *cancelledConcreteNetworkRecoverySteps) Restore(ctx context.Context) error {
	s.restoreContextLive = ctx.Err() == nil
	_, s.restoreHasDeadline = ctx.Deadline()
	return s.mutation.Restore(ctx)
}
func (s *cancelledConcreteNetworkRecoverySteps) FailClosed(ctx context.Context) error {
	s.failClosedContextLive = ctx.Err() == nil
	_, s.failClosedHasDeadline = ctx.Deadline()
	return s.mutation.FailClosed(ctx)
}

func (s *cancelledServiceNetworkRecoverySteps) Stage(context.Context) error        { return nil }
func (s *cancelledServiceNetworkRecoverySteps) StopPrevious(context.Context) error { return nil }
func (s *cancelledServiceNetworkRecoverySteps) Activate(context.Context) error {
	s.cancel()
	return errors.New("activation interrupted")
}
func (s *cancelledServiceNetworkRecoverySteps) Verify(context.Context) error { return nil }
func (s *cancelledServiceNetworkRecoverySteps) Commit(context.Context) error { return nil }
func (s *cancelledServiceNetworkRecoverySteps) Restore(ctx context.Context) error {
	s.restoreContextErr = ctx.Err()
	return s.restoreErr
}
func (s *cancelledServiceNetworkRecoverySteps) FailClosed(ctx context.Context) error {
	s.failClosedCtxErr = ctx.Err()
	if ctx.Err() == nil {
		s.composeProjectStop = true
	}
	return nil
}

func TestRunServiceNetworkMutationCancellationUsesIndependentRecoveryContexts(t *testing.T) {
	for _, tt := range []struct {
		name        string
		restoreErr  error
		wantStopped bool
	}{
		{name: "restore succeeds"},
		{name: "restore fails and compose project is failed closed", restoreErr: errors.New("restore failed"), wantStopped: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			steps := &cancelledServiceNetworkRecoverySteps{cancel: cancel, restoreErr: tt.restoreErr}
			err := runServiceNetworkMutation(ctx, steps)
			if err == nil || !strings.Contains(err.Error(), "activation interrupted") {
				t.Fatalf("runServiceNetworkMutation error = %v", err)
			}
			if steps.restoreContextErr != nil {
				t.Fatalf("restore context error = %v, want cancellation-independent recovery", steps.restoreContextErr)
			}
			if tt.restoreErr != nil && steps.failClosedCtxErr != nil {
				t.Fatalf("fail-closed context error = %v, want fresh cancellation-independent cleanup", steps.failClosedCtxErr)
			}
			if steps.composeProjectStop != tt.wantStopped {
				t.Fatalf("compose project stopped = %t, want %t", steps.composeProjectStop, tt.wantStopped)
			}
		})
	}
}

func TestRunServiceNetworkMutationCancellationBoundsConcreteRecoveryOperations(t *testing.T) {
	oldTimeout := serviceNetworkRecoveryTimeout
	oldSystemctl := runRegularNetworkSystemctlForRuntime
	serviceNetworkRecoveryTimeout = 50 * time.Millisecond
	t.Cleanup(func() {
		serviceNetworkRecoveryTimeout = oldTimeout
		runRegularNetworkSystemctlForRuntime = oldSystemctl
	})

	for _, serviceType := range []db.ServiceType{db.ServiceTypeSystemd, db.ServiceTypeDockerCompose} {
		t.Run(string(serviceType), func(t *testing.T) {
			server := newTestServer(t)
			root := server.defaultServiceRootDir("api")
			for _, dir := range []string{serviceBinDirForRoot(root), serviceDataDirForRoot(root)} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			fakeBin := t.TempDir()
			for _, command := range []string{"docker", "systemctl"} {
				script := "#!/bin/sh\nexit 0\n"
				if (serviceType == db.ServiceTypeSystemd && command == "systemctl") || (serviceType == db.ServiceTypeDockerCompose && command == "docker") {
					script = "#!/bin/sh\nexec sleep 60\n"
				}
				if err := os.WriteFile(filepath.Join(fakeBin, command), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
			runRegularNetworkSystemctlForRuntime = func(ctx context.Context, args ...string) ([]byte, error) {
				return runISOSystemctlForRuntime(ctx, args...)
			}

			previous := &db.Service{Name: "api", ServiceType: serviceType, ServiceRoot: root}
			if serviceType == db.ServiceTypeSystemd {
				unit := filepath.Join(serviceBinDirForRoot(root), "api.service")
				if err := os.WriteFile(unit, []byte("[Service]\nExecStart=/bin/true\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				previous.Artifacts = db.ArtifactStore{db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(0): unit}}}
			} else {
				compose := filepath.Join(serviceBinDirForRoot(root), "compose.yml")
				if err := os.WriteFile(compose, []byte("services: {}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				previous.Artifacts = db.ArtifactStore{db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{db.Gen(0): compose}}}
			}
			if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": previous}}); err != nil {
				t.Fatal(err)
			}
			mutation := &regularServiceNetworkMutation{
				server: server, plan: &serviceNetworkMutationPlan{name: "api", previous: previous, previousRunning: true}, target: previous.Clone(),
			}
			ctx, cancel := context.WithCancel(context.Background())
			steps := &cancelledConcreteNetworkRecoverySteps{cancel: cancel, mutation: mutation}
			started := time.Now()
			err := runServiceNetworkMutation(ctx, steps)
			if err == nil || !strings.Contains(err.Error(), "activation interrupted") {
				t.Fatalf("runServiceNetworkMutation error = %v", err)
			}
			if !steps.restoreContextLive || !steps.restoreHasDeadline || !steps.failClosedContextLive || !steps.failClosedHasDeadline {
				t.Fatalf("bounded recovery contexts = restore(live=%t deadline=%t) fail-closed(live=%t deadline=%t)", steps.restoreContextLive, steps.restoreHasDeadline, steps.failClosedContextLive, steps.failClosedHasDeadline)
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("concrete %s recovery ignored deadline: %s", serviceType, elapsed)
			}
		})
	}
}

func TestServiceSetNetworkPlanIsSideEffectFreeAndCarriesRuntimeIntentAndTransientAuth(t *testing.T) {
	server := newTestServer(t)
	previous := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, Generation: 7, LatestGeneration: 9,
		Network: &db.ServiceNetworkConfig{Modes: []string{"host"}},
		Artifacts: db.ArtifactStore{db.ArtifactBinary: {Refs: map[db.ArtifactRef]string{
			db.Gen(7): "/srv/api/bin/api-7", "latest": "/srv/api/bin/api-9",
		}}},
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": previous}}); err != nil {
		t.Fatal(err)
	}
	before, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	oldRunning := isServiceRunningForNetworkMutation
	isServiceRunningForNetworkMutation = func(*Server, string) (bool, error) { return true, nil }
	t.Cleanup(func() { isServiceRunningForNetworkMutation = oldRunning })

	plan, err := server.planServiceNetworkMutation(context.Background(), "api", cli.ServiceSetFlags{
		Net: "ts", NetSet: true, TsTags: []string{"tag:app"}, TsTagsSet: true,
		TsAuthKey: "tskey-auth-secret", TsAuthKeySet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.AsStruct(), after.AsStruct()) {
		t.Fatalf("planning mutated DB: before %#v after %#v", before.AsStruct(), after.AsStruct())
	}
	if plan.previous.Generation != 7 || plan.previous.LatestGeneration != 9 || !plan.previousRunning {
		t.Fatalf("plan snapshot = %#v", plan)
	}
	if !reflect.DeepEqual(plan.currentDesired.Modes, []string{"host"}) || !reflect.DeepEqual(plan.desired.Modes, []string{"ts"}) {
		t.Fatalf("desired snapshot = current %#v target %#v", plan.currentDesired, plan.desired)
	}
	if plan.network.Tailscale.AuthKey != "tskey-auth-secret" {
		t.Fatalf("transient auth key = %q", plan.network.Tailscale.AuthKey)
	}
	if raw := asJSON(plan.desired); strings.Contains(raw, "tskey-auth-secret") {
		t.Fatalf("desired state persisted transient auth: %s", raw)
	}
}

func TestRegularNetworkMutationReplacesRuntimePointersWithoutAdvancingPayload(t *testing.T) {
	ip := netip.MustParseAddr("192.168.100.17")
	previous := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, Generation: 4, LatestGeneration: 6,
		Network:    &db.ServiceNetworkConfig{Modes: []string{"svc", "ts"}, TSTags: []string{"tag:old"}},
		SvcNetwork: &db.SvcNetwork{IPv4: ip},
		Macvlan:    &db.MacvlanNetwork{Interface: "ymv-old", Parent: "eno1", Mac: "02:00:00:00:00:17"},
		TSNet:      &db.TailscaleNetwork{Interface: "yts-old", StableID: "stable-old", Version: "1.100.0", Tags: []string{"tag:old"}},
		Artifacts: db.ArtifactStore{
			db.ArtifactBinary:       {Refs: map[db.ArtifactRef]string{db.Gen(4): "/payload/api-4", "latest": "/payload/api-6"}},
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(4): "/old/ns.service"}},
			db.ArtifactTSService:    {Refs: map[db.ArtifactRef]string{db.Gen(4): "/old/ts.service"}},
		},
	}
	target := regularNetworkReplacement(previous, db.ServiceNetworkConfig{Modes: []string{"host"}}, nil, nil, nil, nil)
	if target.Generation != 4 || target.LatestGeneration != 6 {
		t.Fatalf("generation/latest = %d/%d, want 4/6", target.Generation, target.LatestGeneration)
	}
	if target.Artifacts[db.ArtifactBinary].Refs[db.Gen(4)] != "/payload/api-4" {
		t.Fatalf("payload artifact changed: %#v", target.Artifacts[db.ArtifactBinary])
	}
	if target.SvcNetwork != nil || target.Macvlan != nil || target.TSNet != nil {
		t.Fatalf("stale runtime pointers survived host transition: %#v", target)
	}
	for name := range isoNetworkArtifactNames {
		if _, ok := target.Artifacts[name]; ok {
			t.Fatalf("stale network artifact %s survived: %#v", name, target.Artifacts[name])
		}
	}
}

func TestRegularNetworkMutationPreservesCompatibleNetworkIdentities(t *testing.T) {
	ip := netip.MustParseAddr("192.168.100.23")
	previous := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd,
		SvcNetwork: &db.SvcNetwork{IPv4: ip},
		Macvlan:    &db.MacvlanNetwork{Interface: "ymv-stable", Parent: "eno1", VLAN: 7, Mac: "02:00:00:00:00:23"},
		TSNet:      &db.TailscaleNetwork{Interface: "yts-stable", StableID: "node-stable", Version: "1.100.0", ExitNode: "old", Tags: []string{"tag:old"}},
	}
	svcNet, macvlan, tsNet, err := regularNetworkAllocations(previous, db.ServiceNetworkConfig{
		Modes: []string{"lan", "svc", "ts"}, TSVersion: "1.101.284", TSExitNode: "new", TSTags: []string{"tag:new"},
		MacvlanParent: "eno1", MacvlanVLAN: 7,
	}, (&db.Data{Services: map[string]*db.Service{"api": previous}}).View())
	if err != nil {
		t.Fatal(err)
	}
	if svcNet == nil || svcNet.IPv4 != ip {
		t.Fatalf("svc identity = %#v, want %s", svcNet, ip)
	}
	if macvlan == nil || macvlan.Interface != "ymv-stable" || macvlan.Mac != "02:00:00:00:00:23" {
		t.Fatalf("macvlan identity = %#v", macvlan)
	}
	if tsNet == nil || tsNet.Interface != "yts-stable" || tsNet.StableID != "node-stable" || tsNet.Version != "1.101.284" || tsNet.ExitNode != "new" || !reflect.DeepEqual(tsNet.Tags, []string{"tag:new"}) {
		t.Fatalf("tailscale identity/settings = %#v", tsNet)
	}
}

func TestRegularNetworkMutationTransitionMatrixClearsAndSetsExactRuntimePointers(t *testing.T) {
	svcNet := &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.40")}
	macvlan := &db.MacvlanNetwork{Interface: "ymv-40", Parent: "eno1", Mac: "02:00:00:00:00:40"}
	tsNet := &db.TailscaleNetwork{Interface: "yts-40", StableID: "node-40", Version: "1.101.284", Tags: []string{"tag:app"}}
	tests := []struct {
		name                string
		from, to            []string
		svc, lan, tailscale bool
	}{
		{name: "host-to-svc", from: []string{"host"}, to: []string{"svc"}, svc: true},
		{name: "svc-to-host", from: []string{"svc"}, to: []string{"host"}},
		{name: "host-to-lan", from: []string{"host"}, to: []string{"lan"}, lan: true},
		{name: "lan-to-host", from: []string{"lan"}, to: []string{"host"}},
		{name: "host-to-ts", from: []string{"host"}, to: []string{"ts"}, tailscale: true},
		{name: "ts-to-host", from: []string{"ts"}, to: []string{"host"}},
		{name: "svc-ts-to-host", from: []string{"svc", "ts"}, to: []string{"host"}},
		{name: "host-to-svc-ts", from: []string{"host"}, to: []string{"svc", "ts"}, svc: true, tailscale: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			previous := &db.Service{
				Name: "api", ServiceType: db.ServiceTypeSystemd, Network: &db.ServiceNetworkConfig{Modes: tt.from},
				SvcNetwork: svcNet, Macvlan: macvlan, TSNet: tsNet,
			}
			var targetSvc *db.SvcNetwork
			var targetLAN *db.MacvlanNetwork
			var targetTS *db.TailscaleNetwork
			if tt.svc {
				targetSvc = svcNet
			}
			if tt.lan {
				targetLAN = macvlan
			}
			if tt.tailscale {
				targetTS = tsNet
			}
			target := regularNetworkReplacement(previous, db.ServiceNetworkConfig{Modes: tt.to}, targetSvc, targetLAN, targetTS, nil)
			if (target.SvcNetwork != nil) != tt.svc || (target.Macvlan != nil) != tt.lan || (target.TSNet != nil) != tt.tailscale {
				t.Fatalf("runtime pointers = svc:%t lan:%t ts:%t, want svc:%t lan:%t ts:%t", target.SvcNetwork != nil, target.Macvlan != nil, target.TSNet != nil, tt.svc, tt.lan, tt.tailscale)
			}
		})
	}
}

func TestRegularNetworkCommitAcceptsPersistenceEquivalentPreviousRecord(t *testing.T) {
	server := newTestServer(t)
	previous := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeDockerCompose, Generation: 1, LatestGeneration: 1,
		Network: &db.ServiceNetworkConfig{Modes: []string{"host"}, TSTags: []string{}},
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": previous}}); err != nil {
		t.Fatal(err)
	}
	view, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	planned := view.Services().Get("api")
	if !planned.Valid() {
		t.Fatal("service api is absent after setup")
	}
	target := planned.AsStruct()
	target.Network = &db.ServiceNetworkConfig{Modes: []string{"svc"}}
	target.SvcNetwork = &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.23")}
	mutation := &regularServiceNetworkMutation{
		server: server,
		plan:   &serviceNetworkMutationPlan{name: "api", previous: planned.AsStruct()},
		target: target,
	}

	if err := mutation.Commit(context.Background()); err != nil {
		t.Fatalf("Commit after persistence round trip: %v", err)
	}
	got, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	service := got.Services().Get("api")
	if !service.Valid() || !reflect.DeepEqual(service.AsStruct(), target) {
		t.Fatalf("committed service = %#v, want %#v", service.AsStruct(), target)
	}
}

func TestRegularNetworkRestoreAcceptsPersistenceEquivalentPreviousRecord(t *testing.T) {
	server := newTestServer(t)
	previous := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeDockerCompose, Generation: 1, LatestGeneration: 1,
		Network: &db.ServiceNetworkConfig{Modes: []string{"host"}, TSTags: []string{}},
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": previous}}); err != nil {
		t.Fatal(err)
	}
	view, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	planned := view.Services().Get("api")
	if !planned.Valid() {
		t.Fatal("service api is absent after setup")
	}
	target := planned.AsStruct()
	target.Network = &db.ServiceNetworkConfig{Modes: []string{"svc"}}
	target.SvcNetwork = &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.23")}
	mutation := &regularServiceNetworkMutation{
		server: server,
		plan:   &serviceNetworkMutationPlan{name: "api", previous: planned.AsStruct()},
		target: target,
	}

	owned, err := mutation.claimReplacementBeforeRestore()
	if err != nil || !owned {
		t.Fatalf("claim after persistence round trip = owned %t, error %v", owned, err)
	}
}

func TestRegularNetworkMutationFailClosedStopsAllUnitsBeforeVerifying(t *testing.T) {
	oldSystemctl, oldActive := runRegularNetworkSystemctlForRuntime, inspectRegularNetworkUnitActive
	t.Cleanup(func() {
		runRegularNetworkSystemctlForRuntime, inspectRegularNetworkUnitActive = oldSystemctl, oldActive
	})
	previous := &db.Service{Name: "api", Generation: 1, Artifacts: db.ArtifactStore{
		db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/timer"}},
		db.ArtifactNetNSService:     {Refs: map[db.ArtifactRef]string{db.Gen(1): "/ns"}},
	}}
	current := &db.Service{Name: "api", Generation: 1, Artifacts: db.ArtifactStore{
		db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/unit"}},
		db.ArtifactTSService:   {Refs: map[db.ArtifactRef]string{db.Gen(1): "/ts"}},
	}}
	var events []string
	runRegularNetworkSystemctlForRuntime = func(_ context.Context, args ...string) ([]byte, error) {
		events = append(events, "stop:"+args[len(args)-1])
		if args[len(args)-1] == "api.service" {
			return nil, errors.New("primary stop failed")
		}
		return nil, nil
	}
	inspectRegularNetworkUnitActive = func(_ context.Context, unit string) (bool, error) {
		events = append(events, "verify:"+unit)
		return unit == "yeet-api-ts.service", nil
	}
	err := stopRegularNetworkMutationUnitsFailClosed(context.Background(), current, previous, "api")
	if err == nil || !strings.Contains(err.Error(), "primary stop failed") || !strings.Contains(err.Error(), "still active") {
		t.Fatalf("fail-closed error = %v", err)
	}
	firstVerify := slices.IndexFunc(events, func(event string) bool { return strings.HasPrefix(event, "verify:") })
	if firstVerify < 0 {
		t.Fatalf("events = %v, missing verification", events)
	}
	for _, event := range events[firstVerify:] {
		if strings.HasPrefix(event, "stop:") {
			t.Fatalf("stop occurred after verification began: %v", events)
		}
	}
	wantStops := []string{"api.timer", "api.service", "yeet-api-ns.service", "yeet-api-ts.service"}
	for _, unit := range wantStops {
		if !slices.Contains(events[:firstVerify], "stop:"+unit) {
			t.Fatalf("events = %v, missing unconditional stop for %s", events, unit)
		}
	}
}

func TestRegularNetworkSystemdRuntimeHelpersPreserveExactState(t *testing.T) {
	oldRun, oldActive := runRegularNetworkSystemctlForRuntime, inspectRegularNetworkUnitActive
	t.Cleanup(func() {
		runRegularNetworkSystemctlForRuntime, inspectRegularNetworkUnitActive = oldRun, oldActive
	})
	record := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, Generation: 1, Artifacts: db.ArtifactStore{
		db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/timer"}},
		db.ArtifactNetNSService:     {Refs: map[db.ArtifactRef]string{db.Gen(1): "/ns"}},
	}}
	stop, _ := serviceIdentityRuntimeUnits(record, record.Name)
	desired := make([]serviceIdentityRuntimeUnitState, len(stop))
	active := map[string]bool{}
	for index, unit := range stop {
		desired[index] = serviceIdentityRuntimeUnitState{Unit: unit, Active: index%2 == 0}
		active[unit] = !desired[index].Active
	}
	runRegularNetworkSystemctlForRuntime = func(_ context.Context, args ...string) ([]byte, error) {
		unit := args[len(args)-1]
		switch args[0] {
		case "start":
			active[unit] = true
		case "stop":
			active[unit] = false
		}
		return nil, nil
	}
	inspectRegularNetworkUnitActive = func(_ context.Context, unit string) (bool, error) { return active[unit], nil }
	if err := reconcileRegularNetworkSystemdRuntime(context.Background(), record, nil, desired); err != nil {
		t.Fatal(err)
	}
	for _, state := range desired {
		if active[state.Unit] != state.Active {
			t.Fatalf("unit %s active = %t, want %t", state.Unit, active[state.Unit], state.Active)
		}
	}

	for unit := range active {
		active[unit] = false
	}
	allActive := make([]serviceIdentityRuntimeUnitState, len(stop))
	for index, unit := range stop {
		allActive[index] = serviceIdentityRuntimeUnitState{Unit: unit, Active: true}
	}
	if err := reconcileRegularNetworkSystemdRuntime(context.Background(), record, nil, allActive); err != nil {
		t.Fatal(err)
	}
	for _, unit := range stop {
		if !active[unit] {
			t.Fatalf("unit %s was not started", unit)
		}
	}
}

func TestRegularNetworkSystemdActivationPreservesExactTimerAndServiceStates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timer   bool
		desired []serviceIdentityRuntimeUnitState
	}{
		{name: "active timer with active job", timer: true, desired: []serviceIdentityRuntimeUnitState{{Unit: "api.timer", Active: true}, {Unit: "api.service", Active: true}}},
		{name: "active timer with inactive job", timer: true, desired: []serviceIdentityRuntimeUnitState{{Unit: "api.timer", Active: true}, {Unit: "api.service", Active: false}}},
		{name: "active non-timer service", desired: []serviceIdentityRuntimeUnitState{{Unit: "api.service", Active: true}}},
		{name: "inactive non-timer service", desired: []serviceIdentityRuntimeUnitState{{Unit: "api.service", Active: false}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldRun, oldActive := runRegularNetworkSystemctlForRuntime, inspectRegularNetworkUnitActive
			t.Cleanup(func() {
				runRegularNetworkSystemctlForRuntime, inspectRegularNetworkUnitActive = oldRun, oldActive
			})
			record := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, Generation: 1, Artifacts: db.ArtifactStore{
				db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/unit"}},
			}}
			if tc.timer {
				record.Artifacts[db.ArtifactSystemdTimerFile] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(1): "/timer"}}
			}
			plan := &serviceNetworkMutationPlan{
				name: "api", previous: record.Clone(), previousRunning: tc.desired[0].Active,
				previousRuntime:    slices.Clone(tc.desired),
				previousEnablement: []serviceIdentityUnitEnablement{{Unit: tc.desired[0].Unit, Enabled: true}},
			}
			want := regularNetworkTargetRuntimeIntent(plan, record)
			if !reflect.DeepEqual(want, tc.desired) {
				t.Fatalf("target runtime intent = %#v, want %#v", want, tc.desired)
			}
			active := map[string]bool{}
			var started []string
			runRegularNetworkSystemctlForRuntime = func(_ context.Context, args ...string) ([]byte, error) {
				unit := args[len(args)-1]
				if args[0] == "start" {
					started = append(started, unit)
					active[unit] = true
				}
				if args[0] == "stop" {
					active[unit] = false
				}
				return nil, nil
			}
			inspectRegularNetworkUnitActive = func(_ context.Context, unit string) (bool, error) { return active[unit], nil }
			if err := reconcileRegularNetworkSystemdRuntime(context.Background(), record, nil, want); err != nil {
				t.Fatal(err)
			}
			if err := verifyRegularNetworkSystemdRuntime(context.Background(), nil, record, record, want); err != nil {
				t.Fatalf("verify exact runtime state: %v", err)
			}
			for _, state := range tc.desired {
				if active[state.Unit] != state.Active {
					t.Fatalf("unit %s active = %t, want %t; started=%v", state.Unit, active[state.Unit], state.Active, started)
				}
			}
			if tc.timer && tc.desired[1].Active != slices.Contains(started, "api.service") {
				t.Fatalf("timer job start calls = %v, desired service active=%t", started, tc.desired[1].Active)
			}
		})
	}
}

func TestRegularNetworkSystemdRestoreWaitsForTailscaleBeforeWorkload(t *testing.T) {
	oldRun, oldActive := runRegularNetworkSystemctlForRuntime, inspectRegularNetworkUnitActive
	oldStartTS, oldVerifyTS := startTailscaleSystemdSidecar, verifyTailscaleSystemdSidecar
	t.Cleanup(func() {
		runRegularNetworkSystemctlForRuntime, inspectRegularNetworkUnitActive = oldRun, oldActive
		startTailscaleSystemdSidecar, verifyTailscaleSystemdSidecar = oldStartTS, oldVerifyTS
	})
	record := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, Generation: 1, Artifacts: db.ArtifactStore{
		db.ArtifactSystemdUnit:      {Refs: map[db.ArtifactRef]string{db.Gen(1): "/unit"}},
		db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/timer"}},
		db.ArtifactNetNSService:     {Refs: map[db.ArtifactRef]string{db.Gen(1): "/ns"}},
		db.ArtifactTSService:        {Refs: map[db.ArtifactRef]string{db.Gen(1): "/ts"}},
	}}
	desired := []serviceIdentityRuntimeUnitState{
		{Unit: "api.timer", Active: true}, {Unit: "api.service", Active: true},
		{Unit: "yeet-api-ts.service", Active: true}, {Unit: "yeet-api-ns.service", Active: true},
	}
	active := map[string]bool{}
	var events []string
	runRegularNetworkSystemctlForRuntime = func(_ context.Context, args ...string) ([]byte, error) {
		unit := args[len(args)-1]
		if args[0] == "start" {
			events = append(events, "start "+unit)
			active[unit] = true
		}
		if args[0] == "stop" {
			events = append(events, "stop "+unit)
			active[unit] = false
		}
		return nil, nil
	}
	inspectRegularNetworkUnitActive = func(_ context.Context, unit string) (bool, error) { return active[unit], nil }
	startTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error {
		events = append(events, "start yeet-api-ts.service")
		active["yeet-api-ts.service"] = true
		return nil
	}
	verifyTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error {
		events = append(events, "verify tailscale")
		return nil
	}
	if err := reconcileRegularNetworkSystemdRuntime(context.Background(), record, &svc.SystemdService{}, desired); err != nil {
		t.Fatal(err)
	}
	wantEvents := []string{
		"start yeet-api-ns.service", "start yeet-api-ts.service", "verify tailscale", "start api.timer", "start api.service",
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("restore events = %v, want %v", events, wantEvents)
	}
}

func TestRunServiceNetworkMutationTailscaleRestoreReadinessFailureFailsClosed(t *testing.T) {
	oldRun, oldActive := runRegularNetworkSystemctlForRuntime, inspectRegularNetworkUnitActive
	oldStartTS, oldVerifyTS := startTailscaleSystemdSidecar, verifyTailscaleSystemdSidecar
	t.Cleanup(func() {
		runRegularNetworkSystemctlForRuntime, inspectRegularNetworkUnitActive = oldRun, oldActive
		startTailscaleSystemdSidecar, verifyTailscaleSystemdSidecar = oldStartTS, oldVerifyTS
	})
	record := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, Generation: 1, Artifacts: db.ArtifactStore{
		db.ArtifactSystemdUnit:  {Refs: map[db.ArtifactRef]string{db.Gen(1): "/unit"}},
		db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/ns"}},
		db.ArtifactTSService:    {Refs: map[db.ArtifactRef]string{db.Gen(1): "/ts"}},
	}}
	desired := []serviceIdentityRuntimeUnitState{
		{Unit: "api.service", Active: true}, {Unit: "yeet-api-ts.service", Active: true}, {Unit: "yeet-api-ns.service", Active: true},
	}
	active := map[string]bool{}
	var stopped []string
	runRegularNetworkSystemctlForRuntime = func(_ context.Context, args ...string) ([]byte, error) {
		unit := args[len(args)-1]
		switch args[0] {
		case "start":
			active[unit] = true
		case "stop":
			active[unit] = false
			stopped = append(stopped, unit)
		}
		return nil, nil
	}
	inspectRegularNetworkUnitActive = func(_ context.Context, unit string) (bool, error) { return active[unit], nil }
	startTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error {
		active["yeet-api-ts.service"] = true
		return nil
	}
	readinessErr := errors.New("previous tailscale sidecar is not ready")
	verifyTailscaleSystemdSidecar = func(ctx context.Context, _ *svc.SystemdService) error {
		if ctx.Err() != nil {
			t.Fatalf("restore readiness context = %v, want live recovery context", ctx.Err())
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("restore readiness context has no recovery deadline")
		}
		return readinessErr
	}
	ctx, cancel := context.WithCancel(context.Background())
	failClosed := false
	steps := &functionServiceNetworkMutationSteps{
		stage: func(context.Context) error { return nil }, stopPrevious: func(context.Context) error { return nil },
		activate: func(context.Context) error { cancel(); return errors.New("replacement activation failed") },
		verify:   func(context.Context) error { return nil }, commit: func(context.Context) error { return nil },
		restore: func(recovery context.Context) error {
			return reconcileRegularNetworkSystemdRuntime(recovery, record, &svc.SystemdService{}, desired)
		},
		failClosed: func(recovery context.Context) error {
			failClosed = true
			return stopRegularNetworkMutationUnitsFailClosed(recovery, record, record, record.Name)
		},
	}
	err := runServiceNetworkMutation(ctx, steps)
	if !errors.Is(err, readinessErr) {
		t.Fatalf("runServiceNetworkMutation error = %v, want readiness failure", err)
	}
	if !failClosed {
		t.Fatal("readiness failure did not invoke fail-closed")
	}
	for _, unit := range []string{"api.service", "yeet-api-ts.service", "yeet-api-ns.service"} {
		if !slices.Contains(stopped, unit) || active[unit] {
			t.Fatalf("fail-closed state for %s: stopped=%v active=%v", unit, stopped, active)
		}
	}
}

func TestVerifyRegularNetworkStaleUnitsRequiresEveryOldUnitInactive(t *testing.T) {
	oldActive := inspectRegularNetworkUnitActive
	t.Cleanup(func() { inspectRegularNetworkUnitActive = oldActive })
	previous := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, Generation: 1, Artifacts: db.ArtifactStore{
		db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/unit"}},
		db.ArtifactTSService:   {Refs: map[db.ArtifactRef]string{db.Gen(1): "/ts"}},
	}}
	active := map[string]bool{"api.service": true}
	inspectRegularNetworkUnitActive = func(_ context.Context, unit string) (bool, error) { return active[unit], nil }
	if err := verifyRegularNetworkStaleUnits(context.Background(), previous, "api", []string{"api.service"}); err != nil {
		t.Fatal(err)
	}
	active["yeet-api-ts.service"] = true
	err := verifyRegularNetworkStaleUnits(context.Background(), previous, "api", []string{"api.service"})
	if err == nil || !strings.Contains(err.Error(), "stale unit") {
		t.Fatalf("verifyRegularNetworkStaleUnits error = %v, want active stale unit", err)
	}
}

func TestReloadAndEnableRegularNetworkUnitsEnablesEveryStagedUnit(t *testing.T) {
	oldRun := runRegularNetworkSystemctlForRuntime
	t.Cleanup(func() {
		runRegularNetworkSystemctlForRuntime = oldRun
	})
	var commands []string
	runRegularNetworkSystemctlForRuntime = func(_ context.Context, args ...string) ([]byte, error) {
		commands = append(commands, strings.Join(args, " "))
		return nil, nil
	}
	if err := reloadAndEnableRegularNetworkUnits(context.Background(), []string{"yeet-api-ns.service", "yeet-api-ts.service"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"daemon-reload", "enable yeet-api-ns.service", "enable yeet-api-ts.service"} {
		if !slices.Contains(commands, want) {
			t.Fatalf("systemctl commands = %v, missing %q", commands, want)
		}
	}
}

func TestRegularNetworkComposeWaitsForTailscaleReadinessBeforeContainers(t *testing.T) {
	oldRun, oldActive, oldVerify := runRegularNetworkSystemctlForRuntime, inspectRegularNetworkUnitActive, verifyTailscaleSystemdSidecar
	t.Cleanup(func() {
		runRegularNetworkSystemctlForRuntime, inspectRegularNetworkUnitActive, verifyTailscaleSystemdSidecar = oldRun, oldActive, oldVerify
	})
	events := []string{}
	runRegularNetworkSystemctlForRuntime = func(_ context.Context, args ...string) ([]byte, error) {
		events = append(events, strings.Join(args, " "))
		return nil, nil
	}
	inspectRegularNetworkUnitActive = func(context.Context, string) (bool, error) { return true, nil }
	readinessErr := errors.New("tailscale sidecar has no address")
	verifyTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error {
		events = append(events, "verify-tailscale")
		return readinessErr
	}
	record := &db.Service{Name: "api", ServiceType: db.ServiceTypeDockerCompose, Generation: 1, Artifacts: db.ArtifactStore{
		db.ArtifactTSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/ts"}},
	}}
	compose := &recordingRegularNetworkComposeStarter{}
	err := startRegularNetworkComposeIfNeeded(context.Background(), compose, record, &svc.SystemdService{}, true)
	if !errors.Is(err, readinessErr) {
		t.Fatalf("startRegularNetworkComposeIfNeeded error = %v, want readiness error", err)
	}
	if len(compose.events) != 0 {
		t.Fatalf("Compose started before Tailscale readiness: %v", compose.events)
	}
	if !slices.Contains(events, "verify-tailscale") {
		t.Fatalf("events = %v, missing Tailscale readiness verification", events)
	}
}

func TestVerifyRegularNetworkComposeRuntimeRejectsUnreadyTailscaleAndStaleUnits(t *testing.T) {
	oldActive, oldVerify := inspectRegularNetworkUnitActive, verifyTailscaleSystemdSidecar
	t.Cleanup(func() {
		inspectRegularNetworkUnitActive, verifyTailscaleSystemdSidecar = oldActive, oldVerify
	})
	server := newTestServer(t)
	previous := &db.Service{Name: "api", ServiceType: db.ServiceTypeDockerCompose, Generation: 1, Artifacts: db.ArtifactStore{
		db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/old-ns"}},
	}}
	target := &db.Service{Name: "api", ServiceType: db.ServiceTypeDockerCompose, Generation: 1, Artifacts: db.ArtifactStore{
		db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/new-ns"}},
		db.ArtifactTSService:    {Refs: map[db.ArtifactRef]string{db.Gen(1): "/new-ts"}},
	}}
	active := map[string]bool{"yeet-api-ns.service": true, "yeet-api-ts.service": true}
	inspectRegularNetworkUnitActive = func(_ context.Context, unit string) (bool, error) { return active[unit], nil }
	readinessErr := errors.New("tailscale resolver mount missing")
	verifyTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error { return readinessErr }
	if err := verifyRegularNetworkComposeRuntime(context.Background(), server, previous, target, true); !errors.Is(err, readinessErr) {
		t.Fatalf("verifyRegularNetworkComposeRuntime error = %v, want readiness error", err)
	}
	verifyTailscaleSystemdSidecar = func(context.Context, *svc.SystemdService) error { return nil }
	active["api.service"] = true
	if err := verifyRegularNetworkComposeRuntime(context.Background(), server, previous, target, true); err == nil || !strings.Contains(err.Error(), "stale unit") {
		t.Fatalf("verifyRegularNetworkComposeRuntime error = %v, want stale-unit error", err)
	}
}

func TestRegularNetworkComposeRestoreHonorsRecoveryDeadline(t *testing.T) {
	server := newTestServer(t)
	root := server.defaultServiceRootDir("api")
	for _, dir := range []string{serviceBinDirForRoot(root), serviceDataDirForRoot(root)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	composePath := filepath.Join(serviceBinDirForRoot(root), "compose.yml")
	if err := os.WriteFile(composePath, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := &db.Service{Name: "api", ServiceType: db.ServiceTypeDockerCompose, Artifacts: db.ArtifactStore{
		db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{db.Gen(0): composePath}},
	}}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": previous}}); err != nil {
		t.Fatal(err)
	}
	fakeBin := t.TempDir()
	docker := filepath.Join(fakeBin, "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	mutation := &regularServiceNetworkMutation{
		server: server,
		plan:   &serviceNetworkMutationPlan{name: "api", previous: previous, previousRunning: true},
		target: previous.Clone(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := mutation.restoreCompose(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("restoreCompose error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded Compose restore took %s", elapsed)
	}
}

func TestRegularNetworkMutationFailClosedDiscardsStagedArtifacts(t *testing.T) {
	oldSystemctl, oldActive := runRegularNetworkSystemctlForRuntime, inspectRegularNetworkUnitActive
	t.Cleanup(func() {
		runRegularNetworkSystemctlForRuntime, inspectRegularNetworkUnitActive = oldSystemctl, oldActive
	})
	runRegularNetworkSystemctlForRuntime = func(context.Context, ...string) ([]byte, error) { return nil, nil }
	inspectRegularNetworkUnitActive = func(context.Context, string) (bool, error) { return false, nil }

	server := newTestServer(t)
	root := server.defaultServiceRootDir("api")
	if err := os.MkdirAll(serviceBinDirForRoot(root), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": previous.Clone()}}); err != nil {
		t.Fatal(err)
	}
	txn, err := beginRegularNetworkArtifactTransaction(root, previous)
	if err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(serviceBinDirForRoot(root), "api-network-staged.service")
	if err := os.WriteFile(staged, []byte("staged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := txn.registerStagedPath(staged); err != nil {
		t.Fatal(err)
	}
	mutation := &regularServiceNetworkMutation{
		server: server,
		plan:   &serviceNetworkMutationPlan{name: "api", previous: previous, artifactTxn: txn},
		target: previous.Clone(),
	}
	if err := mutation.FailClosed(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged artifact remains after fail-closed cleanup: %v", err)
	}
}

func TestRegularNetworkMutationPreservesSystemdEnablementIntent(t *testing.T) {
	previous := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, Generation: 1, Artifacts: db.ArtifactStore{
		db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/old/api.timer"}},
		db.ArtifactNetNSService:     {Refs: map[db.ArtifactRef]string{db.Gen(1): "/old/ns.service"}},
	}}
	target := previous.Clone()
	delete(target.Artifacts, db.ArtifactNetNSService)
	target.Artifacts[db.ArtifactTSService] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(1): "/new/ts.service"}}
	plan := &serviceNetworkMutationPlan{
		name: "api", previous: previous,
		previousEnablement: []serviceIdentityUnitEnablement{
			{Unit: "api.timer", Enabled: false},
			{Unit: "yeet-api-ns.service", Enabled: true},
		},
	}
	mutation := &regularServiceNetworkMutation{plan: plan, target: target}
	want := []serviceIdentityUnitEnablement{
		{Unit: "api.timer", Enabled: false, TargetEnabled: false},
		{Unit: "yeet-api-ns.service", Enabled: true, TargetEnabled: false},
		{Unit: "yeet-api-ts.service", Enabled: false, TargetEnabled: false},
	}
	if got := mutation.targetUnitEnablement(target); !reflect.DeepEqual(got, want) {
		t.Fatalf("target enablement = %#v, want %#v", got, want)
	}

	oldInspect := inspectRegularNetworkUnitEnabled
	oldEnable, oldDisable := enableRegularNetworkUnit, disableRegularNetworkUnit
	t.Cleanup(func() {
		inspectRegularNetworkUnitEnabled = oldInspect
		enableRegularNetworkUnit, disableRegularNetworkUnit = oldEnable, oldDisable
	})
	enabled := map[string]bool{
		"api.timer": true, "yeet-api-ns.service": true, "yeet-api-ts.service": true,
	}
	inspectRegularNetworkUnitEnabled = func(_ context.Context, unit string) (bool, error) { return enabled[unit], nil }
	enableRegularNetworkUnit = func(_ context.Context, unit string) error { enabled[unit] = true; return nil }
	disableRegularNetworkUnit = func(_ context.Context, unit string) error { enabled[unit] = false; return nil }
	if err := mutation.reconcileUnitEnablement(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	for unit, got := range enabled {
		if got {
			t.Fatalf("unit %s enabled = true, want false after preserving disabled timer intent", unit)
		}
	}
	if err := mutation.verifyUnitEnablement(context.Background(), target); err != nil {
		t.Fatalf("verifyUnitEnablement: %v", err)
	}
}

func TestRegularNetworkMutationNewSidecarsFollowEnabledPrimaryIntent(t *testing.T) {
	previous := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, Generation: 1}
	target := previous.Clone()
	target.Artifacts = db.ArtifactStore{
		db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/new/api.service"}},
		db.ArtifactTSService:   {Refs: map[db.ArtifactRef]string{db.Gen(1): "/new/ts.service"}},
	}
	mutation := &regularServiceNetworkMutation{plan: &serviceNetworkMutationPlan{
		name: "api", previous: previous,
		previousEnablement: []serviceIdentityUnitEnablement{{Unit: "api.service", Enabled: true}},
	}, target: target}
	want := []serviceIdentityUnitEnablement{
		{Unit: "api.service", Enabled: true, TargetEnabled: true},
		{Unit: "yeet-api-ts.service", TargetEnabled: true},
	}
	if got := mutation.targetUnitEnablement(target); !reflect.DeepEqual(got, want) {
		t.Fatalf("target enablement = %#v, want %#v", got, want)
	}
}

func TestRegularNetworkIdentityMigrationPreservesDisabledUnitEnablement(t *testing.T) {
	previous := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, Generation: 1, Artifacts: db.ArtifactStore{
		db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{db.Gen(1): "/old/api.timer"}},
	}}
	target := previous.Clone()
	target.Artifacts[db.ArtifactTSService] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(1): "/new/ts.service"}}
	plan := &serviceNetworkMutationPlan{
		name: "api", previous: previous,
		previousEnablement: []serviceIdentityUnitEnablement{{Unit: "api.timer", Enabled: false}},
	}
	wantDisabled := []serviceIdentityUnitEnablement{
		{Unit: "api.timer", TargetEnabled: false},
		{Unit: "yeet-api-ts.service", TargetEnabled: false},
	}
	if got := regularNetworkTargetUnitEnablement(plan, target, target); !reflect.DeepEqual(got, wantDisabled) {
		t.Fatalf("disabled timer migration enablement = %#v, want %#v", got, wantDisabled)
	}
	plan.previousEnablement[0].Enabled = true
	wantEnabled := []serviceIdentityUnitEnablement{
		{Unit: "api.timer", Enabled: true, TargetEnabled: true},
		{Unit: "yeet-api-ts.service", TargetEnabled: true},
	}
	if got := regularNetworkTargetUnitEnablement(plan, target, target); !reflect.DeepEqual(got, wantEnabled) {
		t.Fatalf("enabled timer migration enablement = %#v, want %#v", got, wantEnabled)
	}
}

func TestRegularNetworkMutationExplicitHostStagesFreshUnitWithoutNamespaceDirectives(t *testing.T) {
	stubServiceNetworkStaticVerification(t)
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "api")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	oldUnit := filepath.Join(serviceBinDirForRoot(root), "api-old.service")
	oldRaw := "[Unit]\nRequires=database.service yeet-api-ns.service yeet-api-ts.service\nAfter=database.service yeet-api-ns.service yeet-api-ts.service\n\n[Service]\nExecStart=/srv/api/bin/api-7 --serve\nNetworkNamespacePath=/var/run/netns/yeet-api-ns\nBindReadOnlyPaths=/etc/netns/yeet-api-ns/resolv.conf:/etc/resolv.conf\nPrivateMounts=yes\n\n[Install]\nWantedBy=multi-user.target\n"
	if err := os.WriteFile(oldUnit, []byte(oldRaw), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root, Generation: 7, LatestGeneration: 7,
		Network:    &db.ServiceNetworkConfig{Modes: []string{"svc", "ts"}, TSTags: []string{"tag:app"}},
		SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.31")},
		TSNet:      &db.TailscaleNetwork{Interface: "yts-old", Version: "1.101.284", Tags: []string{"tag:app"}},
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit:  {Refs: map[db.ArtifactRef]string{db.Gen(2): "/old/api-2.service", db.Gen(7): oldUnit, "latest": oldUnit}},
			db.ArtifactNetNSService: {Refs: map[db.ArtifactRef]string{db.Gen(7): "/old/ns"}},
			db.ArtifactNetNSResolv:  {Refs: map[db.ArtifactRef]string{db.Gen(7): "/old/resolv"}},
			db.ArtifactTSService:    {Refs: map[db.ArtifactRef]string{db.Gen(7): "/old/ts"}},
		},
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": previous}}); err != nil {
		t.Fatal(err)
	}
	plan := &serviceNetworkMutationPlan{
		name: "api", previous: previous.Clone(), desired: db.ServiceNetworkConfig{Modes: []string{"host"}},
		network: NetworkOpts{Interfaces: "host", Modes: []string{"host"}},
	}
	target, err := server.stageRegularServiceNetworkReplacement(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	unit, ok := target.Artifacts.Gen(db.ArtifactSystemdUnit, 7)
	if !ok || unit == oldUnit {
		t.Fatalf("replacement unit = %q, want fresh path distinct from %q", unit, oldUnit)
	}
	if historical, ok := target.Artifacts.Gen(db.ArtifactSystemdUnit, 2); !ok || historical != "/old/api-2.service" {
		t.Fatalf("historical systemd artifact was replaced: %q %t", historical, ok)
	}
	raw, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, stale := range []string{"NetworkNamespacePath=", "/etc/netns/yeet-api-ns/resolv.conf", "yeet-api-ns.service", "yeet-api-ts.service"} {
		if strings.Contains(got, stale) {
			t.Fatalf("fresh host unit retained %q:\n%s", stale, got)
		}
	}
	for _, want := range []string{"BindReadOnlyPaths=/etc/resolv.conf:/etc/resolv.conf\n", "PrivateMounts=yes\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("fresh host unit lost canonical direct resolver ownership %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "Requires=database.service") || !strings.Contains(got, "ExecStart=/srv/api/bin/api-7 --serve") {
		t.Fatalf("fresh host unit lost non-network directives:\n%s", got)
	}
}

func TestRenderRegularNetworkSystemdUnitPreservesSandboxResolverOwnership(t *testing.T) {
	oldEnsure := ensureBubblewrapForServiceSandboxMutation
	oldValidate := validateServiceSandboxPolicyForMutation
	t.Cleanup(func() {
		ensureBubblewrapForServiceSandboxMutation = oldEnsure
		validateServiceSandboxPolicyForMutation = oldValidate
	})
	ensureBubblewrapForServiceSandboxMutation = func(context.Context) error { return nil }
	validateServiceSandboxPolicyForMutation = func(req serviceSandboxPlanRequest, _ bool) (serviceSandboxPolicy, error) {
		return req.Policy, nil
	}
	for _, tt := range []struct {
		name   string
		policy serviceSandboxPolicy
	}{
		{name: "sandbox on", policy: serviceSandboxPolicy{State: "on"}},
		{name: "sandbox off", policy: serviceSandboxPolicy{State: "off"}},
		{name: "legacy", policy: serviceSandboxPolicy{State: "legacy"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			root := server.defaultServiceRootDir("api")
			dataDir := serviceDataDirForRoot(root)
			binDir := serviceRunDirForRoot(root)
			for _, dir := range []string{dataDir, binDir} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			payload := filepath.Join(binDir, "api-7")
			oldResolver := filepath.Join(binDir, "resolv-old.conf")
			newResolver := filepath.Join(binDir, "resolv-new.conf")
			for path, content := range map[string]string{payload: "payload", oldResolver: "nameserver 1.1.1.1\n", newResolver: "nameserver 2.2.2.2\n"} {
				if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			argv := payload + " --serve"
			workingDirectory := dataDir
			if tt.policy.State == "on" {
				argv = bubblewrapPath + " --uid 1000 --gid 1000 --ro-bind " + oldResolver + " /etc/resolv.conf --chdir " + dataDir + " -- " + payload + " --serve"
				workingDirectory = "/"
			}
			raw := "[Unit]\nRequires=database.service yeet-api-ns.service\nAfter=database.service yeet-api-ns.service\n" +
				"[Service]\nExecStart=" + argv + "\nWorkingDirectory=" + workingDirectory + "\n" +
				"Environment=HOME=" + dataDir + " USER=1000 LOGNAME=1000 SHELL=/bin/sh\n" +
				"NetworkNamespacePath=/var/run/netns/old\nBindReadOnlyPaths=" + oldResolver + ":/etc/resolv.conf\nPrivateMounts=yes\n"
			previous := &db.Service{
				Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root, Generation: 7,
				Identity: &db.ServiceIdentity{UID: 1000, GID: 1000},
				Artifacts: db.ArtifactStore{
					db.ArtifactBinary:      {Refs: map[db.ArtifactRef]string{db.Gen(7): payload}},
					db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(7): filepath.Join(binDir, "api.service")}},
					db.ArtifactNetNSResolv: {Refs: map[db.ArtifactRef]string{db.Gen(7): oldResolver}},
				},
			}
			target := previous.Clone()
			target.Artifacts[db.ArtifactNetNSResolv].Refs[db.Gen(7)] = newResolver
			if tt.policy.State == "legacy" {
				previous.Sandbox, target.Sandbox = nil, nil
			} else {
				stored := serviceSandboxPolicyToDB(tt.policy)
				previous.Sandbox = &db.ServiceSandboxStore{Refs: map[db.ArtifactRef]*db.ServiceSandboxPolicy{db.Gen(7): stored.Clone()}}
				target.Sandbox = &db.ServiceSandboxStore{Refs: map[db.ArtifactRef]*db.ServiceSandboxPolicy{db.Gen(7): stored.Clone()}}
			}
			got, _, err := renderRegularNetworkSystemdUnit(context.Background(), raw, previous, target, root, &networkConfig{
				NetNS: "new", Deps: []string{"yeet-api-ns.service"}, ResolvConf: newResolver,
			}, true)
			if err != nil {
				t.Fatalf("render network unit: %v", err)
			}
			if !strings.Contains(got, "NetworkNamespacePath=/var/run/netns/new\n") {
				t.Fatalf("network unit did not select target namespace:\n%s", got)
			}
			systemdResolver := "BindReadOnlyPaths=" + newResolver + ":/etc/resolv.conf\n"
			if tt.policy.State == "on" {
				if strings.Contains(got, "BindReadOnlyPaths=") || strings.Contains(got, "PrivateMounts=yes") {
					t.Fatalf("sandbox-on unit retained systemd resolver ownership:\n%s", got)
				}
				argv := serviceIdentityExecStartArgv(t, got)
				if !slicesContainAdjacent(argv, "--ro-bind", newResolver) {
					t.Fatalf("sandbox-on argv did not select target resolver: %#v", argv)
				}
				return
			}
			if !strings.Contains(got, systemdResolver) || !strings.Contains(got, "PrivateMounts=yes\n") {
				t.Fatalf("direct unit lost target systemd resolver ownership:\n%s", got)
			}
		})
	}
}

func TestRegularNetworkMutationTailscaleStagesEveryArtifactFresh(t *testing.T) {
	stubServiceNetworkStaticVerification(t)
	server := newTestServer(t)
	root := server.defaultServiceRootDir("api")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tailscale"), 0o755); err != nil {
		t.Fatal(err)
	}
	version := "1.92.3"
	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tsdDir, "tailscaled-"+version), []byte("tailscaled"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldUnit := filepath.Join(serviceBinDirForRoot(root), "api-old.service")
	if err := os.WriteFile(oldUnit, []byte("[Unit]\n\n[Service]\nExecStart=/srv/api/bin/api\n\n[Install]\nWantedBy=multi-user.target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldArtifacts := map[db.ArtifactName]string{
		db.ArtifactNetNSEnv:     filepath.Join(serviceBinDirForRoot(root), fileutil.ApplyVersion("netns.env")),
		db.ArtifactNetNSService: filepath.Join(serviceBinDirForRoot(root), fileutil.ApplyVersion("yeet-api-ns.service")),
		db.ArtifactNetNSResolv:  filepath.Join(serviceBinDirForRoot(root), fileutil.ApplyVersion("resolv.conf")),
		db.ArtifactTSEnv:        filepath.Join(root, "tailscale", fileutil.ApplyVersion("tailscaled.env")),
		db.ArtifactTSService:    filepath.Join(serviceBinDirForRoot(root), fileutil.ApplyVersion("yeet-api-ts.service")),
		db.ArtifactTSConfig:     filepath.Join(root, "tailscale", fileutil.ApplyVersion("tailscaled.json")),
	}
	for _, path := range oldArtifacts {
		if err := os.WriteFile(path, []byte("old-live-artifact"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	previous := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root, Generation: 2,
		Network: &db.ServiceNetworkConfig{Modes: []string{"ts"}, TSVersion: version, TSTags: []string{"tag:old"}},
		TSNet:   &db.TailscaleNetwork{Interface: "yts-stable", Version: version, Tags: []string{"tag:old"}},
		Artifacts: db.ArtifactStore{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(2): oldUnit, "latest": oldUnit}},
		},
	}
	for name, path := range oldArtifacts {
		previous.Artifacts[name] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(2): path, "latest": path}}
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": previous}}); err != nil {
		t.Fatal(err)
	}
	plan := &serviceNetworkMutationPlan{
		name: "api", previous: previous.Clone(),
		desired: db.ServiceNetworkConfig{Modes: []string{"ts"}, TSVersion: version, TSExitNode: "node", TSTags: []string{"tag:new"}},
		network: NetworkOpts{Interfaces: "ts", Modes: []string{"ts"}, Tailscale: TailscaleOpts{
			Version: version, ExitNode: "node", Tags: []string{"tag:new"}, AuthKey: "tskey-auth-secret",
		}},
	}
	target, err := server.stageRegularServiceNetworkReplacement(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plan.artifactTxn.rollback(server) })
	for name, oldPath := range oldArtifacts {
		newPath, ok := target.Artifacts.Gen(name, target.Generation)
		if !ok || newPath == oldPath {
			t.Fatalf("%s replacement path = %q, want fresh path distinct from %q", name, newPath, oldPath)
		}
		if raw, err := os.ReadFile(oldPath); err != nil || string(raw) != "old-live-artifact" {
			t.Fatalf("live %s artifact changed during staging: %q, %v", name, raw, err)
		}
	}
	config, _ := target.Artifacts.Gen(db.ArtifactTSConfig, target.Generation)
	info, err := os.Stat(config)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("staged auth config mode = %o, want 600", info.Mode().Perm())
	}
}

func TestServiceSetNetworkRunsDedicatedTransaction(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": {
		Name: "api", ServiceType: db.ServiceTypeSystemd,
		Network: &db.ServiceNetworkConfig{Modes: []string{"host"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	oldRunning := isServiceRunningForNetworkMutation
	oldSteps := newRegularServiceNetworkMutationSteps
	t.Cleanup(func() {
		isServiceRunningForNetworkMutation = oldRunning
		newRegularServiceNetworkMutationSteps = oldSteps
	})
	isServiceRunningForNetworkMutation = func(*Server, string) (bool, error) { return false, nil }
	recorded := &artifactCleanupRecordingNetworkMutationSteps{recordingServiceNetworkMutationSteps: &recordingServiceNetworkMutationSteps{}}
	newRegularServiceNetworkMutationSteps = func(*Server, *serviceNetworkMutationPlan) serviceNetworkMutationSteps {
		return recorded
	}
	err := server.updateServiceNetworkLocked(context.Background(), "api", cli.ServiceSetFlags{Net: "svc", NetSet: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"stage", "stop-previous", "activate", "verify", "commit"}
	if !slices.Equal(recorded.events, want) {
		t.Fatalf("events = %v, want %v", recorded.events, want)
	}
	if !recorded.committed {
		t.Fatal("successful network transaction did not run committed artifact cleanup")
	}
}

func TestServiceSetRegularTailscaleMutationUsesResolverGuardAndCanonicalReadiness(t *testing.T) {
	server := newTestServer(t)
	previous := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, Network: &db.ServiceNetworkConfig{Modes: []string{"host"}},
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": previous}}); err != nil {
		t.Fatal(err)
	}
	oldRunning := isServiceRunningForNetworkMutation
	oldSteps := newRegularServiceNetworkMutationSteps
	oldGuard := withRegularNetworkResolverMutationGuard
	oldReadiness := checkRegularNetworkResolverCanonicalReady
	t.Cleanup(func() {
		isServiceRunningForNetworkMutation = oldRunning
		newRegularServiceNetworkMutationSteps = oldSteps
		withRegularNetworkResolverMutationGuard = oldGuard
		checkRegularNetworkResolverCanonicalReady = oldReadiness
	})
	isServiceRunningForNetworkMutation = func(*Server, string) (bool, error) { return false, nil }
	target := previous.Clone()
	target.Network = &db.ServiceNetworkConfig{Modes: []string{"ts"}, TSTags: []string{"tag:app"}}
	target.TSNet = &db.TailscaleNetwork{Interface: "yts-ready", Tags: []string{"tag:app"}}
	target.Artifacts = db.ArtifactStore{
		db.ArtifactTSService: {Refs: map[db.ArtifactRef]string{db.Gen(0): "/staged/ts.service"}},
	}
	recorded := &artifactCleanupRecordingNetworkMutationSteps{recordingServiceNetworkMutationSteps: &recordingServiceNetworkMutationSteps{target: target}}
	newRegularServiceNetworkMutationSteps = func(*Server, *serviceNetworkMutationPlan) serviceNetworkMutationSteps { return recorded }
	guarded := false
	withRegularNetworkResolverMutationGuard = func(_ *Server, run func() error) error {
		guarded = true
		return run()
	}
	readinessErr := errors.New("canonical resolver target is not ready")
	checkRegularNetworkResolverCanonicalReady = func(_ context.Context, _ *Server, got db.Service) error {
		if got.TSNet == nil || got.TSNet.Interface != "yts-ready" {
			t.Fatalf("canonical readiness target = %#v", got)
		}
		return readinessErr
	}
	err := server.updateServiceNetworkLocked(context.Background(), "api", cli.ServiceSetFlags{
		Net: "ts", NetSet: true, TsTags: []string{"tag:app"}, TsTagsSet: true,
	}, io.Discard)
	if !errors.Is(err, readinessErr) {
		t.Fatalf("updateServiceNetworkLocked error = %v, want readiness error", err)
	}
	if !guarded {
		t.Fatal("Tailscale mutation did not enter resolver mutation guard")
	}
	if want := []string{"stage"}; !reflect.DeepEqual(recorded.events, want) {
		t.Fatalf("events = %v, want readiness failure before stop", recorded.events)
	}
	if !recorded.discarded {
		t.Fatal("resolver readiness failure did not discard staged network artifacts")
	}
}

func TestServiceSetRegularTailscaleMutationHonorsResolverRecoveryBlock(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": {
		Name: "api", ServiceType: db.ServiceTypeSystemd,
		Network: &db.ServiceNetworkConfig{Modes: []string{"ts"}, TSTags: []string{"tag:app"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	oldRunning := isServiceRunningForNetworkMutation
	oldSteps := newRegularServiceNetworkMutationSteps
	oldGuard := withRegularNetworkResolverMutationGuard
	t.Cleanup(func() {
		isServiceRunningForNetworkMutation = oldRunning
		newRegularServiceNetworkMutationSteps = oldSteps
		withRegularNetworkResolverMutationGuard = oldGuard
	})
	isServiceRunningForNetworkMutation = func(*Server, string) (bool, error) { return false, nil }
	recorded := &recordingServiceNetworkMutationSteps{}
	newRegularServiceNetworkMutationSteps = func(*Server, *serviceNetworkMutationPlan) serviceNetworkMutationSteps { return recorded }
	blocked := errors.New("resolver recovery blocked")
	withRegularNetworkResolverMutationGuard = func(*Server, func() error) error { return blocked }
	err := server.updateServiceNetworkLocked(context.Background(), "api", cli.ServiceSetFlags{TsExit: "node", TsExitSet: true}, io.Discard)
	if !errors.Is(err, blocked) {
		t.Fatalf("updateServiceNetworkLocked error = %v, want recovery block", err)
	}
	if len(recorded.events) != 0 {
		t.Fatalf("blocked resolver mutation ran transaction steps: %v", recorded.events)
	}
}

func TestRegularNetworkSvcAllocationSerializesThroughCommitAcrossServices(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{
		"api":    {Name: "api", ServiceType: db.ServiceTypeSystemd, Network: &db.ServiceNetworkConfig{Modes: []string{"host"}}},
		"worker": {Name: "worker", ServiceType: db.ServiceTypeSystemd, Network: &db.ServiceNetworkConfig{Modes: []string{"host"}}},
	}}); err != nil {
		t.Fatal(err)
	}

	plans := map[string]*serviceNetworkMutationPlan{
		"api":    {name: "api", desired: db.ServiceNetworkConfig{Modes: []string{"svc"}}},
		"worker": {name: "worker", desired: db.ServiceNetworkConfig{Modes: []string{"svc"}}},
	}
	firstAllocated := make(chan struct{})
	releaseFirst := make(chan struct{})
	type result struct {
		name string
		ip   netip.Addr
		err  error
	}
	results := make(chan result, 2)
	mutate := func(name string, first bool) {
		err := server.withRegularServiceNetworkAllocationLock(plans[name], func() error {
			dv, err := server.cfg.DB.Get()
			if err != nil {
				return err
			}
			network, err := regularSvcNetworkAllocation(nil, plans[name].desired, dv)
			if err != nil {
				return err
			}
			if first {
				close(firstAllocated)
				<-releaseFirst
			}
			_, err = server.cfg.DB.MutateData(func(data *db.Data) error {
				data.Services[name].SvcNetwork = network
				return nil
			})
			if err == nil {
				results <- result{name: name, ip: network.IPv4}
			}
			return err
		})
		if err != nil {
			results <- result{name: name, err: err}
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		mutate("api", true)
	}()
	<-firstAllocated
	go func() {
		defer wg.Done()
		mutate("worker", false)
	}()
	close(releaseFirst)
	wg.Wait()
	close(results)

	allocated := map[string]netip.Addr{}
	for got := range results {
		if got.err != nil {
			t.Fatalf("mutation for %s: %v", got.name, got.err)
		}
		allocated[got.name] = got.ip
	}
	if len(allocated) != 2 {
		t.Fatalf("allocations = %v, want both services", allocated)
	}
	if allocated["api"] == allocated["worker"] {
		t.Fatalf("concurrent service mutations allocated duplicate address %s", allocated["api"])
	}
}

func TestRegularNetworkSvcAllocationLockCoversInitialAndMutationPaths(t *testing.T) {
	for _, tc := range []struct {
		name       string
		secondKind string
	}{
		{name: "initial and mutation", secondKind: "mutation"},
		{name: "initial and initial", secondKind: "initial"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)
			services := map[string]*db.Service{}
			if tc.secondKind == "mutation" {
				services["api"] = &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, Network: &db.ServiceNetworkConfig{Modes: []string{"host"}}}
			}
			if err := server.cfg.DB.Set(&db.Data{Services: services}); err != nil {
				t.Fatal(err)
			}
			first := &FileInstaller{s: server, cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "worker"}, Network: NetworkOpts{Interfaces: "svc", Modes: []string{"svc"}}}}
			second := &FileInstaller{s: server, cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "jobs"}, Network: NetworkOpts{Interfaces: "svc", Modes: []string{"svc"}}}}
			mutation := &serviceNetworkMutationPlan{name: "api", desired: db.ServiceNetworkConfig{Modes: []string{"svc"}}}
			firstAllocated := make(chan struct{})
			releaseFirst := make(chan struct{})
			secondAttempted := make(chan struct{})
			secondEntered := make(chan struct{})
			type result struct {
				name string
				ip   netip.Addr
				err  error
			}
			results := make(chan result, 2)
			allocate := func(name string, create bool) (netip.Addr, error) {
				data, err := server.cfg.DB.Get()
				if err != nil {
					return netip.Addr{}, err
				}
				network, err := regularSvcNetworkAllocation(nil, db.ServiceNetworkConfig{Modes: []string{"svc"}}, data)
				if err != nil {
					return netip.Addr{}, err
				}
				_, err = server.cfg.DB.MutateData(func(data *db.Data) error {
					if create {
						data.Services[name] = &db.Service{Name: name, ServiceType: db.ServiceTypeSystemd, Network: &db.ServiceNetworkConfig{Modes: []string{"svc"}}, SvcNetwork: network}
					} else {
						data.Services[name].SvcNetwork = network
					}
					return nil
				})
				return network.IPv4, err
			}

			go func() {
				err := first.withRegularServiceNetworkAllocationLock(func() error {
					ip, err := allocate("worker", true)
					if err == nil {
						results <- result{name: "worker", ip: ip}
					}
					close(firstAllocated)
					<-releaseFirst
					return err
				})
				if err != nil {
					results <- result{name: "worker", err: err}
				}
			}()
			<-firstAllocated
			go func() {
				close(secondAttempted)
				var err error
				run := func() error {
					close(secondEntered)
					name, create := "api", false
					if tc.secondKind == "initial" {
						name, create = "jobs", true
					}
					ip, allocationErr := allocate(name, create)
					if allocationErr == nil {
						results <- result{name: name, ip: ip}
					}
					return allocationErr
				}
				if tc.secondKind == "initial" {
					err = second.withRegularServiceNetworkAllocationLock(run)
				} else {
					err = server.withRegularServiceNetworkAllocationLock(mutation, run)
				}
				if err != nil {
					results <- result{name: tc.secondKind, err: err}
				}
			}()
			<-secondAttempted
			select {
			case <-secondEntered:
				t.Fatal("second svc allocator entered before initial install committed")
			case <-time.After(25 * time.Millisecond):
			}
			close(releaseFirst)
			got := map[string]netip.Addr{}
			for len(got) < 2 {
				result := <-results
				if result.err != nil {
					t.Fatal(result.err)
				}
				got[result.name] = result.ip
			}
			if got["worker"] == got["api"] || got["worker"] == got["jobs"] {
				t.Fatalf("serialized allocation reused address: %v", got)
			}
		})
	}
}

func TestRegularNetworkISOAllocationPlanningUsesSharedAllocationLock(t *testing.T) {
	server := newTestServer(t)
	svcPlan := &serviceNetworkMutationPlan{name: "worker", desired: db.ServiceNetworkConfig{Modes: []string{"svc"}}}
	isoPlan := &serviceNetworkMutationPlan{name: "api", desired: db.ServiceNetworkConfig{Modes: []string{"iso"}}}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondAttempted := make(chan struct{})
	secondEntered := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		done <- server.withRegularServiceNetworkAllocationLock(svcPlan, func() error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered
	go func() {
		close(secondAttempted)
		done <- server.withRegularServiceNetworkAllocationLock(isoPlan, func() error {
			close(secondEntered)
			return nil
		})
	}()
	<-secondAttempted
	select {
	case <-secondEntered:
		close(releaseFirst)
		t.Fatal("ISO allocation planning entered while the shared network allocation lock was held")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseFirst)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestRegularNetworkArtifactRollbackRemovesAuthMaterialAndPreservesLiveFiles(t *testing.T) {
	root := t.TempDir()
	binDir := serviceBinDirForRoot(root)
	tailscaleDir := filepath.Join(root, "tailscale")
	for _, dir := range []string{binDir, tailscaleDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(tailscaleDir, "tailscaled.json")
	if err := os.WriteFile(config, []byte("old-config"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := &db.Service{Name: "api", Artifacts: db.ArtifactStore{
		db.ArtifactTSConfig: {Refs: map[db.ArtifactRef]string{"latest": config}},
	}}
	txn, err := beginRegularNetworkArtifactTransaction(root, previous)
	if err != nil {
		t.Fatal(err)
	}
	secret := "tskey-auth-super-secret"
	staged, err := writeFreshRegularNetworkArtifact(root, "bin", "tailscaled-config-", ".json", []byte(secret), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := txn.registerStagedPath(staged); err != nil {
		t.Fatal(err)
	}
	liveState := filepath.Join(tailscaleDir, "tailscaled.state")
	if err := os.WriteFile(liveState, []byte("live identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := txn.rollbackUnreferenced(nil); err != nil {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("rollback error leaked auth material: %v", err)
		}
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(config); err != nil || string(raw) != "old-config" {
		t.Fatalf("restored config = %q, %v", raw, err)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged auth config remains after rollback: %v", err)
	}
	if raw, err := os.ReadFile(liveState); err != nil || string(raw) != "live identity" {
		t.Fatalf("live Tailscale state = %q, %v; want preserved", raw, err)
	}
}

func TestRegularNetworkArtifactRollbackRefusesWorkloadReplacementAtStagedPath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(serviceBinDirForRoot(root), 0o755); err != nil {
		t.Fatal(err)
	}
	txn, err := beginRegularNetworkArtifactTransaction(root, &db.Service{Name: "api"})
	if err != nil {
		t.Fatal(err)
	}
	staged, err := writeFreshRegularNetworkArtifact(root, "bin", "network-", ".service", []byte("transaction-owned"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := txn.registerStagedPath(staged); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(staged); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("workload replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = txn.rollbackUnreferenced(nil)
	if err == nil || !strings.Contains(err.Error(), "changed from its durable provenance") {
		t.Fatalf("rollback error = %v, want ownership refusal", err)
	}
	if raw, readErr := os.ReadFile(staged); readErr != nil || string(raw) != "workload replacement" {
		t.Fatalf("workload replacement = %q, %v; cleanup must not remove it", raw, readErr)
	}
}

func TestRegularNetworkArtifactCommitPrunesSupersededAndOrphanedFiles(t *testing.T) {
	server := newTestServer(t)
	root := server.defaultServiceRootDir("api")
	binDir := serviceBinDirForRoot(root)
	tailscaleDir := filepath.Join(root, "tailscale")
	for _, dir := range []string{binDir, tailscaleDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldUnit := filepath.Join(binDir, "api-old.service")
	oldConfig := filepath.Join(tailscaleDir, "tailscaled-old.json")
	for path, raw := range map[string]string{oldUnit: "old-unit", oldConfig: "old-secret"} {
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	previous := &db.Service{Name: "api", Generation: 3, ServiceType: db.ServiceTypeSystemd, Artifacts: db.ArtifactStore{
		db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(3): oldUnit, "latest": oldUnit}},
		db.ArtifactTSConfig:    {Refs: map[db.ArtifactRef]string{db.Gen(3): oldConfig, "latest": oldConfig}},
	}}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": previous}}); err != nil {
		t.Fatal(err)
	}
	txn, err := beginRegularNetworkArtifactTransaction(root, previous)
	if err != nil {
		t.Fatal(err)
	}
	newUnit := filepath.Join(binDir, "api-network-new.service")
	newConfig := filepath.Join(tailscaleDir, "tailscaled-new.json")
	orphanConfig := filepath.Join(tailscaleDir, "tailscaled-orphan.json")
	for path, raw := range map[string]string{newUnit: "new-unit", newConfig: "new-secret", orphanConfig: "orphan-secret"} {
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{newUnit, newConfig, orphanConfig} {
		if err := txn.registerStagedPath(path); err != nil {
			t.Fatal(err)
		}
	}
	liveState := filepath.Join(tailscaleDir, "tailscaled.state")
	if err := os.WriteFile(liveState, []byte("live identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := previous.Clone()
	target.Artifacts[db.ArtifactSystemdUnit] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(3): newUnit, "latest": newUnit}}
	target.Artifacts[db.ArtifactTSConfig] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(3): newConfig, "latest": newConfig}}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": target}}); err != nil {
		t.Fatal(err)
	}
	if err := txn.cleanupCommitted(server); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{oldUnit, oldConfig, orphanConfig} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("superseded or orphaned artifact %s remains: %v", path, err)
		}
	}
	for _, path := range []string{newUnit, newConfig} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("committed artifact %s was removed: %v", path, err)
		}
	}
	if raw, err := os.ReadFile(liveState); err != nil || string(raw) != "live identity" {
		t.Fatalf("live Tailscale state = %q, %v; want preserved", raw, err)
	}
}

func TestRegularNetworkArtifactTransactionDetectsCommittedReference(t *testing.T) {
	server := newTestServer(t)
	root := server.defaultServiceRootDir("api")
	if err := os.MkdirAll(serviceBinDirForRoot(root), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd}
	txn, err := beginRegularNetworkArtifactTransaction(root, previous)
	if err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(serviceBinDirForRoot(root), "api-network-generated.service")
	if err := os.WriteFile(generated, []byte("generated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := txn.registerStagedPath(generated); err != nil {
		t.Fatal(err)
	}
	committed := previous.Clone()
	committed.Artifacts = db.ArtifactStore{db.ArtifactSystemdUnit: {
		Refs: map[db.ArtifactRef]string{"latest": generated},
	}}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": committed}}); err != nil {
		t.Fatal(err)
	}
	got, err := txn.committedArtifactReferenced(server, "api")
	if err != nil || !got {
		t.Fatalf("committedArtifactReferenced = %t, %v; want true", got, err)
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": previous}}); err != nil {
		t.Fatal(err)
	}
	got, err = txn.committedArtifactReferenced(server, "api")
	if err != nil || got {
		t.Fatalf("committedArtifactReferenced after rollback = %t, %v; want false", got, err)
	}
}

func TestServiceSetNetworkNoOpDoesNotStartTransaction(t *testing.T) {
	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": {
		Name: "api", ServiceType: db.ServiceTypeSystemd,
		Network: &db.ServiceNetworkConfig{Modes: []string{"host"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	oldRunning := isServiceRunningForNetworkMutation
	oldSteps := newRegularServiceNetworkMutationSteps
	t.Cleanup(func() {
		isServiceRunningForNetworkMutation = oldRunning
		newRegularServiceNetworkMutationSteps = oldSteps
	})
	isServiceRunningForNetworkMutation = func(*Server, string) (bool, error) { return false, nil }
	started := false
	newRegularServiceNetworkMutationSteps = func(*Server, *serviceNetworkMutationPlan) serviceNetworkMutationSteps {
		started = true
		return &recordingServiceNetworkMutationSteps{}
	}
	if err := server.updateServiceNetworkLocked(context.Background(), "api", cli.ServiceSetFlags{Net: "host", NetSet: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if started {
		t.Fatal("no-op network patch started a replacement transaction")
	}
}

func TestServiceSetNetworkPlanAcceptsIdentityIndependentNativeAndTimerISO(t *testing.T) {
	for _, tt := range []struct {
		name      string
		artifacts db.ArtifactStore
	}{
		{name: "native"},
		{name: "timer", artifacts: db.ArtifactStore{db.ArtifactSystemdTimerFile: {
			Refs: map[db.ArtifactRef]string{db.Gen(1): "/timer"},
		}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": {
				Name: "api", ServiceType: db.ServiceTypeSystemd, Artifacts: tt.artifacts,
				Network: &db.ServiceNetworkConfig{Modes: []string{"host"}},
			}}}); err != nil {
				t.Fatal(err)
			}
			oldRunning := isServiceRunningForNetworkMutation
			oldRuntime := catchSystemdUnitActive
			oldEnabled := inspectRegularNetworkUnitEnabled
			t.Cleanup(func() {
				isServiceRunningForNetworkMutation = oldRunning
				catchSystemdUnitActive = oldRuntime
				inspectRegularNetworkUnitEnabled = oldEnabled
			})
			isServiceRunningForNetworkMutation = func(*Server, string) (bool, error) { return false, nil }
			catchSystemdUnitActive = func(string) bool { return false }
			inspectRegularNetworkUnitEnabled = func(context.Context, string) (bool, error) { return false, nil }

			plan, err := server.planServiceNetworkMutation(context.Background(), "api", cli.ServiceSetFlags{Net: "iso", NetSet: true})
			if err != nil {
				t.Fatalf("planServiceNetworkMutation: %v", err)
			}
			if !reflect.DeepEqual(plan.currentDesired.Modes, []string{"host"}) || !reflect.DeepEqual(plan.desired.Modes, []string{"iso"}) {
				t.Fatalf("plan direction = %v -> %v, want host -> iso", plan.currentDesired.Modes, plan.desired.Modes)
			}
		})
	}
}

func TestServiceSetNetworkPlanRetainsNativeAndTimerISOTopologyRejections(t *testing.T) {
	for _, tt := range []struct {
		name      string
		artifacts db.ArtifactStore
	}{
		{name: "native"},
		{name: "timer", artifacts: db.ArtifactStore{db.ArtifactSystemdTimerFile: {
			Refs: map[db.ArtifactRef]string{db.Gen(1): "/timer"},
		}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": {
				Name: "api", ServiceType: db.ServiceTypeSystemd, Artifacts: tt.artifacts,
				Network: &db.ServiceNetworkConfig{Modes: []string{"host"}},
			}}}); err != nil {
				t.Fatal(err)
			}
			_, err := server.planServiceNetworkMutation(context.Background(), "api", cli.ServiceSetFlags{
				Net: "iso,ts", NetSet: true, TsTags: []string{"tag:app"}, TsTagsSet: true,
			})
			if err == nil || !strings.Contains(err.Error(), "ISO supports only iso") {
				t.Fatalf("planServiceNetworkMutation error = %v, want topology rejection", err)
			}
		})
	}
}

func TestServiceSetNetworkSelectsISOLifecycleForEveryTransitionDirection(t *testing.T) {
	for _, tt := range []struct {
		name    string
		current []string
		flags   cli.ServiceSetFlags
		want    serviceNetworkISOTransition
	}{
		{name: "regular to ISO", current: []string{"host"}, flags: cli.ServiceSetFlags{Net: "iso", NetSet: true}, want: serviceNetworkRegularToISO},
		{name: "ISO to ISO", current: []string{"iso"}, flags: cli.ServiceSetFlags{Net: "iso", NetSet: true, TsAuthKey: "rotate", TsAuthKeySet: true}, want: serviceNetworkISOToISO},
		{name: "ISO to regular", current: []string{"iso"}, flags: cli.ServiceSetFlags{Net: "host", NetSet: true}, want: serviceNetworkISOToRegular},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t)
			service := &db.Service{Name: "api", ServiceType: db.ServiceTypeDockerCompose, Network: &db.ServiceNetworkConfig{Modes: tt.current}}
			if slices.Contains(tt.current, "iso") {
				service.ISO = &db.ISOAllocation{Kind: string(iso.PayloadCompose), State: string(iso.StateReady), DesiredModes: slices.Clone(tt.current)}
			}
			if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": service}}); err != nil {
				t.Fatal(err)
			}
			oldRunning := isServiceRunningForNetworkMutation
			oldRegular := newRegularServiceNetworkMutationSteps
			oldISO := newISOServiceNetworkMutationSteps
			t.Cleanup(func() {
				isServiceRunningForNetworkMutation = oldRunning
				newRegularServiceNetworkMutationSteps = oldRegular
				newISOServiceNetworkMutationSteps = oldISO
			})
			isServiceRunningForNetworkMutation = func(*Server, string) (bool, error) { return false, nil }
			newRegularServiceNetworkMutationSteps = func(*Server, *serviceNetworkMutationPlan) serviceNetworkMutationSteps {
				t.Fatal("ISO transition selected the regular-only lifecycle")
				return nil
			}
			recorder := &recordingServiceNetworkMutationSteps{}
			newISOServiceNetworkMutationSteps = func(_ *Server, plan *serviceNetworkMutationPlan, direction serviceNetworkISOTransition) serviceNetworkMutationSteps {
				if direction != tt.want {
					t.Fatalf("ISO transition direction = %q, want %q", direction, tt.want)
				}
				if plan.name != "api" {
					t.Fatalf("plan name = %q", plan.name)
				}
				return recorder
			}

			err := server.updateServiceNetworkLocked(context.Background(), "api", tt.flags, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if want := []string{"stage", "stop-previous", "activate", "verify", "commit"}; !slices.Equal(recorder.events, want) {
				t.Fatalf("events = %v, want %v", recorder.events, want)
			}
		})
	}
}

func TestServiceSetISOLifecycleRestoresOrFailsClosedOnEveryPostStageFailure(t *testing.T) {
	for _, failed := range []string{"stop-previous", "activate", "verify", "commit"} {
		t.Run(failed, func(t *testing.T) {
			server := newTestServer(t)
			if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": {
				Name: "api", ServiceType: db.ServiceTypeDockerCompose,
				Network: &db.ServiceNetworkConfig{Modes: []string{"host"}},
			}}}); err != nil {
				t.Fatal(err)
			}
			oldRunning := isServiceRunningForNetworkMutation
			oldISO := newISOServiceNetworkMutationSteps
			t.Cleanup(func() {
				isServiceRunningForNetworkMutation = oldRunning
				newISOServiceNetworkMutationSteps = oldISO
			})
			isServiceRunningForNetworkMutation = func(*Server, string) (bool, error) { return false, nil }
			recorder := &recordingServiceNetworkMutationSteps{failures: map[string]error{failed: errors.New("boom")}}
			newISOServiceNetworkMutationSteps = func(*Server, *serviceNetworkMutationPlan, serviceNetworkISOTransition) serviceNetworkMutationSteps {
				return recorder
			}

			if err := server.updateServiceNetworkLocked(context.Background(), "api", cli.ServiceSetFlags{Net: "iso", NetSet: true}, io.Discard); err == nil {
				t.Fatal("ISO transition failure returned nil")
			}
			if recorder.events[len(recorder.events)-1] != "restore" {
				t.Fatalf("events = %v, want restore last", recorder.events)
			}
		})
	}

	server := newTestServer(t)
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": {
		Name: "api", ServiceType: db.ServiceTypeDockerCompose,
		Network: &db.ServiceNetworkConfig{Modes: []string{"host"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	oldRunning := isServiceRunningForNetworkMutation
	oldISO := newISOServiceNetworkMutationSteps
	t.Cleanup(func() {
		isServiceRunningForNetworkMutation = oldRunning
		newISOServiceNetworkMutationSteps = oldISO
	})
	isServiceRunningForNetworkMutation = func(*Server, string) (bool, error) { return false, nil }
	recorder := &recordingServiceNetworkMutationSteps{failures: map[string]error{
		"activate": errors.New("activate"), "restore": errors.New("restore"),
	}}
	newISOServiceNetworkMutationSteps = func(*Server, *serviceNetworkMutationPlan, serviceNetworkISOTransition) serviceNetworkMutationSteps {
		return recorder
	}
	if err := server.updateServiceNetworkLocked(context.Background(), "api", cli.ServiceSetFlags{Net: "iso", NetSet: true}, io.Discard); err == nil {
		t.Fatal("ISO transition failure returned nil")
	}
	if want := []string{"stage", "stop-previous", "activate", "restore", "fail-closed"}; !slices.Equal(recorder.events, want) {
		t.Fatalf("events = %v, want %v", recorder.events, want)
	}
}

func TestStageNativeISOServiceNetworkReplacementPreservesPayloadAndUsesFreshOwnedArtifacts(t *testing.T) {
	stubServiceNetworkStaticVerification(t)
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "api")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(serviceBinDirForRoot(root), "api-4.service")
	original := "[Unit]\nDescription=api\n\n[Service]\nExecStart=/srv/api/bin/api-4\nUser=root\nGroup=root\n"
	if err := os.WriteFile(unit, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root, Generation: 4, LatestGeneration: 6,
		Network: &db.ServiceNetworkConfig{Modes: []string{"host"}},
		Artifacts: db.ArtifactStore{
			db.ArtifactBinary:      {Refs: map[db.ArtifactRef]string{db.Gen(4): "/payload/api-4", "latest": "/payload/api-6"}},
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(4): unit, "latest": unit}},
		},
	}
	if err := server.cfg.DB.Set(&db.Data{
		ISOPool:  &db.ISOPool{Prefix: netip.MustParsePrefix("172.30.0.0/16"), AllocatorVersion: iso.AllocatorVersion, PolicyVersion: iso.PolicyVersion},
		Services: map[string]*db.Service{"api": previous},
	}); err != nil {
		t.Fatal(err)
	}
	plan := &serviceNetworkMutationPlan{
		name: "api", previous: previous.Clone(), currentDesired: db.ServiceNetworkConfig{Modes: []string{"host"}},
		desired: db.ServiceNetworkConfig{Modes: []string{"iso"}}, network: NetworkOpts{Interfaces: "iso", Modes: []string{"iso"}, ISO: true},
	}

	target, staged, err := server.stageISOServiceNetworkReplacement(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if target.Generation != 4 || target.LatestGeneration != 6 || target.Artifacts[db.ArtifactBinary].Refs[db.Gen(4)] != "/payload/api-4" {
		t.Fatalf("payload generation changed: %#v", target)
	}
	if target.ISO == nil || target.ISO.Project.IsValid() || target.ISO.NetNS == "" {
		t.Fatalf("native ISO allocation = %#v", target.ISO)
	}
	if staged == nil || !reflect.DeepEqual(staged.ISO, target.ISO) {
		t.Fatalf("reserved DB record = %#v, target ISO = %#v", staged, target.ISO)
	}
	for _, name := range []db.ArtifactName{db.ArtifactSystemdUnit, db.ArtifactNetNSService, db.ArtifactNetNSResolv} {
		path, ok := target.Artifacts.Gen(name, target.Generation)
		if !ok || path == unit {
			t.Fatalf("target %s artifact = %q, want fresh transaction-owned path", name, path)
		}
		if _, tracked := plan.artifactTxn.stagedPaths[path]; !tracked {
			t.Fatalf("fresh %s artifact %q is not transaction-owned", name, path)
		}
	}
	if raw, err := os.ReadFile(unit); err != nil || string(raw) != original {
		t.Fatalf("current unit changed during staging: %q, %v", raw, err)
	}
	unitPath, _ := target.Artifacts.Gen(db.ArtifactSystemdUnit, target.Generation)
	raw, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"NetworkNamespacePath=/var/run/netns/" + target.ISO.NetNS,
		"Requires=yeet-api-ns.service",
		"After=yeet-api-ns.service",
		"BindReadOnlyPaths=",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("native ISO unit missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"NoNewPrivileges=", "CapabilityBoundingSet=", "RestrictNamespaces=", "RestrictAddressFamilies="} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("native ISO unit added privilege policy %q:\n%s", forbidden, text)
		}
	}
}

func TestNativeISOServiceNetworkMutationCommitsAndDiscardsStagedReplacement(t *testing.T) {
	t.Run("commit", func(t *testing.T) {
		stubServiceNetworkStaticVerification(t)
		server, plan, originalUnit := newNativeISOServiceNetworkMutationFixture(t, false)
		stubNativeISOServiceNetworkMutationRuntime(t)
		mutation := &isoServiceNetworkMutation{server: server, plan: plan, direction: serviceNetworkRegularToISO}

		if err := mutation.Stage(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := mutation.StopPrevious(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := mutation.Verify(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := mutation.Commit(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := mutation.CleanupCommittedArtifacts(context.Background()); err != nil {
			t.Fatal(err)
		}

		view, err := server.serviceView("api")
		if err != nil {
			t.Fatal(err)
		}
		got := view.AsStruct()
		if got.ISO == nil || !reflect.DeepEqual(got.Network.Modes, []string{"iso"}) || got.ISO.State != string(iso.StateStopped) {
			t.Fatalf("committed native ISO record = %#v", got)
		}
		if got.Generation != plan.previous.Generation || got.LatestGeneration != plan.previous.LatestGeneration {
			t.Fatalf("generation changed during ISO commit: %#v", got)
		}
		if _, err := os.Stat(originalUnit); !os.IsNotExist(err) {
			t.Fatalf("superseded unit remains after committed artifact cleanup: %v", err)
		}
	})

	t.Run("discard", func(t *testing.T) {
		stubServiceNetworkStaticVerification(t)
		server, plan, originalUnit := newNativeISOServiceNetworkMutationFixture(t, false)
		stubNativeISOServiceNetworkMutationRuntime(t)
		mutation := &isoServiceNetworkMutation{server: server, plan: plan, direction: serviceNetworkRegularToISO}
		if err := mutation.Stage(context.Background()); err != nil {
			t.Fatal(err)
		}
		stagedUnit, ok := mutation.target.Artifacts.Gen(db.ArtifactSystemdUnit, mutation.target.Generation)
		if !ok {
			t.Fatal("staged ISO unit is missing")
		}
		if err := mutation.DiscardStagedArtifacts(); err != nil {
			t.Fatal(err)
		}
		view, err := server.serviceView("api")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(view.AsStruct(), plan.previous) {
			t.Fatalf("discarded ISO stage record = %#v, want previous %#v", view.AsStruct(), plan.previous)
		}
		if _, err := os.Stat(stagedUnit); !os.IsNotExist(err) {
			t.Fatalf("discarded staged unit remains: %v", err)
		}
		if _, err := os.Stat(originalUnit); err != nil {
			t.Fatalf("original unit was not preserved: %v", err)
		}
	})

	t.Run("discard conflict preserves concurrent ISO record", func(t *testing.T) {
		stubServiceNetworkStaticVerification(t)
		server, plan, _ := newNativeISOServiceNetworkMutationFixture(t, false)
		stubNativeISOServiceNetworkMutationRuntime(t)
		mutation := &isoServiceNetworkMutation{server: server, plan: plan, direction: serviceNetworkRegularToISO}
		if err := mutation.Stage(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := server.markISOState("api", string(iso.StateQuarantined), errors.New("concurrent change")); err != nil {
			t.Fatal(err)
		}
		concurrent, err := server.serviceView("api")
		if err != nil {
			t.Fatal(err)
		}
		if err := mutation.DiscardStagedArtifacts(); err == nil || !strings.Contains(err.Error(), "changed while rolling back") {
			t.Fatalf("discard conflict error = %v", err)
		}
		view, err := server.serviceView("api")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(view.AsStruct(), concurrent.AsStruct()) {
			t.Fatalf("discard conflict overwrote concurrent record: got %#v, want %#v", view.AsStruct(), concurrent.AsStruct())
		}
	})

	t.Run("reservation rollback conflict preserves concurrent ISO record", func(t *testing.T) {
		server, plan, _ := newNativeISOServiceNetworkMutationFixture(t, false)
		stage, err := newISONativeNetworkStage(context.Background(), server, plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := stage.reserve(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := server.markISOState("api", string(iso.StateQuarantined), errors.New("concurrent change")); err != nil {
			t.Fatal(err)
		}
		concurrent, err := server.serviceView("api")
		if err != nil {
			t.Fatal(err)
		}
		stageErr := errors.New("render failed")
		stage.rollbackOnError(&stageErr)
		if !strings.Contains(stageErr.Error(), "changed while rolling back") {
			t.Fatalf("reservation rollback error = %v", stageErr)
		}
		view, err := server.serviceView("api")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(view.AsStruct(), concurrent.AsStruct()) {
			t.Fatalf("reservation rollback overwrote concurrent record: got %#v, want %#v", view.AsStruct(), concurrent.AsStruct())
		}
	})

	t.Run("commit conflict does not overwrite concurrent ISO record", func(t *testing.T) {
		stubServiceNetworkStaticVerification(t)
		server, plan, _ := newNativeISOServiceNetworkMutationFixture(t, false)
		stubNativeISOServiceNetworkMutationRuntime(t)
		baseSystemctl := runISOSystemctlForRuntime
		stopCalls := 0
		removeTopologyCalls := 0
		runISOSystemctlForRuntime = func(ctx context.Context, args ...string) ([]byte, error) {
			if len(args) != 0 && args[0] == "stop" {
				stopCalls++
			}
			return baseSystemctl(ctx, args...)
		}
		removeISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error {
			removeTopologyCalls++
			return nil
		}
		mutation := &isoServiceNetworkMutation{server: server, plan: plan, direction: serviceNetworkRegularToISO}
		if err := mutation.Stage(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := server.markISOState("api", string(iso.StateQuarantined), errors.New("concurrent change")); err != nil {
			t.Fatal(err)
		}
		concurrent, err := server.serviceView("api")
		if err != nil {
			t.Fatal(err)
		}
		if err := mutation.Commit(context.Background()); err == nil || !strings.Contains(err.Error(), "changed during ISO network mutation") {
			t.Fatalf("commit conflict error = %v", err)
		}
		if err := mutation.Restore(context.Background()); err == nil || !strings.Contains(err.Error(), "changed while rolling back") {
			t.Fatalf("restore conflict error = %v", err)
		}
		view, err := server.serviceView("api")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(view.AsStruct(), concurrent.AsStruct()) {
			t.Fatalf("commit recovery overwrote concurrent record: got %#v, want %#v", view.AsStruct(), concurrent.AsStruct())
		}
		if stopCalls != 0 || removeTopologyCalls != 0 {
			t.Fatalf("commit conflict invoked runtime cleanup: stops=%d topology-removals=%d", stopCalls, removeTopologyCalls)
		}
	})
}

func TestComposeISOCommitConflictRecoveryDoesNotStopConcurrentRuntime(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "app")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(serviceBinDirForRoot(root), "compose.yml")
	overlay := filepath.Join(serviceBinDirForRoot(root), "iso-network.yml")
	if err := os.WriteFile(base, []byte("services:\n  api:\n    image: nginx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, []byte("networks:\n  default:\n    driver: yeet\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := &db.Service{
		Name: "app", ServiceType: db.ServiceTypeDockerCompose, ServiceRoot: root, Generation: 2,
		Network:   &db.ServiceNetworkConfig{Modes: []string{"host"}},
		Artifacts: db.ArtifactStore{db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{db.Gen(2): base}}},
	}
	target := previous.Clone()
	target.Network = &db.ServiceNetworkConfig{Modes: []string{"iso"}}
	target.ISO = testISORuntimeAllocation("app", iso.StateReserved)
	target.Artifacts[db.ArtifactDockerComposeNetwork] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(2): overlay}}
	staged := target.Clone()
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"app": staged.Clone()}}); err != nil {
		t.Fatal(err)
	}
	compose, err := server.dockerComposeService("app")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(compose.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dockerDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	composeCalls := 0
	compose.NewCmdContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		composeCalls++
		return exec.CommandContext(ctx, "sh", "-c", "exit 0")
	}
	stubNativeISOServiceNetworkMutationRuntime(t)
	removeTopologyCalls := 0
	removeISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error {
		removeTopologyCalls++
		return nil
	}
	plan := &serviceNetworkMutationPlan{name: "app", previous: previous.Clone(), desired: db.ServiceNetworkConfig{Modes: []string{"iso"}}}
	mutation := &isoServiceNetworkMutation{
		server: server, plan: plan, direction: serviceNetworkRegularToISO,
		target: target, staged: staged, compose: compose,
	}
	concurrent := previous.Clone()
	concurrent.Generation = 3
	concurrent.LatestGeneration = 3
	concurrent.Artifacts[db.ArtifactDockerComposeFile] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(3): base}}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"app": concurrent.Clone()}}); err != nil {
		t.Fatal(err)
	}
	if err := mutation.Commit(context.Background()); err == nil || !strings.Contains(err.Error(), "changed during ISO network mutation") {
		t.Fatalf("Commit error = %v, want exact-record conflict", err)
	}
	if err := mutation.Restore(context.Background()); err == nil || !strings.Contains(err.Error(), "changed while rolling back") {
		t.Fatalf("Restore error = %v, want exact-record conflict", err)
	}
	got, err := server.serviceView("app")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.AsStruct(), concurrent) {
		t.Fatalf("concurrent Compose record changed: got %#v, want %#v", got.AsStruct(), concurrent)
	}
	if composeCalls != 0 || removeTopologyCalls != 0 {
		t.Fatalf("commit conflict invoked runtime cleanup: compose=%d topology-removals=%d", composeCalls, removeTopologyCalls)
	}
}

func TestISOToISOCommitConflictRecoveryDoesNotStopConcurrentRuntime(t *testing.T) {
	stubServiceNetworkStaticVerification(t)
	server, plan, _ := newNativeISOServiceNetworkMutationFixture(t, true)
	plan.desired = db.ServiceNetworkConfig{Modes: []string{"iso"}}
	plan.network = NetworkOpts{Interfaces: "iso", Modes: []string{"iso"}, ISO: true}
	stubNativeISOServiceNetworkMutationRuntime(t)
	baseSystemctl := runISOSystemctlForRuntime
	stopCalls := 0
	removeTopologyCalls := 0
	runISOSystemctlForRuntime = func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) != 0 && args[0] == "stop" {
			stopCalls++
		}
		return baseSystemctl(ctx, args...)
	}
	removeISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error {
		removeTopologyCalls++
		return nil
	}
	mutation := &isoServiceNetworkMutation{server: server, plan: plan, direction: serviceNetworkISOToISO}
	if err := mutation.Stage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := server.markISOState("api", string(iso.StateQuarantined), errors.New("concurrent ISO replacement")); err != nil {
		t.Fatal(err)
	}
	concurrent, err := server.serviceView("api")
	if err != nil {
		t.Fatal(err)
	}
	if err := mutation.Commit(context.Background()); err == nil || !strings.Contains(err.Error(), "changed during ISO network mutation") {
		t.Fatalf("Commit error = %v, want exact-record conflict", err)
	}
	if err := mutation.Restore(context.Background()); err == nil || !strings.Contains(err.Error(), "changed while rolling back") {
		t.Fatalf("Restore error = %v, want exact-record conflict", err)
	}
	got, err := server.serviceView("api")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.AsStruct(), concurrent.AsStruct()) {
		t.Fatalf("concurrent ISO record changed: got %#v, want %#v", got.AsStruct(), concurrent.AsStruct())
	}
	if stopCalls != 0 || removeTopologyCalls != 0 {
		t.Fatalf("ISO-to-ISO commit conflict invoked runtime cleanup: stops=%d topology-removals=%d", stopCalls, removeTopologyCalls)
	}
}

func TestISOServiceNetworkRestoreClaimsStagedRuntimeBeforeCleanup(t *testing.T) {
	for _, tt := range []struct {
		name     string
		stopErr  error
		wantErr  bool
		wantTomb bool
	}{
		{name: "successful attributable rollback"},
		{name: "stop failure retains attributable tombstone", stopErr: errors.New("stop replacement failed"), wantErr: true, wantTomb: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stubServiceNetworkStaticVerification(t)
			server, plan, originalUnit := newNativeISOServiceNetworkMutationFixture(t, false)
			stubNativeISOServiceNetworkMutationRuntime(t)
			mutation := &isoServiceNetworkMutation{server: server, plan: plan, direction: serviceNetworkRegularToISO}
			if err := mutation.Stage(context.Background()); err != nil {
				t.Fatal(err)
			}
			stagedUnit, ok := mutation.target.Artifacts.Gen(db.ArtifactSystemdUnit, mutation.target.Generation)
			if !ok {
				t.Fatal("staged ISO unit is missing")
			}
			oldRestore := activatePreviousISONetworkRuntimeForMutation
			restoreCalls := 0
			activatePreviousISONetworkRuntimeForMutation = func(_ context.Context, _ *isoServiceNetworkMutation) error {
				restoreCalls++
				current, err := server.serviceView("api")
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(current.AsStruct(), plan.previous) {
					return fmt.Errorf("runtime restore observed record %#v, want previous", current.AsStruct())
				}
				return nil
			}
			t.Cleanup(func() { activatePreviousISONetworkRuntimeForMutation = oldRestore })
			baseSystemctl := runISOSystemctlForRuntime
			stopCalls := 0
			runISOSystemctlForRuntime = func(ctx context.Context, args ...string) ([]byte, error) {
				if len(args) != 0 && args[0] == "stop" {
					stopCalls++
					current, err := server.serviceView("api")
					if err != nil {
						return nil, err
					}
					if current.ISO().State() != string(iso.StateTombstoned) {
						return nil, fmt.Errorf("stop observed ISO state %q, want tombstoned", current.ISO().State())
					}
					if tt.stopErr != nil {
						return nil, tt.stopErr
					}
				}
				return baseSystemctl(ctx, args...)
			}
			removeISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error {
				current, err := server.serviceView("api")
				if err != nil {
					return err
				}
				if current.ISO().State() != string(iso.StateTombstoned) {
					return fmt.Errorf("topology cleanup observed ISO state %q, want tombstoned", current.ISO().State())
				}
				return nil
			}

			err := mutation.Restore(context.Background())
			if got := err != nil; got != tt.wantErr {
				t.Fatalf("Restore error = %v, wantErr %t", err, tt.wantErr)
			}
			if stopCalls == 0 {
				t.Fatal("attributable rollback did not stop the staged runtime")
			}
			current, viewErr := server.serviceView("api")
			if viewErr != nil {
				t.Fatal(viewErr)
			}
			if tt.wantTomb {
				if current.ISO().State() != string(iso.StateTombstoned) {
					t.Fatalf("failed cleanup record = %#v, want tombstone", current.ISO().AsStruct())
				}
				if restoreCalls != 0 {
					t.Fatalf("runtime restore calls after failed cleanup = %d, want 0", restoreCalls)
				}
				return
			}
			if restoreCalls != 1 || !reflect.DeepEqual(current.AsStruct(), plan.previous) {
				t.Fatalf("successful rollback record = %#v, restore calls=%d", current.AsStruct(), restoreCalls)
			}
			if _, err := os.Stat(stagedUnit); !os.IsNotExist(err) {
				t.Fatalf("staged unit remains after rollback: %v", err)
			}
			if _, err := os.Stat(originalUnit); err != nil {
				t.Fatalf("previous unit was not preserved: %v", err)
			}
		})
	}
}

func TestISOServiceNetworkFailClosedPreservesConcurrentServiceRecords(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *db.Service)
		verify func(*testing.T, *db.Service)
	}{
		{name: "generation", mutate: func(_ *testing.T, service *db.Service) { service.Generation++ }},
		{name: "identity", mutate: func(_ *testing.T, service *db.Service) {
			service.Identity = &db.ServiceIdentity{RequestedUser: "2000", RequestedGroup: "2001", UID: 2000, GID: 2001}
		}},
		{name: "concurrent non-ISO replacement", mutate: func(t *testing.T, service *db.Service) {
			t.Helper()
			regularUnit := filepath.Join(serviceBinDirForRoot(service.ServiceRoot), "api-concurrent-regular.service")
			regularDefinition := "[Unit]\nDescription=concurrent regular replacement\n\n[Service]\nExecStart=/srv/api/bin/api-regular\n"
			if err := os.WriteFile(regularUnit, []byte(regularDefinition), 0o644); err != nil {
				t.Fatal(err)
			}
			service.Network = &db.ServiceNetworkConfig{Modes: []string{"svc"}}
			service.SvcNetwork = &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.27")}
			service.Macvlan = nil
			service.TSNet = nil
			service.ISO = nil
			for name := range isoNetworkArtifactNames {
				delete(service.Artifacts, name)
			}
			service.Artifacts[db.ArtifactSystemdUnit] = &db.Artifact{Refs: map[db.ArtifactRef]string{
				db.Gen(service.Generation): regularUnit,
				"latest":                   regularUnit,
			}}
		}, verify: func(t *testing.T, service *db.Service) {
			t.Helper()
			if service.ISO != nil || service.Network == nil || !slices.Equal(service.Network.Modes, []string{"svc"}) || service.SvcNetwork == nil {
				t.Fatalf("concurrent regular pointers = %#v", service)
			}
			for name := range isoNetworkArtifactNames {
				if _, exists := service.Artifacts[name]; exists {
					t.Fatalf("concurrent regular record retained ISO artifact %q", name)
				}
			}
			unit, ok := service.Artifacts.Gen(db.ArtifactSystemdUnit, service.Generation)
			if !ok || !strings.HasSuffix(unit, "api-concurrent-regular.service") {
				t.Fatalf("concurrent regular systemd artifact = %q", unit)
			}
			raw, err := os.ReadFile(unit)
			if err != nil {
				t.Fatal(err)
			}
			definition := string(raw)
			for _, forbidden := range []string{"NetworkNamespacePath=", "Requires=yeet-api-ns.service", "BindReadOnlyPaths="} {
				if strings.Contains(definition, forbidden) {
					t.Fatalf("concurrent regular systemd definition retained %q:\n%s", forbidden, definition)
				}
			}
		}},
		{name: "artifacts", mutate: func(_ *testing.T, service *db.Service) {
			service.Artifacts[db.ArtifactBinary] = &db.Artifact{Refs: map[db.ArtifactRef]string{"latest": "/concurrent/api"}}
		}},
		{name: "concurrent ISO replacement", mutate: func(_ *testing.T, service *db.Service) {
			service.Network = &db.ServiceNetworkConfig{Modes: []string{"iso"}}
			service.ISO = newDBISOAllocation("api", isoReservationRequest{Kind: iso.PayloadNative, Modes: []string{"iso"}}, netip.MustParsePrefix("172.30.4.0/30"))
			service.ISO.State = string(iso.StateReady)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubServiceNetworkStaticVerification(t)
			server, plan, _ := newNativeISOServiceNetworkMutationFixture(t, false)
			stubNativeISOServiceNetworkMutationRuntime(t)
			mutation := &isoServiceNetworkMutation{server: server, plan: plan, direction: serviceNetworkRegularToISO}
			if err := mutation.Stage(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
				tt.mutate(t, service)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			concurrent, err := server.serviceView("api")
			if err != nil {
				t.Fatal(err)
			}
			if err := mutation.FailClosed(context.Background()); err == nil || !strings.Contains(err.Error(), "changed before ISO fail-closed tombstone") {
				t.Fatalf("FailClosed error = %v, want exact-record conflict", err)
			}
			got, err := server.serviceView("api")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.AsStruct(), concurrent.AsStruct()) {
				t.Fatalf("FailClosed overwrote concurrent record: got %#v, want %#v", got.AsStruct(), concurrent.AsStruct())
			}
			if tt.verify != nil {
				tt.verify(t, got.AsStruct())
			}
		})
	}
}

func TestRestoreISOStageReservationWithoutExactSnapshotPreservesConcurrentService(t *testing.T) {
	server, plan, _ := newNativeISOServiceNetworkMutationFixture(t, false)
	concurrent := plan.previous.Clone()
	concurrent.Generation++
	concurrent.Network = &db.ServiceNetworkConfig{Modes: []string{"svc"}}
	concurrent.SvcNetwork = &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.31")}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": concurrent.Clone()}}); err != nil {
		t.Fatal(err)
	}

	err := restoreISOStageReservation(server, plan, nil, errors.New("capture reserved record failed"))
	if err == nil || !strings.Contains(err.Error(), "without an exact reserved record") {
		t.Fatalf("restoreISOStageReservation error = %v, want missing exact snapshot", err)
	}
	got, err := server.serviceView("api")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.AsStruct(), concurrent) {
		t.Fatalf("reservation restore overwrote concurrent service: got %#v, want %#v", got.AsStruct(), concurrent)
	}
}

func TestISOStageRollbackPreservesLiveReferencedOwnedArtifact(t *testing.T) {
	stubServiceNetworkStaticVerification(t)
	server, plan, _ := newNativeISOServiceNetworkMutationFixture(t, false)
	stubNativeISOServiceNetworkMutationRuntime(t)
	mutation := &isoServiceNetworkMutation{server: server, plan: plan, direction: serviceNetworkRegularToISO}
	if err := mutation.Stage(context.Background()); err != nil {
		t.Fatal(err)
	}
	stagedUnit, ok := mutation.target.Artifacts.Gen(db.ArtifactSystemdUnit, mutation.target.Generation)
	if !ok {
		t.Fatal("staged ISO unit is missing")
	}
	if _, _, err := server.cfg.DB.MutateService("api", func(_ *db.Data, service *db.Service) error {
		service.Generation++
		service.Artifacts[db.ArtifactSystemdUnit] = &db.Artifact{Refs: map[db.ArtifactRef]string{"latest": stagedUnit}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	concurrent, err := server.serviceView("api")
	if err != nil {
		t.Fatal(err)
	}

	err = mutation.DiscardStagedArtifacts()
	if err == nil || !strings.Contains(err.Error(), "changed while rolling back") {
		t.Fatalf("DiscardStagedArtifacts error = %v, want exact-record conflict", err)
	}
	if _, err := os.Stat(stagedUnit); err != nil {
		t.Fatalf("live-referenced staged artifact was removed: %v", err)
	}
	got, err := server.serviceView("api")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.AsStruct(), concurrent.AsStruct()) {
		t.Fatalf("live-reference rollback overwrote concurrent record: got %#v, want %#v", got.AsStruct(), concurrent.AsStruct())
	}
}

func TestNativeISOStageFailureRemovesEveryCreatedArtifact(t *testing.T) {
	tests := []struct {
		name   string
		inject func(*testing.T, string)
		want   string
	}{
		{
			name: "after resolver",
			inject: func(t *testing.T, _ string) {
				oldExecutable := catchExecutablePath
				catchExecutablePath = func() (string, error) { return "", errors.New("injected gate render failure") }
				t.Cleanup(func() { catchExecutablePath = oldExecutable })
			},
			want: "injected gate render failure",
		},
		{
			name: "after gate",
			inject: func(t *testing.T, originalUnit string) {
				if err := os.WriteFile(originalUnit, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "current systemd unit is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, plan, originalUnit := newNativeISOServiceNetworkMutationFixture(t, false)
			tt.inject(t, originalUnit)
			_, _, err := server.stageISOServiceNetworkReplacement(context.Background(), plan)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("stageISOServiceNetworkReplacement error = %v, want %q", err, tt.want)
			}
			for _, pattern := range []string{"iso-resolv-*", "iso-gate-*", "api-network-*"} {
				matches, globErr := filepath.Glob(filepath.Join(serviceBinDirForRoot(plan.previous.ServiceRoot), pattern))
				if globErr != nil {
					t.Fatal(globErr)
				}
				if len(matches) != 0 {
					t.Fatalf("failed native stage left %s artifacts: %v", pattern, matches)
				}
			}
			view, viewErr := server.serviceView("api")
			if viewErr != nil {
				t.Fatal(viewErr)
			}
			if !reflect.DeepEqual(view.AsStruct(), plan.previous) {
				t.Fatalf("failed native stage record = %#v, want previous %#v", view.AsStruct(), plan.previous)
			}
		})
	}
}

func TestNativeISOStageInjectedFailureAfterEachArtifactRemovesOwnedFiles(t *testing.T) {
	for _, artifact := range []db.ArtifactName{db.ArtifactNetNSResolv, db.ArtifactNetNSService, db.ArtifactSystemdUnit} {
		t.Run(string(artifact), func(t *testing.T) {
			server, plan, originalUnit := newNativeISOServiceNetworkMutationFixture(t, false)
			originalRaw, err := os.ReadFile(originalUnit)
			if err != nil {
				t.Fatal(err)
			}
			oldAfter := afterOwnedRegularNetworkArtifact
			afterOwnedRegularNetworkArtifact = func(name db.ArtifactName, _ string) error {
				if name == artifact {
					return errors.New("injected partial native stage failure")
				}
				return nil
			}
			t.Cleanup(func() { afterOwnedRegularNetworkArtifact = oldAfter })

			_, _, err = server.stageISOServiceNetworkReplacement(context.Background(), plan)
			if err == nil || !strings.Contains(err.Error(), "injected partial native stage failure") {
				t.Fatalf("stageISOServiceNetworkReplacement error = %v", err)
			}
			for _, pattern := range []string{"iso-resolv-*", "iso-gate-*", "api-network-*"} {
				matches, globErr := filepath.Glob(filepath.Join(serviceBinDirForRoot(plan.previous.ServiceRoot), pattern))
				if globErr != nil {
					t.Fatal(globErr)
				}
				if len(matches) != 0 {
					t.Fatalf("failed native stage left %s artifacts: %v", pattern, matches)
				}
			}
			if got, readErr := os.ReadFile(originalUnit); readErr != nil || !bytes.Equal(got, originalRaw) {
				t.Fatalf("prior native unit changed: %q, %v", got, readErr)
			}
		})
	}
}

func TestComposeISOTailscaleStageInjectedFailureAfterEachArtifactRemovesOwnedFiles(t *testing.T) {
	secret := "tskey-auth-partial-stage-secret"
	for _, artifact := range []db.ArtifactName{
		db.ArtifactDockerComposeNetwork, db.ArtifactNetNSService,
		db.ArtifactTSEnv, db.ArtifactTSService, db.ArtifactTSConfig,
	} {
		t.Run(string(artifact), func(t *testing.T) {
			var logs bytes.Buffer
			previousLogWriter := log.Writer()
			log.SetOutput(io.MultiWriter(previousLogWriter, &logs))
			t.Cleanup(func() { log.SetOutput(previousLogWriter) })
			server := newTestServer(t)
			root := filepath.Join(t.TempDir(), "app")
			if err := ensureDirsForRoot(root, ""); err != nil {
				t.Fatal(err)
			}
			base := filepath.Join(serviceBinDirForRoot(root), "compose.yml")
			oldOverlay := filepath.Join(serviceBinDirForRoot(root), "old-iso-network.yml")
			if err := os.WriteFile(base, []byte("services:\n  api:\n    image: nginx\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(oldOverlay, []byte("old live overlay\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			version := "1.92.3"
			tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
			if err := os.MkdirAll(tsdDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(tsdDir, "tailscaled-"+version), []byte("tailscaled"), 0o755); err != nil {
				t.Fatal(err)
			}
			previous := &db.Service{
				Name: "app", ServiceType: db.ServiceTypeDockerCompose, ServiceRoot: root, Generation: 4, LatestGeneration: 6,
				Network: &db.ServiceNetworkConfig{Modes: []string{"host"}},
				Artifacts: db.ArtifactStore{
					db.ArtifactDockerComposeFile:    {Refs: map[db.ArtifactRef]string{db.Gen(4): base, "latest": base}},
					db.ArtifactDockerComposeNetwork: {Refs: map[db.ArtifactRef]string{db.Gen(4): oldOverlay, "latest": oldOverlay}},
				},
			}
			if err := server.cfg.DB.Set(&db.Data{
				ISOPool:  &db.ISOPool{Prefix: netip.MustParsePrefix("172.30.0.0/16"), AllocatorVersion: iso.AllocatorVersion, PolicyVersion: iso.PolicyVersion},
				Services: map[string]*db.Service{"app": previous.Clone()},
			}); err != nil {
				t.Fatal(err)
			}
			oldResolve := resolveISOComposeForNetworkMutation
			resolveISOComposeForNetworkMutation = func(_ context.Context, opts svc.ComposeResolveOptions) ([]byte, error) {
				if len(opts.Files) == 1 {
					return []byte(`{"name":"catch-app","networks":{"default":{"name":"catch-app_default","ipam":{}}},"services":{"api":{"image":"nginx","networks":{"default":null}}}}`), nil
				}
				view, err := server.serviceView("app")
				if err != nil {
					return nil, err
				}
				allocation := view.ISO().AsStruct()
				return []byte(fmt.Sprintf(`{
					"name":"catch-app",
					"networks":{"default":{"name":"catch-app_default","driver":"yeet","driver_opts":{"dev.catchit.mode":"iso","dev.catchit.netns":"/var/run/netns/%s"},"enable_ipv6":false,"ipam":{"config":[{"subnet":"%s","gateway":"%s"}]}}},
					"services":{"api":{"image":"nginx","dns":["100.100.100.100"],"networks":{"default":{"ipv4_address":"%s"}}}}
				}`, allocation.NetNS, allocation.Project, allocation.Gateway, allocation.Components["api"].Address)), nil
			}
			t.Cleanup(func() { resolveISOComposeForNetworkMutation = oldResolve })
			oldAfter := afterOwnedRegularNetworkArtifact
			afterOwnedRegularNetworkArtifact = func(name db.ArtifactName, _ string) error {
				if name == artifact {
					return errors.New("injected partial compose stage failure")
				}
				return nil
			}
			t.Cleanup(func() { afterOwnedRegularNetworkArtifact = oldAfter })
			plan := &serviceNetworkMutationPlan{
				name: "app", previous: previous.Clone(), currentDesired: db.ServiceNetworkConfig{Modes: []string{"host"}},
				desired: db.ServiceNetworkConfig{Modes: []string{"iso", "ts"}, TSVersion: version, TSTags: []string{"tag:app"}},
				network: NetworkOpts{Interfaces: "iso,ts", Modes: []string{"iso", "ts"}, ISO: true, Tailscale: TailscaleOpts{
					Version: version, Tags: []string{"tag:app"}, AuthKey: secret,
				}},
			}

			_, _, err := server.stageISOServiceNetworkReplacement(context.Background(), plan)
			if err == nil || !strings.Contains(err.Error(), "injected partial compose stage failure") {
				t.Fatalf("stageISOServiceNetworkReplacement error = %v", err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("partial stage error leaked auth material: %v", err)
			}
			if strings.Contains(logs.String(), secret) {
				t.Fatalf("partial stage logs leaked auth material: %s", logs.String())
			}
			if raw, readErr := os.ReadFile(oldOverlay); readErr != nil || string(raw) != "old live overlay\n" {
				t.Fatalf("prior live overlay changed: %q, %v", raw, readErr)
			}
			err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil || entry.IsDir() {
					return walkErr
				}
				raw, readErr := os.ReadFile(path)
				if readErr != nil {
					return readErr
				}
				if strings.Contains(string(raw), secret) {
					return fmt.Errorf("auth material remains in %s", path)
				}
				if path != base && path != oldOverlay {
					return fmt.Errorf("partial stage artifact remains: %s", path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			view, viewErr := server.serviceView("app")
			if viewErr != nil {
				t.Fatal(viewErr)
			}
			if !reflect.DeepEqual(view.AsStruct(), previous) {
				t.Fatalf("partial stage DB record = %#v, want previous %#v", view.AsStruct(), previous)
			}
		})
	}
}

func TestNativeISOServiceNetworkVerifyRepairsTopologyAfterActivation(t *testing.T) {
	stubServiceNetworkStaticVerification(t)
	server, plan, _ := newNativeISOServiceNetworkMutationFixture(t, false)
	stubNativeISOServiceNetworkMutationRuntime(t)
	mutation := &isoServiceNetworkMutation{server: server, plan: plan, direction: serviceNetworkRegularToISO}
	if err := mutation.Stage(context.Background()); err != nil {
		t.Fatal(err)
	}

	repaired := false
	ensureISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error {
		repaired = true
		return nil
	}
	verifyISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error {
		if !repaired {
			return errors.New("late topology drift was not repaired")
		}
		return nil
	}
	if err := mutation.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNativeISOToRegularMutationRestoresAfterPostCommitActivationFailure(t *testing.T) {
	stubServiceNetworkStaticVerification(t)
	server, plan, originalUnit := newNativeISOServiceNetworkMutationFixture(t, true)
	stubNativeISOServiceNetworkMutationRuntime(t)
	oldActivatePrevious := activatePreviousISONetworkRuntimeForMutation
	activatePreviousISONetworkRuntimeForMutation = func(context.Context, *isoServiceNetworkMutation) error { return nil }
	t.Cleanup(func() { activatePreviousISONetworkRuntimeForMutation = oldActivatePrevious })
	mutation := &isoServiceNetworkMutation{server: server, plan: plan, direction: serviceNetworkISOToRegular}

	if err := mutation.Stage(context.Background()); err != nil {
		t.Fatal(err)
	}
	stagedUnit, ok := mutation.target.Artifacts.Gen(db.ArtifactSystemdUnit, mutation.target.Generation)
	if !ok || stagedUnit == originalUnit {
		t.Fatalf("regular replacement unit = %q, want fresh", stagedUnit)
	}
	if err := mutation.StopPrevious(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := mutation.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := mutation.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mutation.Commit(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("post-commit activation error = %v, want context canceled", err)
	}
	if !mutation.committed {
		t.Fatal("ISO-to-regular DB commit was not recorded before activation failure")
	}
	committedView, err := server.serviceView("api")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(committedView.AsStruct(), mutation.target) {
		t.Fatalf("committed replacement = %#v, want staged target %#v", committedView.AsStruct(), mutation.target)
	}
	if err := mutation.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	view, err := server.serviceView("api")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(view.AsStruct(), plan.previous) {
		t.Fatalf("restored service = %#v, want previous %#v", view.AsStruct(), plan.previous)
	}
	if _, err := os.Stat(stagedUnit); !os.IsNotExist(err) {
		t.Fatalf("failed regular replacement artifact remains: %v", err)
	}
	if _, err := os.Stat(originalUnit); err != nil {
		t.Fatalf("original ISO unit was not preserved: %v", err)
	}
}

func TestISOToRegularCommittedRestoreConflictDoesNotStopConcurrentRuntime(t *testing.T) {
	for _, serviceType := range []db.ServiceType{db.ServiceTypeSystemd, db.ServiceTypeDockerCompose} {
		t.Run(string(serviceType), func(t *testing.T) {
			server, mutation, _ := newISOToRegularRestoreOwnershipFixture(t, serviceType)
			concurrent := mutation.target.Clone()
			concurrent.Generation++
			concurrent.LatestGeneration = concurrent.Generation
			if serviceType == db.ServiceTypeSystemd {
				unit := filepath.Join(serviceBinDirForRoot(concurrent.ServiceRoot), "api-concurrent.service")
				if err := os.WriteFile(unit, []byte("[Service]\nExecStart=/bin/concurrent\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				concurrent.Artifacts[db.ArtifactSystemdUnit] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(concurrent.Generation): unit}}
			}
			if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": concurrent.Clone()}}); err != nil {
				t.Fatal(err)
			}
			systemctlCalls, dockerLog := recordRestoreOwnershipCallbacks(t, serviceType, nil)

			err := mutation.Restore(context.Background())
			if err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatalf("Restore error = %v, want exact-record conflict", err)
			}
			got, viewErr := server.serviceView("api")
			if viewErr != nil {
				t.Fatal(viewErr)
			}
			if !reflect.DeepEqual(got.AsStruct(), concurrent) {
				t.Fatalf("concurrent record changed: got %#v, want %#v", got.AsStruct(), concurrent)
			}
			assertNoRestoreOwnershipCallbacks(t, *systemctlCalls, dockerLog)
		})
	}
}

func TestISOToRegularCommittedRestoreStopFailureRetainsAttributedTombstone(t *testing.T) {
	stopErr := errors.New("injected replacement stop failure")
	for _, serviceType := range []db.ServiceType{db.ServiceTypeSystemd, db.ServiceTypeDockerCompose} {
		t.Run(string(serviceType), func(t *testing.T) {
			server, mutation, previous := newISOToRegularRestoreOwnershipFixture(t, serviceType)
			systemctlCalls, dockerLog := recordRestoreOwnershipCallbacks(t, serviceType, stopErr)

			err := mutation.Restore(context.Background())
			if err == nil {
				t.Fatalf("Restore error = %v, want stop failure", err)
			}
			got, viewErr := server.serviceView("api")
			if viewErr != nil {
				t.Fatal(viewErr)
			}
			record := got.AsStruct()
			want := previous.Clone()
			want.ISO.State = string(iso.StateTombstoned)
			want.ISO.LastError = "rolling back staged ISO network"
			if !reflect.DeepEqual(record, want) {
				t.Fatalf("attributed ISO-to-regular rollback marker = %#v", got.AsStruct())
			}
			if serviceType == db.ServiceTypeSystemd && len(*systemctlCalls) == 0 {
				t.Fatal("attributed systemd rollback did not attempt replacement stop")
			}
			if serviceType == db.ServiceTypeDockerCompose {
				assertDockerRestoreCallbackOccurred(t, dockerLog)
			}
		})
	}
}

func TestISOServiceNetworkRestoreHandlesPostPublicationClaimOutcomes(t *testing.T) {
	publicationErr := errors.New("injected post-publication failure")
	for _, tt := range []struct {
		name      string
		committed bool
		wantStop  bool
		wantTomb  bool
	}{
		{name: "committed claim continues fail-closed cleanup", committed: true, wantStop: true, wantTomb: true},
		{name: "uncommitted claim invokes no cleanup", committed: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stubServiceNetworkStaticVerification(t)
			server, plan, _ := newNativeISOServiceNetworkMutationFixture(t, false)
			stubNativeISOServiceNetworkMutationRuntime(t)
			mutation := &isoServiceNetworkMutation{server: server, plan: plan, direction: serviceNetworkRegularToISO}
			if err := mutation.Stage(context.Background()); err != nil {
				t.Fatal(err)
			}
			reserved := mutation.staged.Clone()
			oldMutate := mutateServiceNetworkRestoreData
			mutateServiceNetworkRestoreData = func(store *db.Store, f func(*db.Data) error) (*db.Data, error) {
				if !tt.committed {
					return nil, &db.PostPublicationError{Err: publicationErr}
				}
				updated, err := store.MutateData(f)
				if err != nil {
					return updated, err
				}
				return updated, &db.PostPublicationError{Err: publicationErr, MutationCommitted: true}
			}
			t.Cleanup(func() { mutateServiceNetworkRestoreData = oldMutate })
			baseSystemctl := runISOSystemctlForRuntime
			stopCalls := 0
			runISOSystemctlForRuntime = func(ctx context.Context, args ...string) ([]byte, error) {
				if len(args) != 0 && args[0] == "stop" {
					stopCalls++
					return nil, errors.New("hold attributed rollback marker")
				}
				return baseSystemctl(ctx, args...)
			}

			err := mutation.Restore(context.Background())
			if !errors.Is(err, publicationErr) {
				t.Fatalf("Restore error = %v, want post-publication error", err)
			}
			if got := stopCalls > 0; got != tt.wantStop {
				t.Fatalf("runtime stop invoked = %t, want %t", got, tt.wantStop)
			}
			current, viewErr := server.serviceView("api")
			if viewErr != nil {
				t.Fatal(viewErr)
			}
			if tt.wantTomb {
				if current.ISO().State() != string(iso.StateTombstoned) {
					t.Fatalf("committed claim record = %#v, want tombstone", current.ISO().AsStruct())
				}
			} else if !reflect.DeepEqual(current.AsStruct(), reserved) {
				t.Fatalf("uncommitted claim changed record: got %#v, want %#v", current.AsStruct(), reserved)
			}
		})
	}
}

func TestISOServiceNetworkRestoreClaimsQuarantinedStagedRecord(t *testing.T) {
	server := newTestServer(t)
	boundaryErr := errors.New("injected boundary verification failure")
	previous := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeDockerCompose, Generation: 1, LatestGeneration: 1,
		Network: &db.ServiceNetworkConfig{Modes: []string{"lan"}},
	}
	staged := previous.Clone()
	staged.ISO = &db.ISOAllocation{Kind: string(iso.PayloadCompose), State: string(iso.StateReserved), DesiredModes: []string{"iso"}}
	target := staged.Clone()
	target.Network = &db.ServiceNetworkConfig{Modes: []string{"iso"}}
	quarantined := staged.Clone()
	quarantined.ISO.State = string(iso.StateQuarantined)
	quarantined.ISO.LastError = boundaryErr.Error()
	if err := server.cfg.DB.Set(&db.Data{
		ISOPool:  &db.ISOPool{AggregateRouteState: "conflict", LastConflict: boundaryErr.Error()},
		Services: map[string]*db.Service{"api": quarantined},
	}); err != nil {
		t.Fatal(err)
	}
	mutation := &isoServiceNetworkMutation{
		server: server,
		plan: &serviceNetworkMutationPlan{
			name: "api", previous: previous, desired: db.ServiceNetworkConfig{Modes: []string{"iso"}},
		},
		direction:       serviceNetworkRegularToISO,
		staged:          staged,
		target:          target,
		boundaryFailure: boundaryErr,
	}

	owned, err := mutation.claimReplacementBeforeISORestore()
	if err != nil || !owned {
		t.Fatalf("claim quarantined staged record = owned %t, error %v", owned, err)
	}
	view, err := server.serviceView("api")
	if err != nil {
		t.Fatal(err)
	}
	want := target.Clone()
	want.ISO.State = string(iso.StateTombstoned)
	want.ISO.LastError = "rolling back staged ISO network"
	if !reflect.DeepEqual(view.AsStruct(), want) {
		t.Fatalf("claimed record = %#v, want %#v", view.AsStruct(), want)
	}
}

func TestISOToRegularRestoreHandlesPostPublicationClaimOutcomes(t *testing.T) {
	publicationErr := errors.New("injected ISO-to-regular claim publication failure")
	for _, serviceType := range []db.ServiceType{db.ServiceTypeSystemd, db.ServiceTypeDockerCompose} {
		for _, committed := range []bool{true, false} {
			name := string(serviceType) + "/uncommitted"
			if committed {
				name = string(serviceType) + "/committed"
			}
			t.Run(name, func(t *testing.T) {
				server, mutation, previous := newISOToRegularRestoreOwnershipFixture(t, serviceType)
				oldMutate := mutateServiceNetworkRestoreData
				mutateServiceNetworkRestoreData = func(store *db.Store, f func(*db.Data) error) (*db.Data, error) {
					if !committed {
						return nil, &db.PostPublicationError{Err: publicationErr}
					}
					updated, err := store.MutateData(f)
					if err != nil {
						return updated, err
					}
					return updated, &db.PostPublicationError{Err: publicationErr, MutationCommitted: true}
				}
				t.Cleanup(func() { mutateServiceNetworkRestoreData = oldMutate })
				calls, dockerLog := recordRestoreOwnershipCallbacks(t, serviceType, errors.New("hold attributed ISO-to-regular marker"))

				err := mutation.Restore(context.Background())
				if !errors.Is(err, publicationErr) {
					t.Fatalf("Restore error = %v, want post-publication error", err)
				}
				current, viewErr := server.serviceView("api")
				if viewErr != nil {
					t.Fatal(viewErr)
				}
				if !committed {
					if !reflect.DeepEqual(current.AsStruct(), mutation.target) {
						t.Fatalf("uncommitted claim changed record: got %#v, want %#v", current.AsStruct(), mutation.target)
					}
					assertNoRestoreOwnershipCallbacks(t, *calls, dockerLog)
					return
				}
				want := previous.Clone()
				want.ISO.State = string(iso.StateTombstoned)
				want.ISO.LastError = "rolling back staged ISO network"
				if !reflect.DeepEqual(current.AsStruct(), want) {
					t.Fatalf("committed claim marker = %#v, want %#v", current.AsStruct(), want)
				}
				if serviceType == db.ServiceTypeSystemd && len(*calls) == 0 {
					t.Fatal("committed claim did not continue to the systemd stop")
				}
				if serviceType == db.ServiceTypeDockerCompose {
					assertDockerRestoreCallbackOccurred(t, dockerLog)
				}
			})
		}
	}
}

func TestRegularNetworkRestoreConflictDoesNotStopConcurrentRuntime(t *testing.T) {
	for _, serviceType := range []db.ServiceType{db.ServiceTypeSystemd, db.ServiceTypeDockerCompose} {
		t.Run(string(serviceType), func(t *testing.T) {
			server, mutation, _ := newRegularRestoreOwnershipFixture(t, serviceType)
			concurrent := mutation.target.Clone()
			concurrent.Generation++
			concurrent.LatestGeneration = concurrent.Generation
			if serviceType == db.ServiceTypeSystemd {
				unit := filepath.Join(serviceBinDirForRoot(concurrent.ServiceRoot), "api-concurrent-regular.service")
				if err := os.WriteFile(unit, []byte("[Service]\nExecStart=/bin/concurrent\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				concurrent.Artifacts[db.ArtifactSystemdUnit] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(concurrent.Generation): unit}}
			}
			if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": concurrent.Clone()}}); err != nil {
				t.Fatal(err)
			}
			systemctlCalls, dockerLog := recordRestoreOwnershipCallbacks(t, serviceType, nil)

			err := mutation.Restore(context.Background())
			if err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatalf("Restore error = %v, want exact-record conflict", err)
			}
			got, viewErr := server.serviceView("api")
			if viewErr != nil {
				t.Fatal(viewErr)
			}
			if !reflect.DeepEqual(got.AsStruct(), concurrent) {
				t.Fatalf("concurrent regular record changed: got %#v, want %#v", got.AsStruct(), concurrent)
			}
			assertNoRestoreOwnershipCallbacks(t, *systemctlCalls, dockerLog)
		})
	}
}

func TestRegularNetworkRestoreHandlesPostPublicationClaimOutcomes(t *testing.T) {
	publicationErr := errors.New("injected regular restore publication failure")
	for _, tt := range []struct {
		name      string
		committed bool
		wantStop  bool
	}{
		{name: "committed restore claim continues cleanup", committed: true, wantStop: true},
		{name: "uncommitted restore claim invokes no cleanup", committed: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server, mutation, previous := newRegularRestoreOwnershipFixture(t, db.ServiceTypeSystemd)
			oldStageSystemd := stageRegularNetworkSystemdArtifactsForMutation
			stageRegularNetworkSystemdArtifactsForMutation = func(service *svc.SystemdService) ([]string, error) {
				return service.InstallUnits(), nil
			}
			t.Cleanup(func() { stageRegularNetworkSystemdArtifactsForMutation = oldStageSystemd })
			oldMutate := mutateServiceNetworkRestoreData
			mutateServiceNetworkRestoreData = func(store *db.Store, f func(*db.Data) error) (*db.Data, error) {
				if !tt.committed {
					return nil, &db.PostPublicationError{Err: publicationErr}
				}
				updated, err := store.MutateData(f)
				if err != nil {
					return updated, err
				}
				return updated, &db.PostPublicationError{Err: publicationErr, MutationCommitted: true}
			}
			t.Cleanup(func() { mutateServiceNetworkRestoreData = oldMutate })
			calls, _ := recordRestoreOwnershipCallbacks(t, db.ServiceTypeSystemd, errors.New("hold restore after ownership claim"))

			err := mutation.Restore(context.Background())
			if !errors.Is(err, publicationErr) {
				t.Fatalf("Restore error = %v, want committed publication error", err)
			}
			if got := len(*calls) > 0; got != tt.wantStop {
				t.Fatalf("runtime callback invoked = %t (%v), want %t", got, *calls, tt.wantStop)
			}
			current, viewErr := server.serviceView("api")
			if viewErr != nil {
				t.Fatal(viewErr)
			}
			if tt.committed {
				if !reflect.DeepEqual(current.AsStruct(), previous) {
					t.Fatalf("committed restore claim record = %#v, want previous %#v", current.AsStruct(), previous)
				}
			} else if !reflect.DeepEqual(current.AsStruct(), mutation.target) {
				t.Fatalf("uncommitted restore claim changed record: got %#v, want target %#v", current.AsStruct(), mutation.target)
			}
		})
	}
}

func newISOToRegularRestoreOwnershipFixture(t *testing.T, serviceType db.ServiceType) (*Server, *isoServiceNetworkMutation, *db.Service) {
	t.Helper()
	server, regular, previous := newRegularRestoreOwnershipFixture(t, serviceType)
	previous.Network = &db.ServiceNetworkConfig{Modes: []string{"iso"}}
	if serviceType == db.ServiceTypeSystemd {
		previous.ISO = testISONativeRuntimeAllocation("api", iso.StateReady)
	} else {
		previous.ISO = testISORuntimeAllocation("api", iso.StateReady)
	}
	target := regular.target.Clone()
	target.Network = &db.ServiceNetworkConfig{Modes: []string{"host"}}
	target.SvcNetwork = nil
	target.ISO = nil
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": target.Clone()}}); err != nil {
		t.Fatal(err)
	}
	plan := regular.plan
	plan.previous = previous.Clone()
	plan.currentDesired = db.ServiceNetworkConfig{Modes: []string{"iso"}}
	plan.desired = db.ServiceNetworkConfig{Modes: []string{"host"}}
	regular.plan = plan
	regular.target = target
	mutation := &isoServiceNetworkMutation{
		server: server, plan: plan, direction: serviceNetworkISOToRegular,
		target: target, regular: regular, committed: true,
	}
	return server, mutation, previous
}

func newRegularRestoreOwnershipFixture(t *testing.T, serviceType db.ServiceType) (*Server, *regularServiceNetworkMutation, *db.Service) {
	t.Helper()
	server := newTestServer(t)
	root := server.defaultServiceRootDir("api")
	for _, dir := range []string{serviceBinDirForRoot(root), serviceDataDirForRoot(root)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	previous := &db.Service{
		Name: "api", ServiceType: serviceType, ServiceRoot: root, Generation: 2, LatestGeneration: 2,
		Network: &db.ServiceNetworkConfig{Modes: []string{"host"}}, Artifacts: db.ArtifactStore{},
	}
	target := previous.Clone()
	target.Network = &db.ServiceNetworkConfig{Modes: []string{"svc"}}
	target.SvcNetwork = &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.23")}
	switch serviceType {
	case db.ServiceTypeSystemd:
		previousUnit := filepath.Join(serviceBinDirForRoot(root), "api-previous.service")
		targetUnit := filepath.Join(serviceBinDirForRoot(root), "api-target.service")
		if err := os.WriteFile(previousUnit, []byte("[Service]\nExecStart=/bin/previous\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(targetUnit, []byte("[Service]\nExecStart=/bin/target\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		previous.Artifacts[db.ArtifactSystemdUnit] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(2): previousUnit}}
		target.Artifacts[db.ArtifactSystemdUnit] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(2): targetUnit}}
	case db.ServiceTypeDockerCompose:
		previousCompose := filepath.Join(serviceBinDirForRoot(root), "compose-previous.yml")
		targetCompose := filepath.Join(serviceBinDirForRoot(root), "compose-target.yml")
		if err := os.WriteFile(previousCompose, []byte("services:\n  api:\n    image: previous\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(targetCompose, []byte("services:\n  api:\n    image: target\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		previous.Artifacts[db.ArtifactDockerComposeFile] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(2): previousCompose}}
		target.Artifacts[db.ArtifactDockerComposeFile] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(2): targetCompose}}
	default:
		t.Fatalf("unsupported fixture service type %q", serviceType)
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": target.Clone()}}); err != nil {
		t.Fatal(err)
	}
	plan := &serviceNetworkMutationPlan{
		name: "api", previous: previous.Clone(), currentDesired: *previous.Network.Clone(), desired: *target.Network.Clone(),
		previousRuntime: []serviceIdentityRuntimeUnitState{{Unit: "api.service", Active: false}},
	}
	return server, &regularServiceNetworkMutation{server: server, plan: plan, target: target}, previous
}

func recordRestoreOwnershipCallbacks(t *testing.T, serviceType db.ServiceType, callbackErr error) (*[]string, string) {
	t.Helper()
	oldRun := runRegularNetworkSystemctlForRuntime
	oldActive := inspectRegularNetworkUnitActive
	calls := []string{}
	active := true
	runRegularNetworkSystemctlForRuntime = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if callbackErr != nil && len(args) != 0 && args[0] == "stop" {
			return nil, callbackErr
		}
		if len(args) != 0 && args[0] == "stop" {
			active = false
		}
		return nil, nil
	}
	inspectRegularNetworkUnitActive = func(context.Context, string) (bool, error) { return active, nil }
	t.Cleanup(func() {
		runRegularNetworkSystemctlForRuntime = oldRun
		inspectRegularNetworkUnitActive = oldActive
	})
	if serviceType != db.ServiceTypeDockerCompose {
		return &calls, ""
	}
	logPath := filepath.Join(t.TempDir(), "docker.log")
	dockerDir := t.TempDir()
	exitCode := 0
	if callbackErr != nil {
		exitCode = 1
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + strconv.Quote(logPath) + "\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &calls, logPath
}

func assertNoRestoreOwnershipCallbacks(t *testing.T, systemctlCalls []string, dockerLog string) {
	t.Helper()
	if len(systemctlCalls) != 0 {
		t.Fatalf("systemd/Tailscale callbacks = %v, want none", systemctlCalls)
	}
	if dockerLog == "" {
		return
	}
	raw, err := os.ReadFile(dockerLog)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(raw) != 0 {
		t.Fatalf("Compose callbacks = %q, want none", raw)
	}
}

func assertDockerRestoreCallbackOccurred(t *testing.T, dockerLog string) {
	t.Helper()
	raw, err := os.ReadFile(dockerLog)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("Compose replacement stop callback was not invoked")
	}
}

func TestRunServiceNetworkMutationSkipsFailClosedWithoutRecoveryOwnership(t *testing.T) {
	claimErr := errors.New("injected claim failure")
	for _, mutationKind := range []string{"iso", "regular"} {
		for _, serviceType := range []db.ServiceType{db.ServiceTypeSystemd, db.ServiceTypeDockerCompose} {
			for _, failureMode := range []string{"exact conflict", "pre-publication failure", "uncommitted post-publication failure"} {
				t.Run(mutationKind+"/"+string(serviceType)+"/"+failureMode, func(t *testing.T) {
					server, restore, failClosed, current := newRecoveryOwnershipMutationFixture(t, mutationKind, serviceType)
					expected := current.Clone()
					if failureMode == "exact conflict" {
						expected.Generation++
						expected.LatestGeneration = expected.Generation
						if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": expected.Clone()}}); err != nil {
							t.Fatal(err)
						}
					} else {
						oldMutate := mutateServiceNetworkRestoreData
						mutateServiceNetworkRestoreData = func(*db.Store, func(*db.Data) error) (*db.Data, error) {
							if failureMode == "uncommitted post-publication failure" {
								return nil, &db.PostPublicationError{Err: claimErr}
							}
							return nil, claimErr
						}
						t.Cleanup(func() { mutateServiceNetworkRestoreData = oldMutate })
					}
					systemctlCalls, dockerLog := recordRestoreOwnershipCallbacks(t, serviceType, nil)
					failClosedCalls := 0
					err := runFailedServiceNetworkMutation(restore, func(ctx context.Context) error {
						failClosedCalls++
						return failClosed(ctx)
					})

					requireServiceNetworkRecoveryOwnership(t, err, serviceNetworkRecoveryOwnershipUnowned)
					for _, evidence := range []string{"activate service network replacement: injected activation failure", "restore previous service network:"} {
						if !strings.Contains(err.Error(), evidence) {
							t.Fatalf("run error = %v, want cause and restore evidence %q", err, evidence)
						}
					}
					if failClosedCalls != 0 {
						t.Fatalf("FailClosed calls = %d, want 0 without exact recovery ownership", failClosedCalls)
					}
					got, viewErr := server.serviceView("api")
					if viewErr != nil {
						t.Fatal(viewErr)
					}
					if !reflect.DeepEqual(got.AsStruct(), expected) {
						t.Fatalf("unowned recovery changed database record: got %#v, want %#v", got.AsStruct(), expected)
					}
					assertNoRestoreOwnershipCallbacks(t, *systemctlCalls, dockerLog)
				})
			}
		}
	}
}

func TestRunServiceNetworkMutationAllowsFailClosedWithCommittedRecoveryOwnership(t *testing.T) {
	publicationErr := errors.New("injected committed claim publication failure")
	stopErr := errors.New("injected owned replacement stop failure")
	for _, mutationKind := range []string{"iso", "regular"} {
		for _, serviceType := range []db.ServiceType{db.ServiceTypeSystemd, db.ServiceTypeDockerCompose} {
			t.Run(mutationKind+"/"+string(serviceType), func(t *testing.T) {
				server, restore, failClosed, _ := newRecoveryOwnershipMutationFixture(t, mutationKind, serviceType)
				oldMutate := mutateServiceNetworkRestoreData
				mutateServiceNetworkRestoreData = func(store *db.Store, f func(*db.Data) error) (*db.Data, error) {
					updated, err := store.MutateData(f)
					if err != nil {
						return updated, err
					}
					return updated, &db.PostPublicationError{Err: publicationErr, MutationCommitted: true}
				}
				t.Cleanup(func() { mutateServiceNetworkRestoreData = oldMutate })
				systemctlCalls, dockerLog := recordRestoreOwnershipCallbacks(t, serviceType, stopErr)
				failClosedCalls := 0
				err := runFailedServiceNetworkMutation(restore, func(ctx context.Context) error {
					failClosedCalls++
					return failClosed(ctx)
				})

				requireServiceNetworkRecoveryOwnership(t, err, serviceNetworkRecoveryOwnershipOwned)
				if !errors.Is(err, publicationErr) || failClosedCalls != 1 {
					t.Fatalf("run error = %v, FailClosed calls = %d, want committed warning and one fail-closed", err, failClosedCalls)
				}
				if serviceType == db.ServiceTypeSystemd && len(*systemctlCalls) < 2 {
					t.Fatalf("owned systemd recovery callbacks = %v, want restore and fail-closed attempts", *systemctlCalls)
				}
				if serviceType == db.ServiceTypeDockerCompose {
					assertDockerRestoreCallbackOccurred(t, dockerLog)
				}
				if _, viewErr := server.serviceView("api"); viewErr != nil {
					t.Fatal(viewErr)
				}
			})
		}
	}
}

func TestRegularNetworkRestoreStopFailureDefersArtifactsAndRuntimeToFailClosed(t *testing.T) {
	_, mutation, _ := newRegularRestoreOwnershipFixture(t, db.ServiceTypeSystemd)
	stagedPath := attachRegularRecoveryStagedArtifact(t, mutation)
	stopErr := errors.New("injected target stop failure")
	stopCalls, _ := recordRestoreOwnershipCallbacks(t, db.ServiceTypeSystemd, stopErr)
	oldStage := stageRegularNetworkSystemdArtifactsForMutation
	restoreStarts := 0
	stageRegularNetworkSystemdArtifactsForMutation = func(service *svc.SystemdService) ([]string, error) {
		restoreStarts++
		return service.InstallUnits(), nil
	}
	t.Cleanup(func() { stageRegularNetworkSystemdArtifactsForMutation = oldStage })

	err := mutation.Restore(context.Background())
	requireServiceNetworkRecoveryOwnership(t, err, serviceNetworkRecoveryOwnershipOwned)
	if !errors.Is(err, stopErr) {
		t.Fatalf("Restore error = %v, want stop failure", err)
	}
	if _, err := os.Stat(stagedPath); err != nil {
		t.Fatalf("Restore removed staged artifact before replacement stopped: %v", err)
	}
	if restoreStarts != 0 {
		t.Fatalf("prior runtime restore starts = %d, want 0 after replacement stop failure", restoreStarts)
	}

	if err := mutation.FailClosed(context.Background()); err == nil {
		t.Fatal("FailClosed error = nil, want injected exhaustive stop failures")
	}
	joinedCalls := strings.Join(*stopCalls, "\n")
	if got := strings.Count(joinedCalls, "stop api.service"); got < 2 {
		t.Fatalf("Restore and FailClosed workload stop calls = %v, want both attempts", *stopCalls)
	}
	if _, err := os.Stat(stagedPath); !os.IsNotExist(err) {
		t.Fatalf("FailClosed did not remove staged artifact after exhaustive stops: %v", err)
	}
}

func TestRegularNetworkFailClosedAttributesBeforeCallbacksAndCleansArtifactsLast(t *testing.T) {
	_, mutation, _ := newRegularRestoreOwnershipFixture(t, db.ServiceTypeDockerCompose)
	stagedPath := attachRegularRecoveryStagedArtifact(t, mutation)
	oldRun := runRegularNetworkSystemctlForRuntime
	oldActive := inspectRegularNetworkUnitActive
	systemctlCalls := []string{}
	runRegularNetworkSystemctlForRuntime = func(_ context.Context, args ...string) ([]byte, error) {
		systemctlCalls = append(systemctlCalls, strings.Join(args, " "))
		if len(args) != 0 && args[0] == "stop" {
			return nil, errors.New("injected auxiliary stop failure")
		}
		return nil, nil
	}
	inspectRegularNetworkUnitActive = func(context.Context, string) (bool, error) { return true, nil }
	t.Cleanup(func() {
		runRegularNetworkSystemctlForRuntime = oldRun
		inspectRegularNetworkUnitActive = oldActive
	})
	dockerLog := installArtifactAwareDocker(t, stagedPath, 0)
	oldVerify := verifyRegularNetworkComposeProjectAbsentForMutation
	verifyCalls := 0
	verifyRegularNetworkComposeProjectAbsentForMutation = func(context.Context, *svc.DockerComposeService) error {
		verifyCalls++
		return nil
	}
	t.Cleanup(func() { verifyRegularNetworkComposeProjectAbsentForMutation = oldVerify })

	err := mutation.FailClosed(context.Background())
	if err == nil {
		t.Fatal("FailClosed error = nil, want joined auxiliary stop failure")
	}
	raw, readErr := os.ReadFile(dockerLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	logText := string(raw)
	for _, composePath := range []string{"compose-target.yml", "compose-previous.yml"} {
		if !strings.Contains(logText, composePath) {
			t.Fatalf("Compose fail-closed log = %q, missing %s shutdown", logText, composePath)
		}
	}
	if strings.Contains(logText, "artifact-absent") {
		t.Fatalf("Compose callback observed staged artifact deleted before shutdown: %q", logText)
	}
	if len(systemctlCalls) == 0 || len(strings.Split(strings.TrimSpace(logText), "\n")) < 2 || verifyCalls != 2 {
		t.Fatalf("FailClosed did not continue after first stop error: systemctl=%v docker=%q", systemctlCalls, logText)
	}
	if _, err := os.Stat(stagedPath); !os.IsNotExist(err) {
		t.Fatalf("staged artifact remains after exhaustive fail-closed shutdown: %v", err)
	}
}

func TestRegularNetworkFailClosedPreservesConcurrentReplacementAfterOwnedRestore(t *testing.T) {
	stopErr := errors.New("injected replacement stop failure")
	for _, serviceType := range []db.ServiceType{db.ServiceTypeSystemd, db.ServiceTypeDockerCompose} {
		t.Run(string(serviceType), func(t *testing.T) {
			server, mutation, _ := newRegularRestoreOwnershipFixture(t, serviceType)
			stagedPath := attachRegularRecoveryStagedArtifact(t, mutation)
			systemctlCalls, dockerLog := recordRestoreOwnershipCallbacks(t, serviceType, stopErr)
			var concurrent *db.Service
			callbacksBeforeFailClosed := 0
			failClosedCalls := 0
			err := runFailedServiceNetworkMutation(func(ctx context.Context) error {
				restoreErr := mutation.Restore(ctx)
				requireServiceNetworkRecoveryOwnership(t, restoreErr, serviceNetworkRecoveryOwnershipOwned)
				concurrent = mutation.target.Clone()
				concurrent.Generation++
				concurrent.LatestGeneration = concurrent.Generation
				if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": concurrent.Clone()}}); err != nil {
					t.Fatal(err)
				}
				callbacksBeforeFailClosed = restoreOwnershipCallbackCount(t, *systemctlCalls, dockerLog)
				return restoreErr
			}, func(ctx context.Context) error {
				failClosedCalls++
				return mutation.FailClosed(ctx)
			})

			requireServiceNetworkRecoveryOwnership(t, err, serviceNetworkRecoveryOwnershipOwned)
			if failClosedCalls != 1 {
				t.Fatalf("FailClosed calls = %d, want 1 for owned restore failure", failClosedCalls)
			}
			if got := restoreOwnershipCallbackCount(t, *systemctlCalls, dockerLog); got != callbacksBeforeFailClosed {
				t.Fatalf("concurrent runtime callbacks after fail-closed = %d, want unchanged %d", got, callbacksBeforeFailClosed)
			}
			got, viewErr := server.serviceView("api")
			if viewErr != nil {
				t.Fatal(viewErr)
			}
			if !reflect.DeepEqual(got.AsStruct(), concurrent) {
				t.Fatalf("FailClosed changed concurrent record: got %#v, want %#v", got.AsStruct(), concurrent)
			}
			if _, err := os.Stat(stagedPath); err != nil {
				t.Fatalf("unattributed FailClosed removed staged artifact: %v", err)
			}
		})
	}
}

func TestISOToRegularFailClosedRetriesAndVerifiesRegularComposeTarget(t *testing.T) {
	server, mutation, _ := newISOToRegularRestoreOwnershipFixture(t, db.ServiceTypeDockerCompose)
	dockerLog := installArtifactAwareDocker(t, "", 2)
	oldVerify := verifyRegularNetworkComposeProjectAbsentForMutation
	verifyCalls := 0
	verifyRegularNetworkComposeProjectAbsentForMutation = func(context.Context, *svc.DockerComposeService) error {
		verifyCalls++
		return nil
	}
	t.Cleanup(func() { verifyRegularNetworkComposeProjectAbsentForMutation = oldVerify })
	oldActive := inspectRegularNetworkUnitActive
	inspectRegularNetworkUnitActive = func(context.Context, string) (bool, error) { return false, nil }
	t.Cleanup(func() { inspectRegularNetworkUnitActive = oldActive })

	err := runFailedServiceNetworkMutation(mutation.Restore, mutation.FailClosed)
	requireServiceNetworkRecoveryOwnership(t, err, serviceNetworkRecoveryOwnershipOwned)
	raw, readErr := os.ReadFile(dockerLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	logText := string(raw)
	targetCompose, _ := mutation.target.Artifacts.Gen(db.ArtifactDockerComposeFile, mutation.target.Generation)
	if got := strings.Count(logText, targetCompose); got < 2 {
		t.Fatalf("regular Compose target callbacks = %d in %q, want failed Restore stop plus FailClosed retry", got, logText)
	}
	if verifyCalls != 1 {
		t.Fatalf("regular Compose target absence verifications = %d, want 1", verifyCalls)
	}
	view, viewErr := server.serviceView("api")
	if viewErr != nil {
		t.Fatal(viewErr)
	}
	if view.ISO().State() != string(iso.StateTombstoned) {
		t.Fatalf("ISO-to-regular fail-closed record = %#v, want tombstone", view.AsStruct())
	}
}

func newRecoveryOwnershipMutationFixture(t *testing.T, mutationKind string, serviceType db.ServiceType) (*Server, func(context.Context) error, func(context.Context) error, *db.Service) {
	t.Helper()
	if mutationKind == "iso" {
		server, mutation, _ := newISOToRegularRestoreOwnershipFixture(t, serviceType)
		return server, mutation.Restore, mutation.FailClosed, mutation.target
	}
	server, mutation, _ := newRegularRestoreOwnershipFixture(t, serviceType)
	return server, mutation.Restore, mutation.FailClosed, mutation.target
}

func runFailedServiceNetworkMutation(restore, failClosed func(context.Context) error) error {
	activateErr := errors.New("injected activation failure")
	return runServiceNetworkMutation(context.Background(), &functionServiceNetworkMutationSteps{
		stage:        func(context.Context) error { return nil },
		stopPrevious: func(context.Context) error { return nil },
		activate:     func(context.Context) error { return activateErr },
		verify:       func(context.Context) error { return nil },
		commit:       func(context.Context) error { return nil },
		restore:      restore,
		failClosed:   failClosed,
	})
}

func requireServiceNetworkRecoveryOwnership(t *testing.T, err error, want serviceNetworkRecoveryOwnership) {
	t.Helper()
	var recoveryErr *serviceNetworkRecoveryError
	if !errors.As(err, &recoveryErr) {
		t.Fatalf("error = %v, want typed service network recovery ownership", err)
	}
	if recoveryErr.Ownership != want {
		t.Fatalf("recovery ownership = %v, want %v", recoveryErr.Ownership, want)
	}
}

func attachRegularRecoveryStagedArtifact(t *testing.T, mutation *regularServiceNetworkMutation) string {
	t.Helper()
	path := filepath.Join(serviceDataDirForRoot(mutation.plan.previous.ServiceRoot), "staged-recovery.env")
	if err := os.WriteFile(path, []byte("staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	txn, err := beginRegularNetworkArtifactTransaction(mutation.plan.previous.ServiceRoot, mutation.plan.previous)
	if err != nil {
		t.Fatal(err)
	}
	if err := txn.registerStagedPath(path); err != nil {
		t.Fatal(err)
	}
	mutation.plan.artifactTxn = txn
	return path
}

func installArtifactAwareDocker(t *testing.T, artifactPath string, failCalls int) string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "docker-order.log")
	countPath := filepath.Join(t.TempDir(), "docker-count")
	dockerDir := t.TempDir()
	artifactCheck := "artifact-present"
	if artifactPath != "" {
		artifactCheck = "$(if [ -e " + strconv.Quote(artifactPath) + " ]; then printf artifact-present; else printf artifact-absent; fi)"
	}
	script := "#!/bin/sh\n" +
		"count=0\n" +
		"if [ -f " + strconv.Quote(countPath) + " ]; then count=$(sed -n '1p' " + strconv.Quote(countPath) + "); fi\n" +
		"count=$((count + 1))\n" +
		"printf '%s\\n' \"$count\" > " + strconv.Quote(countPath) + "\n" +
		"printf '%s %s\\n' \"" + artifactCheck + "\" \"$*\" >> " + strconv.Quote(logPath) + "\n" +
		"if [ \"$count\" -le " + strconv.Itoa(failCalls) + " ]; then exit 1; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dockerDir, "docker"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dockerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func restoreOwnershipCallbackCount(t *testing.T, systemctlCalls []string, dockerLog string) int {
	t.Helper()
	count := len(systemctlCalls)
	if dockerLog == "" {
		return count
	}
	raw, err := os.ReadFile(dockerLog)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return count
	}
	return count + len(strings.Split(trimmed, "\n"))
}

func TestNativeISOServiceNetworkMutationFailClosedRetainsTombstone(t *testing.T) {
	server, plan, _ := newNativeISOServiceNetworkMutationFixture(t, false)
	stubNativeISOServiceNetworkMutationRuntime(t)
	baseSystemctl := runISOSystemctlForRuntime
	stopCalls := 0
	runISOSystemctlForRuntime = func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) != 0 && args[0] == "stop" {
			stopCalls++
		}
		return baseSystemctl(ctx, args...)
	}
	allocation := newDBISOAllocation("api", isoReservationRequest{Kind: iso.PayloadNative, Modes: []string{"iso"}}, netip.MustParsePrefix("172.30.0.0/30"))
	target := plan.previous.Clone()
	target.Network = &db.ServiceNetworkConfig{Modes: []string{"iso"}}
	target.ISO = allocation
	if err := server.cfg.DB.Set(&db.Data{
		ISOPool:  &db.ISOPool{Prefix: netip.MustParsePrefix("172.30.0.0/16"), AllocatorVersion: iso.AllocatorVersion, PolicyVersion: iso.PolicyVersion},
		Services: map[string]*db.Service{"api": plan.previous.Clone()},
	}); err != nil {
		t.Fatal(err)
	}
	mutation := &isoServiceNetworkMutation{server: server, plan: plan, target: target, direction: serviceNetworkRegularToISO}
	if err := mutation.FailClosed(context.Background()); err != nil {
		t.Fatal(err)
	}
	view, err := server.serviceView("api")
	if err != nil {
		t.Fatal(err)
	}
	if view.ISO().State() != string(iso.StateTombstoned) || !strings.Contains(view.ISO().LastError(), "restoration failed") {
		t.Fatalf("fail-closed ISO state = %#v", view.ISO().AsStruct())
	}
	if stopCalls == 0 {
		t.Fatal("attributable fail-closed tombstone did not stop the ISO runtime")
	}
}

func newNativeISOServiceNetworkMutationFixture(t *testing.T, currentISO bool) (*Server, *serviceNetworkMutationPlan, string) {
	t.Helper()
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "api")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(serviceBinDirForRoot(root), "api-4.service")
	if err := os.WriteFile(unit, []byte("[Unit]\nDescription=api\n\n[Service]\nExecStart=/srv/api/bin/api-4\nUser=root\nGroup=root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root, Generation: 4, LatestGeneration: 6,
		Network: &db.ServiceNetworkConfig{Modes: []string{"host"}},
		Artifacts: db.ArtifactStore{
			db.ArtifactBinary:      {Refs: map[db.ArtifactRef]string{db.Gen(4): "/payload/api-4", "latest": "/payload/api-6"}},
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(4): unit, "latest": unit}},
		},
	}
	desired := db.ServiceNetworkConfig{Modes: []string{"iso"}}
	if currentISO {
		previous.Network = &db.ServiceNetworkConfig{Modes: []string{"iso"}}
		previous.ISO = newDBISOAllocation("api", isoReservationRequest{Kind: iso.PayloadNative, Modes: []string{"iso"}}, netip.MustParsePrefix("172.30.0.0/30"))
		previous.ISO.State = string(iso.StateReady)
		desired = db.ServiceNetworkConfig{Modes: []string{"host"}}
	}
	if err := server.cfg.DB.Set(&db.Data{
		ISOPool:  &db.ISOPool{Prefix: netip.MustParsePrefix("172.30.0.0/16"), AllocatorVersion: iso.AllocatorVersion, PolicyVersion: iso.PolicyVersion},
		Services: map[string]*db.Service{"api": previous.Clone()},
	}); err != nil {
		t.Fatal(err)
	}
	plan := &serviceNetworkMutationPlan{
		name: "api", previous: previous.Clone(), currentDesired: *previous.Network.Clone(), desired: desired,
		network:            NetworkOpts{Interfaces: strings.Join(desired.Modes, ","), Modes: slices.Clone(desired.Modes), ISO: slices.Contains(desired.Modes, "iso")},
		previousRuntime:    []serviceIdentityRuntimeUnitState{{Unit: "api.service", Active: false}},
		previousEnablement: []serviceIdentityUnitEnablement{{Unit: "api.service", Enabled: false}},
	}
	return server, plan, unit
}

func stubServiceNetworkStaticVerification(t *testing.T) {
	t.Helper()
	previous := verifyGeneratedSystemdUnitForSandboxMutation
	calls := 0
	t.Cleanup(func() {
		verifyGeneratedSystemdUnitForSandboxMutation = previous
		if calls == 0 {
			t.Error("service network mutation did not statically verify its generated unit")
		}
	})
	verifyGeneratedSystemdUnitForSandboxMutation = func(ctx context.Context, path string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read generated unit at static-verifier seam: %w", err)
		}
		if !strings.Contains(string(raw), "[Service]") {
			return fmt.Errorf("generated unit at static-verifier seam has no Service section")
		}
		calls++
		return nil
	}
}

func stubNativeISOServiceNetworkMutationRuntime(t *testing.T) {
	t.Helper()
	oldRegularSystemctl := runRegularNetworkSystemctlForRuntime
	oldISOSystemctl := runISOSystemctlForRuntime
	oldCatchSystemctl := catchSystemctl
	oldCatchActive := catchSystemdUnitActive
	oldActive := inspectRegularNetworkUnitActive
	oldEnabled := inspectRegularNetworkUnitEnabled
	oldDetect := detectISOFirewallBackendForRuntime
	oldEnsurePolicy := ensureISOPolicyForRuntime
	oldEnsureTopology := ensureISOTopologyForRuntime
	oldVerifyTopology := verifyISOTopologyForRuntime
	oldVerifyAbsent := verifyISOTopologyAbsentForRuntime
	oldRemoveTopology := removeISOTopologyForRuntime
	oldAcquire := acquireISOOperationLockForRuntime
	t.Cleanup(func() {
		runRegularNetworkSystemctlForRuntime = oldRegularSystemctl
		runISOSystemctlForRuntime = oldISOSystemctl
		catchSystemctl = oldCatchSystemctl
		catchSystemdUnitActive = oldCatchActive
		inspectRegularNetworkUnitActive = oldActive
		inspectRegularNetworkUnitEnabled = oldEnabled
		detectISOFirewallBackendForRuntime = oldDetect
		ensureISOPolicyForRuntime = oldEnsurePolicy
		ensureISOTopologyForRuntime = oldEnsureTopology
		verifyISOTopologyForRuntime = oldVerifyTopology
		verifyISOTopologyAbsentForRuntime = oldVerifyAbsent
		removeISOTopologyForRuntime = oldRemoveTopology
		acquireISOOperationLockForRuntime = oldAcquire
	})
	runRegularNetworkSystemctlForRuntime = func(context.Context, ...string) ([]byte, error) { return nil, nil }
	runISOSystemctlForRuntime = func(_ context.Context, args ...string) ([]byte, error) {
		if slices.Contains(args, "show") {
			return []byte("inactive\n"), nil
		}
		return nil, nil
	}
	active := make(map[string]bool)
	catchSystemctl = func(args ...string) error {
		if len(args) == 2 {
			switch args[0] {
			case "start", "restart", "try-restart":
				active[args[1]] = true
			case "stop":
				active[args[1]] = false
			}
		}
		return nil
	}
	catchSystemdUnitActive = func(unit string) bool { return active[unit] }
	inspectRegularNetworkUnitActive = func(context.Context, string) (bool, error) { return false, nil }
	inspectRegularNetworkUnitEnabled = func(context.Context, string) (bool, error) { return false, nil }
	detectISOFirewallBackendForRuntime = func() (netns.FirewallBackend, error) { return netns.BackendNFT, nil }
	ensureISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error { return nil }
	ensureISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error { return nil }
	verifyISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error { return nil }
	verifyISOTopologyAbsentForRuntime = func(context.Context, netns.ISOTopologySpec) error { return nil }
	removeISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error { return nil }
	acquireISOOperationLockForRuntime = func(context.Context, string) (func(), error) { return func() {}, nil }
}

func TestStageComposeISOServiceNetworkReplacementPreservesStableAllocationAndFreshOverlay(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "app")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(serviceBinDirForRoot(root), "compose.yml")
	if err := os.WriteFile(base, []byte("services:\n  api:\n    image: nginx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldOverlay := filepath.Join(serviceBinDirForRoot(root), "old-iso-network.yml")
	if err := os.WriteFile(oldOverlay, []byte("old overlay\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	allocation := testISORuntimeAllocation("app", iso.StateReady)
	previous := &db.Service{
		Name: "app", ServiceType: db.ServiceTypeDockerCompose, ServiceRoot: root, Generation: 4, LatestGeneration: 6,
		Network: &db.ServiceNetworkConfig{Modes: []string{"iso"}}, ISO: allocation,
		Artifacts: db.ArtifactStore{
			db.ArtifactDockerComposeFile:    {Refs: map[db.ArtifactRef]string{db.Gen(4): base, "latest": base}},
			db.ArtifactDockerComposeNetwork: {Refs: map[db.ArtifactRef]string{db.Gen(4): oldOverlay, "latest": oldOverlay}},
		},
	}
	if err := server.cfg.DB.Set(&db.Data{
		ISOPool:  &db.ISOPool{Prefix: netip.MustParsePrefix("172.30.0.0/16"), AllocatorVersion: iso.AllocatorVersion, PolicyVersion: iso.PolicyVersion},
		Services: map[string]*db.Service{"app": previous},
	}); err != nil {
		t.Fatal(err)
	}
	oldResolve := resolveISOComposeForNetworkMutation
	t.Cleanup(func() { resolveISOComposeForNetworkMutation = oldResolve })
	resolveISOComposeForNetworkMutation = func(_ context.Context, opts svc.ComposeResolveOptions) ([]byte, error) {
		if len(opts.Files) == 1 {
			return []byte(`{"name":"catch-app","networks":{"default":{"name":"catch-app_default","ipam":{}}},"services":{"api":{"image":"nginx","networks":{"default":null}}}}`), nil
		}
		view, err := server.serviceView("app")
		if err != nil {
			return nil, err
		}
		got := view.ISO().AsStruct()
		return []byte(fmt.Sprintf(`{
			"name":"catch-app",
			"networks":{"default":{"name":"catch-app_default","driver":"yeet","driver_opts":{"dev.catchit.mode":"iso","dev.catchit.netns":"/var/run/netns/%s"},"enable_ipv6":false,"ipam":{"config":[{"subnet":"%s","gateway":"%s"}]}}},
			"services":{"api":{"image":"nginx","dns":["%s"],"networks":{"default":{"ipv4_address":"%s"}}}}
		}`, got.NetNS, got.Project, got.Gateway, got.Gateway, got.Components["api"].Address)), nil
	}
	plan := &serviceNetworkMutationPlan{
		name: "app", previous: previous.Clone(), currentDesired: db.ServiceNetworkConfig{Modes: []string{"iso"}},
		desired: db.ServiceNetworkConfig{Modes: []string{"iso"}}, network: NetworkOpts{Interfaces: "iso", Modes: []string{"iso"}, ISO: true},
	}
	target, _, err := server.stageISOServiceNetworkReplacement(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if target.Generation != 4 || target.LatestGeneration != 6 {
		t.Fatalf("generation/latest = %d/%d, want 4/6", target.Generation, target.LatestGeneration)
	}
	if target.ISO.Link != allocation.Link || target.ISO.Project != allocation.Project {
		t.Fatalf("stable allocation changed: before %#v after %#v", allocation, target.ISO)
	}
	stagedOverlay, ok := target.Artifacts.Gen(db.ArtifactDockerComposeNetwork, target.Generation)
	if !ok || stagedOverlay == oldOverlay {
		t.Fatalf("staged overlay = %q, want fresh path", stagedOverlay)
	}
	if raw, err := os.ReadFile(oldOverlay); err != nil || string(raw) != "old overlay\n" {
		t.Fatalf("old overlay changed during staging: %q, %v", raw, err)
	}
	if _, tracked := plan.artifactTxn.stagedPaths[stagedOverlay]; !tracked {
		t.Fatalf("fresh overlay %q is not transaction-owned", stagedOverlay)
	}
}

func TestServiceSetNetworkAndRunAsBuildOneIdentityMigrationTarget(t *testing.T) {
	stubServiceNetworkStaticVerification(t)
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "api")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(serviceBinDirForRoot(root), "api-3.service")
	if err := os.WriteFile(unit, []byte("[Unit]\n\n[Service]\nExecStart=/srv/api/bin/api-3\nUser=root\nGroup=root\n\n[Install]\nWantedBy=multi-user.target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root, Generation: 3, LatestGeneration: 5,
		Network:   &db.ServiceNetworkConfig{Modes: []string{"host"}},
		Artifacts: db.ArtifactStore{db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(3): unit, "latest": unit}}},
	}
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"api": previous}}); err != nil {
		t.Fatal(err)
	}
	oldRunning := isServiceRunningForNetworkMutation
	oldMigration := migrateServiceNetworkIdentityLocked
	t.Cleanup(func() {
		isServiceRunningForNetworkMutation = oldRunning
		migrateServiceNetworkIdentityLocked = oldMigration
	})
	isServiceRunningForNetworkMutation = func(*Server, string) (bool, error) { return false, nil }
	var request serviceIdentityMigrationRequest
	migrateServiceNetworkIdentityLocked = func(_ context.Context, _ *Server, got serviceIdentityMigrationRequest, _ io.Writer) (serviceIdentityMigrationResult, error) {
		request = got
		return serviceIdentityMigrationResult{}, nil
	}
	runAs := strconv.Itoa(os.Geteuid()) + ":" + strconv.Itoa(os.Getegid())
	err := server.updateServiceNetworkLocked(context.Background(), "api", cli.ServiceSetFlags{
		RunAs: runAs, RunAsSet: true, Net: "host", NetSet: true,
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if request.TargetService == nil || request.TargetService.Generation != 3 || request.TargetService.LatestGeneration != 5 {
		t.Fatalf("migration target = %#v", request.TargetService)
	}
	if request.TargetService.Network == nil || !reflect.DeepEqual(request.TargetService.Network.Modes, []string{"host"}) {
		t.Fatalf("migration target network = %#v", request.TargetService.Network)
	}
	if request.TargetService.Identity == nil || request.TargetService.Identity.UID != uint32(os.Geteuid()) || request.TargetService.Identity.GID != uint32(os.Getegid()) {
		t.Fatalf("migration target identity = %#v", request.TargetService.Identity)
	}
	if request.StageGeneration == nil || request.ReplacementUnit == "" || request.InstallGeneration != nil {
		t.Fatalf("migration request did not stage one replacement: %#v", request)
	}
	if !strings.Contains(request.ReplacementUnit, "User="+strconv.Itoa(os.Geteuid())) || !strings.Contains(request.ReplacementUnit, "Group="+strconv.Itoa(os.Getegid())) {
		t.Fatalf("replacement unit did not carry target identity:\n%s", request.ReplacementUnit)
	}
}

func TestISOToRegularCombinedIdentitySandboxPreflightPrecedesRuntimeBoundary(t *testing.T) {
	server, plan, originalUnit := newNativeISOServiceNetworkMutationFixture(t, true)
	stubNativeISOServiceNetworkMutationRuntime(t)
	payload := filepath.Join(serviceRunDirForRoot(plan.previous.ServiceRoot), "api-4")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolver := filepath.Join(serviceRunDirForRoot(plan.previous.ServiceRoot), "target-resolv.conf")
	if err := os.WriteFile(resolver, []byte("nameserver 1.1.1.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := db.ServiceIdentity{RequestedUser: "root", RequestedGroup: "root"}
	policy := serviceSandboxPolicy{State: "on"}
	plan.previous.Artifacts[db.ArtifactBinary].Refs[db.Gen(plan.previous.Generation)] = payload
	plan.previous.Artifacts[db.ArtifactNetNSResolv] = &db.Artifact{Refs: map[db.ArtifactRef]string{
		db.Gen(plan.previous.Generation): resolver,
	}}
	plan.previous.Identity = &identity
	plan.previous.Sandbox = &db.ServiceSandboxStore{Refs: map[db.ArtifactRef]*db.ServiceSandboxPolicy{
		db.Gen(plan.previous.Generation): serviceSandboxPolicyToDB(policy),
	}}
	dataDir := serviceDataDirForRoot(plan.previous.ServiceRoot)
	baseUnit := "[Unit]\nDescription=api\n\n[Service]\nExecStart=" + payload + " --serve\nUser=root\nGroup=root\n" +
		"WorkingDirectory=" + dataDir + "\nEnvironment=HOME=" + dataDir + " USER=root LOGNAME=root SHELL=/bin/sh\n" +
		"NetworkNamespacePath=/var/run/netns/" + plan.previous.ISO.NetNS + "\n"
	sandboxPlan, err := buildValidatedServiceSandboxPlan(serviceSandboxPlanRequest{
		Service: plan.name, Policy: policy, Payload: payload, DataDir: dataDir, ResolverSource: resolver,
		Hostname: plan.name,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonicalUnit, _, err := renderNativeSandboxUnitWithPlan(baseUnit, nativeSandboxUnitRequest{
		CurrentPolicy: serviceSandboxPolicy{State: "legacy"}, TargetPolicy: policy, Identity: identity,
		Payload: payload, DataDir: dataDir, Resolver: resolver, Hostname: plan.name,
	}, &sandboxPlan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(originalUnit, []byte(canonicalUnit), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := server.cfg.DB.MutateData(func(data *db.Data) error {
		data.Services[plan.name] = plan.previous.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	mutation := &isoServiceNetworkMutation{server: server, plan: plan, direction: serviceNetworkISOToRegular}
	plan.deferSandbox = true
	operation := &isoNetworkIdentityMutation{
		server: server, ctx: context.Background(), plan: plan,
		flags: cli.ServiceSetFlags{
			RunAs: strconv.Itoa(os.Geteuid()) + ":" + strconv.Itoa(os.Getegid()), RunAsSet: true,
		},
		direction: serviceNetworkISOToRegular, mutation: mutation,
	}
	if err := mutation.Stage(context.Background()); err != nil {
		t.Fatal(err)
	}
	setRegularNetworkTargetArtifact(mutation.target, db.ArtifactNetNSResolv, resolver)

	oldEnsure, oldValidate := ensureBubblewrapForServiceSandboxMutation, validateServiceSandboxPolicyForMutation
	oldProbe, oldVerify := probeServiceSandboxForMutation, verifyGeneratedSystemdUnitForSandboxMutation
	oldMigration, baseSystemctl := migrateServiceNetworkIdentityLocked, runISOSystemctlForRuntime
	oldStageSystemd := stageRegularNetworkSystemdArtifactsForMutation
	t.Cleanup(func() {
		ensureBubblewrapForServiceSandboxMutation = oldEnsure
		validateServiceSandboxPolicyForMutation = oldValidate
		probeServiceSandboxForMutation = oldProbe
		verifyGeneratedSystemdUnitForSandboxMutation = oldVerify
		migrateServiceNetworkIdentityLocked = oldMigration
		runISOSystemctlForRuntime = baseSystemctl
		stageRegularNetworkSystemdArtifactsForMutation = oldStageSystemd
	})
	var events []string
	ensureBubblewrapForServiceSandboxMutation = func(context.Context) error {
		events = append(events, "ensure")
		return nil
	}
	validateServiceSandboxPolicyForMutation = func(req serviceSandboxPlanRequest, active bool) (serviceSandboxPolicy, error) {
		events = append(events, "validate")
		if !active || req.ResolverSource != resolver {
			t.Fatalf("combined validation active/resolver = %t/%q, want true/%q", active, req.ResolverSource, resolver)
		}
		return req.Policy, nil
	}
	probeServiceSandboxForMutation = func(context.Context, serviceSandboxPlan, uint32, uint32) error {
		events = append(events, "probe")
		return nil
	}
	verifyGeneratedSystemdUnitForSandboxMutation = func(context.Context, string) error {
		events = append(events, "verify")
		return nil
	}
	postBoundary := errors.New("injected post-boundary identity migration failure")
	migrateServiceNetworkIdentityLocked = func(context.Context, *Server, serviceIdentityMigrationRequest, io.Writer) (serviceIdentityMigrationResult, error) {
		events = append(events, "migrate")
		return serviceIdentityMigrationResult{}, postBoundary
	}
	runISOSystemctlForRuntime = func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) != 0 && args[0] == "stop" {
			events = append(events, "stop")
		}
		return baseSystemctl(ctx, args...)
	}
	stageRegularNetworkSystemdArtifactsForMutation = func(service *svc.SystemdService) ([]string, error) {
		return service.InstallUnits(), nil
	}

	err = operation.runAfterStage()
	operation.finish(&err)
	if !errors.Is(err, postBoundary) {
		t.Fatalf("combined ISO-to-regular error = %v, want %v", err, postBoundary)
	}
	wantPrefix := []string{"ensure", "validate", "probe", "verify", "stop", "migrate"}
	if len(events) < len(wantPrefix) || !reflect.DeepEqual(events[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("combined ISO-to-regular events = %v, want prefix %v", events, wantPrefix)
	}
	current, viewErr := server.serviceView(plan.name)
	if viewErr != nil {
		t.Fatal(viewErr)
	}
	wantJSON, wantErr := json.MarshalIndent(plan.previous, "", "  ")
	gotJSON, gotErr := json.MarshalIndent(current.AsStruct(), "", "  ")
	if wantErr != nil || gotErr != nil {
		t.Fatalf("render combined recovery records: want=%v got=%v", wantErr, gotErr)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("combined ISO-to-regular recovery mismatch:\nwant %s\n got %s", wantJSON, gotJSON)
	}
	if _, statErr := os.Stat(originalUnit); statErr != nil {
		t.Fatalf("combined ISO-to-regular recovery lost previous unit: %v", statErr)
	}
}

func TestRegularToISOCombinedIdentitySandboxPreflightPrecedesReservationAndRuntime(t *testing.T) {
	preflightFailure := errors.New("injected final sandbox static verification failure")
	for _, tt := range []struct {
		name                  string
		verifyErr             error
		concurrentAfterVerify bool
		allocationAfterVerify bool
		wantEvents            []string
	}{
		{
			name:       "reservation and topology follow final preflight",
			wantEvents: []string{"ensure", "validate", "probe", "verify", "reservation", "topology", "migrate"},
		},
		{
			name:       "preflight failure leaves database and runtime untouched",
			verifyErr:  preflightFailure,
			wantEvents: []string{"ensure", "validate", "probe", "verify"},
		},
		{
			name:                  "concurrent replacement wins before reservation publication",
			concurrentAfterVerify: true,
			wantEvents:            []string{"ensure", "validate", "probe", "verify"},
		},
		{
			name:                  "changed allocation plan is rejected before publication",
			allocationAfterVerify: true,
			wantEvents:            []string{"ensure", "validate", "probe", "verify"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server, previous, flags := newRegularToISOCombinedSandboxFixture(t)
			stubNativeISOServiceNetworkMutationRuntime(t)

			oldRunning := isServiceRunningForNetworkMutation
			oldEnsure := ensureBubblewrapForServiceSandboxMutation
			oldValidate := validateServiceSandboxPolicyForMutation
			oldProbe := probeServiceSandboxForMutation
			oldVerify := verifyGeneratedSystemdUnitForSandboxMutation
			oldMigration := migrateServiceNetworkIdentityLocked
			oldStageSystemd := stageRegularNetworkSystemdArtifactsForMutation
			oldRegularSystemctl := runRegularNetworkSystemctlForRuntime
			oldISOSystemctl := runISOSystemctlForRuntime
			oldEnsureTopology := ensureISOTopologyForRuntime
			oldRemoveTopology := removeISOTopologyForRuntime
			t.Cleanup(func() {
				isServiceRunningForNetworkMutation = oldRunning
				ensureBubblewrapForServiceSandboxMutation = oldEnsure
				validateServiceSandboxPolicyForMutation = oldValidate
				probeServiceSandboxForMutation = oldProbe
				verifyGeneratedSystemdUnitForSandboxMutation = oldVerify
				migrateServiceNetworkIdentityLocked = oldMigration
				stageRegularNetworkSystemdArtifactsForMutation = oldStageSystemd
				runRegularNetworkSystemctlForRuntime = oldRegularSystemctl
				runISOSystemctlForRuntime = oldISOSystemctl
				ensureISOTopologyForRuntime = oldEnsureTopology
				removeISOTopologyForRuntime = oldRemoveTopology
			})

			isServiceRunningForNetworkMutation = func(*Server, string) (bool, error) { return false, nil }
			stageRegularNetworkSystemdArtifactsForMutation = func(service *svc.SystemdService) ([]string, error) {
				return service.InstallUnits(), nil
			}
			var events []string
			var preflightViolation error
			topologyCalls, runtimeCalls, migrationCalls := 0, 0, 0
			var preflightResolver string
			var topologyAllocation db.ISOAllocation
			var concurrent *db.Service
			observePreflight := func(stage string) {
				current, err := server.serviceView(previous.Name)
				if err != nil && preflightViolation == nil {
					preflightViolation = fmt.Errorf("%s: read service record: %w", stage, err)
					return
				}
				if preflightViolation == nil && !reflect.DeepEqual(current.AsStruct(), previous) {
					preflightViolation = fmt.Errorf("%s: database changed before final sandbox preflight completed", stage)
				}
				if preflightViolation == nil && (topologyCalls != 0 || runtimeCalls != 0) {
					preflightViolation = fmt.Errorf("%s: topology/runtime calls = %d/%d before final sandbox preflight completed", stage, topologyCalls, runtimeCalls)
				}
			}
			ensureBubblewrapForServiceSandboxMutation = func(context.Context) error {
				events = append(events, "ensure")
				observePreflight("ensure")
				return nil
			}
			validateServiceSandboxPolicyForMutation = func(req serviceSandboxPlanRequest, active bool) (serviceSandboxPolicy, error) {
				events = append(events, "validate")
				observePreflight("validate")
				if !active || req.Policy.State != "on" {
					return serviceSandboxPolicy{}, fmt.Errorf("final sandbox validation active/state = %t/%q", active, req.Policy.State)
				}
				preflightResolver = req.ResolverSource
				return req.Policy, nil
			}
			probeServiceSandboxForMutation = func(_ context.Context, plan serviceSandboxPlan, uid, gid uint32) error {
				events = append(events, "probe")
				observePreflight("probe")
				if uid != uint32(os.Geteuid()) || gid != uint32(os.Getegid()) {
					return fmt.Errorf("final sandbox probe identity = %d:%d", uid, gid)
				}
				resolverMounted := false
				for _, mount := range plan.Mounts {
					resolverMounted = resolverMounted || mount.Source == preflightResolver && mount.Destination == "/etc/resolv.conf"
				}
				if !resolverMounted {
					return fmt.Errorf("final sandbox probe omitted resolver %q", preflightResolver)
				}
				return nil
			}
			verifyGeneratedSystemdUnitForSandboxMutation = func(_ context.Context, path string) error {
				events = append(events, "verify")
				observePreflight("verify")
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if !strings.Contains(string(raw), "NetworkNamespacePath=/var/run/netns/") || !strings.Contains(string(raw), bubblewrapPath) {
					return errors.New("final combined unit omitted ISO namespace or Bubblewrap")
				}
				if tt.concurrentAfterVerify {
					concurrent = previous.Clone()
					concurrent.Identity = concurrent.Identity.Clone()
					concurrent.Identity.UID++
					if _, err := server.cfg.DB.MutateData(func(data *db.Data) error {
						data.Services[previous.Name] = concurrent.Clone()
						return nil
					}); err != nil {
						return err
					}
				}
				if tt.allocationAfterVerify {
					resolverRaw, err := os.ReadFile(preflightResolver)
					if err != nil {
						return err
					}
					hostIP, err := netip.ParseAddr(strings.TrimSpace(strings.TrimPrefix(string(resolverRaw), "nameserver ")))
					if err != nil {
						return err
					}
					link := netip.PrefixFrom(hostIP.Prev(), 30).Masked()
					peer := &db.Service{
						Name: "peer", ServiceType: db.ServiceTypeSystemd,
						Network: &db.ServiceNetworkConfig{Modes: []string{"iso"}},
						ISO:     newDBISOAllocation("peer", isoReservationRequest{Kind: iso.PayloadNative, Modes: []string{"iso"}}, link),
					}
					if _, err := server.cfg.DB.MutateData(func(data *db.Data) error {
						data.Services[peer.Name] = peer
						return nil
					}); err != nil {
						return err
					}
				}
				return tt.verifyErr
			}
			ensureISOTopologyForRuntime = func(_ context.Context, spec netns.ISOTopologySpec) error {
				current, err := server.serviceView(previous.Name)
				if err != nil {
					return err
				}
				if current.ISO().AsStruct() == nil || current.ISO().State() != string(iso.StateReserved) {
					return fmt.Errorf("topology observed unreserved service: %#v", current.AsStruct())
				}
				if !reflect.DeepEqual(current.ISO().AsStruct(), &spec.Allocation) {
					return fmt.Errorf("published allocation differs from topology plan: record=%#v topology=%#v", current.ISO().AsStruct(), spec.Allocation)
				}
				if preflightResolver != "" {
					raw, err := os.ReadFile(preflightResolver)
					if err != nil {
						return err
					}
					if string(raw) != "nameserver "+spec.Allocation.HostIP.String()+"\n" {
						return fmt.Errorf("preflighted resolver %q does not match allocation %s", string(raw), spec.Allocation.HostIP)
					}
				}
				topologyAllocation = spec.Allocation
				events = append(events, "reservation", "topology")
				topologyCalls++
				return nil
			}
			runRegularNetworkSystemctlForRuntime = func(ctx context.Context, args ...string) ([]byte, error) {
				runtimeCalls++
				return oldRegularSystemctl(ctx, args...)
			}
			runISOSystemctlForRuntime = func(ctx context.Context, args ...string) ([]byte, error) {
				runtimeCalls++
				return oldISOSystemctl(ctx, args...)
			}
			removeISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error {
				runtimeCalls++
				return nil
			}
			migrationFailure := errors.New("injected post-topology migration failure")
			migrateServiceNetworkIdentityLocked = func(_ context.Context, _ *Server, request serviceIdentityMigrationRequest, _ io.Writer) (serviceIdentityMigrationResult, error) {
				events = append(events, "migrate")
				migrationCalls++
				if request.TargetService == nil || request.TargetService.ISO == nil || !reflect.DeepEqual(request.TargetService.ISO, &topologyAllocation) {
					return serviceIdentityMigrationResult{}, fmt.Errorf("migration target allocation differs from preflighted topology: %#v", request.TargetService)
				}
				return serviceIdentityMigrationResult{}, migrationFailure
			}

			err := server.updateServiceNetworkLocked(context.Background(), previous.Name, flags, io.Discard)
			switch {
			case tt.verifyErr != nil:
				if !errors.Is(err, tt.verifyErr) {
					t.Fatalf("regular-to-ISO preflight error = %v, want %v", err, tt.verifyErr)
				}
				if topologyCalls != 0 || runtimeCalls != 0 || migrationCalls != 0 {
					t.Fatalf("preflight failure topology/runtime/migration calls = %d/%d/%d, want zero", topologyCalls, runtimeCalls, migrationCalls)
				}
			case tt.concurrentAfterVerify:
				if err == nil || concurrent == nil {
					t.Fatalf("concurrent reservation publication error = %v, replacement=%#v", err, concurrent)
				}
				if topologyCalls != 0 || runtimeCalls != 0 || migrationCalls != 0 {
					t.Fatalf("concurrent publication topology/runtime/migration calls = %d/%d/%d, want zero", topologyCalls, runtimeCalls, migrationCalls)
				}
			case tt.allocationAfterVerify:
				if err == nil {
					t.Fatal("changed allocation plan was published after sandbox preflight")
				}
				if topologyCalls != 0 || runtimeCalls != 0 || migrationCalls != 0 {
					t.Fatalf("changed allocation topology/runtime/migration calls = %d/%d/%d, want zero", topologyCalls, runtimeCalls, migrationCalls)
				}
				peer, peerErr := server.serviceView("peer")
				if peerErr != nil || peer.ISO().AsStruct() == nil {
					t.Fatalf("concurrent allocation claimant was not preserved: record=%#v error=%v", peer.AsStruct(), peerErr)
				}
			case !errors.Is(err, migrationFailure):
				t.Fatalf("regular-to-ISO post-topology error = %v, want %v", err, migrationFailure)
			}
			if preflightViolation != nil {
				t.Fatal(preflightViolation)
			}
			if !reflect.DeepEqual(events, tt.wantEvents) {
				t.Fatalf("regular-to-ISO events = %v, want %v", events, tt.wantEvents)
			}
			current, viewErr := server.serviceView(previous.Name)
			if viewErr != nil {
				t.Fatal(viewErr)
			}
			wantRecord := previous
			if concurrent != nil {
				wantRecord = concurrent
			}
			if !reflect.DeepEqual(current.AsStruct(), wantRecord) {
				t.Fatalf("regular-to-ISO failure record = %#v, want exact %#v (mutation error: %v)", current.AsStruct(), wantRecord, err)
			}
			for _, pattern := range []string{"iso-resolv-*", "iso-gate-*", "api-network-*"} {
				matches, globErr := filepath.Glob(filepath.Join(serviceBinDirForRoot(previous.ServiceRoot), pattern))
				if globErr != nil {
					t.Fatal(globErr)
				}
				if len(matches) != 0 {
					t.Fatalf("regular-to-ISO failure left staged %s artifacts: %v", pattern, matches)
				}
			}
		})
	}
}

func newRegularToISOCombinedSandboxFixture(t *testing.T) (*Server, *db.Service, cli.ServiceSetFlags) {
	t.Helper()
	server, plan, unit := newNativeISOServiceNetworkMutationFixture(t, false)
	root := plan.previous.ServiceRoot
	payload := filepath.Join(serviceRunDirForRoot(root), "api-4")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := serviceDataDirForRoot(root)
	identity := db.ServiceIdentity{
		RequestedUser: "root", RequestedGroup: "root", UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
	}
	policy := serviceSandboxPolicy{State: "on"}
	baseUnit := "[Unit]\nDescription=api\n\n[Service]\nExecStart=" + payload + " --serve\nUser=root\nGroup=root\n" +
		"WorkingDirectory=" + dataDir + "\nEnvironment=HOME=" + dataDir + " USER=root LOGNAME=root SHELL=/bin/sh\n"
	sandboxPlan, err := buildValidatedServiceSandboxPlan(serviceSandboxPlanRequest{
		Service: plan.name, Policy: policy, Payload: payload, DataDir: dataDir,
		ResolverSource: "/etc/resolv.conf", UID: identity.UID, GID: identity.GID, Hostname: plan.name,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonicalUnit, _, err := renderNativeSandboxUnitWithPlan(baseUnit, nativeSandboxUnitRequest{
		CurrentPolicy: serviceSandboxPolicy{State: "legacy"}, TargetPolicy: policy, Identity: identity,
		Payload: payload, DataDir: dataDir, Resolver: "/etc/resolv.conf", Hostname: plan.name,
	}, &sandboxPlan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unit, []byte(canonicalUnit), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := plan.previous.Clone()
	previous.Identity = &identity
	previous.Artifacts[db.ArtifactBinary] = &db.Artifact{Refs: map[db.ArtifactRef]string{
		db.Gen(previous.Generation): payload,
		"latest":                    payload,
	}}
	previous.Sandbox = &db.ServiceSandboxStore{Refs: map[db.ArtifactRef]*db.ServiceSandboxPolicy{
		db.Gen(previous.Generation): serviceSandboxPolicyToDB(policy),
		"latest":                    serviceSandboxPolicyToDB(policy),
	}}
	if _, err := server.cfg.DB.MutateData(func(data *db.Data) error {
		data.Services[previous.Name] = previous.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return server, previous, cli.ServiceSetFlags{
		Net: "iso", NetSet: true,
		RunAs: strconv.Itoa(os.Geteuid()) + ":" + strconv.Itoa(os.Getegid()), RunAsSet: true,
	}
}

func TestISOToISOCombinedIdentitySandboxPreflightPrecedesReservationAndRuntime(t *testing.T) {
	preflightFailure := errors.New("injected ISO-to-ISO final sandbox verification failure")
	migrationFailure := errors.New("injected ISO-to-ISO post-publication migration failure")
	for _, tt := range []struct {
		name             string
		verifyErr        error
		migrationErr     error
		sameServiceRace  bool
		peerAllocation   bool
		wantEvents       []string
		wantFinalSuccess bool
	}{
		{
			name:       "preflight failure leaves exact previous database and runtime",
			verifyErr:  preflightFailure,
			wantEvents: []string{"ensure", "validate", "probe", "verify"},
		},
		{
			name:             "stable allocation publishes only after final preflight",
			wantEvents:       []string{"ensure", "validate", "probe", "verify", "reservation", "topology", "migrate"},
			wantFinalSuccess: true,
		},
		{
			name:         "post-publication failure restores previous ISO runtime",
			migrationErr: migrationFailure,
			wantEvents:   []string{"ensure", "validate", "probe", "verify", "reservation", "topology", "migrate", "restore-topology"},
		},
		{
			name:            "same-service replacement wins before publication",
			sameServiceRace: true,
			wantEvents:      []string{"ensure", "validate", "probe", "verify"},
		},
		{
			name:           "peer allocation collision wins before publication",
			peerAllocation: true,
			wantEvents:     []string{"ensure", "validate", "probe", "verify"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server, previous, flags := newISOToISOCombinedSandboxFixture(t)
			stubNativeISOServiceNetworkMutationRuntime(t)

			oldRunning := isServiceRunningForNetworkMutation
			oldEnsure := ensureBubblewrapForServiceSandboxMutation
			oldValidate := validateServiceSandboxPolicyForMutation
			oldProbe := probeServiceSandboxForMutation
			oldVerify := verifyGeneratedSystemdUnitForSandboxMutation
			oldMigration := migrateServiceNetworkIdentityLocked
			oldStageSystemd := stageRegularNetworkSystemdArtifactsForMutation
			oldRegularSystemctl := runRegularNetworkSystemctlForRuntime
			oldISOSystemctl := runISOSystemctlForRuntime
			oldEnsureTopology := ensureISOTopologyForRuntime
			oldRemoveTopology := removeISOTopologyForRuntime
			t.Cleanup(func() {
				isServiceRunningForNetworkMutation = oldRunning
				ensureBubblewrapForServiceSandboxMutation = oldEnsure
				validateServiceSandboxPolicyForMutation = oldValidate
				probeServiceSandboxForMutation = oldProbe
				verifyGeneratedSystemdUnitForSandboxMutation = oldVerify
				migrateServiceNetworkIdentityLocked = oldMigration
				stageRegularNetworkSystemdArtifactsForMutation = oldStageSystemd
				runRegularNetworkSystemctlForRuntime = oldRegularSystemctl
				runISOSystemctlForRuntime = oldISOSystemctl
				ensureISOTopologyForRuntime = oldEnsureTopology
				removeISOTopologyForRuntime = oldRemoveTopology
			})

			isServiceRunningForNetworkMutation = func(*Server, string) (bool, error) { return false, nil }
			stageRegularNetworkSystemdArtifactsForMutation = func(service *svc.SystemdService) ([]string, error) {
				return service.InstallUnits(), nil
			}
			var events []string
			var preflightViolation error
			var preflightResolver string
			var published db.ISOAllocation
			var concurrent *db.Service
			var migratedTarget *db.Service
			topologyCalls, runtimeCalls, migrationCalls, removeCalls := 0, 0, 0, 0
			observePreflight := func(stage string) {
				current, err := server.serviceView(previous.Name)
				if err != nil && preflightViolation == nil {
					preflightViolation = fmt.Errorf("%s: read service record: %w", stage, err)
					return
				}
				if preflightViolation == nil && !reflect.DeepEqual(current.AsStruct(), previous) {
					preflightViolation = fmt.Errorf("%s: database changed before final ISO-to-ISO sandbox preflight completed", stage)
				}
				if preflightViolation == nil && (topologyCalls != 0 || runtimeCalls != 0 || migrationCalls != 0) {
					preflightViolation = fmt.Errorf("%s: topology/runtime/migration calls = %d/%d/%d before final ISO-to-ISO sandbox preflight completed", stage, topologyCalls, runtimeCalls, migrationCalls)
				}
			}
			ensureBubblewrapForServiceSandboxMutation = func(context.Context) error {
				events = append(events, "ensure")
				observePreflight("ensure")
				return nil
			}
			validateServiceSandboxPolicyForMutation = func(req serviceSandboxPlanRequest, active bool) (serviceSandboxPolicy, error) {
				events = append(events, "validate")
				observePreflight("validate")
				if !active || req.Policy.State != "on" {
					return serviceSandboxPolicy{}, fmt.Errorf("ISO-to-ISO validation active/state = %t/%q", active, req.Policy.State)
				}
				preflightResolver = req.ResolverSource
				return req.Policy, nil
			}
			probeServiceSandboxForMutation = func(_ context.Context, plan serviceSandboxPlan, uid, gid uint32) error {
				events = append(events, "probe")
				observePreflight("probe")
				if uid != uint32(os.Geteuid()) || gid != uint32(os.Getegid()) {
					return fmt.Errorf("ISO-to-ISO final sandbox probe identity = %d:%d", uid, gid)
				}
				for _, mount := range plan.Mounts {
					if mount.Source == preflightResolver && mount.Destination == "/etc/resolv.conf" {
						return nil
					}
				}
				return fmt.Errorf("ISO-to-ISO final sandbox probe omitted resolver %q", preflightResolver)
			}
			verifyGeneratedSystemdUnitForSandboxMutation = func(_ context.Context, path string) error {
				events = append(events, "verify")
				observePreflight("verify")
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if !strings.Contains(string(raw), bubblewrapPath) || !strings.Contains(string(raw), "NetworkNamespacePath=/var/run/netns/"+previous.ISO.NetNS) {
					return errors.New("final ISO-to-ISO unit omitted Bubblewrap or stable namespace")
				}
				switch {
				case tt.sameServiceRace:
					concurrent = previous.Clone()
					concurrent.Identity = concurrent.Identity.Clone()
					concurrent.Identity.UID++
					_, err = server.cfg.DB.MutateData(func(data *db.Data) error {
						data.Services[previous.Name] = concurrent.Clone()
						return nil
					})
				case tt.peerAllocation:
					peer := &db.Service{
						Name: "peer", ServiceType: db.ServiceTypeSystemd,
						Network: &db.ServiceNetworkConfig{Modes: []string{"iso"}},
						ISO:     newDBISOAllocation("peer", isoReservationRequest{Kind: iso.PayloadNative, Modes: []string{"iso"}}, previous.ISO.Link),
					}
					_, err = server.cfg.DB.MutateData(func(data *db.Data) error {
						data.Services[peer.Name] = peer
						return nil
					})
				}
				return errors.Join(err, tt.verifyErr)
			}
			ensureISOTopologyForRuntime = func(_ context.Context, spec netns.ISOTopologySpec) error {
				current, err := server.serviceView(previous.Name)
				if err != nil {
					return err
				}
				if preflightResolver == "" {
					events = append(events, "topology")
					topologyCalls++
					return nil
				}
				if reflect.DeepEqual(current.AsStruct(), previous) {
					if !reflect.DeepEqual(&spec.Allocation, previous.ISO) {
						return fmt.Errorf("restored topology allocation = %#v, want previous %#v", spec.Allocation, previous.ISO)
					}
					events = append(events, "restore-topology")
					topologyCalls++
					return nil
				}
				if current.ISO().State() != string(iso.StateReserved) || !reflect.DeepEqual(current.ISO().AsStruct(), &spec.Allocation) {
					return fmt.Errorf("published ISO-to-ISO record/topology mismatch: %#v / %#v", current.AsStruct(), spec.Allocation)
				}
				if spec.Allocation.Link != previous.ISO.Link || spec.Allocation.NetNS != previous.ISO.NetNS || !reflect.DeepEqual(spec.Allocation.DesiredModes, []string{"iso"}) {
					return fmt.Errorf("published ISO-to-ISO allocation was not the stable planned target: %#v", spec.Allocation)
				}
				raw, err := os.ReadFile(preflightResolver)
				if err != nil || string(raw) != "nameserver "+spec.Allocation.HostIP.String()+"\n" {
					return fmt.Errorf("preflighted ISO-to-ISO resolver/allocation mismatch: %q: %w", raw, err)
				}
				published = spec.Allocation
				events = append(events, "reservation", "topology")
				topologyCalls++
				return nil
			}
			runRegularNetworkSystemctlForRuntime = func(ctx context.Context, args ...string) ([]byte, error) {
				runtimeCalls++
				return oldRegularSystemctl(ctx, args...)
			}
			runISOSystemctlForRuntime = func(ctx context.Context, args ...string) ([]byte, error) {
				runtimeCalls++
				return oldISOSystemctl(ctx, args...)
			}
			removeISOTopologyForRuntime = func(ctx context.Context, spec netns.ISOTopologySpec) error {
				removeCalls++
				return oldRemoveTopology(ctx, spec)
			}
			migrateServiceNetworkIdentityLocked = func(_ context.Context, _ *Server, request serviceIdentityMigrationRequest, _ io.Writer) (serviceIdentityMigrationResult, error) {
				events = append(events, "migrate")
				migrationCalls++
				if request.TargetService == nil || request.TargetService.ISO == nil || !reflect.DeepEqual(request.TargetService.ISO, &published) {
					return serviceIdentityMigrationResult{}, fmt.Errorf("ISO-to-ISO migration target allocation differs from preflighted publication: %#v", request.TargetService)
				}
				if tt.migrationErr != nil {
					return serviceIdentityMigrationResult{}, tt.migrationErr
				}
				migratedTarget = request.TargetService.Clone()
				_, err := server.cfg.DB.MutateData(func(data *db.Data) error {
					current := data.Services[previous.Name]
					if current == nil || current.ISO == nil || !reflect.DeepEqual(current.ISO, &published) {
						return fmt.Errorf("migration observed unpreflighted ISO-to-ISO publication: %#v", current)
					}
					data.Services[previous.Name] = migratedTarget.Clone()
					return nil
				})
				return serviceIdentityMigrationResult{}, err
			}

			err := server.updateServiceNetworkLocked(context.Background(), previous.Name, flags, io.Discard)
			switch {
			case tt.verifyErr != nil:
				if !errors.Is(err, tt.verifyErr) {
					t.Fatalf("ISO-to-ISO preflight error = %v, want %v", err, tt.verifyErr)
				}
			case tt.migrationErr != nil:
				if !errors.Is(err, tt.migrationErr) {
					t.Fatalf("ISO-to-ISO post-publication error = %v, want %v", err, tt.migrationErr)
				}
			case tt.sameServiceRace || tt.peerAllocation:
				if err == nil {
					t.Fatal("ISO-to-ISO publication race unexpectedly reached runtime")
				}
			case tt.wantFinalSuccess:
				if err != nil {
					t.Fatal(err)
				}
			}
			if preflightViolation != nil {
				t.Fatal(preflightViolation)
			}
			if !reflect.DeepEqual(events, tt.wantEvents) {
				t.Fatalf("ISO-to-ISO events = %v, want %v", events, tt.wantEvents)
			}
			current, viewErr := server.serviceView(previous.Name)
			if viewErr != nil {
				t.Fatal(viewErr)
			}
			wantRecord := previous
			switch {
			case concurrent != nil:
				wantRecord = concurrent
			case tt.wantFinalSuccess:
				if migratedTarget == nil {
					t.Fatal("successful ISO-to-ISO mutation did not migrate the preflighted target")
				}
				wantRecord = migratedTarget.Clone()
				wantRecord.ISO.State = string(iso.StateStopped)
			}
			if !serviceNetworkRecordsEqual(current.AsStruct(), wantRecord) {
				gotJSON, gotJSONErr := json.MarshalIndent(current.AsStruct(), "", "  ")
				wantJSON, wantJSONErr := json.MarshalIndent(wantRecord, "", "  ")
				t.Fatalf("ISO-to-ISO final record mismatch (mutation error: %v, JSON errors: %v/%v):\n got %s\nwant %s", err, gotJSONErr, wantJSONErr, gotJSON, wantJSON)
			}
			if tt.peerAllocation {
				peer, peerErr := server.serviceView("peer")
				if peerErr != nil || peer.ISO().AsStruct() == nil {
					t.Fatalf("ISO-to-ISO peer allocation race was not preserved: %#v, %v", peer.AsStruct(), peerErr)
				}
			}
			if !tt.wantFinalSuccess {
				if topologyCalls != 0 && tt.migrationErr == nil {
					t.Fatalf("ISO-to-ISO failure reached topology %d times", topologyCalls)
				}
				if migrationCalls != 0 && tt.migrationErr == nil {
					t.Fatalf("ISO-to-ISO failure reached migration %d times", migrationCalls)
				}
				for _, path := range []string{
					exactServiceArtifact(previous, db.ArtifactSystemdUnit),
					exactServiceArtifact(previous, db.ArtifactNetNSResolv),
					exactServiceArtifact(previous, db.ArtifactNetNSService),
				} {
					if _, statErr := os.Stat(path); statErr != nil {
						t.Fatalf("ISO-to-ISO recovery lost previous artifact %s: %v", path, statErr)
					}
				}
				assertNoISOToISOProvisionalArtifacts(t, previous)
			}
			if tt.migrationErr != nil && (removeCalls == 0 || topologyCalls < 2) {
				t.Fatalf("ISO-to-ISO post-publication recovery remove/topology calls = %d/%d, want removal and restored topology", removeCalls, topologyCalls)
			}
		})
	}
}

func newISOToISOCombinedSandboxFixture(t *testing.T) (*Server, *db.Service, cli.ServiceSetFlags) {
	t.Helper()
	server, previous, flags := newRegularToISOCombinedSandboxFixture(t)
	allocation := newDBISOAllocation(
		previous.Name,
		isoReservationRequest{Kind: iso.PayloadNative, Modes: []string{"iso"}},
		netip.MustParsePrefix("172.30.0.0/30"),
	)
	allocation.State = string(iso.StateReady)
	previous.Network = &db.ServiceNetworkConfig{Modes: []string{"iso"}}
	previous.ISO = allocation
	root := previous.ServiceRoot
	resolver := filepath.Join(serviceRunDirForRoot(root), "current-resolv.conf")
	if err := os.WriteFile(resolver, []byte("nameserver "+allocation.HostIP.String()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(serviceBinDirForRoot(root), "current-netns.service")
	if err := os.WriteFile(gate, []byte("[Unit]\nDescription=api current ISO namespace\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	previous.Artifacts[db.ArtifactNetNSResolv] = &db.Artifact{Refs: map[db.ArtifactRef]string{
		db.Gen(previous.Generation): resolver,
		"latest":                    resolver,
	}}
	previous.Artifacts[db.ArtifactNetNSService] = &db.Artifact{Refs: map[db.ArtifactRef]string{
		db.Gen(previous.Generation): gate,
		"latest":                    gate,
	}}
	payload := exactServiceArtifact(previous, db.ArtifactBinary)
	dataDir := serviceDataDirForRoot(root)
	identity := *previous.Identity
	policy := serviceSandboxPolicy{State: "on"}
	baseUnit := "[Unit]\nDescription=api\n\n[Service]\nExecStart=" + payload + " --serve\nUser=root\nGroup=root\n" +
		"WorkingDirectory=" + dataDir + "\nEnvironment=HOME=" + dataDir + " USER=root LOGNAME=root SHELL=/bin/sh\n" +
		"NetworkNamespacePath=/var/run/netns/" + allocation.NetNS + "\n"
	sandboxPlan, err := buildValidatedServiceSandboxPlan(serviceSandboxPlanRequest{
		Service: previous.Name, Policy: policy, Payload: payload, DataDir: dataDir,
		ResolverSource: resolver, UID: identity.UID, GID: identity.GID, Hostname: previous.Name,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonicalUnit, _, err := renderNativeSandboxUnitWithPlan(baseUnit, nativeSandboxUnitRequest{
		CurrentPolicy: serviceSandboxPolicy{State: "legacy"}, TargetPolicy: policy, Identity: identity,
		Payload: payload, DataDir: dataDir, Resolver: resolver, Hostname: previous.Name,
	}, &sandboxPlan)
	if err != nil {
		t.Fatal(err)
	}
	unit := exactServiceArtifact(previous, db.ArtifactSystemdUnit)
	if err := os.WriteFile(unit, []byte(canonicalUnit), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := server.cfg.DB.MutateData(func(data *db.Data) error {
		data.Services[previous.Name] = previous.Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	flags.Net = "iso"
	return server, previous, flags
}

func exactServiceArtifact(service *db.Service, name db.ArtifactName) string {
	path, _ := service.Artifacts.Gen(name, service.Generation)
	return path
}

func assertNoISOToISOProvisionalArtifacts(t *testing.T, service *db.Service) {
	t.Helper()
	for _, pattern := range []string{"iso-resolv-*", "iso-gate-*", service.Name + "-network-*"} {
		matches, err := filepath.Glob(filepath.Join(serviceBinDirForRoot(service.ServiceRoot), pattern))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 0 {
			t.Fatalf("ISO-to-ISO failure left provisional %s artifacts: %v", pattern, matches)
		}
	}
}

func TestServiceSetRunAsAndISOCommitsOrRollsBackAtomically(t *testing.T) {
	migrationErr := errors.New("identity migration failed")
	restoreErr := errors.New("restore previous runtime failed")
	tests := []struct {
		name          string
		migrate       func(*Server, serviceIdentityMigrationRequest) (*db.Service, error)
		restoreErr    error
		wantErr       bool
		wantOld       bool
		wantTombstone bool
		concurrent    bool
	}{
		{
			name: "success",
			migrate: func(server *Server, request serviceIdentityMigrationRequest) (*db.Service, error) {
				target := request.TargetService.Clone()
				_, err := server.cfg.DB.MutateData(func(data *db.Data) error {
					data.Services[request.Service] = target.Clone()
					return nil
				})
				return target, err
			},
		},
		{
			name: "migration failure rolls back network and identity",
			migrate: func(_ *Server, _ serviceIdentityMigrationRequest) (*db.Service, error) {
				return nil, migrationErr
			},
			wantErr: true,
			wantOld: true,
		},
		{
			name: "migration failure fail-closes when prior runtime restore fails",
			migrate: func(_ *Server, _ serviceIdentityMigrationRequest) (*db.Service, error) {
				return nil, migrationErr
			},
			restoreErr:    restoreErr,
			wantErr:       true,
			wantTombstone: true,
		},
		{
			name: "same generation concurrent replacement is preserved",
			migrate: func(server *Server, request serviceIdentityMigrationRequest) (*db.Service, error) {
				concurrent := request.TargetService.Clone()
				concurrent.Identity = concurrent.Identity.Clone()
				concurrent.Identity.UID++
				_, err := server.cfg.DB.MutateData(func(data *db.Data) error {
					data.Services[request.Service] = concurrent.Clone()
					return nil
				})
				return concurrent, err
			},
			wantErr:    true,
			concurrent: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubServiceNetworkStaticVerification(t)
			server, plan, originalUnit := newNativeISOServiceNetworkMutationFixture(t, false)
			stubNativeISOServiceNetworkMutationRuntime(t)
			if tt.wantOld || tt.wantTombstone {
				plan.previousRunning = true
				plan.previousRuntime = []serviceIdentityRuntimeUnitState{{Unit: "api.service", Active: true}}
				plan.previousEnablement = []serviceIdentityUnitEnablement{{Unit: "api.service", Enabled: true}}
			}
			oldMigration := migrateServiceNetworkIdentityLocked
			oldStageSystemd := stageRegularNetworkSystemdArtifactsForMutation
			stageDefinitionCalls := 0
			stageRegularNetworkSystemdArtifactsForMutation = func(service *svc.SystemdService) ([]string, error) {
				stageDefinitionCalls++
				return service.InstallUnits(), nil
			}
			t.Cleanup(func() {
				migrateServiceNetworkIdentityLocked = oldMigration
				stageRegularNetworkSystemdArtifactsForMutation = oldStageSystemd
			})
			baseISOSystemctl := runISOSystemctlForRuntime
			isoStopCalls := 0
			runISOSystemctlForRuntime = func(ctx context.Context, args ...string) ([]byte, error) {
				if len(args) != 0 && args[0] == "stop" {
					isoStopCalls++
				}
				return baseISOSystemctl(ctx, args...)
			}
			regularStartCalls := 0
			var regularCommands [][]string
			regularActive := false
			regularEnabled := false
			inspectRegularNetworkUnitActive = func(context.Context, string) (bool, error) { return regularActive, nil }
			inspectRegularNetworkUnitEnabled = func(context.Context, string) (bool, error) { return regularEnabled, nil }
			runRegularNetworkSystemctlForRuntime = func(_ context.Context, args ...string) ([]byte, error) {
				regularCommands = append(regularCommands, slices.Clone(args))
				if len(args) == 0 {
					return nil, nil
				}
				switch args[0] {
				case "enable":
					regularEnabled = true
				case "disable":
					regularEnabled = false
				case "start":
					regularStartCalls++
					if tt.restoreErr != nil {
						return nil, tt.restoreErr
					}
					regularActive = true
				case "stop":
					regularActive = false
				}
				return nil, nil
			}
			removeTopologyCalls := 0
			removeISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error {
				removeTopologyCalls++
				return nil
			}
			var migrated *db.Service
			migrateServiceNetworkIdentityLocked = func(_ context.Context, gotServer *Server, request serviceIdentityMigrationRequest, _ io.Writer) (serviceIdentityMigrationResult, error) {
				var err error
				migrated, err = tt.migrate(gotServer, request)
				return serviceIdentityMigrationResult{}, err
			}
			mutation := &isoServiceNetworkMutation{server: server, plan: plan, direction: serviceNetworkRegularToISO}
			if err := mutation.Stage(context.Background()); err != nil {
				t.Fatal(err)
			}
			identity := db.ServiceIdentity{
				RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
				UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
			}
			mutation.target.Identity = &identity
			request := serviceIdentityMigrationRequest{
				Service: plan.name, Requested: identity.RequestedUser + ":" + identity.RequestedGroup,
				TargetService: mutation.target, Target: resolvedServiceIdentity{Persisted: identity},
			}
			operation := &isoNetworkIdentityMutation{
				server: server, ctx: context.Background(), plan: plan, flags: cli.ServiceSetFlags{RunAsSet: true},
				out: io.Discard, direction: serviceNetworkRegularToISO, mutation: mutation,
			}
			err := operation.runPrepared(request, false)
			operation.finish(&err)
			if tt.wantErr && err == nil {
				t.Fatal("combined run-as and ISO mutation succeeded, want failure")
			}
			if !tt.wantErr && err != nil {
				t.Fatal(err)
			}
			current, viewErr := server.serviceView("api")
			if viewErr != nil {
				t.Fatal(viewErr)
			}
			switch {
			case tt.wantOld:
				if stageDefinitionCalls != 1 || regularStartCalls == 0 {
					t.Fatalf("concrete prior runtime restore stages=%d starts=%d commands=%v error=%v, want 1 and >0", stageDefinitionCalls, regularStartCalls, regularCommands, err)
				}
				if isoStopCalls == 0 || removeTopologyCalls == 0 {
					t.Fatalf("combined rollback cleanup stops=%d topology-removals=%d, want both >0", isoStopCalls, removeTopologyCalls)
				}
				if !reflect.DeepEqual(current.AsStruct(), plan.previous) {
					t.Fatalf("failed combined mutation = %#v, want exact previous %#v", current.AsStruct(), plan.previous)
				}
				if _, statErr := os.Stat(originalUnit); statErr != nil {
					t.Fatalf("previous unit was not preserved: %v", statErr)
				}
				for _, pattern := range []string{"iso-resolv-*", "iso-gate-*", "api-network-*"} {
					matches, globErr := filepath.Glob(filepath.Join(serviceBinDirForRoot(plan.previous.ServiceRoot), pattern))
					if globErr != nil {
						t.Fatal(globErr)
					}
					if len(matches) != 0 {
						t.Fatalf("combined rollback left staged %s artifacts: %v", pattern, matches)
					}
				}
			case tt.wantTombstone:
				if stageDefinitionCalls != 1 || regularStartCalls == 0 {
					t.Fatalf("failed concrete prior runtime restore stages=%d starts=%d commands=%v error=%v, want 1 and >0", stageDefinitionCalls, regularStartCalls, regularCommands, err)
				}
				if isoStopCalls == 0 || removeTopologyCalls == 0 {
					t.Fatalf("failed combined rollback cleanup stops=%d topology-removals=%d, want both >0", isoStopCalls, removeTopologyCalls)
				}
				if current.ISO().State() != string(iso.StateTombstoned) || !strings.Contains(current.ISO().LastError(), "restoration failed") {
					t.Fatalf("failed recovery did not fail closed: %#v", current.ISO().AsStruct())
				}
			case tt.concurrent:
				if migrated == nil || !reflect.DeepEqual(current.AsStruct(), migrated) {
					t.Fatalf("concurrent replacement = %#v, want exact %#v", current.AsStruct(), migrated)
				}
			default:
				if migrated == nil || current.ISO().State() != string(iso.StateStopped) || current.Identity().UID() != uint32(os.Geteuid()) || !slices.Equal(current.Network().Modes().AsSlice(), []string{"iso"}) {
					t.Fatalf("combined mutation result = %#v, migrated %#v", current.AsStruct(), migrated)
				}
			}
		})
	}
}

func TestServiceSetNetworkAndRunAsUsesResolverGuardForBothDirections(t *testing.T) {
	for _, tc := range []struct {
		name      string
		previous  []string
		desired   []string
		wantReady []string
	}{
		{name: "enter tailscale", previous: []string{"host"}, desired: []string{"ts"}, wantReady: []string{"ts"}},
		{name: "leave tailscale", previous: []string{"ts"}, desired: []string{"host"}, wantReady: []string{"ts"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)
			root := server.defaultServiceRootDir("api")
			if err := ensureDirsForRoot(root, ""); err != nil {
				t.Fatal(err)
			}
			unit := filepath.Join(serviceBinDirForRoot(root), "api-network.service")
			if err := os.WriteFile(unit, []byte("[Service]\nExecStart=/bin/true\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			previous := &db.Service{
				Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root, Network: &db.ServiceNetworkConfig{Modes: tc.previous},
				Artifacts: db.ArtifactStore{db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(0): unit}}},
			}
			if slices.Contains(tc.previous, "ts") {
				previous.TSNet = &db.TailscaleNetwork{Interface: "yts-previous"}
			}
			plan := &serviceNetworkMutationPlan{name: "api", previous: previous, currentDesired: db.ServiceNetworkConfig{Modes: tc.previous}, desired: db.ServiceNetworkConfig{Modes: tc.desired}}
			target := previous.Clone()
			target.Network = &db.ServiceNetworkConfig{Modes: tc.desired}
			if slices.Contains(tc.desired, "ts") {
				target.TSNet = &db.TailscaleNetwork{Interface: "yts-target"}
			} else {
				target.TSNet = nil
			}

			oldPrepare := prepareServiceNetworkIdentityReplacement
			oldGuard := withRegularNetworkResolverMutationGuard
			oldReady := checkRegularNetworkResolverCanonicalReady
			oldMigrate := migrateServiceNetworkIdentityWithResolverGuardLocked
			t.Cleanup(func() {
				prepareServiceNetworkIdentityReplacement = oldPrepare
				withRegularNetworkResolverMutationGuard = oldGuard
				checkRegularNetworkResolverCanonicalReady = oldReady
				migrateServiceNetworkIdentityWithResolverGuardLocked = oldMigrate
			})
			guarded := false
			withRegularNetworkResolverMutationGuard = func(_ *Server, run func() error) error {
				guarded = true
				defer func() { guarded = false }()
				return run()
			}
			generation, err := server.serviceNetworkGenerationInstaller("api", target, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			prepareServiceNetworkIdentityReplacement = func(_ context.Context, _ *Server, _ *serviceNetworkMutationPlan, _ cli.ServiceSetFlags, _ io.Writer) (*db.Service, resolvedServiceIdentity, string, *svc.SystemdService, error) {
				if !guarded {
					t.Fatal("combined mutation staged outside resolver guard")
				}
				return target, resolvedServiceIdentity{}, "unit", generation, nil
			}
			checkRegularNetworkResolverCanonicalReady = func(_ context.Context, _ *Server, got db.Service) error {
				if !guarded {
					t.Fatal("combined readiness check ran outside resolver guard")
				}
				if got.Network == nil || !slices.Equal(got.Network.Modes, tc.wantReady) {
					t.Fatalf("canonical readiness target = %#v, want modes %v", got.Network, tc.wantReady)
				}
				return nil
			}
			migrateServiceNetworkIdentityWithResolverGuardLocked = func(_ context.Context, _ *Server, _ serviceIdentityMigrationRequest, _ io.Writer) (serviceIdentityMigrationResult, error) {
				if !guarded {
					t.Fatal("combined identity transaction ran outside resolver guard")
				}
				return serviceIdentityMigrationResult{}, nil
			}

			err = server.applyServiceNetworkMutationLocked(context.Background(), plan, cli.ServiceSetFlags{RunAsSet: true}, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestServiceSetNetworkAndRunAsResolverBlockPreventsStaging(t *testing.T) {
	server := newTestServer(t)
	previous := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, Network: &db.ServiceNetworkConfig{Modes: []string{"ts"}}}
	plan := &serviceNetworkMutationPlan{
		name: "api", previous: previous, currentDesired: db.ServiceNetworkConfig{Modes: []string{"ts"}}, desired: db.ServiceNetworkConfig{Modes: []string{"host"}},
	}
	oldPrepare := prepareServiceNetworkIdentityReplacement
	oldGuard := withRegularNetworkResolverMutationGuard
	t.Cleanup(func() {
		prepareServiceNetworkIdentityReplacement = oldPrepare
		withRegularNetworkResolverMutationGuard = oldGuard
	})
	prepareServiceNetworkIdentityReplacement = func(context.Context, *Server, *serviceNetworkMutationPlan, cli.ServiceSetFlags, io.Writer) (*db.Service, resolvedServiceIdentity, string, *svc.SystemdService, error) {
		t.Fatal("resolver recovery block did not prevent combined network staging")
		return nil, resolvedServiceIdentity{}, "", nil, nil
	}
	blocked := errors.New("resolver recovery blocked")
	withRegularNetworkResolverMutationGuard = func(*Server, func() error) error { return blocked }
	err := server.applyServiceNetworkMutationLocked(context.Background(), plan, cli.ServiceSetFlags{RunAsSet: true}, io.Discard)
	if !errors.Is(err, blocked) {
		t.Fatalf("applyServiceNetworkMutationLocked error = %v, want %v", err, blocked)
	}
}

func TestBuildServiceNetworkIdentityMigrationRequestKeepsFullInventoryAndExplicitEnablement(t *testing.T) {
	server := newTestServer(t)
	root := server.defaultServiceRootDir("api")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(serviceBinDirForRoot(root), "api-network.service")
	if err := os.WriteFile(unit, []byte("[Service]\nExecStart=/bin/true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := &db.Service{
		Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root,
		Artifacts: db.ArtifactStore{db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(0): unit}}},
	}
	plan := &serviceNetworkMutationPlan{
		name: "api", previous: target.Clone(), previousEnablement: []serviceIdentityUnitEnablement{{Unit: "api.service", Enabled: false}},
	}
	generation, err := server.serviceNetworkGenerationInstaller("api", target, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	request, err := buildServiceNetworkIdentityMigrationRequest(plan, cli.ServiceSetFlags{RunAsSet: true}, target, resolvedServiceIdentity{}, "unit", generation)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(request.GenerationUnits, []string{"api.service"}) {
		t.Fatalf("generation inventory = %v, want complete install plan", request.GenerationUnits)
	}
	if request.GenerationEnablement == nil || len(*request.GenerationEnablement) != 1 || (*request.GenerationEnablement)[0].TargetEnabled {
		t.Fatalf("explicit enablement = %#v, want disabled primary", request.GenerationEnablement)
	}
	if err := request.StageGeneration(context.Background()); err != nil {
		t.Fatalf("full generation staging failed for disabled primary: %v", err)
	}
}

func TestServiceNetworkIdentityRewriteCreatesAnotherFreshOwnedUnit(t *testing.T) {
	server := newTestServer(t)
	root := server.defaultServiceRootDir("api")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	previousUnit := filepath.Join(serviceBinDirForRoot(root), "api-current.service")
	if err := os.WriteFile(previousUnit, []byte("[Unit]\n\n[Service]\nExecStart=/bin/true\nUser=root\nGroup=root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := &db.Service{Name: "api", ServiceType: db.ServiceTypeSystemd, ServiceRoot: root, Artifacts: db.ArtifactStore{
		db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{db.Gen(0): previousUnit}},
	}}
	txn, err := beginRegularNetworkArtifactTransaction(root, previous)
	if err != nil {
		t.Fatal(err)
	}
	firstStaged, err := writeFreshRegularNetworkArtifact(root, "bin", "api-network-", ".service", []byte("[Unit]\n\n[Service]\nExecStart=/bin/true\nUser=root\nGroup=root\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := txn.registerStagedPath(firstStaged); err != nil {
		t.Fatal(err)
	}
	target := previous.Clone()
	target.Artifacts[db.ArtifactSystemdUnit] = &db.Artifact{Refs: map[db.ArtifactRef]string{db.Gen(0): firstStaged}}
	plan := &serviceNetworkMutationPlan{name: "api", previous: previous, artifactTxn: txn}
	oldVerify := verifyGeneratedSystemdUnitForSandboxMutation
	t.Cleanup(func() { verifyGeneratedSystemdUnitForSandboxMutation = oldVerify })
	verified := false
	verifyGeneratedSystemdUnitForSandboxMutation = func(_ context.Context, path string) error {
		verified = true
		if path == firstStaged {
			t.Fatal("static verification used the pre-identity staged unit")
		}
		return nil
	}

	_, err = server.applyServiceNetworkIdentityToUnit(context.Background(), plan, target, db.ServiceIdentity{UID: 1000, GID: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if !verified {
		t.Fatal("fresh identity unit was not statically verified")
	}
	rewritten, _ := target.Artifacts.Gen(db.ArtifactSystemdUnit, 0)
	if rewritten == firstStaged {
		t.Fatal("identity rewrite overwrote the first staged unit instead of creating a fresh owned artifact")
	}
	if raw, readErr := os.ReadFile(firstStaged); readErr != nil || strings.Contains(string(raw), "User=1000") {
		t.Fatalf("first staged unit changed in place: %q, %v", raw, readErr)
	}
	if _, ok := txn.stagedPaths[rewritten]; !ok {
		t.Fatalf("fresh identity unit %q was not registered as transaction-owned", rewritten)
	}
}

func TestServiceNetworkIdentityRewriteRendersAndProbesOneCombinedSandboxTarget(t *testing.T) {
	server, previous := newServiceSandboxMutationFixture(t, serviceSandboxPolicy{State: "on"})
	root := server.serviceRootFromView(previous.View())
	payload, _ := previous.Artifacts.Gen(db.ArtifactBinary, previous.Generation)
	dataDir := serviceDataDirForRoot(root)
	oldResolver := filepath.Join(serviceRunDirForRoot(root), "resolv-old.conf")
	targetResolver := filepath.Join(serviceRunDirForRoot(root), "resolv-target.conf")
	for path, content := range map[string]string{
		oldResolver:    "nameserver 1.1.1.1\n",
		targetResolver: "nameserver 2.2.2.2\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	previous.Artifacts[db.ArtifactNetNSResolv] = &db.Artifact{Refs: map[db.ArtifactRef]string{
		db.Gen(previous.Generation): oldResolver,
	}}
	txn, err := beginRegularNetworkArtifactTransaction(root, previous)
	if err != nil {
		t.Fatal(err)
	}
	firstContent := "[Unit]\nRequires=yeet-api-ns.service\nAfter=yeet-api-ns.service\n" +
		"[Service]\nExecStart=" + bubblewrapPath + " --uid 0 --gid 0 --ro-bind " + oldResolver + " /etc/resolv.conf -- " + payload + " --serve\n" +
		"User=root\nGroup=root\nWorkingDirectory=/\n" +
		"Environment=HOME=" + dataDir + " USER=root LOGNAME=root SHELL=/bin/sh\n" +
		"NetworkNamespacePath=/var/run/netns/target\n"
	firstStaged, err := writeFreshRegularNetworkArtifact(root, "bin", "api-network-", ".service", []byte(firstContent), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := txn.registerStagedPath(firstStaged); err != nil {
		t.Fatal(err)
	}
	target := previous.Clone()
	setRegularNetworkTargetArtifact(target, db.ArtifactSystemdUnit, firstStaged)
	setRegularNetworkTargetArtifact(target, db.ArtifactNetNSResolv, targetResolver)
	plan := &serviceNetworkMutationPlan{name: previous.Name, previous: previous, artifactTxn: txn, deferSandbox: true}
	identity := db.ServiceIdentity{
		RequestedUser: strconv.Itoa(os.Geteuid()), RequestedGroup: strconv.Itoa(os.Getegid()),
		UID: uint32(os.Geteuid()), GID: uint32(os.Getegid()),
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
	var order []string
	ensureBubblewrapForServiceSandboxMutation = func(context.Context) error {
		order = append(order, "ensure")
		return nil
	}
	validateServiceSandboxPolicyForMutation = func(req serviceSandboxPlanRequest, active bool) (serviceSandboxPolicy, error) {
		order = append(order, fmt.Sprintf("validate:%t:%d:%d:%s", active, req.UID, req.GID, req.ResolverSource))
		return req.Policy, nil
	}
	probeServiceSandboxForMutation = func(_ context.Context, plan serviceSandboxPlan, uid, gid uint32) error {
		order = append(order, fmt.Sprintf("probe:%d:%d", uid, gid))
		if !slicesContainAdjacent(plan.Arguments, "--ro-bind", targetResolver) {
			t.Fatalf("probe plan resolver = %#v, want %q", plan.Arguments, targetResolver)
		}
		return nil
	}
	verifyGeneratedSystemdUnitForSandboxMutation = func(_ context.Context, path string) error {
		order = append(order, "verify")
		if path == firstStaged {
			t.Fatal("static verification used the pre-identity staged unit")
		}
		return nil
	}

	replacement, err := server.applyServiceNetworkIdentityToUnit(context.Background(), plan, target, identity)
	if err != nil {
		t.Fatalf("apply combined network identity: %v", err)
	}
	wantOrder := []string{
		"ensure",
		fmt.Sprintf("validate:true:%d:%d:%s", identity.UID, identity.GID, targetResolver),
		fmt.Sprintf("probe:%d:%d", identity.UID, identity.GID),
		"verify",
	}
	if diff := cmp.Diff(wantOrder, order); diff != "" {
		t.Fatalf("combined sandbox preflight order mismatch (-want +got):\n%s", diff)
	}
	argv := serviceIdentityExecStartArgv(t, replacement)
	if !slicesContainAdjacent(argv, "--uid", strconv.FormatUint(uint64(identity.UID), 10)) ||
		!slicesContainAdjacent(argv, "--gid", strconv.FormatUint(uint64(identity.GID), 10)) ||
		!slicesContainAdjacent(argv, "--ro-bind", targetResolver) {
		t.Fatalf("combined replacement retained stale sandbox settings: %#v", argv)
	}
}

func TestExactReadableNetworkSandboxArtifactsRejectsUnreadableAndNonregularResolver(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "unreadable",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if os.Geteuid() == 0 {
					t.Skip("root can read a mode-000 regular file")
				}
				if err := os.WriteFile(path, []byte("nameserver 127.0.0.1\n"), 0); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "nonregular",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			payload := filepath.Join(root, "payload")
			if err := os.WriteFile(payload, []byte("payload"), 0o755); err != nil {
				t.Fatal(err)
			}
			resolver := filepath.Join(root, "selected-resolver")
			test.setup(t, resolver)
			service := &db.Service{
				Name: "api", Generation: 4,
				Artifacts: db.ArtifactStore{
					db.ArtifactBinary:      {Refs: map[db.ArtifactRef]string{db.Gen(4): payload}},
					db.ArtifactNetNSResolv: {Refs: map[db.ArtifactRef]string{db.Gen(4): resolver}},
				},
			}

			_, _, err := exactReadableNetworkSandboxArtifacts(service)
			if err == nil || !strings.Contains(err.Error(), "validate target network sandbox resolver "+resolver) {
				t.Fatalf("selected resolver validation error = %v", err)
			}
		})
	}
}

func TestRegularNetworkMutationStagesComposeOverlayWithoutOverwritingCurrentArtifact(t *testing.T) {
	server := newTestServer(t)
	root := filepath.Join(t.TempDir(), "api")
	if err := ensureDirsForRoot(root, ""); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(serviceBinDirForRoot(root), "compose.yml")
	if err := os.WriteFile(base, []byte("services:\n  web:\n    image: nginx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(serviceBinDirForRoot(root), "compose.network")
	if err := os.WriteFile(current, []byte("current overlay\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installer := &FileInstaller{
		s: server, cfg: FileInstallerCfg{InstallerCfg: InstallerCfg{ServiceName: "api"}},
		serviceRoot: root, artifacts: map[db.ArtifactName]string{db.ArtifactDockerComposeFile: base},
	}
	if err := stageRegularDockerComposeNetwork(installer, netns.Service{ServiceName: "api"}); err != nil {
		t.Fatal(err)
	}
	staged := installer.artifacts[db.ArtifactDockerComposeNetwork]
	if staged == "" || staged == current {
		t.Fatalf("staged overlay = %q, want fresh path distinct from %q", staged, current)
	}
	raw, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "current overlay\n" {
		t.Fatalf("current overlay changed during staging: %q", raw)
	}
}
