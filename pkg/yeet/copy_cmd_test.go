// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
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
				Sources:   []copyEndpoint{{Raw: "local.txt", Path: "local.txt"}},
				Dst:       copyEndpoint{Raw: "svc:data/logs/", Path: "data/logs/", Service: "svc", Remote: true, DirHint: true},
			},
		},
		{
			name: "multiple local sources to remote directory",
			args: []string{"./id_ed25519", "./id_ed25519.pub", "devbox:.ssh/"},
			want: copyRequest{
				Recursive: true,
				Archive:   true,
				Compress:  true,
				Verbose:   true,
				Sources: []copyEndpoint{
					{Raw: "./id_ed25519", Path: "./id_ed25519"},
					{Raw: "./id_ed25519.pub", Path: "./id_ed25519.pub"},
				},
				Dst: copyEndpoint{Raw: "devbox:.ssh/", Path: ".ssh/", Service: "devbox", Remote: true, DirHint: true},
			},
		},
		{
			name: "multiple local sources to service data root alias",
			args: []string{"a.txt", "b.txt", "svc:data"},
			want: copyRequest{
				Recursive: true,
				Archive:   true,
				Compress:  true,
				Verbose:   true,
				Sources: []copyEndpoint{
					{Raw: "a.txt", Path: "a.txt"},
					{Raw: "b.txt", Path: "b.txt"},
				},
				Dst: copyEndpoint{Raw: "svc:data", Path: "data", Service: "svc", Remote: true},
			},
		},
		{
			name: "vm remote glob source path is preserved",
			args: []string{"devbox:.ssh/id*", "./keys/"},
			want: copyRequest{
				Recursive: true,
				Archive:   true,
				Compress:  true,
				Verbose:   true,
				Sources:   []copyEndpoint{{Raw: "devbox:.ssh/id*", Path: ".ssh/id*", Service: "devbox", Remote: true}},
				Dst:       copyEndpoint{Raw: "./keys/", Path: "./keys/"},
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
				Sources:   []copyEndpoint{{Raw: "-", Path: "-"}},
				Dst:       copyEndpoint{Raw: "svc:.", Path: ".", Service: "svc", Remote: true, DirHint: true},
			},
		},
		{
			name: "vm absolute remote destination path is preserved",
			args: []string{"local.txt", "devbox:/etc/nginx/nginx.conf"},
			want: copyRequest{
				Recursive: true,
				Archive:   true,
				Compress:  true,
				Verbose:   true,
				Sources:   []copyEndpoint{{Raw: "local.txt", Path: "local.txt"}},
				Dst:       copyEndpoint{Raw: "devbox:/etc/nginx/nginx.conf", Path: "/etc/nginx/nginx.conf", Service: "devbox", Remote: true},
			},
		},
		{
			name: "vm tilde remote destination path is preserved",
			args: []string{"local.txt", "devbox:~/app/config.yml"},
			want: copyRequest{
				Recursive: true,
				Archive:   true,
				Compress:  true,
				Verbose:   true,
				Sources:   []copyEndpoint{{Raw: "local.txt", Path: "local.txt"}},
				Dst:       copyEndpoint{Raw: "devbox:~/app/config.yml", Path: "~/app/config.yml", Service: "devbox", Remote: true},
			},
		},
		{
			name: "force proxy flag is consumed by copy",
			args: []string{"--force-proxy", "local.txt", "devbox:~/config.yml"},
			want: copyRequest{
				Recursive:  true,
				Archive:    true,
				Compress:   true,
				Verbose:    true,
				ForceProxy: true,
				Sources:    []copyEndpoint{{Raw: "local.txt", Path: "local.txt"}},
				Dst:        copyEndpoint{Raw: "devbox:~/config.yml", Path: "~/config.yml", Service: "devbox", Remote: true},
			},
		},
		{
			name:    "force proxy value is rejected",
			args:    []string{"--force-proxy=true", "local.txt", "devbox:~/config.yml"},
			wantErr: "copy --force-proxy does not take a value",
		},
		{name: "unknown long flag", args: []string{"--bogus", "a", "svc:b"}, wantErr: "unknown flag"},
		{name: "unknown short flag", args: []string{"-x", "a", "svc:b"}, wantErr: "unknown flag"},
		{name: "fewer than two operands", args: []string{"a"}, wantErr: "copy requires at least one source and one destination"},
		{name: "multiple sources require directory destination", args: []string{"a", "b", "svc:file"}, wantErr: "copy with multiple sources requires a directory destination"},
		{name: "multiple sources reject data child file destination", args: []string{"a", "b", "svc:data/file.txt"}, wantErr: "copy with multiple sources requires a directory destination"},
		{name: "mixed local and remote sources rejected", args: []string{"a", "devbox:remote", "./out/"}, wantErr: "copy sources must all be local or all be from the same VM endpoint"},
		{name: "multiple remote services rejected", args: []string{"devbox:a", "other:b", "./out/"}, wantErr: "copy sources must come from one VM endpoint"},
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

	t.Run("multiple remote sources allow existing local directory destination", func(t *testing.T) {
		dest := t.TempDir()
		got, err := parseCopyArgs([]string{"devbox:logs/a.txt", "devbox:logs/b.txt", dest})
		if err != nil {
			t.Fatalf("parseCopyArgs: %v", err)
		}
		want := copyRequest{
			Recursive: true,
			Archive:   true,
			Compress:  true,
			Verbose:   true,
			Sources: []copyEndpoint{
				{Raw: "devbox:logs/a.txt", Path: "logs/a.txt", Service: "devbox", Remote: true},
				{Raw: "devbox:logs/b.txt", Path: "logs/b.txt", Service: "devbox", Remote: true},
			},
			Dst: copyEndpoint{Raw: dest, Path: dest},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("parseCopyArgs = %#v, want %#v", got, want)
		}
	})
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
		{flag: "--force-proxy", want: copyRequest{ForceProxy: true}},
	}

	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			var req copyRequest
			if err := applyLongCopyFlag(&req, tt.flag); err != nil {
				t.Fatalf("applyLongCopyFlag error: %v", err)
			}
			if !reflect.DeepEqual(req, tt.want) {
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

func TestNormalizeServiceDataCopyRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     copyRequest
		want    copyRequest
		wantErr string
	}{
		{
			name: "upload strips data prefix",
			req: copyRequest{
				Sources: []copyEndpoint{{Raw: "local.txt", Path: "local.txt"}},
				Dst:     copyEndpoint{Raw: "svc:data/logs/", Path: "data/logs/", Service: "svc", Remote: true, DirHint: true},
			},
			want: copyRequest{
				Sources: []copyEndpoint{{Raw: "local.txt", Path: "local.txt"}},
				Dst:     copyEndpoint{Raw: "svc:data/logs/", Path: "logs", Service: "svc", Remote: true, DirHint: true},
			},
		},
		{
			name: "download dot targets data root",
			req: copyRequest{
				Sources: []copyEndpoint{{Raw: "svc:.", Path: ".", Service: "svc", Remote: true, DirHint: true}},
				Dst:     copyEndpoint{Raw: "./out", Path: "./out"},
			},
			want: copyRequest{
				Sources: []copyEndpoint{{Raw: "svc:.", Path: "", Service: "svc", Remote: true, DirHint: true}},
				Dst:     copyEndpoint{Raw: "./out", Path: "./out"},
			},
		},
		{
			name: "download normalizes all remote sources",
			req: copyRequest{
				Sources: []copyEndpoint{
					{Raw: "svc:data/logs/a.txt", Path: "data/logs/a.txt", Service: "svc", Remote: true},
					{Raw: "svc:./logs/b.txt", Path: "./logs/b.txt", Service: "svc", Remote: true},
				},
				Dst: copyEndpoint{Raw: "./out/", Path: "./out/"},
			},
			want: copyRequest{
				Sources: []copyEndpoint{
					{Raw: "svc:data/logs/a.txt", Path: "logs/a.txt", Service: "svc", Remote: true},
					{Raw: "svc:./logs/b.txt", Path: "logs/b.txt", Service: "svc", Remote: true},
				},
				Dst: copyEndpoint{Raw: "./out/", Path: "./out/"},
			},
		},
		{
			name: "regular service rejects absolute destination",
			req: copyRequest{
				Sources: []copyEndpoint{{Raw: "local.txt", Path: "local.txt"}},
				Dst:     copyEndpoint{Raw: "svc:/etc/passwd", Path: "/etc/passwd", Service: "svc", Remote: true},
			},
			wantErr: "remote path must be relative",
		},
		{
			name: "regular service rejects absolute source",
			req: copyRequest{
				Sources: []copyEndpoint{{Raw: "svc:/etc/passwd", Path: "/etc/passwd", Service: "svc", Remote: true}},
				Dst:     copyEndpoint{Raw: "./out", Path: "./out"},
			},
			wantErr: "remote path must be relative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeServiceDataCopyRequest(tt.req)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("normalizeServiceDataCopyRequest error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeServiceDataCopyRequest: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("normalizeServiceDataCopyRequest = %#v, want %#v", got, tt.want)
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
				Sources: []copyEndpoint{{Raw: "local.txt", Path: "local.txt"}},
				Dst:     copyEndpoint{Raw: "svc:logs", Path: "logs", Service: "svc", Remote: true},
			},
			wantDirection: copyDirectionToRemote,
			wantRemote:    copyEndpoint{Raw: "svc:logs", Path: "logs", Service: "svc", Remote: true},
		},
		{
			name: "multiple local sources to remote",
			req: copyRequest{
				Sources: []copyEndpoint{
					{Raw: "a.txt", Path: "a.txt"},
					{Raw: "b.txt", Path: "b.txt"},
				},
				Dst: copyEndpoint{Raw: "svc:logs/", Path: "logs/", Service: "svc", Remote: true, DirHint: true},
			},
			wantDirection: copyDirectionToRemote,
			wantRemote:    copyEndpoint{Raw: "svc:logs/", Path: "logs/", Service: "svc", Remote: true, DirHint: true},
		},
		{
			name: "remote to local",
			req: copyRequest{
				Sources: []copyEndpoint{{Raw: "svc:logs", Path: "logs", Service: "svc", Remote: true}},
				Dst:     copyEndpoint{Raw: "local.txt", Path: "local.txt"},
			},
			wantDirection: copyDirectionFromRemote,
			wantRemote:    copyEndpoint{Raw: "svc:logs", Path: "logs", Service: "svc", Remote: true},
		},
		{
			name: "multiple remote sources from same service",
			req: copyRequest{
				Sources: []copyEndpoint{
					{Raw: "svc:logs/a.txt", Path: "logs/a.txt", Service: "svc", Remote: true},
					{Raw: "svc:logs/b.txt", Path: "logs/b.txt", Service: "svc", Remote: true},
				},
				Dst: copyEndpoint{Raw: "./out/", Path: "./out/"},
			},
			wantDirection: copyDirectionFromRemote,
			wantRemote:    copyEndpoint{Raw: "svc:logs/a.txt", Path: "logs/a.txt", Service: "svc", Remote: true},
		},
		{
			name: "remote to remote rejected",
			req: copyRequest{
				Sources: []copyEndpoint{{Raw: "src:logs", Path: "logs", Service: "src", Remote: true}},
				Dst:     copyEndpoint{Raw: "dst:logs", Path: "logs", Service: "dst", Remote: true},
			},
			wantErr: "remote-to-remote",
		},
		{
			name: "local to local rejected",
			req: copyRequest{
				Sources: []copyEndpoint{{Raw: "a", Path: "a"}},
				Dst:     copyEndpoint{Raw: "b", Path: "b"},
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
	t.Setenv("CATCH_HOST", "")
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

func TestRunCopyCommandRoutesVMEndpointToRsync(t *testing.T) {
	oldServerInfo := fetchSSHServerInfoFunc
	oldServiceInfo := fetchSSHServiceInfoFunc
	oldRunVM := runVMRsyncCopyFunc
	oldExec := execRemoteFn
	oldHost := Host()
	oldOverride := hostOverride
	oldOverrideSet := hostOverrideSet
	defer func() {
		fetchSSHServerInfoFunc = oldServerInfo
		fetchSSHServiceInfoFunc = oldServiceInfo
		runVMRsyncCopyFunc = oldRunVM
		execRemoteFn = oldExec
		SetHost(oldHost)
		hostOverride = oldOverride
		hostOverrideSet = oldOverrideSet
	}()
	resetHostOverride()
	SetHost("yeet-pve1")

	fetchSSHServerInfoFunc = func(ctx context.Context, host string) (serverInfo, error) {
		if host != "yeet-pve1" {
			t.Fatalf("server info host = %q, want yeet-pve1", host)
		}
		return serverInfo{InstallUser: "root"}, nil
	}
	fetchSSHServiceInfoFunc = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		if host != "yeet-pve1" || service != "devbox" {
			t.Fatalf("service info = %s %s, want yeet-pve1 devbox", host, service)
		}
		return catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: serviceTypeVM,
				Network:     catchrpc.ServiceNetwork{SvcIP: "192.168.100.12"},
				VM: &catchrpc.ServiceVM{
					SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "192.168.100.12"},
				},
			},
		}, nil
	}
	execRemoteFn = func(context.Context, string, []string, io.Reader, bool) error {
		t.Fatal("regular service-data copy path should not run for VM endpoint")
		return nil
	}

	var gotReq copyRequest
	var gotDirection copyDirection
	var gotRemote copyEndpoint
	runVMRsyncCopyFunc = func(ctx context.Context, req copyRequest, direction copyDirection, remote copyEndpoint, remoteCtx copyRemoteContext) error {
		gotReq = req
		gotDirection = direction
		gotRemote = remote
		if remoteCtx.Host != "yeet-pve1" || remoteCtx.Server.InstallUser != "root" || remoteCtx.Service.Info.ServiceType != serviceTypeVM {
			t.Fatalf("remote context = %#v", remoteCtx)
		}
		return nil
	}

	if err := runCopyCommand([]string{"--force-proxy", "./local.txt", "devbox:/etc/motd"}, nil); err != nil {
		t.Fatalf("runCopyCommand: %v", err)
	}
	if !gotReq.ForceProxy || gotDirection != copyDirectionToRemote || gotRemote.Path != "/etc/motd" {
		t.Fatalf("VM copy routing = req %#v direction %v remote %#v", gotReq, gotDirection, gotRemote)
	}
}

