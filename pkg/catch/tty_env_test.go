// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import "testing"

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
