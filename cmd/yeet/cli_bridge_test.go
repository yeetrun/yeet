// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/yeetrun/yeet/pkg/cli"
)

func TestBridgeServiceArgsSkipsFlagValues(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"status", "--format", "json", "svc-a"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if !ok {
		t.Fatalf("expected to recognize remote command")
	}
	if service != "svc-a" {
		t.Fatalf("expected service svc-a, got %q", service)
	}
	if host != "" {
		t.Fatalf("expected no host, got %q", host)
	}
	if got := strings.Join(bridged, " "); got != "status --format json" {
		t.Fatalf("unexpected bridged args: %s", got)
	}
}

func TestBridgeServiceArgsDoesNotBridgeLocalOrEmptyCommands(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()

	tests := [][]string{
		nil,
		{"copy", "src", "dst"},
		{"docker", "push", "svc-a", "image:tag"},
		{"service", "sync", "svc-a"},
		{"service", "sync", "--all"},
		{"service", "sync", "--config", "./yeet.toml", "svc-a"},
		{"docker"},
		{"unknown", "svc-a"},
		{"env", "bogus", "svc-a"},
	}
	for _, args := range tests {
		service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
		if ok || service != "" || host != "" || bridged != nil {
			t.Fatalf("bridgeServiceArgs(%v) = service=%q host=%q bridged=%v ok=%v, want no bridge", args, service, host, bridged, ok)
		}
	}
}

func TestBridgeServiceArgsServiceHostQualified(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"status", "svc-a@host-a"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if !ok {
		t.Fatalf("expected to recognize remote command")
	}
	if service != "svc-a" {
		t.Fatalf("expected service svc-a, got %q", service)
	}
	if host != "host-a" {
		t.Fatalf("expected host host-a, got %q", host)
	}
	if got := strings.Join(bridged, " "); got != "status" {
		t.Fatalf("unexpected bridged args: %s", got)
	}
}

func TestBridgeServiceArgsTerminatorTreatsFollowingTokenAsService(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"run", "--force", "--", "svc-a", "--app-flag"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if !ok {
		t.Fatalf("expected to bridge service after terminator")
	}
	if service != "svc-a" || host != "" {
		t.Fatalf("service=%q host=%q, want svc-a and empty host", service, host)
	}
	if got := strings.Join(bridged, " "); got != "run --force -- --app-flag" {
		t.Fatalf("bridged args = %q", got)
	}
}

func TestHandleRemoteUsesArgsWithoutBridge(t *testing.T) {
	oldHandle := handleSvcCmdFn
	oldBridged := bridgedArgs
	defer func() {
		handleSvcCmdFn = oldHandle
		bridgedArgs = oldBridged
	}()

	var got []string
	handleSvcCmdFn = func(args []string) error {
		got = append([]string{}, args...)
		return nil
	}

	bridgedArgs = nil

	if err := handleRemote(context.Background(), []string{"status", "--format", "json"}); err != nil {
		t.Fatalf("handleRemote returned error: %v", err)
	}
	if got := strings.Join(got, " "); got != "status --format json" {
		t.Fatalf("unexpected forwarded args: %s", got)
	}
}

func TestBridgeServiceArgsNoServiceDoesNotBridge(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"status", "--format", "json"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if ok {
		t.Fatalf("expected no bridging, got service=%q bridged=%v", service, bridged)
	}
	if host != "" {
		t.Fatalf("expected no host, got %q", host)
	}
}

func TestHandleSvcCmdForwardsArgsOverRPC(t *testing.T) {
	oldHandle := handleSvcCmdFn
	oldBridged := bridgedArgs
	defer func() {
		handleSvcCmdFn = oldHandle
		bridgedArgs = oldBridged
	}()

	var got []string
	handleSvcCmdFn = func(args []string) error {
		got = append([]string{}, args...)
		return nil
	}

	bridgedArgs = []string{"status", "--format", "json"}

	if err := handleRemote(context.Background(), []string{"status"}); err != nil {
		t.Fatalf("handleRemote returned error: %v", err)
	}
	if got := strings.Join(got, " "); got != "status --format json" {
		t.Fatalf("unexpected forwarded args: %s", got)
	}
}

func TestBridgeServiceArgsWithRepeatedArrayFlags(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"run", "--ts-tags", "a", "--ts-tags", "b", "svc-a"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if !ok {
		t.Fatalf("expected to recognize remote command")
	}
	if service != "svc-a" {
		t.Fatalf("expected service svc-a, got %q", service)
	}
	if host != "" {
		t.Fatalf("expected no host, got %q", host)
	}
	if got := strings.Join(bridged, " "); got != "run --ts-tags a --ts-tags b" {
		t.Fatalf("unexpected bridged args: %s", got)
	}
}