func TestRunCopyCommandRejectsForceProxyForRegularService(t *testing.T) {
	oldServerInfo := fetchSSHServerInfoFunc
	oldServiceInfo := fetchSSHServiceInfoFunc
	oldHost := Host()
	defer func() {
		fetchSSHServerInfoFunc = oldServerInfo
		fetchSSHServiceInfoFunc = oldServiceInfo
		SetHost(oldHost)
		resetHostOverride()
	}()
	resetHostOverride()
	SetHost("yeet-pve1")

	fetchSSHServerInfoFunc = func(context.Context, string) (serverInfo, error) {
		return serverInfo{}, nil
	}
	fetchSSHServiceInfoFunc = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{ServiceType: dockerServiceType}}, nil
	}

	err := runCopyCommand([]string{"--force-proxy", "./local.txt", "web:config.yml"}, nil)
	if err == nil || !strings.Contains(err.Error(), "copy --force-proxy only applies to VM services") {
		t.Fatalf("runCopyCommand error = %v, want force proxy regular service error", err)
	}
}

func TestRunCopyCommandRejectsRegularServiceRemoteGlob(t *testing.T) {
	oldServerInfo := fetchSSHServerInfoFunc
	oldServiceInfo := fetchSSHServiceInfoFunc
	oldStream := execRemoteStreamFn
	oldHost := Host()
	defer func() {
		fetchSSHServerInfoFunc = oldServerInfo
		fetchSSHServiceInfoFunc = oldServiceInfo
		execRemoteStreamFn = oldStream
		SetHost(oldHost)
		resetHostOverride()
	}()
	resetHostOverride()
	SetHost("yeet-pve1")

	fetchSSHServerInfoFunc = func(context.Context, string) (serverInfo, error) {
		return serverInfo{}, nil
	}
	fetchSSHServiceInfoFunc = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{ServiceType: dockerServiceType}}, nil
	}
	execRemoteStreamFn = func(context.Context, string, []string, io.Reader) (io.ReadCloser, <-chan error, error) {
		t.Fatal("regular service remote glob should be rejected before streaming")
		return nil, nil, nil
	}

	err := runCopyCommand([]string{"web:logs/*.txt", "./logs/"}, nil)
	if err == nil || !strings.Contains(err.Error(), "remote globs are only supported for VM endpoints; copy an exact service path or directory") {
		t.Fatalf("runCopyCommand error = %v, want regular service remote glob error", err)
	}
}

