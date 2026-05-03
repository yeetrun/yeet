// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func FuzzYeetStringNormalizers(f *testing.F) {
	for _, seed := range [][2]string{
		{"", "media@yeet-edge-a"},
		{".", "media"},
		{"./logs/app.txt", "@host"},
		{"data/logs/app.txt", "service@"},
		{"logs/../state/app.db", "svc@host@tail"},
		{"../secret", "svc@@host"},
		{"/etc/passwd", ""},
		{"data/../secret", "svc@host"},
	} {
		f.Add(seed[0], seed[1])
	}

	f.Fuzz(func(t *testing.T, rawPath, serviceValue string) {
		rel, _, err := normalizeRemotePath(rawPath)
		if err == nil {
			if strings.HasPrefix(rel, "/") {
				t.Fatalf("normalized path %q is absolute for raw %q", rel, rawPath)
			}
			if rel == ".." || strings.HasPrefix(rel, "../") {
				t.Fatalf("normalized path %q escapes remote root for raw %q", rel, rawPath)
			}
			if rel != "" && path.Clean(rel) != rel {
				t.Fatalf("normalized path %q is not clean for raw %q", rel, rawPath)
			}
		}

		service, host, ok := splitServiceHost(serviceValue)
		if !ok {
			if service != serviceValue {
				t.Fatalf("service = %q, want original %q when not qualified", service, serviceValue)
			}
			if host != "" {
				t.Fatalf("host = %q, want empty when not qualified", host)
			}
			return
		}
		if service == "" {
			t.Fatalf("service is empty for qualified value %q", serviceValue)
		}
		if host == "" {
			t.Fatalf("host is empty for qualified value %q", serviceValue)
		}
		if got := service + "@" + host; got != serviceValue {
			t.Fatalf("round trip = %q, want %q", got, serviceValue)
		}
	})
}

func TestParseCopyArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    copyRequest
		wantErr string
	}{
		{
			name: "remote destination with bundled flags",
			args: []string{"-azv", "local.txt", "svc:data/logs/"},
			want: copyRequest{
				Recursive: true,
				Archive:   true,
				Compress:  true,
				Verbose:   true,
				Src:       copyEndpoint{Raw: "local.txt", Path: "local.txt"},
				Dst:       copyEndpoint{Raw: "svc:data/logs/", Path: "logs", Service: "svc", Remote: true, DirHint: true},
			},
		},
		{
			name: "double dash keeps dash path operand",
			args: []string{"--", "-", "svc:."},
			want: copyRequest{
				Recursive: true,
				Archive:   true,
				Compress:  true,
				Verbose:   true,
				Src:       copyEndpoint{Raw: "-", Path: "-"},
				Dst:       copyEndpoint{Raw: "svc:.", Path: "", Service: "svc", Remote: true, DirHint: true},
			},
		},
		{name: "unknown long flag", args: []string{"--bogus", "a", "svc:b"}, wantErr: "unknown flag"},
		{name: "unknown short flag", args: []string{"-x", "a", "svc:b"}, wantErr: "unknown flag"},
		{name: "wrong operand count", args: []string{"a"}, wantErr: "exactly two paths"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCopyArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCopyArgs: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseCopyArgs = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestApplyLongCopyFlag(t *testing.T) {
	tests := []struct {
		flag string
		want copyRequest
	}{
		{flag: "--recursive", want: copyRequest{Recursive: true}},
		{flag: "--archive", want: copyRequest{Recursive: true, Archive: true}},
		{flag: "--compress", want: copyRequest{Compress: true}},
		{flag: "--verbose", want: copyRequest{Verbose: true}},
	}

	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			var req copyRequest
			if err := applyLongCopyFlag(&req, tt.flag); err != nil {
				t.Fatalf("applyLongCopyFlag error: %v", err)
			}
			if req != tt.want {
				t.Fatalf("request = %#v, want %#v", req, tt.want)
			}
		})
	}
}

