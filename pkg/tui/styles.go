// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tui

import (
	"os"

	"charm.land/lipgloss/v2"
)

type Role uint8

const (
	RoleAccent Role = iota
	RoleSuccess
	RoleWarning
	RoleError
	RoleMuted
	RoleHeading
)

type Styles struct {
	enabled bool
}

func NewStyles(enabled bool) Styles {
	if !enabled || os.Getenv("NO_COLOR") != "" {
		return Styles{}
	}

	term := os.Getenv("TERM")
	if term == "" || term == "dumb" {
		return Styles{}
	}

	return Styles{enabled: true}
}

func (s Styles) Enabled() bool {
	return s.enabled
}

func (s Styles) Render(role Role, text string) string {
	if !s.enabled {
		return text
	}

	switch role {
	case RoleAccent, RoleWarning:
		return lipgloss.NewStyle().Foreground(lipgloss.Yellow).Render(text)
	case RoleSuccess:
		return lipgloss.NewStyle().Foreground(lipgloss.Green).Render(text)
	case RoleError:
		return lipgloss.NewStyle().Foreground(lipgloss.Red).Render(text)
	case RoleMuted:
		return lipgloss.NewStyle().Foreground(lipgloss.BrightBlack).Render(text)
	case RoleHeading:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Cyan).Render(text)
	default:
		return text
	}
}