type recordedRsync struct {
	args []string
	err  error
}

func TestRunVMRsyncCopyUploadBuildsRsyncCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stubCurrentExecutable(t, testVMSSHProxyExecutable)

	oldLookPath := lookPathCopyBinaryFunc
	oldRun := runRsyncCommandFunc
	defer func() {
		lookPathCopyBinaryFunc = oldLookPath
		runRsyncCommandFunc = oldRun
	}()
	lookPathCopyBinaryFunc = func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	}

	var got recordedRsync
	runRsyncCommandFunc = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		got.args = append([]string{}, args...)
		return got.err
	}

	req := copyRequest{
		Archive:  true,
		Compress: true,
		Verbose:  true,
		Sources: []copyEndpoint{
			{Raw: "./local.txt", Path: "./local.txt"},
			{Raw: "./local.pub", Path: "./local.pub"},
		},
		Dst: copyEndpoint{Raw: "devbox:/etc/motd", Path: "/etc/motd", Service: "devbox", Remote: true},
	}
	remote := req.Dst
	remoteCtx := copyRemoteContext{
		Host:   "yeet-pve1",
		Server: serverInfo{InstallUser: "root"},
		Service: catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: serviceTypeVM,
				Network:     catchrpc.ServiceNetwork{SvcIP: "192.168.100.12"},
				VM: &catchrpc.ServiceVM{
					SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "192.168.100.12"},
				},
			},
		},
	}

	if err := runVMRsyncCopy(context.Background(), req, copyDirectionToRemote, remote, remoteCtx); err != nil {
		t.Fatalf("runVMRsyncCopy: %v", err)
	}
	if len(got.args) == 0 {
		t.Fatal("rsync did not run")
	}
	for _, want := range []string{"-avz", "-e", "./local.txt", "./local.pub", "yeet-pve1:/etc/motd"} {
		if !slices.Contains(got.args, want) {
			t.Fatalf("rsync args = %#v, want %q", got.args, want)
		}
	}
	localIdx := slices.Index(got.args, "./local.txt")
	pubIdx := slices.Index(got.args, "./local.pub")
	remoteIdx := slices.Index(got.args, "yeet-pve1:/etc/motd")
	if localIdx < 0 || pubIdx < 0 || remoteIdx < 0 || !(localIdx < pubIdx && pubIdx < remoteIdx) {
		t.Fatalf("rsync args = %#v, want local sources before remote destination", got.args)
	}
	remoteShell := got.args[slices.Index(got.args, "-e")+1]
	for _, want := range []string{"ssh", "-l ubuntu", "-o HostName=192.168.100.12", "ProxyCommand=/tmp/yeet-current --host=yeet-pve1 _vm-ssh-proxy devbox %h %p"} {
		if !strings.Contains(remoteShell, want) {
			t.Fatalf("remote shell = %q, want %q", remoteShell, want)
		}
	}
}

