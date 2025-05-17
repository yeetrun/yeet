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
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"bufio"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/pelletier/go-toml/v2"
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
	if is, err := f.detectPython(); err != nil {
		return Unknown, fmt.Errorf("failed to detect Python: %w", err)
	} else if is {
		return Python, nil
	}
	// TypeScript file
	if is, err := f.detectTypeScript(); err != nil {
		return Unknown, fmt.Errorf("failed to detect TypeScript: %w", err)
	} else if is {
		return TypeScript, nil
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
	bs, err := io.ReadAll(f.f)
	if err != nil {
		return false, fmt.Errorf("failed to read file: %v", err)
	}
	// TODO: This is pretty heavy-handed in terms of binary size impact.
	result := api.Transform(string(bs), api.TransformOptions{
		Loader: api.LoaderTS,
	})
	if len(result.Errors) > 0 {
		return false, fmt.Errorf("failed to parse TypeScript: %v", result.Errors)
	}
	return true, nil
}

func (f *file) detectPython() (bool, error) {
	if err := f.checkAndSeek0(); err != nil {
		return false, fmt.Errorf("failed to seek to start of file: %w", err)
	}

	scanner := bufio.NewScanner(f.f)
	
	inScriptBlock := false
	isPython := false
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
		
		// Check for common Python patterns
		if strings.HasPrefix(strings.TrimSpace(line), "import ") || 
		   strings.HasPrefix(strings.TrimSpace(line), "from ") && strings.Contains(line, " import ") ||
		   strings.HasPrefix(strings.TrimSpace(line), "def ") ||
		   strings.HasPrefix(strings.TrimSpace(line), "class ") ||
		   strings.Contains(line, "print(") ||
		   strings.Contains(line, "type ") && strings.Contains(line, "=") {
			isPython = true
		}
		
		// Check for Python comments
		if strings.TrimSpace(line) != "" && strings.TrimSpace(line)[0] == '#' {
			isPython = true
		}
	}
	
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("error scanning file: %w", err)
	}
	
	return isPython, nil
}

type PythonScriptMetadata struct {
	RequiresPython string     `toml:"requires-python"`
	Dependencies   []string   `toml:"dependencies"`
}

// ParsePythonScriptMetadata extracts and parses the metadata from a Python script file
func ParsePythonScriptMetadata(path string) (*PythonScriptMetadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()
	
	scanner := bufio.NewScanner(file)
	
	var metadataLines []string
	inScriptBlock := false
	
	for scanner.Scan() {
		line := scanner.Text()
		
		if strings.TrimSpace(line) == "# /// script" {
			inScriptBlock = true
			continue
		} else if strings.TrimSpace(line) == "# ///" && inScriptBlock {
			break
		} else if inScriptBlock {
			if strings.HasPrefix(line, "# ") {
				metadataLines = append(metadataLines, line[2:])
			}
		}
	}
	
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error scanning file: %w", err)
	}
	
	if len(metadataLines) == 0 {
		return &PythonScriptMetadata{RequiresPython: ">=3.7", Dependencies: []string{}}, nil
	}
	
	metadata := &PythonScriptMetadata{}
	if err := toml.Unmarshal([]byte(strings.Join(metadataLines, "\n")), metadata); err != nil {
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
	}
	
	return metadata, nil
}
