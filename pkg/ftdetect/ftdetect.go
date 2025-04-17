// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ftdetect

import (
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type FileType int

const (
	Unknown FileType = iota
	Binary
	DockerCompose
	TypeScript
	Script
	Zstd
	Python
)

type file struct {
	f      *os.File
	path   string
	goos   string
	goarch string
}

func DetectFile(path, goos, goarch string) (FileType, error) {
	f, err := newFile(path)
	if err != nil {
		return Unknown, err
	}
	f.goarch = goarch
	f.goos = goos

	return f.detect()
}

func newFile(path string) (*file, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %v", err)
	}
	return &file{f: f, path: path}, nil
}

func (f *file) Close() error {
	return f.f.Close()
}

func (f *file) detect() (FileType, error) {
	// Binary file
	if is, err := f.detectBinary(); err != nil {
		return Unknown, fmt.Errorf("failed to detect binary: %w", err)
	} else if is {
		log.Printf("Detected binary file")
		if same, err := f.isSameArch(); err != nil {
			log.Printf("Failed to check architecture: %v", err)
			return Unknown, fmt.Errorf("failed to check architecture: %w", err)
		} else if !same {
			log.Printf("Architecture mismatch")
			return Unknown, fmt.Errorf("architecture mismatch")
		}
		return Binary, nil
	}
	// Zstd file
	if is, err := f.detectZstd(); err != nil {
		return Unknown, fmt.Errorf("failed to detect zstd: %w", err)
	} else if is {
		return Zstd, nil
	}
	if ft, ok := f.detectByName(); ok {
		return ft, nil
	}
	if is, err := f.detectDockerCompose(); err != nil {
		return Unknown, fmt.Errorf("failed to detect Docker Compose: %w", err)
	} else if is {
		return DockerCompose, nil
	}
	if is, err := f.detectScript(); err != nil {
		return Unknown, fmt.Errorf("failed to detect script: %w", err)
	} else if is {
		return Script, nil
	}
	return Unknown, fmt.Errorf("unable to detect file type")
}

func (f *file) detectByName() (FileType, bool) {
	if f.path == "" {
		return Unknown, false
	}

	base := strings.ToLower(filepath.Base(f.path))
	ext := strings.ToLower(filepath.Ext(base))

	switch ext {
	case ".py":
		return Python, true
	case ".ts":
		return TypeScript, true
	case ".yml", ".yaml":
		return DockerCompose, true
	}

	if base == "compose.yml" || base == "compose.yaml" {
		return DockerCompose, true
	}

	return Unknown, false
}

// detectDockerCompose checks for a top-level services key in a YAML file.
func (f *file) detectDockerCompose() (bool, error) {
	if err := f.checkAndSeek0(); err != nil {
		return false, fmt.Errorf("failed to seek to start of file: %w", err)
	}
	bs, err := io.ReadAll(f.f)
	if err != nil {
		return false, fmt.Errorf("failed to read file: %v", err)
	}
	servicesForm := struct {
		Services map[string]any `yaml:"services"`
	}{}
	if err := yaml.Unmarshal(bs, &servicesForm); err != nil {
		return false, nil
	}
	if len(servicesForm.Services) == 0 {
		return false, nil
	}
	return true, nil
}

func (f *file) checkAndSeek0() error {
	if f.f == nil {
		return fmt.Errorf("file is nil")
	}
	if _, err := f.f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("failed to seek to start of file: %w", err)
	}
	return nil
}

func (f *file) detectBinary() (bool, error) {
	if err := f.checkAndSeek0(); err != nil {
		return false, err
	}
	var magic [4]byte
	if _, err := io.ReadFull(f.f, magic[:]); err != nil {
		return false, fmt.Errorf("failed to read file: %w", err)
	}
	mn := binary.LittleEndian.Uint32(magic[:])
	switch mn {
	case 0x464C457F: // ELF magic number (0x7f 'E' 'L' 'F')
		if f.goos == "darwin" {
			return false, fmt.Errorf("non-darwin (ELF) binary on darwin system")
		}
		return true, nil
	case macho.Magic32, macho.Magic64, macho.MagicFat: // Mach-O magic numbers
		if f.goos != "darwin" {
			return false, fmt.Errorf("darwin binary (Mach-O) on non-darwin system")
		}
		return true, nil
	}
	return false, nil
}

func (f *file) detectZstd() (bool, error) {
	if err := f.checkAndSeek0(); err != nil {
		return false, fmt.Errorf("failed to seek to start of file: %w", err)
	}
	bs, err := io.ReadAll(f.f)
	if err != nil {
		return false, fmt.Errorf("failed to read file: %v", err)
	}
	if len(bs) < 4 || bs[0] != 0x28 || bs[1] != 0xb5 || bs[2] != 0x2f || bs[3] != 0xfd {
		return false, nil
	}
	return true, nil
}

func (f *file) isSameArch() (bool, error) {
	binArch, err := f.detectArchitectureElf()
	if err != nil {
		return false, fmt.Errorf("failed to detect architecture: %w", err)
	}
	hostArch := f.hostArchitecture()
	if binArch == hostArch {
		return true, nil
	}
	return false, fmt.Errorf("binary architecture %s does not match host architecture %s", binArch, hostArch)
}

func (f *file) detectArchitectureElf() (string, error) {
	if f.f == nil {
		return "", fmt.Errorf("file is nil")
	}
	elfFile, err := elf.NewFile(f.f)
	if err != nil {
		return "", fmt.Errorf("failed to parse ELF file: %v", err)
	}

	switch elfFile.Machine {
	case elf.EM_X86_64:
		return "x86_64", nil
	case elf.EM_386:
		return "x86", nil
	case elf.EM_ARM:
		return "ARM", nil
	case elf.EM_AARCH64:
		return "ARM64", nil
	default:
		return "unknown", nil
	}
}

func (f *file) hostArchitecture() string {
	switch f.goarch {
	case "amd64":
		return "x86_64"
	case "386":
		return "x86"
	case "arm":
		return "ARM"
	case "arm64":
		return "ARM64"
	default:
		return "unknown"
	}
}

// detectScript verifies that the given file is a script by checking for a
// shebang at the start of the file.
func (f *file) detectScript() (bool, error) {
	if err := f.checkAndSeek0(); err != nil {
		return false, fmt.Errorf("failed to seek to start of file: %w", err)
	}

	var bs [2]byte
	n, err := io.ReadFull(f.f, bs[:])
	if err != nil {
		return false, fmt.Errorf("failed to read file: %v", err)
	}

	// Check for shebang
	if n < 2 || bs[0] != '#' || bs[1] != '!' {
		return false, nil
	}
	return true, nil
}