func TestRunVMRsyncCopyDownloadBuildsRsyncCommand(t *testing.T) {
	oldLookPath := lookPathCopyBinaryFunc
	oldRun := runRsyncCommandFunc
	defer func() {
		lookPathCopyBinaryFunc = oldLookPath
		runRsyncCommandFunc = oldRun
	}()
	lookPathCopyBinaryFunc = func(name string) (string, error) { return "/usr/bin/" + name, nil }

	var gotArgs []string
	runRsyncCommandFunc = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		gotArgs = append([]string{}, args...)
		return nil
	}

	req := copyRequest{
		Archive:  true,
		Compress: true,
		Verbose:  true,
		Sources: []copyEndpoint{
			{Raw: "devbox:.ssh/id*", Path: ".ssh/id*", Service: "devbox", Remote: true},
			{Raw: "devbox:~/app.log", Path: "~/app.log", Service: "devbox", Remote: true},
		},
		Dst: copyEndpoint{Raw: "./logs/", Path: "./logs/"},
	}
	remoteCtx := copyRemoteContext{
		Host:   "yeet-pve1",
		Server: serverInfo{InstallUser: "root"},
		Service: catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: serviceTypeVM,
				VM: &catchrpc.ServiceVM{
					SSH:      &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "10.0.4.80"},
					Networks: []catchrpc.ServiceVMNetwork{{Mode: "lan", IP: "10.0.4.80"}},
				},
			},
		},
	}

	if err := runVMRsyncCopy(context.Background(), req, copyDirectionFromRemote, req.Sources[0], remoteCtx); err != nil {
		t.Fatalf("runVMRsyncCopy: %v", err)
	}
	for _, want := range []string{"-avz", "yeet-pve1:.ssh/id*", "yeet-pve1:~/app.log", "./logs/"} {
		if !slices.Contains(gotArgs, want) {
			t.Fatalf("rsync args = %#v, want %q", gotArgs, want)
		}
	}
	globIdx := slices.Index(gotArgs, "yeet-pve1:.ssh/id*")
	logIdx := slices.Index(gotArgs, "yeet-pve1:~/app.log")
	destIdx := slices.Index(gotArgs, "./logs/")
	if globIdx < 0 || logIdx < 0 || destIdx < 0 || !(globIdx < logIdx && logIdx < destIdx) {
		t.Fatalf("rsync args = %#v, want remote sources before local destination", gotArgs)
	}
	remoteShell := gotArgs[slices.Index(gotArgs, "-e")+1]
	for _, want := range []string{"ssh", "-l ubuntu", "-o HostName=10.0.4.80"} {
		if !strings.Contains(remoteShell, want) {
			t.Fatalf("remote shell = %q, want %q", remoteShell, want)
		}
	}
	if strings.Contains(remoteShell, "ProxyCommand=") {
		t.Fatalf("remote shell = %q, want direct LAN SSH without generated proxy", remoteShell)
	}
}

