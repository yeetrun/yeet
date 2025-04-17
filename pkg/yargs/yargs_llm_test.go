// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yargs

import (
	"strings"
	"testing"
)

// TestGenerateGlobalHelpLLM tests the LLM-optimized global help generation
func TestGenerateGlobalHelpLLM(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"verbose" short:"v" help:"Enable verbose logging"`
		Plain   bool `flag:"plain" help:"Use plain output"`
	}

	config := HelpConfig{
		Command: CommandInfo{
			Name:            "testcli",
			Description:     "Test CLI application",
			LLMInstructions: "This is a test CLI for LLM help generation.",
			Examples: []string{
				"testcli status",
				"testcli run ./app",
			},
		},
		SubCommands: map[string]SubCommandInfo{
			"status": {
				Name:            "status",
				Description:     "Show status",
				LLMInstructions: "Display the current status of all services.",
				Examples: []string{
					"testcli status",
					"testcli status --format=json",
				},
			},
			"run": {
				Name:            "run",
				Description:     "Run a service",
				LLMInstructions: "Deploy and run a service from a payload.",
				Examples: []string{
					"testcli run ./app",
				},
			},
			"hidden": {
				Name:        "hidden",
				Description: "Hidden command",
				Hidden:      true,
			},
		},
		Groups: map[string]GroupInfo{
			"docker": {
				Name:            "docker",
				Description:     "Docker commands",
				LLMInstructions: "Manage Docker containers.",
				Commands: map[string]SubCommandInfo{
					"run": {
						Name:            "run",
						Description:     "Run a container",
						LLMInstructions: "Start a new Docker container.",
					},
					"ps": {
						Name:        "ps",
						Description: "List containers",
					},
				},
			},
			"hiddengroup": {
				Name:        "hiddengroup",
				Description: "Hidden group",
				Hidden:      true,
			},
		},
	}

	output := GenerateGlobalHelpLLM(config, GlobalFlags{})

	// Test markdown structure
	if !strings.Contains(output, "# testcli CLI Reference") {
		t.Error("Missing CLI reference heading")
	}

	// Test LLM instructions present
	if !strings.Contains(output, "## LLM Instructions") {
		t.Error("Missing LLM Instructions section")
	}
	if !strings.Contains(output, "This is a test CLI for LLM help generation") {
		t.Error("Missing command-level LLM instructions")
	}

	// Test global options documented
	if !strings.Contains(output, "## Global Options") {
		t.Error("Missing Global Options section")
	}
	if !strings.Contains(output, "`--verbose`") {
		t.Error("Missing verbose flag")
	}
	if !strings.Contains(output, "`-v`") {
		t.Error("Missing verbose short flag")
	}
	if !strings.Contains(output, "Enable verbose logging") {
		t.Error("Missing flag description")
	}

	// Test commands section
	if !strings.Contains(output, "## Commands") {
		t.Error("Missing Commands section")
	}
	if !strings.Contains(output, "### `status`") {
		t.Error("Missing status command")
	}
	if !strings.Contains(output, "Display the current status of all services") {
		t.Error("Missing status LLM instructions")
	}
	if !strings.Contains(output, "### `run`") {
		t.Error("Missing run command")
	}

	// Test hidden command NOT shown
	if strings.Contains(output, "### `hidden`") {
		t.Error("Hidden command should not appear in help")
	}

	// Test groups section
	if !strings.Contains(output, "## Command Groups") {
		t.Error("Missing Command Groups section")
	}
	if !strings.Contains(output, "### `docker`") {
		t.Error("Missing docker group")
	}
	if !strings.Contains(output, "Manage Docker containers") {
		t.Error("Missing docker group LLM instructions")
	}

	// Test hidden group NOT shown
	if strings.Contains(output, "### `hiddengroup`") {
		t.Error("Hidden group should not appear in help")
	}

	// Test examples included
	if !strings.Contains(output, "testcli status") {
		t.Error("Missing example")
	}

	// Test help references
	if !strings.Contains(output, "Get detailed help: `testcli status --help-llm`") {
		t.Error("Missing command help reference")
	}
	if !strings.Contains(output, "Get detailed help: `testcli docker --help-llm`") {
		t.Error("Missing group help reference")
	}

	// Test footer
	if !strings.Contains(output, "## Getting Help") {
		t.Error("Missing Getting Help section")
	}
}

// TestGenerateGroupHelpLLM tests the LLM-optimized group help generation
func TestGenerateGroupHelpLLM(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"verbose" short:"v" help:"Enable verbose logging"`
	}

	config := HelpConfig{
		Command: CommandInfo{
			Name: "testcli",
		},
		Groups: map[string]GroupInfo{
			"docker": {
				Name:            "docker",
				Description:     "Docker container management",
				LLMInstructions: "Manage Docker containers on the platform.",
				Commands: map[string]SubCommandInfo{
					"run": {
						Name:            "run",
						Description:     "Run a container",
						LLMInstructions: "Deploy a new Docker container.",
						Examples: []string{
							"testcli docker run nginx",
						},
					},
					"ps": {
						Name:        "ps",
						Description: "List containers",
					},
					"hidden": {
						Name:        "hidden",
						Description: "Hidden command",
						Hidden:      true,
					},
				},
			},
		},
	}

	output := GenerateGroupHelpLLM(config, "docker", GlobalFlags{})

	// Test heading
	if !strings.Contains(output, "# testcli - docker") {
		t.Error("Missing group heading")
	}

	// Test description
	if !strings.Contains(output, "Docker container management") {
		t.Error("Missing group description")
	}

	// Test LLM instructions
	if !strings.Contains(output, "## LLM Instructions") {
		t.Error("Missing LLM Instructions section")
	}
	if !strings.Contains(output, "Manage Docker containers on the platform") {
		t.Error("Missing group LLM instructions")
	}

	// Test commands
	if !strings.Contains(output, "## Commands") {
		t.Error("Missing Commands section")
	}
	if !strings.Contains(output, "### `docker run`") {
		t.Error("Missing run command")
	}
	if !strings.Contains(output, "Deploy a new Docker container") {
		t.Error("Missing run command LLM instructions")
	}

	// Test hidden command NOT shown
	if strings.Contains(output, "### `docker hidden`") {
		t.Error("Hidden command should not appear in help")
	}

	// Test global options
	if !strings.Contains(output, "## Global Options") {
		t.Error("Missing Global Options section")
	}
	if !strings.Contains(output, "`--verbose`") {
		t.Error("Missing verbose flag")
	}

	// Test unknown group
	unknownOutput := GenerateGroupHelpLLM(config, "unknown", GlobalFlags{})
	if !strings.Contains(unknownOutput, "# Unknown Group: unknown") {
		t.Error("Should show error for unknown group")
	}
}

