// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
	"github.com/yeetrun/yeet/pkg/netns"
	"github.com/yeetrun/yeet/pkg/svc"
	"golang.org/x/sys/unix"
)

func TestEnsureISONetworkVerifiesPolicyBeforeTopologyAttachment(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app": testISORuntimeAllocation("app", iso.StateReserved),
	})
	withISORuntimeBackend(t, netns.BackendNFT)

	want := errors.New("policy restore failed")
	oldEnsurePolicy := ensureISOPolicyForRuntime
	oldEnsureTopology := ensureISOTopologyForRuntime
	t.Cleanup(func() {
		ensureISOPolicyForRuntime = oldEnsurePolicy
		ensureISOTopologyForRuntime = oldEnsureTopology
	})
	ensureISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error { return want }
	topologyCalls := 0
	ensureISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error {
		topologyCalls++
		return nil
	}

	err := server.EnsureISONetwork(context.Background(), "app")
	if !errors.Is(err, want) {
		t.Fatalf("EnsureISONetwork error = %v, want %v", err, want)
	}
	if topologyCalls != 0 {
		t.Fatalf("topology calls = %d, want zero before policy verification", topologyCalls)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	allocation := dv.Services().Get("app").ISO()
	if allocation.State() != string(iso.StateQuarantined) || !strings.Contains(allocation.LastError(), want.Error()) {
		t.Fatalf("allocation = %#v, want quarantined policy failure", allocation.AsStruct())
	}
}

func TestEnsureISONetworkMarksAllocationAndPoolReady(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app": testISORuntimeAllocation("app", iso.StateReserved),
	})
	withISORuntimeBackend(t, netns.BackendNFT)
	oldEnsurePolicy := ensureISOPolicyForRuntime
	oldEnsureTopology := ensureISOTopologyForRuntime
	t.Cleanup(func() {
		ensureISOPolicyForRuntime = oldEnsurePolicy
		ensureISOTopologyForRuntime = oldEnsureTopology
	})
	var phases []string
	ensureISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error {
		phases = append(phases, "policy")
		return nil
	}
	ensureISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error {
		phases = append(phases, "topology")
		return nil
	}
	if err := server.EnsureISONetwork(context.Background(), "app"); err != nil {
		t.Fatalf("EnsureISONetwork returned error: %v", err)
	}
	if !slices.Equal(phases, []string{"policy", "topology"}) {
		t.Fatalf("phases = %v", phases)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got := dv.ISOPool().AggregateRouteState(); got != "ready" {
		t.Fatalf("aggregate route state = %q, want ready", got)
	}
	if got := dv.ISOPool().LastConflict(); got != "" {
		t.Fatalf("last conflict = %q, want empty", got)
	}
	allocation := dv.Services().Get("app").ISO()
	if allocation.State() != string(iso.StateReady) || allocation.LastError() != "" {
		t.Fatalf("allocation = %#v, want ready", allocation.AsStruct())
	}
}

func TestMarkISOReadyRejectsRemovalAndFailClosedStates(t *testing.T) {
	tests := []struct {
		name            string
		state           iso.AllocationState
		removeRequested bool
		cleanupVerified bool
	}{
		{name: "removal requested", state: iso.StateStopped, removeRequested: true},
		{name: "cleanup verified", state: iso.StateStopped, cleanupVerified: true},
		{name: "removing", state: iso.StateRemoving},
		{name: "tombstoned", state: iso.StateTombstoned},
		{name: "quarantined", state: iso.StateQuarantined},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allocation := testISORuntimeAllocation("app", tt.state)
			allocation.RemoveRequested = tt.removeRequested
			allocation.CleanupVerified = tt.cleanupVerified
			server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"app": allocation})

			if err := server.markISOReady("app"); err == nil {
				t.Fatal("markISOReady returned nil, want lifecycle rejection")
			}
			dv, err := server.cfg.DB.Get()
			if err != nil {
				t.Fatal(err)
			}
			got := dv.Services().Get("app").ISO()
			if got.State() != string(tt.state) || got.RemoveRequested() != tt.removeRequested || got.CleanupVerified() != tt.cleanupVerified {
				t.Fatalf("allocation = %#v, want lifecycle state preserved", got.AsStruct())
			}
			if got := dv.ISOPool().AggregateRouteState(); got == "ready" {
				t.Fatalf("aggregate route state = %q, want unchanged", got)
			}
		})
	}
}

func TestEnsureISONetworkBoundaryDoesNotMarkReadyBeforeRuntimeInspection(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app": testISORuntimeAllocation("app", iso.StateReserved),
	})
	withISORuntimeBackend(t, netns.BackendNFT)
	withSuccessfulISORuntimeCommands(t)

	if err := server.EnsureISONetworkBoundary(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if state := dv.Services().Get("app").ISO().State(); state == string(iso.StateReady) {
		t.Fatal("boundary-only gate persisted ready before workload inspection")
	}
}

func TestEnsureISONetworkGlobalPolicyIncludesEveryNonCleanedAllocation(t *testing.T) {
	allocations := map[string]*db.ISOAllocation{
		"reserved":    testISORuntimeAllocation("reserved", iso.StateReserved),
		"ready":       testISORuntimeAllocation("ready", iso.StateReady),
		"stopped":     testISORuntimeAllocation("stopped", iso.StateStopped),
		"removing":    testISORuntimeAllocation("removing", iso.StateRemoving),
		"quarantined": testISORuntimeAllocation("quarantined", iso.StateQuarantined),
		"tombstoned":  testISORuntimeAllocation("tombstoned", iso.StateTombstoned),
		"cleaned": func() *db.ISOAllocation {
			a := testISORuntimeAllocation("cleaned", iso.StateTombstoned)
			a.RemoveRequested = true
			a.CleanupVerified = true
			return a
		}(),
	}
	server := newISORuntimeTestServer(t, allocations)
	withISORuntimeBackend(t, netns.BackendNFT)
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := server.isoRuntimeSpec(dv, "ready")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, endpoint := range spec.Policy.Endpoints {
		got = append(got, endpoint.Interface)
	}
	for _, name := range []string{"reserved", "ready", "stopped", "removing", "quarantined", "tombstoned"} {
		want := allocations[name].Interface
		if !slices.Contains(got, want) {
			t.Errorf("global endpoints %v missing %q", got, want)
		}
	}
	if slices.Contains(got, allocations["cleaned"].Interface) {
		t.Fatalf("global endpoints include fully cleaned allocation: %v", got)
	}
}

func TestEnsureISONetworkMarksPoolConflictWhenTopologyVerificationFails(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app": testISORuntimeAllocation("app", iso.StateReserved),
	})
	withISORuntimeBackend(t, netns.BackendNFT)
	oldEnsurePolicy := ensureISOPolicyForRuntime
	oldEnsureTopology := ensureISOTopologyForRuntime
	t.Cleanup(func() {
		ensureISOPolicyForRuntime = oldEnsurePolicy
		ensureISOTopologyForRuntime = oldEnsureTopology
	})
	ensureISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error { return nil }
	want := errors.New("aggregate route verification failed")
	ensureISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error { return want }

	if err := server.EnsureISONetwork(context.Background(), "app"); !errors.Is(err, want) {
		t.Fatalf("EnsureISONetwork error = %v, want %v", err, want)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got := dv.ISOPool().AggregateRouteState(); got != "conflict" {
		t.Fatalf("aggregate route state = %q, want conflict", got)
	}
	if got := dv.ISOPool().LastConflict(); got != want.Error() {
		t.Fatalf("last conflict = %q, want %q", got, want)
	}
	if got := dv.Services().Get("app").ISO().State(); got != string(iso.StateQuarantined) {
		t.Fatalf("allocation state = %q, want quarantined", got)
	}
}

func TestEnsureISONetworkGlobalPolicyFailureQuarantinesEveryIncludedAllocation(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app":   testISORuntimeAllocation("app", iso.StateReserved),
		"other": testISORuntimeAllocation("other", iso.StateReady),
	})
	withISORuntimeBackend(t, netns.BackendNFT)
	oldEnsurePolicy := ensureISOPolicyForRuntime
	oldEnsureTopology := ensureISOTopologyForRuntime
	t.Cleanup(func() {
		ensureISOPolicyForRuntime = oldEnsurePolicy
		ensureISOTopologyForRuntime = oldEnsureTopology
	})
	want := errors.New("global policy digest mismatch")
	ensureISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error { return want }
	ensureISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error {
		t.Fatal("topology ran after global policy failure")
		return nil
	}
	if err := server.EnsureISONetwork(context.Background(), "app"); !errors.Is(err, want) {
		t.Fatalf("EnsureISONetwork error = %v, want %v", err, want)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if dv.ISOPool().AggregateRouteState() != "conflict" || dv.ISOPool().LastConflict() != want.Error() {
		t.Fatalf("pool = %#v, want global conflict", dv.ISOPool().AsStruct())
	}
	for _, name := range []string{"app", "other"} {
		if got := dv.Services().Get(name).ISO().State(); got != string(iso.StateQuarantined) {
			t.Errorf("%s state = %q, want quarantined", name, got)
		}
	}
}