func TestRunVMRsyncCopyVMSvcLANUsesReachableLANDirectly(t *testing.T) {
	oldLookPath := lookPathCopyBinaryFunc
	oldRun := runRsyncCommandFunc
	defer func() {
		lookPathCopyBinaryFunc = oldLookPath
		runRsyncCommandFunc = oldRun
	}()
	lookPathCopyBinaryFunc = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	stubVMSSHLANReachable(t, func(host string) bool {
		return host == "10.0.4.80"
	})

	var gotArgs []string
	runRsyncCommandFunc = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		gotArgs = append([]string{}, args...)
		return nil
	}

	req := copyRequest{
		Archive:  true,
		Compress: true,
		Sources:  []copyEndpoint{{Raw: "./local.txt", Path: "./local.txt"}},
		Dst:      copyEndpoint{Raw: "devbox:/etc/motd", Path: "/etc/motd", Service: "devbox", Remote: true},
	}
	remoteCtx := copyRemoteContext{
		Host:   "yeet-pve1",
		Server: serverInfo{InstallUser: "root"},
		Service: catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: serviceTypeVM,
				Network:     catchrpc.ServiceNetwork{SvcIP: "192.168.100.12"},
				VM: &catchrpc.ServiceVM{
					SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "10.0.4.80"},
					Networks: []catchrpc.ServiceVMNetwork{
						{Mode: "svc", IP: "192.168.100.12"},
						{Mode: "lan", IP: "10.0.4.80"},
					},
				},
			},
		},
	}

	if err := runVMRsyncCopy(context.Background(), req, copyDirectionToRemote, req.Dst, remoteCtx); err != nil {
		t.Fatalf("runVMRsyncCopy: %v", err)
	}
	remoteShell := gotArgs[slices.Index(gotArgs, "-e")+1]
	for _, want := range []string{"ssh", "-l ubuntu", "-o HostName=10.0.4.80"} {
		if !strings.Contains(remoteShell, want) {
			t.Fatalf("remote shell = %q, want %q", remoteShell, want)
		}
	}
	if strings.Contains(remoteShell, "ProxyCommand=") {
		t.Fatalf("remote shell = %q, want direct LAN SSH without generated proxy", remoteShell)
	}
}

