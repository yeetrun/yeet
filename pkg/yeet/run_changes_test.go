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
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/catchrpc"
	"github.com/yeetrun/yeet/pkg/cli"
)

type errorWriter struct {
	err error
}

func (w errorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type closeErrorReader struct {
	reader io.Reader
	err    error
}

func (r *closeErrorReader) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

func (r *closeErrorReader) Close() error {
	return r.err
}

func TestExtractEnvFileFlag(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantEnv   string
		wantArgs  []string
		wantFound bool
		wantErr   string
	}{
		{name: "empty", wantArgs: nil},
		{name: "space value", args: []string{"run", "--env-file", ".env", "--flag"}, wantEnv: ".env", wantArgs: []string{"run", "--flag"}, wantFound: true},
		{name: "equals value", args: []string{"--env-file=.prod", "run"}, wantEnv: ".prod", wantArgs: []string{"run"}, wantFound: true},
		{name: "delimiter stops parsing", args: []string{"--env-file", ".env", "--", "--env-file", "remote"}, wantEnv: ".env", wantArgs: []string{"--", "--env-file", "remote"}, wantFound: true},
		{name: "missing value", args: []string{"--env-file"}, wantErr: "requires a value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotEnv, gotArgs, gotFound, err := extractEnvFileFlag(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("extractEnvFileFlag error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("extractEnvFileFlag error: %v", err)
			}
			if gotEnv != tt.wantEnv || gotFound != tt.wantFound || strings.Join(gotArgs, ",") != strings.Join(tt.wantArgs, ",") {
				t.Fatalf("extractEnvFileFlag = env %q args %#v found %v, want env %q args %#v found %v", gotEnv, gotArgs, gotFound, tt.wantEnv, tt.wantArgs, tt.wantFound)
			}
		})
	}
}

func TestExtractServiceRootOptions(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		want      serviceRootOptions
		wantArgs  []string
		wantFound bool
		wantErr   string
	}{
		{name: "path root", args: []string{"--service-root", "/srv/apps/svc-a", "--pull"}, want: serviceRootOptions{Root: "/srv/apps/svc-a"}, wantArgs: []string{"--pull"}, wantFound: true},
		{name: "zfs root", args: []string{"--service-root=tank/apps/svc-a", "--zfs", "--pull"}, want: serviceRootOptions{Root: "tank/apps/svc-a", ZFS: true}, wantArgs: []string{"--pull"}, wantFound: true},
		{name: "zfs before root", args: []string{"--zfs", "--service-root", "tank/apps/svc-a"}, want: serviceRootOptions{Root: "tank/apps/svc-a", ZFS: true}, wantArgs: []string{}, wantFound: true},
		{name: "payload delimiter", args: []string{"--", "--service-root", "payload"}, wantArgs: []string{"--", "--service-root", "payload"}, wantFound: false},
		{name: "zfs without root", args: []string{"--zfs"}, wantErr: "--zfs requires --service-root"},
		{name: "blank root with zfs", args: []string{"--service-root=", "--zfs"}, wantErr: "--service-root requires a value"},
		{name: "space root with zfs", args: []string{"--service-root", " ", "--zfs"}, wantErr: "--service-root requires a value"},
		{name: "relative without zfs", args: []string{"--service-root", "apps/svc-a"}, wantErr: "--service-root must be absolute unless --zfs is set"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotArgs, gotFound, err := extractServiceRootOptions(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("extractServiceRootOptions error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("extractServiceRootOptions error: %v", err)
			}
			if got != tt.want || !reflect.DeepEqual(gotArgs, tt.wantArgs) || gotFound != tt.wantFound {
				t.Fatalf("extractServiceRootOptions = %#v %#v %v, want %#v %#v %v", got, gotArgs, gotFound, tt.want, tt.wantArgs, tt.wantFound)
			}
		})
	}
}

func TestRunArgsWithServiceRootOptions(t *testing.T) {
	got := runArgsWithServiceRootOptions([]string{"--pull"}, serviceRootOptions{Root: "tank/apps/svc-a", ZFS: true})
	want := []string{"--service-root=tank/apps/svc-a", "--zfs", "--pull"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runArgsWithServiceRootOptions = %#v, want %#v", got, want)
	}
}

func TestDetectRunChangesServiceRootOnly(t *testing.T) {
	oldHashes := fetchRemoteArtifactHashesFn
	defer func() { fetchRemoteArtifactHashesFn = oldHashes }()

	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{}, true, nil
	}
	summary, err := detectRunChanges("ghcr.io/example/app:latest", []string{"--service-root=/srv/apps/new"}, "", []string{"--service-root=/srv/apps/old"})
	if err != nil {
		t.Fatalf("detectRunChanges error: %v", err)
	}
	if !summary.argsChanged || !summary.requiresRun() {
		t.Fatalf("summary = %#v, want service-root-only args change to require run", summary)
	}
}

func TestDetectRunChangesServiceRootZFSOnly(t *testing.T) {
	oldHashes := fetchRemoteArtifactHashesFn
	defer func() { fetchRemoteArtifactHashesFn = oldHashes }()

	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{}, true, nil
	}
	summary, err := detectRunChanges("ghcr.io/example/app:latest", []string{"--service-root=tank/apps/svc-a", "--zfs"}, "", []string{"--service-root=tank/apps/svc-a"})
	if err != nil {
		t.Fatalf("detectRunChanges error: %v", err)
	}
	if !summary.argsChanged || !summary.requiresRun() {
		t.Fatalf("summary = %#v, want zfs-only args change to require run", summary)
	}
}

func TestRunRejectsExistingNetworkChangesBeforeRunner(t *testing.T) {
	oldHashes := fetchRemoteArtifactHashesFn
	oldInfo := fetchRunChangeServiceInfoFn
	defer func() {
		fetchRemoteArtifactHashesFn = oldHashes
		fetchRunChangeServiceInfoFn = oldInfo
	}()

	payload := filepath.Join(t.TempDir(), "app")
	if err := os.WriteFile(payload, []byte("payload\n"), 0o755); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	hash, err := hashFileSHA256(payload)
	if err != nil {
		t.Fatalf("hash payload: %v", err)
	}
	fetchRemoteArtifactHashesFn = func(context.Context, string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{
			Found:   true,
			Payload: &catchrpc.ArtifactHash{Kind: "binary", SHA256: hash},
		}, true, nil
	}
	tests := []struct {
		name    string
		runArgs []string
		desired catchrpc.ServiceNetworkSettings
	}{
		{name: "modes", runArgs: []string{"--net=iso"}, desired: catchrpc.ServiceNetworkSettings{Modes: []string{"host"}}},
		{name: "tags", runArgs: []string{"--net=ts", "--ts-tags=tag:new"}, desired: catchrpc.ServiceNetworkSettings{Modes: []string{"ts"}, TSTags: []string{"tag:old"}}},
		{name: "version", runArgs: []string{"--net=ts", "--ts-ver=1.2.3"}, desired: catchrpc.ServiceNetworkSettings{Modes: []string{"ts"}, TSVersion: "1.2.2"}},
		{name: "exit node", runArgs: []string{"--net=ts", "--ts-exit=new"}, desired: catchrpc.ServiceNetworkSettings{Modes: []string{"ts"}, TSExitNode: "old"}},
		{name: "macvlan parent", runArgs: []string{"--net=lan", "--macvlan-parent=eno2"}, desired: catchrpc.ServiceNetworkSettings{Modes: []string{"lan"}, MacvlanParent: "eno1"}},
		{name: "macvlan vlan", runArgs: []string{"--net=lan", "--macvlan-vlan=20"}, desired: catchrpc.ServiceNetworkSettings{Modes: []string{"lan"}, MacvlanVLAN: 10}},
		{name: "macvlan mac", runArgs: []string{"--net=lan", "--macvlan-mac=02:00:00:00:00:02"}, desired: catchrpc.ServiceNetworkSettings{Modes: []string{"lan"}, MacvlanMAC: "02:00:00:00:00:01"}},
		{name: "auth key", runArgs: []string{"--net=ts", "--ts-auth-key=secret"}, desired: catchrpc.ServiceNetworkSettings{Modes: []string{"ts"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetchRunChangeServiceInfoFn = func(_ context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
				if host != "catch.example" || service != "api" {
					t.Fatalf("service info target = %s/%s, want catch.example/api", host, service)
				}
				return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{Network: catchrpc.ServiceNetwork{Desired: &tt.desired}}}, nil
			}
			runs := 0
			err := runWithChangesToWithContextRunner(
				context.Background(), io.Discard, payload, tt.runArgs, "", ServiceEntry{Name: "api", Host: "catch.example"}, false,
				func(context.Context, string, []string) error { runs++; return nil }, false,
			)
			if err == nil || !strings.Contains(err.Error(), "network changes for existing services require `yeet service set <service> ...`") {
				t.Fatalf("error = %v, want service set guidance", err)
			}
			if tt.name == "auth key" && !strings.Contains(err.Error(), "--ts-auth-key") {
				t.Fatalf("auth-key error = %v, want explicit auth-key guidance", err)
			}
			if runs != 0 {
				t.Fatalf("runner calls = %d, want 0", runs)
			}
		})
	}
}

func TestRunNetworkGuardAllowsUnchangedNetworkAndInitialDeploy(t *testing.T) {
	oldInfo := fetchRunChangeServiceInfoFn
	defer func() { fetchRunChangeServiceInfoFn = oldInfo }()

	tests := []struct {
		name    string
		runArgs []string
		remote  catchrpc.ServiceNetwork
		found   bool
	}{
		{
			name:    "desired settings normalize",
			runArgs: []string{"--pull", "--net=ts,lan", "--ts-ver=1.2.3", "--ts-exit=exit", "--ts-tags=tag:b", "--ts-tags=tag:a", "--macvlan-parent=eno1", "--macvlan-vlan=10", "--macvlan-mac=02:00:00:00:00:01", "app-arg"},
			remote: catchrpc.ServiceNetwork{Desired: &catchrpc.ServiceNetworkSettings{
				Modes: []string{"lan", "ts"}, TSVersion: "1.2.3", TSExitNode: "exit", TSTags: []string{"tag:a", "tag:b"},
				MacvlanParent: "eno1", MacvlanVLAN: 10, MacvlanMAC: "02:00:00:00:00:01",
			}},
			found: true,
		},
		{
			name:    "legacy effective fallback",
			runArgs: []string{"--net=ts", "--ts-ver=1.2.3", "--ts-exit=exit", "--ts-tags=tag:a"},
			remote:  catchrpc.ServiceNetwork{Modes: []string{"ts"}, Tailscale: &catchrpc.ServiceTailscale{Version: "1.2.3", ExitNode: "exit", Tags: []string{"tag:a"}}},
			found:   true,
		},
		{
			name:    "not found remains initial deploy",
			runArgs: []string{"--net=iso"},
			found:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
				return catchrpc.ServiceInfoResponse{Found: tt.found, Info: catchrpc.ServiceInfo{Network: tt.remote}}, nil
			}
			if err := rejectExistingRunNetworkChange(context.Background(), ServiceEntry{Name: "api", Host: "catch.example"}, tt.runArgs); err != nil {
				t.Fatalf("rejectExistingRunNetworkChange error: %v", err)
			}
		})
	}
}

