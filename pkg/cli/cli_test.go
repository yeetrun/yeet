// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/shayne/yargs"
)

func FuzzParseRemoteArgs(f *testing.F) {
	for _, seed := range []string{
		"--net ts --ts-tags tag:a --publish 8080:80 svc",
		"--pull commit svc -- --literal",
		"--unknown value arg1",
		"--lines 10 --follow svc",
		"-p 9000:9000 --force payload",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		args := fuzzArgs(raw)

		_, _, _ = ParseRun(args)
		_, _, _, _ = ParseStage(args)
		_, _, _ = ParseRemove(args)
		_, _, _ = ParseLogs(args)
		_, _, _ = ParseStatus(args)
		_, _, _ = ParseInfo(args)
	})
}

func fuzzArgs(raw string) []string {
	const (
		maxArgs   = 32
		maxArgLen = 128
	)
	fields := strings.Fields(raw)
	if len(fields) > maxArgs {
		fields = fields[:maxArgs]
	}
	args := make([]string, len(fields))
	for i, field := range fields {
		if len(field) > maxArgLen {
			field = field[:maxArgLen]
		}
		args[i] = field
	}
	return args
}

func TestParseRunFlagsAndArgs(t *testing.T) {
	args := []string{
		"--net", "svc,ts,lan",
		"--ts-ver", "1.2.3",
		"--ts-exit", "exit-node",
		"--ts-tags", "tag:a",
		"--ts-tags", "tag:b",
		"--ts-auth-key", "tskey-abc",
		"--macvlan-mac", "00:11:22:33:44:55",
		"--macvlan-vlan", "12",
		"--macvlan-parent", "eth0",
		"--env-file", "prod.env",
		"--service-root", "tank/apps/svc-a",
		"--zfs",
		"-p", "8000:8000",
		"-p", "9000:9000",
		"--publish-reset",
		"--force",
		"--pull",
		"arg1", "arg2",
	}

	flags, outArgs, err := ParseRun(args)
	if err != nil {
		t.Fatalf("ParseRun failed: %v", err)
	}
	if flags.Net != "svc,ts,lan" {
		t.Errorf("Net = %q, want %q", flags.Net, "svc,ts,lan")
	}
	if flags.TsVer != "1.2.3" {
		t.Errorf("TsVer = %q, want %q", flags.TsVer, "1.2.3")
	}
	if flags.TsExit != "exit-node" {
		t.Errorf("TsExit = %q, want %q", flags.TsExit, "exit-node")
	}
	if flags.TsAuthKey != "tskey-abc" {
		t.Errorf("TsAuthKey = %q, want %q", flags.TsAuthKey, "tskey-abc")
	}
	if flags.MacvlanMac != "00:11:22:33:44:55" {
		t.Errorf("MacvlanMac = %q, want %q", flags.MacvlanMac, "00:11:22:33:44:55")
	}
	if flags.MacvlanVlan != 12 {
		t.Errorf("MacvlanVlan = %d, want %d", flags.MacvlanVlan, 12)
	}
	if flags.MacvlanParent != "eth0" {
		t.Errorf("MacvlanParent = %q, want %q", flags.MacvlanParent, "eth0")
	}
	if flags.EnvFile != "prod.env" {
		t.Errorf("EnvFile = %q, want %q", flags.EnvFile, "prod.env")
	}
	if flags.ServiceRoot != "tank/apps/svc-a" {
		t.Errorf("ServiceRoot = %q, want %q", flags.ServiceRoot, "tank/apps/svc-a")
	}
	if !flags.ZFS {
		t.Errorf("ZFS = false, want true")
	}
	if !flags.Pull {
		t.Errorf("Pull = false, want true")
	}
	if !flags.Force {
		t.Errorf("Force = false, want true")
	}
	if !flags.PublishReset {
		t.Errorf("PublishReset = false, want true")
	}
	wantTags := []string{"tag:a", "tag:b"}
	if !reflect.DeepEqual(flags.TsTags, wantTags) {
		t.Errorf("TsTags = %v, want %v", flags.TsTags, wantTags)
	}
	wantPublish := []string{"8000:8000", "9000:9000"}
	if !reflect.DeepEqual(flags.Publish, wantPublish) {
		t.Errorf("Publish = %v, want %v", flags.Publish, wantPublish)
	}
	if got := strings.Join(outArgs, " "); got != "arg1 arg2" {
		t.Errorf("args = %q, want %q", got, "arg1 arg2")
	}
}

func TestParseUpgrade(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    UpgradeFlags
		wantPos []string
	}{
		{name: "check all json", args: []string{"check", "--all", "--json"}, want: UpgradeFlags{All: true, JSON: true}, wantPos: []string{"check"}},
		{name: "host yes", args: []string{"--host", "edge-a", "--yes"}, want: UpgradeFlags{Host: "edge-a", Yes: true}},
		{name: "check flag alias", args: []string{"--check"}, want: UpgradeFlags{Check: true}},
		{name: "force specific version", args: []string{"--all", "--force", "--version", "v0.6.1"}, want: UpgradeFlags{All: true, Force: true, Version: "v0.6.1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, pos, err := ParseUpgrade(tt.args)
			if err != nil {
				t.Fatalf("ParseUpgrade error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("flags = %#v, want %#v", got, tt.want)
			}
			if strings.Join(pos, ",") != strings.Join(tt.wantPos, ",") {
				t.Fatalf("pos = %#v, want %#v", pos, tt.wantPos)
			}
		})
	}
}

func TestParseRunAbsoluteServiceRootWithoutZFS(t *testing.T) {
	flags, outArgs, err := ParseRun([]string{"--service-root=/srv/apps/svc-a", "payload"})
	if err != nil {
		t.Fatalf("ParseRun failed: %v", err)
	}
	if flags.ServiceRoot != "/srv/apps/svc-a" {
		t.Fatalf("ServiceRoot = %q, want /srv/apps/svc-a", flags.ServiceRoot)
	}
	if flags.ZFS {
		t.Fatal("ZFS = true, want false")
	}
	if !reflect.DeepEqual(outArgs, []string{"payload"}) {
		t.Fatalf("args = %#v, want %#v", outArgs, []string{"payload"})
	}
}

func TestParseRunSnapshotFlags(t *testing.T) {
	flags, args, err := ParseRun([]string{"--snapshots=off", "--snapshot-keep-last=3", "--snapshot-max-age=72h", "--snapshot-required=false", "payload.yml"})
	if err != nil {
		t.Fatalf("ParseRun: %v", err)
	}
	if flags.Snapshots != "off" || flags.SnapshotKeepLast != "3" || flags.SnapshotMaxAge != "72h" || flags.SnapshotRequired != "false" {
		t.Fatalf("flags = %#v", flags)
	}
	if !reflect.DeepEqual(args, []string{"payload.yml"}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestParseRunWebFlag(t *testing.T) {
	flags, args, err := ParseRun([]string{"--web", "payload.yml"})
	if err != nil {
		t.Fatalf("ParseRun: %v", err)
	}
	if !flags.Web {
		t.Fatal("Web = false, want true")
	}
	if !reflect.DeepEqual(args, []string{"payload.yml"}) {
		t.Fatalf("args = %#v, want payload", args)
	}
}

func TestParseRunVMFlags(t *testing.T) {
	flags, args, err := ParseRun([]string{
		"--cpus=4",
		"--memory=4g",
		"--disk=128g",
		"--net=svc,lan",
		"vm://ubuntu/26.04",
	})
	if err != nil {
		t.Fatalf("ParseRun: %v", err)
	}
	if flags.CPUs != 4 || flags.Memory != "4g" || flags.Disk != "128g" {
		t.Fatalf("VM flags = cpus %d memory %q disk %q", flags.CPUs, flags.Memory, flags.Disk)
	}
	if flags.Net != "svc,lan" {
		t.Fatalf("Net = %q, want svc,lan", flags.Net)
	}
	if !reflect.DeepEqual(args, []string{"vm://ubuntu/26.04"}) {
		t.Fatalf("args = %#v", args)
	}
}

func TestParseRunRejectsEmptyNetFlag(t *testing.T) {
	tests := [][]string{
		{"--net=", "vm://ubuntu/26.04"},
		{"--net", "", "vm://ubuntu/26.04"},
		{"--net", "--cpus=2", "vm://ubuntu/26.04"},
	}
	for _, args := range tests {
		if _, _, err := ParseRun(args); err == nil || !strings.Contains(err.Error(), "--net must not be empty") {
			t.Fatalf("ParseRun(%#v) error = %v, want empty --net error", args, err)
		}
	}
}

func TestParseRunRejectsEmptyNetComponents(t *testing.T) {
	for _, args := range [][]string{
		{"--net=,", "ghcr.io/example/app:latest"},
		{"--net=svc,", "ghcr.io/example/app:latest"},
		{"--net=svc,,lan", "ghcr.io/example/app:latest"},
	} {
		if _, _, err := ParseRun(args); err == nil || !strings.Contains(err.Error(), "--net must not contain empty network modes") {
			t.Fatalf("ParseRun(%#v) error = %v, want empty mode error", args, err)
		}
	}
}

func TestParseRunRejectsInvalidMacvlanVLANFlag(t *testing.T) {
	for _, tt := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "negative", args: []string{"--macvlan-vlan=-1", "vm://ubuntu/26.04"}, wantErr: "--macvlan-vlan must not be negative"},
		{name: "too large", args: []string{"--macvlan-vlan=4095", "vm://ubuntu/26.04"}, wantErr: "--macvlan-vlan must be between 1 and 4094"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := ParseRun(tt.args); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ParseRun(%#v) error = %v, want %q", tt.args, err, tt.wantErr)
			}
		})
	}
}