func TestEnsureISONetworkDoesNotClearConflictUntilSiblingTopologyVerifies(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app":   testISORuntimeAllocation("app", iso.StateReserved),
		"other": testISORuntimeAllocation("other", iso.StateReady),
	})
	withISORuntimeBackend(t, netns.BackendNFT)
	oldEnsurePolicy := ensureISOPolicyForRuntime
	oldEnsureTopology := ensureISOTopologyForRuntime
	oldVerifyTopology := verifyISOTopologyForRuntime
	t.Cleanup(func() {
		ensureISOPolicyForRuntime = oldEnsurePolicy
		ensureISOTopologyForRuntime = oldEnsureTopology
		verifyISOTopologyForRuntime = oldVerifyTopology
	})
	ensureISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error { return nil }
	ensureISOTopologyForRuntime = func(_ context.Context, spec netns.ISOTopologySpec) error {
		if spec.Allocation.Interface != testISORuntimeAllocation("app", iso.StateReserved).Interface {
			t.Fatalf("ensured non-target topology %q", spec.Allocation.Interface)
		}
		return nil
	}
	want := errors.New("sibling project route drift")
	verifyCalls := 0
	verifyISOTopologyForRuntime = func(_ context.Context, spec netns.ISOTopologySpec) error {
		verifyCalls++
		if spec.Allocation.Interface == testISORuntimeAllocation("other", iso.StateReady).Interface {
			return want
		}
		return nil
	}
	if err := server.EnsureISONetwork(context.Background(), "app"); !errors.Is(err, want) {
		t.Fatalf("EnsureISONetwork error = %v, want %v", err, want)
	}
	if verifyCalls != 1 {
		t.Fatalf("sibling topology verify calls = %d, want 1", verifyCalls)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if dv.ISOPool().AggregateRouteState() != "conflict" || dv.ISOPool().LastConflict() != want.Error() {
		t.Fatalf("pool = %#v, want sibling conflict", dv.ISOPool().AsStruct())
	}
	for _, name := range []string{"app", "other"} {
		if got := dv.Services().Get(name).ISO().State(); got != string(iso.StateQuarantined) {
			t.Errorf("%s state = %q, want quarantined", name, got)
		}
	}
}

func TestEnsureISONetworkRejectsMalformedOrCollidingPersistedAllocationsBeforeCommands(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(map[string]*db.ISOAllocation)
	}{
		{name: "generated identity drift", edit: func(allocations map[string]*db.ISOAllocation) {
			allocations["app"].Interface = "yi-wrong"
		}},
		{name: "link outside lower half", edit: func(allocations map[string]*db.ISOAllocation) {
			allocations["app"].Link = netip.MustParsePrefix("172.30.128.0/30")
			allocations["app"].HostIP = netip.MustParseAddr("172.30.128.1")
			allocations["app"].PeerIP = netip.MustParseAddr("172.30.128.2")
		}},
		{name: "duplicate link route", edit: func(allocations map[string]*db.ISOAllocation) {
			allocations["other"].Link = allocations["app"].Link
			allocations["other"].HostIP = allocations["app"].HostIP
			allocations["other"].PeerIP = allocations["app"].PeerIP
		}},
		{name: "duplicate project route", edit: func(allocations map[string]*db.ISOAllocation) {
			allocations["other"].Project = allocations["app"].Project
			allocations["other"].Gateway = allocations["app"].Gateway
		}},
		{name: "duplicate interface identity", edit: func(allocations map[string]*db.ISOAllocation) {
			allocations["other"].Interface = allocations["app"].Interface
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			allocations := map[string]*db.ISOAllocation{
				"app":   testISOAllocatorRuntimeAllocation(t, "app", 1, 1),
				"other": testISOAllocatorRuntimeAllocation(t, "other", 2, 2),
			}
			tt.edit(allocations)
			server := newISORuntimeTestServer(t, allocations)
			withISORuntimeBackend(t, netns.BackendNFT)
			oldEnsurePolicy := ensureISOPolicyForRuntime
			oldEnsureTopology := ensureISOTopologyForRuntime
			t.Cleanup(func() {
				ensureISOPolicyForRuntime = oldEnsurePolicy
				ensureISOTopologyForRuntime = oldEnsureTopology
			})
			calls := 0
			ensureISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error { calls++; return nil }
			ensureISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error { calls++; return nil }

			if err := server.EnsureISONetwork(context.Background(), "app"); err == nil {
				t.Fatal("EnsureISONetwork returned nil error")
			}
			if calls != 0 {
				t.Fatalf("privileged calls = %d, want zero", calls)
			}
			dv, err := server.cfg.DB.Get()
			if err != nil {
				t.Fatal(err)
			}
			if dv.ISOPool().AggregateRouteState() != "conflict" || dv.ISOPool().LastConflict() == "" {
				t.Fatalf("pool = %#v, want persisted conflict", dv.ISOPool().AsStruct())
			}
			for _, name := range []string{"app", "other"} {
				if got := dv.Services().Get(name).ISO().State(); got != string(iso.StateQuarantined) {
					t.Errorf("%s state = %q, want quarantined", name, got)
				}
			}
		})
	}
}

func TestEnsureISONetworkVMUsesOnlyAggregateTopologyAndRejectsRouterShape(t *testing.T) {
	vm := testISOAllocatorRuntimeAllocation(t, "vm", 4, 0)
	vm.Kind = string(iso.PayloadVM)
	vm.Project = netip.Prefix{}
	vm.Gateway = netip.Addr{}
	vm.NetNS = ""
	vm.Bridge = ""
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"vm": vm})
	withISORuntimeBackend(t, netns.BackendNFT)
	oldEnsurePolicy := ensureISOPolicyForRuntime
	oldEnsureTopology := ensureISOTopologyForRuntime
	t.Cleanup(func() {
		ensureISOPolicyForRuntime = oldEnsurePolicy
		ensureISOTopologyForRuntime = oldEnsureTopology
	})
	ensureISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error { return nil }
	topologyCalls := 0
	ensureISOTopologyForRuntime = func(_ context.Context, spec netns.ISOTopologySpec) error {
		topologyCalls++
		if spec.Allocation.Kind != string(iso.PayloadVM) || spec.Allocation.Project.IsValid() || spec.Allocation.NetNS != "" {
			t.Fatalf("VM topology was not reduced: %#v", spec.Allocation)
		}
		return nil
	}
	if err := server.EnsureISONetwork(context.Background(), "vm"); err != nil {
		t.Fatal(err)
	}
	if topologyCalls != 1 {
		t.Fatalf("VM aggregate topology calls = %d, want 1", topologyCalls)
	}
}

func TestCleanISONetworkVerifiesAbsenceBeforePolicyRemoval(t *testing.T) {
	app := testISORuntimeAllocation("app", iso.StateReady)
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app": app,
	})
	withISORuntimeBackend(t, netns.BackendNFT)
	oldRemove := removeISOTopologyForRuntime
	oldEnsurePolicy := ensureISOPolicyForRuntime
	t.Cleanup(func() {
		removeISOTopologyForRuntime = oldRemove
		ensureISOPolicyForRuntime = oldEnsurePolicy
	})
	var phases []string
	removeISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error {
		phases = append(phases, "topology-absent")
		return nil
	}
	ensureISOPolicyForRuntime = func(_ context.Context, rules netns.ISOPolicyRules) error {
		phases = append(phases, "policy-without-endpoint")
		if strings.Contains(rules.IPv4, app.Interface) {
			t.Fatalf("cleaned endpoint remains in rendered policy:\n%s", rules.IPv4)
		}
		return nil
	}
	if err := server.CleanISONetwork(context.Background(), "app"); err != nil {
		t.Fatalf("CleanISONetwork returned error: %v", err)
	}
	if !slices.Equal(phases, []string{"topology-absent", "policy-without-endpoint"}) {
		t.Fatalf("phases = %v", phases)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	allocation := dv.Services().Get("app").ISO()
	if !allocation.RemoveRequested() || !allocation.CleanupVerified() || allocation.State() != string(iso.StateTombstoned) {
		t.Fatalf("clean allocation = %#v", allocation.AsStruct())
	}
}

func TestCleanISONetworkRetainsUnverifiedTombstoneOnRemovalFailure(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app": testISORuntimeAllocation("app", iso.StateReady),
	})
	withISORuntimeBackend(t, netns.BackendNFT)
	oldRemove := removeISOTopologyForRuntime
	oldEnsurePolicy := ensureISOPolicyForRuntime
	t.Cleanup(func() {
		removeISOTopologyForRuntime = oldRemove
		ensureISOPolicyForRuntime = oldEnsurePolicy
	})
	want := errors.New("veth remains")
	removeISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error { return want }
	ensureISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error {
		t.Fatal("policy must retain an endpoint whose cleanup was not verified")
		return nil
	}
	if err := server.CleanISONetwork(context.Background(), "app"); !errors.Is(err, want) {
		t.Fatalf("CleanISONetwork error = %v, want %v", err, want)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	allocation := dv.Services().Get("app").ISO()
	if !allocation.RemoveRequested() || allocation.CleanupVerified() || allocation.State() != string(iso.StateTombstoned) {
		t.Fatalf("failed cleanup allocation = %#v", allocation.AsStruct())
	}
}

