// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/iso"
)

type fakeISOPoolProbe struct {
	host       []netip.Prefix
	namespaces []netip.Prefix
	docker     []netip.Prefix
	hostErr    error
	netnsErr   error
	dockerErr  error
}

func (p *fakeISOPoolProbe) HostPrefixes(context.Context) ([]netip.Prefix, error) {
	return append([]netip.Prefix(nil), p.host...), p.hostErr
}

func (p *fakeISOPoolProbe) NamespacePrefixes(context.Context) ([]netip.Prefix, error) {
	return append([]netip.Prefix(nil), p.namespaces...), p.netnsErr
}

func (p *fakeISOPoolProbe) DockerPrefixes(context.Context) ([]netip.Prefix, error) {
	return append([]netip.Prefix(nil), p.docker...), p.dockerErr
}

func TestSelectISOPoolPrefers17230(t *testing.T) {
	probe := &fakeISOPoolProbe{}
	got, err := selectISOPool(context.Background(), probe, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != netip.MustParsePrefix("172.30.0.0/16") {
		t.Fatalf("pool = %v", got)
	}
}

func TestSelectISOPoolFallsBackAroundCollisions(t *testing.T) {
	probe := &fakeISOPoolProbe{host: []netip.Prefix{
		netip.MustParsePrefix("172.30.0.0/16"),
		netip.MustParsePrefix("172.29.0.0/16"),
	}}
	got, err := selectISOPool(context.Background(), probe, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != netip.MustParsePrefix("172.28.0.0/16") {
		t.Fatalf("pool = %v, want 172.28.0.0/16", got)
	}
}

func TestSelectISOPoolUsesCuratedCandidateOrder(t *testing.T) {
	want := []string{
		"172.30.0.0/16", "172.29.0.0/16", "172.28.0.0/16", "172.27.0.0/16",
		"172.26.0.0/16", "172.25.0.0/16", "172.24.0.0/16", "172.23.0.0/16",
		"172.22.0.0/16", "172.21.0.0/16", "172.20.0.0/16", "172.19.0.0/16",
		"172.18.0.0/16", "172.16.0.0/16", "172.17.0.0/16", "172.31.0.0/16",
	}
	if len(automaticISOPoolCandidates) != len(want) {
		t.Fatalf("candidate count = %d, want %d", len(automaticISOPoolCandidates), len(want))
	}
	for i, raw := range want {
		if automaticISOPoolCandidates[i] != raw {
			t.Fatalf("candidate %d = %q, want %q", i, automaticISOPoolCandidates[i], raw)
		}
	}
}

func TestSelectISOPoolFailsClosedOnProbeErrors(t *testing.T) {
	wantErr := errors.New("probe failed")
	tests := []struct {
		name  string
		probe *fakeISOPoolProbe
	}{
		{name: "host", probe: &fakeISOPoolProbe{hostErr: wantErr}},
		{name: "namespace", probe: &fakeISOPoolProbe{netnsErr: wantErr}},
		{name: "docker", probe: &fakeISOPoolProbe{dockerErr: wantErr}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := selectISOPool(context.Background(), tt.probe, nil); !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, want %v", err, wantErr)
			}
		})
	}
}

func TestSelectISOPoolReportsExhaustion(t *testing.T) {
	occupied := make([]netip.Prefix, 0, len(automaticISOPoolCandidates))
	for _, raw := range automaticISOPoolCandidates {
		occupied = append(occupied, netip.MustParsePrefix(raw))
	}
	_, err := selectISOPool(context.Background(), &fakeISOPoolProbe{docker: occupied}, nil)
	if err == nil || !strings.Contains(err.Error(), "no collision-free ISO /16") {
		t.Fatalf("error = %v, want exhaustion", err)
	}
}