func TestParseRunRejectsMacvlanFlagsWithoutLAN(t *testing.T) {
	for _, args := range [][]string{
		{"--macvlan-parent=vmbr0", "ghcr.io/example/app:latest"},
		{"--net=svc", "--macvlan-vlan=4", "ghcr.io/example/app:latest"},
		{"--net=ts", "--macvlan-mac=02:00:00:00:00:42", "ghcr.io/example/app:latest"},
	} {
		if _, _, err := ParseRun(args); err == nil || !strings.Contains(err.Error(), "--macvlan-* settings require LAN networking") {
			t.Fatalf("ParseRun(%#v) error = %v, want LAN requirement", args, err)
		}
	}
}

func TestParseRunVMImagePolicy(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "default", args: []string{"vm://ubuntu/26.04"}, want: "prompt"},
		{name: "prompt", args: []string{"--image-policy=prompt", "vm://ubuntu/26.04"}, want: "prompt"},
		{name: "update", args: []string{"--image-policy=update", "vm://ubuntu/26.04"}, want: "update"},
		{name: "cached", args: []string{"--image-policy=cached", "vm://ubuntu/26.04"}, want: "cached"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags, args, err := ParseRun(tt.args)
			if err != nil {
				t.Fatalf("ParseRun: %v", err)
			}
			if flags.ImagePolicy != tt.want {
				t.Fatalf("ImagePolicy = %q, want %q", flags.ImagePolicy, tt.want)
			}
			if !reflect.DeepEqual(args, []string{"vm://ubuntu/26.04"}) {
				t.Fatalf("args = %#v, want VM payload", args)
			}
		})
	}
}

func TestParseRunRejectsInvalidVMImagePolicy(t *testing.T) {
	_, _, err := ParseRun([]string{"--image-policy=always", "vm://ubuntu/26.04"})
	if err == nil {
		t.Fatal("ParseRun succeeded with invalid image policy")
	}
	for _, want := range []string{"image-policy", "prompt", "update", "cached"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ParseRun error = %q, want %q", err.Error(), want)
		}
	}
}

func TestParseRunRejectsMissingSnapshotMode(t *testing.T) {
	tests := [][]string{
		{"--snapshots", "payload.yml"},
		{"--snapshots=off", "--snapshots"},
		{"--snapshots=off", "--snapshots", "--pull", "payload.yml"},
	}
	for _, args := range tests {
		if _, _, err := ParseRun(args); err == nil || !strings.Contains(err.Error(), "--snapshots must be on, off, or inherit") {
			t.Fatalf("ParseRun(%#v) error = %v, want snapshots value error", args, err)
		}
	}
}

func TestParseRunStopsAtUnknownFlag(t *testing.T) {
	args := []string{
		"--net", "ts",
		"--ts-tags", "tag:a",
		"--unknown", "value",
		"arg1",
	}

	flags, outArgs, err := ParseRun(args)
	if err != nil {
		t.Fatalf("ParseRun failed: %v", err)
	}
	if flags.Net != "ts" {
		t.Errorf("Net = %q, want %q", flags.Net, "ts")
	}
	wantTags := []string{"tag:a"}
	if !reflect.DeepEqual(flags.TsTags, wantTags) {
		t.Errorf("TsTags = %v, want %v", flags.TsTags, wantTags)
	}
	if got := strings.Join(outArgs, " "); got != "--unknown value arg1" {
		t.Errorf("args = %q, want %q", got, "--unknown value arg1")
	}
}

func TestParseStagePullFlag(t *testing.T) {
	args := []string{
		"--pull",
		"commit",
	}
	flags, subcmd, outArgs, err := ParseStage(args)
	if err != nil {
		t.Fatalf("ParseStage failed: %v", err)
	}
	if !flags.Pull {
		t.Fatalf("Pull = false, want true")
	}
	if subcmd != "commit" {
		t.Fatalf("subcmd = %q, want %q", subcmd, "commit")
	}
	if len(outArgs) != 0 {
		t.Fatalf("expected no args, got %v", outArgs)
	}
}

func TestParseEnvShowFlags(t *testing.T) {
	flags, outArgs, err := ParseEnvShow([]string{"--staged"})
	if err != nil {
		t.Fatalf("ParseEnvShow failed: %v", err)
	}
	if !flags.Staged {
		t.Fatalf("Staged = false, want true")
	}
	if len(outArgs) != 0 {
		t.Fatalf("expected no args, got %v", outArgs)
	}
}

func TestParseRemoveFlags(t *testing.T) {
	flags, outArgs, err := ParseRemove([]string{"-y", "--clean-config", "--clean-data"})
	if err != nil {
		t.Fatalf("ParseRemove failed: %v", err)
	}
	if !flags.Yes {
		t.Fatalf("Yes = false, want true")
	}
	if !flags.CleanConfig {
		t.Fatalf("CleanConfig = false, want true")
	}
	if !flags.CleanData {
		t.Fatalf("CleanData = false, want true")
	}
	if len(outArgs) != 0 {
		t.Fatalf("expected no args, got %v", outArgs)
	}
}

func TestParseSnapshotDefaultsSet(t *testing.T) {
	flags, args, err := ParseSnapshotDefaultsSet([]string{"--enabled=false", "--keep-last=3", "--max-age=72h", "--events=run,docker-update", "--required=false"})
	if err != nil {
		t.Fatalf("ParseSnapshotDefaultsSet: %v", err)
	}
	if len(args) != 0 {
		t.Fatalf("args = %#v, want none", args)
	}
	if flags.Enabled != "false" || flags.KeepLast != "3" || flags.MaxAge != "72h" || flags.Events != "run,docker-update" || flags.Required != "false" {
		t.Fatalf("flags = %#v", flags)
	}
}

func TestParseSnapshotDefaultsShowRejectsArgs(t *testing.T) {
	if _, err := ParseSnapshotDefaultsShow([]string{"extra"}); err == nil || !strings.Contains(err.Error(), "snapshots defaults show takes no arguments") {
		t.Fatalf("ParseSnapshotDefaultsShow error = %v, want extra args error", err)
	}
}

func TestParseSnapshotsLifecycleCommands(t *testing.T) {
	listFlags, listArgs, err := ParseSnapshotsList([]string{"svc-a", "--format=json-pretty"})
	if err != nil {
		t.Fatalf("ParseSnapshotsList: %v", err)
	}
	if listFlags.Format != "json-pretty" || len(listArgs) != 1 || listArgs[0] != "svc-a" {
		t.Fatalf("list flags=%#v args=%#v", listFlags, listArgs)
	}

	inspectFlags, inspectArgs, err := ParseSnapshotsInspect([]string{"svc-a", "yeet-abc", "--format=json"})
	if err != nil {
		t.Fatalf("ParseSnapshotsInspect: %v", err)
	}
	if inspectFlags.Format != "json" || len(inspectArgs) != 2 || inspectArgs[0] != "svc-a" || inspectArgs[1] != "yeet-abc" {
		t.Fatalf("inspect flags=%#v args=%#v", inspectFlags, inspectArgs)
	}

	createFlags, createArgs, err := ParseSnapshotsCreate([]string{"devbox", "--comment", " before upgrade ", "--full"})
	if err != nil {
		t.Fatalf("ParseSnapshotsCreate: %v", err)
	}
	if createFlags.Comment != "before upgrade" || !createFlags.Full || len(createArgs) != 1 || createArgs[0] != "devbox" {
		t.Fatalf("create flags=%#v args=%#v", createFlags, createArgs)
	}

	rmFlags, rmArgs, err := ParseSnapshotsRemove([]string{"svc-a", "yeet-abc", "--yes"})
	if err != nil {
		t.Fatalf("ParseSnapshotsRemove: %v", err)
	}
	if !rmFlags.Yes || len(rmArgs) != 2 || rmArgs[1] != "yeet-abc" {
		t.Fatalf("rm flags=%#v args=%#v", rmFlags, rmArgs)
	}
}

