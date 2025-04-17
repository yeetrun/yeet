// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTryRunDockerfileBuildsAndDelegates(t *testing.T) {
	tmp := t.TempDir()
	df := filepath.Join(tmp, "Dockerfile")
	if err := os.WriteFile(df, []byte("FROM scratch\n"), 0644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}

	prevSvc := serviceOverride
	prevBuild := buildDockerImageForRemoteFn
	prevTry := tryRunDockerFn
	defer func() {
		serviceOverride = prevSvc
		buildDockerImageForRemoteFn = prevBuild
		tryRunDockerFn = prevTry
	}()

	serviceOverride = "svc"
	var gotBuildPath, gotBuildImage string
	buildDockerImageForRemoteFn = func(ctx context.Context, path, image string) error {
		gotBuildPath = path
		gotBuildImage = image
		return nil
	}
	var gotRunImage string
	tryRunDockerFn = func(image string, args []string) (bool, error) {
		gotRunImage = image
		return true, nil
	}

	ok, err := tryRunDockerfile(df, []string{"--net=svc"})
	if err != nil {
		t.Fatalf("tryRunDockerfile returned error: %v", err)
	}
	if !ok {
		t.Fatalf("tryRunDockerfile returned ok=false")
	}
	if gotBuildPath != df {
		t.Fatalf("build path = %q, want %q", gotBuildPath, df)
	}
	if !strings.HasPrefix(gotBuildImage, "svc:yeet-build-") {
		t.Fatalf("build image = %q, want prefix %q", gotBuildImage, "svc:yeet-build-")
	}
	if gotRunImage != gotBuildImage {
		t.Fatalf("run image = %q, want %q", gotRunImage, gotBuildImage)
	}
}

func TestTryRunDockerfileNonDockerfile(t *testing.T) {
	ok, err := tryRunDockerfile("not-a-dockerfile.txt", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("expected ok=false for non-Dockerfile payload")
	}
}
