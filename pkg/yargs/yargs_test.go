// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yargs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseWithCommand_FlagSeparation(t *testing.T) {
	type GlobalFlags struct {
		Verbose    bool   `flag:"v"`
		ControlURL string `flag:"control-url"`
	}

	type RunFlags struct {
		Remove      bool `flag:"rm"`
		Interactive bool `flag:"it"`
	}

	tests := []struct {
		name            string
		args            []string
		wantSubCommand  string
		wantArgs        []string
		wantGlobalFlags GlobalFlags
		wantSubCmdFlags RunFlags
	}{
		{
			name:            "sub-command flag separated from global flag",
			args:            []string{"run", "--rm", "bin"},
			wantSubCommand:  "run",
			wantArgs:        []string{"bin"},
			wantGlobalFlags: GlobalFlags{},
			wantSubCmdFlags: RunFlags{Remove: true},
		},
		{
			name:            "global flags and sub-command flags mixed",
			args:            []string{"-v", "run", "--rm", "bin", "--control-url=localhost:3000"},
			wantSubCommand:  "run",
			wantArgs:        []string{"bin"},
			wantGlobalFlags: GlobalFlags{Verbose: true, ControlURL: "localhost:3000"},
			wantSubCmdFlags: RunFlags{Remove: true},
		},
		{
			name:            "only global flags",
			args:            []string{"-v", "status"},
			wantSubCommand:  "status",
			wantArgs:        []string{},
			wantGlobalFlags: GlobalFlags{Verbose: true},
			wantSubCmdFlags: RunFlags{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseWithCommand[GlobalFlags, RunFlags, struct{}](tt.args)
			if err != nil {
				t.Fatalf("ParseWithCommand() error = %v", err)
			}

			if got.SubCommand != tt.wantSubCommand {
				t.Errorf("SubCommand = %q, want %q", got.SubCommand, tt.wantSubCommand)
			}
			if !reflect.DeepEqual(got.Parser.Args, tt.wantArgs) {
				t.Errorf("Args = %v, want %v", got.Parser.Args, tt.wantArgs)
			}
			if !reflect.DeepEqual(got.GlobalFlags, tt.wantGlobalFlags) {
				t.Errorf("GlobalFlags = %+v, want %+v", got.GlobalFlags, tt.wantGlobalFlags)
			}
			if !reflect.DeepEqual(got.SubCommandFlags, tt.wantSubCmdFlags) {
				t.Errorf("SubCommandFlags = %+v, want %+v", got.SubCommandFlags, tt.wantSubCmdFlags)
			}
		})
	}
}

func TestParseWithCommand_ConflictPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("ParseWithCommand did not panic when flag conflicts between global and subcommand types")
		}
	}()

	// Define conflicting flag types
	type GlobalFlags struct {
		Remove bool `flag:"rm"`
	}

	type RunFlags struct {
		Remove bool `flag:"rm"` // Conflict!
	}

	// This should panic
	_, _ = ParseWithCommand[GlobalFlags, RunFlags, struct{}]([]string{"run", "--rm"})
}

func TestParseFlags(t *testing.T) {
	type TestFlags struct {
		Verbose    bool          `flag:"v"`
		ControlURL string        `flag:"control-url"`
		Port       int           `flag:"port"`
		Timeout    time.Duration `flag:"timeout"`
		MaxRetries uint          `flag:"max-retries"`
		Threshold  float64       `flag:"threshold"`
	}

	result, err := ParseFlags[TestFlags]([]string{
		"-v",
		"--control-url=localhost:3000",
		"--port=8080",
		"--timeout=30s",
		"--max-retries=5",
		"--threshold=0.95",
		"arg1",
		"arg2",
	})
	if err != nil {
		t.Fatalf("ParseFlags failed: %v", err)
	}

	// ParseFlags doesn't have subcommands
	if result.SubCommand != "" {
		t.Errorf("SubCommand = %q, want empty", result.SubCommand)
	}
	wantArgs := []string{"arg1", "arg2"}
	if !reflect.DeepEqual(result.Args, wantArgs) {
		t.Errorf("Args = %v, want %v", result.Args, wantArgs)
	}
	if !result.Flags.Verbose {
		t.Error("Verbose = false, want true")
	}
	if result.Flags.ControlURL != "localhost:3000" {
		t.Errorf("ControlURL = %q, want %q", result.Flags.ControlURL, "localhost:3000")
	}
	if result.Flags.Port != 8080 {
		t.Errorf("Port = %d, want %d", result.Flags.Port, 8080)
	}
	if result.Flags.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want %v", result.Flags.Timeout, 30*time.Second)
	}
	if result.Flags.MaxRetries != 5 {
		t.Errorf("MaxRetries = %d, want %d", result.Flags.MaxRetries, 5)
	}
	if result.Flags.Threshold != 0.95 {
		t.Errorf("Threshold = %f, want %f", result.Flags.Threshold, 0.95)
	}
}

func TestParseFlags_BoolFlagBetweenArgs(t *testing.T) {
	type Flags struct {
		Verbose bool `flag:"v"`
	}

	result, err := ParseFlags[Flags]([]string{"bar", "-v", "123"})
	if err != nil {
		t.Fatalf("ParseFlags failed: %v", err)
	}

	if !result.Flags.Verbose {
		t.Error("Verbose = false, want true")
	}
	// ParseFlags doesn't have subcommands - all non-flag args are positional
	if result.SubCommand != "" {
		t.Errorf("SubCommand = %q, want empty", result.SubCommand)
	}
	wantArgs := []string{"bar", "123"}
	if !reflect.DeepEqual(result.Args, wantArgs) {
		t.Errorf("Args = %v, want %v", result.Args, wantArgs)
	}
}

func TestParseFlags_StringSliceRepeatedFlag(t *testing.T) {
	type Flags struct {
		Tags []string `flag:"tags"`
	}

	result, err := ParseFlags[Flags]([]string{"--tags", "a", "--tags", "b"})
	if err != nil {
		t.Fatalf("ParseFlags failed: %v", err)
	}
	want := []string{"a", "b"}
	if !reflect.DeepEqual(result.Flags.Tags, want) {
		t.Errorf("Tags = %v, want %v", result.Flags.Tags, want)
	}
}

func TestParseFlags_StringSliceCommaSeparated(t *testing.T) {
	type Flags struct {
		Tags []string `flag:"tags"`
	}

	result, err := ParseFlags[Flags]([]string{"--tags=a,b"})
	if err != nil {
		t.Fatalf("ParseFlags failed: %v", err)
	}
	want := []string{"a", "b"}
	if !reflect.DeepEqual(result.Flags.Tags, want) {
		t.Errorf("Tags = %v, want %v", result.Flags.Tags, want)
	}
}

func TestParser_GetFlagAndHasFlag(t *testing.T) {
	type Flags struct {
		Verbose bool   `flag:"v"`
		Config  string `flag:"config"`
	}

	result, _ := ParseFlags[Flags]([]string{"-v", "--config=test.yaml"})

	// Test GetFlag
	if got := result.GetFlag("v"); got != "true" {
		t.Errorf("GetFlag(v) = %q, want %q", got, "true")
	}
	if got := result.GetFlag("config"); got != "test.yaml" {
		t.Errorf("GetFlag(config) = %q, want %q", got, "test.yaml")
	}
	if got := result.GetFlag("nonexistent"); got != "" {
		t.Errorf("GetFlag(nonexistent) = %q, want empty string", got)
	}

	// Test HasFlag
	if !result.HasFlag("v") {
		t.Error("HasFlag(v) = false, want true")
	}
	if !result.HasFlag("config") {
		t.Error("HasFlag(config) = false, want true")
	}
	if result.HasFlag("nonexistent") {
		t.Error("HasFlag(nonexistent) = true, want false")
	}
}

func TestRunSubcommandsWithGroups_SubcommandHelp(t *testing.T) {
	config := HelpConfig{
		Command: CommandInfo{Name: "testcli"},
		SubCommands: map[string]SubCommandInfo{
			"run": {Name: "run", Description: "Run command"},
		},
	}
	called := false
	handlers := map[string]SubcommandHandler{
		"run": func(ctx context.Context, args []string) error {
			called = true
			return nil
		},
	}

	output := captureStdout(t, func() {
		if err := RunSubcommandsWithGroups(context.Background(), []string{"run", "--help"}, config, struct{}{}, handlers, nil); err != nil {
			t.Fatalf("RunSubcommandsWithGroups returned error: %v", err)
		}
	})

	if called {
		t.Fatal("expected help to short-circuit handler")
	}
	if !strings.Contains(output, "USAGE:") {
		t.Fatalf("expected help output to include USAGE, got: %s", output)
	}
	if !strings.Contains(output, "testcli run") {
		t.Fatalf("expected help output to include command name, got: %s", output)
	}
}

func TestRunSubcommandsWithGroups_SubcommandHelpLLM(t *testing.T) {
	config := HelpConfig{
		Command: CommandInfo{Name: "testcli"},
		SubCommands: map[string]SubCommandInfo{
			"run": {Name: "run", Description: "Run command"},
		},
	}
	called := false
	handlers := map[string]SubcommandHandler{
		"run": func(ctx context.Context, args []string) error {
			called = true
			return nil
		},
	}

	output := captureStdout(t, func() {
		if err := RunSubcommandsWithGroups(context.Background(), []string{"run", "--help-llm"}, config, struct{}{}, handlers, nil); err != nil {
			t.Fatalf("RunSubcommandsWithGroups returned error: %v", err)
		}
	})

	if called {
		t.Fatal("expected help to short-circuit handler")
	}
	if !strings.Contains(output, "# testcli run") {
		t.Fatalf("expected LLM help output to include header, got: %s", output)
	}
	if !strings.Contains(output, "## Usage") {
		t.Fatalf("expected LLM help output to include usage section, got: %s", output)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = old
	}()
	fn()
	_ = w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout failed: %v", err)
	}
	_ = r.Close()
	return string(out)
}

func TestParseFlags_EdgeCases(t *testing.T) {
	type Flags struct {
		Verbose bool   `flag:"v"`
		Port    int    `flag:"port"`
		Name    string `flag:"name"`
	}

	t.Run("double-dash with args after", func(t *testing.T) {
		result, _ := ParseFlags[Flags]([]string{"-v", "arg1", "--", "--foo", "--bar=baz"})
		if !result.Flags.Verbose {
			t.Error("Verbose should be true")
		}
		if len(result.Args) != 1 || result.Args[0] != "arg1" {
			t.Errorf("Args = %v, want [arg1]", result.Args)
		}
		expectedRemaining := []string{"--foo", "--bar=baz"}
		if len(result.RemainingArgs) != 2 || result.RemainingArgs[0] != expectedRemaining[0] || result.RemainingArgs[1] != expectedRemaining[1] {
			t.Errorf("RemainingArgs = %v, want %v", result.RemainingArgs, expectedRemaining)
		}
	})

	t.Run("double-dash as last arg", func(t *testing.T) {
		result, _ := ParseFlags[Flags]([]string{"-v", "arg1", "--"})
		if !result.Flags.Verbose {
			t.Error("Verbose should be true")
		}
		if len(result.Args) != 1 || result.Args[0] != "arg1" {
			t.Errorf("Args = %v, want [arg1]", result.Args)
		}
		if len(result.RemainingArgs) != 0 {
			t.Errorf("RemainingArgs = %v, want empty", result.RemainingArgs)
		}
	})

	t.Run("flag with equals sign in value", func(t *testing.T) {
		result, _ := ParseFlags[Flags]([]string{"--name=foo=bar"})
		if result.Flags.Name != "foo=bar" {
			t.Errorf("Name = %q, want %q", result.Flags.Name, "foo=bar")
		}
	})
}

