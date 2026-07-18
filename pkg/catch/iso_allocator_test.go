// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
)

func TestReserveISOAllocationStableAcrossRetries(t *testing.T) {
	server := newISOAllocatorTestServer(t, "172.30.0.0/16")
	req := isoReservationRequest{Kind: iso.PayloadCompose, Modes: []string{"iso"}, Components: []string{"api", "worker"}}
	first, err := server.reserveISOAllocation(context.Background(), "app", req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := server.reserveISOAllocation(context.Background(), "app", req)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(first, second, cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{})); diff != "" {
		t.Fatalf("allocation changed (-first +second):\n%s", diff)
	}
	if got, want := first.Link.String(), "172.30.0.0/30"; got != want {
		t.Fatalf("link = %s, want %s", got, want)
	}
	if got, want := first.Project.String(), "172.30.128.0/27"; got != want {
		t.Fatalf("project = %s, want %s", got, want)
	}
	if got, want := first.Components["api"].Address.String(), "172.30.128.2"; got != want {
		t.Fatalf("api address = %s, want %s", got, want)
	}
	if got, want := first.Components["worker"].Address.String(), "172.30.128.3"; got != want {
		t.Fatalf("worker address = %s, want %s", got, want)
	}
	if first.Interface != "yi-a172cedcae" || first.PeerInterface != "yo-a172cedcae" || first.NetNS != "yeet-a172cedcae-ns" {
		t.Fatalf("stable names = %q, %q, %q", first.Interface, first.PeerInterface, first.NetNS)
	}
	if first.AllocatorVersion != iso.AllocatorVersion || first.PolicyVersion != iso.PolicyVersion {
		t.Fatalf("versions = allocator %d policy %d", first.AllocatorVersion, first.PolicyVersion)
	}
}

func TestReserveISOAllocationPreservesRetiredComponentsAcrossRetries(t *testing.T) {
	server := newISOAllocatorTestServer(t, "172.30.0.0/16")
	initial, err := server.reserveISOAllocation(context.Background(), "app", isoReservationRequest{
		Kind:       iso.PayloadCompose,
		Modes:      []string{"iso"},
		Components: []string{"api", "worker"},
	})
	if err != nil {
		t.Fatal(err)
	}
	retiredAddress := initial.Components["worker"].Address
	apiAddress := initial.Components["api"].Address

	reduced := isoReservationRequest{Kind: iso.PayloadCompose, Modes: []string{"iso"}, Components: []string{"api"}}
	if _, err := server.reserveISOAllocation(context.Background(), "app", reduced); err != nil {
		t.Fatal(err)
	}
	if _, err := server.reserveISOAllocation(context.Background(), "app", reduced); err != nil {
		t.Fatal(err)
	}
	expanded, err := server.reserveISOAllocation(context.Background(), "app", isoReservationRequest{
		Kind:       iso.PayloadCompose,
		Modes:      []string{"iso"},
		Components: []string{"api", "newcomer"},
	})
	if err != nil {
		t.Fatal(err)
	}

	retired, ok := expanded.RetiredComponents["worker"]
	if !ok || retired.Address != retiredAddress {
		t.Errorf("retired worker = %#v, present=%v; want address %s", retired, ok, retiredAddress)
	}
	if got := expanded.Components["newcomer"].Address; got == retiredAddress {
		t.Errorf("newcomer reused still-retired address %s", got)
	}
	if got := expanded.Components["api"].Address; got != apiAddress {
		t.Errorf("api address changed from %s to %s", apiAddress, got)
	}
}

