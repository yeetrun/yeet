// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
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

func TestRenderInfoSectionsStylesTTYTitlesOnly(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	oldIsTerminal := isTerminalFn
	t.Cleanup(func() { isTerminalFn = oldIsTerminal })
	isTerminalFn = func(fd int) bool { return fd == 42 }

	out := &fdBuffer{fd: 42}
	if err := renderInfoSections(out, []infoSection{{
		Title: "Service",
		Rows:  []infoRow{{Label: "Name", Value: "svc-a"}},
	}}); err != nil {
		t.Fatalf("renderInfoSections error: %v", err)
	}

	const styledTitle = "\x1b[1;36mService\x1b[m"
	if !strings.Contains(out.String(), styledTitle) {
		t.Fatalf("info output missing styled title:\n%s", out.String())
	}
	plain := strings.ReplaceAll(out.String(), styledTitle, "Service")
	if strings.Contains(plain, "\x1b[") {
		t.Fatalf("info output styled more than its titles:\n%s", out.String())
	}
	if !strings.Contains(plain, "Name:") || !strings.Contains(plain, "svc-a") {
		t.Fatalf("info row changed:\n%s", out.String())
	}
}

func TestRenderInfoPlainIncludesVMSection(t *testing.T) {
	server := catchrpc.ServiceInfoResponse{
		Found: true,
		Info: catchrpc.ServiceInfo{
			DataType:    "vm",
			ServiceType: "vm",
			VM: &catchrpc.ServiceVM{
				Runtime:      "firecracker",
				VMMIsolation: "jailer",
				Image:        "vm://ubuntu/26.04",
				CPUs:         4,
				MemoryBytes:  4 << 30,
				Balloon: catchrpc.ServiceVMBalloon{
					Mode:      "auto",
					MinBytes:  1 << 30,
					MinMemory: "1 GB",
				},
				DiskBytes: 128 << 30,
				SSH:       &catchrpc.ServiceVMSSH{User: "ubuntu", Host: "192.168.100.12"},
				Console:   &catchrpc.ServiceVMConsole{Available: true},
				Networks: []catchrpc.ServiceVMNetwork{
					{Mode: "svc", Interface: "eth0", IP: "192.168.100.12", Source: "config"},
					{Mode: "lan", Interface: "eth1", IP: "10.0.4.200", Source: "agent"},
				},
				SetupState: "ready",
			},
		},
	}

	var out bytes.Buffer
	if err := renderInfoPlain(&out, "devbox", "host", nil, serverInfo{}, clientInfo{}, server); err != nil {
		t.Fatalf("renderInfoPlain: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"VM\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered info missing %q:\n%s", want, text)
		}
	}
	assertPlainRow(t, text, "Type", "VM")
	assertPlainRow(t, text, "Runtime", "firecracker")
	assertPlainRow(t, text, "VMM isolation", "jailer")
	assertPlainRow(t, text, "CPU", "4")
	assertPlainRow(t, text, "Balloon", "auto, floor 1 GB")
	assertPlainRow(t, text, "SSH", "ubuntu@192.168.100.12")
	assertPlainRow(t, text, "Provisioning", "ready")
	if strings.Contains(text, "Backend:") {
		t.Fatalf("rendered VM info should not duplicate service type with backend row:\n%s", text)
	}
	if strings.Contains(text, "VM networks:") {
		t.Fatalf("rendered VM info should not duplicate IP rows with VM networks summary:\n%s", text)
	}
	if strings.Contains(text, "\nRuntime\n") {
		t.Fatalf("rendered VM info should not duplicate service status in Runtime section:\n%s", text)
	}
	if strings.Contains(text, "Staged changes:") {
		t.Fatalf("rendered VM info should not include clean staged-changes row:\n%s", text)
	}
	if strings.Contains(text, "Client (yeet.toml)") {
		t.Fatalf("rendered VM info should not include empty local client section:\n%s", text)
	}
	if strings.Contains(text, "Tailscale:") {
		t.Fatalf("rendered VM info should not include service tailscale row:\n%s", text)
	}
	if strings.Contains(text, "Macvlan:") {
		t.Fatalf("rendered VM info should not include service macvlan row:\n%s", text)
	}
	if strings.Contains(text, "Ports:") {
		t.Fatalf("rendered VM info should not include service ports row:\n%s", text)
	}
}

func TestRenderInfoPlainIncludesNativeIdentityAndWarning(t *testing.T) {
	server := catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
		ServiceType: "systemd", DataType: "binary",
		Identity: &catchrpc.ServiceIdentity{
			RequestedUser: "app", RequestedGroup: "app", UID: 1002, GID: 1003,
			Class: "operator", Mismatch: "service identity UID drift: app now resolves to UID 1012, persisted UID is 1002",
		},
	}}
	var out bytes.Buffer
	if err := renderInfoPlain(&out, "api", "host", nil, serverInfo{}, clientInfo{}, server); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	assertPlainRow(t, text, "Run as", "app:app (UID 1002, GID 1003; operator)")
	assertPlainRow(t, text, "Identity warning", server.Info.Identity.Mismatch)
}