func TestRunVMRsyncCopyVMSvcLANFallsBackToSvcProxyWhenLANUnreachable(t *testing.T) {
	stubCurrentExecutable(t, testVMSSHProxyExecutable)

	oldLookPath := lookPathCopyBinaryFunc
	oldRun := runRsyncCommandFunc
	defer func() {
		lookPathCopyBinaryFunc = oldLookPath
		runRsyncCommandFunc = oldRun
	}()
	lookPathCopyBinaryFunc = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	stubVMSSHLANReachable(t, func(host string) bool {
		if host != "10.0.4.80" {
			t.Fatalf("LAN reachability checked host %q, want 10.0.4.80", host)
		}
		return false
	})

	var gotArgs []string
	runRsyncCommandFunc = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		gotArgs = append([]string{}, args...)
		return nil
	}

	req := copyRequest{
		Archive:  true,
		Compress: true,
		Sources:  []copyEndpoint{{Raw: "./local.txt", Path: "./local.txt"}},
		Dst:      copyEndpoint{Raw: "devbox:/etc/motd", Path: "/etc/motd", Service: "devbox", Remote: true},
	}
	remoteCtx := copyRemoteContext{
		Host:   "yeet-pve1",
		Server: serverInfo{InstallUser: "root"},
		Service: catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: serviceTypeVM,
				Network:     catchrpc.ServiceNetwork{SvcIP: "192.168.100.12"},
				VM: &catchrpc.ServiceVM{
					SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "10.0.4.80"},
					Networks: []catchrpc.ServiceVMNetwork{
						{Mode: "svc", IP: "192.168.100.12"},
						{Mode: "lan", IP: "10.0.4.80"},
					},
				},
			},
		},
	}

	if err := runVMRsyncCopy(context.Background(), req, copyDirectionToRemote, req.Dst, remoteCtx); err != nil {
		t.Fatalf("runVMRsyncCopy: %v", err)
	}
	remoteShell := gotArgs[slices.Index(gotArgs, "-e")+1]
	for _, want := range []string{"ssh", "-l ubuntu", "-o HostName=192.168.100.12", "ProxyCommand=/tmp/yeet-current --host=yeet-pve1 _vm-ssh-proxy devbox %h %p"} {
		if !strings.Contains(remoteShell, want) {
			t.Fatalf("remote shell = %q, want %q", remoteShell, want)
		}
	}
}