func TestReserveISOAllocationDoesNotReuseTombstone(t *testing.T) {
	server := newISOAllocatorTestServer(t, "172.30.0.0/16")
	first, err := server.reserveISOAllocation(context.Background(), "old", isoReservationRequest{Kind: iso.PayloadVM, Modes: []string{"iso"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.markISOState("old", string(iso.StateTombstoned), errors.New("link still present")); err != nil {
		t.Fatal(err)
	}
	second, err := server.reserveISOAllocation(context.Background(), "new", isoReservationRequest{Kind: iso.PayloadVM, Modes: []string{"iso"}})
	if err != nil {
		t.Fatal(err)
	}
	if second.Link == first.Link {
		t.Fatalf("reused tombstoned link %v", first.Link)
	}
	if _, err := server.reserveISOAllocation(context.Background(), "old", isoReservationRequest{Kind: iso.PayloadVM, Modes: []string{"iso"}}); err == nil || !strings.Contains(err.Error(), "cleanup tombstone") {
		t.Fatalf("reserving tombstoned service error = %v", err)
	}
}

func TestReserveISOAllocationDoesNotReuseLiveResources(t *testing.T) {
	server := newISOAllocatorTestServer(t, "172.30.0.0/16")
	first, err := server.reserveISOAllocation(context.Background(), "first", isoReservationRequest{Kind: iso.PayloadCompose, Modes: []string{"iso"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := server.reserveISOAllocation(context.Background(), "second", isoReservationRequest{Kind: iso.PayloadCompose, Modes: []string{"iso"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Link == second.Link || first.Project == second.Project {
		t.Fatalf("allocations overlap: first=%#v second=%#v", first, second)
	}
}

func TestReserveISOAllocationIsAtomicUnderConcurrency(t *testing.T) {
	server := newISOAllocatorTestServer(t, "172.30.0.0/16")
	const count = 16
	allocations := make([]*db.ISOAllocation, count)
	errs := make([]error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allocations[i], errs[i] = server.reserveISOAllocation(context.Background(), fmt.Sprintf("app-%02d", i), isoReservationRequest{Kind: iso.PayloadCompose, Modes: []string{"iso"}})
		}()
	}
	wg.Wait()

	links := map[netip.Prefix]bool{}
	projects := map[netip.Prefix]bool{}
	for i, allocation := range allocations {
		if errs[i] != nil {
			t.Fatalf("reservation %d: %v", i, errs[i])
		}
		if links[allocation.Link] || projects[allocation.Project] {
			t.Fatalf("reservation %d reused link or project: %#v", i, allocation)
		}
		links[allocation.Link] = true
		projects[allocation.Project] = true
	}
}

func TestReserveISOAllocationReportsLinkExhaustion(t *testing.T) {
	server := newISOAllocatorTestServer(t, "172.30.0.0/16")
	layout := mustISOLayout(t, "172.30.0.0/16")
	data := newISOAllocatorData("172.30.0.0/16")
	for i := range iso.MaxLinks {
		link, err := layout.Link(i)
		if err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("used-%04d", i)
		data.Services[name] = &db.Service{Name: name, ISO: &db.ISOAllocation{Link: link, State: string(iso.StateTombstoned)}}
	}
	setISOAllocatorData(t, server, data)

	_, err := server.reserveISOAllocation(context.Background(), "new", isoReservationRequest{Kind: iso.PayloadVM, Modes: []string{"iso"}})
	if err == nil || !strings.Contains(err.Error(), iso.ErrLinkCapacity.Error()) {
		t.Fatalf("reserve error = %v, want %v", err, iso.ErrLinkCapacity)
	}
	assertISOServiceAbsent(t, server, "new")
}

func TestReserveISOAllocationReportsProjectExhaustionAtomically(t *testing.T) {
	server := newISOAllocatorTestServer(t, "172.30.0.0/16")
	layout := mustISOLayout(t, "172.30.0.0/16")
	data := newISOAllocatorData("172.30.0.0/16")
	for i := range iso.MaxProjects {
		project, err := layout.Project(i)
		if err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("used-%04d", i)
		data.Services[name] = &db.Service{Name: name, ISO: &db.ISOAllocation{Project: project, State: string(iso.StateTombstoned)}}
	}
	setISOAllocatorData(t, server, data)

	_, err := server.reserveISOAllocation(context.Background(), "new", isoReservationRequest{Kind: iso.PayloadCompose, Modes: []string{"iso"}})
	if err == nil || !strings.Contains(err.Error(), iso.ErrProjectCapacity.Error()) {
		t.Fatalf("reserve error = %v, want %v", err, iso.ErrProjectCapacity)
	}
	assertISOServiceAbsent(t, server, "new")
}

func TestISOAllocationLifecycleMutationsPersist(t *testing.T) {
	server := newISOAllocatorTestServer(t, "172.30.0.0/16")
	if _, err := server.reserveISOAllocation(context.Background(), "app", isoReservationRequest{Kind: iso.PayloadVM, Modes: []string{"iso"}}); err != nil {
		t.Fatal(err)
	}
	if err := server.markISOState("app", string(iso.StateDegraded), errors.New("setup failed")); err != nil {
		t.Fatal(err)
	}
	allocation := readISOAllocation(t, server, "app")
	if allocation.State != string(iso.StateDegraded) || allocation.LastError != "setup failed" {
		t.Fatalf("marked allocation = %#v", allocation)
	}
	if err := server.markISOState("app", string(iso.StateReady), nil); err != nil {
		t.Fatal(err)
	}
	allocation = readISOAllocation(t, server, "app")
	if allocation.State != string(iso.StateReady) || allocation.LastError != "" {
		t.Fatalf("ready allocation = %#v", allocation)
	}
	if err := server.releaseISOAllocation("app"); err != nil {
		t.Fatal(err)
	}
	if got := readISOService(t, server, "app").ISO; got != nil {
		t.Fatalf("released allocation = %#v, want nil", got)
	}
}

func TestISOAllocationMutationErrors(t *testing.T) {
	server := newTestServer(t)
	probeErr := errors.New("host probe failed")
	setISOPoolProbeForTest(t, &fakeISOPoolProbe{hostErr: probeErr})
	if _, err := server.reserveISOAllocation(context.Background(), "app", isoReservationRequest{Kind: iso.PayloadVM, Modes: []string{"iso"}}); !errors.Is(err, probeErr) {
		t.Fatalf("probe error = %v, want %v", err, probeErr)
	}
	if err := server.markISOState("app", string(iso.StateReady), nil); err == nil || !strings.Contains(err.Error(), "has no ISO allocation") {
		t.Fatalf("missing allocation error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := server.reserveISOAllocation(canceled, "app", isoReservationRequest{Kind: iso.PayloadVM, Modes: []string{"iso"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reservation error = %v", err)
	}
}

func newISOAllocatorTestServer(t *testing.T, prefix string) *Server {
	t.Helper()
	server := newTestServer(t)
	setISOAllocatorData(t, server, newISOAllocatorData(prefix))
	return server
}

func newISOAllocatorData(prefix string) *db.Data {
	return &db.Data{
		DataVersion: db.CurrentDataVersion,
		ISOPool: &db.ISOPool{
			Prefix:           netip.MustParsePrefix(prefix),
			Source:           "test",
			AllocatorVersion: iso.AllocatorVersion,
			PolicyVersion:    iso.PolicyVersion,
		},
		Services: map[string]*db.Service{},
	}
}

func setISOAllocatorData(t *testing.T, server *Server, data *db.Data) {
	t.Helper()
	if err := server.cfg.DB.Set(data); err != nil {
		t.Fatal(err)
	}
}

func mustISOLayout(t *testing.T, prefix string) iso.Layout {
	t.Helper()
	layout, err := iso.NewLayout(netip.MustParsePrefix(prefix))
	if err != nil {
		t.Fatal(err)
	}
	return layout
}

func readISOAllocation(t *testing.T, server *Server, name string) *db.ISOAllocation {
	t.Helper()
	allocation := readISOService(t, server, name).ISO
	if allocation == nil {
		t.Fatalf("service %q has no ISO allocation", name)
	}
	return allocation
}

func readISOService(t *testing.T, server *Server, name string) *db.Service {
	t.Helper()
	view, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	service := view.AsStruct().Services[name]
	if service == nil {
		t.Fatalf("service %q is absent", name)
	}
	return service
}

func assertISOServiceAbsent(t *testing.T, server *Server, name string) {
	t.Helper()
	view, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if service := view.AsStruct().Services[name]; service != nil {
		t.Fatalf("service %q persisted after failed reservation: %#v", name, service)
	}
}