func TestBridgeServiceArgsWithEnvFileFlag(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"run", "--env-file", "prod.env", "svc-a", "./compose.yml"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if !ok {
		t.Fatalf("expected to recognize remote command")
	}
	if service != "svc-a" {
		t.Fatalf("expected service svc-a, got %q", service)
	}
	if host != "" {
		t.Fatalf("expected no host, got %q", host)
	}
	if got := strings.Join(bridged, " "); got != "run --env-file prod.env ./compose.yml" {
		t.Fatalf("unexpected bridged args: %s", got)
	}
}

func TestBridgeServiceArgsWithForceFlag(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"run", "--force", "svc-a", "./compose.yml"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if !ok {
		t.Fatalf("expected to recognize remote command")
	}
	if service != "svc-a" {
		t.Fatalf("expected service svc-a, got %q", service)
	}
	if host != "" {
		t.Fatalf("expected no host, got %q", host)
	}
	if got := strings.Join(bridged, " "); got != "run --force ./compose.yml" {
		t.Fatalf("unexpected bridged args: %s", got)
	}
}

func TestBridgeServiceArgsWithZFSServiceRoot(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"run", "--service-root=tank/apps/svc-a", "--zfs", "svc-a", "./compose.yml"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if !ok {
		t.Fatalf("expected to recognize remote command")
	}
	if service != "svc-a" {
		t.Fatalf("expected service svc-a, got %q", service)
	}
	if host != "" {
		t.Fatalf("expected no host, got %q", host)
	}
	if got := strings.Join(bridged, " "); got != "run --service-root=tank/apps/svc-a --zfs ./compose.yml" {
		t.Fatalf("unexpected bridged args: %s", got)
	}
}

func TestBridgeServiceArgsRunWeb(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"run", "--web", "svc-a", "./compose.yml"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if !ok {
		t.Fatal("bridgeServiceArgs ok=false, want true")
	}
	if service != "svc-a" || host != "" {
		t.Fatalf("service/host = %q/%q, want svc-a/empty", service, host)
	}
	want := []string{"run", "--web", "./compose.yml"}
	if !reflect.DeepEqual(bridged, want) {
		t.Fatalf("bridged = %#v, want %#v", bridged, want)
	}
}

func TestBridgeServiceArgsServiceSet(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	tests := []struct {
		name        string
		args        []string
		wantService string
		wantHost    string
		wantBridged string
		wantOK      bool
	}{
		{
			name:        "inline root after service",
			args:        []string{"service", "set", "svc-a", "--service-root=/srv/apps/svc-a"},
			wantService: "svc-a",
			wantBridged: "service set --service-root=/srv/apps/svc-a",
			wantOK:      true,
		},
		{
			name:        "root flag before service",
			args:        []string{"service", "set", "--service-root", "/srv/apps/svc-a", "svc-a"},
			wantService: "svc-a",
			wantBridged: "service set --service-root /srv/apps/svc-a",
			wantOK:      true,
		},
		{
			name:        "service set zfs root",
			args:        []string{"service", "set", "svc-a", "--service-root=tank/apps/svc-a", "--zfs"},
			wantService: "svc-a",
			wantHost:    "",
			wantBridged: "service set --service-root=tank/apps/svc-a --zfs",
			wantOK:      true,
		},
		{
			name:        "service set publish shorthand",
			args:        []string{"service", "set", "svc-a", "-p", "80:80"},
			wantService: "svc-a",
			wantHost:    "",
			wantBridged: "service set -p 80:80",
			wantOK:      true,
		},
		{
			name:        "vm shape flags after service",
			args:        []string{"service", "set", "devbox", "--cpus=8", "--memory", "8g", "--disk=128g"},
			wantService: "devbox",
			wantHost:    "",
			wantBridged: "service set --cpus=8 --memory 8g --disk=128g",
			wantOK:      true,
		},
		{
			name:        "vm net flags before service",
			args:        []string{"service", "set", "--net", "lan", "--macvlan-parent=vmbr0", "devbox"},
			wantService: "devbox",
			wantHost:    "",
			wantBridged: "service set --net lan --macvlan-parent=vmbr0",
			wantOK:      true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, host, bridged, ok := bridgeServiceArgs(tt.args, remoteSpecs, groupSpecs, "")
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if service != tt.wantService {
				t.Fatalf("service = %q, want %q", service, tt.wantService)
			}
			if host != tt.wantHost {
				t.Fatalf("host = %q, want %q", host, tt.wantHost)
			}
			if got := strings.Join(bridged, " "); got != tt.wantBridged {
				t.Fatalf("bridged args = %q, want %q", got, tt.wantBridged)
			}
		})
	}
}