func TestCleanISONetworkPolicyFailureQuarantinesEveryRemainingAllocation(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app":   testISORuntimeAllocation("app", iso.StateReady),
		"other": testISORuntimeAllocation("other", iso.StateReady),
	})
	withISORuntimeBackend(t, netns.BackendNFT)
	oldRemove := removeISOTopologyForRuntime
	oldEnsurePolicy := ensureISOPolicyForRuntime
	t.Cleanup(func() {
		removeISOTopologyForRuntime = oldRemove
		ensureISOPolicyForRuntime = oldEnsurePolicy
	})
	removeISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error { return nil }
	want := errors.New("global policy replacement failed")
	ensureISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error { return want }
	if err := server.CleanISONetwork(context.Background(), "app"); !errors.Is(err, want) {
		t.Fatalf("CleanISONetwork error = %v, want %v", err, want)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if dv.ISOPool().AggregateRouteState() != "conflict" || dv.ISOPool().LastConflict() != want.Error() {
		t.Fatalf("pool = %#v, want cleanup policy conflict", dv.ISOPool().AsStruct())
	}
	app := dv.Services().Get("app").ISO()
	if app.State() != string(iso.StateTombstoned) || !strings.Contains(app.LastError(), want.Error()) {
		t.Fatalf("cleaned target = %#v, want retained tombstone", app.AsStruct())
	}
	other := dv.Services().Get("other").ISO()
	if other.State() != string(iso.StateQuarantined) || !strings.Contains(other.LastError(), want.Error()) {
		t.Fatalf("remaining allocation = %#v, want quarantined", other.AsStruct())
	}
}

func TestISOOperationFileLockSerializesIndependentClientsAndHonorsContext(t *testing.T) {
	root := canonicalISORuntimeTempDir(t)
	unlockFirst, err := acquireISOOperationLock(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	firstHeld := true
	t.Cleanup(func() {
		if firstHeld {
			unlockFirst()
		}
	})

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancelResult := make(chan error, 1)
	cancelStarted := make(chan struct{})
	go func() {
		close(cancelStarted)
		unlock, lockErr := acquireISOOperationLock(cancelCtx, root)
		if unlock != nil {
			unlock()
		}
		cancelResult <- lockErr
	}()
	<-cancelStarted
	select {
	case err := <-cancelResult:
		t.Fatalf("second lock client entered while first held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	if err := <-cancelResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lock error = %v, want context.Canceled", err)
	}

	acquired := make(chan func(), 1)
	waitResult := make(chan error, 1)
	go func() {
		unlock, lockErr := acquireISOOperationLock(context.Background(), root)
		if lockErr != nil {
			waitResult <- lockErr
			return
		}
		acquired <- unlock
	}()
	select {
	case unlock := <-acquired:
		unlock()
		t.Fatal("second independent file-open client acquired before release")
	case err := <-waitResult:
		t.Fatalf("waiting lock client failed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	unlockFirst()
	firstHeld = false
	select {
	case unlock := <-acquired:
		unlock()
	case err := <-waitResult:
		t.Fatalf("waiting lock client failed after release: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("waiting lock client did not acquire after release")
	}

	info, err := os.Stat(filepath.Join(root, isoOperationLockFileName))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("ISO operation lock mode = %o, want 600", got)
	}
}

func TestISOOperationFileLockDoesNotSerializeDifferentRoots(t *testing.T) {
	unlockFirst, err := acquireISOOperationLock(context.Background(), canonicalISORuntimeTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer unlockFirst()
	unlockSecond, err := acquireISOOperationLock(context.Background(), canonicalISORuntimeTempDir(t))
	if err != nil {
		t.Fatalf("different RootDir was blocked: %v", err)
	}
	unlockSecond()
}

func TestISOOperationFileLockRejectsUnsafeRootDirs(t *testing.T) {
	for _, root := range []string{"", "relative", string(filepath.Separator)} {
		t.Run(strings.ReplaceAll(root, string(filepath.Separator), "root"), func(t *testing.T) {
			if unlock, err := acquireISOOperationLock(context.Background(), root); err == nil {
				unlock()
				t.Fatalf("acquireISOOperationLock(%q) returned nil error", root)
			}
		})
	}
	writableRoot := canonicalISORuntimeTempDir(t)
	if err := os.Chmod(writableRoot, 0o777); err != nil {
		t.Fatal(err)
	}
	if unlock, err := acquireISOOperationLock(context.Background(), writableRoot); err == nil {
		unlock()
		t.Fatal("acquireISOOperationLock accepted a group/world-writable RootDir")
	}
}

func TestISOOperationFileLockRejectsConfiguredPathSymlinks(t *testing.T) {
	t.Run("final component", func(t *testing.T) {
		parent := canonicalISORuntimeTempDir(t)
		target := canonicalISORuntimeTempDir(t)
		alias := filepath.Join(parent, "root-alias")
		if err := os.Symlink(target, alias); err != nil {
			t.Fatal(err)
		}
		if unlock, err := acquireISOOperationLock(context.Background(), alias); err == nil {
			unlock()
			t.Fatal("acquireISOOperationLock accepted a final RootDir symlink")
		}
	})

	t.Run("intermediate component", func(t *testing.T) {
		parent := canonicalISORuntimeTempDir(t)
		target := canonicalISORuntimeTempDir(t)
		root := filepath.Join(target, "root")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(parent, "parent-alias")
		if err := os.Symlink(target, alias); err != nil {
			t.Fatal(err)
		}
		if unlock, err := acquireISOOperationLock(context.Background(), filepath.Join(alias, "root")); err == nil {
			unlock()
			t.Fatal("acquireISOOperationLock accepted an intermediate RootDir symlink")
		}
	})
}

func TestISOOperationFileLockCannotSplitAcrossAlternatingConfiguredSymlink(t *testing.T) {
	parent := canonicalISORuntimeTempDir(t)
	firstTarget := canonicalISORuntimeTempDir(t)
	secondTarget := canonicalISORuntimeTempDir(t)
	alias := filepath.Join(parent, "root-alias")
	if err := os.Symlink(firstTarget, alias); err != nil {
		t.Fatal(err)
	}
	unlockFirst, firstErr := acquireISOOperationLock(context.Background(), alias)
	if firstErr != nil {
		return
	}
	defer unlockFirst()
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secondTarget, alias); err != nil {
		t.Fatal(err)
	}
	if unlockSecond, err := acquireISOOperationLock(context.Background(), alias); err == nil {
		unlockSecond()
		t.Fatal("alternating one configured RootDir symlink produced two accepted lock domains")
	}
}

func TestISOOperationFileLockRejectsPermissiveExistingModeWithoutRepair(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o666} {
		t.Run(mode.String(), func(t *testing.T) {
			root := canonicalISORuntimeTempDir(t)
			lockPath := filepath.Join(root, isoOperationLockFileName)
			if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(lockPath, mode); err != nil {
				t.Fatal(err)
			}
			if unlock, err := acquireISOOperationLock(context.Background(), root); err == nil {
				unlock()
				t.Fatalf("acquireISOOperationLock accepted existing mode %o", mode)
			}
			info, err := os.Stat(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != mode {
				t.Fatalf("existing lock mode repaired from %o to %o; want rejection without mutation", mode, got)
			}
		})
	}
}

func TestOpenISOOperationLockFDAtFailsClosedWhenExistingEntryDisappears(t *testing.T) {
	var flags []int
	openAt := func(_ int, _ string, flag int, _ uint32) (int, error) {
		flags = append(flags, flag)
		if len(flags) == 1 {
			return -1, unix.EEXIST
		}
		return -1, unix.ENOENT
	}
	fd, created, err := openISOOperationLockFDAt(42, openAt)
	if fd != -1 || created || !errors.Is(err, unix.ENOENT) {
		t.Fatalf("openISOOperationLockFDAt() = (%d, %v, %v), want (-1, false, ENOENT)", fd, created, err)
	}
	if len(flags) != 2 {
		t.Fatalf("openat calls = %d, want create-exclusive then existing open", len(flags))
	}
	if flags[0]&unix.O_CREAT == 0 || flags[0]&unix.O_EXCL == 0 {
		t.Fatalf("first openat flags = %#x, want O_CREAT|O_EXCL", flags[0])
	}
	if flags[1]&(unix.O_CREAT|unix.O_EXCL) != 0 {
		t.Fatalf("existing openat flags = %#x, want neither O_CREAT nor O_EXCL", flags[1])
	}
}

func TestValidateISOOperationDirectoryStatRejectsUnsafeOwnershipAndAncestors(t *testing.T) {
	effectiveUID := uint32(os.Geteuid())
	otherUID := effectiveUID + 1
	for _, test := range []struct {
		name  string
		stat  isoOperationInodeStat
		final bool
		want  bool
	}{
		{name: "final wrong owner at 0700", stat: isoOperationInodeStat{mode: unix.S_IFDIR | 0o700, uid: otherUID, nlink: 1}, final: true, want: false},
		{name: "ancestor wrong owner", stat: isoOperationInodeStat{mode: unix.S_IFDIR | 0o755, uid: otherUID, nlink: 1}, want: false},
		{name: "unsafe writable ancestor", stat: isoOperationInodeStat{mode: unix.S_IFDIR | 0o777, uid: 0, nlink: 1}, want: false},
		{name: "root sticky ancestor", stat: isoOperationInodeStat{mode: unix.S_IFDIR | unix.S_ISVTX | 0o777, uid: 0, nlink: 1}, want: true},
		{name: "effective user sticky ancestor", stat: isoOperationInodeStat{mode: unix.S_IFDIR | unix.S_ISVTX | 0o770, uid: effectiveUID, nlink: 1}, want: true},
		{name: "sticky final remains unsafe", stat: isoOperationInodeStat{mode: unix.S_IFDIR | unix.S_ISVTX | 0o777, uid: effectiveUID, nlink: 1}, final: true, want: false},
		{name: "non directory", stat: isoOperationInodeStat{mode: unix.S_IFREG | 0o700, uid: effectiveUID, nlink: 1}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateISOOperationDirectoryStat("component", test.stat, effectiveUID, test.final)
			if (err == nil) != test.want {
				t.Fatalf("validateISOOperationDirectoryStat() error = %v, want valid %v", err, test.want)
			}
		})
	}
}

func TestISOOperationFileLockValidatesEveryAncestor(t *testing.T) {
	unsafeParent := filepath.Join(canonicalISORuntimeTempDir(t), "unsafe")
	if err := os.Mkdir(unsafeParent, 0o700); err != nil {
		t.Fatal(err)
	}
	unsafeChild := filepath.Join(unsafeParent, "root")
	if err := os.Mkdir(unsafeChild, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafeParent, 0o777); err != nil {
		t.Fatal(err)
	}
	if unlock, err := acquireISOOperationLock(context.Background(), unsafeChild); err == nil {
		unlock()
		t.Fatal("acquireISOOperationLock accepted a non-sticky world-writable ancestor")
	}

	stickyParent := filepath.Join(canonicalISORuntimeTempDir(t), "sticky")
	if err := os.Mkdir(stickyParent, 0o700); err != nil {
		t.Fatal(err)
	}
	stickyChild := filepath.Join(stickyParent, "root")
	if err := os.Mkdir(stickyChild, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stickyParent, os.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	unlock, err := acquireISOOperationLock(context.Background(), stickyChild)
	if err != nil {
		t.Fatalf("safe sticky ancestor rejected: %v", err)
	}
	unlock()
}

func TestValidateISOOperationLockStatRejectsUnsafeInodes(t *testing.T) {
	effectiveUID := uint32(os.Geteuid())
	for _, test := range []struct {
		name string
		stat isoOperationInodeStat
		want bool
	}{
		{name: "regular owned single link", stat: isoOperationInodeStat{mode: unix.S_IFREG | 0o600, uid: effectiveUID, nlink: 1}, want: true},
		{name: "wrong owner", stat: isoOperationInodeStat{mode: unix.S_IFREG | 0o600, uid: effectiveUID + 1, nlink: 1}, want: false},
		{name: "hardlinked", stat: isoOperationInodeStat{mode: unix.S_IFREG | 0o600, uid: effectiveUID, nlink: 2}, want: false},
		{name: "directory", stat: isoOperationInodeStat{mode: unix.S_IFDIR | 0o600, uid: effectiveUID, nlink: 1}, want: false},
		{name: "fifo", stat: isoOperationInodeStat{mode: unix.S_IFIFO | 0o600, uid: effectiveUID, nlink: 1}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateISOOperationLockStat("lock", test.stat, effectiveUID)
			if (err == nil) != test.want {
				t.Fatalf("validateISOOperationLockStat() error = %v, want valid %v", err, test.want)
			}
		})
	}
}

func TestISOOperationFileLockRejectsHardlinkAndSymlink(t *testing.T) {
	t.Run("hardlink", func(t *testing.T) {
		root := canonicalISORuntimeTempDir(t)
		lockPath := filepath.Join(root, isoOperationLockFileName)
		if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(lockPath, filepath.Join(root, "other-link")); err != nil {
			t.Fatal(err)
		}
		if unlock, err := acquireISOOperationLock(context.Background(), root); err == nil {
			unlock()
			t.Fatal("acquireISOOperationLock accepted a multiply linked lock inode")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		root := canonicalISORuntimeTempDir(t)
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, isoOperationLockFileName)); err != nil {
			t.Fatal(err)
		}
		if unlock, err := acquireISOOperationLock(context.Background(), root); err == nil {
			unlock()
			t.Fatal("acquireISOOperationLock accepted a symlink lock path")
		}
	})
}

func TestISOOperationLockOpenUsesValidatedDirectoryDescriptor(t *testing.T) {
	parent := canonicalISORuntimeTempDir(t)
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, _, err := openValidatedISOOperationRootDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	moved := filepath.Join(parent, "validated-root")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	file, _, err := openISOOperationLockFileAt(int(dir.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moved, isoOperationLockFileName)); err != nil {
		t.Fatalf("validated directory did not receive lock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, isoOperationLockFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement path received lock: %v", err)
	}
}

func TestISOOperationUnlockRunsOnlyOnce(t *testing.T) {
	var unlockCalls atomic.Int32
	var closeCalls atomic.Int32
	unlock := newISOOperationUnlock(
		func() { unlockCalls.Add(1) },
		func() { closeCalls.Add(1) },
	)
	unlock()
	unlock()
	if got := unlockCalls.Load(); got != 1 {
		t.Fatalf("kernel unlock calls = %d, want 1", got)
	}
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("file close calls = %d, want 1", got)
	}
}