// TestGenerateSubCommandHelpLLM tests the LLM-optimized subcommand help generation
func TestGenerateSubCommandHelpLLM(t *testing.T) {
	type GlobalFlags struct {
		Verbose bool `flag:"verbose" short:"v" help:"Enable verbose logging"`
	}

	type RunFlags struct {
		Service string `flag:"service" help:"Service name"`
		Port    int    `flag:"port" short:"p" help:"Port number" default:"8080"`
	}

	type RunArgs struct {
		Payload string `pos:"0" help:"Path to payload"`
	}

	config := HelpConfig{
		Command: CommandInfo{
			Name: "testcli",
		},
		SubCommands: map[string]SubCommandInfo{
			"run": {
				Name:            "run",
				Description:     "Deploy and run a service",
				LLMInstructions: "Deploy a new service from a payload file.",
				Examples: []string{
					"testcli run ./app",
					"testcli run --service=api ./app",
				},
			},
		},
	}

	output := GenerateSubCommandHelpLLM(config, "run", GlobalFlags{}, RunFlags{}, RunArgs{})

	// Test heading
	if !strings.Contains(output, "# testcli run") {
		t.Error("Missing command heading")
	}

	// Test description
	if !strings.Contains(output, "Deploy and run a service") {
		t.Error("Missing description")
	}

	// Test LLM instructions
	if !strings.Contains(output, "## LLM Instructions") {
		t.Error("Missing LLM Instructions section")
	}
	if !strings.Contains(output, "Deploy a new service from a payload file") {
		t.Error("Missing command LLM instructions")
	}

	// Test usage
	if !strings.Contains(output, "## Usage") {
		t.Error("Missing Usage section")
	}
	if !strings.Contains(output, "testcli [GLOBAL_OPTIONS] run <PAYLOAD> [OPTIONS]") {
		t.Error("Missing or incorrect usage string")
	}

	// Test arguments section
	if !strings.Contains(output, "## Arguments") {
		t.Error("Missing Arguments section")
	}
	if !strings.Contains(output, "### `PAYLOAD`") {
		t.Error("Missing PAYLOAD argument")
	}
	if !strings.Contains(output, "Path to payload") {
		t.Error("Missing argument description")
	}
	if !strings.Contains(output, "**Required**: true") {
		t.Error("Missing required indicator")
	}

	// Test options section
	if !strings.Contains(output, "## Options") {
		t.Error("Missing Options section")
	}
	if !strings.Contains(output, "### `--service`") {
		t.Error("Missing service flag")
	}
	if !strings.Contains(output, "Service name") {
		t.Error("Missing service flag description")
	}
	if !strings.Contains(output, "### `--port`") {
		t.Error("Missing port flag")
	}
	if !strings.Contains(output, "`-p`") {
		t.Error("Missing port short flag")
	}
	if !strings.Contains(output, "**Default**: `8080`") {
		t.Error("Missing default value")
	}

	// Test global options
	if !strings.Contains(output, "## Global Options") {
		t.Error("Missing Global Options section")
	}
	if !strings.Contains(output, "`--verbose`") {
		t.Error("Missing verbose flag in global options")
	}

	// Test examples
	if !strings.Contains(output, "## Examples") {
		t.Error("Missing Examples section")
	}
	if !strings.Contains(output, "testcli run ./app") {
		t.Error("Missing example")
	}

	// Test unknown command
	unknownOutput := GenerateSubCommandHelpLLM(config, "unknown", GlobalFlags{}, RunFlags{}, RunArgs{})
	if !strings.Contains(unknownOutput, "# Unknown Command: unknown") {
		t.Error("Should show error for unknown command")
	}
}

