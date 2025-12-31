// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"context"
	"testing"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/opencontainers/go-digest"
)

func TestParseRepositoryName(t *testing.T) {
	tests := []struct {
		name     string
		repo     string
		expected struct {
			domain string
			path   string
		}
	}{
		{
			name: "simple docker hub image",
			repo: "nginx",
			expected: struct {
				domain string
				path   string
			}{
				domain: "docker.io",
				path:   "library/nginx",
			},
		},
		{
			name: "docker hub user image",
			repo: "user/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "docker.io",
				path:   "user/app",
			},
		},
		{
			name: "docker hub user image with single slash",
			repo: "alpine",
			expected: struct {
				domain string
				path   string
			}{
				domain: "docker.io",
				path:   "library/alpine",
			},
		},
		{
			name: "custom registry with user and app",
			repo: "registry.example.com/user/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "registry.example.com",
				path:   "user/app",
			},
		},
		{
			name: "custom registry with single app",
			repo: "registry.example.com/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "registry.example.com",
				path:   "app",
			},
		},
		{
			name: "custom registry with nested path",
			repo: "registry.example.com/org/project/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "registry.example.com",
				path:   "org/project/app",
			},
		},
		{
			name: "docker hub with explicit domain",
			repo: "docker.io/nginx",
			expected: struct {
				domain string
				path   string
			}{
				domain: "docker.io",
				path:   "library/nginx",
			},
		},
		{
			name: "docker hub with explicit domain and user",
			repo: "docker.io/user/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "docker.io",
				path:   "user/app",
			},
		},
		{
			name: "localhost registry",
			repo: "localhost:5000/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "localhost:5000",
				path:   "app",
			},
		},
		{
			name: "localhost registry with user",
			repo: "localhost:5000/user/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "localhost:5000",
				path:   "user/app",
			},
		},
		{
			name: "single word without dots (treated as docker.io)",
			repo: "myapp",
			expected: struct {
				domain string
				path   string
			}{
				domain: "docker.io",
				path:   "library/myapp",
			},
		},
		{
			name: "two words without dots (treated as docker.io)",
			repo: "user/myapp",
			expected: struct {
				domain string
				path   string
			}{
				domain: "docker.io",
				path:   "user/myapp",
			},
		},
		{
			name: "empty string",
			repo: "",
			expected: struct {
				domain string
				path   string
			}{
				domain: "docker.io",
				path:   "",
			},
		},
		{
			name: "registry with port and complex path",
			repo: "registry.example.com:8080/org/project/subproject/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "registry.example.com:8080",
				path:   "org/project/subproject/app",
			},
		},
		{
			name: "docker hub official image with explicit library",
			repo: "docker.io/library/nginx",
			expected: struct {
				domain string
				path   string
			}{
				domain: "docker.io",
				path:   "library/nginx",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			domain, path := ParseRepositoryName(tt.repo)

			if domain != tt.expected.domain {
				t.Errorf("ParseRepositoryName(%q) domain = %q, want %q", tt.repo, domain, tt.expected.domain)
			}

			if path != tt.expected.path {
				t.Errorf("ParseRepositoryName(%q) path = %q, want %q", tt.repo, path, tt.expected.path)
			}
		})
	}
}

func TestParseRepositoryNameEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		repo     string
		expected struct {
			domain string
			path   string
		}
	}{
		{
			name: "multiple slashes in path",
			repo: "registry.example.com/a/b/c/d",
			expected: struct {
				domain string
				path   string
			}{
				domain: "registry.example.com",
				path:   "a/b/c/d",
			},
		},
		{
			name: "registry with subdomain",
			repo: "sub.registry.example.com/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "sub.registry.example.com",
				path:   "app",
			},
		},
		{
			name: "registry with IP address",
			repo: "192.168.1.100:5000/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "192.168.1.100:5000",
				path:   "app",
			},
		},
		{
			name: "registry with hyphen in domain",
			repo: "my-registry.example.com/app",
			expected: struct {
				domain string
				path   string
			}{
				domain: "my-registry.example.com",
				path:   "app",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			domain, path := ParseRepositoryName(tt.repo)

			if domain != tt.expected.domain {
				t.Errorf("ParseRepositoryName(%q) domain = %q, want %q", tt.repo, domain, tt.expected.domain)
			}

			if path != tt.expected.path {
				t.Errorf("ParseRepositoryName(%q) path = %q, want %q", tt.repo, path, tt.expected.path)
			}
		})
	}
}