func TestNormalizeRemotePath(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantPath    string
		wantDirHint bool
		wantErr     string
	}{
		{name: "empty path targets data root", raw: "", wantDirHint: true},
		{name: "dot path targets data root", raw: ".", wantDirHint: true},
		{name: "slash suffix records directory hint", raw: "logs/", wantPath: "logs", wantDirHint: true},
		{name: "trims dot slash", raw: "./logs/app.txt", wantPath: "logs/app.txt"},
		{name: "strips data prefix", raw: "data/logs/app.txt", wantPath: "logs/app.txt"},
		{name: "strips repeated slash after data prefix", raw: "data//logs/app.txt", wantPath: "logs/app.txt"},
		{name: "cleans relative path", raw: "logs/../state/app.db", wantPath: "state/app.db"},
		{name: "rejects absolute path", raw: "/etc/passwd", wantErr: "remote path must be relative"},
		{name: "rejects parent escape", raw: "../secret", wantErr: "invalid remote path"},
		{name: "rejects parent escape under data prefix", raw: "data/../secret", wantErr: "invalid remote path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPath, gotDirHint, err := normalizeRemotePath(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeRemotePath: %v", err)
			}
			if gotPath != tt.wantPath {
				t.Fatalf("path = %q, want %q", gotPath, tt.wantPath)
			}
			if gotDirHint != tt.wantDirHint {
				t.Fatalf("dirHint = %v, want %v", gotDirHint, tt.wantDirHint)
			}
		})
	}
}

func TestCopyPathHelpers(t *testing.T) {
	if got := trimRemoteDataPrefix("data"); got != "" {
		t.Fatalf("trimRemoteDataPrefix data = %q, want empty", got)
	}
	if got := trimRemoteDataPrefix("database/file"); got != "database/file" {
		t.Fatalf("trimRemoteDataPrefix database = %q, want unchanged", got)
	}
	if !isWindowsDrivePath(`C:\Users\me\file.txt`) {
		t.Fatal("isWindowsDrivePath backslash = false, want true")
	}
	if !isWindowsDrivePath("C:/Users/me/file.txt") {
		t.Fatal("isWindowsDrivePath slash = false, want true")
	}
	if isWindowsDrivePath("svc:path") {
		t.Fatal("isWindowsDrivePath remote spec = true, want false")
	}
}

func TestClassifyCopyEndpoints(t *testing.T) {
	tests := []struct {
		name          string
		req           copyRequest
		wantDirection copyDirection
		wantRemote    copyEndpoint
		wantErr       string
	}{
		{
			name: "local to remote",
			req: copyRequest{
				Src: copyEndpoint{Raw: "local.txt", Path: "local.txt"},
				Dst: copyEndpoint{Raw: "svc:logs", Path: "logs", Service: "svc", Remote: true},
			},
			wantDirection: copyDirectionToRemote,
			wantRemote:    copyEndpoint{Raw: "svc:logs", Path: "logs", Service: "svc", Remote: true},
		},
		{
			name: "remote to local",
			req: copyRequest{
				Src: copyEndpoint{Raw: "svc:logs", Path: "logs", Service: "svc", Remote: true},
				Dst: copyEndpoint{Raw: "local.txt", Path: "local.txt"},
			},
			wantDirection: copyDirectionFromRemote,
			wantRemote:    copyEndpoint{Raw: "svc:logs", Path: "logs", Service: "svc", Remote: true},
		},
		{
			name: "remote to remote rejected",
			req: copyRequest{
				Src: copyEndpoint{Raw: "src:logs", Path: "logs", Service: "src", Remote: true},
				Dst: copyEndpoint{Raw: "dst:logs", Path: "logs", Service: "dst", Remote: true},
			},
			wantErr: "remote-to-remote",
		},
		{
			name: "local to local rejected",
			req: copyRequest{
				Src: copyEndpoint{Raw: "a", Path: "a"},
				Dst: copyEndpoint{Raw: "b", Path: "b"},
			},
			wantErr: "requires a service endpoint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDirection, gotRemote, err := classifyCopyEndpoints(tt.req)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("classifyCopyEndpoints: %v", err)
			}
			if gotDirection != tt.wantDirection {
				t.Fatalf("direction = %v, want %v", gotDirection, tt.wantDirection)
			}
			if !reflect.DeepEqual(gotRemote, tt.wantRemote) {
				t.Fatalf("remote = %#v, want %#v", gotRemote, tt.wantRemote)
			}
		})
	}
}