func TestRunVMRsyncCopyMissingLocalRsync(t *testing.T) {
	oldLookPath := lookPathCopyBinaryFunc
	defer func() { lookPathCopyBinaryFunc = oldLookPath }()
	lookPathCopyBinaryFunc = func(name string) (string, error) {
		if name == "rsync" {
			return "", exec.ErrNotFound
		}
		return "/usr/bin/" + name, nil
	}

	err := runVMRsyncCopy(context.Background(), copyRequest{}, copyDirectionToRemote, copyEndpoint{Service: "devbox"}, copyRemoteContext{
		Host:   "yeet-pve1",
		Server: serverInfo{InstallUser: "root"},
		Service: catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				ServiceType: serviceTypeVM,
				Network:     catchrpc.ServiceNetwork{SvcIP: "192.168.100.12"},
				VM:          &catchrpc.ServiceVM{SSH: &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "192.168.100.12"}},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "rsync CLI not found in PATH") {
		t.Fatalf("runVMRsyncCopy error = %v, want missing rsync", err)
	}
}

func TestWithGuestRsyncHint(t *testing.T) {
	err := withGuestRsyncHint(errors.New("exit status 127"), "bash: rsync: command not found\n", "devbox")
	if err == nil || !strings.Contains(err.Error(), "remote rsync is not available on VM \"devbox\"") {
		t.Fatalf("hint error = %v", err)
	}
}

func TestRunRsyncPlanRepairsKnownHostOnce(t *testing.T) {
	oldRun := runRsyncCommandFunc
	oldRemove := removeSSHKnownHostFunc
	defer func() {
		runRsyncCommandFunc = oldRun
		removeSSHKnownHostFunc = oldRemove
	}()

	var runs int
	runRsyncCommandFunc = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		runs++
		if runs == 1 {
			_, _ = io.WriteString(stderr, "WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!\nOffending ED25519 key in /tmp/known_hosts:3\n")
			return errors.New("exit status 255")
		}
		return nil
	}
	var removed []string
	removeSSHKnownHostFunc = func(ctx context.Context, alias, knownHosts string) error {
		removed = append(removed, alias+"@"+knownHosts)
		return nil
	}

	plan := sshExecutionPlan{
		Args: []string{"-o", "HostName=192.168.100.12", "yeet-pve1"},
		KnownHostRepair: &sshKnownHostRepair{
			Alias:          "yeet-vm-devbox@yeet-pve1",
			KnownHostsFile: "/tmp/known_hosts",
		},
	}
	var stderr bytes.Buffer
	if err := runRsyncPlan(context.Background(), []string{"-avz"}, plan, "devbox", io.Discard, &stderr); err != nil {
		t.Fatalf("runRsyncPlan: %v; stderr=%s", err, stderr.String())
	}
	if runs != 2 || len(removed) != 1 {
		t.Fatalf("runs=%d removed=%v, want one repair and retry", runs, removed)
	}
}

func TestRunRsyncPlanHintsWhenInitialAttemptFindsMissingGuestRsync(t *testing.T) {
	oldRun := runRsyncCommandFunc
	defer func() {
		runRsyncCommandFunc = oldRun
	}()

	runRsyncCommandFunc = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		_, _ = io.WriteString(stderr, "bash: rsync: command not found\n")
		return errors.New("exit status 127")
	}

	plan := sshExecutionPlan{Args: []string{"-o", "HostName=192.168.100.12", "yeet-pve1"}}
	err := runRsyncPlan(context.Background(), []string{"-avz"}, plan, "devbox", io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "remote rsync is not available on VM \"devbox\"") {
		t.Fatalf("runRsyncPlan error = %v, want missing guest rsync hint", err)
	}
}

func TestRunRsyncPlanHintsWhenRetryFindsMissingGuestRsync(t *testing.T) {
	oldRun := runRsyncCommandFunc
	oldRemove := removeSSHKnownHostFunc
	defer func() {
		runRsyncCommandFunc = oldRun
		removeSSHKnownHostFunc = oldRemove
	}()

	var runs int
	runRsyncCommandFunc = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		runs++
		if runs == 1 {
			_, _ = io.WriteString(stderr, "WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!\nOffending ED25519 key in /tmp/known_hosts:3\n")
			return errors.New("exit status 255")
		}
		_, _ = io.WriteString(stderr, "bash: rsync: command not found\n")
		return errors.New("exit status 127")
	}
	removeSSHKnownHostFunc = func(ctx context.Context, alias, knownHosts string) error {
		return nil
	}

	plan := sshExecutionPlan{
		Args: []string{"-o", "HostName=192.168.100.12", "yeet-pve1"},
		KnownHostRepair: &sshKnownHostRepair{
			Alias:          "yeet-vm-devbox@yeet-pve1",
			KnownHostsFile: "/tmp/known_hosts",
		},
	}
	err := runRsyncPlan(context.Background(), []string{"-avz"}, plan, "devbox", io.Discard, io.Discard)
	if runs != 2 {
		t.Fatalf("runs = %d, want stale-key repair retry", runs)
	}
	if err == nil || !strings.Contains(err.Error(), "remote rsync is not available on VM \"devbox\"") {
		t.Fatalf("runRsyncPlan error = %v, want missing guest rsync hint", err)
	}
}

