// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"reflect"
	"testing"
)

func TestSplitRunPayloadArgsPreservesFlagScanningBehavior(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantPayload string
		wantArgs    []string
	}{
		{
			name:        "long flag value before payload",
			args:        []string{"--net", "svc,ts", "app:latest", "--", "-app-flag"},
			wantPayload: "app:latest",
			wantArgs:    []string{"--net", "svc,ts", "--", "-app-flag"},
		},
		{
			name:        "short publish value before payload",
			args:        []string{"-p", "8080:80", "compose.yml"},
			wantPayload: "compose.yml",
			wantArgs:    []string{"-p", "8080:80"},
		},
		{
			name:        "unknown dashed payload",
			args:        []string{"-app-binary", "--arg"},
			wantPayload: "-app-binary",
			wantArgs:    []string{"--arg"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPayload, gotArgs, err := splitRunPayloadArgs(tt.args)
			if err != nil {
				t.Fatalf("splitRunPayloadArgs error: %v", err)
			}
			if gotPayload != tt.wantPayload || !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Fatalf("splitRunPayloadArgs = (%q, %#v), want (%q, %#v)", gotPayload, gotArgs, tt.wantPayload, tt.wantArgs)
			}
		})
	}
}

func TestBuildStatusRowsHandlesAggregateAndEmptyServices(t *testing.T) {
	results := []hostStatusData{
		{
			Host: "host-b",
			Services: []statusService{
				{ServiceName: "svc-empty", ServiceType: "service"},
				{ServiceName: "svc-docker", ServiceType: dockerServiceType, Components: []statusComponent{
					{Name: "web", Status: "running"},
					{Name: "worker", Status: "stopped"},
				}},
			},
		},
	}

	got := buildStatusRows(results, true)
	want := []statusRow{
		{Host: "host-b", Service: "svc-docker", Type: dockerServiceType, Containers: "web,worker", Status: "partial (1/2)"},
		{Host: "host-b", Service: "svc-empty", Type: "service", Containers: "-", Status: "unknown"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %#v, want %#v", got, want)
	}
}

func TestStoredServiceConfigSelectsSingleHostAndValidatesType(t *testing.T) {
	oldService := serviceOverride
	defer func() {
		serviceOverride = oldService
		resetHostOverride()
	}()

	serviceOverride = "svc-a"
	cfg := &ProjectConfig{Version: projectConfigVersion}
	cfg.SetServiceEntry(ServiceEntry{
		Name:    "svc-a",
		Host:    "host-a",
		Type:    serviceTypeCron,
		Payload: "run.sh",
	})
	loc := &projectConfigLocation{Dir: "/tmp/project", Config: cfg}

	got, err := storedServiceConfig(loc, "", "cron", serviceTypeCron)
	if err != nil {
		t.Fatalf("storedServiceConfig error: %v", err)
	}
	if got.Service != "svc-a" || got.Host != "host-a" || got.Entry.Payload != "run.sh" {
		t.Fatalf("stored config = %#v", got)
	}
	if Host() != "host-a" {
		t.Fatalf("Host() = %q, want host-a", Host())
	}

	if _, err := storedServiceConfig(loc, "host-a", "run", serviceTypeRun); err == nil {
		t.Fatalf("expected type validation error")
	}
}