func TestPlanISOPoolReturnsStructuredBlocker(t *testing.T) {
	server := newISOPoolTestServer(t, &fakeISOPoolProbe{})
	seedISOAllocation(t, server, "stuck", iso.StateTombstoned)
	plan, err := server.PlanISOPool(context.Background(), catchrpc.ISOPoolPlanRequest{Prefix: "172.28.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Blockers, []string{"stuck"}) || len(plan.Conflicts) != 0 {
		t.Fatalf("plan = %#v, want structured blocker", plan)
	}
}

func TestPlanISOPoolRejectsInvalidExplicitPrefix(t *testing.T) {
	server := newISOPoolTestServer(t, &fakeISOPoolProbe{})
	for _, invalid := range []string{"10.42.1.0/16", "8.8.0.0/16", "172.30.0.0/24"} {
		if _, err := server.PlanISOPool(context.Background(), catchrpc.ISOPoolPlanRequest{Prefix: invalid}); err == nil {
			t.Errorf("PlanISOPool(%q) returned nil error", invalid)
		}
	}
}

func TestPlanISOPoolReturnsStructuredPersistedConflict(t *testing.T) {
	server := newISOPoolTestServer(t, &fakeISOPoolProbe{})
	_, _, err := server.cfg.DB.MutateService("persisted", func(_ *db.Data, service *db.Service) error {
		service.SvcNetwork = &db.SvcNetwork{IPv4: netip.MustParseAddr("10.42.1.7")}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := server.PlanISOPool(context.Background(), catchrpc.ISOPoolPlanRequest{Prefix: "10.42.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Conflicts) != 1 || !strings.Contains(plan.Conflicts[0], "10.42.1.7/32") {
		t.Fatalf("plan = %#v, want structured persisted conflict", plan)
	}
}

func TestPlanISOPoolReturnsStructuredLiveConflict(t *testing.T) {
	server := newISOPoolTestServer(t, &fakeISOPoolProbe{host: []netip.Prefix{netip.MustParsePrefix("10.42.1.0/24")}})
	plan, err := server.PlanISOPool(context.Background(), catchrpc.ISOPoolPlanRequest{Prefix: "10.42.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Conflicts) != 1 || !strings.Contains(plan.Conflicts[0], "10.42.1.0/24") {
		t.Fatalf("plan = %#v, want structured live conflict", plan)
	}
}

func TestCommandISOPoolProbeParsesHostNamespaceAndDockerPrefixes(t *testing.T) {
	outputs := map[string]string{
		"ip -j address": `[{
			"ifname":"eth0","addr_info":[{"family":"inet","local":"10.0.4.12","prefixlen":24},{"family":"inet6","local":"::1","prefixlen":128}]
		}]`,
		"ip -j route show table all":                    `[{"dst":"10.42.0.0/16"},{"dst":"default"},{"dst":"192.0.2.5"},{"dst":"fd7a:115c:a1e0::53"},{"dst":"fd7a:115c:a1e0::/48"}]`,
		"ip netns list":                                 "blue (id: 1)\n",
		"ip netns exec blue ip -j address":              `[{"addr_info":[{"family":"inet","local":"172.20.4.2","prefixlen":24}]}]`,
		"ip netns exec blue ip -j route show table all": `[{"dst":"172.21.0.0/16"}]`,
		"docker network ls --quiet":                     "bridge\ncustom\n",
		"docker network inspect bridge custom":          `[{"IPAM":{"Config":[{"Subnet":"172.17.0.0/16","Gateway":"172.17.0.1","IPRange":"172.17.8.0/21"}]}},{"IPAM":{"Config":[{"Subnet":"10.88.0.0/16"}]}}]`,
	}
	probe := commandISOPoolProbe{run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		out, ok := outputs[key]
		if !ok {
			return nil, fmt.Errorf("unexpected command %s", key)
		}
		return []byte(out), nil
	}}

	host, err := probe.HostPrefixes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertPrefixes(t, host, "10.0.4.0/24", "10.42.0.0/16", "192.0.2.5/32")
	netns, err := probe.NamespacePrefixes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertPrefixes(t, netns, "172.20.4.0/24", "172.21.0.0/16")
	docker, err := probe.DockerPrefixes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertPrefixes(t, docker, "172.17.0.0/16", "172.17.0.1/32", "172.17.8.0/21", "10.88.0.0/16")
}

func TestCommandISOPoolProbeFailsClosed(t *testing.T) {
	wantErr := errors.New("command failed")
	baseOutputs := map[string]string{
		"ip -j address":                                 `[]`,
		"ip -j route show table all":                    `[]`,
		"ip netns list":                                 "blue\n",
		"ip netns exec blue ip -j address":              `[]`,
		"ip netns exec blue ip -j route show table all": `[]`,
		"docker network ls --quiet":                     "bridge\n",
		"docker network inspect bridge":                 `[{"IPAM":{"Config":[]}}]`,
	}
	tests := []struct {
		name     string
		fail     string
		override map[string]string
		load     func(commandISOPoolProbe) ([]netip.Prefix, error)
	}{
		{
			name: "host address command",
			fail: "ip -j address",
			load: func(p commandISOPoolProbe) ([]netip.Prefix, error) { return p.HostPrefixes(context.Background()) },
		},
		{
			name: "host route command",
			fail: "ip -j route show table all",
			load: func(p commandISOPoolProbe) ([]netip.Prefix, error) { return p.HostPrefixes(context.Background()) },
		},
		{
			name: "namespace list command",
			fail: "ip netns list",
			load: func(p commandISOPoolProbe) ([]netip.Prefix, error) { return p.NamespacePrefixes(context.Background()) },
		},
		{
			name: "namespace address command",
			fail: "ip netns exec blue ip -j address",
			load: func(p commandISOPoolProbe) ([]netip.Prefix, error) { return p.NamespacePrefixes(context.Background()) },
		},
		{
			name: "namespace route command",
			fail: "ip netns exec blue ip -j route show table all",
			load: func(p commandISOPoolProbe) ([]netip.Prefix, error) { return p.NamespacePrefixes(context.Background()) },
		},
		{
			name: "Docker list command",
			fail: "docker network ls --quiet",
			load: func(p commandISOPoolProbe) ([]netip.Prefix, error) { return p.DockerPrefixes(context.Background()) },
		},
		{
			name: "Docker inspect command",
			fail: "docker network inspect bridge",
			load: func(p commandISOPoolProbe) ([]netip.Prefix, error) { return p.DockerPrefixes(context.Background()) },
		},
		{
			name:     "host address JSON",
			override: map[string]string{"ip -j address": `{`},
			load:     func(p commandISOPoolProbe) ([]netip.Prefix, error) { return p.HostPrefixes(context.Background()) },
		},
		{
			name:     "host address prefix",
			override: map[string]string{"ip -j address": `[{"addr_info":[{"family":"inet","local":"bad","prefixlen":24}]}]`},
			load:     func(p commandISOPoolProbe) ([]netip.Prefix, error) { return p.HostPrefixes(context.Background()) },
		},
		{
			name:     "host route JSON",
			override: map[string]string{"ip -j route show table all": `{`},
			load:     func(p commandISOPoolProbe) ([]netip.Prefix, error) { return p.HostPrefixes(context.Background()) },
		},
		{
			name:     "host route prefix",
			override: map[string]string{"ip -j route show table all": `[{"dst":"bad"}]`},
			load:     func(p commandISOPoolProbe) ([]netip.Prefix, error) { return p.HostPrefixes(context.Background()) },
		},
		{
			name:     "namespace address JSON",
			override: map[string]string{"ip netns exec blue ip -j address": `{`},
			load:     func(p commandISOPoolProbe) ([]netip.Prefix, error) { return p.NamespacePrefixes(context.Background()) },
		},
		{
			name:     "namespace address prefix",
			override: map[string]string{"ip netns exec blue ip -j address": `[{"addr_info":[{"family":"inet","local":"bad","prefixlen":24}]}]`},
			load:     func(p commandISOPoolProbe) ([]netip.Prefix, error) { return p.NamespacePrefixes(context.Background()) },
		},
		{
			name:     "namespace route JSON",
			override: map[string]string{"ip netns exec blue ip -j route show table all": `{`},
			load:     func(p commandISOPoolProbe) ([]netip.Prefix, error) { return p.NamespacePrefixes(context.Background()) },
		},
		{
			name:     "namespace route prefix",
			override: map[string]string{"ip netns exec blue ip -j route show table all": `[{"dst":"bad"}]`},
			load:     func(p commandISOPoolProbe) ([]netip.Prefix, error) { return p.NamespacePrefixes(context.Background()) },
		},
		{
			name:     "Docker inspect JSON",
			override: map[string]string{"docker network inspect bridge": `{`},
			load:     func(p commandISOPoolProbe) ([]netip.Prefix, error) { return p.DockerPrefixes(context.Background()) },
		},
		{
			name:     "Docker subnet prefix",
			override: map[string]string{"docker network inspect bridge": `[{"IPAM":{"Config":[{"Subnet":"bad"}]}}]`},
			load:     func(p commandISOPoolProbe) ([]netip.Prefix, error) { return p.DockerPrefixes(context.Background()) },
		},
		{
			name:     "Docker IP range prefix",
			override: map[string]string{"docker network inspect bridge": `[{"IPAM":{"Config":[{"IPRange":"bad"}]}}]`},
			load:     func(p commandISOPoolProbe) ([]netip.Prefix, error) { return p.DockerPrefixes(context.Background()) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := func(_ context.Context, name string, args ...string) ([]byte, error) {
				key := strings.Join(append([]string{name}, args...), " ")
				if key == tt.fail {
					return nil, wantErr
				}
				if out, ok := tt.override[key]; ok {
					return []byte(out), nil
				}
				out, ok := baseOutputs[key]
				if !ok {
					return nil, fmt.Errorf("unexpected command %s", key)
				}
				return []byte(out), nil
			}
			_, err := tt.load(commandISOPoolProbe{run: run})
			if err == nil {
				t.Fatal("probe returned nil error")
			}
			if tt.fail != "" && !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, want %v", err, wantErr)
			}
		})
	}
}

func TestPersistedNetworkPrefixesIncludesEveryRelevantState(t *testing.T) {
	server := newTestServer(t)
	data := &db.Data{
		DataVersion: db.CurrentDataVersion,
		ISOPool:     &db.ISOPool{Prefix: netip.MustParsePrefix("172.30.0.0/16")},
		Services: map[string]*db.Service{
			"networks": {
				SvcNetwork: &db.SvcNetwork{IPv4: netip.MustParseAddr("192.168.100.7")},
				VM:         &db.VMConfig{Networks: []db.VMNetworkConfig{{IP: netip.MustParseAddr("10.0.4.19")}}},
			},
			"tombstone": {ISO: &db.ISOAllocation{
				State:   string(iso.StateTombstoned),
				Link:    netip.MustParsePrefix("172.30.0.4/30"),
				Project: netip.MustParsePrefix("172.30.128.32/27"),
			}},
		},
		DockerNetworks: map[string]*db.DockerNetwork{
			"svc": {
				IPv4Gateway: netip.MustParsePrefix("10.64.0.1/24"),
				IPv4Range:   netip.MustParsePrefix("10.64.0.0/24"),
				Endpoints: map[string]*db.DockerEndpoint{
					"api": {IPv4: netip.MustParsePrefix("10.64.0.5/32")},
				},
			},
		},
	}
	if err := server.cfg.DB.Set(data); err != nil {
		t.Fatal(err)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	assertPrefixes(t, persistedNetworkPrefixes(dv),
		"172.30.0.0/16", "192.168.100.7/32", "10.0.4.19/32",
		"172.30.0.4/30", "172.30.128.32/27", "10.64.0.0/24", "10.64.0.5/32",
	)
}

func TestReserveISOAllocationSelectsAndPersistsPoolOnFirstUse(t *testing.T) {
	server := newTestServer(t)
	setISOPoolProbeForTest(t, &fakeISOPoolProbe{})
	allocation, err := server.reserveISOAllocation(context.Background(), "app", isoReservationRequest{Kind: iso.PayloadVM, Modes: []string{"iso"}})
	if err != nil {
		t.Fatal(err)
	}
	if allocation.Link != netip.MustParsePrefix("172.30.0.0/30") {
		t.Fatalf("link = %s, want first preferred-pool link", allocation.Link)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	pool := dv.ISOPool()
	if !pool.Valid() || pool.Prefix() != netip.MustParsePrefix("172.30.0.0/16") || pool.Source() != "automatic" {
		t.Fatalf("pool = %#v, want durable automatic preferred pool", pool.AsStruct())
	}
}

func TestEnsureISOPoolReprobesImmediatelyBeforePersistence(t *testing.T) {
	server := newTestServer(t)
	probe := &secondHostProbeCollision{}
	setISOPoolProbeForTest(t, probe)
	err := server.ensureISOPool(context.Background())
	if err == nil || !strings.Contains(err.Error(), "172.30.0.0/16") {
		t.Fatalf("ensure error = %v, want second-probe collision", err)
	}
	dv, getErr := server.cfg.DB.Get()
	if getErr != nil {
		t.Fatal(getErr)
	}
	if dv.ISOPool().Valid() {
		t.Fatalf("pool persisted after collision: %#v", dv.ISOPool().AsStruct())
	}
}

func TestEnsureISOPoolIsAtomicUnderConcurrency(t *testing.T) {
	server := newTestServer(t)
	setISOPoolProbeForTest(t, &fakeISOPoolProbe{})
	const count = 16
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- server.ensureISOPool(context.Background())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got := dv.ISOPool().Prefix(); got != netip.MustParsePrefix("172.30.0.0/16") {
		t.Fatalf("pool = %s, want preferred pool", got)
	}
}

func TestEnsureISOPoolKeepsConcurrentlySelectedPool(t *testing.T) {
	server := newTestServer(t)
	probe := &concurrentSelectingISOPoolProbe{server: server}
	setISOPoolProbeForTest(t, probe)
	if err := server.ensureISOPool(context.Background()); err != nil {
		t.Fatal(err)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatal(err)
	}
	pool := dv.ISOPool()
	if got := pool.Prefix(); got != netip.MustParsePrefix("10.44.0.0/16") || pool.Source() != "explicit" {
		t.Fatalf("pool = %#v, want concurrent explicit selection", pool.AsStruct())
	}
}

func TestApplyISOPoolReplansAgainstNewCollision(t *testing.T) {
	server := newISOPoolTestServer(t, &fakeISOPoolProbe{})
	plan, err := server.PlanISOPool(context.Background(), catchrpc.ISOPoolPlanRequest{Prefix: "172.28.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	setISOPoolProbeForTest(t, &fakeISOPoolProbe{host: []netip.Prefix{netip.MustParsePrefix("172.28.10.0/24")}})
	if _, err := server.ApplyISOPool(context.Background(), catchrpc.ISOPoolApplyRequest{Plan: plan}); err == nil || !strings.Contains(err.Error(), "172.28") {
		t.Fatalf("apply error = %v, want stale-plan collision", err)
	}
}

func TestApplyISOPoolRechecksAllocationsAtomically(t *testing.T) {
	server := newISOPoolTestServer(t, &fakeISOPoolProbe{})
	plan, err := server.PlanISOPool(context.Background(), catchrpc.ISOPoolPlanRequest{Prefix: "172.28.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	seedISOAllocation(t, server, "new-blocker", iso.StateReserved)
	if _, err := server.ApplyISOPool(context.Background(), catchrpc.ISOPoolApplyRequest{Plan: plan}); err == nil || !strings.Contains(err.Error(), "new-blocker") {
		t.Fatalf("apply error = %v, want allocation blocker", err)
	}
}

func TestApplyISOPoolPersistsExplicitPool(t *testing.T) {
	server := newISOPoolTestServer(t, &fakeISOPoolProbe{})
	plan, err := server.PlanISOPool(context.Background(), catchrpc.ISOPoolPlanRequest{Prefix: "10.42.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := server.ApplyISOPool(context.Background(), catchrpc.ISOPoolApplyRequest{Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if result.Prefix != "10.42.0.0/16" || result.Source != "explicit" {
		t.Fatalf("result = %#v", result)
	}
}

func TestInfoCatchISOPoolSummaryCountsEveryAllocationState(t *testing.T) {
	server := newTestServer(t)
	data := newISOAllocatorData("172.30.0.0/16")
	data.ISOPool.Source = "automatic"
	data.ISOPool.LastConflict = "aggregate route missing"
	states := []iso.AllocationState{iso.StateReserved, iso.StateReady, iso.StateQuarantined, iso.StateTombstoned}
	for i, state := range states {
		layout := mustISOLayout(t, "172.30.0.0/16")
		link, err := layout.Link(i)
		if err != nil {
			t.Fatal(err)
		}
		allocation := &db.ISOAllocation{State: string(state), Link: link}
		if i < 2 {
			allocation.Project, err = layout.Project(i)
			if err != nil {
				t.Fatal(err)
			}
		}
		name := fmt.Sprintf("app-%d", i)
		data.Services[name] = &db.Service{Name: name, ISO: allocation}
	}
	setISOAllocatorData(t, server, data)

	summary := GetInfoWithConfig(&server.cfg).ISO
	want := catchrpc.ISOPoolSummary{
		Prefix:       "172.30.0.0/16",
		Source:       "automatic",
		Allocator:    iso.AllocatorVersion,
		Policy:       iso.PolicyVersion,
		LinksUsed:    4,
		ProjectsUsed: 2,
		Reserved:     1,
		Active:       1,
		Quarantined:  1,
		Tombstoned:   1,
		Conflict:     "aggregate route missing",
	}
	if summary != want {
		t.Fatalf("summary = %#v, want %#v", summary, want)
	}
}

func TestInfoCatchZeroISOSummaryPreservesJSONCompatibility(t *testing.T) {
	raw, err := json.Marshal(GetInfo())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"iso"`) {
		t.Fatalf("GetInfo JSON = %s, want omitted zero ISO summary", raw)
	}
}

type secondHostProbeCollision struct {
	mu    sync.Mutex
	calls int
}

type concurrentSelectingISOPoolProbe struct {
	server      *Server
	dockerCalls int
}

func (*concurrentSelectingISOPoolProbe) HostPrefixes(context.Context) ([]netip.Prefix, error) {
	return nil, nil
}

func (*concurrentSelectingISOPoolProbe) NamespacePrefixes(context.Context) ([]netip.Prefix, error) {
	return nil, nil
}

func (p *concurrentSelectingISOPoolProbe) DockerPrefixes(context.Context) ([]netip.Prefix, error) {
	p.dockerCalls++
	if p.dockerCalls == 2 {
		_, err := p.server.cfg.DB.MutateData(func(data *db.Data) error {
			data.ISOPool = &db.ISOPool{
				Prefix:           netip.MustParsePrefix("10.44.0.0/16"),
				Source:           "explicit",
				AllocatorVersion: iso.AllocatorVersion,
				PolicyVersion:    iso.PolicyVersion,
			}
			return nil
		})
		return nil, err
	}
	return nil, nil
}

func (p *secondHostProbeCollision) HostPrefixes(context.Context) ([]netip.Prefix, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls == 2 {
		return []netip.Prefix{netip.MustParsePrefix("172.30.99.0/24")}, nil
	}
	return nil, nil
}

func (*secondHostProbeCollision) NamespacePrefixes(context.Context) ([]netip.Prefix, error) {
	return nil, nil
}

func (*secondHostProbeCollision) DockerPrefixes(context.Context) ([]netip.Prefix, error) {
	return nil, nil
}

func assertPrefixes(t *testing.T, got []netip.Prefix, want ...string) {
	t.Helper()
	gotSet := make(map[netip.Prefix]bool, len(got))
	for _, prefix := range got {
		gotSet[prefix.Masked()] = true
	}
	for _, raw := range want {
		prefix := netip.MustParsePrefix(raw).Masked()
		if !gotSet[prefix] {
			t.Errorf("prefixes = %v, missing %s", got, prefix)
		}
	}
	if len(gotSet) != len(want) {
		t.Errorf("prefixes = %v, want exactly %v", got, want)
	}
}

func newISOPoolTestServer(t *testing.T, probe isoPoolProbe) *Server {
	t.Helper()
	server := newTestServer(t)
	setISOPoolProbeForTest(t, probe)
	setISOAllocatorData(t, server, newISOAllocatorData("172.30.0.0/16"))
	return server
}

func setISOPoolProbeForTest(t *testing.T, probe isoPoolProbe) {
	t.Helper()
	old := isoPoolProbeForServer
	isoPoolProbeForServer = func(*Server) isoPoolProbe { return probe }
	t.Cleanup(func() { isoPoolProbeForServer = old })
}

func seedISOAllocation(t *testing.T, server *Server, name string, state iso.AllocationState) {
	t.Helper()
	_, _, err := server.cfg.DB.MutateService(name, func(_ *db.Data, service *db.Service) error {
		service.ISO = &db.ISOAllocation{
			State:   string(state),
			Link:    netip.MustParsePrefix("172.30.0.0/30"),
			Project: netip.MustParsePrefix("172.30.128.0/27"),
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