func TestParseSnapshotsCloneAndRestore(t *testing.T) {
	cloneFlags, cloneArgs, err := ParseSnapshotsClone([]string{"vm-a", "yeet-abc", "vm-copy", "--start"})
	if err != nil {
		t.Fatalf("ParseSnapshotsClone: %v", err)
	}
	if !cloneFlags.Start || !reflect.DeepEqual(cloneArgs, []string{"vm-a", "yeet-abc", "vm-copy"}) {
		t.Fatalf("clone flags=%#v args=%#v", cloneFlags, cloneArgs)
	}

	restoreFlags, restoreArgs, err := ParseSnapshotsRestore([]string{"vm-a", "yeet-abc", "--stop", "--start", "--yes", "--mode=full", "--generation=snapshot"})
	if err != nil {
		t.Fatalf("ParseSnapshotsRestore: %v", err)
	}
	if !restoreFlags.Stop || !restoreFlags.Start || !restoreFlags.Yes || restoreFlags.Mode != "full" || restoreFlags.Generation != "snapshot" ||
		!reflect.DeepEqual(restoreArgs, []string{"vm-a", "yeet-abc"}) {
		t.Fatalf("restore flags=%#v args=%#v", restoreFlags, restoreArgs)
	}

	defaultFlags, defaultArgs, err := ParseSnapshotsRestore([]string{"vm-a", "yeet-abc"})
	if err != nil {
		t.Fatalf("ParseSnapshotsRestore defaults: %v", err)
	}
	if defaultFlags.Mode != "disk" || defaultFlags.Generation != "current" || !reflect.DeepEqual(defaultArgs, []string{"vm-a", "yeet-abc"}) {
		t.Fatalf("restore defaults flags=%#v args=%#v", defaultFlags, defaultArgs)
	}
}

func TestSnapshotsRestoreHelpAdvertisesFullRestoreAsSupported(t *testing.T) {
	restore, ok := RemoteGroupInfos()["snapshots"].Commands["restore"]
	if !ok {
		t.Fatal("snapshots restore command missing")
	}
	if !strings.Contains(restore.Usage, "[--mode=disk|full]") {
		t.Fatalf("snapshots restore usage %q should present full restore mode", restore.Usage)
	}
	if strings.Contains(restore.Description, "validation-only") || strings.Contains(restore.Description, "refused") {
		t.Fatalf("snapshots restore description %q should not mark full restore as refused", restore.Description)
	}
	foundFullExample := false
	for _, example := range restore.Examples {
		if strings.Contains(example, "--mode=full") {
			foundFullExample = true
		}
	}
	if !foundFullExample {
		t.Fatalf("snapshots restore examples %#v should include --mode=full", restore.Examples)
	}
}

func TestParseSnapshotsLifecycleRejectsBadInput(t *testing.T) {
	if _, _, err := ParseSnapshotsList([]string{"--format=yaml"}); err == nil || !strings.Contains(err.Error(), "--format must be table, json, or json-pretty") {
		t.Fatalf("ParseSnapshotsList error = %v, want format error", err)
	}
	if _, _, err := ParseSnapshotsInspect([]string{"svc-a"}); err == nil || !strings.Contains(err.Error(), "snapshots inspect requires service and snapshot") {
		t.Fatalf("ParseSnapshotsInspect error = %v, want arity error", err)
	}
	if _, _, err := ParseSnapshotsCreate([]string{}); err == nil || !strings.Contains(err.Error(), "snapshots create requires a service") {
		t.Fatalf("ParseSnapshotsCreate error = %v, want service error", err)
	}
	if _, _, err := ParseSnapshotsRemove([]string{"svc-a"}); err == nil || !strings.Contains(err.Error(), "snapshots rm requires service and snapshot") {
		t.Fatalf("ParseSnapshotsRemove error = %v, want arity error", err)
	}
}

func TestParseSnapshotsCloneAndRestoreRejectBadInput(t *testing.T) {
	tests := []struct {
		name    string
		parse   func([]string) error
		args    []string
		wantErr string
	}{
		{
			name: "clone missing new service",
			parse: func(args []string) error {
				_, _, err := ParseSnapshotsClone(args)
				return err
			},
			args:    []string{"vm-a", "yeet-abc"},
			wantErr: "snapshots clone requires service, snapshot, and new service",
		},
		{
			name: "clone extra arg",
			parse: func(args []string) error {
				_, _, err := ParseSnapshotsClone(args)
				return err
			},
			args:    []string{"vm-a", "yeet-abc", "vm-copy", "extra"},
			wantErr: "snapshots clone requires service, snapshot, and new service",
		},
		{
			name: "restore missing snapshot",
			parse: func(args []string) error {
				_, _, err := ParseSnapshotsRestore(args)
				return err
			},
			args:    []string{"vm-a"},
			wantErr: "snapshots restore requires service and snapshot",
		},
		{
			name: "restore invalid mode",
			parse: func(args []string) error {
				_, _, err := ParseSnapshotsRestore(args)
				return err
			},
			args:    []string{"vm-a", "yeet-abc", "--mode=bogus"},
			wantErr: "--mode must be disk or full",
		},
		{
			name: "restore invalid generation",
			parse: func(args []string) error {
				_, _, err := ParseSnapshotsRestore(args)
				return err
			},
			args:    []string{"vm-a", "yeet-abc", "--generation=bogus"},
			wantErr: "--generation must be current or snapshot",
		},
		{
			name: "restore bare mode",
			parse: func(args []string) error {
				_, _, err := ParseSnapshotsRestore(args)
				return err
			},
			args:    []string{"svc", "snap", "--mode"},
			wantErr: "--mode must be disk or full",
		},
		{
			name: "restore inline empty mode",
			parse: func(args []string) error {
				_, _, err := ParseSnapshotsRestore(args)
				return err
			},
			args:    []string{"svc", "snap", "--mode="},
			wantErr: "--mode must be disk or full",
		},
		{
			name: "restore bare generation",
			parse: func(args []string) error {
				_, _, err := ParseSnapshotsRestore(args)
				return err
			},
			args:    []string{"svc", "snap", "--generation"},
			wantErr: "--generation must be current or snapshot",
		},
		{
			name: "restore inline empty generation",
			parse: func(args []string) error {
				_, _, err := ParseSnapshotsRestore(args)
				return err
			},
			args:    []string{"svc", "snap", "--generation="},
			wantErr: "--generation must be current or snapshot",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.parse(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseServiceSetFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    ServiceSetFlags
		wantOut []string
		wantErr string
	}{
		{
			name:    "absolute service root with copy",
			args:    []string{"svc-a", "--service-root=/srv/apps/svc-a", "--copy"},
			want:    ServiceSetFlags{ServiceRoot: "/srv/apps/svc-a", Copy: true},
			wantOut: []string{"svc-a"},
		},
		{
			name:    "absolute service root separate value with empty",
			args:    []string{"--service-root", "/srv/apps/svc-a", "--empty", "svc-a"},
			want:    ServiceSetFlags{ServiceRoot: "/srv/apps/svc-a", Empty: true},
			wantOut: []string{"svc-a"},
		},
		{
			name:    "zfs dataset root",
			args:    []string{"svc-a", "--service-root=tank/apps/svc-a", "--zfs", "--copy"},
			want:    ServiceSetFlags{ServiceRoot: "tank/apps/svc-a", ZFS: true, Copy: true},
			wantOut: []string{"svc-a"},
		},
		{name: "missing change", args: []string{"svc-a"}, wantErr: "service set requires settings to change"},
		{name: "rejects vm shape flags", args: []string{"svc-a", "--cpus=8"}, wantErr: "unknown flag"},
		{name: "rejects vm network flags", args: []string{"svc-a", "--net=lan"}, wantErr: "unknown flag"},
		{name: "zfs without root", args: []string{"svc-a", "--zfs"}, wantErr: "--service-root is required when --zfs is set"},
		{name: "relative root without zfs", args: []string{"svc-a", "--service-root", "apps/svc-a"}, wantErr: "--service-root must be absolute unless --zfs is set"},
		{name: "copy and empty", args: []string{"svc-a", "--service-root", "/srv/apps/svc-a", "--copy", "--empty"}, wantErr: "cannot use --copy and --empty together"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, out, err := ParseServiceSet(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseServiceSet error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseServiceSet error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("flags = %#v, want %#v", got, tt.want)
			}
			if !reflect.DeepEqual(out, tt.wantOut) {
				t.Fatalf("args = %#v, want %#v", out, tt.wantOut)
			}
		})
	}
}

func TestParseVMSetFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    VMSetFlags
		wantOut []string
		wantErr string
	}{
		{
			name:    "shape flags",
			args:    []string{"devbox", "--cpus=8", "--memory", "8g", "--disk=128g"},
			want:    VMSetFlags{CPUs: 8, Memory: "8g", Disk: "128g"},
			wantOut: []string{"devbox"},
		},
		{
			name: "network flags",
			args: []string{"--net", "svc,lan", "--macvlan-parent=vmbr0", "--macvlan-vlan=42", "--macvlan-mac=02:00:00:00:00:42", "devbox"},
			want: VMSetFlags{
				Net:           "svc,lan",
				NetworkChange: true,
				MacvlanParent: "vmbr0",
				MacvlanVlan:   42,
				MacvlanMac:    "02:00:00:00:00:42",
			},
			wantOut: []string{"devbox"},
		},
		{name: "missing change", args: []string{"devbox"}, wantErr: "vm set requires settings to change"},
		{name: "negative cpus", args: []string{"devbox", "--cpus=-1"}, wantErr: "VM CPU count must be positive"},
		{name: "negative vlan", args: []string{"devbox", "--macvlan-vlan=-1"}, wantErr: "--macvlan-vlan must not be negative"},
		{name: "too large vlan", args: []string{"devbox", "--macvlan-vlan=4095"}, wantErr: "--macvlan-vlan must be between 1 and 4094"},
		{name: "empty net inline", args: []string{"devbox", "--net="}, wantErr: "--net must not be empty"},
		{name: "empty net separate", args: []string{"devbox", "--net", ""}, wantErr: "--net must not be empty"},
		{name: "missing net value", args: []string{"devbox", "--net", "--cpus=2"}, wantErr: "--net must not be empty"},
		{name: "empty net component", args: []string{"devbox", "--net=svc,"}, wantErr: "--net must not contain empty network modes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, out, err := ParseVMSet(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseVMSet error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseVMSet error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("flags = %#v, want %#v", got, tt.want)
			}
			if !reflect.DeepEqual(out, tt.wantOut) {
				t.Fatalf("args = %#v, want %#v", out, tt.wantOut)
			}
		})
	}
}

