// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ftdetect

import (
	"debug/elf"
	"debug/macho"
	"encoding/binary"
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

func TestDetectFileMissingFile(t *testing.T) {
	t.Parallel()

	ft, err := DetectFile(filepath.Join(t.TempDir(), "missing"), runtime.GOOS, runtime.GOARCH)
	if err == nil {
		t.Fatalf("expected missing file error, got nil (type %v)", ft)
	}
	if ft != Unknown {
		t.Fatalf("expected Unknown type, got %v", ft)
	}
}

func TestDetectFileZstdMagic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "archive")
	if err := os.WriteFile(path, []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00}, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ft, err := DetectFile(path, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("DetectFile error: %v", err)
	}
	if ft != Zstd {
		t.Fatalf("expected Zstd type, got %v", ft)
	}
}

func TestDetectFileDockerComposeByContent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "stack")
	if err := os.WriteFile(path, []byte("services:\n  app:\n    image: busybox\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ft, err := DetectFile(path, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("DetectFile error: %v", err)
	}
	if ft != DockerCompose {
		t.Fatalf("expected DockerCompose type, got %v", ft)
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

func TestDetectBinaryRejectsWrongOSMagic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	elfPath := filepath.Join(dir, "elf")
	if err := os.WriteFile(elfPath, []byte{0x7f, 'E', 'L', 'F'}, 0o644); err != nil {
		t.Fatalf("write ELF magic: %v", err)
	}
	elfFile, err := newFile(elfPath)
	if err != nil {
		t.Fatalf("newFile ELF: %v", err)
	}
	defer elfFile.Close()
	elfFile.goos = "darwin"
	if _, _, err := elfFile.detectBinaryType(); err == nil {
		t.Fatal("detectBinaryType accepted ELF on darwin")
	}

	machoPath := filepath.Join(dir, "macho")
	var magic [4]byte
	binary.LittleEndian.PutUint32(magic[:], macho.Magic64)
	if err := os.WriteFile(machoPath, magic[:], 0o644); err != nil {
		t.Fatalf("write Mach-O magic: %v", err)
	}
	machoFile, err := newFile(machoPath)
	if err != nil {
		t.Fatalf("newFile Mach-O: %v", err)
	}
	defer machoFile.Close()
	machoFile.goos = "linux"
	if _, _, err := machoFile.detectBinaryType(); err == nil {
		t.Fatal("detectBinaryType accepted Mach-O on non-darwin")
	}
}

func TestDetectBinaryReportsInvalidELFArchitecture(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "elf")
	if err := os.WriteFile(path, []byte{0x7f, 'E', 'L', 'F'}, 0o644); err != nil {
		t.Fatalf("write ELF magic: %v", err)
	}
	f, err := newFile(path)
	if err != nil {
		t.Fatalf("newFile: %v", err)
	}
	defer f.Close()
	f.goos = "linux"
	f.goarch = runtime.GOARCH

	if _, _, err := f.detectBinaryType(); err == nil {
		t.Fatal("detectBinaryType succeeded for truncated ELF")
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

func TestHostArchitecture(t *testing.T) {
	tests := []struct {
		goarch string
		want   string
	}{
		{goarch: "amd64", want: "x86_64"},
		{goarch: "386", want: "x86"},
		{goarch: "arm", want: "ARM"},
		{goarch: "arm64", want: "ARM64"},
		{goarch: "mips", want: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.goarch, func(t *testing.T) {
			if got := (&file{goarch: tt.goarch}).hostArchitecture(); got != tt.want {
				t.Fatalf("hostArchitecture(%q) = %q, want %q", tt.goarch, got, tt.want)
			}
		})
	}
}

func TestFileHelperErrorBranches(t *testing.T) {
	t.Parallel()

	if ft, ok := (&file{}).detectByName(); ok || ft != Unknown {
		t.Fatalf("empty path detectByName = %v, %v; want Unknown, false", ft, ok)
	}
	if err := (&file{}).checkAndSeek0(); err == nil {
		t.Fatal("checkAndSeek0 succeeded with nil file")
	}
	if _, err := (&file{}).detectDockerCompose(); err == nil {
		t.Fatal("detectDockerCompose succeeded with nil file")
	}
	if _, err := (&file{}).detectZstd(); err == nil {
		t.Fatal("detectZstd succeeded with nil file")
	}
	if _, err := (&file{}).detectScript(); err == nil {
		t.Fatal("detectScript succeeded with nil file")
	}
	if _, err := (&file{}).detectArchitectureElf(); err == nil {
		t.Fatal("detectArchitectureElf succeeded with nil file")
	}
}