func TestRunUnchangedNetworkAllowsOtherRedeploymentChanges(t *testing.T) {
	oldHashes := fetchRemoteArtifactHashesFn
	oldInfo := fetchRunChangeServiceInfoFn
	defer func() {
		fetchRemoteArtifactHashesFn = oldHashes
		fetchRunChangeServiceInfoFn = oldInfo
	}()
	payload := filepath.Join(t.TempDir(), "app")
	if err := os.WriteFile(payload, []byte("payload\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	hash, err := hashFileSHA256(payload)
	if err != nil {
		t.Fatal(err)
	}
	fetchRemoteArtifactHashesFn = func(context.Context, string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{Found: true, Payload: &catchrpc.ArtifactHash{Kind: "binary", SHA256: hash}}, true, nil
	}
	fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		desired := catchrpc.ServiceNetworkSettings{Modes: []string{"host"}}
		return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{Network: catchrpc.ServiceNetwork{Desired: &desired}}}, nil
	}
	runs := 0
	err = runWithChangesToWithContextRunner(
		context.Background(), io.Discard, payload, []string{"--net=host", "--pull"}, "",
		ServiceEntry{Name: "api", Host: "catch.example", Args: []string{"--net=host"}}, false,
		func(context.Context, string, []string) error { runs++; return nil }, false,
	)
	if err != nil {
		t.Fatalf("runWithChangesToWithContextRunner: %v", err)
	}
	if runs != 1 {
		t.Fatalf("runner calls = %d, want 1 for non-network config change", runs)
	}
}

func TestRunPreservesStoredNetworkFlagsForUnrelatedOverrides(t *testing.T) {
	entry := ServiceEntry{Args: []string{
		"--net=ts,lan", "--ts-ver=1.2.3", "--ts-exit=exit", "--ts-tags=tag:api",
		"--macvlan-parent=eno1", "--macvlan-vlan=10", "--macvlan-mac=02:00:00:00:00:01",
	}}
	got, err := effectiveRunArgsForExistingEntry(entry, []string{"--pull"})
	if err != nil {
		t.Fatalf("effectiveRunArgsForExistingEntry: %v", err)
	}
	want := []string{
		"--net=ts,lan", "--ts-ver=1.2.3", "--ts-exit=exit", "--ts-tags=tag:api",
		"--macvlan-parent=eno1", "--macvlan-vlan=10", "--macvlan-mac=02:00:00:00:00:01", "--pull",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("effective run args = %#v, want %#v", got, want)
	}
}

func TestRunArgsWithSandboxOptionsRehydratesCanonicalPolicyBeforePayload(t *testing.T) {
	tests := []struct {
		name  string
		entry ServiceEntry
		args  []string
		want  []string
	}{
		{
			name:  "on with binds and payload boundary",
			entry: ServiceEntry{Sandbox: "on", SandboxRO: []string{"/srv/z:/z", "/etc/ssl"}, SandboxRW: []string{"/srv/cache"}},
			args:  []string{"--pull", "--", "--payload-flag"},
			want: []string{
				"--sandbox=on", "--sandbox-ro=/etc/ssl", "--sandbox-ro=/srv/z:/z", "--sandbox-rw=/srv/cache",
				"--pull", "--", "--payload-flag",
			},
		},
		{name: "off retains dormant binds", entry: ServiceEntry{Sandbox: "off", SandboxRO: []string{"/srv/read"}}, args: []string{"--pull"}, want: []string{"--sandbox=off", "--sandbox-ro=/srv/read", "--pull"}},
		{name: "legacy emits no run flag", entry: ServiceEntry{Sandbox: "legacy", SandboxRO: []string{"/ignored"}}, args: []string{"--pull"}, want: []string{"--pull"}},
		{name: "absent remains absent", entry: ServiceEntry{}, args: []string{"--pull"}, want: []string{"--pull"}},
		{name: "explicit caller policy wins", entry: ServiceEntry{Sandbox: "on", SandboxRO: []string{"/stored"}}, args: []string{"--sandbox=off", "--pull"}, want: []string{"--sandbox=off", "--pull"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runArgsWithSandboxOptions(tt.args, tt.entry)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("runArgsWithSandboxOptions = %#v, want %#v", got, tt.want)
			}
			if _, _, err := cli.ParseRun(got); err != nil {
				t.Fatalf("rehydrated args do not parse: %v", err)
			}
		})
	}
}

func TestEffectiveRunArgsRehydratesStoredSandboxThroughRealParser(t *testing.T) {
	entry := ServiceEntry{
		Sandbox: "on", SandboxRO: []string{"/srv/read:/read"}, SandboxRW: []string{"/srv/write"},
		Args: []string{"--net=host", "--", "--stored-payload"},
	}
	for _, tt := range []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "empty run args",
			want: []string{"--sandbox=on", "--sandbox-ro=/srv/read:/read", "--sandbox-rw=/srv/write", "--net=host", "--", "--stored-payload"},
		},
		{
			name: "unrelated override",
			args: []string{"--pull", "--", "--new-payload"},
			want: []string{"--sandbox=on", "--sandbox-ro=/srv/read:/read", "--sandbox-rw=/srv/write", "--net=host", "--pull", "--", "--new-payload"},
		},
		{
			name: "explicit sandbox",
			args: []string{"--sandbox=off", "--pull"},
			want: []string{"--net=host", "--sandbox=off", "--pull"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := effectiveRunArgsForExistingEntry(entry, tt.args)
			if err != nil {
				t.Fatalf("effectiveRunArgsForExistingEntry: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("effective args = %#v, want %#v", got, tt.want)
			}
			if _, _, err := cli.ParseRun(got); err != nil {
				t.Fatalf("effective args do not parse: %v", err)
			}
		})
	}
}

func TestSandboxEntryFromServiceInfoCanonicalizesAndRejectsInvalidData(t *testing.T) {
	state, ro, rw, err := sandboxEntryFromServiceInfo(&catchrpc.ServiceSandbox{
		State: "on",
		ReadOnly: []catchrpc.ServiceSandboxExposure{
			{Source: "/srv/z", Destination: "/z"},
			{Source: "/etc/ssl", Destination: "/etc/ssl"},
		},
		Writable: []catchrpc.ServiceSandboxExposure{{Source: "/srv/cache", Destination: "/srv/cache"}},
	})
	if err != nil {
		t.Fatalf("sandboxEntryFromServiceInfo: %v", err)
	}
	if state != "on" || !reflect.DeepEqual(ro, []string{"/etc/ssl", "/srv/z:/z"}) || !reflect.DeepEqual(rw, []string{"/srv/cache"}) {
		t.Fatalf("canonical sandbox entry = %q %#v %#v", state, ro, rw)
	}
	ro[0] = "/mutated"
	if state2, ro2, _, err := sandboxEntryFromServiceInfo(&catchrpc.ServiceSandbox{State: "legacy"}); err != nil || state2 != "legacy" || ro2 != nil {
		t.Fatalf("legacy sandbox entry = %q %#v, err %v", state2, ro2, err)
	}

	for _, tt := range []struct {
		name string
		info *catchrpc.ServiceSandbox
	}{
		{name: "nil", info: nil},
		{name: "invalid state", info: &catchrpc.ServiceSandbox{State: "broken"}},
		{name: "relative source", info: &catchrpc.ServiceSandbox{State: "on", ReadOnly: []catchrpc.ServiceSandboxExposure{{Source: "relative", Destination: "/dest"}}}},
		{name: "reset is not an exposure", info: &catchrpc.ServiceSandbox{State: "off", Writable: []catchrpc.ServiceSandboxExposure{{Source: "reset", Destination: "reset"}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, _, err := sandboxEntryFromServiceInfo(tt.info); err == nil {
				t.Fatal("sandboxEntryFromServiceInfo error = nil")
			}
		})
	}
}

func TestSandboxEntryFromServiceInfoRejectsUnsafeDestinationSets(t *testing.T) {
	for _, tt := range []struct {
		name string
		info *catchrpc.ServiceSandbox
	}{
		{
			name: "root destination",
			info: &catchrpc.ServiceSandbox{State: "on", ReadOnly: []catchrpc.ServiceSandboxExposure{{Source: "/srv/read", Destination: "/"}}},
		},
		{
			name: "source contains nul",
			info: &catchrpc.ServiceSandbox{State: "on", ReadOnly: []catchrpc.ServiceSandboxExposure{{Source: "/srv/read\x00escape", Destination: "/read"}}},
		},
		{
			name: "destination contains nul",
			info: &catchrpc.ServiceSandbox{State: "on", ReadOnly: []catchrpc.ServiceSandboxExposure{{Source: "/srv/read", Destination: "/read\x00escape"}}},
		},
		{
			name: "same class equal destinations",
			info: &catchrpc.ServiceSandbox{State: "on", ReadOnly: []catchrpc.ServiceSandboxExposure{
				{Source: "/srv/one", Destination: "/data"},
				{Source: "/srv/two", Destination: "/data"},
			}},
		},
		{
			name: "same class ancestor destination",
			info: &catchrpc.ServiceSandbox{State: "on", ReadOnly: []catchrpc.ServiceSandboxExposure{
				{Source: "/srv/one", Destination: "/data"},
				{Source: "/srv/two", Destination: "/data/child"},
			}},
		},
		{
			name: "same class descendant destination",
			info: &catchrpc.ServiceSandbox{State: "on", ReadOnly: []catchrpc.ServiceSandboxExposure{
				{Source: "/srv/one", Destination: "/data/child"},
				{Source: "/srv/two", Destination: "/data"},
			}},
		},
		{
			name: "cross class equal destinations",
			info: &catchrpc.ServiceSandbox{
				State:    "on",
				ReadOnly: []catchrpc.ServiceSandboxExposure{{Source: "/srv/read", Destination: "/data"}},
				Writable: []catchrpc.ServiceSandboxExposure{{Source: "/srv/write", Destination: "/data"}},
			},
		},
		{
			name: "read only ancestor of writable",
			info: &catchrpc.ServiceSandbox{
				State:    "on",
				ReadOnly: []catchrpc.ServiceSandboxExposure{{Source: "/srv/read", Destination: "/data"}},
				Writable: []catchrpc.ServiceSandboxExposure{{Source: "/srv/write", Destination: "/data/child"}},
			},
		},
		{
			name: "writable ancestor of read only",
			info: &catchrpc.ServiceSandbox{
				State:    "on",
				ReadOnly: []catchrpc.ServiceSandboxExposure{{Source: "/srv/read", Destination: "/data/child"}},
				Writable: []catchrpc.ServiceSandboxExposure{{Source: "/srv/write", Destination: "/data"}},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, _, err := sandboxEntryFromServiceInfo(tt.info); err == nil {
				t.Fatal("sandboxEntryFromServiceInfo error = nil")
			}
		})
	}

	state, ro, rw, err := sandboxEntryFromServiceInfo(&catchrpc.ServiceSandbox{
		State:    "on",
		ReadOnly: []catchrpc.ServiceSandboxExposure{{Source: "/srv/read", Destination: "/data"}},
		Writable: []catchrpc.ServiceSandboxExposure{{Source: "/srv/write", Destination: "/database"}},
	})
	if err != nil {
		t.Fatalf("disjoint destination prefixes rejected: %v", err)
	}
	if state != "on" || !reflect.DeepEqual(ro, []string{"/srv/read:/data"}) || !reflect.DeepEqual(rw, []string{"/srv/write:/database"}) {
		t.Fatalf("disjoint sandbox entry = %q %#v %#v", state, ro, rw)
	}
}

func TestRejectExistingRunProtectedChangesUsesOneFetchForNetworkAndSandbox(t *testing.T) {
	oldInfo := fetchRunChangeServiceInfoFn
	t.Cleanup(func() { fetchRunChangeServiceInfoFn = oldInfo })
	fetches := 0
	desired := catchrpc.ServiceNetworkSettings{Modes: []string{"host"}}
	fetchRunChangeServiceInfoFn = func(_ context.Context, host, service string) (catchrpc.ServiceInfoResponse, error) {
		fetches++
		if host != "catch.example" || service != "api" {
			t.Fatalf("fetch target = %s/%s", host, service)
		}
		return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
			ServiceType: "systemd",
			Network:     catchrpc.ServiceNetwork{Desired: &desired},
			Sandbox: &catchrpc.ServiceSandbox{State: "on", ReadOnly: []catchrpc.ServiceSandboxExposure{
				{Source: "/etc/ssl", Destination: "/etc/ssl"},
			}},
		}}, nil
	}

	err := rejectExistingRunProtectedChanges(context.Background(), ServiceEntry{Name: "api", Host: "catch.example"}, []string{
		"--net=host", "--sandbox=on", "--sandbox-ro=/etc/ssl", "--", "--payload-flag",
	})
	if err != nil {
		t.Fatalf("rejectExistingRunProtectedChanges: %v", err)
	}
	if fetches != 1 {
		t.Fatalf("ServiceInfo fetches = %d, want 1", fetches)
	}
}

