// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
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