// TestErrHelpLLM tests that ErrHelpLLM is returned correctly
func TestErrHelpLLM(t *testing.T) {
	type GlobalFlags struct{}
	type RunFlags struct{}
	type RunArgs struct{}

	config := HelpConfig{
		Command: CommandInfo{
			Name: "testcli",
		},
		SubCommands: map[string]SubCommandInfo{
			"run": {
				Name:        "run",
				Description: "Run command",
			},
		},
	}

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "global help-llm",
			args: []string{"--help-llm"},
		},
		{
			name: "subcommand help-llm",
			args: []string{"run", "--help-llm"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseWithCommandAndHelp[GlobalFlags, RunFlags, RunArgs](tt.args, config)
			if err != ErrHelpLLM {
				t.Errorf("Expected ErrHelpLLM, got %v", err)
			}
			if result == nil {
				t.Error("Expected result with HelpText")
				return
			}
			if result.HelpText == "" {
				t.Error("Expected non-empty HelpText")
			}
		})
	}
}

// TestParseAndHandleHelpLLM tests that ParseAndHandleHelp correctly handles ErrHelpLLM
func TestParseAndHandleHelpLLM(t *testing.T) {
	type GlobalFlags struct{}
	type RunFlags struct{}
	type RunArgs struct{}

	config := HelpConfig{
		Command: CommandInfo{
			Name: "testcli",
		},
		SubCommands: map[string]SubCommandInfo{
			"run": {
				Name:        "run",
				Description: "Run command",
			},
		},
	}

	// Capture stdout would require more setup, so we just verify the function returns ErrShown
	result, err := ParseAndHandleHelp[GlobalFlags, RunFlags, RunArgs]([]string{"--help-llm"}, config)
	if err != ErrShown {
		t.Errorf("Expected ErrShown after handling help, got %v", err)
	}
	if result != nil {
		t.Error("Expected nil result when ErrShown is returned")
	}
}

// TestVariadicArgsInLLMHelp tests that variadic arguments are properly documented
func TestVariadicArgsInLLMHelp(t *testing.T) {
	type GlobalFlags struct{}
	type StartFlags struct{}
	type StartArgs struct {
		Services []string `pos:"0+" help:"Service names to start"`
	}

	config := HelpConfig{
		Command: CommandInfo{
			Name: "testcli",
		},
		SubCommands: map[string]SubCommandInfo{
			"start": {
				Name:        "start",
				Description: "Start services",
			},
		},
	}

	output := GenerateSubCommandHelpLLM(config, "start", GlobalFlags{}, StartFlags{}, StartArgs{})

	// Test variadic argument notation in usage
	if !strings.Contains(output, "<SERVICES...>") {
		t.Error("Missing variadic argument notation in usage")
	}

	// Test variadic indicator in arguments section
	if !strings.Contains(output, "**Variadic**: true") {
		t.Error("Missing variadic indicator")
	}
	if !strings.Contains(output, "minimum: 1") {
		t.Error("Missing minimum count for variadic argument")
	}
}
