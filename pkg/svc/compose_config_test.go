// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestResolveComposeJSONUsesExactFiles(t *testing.T) {
	projectDir := t.TempDir()
	files := []string{
		filepath.Join(projectDir, "compose.yml"),
		filepath.Join(projectDir, "compose.network"),
	}
	var got []string
	var command *exec.Cmd
	opts := ComposeResolveOptions{
		ProjectName: "catch-app",
		ProjectDir:  projectDir,
		Files:       files,
		NewCmd: func(_ context.Context, name string, args ...string) *exec.Cmd {
			got = append([]string{filepath.Base(name)}, args...)
			command = exec.Command("sh", "-c", `printf '%s' '{"services":{"api":{"image":"nginx"}}}'`)
			return command
		},
	}

	out, err := ResolveComposeJSON(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"docker", "compose",
		"--project-name", "catch-app",
		"--project-directory", projectDir,
		"--file", files[0],
		"--file", files[1],
		"config", "--format", "json",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("command mismatch (-want +got):\n%s", diff)
	}
	if command.Dir != projectDir {
		t.Fatalf("command Dir = %q, want %q", command.Dir, projectDir)
	}
	if diff := cmp.Diff([]byte(`{"services":{"api":{"image":"nginx"}}}`), out); diff != "" {
		t.Fatalf("output mismatch (-want +got):\n%s", diff)
	}
}

func TestResolveComposeJSONReportsDockerFailure(t *testing.T) {
	opts := ComposeResolveOptions{
		ProjectName: "catch-app",
		ProjectDir:  t.TempDir(),
		Files:       []string{"compose.yml"},
		NewCmd: func(context.Context, string, ...string) *exec.Cmd {
			return exec.Command("sh", "-c", `printf 'invalid compose' >&2; exit 23`)
		},
	}

	_, err := ResolveComposeJSON(context.Background(), opts)
	if err == nil {
		t.Fatal("ResolveComposeJSON returned nil error")
	}
	for _, want := range []string{"resolve Docker Compose application model", "invalid compose"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err, want)
		}
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected cancellation error: %v", err)
	}
}

func TestResolveComposeJSONRequiresExplicitFiles(t *testing.T) {
	_, err := ResolveComposeJSON(context.Background(), ComposeResolveOptions{
		ProjectName: "catch-app",
		ProjectDir:  t.TempDir(),
		NewCmd: func(context.Context, string, ...string) *exec.Cmd {
			return exec.Command("sh", "-c", `printf '%s' '{"services":{}}'`)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "files") {
		t.Fatalf("error = %v, want explicit files error", err)
	}
}