func TestCompleteUploadReleasesLeaseOnSuccess(t *testing.T) {
	store := &ContainerdCacheStorage{bgCtx: context.Background()}
	released := 0
	want := digest.FromString("success")
	store.uploads.Store("ok", &containerdUpload{
		writer:  &fakeWriter{digest: want},
		release: func(context.Context) error { released++; return nil },
	})

	got, err := store.CompleteUpload(context.Background(), "ok", want.String())
	if err != nil {
		t.Fatalf("CompleteUpload returned error: %v", err)
	}
	if got != want.String() {
		t.Fatalf("CompleteUpload digest=%q, want %q", got, want.String())
	}
	if released != 1 {
		t.Fatalf("expected release to be called once, got %d", released)
	}
}

func TestCompleteUploadReleasesLeaseOnAlreadyExists(t *testing.T) {
	store := &ContainerdCacheStorage{bgCtx: context.Background()}
	released := 0
	want := digest.FromString("exists")
	store.uploads.Store("exists", &containerdUpload{
		writer:  &fakeWriter{commitErr: errdefs.ErrAlreadyExists},
		release: func(context.Context) error { released++; return nil },
	})

	got, err := store.CompleteUpload(context.Background(), "exists", want.String())
	if err != nil {
		t.Fatalf("CompleteUpload returned error: %v", err)
	}
	if got != want.String() {
		t.Fatalf("CompleteUpload digest=%q, want %q", got, want.String())
	}
	if released != 1 {
		t.Fatalf("expected release to be called once, got %d", released)
	}
}

type fakeWriter struct {
	digest    digest.Digest
	commitErr error
	status    content.Status
}

func (f *fakeWriter) Write(p []byte) (int, error) {
	f.status.Offset += int64(len(p))
	return len(p), nil
}

func (f *fakeWriter) Close() error { return nil }

func (f *fakeWriter) Digest() digest.Digest { return f.digest }

func (f *fakeWriter) Commit(_ context.Context, _ int64, expected digest.Digest, _ ...content.Opt) error {
	if f.commitErr != nil {
		return f.commitErr
	}
	if f.digest == "" {
		f.digest = expected
	}
	return nil
}

func (f *fakeWriter) Status() (content.Status, error) { return f.status, nil }

func (f *fakeWriter) Truncate(size int64) error {
	f.status.Offset = size
	return nil
}

// TestParseRepositoryNameConsistency tests that the function is consistent
// with Docker's repository naming conventions.
func TestParseRepositoryNameConsistency(t *testing.T) {
	// Test that official Docker Hub images get the library prefix.
	officialImages := []string{"nginx", "alpine", "ubuntu", "redis", "postgres"}

	for _, img := range officialImages {
		domain, path := ParseRepositoryName(img)
		if domain != "docker.io" {
			t.Errorf("ParseRepositoryName(%q) domain = %q, want docker.io", img, domain)
		}
		if path != "library/"+img {
			t.Errorf("ParseRepositoryName(%q) path = %q, want library/%s", img, path, img)
		}
	}

	// Test that user images on Docker Hub don't get the library prefix.
	userImages := []string{"user/app", "company/service", "org/project"}

	for _, img := range userImages {
		domain, path := ParseRepositoryName(img)
		if domain != "docker.io" {
			t.Errorf("ParseRepositoryName(%q) domain = %q, want docker.io", img, domain)
		}
		if path != img {
			t.Errorf("ParseRepositoryName(%q) path = %q, want %s", img, path, img)
		}
	}
}
