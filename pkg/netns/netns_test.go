// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package netns

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestWriteServiceNetNSOrdersBeforeDockerPrereqs(t *testing.T) {
	root := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd returned error: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir returned error: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatalf("restore Chdir returned error: %v", err)
		}
	})

	binDir := filepath.Join(root, "bin")
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("MkdirAll binDir returned error: %v", err)
	}
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatalf("MkdirAll runDir returned error: %v", err)
	}

	artifacts, err := WriteServiceNetNS(binDir, runDir, Service{ServiceName: "plex"})
	if err != nil {
		t.Fatalf("WriteServiceNetNS returned error: %v", err)
	}
	raw, err := os.ReadFile(artifacts[db.ArtifactNetNSService])
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	got := string(raw)
	for _, want := range []string{
		"Requires=yeet-ns.service\n",
		"After=yeet-ns.service\n",
		"Before=yeet-docker-prereqs.target docker.service\n",
		"WantedBy=multi-user.target yeet-docker-prereqs.target\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unit missing %q:\n%s", want, got)
		}
	}
}
