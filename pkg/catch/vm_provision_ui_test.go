// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestVMProvisionSuccessBlockLines(t *testing.T) {
	plan := vmProvisionPlan{
		Service: "devbox",
		Shape: vmShape{
			CPUs:        2,
			MemoryBytes: 2 << 30,
			DiskBytes:   64 << 30,
		},
		Image: vmImageAsset{
			Manifest: vmImageManifest{
				Name:    "Ubuntu 26.04",
				Version: "ubuntu-26.04-amd64-v15",
			},
		},
		Network: vmNetworkPlan{
			Interfaces: []vmNetworkInterfacePlan{
				{Mode: "svc"},
				{Mode: "lan"},
			},
		},
	}

	got := vmProvisionSuccessBlockLines(vmProvisionSuccess{
		ServiceLabel: "devbox@yeet-lab",
		Plan:         plan,
		Payload:      testUbuntuVMPayload,
		Elapsed:      12400 * time.Millisecond,
		Started:      true,
	})
	want := []string{
		"✔ VM ready in 12.4s",
		"",
		"devbox@yeet-lab",
		"Image    Ubuntu 26.04",
		"Shape    2 vCPU, 2.0 GB memory, 64.0 GB disk",
		"Network  svc,lan",
		"",
		"SSH      yeet ssh devbox",
		"Console  yeet vm console devbox",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("success block = %#v, want %#v", got, want)
	}
}

func TestVMProvisionReadinessFailureBlockLines(t *testing.T) {
	got := vmProvisionReadinessFailureBlockLines("devbox")
	want := []string{
		"",
		"VM service was created, but readiness did not complete.",
		"",
		"Console  yeet vm console devbox",
		"Logs     yeet logs devbox",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("failure block = %#v, want %#v", got, want)
	}
}

func TestVMProvisionSuccessBlockLinesIncludesReadyIP(t *testing.T) {
	plan := vmProvisionPlan{
		Service: "devbox",
		Shape:   vmShape{CPUs: 1, MemoryBytes: 1 << 30, DiskBytes: 8 << 30},
		Network: vmNetworkPlan{Interfaces: []vmNetworkInterfacePlan{{Mode: "svc"}}},
	}

	got := vmProvisionSuccessBlockLines(vmProvisionSuccess{
		ServiceLabel: "devbox@yeet-lab",
		Plan:         plan,
		Payload:      testUbuntuVMPayload,
		Elapsed:      time.Second,
		Ready:        vmGuestReadyReport{IP: netip.MustParseAddr("10.0.4.80")},
		Started:      true,
	})

	if !reflect.DeepEqual(got, []string{
		"✔ VM ready in 1.0s",
		"",
		"devbox@yeet-lab",
		"Image    vm://ubuntu/26.04",
		"Shape    1 vCPU, 1.0 GB memory, 8.0 GB disk",
		"Network  svc",
		"IP       10.0.4.80",
		"",
		"SSH      yeet ssh devbox",
		"Console  yeet vm console devbox",
	}) {
		t.Fatalf("success block with IP = %#v", got)
	}
}

func TestVMProvisionCreatedBlockLines(t *testing.T) {
	plan := vmProvisionPlan{
		Service: "devbox",
		Shape:   vmShape{CPUs: 1, MemoryBytes: 1 << 30, DiskBytes: 8 << 30},
		Network: vmNetworkPlan{Interfaces: []vmNetworkInterfacePlan{{Mode: "svc"}}},
	}

	got := vmProvisionSuccessBlockLines(vmProvisionSuccess{
		ServiceLabel: "devbox@yeet-lab",
		Plan:         plan,
		Payload:      testUbuntuVMPayload,
		Elapsed:      time.Second,
		Started:      false,
	})

	want := []string{
		"✔ VM created in 1.0s",
		"",
		"devbox@yeet-lab",
		"Image    vm://ubuntu/26.04",
		"Shape    1 vCPU, 1.0 GB memory, 8.0 GB disk",
		"Network  svc",
		"",
		"Start    yeet start devbox",
		"Console  yeet vm console devbox",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("created block = %#v, want %#v", got, want)
	}
}

func TestVMProvisionPlanDetail(t *testing.T) {
	plan := vmProvisionPlan{
		Image: vmImageAsset{Manifest: vmImageManifest{Name: "Ubuntu 26.04", Version: "ubuntu-26.04-amd64-v15"}},
	}
	if got := vmProvisionPlanDetail(plan); got != "Ubuntu 26.04" {
		t.Fatalf("vmProvisionPlanDetail = %q", got)
	}
}

func TestVMProvisionImageProgressRestoresInterruptedParentStep(t *testing.T) {
	var out bytes.Buffer
	ui := &vmProvisionUI{ui: newRunUI(&out, false, false, "run", "devbox@yeet-lab")}
	progress := ui.ImageProgress()

	ui.StartStep(vmRunStepResolve)
	progress.StartStep("Download VM image")
	progress.DoneStep("10.0 MB @ 20.0 MB/s")
	ui.DoneStep("Ubuntu 26.04")

	text := out.String()
	for _, want := range []string{
		`status=running step="Resolve VM plan"`,
		`status=running step="Download VM image"`,
		`status=ok step="Download VM image" detail="10.0 MB @ 20.0 MB/s"`,
		`status=ok step="Resolve VM plan" detail="Ubuntu 26.04"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("image progress output missing %q:\n%s", want, text)
		}
	}
}
