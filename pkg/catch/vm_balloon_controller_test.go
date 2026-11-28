// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/yeetrun/yeet/pkg/db"
	"github.com/yeetrun/yeet/pkg/svc"
)

func TestVMBalloonControllerPlansNoTargetsWhenMemoryHealthy(t *testing.T) {
	plan := planVMBalloonTargets(vmBalloonControllerInput{
		HostBytes:    16 << 30,
		MemAvailable: 8 << 30,
		Candidates: []vmBalloonReclaimCandidate{
			{Service: "a", MaxBytes: 4 << 30, MinBytes: 1 << 30, FreeBytes: 2 << 30},
		},
	})
	if len(plan) != 0 {
		t.Fatalf("targets = %#v, want none", plan)
	}
}

func TestVMBalloonControllerReclaimsUnderPressureWithoutPassingFloor(t *testing.T) {
	plan := planVMBalloonTargets(vmBalloonControllerInput{
		HostBytes:    16 << 30,
		MemAvailable: 1 << 30,
		Candidates: []vmBalloonReclaimCandidate{
			{Service: "a", MaxBytes: 4 << 30, MinBytes: 1 << 30, FreeBytes: 3 << 30},
		},
	})
	if len(plan) != 1 {
		t.Fatalf("targets = %#v, want one target", plan)
	}
	if plan["a"] != 3<<30 {
		t.Fatalf("target = %d, want max-floor", plan["a"])
	}
}

func TestVMBalloonControllerCapsReclaimAtGuestFreeBytes(t *testing.T) {
	plan := planVMBalloonTargets(vmBalloonControllerInput{
		HostBytes:    16 << 30,
		MemAvailable: 512 << 20,
		Candidates: []vmBalloonReclaimCandidate{
			{Service: "a", MaxBytes: 4 << 30, MinBytes: 1 << 30, FreeBytes: 256 << 20},
		},
	})
	if got := plan["a"]; got != 256<<20 {
		t.Fatalf("target = %d, want guest free cap %d", got, int64(256<<20))
	}
}

func TestVMBalloonControllerDoesNotReclaimWhenGuestReportsNoFreeMemory(t *testing.T) {
	plan := planVMBalloonTargets(vmBalloonControllerInput{
		HostBytes:    16 << 30,
		MemAvailable: 512 << 20,
		Candidates: []vmBalloonReclaimCandidate{
			{Service: "a", MaxBytes: 4 << 30, MinBytes: 1 << 30, FreeBytes: 0},
		},
	})
	if len(plan) != 0 {
		t.Fatalf("targets = %#v, want none when guest reports no free memory", plan)
	}
}

