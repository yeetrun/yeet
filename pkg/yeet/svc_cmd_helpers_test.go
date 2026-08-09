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

func TestSvcCommandFromArgs(t *testing.T) {
	tests := []struct {
		name            string
		args            []string
		wantCommand     string
		wantCommandArgs []string
		wantCheckArgs   []string
	}{
		{
			name:            "default status",
			args:            nil,
			wantCommand:     "status",
			wantCommandArgs: nil,
			wantCheckArgs:   []string{"status"},
		},
		{
			name:            "explicit command",
			args:            []string{"logs", "--tail", "10"},
			wantCommand:     "logs",
			wantCommandArgs: []string{"--tail", "10"},
			wantCheckArgs:   []string{"logs", "--tail", "10"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := svcCommandFromArgs(tt.args)
			if got.Name != tt.wantCommand {
				t.Fatalf("Name = %q, want %q", got.Name, tt.wantCommand)
			}
			if !reflect.DeepEqual(got.Args, tt.wantCommandArgs) {
				t.Fatalf("Args = %#v, want %#v", got.Args, tt.wantCommandArgs)
			}
			if !reflect.DeepEqual(got.CheckArgs, tt.wantCheckArgs) {
				t.Fatalf("CheckArgs = %#v, want %#v", got.CheckArgs, tt.wantCheckArgs)
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
