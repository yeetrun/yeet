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

func TestPrepareEditSourceForConfigWritesJSON(t *testing.T) {
	source, err := serviceConfigEditSource(&db.Service{
		Name:        "svc-config",
		ServiceType: db.ServiceTypeSystemd,
		Generation:  3,
	})
	if err != nil {
		t.Fatalf("serviceConfigEditSource returned error: %v", err)
	}
	defer source.cleanupInto(new(error))

	got, err := os.ReadFile(source.path)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if !strings.Contains(string(got), `"Name": "svc-config"`) {
		t.Fatalf("source = %s, want service name JSON", got)
	}
}

func TestSystemdEditSourceRejectsNoUnits(t *testing.T) {
	_, err := systemdEditSource(db.ArtifactStore{})
	if err == nil || !strings.Contains(err.Error(), "no unit files found") {
		t.Fatalf("systemdEditSource error = %v, want no units", err)
	}
}

func TestCopyToTmpFileCreatesEmptyTempForMissingSource(t *testing.T) {
	tmpPath, err := copyToTmpFile("")
	if err != nil {
		t.Fatalf("copyToTmpFile returned error: %v", err)
	}
	defer func() {
		if err := removeFile(tmpPath); err != nil {
			t.Fatalf("remove temp: %v", err)
		}
	}()
	got, err := os.ReadFile(tmpPath)
	if err != nil {
		t.Fatalf("read temp: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("temp contents = %q, want empty", got)
	}
}

func TestRunEditSessionNoChangesPrintsMessage(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	tmp := filepath.Join(dir, "edit.txt")
	if err := os.WriteFile(src, []byte("same"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(tmp, []byte("same"), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}

	var out bytes.Buffer
	execer := &ttyExecer{
		rw: &out,
		editFileFunc: func(path string) error {
			if path != tmp {
				t.Fatalf("edit path = %q, want %q", path, tmp)
			}
			return nil
		},
	}

	changed, err := execer.runEditSession(&editSession{
		source:  editSource{path: src},
		tmpPath: tmp,
	})
	if err != nil {
		t.Fatalf("runEditSession returned error: %v", err)
	}
	if changed {
		t.Fatal("changed = true, want false")
	}
	if got := out.String(); got != "No changes detected\n" {
		t.Fatalf("output = %q, want no changes message", got)
	}
}

func TestRunEditSessionReportsChanges(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	tmp := filepath.Join(dir, "edit.txt")
	if err := os.WriteFile(src, []byte("before"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(tmp, []byte("before"), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}

	execer := &ttyExecer{
		rw: &bytes.Buffer{},
		editFileFunc: func(path string) error {
			return os.WriteFile(path, []byte("after"), 0o644)
		},
	}

	changed, err := execer.runEditSession(&editSession{
		source:  editSource{path: src},
		tmpPath: tmp,
	})
	if err != nil {
		t.Fatalf("runEditSession returned error: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
}

func TestEditFileRequiresPTYBeforeRunningCommand(t *testing.T) {
	execer := &ttyExecer{isPty: false}
	err := execer.editFile("/tmp/service")
	if err == nil || !strings.Contains(err.Error(), "edit requires a pty") {
		t.Fatalf("editFile error = %v, want pty requirement", err)
	}
}

func TestApplyEditSessionRejectsUnsupportedServiceType(t *testing.T) {
	execer := &ttyExecer{}
	err := execer.applyEditSession(&editSession{serviceType: db.ServiceType("unknown")})
	if err == nil || !strings.Contains(err.Error(), "unsupported service type") {
		t.Fatalf("applyEditSession error = %v, want unsupported type", err)
	}
}

func TestStageEditedArtifactsUpdatesStagedRefs(t *testing.T) {
	server := newTestServer(t)
	if _, _, err := server.cfg.DB.MutateService("svc-edit", func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeSystemd
		s.Artifacts = db.ArtifactStore{
			db.ArtifactSystemdUnit: {Refs: map[db.ArtifactRef]string{"latest": "/tmp/old.service"}},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed service: %v", err)
	}

	execer := &ttyExecer{s: server, sn: "svc-edit"}
	if err := execer.stageEditedArtifacts(map[db.ArtifactName]string{
		db.ArtifactSystemdUnit: "/tmp/new.service",
	}); err != nil {
		t.Fatalf("stageEditedArtifacts returned error: %v", err)
	}

	sv, err := server.serviceView("svc-edit")
	if err != nil {
		t.Fatalf("serviceView: %v", err)
	}
	got, ok := sv.AsStruct().Artifacts.Staged(db.ArtifactSystemdUnit)
	if !ok || got != "/tmp/new.service" {
		t.Fatalf("staged unit = %q, %v; want /tmp/new.service true", got, ok)
	}
}

func TestStageEditedArtifactRejectsMissingArtifact(t *testing.T) {
	err := stageEditedArtifact(&db.Service{Artifacts: db.ArtifactStore{}}, db.ArtifactSystemdUnit, "/tmp/new.service")
	if err == nil || !strings.Contains(err.Error(), `no artifact found for "systemd.service"`) {
		t.Fatalf("stageEditedArtifact error = %v, want missing artifact", err)
	}
}

func TestReadEditedSystemdUnitsReadsAndParsesFile(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "units.txt")
	raw := fmt.Sprintf("%s\n\n[Service]\nExecStart=/bin/true\n", fmt.Sprintf(editUnitsSeparator, db.ArtifactSystemdUnit))
	if err := os.WriteFile(tmp, []byte(raw), 0o644); err != nil {
		t.Fatalf("write units: %v", err)
	}

	units, err := readEditedSystemdUnits(tmp, 1)
	if err != nil {
		t.Fatalf("readEditedSystemdUnits returned error: %v", err)
	}
	want := []systemdEditUnit{{name: db.ArtifactSystemdUnit, content: "[Service]\nExecStart=/bin/true"}}
	if !reflect.DeepEqual(units, want) {
		t.Fatalf("units = %#v, want %#v", units, want)
	}
}