func TestDoubleDashSeparator(t *testing.T) {
	t.Run("ParseFlags with -- separator", func(t *testing.T) {
		type Flags struct {
			Service string `flag:"service"`
		}

		result, err := ParseFlags[Flags]([]string{"--service=catch", "--", "--data-dir=/home/yeet/data", "foo", "bar", "--baz=qux"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		// Check that service flag is parsed
		if result.Flags.Service != "catch" {
			t.Errorf("Service = %q, want %q", result.Flags.Service, "catch")
		}

		// Check that args after -- are captured
		if len(result.RemainingArgs) != 4 {
			t.Errorf("RemainingArgs length = %d, want 4", len(result.RemainingArgs))
		}

		expectedRemaining := []string{"--data-dir=/home/yeet/data", "foo", "bar", "--baz=qux"}
		for i, arg := range expectedRemaining {
			if i >= len(result.RemainingArgs) || result.RemainingArgs[i] != arg {
				t.Errorf("RemainingArgs[%d] = %q, want %q", i, result.RemainingArgs[i], arg)
			}
		}

	})

	t.Run("ParseWithCommand with -- separator", func(t *testing.T) {
		type GlobalFlags struct {
			Verbose bool `flag:"verbose"`
		}

		type RunFlags struct {
			Service string `flag:"service"`
		}

		result, err := ParseWithCommand[GlobalFlags, RunFlags, struct{}]([]string{"--verbose", "run", "./catch", "--service=catch", "--", "--data-dir=/home/yeet/data"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		// Check parsed values
		if !result.GlobalFlags.Verbose {
			t.Error("Expected Verbose to be true")
		}

		if result.SubCommandFlags.Service != "catch" {
			t.Errorf("Service = %q, want %q", result.SubCommandFlags.Service, "catch")
		}

		if result.SubCommand != "run" {
			t.Errorf("SubCommand = %q, want %q", result.SubCommand, "run")
		}

		// Check args before -- (using Parser.Args since Args type is struct{})
		if len(result.Parser.Args) != 1 || result.Parser.Args[0] != "./catch" {
			t.Errorf("Args = %v, want [./catch]", result.Parser.Args)
		}

		// Check args after --
		if len(result.RemainingArgs) != 1 || result.RemainingArgs[0] != "--data-dir=/home/yeet/data" {
			t.Errorf("RemainingArgs = %v, want [--data-dir=/home/yeet/data]", result.RemainingArgs)
		}
	})

	t.Run("Empty -- separator", func(t *testing.T) {
		type Flags struct{}

		result, err := ParseFlags[Flags]([]string{"arg1", "--", ""})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if len(result.Args) != 1 || result.Args[0] != "arg1" {
			t.Errorf("Args = %v, want [arg1]", result.Args)
		}

		// Empty string should be preserved
		if len(result.RemainingArgs) != 1 || result.RemainingArgs[0] != "" {
			t.Errorf("RemainingArgs = %v, want [\"\"]", result.RemainingArgs)
		}
	})

	t.Run("No args after -- separator", func(t *testing.T) {
		type Flags struct{}

		result, err := ParseFlags[Flags]([]string{"arg1", "--"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if len(result.Args) != 1 || result.Args[0] != "arg1" {
			t.Errorf("Args = %v, want [arg1]", result.Args)
		}

		// No args after --
		if len(result.RemainingArgs) != 0 {
			t.Errorf("RemainingArgs = %v, want []", result.RemainingArgs)
		}
	})
}

func TestParseWithCommand(t *testing.T) {
	type GlobalFlags struct {
		Verbose    bool   `flag:"v"`
		ControlURL string `flag:"control-url"`
	}

	type SubCmdFlags struct {
		Remove bool `flag:"rm"`
	}

	result, err := ParseWithCommand[GlobalFlags, SubCmdFlags, struct{}]([]string{"-v", "run", "--rm", "bin", "--control-url=localhost:3000"})
	if err != nil {
		t.Fatalf("ParseWithCommand failed: %v", err)
	}

	if result.SubCommand != "run" {
		t.Errorf("SubCommand = %q, want %q", result.SubCommand, "run")
	}
	if len(result.Parser.Args) != 1 || result.Parser.Args[0] != "bin" {
		t.Errorf("Args = %v, want [bin]", result.Parser.Args)
	}
	if !result.GlobalFlags.Verbose {
		t.Error("Verbose = false, want true")
	}
	if result.GlobalFlags.ControlURL != "localhost:3000" {
		t.Errorf("ControlURL = %q, want %q", result.GlobalFlags.ControlURL, "localhost:3000")
	}
	if !result.SubCommandFlags.Remove {
		t.Error("Remove = false, want true")
	}
}

func TestParseFlags_InvalidTypes(t *testing.T) {
	t.Run("invalid bool value", func(t *testing.T) {
		type Flags struct {
			Enabled bool `flag:"enabled"`
		}
		_, err := ParseFlags[Flags]([]string{"--enabled=invalid"})
		if err == nil {
			t.Error("ParseFlags should fail with invalid bool value")
		}
	})

	t.Run("invalid int value", func(t *testing.T) {
		type Flags struct {
			Port int `flag:"port"`
		}
		_, err := ParseFlags[Flags]([]string{"--port=notanumber"})
		if err == nil {
			t.Error("ParseFlags should fail with invalid int value")
		}
	})

	t.Run("invalid duration value", func(t *testing.T) {
		type Flags struct {
			Timeout time.Duration `flag:"timeout"`
		}
		_, err := ParseFlags[Flags]([]string{"--timeout=invalid"})
		if err == nil {
			t.Error("ParseFlags should fail with invalid duration value")
		}
	})

	t.Run("invalid URL value", func(t *testing.T) {
		type Flags struct {
			URL *url.URL `flag:"url"`
		}
		_, err := ParseFlags[Flags]([]string{"--url=ht!tp://invalid"})
		if err == nil {
			t.Error("ParseFlags should fail with invalid URL value")
		}
	})
}

// Ergonomic API tests for real-world usage patterns

func TestCommand_HelpFlag(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr error
	}{
		{
			name:    "global help flag",
			args:    []string{"--help"},
			wantErr: ErrHelp,
		},
		{
			name:    "global help subcommand",
			args:    []string{"help"},
			wantErr: ErrHelp,
		},
		{
			name:    "short help flag",
			args:    []string{"-h"},
			wantErr: ErrHelp,
		},
		{
			name:    "subcommand help flag",
			args:    []string{"run", "--help"},
			wantErr: ErrSubCommandHelp,
		},
		{
			name:    "subcommand help flag short",
			args:    []string{"run", "-h"},
			wantErr: ErrSubCommandHelp,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			type GlobalFlags struct {
				Verbose bool `flag:"v"`
			}

			type RunFlags struct {
				Remove bool `flag:"rm"`
			}

			result, err := ParseWithCommand[GlobalFlags, RunFlags, struct{}](tt.args)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ParseWithCommand() error = %v, want %v", err, tt.wantErr)
			}
			if err == nil && result == nil {
				t.Error("result should not be nil when no error")
			}
		})
	}
}

func TestCommand_SubCommandRouting(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"v"`
	}

	type RunFlags struct {
		Remove      bool   `flag:"rm"`
		Interactive bool   `flag:"it"`
		Name        string `flag:"name"`
	}

	type StatusFlags struct {
		Format string `flag:"format"`
	}

	tests := []struct {
		name              string
		args              []string
		wantSubCommand    string
		wantGlobalVerbose bool
		wantArgs          []string
	}{
		{
			name:           "run with flags and args",
			args:           []string{"run", "--rm", "--name=test", "bin"},
			wantSubCommand: "run",
			wantArgs:       []string{"bin"},
		},
		{
			name:              "run with global flag",
			args:              []string{"-v", "run", "--rm", "bin"},
			wantSubCommand:    "run",
			wantGlobalVerbose: true,
			wantArgs:          []string{"bin"},
		},
		{
			name:              "global flag after subcommand",
			args:              []string{"run", "-v", "--rm", "bin"},
			wantSubCommand:    "run",
			wantGlobalVerbose: true,
			wantArgs:          []string{"bin"},
		},
		{
			name:           "status subcommand",
			args:           []string{"status", "--format=json"},
			wantSubCommand: "status",
			wantArgs:       []string{},
		},
		{
			name:              "bool flag between positional args",
			args:              []string{"run", "bar", "-v", "123"},
			wantSubCommand:    "run",
			wantGlobalVerbose: true,
			wantArgs:          []string{"bar", "123"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result any
			var err error

			// Route to the appropriate subcommand parser based on what we expect
			switch tt.wantSubCommand {
			case "run":
				result, err = ParseWithCommand[GlobalFlags, RunFlags, struct{}](tt.args)
			case "status":
				result, err = ParseWithCommand[GlobalFlags, StatusFlags, struct{}](tt.args)
			default:
				t.Fatalf("unexpected subcommand: %s", tt.wantSubCommand)
			}

			if err != nil {
				t.Fatalf("ParseWithCommand() error = %v", err)
			}

			// Type assert to check results
			switch tt.wantSubCommand {
			case "run":
				r := result.(*TypedParseResult[GlobalFlags, RunFlags, struct{}])
				if r.SubCommand != tt.wantSubCommand {
					t.Errorf("SubCommand = %q, want %q", r.SubCommand, tt.wantSubCommand)
				}
				if r.GlobalFlags.Verbose != tt.wantGlobalVerbose {
					t.Errorf("GlobalFlags.Verbose = %v, want %v", r.GlobalFlags.Verbose, tt.wantGlobalVerbose)
				}
				if !reflect.DeepEqual(r.Parser.Args, tt.wantArgs) {
					t.Errorf("Args = %v, want %v", r.Parser.Args, tt.wantArgs)
				}
			case "status":
				r := result.(*TypedParseResult[GlobalFlags, StatusFlags, struct{}])
				if r.SubCommand != tt.wantSubCommand {
					t.Errorf("SubCommand = %q, want %q", r.SubCommand, tt.wantSubCommand)
				}
			}
		})
	}
}

func TestCommand_UnknownSubCommand(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"v"`
	}

	type RunFlags struct {
		Remove bool `flag:"rm"`
	}

	// Try to parse with an unknown subcommand (but valid or no flags)
	result, err := ParseWithCommand[GlobalFlags, RunFlags, struct{}]([]string{"unknown"})

	// Should still parse successfully, just with "unknown" as the SubCommand
	if err != nil {
		t.Fatalf("ParseWithCommand() should not error on unknown subcommand: %v", err)
	}
	if result.SubCommand != "unknown" {
		t.Errorf("SubCommand = %q, want %q", result.SubCommand, "unknown")
	}
}

func TestCommand_RealWorldExample(t *testing.T) {
	// This test demonstrates the ergonomic API for a real CLI app

	type GlobalFlags struct {
		Verbose    bool   `flag:"v"`
		ControlURL string `flag:"control-url"`
	}

	type RunFlags struct {
		Remove      bool `flag:"rm"`
		Interactive bool `flag:"it"`
	}

	// Simulate: yeet -v run --rm --it bin1 bin2
	result, err := ParseWithCommand[GlobalFlags, RunFlags, struct{}]([]string{
		"-v",
		"run",
		"--rm",
		"--it",
		"bin1",
		"bin2",
	})

	if err != nil {
		t.Fatalf("ParseWithCommand() error = %v", err)
	}

	// Check global flags
	if !result.GlobalFlags.Verbose {
		t.Error("Expected verbose flag to be set")
	}

	// Check subcommand
	if result.SubCommand != "run" {
		t.Errorf("SubCommand = %q, want %q", result.SubCommand, "run")
	}

	// Check subcommand flags
	if !result.SubCommandFlags.Remove {
		t.Error("Expected rm flag to be set")
	}
	if !result.SubCommandFlags.Interactive {
		t.Error("Expected it flag to be set")
	}

	// Check positional args
	wantArgs := []string{"bin1", "bin2"}
	if !reflect.DeepEqual(result.Parser.Args, wantArgs) {
		t.Errorf("Args = %v, want %v", result.Parser.Args, wantArgs)
	}
}

func TestCommand_NoSubCommand(t *testing.T) {
	type GlobalFlags struct {
		Version bool `flag:"version"`
	}

	type EmptyFlags struct{}

	// Just global flags, no subcommand: yeet --version
	result, err := ParseWithCommand[GlobalFlags, EmptyFlags, struct{}]([]string{"--version"})
	if err != nil {
		t.Fatalf("ParseWithCommand() error = %v", err)
	}

	if result.SubCommand != "" {
		t.Errorf("SubCommand = %q, want empty", result.SubCommand)
	}
	if !result.GlobalFlags.Version {
		t.Error("Expected version flag to be set")
	}
}

func TestHelpGeneration(t *testing.T) {
	type GlobalFlags struct {
		Verbose    bool   `flag:"v" help:"Enable verbose output"`
		ControlURL string `flag:"control-url" help:"Control plane URL" default:"localhost:3000"`
	}

	type RunFlags struct {
		Detach bool   `flag:"detach" help:"Run in detached mode"`
		Env    string `flag:"env" help:"Set environment"`
	}

	config := HelpConfig{
		Command: CommandInfo{
			Name:        "testapp",
			Description: "A test application",
			Examples: []string{
				"# Show status",
				"testapp status",
				"",
				"# Run a service",
				"testapp run myservice",
			},
		},
		SubCommands: map[string]SubCommandInfo{
			"run": {
				Name:        "run",
				Description: "Run a service",
				Usage:       "SERVICE",
				Examples: []string{
					"# Run a service",
					"testapp run web-server",
					"",
					"# Run with environment",
					"testapp run --env=prod api",
				},
			},
			"status": {
				Name:        "status",
				Description: "Show service status",
				Usage:       "",
				Examples: []string{
					"# Show status",
					"testapp status",
				},
			},
		},
	}

	t.Run("global help with --help", func(t *testing.T) {
		result, err := ParseWithCommandAndHelp[GlobalFlags, RunFlags, struct{}]([]string{"--help"}, config)
		if !errors.Is(err, ErrHelp) {
			t.Errorf("Expected ErrHelp, got %v", err)
		}
		if result.HelpText == "" {
			t.Error("Expected help text to be generated")
		}
		if !strings.Contains(result.HelpText, "testapp") {
			t.Error("Help text should contain app name")
		}
		if !strings.Contains(result.HelpText, "A test application") {
			t.Error("Help text should contain description")
		}
		if !strings.Contains(result.HelpText, "run") {
			t.Error("Help text should list run subcommand")
		}
		if !strings.Contains(result.HelpText, "status") {
			t.Error("Help text should list status subcommand")
		}
		if !strings.Contains(result.HelpText, "Enable verbose output") {
			t.Error("Help text should show flag descriptions")
		}
	})

	t.Run("global help with help command", func(t *testing.T) {
		result, err := ParseWithCommandAndHelp[GlobalFlags, RunFlags, struct{}]([]string{"help"}, config)
		if !errors.Is(err, ErrHelp) {
			t.Errorf("Expected ErrHelp, got %v", err)
		}
		if result.HelpText == "" {
			t.Error("Expected help text to be generated")
		}
	})

	t.Run("subcommand help with --help", func(t *testing.T) {
		result, err := ParseWithCommandAndHelp[GlobalFlags, RunFlags, struct{}]([]string{"run", "--help"}, config)
		if !errors.Is(err, ErrSubCommandHelp) {
			t.Errorf("Expected ErrSubCommandHelp, got %v", err)
		}
		if result.HelpText == "" {
			t.Error("Expected help text to be generated")
		}
		if !strings.Contains(result.HelpText, "Run a service") {
			t.Error("Help text should contain subcommand description")
		}
		if !strings.Contains(result.HelpText, "testapp run") {
			t.Error("Help text should contain usage")
		}
		if !strings.Contains(result.HelpText, "Run in detached mode") {
			t.Error("Help text should show subcommand flag descriptions")
		}
		if !strings.Contains(result.HelpText, "Enable verbose output") {
			t.Error("Help text should show global flag descriptions")
		}
	})

	t.Run("subcommand help with help command", func(t *testing.T) {
		result, err := ParseWithCommandAndHelp[GlobalFlags, RunFlags, struct{}]([]string{"help", "run"}, config)
		if !errors.Is(err, ErrSubCommandHelp) {
			t.Errorf("Expected ErrSubCommandHelp, got %v", err)
		}
		if result.HelpText == "" {
			t.Error("Expected help text to be generated")
		}
		if !strings.Contains(result.HelpText, "Run a service") {
			t.Error("Help text should contain subcommand description")
		}
	})

	t.Run("no help requested", func(t *testing.T) {
		result, err := ParseWithCommandAndHelp[GlobalFlags, RunFlags, struct{}]([]string{"-v", "run", "--detach", "myservice"}, config)
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if result.HelpText != "" {
			t.Error("Help text should not be generated when help is not requested")
		}
		if result.SubCommand != "run" {
			t.Errorf("SubCommand = %q, want %q", result.SubCommand, "run")
		}
		if !result.GlobalFlags.Verbose {
			t.Error("Verbose flag should be set")
		}
		if !result.SubCommandFlags.Detach {
			t.Error("Detach flag should be set")
		}
	})
}

func TestGenerateGlobalHelp(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool   `flag:"v" help:"Enable verbose mode"`
		Config  string `flag:"config" help:"Config file path" default:"/etc/app.conf"`
	}

	config := HelpConfig{
		Command: CommandInfo{
			Name:        "myapp",
			Description: "My awesome application",
			Examples: []string{
				"myapp status",
				"myapp run foo",
			},
		},
		SubCommands: map[string]SubCommandInfo{
			"run": {
				Name:        "run",
				Description: "Run a thing",
			},
			"status": {
				Name:        "status",
				Description: "Check status",
			},
		},
	}

	var globalFlags GlobalFlags
	helpText := GenerateGlobalHelp(config, globalFlags)

	// Verify structure
	if !strings.Contains(helpText, "myapp - My awesome application") {
		t.Error("Should contain app name and description")
	}
	if !strings.Contains(helpText, "USAGE:") {
		t.Error("Should contain USAGE section")
	}
	if !strings.Contains(helpText, "COMMANDS:") {
		t.Error("Should contain COMMANDS section")
	}
	if !strings.Contains(helpText, "GLOBAL OPTIONS:") {
		t.Error("Should contain GLOBAL OPTIONS section")
	}
	if !strings.Contains(helpText, "Enable verbose mode") {
		t.Error("Should contain flag help text")
	}
	if !strings.Contains(helpText, "default: /etc/app.conf") {
		t.Error("Should contain default values")
	}
	if !strings.Contains(helpText, "run") && !strings.Contains(helpText, "Run a thing") {
		t.Error("Should list subcommands with descriptions")
	}
}

func TestGenerateSubCommandHelp(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"v" help:"Verbose mode"`
	}

	type RunFlags struct {
		Detach bool   `flag:"detach" help:"Detach from terminal"`
		Name   string `flag:"name" help:"Service name"`
	}

	config := HelpConfig{
		Command: CommandInfo{
			Name: "myapp",
		},
		SubCommands: map[string]SubCommandInfo{
			"run": {
				Name:        "run",
				Description: "Run a service instance",
				Usage:       "SERVICE [ARGS...]",
				Examples: []string{
					"myapp run web-server",
					"myapp run --detach api-server",
				},
			},
		},
	}

	var globalFlags GlobalFlags
	var runFlags RunFlags
	helpText := GenerateSubCommandHelp(config, "run", globalFlags, runFlags, struct{}{})

	if !strings.Contains(helpText, "Run a service instance") {
		t.Error("Should contain subcommand description")
	}
	if !strings.Contains(helpText, "myapp run [OPTIONS] SERVICE [ARGS...]") {
		t.Error("Should contain usage with positional args")
	}
	if !strings.Contains(helpText, "OPTIONS:") {
		t.Error("Should contain OPTIONS section")
	}
	if !strings.Contains(helpText, "Detach from terminal") {
		t.Error("Should contain subcommand flag descriptions")
	}
	if !strings.Contains(helpText, "GLOBAL OPTIONS:") {
		t.Error("Should contain GLOBAL OPTIONS section")
	}
	if !strings.Contains(helpText, "Verbose mode") {
		t.Error("Should contain global flag descriptions")
	}
	if !strings.Contains(helpText, "EXAMPLES:") {
		t.Error("Should contain EXAMPLES section")
	}
}

func TestExtractSubcommand(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected string
	}{
		{
			name:     "simple subcommand",
			args:     []string{"run", "service"},
			expected: "run",
		},
		{
			name:     "subcommand after global flags",
			args:     []string{"-v", "--timeout=30s", "run", "service"},
			expected: "run",
		},
		{
			name:     "no subcommand, only flags",
			args:     []string{"-v", "--timeout=30s"},
			expected: "",
		},
		{
			name:     "help is not a subcommand",
			args:     []string{"help", "run"},
			expected: "run",
		},
		{
			name:     "help only",
			args:     []string{"help"},
			expected: "",
		},
		{
			name:     "empty args",
			args:     []string{},
			expected: "",
		},
		{
			name:     "help flag before subcommand",
			args:     []string{"--help", "run"},
			expected: "run",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractSubcommand(tt.args)
			if result != tt.expected {
				t.Errorf("ExtractSubcommand(%v) = %q, want %q", tt.args, result, tt.expected)
			}
		})
	}
}

func TestParseAndHandleHelp(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"v" help:"Verbose mode"`
	}

	type RunFlags struct {
		Detach bool `flag:"detach" help:"Detach from terminal"`
	}

	config := HelpConfig{
		Command: CommandInfo{
			Name:        "testapp",
			Description: "A test application",
		},
		SubCommands: map[string]SubCommandInfo{
			"run": {
				Name:        "run",
				Description: "Run a service",
			},
		},
	}

	t.Run("help returns ErrShown", func(t *testing.T) {
		result, err := ParseAndHandleHelp[GlobalFlags, RunFlags, struct{}]([]string{"--help"}, config)
		if !errors.Is(err, ErrShown) {
			t.Errorf("Expected ErrShown, got %v", err)
		}
		if result != nil {
			t.Error("Expected nil result when help is displayed")
		}
	})

	t.Run("subcommand help returns ErrShown", func(t *testing.T) {
		result, err := ParseAndHandleHelp[GlobalFlags, RunFlags, struct{}]([]string{"run", "--help"}, config)
		if !errors.Is(err, ErrShown) {
			t.Errorf("Expected ErrShown, got %v", err)
		}
		if result != nil {
			t.Error("Expected nil result when help is displayed")
		}
	})

	t.Run("normal parse returns result", func(t *testing.T) {
		result, err := ParseAndHandleHelp[GlobalFlags, RunFlags, struct{}]([]string{"-v", "run", "--detach"}, config)
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if result == nil {
			t.Fatal("Expected non-nil result")
		}
		if !result.GlobalFlags.Verbose {
			t.Error("Expected Verbose to be true")
		}
		if !result.SubCommandFlags.Detach {
			t.Error("Expected Detach to be true")
		}
		if result.SubCommand != "run" {
			t.Errorf("Expected SubCommand to be 'run', got %q", result.SubCommand)
		}
	})
}