func TestBridgeServiceArgsServiceSetSnapshotFlags(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	service, host, bridged, ok := bridgeServiceArgs(
		[]string{"service", "set", "--snapshots=off", "--snapshot-keep-last", "3", "sabnzbd"},
		remoteSpecs,
		groupSpecs,
		"",
	)
	if !ok || service != "sabnzbd" || host != "" {
		t.Fatalf("service=%q host=%q ok=%v", service, host, ok)
	}
	want := []string{"service", "set", "--snapshots=off", "--snapshot-keep-last", "3"}
	if !reflect.DeepEqual(bridged, want) {
		t.Fatalf("bridged = %#v, want %#v", bridged, want)
	}
}

func TestBridgeServiceArgsServiceSyncDoesNotBridge(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"service", "sync", "svc-a", "--config", "./yeet.toml"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if ok || service != "" || host != "" || bridged != nil {
		t.Fatalf("bridgeServiceArgs service sync = service=%q host=%q bridged=%v ok=%v, want no bridge", service, host, bridged, ok)
	}
}

func TestBridgeServiceArgsUnknownFlagBeforeServiceTreatsNextTokenAsService(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"run", "--foo", "bar", "svc-a", "./bin"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if !ok {
		t.Fatalf("expected to recognize remote command")
	}
	if service != "bar" {
		t.Fatalf("expected service bar, got %q", service)
	}
	if host != "" {
		t.Fatalf("expected no host, got %q", host)
	}
	if got := strings.Join(bridged, " "); got != "run --foo svc-a ./bin" {
		t.Fatalf("unexpected bridged args: %s", got)
	}
}

func TestBridgeServiceArgsUnknownFlagExplicitValue(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"run", "--foo=true", "svc-a", "./bin"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if !ok {
		t.Fatalf("expected to recognize remote command")
	}
	if service != "svc-a" {
		t.Fatalf("expected service svc-a, got %q", service)
	}
	if host != "" {
		t.Fatalf("expected no host, got %q", host)
	}
	if got := strings.Join(bridged, " "); got != "run --foo=true ./bin" {
		t.Fatalf("unexpected bridged args: %s", got)
	}
}

func TestBridgeServiceArgsUnknownFlagAfterService(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"run", "svc-a", "--foo", "bar", "./bin"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if !ok {
		t.Fatalf("expected to recognize remote command")
	}
	if service != "svc-a" {
		t.Fatalf("expected service svc-a, got %q", service)
	}
	if host != "" {
		t.Fatalf("expected no host, got %q", host)
	}
	if got := strings.Join(bridged, " "); got != "run --foo bar ./bin" {
		t.Fatalf("unexpected bridged args: %s", got)
	}
}

func TestBridgeServiceArgsHonorsOverride(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"run", "svc-a", "./bin"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "svc-override")
	if !ok {
		t.Fatalf("expected to recognize remote command")
	}
	if service != "svc-override" {
		t.Fatalf("expected service svc-override, got %q", service)
	}
	if host != "" {
		t.Fatalf("expected no host, got %q", host)
	}
	if got := strings.Join(bridged, " "); got != "run svc-a ./bin" {
		t.Fatalf("unexpected bridged args: %s", got)
	}
}

func TestBridgeServiceArgsDockerGroup(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"docker", "update", "svc-a"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if ok {
		t.Fatalf("expected variadic docker update to stay unbridged, got service=%q host=%q bridged=%v", service, host, bridged)
	}
	if service != "" || host != "" || bridged != nil {
		t.Fatalf("bridge result = service=%q host=%q bridged=%v, want empty", service, host, bridged)
	}
}

func TestBridgeServiceArgsDockerUpdateVariadicDoesNotBridge(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"docker", "update", "svc-a", "svc-b@host-b"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if ok {
		t.Fatalf("expected variadic docker update to stay unbridged, got service=%q host=%q bridged=%v", service, host, bridged)
	}
	if service != "" || host != "" || bridged != nil {
		t.Fatalf("bridge result = service=%q host=%q bridged=%v, want empty", service, host, bridged)
	}
}

func TestBridgeServiceArgsDockerUpdateOutdatedScopedForRejection(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"docker", "update", "--outdated", "svc-a@host-a"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if ok {
		t.Fatalf("expected docker update --outdated service arg to stay unbridged, got service=%q host=%q bridged=%v", service, host, bridged)
	}
	if service != "" || host != "" || bridged != nil {
		t.Fatalf("bridge result = service=%q host=%q bridged=%v, want empty", service, host, bridged)
	}
}

func TestBridgeServiceArgsDockerGroupNoServiceDoesNotBridge(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"docker", "pull"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if ok {
		t.Fatalf("expected no bridging, got service=%q bridged=%v", service, bridged)
	}
	if host != "" {
		t.Fatalf("expected no host, got %q", host)
	}
}