func TestISONetworkOperationsFailClosedWhenProcessLockAcquisitionFails(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app": testISORuntimeAllocation("app", iso.StateReady),
	})
	want := errors.New("ISO operation flock denied")
	oldAcquire := acquireISOOperationLockForRuntime
	oldEnsurePolicy := ensureISOPolicyForRuntime
	oldEnsureTopology := ensureISOTopologyForRuntime
	oldRemoveTopology := removeISOTopologyForRuntime
	t.Cleanup(func() {
		acquireISOOperationLockForRuntime = oldAcquire
		ensureISOPolicyForRuntime = oldEnsurePolicy
		ensureISOTopologyForRuntime = oldEnsureTopology
		removeISOTopologyForRuntime = oldRemoveTopology
	})
	acquireISOOperationLockForRuntime = func(context.Context, string) (func(), error) { return nil, want }
	commands := 0
	ensureISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error { commands++; return nil }
	ensureISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error { commands++; return nil }
	removeISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error { commands++; return nil }

	for _, operation := range []struct {
		name string
		run  func() error
	}{
		{name: "ensure", run: func() error { return server.EnsureISONetwork(context.Background(), "app") }},
		{name: "clean", run: func() error { return server.CleanISONetwork(context.Background(), "app") }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.run(); !errors.Is(err, want) {
				t.Fatalf("operation error = %v, want exact lock error %v", err, want)
			}
		})
	}
	if commands != 0 {
		t.Fatalf("policy/topology commands after lock failure = %d, want zero", commands)
	}
}

func TestISONetworkLocalWrappersRejectMissingRootDirBeforeCommands(t *testing.T) {
	root := canonicalISORuntimeTempDir(t)
	store := db.NewStore(filepath.Join(root, "db.json"), filepath.Join(root, "services"))
	cfg := &Config{DB: store}
	oldDetect := detectISOFirewallBackendForRuntime
	detectCalls := 0
	detectISOFirewallBackendForRuntime = func() (netns.FirewallBackend, error) {
		detectCalls++
		return netns.BackendNFT, nil
	}
	t.Cleanup(func() { detectISOFirewallBackendForRuntime = oldDetect })
	for _, operation := range []struct {
		name string
		run  func() error
	}{
		{name: "ensure", run: func() error { return EnsureISONetwork(context.Background(), cfg, "app") }},
		{name: "clean", run: func() error { return CleanISONetwork(context.Background(), cfg, "app") }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.run(); err == nil || !strings.Contains(err.Error(), "RootDir") {
				t.Fatalf("operation error = %v, want missing RootDir failure", err)
			}
		})
	}
	if detectCalls != 0 {
		t.Fatalf("backend detection calls = %d, want zero before valid RootDir lock", detectCalls)
	}
}

func TestISONetworkOperationLockIsReleasedAfterEnsureAndCleanErrors(t *testing.T) {
	for _, operation := range []struct {
		name  string
		state iso.AllocationState
		run   func(*Server, error) error
	}{
		{name: "ensure policy", state: iso.StateReserved, run: func(server *Server, want error) error {
			ensureISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error { return want }
			return server.EnsureISONetwork(context.Background(), "app")
		}},
		{name: "clean topology", state: iso.StateReady, run: func(server *Server, want error) error {
			removeISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error { return want }
			return server.CleanISONetwork(context.Background(), "app")
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
				"app": testISORuntimeAllocation("app", operation.state),
			})
			withISORuntimeBackend(t, netns.BackendNFT)
			oldEnsurePolicy := ensureISOPolicyForRuntime
			oldRemoveTopology := removeISOTopologyForRuntime
			t.Cleanup(func() {
				ensureISOPolicyForRuntime = oldEnsurePolicy
				removeISOTopologyForRuntime = oldRemoveTopology
			})
			want := errors.New("transaction failed")
			if err := operation.run(server, want); !errors.Is(err, want) {
				t.Fatalf("operation error = %v, want %v", err, want)
			}
			unlock, err := acquireISOOperationLock(context.Background(), server.cfg.RootDir)
			if err != nil {
				t.Fatalf("lock remained held after error: %v", err)
			}
			unlock()
		})
	}
}

