// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParsePushRequest(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    pushRequest
		wantErr string
	}{
		{
			name: "direct image",
			args: []string{"svc-a", "registry.example.com/team/app:v1"},
			want: pushRequest{Service: "svc-a", Image: "registry.example.com/team/app:v1", Tag: "latest"},
		},
		{
			name: "strips command name",
			args: []string{"push", "svc-a", "app:v1"},
			want: pushRequest{Service: "svc-a", Image: "app:v1", Tag: "latest"},
		},
		{
			name: "run tag",
			args: []string{"svc-a", "--run", "app:v1"},
			want: pushRequest{Service: "svc-a", Image: "app:v1", Tag: "run"},
		},
		{
			name: "all local",
			args: []string{"svc-a", "--all-local"},
			want: pushRequest{Service: "svc-a", AllLocal: true},
		},
		{
			name:    "missing service",
			args:    nil,
			wantErr: "missing svc argument",
		},
		{
			name:    "missing image",
			args:    []string{"svc-a"},
			wantErr: "missing image argument",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePushRequest(tt.args)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("parsePushRequest error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePushRequest returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("parsePushRequest = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestDockerBuildPlan(t *testing.T) {
	got, err := dockerBuildPlan("/tmp/app/Dockerfile", "svc:yeet-build-test", "linux", "arm64")
	if err != nil {
		t.Fatalf("dockerBuildPlan returned error: %v", err)
	}
	want := []string{
		"build",
		"--platform", "linux/arm64",
		"-t", "svc:yeet-build-test",
		"-f", "/tmp/app/Dockerfile",
		"/tmp/app",
	}
	if !reflect.DeepEqual(got.Args, want) {
		t.Fatalf("dockerBuildPlan args = %#v, want %#v", got.Args, want)
	}

	if _, err := dockerBuildPlan("/tmp/app/Dockerfile", "svc:tag", "darwin", "arm64"); err == nil || !strings.Contains(err.Error(), "remote host is not running linux") {
		t.Fatalf("dockerBuildPlan non-linux error = %v, want linux error", err)
	}
	if _, err := dockerBuildPlan("/tmp/app/Dockerfile", "svc:tag", "linux", "riscv64"); err == nil || !strings.Contains(err.Error(), "unsupported architecture") {
		t.Fatalf("dockerBuildPlan unsupported arch error = %v, want arch error", err)
	}
}

func TestBuildDockerImageForRemoteUsesPlannedDockerCommand(t *testing.T) {
	tmp := t.TempDir()
	dockerfile := filepath.Join(tmp, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM scratch\n"), 0644); err != nil {
		t.Fatalf("write dockerfile: %v", err)
	}

	binDir := filepath.Join(tmp, "bin")
	if err := os.Mkdir(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	argsFile := filepath.Join(tmp, "args")
	fakeDocker := filepath.Join(binDir, "docker")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\n", argsFile)
	if err := os.WriteFile(fakeDocker, []byte(script), 0755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldArch := remoteCatchOSAndArchFn
	defer func() {
		remoteCatchOSAndArchFn = oldArch
	}()
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}

	if err := buildDockerImageForRemote(context.Background(), dockerfile, "svc:build-test"); err != nil {
		t.Fatalf("buildDockerImageForRemote returned error: %v", err)
	}

	gotBytes, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(gotBytes)), "\n")
	want := []string{"build", "--platform", "linux/amd64", "-t", "svc:build-test", "-f", dockerfile, tmp}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("docker args = %#v, want %#v", got, want)
	}
}

func TestPushTargetImageName(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		image   string
		tag     string
		want    string
		wantErr string
	}{
		{
			name:  "plain image strips tag",
			host:  "catch.example.ts.net",
			image: "app:dev",
			tag:   "latest",
			want:  "catch.example.ts.net/app:latest",
		},
		{
			name:  "registry host is stripped",
			host:  "catch.example.ts.net",
			image: "registry.example.com/team/app:v1",
			tag:   "run",
			want:  "catch.example.ts.net/team/app:run",
		},
		{
			name:  "registry port is stripped",
			host:  "catch.example.ts.net",
			image: "localhost:5000/app:v1",
			tag:   "latest",
			want:  "catch.example.ts.net/app:latest",
		},
		{
			name:    "too many path components after registry",
			host:    "catch.example.ts.net",
			image:   "registry.example.com/team/app/sidecar:v1",
			tag:     "latest",
			wantErr: "repo must be in format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pushTargetImageName(tt.host, tt.image, tt.tag)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("pushTargetImageName error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("pushTargetImageName returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("pushTargetImageName = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPushImageWithDepsPlansTagPushAndCleanup(t *testing.T) {
	var pushedSource, pushedTarget string
	err := pushImageWithDeps(context.Background(), "registry.example.com/team/app:v1", "run", pushImageDeps{
		host: func(context.Context) (string, error) {
			return "catch.example.ts.net", nil
		},
		imageExists: func(image string) bool {
			return image == "registry.example.com/team/app:v1"
		},
		push: func(source, target string) error {
			pushedSource = source
			pushedTarget = target
			return nil
		},
	})
	if err != nil {
		t.Fatalf("pushImageWithDeps returned error: %v", err)
	}
	if pushedSource != "registry.example.com/team/app:v1" {
		t.Fatalf("source = %q, want original image", pushedSource)
	}
	if pushedTarget != "catch.example.ts.net/team/app:run" {
		t.Fatalf("target = %q, want rewritten registry ref", pushedTarget)
	}
}

func TestLocalImagesFromDockerOutput(t *testing.T) {
	got := localImagesFromDockerOutput([]byte("\nrepo/svc/app:latest\n\nrepo/svc/sidecar:dev\n"))
	want := []string{"repo/svc/app:latest", "repo/svc/sidecar:dev"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("localImagesFromDockerOutput = %#v, want %#v", got, want)
	}
}

func TestLocalImagePushDecision(t *testing.T) {
	tests := []struct {
		name       string
		image      string
		imageOS    string
		imageArch  string
		remoteOS   string
		remoteArch string
		wantPush   bool
		wantSkip   string
	}{
		{
			name:       "matching target",
			image:      "repo/svc/app:latest",
			imageOS:    "linux",
			imageArch:  "arm64",
			remoteOS:   "linux",
			remoteArch: "arm64",
			wantPush:   true,
		},
		{
			name:       "wrong os",
			image:      "repo/svc/app:latest",
			imageOS:    "darwin",
			imageArch:  "arm64",
			remoteOS:   "linux",
			remoteArch: "arm64",
			wantSkip:   `skipping, image "repo/svc/app:latest" is for (local) darwin, not (remote) linux`,
		},
		{
			name:       "wrong arch",
			image:      "repo/svc/app:latest",
			imageOS:    "linux",
			imageArch:  "amd64",
			remoteOS:   "linux",
			remoteArch: "arm64",
			wantSkip:   `skipping, image "repo/svc/app:latest" is for (local) amd64, not (remote) arm64`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPush, gotSkip := localImagePushDecision(tt.image, tt.imageOS, tt.imageArch, tt.remoteOS, tt.remoteArch)
			if gotPush != tt.wantPush {
				t.Fatalf("push = %v, want %v", gotPush, tt.wantPush)
			}
			if gotSkip != tt.wantSkip {
				t.Fatalf("skip = %q, want %q", gotSkip, tt.wantSkip)
			}
		})
	}
}
