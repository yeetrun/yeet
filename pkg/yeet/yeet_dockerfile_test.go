// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

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
	prevRemove := removeDockerImageFn
	defer func() {
		serviceOverride = prevSvc
		buildDockerImageForRemoteFn = prevBuild
		tryRunDockerFn = prevTry
		removeDockerImageFn = prevRemove
	}()

	serviceOverride = "svc"
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "dockerfile")
	var gotBuildPath, gotBuildImage string
	buildDockerImageForRemoteFn = func(ctx context.Context, path, image string) error {
		gotBuildPath = path
		gotBuildImage = image
		return nil
	}
	var gotRunImage string
	tryRunDockerFn = func(ctx context.Context, image string, args []string) (bool, error) {
		gotRunImage = image
		return true, nil
	}
	removeContextSeen := false
	removeDockerImageFn = func(ctx context.Context, image string) error {
		removeContextSeen = ctx.Value(contextKey{}) == "dockerfile"
		return nil
	}

	ok, err := tryRunDockerfileContext(ctx, df, []string{"--net=svc"})
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
	if !removeContextSeen {
		t.Fatal("remove docker image did not receive tryRunDockerfile context")
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
