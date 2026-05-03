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
	if reg.SubCommands["remove"].Info.Aliases[0] != "rm" {
		t.Fatalf("registry remove aliases = %v, want rm", reg.SubCommands["remove"].Info.Aliases)
	}
	if reg.Groups["docker"].Commands["push"].Info.Name != "push" {
		t.Fatalf("registry docker push command = %#v", reg.Groups["docker"].Commands["push"])
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
}

func TestServiceArgSpecDetection(t *testing.T) {
	if !IsServiceArgSpec(yargs.ArgSpec{GoType: reflect.TypeOf(ServiceName(""))}) {
		t.Fatal("ServiceName arg spec was not detected")
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
