// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package catch

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/db"
	"tailscale.com/ipn"
)

func TestNewTailscaleDownloadSelectsTrackAndURL(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		wantTrack string
		wantURL   string
	}{
		{
			name:      "stable",
			version:   "1.92.3",
			wantTrack: "stable",
			wantURL:   "https://pkgs.tailscale.com/stable/tailscale_1.92.3_amd64.tgz",
		},
		{
			name:      "unstable",
			version:   "1.93.0",
			wantTrack: "unstable",
			wantURL:   "https://pkgs.tailscale.com/unstable/tailscale_1.93.0_amd64.tgz",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := newTailscaleDownload(tt.version, "linux", "amd64")
			if err != nil {
				t.Fatalf("newTailscaleDownload returned error: %v", err)
			}
			if got.track != tt.wantTrack {
				t.Fatalf("track = %q, want %q", got.track, tt.wantTrack)
			}
			if got.url != tt.wantURL {
				t.Fatalf("url = %q, want %q", got.url, tt.wantURL)
			}
		})
	}
}

func TestNewTailscaleDownloadRejectsUnsupportedInputs(t *testing.T) {
	if _, err := newTailscaleDownload("not-semver", "linux", "amd64"); err == nil {
		t.Fatal("expected invalid version error")
	}
	_, err := newTailscaleDownload("1.92.3", "darwin", "arm64")
	if err == nil || !strings.Contains(err.Error(), "unsupported OS: darwin") {
		t.Fatalf("error = %v, want unsupported OS", err)
	}
}

func TestExtractTailscaleBinariesWritesExpectedArtifacts(t *testing.T) {
	dstDir := t.TempDir()
	archive := makeTailscaleArchive(t, map[string]string{
		"tailscale_1.92.3_amd64/tailscaled": "daemon",
		"tailscale_1.92.3_amd64/tailscale":  "client",
		"tailscale_1.92.3_amd64/README":     "ignore me",
	})

	if err := extractTailscaleBinaries(bytes.NewReader(archive), dstDir, "1.92.3"); err != nil {
		t.Fatalf("extractTailscaleBinaries returned error: %v", err)
	}

	assertFileContent(t, filepath.Join(dstDir, "tailscaled-1.92.3"), "daemon")
	assertFileContent(t, filepath.Join(dstDir, "tailscale-1.92.3"), "client")
	if _, err := os.Stat(filepath.Join(dstDir, "README-1.92.3")); !os.IsNotExist(err) {
		t.Fatalf("README artifact exists, stat err: %v", err)
	}
	assertExecutable(t, filepath.Join(dstDir, "tailscaled-1.92.3"))
	assertExecutable(t, filepath.Join(dstDir, "tailscale-1.92.3"))
}

func TestExtractTailscaleBinariesRequiresBothArtifacts(t *testing.T) {
	archive := makeTailscaleArchive(t, map[string]string{
		"tailscale_1.92.3_amd64/tailscaled": "daemon",
	})

	err := extractTailscaleBinaries(bytes.NewReader(archive), t.TempDir(), "1.92.3")
	if err == nil || !strings.Contains(err.Error(), "expected 2 binaries, got 1") {
		t.Fatalf("error = %v, want missing binary count", err)
	}
}

func TestDownloadTailscaleArchiveUsesHTTPClientAndClosesBody(t *testing.T) {
	var gotURL string
	closed := false
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			gotURL = req.URL.String()
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: &trackingReadCloser{
					Reader: bytes.NewReader(makeTailscaleArchive(t, map[string]string{
						"tailscaled": "daemon",
						"tailscale":  "client",
					})),
					closed: &closed,
				},
				Header: make(http.Header),
			}, nil
		}),
	}

	dstDir := t.TempDir()
	if err := downloadTailscaleArchive(client, "https://example.test/tailscale.tgz", dstDir, "1.92.3"); err != nil {
		t.Fatalf("downloadTailscaleArchive returned error: %v", err)
	}
	if gotURL != "https://example.test/tailscale.tgz" {
		t.Fatalf("got URL %q", gotURL)
	}
	if !closed {
		t.Fatal("response body was not closed")
	}
	assertFileContent(t, filepath.Join(dstDir, "tailscale-1.92.3"), "client")
}

