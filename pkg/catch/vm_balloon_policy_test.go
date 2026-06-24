// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestDefaultVMBalloonConfigUsesAutoAndFloor(t *testing.T) {
	got := defaultVMBalloonConfig(4 << 30)
	if got.Mode != vmBalloonModeAuto || got.MinBytes != 1<<30 || got.StatsIntervalSeconds != vmBalloonDefaultStatsIntervalSeconds {
		t.Fatalf("defaultVMBalloonConfig = %#v, want auto 1GiB floor", got)
	}
}

func TestDefaultVMBalloonConfigCapsTinyVMFloor(t *testing.T) {
	got := defaultVMBalloonConfig(256 << 20)
	if got.MinBytes != 256<<20 {
		t.Fatalf("MinBytes = %d, want tiny VM max", got.MinBytes)
	}
}

func TestEffectiveVMBalloonConfigOffReservesMax(t *testing.T) {
	got, err := effectiveVMBalloonConfig(2<<30, db.VMBalloonConfig{Mode: "off", MinBytes: 512 << 20})
	if err != nil {
		t.Fatalf("effectiveVMBalloonConfig: %v", err)
	}
	if got.Mode != vmBalloonModeOff || got.MinBytes != 2<<30 || got.LastTargetBytes != 0 {
		t.Fatalf("off config = %#v, want floor=max and no target", got)
	}
}

func TestEffectiveVMBalloonConfigRejectsFloorAboveMax(t *testing.T) {
	_, err := effectiveVMBalloonConfig(1<<30, db.VMBalloonConfig{Mode: "auto", MinBytes: 2 << 30})
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("error = %v, want floor above max rejection", err)
	}
}

func TestNormalizeVMHostMemoryPolicyRatios(t *testing.T) {
	cases := map[string][2]int64{
		"":           {1, 1},
		"safe":       {1, 1},
		"balanced":   {3, 2},
		"aggressive": {2, 1},
	}
	for raw, want := range cases {
		got, err := normalizeVMHostMemoryPolicy(raw)
		if err != nil {
			t.Fatalf("normalizeVMHostMemoryPolicy(%q): %v", raw, err)
		}
		if got.RatioNumerator != want[0] || got.RatioDenominator != want[1] {
			t.Fatalf("policy %q ratio = %d/%d, want %d/%d", raw, got.RatioNumerator, got.RatioDenominator, want[0], want[1])
		}
	}
}

func TestAdmitVMMemoryWithPolicyBalancedAllowsMaxOvercommitWhenFloorsFit(t *testing.T) {
	policy, err := normalizeVMHostMemoryPolicy("balanced")
	if err != nil {
		t.Fatal(err)
	}
	err = admitVMMemoryWithPolicy(vmMemoryAdmissionInput{
		Policy:          policy,
		HostBytes:       16 << 30,
		RunningMaxBytes: 12 << 30,
		RunningMinBytes: 3 << 30,
		RequestMaxBytes: 4 << 30,
		RequestMinBytes: 1 << 30,
	})
	if err != nil {
		t.Fatalf("admit balanced overcommit: %v", err)
	}
}

func TestAdmitVMMemoryWithPolicyDefaultsZeroPolicyToSafe(t *testing.T) {
	err := admitVMMemoryWithPolicy(vmMemoryAdmissionInput{
		HostBytes:       16 << 30,
		RunningMaxBytes: 4 << 30,
		RunningMinBytes: 1 << 30,
		RequestMaxBytes: 4 << 30,
		RequestMinBytes: 1 << 30,
	})
	if err != nil {
		t.Fatalf("admit zero-value policy: %v", err)
	}
}

func TestAdmitVMMemoryWithPolicyRejectsInvalidPolicyName(t *testing.T) {
	err := admitVMMemoryWithPolicy(vmMemoryAdmissionInput{
		Policy: vmMemoryPolicy{
			Name:             "turbo",
			RatioNumerator:   100,
			RatioDenominator: 1,
		},
		HostBytes:       16 << 30,
		RunningMaxBytes: 4 << 30,
		RunningMinBytes: 1 << 30,
		RequestMaxBytes: 4 << 30,
		RequestMinBytes: 1 << 30,
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported VM memory policy") {
		t.Fatalf("error = %v, want invalid policy rejection", err)
	}
}

func TestAdmitVMMemoryWithPolicyRejectsFloorsPastBudget(t *testing.T) {
	policy, err := normalizeVMHostMemoryPolicy("aggressive")
	if err != nil {
		t.Fatal(err)
	}
	err = admitVMMemoryWithPolicy(vmMemoryAdmissionInput{
		Policy:          policy,
		HostBytes:       8 << 30,
		RunningMaxBytes: 4 << 30,
		RunningMinBytes: 4 << 30,
		RequestMaxBytes: 4 << 30,
		RequestMinBytes: 3 << 30,
	})
	if err == nil || !strings.Contains(err.Error(), "minimum commit") {
		t.Fatalf("error = %v, want minimum commit rejection", err)
	}
}

func TestVMBalloonTargetForPressureCapsAtAvailableHeadroom(t *testing.T) {
	cases := []struct {
		name                string
		maxBytes            int64
		minBytes            int64
		desiredReclaimBytes int64
		want                int64
	}{
		{name: "no pressure", maxBytes: 4 << 30, minBytes: 1 << 30, desiredReclaimBytes: 0, want: 0},
		{name: "within headroom", maxBytes: 4 << 30, minBytes: 1 << 30, desiredReclaimBytes: 2 << 30, want: 2 << 30},
		{name: "caps at headroom", maxBytes: 4 << 30, minBytes: 1 << 30, desiredReclaimBytes: 8 << 30, want: 3 << 30},
		{name: "no headroom", maxBytes: 1 << 30, minBytes: 1 << 30, desiredReclaimBytes: 512 << 20, want: 0},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := vmBalloonTargetForPressure(tt.maxBytes, tt.minBytes, tt.desiredReclaimBytes)
			if got != tt.want {
				t.Fatalf("target = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSortVMBalloonReclaimCandidates(t *testing.T) {
	candidates := []vmBalloonReclaimCandidate{
		{Service: "small", MaxBytes: 1 << 30, MinBytes: 512 << 20, FreeBytes: 768 << 20},
		{Service: "large-low-free", MaxBytes: 8 << 30, MinBytes: 2 << 30, FreeBytes: 1 << 30},
		{Service: "large-free", MaxBytes: 8 << 30, MinBytes: 2 << 30, FreeBytes: 4 << 30},
	}
	sortVMBalloonReclaimCandidates(candidates)
	if candidates[0].Service != "large-free" {
		t.Fatalf("first candidate = %q, want large-free", candidates[0].Service)
	}
}