func TestRejectExistingRunProtectedChangesNamesExactSandboxMigration(t *testing.T) {
	oldInfo := fetchRunChangeServiceInfoFn
	t.Cleanup(func() { fetchRunChangeServiceInfoFn = oldInfo })
	desired := catchrpc.ServiceNetworkSettings{Modes: []string{"host"}}
	fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
			ServiceType: "systemd", Network: catchrpc.ServiceNetwork{Desired: &desired},
			Sandbox: &catchrpc.ServiceSandbox{
				State:    "on",
				ReadOnly: []catchrpc.ServiceSandboxExposure{{Source: "/srv/read", Destination: "/srv/read"}},
				Writable: []catchrpc.ServiceSandboxExposure{{Source: "/srv/write", Destination: "/work"}},
			},
		}}, nil
	}

	err := rejectExistingRunProtectedChanges(context.Background(), ServiceEntry{Name: "api", Host: "catch.example"}, []string{
		"--sandbox=off", "--sandbox-ro=/srv/new:/new", "--sandbox-rw=/srv/write:/work",
	})
	if err == nil {
		t.Fatal("sandbox change error = nil")
	}
	want := "yeet service set api --sandbox=off --sandbox-ro=reset --sandbox-ro=/srv/new:/new"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("sandbox change error = %v, want command containing %q", err, want)
	}
	if strings.Contains(err.Error(), "--sandbox-rw=reset") {
		t.Fatalf("unchanged writable list was unnecessarily reset: %v", err)
	}
}

func TestSandboxGuidanceQuotesBackticks(t *testing.T) {
	current := clientSandboxPolicy{State: "on", ReadOnly: []string{"/old"}}
	target := clientSandboxPolicy{State: "on", ReadOnly: []string{"/srv/`id`"}}
	got := serviceSetCommandForSandboxPolicy("api", current, target)
	want := "yeet service set api --sandbox=on --sandbox-ro=reset '--sandbox-ro=/srv/`id`'"
	if got != want {
		t.Fatalf("sandbox guidance = %q, want shell-safe %q", got, want)
	}
}

func TestSandboxGuidanceResetsOnlyWhenExistingEntriesAreRemoved(t *testing.T) {
	for _, tt := range []struct {
		name    string
		current clientSandboxPolicy
		target  clientSandboxPolicy
		want    string
	}{
		{
			name:    "read only addition",
			current: clientSandboxPolicy{State: "on", ReadOnly: []string{"/old"}},
			target:  clientSandboxPolicy{State: "on", ReadOnly: []string{"/new", "/old"}},
			want:    "yeet service set api --sandbox=on --sandbox-ro=/new",
		},
		{
			name:    "writable addition",
			current: clientSandboxPolicy{State: "on", Writable: []string{"/old"}},
			target:  clientSandboxPolicy{State: "on", Writable: []string{"/new", "/old"}},
			want:    "yeet service set api --sandbox=on --sandbox-rw=/new",
		},
		{
			name:    "read only replacement",
			current: clientSandboxPolicy{State: "on", ReadOnly: []string{"/old", "/removed"}},
			target:  clientSandboxPolicy{State: "on", ReadOnly: []string{"/new", "/old"}},
			want:    "yeet service set api --sandbox=on --sandbox-ro=reset --sandbox-ro=/new --sandbox-ro=/old",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := serviceSetCommandForSandboxPolicy("api", tt.current, tt.target); got != tt.want {
				t.Fatalf("sandbox guidance = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSuccessfulFreshNativeRunPersistsCatchSandboxInsteadOfRequestFlags(t *testing.T) {
	oldInfo := fetchRunChangeServiceInfoFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldService := serviceOverride
	t.Cleanup(func() {
		fetchRunChangeServiceInfoFn = oldInfo
		fetchRemoteArtifactHashesFn = oldHashes
		serviceOverride = oldService
	})
	payload := filepath.Join(t.TempDir(), "api")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile payload: %v", err)
	}
	fetchRemoteArtifactHashesFn = func(context.Context, string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{}, false, nil
	}
	fetches := 0
	fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		fetches++
		if fetches%2 == 1 {
			return catchrpc.ServiceInfoResponse{Found: false}, nil
		}
		return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
			ServiceType: "systemd",
			Sandbox: &catchrpc.ServiceSandbox{
				State: "on",
				ReadOnly: []catchrpc.ServiceSandboxExposure{
					{Source: "/canonical/z", Destination: "/z"},
					{Source: "/canonical/read", Destination: "/canonical/read"},
				},
				Writable: []catchrpc.ServiceSandboxExposure{{Source: "/canonical/write", Destination: "/work"}},
			},
		}}, nil
	}
	runArgs := []string{"--sandbox=off", "--sandbox-ro=/requested", "--", "--payload"}
	runs := 0
	result, err := runWithChangesToWithContextRunnerResult(
		context.Background(), io.Discard, payload, runArgs, "", ServiceEntry{Name: "api", Host: "host-a"}, false,
		func(context.Context, string, []string) error { runs++; return nil }, false,
	)
	if err != nil {
		t.Fatalf("runWithChangesToWithContextRunner: %v", err)
	}
	if runs != 1 || fetches != 2 {
		t.Fatalf("runs/fetches = %d/%d, want 1/2", runs, fetches)
	}

	serviceOverride = "api"
	dir := t.TempDir()
	loc := &projectConfigLocation{
		Path: filepath.Join(dir, projectConfigName), Dir: dir,
		Config: &ProjectConfig{Version: projectConfigVersion},
	}
	if err := saveRunConfigWithPayloadKindResult(loc, "host-a", payload, "", runArgs, "", false, result); err != nil {
		t.Fatalf("saveRunConfig: %v", err)
	}
	entry, ok := loc.Config.ServiceEntry("api", "host-a")
	if !ok {
		t.Fatal("saved entry missing")
	}
	if entry.Sandbox != "on" ||
		!reflect.DeepEqual(entry.SandboxRO, []string{"/canonical/read", "/canonical/z:/z"}) ||
		!reflect.DeepEqual(entry.SandboxRW, []string{"/canonical/write:/work"}) {
		t.Fatalf("saved sandbox = %q %#v %#v", entry.Sandbox, entry.SandboxRO, entry.SandboxRW)
	}
	if !reflect.DeepEqual(entry.Args, []string{"--", "--payload"}) {
		t.Fatalf("saved args = %#v, want payload args without sandbox controls", entry.Args)
	}
}