func TestISONetworkOperationLockPreventsInterleavedEnsureCleanStalePolicy(t *testing.T) {
	app := testISORuntimeAllocation("app", iso.StateReady)
	other := testISORuntimeAllocation("other", iso.StateReserved)
	cleanServer := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"app": app, "other": other})
	ensureServer := &Server{cfg: cleanServer.cfg}
	withISORuntimeBackend(t, netns.BackendNFT)
	oldAcquire := acquireISOOperationLockForRuntime
	oldEnsurePolicy := ensureISOPolicyForRuntime
	oldEnsureTopology := ensureISOTopologyForRuntime
	oldRemoveTopology := removeISOTopologyForRuntime
	t.Cleanup(func() {
		acquireISOOperationLockForRuntime = oldAcquire
		ensureISOPolicyForRuntime = oldEnsurePolicy
		ensureISOTopologyForRuntime = oldEnsureTopology
		removeISOTopologyForRuntime = oldRemoveTopology
	})

	var lockAttempts atomic.Int32
	secondLockAttempted := make(chan struct{})
	acquireISOOperationLockForRuntime = func(ctx context.Context, root string) (func(), error) {
		if lockAttempts.Add(1) == 2 {
			close(secondLockAttempted)
		}
		return acquireISOOperationLock(ctx, root)
	}
	removeEntered := make(chan struct{})
	allowRemove := make(chan struct{})
	removeISOTopologyForRuntime = func(ctx context.Context, _ netns.ISOTopologySpec) error {
		close(removeEntered)
		select {
		case <-allowRemove:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	policyCalls := make(chan netns.ISOPolicyRules, 2)
	ensureISOPolicyForRuntime = func(_ context.Context, rules netns.ISOPolicyRules) error {
		policyCalls <- rules
		return nil
	}
	ensureISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error { return nil }

	cleanResult := make(chan error, 1)
	go func() { cleanResult <- cleanServer.CleanISONetwork(context.Background(), "app") }()
	<-removeEntered
	ensureResult := make(chan error, 1)
	go func() { ensureResult <- ensureServer.EnsureISONetwork(context.Background(), "other") }()
	<-secondLockAttempted
	select {
	case rules := <-policyCalls:
		t.Fatalf("second transaction applied policy while cleanup held lock:\n%s", rules.IPv4)
	case <-time.After(100 * time.Millisecond):
	}
	close(allowRemove)
	if err := <-cleanResult; err != nil {
		t.Fatalf("CleanISONetwork: %v", err)
	}
	if err := <-ensureResult; err != nil {
		t.Fatalf("EnsureISONetwork: %v", err)
	}
	for call := 1; call <= 2; call++ {
		select {
		case rules := <-policyCalls:
			if !strings.Contains(rules.IPv4, other.Interface) || strings.Contains(rules.IPv4, app.Interface) {
				t.Fatalf("policy call %d did not use cleaned final snapshot:\n%s", call, rules.IPv4)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for policy call %d", call)
		}
	}
	dv, err := cleanServer.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if allocation := dv.Services().Get("app").ISO(); !allocation.CleanupVerified() || allocation.State() != string(iso.StateTombstoned) {
		t.Fatalf("cleanup final state = %#v", allocation.AsStruct())
	}
	if state := dv.Services().Get("other").ISO().State(); state != string(iso.StateReady) {
		t.Fatalf("ensured allocation state = %q, want ready", state)
	}
}

func newISORuntimeTestServer(t *testing.T, allocations map[string]*db.ISOAllocation) *Server {
	t.Helper()
	root := canonicalISORuntimeTempDir(t)
	store := db.NewStore(filepath.Join(root, "db.json"), filepath.Join(root, "services"))
	_, err := store.MutateData(func(data *db.Data) error {
		data.ISOPool = &db.ISOPool{
			Prefix:           netip.MustParsePrefix("172.30.0.0/16"),
			AllocatorVersion: iso.AllocatorVersion,
			PolicyVersion:    iso.PolicyVersion,
		}
		data.Services = map[string]*db.Service{}
		for name, allocation := range allocations {
			data.Services[name] = &db.Service{Name: name, ISO: allocation}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Server{cfg: Config{DB: store, RootDir: root, ServicesRoot: filepath.Join(root, "services")}}
}

func canonicalISORuntimeTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func testISORuntimeAllocation(name string, state iso.AllocationState) *db.ISOAllocation {
	layout, _ := iso.NewLayout(netip.MustParsePrefix("172.30.0.0/16"))
	sum := sha256.Sum256([]byte(name))
	link, _ := layout.Link(int(binary.BigEndian.Uint16(sum[:2])) % iso.MaxLinks)
	project, _ := layout.Project(int(binary.BigEndian.Uint16(sum[2:4])) % iso.MaxProjects)
	allocation := newDBISOAllocation(name, isoReservationRequest{Kind: iso.PayloadCompose, Modes: []string{"iso"}}, link)
	allocation.State = string(state)
	allocation.Project = project
	allocation.Gateway = project.Addr().Next()
	allocation.Bridge = "br0"
	return allocation
}

func testISOAllocatorRuntimeAllocation(t *testing.T, name string, linkIndex, projectIndex int) *db.ISOAllocation {
	t.Helper()
	layout, err := iso.NewLayout(netip.MustParsePrefix("172.30.0.0/16"))
	if err != nil {
		t.Fatal(err)
	}
	link, err := layout.Link(linkIndex)
	if err != nil {
		t.Fatal(err)
	}
	allocation := newDBISOAllocation(name, isoReservationRequest{Kind: iso.PayloadCompose, Modes: []string{"iso"}}, link)
	project, err := layout.Project(projectIndex)
	if err != nil {
		t.Fatal(err)
	}
	allocation.Project = project
	allocation.Gateway = project.Addr().Next()
	allocation.Bridge = "br0"
	return allocation
}

func withISORuntimeBackend(t *testing.T, backend netns.FirewallBackend) {
	t.Helper()
	old := detectISOFirewallBackendForRuntime
	detectISOFirewallBackendForRuntime = func() (netns.FirewallBackend, error) { return backend, nil }
	t.Cleanup(func() { detectISOFirewallBackendForRuntime = old })
}

func withSuccessfulISORuntimeCommands(t *testing.T) {
	t.Helper()
	oldPolicy := ensureISOPolicyForRuntime
	oldTopology := ensureISOTopologyForRuntime
	ensureISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error { return nil }
	ensureISOTopologyForRuntime = func(context.Context, netns.ISOTopologySpec) error { return nil }
	t.Cleanup(func() {
		ensureISOPolicyForRuntime = oldPolicy
		ensureISOTopologyForRuntime = oldTopology
	})
}

func TestISOReconcileStopsDriftBeforeQuarantineAndVerifiesGlobalPolicyLast(t *testing.T) {
	allocation := testISORuntimeAllocation("app", iso.StateReady)
	allocation.DesiredModes = []string{"iso", "ts"}
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"app": allocation})
	recorder := &isoReconcileRecorder{server: server, failAt: "inspect-runtime"}

	err := server.reconcileISONetworksWith(context.Background(), recorder)
	if err == nil || !strings.Contains(err.Error(), "inspect-runtime") {
		t.Fatalf("reconcileISONetworksWith error = %v, want runtime drift", err)
	}
	want := []string{
		"validate-pool", "install-dns", "ensure-policy:app", "verify-policy:app",
		"ensure-topology:app", "verify-topology:app", "inspect-runtime:app",
		"stop:app", "quarantine:app", "verify-global-policy",
	}
	if !slices.Equal(recorder.events, want) {
		t.Fatalf("reconcile events = %#v, want %#v", recorder.events, want)
	}
	dv, getErr := server.cfg.DB.Get()
	if getErr != nil {
		t.Fatal(getErr)
	}
	got := dv.Services().Get("app").ISO()
	if got.State() != string(iso.StateQuarantined) || !strings.Contains(got.LastError(), "inspect-runtime") {
		t.Fatalf("reconciled ISO state = %#v, want quarantined runtime drift", got.AsStruct())
	}
}

func TestISOReconcileStoppedTailscaleServiceVerifiesStoppedWithoutActiveTSCheck(t *testing.T) {
	allocation := testISORuntimeAllocation("app", iso.StateStopped)
	allocation.DesiredModes = []string{"iso", "ts"}
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"app": allocation})
	recorder := &isoReconcileRecorder{server: server}

	if err := server.reconcileISONetworksWith(context.Background(), recorder); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(recorder.events, "verify-tailscale:app") {
		t.Fatalf("stopped ISO service required active Tailscale: %#v", recorder.events)
	}
	if !slices.Contains(recorder.events, "verify-stopped:app") {
		t.Fatalf("stopped ISO service absence was not verified: %#v", recorder.events)
	}
}

func TestISOReconcileGlobalPolicyFailureStopsThenQuarantinesEveryService(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app": testISORuntimeAllocation("app", iso.StateReady),
	})
	recorder := &isoReconcileRecorder{server: server, failAt: "verify-global-policy"}

	err := server.reconcileISONetworksWith(context.Background(), recorder)
	if err == nil || !strings.Contains(err.Error(), "verify-global-policy") {
		t.Fatalf("reconcileISONetworksWith error = %v, want global policy failure", err)
	}
	wantTail := []string{"verify-global-policy", "stop:app", "quarantine:app"}
	if got := recorder.events[len(recorder.events)-len(wantTail):]; !slices.Equal(got, wantTail) {
		t.Fatalf("global-policy failure tail = %#v, want %#v", got, wantTail)
	}
}