func TestParseServiceSetSnapshotFlags(t *testing.T) {
	flags, args, err := ParseServiceSet([]string{"svc", "--snapshots=off", "--snapshot-keep-last=3", "--snapshot-max-age=72h", "--snapshot-required=false", "--snapshot-events=run"})
	if err != nil {
		t.Fatalf("ParseServiceSet: %v", err)
	}
	if len(args) != 1 || args[0] != "svc" {
		t.Fatalf("args = %#v, want svc", args)
	}
	if flags.Snapshots != "off" || flags.SnapshotKeepLast != "3" || flags.SnapshotMaxAge != "72h" || flags.SnapshotRequired != "false" || flags.SnapshotEvents != "run" {
		t.Fatalf("flags = %#v", flags)
	}
}

func TestParseServiceSetPublishFlags(t *testing.T) {
	flags, args, err := ParseServiceSet([]string{"svc", "-p", "80:80", "--publish", "443:443", "--publish-reset"})
	if err != nil {
		t.Fatalf("ParseServiceSet: %v", err)
	}
	if len(args) != 1 || args[0] != "svc" {
		t.Fatalf("args = %#v, want svc", args)
	}
	if !reflect.DeepEqual(flags.Publish, []string{"80:80", "443:443"}) {
		t.Fatalf("Publish = %#v, want two ports", flags.Publish)
	}
	if !flags.PublishReset {
		t.Fatal("PublishReset = false, want true")
	}
}

func TestParseServiceSetPublishOnlyDoesNotRequireServiceRoot(t *testing.T) {
	if _, _, err := ParseServiceSet([]string{"svc", "-p", "80:80"}); err != nil {
		t.Fatalf("ParseServiceSet publish-only: %v", err)
	}
	if _, _, err := ParseServiceSet([]string{"svc", "--publish-reset"}); err != nil {
		t.Fatalf("ParseServiceSet publish-reset-only: %v", err)
	}
}

func TestParseServiceSetSnapshotOnlyDoesNotRequireServiceRoot(t *testing.T) {
	if _, _, err := ParseServiceSet([]string{"svc", "--snapshots=inherit"}); err != nil {
		t.Fatalf("ParseServiceSet snapshots-only: %v", err)
	}
}

func TestParseServiceSetRejectsEmptySnapshotMode(t *testing.T) {
	if _, _, err := ParseServiceSet([]string{"svc", "--snapshots="}); err == nil || !strings.Contains(err.Error(), "--snapshots must be on, off, or inherit") {
		t.Fatalf("ParseServiceSet error = %v, want snapshots value error", err)
	}
}

func TestParseServiceSetRejectsMissingSnapshotMode(t *testing.T) {
	tests := [][]string{
		{"svc", "--service-root=/srv/app", "--snapshots"},
		{"svc", "--service-root=/srv/app", "--snapshots", "--copy"},
		{"svc", "--service-root=/srv/app", "--snapshots=off", "--snapshots"},
		{"svc", "--service-root=/srv/app", "--snapshots=off", "--snapshots", "--copy"},
	}
	for _, args := range tests {
		if _, _, err := ParseServiceSet(args); err == nil || !strings.Contains(err.Error(), "--snapshots must be on, off, or inherit") {
			t.Fatalf("ParseServiceSet(%#v) error = %v, want snapshots value error", args, err)
		}
	}
}

func TestParseServiceSetRejectsCopyEmptyWithoutRootChange(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "copy", args: []string{"svc", "--snapshots=off", "--copy"}, wantErr: "--copy requires --service-root"},
		{name: "empty", args: []string{"svc", "--snapshots=off", "--empty"}, wantErr: "--empty requires --service-root"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := ParseServiceSet(tt.args); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ParseServiceSet error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseServiceSyncFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    ServiceSyncFlags
		wantOut []string
		wantErr string
	}{
		{
			name:    "single service",
			args:    []string{"sonarr"},
			want:    ServiceSyncFlags{},
			wantOut: []string{"sonarr"},
		},
		{
			name:    "all",
			args:    []string{"--all"},
			want:    ServiceSyncFlags{All: true},
			wantOut: nil,
		},
		{
			name:    "config before service",
			args:    []string{"--config", "./yeet.toml", "sonarr"},
			want:    ServiceSyncFlags{Config: "./yeet.toml"},
			wantOut: []string{"sonarr"},
		},
		{
			name:    "config equals",
			args:    []string{"sonarr", "--config=./yeet.toml"},
			want:    ServiceSyncFlags{Config: "./yeet.toml"},
			wantOut: []string{"sonarr"},
		},
		{name: "all plus service", args: []string{"--all", "sonarr"}, wantErr: "--all cannot be combined with a service name"},
		{name: "missing service and all", args: nil, wantErr: "service sync requires a service name or --all"},
		{name: "too many services", args: []string{"sonarr", "radarr"}, wantErr: "service sync accepts one service name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, out, err := ParseServiceSync(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseServiceSync error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseServiceSync error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("flags = %#v, want %#v", got, tt.want)
			}
			if !reflect.DeepEqual(out, tt.wantOut) {
				t.Fatalf("args = %#v, want %#v", out, tt.wantOut)
			}
		})
	}
}

func TestParseInfoFlags(t *testing.T) {
	flags, outArgs, err := ParseInfo([]string{"--format=json"})
	if err != nil {
		t.Fatalf("ParseInfo failed: %v", err)
	}
	if flags.Format != "json" {
		t.Fatalf("Format = %q, want %q", flags.Format, "json")
	}
	if len(outArgs) != 0 {
		t.Fatalf("expected no args, got %v", outArgs)
	}

	flags, outArgs, err = ParseInfo(nil)
	if err != nil {
		t.Fatalf("ParseInfo (default) failed: %v", err)
	}
	if flags.Format != "plain" {
		t.Fatalf("Format = %q, want %q", flags.Format, "plain")
	}
	if len(outArgs) != 0 {
		t.Fatalf("expected no args, got %v", outArgs)
	}
}

func TestSplitArgsForParsing(t *testing.T) {
	specs := map[string]FlagSpec{
		"--name": {ConsumesValue: true},
		"--all":  {},
		"-n":     {ConsumesValue: true},
		"-a":     {},
	}
	tests := []struct {
		name      string
		args      []string
		wantParse []string
		wantExtra []string
	}{
		{
			name:      "delimiter",
			args:      []string{"--name", "api", "--", "--remote"},
			wantParse: []string{"--name", "api"},
			wantExtra: []string{"--remote"},
		},
		{
			name:      "long value",
			args:      []string{"--name", "api", "payload"},
			wantParse: []string{"--name", "api", "payload"},
		},
		{
			name:      "long inline value",
			args:      []string{"--name=api", "payload"},
			wantParse: []string{"--name=api", "payload"},
		},
		{
			name:      "unknown long starts extra",
			args:      []string{"--all", "--remote", "cmd"},
			wantParse: []string{"--all"},
			wantExtra: []string{"--remote", "cmd"},
		},
		{
			name:      "short value",
			args:      []string{"-n", "api", "payload"},
			wantParse: []string{"-n", "api", "payload"},
		},
		{
			name:      "short inline value",
			args:      []string{"-n=api", "payload"},
			wantParse: []string{"-n=api", "payload"},
		},
		{
			name:      "short inline unknown starts extra",
			args:      []string{"-x=value", "payload"},
			wantParse: []string{},
			wantExtra: []string{"-x=value", "payload"},
		},
		{
			name:      "short cluster validates first flag",
			args:      []string{"-abc", "payload"},
			wantParse: []string{"-abc", "payload"},
		},
		{
			name:      "dash is positional",
			args:      []string{"-", "payload"},
			wantParse: []string{"-", "payload"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotParse, gotExtra := splitArgsForParsing(tt.args, specs)
			if !reflect.DeepEqual(gotParse, tt.wantParse) {
				t.Fatalf("parse args = %#v, want %#v", gotParse, tt.wantParse)
			}
			if !reflect.DeepEqual(gotExtra, tt.wantExtra) {
				t.Fatalf("extra args = %#v, want %#v", gotExtra, tt.wantExtra)
			}
		})
	}
}

