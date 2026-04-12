// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"strings"
	"testing"
)

func TestBuildCatchCmdDisablesCGOForCrossCompile(t *testing.T) {
	cmd := buildCatchCmd("amd64", "/tmp/repo")

	if got := cmd.Dir; got != "/tmp/repo" {
		t.Fatalf("Dir = %q, want /tmp/repo", got)
	}
	if got := strings.Join(cmd.Args, " "); got != "go build -o catch ./cmd/catch" {
		t.Fatalf("Args = %q", got)
	}

	env := strings.Join(cmd.Env, "\n")
	for _, want := range []string{
		"GOOS=linux",
		"GOARCH=amd64",
		"CGO_ENABLED=0",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("expected %q in env", want)
		}
	}
}
