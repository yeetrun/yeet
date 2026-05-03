// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
	"github.com/yeetrun/yeet/pkg/db"
)

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestConfirmTSUpdateReturnsPromptWriteError(t *testing.T) {
	writeErr := errors.New("write failed")
	ok, err := confirmTSUpdate(readWriter{
		Reader: strings.NewReader("y\n"),
		Writer: failingWriter{err: writeErr},
	}, "1.90.0", "1.92.0")
	if err == nil {
		t.Fatal("expected prompt write error")
	}
	if ok {
		t.Fatal("confirmation should be false on prompt write error")
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected wrapped write error, got %v", err)
	}
}

func TestMountCmdListReturnsFlushError(t *testing.T) {
	server := newTestServer(t)
	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		d.Volumes = map[string]*db.Volume{
			"data": {
				Name: "data",
				Src:  "host:/srv/data",
				Path: "/mnt/data",
				Type: "sshfs",
				Opts: "ro",
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed volume: %v", err)
	}

	writeErr := errors.New("write failed")
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		rw: readWriter{
			Reader: strings.NewReader(""),
			Writer: failingWriter{err: writeErr},
		},
	}

	err := execer.mountCmdFunc(cli.MountFlags{}, nil)
	if err == nil {
		t.Fatal("expected mount listing write error")
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected wrapped write error, got %v", err)
	}
}

func TestMountCmdCreatePersistsVolumeAndRunsMount(t *testing.T) {
	server := newTestServer(t)

	oldCheckMountCommand := checkMountCommand
	oldMountVolume := mountVolume
	defer func() {
		checkMountCommand = oldCheckMountCommand
		mountVolume = oldMountVolume
	}()

	var checkedType string
	checkMountCommand = func(mountType string) error {
		checkedType = mountType
		return nil
	}
	var mounted db.Volume
	mountVolume = func(vol db.Volume) error {
		mounted = vol
		return nil
	}

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		rw:  &out,
	}

	flags := cli.MountFlags{
		Type: "sshfs",
		Opts: "ro",
		Deps: []string{"network-online.target"},
	}
	if err := execer.mountCmdFunc(flags, []string{"host:/srv/data", "data"}); err != nil {
		t.Fatalf("mountCmdFunc: %v", err)
	}

	if checkedType != "sshfs" {
		t.Fatalf("checked mount type = %q, want sshfs", checkedType)
	}
	wantPath := server.cfg.MountsRoot + "/data"
	if mounted.Name != "data" || mounted.Src != "host:/srv/data" || mounted.Path != wantPath || mounted.Type != "sshfs" || mounted.Opts != "ro" || mounted.Deps != "network-online.target" {
		t.Fatalf("mounted volume = %+v", mounted)
	}
	if got := out.String(); !strings.Contains(got, "Mounted host:/srv/data at "+wantPath) {
		t.Fatalf("mount output = %q", got)
	}

	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("get db: %v", err)
	}
	vol, ok := dv.Volumes().GetOk("data")
	if !ok {
		t.Fatal("expected persisted data volume")
	}
	if got := vol.Path(); got != wantPath {
		t.Fatalf("persisted volume path = %q, want %q", got, wantPath)
	}
}

func TestUmountNameFromArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "one mount name", args: []string{"data"}, want: "data"},
		{name: "missing mount name", args: nil, wantErr: true},
		{name: "too many args", args: []string{"data", "extra"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := umountNameFromArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("mount name = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUmountCmdFuncRunsUnmountAndDeletesVolume(t *testing.T) {
	server := newTestServer(t)
	seedTTYOpsVolumes(t, server)

	oldUnmountVolume := unmountVolume
	defer func() { unmountVolume = oldUnmountVolume }()
	var gotVolume db.Volume
	unmountVolume = func(_ *ttyExecer, vol db.Volume) error {
		gotVolume = vol
		return nil
	}

	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		rw:  &bytes.Buffer{},
	}
	if err := execer.umountCmdFunc([]string{"data"}); err != nil {
		t.Fatalf("umountCmdFunc: %v", err)
	}
	if gotVolume.Name != "data" || gotVolume.Path != "/mnt/data" {
		t.Fatalf("unmounted volume = %+v", gotVolume)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("get db: %v", err)
	}
	if dv.Volumes().Contains("data") {
		t.Fatal("expected data volume to be removed")
	}
	if !dv.Volumes().Contains("logs") {
		t.Fatal("expected unrelated logs volume to remain")
	}
}

func TestUmountCmdFuncKeepsVolumeWhenUnmountFails(t *testing.T) {
	server := newTestServer(t)
	seedTTYOpsVolumes(t, server)

	oldUnmountVolume := unmountVolume
	defer func() { unmountVolume = oldUnmountVolume }()
	unmountErr := errors.New("busy")
	unmountVolume = func(_ *ttyExecer, vol db.Volume) error {
		if vol.Name != "data" {
			t.Fatalf("unmounted volume name = %q, want data", vol.Name)
		}
		return unmountErr
	}

	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		rw:  &bytes.Buffer{},
	}
	err := execer.umountCmdFunc([]string{"data"})
	if err == nil {
		t.Fatal("expected unmount error")
	}
	if !errors.Is(err, unmountErr) {
		t.Fatalf("expected wrapped unmount error, got %v", err)
	}
	dv, err := server.cfg.DB.Get()
	if err != nil {
		t.Fatalf("get db: %v", err)
	}
	if !dv.Volumes().Contains("data") {
		t.Fatal("expected data volume to remain")
	}
}