func TestRemoteRegistryContainsExpectedCommands(t *testing.T) {
	reg := RemoteCommandRegistry()
	clone, ok := reg.Groups["snapshots"].Commands["clone"]
	if !ok {
		t.Fatal("snapshots clone command missing")
	}
	if strings.Contains(clone.Info.Usage, "[--start]") {
		t.Fatalf("snapshots clone usage %q should not advertise unsupported --start", clone.Info.Usage)
	}
	for _, example := range clone.Info.Examples {
		if strings.Contains(example, "--start") {
			t.Fatalf("snapshots clone example %q should not advertise --start while VM clone start is unsupported", example)
		}
	}
	cloneInfo := RemoteGroupInfos()["snapshots"].Commands["clone"]
	startHelp := flagHelpTag(t, cloneInfo.FlagsSchema, "start")
	lowerStartHelp := strings.ToLower(startHelp)
	if !strings.Contains(lowerStartHelp, "reserved") || !strings.Contains(lowerStartHelp, "unsupported") {
		t.Fatalf("snapshots clone --start help = %q, want explicit reserved/unsupported wording", startHelp)
	}
	if strings.Contains(lowerStartHelp, "start the cloned service") {
		t.Fatalf("snapshots clone --start help = %q, should not claim it starts clones", startHelp)
	}
}

func flagHelpTag(t *testing.T, schema any, flag string) string {
	t.Helper()
	typ := reflect.TypeOf(schema)
	if typ == nil {
		t.Fatalf("nil flags schema, want flag %q", flag)
	}
	typ = indirectType(typ)
	if typ.Kind() != reflect.Struct {
		t.Fatalf("flags schema type = %s, want struct", typ)
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		if field.Tag.Get("flag") == flag {
			return field.Tag.Get("help")
		}
	}
	t.Fatalf("flag %q not found in %s", flag, typ)
	return ""
}

func TestRemoteCommandRegistryAndFlagSpecs(t *testing.T) {
	names := RemoteCommandNames()
	if !containsString(names, "run") || !containsString(names, "status") {
		t.Fatalf("RemoteCommandNames = %v, want run and status", names)
	}

	infos := RemoteCommandInfos()
	if infos["run"].Name != "run" || infos["run"].ArgsSchema == nil {
		t.Fatalf("run command info = %#v, want name and args schema", infos["run"])
	}
	if infos["copy"].Aliases[0] != "cp" {
		t.Fatalf("copy aliases = %v, want cp", infos["copy"].Aliases)
	}

	reg := RemoteCommandRegistry()
	if reg.Command.Name != "yeet" {
		t.Fatalf("registry command = %q, want yeet", reg.Command.Name)
	}
	if reg.SubCommands["run"].Info.Name != "run" {
		t.Fatalf("registry run command = %#v", reg.SubCommands["run"])
	}
	if got := reg.SubCommands["run"].Info.Usage; got != "SVC [PAYLOAD] [-p HOST:CONTAINER] [--publish-reset] [--service-root=/abs/path|dataset] [--zfs] [--snapshots=on|off|inherit] [-- <payload args>] | --web [SVC] [PAYLOAD]" {
		t.Fatalf("run usage = %q", got)
	}
	if !containsString(reg.SubCommands["run"].Info.Examples, "yeet run <svc> ./compose.yml --service-root=tank/apps/<svc> --zfs") {
		t.Fatalf("run examples = %#v, want zfs service-root example", reg.SubCommands["run"].Info.Examples)
	}
	if !containsString(reg.SubCommands["run"].Info.Examples, "yeet run <svc> vm://nixos/26.05") {
		t.Fatalf("run examples = %#v, want NixOS VM example", reg.SubCommands["run"].Info.Examples)
	}
	if reg.SubCommands["remove"].Info.Aliases[0] != "rm" {
		t.Fatalf("registry remove aliases = %v, want rm", reg.SubCommands["remove"].Info.Aliases)
	}
	if reg.Groups["docker"].Commands["push"].Info.Name != "push" {
		t.Fatalf("registry docker push command = %#v", reg.Groups["docker"].Commands["push"])
	}
	if reg.Groups["docker"].Commands["outdated"].Info.Name != "outdated" {
		t.Fatalf("registry docker outdated command = %#v", reg.Groups["docker"].Commands["outdated"])
	}
	if reg.Groups["service"].Commands["set"].Info.Name != "set" {
		t.Fatalf("registry service set command = %#v", reg.Groups["service"].Commands["set"])
	}
	if reg.Groups["service"].Commands["set"].Info.Usage != "service set <svc> [-p HOST:CONTAINER] [--publish-reset] [--service-root=/abs/path|dataset] [--zfs] [--copy|--empty] [--snapshots=on|off|inherit] [--snapshot-keep-last=N] [--snapshot-max-age=7d] [--snapshot-events=run,docker-update] [--snapshot-required=true|false]" {
		t.Fatalf("service set usage = %q", reg.Groups["service"].Commands["set"].Info.Usage)
	}
	wantServiceSetExamples := []string{
		"yeet service set <svc> -p 80:80 -p 443:443",
		"yeet service set <svc> --publish-reset -p 443:443",
		"yeet service set <svc> --publish-reset",
		"yeet service set <svc> --service-root=/srv/apps/<svc>",
		"yeet service set <svc> --service-root=tank/apps/<svc> --zfs --copy",
		"yeet service set <svc> --service-root=/srv/apps/<svc> --empty",
		"yeet service set <svc> --snapshots=off",
		"yeet service set <svc> --snapshots=on --snapshot-keep-last=5 --snapshot-max-age=7d",
	}
	if !reflect.DeepEqual(reg.Groups["service"].Commands["set"].Info.Examples, wantServiceSetExamples) {
		t.Fatalf("service set examples = %#v, want %#v", reg.Groups["service"].Commands["set"].Info.Examples, wantServiceSetExamples)
	}
	if reg.Groups["vm"].Commands["set"].Info.Usage != "vm set <vm> [--cpus=N] [--memory=SIZE] [--disk=SIZE] [--net=svc|lan|svc,lan] [--macvlan-parent=IFACE] [--macvlan-vlan=ID] [--macvlan-mac=MAC]" {
		t.Fatalf("vm set usage = %q", reg.Groups["vm"].Commands["set"].Info.Usage)
	}
	if _, ok := reg.Groups["vm"].Commands["snapshot"]; ok {
		t.Fatal("vm snapshot command should not be registered; use snapshots create")
	}
	if reg.Groups["snapshots"].Commands["defaults"].Info.Name != "defaults" {
		t.Fatalf("registry snapshots defaults command = %#v", reg.Groups["snapshots"].Commands["defaults"])
	}
	for _, cmd := range []string{"list", "inspect", "create", "clone", "restore", "rm", "protect", "unprotect", "defaults"} {
		if _, ok := reg.Groups["snapshots"].Commands[cmd]; !ok {
			t.Fatalf("snapshots %s command missing", cmd)
		}
		if _, ok := RemoteGroupFlagSpecs()["snapshots"][cmd]; !ok {
			t.Fatalf("snapshots %s flag spec missing", cmd)
		}
	}
	outdatedArg, ok := yargs.ArgSpecAt(reg.Groups["docker"].Commands["outdated"].ArgsSchema, 0)
	if !ok {
		t.Fatal("docker outdated should expose optional service arg metadata")
	}
	if !IsServiceArgSpec(outdatedArg) || outdatedArg.Required {
		t.Fatalf("docker outdated arg = %#v, want optional ServiceName", outdatedArg)
	}
	updateArg, ok := yargs.ArgSpecAt(reg.Groups["docker"].Commands["update"].ArgsSchema, 0)
	if !ok {
		t.Fatal("docker update should expose variadic service arg metadata")
	}
	if !IsServiceArgSpec(updateArg) || !updateArg.Variadic || updateArg.MinCount != 1 {
		t.Fatalf("docker update arg = %#v, want variadic []ServiceName with minimum 1", updateArg)
	}

	flags := RemoteFlagSpecs()
	if !flags["run"]["--net"].ConsumesValue {
		t.Fatal("run --net should consume a value")
	}
	if flags["run"]["--restart"].ConsumesValue {
		t.Fatal("run --restart should not consume a value")
	}
	if !flags["run"]["-p"].ConsumesValue {
		t.Fatal("run -p should consume a value")
	}
	if flags["run"]["--publish-reset"].ConsumesValue {
		t.Fatal("run --publish-reset should not consume a value")
	}
	if _, ok := flags["run"]["--zfs"]; !ok {
		t.Fatal("run --zfs should be registered")
	}
	if !flags["run"]["--image-policy"].ConsumesValue {
		t.Fatal("run --image-policy should consume a value")
	}
	spec, ok := flags["run"]["--web"]
	if !ok {
		t.Fatal("run --web should be registered")
	}
	if spec.ConsumesValue {
		t.Fatal("run --web should not consume a value")
	}

	groups := RemoteGroupInfos()
	if groups["env"].Commands["copy"].Aliases[0] != "cp" {
		t.Fatalf("env copy aliases = %v, want cp", groups["env"].Commands["copy"].Aliases)
	}
	groupFlags := RemoteGroupFlagSpecs()
	if groupFlags["env"]["show"]["--staged"].ConsumesValue {
		t.Fatal("env show --staged should not consume a value")
	}
	if groupFlags["docker"]["push"]["--run"].ConsumesValue {
		t.Fatal("docker push --run should not consume a value")
	}
	if groupFlags["docker"]["push"]["--all-local"].ConsumesValue {
		t.Fatal("docker push --all-local should not consume a value")
	}
	if groupFlags["docker"]["update"]["--outdated"].ConsumesValue {
		t.Fatal("docker update --outdated should not consume a value")
	}
	if !RemoteGroupFlagSpecs()["docker"]["outdated"]["--format"].ConsumesValue {
		t.Fatal("docker outdated --format should consume a value")
	}
	if !RemoteGroupFlagSpecs()["service"]["set"]["--service-root"].ConsumesValue {
		t.Fatal("service set --service-root should consume a value")
	}
	if RemoteGroupFlagSpecs()["service"]["set"]["--copy"].ConsumesValue {
		t.Fatal("service set --copy should not consume a value")
	}
	if RemoteGroupFlagSpecs()["service"]["set"]["--empty"].ConsumesValue {
		t.Fatal("service set --empty should not consume a value")
	}
	if _, ok := RemoteGroupFlagSpecs()["service"]["set"]["--zfs"]; !ok {
		t.Fatal("service set --zfs should be registered")
	}
	if !RemoteGroupFlagSpecs()["service"]["set"]["--snapshots"].ConsumesValue {
		t.Fatal("service set --snapshots should consume a value")
	}
	if !RemoteGroupFlagSpecs()["snapshots"]["defaults"]["--enabled"].ConsumesValue {
		t.Fatal("snapshots defaults --enabled should consume a value")
	}
	if !RemoteGroupFlagSpecs()["snapshots"]["list"]["--format"].ConsumesValue {
		t.Fatal("snapshots list --format should consume a value")
	}
	if !RemoteGroupFlagSpecs()["snapshots"]["inspect"]["--format"].ConsumesValue {
		t.Fatal("snapshots inspect --format should consume a value")
	}
	if !RemoteGroupFlagSpecs()["snapshots"]["create"]["--comment"].ConsumesValue {
		t.Fatal("snapshots create --comment should consume a value")
	}
	if RemoteGroupFlagSpecs()["snapshots"]["create"]["--full"].ConsumesValue {
		t.Fatal("snapshots create --full should not consume a value")
	}
	if RemoteGroupFlagSpecs()["snapshots"]["rm"]["--yes"].ConsumesValue {
		t.Fatal("snapshots rm --yes should not consume a value")
	}
	if RemoteGroupFlagSpecs()["snapshots"]["clone"]["--start"].ConsumesValue {
		t.Fatal("snapshots clone --start should not consume a value")
	}
	if RemoteGroupFlagSpecs()["snapshots"]["restore"]["--stop"].ConsumesValue {
		t.Fatal("snapshots restore --stop should not consume a value")
	}
	if RemoteGroupFlagSpecs()["snapshots"]["restore"]["--start"].ConsumesValue {
		t.Fatal("snapshots restore --start should not consume a value")
	}
	if RemoteGroupFlagSpecs()["snapshots"]["restore"]["--yes"].ConsumesValue {
		t.Fatal("snapshots restore --yes should not consume a value")
	}
	if !RemoteGroupFlagSpecs()["snapshots"]["restore"]["--mode"].ConsumesValue {
		t.Fatal("snapshots restore --mode should consume a value")
	}
	if !RemoteGroupFlagSpecs()["snapshots"]["restore"]["--generation"].ConsumesValue {
		t.Fatal("snapshots restore --generation should consume a value")
	}
	if _, ok := RemoteGroupFlagSpecs()["vm"]["snapshot"]; ok {
		t.Fatal("vm snapshot flags should not be registered; use snapshots create")
	}
}

