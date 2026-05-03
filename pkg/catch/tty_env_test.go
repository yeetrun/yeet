// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

func TestParseEnvAssignmentsUsesLastValueForDuplicateKey(t *testing.T) {
	assignments, err := parseEnvAssignments([]string{"FOO=one", "BAR=two", "FOO=three"})
	if err != nil {
		t.Fatalf("parseEnvAssignments failed: %v", err)
	}
	want := []envAssignment{
		{Key: "FOO", Value: "three"},
		{Key: "BAR", Value: "two"},
	}
	if len(assignments) != len(want) {
		t.Fatalf("assignment count = %d, want %d", len(assignments), len(want))
	}
	for i := range want {
		if assignments[i] != want[i] {
			t.Fatalf("assignment %d = %#v, want %#v", i, assignments[i], want[i])
		}
	}
}

func TestSplitEnvAssignmentRejectsLineBreaksInValue(t *testing.T) {
	_, _, err := splitEnvAssignment("FOO=one\nBAR=two")
	if err == nil {
		t.Fatalf("expected newline value to be rejected")
	}
}

func TestIsValidEnvKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{key: "FOO", want: true},
		{key: "_FOO1", want: true},
		{key: "", want: false},
		{key: "1FOO", want: false},
		{key: "FOO-BAR", want: false},
		{key: "FOO BAR", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := isValidEnvKey(tt.key); got != tt.want {
				t.Fatalf("isValidEnvKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestApplyEnvAssignmentsUpdatesExistingKey(t *testing.T) {
	contents := []byte("FOO=one\nBAR=two\n")
	out, changed, err := applyEnvAssignments(contents, []envAssignment{{Key: "FOO", Value: "three"}})
	if err != nil {
		t.Fatalf("applyEnvAssignments failed: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true")
	}
	want := "FOO=three\nBAR=two\n"
	if string(out) != want {
		t.Fatalf("unexpected output:\n%s", string(out))
	}
}

func TestApplyEnvAssignmentsPreservesExportPrefix(t *testing.T) {
	contents := []byte("export FOO=one\n")
	out, changed, err := applyEnvAssignments(contents, []envAssignment{{Key: "FOO", Value: "two"}})
	if err != nil {
		t.Fatalf("applyEnvAssignments failed: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true")
	}
	want := "export FOO=two\n"
	if string(out) != want {
		t.Fatalf("unexpected output:\n%s", string(out))
	}
}

func TestApplyEnvAssignmentsAppendsMissingKey(t *testing.T) {
	contents := []byte("FOO=one\n")
	out, changed, err := applyEnvAssignments(contents, []envAssignment{{Key: "BAR", Value: "two"}})
	if err != nil {
		t.Fatalf("applyEnvAssignments failed: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true")
	}
	want := "FOO=one\nBAR=two\n"
	if string(out) != want {
		t.Fatalf("unexpected output:\n%s", string(out))
	}
}

func TestApplyEnvAssignmentsNoChange(t *testing.T) {
	contents := []byte("FOO=one\n")
	out, changed, err := applyEnvAssignments(contents, []envAssignment{{Key: "FOO", Value: "one"}})
	if err != nil {
		t.Fatalf("applyEnvAssignments failed: %v", err)
	}
	if changed {
		t.Fatalf("expected changed=false")
	}
	if string(out) != string(contents) {
		t.Fatalf("expected output to match input")
	}
}

func TestApplyEnvAssignmentsUnsetKey(t *testing.T) {
	contents := []byte("FOO=one\nBAR=two\n")
	out, changed, err := applyEnvAssignments(contents, []envAssignment{{Key: "FOO", Value: ""}})
	if err != nil {
		t.Fatalf("applyEnvAssignments failed: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true")
	}
	want := "BAR=two\n"
	if string(out) != want {
		t.Fatalf("unexpected output:\n%s", string(out))
	}
}

func TestApplyEnvAssignmentsUnsetsAdjacentKeys(t *testing.T) {
	contents := []byte("FOO=one\nBAR=two\nBAZ=three\n")
	out, changed, err := applyEnvAssignments(contents, []envAssignment{
		{Key: "FOO", Value: ""},
		{Key: "BAR", Value: ""},
	})
	if err != nil {
		t.Fatalf("applyEnvAssignments failed: %v", err)
	}
	if !changed {
		t.Fatalf("expected changed=true")
	}
	want := "BAZ=three\n"
	if string(out) != want {
		t.Fatalf("unexpected output:\n%s", string(out))
	}
}

func TestApplyEnvAssignmentsUnsetMissingNoChange(t *testing.T) {
	contents := []byte("FOO=one\n")
	out, changed, err := applyEnvAssignments(contents, []envAssignment{{Key: "BAR", Value: ""}})
	if err != nil {
		t.Fatalf("applyEnvAssignments failed: %v", err)
	}
	if changed {
		t.Fatalf("expected changed=false")
	}
	if string(out) != string(contents) {
		t.Fatalf("expected output to match input")
	}
}

func TestEnvCommandFromArgsParsesShow(t *testing.T) {
	cmd, err := envCommandFromArgs([]string{"show", "--staged"})
	if err != nil {
		t.Fatalf("envCommandFromArgs failed: %v", err)
	}
	if cmd.name != envSubcommandShow {
		t.Fatalf("command name = %q, want %q", cmd.name, envSubcommandShow)
	}
	if !cmd.showFlags.Staged {
		t.Fatalf("expected staged show flag")
	}
}

func TestEnvCommandFromArgsParsesSetAssignments(t *testing.T) {
	cmd, err := envCommandFromArgs([]string{"set", "FOO=one", "BAR=two"})
	if err != nil {
		t.Fatalf("envCommandFromArgs failed: %v", err)
	}
	if cmd.name != envSubcommandSet {
		t.Fatalf("command name = %q, want %q", cmd.name, envSubcommandSet)
	}
	want := []envAssignment{{Key: "FOO", Value: "one"}, {Key: "BAR", Value: "two"}}
	if len(cmd.assignments) != len(want) {
		t.Fatalf("assignment count = %d, want %d", len(cmd.assignments), len(want))
	}
	for i := range want {
		if cmd.assignments[i] != want[i] {
			t.Fatalf("assignment %d = %#v, want %#v", i, cmd.assignments[i], want[i])
		}
	}
}

func TestEnvCommandFromArgsRejectsExtraEditArgs(t *testing.T) {
	_, err := envCommandFromArgs([]string{"edit", "extra"})
	if err == nil {
		t.Fatalf("expected env edit args to be rejected")
	}
}

func TestPrintEnv(t *testing.T) {
	server := newTestServer(t)
	dir := t.TempDir()
	latest := filepath.Join(dir, ".env")
	staged := filepath.Join(dir, ".env.staged")
	if err := os.WriteFile(latest, []byte("FOO=one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("FOO=two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := serviceWithEnvArtifacts(latest, staged)

	var out bytes.Buffer
	if err := server.printEnv(&out, service.View(), false); err != nil {
		t.Fatalf("printEnv latest: %v", err)
	}
	if got := out.String(); got != "FOO=one\n\n" {
		t.Fatalf("latest printEnv output = %q", got)
	}

	out.Reset()
	if err := server.printEnv(&out, service.View(), true); err != nil {
		t.Fatalf("printEnv staged: %v", err)
	}
	if got := out.String(); got != "FOO=two\n\n" {
		t.Fatalf("staged printEnv output = %q", got)
	}
}

func TestPrintEnvReportsMissingFiles(t *testing.T) {
	server := newTestServer(t)
	service := (&db.Service{Name: "svc", Artifacts: db.ArtifactStore{}}).View()

	if err := server.printEnv(&bytes.Buffer{}, service, false); err == nil {
		t.Fatal("expected missing latest env error")
	}
	if err := server.printEnv(&bytes.Buffer{}, service, true); err == nil {
		t.Fatal("expected missing staged env error")
	}
}

func TestRunEnvCommandShow(t *testing.T) {
	server := newTestServer(t)
	dir := t.TempDir()
	latest := filepath.Join(dir, ".env")
	staged := filepath.Join(dir, ".env.staged")
	if err := os.WriteFile(latest, []byte("FOO=one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("FOO=two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := serviceWithEnvArtifacts(latest, staged)
	service.Name = "svc"
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"svc": service}}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  "svc",
		rw:  &out,
	}
	err := execer.runEnvCommand(envCommand{
		name:      envSubcommandShow,
		showFlags: cli.EnvShowFlags{Staged: true},
	})
	if err != nil {
		t.Fatalf("runEnvCommand show: %v", err)
	}
	if got := out.String(); got != "FOO=two\n\n" {
		t.Fatalf("runEnvCommand output = %q", got)
	}
}

func TestRunEnvCommandRejectsUnknown(t *testing.T) {
	err := (&ttyExecer{}).runEnvCommand(envCommand{name: "bogus"})
	if err == nil {
		t.Fatal("expected unknown env command error")
	}
}

func TestEditEnvCmdFuncReturnsServiceError(t *testing.T) {
	execer := &ttyExecer{s: newTestServer(t), sn: "missing"}

	err := execer.editEnvCmdFunc()
	if !errors.Is(err, errServiceNotFound) {
		t.Fatalf("editEnvCmdFunc error = %v, want errServiceNotFound", err)
	}
}

func TestEnvSetCmdFuncNoChange(t *testing.T) {
	server := newTestServer(t)
	dir := t.TempDir()
	latest := filepath.Join(dir, ".env")
	if err := os.WriteFile(latest, []byte("FOO=one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := serviceWithEnvArtifacts(latest, "")
	service.Name = "svc"
	if err := server.cfg.DB.Set(&db.Data{Services: map[string]*db.Service{"svc": service}}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	execer := &ttyExecer{s: server, sn: "svc", rw: &out}
	err := execer.envSetCmdFunc([]envAssignment{{Key: "FOO", Value: "one"}})
	if err != nil {
		t.Fatalf("envSetCmdFunc failed: %v", err)
	}
	if got := out.String(); got != "No changes detected\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestPrepareEditedEnvFileDetectsNoChangeWithExistingSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, ".env")
	tmp := filepath.Join(dir, "tmp.env")
	if err := os.WriteFile(src, []byte("FOO=one\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(tmp, []byte("FOO=one\n"), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	changed, err := editedEnvFileChanged(src, tmp)
	if err != nil {
		t.Fatalf("editedEnvFileChanged failed: %v", err)
	}
	if changed {
		t.Fatalf("expected unchanged files")
	}
}

func TestPrepareEnvEditSessionCopiesLatestEnvFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, ".env")
	if err := os.WriteFile(src, []byte("FOO=one\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	session, err := prepareEnvEditSession(serviceWithEnvArtifacts(src, "").View())
	if err != nil {
		t.Fatalf("prepareEnvEditSession failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(session.tmpPath)
	})

	if session.srcPath != src {
		t.Fatalf("session source = %q, want %q", session.srcPath, src)
	}
	got, err := os.ReadFile(session.tmpPath)
	if err != nil {
		t.Fatalf("read temp env: %v", err)
	}
	if string(got) != "FOO=one\n" {
		t.Fatalf("temp env content = %q", got)
	}
}

func TestApplyEnvEditSessionReportsNoChange(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, ".env")
	tmp := filepath.Join(dir, "tmp.env")
	for _, path := range []string{src, tmp} {
		if err := os.WriteFile(path, []byte("FOO=one\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	var out bytes.Buffer
	execer := &ttyExecer{rw: &out}
	err := execer.applyEnvEditSession(envEditSession{srcPath: src, tmpPath: tmp})
	if err != nil {
		t.Fatalf("applyEnvEditSession failed: %v", err)
	}
	if got := out.String(); got != "No changes detected\n" {
		t.Fatalf("output = %q", got)
	}
}

func serviceWithEnvArtifacts(latest, staged string) *db.Service {
	refs := map[db.ArtifactRef]string{}
	if latest != "" {
		refs["latest"] = latest
	}
	if staged != "" {
		refs["staged"] = staged
	}
	return &db.Service{
		Name: "svc",
		Artifacts: db.ArtifactStore{
			db.ArtifactEnvFile: {Refs: refs},
		},
	}
}

func TestPrepareEditedEnvFileDetectsNoChangeWithEmptyNewFile(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "tmp.env")
	if err := os.WriteFile(tmp, nil, 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	changed, err := editedEnvFileChanged("", tmp)
	if err != nil {
		t.Fatalf("editedEnvFileChanged failed: %v", err)
	}
	if changed {
		t.Fatalf("expected empty new env file to be unchanged")
	}
}

func TestRemoveTempFileReturnsCleanupError(t *testing.T) {
	err := removeTempFile(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatalf("expected remove error")
	}
}

func TestNewEnvSetPlanAppliesAssignments(t *testing.T) {
	plan, err := newEnvSetPlan([]byte("FOO=one\n"), []envAssignment{{Key: "FOO", Value: "two"}})
	if err != nil {
		t.Fatalf("newEnvSetPlan failed: %v", err)
	}
	if !plan.changed {
		t.Fatalf("expected changed=true")
	}
	if got := string(plan.contents); got != "FOO=two\n" {
		t.Fatalf("plan contents = %q", got)
	}
}

func TestRunEnvSetPlanReportsNoChange(t *testing.T) {
	var out bytes.Buffer
	execer := &ttyExecer{rw: &out}
	err := execer.runEnvSetPlan(envSetPlan{contents: []byte("FOO=one\n")})
	if err != nil {
		t.Fatalf("runEnvSetPlan failed: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "No changes detected\n") {
		t.Fatalf("output = %q", got)
	}
}

func TestEnvCommandFromArgsRejectsInvalidForms(t *testing.T) {
	tests := [][]string{
		nil,
		{"bogus"},
		{"show", "extra"},
		{"copy", "extra"},
		{"set"},
		{"set", "NO_EQUALS"},
		{"set", " BAD=value"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, err := envCommandFromArgs(args); err == nil {
				t.Fatalf("envCommandFromArgs(%v) returned nil error", args)
			}
		})
	}
}

func TestSplitEnvFileLinesAndJoinPreserveTrailingNewline(t *testing.T) {
	lines, trailing := splitEnvFileLines([]byte("A=1\nB=2\n"))
	if !trailing || len(lines) != 2 {
		t.Fatalf("lines=%#v trailing=%v", lines, trailing)
	}
	if got := string(joinEnvFileLines(lines, trailing)); got != "A=1\nB=2\n" {
		t.Fatalf("joined = %q", got)
	}

	lines, trailing = splitEnvFileLines(nil)
	if trailing || len(lines) != 0 {
		t.Fatalf("empty lines=%#v trailing=%v", lines, trailing)
	}
	if got := string(joinEnvFileLines(nil, false)); got != "" {
		t.Fatalf("empty join = %q", got)
	}
}

func TestParseEnvLine(t *testing.T) {
	parsed, ok := parseEnvLine("  export FOO =bar")
	if !ok {
		t.Fatal("expected env line match")
	}
	if parsed.prefix != "  export " || parsed.key != "FOO" {
		t.Fatalf("parsed = %#v", parsed)
	}
	if _, ok := parseEnvLine("# FOO=bar"); ok {
		t.Fatal("comment should not parse as env assignment")
	}
}

func TestEditedEnvFileChangedDetectsNewNonEmptyFile(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "tmp.env")
	if err := os.WriteFile(tmp, []byte("FOO=one\n"), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	changed, err := editedEnvFileChanged("", tmp)
	if err != nil {
		t.Fatalf("editedEnvFileChanged: %v", err)
	}
	if !changed {
		t.Fatal("expected non-empty new env file to be changed")
	}
}

func TestCloseEnvFileAndReadEnvFileIfExists(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "env")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if err := closeEnvFile(f); err != nil {
		t.Fatalf("closeEnvFile: %v", err)
	}
	if err := closeEnvFile(f); err == nil {
		t.Fatal("expected second close to report error")
	}

	missing := filepath.Join(t.TempDir(), "missing.env")
	contents, err := readEnvFileIfExists(missing)
	if err != nil {
		t.Fatalf("read missing env: %v", err)
	}
	if contents != nil {
		t.Fatalf("missing contents = %q, want nil", contents)
	}
	if _, err := readEnvFileIfExists(t.TempDir()); err == nil {
		t.Fatal("expected read error for directory")
	}
}

func TestCurrentEnvContentsReturnsServiceError(t *testing.T) {
	_, err := (&ttyExecer{s: newTestServer(t), sn: "missing"}).currentEnvContents()
	if !errors.Is(err, errServiceNotFound) {
		t.Fatalf("currentEnvContents error = %v, want errServiceNotFound", err)
	}
}