func TestApplyCopyHostOverrideForEndpoint(t *testing.T) {
	oldPrefs := loadedPrefs
	oldOverride := hostOverride
	oldOverrideSet := hostOverrideSet
	defer func() {
		loadedPrefs = oldPrefs
		hostOverride = oldOverride
		hostOverrideSet = oldOverrideSet
	}()

	cfg := &ProjectConfig{}
	cfg.SetServiceEntry(ServiceEntry{Name: "svc-a", Host: "configured-host"})

	loadedPrefs.DefaultHost = "default-host"
	resetHostOverride()
	if err := applyCopyHostOverrideForEndpoint(copyEndpoint{Service: "svc-a"}, cfg); err != nil {
		t.Fatalf("apply configured host error: %v", err)
	}
	if Host() != "configured-host" {
		t.Fatalf("Host = %q, want configured-host", Host())
	}

	resetHostOverride()
	if err := applyCopyHostOverrideForEndpoint(copyEndpoint{Service: "svc-a", Host: "remote-host"}, cfg); err != nil {
		t.Fatalf("apply remote host error: %v", err)
	}
	if got, ok := HostOverride(); !ok || got != "remote-host" {
		t.Fatalf("HostOverride = %q %v, want remote-host true", got, ok)
	}

	SetHostOverride("active-host")
	if err := applyCopyHostOverrideForEndpoint(copyEndpoint{Service: "svc-a", Host: "remote-host"}, cfg); err != nil {
		t.Fatalf("apply active host error: %v", err)
	}
	if got, ok := HostOverride(); !ok || got != "active-host" {
		t.Fatalf("active HostOverride = %q %v, want active-host true", got, ok)
	}
}

func TestCopyEndpointValidationHelpers(t *testing.T) {
	if _, err := remoteCopyDestination(copyRequest{Dst: copyEndpoint{Path: "logs"}}); err == nil {
		t.Fatal("remoteCopyDestination local dst error = nil, want error")
	}
	if _, err := localCopySource(copyRequest{}); err == nil {
		t.Fatal("localCopySource empty source error = nil, want error")
	}
	if _, err := remoteCopySource(copyRequest{Src: copyEndpoint{Path: "logs"}}); err == nil {
		t.Fatal("remoteCopySource local src error = nil, want error")
	}
	if _, err := remoteCopySource(copyRequest{Src: copyEndpoint{Remote: true}}); err == nil {
		t.Fatal("remoteCopySource missing service error = nil, want error")
	}
	if _, err := remoteCopySource(copyRequest{Src: copyEndpoint{Remote: true, Service: "svc-a"}}); err == nil {
		t.Fatal("remoteCopySource empty path without dir hint error = nil, want error")
	}
	src, err := remoteCopySource(copyRequest{Src: copyEndpoint{Remote: true, Service: "svc-a", DirHint: true}})
	if err != nil {
		t.Fatalf("remoteCopySource dir hint error: %v", err)
	}
	if src.Service != "svc-a" {
		t.Fatalf("remote source = %#v, want svc-a", src)
	}
}

func TestRemoteCopyCommandArgs(t *testing.T) {
	upload := copyUploadArgs("configs", true, true)
	if want := []string{"copy", "--to", "configs", "--archive", "--compress"}; !reflect.DeepEqual(upload, want) {
		t.Fatalf("copyUploadArgs = %#v, want %#v", upload, want)
	}

	download := copyDownloadArgs(copyRequest{
		Recursive: true,
		Archive:   false,
		Compress:  true,
		Src:       copyEndpoint{Path: "", DirHint: true},
	})
	if want := []string{"copy", "--from", ".", "--compress", "--recursive"}; !reflect.DeepEqual(download, want) {
		t.Fatalf("copyDownloadArgs = %#v, want %#v", download, want)
	}
}