func TestRenderInfoPlainIncludesActiveSandboxAndLegacyMigration(t *testing.T) {
	on := catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
		ServiceType: "systemd",
		Sandbox: &catchrpc.ServiceSandbox{
			State: "on",
			ReadOnly: []catchrpc.ServiceSandboxExposure{
				{Source: "/z", Destination: "/inside/z"},
				{Source: "/a", Destination: "/a"},
			},
			Writable: []catchrpc.ServiceSandboxExposure{{Source: "/work", Destination: "/data"}},
		},
	}}
	var plain bytes.Buffer
	if err := renderInfoPlain(&plain, "api", "host", nil, serverInfo{}, clientInfo{}, on); err != nil {
		t.Fatalf("renderInfoPlain on: %v", err)
	}
	assertPlainRow(t, plain.String(), "Sandbox", "on")
	assertPlainRow(t, plain.String(), "Sandbox read-only", "/a, /z:/inside/z")
	assertPlainRow(t, plain.String(), "Sandbox writable", "/work:/data")
	if strings.Contains(plain.String(), "Sandbox migration") {
		t.Fatalf("on sandbox rendered migration guidance:\n%s", plain.String())
	}

	legacy := catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
		ServiceType: "systemd",
		Sandbox:     &catchrpc.ServiceSandbox{State: "legacy"},
	}}
	plain.Reset()
	if err := renderInfoPlain(&plain, "api", "host", nil, serverInfo{}, clientInfo{}, legacy); err != nil {
		t.Fatalf("renderInfoPlain legacy: %v", err)
	}
	assertPlainRow(t, plain.String(), "Sandbox", "legacy")
	assertPlainRow(t, plain.String(), "Sandbox migration", "yeet service set api --sandbox=on (or --sandbox=off)")
	if !strings.Contains(plain.String(), "yeet service set api --sandbox=on  (or --sandbox=off)") {
		t.Fatalf("legacy migration output lost concise two-option formatting:\n%s", plain.String())
	}
}

func TestInfoJSONPreservesTypedSandboxObject(t *testing.T) {
	want := &catchrpc.ServiceSandbox{
		State:    "off",
		ReadOnly: []catchrpc.ServiceSandboxExposure{{Source: "/input", Destination: "/input"}},
		Writable: []catchrpc.ServiceSandboxExposure{{Source: "/cache", Destination: "/var/cache/app"}},
	}
	out := infoOutput{
		Service: "api",
		Host:    "host-a",
		Server:  catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{Sandbox: want}},
	}
	var encoded bytes.Buffer
	if err := encodeInfoOutput(&encoded, "json", out); err != nil {
		t.Fatalf("encodeInfoOutput: %v", err)
	}
	var decoded infoOutput
	if err := json.Unmarshal(encoded.Bytes(), &decoded); err != nil {
		t.Fatalf("decode info JSON: %v", err)
	}
	if !reflect.DeepEqual(decoded.Server.Info.Sandbox, want) {
		t.Fatalf("decoded sandbox = %#v, want typed object %#v", decoded.Server.Info.Sandbox, want)
	}
	for _, fragment := range []string{`"sandbox":{"state":"off"`, `"source":"/input"`, `"destination":"/var/cache/app"`} {
		if !strings.Contains(encoded.String(), fragment) {
			t.Fatalf("JSON = %s, want %s", encoded.String(), fragment)
		}
	}
}

func TestFormatManagedServiceIdentityInfo(t *testing.T) {
	got := formatServiceIdentityInfo(&catchrpc.ServiceIdentity{
		RequestedUser: "yeet-svc", RequestedGroup: "yeet-svc", UID: 997, GID: 997, Class: "managed",
	})
	if want := "yeet-svc:yeet-svc (system UID 997, GID 997; managed)"; got != want {
		t.Fatalf("formatServiceIdentityInfo = %q, want %q", got, want)
	}
}

func TestFormatVMMIsolation(t *testing.T) {
	tests := map[string]string{
		"jailer":                 "jailer",
		"jailer-pending-restart": "jailer (pending restart)",
	}
	for input, want := range tests {
		if got := formatVMMIsolation(input); got != want {
			t.Fatalf("formatVMMIsolation(%q) = %q, want %q", input, got, want)
		}
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

func TestIsInfoJSONFormat(t *testing.T) {
	tests := []struct {
		format string
		want   bool
	}{
		{format: "json", want: true},
		{format: "json-pretty", want: true},
		{format: "plain"},
		{format: "text"},
	}

	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			if got := isInfoJSONFormat(tt.format); got != tt.want {
				t.Fatalf("isInfoJSONFormat(%q) = %v, want %v", tt.format, got, tt.want)
			}
		})
	}
}

func TestNewInfoOutputIncludesHostInfoOnlyWhenAvailable(t *testing.T) {
	hostInfo := serverInfo{Version: "v0.2.3", GOOS: "linux", GOARCH: "arm64"}
	client := clientInfo{Found: true}
	server := catchrpc.ServiceInfoResponse{Found: true}

	withHost := newInfoOutput("svc", "host-a", hostInfo, nil, client, server)
	if withHost.HostInfo == nil || withHost.HostInfo.Version != "v0.2.3" {
		t.Fatalf("HostInfo = %#v, want populated host info", withHost.HostInfo)
	}

	withoutHost := newInfoOutput("svc", "host-a", hostInfo, errors.New("offline"), client, server)
	if withoutHost.HostInfo != nil {
		t.Fatalf("HostInfo = %#v, want nil when host info failed", withoutHost.HostInfo)
	}
}

func TestEncodeInfoOutputFormatsJSON(t *testing.T) {
	out := infoOutput{
		Service: "svc",
		Host:    "host-a",
		Client:  clientInfo{Found: true},
		Server: catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
			Network: catchrpc.ServiceNetwork{ISO: &catchrpc.ServiceISO{
				Modes: []string{"iso"}, State: "ready", Namespace: "yeet-0123456789-ns",
			}},
		}},
	}

	var compact bytes.Buffer
	if err := encodeInfoOutput(&compact, "json", out); err != nil {
		t.Fatalf("encodeInfoOutput compact error: %v", err)
	}
	var decoded infoOutput
	if err := json.Unmarshal(compact.Bytes(), &decoded); err != nil {
		t.Fatalf("compact JSON did not decode: %v\n%s", err, compact.String())
	}
	if decoded.Service != "svc" || decoded.Host != "host-a" {
		t.Fatalf("decoded = %#v, want service and host", decoded)
	}
	if decoded.Server.Info.Network.ISO == nil ||
		decoded.Server.Info.Network.ISO.Namespace != "yeet-0123456789-ns" {
		t.Fatalf("decoded isolated-network info = %#v, want namespace", decoded.Server.Info.Network.ISO)
	}
	if !bytes.Contains(compact.Bytes(), []byte(`"namespace":"yeet-0123456789-ns"`)) {
		t.Fatalf("compact JSON omitted isolated-network namespace: %s", compact.String())
	}

	var pretty bytes.Buffer
	if err := encodeInfoOutput(&pretty, "json-pretty", out); err != nil {
		t.Fatalf("encodeInfoOutput pretty error: %v", err)
	}
	if !strings.Contains(pretty.String(), "\n  \"service\": \"svc\"") {
		t.Fatalf("pretty output = %q, want indented JSON", pretty.String())
	}
}

