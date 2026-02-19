// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
)

func TestTsCmdUpdateUsesYeetManagedUpdater(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires shell scripts")
	}

	server := newTestServer(t)
	const (
		svcName    = "svc-ts-update"
		oldVersion = "1.90.0"
		newVersion = "1.94.2"
	)

	runDir := server.serviceRunDir(svcName)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "tailscaled"), []byte("old-daemon"), 0o755); err != nil {
		t.Fatalf("write existing tailscaled: %v", err)
	}

	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatalf("mkdir tsd dir: %v", err)
	}
	newDaemon := filepath.Join(tsdDir, "tailscaled-"+newVersion)
	if err := os.WriteFile(newDaemon, []byte("new-daemon"), 0o755); err != nil {
		t.Fatalf("write new tailscaled: %v", err)
	}
	newClient := filepath.Join(tsdDir, "tailscale-"+newVersion)
	if err := os.WriteFile(newClient, []byte("new-client"), 0o755); err != nil {
		t.Fatalf("write new tailscale: %v", err)
	}

	if _, _, err := server.cfg.DB.MutateService(svcName, func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeDockerCompose
		s.Generation = 1
		s.LatestGeneration = 1
		s.TSNet = &db.TailscaleNetwork{Interface: "yts-test", Version: oldVersion}
		s.Artifacts = db.ArtifactStore{
			db.ArtifactTSBinary: {
				Refs: map[db.ArtifactRef]string{
					db.ArtifactRef("latest"): filepath.Join(tsdDir, "tailscaled-"+oldVersion),
					db.Gen(s.Generation):     filepath.Join(tsdDir, "tailscaled-"+oldVersion),
				},
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed service: %v", err)
	}

	fakeBin := t.TempDir()
	systemctlLog := filepath.Join(fakeBin, "systemctl.log")
	systemctlScript := filepath.Join(fakeBin, "systemctl")
	script := "#!/bin/sh\nprintf '%s\n' \"$*\" >> \"$SYSTEMCTL_LOG\"\n"
	if err := os.WriteFile(systemctlScript, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
	}
	t.Setenv("SYSTEMCTL_LOG", systemctlLog)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	origLatest := tailscaleLatestVersionForTrackFn
	defer func() { tailscaleLatestVersionForTrackFn = origLatest }()
	var gotTrack string
	tailscaleLatestVersionForTrackFn = func(track string) (string, error) {
		gotTrack = track
		return newVersion, nil
	}

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  svcName,
		rw:  readWriter{Reader: strings.NewReader("y\n"), Writer: &out},
	}

	if err := execer.tsCmdFunc([]string{"update"}); err != nil {
		t.Fatalf("tsCmdFunc(update): %v", err)
	}

	if gotTrack != "stable" {
		t.Fatalf("track = %q, want stable", gotTrack)
	}
	if got := out.String(); !strings.Contains(got, "yeet-managed") {
		t.Fatalf("expected yeet-managed message, got %q", got)
	}
	if got := out.String(); !strings.Contains(got, "Continue? [y/n]") {
		t.Fatalf("expected confirmation prompt, got %q", got)
	}

	sv, err := server.serviceView(svcName)
	if err != nil {
		t.Fatalf("serviceView: %v", err)
	}
	if got := sv.TSNet().Version(); got != newVersion {
		t.Fatalf("TSNet.Version = %q, want %q", got, newVersion)
	}

	runBinary, err := os.ReadFile(filepath.Join(runDir, "tailscaled"))
	if err != nil {
		t.Fatalf("read run tailscaled: %v", err)
	}
	if got := string(runBinary); got != "new-daemon" {
		t.Fatalf("run tailscaled = %q, want %q", got, "new-daemon")
	}

	systemctlCalls, err := os.ReadFile(systemctlLog)
	if err != nil {
		t.Fatalf("read systemctl log: %v", err)
	}
	wantCall := fmt.Sprintf("restart yeet-%s-ts.service", svcName)
	if got := strings.TrimSpace(string(systemctlCalls)); got != wantCall {
		t.Fatalf("systemctl call = %q, want %q", got, wantCall)
	}
}