func TestRegistryMovesRollbackUnderService(t *testing.T) {
	if containsString(RemoteCommandNames(), "rollback") {
		t.Fatalf("RemoteCommandNames includes rollback, want rollback under service group")
	}
	if _, ok := RemoteCommandInfos()["rollback"]; ok {
		t.Fatal("RemoteCommandInfos includes top-level rollback, want rollback under service group")
	}
	if _, ok := RemoteFlagSpecs()["rollback"]; ok {
		t.Fatal("RemoteFlagSpecs includes top-level rollback, want rollback under service group")
	}

	reg := RemoteCommandRegistry()
	if _, ok := reg.SubCommands["rollback"]; ok {
		t.Fatal("RemoteCommandRegistry includes top-level rollback, want rollback under service group")
	}
	if got := reg.Groups["service"].Commands["rollback"].Info.Usage; got != "service rollback <svc>" {
		t.Fatalf("service rollback usage = %q, want service rollback <svc>", got)
	}
	if got := reg.Groups["service"].Commands["generations"].Info.Usage; got != "service generations <svc> [--format=table|json|json-pretty]" {
		t.Fatalf("service generations usage = %q, want service generations usage", got)
	}

	groupFlags := RemoteGroupFlagSpecs()["service"]
	if _, ok := groupFlags["rollback"]; !ok {
		t.Fatal("service rollback flag specs missing")
	}
	genFlags, ok := groupFlags["generations"]
	if !ok {
		t.Fatal("service generations flag specs missing")
	}
	if !genFlags["--format"].ConsumesValue {
		t.Fatal("service generations --format should consume a value")
	}
}

func TestParseServiceGenerationCommands(t *testing.T) {
	rollback, err := ParseServiceRollback([]string{"plex"})
	if err != nil {
		t.Fatalf("ParseServiceRollback: %v", err)
	}
	if !reflect.DeepEqual(rollback, []string{"plex"}) {
		t.Fatalf("rollback args = %#v, want plex", rollback)
	}

	flags, args, err := ParseServiceGenerations([]string{"plex", "--format=json"})
	if err != nil {
		t.Fatalf("ParseServiceGenerations: %v", err)
	}
	if flags.Format != "json" {
		t.Fatalf("service generations format = %q, want json", flags.Format)
	}
	if !reflect.DeepEqual(args, []string{"plex"}) {
		t.Fatalf("service generations args = %#v, want plex", args)
	}

	flags, args, err = ParseServiceGenerations([]string{"plex", "--format=json-pretty"})
	if err != nil {
		t.Fatalf("ParseServiceGenerations json-pretty: %v", err)
	}
	if flags.Format != "json-pretty" || !reflect.DeepEqual(args, []string{"plex"}) {
		t.Fatalf("service generations json-pretty = %#v args=%#v, want json-pretty plex", flags, args)
	}

	if _, err := ParseServiceRollback(nil); err == nil || !strings.Contains(err.Error(), "service rollback requires a service") {
		t.Fatalf("ParseServiceRollback missing service error = %v, want service required error", err)
	}
	if _, err := ParseServiceRollback([]string{"plex", "jellyfin"}); err == nil || !strings.Contains(err.Error(), "service rollback requires exactly one service") {
		t.Fatalf("ParseServiceRollback extra args error = %v, want arity error", err)
	}
	if _, _, err := ParseServiceGenerations([]string{"plex", "--format=yaml"}); err == nil || !strings.Contains(err.Error(), "--format must be table, json, or json-pretty") {
		t.Fatalf("ParseServiceGenerations format error = %v, want format error", err)
	}
}

func TestRemoteRegistryIncludesVMConsole(t *testing.T) {
	group, ok := RemoteGroupInfos()["vm"]
	if !ok {
		t.Fatal("vm group missing")
	}
	if _, ok := group.Commands["console"]; !ok {
		t.Fatal("vm console command missing")
	}
	if _, ok := RemoteGroupFlagSpecs()["vm"]["console"]; !ok {
		t.Fatal("vm console flag spec missing")
	}
	if _, ok := group.Commands["set"]; !ok {
		t.Fatal("vm set command missing")
	}
	if _, ok := RemoteGroupFlagSpecs()["vm"]["set"]; !ok {
		t.Fatal("vm set flag spec missing")
	}
	for _, flag := range []string{"--cpus", "--memory", "--disk", "--net", "--macvlan-parent", "--macvlan-vlan", "--macvlan-mac"} {
		if !RemoteGroupFlagSpecs()["vm"]["set"][flag].ConsumesValue {
			t.Fatalf("vm set %s should consume a value", flag)
		}
		if _, ok := RemoteGroupFlagSpecs()["service"]["set"][flag]; ok {
			t.Fatalf("service set %s should not be registered", flag)
		}
	}
	if _, ok := group.Commands["images"]; !ok {
		t.Fatal("vm images command missing")
	}
	if _, ok := RemoteGroupFlagSpecs()["vm"]["images"]; !ok {
		t.Fatal("vm images flag spec missing")
	}
	if !RemoteGroupFlagSpecs()["vm"]["images"]["--format"].ConsumesValue {
		t.Fatal("vm images --format should consume a value")
	}
	if !RemoteGroupFlagSpecs()["vm"]["images"]["--output"].ConsumesValue {
		t.Fatal("vm images --output alias should consume a value")
	}
	if RemoteGroupFlagSpecs()["vm"]["images"]["--dry-run"].ConsumesValue {
		t.Fatal("vm images --dry-run should not consume a value")
	}
	if !containsString(group.Commands["images"].Examples, "yeet vm images catalog") {
		t.Fatalf("vm images examples = %#v, want catalog example", group.Commands["images"].Examples)
	}
	if !containsString(group.Commands["images"].Examples, "yeet vm images update vm://nixos/26.05") {
		t.Fatalf("vm images examples = %#v, want selected NixOS update example", group.Commands["images"].Examples)
	}
	if _, ok := group.Commands["snapshot"]; ok {
		t.Fatal("vm snapshot command should not be present")
	}
	if _, ok := RemoteGroupFlagSpecs()["vm"]["snapshot"]; ok {
		t.Fatal("vm snapshot flag spec should not be present")
	}
}