func TestISOReconcileGlobalPrerequisiteFailureStopsRemovalRequestedService(t *testing.T) {
	for _, failAt := range []string{"validate-pool", "install-dns", "verify-global-policy"} {
		t.Run(failAt, func(t *testing.T) {
			allocation := testISORuntimeAllocation("app", iso.StateTombstoned)
			allocation.RemoveRequested = true
			server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"app": allocation})
			recorder := &isoReconcileRecorder{server: server, failAt: failAt}

			err := server.reconcileISONetworksWith(context.Background(), recorder)
			if err == nil || !strings.Contains(err.Error(), failAt) {
				t.Fatalf("reconcileISONetworksWith error = %v, want %s failure", err, failAt)
			}
			for _, want := range []string{"stop:app", "quarantine:app"} {
				if !slices.Contains(recorder.events, want) {
					t.Fatalf("reconcile events = %#v, missing fail-closed %q", recorder.events, want)
				}
			}
			got := mustService(t, server, "app").ISO
			if !got.RemoveRequested {
				t.Fatalf("removal intent was cleared after global failure: %#v", got)
			}
		})
	}
}

func TestISOConcreteReconcileQuarantineKeepsRemovalRequestedTombstoned(t *testing.T) {
	allocation := testISORuntimeAllocation("app", iso.StateTombstoned)
	allocation.RemoveRequested = true
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"app": allocation})
	steps := &isoConcreteReconcileSteps{server: server}
	if err := steps.Quarantine(context.Background(), "app", errors.New("startup prerequisite failed")); err != nil {
		t.Fatal(err)
	}
	got := mustService(t, server, "app").ISO
	if !got.RemoveRequested || got.State != string(iso.StateTombstoned) {
		t.Fatalf("removal tombstone after fail-closed quarantine = %#v", got)
	}
}

func TestISOReconcileRestartsOnlyPreviouslyReadyAfterFinalGlobalPolicy(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"ready":   testISORuntimeAllocation("ready", iso.StateReady),
		"stopped": testISORuntimeAllocation("stopped", iso.StateStopped),
	})
	recorder := &isoReconcileRecorder{server: server, runtimeAbsent: map[string]bool{"ready": true}}

	if err := server.reconcileISONetworksWith(context.Background(), recorder); err != nil {
		t.Fatal(err)
	}
	restart := slices.Index(recorder.events, "restart-trusted:ready")
	global := slices.Index(recorder.events, "verify-global-policy")
	if restart < 0 || global < 0 || restart <= global {
		t.Fatalf("reconcile events = %#v, want ready restart after final global policy", recorder.events)
	}
	if slices.Contains(recorder.events, "restart-trusted:stopped") {
		t.Fatalf("intentionally stopped allocation restarted: %#v", recorder.events)
	}
	if slices.Contains(recorder.events, "inspect-runtime:stopped") {
		t.Fatalf("intentionally stopped allocation received exact-running inspection: %#v", recorder.events)
	}
	if !slices.Contains(recorder.events, "verify-stopped:stopped") {
		t.Fatalf("stopped allocation absence was not verified: %#v", recorder.events)
	}
}

func TestISOReconcileLeavesVerifiedRunningReadyWorkloadRunning(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"ready": testISORuntimeAllocation("ready", iso.StateReady),
	})
	recorder := &isoReconcileRecorder{server: server}
	if err := server.reconcileISONetworksWith(context.Background(), recorder); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(recorder.events, "restart-trusted:ready") {
		t.Fatalf("verified running workload restarted unnecessarily: %#v", recorder.events)
	}
}

func TestISOReconcileSkipsComposeRuntimeInspectionForVM(t *testing.T) {
	allocation := testISORuntimeAllocation("devbox", iso.StateStopped)
	allocation.Kind = string(iso.PayloadVM)
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"devbox": allocation})
	recorder := &isoReconcileRecorder{server: server}

	if err := server.reconcileISONetworksWith(context.Background(), recorder); err != nil {
		t.Fatal(err)
	}
	for _, event := range recorder.events {
		if strings.Contains(event, "topology:devbox") || event == "inspect-runtime:devbox" {
			t.Fatalf("VM reconciliation used non-VM runtime phase %q: %#v", event, recorder.events)
		}
	}
}

func TestISOConcreteReconcileRepairsStoppedVMTAPBehindVerifiedPolicy(t *testing.T) {
	server, allocation := newISOReconcileVMTestServer(t, iso.StateStopped)
	withISORuntimeBackend(t, netns.BackendNFT)
	oldVerifyPolicy := verifyISOPolicyForRuntime
	oldStatus := serverVMStatusFunc
	oldRunner := vmNetworkReconcileRunner
	oldVerifyPlan := verifyVMNetworkPlanForReconcile
	oldIdentity := vmNetworkEnsureRuntimeIdentity
	var events []string
	verifyISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error {
		events = append(events, "verify-policy")
		return nil
	}
	serverVMStatusFunc = func(string) (svc.Status, error) { return svc.StatusStopped, nil }
	vmNetworkReconcileRunner = func(command []string) error {
		if len(command) >= 4 && command[0] == "ip" && command[1] == "tuntap" && command[2] == "add" && command[3] == allocation.Interface {
			events = append(events, "create-tap")
		}
		return nil
	}
	verifyVMNetworkPlanForReconcile = func(context.Context, vmNetworkPlan) error {
		events = append(events, "verify-tap")
		return nil
	}
	vmNetworkEnsureRuntimeIdentity = func() (vmRuntimeIdentity, error) { return vmRuntimeIdentity{UID: 812, GID: 813}, nil }
	t.Cleanup(func() {
		verifyISOPolicyForRuntime = oldVerifyPolicy
		serverVMStatusFunc = oldStatus
		vmNetworkReconcileRunner = oldRunner
		verifyVMNetworkPlanForReconcile = oldVerifyPlan
		vmNetworkEnsureRuntimeIdentity = oldIdentity
	})

	steps := &isoConcreteReconcileSteps{server: server}
	if err := steps.VerifyPolicy(context.Background(), "devbox"); err != nil {
		t.Fatal(err)
	}
	want := []string{"verify-policy", "create-tap", "verify-tap"}
	if !slices.Equal(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestISOConcreteReconcileStopsRunningVMWhenTAPDrifts(t *testing.T) {
	server, _ := newISOReconcileVMTestServer(t, iso.StateReady)
	withISORuntimeBackend(t, netns.BackendNFT)
	oldVerifyPolicy := verifyISOPolicyForRuntime
	oldStatus := serverVMStatusFunc
	oldVerifyPlan := verifyVMNetworkPlanForReconcile
	oldSystemctl := runISOReconcileVMSystemctl
	oldIdentity := vmNetworkEnsureRuntimeIdentity
	verifyISOPolicyForRuntime = func(context.Context, netns.ISOPolicyRules) error { return nil }
	serverVMStatusFunc = func(string) (svc.Status, error) { return svc.StatusRunning, nil }
	verifyVMNetworkPlanForReconcile = func(context.Context, vmNetworkPlan) error { return errors.New("tap attached to bridge") }
	vmNetworkEnsureRuntimeIdentity = func() (vmRuntimeIdentity, error) { return vmRuntimeIdentity{UID: 812, GID: 813}, nil }
	var stopped bool
	runISOReconcileVMSystemctl = func(_ context.Context, action, service string) error {
		stopped = action == "stop" && service == "devbox"
		return nil
	}
	t.Cleanup(func() {
		verifyISOPolicyForRuntime = oldVerifyPolicy
		serverVMStatusFunc = oldStatus
		verifyVMNetworkPlanForReconcile = oldVerifyPlan
		runISOReconcileVMSystemctl = oldSystemctl
		vmNetworkEnsureRuntimeIdentity = oldIdentity
	})

	steps := &isoConcreteReconcileSteps{server: server}
	err := steps.VerifyPolicy(context.Background(), "devbox")
	if err == nil || !strings.Contains(err.Error(), "tap attached to bridge") {
		t.Fatalf("VerifyPolicy error = %v", err)
	}
	if err := steps.StopUntrusted(context.Background(), "devbox"); err != nil {
		t.Fatal(err)
	}
	if !stopped {
		t.Fatal("drifted running VM was not stopped")
	}
}

func newISOReconcileVMTestServer(t *testing.T, state iso.AllocationState) (*Server, *db.ISOAllocation) {
	t.Helper()
	layout, err := iso.NewLayout(netip.MustParsePrefix("172.30.0.0/16"))
	if err != nil {
		t.Fatal(err)
	}
	link, err := layout.Link(0)
	if err != nil {
		t.Fatal(err)
	}
	allocation := newDBISOAllocation("devbox", isoReservationRequest{Kind: iso.PayloadVM, Modes: []string{"iso"}}, link)
	allocation.State = string(state)
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"devbox": allocation})
	_, _, err = server.cfg.DB.MutateService("devbox", func(_ *db.Data, service *db.Service) error {
		service.ServiceType = db.ServiceTypeVM
		service.VM = &db.VMConfig{Runtime: vmRuntimeFirecracker, Networks: newVMNetworkPlan("devbox", []string{"iso"}, vmNetworkInputs{
			ISOHostIP: allocation.HostIP, ISOGuestIP: allocation.PeerIP, ISOLink: allocation.Link, ISOTap: allocation.Interface,
		}).DBNetworks()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, allocation
}

func TestISOReconcileCancellationUsesLiveStopAndQuarantineContext(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app": testISORuntimeAllocation("app", iso.StateReady),
	})
	ctx, cancel := context.WithCancel(context.Background())
	recorder := &isoReconcileRecorder{server: server, cancelAt: "inspect-runtime", cancel: cancel}

	err := server.reconcileISONetworksWith(ctx, recorder)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("reconcileISONetworksWith error = %v, want cancellation", err)
	}
	if !recorder.stopContextLive || !recorder.quarantineContextLive {
		t.Fatalf("cleanup contexts live = stop %v quarantine %v, want both true", recorder.stopContextLive, recorder.quarantineContextLive)
	}
}