func TestVMBalloonControllerTargetAdjustments(t *testing.T) {
	const host = int64(16 << 30)
	returnThreshold := vmHostMemoryReserve(host) + host*15/100
	lowFree := vmBalloonReclaimCandidate{Service: "a", MaxBytes: 4 << 30, MinBytes: 1 << 30, CurrentTarget: 1 << 30, ActualBytes: 1 << 30}
	highFree := vmBalloonReclaimCandidate{Service: "b", MaxBytes: 4 << 30, MinBytes: 1 << 30, CurrentTarget: 1 << 30, ActualBytes: 1 << 30, FreeBytes: 512 << 20}
	stalled := vmBalloonReclaimCandidate{Service: "a", CurrentTarget: 3 << 30, ActualBytes: (1 << 30) + 4096, Stalled: true, BackingOff: true}
	for _, tt := range []struct {
		name       string
		available  int64
		candidates []vmBalloonReclaimCandidate
		want       map[string]int64
	}{
		{
			name:       "hold targets between thresholds",
			available:  4 << 30,
			candidates: []vmBalloonReclaimCandidate{lowFree, highFree},
		},
		{
			name:       "cancel stalled inflation during host pressure and reclaim elsewhere",
			available:  1 << 30,
			candidates: []vmBalloonReclaimCandidate{stalled, highFree},
			want:       map[string]int64{"a": 1 << 30, "b": (1 << 30) + (512 << 20)},
		},
		{
			name:       "cancel stalled inflation between thresholds",
			available:  4 << 30,
			candidates: []vmBalloonReclaimCandidate{stalled, highFree},
			want:       map[string]int64{"a": 1 << 30},
		},
		{
			name:       "limit aggregate return and prioritize guests with less free memory",
			available:  8 << 30,
			candidates: []vmBalloonReclaimCandidate{highFree, lowFree},
			want:       map[string]int64{"a": (1 << 30) - vmBalloonReturnStep},
		},
		{
			name:       "return only available host headroom",
			available:  returnThreshold + (32 << 20),
			candidates: []vmBalloonReclaimCandidate{lowFree},
			want:       map[string]int64{"a": (1 << 30) - (32 << 20)},
		},
		{
			name:      "subtract memory already being returned",
			available: 8 << 30,
			candidates: []vmBalloonReclaimCandidate{
				{Service: "a", CurrentTarget: 0, ActualBytes: 96 << 20},
				highFree,
			},
			want: map[string]int64{"b": (1 << 30) - (32 << 20)},
		},
		{
			name:      "cancel pending inflation once the host recovers",
			available: returnThreshold,
			candidates: []vmBalloonReclaimCandidate{
				{Service: "a", CurrentTarget: 1 << 30, ActualBytes: 256 << 20},
			},
			want: map[string]int64{"a": 256 << 20},
		},
		{
			name:      "account for pending inflation before requesting more",
			available: 1 << 30,
			candidates: []vmBalloonReclaimCandidate{
				{Service: "a", CurrentTarget: 4 << 30},
				highFree,
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := planVMBalloonTargets(vmBalloonControllerInput{
				HostBytes: host, MemAvailable: tt.available, Candidates: tt.candidates,
			})
			if !maps.Equal(got, tt.want) {
				t.Fatalf("targets = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVMBalloonControllerReconcilesStallAndRecovery(t *testing.T) {
	server := newTestServer(t)
	socketPath := "/run/firecracker/devbox.sock"
	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		d.Services = map[string]*db.Service{
			"devbox": {
				Name:        "devbox",
				ServiceType: db.ServiceTypeVM,
				VM: &db.VMConfig{
					MemoryBytes: 4 << 30,
					Balloon: db.VMBalloonConfig{
						Mode:                 vmBalloonModeAuto,
						MinBytes:             1 << 30,
						StatsIntervalSeconds: vmBalloonDefaultStatsIntervalSeconds,
					},
					Sockets: db.VMSocketConfig{APISocketPath: socketPath},
				},
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	oldVMStatus := serverVMStatusFunc
	serverVMStatusFunc = func(name string) (svc.Status, error) {
		if name == "devbox" {
			return svc.StatusRunning, nil
		}
		return svc.StatusStopped, nil
	}
	t.Cleanup(func() { serverVMStatusFunc = oldVMStatus })
	api := &recordingVMBalloonAPI{
		stats: map[string]vmBalloonStats{
			socketPath: {FreeMemoryBytes: 3 << 30},
		},
	}
	oldMemTotal := linuxMemTotalBytesFunc
	oldMemAvailable := linuxMemAvailableBytesFunc
	hostAvailable := int64(1 << 30)
	linuxMemTotalBytesFunc = func() int64 { return 16 << 30 }
	linuxMemAvailableBytesFunc = func() int64 { return hostAvailable }
	t.Cleanup(func() {
		linuxMemTotalBytesFunc = oldMemTotal
		linuxMemAvailableBytesFunc = oldMemAvailable
	})

	observations := make(map[string]vmBalloonObservation)
	start := time.Unix(1000, 0)
	settled := int64(256 << 20)
	for _, step := range []struct {
		name        string
		elapsed     time.Duration
		actual      int64
		recovered   bool
		want        int64
		wantRequest bool
	}{
		{name: "inflate under pressure", want: 3 << 30, wantRequest: true},
		{name: "observe inflation", elapsed: 5 * time.Second, want: 3 << 30},
		{name: "progress resets the timer", elapsed: 30 * time.Second, actual: settled, want: 3 << 30},
		{name: "hold past the original stall deadline", elapsed: 40 * time.Second, actual: settled, want: 3 << 30},
		{name: "partial inflation stalls", elapsed: 60 * time.Second, actual: settled, want: settled, wantRequest: true},
		{name: "back off with host still under pressure", elapsed: 65 * time.Second, actual: settled, want: settled},
		{name: "return memory when host recovers", elapsed: 70 * time.Second, actual: settled, recovered: true, want: settled - vmBalloonReturnStep, wantRequest: true},
		{name: "finish returning memory", elapsed: 75 * time.Second, actual: settled - vmBalloonReturnStep, recovered: true, want: 0, wantRequest: true},
		{name: "allow inflation after cooldown", elapsed: 120 * time.Second, want: 3 << 30, wantRequest: true},
	} {
		t.Run(step.name, func(t *testing.T) {
			hostAvailable = 1 << 30
			if step.recovered {
				hostAvailable = 8 << 30
			}
			stats := api.stats[socketPath]
			stats.ActualBytes = step.actual
			api.stats[socketPath] = stats
			clear(api.targets)
			if err := server.reconcileVMBalloons(context.Background(), api, start.Add(step.elapsed), observations); err != nil {
				t.Fatalf("reconcileVMBalloons: %v", err)
			}
			got, requested := api.targets[socketPath]
			if requested != step.wantRequest || (requested && got != step.want) {
				t.Fatalf("requested target = %d (%t), want %d (%t)", got, requested, step.want, step.wantRequest)
			}
			dv, err := server.getDB()
			if err != nil {
				t.Fatalf("getDB: %v", err)
			}
			if got := dv.Services().Get("devbox").VM().Balloon().LastTargetBytes; got != step.want {
				t.Fatalf("LastTargetBytes = %d, want %d", got, step.want)
			}
		})
	}
}

type recordingVMBalloonAPI struct {
	stats   map[string]vmBalloonStats
	targets map[string]int64
}

func (a *recordingVMBalloonAPI) SetTarget(_ context.Context, socket string, targetBytes int64) error {
	if a.targets == nil {
		a.targets = map[string]int64{}
	}
	a.targets[socket] = targetBytes
	stats := a.stats[socket]
	stats.TargetBytes = targetBytes
	a.stats[socket] = stats
	return nil
}

func (a *recordingVMBalloonAPI) Stats(_ context.Context, socket string) (vmBalloonStats, error) {
	return a.stats[socket], nil
}
