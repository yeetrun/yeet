// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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