func TestServiceArgSpecDetection(t *testing.T) {
	if !IsServiceArgSpec(yargs.ArgSpec{GoType: reflect.TypeOf(ServiceName(""))}) {
		t.Fatal("ServiceName arg spec was not detected")
	}
	if !IsServiceArgSpec(yargs.ArgSpec{GoType: reflect.TypeOf([]ServiceName{})}) {
		t.Fatal("[]ServiceName arg spec was not detected")
	}
	if IsServiceArgSpec(yargs.ArgSpec{GoType: reflect.TypeOf("")}) {
		t.Fatal("plain string arg spec detected as ServiceName")
	}
}

func TestParseAdditionalCommandFlags(t *testing.T) {
	t.Run("edit", func(t *testing.T) {
		flags, args, err := ParseEdit([]string{"--config", "--ts", "svc", "--", "--payload-flag"})
		if err != nil {
			t.Fatalf("ParseEdit: %v", err)
		}
		if !flags.Config || !flags.TS || !flags.Restart {
			t.Fatalf("ParseEdit flags = %#v, want config/ts/restart", flags)
		}
		if got := strings.Join(args, " "); got != "svc --payload-flag" {
			t.Fatalf("ParseEdit args = %q, want svc --payload-flag", got)
		}
	})

	t.Run("logs", func(t *testing.T) {
		flags, args, err := ParseLogs([]string{"-f", "--lines", "10", "svc"})
		if err != nil {
			t.Fatalf("ParseLogs: %v", err)
		}
		if !flags.Follow || flags.Lines != 10 {
			t.Fatalf("ParseLogs flags = %#v, want follow true lines 10", flags)
		}
		if got := strings.Join(args, " "); got != "svc" {
			t.Fatalf("ParseLogs args = %q, want svc", got)
		}

		flags, args, err = ParseLogs(nil)
		if err != nil {
			t.Fatalf("ParseLogs default: %v", err)
		}
		if flags.Follow || flags.Lines != -1 || len(args) != 0 {
			t.Fatalf("ParseLogs default = %#v args=%v, want follow false lines -1 no args", flags, args)
		}
	})

	t.Run("status", func(t *testing.T) {
		flags, args, err := ParseStatus([]string{"svc", "--", "--tail"})
		if err != nil {
			t.Fatalf("ParseStatus: %v", err)
		}
		if flags.Format != "table" {
			t.Fatalf("status format = %q, want table", flags.Format)
		}
		if got := strings.Join(args, " "); got != "svc --tail" {
			t.Fatalf("ParseStatus args = %q, want svc --tail", got)
		}

		flags, args, err = ParseStatus([]string{"--format=json"})
		if err != nil {
			t.Fatalf("ParseStatus format: %v", err)
		}
		if flags.Format != "json" || len(args) != 0 {
			t.Fatalf("ParseStatus format = %#v args=%v, want json no args", flags, args)
		}
	})

	t.Run("docker outdated", func(t *testing.T) {
		flags, args, err := ParseDockerOutdated([]string{"--format=json", "svc"})
		if err != nil {
			t.Fatalf("ParseDockerOutdated: %v", err)
		}
		if flags.Format != "json" {
			t.Fatalf("docker outdated format = %q, want json", flags.Format)
		}
		if got := strings.Join(args, " "); got != "svc" {
			t.Fatalf("ParseDockerOutdated args = %q, want svc", got)
		}

		flags, args, err = ParseDockerOutdated(nil)
		if err != nil {
			t.Fatalf("ParseDockerOutdated default: %v", err)
		}
		if flags.Format != "table" || len(args) != 0 {
			t.Fatalf("ParseDockerOutdated default = %#v args=%v, want table no args", flags, args)
		}

		flags, args, err = ParseDockerOutdated([]string{"--format=json", "svc", "--", "--raw"})
		if err != nil {
			t.Fatalf("ParseDockerOutdated double dash: %v", err)
		}
		if flags.Format != "json" {
			t.Fatalf("docker outdated double dash format = %q, want json", flags.Format)
		}
		if got := strings.Join(args, " "); got != "svc --raw" {
			t.Fatalf("ParseDockerOutdated double dash args = %q, want svc --raw", got)
		}
	})

	t.Run("docker update", func(t *testing.T) {
		flags, args, err := ParseDockerUpdate([]string{"--outdated"})
		if err != nil {
			t.Fatalf("ParseDockerUpdate: %v", err)
		}
		if !flags.Outdated {
			t.Fatal("docker update --outdated was not parsed")
		}
		if len(args) != 0 {
			t.Fatalf("ParseDockerUpdate args = %q, want none", strings.Join(args, " "))
		}

		flags, args, err = ParseDockerUpdate([]string{"svc-a"})
		if err != nil {
			t.Fatalf("ParseDockerUpdate service: %v", err)
		}
		if flags.Outdated {
			t.Fatal("docker update svc should not set Outdated")
		}
		if got := strings.Join(args, " "); got != "svc-a" {
			t.Fatalf("ParseDockerUpdate service args = %q, want svc-a", got)
		}
	})

	t.Run("vm images", func(t *testing.T) {
		flags, args, err := ParseVMImages(nil)
		if err != nil {
			t.Fatalf("ParseVMImages default: %v", err)
		}
		if flags.Format != "table" || len(args) != 0 {
			t.Fatalf("ParseVMImages default = %#v args=%v, want table no args", flags, args)
		}

		flags, args, err = ParseVMImages([]string{"--format=json"})
		if err != nil {
			t.Fatalf("ParseVMImages format: %v", err)
		}
		if flags.Format != "json" || len(args) != 0 {
			t.Fatalf("ParseVMImages format = %#v args=%v, want json no args", flags, args)
		}

		flags, args, err = ParseVMImages([]string{"catalog", "--format=json"})
		if err != nil {
			t.Fatalf("ParseVMImages catalog format: %v", err)
		}
		if flags.Format != "json" || strings.Join(args, " ") != "catalog" {
			t.Fatalf("ParseVMImages catalog format = %#v args=%v, want json catalog", flags, args)
		}

		flags, args, err = ParseVMImages([]string{"--output=json-pretty"})
		if err != nil {
			t.Fatalf("ParseVMImages output alias: %v", err)
		}
		if flags.Format != "json-pretty" || len(args) != 0 {
			t.Fatalf("ParseVMImages output alias = %#v args=%v, want json-pretty no args", flags, args)
		}

		flags, args, err = ParseVMImages([]string{"update", "--output", "json-pretty"})
		if err != nil {
			t.Fatalf("ParseVMImages update: %v", err)
		}
		if flags.Format != "json-pretty" {
			t.Fatalf("VMImages format = %q, want json-pretty", flags.Format)
		}
		if !reflect.DeepEqual(args, []string{"update"}) {
			t.Fatalf("ParseVMImages args = %#v, want update", args)
		}
	})

	t.Run("events", func(t *testing.T) {
		flags, args, err := ParseEvents([]string{"--all", "svc"})
		if err != nil {
			t.Fatalf("ParseEvents: %v", err)
		}
		if !flags.All {
			t.Fatal("events --all was not parsed")
		}
		if got := strings.Join(args, " "); got != "svc" {
			t.Fatalf("ParseEvents args = %q, want svc", got)
		}
	})

	t.Run("mount", func(t *testing.T) {
		flags, args, err := ParseMount([]string{
			"--type", "cifs",
			"--opts", "rw",
			"--deps", "network-online.target",
			"--deps", "remote-fs.target",
			"server:/export",
			"data",
		})
		if err != nil {
			t.Fatalf("ParseMount: %v", err)
		}
		if flags.Type != "cifs" || flags.Opts != "rw" {
			t.Fatalf("mount scalar flags = %#v, want cifs/rw", flags)
		}
		wantDeps := []string{"network-online.target", "remote-fs.target"}
		if !reflect.DeepEqual(flags.Deps, wantDeps) {
			t.Fatalf("mount deps = %v, want %v", flags.Deps, wantDeps)
		}
		if got := strings.Join(args, " "); got != "server:/export data" {
			t.Fatalf("ParseMount args = %q, want server:/export data", got)
		}

		flags, args, err = ParseMount(nil)
		if err != nil {
			t.Fatalf("ParseMount default: %v", err)
		}
		if flags.Type != "nfs" || flags.Opts != "defaults" || len(args) != 0 {
			t.Fatalf("ParseMount default = %#v args=%v, want nfs/defaults no args", flags, args)
		}
	})

	t.Run("version", func(t *testing.T) {
		flags, args, err := ParseVersion([]string{"--json", "--", "extra"})
		if err != nil {
			t.Fatalf("ParseVersion: %v", err)
		}
		if !flags.JSON {
			t.Fatal("version --json was not parsed")
		}
		if got := strings.Join(args, " "); got != "extra" {
			t.Fatalf("ParseVersion args = %q, want extra", got)
		}
	})
}

