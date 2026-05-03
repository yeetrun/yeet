// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/ftdetect"
)

type failingInfoWriter struct {
	err error
}

func (w failingInfoWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type failAfterInfoWriter struct {
	writes int
	err    error
}

func (w *failAfterInfoWriter) Write(p []byte) (int, error) {
	if w.writes == 0 {
		w.writes++
		return len(p), nil
	}
	return 0, w.err
}

func TestRenderInfoPlainReportsWriteError(t *testing.T) {
	want := errors.New("write failed")

	err := renderInfoPlain(failingInfoWriter{err: want}, "svc", "host", nil, serverInfo{}, clientInfo{}, catchrpc.ServiceInfoResponse{})
	if !errors.Is(err, want) {
		t.Fatalf("renderInfoPlain error = %v, want %v", err, want)
	}
}

func TestRenderInfoPlainReportsTabwriterFlushError(t *testing.T) {
	want := errors.New("flush failed")
	w := &failAfterInfoWriter{err: want}

	err := renderInfoPlain(w, "svc", "host", nil, serverInfo{}, clientInfo{}, catchrpc.ServiceInfoResponse{})
	if !errors.Is(err, want) {
		t.Fatalf("renderInfoPlain error = %v, want %v", err, want)
	}
}

func TestNormalizeInfoFormat(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{name: "default", input: "", want: "plain"},
		{name: "trim", input: " json-pretty ", want: "json-pretty"},
		{name: "plain alias", input: "text", want: "text"},
		{name: "unsupported", input: " yaml ", wantErr: `unsupported format "yaml" (expected plain, json, or json-pretty)`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeInfoFormat(tt.input)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("normalizeInfoFormat error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeInfoFormat returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("normalizeInfoFormat = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInfoInspectPayloadClassifiesConfiguredPayloads(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Dockerfile"), "FROM alpine\n")
	writeFile(t, filepath.Join(dir, "app.ts"), "console.log('hello')\n")

	tests := []struct {
		name      string
		payload   string
		wantKind  string
		wantImage bool
		wantExist bool
		wantErr   bool
	}{
		{name: "empty", payload: "", wantErr: true},
		{name: "image ref", payload: "ghcr.io/example/app:latest", wantKind: "image", wantImage: true},
		{name: "missing file", payload: "missing", wantErr: true},
		{name: "dockerfile", payload: "Dockerfile", wantKind: "dockerfile", wantExist: true},
		{name: "typescript", payload: "app.ts", wantKind: "typescript", wantExist: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inspectPayload(tt.payload, dir, serverInfo{}, nil)
			if got.Kind != tt.wantKind {
				t.Fatalf("Kind = %q, want %q (full payload: %+v)", got.Kind, tt.wantKind, got)
			}
			if got.ImageRef != tt.wantImage {
				t.Fatalf("ImageRef = %v, want %v", got.ImageRef, tt.wantImage)
			}
			if got.Exists != tt.wantExist {
				t.Fatalf("Exists = %v, want %v", got.Exists, tt.wantExist)
			}
			hasErr := got.ResolveErr != "" || got.DetectErr != ""
			if hasErr != tt.wantErr {
				t.Fatalf("has error = %v, want %v (full payload: %+v)", hasErr, tt.wantErr, got)
			}
		})
	}
}

func TestInfoFormatFileType(t *testing.T) {
	tests := []struct {
		ft   ftdetect.FileType
		want string
	}{
		{ftdetect.Binary, "binary"},
		{ftdetect.Script, "script"},
		{ftdetect.DockerCompose, "docker compose"},
		{ftdetect.TypeScript, "typescript"},
		{ftdetect.Python, "python"},
		{ftdetect.Zstd, "zstd archive"},
		{ftdetect.Unknown, "unknown"},
	}

	for _, tt := range tests {
		if got := formatFileType(tt.ft); got != tt.want {
			t.Fatalf("formatFileType(%v) = %q, want %q", tt.ft, got, tt.want)
		}
	}
}

func TestInfoFormatServiceDataType(t *testing.T) {
	tests := []struct {
		dt   string
		want string
	}{
		{"docker", "docker compose service"},
		{"service", "systemd service"},
		{"cron", "cron service"},
		{"binary", "systemd binary service"},
		{"typescript", "typescript service"},
		{"python", "python service"},
		{"custom", "custom"},
	}

	for _, tt := range tests {
		if got := formatServiceDataType(tt.dt); got != tt.want {
			t.Fatalf("formatServiceDataType(%q) = %q, want %q", tt.dt, got, tt.want)
		}
	}
}

func TestInfoSummarizeStatus(t *testing.T) {
	tests := []struct {
		name       string
		components []catchrpc.ServiceComponentStatus
		err        string
		want       string
	}{
		{name: "error", err: "rpc unavailable", want: "unknown (rpc unavailable)"},
		{name: "none", want: "unknown"},
		{name: "single empty", components: []catchrpc.ServiceComponentStatus{{Name: "svc"}}, want: "unknown"},
		{name: "single status", components: []catchrpc.ServiceComponentStatus{{Name: "svc", Status: "running"}}, want: "running"},
		{name: "all running", components: []catchrpc.ServiceComponentStatus{{Status: "running"}, {Status: "running"}}, want: "running (2)"},
		{name: "all stopped", components: []catchrpc.ServiceComponentStatus{{Status: "stopped"}, {Status: "stopped"}}, want: "stopped (2)"},
		{name: "all starting", components: []catchrpc.ServiceComponentStatus{{Status: "starting"}, {Status: "starting"}}, want: "starting (2)"},
		{name: "all stopping", components: []catchrpc.ServiceComponentStatus{{Status: "stopping"}, {Status: "stopping"}}, want: "stopping (2)"},
		{name: "partial running", components: []catchrpc.ServiceComponentStatus{{Status: "running"}, {Status: "stopped"}}, want: "partial (1/2)"},
		{name: "mixed", components: []catchrpc.ServiceComponentStatus{{Status: "failed"}, {Status: ""}}, want: "mixed (2)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := catchrpc.ServiceInfo{
				Status: catchrpc.ServiceStatus{
					Components: tt.components,
					Error:      tt.err,
				},
			}
			if got := summarizeStatus(info); got != tt.want {
				t.Fatalf("summarizeStatus = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInfoBuildIPGroupsOrdersLabelsAndDeduplicates(t *testing.T) {
	entries := []catchrpc.ServiceIP{
		{Label: "docker", IP: "172.18.0.2", Interface: "br-123"},
		{Label: "tailscale", IP: "100.64.0.2", Interface: "tailscale0"},
		{Label: "service", IP: "10.0.0.2", Interface: "ignored"},
		{IP: "192.168.1.20", Interface: "eth0"},
		{IP: "192.168.1.20", Interface: "eth0"},
	}

	got := buildIPGroups(entries, "10.0.0.2")
	want := []ipGroup{
		{label: "service", base: "service", ips: []string{"10.0.0.2"}},
		{label: "tailscale (tailscale0)", base: "tailscale", ips: []string{"100.64.0.2"}},
		{label: "docker (br-123)", base: "docker", ips: []string{"172.18.0.2"}},
		{label: "ip (eth0)", base: "ip", ips: []string{"192.168.1.20"}},
	}
	assertIPGroups(t, got, want)
}

func TestInfoBuildIPGroupsEmpty(t *testing.T) {
	if got := buildIPGroups(nil, ""); got != nil {
		t.Fatalf("buildIPGroups empty = %#v, want nil", got)
	}
}

func TestInfoNetworkIPRows(t *testing.T) {
	net := catchrpc.ServiceNetwork{
		SvcIP: "10.0.0.2",
		IPs: []catchrpc.ServiceIP{
			{Label: "docker", IP: "172.18.0.2", Interface: "br-123"},
			{Label: "tailscale", IP: "100.64.0.2", Interface: "tailscale0"},
		},
	}

	got := networkIPRows(net)
	want := []infoRow{
		{Label: "IPs", Value: ""},
		{Label: "  service", Value: "10.0.0.2"},
		{Label: "  tailscale (tailscale0)", Value: "100.64.0.2"},
		{Label: "  docker (br-123)", Value: "172.18.0.2"},
	}
	assertInfoRows(t, got, want)
}

func TestInfoNetworkIPRowsReportsErrorsAndEmpty(t *testing.T) {
	got := networkIPRows(catchrpc.ServiceNetwork{IPError: "permission denied"})
	assertInfoRows(t, got, []infoRow{{Label: "IPs", Value: "unavailable (permission denied)"}})

	got = networkIPRows(catchrpc.ServiceNetwork{})
	assertInfoRows(t, got, []infoRow{{Label: "IPs", Value: "none"}})
}

func TestInfoDescribeTailscale(t *testing.T) {
	tests := []struct {
		name string
		ts   *catchrpc.ServiceTailscale
		want string
	}{
		{name: "disabled", want: "disabled"},
		{name: "enabled", ts: &catchrpc.ServiceTailscale{}, want: "enabled"},
		{
			name: "details",
			ts: &catchrpc.ServiceTailscale{
				Interface: "tailscale0",
				Version:   "1.2.3",
				Tags:      []string{"tag:prod", "tag:web"},
				ExitNode:  "exit-node",
			},
			want: "tailscale0 (ver 1.2.3), tags: tag:prod, tag:web, exit: exit-node",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := describeTailscale(tt.ts); got != tt.want {
				t.Fatalf("describeTailscale = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInfoDescribeMacvlan(t *testing.T) {
	tests := []struct {
		name string
		mv   *catchrpc.ServiceMacvlan
		want string
	}{
		{name: "disabled", want: "disabled"},
		{name: "enabled", mv: &catchrpc.ServiceMacvlan{}, want: "enabled"},
		{
			name: "details",
			mv: &catchrpc.ServiceMacvlan{
				Interface: "macvlan0",
				Parent:    "eth0",
				VLAN:      20,
				Mac:       "02:42:ac:11:00:02",
			},
			want: "macvlan0, parent eth0, vlan 20, mac 02:42:ac:11:00:02",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := describeMacvlan(tt.mv); got != tt.want {
				t.Fatalf("describeMacvlan = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInfoClientPayloadRows(t *testing.T) {
	payload := &clientPayloadInfo{
		Stored:    "app.ts",
		Kind:      "typescript",
		SizeBytes: 2048,
		Exists:    true,
	}

	got := clientPayloadRows(payload)
	want := []infoRow{
		{Label: "Payload", Value: "app.ts"},
		{Label: "Payload type", Value: "typescript"},
		{Label: "Payload size", Value: "2.0 KB"},
	}
	assertInfoRows(t, got, want)
}

func TestInfoClientPayloadRowsPrefersResolveError(t *testing.T) {
	payload := &clientPayloadInfo{
		Stored:     "missing",
		Kind:       "unknown",
		ResolveErr: "stat missing: no such file or directory",
		DetectErr:  "unable to detect file type",
	}

	got := clientPayloadRows(payload)
	want := []infoRow{
		{Label: "Payload", Value: "missing"},
		{Label: "Payload type", Value: "unknown"},
		{Label: "Payload error", Value: "stat missing: no such file or directory"},
	}
	assertInfoRows(t, got, want)
}

func TestInfoClientEntryMetadataRows(t *testing.T) {
	entry := &clientServiceEntry{
		EnvFile:  ".env",
		Args:     []string{"--port", "8080"},
		Schedule: "@hourly",
	}

	got := clientEntryMetadataRows(entry)
	want := []infoRow{
		{Label: "Env file", Value: ".env"},
		{Label: "Payload args", Value: "--port 8080"},
		{Label: "Schedule", Value: "@hourly"},
	}
	assertInfoRows(t, got, want)
}

func TestInfoRenderRuntimeSection(t *testing.T) {
	tests := []struct {
		name   string
		server catchrpc.ServiceInfoResponse
		want   []infoRow
	}{
		{name: "not found", server: catchrpc.ServiceInfoResponse{}, want: nil},
		{
			name: "status error",
			server: catchrpc.ServiceInfoResponse{
				Found: true,
				Info:  catchrpc.ServiceInfo{Status: catchrpc.ServiceStatus{Error: "status unavailable"}},
			},
			want: []infoRow{{Label: "Status", Value: "status unavailable"}},
		},
		{
			name:   "unknown",
			server: catchrpc.ServiceInfoResponse{Found: true},
			want:   []infoRow{{Label: "Status", Value: "unknown"}},
		},
		{
			name: "components",
			server: catchrpc.ServiceInfoResponse{
				Found: true,
				Info: catchrpc.ServiceInfo{
					Status: catchrpc.ServiceStatus{
						Components: []catchrpc.ServiceComponentStatus{
							{Name: "web", Status: "running"},
							{Status: "stopped"},
						},
					},
				},
			},
			want: []infoRow{
				{Label: "web", Value: "running"},
				{Label: "component", Value: "stopped"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderRuntimeSection(tt.server)
			if got.Title != "Runtime" {
				t.Fatalf("Title = %q, want Runtime", got.Title)
			}
			assertInfoRows(t, got.Rows, tt.want)
		})
	}
}

func TestInfoRenderImagesSection(t *testing.T) {
	tests := []struct {
		name   string
		server catchrpc.ServiceInfoResponse
		want   []infoRow
	}{
		{name: "not found", server: catchrpc.ServiceInfoResponse{}, want: nil},
		{name: "no images", server: catchrpc.ServiceInfoResponse{Found: true}, want: nil},
		{
			name: "images",
			server: catchrpc.ServiceInfoResponse{
				Found: true,
				Info: catchrpc.ServiceInfo{
					Images: []catchrpc.ServiceImage{
						{
							Repo: "example/app",
							Refs: map[string]catchrpc.ServiceImageRef{
								"stable": {},
								"latest": {Digest: "sha256:abc"},
							},
						},
					},
				},
			},
			want: []infoRow{{Label: "example/app", Value: "latest=sha256:abc, stable"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderImagesSection(tt.server)
			if got.Title != "Images" {
				t.Fatalf("Title = %q, want Images", got.Title)
			}
			assertInfoRows(t, got.Rows, tt.want)
		})
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}

func assertInfoRows(t *testing.T, got, want []infoRow) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("rows len = %d, want %d\n got: %#v\nwant: %#v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %#v, want %#v\n got: %#v\nwant: %#v", i, got[i], want[i], got, want)
		}
	}
}

func assertIPGroups(t *testing.T, got, want []ipGroup) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("groups len = %d, want %d\n got: %#v\nwant: %#v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i].label != want[i].label || got[i].base != want[i].base {
			t.Fatalf("group %d = %#v, want %#v\n got: %#v\nwant: %#v", i, got[i], want[i], got, want)
		}
		assertStrings(t, got[i].ips, want[i].ips)
	}
}

func assertStrings(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("strings len = %d, want %d\n got: %#v\nwant: %#v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("string %d = %q, want %q\n got: %#v\nwant: %#v", i, got[i], want[i], got, want)
		}
	}
}