func TestSuccessfulRunConfigSavePublishesExactIndependentCandidate(t *testing.T) {
	oldService := serviceOverride
	t.Cleanup(func() { serviceOverride = oldService })
	serviceOverride = "api"

	hosts := []string{"z-host"}
	emptySnapshotEvents := make([]string, 0)
	emptyPorts := make([]string, 0)
	emptySandboxRO := make([]string, 0)
	emptySandboxRW := make([]string, 0)
	emptyArgs := make([]string, 0)
	dataSnapshotEvents := []string{"start"}
	dataPorts := []string{"127.0.0.1:8080:80"}
	dataSandboxRO := []string{"/srv/read"}
	dataSandboxRW := []string{"/srv/write"}
	dataArgs := []string{"--old"}
	required := true
	config := &ProjectConfig{
		Version: projectConfigVersion,
		Hosts:   hosts,
		Services: []ServiceEntry{
			{
				Name: "old-empty", Host: "z-host", Type: serviceTypeRun, Payload: "empty.sh",
				SnapshotEvents: emptySnapshotEvents, Ports: emptyPorts,
				SandboxRO: emptySandboxRO, SandboxRW: emptySandboxRW, Args: emptyArgs,
			},
			{Name: "old-nil", Host: "z-host", Type: serviceTypeRun, Payload: "nil.sh"},
			{
				Name: "old-data", Host: "z-host", Type: serviceTypeRun, Payload: "data.sh",
				SnapshotRequired: &required, SnapshotEvents: dataSnapshotEvents, Ports: dataPorts,
				SandboxRO: dataSandboxRO, SandboxRW: dataSandboxRW, Args: dataArgs,
			},
		},
	}
	originalConfigPointer := config
	dir := t.TempDir()
	payload := filepath.Join(dir, "api.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	loc := &projectConfigLocation{Path: filepath.Join(dir, projectConfigName), Dir: dir, Config: config}
	capturedRO := []string{"/captured/read"}
	capturedRW := []string{"/captured/write:/work"}
	result := runChangeResult{
		sandbox:      &clientSandboxPolicy{State: "on", ReadOnly: capturedRO, Writable: capturedRW},
		catchChanged: true,
	}
	if err := saveRunConfigWithPayloadKindResult(loc, "a-host", payload, "file", []string{"--", "--target"}, "", false, result); err != nil {
		t.Fatalf("saveRunConfigWithPayloadKindResult: %v", err)
	}
	if loc.Config != originalConfigPointer {
		t.Fatal("successful config publication replaced the outer ProjectConfig pointer")
	}
	if _, err := os.Stat(loc.Path); err != nil {
		t.Fatalf("successful config save did not persist %s: %v", loc.Path, err)
	}
	if !reflect.DeepEqual(loc.Config.Hosts, []string{"a-host", "z-host"}) {
		t.Fatalf("published hosts = %#v, want target plus existing host", loc.Config.Hosts)
	}

	findEntry := func(name, host string) *ServiceEntry {
		for i := range loc.Config.Services {
			entry := &loc.Config.Services[i]
			if entry.Name == name && entry.Host == host {
				return entry
			}
		}
		return nil
	}
	target := findEntry("api", "a-host")
	if target == nil {
		t.Fatal("published config missing api@a-host")
	}
	if target.Payload != "api.sh" || target.PayloadKind != "file" || target.Sandbox != "on" ||
		!reflect.DeepEqual(target.SandboxRO, []string{"/captured/read"}) ||
		!reflect.DeepEqual(target.SandboxRW, []string{"/captured/write:/work"}) ||
		!reflect.DeepEqual(target.Args, []string{"--", "--target"}) {
		t.Fatalf("published target entry = %#v", *target)
	}

	emptyEntry := findEntry("old-empty", "z-host")
	if emptyEntry == nil {
		t.Fatal("published config missing old-empty@z-host")
	}
	for name, values := range map[string][]string{
		"snapshot events": emptyEntry.SnapshotEvents,
		"ports":           emptyEntry.Ports,
		"sandbox ro":      emptyEntry.SandboxRO,
		"sandbox rw":      emptyEntry.SandboxRW,
		"args":            emptyEntry.Args,
	} {
		if values == nil || len(values) != 0 {
			t.Fatalf("unrelated allocated-empty %s = %#v, want non-nil empty", name, values)
		}
	}
	nilEntry := findEntry("old-nil", "z-host")
	if nilEntry == nil {
		t.Fatal("published config missing old-nil@z-host")
	}
	for name, values := range map[string][]string{
		"snapshot events": nilEntry.SnapshotEvents,
		"ports":           nilEntry.Ports,
		"sandbox ro":      nilEntry.SandboxRO,
		"sandbox rw":      nilEntry.SandboxRW,
		"args":            nilEntry.Args,
	} {
		if values != nil {
			t.Fatalf("unrelated nil %s = %#v, want nil", name, values)
		}
	}

	hosts[0] = "mutated-input-host"
	dataSnapshotEvents[0] = "mutated-input-event"
	dataPorts[0] = "mutated-input-port"
	dataSandboxRO[0] = "mutated-input-ro"
	dataSandboxRW[0] = "mutated-input-rw"
	dataArgs[0] = "mutated-input-arg"
	capturedRO[0] = "mutated-captured-ro"
	capturedRW[0] = "mutated-captured-rw"
	dataEntry := findEntry("old-data", "z-host")
	if dataEntry == nil || !reflect.DeepEqual(dataEntry.SnapshotEvents, []string{"start"}) ||
		!reflect.DeepEqual(dataEntry.Ports, []string{"127.0.0.1:8080:80"}) ||
		!reflect.DeepEqual(dataEntry.SandboxRO, []string{"/srv/read"}) ||
		!reflect.DeepEqual(dataEntry.SandboxRW, []string{"/srv/write"}) ||
		!reflect.DeepEqual(dataEntry.Args, []string{"--old"}) {
		t.Fatalf("published unrelated entry aliases pre-save input: %#v", dataEntry)
	}
	if !reflect.DeepEqual(loc.Config.Hosts, []string{"a-host", "z-host"}) ||
		!reflect.DeepEqual(target.SandboxRO, []string{"/captured/read"}) ||
		!reflect.DeepEqual(target.SandboxRW, []string{"/captured/write:/work"}) {
		t.Fatalf("published target or hosts alias pre-save input: hosts=%#v target=%#v", loc.Config.Hosts, *target)
	}

	expectedTargetArgs := []string{"--", "--target"}
	expectedTargetArgs[0] = "mutated-expected-arg"
	if !reflect.DeepEqual(target.Args, []string{"--", "--target"}) {
		t.Fatalf("published target aliases expected snapshot: %#v", target.Args)
	}

	emptyHosts := make([]string, 0)
	emptyHostsClone := cloneRunProjectConfig(&ProjectConfig{Hosts: emptyHosts})
	if emptyHostsClone.Hosts == nil || len(emptyHostsClone.Hosts) != 0 {
		t.Fatalf("allocated-empty hosts clone = %#v, want non-nil empty", emptyHostsClone.Hosts)
	}
}

func TestExistingNativeRunTransitionRefreshesNonNativeSandboxBeforeConfigSave(t *testing.T) {
	oldInfo := fetchRunChangeServiceInfoFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldService := serviceOverride
	oldArch := remoteCatchOSAndArchFn
	t.Cleanup(func() {
		fetchRunChangeServiceInfoFn = oldInfo
		fetchRemoteArtifactHashesFn = oldHashes
		serviceOverride = oldService
		remoteCatchOSAndArchFn = oldArch
	})
	serviceOverride = "api"
	remoteCatchOSAndArchFn = func() (string, string, error) { return "linux", "amd64", nil }
	fetchRemoteArtifactHashesFn = func(context.Context, string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{}, false, nil
	}

	for _, tt := range []struct {
		name        string
		payloadKind string
		payload     func(string) string
	}{
		{
			name: "generated compose", payloadKind: "compose",
			payload: func(dir string) string {
				path := filepath.Join(dir, "compose.yml")
				if err := os.WriteFile(path, []byte("services:\n  app:\n    image: alpine\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "remote image", payloadKind: "remote-image",
			payload: func(string) string { return "ghcr.io/example/app:latest" },
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			payload := tt.payload(dir)
			loc := &projectConfigLocation{
				Path: filepath.Join(dir, projectConfigName), Dir: dir,
				Config: &ProjectConfig{Version: projectConfigVersion},
			}
			loc.Config.SetServiceEntry(ServiceEntry{
				Name: "api", Host: "host-a", Type: serviceTypeRun, Payload: "old-script", PayloadKind: "file",
				Args: []string{"--net=host"}, Sandbox: "on", SandboxRO: []string{"/old/read"},
			})
			if err := saveProjectConfig(loc); err != nil {
				t.Fatal(err)
			}
			entry, _ := loc.Config.ServiceEntry("api", "host-a")
			fetches := 0
			fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
				fetches++
				if fetches == 1 {
					desired := catchrpc.ServiceNetworkSettings{Modes: []string{"host"}}
					return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
						ServiceType: "systemd", Network: catchrpc.ServiceNetwork{Desired: &desired},
						Sandbox: &catchrpc.ServiceSandbox{State: "on", ReadOnly: []catchrpc.ServiceSandboxExposure{
							{Source: "/old/read", Destination: "/old/read"},
						}},
					}}, nil
				}
				return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{ServiceType: "docker-compose"}}, nil
			}
			runs := 0
			runArgs := []string{"--net=host", "--sandbox=on", "--sandbox-ro=/old/read"}
			result, err := runWithChangesToWithContextRunnerResult(
				context.Background(), io.Discard, payload, runArgs, "", entry, false,
				func(context.Context, string, []string) error { runs++; return nil }, false,
			)
			if err != nil {
				t.Fatalf("runWithChangesToWithContextRunner: %v", err)
			}
			if err := saveRunConfigWithPayloadKindResult(loc, "host-a", payload, tt.payloadKind, runArgs, "", false, result); err != nil {
				t.Fatalf("saveRunConfigWithPayloadKind: %v", err)
			}
			if runs != 1 || fetches != 2 {
				t.Fatalf("runs/fetches = %d/%d, want 1/2", runs, fetches)
			}
			saved, _ := loc.Config.ServiceEntry("api", "host-a")
			if saved.Sandbox != "" || len(saved.SandboxRO) != 0 || len(saved.SandboxRW) != 0 {
				t.Fatalf("stale native sandbox survived %s transition: %#v", tt.payloadKind, saved)
			}
			for _, arg := range saved.Args {
				if strings.HasPrefix(arg, "--sandbox") {
					t.Fatalf("sandbox control flag persisted in args: %#v", saved.Args)
				}
			}
			raw, err := os.ReadFile(loc.Path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "sandbox") {
				t.Fatalf("sandbox state persisted in non-native TOML:\n%s", raw)
			}
		})
	}
}

func TestExistingNativeRunTransitionRefreshFailureFailsClosed(t *testing.T) {
	oldInfo := fetchRunChangeServiceInfoFn
	oldHashes := fetchRemoteArtifactHashesFn
	t.Cleanup(func() {
		fetchRunChangeServiceInfoFn = oldInfo
		fetchRemoteArtifactHashesFn = oldHashes
	})
	dir := t.TempDir()
	payload := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(payload, []byte("services:\n  app:\n    image: alpine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fetchRemoteArtifactHashesFn = func(context.Context, string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{}, false, nil
	}
	fetches := 0
	fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		fetches++
		if fetches == 1 {
			desired := catchrpc.ServiceNetworkSettings{Modes: []string{"host"}}
			return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
				ServiceType: "systemd", Network: catchrpc.ServiceNetwork{Desired: &desired},
				Sandbox: &catchrpc.ServiceSandbox{State: "on"},
			}}, nil
		}
		return catchrpc.ServiceInfoResponse{}, errors.New("post-transition info unavailable")
	}
	runs := 0
	err := runWithChangesToWithContextRunner(
		context.Background(), io.Discard, payload, []string{"--net=host", "--sandbox=on"}, "",
		ServiceEntry{Name: "api", Host: "host-a", PayloadKind: "file", Args: []string{"--net=host"}, Sandbox: "on"}, false,
		func(context.Context, string, []string) error { runs++; return nil }, false,
	)
	wantRecovery := "recover with `yeet --host host-a service sync api --config ~/yeet-services/yeet.toml`"
	if err == nil || !strings.Contains(err.Error(), "catch service changed") || !strings.Contains(err.Error(), wantRecovery) {
		t.Fatalf("error = %v, want Catch-changed recovery %q", err, wantRecovery)
	}
	if runs != 1 || fetches != 2 {
		t.Fatalf("runs/fetches = %d/%d, want 1/2", runs, fetches)
	}
}

func TestExistingNativeRunKeepsSingleProtectedInfoFetch(t *testing.T) {
	oldInfo := fetchRunChangeServiceInfoFn
	oldHashes := fetchRemoteArtifactHashesFn
	t.Cleanup(func() {
		fetchRunChangeServiceInfoFn = oldInfo
		fetchRemoteArtifactHashesFn = oldHashes
	})
	payload := filepath.Join(t.TempDir(), "api")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fetchRemoteArtifactHashesFn = func(context.Context, string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{}, false, nil
	}
	fetches := 0
	fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		fetches++
		desired := catchrpc.ServiceNetworkSettings{Modes: []string{"host"}}
		return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
			ServiceType: "systemd", Network: catchrpc.ServiceNetwork{Desired: &desired},
			Sandbox: &catchrpc.ServiceSandbox{State: "on"},
		}}, nil
	}
	runs := 0
	err := runWithChangesToWithContextRunner(
		context.Background(), io.Discard, payload, []string{"--net=host", "--sandbox=on"}, "",
		ServiceEntry{Name: "api", Host: "host-a", PayloadKind: "file", Args: []string{"--net=host"}, Sandbox: "on"}, false,
		func(context.Context, string, []string) error { runs++; return nil }, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if runs != 1 || fetches != 1 {
		t.Fatalf("runs/fetches = %d/%d, want 1/1", runs, fetches)
	}
}