func TestHandleInfoCommandWithoutServiceRendersHostInfo(t *testing.T) {
	oldService := serviceOverride
	oldPrefs := loadedPrefs
	oldHostOverride := hostOverride
	oldHostOverrideSet := hostOverrideSet
	oldHostOverrideHard := hostOverrideHard
	oldFetchHostInfo := fetchInfoHostInfoFn
	oldFetchServiceInfo := fetchInfoServiceInfoFn
	oldFetchStatus := fetchStatusForHostFn
	t.Cleanup(func() {
		serviceOverride = oldService
		loadedPrefs = oldPrefs
		hostOverride = oldHostOverride
		hostOverrideSet = oldHostOverrideSet
		hostOverrideHard = oldHostOverrideHard
		fetchInfoHostInfoFn = oldFetchHostInfo
		fetchInfoServiceInfoFn = oldFetchServiceInfo
		fetchStatusForHostFn = oldFetchStatus
	})

	serviceOverride = ""
	loadedPrefs.DefaultHost = "yeet-lab"
	resetHostOverride()

	fetchInfoHostInfoFn = func(ctx context.Context, host string) (serverInfo, error) {
		if host != "yeet-lab" {
			t.Fatalf("Info called with host=%q, want yeet-lab", host)
		}
		return serverInfo{
			Version:     "v0.9.0",
			GOOS:        "linux",
			GOARCH:      "amd64",
			RootDir:     "/flash/yeet/data",
			ServicesDir: "/flash/yeet/services",
		}, nil
	}
	fetchInfoServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		if host != "yeet-lab" || service != catchServiceName {
			t.Fatalf("ServiceInfo called with host=%q service=%q, want yeet-lab/%s", host, service, catchServiceName)
		}
		return catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				Paths: catchrpc.ServicePaths{
					Root:           "/flash/yeet/services/catch",
					ServiceRoot:    "/flash/yeet/services/catch",
					ServiceRootZFS: "flash/yeet/services/catch",
				},
			},
		}, nil
	}
	fetchStatusForHostFn = func(ctx context.Context, host string, flags cli.StatusFlags) ([]statusService, error) {
		if host != "yeet-lab" {
			t.Fatalf("status called with host=%q, want yeet-lab", host)
		}
		if flags.Format != "" {
			t.Fatalf("status flags = %#v, want zero value", flags)
		}
		return []statusService{
			{ServiceName: "app", ServiceType: "docker", Components: []statusComponent{{Status: "running"}}},
			{ServiceName: "db", ServiceType: "docker", Components: []statusComponent{{Status: "stopped"}}},
			{ServiceName: "worker", ServiceType: "docker", Components: []statusComponent{{Status: "running"}, {Status: "failed"}}},
			{ServiceName: "devbox", ServiceType: serviceTypeVM, Components: []statusComponent{{Status: "running"}}},
		}, nil
	}

	out, err := captureSvcStdout(t, func() error {
		return handleInfoCommand(context.Background(), nil, nil)
	})
	if err != nil {
		t.Fatalf("handleInfoCommand returned error: %v", err)
	}
	assertPlainRow(t, out, "Host", "yeet-lab")
	assertPlainRow(t, out, "Catch", "v0.9.0 (linux/amd64)")
	assertPlainRow(t, out, "Data dir", "/flash/yeet/data")
	assertPlainRow(t, out, "Services root", "/flash/yeet/services")
	assertPlainRow(t, out, "Catch root", "/flash/yeet/services/catch (zfs flash/yeet/services/catch)")
	assertPlainRow(t, out, "Services", "4 total, 2 running, 1 stopped, 1 unhealthy")
	assertPlainRow(t, out, "VMs", "1 total, 1 running, 0 stopped, 0 unhealthy")
	if strings.Contains(out, "\nService\n") {
		t.Fatalf("host info should not render service-specific section:\n%s", out)
	}
}

func TestRenderHostInfoPlainIncludesStorageAndInventory(t *testing.T) {
	out := hostInfoOutput{
		Host: "host-a",
		HostInfo: &serverInfo{
			Version:     "v0.9.0",
			GOOS:        "linux",
			GOARCH:      "amd64",
			RootDir:     "/srv/yeet-data",
			ServicesDir: "/srv/yeet-services",
		},
		CatchService: &catchrpc.ServiceInfoResponse{
			Found: true,
			Info: catchrpc.ServiceInfo{
				Paths: catchrpc.ServicePaths{
					ServiceRoot:    "/srv/yeet-services/catch",
					ServiceRootZFS: "tank/yeet/services/catch",
				},
			},
		},
		Inventory: hostInfoInventory{
			Services: hostInventoryCounts{Total: 2, Running: 1, Stopped: 1},
			VMs:      hostInventoryCounts{Total: 1, Running: 1},
		},
	}

	var rendered bytes.Buffer
	if err := renderHostInfoPlain(&rendered, out); err != nil {
		t.Fatalf("renderHostInfoPlain: %v", err)
	}
	text := rendered.String()
	assertPlainRow(t, text, "Host", "host-a")
	assertPlainRow(t, text, "Data dir", "/srv/yeet-data")
	assertPlainRow(t, text, "Services root", "/srv/yeet-services")
	assertPlainRow(t, text, "Catch root", "/srv/yeet-services/catch (zfs tank/yeet/services/catch)")
	assertPlainRow(t, text, "Services", "2 total, 1 running, 1 stopped, 0 unhealthy")
	assertPlainRow(t, text, "VMs", "1 total, 1 running, 0 stopped, 0 unhealthy")
}

