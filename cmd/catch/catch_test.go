// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectInstallUserFromEnv(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		currentUser string
		want        string
	}{
		{
			name: "explicit install user wins",
			env: map[string]string{
				"CATCH_INSTALL_USER": "catch-user",
				"SUDO_USER":          "sudo-user",
				"USER":               "env-user",
			},
			currentUser: "current-user",
			want:        "catch-user",
		},
		{
			name: "sudo user before user",
			env: map[string]string{
				"SUDO_USER": "sudo-user",
				"USER":      "env-user",
			},
			currentUser: "current-user",
			want:        "sudo-user",
		},
		{
			name:        "current user fallback",
			env:         map[string]string{},
			currentUser: "current-user",
			want:        "current-user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectInstallUserFromEnv(func(key string) string {
				return tt.env[key]
			}, func() (string, error) {
				return tt.currentUser, nil
			})
			if got != tt.want {
				t.Fatalf("detectInstallUserFromEnv() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectInstallUserFromEnvCurrentUserError(t *testing.T) {
	got := detectInstallUserFromEnv(func(string) string { return "" }, func() (string, error) {
		return "", errors.New("boom")
	})
	if got != "" {
		t.Fatalf("detectInstallUserFromEnv() = %q, want empty string", got)
	}
}

func TestVerifyContainerdSnapshotterConfig(t *testing.T) {
	if err := verifyContainerdSnapshotterConfig([]byte(`{"features":{"containerd-snapshotter":true}}`), "daemon.json"); err != nil {
		t.Fatalf("verifyContainerdSnapshotterConfig returned error: %v", err)
	}

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "invalid json", raw: `{`, want: "failed to parse"},
		{name: "missing features", raw: `{}`, want: "missing features.containerd-snapshotter=true"},
		{name: "disabled snapshotter", raw: `{"features":{"containerd-snapshotter":false}}`, want: "must set features.containerd-snapshotter=true"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifyContainerdSnapshotterConfig([]byte(tt.raw), "daemon.json")
			if err == nil {
				t.Fatalf("verifyContainerdSnapshotterConfig succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("verifyContainerdSnapshotterConfig error = %q, want substring %q", err, tt.want)
			}
		})
	}
}

func TestHandleSpecialCommand(t *testing.T) {
	var out strings.Builder
	handled, err := handleSpecialCommand([]string{"is-catch"}, &out)
	if err != nil {
		t.Fatalf("handleSpecialCommand returned error: %v", err)
	}
	if !handled {
		t.Fatalf("handleSpecialCommand did not handle is-catch")
	}
	if got := strings.TrimSpace(out.String()); got != "yes" {
		t.Fatalf("handleSpecialCommand output = %q, want yes", got)
	}

	out.Reset()
	handled, err = handleSpecialCommand(nil, &out)
	if err != nil {
		t.Fatalf("handleSpecialCommand returned error for no args: %v", err)
	}
	if handled {
		t.Fatalf("handleSpecialCommand handled no args")
	}
	if out.Len() != 0 {
		t.Fatalf("handleSpecialCommand wrote output for no args: %q", out.String())
	}
}

func TestListenDockerPluginSocketRemovesStaleSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "yeet-sock-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	sock := filepath.Join(dir, "plugins", "yeet.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sock, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	ln, err := listenDockerPluginSocket(sock)
	if err != nil {
		t.Fatalf("listenDockerPluginSocket: %v", err)
	}
	defer logClose("test unix listener", ln)
	defer logRemove(sock)

	if got := ln.Addr().String(); got != sock {
		t.Fatalf("listener addr = %q, want %q", got, sock)
	}
}
