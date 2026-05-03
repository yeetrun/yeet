// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestResolveEditCommandDefaults(t *testing.T) {
	spec := resolveEditCommand("", "", "/tmp/service")
	if spec.name != "vim" {
		t.Fatalf("editor = %q, want vim", spec.name)
	}
	if !reflect.DeepEqual(spec.args, []string{"/tmp/service"}) {
		t.Fatalf("args = %#v, want path arg", spec.args)
	}
	if spec.term != "xterm" {
		t.Fatalf("term = %q, want xterm", spec.term)
	}
}

func TestResolveEditCommandUsesProvidedEditorAndTerm(t *testing.T) {
	spec := resolveEditCommand("nano", "screen-256color", "/tmp/service")
	if spec.name != "nano" {
		t.Fatalf("editor = %q, want nano", spec.name)
	}
	if !reflect.DeepEqual(spec.args, []string{"/tmp/service"}) {
		t.Fatalf("args = %#v, want path arg", spec.args)
	}
	if spec.term != "screen-256color" {
		t.Fatalf("term = %q, want screen-256color", spec.term)
	}
}

func TestWriteSystemdEditSourceIncludesLatestUnitAndTimerInOrder(t *testing.T) {
	dir := t.TempDir()
	unitPath := filepath.Join(dir, "svc.service")
	timerPath := filepath.Join(dir, "svc.timer")
	if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/bin/true\n"), 0644); err != nil {
		t.Fatalf("WriteFile unit returned error: %v", err)
	}
	if err := os.WriteFile(timerPath, []byte("[Timer]\nOnCalendar=hourly\n"), 0644); err != nil {
		t.Fatalf("WriteFile timer returned error: %v", err)
	}

	var buf bytes.Buffer
	names, err := writeSystemdEditSource(&buf, db.ArtifactStore{
		db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{"latest": timerPath}},
		db.ArtifactSystemdUnit:      {Refs: map[db.ArtifactRef]string{"latest": unitPath}},
	})
	if err != nil {
		t.Fatalf("writeSystemdEditSource returned error: %v", err)
	}

	wantNames := []db.ArtifactName{db.ArtifactSystemdUnit, db.ArtifactSystemdTimerFile}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("names = %#v, want %#v", names, wantNames)
	}

	got := buf.String()
	unitHeader := fmt.Sprintf(editUnitsSeparator, db.ArtifactSystemdUnit)
	timerHeader := fmt.Sprintf(editUnitsSeparator, db.ArtifactSystemdTimerFile)
	if !strings.Contains(got, unitHeader+"\n\n[Service]\nExecStart=/bin/true\n") {
		t.Fatalf("unit content missing from edit source:\n%s", got)
	}
	if !strings.Contains(got, timerHeader+"\n\n[Timer]\nOnCalendar=hourly\n") {
		t.Fatalf("timer content missing from edit source:\n%s", got)
	}
	if strings.Index(got, unitHeader) > strings.Index(got, timerHeader) {
		t.Fatalf("systemd unit should appear before timer:\n%s", got)
	}
}

func TestParseEditedSystemdUnitsTrimsContents(t *testing.T) {
	raw := fmt.Sprintf("%s\n\n  [Service]\nExecStart=/bin/true\n\n%s\n\n[Timer]\nOnCalendar=hourly\n\n",
		fmt.Sprintf(editUnitsSeparator, db.ArtifactSystemdUnit),
		fmt.Sprintf(editUnitsSeparator, db.ArtifactSystemdTimerFile),
	)

	got, err := parseEditedSystemdUnits([]byte(raw), 2)
	if err != nil {
		t.Fatalf("parseEditedSystemdUnits returned error: %v", err)
	}

	want := []systemdEditUnit{
		{name: db.ArtifactSystemdUnit, content: "[Service]\nExecStart=/bin/true"},
		{name: db.ArtifactSystemdTimerFile, content: "[Timer]\nOnCalendar=hourly"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("units = %#v, want %#v", got, want)
	}
}

func TestParseEditedSystemdUnitsRejectsMismatchedSections(t *testing.T) {
	_, err := parseEditedSystemdUnits([]byte("[Service]\nExecStart=/bin/true\n"), 1)
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	if !strings.Contains(err.Error(), "mismatched number of unit files and contents") {
		t.Fatalf("error = %v, want mismatch", err)
	}
}

func TestStageEditedSystemdUnitsWritesNewArtifactPaths(t *testing.T) {
	dir := t.TempDir()
	unitPath := filepath.Join(dir, "svc.service")
	timerPath := filepath.Join(dir, "svc.timer")
	if err := os.WriteFile(unitPath, []byte("old unit\n"), 0644); err != nil {
		t.Fatalf("WriteFile unit returned error: %v", err)
	}
	if err := os.WriteFile(timerPath, []byte("old timer\n"), 0644); err != nil {
		t.Fatalf("WriteFile timer returned error: %v", err)
	}

	paths, err := stageEditedSystemdUnits(db.ArtifactStore{
		db.ArtifactSystemdUnit:      {Refs: map[db.ArtifactRef]string{"latest": unitPath}},
		db.ArtifactSystemdTimerFile: {Refs: map[db.ArtifactRef]string{"latest": timerPath}},
	}, []systemdEditUnit{
		{name: db.ArtifactSystemdUnit, content: "[Service]\nExecStart=/bin/false"},
		{name: db.ArtifactSystemdTimerFile, content: "[Timer]\nOnCalendar=daily"},
	})
	if err != nil {
		t.Fatalf("stageEditedSystemdUnits returned error: %v", err)
	}

	assertStagedContent(t, paths[db.ArtifactSystemdUnit], unitPath, "[Service]\nExecStart=/bin/false")
	assertStagedContent(t, paths[db.ArtifactSystemdTimerFile], timerPath, "[Timer]\nOnCalendar=daily")
}

func TestStageEditedSystemdUnitsRejectsMissingArtifact(t *testing.T) {
	_, err := stageEditedSystemdUnits(db.ArtifactStore{}, []systemdEditUnit{
		{name: db.ArtifactSystemdUnit, content: "[Service]\nExecStart=/bin/false"},
	})
	if err == nil {
		t.Fatal("expected missing artifact error")
	}
	if !strings.Contains(err.Error(), `no unit file found for "systemd.service"`) {
		t.Fatalf("error = %v, want missing unit file", err)
	}
}

func assertStagedContent(t *testing.T, gotPath, oldPath, want string) {
	t.Helper()
	if gotPath == "" {
		t.Fatal("staged path is empty")
	}
	if gotPath == oldPath {
		t.Fatalf("staged path reused old path %q", gotPath)
	}
	got, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) returned error: %v", gotPath, err)
	}
	if string(got) != want {
		t.Fatalf("staged content = %q, want %q", got, want)
	}
}
