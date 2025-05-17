// Copyright 2025 AUTHORS
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ftdetect

import (
	"bufio"
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
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
	return &file{f: f}, nil
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
	if is, err := f.detectScript(); err != nil {
		return Unknown, fmt.Errorf("failed to detect script: %w", err)
	} else if is {
		return Script, nil
	}
	// Docker Compose file
	if is, err := f.detectDockerCompose(); err != nil {
		return Unknown, fmt.Errorf("failed to detect Docker Compose: %w", err)
	} else if is {
		return DockerCompose, nil
	}
	// TypeScript file - checking before Python to prevent false positives
	if is, err := f.detectTypeScript(); err != nil {
		return Unknown, fmt.Errorf("failed to detect TypeScript: %w", err)
	} else if is {
		return TypeScript, nil
	}
	if is, err := f.detectPython(); err != nil {
		return Unknown, fmt.Errorf("failed to detect Python: %w", err)
	} else if is {
		return Python, nil
	}
	return Unknown, fmt.Errorf("unable to detect file type")
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

// detectDockerCompose verifies that the given file is a valid Docker Compose by
// unmarshalling it and checking for errors. It only checks for the presence of
// the top-level `services` key.
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

	err = yaml.Unmarshal(bs, &servicesForm)
	if err != nil {
		return false, nil // not a Docker Compose file
	}

	return true, nil
}

func (f *file) detectTypeScript() (bool, error) {
	if err := f.checkAndSeek0(); err != nil {
		return false, fmt.Errorf("failed to seek to start of file: %w", err)
	}

	// First do a quick check for common TypeScript patterns
	scanner := bufio.NewScanner(f.f)
	tsPatterns := 0
	lineCount := 0

	for scanner.Scan() && lineCount < 20 {
		line := scanner.Text()
		lineCount++

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Check for TS-specific patterns
		if strings.Contains(line, ": ") && (strings.Contains(line, "string") ||
			strings.Contains(line, "number") || strings.Contains(line, "boolean") ||
			strings.Contains(line, "any") || strings.Contains(line, "void")) {
			tsPatterns += 2
		} else if strings.Contains(line, "interface ") || strings.Contains(line, "namespace ") {
			tsPatterns += 2
		} else if strings.Contains(line, "as ") && (strings.Contains(line, "string") ||
			strings.Contains(line, "number") || strings.Contains(line, "boolean")) {
			tsPatterns++
		} else if strings.HasPrefix(trimmed, "import ") &&
			(strings.Contains(line, "from ") || strings.Contains(line, "{ ")) {
			tsPatterns++
		} else if strings.Contains(line, "export ") {
			tsPatterns++
		}
	}

	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("error scanning file: %w", err)
	}

	// If we have enough TypeScript patterns, return true immediately
	if tsPatterns >= 2 {
		return true, nil
	}

	// If quick check was inconclusive, use the full TypeScript parser
	if err := f.checkAndSeek0(); err != nil {
		return false, fmt.Errorf("failed to seek to start of file: %w", err)
	}

	bs, err := io.ReadAll(f.f)
	if err != nil {
		return false, fmt.Errorf("failed to read file: %v", err)
	}

	// Try to parse as TypeScript
	result := api.Transform(string(bs), api.TransformOptions{
		Loader: api.LoaderTS,
	})

	if len(result.Errors) > 0 {
		return false, nil
	}

	return true, nil
}

func (f *file) detectPython() (bool, error) {
	if err := f.checkAndSeek0(); err != nil {
		return false, fmt.Errorf("failed to seek to start of file: %w", err)
	}

	scanner := bufio.NewScanner(f.f)

	inScriptBlock := false
	pythonPatterns := 0
	lineCount := 0

	for scanner.Scan() && lineCount < 20 {
		line := scanner.Text()
		lineCount++

		// Check for Python script header
		if strings.TrimSpace(line) == "# /// script" {
			inScriptBlock = true
		} else if strings.TrimSpace(line) == "# ///" && inScriptBlock {
			return true, nil
		}

		// Skip empty lines and potential TypeScript-style comments
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}

		// Check for distinctive Python patterns
		if strings.HasPrefix(trimmed, "import ") && !strings.Contains(line, "from") {
			pythonPatterns++
		} else if strings.HasPrefix(trimmed, "from ") && strings.Contains(line, " import ") {
			// This is a strong Python indicator
			pythonPatterns += 2
		} else if strings.HasPrefix(trimmed, "def ") && strings.Contains(line, ":") {
			// Function definitions with colon are distinctive to Python
			pythonPatterns += 2
		} else if strings.HasPrefix(trimmed, "class ") && strings.Contains(line, ":") {
			// Class definitions with colon are distinctive to Python
			pythonPatterns += 2
		} else if trimmed[0] == '#' {
			// Pure Python-style comments (not inside a string)
			pythonPatterns++
		} else if strings.Contains(line, "elif ") || strings.Contains(line, " and ") ||
			strings.Contains(line, " or ") || strings.Contains(line, " is ") ||
			strings.Contains(line, " not ") || strings.Contains(line, " pass ") {
			// Python-specific keywords
			pythonPatterns++
		}
	}

	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("error scanning file: %w", err)
	}

	// Need multiple Python patterns to be confident
	return pythonPatterns >= 2, nil
}