func TestCopyEndpointValidationHelpers(t *testing.T) {
	if _, err := remoteCopyDestination(copyRequest{Dst: copyEndpoint{Path: "logs"}}); err == nil {
		t.Fatal("remoteCopyDestination local dst error = nil, want error")
	}
	if _, err := localCopySource(copyRequest{}); err == nil {
		t.Fatal("localCopySource empty source error = nil, want error")
	}
	if _, err := remoteCopySource(copyRequest{Sources: []copyEndpoint{{Path: "logs"}}}); err == nil {
		t.Fatal("remoteCopySource local src error = nil, want error")
	}
	if _, err := remoteCopySource(copyRequest{Sources: []copyEndpoint{{Remote: true}}}); err == nil {
		t.Fatal("remoteCopySource missing service error = nil, want error")
	}
	if _, err := remoteCopySource(copyRequest{Sources: []copyEndpoint{{Remote: true, Service: "svc-a"}}}); err == nil {
		t.Fatal("remoteCopySource empty path without dir hint error = nil, want error")
	}
	src, err := remoteCopySource(copyRequest{Sources: []copyEndpoint{{Remote: true, Service: "svc-a", DirHint: true}}})
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
		Sources:   []copyEndpoint{{Path: "", DirHint: true}},
	})
	if want := []string{"copy", "--from", ".", "--compress", "--recursive"}; !reflect.DeepEqual(download, want) {
		t.Fatalf("copyDownloadArgs = %#v, want %#v", download, want)
	}
}

func TestCopyServiceDataFromRemoteRejectsMultipleSources(t *testing.T) {
	oldStream := execRemoteStreamFn
	defer func() { execRemoteStreamFn = oldStream }()

	execRemoteStreamFn = func(context.Context, string, []string, io.Reader) (io.ReadCloser, <-chan error, error) {
		t.Fatal("regular service multi-source download should be rejected before streaming")
		return nil, nil, nil
	}

	err := copyServiceDataFromRemote(copyRequest{
		Archive:  true,
		Compress: true,
		Sources: []copyEndpoint{
			{Raw: "svc-a:logs/a.txt", Path: "logs/a.txt", Service: "svc-a", Remote: true},
			{Raw: "svc-a:logs/b.txt", Path: "logs/b.txt", Service: "svc-a", Remote: true},
		},
		Dst: copyEndpoint{Raw: "./out/", Path: "./out/"},
	})
	if err == nil || !strings.Contains(err.Error(), "regular service copy downloads support one source; use a VM endpoint for rsync-style remote multi-source copy") {
		t.Fatalf("copyServiceDataFromRemote error = %v, want multi-source regular download error", err)
	}
}

func TestCopyServiceDataToRemoteIncludesSourcePathOnFailure(t *testing.T) {
	oldExec := execRemoteFn
	defer func() { execRemoteFn = oldExec }()

	tmp := t.TempDir()
	srcA := filepath.Join(tmp, "a.txt")
	srcB := filepath.Join(tmp, "b.txt")
	if err := os.WriteFile(srcA, []byte("a"), 0o600); err != nil {
		t.Fatalf("WriteFile a.txt: %v", err)
	}
	if err := os.WriteFile(srcB, []byte("b"), 0o600); err != nil {
		t.Fatalf("WriteFile b.txt: %v", err)
	}

	var calls int
	execRemoteFn = func(context.Context, string, []string, io.Reader, bool) error {
		calls++
		if calls == 2 {
			return errors.New("upload failed")
		}
		return nil
	}

	err := copyServiceDataToRemote(copyRequest{
		Archive:  false,
		Compress: false,
		Sources: []copyEndpoint{
			{Raw: srcA, Path: srcA},
			{Raw: srcB, Path: srcB},
		},
		Dst: copyEndpoint{Raw: "svc-a:incoming/", Path: "incoming", Service: "svc-a", Remote: true, DirHint: true},
	})
	if err == nil || !strings.Contains(err.Error(), "copy "+srcB+": upload failed") {
		t.Fatalf("copyServiceDataToRemote error = %v, want source path wrapper", err)
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
		Sources:  []copyEndpoint{{Raw: src, Path: src}},
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
		Sources: []copyEndpoint{{Raw: src, Path: src}},
		Dst:     copyEndpoint{Raw: "svc:../secret", Path: "../secret"},
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