func TestInfoCatchRendersConfiguredISOPool(t *testing.T) {
	out := hostInfoOutput{
		Host: "host-a",
		HostInfo: &serverInfo{
			Version: "v0.9.0",
			ISO: catchrpc.ISOPoolSummary{
				Prefix:       "172.30.0.0/16",
				Source:       "automatic",
				Allocator:    1,
				Policy:       1,
				LinksUsed:    3,
				ProjectsUsed: 2,
				Reserved:     1,
				Active:       2,
				Quarantined:  1,
				Tombstoned:   1,
				Conflict:     "ISO pool aggregate route missing",
			},
		},
	}
	var rendered bytes.Buffer
	if err := renderHostInfoPlain(&rendered, out); err != nil {
		t.Fatal(err)
	}
	text := rendered.String()
	assertPlainRow(t, text, "Pool", "172.30.0.0/16")
	assertPlainRow(t, text, "Source", "automatic")
	assertPlainRow(t, text, "Version", "allocator 1, policy 1")
	assertPlainRow(t, text, "Capacity", "links 3/8192, projects 2/1024")
	assertPlainRow(t, text, "State", "active 2, reserved 1, quarantined 1, tombstoned 1")
	assertPlainRow(t, text, "Conflict", "isolated network pool aggregate route missing")
	if !strings.Contains(text, "Isolated network\n") || strings.Contains(text, "ISO") {
		t.Fatalf("host info uses inconsistent isolation terminology:\n%s", text)
	}
}