func TestParseVMImagesImport(t *testing.T) {
	flags, args, err := ParseVMImages([]string{"import", "foo/bar", "./bundle", "--allow-local-kernel", "--format=json"})
	if err != nil {
		t.Fatalf("ParseVMImages import: %v", err)
	}
	if flags.Format != "json" {
		t.Fatalf("format = %q, want json", flags.Format)
	}
	if !flags.AllowLocalKernel {
		t.Fatal("AllowLocalKernel = false, want true")
	}
	if flags.Stdin {
		t.Fatal("Stdin = true for public import args, want false")
	}
	want := []string{"import", "foo/bar", "./bundle"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestParseVMImagesImportStdin(t *testing.T) {
	flags, args, err := ParseVMImages([]string{"import", "foo/bar", "--stdin", "--format=json-pretty"})
	if err != nil {
		t.Fatalf("ParseVMImages import stdin: %v", err)
	}
	if !flags.Stdin {
		t.Fatal("Stdin = false, want true")
	}
	if flags.Format != "json-pretty" {
		t.Fatalf("format = %q, want json-pretty", flags.Format)
	}
	want := []string{"import", "foo/bar"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestParseVMImagesRemove(t *testing.T) {
	flags, args, err := ParseVMImages([]string{"rm", "foo/bar", "--yes", "--format=table"})
	if err != nil {
		t.Fatalf("ParseVMImages rm: %v", err)
	}
	if !flags.Yes {
		t.Fatal("Yes = false, want true")
	}
	if flags.Format != "table" {
		t.Fatalf("format = %q, want table", flags.Format)
	}
	want := []string{"rm", "foo/bar"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}

	flags, args, err = ParseVMImages([]string{"rm", "foo/bar", "-y"})
	if err != nil {
		t.Fatalf("ParseVMImages rm short yes: %v", err)
	}
	if !flags.Yes {
		t.Fatal("Yes = false for -y, want true")
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestParseVMImagesPrune(t *testing.T) {
	flags, args, err := ParseVMImages([]string{"prune", "--dry-run", "--yes", "--format=json"})
	if err != nil {
		t.Fatalf("ParseVMImages prune: %v", err)
	}
	if !flags.DryRun {
		t.Fatal("DryRun = false, want true")
	}
	if !flags.Yes {
		t.Fatal("Yes = false, want true")
	}
	if flags.Format != "json" {
		t.Fatalf("format = %q, want json", flags.Format)
	}
	want := []string{"prune"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestParseVMImagesListAlias(t *testing.T) {
	flags, args, err := ParseVMImages([]string{"ls", "--output=json"})
	if err != nil {
		t.Fatalf("ParseVMImages ls: %v", err)
	}
	if flags.Format != "json" {
		t.Fatalf("format = %q, want json", flags.Format)
	}
	want := []string{"ls"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestParseFlagErrors(t *testing.T) {
	if _, _, err := ParseRun([]string{"--macvlan-vlan", "not-an-int"}); err == nil {
		t.Fatal("ParseRun succeeded with invalid int")
	}
	if _, _, err := ParseVMImages([]string{"--format=xml"}); err == nil || !strings.Contains(err.Error(), "--format must be table, json, or json-pretty") {
		t.Fatalf("ParseVMImages invalid output error = %v", err)
	}
	if _, _, err := ParseLogs([]string{"--lines", "not-an-int"}); err == nil {
		t.Fatal("ParseLogs succeeded with invalid int")
	}
	if _, _, _, err := ParseStage([]string{"--macvlan-vlan", "not-an-int"}); err == nil {
		t.Fatal("ParseStage succeeded with invalid int")
	}
}

func TestParseStageDefaultSubcommandAndExtraArgs(t *testing.T) {
	flags, subcmd, args, err := ParseStage([]string{"--net=ts", "svc", "payload", "--", "--payload-arg"})
	if err != nil {
		t.Fatalf("ParseStage: %v", err)
	}
	if flags.Net != "ts" {
		t.Fatalf("Net = %q, want ts", flags.Net)
	}
	if subcmd != "stage" {
		t.Fatalf("subcmd = %q, want stage", subcmd)
	}
	if got := strings.Join(args, " "); got != "svc payload --payload-arg" {
		t.Fatalf("args = %q, want svc payload --payload-arg", got)
	}
}

func TestParseStageRejectsEmptyNetFlag(t *testing.T) {
	for _, args := range [][]string{
		{"--net=", "svc", "payload"},
		{"--net", "", "svc", "payload"},
		{"--net", "--pull", "svc", "payload"},
	} {
		if _, _, _, err := ParseStage(args); err == nil || !strings.Contains(err.Error(), "--net must not be empty") {
			t.Fatalf("ParseStage(%#v) error = %v, want empty --net error", args, err)
		}
	}
}

func TestParseStageRejectsEmptyNetComponents(t *testing.T) {
	for _, args := range [][]string{
		{"--net=,", "svc", "payload"},
		{"--net=svc,", "svc", "payload"},
		{"--net=svc,,lan", "svc", "payload"},
	} {
		if _, _, _, err := ParseStage(args); err == nil || !strings.Contains(err.Error(), "--net must not contain empty network modes") {
			t.Fatalf("ParseStage(%#v) error = %v, want empty mode error", args, err)
		}
	}
}

func TestParseStageRejectsInvalidMacvlanVLANFlag(t *testing.T) {
	for _, tt := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "negative", args: []string{"--macvlan-vlan=-1", "svc", "payload"}, wantErr: "--macvlan-vlan must not be negative"},
		{name: "too large", args: []string{"--macvlan-vlan=4095", "svc", "payload"}, wantErr: "--macvlan-vlan must be between 1 and 4094"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, _, err := ParseStage(tt.args); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ParseStage(%#v) error = %v, want %q", tt.args, err, tt.wantErr)
			}
		})
	}
}

func TestParseStageRejectsMacvlanFlagsWithoutLAN(t *testing.T) {
	for _, args := range [][]string{
		{"--macvlan-parent=vmbr0", "svc", "payload"},
		{"--net=svc", "--macvlan-vlan=4", "svc", "payload"},
		{"--net=ts", "--macvlan-mac=02:00:00:00:00:42", "svc", "payload"},
	} {
		if _, _, _, err := ParseStage(args); err == nil || !strings.Contains(err.Error(), "--macvlan-* settings require LAN networking") {
			t.Fatalf("ParseStage(%#v) error = %v, want LAN requirement", args, err)
		}
	}
}

func TestSplitArgsAtDoubleDash(t *testing.T) {
	parse, extra := splitArgsAtDoubleDash([]string{"svc", "--"})
	if !reflect.DeepEqual(parse, []string{"svc"}) {
		t.Fatalf("parse args = %v, want [svc]", parse)
	}
	if extra != nil {
		t.Fatalf("extra args = %v, want nil", extra)
	}
}

func TestFlagSpecsFromStruct(t *testing.T) {
	type sample struct {
		Name  string `flag:"name" short:"n"`
		Force bool
		Count *int `flag:"count"`
	}
	specs := flagSpecsFromStruct((*sample)(nil))
	if !specs["--name"].ConsumesValue || !specs["-n"].ConsumesValue {
		t.Fatalf("name specs = %#v %#v, want value-consuming long and short", specs["--name"], specs["-n"])
	}
	if specs["--force"].ConsumesValue {
		t.Fatal("bool field should not consume a value")
	}
	if !specs["--count"].ConsumesValue {
		t.Fatal("pointer-to-int field should consume a value")
	}
	if got := flagSpecsFromStruct("not a struct"); len(got) != 0 {
		t.Fatalf("non-struct specs = %#v, want empty", got)
	}
}

func TestRequireArgsAtLeast(t *testing.T) {
	if err := RequireArgsAtLeast("run", []string{"svc", "payload"}, 2); err != nil {
		t.Fatalf("RequireArgsAtLeast satisfied: %v", err)
	}
	err := RequireArgsAtLeast("run", []string{"svc"}, 2)
	if err == nil {
		t.Fatal("RequireArgsAtLeast succeeded with too few args")
	}
	if got := err.Error(); got != "'run' requires at least 2 argument(s), got 1" {
		t.Fatalf("error = %q, want argument count message", got)
	}
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