func TestRunSandboxResultStaysMatchedAcrossInterleavedSameServiceRuns(t *testing.T) {
	oldInfo, oldHashes, oldService := fetchRunChangeServiceInfoFn, fetchRemoteArtifactHashesFn, serviceOverride
	t.Cleanup(func() {
		fetchRunChangeServiceInfoFn, fetchRemoteArtifactHashesFn, serviceOverride = oldInfo, oldHashes, oldService
	})
	serviceOverride = "api"
	fetchRemoteArtifactHashesFn = func(context.Context, string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{}, false, nil
	}
	desired := catchrpc.ServiceNetworkSettings{Modes: []string{"host"}}
	fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
			ServiceType: "systemd", Network: catchrpc.ServiceNetwork{Desired: &desired},
			Sandbox: &catchrpc.ServiceSandbox{State: "on", ReadOnly: []catchrpc.ServiceSandboxExposure{
				{Source: "/native/read", Destination: "/read"},
			}},
		}}, nil
	}
	nativePayload := filepath.Join(t.TempDir(), "api.sh")
	if err := os.WriteFile(nativePayload, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	nativeResult, err := runWithChangesToWithContextRunnerResult(
		context.Background(), io.Discard, nativePayload, []string{"--net=host", "--sandbox=on", "--sandbox-ro=/native/read:/read"}, "",
		ServiceEntry{Name: "api", Host: "host-a", PayloadKind: "file", Args: []string{"--net=host"}, Sandbox: "on", SandboxRO: []string{"/native/read:/read"}}, false,
		func(context.Context, string, []string) error { return nil }, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
			ServiceType: "docker-compose", Network: catchrpc.ServiceNetwork{Desired: &desired},
		}}, nil
	}
	containerPayload := "ghcr.io/example/api:latest"
	containerResult, err := runWithChangesToWithContextRunnerResult(
		context.Background(), io.Discard, containerPayload, []string{"--net=host"}, "",
		ServiceEntry{Name: "api", Host: "host-a", PayloadKind: "remote-image", Args: []string{"--net=host"}}, false,
		func(context.Context, string, []string) error { return nil }, false,
	)
	if err != nil {
		t.Fatal(err)
	}

	nativeLoc := &projectConfigLocation{Path: filepath.Join(t.TempDir(), projectConfigName), Config: &ProjectConfig{Version: projectConfigVersion}}
	containerLoc := &projectConfigLocation{Path: filepath.Join(t.TempDir(), projectConfigName), Config: &ProjectConfig{Version: projectConfigVersion}}
	nativeLoc.Config.SetServiceEntry(ServiceEntry{Name: "api", Host: "host-a", Sandbox: "legacy"})
	containerLoc.Config.SetServiceEntry(ServiceEntry{Name: "api", Host: "host-a", Sandbox: "on", SandboxRO: []string{"/stale"}})
	if err := saveRunConfigWithPayloadKindResult(containerLoc, "host-a", containerPayload, "remote-image", []string{"--net=host"}, "", false, containerResult); err != nil {
		t.Fatal(err)
	}
	if err := saveRunConfigWithPayloadKindResult(nativeLoc, "host-a", nativePayload, "file", []string{"--net=host"}, "", false, nativeResult); err != nil {
		t.Fatal(err)
	}
	native, _ := nativeLoc.Config.ServiceEntry("api", "host-a")
	container, _ := containerLoc.Config.ServiceEntry("api", "host-a")
	if native.Sandbox != "on" || !reflect.DeepEqual(native.SandboxRO, []string{"/native/read:/read"}) {
		t.Fatalf("native operation lost its capture: %#v", native)
	}
	if container.Sandbox != "" || len(container.SandboxRO) != 0 || len(container.SandboxRW) != 0 {
		t.Fatalf("container operation stole native capture: %#v", container)
	}
}

func TestRunSandboxResultDoesNotSurviveFailedRunOrRetry(t *testing.T) {
	oldInfo, oldHashes, oldService := fetchRunChangeServiceInfoFn, fetchRemoteArtifactHashesFn, serviceOverride
	t.Cleanup(func() {
		fetchRunChangeServiceInfoFn, fetchRemoteArtifactHashesFn, serviceOverride = oldInfo, oldHashes, oldService
	})
	serviceOverride = "api"
	fetchRemoteArtifactHashesFn = func(context.Context, string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{}, false, nil
	}
	fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{Found: false}, nil
	}
	nativePayload := filepath.Join(t.TempDir(), "api.sh")
	if err := os.WriteFile(nativePayload, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	failedResult, err := runWithChangesToWithContextRunnerResult(
		context.Background(), io.Discard, nativePayload, nil, "", ServiceEntry{Name: "api", Host: "host-a", PayloadKind: "file"}, false,
		func(context.Context, string, []string) error { return errors.New("remote run failed") }, false,
	)
	if err == nil || !strings.Contains(err.Error(), "remote run failed") {
		t.Fatalf("failed run error = %v", err)
	}
	loc := &projectConfigLocation{Path: filepath.Join(t.TempDir(), projectConfigName), Config: &ProjectConfig{Version: projectConfigVersion}}
	loc.Config.SetServiceEntry(ServiceEntry{Name: "api", Host: "host-a", Sandbox: "legacy"})
	if err := saveRunConfigWithPayloadKindResult(loc, "host-a", nativePayload, "file", nil, "", false, failedResult); err != nil {
		t.Fatal(err)
	}
	afterFailure, _ := loc.Config.ServiceEntry("api", "host-a")
	if afterFailure.Sandbox != "legacy" {
		t.Fatalf("failed run produced a sandbox capture: %#v", afterFailure)
	}

	containerPayload := "ghcr.io/example/api:retry"
	retryResult, err := runWithChangesToWithContextRunnerResult(
		context.Background(), io.Discard, containerPayload, nil, "", ServiceEntry{Name: "api", Host: "host-a", PayloadKind: "remote-image"}, false,
		func(context.Context, string, []string) error { return nil }, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveRunConfigWithPayloadKindResult(loc, "host-a", containerPayload, "remote-image", nil, "", false, retryResult); err != nil {
		t.Fatal(err)
	}
	afterRetry, _ := loc.Config.ServiceEntry("api", "host-a")
	if afterRetry.Sandbox != "" || len(afterRetry.SandboxRO) != 0 || len(afterRetry.SandboxRW) != 0 {
		t.Fatalf("retry preserved stale native sandbox: %#v", afterRetry)
	}
}

func TestRunSandboxResultDiscardedByNoConfigCannotLeakIntoBackendRun(t *testing.T) {
	preserveSvcCommandGlobals(t)
	oldInfo, oldHashes, oldPrompt, oldWarn, oldTerminal := fetchRunChangeServiceInfoFn, fetchRemoteArtifactHashesFn, activePrompter, warnProjectConfigNotSavedFn, isTerminalFn
	t.Cleanup(func() {
		fetchRunChangeServiceInfoFn, fetchRemoteArtifactHashesFn = oldInfo, oldHashes
		activePrompter, warnProjectConfigNotSavedFn, isTerminalFn = oldPrompt, oldWarn, oldTerminal
	})
	serviceOverride = "api"
	loadedPrefs.DefaultHost = "host-a"
	fetchRemoteArtifactHashesFn = func(context.Context, string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{}, false, nil
	}
	fetches := 0
	fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		fetches++
		if fetches == 1 {
			return catchrpc.ServiceInfoResponse{Found: false}, nil
		}
		return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
			ServiceType: "systemd", Sandbox: &catchrpc.ServiceSandbox{State: "on", ReadOnly: []catchrpc.ServiceSandboxExposure{
				{Source: "/native/read", Destination: "/read"},
			}},
		}}, nil
	}
	payload := filepath.Join(t.TempDir(), "api.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := runWithChangesToWithContextRunnerResult(
		context.Background(), io.Discard, payload, nil, "", ServiceEntry{Name: "api", Host: "host-a", PayloadKind: "file"}, false,
		func(context.Context, string, []string) error { return nil }, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	cwd := useTempSvcCwd(t)
	activePrompter = fakePrompter{selection: workspaceSelection{Choice: workspacePromptRunOnce}}
	warnProjectConfigNotSavedFn = func(string) {}
	isTerminalFn = func(int) bool { return true }
	if err := saveRunConfigWithPayloadKindResult(nil, "host-a", payload, "file", nil, "", false, result); err != nil {
		t.Fatal(err)
	}
	if loaded, err := loadProjectConfigFromDir(cwd); err != nil || loaded != nil {
		t.Fatalf("run-once config = %#v, err %v, want none", loaded, err)
	}

	loc := &projectConfigLocation{Path: filepath.Join(t.TempDir(), projectConfigName), Config: &ProjectConfig{Version: projectConfigVersion}}
	loc.Config.SetServiceEntry(ServiceEntry{Name: "api", Host: "host-a", Sandbox: "legacy"})
	vmResult, err := runWithChangesToWithContextRunnerResult(
		context.Background(), io.Discard, "vm://ubuntu/26.04", nil, "",
		ServiceEntry{Name: "api", Host: "host-a", PayloadKind: serviceTypeVM}, false,
		func(context.Context, string, []string) error { return nil }, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveRunConfigWithPayloadKindResult(loc, "host-a", "vm://ubuntu/26.04", serviceTypeVM, nil, "", false, vmResult); err != nil {
		t.Fatal(err)
	}
	entry, _ := loc.Config.ServiceEntry("api", "host-a")
	if entry.Sandbox != "" || len(entry.SandboxRO) != 0 || len(entry.SandboxRW) != 0 {
		t.Fatalf("discarded native capture leaked into VM/backend save: %#v", entry)
	}
}

func TestUnchangedSandboxRunDoesNotRedeploy(t *testing.T) {
	oldInfo := fetchRunChangeServiceInfoFn
	oldHashes := fetchRemoteArtifactHashesFn
	t.Cleanup(func() {
		fetchRunChangeServiceInfoFn = oldInfo
		fetchRemoteArtifactHashesFn = oldHashes
	})
	payload := filepath.Join(t.TempDir(), "api")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	payloadHash, err := hashFileSHA256(payload)
	if err != nil {
		t.Fatal(err)
	}
	fetchRemoteArtifactHashesFn = func(context.Context, string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{
			Found: true, Payload: &catchrpc.ArtifactHash{Kind: "script", SHA256: payloadHash},
		}, true, nil
	}
	fetches := 0
	desired := catchrpc.ServiceNetworkSettings{Modes: []string{"host"}}
	fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		fetches++
		return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
			ServiceType: "systemd", Network: catchrpc.ServiceNetwork{Desired: &desired},
			Sandbox: &catchrpc.ServiceSandbox{
				State:    "on",
				ReadOnly: []catchrpc.ServiceSandboxExposure{{Source: "/srv/read", Destination: "/read"}},
			},
		}}, nil
	}
	entry := ServiceEntry{
		Name: "api", Host: "host-a", PayloadKind: "file", Args: []string{"--net=host", "--", "app-arg"},
		Sandbox: "on", SandboxRO: []string{"/srv/read:/read"},
	}
	runArgs, err := effectiveRunArgsForExistingEntry(entry, nil)
	if err != nil {
		t.Fatal(err)
	}
	runs := 0
	var stdout bytes.Buffer
	if err := runWithChangesToWithContextRunner(
		context.Background(), &stdout, payload, runArgs, "", entry, false,
		func(context.Context, string, []string) error { runs++; return nil }, false,
	); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("runner calls = %d, want 0 for unchanged payload, args, network, and sandbox", runs)
	}
	if fetches != 1 {
		t.Fatalf("ServiceInfo fetches = %d, want one shared protected-setting fetch", fetches)
	}
	if got := stdout.String(); got != "No changes detected\n" {
		t.Fatalf("stdout = %q, want unchanged message", got)
	}
}