func TestInfoCatchZeroISOSummaryPreservesJSONCompatibility(t *testing.T) {
	raw, err := json.Marshal(serverInfo{Version: "v0.9.0"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"iso"`) {
		t.Fatalf("serverInfo JSON = %s, want omitted zero ISO summary", raw)
	}
}

func TestBuildHostInventoryCountsServicesAndVMs(t *testing.T) {
	got := buildHostInventory([]statusService{
		{ServiceName: "app", ServiceType: "docker", Components: []statusComponent{{Status: "running"}}},
		{ServiceName: "db", ServiceType: "docker", Components: []statusComponent{{Status: "stopped"}}},
		{ServiceName: "worker", ServiceType: "docker", Components: []statusComponent{{Status: "running"}, {Status: "failed"}}},
		{ServiceName: "devbox", ServiceType: serviceTypeVM, Components: []statusComponent{{Status: "running"}}},
		{ServiceName: "broken-vm", ServiceType: serviceTypeVM},
	})
	want := hostInfoInventory{
		Services: hostInventoryCounts{Total: 5, Running: 2, Stopped: 1, Unhealthy: 2},
		VMs:      hostInventoryCounts{Total: 2, Running: 1, Unhealthy: 1},
	}
	if got != want {
		t.Fatalf("inventory = %#v, want %#v", got, want)
	}
}

func TestHandleInfoCommandErrorsWhenServiceIsMissingOnCatch(t *testing.T) {
	oldService := serviceOverride
	oldPrefs := loadedPrefs
	oldHostOverride := hostOverride
	oldHostOverrideSet := hostOverrideSet
	oldHostOverrideHard := hostOverrideHard
	oldFetchHostInfo := fetchInfoHostInfoFn
	oldFetchServiceInfo := fetchInfoServiceInfoFn
	t.Cleanup(func() {
		serviceOverride = oldService
		loadedPrefs = oldPrefs
		hostOverride = oldHostOverride
		hostOverrideSet = oldHostOverrideSet
		hostOverrideHard = oldHostOverrideHard
		fetchInfoHostInfoFn = oldFetchHostInfo
		fetchInfoServiceInfoFn = oldFetchServiceInfo
	})

	serviceOverride = "notreal"
	loadedPrefs.DefaultHost = "yeet-lab"
	resetHostOverride()

	fetchInfoHostInfoFn = func(context.Context, string) (serverInfo, error) {
		t.Fatal("host info should not be fetched for a missing service")
		return serverInfo{}, nil
	}
	fetchInfoServiceInfoFn = func(ctx context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		if host != "yeet-lab" || service != "notreal" {
			t.Fatalf("ServiceInfo called with host=%q service=%q, want yeet-lab/notreal", host, service)
		}
		return catchrpc.ServiceInfoResponse{Found: false, Message: "service not found"}, nil
	}

	err := handleInfoCommand(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected missing service error")
	}
	if got := err.Error(); got != `service "notreal" not found on yeet-lab` {
		t.Fatalf("error = %q, want missing service on host", got)
	}
}

func TestBuildClientInfo(t *testing.T) {
	dir := t.TempDir()
	cfg := &ProjectConfig{}
	cfg.SetServiceEntry(ServiceEntry{
		Name:        "svc-a",
		Host:        "host-a",
		Type:        serviceTypeRun,
		Payload:     "ghcr.io/example/app:latest",
		PayloadKind: "remote-image",
		EnvFile:     ".env",
		Schedule:    "@hourly",
		Args:        []string{"--port", "8080"},
		Ports:       []string{"80:80"},
	})
	loc := &projectConfigLocation{Path: filepath.Join(dir, projectConfigName), Dir: dir, Config: cfg}

	got := buildClientInfo(loc, "svc-a", "host-a", serverInfo{}, nil)
	if !got.Found {
		t.Fatalf("Found = false, want true: %#v", got)
	}
	if got.ConfigFile != loc.Path || got.ConfigDir != dir {
		t.Fatalf("config paths = %q %q, want %q %q", got.ConfigFile, got.ConfigDir, loc.Path, dir)
	}
	if got.Entry == nil || got.Entry.Name != "svc-a" || got.Entry.Host != "host-a" || got.Entry.Type != serviceTypeRun || got.Entry.PayloadKind != "remote-image" {
		t.Fatalf("Entry = %#v, want saved service entry", got.Entry)
	}
	if got.Payload == nil || got.Payload.Kind != "image" || !got.Payload.ImageRef {
		t.Fatalf("Payload = %#v, want image payload info", got.Payload)
	}
	if got.Entry == nil || len(got.Entry.Ports) != 1 || got.Entry.Ports[0] != "80:80" {
		t.Fatalf("Entry ports = %#v, want saved publish ports", got.Entry)
	}

	missingConfig := buildClientInfo(nil, "svc-a", "host-a", serverInfo{}, nil)
	if missingConfig.Found || missingConfig.Message != "no yeet.toml found" {
		t.Fatalf("missing config info = %#v", missingConfig)
	}

	missingEntry := buildClientInfo(loc, "svc-b", "host-a", serverInfo{}, nil)
	if missingEntry.Found || missingEntry.Message != "no entry for svc-b@host-a" {
		t.Fatalf("missing entry info = %#v", missingEntry)
	}
}

func TestBuildClientInfoUsesLocalImagePayloadKind(t *testing.T) {
	dir := t.TempDir()
	cfg := &ProjectConfig{}
	cfg.SetServiceEntry(ServiceEntry{
		Name:        "svc-a",
		Host:        "host-a",
		Type:        serviceTypeRun,
		Payload:     "alpine",
		PayloadKind: "local-image",
	})
	loc := &projectConfigLocation{Path: filepath.Join(dir, projectConfigName), Dir: dir, Config: cfg}

	got := buildClientInfo(loc, "svc-a", "host-a", serverInfo{}, nil)
	if got.Payload == nil {
		t.Fatal("Payload = nil, want local image payload info")
	}
	if got.Payload.Kind != "local image" || !got.Payload.ImageRef || got.Payload.ResolveErr != "" {
		t.Fatalf("Payload = %#v, want local image without resolve error", got.Payload)
	}
}

func TestBuildClientInfoUsesVMPayloadKind(t *testing.T) {
	dir := t.TempDir()
	cfg := &ProjectConfig{}
	cfg.SetServiceEntry(ServiceEntry{
		Name:        "devbox",
		Host:        "yeet-lab",
		Type:        serviceTypeVM,
		Payload:     "vm://ubuntu/26.04",
		PayloadKind: serviceTypeVM,
	})
	loc := &projectConfigLocation{Path: filepath.Join(dir, projectConfigName), Dir: dir, Config: cfg}

	got := buildClientInfo(loc, "devbox", "yeet-lab", serverInfo{}, nil)
	if got.Payload == nil {
		t.Fatal("Payload = nil, want VM payload info")
	}
	if got.Payload.Kind != "vm" || got.Payload.ImageRef || got.Payload.ResolveErr != "" {
		t.Fatalf("Payload = %#v, want VM payload without local path resolution", got.Payload)
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
		{"vm", "VM"},
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

func TestInfoServiceNetworkRowsRenderHealthyNativeIsolation(t *testing.T) {
	rows := serviceNetworkRows(catchrpc.ServiceNetwork{
		Modes: []string{"iso"},
		ISO: &catchrpc.ServiceISO{
			Modes: []string{"iso"}, State: "ready", PublicEgress: true, DNS: "public-only",
			Namespace:  "yeet-0123456789-ns",
			Components: []catchrpc.ServiceISOComponent{{Name: "service", IP: "172.16.0.6"}},
		},
	})
	assertInfoRows(t, rows, []infoRow{
		{Label: "Network modes", Value: "iso"},
		{Label: "IP", Value: "172.16.0.6"},
		{Label: "Namespace", Value: "yeet-0123456789-ns"},
		{Label: "Egress", Value: "public IPv4 via NAT"},
		{Label: "DNS", Value: "public-only"},
	})
}

func TestInfoServiceNetworkRowsRenderComposeEndpointsAndAbnormalState(t *testing.T) {
	rows := serviceNetworkRows(catchrpc.ServiceNetwork{
		Modes: []string{"host"}, Desired: &catchrpc.ServiceNetworkSettings{Modes: []string{"iso"}},
		ISO: &catchrpc.ServiceISO{
			Modes: []string{"iso"}, State: "quarantined", PublicEgress: true, DNS: "public-only",
			Namespace: "yeet-fedcba9876-ns",
			Components: []catchrpc.ServiceISOComponent{
				{Name: "api", IP: "172.30.128.2"},
				{Name: "worker", IP: "172.30.128.3"},
			},
			LastError: "service MYISOAPP ISO network firewall digest mismatch",
		},
	})
	assertInfoRows(t, rows, []infoRow{
		{Label: "Network modes", Value: "host"},
		{Label: "Desired network modes", Value: "iso"},
		{Label: "Network state", Value: "quarantined"},
		{Label: "IP (api)", Value: "172.30.128.2"},
		{Label: "IP (worker)", Value: "172.30.128.3"},
		{Label: "Namespace", Value: "yeet-fedcba9876-ns"},
		{Label: "Egress", Value: "public IPv4 via NAT"},
		{Label: "DNS", Value: "public-only"},
		{Label: "Network error", Value: "service MYISOAPP isolated network firewall digest mismatch"},
	})
}

func TestInfoVMNetworkRowsRenderIsolatedPeerIP(t *testing.T) {
	section := renderVMNetworkSection(catchrpc.ServiceInfo{
		Network: catchrpc.ServiceNetwork{
			Modes: []string{"iso"},
			ISO: &catchrpc.ServiceISO{
				Modes: []string{"iso"}, State: "ready", PublicEgress: true, DNS: "public-only",
				Components: []catchrpc.ServiceISOComponent{{Name: "vm", IP: "172.16.0.14"}},
			},
		},
	})
	assertInfoRows(t, section.Rows, []infoRow{
		{Label: "Network modes", Value: "iso"},
		{Label: "IP", Value: "172.16.0.14"},
		{Label: "Egress", Value: "public IPv4 via NAT"},
		{Label: "DNS", Value: "public-only"},
	})
}

func TestInfoNetworkRowsShowEffectiveModesAndDesiredDrift(t *testing.T) {
	tests := []struct {
		name string
		net  catchrpc.ServiceNetwork
		want []infoRow
	}{
		{
			name: "implicit host",
			net:  catchrpc.ServiceNetwork{},
			want: []infoRow{{Label: "Network modes", Value: "host"}},
		},
		{
			name: "matching desired modes stay concise",
			net: catchrpc.ServiceNetwork{
				Modes:   []string{"svc", "ts"},
				Desired: &catchrpc.ServiceNetworkSettings{Modes: []string{"svc", "ts"}},
			},
			want: []infoRow{{Label: "Network modes", Value: "svc,ts"}},
		},
		{
			name: "failed transition shows desired and lifecycle",
			net: catchrpc.ServiceNetwork{
				Modes:   []string{"host"},
				Desired: &catchrpc.ServiceNetworkSettings{Modes: []string{"iso"}},
				ISO: &catchrpc.ServiceISO{
					Modes: []string{"iso"}, State: "quarantined", LastError: "firewall digest mismatch",
				},
			},
			want: []infoRow{
				{Label: "Network modes", Value: "host"},
				{Label: "Desired network modes", Value: "iso"},
				{Label: "Network state", Value: "quarantined"},
				{Label: "Network error", Value: "firewall digest mismatch"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertInfoRows(t, serviceNetworkRows(tt.net), tt.want)
		})
	}
}

func TestInfoNetworkRowsDistinguishDesiredSettingsFromRuntimeAttachments(t *testing.T) {
	net := catchrpc.ServiceNetwork{
		Modes: []string{"lan", "ts"},
		Desired: &catchrpc.ServiceNetworkSettings{
			Modes:         []string{"lan", "ts"},
			TSVersion:     "1.96.1",
			TSExitNode:    "exit.example",
			TSTags:        []string{"tag:app", "tag:worker"},
			MacvlanParent: "vmbr0",
			MacvlanVLAN:   42,
			MacvlanMAC:    "02:00:00:00:00:42",
		},
		Tailscale: &catchrpc.ServiceTailscale{
			Interface: "yts-app", StableID: "node-runtime", Version: "1.94.0",
			ExitNode: "old-exit.example", Tags: []string{"tag:old"},
		},
		Macvlan: &catchrpc.ServiceMacvlan{
			Interface: "ymv-app", Parent: "eno1", VLAN: 20, Mac: "02:00:00:00:00:20",
		},
	}

	assertInfoRows(t, serviceNetworkRows(net), []infoRow{
		{Label: "Network modes", Value: "lan,ts"},
		{Label: "Desired Tailscale", Value: "ver 1.96.1, tags: tag:app, tag:worker, exit: exit.example"},
		{Label: "Desired macvlan", Value: "parent vmbr0, vlan 42, mac 02:00:00:00:00:42"},
		{Label: "Tailscale", Value: "yts-app (ver 1.94.0), tags: tag:old, exit: old-exit.example"},
		{Label: "Macvlan", Value: "ymv-app, parent eno1, vlan 20, mac 02:00:00:00:00:20"},
	})
}

func TestAuthKeyRedactionAcrossErrorPreviewRPCInfoAndSavedConfig(t *testing.T) {
	const secret = "tskey-auth-task6-must-not-leak"
	runArgs := []string{"--net=ts", "--ts-tags=tag:app", "--ts-auth-key=" + secret}

	desired, authKeySet, err := requestedRunNetworkSettings(runArgs)
	if err != nil {
		t.Fatalf("requestedRunNetworkSettings: %v", err)
	}
	if !authKeySet {
		t.Fatal("requestedRunNetworkSettings did not preserve auth-key presence")
	}

	oldFetch := fetchRunChangeServiceInfoFn
	t.Cleanup(func() { fetchRunChangeServiceInfoFn = oldFetch })
	fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
			Network: catchrpc.ServiceNetwork{Modes: []string{"ts"}, Desired: &desired},
		}}, nil
	}
	guardErr := rejectExistingRunNetworkChange(context.Background(), ServiceEntry{Name: "app", Host: "catch.example"}, runArgs)
	if guardErr == nil {
		t.Fatal("rejectExistingRunNetworkChange accepted an auth key for an existing service")
	}

	preview := runDraftCommandPreview(RunDraft{
		Service: "app", Host: "catch.example", Payload: "ghcr.io/example/app:latest",
		RunArgsSet: true, RunArgs: runArgs,
	})

	flags, _, err := cli.ParseServiceSet(append([]string{"app"}, runArgs...))
	if err != nil {
		t.Fatalf("ParseServiceSet: %v", err)
	}
	entry := ServiceEntry{Args: []string{"--net=host", "--ts-auth-key=stale-secret"}}
	if err := applyServiceSetConfigFlags(&entry, flags); err != nil {
		t.Fatalf("applyServiceSetConfigFlags: %v", err)
	}

	server := catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
		Network: catchrpc.ServiceNetwork{Modes: []string{"ts"}, Desired: &desired},
	}}
	rpcJSON, err := json.Marshal(server)
	if err != nil {
		t.Fatalf("marshal service info: %v", err)
	}
	var plain bytes.Buffer
	if err := renderInfoPlain(&plain, "app", "catch.example", nil, serverInfo{}, clientInfo{}, server); err != nil {
		t.Fatalf("renderInfoPlain: %v", err)
	}

	surfaces := map[string]string{
		"error":        guardErr.Error(),
		"preview":      preview,
		"RPC JSON":     string(rpcJSON),
		"plain info":   plain.String(),
		"saved config": strings.Join(entry.Args, " "),
	}
	for name, surface := range surfaces {
		if strings.Contains(surface, secret) {
			t.Fatalf("%s leaked auth key: %s", name, surface)
		}
	}
	if !strings.Contains(preview, "--ts-auth-key=<hidden>") {
		t.Fatalf("preview = %q, want hidden auth-key marker", preview)
	}
	if strings.Contains(surfaces["saved config"], "--ts-auth-key") {
		t.Fatalf("saved config retained auth-key flag: %q", surfaces["saved config"])
	}
	if !strings.Contains(string(rpcJSON), `"desired":{"modes":["ts"],"tsTags":["tag:app"]}`) {
		t.Fatalf("RPC JSON = %s, want non-secret desired settings", rpcJSON)
	}
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

func TestInfoClientConfigRows(t *testing.T) {
	got := clientConfigRows(clientInfo{})
	assertInfoRows(t, got, nil)

	got = clientConfigRows(clientInfo{Message: "no entry"})
	assertInfoRows(t, got, nil)

	got = clientConfigRows(clientInfo{
		Found: true,
		Entry: &clientServiceEntry{Host: "host-a", Schedule: "0 3 * * *"},
	})
	assertInfoRows(t, got, []infoRow{
		{Label: "Saved host", Value: "host-a"},
		{Label: "Schedule", Value: "0 3 * * *"},
	})
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
		Ports:    []string{"80:80"},
		Schedule: "@hourly",
	}

	got := clientEntryMetadataRows(entry)
	want := []infoRow{
		{Label: "Env file", Value: ".env"},
		{Label: "Payload args", Value: "--port 8080"},
		{Label: "Published ports", Value: "80:80"},
		{Label: "Schedule", Value: "@hourly"},
	}
	assertInfoRows(t, got, want)
}

func TestInfoRendersScheduledClientTypeFromSchedule(t *testing.T) {
	section := renderServiceSection("backup", "host-a", clientInfo{
		Found: true,
		Entry: &clientServiceEntry{Host: "host-a", Schedule: "0 3 * * *"},
	}, catchrpc.ServiceInfoResponse{})
	assertInfoRows(t, section.Rows, []infoRow{
		{Label: "Name", Value: "backup"},
		{Label: "Host", Value: "host-a"},
		{Label: "Type", Value: "cron service (local config)"},
		{Label: "Status", Value: "unknown"},
	})
}

func TestInfoRendersUnknownClientTypeForUnscheduledBlankType(t *testing.T) {
	section := renderServiceSection("api", "host-a", clientInfo{
		Found: true,
		Entry: &clientServiceEntry{Host: "host-a"},
	}, catchrpc.ServiceInfoResponse{})
	assertInfoRows(t, section.Rows, []infoRow{
		{Label: "Name", Value: "api"},
		{Label: "Host", Value: "host-a"},
		{Label: "Type", Value: "unknown"},
		{Label: "Status", Value: "unknown"},
	})
}

func TestInfoReportsScheduledServerConfigDriftWithoutInventingSchedule(t *testing.T) {
	var out strings.Builder
	err := renderInfoPlain(&out, "backup", "host-a", nil, serverInfo{}, clientInfo{
		Found: true,
		Entry: &clientServiceEntry{Host: "host-a", Payload: "job.sh"},
	}, catchrpc.ServiceInfoResponse{
		Found: true,
		Info:  catchrpc.ServiceInfo{DataType: "cron"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, `schedule = "<five-field expression>"`) {
		t.Fatalf("info output = %q, want schedule drift guidance", got)
	}
	if strings.Contains(got, "yeet cron") {
		t.Fatalf("info output suggests removed command: %q", got)
	}
}

func TestInfoRenderServerSection(t *testing.T) {
	got := renderServerSection(catchrpc.ServiceInfoResponse{})
	if got.Title != "Server (catch)" {
		t.Fatalf("Title = %q, want Server (catch)", got.Title)
	}
	assertInfoRows(t, got.Rows, []infoRow{{Label: "Status", Value: "not installed"}})

	got = renderServerSection(catchrpc.ServiceInfoResponse{
		Found: true,
		Info: catchrpc.ServiceInfo{
			DataType:         "python",
			ServiceType:      "docker-compose",
			Generation:       2,
			LatestGeneration: 3,
			Staged:           true,
			Paths:            catchrpc.ServicePaths{Root: "/srv/yeet/services/app"},
		},
	})
	assertInfoRows(t, got.Rows, []infoRow{
		{Label: "Backend", Value: "Docker Compose"},
		{Label: "Generation", Value: "2 (latest 3)"},
		{Label: "Staged changes", Value: "yes"},
		{Label: "Root dir", Value: "/srv/yeet/services/app"},
	})

	got = renderServerSection(catchrpc.ServiceInfoResponse{
		Found: true,
		Info: catchrpc.ServiceInfo{
			DataType:    "python",
			ServiceType: "docker-compose",
			Generation:  4,
			Paths:       catchrpc.ServicePaths{Root: "/srv/yeet/services/api"},
		},
	})
	assertInfoRows(t, got.Rows, []infoRow{
		{Label: "Backend", Value: "Docker Compose"},
		{Label: "Generation", Value: "4"},
		{Label: "Root dir", Value: "/srv/yeet/services/api"},
	})

	got = renderServerSection(catchrpc.ServiceInfoResponse{
		Found: true,
		Info: catchrpc.ServiceInfo{
			DataType:         "binary",
			ServiceType:      "systemd",
			Generation:       13,
			LatestGeneration: 13,
			Paths:            catchrpc.ServicePaths{Root: "/srv/yeet/services/worker"},
		},
	})
	assertInfoRows(t, got.Rows, []infoRow{
		{Label: "Root dir", Value: "/srv/yeet/services/worker"},
	})

	got = renderServerSection(catchrpc.ServiceInfoResponse{
		Found: true,
		Info: catchrpc.ServiceInfo{
			DataType:    "docker",
			ServiceType: "docker-compose",
			Paths:       catchrpc.ServicePaths{Root: "/srv/yeet/services/web"},
		},
	})
	assertInfoRows(t, got.Rows, []infoRow{
		{Label: "Root dir", Value: "/srv/yeet/services/web"},
	})
}

func TestInfoRenderNetworkSection(t *testing.T) {
	got := renderNetworkSection(catchrpc.ServiceInfoResponse{})
	if got.Title != "Network" || got.Rows != nil {
		t.Fatalf("renderNetworkSection not found = %#v, want empty Network section", got)
	}

	got = renderNetworkSection(catchrpc.ServiceInfoResponse{
		Found: true,
		Info: catchrpc.ServiceInfo{
			Network: catchrpc.ServiceNetwork{
				SvcIP:        "10.0.0.2",
				IPWarning:    "configured IP not present in guest",
				PortsPresent: true,
				Ports: []catchrpc.ServicePort{
					{HostPort: 80, ContainerPort: 80, Protocol: "tcp"},
					{HostIP: "127.0.0.1", HostPort: 8443, ContainerPort: 443, Protocol: "tcp"},
				},
				Tailscale: &catchrpc.ServiceTailscale{
					Interface: "tailscale0",
				},
				Macvlan: &catchrpc.ServiceMacvlan{
					Interface: "macvlan0",
					Parent:    "eth0",
				},
			},
		},
	})
	assertInfoRows(t, got.Rows, []infoRow{
		{Label: "Network modes", Value: "lan,svc,ts"},
		{Label: "IPs", Value: ""},
		{Label: "  service", Value: "10.0.0.2"},
		{Label: "IP warning", Value: "configured IP not present in guest"},
		{Label: "Ports", Value: "80/tcp -> 80/tcp, 127.0.0.1:8443/tcp -> 443/tcp"},
		{Label: "Tailscale", Value: "tailscale0"},
		{Label: "Macvlan", Value: "macvlan0, parent eth0"},
	})

	got = renderNetworkSection(catchrpc.ServiceInfoResponse{
		Found: true,
		Info: catchrpc.ServiceInfo{
			Network: catchrpc.ServiceNetwork{
				IPs: []catchrpc.ServiceIP{
					{Label: "tailscale", IP: "100.88.145.8", Interface: "tailscale0"},
					{Label: "host", IP: "10.0.4.2", Interface: "vmbr0"},
					{Label: "host", IP: "192.168.100.1", Interface: "yeet0"},
				},
				PortsPresent: true,
			},
		},
	})
	assertInfoRows(t, got.Rows, []infoRow{{Label: "Network modes", Value: "host"}})

	got = renderNetworkSection(catchrpc.ServiceInfoResponse{
		Found: true,
		Info: catchrpc.ServiceInfo{
			Network: catchrpc.ServiceNetwork{
				PortsPresent: true,
			},
		},
	})
	assertInfoRows(t, got.Rows, []infoRow{{Label: "Network modes", Value: "host"}})
}

func TestInfoRenderNetworkSectionUsesVMContext(t *testing.T) {
	got := renderNetworkSection(catchrpc.ServiceInfoResponse{
		Found: true,
		Info: catchrpc.ServiceInfo{
			DataType: "vm",
			VM: &catchrpc.ServiceVM{
				Networks: []catchrpc.ServiceVMNetwork{
					{Mode: "svc", Interface: "eth0", IP: "192.168.100.12", Source: "config"},
					{Mode: "lan", Interface: "eth1", IP: "10.0.4.200", Source: "agent"},
				},
			},
			Network: catchrpc.ServiceNetwork{
				SvcIP: "192.168.100.12",
				IPs: []catchrpc.ServiceIP{
					{Label: "service", IP: "192.168.100.12", Interface: "eth0", Source: "config"},
					{Label: "lan", IP: "10.0.4.200", Interface: "eth1", Source: "agent"},
				},
				PortsPresent: true,
				Ports:        nil,
			},
		},
	})

	assertInfoRows(t, got.Rows, []infoRow{
		{Label: "IPs", Value: ""},
		{Label: "  service", Value: "192.168.100.12"},
		{Label: "  lan (eth1)", Value: "10.0.4.200"},
	})
}

func TestInfoRenderRuntimeSection(t *testing.T) {
	tests := []struct {
		name    string
		service string
		server  catchrpc.ServiceInfoResponse
		want    []infoRow
	}{
		{name: "not found", service: "app", server: catchrpc.ServiceInfoResponse{}, want: nil},
		{
			name:    "status error",
			service: "app",
			server: catchrpc.ServiceInfoResponse{
				Found: true,
				Info:  catchrpc.ServiceInfo{Status: catchrpc.ServiceStatus{Error: "status unavailable"}},
			},
			want: []infoRow{{Label: "Status", Value: "status unavailable"}},
		},
		{
			name:    "unknown",
			service: "app",
			server:  catchrpc.ServiceInfoResponse{Found: true},
			want:    nil,
		},
		{
			name:    "single duplicate component",
			service: "app",
			server: catchrpc.ServiceInfoResponse{
				Found: true,
				Info: catchrpc.ServiceInfo{
					Status: catchrpc.ServiceStatus{
						Components: []catchrpc.ServiceComponentStatus{
							{Name: "app", Status: "running"},
						},
					},
				},
			},
			want: nil,
		},
		{
			name:    "single named subcomponent",
			service: "app",
			server: catchrpc.ServiceInfoResponse{
				Found: true,
				Info: catchrpc.ServiceInfo{
					Status: catchrpc.ServiceStatus{
						Components: []catchrpc.ServiceComponentStatus{
							{Name: "worker", Status: "running"},
						},
					},
				},
			},
			want: []infoRow{{Label: "worker", Value: "running"}},
		},
		{
			name:    "components",
			service: "app",
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
			got := renderRuntimeSection(tt.service, tt.server)
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

func TestInfoFormatClientServiceType(t *testing.T) {
	tests := []struct {
		entry clientServiceEntry
		want  string
	}{
		{clientServiceEntry{Schedule: "0 3 * * *"}, "cron service (local config)"},
		{clientServiceEntry{Type: serviceTypeRun}, "run service (local config)"},
		{clientServiceEntry{Type: "custom"}, "custom"},
	}

	for _, tt := range tests {
		if got := formatClientServiceType(tt.entry.Type, tt.entry.Schedule); got != tt.want {
			t.Fatalf("formatClientServiceType(%#v) = %q, want %q", tt.entry, got, tt.want)
		}
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

func assertPlainRow(t *testing.T, text, label, value string) {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		rowLabel, rowValue, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		rowLabel = strings.Join(strings.Fields(rowLabel), " ")
		rowValue = strings.Join(strings.Fields(rowValue), " ")
		if rowLabel == label && rowValue == value {
			return
		}
	}
	t.Fatalf("rendered info missing row %s=%q:\n%s", label, value, text)
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
