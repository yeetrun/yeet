// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
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
	if !ok {
		t.Fatalf("expected to recognize docker group command")
	}
	if service != "svc-a" {
		t.Fatalf("expected service svc-a, got %q", service)
	}
	if host != "" {
		t.Fatalf("expected no host, got %q", host)
	}
	if got := strings.Join(bridged, " "); got != "docker update" {
		t.Fatalf("unexpected bridged args: %s", got)
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