func stubSuccessfulFreshNativeSandboxInfo(t *testing.T) {
	t.Helper()
	oldInfo := fetchRunChangeServiceInfoFn
	fetches := 0
	fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		fetches++
		if fetches%2 == 1 {
			return catchrpc.ServiceInfoResponse{Found: false}, nil
		}
		return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
			ServiceType: "systemd",
			Sandbox:     &catchrpc.ServiceSandbox{State: "on"},
		}}, nil
	}
	t.Cleanup(func() {
		fetchRunChangeServiceInfoFn = oldInfo
		if fetches == 0 || fetches%2 != 0 {
			t.Errorf("ServiceInfo fetches = %d, want initial/post-success pairs", fetches)
		}
	})
}

func stubExistingNativeSandboxInfo(t *testing.T) {
	t.Helper()
	oldInfo := fetchRunChangeServiceInfoFn
	fetches := 0
	fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		fetches++
		return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
			ServiceType: "systemd",
			Sandbox:     &catchrpc.ServiceSandbox{State: "on"},
		}}, nil
	}
	t.Cleanup(func() {
		fetchRunChangeServiceInfoFn = oldInfo
		if fetches == 0 {
			t.Error("ServiceInfo fetches = 0, want authoritative existing-service fetch")
		}
	})
}

func TestFreshRunPostSuccessSandboxFetchUsesNativePayloadSemantics(t *testing.T) {
	oldInfo := fetchRunChangeServiceInfoFn
	oldHashes := fetchRemoteArtifactHashesFn
	t.Cleanup(func() {
		fetchRunChangeServiceInfoFn = oldInfo
		fetchRemoteArtifactHashesFn = oldHashes
	})
	fetchRemoteArtifactHashesFn = func(context.Context, string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{}, false, nil
	}
	tmp := t.TempDir()
	script := filepath.Join(tmp, "job.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile script: %v", err)
	}
	compose := filepath.Join(tmp, "compose.yml")
	if err := os.WriteFile(compose, []byte("services:\n  app:\n    image: alpine\n"), 0o644); err != nil {
		t.Fatalf("WriteFile compose: %v", err)
	}
	unknown := filepath.Join(tmp, "payload.unknown")
	if err := os.WriteFile(unknown, []byte("not a native executable\n"), 0o644); err != nil {
		t.Fatalf("WriteFile unknown: %v", err)
	}
	python := filepath.Join(tmp, "job.py")
	if err := os.WriteFile(python, []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile Python: %v", err)
	}
	typescript := filepath.Join(tmp, "job.ts")
	if err := os.WriteFile(typescript, []byte("console.log('ok')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile TypeScript: %v", err)
	}

	tests := []struct {
		name        string
		payload     string
		entry       ServiceEntry
		runArgs     []string
		always      bool
		wantFetches int
	}{
		{name: "autodetected script", payload: script, entry: ServiceEntry{Name: "api", Host: "host-a"}, wantFetches: 2},
		{name: "declared native file", payload: script, entry: ServiceEntry{Name: "api", Host: "host-a", PayloadKind: "file"}, wantFetches: 2},
		{name: "timer", payload: script, entry: ServiceEntry{Name: "api", Host: "host-a", PayloadKind: "file"}, runArgs: []string{"--cron=0 3 * * *"}, wantFetches: 2},
		{name: "compose", payload: compose, entry: ServiceEntry{Name: "api", Host: "host-a", PayloadKind: "compose"}, wantFetches: 1},
		{name: "explicit file Python", payload: python, entry: ServiceEntry{Name: "api", Host: "host-a", PayloadKind: "file"}, wantFetches: 1},
		{name: "stored file TypeScript", payload: typescript, entry: ServiceEntry{Name: "api", Host: "host-a", PayloadKind: "file"}, wantFetches: 1},
		{name: "remote image", payload: "ghcr.io/example/app:latest", entry: ServiceEntry{Name: "api", Host: "host-a", PayloadKind: "remote-image"}, wantFetches: 1},
		{name: "vm", payload: "vm://ubuntu/26.04", entry: ServiceEntry{Name: "api", Host: "host-a", PayloadKind: serviceTypeVM}, always: true, wantFetches: 0},
		{name: "catch helper", payload: script, entry: ServiceEntry{Name: catchServiceName, Host: "host-a", PayloadKind: "file"}, wantFetches: 1},
		{name: "system helper", payload: script, entry: ServiceEntry{Name: systemServiceName, Host: "host-a", PayloadKind: "file"}, wantFetches: 1},
		{name: "unknown payload", payload: unknown, entry: ServiceEntry{Name: "api", Host: "host-a"}, wantFetches: 1},
		{name: "draft without service identity", payload: script, entry: ServiceEntry{}, wantFetches: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetches := 0
			fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
				fetches++
				if fetches == 1 {
					return catchrpc.ServiceInfoResponse{Found: false}, nil
				}
				return catchrpc.ServiceInfoResponse{Found: true, Info: catchrpc.ServiceInfo{
					ServiceType: "systemd", Sandbox: &catchrpc.ServiceSandbox{State: "on"},
				}}, nil
			}
			runs := 0
			err := runWithChangesToWithContextRunner(
				context.Background(), io.Discard, tt.payload, tt.runArgs, "", tt.entry, false,
				func(context.Context, string, []string) error { runs++; return nil }, tt.always,
			)
			if err != nil {
				t.Fatalf("runWithChangesToWithContextRunner: %v", err)
			}
			if runs != 1 || fetches != tt.wantFetches {
				t.Fatalf("runs/fetches = %d/%d, want 1/%d", runs, fetches, tt.wantFetches)
			}
		})
	}
}

func TestFreshFilePythonAndTypeScriptSkipNativePostSuccessFetch(t *testing.T) {
	oldInfo := fetchRunChangeServiceInfoFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldService := serviceOverride
	t.Cleanup(func() {
		fetchRunChangeServiceInfoFn = oldInfo
		fetchRemoteArtifactHashesFn = oldHashes
		serviceOverride = oldService
	})
	serviceOverride = "api"
	fetchRemoteArtifactHashesFn = func(context.Context, string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{}, false, nil
	}
	for _, tt := range []struct {
		name    string
		path    string
		content string
	}{
		{name: "Python", path: "job.py", content: "print('ok')\n"},
		{name: "TypeScript", path: "job.ts", content: "console.log('ok')\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			payload := filepath.Join(t.TempDir(), tt.path)
			if err := os.WriteFile(payload, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			fetches := 0
			fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
				fetches++
				if fetches == 1 {
					return catchrpc.ServiceInfoResponse{Found: false}, nil
				}
				return catchrpc.ServiceInfoResponse{}, errors.New("native-only post-success fetch sentinel")
			}
			runs := 0
			result, err := runWithChangesToWithContextRunnerResult(
				context.Background(), io.Discard, payload, nil, "",
				ServiceEntry{Name: "api", Host: "host-a", PayloadKind: "file"}, false,
				func(context.Context, string, []string) error { runs++; return nil }, false,
			)
			if err != nil {
				t.Fatalf("run returned unnecessary post-success fetch error: %v", err)
			}
			if runs != 1 || fetches != 1 {
				t.Fatalf("runs/fetches = %d/%d, want 1/1", runs, fetches)
			}
			loc := &projectConfigLocation{Path: filepath.Join(t.TempDir(), projectConfigName), Config: &ProjectConfig{Version: projectConfigVersion}}
			loc.Config.SetServiceEntry(ServiceEntry{Name: "api", Host: "host-a", Sandbox: "legacy"})
			if err := saveRunConfigWithPayloadKindResult(loc, "host-a", payload, "file", nil, "", false, result); err != nil {
				t.Fatal(err)
			}
			entry, _ := loc.Config.ServiceEntry("api", "host-a")
			if entry.Sandbox != "" || len(entry.SandboxRO) != 0 || len(entry.SandboxRW) != 0 {
				t.Fatalf("generated non-native file kept stale sandbox: %#v", entry)
			}
		})
	}
}

func TestRunNetworkGuardFailsClosedOnServiceInfoError(t *testing.T) {
	oldInfo := fetchRunChangeServiceInfoFn
	defer func() { fetchRunChangeServiceInfoFn = oldInfo }()
	want := errors.New("service info unavailable")
	fetchRunChangeServiceInfoFn = func(context.Context, string, string) (catchrpc.ServiceInfoResponse, error) {
		return catchrpc.ServiceInfoResponse{}, want
	}

	err := rejectExistingRunNetworkChange(
		context.Background(), ServiceEntry{Name: "api", Host: "catch.example"}, []string{"--net=iso"},
	)
	if !errors.Is(err, want) {
		t.Fatalf("rejectExistingRunNetworkChange = %v, want %v", err, want)
	}
}

func TestServiceEntryForConfigAndHasServiceConfig(t *testing.T) {
	oldService := serviceOverride
	oldPrefs := loadedPrefs
	defer func() {
		serviceOverride = oldService
		loadedPrefs = oldPrefs
	}()
	loadedPrefs.DefaultHost = "host-a"
	cfg := &ProjectConfig{}
	cfg.SetServiceEntry(ServiceEntry{Name: "svc-a", Host: "host-a", Payload: "run.sh"})
	loc := &projectConfigLocation{Config: cfg}

	serviceOverride = ""
	if hasServiceConfig(loc, "") {
		t.Fatal("hasServiceConfig without service override = true, want false")
	}

	serviceOverride = "svc-a"
	entry, ok := serviceEntryForConfig(loc, "")
	if !ok || entry.Payload != "run.sh" {
		t.Fatalf("serviceEntryForConfig = %#v %v, want saved entry", entry, ok)
	}
	if !hasServiceConfig(loc, "") {
		t.Fatal("hasServiceConfig saved entry = false, want true")
	}
	if hasServiceConfig(loc, "host-b") {
		t.Fatal("hasServiceConfig wrong host = true, want false")
	}
	if hasServiceConfig(nil, "") {
		t.Fatal("hasServiceConfig nil config = true, want false")
	}
}

