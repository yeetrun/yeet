// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/template"

	"github.com/shayne/yeet/pkg/db"
)

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

	unitName := translateMountPathToUnitName(m.v.Path)

	svcContent := bytes.NewBuffer(nil)
	if err := systemdMountTemplate.Execute(svcContent, m.v); err != nil {
		return fmt.Errorf("failed to execute template: %v", err)
	}

	svcPath := "/etc/systemd/system/" + unitName + ".mount"
	if err := os.WriteFile(svcPath, svcContent.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write service file: %v", err)
	}

	automountContent := bytes.NewBuffer(nil)
	if err := systemdAutomountTemplate.Execute(automountContent, m.v); err != nil {
		return fmt.Errorf("failed to execute template: %v", err)
	}

	svcPath = "/etc/systemd/system/" + unitName + ".automount"
	if err := os.WriteFile(svcPath, automountContent.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write automount file: %v", err)
	}

	// Reload systemd
	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("failed to reload systemd: %v", err)
	}

	// Enable and start the mount
	if err := exec.Command("systemctl", "enable", "--now", unitName+".automount").Run(); err != nil {
		return fmt.Errorf("failed to enable and start service: %v", err)
	}

	return nil
}

func (m *systemdMounter) umount() error {
	unitName := translateMountPathToUnitName(m.v.Path)
	if _, err := exec.Command("systemctl", "is-enabled", "--quiet", unitName+".automount").CombinedOutput(); err == nil {
		if err := exec.Command("systemctl", "disable", "--now", unitName+".automount").Run(); err != nil {
			return fmt.Errorf("failed to disable and stop service: %v", err)
		}
	}
	if _, err := exec.Command("systemctl", "is-active", "--quiet", unitName+".mount").CombinedOutput(); err == nil {
		if err := exec.Command("systemctl", "stop", unitName+".mount").Run(); err != nil {
			return fmt.Errorf("failed to stop service: %v", err)
		}
	}

	if err := os.Remove("/etc/systemd/system/" + unitName + ".mount"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove service file: %v", err)
	}
	if err := os.Remove("/etc/systemd/system/" + unitName + ".automount"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove automount file: %v", err)
	}
	if err := os.Remove(m.v.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove mount directory: %v", err)
	}

	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
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
			switch {
			case c >= 'a' && c <= 'z',
				c >= 'A' && c <= 'Z',
				c >= '0' && c <= '9',
				c == '.', c == '_', c == ':':
				sb.WriteRune(c)
			default:
				fmt.Fprintf(&sb, "\\x%x", c)
			}
		}
		count++
	}
	return sb.String()
}