func TestNewTailscaleSystemdUnitPlansTapAndNetNSModes(t *testing.T) {
	tap := newTailscaleSystemdUnit(tailscaleInstallPlan{
		service:       "demo",
		runDir:        "/srv/demo/run",
		serviceTSDir:  "/srv/demo/tailscale",
		interfaceName: "ts0",
	})
	if got := strings.Join(tap.Arguments, " "); !strings.Contains(got, "--tun=tap:ts0") {
		t.Fatalf("tap args = %q", got)
	}
	if tap.NetNS != "" || tap.Wants != "" || len(tap.ExecStartPre) != 0 {
		t.Fatalf("tap unit has netns fields: %+v", tap)
	}

	netns := newTailscaleSystemdUnit(tailscaleInstallPlan{
		service:       "demo",
		runDir:        "/srv/demo/run",
		serviceTSDir:  "/srv/demo/tailscale",
		runInNetNS:    "yeet-demo-net",
		interfaceName: "ts0",
		resolvConf:    "/srv/demo/bin/resolv.conf",
	})
	if got := strings.Join(netns.Arguments, " "); !strings.Contains(got, "--tun=ts0") || strings.Contains(got, "--tun=tap:") {
		t.Fatalf("netns args = %q", got)
	}
	if netns.Wants != "yeet-demo-net.service" || netns.After != "yeet-demo-net.service" {
		t.Fatalf("netns deps = wants %q after %q", netns.Wants, netns.After)
	}
	if netns.NetNS != "yeet-demo-net" || netns.ResolvConf != "/srv/demo/bin/resolv.conf" {
		t.Fatalf("netns fields = netns %q resolv %q", netns.NetNS, netns.ResolvConf)
	}
	if len(netns.ExecStartPre) != 1 || netns.ExecStartPre[0] != "/bin/systemctl is-active --quiet yeet-demo-net.service" {
		t.Fatalf("ExecStartPre = %#v", netns.ExecStartPre)
	}
}

func TestInstallTSWritesArtifactsWithoutNetworkWhenAuthKeyProvided(t *testing.T) {
	server := newTestServer(t)
	const (
		service = "demo"
		version = "1.92.3"
		authKey = "tskey-auth-test"
	)
	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatalf("mkdir tsd dir: %v", err)
	}
	tsdPath := filepath.Join(tsdDir, "tailscaled-"+version)
	if err := os.WriteFile(tsdPath, []byte("daemon"), 0o755); err != nil {
		t.Fatalf("write tailscaled: %v", err)
	}
	if err := os.MkdirAll(server.serviceBinDir(service), 0o755); err != nil {
		t.Fatalf("mkdir service bin dir: %v", err)
	}

	artifacts, err := server.installTS(service, "yeet-demo-net", &db.TailscaleNetwork{
		Interface: "ts0",
		Version:   version,
		ExitNode:  "exit.example",
	}, authKey, "/srv/demo/resolv.conf")
	if err != nil {
		t.Fatalf("installTS returned error: %v", err)
	}

	if artifacts[db.ArtifactTSBinary] != tsdPath {
		t.Fatalf("ArtifactTSBinary = %q, want %q", artifacts[db.ArtifactTSBinary], tsdPath)
	}
	if _, ok := artifacts[db.ArtifactSystemdUnit]; ok {
		t.Fatalf("unexpected ArtifactSystemdUnit: %#v", artifacts)
	}
	for _, name := range []db.ArtifactName{db.ArtifactTSConfig, db.ArtifactTSService, db.ArtifactTSEnv} {
		if artifacts[name] == "" {
			t.Fatalf("missing artifact %s in %#v", name, artifacts)
		}
	}

	rawCfg, err := os.ReadFile(artifacts[db.ArtifactTSConfig])
	if err != nil {
		t.Fatalf("read tailscaled config: %v", err)
	}
	var cfg ipn.ConfigVAlpha
	if err := json.Unmarshal(rawCfg, &cfg); err != nil {
		t.Fatalf("unmarshal tailscaled config: %v", err)
	}
	if cfg.Hostname == nil || *cfg.Hostname != service {
		t.Fatalf("Hostname = %#v, want %q", cfg.Hostname, service)
	}
	if cfg.AuthKey == nil || *cfg.AuthKey != authKey {
		t.Fatalf("AuthKey = %#v, want %q", cfg.AuthKey, authKey)
	}
	if cfg.ExitNode == nil || *cfg.ExitNode != "exit.example" {
		t.Fatalf("ExitNode = %#v, want exit.example", cfg.ExitNode)
	}

	unitRaw, err := os.ReadFile(artifacts[db.ArtifactTSService])
	if err != nil {
		t.Fatalf("read tailscale service: %v", err)
	}
	unit := string(unitRaw)
	for _, want := range []string{
		"--tun=ts0",
		"NetworkNamespacePath=/var/run/netns/yeet-demo-net",
		"BindPaths=/srv/demo/resolv.conf:/etc/resolv.conf",
		"ExecStartPre=/bin/systemctl is-active --quiet yeet-demo-net.service",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
}