func TestSaveEnvFileConfigSkipsEmptyInputs(t *testing.T) {
	oldService := serviceOverride
	defer func() { serviceOverride = oldService }()

	serviceOverride = ""
	if err := saveEnvFileConfig(nil, "", ".env"); err != nil {
		t.Fatalf("saveEnvFileConfig empty service error: %v", err)
	}
	serviceOverride = "svc-a"
	if err := saveEnvFileConfig(nil, "", " "); err != nil {
		t.Fatalf("saveEnvFileConfig empty env error: %v", err)
	}
}

func TestDetectRunChangesSummaries(t *testing.T) {
	oldArch := remoteCatchOSAndArchFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldService := serviceOverride
	defer func() {
		remoteCatchOSAndArchFn = oldArch
		fetchRemoteArtifactHashesFn = oldHashes
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "main.py")
	if err := os.WriteFile(payload, []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatalf("failed to write payload: %v", err)
	}
	envFile := filepath.Join(tmp, "envfile")
	if err := os.WriteFile(envFile, []byte("KEY=VALUE\n"), 0o600); err != nil {
		t.Fatalf("failed to write env file: %v", err)
	}
	payloadHash, err := hashFileSHA256(payload)
	if err != nil {
		t.Fatalf("hash payload: %v", err)
	}
	envHash, err := hashFileSHA256(envFile)
	if err != nil {
		t.Fatalf("hash env: %v", err)
	}

	artifact := func(kind, sha string) *catchrpc.ArtifactHash {
		return &catchrpc.ArtifactHash{Kind: kind, SHA256: sha}
	}

	tests := []struct {
		name      string
		runArgs   []string
		envFile   string
		stored    []string
		response  catchrpc.ArtifactHashesResponse
		supported bool
		want      runChangeSummary
	}{
		{
			name:      "args changed only",
			runArgs:   []string{"--pull"},
			stored:    nil,
			response:  catchrpc.ArtifactHashesResponse{Found: true, Payload: artifact("python", payloadHash)},
			supported: true,
			want: runChangeSummary{
				argsChanged:  true,
				payloadLabel: "python file",
			},
		},
		{
			name:      "matching hashes have no changes",
			envFile:   envFile,
			stored:    []string{},
			response:  catchrpc.ArtifactHashesResponse{Found: true, Payload: artifact("python", payloadHash), Env: artifact("env file", envHash)},
			supported: true,
			want: runChangeSummary{
				payloadLabel: "python file",
			},
		},
		{
			name:      "unsupported remote marks hash-backed artifacts changed",
			envFile:   envFile,
			stored:    []string{},
			supported: false,
			want: runChangeSummary{
				payloadChanged: true,
				envChanged:     true,
				payloadLabel:   "python file",
			},
		},
		{
			name:      "remote kind labels changed payload",
			stored:    []string{},
			response:  catchrpc.ArtifactHashesResponse{Found: true, Payload: artifact("binary", "deadbeef")},
			supported: true,
			want: runChangeSummary{
				payloadChanged: true,
				payloadLabel:   "binary",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
				if service != "svc-a" {
					t.Fatalf("service = %q, want svc-a", service)
				}
				return tt.response, tt.supported, nil
			}

			got, err := detectRunChanges(payload, tt.runArgs, tt.envFile, tt.stored)
			if err != nil {
				t.Fatalf("detectRunChanges error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("summary = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestPayloadLabelFromLocal(t *testing.T) {
	oldArch := remoteCatchOSAndArchFn
	defer func() {
		remoteCatchOSAndArchFn = oldArch
	}()

	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}

	tmp := t.TempDir()
	write := func(name, contents string) string {
		t.Helper()
		path := filepath.Join(tmp, name)
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
		return path
	}

	script := write("run", "#!/bin/sh\necho ok\n")
	compose := write("compose.yml", "services:\n  app:\n    image: busybox\n")
	typescript := write("main.ts", "export const x: number = 1;\n")
	python := write("main.py", "print('ok')\n")
	unknown := write("readme.txt", "hello\n")

	tests := []struct {
		name       string
		payload    string
		remoteKind string
		want       string
	}{
		{name: "remote kind wins", payload: unknown, remoteKind: "docker-compose", want: "docker compose file"},
		{name: "script", payload: script, want: "script"},
		{name: "compose", payload: compose, want: "docker compose file"},
		{name: "typescript", payload: typescript, want: "typescript file"},
		{name: "python", payload: python, want: "python file"},
		{name: "unknown", payload: unknown, want: "payload"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := payloadLabelFromLocal(tt.payload, tt.remoteKind)
			if got != tt.want {
				t.Fatalf("payloadLabelFromLocal() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHashReadCloserSHA256ReturnsCloseError(t *testing.T) {
	closeErr := errors.New("close failed")
	_, err := hashReadCloserSHA256(&closeErrorReader{
		reader: strings.NewReader("payload"),
		err:    closeErr,
	})
	if !errors.Is(err, closeErr) {
		t.Fatalf("hashReadCloserSHA256 error = %v, want %v", err, closeErr)
	}
}

func TestWriteRunDeployStatus(t *testing.T) {
	tests := []struct {
		name    string
		summary runChangeSummary
		want    string
	}{
		{name: "payload label", summary: runChangeSummary{payloadChanged: true, payloadLabel: "python file"}, want: "Updated python file\n"},
		{name: "args only", summary: runChangeSummary{argsChanged: true}, want: "Updated run config\n"},
		{name: "no deploy status", summary: runChangeSummary{envChanged: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := writeRunDeployStatus(&buf, tt.summary); err != nil {
				t.Fatalf("writeRunDeployStatus error: %v", err)
			}
			if buf.String() != tt.want {
				t.Fatalf("output = %q, want %q", buf.String(), tt.want)
			}
		})
	}
}

func TestRunChangeRemoteHashHelpers(t *testing.T) {
	if got, kind := remotePayloadHash(catchrpc.ArtifactHashesResponse{}); got != "" || kind != "" {
		t.Fatalf("remotePayloadHash missing = %q %q, want empty", got, kind)
	}
	resp := catchrpc.ArtifactHashesResponse{
		Found:   true,
		Payload: &catchrpc.ArtifactHash{Kind: "python", SHA256: "payload-sha"},
		Env:     &catchrpc.ArtifactHash{SHA256: "env-sha"},
	}
	if got, kind := remotePayloadHash(resp); got != "payload-sha" || kind != "python" {
		t.Fatalf("remotePayloadHash = %q %q, want payload-sha python", got, kind)
	}
	if got := remoteEnvHash(catchrpc.ArtifactHashesResponse{}); got != "" {
		t.Fatalf("remoteEnvHash missing = %q, want empty", got)
	}
	if got := remoteEnvHash(resp); got != "env-sha" {
		t.Fatalf("remoteEnvHash = %q, want env-sha", got)
	}
}

func TestShouldAlwaysDeployPayload(t *testing.T) {
	tmp := t.TempDir()
	extensionlessFile := filepath.Join(tmp, "myapp")
	if err := os.WriteFile(extensionlessFile, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("write extensionless file: %v", err)
	}

	tests := []struct {
		payload string
		want    bool
	}{
		{payload: "ghcr.io/example/app:latest", want: true},
		{payload: "alpine", want: true},
		{payload: "myapp", want: true},
		{payload: "repo/myapp", want: true},
		{payload: "registry.local/team/app", want: true},
		{payload: "registry.local:5000/team/app", want: true},
		{payload: "/tmp/Dockerfile", want: true},
		{payload: "/tmp/run.sh"},
		{payload: "./compose.yml"},
		{payload: extensionlessFile},
	}

	for _, tt := range tests {
		t.Run(tt.payload, func(t *testing.T) {
			if got := shouldAlwaysDeployPayload(tt.payload); got != tt.want {
				t.Fatalf("shouldAlwaysDeployPayload(%q) = %v, want %v", tt.payload, got, tt.want)
			}
		})
	}
}

func TestIsRPCMethodNotFound(t *testing.T) {
	if isRPCMethodNotFound(nil) {
		t.Fatal("isRPCMethodNotFound nil = true, want false")
	}
	if !isRPCMethodNotFound(errors.New("rpc error: method not found")) {
		t.Fatal("isRPCMethodNotFound method-not-found = false, want true")
	}
	if isRPCMethodNotFound(errors.New("connection refused")) {
		t.Fatal("isRPCMethodNotFound other error = true, want false")
	}
}

func TestRunWithChangesToReturnsStatusWriteError(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldService := serviceOverride
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		fetchRemoteArtifactHashesFn = oldHashes
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("failed to write payload: %v", err)
	}
	payloadHash, err := hashFileSHA256(payload)
	if err != nil {
		t.Fatalf("hash payload: %v", err)
	}

	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{
			Found: true,
			Payload: &catchrpc.ArtifactHash{
				Kind:   "script",
				SHA256: payloadHash,
			},
		}, true, nil
	}
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		t.Fatalf("execRemoteFn should not be called")
		return nil
	}

	writeErr := errors.New("stdout failed")
	err = runWithChangesTo(errorWriter{err: writeErr}, payload, nil, "", ServiceEntry{}, false)
	if !errors.Is(err, writeErr) {
		t.Fatalf("runWithChangesTo error = %v, want %v", err, writeErr)
	}
}

func TestApplyRunChangeSummaryCopiesEnvFileToProvidedOutput(t *testing.T) {
	oldExec := execRemoteFn
	oldExecTo := execRemoteToFn
	oldService := serviceOverride
	defer func() {
		execRemoteFn = oldExec
		execRemoteToFn = oldExecTo
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"
	tmp := t.TempDir()
	envFile := filepath.Join(tmp, ".env")
	if err := os.WriteFile(envFile, []byte("A=B\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		return errors.New("execRemoteFn should not be called for output-aware env copy")
	}
	execRemoteToFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool, stdout io.Writer) error {
		if service != "svc-a" || !reflect.DeepEqual(args, []string{"env", "copy"}) || tty {
			t.Fatalf("execRemoteToFn = service %q args %#v tty %v, want svc-a env copy false", service, args, tty)
		}
		_, _ = io.Copy(io.Discard, stdin)
		_, err := io.WriteString(stdout, "env copy output\n")
		return err
	}
	runner := func(ctx context.Context, payload string, runArgs []string) error {
		t.Fatal("runner should not be called for env-only change")
		return nil
	}

	var out bytes.Buffer
	err := applyRunChangeSummary(context.Background(), &out, "run.sh", nil, envFile, runChangeSummary{envChanged: true}, false, runner)
	if err != nil {
		t.Fatalf("applyRunChangeSummary: %v", err)
	}
	if got, want := out.String(), "env copy output\nUpdated env file\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestApplyRunChangeSummaryCopiesEnvFileWithServiceRootOptions(t *testing.T) {
	oldExecTo := execRemoteToFn
	oldService := serviceOverride
	defer func() {
		execRemoteToFn = oldExecTo
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"
	tmp := t.TempDir()
	envFile := filepath.Join(tmp, ".env")
	if err := os.WriteFile(envFile, []byte("A=B\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	var gotArgs []string
	execRemoteToFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool, stdout io.Writer) error {
		if service != "svc-a" {
			t.Fatalf("service = %q, want svc-a", service)
		}
		gotArgs = append([]string{}, args...)
		_, _ = io.Copy(io.Discard, stdin)
		return nil
	}
	runner := func(ctx context.Context, payload string, runArgs []string) error {
		t.Fatal("runner should not be called for env-only change")
		return nil
	}

	err := applyRunChangeSummary(
		context.Background(),
		io.Discard,
		"compose.yml",
		[]string{"--service-root=flash/yeet/searxng", "--zfs", "--net=lan"},
		envFile,
		runChangeSummary{envChanged: true},
		false,
		runner,
	)
	if err != nil {
		t.Fatalf("applyRunChangeSummary: %v", err)
	}
	want := []string{"env", "copy", "--service-root=flash/yeet/searxng", "--zfs"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("env copy args = %#v, want %#v", gotArgs, want)
	}
}

func TestRunWithChangesNoChangesSkips(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldService := serviceOverride
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		fetchRemoteArtifactHashesFn = oldHashes
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("failed to write payload: %v", err)
	}
	envFile := filepath.Join(tmp, "envfile")
	if err := os.WriteFile(envFile, []byte("KEY=VALUE\n"), 0o600); err != nil {
		t.Fatalf("failed to write env file: %v", err)
	}
	payloadHash, err := hashFileSHA256(payload)
	if err != nil {
		t.Fatalf("hash payload: %v", err)
	}
	envHash, err := hashFileSHA256(envFile)
	if err != nil {
		t.Fatalf("hash env: %v", err)
	}

	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{
			Found: true,
			Payload: &catchrpc.ArtifactHash{
				Kind:   "binary",
				SHA256: payloadHash,
			},
			Env: &catchrpc.ArtifactHash{
				Kind:   "env file",
				SHA256: envHash,
			},
		}, true, nil
	}

	calls := 0
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		calls++
		return nil
	}

	if err := runWithChanges(payload, nil, envFile, ServiceEntry{}, false); err != nil {
		t.Fatalf("runWithChanges error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("expected no remote calls, got %d", calls)
	}
}

func TestRunWithChangesEnvOnly(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldService := serviceOverride
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		fetchRemoteArtifactHashesFn = oldHashes
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("failed to write payload: %v", err)
	}
	envFile := filepath.Join(tmp, "envfile")
	if err := os.WriteFile(envFile, []byte("KEY=VALUE\n"), 0o600); err != nil {
		t.Fatalf("failed to write env file: %v", err)
	}
	payloadHash, err := hashFileSHA256(payload)
	if err != nil {
		t.Fatalf("hash payload: %v", err)
	}

	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{
			Found: true,
			Payload: &catchrpc.ArtifactHash{
				Kind:   "binary",
				SHA256: payloadHash,
			},
			Env: &catchrpc.ArtifactHash{
				Kind:   "env file",
				SHA256: "deadbeef",
			},
		}, true, nil
	}

	var calls [][]string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		calls = append(calls, append([]string{}, args...))
		return nil
	}

	if err := runWithChanges(payload, nil, envFile, ServiceEntry{}, false); err != nil {
		t.Fatalf("runWithChanges error: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected one remote call, got %d", len(calls))
	}
	if len(calls[0]) < 2 || calls[0][0] != "env" || calls[0][1] != "copy" {
		t.Fatalf("expected env copy call, got %v", calls[0])
	}
}

func TestRunWithChangesContextEnvOnly(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldService := serviceOverride
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		fetchRemoteArtifactHashesFn = oldHashes
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("failed to write payload: %v", err)
	}
	envFile := filepath.Join(tmp, "envfile")
	if err := os.WriteFile(envFile, []byte("KEY=VALUE\n"), 0o600); err != nil {
		t.Fatalf("failed to write env file: %v", err)
	}
	payloadHash, err := hashFileSHA256(payload)
	if err != nil {
		t.Fatalf("hash payload: %v", err)
	}

	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "web-run")
	hashContextSeen := false
	execContextSeen := false
	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		hashContextSeen = ctx.Value(contextKey{}) == "web-run"
		return catchrpc.ArtifactHashesResponse{
			Found: true,
			Payload: &catchrpc.ArtifactHash{
				Kind:   "binary",
				SHA256: payloadHash,
			},
			Env: &catchrpc.ArtifactHash{
				Kind:   "env file",
				SHA256: "deadbeef",
			},
		}, true, nil
	}
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		execContextSeen = ctx.Value(contextKey{}) == "web-run"
		return nil
	}

	if err := runWithChangesContext(ctx, payload, nil, envFile, ServiceEntry{}, false); err != nil {
		t.Fatalf("runWithChangesContext error: %v", err)
	}
	if !hashContextSeen {
		t.Fatal("artifact hash lookup did not receive runWithChangesContext context")
	}
	if !execContextSeen {
		t.Fatal("env copy did not receive runWithChangesContext context")
	}
}