func TestBridgeServiceArgsDockerOutdatedScoped(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"docker", "outdated", "--format=json", "svc-a"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if !ok {
		t.Fatalf("expected to recognize docker outdated group command")
	}
	if service != "svc-a" {
		t.Fatalf("service = %q, want svc-a", service)
	}
	if host != "" {
		t.Fatalf("host = %q, want empty", host)
	}
	if got := strings.Join(bridged, " "); got != "docker outdated --format=json" {
		t.Fatalf("bridged = %q, want docker outdated --format=json", got)
	}
}

func TestBridgeServiceArgsDockerOutdatedNoServiceDoesNotBridge(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"docker", "outdated", "--format=json"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if ok {
		t.Fatalf("expected unscoped command to stay local, got service=%q host=%q bridged=%v", service, host, bridged)
	}
}

func TestBridgeServiceArgsDockerPushDoesNotBridge(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"docker", "push", "svc-a", "image:tag"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if ok {
		t.Fatalf("expected no bridging, got service=%q bridged=%v", service, bridged)
	}
	if host != "" {
		t.Fatalf("expected no host, got %q", host)
	}
}

func TestBridgeServiceArgsEnvGroup(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"env", "show", "svc-a", "--staged"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if !ok {
		t.Fatalf("expected to recognize env group command")
	}
	if service != "svc-a" {
		t.Fatalf("expected service svc-a, got %q", service)
	}
	if host != "" {
		t.Fatalf("expected no host, got %q", host)
	}
	if got := strings.Join(bridged, " "); got != "env show --staged" {
		t.Fatalf("unexpected bridged args: %s", got)
	}
}

func TestBridgeServiceArgsEnvSetGroup(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"env", "set", "svc-a", "FOO=bar"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if !ok {
		t.Fatalf("expected to recognize env set group command")
	}
	if service != "svc-a" {
		t.Fatalf("expected service svc-a, got %q", service)
	}
	if host != "" {
		t.Fatalf("expected no host, got %q", host)
	}
	if got := strings.Join(bridged, " "); got != "env set FOO=bar" {
		t.Fatalf("unexpected bridged args: %s", got)
	}
}

func TestBridgeServiceArgsVMConsoleGroup(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"vm", "console", "devbox@host-a"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if !ok {
		t.Fatalf("expected to recognize vm console group command")
	}
	if service != "devbox" {
		t.Fatalf("expected service devbox, got %q", service)
	}
	if host != "host-a" {
		t.Fatalf("expected host host-a, got %q", host)
	}
	if got := strings.Join(bridged, " "); got != "vm console" {
		t.Fatalf("unexpected bridged args: %s", got)
	}
}

func TestBridgeServiceArgsSkipsVMImages(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"vm", "images", "update"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if ok {
		t.Fatalf("vm images update should not bridge service args, got service=%q host=%q bridged=%v", service, host, bridged)
	}
	if service != "" || host != "" || bridged != nil {
		t.Fatalf("bridge result = service=%q host=%q bridged=%v, want empty", service, host, bridged)
	}
}

func TestBridgeServiceArgsSkipsVMImagesImport(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"vm", "images", "import", "foo/bar", "./bundle"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if ok {
		t.Fatalf("vm images import should not bridge service args, got service=%q host=%q bridged=%v", service, host, bridged)
	}
	if service != "" || host != "" || bridged != nil {
		t.Fatalf("bridge result = service=%q host=%q bridged=%v, want empty", service, host, bridged)
	}
}

func TestBridgeServiceArgsLogsServiceBeforeFollowFlag(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"logs", "service-a@yeet-edge-a", "-f"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if !ok {
		t.Fatalf("expected to recognize logs command")
	}
	if service != "service-a" {
		t.Fatalf("expected service service-a, got %q", service)
	}
	if host != "yeet-edge-a" {
		t.Fatalf("expected host yeet-edge-a, got %q", host)
	}
	if got := strings.Join(bridged, " "); got != "logs -f" {
		t.Fatalf("unexpected bridged args: %s", got)
	}
}

func TestBridgeServiceArgsLogsServiceAfterFollowFlag(t *testing.T) {
	remoteSpecs := cli.RemoteFlagSpecs()
	groupSpecs := cli.RemoteGroupFlagSpecs()
	args := []string{"logs", "-f", "service-a@yeet-edge-a"}
	service, host, bridged, ok := bridgeServiceArgs(args, remoteSpecs, groupSpecs, "")
	if !ok {
		t.Fatalf("expected to recognize logs command")
	}
	if service != "service-a" {
		t.Fatalf("expected service service-a, got %q", service)
	}
	if host != "yeet-edge-a" {
		t.Fatalf("expected host yeet-edge-a, got %q", host)
	}
	if got := strings.Join(bridged, " "); got != "logs -f" {
		t.Fatalf("unexpected bridged args: %s", got)
	}
}
