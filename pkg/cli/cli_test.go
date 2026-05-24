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
		"--net", "ts",
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
		"--force",
		"--pull",
		"arg1", "arg2",
	}

	flags, outArgs, err := ParseRun(args)
	if err != nil {
		t.Fatalf("ParseRun failed: %v", err)
	}
	if flags.Net != "ts" {
		t.Errorf("Net = %q, want %q", flags.Net, "ts")
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
	flags, outArgs, err := ParseRemove([]string{"-y", "--clean-config"})
	if err != nil {
		t.Fatalf("ParseRemove failed: %v", err)
	}
	if !flags.Yes {
		t.Fatalf("Yes = false, want true")
	}
	if !flags.CleanConfig {
		t.Fatalf("CleanConfig = false, want true")
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
		{name: "missing root", args: []string{"svc-a"}, wantErr: "service set requires --service-root or snapshot settings"},
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
			if got != tt.want {
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

func TestRemoteRegistryMetadata(t *testing.T) {
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
	if !containsString(reg.SubCommands["run"].Info.Examples, "yeet run <svc> ./compose.yml --service-root=tank/apps/<svc> --zfs") {
		t.Fatalf("run examples = %#v, want zfs service-root example", reg.SubCommands["run"].Info.Examples)
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
	if reg.Groups["service"].Commands["set"].Info.Usage != "service set <svc> [--service-root=/abs/path|dataset] [--zfs] [--copy|--empty] [--snapshots=on|off|inherit] [--snapshot-keep-last=N] [--snapshot-max-age=7d] [--snapshot-events=run,docker-update] [--snapshot-required=true|false]" {
		t.Fatalf("service set usage = %q", reg.Groups["service"].Commands["set"].Info.Usage)
	}
	wantServiceSetExamples := []string{
		"yeet service set <svc> --service-root=/srv/apps/<svc>",
		"yeet service set <svc> --service-root=tank/apps/<svc> --zfs --copy",
		"yeet service set <svc> --service-root=/srv/apps/<svc> --empty",
		"yeet service set <svc> --snapshots=off",
		"yeet service set <svc> --snapshots=on --snapshot-keep-last=5 --snapshot-max-age=7d",
	}
	if !reflect.DeepEqual(reg.Groups["service"].Commands["set"].Info.Examples, wantServiceSetExamples) {
		t.Fatalf("service set examples = %#v, want %#v", reg.Groups["service"].Commands["set"].Info.Examples, wantServiceSetExamples)
	}
	if reg.Groups["snapshots"].Commands["defaults"].Info.Name != "defaults" {
		t.Fatalf("registry snapshots defaults command = %#v", reg.Groups["snapshots"].Commands["defaults"])
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
	if _, ok := flags["run"]["--zfs"]; !ok {
		t.Fatal("run --zfs should be registered")
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

func TestParseFlagErrors(t *testing.T) {
	if _, _, err := ParseRun([]string{"--macvlan-vlan", "not-an-int"}); err == nil {
		t.Fatal("ParseRun succeeded with invalid int")
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