func TestISOReconcileQuarantineGetsFreshContextAfterStopDeadline(t *testing.T) {
	oldTimeout := isoSecurityCleanupTimeout
	isoSecurityCleanupTimeout = 5 * time.Millisecond
	t.Cleanup(func() { isoSecurityCleanupTimeout = oldTimeout })
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app": testISORuntimeAllocation("app", iso.StateReady),
	})
	recorder := &isoReconcileRecorder{server: server, failAt: "inspect-runtime", exhaustStopContext: true}

	if err := server.reconcileISONetworksWith(context.Background(), recorder); err == nil {
		t.Fatal("reconcileISONetworksWith unexpectedly succeeded")
	}
	if !recorder.quarantineContextLive {
		t.Fatal("quarantine inherited the expired stop cleanup context")
	}
}

func TestISOReconcileResumesRemovalTombstonesWithoutActiveBoundaryChecks(t *testing.T) {
	for _, cleanupVerified := range []bool{false, true} {
		t.Run(fmt.Sprintf("cleanup-verified-%v", cleanupVerified), func(t *testing.T) {
			allocation := testISORuntimeAllocation("app", iso.StateTombstoned)
			allocation.RemoveRequested = true
			allocation.CleanupVerified = cleanupVerified
			server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"app": allocation})
			recorder := &isoReconcileRecorder{server: server}

			if err := server.reconcileISONetworksWith(context.Background(), recorder); err != nil {
				t.Fatal(err)
			}
			want := []string{"validate-pool", "install-dns", "resume-removal:app", "verify-global-policy"}
			if !slices.Equal(recorder.events, want) {
				t.Fatalf("reconcile events = %#v, want removal-only branch %#v", recorder.events, want)
			}
		})
	}
}

type isoReconcileRecorder struct {
	server                *Server
	events                []string
	failAt                string
	cancelAt              string
	cancel                context.CancelFunc
	stopContextLive       bool
	quarantineContextLive bool
	exhaustStopContext    bool
	runtimeAbsent         map[string]bool
}

func (r *isoReconcileRecorder) step(name string) error {
	r.events = append(r.events, name)
	if strings.TrimSuffix(name, ":app") == r.cancelAt {
		r.cancel()
		return context.Canceled
	}
	if strings.TrimSuffix(name, ":app") == r.failAt {
		return errors.New(r.failAt + " failed")
	}
	return nil
}

func (r *isoReconcileRecorder) ValidatePool(context.Context) error {
	return r.step("validate-pool")
}

func (r *isoReconcileRecorder) InstallDNS(context.Context) error {
	return r.step("install-dns")
}

func (r *isoReconcileRecorder) EnsurePolicy(_ context.Context, service string) error {
	return r.step("ensure-policy:" + service)
}

func (r *isoReconcileRecorder) VerifyPolicy(_ context.Context, service string) error {
	return r.step("verify-policy:" + service)
}

func (r *isoReconcileRecorder) EnsureTopology(_ context.Context, service string) error {
	return r.step("ensure-topology:" + service)
}

func (r *isoReconcileRecorder) VerifyTopology(_ context.Context, service string) error {
	return r.step("verify-topology:" + service)
}

func (r *isoReconcileRecorder) InspectRuntime(_ context.Context, service string) (isoReconcileRuntimeState, error) {
	if err := r.step("inspect-runtime:" + service); err != nil {
		return "", err
	}
	if r.runtimeAbsent[service] {
		return isoReconcileRuntimeAbsent, nil
	}
	return isoReconcileRuntimeRunning, nil
}

func (r *isoReconcileRecorder) VerifyStopped(_ context.Context, service string) error {
	return r.step("verify-stopped:" + service)
}

func (r *isoReconcileRecorder) VerifyTailscale(_ context.Context, service string) error {
	return r.step("verify-tailscale:" + service)
}

func (r *isoReconcileRecorder) ResumeRemoval(_ context.Context, service string) error {
	return r.step("resume-removal:" + service)
}

func (r *isoReconcileRecorder) StopUntrusted(ctx context.Context, service string) error {
	r.stopContextLive = ctx.Err() == nil
	if r.exhaustStopContext {
		r.events = append(r.events, "stop:"+service)
		<-ctx.Done()
		return ctx.Err()
	}
	return r.step("stop:" + service)
}

func (r *isoReconcileRecorder) Quarantine(ctx context.Context, service string, cause error) error {
	r.quarantineContextLive = ctx.Err() == nil
	r.events = append(r.events, "quarantine:"+service)
	return r.server.markISOState(service, string(iso.StateQuarantined), cause)
}

func (r *isoReconcileRecorder) VerifyGlobalPolicy(context.Context) error {
	return r.step("verify-global-policy")
}

func (r *isoReconcileRecorder) RestartTrusted(_ context.Context, service string) error {
	return r.step("restart-trusted:" + service)
}

func TestISORetirementRetainsAddressWhileOldEndpointRemains(t *testing.T) {
	allocation := testISORuntimeAllocation("app", iso.StateReady)
	allocation.Components = map[string]db.ISOComponent{
		"api": {Address: netip.MustParseAddr("172.30.128.2")},
	}
	allocation.RetiredComponents = map[string]db.ISOComponent{
		"worker": {Address: netip.MustParseAddr("172.30.128.3"), State: "retiring"},
	}
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"app": allocation})
	recorder := &isoRetirementRecorder{server: server, failAt: "verify-docker-endpoints"}

	err := server.finalizeISORetirementsWith(context.Background(), "app", recorder)
	if err == nil || !strings.Contains(err.Error(), "endpoint still attached") {
		t.Fatalf("finalizeISORetirementsWith error = %v, want endpoint uncertainty", err)
	}
	want := []string{"stop-project", "verify-containers", "verify-docker-endpoints", "quarantine"}
	if !slices.Equal(recorder.events, want) {
		t.Fatalf("retirement events = %#v, want %#v", recorder.events, want)
	}
	dv, getErr := server.cfg.DB.Get()
	if getErr != nil {
		t.Fatal(getErr)
	}
	got := dv.Services().Get("app").ISO()
	if _, ok := got.RetiredComponents().GetOk("worker"); !ok {
		t.Fatalf("retired worker mapping was released while endpoint remained: %#v", got.AsStruct())
	}
	if got.State() != string(iso.StateQuarantined) {
		t.Fatalf("retirement state = %q, want quarantined", got.State())
	}
}

func TestISORetirementClearsOnlyAfterAllProofsThenReservesOnce(t *testing.T) {
	allocation := testISORuntimeAllocation("app", iso.StateReady)
	allocation.RetiredComponents = map[string]db.ISOComponent{
		"worker": {Address: netip.MustParseAddr("172.30.128.3"), State: "retiring"},
	}
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{"app": allocation})
	recorder := &isoRetirementRecorder{server: server}

	if err := server.finalizeISORetirementsWith(context.Background(), "app", recorder); err != nil {
		t.Fatal(err)
	}
	want := []string{"stop-project", "verify-containers", "verify-docker-endpoints", "verify-dnet-records", "reserve"}
	if !slices.Equal(recorder.events, want) {
		t.Fatalf("retirement events = %#v, want %#v", recorder.events, want)
	}
	if recorder.reserveCalls != 1 || !recorder.reserveSawCleared {
		t.Fatalf("reserve calls/cleared = %d/%v, want one call after DB clear", recorder.reserveCalls, recorder.reserveSawCleared)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got := dv.Services().Get("app").ISO().RetiredComponents().Len(); got != 0 {
		t.Fatalf("retired mapping count = %d, want zero after verified success", got)
	}
}

type isoRetirementRecorder struct {
	server            *Server
	events            []string
	failAt            string
	reserveCalls      int
	reserveSawCleared bool
}

func (r *isoRetirementRecorder) step(name string) error {
	r.events = append(r.events, name)
	if name == r.failAt {
		return errors.New("endpoint still attached")
	}
	return nil
}

func (r *isoRetirementRecorder) StopProject(context.Context, string) error {
	return r.step("stop-project")
}

func (r *isoRetirementRecorder) VerifyContainersAbsent(context.Context, string, map[string]db.ISOComponent) error {
	return r.step("verify-containers")
}

func (r *isoRetirementRecorder) VerifyDockerEndpointsAbsent(context.Context, string, map[string]db.ISOComponent) error {
	return r.step("verify-docker-endpoints")
}

func (r *isoRetirementRecorder) VerifyDNetRecordsAbsent(context.Context, string, map[string]db.ISOComponent) error {
	return r.step("verify-dnet-records")
}