func TestTailscaleBinaryGettersUseExistingFiles(t *testing.T) {
	server := newTestServer(t)
	tsdDir := filepath.Join(server.cfg.RootDir, "tsd")
	if err := os.MkdirAll(tsdDir, 0o755); err != nil {
		t.Fatalf("mkdir tsd: %v", err)
	}
	for _, name := range []string{"tailscale-1.92.3", "tailscaled-1.92.3"} {
		if err := os.WriteFile(filepath.Join(tsdDir, name), []byte(name), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	ts, err := server.getTailscaleBinary("1.92.3")
	if err != nil {
		t.Fatalf("getTailscaleBinary: %v", err)
	}
	if ts != filepath.Join(tsdDir, "tailscale-1.92.3") {
		t.Fatalf("tailscale path = %q", ts)
	}
	tsd, err := server.getTailscaledBinary("1.92.3")
	if err != nil {
		t.Fatalf("getTailscaledBinary: %v", err)
	}
	if tsd != filepath.Join(tsdDir, "tailscaled-1.92.3") {
		t.Fatalf("tailscaled path = %q", tsd)
	}
}

func TestDownloadTailscaleArchiveReturnsHTTPClientError(t *testing.T) {
	wantErr := errors.New("dial failed")
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, wantErr
		}),
	}
	err := downloadTailscaleArchive(client, "https://example.test/tailscale.tgz", t.TempDir(), "1.92.3")
	if !errors.Is(err, wantErr) {
		t.Fatalf("download error = %v, want %v", err, wantErr)
	}
}

func TestExtractTailscaleBinaryIgnoresNonBinaryEntries(t *testing.T) {
	err := extractTailscaleBinary(&tar.Header{Name: "README.md"}, strings.NewReader("readme"), t.TempDir(), "1.92.3")
	if err != nil {
		t.Fatalf("extract non-binary: %v", err)
	}
}

func TestExtractOauthID(t *testing.T) {
	id, ok := extractOauthID("tskey-client-abc123-secret")
	if !ok || id != "abc123" {
		t.Fatalf("extractOauthID = %q, %v", id, ok)
	}
	if id, ok := extractOauthID("tskey-auth-abc123"); ok || id != "" {
		t.Fatalf("invalid extractOauthID = %q, %v", id, ok)
	}
}

func TestTSClientRejectsInvalidSecret(t *testing.T) {
	dir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})
	if err := os.WriteFile("tailscale.key", []byte("not-an-oauth-secret"), 0o600); err != nil {
		t.Fatalf("write tailscale.key: %v", err)
	}

	if _, err := tsClient(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid tailscale oauth secret") {
		t.Fatalf("tsClient error = %v", err)
	}
}

func TestResolveTailscaleAuthKeyUsesProvidedKey(t *testing.T) {
	server := newTestServer(t)
	key, err := server.resolveTailscaleAuthKey(&db.TailscaleNetwork{Tags: []string{"tag:svc"}}, "tskey-auth-provided")
	if err != nil {
		t.Fatalf("resolveTailscaleAuthKey: %v", err)
	}
	if key != "tskey-auth-provided" {
		t.Fatalf("auth key = %q", key)
	}
}

func TestWriteTailscaleConfigWithoutExitNode(t *testing.T) {
	path, err := writeTailscaleConfig(t.TempDir(), "svc", "tskey-auth-test", "")
	if err != nil {
		t.Fatalf("writeTailscaleConfig: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg ipn.ConfigVAlpha
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if cfg.ExitNode != nil {
		t.Fatalf("ExitNode = %#v, want nil", cfg.ExitNode)
	}
	if cfg.Hostname == nil || *cfg.Hostname != "svc" {
		t.Fatalf("Hostname = %#v", cfg.Hostname)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type trackingReadCloser struct {
	io.Reader
	closed *bool
}

func (r *trackingReadCloser) Close() error {
	*r.closed = true
	return nil
}

func makeTailscaleArchive(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		b := []byte(content)
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o755,
			Size: int64(len(b)),
		}); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tw.Write(b); err != nil {
			t.Fatalf("write tar content: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func assertExecutable(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("%s mode = %v, want 0755", path, info.Mode().Perm())
	}
}
