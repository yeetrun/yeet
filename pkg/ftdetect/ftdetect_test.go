// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ftdetect

import (
	"debug/elf"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDetectFileByExtension(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		fileName string
		contents string
		want     FileType
	}{
		{
			name:     "compose_yml",
			fileName: "compose.yml",
			contents: "services:\n  app:\n    image: busybox\n",
			want:     DockerCompose,
		},
		{
			name:     "compose_yaml_other_name",
			fileName: "stack.yaml",
			contents: "def hello():\n  pass\n",
			want:     DockerCompose,
		},
		{
			name:     "python_by_ext",
			fileName: "main.py",
			contents: "export const x: number = 1;\n",
			want:     Python,
		},
		{
			name:     "typescript_by_ext",
			fileName: "main.ts",
			contents: "def hello():\n  pass\n",
			want:     TypeScript,
		},
		{
			name:     "script_shebang",
			fileName: "run",
			contents: "#!/usr/bin/env bash\necho hi\n",
			want:     Script,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			path := filepath.Join(dir, tc.fileName)

			if err := os.WriteFile(path, []byte(tc.contents), 0o644); err != nil {
				t.Fatalf("write file: %v", err)
			}

			ft, err := DetectFile(path, runtime.GOOS, runtime.GOARCH)
			if err != nil {
				t.Fatalf("DetectFile error: %v", err)
			}
			if ft != tc.want {
				t.Fatalf("DetectFile type mismatch: got %v want %v", ft, tc.want)
			}
		})
	}
}

func TestDetectFileUnknown(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "readme.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ft, err := DetectFile(path, runtime.GOOS, runtime.GOARCH)
	if err == nil {
		t.Fatalf("expected error, got nil (type %v)", ft)
	}
	if ft != Unknown {
		t.Fatalf("expected Unknown type, got %v", ft)
	}
}

func TestDetectFileShortScript(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "run")
	if err := os.WriteFile(path, []byte("#!"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ft, err := DetectFile(path, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("DetectFile error: %v", err)
	}
	if ft != Script {
		t.Fatalf("expected Script type, got %v", ft)
	}
}

func TestDetectFileShortUnknown(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "readme")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ft, err := DetectFile(path, runtime.GOOS, runtime.GOARCH)
	if err == nil {
		t.Fatalf("expected error, got nil (type %v)", ft)
	}
	if ft != Unknown {
		t.Fatalf("expected Unknown type, got %v", ft)
	}
	if strings.Contains(err.Error(), "failed to detect binary") {
		t.Fatalf("expected detection miss, got low-level binary error: %v", err)
	}
}

func TestELFMachineArchitecture(t *testing.T) {
	tests := []struct {
		name    string
		machine elf.Machine
		want    string
	}{
		{name: "amd64", machine: elf.EM_X86_64, want: "x86_64"},
		{name: "386", machine: elf.EM_386, want: "x86"},
		{name: "arm", machine: elf.EM_ARM, want: "ARM"},
		{name: "arm64", machine: elf.EM_AARCH64, want: "ARM64"},
		{name: "unknown", machine: elf.EM_NONE, want: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := elfMachineArchitecture(tt.machine); got != tt.want {
				t.Fatalf("elfMachineArchitecture(%v) = %q, want %q", tt.machine, got, tt.want)
			}
		})
	}
}