func TestShortFlags(t *testing.T) {
	type Flags struct {
		Verbose bool   `flag:"verbose" short:"v" help:"Verbose mode"`
		Output  string `flag:"output" short:"o" help:"Output file"`
		Count   int    `flag:"count" short:"c" help:"Number of items"`
	}

	t.Run("use short flag", func(t *testing.T) {
		result, err := ParseFlags[Flags]([]string{"-v", "-o=test.txt", "-c=5"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !result.Flags.Verbose {
			t.Error("Expected Verbose to be true")
		}
		if result.Flags.Output != "test.txt" {
			t.Errorf("Expected Output to be 'test.txt', got %q", result.Flags.Output)
		}
		if result.Flags.Count != 5 {
			t.Errorf("Expected Count to be 5, got %d", result.Flags.Count)
		}
	})

	t.Run("use long flag", func(t *testing.T) {
		result, err := ParseFlags[Flags]([]string{"--verbose", "--output=test.txt", "--count=5"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !result.Flags.Verbose {
			t.Error("Expected Verbose to be true")
		}
		if result.Flags.Output != "test.txt" {
			t.Errorf("Expected Output to be 'test.txt', got %q", result.Flags.Output)
		}
		if result.Flags.Count != 5 {
			t.Errorf("Expected Count to be 5, got %d", result.Flags.Count)
		}
	})

	t.Run("mix short and long flags", func(t *testing.T) {
		result, err := ParseFlags[Flags]([]string{"-v", "--output=test.txt", "-c=5"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !result.Flags.Verbose {
			t.Error("Expected Verbose to be true")
		}
		if result.Flags.Output != "test.txt" {
			t.Errorf("Expected Output to be 'test.txt', got %q", result.Flags.Output)
		}
		if result.Flags.Count != 5 {
			t.Errorf("Expected Count to be 5, got %d", result.Flags.Count)
		}
	})

	t.Run("short flag with equals", func(t *testing.T) {
		result, err := ParseFlags[Flags]([]string{"-v", "-o=test.txt", "-c=5"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !result.Flags.Verbose {
			t.Error("Expected Verbose to be true")
		}
		if result.Flags.Output != "test.txt" {
			t.Errorf("Expected Output to be 'test.txt', got %q", result.Flags.Output)
		}
		if result.Flags.Count != 5 {
			t.Errorf("Expected Count to be 5, got %d", result.Flags.Count)
		}
	})
}

func TestShortFlagsWithCommand(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"verbose" short:"v" help:"Verbose mode"`
	}

	type RunFlags struct {
		Detach bool `flag:"detach" short:"d" help:"Detach from terminal"`
	}

	t.Run("short global flag before subcommand", func(t *testing.T) {
		result, err := ParseWithCommand[GlobalFlags, RunFlags, struct{}]([]string{"-v", "run"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !result.GlobalFlags.Verbose {
			t.Error("Expected Verbose to be true")
		}
		if result.SubCommand != "run" {
			t.Errorf("Expected SubCommand to be 'run', got %q", result.SubCommand)
		}
	})

	t.Run("short subcommand flag after subcommand", func(t *testing.T) {
		result, err := ParseWithCommand[GlobalFlags, RunFlags, struct{}]([]string{"run", "-d"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !result.SubCommandFlags.Detach {
			t.Error("Expected Detach to be true")
		}
		if result.SubCommand != "run" {
			t.Errorf("Expected SubCommand to be 'run', got %q", result.SubCommand)
		}
	})

	t.Run("mix short and long flags with subcommand", func(t *testing.T) {
		result, err := ParseWithCommand[GlobalFlags, RunFlags, struct{}]([]string{"-v", "run", "--detach"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !result.GlobalFlags.Verbose {
			t.Error("Expected Verbose to be true")
		}
		if !result.SubCommandFlags.Detach {
			t.Error("Expected Detach to be true")
		}
	})
}

func TestShortFlagHelpGeneration(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"verbose" short:"v" help:"Verbose mode"`
	}

	config := HelpConfig{
		Command: CommandInfo{
			Name:        "testapp",
			Description: "A test application",
		},
	}

	helpText := GenerateGlobalHelp(config, GlobalFlags{})

	// Should show both short and long forms
	if !strings.Contains(helpText, "-v, --verbose") {
		t.Error("Help text should contain '-v, --verbose' for flag with short name")
	}
	if !strings.Contains(helpText, "Verbose mode") {
		t.Error("Help text should contain flag description")
	}
}

func TestDefaultValues(t *testing.T) {
	type Flags struct {
		Format  string `flag:"format" help:"Output format" default:"table"`
		Timeout int    `flag:"timeout" help:"Timeout in seconds" default:"30"`
		Verbose bool   `flag:"verbose" help:"Verbose mode" default:"true"`
		Port    int    `flag:"port" help:"Port number"`
	}

	t.Run("default values applied when flag not provided", func(t *testing.T) {
		result, err := ParseFlags[Flags]([]string{})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.Flags.Format != "table" {
			t.Errorf("Expected Format to be 'table', got %q", result.Flags.Format)
		}
		if result.Flags.Timeout != 30 {
			t.Errorf("Expected Timeout to be 30, got %d", result.Flags.Timeout)
		}
		if !result.Flags.Verbose {
			t.Error("Expected Verbose to be true")
		}
	})

	t.Run("explicit values override defaults", func(t *testing.T) {
		result, err := ParseFlags[Flags]([]string{"--format=json", "--timeout=60", "--verbose=false"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.Flags.Format != "json" {
			t.Errorf("Expected Format to be 'json', got %q", result.Flags.Format)
		}
		if result.Flags.Timeout != 60 {
			t.Errorf("Expected Timeout to be 60, got %d", result.Flags.Timeout)
		}
		if result.Flags.Verbose {
			t.Error("Expected Verbose to be false")
		}
	})

	t.Run("fields without default stay at zero value", func(t *testing.T) {
		result, err := ParseFlags[Flags]([]string{})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.Flags.Port != 0 {
			t.Errorf("Expected Port to be 0, got %d", result.Flags.Port)
		}
	})
}

func TestDefaultValuesWithCommand(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"verbose" short:"v" help:"Verbose mode"`
	}

	type StatusFlags struct {
		Format string `flag:"format" help:"Output format (table, json, json-pretty)" default:"table"`
	}

	t.Run("default applied for subcommand flags", func(t *testing.T) {
		result, err := ParseWithCommand[GlobalFlags, StatusFlags, struct{}]([]string{"status"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.SubCommandFlags.Format != "table" {
			t.Errorf("Expected Format to be 'table', got %q", result.SubCommandFlags.Format)
		}
	})

	t.Run("explicit value overrides default for subcommand flags", func(t *testing.T) {
		result, err := ParseWithCommand[GlobalFlags, StatusFlags, struct{}]([]string{"status", "--format=json"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.SubCommandFlags.Format != "json" {
			t.Errorf("Expected Format to be 'json', got %q", result.SubCommandFlags.Format)
		}
	})
}

func TestRunSubcommands(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"verbose" short:"v" help:"Verbose mode"`
	}

	config := HelpConfig{
		Command: CommandInfo{
			Name:        "testapp",
			Description: "A test application",
		},
		SubCommands: map[string]SubCommandInfo{
			"run": {
				Name:        "run",
				Description: "Run a service",
			},
			"stop": {
				Name:        "stop",
				Description: "Stop a service",
			},
		},
	}

	t.Run("calls correct handler", func(t *testing.T) {
		called := ""
		handlers := map[string]SubcommandHandler{
			"run": func(ctx context.Context, args []string) error {
				called = "run"
				return nil
			},
			"stop": func(ctx context.Context, args []string) error {
				called = "stop"
				return nil
			},
		}

		err := RunSubcommands(t.Context(), []string{"run", "service1"}, config, GlobalFlags{}, handlers)
		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}
		if called != "run" {
			t.Errorf("Expected 'run' handler to be called, got %q", called)
		}
	})

	t.Run("returns error from handler", func(t *testing.T) {
		handlers := map[string]SubcommandHandler{
			"run": func(ctx context.Context, args []string) error {
				return fmt.Errorf("test error")
			},
		}

		err := RunSubcommands(t.Context(), []string{"run"}, config, GlobalFlags{}, handlers)
		if err == nil {
			t.Error("Expected error from handler")
		}
		if err.Error() != "test error" {
			t.Errorf("Expected 'test error', got %q", err.Error())
		}
	})

	t.Run("unknown subcommand returns error", func(t *testing.T) {
		handlers := map[string]SubcommandHandler{
			"run": func(ctx context.Context, args []string) error { return nil },
		}

		err := RunSubcommands(t.Context(), []string{"unknown"}, config, GlobalFlags{}, handlers)
		if err == nil {
			t.Error("Expected error for unknown subcommand")
		}
		if !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("Error should mention 'unknown command', got %q", err.Error())
		}
	})

	t.Run("empty args shows help", func(t *testing.T) {
		handlers := map[string]SubcommandHandler{
			"run": func(ctx context.Context, args []string) error { return nil },
		}

		err := RunSubcommands(t.Context(), []string{}, config, GlobalFlags{}, handlers)
		if err != nil {
			t.Errorf("Expected no error for empty args, got %v", err)
		}
	})

	t.Run("help flag shows help", func(t *testing.T) {
		handlers := map[string]SubcommandHandler{
			"run": func(ctx context.Context, args []string) error { return nil },
		}

		err := RunSubcommands(t.Context(), []string{"--help"}, config, GlobalFlags{}, handlers)
		if err != nil {
			t.Errorf("Expected no error for --help, got %v", err)
		}
	})

	t.Run("help command shows help", func(t *testing.T) {
		handlers := map[string]SubcommandHandler{
			"run": func(ctx context.Context, args []string) error { return nil },
		}

		err := RunSubcommands(t.Context(), []string{"help"}, config, GlobalFlags{}, handlers)
		if err != nil {
			t.Errorf("Expected no error for help command, got %v", err)
		}
	})
}

func TestParseFlagWithIntegerTypes(t *testing.T) {
	type Flags struct {
		Lines int `flag:"lines" short:"n" help:"Number of lines"`
	}

	t.Run("positive number with equals", func(t *testing.T) {
		result, err := ParseFlags[Flags]([]string{"-n=10"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.Flags.Lines != 10 {
			t.Errorf("Expected Lines to be 10, got %d", result.Flags.Lines)
		}
	})

	t.Run("positive number with space", func(t *testing.T) {
		result, err := ParseFlags[Flags]([]string{"-n", "10"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.Flags.Lines != 10 {
			t.Errorf("Expected Lines to be 10, got %d", result.Flags.Lines)
		}
	})

	t.Run("negative number with equals", func(t *testing.T) {
		result, err := ParseFlags[Flags]([]string{"-n=-10"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.Flags.Lines != -10 {
			t.Errorf("Expected Lines to be -10, got %d", result.Flags.Lines)
		}
	})

	t.Run("negative number with space", func(t *testing.T) {
		result, err := ParseFlags[Flags]([]string{"-n", "-10"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.Flags.Lines != -10 {
			t.Errorf("Expected Lines to be -10, got %d", result.Flags.Lines)
		}
	})

	t.Run("long flag with positive number and space", func(t *testing.T) {
		result, err := ParseFlags[Flags]([]string{"--lines", "10"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.Flags.Lines != 10 {
			t.Errorf("Expected Lines to be 10, got %d", result.Flags.Lines)
		}
	})

	t.Run("long flag with negative number and space", func(t *testing.T) {
		result, err := ParseFlags[Flags]([]string{"--lines", "-10"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.Flags.Lines != -10 {
			t.Errorf("Expected Lines to be -10, got %d", result.Flags.Lines)
		}
	})

	t.Run("zero value with equals", func(t *testing.T) {
		result, err := ParseFlags[Flags]([]string{"-n=0"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.Flags.Lines != 0 {
			t.Errorf("Expected Lines to be 0, got %d", result.Flags.Lines)
		}
	})
}

func TestIsNumeric(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		// Positive integers
		{"10", true},
		{"1", true},
		{"999", true},
		{"0", true},
		// Negative integers
		{"-10", true},
		{"-1", true},
		{"-999", true},
		{"-0", true},
		// Positive floats
		{"3.14", true},
		{"0.5", true},
		{"123.456", true},
		// Negative floats
		{"-3.14", true},
		{"-0.5", true},
		{"-123.456", true},
		// Positive sign
		{"+10", true},
		{"+3.14", true},
		// Invalid
		{"-", false},
		{"+", false},
		{"", false},
		{"--10", false},
		{"-abc", false},
		{"-10abc", false},
		{"abc", false},
		{"-v", false},
		{"-verbose", false},
		{"3.14.15", false}, // Multiple dots
		{".", false},
		{"-.5", true}, // Valid: negative decimal
		{".5", true},  // Valid: decimal without leading zero
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isNumeric(tt.input)
			if got != tt.want {
				t.Errorf("isNumeric(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsNegativeNumber(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"-10", true},
		{"-1", true},
		{"-999", true},
		{"-3.14", true},
		{"-0", true},
		{"10", false},
		{"0", false},
		{"-", false},
		{"--10", false},
		{"-abc", false},
		{"-10abc", false},
		{"", false},
		{"-v", false},
		{"-verbose", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isNegativeNumber(tt.input)
			if got != tt.want {
				t.Errorf("isNegativeNumber(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestSpaceSeparatedStringFlags(t *testing.T) {
	type GlobalFlags struct {
		Name string `flag:"name" help:"Name value"`
	}

	type EmptyFlags struct{}

	t.Run("string flag with space-separated value IS auto-consumed", func(t *testing.T) {
		// New behavior: string flags consume next arg if it doesn't start with "-"
		result, err := ParseWithCommand[GlobalFlags, EmptyFlags, struct{}]([]string{"--name", "bar"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		// "bar" should be consumed as the value of --name
		if result.GlobalFlags.Name != "bar" {
			t.Errorf("Expected Name to be 'bar', got %q", result.GlobalFlags.Name)
		}
		if result.SubCommand != "" {
			t.Errorf("Expected no subcommand, got %q", result.SubCommand)
		}
	})

	t.Run("string flag with equals syntax works", func(t *testing.T) {
		result, err := ParseWithCommand[GlobalFlags, EmptyFlags, struct{}]([]string{"--name=bar"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.GlobalFlags.Name != "bar" {
			t.Errorf("Expected Name to be 'bar', got %q", result.GlobalFlags.Name)
		}
		if result.SubCommand != "" {
			t.Errorf("Expected no subcommand, got %q", result.SubCommand)
		}
	})

	t.Run("string flag with subcommand and space-separated value", func(t *testing.T) {
		result, err := ParseWithCommand[GlobalFlags, EmptyFlags, struct{}]([]string{"subcmd", "--name", "bar"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.SubCommand != "subcmd" {
			t.Errorf("Expected SubCommand to be 'subcmd', got %q", result.SubCommand)
		}
		if result.GlobalFlags.Name != "bar" {
			t.Errorf("Expected Name to be 'bar', got %q", result.GlobalFlags.Name)
		}
	})

	t.Run("string flag before subcommand with space-separated value", func(t *testing.T) {
		result, err := ParseWithCommand[GlobalFlags, EmptyFlags, struct{}]([]string{"--name", "bar", "subcmd"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.GlobalFlags.Name != "bar" {
			t.Errorf("Expected Name to be 'bar', got %q", result.GlobalFlags.Name)
		}
		if result.SubCommand != "subcmd" {
			t.Errorf("Expected SubCommand to be 'subcmd', got %q", result.SubCommand)
		}
	})

	t.Run("string flag followed by another flag doesn't consume", func(t *testing.T) {
		type Flags struct {
			Name    string `flag:"name"`
			Verbose bool   `flag:"verbose" short:"v"`
		}
		result, err := ParseWithCommand[Flags, EmptyFlags, struct{}]([]string{"--name", "-v"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		// -v should NOT be consumed as the value of --name since it starts with "-"
		if result.GlobalFlags.Name != "" {
			t.Errorf("Expected Name to be empty, got %q", result.GlobalFlags.Name)
		}
		if !result.GlobalFlags.Verbose {
			t.Error("Expected Verbose to be true")
		}
	})
}

func TestParseFlagWithCommand_NegativeNumbers(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"verbose" short:"v" help:"Verbose mode"`
	}

	type LogsFlags struct {
		Lines int `flag:"lines" short:"n" help:"Number of lines"`
	}

	t.Run("negative number as flag value", func(t *testing.T) {
		result, err := ParseWithCommand[GlobalFlags, LogsFlags, struct{}]([]string{"logs", "-n", "-10"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.SubCommandFlags.Lines != -10 {
			t.Errorf("Expected Lines to be -10, got %d", result.SubCommandFlags.Lines)
		}
	})

	t.Run("negative number with space-separated value", func(t *testing.T) {
		result, err := ParseWithCommand[GlobalFlags, LogsFlags, struct{}]([]string{"logs", "-n", "-10"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.SubCommandFlags.Lines != -10 {
			t.Errorf("Expected Lines to be -10, got %d", result.SubCommandFlags.Lines)
		}
	})

	t.Run("positive number with equals-separated value", func(t *testing.T) {
		result, err := ParseWithCommand[GlobalFlags, LogsFlags, struct{}]([]string{"logs", "-n=10"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.SubCommandFlags.Lines != 10 {
			t.Errorf("Expected Lines to be 10, got %d", result.SubCommandFlags.Lines)
		}
	})

	t.Run("boolean flag doesn't consume non-numeric string", func(t *testing.T) {
		result, err := ParseWithCommand[GlobalFlags, LogsFlags, struct{}]([]string{"-v", "foo"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !result.GlobalFlags.Verbose {
			t.Error("Expected Verbose to be true")
		}
		if result.SubCommand != "foo" {
			t.Errorf("Expected SubCommand to be 'foo', got %q", result.SubCommand)
		}
	})

	t.Run("boolean flag before subcommand with non-numeric name", func(t *testing.T) {
		result, err := ParseWithCommand[GlobalFlags, LogsFlags, struct{}]([]string{"-v", "logs", "service1"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !result.GlobalFlags.Verbose {
			t.Error("Expected Verbose to be true")
		}
		if result.SubCommand != "logs" {
			t.Errorf("Expected SubCommand to be 'logs', got %q", result.SubCommand)
		}
		if len(result.Parser.Args) != 1 || result.Parser.Args[0] != "service1" {
			t.Errorf("Expected Args to be ['service1'], got %v", result.Parser.Args)
		}
	})
}

func TestApplyAliases(t *testing.T) {
	config := HelpConfig{
		Command: CommandInfo{Name: "testapp"},
		SubCommands: map[string]SubCommandInfo{
			"copy":   {Name: "copy", Aliases: []string{"cp"}},
			"remove": {Name: "remove", Aliases: []string{"rm"}},
		},
		Groups: map[string]GroupInfo{
			"docker": {
				Name: "docker",
				Commands: map[string]SubCommandInfo{
					"pull": {Name: "pull", Aliases: []string{"pl"}},
				},
			},
		},
	}

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "flat alias", args: []string{"cp", "svc", "file"}, want: []string{"copy", "svc", "file"}},
		{name: "flat alias with flags", args: []string{"--verbose", "rm", "svc"}, want: []string{"--verbose", "remove", "svc"}},
		{name: "group alias", args: []string{"docker", "pl", "svc"}, want: []string{"docker", "pull", "svc"}},
		{name: "no alias", args: []string{"status"}, want: []string{"status"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyAliases(tt.args, config)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ApplyAliases = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestChaosMonkey_InvalidInputs tests error handling with invalid/malformed inputs
func TestChaosMonkey_InvalidInputs(t *testing.T) {
	type Flags struct {
		Port     int           `flag:"port"`
		Timeout  time.Duration `flag:"timeout"`
		Count    uint          `flag:"count"`
		Ratio    float64       `flag:"ratio"`
		Enabled  bool          `flag:"enabled"`
		URL      *url.URL      `flag:"url"`
		Endpoint url.URL       `flag:"endpoint"`
	}

	tests := []struct {
		name        string
		args        []string
		expectError bool
		errorMsg    string
	}{
		// Invalid integer values
		{
			name:        "invalid int - not a number",
			args:        []string{"--port=notanumber"},
			expectError: true,
			errorMsg:    "invalid int value",
		},
		{
			name:        "invalid int - float",
			args:        []string{"--port=8080.5"},
			expectError: true,
			errorMsg:    "invalid int value",
		},
		{
			name:        "invalid int - empty",
			args:        []string{"--port="},
			expectError: true,
			errorMsg:    "invalid int value",
		},
		{
			name:        "invalid int - overflow",
			args:        []string{"--port=99999999999999999999999999"},
			expectError: true,
			errorMsg:    "invalid int value",
		},

		// Invalid uint values
		{
			name:        "invalid uint - negative",
			args:        []string{"--count=-10"},
			expectError: true,
			errorMsg:    "invalid uint value",
		},
		{
			name:        "invalid uint - not a number",
			args:        []string{"--count=abc"},
			expectError: true,
			errorMsg:    "invalid uint value",
		},

		// Invalid float values
		{
			name:        "invalid float - not a number",
			args:        []string{"--ratio=notafloat"},
			expectError: true,
			errorMsg:    "invalid float value",
		},
		{
			name:        "invalid float - multiple dots",
			args:        []string{"--ratio=1.2.3"},
			expectError: true,
			errorMsg:    "invalid float value",
		},

		// Invalid duration values
		{
			name:        "invalid duration - no unit",
			args:        []string{"--timeout=123"},
			expectError: true,
			errorMsg:    "invalid duration",
		},
		{
			name:        "invalid duration - bad format",
			args:        []string{"--timeout=abc"},
			expectError: true,
			errorMsg:    "invalid duration",
		},
		{
			name:        "invalid duration - wrong unit",
			args:        []string{"--timeout=10x"},
			expectError: true,
			errorMsg:    "invalid duration",
		},

		// Invalid bool values
		{
			name:        "invalid bool - not true/false",
			args:        []string{"--enabled=notabool"},
			expectError: true,
			errorMsg:    "invalid bool value",
		},
		{
			name:        "invalid bool - number 2",
			args:        []string{"--enabled=2"},
			expectError: true,
			errorMsg:    "invalid bool value",
		},

		// Invalid URL values
		{
			name:        "invalid url - malformed",
			args:        []string{"--url=ht!tp://bad url"},
			expectError: true,
			errorMsg:    "invalid URL",
		},
		{
			name:        "invalid url struct - malformed",
			args:        []string{"--endpoint=://bad"},
			expectError: true,
			errorMsg:    "invalid URL",
		},

		// Valid cases that should NOT error
		{
			name:        "valid mixed flags",
			args:        []string{"--port=8080", "--timeout=30s", "--enabled"},
			expectError: false,
		},
		{
			name:        "empty args",
			args:        []string{},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseFlags[Flags](tt.args)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error containing %q, got nil", tt.errorMsg)
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error containing %q, got %q", tt.errorMsg, err.Error())
				}
				if result != nil {
					t.Error("Expected nil result on error")
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, got %v", err)
				}
				if result == nil {
					t.Error("Expected non-nil result")
				}
			}
		})
	}
}

// TestChaosMonkey_ParseWithCommandErrors tests error handling in ParseWithCommand
func TestChaosMonkey_ParseWithCommandErrors(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"v"`
		Port    int  `flag:"port"`
	}

	type SubFlags struct {
		Remove  bool   `flag:"rm"`
		Timeout string `flag:"timeout"`
	}

	tests := []struct {
		name        string
		args        []string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "invalid global flag type",
			args:        []string{"--port=notanumber", "run"},
			expectError: true,
			errorMsg:    "invalid int value",
		},
		{
			name:        "valid command",
			args:        []string{"run", "--rm"},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseWithCommand[GlobalFlags, SubFlags, struct{}](tt.args)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error containing %q, got nil", tt.errorMsg)
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error containing %q, got %q", tt.errorMsg, err.Error())
				}
				if result != nil {
					t.Error("Expected nil result on error")
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, got %v", err)
				}
				if result == nil {
					t.Error("Expected non-nil result")
				}
			}
		})
	}
}

// TestChaosMonkey_WeirdInputs tests handling of unusual but valid inputs
func TestChaosMonkey_WeirdInputs(t *testing.T) {
	type Flags struct {
		Name  string `flag:"name"`
		Value int    `flag:"value"`
	}

	tests := []struct {
		name string
		args []string
		want func(*ParseResult[Flags]) bool
	}{
		{
			name: "very long flag value",
			args: []string{"--name=" + strings.Repeat("x", 10000)},
			want: func(r *ParseResult[Flags]) bool {
				return r.Flags.Name == strings.Repeat("x", 10000)
			},
		},
		{
			name: "unicode in flag value",
			args: []string{"--name=日本語"},
			want: func(r *ParseResult[Flags]) bool {
				return r.Flags.Name == "日本語"
			},
		},
		{
			name: "empty flag value",
			args: []string{"--name="},
			want: func(r *ParseResult[Flags]) bool {
				return r.Flags.Name == ""
			},
		},
		{
			name: "flag with spaces in value",
			args: []string{"--name=hello world"},
			want: func(r *ParseResult[Flags]) bool {
				return r.Flags.Name == "hello world"
			},
		},
		{
			name: "flag with special chars in value",
			args: []string{"--name=!@#$%^&*()"},
			want: func(r *ParseResult[Flags]) bool {
				return r.Flags.Name == "!@#$%^&*()"
			},
		},
		{
			name: "many equals signs",
			args: []string{"--name=a=b=c=d=e"},
			want: func(r *ParseResult[Flags]) bool {
				return r.Flags.Name == "a=b=c=d=e"
			},
		},
		{
			name: "negative zero",
			args: []string{"--value=-0"},
			want: func(r *ParseResult[Flags]) bool {
				return r.Flags.Value == 0
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseFlags[Flags](tt.args)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if !tt.want(result) {
				t.Errorf("Validation failed for %q", tt.name)
			}
		})
	}
}

// TestInvalidFlagDetection tests that undefined flags are detected and return errors
func TestInvalidFlagDetection(t *testing.T) {
	type Flags struct {
		Verbose bool   `flag:"verbose" short:"v" help:"Verbose mode"`
		Output  string `flag:"output" short:"o" help:"Output file"`
	}

	tests := []struct {
		name      string
		args      []string
		wantError bool
		errorFlag string
	}{
		{
			name:      "valid flags only",
			args:      []string{"-v", "--output=test.txt"},
			wantError: false,
		},
		{
			name:      "invalid long flag",
			args:      []string{"--invalid", "arg"},
			wantError: true,
			errorFlag: "--invalid",
		},
		{
			name:      "invalid short flag",
			args:      []string{"-x", "arg"},
			wantError: true,
			errorFlag: "--x",
		},
		{
			name:      "invalid flag with equals",
			args:      []string{"--unknown=value"},
			wantError: true,
			errorFlag: "--unknown",
		},
		{
			name:      "valid flag then invalid flag",
			args:      []string{"-v", "--bad", "arg"},
			wantError: true,
			errorFlag: "--bad",
		},
		{
			name:      "invalid flag after -- is OK",
			args:      []string{"-v", "--", "--invalid", "--also-invalid"},
			wantError: false,
		},
		{
			name:      "invalid flag before -- errors",
			args:      []string{"--invalid", "--", "--also-invalid"},
			wantError: true,
			errorFlag: "--invalid",
		},
		{
			name:      "valid and invalid flags with -- in middle",
			args:      []string{"-v", "--", "--invalid"},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseFlags[Flags](tt.args)

			if tt.wantError {
				if err == nil {
					t.Error("Expected error for invalid flag, got nil")
				}
				var invalidFlagErr *InvalidFlagError
				if !errors.As(err, &invalidFlagErr) {
					t.Errorf("Expected InvalidFlagError, got %T: %v", err, err)
				}
				if invalidFlagErr != nil && invalidFlagErr.Flag != tt.errorFlag {
					t.Errorf("Expected error for flag %q, got %q", tt.errorFlag, invalidFlagErr.Flag)
				}
				if result != nil {
					t.Error("Expected nil result on error")
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, got %v", err)
				}
				if result == nil {
					t.Error("Expected non-nil result")
				}
			}
		})
	}
}

// TestInvalidFlagDetectionWithCommand tests invalid flag detection with subcommands
func TestInvalidFlagDetectionWithCommand(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"verbose" short:"v" help:"Verbose mode"`
	}

	type RunFlags struct {
		Detach bool `flag:"detach" short:"d" help:"Detach from terminal"`
	}

	tests := []struct {
		name      string
		args      []string
		wantError bool
		errorFlag string
	}{
		{
			name:      "valid global and subcommand flags",
			args:      []string{"-v", "run", "-d"},
			wantError: false,
		},
		{
			name:      "invalid global flag",
			args:      []string{"--invalid", "run"},
			wantError: true,
			errorFlag: "--invalid",
		},
		{
			name:      "invalid subcommand flag",
			args:      []string{"run", "--bad"},
			wantError: true,
			errorFlag: "--bad",
		},
		{
			name:      "invalid flag after subcommand",
			args:      []string{"run", "-d", "--unknown"},
			wantError: true,
			errorFlag: "--unknown",
		},
		{
			name:      "invalid flags after -- are OK",
			args:      []string{"-v", "run", "-d", "--", "--invalid", "--foo=bar"},
			wantError: false,
		},
		{
			name:      "invalid flag before -- with subcommand",
			args:      []string{"run", "--invalid", "--", "--also-invalid"},
			wantError: true,
			errorFlag: "--invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseWithCommand[GlobalFlags, RunFlags, struct{}](tt.args)

			if tt.wantError {
				if err == nil {
					t.Error("Expected error for invalid flag, got nil")
				}
				var invalidFlagErr *InvalidFlagError
				if !errors.As(err, &invalidFlagErr) {
					t.Errorf("Expected InvalidFlagError, got %T: %v", err, err)
				}
				if invalidFlagErr != nil && invalidFlagErr.Flag != tt.errorFlag {
					t.Errorf("Expected error for flag %q, got %q", tt.errorFlag, invalidFlagErr.Flag)
				}
				if result != nil {
					t.Error("Expected nil result on error")
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, got %v", err)
				}
				if result == nil {
					t.Error("Expected non-nil result")
				}
			}
		})
	}
}

// TestChaosMonkey_EmptyAndNil tests handling of empty/nil cases
func TestChaosMonkey_EmptyAndNil(t *testing.T) {
	type Flags struct {
		Name string `flag:"name"`
	}

	t.Run("nil args", func(t *testing.T) {
		result, err := ParseFlags[Flags](nil)
		if err != nil {
			t.Errorf("Should handle nil args gracefully, got error: %v", err)
		}
		if len(result.Args) != 0 {
			t.Errorf("Expected empty args, got %v", result.Args)
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		result, err := ParseFlags[Flags]([]string{})
		if err != nil {
			t.Errorf("Should handle empty slice gracefully, got error: %v", err)
		}
		if len(result.Args) != 0 {
			t.Errorf("Expected empty args, got %v", result.Args)
		}
	})

	t.Run("slice with empty strings", func(t *testing.T) {
		result, err := ParseFlags[Flags]([]string{"", "", ""})
		if err != nil {
			t.Errorf("Should handle empty strings gracefully, got error: %v", err)
		}
		// Empty strings should be treated as positional args
		if len(result.Args) != 3 {
			t.Errorf("Expected 3 args, got %d", len(result.Args))
		}
	})
}

// TestChaosMonkey_UnsupportedTypes tests error handling for unsupported field types
func TestChaosMonkey_UnsupportedTypes(t *testing.T) {
	t.Run("unsupported struct type", func(t *testing.T) {
		type CustomStruct struct {
			Field string
		}
		type Flags struct {
			Custom CustomStruct `flag:"custom"`
		}

		_, err := ParseFlags[Flags]([]string{"--custom=value"})
		if err == nil {
			t.Error("Expected error for unsupported struct type")
		}
		if !strings.Contains(err.Error(), "unsupported") {
			t.Errorf("Expected unsupported type error, got: %v", err)
		}
	})

	t.Run("pointer types are supported", func(t *testing.T) {
		type Flags struct {
			IntPtr    *int    `flag:"int-ptr"`
			BoolPtr   *bool   `flag:"bool-ptr"`
			StringPtr *string `flag:"string-ptr"`
		}

		// Test with values provided
		result, err := ParseFlags[Flags]([]string{"--int-ptr=123", "--bool-ptr", "--string-ptr=hello"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.Flags.IntPtr == nil {
			t.Error("Expected IntPtr to be set")
		} else if *result.Flags.IntPtr != 123 {
			t.Errorf("Expected IntPtr=123, got %d", *result.Flags.IntPtr)
		}
		if result.Flags.BoolPtr == nil {
			t.Error("Expected BoolPtr to be set")
		} else if *result.Flags.BoolPtr != true {
			t.Errorf("Expected BoolPtr=true, got %v", *result.Flags.BoolPtr)
		}
		if result.Flags.StringPtr == nil {
			t.Error("Expected StringPtr to be set")
		} else if *result.Flags.StringPtr != "hello" {
			t.Errorf("Expected StringPtr=hello, got %s", *result.Flags.StringPtr)
		}

		// Test with no values (should be nil)
		result2, err := ParseFlags[Flags]([]string{})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result2.Flags.IntPtr != nil {
			t.Error("Expected IntPtr to be nil when not provided")
		}
		if result2.Flags.BoolPtr != nil {
			t.Error("Expected BoolPtr to be nil when not provided")
		}
		if result2.Flags.StringPtr != nil {
			t.Error("Expected StringPtr to be nil when not provided")
		}
	})

	t.Run("double pointer types work", func(t *testing.T) {
		type Flags struct {
			DoubleIntPtr    **int    `flag:"double-int"`
			DoubleBoolPtr   **bool   `flag:"double-bool"`
			DoubleStringPtr **string `flag:"double-string"`
		}

		// Test with values provided
		result, err := ParseFlags[Flags]([]string{"--double-int=456", "--double-bool", "--double-string=test"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.Flags.DoubleIntPtr == nil {
			t.Error("Expected DoubleIntPtr to be set")
		} else if *result.Flags.DoubleIntPtr == nil {
			t.Error("Expected *DoubleIntPtr to be set")
		} else if **result.Flags.DoubleIntPtr != 456 {
			t.Errorf("Expected **DoubleIntPtr=456, got %d", **result.Flags.DoubleIntPtr)
		}
		if result.Flags.DoubleBoolPtr == nil {
			t.Error("Expected DoubleBoolPtr to be set")
		} else if *result.Flags.DoubleBoolPtr == nil {
			t.Error("Expected *DoubleBoolPtr to be set")
		} else if **result.Flags.DoubleBoolPtr != true {
			t.Errorf("Expected **DoubleBoolPtr=true, got %v", **result.Flags.DoubleBoolPtr)
		}
		if result.Flags.DoubleStringPtr == nil {
			t.Error("Expected DoubleStringPtr to be set")
		} else if *result.Flags.DoubleStringPtr == nil {
			t.Error("Expected *DoubleStringPtr to be set")
		} else if **result.Flags.DoubleStringPtr != "test" {
			t.Errorf("Expected **DoubleStringPtr=test, got %s", **result.Flags.DoubleStringPtr)
		}

		// Test with no value (should be nil)
		result2, err := ParseFlags[Flags]([]string{})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result2.Flags.DoubleIntPtr != nil {
			t.Error("Expected DoubleIntPtr to be nil when not provided")
		}
		if result2.Flags.DoubleBoolPtr != nil {
			t.Error("Expected DoubleBoolPtr to be nil when not provided")
		}
		if result2.Flags.DoubleStringPtr != nil {
			t.Error("Expected DoubleStringPtr to be nil when not provided")
		}

		// Test bool with explicit false
		result3, err := ParseFlags[Flags]([]string{"--double-bool=false"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result3.Flags.DoubleBoolPtr == nil || *result3.Flags.DoubleBoolPtr == nil {
			t.Error("Expected DoubleBoolPtr to be set")
		} else if **result3.Flags.DoubleBoolPtr != false {
			t.Errorf("Expected **DoubleBoolPtr=false, got %v", **result3.Flags.DoubleBoolPtr)
		}
	})

	t.Run("pointer types with space-separated values", func(t *testing.T) {
		type Flags struct {
			IntPtr    *int    `flag:"int-ptr"`
			StringPtr *string `flag:"string-ptr"`
		}

		result, err := ParseFlags[Flags]([]string{"--int-ptr", "789", "--string-ptr", "world"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.Flags.IntPtr == nil || *result.Flags.IntPtr != 789 {
			t.Error("Expected IntPtr=789 from space-separated value")
		}
		if result.Flags.StringPtr == nil || *result.Flags.StringPtr != "world" {
			t.Error("Expected StringPtr=world from space-separated value")
		}
	})

	t.Run("pointer types with invalid values", func(t *testing.T) {
		type Flags struct {
			IntPtr *int `flag:"int-ptr"`
		}

		_, err := ParseFlags[Flags]([]string{"--int-ptr=notanumber"})
		if err == nil {
			t.Error("Expected error for invalid int value")
		}
		if !strings.Contains(err.Error(), "invalid") {
			t.Errorf("Expected 'invalid' in error message, got: %v", err)
		}
	})

	t.Run("pointer bool with explicit false", func(t *testing.T) {
		type Flags struct {
			BoolPtr *bool `flag:"bool-ptr"`
		}

		result, err := ParseFlags[Flags]([]string{"--bool-ptr=false"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.Flags.BoolPtr == nil {
			t.Error("Expected BoolPtr to be set")
		} else if *result.Flags.BoolPtr != false {
			t.Errorf("Expected BoolPtr=false, got %v", *result.Flags.BoolPtr)
		}
	})

	t.Run("slice field type - string slice", func(t *testing.T) {
		type Flags struct {
			Items []string `flag:"items"`
		}

		result, err := ParseFlags[Flags]([]string{"--items=a,b,c"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		want := []string{"a", "b", "c"}
		if !reflect.DeepEqual(result.Flags.Items, want) {
			t.Errorf("Items = %v, want %v", result.Flags.Items, want)
		}
	})

	t.Run("unsupported field type - map", func(t *testing.T) {
		type Flags struct {
			Data map[string]string `flag:"data"`
		}

		_, err := ParseFlags[Flags]([]string{"--data=key=value"})
		if err == nil {
			t.Error("Expected error for unsupported map type")
		}
		if !strings.Contains(err.Error(), "unsupported") {
			t.Errorf("Expected unsupported type error, got: %v", err)
		}
	})
}

// TestArgsValidation_Required tests required positional arguments
func TestArgsValidation_Required(t *testing.T) {
	type GlobalFlags struct{}
	type SubFlags struct{}
	type Args struct {
		Service string `pos:"0" help:"Service name"`
	}

	t.Run("valid single required arg", func(t *testing.T) {
		result, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{"cmd", "myservice"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.Args.Service != "myservice" {
			t.Errorf("Expected Service to be 'myservice', got %q", result.Args.Service)
		}
	})

	t.Run("missing required arg", func(t *testing.T) {
		_, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{})
		if err == nil {
			t.Fatal("Expected error for missing required arg")
		}
		var argsErr *InvalidArgsError
		if !errors.As(err, &argsErr) {
			t.Errorf("Expected InvalidArgsError, got %T: %v", err, err)
		}
		if argsErr.Expected != "1" || argsErr.Got != 0 {
			t.Errorf("Expected 1 arg got 0, but error says: %v", argsErr)
		}
	})

	t.Run("too many args", func(t *testing.T) {
		_, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{"cmd", "service1", "service2"})
		if err == nil {
			t.Fatal("Expected error for too many args")
		}
		var argsErr *InvalidArgsError
		if !errors.As(err, &argsErr) {
			t.Errorf("Expected InvalidArgsError, got %T: %v", err, err)
		}
		if argsErr.Expected != "1" || argsErr.Got != 2 {
			t.Errorf("Expected 1 arg got 2, but error says: %v", argsErr)
		}
	})
}

// TestArgsValidation_Variadic tests variadic positional arguments
func TestArgsValidation_Variadic(t *testing.T) {
	type GlobalFlags struct{}
	type SubFlags struct{}

	t.Run("zero or more - accepts zero", func(t *testing.T) {
		type Args struct {
			Services []string `pos:"0*" help:"Service names"`
		}
		result, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if len(result.Args.Services) != 0 {
			t.Errorf("Expected empty Services, got %v", result.Args.Services)
		}
	})

	t.Run("zero or more - accepts multiple", func(t *testing.T) {
		type Args struct {
			Services []string `pos:"0*" help:"Service names"`
		}
		result, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{"cmd", "svc1", "svc2", "svc3"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		expected := []string{"svc1", "svc2", "svc3"}
		if !reflect.DeepEqual(result.Args.Services, expected) {
			t.Errorf("Expected %v, got %v", expected, result.Args.Services)
		}
	})

	t.Run("one or more - rejects zero", func(t *testing.T) {
		type Args struct {
			Services []string `pos:"0+" help:"Service names"`
		}
		_, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{})
		if err == nil {
			t.Fatal("Expected error for zero args when one or more required")
		}
		var argsErr *InvalidArgsError
		if !errors.As(err, &argsErr) {
			t.Errorf("Expected InvalidArgsError, got %T: %v", err, err)
		}
		if !strings.Contains(argsErr.Expected, "at least 1") {
			t.Errorf("Expected 'at least 1' in error, got: %v", argsErr)
		}
	})

	t.Run("one or more - accepts one", func(t *testing.T) {
		type Args struct {
			Services []string `pos:"0+" help:"Service names"`
		}
		result, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{"cmd", "svc1"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		expected := []string{"svc1"}
		if !reflect.DeepEqual(result.Args.Services, expected) {
			t.Errorf("Expected %v, got %v", expected, result.Args.Services)
		}
	})

	t.Run("one or more - accepts many", func(t *testing.T) {
		type Args struct {
			Services []string `pos:"0+" help:"Service names"`
		}
		result, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{"cmd", "a", "b", "c", "d", "e"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		expected := []string{"a", "b", "c", "d", "e"}
		if !reflect.DeepEqual(result.Args.Services, expected) {
			t.Errorf("Expected %v, got %v", expected, result.Args.Services)
		}
	})
}

// TestArgsValidation_Mixed tests mixing required and variadic args
func TestArgsValidation_Mixed(t *testing.T) {
	type GlobalFlags struct{}
	type SubFlags struct{}

	t.Run("required then variadic - valid", func(t *testing.T) {
		type Args struct {
			Command string   `pos:"0" help:"Command"`
			Args    []string `pos:"1*" help:"Additional args"`
		}
		result, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{"cmd", "deploy", "arg1", "arg2"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.Args.Command != "deploy" {
			t.Errorf("Expected Command 'deploy', got %q", result.Args.Command)
		}
		expected := []string{"arg1", "arg2"}
		if !reflect.DeepEqual(result.Args.Args, expected) {
			t.Errorf("Expected %v, got %v", expected, result.Args.Args)
		}
	})

	t.Run("required then variadic - missing required", func(t *testing.T) {
		type Args struct {
			Command string   `pos:"0" help:"Command"`
			Args    []string `pos:"1*" help:"Additional args"`
		}
		_, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{})
		if err == nil {
			t.Fatal("Expected error for missing required arg")
		}
		var argsErr *InvalidArgsError
		if !errors.As(err, &argsErr) {
			t.Errorf("Expected InvalidArgsError, got %T: %v", err, err)
		}
	})

	t.Run("required then variadic plus - valid with extras", func(t *testing.T) {
		type Args struct {
			Service string   `pos:"0" help:"Service name"`
			Files   []string `pos:"1+" help:"Files to deploy"`
		}
		result, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{"cmd", "myapp", "file1.txt", "file2.txt"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.Args.Service != "myapp" {
			t.Errorf("Expected Service 'myapp', got %q", result.Args.Service)
		}
		expected := []string{"file1.txt", "file2.txt"}
		if !reflect.DeepEqual(result.Args.Files, expected) {
			t.Errorf("Expected %v, got %v", expected, result.Args.Files)
		}
	})

	t.Run("required then variadic plus - missing variadic", func(t *testing.T) {
		type Args struct {
			Service string   `pos:"0" help:"Service name"`
			Files   []string `pos:"1+" help:"Files to deploy"`
		}
		_, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{"cmd", "myapp"})
		if err == nil {
			t.Fatal("Expected error for missing variadic+ args")
		}
		var argsErr *InvalidArgsError
		if !errors.As(err, &argsErr) {
			t.Errorf("Expected InvalidArgsError, got %T: %v", err, err)
		}
		if !strings.Contains(argsErr.Expected, "at least 2") {
			t.Errorf("Expected 'at least 2' in error, got: %v", argsErr)
		}
	})
}

// TestArgsValidation_WithSubcommand tests args with subcommands
func TestArgsValidation_WithSubcommand(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"verbose" short:"v"`
	}
	type SubFlags struct{}

	t.Run("subcommand with required arg", func(t *testing.T) {
		type Args struct {
			Service string `pos:"0" help:"Service name"`
		}
		result, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{"deploy", "myapp"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.SubCommand != "deploy" {
			t.Errorf("Expected SubCommand 'deploy', got %q", result.SubCommand)
		}
		if result.Args.Service != "myapp" {
			t.Errorf("Expected Service 'myapp', got %q", result.Args.Service)
		}
	})

	t.Run("subcommand with missing arg includes subcommand in error", func(t *testing.T) {
		type Args struct {
			Service string `pos:"0" help:"Service name"`
		}
		_, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{"deploy"})
		if err == nil {
			t.Fatal("Expected error for missing arg")
		}
		var argsErr *InvalidArgsError
		if !errors.As(err, &argsErr) {
			t.Errorf("Expected InvalidArgsError, got %T: %v", err, err)
		}
		if argsErr.SubCommand != "deploy" {
			t.Errorf("Expected SubCommand 'deploy' in error, got %q", argsErr.SubCommand)
		}
		errorMsg := argsErr.Error()
		if !strings.Contains(errorMsg, "'deploy'") {
			t.Errorf("Error message should mention subcommand 'deploy', got: %s", errorMsg)
		}
	})

	t.Run("flags and args together", func(t *testing.T) {
		type Args struct {
			Service string `pos:"0" help:"Service name"`
		}
		result, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{"-v", "deploy", "myapp"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if !result.GlobalFlags.Verbose {
			t.Error("Expected Verbose to be true")
		}
		if result.SubCommand != "deploy" {
			t.Errorf("Expected SubCommand 'deploy', got %q", result.SubCommand)
		}
		if result.Args.Service != "myapp" {
			t.Errorf("Expected Service 'myapp', got %q", result.Args.Service)
		}
	})
}

// TestArgsValidation_DoubleDash tests args with -- separator
func TestArgsValidation_DoubleDash(t *testing.T) {
	type GlobalFlags struct{}
	type SubFlags struct{}

	t.Run("args before -- are validated", func(t *testing.T) {
		type Args struct {
			Service string `pos:"0" help:"Service name"`
		}
		result, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{"cmd", "myapp", "--", "--invalid", "args"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if result.Args.Service != "myapp" {
			t.Errorf("Expected Service 'myapp', got %q", result.Args.Service)
		}
		// Args after -- should be in RemainingArgs
		expected := []string{"--invalid", "args"}
		if !reflect.DeepEqual(result.RemainingArgs, expected) {
			t.Errorf("Expected RemainingArgs %v, got %v", expected, result.RemainingArgs)
		}
	})

	t.Run("variadic collects only before --", func(t *testing.T) {
		type Args struct {
			Services []string `pos:"0+" help:"Service names"`
		}
		result, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{"cmd", "svc1", "svc2", "--", "after1", "after2"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		expected := []string{"svc1", "svc2"}
		if !reflect.DeepEqual(result.Args.Services, expected) {
			t.Errorf("Expected Services %v, got %v", expected, result.Args.Services)
		}
		expectedRemaining := []string{"after1", "after2"}
		if !reflect.DeepEqual(result.RemainingArgs, expectedRemaining) {
			t.Errorf("Expected RemainingArgs %v, got %v", expectedRemaining, result.RemainingArgs)
		}
	})
}

// TestArgsValidation_EmptyStruct tests empty args struct
func TestArgsValidation_EmptyStruct(t *testing.T) {
	type GlobalFlags struct{}
	type SubFlags struct{}
	type Args struct{}

	t.Run("empty args struct accepts no args", func(t *testing.T) {
		result, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if len(result.Parser.Args) != 0 {
			t.Errorf("Expected no args, got %v", result.Parser.Args)
		}
	})

	t.Run("empty args struct with positional args stores in Parser.Args", func(t *testing.T) {
		result, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{"cmd", "arg1", "arg2"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		// When no Args struct fields are defined, positional args are still in Parser.Args
		expected := []string{"arg1", "arg2"}
		if !reflect.DeepEqual(result.Parser.Args, expected) {
			t.Errorf("Expected Parser.Args %v, got %v", expected, result.Parser.Args)
		}
	})
}

// TestArgsValidation_ErrorMessages tests error message quality
func TestArgsValidation_ErrorMessages(t *testing.T) {
	type GlobalFlags struct{}
	type SubFlags struct{}

	t.Run("error message for exact count", func(t *testing.T) {
		type Args struct {
			File string `pos:"0" help:"File path"`
		}
		_, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{})
		if err == nil {
			t.Fatal("Expected error")
		}
		if !strings.Contains(err.Error(), "requires 1 argument") {
			t.Errorf("Error should mention 'requires 1 argument', got: %v", err)
		}
	})

	t.Run("error message for variadic minimum", func(t *testing.T) {
		type Args struct {
			Files []string `pos:"0+" help:"Files"`
		}
		_, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{})
		if err == nil {
			t.Fatal("Expected error")
		}
		if !strings.Contains(err.Error(), "at least 1") {
			t.Errorf("Error should mention 'at least 1', got: %v", err)
		}
	})

	t.Run("error message includes subcommand", func(t *testing.T) {
		type Args struct {
			File string `pos:"0" help:"File path"`
		}
		_, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{"deploy"})
		if err == nil {
			t.Fatal("Expected error")
		}
		var argsErr *InvalidArgsError
		if errors.As(err, &argsErr) {
			if argsErr.SubCommand != "deploy" {
				t.Errorf("Expected subcommand 'deploy', got %q", argsErr.SubCommand)
			}
			if !strings.Contains(argsErr.Error(), "'deploy'") {
				t.Errorf("Error should mention 'deploy', got: %v", argsErr.Error())
			}
		}
	})
}

// TestInvalidFlagError_SubCommand tests that InvalidFlagError includes subcommand context
func TestInvalidFlagError_SubCommand(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"verbose" short:"v"`
	}
	type SubFlags struct{}

	t.Run("invalid flag before subcommand has no subcommand", func(t *testing.T) {
		type Args struct{}
		_, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{"--invalid", "run"})
		if err == nil {
			t.Fatal("Expected error")
		}
		var flagErr *InvalidFlagError
		if errors.As(err, &flagErr) {
			if flagErr.SubCommand != "" {
				t.Errorf("Expected empty SubCommand, got %q", flagErr.SubCommand)
			}
		}
	})

	t.Run("invalid flag after subcommand includes subcommand", func(t *testing.T) {
		type Args struct{}
		_, err := ParseWithCommand[GlobalFlags, SubFlags, Args]([]string{"run", "--invalid"})
		if err == nil {
			t.Fatal("Expected error")
		}
		var flagErr *InvalidFlagError
		if errors.As(err, &flagErr) {
			if flagErr.SubCommand != "run" {
				t.Errorf("Expected SubCommand 'run', got %q", flagErr.SubCommand)
			}
		}
	})
}

// TestExtractGroupAndSubcommand tests the ExtractGroupAndSubcommand helper
func TestExtractGroupAndSubcommand(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		wantGroup      string
		wantSubcommand string
	}{
		{
			name:           "group and subcommand",
			args:           []string{"docker", "run"},
			wantGroup:      "docker",
			wantSubcommand: "run",
		},
		{
			name:           "group and subcommand with flags before",
			args:           []string{"-v", "docker", "run"},
			wantGroup:      "docker",
			wantSubcommand: "run",
		},
		{
			name:           "group and subcommand with flags between",
			args:           []string{"docker", "--verbose", "run"},
			wantGroup:      "docker",
			wantSubcommand: "run",
		},
		{
			name:           "only subcommand",
			args:           []string{"status"},
			wantGroup:      "",
			wantSubcommand: "status",
		},
		{
			name:           "only subcommand with flags",
			args:           []string{"-v", "status", "--format=json"},
			wantGroup:      "",
			wantSubcommand: "status",
		},
		{
			name:           "help command",
			args:           []string{"help", "docker"},
			wantGroup:      "",
			wantSubcommand: "docker",
		},
		{
			name:           "empty args",
			args:           []string{},
			wantGroup:      "",
			wantSubcommand: "",
		},
		{
			name:           "only flags",
			args:           []string{"-v", "--verbose"},
			wantGroup:      "",
			wantSubcommand: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotGroup, gotSubcommand := ExtractGroupAndSubcommand(tt.args)
			if gotGroup != tt.wantGroup {
				t.Errorf("ExtractGroupAndSubcommand() group = %q, want %q", gotGroup, tt.wantGroup)
			}
			if gotSubcommand != tt.wantSubcommand {
				t.Errorf("ExtractGroupAndSubcommand() subcommand = %q, want %q", gotSubcommand, tt.wantSubcommand)
			}
		})
	}
}

func TestResolveCommand(t *testing.T) {
	type RunArgs struct {
		Service string   `pos:"0"`
		Payload string   `pos:"1"`
		Extra   []string `pos:"2*"`
	}

	reg := Registry{
		Command: CommandInfo{Name: "testcli"},
		SubCommands: map[string]CommandSpec{
			"status": {Info: SubCommandInfo{Name: "status", Aliases: []string{"st"}}},
			"copy":   {Info: SubCommandInfo{Name: "copy", Aliases: []string{"cp"}}},
			"run":    {Info: SubCommandInfo{Name: "run"}, ArgsSchema: RunArgs{}},
		},
		Groups: map[string]GroupSpec{
			"docker": {
				Info: GroupInfo{Name: "docker"},
				Commands: map[string]CommandSpec{
					"pull": {Info: SubCommandInfo{Name: "pull", Aliases: []string{"pl"}}},
				},
			},
		},
	}

	t.Run("flat command", func(t *testing.T) {
		res, ok, err := ResolveCommandWithRegistry([]string{"status", "--json"}, reg)
		if err != nil {
			t.Fatalf("ResolveCommand error: %v", err)
		}
		if !ok {
			t.Fatalf("expected ok")
		}
		if got := strings.Join(res.Path, " "); got != "status" {
			t.Fatalf("unexpected path: %s", got)
		}
		if got := strings.Join(res.Args, " "); got != "--json" {
			t.Fatalf("unexpected args: %s", got)
		}
	})

	t.Run("flat command alias", func(t *testing.T) {
		res, ok, err := ResolveCommandWithRegistry([]string{"cp", "svc-a"}, reg)
		if err != nil {
			t.Fatalf("ResolveCommand error: %v", err)
		}
		if !ok {
			t.Fatalf("expected ok")
		}
		if got := strings.Join(res.Path, " "); got != "copy" {
			t.Fatalf("unexpected path: %s", got)
		}
		if got := strings.Join(res.Args, " "); got != "svc-a" {
			t.Fatalf("unexpected args: %s", got)
		}
	})

	t.Run("group command", func(t *testing.T) {
		res, ok, err := ResolveCommandWithRegistry([]string{"docker", "pull", "svc-a"}, reg)
		if err != nil {
			t.Fatalf("ResolveCommand error: %v", err)
		}
		if !ok {
			t.Fatalf("expected ok")
		}
		if got := strings.Join(res.Path, " "); got != "docker pull" {
			t.Fatalf("unexpected path: %s", got)
		}
		if got := strings.Join(res.Args, " "); got != "svc-a" {
			t.Fatalf("unexpected args: %s", got)
		}
	})

	t.Run("group command alias", func(t *testing.T) {
		res, ok, err := ResolveCommandWithRegistry([]string{"docker", "pl", "svc-a"}, reg)
		if err != nil {
			t.Fatalf("ResolveCommand error: %v", err)
		}
		if !ok {
			t.Fatalf("expected ok")
		}
		if got := strings.Join(res.Path, " "); got != "docker pull" {
			t.Fatalf("unexpected path: %s", got)
		}
		if got := strings.Join(res.Args, " "); got != "svc-a" {
			t.Fatalf("unexpected args: %s", got)
		}
	})

	t.Run("group only", func(t *testing.T) {
		_, ok, err := ResolveCommandWithRegistry([]string{"docker"}, reg)
		if err != nil {
			t.Fatalf("ResolveCommand error: %v", err)
		}
		if ok {
			t.Fatalf("expected ok=false for group without subcommand")
		}
	})

	t.Run("unknown command", func(t *testing.T) {
		_, ok, err := ResolveCommandWithRegistry([]string{"nope"}, reg)
		if err == nil {
			t.Fatalf("expected error for unknown command")
		}
		if ok {
			t.Fatalf("expected ok=false for unknown command")
		}
	})

	t.Run("help flag", func(t *testing.T) {
		_, ok, err := ResolveCommandWithRegistry([]string{"--help"}, reg)
		if err != nil {
			t.Fatalf("ResolveCommand error: %v", err)
		}
		if ok {
			t.Fatalf("expected ok=false for help")
		}
	})

	t.Run("arg spec", func(t *testing.T) {
		res, ok, err := ResolveCommandWithRegistry([]string{"run", "svc", "payload"}, reg)
		if err != nil {
			t.Fatalf("ResolveCommand error: %v", err)
		}
		if !ok {
			t.Fatalf("expected ok")
		}
		arg, ok := res.PArg(0)
		if !ok {
			t.Fatalf("expected arg 0")
		}
		if arg.Name != "Service" {
			t.Fatalf("expected arg name Service, got %q", arg.Name)
		}
		if arg.GoType == nil {
			t.Fatalf("expected GoType to be set")
		}
		if arg.GoType != reflect.TypeOf("") {
			t.Fatalf("expected GoType to be string, got %v", arg.GoType)
		}
		if !arg.Required {
			t.Fatalf("expected arg to be required")
		}
	})
}

// TestRunSubcommandsWithGroups tests command group routing
func TestRunSubcommandsWithGroups(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"verbose" short:"v"`
	}

	config := HelpConfig{
		Command: CommandInfo{
			Name:        "testcli",
			Description: "Test CLI",
		},
		SubCommands: map[string]SubCommandInfo{
			"status": {
				Name:        "status",
				Description: "Show status",
				Aliases:     []string{"st"},
			},
			"whoami": {
				Name:        "whoami",
				Description: "Show user info",
			},
		},
		Groups: map[string]GroupInfo{
			"docker": {
				Name:        "docker",
				Description: "Docker-related commands",
				Commands: map[string]SubCommandInfo{
					"run": {
						Name:        "run",
						Description: "Run a container",
						Aliases:     []string{"r"},
					},
					"ps": {
						Name:        "ps",
						Description: "List containers",
					},
					"stop": {
						Name:        "stop",
						Description: "Stop containers",
					},
				},
			},
		},
	}

	// Track which handlers were called
	type handlerCall struct {
		name string
		args []string
	}
	var calls []handlerCall

	// Create flat command handlers
	commands := map[string]SubcommandHandler{
		"status": func(ctx context.Context, args []string) error {
			calls = append(calls, handlerCall{"status", args})
			return nil
		},
		"whoami": func(ctx context.Context, args []string) error {
			calls = append(calls, handlerCall{"whoami", args})
			return nil
		},
	}

	// Create grouped command handlers
	groups := map[string]Group{
		"docker": {
			Description: "Docker-related commands",
			Commands: map[string]SubcommandHandler{
				"run": func(ctx context.Context, args []string) error {
					calls = append(calls, handlerCall{"docker.run", args})
					return nil
				},
				"ps": func(ctx context.Context, args []string) error {
					calls = append(calls, handlerCall{"docker.ps", args})
					return nil
				},
				"stop": func(ctx context.Context, args []string) error {
					calls = append(calls, handlerCall{"docker.stop", args})
					return nil
				},
			},
		},
	}

	tests := []struct {
		name        string
		args        []string
		wantHandler string
		wantErr     bool
		wantHelp    bool
	}{
		{
			name:        "flat command - status",
			args:        []string{"status"},
			wantHandler: "status",
		},
		{
			name:        "flat command alias - st",
			args:        []string{"st"},
			wantHandler: "status",
		},
		{
			name:        "flat command - whoami",
			args:        []string{"whoami"},
			wantHandler: "whoami",
		},
		{
			name:        "grouped command - docker run",
			args:        []string{"docker", "run", "nginx"},
			wantHandler: "docker.run",
		},
		{
			name:        "grouped command alias - docker r",
			args:        []string{"docker", "r", "nginx"},
			wantHandler: "docker.run",
		},
		{
			name:        "grouped command - docker ps",
			args:        []string{"docker", "ps"},
			wantHandler: "docker.ps",
		},
		{
			name:        "grouped command - docker stop",
			args:        []string{"docker", "stop", "container1"},
			wantHandler: "docker.stop",
		},
		{
			name:        "grouped command with global flags",
			args:        []string{"-v", "docker", "run"},
			wantHandler: "docker.run",
		},
		{
			name:    "unknown flat command",
			args:    []string{"unknown"},
			wantErr: true,
		},
		{
			name:    "unknown group",
			args:    []string{"badgroup", "run"},
			wantErr: true,
		},
		{
			name:    "unknown command in group",
			args:    []string{"docker", "unknown"},
			wantErr: true,
		},
		{
			name:     "group without subcommand shows help",
			args:     []string{"docker"},
			wantHelp: true,
		},
		{
			name:     "empty args shows help",
			args:     []string{},
			wantHelp: true,
		},
		{
			name:     "global help",
			args:     []string{"--help"},
			wantHelp: true,
		},
		{
			name:     "group help with --help",
			args:     []string{"docker", "--help"},
			wantHelp: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset calls
			calls = nil

			err := RunSubcommandsWithGroups(context.Background(), tt.args, config, GlobalFlags{}, commands, groups)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
				}
				return
			}

			if tt.wantHelp {
				// Help should not call a handler
				if len(calls) > 0 {
					t.Errorf("Expected no handler calls for help, but got: %v", calls)
				}
				if err != nil {
					t.Errorf("Help should not return error, got: %v", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if len(calls) != 1 {
				t.Fatalf("Expected 1 handler call, got %d: %v", len(calls), calls)
			}

			if calls[0].name != tt.wantHandler {
				t.Errorf("Handler = %q, want %q", calls[0].name, tt.wantHandler)
			}
		})
	}
}

// TestGenerateGroupHelp tests the group help generation
func TestGenerateGroupHelp(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"verbose" short:"v" help:"Enable verbose logging"`
	}

	config := HelpConfig{
		Command: CommandInfo{
			Name:        "testcli",
			Description: "Test CLI",
		},
		Groups: map[string]GroupInfo{
			"docker": {
				Name:        "docker",
				Description: "Docker-related commands",
				Commands: map[string]SubCommandInfo{
					"run": {
						Name:        "run",
						Description: "Run a container",
					},
					"ps": {
						Name:        "ps",
						Description: "List containers",
					},
				},
			},
		},
	}

	help := GenerateGroupHelp(config, "docker", GlobalFlags{})

	// Check that help contains expected elements
	expectedStrings := []string{
		"Docker-related commands",
		"USAGE:",
		"testcli [GLOBAL OPTIONS] docker COMMAND",
		"COMMANDS:",
		"ps",
		"List containers",
		"run",
		"Run a container",
		"GLOBAL OPTIONS:",
		"-v, --verbose",
		"Enable verbose logging",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(help, expected) {
			t.Errorf("Help should contain %q, but got:\n%s", expected, help)
		}
	}
}

// TestGenerateGlobalHelpWithGroups tests that global help shows command groups
func TestGenerateGlobalHelpWithGroups(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"verbose" short:"v" help:"Enable verbose logging"`
	}

	config := HelpConfig{
		Command: CommandInfo{
			Name:        "testcli",
			Description: "Test CLI",
		},
		SubCommands: map[string]SubCommandInfo{
			"status": {
				Name:        "status",
				Description: "Show status",
			},
		},
		Groups: map[string]GroupInfo{
			"docker": {
				Name:        "docker",
				Description: "Docker-related commands",
			},
		},
	}

	help := GenerateGlobalHelp(config, GlobalFlags{})

	// Check that help contains expected elements
	expectedStrings := []string{
		"COMMANDS:",
		"status",
		"Show status",
		"COMMAND GROUPS:",
		"docker",
		"Docker-related commands",
		"Run 'testcli <group>' to see commands in a group",
	}

	for _, expected := range expectedStrings {
		if !strings.Contains(help, expected) {
			t.Errorf("Help should contain %q, but got:\n%s", expected, help)
		}
	}
}

func TestPortType_Parsing(t *testing.T) {
	type Flags struct {
		HTTPPort  Port  `flag:"port"`
		AdminPort *Port `flag:"admin-port"`
	}

	tests := []struct {
		name          string
		args          []string
		wantHTTPPort  Port
		wantAdminPort *Port
		wantErr       bool
	}{
		{
			name:         "valid port",
			args:         []string{"--port=8080"},
			wantHTTPPort: 8080,
		},
		{
			name:          "valid pointer port",
			args:          []string{"--admin-port=9000"},
			wantAdminPort: ptrToPort(Port(9000)),
		},
		{
			name:          "both ports",
			args:          []string{"--port=80", "--admin-port=443"},
			wantHTTPPort:  80,
			wantAdminPort: ptrToPort(Port(443)),
		},
		{
			name:         "port 0",
			args:         []string{"--port=0"},
			wantHTTPPort: 0,
		},
		{
			name:         "max port",
			args:         []string{"--port=65535"},
			wantHTTPPort: 65535,
		},
		{
			name:    "port too large",
			args:    []string{"--port=65536"},
			wantErr: true,
		},
		{
			name:    "negative port",
			args:    []string{"--port=-1"},
			wantErr: true,
		},
		{
			name:    "invalid port",
			args:    []string{"--port=abc"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseFlags[Flags](tt.args)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseFlags() expected error but got none")
				}
				return
			}
			if err != nil {
				t.Errorf("ParseFlags() unexpected error: %v", err)
				return
			}

			if result.Flags.HTTPPort != tt.wantHTTPPort {
				t.Errorf("HTTPPort = %v, want %v", result.Flags.HTTPPort, tt.wantHTTPPort)
			}

			if tt.wantAdminPort != nil {
				if result.Flags.AdminPort == nil {
					t.Errorf("AdminPort = nil, want %v", *tt.wantAdminPort)
				} else if *result.Flags.AdminPort != *tt.wantAdminPort {
					t.Errorf("AdminPort = %v, want %v", *result.Flags.AdminPort, *tt.wantAdminPort)
				}
			} else if result.Flags.AdminPort != nil {
				t.Errorf("AdminPort = %v, want nil", *result.Flags.AdminPort)
			}
		})
	}
}

func TestPortType_RangeValidation(t *testing.T) {
	type Flags struct {
		HTTPPort  Port  `flag:"port" port:"1-65535"`
		AdminPort *Port `flag:"admin-port" port:"8000-9000"`
	}

	tests := []struct {
		name    string
		args    []string
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid http port in range",
			args: []string{"--port=8080"},
		},
		{
			name: "valid admin port in range",
			args: []string{"--admin-port=8500"},
		},
		{
			name:    "http port 0 out of range",
			args:    []string{"--port=0"},
			wantErr: true,
			errMsg:  "port must be between 1-65535, got 0",
		},
		{
			name:    "admin port below range",
			args:    []string{"--admin-port=7999"},
			wantErr: true,
			errMsg:  "port must be between 8000-9000, got 7999",
		},
		{
			name:    "admin port above range",
			args:    []string{"--admin-port=9001"},
			wantErr: true,
			errMsg:  "port must be between 8000-9000, got 9001",
		},
		{
			name: "http port at lower bound",
			args: []string{"--port=1"},
		},
		{
			name: "http port at upper bound",
			args: []string{"--port=65535"},
		},
		{
			name: "admin port at lower bound",
			args: []string{"--admin-port=8000"},
		},
		{
			name: "admin port at upper bound",
			args: []string{"--admin-port=9000"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseFlags[Flags](tt.args)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseFlags() expected error but got none")
					return
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("ParseFlags() error = %v, want error containing %q", err, tt.errMsg)
				}
				return
			}
			if err != nil {
				t.Errorf("ParseFlags() unexpected error: %v", err)
			}
		})
	}
}

func TestPortType_ErrorMessages(t *testing.T) {
	type Flags struct {
		Port Port `flag:"port" port:"1-65535"`
	}

	tests := []struct {
		name       string
		args       []string
		wantErrMsg string
	}{
		{
			name:       "port too large",
			args:       []string{"--port=65536"},
			wantErrMsg: "port must be between 1-65535",
		},
		{
			name:       "port way too large",
			args:       []string{"--port=999999"},
			wantErrMsg: "port must be between 1-65535",
		},
		{
			name:       "invalid characters",
			args:       []string{"--port=abc"},
			wantErrMsg: "invalid port value",
		},
		{
			name:       "port with spaces",
			args:       []string{"--port=80 80"},
			wantErrMsg: "invalid port value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseFlags[Flags](tt.args)
			if err == nil {
				t.Errorf("ParseFlags() expected error but got none")
				return
			}
			if !strings.Contains(err.Error(), tt.wantErrMsg) {
				t.Errorf("ParseFlags() error = %v, want error containing %q", err, tt.wantErrMsg)
			}
		})
	}
}

func TestPortType_WithCommand(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"verbose"`
	}

	type RunFlags struct {
		Port *Port `flag:"port" port:"1-65535"`
	}

	type Args struct{}

	tests := []struct {
		name     string
		args     []string
		wantPort *Port
		wantErr  bool
	}{
		{
			name:     "valid port with command",
			args:     []string{"run", "--port=8080"},
			wantPort: ptrToPort(Port(8080)),
		},
		{
			name:    "invalid port with command",
			args:    []string{"run", "--port=0"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseWithCommand[GlobalFlags, RunFlags, Args](tt.args)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseWithCommand() expected error but got none")
				}
				return
			}
			if err != nil {
				t.Errorf("ParseWithCommand() unexpected error: %v", err)
				return
			}

			if tt.wantPort != nil {
				if result.SubCommandFlags.Port == nil {
					t.Errorf("Port = nil, want %v", *tt.wantPort)
				} else if *result.SubCommandFlags.Port != *tt.wantPort {
					t.Errorf("Port = %v, want %v", *result.SubCommandFlags.Port, *tt.wantPort)
				}
			}
		})
	}
}

func TestParsePortRange(t *testing.T) {
	tests := []struct {
		name     string
		rangeStr string
		wantMin  uint16
		wantMax  uint16
		wantErr  bool
	}{
		{
			name:     "valid range",
			rangeStr: "1-65535",
			wantMin:  1,
			wantMax:  65535,
		},
		{
			name:     "custom range",
			rangeStr: "8000-9000",
			wantMin:  8000,
			wantMax:  9000,
		},
		{
			name:     "single port range",
			rangeStr: "80-80",
			wantMin:  80,
			wantMax:  80,
		},
		{
			name:     "empty string",
			rangeStr: "",
			wantMin:  0,
			wantMax:  0,
		},
		{
			name:     "invalid format",
			rangeStr: "1-2-3",
			wantErr:  true,
		},
		{
			name:     "invalid min",
			rangeStr: "abc-1000",
			wantErr:  true,
		},
		{
			name:     "invalid max",
			rangeStr: "1000-abc",
			wantErr:  true,
		},
		{
			name:     "min greater than max",
			rangeStr: "9000-8000",
			wantErr:  true,
		},
		{
			name:     "value too large",
			rangeStr: "1-99999",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			min, max, err := parsePortRange(tt.rangeStr)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parsePortRange() expected error but got none")
				}
				return
			}
			if err != nil {
				t.Errorf("parsePortRange() unexpected error: %v", err)
				return
			}

			if min != tt.wantMin {
				t.Errorf("min = %v, want %v", min, tt.wantMin)
			}
			if max != tt.wantMax {
				t.Errorf("max = %v, want %v", max, tt.wantMax)
			}
		})
	}
}

func TestParsePortValue(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		wantPort Port
		wantErr  bool
		errMsg   string
	}{
		{
			name:     "valid port",
			value:    "8080",
			wantPort: 8080,
		},
		{
			name:     "port 0",
			value:    "0",
			wantPort: 0,
		},
		{
			name:     "max port",
			value:    "65535",
			wantPort: 65535,
		},
		{
			name:    "port too large",
			value:   "65536",
			wantErr: true,
			errMsg:  "port must be between 0 and 65535",
		},
		{
			name:    "invalid characters",
			value:   "abc",
			wantErr: true,
			errMsg:  "invalid port value",
		},
		{
			name:    "negative",
			value:   "-1",
			wantErr: true,
			errMsg:  "invalid port value",
		},
		{
			name:    "empty string",
			value:   "",
			wantErr: true,
			errMsg:  "invalid port value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port, err := parsePortValue(tt.value)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parsePortValue() expected error but got none")
					return
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("parsePortValue() error = %v, want error containing %q", err, tt.errMsg)
				}
				return
			}
			if err != nil {
				t.Errorf("parsePortValue() unexpected error: %v", err)
				return
			}

			if port != tt.wantPort {
				t.Errorf("port = %v, want %v", port, tt.wantPort)
			}
		})
	}
}

// ptrToPort is a helper function to create pointer to Port
func ptrToPort(p Port) *Port {
	return &p
}

// TestHiddenSubcommands tests that hidden subcommands are excluded from help but still work
func TestHiddenSubcommands(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"verbose" short:"v" help:"Verbose mode"`
	}

	config := HelpConfig{
		Command: CommandInfo{
			Name:        "testapp",
			Description: "A test application",
		},
		SubCommands: map[string]SubCommandInfo{
			"run": {
				Name:        "run",
				Description: "Run a service",
				Hidden:      false,
			},
			"debug": {
				Name:        "debug",
				Description: "Debug commands",
				Hidden:      true,
			},
			"status": {
				Name:        "status",
				Description: "Check status",
				Hidden:      false,
			},
		},
	}

	t.Run("hidden subcommand excluded from help", func(t *testing.T) {
		helpText := GenerateGlobalHelp(config, GlobalFlags{})

		// Visible commands should appear
		if !strings.Contains(helpText, "run") {
			t.Error("Help should contain visible 'run' command")
		}
		if !strings.Contains(helpText, "status") {
			t.Error("Help should contain visible 'status' command")
		}

		// Hidden command should NOT appear
		if strings.Contains(helpText, "debug") {
			t.Error("Help should NOT contain hidden 'debug' command")
		}
	})

	t.Run("all visible commands appear when no hidden commands exist", func(t *testing.T) {
		allVisibleConfig := HelpConfig{
			Command: CommandInfo{
				Name:        "testapp",
				Description: "A test application",
			},
			SubCommands: map[string]SubCommandInfo{
				"run": {
					Name:        "run",
					Description: "Run a service",
					Hidden:      false,
				},
				"status": {
					Name:        "status",
					Description: "Check status",
					Hidden:      false,
				},
			},
		}

		helpText := GenerateGlobalHelp(allVisibleConfig, GlobalFlags{})

		if !strings.Contains(helpText, "run") {
			t.Error("Help should contain 'run' command")
		}
		if !strings.Contains(helpText, "status") {
			t.Error("Help should contain 'status' command")
		}
		if !strings.Contains(helpText, "COMMANDS:") {
			t.Error("Help should contain COMMANDS section")
		}
	})

	t.Run("all hidden commands means no COMMANDS section", func(t *testing.T) {
		allHiddenConfig := HelpConfig{
			Command: CommandInfo{
				Name:        "testapp",
				Description: "A test application",
			},
			SubCommands: map[string]SubCommandInfo{
				"debug": {
					Name:        "debug",
					Description: "Debug commands",
					Hidden:      true,
				},
				"internal": {
					Name:        "internal",
					Description: "Internal commands",
					Hidden:      true,
				},
			},
		}

		helpText := GenerateGlobalHelp(allHiddenConfig, GlobalFlags{})

		// Should not show COMMANDS section if all commands are hidden
		if strings.Contains(helpText, "COMMANDS:") {
			t.Error("Help should NOT contain COMMANDS section when all commands are hidden")
		}
		if strings.Contains(helpText, "debug") {
			t.Error("Help should NOT contain hidden 'debug' command")
		}
	})
}

// TestHiddenGroups tests that hidden groups are excluded from help but still work
func TestHiddenGroups(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"verbose" short:"v" help:"Verbose mode"`
	}

	config := HelpConfig{
		Command: CommandInfo{
			Name:        "testapp",
			Description: "A test application",
		},
		Groups: map[string]GroupInfo{
			"docker": {
				Name:        "docker",
				Description: "Docker container management",
				Hidden:      false,
				Commands: map[string]SubCommandInfo{
					"run": {Name: "run", Description: "Run a container"},
					"ps":  {Name: "ps", Description: "List containers"},
				},
			},
			"debug": {
				Name:        "debug",
				Description: "Debug and diagnostic commands",
				Hidden:      true,
				Commands: map[string]SubCommandInfo{
					"whoami": {Name: "whoami", Description: "Show user info"},
					"ip":     {Name: "ip", Description: "Show IP addresses"},
				},
			},
			"admin": {
				Name:        "admin",
				Description: "Admin commands",
				Hidden:      false,
				Commands: map[string]SubCommandInfo{
					"users": {Name: "users", Description: "Manage users"},
				},
			},
		},
	}

	t.Run("hidden group excluded from global help", func(t *testing.T) {
		helpText := GenerateGlobalHelp(config, GlobalFlags{})

		// Visible groups should appear
		if !strings.Contains(helpText, "docker") {
			t.Error("Help should contain visible 'docker' group")
		}
		if !strings.Contains(helpText, "admin") {
			t.Error("Help should contain visible 'admin' group")
		}

		// Hidden group should NOT appear
		if strings.Contains(helpText, "debug") {
			t.Error("Help should NOT contain hidden 'debug' group")
		}
		if strings.Contains(helpText, "Debug and diagnostic commands") {
			t.Error("Help should NOT contain hidden group description")
		}
	})

	t.Run("all visible groups appear when no hidden groups exist", func(t *testing.T) {
		allVisibleConfig := HelpConfig{
			Command: CommandInfo{
				Name:        "testapp",
				Description: "A test application",
			},
			Groups: map[string]GroupInfo{
				"docker": {
					Name:        "docker",
					Description: "Docker container management",
					Hidden:      false,
					Commands: map[string]SubCommandInfo{
						"run": {Name: "run", Description: "Run a container"},
					},
				},
			},
		}

		helpText := GenerateGlobalHelp(allVisibleConfig, GlobalFlags{})

		if !strings.Contains(helpText, "docker") {
			t.Error("Help should contain 'docker' group")
		}
		if !strings.Contains(helpText, "COMMAND GROUPS:") {
			t.Error("Help should contain COMMAND GROUPS section")
		}
	})

	t.Run("all hidden groups means no COMMAND GROUPS section", func(t *testing.T) {
		allHiddenConfig := HelpConfig{
			Command: CommandInfo{
				Name:        "testapp",
				Description: "A test application",
			},
			Groups: map[string]GroupInfo{
				"debug": {
					Name:        "debug",
					Description: "Debug commands",
					Hidden:      true,
					Commands:    map[string]SubCommandInfo{},
				},
				"internal": {
					Name:        "internal",
					Description: "Internal commands",
					Hidden:      true,
					Commands:    map[string]SubCommandInfo{},
				},
			},
		}

		helpText := GenerateGlobalHelp(allHiddenConfig, GlobalFlags{})

		// Should not show COMMAND GROUPS section if all groups are hidden
		if strings.Contains(helpText, "COMMAND GROUPS:") {
			t.Error("Help should NOT contain COMMAND GROUPS section when all groups are hidden")
		}
		if strings.Contains(helpText, "debug") {
			t.Error("Help should NOT contain hidden 'debug' group")
		}
	})
}

// TestHiddenCommandsInGroups tests that hidden commands within groups are excluded from group help
func TestHiddenCommandsInGroups(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"verbose" short:"v" help:"Verbose mode"`
	}

	config := HelpConfig{
		Command: CommandInfo{
			Name:        "testapp",
			Description: "A test application",
		},
		Groups: map[string]GroupInfo{
			"docker": {
				Name:        "docker",
				Description: "Docker container management",
				Hidden:      false,
				Commands: map[string]SubCommandInfo{
					"run": {
						Name:        "run",
						Description: "Run a container",
						Hidden:      false,
					},
					"ps": {
						Name:        "ps",
						Description: "List containers",
						Hidden:      false,
					},
					"internal-cmd": {
						Name:        "internal-cmd",
						Description: "Internal command",
						Hidden:      true,
					},
				},
			},
		},
	}

	t.Run("hidden commands in group excluded from group help", func(t *testing.T) {
		helpText := GenerateGroupHelp(config, "docker", GlobalFlags{})

		// Visible commands should appear
		if !strings.Contains(helpText, "run") {
			t.Error("Group help should contain visible 'run' command")
		}
		if !strings.Contains(helpText, "ps") {
			t.Error("Group help should contain visible 'ps' command")
		}

		// Hidden command should NOT appear
		if strings.Contains(helpText, "internal-cmd") {
			t.Error("Group help should NOT contain hidden 'internal-cmd' command")
		}
	})

	t.Run("all commands visible in group", func(t *testing.T) {
		allVisibleConfig := HelpConfig{
			Command: CommandInfo{
				Name:        "testapp",
				Description: "A test application",
			},
			Groups: map[string]GroupInfo{
				"docker": {
					Name:        "docker",
					Description: "Docker container management",
					Commands: map[string]SubCommandInfo{
						"run": {
							Name:        "run",
							Description: "Run a container",
							Hidden:      false,
						},
						"ps": {
							Name:        "ps",
							Description: "List containers",
							Hidden:      false,
						},
					},
				},
			},
		}

		helpText := GenerateGroupHelp(allVisibleConfig, "docker", GlobalFlags{})

		if !strings.Contains(helpText, "run") {
			t.Error("Group help should contain 'run' command")
		}
		if !strings.Contains(helpText, "ps") {
			t.Error("Group help should contain 'ps' command")
		}
		if !strings.Contains(helpText, "COMMANDS:") {
			t.Error("Group help should contain COMMANDS section")
		}
	})

	t.Run("all commands hidden in group means no COMMANDS section", func(t *testing.T) {
		allHiddenConfig := HelpConfig{
			Command: CommandInfo{
				Name:        "testapp",
				Description: "A test application",
			},
			Groups: map[string]GroupInfo{
				"docker": {
					Name:        "docker",
					Description: "Docker container management",
					Commands: map[string]SubCommandInfo{
						"internal1": {
							Name:        "internal1",
							Description: "Internal command 1",
							Hidden:      true,
						},
						"internal2": {
							Name:        "internal2",
							Description: "Internal command 2",
							Hidden:      true,
						},
					},
				},
			},
		}

		helpText := GenerateGroupHelp(allHiddenConfig, "docker", GlobalFlags{})

		// Should not show COMMANDS section if all commands in group are hidden
		if strings.Contains(helpText, "COMMANDS:") {
			t.Error("Group help should NOT contain COMMANDS section when all commands are hidden")
		}
		if strings.Contains(helpText, "internal1") {
			t.Error("Group help should NOT contain hidden 'internal1' command")
		}
	})
}

// TestMixedHiddenAndVisible tests combinations of hidden and visible commands/groups
func TestMixedHiddenAndVisible(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"verbose" short:"v" help:"Verbose mode"`
	}

	config := HelpConfig{
		Command: CommandInfo{
			Name:        "testapp",
			Description: "A test application",
		},
		SubCommands: map[string]SubCommandInfo{
			"run": {
				Name:        "run",
				Description: "Run a service",
				Hidden:      false,
			},
			"whoami": {
				Name:        "whoami",
				Description: "Show user info",
				Hidden:      true,
			},
		},
		Groups: map[string]GroupInfo{
			"docker": {
				Name:        "docker",
				Description: "Docker commands",
				Hidden:      false,
				Commands: map[string]SubCommandInfo{
					"run": {Name: "run", Description: "Run container", Hidden: false},
				},
			},
			"debug": {
				Name:        "debug",
				Description: "Debug commands",
				Hidden:      true,
				Commands: map[string]SubCommandInfo{
					"ip": {Name: "ip", Description: "Show IP", Hidden: false},
				},
			},
		},
	}

	t.Run("mixed hidden and visible in global help", func(t *testing.T) {
		helpText := GenerateGlobalHelp(config, GlobalFlags{})

		// Visible items should appear
		if !strings.Contains(helpText, "run") {
			t.Error("Help should contain visible 'run' command")
		}
		if !strings.Contains(helpText, "docker") {
			t.Error("Help should contain visible 'docker' group")
		}

		// Hidden items should NOT appear
		if strings.Contains(helpText, "whoami") {
			t.Error("Help should NOT contain hidden 'whoami' command")
		}
		if strings.Contains(helpText, "debug") {
			t.Error("Help should NOT contain hidden 'debug' group")
		}

		// Both sections should appear
		if !strings.Contains(helpText, "COMMANDS:") {
			t.Error("Help should contain COMMANDS section")
		}
		if !strings.Contains(helpText, "COMMAND GROUPS:") {
			t.Error("Help should contain COMMAND GROUPS section")
		}
	})
}

// TestFlagValueError tests the FlagValueError error type
func TestFlagValueError(t *testing.T) {
	t.Run("error message is user-friendly", func(t *testing.T) {
		type Flags struct {
			Port Port `flag:"port" port:"1-65535"`
		}

		_, err := ParseFlags[Flags]([]string{"--port=99999"})
		if err == nil {
			t.Fatal("Expected error")
		}

		var flagErr *FlagValueError
		if !errors.As(err, &flagErr) {
			t.Fatalf("Expected FlagValueError, got %T: %v", err, err)
		}

		// The error message should be clean and user-friendly
		errMsg := err.Error()
		if strings.Contains(errMsg, "failed to parse") || strings.Contains(errMsg, "failed to set field") {
			t.Errorf("Error message should not contain internal error wrapping: %s", errMsg)
		}

		// Should contain the user-friendly message with the actual range from struct tag
		if !strings.Contains(errMsg, "port must be between 1-65535") {
			t.Errorf("Error message should be user-friendly and use struct tag range: %s", errMsg)
		}
	})

	t.Run("error chain is preserved", func(t *testing.T) {
		type Flags struct {
			Port Port `flag:"port" port:"1-65535"`
		}

		_, err := ParseFlags[Flags]([]string{"--port=99999"})
		if err == nil {
			t.Fatal("Expected error")
		}

		// Should be able to unwrap to get underlying error
		var flagErr *FlagValueError
		if !errors.As(err, &flagErr) {
			t.Fatalf("Expected FlagValueError, got %T", err)
		}

		if flagErr.Unwrap() == nil {
			t.Error("FlagValueError should preserve underlying error")
		}
	})

	t.Run("captures flag name and value", func(t *testing.T) {
		type Flags struct {
			Port Port `flag:"port" port:"1-65535"`
		}

		_, err := ParseFlags[Flags]([]string{"--port=99999"})
		if err == nil {
			t.Fatal("Expected error")
		}

		var flagErr *FlagValueError
		if !errors.As(err, &flagErr) {
			t.Fatalf("Expected FlagValueError, got %T", err)
		}

		if flagErr.FlagName != "port" {
			t.Errorf("FlagName = %q, want %q", flagErr.FlagName, "port")
		}

		if flagErr.Value != "99999" {
			t.Errorf("Value = %q, want %q", flagErr.Value, "99999")
		}
	})

	t.Run("includes subcommand context", func(t *testing.T) {
		type GlobalFlags struct{}
		type RunFlags struct {
			Port Port `flag:"port" port:"1-65535"`
		}
		type Args struct{}

		_, err := ParseWithCommand[GlobalFlags, RunFlags, Args]([]string{"run", "--port=99999"})
		if err == nil {
			t.Fatal("Expected error")
		}

		var flagErr *FlagValueError
		if !errors.As(err, &flagErr) {
			t.Fatalf("Expected FlagValueError, got %T: %v", err, err)
		}

		if flagErr.SubCommand != "run" {
			t.Errorf("SubCommand = %q, want %q", flagErr.SubCommand, "run")
		}
	})

	t.Run("works with pointer types", func(t *testing.T) {
		type Flags struct {
			Port *Port `flag:"port" port:"1-65535"`
		}

		_, err := ParseFlags[Flags]([]string{"--port=0"})
		if err == nil {
			t.Fatal("Expected error")
		}

		var flagErr *FlagValueError
		if !errors.As(err, &flagErr) {
			t.Fatalf("Expected FlagValueError, got %T: %v", err, err)
		}

		if !strings.Contains(err.Error(), "port must be between 1-65535") {
			t.Errorf("Error message should mention port range from struct tag: %s", err.Error())
		}
	})

	t.Run("handles invalid port format", func(t *testing.T) {
		type Flags struct {
			Port Port `flag:"port"`
		}

		_, err := ParseFlags[Flags]([]string{"--port=abc"})
		if err == nil {
			t.Fatal("Expected error")
		}

		var flagErr *FlagValueError
		if !errors.As(err, &flagErr) {
			t.Fatalf("Expected FlagValueError, got %T: %v", err, err)
		}

		if !strings.Contains(err.Error(), "invalid port value") {
			t.Errorf("Error message should mention invalid port value: %s", err.Error())
		}
	})

	t.Run("error message uses struct tag range not hardcoded range", func(t *testing.T) {
		// Regression test: ensure error messages derive from struct tags, not hardcoded "0-65535"
		type Flags1 struct {
			Port Port `flag:"port" port:"1-65535"`
		}
		type Flags2 struct {
			Port Port `flag:"port" port:"8000-9000"`
		}

		// Test with port:"1-65535" - should show "1-65535" in error
		_, err1 := ParseFlags[Flags1]([]string{"--port=99999"})
		if err1 == nil {
			t.Fatal("Expected error for Flags1")
		}
		if !strings.Contains(err1.Error(), "1-65535") {
			t.Errorf("Error message should contain range from struct tag (1-65535), got: %s", err1.Error())
		}
		if strings.Contains(err1.Error(), "0 and 65535") {
			t.Errorf("Error message should not contain hardcoded range (0 and 65535), got: %s", err1.Error())
		}

		// Test with port:"8000-9000" - should show "8000-9000" in error
		_, err2 := ParseFlags[Flags2]([]string{"--port=99999"})
		if err2 == nil {
			t.Fatal("Expected error for Flags2")
		}
		if !strings.Contains(err2.Error(), "8000-9000") {
			t.Errorf("Error message should contain range from struct tag (8000-9000), got: %s", err2.Error())
		}
		if strings.Contains(err2.Error(), "0 and 65535") {
			t.Errorf("Error message should not contain hardcoded range (0 and 65535), got: %s", err2.Error())
		}
	})
}
