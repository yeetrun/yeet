// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/yeetrun/yeet/pkg/db"
)

var systemdSystemDir = "/etc/systemd/system"

type systemdMounter struct {
	e *ttyExecer
	v db.Volume
}

var (
	systemdMountTemplateStr = `[Unit]
Description=Mount {{ .Name }}
{{ if .Deps }} Requires={{.Deps}} {{end}}
{{ if .Deps }} After={{.Deps}} {{end}}

[Mount]
What={{ .Src }}
Where={{ .Path }}
Type={{ .Type }}
Options={{ .Opts }}

[Install]
WantedBy=multi-user.target
`
	systemdMountTemplate = template.Must(template.New("systemdMountTemplate").Parse(systemdMountTemplateStr))

	systemdAutomountTemplateStr = `[Unit]
Description=Automount {{ .Name }}

[Automount]
Where={{ .Path }}

[Install]
WantedBy=multi-user.target
`
	systemdAutomountTemplate = template.Must(template.New("systemdAutomountTemplate").Parse(systemdAutomountTemplateStr))
)

func (m *systemdMounter) mount() error {
	if err := os.Mkdir(m.v.Path, 0755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("failed to create mount target directory: %v", err)
	}

	files, err := systemdMountFiles(systemdSystemDir, m.v)
	if err != nil {
		return err
	}
	if err := os.WriteFile(files.mountPath, files.mountContent, 0644); err != nil {
		return fmt.Errorf("failed to write service file: %v", err)
	}
	if err := os.WriteFile(files.automountPath, files.automountContent, 0644); err != nil {
		return fmt.Errorf("failed to write automount file: %v", err)
	}

	if err := runSystemdCommand("daemon-reload"); err != nil {
		return fmt.Errorf("failed to reload systemd: %v", err)
	}
	if err := runSystemdCommand("enable", "--now", files.unitName+".automount"); err != nil {
		return fmt.Errorf("failed to enable and start service: %v", err)
	}

	return nil
}

func (m *systemdMounter) umount() error {
	unitName := translateMountPathToUnitName(m.v.Path)
	automountEnabled := systemdUnitEnabled(unitName + ".automount")
	mountActive := systemdUnitActive(unitName + ".mount")
	if err := runSystemdUmountCommands(unitName, automountEnabled, mountActive); err != nil {
		return err
	}

	if err := removeSystemdUnit(filepath.Join(systemdSystemDir, unitName+".mount")); err != nil {
		return fmt.Errorf("failed to remove service file: %v", err)
	}
	if err := removeSystemdUnit(filepath.Join(systemdSystemDir, unitName+".automount")); err != nil {
		return fmt.Errorf("failed to remove automount file: %v", err)
	}
	if err := removeSystemdUnit(m.v.Path); err != nil {
		return fmt.Errorf("failed to remove mount directory: %v", err)
	}

	if err := runSystemdCommand("daemon-reload"); err != nil {
		return fmt.Errorf("failed to reload systemd: %v", err)
	}

	return nil
}

func translateMountPathToUnitName(path string) string {
	var sb strings.Builder
	count := 0
	for _, part := range strings.Split(path, "/") {
		if part == "" {
			continue
		}
		if count > 0 {
			sb.WriteRune('-')
		}
		for _, c := range part {
			if isSystemdUnitSafeRune(c) {
				sb.WriteRune(c)
				continue
			}
			fmt.Fprintf(&sb, "\\x%x", c)
		}
		count++
	}
	return sb.String()
}

func isSystemdUnitSafeRune(c rune) bool {
	return c >= 'a' && c <= 'z' ||
		c >= 'A' && c <= 'Z' ||
		c >= '0' && c <= '9' ||
		c == '.' || c == '_' || c == ':'
}

type systemdMountFileSet struct {
	unitName         string
	mountPath        string
	automountPath    string
	mountContent     []byte
	automountContent []byte
}

func systemdMountFiles(root string, vol db.Volume) (systemdMountFileSet, error) {
	unitName := translateMountPathToUnitName(vol.Path)
	mountContent, err := renderSystemdTemplate(systemdMountTemplate, vol)
	if err != nil {
		return systemdMountFileSet{}, err
	}
	automountContent, err := renderSystemdTemplate(systemdAutomountTemplate, vol)
	if err != nil {
		return systemdMountFileSet{}, err
	}
	return systemdMountFileSet{
		unitName:         unitName,
		mountPath:        filepath.Join(root, unitName+".mount"),
		automountPath:    filepath.Join(root, unitName+".automount"),
		mountContent:     mountContent,
		automountContent: automountContent,
	}, nil
}

func renderSystemdTemplate(t *template.Template, vol db.Volume) ([]byte, error) {
	var content bytes.Buffer
	if err := t.Execute(&content, vol); err != nil {
		return nil, fmt.Errorf("failed to execute template: %v", err)
	}
	return content.Bytes(), nil
}

func systemdUnitEnabled(unit string) bool {
	return systemdQuietStatus("is-enabled", "--quiet", unit)
}

func systemdUnitActive(unit string) bool {
	return systemdQuietStatus("is-active", "--quiet", unit)
}

var systemdQuietStatus = func(args ...string) bool {
	_, err := exec.Command("systemctl", args...).CombinedOutput()
	return err == nil
}

func runSystemdUmountCommands(unitName string, automountEnabled, mountActive bool) error {
	for _, args := range systemdUmountCommands(unitName, automountEnabled, mountActive) {
		if err := runSystemdCommand(args[1:]...); err != nil {
			return systemdUmountCommandError(args, err)
		}
	}
	return nil
}

func systemdUmountCommands(unitName string, automountEnabled, mountActive bool) [][]string {
	commands := make([][]string, 0, 2)
	if automountEnabled {
		commands = append(commands, []string{"systemctl", "disable", "--now", unitName + ".automount"})
	}
	if mountActive {
		commands = append(commands, []string{"systemctl", "stop", unitName + ".mount"})
	}
	return commands
}

func systemdUmountCommandError(args []string, err error) error {
	if len(args) >= 2 && args[1] == "disable" {
		return fmt.Errorf("failed to disable and stop service: %v", err)
	}
	if len(args) >= 2 && args[1] == "stop" {
		return fmt.Errorf("failed to stop service: %v", err)
	}
	return fmt.Errorf("failed to run systemd command: %v", err)
}

var runSystemdCommand = func(args ...string) error {
	return exec.Command("systemctl", args...).Run()
}

func removeSystemdUnit(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
