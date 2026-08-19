// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tui

import "testing"

func TestStylesDisabledRenderIsExact(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	if got := NewStyles(false).Render(RoleSuccess, "ready"); got != "ready" {
		t.Fatalf("Render() = %q, want plain text", got)
	}
}

func TestStylesRespectEnvironment(t *testing.T) {
	const text = "\x00ready\n\xff"

	for _, tc := range []struct {
		name    string
		noColor string
		term    string
	}{
		{name: "no color", noColor: "1", term: "xterm-256color"},
		{name: "dumb", term: "dumb"},
		{name: "missing term"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tc.noColor)
			t.Setenv("TERM", tc.term)

			styles := NewStyles(true)
			if styles.Enabled() {
				t.Fatal("styles unexpectedly enabled")
			}
			if got := styles.Render(RoleHeading, text); got != text {
				t.Fatalf("Render() = %q, want byte-exact %q", got, text)
			}
		})
	}
}

func TestStylesRenderEveryRole(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	styles := NewStyles(true)
	for _, tc := range []struct {
		name string
		role Role
		want string
	}{
		{name: "accent", role: RoleAccent, want: "\x1b[33mtext\x1b[m"},
		{name: "success", role: RoleSuccess, want: "\x1b[32mtext\x1b[m"},
		{name: "warning", role: RoleWarning, want: "\x1b[33mtext\x1b[m"},
		{name: "error", role: RoleError, want: "\x1b[31mtext\x1b[m"},
		{name: "muted", role: RoleMuted, want: "\x1b[90mtext\x1b[m"},
		{name: "heading", role: RoleHeading, want: "\x1b[1;36mtext\x1b[m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := styles.Render(tc.role, "text"); got != tc.want {
				t.Fatalf("Render(%v, %q) = %q, want %q", tc.role, "text", got, tc.want)
			}
		})
	}
}

func TestStylesRenderUnknownRoleIsExact(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	if got := NewStyles(true).Render(Role(99), "plain"); got != "plain" {
		t.Fatalf("Render() = %q, want plain text", got)
	}
}
