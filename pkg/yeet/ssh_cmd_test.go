// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import "testing"

func TestParseSSHArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		options []string
		service string
		command []string
		wantErr bool
	}{
		{name: "empty"},
		{
			name:    "options and service",
			args:    []string{"-i", "id_rsa", "svc"},
			options: []string{"-i", "id_rsa"},
			service: "svc",
		},
		{
			name:    "service with command",
			args:    []string{"svc", "--", "ls", "-la"},
			options: []string{},
			service: "svc",
			command: []string{"ls", "-la"},
		},
		{
			name:    "host command only",
			args:    []string{"--", "uname", "-a"},
			options: []string{},
			service: "",
			command: []string{"uname", "-a"},
		},
		{
			name:    "reject extra args",
			args:    []string{"svc", "ls"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			options, service, command, err := parseSSHArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(options) != len(tc.options) {
				t.Fatalf("options=%v, want %v", options, tc.options)
			}
			for i := range options {
				if options[i] != tc.options[i] {
					t.Fatalf("options=%v, want %v", options, tc.options)
				}
			}
			if service != tc.service {
				t.Fatalf("service=%q, want %q", service, tc.service)
			}
			if len(command) != len(tc.command) {
				t.Fatalf("command=%v, want %v", command, tc.command)
			}
			for i := range command {
				if command[i] != tc.command[i] {
					t.Fatalf("command=%v, want %v", command, tc.command)
				}
			}
		})
	}
}

func TestEnsureTTYOption(t *testing.T) {
	options := ensureTTYOption(nil)
	if len(options) != 1 || options[0] != "-t" {
		t.Fatalf("options=%v, want [-t]", options)
	}

	options = ensureTTYOption([]string{"-i", "id_rsa"})
	if len(options) != 3 || options[2] != "-t" {
		t.Fatalf("options=%v, want [-i id_rsa -t]", options)
	}

	options = ensureTTYOption([]string{"-t"})
	if len(options) != 1 || options[0] != "-t" {
		t.Fatalf("options=%v, want [-t]", options)
	}

	options = ensureTTYOption([]string{"-T"})
	if len(options) != 1 || options[0] != "-T" {
		t.Fatalf("options=%v, want [-T]", options)
	}
}

func TestShellQuoteJoin(t *testing.T) {
	if got := shellQuote("simple"); got != "simple" {
		t.Fatalf("shellQuote(simple)=%q", got)
	}
	if got := shellQuote("a b"); got != "'a b'" {
		t.Fatalf("shellQuote(space)=%q", got)
	}
	if got := shellQuote("can't"); got != "'can'\"'\"'t'" {
		t.Fatalf("shellQuote(quote)=%q", got)
	}
	if got := shellJoin([]string{"echo", "a b"}); got != "echo 'a b'" {
		t.Fatalf("shellJoin=%q", got)
	}
}