func TestIPCmdFuncUsesSystemArgsAndPrintsIPs(t *testing.T) {
	oldListIPv4Addrs := listIPv4AddrsFn
	defer func() { listIPv4AddrsFn = oldListIPv4Addrs }()
	var gotArgs []string
	listIPv4AddrsFn = func(args []string) ([]ifaceIP, error) {
		gotArgs = append([]string(nil), args...)
		return []ifaceIP{
			{Interface: "eth0", IP: "10.0.0.5"},
			{Interface: "tailscale0", IP: "100.64.0.2"},
		}, nil
	}

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   newTestServer(t),
		sn:  SystemService,
		rw:  &out,
	}
	if err := execer.ipCmdFunc(); err != nil {
		t.Fatalf("ipCmdFunc: %v", err)
	}

	wantArgs := []string{"-o", "-4", "addr", "list"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("ip args = %#v, want %#v", gotArgs, wantArgs)
	}
	if got := out.String(); got != "10.0.0.5\n100.64.0.2\n" {
		t.Fatalf("ip output = %q", got)
	}
}

func TestIPCmdFuncUsesNetNSArgsForServiceArtifact(t *testing.T) {
	server := newTestServer(t)
	if _, _, err := server.cfg.DB.MutateService("svc-ip", func(_ *db.Data, s *db.Service) error {
		s.ServiceType = db.ServiceTypeSystemd
		s.Generation = 3
		s.LatestGeneration = 3
		s.Artifacts = db.ArtifactStore{
			db.ArtifactNetNSService: {
				Refs: map[db.ArtifactRef]string{
					db.Gen(3): "/tmp/netns.service",
				},
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed service: %v", err)
	}

	oldListIPv4Addrs := listIPv4AddrsFn
	defer func() { listIPv4AddrsFn = oldListIPv4Addrs }()
	var gotArgs []string
	listIPv4AddrsFn = func(args []string) ([]ifaceIP, error) {
		gotArgs = append([]string(nil), args...)
		return []ifaceIP{{Interface: "eth0", IP: "10.0.0.8"}}, nil
	}

	var out bytes.Buffer
	execer := &ttyExecer{
		ctx: context.Background(),
		s:   server,
		sn:  "svc-ip",
		rw:  &out,
	}
	if err := execer.ipCmdFunc(); err != nil {
		t.Fatalf("ipCmdFunc: %v", err)
	}

	wantArgs := []string{"netns", "exec", "yeet-svc-ip-ns", "ip", "-o", "-4", "addr", "list"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("ip args = %#v, want %#v", gotArgs, wantArgs)
	}
	if got := out.String(); got != "10.0.0.8\n" {
		t.Fatalf("ip output = %q", got)
	}
}

func TestNormalizeTailscaleTrack(t *testing.T) {
	tests := []struct {
		name    string
		track   string
		want    string
		wantErr bool
	}{
		{name: "stable", track: " stable ", want: "stable"},
		{name: "unstable", track: "unstable", want: "unstable"},
		{name: "invalid", track: "latest", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeTailscaleTrack(tt.track)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("track = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTailscaleTrackVersionFromMeta(t *testing.T) {
	tests := []struct {
		name    string
		meta    tailscaleTrackMeta
		want    string
		wantErr bool
	}{
		{name: "valid", meta: tailscaleTrackMeta{TarballsVersion: " 1.94.2 "}, want: "1.94.2"},
		{name: "empty", meta: tailscaleTrackMeta{}, wantErr: true},
		{name: "invalid", meta: tailscaleTrackMeta{TarballsVersion: "not-semver"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tailscaleTrackVersionFromMeta(tt.meta)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("version = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTailscaleLatestVersionForTrackUsesLookupURL(t *testing.T) {
	var gotTrack string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/meta" {
			t.Fatalf("path = %q, want /meta", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"TarballsVersion":"1.94.2"}`))
	}))
	defer ts.Close()

	oldURL := tailscaleTrackMetaURL
	oldClient := tailscaleTrackHTTPClient
	defer func() {
		tailscaleTrackMetaURL = oldURL
		tailscaleTrackHTTPClient = oldClient
	}()
	tailscaleTrackMetaURL = func(track string) string {
		gotTrack = track
		return ts.URL + "/meta"
	}
	tailscaleTrackHTTPClient = ts.Client()

	got, err := tailscaleLatestVersionForTrack(" stable ")
	if err != nil {
		t.Fatalf("tailscaleLatestVersionForTrack: %v", err)
	}
	if got != "1.94.2" {
		t.Fatalf("version = %q, want 1.94.2", got)
	}
	if gotTrack != "stable" {
		t.Fatalf("lookup track = %q, want stable", gotTrack)
	}
}

func TestTailscaleLatestVersionForTrackRejectsHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	oldURL := tailscaleTrackMetaURL
	oldClient := tailscaleTrackHTTPClient
	defer func() {
		tailscaleTrackMetaURL = oldURL
		tailscaleTrackHTTPClient = oldClient
	}()
	tailscaleTrackMetaURL = func(track string) string {
		return ts.URL
	}
	tailscaleTrackHTTPClient = ts.Client()

	if _, err := tailscaleLatestVersionForTrack("unstable"); err == nil {
		t.Fatal("expected HTTP status error")
	}
}

func seedTTYOpsVolumes(t *testing.T, server *Server) {
	t.Helper()
	if _, err := server.cfg.DB.MutateData(func(d *db.Data) error {
		d.Volumes = map[string]*db.Volume{
			"data": {
				Name: "data",
				Src:  "host:/srv/data",
				Path: "/mnt/data",
				Type: "sshfs",
				Opts: "ro",
			},
			"logs": {
				Name: "logs",
				Src:  "host:/srv/logs",
				Path: "/mnt/logs",
				Type: "sshfs",
				Opts: "ro",
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed volumes: %v", err)
	}
}
