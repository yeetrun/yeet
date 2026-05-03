// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import "testing"

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