func (r *isoRetirementRecorder) Reserve(context.Context, string) error {
	r.reserveCalls++
	dv, err := r.server.cfg.DB.Get()
	if err != nil {
		return err
	}
	r.reserveSawCleared = dv.Services().Get("app").ISO().RetiredComponents().Len() == 0
	return r.step("reserve")
}

func (r *isoRetirementRecorder) Quarantine(_ context.Context, service string, cause error) error {
	r.events = append(r.events, "quarantine")
	return r.server.markISOState(service, string(iso.StateQuarantined), cause)
}

func TestTransitionAwayFromISOCommitsOnlyAfterVerifiedCleanup(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app": testISORuntimeAllocation("app", iso.StateStopped),
	})
	if _, _, err := server.cfg.DB.MutateService("app", func(_ *db.Data, service *db.Service) error {
		service.Artifacts = db.ArtifactStore{
			db.ArtifactDockerComposeFile:    {Refs: map[db.ArtifactRef]string{"latest": "/srv/app/compose.yml"}},
			db.ArtifactDockerComposeNetwork: {Refs: map[db.ArtifactRef]string{"latest": "/srv/app/old-compose.network.yml"}},
			db.ArtifactNetNSResolv:          {Refs: map[db.ArtifactRef]string{"latest": "/srv/app/old-resolv.conf"}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	recorder := &isoTransitionRecorder{server: server}

	if err := server.transitionFromISOWith(context.Background(), "app", []string{"svc"}, recorder); err != nil {
		t.Fatal(err)
	}
	want := []string{"prepare", "stop-iso", "clean-iso", "verify-iso-absent", "start-replacement"}
	if !slices.Equal(recorder.events, want) {
		t.Fatalf("transition events = %#v, want %#v", recorder.events, want)
	}
	if !recorder.startSawCommitted {
		t.Fatal("replacement started before atomic network commit released ISO")
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	service := dv.Services().Get("app")
	if service.ISO().Valid() || !service.SvcNetwork().Valid() {
		t.Fatalf("committed replacement = %#v, want svc network and no ISO", service.AsStruct())
	}
	artifacts := service.AsStruct().Artifacts
	if got := artifacts[db.ArtifactDockerComposeFile].Refs["latest"]; got != "/srv/app/compose.yml" {
		t.Fatalf("payload compose artifact = %q, want preserved payload artifact", got)
	}
	if got := artifacts[db.ArtifactDockerComposeNetwork].Refs["latest"]; got != "/srv/app/new-compose.network.yml" {
		t.Fatalf("replacement network artifact = %q, want prepared replacement", got)
	}
	if _, ok := artifacts[db.ArtifactNetNSResolv]; ok {
		t.Fatalf("retired ISO network artifact survived transition: %#v", artifacts[db.ArtifactNetNSResolv])
	}
}

func TestTransitionAwayFromISORejectsInvalidPreparedBoundaryBeforeStop(t *testing.T) {
	tests := []struct {
		name     string
		desired  []string
		prepared isoReplacementNetwork
		want     string
	}{
		{
			name:    "requested modes are not normalized",
			desired: []string{"ts", "svc"},
			prepared: isoReplacementNetwork{
				Modes: []string{"svc", "ts"}, SvcNetwork: &db.SvcNetwork{}, Tailscale: &db.TailscaleNetwork{},
			},
			want: "requested replacement modes must be normalized",
		},
		{
			name:    "prepared modes are not normalized",
			desired: []string{"svc", "ts"},
			prepared: isoReplacementNetwork{
				Modes: []string{"ts", "svc"}, SvcNetwork: &db.SvcNetwork{}, Tailscale: &db.TailscaleNetwork{},
			},
			want: "prepared replacement modes must be normalized",
		},
		{
			name:     "prepared modes differ",
			desired:  []string{"svc"},
			prepared: isoReplacementNetwork{Modes: []string{"lan"}, Macvlan: &db.MacvlanNetwork{}},
			want:     "do not match requested modes",
		},
		{
			name:     "prepared modes contain iso",
			desired:  []string{"iso"},
			prepared: isoReplacementNetwork{Modes: []string{"iso"}},
			want:     "must not contain iso",
		},
		{
			name:     "svc network missing",
			desired:  []string{"svc"},
			prepared: isoReplacementNetwork{Modes: []string{"svc"}},
			want:     "svc mode and network state disagree",
		},
		{
			name:    "unexpected lan network",
			desired: []string{"svc"},
			prepared: isoReplacementNetwork{
				Modes: []string{"svc"}, SvcNetwork: &db.SvcNetwork{}, Macvlan: &db.MacvlanNetwork{},
			},
			want: "lan mode and network state disagree",
		},
		{
			name:    "payload artifact staged as network state",
			desired: []string{"svc"},
			prepared: isoReplacementNetwork{
				Modes: []string{"svc"}, SvcNetwork: &db.SvcNetwork{},
				Artifacts: db.ArtifactStore{db.ArtifactDockerComposeFile: {Refs: map[db.ArtifactRef]string{"staged": "/tmp/compose.yml"}}},
			},
			want: "is not network-owned",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
				"app": testISORuntimeAllocation("app", iso.StateStopped),
			})
			recorder := &isoTransitionRecorder{server: server, prepared: &tt.prepared}

			err := server.transitionFromISOWith(context.Background(), "app", tt.desired, recorder)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("transitionFromISOWith error = %v, want %q", err, tt.want)
			}
			if want := []string{"prepare"}; !slices.Equal(recorder.events, want) {
				t.Fatalf("events = %#v, want validation before stop %#v", recorder.events, want)
			}
			dv, getErr := server.cfg.DB.Get()
			if getErr != nil {
				t.Fatal(getErr)
			}
			if allocation := dv.Services().Get("app").ISO(); !allocation.Valid() || allocation.State() != string(iso.StateStopped) {
				t.Fatalf("ISO state changed after prepared boundary rejection: %#v", allocation.AsStruct())
			}
		})
	}
}

func TestTransitionAwayFromISOAllowsExplicitHostReplacement(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app": testISORuntimeAllocation("app", iso.StateReady),
	})
	recorder := &isoTransitionRecorder{server: server, prepared: &isoReplacementNetwork{Modes: []string{}}}
	if err := server.transitionFromISOWith(context.Background(), "app", []string{}, recorder); err != nil {
		t.Fatal(err)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	service := dv.Services().Get("app")
	if service.ISO().Valid() || service.SvcNetwork().Valid() || service.Macvlan().Valid() || service.TSNet().Valid() {
		t.Fatalf("host replacement retained managed network state: %#v", service.AsStruct())
	}
}

func TestTransitionAwayFromISORetainsTombstoneWhenAbsenceIsUncertain(t *testing.T) {
	server := newISORuntimeTestServer(t, map[string]*db.ISOAllocation{
		"app": testISORuntimeAllocation("app", iso.StateStopped),
	})
	recorder := &isoTransitionRecorder{server: server, failAt: "verify-iso-absent"}

	err := server.transitionFromISOWith(context.Background(), "app", []string{"svc"}, recorder)
	if err == nil || !strings.Contains(err.Error(), "endpoint remains") {
		t.Fatalf("transitionFromISOWith error = %v, want uncertain absence", err)
	}
	if slices.Contains(recorder.events, "start-replacement") {
		t.Fatalf("replacement started after uncertain cleanup: %#v", recorder.events)
	}
	dv, getErr := server.cfg.DB.Get()
	if getErr != nil {
		t.Fatal(getErr)
	}
	service := dv.Services().Get("app")
	if !service.ISO().Valid() || service.ISO().State() != string(iso.StateTombstoned) || service.SvcNetwork().Valid() {
		t.Fatalf("failed transition state = %#v, want retained ISO tombstone only", service.AsStruct())
	}
}

type isoTransitionRecorder struct {
	server            *Server
	events            []string
	failAt            string
	prepared          *isoReplacementNetwork
	startSawCommitted bool
}

func (r *isoTransitionRecorder) step(name string) error {
	r.events = append(r.events, name)
	if name == r.failAt {
		return errors.New("endpoint remains")
	}
	return nil
}

func (r *isoTransitionRecorder) PrepareReplacement(context.Context, string, []string) (isoReplacementNetwork, error) {
	prepared := isoReplacementNetwork{
		Modes:      []string{"svc"},
		SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.3")},
		Artifacts: db.ArtifactStore{
			db.ArtifactDockerComposeNetwork: {Refs: map[db.ArtifactRef]string{"latest": "/srv/app/new-compose.network.yml"}},
		},
	}
	if r.prepared != nil {
		prepared = *r.prepared
	}
	return prepared, r.step("prepare")
}

func (r *isoTransitionRecorder) StopISO(context.Context, string) error {
	return r.step("stop-iso")
}

func (r *isoTransitionRecorder) CleanISO(context.Context, string) error {
	return r.step("clean-iso")
}

func (r *isoTransitionRecorder) VerifyISOAbsent(context.Context, string) error {
	return r.step("verify-iso-absent")
}

func (r *isoTransitionRecorder) StartReplacement(_ context.Context, service string, _ isoReplacementNetwork) error {
	r.events = append(r.events, "start-replacement")
	dv, err := r.server.cfg.DB.Get()
	if err != nil {
		return err
	}
	current := dv.Services().Get(service)
	r.startSawCommitted = !current.ISO().Valid() && current.SvcNetwork().Valid()
	return nil
}
