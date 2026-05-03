// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ftdetect

import (
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"errors"
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
	if ft, ok, err := f.detectBinaryType(); err != nil || ok {
		return ft, err
	}

	if ft, ok, err := f.detectZstdType(); err != nil || ok {
		return ft, err
	}

	if ft, ok := f.detectByName(); ok {
		return ft, nil
	}

	if ft, ok, err := f.detectContentType(); err != nil || ok {
		return ft, err
	}

	return Unknown, fmt.Errorf("unable to detect file type")
}

func (f *file) detectBinaryType() (FileType, bool, error) {
	is, err := f.detectBinary()
	if err != nil {
		return Unknown, false, fmt.Errorf("failed to detect binary: %w", err)
	}
	if !is {
		return Unknown, false, nil
	}

	log.Printf("Detected binary file")
	same, err := f.isSameArch()
	if err != nil {
		log.Printf("Failed to check architecture: %v", err)
		return Unknown, false, fmt.Errorf("failed to check architecture: %w", err)
	}
	if !same {
		log.Printf("Architecture mismatch")
		return Unknown, false, fmt.Errorf("architecture mismatch")
	}
	return Binary, true, nil
}

func (f *file) detectZstdType() (FileType, bool, error) {
	is, err := f.detectZstd()
	if err != nil {
		return Unknown, false, fmt.Errorf("failed to detect zstd: %w", err)
	}
	if !is {
		return Unknown, false, nil
	}
	return Zstd, true, nil
}

func (f *file) detectContentType() (FileType, bool, error) {
	contentDetectors := []struct {
		name string
		ft   FileType
		fn   func() (bool, error)
	}{
		{name: "Docker Compose", ft: DockerCompose, fn: f.detectDockerCompose},
		{name: "script", ft: Script, fn: f.detectScript},
	}

	for _, detector := range contentDetectors {
		is, err := detector.fn()
		if err != nil {
			return Unknown, false, fmt.Errorf("failed to detect %s: %w", detector.name, err)
		}
		if is {
			return detector.ft, true, nil
		}
	}

	return Unknown, false, nil
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
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil
		}
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

	return elfMachineArchitecture(elfFile.Machine), nil
}

func elfMachineArchitecture(machine elf.Machine) string {
	switch machine {
	case elf.EM_X86_64:
		return "x86_64"
	case elf.EM_386:
		return "x86"
	case elf.EM_ARM:
		return "ARM"
	case elf.EM_AARCH64:
		return "ARM64"
	default:
		return "unknown"
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
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil
		}
		return false, fmt.Errorf("failed to read file: %v", err)
	}

	// Check for shebang
	if n < 2 || bs[0] != '#' || bs[1] != '!' {
		return false, nil
	}
	return true, nil
}