func TestTsCmdUpdatePassthroughWithDoubleDash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires shell scripts")
	}

	server := newTestServer(t)
	const (
		svcName = "svc-ts-raw-update"
		version = "1.90.0"
	)

	runDir := server.serviceRunDir(svcName)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	sock := filepath.Join(runDir, "tailscaled.sock")
	if err := os.WriteFile(sock, []byte(""), 0o644); err != nil {
		t.Fatalf("write socket placeholder: %v", err)
	}

	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatalf("mkdir tsd dir: %v", err)
	}
	argsLog := filepath.Join(tsdDir, "tailscale-args.log")
	clientBin := filepath.Join(tsdDir, "tailscale-"+version)
	clientScript := "#!/bin/sh\nprintf '%s\n' \"$@\" > \"$TAILSCALE_ARGS_LOG\"\n"
	if err := os.WriteFile(clientBin, []byte(clientScript), 0o755); err != nil {
		t.Fatalf("write fake tailscale client: %v", err)
	}
	t.Setenv("TAILSCALE_ARGS_LOG", argsLog)

	if _, _, err := server.cfg.DB.MutateService(svcName, func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeDockerCompose
		s.Generation = 1
		s.LatestGeneration = 1
		s.TSNet = &db.TailscaleNetwork{Interface: "yts-test", Version: version}
		return nil
	}); err != nil {
		t.Fatalf("seed service: %v", err)
	}

	origLatest := tailscaleLatestVersionForTrackFn
	defer func() { tailscaleLatestVersionForTrackFn = origLatest }()
	tailscaleLatestVersionForTrackFn = func(track string) (string, error) {
		t.Fatalf("latest version resolver should not be called in passthrough mode")
		return "", nil
	}

	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  svcName,
		rw:  readWriter{Reader: strings.NewReader(""), Writer: &bytes.Buffer{}},
	}

	if err := execer.tsCmdFunc([]string{"--", "update"}); err != nil {
		t.Fatalf("tsCmdFunc(-- update): %v", err)
	}

	b, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 args, got %v", lines)
	}
	if got := lines[0]; got != "--socket="+sock {
		t.Fatalf("arg[0] = %q, want %q", got, "--socket="+sock)
	}
	if got := lines[1]; got != "update" {
		t.Fatalf("arg[1] = %q, want update", got)
	}
}

func TestTsCmdUpdatePinnedVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires shell scripts")
	}

	server := newTestServer(t)
	const (
		svcName       = "svc-ts-pinned-update"
		currentVer    = "1.90.0"
		pinnedVersion = "1.95.112"
	)

	runDir := server.serviceRunDir(svcName)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "tailscaled"), []byte("old-daemon"), 0o755); err != nil {
		t.Fatalf("write existing tailscaled: %v", err)
	}

	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatalf("mkdir tsd dir: %v", err)
	}
	newDaemon := filepath.Join(tsdDir, "tailscaled-"+pinnedVersion)
	if err := os.WriteFile(newDaemon, []byte("pinned-daemon"), 0o755); err != nil {
		t.Fatalf("write new tailscaled: %v", err)
	}
	newClient := filepath.Join(tsdDir, "tailscale-"+pinnedVersion)
	if err := os.WriteFile(newClient, []byte("pinned-client"), 0o755); err != nil {
		t.Fatalf("write new tailscale: %v", err)
	}

	if _, _, err := server.cfg.DB.MutateService(svcName, func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeDockerCompose
		s.Generation = 1
		s.LatestGeneration = 1
		s.TSNet = &db.TailscaleNetwork{Interface: "yts-test", Version: currentVer}
		return nil
	}); err != nil {
		t.Fatalf("seed service: %v", err)
	}

	fakeBin := t.TempDir()
	systemctlLog := filepath.Join(fakeBin, "systemctl.log")
	systemctlScript := filepath.Join(fakeBin, "systemctl")
	script := "#!/bin/sh\nprintf '%s\n' \"$*\" >> \"$SYSTEMCTL_LOG\"\n"
	if err := os.WriteFile(systemctlScript, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
	}
	t.Setenv("SYSTEMCTL_LOG", systemctlLog)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	origLatest := tailscaleLatestVersionForTrackFn
	defer func() { tailscaleLatestVersionForTrackFn = origLatest }()
	tailscaleLatestVersionForTrackFn = func(track string) (string, error) {
		t.Fatalf("latest resolver should not be called for pinned update")
		return "", nil
	}

	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  svcName,
		rw:  readWriter{Reader: strings.NewReader("y\n"), Writer: &bytes.Buffer{}},
	}
	if err := execer.tsCmdFunc([]string{"update", pinnedVersion}); err != nil {
		t.Fatalf("tsCmdFunc(update <version>): %v", err)
	}

	sv, err := server.serviceView(svcName)
	if err != nil {
		t.Fatalf("serviceView: %v", err)
	}
	if got := sv.TSNet().Version(); got != pinnedVersion {
		t.Fatalf("TSNet.Version = %q, want %q", got, pinnedVersion)
	}
}