func TestRunWithChangesNoChangesForceDeploys(t *testing.T) {
	oldExec := execRemoteFn
	oldArch := remoteCatchOSAndArchFn
	oldHashes := fetchRemoteArtifactHashesFn
	oldService := serviceOverride
	defer func() {
		execRemoteFn = oldExec
		remoteCatchOSAndArchFn = oldArch
		fetchRemoteArtifactHashesFn = oldHashes
		serviceOverride = oldService
	}()

	serviceOverride = "svc-a"
	remoteCatchOSAndArchFn = func() (string, string, error) {
		return "linux", "amd64", nil
	}

	tmp := t.TempDir()
	payload := filepath.Join(tmp, "run.sh")
	if err := os.WriteFile(payload, []byte("#!/bin/sh\necho ok\n"), 0o700); err != nil {
		t.Fatalf("failed to write payload: %v", err)
	}
	envFile := filepath.Join(tmp, "envfile")
	if err := os.WriteFile(envFile, []byte("KEY=VALUE\n"), 0o600); err != nil {
		t.Fatalf("failed to write env file: %v", err)
	}
	payloadHash, err := hashFileSHA256(payload)
	if err != nil {
		t.Fatalf("hash payload: %v", err)
	}
	envHash, err := hashFileSHA256(envFile)
	if err != nil {
		t.Fatalf("hash env: %v", err)
	}

	fetchRemoteArtifactHashesFn = func(ctx context.Context, service string) (catchrpc.ArtifactHashesResponse, bool, error) {
		return catchrpc.ArtifactHashesResponse{
			Found: true,
			Payload: &catchrpc.ArtifactHash{
				Kind:   "binary",
				SHA256: payloadHash,
			},
			Env: &catchrpc.ArtifactHash{
				Kind:   "env file",
				SHA256: envHash,
			},
		}, true, nil
	}

	var calls [][]string
	execRemoteFn = func(ctx context.Context, service string, args []string, stdin io.Reader, tty bool) error {
		calls = append(calls, append([]string{}, args...))
		return nil
	}

	entry := ServiceEntry{Args: []string{"--pull"}}
	if err := runWithChanges(payload, []string{"--pull"}, envFile, entry, true); err != nil {
		t.Fatalf("runWithChanges error: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected one remote call, got %d", len(calls))
	}
	if len(calls[0]) < 2 || calls[0][0] != "run" || calls[0][1] != "--pull" {
		t.Fatalf("expected run call with --pull, got %v", calls[0])
	}
}

func TestSaveEnvFileConfigStoresRelativePath(t *testing.T) {
	oldService := serviceOverride
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	defer func() {
		serviceOverride = oldService
		_ = os.Chdir(cwd)
	}()

	serviceOverride = "svc-a"
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	envPath := filepath.Join(tmp, "prod.env")
	if err := os.WriteFile(envPath, []byte("KEY=VALUE\n"), 0o600); err != nil {
		t.Fatalf("failed to write env file: %v", err)
	}
	loc := &projectConfigLocation{Path: filepath.Join(tmp, projectConfigName), Dir: tmp, Config: &ProjectConfig{Version: projectConfigVersion}}
	if err := saveEnvFileConfig(loc, "host-a", envPath); err != nil {
		t.Fatalf("saveEnvFileConfig error: %v", err)
	}
	loaded, err := loadProjectConfigFromCwd()
	if err != nil {
		t.Fatalf("loadProjectConfigFromCwd error: %v", err)
	}
	entry, ok := loaded.Config.ServiceEntry("svc-a", "host-a")
	if !ok {
		t.Fatalf("expected service config to be saved")
	}
	if entry.EnvFile != "prod.env" {
		t.Fatalf("env file = %q, want %q", entry.EnvFile, "prod.env")
	}
}

func TestSaveEnvFileConfigSkipsPersistenceWhenWorkspaceDeclined(t *testing.T) {
	t.Setenv("CATCH_HOST", "")
	oldService := serviceOverride
	oldPrompt := activePrompter
	oldWarn := warnProjectConfigNotSavedFn
	oldIsTerminal := isTerminalFn
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir error: %v", err)
	}
	defer func() { _ = os.Chdir(cwd) }()
	restore := stubClientConfigState(t, clientConfig{DefaultHost: "yeet-lab"})
	defer restore()
	serviceOverride = "api"
	activePrompter = fakePrompter{selection: workspaceSelection{Choice: workspacePromptRunOnce}}
	isTerminalFn = func(int) bool { return true }
	defer func() {
		serviceOverride = oldService
		activePrompter = oldPrompt
		warnProjectConfigNotSavedFn = oldWarn
		isTerminalFn = oldIsTerminal
	}()
	var warned string
	warnProjectConfigNotSavedFn = func(reason string) { warned = reason }

	if err := saveEnvFileConfig(nil, "yeet-lab", ".env"); err != nil {
		t.Fatalf("saveEnvFileConfig error: %v", err)
	}
	if warned == "" {
		t.Fatal("warning not emitted")
	}
}

func TestEnsureLockedRunFlagsAcceptsNetworkChanges(t *testing.T) {
	entry := ServiceEntry{
		Name: "svc-a",
		Host: "host-a",
		Args: []string{"--net=ts", "--ts-tags=tag:a"},
	}
	if err := ensureLockedRunFlags(entry, []string{"--net=ts", "--ts-tags=tag:a"}); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if err := ensureLockedRunFlags(entry, []string{"--net=lan"}); err != nil {
		t.Fatalf("--net change rejected: %v", err)
	}
	if err := ensureLockedRunFlags(entry, []string{"--net=ts", "--ts-tags=tag:b"}); err != nil {
		t.Fatalf("--ts-tags change rejected: %v", err)
	}
}

func TestExtractForceFlag(t *testing.T) {
	force, args, err := extractForceFlag([]string{"--pull", "--force", "--", "--force"})
	if err != nil {
		t.Fatalf("extractForceFlag error: %v", err)
	}
	if !force {
		t.Fatalf("expected force to be true")
	}
	want := []string{"--pull", "--", "--force"}
	if len(args) != len(want) {
		t.Fatalf("args len = %d, want %d (%v)", len(args), len(want), args)
	}
	for i := range args {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestExtractForceFlagInvalidValue(t *testing.T) {
	if _, _, err := extractForceFlag([]string{"--force=not-a-bool"}); err == nil {
		t.Fatalf("expected invalid --force value error")
	}
}
