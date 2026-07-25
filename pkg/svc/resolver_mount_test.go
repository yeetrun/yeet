// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package svc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyReadOnlyResolverMount(t *testing.T) {
	tmp := t.TempDir()
	source := filepath.Join(tmp, "source")
	target := filepath.Join(tmp, "target")
	if err := os.WriteFile(source, []byte("nameserver 100.100.100.100\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, target); err != nil {
		t.Fatal(err)
	}

	probe := ResolverMountProbe{
		SourcePath:    source,
		TargetPath:    target,
		MountInfoPath: "/proc/self/mountinfo",
		MountPoint:    "/etc/resolv.conf",
	}
	healthyMountInfo := "37 26 0:31 / /etc/resolv.conf ro,relatime - ext4 /dev/root rw\n"

	tests := []struct {
		name      string
		probe     ResolverMountProbe
		deps      resolverMountProbeDeps
		wantError string
	}{
		{
			name:  "healthy hard-linked read-only resolver",
			probe: probe,
			deps: resolverMountProbeDeps{
				stat: os.Stat,
				open: mountInfoOpener(healthyMountInfo),
			},
		},
		{
			name:  "healthy resolver covered by read-only etc overlay",
			probe: probe,
			deps: resolverMountProbeDeps{
				stat: os.Stat,
				open: mountInfoOpener("37 26 0:31 / / rw,relatime - ext4 /dev/root rw\n38 37 0:44 / /etc ro,relatime - overlay overlay ro\n"),
			},
		},
		{
			name:  "mountinfo close failure",
			probe: probe,
			deps: resolverMountProbeDeps{
				stat: os.Stat,
				open: func(string) (io.ReadCloser, error) {
					return &resolverMountErrorReadCloser{
						Reader: strings.NewReader(healthyMountInfo),
						err:    errors.New("close failed"),
					}, nil
				},
			},
			wantError: "close mountinfo /proc/self/mountinfo: close failed",
		},
		{
			name:  "missing source",
			probe: withResolverProbe(probe, func(p *ResolverMountProbe) { p.SourcePath = "/missing/source" }),
			deps: resolverMountProbeDeps{
				stat: func(path string) (os.FileInfo, error) {
					if path == "/missing/source" {
						return nil, errors.New("source missing")
					}
					return os.Stat(path)
				},
				open: mountInfoOpener(healthyMountInfo),
			},
			wantError: "stat resolver source /missing/source: source missing",
		},
		{
			name:  "non-regular source",
			probe: withResolverProbe(probe, func(p *ResolverMountProbe) { p.SourcePath = tmp }),
			deps: resolverMountProbeDeps{
				stat: os.Stat,
				open: mountInfoOpener(healthyMountInfo),
			},
			wantError: "resolver source " + tmp + " is not a regular file",
		},
		{
			name: "different target inode",
			probe: withResolverProbe(probe, func(p *ResolverMountProbe) {
				p.TargetPath = filepath.Join(tmp, "different-target")
			}),
			deps: resolverMountProbeDeps{
				stat: func(path string) (os.FileInfo, error) {
					if path == filepath.Join(tmp, "different-target") {
						return os.Stat(filepath.Join(tmp, "different"))
					}
					return os.Stat(path)
				},
				open: mountInfoOpener(healthyMountInfo),
			},
			wantError: "resolver source and target do not refer to the same file",
		},
		{
			name:  "absent mount point",
			probe: probe,
			deps: resolverMountProbeDeps{
				stat: os.Stat,
				open: mountInfoOpener("37 26 0:31 / /etc/hosts ro - ext4 /dev/root rw\n"),
			},
			wantError: "resolver mount point /etc/resolv.conf is absent from mountinfo",
		},
		{
			name:  "read-write top mount",
			probe: probe,
			deps: resolverMountProbeDeps{
				stat: os.Stat,
				open: mountInfoOpener("37 26 0:31 / /etc/resolv.conf ro - ext4 /dev/root rw\n38 26 0:31 / /etc/resolv.conf rw,relatime - ext4 /dev/root rw\n"),
			},
			wantError: "resolver mount point /etc/resolv.conf is not read-only",
		},
		{
			name:  "malformed mount ID",
			probe: probe,
			deps: resolverMountProbeDeps{
				stat: os.Stat,
				open: mountInfoOpener("not-an-id 26 0:31 / /etc/resolv.conf ro - ext4 /dev/root rw\n"),
			},
			wantError: "mountinfo record 1: invalid mount ID \"not-an-id\"",
		},
		{
			name:  "invalid mount path escape",
			probe: probe,
			deps: resolverMountProbeDeps{
				stat: os.Stat,
				open: mountInfoOpener("37 26 0:31 / /etc/resolv\\xyz ro - ext4 /dev/root rw\n"),
			},
			wantError: fmt.Sprintf("decode mount point %q: invalid mountinfo escape %q", "/etc/resolv\\xyz", "\\xyz"),
		},
	}

	if err := os.WriteFile(filepath.Join(tmp, "different"), []byte("different"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifyReadOnlyResolverMount(tt.probe, tt.deps)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("verifyReadOnlyResolverMount() error = %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantError {
				t.Fatalf("verifyReadOnlyResolverMount() error = %v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestVisibleResolverMountUsesHighestMountID(t *testing.T) {
	entry, err := visibleResolverMount(strings.NewReader("37 26 0:31 / /etc/resolv.conf ro - ext4 /dev/root rw\n38 26 0:31 / /etc/resolv.conf rw,relatime - ext4 /dev/root rw\n"), "/etc/resolv.conf")
	if err != nil {
		t.Fatalf("visibleResolverMount() error = %v", err)
	}
	if entry.ID != 38 || entry.MountPoint != "/etc/resolv.conf" || entry.ReadOnly {
		t.Fatalf("visibleResolverMount() = %#v, want highest read-write entry", entry)
	}
}

func TestVisibleResolverMountUsesDeepestCoveringMount(t *testing.T) {
	entry, err := visibleResolverMount(strings.NewReader(
		"37 26 0:31 / / ro - ext4 /dev/root ro\n"+
			"38 37 0:44 / /etc rw,relatime - overlay overlay rw\n",
	), "/etc/resolv.conf")
	if err != nil {
		t.Fatalf("visibleResolverMount() error = %v", err)
	}
	if entry.ID != 38 || entry.MountPoint != "/etc" || entry.ReadOnly {
		t.Fatalf("visibleResolverMount() = %#v, want deepest read-write /etc mount", entry)
	}
}

func TestVisibleResolverMountAllowsOpaqueRootOnUnrelatedMount(t *testing.T) {
	mountInfo := "356 352 0:5 net:[4026532916] /run/netns/yeet-ns rw shared:149 - nsfs nsfs rw\n" +
		"357 352 0:31 / /etc/resolv.conf ro,relatime - ext4 /dev/root rw\n"
	entry, err := visibleResolverMount(strings.NewReader(mountInfo), "/etc/resolv.conf")
	if err != nil {
		t.Fatalf("visibleResolverMount() error = %v", err)
	}
	if entry.ID != 357 || entry.MountPoint != "/etc/resolv.conf" || !entry.ReadOnly {
		t.Fatalf("visibleResolverMount() = %#v, want resolver mount", entry)
	}
}

func TestVisibleResolverMountRejectsMalformedRecord(t *testing.T) {
	tests := []struct {
		name      string
		mountInfo string
		wantError string
	}{
		{
			name:      "truncated record before valid resolver mount",
			mountInfo: "37 26 0:31 / /etc/resolv.conf\n38 26 0:31 / /etc/resolv.conf ro - ext4 /dev/root rw\n",
			wantError: "mountinfo record 1: expected at least 10 fields",
		},
		{
			name:      "missing separator",
			mountInfo: "37 26 0:31 / /etc/resolv.conf ro\n",
			wantError: "mountinfo record 1: expected at least 10 fields",
		},
		{
			name:      "incomplete post-separator fields",
			mountInfo: "37 26 0:31 / /etc/resolv.conf ro - ext4 /dev/root\n",
			wantError: "mountinfo record 1: expected three fields after separator",
		},
		{
			name:      "invalid parent mount ID",
			mountInfo: "37 bad 0:31 / /etc/resolv.conf ro - ext4 /dev/root rw\n",
			wantError: "mountinfo record 1: invalid parent mount ID \"bad\"",
		},
		{
			name:      "invalid major minor device",
			mountInfo: "37 26 bad / /etc/resolv.conf ro - ext4 /dev/root rw\n",
			wantError: "mountinfo record 1: invalid major:minor device \"bad\"",
		},
		{
			name:      "invalid root path escape",
			mountInfo: "37 26 0:31 /bad\\xyz /etc/resolv.conf ro - ext4 /dev/root rw\n",
			wantError: fmt.Sprintf("decode mount root %q: invalid mountinfo escape %q", "/bad\\xyz", "\\xyz"),
		},
		{
			name:      "non-absolute resolver mount root",
			mountInfo: "37 26 0:31 net:[4026532916] /etc/resolv.conf ro - nsfs nsfs rw\n",
			wantError: "mount root \"net:[4026532916]\" must be a clean absolute path",
		},
		{
			name:      "relative mount point",
			mountInfo: "37 26 0:31 / etc/resolv.conf ro - ext4 /dev/root rw\n",
			wantError: "mount point \"etc/resolv.conf\" must be a clean absolute path",
		},
		{
			name:      "invalid per-mount option structure",
			mountInfo: "37 26 0:31 / /etc/resolv.conf ro,,nosuid - ext4 /dev/root rw\n",
			wantError: "mountinfo record 1: invalid per-mount options \"ro,,nosuid\"",
		},
		{
			name:      "per-mount options missing access mode",
			mountInfo: "37 26 0:31 / /etc/resolv.conf nosuid - ext4 /dev/root rw\n",
			wantError: "mountinfo record 1: invalid per-mount options \"nosuid\"",
		},
		{
			name:      "invalid post-separator fields",
			mountInfo: "37 26 0:31 / /etc/resolv.conf ro - - - -\n",
			wantError: "mountinfo record 1: invalid filesystem type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := visibleResolverMount(strings.NewReader(tt.mountInfo), "/etc/resolv.conf")
			if err == nil || err.Error() != tt.wantError {
				t.Fatalf("visibleResolverMount() error = %v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestVisibleResolverMountAcceptsForwardCompatibleOptionalFields(t *testing.T) {
	entry, err := visibleResolverMount(strings.NewReader("37 26 0:31 / /etc/resolv.conf ro shared:1 future:opaque - ext4 /dev/root rw\n"), "/etc/resolv.conf")
	if err != nil {
		t.Fatalf("visibleResolverMount() error = %v", err)
	}
	if entry.ID != 37 || !entry.ReadOnly {
		t.Fatalf("visibleResolverMount() = %#v, want read-only entry", entry)
	}
}

func TestUnescapeMountInfoPath(t *testing.T) {
	tests := []struct {
		raw       string
		want      string
		wantError string
	}{
		{raw: "/a\\040b\\011c\\012d\\134e", want: "/a b\tc\nd\\e"},
		{raw: "/a\\", wantError: fmt.Sprintf("invalid mountinfo escape %q", `\`)},
		{raw: "/a\\xyz", wantError: fmt.Sprintf("invalid mountinfo escape %q", `\xyz`)},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := unescapeMountInfoPath(tt.raw)
			if tt.wantError != "" {
				if err == nil || err.Error() != tt.wantError {
					t.Fatalf("unescapeMountInfoPath() error = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("unescapeMountInfoPath() = %q, %v, want %q, nil", got, err, tt.want)
			}
		})
	}
}

func withResolverProbe(probe ResolverMountProbe, update func(*ResolverMountProbe)) ResolverMountProbe {
	update(&probe)
	return probe
}

func mountInfoOpener(contents string) func(string) (io.ReadCloser, error) {
	return func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(contents)), nil
	}
}

type resolverMountErrorReadCloser struct {
	io.Reader
	err error
}

func (r *resolverMountErrorReadCloser) Close() error {
	return r.err
}