func TestTsCmdUpdateCanceledByUser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires shell scripts")
	}

	server := newTestServer(t)
	const (
		svcName    = "svc-ts-cancel-update"
		oldVersion = "1.90.0"
		newVersion = "1.95.112"
	)
	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatalf("mkdir tsd dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tsdDir, "tailscaled-"+newVersion), []byte("new-daemon"), 0o755); err != nil {
		t.Fatalf("write new tailscaled: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tsdDir, "tailscale-"+newVersion), []byte("new-client"), 0o755); err != nil {
		t.Fatalf("write new tailscale: %v", err)
	}

	if _, _, err := server.cfg.DB.MutateService(svcName, func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeDockerCompose
		s.Generation = 1
		s.LatestGeneration = 1
		s.TSNet = &db.TailscaleNetwork{Interface: "yts-test", Version: oldVersion}
		return nil
	}); err != nil {
		t.Fatalf("seed service: %v", err)
	}

	origLatest := tailscaleLatestVersionForTrackFn
	defer func() { tailscaleLatestVersionForTrackFn = origLatest }()
	tailscaleLatestVersionForTrackFn = func(track string) (string, error) {
		return newVersion, nil
	}

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  svcName,
		rw:  readWriter{Reader: strings.NewReader("n\n"), Writer: &out},
	}
	if err := execer.tsCmdFunc([]string{"update"}); err != nil {
		t.Fatalf("tsCmdFunc(update): %v", err)
	}

	if got := out.String(); !strings.Contains(got, "Continue? [y/n]") {
		t.Fatalf("expected confirmation prompt, got %q", got)
	}
	if got := out.String(); !strings.Contains(got, "Update canceled.") {
		t.Fatalf("expected cancellation message, got %q", got)
	}
	sv, err := server.serviceView(svcName)
	if err != nil {
		t.Fatalf("serviceView: %v", err)
	}
	if got := sv.TSNet().Version(); got != oldVersion {
		t.Fatalf("TSNet.Version = %q, want %q", got, oldVersion)
	}
}

func TestParseTSUpdateTarget(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantTarget string
		wantPinned bool
		wantErr    bool
	}{
		{name: "latest default", args: nil, wantTarget: "", wantPinned: false, wantErr: false},
		{name: "positional pinned", args: []string{"1.95.112"}, wantTarget: "1.95.112", wantPinned: true, wantErr: false},
		{name: "version equals flag", args: []string{"--version=1.95.112"}, wantTarget: "1.95.112", wantPinned: true, wantErr: false},
		{name: "version split flag", args: []string{"--version", "1.95.112"}, wantTarget: "1.95.112", wantPinned: true, wantErr: false},
		{name: "invalid long flag", args: []string{"--check"}, wantErr: true},
		{name: "too many args", args: []string{"1.95.112", "extra"}, wantErr: true},
		{name: "invalid version", args: []string{"not-a-version"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTarget, gotPinned, err := parseTSUpdateTarget(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotTarget != tt.wantTarget {
				t.Fatalf("target = %q, want %q", gotTarget, tt.wantTarget)
			}
			if gotPinned != tt.wantPinned {
				t.Fatalf("pinned = %v, want %v", gotPinned, tt.wantPinned)
			}
		})
	}
}