func TestRemoteFileDestinations(t *testing.T) {
	root, entry, err := remoteArchiveFileDestination("configs/app.yml", false, "/tmp/config.yml")
	if err != nil {
		t.Fatalf("remoteArchiveFileDestination: %v", err)
	}
	if root != "configs" || entry != "app.yml" {
		t.Fatalf("archive destination root=%q entry=%q, want configs/app.yml", root, entry)
	}

	plain, err := remotePlainFileDestination("", true, "/tmp/config.yml")
	if err != nil {
		t.Fatalf("remotePlainFileDestination: %v", err)
	}
	if plain != "config.yml" {
		t.Fatalf("plain destination = %q, want config.yml", plain)
	}
}

func TestOpenPlainFileCopyUpload(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "config.yml")
	if err := os.WriteFile(src, []byte("key: value\n"), 0o600); err != nil {
		t.Fatalf("WriteFile source: %v", err)
	}

	upload, err := openPlainFileCopyUpload(copyRequest{
		Compress: true,
		Src:      copyEndpoint{Raw: src, Path: src},
		Dst:      copyEndpoint{Raw: "svc:configs/", Path: "configs", DirHint: true},
	})
	if err != nil {
		t.Fatalf("openPlainFileCopyUpload error: %v", err)
	}
	defer upload.reader.Close()
	if want := []string{"copy", "--to", "configs/config.yml", "--compress"}; !reflect.DeepEqual(upload.args, want) {
		t.Fatalf("upload args = %#v, want %#v", upload.args, want)
	}
	body, err := io.ReadAll(upload.reader)
	if err != nil {
		t.Fatalf("ReadAll upload reader: %v", err)
	}
	if string(body) != "key: value\n" {
		t.Fatalf("upload body = %q, want source contents", string(body))
	}
}

func TestOpenPlainFileCopyUploadRejectsInvalidDestination(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "config.yml")
	if err := os.WriteFile(src, []byte("key: value\n"), 0o600); err != nil {
		t.Fatalf("WriteFile source: %v", err)
	}

	_, err := openPlainFileCopyUpload(copyRequest{
		Src: copyEndpoint{Raw: src, Path: src},
		Dst: copyEndpoint{Raw: "svc:../secret", Path: "../secret"},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid copy destination") {
		t.Fatalf("openPlainFileCopyUpload error = %v, want invalid destination", err)
	}
}

func TestLocalOutputPathHelpers(t *testing.T) {
	if got := localFileOutputPath(localCopyTarget{path: "/tmp/out.txt"}, "base.txt", "/stage/file.txt"); got != "/tmp/out.txt" {
		t.Fatalf("localFileOutputPath file = %q", got)
	}
	if got := localFileOutputPath(localCopyTarget{path: "/tmp/out", dir: true}, "", "/stage/file.txt"); got != filepath.Join("/tmp/out", "file.txt") {
		t.Fatalf("localFileOutputPath dir fallback = %q", got)
	}
	if got := localDirOutputPath(localCopyTarget{path: "/tmp/out", dir: true}, "srcdir", false); got != filepath.Join("/tmp/out", "srcdir") {
		t.Fatalf("localDirOutputPath named dir = %q", got)
	}
	if got := localDirOutputPath(localCopyTarget{path: "/tmp/out", dir: true}, "srcdir", true); got != "/tmp/out" {
		t.Fatalf("localDirOutputPath source hint = %q", got)
	}
}

func TestIsLocalDirHint(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: ""},
		{path: ".", want: true},
		{path: "./", want: true},
		{path: "..", want: true},
		{path: "../", want: true},
		{path: "logs/", want: true},
		{path: "logs"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := isLocalDirHint(tt.path); got != tt.want {
				t.Fatalf("isLocalDirHint(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestWaitRemoteCopyDrainsDone(t *testing.T) {
	done := make(chan error, 1)
	done <- nil
	waitRemoteCopy(done)
}
